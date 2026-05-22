package app

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func processGatewayGeminiSSE(w http.ResponseWriter, src io.Reader, ctx context.Context, inputEst int) (TokenUsage, error) {
	usage := estimatedUsage(inputEst, 0)
	writer := &geminiSSEWriter{w: w}
	reader := bufio.NewReader(src)
	dataLines := make([]string, 0, 1)
	sources := make([]map[string]any, 0)

	processEvent := func(data string) error {
		data = strings.TrimSpace(data)
		if data == "" {
			return nil
		}
		if data == "[DONE]" || data == "DONE" {
			return io.EOF
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return err
		}
		eventType, _ := event["type"].(string)
		switch eventType {
		case "text-delta":
			delta, _ := event["delta"].(string)
			if delta == "" {
				return nil
			}
			return writer.Write(map[string]any{
				"candidates": []any{map[string]any{
					"content": map[string]any{
						"role":  "model",
						"parts": []any{map[string]any{"text": delta}},
					},
				}},
			})
		case "reasoning-delta":
			delta, _ := event["delta"].(string)
			if delta == "" {
				return nil
			}
			return writer.Write(map[string]any{
				"candidates": []any{map[string]any{
					"content": map[string]any{
						"role":  "model",
						"parts": []any{map[string]any{"text": delta, "thought": true}},
					},
				}},
			})
		case "source":
			sources = append(sources, event)
		case "finish":
			res := gatewayLanguageModelResponse{
				FinishReason:     event["finishReason"],
				Usage:            mapFromAny(event["usage"]),
				ProviderMetadata: mapFromAny(event["providerMetadata"]),
			}
			usage = gatewayUsageToTokenUsage(res, inputEst)
			final := map[string]any{
				"candidates": []any{map[string]any{
					"finishReason": geminiFinishReason(res.FinishReason),
				}},
			}
			if grounding := geminiGroundingMetadata(res.ProviderMetadata, sources); len(grounding) > 0 {
				cands := final["candidates"].([]any)
				cand := cands[0].(map[string]any)
				cand["groundingMetadata"] = grounding
			}
			if usageMeta := geminiUsageMetadata(res, usage); len(usageMeta) > 0 {
				final["usageMetadata"] = usageMeta
			}
			return writer.Write(final)
		default:
			return nil
		}
		return nil
	}

	for {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return usage, ctx.Err()
			default:
			}
		}
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if len(dataLines) > 0 {
					if pErr := processEvent(strings.Join(dataLines, "\n")); pErr != nil {
						if pErr == io.EOF {
							return usage, nil
						}
						return usage, pErr
					}
					dataLines = dataLines[:0]
				}
			case strings.HasPrefix(line, "data:"):
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			case strings.TrimSpace(line) == "DONE" || strings.TrimSpace(line) == "[DONE]":
				return usage, nil
			}
		}
		if err != nil {
			if err == io.EOF {
				if len(dataLines) > 0 {
					if pErr := processEvent(strings.Join(dataLines, "\n")); pErr != nil && pErr != io.EOF {
						return usage, pErr
					}
				}
				return usage, nil
			}
			return usage, err
		}
	}
}

type geminiSSEWriter struct {
	w       http.ResponseWriter
	started bool
}

func (s *geminiSSEWriter) Write(resp map[string]any) error {
	if !s.started {
		writeGeminiSSEHeaders(s.w)
		s.w.WriteHeader(http.StatusOK)
		s.started = true
	}
	event, err := geminiSSEEvent(resp)
	if err != nil {
		return err
	}
	if _, err := s.w.Write(event); err != nil {
		return err
	}
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

func geminiSSEEvent(resp map[string]any) ([]byte, error) {
	payload, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(payload)+8)
	out = append(out, "data: "...)
	out = append(out, payload...)
	out = append(out, "\n\n"...)
	return out, nil
}

func writeGeminiSSE(w http.ResponseWriter, resp map[string]any) error {
	writeGeminiSSEHeaders(w)
	w.WriteHeader(http.StatusOK)

	event, err := geminiSSEEvent(resp)
	if err != nil {
		return err
	}
	if _, err := w.Write(event); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

func writeGeminiSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}

func mapFromAny(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
