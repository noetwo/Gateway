package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func handleGetState(state *AppState, rtCfg *RuntimeConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := rtCfg.Get()
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		state.mu.RLock()
		allKeys := make([]PublicKeyView, 0, len(state.Keys))
		for _, k := range state.Keys {
			allKeys = append(allKeys, publicView(k))
		}
		state.mu.RUnlock()
		activeKeys := make([]PublicKeyView, 0)
		cooldownKeys := make([]PublicKeyView, 0)
		scrapKeys := make([]PublicKeyView, 0)
		for _, k := range allKeys {
			switch {
			case k.Scrapped:
				scrapKeys = append(scrapKeys, k)
			case k.Paused:
				cooldownKeys = append(cooldownKeys, k)
			default:
				activeKeys = append(activeKeys, k)
			}
		}
		// 排序：数字名按值升序，自定义名按字典序，数字优先排前面。
		byName := func(s []PublicKeyView) {
			sort.Slice(s, func(i, j int) bool {
				ai := strings.TrimPrefix(s[i].Name, "报废")
				aj := strings.TrimPrefix(s[j].Name, "报废")
				ni, ei := strconv.Atoi(ai)
				nj, ej := strconv.Atoi(aj)
				switch {
				case ei == nil && ej == nil:
					return ni < nj
				case ei == nil:
					return true
				case ej == nil:
					return false
				default:
					return ai < aj
				}
			})
		}
		byName(activeKeys)
		byName(cooldownKeys)
		byName(scrapKeys)
		var totalBalance, totalUsed, totalMonthly, activeBalance, overdraft float64
		for _, k := range activeKeys {
			totalBalance += k.LastBalance
			totalUsed += k.LastUsedTotal
			totalMonthly += k.MonthlySpentUSD
			activeBalance += k.LastBalance
			if k.LastBalance < 0 {
				overdraft += k.LastBalance
			}
		}
		for _, k := range cooldownKeys {
			totalBalance += k.LastBalance
			totalUsed += k.LastUsedTotal
			totalMonthly += k.MonthlySpentUSD
			if k.LastBalance < 0 {
				overdraft += k.LastBalance
			}
		}
		activeCount := len(activeKeys)
		activeQuota := cfg.MonthlyQuotaPerKey * float64(activeCount)
		stickyKeyName := ""
		state.mu.RLock()
		stickyMode := state.StickyMode
		stickyKeyID := state.StickyKeyID
		if stickyMode && stickyKeyID != "" {
			if sk, ok := state.Keys[stickyKeyID]; ok {
				stickyKeyName = sk.Name
			}
		}
		state.mu.RUnlock()
		retryCodes, maxRetryAttempts := state.retrySettings()
		hobbyBlockedModels := state.hobbyBlockedSettings()
		preferredTier, teamPriorityModels, hobbyPriorityModels := state.tierRoutingSettings()
		writeJSON(w, http.StatusOK, map[string]any{
			"cooldown_usd":          state.Cooldown,
			"keys":                  activeKeys,
			"cooldown_keys":         cooldownKeys,
			"scrap_keys":            scrapKeys,
			"active_count":          activeCount,
			"cooldown_count":        len(cooldownKeys),
			"scrap_count":           len(scrapKeys),
			"total_count":           activeCount + len(cooldownKeys),
			"month":                 time.Now().UTC().Format("2006-01"),
			"total_balance":         round2(totalBalance),
			"total_used":            round2(totalUsed),
			"total_monthly":         round2(totalMonthly),
			"overdraft":             round2(overdraft),
			"active_balance":        round2(activeBalance),
			"active_quota":          round2(activeQuota),
			"monthly_quota_per_key": cfg.MonthlyQuotaPerKey,
			"sticky_mode":           stickyMode,
			"sticky_key_id":         stickyKeyID,
			"sticky_key_name":       stickyKeyName,
			"retry_status_codes":    retryCodes,
			"max_retry_attempts":    maxRetryAttempts,
			"hobby_blocked_models":  hobbyBlockedModels,
			"preferred_tier":        preferredTier,
			"team_priority_models":  teamPriorityModels,
			"hobby_priority_models": hobbyPriorityModels,
		})
	}
}

