package commands

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// E3.4b-余项②：新旧路径对比契约测试（设计 §6 / §11 G5）。
//
// 目标：把「统一执行核」路径（`runtimehost.BuildSessionActor` + `aicliRuntimeDeps`）
// 产出的会话 actor 的**关键不变量**钉死，并与 aicli 旧装配路径做**可观测等价**对比。
//
// 观察面（只依赖 actor 的公开行为，不窥探内部配置）：
//   - 构造成功（无错误）；
//   - 构造后会话租约**已被持有**（actor-scoped，§11 G1/O1）——用「外来 owner」探测，
//     期望 `*runtimechat.LeaseConflictError`；
//   - 路由不变量：一轮真实 run 落到**冻结的** provider/model；
//   - `Stop()` 后租约**被释放**（外来 owner 可获取）。
//
// G5 约束：断言的是**内嵌冻结的期望值**（`aicliActorContractFrozen*` 字面量），
// 不引用旧实现函数，因此 E3.4c 删除旧路径后本测试仍可运行（对比段退化为自比）。
const (
	aicliContractProviderName      = "contract-provider"
	aicliContractModelName         = "contract-model"
	aicliContractChildProviderName = "contract-child-provider"
	aicliContractChildModelName    = "contract-child-model"
	aicliContractReply             = "contract ok"
)

// aicliActorContractObserved 是一次「建 actor → 跑一轮 → 停 actor」的完整契约观察。
// 全字段可比较，作为冻结期望值的载体。
type aicliActorContractObserved struct {
	BuildErr               string
	LeaseHeldAfterBuild    bool
	Output                 string
	RequestProvider        string
	RequestModel           string
	RequestReasoningEffort string
	LeaseReleasedAfterStop bool
}

// aicliActorContractFixture 提供能构造完整 host 的集成夹具（真实 bootstrap.Manager）。
type aicliActorContractFixture struct {
	host      *localChatRuntimeHost
	store     *runtimechat.InMemoryRuntimeStore
	manager   *runtimechat.SessionManager
	storage   runtimechat.SessionStorage
	userID    string
	session   *ChatSession
	sessionID string
	providers []*capturingLocalChatProvider
}

