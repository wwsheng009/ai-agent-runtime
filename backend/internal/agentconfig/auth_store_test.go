package agentconfig

import (
	"path/filepath"
	"testing"
)

func TestProviderAuthStore_SaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	record := ProviderAuthRecord{
		AuthMode:     "oauth",
		Issuer:       "https://auth.example.com",
		ClientID:     "client",
		AccessToken:  "access",
		RefreshToken: "refresh",
		IDToken:      "id",
	}
	if err := SaveProviderAuthToPath(path, "codex", record); err != nil {
		t.Fatalf("SaveProviderAuthToPath: %v", err)
	}
	loaded, err := LoadProviderAuthFromPath(path, "codex")
	if err != nil {
		t.Fatalf("LoadProviderAuthFromPath: %v", err)
	}
	if loaded.AccessToken != "access" || loaded.RefreshToken != "refresh" || loaded.AuthMode != "oauth" {
		t.Fatalf("unexpected loaded record: %+v", loaded)
	}
}
