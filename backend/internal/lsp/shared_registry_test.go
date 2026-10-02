package lsp

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// shared_registry_test.go 覆盖进程内共享池的不变量。
//
// 这组测试保护的不是"能共享"，而是**不该发生的事**：
//   - 两个同键会话各起一个进程（内存翻倍，正是要消除的浪费）
//   - 引用计数少减一次 → 池被提前关停（别人的编辑突然失去诊断）
//   - 引用计数多减一次 → 池提前关停 / 桶被误删
//   - 一个会话关闭后仍继续收到事件推送
//   - 注入传输（测试 / 自定义宿主）被并进真实进程池

// resetSharedRegistriesForTest 清空共享表。用它而不是让测试互相独立构造，
// 是因为"表里有残留"本身就是一类缺陷来源：上一个用例的桶若没被 release，
// 下一个用例会拿到别人的池。
func resetSharedRegistriesForTest(t *testing.T) {
	t.Helper()
	sharedRegistries.mu.Lock()
	for _, entry := range sharedRegistries.byKey {
		entry.registry.Stop(context.Background())
	}
	sharedRegistries.byKey = map[sharedKey]*sharedRegistryEntry{}
	sharedRegistries.mu.Unlock()
}

// shareableConfig 返回一个"会进共享表"的配置：enabled + 真实 spawn 路径
// （Dial=nil）。
func shareableConfig() Config {
	return Config{
		Enabled: true,
		Servers: []ServerSpec{{
			Name:       "alpha",
			Command:    "alpha-lsp",
			Extensions: []string{".go"},
		}},
		Diagnostics: DiagnosticsConfig{Scope: ScopeAll, WaitMS: 2000, StartWaitMS: 1000},
	}
}

func TestSharedRegistrySameKeyReturnsSamePool(t *testing.T) {
	resetSharedRegistriesForTest(t)
	cfg := shareableConfig()

	root := t.TempDir()
	first, _, releaseFirst, shared := AcquireSharedRegistry(cfg, root, RegistryOptions{})
	if !shared {
		t.Fatal("真实 spawn 路径 + enabled 必须参与进程内共享")
	}
	defer releaseFirst()
	second, _, releaseSecond, shared := AcquireSharedRegistry(cfg, root, RegistryOptions{})
	if !shared {
		t.Fatal("同键第二次 Acquire 必须仍然共享")
	}
	defer releaseSecond()

	if first != second {
		t.Fatalf("同 root 同配置得到两个不同池（%p vs %p）：单实例约束已失效", first, second)
	}
}

func TestSharedRegistryDifferentRootStaysSeparate(t *testing.T) {
	resetSharedRegistriesForTest(t)
	cfg := shareableConfig()

	first, _, releaseFirst, _ := AcquireSharedRegistry(cfg, t.TempDir(), RegistryOptions{})
	defer releaseFirst()
	second, _, releaseSecond, _ := AcquireSharedRegistry(cfg, t.TempDir(), RegistryOptions{})
	defer releaseSecond()

	if first == second {
		t.Fatal("不同 root 必须各自成池：跨项目共享进程会让两个模块的诊断互相污染")
	}
}

func TestSharedRegistryDifferentSpecStaysSeparate(t *testing.T) {
	resetSharedRegistriesForTest(t)
	root := t.TempDir()

	base := shareableConfig()
	first, _, releaseFirst, _ := AcquireSharedRegistry(base, root, RegistryOptions{})
	defer releaseFirst()

	changed := shareableConfig()
	changed.Servers[0].Command = "beta-lsp"
	second, _, releaseSecond, _ := AcquireSharedRegistry(changed, root, RegistryOptions{})
	defer releaseSecond()

	if first == second {
		t.Fatal("成员命令不同必须分池：否则先注册的声明会静默胜出")
	}
}

func TestSharedRegistryEnvMapOrderDoesNotSplitKey(t *testing.T) {
	resetSharedRegistriesForTest(t)
	root := t.TempDir()

	first := shareableConfig()
	first.Servers[0].Env = map[string]string{"B": "2", "A": "1", "C": "3"}
	a, _, releaseA, _ := AcquireSharedRegistry(first, root, RegistryOptions{})
	defer releaseA()

	// 同一份 map，不同插入顺序：Go 的 map 迭代顺序是随机的，必须仍然同键。
	second := shareableConfig()
	second.Servers[0].Env = map[string]string{"C": "3", "A": "1", "B": "2"}
	b, _, releaseB, _ := AcquireSharedRegistry(second, root, RegistryOptions{})
	defer releaseB()

	if a != b {
		t.Fatal("Env/InitializationOptions 是 map：迭代顺序不得改变共享键，否则本该共享的池会分裂")
	}
}

