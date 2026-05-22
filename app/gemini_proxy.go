package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultGeminiFacadeModel = "gemini-3.1-pro-preview"

type geminiGenerateRequest struct {
	Contents          []geminiContent  `json:"contents"`
	SystemInstruction *geminiContent   `json:"systemInstruction,omitempty"`
	Tools             []map[string]any `json:"tools,omitempty"`
	ToolConfig        map[string]any   `json:"toolConfig,omitempty"`
	GenerationConfig  map[string]any   `json:"generationConfig,omitempty"`
	SafetySettings    []any            `json:"safetySettings,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiInlineData       `json:"inlineData,omitempty"`
	FileData         *geminiFileData         `json:"fileData,omitempty"`
	FunctionCall     map[string]any          `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiInlineData struct {
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
}

type geminiFileData struct {
	MimeType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string         `json:"name,omitempty"`
	Response map[string]any `json:"response,omitempty"`
}

type gatewayLanguageModelResponse struct {
	Content          []map[string]any `json:"content"`
	FinishReason     any              `json:"finishReason"`
	Usage            map[string]any   `json:"usage,omitempty"`
	ProviderMetadata map[string]any   `json:"providerMetadata,omitempty"`
	Warnings         []any            `json:"warnings,omitempty"`
}

type gatewayLanguageModelRequest struct {
	Body          map[string]any
	InputEstimate int
	SearchEnabled bool
}

func handleGeminiProxy(rtCfg *RuntimeConfig, state *AppState, proxyLogs *ProxyLogRing) http.HandlerFunc {
	client := &http.Client{Timeout: 30 * time.Minute}

	return func(w http.ResponseWriter, r *http.Request) {
		cfg := rtCfg.Get()
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		op, modelName, ok := parseGeminiFacadePath(r.URL.Path)
		if !ok {
			writeGeminiError(w, http.StatusNotFound, "NOT_FOUND", "unknown Gemini API path")
			return
		}

		wantGeminiStream := op == "streamGenerateContent"
		switch op {
		case "listModels":
			if r.Method != http.MethodGet {
				writeGeminiError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"models": []any{geminiFacadeModel(defaultGeminiFacadeModel)}})
			return
		case "getModel":
			if r.Method != http.MethodGet {
				writeGeminiError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
				return
			}
			writeJSON(w, http.StatusOK, geminiFacadeModel(nativeGeminiModelName(modelName)))
			return
		case "countTokens":
			if r.Method != http.MethodPost {
				writeGeminiError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
			if err != nil {
				writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body")
				return
			}
			estimate := estimateGeminiInputTokens(body)
			writeJSON(w, http.StatusOK, map[string]any{"totalTokens": estimate})
			return
		case "streamGenerateContent":
		case "generateContent":
		default:
			writeGeminiError(w, http.StatusNotFound, "NOT_FOUND", "unknown Gemini operation")
			return
		}

		if r.Method != http.MethodPost {
			writeGeminiError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}

		reqStart := time.Now()
		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body")
			return
		}

		gatewayModel := canonicalGeminiGatewayModel(modelName)
		converted, err := buildGatewayLanguageModelRequest(body, gatewayModel, cfg.ProviderOrder)
		if err != nil {
			writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		payload, err := json.Marshal(converted.Body)
		if err != nil {
			writeGeminiError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}

		cands := state.nextProxyCandidates(gatewayModel)
		if len(cands) == 0 {
			if modelBlockedForHobby(gatewayModel, state.hobbyBlockedSettings()) {
				writeGeminiError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "no non-hobby active key available for "+gatewayModel)
				return
			}
			writeGeminiError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "no active key available")
			return
		}
		retryCodes, maxAttempts := state.retrySettings()
		if maxAttempts > 0 && len(cands) > maxAttempts {
			cands = cands[:maxAttempts]
		}

		targetURL := gatewayLanguageModelURL(cfg.GatewayBaseURL)
		getKeyName := func(id string) string {
			state.mu.RLock()
			defer state.mu.RUnlock()
			if k, ok := state.Keys[id]; ok {
				return k.Name
			}
			return id
		}
		addProxyLog := func(c proxyCandidate, statusCode int, success bool, retried bool, errMsg string, usage TokenUsage) {
			usage.finish(converted.InputEstimate, 0)
			proxyLogs.Add(ProxyLog{
				Time:         time.Now(),
				Model:        gatewayModel,
				KeyName:      getKeyName(c.ID),
				KeyID:        c.ID,
				StatusCode:   statusCode,
				ElapsedMs:    time.Since(reqStart).Milliseconds(),
				Success:      success,
				Retried:      retried,
				Error:        errMsg,
				Path:         r.URL.Path,
				Method:       r.Method,
				Endpoint:     "/v1beta/models/" + nativeGeminiModelName(gatewayModel) + ":" + op,
				Interface:    "gemini-v1beta",
				Provider:     cfg.ProviderOrder,
				Stream:       wantGeminiStream,
				InputTokens:  usage.InputTokens,
				OutputTokens: usage.OutputTokens,
				TotalTokens:  usage.TotalTokens,
				UsageSource:  usage.Source,
			})
		}

		var lastErr error
		for i, c := range cands {
			outReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(payload))
			if err != nil {
				writeGeminiError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
				return
			}
			outReq.Header.Set("Authorization", "Bearer "+c.APIKey)
			outReq.Header.Set("Content-Type", "application/json")
			if wantGeminiStream {
				outReq.Header.Set("Accept", "text/event-stream")
			} else {
				outReq.Header.Set("Accept", "application/json")
			}
			outReq.Header.Set("Accept-Encoding", "identity")
			outReq.Header.Set("ai-gateway-protocol-version", "0.0.1")
			outReq.Header.Set("ai-language-model-id", gatewayModel)
			outReq.Header.Set("ai-language-model-specification-version", "4")
			if wantGeminiStream {
				outReq.Header.Set("ai-language-model-streaming", "true")
			} else {
				outReq.Header.Set("ai-language-model-streaming", "false")
			}

			resp, err := client.Do(outReq)
			if err != nil {
				if r.Context().Err() != nil {
					addProxyLog(c, 0, false, i > 0, "client_gone: "+err.Error(), estimatedUsage(converted.InputEstimate, 0))
					return
				}
				state.markProxyFailure(c.ID, err.Error(), 0, cfg)
				lastErr = err
				addProxyLog(c, 0, false, i > 0, err.Error(), estimatedUsage(converted.InputEstimate, 0))
				continue
			}

			respIsSSE := strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "event-stream")
			if wantGeminiStream && resp.StatusCode >= 200 && resp.StatusCode < 300 && respIsSSE {
				usage, copyErr := processGatewayGeminiSSE(w, resp.Body, r.Context(), converted.InputEstimate)
				_ = resp.Body.Close()
				if copyErr != nil {
					if r.Context().Err() != nil {
						state.markProxySuccess(c.ID)
						addProxyLog(c, resp.StatusCode, true, i > 0, "client_gone: "+copyErr.Error(), usage)
						return
					}
					state.markProxyFailure(c.ID, copyErr.Error(), 0, cfg)
					addProxyLog(c, resp.StatusCode, false, i > 0, "stream error: "+copyErr.Error(), usage)
					return
				}
				state.markProxySuccess(c.ID)
				addProxyLog(c, resp.StatusCode, true, i > 0, "", usage)
				return
			}

			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
			_ = resp.Body.Close()

			if shouldRetryWithNextKey(resp.StatusCode, retryCodes) {
				errMsg := fmt.Sprintf("upstream status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
				state.markProxyFailure(c.ID, errMsg, resp.StatusCode, cfg)
				lastErr = errors.New(errMsg)
				addProxyLog(c, resp.StatusCode, false, i > 0, truncate(errMsg, 200), estimatedUsage(converted.InputEstimate, 0))
				if i < len(cands)-1 {
					continue
				}
				writeGeminiError(w, http.StatusBadGateway, "UNAVAILABLE", errMsg)
				return
			}

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				errMsg := fmt.Sprintf("upstream status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
				state.markProxyFailure(c.ID, fmt.Sprintf("upstream status=%d", resp.StatusCode), resp.StatusCode, cfg)
				addProxyLog(c, resp.StatusCode, false, i > 0, truncate(errMsg, 200), estimatedUsage(converted.InputEstimate, 0))
				writeGeminiError(w, http.StatusBadGateway, "UNAVAILABLE", errMsg)
				return
			}

			geminiResp, usage, err := gatewayLanguageModelToGemini(respBody, converted.InputEstimate)
			if err != nil {
				state.markProxyFailure(c.ID, err.Error(), 0, cfg)
				addProxyLog(c, resp.StatusCode, false, i > 0, "response transform error: "+err.Error(), estimatedUsage(converted.InputEstimate, 0))
				writeGeminiError(w, http.StatusBadGateway, "INTERNAL", err.Error())
				return
			}

			state.markProxySuccess(c.ID)
			addProxyLog(c, resp.StatusCode, true, i > 0, "", usage)
			if wantGeminiStream {
				_ = writeGeminiSSE(w, geminiResp)
			} else {
				writeJSON(w, http.StatusOK, geminiResp)
			}
			return
		}

		if lastErr == nil {
			lastErr = errors.New("all keys failed")
		}
		writeGeminiError(w, http.StatusBadGateway, "UNAVAILABLE", lastErr.Error())
	}
}

