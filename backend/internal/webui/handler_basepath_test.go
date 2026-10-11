package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIndexInjectsForwardedPrefix(t *testing.T) {
	if !Available() {
		t.Skip("no embedded frontend")
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Prefix", "/n/node-x")
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<base href="/n/node-x/">`) {
		t.Fatalf("missing injected base href")
	}
	if !strings.Contains(body, `__RUNTIME_BASE__="/n/node-x"`) {
		t.Fatalf("missing injected runtime base global")
	}
}

func TestIndexWithoutPrefixHasNoInjection(t *testing.T) {
	if !Available() {
		t.Skip("no embedded frontend")
	}
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(rec.Body.String(), `<base href="/n/`) {
		t.Fatal("base href injected without the forwarded prefix header")
	}
}

func TestForwardedPrefixSanitized(t *testing.T) {
	cases := map[string]string{
		"/n/node-x":     "/n/node-x",
		"/n/node-x/":    "/n/node-x",
		"":              "",
		"n/node-x":      "",
		"/n/../etc":     "",
		"  /n/node-y  ": "/n/node-y",
	}
	for in, want := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if in != "" {
			req.Header.Set("X-Forwarded-Prefix", in)
		}
		if got := forwardedPrefix(req); got != want {
			t.Fatalf("forwardedPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}
