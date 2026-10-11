package device

import (
	"strings"
	"testing"
	"time"
)

func TestCreateReturnsOnceAndAuthenticates(t *testing.T) {
	store := NewStore()
	token, info, err := store.Create("laptop")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(token, TokenPrefix) {
		t.Fatalf("token %q missing prefix %q", token, TokenPrefix)
	}
	if info.Prefix == "" || strings.Contains(info.Prefix, token[8:]) {
		t.Fatalf("prefix must be short, got %q", info.Prefix)
	}
	if _, ok := store.Authenticate(token); !ok {
		t.Fatal("issued token must authenticate")
	}
	if _, ok := store.Authenticate("agw_wrong"); ok {
		t.Fatal("unknown token must not authenticate")
	}
	if _, ok := store.Authenticate(""); ok {
		t.Fatal("empty token must not authenticate")
	}
}

func TestRevokeBlocksAuth(t *testing.T) {
	store := NewStore()
	token, info, _ := store.Create("build-server")
	if !store.Revoke(info.ID) {
		t.Fatal("Revoke must report the id existed")
	}
	if _, ok := store.Authenticate(token); ok {
		t.Fatal("revoked token must not authenticate")
	}
	if store.Revoke("missing") {
		t.Fatal("Revoke of an unknown id must return false")
	}
}

func TestListHidesSecretAndSorts(t *testing.T) {
	store := NewStore()
	tokenA, _, _ := store.Create("a")
	_, _, _ = store.Create("b")
	list := store.List()
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	if list[0].ID > list[1].ID {
		t.Fatalf("list not sorted by id: %+v", list)
	}
	for _, item := range list {
		if strings.Contains(item.Prefix, tokenA[8:]) {
			t.Fatalf("list leaked token material: %+v", item)
		}
	}
	if store.Count() != 2 {
		t.Fatalf("count = %d, want 2", store.Count())
	}
}

func TestLastUsedUpdated(t *testing.T) {
	store := NewStore()
	base := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return base }
	token, _, _ := store.Create("x")

	store.now = func() time.Time { return base.Add(time.Minute) }
	info, ok := store.Authenticate(token)
	if !ok {
		t.Fatal("authenticate failed")
	}
	if !info.LastUsed.Equal(base.Add(time.Minute)) {
		t.Fatalf("last used = %v, want %v", info.LastUsed, base.Add(time.Minute))
	}
}
