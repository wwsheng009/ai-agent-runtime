package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// TestFacadeAction_PosterWiredPostsWithoutMutation 验证实施指南 Phase 1
// 任务 4：facade 接 poster 后内部只投递 action，不直接 mutation。
func TestFacadeAction_PosterWiredPostsWithoutMutation(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	var posted []UIAction
	surface.SetUIActorPoster(func(a UIAction) bool {
		posted = append(posted, a)
		return true
	})

	if !surface.ShowPrompt("> ") {
		t.Fatal("ShowPrompt should report accepted")
	}
	surface.ClearPromptRows(1)
	surface.SetActiveBand([]string{"band-1"})
	surface.SetStatusModels(style.StatusLineModel{State: style.RunReady, StateText: "Ready"}, nil)
	surface.SetStatusModel(style.StatusLineModel{State: style.RunStreaming, StateText: "Streaming"})
	surface.SetDynamicStatusModel(&style.StatusLineModel{State: style.RunStreaming, StateText: "Working"})
	surface.SetPromptInputState("> ", "input", 1, 0, 2)
	surface.TrackPromptInputState("> ", "input", 2, 1, 1)
	surface.ResetPrompt("> ", 2)
	surface.SetPromptRows(3)
	surface.SetPromptNoticeLine("queued")
	surface.SetPromptEditorStatusLine("editing")
	surface.SetComposerPreview("composer> ")
	surface.ClearComposerPreview()
	surface.ShowPopup([]string{"popup-1"})
	surface.ClearPopup()
	surface.ClearActiveBand()

	// 未接线状态下这些调用会 mutation surface；接线后必须保持未变。
	surface.mu.Lock()
	if surface.promptLine != "" {
		t.Errorf("promptLine mutated to %q, want empty (poster should own mutation)", surface.promptLine)
	}
	if len(surface.activeBandLines) != 0 {
		t.Errorf("activeBandLines mutated, want empty")
	}
	if len(surface.popupLines) != 0 {
		t.Errorf("popupLines mutated, want empty")
	}
	surface.mu.Unlock()

	want := []string{
		"ShowPromptAction", "ClearPromptRowsAction", "SetActiveBandAction", "SetStatusModelsAction",
		"SetStatusModelAction", "SetDynamicStatusModelAction", "SetPromptStateAction", "TrackPromptInputAction",
		"ResetPromptAction", "SetPromptRowsAction", "SetPromptNoticeAction", "SetPromptEditorStatusAction",
		"SetComposerPreviewAction", "ClearComposerPreviewAction", "ShowPopupAction", "ClearPopupAction", "ClearActiveBandAction",
	}
	if len(posted) != len(want) {
		t.Fatalf("posted = %d actions, want %d", len(posted), len(want))
	}
	for i, w := range want {
		if got := facadeActionName(posted[i]); got != w {
			t.Errorf("posted[%d] = %s, want %s", i, got, w)
		}
	}
}

func facadeActionName(a UIAction) string {
	switch a.(type) {
	case ShowPromptAction:
		return "ShowPromptAction"
	case ClearPromptRowsAction:
		return "ClearPromptRowsAction"
	case SetActiveBandAction:
		return "SetActiveBandAction"
	case SetStatusModelsAction:
		return "SetStatusModelsAction"
	case SetStatusModelAction:
		return "SetStatusModelAction"
	case SetDynamicStatusModelAction:
		return "SetDynamicStatusModelAction"
	case SetPromptStateAction:
		return "SetPromptStateAction"
	case TrackPromptInputAction:
		return "TrackPromptInputAction"
	case ResetPromptAction:
		return "ResetPromptAction"
	case SetPromptRowsAction:
		return "SetPromptRowsAction"
	case SetPromptNoticeAction:
		return "SetPromptNoticeAction"
	case SetPromptEditorStatusAction:
		return "SetPromptEditorStatusAction"
	case SetComposerPreviewAction:
		return "SetComposerPreviewAction"
	case ClearComposerPreviewAction:
		return "ClearComposerPreviewAction"
	case ShowPopupAction:
		return "ShowPopupAction"
	case ClearPopupAction:
		return "ClearPopupAction"
	case ClearActiveBandAction:
		return "ClearActiveBandAction"
	case UpdatePopupAction:
		return "UpdatePopupAction"
	}
	return "unknown"
}

