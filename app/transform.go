package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
)

// injectProviderOrder 处理路径前缀指定的渠道（vertex/bedrock/anthropic/azure）。
//
// Vercel 实际机制：模型 id 是规范的 `{namespace}/{name}`（anthropic/claude-*, google/gemini-*, openai/gpt-*），
// 真正的渠道路由通过 body 的 `providerOptions.gateway.order` 数组传递。把模型名前缀写成 `vertex/claude-*`
// 是错的——Vercel 没这种 id（404），而 `bedrock/claude-*` 虽然不报错但 Vercel 会悄悄退到 anthropic。
func injectProviderOrder(body []byte, order string) []byte {
	if order == "" {
		return body
	}
	providers := []string{}
	for _, p := range strings.Split(order, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			providers = append(providers, p)
		}
	}
	if len(providers) == 0 {
		return body
	}

	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	// 1. 规范化模型 namespace。客户端传裸名（claude-sonnet-4 / gemini-2.5-pro / gpt-4o）时补上正确前缀；
	//    已有前缀则保持原样（包括用户自己写的 anthropic/、google/、openai/ 等）。
	if modelStr, ok := req["model"].(string); ok && modelStr != "" {
		if !strings.Contains(modelStr, "/") {
			if ns := canonicalNamespace(modelStr); ns != "" {
				req["model"] = ns + "/" + modelStr
			}
		}
	}

	// 2. 仅对已知 namespace 的模型注入 providerOptions.gateway.order。
	//    OpenAI namespace 只保留 azure/openai，避免把 GPT 强锁到 bedrock/anthropic/vertex。
	modelStr, _ := req["model"].(string)
	modelNS := ""
	if idx := strings.Index(modelStr, "/"); idx > 0 {
		modelNS = strings.ToLower(modelStr[:idx])
	}
	providers = providersForModelNamespace(providers, modelNS)
	if len(providers) == 0 {
		out, err := json.Marshal(req)
		if err != nil {
			return body
		}
		return out
	}

	po, _ := req["providerOptions"].(map[string]any)
	if po == nil {
		po = map[string]any{}
	}
	gw, _ := po["gateway"].(map[string]any)
	if gw == nil {
		gw = map[string]any{}
	}
	orderArr := make([]any, len(providers))
	for i, p := range providers {
		orderArr[i] = p
	}
	gw["order"] = orderArr
	po["gateway"] = gw
	req["providerOptions"] = po

	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}

func providersForModelNamespace(providers []string, namespace string) []string {
	switch namespace {
	case "anthropic", "google":
		return providers
	case "openai":
		out := make([]string, 0, len(providers))
		for _, p := range providers {
			switch strings.ToLower(p) {
			case "azure", "openai":
				out = append(out, p)
			}
		}
		return out
	default:
		return nil
	}
}

func canonicalNamespace(name string) string {
	lower := strings.ToLower(name)
	switch {
	case isOpenAIOnlyModel(name):
		return "openai"
	case strings.Contains(lower, "claude"):
		return "anthropic"
	case strings.Contains(lower, "gemini"):
		return "google"
	}
	return ""
}

