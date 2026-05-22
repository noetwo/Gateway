package app

import (
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
	debugDumpDir := normalizeDebugDumpDir(os.Getenv("DEBUG_DUMP_DIR"))
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
