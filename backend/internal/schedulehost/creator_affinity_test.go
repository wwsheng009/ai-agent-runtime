package schedulehost_test

import (
	"context"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedulehost"
)

// newAffinityTask 在 store 里建一条活动任务（可指定创建者与显式时点）。
func newAffinityTaskAt(t *testing.T, store schedule.Store, creator, id string, scheduledAt time.Time) {
	t.Helper()
	record, err := schedule.CreateAfter(id, "p", "t", 1, scheduledAt.Add(-time.Second))
	if err != nil {
		t.Fatalf("CreateAfter: %v", err)
	}
	record.ScheduledAt = scheduledAt.UTC()
	task := schedule.Task{
		SessionID: creator, Record: record, Status: schedule.StatusActive,
		CreatedBySessionID: creator,
	}
	if err := store.Put(task); err != nil {
		t.Fatalf("Put: %v", err)
	}
	t.Logf("[fixture] %s creator=%s scheduledAt=%s", id, creator, scheduledAt.Format("15:04:05.000"))
}

// newAffinityTask 在 store 里建一条活动任务（可指定创建者会话）。
func newAffinityTask(t *testing.T, store schedule.Store, creator, id string, afterSeconds int64) {
	t.Helper()
	record, err := schedule.CreateAfter(id, "p", "t", afterSeconds, time.Now())
	if err != nil {
		t.Fatalf("CreateAfter: %v", err)
	}
	task := schedule.Task{
		SessionID: creator, Record: record, Status: schedule.StatusActive,
		CreatedBySessionID: creator,
	}
	if err := store.Put(task); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

// TestStoreCreatorAffinityOrdersByDue 钉住亲和源契约：活动任务的创建者会话
// 按到期时点升序（最快到期在前）去重返回；非活动任务与无创建者的任务忽略。
func TestStoreCreatorAffinityOrdersByDue(t *testing.T) {
	store, err := schedule.NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if got := (schedulehost.StoreCreatorAffinity{Store: store}).ActiveCreatorSessions(8); got != nil {
		t.Fatalf("empty store must return nil, got %v", got)
	}
	base := time.Now().UTC()
	newAffinityTaskAt(t, store, "sess-old", "task-old", base.Add(10*time.Minute))
	newAffinityTaskAt(t, store, "sess-a", "task-a", base.Add(5*time.Minute))
	newAffinityTaskAt(t, store, "sess-b", "task-b", base.Add(1*time.Minute))
	newAffinityTaskAt(t, store, "sess-a", "task-a2", base.Add(30*time.Second)) // 同创建者去重

	// 断言：最快到期的创建者在前（马上要投递 → 创建者节点优先持租走 L1）。
	got := (schedulehost.StoreCreatorAffinity{Store: store}).ActiveCreatorSessions(8)
	if len(got) != 3 || got[0] != "sess-a" || got[1] != "sess-b" || got[2] != "sess-old" {
		t.Fatalf("ActiveCreatorSessions = %v, want [sess-a sess-b sess-old]", got)
	}
	limited := (schedulehost.StoreCreatorAffinity{Store: store}).ActiveCreatorSessions(1)
	if len(limited) != 1 || limited[0] != "sess-a" {
		t.Fatalf("ActiveCreatorSessions(1) = %v, want [sess-a]", limited)
	}
}

// TestCreatorAffinityLeaderDefers 钉住 S6 集成行为：aicli 持租约后，活动任务
// 创建者会话被另一存活 aicli 宿主 → 主动让位回 standby。
func TestCreatorAffinityLeaderDefers(t *testing.T) {
	caps := []string{"mesh", schedulehost.CapabilityScheduler, schedulehost.SchedulerCapability(schedule.SupportedSchemaVersion())}
	// 创建者节点：同一 mesh 根上的另一个 aicli，宿主会话 sess-creator。
	creatorDir := t.TempDir()
	t.Setenv(mesh.EnvMeshDir, creatorDir)
	paths := mesh.ResolvePaths()

	store, err := schedule.NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	newAffinityTask(t, store, "sess-creator", "task-1", 60)

	// 本节点：同 mesh 根上的旁观者 aicli（自己没有任务）。先在"无创建者节点
	// 在岗"的状态下等它上位（亲和只发生在"创建者宿主出现"之后；用例断言
	// 上位后被亲和踢回 standby，而不是从未上位）。
	observer := newTestLeader(t, creatorDir, func(o *schedulehost.LeaderOptions) {
		o.Identity = schedulehost.DriverIdentity{NodeID: "observer-aicli", Kind: schedulehost.RoleAICLI, Priority: 3}
		o.DeferToHigherPriority = true
		o.CreatorAffinity = schedulehost.StoreCreatorAffinity{Store: store}
	})
	observer.Start(context.Background())
	waitState(t, observer, schedulehost.StateLeader, 3*time.Second)

	// 创建者节点带着 sess-creator 出现（晚于上位，模拟"另一个 aicli 后来才
	// 创建任务"的真实时序）→ 观察者必须让位。
	creatorHost := mesh.NewHost(mesh.HostConfig{Kind: "chat", Capabilities: caps, Warn: func(string, ...any) {}})
	if err := creatorHost.Start(); err != nil {
		t.Fatalf("creator host start: %v", err)
	}
	creatorHost.SetSession(&mesh.SessionInfo{ID: "sess-creator", Busy: true})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = creatorHost.Stop(ctx, mesh.StopRequest{Target: creatorHost.NodeID()})
	})
	waitState(t, observer, schedulehost.StateStandby, 5*time.Second)
	if _, ok := mesh.ReadLease(paths.LeasePath(schedulehost.LeasePurpose, schedulehost.LeaseKey)); ok {
		t.Fatal("lease must be released after creator-affinity defer")
	}
}

// TestCreatorAffinityDoesNotBlockDaemon 钉住边界：daemon（DeferToHigherPriority=false）
// 不做亲和让位——即使创建者会话宿主在别的节点，正统调度者也不受影响。
func TestCreatorAffinityDoesNotBlockDaemon(t *testing.T) {
	caps := []string{"mesh", schedulehost.CapabilityScheduler, schedulehost.SchedulerCapability(schedule.SupportedSchemaVersion())}
	daemonDir := t.TempDir()
	t.Setenv(mesh.EnvMeshDir, daemonDir)
	creatorHost := mesh.NewHost(mesh.HostConfig{Kind: "chat", Capabilities: caps, Warn: func(string, ...any) {}})
	if err := creatorHost.Start(); err != nil {
		t.Fatalf("creator host start: %v", err)
	}
	creatorHost.SetSession(&mesh.SessionInfo{ID: "sess-creator", Busy: true})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = creatorHost.Stop(ctx, mesh.StopRequest{Target: creatorHost.NodeID()})
	})

	store, err := schedule.NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	newAffinityTask(t, store, "sess-creator", "task-1", 60)

	daemon := newTestLeader(t, daemonDir, func(o *schedulehost.LeaderOptions) {
		o.Identity = schedulehost.DriverIdentity{NodeID: "daemon-driver", Kind: schedulehost.RoleDaemon, Priority: 1}
		o.CreatorAffinity = schedulehost.StoreCreatorAffinity{Store: store}
	})
	daemon.Start(context.Background())
	waitState(t, daemon, schedulehost.StateLeader, 3*time.Second)
	// 多等一个轮询周期确认没有意外让位。
	time.Sleep(300 * time.Millisecond)
	if st := daemon.Status(); st.State != schedulehost.StateLeader {
		t.Fatalf("daemon must keep the lease despite creator affinity, state=%q", st.State)
	}
}
