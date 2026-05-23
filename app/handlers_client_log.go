package app

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
)

type clientLogReq struct {
	Level   string         `json:"level"`
	Scope   string         `json:"scope"`
	Action  string         `json:"action"`
	Path    string         `json:"path"`
	Href    string         `json:"href"`
	Method  string         `json:"method"`
	Status  int            `json:"status"`
	Message string         `json:"message"`
	Body    string         `json:"body"`
	Stack   string         `json:"stack"`
	Extra   map[string]any `json:"extra"`
}

func handleClientLog() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		var req clientLogReq
		dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
		dec.UseNumber()
		if err := dec.Decode(&req); err != nil {
			log.Printf("[client] invalid json remote=%s err=%v", r.RemoteAddr, err)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}

		extra := ""
		if len(req.Extra) > 0 {
			if b, err := json.Marshal(req.Extra); err == nil {
				extra = cleanClientLogString(string(b), 1000)
			}
		}

		log.Printf(
			"[client] level=%s scope=%s action=%s method=%s path=%s status=%d href=%s remote=%s ua=%q message=%q body=%q stack=%q extra=%s",
			cleanClientLogToken(req.Level, "info"),
			cleanClientLogToken(req.Scope, "ui"),
			cleanClientLogToken(req.Action, "unknown"),
			cleanClientLogToken(req.Method, ""),
			cleanClientLogString(req.Path, 300),
			req.Status,
			cleanClientLogString(req.Href, 300),
			r.RemoteAddr,
			cleanClientLogString(r.UserAgent(), 300),
			cleanClientLogString(req.Message, 500),
			cleanClientLogString(req.Body, 500),
			cleanClientLogString(req.Stack, 1200),
			extra,
		)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func cleanClientLogToken(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	s = cleanClientLogString(s, 64)
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '=' || r == '"' {
			return '-'
		}
		return r
	}, s)
}

func cleanClientLogString(s string, max int) string {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(s)
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	if max > 0 && len(s) > max {
		return s[:max] + "..."
	}
	return s
}
