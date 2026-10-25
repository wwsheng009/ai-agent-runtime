package appdevice

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRegisterPublicAndUpsert(t *testing.T) {
	store := NewStore()
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }

	public, created, err := store.Register("Android", "fcm-token-1234567890", "phone")
	if err != nil {
		t.Fatal(err)
	}
	if !created || public.ID == "" {
		t.Fatalf("created=%v device=%+v", created, public)
	}
	if public.Platform != "android" || public.Label != "phone" {
		t.Fatalf("public = %+v", public)
	}
	if strings.Contains(public.TokenHint, "1234567890") {
		t.Fatalf("token hint leaked token material: %q", public.TokenHint)
	}
	if len(store.List()) != 1 || store.Count() != 1 {
		t.Fatalf("list=%d count=%d", len(store.List()), store.Count())
	}

	store.now = func() time.Time { return base.Add(time.Minute) }
	again, created, err := store.Register("android", "fcm-token-1234567890", "new label")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("same token must upsert, not create a second device")
	}
	if again.ID != public.ID || again.Label != "new label" {
		t.Fatalf("upsert = %+v, want id=%s label=new label", again, public.ID)
	}
	if !again.UpdatedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("updated_at = %v", again.UpdatedAt)
	}
	if store.Count() != 1 {
		t.Fatalf("count after upsert = %d", store.Count())
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app-devices.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := store.Register("ios", "apns-token-abcdef", "iphone")
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	devices := reopened.List()
	if len(devices) != 1 {
		t.Fatalf("reopened devices = %d", len(devices))
	}
	if devices[0].ID != public.ID || devices[0].Token != "apns-token-abcdef" {
		t.Fatalf("reopened device = %+v", devices[0])
	}
	if reopened.PublicList()[0].TokenHint == "apns-token-abcdef" {
		t.Fatal("public projection leaked token")
	}
}

func TestDelete(t *testing.T) {
	store := NewStore()
	public, _, err := store.Register("windows", "wns-token", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	if !store.Delete(public.ID) {
		t.Fatal("delete must report an existing id")
	}
	if store.Delete(public.ID) {
		t.Fatal("second delete must report false")
	}
	if store.Count() != 0 {
		t.Fatalf("count = %d", store.Count())
	}
}

func TestTokenHintNeverLeaksShortToken(t *testing.T) {
	store := NewStore()
	public, _, err := store.Register("ios", "short", "test")
	if err != nil {
		t.Fatal(err)
	}
	if public.TokenHint != "…" {
		t.Fatalf("short token hint = %q", public.TokenHint)
	}
}

func TestValidation(t *testing.T) {
	store := NewStore()
	for _, tc := range []struct {
		name     string
		platform string
		token    string
		label    string
	}{
		{name: "platform", platform: "symbian", token: "x"},
		{name: "empty token", platform: "ios", token: " "},
		{name: "long token", platform: "ios", token: strings.Repeat("x", TokenMaxLen+1)},
		{name: "long label", platform: "ios", token: "x", label: strings.Repeat("y", LabelMaxLen+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := store.Register(tc.platform, tc.token, tc.label); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
