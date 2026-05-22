package app

import (
	"net/http"
	"sort"
	"strings"
)

func handleExportKeys(state *AppState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		state.mu.RLock()
		keys := make([]*Key, 0, len(state.Keys))
		for _, k := range state.Keys {
			keys = append(keys, k)
		}
		state.mu.RUnlock()
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].Name == keys[j].Name {
				return keys[i].ID < keys[j].ID
			}
			return keys[i].Name < keys[j].Name
		})
		var b strings.Builder
		for _, k := range keys {
			b.WriteString(strings.ReplaceAll(k.Name, "\t", " "))
			b.WriteByte('\t')
			b.WriteString(strings.ReplaceAll(k.Tier, "\t", " "))
			b.WriteByte('\t')
			b.WriteString(k.APIKey)
			b.WriteByte('\n')
		}
		w.Header().Set("Content-Type", "text/tab-separated-values; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="keys.tsv"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(b.String()))
	}
}
