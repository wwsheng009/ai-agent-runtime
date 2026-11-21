package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/auth"
)

func TestNormalizeGatewayAccessToken(t *testing.T) {
	if got := normalizeGatewayAccessToken(" secret "); got != "secret" {
		t.Fatalf("normalize = %q, want secret", got)
	}
	if got := normalizeGatewayAccessToken("${AICLI_GATEWAY_ACCESS_TOKEN:-}"); got != "" {
		t.Fatalf("unresolved placeholder must be empty, got %q", got)
	}
}

func TestGatewayNodeProxyAuthRedirectsBrowserAndAcceptsToken(t *testing.T) {
	owner, err := auth.NewOwner("pw", time.Minute)
	if err != nil {
		t.Fatalf("NewOwner: %v", err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := gatewayNodeProxyAuth(owner, "gw-secret", next)

	req := httptest.NewRequest(http.MethodGet, "/n/node-1/api/runtime/status?x=1", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("browser navigation status = %d, want 302", rec.Code)
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, "/?next=%2Fn%2Fnode-1%2Fapi%2Fruntime%2Fstatus") {
		t.Fatalf("redirect location = %q", location)
	}

	apiReq := httptest.NewRequest(http.MethodGet, "/n/node-1/api/runtime/status", nil)
	apiReq.Header.Set("Accept", "application/json")
	apiRec := httptest.NewRecorder()
	handler.ServeHTTP(apiRec, apiReq)
	if apiRec.Code != http.StatusUnauthorized {
		t.Fatalf("API request status = %d, want 401", apiRec.Code)
	}

	tokenReq := httptest.NewRequest(http.MethodGet, "/n/node-1/api/runtime/status", nil)
	tokenReq.Header.Set("Accept", "application/json")
	tokenReq.Header.Set("Authorization", "Bearer gw-secret")
	tokenRec := httptest.NewRecorder()
	handler.ServeHTTP(tokenRec, tokenReq)
	if tokenRec.Code != http.StatusOK {
		t.Fatalf("bearer token status = %d, want 200", tokenRec.Code)
	}
}

func TestWantsHTMLNavigation(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/n/node/", nil)
	req.Header.Set("Accept", "text/html")
	if !wantsHTMLNavigation(req) {
		t.Fatal("browser navigation should be detected")
	}
	req = httptest.NewRequest(http.MethodPost, "/n/node/", nil)
	req.Header.Set("Accept", "text/html")
	if wantsHTMLNavigation(req) {
		t.Fatal("non-GET navigation must not redirect")
	}
}
