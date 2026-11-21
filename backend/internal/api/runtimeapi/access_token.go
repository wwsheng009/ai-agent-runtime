package runtimeapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	pathpkg "path"
	"strconv"
	"strings"
	"time"
)

const (
	// AccessTokenHeader is an internal/API-client header accepted in addition
	// to the standard Authorization: Bearer form. Gateway-owned hops strip it
	// so a remote caller can never inject a node-local credential.
	AccessTokenHeader = "X-AICLI-Access-Token"
	// GatewayTrustHeader is injected by the local gateway agent after a
	// successful gateway registration. It proves the request came through the
	// registered gateway path and lets that path skip the user-facing runtime
	// access token (the gateway performs its own external auth).
	GatewayTrustHeader = "X-AICLI-Gateway-Token"
	// AccessCookieName stores the browser remote-access session created by the
	// login endpoint. It is HttpOnly and never contains the raw access token.
	AccessCookieName = "aicli_runtime_access"
	// AccessStatusPath / AccessLoginPath / AccessLogoutPath are public auth
	// endpoints (loopback and non-loopback) used by the SPA access gate.
	AccessStatusPath = "/api/runtime/auth/access/status"
	AccessLoginPath  = "/api/runtime/auth/access/login"
	AccessLogoutPath = "/api/runtime/auth/access/logout"

	defaultAccessSessionTTL = 12 * time.Hour
	maxAccessLoginBodyBytes = 4096
)

// RuntimeAccessGuard enforces the non-local runtime API access token and
// exposes the browser login/session lifecycle.
type RuntimeAccessGuard struct {
	token        string
	gatewayToken string
	sessionTTL   time.Duration
	now          func() time.Time
}

// SetGatewayTrustToken configures the shared gateway registration token. The
// gateway agent injects it on the loopback hop only; the raw device token never
// crosses the tunnel to the browser.
func (g *RuntimeAccessGuard) SetGatewayTrustToken(token string) {
	if g == nil {
		return
	}
	g.gatewayToken = normalizeRuntimeAccessToken(token)
}

// NewRuntimeAccessGuard creates a guard for the configured access token. An
// empty token disables non-local API access (fail closed) but keeps local
// loopback usage token-free.
func NewRuntimeAccessGuard(token string) *RuntimeAccessGuard {
	return &RuntimeAccessGuard{
		token:      normalizeRuntimeAccessToken(token),
		sessionTTL: defaultAccessSessionTTL,
		now:        time.Now,
	}
}

// WithNonLocalAccessToken is the middleware-only compatibility entry used by
// tests and hosts that do not need the browser login endpoints.
func WithNonLocalAccessToken(token string, next http.Handler) http.Handler {
	return NewRuntimeAccessGuard(token).Middleware(next)
}

// Middleware protects the runtime data plane (/api/*) from direct non-loopback
// access. The local UI and the gateway agent connect over loopback and keep
// the zero-friction local experience; remote callers must present the
// configured token or a valid login cookie.
//
// Access is fail-closed: an empty token denies every non-loopback API request.
// Only the direct peer address is considered; X-Forwarded-For is deliberately
// ignored here so a remote client cannot spoof loopback, and a local reverse
// proxy is trusted only insofar as it is a local process (operators exposing a
// local proxy must add their own edge authentication).
func (g *RuntimeAccessGuard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if next == nil {
			http.NotFound(w, r)
			return
		}
		if !requiresRuntimeAccessToken(r) || isDirectLoopbackRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if g != nil && g.authenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		writeRuntimeAccessTokenError(w, g == nil || g.token == "")
	})
}

// Status returns the access state for the SPA gate. It is intentionally
// unauthenticated: a remote browser must be able to discover whether a token
// is required/configured before it can show a login form.
func (g *RuntimeAccessGuard) Status(w http.ResponseWriter, r *http.Request) {
	local := isDirectLoopbackRequest(r)
	configured := g != nil && g.token != ""
	authenticated := local
	if !authenticated && g != nil {
		authenticated = g.authenticated(r)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeAccessJSON(w, http.StatusOK, map[string]any{
		"required":      !local,
		"authenticated": authenticated,
		"local":         local,
		"configured":    configured,
	})
}

// Login validates the configured token and issues a signed HttpOnly session
// cookie. The raw token is never stored in the cookie.
func (g *RuntimeAccessGuard) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAccessJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"code":  "METHOD_NOT_ALLOWED",
			"error": "POST required",
		})
		return
	}
	if g == nil || g.token == "" {
		writeAccessJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":  "API_UNAUTHORIZED",
			"error": "[API_UNAUTHORIZED] non-local access is disabled: server.access_token is not configured",
		})
		return
	}
	var payload struct {
		Token string `json:"token"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAccessLoginBodyBytes))
	if err := decoder.Decode(&payload); err != nil {
		writeAccessJSON(w, http.StatusBadRequest, map[string]any{
			"code":  "VALIDATION_FAILED",
			"error": "[VALIDATION_FAILED] invalid access login payload",
		})
		return
	}
	provided := strings.TrimSpace(payload.Token)
	if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(g.token)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="ai-agent-runtime"`)
		writeAccessJSON(w, http.StatusUnauthorized, map[string]any{
			"code":  "API_UNAUTHORIZED",
			"error": "[API_UNAUTHORIZED] invalid access token",
		})
		return
	}
	value, expiresAt := g.issueSession()
	http.SetCookie(w, &http.Cookie{
		Name:     AccessCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestUsesHTTPS(r),
		Expires:  expiresAt,
		MaxAge:   int(g.sessionTTL.Seconds()),
	})
	writeAccessJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"expires_at":    expiresAt.UTC().Format(time.RFC3339),
	})
}

