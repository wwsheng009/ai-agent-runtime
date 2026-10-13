package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// daemonSessionLeaseOwnerKind 标识 daemon 作为会话执行租约的持有者类型。
const daemonSessionLeaseOwnerKind = "scheduled-daemon"

// daemonSessionArbiter 用**与宿主同一把**会话执行租约（chat.SessionLeaseStore，
// 共享 session_runtime.sqlite）实现 E1 仲裁。
//
// 关键：绝不能用 internal/mesh 的文件租约。宿主（runtime-server / aicli）执行会话用的是
// chat.SessionLeaseStore（DB 租约），与 mesh 是**独立锁域**——mesh 查不到宿主是否在跑该会话
// 的 turn，会导致 daemon 误判空闲并并发执行。详见
// docs/automation-tasks/runtimehost-executor-extraction-plan.md §2.5a。
//
// store 为 nil 时 **fail-closed**：恒返回 ok=false。宁可退回持久 mailbox，也不在没有共享
// 租约可依据时内嵌执行。
type daemonSessionArbiter struct {
	store     chat.SessionLeaseStore
	ownerKind string
	ownerID   string
}

// newDaemonSessionArbiter 构造生产仲裁器（owner 身份 = 类型 + 进程 pid）。
func newDaemonSessionArbiter(store chat.SessionLeaseStore) *daemonSessionArbiter {
	return newDaemonSessionArbiterWithOwner(store, daemonSessionLeaseOwnerKind,
		fmt.Sprintf("%s-%d", daemonSessionLeaseOwnerKind, os.Getpid()))
}

// newDaemonSessionArbiterWithOwner 是显式 owner 变体（测试用：模拟不同持有者）。
func newDaemonSessionArbiterWithOwner(store chat.SessionLeaseStore, ownerKind, ownerID string) *daemonSessionArbiter {
	return &daemonSessionArbiter{store: store, ownerKind: ownerKind, ownerID: ownerID}
}

// AcquireSession 尝试获取该会话的执行租约（与宿主同源）。拿不到（被宿主持有 / 存储不可用）
// 返回 ok=false，调用方必须退回兜底路径，绝不抢占。
func (a *daemonSessionArbiter) AcquireSession(ctx context.Context, sessionID string) (func(), bool) {
	sessionID = strings.TrimSpace(sessionID)
	if a == nil || a.store == nil || sessionID == "" {
		return func() {}, false
	}
	handle, err := chat.AcquireSessionLease(ctx, a.store, chat.LeaseRequest{
		SessionID: sessionID,
		OwnerID:   a.ownerID,
		OwnerKind: a.ownerKind,
		PID:       os.Getpid(),
		Hostname:  daemonHostname(),
	})
	if err != nil {
		return func() {}, false
	}
	return func() { _ = handle.Release(context.Background()) }, true
}

func daemonHostname() string {
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return host
}
