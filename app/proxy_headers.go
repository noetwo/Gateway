package app

import (
	"errors"
	"io"
	"net/http"
	"strings"
)

func copyProxyHeaders(dst, src http.Header) {
	for k, vals := range src {
		kl := strings.ToLower(k)
		if kl == "authorization" || kl == "x-api-key" || kl == "x-goog-api-key" || kl == "x-auth-token" || kl == "cookie" || kl == "host" || kl == "connection" || kl == "proxy-connection" || kl == "keep-alive" || kl == "transfer-encoding" || kl == "upgrade" {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for k, vals := range src {
		kl := strings.ToLower(k)
		if kl == "connection" || kl == "proxy-connection" || kl == "keep-alive" || kl == "transfer-encoding" || kl == "upgrade" {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func streamCopy(w http.ResponseWriter, src io.Reader) error {
	if f, ok := w.(http.Flusher); ok {
		buf := make([]byte, 4096)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				if _, wErr := w.Write(buf[:n]); wErr != nil {
					return wErr
				}
				f.Flush()
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
		}
	}
	_, err := io.Copy(w, src)
	return err
}
