package runtimehost

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// fakeDeps 构造一套纯内存的执行核依赖（不依赖 runtimeapi）。
func fakeDeps(t *testing.T) Deps {
	t.Helper()
	return fakeDepsWithLeaseStore(t, chat.NewInMemoryRuntimeStore(16))
}

// fakeDepsWithLeaseStore 同上，但允许注入共享租约存储（用于观察租约持有状态）。
func fakeDepsWithLeaseStore(t *testing.T, leaseStore chat.SessionLeaseStore) Deps {
	t.Helper()
	store := chat.NewInMemoryRuntimeStore(16)
	acquire := func(ctx context.Context, sessionID string) (*chat.SessionLeaseHandle, error) {
		return chat.AcquireSessionLease(ctx, leaseStore, chat.LeaseRequest{
			SessionID: sessionID,
			OwnerID:   "runtimehost-test",
			OwnerKind: "test",
			PID:       os.Getpid(),
		})
	}
	return Deps{
		SessionStorage:            chat.NewInMemoryStorage(),
		RuntimeStore:              store,
		EventStore:                store,
		AcquireSessionLeaseNoWait: acquire,
		AcquireSessionLease:       acquire,
		PrepareAgent: func(context.Context, string, SessionContext) (*agent.Agent, *runtimecfg.RuntimeConfig, any) {
			return agent.NewAgent(&agent.Config{Name: "test-agent", Provider: "test-provider", Model: "test-model"}, nil), nil, nil
		},
		BuildLoopConfig: func(string, SessionContext, *runtimecfg.RuntimeConfig, any) *agent.LoopReActConfig {
			return &agent.LoopReActConfig{}
		},
	}
}

// TestBuildSessionActorWithFakeDeps 验证执行核工厂可脱离 runtimeapi 组装 actor
// （E3.1b-2b-iii 验收：runtimehost 包内单测工厂）。
func TestBuildSessionActorWithFakeDeps(t *testing.T) {
	actor, err := BuildSessionActor("session-runtimehost-test", fakeDeps(t))
	if err != nil {
		t.Fatalf("BuildSessionActor: %v", err)
	}
	if actor == nil {
		t.Fatal("actor must not be nil")
	}
}

// TestBuildSessionActorRequiresDeps 验证缺依赖时 fail-closed。
func TestBuildSessionActorRequiresDeps(t *testing.T) {
	if _, err := BuildSessionActor("s", Deps{}); err == nil {
		t.Fatal("missing storage must error")
	}
	base := fakeDeps(t)
	base.PrepareAgent = nil
	if _, err := BuildSessionActor("s", base); err == nil {
		t.Fatal("missing PrepareAgent must error")
	}
	base = fakeDeps(t)
	base.AcquireSessionLease = nil
	if _, err := BuildSessionActor("s", base); err == nil {
		t.Fatal("missing lease callback must error")
	}
}

// TestBuildSessionActorHonorsHostOverrides 验证宿主的可选覆盖（PrepareRun /
// EnsureSession / CheckpointInterval / RunStallTimeout）可注入且不改变构造结果
// （E3.4 前置：runtimehost 的 config 面需覆盖 aicli 的差异）。
func TestBuildSessionActorHonorsHostOverrides(t *testing.T) {
	deps := fakeDeps(t)
	deps.PrepareRun = func(context.Context, *chat.Session, bool) error { return nil }
	deps.EnsureSession = func(context.Context, string) error { return nil }
	deps.CheckpointInterval = 15 * time.Second
	deps.RunStallTimeout = 30 * time.Second
	actor, err := BuildSessionActor("session-overrides", deps)
	if err != nil {
		t.Fatalf("BuildSessionActor with overrides: %v", err)
	}
	if actor == nil {
		t.Fatal("actor must not be nil")
	}
}

// TestLeaseModePolicy 验证两种租约模式的释放策略（E3.4-0）。
func TestLeaseModePolicy(t *testing.T) {
	if !LeaseModeRunScoped.ReleasesAfterConstruction() || !LeaseModeRunScoped.ReleasesOnRunFinished() {
		t.Fatal("run-scoped (zero value) must release after construction and on run finish")
	}
	if LeaseModeActorScoped.ReleasesAfterConstruction() || LeaseModeActorScoped.ReleasesOnRunFinished() {
		t.Fatal("actor-scoped must not release after construction nor on run finish")
	}
}

