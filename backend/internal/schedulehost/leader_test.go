package schedulehost_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedulehost"
)

// recordingDeliverer 只记录调用（领导权测试不关心投递内容）。
type recordingDeliverer struct {
	mu    sync.Mutex
	calls int
}

func (d *recordingDeliverer) Deliver(context.Context, string, string) error {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	return nil
}

// newTestLeader 构造一个测试用 Leader。meshDir 为空时新建临时网格根；
// 需要"同网格双实例"的用例传入同一个 dir。
func newTestLeader(t *testing.T, meshDir string, mutate func(*schedulehost.LeaderOptions)) *schedulehost.Leader {
	t.Helper()
	if meshDir == "" {
		meshDir = t.TempDir()
	}
	t.Setenv(mesh.EnvMeshDir, meshDir)
	store, err := schedule.NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	opts := schedulehost.LeaderOptions{
		Store:             store,
		Service:           schedule.NewService(store, schedule.SystemClock),
		Deliverer:         &recordingDeliverer{},
		Mode:              "delegate",
		MaxAttempts:       3,
		FallbackToMailbox: true,
		Identity:          schedulehost.DriverIdentity{NodeID: "test-driver", Kind: schedulehost.RoleDaemon, Priority: 1},
		LeaseTTL:          1200 * time.Millisecond,
		StandbyRetry:      50 * time.Millisecond,
		Degraded:          schedulehost.DegradedStandby,
		Logf:              func(string, ...any) {},
	}
	if mutate != nil {
		mutate(&opts)
	}
	leader, err := schedulehost.NewLeader(opts)
	if err != nil {
		t.Fatalf("NewLeader: %v", err)
	}
	t.Cleanup(func() { stopLeader(t, leader) })
	return leader
}

func stopLeader(t *testing.T, l *schedulehost.Leader) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	l.Stop(ctx)
}

func waitState(t *testing.T, l *schedulehost.Leader, want schedulehost.State, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if l.Status().State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := l.Status()
	t.Fatalf("state = %q, want %q (status=%+v)", st.State, want, st)
}

func TestNewLeaderValidatesDependencies(t *testing.T) {
	store, err := schedule.NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	if _, err := schedulehost.NewLeader(schedulehost.LeaderOptions{Deliverer: &recordingDeliverer{}}); err == nil {
		t.Fatal("missing store must fail")
	}
	if _, err := schedulehost.NewLeader(schedulehost.LeaderOptions{Store: store}); err == nil {
		t.Fatal("missing deliverer must fail")
	}
}

func TestLeaderAcquiresDrivesAndReleases(t *testing.T) {
	leader := newTestLeader(t, "", nil)
	leader.Start(context.Background())
	waitState(t, leader, schedulehost.StateLeader, 3*time.Second)
	if st := leader.Status(); !st.Driving {
		t.Fatalf("leader must be driving: %+v", st)
	}
	paths := mesh.ResolvePaths()
	lease, ok := mesh.ReadLease(paths.LeasePath(schedulehost.LeasePurpose, schedulehost.LeaseKey))
	if !ok || lease.OwnerNodeID != "test-driver" {
		t.Fatalf("lease = %+v ok=%v, want owner test-driver", lease, ok)
	}

	stopLeader(t, leader)
	waitState(t, leader, schedulehost.StateQuiesced, 3*time.Second)
	if _, ok := mesh.ReadLease(paths.LeasePath(schedulehost.LeasePurpose, schedulehost.LeaseKey)); ok {
		t.Fatal("lease must be released after Stop")
	}
}

func TestLeaderStandsByThenTakesOverExpiredLease(t *testing.T) {
	leader := newTestLeader(t, "", nil)
	paths := mesh.ResolvePaths()
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	other := mesh.LeaseOwner{NodeID: "incumbent", PID: os.Getpid()}
	if outcome := mesh.Acquire(paths, schedulehost.LeasePurpose, schedulehost.LeaseKey, other, mesh.AcquireOptions{TTL: time.Second}); !outcome.Acquired {
		t.Fatalf("pre-acquire: %+v", outcome)
	}

	leader.Start(context.Background())
	waitState(t, leader, schedulehost.StateStandby, 2*time.Second)
	// 前任 TTL 过期后可回收：待命者应在轮询周期内接管。
	waitState(t, leader, schedulehost.StateLeader, 5*time.Second)
}

func TestLeaderDemotesWhenLeaseStolen(t *testing.T) {
	leader := newTestLeader(t, "", func(o *schedulehost.LeaderOptions) { o.LeaseTTL = 1500 * time.Millisecond })
	leader.Start(context.Background())
	waitState(t, leader, schedulehost.StateLeader, 3*time.Second)

	paths := mesh.ResolvePaths()
	thief := mesh.LeaseOwner{NodeID: "thief", PID: os.Getpid()}
	if outcome := mesh.Acquire(paths, schedulehost.LeasePurpose, schedulehost.LeaseKey, thief, mesh.AcquireOptions{TTL: 5 * time.Second, Takeover: true}); !outcome.Acquired {
		t.Fatalf("steal: %+v", outcome)
	}

	waitState(t, leader, schedulehost.StateStandby, 4*time.Second)
	if st := leader.Status(); st.Driving {
		t.Fatalf("stolen leader must stop driving: %+v", st)
	}
	lease, ok := mesh.ReadLease(paths.LeasePath(schedulehost.LeasePurpose, schedulehost.LeaseKey))
	if !ok || lease.OwnerNodeID != "thief" {
		t.Fatalf("lease = %+v ok=%v, want thief (stale leader must not clobber)", lease, ok)
	}
}

