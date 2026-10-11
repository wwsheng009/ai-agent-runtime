//go:build windows

package main

import (
	"context"
	"os"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// daemonSessionArbiter 用 mesh 的 session 排他租约实现 E1 仲裁：
// `session-<sid>` 租约被交互宿主（aicli/runtime-server）持有时 AcquireSession
// 返回 ok=false——daemon 绝不抢占活跃会话（embedded-execution-design.md §5）。
type daemonSessionArbiter struct {
	paths mesh.Paths
	owner mesh.LeaseOwner
}

func newDaemonSessionArbiter(paths mesh.Paths) *daemonSessionArbiter {
	return &daemonSessionArbiter{
		paths: paths,
		owner: mesh.LeaseOwner{
			NodeID: "scheduled-" + os.Getenv("AICLI_SCHEDULE_INSTANCE"),
			PID:    os.Getpid(),
		},
	}
}

// AcquireSession 尝试获取 session 租约（purpose="session"，key="session-<sid>"）。
// 与 mesh.Host 的租约约定一致；TTL 30s 由调用方在执行完后立即 Release，
// 不依赖续租。
func (a *daemonSessionArbiter) AcquireSession(_ context.Context, sessionID string) (func(), bool) {
	if a == nil || a.paths.Root == "" {
		return func() {}, false
	}
	key := "session-" + sessionID
	outcome := mesh.Acquire(a.paths, mesh.LeasePurposeSession, key, a.owner, mesh.AcquireOptions{TTL: 30 * time.Second})
	if outcome.Err != nil || !outcome.Acquired {
		return func() {}, false
	}
	return func() { mesh.Release(a.paths, mesh.LeasePurposeSession, key, a.owner) }, true
}
