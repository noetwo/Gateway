package app

import (
	"bufio"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func parseTokenList(raw string) []string {
	return cleanTokens(strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t'
	}))
}

func cleanTokens(tokens []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	return out
}

func normalizeStatusCodesOrDefault(raw, fallback string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return strings.TrimSpace(fallback)
	}
	codes, err := normalizeRetryStatusCodes(raw)
	if err != nil {
		return strings.TrimSpace(fallback)
	}
	return codes
}

func loadConfigFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			runtimePath := filepath.Join(getenvDefault("STATE_DIR", defaultStateDirName), defaultRuntimeConfigFile)
			if _, statErr := os.Stat(runtimePath); statErr == nil {
				return
			}
			log.Fatalf("config file %s not found in working directory and runtime config is missing; copy the template from the repo and fill in WEB_AUTH_TOKEN before starting", path)
		}
		log.Fatalf("open config file %s: %v", path, err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		v = strings.Trim(v, "\"'")
		if k == "" {
			continue
		}
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}
}

func getenvDefault(k, fallback string) string {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return fallback
	}
	return v
}

func getenvIntDefault(k string, fallback, min, max int) int {
	v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(k)))
	if err != nil {
		return fallback
	}
	if v < min {
		return min
	}
	if max > 0 && v > max {
		return max
	}
	return v
}
