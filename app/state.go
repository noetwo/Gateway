package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func loadState(path string, cooldown float64) (*AppState, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	state := &AppState{filePath: path, Cooldown: cooldown, Keys: map[string]*Key{}}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		state.normalizeRetrySettings()
		state.normalizeHobbyBlockedModels()
		state.normalizeTierRoutingSettings()
		if err := state.save(); err != nil {
			return nil, err
		}
		return state, nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, state); err != nil {
			return nil, err
		}
	}
	if state.Keys == nil {
		state.Keys = map[string]*Key{}
	}
	state.filePath = path
	if state.Cooldown <= 0 {
		state.Cooldown = cooldown
	}
	state.normalizeRetrySettings()
	state.normalizeHobbyBlockedModels()
	state.normalizeTierRoutingSettings()

	for _, k := range state.Keys {
		roll30DayWindow(k)
	}
	if err := state.save(); err != nil {
		return nil, err
	}
	return state, nil
}

func (s *AppState) save() error {
	s.LastSaved = time.Now().UTC()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.filePath)
}

func normalizeRetryStatusCodes(raw string) (string, error) {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	seen := map[string]bool{}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if p == "5xx" {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
			continue
		}
		code, err := strconv.Atoi(p)
		if err != nil || code < 100 || code > 599 {
			return "", fmt.Errorf("invalid retry status code: %s", p)
		}
		normalized := strconv.Itoa(code)
		if !seen[normalized] {
			seen[normalized] = true
			out = append(out, normalized)
		}
	}
	if len(out) == 0 {
		return "", errors.New("retry status codes cannot be empty")
	}
	return strings.Join(out, ","), nil
}

func parseModelPatternsText(raw string) []string {
	return normalizeModelPatterns([]string{raw}, false)
}

func normalizePreferredTier(raw string) string {
	tier := strings.ToLower(strings.TrimSpace(raw))
	switch tier {
	case "team", "hobby":
		return tier
	default:
		return defaultPreferredTier
	}
}

func clampRetryAttempts(n int) int {
	if n <= 0 {
		return defaultMaxRetryAttempts
	}
	if n > maxAllowedRetryAttempts {
		return maxAllowedRetryAttempts
	}
	return n
}

func (s *AppState) normalizeRetrySettings() {
	codes, err := normalizeRetryStatusCodes(s.RetryCodes)
	if err != nil {
		codes = defaultRetryStatusCodes
	}
	s.RetryCodes = codes
	s.MaxAttempts = clampRetryAttempts(s.MaxAttempts)
}

func (s *AppState) retrySettings() (string, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	codes := s.RetryCodes
	if strings.TrimSpace(codes) == "" {
		codes = defaultRetryStatusCodes
	}
	return codes, clampRetryAttempts(s.MaxAttempts)
}

func retryStatusEnabled(code int, raw string) bool {
	parts := strings.Split(raw, ",")
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p == "" {
			continue
		}
		if p == "5xx" && code >= 500 && code <= 599 {
			return true
		}
		n, err := strconv.Atoi(p)
		if err == nil && n == code {
			return true
		}
	}
	return false
}

func normalizeModelPatterns(patterns []string, useDefault bool) []string {
	if useDefault && len(patterns) == 0 {
		patterns = strings.Split(defaultHobbyBlocked, ",")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(patterns))
	for _, raw := range patterns {
		for _, p := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t'
		}) {
			p = normalizeModelName(p)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func (s *AppState) normalizeHobbyBlockedModels() {
	if len(s.HobbyBlocked) == 0 && len(s.TeamOnly) > 0 {
		s.HobbyBlocked = s.TeamOnly
	}
	s.HobbyBlocked = normalizeModelPatterns(s.HobbyBlocked, s.HobbyBlocked == nil)
	s.TeamOnly = nil
}

func (s *AppState) hobbyBlockedSettings() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.HobbyBlocked...)
}

func (s *AppState) normalizeTierRoutingSettings() {
	s.PreferredTier = normalizePreferredTier(s.PreferredTier)
	s.TeamPriority = normalizeModelPatterns(s.TeamPriority, false)
	s.HobbyPriority = normalizeModelPatterns(s.HobbyPriority, false)
}

