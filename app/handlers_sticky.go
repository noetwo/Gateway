package app

import (
	"encoding/json"
	"io"
	"net/http"
)

func handleStickyToggle(state *AppState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		state.StickyMode = !state.StickyMode
		if state.StickyMode {
			if state.StickyKeyID == "" || !state.stickyKeyAvailableLocked(state.StickyKeyID) {
				state.StickyKeyID = state.firstAvailableStickyKeyIDLocked()
			}
		} else {
			state.StickyKeyID = ""
		}
		if err := state.save(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		stickyName := ""
		if state.StickyMode && state.StickyKeyID != "" {
			if k, ok := state.Keys[state.StickyKeyID]; ok {
				stickyName = k.Name
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"sticky_mode":     state.StickyMode,
			"sticky_key_id":   state.StickyKeyID,
			"sticky_key_name": stickyName,
		})
	}
}

func handleStickySelect(state *AppState) http.HandlerFunc {
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
			ID string `json:"id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		k, ok := state.Keys[req.ID]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "key not found"})
			return
		}
		if k.Scrapped || k.Paused {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "该 key 已冷却或报废，无法锁定"})
			return
		}
		state.StickyMode = true
		state.StickyKeyID = req.ID
		if err := state.save(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"sticky_mode":     true,
			"sticky_key_id":   req.ID,
			"sticky_key_name": k.Name,
		})
	}
}
