package httpclient

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/buildinfo"
)

type captureRoundTripper struct {
	last *http.Request
}

func (c *captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.last = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestWithDefaultUserAgentInjectsWhenMissing(t *testing.T) {
	capture := &captureRoundTripper{}
	client := &http.Client{Transport: WithDefaultUserAgent(capture)}

	req, err := http.NewRequest(http.MethodGet, "http://example.invalid/v1", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	resp.Body.Close()

	if capture.last == nil {
		t.Fatal("expected captured request")
	}
	got := capture.last.Header.Get("User-Agent")
	if got != buildinfo.UserAgent() {
		t.Fatalf("User-Agent = %q, want %q", got, buildinfo.UserAgent())
	}
}

func TestWithDefaultUserAgentPreservesExplicitValue(t *testing.T) {
	capture := &captureRoundTripper{}
	client := &http.Client{Transport: WithDefaultUserAgent(capture)}

	req, err := http.NewRequest(http.MethodGet, "http://example.invalid/v1", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("User-Agent", "custom-agent/9.9")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	resp.Body.Close()

	if got := capture.last.Header.Get("User-Agent"); got != "custom-agent/9.9" {
		t.Fatalf("User-Agent = %q, want custom-agent/9.9", got)
	}
}

func TestNewProviderHTTPClientSetsDefaultUserAgentTransport(t *testing.T) {
	client := NewProviderHTTPClient(0, nil, false)
	if client == nil || client.Transport == nil {
		t.Fatal("expected non-nil client transport")
	}
	if _, ok := client.Transport.(*defaultUserAgentRoundTripper); !ok {
		t.Fatalf("expected defaultUserAgentRoundTripper, got %T", client.Transport)
	}
}
