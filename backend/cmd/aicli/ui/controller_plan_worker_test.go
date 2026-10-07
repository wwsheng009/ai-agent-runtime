package ui

import (
	"bytes"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// TestAsyncPlanWorkerConvergesResumedSessionThroughActor 是最接近生产的端到端：
// 控制器开启 plan worker，screening 在 worker 上无预算单遍完成，executor 负责
// 交付/ack，最终必须收敛且 finalized cell 全覆盖——与既有同步 E2E 的断言一致。
func TestAsyncPlanWorkerConvergesResumedSessionThroughActor(t *testing.T) {
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
			diag := controller.HistoryEffectDiagnostics()
			if diag.PlanWindowsDelegated == 0 || diag.PlanRequestInFlight {
				t.Fatalf("委派读数异常：windows=%d inFlight=%t（sink 未注入或未收敛）",
					diag.PlanWindowsDelegated, diag.PlanRequestInFlight)
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

// TestAsyncPlanWorkerDoesNotSpinOnUnchangedInputs 回归：从不 Resize 的会话
// （几何 0×0）里任何规划需求都会派发，结果必须按与同步路径相同的语义直接结算；
// 若结果栅栏对"持久不变的状态"（几何未知/冻结/投影未知）反复判 stale 并重派发，
// actor 会陷入 result→dispatch→result 的无界热循环（生产表现为 WaitIdle 永不返回、
// actor 永久 busy；本用例在修复前挂死 10 分钟）。
func TestAsyncPlanWorkerDoesNotSpinOnUnchangedInputs(t *testing.T) {
	controller := NewUIController(UIControllerConfig{MailboxSize: 64, AsyncTranscriptPlan: true}, nil, nil)
	go controller.Run()
	t.Cleanup(func() {
		controller.Close()
		controller.WaitIdle()
	})

	if !controller.Post(SetActiveCellAction{Active: ActiveCellState{
		CellID:   41,
		Revision: 7,
		Kind:     scene.KindAssistant,
		Phase:    ActiveCellMutable,
		Source:   "partial streamed body text",
		Stable:   SourceRange{Start: 0, End: 22},
		Enqueued: SourceRange{Start: 0, End: 22},
		Acked:    SourceRange{Start: 0, End: 8},
	}}) {
		t.Fatal("post active cell mount")
	}
	if !controller.WaitIdleTimeout(10 * time.Second) {
		t.Fatal("持续性恢复门不得把 actor 打成 result→dispatch 热循环")
	}
	state := controller.State()
	if state.HistoryEffects.planRequestInFlight {
		t.Fatal("恢复门下的陈旧结果必须结算，不能保持在飞")
	}
	if state.HistoryEffects.PlanIncomplete {
		t.Fatal("几何未知时不得留下 incomplete 计划（应由后续 Resize 重新触发）")
	}
}
