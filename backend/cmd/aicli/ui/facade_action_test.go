package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
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

	out := captureUIStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("sync fallback should render prompt")
		}
	})
	if out == "" {
		t.Fatal("sync fallback produced no output")
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

// TestFacadeAction_FinalizedReleaseFencesQueuedBandPaint verifies the ordering
// bridge used by stream finalization: an already-posted transient band update
// cannot remount after permanent output has released that projection.
func TestFacadeAction_FinalizedReleaseFencesQueuedBandPaint(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	var pending UIAction
	surface.SetUIActorPoster(func(action UIAction) bool {
		pending = action
		return true
	})
	if !surface.SetActiveBand([]string{"streaming"}) {
		t.Fatal("band action should be accepted")
	}
	if pending == nil {
		t.Fatal("expected queued band action")
	}
	if !surface.ReleaseActiveBandForFinalizedOutput() {
		t.Fatal("finalized release should succeed")
	}
	if !surface.Apply(pending) {
		t.Fatal("stale action should be consumed without error")
	}
	if got := surface.ActiveBandLines(); len(got) != 0 {
		t.Fatalf("stale band action remounted transient rows: %q", got)
	}
}

// TestFacadeAction_ApplyParity 验证实施指南任务 5：reducer 经 Apply 应用
// action 的输出与同步 facade 路径逐字节一致。
func TestFacadeAction_ApplyParity(t *testing.T) {
	runSequence := func(s *FixedBottomSurface, viaApply bool) string {
		return captureUIStdout(t, func() {
			apply := func(a UIAction) {
				if viaApply {
					if !s.Apply(a) {
						t.Fatalf("Apply rejected %T", a)
					}
				}
			}
			if viaApply {
				apply(SetStatusModelsAction{
					Status:  style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
					Dynamic: &style.StatusLineModel{State: style.RunStreaming, StateText: "Working"},
				})
				apply(ShowPromptAction{Line: "> "})
				apply(SetActiveBandAction{RawLines: []string{"band-1", "band-2"}})
				apply(SetPromptStateAction{Line: "> ", Input: "abc", Rows: 1, CursorRow: 0, CursorCol: 3})
				apply(ShowPopupAction{Lines: []string{"popup-1", "popup-2"}})
				apply(ClearPopupAction{})
				apply(ClearActiveBandAction{})
			} else {
				s.SetStatusModels(
					style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
					&style.StatusLineModel{State: style.RunStreaming, StateText: "Working"},
				)
				if !s.ShowPrompt("> ") {
					t.Fatal("prompt")
				}
				if !s.SetActiveBand([]string{"band-1", "band-2"}) {
					t.Fatal("band")
				}
				if !s.SetPromptInputState("> ", "abc", 1, 0, 3) {
					t.Fatal("prompt state")
				}
				s.ShowPopup([]string{"popup-1", "popup-2"})
				s.ClearPopup()
				if !s.ClearActiveBand() {
					t.Fatal("clear band")
				}
			}
		})
	}

	syncOut := runSequence(newOwnedTestFixedBottomSurfaceWithSize(80, 24), false)
	actionOut := runSequence(newOwnedTestFixedBottomSurfaceWithSize(80, 24), true)
	if syncOut != actionOut {
		t.Errorf("Apply path output differs from sync facade path\n--- sync ---\n%q\n--- apply ---\n%q", syncOut, actionOut)
	}
}

// TestFacadeAction_ApplyParityRemainingBottomPaneProducers keeps the second
// Phase 2 facade slice honest: every newly actionized prompt/status/composer
// API must retain the legacy synchronous terminal transaction when reduced.
func TestFacadeAction_ApplyParityRemainingBottomPaneProducers(t *testing.T) {
	run := func(s *FixedBottomSurface, viaApply bool) string {
		return captureUIStdout(t, func() {
			persistent := style.StatusLineModel{State: style.RunReady, StateText: "Ready"}
			dynamic := &style.StatusLineModel{State: style.RunStreaming, StateText: "Working"}
			if viaApply {
				actions := []UIAction{
					SetStatusModelAction{Status: persistent},
					SetDynamicStatusModelAction{Dynamic: dynamic},
					ShowPromptAction{Line: "> "},
					SetPromptNoticeAction{Line: "queued"},
					SetPromptEditorStatusAction{Line: "editing"},
					TrackPromptInputAction{Line: "> ", Input: "draft", Rows: 2, CursorRow: 1, CursorCol: 5},
					SetPromptRowsAction{Rows: 3},
					ResetPromptAction{Line: "> ", Rows: 3},
					SetComposerPreviewAction{Line: "compose> "},
					ClearComposerPreviewAction{},
				}
				for _, action := range actions {
					if !s.Apply(action) {
						t.Fatalf("Apply rejected %T", action)
					}
				}
				return
			}
			s.SetStatusModel(persistent)
			s.SetDynamicStatusModel(dynamic)
			if !s.ShowPrompt("> ") || !s.SetPromptNoticeLine("queued") || !s.SetPromptEditorStatusLine("editing") {
				t.Fatal("prompt overlay setup failed")
			}
			if !s.TrackPromptInputState("> ", "draft", 2, 1, 5) || !s.SetPromptRows(3) || !s.ResetPrompt("> ", 3) {
				t.Fatal("prompt transition failed")
			}
			s.SetComposerPreview("compose> ")
			s.ClearComposerPreview()
		})
	}

	syncOut := run(newOwnedTestFixedBottomSurfaceWithSize(80, 24), false)
	actionOut := run(newOwnedTestFixedBottomSurfaceWithSize(80, 24), true)
	if syncOut != actionOut {
		t.Errorf("remaining BottomPane Apply differs from sync\n--- sync ---\n%q\n--- apply ---\n%q", syncOut, actionOut)
	}
}

// TestFacadeAction_ApplyParityStyledBand 验证 styled band 的 Apply 与同步
// SetActiveBandStyled 输出一致。
func TestFacadeAction_ApplyParityStyledBand(t *testing.T) {
	lines := []render.Line{{Spans: []render.Span{{Text: "styled-band"}}}}

	syncOut := captureUIStdout(t, func() {
		if !newOwnedTestFixedBottomSurfaceWithSize(80, 24).SetActiveBandStyled(lines) {
			t.Fatal("sync styled band")
		}
	})
	actionOut := captureUIStdout(t, func() {
		s := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
		if !s.Apply(SetActiveBandAction{Lines: lines}) {
			t.Fatal("apply styled band")
		}
	})
	if syncOut != actionOut {
		t.Errorf("styled band Apply differs from sync\n--- sync ---\n%q\n--- apply ---\n%q", syncOut, actionOut)
	}
}

// TestFacadeAction_ApplyParityPopupPreserveCursor 验证 preserve-cursor
// popup 变体的 Apply 与同步路径输出一致。
func TestFacadeAction_ApplyParityPopupPreserveCursor(t *testing.T) {
	run := func(s *FixedBottomSurface, viaApply bool) string {
		return captureUIStdout(t, func() {
			if viaApply {
				if !s.Apply(ShowPopupAction{Lines: []string{"popup-pc"}, PreserveCursor: true}) {
					t.Fatal("apply show popup")
				}
				if !s.Apply(ClearPopupAction{PreserveCursor: true}) {
					t.Fatal("apply clear popup")
				}
			} else {
				s.ShowPopupPreserveCursor([]string{"popup-pc"})
				s.ClearPopupPreserveCursor()
			}
		})
	}
	syncOut := run(newOwnedTestFixedBottomSurfaceWithSize(80, 24), false)
	actionOut := run(newOwnedTestFixedBottomSurfaceWithSize(80, 24), true)
	if syncOut != actionOut {
		t.Errorf("preserve-cursor popup Apply differs from sync\n--- sync ---\n%q\n--- apply ---\n%q", syncOut, actionOut)
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

// TestFacadeAction_ApplyParityPopupHandle verifies reducer application uses
// the same terminal path and popup-stack restoration semantics as the sync
// facade. The action handle is deterministic because real handles are minted
// before their Begin action is posted.
func TestFacadeAction_ApplyParityPopupHandle(t *testing.T) {
	runSync := func(s *FixedBottomSurface) string {
		return captureUIStdout(t, func() {
			handle := s.BeginPopupInputForOwner([]string{"first"}, "pick> ", "modal:selection")
			if !s.UpdatePopupInputForHandle(handle, []string{"second"}, "pick> ", true) {
				t.Fatal("sync update failed")
			}
			s.ClearPopupHandlePreserveCursor(handle)
		})
	}
	runApply := func(s *FixedBottomSurface) string {
		return captureUIStdout(t, func() {
			handle := PopupHandle{owner: "modal:selection", instance: 1}
			if !s.Apply(ShowPopupAction{Lines: []string{"first"}, Owner: handle.owner, Prompt: "pick> ", Input: true, Handle: &handle}) {
				t.Fatal("apply begin failed")
			}
			if !s.Apply(UpdatePopupAction{Handle: handle, Lines: []string{"second"}, Prompt: "pick> ", PreserveCursor: true}) {
				t.Fatal("apply update failed")
			}
			if !s.Apply(ClearPopupAction{PreserveCursor: true, Handle: &handle}) {
				t.Fatal("apply clear failed")
			}
		})
	}

	syncOut := runSync(newOwnedTestFixedBottomSurfaceWithSize(80, 24))
	actionOut := runApply(newOwnedTestFixedBottomSurfaceWithSize(80, 24))
	if syncOut != actionOut {
		t.Errorf("tokenized popup Apply differs from sync\n--- sync ---\n%q\n--- apply ---\n%q", syncOut, actionOut)
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

// TestFacadeAction_ApplyUnknownReturnsFalse 验证未识别 action 返回 false。
func TestFacadeAction_ApplyUnknownReturnsFalse(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	if surface.Apply(RuntimeEvent{Kind: "unhandled"}) {
		t.Error("Apply should reject unknown action")
	}
	if surface.Apply(InputEvent{Text: "x"}) {
		t.Error("Apply should reject InputEvent (not a facade action)")
	}
}
