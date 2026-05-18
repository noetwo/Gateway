package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
			if tp, ok := m["type"].(string); ok && (tp == "image" || tp == "image_url" || tp == "input_image") {
				sum += 1500
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

// processAnthropicSSE 解析 Anthropic 流，注入 input_tokens、累计 output_tokens 估算、
// 每隔若干 chunk 插入合成 message_delta（防客户端断连后下游拿不到 usage）
func processAnthropicSSE(w http.ResponseWriter, src io.Reader, ctx context.Context, inputEst int) error {
	flusher, _ := w.(http.Flusher)
	reader := bufio.NewReaderSize(src, 64*1024)

	var (
		curEvent     string
		curData      strings.Builder
		outputAccum  int
		chunkCount   int
		sawRealUsage bool
		gotStart     bool
		stopped      bool
	)

	emit := func(event, data string) error {
		var b strings.Builder
		if event != "" {
			b.WriteString("event: ")
			b.WriteString(event)
			b.WriteByte('\n')
		}
		b.WriteString("data: ")
		b.WriteString(data)
		b.WriteString("\n\n")
		if _, err := w.Write([]byte(b.String())); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	emitFinalSynthetic := func() {
		delta := map[string]any{
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
		}
		usage := map[string]any{
			"input_tokens":  inputEst,
			"output_tokens": outputAccum,
		}
		obj := map[string]any{
			"type":  "message_delta",
			"delta": delta,
			"usage": usage,
		}
		if b, err := json.Marshal(obj); err == nil {
			_ = emit("message_delta", string(b))
		}
		_ = emit("message_stop", `{"type":"message_stop"}`)
	}

	handleEvent := func(event, data string) error {
		var obj map[string]any
		dec := json.NewDecoder(strings.NewReader(data))
		dec.UseNumber()
		if err := dec.Decode(&obj); err != nil {
			return emit(event, data)
		}

		switch event {
		case "message_start":
			gotStart = true
			if msg, ok := obj["message"].(map[string]any); ok {
				usage, _ := msg["usage"].(map[string]any)
				if usage == nil {
					usage = map[string]any{}
				}
				if it := getFloat(usage, "input_tokens"); it == 0 && inputEst > 0 {
					usage["input_tokens"] = inputEst
				}
				if _, ok := usage["output_tokens"]; !ok {
					usage["output_tokens"] = 0
				}
				msg["usage"] = usage
			}

		case "content_block_delta":
			if delta, ok := obj["delta"].(map[string]any); ok {
				if t, ok := delta["text"].(string); ok {
					outputAccum += estimateTokens(t)
				}
				if t, ok := delta["thinking"].(string); ok {
					outputAccum += estimateTokens(t)
				}
			}
			chunkCount++

		case "message_delta":
			if usage, ok := obj["usage"].(map[string]any); ok {
				if it := getFloat(usage, "input_tokens"); it > 0 {
					sawRealUsage = true
				}
				if ot := getFloat(usage, "output_tokens"); ot > 0 {
					outputAccum = int(ot)
					sawRealUsage = true
				}
			}

		case "message_stop":
			stopped = true
		}

		out, err := json.Marshal(obj)
		if err != nil {
			return emit(event, data)
		}
		if err := emit(event, string(out)); err != nil {
			return err
		}

		_ = chunkCount
		return nil
	}

	flushEvent := func() error {
		if curData.Len() == 0 && curEvent == "" {
			return nil
		}
		ev := curEvent
		d := curData.String()
		curEvent = ""
		curData.Reset()
		if d == "" {
			return nil
		}
		return handleEvent(ev, d)
	}

	clientGone := false

	for {
		select {
		case <-ctx.Done():
			clientGone = true
		default:
		}
		if clientGone {
			break
		}

		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\r\n")
			if trimmed == "" {
				if e := flushEvent(); e != nil {
					return e
				}
			} else if strings.HasPrefix(trimmed, "event:") {
				curEvent = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
			} else if strings.HasPrefix(trimmed, "data:") {
				if curData.Len() > 0 {
					curData.WriteByte('\n')
				}
				curData.WriteString(strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
			}
		}
		if err != nil {
			_ = flushEvent()
			if errors.Is(err, io.EOF) {
				if gotStart && !stopped && !sawRealUsage {
					emitFinalSynthetic()
				}
				return nil
			}
			return err
		}
	}

	if gotStart && !stopped {
		emitFinalSynthetic()
	}
	return nil
}

// processOpenAISSE 解析 OpenAI 兼容流，累计 output_tokens 估算，
// 每隔若干 chunk 注入带 usage 的 chunk，断连时补发 final
func processOpenAISSE(w http.ResponseWriter, src io.Reader, ctx context.Context, inputEst int) error {
	flusher, _ := w.(http.Flusher)
	reader := bufio.NewReaderSize(src, 64*1024)

	var (
		outputAccum  int
		chunkCount   int
		sawRealUsage bool
		lastID       string
		lastModel    string
		lastCreated  any
		sawDone      bool
	)

	emitLine := func(s string) error {
		if _, err := w.Write([]byte(s)); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	emitSyntheticUsageChunk := func(includeFinish bool) {
		obj := map[string]any{
			"id":      lastID,
			"object":  "chat.completion.chunk",
			"created": lastCreated,
			"model":   lastModel,
			"choices": []any{},
			"usage": map[string]any{
				"prompt_tokens":     inputEst,
				"completion_tokens": outputAccum,
				"total_tokens":      inputEst + outputAccum,
			},
		}
		if includeFinish {
			obj["choices"] = []any{
				map[string]any{
					"index":         0,
					"delta":         map[string]any{},
					"finish_reason": "stop",
				},
			}
		}
		if b, err := json.Marshal(obj); err == nil {
			_ = emitLine("data: " + string(b) + "\n\n")
		}
	}

	processDataLine := func(payload string) error {
		if payload == "[DONE]" {
			sawDone = true
			return emitLine("data: [DONE]\n\n")
		}
		var obj map[string]any
		dec := json.NewDecoder(strings.NewReader(payload))
		dec.UseNumber()
		if err := dec.Decode(&obj); err != nil {
			return emitLine("data: " + payload + "\n\n")
		}
		if id, ok := obj["id"].(string); ok && id != "" {
			lastID = id
		}
		if model, ok := obj["model"].(string); ok && model != "" {
			lastModel = model
		}
		if c, ok := obj["created"]; ok {
			lastCreated = c
		}
		if choices, ok := obj["choices"].([]any); ok {
			for _, ch := range choices {
				cm, ok := ch.(map[string]any)
				if !ok {
					continue
				}
				if delta, ok := cm["delta"].(map[string]any); ok {
					if c, ok := delta["content"].(string); ok {
						outputAccum += estimateTokens(c)
					}
					if r, ok := delta["reasoning"].(string); ok {
						outputAccum += estimateTokens(r)
					}
					if r, ok := delta["reasoning_content"].(string); ok {
						outputAccum += estimateTokens(r)
					}
				}
			}
		}
		if usage, ok := obj["usage"].(map[string]any); ok && usage != nil {
			if ct := getFloat(usage, "completion_tokens"); ct > 0 {
				sawRealUsage = true
				outputAccum = int(ct)
			}
		}
		chunkCount++

		out, err := json.Marshal(obj)
		if err != nil {
			return emitLine("data: " + payload + "\n\n")
		}
		if err := emitLine("data: " + string(out) + "\n\n"); err != nil {
			return err
		}

		_ = chunkCount
		return nil
	}

	clientGone := false

	for {
		select {
		case <-ctx.Done():
			clientGone = true
		default:
		}
		if clientGone {
			break
		}

		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\r\n")
			if trimmed == "" {
				// blank line, ignore (we emit our own)
			} else if strings.HasPrefix(trimmed, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				if e := processDataLine(payload); e != nil {
					return e
				}
			} else {
				_ = emitLine(line)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if !sawRealUsage && !sawDone {
					emitSyntheticUsageChunk(true)
					_ = emitLine("data: [DONE]\n\n")
				}
				return nil
			}
			return err
		}
	}

	if !sawRealUsage && !sawDone {
		emitSyntheticUsageChunk(true)
		_ = emitLine("data: [DONE]\n\n")
	}
	return nil
}
