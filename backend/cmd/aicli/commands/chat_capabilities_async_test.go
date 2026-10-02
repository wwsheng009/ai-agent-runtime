package commands

// chat_capabilities_async_test.go — 「先渲染 UI、再异步装载能力面」的门控与
// 动态栏进度回归围栏。
//
// 覆盖三条不变式：
//  1. 门控句柄必须在首帧之前安装（否则首帧后立刻提交会绕过 await 直接失败）；
//  2. await 必须真的阻塞到装载完成，且把装载错误原样交给首个 turn；
//  3. 动态栏进度行只在装载期间存在，结束后撤行，且前台活动时让位。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestInstallChatCapabilitiesGate_GateVisibleBeforeLoadStarts(t *testing.T) {
	session := &ChatSession{}
	installChatCapabilitiesGate(session,
		func() (*chatCapabilityDiscovery, error) { return &chatCapabilityDiscovery{}, nil },
		func(*chatCapabilityDiscovery) (func(), error) { return nil, nil },
	)

	load := currentChatCapabilityLoad(session)
	if load == nil {
		t.Fatal("门控句柄必须在装载启动之前就已安装（首帧后立刻提交要能被 await 捕获）")
	}
	// 尚未 start：await 必须阻塞而不是立即返回。
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := awaitChatCapabilities(ctx, session); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("装载未启动时 await 应超时，实际 err=%v", err)
	}
}

func TestRunChatCapabilitiesLoad_AwaitBlocksUntilDone(t *testing.T) {
	session := &ChatSession{}
	release := make(chan struct{})
	var attachCalls int
	installChatCapabilitiesGate(session,
		func() (*chatCapabilityDiscovery, error) {
			<-release
			return &chatCapabilityDiscovery{}, nil
		},
		func(*chatCapabilityDiscovery) (func(), error) {
			attachCalls++
			return func() {}, nil
		},
	)

	started := make(chan struct{})
	go func() {
		close(started)
		runChatCapabilitiesLoad(session)
	}()
	<-started

	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- awaitChatCapabilities(waitCtx, session) }()

	select {
	case err := <-errCh:
		t.Fatalf("装载未完成时 await 不应返回，实际 err=%v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("装载完成后 await 应返回 nil，实际 err=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("装载完成后 await 未返回")
	}
	if attachCalls != 1 {
		t.Fatalf("挂载阶段应恰好执行一次（它是唯一写 session 能力字段的地方），实际 %d 次", attachCalls)
	}
}

func TestRunChatCapabilitiesLoad_ErrorReachesFirstTurn(t *testing.T) {
	session := &ChatSession{}
	boom := errors.New("boom")
	installChatCapabilitiesGate(session,
		func() (*chatCapabilityDiscovery, error) { return nil, boom },
		func(*chatCapabilityDiscovery) (func(), error) {
			t.Error("发现失败时不得执行挂载（会拿不到 discovery）")
			return nil, nil
		},
	)

	runChatCapabilitiesLoad(session)

	err := awaitChatCapabilitiesForTurn(context.Background(), session)
	if !errors.Is(err, boom) {
		t.Fatalf("装载错误必须原样交给首个 turn（空工具面比报错更糟），实际 err=%v", err)
	}
}

func TestAwaitChatCapabilitiesForTurn_NoGateIsNoOp(t *testing.T) {
	// 同步路径 / runtime-server 路径 / 测试路径都不安装门控，必须保持原行为。
	if err := awaitChatCapabilitiesForTurn(context.Background(), &ChatSession{}); err != nil {
		t.Fatalf("未安装门控时必须是 no-op，实际 err=%v", err)
	}
}

