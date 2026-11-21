package runtimeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWithNonLocalAccessTokenAllowsLoopbackWithoutToken(t *testing.T) {
	handler := WithNonLocalAccessToken("secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/status", nil)
	req.RemoteAddr = "127.0.0.1:4321"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestWithNonLocalAccessTokenRequiresTokenForRemote(t *testing.T) {
	handler := WithNonLocalAccessToken("secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/status", nil)
	req.RemoteAddr = "203.0.113.7:4321"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "API_UNAUTHORIZED")
}

func TestWithNonLocalAccessTokenAcceptsBearerAndHeader(t *testing.T) {
	handler := WithNonLocalAccessToken("secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for name, set := range map[string]func(*http.Request){
		"authorization": func(r *http.Request) { r.Header.Set("Authorization", "Bearer secret") },
		"header":        func(r *http.Request) { r.Header.Set(AccessTokenHeader, "secret") },
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/runtime/status", nil)
			req.RemoteAddr = "203.0.113.7:4321"
			set(req)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

func TestWithNonLocalAccessTokenRejectsWrongOrMissingConfiguration(t *testing.T) {
	for name, token := range map[string]string{
		"wrong token":   "wrong",
		"missing token": "",
	} {
		t.Run(name, func(t *testing.T) {
			handler := WithNonLocalAccessToken("secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodGet, "/api/runtime/status", nil)
			req.RemoteAddr = "203.0.113.7:4321"
			if token == "" {
				handler = WithNonLocalAccessToken("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
			} else {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			require.Equal(t, http.StatusUnauthorized, rec.Code)
		})
	}
}

func TestWithNonLocalAccessTokenLeavesNonAPIPathsOpen(t *testing.T) {
	handler := WithNonLocalAccessToken("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "203.0.113.7:4321"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestWithNonLocalAccessTokenIgnoresForwardedLoopbackSpoof(t *testing.T) {
	handler := WithNonLocalAccessToken("secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/status", nil)
	req.RemoteAddr = "203.0.113.7:4321"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestNormalizeRuntimeAccessTokenRejectsUnresolvedPlaceholder(t *testing.T) {
	require.Empty(t, normalizeRuntimeAccessToken(" ${SERVER_ACCESS_TOKEN:-} "))
	require.Equal(t, "secret", normalizeRuntimeAccessToken(" secret "))
}

func TestRuntimeAccessStatusAndCookieLogin(t *testing.T) {
	guard := NewRuntimeAccessGuard("secret")
	guard.now = func() time.Time { return time.Unix(1_700_000_000, 0) }

	statusRec := httptest.NewRecorder()
	statusReq := httptest.NewRequest(http.MethodGet, AccessStatusPath, nil)
	statusReq.RemoteAddr = "203.0.113.7:4321"
	guard.Status(statusRec, statusReq)
	require.Equal(t, http.StatusOK, statusRec.Code)
	var status map[string]any
	require.NoError(t, json.Unmarshal(statusRec.Body.Bytes(), &status))
	require.Equal(t, true, status["required"])
	require.Equal(t, false, status["authenticated"])
	require.Equal(t, true, status["configured"])

	badRec := httptest.NewRecorder()
	guard.Login(badRec, httptest.NewRequest(http.MethodPost, AccessLoginPath, strings.NewReader(`{"token":"wrong"}`)))
	require.Equal(t, http.StatusUnauthorized, badRec.Code)

	loginRec := httptest.NewRecorder()
	guard.Login(loginRec, httptest.NewRequest(http.MethodPost, AccessLoginPath, strings.NewReader(`{"token":"secret"}`)))
	require.Equal(t, http.StatusOK, loginRec.Code)
	cookies := loginRec.Result().Cookies()
	require.Len(t, cookies, 1)
	cookie := cookies[0]
	require.Equal(t, AccessCookieName, cookie.Name)
	require.True(t, cookie.HttpOnly)
	require.NotContains(t, cookie.Value, "secret")

	protected := guard.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	remoteReq := httptest.NewRequest(http.MethodGet, "/api/runtime/status", nil)
	remoteReq.RemoteAddr = "203.0.113.7:4321"
	remoteReq.AddCookie(cookie)
	remoteRec := httptest.NewRecorder()
	protected.ServeHTTP(remoteRec, remoteReq)
	require.Equal(t, http.StatusOK, remoteRec.Code)

	statusRec = httptest.NewRecorder()
	statusReq = httptest.NewRequest(http.MethodGet, AccessStatusPath, nil)
	statusReq.RemoteAddr = "203.0.113.7:4321"
	statusReq.AddCookie(cookie)
	guard.Status(statusRec, statusReq)
	require.NoError(t, json.Unmarshal(statusRec.Body.Bytes(), &status))
	require.Equal(t, true, status["authenticated"])
}

func TestRuntimeAccessCookieRejectsTamperAndExpiry(t *testing.T) {
	guard := NewRuntimeAccessGuard("secret")
	base := time.Unix(1_700_000_000, 0)
	guard.now = func() time.Time { return base }
	value, _ := guard.issueSession()

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/status", nil)
	req.RemoteAddr = "203.0.113.7:4321"
	req.AddCookie(&http.Cookie{Name: AccessCookieName, Value: value + "x"})
	require.False(t, guard.authenticated(req))

	guard.now = func() time.Time { return base.Add(guard.sessionTTL + time.Second) }
	req = httptest.NewRequest(http.MethodGet, "/api/runtime/status", nil)
	req.RemoteAddr = "203.0.113.7:4321"
	req.AddCookie(&http.Cookie{Name: AccessCookieName, Value: value})
	require.False(t, guard.authenticated(req))
}

func TestRuntimeAccessAuthEndpointsBypassMiddleware(t *testing.T) {
	handler := WithNonLocalAccessToken("secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, path := range []string{AccessStatusPath, AccessLoginPath, AccessLogoutPath} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.RemoteAddr = "203.0.113.7:4321"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, path)
	}
}

func TestRuntimeAccessStatusLocalIsTokenFree(t *testing.T) {
	guard := NewRuntimeAccessGuard("secret")
	req := httptest.NewRequest(http.MethodGet, AccessStatusPath, nil)
	req.RemoteAddr = "127.0.0.1:4321"
	rec := httptest.NewRecorder()
	guard.Status(rec, req)
	var status map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	require.Equal(t, false, status["required"])
	require.Equal(t, true, status["authenticated"])
	require.Equal(t, true, status["local"])
}

func TestRuntimeAccessGatewayTrustTokenSkipsUserToken(t *testing.T) {
	guard := NewRuntimeAccessGuard("")
	guard.SetGatewayTrustToken("gw-device-secret")
	protected := guard.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/status", nil)
	req.RemoteAddr = "203.0.113.7:4321"
	req.Header.Set(GatewayTrustHeader, "gw-device-secret")
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/api/runtime/status", nil)
	req.RemoteAddr = "203.0.113.7:4321"
	req.Header.Set(GatewayTrustHeader, "wrong")
	rec = httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}
