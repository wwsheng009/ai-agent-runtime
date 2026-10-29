package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/mcpmount"
)

// A token issued for mount B must not be revocable through mount A's URL: the
// path is the authorization scope, not a label.
func TestMountTokenDeleteRejectsCrossMount(t *testing.T) {
	store, err := mcpmount.OpenTokenStore(filepath.Join(t.TempDir(), "mount-tokens.json"))
	if err != nil {
		t.Fatalf("OpenTokenStore: %v", err)
	}
	_, info, err := store.Create("other", []string{"bbbbbbbb"}, time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	manager := &mountManager{tokens: store}
	router := mux.NewRouter()
	router.Handle("/api/mounts/{slug}/tokens/{id}", http.HandlerFunc(manager.handleTokenDelete)).
		Methods(http.MethodDelete)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(
		http.MethodDelete, "/api/mounts/aaaaaaaa/tokens/"+info.ID, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-mount delete = %d, want 404", rec.Code)
	}
	if got, ok := store.Get(info.ID); !ok || got.Revoked {
		t.Fatalf("cross-mount delete revoked the token: %+v, %v", got, ok)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(
		http.MethodDelete, "/api/mounts/bbbbbbbb/tokens/"+info.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("owning-mount delete = %d, want 200", rec.Code)
	}
	if got, ok := store.Get(info.ID); !ok || !got.Revoked {
		t.Fatalf("owning-mount delete did not revoke: %+v, %v", got, ok)
	}
}

func TestMountRecentLogsRingFiltersAndLimits(t *testing.T) {
	manager := &mountManager{reqLogMax: 2}
	manager.observe(mcpmount.Event{
		Slug: "aaaaaaaa", Method: "POST", Path: "/mcp", Status: 200,
		Duration: 5 * time.Millisecond, RemoteAddr: "127.0.0.1:1111",
	})
	manager.observe(mcpmount.Event{
		Slug: "bbbbbbbb", Method: "GET", Path: "/mcp", Status: 401,
		Reason: "token_missing", RemoteAddr: "127.0.0.1:2222",
	})
	manager.observe(mcpmount.Event{
		Slug: "aaaaaaaa", Method: "POST", Path: "/mcp", Status: 200,
		Duration: 7 * time.Millisecond, RemoteAddr: "127.0.0.1:3333",
	})

	entries := manager.recentLogs("", 10)
	if len(entries) != 2 {
		t.Fatalf("ring kept %d entries, want 2", len(entries))
	}
	if entries[0].Slug != "aaaaaaaa" || entries[1].Slug != "bbbbbbbb" {
		t.Fatalf("entries not newest first: %+v", entries)
	}
	if entries[0].RemoteAddr != "127.0.0.1" {
		t.Fatalf("RemoteAddr = %q, want host only", entries[0].RemoteAddr)
	}

	rec := httptest.NewRecorder()
	manager.handleLogs(rec, httptest.NewRequest(
		http.MethodGet, "/api/mounts/logs?slug=aaaaaaaa&limit=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("handleLogs = %d, want 200", rec.Code)
	}
	var body struct {
		Entries []mountLogEntry `json:"entries"`
		Count   int             `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Count != 1 || len(body.Entries) != 1 || body.Entries[0].Slug != "aaaaaaaa" {
		t.Fatalf("filtered logs = %+v", body)
	}
}

// TestMountLogEntrySerializesNewFields proves the console call log (the
// in-memory ring surfaced through /api/mounts/logs) carries the MCP command,
// caller User-Agent and request/response sizes end-to-end from the event.
func TestMountLogEntrySerializesNewFields(t *testing.T) {
	manager := &mountManager{reqLogMax: 100}
	manager.observe(mcpmount.Event{
		Slug:          "aaaaaaaa",
		NodeID:        "node-a",
		TokenID:       "tok-123",
		Method:        "POST",
		Path:          "/mcp",
		Status:        200,
		Duration:      9 * time.Millisecond,
		RemoteAddr:    "203.0.113.7:55555",
		Command:       "tools/call:read_file",
		UserAgent:     "mcp-client/3.1",
		RequestBytes:  512,
		ResponseBytes: 2048,
	})

	rec := httptest.NewRecorder()
	manager.handleLogs(rec, httptest.NewRequest(
		http.MethodGet, "/api/mounts/logs?slug=aaaaaaaa&limit=10", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("handleLogs = %d, want 200", rec.Code)
	}
	var body struct {
		Entries []mountLogEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(body.Entries))
	}
	e := body.Entries[0]
	if e.Command != "tools/call:read_file" {
		t.Fatalf("command = %q", e.Command)
	}
	if e.UserAgent != "mcp-client/3.1" {
		t.Fatalf("user_agent = %q", e.UserAgent)
	}
	if e.RequestBytes != 512 {
		t.Fatalf("request_bytes = %d, want 512", e.RequestBytes)
	}
	if e.ResponseBytes != 2048 {
		t.Fatalf("response_bytes = %d, want 2048", e.ResponseBytes)
	}
	if e.RemoteAddr != "203.0.113.7" {
		t.Fatalf("remote_addr = %q, want host only", e.RemoteAddr)
	}

	// The JSON wire form must use the documented keys.
	raw := rec.Body.String()
	for _, key := range []string{"\"command\"", "\"user_agent\"", "\"request_bytes\"", "\"response_bytes\""} {
		if !strings.Contains(raw, key) {
			t.Fatalf("serialized log missing %s in: %s", key, raw)
		}
	}
}

func TestMountTokenIssueTTLAndNever(t *testing.T) {
	dir := t.TempDir()
	registry, err := mcpmount.OpenRegistry(filepath.Join(dir, "mounts.json"))
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	if err := registry.Upsert(&mcpmount.Record{Slug: "aaaaaaaa", MountID: "node-stable/self"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	tokens, err := mcpmount.OpenTokenStore(filepath.Join(dir, "mount-tokens.json"))
	if err != nil {
		t.Fatalf("OpenTokenStore: %v", err)
	}
	manager := &mountManager{
		registry: registry,
		tokens:   tokens,
		tokenTTL: 90 * 24 * time.Hour,
	}
	router := mux.NewRouter()
	router.Handle("/api/mounts/{slug}/tokens", http.HandlerFunc(manager.handleTokens))

	post := func(body string) (int, struct {
		Token     string         `json:"token"`
		TokenInfo mcpmount.Token `json:"token_info"`
	}) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/mounts/aaaaaaaa/tokens", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		var out struct {
			Token     string         `json:"token"`
			TokenInfo mcpmount.Token `json:"token_info"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil && rec.Code == http.StatusCreated {
			t.Fatalf("decode response: %v", err)
		}
		return rec.Code, out
	}

	code, out := post(`{"name":"forever","never_expires":true}`)
	if code != http.StatusCreated || !out.TokenInfo.ExpiresAt.IsZero() {
		t.Fatalf("never_expires = code %d, expires %v; want 201 with zero expiry", code, out.TokenInfo.ExpiresAt)
	}
	if got, ok := tokens.Get(out.TokenInfo.ID); !ok || !got.ExpiresAt.IsZero() {
		t.Fatalf("stored never token = %+v, %v", got, ok)
	}

	code, out = post(`{"name":"week","ttl_seconds":604800}`)
	if code != http.StatusCreated {
		t.Fatalf("custom ttl = %d, want 201", code)
	}
	wantExpiry := time.Now().Add(7 * 24 * time.Hour)
	if out.TokenInfo.ExpiresAt.Before(wantExpiry.Add(-time.Minute)) ||
		out.TokenInfo.ExpiresAt.After(wantExpiry.Add(time.Minute)) {
		t.Fatalf("custom expiry = %v, want about %v", out.TokenInfo.ExpiresAt, wantExpiry)
	}

	code, out = post(`{"name":"default"}`)
	if code != http.StatusCreated {
		t.Fatalf("default ttl = %d, want 201", code)
	}
	wantDefault := time.Now().Add(90 * 24 * time.Hour)
	if out.TokenInfo.ExpiresAt.Before(wantDefault.Add(-time.Minute)) {
		t.Fatalf("default expiry = %v, want about %v", out.TokenInfo.ExpiresAt, wantDefault)
	}

	code, out = post(`{"name":"negative","ttl_seconds":-1}`)
	if code != http.StatusCreated || !out.TokenInfo.ExpiresAt.IsZero() {
		t.Fatalf("negative ttl = code %d, expires %v; want 201 with zero expiry", code, out.TokenInfo.ExpiresAt)
	}
}
