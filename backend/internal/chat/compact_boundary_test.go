package chat

import (
	"context"
	"testing"
	"time"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// newCompactBoundaryStore 构造独立的 SQLite 存储与会话管理器。
func newCompactBoundaryStore(t *testing.T) *SessionManager {
	t.Helper()
	storage, err := NewSQLiteSessionStorage(DefaultPersistentSessionStorageConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("new sqlite storage: %v", err)
	}
	manager := NewSessionManager(storage, &SessionManagerConfig{
		TTL:             24 * time.Hour,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	t.Cleanup(manager.Stop)
	return manager
}

func compactBoundaryTestSession(userID string, seedCount int) *Session {
	session := NewSession(userID)
	seed := make([]runtimetypes.Message, 0, seedCount)
	for index := 0; index < seedCount; index++ {
		if index%2 == 0 {
			seed = append(seed, *runtimetypes.NewUserMessage("boundary user"))
		} else {
			seed = append(seed, *runtimetypes.NewAssistantMessage("boundary assistant"))
		}
	}
	session.ReplaceHistory(seed)
	summary := *runtimetypes.NewUserMessage("Compacted context from earlier turns: boundary summary")
	summary.Metadata["context_stage"] = "compaction"
	summary.Metadata["message_id"] = "boundary-checkpoint-id"
	summary.Metadata["segment_start"] = 0
	summary.Metadata["segment_end"] = seedCount
	summary.Metadata["replacement_tail_message_id"] = "boundary-tail-anchor"
	session.ReplaceHistory([]runtimetypes.Message{summary})
	return session
}

// TestCompactBoundaryMirroredOnSave 写入带 compaction checkpoint 的会话后，
// metadata context 里必须出现 canonical 层权威边界；重新 Load（新会话对象，
// 模拟另一进程恢复）后仍能读到。
func TestCompactBoundaryMirroredOnSave(t *testing.T) {
	manager := newCompactBoundaryStore(t)
	ctx := context.Background()

	created, err := manager.Create(ctx, "boundary-tester")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	session := compactBoundaryTestSession(created.UserID, 12)
	session.ID = created.ID
	if err := manager.Update(ctx, session); err != nil {
		t.Fatalf("persist compacted session: %v", err)
	}

	if _, ok := session.GetContext(ContextCompactBoundary); !ok {
		t.Fatal("in-memory session missing compact boundary after save")
	}

	reloaded, err := manager.Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	value, ok := reloaded.GetContext(ContextCompactBoundary)
	if !ok {
		t.Fatalf("reloaded session lost compact boundary; context=%v", reloaded.Metadata.Context)
	}
	boundary, ok := DecodeCompactBoundaryValue(value)
	if !ok {
		t.Fatalf("decode boundary failed: %#v", value)
	}
	if boundary.MessageID != "boundary-checkpoint-id" {
		t.Fatalf("boundary message_id = %q, want checkpoint id", boundary.MessageID)
	}
	if boundary.ReplacementTailMessageID != "boundary-tail-anchor" {
		t.Fatalf("boundary tail anchor = %q, want anchor id", boundary.ReplacementTailMessageID)
	}
	if boundary.SegmentEnd != 12 {
		t.Fatalf("boundary segment_end = %d, want 12", boundary.SegmentEnd)
	}
}

// TestCompactBoundaryClearedWhenCheckpointGone 回退（backtrack）到 checkpoint
// 之前再持久化：装载了 history 且无 checkpoint 的写入必须清除 boundary，
// 恢复端不再拿到过期边界。
func TestCompactBoundaryClearedWhenCheckpointGone(t *testing.T) {
	manager := newCompactBoundaryStore(t)
	ctx := context.Background()

	created, err := manager.Create(ctx, "boundary-tester")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	session := compactBoundaryTestSession(created.UserID, 12)
	session.ID = created.ID
	if err := manager.Update(ctx, session); err != nil {
		t.Fatalf("persist compacted session: %v", err)
	}

	// backtrack 语义：替换为不含 checkpoint 的更短 history + HistoryTruncated。
	rewound := compactBoundaryTestSession(created.UserID, 0)
	rewound.ID = created.ID
	rewound.ReplaceHistory([]runtimetypes.Message{
		*runtimetypes.NewUserMessage("rewound user"),
		*runtimetypes.NewAssistantMessage("rewound assistant"),
	})
	rewound.HistoryTruncated = true
	if err := manager.Update(ctx, rewound); err != nil {
		t.Fatalf("persist rewind: %v", err)
	}
	if _, ok := rewound.GetContext(ContextCompactBoundary); ok {
		t.Fatal("boundary survived a checkpoint-free full-history write")
	}

	reloaded, err := manager.Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("reload rewound session: %v", err)
	}
	if _, ok := reloaded.GetContext(ContextCompactBoundary); ok {
		t.Fatal("reloaded session still carries a stale compact boundary after rewind")
	}
}

// TestCompactBoundaryPreservedByMetadataShellWrite 元数据壳写入
// （HistoryLoaded=false）不得清除或覆盖已镜像的 boundary。
func TestCompactBoundaryPreservedByMetadataShellWrite(t *testing.T) {
	manager := newCompactBoundaryStore(t)
	ctx := context.Background()

	created, err := manager.Create(ctx, "boundary-tester")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	session := compactBoundaryTestSession(created.UserID, 12)
	session.ID = created.ID
	if err := manager.Update(ctx, session); err != nil {
		t.Fatalf("persist compacted session: %v", err)
	}

	shell, err := manager.GetMetadata(ctx, session.ID)
	if err != nil {
		t.Fatalf("load metadata shell: %v", err)
	}
	if shell.HistoryLoaded {
		t.Fatal("GetMetadata shell unexpectedly carries loaded history")
	}
	shell.UpdateTitle("boundary title")
	if err := manager.Update(ctx, shell); err != nil {
		t.Fatalf("persist metadata shell: %v", err)
	}

	reloaded, err := manager.Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if _, ok := reloaded.GetContext(ContextCompactBoundary); !ok {
		t.Fatal("metadata-only write erased the compact boundary")
	}
}

// TestDecodeCompactBoundaryValueLooseNumbers JSON 读回后数字是 float64：
// 解码必须收敛为 int 并保留各字段。
func TestDecodeCompactBoundaryValueLooseNumbers(t *testing.T) {
	decoded, ok := DecodeCompactBoundaryValue(map[string]interface{}{
		"message_id":                  "checkpoint",
		"replacement_tail_message_id": "tail",
		"segment_start":               float64(0),
		"segment_end":                 float64(4532),
	})
	if !ok {
		t.Fatal("decode failed for a float64 context map")
	}
	if decoded.SegmentEnd != 4532 || decoded.SegmentStart != 0 {
		t.Fatalf("decoded numbers = %+v, want segment_end=4532", decoded)
	}
	if _, ok := DecodeCompactBoundaryValue("garbage"); ok {
		t.Fatal("garbage value must not decode")
	}
	if _, ok := DecodeCompactBoundaryValue(map[string]interface{}{}); ok {
		t.Fatal("empty map must not decode")
	}
}