func newAicliActorContractFixture(t *testing.T) *aicliActorContractFixture {
	t.Helper()
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	t.Cleanup(manager.Stop)

	ctx := context.Background()
	runtimeSession, err := manager.Create(ctx, userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	bootstrapManager, err := runtimebootstrap.NewManager(&runtimebootstrap.Options{
		Config: runtimecfg.DefaultRuntimeConfig(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = bootstrapManager.Stop() })

	specs := []struct{ provider, model string }{
		{aicliContractProviderName, aicliContractModelName},
		{aicliContractChildProviderName, aicliContractChildModelName},
	}
	providers := make([]*capturingLocalChatProvider, 0, len(specs))
	for _, spec := range specs {
		provider := &capturingLocalChatProvider{
			name: spec.provider,
			responses: []*runtimellm.LLMResponse{
				{Content: aicliContractReply, Model: spec.model},
			},
		}
		if err := bootstrapManager.LLMRuntime().RegisterProvider(spec.provider, provider); err != nil {
			t.Fatalf("RegisterProvider(%s): %v", spec.provider, err)
		}
		if err := bootstrapManager.LLMRuntime().RegisterProviderAlias(spec.model, spec.provider); err != nil {
			t.Fatalf("RegisterProviderAlias(%s): %v", spec.model, err)
		}
		providers = append(providers, provider)
	}

	store := runtimechat.NewInMemoryRuntimeStore(64)
	host := &localChatRuntimeHost{
		Bootstrap:    bootstrapManager,
		RuntimeStore: store,
	}
	session := &ChatSession{
		ProviderName:     aicliContractProviderName,
		Model:            aicliContractModelName,
		RuntimeSession:   runtimeSession,
		SessionManager:   manager,
		LocalRuntimeHost: host,
	}
	return &aicliActorContractFixture{
		host:      host,
		store:     store,
		manager:   manager,
		storage:   manager.GetStorage(),
		userID:    userID,
		session:   session,
		sessionID: runtimeSession.ID,
		providers: providers,
	}
}

// createChildSession 建一个子会话并把路由上下文写进 runtime session（真实 spawn 路径口径）。
func (f *aicliActorContractFixture) createChildSession(t *testing.T, provider, model, effort string) string {
	t.Helper()
	ctx := context.Background()
	child, err := f.manager.Create(ctx, f.userID)
	if err != nil {
		t.Fatalf("manager.Create child: %v", err)
	}
	child.SetContext(sessionmeta.ProviderName, provider)
	child.SetContext(toolbroker.AgentSessionContextRequestedModel, model)
	child.SetContext(sessionmeta.ReasoningEffort, effort)
	if err := f.manager.Update(ctx, child); err != nil {
		t.Fatalf("manager.Update child: %v", err)
	}
	return child.ID
}

// lastRequest 汇总所有 provider 的捕获请求，返回最后一次的 provider/model/reasoning。
func (f *aicliActorContractFixture) lastRequest() (string, string, string) {
	var provider, model, effort string
	for _, p := range f.providers {
		if len(p.requests) == 0 {
			continue
		}
		req := p.requests[len(p.requests)-1]
		provider, model, effort = req.Provider, req.Model, req.ReasoningEffort
	}
	return provider, model, effort
}

// probeForeignSessionLease 以**外来 owner**（非 aicli actor）尝试获取会话租约。
// 返回 nil 表示当前无人持有；返回 *LeaseConflictError 表示已有存活持有者。
func probeForeignSessionLease(ctx context.Context, store runtimechat.RuntimeStateStore, sessionID string) error {
	leaseStore, ok := store.(runtimechat.SessionLeaseStore)
	if !ok {
		return errors.New("store does not implement runtimechat.SessionLeaseStore")
	}
	_, err := runtimechat.AcquireSessionLease(ctx, leaseStore, runtimechat.LeaseRequest{
		SessionID: sessionID,
		OwnerID:   "contract-probe:" + localChatHostname() + ":" + strconv.Itoa(os.Getpid()),
		OwnerKind: "runtime-server-actor",
		PID:       os.Getpid(),
		Hostname:  localChatHostname(),
		TTL:       time.Minute,
	})
	return err
}

func isForeignLeaseConflict(err error) bool {
	var conflict *runtimechat.LeaseConflictError
	return errors.As(err, &conflict)
}

// waitForeignLeaseAcquirable 轮询等待租约被释放（Stop 的释放可能略滞后于调用返回）。
func waitForeignLeaseAcquirable(ctx context.Context, store runtimechat.RuntimeStateStore, sessionID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		err := probeForeignSessionLease(ctx, store, sessionID)
		if err == nil {
			return true
		}
		if !isForeignLeaseConflict(err) || time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// observeSession 走一次完整的 actor 生命周期（经 buildSessionActor，按开关选择新旧路径）。
func (f *aicliActorContractFixture) observeSession(t *testing.T, sessionID string, reuse bool) aicliActorContractObserved {
	t.Helper()
	if reuse {
		t.Setenv("AICLI_EXECUTOR_REUSE", "1")
	} else {
		t.Setenv("AICLI_EXECUTOR_REUSE", "")
	}
	ctx := context.Background()
	var observed aicliActorContractObserved

	actor, err := f.host.buildSessionActor(sessionID, f.session, f.storage, nil, "")
	if err != nil {
		observed.BuildErr = err.Error()
		return observed
	}
	// actor-scoped：构造完成即持锁（未被释放）。
	observed.LeaseHeldAfterBuild = isForeignLeaseConflict(probeForeignSessionLease(ctx, f.store, sessionID))

	result, err := actor.SubmitPrompt(ctx, "hello", nil)
	if err != nil {
		observed.BuildErr = "SubmitPrompt: " + err.Error()
		actor.Stop()
		return observed
	}
	if result != nil {
		observed.Output = strings.TrimSpace(result.Output)
	}
	observed.RequestProvider, observed.RequestModel, observed.RequestReasoningEffort = f.lastRequest()

	actor.Stop()
	observed.LeaseReleasedAfterStop = waitForeignLeaseAcquirable(ctx, f.store, sessionID, 5*time.Second)
	return observed
}

// TestAicliExecutorReuseContractFrozenInvariants 钉住统一执行核路径的冻结不变量，
// 并与旧装配路径做可观测等价对比。
func TestAicliExecutorReuseContractFrozenInvariants(t *testing.T) {
	frozen := aicliActorContractObserved{
		Output:                 aicliContractReply,
		RequestProvider:        aicliContractProviderName,
		RequestModel:           aicliContractModelName,
		LeaseHeldAfterBuild:    true,
		LeaseReleasedAfterStop: true,
	}

	oldFixture := newAicliActorContractFixture(t)
	oldObserved := oldFixture.observeSession(t, oldFixture.sessionID, false)
	newFixture := newAicliActorContractFixture(t)

	// 探针自检：从未持锁的会话必须可获取——否则 LeaseHeldAfterBuild 会因探针恒冲突而假绿。
	if err := probeForeignSessionLease(context.Background(), newFixture.store, "contract-never-leased"); err != nil {
		t.Fatalf("租约探针不自洽（未持锁会话也不可获取），契约测试失去判别力: %v", err)
	}

	newObserved := newFixture.observeSession(t, newFixture.sessionID, true)

	// 新路径必须与冻结期望逐字段一致（内嵌字面量 → E3.4c 后仍可运行，§11 G5）。
	if newObserved != frozen {
		t.Fatalf("统一执行核路径契约漂移：\n got %+v\nwant %+v", newObserved, frozen)
	}
	// 旧路径同样与冻结期望一致（当前基线）。
	if oldObserved != frozen {
		t.Fatalf("旧装配路径契约漂移：\n got %+v\nwant %+v", oldObserved, frozen)
	}
	// 两条路径可观测行为必须等价（§6「新旧路径对比」核心）。
	if newObserved != oldObserved {
		t.Fatalf("新旧路径可观测行为不一致：\n new %+v\n old %+v", newObserved, oldObserved)
	}
}

// TestAicliExecutorReuseContractChildRouteInvariants 钉住子会话路由不变量：
// 子会话（hub 工厂以「基础会话闭包 + 子 sessionID」调用）必须按 runtime session 的
// 路由上下文落到子 provider/model，且新旧路径一致（设计 §11 G2 / 风险 #4）。
func TestAicliExecutorReuseContractChildRouteInvariants(t *testing.T) {
	frozen := aicliActorContractObserved{
		Output:                 aicliContractReply,
		RequestProvider:        aicliContractChildProviderName,
		RequestModel:           aicliContractChildModelName,
		RequestReasoningEffort: "high",
		LeaseHeldAfterBuild:    true,
		LeaseReleasedAfterStop: true,
	}

	oldFixture := newAicliActorContractFixture(t)
	oldChild := oldFixture.createChildSession(t, aicliContractChildProviderName, aicliContractChildModelName, "high")
	oldObserved := oldFixture.observeSession(t, oldChild, false)

	newFixture := newAicliActorContractFixture(t)
	newChild := newFixture.createChildSession(t, aicliContractChildProviderName, aicliContractChildModelName, "high")
	newObserved := newFixture.observeSession(t, newChild, true)

	if newObserved != frozen {
		t.Fatalf("子会话路由契约漂移（统一执行核）：\n got %+v\nwant %+v", newObserved, frozen)
	}
	if oldObserved != frozen {
		t.Fatalf("子会话路由契约漂移（旧装配）：\n got %+v\nwant %+v", oldObserved, frozen)
	}
	if newObserved != oldObserved {
		t.Fatalf("子会话新旧路径可观测行为不一致：\n new %+v\n old %+v", newObserved, oldObserved)
	}
}