func handleSettings(state *AppState) http.HandlerFunc {
	type settingsReq struct {
		StatusCodes         string   `json:"status_codes"`
		MaxAttempts         int      `json:"max_attempts"`
		HobbyBlockedModels  []string `json:"hobby_blocked_models"`
		HobbyBlockedText    *string  `json:"hobby_blocked_text"`
		PreferredTier       *string  `json:"preferred_tier"`
		TeamPriorityModels  []string `json:"team_priority_models"`
		TeamPriorityText    *string  `json:"team_priority_text"`
		HobbyPriorityModels []string `json:"hobby_priority_models"`
		HobbyPriorityText   *string  `json:"hobby_priority_text"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch r.Method {
		case http.MethodGet:
			codes, maxAttempts := state.retrySettings()
			hobbyBlockedModels := state.hobbyBlockedSettings()
			preferredTier, teamPriorityModels, hobbyPriorityModels := state.tierRoutingSettings()
			writeJSON(w, http.StatusOK, map[string]any{
				"retry_status_codes":    codes,
				"max_retry_attempts":    maxAttempts,
				"hobby_blocked_models":  hobbyBlockedModels,
				"preferred_tier":        preferredTier,
				"team_priority_models":  teamPriorityModels,
				"hobby_priority_models": hobbyPriorityModels,
			})
		case http.MethodPost:
			var req settingsReq
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
				return
			}
			codes, err := normalizeRetryStatusCodes(req.StatusCodes)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			maxAttempts := clampRetryAttempts(req.MaxAttempts)
			state.mu.Lock()
			state.RetryCodes = codes
			state.MaxAttempts = maxAttempts
			if req.HobbyBlockedText != nil {
				state.HobbyBlocked = parseModelPatternsText(*req.HobbyBlockedText)
			} else if req.HobbyBlockedModels != nil {
				state.HobbyBlocked = normalizeModelPatterns(req.HobbyBlockedModels, false)
			}
			if req.PreferredTier != nil {
				state.PreferredTier = normalizePreferredTier(*req.PreferredTier)
			}
			if req.TeamPriorityText != nil {
				state.TeamPriority = parseModelPatternsText(*req.TeamPriorityText)
			} else if req.TeamPriorityModels != nil {
				state.TeamPriority = normalizeModelPatterns(req.TeamPriorityModels, false)
			}
			if req.HobbyPriorityText != nil {
				state.HobbyPriority = parseModelPatternsText(*req.HobbyPriorityText)
			} else if req.HobbyPriorityModels != nil {
				state.HobbyPriority = normalizeModelPatterns(req.HobbyPriorityModels, false)
			}
			state.normalizeTierRoutingSettings()
			state.TeamOnly = nil
			hobbyBlockedModels := append([]string(nil), state.HobbyBlocked...)
			preferredTier := state.PreferredTier
			teamPriorityModels := append([]string(nil), state.TeamPriority...)
			hobbyPriorityModels := append([]string(nil), state.HobbyPriority...)
			err = state.save()
			state.mu.Unlock()
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"retry_status_codes":    codes,
				"max_retry_attempts":    maxAttempts,
				"hobby_blocked_models":  hobbyBlockedModels,
				"preferred_tier":        preferredTier,
				"team_priority_models":  teamPriorityModels,
				"hobby_priority_models": hobbyPriorityModels,
			})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}

func handleRetrySettings(state *AppState) http.HandlerFunc {
	return handleSettings(state)
}

func handleConfig(rtCfg *RuntimeConfig, state *AppState) http.HandlerFunc {
	type configReq struct {
		ListenAddr                 string   `json:"listen_addr"`
		GatewayBaseURL             string   `json:"gateway_base_url"`
		CooldownUSD                float64  `json:"monthly_cooldown_usd"`
		WebAuthToken               string   `json:"web_auth_token"`
		ApiAuthTokens              []string `json:"api_auth_tokens"`
		DefaultReasoning           string   `json:"default_reasoning_effort"`
		DebugDumpDir               string   `json:"debug_dump_dir"`
		DebugEnabled               *bool    `json:"debug_enabled"`
		PassthroughOnly            bool     `json:"passthrough_only"`
		MonthlyQuotaPerKey         float64  `json:"monthly_quota_per_key"`
		ProviderOrder              string   `json:"provider_order"`
		CooldownStatusCodes        string   `json:"cooldown_status_codes"`
		ProxyScrapStatusCodes      string   `json:"proxy_scrap_status_codes"`
		ProxyScrapFailThreshold    int      `json:"proxy_scrap_fail_threshold"`
		RefreshScrapStatusCodes    string   `json:"refresh_scrap_status_codes"`
		RefreshCooldownStatusCodes string   `json:"refresh_cooldown_status_codes"`
	}
	view := func(cfg Config) map[string]any {
		return map[string]any{
			"listen_addr":                   cfg.ListenAddr,
			"gateway_base_url":              cfg.GatewayBaseURL,
			"monthly_cooldown_usd":          cfg.CooldownUSD,
			"web_auth_token":                cfg.WebAuthToken,
			"api_auth_tokens":               cfg.ApiAuthTokens,
			"default_reasoning_effort":      cfg.DefaultReasoning,
			"debug_dump_dir":                cfg.DebugDumpDir,
			"debug_enabled":                 cfg.DebugEnabled,
			"passthrough_only":              cfg.PassthroughOnly,
			"monthly_quota_per_key":         cfg.MonthlyQuotaPerKey,
			"provider_order":                cfg.ProviderOrder,
			"cooldown_status_codes":         cfg.CooldownStatusCodes,
			"proxy_scrap_status_codes":      cfg.ProxyScrapStatusCodes,
			"proxy_scrap_fail_threshold":    cfg.ProxyScrapFailThreshold,
			"refresh_scrap_status_codes":    cfg.RefreshScrapStatusCodes,
			"refresh_cooldown_status_codes": cfg.RefreshCooldownStatusCodes,
			"runtime_config_path":           cfg.RuntimeConfigPath,
			"listen_addr_restart_required":  true,
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, view(rtCfg.Get()))
		case http.MethodPost:
			var req configReq
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
				return
			}
			cfg := rtCfg.Get()
			cfg.ListenAddr = strings.TrimSpace(req.ListenAddr)
			cfg.GatewayBaseURL = strings.TrimSpace(req.GatewayBaseURL)
			cfg.CooldownUSD = req.CooldownUSD
			cfg.WebAuthToken = strings.TrimSpace(req.WebAuthToken)
			cfg.ApiAuthTokens = cleanTokens(req.ApiAuthTokens)
			cfg.DefaultReasoning = strings.TrimSpace(req.DefaultReasoning)
			cfg.DebugDumpDir = fixedDebugDir()
			if req.DebugEnabled != nil {
				cfg.DebugEnabled = *req.DebugEnabled
			}
			cfg.PassthroughOnly = req.PassthroughOnly
			cfg.MonthlyQuotaPerKey = req.MonthlyQuotaPerKey
			cfg.ProviderOrder = strings.TrimSpace(req.ProviderOrder)
			cfg.CooldownStatusCodes = strings.TrimSpace(req.CooldownStatusCodes)
			cfg.ProxyScrapStatusCodes = strings.TrimSpace(req.ProxyScrapStatusCodes)
			cfg.ProxyScrapFailThreshold = req.ProxyScrapFailThreshold
			cfg.RefreshScrapStatusCodes = strings.TrimSpace(req.RefreshScrapStatusCodes)
			cfg.RefreshCooldownStatusCodes = strings.TrimSpace(req.RefreshCooldownStatusCodes)
			if cfg.WebAuthToken == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "web auth token cannot be empty"})
				return
			}
			saved, err := rtCfg.Update(cfg)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			state.mu.Lock()
			state.Cooldown = saved.CooldownUSD
			err = state.save()
			state.mu.Unlock()
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, view(saved))
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}

func handleExportKeys(state *AppState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		state.mu.RLock()
		keys := make([]*Key, 0, len(state.Keys))
		for _, k := range state.Keys {
			keys = append(keys, k)
		}
		state.mu.RUnlock()
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].Name == keys[j].Name {
				return keys[i].ID < keys[j].ID
			}
			return keys[i].Name < keys[j].Name
		})
		var b strings.Builder
		for _, k := range keys {
			b.WriteString(strings.ReplaceAll(k.Name, "\t", " "))
			b.WriteByte('\t')
			b.WriteString(strings.ReplaceAll(k.Tier, "\t", " "))
			b.WriteByte('\t')
			b.WriteString(k.APIKey)
			b.WriteByte('\n')
		}
		w.Header().Set("Content-Type", "text/tab-separated-values; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="keys.tsv"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(b.String()))
	}
}

func handleRefresh(rtCfg *RuntimeConfig, state *AppState) http.HandlerFunc {
	type refreshReq struct {
		ID string `json:"id"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req refreshReq
		cfg := rtCfg.Get()
		if r.ContentLength > 0 {
			_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req)
		}
		id := strings.TrimSpace(req.ID)
		errs := map[string]string{}
		okCount := 0

		if id != "" {
			if err := pollOne(cfg, state, id); err != nil {
				errs[id] = err.Error()
			} else {
				okCount++
			}
		} else {
			ids := state.activeKeyIDs()
			var wg sync.WaitGroup
			var mu sync.Mutex
			sem := make(chan struct{}, 10)
			for _, kid := range ids {
				wg.Add(1)
				sem <- struct{}{}
				go func(id string) {
					defer wg.Done()
					defer func() { <-sem }()
					if err := pollOne(cfg, state, id); err != nil {
						mu.Lock()
						errs[id] = err.Error()
						mu.Unlock()
						return
					}
					mu.Lock()
					okCount++
					mu.Unlock()
				}(kid)
			}
			wg.Wait()
		}
		writeJSON(w, http.StatusOK, map[string]any{"refreshed": okCount, "errors": errs})
	}
}

