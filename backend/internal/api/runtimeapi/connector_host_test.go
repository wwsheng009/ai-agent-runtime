package runtimeapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func connectorRequest(remoteAddr string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/connectors", nil)
	req.RemoteAddr = remoteAddr
	return req
}

func TestAuthorizeConnectorAdminAllowsLoopback(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	if err := handler.AuthorizeConnectorAdmin(connectorRequest("127.0.0.1:12345")); err != nil {
		t.Fatalf("loopback should be allowed: %v", err)
	}
}

func TestAuthorizeConnectorAdminDeniesRemoteWithoutToken(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	if err := handler.AuthorizeConnectorAdmin(connectorRequest("203.0.113.7:443")); err == nil {
		t.Fatal("remote request without admin token should be denied")
	}
}

func TestAuthorizeConnectorAdminAcceptsAdminToken(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	handler.SetAdminToken("secret-token")
	req := connectorRequest("203.0.113.7:443")
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	if err := handler.AuthorizeConnectorAdmin(req); err != nil {
		t.Fatalf("valid admin token should be allowed: %v", err)
	}

	bad := connectorRequest("203.0.113.7:443")
	bad.Header.Set("X-Skills-Admin-Token", "wrong")
	if err := handler.AuthorizeConnectorAdmin(bad); err == nil {
		t.Fatal("wrong admin token should be denied")
	}
}
