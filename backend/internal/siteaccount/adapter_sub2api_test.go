package siteaccount

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchSub2APIUsage_QuotaLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("unexpected auth: %q", got)
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
	}))
	defer server.Close()

	snapshot, err := NewClient(server.Client()).FetchAccountSnapshot(context.Background(), FetchInput{
		BaseURL:  server.URL,
		SiteType: SiteTypeSub2API,
		Credential: AccountCredential{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("FetchAccountSnapshot: %v", err)
	}
	if snapshot.Source != "v1_usage" || snapshot.Mode != "quota_limited" {
		t.Fatalf("unexpected snapshot meta: %+v", snapshot)
	}
	if snapshot.QuotaRemaining == nil || *snapshot.QuotaRemaining != 12.34 {
		t.Fatalf("unexpected remaining: %+v", snapshot.QuotaRemaining)
	}
	if snapshot.QuotaLimit == nil || *snapshot.QuotaLimit != 20 {
		t.Fatalf("unexpected limit: %+v", snapshot.QuotaLimit)
	}
	if snapshot.Usage == nil || snapshot.Usage.TotalRequests == nil || *snapshot.Usage.TotalRequests != 128 {
		t.Fatalf("unexpected usage: %+v", snapshot.Usage)
	}

	view := NormalizeAccountView(snapshot, ConfidenceHigh)
	if view.BalanceValue == nil || *view.BalanceValue != 12.34 {
		t.Fatalf("unexpected view balance: %+v", view)
	}
}

func TestFetchSub2APIUsage_UnrestrictedWallet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mode":      "unrestricted",
			"planName":  "钱包余额",
			"unit":      "USD",
			"remaining": 5.5,
			"balance":   5.5,
		})
	}))
	defer server.Close()

	snapshot, err := NewClient(server.Client()).FetchAccountSnapshot(context.Background(), FetchInput{
		BaseURL:    server.URL + "/v1",
		SiteType:   SiteTypeSub2API,
		Credential: AccountCredential{APIKey: "sk-wallet"},
	})
	if err != nil {
		t.Fatalf("FetchAccountSnapshot: %v", err)
	}
	if snapshot.WalletBalance == nil || *snapshot.WalletBalance != 5.5 {
		t.Fatalf("unexpected wallet: %+v", snapshot.WalletBalance)
	}
	if snapshot.PlanName != "钱包余额" {
		t.Fatalf("unexpected plan: %q", snapshot.PlanName)
	}
}

func TestFetchSub2APIUsage_UnrestrictedSubscription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mode":     "unrestricted",
			"planName": "pro",
			"unit":     "USD",
			"remaining": 3.2,
			"subscription": map[string]any{
				"daily_limit_usd":   10,
				"daily_usage_usd":   6.8,
				"weekly_limit_usd":  50,
				"weekly_usage_usd":  20,
				"monthly_limit_usd": 100,
				"monthly_usage_usd": 40,
			},
		})
	}))
	defer server.Close()

	snapshot, err := NewClient(server.Client()).FetchAccountSnapshot(context.Background(), FetchInput{
		BaseURL:    server.URL,
		SiteType:   SiteTypeSub2API,
		Credential: AccountCredential{APIKey: "sk-sub"},
	})
	if err != nil {
		t.Fatalf("FetchAccountSnapshot: %v", err)
	}
	if len(snapshot.Subscriptions) != 1 {
		t.Fatalf("expected 1 subscription, got %+v", snapshot.Subscriptions)
	}
	sub := snapshot.Subscriptions[0]
	if sub.Name != "pro" || sub.Remaining == nil || *sub.Remaining != 3.2 {
		t.Fatalf("unexpected subscription: %+v", sub)
	}
}

func TestFetchSub2APIUsage_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"nope"}`))
	}))
	defer server.Close()

	_, err := NewClient(server.Client()).FetchAccountSnapshot(context.Background(), FetchInput{
		BaseURL:    server.URL,
		SiteType:   SiteTypeSub2API,
		Credential: AccountCredential{APIKey: "bad"},
	})
	if err == nil {
		t.Fatal("expected unauthorized error")
	}
}
