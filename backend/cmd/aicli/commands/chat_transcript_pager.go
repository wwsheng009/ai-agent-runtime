package commands

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// canOpenChatTranscriptPager is intentionally strict. Ctrl+T is only claimed
// by the owned interactive chat surface; a popup/approval or another alternate
// screen keeps its current input owner and Ctrl+T retains editor transpose.
func canOpenChatTranscriptPager(session *ChatSession) bool {
	if session == nil || session.NoInteractive || session.JSONOutput || session.Surface == nil {
		return false
	}
	if !session.Surface.Enabled() || !session.Surface.OwnedViewport() ||
		session.Surface.LeaseActive() || session.Surface.HasActivePopup() {
		return false
	}
	return ui.CanUseFullScreenList(resumeFullScreenTerminal(session))
}

// chatScreenTranscriptSpec 是 /history（统一 TUI 出口）与 Ctrl+T 的只读
// ScreenDocument Spec：渲染复用既有 transcript 分页器，租约与 close 屏障
// 由框架统一持有（批次 1 收编；§2.4 的行内租约实现之一）。
//
// SilentDegrade 保持旧行为：能力不足时 transcript 不打开、不抢键，也不在主屏
// 刷提示（只记 degrade 事件与计数器）。
func chatScreenTranscriptSpec(trigger string) chatScreenSpec {
	return chatScreenSpec{
		ID:            "transcript.screen",
		Title:         "Transcript",
		Kind:          chatScreenDocument,
		Doc:           textLinesDocument([]string{"Transcript"}),
		Trigger:       trigger,
		SilentDegrade: true,
		RunDocument:   runChatTranscriptScreen,
	}
}

// runChatTranscriptScreen 在框架租约内运行 transcript 分页器。
//
// 首帧屏障：先把语义快照与 overlay 状态发布给 actor，再进入分页器，保证
// 刚在 /history 中恢复的历史不会以"空屏"开屏。退出时关闭 overlay 状态；
// 租约释放与 primary repaint 由框架的 close 序列（post Close → release →
// actor idle）统一完成。
func runChatTranscriptScreen(session *ChatSession, lease ui.ScreenLease, _ chatScreenSpec) error {
	if session == nil || session.Interaction == nil || lease == nil {
		return errChatScreenRenderNotReady
	}
	session.Interaction.postTranscriptSnapshotFromBridge(session.RuntimeEventBridge)
	_ = session.Interaction.postUIAction(ui.OpenTranscriptOverlay{LeaseID: lease.ID()})
	if !session.Interaction.waitUIActorIdleBounded("open transcript overlay") {
		return errChatScreenRenderNotReady
	}
	defer func() {
		_ = session.Interaction.postUIAction(ui.CloseTranscriptOverlay{LeaseID: lease.ID()})
	}()

	_ = ui.RunTranscriptPagerWithLease(context.Background(), resumeFullScreenTerminal(session), ui.TranscriptPagerOptions{
		View: func() ui.TranscriptPagerView {
			return chatTranscriptPagerView(session, lease.ID())
		},
		PostAction: func(action ui.UIAction) bool {
			if session == nil || session.Interaction == nil {
				return false
			}
			return session.Interaction.postUIAction(action)
		},
	}, lease)
	return nil
}

// openChatTranscriptPager 是 Ctrl+T 入口：claim 条件保持不变（不能开屏时不
// 抢 Ctrl+T 的 transpose 语义）。批次 5 起统一走框架 Spec（§6.1 legacy 分支
// 已删除）。
func openChatTranscriptPager(session *ChatSession) {
	if !canOpenChatTranscriptPager(session) {
		return
	}
	openChatScreen(session, chatScreenTranscriptSpec("hotkey"))
}

func chatTranscriptPagerView(session *ChatSession, leaseID uint64) ui.TranscriptPagerView {
	if session == nil || session.Interaction == nil {
		return ui.TranscriptPagerView{}
	}
	actor := session.Interaction.currentUIActor()
	if actor == nil {
		return ui.TranscriptPagerView{}
	}
	state := actor.AppState()
	active := state.Active
	if active.Phase == ui.ActiveCellInactive {
		active = ui.ActiveCellState{}
	}
	return ui.TranscriptPagerView{
		Snapshot:   ui.TranscriptPagerSnapshot{Transcript: state.Transcript, Active: active},
		Pager:      state.TranscriptOverlay.Pager,
		PagerKnown: state.TranscriptOverlay.Active && state.TranscriptOverlay.LeaseID == leaseID,
	}
}
