package siteaccount

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchNewAPIUserSelf_ConvertsQuotaWithStatusConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"quota_per_unit":     500000,
					"quota_display_type": "USD",
				},
			})
		case "/api/user/self":
			if got := r.Header.Get("Authorization"); got != "Bearer sys-token" {
				t.Fatalf("unexpected Authorization: %q", got)
			}
			if got := r.Header.Get("New-Api-User"); got != "42" {
				t.Fatalf("unexpected New-Api-User: %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"id":            42,
					"username":      "alice",
					"email":         "alice@example.com",
					"group":         "default",
					"quota":         1_000_000,
					"used_quota":    500000,
					"request_count": 9,
				},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	snapshot, err := NewClient(server.Client()).FetchAccountSnapshot(context.Background(), FetchInput{
		BaseURL:  server.URL + "/v1",
		SiteType: SiteTypeNewAPI,
		Credential: AccountCredential{
			SystemAccessToken: "sys-token",
			SubjectUserID:     42,
		},
	})
	if err != nil {
		t.Fatalf("FetchAccountSnapshot: %v", err)
	}
	if snapshot.Source != newAPIUserSelfSource || snapshot.SiteType != SiteTypeNewAPI {
		t.Fatalf("unexpected meta: %+v", snapshot)
	}
	if snapshot.QuotaRemaining == nil || *snapshot.QuotaRemaining != 2.0 {
		t.Fatalf("unexpected remaining display: %+v", snapshot.QuotaRemaining)
	}
	if snapshot.UsedQuota == nil || *snapshot.UsedQuota != 1.0 {
		t.Fatalf("unexpected used display: %+v", snapshot.UsedQuota)
	}
	if snapshot.QuotaLimit == nil || *snapshot.QuotaLimit != 3.0 {
		t.Fatalf("unexpected limit display: %+v", snapshot.QuotaLimit)
	}
	if snapshot.QuotaBalanceRaw == nil || *snapshot.QuotaBalanceRaw != 1_000_000 {
		t.Fatalf("unexpected raw quota: %+v", snapshot.QuotaBalanceRaw)
	}
	if snapshot.ExternalUser == nil || snapshot.ExternalUser.ID != "42" || snapshot.ExternalUser.Username != "alice" {
		t.Fatalf("unexpected external user: %+v", snapshot.ExternalUser)
	}
	if snapshot.Usage == nil || snapshot.Usage.TotalRequests == nil || *snapshot.Usage.TotalRequests != 9 {
		t.Fatalf("unexpected usage: %+v", snapshot.Usage)
	}
	view := NormalizeAccountView(snapshot, ConfidenceHigh)
	if view.BalanceValue == nil || *view.BalanceValue != 2.0 {
		t.Fatalf("unexpected view balance: %+v", view)
	}
}

func TestFetchNewAPIUserSelf_MissingCredential(t *testing.T) {
	_, err := NewClient(nil).FetchAccountSnapshot(context.Background(), FetchInput{
		BaseURL:  "https://example.com",
		SiteType: SiteTypeNewAPI,
	})
	if err == nil {
		t.Fatal("expected missing credential error")
	}
	if !IsMissingCredential(err) {
		t.Fatalf("expected missing credential classification, got %v", err)
	}
}

func TestFetchNewAPIUserSelf_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
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

	_, err := NewClient(server.Client()).FetchAccountSnapshot(context.Background(), FetchInput{
		BaseURL:  server.URL,
		SiteType: SiteTypeNewAPI,
		Credential: AccountCredential{
			SystemAccessToken: "bad",
			SubjectUserID:     1,
		},
	})
	if err == nil {
		t.Fatal("expected unauthorized")
	}
	if !IsUnauthorized(err) {
		t.Fatalf("expected unauthorized classification, got %v", err)
	}
}