// TestFacadeAction_PosterRejectFallsBackToSync 验证 poster 拒绝（如 actor
// 已关闭）时 facade 回退同步实现，行为保持。
func TestFacadeAction_PosterRejectFallsBackToSync(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	surface.SetUIActorPoster(func(UIAction) bool { return false })

	captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("sync fallback should render prompt")
		}
	})
	if frame := frameDump(surface.ComposedFrameForTest()); !strings.Contains(frame, ">") {
		t.Fatalf("sync fallback should render prompt into composed frame:\n%s", frame)
	}
	surface.mu.Lock()
	if surface.promptLine != "> " {
		t.Errorf("promptLine = %q, want %q", surface.promptLine, "> ")
	}
	surface.mu.Unlock()
}

// TestFacadeAction_SetUIActorPosterNilRestoresSync 验证 SetUIActorPoster(nil)
// 恢复同步路径。
func TestFacadeAction_SetUIActorPosterNilRestoresSync(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	surface.SetUIActorPoster(func(UIAction) bool { return true })
	surface.SetUIActorPoster(nil)

	if !surface.ShowPrompt("> ") {
		t.Fatal("prompt should render after poster cleared")
	}
	surface.mu.Lock()
	if surface.promptLine != "> " {
		t.Errorf("promptLine = %q, want %q", surface.promptLine, "> ")
	}
	surface.mu.Unlock()
}

// TestFacadeAction_FinalizedReleaseClearsBand pins the stream-finalization
// bridge: the synchronous release must clear the transient band state. L3-3
// retired the Apply sink, so the queued-action fence is no longer observable
// on the surface; the release itself stays covered here.
func TestFacadeAction_FinalizedReleaseClearsBand(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	if !surface.SetActiveBand([]string{"streaming"}) {
		t.Fatal("band action should be accepted")
	}
	if !surface.ReleaseActiveBandForFinalizedOutput() {
		t.Fatal("finalized release should succeed")
	}
	if got := surface.ActiveBandLines(); len(got) != 0 {
		t.Fatalf("finalized release must clear the transient band: %q", got)
	}
}

// TestFacadeAction_PopupHandleUsesOrderedActions guards the synchronous
// handle API during the actor migration. Begin returns identity immediately,
// but begin/update/clear themselves must be reducer-applied durable actions.
func TestFacadeAction_PopupHandleUsesOrderedActions(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	var posted []UIAction
	surface.SetUIActorPoster(func(action UIAction) bool {
		posted = append(posted, action)
		return true
	})

	handle := surface.BeginPopupInputForOwner([]string{"first"}, "pick> ", "modal:selection")
	if !handle.Valid() {
		t.Fatal("BeginPopupInputForOwner returned an invalid handle")
	}
	if !surface.UpdatePopupInputForHandle(handle, []string{"second"}, "pick> ", true) {
		t.Fatal("UpdatePopupInputForHandle was not accepted")
	}
	surface.ClearPopupHandlePreserveCursor(handle)

	if len(posted) != 3 {
		t.Fatalf("posted = %d actions, want begin/update/clear", len(posted))
	}
	begin, ok := posted[0].(ShowPopupAction)
	if !ok || begin.Handle == nil || *begin.Handle != handle || !begin.Input {
		t.Fatalf("begin action = %#v, want tokenized input popup", posted[0])
	}
	if update, ok := posted[1].(UpdatePopupAction); !ok || update.Handle != handle {
		t.Fatalf("update action = %#v, want handle %v", posted[1], handle)
	}
	if clear, ok := posted[2].(ClearPopupAction); !ok || clear.Handle == nil || *clear.Handle != handle {
		t.Fatalf("clear action = %#v, want handle %v", posted[2], handle)
	}

	surface.mu.Lock()
	defer surface.mu.Unlock()
	if surface.popupOwner != "" || len(surface.popupLines) != 0 {
		t.Fatalf("poster path mutated surface before reducer: owner=%q lines=%q", surface.popupOwner, surface.popupLines)
	}
}

func TestBottomPaneState_ComposerOnlyPopupOwnsFocus(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	captureUIStdout(t, func() {
		handle := surface.BeginPopupInputForOwner(nil, "select> ", "modal:selection")
		if !handle.Valid() {
			t.Fatal("expected composer-only popup handle")
		}
	})

	surface.mu.Lock()
	state := surface.bottomPaneStateLocked()
	surface.mu.Unlock()
	if state.ComposerLine != "select> " || state.Focus != BottomFocusPopup {
		t.Fatalf("composer-only popup state = %+v", state)
	}
}
