package app

import (
	"encoding/json"
	"strings"
)

func transformReasoning(body []byte, defaultEffort, path string) []byte {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	if _, hasThinking := req["thinking"]; hasThinking {
		changed := stripThinkingSamplingFields(req)
		if !changed {
			return body
		}
		out, err := json.Marshal(req)
		if err != nil {
			return body
		}
		return out
	}

	if strings.Contains(path, "/messages") && hasMessagesProviderReasoning(req) {
		changed := stripThinkingSamplingFields(req)
		if !changed {
			return body
		}
		out, err := json.Marshal(req)
		if err != nil {
			return body
		}
		return out
	}

	if _, ok := req["reasoning"]; ok {
		return body
	}

	if re, ok := req["reasoning_effort"]; ok {
		if effortStr, ok := re.(string); ok && strings.TrimSpace(effortStr) != "" {
			if strings.Contains(path, "/messages") {
				injectMessagesReasoning(req, effortStr)
				stripThinkingSamplingFields(req)
			} else {
				req["reasoning"] = map[string]any{
					"enabled": true,
					"effort":  effortStr,
				}
			}
			delete(req, "reasoning_effort")
			out, err := json.Marshal(req)
			if err != nil {
				return body
			}
			return out
		}
	}

	if defaultEffort != "" && defaultEffort != "off" && defaultEffort != "none" {
		modelStr, _ := req["model"].(string)
		if modelSupportsReasoning(modelStr) {
			switch {
			case strings.Contains(path, "/messages"):
				injectMessagesReasoning(req, defaultEffort)
				stripThinkingSamplingFields(req)
			case strings.Contains(path, "chat/completions"):
				req["reasoning"] = map[string]any{
					"enabled": true,
					"effort":  defaultEffort,
				}
			default:
				return body
			}
			out, err := json.Marshal(req)
			if err != nil {
				return body
			}
			return out
		}
	}

	return body
}

type thinkingTier struct {
	suffix string
	budget int
	effort string
}

func thinkingSuffixTiers() []thinkingTier {
	return []thinkingTier{
		{"-thinking-minimal", 1024, "minimal"},
		{"-thinking-medium", 4000, "medium"},
		{"-thinking-xhigh", 16000, "xhigh"},
		{"-thinking-high", 8000, "high"},
		{"-thinking-mid", 4000, "medium"},
		{"-thinking-max", 32000, "xhigh"},
		{"-thinking-low", 2048, "low"},
		{"-minimal", 1024, "minimal"},
		{"-medium", 4000, "medium"},
		{"-xhigh", 16000, "xhigh"},
		{"-high", 8000, "high"},
		{"-low", 2048, "low"},
		{"-thinking", 8000, "high"},
	}
}

func matchThinkingSuffix(model string) (thinkingTier, bool) {
	var matched thinkingTier
	found := false
	for _, tier := range thinkingSuffixTiers() {
		if strings.HasSuffix(model, tier.suffix) {
			if !found || len(tier.suffix) > len(matched.suffix) {
				matched = tier
				found = true
			}
		}
	}
	return matched, found
}

func stripThinkingSuffix(model string) string {
	if tier, ok := matchThinkingSuffix(model); ok {
		return strings.TrimSuffix(model, tier.suffix)
	}
	return model
}

func injectMessagesReasoning(req map[string]any, effort string) {
	modelStr, _ := req["model"].(string)
	if modelNamespace(modelStr) == "openai" {
		injectOpenAIReasoning(req, effort)
		return
	}
	injectAnthropicThinking(req, effortToBudget(effort))
}

func modelNamespace(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if idx := strings.Index(model, "/"); idx > 0 {
		return strings.ToLower(model[:idx])
	}
	return canonicalNamespace(model)
}

func injectOpenAIReasoning(req map[string]any, effort string) {
	po, _ := req["providerOptions"].(map[string]any)
	if po == nil {
		po = map[string]any{}
	}
	openai, _ := po["openai"].(map[string]any)
	if openai == nil {
		openai = map[string]any{}
	}
	openai["reasoningEffort"] = effort
	openai["reasoningSummary"] = "detailed"
	po["openai"] = openai
	req["providerOptions"] = po
}

