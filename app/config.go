package app

import (
	"bufio"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type RuntimeConfig struct {
	mu       sync.RWMutex
	filePath string
	current  Config
}

type runtimeConfigFile struct {
	ListenAddr                 string    `json:"listen_addr"`
	GatewayBaseURL             string    `json:"gateway_base_url"`
	CooldownUSD                float64   `json:"monthly_cooldown_usd"`
	WebAuthToken               string    `json:"web_auth_token"`
	ApiAuthTokens              []string  `json:"api_auth_tokens"`
	DefaultReasoning           string    `json:"default_reasoning_effort"`
	DebugDumpDir               string    `json:"debug_dump_dir"`
	DebugEnabled               bool      `json:"debug_enabled"`
	PassthroughOnly            bool      `json:"passthrough_only"`
	MonthlyQuotaPerKey         float64   `json:"monthly_quota_per_key"`
	ProviderOrder              string    `json:"provider_order"`
	CooldownStatusCodes        string    `json:"cooldown_status_codes"`
	ProxyScrapStatusCodes      string    `json:"proxy_scrap_status_codes"`
	ProxyScrapFailThreshold    int       `json:"proxy_scrap_fail_threshold"`
	RefreshScrapStatusCodes    string    `json:"refresh_scrap_status_codes"`
	RefreshCooldownStatusCodes string    `json:"refresh_cooldown_status_codes"`
	LastSaved                  time.Time `json:"last_saved"`
}

func readConfig() Config {
	listen := getenvDefault("LISTEN_ADDR", defaultListenAddr)
	base := strings.TrimRight(getenvDefault("GATEWAY_BASE_URL", defaultGatewayBase), "/")
	cooldown, err := strconv.ParseFloat(getenvDefault("MONTHLY_COOLDOWN_USD", "5"), 64)
	if err != nil || cooldown <= 0 {
		cooldown = defaultCooldownUSD
	}
	stateDir := getenvDefault("STATE_DIR", defaultStateDirName)
	webAuthToken := strings.TrimSpace(os.Getenv("WEB_AUTH_TOKEN"))
	apiAuthTokens := parseTokenList(os.Getenv("API_AUTH_TOKEN"))
	apiAuthToken := ""
	if len(apiAuthTokens) > 0 {
		apiAuthToken = apiAuthTokens[0]
	}
	defaultReasoning := strings.ToLower(strings.TrimSpace(os.Getenv("DEFAULT_REASONING_EFFORT")))
	debugDumpDir := fixedDebugDir()
	passthrough := strings.ToLower(strings.TrimSpace(os.Getenv("PASSTHROUGH_ONLY"))) == "true"
	providerOrder := strings.ToLower(strings.TrimSpace(os.Getenv("PROVIDER_ORDER")))
	monthlyQuota, err := strconv.ParseFloat(getenvDefault("MONTHLY_QUOTA_PER_KEY", "5"), 64)
	if err != nil || monthlyQuota <= 0 {
		monthlyQuota = 5
	}
	return Config{
		ListenAddr:                 listen,
		GatewayBaseURL:             base,
		CooldownUSD:                cooldown,
		StateFilePath:              filepath.Join(stateDir, defaultStateFile),
		RuntimeConfigPath:          filepath.Join(stateDir, defaultRuntimeConfigFile),
		WebAuthToken:               webAuthToken,
		ApiAuthToken:               apiAuthToken,
		ApiAuthTokens:              apiAuthTokens,
		DefaultReasoning:           defaultReasoning,
		DebugDumpDir:               debugDumpDir,
		DebugEnabled:               strings.EqualFold(strings.TrimSpace(os.Getenv("DEBUG_ENABLED")), "true"),
		PassthroughOnly:            passthrough,
		MonthlyQuotaPerKey:         monthlyQuota,
		ProviderOrder:              providerOrder,
		CooldownStatusCodes:        normalizeStatusCodesOrDefault(os.Getenv("COOLDOWN_STATUS_CODES"), defaultCooldownCodes),
		ProxyScrapStatusCodes:      normalizeStatusCodesOrDefault(os.Getenv("PROXY_SCRAP_STATUS_CODES"), defaultProxyScrapCodes),
		ProxyScrapFailThreshold:    getenvIntDefault("PROXY_SCRAP_FAIL_THRESHOLD", 3, 1, 50),
		RefreshScrapStatusCodes:    normalizeStatusCodesOrDefault(os.Getenv("REFRESH_SCRAP_STATUS_CODES"), defaultRefreshScrapCodes),
		RefreshCooldownStatusCodes: normalizeStatusCodesOrDefault(os.Getenv("REFRESH_COOLDOWN_STATUS_CODES"), ""),
	}
}

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
	cfg.DebugDumpDir = fixedDebugDir()
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
	cfg.DebugDumpDir = fixedDebugDir()
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

func parseTokenList(raw string) []string {
	return cleanTokens(strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t'
	}))
}

func cleanTokens(tokens []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	return out
}

func normalizeStatusCodesOrDefault(raw, fallback string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return strings.TrimSpace(fallback)
	}
	codes, err := normalizeRetryStatusCodes(raw)
	if err != nil {
		return strings.TrimSpace(fallback)
	}
	return codes
}

func loadConfigFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			runtimePath := filepath.Join(getenvDefault("STATE_DIR", defaultStateDirName), defaultRuntimeConfigFile)
			if _, statErr := os.Stat(runtimePath); statErr == nil {
				return
			}
			log.Fatalf("config file %s not found in working directory and runtime config is missing; copy the template from the repo and fill in WEB_AUTH_TOKEN before starting", path)
		}
		log.Fatalf("open config file %s: %v", path, err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		v = strings.Trim(v, "\"'")
		if k == "" {
			continue
		}
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}
}

func getenvDefault(k, fallback string) string {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return fallback
	}
	return v
}

func getenvIntDefault(k string, fallback, min, max int) int {
	v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(k)))
	if err != nil {
		return fallback
	}
	if v < min {
		return min
	}
	if max > 0 && v > max {
		return max
	}
	return v
}