func TestSharedRegistryDisabledConfigNeverShares(t *testing.T) {
	resetSharedRegistriesForTest(t)
	cfg := shareableConfig()
	cfg.Enabled = false

	first, _, _, shared := AcquireSharedRegistry(cfg, t.TempDir(), RegistryOptions{})
	if shared {
		t.Fatal("disabled 配置没有进程可共享，不得占用共享表键")
	}
	if first == nil {
		t.Fatal("仍须返回一个私有池（调用方按存在性判断）")
	}
	// 第二个 disabled Acquire 也不共享，且两次拿到不同的私有池。
	second, _, _, shared := AcquireSharedRegistry(cfg, first.Root(), RegistryOptions{})
	if shared || first == second {
		t.Fatal("disabled 配置的每次 Acquire 都应是独立私有池")
	}
}

func TestSharedRegistryInjectedDialStaysPrivate(t *testing.T) {
	resetSharedRegistriesForTest(t)
	cfg := shareableConfig()
	dial := func(context.Context, ServerSpec, string, Logger) (*DialResult, error) {
		return nil, fmt.Errorf("not used")
	}

	first, _, _, shared := AcquireSharedRegistry(cfg, t.TempDir(), RegistryOptions{Dial: dial})
	if shared {
		t.Fatal("注入传输必须私有：两个互不相干的假 server 会被并成一个，测试隔离当场失效")
	}
	second, _, _, _ := AcquireSharedRegistry(cfg, t.TempDir(), RegistryOptions{Dial: dial})
	if first == second {
		t.Fatal("注入传输的两次 Acquire 必须互不共享")
	}
}

func TestSharedRegistryEmptyServerListNeverShares(t *testing.T) {
	resetSharedRegistriesForTest(t)
	cfg := shareableConfig()
	// 注意：空切片而不是 nil——Config.Normalize 会把 nil 填成预设目录
	// （spec.go:417），那是"用默认成员"，不是"没有成员"。
	cfg.Servers = []ServerSpec{}

	_, _, _, shared := AcquireSharedRegistry(cfg, t.TempDir(), RegistryOptions{})
	if shared {
		t.Fatal("没有成员的惰性空壳不进共享表")
	}
}

func TestSharedRegistryReleaseIsIdempotent(t *testing.T) {
	resetSharedRegistriesForTest(t)
	cfg := shareableConfig()

	root := t.TempDir()
	_, _, releaseFirst, _ := AcquireSharedRegistry(cfg, root, RegistryOptions{})
	second, _, releaseSecond, _ := AcquireSharedRegistry(cfg, root, RegistryOptions{})

	releaseFirst()
	// 宿主可能在 Close 与迟到清理两条路径各调一次 release。多减一次引用会把
	// 第二个会话还在用的池减到 0 并提前关停——症状是"别人的编辑突然没有诊断"。
	releaseFirst()

	if second.isStopped() {
		t.Fatal("重复 release 把仍在使用的池关停了")
	}
	releaseSecond()
}

func TestSharedRegistryLastReleaseStopsProcess(t *testing.T) {
	resetSharedRegistriesForTest(t)
	cfg := shareableConfig()

	root := t.TempDir()
	registry, _, releaseFirst, _ := AcquireSharedRegistry(cfg, root, RegistryOptions{})
	_, _, releaseSecond, _ := AcquireSharedRegistry(cfg, root, RegistryOptions{})
	if registry == nil {
		t.Fatal("池为空")
	}

	releaseFirst()
	if registry.isStopped() {
		t.Fatal("还有一个借用方时不得关停")
	}
	releaseSecond()
	if !registry.isStopped() {
		t.Fatal("最后一个 release 必须关停进程（否则留下孤儿 gopls）")
	}
}

func TestSharedRegistryEntryEvictedOnLastRelease(t *testing.T) {
	resetSharedRegistriesForTest(t)
	cfg := shareableConfig()

	root := t.TempDir()
	_, _, release, _ := AcquireSharedRegistry(cfg, root, RegistryOptions{})
	release()

	sharedRegistries.mu.Lock()
	size := len(sharedRegistries.byKey)
	sharedRegistries.mu.Unlock()
	if size != 0 {
		t.Fatalf("最后一个 release 后共享表仍有 %d 个桶：下次 Acquire 会拿到一个 stopped 的残骸", size)
	}
}

func TestSharedBridgeStopTwiceKeepsPoolAliveForOthers(t *testing.T) {
	resetSharedRegistriesForTest(t)
	cfg := shareableConfig()

	// 走 Bridge 而非裸 Registry：共享模式下 Bridge.Stop 只归还引用，直接对
	// Registry 调 Stop 会把所有人一起关停——那正是本设计要防的越权。
	// 这里不启动任何成员（命令名是假的也无妨），所以不会真的 spawn 进程。
	root := t.TempDir()
	first := NewBridge(cfg, root, nil, nil)
	second := NewBridge(cfg, root, nil, nil)
	if !first.sharedPool || !second.sharedPool {
		t.Fatal("两个桥都应走共享路径")
	}
	registry := first.registry

	// 幂等性必须落在 Bridge 层：Close 与迟到清理都会调 Stop。
	first.Stop(context.Background())
	first.Stop(context.Background())
	if registry.isStopped() {
		t.Fatal("第一个桥的重复 Stop 把第二个桥仍在用的池关停了")
	}
	second.Stop(context.Background())
	if !registry.isStopped() {
		t.Fatal("最后一个桥 Stop 后必须关停进程")
	}
}

