package memorystore

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStoreExposureDoesNotCountAsUse(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	note, err := store.Append(AppendNoteOptions{Text: "Prefer append-only memory writes."})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	at := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	result, err := store.RecordExposure(context.Background(), Exposure{
		MemoryIDs: []string{note.ID, "mem_missing"},
		SessionID: "s1", TurnID: "turn1", MessageID: "msg1",
		UseKind: UsageInjectedOnly, UsedAt: at,
	})
	if err != nil {
		t.Fatalf("RecordExposure injected_only: %v", err)
	}
	if result.Recorded != 1 || result.Skipped != 1 || result.UseCounted != 0 {
		t.Fatalf("unexpected exposure result: %+v", result)
	}
	record, err := store.GetRecord(context.Background(), note.ID)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if record.UseCount != 0 || !record.LastUsedAt.IsZero() {
		t.Fatalf("injected_only must not increment usage: %+v", record)
	}
	usage, err := store.ListUsage(context.Background(), UsageFilter{MemoryID: note.ID, Limit: 10})
	if err != nil || len(usage) != 1 || usage[0].UseKind != UsageInjectedOnly {
		t.Fatalf("usage = %+v, %v", usage, err)
	}
}

func TestMemoryStoreCitedIncrementsUseCount(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	note, err := store.Append(AppendNoteOptions{Text: "Run memory gates before merging."})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	at := time.Date(2026, 11, 10, 13, 0, 0, 0, time.UTC)
	result, err := store.RecordExposure(context.Background(), Exposure{
		MemoryIDs: []string{note.ID, note.ID},
		SessionID: "s1", TurnID: "turn1", UseKind: UsageCited, UsedAt: at,
	})
	if err != nil {
		t.Fatalf("RecordExposure cited: %v", err)
	}
	if result.Recorded != 1 || result.UseCounted != 1 {
		t.Fatalf("unexpected cited result: %+v", result)
	}
	record, err := store.GetRecord(context.Background(), note.ID)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if record.UseCount != 1 || !record.LastUsedAt.Equal(at) {
		t.Fatalf("cited record usage = %+v", record)
	}
}
