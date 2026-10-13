package main

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// TestDaemonSessionArbiterUsesChatLeaseStore 验证仲裁器用的是宿主的
// chat.SessionLeaseStore：同一会话被另一持有者持有时必须被拒（绝不抢占），
// 释放后可再获取。这是 E1「不抢占活跃会话」的正确锁域（§2.5a）。
func TestDaemonSessionArbiterUsesChatLeaseStore(t *testing.T) {
	store := chat.NewInMemoryRuntimeStore(16)
	ctx := context.Background()

	holder := newDaemonSessionArbiterWithOwner(store, "test-holder", "owner-holder")
	release, ok := holder.AcquireSession(ctx, "session-1")
	if !ok {
		t.Fatal("idle session must be grantable")
	}

	other := newDaemonSessionArbiterWithOwner(store, "test-other", "owner-other")
	if _, ok := other.AcquireSession(ctx, "session-1"); ok {
		t.Fatal("a session held by another owner must be refused (never preempt)")
	}

	release()
	if _, ok := other.AcquireSession(ctx, "session-1"); !ok {
		t.Fatal("after release the session must be re-acquirable")
	}
}

// TestDaemonSessionArbiterFailsClosedWithoutStore 验证无共享存储时 fail-closed：
// 宁可退回 mailbox，也不在没有共享租约可依据时内嵌执行。
func TestDaemonSessionArbiterFailsClosedWithoutStore(t *testing.T) {
	arbiter := newDaemonSessionArbiter(nil)
	if _, ok := arbiter.AcquireSession(context.Background(), "session-1"); ok {
		t.Fatal("nil store must be fail-closed (ok=false), never grant")
	}
	if _, ok := newDaemonSessionArbiter(nil).AcquireSession(context.Background(), "  "); ok {
		t.Fatal("blank session id must be fail-closed")
	}
}
