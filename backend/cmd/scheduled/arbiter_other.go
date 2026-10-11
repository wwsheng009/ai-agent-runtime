//go:build !windows

package main

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// daemonSessionArbiter 非 Windows 构建：与 windows 版同语义（mesh 租约跨平台）。
type daemonSessionArbiter struct {
	paths mesh.Paths
	owner mesh.LeaseOwner
}

func newDaemonSessionArbiter(paths mesh.Paths) *daemonSessionArbiter {
	return &daemonSessionArbiter{paths: paths}
}

func (a *daemonSessionArbiter) AcquireSession(_ context.Context, sessionID string) (func(), bool) {
	if a == nil || a.paths.Root == "" {
		return func() {}, false
	}
	key := "session-" + sessionID
	outcome := mesh.Acquire(a.paths, mesh.LeasePurposeSession, key, a.owner, mesh.AcquireOptions{})
	if outcome.Err != nil || !outcome.Acquired {
		return func() {}, false
	}
	return func() { mesh.Release(a.paths, mesh.LeasePurposeSession, key, a.owner) }, true
}