func TestLeaderDegradedPolicies(t *testing.T) {
	t.Run("standby", func(t *testing.T) {
		leader := newTestLeader(t, "", func(o *schedulehost.LeaderOptions) {
			o.Paths = &mesh.Paths{}
			o.Degraded = schedulehost.DegradedStandby
		})
		leader.Start(context.Background())
		waitState(t, leader, schedulehost.StateStandby, 2*time.Second)
		if st := leader.Status(); st.Driving {
			t.Fatalf("fail-closed standby must not drive: %+v", st)
		}
	})
	t.Run("standalone", func(t *testing.T) {
		leader := newTestLeader(t, "", func(o *schedulehost.LeaderOptions) {
			o.Paths = &mesh.Paths{}
			o.Degraded = schedulehost.DegradedStandalone
		})
		leader.Start(context.Background())
		waitState(t, leader, schedulehost.StateDegraded, 2*time.Second)
		if st := leader.Status(); !st.Driving {
			t.Fatalf("standalone degraded must drive: %+v", st)
		}
	})
}

// TestTwoLeadersSingleHolderAndFailover 把验收场景放进单进程双实例：
// 同一时刻只有一个持租约者；持有者停止后，待命者自动接管（等价于双进程实验）。
func TestTwoLeadersSingleHolderAndFailover(t *testing.T) {
	meshDir := t.TempDir()
	first := newTestLeader(t, meshDir, func(o *schedulehost.LeaderOptions) {
		o.Identity.NodeID = "driver-one"
		o.LeaseTTL = 1200 * time.Millisecond
	})
	second := newTestLeader(t, meshDir, func(o *schedulehost.LeaderOptions) {
		o.Identity.NodeID = "driver-two"
		o.LeaseTTL = 1200 * time.Millisecond
	})

	first.Start(context.Background())
	waitState(t, first, schedulehost.StateLeader, 3*time.Second)
	second.Start(context.Background())
	waitState(t, second, schedulehost.StateStandby, 2*time.Second)

	stopLeader(t, first)
	waitState(t, second, schedulehost.StateLeader, 5*time.Second)
}

// TestLeaderStoreCompatCheckBlocksAndReleases 钉住 S3（§4.2）版本握手：
// 自检失败 → 不参与领导权（绝不持租约）；恢复 → 自动上位；在任期间失配 →
// 立即释放租约让位（状态 incompatible）。
func TestLeaderStoreCompatCheckBlocksAndReleases(t *testing.T) {
	var blocked atomic.Bool
	blocked.Store(true)
	leader := newTestLeader(t, "", func(opts *schedulehost.LeaderOptions) {
		opts.StoreCompatCheck = func() error {
			if blocked.Load() {
				return fmt.Errorf("schedule store schema v99 is newer than this binary (v6)")
			}
			return nil
		}
	})
	leader.Start(context.Background())
	waitState(t, leader, schedulehost.StateIncompatible, 3*time.Second)
	paths := mesh.ResolvePaths()
	if _, ok := mesh.ReadLease(paths.LeasePath(schedulehost.LeasePurpose, schedulehost.LeaseKey)); ok {
		t.Fatal("incompatible leader must not hold the lease")
	}
	blocked.Store(false)
	waitState(t, leader, schedulehost.StateLeader, 5*time.Second)
	blocked.Store(true)
	waitState(t, leader, schedulehost.StateIncompatible, 5*time.Second)
	if _, ok := mesh.ReadLease(paths.LeasePath(schedulehost.LeasePurpose, schedulehost.LeaseKey)); ok {
		t.Fatal("lease must be released once the store compat check fails while leading")
	}
}