func TestAwaitChatCapabilities_AttachRunsOnceUnderConcurrentWaiters(t *testing.T) {
	session := &ChatSession{}
	var mu sync.Mutex
	attachCalls := 0
	installChatCapabilitiesGate(session,
		func() (*chatCapabilityDiscovery, error) { return &chatCapabilityDiscovery{}, nil },
		func(*chatCapabilityDiscovery) (func(), error) {
			mu.Lock()
			attachCalls++
			mu.Unlock()
			return func() {}, nil
		},
	)
	go runChatCapabilitiesLoad(session)

	// 多个并发 await 者（首帧后用户连按几次提交）只能触发一次挂载：挂载是唯一
	// 写 session 能力字段的地方，重复执行会和其它写入打架。
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := awaitChatCapabilities(context.Background(), session); err != nil {
				t.Errorf("await 失败: %v", err)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	got := attachCalls
	mu.Unlock()
	if got != 1 {
		t.Fatalf("并发 await 下挂载阶段必须只执行一次，实际 %d 次", got)
	}
}

func TestChatStartupProgress_RendersPhaseAndStopwatch(t *testing.T) {
	coordinator := newStartupProgressTestCoordinator(t)

	if !coordinator.ShowChatStartupProgress(chatStartupProgressPhaseTools) {
		t.Fatal("交互式会话应接受启动进度")
	}
	coordinator.mu.Lock()
	coordinator.startupProgress.started = time.Now().Add(-3 * time.Second)
	coordinator.mu.Unlock()

	model := coordinator.buildChatStartupProgressModelLocked(80, time.Now())
	if model == nil {
		t.Fatal("启动进度应渲染出动态栏模型")
	}
	if !strings.Contains(model.StateText, chatStartupProgressPhaseTools) {
		t.Fatalf("状态行应含阶段名 %q，实际 %q", chatStartupProgressPhaseTools, model.StateText)
	}
	if !strings.Contains(model.StateText, "3s") {
		t.Fatalf("状态行应含秒表 3s，实际 %q", model.StateText)
	}
	if style.StatusLineBlank(*model) {
		t.Fatal("启动进度行不得为空行（否则会预留一行却不显示内容）")
	}
}

func TestChatStartupProgress_ClearRemovesRow(t *testing.T) {
	coordinator := newStartupProgressTestCoordinator(t)
	coordinator.ShowChatStartupProgress(chatStartupProgressPhaseRuntime)

	coordinator.ClearChatStartupProgress()
	coordinator.mu.Lock()
	active := coordinator.startupProgressActiveLocked()
	coordinator.mu.Unlock()
	if active {
		t.Fatal("清除后不应再有启动进度（否则秒表停在最后一个阶段）")
	}
	// 重复清除必须是 no-op（错误路径也无条件清除）。
	coordinator.ClearChatStartupProgress()
}

func TestChatStartupProgress_SamePhaseKeepsStopwatch(t *testing.T) {
	coordinator := newStartupProgressTestCoordinator(t)
	coordinator.ShowChatStartupProgress(chatStartupProgressPhaseTools)
	coordinator.mu.Lock()
	first := coordinator.startupProgress.started
	coordinator.mu.Unlock()

	coordinator.ShowChatStartupProgress(chatStartupProgressPhaseTools)
	coordinator.mu.Lock()
	second := coordinator.startupProgress.started
	coordinator.mu.Unlock()

	if !first.Equal(second) {
		t.Fatal("同一阶段重复推进不得重置秒表起点（秒表会跳变）")
	}
}

func TestChatStartupProgress_DifferentPhaseRestartsStopwatch(t *testing.T) {
	coordinator := newStartupProgressTestCoordinator(t)
	coordinator.ShowChatStartupProgress(chatStartupProgressPhaseTools)
	coordinator.mu.Lock()
	first := coordinator.startupProgress.started
	coordinator.mu.Unlock()

	coordinator.ShowChatStartupProgress(chatStartupProgressPhaseSkills)
	coordinator.mu.Lock()
	second := coordinator.startupProgress.started
	phase := coordinator.startupProgress.phase
	coordinator.mu.Unlock()

	if phase != chatStartupProgressPhaseSkills {
		t.Fatalf("换阶段后应展示新阶段名，实际 %q", phase)
	}
	// 断言「不早于旧起点」而不是「严格晚于」：Windows 上 time.Now() 分辨率较粗，
	// 两次相邻调用可能取到同一时刻，严格 Before 会偶发失败。真正要锁住的不变式是
	// 秒表没有沿用旧阶段的起点（沿用会让秒表继续累加，掩盖阶段边界）。
	if second.Before(first) {
		t.Fatal("换阶段不得把秒表起点回退到旧阶段之前")
	}
}

func TestChatStartupProgress_YieldsToForegroundActivity(t *testing.T) {
	coordinator := newStartupProgressTestCoordinator(t)
	coordinator.ShowChatStartupProgress(chatStartupProgressPhaseTools)

	// 模拟前台 turn 正在运行：启动后台装载必须让出动态栏。
	coordinator.streamingActive = true
	coordinator.mu.Lock()
	model := coordinator.applyStartupProgressLocked(&style.StatusLineModel{StateText: "Streaming"})
	coordinator.mu.Unlock()
	if strings.Contains(model.StateText, chatStartupProgressPhaseTools) {
		t.Fatal("前台活动存在时启动进度必须让位，不能覆盖用户回合状态行")
	}
}

func TestChatStartupProgress_NonInteractiveSessionIsNoOp(t *testing.T) {
	coordinator := newStartupProgressTestCoordinator(t)
	coordinator.session.NoInteractive = true
	if coordinator.ShowChatStartupProgress(chatStartupProgressPhaseTools) {
		t.Fatal("非交互会话没有动态栏，必须返回 false 而不是写裸字节")
	}
}

func TestChatStartupProgress_AdvanceOnlyWhenAlreadyActive(t *testing.T) {
	coordinator := newStartupProgressTestCoordinator(t)
	// 同步路径（initializeChatCapabilities 直接进发现）没有后台装载：
	// 阶段推进必须是 no-op，不能凭空在动态栏上冒出一条没人清的
	// 「加载 Skills」。
	if coordinator.advanceChatStartupProgress(chatStartupProgressPhaseSkills) {
		t.Fatal("未在展示启动进度时，阶段推进必须是 no-op")
	}
	coordinator.mu.Lock()
	active := coordinator.startupProgressActiveLocked()
	coordinator.mu.Unlock()
	if active {
		t.Fatal("阶段推进不得创建启动进度")
	}
}

func TestChatStartupProgress_AdvanceSwitchesPhaseAndRestartsStopwatch(t *testing.T) {
	coordinator := newStartupProgressTestCoordinator(t)
	coordinator.ShowChatStartupProgress(chatStartupProgressPhaseTools)
	coordinator.mu.Lock()
	first := coordinator.startupProgress.started
	coordinator.mu.Unlock()

	if !coordinator.advanceChatStartupProgress(chatStartupProgressPhaseSkills) {
		t.Fatal("已在展示启动进度时，阶段推进应生效")
	}
	coordinator.mu.Lock()
	second := coordinator.startupProgress.started
	phase := coordinator.startupProgress.phase
	coordinator.mu.Unlock()

	if phase != chatStartupProgressPhaseSkills {
		t.Fatalf("阶段推进后应展示新阶段名，实际 %q", phase)
	}
	// 断言「不早于旧起点」而不是「严格晚于」：Windows 上 time.Now()
	// 分辨率较粗，两次相邻调用可能取到同一时刻。
	if second.Before(first) {
		t.Fatal("换阶段不得把秒表起点回退到旧阶段之前")
	}

	// 同阶段再推进：保留起点，不重置秒表。
	coordinator.mu.Lock()
	third := coordinator.startupProgress.started
	coordinator.mu.Unlock()
	coordinator.advanceChatStartupProgress(chatStartupProgressPhaseSkills)
	coordinator.mu.Lock()
	fourth := coordinator.startupProgress.started
	coordinator.mu.Unlock()
	if !third.Equal(fourth) {
		t.Fatal("同阶段推进不得重置秒表起点（秒表会跳变）")
	}
}

func TestRunChatCapabilitiesLoad_ClearsProgressOnSuccess(t *testing.T) {
	coordinator := newStartupProgressTestCoordinator(t)
	session := coordinator.session
	installChatCapabilitiesGate(session,
		func() (*chatCapabilityDiscovery, error) { return &chatCapabilityDiscovery{}, nil },
		func(*chatCapabilityDiscovery) (func(), error) { return func() {}, nil },
	)
	showChatStartupProgress(session, chatStartupProgressPhaseTools)
	coordinator.mu.Lock()
	shown := coordinator.startupProgressActiveLocked()
	coordinator.mu.Unlock()
	if !shown {
		t.Fatal("交互式会话应接受启动进度")
	}

	runChatCapabilitiesLoad(session)

	// 发现成功后必须撤行：否则用户不提交 turn 就会一直盯着
	// 「加载工具 (Ns)」。这是本用例锁住的不变式。
	coordinator.mu.Lock()
	active := coordinator.startupProgressActiveLocked()
	coordinator.mu.Unlock()
	if active {
		t.Fatal("发现成功后启动进度必须撤行（否则秒表停在最后一个阶段）")
	}
}

// newStartupProgressTestCoordinator 构造一个带动态栏的最小 coordinator。
func newStartupProgressTestCoordinator(t *testing.T) *chatInteractionCoordinator {
	t.Helper()
	session := &ChatSession{
		Provider:  testChatProvider(),
		cancelCtx: newTestChatContext(),
		InputBox:  ui.NewInputBox(nil),
	}
	session.Interaction = newTestChatInteractionCoordinator(t, session)
	session.Interaction.session = session
	return session.Interaction
}