func TestObserverHubFansOutToEverySubscriber(t *testing.T) {
	hub := newObserverHub(nil)
	var mu sync.Mutex
	firstCount, secondCount := 0, 0
	unsubscribeFirst := hub.Subscribe(func(Event) {
		mu.Lock()
		firstCount++
		mu.Unlock()
	})
	hub.Subscribe(func(Event) {
		mu.Lock()
		secondCount++
		mu.Unlock()
	})

	hub.emit(Event{Kind: EventServerState})
	mu.Lock()
	first, second := firstCount, secondCount
	mu.Unlock()
	if first != 1 || second != 1 {
		t.Fatalf("每个订阅者各收一次：first=%d second=%d", first, second)
	}

	unsubscribeFirst()
	unsubscribeFirst() // 幂等
	hub.emit(Event{Kind: EventServerState})
	mu.Lock()
	first, second = firstCount, secondCount
	mu.Unlock()
	if first != 1 {
		t.Fatalf("退订后不得再收到事件（否则已关闭的会话继续被推送）：first=%d", first)
	}
	if second != 2 {
		t.Fatalf("未退订的订阅者必须继续收到事件：second=%d", second)
	}
}

func TestObserverHubNilObserverIsNoop(t *testing.T) {
	hub := newObserverHub(nil)
	if unsub := hub.Subscribe(nil); unsub == nil {
		t.Fatal("nil Observer 的退订函数不得为 nil（调用方会无条件调用它）")
	} else {
		unsub()
	}
	hub.emit(Event{Kind: EventServerState})
}

func TestSharedSessionCountReportsBorrowers(t *testing.T) {
	resetSharedRegistriesForTest(t)
	cfg := shareableConfig()
	root := t.TempDir()

	first, _, releaseFirst, _ := AcquireSharedRegistry(cfg, root, RegistryOptions{})
	if got := SharedSessionCount(first); got != 1 {
		t.Fatalf("单会话计数 = %d，want 1", got)
	}
	_, _, releaseSecond, _ := AcquireSharedRegistry(cfg, root, RegistryOptions{})
	if got := SharedSessionCount(first); got != 2 {
		t.Fatalf("两个借用方时计数 = %d，want 2", got)
	}
	releaseFirst()
	if got := SharedSessionCount(first); got != 1 {
		t.Fatalf("归还一个后计数 = %d，want 1", got)
	}
	releaseSecond()
	// 归零后不再共享：计数必须回到 0，否则状态面会一直显示"共享 N 个会话"。
	if got := SharedSessionCount(first); got != 0 {
		t.Fatalf("全部归还后计数 = %d，want 0", got)
	}
}

func TestSharedSessionCountZeroForPrivatePool(t *testing.T) {
	resetSharedRegistriesForTest(t)
	cfg := shareableConfig()
	cfg.Enabled = false

	private := NewRegistry(cfg, t.TempDir(), RegistryOptions{})
	if got := SharedSessionCount(private); got != 0 {
		t.Fatalf("私有池不得报告共享：got %d", got)
	}
	if got := SharedSessionCount(nil); got != 0 {
		t.Fatalf("nil 池不得报告共享：got %d", got)
	}
}

// 重启预算是共享 entry 上的状态（registryEntry.restarts / lastRestartAt），
// 但预算值从 entry.parent.cfg 读——共享后那就是"第一个注册者"的声明。
// 两个方向都会静默出错，所以恢复策略不同必须分成两个池。
func TestSharedKeySeparatesRestartPolicy(t *testing.T) {
	root := t.TempDir()
	base := shareableConfig()

	noRecovery := base
	zero, two := 0, 2
	noRecovery.RestartLimit = &zero
	moreRecovery := base
	moreRecovery.RestartLimit = &two
	shorterWindow := base
	shorterWindow.RestartWindow = 3 * time.Minute

	cases := []struct {
		name string
		a, b Config
	}{
		{"restartLimit 0 vs 默认", noRecovery, base},
		{"restartLimit 默认 vs 2", base, moreRecovery},
		{"restartWindow 默认 vs 3m", base, shorterWindow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keyA, okA := sharedRegistryKey(tc.a, root, RegistryOptions{})
			keyB, okB := sharedRegistryKey(tc.b, root, RegistryOptions{})
			if !okA || !okB {
				t.Fatalf("两个配置都应可共享：okA=%v okB=%v", okA, okB)
			}
			if keyA == keyB {
				t.Fatal("恢复策略不同却分到同一个池：预算会取第一个注册者的值（双向静默出错）")
			}
		})
	}

	// 反向：完全相同的恢复策略必须仍然共享，否则这条修复反而把池拆开。
	same, ok1 := sharedRegistryKey(base, root, RegistryOptions{})
	same2, ok2 := sharedRegistryKey(base, root, RegistryOptions{})
	if !ok1 || !ok2 || same != same2 {
		t.Fatal("相同配置必须仍然共享")
	}
}
