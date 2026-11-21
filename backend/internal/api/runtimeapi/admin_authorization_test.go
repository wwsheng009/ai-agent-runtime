package runtimeapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

const runtimeAdminTestToken = "runtimeapi-admin-test-token"

func runtimeAdminRequest(remoteAddr string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/status", nil)
	req.RemoteAddr = remoteAddr
	return req
}

func addRuntimeAdminTestToken(req *http.Request) *http.Request {
	req.Header.Set("X-Skills-Admin-Token", runtimeAdminTestToken)
	return req
}

func runtimeAdminAuthorizers(handler *Handler) map[string]func(*http.Request) error {
	return map[string]func(*http.Request) error{
		"search":          handler.authorizeSearchAdmin,
		"usage":           handler.authorizeUsageAdmin,
		"skill mutation":  handler.authorizeSkillMutation,
		"memory":          handler.authorizeMemoryAdmin,
		"profile write":   handler.authorizeProfileWrite,
		"session routing": handler.authorizeSessionRoutingWrite,
		"connector":       handler.AuthorizeConnectorAdmin,
	}
}

func TestRuntimeAdminAuthorizationRejectsLoopbackWithoutCredentials(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	// 本用例锁定 legacy（显式开启）模式下的凭证边界；默认模式已改为放行。
	handler.SetAdminAuthRequired(true)
	handler.SetAdminToken(runtimeAdminTestToken)

	for name, authorize := range runtimeAdminAuthorizers(handler) {
		t.Run(name, func(t *testing.T) {
			req := runtimeAdminRequest("127.0.0.1:54321")
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			require.Error(t, authorize(req), "loopback source must not bypass management credentials")
		})
	}

	req := runtimeAdminRequest("127.0.0.1:54321")
	require.Equal(t, "denied", handler.searchAdminAccessMode(req))
	require.Equal(t, "denied", handler.skillMutationAccessMode(req))
	require.Empty(t, handler.resolveGrantedSkillPermissions(req))
}

func TestRuntimeAdminAuthorizationAcceptsTokenFromAnySource(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	handler.SetAdminAuthRequired(true)
	handler.SetAdminToken(runtimeAdminTestToken)

	for name, authorize := range runtimeAdminAuthorizers(handler) {
		t.Run(name, func(t *testing.T) {
			req := runtimeAdminRequest("203.0.113.7:443")
			req.Header.Set("X-Skills-Admin-Token", runtimeAdminTestToken)
			require.NoError(t, authorize(req))
		})
	}

	req := runtimeAdminRequest("203.0.113.7:443")
	req.Header.Set("X-Skills-Admin-Token", runtimeAdminTestToken)
	require.Equal(t, "token", handler.searchAdminAccessMode(req))
	require.Equal(t, "token", handler.skillMutationAccessMode(req))
	require.Equal(t, []string{"*"}, handler.resolveGrantedSkillPermissions(req))
}

func TestRuntimeAdminAuthorizationAcceptsTrustedRoleFromAnySource(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	handler.SetAdminAuthRequired(true)
	handler.SetScopeResolverConfig(ScopeResolverConfig{
		Enabled:     true,
		RoleHeaders: []string{"X-Test-Role"},
		AdminRoles:  []string{"admin"},
	})

	for name, authorize := range runtimeAdminAuthorizers(handler) {
		t.Run(name, func(t *testing.T) {
			req := runtimeAdminRequest("203.0.113.7:443")
			req.Header.Set("X-Test-Role", "admin")
			require.NoError(t, authorize(req))
		})
	}

	req := runtimeAdminRequest("203.0.113.7:443")
	req.Header.Set("X-Test-Role", "admin")
	require.Equal(t, "role", handler.searchAdminAccessMode(req))
	require.Equal(t, "role", handler.skillMutationAccessMode(req))
	require.Equal(t, []string{"*"}, handler.resolveGrantedSkillPermissions(req))
}

// TestRuntimeAdminAuthorizationOpenByDefault 锁定临时策略：运行管理面默认
// 不再要求 admin token / admin role，workspace 用户不应出现"部分功能 403"。
func TestRuntimeAdminAuthorizationOpenByDefault(t *testing.T) {
	handler := NewHandler(nil, nil, nil)

	for name, authorize := range runtimeAdminAuthorizers(handler) {
		t.Run(name, func(t *testing.T) {
			req := runtimeAdminRequest("203.0.113.7:443")
			require.NoError(t, authorize(req), "default runtime admin access must be open")
		})
	}

	req := runtimeAdminRequest("203.0.113.7:443")
	require.Equal(t, "open", handler.searchAdminAccessMode(req))
	require.Equal(t, "open", handler.skillMutationAccessMode(req))

	// admin access 放行不等于自动授予全部技能权限：显式权限头仍按原规则生效。
	req.Header.Set("X-Skills-Permission", "shell,view")
	require.Equal(t, []string{"shell", "view"}, handler.resolveGrantedSkillPermissions(req))
}
