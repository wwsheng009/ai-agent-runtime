package siteaccount

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDetectSiteType_Sub2API(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/api/v1/settings/public":
			enabled := true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"site_name":            "Demo Sub2",
					"registration_enabled": enabled,
					"payment_enabled":      enabled,
				},
			})
		case "/api/v1/auth/me":
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "unauthorized"})
		case "/health":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	result, err := NewClient(server.Client()).DetectSiteType(context.Background(), DetectInput{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("DetectSiteType: %v", err)
	}
	if result.SiteType != SiteTypeSub2API {
		t.Fatalf("site type = %q, want sub2api; scores=%v hits=%v", result.SiteType, result.Score, result.Hits)
	}
	if result.Confidence == ConfidenceLow {
		t.Fatalf("expected medium/high confidence, got %q scores=%v", result.Confidence, result.Score)
	}
}

func TestDetectSiteType_NewAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			w.Header().Set("X-New-Api-Version", "v0.0-test")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"version":           "v0.0-test",
					"system_name":       "New API",
					"quota_per_unit":    500000,
					"quota_display_type": "USD",
				},
			})
		case "/api/user/self", "/api/user/self/groups":
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "message": "auth"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	result, err := NewClient(server.Client()).DetectSiteType(context.Background(), DetectInput{BaseURL: server.URL + "/v1"})
	if err != nil {
		t.Fatalf("DetectSiteType: %v", err)
	}
	if result.SiteType != SiteTypeNewAPI {
		t.Fatalf("site type = %q, want new-api; scores=%v", result.SiteType, result.Score)
	}
	if result.Confidence != ConfidenceHigh {
		t.Fatalf("confidence = %q, want high; scores=%v", result.Confidence, result.Score)
	}
}

func TestDetectSiteType_Unknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("missing"))
	}))
	defer server.Close()

	result, err := NewClient(server.Client()).DetectSiteType(context.Background(), DetectInput{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("DetectSiteType: %v", err)
	}
	if result.SiteType != SiteTypeUnknown {
		t.Fatalf("site type = %q, want unknown", result.SiteType)
	}
}

func TestParseSiteTypeFlag(t *testing.T) {
	siteType, auto, err := ParseSiteTypeFlag("auto")
	if err != nil || !auto || siteType != SiteTypeUnknown {
		t.Fatalf("auto: type=%q auto=%v err=%v", siteType, auto, err)
	}
	siteType, auto, err = ParseSiteTypeFlag("sub2api")
	if err != nil || auto || siteType != SiteTypeSub2API {
		t.Fatalf("sub2api: type=%q auto=%v err=%v", siteType, auto, err)
	}
}
