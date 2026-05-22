package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func loadRuntimeConfig(seed Config) (*RuntimeConfig, error) {
	if err := os.MkdirAll(filepath.Dir(seed.RuntimeConfigPath), 0o755); err != nil {
		return nil, err
	}
	rt := &RuntimeConfig{filePath: seed.RuntimeConfigPath, current: normalizeConfig(seed)}
	if _, err := os.Stat(seed.RuntimeConfigPath); errors.Is(err, os.ErrNotExist) {
		if err := rt.saveLocked(); err != nil {
			return nil, err
		}
		return rt, nil
	}
	b, err := os.ReadFile(seed.RuntimeConfigPath)
	if err != nil {
		return nil, err
	}
	var disk runtimeConfigFile
	if len(b) > 0 {
		if err := json.Unmarshal(b, &disk); err != nil {
			return nil, err
		}
	}
	cfg := seed
	if strings.TrimSpace(disk.ListenAddr) != "" {
		cfg.ListenAddr = strings.TrimSpace(disk.ListenAddr)
	}
	if strings.TrimSpace(disk.GatewayBaseURL) != "" {
		cfg.GatewayBaseURL = strings.TrimRight(strings.TrimSpace(disk.GatewayBaseURL), "/")
	}
	if disk.CooldownUSD > 0 {
		cfg.CooldownUSD = disk.CooldownUSD
	}
	if strings.TrimSpace(disk.WebAuthToken) != "" {
		cfg.WebAuthToken = strings.TrimSpace(disk.WebAuthToken)
	}
	if len(disk.ApiAuthTokens) > 0 {
		cfg.ApiAuthTokens = cleanTokens(disk.ApiAuthTokens)
		if len(cfg.ApiAuthTokens) > 0 {
			cfg.ApiAuthToken = cfg.ApiAuthTokens[0]
		}
	}
	cfg.DefaultReasoning = strings.ToLower(strings.TrimSpace(disk.DefaultReasoning))
	if strings.TrimSpace(disk.DebugDumpDir) != "" {
		cfg.DebugDumpDir = normalizeDebugDumpDir(disk.DebugDumpDir)
	}
	if envDebugDir := strings.TrimSpace(os.Getenv("DEBUG_DUMP_DIR")); envDebugDir != "" {
		cfg.DebugDumpDir = normalizeDebugDumpDir(envDebugDir)
	}
	cfg.DebugEnabled = disk.DebugEnabled
	cfg.PassthroughOnly = disk.PassthroughOnly
	if disk.MonthlyQuotaPerKey > 0 {
		cfg.MonthlyQuotaPerKey = disk.MonthlyQuotaPerKey
	}
	cfg.ProviderOrder = strings.ToLower(strings.TrimSpace(disk.ProviderOrder))
	cfg.CooldownStatusCodes = normalizeStatusCodesOrDefault(disk.CooldownStatusCodes, cfg.CooldownStatusCodes)
	cfg.ProxyScrapStatusCodes = normalizeStatusCodesOrDefault(disk.ProxyScrapStatusCodes, cfg.ProxyScrapStatusCodes)
	if disk.ProxyScrapFailThreshold > 0 {
		cfg.ProxyScrapFailThreshold = disk.ProxyScrapFailThreshold
	}
	cfg.RefreshScrapStatusCodes = normalizeStatusCodesOrDefault(disk.RefreshScrapStatusCodes, cfg.RefreshScrapStatusCodes)
	cfg.RefreshCooldownStatusCodes = normalizeStatusCodesOrDefault(disk.RefreshCooldownStatusCodes, cfg.RefreshCooldownStatusCodes)
	rt.current = normalizeConfig(cfg)
	if err := rt.saveLocked(); err != nil {
		return nil, err
	}
	return rt, nil
}

