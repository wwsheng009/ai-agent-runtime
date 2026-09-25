package planmode

import (
	"context"
	"fmt"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
)

// AICLI_PLANS_MAX_VERSIONS bounds the snapshot rounds kept per plan; unset keeps
// every round (the historical behavior).
func TestArchivePlanPrunesRoundsWithRetentionEnv(t *testing.T) {
	ctx := context.Background()
	store := planstore.NewStore(t.TempDir())
	workspace := t.TempDir()
	archive := func(content string) planstore.Record {
		t.Helper()
		rec, err := ArchivePlan(ctx, ArchiveOptions{
			Store:     store,
			SessionID: "session-retention",
			Workspace: workspace,
			PlanPath:  "docs/plan.md",
			Decision:  string(ExitRequestChanges),
			Source:    "user",
			Content:   []byte(content),
		})
		if err != nil {
			t.Fatalf("archive: %v", err)
		}
		return rec
	}

	t.Setenv(archiveRetentionEnv, "2")
	rec := archive("round 1")
	rec = archive("round 2")
	rec = archive("round 3")

	if rec.Version != 3 {
		t.Fatalf("expected the version counter to keep counting, got %d", rec.Version)
	}
	if len(rec.Rounds) != 2 {
		t.Fatalf("expected 2 retained rounds, got %d", len(rec.Rounds))
	}
	if rec.Rounds[0].Version != 2 || rec.Rounds[1].Version != 3 {
		t.Fatalf("unexpected retained rounds: %+v", rec.Rounds)
	}
	if _, err := store.ReadVersion(rec.ID, 1); err == nil {
		t.Fatal("pruned round 1 snapshot must be unreadable")
	}
	latest, err := store.ReadLatest(rec.ID)
	if err != nil {
		t.Fatalf("read latest: %v", err)
	}
	if string(latest) != "round 3" {
		t.Fatalf("latest snapshot changed: %q", latest)
	}

	// Without the env every round is kept, which is what the docs promise.
	t.Setenv(archiveRetentionEnv, "")
	other := planstore.NewStore(t.TempDir())
	for i := 0; i < 3; i++ {
		rec, err = ArchivePlan(ctx, ArchiveOptions{
			Store:     other,
			SessionID: "session-retention",
			Workspace: workspace,
			PlanPath:  "docs/other-plan.md",
			Decision:  string(ExitRequestChanges),
			Source:    "user",
			Content:   []byte(fmt.Sprintf("round %d", i+1)),
		})
		if err != nil {
			t.Fatalf("archive without retention: %v", err)
		}
	}
	if len(rec.Rounds) != 3 {
		t.Fatalf("unset retention must keep all rounds, got %d", len(rec.Rounds))
	}
}

func TestPlanRetentionLimitParsing(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want int
	}{
		{"", 0},
		{"0", 0},
		{"-3", 0},
		{"abc", 0},
		{"4", 4},
		{" 2 ", 2},
	} {
		t.Setenv(archiveRetentionEnv, test.raw)
		if got := PlanRetentionLimit(); got != test.want {
			t.Fatalf("PlanRetentionLimit(%q) = %d, want %d", test.raw, got, test.want)
		}
	}
}
