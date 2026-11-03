package schedule

import (
	"testing"
	"time"
)

func TestServicePauseResumePreservesFutureTarget(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	svc := NewService(store, func() time.Time { return now })
	every := int64(300)
	rec, err := svc.Create("session-1", CreateRequest{Prompt: "ping", Title: "Ping", EverySeconds: &every}, OriginManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	paused, err := svc.Pause("session-1", rec.ID)
	if err != nil || !paused.Updated || paused.Status != StatusPaused {
		t.Fatalf("Pause = %+v, err=%v", paused, err)
	}
	due, err := store.ListActiveDue(now.Add(24 * time.Hour))
	if err != nil {
		t.Fatalf("ListActiveDue: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("paused task must not be due, got %+v", due)
	}

	title := "Ping edited"
	updated, err := svc.Update("session-1", rec.ID, rec, UpdateContent{Title: &title}, nil)
	if err != nil || !updated.Updated || updated.Status != StatusPaused {
		t.Fatalf("Update paused = %+v, err=%v", updated, err)
	}

	resumed, err := svc.Resume("session-1", rec.ID)
	if err != nil || !resumed.Updated || resumed.Status != StatusActive {
		t.Fatalf("Resume = %+v, err=%v", resumed, err)
	}
	if resumed.Record == nil || !resumed.Record.ScheduledAt.Equal(rec.ScheduledAt) {
		t.Fatalf("resume before target must preserve scheduledAt: %+v", resumed.Record)
	}
}

func TestServiceResumeAfterTargetAdvancesRecurringRule(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	svc := NewService(store, func() time.Time { return now })
	every := int64(300)
	rec, err := svc.Create("session-1", CreateRequest{Prompt: "ping", Title: "Ping", EverySeconds: &every}, OriginManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Pause("session-1", rec.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	now = rec.ScheduledAt.Add(10 * time.Minute)
	resumed, err := svc.Resume("session-1", rec.ID)
	if err != nil || !resumed.Updated {
		t.Fatalf("Resume = %+v, err=%v", resumed, err)
	}
	want := now.Add(5 * time.Minute)
	if resumed.Record == nil || !resumed.Record.ScheduledAt.Equal(want) {
		t.Fatalf("scheduledAt = %+v, want %s", resumed.Record, want)
	}
}

func TestServiceResumePastAtRequiresEdit(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	svc := NewService(store, func() time.Time { return now })
	at := now.Add(time.Hour).Format(time.RFC3339)
	rec, err := svc.Create("session-1", CreateRequest{Prompt: "ping", Title: "Ping", At: &at}, OriginManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Pause("session-1", rec.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	now = now.Add(2 * time.Hour)
	resumed, err := svc.Resume("session-1", rec.ID)
	if err != nil {
		t.Fatalf("Resume returned transport error: %v", err)
	}
	if resumed.Updated || resumed.Code != CodeNotFuture || resumed.Message == "" {
		t.Fatalf("resume result = %+v, want not_future with detail", resumed)
	}
}

func TestServiceHeadlessTaskManagedByCreatorSession(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()
	svc := NewService(store, fixedClock(time.Now()))
	delay := int64(60)
	rec, err := svc.Create("creator-session", CreateRequest{
		Prompt: "nightly", Title: "Nightly", AfterSeconds: &delay,
		Goal: "run nightly check", TargetKind: TargetHeadless,
	}, OriginManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	catalog, err := svc.Catalog()
	if err != nil || len(catalog) != 1 {
		t.Fatalf("Catalog = %+v, err=%v", catalog, err)
	}
	if catalog[0].CreatedBySessionID != "creator-session" {
		t.Fatalf("createdBySessionId = %q, want creator-session", catalog[0].CreatedBySessionID)
	}
	deleted, err := svc.Delete("creator-session", rec.ID)
	if err != nil || !deleted.Deleted {
		t.Fatalf("Delete by creator = %+v, err=%v", deleted, err)
	}
}
