package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBanListAutoBansAtThreshold(t *testing.T) {
	bans := NewBanList(3, time.Minute, 10*time.Minute)
	base := time.Unix(1_700_000_000, 0)
	bans.now = func() time.Time { return base }

	if bans.Record("ip") || bans.Record("ip") {
		t.Fatal("must not ban before the threshold")
	}
	if !bans.Record("ip") {
		t.Fatal("third penalty must ban")
	}
	if !bans.IsBanned("ip") {
		t.Fatal("ip must be banned")
	}
	if len(bans.Entries()) != 1 {
		t.Fatalf("expected 1 ban entry, got %d", len(bans.Entries()))
	}
}

func TestBanListExpires(t *testing.T) {
	bans := NewBanList(1, time.Minute, time.Minute)
	base := time.Unix(1_700_000_000, 0)
	bans.now = func() time.Time { return base }
	bans.Record("ip")
	if !bans.IsBanned("ip") {
		t.Fatal("expected ban")
	}
	bans.now = func() time.Time { return base.Add(2 * time.Minute) }
	if bans.IsBanned("ip") {
		t.Fatal("ban must expire")
	}
}

func TestBanListUnbanAndDisabled(t *testing.T) {
	bans := NewBanList(1, time.Minute, time.Minute)
	bans.Record("ip")
	if !bans.Unban("ip") {
		t.Fatal("Unban must report the entry existed")
	}
	if bans.IsBanned("ip") {
		t.Fatal("unbanned ip must not be banned")
	}

	disabled := NewBanList(0, time.Minute, time.Minute)
	for i := 0; i < 10; i++ {
		if disabled.Record("ip") {
			t.Fatal("disabled ban list must never ban")
		}
	}
}

func TestBanListMiddleware(t *testing.T) {
	bans := NewBanList(1, time.Minute, time.Minute)
	bans.Ban("203.0.113.7", time.Minute)
	handler := bans.Middleware(ClientIP, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/n/x/", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("banned ip = %d, want 403", rec.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/n/x/", nil)
	req2.RemoteAddr = "198.51.100.9:5555"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("unbanned ip = %d, want 200", rec2.Code)
	}
}
