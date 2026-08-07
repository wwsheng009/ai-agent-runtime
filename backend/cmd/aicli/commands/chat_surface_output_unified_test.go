package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func TestUnifiedDirectInteractiveOutputUsesSceneAndNeverFallsBackToSurfaceOrStdout(t *testing.T) {
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(64, 18)
	coordinator.SetSurface(surface)

	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	// The presenter's executor flushes the attach-time frame asynchronously;
	// wait for it before resetting so Reset cannot race the writer goroutine.
	awaitUnifiedPresenterIdle(t, coordinator)
	terminal.Reset()

	const visible = "unified direct notice"
	raw := captureStdout(t, func() {
		beginDirectInteractiveOutput(session)
		settleInteractiveOutputLayout(session)
		if !writeDirectInteractiveOutput(session, "\x1b[31m"+visible+"\x1b[0m\n") {
			t.Fatal("unified direct output was not claimed")
		}
		printDirectInteractiveOutput(session, "\n\r\n")
	})
	if raw != "" {
		t.Fatalf("unified direct output wrote raw stdout: %q", raw)
	}

	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.AppState()
	if len(state.Transcript.Cells) != 1 {
		t.Fatalf("transcript cells=%d, want exactly one visible semantic output", len(state.Transcript.Cells))
	}
	if !strings.Contains(terminal.String(), visible) {
		t.Fatalf("TerminalSession output missing semantic text %q: %q", visible, terminal.String())
	}
	if strings.Contains(terminal.String(), "\x1b[31m") {
		t.Fatalf("raw ANSI payload reached the unified terminal transaction: %q", terminal.String())
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified direct output populated legacy historyWindow: %#v", got)
	}
	if reserve := surface.LegacyReserveStateForTest(); reserve.ScrollCompensatedRows != 0 ||
		reserve.PendingScrollDownRows != 0 || reserve.OutputScrollDebtRows != 0 || reserve.CursorOnBlankRow {
		t.Fatalf("unified direct output entered legacy surface reserve lifecycle: %#v", reserve)
	}
}

func TestUnifiedDirectInteractiveOutputFailsClosedWithoutInteraction(t *testing.T) {
	session := &ChatSession{
		TerminalSession: ui.NewTerminalSession(&bytes.Buffer{}),
	}

	raw := captureStdout(t, func() {
		beginDirectInteractiveOutput(session)
		settleInteractiveOutputLayout(session)
		if !writeDirectInteractiveOutput(session, "must not reach stdout\n") {
			t.Fatal("unified teardown state must claim direct output")
		}
		printDirectInteractiveOutput(session, "must not reach stdout either\n")
	})
	if raw != "" {
		t.Fatalf("missing Interaction revived raw stdout writer: %q", raw)
	}
}

func TestUnifiedTeardownBlocksDirectToolAndShellCompatibilityEntrypoints(t *testing.T) {
	var terminal bytes.Buffer
	session := &ChatSession{
		TerminalSession: ui.NewTerminalSession(&terminal),
	}

	if handleDirectFunctionCommand(session, "/call example") {
		t.Fatal("direct function command unexpectedly requested exit")
	}
	if handleDirectSkillCommand(session, "/skill example prompt") {
		t.Fatal("direct skill command unexpectedly requested exit")
	}
	if _, err := executeShellCommandDetailed(session, "echo must-not-run"); err == nil {
		t.Fatal("unified shell compatibility entrypoint was not rejected")
	}
	if terminal.Len() != 0 {
		t.Fatalf("teardown compatibility entrypoint wrote TerminalSession bytes: %q", terminal.String())
	}
}
