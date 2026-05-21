package app

import (
	"net/http"
	"testing"
)

func TestCopyProxyHeadersForwardsAnthropicBetaHeaders(t *testing.T) {
	src := http.Header{}
	src.Set("Authorization", "Bearer local-token")
	src.Set("Connection", "keep-alive")
	src.Set("Anthropic-Beta", "code-execution-2025-08-25")
	src.Set("X-Anthropic-Beta", "files-api-2025-04-14")
	src.Set("Anthropic-Version", "2023-06-01")

	dst := http.Header{}
	copyProxyHeaders(dst, src)

	if got := dst.Get("Authorization"); got != "" {
		t.Fatalf("Authorization was forwarded: %q", got)
	}
	if got := dst.Get("Connection"); got != "" {
		t.Fatalf("Connection was forwarded: %q", got)
	}
	if got := dst.Get("Anthropic-Beta"); got != "code-execution-2025-08-25" {
		t.Fatalf("Anthropic-Beta = %q", got)
	}
	if got := dst.Get("X-Anthropic-Beta"); got != "files-api-2025-04-14" {
		t.Fatalf("X-Anthropic-Beta = %q", got)
	}
	if got := dst.Get("Anthropic-Version"); got != "2023-06-01" {
		t.Fatalf("Anthropic-Version = %q", got)
	}
}
