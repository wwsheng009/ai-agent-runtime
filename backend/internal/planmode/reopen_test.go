package planmode

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
)

func reopenStoreFixture(t *testing.T, workspace string) (*planstore.Store, planstore.Record, string) {
	t.Helper()
	store := planstore.NewStore(t.TempDir())
	record, err := store.Record(planstore.RecordOptions{
		SessionID:   "session-1",
		ProjectPath: workspace,
		PlanPath:    "docs/plan.md",
		Title:       "reopen fixture",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	content := "# Plan\n\nrestored body\n"
	record, err = store.Snapshot(planstore.SnapshotOptions{
		ID:       record.ID,
		Decision: "quit",
		Source:   "user",
		Content:  []byte(content),
		Status:   planstore.StatusNotImplemented,
	})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return store, record, content
}

func TestReopenPlanCreatesMissingWorkspaceFile(t *testing.T) {
	workspace := t.TempDir()
	store, record, content := reopenStoreFixture(t, workspace)

	result, err := ReopenPlan(ReopenOptions{Store: store, Workspace: workspace, RecordID: record.ID})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !result.Restored || !result.Created || result.Unchanged {
		t.Fatalf("unexpected result flags: %+v", result)
	}
	if result.Version != 1 || result.Bytes != len(content) {
		t.Fatalf("unexpected version/bytes: %+v", result)
	}
	written, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash("docs/plan.md")))
	if err != nil {
		t.Fatalf("read restored plan: %v", err)
	}
	if string(written) != content {
		t.Fatalf("restored content mismatch: %q", string(written))
	}
}

func TestReopenPlanLeavesIdenticalFileUntouched(t *testing.T) {
	workspace := t.TempDir()
	store, record, content := reopenStoreFixture(t, workspace)
	target := filepath.Join(workspace, filepath.FromSlash("docs/plan.md"))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	result, err := ReopenPlan(ReopenOptions{Store: store, Workspace: workspace, RecordID: record.ID})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !result.Unchanged || result.Restored || result.Created {
		t.Fatalf("expected an unchanged reopen, got %+v", result)
	}
}

func TestReopenPlanRefusesDivergedFileWithoutForce(t *testing.T) {
	workspace := t.TempDir()
	store, record, _ := reopenStoreFixture(t, workspace)
	target := filepath.Join(workspace, filepath.FromSlash("docs/plan.md"))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	local := "# Plan\n\nlocally edited after archiving\n"
	if err := os.WriteFile(target, []byte(local), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := ReopenPlan(ReopenOptions{Store: store, Workspace: workspace, RecordID: record.ID})
	if !errors.Is(err, ErrReopenConflict) {
		t.Fatalf("expected ErrReopenConflict, got %v", err)
	}
	kept, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("read: %v", readErr)
	}
	if string(kept) != local {
		t.Fatalf("conflicting reopen must not touch the file: %q", string(kept))
	}
}

func TestReopenPlanForceOverwritesDivergedFile(t *testing.T) {
	workspace := t.TempDir()
	store, record, content := reopenStoreFixture(t, workspace)
	target := filepath.Join(workspace, filepath.FromSlash("docs/plan.md"))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	result, err := ReopenPlan(ReopenOptions{
		Store:     store,
		Workspace: workspace,
		RecordID:  record.ID,
		Force:     true,
	})
	if err != nil {
		t.Fatalf("reopen --force: %v", err)
	}
	if !result.Restored || result.Created || result.Unchanged {
		t.Fatalf("unexpected forced result: %+v", result)
	}
	written, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("read: %v", readErr)
	}
	if string(written) != content {
		t.Fatalf("forced reopen must restore the snapshot: %q", string(written))
	}
}

func TestReopenPlanSelectsRequestedVersion(t *testing.T) {
	workspace := t.TempDir()
	store, record, _ := reopenStoreFixture(t, workspace)
	second := "# Plan v2\n\nsecond round body\n"
	record, err := store.Snapshot(planstore.SnapshotOptions{
		ID:       record.ID,
		Decision: "request_changes",
		Source:   "user",
		Notes:    "tighten the scope",
		Content:  []byte(second),
	})
	if err != nil {
		t.Fatalf("snapshot v2: %v", err)
	}
	if record.Version != 2 {
		t.Fatalf("expected two rounds, got v%d", record.Version)
	}

	result, err := ReopenPlan(ReopenOptions{
		Store:     store,
		Workspace: workspace,
		RecordID:  record.ID,
		Version:   1,
	})
	if err != nil {
		t.Fatalf("reopen v1: %v", err)
	}
	if result.Version != 1 || result.Bytes != len("# Plan\n\nrestored body\n") {
		t.Fatalf("unexpected version result: %+v", result)
	}
	written, readErr := os.ReadFile(filepath.Join(workspace, filepath.FromSlash("docs/plan.md")))
	if readErr != nil {
		t.Fatalf("read: %v", readErr)
	}
	if string(written) != "# Plan\n\nrestored body\n" {
		t.Fatalf("expected the v1 body, got %q", string(written))
	}
}

func TestReopenPlanRejectsUnknownRecordAndMissingPath(t *testing.T) {
	workspace := t.TempDir()
	store, _, _ := reopenStoreFixture(t, workspace)

	if _, err := ReopenPlan(ReopenOptions{Store: store, Workspace: workspace, RecordID: "demo/ghost"}); !errors.Is(err, planstore.ErrNotFound) {
		t.Fatalf("expected not-found for an unknown id, got %v", err)
	}
	if _, err := ReopenPlan(ReopenOptions{Store: store, Workspace: workspace}); err == nil {
		t.Fatal("expected an empty-id error")
	}

	// A record that has not been reviewed yet carries no snapshot to restore.
	unsnapshotted, err := store.Record(planstore.RecordOptions{
		SessionID:   "s",
		ProjectPath: workspace,
		PlanPath:    "docs/empty.md",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := ReopenPlan(ReopenOptions{Store: store, Workspace: workspace, RecordID: unsnapshotted.ID}); err == nil ||
		!strings.Contains(err.Error(), "no snapshot") {
		t.Fatalf("expected a no-snapshot error, got %v", err)
	}

	// Escaping the workspace is refused rather than writing outside the project.
	escaping, err := store.Record(planstore.RecordOptions{
		SessionID:   "s",
		ProjectPath: workspace,
		PlanPath:    "../outside.md",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := store.Snapshot(planstore.SnapshotOptions{
		ID:       escaping.ID,
		Decision: "quit",
		Source:   "user",
		Content:  []byte("# outside\n"),
	}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// Records keep the raw path; escaping ones must fail, never write outside.
	if _, err := ReopenPlan(ReopenOptions{Store: store, Workspace: workspace, RecordID: escaping.ID}); err == nil ||
		!strings.Contains(err.Error(), "cannot resolve") {
		t.Fatalf("expected an escape rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(workspace), "outside.md")); err == nil {
		t.Fatal("reopen must not create files outside the workspace")
	}
}

func TestReopenProvenanceRoundTripsThroughState(t *testing.T) {
	state := MarkReopened(Enter("default", "docs/plan.md"), "demo/plan", 3)
	if got := ReopenProvenance(state); got != "demo/plan v3" {
		t.Fatalf("unexpected provenance: %q", got)
	}
	restored := stateFromMap(state.ToMap())
	if restored.ReopenedFrom != "demo/plan" || restored.ReopenedVersion != 3 {
		t.Fatalf("provenance did not round-trip: %+v", restored)
	}
	if got := ReopenProvenance(MarkReopened(state, "", 0)); got != "" {
		t.Fatalf("clearing the record id must clear the provenance, got %q", got)
	}
}