// TestLeaderCountsTakeoverFromForeignLease 钉住 S4b 观测：首次空租约上位
// 不计接管；从他节点（含过期残留）的租约上位累计一次。
func TestLeaderCountsTakeoverFromForeignLease(t *testing.T) {
	t.Run("fresh lease is not a takeover", func(t *testing.T) {
		leader := newTestLeader(t, "", nil)
		leader.Start(context.Background())
		waitState(t, leader, schedulehost.StateLeader, 3*time.Second)
		if got := leader.Status().Takeovers; got != 0 {
			t.Fatalf("Takeovers = %d, want 0 on fresh lease", got)
		}
		if got := leader.Status().Epoch; got != 1 {
			t.Fatalf("Epoch = %d, want 1 on a fresh lease", got)
		}
	})
	t.Run("expired foreign lease counts", func(t *testing.T) {
		dir := t.TempDir()
		leader := newTestLeader(t, dir, nil)
		paths := mesh.ResolvePaths()
		foreign := mesh.LeaseOwner{NodeID: "foreign-node", PID: os.Getpid()}
		if outcome := mesh.Acquire(paths, schedulehost.LeasePurpose, schedulehost.LeaseKey, foreign, mesh.AcquireOptions{TTL: time.Second}); !outcome.Acquired {
			t.Fatalf("pre-acquire: %+v", outcome)
		}
		leader.Start(context.Background())
		waitState(t, leader, schedulehost.StateLeader, 5*time.Second)
		if got := leader.Status().Takeovers; got != 1 {
			t.Fatalf("Takeovers = %d, want 1 after taking over an expired foreign lease", got)
		}
		if got := leader.Status().Epoch; got != 2 {
			t.Fatalf("Epoch = %d, want 2 after taking over a foreign lease (epoch 1)", got)
		}
	})
}

// writeLiveSchedulerNode 在给定 mesh 根写一条"存活 + 带调度能力位"的节点档案
// （S5 优先级让位测试的 fixture：pid 用本进程，心跳新鲜 → 视图判定 live）。
func writeLiveSchedulerNode(t *testing.T, paths mesh.Paths, kind string, capabilities []string) string {
	t.Helper()
	if err := os.MkdirAll(paths.Nodes, 0o700); err != nil {
		t.Fatalf("mkdir nodes: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	nodeID := "node-" + kind + "-fixture"
	payload := map[string]any{
		"schema_version": 2,
		"node_id":        nodeID,
		"pid":            os.Getpid(),
		"kind":           kind,
		"capabilities":   capabilities,
		"liveness": map[string]any{
			"started_at":        now,
			"updated_at":        now,
			"heartbeat_at":      now,
			"heartbeat_ttl_sec": 90,
			"state":             "live",
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal fixture record: %v", err)
	}
	if err := os.WriteFile(paths.NodePath(nodeID), data, 0o600); err != nil {
		t.Fatalf("write fixture record: %v", err)
	}
	return nodeID
}

// TestLeaderDefersToHigherPriorityScheduler 钉住 S5 让位 v2（不抢）：更高优先级
// 的在岗调度者存在时，aicli 角色（DeferToHigherPriority）保持 standby、绝不
// 持有租约；对方离开后自动上位。
func TestLeaderDefersToHigherPriorityScheduler(t *testing.T) {
	leader := newTestLeader(t, "", func(opts *schedulehost.LeaderOptions) {
		opts.DeferToHigherPriority = true
		opts.Identity.Kind = schedulehost.RoleAICLI
	})
	paths := mesh.ResolvePaths()
	writeLiveSchedulerNode(t, paths, "scheduled", []string{
		"mesh", schedulehost.CapabilityScheduler, schedulehost.SchedulerCapability(schedule.SupportedSchemaVersion()),
	})

	leader.Start(context.Background())
	time.Sleep(400 * time.Millisecond) // 覆盖数个待命轮询周期（50ms）
	if st := leader.Status(); st.State != schedulehost.StateStandby {
		t.Fatalf("state = %q, want standby while a daemon is online (%+v)", st.State, st)
	}
	if _, ok := mesh.ReadLease(paths.LeasePath(schedulehost.LeasePurpose, schedulehost.LeaseKey)); ok {
		t.Fatal("a deferring leader must not hold the lease")
	}

	if err := os.Remove(paths.NodePath("node-scheduled-fixture")); err != nil {
		t.Fatalf("remove fixture record: %v", err)
	}
	waitState(t, leader, schedulehost.StateLeader, 3*time.Second)
}

// TestLeaderYieldsWhenHigherPriorityAppears 钉住让位 v2（主动让）：持租约期间
// 更高优先级者上线 → 一个续租周期内释放租约并回到 standby（不等 TTL）。
func TestLeaderYieldsWhenHigherPriorityAppears(t *testing.T) {
	leader := newTestLeader(t, "", func(opts *schedulehost.LeaderOptions) {
		opts.DeferToHigherPriority = true
		opts.Identity.Kind = schedulehost.RoleAICLI
	})
	leader.Start(context.Background())
	waitState(t, leader, schedulehost.StateLeader, 3*time.Second)

	paths := mesh.ResolvePaths()
	writeLiveSchedulerNode(t, paths, "scheduled", []string{
		"mesh", schedulehost.CapabilityScheduler, schedulehost.SchedulerCapability(schedule.SupportedSchemaVersion()),
	})
	// 续租周期 = TTL/3（1200ms/3 = 400ms）：一个周期内应让位。
	waitState(t, leader, schedulehost.StateStandby, 5*time.Second)
	if _, ok := mesh.ReadLease(paths.LeasePath(schedulehost.LeasePurpose, schedulehost.LeaseKey)); ok {
		t.Fatal("lease must be released after yielding to a higher-priority scheduler")
	}
}
