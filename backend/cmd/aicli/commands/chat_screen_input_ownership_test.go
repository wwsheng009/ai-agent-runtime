package commands

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// 2026-10-18 入口审计 R5：副屏（L0）持有租约期间必须独占 ESC——
// KeyHandler 让位（否则 50ms 轮询会抢走副屏的 Esc/中断后台回合），
// 且状态行/Web payload 不再承诺 esc to interrupt；关闭后只在仍有回合级
// 消费者时重新 Arm。
func TestOpenChatScreenTakesEscOwnershipFromKeyHandler(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)
	session := newChatScreenTestSession(t)
	kh := ui.NewKeyHandler()
	kh.Start()
	t.Cleanup(kh.Stop)
	session.KeyHandler = kh
	stopConsumer := startChatEscapeInterruptWatcher(session)
	defer stopConsumer()
	if !kh.Armed() || !chatEscapeInterruptAvailable(session) {
		t.Fatal("precondition: 活跃回合消费者必须 armed 且承诺可中断")
	}

	var armedInside, availableInside bool
	chatScreenDocumentRunner = func(_ *ChatSession, _ ui.ScreenLease, _ ui.DebugOverlayOptions) error {
		armedInside = kh.Armed()
		availableInside = chatEscapeInterruptAvailable(session)
		return nil
	}
	_ = openChatScreen(session, documentScreenSpec("test.esc-ownership"))

	if armedInside {
		t.Fatal("副屏持有租约期间 KeyHandler 必须让位（Disarm）")
	}
	if availableInside {
		t.Fatal("副屏持有租约期间不得承诺 esc to interrupt")
	}
	if !kh.Armed() {
		t.Fatal("副屏关闭后仍有回合级消费者时必须重新 Arm")
	}

	// 消费者在副屏存续期间结束：关闭后不得留下无人消费的 armed 状态
	// （那会把之后的 Esc 静默吞掉）。
	secondConsumer := startChatEscapeInterruptWatcher(session)
	chatScreenDocumentRunner = func(_ *ChatSession, _ ui.ScreenLease, _ ui.DebugOverlayOptions) error {
		secondConsumer()
		stopConsumer() // 释放最后一个（defer 再调幂等）
		return nil
	}
	_ = openChatScreen(session, documentScreenSpec("test.esc-ownership-ended"))
	if kh.Armed() {
		t.Fatal("消费者已结束，副屏关闭后不得重新 Arm KeyHandler")
	}
}
