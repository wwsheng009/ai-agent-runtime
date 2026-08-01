package siteaccount

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchDeepSeekBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/balance" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-deepseek" {
			t.Fatalf("unexpected authorization: %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"is_available": true,
			"balance_infos": []map[string]any{
				{"currency": "CNY", "total_balance": "110.00", "granted_balance": "10.00", "topped_up_balance": "100.00"},
				{"currency": "USD", "total_balance": "2.50", "granted_balance": "0.50", "topped_up_balance": "2.00"},
			},
		})
	}))
	defer server.Close()

	snapshot, err := NewClient(server.Client()).FetchAccountSnapshot(context.Background(), FetchInput{
		BaseURL: server.URL + "/v1", SiteType: SiteTypeDeepSeek,
		Credential: AccountCredential{APIKey: "sk-deepseek"},
	})
	if err != nil {
		t.Fatalf("FetchAccountSnapshot: %v", err)
	}
	if snapshot.SiteType != SiteTypeDeepSeek || snapshot.Source != deepSeekBalanceSource || snapshot.Mode != "wallet" {
		t.Fatalf("unexpected metadata: %+v", snapshot)
	}
	if snapshot.WalletBalance == nil || *snapshot.WalletBalance != 110 || snapshot.Currency != "CNY" {
		t.Fatalf("unexpected primary balance: %+v", snapshot)
	}
	if snapshot.IsAvailable == nil || !*snapshot.IsAvailable || len(snapshot.BalanceDetails) != 2 {
		t.Fatalf("unexpected details: %+v", snapshot)
	}
	if got := snapshot.BalanceDetails[0]; got.GrantedBalance != 10 || got.ToppedUpBalance != 100 {
		t.Fatalf("unexpected breakdown: %+v", got)
	}
	view := NormalizeAccountView(snapshot, ConfidenceHigh)
	if view.BalanceValue == nil || *view.BalanceValue != 110 || len(view.BalanceDetails) != 2 {
		t.Fatalf("unexpected normalized view: %+v", view)
	}
}

func TestFetchDeepSeekBalanceRejectsInvalidAmount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"NaN","granted_balance":"0","topped_up_balance":"0"}]}`))
	}))
	defer server.Close()
	_, err := NewClient(server.Client()).FetchAccountSnapshot(context.Background(), FetchInput{
		BaseURL: server.URL, SiteType: SiteTypeDeepSeek, Credential: AccountCredential{APIKey: "bad-payload"},
	})
	if err == nil {
		t.Fatal("expected invalid amount error")
	}
}

func TestFetchDeepSeekBalanceUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	_, err := NewClient(server.Client()).FetchAccountSnapshot(context.Background(), FetchInput{
		BaseURL: server.URL, SiteType: SiteTypeDeepSeek, Credential: AccountCredential{APIKey: "bad"},
	})
	if !IsUnauthorized(err) {
		t.Fatalf("expected unauthorized error, got %v", err)
	}
}

func TestDefaultAdapterRegistryIncludesDeepSeek(t *testing.T) {
	registry := DefaultAdapterRegistry()
	adapter, ok := registry.Adapter(SiteTypeDeepSeek)
	if !ok || adapter.SiteType() != SiteTypeDeepSeek {
		t.Fatalf("DeepSeek adapter is not registered")
	}
	if !registry.SupportsFetch(SiteTypeDeepSeek) || !SupportsAccountFetch(SiteTypeDeepSeek) {
		t.Fatal("DeepSeek adapter should support account fetch")
	}
	if got := registry.SiteTypeForAccountSource(deepSeekBalanceSource); got != SiteTypeDeepSeek {
		t.Fatalf("site type from source = %q, want %q", got, SiteTypeDeepSeek)
	}
	if got := SiteTypeFromAccountSource(deepSeekBalanceSource); got != SiteTypeDeepSeek {
		t.Fatalf("built-in site type from source = %q, want %q", got, SiteTypeDeepSeek)
	}
}
