package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDebugHandlersUseRuntimeConfigDir(t *testing.T) {
	dir := t.TempDir()
	name := "20260522_120000_000000000_meta.json"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := &RuntimeConfig{current: Config{DebugDumpDir: dir}}

	listReq := httptest.NewRequest(http.MethodGet, "/api/debug?q=20260522", nil)
	listRec := httptest.NewRecorder()
	handleDebugFiles(rt)(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listRec.Code, listRec.Body.String())
	}
	if !strings.Contains(listRec.Body.String(), name) {
		t.Fatalf("list response did not include debug file: %s", listRec.Body.String())
	}

	viewReq := httptest.NewRequest(http.MethodGet, "/api/debug/file?name="+name, nil)
	viewRec := httptest.NewRecorder()
	handleDebugFile(rt)(viewRec, viewReq)
	if viewRec.Code != http.StatusOK {
		t.Fatalf("view status = %d body=%s", viewRec.Code, viewRec.Body.String())
	}
	if !strings.Contains(viewRec.Body.String(), `{\"ok\":true}`) {
		t.Fatalf("view response did not include file content: %s", viewRec.Body.String())
	}

	requestReq := httptest.NewRequest(http.MethodGet, "/api/debug/request?id=20260522_120000_000000000", nil)
	requestRec := httptest.NewRecorder()
	handleDebugRequest(rt)(requestRec, requestReq)
	if requestRec.Code != http.StatusOK {
		t.Fatalf("request status = %d body=%s", requestRec.Code, requestRec.Body.String())
	}
	if !strings.Contains(requestRec.Body.String(), name) || !strings.Contains(requestRec.Body.String(), `{\"ok\":true}`) {
		t.Fatalf("request response did not include debug bundle: %s", requestRec.Body.String())
	}

	requestByFileReq := httptest.NewRequest(http.MethodGet, "/api/debug/request?id="+name, nil)
	requestByFileRec := httptest.NewRecorder()
	handleDebugRequest(rt)(requestByFileRec, requestByFileReq)
	if requestByFileRec.Code != http.StatusOK {
		t.Fatalf("request-by-file status = %d body=%s", requestByFileRec.Code, requestByFileRec.Body.String())
	}

	downloadReq := httptest.NewRequest(http.MethodGet, "/api/debug/file?download=1&name="+name, nil)
	downloadRec := httptest.NewRecorder()
	handleDebugFile(rt)(downloadRec, downloadReq)
	if downloadRec.Code != http.StatusOK {
		t.Fatalf("download status = %d body=%s", downloadRec.Code, downloadRec.Body.String())
	}
	if got := downloadRec.Header().Get("Content-Disposition"); !strings.Contains(got, name) {
		t.Fatalf("download content disposition = %q, want filename %q", got, name)
	}
	if !strings.Contains(downloadRec.Body.String(), `{"ok":true}`) {
		t.Fatalf("download response did not include raw file content: %s", downloadRec.Body.String())
	}
}

func TestNewDumpSessionDoesNotExposeIDWhenDirCannotBeCreated(t *testing.T) {
	dir := t.TempDir()
	notDir := filepath.Join(dir, "debug")
	if err := os.WriteFile(notDir, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))

	dump := newDumpSession(notDir, req, []byte("{}"), 1)
	if dump.enabled {
		t.Fatal("dump session should be disabled when dump dir cannot be created")
	}
	if dump.id != "" {
		t.Fatalf("disabled dump session exposed id %q", dump.id)
	}
}
