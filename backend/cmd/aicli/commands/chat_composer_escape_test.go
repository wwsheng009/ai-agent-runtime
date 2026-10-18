package commands

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// 2026-10-18 现场（session_20261018191622_xGGs4QZJ）：actor / goal 续跑 /
// supervision wake 回合没有 busy capture 抢占 stdin，主循环 composer 才是真正
// 的读侧。旧实现在该窗口把 Esc 当弹层取消/回溯/无操作吞掉（KeyHandler 的 peek
// 轮询通常输给 composer 的读），状态行却承诺 "esc to interrupt"——429 重试的
// 后台回合因此完全无法中断。
//
// 本用例锁定修复后的契约：存在活跃 ESC 消费者时，composer 的 OnCancel 必须
// 认领按键并把中断交给同一决策点，且草稿在 ReadLine 收尾后原样恢复。
func TestChatComposerEscInterruptsBackgroundTurnAndPreservesDraft(t *testing.T) {
	kh := ui.NewKeyHandler()
	kh.Start()
	defer kh.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &ChatSession{
		KeyHandler: kh,
		cancelCtx:  ctx,
		cancelFunc: cancel,
	}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord
	coord.SetPromptInput("half typed follow-up")

	// 后台回合（actor / goal 续跑 / wake）注册会话级消费者。
	stopConsumer := startChatEscapeInterruptWatcher(session)
	defer stopConsumer()

	controller := newChatComposerController(session)
	hooks := controller.hooks()
	if hooks.InitialText != "half typed follow-up" {
		t.Fatalf("composer InitialText = %q，期望已有草稿", hooks.InitialText)
	}

	// 编辑器 Esc 语义：先 OnCancel(snapshot)；被认领后 editor 追加
	// onChange("")（inputbox_editor.go editorKeyCancelPopup 分支）。
	draft := coord.PromptInputSnapshot()
	if !hooks.OnCancel(draft) {
		t.Fatal("存在活跃 ESC 消费者时，composer Esc 必须被中断路径认领")
	}
	hooks.OnChange(ui.LineEditorSnapshot{})
	controller.restoreCancelledDraft() // ReadLine 的草稿回填步骤

	if !session.IsInterrupted() {
		t.Fatal("后台回合的 composer Esc 必须中断当前回合")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("后台回合的 composer Esc 必须取消当前回合的 context")
	}

	restored := coord.PromptInputSnapshot()
	if restored.Text != draft.Text || restored.Cursor != draft.Cursor {
		t.Fatalf("Esc 中断后草稿丢失：%#v，期望 %#v", restored, draft)
	}
	next := newChatComposerController(session).hooks()
	if next.InitialText != draft.Text || next.InitialCursor != draft.Cursor {
		t.Fatalf("下次 composer InitialText = %q cursor = %d，期望 %q/%d",
			next.InitialText, next.InitialCursor, draft.Text, draft.Cursor)
	}
}

// 空闲（无活跃消费者）时保持原有语义：OnCancel 不认领按键，交给编辑器的
// popup 取消 / 空草稿回溯 / 有草稿无操作路径处理，且不得产生中断。
func TestChatComposerEscWithoutConsumerKeepsEditorSemantics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &ChatSession{cancelCtx: ctx, cancelFunc: cancel}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord

	controller := newChatComposerController(session)
	if controller.hooks().OnCancel(coord.PromptInputSnapshot()) {
		t.Fatal("无 ESC 消费者时 composer 不得认领 Esc（回溯语义必须保留）")
	}
	if session.IsInterrupted() {
		t.Fatal("无 ESC 消费者时 composer Esc 不得中断回合")
	}
	select {
	case <-ctx.Done():
		t.Fatal("无 ESC 消费者时 composer Esc 不得取消 context")
	default:
	}
}
