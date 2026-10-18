package qqbot

// 已发消息账本：撤回窗口、容量、去重与移除。

import (
	"context"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

func TestSentLedgerRecallWindow(t *testing.T) {
	now := time.Now()
	var ledger sentLedger
	ledger.record("user:u1", "m1", now.Add(-3*time.Minute))
	if _, ok := ledger.lastRecallable("user:u1", now); ok {
		t.Fatal("expired message must not be recallable")
	}
	ledger.record("user:u1", "m2", now.Add(-time.Minute))
	entry, ok := ledger.lastRecallable("user:u1", now)
	if !ok || entry.MessageID != "m2" {
		t.Fatalf("entry = %+v, ok=%v", entry, ok)
	}
}

func TestSentLedgerIsolatesChats(t *testing.T) {
	now := time.Now()
	var ledger sentLedger
	ledger.record("user:u1", "m1", now)
	if _, ok := ledger.lastRecallable("user:u2", now); ok {
		t.Fatal("other chat must not see u1 messages")
	}
}

func TestSentLedgerCapacityAndDedup(t *testing.T) {
	now := time.Now()
	var ledger sentLedger
	ledger.record("user:u1", "dup", now)
	ledger.record("user:u1", "dup", now)
	for i := 0; i < sentLedgerCapacity+3; i++ {
		ledger.record("user:u1", "m"+itoa(i), now)
	}
	ledger.mu.Lock()
	count := len(ledger.entries["user:u1"])
	ledger.mu.Unlock()
	if count != sentLedgerCapacity {
		t.Fatalf("entries = %d, want %d", count, sentLedgerCapacity)
	}
}

func TestSentLedgerRemove(t *testing.T) {
	now := time.Now()
	var ledger sentLedger
	ledger.record("user:u1", "m1", now)
	ledger.remove("user:u1", "m1")
	if _, ok := ledger.lastRecallable("user:u1", now); ok {
		t.Fatal("removed message must not be recallable")
	}
}

func TestSendRecordsMessageID(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	if err := provider.Send(context.Background(), cfg, "user:u1",
		[]connectors.OutboundMessage{{Text: "hi"}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	messageID, ok := provider.LastRecallableMessage("user:u1")
	if !ok || messageID != "msg-out-1" {
		t.Fatalf("recorded = %q, ok=%v", messageID, ok)
	}
}
