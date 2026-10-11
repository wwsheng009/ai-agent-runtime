package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func resolverRequest(remote, xff, xri string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remote
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	if xri != "" {
		req.Header.Set("X-Real-IP", xri)
	}
	return req
}

func TestResolverHonorsForwardedFromTrustedProxy(t *testing.T) {
	resolver := NewResolver([]string{"127.0.0.1/32", "10.0.0.0/8"})
	got := resolver.ClientIP(resolverRequest("127.0.0.1:5555", "203.0.113.7, 10.0.0.1", ""))
	if got != "203.0.113.7" {
		t.Fatalf("ClientIP = %q, want 203.0.113.7", got)
	}
}

func TestResolverIgnoresForwardedFromUntrusted(t *testing.T) {
	resolver := NewResolver([]string{"127.0.0.1/32"})
	got := resolver.ClientIP(resolverRequest("198.51.100.9:5555", "203.0.113.7", ""))
	if got != "198.51.100.9" {
		t.Fatalf("ClientIP = %q, want the socket peer 198.51.100.9", got)
	}
}

func TestResolverFallsBackToRealIPThenRemote(t *testing.T) {
	resolver := NewResolver([]string{"10.0.0.0/8"})
	if got := resolver.ClientIP(resolverRequest("10.1.2.3:1", "", "203.0.113.9")); got != "203.0.113.9" {
		t.Fatalf("ClientIP = %q, want 203.0.113.9", got)
	}
	if got := resolver.ClientIP(resolverRequest("10.1.2.3:1", "not-an-ip", "")); got != "10.1.2.3" {
		t.Fatalf("ClientIP = %q, want 10.1.2.3", got)
	}
}

func TestResolverBareIPTrust(t *testing.T) {
	resolver := NewResolver([]string{"127.0.0.1"})
	if got := resolver.ClientIP(resolverRequest("127.0.0.1:1", "203.0.113.7", "")); got != "203.0.113.7" {
		t.Fatalf("bare IP trust failed: %q", got)
	}
}