func handleStickyToggle(state *AppState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		state.StickyMode = !state.StickyMode
		if state.StickyMode {
			if state.StickyKeyID == "" {
				for _, k := range state.Keys {
					if !k.Scrapped && !k.Paused && strings.TrimSpace(k.APIKey) != "" {
						state.StickyKeyID = k.ID
						break
					}
				}
			}
		} else {
			state.StickyKeyID = ""
		}
		if err := state.save(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		stickyName := ""
		if state.StickyMode && state.StickyKeyID != "" {
			if k, ok := state.Keys[state.StickyKeyID]; ok {
				stickyName = k.Name
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"sticky_mode":     state.StickyMode,
			"sticky_key_id":   state.StickyKeyID,
			"sticky_key_name": stickyName,
		})
	}
}

func handleStickySelect(state *AppState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		k, ok := state.Keys[req.ID]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "key not found"})
			return
		}
		if k.Scrapped || k.Paused {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "该 key 已冷却或报废，无法锁定"})
			return
		}
		state.StickyMode = true
		state.StickyKeyID = req.ID
		if err := state.save(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"sticky_mode":     true,
			"sticky_key_id":   req.ID,
			"sticky_key_name": k.Name,
		})
	}
}

func handleKeys(state *AppState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req CreateKeyReq
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		apiKey := strings.TrimSpace(req.APIKey)
		if apiKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "api_key required"})
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		name := strings.TrimSpace(req.Name)
		tier := strings.TrimSpace(req.Tier)
		// 前端用 name="auto" 表示「没提供名称，请自动编号」（兼容旧行为）；
		// 显式给名称/等级时按给的来，name 可以为空字符串。
		if name == "auto" {
			maxNum := 0
			for _, k := range state.Keys {
				n, err := strconv.Atoi(k.Name)
				if err == nil && n > maxNum && !k.Scrapped {
					maxNum = n
				}
			}
			name = strconv.Itoa(maxNum + 1)
		}
		now := time.Now().UTC()
		id := strconv.FormatInt(now.UnixNano(), 36)
		state.Keys[id] = &Key{ID: id, Name: name, Tier: tier, APIKey: apiKey, CreatedAt: now, UpdatedAt: now}
		if err := state.save(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, publicView(state.Keys[id]))
	}
}

