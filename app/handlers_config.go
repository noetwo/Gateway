package app

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func handleConfig(rtCfg *RuntimeConfig, state *AppState) http.HandlerFunc {
	type configReq struct {
		ListenAddr                 string   `json:"listen_addr"`
		GatewayBaseURL             string   `json:"gateway_base_url"`
		CooldownUSD                float64  `json:"monthly_cooldown_usd"`
		WebAuthToken               string   `json:"web_auth_token"`
		ApiAuthTokens              []string `json:"api_auth_tokens"`
		DefaultReasoning           string   `json:"default_reasoning_effort"`
		DebugDumpDir               *string  `json:"debug_dump_dir"`
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
			if req.DebugDumpDir != nil {
				cfg.DebugDumpDir = normalizeDebugDumpDir(*req.DebugDumpDir)
			}
			if req.DebugEnabled != nil {
				cfg.DebugEnabled = *req.DebugEnabled
			}
			if cfg.DebugEnabled {
				if err := checkDebugDirWritable(cfg.DebugDumpDir); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "debug dir is not writable: " + err.Error()})
					return
				}
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
