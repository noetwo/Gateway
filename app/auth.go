package app

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func requireAuth(token string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions {
				next(w, r)
				return
			}
			if !checkAuthRequest(r, token) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			next(w, r)
		}
	}
}

func requireDynamicAuth(tokenFn func() []string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions {
				next(w, r)
				return
			}
			if !checkAuthRequestAny(r, tokenFn()) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			next(w, r)
		}
	}
}

func requireDynamicAuthFailClosed(tokenFn func() []string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions {
				next(w, r)
				return
			}
			if !checkAuthRequestAnyFailClosed(r, tokenFn()) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			next(w, r)
		}
	}
}

func checkAuthRequest(r *http.Request, token string) bool {
	return checkAuthRequestAny(r, []string{token})
}

func checkAuthRequestAny(r *http.Request, tokens []string) bool {
	tokens = cleanTokens(tokens)
	if len(tokens) == 0 {
		return true
	}
	return checkAuthRequestAnyFailClosed(r, tokens)
}

func checkAuthRequestAnyFailClosed(r *http.Request, tokens []string) bool {
	tokens = cleanTokens(tokens)
	if len(tokens) == 0 {
		return false
	}
	matches := func(provided string) bool {
		provided = strings.TrimSpace(provided)
		if provided == "" {
			return false
		}
		for _, token := range tokens {
			if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1 {
				return true
			}
		}
		return false
	}
	if c, err := r.Cookie("auth_token"); err == nil && c.Value != "" {
		if matches(c.Value) {
			return true
		}
	}
	provided := extractBearer(r.Header.Get("Authorization"))
	if provided == "" {
		provided = strings.TrimSpace(r.Header.Get("X-Auth-Token"))
	}
	if provided == "" {
		provided = strings.TrimSpace(r.Header.Get("X-Api-Key"))
	}
	if provided == "" {
		provided = strings.TrimSpace(r.Header.Get("X-Goog-Api-Key"))
	}
	if provided == "" {
		provided = strings.TrimSpace(r.URL.Query().Get("key"))
	}
	if matches(provided) {
		return true
	}
	return false
}

func handleDynamicLogin(tokenFn func() string) http.HandlerFunc {
	type req struct {
		Token string `json:"token"`
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
		var body req
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		token := tokenFn()
		provided := strings.TrimSpace(body.Token)
		if token == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "auth_token",
			Value:    provided,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   30 * 24 * 3600,
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func handleLogin(token string) http.HandlerFunc {
	type req struct {
		Token string `json:"token"`
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
		var body req
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		provided := strings.TrimSpace(body.Token)
		if token == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "auth_token",
			Value:    provided,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   30 * 24 * 3600,
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func serveIndex(token, loginHTML, indexHTML string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if !checkAuthRequest(r, token) {
			_, _ = io.WriteString(w, loginHTML)
			return
		}
		_, _ = io.WriteString(w, indexHTML)
	}
}

func serveIndexDynamic(tokenFn func() string, loginHTML, indexHTML string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if !checkAuthRequest(r, tokenFn()) {
			_, _ = io.WriteString(w, loginHTML)
			return
		}
		_, _ = io.WriteString(w, indexHTML)
	}
}

func extractBearer(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}
	if len(h) >= 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return h
}