func (s *AppState) tierRoutingSettings() (string, []string, []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return normalizePreferredTier(s.PreferredTier), append([]string(nil), s.TeamPriority...), append([]string(nil), s.HobbyPriority...)
}

func normalizeModelName(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" {
		return ""
	}
	for _, suffix := range []string{"-thinking-high", "-thinking-mid", "-thinking-max", "-thinking-low", "-thinking"} {
		name = strings.TrimSuffix(name, suffix)
	}
	return name
}

func isHobbyTier(tier string) bool {
	return strings.EqualFold(strings.TrimSpace(tier), "hobby")
}

func isTeamTier(tier string) bool {
	return strings.EqualFold(strings.TrimSpace(tier), "team")
}

func modelMatchesAnyPattern(model string, patterns []string) bool {
	return modelBlockedForHobby(model, patterns)
}

func preferredTierForModel(model, defaultTier string, teamPriority, hobbyPriority []string, hobbyBlocked bool) string {
	if hobbyBlocked || modelMatchesAnyPattern(model, teamPriority) {
		return "team"
	}
	if modelMatchesAnyPattern(model, hobbyPriority) {
		return "hobby"
	}
	return normalizePreferredTier(defaultTier)
}

func selectProxyCandidatePool(team, hobby []proxyCandidate, preferredTier string, hobbyBlocked bool) []proxyCandidate {
	if preferredTier == "hobby" && !hobbyBlocked {
		if len(hobby) > 0 {
			return hobby
		}
		return team
	}
	if len(team) > 0 {
		return team
	}
	if hobbyBlocked {
		return nil
	}
	return hobby
}

func rotateProxyCandidates(list []proxyCandidate, turn uint64) []proxyCandidate {
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	if len(list) == 0 {
		return list
	}
	start := int(turn % uint64(len(list)))
	result := make([]proxyCandidate, 0, len(list))
	result = append(result, list[start:]...)
	result = append(result, list[:start]...)
	return result
}

func modelBlockedForHobby(model string, patterns []string) bool {
	name := normalizeModelName(model)
	if name == "" {
		return false
	}
	bare := name
	if idx := strings.Index(bare, "/"); idx >= 0 {
		bare = bare[idx+1:]
	}
	for _, raw := range patterns {
		p := normalizeModelName(raw)
		if p == "" {
			continue
		}
		pBare := p
		if idx := strings.Index(pBare, "/"); idx >= 0 {
			pBare = pBare[idx+1:]
		}
		if strings.HasSuffix(p, "*") {
			prefix := strings.TrimSuffix(p, "*")
			prefixBare := strings.TrimSuffix(pBare, "*")
			if strings.HasPrefix(name, prefix) || strings.HasPrefix(bare, prefixBare) {
				return true
			}
			continue
		}
		if name == p || bare == pBare || strings.HasPrefix(name, p) || strings.HasPrefix(bare, pBare) {
			return true
		}
	}
	return false
}

func (s *AppState) nextProxyCandidates(model string) []proxyCandidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	hobbyBlocked := modelBlockedForHobby(model, s.HobbyBlocked)
	preferredTier := preferredTierForModel(model, s.PreferredTier, s.TeamPriority, s.HobbyPriority, hobbyBlocked)
	team := make([]proxyCandidate, 0, len(s.Keys))
	hobby := make([]proxyCandidate, 0, len(s.Keys))
	for _, k := range s.Keys {
		roll30DayWindow(k)
		if k.Scrapped || k.Paused || strings.TrimSpace(k.APIKey) == "" {
			continue
		}
		c := proxyCandidate{ID: k.ID, APIKey: k.APIKey, Tier: k.Tier}
		switch {
		case isTeamTier(k.Tier):
			team = append(team, c)
		case isHobbyTier(k.Tier):
			hobby = append(hobby, c)
		}
	}
	list := selectProxyCandidatePool(team, hobby, preferredTier, hobbyBlocked)
	if len(list) == 0 {
		return list
	}

	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	if s.StickyMode {
		stickyIdx := -1
		if s.StickyKeyID != "" {
			for i, c := range list {
				if c.ID == s.StickyKeyID {
					stickyIdx = i
					break
				}
			}
		}
		if stickyIdx < 0 {
			stickyIdx = 0
			s.StickyKeyID = list[0].ID
			_ = s.save()
		}
		result := make([]proxyCandidate, 0, len(list))
		result = append(result, list[stickyIdx])
		for i, c := range list {
			if i != stickyIdx {
				result = append(result, c)
			}
		}
		return result
	}

	turn := s.proxyTurn
	s.proxyTurn++
	_ = s.save()
	return rotateProxyCandidates(list, turn)
}

