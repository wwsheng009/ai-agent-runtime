package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/audit"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
)

func nodesAPIForTest(t *testing.T) (*tunnel.Registry, *audit.Log, *int, http.HandlerFunc) {
	t.Helper()
	registry := tunnel.NewRegistry()
	log := audit.New(10, "")
	changes := 0
	handler := handleNodes(registry, log, func(*http.Request) string { return "10.0.0.9" }, func() { changes++ })
	return registry, log, &changes, handler
}

func TestNodesAPIListsNodes(t *testing.T) {
	registry, _, _, handler := nodesAPIForTest(t)
	registry.Upsert(&tunnel.Node{NodeID: "live-a"})
	registry.Upsert(&tunnel.Node{NodeID: "stale-a", Stale: true})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nodes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Nodes []*tunnel.Node `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(body.Nodes))
	}
}

func TestNodesAPIBulkDeleteRequiresStaleFlag(t *testing.T) {
	registry, _, changes, handler := nodesAPIForTest(t)
	registry.Upsert(&tunnel.Node{NodeID: "stale-a", Stale: true})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/nodes", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := registry.Count(); got != 1 {
		t.Fatalf("nodes = %d after refused delete, want 1 (nothing removed)", got)
	}
	if *changes != 0 {
		t.Fatalf("onChanged called %d times, want 0", *changes)
	}
}

func TestNodesAPIBulkDeleteClearsOnlyStale(t *testing.T) {
	registry, log, changes, handler := nodesAPIForTest(t)
	registry.Upsert(&tunnel.Node{NodeID: "live-a"})
	registry.Upsert(&tunnel.Node{NodeID: "stale-b", Stale: true})
	registry.Upsert(&tunnel.Node{NodeID: "stale-a", Stale: true})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/nodes?stale=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Removed []string `json:"removed"`
		Count   int      `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Count != 2 || len(body.Removed) != 2 {
		t.Fatalf("removed = %v (count %d), want the 2 stale nodes", body.Removed, body.Count)
	}
	if body.Removed[0] != "stale-a" || body.Removed[1] != "stale-b" {
		t.Fatalf("removed = %v, want sorted [stale-a stale-b]", body.Removed)
	}
	if got := registry.Count(); got != 1 {
		t.Fatalf("nodes = %d, want 1 (live node kept)", got)
	}
	if _, ok := registry.Get("live-a"); !ok {
		t.Fatalf("live node live-a was removed")
	}
	if *changes != 1 {
		t.Fatalf("onChanged called %d times, want 1", *changes)
	}
	events := log.Entries(10)
	if len(events) != 2 {
		t.Fatalf("audit events = %d, want 2 (one node_removed per stale node)", len(events))
	}
	for _, ev := range events {
		if ev.Type != audit.TypeNodeRemoved || ev.IP != "10.0.0.9" {
			t.Fatalf("audit event = %+v, want type=%s ip=10.0.0.9", ev, audit.TypeNodeRemoved)
		}
	}
}

func TestNodesAPIBulkDeleteWithoutStaleNodesDoesNotNotify(t *testing.T) {
	registry, log, changes, handler := nodesAPIForTest(t)
	registry.Upsert(&tunnel.Node{NodeID: "live-a"})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/nodes?stale=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if *changes != 0 {
		t.Fatalf("onChanged called %d times, want 0", *changes)
	}
	if got := len(log.Entries(10)); got != 0 {
		t.Fatalf("audit events = %d, want 0", got)
	}
	if got := registry.Count(); got != 1 {
		t.Fatalf("nodes = %d, want 1", got)
	}
}

func TestNodesAPIMethodNotAllowed(t *testing.T) {
	_, _, _, handler := nodesAPIForTest(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/nodes", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
