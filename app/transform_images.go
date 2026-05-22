package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
)

// rewriteImageEditsToGenerations 把 /v1/images/edits 请求转成 /v1/images/generations
func rewriteImageEditsToGenerations(body []byte, r *http.Request) ([]byte, *http.Request) {
	ct := r.Header.Get("Content-Type")

	var prompt, model, size, quality string
	var n int
	var images []string

	if strings.HasPrefix(ct, "multipart/") {
		boundary := ""
		for _, p := range strings.Split(ct, ";") {
			p = strings.TrimSpace(p)
			if strings.HasPrefix(p, "boundary=") {
				boundary = strings.TrimPrefix(p, "boundary=")
				boundary = strings.Trim(boundary, "\"")
			}
		}
		if boundary != "" {
			mr := multipart.NewReader(bytes.NewReader(body), boundary)
			for {
				part, err := mr.NextPart()
				if err != nil {
					break
				}
				name := part.FormName()
				switch name {
				case "prompt":
					val, _ := io.ReadAll(io.LimitReader(part, 1<<20))
					prompt = string(val)
				case "model":
					val, _ := io.ReadAll(io.LimitReader(part, 1<<16))
					model = string(val)
				case "size":
					val, _ := io.ReadAll(io.LimitReader(part, 1<<16))
					size = string(val)
				case "quality":
					val, _ := io.ReadAll(io.LimitReader(part, 1<<16))
					quality = string(val)
				case "n":
					val, _ := io.ReadAll(io.LimitReader(part, 1<<16))
					n, _ = strconv.Atoi(string(val))
				case "image", "image[]":
					imgData, _ := io.ReadAll(io.LimitReader(part, 10<<20))
					if len(imgData) > 0 {
						encoded := base64Encode(imgData)
						images = append(images, encoded)
					}
				}
				part.Close()
			}
		}
	} else {
		var req map[string]any
		if err := json.Unmarshal(body, &req); err == nil {
			if v, ok := req["prompt"].(string); ok {
				prompt = v
			}
			if v, ok := req["model"].(string); ok {
				model = v
			}
			if v, ok := req["size"].(string); ok {
				size = v
			}
			if v, ok := req["quality"].(string); ok {
				quality = v
			}
			if v, ok := req["n"].(float64); ok {
				n = int(v)
			}
			switch img := req["image"].(type) {
			case string:
				images = append(images, img)
			case []any:
				for _, item := range img {
					if s, ok := item.(string); ok {
						images = append(images, s)
					}
				}
			}
		}
	}

	if prompt == "" {
		prompt = "generate an image"
	}
	if model == "" {
		model = "gpt-image-1"
	}
	if n <= 0 {
		n = 1
	}

	genReq := map[string]any{
		"model":  model,
		"prompt": prompt,
		"n":      n,
	}
	if size != "" {
		genReq["size"] = size
	}
	if quality != "" {
		genReq["quality"] = quality
	}
	if len(images) > 0 {
		genReq["image"] = images
	}

	newBody, err := json.Marshal(genReq)
	if err != nil {
		return body, r
	}

	r2 := r.Clone(r.Context())
	r2.URL.Path = "/v1/images/generations"
	r2.Header.Set("Content-Type", "application/json")
	imgInfo := "none"
	if len(images) > 0 {
		imgInfo = fmt.Sprintf("%d images", len(images))
	}
	log.Printf("[image-rewrite] /v1/images/edits → /v1/images/generations model=%s images=%s prompt=%s", model, imgInfo, truncate(prompt, 80))
	return newBody, r2
}

func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func injectImageDefaults(body []byte) []byte {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	changed := false
	if _, ok := req["quality"]; !ok {
		req["quality"] = "high"
		changed = true
	}
	if _, ok := req["size"]; !ok {
		req["size"] = "3840x2160"
		changed = true
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}
