package commands

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestSessionLoadBeforeGeometryStillDeliversTranscript pins the reported
// "input area recovered, transcript body still blank" regression on the real
// load path.
//
// /resume, /load and startup restore all restore the body by seeding canonical
// history into the Scene (printVisibleChatHistory -> seedPersistedHistory ->
// sessionInteractionReplacementSnapshot). Planning is geometry-gated, so when
// that replacement snapshot reaches the reducer before the first applied
// resize, the load mints no history commit at all: the ledger stays empty
// (pending=0 in-flight=0 acked=0) while the one-shot scrollback replay
// authorization is already armed. Rebasing pending payloads on the next resize
// cannot recreate a plan that was never made, so the body stayed blank no
// matter how often the user resized the terminal.
func TestSessionLoadBeforeGeometryStillDeliversTranscript(t *testing.T) {
	bridge, coordinator := scrollbackReplayGrantHarness(t)

	history := []runtimetypes.Message{
		*runtimetypes.NewUserMessage("查看 docs"),
		*runtimetypes.NewAssistantMessage("目录里有 README。"),
	}
	bridge.seedPersistedHistory(history, "已加载历史会话 (2 条消息):")
	coordinator.waitUIActorIdle()

	loaded := uiActorStateForGrantTest(t, coordinator)
	if len(loaded.Transcript.Cells) == 0 {
		t.Fatal("load seeded no transcript cells")
	}
	if !loaded.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("load did not arm the one-shot scrollback replay authorization")
	}
	if len(loaded.HistoryEffects.Entries()) != 0 {
		t.Fatalf("precondition: geometry-free load planned %d entries, want 0",
			len(loaded.HistoryEffects.Entries()))
	}

	// The terminal reports its size: the loaded generation must become
	// deliverable now, otherwise the body can never be replayed.
	if !coordinator.postUIAction(ui.Resize{Width: 100, Height: 30, Generation: 1}) {
		t.Fatal("post resize")
	}
	coordinator.waitUIActorIdle()

	after := uiActorStateForGrantTest(t, coordinator)
	entries := after.HistoryEffects.Entries()
	if len(entries) == 0 {
		t.Fatalf("geometry arrival did not re-plan the loaded transcript (cells=%d)",
			len(after.Transcript.Cells))
	}
	planned := make(map[uint64]struct{}, len(entries))
	for _, entry := range entries {
		if entry.State != ui.HistoryCommitPending {
			t.Fatalf("token %d planned in state %v, want pending", entry.Commit.Token, entry.State)
		}
		planned[uint64(entry.Commit.CellID)] = struct{}{}
	}
	for _, cell := range after.Transcript.Cells {
		if cell.Source == "" {
			continue
		}
		if _, ok := planned[uint64(cell.ID)]; !ok {
			t.Fatalf("finalized cell %d was never planned for delivery", cell.ID)
		}
	}
}