// handleKeysBulk 接受多行文本，每行格式：[name] [tier] vck_xxx
// 也兼容纯 key 一行；同一行多个 vck_ 时只有第一个能带 name/tier，其余字段留空。
func handleKeysBulk(state *AppState) http.HandlerFunc {
	vckRE := regexp.MustCompile(`^vck_[A-Za-z0-9]+$`)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req struct {
			Raw string `json:"raw"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}

		type parsedKey struct{ name, tier, apiKey string }
		var parsed []parsedKey
		for _, line := range strings.Split(req.Raw, "\n") {
			parts := strings.Fields(line)
			if len(parts) == 0 {
				continue
			}
			firstIdx := -1
			for i, p := range parts {
				if vckRE.MatchString(p) {
					firstIdx = i
					break
				}
			}
			if firstIdx < 0 {
				continue
			}
			name, tier := "", ""
			before := parts[:firstIdx]
			if len(before) >= 1 {
				name = before[0]
			}
			if len(before) >= 2 {
				tier = before[1]
			}
			parsed = append(parsed, parsedKey{name, tier, parts[firstIdx]})
			for i := firstIdx + 1; i < len(parts); i++ {
				if vckRE.MatchString(parts[i]) {
					parsed = append(parsed, parsedKey{"", "", parts[i]})
				}
			}
		}
		if len(parsed) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no vck_ keys found"})
			return
		}

		state.mu.Lock()
		defer state.mu.Unlock()
		added := 0
		now := time.Now().UTC()
		for _, pk := range parsed {
			dup := false
			for _, k := range state.Keys {
				if k.APIKey == pk.apiKey {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			id := strconv.FormatInt(now.UnixNano()+int64(added), 36)
			state.Keys[id] = &Key{ID: id, Name: pk.name, Tier: pk.tier, APIKey: pk.apiKey, CreatedAt: now, UpdatedAt: now}
			added++
		}
		if err := state.save(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"added": added, "total_found": len(parsed)})
	}
}

func handleKeysBatch(state *AppState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req struct {
			Action string   `json:"action"`
			IDs    []string `json:"ids"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		if len(req.IDs) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ids required"})
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		now := time.Now().UTC()
		processed := 0
		missing := 0
		switch req.Action {
		case "delete":
			for _, id := range req.IDs {
				if _, ok := state.Keys[id]; ok {
					delete(state.Keys, id)
					if state.StickyKeyID == id {
						state.StickyKeyID = ""
					}
					processed++
				} else {
					missing++
				}
			}
			state.renumberKeys()
		case "scrap":
			for _, id := range req.IDs {
				k, ok := state.Keys[id]
				if !ok {
					missing++
					continue
				}
				if !k.Scrapped {
					k.Scrapped = true
					k.ScrappedErr = "手动报废"
					k.ScrappedAt = now
					k.Paused = true
					k.UpdatedAt = now
					if state.StickyKeyID == id {
						state.StickyKeyID = ""
					}
				}
				processed++
			}
			state.renumberKeys()
		case "restore":
			for _, id := range req.IDs {
				k, ok := state.Keys[id]
				if !ok {
					missing++
					continue
				}
				if k.Scrapped {
					k.Scrapped = false
					k.ScrappedErr = ""
					k.ScrappedAt = time.Time{}
					k.Paused = false
					k.MonthlySpentUSD = 0
					k.ConsecFails = 0
					k.LastErr = ""
					k.UpdatedAt = now
				}
				processed++
			}
			state.renumberKeys()
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
			return
		}
		if err := state.save(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"processed": processed, "missing": missing})
	}
}

