package app

import (
	"encoding/json"
	"errors"
	"io"
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

func ensureDebugDir() error {
	return os.MkdirAll(fixedDebugDir(), 0o755)
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

func debugFilePath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", errors.New("invalid debug file name")
	}
	root, err := filepath.Abs(fixedDebugDir())
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

func listDebugFiles(q string) ([]debugFileView, error) {
	if err := ensureDebugDir(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(fixedDebugDir())
	if err != nil {
		return nil, err
	}
	files := make([]debugFileView, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		path, err := debugFilePath(name)
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

func handleDebugFiles() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch r.Method {
		case http.MethodGet:
			files, err := listDebugFiles(r.URL.Query().Get("q"))
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"dir":   fixedDebugDir(),
				"files": files,
				"count": len(files),
			})
		case http.MethodDelete:
			files, err := listDebugFiles("")
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			deleted := 0
			for _, file := range files {
				path, err := debugFilePath(file.Name)
				if err != nil {
					continue
				}
				if err := os.Remove(path); err == nil {
					deleted++
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}

func handleDebugSettings(rtCfg *RuntimeConfig) http.HandlerFunc {
	type debugSettingsReq struct {
		Enabled bool `json:"enabled"`
	}
	view := func(cfg Config) map[string]any {
		return map[string]any{
			"enabled": cfg.DebugEnabled,
			"dir":     cfg.DebugDumpDir,
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, view(rtCfg.Get()))
		case http.MethodPost:
			var req debugSettingsReq
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
				return
			}
			cfg := rtCfg.Get()
			cfg.DebugEnabled = req.Enabled
			cfg.DebugDumpDir = fixedDebugDir()
			saved, err := rtCfg.Update(cfg)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, view(saved))
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}

func handleDebugFile() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		path, err := debugFilePath(name)
		if err != nil {
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
				writeJSON(w, status, map[string]string{"error": err.Error()})
				return
			}
			if r.URL.Query().Get("download") == "1" {
				filename := strings.ReplaceAll(name, `"`, "_")
				w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
				http.ServeFile(w, r, path)
				return
			}
			f, err := os.Open(path)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			defer f.Close()
			b, _ := io.ReadAll(io.LimitReader(f, maxDebugViewBytes+1))
			truncated := len(b) > maxDebugViewBytes
			if truncated {
				b = b[:maxDebugViewBytes]
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"name":       name,
				"size":       info.Size(),
				"modified":   info.ModTime().UTC(),
				"kind":       debugKind(name),
				"request_id": debugRequestID(name),
				"content":    string(b),
				"truncated":  truncated,
			})
		case http.MethodDelete:
			if err := os.Remove(path); err != nil {
				status := http.StatusInternalServerError
				if errors.Is(err, os.ErrNotExist) {
					status = http.StatusNotFound
				}
				writeJSON(w, status, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}
