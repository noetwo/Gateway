package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func handleGatewayProxy(rtCfg *RuntimeConfig, state *AppState, proxyLogs *ProxyLogRing, channelProvider string) http.HandlerFunc {
	client := &http.Client{Timeout: 30 * time.Minute}

	return func(w http.ResponseWriter, r *http.Request) {
		cfg := rtCfg.Get()
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		reqStart := time.Now()
		// 支持路径前缀：/aws/v1, /vertex/v1, /anthropic/v1, /azure/v1 或裸 /v1
		logicalPath := r.URL.Path
		switch {
		case strings.HasPrefix(logicalPath, "/aws/v1"):
			logicalPath = strings.TrimPrefix(logicalPath, "/aws")
		case strings.HasPrefix(logicalPath, "/vertex/v1"):
			logicalPath = strings.TrimPrefix(logicalPath, "/vertex")
		case strings.HasPrefix(logicalPath, "/anthropic/v1"):
			logicalPath = strings.TrimPrefix(logicalPath, "/anthropic")
		case strings.HasPrefix(logicalPath, "/azure/v1"):
			logicalPath = strings.TrimPrefix(logicalPath, "/azure")
		}
		suffix := strings.TrimPrefix(logicalPath, "/v1")
		targetURL := cfg.GatewayBaseURL + suffix
		if r.URL.RawQuery != "" {
			targetURL += "?" + r.URL.RawQuery
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		// /v1/images/edits → /v1/images/generations 转换
		if strings.HasPrefix(logicalPath, "/v1/images/edits") {
			body, r = rewriteImageEditsToGenerations(body, r)
			logicalPath = r.URL.Path
			suffix = strings.TrimPrefix(logicalPath, "/v1")
			targetURL = cfg.GatewayBaseURL + suffix
			if r.URL.RawQuery != "" {
				targetURL += "?" + r.URL.RawQuery
			}
		}

		if strings.HasPrefix(logicalPath, "/v1/images/") {
			body = injectImageDefaults(body)
		}

		reqModel := extractModelFromBody(body)

		providerForThisReq := channelProvider
		if providerForThisReq == "" {
			providerForThisReq = cfg.ProviderOrder
		}

		body = rewriteThinkingSuffix(body, logicalPath)
		body = transformReasoning(body, cfg.DefaultReasoning, logicalPath)
		body = ensureStreamUsage(body, logicalPath)
		body = sanitizeForVercel(body)
		body = injectProviderOrder(body, providerForThisReq)
		wantStream := requestWantsStream(body)
		inputEst := estimateInputTokensFromBody(body)
		isAnthropicPath := strings.Contains(logicalPath, "/messages")
		isOpenAIPath := strings.Contains(logicalPath, "chat/completions")

		dumpDir := ""
		if cfg.DebugEnabled {
			dumpDir = cfg.DebugDumpDir
		}
		dump := newDumpSession(dumpDir, r, body, inputEst)
		defer dump.finalize(0, r.Context().Err() != nil, nil)

		cands := state.nextProxyCandidates(reqModel)
		if len(cands) == 0 {
			dump.finalize(http.StatusServiceUnavailable, r.Context().Err() != nil, nil)
			if modelBlockedForHobby(reqModel, state.hobbyBlockedSettings()) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no non-hobby active key available for " + reqModel})
				return
			}
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no active key available"})
			return
		}
		retryCodes, maxAttempts := state.retrySettings()
		if maxAttempts > 0 && len(cands) > maxAttempts {
			cands = cands[:maxAttempts]
		}

		getKeyName := func(id string) string {
			state.mu.RLock()
			defer state.mu.RUnlock()
			if k, ok := state.Keys[id]; ok {
				return k.Name
			}
			return id
		}
		interfaceName := proxyInterfaceName(r.URL.Path, channelProvider)
		addProxyLog := func(c proxyCandidate, statusCode int, success bool, retried bool, errMsg string, usage TokenUsage) {
			usage.finish(inputEst, 0)
			proxyLogs.Add(ProxyLog{
				Time:         time.Now(),
				Model:        reqModel,
				KeyName:      getKeyName(c.ID),
				KeyID:        c.ID,
				StatusCode:   statusCode,
				ElapsedMs:    time.Since(reqStart).Milliseconds(),
				Success:      success,
				Retried:      retried,
				Error:        errMsg,
				Path:         r.URL.Path,
				Method:       r.Method,
				Endpoint:     logicalPath,
				Interface:    interfaceName,
				Provider:     providerForThisReq,
				Stream:       wantStream,
				InputTokens:  usage.InputTokens,
				OutputTokens: usage.OutputTokens,
				TotalTokens:  usage.TotalTokens,
				UsageSource:  usage.Source,
				DumpID:       dump.id,
			})
		}

		var lastErr error
		for i, c := range cands {
			outReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(body))
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			copyProxyHeaders(outReq.Header, r.Header)
			outReq.Header.Set("Authorization", "Bearer "+c.APIKey)
			outReq.Header.Set("Accept-Encoding", "identity")
			if wantStream {
				outReq.Header.Set("Accept", "text/event-stream")
			}

			resp, err := client.Do(outReq)
			if err != nil {
				if r.Context().Err() != nil {
					addProxyLog(c, 0, false, i > 0, "client_gone: "+err.Error(), estimatedUsage(inputEst, 0))
					return
				}
				state.markProxyFailure(c.ID, err.Error(), 0, cfg)
				lastErr = err
				addProxyLog(c, 0, false, i > 0, err.Error(), estimatedUsage(inputEst, 0))
				continue
			}

			if shouldRetryWithNextKey(resp.StatusCode, retryCodes) {
				preview, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				_ = resp.Body.Close()
				errMsg := fmt.Sprintf("upstream status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(preview)))
				state.markProxyFailure(c.ID, errMsg, resp.StatusCode, cfg)
				lastErr = errors.New(errMsg)
				addProxyLog(c, resp.StatusCode, false, i > 0, truncate(errMsg, 200), estimatedUsage(inputEst, 0))
				if i < len(cands)-1 {
					continue
				}
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg})
				return
			}

			copyResponseHeaders(w.Header(), resp.Header)
			w.Header().Del("Content-Length")
			if wantStream {
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				w.Header().Del("Content-Encoding")
				w.Header().Set("X-Accel-Buffering", "no")
			}

			w.WriteHeader(resp.StatusCode)
			var copyErr error
			usage := estimatedUsage(inputEst, 0)
			respIsSSE := wantStream && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "event-stream")
			respBody := dump.wrapUpstream(resp.Body)
			respWriter := dump.wrapDownstream(w)
			captureWriter := newCaptureResponseWriter(respWriter, maxDebugViewBytes)
			switch {
			case respIsSSE && isAnthropicPath:
				copyErr = processAnthropicSSE(captureWriter, respBody, r.Context(), inputEst, &usage)
			case respIsSSE && isOpenAIPath:
				copyErr = processOpenAISSE(captureWriter, respBody, r.Context(), inputEst, &usage)
			default:
				copyErr = streamCopy(captureWriter, respBody)
				usage = extractUsageFromResponse(captureWriter.Bytes(), inputEst)
			}
			_ = resp.Body.Close()
			dump.finalize(resp.StatusCode, r.Context().Err() != nil, copyErr)
			clientCancelled := r.Context().Err() != nil
			if copyErr != nil {
				if clientCancelled {
					if resp.StatusCode >= 200 && resp.StatusCode < 300 {
						state.markProxySuccess(c.ID)
					}
					addProxyLog(c, resp.StatusCode, resp.StatusCode >= 200 && resp.StatusCode < 300, i > 0, "client_gone: "+copyErr.Error(), usage)
					return
				}
				state.markProxyFailure(c.ID, copyErr.Error(), 0, cfg)
				addProxyLog(c, resp.StatusCode, false, i > 0, "stream error: "+copyErr.Error(), usage)
				return
			}

			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				state.markProxySuccess(c.ID)
				addProxyLog(c, resp.StatusCode, true, i > 0, "", usage)
			} else {
				state.markProxyFailure(c.ID, fmt.Sprintf("upstream status=%d", resp.StatusCode), resp.StatusCode, cfg)
				addProxyLog(c, resp.StatusCode, false, i > 0, fmt.Sprintf("status %d", resp.StatusCode), usage)
			}
			return
		}

		if lastErr == nil {
			lastErr = errors.New("all keys failed")
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": lastErr.Error()})
	}
}

func shouldRetryWithNextKey(code int, retryCodes string) bool {
	return retryStatusEnabled(code, retryCodes)
}

func proxyInterfaceName(path, channelProvider string) string {
	switch {
	case strings.HasPrefix(path, "/aws/v1"):
		return "aws/bedrock"
	case strings.HasPrefix(path, "/vertex/v1"):
		return "vertex"
	case strings.HasPrefix(path, "/anthropic/v1"):
		return "anthropic"
	case strings.HasPrefix(path, "/azure/v1"):
		return "azure"
	case strings.TrimSpace(channelProvider) != "":
		return channelProvider
	case strings.Contains(path, "/messages"):
		return "anthropic-messages"
	case strings.Contains(path, "/chat/completions"):
		return "openai-chat"
	case strings.Contains(path, "/responses"):
		return "openai-responses"
	case strings.Contains(path, "/images/"):
		return "openai-images"
	default:
		return "openai-compatible"
	}
}
