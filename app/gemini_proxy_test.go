package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildGatewayLanguageModelRequestTranslatesGoogleSearch(t *testing.T) {
	body := []byte(`{
		"systemInstruction":{"parts":[{"text":"Be concise."}]},
		"contents":[{"role":"user","parts":[{"text":"Search the web for the latest Go version."}]}],
		"tools":[{"google_search":{}}],
		"generationConfig":{"temperature":0.2,"maxOutputTokens":1024}
	}`)

	req, err := buildGatewayLanguageModelRequest(body, "google/gemini-3.1-pro-preview", "bedrock,google,vertex")
	if err != nil {
		t.Fatalf("buildGatewayLanguageModelRequest failed: %v", err)
	}
	if !req.SearchEnabled {
		t.Fatal("SearchEnabled = false, want true")
	}
	if got := req.Body["temperature"]; got != float64(0.2) {
		t.Fatalf("temperature = %#v, want 0.2", got)
	}
	if got := req.Body["maxOutputTokens"]; got != float64(1024) {
		t.Fatalf("maxOutputTokens = %#v, want 1024", got)
	}

	tools, ok := req.Body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools missing or wrong length: %#v", req.Body["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tool wrong type: %#v", tools[0])
	}
	if got := tool["type"]; got != "provider" {
		t.Fatalf("tool.type = %v, want provider", got)
	}
	if got := tool["id"]; got != "google.google_search" {
		t.Fatalf("tool.id = %v, want google.google_search", got)
	}

	po, ok := req.Body["providerOptions"].(map[string]any)
	if !ok {
		t.Fatalf("providerOptions missing: %#v", req.Body["providerOptions"])
	}
	gw, ok := po["gateway"].(map[string]any)
	if !ok {
		t.Fatalf("providerOptions.gateway missing: %#v", po["gateway"])
	}
	order, ok := gw["order"].([]any)
	if !ok || len(order) != 2 || order[0] != "google" || order[1] != "vertex" {
		t.Fatalf("gateway order = %#v, want google,vertex", gw["order"])
	}
	google, ok := po["google"].(map[string]any)
	if !ok {
		t.Fatalf("providerOptions.google missing: %#v", po["google"])
	}
	thinking, ok := google["thinkingConfig"].(map[string]any)
	if !ok || thinking["thinkingLevel"] != "low" {
		t.Fatalf("thinkingConfig = %#v, want low", google["thinkingConfig"])
	}

	prompt, ok := req.Body["prompt"].([]any)
	if !ok || len(prompt) != 2 {
		t.Fatalf("prompt = %#v, want 2 messages", req.Body["prompt"])
	}
	sys := prompt[0].(map[string]any)
	if sys["role"] != "system" || sys["content"] != "Be concise." {
		t.Fatalf("system prompt = %#v", sys)
	}
}

func TestBuildGatewayLanguageModelRequestTranslatesImageParts(t *testing.T) {
	body := []byte(`{
		"contents":[{"role":"user","parts":[
			{"text":"Describe this image."},
			{"inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo="}},
			{"fileData":{"mimeType":"image/jpeg","fileUri":"https://example.com/cat.jpg"}}
		]}]
	}`)

	req, err := buildGatewayLanguageModelRequest(body, "google/gemini-3.1-pro-preview", "google")
	if err != nil {
		t.Fatalf("buildGatewayLanguageModelRequest failed: %v", err)
	}
	prompt := req.Body["prompt"].([]any)
	msg := prompt[0].(map[string]any)
	content := msg["content"].([]any)
	if len(content) != 3 {
		t.Fatalf("content length = %d, want 3: %#v", len(content), content)
	}

	inlineFile := content[1].(map[string]any)
	if inlineFile["type"] != "file" || inlineFile["mediaType"] != "image/png" {
		t.Fatalf("inline file = %#v", inlineFile)
	}
	inlineData := inlineFile["data"].(map[string]any)
	if inlineData["type"] != "data" || inlineData["data"] != "iVBORw0KGgo=" {
		t.Fatalf("inline data = %#v", inlineData)
	}

	urlFile := content[2].(map[string]any)
	if urlFile["type"] != "file" || urlFile["mediaType"] != "image/jpeg" {
		t.Fatalf("url file = %#v", urlFile)
	}
	urlData := urlFile["data"].(map[string]any)
	if urlData["type"] != "url" || urlData["url"] != "https://example.com/cat.jpg" {
		t.Fatalf("url data = %#v", urlData)
	}
}

