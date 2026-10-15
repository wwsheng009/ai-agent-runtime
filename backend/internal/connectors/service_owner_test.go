package connectors

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── 运行归属（07 §5.6）测试助手 ────────────────────────────────────────────

// logRecorder 收集 Service 日志（心跳/归属闸在后台 goroutine 里打印，需要加锁）。
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRecorder) logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *logRecorder) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// newOwnerTestService 构造带运行归属身份的 Service（心跳/超时可缩短）。
func newOwnerTestService(t *testing.T, store Store, provider Provider, hostID string, heartbeat, stale time.Duration) (*Service, *logRecorder) {
	t.Helper()
	logs := &logRecorder{}
	service := newTestService(t, store, &fakeBackend{}, provider, func(deps *Deps) {
		deps.HostID = hostID
		deps.OwnerHeartbeatInterval = heartbeat
		deps.OwnerStaleAfter = stale
		deps.Logf = logs.logf
	})
	return service, logs
}

// TestServiceAutoOwnerFirstHostWins 覆盖 auto（空 runtime_owner）语义：先到先得，
// 后启动者只读配置不轮询；先启动者停机后释放归属，后启动者随后可接管。
func TestServiceAutoOwnerFirstHostWins(t *testing.T) {
	store := NewMemoryStore(testConfig("c1", "fake"))
	providerA := &fakeProvider{polling: true}
	providerB := &fakeProvider{polling: true}
	serviceA, _ := newOwnerTestService(t, store, providerA, "runtime-server", time.Minute, time.Minute)
	serviceB, logsB := newOwnerTestService(t, store, providerB, "aicli", time.Minute, time.Minute)

	if err := serviceA.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "host A polling", func() bool {
		starts, _ := providerA.pollCounts()
		return starts == 1
	})
	if record, ok := store.OwnerOf("c1"); !ok || record.Owner != "runtime-server" {
		t.Fatalf("owner after A start = %+v ok=%v, want runtime-server", record, ok)
	}

	if err := serviceB.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if starts, _ := providerB.pollCounts(); starts != 0 {
		t.Fatalf("host B must not poll while runtime-server holds the claim (starts=%d)", starts)
	}
	if !strings.Contains(logsB.joined(), "未运行（由 runtime-server 持有）") {
		t.Fatalf("host B should log the ownership conflict:\n%s", logsB.joined())
	}

	// 停机释放归属（Stop），另一宿主随后可认领并轮询。
	serviceA.Stop(context.Background())
	waitFor(t, "host A polling exit", func() bool {
		_, exits := providerA.pollCounts()
		return exits == 1
	})
	if _, ok := store.OwnerOf("c1"); ok {
		t.Fatal("Stop 必须释放本宿主认领")
	}
	if err := serviceB.StartConnector(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "host B polling", func() bool {
		starts, _ := providerB.pollCounts()
		return starts == 1
	})
	t.Cleanup(func() { serviceB.Stop(context.Background()) })
}

// TestServiceExplicitRuntimeOwnerGatesHost 覆盖显式 runtime_owner：身份不匹配的
// 宿主不认领、不轮询；身份匹配的宿主照常启动。
func TestServiceExplicitRuntimeOwnerGatesHost(t *testing.T) {
	config := func() Config {
		return testConfig("c1", "fake", func(cfg *Config) { cfg.Policy.RuntimeOwner = "runtime-server" })
	}

	mismatchStore := NewMemoryStore(config())
	mismatchProvider := &fakeProvider{polling: true}
	mismatch, logs := newOwnerTestService(t, mismatchStore, mismatchProvider, "aicli", time.Minute, time.Minute)
	if err := mismatch.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if starts, _ := mismatchProvider.pollCounts(); starts != 0 {
		t.Fatalf("aicli must not poll a runtime-server-owned connector (starts=%d)", starts)
	}
	if !strings.Contains(logs.joined(), "未运行（由 runtime-server 持有") {
		t.Fatalf("expected ownership conflict log:\n%s", logs.joined())
	}
	if _, ok := mismatchStore.OwnerOf("c1"); ok {
		t.Fatal("policy 不匹配时不得写入归属记录")
	}

	matchStore := NewMemoryStore(config())
	matchProvider := &fakeProvider{polling: true}
	match, _ := newOwnerTestService(t, matchStore, matchProvider, "runtime-server", time.Minute, time.Minute)
	if err := match.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "matching host polling", func() bool {
		starts, _ := matchProvider.pollCounts()
		return starts == 1
	})
	t.Cleanup(func() { match.Stop(context.Background()) })
	if record, ok := matchStore.OwnerOf("c1"); !ok || record.Owner != "runtime-server" {
		t.Fatalf("owner = %+v ok=%v, want runtime-server", record, ok)
	}
}

