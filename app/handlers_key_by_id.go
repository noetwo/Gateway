package app

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

func handleKeyByID(state *AppState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/keys/")
		if id == "" || strings.Contains(id, "/") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}

		switch r.Method {
		case http.MethodGet:
			state.mu.RLock()
			defer state.mu.RUnlock()
			k, ok := state.Keys[id]
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
				return
			}
			view := publicView(k)
			view.APIKey = k.APIKey
			writeJSON(w, http.StatusOK, view)

		case http.MethodPatch:
			var req UpdateKeyReq
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
				return
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			k, ok := state.Keys[id]
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
				return
			}
			if req.Name != nil {
				k.Name = strings.TrimSpace(*req.Name)
			}
			if req.Tier != nil {
				k.Tier = strings.TrimSpace(*req.Tier)
			}
			if req.APIKey != nil {
				v := strings.TrimSpace(*req.APIKey)
				if v == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "api_key cannot be empty"})
					return
				}
				k.APIKey = v
			}
			if req.Paused != nil {
				k.Paused = *req.Paused
				if *req.Paused && state.StickyMode && state.StickyKeyID == id {
					state.StickyKeyID = ""
				}
			}
			if req.ResetCost != nil && *req.ResetCost {
				k.MonthlySpentUSD = 0
				k.Paused = false
			}
			if req.Scrapped != nil {
				k.Scrapped = *req.Scrapped
				if *req.Scrapped {
					k.ScrappedErr = "手动报废"
					k.ScrappedAt = time.Now().UTC()
					k.Paused = true
					if state.StickyMode && state.StickyKeyID == id {
						state.StickyKeyID = ""
					}
				} else {
					k.ScrappedErr = ""
					k.ScrappedAt = time.Time{}
					k.Paused = false
					k.MonthlySpentUSD = 0
					k.ConsecFails = 0
					k.LastErr = ""
				}
				state.renumberKeys()
			}
			k.UpdatedAt = time.Now().UTC()
			if err := state.save(); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, publicView(k))

		case http.MethodDelete:
			state.mu.Lock()
			defer state.mu.Unlock()
			if _, ok := state.Keys[id]; !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
				return
			}
			delete(state.Keys, id)
			if state.StickyKeyID == id {
				state.StickyKeyID = ""
			}
			state.renumberKeys()
			if err := state.save(); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})

		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}
