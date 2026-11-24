package runtimeapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestResolveTeamProductIdentityRejectsCallerChosenAndIncompleteScopes(t *testing.T) {
	handler := &Handler{}
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled:          true,
		WorkspaceHeaders: []string{"X-Workspace-ID"},
		UserHeaders:      []string{"X-User-ID"},
	})

	request := httptest.NewRequest(http.MethodGet, "/?workspace_id=chosen-workspace&user_id=chosen-owner", nil)
	request.Header.Set("X-Workspace-ID", "header-workspace")
	request.Header.Set("X-User-ID", "header-owner")
	if identity, ok := handler.resolveTeamProductIdentity(request); ok {
		t.Fatalf("caller-controlled values must not establish product identity: %#v", identity)
	}

	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled:          true,
		JWTClaimsEnabled: true,
		JWTSecret:        "team-test-secret",
		UserClaims:       []string{"sub"},
		WorkspaceClaims:  []string{"workspace_id"},
	})
	token := signedTeamIdentityToken(t, "team-test-secret", jwt.MapClaims{"sub": "owner-a"})
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	if identity, ok := handler.resolveTeamProductIdentity(request); ok {
		t.Fatalf("JWT without workspace claim must not establish product identity: %#v", identity)
	}
}

func TestResolveTeamProductIdentityUsesSignedJWTClaimsOnly(t *testing.T) {
	const secret = "team-test-secret"
	handler := &Handler{}
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled:          true,
		JWTClaimsEnabled: true,
		JWTSecret:        secret,
		UserClaims:       []string{"sub"},
		WorkspaceClaims:  []string{"workspace_id"},
		UserHeaders:      []string{"X-User-ID"},
		WorkspaceHeaders: []string{"X-Workspace-ID"},
	})

	token := signedTeamIdentityToken(t, secret, jwt.MapClaims{
		"sub":          "trusted-owner",
		"workspace_id": "trusted-workspace",
	})
	request := httptest.NewRequest(http.MethodGet,
		"/?user_id=query-owner&workspace_id=query-workspace", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-User-ID", "header-owner")
	request.Header.Set("X-Workspace-ID", "header-workspace")

	identity, ok := handler.resolveTeamProductIdentity(request)
	if !ok {
		t.Fatal("valid signed JWT identity should be accepted")
	}
	if identity.OwnerID != "trusted-owner" || identity.WorkspaceID != "trusted-workspace" {
		t.Fatalf("identity must come from signed JWT claims, got %#v", identity)
	}
}

func TestResolveTeamProductIdentityUsesConfiguredAPIKeyBinding(t *testing.T) {
	handler := &Handler{}
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled: true,
		APIKeyScopes: map[string]UsageScope{
			"known-key": {UserID: "key-owner", WorkspaceID: "key-workspace"},
		},
		UserHeaders:      []string{"X-User-ID"},
		WorkspaceHeaders: []string{"X-Workspace-ID"},
	})
	request := httptest.NewRequest(http.MethodGet,
		"/?user_id=query-owner&workspace_id=query-workspace", nil)
	request.Header.Set("X-API-Key", "known-key")
	request.Header.Set("X-User-ID", "header-owner")
	request.Header.Set("X-Workspace-ID", "header-workspace")

	identity, ok := handler.resolveTeamProductIdentity(request)
	if !ok {
		t.Fatal("configured API-key binding should establish product identity")
	}
	if identity.OwnerID != "key-owner" || identity.WorkspaceID != "key-workspace" {
		t.Fatalf("identity must come from API-key binding, got %#v", identity)
	}
}

func TestResolveTeamProductIdentityRejectsConflictingTrustedCredentials(t *testing.T) {
	const secret = "team-test-secret"
	handler := &Handler{}
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled:          true,
		JWTClaimsEnabled: true,
		JWTSecret:        secret,
		UserClaims:       []string{"sub"},
		WorkspaceClaims:  []string{"workspace_id"},
		APIKeyScopes: map[string]UsageScope{
			"known-key": {UserID: "key-owner", WorkspaceID: "key-workspace"},
		},
	})
	token := signedTeamIdentityToken(t, secret, jwt.MapClaims{
		"sub":          "jwt-owner",
		"workspace_id": "jwt-workspace",
	})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-API-Key", "known-key")

	if identity, ok := handler.resolveTeamProductIdentity(request); ok {
		t.Fatalf("conflicting trusted credentials must fail closed, got %#v", identity)
	}
}

func signedTeamIdentityToken(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign test JWT: %v", err)
	}
	return signed
}
