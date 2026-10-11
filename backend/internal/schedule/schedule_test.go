package schedule

import (
	"testing"
	"time"
)

func fixedClock(t time.Time) Clock { return func() time.Time { return t } }

func TestCreateAfterValidatesAndComputesTarget(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	rec, err := CreateAfter("schedule-1", "check the build", "Build check", 600, now)
	if err != nil {
		t.Fatalf("CreateAfter: %v", err)
	}
	if rec.Kind != KindAfter || rec.AfterSeconds != 600 {
		t.Fatalf("unexpected record: %+v", rec)
	}
	if want := now.Add(10 * time.Minute); !rec.ScheduledAt.Equal(want) {
		t.Fatalf("scheduledAt = %s, want %s", rec.ScheduledAt, want)
	}
	if _, err := CreateAfter("schedule-2", "x", "  ", 60, now); err == nil {
		t.Fatal("blank title must be rejected")
	}
	if _, err := CreateAfter("schedule-3", "x", "t", 0, now); err == nil {
		t.Fatal("non-positive delay must be rejected")
	}
}

func TestCreateAtRequiresOffsetAndFuture(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	rec, err := CreateAt("schedule-1", "review", "Release review", "2099-01-01T09:00:00+08:00", now)
	if err != nil {
		t.Fatalf("CreateAt: %v", err)
	}
	if rec.ScheduledAt.Location() != time.UTC {
		t.Fatalf("scheduledAt must be UTC, got %s", rec.ScheduledAt.Location())
	}
	if _, err := CreateAt("schedule-2", "review", "t", "2099-01-01T09:00:00", now); err == nil {
		t.Fatal("offset-less string must be rejected")
	}
	if _, err := CreateAt("schedule-3", "review", "t", "2020-01-01T09:00:00Z", now); err == nil {
		t.Fatal("past target must be rejected")
	}
}

func TestCreateEveryEnforcesMinimumInterval(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	if _, err := CreateEvery("schedule-1", "queue", "Queue check", 30, now); err == nil {
		t.Fatal("interval below the floor must be rejected")
	}
	rec, err := CreateEvery("schedule-2", "queue", "Queue check", 300, now)
	if err != nil {
		t.Fatalf("CreateEvery: %v", err)
	}
	if want := now.Add(5 * time.Minute); !rec.ScheduledAt.Equal(want) {
		t.Fatalf("scheduledAt = %s, want %s", rec.ScheduledAt, want)
	}
}

func TestResolveOccurrenceEverySkipsMissedOccurrences(t *testing.T) {
	target := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	rec := Record{ID: "s", Kind: KindEvery, EverySeconds: 300, ScheduledAt: target}
	accepted := target.Add(25 * time.Minute) // 错过 5 次
	occ, err := ResolveOccurrence(rec, accepted)
	if err != nil {
		t.Fatalf("ResolveOccurrence: %v", err)
	}
	if want := target.Add(25 * time.Minute); !occ.OccurrenceAt.Equal(want) {
		t.Fatalf("occurrence = %s, want %s", occ.OccurrenceAt, want)
	}
	if occ.NextScheduledAt == nil || !occ.NextScheduledAt.Equal(target.Add(30*time.Minute)) {
		t.Fatalf("next = %v, want +30m", occ.NextScheduledAt)
	}
}

func TestResolveOccurrenceOneShotHasNoNext(t *testing.T) {
	target := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	occ, err := ResolveOccurrence(Record{ID: "s", Kind: KindAt, ScheduledAt: target}, target)
	if err != nil {
		t.Fatalf("ResolveOccurrence: %v", err)
	}
	if !occ.OccurrenceAt.Equal(target) || occ.NextScheduledAt != nil {
		t.Fatalf("unexpected occurrence: %+v", occ)
	}
}

func TestServiceCreateListDeleteAndConflict(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	svc := NewService(store, fixedClock(now))

	delay := int64(600)
	rec, err := svc.Create("session-1", CreateRequest{Prompt: "check", Title: "Check", AfterSeconds: &delay})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	list, err := svc.List("session-1")
	if err != nil || len(list) != 1 || list[0].ID != rec.ID {
		t.Fatalf("List = %+v, err = %v", list, err)
	}
	catalog, err := svc.Catalog()
	if err != nil || len(catalog) != 1 || catalog[0].SessionID != "session-1" {
		t.Fatalf("Catalog = %+v, err = %v", catalog, err)
	}

	// 冲突：expected 与当前不一致。
	stale := rec
	stale.ScheduledAt = stale.ScheduledAt.Add(time.Minute)
	title := "Renamed"
	res, err := svc.Update("session-1", rec.ID, stale, UpdateContent{Title: &title}, nil)
	if err != nil || res.Updated || res.Code != CodeConflict {
		t.Fatalf("Update conflict = %+v, err = %v", res, err)
	}

	// 正常更新名称。
	res, err = svc.Update("session-1", rec.ID, rec, UpdateContent{Title: &title}, nil)
	if err != nil || !res.Updated || res.Record.Title != "Renamed" {
		t.Fatalf("Update = %+v, err = %v", res, err)
	}

	// 删除。
	del, err := svc.Delete("session-1", rec.ID)
	if err != nil || !del.Deleted {
		t.Fatalf("Delete = %+v, err = %v", del, err)
	}
	if list, _ := svc.List("session-1"); len(list) != 0 {
		t.Fatalf("task should be gone, got %+v", list)
	}
}

func TestServiceSelectorXOR(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()
	svc := NewService(store, fixedClock(time.Now()))
	delay := int64(60)
	every := int64(60)
	if _, err := svc.Create("s", CreateRequest{Prompt: "x", Title: "t", AfterSeconds: &delay, EverySeconds: &every}); err == nil {
		t.Fatal("two selectors must be rejected")
	}
	if _, err := svc.Create("s", CreateRequest{Prompt: "x", Title: "t"}); err == nil {
		t.Fatal("zero selectors must be rejected")
	}
}
