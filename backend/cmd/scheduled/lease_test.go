package main

import (
	"os"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// TestSchedulerLeaseRenewsAcrossTTL 验证续租循环让租约跨过原始 TTL 仍然新鲜：
// 没有续租时，超过 TTL 后 ReadLease().Stale 会为 true（第二个实例即可接管）。
func TestSchedulerLeaseRenewsAcrossTTL(t *testing.T) {
	t.Setenv(mesh.EnvMeshDir, t.TempDir())

	ttl := 400 * time.Millisecond
	lease, ok := newSchedulerLeaseWithTTL(ttl)
	if !ok || !lease.active {
		t.Fatalf("first instance must acquire: ok=%v active=%v", ok, lease.active)
	}
	defer lease.release()

	// 等待超过一个 TTL：renewLoop（ttl/3）应已多次刷新 renewed_at。
	time.Sleep(ttl + ttl/2)

	paths := mesh.ResolvePaths()
	stored, ok := mesh.ReadLease(paths.LeasePath(schedulerLeasePurpose, schedulerLeaseKey))
	if !ok {
		t.Fatal("lease file missing while daemon is running")
	}
	if stored.Stale(mesh.NowUTC()) {
		t.Fatal("lease went stale: renewal loop did not refresh renewed_at")
	}
}

// TestSchedulerLeaseStandsByWhenHeld 验证租约被别的实例持有时不接管（ok=false）。
func TestSchedulerLeaseStandsByWhenHeld(t *testing.T) {
	t.Setenv(mesh.EnvMeshDir, t.TempDir())

	paths := mesh.ResolvePaths()
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	other := mesh.LeaseOwner{NodeID: "other-instance", PID: os.Getpid()}
	outcome := mesh.Acquire(paths, schedulerLeasePurpose, schedulerLeaseKey, other, mesh.AcquireOptions{TTL: time.Minute})
	if !outcome.Acquired {
		t.Fatalf("pre-acquire with a different owner failed: %+v", outcome)
	}
	defer mesh.Release(paths, schedulerLeasePurpose, schedulerLeaseKey, other)

	lease, ok := newSchedulerLease()
	if ok || lease.active {
		lease.release()
		t.Fatal("must stand by (ok=false, active=false) when another instance holds the lease")
	}
}

// TestSchedulerLeaseReleaseAllowsReacquire 验证释放后租约文件被移除、可再次获取。
func TestSchedulerLeaseReleaseAllowsReacquire(t *testing.T) {
	t.Setenv(mesh.EnvMeshDir, t.TempDir())

	first, ok := newSchedulerLease()
	if !ok || !first.active {
		t.Fatalf("first acquire failed: ok=%v active=%v", ok, first.active)
	}
	paths := mesh.ResolvePaths()
	path := paths.LeasePath(schedulerLeasePurpose, schedulerLeaseKey)
	if _, ok := mesh.ReadLease(path); !ok {
		t.Fatal("lease file missing after acquire")
	}

	first.release()
	if _, ok := mesh.ReadLease(path); ok {
		t.Fatal("lease file must be removed after release")
	}

	second, ok := newSchedulerLease()
	if !ok || !second.active {
		t.Fatalf("lease must be re-acquirable after release: ok=%v active=%v", ok, second.active)
	}
	second.release()
}