// TestServiceStaleOwnerTakeover 覆盖陈旧接管：持有者心跳超时（可配）后，
// 后来者按 OwnerStaleAfter 判定并接管；阈值未到则仍然跳过。
func TestServiceStaleOwnerTakeover(t *testing.T) {
	old := time.Now().UTC().Add(-2 * time.Minute)

	staleStore := NewMemoryStore(testConfig("c1", "fake"))
	if _, _, err := staleStore.ClaimConnectorOwner("c1", "runtime-server", old, time.Hour); err != nil {
		t.Fatal(err)
	}
	staleProvider := &fakeProvider{polling: true}
	staleService, _ := newOwnerTestService(t, staleStore, staleProvider, "aicli", time.Minute, time.Minute)
	if err := staleService.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stale takeover polling", func() bool {
		starts, _ := staleProvider.pollCounts()
		return starts == 1
	})
	if record, _ := staleStore.OwnerOf("c1"); record.Owner != "aicli" {
		t.Fatalf("owner after stale takeover = %+v, want aicli", record)
	}
	t.Cleanup(func() { staleService.Stop(context.Background()) })

	// 对照组：心跳仍在 10min 阈值内，不得接管。
	freshStore := NewMemoryStore(testConfig("c1", "fake"))
	if _, _, err := freshStore.ClaimConnectorOwner("c1", "runtime-server", old, time.Hour); err != nil {
		t.Fatal(err)
	}
	freshProvider := &fakeProvider{polling: true}
	freshService, logs := newOwnerTestService(t, freshStore, freshProvider, "aicli", time.Minute, 10*time.Minute)
	if err := freshService.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if starts, _ := freshProvider.pollCounts(); starts != 0 {
		t.Fatalf("fresh owner must not be taken over (starts=%d)", starts)
	}
	if !strings.Contains(logs.joined(), "未运行（由 runtime-server 持有）") {
		t.Fatalf("expected conflict log:\n%s", logs.joined())
	}
}

// TestServiceStopConnectorReleasesOwnership 覆盖 StopConnector 的归属释放。
func TestServiceStopConnectorReleasesOwnership(t *testing.T) {
	store := NewMemoryStore(testConfig("c1", "fake"))
	provider := &fakeProvider{polling: true}
	service, _ := newOwnerTestService(t, store, provider, "aicli", time.Minute, time.Minute)
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "polling", func() bool {
		starts, _ := provider.pollCounts()
		return starts == 1
	})

	service.StopConnector("c1")
	waitFor(t, "polling exit", func() bool {
		_, exits := provider.pollCounts()
		return exits == 1
	})
	if _, ok := store.OwnerOf("c1"); ok {
		t.Fatal("StopConnector 必须释放本宿主认领")
	}
}

// TestServiceHeartbeatRefreshesAndDetectsLostOwnership 覆盖心跳语义：
// 轮询期间周期续约；归属被他人强制接管后，本宿主心跳发现 held=false 并停止轮询
// （尽力围栏），且不回写归属。
func TestServiceHeartbeatRefreshesAndDetectsLostOwnership(t *testing.T) {
	store := NewMemoryStore(testConfig("c1", "fake"))
	provider := &fakeProvider{polling: true}
	service, logs := newOwnerTestService(t, store, provider, "runtime-server", 20*time.Millisecond, time.Minute)
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "polling", func() bool {
		starts, _ := provider.pollCounts()
		return starts == 1
	})
	first, _ := store.OwnerOf("c1")
	waitFor(t, "heartbeat refresh", func() bool {
		record, ok := store.OwnerOf("c1")
		return ok && record.HeartbeatAt.After(first.HeartbeatAt)
	})

	// 另一宿主抢占（管理面 takeover 的等价存储操作）。
	if err := store.ForceClaimConnectorOwner("c1", "aicli", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "lost ownership cancels polling", func() bool {
		_, exits := provider.pollCounts()
		return exits == 1
	})
	if record, _ := store.OwnerOf("c1"); record.Owner != "aicli" {
		t.Fatalf("本宿主不得回写被抢占的归属: %+v", record)
	}
	if !strings.Contains(logs.joined(), "运行归属已被接管") {
		t.Fatalf("expected lost-ownership log:\n%s", logs.joined())
	}
}