func parseGeminiFacadePath(path string) (op string, model string, ok bool) {
	path = strings.TrimRight(path, "/")
	if path == "/v1beta/models" {
		return "listModels", "", true
	}
	const prefix = "/v1beta/models/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	if rest == "" {
		return "listModels", "", true
	}
	modelPart, opPart, hasOp := strings.Cut(rest, ":")
	if decoded, err := url.PathUnescape(modelPart); err == nil {
		modelPart = decoded
	}
	if modelPart == "" {
		return "", "", false
	}
	if !hasOp {
		return "getModel", modelPart, true
	}
	return opPart, modelPart, true
}

func nativeGeminiModelName(model string) string {
	model = strings.TrimSpace(strings.TrimPrefix(model, "models/"))
	if strings.HasPrefix(model, "google/") {
		return strings.TrimPrefix(model, "google/")
	}
	return model
}

func canonicalGeminiGatewayModel(model string) string {
	raw := strings.TrimSpace(strings.TrimPrefix(model, "models/"))
	if strings.HasPrefix(raw, "google/") {
		return raw
	}
	name := nativeGeminiModelName(model)
	if name == "" {
		name = defaultGeminiFacadeModel
	}
	return "google/" + name
}

func geminiFacadeModel(model string) map[string]any {
	name := nativeGeminiModelName(model)
	if name == "" {
		name = defaultGeminiFacadeModel
	}
	return map[string]any{
		"name":                       "models/" + name,
		"version":                    name,
		"displayName":                name,
		"description":                "Vercel AI Gateway Gemini-compatible facade",
		"inputTokenLimit":            1048576,
		"outputTokenLimit":           65536,
		"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"},
	}
}

func gatewayLanguageModelURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = defaultGatewayBase
	}
	switch {
	case strings.HasSuffix(base, "/v4/ai/language-model"):
		return base
	case strings.HasSuffix(base, "/v4/ai"):
		return base + "/language-model"
	case strings.HasSuffix(base, "/v4"):
		return base + "/ai/language-model"
	case strings.HasSuffix(base, "/v1"):
		return strings.TrimSuffix(base, "/v1") + "/v4/ai/language-model"
	default:
		return base + "/v4/ai/language-model"
	}
}

func buildGatewayLanguageModelRequest(body []byte, model, providerOrder string) (gatewayLanguageModelRequest, error) {
	var req geminiGenerateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return gatewayLanguageModelRequest{}, fmt.Errorf("invalid JSON: %w", err)
	}

	prompt := make([]any, 0, len(req.Contents)+1)
	if req.SystemInstruction != nil {
		if sys := geminiTextFromParts(req.SystemInstruction.Parts); sys != "" {
			prompt = append(prompt, map[string]any{"role": "system", "content": sys})
		}
	}
	for _, content := range req.Contents {
		msg, ok := geminiContentToGatewayMessage(content)
		if ok {
			prompt = append(prompt, msg)
		}
	}
	if len(prompt) == 0 {
		return gatewayLanguageModelRequest{}, errors.New("contents must include at least one text or file part")
	}

	out := map[string]any{"prompt": prompt}
	if cfg := req.GenerationConfig; cfg != nil {
		copyGenerationConfig(out, cfg)
	}

	po := buildGeminiProviderOptions(model, providerOrder, req.GenerationConfig, req.SafetySettings)
	if len(po) > 0 {
		out["providerOptions"] = po
	}

	searchEnabled := geminiToolsWantGoogleSearch(req.Tools)
	if searchEnabled {
		out["tools"] = []any{map[string]any{
			"type": "provider",
			"id":   "google.google_search",
			"name": "google_search",
			"args": map[string]any{},
		}}
		out["toolChoice"] = map[string]any{"type": "auto"}
	}

	return gatewayLanguageModelRequest{
		Body:          out,
		InputEstimate: estimateGatewayPromptTokens(prompt),
		SearchEnabled: searchEnabled,
	}, nil
}

