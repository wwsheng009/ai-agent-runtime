// Package auth implements the gateway's single-owner authentication: an
// Argon2id-hashed password, in-memory session cookies and an HTTP middleware
// that protects the console/API and the reverse-proxy routes. The tunnel
// endpoint (/gw/agent) is authenticated separately by device token.
//
// Design: docs/gateway/04-security.md §2.1.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// CookieName is the owner session cookie.
const CookieName = "aicli_gateway_session"

// DefaultTTL is the default owner session lifetime.
const DefaultTTL = 12 * time.Hour

// argon2 parameters (RFC 9106 low-memory profile).
const (
	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
)

// Owner is the single gateway owner. A zero-value Owner (no password) is
// disabled: middleware passes requests through (development only).
type Owner struct {
	enabled bool
	hash    string
	ttl     time.Duration
	now     func() time.Time

	mu       sync.Mutex
	sessions map[string]time.Time
}

// NewOwner builds an owner from a plaintext password. An empty password yields
// a disabled owner (callers should warn).
func NewOwner(password string, ttl time.Duration) (*Owner, error) {
	o := &Owner{ttl: ttl, now: time.Now, sessions: make(map[string]time.Time)}
	if o.ttl <= 0 {
		o.ttl = DefaultTTL
	}
	password = strings.TrimSpace(password)
	if password == "" {
		return o, nil
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	o.enabled = true
	o.hash = hash
	return o, nil
}

// Enabled reports whether authentication is enforced.
func (o *Owner) Enabled() bool { return o != nil && o.enabled }

// TTL returns the session lifetime.
func (o *Owner) TTL() time.Duration {
	if o == nil || o.ttl <= 0 {
		return DefaultTTL
	}
	return o.ttl
}

// VerifyPassword reports whether password matches the stored hash.
func (o *Owner) VerifyPassword(password string) bool {
	if !o.Enabled() {
		return false
	}
	return verifyPassword(password, o.hash)
}

// Issue creates a new session token with its expiry.
func (o *Owner) Issue() (string, time.Time) {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	token := hex.EncodeToString(buf)
	expiry := o.now().Add(o.TTL())
	o.mu.Lock()
	o.sessions[token] = expiry
	o.mu.Unlock()
	return token, expiry
}

// Valid reports whether token names a live session.
func (o *Owner) Valid(token string) bool {
	if !o.Enabled() {
		return false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	expiry, ok := o.sessions[token]
	if !ok {
		return false
	}
	if o.now().After(expiry) {
		delete(o.sessions, token)
		return false
	}
	return true
}

// Revoke invalidates a session token.
func (o *Owner) Revoke(token string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	delete(o.sessions, token)
	o.mu.Unlock()
}

// RequireSession wraps next with owner-session enforcement. A disabled owner
// passes requests through unchanged.
func (o *Owner) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !o.Enabled() {
			next.ServeHTTP(w, r)
			return
		}
		if cookie, err := r.Cookie(CookieName); err == nil && o.Valid(cookie.Value) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

// SessionCookie builds the session cookie for a token.
func SessionCookie(token string, ttl time.Duration, secure bool) *http.Cookie {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
		Secure:   secure,
	}
}

// ClearCookie builds a cookie that deletes the session.
func ClearCookie(secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Secure:   secure,
	}
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 3 || parts[0] != "argon2id" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
