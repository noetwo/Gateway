package app

import (
	"sync"
	"time"
)

const (
	defaultListenAddr        = ":8211"
	defaultGatewayBase       = "https://ai-gateway.vercel.sh/v1"
	defaultCooldownUSD       = 5.0
	defaultStateDirName      = "data"
	defaultStateFile         = "state.json"
	defaultRuntimeConfigFile = "config.json"
	defaultDebugDirName      = "debug"
	defaultConfigFile        = "config.txt"
	defaultRetryStatusCodes  = "400,401,402,403,429,5xx"
	defaultHobbyBlocked      = "anthropic/claude-opus-4.5,anthropic/claude-opus-4.6,anthropic/claude-opus-4.7"
	defaultCooldownCodes     = "402"
	defaultProxyScrapCodes   = "401"
	defaultRefreshScrapCodes = "401,403"
	defaultMaxRetryAttempts  = 20
	maxAllowedRetryAttempts  = 20
	defaultPreferredTier     = "team"
)

type AppState struct {
	mu            sync.RWMutex
	filePath      string
	proxyTurn     uint64
	Cooldown      float64         `json:"cooldown_usd"`
	LastSaved     time.Time       `json:"last_saved"`
	Keys          map[string]*Key `json:"keys"`
	StickyMode    bool            `json:"sticky_mode"`
	StickyKeyID   string          `json:"sticky_key_id,omitempty"`
	RetryCodes    string          `json:"retry_status_codes,omitempty"`
	MaxAttempts   int             `json:"max_retry_attempts,omitempty"`
	HobbyBlocked  []string        `json:"hobby_blocked_models"`
	PreferredTier string          `json:"preferred_tier,omitempty"`
	TeamPriority  []string        `json:"team_priority_models,omitempty"`
	HobbyPriority []string        `json:"hobby_priority_models,omitempty"`
	TeamOnly      []string        `json:"team_only_models,omitempty"`
}

type Key struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Tier            string    `json:"tier"`
	APIKey          string    `json:"api_key"`
	MonthlySpentUSD float64   `json:"monthly_spent_usd"`
	Paused          bool      `json:"paused"`
	LastBalance     float64   `json:"last_balance"`
	LastUsedTotal   float64   `json:"last_used_total"`
	LastPollAt      time.Time `json:"last_poll_at"`
	LastProxyAt     time.Time `json:"last_proxy_at"`
	LastErr         string    `json:"last_error"`
	RequestCount    int64     `json:"request_count"`
	ProxyReqCount   int64     `json:"proxy_request_count"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	Scrapped        bool      `json:"scrapped"`
	ScrappedErr     string    `json:"scrapped_error,omitempty"`
	ScrappedAt      time.Time `json:"scrapped_at,omitempty"`
	ConsecFails     int       `json:"consec_fails"`
}

type PublicKeyView struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Tier            string    `json:"tier"`
	APIKey          string    `json:"api_key,omitempty"`
	MonthlySpentUSD float64   `json:"monthly_spent_usd"`
	Paused          bool      `json:"paused"`
	LastBalance     float64   `json:"last_balance"`
	LastUsedTotal   float64   `json:"last_used_total"`
	LastPollAt      time.Time `json:"last_poll_at"`
	LastProxyAt     time.Time `json:"last_proxy_at"`
	LastErr         string    `json:"last_error"`
	RequestCount    int64     `json:"request_count"`
	ProxyReqCount   int64     `json:"proxy_request_count"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	ResetAt         time.Time `json:"reset_at"`
	Scrapped        bool      `json:"scrapped"`
	ScrappedErr     string    `json:"scrapped_error,omitempty"`
	ScrappedAt      time.Time `json:"scrapped_at,omitempty"`
	ConsecFails     int       `json:"consec_fails"`
}

type CreateKeyReq struct {
	Name   string `json:"name"`
	Tier   string `json:"tier"`
	APIKey string `json:"api_key"`
}

type UpdateKeyReq struct {
	Name      *string `json:"name"`
	Tier      *string `json:"tier"`
	APIKey    *string `json:"api_key"`
	Paused    *bool   `json:"paused"`
	ResetCost *bool   `json:"reset_cost"`
	Scrapped  *bool   `json:"scrapped"`
}

type CreditsResp struct {
	Balance   string `json:"balance"`
	TotalUsed string `json:"total_used"`
}

type Config struct {
	ListenAddr                 string
	GatewayBaseURL             string
	CooldownUSD                float64
	StateFilePath              string
	RuntimeConfigPath          string
	WebAuthToken               string
	ApiAuthToken               string
	ApiAuthTokens              []string
	DefaultReasoning           string
	DebugDumpDir               string
	DebugEnabled               bool
	PassthroughOnly            bool
	MonthlyQuotaPerKey         float64
	ProviderOrder              string
	CooldownStatusCodes        string
	ProxyScrapStatusCodes      string
	ProxyScrapFailThreshold    int
	RefreshScrapStatusCodes    string
	RefreshCooldownStatusCodes string
}

type proxyCandidate struct {
	ID     string
	APIKey string
	Tier   string
}

type ProxyLog struct {
	Time         time.Time `json:"time"`
	Model        string    `json:"model"`
	KeyName      string    `json:"key_name"`
	KeyID        string    `json:"key_id"`
	StatusCode   int       `json:"status_code"`
	ElapsedMs    int64     `json:"elapsed_ms"`
	Success      bool      `json:"success"`
	Retried      bool      `json:"retried"`
	Error        string    `json:"error,omitempty"`
	Path         string    `json:"path"`
	Method       string    `json:"method,omitempty"`
	Endpoint     string    `json:"endpoint,omitempty"`
	Interface    string    `json:"interface,omitempty"`
	Provider     string    `json:"provider,omitempty"`
	Stream       bool      `json:"stream,omitempty"`
	InputTokens  int       `json:"input_tokens,omitempty"`
	OutputTokens int       `json:"output_tokens,omitempty"`
	TotalTokens  int       `json:"total_tokens,omitempty"`
	UsageSource  string    `json:"usage_source,omitempty"`
	DumpID       string    `json:"dump_id,omitempty"`
}

type TokenUsage struct {
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	TotalTokens  int    `json:"total_tokens,omitempty"`
	Source       string `json:"usage_source,omitempty"`
}

type ProxyLogRing struct {
	mu   sync.Mutex
	logs []ProxyLog
	cap  int
}

func newProxyLogRing(cap int) *ProxyLogRing {
	return &ProxyLogRing{logs: make([]ProxyLog, 0, cap), cap: cap}
}

func (r *ProxyLogRing) Add(entry ProxyLog) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.logs) >= r.cap {
		r.logs = r.logs[1:]
	}
	r.logs = append(r.logs, entry)
}

func (r *ProxyLogRing) Recent(n int) []ProxyLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || len(r.logs) == 0 {
		return nil
	}
	if n > len(r.logs) {
		n = len(r.logs)
	}
	result := make([]ProxyLog, n)
	for i := 0; i < n; i++ {
		result[i] = r.logs[len(r.logs)-1-i]
	}
	return result
}

type BatchTestResult struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Restored bool   `json:"restored"`
	Status   int    `json:"status_code"`
	Error    string `json:"error,omitempty"`
	Content  string `json:"content,omitempty"`
}
