package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestRunProviderLogin_Sub2APIAccountSyncSuccess(t *testing.T) {
	var sawUsage bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/models":
			if got := r.Header.Get("Authorization"); got != "Bearer sk-sub2" {
				t.Fatalf("unexpected models Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1-mini"}]}`))
		case r.URL.Path == "/v1/usage":
			sawUsage = true
			if got := r.Header.Get("Authorization"); got != "Bearer sk-sub2" {
				t.Fatalf("unexpected usage Authorization: %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"mode": "quota_limited",
				"unit": "USD",
				"quota": map[string]any{
					"limit":     20,
					"used":      7.66,
					"remaining": 12.34,
					"unit":      "USD",
				},
				"remaining": 12.34,
				"usage": map[string]any{
					"today": map[string]any{"requests": 3, "cost": 1.1},
					"total": map[string]any{"requests": 128, "cost": 7.66},
				},
			})
		default:
			// Detect probes or unrelated paths.
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	result, err := runProviderLogin(providerLoginRequest{
		Config:            cfg,
		ProviderName:      "sub2",
		LoginProtocol:     "openai",
		BaseURL:           server.URL,
		APIKey:            "sk-sub2",
		SiteType:          "sub2api",
		SkipSiteDetect:    true,
		DisableModelCards: true,
		Yes:               true,
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if !sawUsage {
		t.Fatal("expected /v1/usage to be called")
	}
	if result.SiteType != "sub2api" {
		t.Fatalf("expected site_type sub2api, got %q", result.SiteType)
	}
	if result.Account == nil || result.Account.QuotaRemaining == nil || *result.Account.QuotaRemaining != 12.34 {
		t.Fatalf("unexpected account snapshot: %+v", result.Account)
	}
	if result.BalanceLine == "" || !strings.Contains(result.BalanceLine, "12.34") {
		t.Fatalf("expected balance line with 12.34, got %q", result.BalanceLine)
	}
	provider := cfg.Providers.Items["sub2"]
	if provider.SiteType != "sub2api" {
		t.Fatalf("expected cfg site_type sub2api, got %+v", provider)
	}
	if provider.Account == nil || provider.Account.Source != "v1_usage" {
		t.Fatalf("expected cfg account source v1_usage, got %+v", provider.Account)
	}
	if provider.Account.QuotaRemaining == nil || *provider.Account.QuotaRemaining != 12.34 {
		t.Fatalf("unexpected cfg account remaining: %+v", provider.Account)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	for _, expected := range []string{
		"site_type: sub2api",
		"source: v1_usage",
		"quota_remaining:",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in config:\n%s", expected, text)
		}
	}
}

func TestRunProviderLogin_Sub2APIAccountSyncBestEffortOnUsageFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1-mini"}]}`))
		case "/v1/usage":
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	result, err := runProviderLogin(providerLoginRequest{
		Config:            cfg,
		ProviderName:      "sub2_soft",
		LoginProtocol:     "openai",
		BaseURL:           server.URL,
		APIKey:            "sk-sub2",
		SiteType:          "sub2api",
		SkipSiteDetect:    true,
		DisableModelCards: true,
		Yes:               true,
	})
	if err != nil {
		t.Fatalf("runProviderLogin should succeed best-effort, got: %v", err)
	}
	if result.Account != nil {
		t.Fatalf("expected no account on usage failure, got %+v", result.Account)
	}
	if result.SiteType != "sub2api" {
		t.Fatalf("expected site type still set, got %q", result.SiteType)
	}
	joined := strings.Join(result.SiteAccountWarnings, "\n")
	if !strings.Contains(joined, "account sync failed") {
		t.Fatalf("expected account sync warning, got %q", joined)
	}
	provider := cfg.Providers.Items["sub2_soft"]
	if provider.SiteType != "sub2api" {
		t.Fatalf("expected site_type persisted, got %+v", provider)
	}
	if provider.Account != nil {
		t.Fatalf("expected no account persisted on soft failure, got %+v", provider.Account)
	}
}

