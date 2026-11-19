package schedule

import (
	"testing"
	"time"
)

func TestServiceUpdateEditsContentAndExecutionTarget(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	svc := NewService(store, func() time.Time { return now })
	every := int64(300)
	rec, err := svc.Create("session-1", CreateRequest{
		Prompt: "old prompt", Title: "Old title", EverySeconds: &every,
		Goal: "old goal", Plan: []string{"old step"},
	}, OriginManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	title := "New title"
	prompt := "new prompt"
	goal := "new goal"
	plan := []string{" inspect disk ", "", "inspect memory"}
	workspace := " /workspace/demo "
	profile := " scheduled "
	targetKind := TargetWorkspace
	result, err := svc.Update("session-1", rec.ID, rec, UpdateContent{
		Title:           &title,
		Prompt:          &prompt,
		Goal:            &goal,
		Plan:            &plan,
		TargetKind:      &targetKind,
		TargetWorkspace: &workspace,
		TargetProfile:   &profile,
	}, nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !result.Updated || result.Record == nil {
		t.Fatalf("Update result = %+v", result)
	}
	if result.Record.Title != title || result.Record.Prompt != prompt || result.Record.Goal != goal {
		t.Fatalf("content not updated: %+v", result.Record)
	}
	if len(result.Record.Plan) != 2 || result.Record.Plan[0] != "inspect disk" {
		t.Fatalf("plan not normalized: %+v", result.Record.Plan)
	}

	task, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if task.SessionID != "" {
		t.Fatalf("workspace job must unbind SessionID, got %q", task.SessionID)
	}
	if task.CreatedBySessionID != "session-1" {
		t.Fatalf("creator session changed: %q", task.CreatedBySessionID)
	}
	if task.TargetKind != TargetWorkspace || task.TargetWorkspace != "/workspace/demo" || task.TargetProfile != "scheduled" {
		t.Fatalf("target not updated: %+v", task)
	}
}

func TestServiceUpdateRejectsInvalidExecutionTarget(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	svc := NewService(store, func() time.Time { return now })
	every := int64(300)
	rec, err := svc.Create("session-1", CreateRequest{
		Prompt: "p", Title: "t", EverySeconds: &every,
	}, OriginManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	workspaceKind := TargetWorkspace
	emptyWorkspace := " "
	result, err := svc.Update("session-1", rec.ID, rec, UpdateContent{
		TargetKind:      &workspaceKind,
		TargetWorkspace: &emptyWorkspace,
	}, nil)
	if err != nil {
		t.Fatalf("Update returned transport error: %v", err)
	}
	if result.Updated || result.Code != CodeInvalidRule {
		t.Fatalf("workspace without path = %+v", result)
	}

	headlessKind := TargetHeadless
	result, err = svc.Update("session-1", rec.ID, rec, UpdateContent{
		TargetKind: &headlessKind,
	}, nil)
	if err != nil {
		t.Fatalf("Update returned transport error: %v", err)
	}
	if result.Updated || result.Code != CodeInvalidRule {
		t.Fatalf("headless without goal = %+v", result)
	}
}

func TestServiceUpdateAfterRestartsDelayFromEditTime(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	svc := NewService(store, func() time.Time { return now })
	every := int64(300)
	rec, err := svc.Create("session-1", CreateRequest{
		Prompt: "p", Title: "t", EverySeconds: &every,
	}, OriginManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	afterSeconds := int64(120)
	result, err := svc.Update("session-1", rec.ID, rec, UpdateContent{}, &TimingChange{
		Kind:         KindAfter,
		AfterSeconds: &afterSeconds,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !result.Updated || result.Record == nil {
		t.Fatalf("Update result = %+v", result)
	}
	if result.Record.Kind != KindAfter || result.Record.AfterSeconds != afterSeconds {
		t.Fatalf("after selector not applied: %+v", result.Record)
	}
	want := now.Add(2 * time.Minute)
	if !result.Record.ScheduledAt.Equal(want) {
		t.Fatalf("scheduledAt = %s, want %s", result.Record.ScheduledAt, want)
	}
}

func TestServiceUpdateCanRebindHeadlessJobToSession(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	svc := NewService(store, func() time.Time { return now })
	delay := int64(60)
	rec, err := svc.Create("creator-session", CreateRequest{
		Prompt: "p", Title: "t", AfterSeconds: &delay,
		Goal: "goal", TargetKind: TargetHeadless,
	}, OriginManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	sessionKind := TargetSession
	targetSessionID := "target-session"
	result, err := svc.Update("creator-session", rec.ID, rec, UpdateContent{
		TargetKind:      &sessionKind,
		TargetSessionID: &targetSessionID,
	}, nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !result.Updated {
		t.Fatalf("Update result = %+v", result)
	}
	task, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if task.SessionID != targetSessionID || task.TargetKind != TargetSession {
		t.Fatalf("session rebind failed: %+v", task)
	}
	if task.TargetWorkspace != "" || task.TargetProfile != "" {
		t.Fatalf("session target must clear workspace/profile: %+v", task)
	}
}

func TestServiceUpdateEndedTaskKeepsInactiveUnlessTimingRearms(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	svc := NewService(store, func() time.Time { return now })
	delay := int64(60)
	rec, err := svc.Create("session-1", CreateRequest{
		Prompt: "p", Title: "t", AfterSeconds: &delay,
	}, OriginManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	task, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	task.Status = StatusInactive
	applied, err := store.UpdateCAS(task, task.StateVersion)
	if err != nil || !applied {
		t.Fatalf("mark inactive: applied=%v err=%v", applied, err)
	}

	title := "edited title"
	contentOnly, err := svc.Update("session-1", rec.ID, rec, UpdateContent{
		Title: &title,
	}, nil)
	if err != nil || !contentOnly.Updated {
		t.Fatalf("content update = %+v, err=%v", contentOnly, err)
	}
	if contentOnly.Status != StatusInactive {
		t.Fatalf("content-only edit must stay inactive, got %q", contentOnly.Status)
	}

	afterSeconds := int64(120)
	rearmed, err := svc.Update("session-1", rec.ID, *contentOnly.Record, UpdateContent{}, &TimingChange{
		Kind:         KindAfter,
		AfterSeconds: &afterSeconds,
	})
	if err != nil || !rearmed.Updated {
		t.Fatalf("timing update = %+v, err=%v", rearmed, err)
	}
	if rearmed.Status != StatusActive || rearmed.Record == nil {
		t.Fatalf("timing edit must re-arm task: %+v", rearmed)
	}
	want := now.Add(2 * time.Minute)
	if !rearmed.Record.ScheduledAt.Equal(want) {
		t.Fatalf("scheduledAt = %s, want %s", rearmed.Record.ScheduledAt, want)
	}
}
