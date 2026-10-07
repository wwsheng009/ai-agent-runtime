package commands

import (
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// scrollbackReplayGrantHarness wires a real coordinator/UI actor to a bridge
// without enabling the unified terminal writer, so load-path AppState can be
// observed before any executor consumes it.
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

	coordinator := newTestChatInteractionCoordinator(t, session)
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

// TestSessionLoadKeepsAppendOnlyProjection：装载（含空会话）仍然发布 replacement
// snapshot 并触发从源重证明，但不推进语义 epoch —— native scrollback append-only。
func TestSessionLoadKeepsAppendOnlyProjection(t *testing.T) {
	bridge, coordinator := scrollbackReplayGrantHarness(t)
	// start() is the /resume, --session and startup-restore entry point. The
	// event log intentionally does not exist here, which also pins that an empty
	// session load still publishes its replacement snapshot.
	bridge.start()
	coordinator.waitUIActorIdle()

	state := uiActorStateForGrantTest(t, coordinator)
	if state.HistoryEffects.TerminalEpoch != 0 {
		t.Fatalf("session load advanced the terminal epoch without a physical act: %d",
			state.HistoryEffects.TerminalEpoch)
	}
}

func TestNonLoadReplayDoesNotAdvanceEpoch(t *testing.T) {
	bridge, coordinator := scrollbackReplayGrantHarness(t)
	// replayEventLog is the diagnostic/unit-test entry point: it republishes the
	// Scene through the ordinary (non-load) replacement path.
	if _, err := bridge.replayEventLog(); err != nil {
		t.Fatalf("non-load replay: %v", err)
	}
	coordinator.waitUIActorIdle()

	if state := uiActorStateForGrantTest(t, coordinator); state.HistoryEffects.TerminalEpoch != 0 {
		t.Fatalf("non-load replay advanced the epoch: %d", state.HistoryEffects.TerminalEpoch)
	}
}

// TestCanonicalHistorySeedReProvesOnlyForImportedUnits pins the canonical-seed
// policy: importing canonical units the Scene does not have publishes a load
// replacement (re-proof from source, appended by the ordinary handoff);
// re-presenting the same canonical history (/history, resume presentation
// requested twice) imports nothing and publishes no replacement.
func TestCanonicalHistorySeedReProvesOnlyForImportedUnits(t *testing.T) {
	bridge, coordinator := scrollbackReplayGrantHarness(t)
	if !coordinator.postUIAction(ui.Resize{Width: 80, Height: 24, Generation: 1}) {
		t.Fatal("post resize")
	}
	coordinator.waitUIActorIdle()
	history := []runtimetypes.Message{
		*runtimetypes.NewUserMessage("查看 docs"),
		*runtimetypes.NewAssistantMessage("目录里有 README。"),
	}

	bridge.seedPersistedHistory(history, "已加载历史会话")
	coordinator.waitUIActorIdle()
	state := uiActorStateForGrantTest(t, coordinator)
	firstEntries := len(state.HistoryEffects.Entries())
	if firstEntries == 0 {
		t.Fatalf("canonical seed planned no delivery: %#v", state.HistoryEffects)
	}

	// Same canonical history again: every unit is already seeded and matched, so
	// no replacement snapshot is published and nothing is re-planned.
	bridge.seedPersistedHistory(history, "已加载历史会话")
	coordinator.waitUIActorIdle()
	state = uiActorStateForGrantTest(t, coordinator)
	if got := len(state.HistoryEffects.Entries()); got != firstEntries {
		t.Fatalf("re-presenting the same canonical history re-planned %d entries (want %d)", got, firstEntries)
	}

	// A genuinely new canonical unit is a replacement again (log-loss gap fill or
	// a canonical rewrite): the imported unit must be planned for append-only
	// delivery.
	grown := append(append([]runtimetypes.Message{}, history...), *runtimetypes.NewAssistantMessage("补充说明"))
	bridge.seedPersistedHistory(grown, "已加载历史会话")
	coordinator.waitUIActorIdle()
	state = uiActorStateForGrantTest(t, coordinator)
	if got := len(state.HistoryEffects.Entries()); got <= firstEntries {
		t.Fatalf("new canonical unit was not planned: entries %d -> %d", firstEntries, got)
	}
}
