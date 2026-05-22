package app

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

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
	return stripThinkingSuffix(name)
}

func isHobbyTier(tier string) bool {
	return strings.EqualFold(strings.TrimSpace(tier), "hobby")
}

func isTeamLikeTier(tier string) bool {
	return !isHobbyTier(tier)
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
		case isHobbyTier(k.Tier):
			hobby = append(hobby, c)
		case isTeamLikeTier(k.Tier):
			team = append(team, c)
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