func isOpenAIOnlyModel(model string) bool {
	lower := strings.ToLower(model)
	return strings.HasPrefix(lower, "gpt-image") ||
		strings.HasPrefix(lower, "dall-e") ||
		strings.HasPrefix(lower, "gpt-4o") ||
		strings.HasPrefix(lower, "gpt-4-") ||
		strings.HasPrefix(lower, "gpt-4.") ||
		strings.HasPrefix(lower, "gpt-5") ||
		strings.HasPrefix(lower, "o1") ||
		strings.HasPrefix(lower, "o3") ||
		strings.HasPrefix(lower, "o4-")
}

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

	if strings.Contains(path, "/messages") && hasAnthropicProviderThinking(req) {
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
				injectAnthropicThinking(req, effortToBudget(effortStr))
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
				injectAnthropicThinking(req, effortToBudget(defaultEffort))
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

func hasAnthropicProviderThinking(req map[string]any) bool {
	po, _ := req["providerOptions"].(map[string]any)
	anthropic, _ := po["anthropic"].(map[string]any)
	_, ok := anthropic["thinking"]
	return ok
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
			injectAnthropicThinking(req, matched.budget)
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

// rewriteImageEditsToGenerations 把 /v1/images/edits 请求转成 /v1/images/generations
func rewriteImageEditsToGenerations(body []byte, r *http.Request) ([]byte, *http.Request) {
	ct := r.Header.Get("Content-Type")

	var prompt, model, size, quality string
	var n int
	var images []string

	if strings.HasPrefix(ct, "multipart/") {
		boundary := ""
		for _, p := range strings.Split(ct, ";") {
			p = strings.TrimSpace(p)
			if strings.HasPrefix(p, "boundary=") {
				boundary = strings.TrimPrefix(p, "boundary=")
				boundary = strings.Trim(boundary, "\"")
			}
		}
		if boundary != "" {
			mr := multipart.NewReader(bytes.NewReader(body), boundary)
			for {
				part, err := mr.NextPart()
				if err != nil {
					break
				}
				name := part.FormName()
				switch name {
				case "prompt":
					val, _ := io.ReadAll(io.LimitReader(part, 1<<20))
					prompt = string(val)
				case "model":
					val, _ := io.ReadAll(io.LimitReader(part, 1<<16))
					model = string(val)
				case "size":
					val, _ := io.ReadAll(io.LimitReader(part, 1<<16))
					size = string(val)
				case "quality":
					val, _ := io.ReadAll(io.LimitReader(part, 1<<16))
					quality = string(val)
				case "n":
					val, _ := io.ReadAll(io.LimitReader(part, 1<<16))
					n, _ = strconv.Atoi(string(val))
				case "image", "image[]":
					imgData, _ := io.ReadAll(io.LimitReader(part, 10<<20))
					if len(imgData) > 0 {
						encoded := base64Encode(imgData)
						images = append(images, encoded)
					}
				}
				part.Close()
			}
		}
	} else {
		var req map[string]any
		if err := json.Unmarshal(body, &req); err == nil {
			if v, ok := req["prompt"].(string); ok {
				prompt = v
			}
			if v, ok := req["model"].(string); ok {
				model = v
			}
			if v, ok := req["size"].(string); ok {
				size = v
			}
			if v, ok := req["quality"].(string); ok {
				quality = v
			}
			if v, ok := req["n"].(float64); ok {
				n = int(v)
			}
			switch img := req["image"].(type) {
			case string:
				images = append(images, img)
			case []any:
				for _, item := range img {
					if s, ok := item.(string); ok {
						images = append(images, s)
					}
				}
			}
		}
	}

	if prompt == "" {
		prompt = "generate an image"
	}
	if model == "" {
		model = "gpt-image-1"
	}
	if n <= 0 {
		n = 1
	}

	genReq := map[string]any{
		"model":  model,
		"prompt": prompt,
		"n":      n,
	}
	if size != "" {
		genReq["size"] = size
	}
	if quality != "" {
		genReq["quality"] = quality
	}
	if len(images) > 0 {
		genReq["image"] = images
	}

	newBody, err := json.Marshal(genReq)
	if err != nil {
		return body, r
	}

	r2 := r.Clone(r.Context())
	r2.URL.Path = "/v1/images/generations"
	r2.Header.Set("Content-Type", "application/json")
	imgInfo := "none"
	if len(images) > 0 {
		imgInfo = fmt.Sprintf("%d images", len(images))
	}
	log.Printf("[image-rewrite] /v1/images/edits → /v1/images/generations model=%s images=%s prompt=%s", model, imgInfo, truncate(prompt, 80))
	return newBody, r2
}

func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func injectImageDefaults(body []byte) []byte {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	changed := false
	if _, ok := req["quality"]; !ok {
		req["quality"] = "high"
		changed = true
	}
	if _, ok := req["size"]; !ok {
		req["size"] = "3840x2160"
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
