package commands

import (
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// scrollbackReplayGrantHarness wires a real coordinator/UI actor to a bridge
// without enabling the unified terminal writer, so the one-shot replay grant can
// be observed in AppState before any executor consumes it.
func scrollbackReplayGrantHarness(t *testing.T) (*chatRuntimeEventBridge, *chatInteractionCoordinator) {
	t.Helper()
	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "scrollback-replay-grant"
	session := &ChatSession{
		RuntimeSession:   runtimeSession,
		LocalRuntimeHost: &localChatRuntimeHost{EventBus: runtimeevents.NewBusWithRetention(8)},
	}
	bridge := newChatRuntimeEventBridge(session)
	bridge.eventLogPathOverride = filepath.Join(t.TempDir(), "runtime-events.jsonl")
	session.RuntimeEventBridge = bridge

	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	return bridge, coordinator
}

func uiActorStateForGrantTest(t *testing.T, coordinator *chatInteractionCoordinator) ui.UIControllerState {
	t.Helper()
	actor := coordinator.ensureUIActor()
	if actor == nil {
		t.Fatal("ui actor was not attached")
	}
	return actor.State()
}

func TestSessionLoadGrantsScrollbackReplayInReplacementAction(t *testing.T) {
	bridge, coordinator := scrollbackReplayGrantHarness(t)
	// start() is the /resume, --session and startup-restore entry point. The
	// event log intentionally does not exist here, which also pins that an empty
	// session load still publishes a replacement snapshot carrying the grant.
	bridge.start()
	coordinator.waitUIActorIdle()

	state := uiActorStateForGrantTest(t, coordinator)
	if !state.HistoryEffects.ScrollbackReplayArmed || !state.HistoryEffects.ReconciliationRequired {
		t.Fatalf("session load armed=%t reconciliationRequired=%t, want both true",
			state.HistoryEffects.ScrollbackReplayArmed, state.HistoryEffects.ReconciliationRequired)
	}
}

func TestNonLoadReplayDoesNotGrantScrollbackReplay(t *testing.T) {
	bridge, coordinator := scrollbackReplayGrantHarness(t)
	// replayEventLog is the diagnostic/unit-test entry point: it republishes the
	// Scene without ever authorizing a destructive scrollback replacement.
	if _, err := bridge.replayEventLog(); err != nil {
		t.Fatalf("non-load replay: %v", err)
	}
	coordinator.waitUIActorIdle()

	if state := uiActorStateForGrantTest(t, coordinator); state.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("non-load replay authorized a scrollback replacement")
	}
}
