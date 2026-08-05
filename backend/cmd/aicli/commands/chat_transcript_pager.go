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

// openChatTranscriptPager transfers physical screen ownership to a short-lived
// alternate-screen pager. The primary surface remains suspended by ScreenLease
// and keeps receiving retained semantic updates; release performs one complete
// primary repaint from that retained state.
func openChatTranscriptPager(session *ChatSession) {
	if !canOpenChatTranscriptPager(session) {
		return
	}
	lease, err := session.Surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{
		Title: "Transcript",
	})
	if err != nil {
		return
	}
	if session.Interaction != nil {
		// Publish the latest semantic scene before opening the overlay. The
		// pager itself must read only actor-owned AppState after this barrier;
		// it must not race a Scene snapshot against a separate scroll state.
		session.Interaction.postTranscriptSnapshotFromBridge(session.RuntimeEventBridge)
		_ = session.Interaction.postUIAction(ui.OpenTranscriptOverlay{LeaseID: lease.ID()})
	}
	defer func() {
		if session.Interaction != nil {
			_ = session.Interaction.postUIAction(ui.CloseTranscriptOverlay{LeaseID: lease.ID()})
		}
		_ = lease.Release(context.Background())
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
}

func chatTranscriptPagerView(session *ChatSession, leaseID uint64) ui.TranscriptPagerView {
	if session == nil || session.Interaction == nil || session.Interaction.uiActor == nil {
		return ui.TranscriptPagerView{}
	}
	state := session.Interaction.uiActor.AppState()
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
