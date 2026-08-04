package commands

import (
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// newPaintTraceDebugSession wires a real surface with the interaction
// coordinator so surface.SetEngine runs (the paint probe lives on the engine).
func newPaintTraceDebugSession(t *testing.T) (*ChatSession, *ui.FixedBottomSurface) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	session := &ChatSession{
		ProviderName: "openai",
		Model:        "gpt-test",
		Surface:      surface,
	}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord
	coord.SetSurface(surface)
	return session, surface
}

// TestDebugOnOffTogglesPaintTrace pins the /debug on|off wiring: enabling the
// debug mode arms the render paint reconciliation probe so rendering
// anomalies are captured while the symptom is reproduced.
func TestDebugOnOffTogglesPaintTrace(t *testing.T) {
	session, surface := newPaintTraceDebugSession(t)

	captureSurfaceStdout(t, func() {
		if dispatchChatCommand(session, "/debug on", false) {
			t.Fatal("/debug on unexpectedly requested chat exit")
		}
	})
	if !session.DebugMode {
		t.Fatal("/debug on must set session debug mode")
	}
	// The probe must be armed: a report is available even with no events.
	if report := surface.PaintTraceDebugString(); report == "" {
		t.Fatal("/debug on must arm the paint trace (report expected)")
	}
	captureSurfaceStdout(t, func() {
		if dispatchChatCommand(session, "/debug off", false) {
			t.Fatal("/debug off unexpectedly requested chat exit")
		}
	})
	if session.DebugMode {
		t.Fatal("/debug off must clear session debug mode")
	}
	// Disabling keeps the accumulated report (reproduce first, inspect after).
	if report := surface.PaintTraceDebugString(); report == "" {
		t.Fatal("/debug off must keep the accumulated paint trace")
	}
}

// TestDebugDisplayIncludesRenderPaintTrace pins that /debug display surfaces
// the paint reconciliation report (white repaints / missing coverage) once
// the probe has recorded events.
func TestDebugDisplayIncludesRenderPaintTrace(t *testing.T) {
	session, surface := newPaintTraceDebugSession(t)

	captureSurfaceStdout(t, func() {
		if dispatchChatCommand(session, "/debug on", false) {
			t.Fatal("/debug on unexpectedly requested chat exit")
		}
	})
	// Produce painted frames through the surface so the probe records events.
	captureSurfaceStdout(t, func() {
		surface.ShowPrompt("> ")
		surface.WriteOutput(os.Stdout, "line-1\nline-2\n")
	})

	captureSurfaceStdout(t, func() {
		if dispatchChatCommand(session, "/debug display", false) {
			t.Fatal("/debug display unexpectedly requested chat exit")
		}
	})
	plain := ui.RenderDocumentPlain(buildChatDebugDisplayDocument(session))
	for _, marker := range []string{"Render Paint Trace:", "Paint Trace: frames=", "emits", "white", "miss"} {
		if !strings.Contains(plain, marker) {
			t.Errorf("/debug display document missing %q:\n%s", marker, plain)
		}
	}
}

// TestDebugDisplayNoRenderPaintTraceWithoutEvents pins the empty state: with
// the probe armed but nothing painted yet, the display explains the no-events
// condition instead of omitting the section silently.
func TestDebugDisplayNoRenderPaintTraceWithoutEvents(t *testing.T) {
	session, _ := newPaintTraceDebugSession(t)

	captureSurfaceStdout(t, func() {
		if dispatchChatCommand(session, "/debug on", false) {
			t.Fatal("/debug on unexpectedly requested chat exit")
		}
	})
	plain := ui.RenderDocumentPlain(buildChatDebugDisplayDocument(session))
	if !strings.Contains(plain, "no events recorded") {
		t.Errorf("empty trace must explain the no-events state:\n%s", plain)
	}
}

// TestDebugDisplayIncludesAppStatePresenterDiagnostics keeps the migration
// audit surface source-backed: it reports reducer state and an in-memory
// AppState-to-legacy-frame comparison, without treating historyWindow or the
// native terminal scrollback as a semantic source.
func TestDebugDisplayIncludesAppStatePresenterDiagnostics(t *testing.T) {
	session, _ := newPaintTraceDebugSession(t)
	coordinator := session.Interaction
	if coordinator == nil {
		t.Fatal("test session must install an interaction coordinator")
	}
	if !coordinator.postUIAction(ui.Resize{Width: 80, Height: 24, Generation: 1}) {
		t.Fatal("post resize action")
	}
	coordinator.waitUIActorIdle()

	plain := ui.RenderDocumentPlain(buildChatDebugDisplayDocument(session))
	for _, marker := range []string{
		"AppState / Presenter Migration:",
		"UI Revision:",
		"Layout Generation:",
		"Geometry:",
		"Primary Lease:",
		"History Effects:",
		"History Projection:",
		"AppState Frame Parity:",
		"parity:",
	} {
		if !strings.Contains(plain, marker) {
			t.Errorf("/debug display document missing %q:\n%s", marker, plain)
		}
	}
}
