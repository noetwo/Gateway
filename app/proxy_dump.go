package app

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type dumpSession struct {
	dir       string
	id        string
	startTime time.Time
	reqPath   string
	upstream  *os.File
	downReal  http.ResponseWriter
	downFile  *os.File
	upBytes   int64
	downBytes int64
	inputEst  int
	enabled   bool
	finalized bool
}

func newDumpSession(dir string, r *http.Request, body []byte, inputEst int) *dumpSession {
	if strings.TrimSpace(dir) == "" {
		return &dumpSession{enabled: false}
	}
	dir = normalizeDebugDumpDir(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("dump: mkdir %s failed: %v", dir, err)
		return &dumpSession{enabled: false}
	}
	now := time.Now().UTC()
	id := now.Format("20060102_150405.000000000")
	id = strings.ReplaceAll(id, ".", "_")
	d := &dumpSession{
		dir:       dir,
		id:        id,
		startTime: now,
		reqPath:   r.URL.Path,
		inputEst:  inputEst,
	}
	wroteFile := false
	reqMeta := map[string]any{
		"path":         r.URL.Path,
		"method":       r.Method,
		"query":        r.URL.RawQuery,
		"input_est":    inputEst,
		"received_at":  now.Format(time.RFC3339Nano),
		"content_type": r.Header.Get("Content-Type"),
	}
	if mb, err := json.MarshalIndent(reqMeta, "", "  "); err == nil {
		if err := os.WriteFile(filepath.Join(dir, id+"_request_meta.json"), mb, 0o644); err == nil {
			wroteFile = true
		} else {
			log.Printf("dump: write request meta failed: %v", err)
		}
	}
	if len(body) > 0 {
		if err := os.WriteFile(filepath.Join(dir, id+"_request_body.json"), body, 0o644); err == nil {
			wroteFile = true
		} else {
			log.Printf("dump: write request body failed: %v", err)
		}
	}
	if f, err := os.Create(filepath.Join(dir, id+"_upstream.txt")); err == nil {
		d.upstream = f
		wroteFile = true
	} else {
		log.Printf("dump: create upstream file failed: %v", err)
	}
	if f, err := os.Create(filepath.Join(dir, id+"_downstream.txt")); err == nil {
		d.downFile = f
		wroteFile = true
	} else {
		log.Printf("dump: create downstream file failed: %v", err)
	}
	if !wroteFile {
		return &dumpSession{enabled: false}
	}
	d.enabled = true
	return d
}

type dumpReader struct {
	src  io.Reader
	sink *dumpSession
}

func (r *dumpReader) Read(p []byte) (int, error) {
	n, err := r.src.Read(p)
	if n > 0 && r.sink != nil && r.sink.upstream != nil {
		_, _ = r.sink.upstream.Write(p[:n])
		r.sink.upBytes += int64(n)
	}
	return n, err
}

func (d *dumpSession) wrapUpstream(src io.Reader) io.Reader {
	if !d.enabled {
		return src
	}
	return &dumpReader{src: src, sink: d}
}

type dumpWriter struct {
	real    http.ResponseWriter
	flusher http.Flusher
	sink    *dumpSession
}

func (w *dumpWriter) Header() http.Header { return w.real.Header() }

func (w *dumpWriter) Write(p []byte) (int, error) {
	n, err := w.real.Write(p)
	if n > 0 && w.sink != nil && w.sink.downFile != nil {
		_, _ = w.sink.downFile.Write(p[:n])
		w.sink.downBytes += int64(n)
	}
	return n, err
}

func (w *dumpWriter) WriteHeader(code int) { w.real.WriteHeader(code) }

func (w *dumpWriter) Flush() {
	if w.flusher != nil {
		w.flusher.Flush()
	}
}

func (d *dumpSession) wrapDownstream(w http.ResponseWriter) http.ResponseWriter {
	if !d.enabled {
		return w
	}
	dw := &dumpWriter{real: w, sink: d}
	if f, ok := w.(http.Flusher); ok {
		dw.flusher = f
	}
	d.downReal = w
	return dw
}

func (d *dumpSession) finalize(statusCode int, clientCancelled bool, copyErr error) {
	if !d.enabled || d.finalized {
		return
	}
	d.finalized = true
	if d.upstream != nil {
		_ = d.upstream.Close()
	}
	if d.downFile != nil {
		_ = d.downFile.Close()
	}
	end := time.Now().UTC()
	meta := map[string]any{
		"id":               d.id,
		"path":             d.reqPath,
		"start":            d.startTime.Format(time.RFC3339Nano),
		"end":              end.Format(time.RFC3339Nano),
		"duration_ms":      end.Sub(d.startTime).Milliseconds(),
		"input_est":        d.inputEst,
		"upstream_bytes":   d.upBytes,
		"downstream_bytes": d.downBytes,
		"status_code":      statusCode,
		"client_cancelled": clientCancelled,
	}
	if copyErr != nil {
		meta["copy_error"] = copyErr.Error()
	}
	if mb, err := json.MarshalIndent(meta, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(d.dir, d.id+"_meta.json"), mb, 0o644)
	}
}
