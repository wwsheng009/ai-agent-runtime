package goal

import (
	"context"
	"strings"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

func TestNewSessionGoalValidatesObjective(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 13, 8, 0, 0, 0, time.UTC)
	created, err := NewSessionGoal("session-1", "  finish implementation  ", now)
	if err != nil {
		t.Fatalf("NewSessionGoal failed: %v", err)
	}
	if created.Objective != "finish implementation" {
		t.Fatalf("expected trimmed objective, got %q", created.Objective)
	}
	if created.Status != StatusActive {
		t.Fatalf("expected active status, got %q", created.Status)
	}
	if created.CreatedAt != now || created.UpdatedAt != now {
		t.Fatalf("expected timestamps to use provided time, got %+v", created)
	}

	if _, err := NewSessionGoal("session-1", " ", now); err == nil {
		t.Fatal("expected empty objective to fail")
	}
	if _, err := NewSessionGoal("session-1", strings.Repeat("a", MaxObjectiveRunes+1), now); err == nil {
		t.Fatal("expected oversize objective to fail")
	}
}

func TestMetadataStoreRoundTripAndClear(t *testing.T) {
	t.Parallel()

	session := runtimechat.NewSession("tester")
	session.ID = "session-1"
	store := NewMetadataStore()
	goal, err := NewSessionGoal(session.ID, "ship goal", time.Now())
	if err != nil {
		t.Fatalf("NewSessionGoal failed: %v", err)
	}
	if err := store.Put(session, goal); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	got, ok, err := store.Get(session)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !ok || got == nil {
		t.Fatal("expected stored goal")
	}
	if got.Objective != goal.Objective || got.Status != StatusActive {
		t.Fatalf("unexpected goal: %+v", got)
	}

	if err := store.Clear(session); err != nil {
		t.Fatalf("Clear failed: %v", err)
	}
	if _, ok, err := store.Get(session); err != nil || ok {
		t.Fatalf("expected goal to be cleared, ok=%v err=%v", ok, err)
	}
}

func TestMetadataStoreDecodesJSONMap(t *testing.T) {
	t.Parallel()

	session := runtimechat.NewSession("tester")
	session.Metadata.Context[MetadataKey] = map[string]interface{}{
		"goal_id":    "goal-1",
		"session_id": "session-1",
		"objective":  "review implementation",
		"status":     "paused",
		"created_at": time.Now().Format(time.RFC3339Nano),
		"updated_at": time.Now().Format(time.RFC3339Nano),
	}

	got, ok, err := NewMetadataStore().Get(session)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !ok || got == nil {
		t.Fatal("expected goal from map")
	}
	if got.Status != StatusPaused || got.Objective != "review implementation" {
		t.Fatalf("unexpected decoded goal: %+v", got)
	}
}

func TestMetadataStorePutPersistentUsesLatestStoredSession(t *testing.T) {
	storage := runtimechat.NewInMemoryStorage()
	session := runtimechat.NewSession("tester")
	session.SetContext("keep", "value")
	if err := storage.Save(context.Background(), session); err != nil {
		t.Fatalf("save session: %v", err)
	}
	goal, err := NewSessionGoal(session.ID, "persistent goal", time.Now())
	if err != nil {
		t.Fatalf("NewSessionGoal failed: %v", err)
	}

	updated, err := NewMetadataStore().PutPersistent(context.Background(), storage, session.ID, goal, MutationUser)
	if err != nil {
		t.Fatalf("PutPersistent failed: %v", err)
	}
	if got, ok := updated.GetContext("keep"); !ok || got != "value" {
		t.Fatalf("expected unrelated context to remain, got ok=%v value=%#v", ok, got)
	}
	stored, ok, err := NewMetadataStore().Get(updated)
	if err != nil || !ok || stored == nil {
		t.Fatalf("expected stored goal, ok=%v err=%v", ok, err)
	}
	if stored.Objective != "persistent goal" {
		t.Fatalf("unexpected stored goal: %+v", stored)
	}
}

func TestGoalMergePolicyDoesNotRegressCompleteToActive(t *testing.T) {
	storage := runtimechat.NewInMemoryStorage()
	session := runtimechat.NewSession("tester")
	now := time.Now()
	complete, err := NewSessionGoal(session.ID, "finish goal", now)
	if err != nil {
		t.Fatalf("NewSessionGoal failed: %v", err)
	}
	complete.Status = StatusComplete
	completedAt := now.Add(time.Minute)
	complete.CompletedAt = &completedAt
	complete.CompletedBy = "model"
	complete.CompletionSummary = "done"
	if err := NewMetadataStore().Put(session, complete); err != nil {
		t.Fatalf("Put complete failed: %v", err)
	}
	if err := storage.Save(context.Background(), session); err != nil {
		t.Fatalf("save session: %v", err)
	}

	stale := complete
	stale.Status = StatusActive
	stale.CompletedAt = nil
	stale.CompletedBy = ""
	stale.CompletionSummary = ""
	stale.UpdatedAt = now.Add(-time.Minute)
	updated, err := NewMetadataStore().PutPersistent(context.Background(), storage, session.ID, stale, MutationSystem)
	if err != nil {
		t.Fatalf("PutPersistent stale active failed: %v", err)
	}
	got, ok, err := NewMetadataStore().Get(updated)
	if err != nil || !ok || got == nil {
		t.Fatalf("expected goal, ok=%v err=%v", ok, err)
	}
	if got.Status != StatusComplete || got.CompletedBy != "model" || got.CompletionSummary != "done" {
		t.Fatalf("expected complete goal to be preserved, got %+v", got)
	}
}

func TestGoalMergePolicyRejectsModelCompletionOfPausedGoal(t *testing.T) {
	oldGoal := SessionGoal{
		GoalID:    "goal-1",
		SessionID: "session-1",
		Objective: "paused goal",
		Status:    StatusPaused,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	newGoal := oldGoal
	newGoal.Status = StatusComplete
	if _, err := (GoalMergePolicy{Actor: MutationModel}).MergeContextValue(MetadataKey, oldGoal, newGoal); err == nil {
		t.Fatal("expected model completion of paused goal to be rejected")
	}
}

func TestValidateStatusRejectsUnknown(t *testing.T) {
	t.Parallel()

	if err := ValidateStatus(Status("unknown")); err == nil {
		t.Fatal("expected unknown status to fail")
	}
}