// Logout clears the browser access session.
func (g *RuntimeAccessGuard) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAccessJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"code":  "METHOD_NOT_ALLOWED",
			"error": "POST required",
		})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     AccessCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestUsesHTTPS(r),
		MaxAge:   -1,
	})
	writeAccessJSON(w, http.StatusOK, map[string]any{"authenticated": false})
}

func (g *RuntimeAccessGuard) authenticated(r *http.Request) bool {
	if g == nil || r == nil {
		return false
	}
	if g.token != "" && hasValidRuntimeAccessToken(r, g.token) {
		return true
	}
	if g.gatewayToken != "" && hasValidGatewayTrustToken(r, g.gatewayToken) {
		return true
	}
	return g.hasValidSessionCookie(r)
}

func (g *RuntimeAccessGuard) issueSession() (string, time.Time) {
	expiresAt := g.now().Add(g.sessionTTL)
	payload := strconv.FormatInt(expiresAt.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(g.token))
	_, _ = mac.Write([]byte(payload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + signature, expiresAt
}

func (g *RuntimeAccessGuard) hasValidSessionCookie(r *http.Request) bool {
	if g == nil || g.token == "" || r == nil {
		return false
	}
	cookie, err := r.Cookie(AccessCookieName)
	if err != nil || cookie == nil {
		return false
	}
	parts := strings.SplitN(strings.TrimSpace(cookie.Value), ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || g.now().Unix() >= expiry {
		return false
	}
	mac := hmac.New(sha256.New, []byte(g.token))
	_, _ = mac.Write([]byte(parts[0]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(parts[1]), []byte(expected)) == 1
}

func requestUsesHTTPS(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func requiresRuntimeAccessToken(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return true
	}
	cleaned := pathpkg.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
	if isRuntimeAccessAuthPath(cleaned) {
		return false
	}
	return cleaned == "/api" || strings.HasPrefix(cleaned, "/api/")
}

func isRuntimeAccessAuthPath(cleaned string) bool {
	switch cleaned {
	case AccessStatusPath, AccessLoginPath, AccessLogoutPath:
		return true
	default:
		return false
	}
}

func isDirectLoopbackRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	return isLoopbackIP(remoteAddrHost(r.RemoteAddr))
}

func hasValidRuntimeAccessToken(r *http.Request, expected string) bool {
	if r == nil || expected == "" {
		return false
	}
	candidates := make([]string, 0, 2)
	if header := strings.TrimSpace(r.Header.Get(AccessTokenHeader)); header != "" {
		candidates = append(candidates, header)
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		if token := strings.TrimSpace(auth[len("Bearer "):]); token != "" {
			candidates = append(candidates, token)
		}
	}
	for _, candidate := range candidates {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(expected)) == 1 {
			return true
		}
	}
	return false
}

func hasValidGatewayTrustToken(r *http.Request, expected string) bool {
	if r == nil || expected == "" {
		return false
	}
	provided := strings.TrimSpace(r.Header.Get(GatewayTrustHeader))
	if provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func writeRuntimeAccessTokenError(w http.ResponseWriter, tokenMissing bool) {
	message := "non-local runtime API access requires a valid access token"
	if tokenMissing {
		message = "non-local runtime API access is disabled: server.access_token is not configured"
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="ai-agent-runtime"`)
	writeAccessJSON(w, http.StatusUnauthorized, map[string]any{
		"code":  "API_UNAUTHORIZED",
		"error": "[API_UNAUTHORIZED] " + message,
	})
}

func writeAccessJSON(w http.ResponseWriter, status int, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func normalizeRuntimeAccessToken(raw string) string {
	token := strings.TrimSpace(raw)
	// Defensive: an unresolved YAML placeholder must never become a public
	// default credential.
	if strings.HasPrefix(token, "${") && strings.HasSuffix(token, "}") {
		return ""
	}
	return token
}
