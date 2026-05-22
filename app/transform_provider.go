package app

import (
	"encoding/json"
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
	case "anthropic":
		return providers
	case "google":
		out := make([]string, 0, len(providers))
		for _, p := range providers {
			switch strings.ToLower(p) {
			case "google", "vertex":
				out = append(out, p)
			}
		}
		return out
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