func geminiContentToGatewayMessage(content geminiContent) (map[string]any, bool) {
	role := strings.ToLower(strings.TrimSpace(content.Role))
	switch role {
	case "", "user":
		role = "user"
	case "model", "assistant":
		role = "assistant"
	case "system":
		role = "system"
	case "function", "tool":
		role = "tool"
	default:
		role = "user"
	}

	if role == "system" {
		text := geminiTextFromParts(content.Parts)
		if text == "" {
			return nil, false
		}
		return map[string]any{"role": "system", "content": text}, true
	}

	parts := make([]any, 0, len(content.Parts))
	for _, part := range content.Parts {
		switch {
		case part.Text != "":
			parts = append(parts, map[string]any{"type": "text", "text": part.Text})
		case part.InlineData != nil && part.InlineData.Data != "":
			file := map[string]any{
				"type": "file",
				"data": map[string]any{
					"type": "data",
					"data": part.InlineData.Data,
				},
			}
			if part.InlineData.MimeType != "" {
				file["mediaType"] = part.InlineData.MimeType
			}
			parts = append(parts, file)
		case part.FileData != nil && part.FileData.FileURI != "":
			file := map[string]any{
				"type": "file",
				"data": map[string]any{
					"type": "url",
					"url":  part.FileData.FileURI,
				},
			}
			if part.FileData.MimeType != "" {
				file["mediaType"] = part.FileData.MimeType
			}
			parts = append(parts, file)
		case part.FunctionResponse != nil:
			payload, _ := json.Marshal(part.FunctionResponse.Response)
			parts = append(parts, map[string]any{"type": "text", "text": string(payload)})
		}
	}
	if len(parts) == 0 {
		return nil, false
	}
	return map[string]any{"role": role, "content": parts}, true
}

func geminiTextFromParts(parts []geminiPart) string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func copyGenerationConfig(out map[string]any, cfg map[string]any) {
	copyMapKey(out, cfg, "temperature", "temperature")
	copyMapKey(out, cfg, "topP", "topP")
	copyMapKey(out, cfg, "topK", "topK")
	copyMapKey(out, cfg, "maxOutputTokens", "maxOutputTokens")
	copyMapKey(out, cfg, "stopSequences", "stopSequences")
	copyMapKey(out, cfg, "seed", "seed")
	if mime, ok := cfg["responseMimeType"].(string); ok && strings.EqualFold(mime, "application/json") {
		out["responseFormat"] = map[string]any{"type": "json"}
	}
}

func copyMapKey(dst, src map[string]any, srcKey, dstKey string) {
	if v, ok := src[srcKey]; ok {
		dst[dstKey] = v
	}
}

func buildGeminiProviderOptions(model, providerOrder string, generationConfig map[string]any, safetySettings []any) map[string]any {
	po := map[string]any{}
	if providers := providersForModelNamespace(splitProviderOrder(providerOrder), "google"); len(providers) > 0 {
		order := make([]any, len(providers))
		for i, p := range providers {
			order[i] = p
		}
		po["gateway"] = map[string]any{"order": order}
	}

	google := map[string]any{}
	if len(safetySettings) > 0 {
		google["safetySettings"] = safetySettings
	}
	if generationConfig != nil {
		if tc := thinkingConfigFromGenerationConfig(model, generationConfig); len(tc) > 0 {
			google["thinkingConfig"] = tc
		}
		if modalities, ok := generationConfig["responseModalities"]; ok {
			google["responseModalities"] = modalities
		}
	}
	if len(google) > 0 {
		po["google"] = google
	}
	return po
}

