package device

import (
	"path/filepath"
	"testing"
)

func TestPersistentStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	token, info, err := store.Create("laptop")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, ok := reopened.Authenticate(token); !ok {
		t.Fatal("issued token must survive a reopen")
	}
	if len(reopened.List()) != 1 {
		t.Fatalf("expected 1 token after reopen, got %d", len(reopened.List()))
	}

	if !reopened.Revoke(info.ID) {
		t.Fatal("revoke must report the id existed")
	}
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after revoke: %v", err)
	}
	if _, ok := again.Authenticate(token); ok {
		t.Fatal("revocation must survive a reopen")
	}
}

func TestPersistentStoreOpenMissingFile(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "nested", "devices.json"))
	if err != nil {
		t.Fatalf("open missing: %v", err)
	}
	if store.Count() != 0 {
		t.Fatalf("expected empty store, got %d", store.Count())
	}
}
