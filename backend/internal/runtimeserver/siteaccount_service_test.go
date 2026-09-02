package runtimeserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
	"github.com/wwsheng009/ai-agent-runtime/internal/siteaccount"
)

func TestNormalizeSiteConfidence(t *testing.T) {
	require.Equal(t, siteaccount.ConfidenceHigh, normalizeSiteConfidence("HIGH"))
	require.Equal(t, siteaccount.ConfidenceMedium, normalizeSiteConfidence(" medium "))
	require.Equal(t, siteaccount.ConfidenceLow, normalizeSiteConfidence("low"))
	require.Equal(t, siteaccount.Confidence(""), normalizeSiteConfidence("unknown"))
}

func TestLocalSiteAccountServiceDetect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "version": "1.0.0"})
		case "/api/v1/settings/public":
			_ = json.NewEncoder(w).Encode(map[string]any{"app_name": "sub2api"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := NewLocalSiteAccountService("", "")
	service.SetClient(siteaccount.NewClient(server.Client()))

	result, err := service.Detect(context.Background(), skillsapi.SiteAccountDetectRequest{
		BaseURL: server.URL,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Detect)
	require.Equal(t, siteaccount.SiteTypeSub2API, result.Detect.SiteType)
}

func TestLocalSiteAccountServiceFetchSub2API(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/usage", r.URL.Path)
		require.Equal(t, "Bearer sk-test", r.Header.Get("Authorization"))
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
		})
	}))
	defer server.Close()

	service := NewLocalSiteAccountService("", "")
	service.SetClient(siteaccount.NewClient(server.Client()))

	result, err := service.Fetch(context.Background(), skillsapi.SiteAccountFetchRequest{
		BaseURL:  server.URL,
		SiteType: "sub2api",
		APIKey:   "sk-test",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	require.NotNil(t, result.Account.QuotaRemaining)
	require.Equal(t, 12.34, *result.Account.QuotaRemaining)
	require.NotEmpty(t, result.BalanceLine)
	require.NotNil(t, result.AccountView)
}

func TestLocalSiteAccountServiceRefreshProviderPersists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/usage", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mode": "quota_limited",
			"unit": "USD",
			"quota": map[string]any{
				"limit":     50,
				"used":      10,
				"remaining": 40,
				"unit":      "USD",
			},
			"remaining": 40,
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	authPath := filepath.Join(dir, "auth.json")
	raw := fmt.Sprintf(`providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
      base_url: %s
      api_key: sk-alpha
      default_model: gpt-4.1-mini
`, server.URL)
	require.NoError(t, os.WriteFile(configPath, []byte(raw), 0o644))

	service := NewLocalSiteAccountService(configPath, authPath)
	service.SetClient(siteaccount.NewClient(server.Client()))
	service.now = func() time.Time {
		return time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	}

	result, err := service.RefreshProvider(context.Background(), "alpha", skillsapi.SiteAccountRefreshRequest{
		SiteType:   "sub2api",
		SkipDetect: true,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "alpha", result.Provider)
	require.Equal(t, string(siteaccount.SiteTypeSub2API), result.SiteType)
	require.True(t, result.Persisted)
	require.NotNil(t, result.AccountCache)
	require.NotNil(t, result.AccountCache.QuotaRemaining)
	require.Equal(t, 40.0, *result.AccountCache.QuotaRemaining)

	content, err := os.ReadFile(configPath)
	require.NoError(t, err)
	text := string(content)
	require.Contains(t, text, "site_type: sub2api")
	require.Contains(t, text, "quota_remaining:")
	require.Contains(t, text, "account:")
}

func TestLocalSiteAccountServiceRefreshDeepSeekPersistsBalanceDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/user/balance", r.URL.Path)
		require.Equal(t, "Bearer sk-deepseek", r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"is_available": true,
			"balance_infos": []map[string]any{
				{
					"currency":          "CNY",
					"total_balance":     "110.00",
					"granted_balance":   "10.00",
					"topped_up_balance": "100.00",
				},
			},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	authPath := filepath.Join(dir, "auth.json")
	raw := fmt.Sprintf(`providers:
  default_provider: deepseek
  items:
    deepseek:
      enabled: true
      protocol: openai
      base_url: %s/v1
      api_key: sk-deepseek
      default_model: deepseek-chat
`, server.URL)
	require.NoError(t, os.WriteFile(configPath, []byte(raw), 0o644))

	service := NewLocalSiteAccountService(configPath, authPath)
	service.SetClient(siteaccount.NewClient(server.Client()))
	result, err := service.RefreshProvider(context.Background(), "deepseek", skillsapi.SiteAccountRefreshRequest{
		SiteType:   "deepseek",
		SkipDetect: true,
	})
	require.NoError(t, err)
	require.True(t, result.Persisted)
	require.Equal(t, string(siteaccount.SiteTypeDeepSeek), result.SiteType)
	require.NotNil(t, result.AccountCache)
	require.NotNil(t, result.AccountCache.IsAvailable)
	require.True(t, *result.AccountCache.IsAvailable)
	require.Len(t, result.AccountCache.BalanceDetails, 1)
	require.Equal(t, 10.0, result.AccountCache.BalanceDetails[0].GrantedBalance)
	require.Equal(t, 100.0, result.AccountCache.BalanceDetails[0].ToppedUpBalance)

	content, err := os.ReadFile(configPath)
	require.NoError(t, err)
	text := string(content)
	require.Contains(t, text, "site_type: deepseek")
	require.Contains(t, text, "is_available: true")
	require.Contains(t, text, "balance_details:")
	require.Contains(t, text, "granted_balance:")
	require.Contains(t, text, "topped_up_balance:")
}

func TestLocalSiteAccountServiceRefreshProviderUsesAuthStore(t *testing.T) {
	var sawAuth bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/self":
			sawAuth = true
			require.Equal(t, "Bearer system-token", r.Header.Get("Authorization"))
			require.Equal(t, "99", r.Header.Get("New-Api-User"))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"id":         99,
					"username":   "alice",
					"quota":      1000000,
					"used_quota": 250000,
				},
			})
		case "/api/status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"quota_per_unit":     500000,
					"quota_display_type": "USD",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	authPath := filepath.Join(dir, "auth.json")
	raw := fmt.Sprintf(`providers:
  default_provider: newapi
  items:
    newapi:
      enabled: true
      protocol: openai
      base_url: %s
      api_key: sk-unused
      default_model: gpt-4.1-mini
      site_type: new-api
      account_auth_ref: newapi-account
`, server.URL)
	require.NoError(t, os.WriteFile(configPath, []byte(raw), 0o644))
	require.NoError(t, agentconfig.SaveProviderAuthToPath(authPath, "newapi-account", agentconfig.ProviderAuthRecord{
		KeyType:       agentconfig.AuthKeyTypeNewAPISystemAccessToken,
		AuthMode:      agentconfig.AuthKeyTypeNewAPISystemAccessToken,
		AccessToken:   "system-token",
		SubjectUserID: "99",
	}))

	service := NewLocalSiteAccountService(configPath, authPath)
	service.SetClient(siteaccount.NewClient(server.Client()))

	result, err := service.RefreshProvider(context.Background(), "newapi", skillsapi.SiteAccountRefreshRequest{
		SkipDetect: true,
		SiteType:   "new-api",
	})
	require.NoError(t, err)
	require.True(t, sawAuth)
	require.NotNil(t, result)
	require.Equal(t, "newapi-account", result.AccountAuthRef)
	require.True(t, result.Persisted)
	require.NotNil(t, result.Account)
}

