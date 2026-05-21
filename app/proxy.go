package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
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

		dump := newDumpSession(cfg.DebugDumpDir, r, body, inputEst)

		cands := state.nextProxyCandidates(reqModel)
		if len(cands) == 0 {
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
					proxyLogs.Add(ProxyLog{
						Time: time.Now(), Model: reqModel, KeyName: getKeyName(c.ID), KeyID: c.ID,
						StatusCode: 0, ElapsedMs: time.Since(reqStart).Milliseconds(),
						Success: false, Retried: i > 0, Error: "client_gone: " + err.Error(), Path: r.URL.Path,
					})
					return
				}
				state.markProxyFailure(c.ID, err.Error(), 0, cfg)
				lastErr = err
				proxyLogs.Add(ProxyLog{
					Time: time.Now(), Model: reqModel, KeyName: getKeyName(c.ID), KeyID: c.ID,
					StatusCode: 0, ElapsedMs: time.Since(reqStart).Milliseconds(),
					Success: false, Retried: i > 0, Error: err.Error(), Path: r.URL.Path,
				})
				continue
			}

			if shouldRetryWithNextKey(resp.StatusCode, retryCodes) {
				preview, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				_ = resp.Body.Close()
				errMsg := fmt.Sprintf("upstream status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(preview)))
				state.markProxyFailure(c.ID, errMsg, resp.StatusCode, cfg)
				lastErr = errors.New(errMsg)
				proxyLogs.Add(ProxyLog{
					Time: time.Now(), Model: reqModel, KeyName: getKeyName(c.ID), KeyID: c.ID,
					StatusCode: resp.StatusCode, ElapsedMs: time.Since(reqStart).Milliseconds(),
					Success: false, Retried: i > 0, Error: truncate(errMsg, 200), Path: r.URL.Path,
				})
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
			respIsSSE := wantStream && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "event-stream")
			respBody := dump.wrapUpstream(resp.Body)
			respWriter := dump.wrapDownstream(w)
			switch {
			case respIsSSE && isAnthropicPath:
				copyErr = processAnthropicSSE(respWriter, respBody, r.Context(), inputEst)
			case respIsSSE && isOpenAIPath:
				copyErr = processOpenAISSE(respWriter, respBody, r.Context(), inputEst)
			default:
				copyErr = streamCopy(respWriter, respBody)
			}
			_ = resp.Body.Close()
			dump.finalize(resp.StatusCode, r.Context().Err() != nil, copyErr)
			clientCancelled := r.Context().Err() != nil
			if copyErr != nil {
				if clientCancelled {
					if resp.StatusCode >= 200 && resp.StatusCode < 300 {
						state.markProxySuccess(c.ID)
					}
					proxyLogs.Add(ProxyLog{
						Time: time.Now(), Model: reqModel, KeyName: getKeyName(c.ID), KeyID: c.ID,
						StatusCode: resp.StatusCode, ElapsedMs: time.Since(reqStart).Milliseconds(),
						Success: resp.StatusCode >= 200 && resp.StatusCode < 300, Retried: i > 0,
						Error: "client_gone: " + copyErr.Error(), Path: r.URL.Path,
					})
					return
				}
				state.markProxyFailure(c.ID, copyErr.Error(), 0, cfg)
				proxyLogs.Add(ProxyLog{
					Time: time.Now(), Model: reqModel, KeyName: getKeyName(c.ID), KeyID: c.ID,
					StatusCode: resp.StatusCode, ElapsedMs: time.Since(reqStart).Milliseconds(),
					Success: false, Retried: i > 0, Error: "stream error: " + copyErr.Error(), Path: r.URL.Path,
				})
				return
			}

			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				state.markProxySuccess(c.ID)
				proxyLogs.Add(ProxyLog{
					Time: time.Now(), Model: reqModel, KeyName: getKeyName(c.ID), KeyID: c.ID,
					StatusCode: resp.StatusCode, ElapsedMs: time.Since(reqStart).Milliseconds(),
					Success: true, Retried: i > 0, Path: r.URL.Path,
				})
			} else {
				state.markProxyFailure(c.ID, fmt.Sprintf("upstream status=%d", resp.StatusCode), resp.StatusCode, cfg)
				proxyLogs.Add(ProxyLog{
					Time: time.Now(), Model: reqModel, KeyName: getKeyName(c.ID), KeyID: c.ID,
					StatusCode: resp.StatusCode, ElapsedMs: time.Since(reqStart).Milliseconds(),
					Success: false, Retried: i > 0, Error: fmt.Sprintf("status %d", resp.StatusCode), Path: r.URL.Path,
				})
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

func copyProxyHeaders(dst, src http.Header) {
	for k, vals := range src {
		kl := strings.ToLower(k)
		if kl == "authorization" || kl == "x-api-key" || kl == "x-auth-token" || kl == "cookie" || kl == "host" || kl == "connection" || kl == "proxy-connection" || kl == "keep-alive" || kl == "transfer-encoding" || kl == "upgrade" {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for k, vals := range src {
		kl := strings.ToLower(k)
		if kl == "connection" || kl == "proxy-connection" || kl == "keep-alive" || kl == "transfer-encoding" || kl == "upgrade" {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func streamCopy(w http.ResponseWriter, src io.Reader) error {
	if f, ok := w.(http.Flusher); ok {
		buf := make([]byte, 4096)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				if _, wErr := w.Write(buf[:n]); wErr != nil {
					return wErr
				}
				f.Flush()
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
		}
	}
	_, err := io.Copy(w, src)
	return err
}

// ============================================================
// 调试 dump：把每个请求/响应原样落盘
// ============================================================

type dumpSession struct {
	dir       string
	id        string
	startTime time.Time
	reqPath   string
	upstream  *os.File
	downReal  http.ResponseWriter
	downFile  *os.File
	upBytes   int64
	downBytes int64
	inputEst  int
	enabled   bool
}

func newDumpSession(dir string, r *http.Request, body []byte, inputEst int) *dumpSession {
	if strings.TrimSpace(dir) == "" {
		return &dumpSession{enabled: false}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("dump: mkdir %s failed: %v", dir, err)
		return &dumpSession{enabled: false}
	}
	now := time.Now().UTC()
	id := now.Format("20060102_150405.000000000")
	id = strings.ReplaceAll(id, ".", "_")
	d := &dumpSession{
		dir:       dir,
		id:        id,
		startTime: now,
		reqPath:   r.URL.Path,
		inputEst:  inputEst,
		enabled:   true,
	}
	reqMeta := map[string]any{
		"path":         r.URL.Path,
		"method":       r.Method,
		"query":        r.URL.RawQuery,
		"input_est":    inputEst,
		"received_at":  now.Format(time.RFC3339Nano),
		"content_type": r.Header.Get("Content-Type"),
	}
	if mb, err := json.MarshalIndent(reqMeta, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(dir, id+"_request_meta.json"), mb, 0o644)
	}
	if len(body) > 0 {
		_ = os.WriteFile(filepath.Join(dir, id+"_request_body.json"), body, 0o644)
	}
	if f, err := os.Create(filepath.Join(dir, id+"_upstream.txt")); err == nil {
		d.upstream = f
	}
	if f, err := os.Create(filepath.Join(dir, id+"_downstream.txt")); err == nil {
		d.downFile = f
	}
	return d
}

type dumpReader struct {
	src  io.Reader
	sink *dumpSession
}

func (r *dumpReader) Read(p []byte) (int, error) {
	n, err := r.src.Read(p)
	if n > 0 && r.sink != nil && r.sink.upstream != nil {
		_, _ = r.sink.upstream.Write(p[:n])
		r.sink.upBytes += int64(n)
	}
	return n, err
}

func (d *dumpSession) wrapUpstream(src io.Reader) io.Reader {
	if !d.enabled {
		return src
	}
	return &dumpReader{src: src, sink: d}
}

type dumpWriter struct {
	real    http.ResponseWriter
	flusher http.Flusher
	sink    *dumpSession
}

func (w *dumpWriter) Header() http.Header { return w.real.Header() }
func (w *dumpWriter) Write(p []byte) (int, error) {
	n, err := w.real.Write(p)
	if n > 0 && w.sink != nil && w.sink.downFile != nil {
		_, _ = w.sink.downFile.Write(p[:n])
		w.sink.downBytes += int64(n)
	}
	return n, err
}
func (w *dumpWriter) WriteHeader(code int) { w.real.WriteHeader(code) }
func (w *dumpWriter) Flush() {
	if w.flusher != nil {
		w.flusher.Flush()
	}
}

func (d *dumpSession) wrapDownstream(w http.ResponseWriter) http.ResponseWriter {
	if !d.enabled {
		return w
	}
	dw := &dumpWriter{real: w, sink: d}
	if f, ok := w.(http.Flusher); ok {
		dw.flusher = f
	}
	d.downReal = w
	return dw
}

func (d *dumpSession) finalize(statusCode int, clientCancelled bool, copyErr error) {
	if !d.enabled {
		return
	}
	if d.upstream != nil {
		_ = d.upstream.Close()
	}
	if d.downFile != nil {
		_ = d.downFile.Close()
	}
	end := time.Now().UTC()
	meta := map[string]any{
		"id":               d.id,
		"path":             d.reqPath,
		"start":            d.startTime.Format(time.RFC3339Nano),
		"end":              end.Format(time.RFC3339Nano),
		"duration_ms":      end.Sub(d.startTime).Milliseconds(),
		"input_est":        d.inputEst,
		"upstream_bytes":   d.upBytes,
		"downstream_bytes": d.downBytes,
		"status_code":      statusCode,
		"client_cancelled": clientCancelled,
	}
	if copyErr != nil {
		meta["copy_error"] = copyErr.Error()
	}
	if mb, err := json.MarshalIndent(meta, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(d.dir, d.id+"_meta.json"), mb, 0o644)
	}
}
