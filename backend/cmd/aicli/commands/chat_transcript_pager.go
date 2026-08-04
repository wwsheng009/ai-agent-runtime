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
		_ = session.Interaction.postUIAction(ui.OpenTranscriptOverlay{LeaseID: lease.ID()})
	}
	defer func() {
		if session.Interaction != nil {
			_ = session.Interaction.postUIAction(ui.CloseTranscriptOverlay{LeaseID: lease.ID()})
		}
		_ = lease.Release(context.Background())
	}()

	_ = ui.RunTranscriptPagerWithLease(context.Background(), resumeFullScreenTerminal(session), ui.TranscriptPagerOptions{
		Snapshot: func() ui.TranscriptPagerSnapshot {
			return chatTranscriptPagerSnapshot(session)
		},
	}, lease)
}

func chatTranscriptPagerSnapshot(session *ChatSession) ui.TranscriptPagerSnapshot {
	var transcript ui.TranscriptState
	if session != nil && session.RuntimeEventBridge != nil {
		transcript = ui.NewTranscriptState(session.RuntimeEventBridge.sceneSnapshot())
	}
	active, activeOK := ui.ActiveCellFromTranscript(transcript)
	if session != nil && session.Interaction != nil && session.Interaction.uiActor != nil {
		state := session.Interaction.uiActor.AppState()
		if transcript.Revision == 0 && state.Transcript.Revision != 0 {
			transcript = state.Transcript
			active = state.Active
			activeOK = active.Phase != ui.ActiveCellInactive
		}
		if state.Active.Phase != ui.ActiveCellInactive &&
			activeOK && state.Active.CellID == active.CellID && state.Active.Revision >= active.Revision {
			active = state.Active
			activeOK = true
		}
	}
	if !activeOK {
		active = ui.ActiveCellState{}
	}
	return ui.TranscriptPagerSnapshot{Transcript: transcript, Active: active}
}
