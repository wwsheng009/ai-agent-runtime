package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDisabledOwnerPassesThrough(t *testing.T) {
	owner, err := NewOwner("", 0)
	if err != nil {
		t.Fatalf("NewOwner: %v", err)
	}
	if owner.Enabled() {
		t.Fatal("empty password must yield a disabled owner")
	}
	handler := owner.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nodes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("disabled owner should pass through, got %d", rec.Code)
	}
}

func TestEnabledOwnerRequiresSession(t *testing.T) {
	owner, err := NewOwner("correct horse", time.Hour)
	if err != nil {
		t.Fatalf("NewOwner: %v", err)
	}
	if !owner.Enabled() {
		t.Fatal("expected enabled owner")
	}
	if !owner.VerifyPassword("correct horse") {
		t.Fatal("correct password must verify")
	}
	if owner.VerifyPassword("wrong") {
		t.Fatal("wrong password must not verify")
	}

	handler := owner.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nodes", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing cookie must be 401, got %d", rec.Code)
	}

	token, _ := owner.Issue()
	req := httptest.NewRequest(http.MethodGet, "/api/nodes", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid session must pass, got %d", rec.Code)
	}

	owner.Revoke(token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session must be 401, got %d", rec.Code)
	}
}

func TestSessionExpiry(t *testing.T) {
	owner, err := NewOwner("pw", time.Minute)
	if err != nil {
		t.Fatalf("NewOwner: %v", err)
	}
	base := time.Unix(1_700_000_000, 0)
	owner.now = func() time.Time { return base }
	token, _ := owner.Issue()
	if !owner.Valid(token) {
		t.Fatal("fresh session must be valid")
	}
	owner.now = func() time.Time { return base.Add(2 * time.Minute) }
	if owner.Valid(token) {
		t.Fatal("expired session must be invalid")
	}
}

func TestCookieHelpers(t *testing.T) {
	c := SessionCookie("tok", time.Hour, true)
	if c.Name != CookieName || c.Value != "tok" || !c.HttpOnly || !c.Secure {
		t.Fatalf("session cookie = %+v", c)
	}
	if c.MaxAge != 3600 {
		t.Fatalf("max age = %d, want 3600", c.MaxAge)
	}
	clear := ClearCookie(false)
	if clear.MaxAge != -1 {
		t.Fatalf("clear cookie max age = %d, want -1", clear.MaxAge)
	}
}
