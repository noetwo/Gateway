package app

import (
	"errors"
	"log"
	"net/http"
	"time"
)

// Run 启动整个服务。loginHTML / indexHTML 由调用方（顶层 main.go）通过 //go:embed 注入。
func Run(loginHTML, indexHTML string) {
	loadConfigFile(defaultConfigFile)
	seedCfg := readConfig()
	rtCfg, err := loadRuntimeConfig(seedCfg)
	if err != nil {
		log.Fatalf("load runtime config failed: %v", err)
	}
	cfg := rtCfg.Get()
	state, err := loadState(cfg.StateFilePath, cfg.CooldownUSD)
	if err != nil {
		log.Fatalf("load state failed: %v", err)
	}

	if cfg.WebAuthToken == "" {
		log.Fatalf("WEB_AUTH_TOKEN is required (set in config.txt or env), cannot be empty")
	}
	webAuth := requireDynamicAuth(func() []string { return []string{rtCfg.Get().WebAuthToken} })
	apiAuth := requireDynamicAuthFailClosed(func() []string { return rtCfg.Get().ApiAuthTokens })
	log.Printf("auth enabled (web token length=%d, api tokens=%d)", len(cfg.WebAuthToken), len(cfg.ApiAuthTokens))
	if cfg.DefaultReasoning != "" {
		log.Printf("default reasoning effort = %s (auto-injected on /v1/chat/completions and /v1/messages)", cfg.DefaultReasoning)
	}

	proxyLogs := newProxyLogRing(200)

	mux := http.NewServeMux()
	mux.HandleFunc("/", serveIndexDynamic(func() string { return rtCfg.Get().WebAuthToken }, loginHTML, indexHTML))
	mux.HandleFunc("/api/login", withCORS(handleDynamicLogin(func() string { return rtCfg.Get().WebAuthToken })))
	mux.HandleFunc("/api/logout", withCORS(handleLogout))
	mux.HandleFunc("/api/state", withCORS(webAuth(handleGetState(state, rtCfg))))
	mux.HandleFunc("/api/settings", withCORS(webAuth(handleSettings(state))))
	mux.HandleFunc("/api/retry-settings", withCORS(webAuth(handleRetrySettings(state))))
	mux.HandleFunc("/api/config", withCORS(webAuth(handleConfig(rtCfg, state))))
	mux.HandleFunc("/api/client-log", withCORS(webAuth(handleClientLog())))
	mux.HandleFunc("/api/debug", withCORS(webAuth(handleDebugFiles(rtCfg))))
	mux.HandleFunc("/api/debug/request", withCORS(webAuth(handleDebugRequest(rtCfg))))
	mux.HandleFunc("/api/debug/settings", withCORS(webAuth(handleDebugSettings(rtCfg))))
	mux.HandleFunc("/api/debug/file", withCORS(webAuth(handleDebugFile(rtCfg))))
	mux.HandleFunc("/api/export/keys", withCORS(webAuth(handleExportKeys(state))))
	mux.HandleFunc("/api/refresh", withCORS(webAuth(handleRefresh(rtCfg, state))))
	mux.HandleFunc("/api/test/", withCORS(webAuth(handleTestKey(rtCfg, state))))
	mux.HandleFunc("/api/sticky", withCORS(webAuth(handleStickyToggle(state))))
	mux.HandleFunc("/api/sticky/select", withCORS(webAuth(handleStickySelect(state))))
	mux.HandleFunc("/api/logs", withCORS(webAuth(handleLogs(proxyLogs))))
	mux.HandleFunc("/api/keys", withCORS(webAuth(handleKeys(state))))
	mux.HandleFunc("/api/keys/bulk", withCORS(webAuth(handleKeysBulk(state))))
	mux.HandleFunc("/api/keys/batch", withCORS(webAuth(handleKeysBatch(state))))
	mux.HandleFunc("/api/keys/batch/test", withCORS(webAuth(handleKeysBatchTest(rtCfg, state))))
	mux.HandleFunc("/api/keys/", withCORS(webAuth(handleKeyByID(state))))
	mux.HandleFunc("/v1", withCORS(apiAuth(handleGatewayProxy(rtCfg, state, proxyLogs, ""))))
	mux.HandleFunc("/v1/", withCORS(apiAuth(handleGatewayProxy(rtCfg, state, proxyLogs, ""))))
	mux.HandleFunc("/aws/v1", withCORS(apiAuth(handleGatewayProxy(rtCfg, state, proxyLogs, "bedrock"))))
	mux.HandleFunc("/aws/v1/", withCORS(apiAuth(handleGatewayProxy(rtCfg, state, proxyLogs, "bedrock"))))
	mux.HandleFunc("/vertex/v1", withCORS(apiAuth(handleGatewayProxy(rtCfg, state, proxyLogs, "vertex"))))
	mux.HandleFunc("/vertex/v1/", withCORS(apiAuth(handleGatewayProxy(rtCfg, state, proxyLogs, "vertex"))))
	mux.HandleFunc("/anthropic/v1", withCORS(apiAuth(handleGatewayProxy(rtCfg, state, proxyLogs, "anthropic"))))
	mux.HandleFunc("/anthropic/v1/", withCORS(apiAuth(handleGatewayProxy(rtCfg, state, proxyLogs, "anthropic"))))
	mux.HandleFunc("/azure/v1", withCORS(apiAuth(handleGatewayProxy(rtCfg, state, proxyLogs, "azure"))))
	mux.HandleFunc("/azure/v1/", withCORS(apiAuth(handleGatewayProxy(rtCfg, state, proxyLogs, "azure"))))
	mux.HandleFunc("/v1beta", withCORS(apiAuth(handleGeminiProxy(rtCfg, state, proxyLogs))))
	mux.HandleFunc("/v1beta/", withCORS(apiAuth(handleGeminiProxy(rtCfg, state, proxyLogs))))

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           logRequest(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("AI Gateway poller listening on http://%s", displayListenAddr(cfg.ListenAddr))
	log.Printf("manual refresh mode cooldown=%.2fUSD gateway=%s", cfg.CooldownUSD, cfg.GatewayBaseURL)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}