func (s *AppState) markProxySuccess(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.Keys[id]
	if k == nil {
		return
	}
	now := time.Now().UTC()
	k.LastProxyAt = now
	k.ProxyReqCount++
	k.LastErr = ""
	k.ConsecFails = 0
	k.UpdatedAt = now
	if s.StickyMode && s.StickyKeyID == "" {
		s.StickyKeyID = id
	}
	_ = s.save()
}

func (s *AppState) markProxyFailure(id, msg string, statusCode int, cfg Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.Keys[id]
	if k == nil {
		return
	}
	now := time.Now().UTC()
	k.LastProxyAt = now
	k.LastErr = msg
	k.UpdatedAt = now

	switch {
	case retryStatusEnabled(statusCode, cfg.CooldownStatusCodes):
		k.ConsecFails = 0
		k.Paused = true
		k.LastErr = fmt.Sprintf("%d 命中冷却策略，自动冷却", statusCode)
		if s.StickyMode && s.StickyKeyID == id {
			s.StickyKeyID = ""
		}
		log.Printf("[防打断] key %s (%s) status=%d 命中冷却策略，进入冷却", k.Name, id, statusCode)

	case retryStatusEnabled(statusCode, cfg.ProxyScrapStatusCodes):
		k.ConsecFails++
		threshold := cfg.ProxyScrapFailThreshold
		if threshold <= 0 {
			threshold = 3
		}
		if k.ConsecFails >= threshold {
			k.Scrapped = true
			k.ScrappedErr = fmt.Sprintf("连续%d次%d错误，自动报废: %s", k.ConsecFails, statusCode, msg)
			k.ScrappedAt = now
			k.Paused = true
			if s.StickyMode && s.StickyKeyID == id {
				s.StickyKeyID = ""
			}
			s.renumberKeys()
			log.Printf("[防打断] key %s (%s) 连续%d次%d，自动报废", k.Name, id, k.ConsecFails, statusCode)
		} else {
			log.Printf("[防打断] key %s (%s) %d错误 (%d/%d)，切换下一个", k.Name, id, statusCode, k.ConsecFails, threshold)
		}

	case statusCode == http.StatusForbidden:
		k.ConsecFails = 0
		log.Printf("[防打断] key %s (%s) 403上游拒绝，切换下一个", k.Name, id)

	default:
		k.ConsecFails = 0
		log.Printf("[防打断] key %s (%s) status=%d，切换下一个", k.Name, id, statusCode)
	}

	_ = s.save()
}

func publicView(k *Key) PublicKeyView {
	ref := k.LastProxyAt
	if ref.IsZero() {
		ref = k.CreatedAt
	}
	resetAt := ref.Add(30 * 24 * time.Hour)
	return PublicKeyView{
		ID:              k.ID,
		Name:            k.Name,
		Tier:            k.Tier,
		MonthlySpentUSD: round2(k.MonthlySpentUSD),
		Paused:          k.Paused,
		LastBalance:     round2(k.LastBalance),
		LastUsedTotal:   round2(k.LastUsedTotal),
		LastPollAt:      k.LastPollAt,
		LastProxyAt:     k.LastProxyAt,
		LastErr:         k.LastErr,
		RequestCount:    k.RequestCount,
		ProxyReqCount:   k.ProxyReqCount,
		CreatedAt:       k.CreatedAt,
		UpdatedAt:       k.UpdatedAt,
		ResetAt:         resetAt,
		Scrapped:        k.Scrapped,
		ScrappedErr:     k.ScrappedErr,
		ScrappedAt:      k.ScrappedAt,
		ConsecFails:     k.ConsecFails,
	}
}

func roll30DayWindow(k *Key) {
	ref := k.LastProxyAt
	if ref.IsZero() {
		ref = k.CreatedAt
	}
	if time.Since(ref) >= 30*24*time.Hour {
		k.MonthlySpentUSD = 0
		k.Paused = false
	}
}

