package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// 调度守护进程的单实例租约（设计文档 scheduler-daemon-design.md §4）。
const (
	schedulerLeasePurpose = "schedule-scheduler"
	schedulerLeaseKey     = "global"
	// schedulerLeaseTTL 是租约寿命；续租周期取其三分之一，留三次失败预算。
	schedulerLeaseTTL = 30 * time.Second
)

// schedulerLease 持有 daemon 的 mesh 单实例租约并在后台续租。
//
// 为什么必须续租：mesh.Lease.Stale 基于 renewed_at + TTL 判定过期，若只 Acquire
// 不 Renew，30s 后租约即被视为过期，第二个 daemon 能直接接管 → 双实例同时投递。
// 续租丢失（被别的实例接管）时通过 Lost() 通知主循环停止调度，避免双投递。
type schedulerLease struct {
	paths      mesh.Paths
	owner      mesh.LeaseOwner
	ttl        time.Duration
	renewEvery time.Duration
	active     bool // 真正持租约（false = 降级无互斥运行）

	lost     chan struct{}
	stop     chan struct{}
	lostOnce sync.Once
	stopOnce sync.Once
}

// newSchedulerLease 获取单实例租约：
//   - 获取成功 → active=true 并启动后台续租，返回 ok=true；
//   - mesh 根不可用（降级）→ 无互斥继续运行（打印警告），返回 ok=true；
//   - 被其他活跃实例持有 → 返回 ok=false，调用方应待命退出（绝不接管，设计 §4）。
func newSchedulerLease() (*schedulerLease, bool) {
	return newSchedulerLeaseWithTTL(schedulerLeaseTTL)
}

// newSchedulerLeaseWithTTL 是 newSchedulerLease 的可注入 TTL 变体（测试用）。
func newSchedulerLeaseWithTTL(ttl time.Duration) (*schedulerLease, bool) {
	paths := mesh.ResolvePaths()
	lease := &schedulerLease{
		paths:      paths,
		owner:      mesh.LeaseOwner{NodeID: fmt.Sprintf("scheduled-%d", os.Getpid()), PID: os.Getpid()},
		ttl:        ttl,
		renewEvery: ttl / 3,
		lost:       make(chan struct{}),
		stop:       make(chan struct{}),
	}
	if strings.TrimSpace(paths.Root) == "" {
		fmt.Fprintln(os.Stderr, "scheduled: warning: mesh root unavailable; running without single-instance mutual exclusion")
		return lease, true
	}
	outcome := mesh.Acquire(paths, schedulerLeasePurpose, schedulerLeaseKey, lease.owner, mesh.AcquireOptions{TTL: lease.ttl})
	if outcome.Acquired {
		lease.active = true
		go lease.renewLoop()
		return lease, true
	}
	if outcome.Degraded {
		fmt.Fprintf(os.Stderr, "scheduled: warning: scheduler lease degraded (%s); running without single-instance mutual exclusion\n", outcome.Reason)
		return lease, true
	}
	holder := ""
	if outcome.Holder != nil {
		holder = fmt.Sprintf(" (holder node=%s pid=%d)", outcome.Holder.OwnerNodeID, outcome.Holder.OwnerPID)
	}
	fmt.Fprintf(os.Stderr, "scheduled: scheduler lease is held by another instance%s; standing by\n", holder)
	return lease, false
}

// renewLoop 按 TTL/3 周期续租，直到释放或被接管。
func (l *schedulerLease) renewLoop() {
	every := l.renewEvery
	if every <= 0 {
		every = l.ttl / 3
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			result := mesh.Renew(l.paths, schedulerLeasePurpose, schedulerLeaseKey, l.owner, mesh.NowUTC(), l.ttl)
			if result.Lost {
				l.lostOnce.Do(func() { close(l.lost) })
				return
			}
		}
	}
}

// Lost 在租约被其他实例接管时关闭（active=false 时永不关闭）。
func (l *schedulerLease) Lost() <-chan struct{} { return l.lost }

// release 停止续租并释放租约。
func (l *schedulerLease) release() {
	l.stopOnce.Do(func() { close(l.stop) })
	if l.active {
		mesh.Release(l.paths, schedulerLeasePurpose, schedulerLeaseKey, l.owner)
	}
}
