package skills

import (
	"errors"
	"testing"
)

// P0.5：runtime-server 桥接落盘失败必须计入 runtime_event_delivery 快照，
// 不再静默丢弃（审查 R8）。
func TestRecordRuntimeEventPersistErrorCounts(t *testing.T) {
	before, ok := SnapshotRuntimeEventDelivery()["persist_errors"].(uint64)
	if !ok {
		t.Fatalf("persist_errors missing from delivery snapshot")
	}
	recordRuntimeEventPersistError("tool.completed", "session-x", errors.New("database is locked"))
	after, _ := SnapshotRuntimeEventDelivery()["persist_errors"].(uint64)
	if after != before+1 {
		t.Fatalf("persist_errors = %d, want %d", after, before+1)
	}
}