func TestGatewayLanguageModelToGeminiMapsTextSourcesAndUsage(t *testing.T) {
	body := []byte(`{
		"content":[
			{"type":"text","text":"Go 1.26.3 is current."},
			{"type":"source","sourceType":"url","url":"https://go.dev/dl/","title":"go.dev"}
		],
		"finishReason":{"unified":"stop","raw":"STOP"},
		"usage":{"inputTokens":{"total":10},"outputTokens":{"total":5,"text":3,"reasoning":2}},
		"providerMetadata":{"google":{"groundingMetadata":{"webSearchQueries":["latest Go version"]},"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3,"thoughtsTokenCount":2,"totalTokenCount":15}}}
	}`)

	resp, usage, err := gatewayLanguageModelToGemini(body, 8)
	if err != nil {
		t.Fatalf("gatewayLanguageModelToGemini failed: %v", err)
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 5 || usage.TotalTokens != 15 {
		t.Fatalf("usage = %#v", usage)
	}
	cands, ok := resp["candidates"].([]any)
	if !ok || len(cands) != 1 {
		t.Fatalf("candidates = %#v", resp["candidates"])
	}
	cand := cands[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Fatalf("finishReason = %v, want STOP", cand["finishReason"])
	}
	content := cand["content"].(map[string]any)
	parts := content["parts"].([]any)
	textPart := parts[0].(map[string]any)
	if textPart["text"] != "Go 1.26.3 is current." {
		t.Fatalf("text = %v", textPart["text"])
	}
	grounding := cand["groundingMetadata"].(map[string]any)
	if _, ok := grounding["groundingChunks"]; !ok {
		t.Fatalf("groundingChunks missing: %#v", grounding)
	}
	usageMeta := resp["usageMetadata"].(map[string]any)
	if usageMeta["totalTokenCount"] != float64(15) {
		t.Fatalf("usageMetadata = %#v", usageMeta)
	}
}

func TestParseGeminiFacadePath(t *testing.T) {
	op, model, ok := parseGeminiFacadePath("/v1beta/models/gemini-3.1-pro-preview:generateContent")
	if !ok || op != "generateContent" || model != "gemini-3.1-pro-preview" {
		t.Fatalf("parse generateContent = (%q, %q, %v)", op, model, ok)
	}
	op, model, ok = parseGeminiFacadePath("/v1beta/models")
	if !ok || op != "listModels" || model != "" {
		t.Fatalf("parse listModels = (%q, %q, %v)", op, model, ok)
	}
	op, model, ok = parseGeminiFacadePath("/v1beta/models/gemini-3.1-pro-preview")
	if !ok || op != "getModel" || model != "gemini-3.1-pro-preview" {
		t.Fatalf("parse getModel = (%q, %q, %v)", op, model, ok)
	}
	op, model, ok = parseGeminiFacadePath("/v1beta/models/gemini-3.1-pro-preview:streamGenerateContent")
	if !ok || op != "streamGenerateContent" || model != "gemini-3.1-pro-preview" {
		t.Fatalf("parse streamGenerateContent = (%q, %q, %v)", op, model, ok)
	}
}

func TestGatewayLanguageModelURL(t *testing.T) {
	got := gatewayLanguageModelURL("https://ai-gateway.vercel.sh/v1")
	want := "https://ai-gateway.vercel.sh/v4/ai/language-model"
	if got != want {
		t.Fatalf("gatewayLanguageModelURL = %q, want %q", got, want)
	}
}

func TestGeminiSSEEventWrapsFullResponse(t *testing.T) {
	event, err := geminiSSEEvent(map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{map[string]any{"text": "hello"}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("geminiSSEEvent failed: %v", err)
	}
	if !strings.HasPrefix(string(event), "data: ") || !strings.HasSuffix(string(event), "\n\n") {
		t.Fatalf("event framing = %q", string(event))
	}
	var payload map[string]any
	if err := json.Unmarshal(event[len("data: "):len(event)-2], &payload); err != nil {
		t.Fatalf("event payload is not JSON: %v", err)
	}
	if _, ok := payload["candidates"]; !ok {
		t.Fatalf("candidates missing: %#v", payload)
	}
}

func TestProcessGatewayGeminiSSEStreamsTextAndFinalMetadata(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"type":"stream-start","warnings":[]}`,
		``,
		`data: {"type":"text-start","id":"0"}`,
		``,
		`data: {"type":"text-delta","id":"0","delta":"hello "}`,
		``,
		`data: {"type":"text-delta","id":"0","delta":"world"}`,
		``,
		`data: {"type":"source","sourceType":"url","url":"https://go.dev/dl/","title":"go.dev"}`,
		``,
		`data: {"type":"finish","finishReason":{"unified":"stop","raw":"STOP"},"usage":{"inputTokens":{"total":7},"outputTokens":{"total":4,"text":2,"reasoning":2}},"providerMetadata":{"google":{"groundingMetadata":{"webSearchQueries":["go"]},"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":2,"thoughtsTokenCount":2,"totalTokenCount":11}}}}`,
		``,
		`DONE`,
	}, "\n")
	rec := httptest.NewRecorder()

	usage, err := processGatewayGeminiSSE(rec, strings.NewReader(upstream), context.Background(), 6)
	if err != nil {
		t.Fatalf("processGatewayGeminiSSE failed: %v", err)
	}
	if usage.InputTokens != 7 || usage.OutputTokens != 4 || usage.TotalTokens != 11 {
		t.Fatalf("usage = %#v", usage)
	}
	if got := rec.Code; got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want event-stream", got)
	}

	events := parseGeminiSSEPayloads(t, rec.Body.String())
	if len(events) != 3 {
		t.Fatalf("event count = %d, want 3: %s", len(events), rec.Body.String())
	}
	firstText := firstCandidateText(t, events[0])
	if firstText != "hello " {
		t.Fatalf("first delta = %q, want hello ", firstText)
	}
	secondText := firstCandidateText(t, events[1])
	if secondText != "world" {
		t.Fatalf("second delta = %q, want world", secondText)
	}
	finalCand := events[2]["candidates"].([]any)[0].(map[string]any)
	if finalCand["finishReason"] != "STOP" {
		t.Fatalf("finishReason = %v, want STOP", finalCand["finishReason"])
	}
	if _, ok := finalCand["groundingMetadata"]; !ok {
		t.Fatalf("groundingMetadata missing: %#v", finalCand)
	}
	if _, ok := events[2]["usageMetadata"]; !ok {
		t.Fatalf("usageMetadata missing: %#v", events[2])
	}
}

func parseGeminiSSEPayloads(t *testing.T, body string) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		if !strings.HasPrefix(block, "data: ") {
			t.Fatalf("invalid SSE block: %q", block)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(block, "data: ")), &payload); err != nil {
			t.Fatalf("invalid payload %q: %v", block, err)
		}
		out = append(out, payload)
	}
	return out
}

func firstCandidateText(t *testing.T, payload map[string]any) string {
	t.Helper()
	candidates := payload["candidates"].([]any)
	candidate := candidates[0].(map[string]any)
	content := candidate["content"].(map[string]any)
	parts := content["parts"].([]any)
	part := parts[0].(map[string]any)
	text, _ := part["text"].(string)
	return text
}