// TestServiceTakeoverConnector 覆盖管理面「抢占」：强制改写存活持有者的归属并
// 启动本宿主轮询；未配置 HostID / 停用 / 策略指定他人时返回明确错误。
func TestServiceTakeoverConnector(t *testing.T) {
	store := NewMemoryStore(testConfig("c1", "fake"))
	if _, _, err := store.ClaimConnectorOwner("c1", "runtime-server", time.Now().UTC(), time.Hour); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{polling: true}
	service, _ := newOwnerTestService(t, store, provider, "aicli", time.Minute, time.Minute)
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if starts, _ := provider.pollCounts(); starts != 0 {
		t.Fatalf("aicli must not poll before takeover (starts=%d)", starts)
	}

	if err := service.TakeoverConnector(context.Background(), "c1"); err != nil {
		t.Fatalf("TakeoverConnector: %v", err)
	}
	waitFor(t, "takeover polling", func() bool {
		starts, _ := provider.pollCounts()
		return starts == 1
	})
	if record, _ := store.OwnerOf("c1"); record.Owner != "aicli" {
		t.Fatalf("owner after takeover = %+v, want aicli", record)
	}
	t.Cleanup(func() { service.Stop(context.Background()) })

	// 未配置 HostID：明确报错（REST 端映射 503）。
	noHost, _ := newOwnerTestService(t, store, &fakeProvider{polling: true}, "", time.Minute, time.Minute)
	if err := noHost.TakeoverConnector(context.Background(), "c1"); !errors.Is(err, ErrHostIDRequired) {
		t.Fatalf("no-host takeover err = %v, want ErrHostIDRequired", err)
	}

	// 停用连接器：拒绝接管。
	if err := store.PutConfig(testConfig("c2", "fake", func(cfg *Config) { cfg.Enabled = false })); err != nil {
		t.Fatal(err)
	}
	if err := service.TakeoverConnector(context.Background(), "c2"); !errors.Is(err, ErrConnectorDisabled) {
		t.Fatalf("disabled takeover err = %v, want ErrConnectorDisabled", err)
	}

	// 策略指定其它宿主：拒绝接管（策略优先）。
	if err := store.PutConfig(testConfig("c3", "fake", func(cfg *Config) { cfg.Policy.RuntimeOwner = "gateway" })); err != nil {
		t.Fatal(err)
	}
	if err := service.TakeoverConnector(context.Background(), "c3"); !errors.Is(err, ErrOwnerPolicyMismatch) {
		t.Fatalf("policy mismatch err = %v, want ErrOwnerPolicyMismatch", err)
	}

	// 未知连接器：保持既有「unknown connector」错误。
	if err := service.TakeoverConnector(context.Background(), "missing"); err == nil || !strings.Contains(err.Error(), "unknown connector") {
		t.Fatalf("unknown takeover err = %v", err)
	}
}

// TestServiceReclaimsOwnershipAfterHolderCrash 覆盖 2026-10-15 真机故障：
// 宿主 B 启动时宿主 A 心跳仍新鲜 → 认领被拒（登记待接管）；A 随后崩溃
// （不再续约，未走停机释放）→ 复查循环必须在 stale 阈值过后自动接管并启动
// 轮询——否则连接器永久无人轮询，IM 消息无法进入绑定会话。
func TestServiceReclaimsOwnershipAfterHolderCrash(t *testing.T) {
	store := NewMemoryStore(testConfig("c1", "fake"))
	// A 认领后"崩溃"：心跳停留在认领时刻，此后不再刷新。
	if _, _, err := store.ClaimConnectorOwner("c1", "runtime-server", time.Now().UTC(), time.Hour); err != nil {
		t.Fatal(err)
	}

	provider := &fakeProvider{polling: true}
	service, logs := newOwnerTestService(t, store, provider, "aicli", 20*time.Millisecond, 60*time.Millisecond)
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Stop(context.Background()) })

	// 启动时 A 心跳新鲜：认领被拒、登记待接管，不得立即轮询。
	if starts, _ := provider.pollCounts(); starts != 0 {
		t.Fatalf("must not steal fresh ownership at startup (starts=%d)", starts)
	}
	if !strings.Contains(logs.joined(), "未运行（由 runtime-server 持有）") {
		t.Fatalf("expected ownership conflict log:\n%s", logs.joined())
	}

	// A 不再续约 → stale 阈值过后复查循环自动接管并启动轮询。
	waitFor(t, "reclaim stale ownership", func() bool {
		starts, _ := provider.pollCounts()
		return starts == 1
	})
	if record, _ := store.OwnerOf("c1"); record.Owner != "aicli" {
		t.Fatalf("owner after reclaim = %+v, want aicli", record)
	}
}

// TestServiceReclaimDoesNotStealLiveOwnership 覆盖复查循环的活体保护与
// 兜底：持有者持续续约期间不得抢占；续约停止（等同死亡）后仍会接管。
func TestServiceReclaimDoesNotStealLiveOwnership(t *testing.T) {
	store := NewMemoryStore(testConfig("c1", "fake"))
	if _, _, err := store.ClaimConnectorOwner("c1", "runtime-server", time.Now().UTC(), time.Hour); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{polling: true}
	service, _ := newOwnerTestService(t, store, provider, "aicli", 20*time.Millisecond, 60*time.Millisecond)
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Stop(context.Background()) })

	// A "存活"：持续续约 200ms（跨越多个 stale 窗口与复查周期）→ B 不得抢占。
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_, _ = store.HeartbeatConnectorOwner("c1", "runtime-server", time.Now().UTC())
			}
		}
	}()
	time.Sleep(200 * time.Millisecond)
	close(stop)
	<-done
	if starts, _ := provider.pollCounts(); starts != 0 {
		t.Fatalf("must not steal live ownership (starts=%d)", starts)
	}

	// A 停止续约 → 复查循环兜底接管，避免永久失联。
	waitFor(t, "reclaim after holder stops renewing", func() bool {
		starts, _ := provider.pollCounts()
		return starts == 1
	})
}
