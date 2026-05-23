package app

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxDebugViewBytes = 2 << 20

type debugFileView struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	Modified  time.Time `json:"modified"`
	Kind      string    `json:"kind"`
	RequestID string    `json:"request_id"`
}

type debugFileContent struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	Modified  time.Time `json:"modified"`
	Kind      string    `json:"kind"`
	RequestID string    `json:"request_id"`
	Content   string    `json:"content"`
	Truncated bool      `json:"truncated"`
}

func ensureDebugDir() error {
	return ensureDebugDirFor(fixedDebugDir())
}

func ensureDebugDirFor(dir string) error {
	return os.MkdirAll(normalizeDebugDumpDir(dir), 0o755)
}

func checkDebugDirWritable(dir string) error {
	dir = normalizeDebugDumpDir(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".debug-write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	closeErr := f.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

func fixedDebugDir() string {
	base, err := os.Getwd()
	if err != nil || strings.TrimSpace(base) == "" {
		if exe, exeErr := os.Executable(); exeErr == nil {
			base = filepath.Dir(exe)
		} else {
			base = "."
		}
	}
	dir, err := filepath.Abs(filepath.Join(base, defaultDebugDirName))
	if err != nil {
		return filepath.Join(base, defaultDebugDirName)
	}
	return dir
}

func normalizeDebugDumpDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fixedDebugDir()
	}
	if !filepath.IsAbs(dir) {
		if abs, err := filepath.Abs(dir); err == nil {
			return abs
		}
	}
	if clean, err := filepath.Abs(dir); err == nil {
		return clean
	}
	return filepath.Clean(dir)
}

func debugFilePath(dir, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", errors.New("invalid debug file name")
	}
	root, err := filepath.Abs(normalizeDebugDumpDir(dir))
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(root, name))
	if err != nil {
		return "", err
	}
	if filepath.Dir(target) != root {
		return "", errors.New("invalid debug file name")
	}
	return target, nil
}

func debugKind(name string) string {
	switch {
	case strings.HasSuffix(name, "_request_meta.json"):
		return "request-meta"
	case strings.HasSuffix(name, "_request_body.json"):
		return "request-body"
	case strings.HasSuffix(name, "_upstream.txt"):
		return "upstream"
	case strings.HasSuffix(name, "_downstream.txt"):
		return "downstream"
	case strings.HasSuffix(name, "_meta.json"):
		return "meta"
	default:
		return "file"
	}
}

func debugRequestID(name string) string {
	for _, suffix := range []string{
		"_request_meta.json",
		"_request_body.json",
		"_upstream.txt",
		"_downstream.txt",
		"_meta.json",
	} {
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix)
		}
	}
	return strings.TrimSuffix(name, filepath.Ext(name))
}

