package app

import (
	"encoding/json"
	"strings"
)

// estimateTokens: char-based heuristic tokenizer.
// CJK / Hangul / 假名 ≈ 1 token/字, 其他 ≈ 1 token/4 字符
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	cjk := 0
	other := 0
	for _, r := range s {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF,
			r >= 0x3400 && r <= 0x4DBF,
			r >= 0x3040 && r <= 0x30FF,
			r >= 0xAC00 && r <= 0xD7AF,
			r >= 0xFF00 && r <= 0xFFEF:
			cjk++
		default:
			other++
		}
	}
	return cjk + (other+3)/4
}

func estimateContentTokens(content any) int {
	switch v := content.(type) {
	case string:
		return estimateTokens(v)
	case []any:
		sum := 0
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := m["text"].(string); ok {
				sum += estimateTokens(t)
			}
			if t, ok := m["thinking"].(string); ok {
				sum += estimateTokens(t)
			}
			if tp, ok := m["type"].(string); ok {
				if tp == "image" || tp == "image_url" || tp == "input_image" {
					sum += 1500
				}
				if tp == "file" {
					if mt, _ := m["mediaType"].(string); strings.HasPrefix(strings.ToLower(mt), "image/") {
						sum += 1500
					}
				}
			}
		}
		return sum
	}
	return 0
}

func estimateInputTokensFromBody(body []byte) int {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return 0
	}
	total := 10
	if sys, ok := req["system"]; ok {
		total += estimateContentTokens(sys)
	}
	if msgs, ok := req["messages"].([]any); ok {
		for _, m := range msgs {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			if c, ok := mm["content"]; ok {
				total += estimateContentTokens(c)
			}
			total += 4
		}
	}
	if tools, ok := req["tools"].([]any); ok {
		for _, t := range tools {
			if b, err := json.Marshal(t); err == nil {
				total += estimateTokens(string(b))
			}
		}
	}
	return total
}

func ensureStreamUsage(body []byte, path string) []byte {
	if !strings.Contains(path, "chat/completions") {
		return body
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	if s, ok := req["stream"].(bool); !ok || !s {
		return body
	}
	so, _ := req["stream_options"].(map[string]any)
	if so == nil {
		so = map[string]any{}
	}
	if _, has := so["include_usage"]; !has {
		so["include_usage"] = true
	}
	req["stream_options"] = so
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}

func getFloat(m map[string]any, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case json.Number:
		f, _ := v.Float64()
		return f
	}
	return 0
}
