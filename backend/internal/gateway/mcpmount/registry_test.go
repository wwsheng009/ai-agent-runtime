package mcpmount

import (
	"errors"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

func record(slug, mountID, nodeID string) *Record {
	return &Record{
		Slug:    slug,
		MountID: mountID,
		NodeID:  nodeID,
		Kind:    "mcp",
		Name:    mountID,
		Labels:  &wire.MountLabels{Profile: "readonly", ToolPrefix: "remote_prod"},
		Limits:  wire.MountLimits{MaxBodyBytes: 8 << 20, TimeoutSec: 600},
	}
}

func TestRegistryUpsertIsIdempotent(t *testing.T) {
	reg := NewRegistry()
	first := record("k7f3qz2a", "node-a/prod", "node-a")
	if err := reg.Upsert(first); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	created := mustGet(t, reg, "k7f3qz2a").CreatedAt
	if created.IsZero() {
		t.Fatalf("CreatedAt was not stamped")
	}

	// Operator emergency stop must survive a reconnect.
	if !reg.SetDisabled("k7f3qz2a", true) {
		t.Fatalf("SetDisabled reported missing mount")
	}

	time.Sleep(time.Millisecond)
	again := record("k7f3qz2a", "node-a/prod", "node-b")
	if err := reg.Upsert(again); err != nil {
		t.Fatalf("re-Upsert: %v", err)
	}
	got := mustGet(t, reg, "k7f3qz2a")
	if got.NodeID != "node-b" {
		t.Fatalf("NodeID = %q, want node-b (reconnect must rebind)", got.NodeID)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt changed on reconnect: %v -> %v", created, got.CreatedAt)
	}
	if !got.Disabled {
		t.Fatalf("Disabled was cleared by a reconnect")
	}
	if got.Stale {
		t.Fatalf("reconnect left the record stale")
	}
}

func TestRegistryRejectsSlugConflict(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Upsert(record("k7f3qz2a", "node-a/prod", "node-a")); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	err := reg.Upsert(record("k7f3qz2a", "node-b/prod", "node-b"))
	if err == nil {
		t.Fatalf("Upsert accepted a conflicting mount id")
	}
	var conflict *SlugConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %T (%v), want *SlugConflictError", err, err)
	}
	if conflict.ExistingMountID != "node-a/prod" || conflict.IncomingMountID != "node-b/prod" {
		t.Fatalf("conflict detail = %+v", conflict)
	}
	// The incumbent must be untouched: a rejected registration never rewrites
	// the published URL.
	if got := mustGet(t, reg, "k7f3qz2a"); got.MountID != "node-a/prod" || got.NodeID != "node-a" {
		t.Fatalf("incumbent record was modified: %+v", got)
	}
}

func TestRegistryPinChangeReleasesOldSlug(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Upsert(record("k7f3qz2a", "node-a/prod", "node-a")); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := reg.Upsert(record("team-api", "node-a/prod", "node-a")); err != nil {
		t.Fatalf("Upsert pinned slug: %v", err)
	}
	if _, ok := reg.Get("k7f3qz2a"); ok {
		t.Fatalf("old slug still registered after a pin change")
	}
	if got, ok := reg.GetByMountID("node-a/prod"); !ok || got.Slug != "team-api" {
		t.Fatalf("GetByMountID = %+v, %v", got, ok)
	}
	if got := reg.Count(); got != 1 {
		t.Fatalf("Count = %d, want 1 (a pin change must not duplicate)", got)
	}
}

func TestRegistryStaleAndRemoveStale(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Upsert(record("live-a", "node-a/a", "node-a"))
	_ = reg.Upsert(record("gone-b", "node-b/b", "node-b"))
	_ = reg.Upsert(record("gone-c", "node-c/c", "node-c"))
	reg.MarkStale("gone-b")
	reg.MarkStale("gone-c")

	if got := reg.CountLive(); got != 1 {
		t.Fatalf("CountLive = %d, want 1", got)
	}
	removed := reg.RemoveStale()
	if len(removed) != 2 || removed[0] != "gone-b" || removed[1] != "gone-c" {
		t.Fatalf("RemoveStale = %v, want sorted [gone-b gone-c]", removed)
	}
	if got := reg.Count(); got != 1 {
		t.Fatalf("Count after RemoveStale = %d, want 1", got)
	}
	// Idempotent and non-nil so the API can serialize "removed": [].
	again := reg.RemoveStale()
	if again == nil || len(again) != 0 {
		t.Fatalf("second RemoveStale = %v, want empty non-nil", again)
	}
}

func TestRegistryRestoreMarksStale(t *testing.T) {
	source := NewRegistry()
	_ = source.Upsert(record("k7f3qz2a", "node-a/prod", "node-a"))

	target := NewRegistry()
	if restored := target.Restore(source.List()); restored != 1 {
		t.Fatalf("Restore = %d, want 1", restored)
	}
	got, ok := target.Get("k7f3qz2a")
	if !ok || !got.Stale {
		t.Fatalf("restored record = %+v, %v (want stale)", got, ok)
	}
	if got.NodeID != "node-a" {
		t.Fatalf("restored NodeID = %q, want the last known node", got.NodeID)
	}
	// Reconnect flips it back to live without changing the slug.
	if err := target.Upsert(record("k7f3qz2a", "node-a/prod", "node-a")); err != nil {
		t.Fatalf("re-Upsert after restore: %v", err)
	}
	if got := mustGet(t, target, "k7f3qz2a"); got.Stale {
		t.Fatalf("reconnect did not clear Stale")
	}
}

func TestRegistryRejectsIncompleteRecord(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Upsert(&Record{Slug: "k7f3qz2a"}); err == nil {
		t.Fatalf("Upsert accepted a record without a mount id")
	}
	if err := reg.Upsert(&Record{MountID: "node-a/prod"}); err == nil {
		t.Fatalf("Upsert accepted a record without a slug")
	}
	if reg.Upsert(nil) == nil {
		t.Fatalf("Upsert(nil) = nil error")
	}
}

func mustGet(t *testing.T, reg *Registry, slug string) *Record {
	t.Helper()
	record, ok := reg.Get(slug)
	if !ok {
		t.Fatalf("mount %q not found", slug)
	}
	return record
}
