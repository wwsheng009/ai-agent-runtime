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

func TestApplySystemBlockedUsesCanonicalReason(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	g := &SessionGoal{Status: StatusActive}
	if err := ApplySystemBlocked(g, "no_progress", now); err != nil {
		t.Fatal(err)
	}
	if g.Status != StatusBlocked || g.BlockedReason != SystemBlockedReason("no_progress") || !g.UpdatedAt.Equal(now) {
		t.Fatalf("unexpected blocked state: %+v", g)
	}
	if err := ApplySystemBlocked(g, "no_progress", now); err != nil {
		t.Fatalf("same-status transition must be idempotent: %v", err)
	}
}

func TestApplySystemRoundLimitSetsCodeAndReason(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	g := &SessionGoal{Status: StatusActive, MaxRounds: 3}
	if err := ApplySystemRoundLimit(g, now); err != nil {
		t.Fatal(err)
	}
	if g.Status != StatusBlocked || g.BlockedCode != BlockedCodeRoundLimit {
		t.Fatalf("unexpected round-limit state: %+v", g)
	}
	if g.BlockedReason != "Goal reached its configured limit of 3 rounds." {
		t.Fatalf("BlockedReason = %q", g.BlockedReason)
	}
}

func TestApplySystemBudgetLimitedAndAddTokens(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	g := &SessionGoal{Status: StatusActive, TokenBudget: 100}
	if !AddTokens(g, 25) || g.TokensUsed != 25 {
		t.Fatalf("usage not added: %+v", g)
	}
	if AddTokens(g, 0) || AddTokens(g, -1) {
		t.Fatal("non-positive usage must be ignored")
	}
	if err := ApplySystemBudgetLimited(g, now); err != nil {
		t.Fatal(err)
	}
	if g.Status != StatusBudgetLimited || !g.UpdatedAt.Equal(now) {
		t.Fatalf("unexpected budget_limited state: %+v", g)
	}
	if !AddTokens(g, 75) || g.TokensUsed != 100 {
		t.Fatalf("budget_limited accounting must remain active: %+v", g)
	}
	g.Status = StatusComplete
	if AddTokens(g, 10) {
		t.Fatal("terminal goals must not accrue usage")
	}
}
