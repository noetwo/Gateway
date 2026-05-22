package app

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func normalizeRetryStatusCodes(raw string) (string, error) {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	seen := map[string]bool{}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if p == "5xx" {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
			continue
		}
		code, err := strconv.Atoi(p)
		if err != nil || code < 100 || code > 599 {
			return "", fmt.Errorf("invalid retry status code: %s", p)
		}
		normalized := strconv.Itoa(code)
		if !seen[normalized] {
			seen[normalized] = true
			out = append(out, normalized)
		}
	}
	if len(out) == 0 {
		return "", errors.New("retry status codes cannot be empty")
	}
	return strings.Join(out, ","), nil
}

func parseModelPatternsText(raw string) []string {
	return normalizeModelPatterns([]string{raw}, false)
}

func normalizePreferredTier(raw string) string {
	tier := strings.ToLower(strings.TrimSpace(raw))
	switch tier {
	case "team", "hobby":
		return tier
	default:
		return defaultPreferredTier
	}
}

func clampRetryAttempts(n int) int {
	if n <= 0 {
		return defaultMaxRetryAttempts
	}
	if n > maxAllowedRetryAttempts {
		return maxAllowedRetryAttempts
	}
	return n
}

func (s *AppState) normalizeRetrySettings() {
	codes, err := normalizeRetryStatusCodes(s.RetryCodes)
	if err != nil {
		codes = defaultRetryStatusCodes
	}
	s.RetryCodes = codes
	s.MaxAttempts = clampRetryAttempts(s.MaxAttempts)
}

func (s *AppState) retrySettings() (string, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	codes := s.RetryCodes
	if strings.TrimSpace(codes) == "" {
		codes = defaultRetryStatusCodes
	}
	return codes, clampRetryAttempts(s.MaxAttempts)
}

func retryStatusEnabled(code int, raw string) bool {
	parts := strings.Split(raw, ",")
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p == "" {
			continue
		}
		if p == "5xx" && code >= 500 && code <= 599 {
			return true
		}
		n, err := strconv.Atoi(p)
		if err == nil && n == code {
			return true
		}
	}
	return false
}