func TestRunProviderLogin_Sub2APIAccountSyncRequireAccountFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1-mini"}]}`))
		case "/v1/usage":
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	_, err := runProviderLogin(providerLoginRequest{
		Config:            cfg,
		ProviderName:      "sub2_strict",
		LoginProtocol:     "openai",
		BaseURL:           server.URL,
		APIKey:            "sk-sub2",
		SiteType:          "sub2api",
		SkipSiteDetect:    true,
		RequireAccount:    true,
		DisableModelCards: true,
		Yes:               true,
	})
	if err == nil {
		t.Fatal("expected require-account failure")
	}
	if !strings.Contains(err.Error(), "account sync required") {
		t.Fatalf("unexpected error: %v", err)
	}
	content, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read config: %v", errRead)
	}
	if strings.Contains(string(content), "sub2_strict") {
		t.Fatalf("config should not persist provider on require-account failure:\n%s", content)
	}
}

func TestRunProviderLogin_DetectsSub2APIFromStatusAndSyncsAccount(t *testing.T) {
	var sawUsage bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet"}]}`))
		case "/api/v1/status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "1.2.3",
				"status":  "ok",
			})
		case "/api/v1/settings/public":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"app_name": "Sub2API",
			})
		case "/v1/usage":
			sawUsage = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"mode":      "unrestricted",
				"planName":  "wallet",
				"unit":      "USD",
				"remaining": 5.5,
				"balance":   5.5,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	result, err := runProviderLogin(providerLoginRequest{
		Config:            cfg,
		ProviderName:      "sub2_auto",
		LoginProtocol:     "openai",
		BaseURL:           server.URL,
		APIKey:            "sk-auto",
		SiteType:          "auto",
		DisableModelCards: true,
		Yes:               true,
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if result.SiteType != "sub2api" {
		t.Fatalf("expected auto-detect sub2api, got %q (warnings=%v)", result.SiteType, result.SiteAccountWarnings)
	}
	if !sawUsage {
		t.Fatal("expected /v1/usage after detect")
	}
	if result.Account == nil || result.Account.WalletBalance == nil || *result.Account.WalletBalance != 5.5 {
		t.Fatalf("unexpected account: %+v", result.Account)
	}
	if !strings.Contains(result.BalanceLine, "5.5") {
		t.Fatalf("expected balance line with 5.5, got %q", result.BalanceLine)
	}
}

func TestRunProviderLogin_NewAPIAccountSyncSuccess(t *testing.T) {
	var sawSelf bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			if got := r.Header.Get("Authorization"); got != "Bearer sk-newapi" {
				t.Fatalf("unexpected models Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1-mini"}]}`))
		case "/api/status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"quota_per_unit":     500000,
					"quota_display_type": "USD",
				},
			})
		case "/api/user/self":
			sawSelf = true
			if got := r.Header.Get("Authorization"); got != "Bearer sys-token" {
				t.Fatalf("unexpected self Authorization: %q", got)
			}
			if got := r.Header.Get("New-Api-User"); got != "42" {
				t.Fatalf("unexpected New-Api-User: %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"id":         42,
					"username":   "alice",
					"quota":      1_000_000,
					"used_quota": 500000,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	result, err := runProviderLogin(providerLoginRequest{
		Config:            cfg,
		ProviderName:      "newapi",
		LoginProtocol:     "openai",
		BaseURL:           server.URL + "/v1",
		APIKey:            "sk-newapi",
		SiteType:          "new-api",
		SkipSiteDetect:    true,
		NewAPIAccessToken: "sys-token",
		NewAPIUserID:      "42",
		DisableModelCards: true,
		Yes:               true,
	})
	if err != nil {
		t.Fatalf("runProviderLogin: %v", err)
	}
	if !sawSelf {
		t.Fatal("expected /api/user/self to be called")
	}
	if result.SiteType != "new-api" {
		t.Fatalf("expected site_type new-api, got %q", result.SiteType)
	}
	if result.Account == nil || result.Account.Source != "newapi_user_self" {
		t.Fatalf("unexpected account: %+v", result.Account)
	}
	if result.Account.QuotaRemaining == nil || *result.Account.QuotaRemaining != 2.0 {
		t.Fatalf("unexpected remaining: %+v", result.Account.QuotaRemaining)
	}
	if result.AccountAuthRef != "newapi-account" {
		t.Fatalf("expected account_auth_ref newapi-account, got %q", result.AccountAuthRef)
	}
	if result.BalanceLine == "" || !strings.Contains(result.BalanceLine, "2") {
		t.Fatalf("expected balance line with converted quota, got %q", result.BalanceLine)
	}

	provider := cfg.Providers.Items["newapi"]
	if provider.SiteType != "new-api" {
		t.Fatalf("expected cfg site_type new-api, got %+v", provider)
	}
	if provider.AccountAuthRef != "newapi-account" {
		t.Fatalf("expected cfg account_auth_ref, got %+v", provider)
	}
	if provider.Account == nil || provider.Account.QuotaRemaining == nil || *provider.Account.QuotaRemaining != 2.0 {
		t.Fatalf("unexpected cfg account: %+v", provider.Account)
	}

	authStorePath := testAuthStorePath(dir)
	loadedAuth, err := config.LoadProviderAuthFromPath(authStorePath, "newapi-account")
	if err != nil {
		t.Fatalf("LoadProviderAuthFromPath: %v", err)
	}
	if loadedAuth.KeyType != config.AuthKeyTypeNewAPISystemAccessToken {
		t.Fatalf("unexpected auth key type: %+v", loadedAuth)
	}
	if loadedAuth.AccessToken != "sys-token" || loadedAuth.SubjectUserID != "42" {
		t.Fatalf("unexpected account auth record: %+v", loadedAuth)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	for _, expected := range []string{
		"site_type: new-api",
		"account_auth_ref: newapi-account",
		"source: newapi_user_self",
		"quota_remaining:",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in config:\n%s", expected, text)
		}
	}
	for _, forbidden := range []string{"sys-token", "access_token:", "newapi_system_access_token"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("config must not contain secret %q:\n%s", forbidden, text)
		}
	}
}

func TestRunProviderLogin_NewAPIAccountSyncSkippedWithoutToken(t *testing.T) {
	var sawSelf bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1-mini"}]}`))
		case "/api/user/self":
			sawSelf = true
			http.Error(w, "should not be called", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	result, err := runProviderLogin(providerLoginRequest{
		Config:            cfg,
		ProviderName:      "newapi_soft",
		LoginProtocol:     "openai",
		BaseURL:           server.URL,
		APIKey:            "sk-newapi",
		SiteType:          "new-api",
		SkipSiteDetect:    true,
		DisableModelCards: true,
		Yes:               true,
	})
	if err != nil {
		t.Fatalf("runProviderLogin should succeed without token, got: %v", err)
	}
	if sawSelf {
		t.Fatal("expected /api/user/self not to be called without token")
	}
	if result.SiteType != "new-api" {
		t.Fatalf("expected site_type new-api, got %q", result.SiteType)
	}
	if result.Account != nil {
		t.Fatalf("expected no account without token, got %+v", result.Account)
	}
	joined := strings.Join(result.SiteAccountWarnings, "\n")
	if !strings.Contains(joined, "account sync skipped") {
		t.Fatalf("expected skip warning, got %q", joined)
	}
	provider := cfg.Providers.Items["newapi_soft"]
	if provider.SiteType != "new-api" {
		t.Fatalf("expected site_type persisted, got %+v", provider)
	}
	if provider.Account != nil || provider.AccountAuthRef != "" {
		t.Fatalf("expected no account auth without token, got %+v", provider)
	}
}

