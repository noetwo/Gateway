package app

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
)

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