func thinkingConfigFromGenerationConfig(model string, generationConfig map[string]any) map[string]any {
	var raw any
	if v, ok := generationConfig["thinkingConfig"]; ok {
		raw = v
	} else if v, ok := generationConfig["thinking_config"]; ok {
		raw = v
	}

	out := map[string]any{}
	if m, ok := raw.(map[string]any); ok {
		copyThinkingConfigKey(out, m, "thinkingLevel", "thinking_level", "thinkingLevel")
		copyThinkingConfigKey(out, m, "thinkingBudget", "thinking_budget", "thinkingBudget")
		copyThinkingConfigKey(out, m, "includeThoughts", "include_thoughts", "includeThoughts")
		for k, v := range m {
			if !knownThinkingConfigKey(k) {
				out[k] = v
			}
		}
	}
	copyThinkingConfigKey(out, generationConfig, "thinkingLevel", "thinking_level", "thinkingLevel")
	copyThinkingConfigKey(out, generationConfig, "thinkingBudget", "thinking_budget", "thinkingBudget")
	copyThinkingConfigKey(out, generationConfig, "includeThoughts", "include_thoughts", "includeThoughts")

	if strings.Contains(strings.ToLower(model), "gemini-3") {
		if _, hasLevel := out["thinkingLevel"]; hasLevel {
			delete(out, "thinkingBudget")
			delete(out, "thinking_budget")
		}
	}
	return out
}

func knownThinkingConfigKey(key string) bool {
	switch key {
	case "thinkingLevel", "thinking_level", "thinkingBudget", "thinking_budget", "includeThoughts", "include_thoughts":
		return true
	default:
		return false
	}
}

func copyThinkingConfigKey(dst, src map[string]any, camelKey, snakeKey, dstKey string) {
	if v, ok := src[camelKey]; ok {
		dst[dstKey] = v
		return
	}
	if v, ok := src[snakeKey]; ok {
		dst[dstKey] = v
	}
}

func splitProviderOrder(order string) []string {
	providers := []string{}
	for _, p := range strings.Split(order, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			providers = append(providers, p)
		}
	}
	return providers
}

func geminiToolsWantGoogleSearch(tools []map[string]any) bool {
	for _, tool := range tools {
		for key := range tool {
			switch strings.ToLower(key) {
			case "google_search", "googlesearch", "google_search_retrieval", "googlesearchretrieval":
				return true
			}
		}
	}
	return false
}

func gatewayLanguageModelToGemini(body []byte, inputEst int) (map[string]any, TokenUsage, error) {
	var res gatewayLanguageModelResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, estimatedUsage(inputEst, 0), fmt.Errorf("invalid Vercel language-model response: %w", err)
	}

	parts := []any{}
	sources := []map[string]any{}
	for _, item := range res.Content {
		t, _ := item["type"].(string)
		switch t {
		case "text":
			if text, ok := item["text"].(string); ok && text != "" {
				parts = append(parts, map[string]any{"text": text})
			}
		case "reasoning":
			if text, ok := item["text"].(string); ok && text != "" {
				parts = append(parts, map[string]any{"text": text, "thought": true})
			}
		case "source":
			sources = append(sources, item)
		}
	}
	if len(parts) == 0 {
		parts = append(parts, map[string]any{"text": ""})
	}
	usage := gatewayUsageToTokenUsage(res, inputEst)

	candidate := map[string]any{
		"content": map[string]any{
			"role":  "model",
			"parts": parts,
		},
		"finishReason": geminiFinishReason(res.FinishReason),
	}
	if grounding := geminiGroundingMetadata(res.ProviderMetadata, sources); len(grounding) > 0 {
		candidate["groundingMetadata"] = grounding
	}

	usageMetadata := geminiUsageMetadata(res, usage)
	out := map[string]any{
		"candidates": []any{candidate},
	}
	if len(usageMetadata) > 0 {
		out["usageMetadata"] = usageMetadata
	}
	return out, usage, nil
}

func gatewayUsageToTokenUsage(res gatewayLanguageModelResponse, inputEst int) TokenUsage {
	if googleUsage := googleUsageMetadata(res.ProviderMetadata); len(googleUsage) > 0 {
		input := intFromAny(googleUsage["promptTokenCount"])
		textOut := intFromAny(googleUsage["candidatesTokenCount"])
		thoughts := intFromAny(googleUsage["thoughtsTokenCount"])
		total := intFromAny(googleUsage["totalTokenCount"])
		output := textOut + thoughts
		if output == 0 && total > input {
			output = total - input
		}
		return TokenUsage{InputTokens: input, OutputTokens: output, TotalTokens: total, Source: "gateway-google"}
	}

	input := nestedInt(res.Usage, "inputTokens", "total")
	if input == 0 {
		input = inputEst
	}
	output := nestedInt(res.Usage, "outputTokens", "total")
	total := input + output
	return TokenUsage{InputTokens: input, OutputTokens: output, TotalTokens: total, Source: "gateway-v4"}
}

