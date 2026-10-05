package ui

import (
	"bytes"
	"testing"
	"time"
)

// TestAsyncPlanWorkerConvergesResumedSessionThroughActor 是最接近生产的端到端：
// 控制器开启 plan worker，0 预算强制多轮截断，executor 负责交付/ack/kick，最终
// 必须收敛且 finalized cell 全覆盖——与既有同步 E2E 的断言一致。
func TestAsyncPlanWorkerConvergesResumedSessionThroughActor(t *testing.T) {
	restoreBudget := historyCommitPlanningBudget
	defer func() { historyCommitPlanningBudget = restoreBudget }()
	historyCommitPlanningBudget = 0

	controller := NewUIController(UIControllerConfig{MailboxSize: 256, AsyncTranscriptPlan: true}, nil, nil)
	go controller.Run()
	physical := &bytes.Buffer{}
	executor := NewTerminalSessionExecutor(controller, NewTerminalSession(physical))
	t.Cleanup(func() {
		executor.Close()
		controller.Close()
		controller.WaitIdle()
		if !controller.WaitPlanWorker(5 * time.Second) {
			t.Error("plan worker 没有在 Close 后退出")
		}
	})

	if !controller.Post(Resize{Width: 90, Height: 40, Generation: 1}) {
		t.Fatal("post resize")
	}
	if !controller.Post(ReplaceTranscriptAction{Snapshot: resumeParitySnapshot()}) {
		t.Fatal("post transcript")
	}

	deadline := time.Now().Add(90 * time.Second)
	for {
		executor.Request()
		executor.WaitIdle()
		state := controller.State()
		if !state.HistoryEffects.PlanIncomplete && !state.HistoryEffects.planRequestInFlight {
			assertHistoryCoversFinalizedCells(t, state)
			if state.HistoryEffects.planRequestSeq == 0 {
				t.Fatal("没有观察到任何 worker 窗口请求（sink 未注入？）")
			}
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("异步 E2E 未收敛：incomplete=%t inFlight=%t seq=%d pending=%d",
				state.HistoryEffects.PlanIncomplete, state.HistoryEffects.planRequestInFlight,
				state.HistoryEffects.planRequestSeq, len(state.HistoryEffects.Pending()))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestWaitIdleWaitsForDelegatedScreening 锁定生命周期修正：未 Close 时在飞请求
// 计入繁忙（否则装载/覆盖度断言读到半应用状态）；Close 后不再等待（结果不会
// 再回来，等待会让既有 teardown 挂死）。
func TestWaitIdleWaitsForDelegatedScreening(t *testing.T) {
	controller := NewUIController(UIControllerConfig{MailboxSize: 8, AsyncTranscriptPlan: true}, nil, nil)

	controller.mu.Lock()
	controller.state.HistoryEffects.planRequestInFlight = true
	controller.mu.Unlock()
	if controller.WaitIdleTimeout(50 * time.Millisecond) {
		t.Fatal("在飞 screening 期间 WaitIdleTimeout 不得报告空闲")
	}

	controller.mu.Lock()
	controller.state.HistoryEffects.planRequestInFlight = false
	controller.mu.Unlock()
	if !controller.WaitIdleTimeout(time.Second) {
		t.Fatal("结算后 WaitIdleTimeout 必须报告空闲")
	}

	// Close 之后不得为在飞状态等待。
	controller.mu.Lock()
	controller.state.HistoryEffects.planRequestInFlight = true
	controller.closed = true
	controller.mu.Unlock()
	if !controller.WaitIdleTimeout(200 * time.Millisecond) {
		t.Fatal("Close 后 WaitIdleTimeout 不得因在飞状态挂住")
	}
}

// TestPlanWorkerStopsOnCloseAndIsIdempotent Close 必须停止 worker 且重复调用安全。
func TestPlanWorkerStopsOnCloseAndIsIdempotent(t *testing.T) {
	controller := NewUIController(UIControllerConfig{MailboxSize: 8, AsyncTranscriptPlan: true}, nil, nil)
	go controller.Run()
	if !controller.Post(Resize{Width: 80, Height: 24, Generation: 1}) {
		t.Fatal("post resize")
	}
	controller.WaitIdle()
	controller.Close()
	controller.Close()
	if !controller.WaitPlanWorker(5 * time.Second) {
		t.Fatal("Close 后 worker 必须在超时内退出")
	}
	controller.WaitIdle() // Close 后不得因在飞状态挂死
}

// TestAsyncPlanWorkerDisabledByDefault 锁定默认路径：未开启配置时 sink 为 nil，
// 规划完全走 Stage A 的锁内同步实现（既有 wiring 与测试零变化）。
func TestAsyncPlanWorkerDisabledByDefault(t *testing.T) {
	controller := NewUIController(UIControllerConfig{MailboxSize: 64}, nil, nil)
	go controller.Run()
	t.Cleanup(func() {
		controller.Close()
		controller.WaitIdle()
	})
	if !controller.Post(Resize{Width: 90, Height: 40, Generation: 1}) {
		t.Fatal("post resize")
	}
	if !controller.Post(ReplaceTranscriptAction{Snapshot: resumeParitySnapshot()}) {
		t.Fatal("post transcript")
	}
	controller.WaitIdle()
	state := controller.State()
	if state.HistoryEffects.planRequestSeq != 0 || state.HistoryEffects.planRequestInFlight {
		t.Fatal("默认配置不得启用异步规划")
	}
}