// TestActorScopedKeepsLeaseAfterConstruction 验证 actor-scoped 模式构造后仍持锁
// （G1：aicli 语义；runtimehost 不得在此释放）。
func TestActorScopedKeepsLeaseAfterConstruction(t *testing.T) {
	leaseStore := chat.NewInMemoryRuntimeStore(16)
	deps := fakeDepsWithLeaseStore(t, leaseStore)
	deps.LeaseMode = LeaseModeActorScoped
	const sessionID = "session-actor-scoped"
	if _, err := BuildSessionActor(sessionID, deps); err != nil {
		t.Fatalf("BuildSessionActor: %v", err)
	}
	lease, err := leaseStore.GetLease(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("GetLease: %v", err)
	}
	if lease == nil {
		t.Fatal("actor-scoped must keep the session lease after construction")
	}
}

// TestRunScopedReleasesLeaseAfterConstruction 验证 run-scoped（零值/默认）构造后释放租约
// ——保持既有行为不变。
func TestRunScopedReleasesLeaseAfterConstruction(t *testing.T) {
	leaseStore := chat.NewInMemoryRuntimeStore(16)
	deps := fakeDepsWithLeaseStore(t, leaseStore) // LeaseMode 零值 = run-scoped
	const sessionID = "session-run-scoped"
	if _, err := BuildSessionActor(sessionID, deps); err != nil {
		t.Fatalf("BuildSessionActor: %v", err)
	}
	lease, err := leaseStore.GetLease(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("GetLease: %v", err)
	}
	if lease != nil {
		t.Fatal("run-scoped must release the construction lease (idle actor must not block)")
	}
}

// TestComposePrepareRunRunsLeaseBeforeHostHook 验证顺序契约：OnRunPrepare 只在
// 默认租约获取（或宿主 PrepareRun）成功之后执行，宿主因此无需自行管理租约。
func TestComposePrepareRunRunsLeaseBeforeHostHook(t *testing.T) {
	var order []string
	deps := Deps{
		OnRunPrepare: func(context.Context, *chat.Session, bool) error {
			order = append(order, "hook")
			return nil
		},
	}
	ensureLease := func(context.Context) error {
		order = append(order, "lease")
		return nil
	}
	prepare := composePrepareRun(deps, ensureLease)
	if err := prepare(context.Background(), nil, false); err != nil {
		t.Fatalf("prepareRun: %v", err)
	}
	if len(order) != 2 || order[0] != "lease" || order[1] != "hook" {
		t.Fatalf("expected lease before host hook, got %v", order)
	}
}

// TestComposePrepareRunSkipsHookWhenLeaseFails 验证租约失败时不执行宿主刷新
// （fail-closed，且不产生半准备状态）。
func TestComposePrepareRunSkipsHookWhenLeaseFails(t *testing.T) {
	hookCalled := false
	deps := Deps{
		OnRunPrepare: func(context.Context, *chat.Session, bool) error {
			hookCalled = true
			return nil
		},
	}
	ensureLease := func(context.Context) error { return errors.New("lease denied") }
	prepare := composePrepareRun(deps, ensureLease)
	if err := prepare(context.Background(), nil, false); err == nil {
		t.Fatal("expected lease failure to propagate")
	}
	if hookCalled {
		t.Fatal("host hook must not run when the lease is not held")
	}
}

// TestComposePrepareRunHostOverrideWinsAndNilHookIsPassthrough 验证：
// PrepareRun 覆盖存在时不再调用默认 ensureLease；OnRunPrepare 为空时直接复用
// 基础实现（零开销，行为与改动前一致）。
func TestComposePrepareRunHostOverrideWinsAndNilHookIsPassthrough(t *testing.T) {
	var order []string
	deps := Deps{
		PrepareRun: func(context.Context, *chat.Session, bool) error {
			order = append(order, "host")
			return nil
		},
	}
	ensureLease := func(context.Context) error {
		order = append(order, "lease")
		return nil
	}
	prepare := composePrepareRun(deps, ensureLease)
	if err := prepare(context.Background(), nil, true); err != nil {
		t.Fatalf("prepareRun: %v", err)
	}
	if len(order) != 1 || order[0] != "host" {
		t.Fatalf("expected host PrepareRun only, got %v", order)
	}

	noop := composePrepareRun(Deps{}, ensureLease)
	if err := noop(context.Background(), nil, false); err != nil {
		t.Fatalf("nil hook passthrough: %v", err)
	}
}