func handleKeysBatchTest(rtCfg *RuntimeConfig, state *AppState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req struct {
			IDs     []string `json:"ids"`
			Restore bool     `json:"restore"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		if len(req.IDs) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ids required"})
			return
		}

		type target struct{ id, name, apiKey string }
		state.mu.RLock()
		targets := make([]target, 0, len(req.IDs))
		for _, id := range req.IDs {
			k, ok := state.Keys[id]
			if !ok {
				continue
			}
			targets = append(targets, target{id: id, name: k.Name, apiKey: k.APIKey})
		}
		state.mu.RUnlock()

		client := &http.Client{Timeout: 30 * time.Second}
		cfg := rtCfg.Get()
		payload, _ := json.Marshal(map[string]any{
			"model":      "anthropic/claude-haiku-4.5",
			"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
			"max_tokens": 8,
		})

		results := make([]BatchTestResult, len(targets))
		var wg sync.WaitGroup
		sem := make(chan struct{}, 5)
		for i, t := range targets {
			wg.Add(1)
			sem <- struct{}{}
			go func(idx int, tg target) {
				defer wg.Done()
				defer func() { <-sem }()
				res := BatchTestResult{ID: tg.id, Name: tg.name}
				hreq, err := http.NewRequestWithContext(r.Context(), "POST", cfg.GatewayBaseURL+"/chat/completions", bytes.NewReader(payload))
				if err != nil {
					res.Error = err.Error()
					results[idx] = res
					return
				}
				hreq.Header.Set("Authorization", "Bearer "+tg.apiKey)
				hreq.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(hreq)
				if err != nil {
					res.Error = err.Error()
					results[idx] = res
					return
				}
				defer resp.Body.Close()
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				res.Status = resp.StatusCode
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					res.Error = truncate(strings.TrimSpace(string(body)), 300)
					results[idx] = res
					return
				}
				content := extractChatContent(body)
				res.Content = truncate(content, 200)
				if strings.TrimSpace(content) != "" {
					res.OK = true
				} else {
					res.Error = "响应无内容: " + truncate(strings.TrimSpace(string(body)), 200)
				}
				results[idx] = res
			}(i, t)
		}
		wg.Wait()

		restored := 0
		if req.Restore {
			state.mu.Lock()
			now := time.Now().UTC()
			for i := range results {
				if !results[i].OK {
					continue
				}
				k, ok := state.Keys[results[i].ID]
				if !ok || !k.Scrapped {
					continue
				}
				k.Scrapped = false
				k.ScrappedErr = ""
				k.ScrappedAt = time.Time{}
				k.Paused = false
				k.MonthlySpentUSD = 0
				k.ConsecFails = 0
				k.LastErr = ""
				k.UpdatedAt = now
				results[i].Restored = true
				restored++
			}
			if restored > 0 {
				state.renumberKeys()
				_ = state.save()
			}
			state.mu.Unlock()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"results":  results,
			"total":    len(results),
			"passed":   countOK(results),
			"restored": restored,
		})
	}
}

func countOK(rs []BatchTestResult) int {
	n := 0
	for _, r := range rs {
		if r.OK {
			n++
		}
	}
	return n
}

func extractChatContent(body []byte) string {
	var obj struct {
		Choices []struct {
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &obj); err != nil {
		return ""
	}
	if len(obj.Choices) == 0 {
		return ""
	}
	switch c := obj.Choices[0].Message.Content.(type) {
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, p := range c {
			if m, ok := p.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
		return sb.String()
	}
	return ""
}

func handleKeyByID(state *AppState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/keys/")
		if id == "" || strings.Contains(id, "/") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}

		switch r.Method {
		case http.MethodGet:
			state.mu.RLock()
			defer state.mu.RUnlock()
			k, ok := state.Keys[id]
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
				return
			}
			view := publicView(k)
			view.APIKey = k.APIKey
			writeJSON(w, http.StatusOK, view)

		case http.MethodPatch:
			var req UpdateKeyReq
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
				return
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			k, ok := state.Keys[id]
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
				return
			}
			if req.Name != nil {
				k.Name = strings.TrimSpace(*req.Name)
			}
			if req.Tier != nil {
				k.Tier = strings.TrimSpace(*req.Tier)
			}
			if req.APIKey != nil {
				v := strings.TrimSpace(*req.APIKey)
				if v == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "api_key cannot be empty"})
					return
				}
				k.APIKey = v
			}
			if req.Paused != nil {
				k.Paused = *req.Paused
				if *req.Paused && state.StickyMode && state.StickyKeyID == id {
					state.StickyKeyID = ""
				}
			}
			if req.ResetCost != nil && *req.ResetCost {
				k.MonthlySpentUSD = 0
				k.Paused = false
			}
			if req.Scrapped != nil {
				k.Scrapped = *req.Scrapped
				if *req.Scrapped {
					k.ScrappedErr = "手动报废"
					k.ScrappedAt = time.Now().UTC()
					k.Paused = true
					if state.StickyMode && state.StickyKeyID == id {
						state.StickyKeyID = ""
					}
				} else {
					k.ScrappedErr = ""
					k.ScrappedAt = time.Time{}
					k.Paused = false
					k.MonthlySpentUSD = 0
				}
				state.renumberKeys()
			}
			k.UpdatedAt = time.Now().UTC()
			if err := state.save(); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, publicView(k))

		case http.MethodDelete:
			state.mu.Lock()
			defer state.mu.Unlock()
			if _, ok := state.Keys[id]; !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
				return
			}
			delete(state.Keys, id)
			if err := state.save(); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})

		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}

func handleLogs(ring *ProxyLogRing) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		n := 200
		if qs := r.URL.Query().Get("n"); qs != "" {
			if v, err := strconv.Atoi(qs); err == nil && v > 0 {
				n = v
			}
		}
		logs := ring.Recent(n)
		if logs == nil {
			logs = []ProxyLog{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"logs": logs, "count": len(logs)})
	}
}

func handleTestKey(rtCfg *RuntimeConfig, state *AppState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := rtCfg.Get()
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/test/")
		if path == "" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		parts := strings.SplitN(path, "/", 2)
		id := parts[0]
		action := ""
		if len(parts) > 1 {
			action = parts[1]
		}

		state.mu.RLock()
		k, ok := state.Keys[id]
		if !ok {
			state.mu.RUnlock()
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "key not found"})
			return
		}
		apiKey := k.APIKey
		state.mu.RUnlock()

		httpClient := &http.Client{Timeout: 30 * time.Second}

		if action == "models" {
			if r.Method != http.MethodGet {
				writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
				return
			}
			req, err := http.NewRequestWithContext(r.Context(), "GET", cfg.GatewayBaseURL+"/models", nil)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			req.Header.Set("Authorization", "Bearer "+apiKey)
			resp, err := httpClient.Do(req)
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": truncate(string(body), 500), "status_code": resp.StatusCode})
				return
			}
			var mr struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &mr); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "parse failed: " + err.Error()})
				return
			}
			names := make([]string, 0, len(mr.Data))
			for _, m := range mr.Data {
				names = append(names, m.ID)
			}
			sort.Strings(names)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "models": names})
			return
		}

		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
			return
		}
		var reqBody struct {
			Model string `json:"model"`
		}
		if r.ContentLength > 0 {
			_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&reqBody)
		}
		model := strings.TrimSpace(reqBody.Model)
		if model == "" {
			model = "anthropic/claude-haiku-4.5"
		}

		testPayload := map[string]any{
			"model":      model,
			"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
			"max_tokens": 1,
		}
		b, _ := json.Marshal(testPayload)
		req, err := http.NewRequestWithContext(r.Context(), "POST", cfg.GatewayBaseURL+"/chat/completions", bytes.NewReader(b))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")
		start := time.Now()
		resp, err := httpClient.Do(req)
		elapsed := time.Since(start).Milliseconds()
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "elapsed_ms": elapsed})
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		success := resp.StatusCode >= 200 && resp.StatusCode < 300

		if success {
			state.mu.Lock()
			if tk, ok := state.Keys[id]; ok && tk.ConsecFails > 0 {
				tk.ConsecFails = 0
				tk.LastErr = ""
				tk.UpdatedAt = time.Now().UTC()
				_ = state.save()
			}
			state.mu.Unlock()
		}

		go func() { _ = pollOne(cfg, state, id) }()

		result := map[string]any{
			"ok":          success,
			"status_code": resp.StatusCode,
			"model":       model,
			"elapsed_ms":  elapsed,
		}
		if !success {
			result["error"] = truncate(string(respBody), 500)
		}
		writeJSON(w, http.StatusOK, result)
	}
}
