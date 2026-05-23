package app

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProcessResponsesSSECanonicalizesReasoningEventFraming(t *testing.T) {
	src := strings.NewReader(strings.Join([]string{
		`data: {"type":"response.reasoning_text.delta","delta":"private chain of thought","item_id":"rs_1"}`,
		``,
		`event: response.reasoning_summary_text.delta`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"Checked the constraints.","item_id":"rs_1"}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"visible answer","item_id":"msg_1"}`,
		``,
	}, "\n"))
	rec := httptest.NewRecorder()
	var usage TokenUsage

	if err := processResponsesSSE(rec, src, context.Background(), 7, &usage); err != nil {
		t.Fatalf("processResponsesSSE failed: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: response.reasoning_text.delta") {
		t.Fatalf("reasoning event was not framed with its official event name: %s", body)
	}
	if !strings.Contains(body, "private chain of thought") {
		t.Fatalf("reasoning text event was removed: %s", body)
	}
	if !strings.Contains(body, "response.reasoning_summary_text.delta") {
		t.Fatalf("reasoning summary event was removed: %s", body)
	}
	if !strings.Contains(body, "visible answer") {
		t.Fatalf("output text event missing: %s", body)
	}
	if usage.InputTokens != 7 {
		t.Fatalf("InputTokens = %d, want 7", usage.InputTokens)
	}
	if usage.OutputTokens == 0 {
		t.Fatalf("OutputTokens was not estimated: %#v", usage)
	}
}

func TestProcessResponsesSSEPreservesCompletedReasoningItems(t *testing.T) {
	src := strings.NewReader(strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14},"output":[{"id":"rs_1","type":"reasoning","content":[{"type":"reasoning_text","text":"hidden reasoning"}],"summary":[{"type":"summary_text","text":"safe summary"}]},{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"visible answer"}]}]}}`,
		``,
	}, "\n"))
	rec := httptest.NewRecorder()
	var usage TokenUsage

	if err := processResponsesSSE(rec, src, context.Background(), 0, &usage); err != nil {
		t.Fatalf("processResponsesSSE failed: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: response.completed") {
		t.Fatalf("completed event was not framed with its official event name: %s", body)
	}
	if !strings.Contains(body, "hidden reasoning") || !strings.Contains(body, "reasoning_text") {
		t.Fatalf("completed response reasoning item was removed: %s", body)
	}
	if !strings.Contains(body, "safe summary") {
		t.Fatalf("reasoning summary was removed: %s", body)
	}
	if !strings.Contains(body, "visible answer") {
		t.Fatalf("message output was removed: %s", body)
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 4 || usage.TotalTokens != 14 || usage.Source != "actual" {
		t.Fatalf("usage = %#v, want actual 10/4/14", usage)
	}
}

func TestProcessResponsesSSERestoresCompletedOutputFromDoneItems(t *testing.T) {
	src := strings.NewReader(strings.Join([]string{
		`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"msg_1","type":"message","status":"completed","content":[{"type":"output_text","text":"visible"}],"role":"assistant"}}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"checked"}]}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`,
		``,
	}, "\n"))
	rec := httptest.NewRecorder()

	if err := processResponsesSSE(rec, src, context.Background(), 0, nil); err != nil {
		t.Fatalf("processResponsesSSE failed: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"output":[{"id":"rs_1"`) {
		t.Fatalf("completed output was not restored in output_index order: %s", body)
	}
	if !strings.Contains(body, `"id":"msg_1"`) || !strings.Contains(body, `"text":"visible"`) {
		t.Fatalf("message output item missing from completed response: %s", body)
	}
}
