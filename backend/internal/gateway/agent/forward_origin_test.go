package agent

import (
	"net/http"
	"testing"
)

func TestRewriteForwardOriginHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Origin", "http://127.0.0.1:8400")
	h.Set("Referer", "http://127.0.0.1:8400/n/node-x/web/?session=s1")
	rewriteForwardOriginHeaders(h, "http://127.0.0.1:52361")
	if got := h.Get("Origin"); got != "http://127.0.0.1:52361" {
		t.Fatalf("Origin = %q, want the local origin", got)
	}
	if got := h.Get("Referer"); got != "http://127.0.0.1:52361/n/node-x/web/?session=s1" {
		t.Fatalf("Referer = %q, want local origin with the original path", got)
	}

	// No Origin/Referer -> nothing added.
	h2 := http.Header{}
	rewriteForwardOriginHeaders(h2, "http://127.0.0.1:52361")
	if h2.Get("Origin") != "" || h2.Get("Referer") != "" {
		t.Fatalf("absent headers must stay absent: %v", h2)
	}

	// Unusable base -> headers untouched.
	h3 := http.Header{}
	h3.Set("Origin", "http://gw:8400")
	rewriteForwardOriginHeaders(h3, "::::bad")
	if h3.Get("Origin") != "http://gw:8400" {
		t.Fatalf("bad base must not corrupt headers: %v", h3)
	}

	// https local base keeps the scheme.
	h4 := http.Header{}
	h4.Set("Origin", "http://gw:8400")
	rewriteForwardOriginHeaders(h4, "https://127.0.0.1:9443")
	if h4.Get("Origin") != "https://127.0.0.1:9443" {
		t.Fatalf("https base origin = %q", h4.Get("Origin"))
	}
}
