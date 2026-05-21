package app

import (
	"encoding/json"
	"testing"
)

func TestInjectProviderOrderAddsAzureForOpenAIModels(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","messages":[]}`)

	req := decodeTransformedBody(t, injectProviderOrder(body, "azure"))

	if got := req["model"]; got != "openai/gpt-5.5" {
		t.Fatalf("model = %v", got)
	}
	assertGatewayOrder(t, req, []string{"azure"})
}

func TestInjectProviderOrderFiltersOpenAIProviders(t *testing.T) {
	body := []byte(`{"model":"openai/gpt-5.5","messages":[]}`)

	req := decodeTransformedBody(t, injectProviderOrder(body, "bedrock,azure,openai,anthropic"))

	assertGatewayOrder(t, req, []string{"azure", "openai"})
}

func TestInjectProviderOrderSkipsUnsupportedOpenAIProviders(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","messages":[]}`)

	req := decodeTransformedBody(t, injectProviderOrder(body, "bedrock"))

	if got := req["model"]; got != "openai/gpt-5.5" {
		t.Fatalf("model = %v", got)
	}
	if _, ok := req["providerOptions"]; ok {
		t.Fatalf("providerOptions was injected for unsupported OpenAI provider: %#v", req["providerOptions"])
	}
}

func TestTransformReasoningAddsDefaultOpenAIReasoningForMessages(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","messages":[],"temperature":0.2,"top_p":1,"top_k":5,"providerOptions":{"gateway":{"order":["azure"]}}}`)

	req := decodeTransformedBody(t, transformReasoning(body, "high", "/v1/messages"))

	assertOpenAIReasoning(t, req, "high")
	assertGatewayOrder(t, req, []string{"azure"})
	assertMissingField(t, req, "temperature")
	assertMissingField(t, req, "top_p")
	assertMissingField(t, req, "top_k")
	assertMissingField(t, req, "reasoning")
}

func TestTransformReasoningAddsDefaultAnthropicThinkingForMessages(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4.6","messages":[]}`)

	req := decodeTransformedBody(t, transformReasoning(body, "high", "/v1/messages"))

	assertAnthropicThinking(t, req, 8000)
}

func TestTransformReasoningDoesNotOverrideExistingMessagesThinking(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4.6","messages":[],"temperature":0.2,"providerOptions":{"anthropic":{"thinking":{"type":"enabled","budgetTokens":1234}}}}`)

	req := decodeTransformedBody(t, transformReasoning(body, "high", "/v1/messages"))

	assertAnthropicThinking(t, req, 1234)
	assertMissingField(t, req, "temperature")
}

func TestRewriteThinkingSuffixAcceptsConfigStyleSuffixes(t *testing.T) {
	body := []byte(`{"model":"openai/gpt-5.5-xhigh","messages":[],"temperature":0.2}`)

	req := decodeTransformedBody(t, rewriteThinkingSuffix(body, "/v1/messages"))

	if got := req["model"]; got != "openai/gpt-5.5" {
		t.Fatalf("model = %v", got)
	}
	assertOpenAIReasoning(t, req, "xhigh")
	assertMissingField(t, req, "temperature")
}

func TestRewriteThinkingSuffixUsesLongestMatch(t *testing.T) {
	body := []byte(`{"model":"openai/gpt-5.5-thinking-high","messages":[]}`)

	req := decodeTransformedBody(t, rewriteThinkingSuffix(body, "/v1/chat/completions"))

	if got := req["model"]; got != "openai/gpt-5.5" {
		t.Fatalf("model = %v", got)
	}
	reasoning, ok := req["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("reasoning missing or wrong type: %#v", req["reasoning"])
	}
	if got := reasoning["effort"]; got != "high" {
		t.Fatalf("reasoning.effort = %v, want high", got)
	}
}

func decodeTransformedBody(t *testing.T, body []byte) map[string]any {
	t.Helper()

	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	return req
}

func assertGatewayOrder(t *testing.T, req map[string]any, want []string) {
	t.Helper()

	po, ok := req["providerOptions"].(map[string]any)
	if !ok {
		t.Fatalf("providerOptions missing or wrong type: %#v", req["providerOptions"])
	}
	gw, ok := po["gateway"].(map[string]any)
	if !ok {
		t.Fatalf("providerOptions.gateway missing or wrong type: %#v", po["gateway"])
	}
	order, ok := gw["order"].([]any)
	if !ok {
		t.Fatalf("providerOptions.gateway.order missing or wrong type: %#v", gw["order"])
	}
	if len(order) != len(want) {
		t.Fatalf("order length = %d, want %d: %#v", len(order), len(want), order)
	}
	for i, w := range want {
		if got := order[i]; got != w {
			t.Fatalf("order[%d] = %v, want %s", i, got, w)
		}
	}
}

func assertAnthropicThinking(t *testing.T, req map[string]any, wantBudget int) {
	t.Helper()

	po, ok := req["providerOptions"].(map[string]any)
	if !ok {
		t.Fatalf("providerOptions missing or wrong type: %#v", req["providerOptions"])
	}
	anthropic, ok := po["anthropic"].(map[string]any)
	if !ok {
		t.Fatalf("providerOptions.anthropic missing or wrong type: %#v", po["anthropic"])
	}
	thinking, ok := anthropic["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("providerOptions.anthropic.thinking missing or wrong type: %#v", anthropic["thinking"])
	}
	if got := thinking["type"]; got != "enabled" {
		t.Fatalf("thinking.type = %v, want enabled", got)
	}
	gotBudget, ok := thinking["budgetTokens"].(float64)
	if !ok {
		t.Fatalf("thinking.budgetTokens missing or wrong type: %#v", thinking["budgetTokens"])
	}
	if int(gotBudget) != wantBudget {
		t.Fatalf("thinking.budgetTokens = %v, want %d", gotBudget, wantBudget)
	}
}

func assertOpenAIReasoning(t *testing.T, req map[string]any, wantEffort string) {
	t.Helper()

	po, ok := req["providerOptions"].(map[string]any)
	if !ok {
		t.Fatalf("providerOptions missing or wrong type: %#v", req["providerOptions"])
	}
	openai, ok := po["openai"].(map[string]any)
	if !ok {
		t.Fatalf("providerOptions.openai missing or wrong type: %#v", po["openai"])
	}
	if got := openai["reasoningEffort"]; got != wantEffort {
		t.Fatalf("openai.reasoningEffort = %v, want %s", got, wantEffort)
	}
	if got := openai["reasoningSummary"]; got != "detailed" {
		t.Fatalf("openai.reasoningSummary = %v, want detailed", got)
	}
}

func assertMissingField(t *testing.T, req map[string]any, key string) {
	t.Helper()

	if _, ok := req[key]; ok {
		t.Fatalf("%s should be absent: %#v", key, req[key])
	}
}
