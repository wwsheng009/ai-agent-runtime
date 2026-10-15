package goal

import (
	"testing"
	"time"
)

func TestApplyUserStatusUpdateComplete(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	g := &SessionGoal{Objective: "x", Status: StatusActive, BlockedReason: "stale blocker"}
	ApplyUserStatusUpdate(g, StatusComplete, "user", "  verified end-to-end  ", now)

	if g.Status != StatusComplete {
		t.Fatalf("status = %q, want complete", g.Status)
	}
	if g.CompletedAt == nil || !g.CompletedAt.Equal(now) {
		t.Fatalf("CompletedAt = %v, want %v", g.CompletedAt, now)
	}
	if g.CompletedBy != "user" {
		t.Fatalf("CompletedBy = %q, want user", g.CompletedBy)
	}
	if g.CompletionSummary != "verified end-to-end" {
		t.Fatalf("CompletionSummary = %q (must be trimmed)", g.CompletionSummary)
	}
	if g.BlockedReason != "" {
		t.Fatalf("BlockedReason = %q, want cleared outside blocked", g.BlockedReason)
	}
	if !g.UpdatedAt.Equal(now) {
		t.Fatalf("UpdatedAt = %v, want %v", g.UpdatedAt, now)
	}
}

func TestApplyUserStatusUpdateNonCompleteClearsCompletionFields(t *testing.T) {
	t.Parallel()

	completed := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	g := &SessionGoal{
		Objective:         "x",
		Status:            StatusComplete,
		CompletedAt:       &completed,
		CompletedBy:       "model",
		CompletionSummary: "done",
	}
	ApplyUserStatusUpdate(g, StatusActive, "user", "ignored", time.Time{})

	if g.Status != StatusActive {
		t.Fatalf("status = %q, want active", g.Status)
	}
	if g.CompletedAt != nil || g.CompletedBy != "" || g.CompletionSummary != "" {
		t.Fatalf("completion fields must be cleared off the complete status: %+v", g)
	}
	if g.UpdatedAt.IsZero() {
		t.Fatal("zero now must fall back to time.Now()")
	}
}

func TestApplyUserStatusUpdateKeepsBlockedReasonOnlyForBlocked(t *testing.T) {
	t.Parallel()

	g := &SessionGoal{Objective: "x", Status: StatusActive, BlockedReason: "same blocker x3"}
	ApplyUserStatusUpdate(g, StatusBlocked, "", "", time.Time{})
	if g.BlockedReason != "same blocker x3" {
		t.Fatalf("BlockedReason = %q, want preserved when entering blocked", g.BlockedReason)
	}
}