func normalizeConfig(cfg Config) Config {
	cfg.GatewayBaseURL = strings.TrimRight(strings.TrimSpace(cfg.GatewayBaseURL), "/")
	if cfg.GatewayBaseURL == "" {
		cfg.GatewayBaseURL = defaultGatewayBase
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = defaultListenAddr
	}
	if cfg.CooldownUSD <= 0 {
		cfg.CooldownUSD = defaultCooldownUSD
	}
	if cfg.MonthlyQuotaPerKey <= 0 {
		cfg.MonthlyQuotaPerKey = 5
	}
	cfg.WebAuthToken = strings.TrimSpace(cfg.WebAuthToken)
	cfg.ApiAuthTokens = cleanTokens(cfg.ApiAuthTokens)
	if len(cfg.ApiAuthTokens) == 0 && strings.TrimSpace(cfg.ApiAuthToken) != "" {
		cfg.ApiAuthTokens = cleanTokens([]string{cfg.ApiAuthToken})
	}
	cfg.ApiAuthToken = ""
	if len(cfg.ApiAuthTokens) > 0 {
		cfg.ApiAuthToken = cfg.ApiAuthTokens[0]
	}
	cfg.DefaultReasoning = strings.ToLower(strings.TrimSpace(cfg.DefaultReasoning))
	cfg.DebugDumpDir = normalizeDebugDumpDir(cfg.DebugDumpDir)
	cfg.ProviderOrder = strings.ToLower(strings.TrimSpace(cfg.ProviderOrder))
	cfg.CooldownStatusCodes = normalizeStatusCodesOrDefault(cfg.CooldownStatusCodes, defaultCooldownCodes)
	cfg.ProxyScrapStatusCodes = normalizeStatusCodesOrDefault(cfg.ProxyScrapStatusCodes, defaultProxyScrapCodes)
	if cfg.ProxyScrapFailThreshold <= 0 {
		cfg.ProxyScrapFailThreshold = 3
	}
	cfg.RefreshScrapStatusCodes = normalizeStatusCodesOrDefault(cfg.RefreshScrapStatusCodes, defaultRefreshScrapCodes)
	cfg.RefreshCooldownStatusCodes = normalizeStatusCodesOrDefault(cfg.RefreshCooldownStatusCodes, "")
	return cfg
}

func (rt *RuntimeConfig) Get() Config {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	cfg := rt.current
	cfg.ApiAuthTokens = append([]string(nil), rt.current.ApiAuthTokens...)
	return cfg
}

func (rt *RuntimeConfig) Update(next Config) (Config, error) {
	next.RuntimeConfigPath = rt.filePath
	next.StateFilePath = rt.current.StateFilePath
	next = normalizeConfig(next)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.current = next
	if err := rt.saveLocked(); err != nil {
		return Config{}, err
	}
	out := rt.current
	out.ApiAuthTokens = append([]string(nil), rt.current.ApiAuthTokens...)
	return out, nil
}

func (rt *RuntimeConfig) saveLocked() error {
	cfg := normalizeConfig(rt.current)
	disk := runtimeConfigFile{
		ListenAddr:                 cfg.ListenAddr,
		GatewayBaseURL:             cfg.GatewayBaseURL,
		CooldownUSD:                cfg.CooldownUSD,
		WebAuthToken:               cfg.WebAuthToken,
		ApiAuthTokens:              append([]string(nil), cfg.ApiAuthTokens...),
		DefaultReasoning:           cfg.DefaultReasoning,
		DebugDumpDir:               cfg.DebugDumpDir,
		DebugEnabled:               cfg.DebugEnabled,
		PassthroughOnly:            cfg.PassthroughOnly,
		MonthlyQuotaPerKey:         cfg.MonthlyQuotaPerKey,
		ProviderOrder:              cfg.ProviderOrder,
		CooldownStatusCodes:        cfg.CooldownStatusCodes,
		ProxyScrapStatusCodes:      cfg.ProxyScrapStatusCodes,
		ProxyScrapFailThreshold:    cfg.ProxyScrapFailThreshold,
		RefreshScrapStatusCodes:    cfg.RefreshScrapStatusCodes,
		RefreshCooldownStatusCodes: cfg.RefreshCooldownStatusCodes,
		LastSaved:                  time.Now().UTC(),
	}
	b, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return err
	}
	tmp := rt.filePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, rt.filePath)
}