// renumberKeys 只对自动编号的 key 重排（活跃池里 "N"、报废池里 "报废N"）。
// 自定义名称（导入时带的）和空名称一概不动。
func (s *AppState) renumberKeys() {
	type pair struct {
		id  string
		num int
	}
	var active, scrapped []pair
	for id, k := range s.Keys {
		raw := k.Name
		if k.Scrapped {
			raw = strings.TrimPrefix(raw, "报废")
		}
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			continue
		}
		if k.Scrapped {
			scrapped = append(scrapped, pair{id, n})
		} else {
			active = append(active, pair{id, n})
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].num < active[j].num })
	sort.Slice(scrapped, func(i, j int) bool { return scrapped[i].num < scrapped[j].num })
	for i, p := range active {
		s.Keys[p.id].Name = strconv.Itoa(i + 1)
	}
	for i, p := range scrapped {
		s.Keys[p.id].Name = "报废" + strconv.Itoa(i+1)
	}
}

func (s *AppState) keyIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.Keys))
	for id := range s.Keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (s *AppState) activeKeyIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.Keys))
	for id, k := range s.Keys {
		if !k.Scrapped {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func fetchCredits(baseURL, apiKey string) (*struct {
	Balance   float64
	TotalUsed float64
}, error) {
	req, err := http.NewRequest(http.MethodGet, baseURL+"/credits", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("credits status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var cr CreditsResp
	if err := json.Unmarshal(b, &cr); err != nil {
		return nil, err
	}
	balance, err := strconv.ParseFloat(strings.TrimSpace(cr.Balance), 64)
	if err != nil {
		return nil, err
	}
	totalUsed, err := strconv.ParseFloat(strings.TrimSpace(cr.TotalUsed), 64)
	if err != nil {
		return nil, err
	}
	return &struct {
		Balance   float64
		TotalUsed float64
	}{Balance: balance, TotalUsed: totalUsed}, nil
}

func pollOne(cfg Config, state *AppState, id string) error {
	state.mu.Lock()
	k, ok := state.Keys[id]
	if !ok {
		state.mu.Unlock()
		return errors.New("key not found")
	}
	roll30DayWindow(k)
	wasPaused := k.Paused
	apiKey := k.APIKey
	prev := k.LastUsedTotal
	state.mu.Unlock()

	credits, err := fetchCredits(cfg.GatewayBaseURL, apiKey)
	now := time.Now().UTC()

	state.mu.Lock()
	defer state.mu.Unlock()
	k2, ok := state.Keys[id]
	if !ok {
		return errors.New("key not found")
	}
	roll30DayWindow(k2)
	k2.LastPollAt = now
	k2.UpdatedAt = now
	if err != nil {
		k2.LastErr = err.Error()
		statusCode := statusCodeFromError(err.Error())
		if retryStatusEnabled(statusCode, cfg.RefreshScrapStatusCodes) {
			k2.Scrapped = true
			k2.ScrappedErr = err.Error()
			k2.ScrappedAt = now
			k2.Paused = true
			if state.StickyMode && state.StickyKeyID == id {
				state.StickyKeyID = ""
			}
			state.renumberKeys()
		} else if retryStatusEnabled(statusCode, cfg.RefreshCooldownStatusCodes) {
			k2.Paused = true
			if state.StickyMode && state.StickyKeyID == id {
				state.StickyKeyID = ""
			}
		}
		_ = state.save()
		return err
	}
	k2.LastErr = ""
	k2.RequestCount++
	k2.LastBalance = credits.Balance
	k2.LastUsedTotal = credits.TotalUsed
	if !wasPaused {
		if credits.TotalUsed > prev {
			k2.MonthlySpentUSD += credits.TotalUsed - prev
		}
		if k2.MonthlySpentUSD >= state.Cooldown {
			k2.Paused = true
			if state.StickyMode && state.StickyKeyID == id {
				state.StickyKeyID = ""
			}
		}
	}
	return state.save()
}

func statusCodeFromError(msg string) int {
	idx := strings.Index(msg, "status=")
	if idx < 0 {
		return 0
	}
	rest := msg[idx+len("status="):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0
	}
	return n
}