func TestRunProviderLogin_NewAPIAccountSyncRequireAccountFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1-mini"}]}`))
		case "/api/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{}})
		case "/api/user/self":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"success":false,"message":"auth"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{ConfigFilePath: path}

	_, err := runProviderLogin(providerLoginRequest{
		Config:            cfg,
		ProviderName:      "newapi_strict",
		LoginProtocol:     "openai",
		BaseURL:           server.URL,
		APIKey:            "sk-newapi",
		SiteType:          "new-api",
		SkipSiteDetect:    true,
		RequireAccount:    true,
		NewAPIAccessToken: "bad-token",
		NewAPIUserID:      "7",
		DisableModelCards: true,
		Yes:               true,
	})
	if err == nil {
		t.Fatal("expected require-account failure")
	}
	if !strings.Contains(err.Error(), "account sync required") {
		t.Fatalf("unexpected error: %v", err)
	}
	content, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read config: %v", errRead)
	}
	if strings.Contains(string(content), "newapi_strict") {
		t.Fatalf("config should not persist provider on require-account failure:\n%s", content)
	}
	if _, loadErr := config.LoadProviderAuthFromPath(testAuthStorePath(dir), "newapi_strict-account"); loadErr == nil {
		t.Fatal("account auth must not be written on require-account failure")
	}
}
