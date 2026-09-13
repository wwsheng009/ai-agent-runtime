package commands

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
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

// TestCanonicalHistorySeedArmsReplayOnlyForImportedUnits pins the canonical-seed
// arm policy — the second and last producer of the one-shot replay
// authorization. Importing canonical units the Scene does not have replaces the
// physical projection, so it authorizes exactly one replay; re-presenting the
// same canonical history (/history, resume presentation requested twice) imports
// nothing and must leave a spent grant spent, so no normal interaction can
// re-arm a scrollback replay.
func TestCanonicalHistorySeedArmsReplayOnlyForImportedUnits(t *testing.T) {
	bridge, coordinator := scrollbackReplayGrantHarness(t)
	history := []runtimetypes.Message{
		*runtimetypes.NewUserMessage("查看 docs"),
		*runtimetypes.NewAssistantMessage("目录里有 README。"),
	}

	bridge.seedPersistedHistory(history, "已加载历史会话")
	coordinator.waitUIActorIdle()
	state := uiActorStateForGrantTest(t, coordinator)
	if !state.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("importing canonical history did not authorize the one-shot replay")
	}

	// Consume the grant the way the terminal owner does after a proven scrollback
	// replacement: a newer terminal epoch that starts a fresh delivery ledger.
	if !coordinator.postUIAction(ui.HistoryScrollbackReconciled{
		LayoutGeneration: state.LayoutGeneration,
		TerminalEpoch:    state.HistoryEffects.TerminalEpoch + 1,
	}) {
		t.Fatal("post scrollback reconciliation")
	}
	coordinator.waitUIActorIdle()
	if state = uiActorStateForGrantTest(t, coordinator); state.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("proven scrollback replacement did not consume the grant")
	}

	// Same canonical history again: every unit is already seeded and matched, so
	// no replacement snapshot is published and the consumed grant stays consumed.
	bridge.seedPersistedHistory(history, "已加载历史会话")
	coordinator.waitUIActorIdle()
	if state = uiActorStateForGrantTest(t, coordinator); state.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("re-presenting the same canonical history re-armed a consumed replay grant")
	}

	// A genuinely new canonical unit is a replacement again (log-loss gap fill or
	// a canonical rewrite): the imported unit invalidates the shown projection, so
	// the seed authorizes one more replay. Pinned deliberately — narrowing this
	// would let stale rows survive a canonical rewrite.
	grown := append(append([]runtimetypes.Message{}, history...), *runtimetypes.NewAssistantMessage("补充说明"))
	bridge.seedPersistedHistory(grown, "已加载历史会话")
	coordinator.waitUIActorIdle()
	if state = uiActorStateForGrantTest(t, coordinator); !state.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("importing a new canonical unit did not authorize a replacement replay")
	}
}

// TestHistoryEffectDiagnosticsExposeScrollbackReplayGrant keeps the one-shot
// authorization observable: without it, an operator cannot tell from /debug
// whether an outstanding history obligation will replace native scrollback or
// settle in place.
func TestHistoryEffectDiagnosticsExposeScrollbackReplayGrant(t *testing.T) {
	spent := chatDebugHistoryEffectSummary(ui.HistoryEffectQueueState{})
	if !strings.Contains(spent, "scrollback-replay-armed=false") {
		t.Fatalf("history effect summary hides the replay grant: %q", spent)
	}
	armed := chatDebugHistoryEffectSummary(ui.HistoryEffectQueueState{ScrollbackReplayArmed: true})
	if !strings.Contains(armed, "scrollback-replay-armed=true") {
		t.Fatalf("history effect summary hides the armed replay grant: %q", armed)
	}

	raw, err := json.Marshal(chatDebugDisplayHistoryGateInfo{ScrollbackReplayArmed: true})
	if err != nil {
		t.Fatalf("marshal history gates: %v", err)
	}
	if !strings.Contains(string(raw), `"scrollback_replay_armed":true`) {
		t.Fatalf("debug HTTP history gates lost the replay grant field: %s", raw)
	}
}