func geminiUsageMetadata(res gatewayLanguageModelResponse, usage TokenUsage) map[string]any {
	if googleUsage := googleUsageMetadata(res.ProviderMetadata); len(googleUsage) > 0 {
		return googleUsage
	}
	out := map[string]any{}
	if usage.InputTokens > 0 {
		out["promptTokenCount"] = usage.InputTokens
	}
	textOut := nestedInt(res.Usage, "outputTokens", "text")
	if textOut > 0 {
		out["candidatesTokenCount"] = textOut
	} else if usage.OutputTokens > 0 {
		out["candidatesTokenCount"] = usage.OutputTokens
	}
	reasoning := nestedInt(res.Usage, "outputTokens", "reasoning")
	if reasoning > 0 {
		out["thoughtsTokenCount"] = reasoning
	}
	if usage.TotalTokens > 0 {
		out["totalTokenCount"] = usage.TotalTokens
	}
	return out
}

func googleUsageMetadata(providerMetadata map[string]any) map[string]any {
	google, _ := providerMetadata["google"].(map[string]any)
	usage, _ := google["usageMetadata"].(map[string]any)
	return usage
}

func geminiGroundingMetadata(providerMetadata map[string]any, sources []map[string]any) map[string]any {
	google, _ := providerMetadata["google"].(map[string]any)
	grounding, _ := google["groundingMetadata"].(map[string]any)
	out := map[string]any{}
	for k, v := range grounding {
		out[k] = v
	}
	if len(sources) > 0 {
		chunks := make([]any, 0, len(sources))
		for _, src := range sources {
			web := map[string]any{}
			if uri, ok := src["url"].(string); ok && uri != "" {
				web["uri"] = uri
			}
			if title, ok := src["title"].(string); ok && title != "" {
				web["title"] = title
			}
			if len(web) > 0 {
				chunks = append(chunks, map[string]any{"web": web})
			}
		}
		if len(chunks) > 0 {
			if _, exists := out["groundingChunks"]; !exists {
				out["groundingChunks"] = chunks
			}
		}
	}
	return out
}

func geminiFinishReason(reason any) string {
	var unified string
	switch v := reason.(type) {
	case string:
		unified = v
	case map[string]any:
		if s, ok := v["unified"].(string); ok {
			unified = s
		} else if s, ok := v["raw"].(string); ok {
			unified = s
		}
	}
	switch strings.ToLower(strings.TrimSpace(unified)) {
	case "", "stop":
		return "STOP"
	case "length", "max_tokens", "max-tokens":
		return "MAX_TOKENS"
	case "content-filter", "content_filter", "safety":
		return "SAFETY"
	case "tool-calls", "tool_calls":
		return "STOP"
	default:
		return strings.ToUpper(strings.ReplaceAll(unified, "-", "_"))
	}
}

func estimateGatewayPromptTokens(prompt []any) int {
	total := 10
	for _, item := range prompt {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		total += 4
		switch c := msg["content"].(type) {
		case string:
			total += estimateTokens(c)
		case []any:
			total += estimateContentTokens(c)
		}
	}
	return total
}

func estimateGeminiInputTokens(body []byte) int {
	converted, err := buildGatewayLanguageModelRequest(body, defaultGeminiFacadeModel, "")
	if err != nil {
		return 0
	}
	return converted.InputEstimate
}

func nestedInt(m map[string]any, outer, inner string) int {
	if m == nil {
		return 0
	}
	om, _ := m[outer].(map[string]any)
	if om == nil {
		return 0
	}
	return intFromAny(om[inner])
}

func intFromAny(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case float32:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	case json.Number:
		i, _ := x.Int64()
		return int(i)
	}
	return 0
}

func writeGeminiError(w http.ResponseWriter, code int, status, message string) {
	writeJSON(w, code, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
			"status":  status,
		},
	})
}
