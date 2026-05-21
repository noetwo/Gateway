package app

import (
	"bytes"
	"encoding/json"
	"net/http"
)

func (u *TokenUsage) noteEstimated(input, output int) {
	if input > 0 && u.InputTokens == 0 {
		u.InputTokens = input
	}
	if output > 0 && u.OutputTokens == 0 {
		u.OutputTokens = output
	}
	if u.InputTokens > 0 || u.OutputTokens > 0 {
		if u.TotalTokens == 0 {
			u.TotalTokens = u.InputTokens + u.OutputTokens
		}
		if u.Source == "" {
			u.Source = "estimated"
		} else if u.Source == "actual" {
			u.Source = "mixed"
		}
	}
}

func (u *TokenUsage) noteActual(input, output, total int) {
	if input > 0 {
		u.InputTokens = input
	}
	if output > 0 {
		u.OutputTokens = output
	}
	if total > 0 {
		u.TotalTokens = total
	} else if u.InputTokens > 0 || u.OutputTokens > 0 {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
	if u.InputTokens > 0 || u.OutputTokens > 0 || u.TotalTokens > 0 {
		if u.Source == "" || u.Source == "actual" {
			u.Source = "actual"
		} else {
			u.Source = "mixed"
		}
	}
}

func (u *TokenUsage) finish(inputEst, outputEst int) {
	if u.InputTokens == 0 && inputEst > 0 {
		u.noteEstimated(inputEst, 0)
	}
	if u.OutputTokens == 0 && outputEst > 0 {
		u.noteEstimated(0, outputEst)
	}
	if u.TotalTokens == 0 && (u.InputTokens > 0 || u.OutputTokens > 0) {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
}

func estimatedUsage(input, output int) TokenUsage {
	var usage TokenUsage
	usage.noteEstimated(input, output)
	return usage
}

func intField(m map[string]any, keys ...string) int {
	for _, key := range keys {
		if v := int(getFloat(m, key)); v > 0 {
			return v
		}
	}
	return 0
}

func extractUsageFromResponse(body []byte, inputEst int) TokenUsage {
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return estimatedUsage(inputEst, 0)
	}
	var usage TokenUsage
	if raw, ok := obj["usage"].(map[string]any); ok && raw != nil {
		usage.noteActual(
			intField(raw, "prompt_tokens", "input_tokens"),
			intField(raw, "completion_tokens", "output_tokens"),
			intField(raw, "total_tokens"),
		)
	}
	usage.finish(inputEst, estimateOutputTokensFromResponse(obj))
	return usage
}

func estimateOutputTokensFromResponse(obj map[string]any) int {
	total := 0
	if c, ok := obj["content"]; ok {
		total += estimateContentTokens(c)
	}
	if s, ok := obj["output_text"].(string); ok {
		total += estimateTokens(s)
	}
	if choices, ok := obj["choices"].([]any); ok {
		for _, choice := range choices {
			cm, ok := choice.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := cm["text"].(string); ok {
				total += estimateTokens(text)
			}
			for _, field := range []string{"message", "delta"} {
				msg, ok := cm[field].(map[string]any)
				if !ok {
					continue
				}
				if content, ok := msg["content"]; ok {
					total += estimateContentTokens(content)
				}
				if text, ok := msg["reasoning"].(string); ok {
					total += estimateTokens(text)
				}
				if text, ok := msg["reasoning_content"].(string); ok {
					total += estimateTokens(text)
				}
			}
		}
	}
	if output, ok := obj["output"].([]any); ok {
		for _, item := range output {
			im, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if content, ok := im["content"]; ok {
				total += estimateContentTokens(content)
			}
			if text, ok := im["text"].(string); ok {
				total += estimateTokens(text)
			}
		}
	}
	return total
}

type captureResponseWriter struct {
	real http.ResponseWriter
	max  int
	buf  []byte
}

func newCaptureResponseWriter(real http.ResponseWriter, max int) *captureResponseWriter {
	return &captureResponseWriter{real: real, max: max}
}

func (w *captureResponseWriter) Header() http.Header  { return w.real.Header() }
func (w *captureResponseWriter) WriteHeader(code int) { w.real.WriteHeader(code) }
func (w *captureResponseWriter) Write(p []byte) (int, error) {
	if len(w.buf) < w.max {
		remaining := w.max - len(w.buf)
		if len(p) < remaining {
			remaining = len(p)
		}
		w.buf = append(w.buf, p[:remaining]...)
	}
	return w.real.Write(p)
}
func (w *captureResponseWriter) Flush() {
	if f, ok := w.real.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *captureResponseWriter) Bytes() []byte {
	return append([]byte(nil), w.buf...)
}
