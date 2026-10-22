package chat

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSessionCellStorePutGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cells.sqlite")
	store, err := OpenSessionCellStore(path, 0, 0)
	if err != nil {
		t.Fatalf("OpenSessionCellStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	row := SessionCellRow{
		SessionID: "session-1",
		CellID:    42,
		Revision:  7,
		SourceFP:  0xABCDEF0123456789,
		RowsAtGen: 17,
		Gen:       3,
		Encoding:  "json-v1",
		Payload:   []byte(`{"cell":{"source":"hi"}}`),
	}
	if err := store.PutCell(ctx, row); err != nil {
		t.Fatalf("PutCell: %v", err)
	}

	got, ok, err := store.GetCell(ctx, "session-1", 42, 7)
	if err != nil || !ok {
		t.Fatalf("GetCell ok=%v err=%v", ok, err)
	}
	if got.CellID != row.CellID || got.Revision != row.Revision || got.SourceFP != row.SourceFP ||
		got.RowsAtGen != row.RowsAtGen || got.Gen != row.Gen || got.Encoding != row.Encoding ||
		string(got.Payload) != string(row.Payload) {
		t.Fatalf("round-trip mismatch:\n got=%+v\nwant=%+v", got, row)
	}

	// 身份不同（revision 不同）必须 miss。
	if _, ok, err := store.GetCell(ctx, "session-1", 42, 8); err != nil || ok {
		t.Fatalf("revision mismatch must miss: ok=%v err=%v", ok, err)
	}

	count, err := store.SessionCellCount(ctx, "session-1")
	if err != nil || count != 1 {
		t.Fatalf("SessionCellCount=%d err=%v", count, err)
	}
	stats := store.Stats()
	if stats.Puts != 1 || stats.Gets != 2 || stats.Misses != 1 || stats.Errors != 0 {
		t.Fatalf("stats=%+v", stats)
	}

	// 覆写同一身份：行数不变。
	row.RowsAtGen = 19
	if err := store.PutCell(ctx, row); err != nil {
		t.Fatalf("PutCell(overwrite): %v", err)
	}
	if count, _ := store.SessionCellCount(ctx, "session-1"); count != 1 {
		t.Fatalf("overwrite 后仍应为 1 行，实际 %d", count)
	}

	// 重开仍可读（持久化验证）。
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := OpenSessionCellStore(path, 0, 0)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if got, ok, err := reopened.GetCell(ctx, "session-1", 42, 7); err != nil || !ok || got.RowsAtGen != 19 {
		t.Fatalf("reopen read: ok=%v err=%v rows=%d", ok, err, got.RowsAtGen)
	}
}

func TestSessionCellStoreRejectsEmptyPayload(t *testing.T) {
	store, err := OpenSessionCellStore(filepath.Join(t.TempDir(), "cells.sqlite"), 0, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.PutCell(context.Background(), SessionCellRow{
		SessionID: "s", CellID: 1, Revision: 1, Encoding: "json-v1",
	}); err == nil {
		t.Fatal("空 payload 应被拒绝")
	}
	if stats := store.Stats(); stats.Errors != 1 || stats.Puts != 1 {
		t.Fatalf("错误计数口径不符：%+v", stats)
	}
}
