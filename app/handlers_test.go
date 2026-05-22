package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleKeyByIDDeleteClearsStickyAndRenumbers(t *testing.T) {
	state := testState(t, map[string]*Key{
		"01": {ID: "01", Name: "1", APIKey: "vck_one"},
		"02": {ID: "02", Name: "2", APIKey: "vck_two"},
	})
	state.StickyMode = true
	state.StickyKeyID = "01"

	req := httptest.NewRequest(http.MethodDelete, "/api/keys/01", nil)
	rec := httptest.NewRecorder()

	handleKeyByID(state).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	state.mu.RLock()
	defer state.mu.RUnlock()
	if state.StickyKeyID != "" {
		t.Fatalf("sticky key id = %q, want empty", state.StickyKeyID)
	}
	if _, ok := state.Keys["01"]; ok {
		t.Fatalf("deleted key still exists")
	}
	if got := state.Keys["02"].Name; got != "1" {
		t.Fatalf("remaining numeric key name = %q, want %q", got, "1")
	}
}
