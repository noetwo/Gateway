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

// processAnthropicSSE 解析 Anthropic 流，注入 input_tokens、累计 output_tokens 估算、
// 每隔若干 chunk 插入合成 message_delta（防客户端断连后下游拿不到 usage）
func processAnthropicSSE(w http.ResponseWriter, src io.Reader, ctx context.Context, inputEst int, usageStats *TokenUsage) error {
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
	if usageStats == nil {
		usageStats = &TokenUsage{}
	}
	defer func() { usageStats.finish(inputEst, outputAccum) }()

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
		usageStats.noteEstimated(inputEst, outputAccum)
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
				if it := getFloat(usage, "input_tokens"); it > 0 {
					usageStats.noteActual(int(it), 0, 0)
				} else if inputEst > 0 {
					usage["input_tokens"] = inputEst
					usageStats.noteEstimated(inputEst, 0)
				}
				if ot := getFloat(usage, "output_tokens"); ot > 0 {
					usageStats.noteActual(0, int(ot), 0)
				} else if _, ok := usage["output_tokens"]; !ok {
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
					usageStats.noteActual(int(it), 0, 0)
				}
				if ot := getFloat(usage, "output_tokens"); ot > 0 {
					outputAccum = int(ot)
					sawRealUsage = true
					usageStats.noteActual(0, int(ot), 0)
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
func processOpenAISSE(w http.ResponseWriter, src io.Reader, ctx context.Context, inputEst int, usageStats *TokenUsage) error {
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
	if usageStats == nil {
		usageStats = &TokenUsage{}
	}
	defer func() { usageStats.finish(inputEst, outputAccum) }()

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
		usageStats.noteEstimated(inputEst, outputAccum)
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
			pt := intField(usage, "prompt_tokens")
			ct := intField(usage, "completion_tokens")
			tt := intField(usage, "total_tokens")
			if pt > 0 || ct > 0 || tt > 0 {
				sawRealUsage = true
				if ct > 0 {
					outputAccum = ct
				}
				usageStats.noteActual(pt, ct, tt)
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
