package tunnel

import (
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

func TestRegistryRemoveStale(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(&Node{NodeID: "live-a"})
	reg.Upsert(&Node{NodeID: "stale-b", Stale: true})
	reg.Upsert(&Node{NodeID: "live-c"})
	reg.Upsert(&Node{NodeID: "stale-a", Stale: true, Sessions: []wire.SessionSummary{{SessionID: "s1"}}})

	removed := reg.RemoveStale()
	if want := []string{"stale-a", "stale-b"}; !reflect.DeepEqual(removed, want) {
		t.Fatalf("RemoveStale() = %v, want %v", removed, want)
	}
	if got := reg.Count(); got != 2 {
		t.Fatalf("Count() after RemoveStale = %d, want 2 (live nodes kept)", got)
	}
	if _, ok := reg.Get("stale-a"); ok {
		t.Fatalf("stale node stale-a still present after RemoveStale")
	}
	if _, ok := reg.Get("live-a"); !ok {
		t.Fatalf("live node live-a was removed by RemoveStale")
	}

	// Idempotent: a second call finds nothing and returns an empty (non-nil)
	// slice so the API can serialize "removed": [] instead of null.
	again := reg.RemoveStale()
	if again == nil || len(again) != 0 {
		t.Fatalf("second RemoveStale() = %v, want empty non-nil slice", again)
	}
	if got := reg.Count(); got != 2 {
		t.Fatalf("Count() after second RemoveStale = %d, want 2", got)
	}
}