func TestLocalSiteAccountServiceRefreshTriggersProviderReloader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/usage", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mode": "quota_limited",
			"unit": "USD",
			"quota": map[string]any{
				"limit":     50,
				"used":      10,
				"remaining": 40,
				"unit":      "USD",
			},
			"remaining": 40,
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	authPath := filepath.Join(dir, "auth.json")
	raw := fmt.Sprintf(`providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
      base_url: %s
      api_key: sk-alpha
      default_model: gpt-4.1-mini
`, server.URL)
	require.NoError(t, os.WriteFile(configPath, []byte(raw), 0o644))

	service := NewLocalSiteAccountService(configPath, authPath)
	service.SetClient(siteaccount.NewClient(server.Client()))

	var reloaded *agentconfig.Config
	reloadCalls := 0
	service.SetProviderReloader(func(nextCfg *agentconfig.Config) error {
		reloadCalls++
		reloaded = nextCfg
		return nil
	})

	result, err := service.RefreshProvider(context.Background(), "alpha", skillsapi.SiteAccountRefreshRequest{
		SiteType:   "sub2api",
		SkipDetect: true,
	})
	require.NoError(t, err)
	require.True(t, result.Persisted)
	require.Equal(t, 1, reloadCalls, "provider reloader should be invoked once after successful persist")
	require.NotNil(t, reloaded, "reloader should receive the freshly reloaded config")
	item, ok := reloaded.Providers.Items["alpha"]
	require.True(t, ok, "reloaded config should still contain the provider")
	require.Equal(t, "sub2api", item.SiteType)
	require.NotNil(t, item.Account)
}
