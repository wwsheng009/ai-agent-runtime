package memoryruntime

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestParseMemoryCitationsStripsBlockAndFiltersAllowlist(t *testing.T) {
	answer := "Use append-only writes.\n<agent-memory-citation>\n<entry id=\"mem_1\" />\n<entry id=\"mem_other\" />\n</agent-memory-citation>"
	result := ParseMemoryCitations(answer, []string{"mem_1"})
	if result.CleanText != "Use append-only writes." {
		t.Fatalf("clean text = %q", result.CleanText)
	}
	if len(result.Cited) != 1 || result.Cited[0] != "mem_1" {
		t.Fatalf("cited = %#v", result.Cited)
	}
	if len(result.Invalid) != 1 || result.Invalid[0] != "mem_other" {
		t.Fatalf("invalid = %#v", result.Invalid)
	}
	if result.ParseErr != "" {
		t.Fatalf("unexpected parse error: %s", result.ParseErr)
	}
}

func TestParseMemoryCitationsUnclosedBlockIsRemoved(t *testing.T) {
	result := ParseMemoryCitations("answer\n<agent-memory-citation><entry id=\"mem_1\" />", []string{"mem_1"})
	if strings.Contains(result.CleanText, "agent-memory-citation") {
		t.Fatalf("hidden block leaked: %q", result.CleanText)
	}
	if result.ParseErr == "" {
		t.Fatal("expected parse error for unclosed block")
	}
}

func TestRecordAnswerExposureFallbackDoesNotCountUse(t *testing.T) {
	store, err := memorystore.New(memorystore.Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatalf("memorystore.New: %v", err)
	}
	defer store.Close()
	note, err := store.Append(memorystore.AppendNoteOptions{Text: "Use the memory gate."})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	result, err := RecordAnswerExposure(context.Background(), store, AnswerExposureInput{
		Answer:      "answer\n<agent-memory-citation><entry id=\"" + note.ID + "\" />",
		SessionID:   "s1",
		TurnID:      "turn1",
		InjectedIDs: []string{note.ID},
	})
	if err != nil {
		t.Fatalf("RecordAnswerExposure: %v", err)
	}
	if result.ParseErr == "" {
		t.Fatal("expected parse failure")
	}
	record, err := store.GetRecord(context.Background(), note.ID)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if record.UseCount != 0 {
		t.Fatalf("parse failure must not increase use_count: %+v", record)
	}
	usage, err := store.ListUsage(context.Background(), memorystore.UsageFilter{MemoryID: note.ID, Limit: 10})
	if err != nil || len(usage) != 1 || usage[0].UseKind != memorystore.UsageInjectedOnly {
		t.Fatalf("usage = %+v, %v", usage, err)
	}
}

func TestRecordAnswerExposureCitedVsInjectedOnly(t *testing.T) {
	store, err := memorystore.New(memorystore.Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatalf("memorystore.New: %v", err)
	}
	defer store.Close()
	cited, err := store.Append(memorystore.AppendNoteOptions{Text: "Use append-only writes."})
	if err != nil {
		t.Fatalf("Append cited: %v", err)
	}
	uncited, err := store.Append(memorystore.AppendNoteOptions{Text: "Run tests before merging."})
	if err != nil {
		t.Fatalf("Append uncited: %v", err)
	}
	result, err := RecordAnswerExposure(context.Background(), store, AnswerExposureInput{
		Answer:      "answer\n<agent-memory-citation><entry id=\"" + cited.ID + "\" /></agent-memory-citation>",
		SessionID:   "s1",
		InjectedIDs: []string{cited.ID, uncited.ID},
	})
	if err != nil {
		t.Fatalf("RecordAnswerExposure: %v", err)
	}
	if len(result.Cited) != 1 || result.Cited[0] != cited.ID {
		t.Fatalf("cited = %#v", result.Cited)
	}
	citedRecord, err := store.GetRecord(context.Background(), cited.ID)
	if err != nil || citedRecord.UseCount != 1 {
		t.Fatalf("cited usage = %+v, %v", citedRecord, err)
	}
	uncitedRecord, err := store.GetRecord(context.Background(), uncited.ID)
	if err != nil || uncitedRecord.UseCount != 0 {
		t.Fatalf("uncited usage = %+v, %v", uncitedRecord, err)
	}
}

func TestRecordAnswerExposureAcceptsReadIDsWithoutDoubleCounting(t *testing.T) {
	store, err := memorystore.New(memorystore.Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	readRecord, err := store.Append(memorystore.AppendNoteOptions{Text: "Read through memory_read."})
	require.NoError(t, err)

	result, err := RecordAnswerExposure(context.Background(), store, AnswerExposureInput{
		Answer:            "answer\n<agent-memory-citation><entry id=\"" + readRecord.ID + "\" /></agent-memory-citation>",
		SessionID:         "s1",
		ReadIDs:           []string{readRecord.ID},
		ReadUsageRecorded: true,
	})
	require.NoError(t, err)
	require.Equal(t, []string{readRecord.ID}, result.Cited)

	updated, err := store.GetRecord(context.Background(), readRecord.ID)
	require.NoError(t, err)
	require.Equal(t, 1, updated.UseCount, "cited usage counts once; read usage was already recorded by the tool")
	usage, err := store.ListUsage(context.Background(), memorystore.UsageFilter{MemoryID: readRecord.ID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, usage, 1)
	require.Equal(t, memorystore.UsageCited, usage[0].UseKind)
}
