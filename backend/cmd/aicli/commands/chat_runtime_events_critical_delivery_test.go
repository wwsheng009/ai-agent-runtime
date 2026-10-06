package commands

import (
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// newStalledUIActorFixture builds a one-slot UI actor whose Run loop is not
// started yet, so the mailbox stays full for the whole retry window — the exact
// condition that dropped tool.completed in production
// (session_20260927073805_QbWBceF5, 2026-09-30 09:50:10).
func newStalledUIActorFixture(t *testing.T, sessionID string) (*chatRuntimeEventBridge, *ui.UIController, uint64) {
	t.Helper()
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: sessionID}}
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	actor := ui.NewUIController(ui.UIControllerConfig{MailboxSize: 1}, ui.ReducerFunc(func(uint64, ui.UIAction) []ui.Effect {
		return nil
	}), func(ui.Effect) {})
	coordinator.uiActorOnce.Do(func() { coordinator.publishUIActor(actor) })
	t.Cleanup(func() { actor.Close() })
	if !actor.TryPost(ui.Resize{}) {
		t.Fatal("failed to fill the one-slot mailbox")
	}
	if stats := actor.Stats(); stats.Pending != 1 {
		t.Fatalf("mailbox pending = %d, want 1 for a full mailbox", stats.Pending)
	}

	bridge := newChatRuntimeEventBridge(session)
	bridge.uiActionPostTimeout = 50 * time.Millisecond
	bridge.BeginRun()
	return bridge, actor, bridge.runEpoch
}

func toolCompletedEvent() runtimeevents.Event {
	return runtimeevents.Event{
		Type:    "tool.completed",
		TraceID: "trace-critical-tool",
		Payload: map[string]interface{}{
			"tool_call_id": "call-critical-1",
			"tool_name":    "edit",
			"turn_id":      "turn-critical-1",
		},
	}
}

// TestCriticalToolCompletedPostSurvivesStalledMailbox 锁定修复契约：
// tool.completed 属于 eventClassCritical（"不丢"），UI actor 邮箱拥塞时
// 必须有界等待后**重试**而不是静默丢弃。丢一条终态会让 Scene 的 tool-chain
// cell 永远 mutable，commit frontier 卡死、整个已提交显示冻结。
func TestCriticalToolCompletedPostSurvivesStalledMailbox(t *testing.T) {
	bridge, actor, epoch := newStalledUIActorFixture(t, "critical-tool-mailbox-full")

	type result struct {
		accepted bool
		legacyOK bool
	}
	done := make(chan result, 1)
	go func() {
		accepted, legacyOK := bridge.postRuntimeEventToUIActorWithEpoch(toolCompletedEvent(), epoch)
		done <- result{accepted: accepted, legacyOK: legacyOK}
	}()

	// 旧行为在 uiActionPostTimeout（50ms）后返回 (false,false)；修复后必须仍在重试。
	select {
	case got := <-done:
		t.Fatalf("critical tool.completed returned early: %+v, want bounded-wait retry", got)
	case <-time.After(300 * time.Millisecond):
	}

	// 邮箱腾出后必须真正送达（按序，而不是丢弃或走 legacy 兜底）。
	go actor.Run()
	select {
	case got := <-done:
		if !got.accepted || !got.legacyOK {
			t.Fatalf("critical tool.completed result = %+v, want accepted delivery", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("critical tool.completed was not delivered after the mailbox drained")
	}
}

// TestCriticalToolCompletedPostSurvivesStaleRunEpoch 锁定跨 run 退休语义：
// tool.completed 携带的 epoch 在下一轮 BeginRun 前被推进（旧 run 已结束），
// 也不能因为 epoch 检查被静默丢弃 —— 否则长 turn 收尾/新 turn 开始时的
// 终态同样丢失。
func TestCriticalToolCompletedPostSurvivesStaleRunEpoch(t *testing.T) {
	bridge, actor, epoch := newStalledUIActorFixture(t, "critical-tool-stale-epoch")

	// 模拟下一轮 BeginRun 推进 epoch：本事件所属 run 已经退休。
	bridge.renderMu.Lock()
	bridge.runEpoch++
	bridge.renderMu.Unlock()

	type result struct {
		accepted bool
		legacyOK bool
	}
	done := make(chan result, 1)
	go func() {
		accepted, legacyOK := bridge.postRuntimeEventToUIActorWithEpoch(toolCompletedEvent(), epoch)
		done <- result{accepted: accepted, legacyOK: legacyOK}
	}()

	select {
	case got := <-done:
		t.Fatalf("stale-epoch critical tool.completed returned early: %+v, want retry", got)
	case <-time.After(300 * time.Millisecond):
	}

	go actor.Run()
	select {
	case got := <-done:
		if !got.accepted || !got.legacyOK {
			t.Fatalf("stale-epoch critical tool.completed result = %+v, want accepted delivery", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale-epoch critical tool.completed was not delivered after the mailbox drained")
	}
}

// TestNonCriticalEventStillDropsAfterBoundedWait 是保护性对照：非 critical
// 事件（此处 tool.progress 为 coalescible）保持既有有界降级行为，修复没有
// 把所有事件都变成永久等待。
func TestNonCriticalEventStillDropsAfterBoundedWait(t *testing.T) {
	bridge, _, _ := newStalledUIActorFixture(t, "noncritical-drop")

	start := time.Now()
	accepted, legacyOK := bridge.postRuntimeEventToUIActorWithEpoch(runtimeevents.Event{
		Type: "tool.progress",
		Payload: map[string]interface{}{
			"tool_call_id": "call-progress-1",
			"tool_name":    "edit",
		},
	}, bridge.runEpoch)
	elapsed := time.Since(start)
	if accepted || legacyOK {
		t.Fatalf("non-critical post = (%v,%v), want (false,false) with full mailbox", accepted, legacyOK)
	}
	if elapsed < 40*time.Millisecond {
		t.Fatalf("non-critical post returned after %v; expected bounded retry before drop", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("non-critical post waited %v; expected bounded drop", elapsed)
	}
}
