package acp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanUpdateNormalizesEntries(t *testing.T) {
	t.Parallel()

	update := PlanUpdate([]PlanEntry{
		{Content: "  分析需求  ", Status: "PENDING", Priority: ""},
		{Content: "修改实现", Status: "in_progress", Priority: "HIGH"},
		{Content: "运行测试", Status: "completed", Priority: "low"},
		{Content: "   ", Status: "pending", Priority: "medium"}, // blank content dropped
		{Content: "非法状态", Status: "blocked", Priority: "high"},  // unknown status dropped
	})

	if update.SessionUpdate != SessionUpdatePlan {
		t.Fatalf("kind = %q, want %q", update.SessionUpdate, SessionUpdatePlan)
	}
	want := []PlanEntry{
		{Content: "分析需求", Status: PlanStatusPending, Priority: PlanPriorityMedium},
		{Content: "修改实现", Status: PlanStatusInProgress, Priority: PlanPriorityHigh},
		{Content: "运行测试", Status: PlanStatusCompleted, Priority: PlanPriorityLow},
	}
	if len(update.Entries) != len(want) {
		t.Fatalf("entries = %+v, want %+v", update.Entries, want)
	}
	for i := range want {
		if update.Entries[i] != want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, update.Entries[i], want[i])
		}
	}
}

func TestPlanUpdateMarshalShape(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(PlanUpdate([]PlanEntry{
		{Content: "分析需求", Status: PlanStatusPending},
	}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(payload)
	for _, want := range []string{
		`"sessionUpdate":"plan"`,
		`"content":"分析需求"`,
		`"status":"pending"`,
		`"priority":"medium"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("payload %s missing %s", text, want)
		}
	}
}

// TestPlanUpdateEmptyEntriesMarshalsAsArray pins the wire contract: clients
// replace their plan wholesale, so an empty list must clear it rather than
// arriving as a missing or null field.
func TestPlanUpdateEmptyEntriesMarshalsAsArray(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(PlanUpdate(nil))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(payload); got != `{"sessionUpdate":"plan","entries":[]}` {
		t.Fatalf("payload = %s", got)
	}
}
