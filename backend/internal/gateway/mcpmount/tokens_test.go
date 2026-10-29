package mcpmount

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokenCreateAuthenticateAndScope(t *testing.T) {
	store := NewTokenStore()
	plaintext, info, err := store.Create("ci", []string{"team-b", "k7f3qz2a", "k7f3qz2a"}, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(plaintext, TokenPrefix) {
		t.Fatalf("token %q does not carry the %q prefix", plaintext, TokenPrefix)
	}
	if info.Prefix == "" || strings.Contains(plaintext, info.Prefix) == false {
		t.Fatalf("prefix %q is not a prefix of the token", info.Prefix)
	}
	if len(info.Prefix) >= len(plaintext) {
		t.Fatalf("prefix exposes the whole secret")
	}
	if len(info.Mounts) != 2 || info.Mounts[0] != "k7f3qz2a" || info.Mounts[1] != "team-b" {
		t.Fatalf("scope = %v, want deduped sorted [k7f3qz2a team-b]", info.Mounts)
	}

	got, ok := store.Authenticate(plaintext)
	if !ok {
		t.Fatalf("Authenticate rejected a fresh token")
	}
	if !got.Allows("k7f3qz2a") || !got.Allows("team-b") {
		t.Fatalf("token does not allow its declared mounts: %v", got.Mounts)
	}
	if got.Allows("other-mount") {
		t.Fatalf("token allows a mount outside its scope")
	}
	if got.LastUsed.IsZero() {
		t.Fatalf("LastUsed was not refreshed")
	}

	if _, ok := store.Authenticate("amt_not-a-real-token"); ok {
		t.Fatalf("Authenticate accepted an unknown token")
	}
	if _, ok := store.Authenticate(""); ok {
		t.Fatalf("Authenticate accepted an empty token")
	}
	if got := store.CountForMount("k7f3qz2a"); got != 1 {
		t.Fatalf("CountForMount = %d, want 1", got)
	}
}

func TestTokenCreateRequiresScope(t *testing.T) {
	store := NewTokenStore()
	if _, _, err := store.Create("no-scope", nil, 0); err == nil {
		t.Fatalf("Create accepted an empty scope")
	}
	if _, _, err := store.Create("blank-scope", []string{"  "}, 0); err == nil {
		t.Fatalf("Create accepted a blank scope")
	}
}

func TestTokenListForMountAndGet(t *testing.T) {
	store := NewTokenStore()
	base := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	tick := 0
	store.now = func() time.Time {
		tick++
		return base.Add(time.Duration(tick) * time.Second)
	}
	_, first, err := store.Create("first", []string{"aaaaaaaa", "bbbbbbbb"}, time.Hour)
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	_, second, err := store.Create("second", []string{"bbbbbbbb"}, time.Hour)
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}
	if !store.Revoke(second.ID) {
		t.Fatalf("Revoke second failed")
	}

	aTokens := store.ListForMount("aaaaaaaa")
	if len(aTokens) != 1 || aTokens[0].ID != first.ID {
		t.Fatalf("ListForMount(aaaaaaaa) = %+v, want only %s", aTokens, first.ID)
	}
	bTokens := store.ListForMount("bbbbbbbb")
	if len(bTokens) != 2 {
		t.Fatalf("ListForMount(bbbbbbbb) = %d tokens, want 2", len(bTokens))
	}
	if bTokens[0].ID != second.ID {
		t.Fatalf("ListForMount must be newest first, got %s then %s", bTokens[0].ID, bTokens[1].ID)
	}

	got, ok := store.Get(second.ID)
	if !ok || !got.Revoked {
		t.Fatalf("Get(%s) = %+v, %v; want revoked record", second.ID, got, ok)
	}
	if _, ok := store.Get("missing"); ok {
		t.Fatalf("Get returned a record for an unknown id")
	}
}

func TestTokenExpiry(t *testing.T) {
	store := NewTokenStore()
	base := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }

	plaintext, info, err := store.Create("short-lived", []string{"k7f3qz2a"}, time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !info.ExpiresAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("ExpiresAt = %v, want %v", info.ExpiresAt, base.Add(time.Hour))
	}
	if _, ok := store.Authenticate(plaintext); !ok {
		t.Fatalf("Authenticate rejected a token before expiry")
	}

	store.now = func() time.Time { return base.Add(2 * time.Hour) }
	if _, ok := store.Authenticate(plaintext); ok {
		t.Fatalf("Authenticate accepted an expired token")
	}
	if got := store.CountForMount("k7f3qz2a"); got != 0 {
		t.Fatalf("CountForMount = %d after expiry, want 0", got)
	}
}

func TestTokenRevokeAndPurge(t *testing.T) {
	store := NewTokenStore()
	plaintext, info, err := store.Create("laptop", []string{"k7f3qz2a"}, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if store.Delete(info.ID) {
		t.Fatalf("Delete removed a live token")
	}
	if !store.Revoke(info.ID) {
		t.Fatalf("Revoke reported a missing token")
	}
	if _, ok := store.Authenticate(plaintext); ok {
		t.Fatalf("Authenticate accepted a revoked token")
	}
	if store.Revoke(info.ID) == false {
		t.Fatalf("second Revoke = false, want idempotent success")
	}
	if !store.Delete(info.ID) {
		t.Fatalf("Delete refused an already revoked token")
	}
	if got := store.Count(); got != 0 {
		t.Fatalf("Count = %d after purge, want 0", got)
	}
	if store.Revoke("unknown") {
		t.Fatalf("Revoke(unknown) = true")
	}
}

func TestTokenPersistenceNeverStoresPlaintext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, TokensFileName)

	store, err := OpenTokenStore(path)
	if err != nil {
		t.Fatalf("OpenTokenStore: %v", err)
	}
	plaintext, info, err := store.Create("prod", []string{"k7f3qz2a"}, 24*time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	if strings.Contains(string(raw), plaintext) {
		t.Fatalf("token file contains the plaintext secret")
	}
	if !strings.Contains(string(raw), info.ID) || !strings.Contains(string(raw), hashToken(plaintext)) {
		t.Fatalf("token file is missing the id or hash:\n%s", raw)
	}

	reopened, err := OpenTokenStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, ok := reopened.Authenticate(plaintext); !ok {
		t.Fatalf("persisted token no longer authenticates")
	}
	if !reopened.Revoke(info.ID) {
		t.Fatalf("Revoke after reopen failed")
	}

	third, err := OpenTokenStore(path)
	if err != nil {
		t.Fatalf("third open: %v", err)
	}
	if _, ok := third.Authenticate(plaintext); ok {
		t.Fatalf("revocation did not persist")
	}
}

func TestRegistryPersistenceRestoresStale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, RegistryFileName)

	store, err := OpenRegistry(path)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	if err := store.Upsert(record("k7f3qz2a", "node-a/prod", "node-a")); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened, err := OpenRegistry(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := reopened.Get("k7f3qz2a")
	if !ok {
		t.Fatalf("persisted mount is missing after reopen")
	}
	if !got.Stale {
		t.Fatalf("restored mount must be stale")
	}
	if got.MountID != "node-a/prod" || got.NodeID != "node-a" {
		t.Fatalf("restored record lost identity: %+v", got)
	}
}
