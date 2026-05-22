package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

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
