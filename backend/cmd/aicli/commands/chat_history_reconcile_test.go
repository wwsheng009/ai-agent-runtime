package commands

import (
	"bytes"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// A runtime event log can be persisted after only a prefix of a turn while
// the canonical session store already contains the full transcript. A nonempty
// Scene must therefore not suppress persisted-history import, and repeated
// resume/history presentation must not append a second copy.
func TestPrintVisibleChatHistory_UnifiedReconcilesPartialEventLogIdempotently(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 20)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}

	// Simulate an event log that restored only the first of two identical user
	// inputs. Content alone is insufficient as an identity: reconcile must
	// retain the restored occurrence and import the second occurrence exactly
	// once, together with the missing assistant rows.
	bridge.submitUserInput("repeat")
	replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("repeat"),
		*runtimetypes.NewAssistantMessage("first response"),
		*runtimetypes.NewUserMessage("repeat"),
		*runtimetypes.NewAssistantMessage("second response"),
	})

	if got := printVisibleChatHistory(session, "已加载历史会话"); got != 4 {
		t.Fatalf("visible history count=%d want 4", got)
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	assertPersistedHistoryScene(t, bridge.sceneSnapshot())

	if got := printVisibleChatHistory(session, "已加载历史会话"); got != 4 {
		t.Fatalf("second visible history count=%d want 4", got)
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	assertPersistedHistoryScene(t, bridge.sceneSnapshot())

	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified history reconcile populated legacy historyWindow: %#v", got)
	}
	if terminal.Len() == 0 {
		t.Fatal("TerminalSession did not render reconciled history")
	}
}

func assertPersistedHistoryScene(t *testing.T, snapshot *scene.Snapshot) {
	t.Helper()
	if snapshot == nil || len(snapshot.Cells) != 4 {
		count := 0
		if snapshot != nil {
			count = len(snapshot.Cells)
		}
		t.Fatalf("scene cells=%d want exactly 4 canonical rows", count)
	}
	wantKinds := []scene.CellKind{
		scene.KindUser,
		scene.KindAssistant,
		scene.KindUser,
		scene.KindAssistant,
	}
	wantSources := []string{"repeat", "first response", "repeat", "second response"}
	for index, cell := range snapshot.Cells {
		if cell.Kind != wantKinds[index] || cell.Source != wantSources[index] {
			t.Fatalf("cell[%d]=%+v, want kind=%v source=%q", index, cell, wantKinds[index], wantSources[index])
		}
	}
}

func TestSeedPersistedHistory_ImportsFinalToolChainOnce(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	messages := []runtimetypes.Message{
		{
			Role: "assistant",
			ToolCalls: []runtimetypes.ToolCall{{
				ID: "call-history-1", Name: "read_file",
			}},
			Metadata: runtimetypes.NewMetadata(),
		},
		*runtimetypes.NewToolMessage("call-history-1", "README contents"),
	}

	bridge.seedPersistedHistory(messages, "")
	snapshot := bridge.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != 1 {
		count := 0
		if snapshot != nil {
			count = len(snapshot.Cells)
		}
		t.Fatalf("scene cells=%d want one committed tool chain", count)
	}
	cell := snapshot.Cells[0]
	if cell.Kind != scene.KindToolChain || cell.Phase != scene.CellCommitted || cell.Source != "read_file\nREADME contents" {
		t.Fatalf("tool history cell=%+v", cell)
	}

	bridge.seedPersistedHistory(messages, "")
	if got := bridge.sceneSnapshot(); got == nil || len(got.Cells) != 1 {
		count := 0
		if got != nil {
			count = len(got.Cells)
		}
		t.Fatalf("second seed duplicated tool chain: cells=%d", count)
	}
}