func injectAnthropicThinking(req map[string]any, budgetTokens int) {
	po, _ := req["providerOptions"].(map[string]any)
	if po == nil {
		po = map[string]any{}
	}
	anthropic, _ := po["anthropic"].(map[string]any)
	if anthropic == nil {
		anthropic = map[string]any{}
	}
	anthropic["thinking"] = map[string]any{
		"type":         "enabled",
		"budgetTokens": budgetTokens,
	}
	po["anthropic"] = anthropic
	req["providerOptions"] = po
}

func hasMessagesProviderReasoning(req map[string]any) bool {
	po, _ := req["providerOptions"].(map[string]any)
	anthropic, _ := po["anthropic"].(map[string]any)
	if _, ok := anthropic["thinking"]; ok {
		return true
	}
	openai, _ := po["openai"].(map[string]any)
	if _, ok := openai["reasoningEffort"]; ok {
		return true
	}
	if _, ok := openai["reasoningSummary"]; ok {
		return true
	}
	return false
}

func stripThinkingSamplingFields(req map[string]any) bool {
	changed := false
	for _, k := range []string{"temperature", "top_p", "top_k"} {
		if _, ok := req[k]; ok {
			delete(req, k)
			changed = true
		}
	}
	return changed
}

// rewriteThinkingSuffix 模型名是唯一权威:
//   - 有 -thinking-* 后缀: 剥掉后缀, 按等级注入 thinking/reasoning
//   - 无后缀: 保持原样透传，由客户端自己控制
func rewriteThinkingSuffix(body []byte, path string) []byte {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	modelStr, ok := req["model"].(string)
	if !ok || modelStr == "" {
		return body
	}

	changed := false

	if matched, ok := matchThinkingSuffix(modelStr); ok {
		req["model"] = strings.TrimSuffix(modelStr, matched.suffix)
		delete(req, "reasoning_effort")
		delete(req, "reasoning")
		delete(req, "thinking")
		switch {
		case strings.Contains(path, "/messages"):
			injectMessagesReasoning(req, matched.effort)
			stripThinkingSamplingFields(req)
		case strings.Contains(path, "chat/completions"):
			req["reasoning"] = map[string]any{
				"enabled": true,
				"effort":  matched.effort,
			}
		}
		changed = true
	}

	if !changed {
		return body
	}
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}

// sanitizeForVercel 剥掉 Vercel 严格校验会炸的 0 值字段
func sanitizeForVercel(body []byte) []byte {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	changed := false
	stripIfZeroOrNeg := func(key string) {
		v, ok := req[key]
		if !ok {
			return
		}
		var f float64
		valid := false
		switch x := v.(type) {
		case float64:
			f, valid = x, true
		case json.Number:
			if n, err := x.Float64(); err == nil {
				f, valid = n, true
			}
		case int:
			f, valid = float64(x), true
		case int64:
			f, valid = float64(x), true
		}
		if valid && f <= 0 {
			delete(req, key)
			changed = true
		}
	}
	for _, k := range []string{"top_k", "top_p", "max_tokens", "max_completion_tokens", "n"} {
		stripIfZeroOrNeg(k)
	}
	if v, ok := req["temperature"]; ok {
		var f float64
		valid := false
		switch x := v.(type) {
		case float64:
			f, valid = x, true
		case json.Number:
			if n, err := x.Float64(); err == nil {
				f, valid = n, true
			}
		}
		if valid && f < 0 {
			delete(req, "temperature")
			changed = true
		}
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}

func effortToBudget(effort string) int {
	switch strings.ToLower(effort) {
	case "minimal":
		return 1024
	case "low":
		return 2048
	case "medium":
		return 4000
	case "high":
		return 8000
	case "xhigh":
		return 16000
	}
	return 4000
}

func modelSupportsReasoning(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	parts := strings.Split(m, "/")
	name := parts[len(parts)-1]
	prefixes := []string{
		"o1", "o3", "o4-",
		"gpt-5",
		"claude-opus-4", "claude-sonnet-4",
		"claude-3-7-sonnet", "claude-3.7-sonnet",
		"gemini-2.5-pro", "gemini-3",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return strings.Contains(name, "thinking")
}

func requestWantsStream(body []byte) bool {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	v, ok := req["stream"]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

func extractModelFromBody(body []byte) string {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	if m, ok := req["model"].(string); ok {
		return m
	}
	return ""
}