func normalizeDebugRequestID(requestID string) (string, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || strings.ContainsAny(requestID, `/\`) {
		return "", errors.New("invalid debug request id")
	}
	for _, suffix := range []string{
		"_request_meta.json",
		"_request_body.json",
		"_upstream.txt",
		"_downstream.txt",
		"_meta.json",
	} {
		if strings.HasSuffix(requestID, suffix) {
			requestID = strings.TrimSuffix(requestID, suffix)
			break
		}
	}
	return requestID, nil
}

func debugFileMatches(path, name, q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return true
	}
	if strings.Contains(strings.ToLower(name), q) {
		return true
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, maxDebugViewBytes))
	return strings.Contains(strings.ToLower(string(b)), q)
}

func listDebugFiles(dir, q string) ([]debugFileView, error) {
	dir = normalizeDebugDumpDir(dir)
	if err := ensureDebugDirFor(dir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]debugFileView, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		path, err := debugFilePath(dir, name)
		if err != nil {
			continue
		}
		if !debugFileMatches(path, name, q) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, debugFileView{
			Name:      name,
			Size:      info.Size(),
			Modified:  info.ModTime().UTC(),
			Kind:      debugKind(name),
			RequestID: debugRequestID(name),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Modified.Equal(files[j].Modified) {
			return files[i].Name > files[j].Name
		}
		return files[i].Modified.After(files[j].Modified)
	})
	return files, nil
}

func readDebugFileContent(path string) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, maxDebugViewBytes+1))
	truncated := len(b) > maxDebugViewBytes
	if truncated {
		b = b[:maxDebugViewBytes]
	}
	return string(b), truncated, nil
}

func listDebugFilesByRequestID(dir, requestID string, includeContent bool) ([]debugFileContent, error) {
	normalizedID, err := normalizeDebugRequestID(requestID)
	if err != nil {
		return nil, err
	}
	files, err := listDebugFiles(dir, "")
	if err != nil {
		return nil, err
	}
	selected := make([]debugFileView, 0, len(files))
	fallback := make([]debugFileView, 0, len(files))
	for _, file := range files {
		switch {
		case file.RequestID == normalizedID:
			selected = append(selected, file)
		case strings.HasPrefix(file.RequestID, normalizedID) || strings.HasPrefix(normalizedID, file.RequestID):
			fallback = append(fallback, file)
		}
	}
	if len(selected) == 0 {
		selected = fallback
	}
	out := make([]debugFileContent, 0, len(files))
	for _, file := range selected {
		item := debugFileContent{
			Name:      file.Name,
			Size:      file.Size,
			Modified:  file.Modified,
			Kind:      file.Kind,
			RequestID: file.RequestID,
		}
		if includeContent {
			path, err := debugFilePath(dir, file.Name)
			if err != nil {
				continue
			}
			content, truncated, err := readDebugFileContent(path)
			if err != nil {
				continue
			}
			item.Content = content
			item.Truncated = truncated
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return debugKindOrder(out[i].Kind) < debugKindOrder(out[j].Kind)
	})
	return out, nil
}

func debugKindOrder(kind string) int {
	switch kind {
	case "request-meta":
		return 1
	case "request-body":
		return 2
	case "upstream":
		return 3
	case "downstream":
		return 4
	case "meta":
		return 5
	default:
		return 99
	}
}

func handleDebugFiles(rtCfg *RuntimeConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		dir := rtCfg.Get().DebugDumpDir
		switch r.Method {
		case http.MethodGet:
			files, err := listDebugFiles(dir, r.URL.Query().Get("q"))
			if err != nil {
				log.Printf("[debug] list failed dir=%s q=%q err=%v", normalizeDebugDumpDir(dir), r.URL.Query().Get("q"), err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			log.Printf("[debug] list dir=%s q=%q count=%d", normalizeDebugDumpDir(dir), r.URL.Query().Get("q"), len(files))
			writeJSON(w, http.StatusOK, map[string]any{
				"dir":   normalizeDebugDumpDir(dir),
				"files": files,
				"count": len(files),
			})
		case http.MethodDelete:
			files, err := listDebugFiles(dir, "")
			if err != nil {
				log.Printf("[debug] clear failed dir=%s err=%v", normalizeDebugDumpDir(dir), err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			deleted := 0
			for _, file := range files {
				path, err := debugFilePath(dir, file.Name)
				if err != nil {
					continue
				}
				if err := os.Remove(path); err == nil {
					deleted++
				}
			}
			log.Printf("[debug] clear dir=%s deleted=%d", normalizeDebugDumpDir(dir), deleted)
			writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}

func handleDebugRequest(rtCfg *RuntimeConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		dir := rtCfg.Get().DebugDumpDir
		requestID := strings.TrimSpace(r.URL.Query().Get("id"))
		files, err := listDebugFilesByRequestID(dir, requestID, true)
		if err != nil {
			log.Printf("[debug] request failed dir=%s id=%q err=%v", normalizeDebugDumpDir(dir), requestID, err)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if len(files) == 0 {
			log.Printf("[debug] request not found dir=%s id=%q", normalizeDebugDumpDir(dir), requestID)
			writeJSON(w, http.StatusNotFound, map[string]any{
				"error":      "debug request not found",
				"dir":        normalizeDebugDumpDir(dir),
				"request_id": requestID,
			})
			return
		}
		log.Printf("[debug] request dir=%s id=%q count=%d", normalizeDebugDumpDir(dir), requestID, len(files))
		writeJSON(w, http.StatusOK, map[string]any{
			"dir":        normalizeDebugDumpDir(dir),
			"request_id": requestID,
			"files":      files,
			"count":      len(files),
		})
	}
}

func handleDebugSettings(rtCfg *RuntimeConfig) http.HandlerFunc {
	type debugSettingsReq struct {
		Enabled bool   `json:"enabled"`
		Dir     string `json:"dir"`
	}
	view := func(cfg Config) map[string]any {
		dir := normalizeDebugDumpDir(cfg.DebugDumpDir)
		writable := false
		var writableErr string
		if cfg.DebugEnabled {
			if err := checkDebugDirWritable(dir); err != nil {
				writableErr = err.Error()
			} else {
				writable = true
			}
		}
		return map[string]any{
			"enabled":        cfg.DebugEnabled,
			"dir":            dir,
			"writable":       writable,
			"writable_error": writableErr,
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch r.Method {
		case http.MethodGet:
			cfg := rtCfg.Get()
			resp := view(cfg)
			log.Printf("[debug] settings get enabled=%v dir=%s writable=%v err=%q", cfg.DebugEnabled, resp["dir"], resp["writable"], resp["writable_error"])
			writeJSON(w, http.StatusOK, resp)
		case http.MethodPost:
			var req debugSettingsReq
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
				log.Printf("[debug] settings invalid json err=%v", err)
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
				return
			}
			cfg := rtCfg.Get()
			cfg.DebugEnabled = req.Enabled
			if strings.TrimSpace(req.Dir) != "" {
				cfg.DebugDumpDir = normalizeDebugDumpDir(req.Dir)
			}
			if cfg.DebugEnabled {
				if err := checkDebugDirWritable(cfg.DebugDumpDir); err != nil {
					log.Printf("[debug] settings rejected enabled=%v dir=%s err=%v", cfg.DebugEnabled, normalizeDebugDumpDir(cfg.DebugDumpDir), err)
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "debug dir is not writable: " + err.Error()})
					return
				}
			}
			saved, err := rtCfg.Update(cfg)
			if err != nil {
				log.Printf("[debug] settings save failed enabled=%v dir=%s err=%v", cfg.DebugEnabled, normalizeDebugDumpDir(cfg.DebugDumpDir), err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			resp := view(saved)
			log.Printf("[debug] settings saved enabled=%v dir=%s writable=%v err=%q", saved.DebugEnabled, resp["dir"], resp["writable"], resp["writable_error"])
			writeJSON(w, http.StatusOK, resp)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}

func handleDebugFile(rtCfg *RuntimeConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		dir := rtCfg.Get().DebugDumpDir
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		path, err := debugFilePath(dir, name)
		if err != nil {
			log.Printf("[debug] file invalid dir=%s name=%q err=%v", normalizeDebugDumpDir(dir), name, err)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		switch r.Method {
		case http.MethodGet:
			info, err := os.Stat(path)
			if err != nil {
				status := http.StatusInternalServerError
				if errors.Is(err, os.ErrNotExist) {
					status = http.StatusNotFound
				}
				log.Printf("[debug] file stat failed dir=%s name=%q status=%d err=%v", normalizeDebugDumpDir(dir), name, status, err)
				writeJSON(w, status, map[string]string{"error": err.Error()})
				return
			}
			if r.URL.Query().Get("download") == "1" {
				filename := strings.ReplaceAll(filepath.Base(name), `"`, "_")
				w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("X-Content-Type-Options", "nosniff")
				f, err := os.Open(path)
				if err != nil {
					log.Printf("[debug] file download open failed dir=%s name=%q err=%v", normalizeDebugDumpDir(dir), name, err)
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
					return
				}
				defer f.Close()
				log.Printf("[debug] file download dir=%s name=%q size=%d", normalizeDebugDumpDir(dir), name, info.Size())
				http.ServeContent(w, r, filename, info.ModTime(), f)
				return
			}
			content, truncated, err := readDebugFileContent(path)
			if err != nil {
				log.Printf("[debug] file read failed dir=%s name=%q err=%v", normalizeDebugDumpDir(dir), name, err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			log.Printf("[debug] file view dir=%s name=%q size=%d truncated=%v", normalizeDebugDumpDir(dir), name, info.Size(), truncated)
			writeJSON(w, http.StatusOK, map[string]any{
				"name":       name,
				"size":       info.Size(),
				"modified":   info.ModTime().UTC(),
				"kind":       debugKind(name),
				"request_id": debugRequestID(name),
				"content":    content,
				"truncated":  truncated,
			})
		case http.MethodDelete:
			if err := os.Remove(path); err != nil {
				status := http.StatusInternalServerError
				if errors.Is(err, os.ErrNotExist) {
					status = http.StatusNotFound
				}
				log.Printf("[debug] file delete failed dir=%s name=%q status=%d err=%v", normalizeDebugDumpDir(dir), name, status, err)
				writeJSON(w, status, map[string]string{"error": err.Error()})
				return
			}
			log.Printf("[debug] file deleted dir=%s name=%q", normalizeDebugDumpDir(dir), name)
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}
