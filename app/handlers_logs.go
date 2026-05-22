package app

import (
	"net/http"
	"strconv"
)

func handleLogs(ring *ProxyLogRing) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		n := 200
		if qs := r.URL.Query().Get("n"); qs != "" {
			if v, err := strconv.Atoi(qs); err == nil && v > 0 {
				n = v
			}
		}
		logs := ring.Recent(n)
		if logs == nil {
			logs = []ProxyLog{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"logs": logs, "count": len(logs)})
	}
}
