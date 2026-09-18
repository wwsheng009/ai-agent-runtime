//go:build !win7compat

package transport

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestNewTransport_StreamableAliases(t *testing.T) {
	aliases := []string{"streamable", "streamableHttp", "streamable-http", "streamable_http", "http"}
	for _, alias := range aliases {
		tr, err := NewTransport(&Config{Type: alias, URL: "http://127.0.0.1:12306/mcp"})
		if err != nil {
			t.Fatalf("alias %q: unexpected error: %v", alias, err)
		}
		if got := tr.Type(); got != "streamable" {
			t.Fatalf("alias %q: expected type streamable, got %q", alias, got)
		}
		if tr.ToMCPSdkTransport(context.Background()) == nil {
			t.Fatalf("alias %q: nil SDK transport", alias)
		}
	}
}

func TestNewTransport_Unsupported(t *testing.T) {
	if _, err := NewTransport(&Config{Type: "carrier-pigeon"}); err == nil {
		t.Fatal("expected error for unsupported transport type")
	}
}

func TestStreamableTransport_InjectsHeaders(t *testing.T) {
	var captured http.Header
	base := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		captured = req.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       http.NoBody,
		}, nil
	})
	rt := headerRoundTripper{base: base, headers: http.Header{"Authorization": {"Bearer test"}}}

	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:12306/mcp", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if got := captured.Get("Authorization"); got != "Bearer test" {
		t.Fatalf("expected injected header, got %q", got)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("original request must not be mutated")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
