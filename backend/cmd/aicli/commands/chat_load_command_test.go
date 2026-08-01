package commands

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// newLoadCommandTestManager 构造带一个已持久化会话（含历史）的 manager。
func newLoadCommandTestManager(t *testing.T, id string) *runtimechat.SessionManager {
	t.Helper()
	storage := runtimechat.NewInMemoryStorage()
	manager := runtimechat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)

	persisted := runtimechat.NewSession("tester")
	persisted.ID = id
	persisted.Metadata.Title = "Load fixture"
	persisted.ReplaceHistory(historyCommandTranscriptFixture())
	if err := storage.Save(context.Background(), persisted); err != nil {
		t.Fatalf("save load fixture: %v", err)
	}
	return manager
}

func TestTryExecuteStructuredChatCommandLoad(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	manager := newLoadCommandTestManager(t, "load-structured-session")
	session := newHistoryCommandTestSession(manager)

	result, handled, err := tryExecuteStructuredChatCommand(session, "/load load-structured-session")
	if err != nil {
		t.Fatalf("/load structured error: %v", err)
	}
	if !handled {
		t.Fatal("/load with existing session was not handled as structured")
	}
	if result.Action != CommandContinue {
		t.Fatalf("/load action=%v want CommandContinue", result.Action)
	}
	if !result.ReplayHistory {
		t.Fatal("/load should request history replay (fixture has messages)")
	}
	if len(result.Blocks) != 1 {
		t.Fatalf("/load blocks=%d want 1", len(result.Blocks))
	}
	plain := ui.RenderDocumentPlain(result.Document())
	for _, marker := range []string{
		"会话已加载",
		"Session:",
		"load-structured-session [active]",
		"Title:",
		"Load fixture",
		"History:",
	} {
		if !strings.Contains(plain, marker) {
			t.Fatalf("/load document missing %q:\n%s", marker, plain)
		}
	}
	if strings.HasPrefix(plain, "\n") || strings.HasSuffix(plain, "\n") {
		t.Fatalf("/load document owns a top-level boundary blank: %q", plain)
	}
	if session.RuntimeSession == nil || session.RuntimeSession.ID != "load-structured-session" {
		t.Fatalf("/load did not restore runtime session: %+v", session.RuntimeSession)
	}
}

func TestTryExecuteStructuredChatCommandLoadErrorsStayLegacy(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	manager := newLoadCommandTestManager(t, "load-structured-session")
	session := newHistoryCommandTestSession(manager)

	for _, command := range []string{"/load", "/load   ", "/load missing-session"} {
		result, handled, err := tryExecuteStructuredChatCommand(session, command)
		if err != nil || handled {
			t.Fatalf("%q structured match=(%t, %v), want legacy error path", command, handled, err)
		}
		if len(result.Blocks) != 0 {
			t.Fatalf("%q produced blocks on legacy path", command)
		}
	}
	if _, handled, _ := tryExecuteStructuredChatCommand(nil, "/load load-structured-session"); handled {
		t.Fatal("nil session /load was structured-handled, want legacy")
	}
}

func TestDispatchChatCommandLoadCommitsDocumentBeforeReplay(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	manager := newLoadCommandTestManager(t, "load-structured-session")
	session := newHistoryCommandTestSession(manager)
	var output bytes.Buffer
	session.Interaction.SetWriter(&output)

	// No captureStdout wrapper here: replacing the global os.Stdout races with
	// other package tests that stream to it. The writer-side assertions below
	// are the actual gate — if dispatch ever bypassed the coordinator for raw
	// stdout, the confirmation and replay cells would not appear in the
	// retained buffer at all.
	if dispatchChatCommand(session, "/load load-structured-session", false) {
		t.Fatal("/load unexpectedly requested chat exit")
	}
	text := output.String()
	confirmAt := strings.Index(text, "会话已加载")
	replayAt := strings.Index(text, "已加载历史会话")
	if confirmAt < 0 {
		t.Fatalf("confirmation cell missing:\n%s", text)
	}
	if replayAt < 0 {
		t.Fatalf("replay header missing:\n%s", text)
	}
	if confirmAt > replayAt {
		t.Fatalf("replay appeared before confirmation document:\n%s", text)
	}
	if !strings.Contains(text, "我先检查目录。") {
		t.Fatalf("replayed transcript missing assistant body:\n%s", text)
	}
}

func TestDispatchChatCommandLoadSurvivesOwnedViewportRepaints(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	// 空会话 fixture：无历史回放，frame 只含确认 cell，便于唯一性断言。
	storage := runtimechat.NewInMemoryStorage()
	manager := runtimechat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)
	persisted := runtimechat.NewSession("tester")
	persisted.ID = "load-empty-session"
	if err := storage.Save(context.Background(), persisted); err != nil {
		t.Fatalf("save empty fixture: %v", err)
	}

	const width, height = 100, 120
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	session := &ChatSession{
		ProviderName:   "openai",
		Model:          "gpt-test",
		Surface:        surface,
		SessionManager: manager,
		SessionUserID:  "tester",
	}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord
	coord.SetSurface(surface)
	screen := newScreenVT(width, height)
	feed := func(paint func()) {
		t.Helper()
		screen.feed(captureSurfaceStdout(t, func() {
			coord.SetWriter(os.Stdout)
			paint()
		}))
	}

	feed(func() {
		coord.PrintPrompt()
	})
	feed(func() {
		if dispatchChatCommand(session, "/load load-empty-session", false) {
			t.Fatal("/load unexpectedly requested chat exit")
		}
	})
	assertSingleChatLoadMarker(t, "initial load frame", surface, screen)

	feed(func() {
		surface.SetStatusModels(style.StatusLineModel{State: style.RunReady}, nil)
		surface.ShowPrompt("> ")
	})
	assertSingleChatLoadMarker(t, "status and prompt repaint", surface, screen)

	feed(func() {
		surface.SetActiveBand([]string{"• Running structured load check", "  retained active row"})
	})
	assertSingleChatLoadMarker(t, "active band growth", surface, screen)

	feed(func() {
		surface.ClearActiveBand()
	})
	assertSingleChatLoadMarker(t, "active band shrink", surface, screen)

	surface.EnableForTest(88, height)
	if frame := commandResultFrameText(surface); strings.Count(frame, "会话已加载") != 1 {
		t.Fatalf("resize recompose lost or duplicated load marker:\n%s", frame)
	}
}

func assertSingleChatLoadMarker(t *testing.T, stage string, surface *ui.FixedBottomSurface, screen *screenVT) {
	t.Helper()
	const marker = "会话已加载"
	frame := commandResultFrameText(surface)
	if count := strings.Count(frame, marker); count != 1 {
		t.Fatalf("%s composed frame marker count=%d want 1:\n%s", stage, count, frame)
	}
	if rows := screen.RowsContaining(marker); len(rows) != 1 {
		t.Fatalf("%s physical screen marker rows=%v want one:\n%s", stage, rows, screen.dump())
	}
}
