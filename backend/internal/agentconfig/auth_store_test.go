package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderAuthStore_SaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	record := ProviderAuthRecord{
		KeyType:      AuthKeyTypeOAuth,
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
	if loaded.KeyType != AuthKeyTypeOAuth || loaded.AccessToken != "access" || loaded.RefreshToken != "refresh" || loaded.AuthMode != "oauth" {
		t.Fatalf("unexpected loaded record: %+v", loaded)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read auth store: %v", err)
	}
	text := string(content)
	for _, expected := range []string{
		`"key_type": "oauth"`,
		`"access_token": "access"`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in auth store:\n%s", expected, text)
		}
	}
}

func TestProviderAuthStore_SaveAndLoadAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	record := ProviderAuthRecord{
		KeyType:  AuthKeyTypeAPIKey,
		AuthMode: AuthKeyTypeAPIKey,
		APIKey:   "sk-test",
	}
	if err := SaveProviderAuthToPath(path, "openai", record); err != nil {
		t.Fatalf("SaveProviderAuthToPath: %v", err)
	}
	loaded, err := LoadProviderAuthFromPath(path, "openai")
	if err != nil {
		t.Fatalf("LoadProviderAuthFromPath: %v", err)
	}
	if loaded.KeyType != AuthKeyTypeAPIKey || loaded.APIKey != "sk-test" || loaded.AccessToken != "" {
		t.Fatalf("unexpected loaded record: %+v", loaded)
	}
	secret, err := LoadProviderAuthSecretFromPath(path, "openai", AuthKeyTypeAPIKey)
	if err != nil {
		t.Fatalf("LoadProviderAuthSecretFromPath: %v", err)
	}
	if secret != "sk-test" {
		t.Fatalf("unexpected secret: %q", secret)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read auth store: %v", err)
	}
	text := string(content)
	for _, expected := range []string{
		`"key_type": "api_key"`,
		`"api_key": "sk-test"`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in auth store:\n%s", expected, text)
		}
	}
}

func TestProviderAuthStore_ExpandsEnvVarsOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("AICLI_TEST_AUTH_API_KEY", "sk-from-env")
	t.Setenv("AICLI_TEST_AUTH_ACCESS_TOKEN", "access-from-env")
	t.Setenv("AICLI_TEST_AUTH_REFRESH_TOKEN", "refresh-from-env")

	raw := `{
  "version": 1,
  "providers": {
    "openai": {
      "key_type": "api_key",
      "auth_mode": "api_key",
      "api_key": "${AICLI_TEST_AUTH_API_KEY}"
    },
    "codex": {
      "key_type": "oauth",
      "auth_mode": "oauth",
      "access_token": "${AICLI_TEST_AUTH_ACCESS_TOKEN}",
      "refresh_token": "${AICLI_TEST_AUTH_REFRESH_TOKEN}",
      "id_token": "${AICLI_TEST_AUTH_ID_TOKEN:-id-default}"
    }
  }
}
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write auth store: %v", err)
	}

	apiKey, err := LoadProviderAuthSecretFromPath(path, "openai", AuthKeyTypeAPIKey)
	if err != nil {
		t.Fatalf("LoadProviderAuthSecretFromPath: %v", err)
	}
	if apiKey != "sk-from-env" {
		t.Fatalf("unexpected expanded api key: %q", apiKey)
	}

	oauth, err := LoadProviderAuthFromPath(path, "codex")
	if err != nil {
		t.Fatalf("LoadProviderAuthFromPath: %v", err)
	}
	if oauth.AccessToken != "access-from-env" || oauth.RefreshToken != "refresh-from-env" || oauth.IDToken != "id-default" {
		t.Fatalf("unexpected expanded oauth record: %+v", oauth)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read auth store: %v", err)
	}
	if !strings.Contains(string(content), `"api_key": "${AICLI_TEST_AUTH_API_KEY}"`) {
		t.Fatalf("auth store should keep original env reference:\n%s", string(content))
	}
}
