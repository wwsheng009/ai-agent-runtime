package storefile

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// TestConfigStoreFailedReplyRoundTripAndReopen 锁定失败回复快照的持久化与
// 重开还原（切片 2f：失败回复重放的账本层）。
func TestConfigStoreFailedReplyRoundTripAndReopen(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	statePath := filepath.Join(dir, "state.json")
	store := NewConfigStore(configPath, statePath)

	if err := store.PutFailedReply(connectors.FailedReply{}); err == nil {
		t.Fatalf("missing connector id/chat should fail")
	}
	record := connectors.FailedReply{
		ConnectorID: "c1",
		Chat:        "u1",
		MessageID:   "m1",
		Error:       "boom",
		Reply: []connectors.OutboundMessage{{
			Text:  "回答",
			Media: []connectors.Media{{Kind: "image", FileID: "f1"}},
		}},
		FailedAt: time.Now().UTC(),
	}
	if err := store.PutFailedReply(record); err != nil {
		t.Fatalf("PutFailedReply: %v", err)
	}

	got, ok := store.GetFailedReply("c1", "u1")
	if !ok || got.MessageID != "m1" || got.Error != "boom" {
		t.Fatalf("snapshot = %+v ok=%v", got, ok)
	}
	if len(got.Reply) != 1 || got.Reply[0].Text != "回答" || got.Reply[0].Media[0].FileID != "f1" {
		t.Fatalf("snapshot reply = %+v", got.Reply)
	}
	if _, ok := store.GetFailedReply("c1", "other"); ok {
		t.Fatalf("unexpected snapshot for other chat")
	}

	// 重启（重开存储实例）后仍可读。
	reopened := NewConfigStore(configPath, statePath)
	persisted, ok := reopened.GetFailedReply("c1", "u1")
	if !ok || persisted.Reply[0].Text != "回答" {
		t.Fatalf("persisted snapshot = %+v ok=%v", persisted, ok)
	}

	if err := reopened.ClearFailedReply("c1", "u1"); err != nil {
		t.Fatalf("ClearFailedReply: %v", err)
	}
	if _, ok := reopened.GetFailedReply("c1", "u1"); ok {
		t.Fatalf("snapshot should be cleared")
	}
	if err := reopened.ClearFailedReply("c1", "u1"); err != nil {
		t.Fatalf("clear should be idempotent: %v", err)
	}
	if _, ok := NewConfigStore(configPath, statePath).GetFailedReply("c1", "u1"); ok {
		t.Fatalf("cleared snapshot should not resurrect after reopen")
	}
}
