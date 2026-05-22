package app

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func handleKeys(state *AppState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req CreateKeyReq
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		apiKey := strings.TrimSpace(req.APIKey)
		if apiKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "api_key required"})
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		name := strings.TrimSpace(req.Name)
		tier := strings.TrimSpace(req.Tier)
		// 前端用 name="auto" 表示「没提供名称，请自动编号」（兼容旧行为）；
		// 显式给名称/等级时按给的来，name 可以为空字符串。
		if name == "auto" {
			maxNum := 0
			for _, k := range state.Keys {
				n, err := strconv.Atoi(k.Name)
				if err == nil && n > maxNum && !k.Scrapped {
					maxNum = n
				}
			}
			name = strconv.Itoa(maxNum + 1)
		}
		now := time.Now().UTC()
		id := strconv.FormatInt(now.UnixNano(), 36)
		state.Keys[id] = &Key{ID: id, Name: name, Tier: tier, APIKey: apiKey, CreatedAt: now, UpdatedAt: now}
		if err := state.save(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, publicView(state.Keys[id]))
	}
}

// handleKeysBulk 接受多行文本，每行格式：[name] [tier] vck_xxx
// 也兼容纯 key 一行；同一行多个 vck_ 时只有第一个能带 name/tier，其余字段留空。
func handleKeysBulk(state *AppState) http.HandlerFunc {
	vckRE := regexp.MustCompile(`^vck_[A-Za-z0-9]+$`)
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
			Raw string `json:"raw"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}

		type parsedKey struct{ name, tier, apiKey string }
		var parsed []parsedKey
		for _, line := range strings.Split(req.Raw, "\n") {
			parts := strings.Fields(line)
			if len(parts) == 0 {
				continue
			}
			firstIdx := -1
			for i, p := range parts {
				if vckRE.MatchString(p) {
					firstIdx = i
					break
				}
			}
			if firstIdx < 0 {
				continue
			}
			name, tier := "", ""
			before := parts[:firstIdx]
			if len(before) >= 1 {
				name = before[0]
			}
			if len(before) >= 2 {
				tier = before[1]
			}
			parsed = append(parsed, parsedKey{name, tier, parts[firstIdx]})
			for i := firstIdx + 1; i < len(parts); i++ {
				if vckRE.MatchString(parts[i]) {
					parsed = append(parsed, parsedKey{"", "", parts[i]})
				}
			}
		}
		if len(parsed) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no vck_ keys found"})
			return
		}

		state.mu.Lock()
		defer state.mu.Unlock()
		added := 0
		now := time.Now().UTC()
		for _, pk := range parsed {
			dup := false
			for _, k := range state.Keys {
				if k.APIKey == pk.apiKey {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			id := strconv.FormatInt(now.UnixNano()+int64(added), 36)
			state.Keys[id] = &Key{ID: id, Name: pk.name, Tier: pk.tier, APIKey: pk.apiKey, CreatedAt: now, UpdatedAt: now}
			added++
		}
		if err := state.save(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"added": added, "total_found": len(parsed)})
	}
}
