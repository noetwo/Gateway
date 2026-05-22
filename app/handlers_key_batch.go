package app

import (
	"encoding/json"
	"io"
	"net/http"
	"time"
)

func handleKeysBatch(state *AppState) http.HandlerFunc {
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
			Action string   `json:"action"`
			IDs    []string `json:"ids"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		if len(req.IDs) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ids required"})
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		now := time.Now().UTC()
		processed := 0
		missing := 0
		switch req.Action {
		case "delete":
			for _, id := range req.IDs {
				if _, ok := state.Keys[id]; ok {
					delete(state.Keys, id)
					if state.StickyKeyID == id {
						state.StickyKeyID = ""
					}
					processed++
				} else {
					missing++
				}
			}
			state.renumberKeys()
		case "scrap":
			for _, id := range req.IDs {
				k, ok := state.Keys[id]
				if !ok {
					missing++
					continue
				}
				if !k.Scrapped {
					k.Scrapped = true
					k.ScrappedErr = "手动报废"
					k.ScrappedAt = now
					k.Paused = true
					k.UpdatedAt = now
					if state.StickyKeyID == id {
						state.StickyKeyID = ""
					}
				}
				processed++
			}
			state.renumberKeys()
		case "restore":
			for _, id := range req.IDs {
				k, ok := state.Keys[id]
				if !ok {
					missing++
					continue
				}
				if k.Scrapped {
					k.Scrapped = false
					k.ScrappedErr = ""
					k.ScrappedAt = time.Time{}
					k.Paused = false
					k.MonthlySpentUSD = 0
					k.ConsecFails = 0
					k.LastErr = ""
					k.UpdatedAt = now
				}
				processed++
			}
			state.renumberKeys()
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
			return
		}
		if err := state.save(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"processed": processed, "missing": missing})
	}
}
