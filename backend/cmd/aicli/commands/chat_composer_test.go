package commands

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func TestChatComposerControllerBuildsCoreHooksWithoutCompletion(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("draft")

	composer := newChatComposerController(session)
	hooks := composer.hooks()

	if hooks.InitialText != "draft" || hooks.InitialCursor != len([]rune("draft")) {
		t.Fatalf("expected composer to seed draft snapshot, got text=%q cursor=%d", hooks.InitialText, hooks.InitialCursor)
	}
	if hooks.MaxVisibleRows != ui.ChatComposerMaxVisibleRows {
		t.Fatalf("expected bounded composer viewport, got %d", hooks.MaxVisibleRows)
	}
	if hooks.ResolveMaxVisibleRows == nil || hooks.ResolveMaxVisibleRows() != ui.ChatComposerMaxVisibleRows {
		t.Fatal("expected composer viewport budget to be resolved dynamically")
	}
	if hooks.OnChange == nil || hooks.OnBeforeTerminalWrite == nil || hooks.OnTerminalWrite == nil {
		t.Fatal("expected core editor hooks to be present")
	}
	if hooks.OnComplete == nil {
		t.Fatal("expected Tab plan-mode toggle hook to be present")
	}
	if hooks.OnNavigate != nil || hooks.OnSubmit != nil || hooks.OnCancelPopup != nil {
		t.Fatal("expected slash-only hooks to be absent without a fixed-bottom surface")
	}

	hooks.OnChange(ui.LineEditorSnapshot{Text: "next", Cursor: 4})
	snapshot := coord.PromptInputSnapshot()
	if snapshot.Text != "next" || snapshot.Cursor != 4 {
		t.Fatalf("expected composer change hook to update interaction prompt, got %#v", snapshot)
	}
}

func TestChatComposerControllerTabTogglesPlanModeAndPreservesDraft(t *testing.T) {
	session := newPlanCommandSession("")
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	composer := &chatComposerController{
		session: session,
		prompt:  formatSessionUserPrompt(session),
	}
	hooks := composer.hooks()
	if hooks.OnComplete == nil {
		t.Fatal("expected Tab hook")
	}

	replacement, ok := hooks.OnComplete(ui.LineEditorSnapshot{Text: "draft", Cursor: len([]rune("draft"))})
	if !ok {
		t.Fatal("expected Tab to toggle plan mode")
	}
	if replacement.Text != "draft" || replacement.Cursor != len([]rune("draft")) {
		t.Fatalf("expected Tab toggle to preserve draft, got %#v", replacement)
	}
	if session.PermissionMode != "plan" || !chatPlanModeActive(session) {
		t.Fatalf("expected Tab to enter plan mode, permission=%q active=%t", session.PermissionMode, chatPlanModeActive(session))
	}

	replacement, ok = hooks.OnComplete(ui.LineEditorSnapshot{Text: "draft", Cursor: 2})
	if !ok {
		t.Fatal("expected second Tab to toggle plan mode off")
	}
	if replacement.Text != "draft" || replacement.Cursor != 2 {
		t.Fatalf("expected second Tab toggle to preserve cursor, got %#v", replacement)
	}
	if session.PermissionMode != "default" || chatPlanModeActive(session) {
		t.Fatalf("expected Tab to leave plan mode, permission=%q active=%t", session.PermissionMode, chatPlanModeActive(session))
	}
}

func TestChatComposerControllerTabKeepsSlashCompletionSemantics(t *testing.T) {
	session := newPlanCommandSession("")
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	composer := &chatComposerController{
		session:    session,
		prompt:     formatSessionUserPrompt(session),
		completion: newChatSlashCompletionController(session),
	}
	hooks := composer.hooks()

	replacement, ok := hooks.OnComplete(ui.LineEditorSnapshot{Text: "/m", Cursor: 2})
	if !ok || replacement.Text == "/m" {
		t.Fatalf("expected slash completion to keep owning Tab, replacement=%#v ok=%t", replacement, ok)
	}
	if chatPlanModeActive(session) {
		t.Fatal("slash completion Tab must not toggle plan mode")
	}

	replacement, ok = hooks.OnComplete(ui.LineEditorSnapshot{Text: "/unknown", Cursor: len([]rune("/unknown"))})
	if ok || replacement.Text != "" {
		t.Fatalf("expected unknown slash draft to leave Tab unconsumed, replacement=%#v ok=%t", replacement, ok)
	}
	if chatPlanModeActive(session) {
		t.Fatal("unknown slash Tab must not toggle plan mode")
	}
}

func TestFormatChatComposerEditorStatusIsConditionalAndTracksLogicalLine(t *testing.T) {
	if status := formatChatComposerEditorStatus(ui.LineEditorSnapshot{LogicalLine: 1, LogicalLines: 1, DisplayRows: 1}); status != "" {
		t.Fatalf("expected single-line composer status to stay hidden, got %q", status)
	}
	status := formatChatComposerEditorStatus(ui.LineEditorSnapshot{
		LogicalLine:  2,
		LogicalLines: 4,
		DisplayRows:  4,
	})
	if status != "多行 2/4" {
		t.Fatalf("unexpected multiline composer status %q", status)
	}
	status = formatChatComposerEditorStatus(ui.LineEditorSnapshot{
		LogicalLine:   1,
		LogicalLines:  1,
		DisplayRows:   12,
		ViewportStart: 6,
		ViewportRows:  ui.ChatComposerMaxVisibleRows,
	})
	if status != "自动换行 · 显示 12 行 · 视图 7-12/12" {
		t.Fatalf("unexpected wrapped composer status %q", status)
	}
}

func TestChatComposerControllerAddsSlashCompletionHooksWhenCompletionIsPresent(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	composer := &chatComposerController{
		session:    session,
		prompt:     formatSessionUserPrompt(session),
		completion: newChatSlashCompletionController(session),
	}

	hooks := composer.hooks()
	if hooks.OnComplete == nil || hooks.OnNavigate == nil || hooks.OnSubmit == nil || hooks.OnCancelPopup == nil {
		t.Fatal("expected slash completion hooks to be installed")
	}

	replacement, ok := hooks.OnComplete(ui.LineEditorSnapshot{Text: "/sh", Cursor: 3})
	if !ok {
		t.Fatal("expected slash completion to replace /sh")
	}
	if replacement.Text != "/shell " || replacement.Cursor != len([]rune("/shell ")) {
		t.Fatalf("expected slash completion to choose /shell, got %#v", replacement)
	}

	replacement, ok = hooks.OnSubmit(ui.LineEditorSnapshot{Text: "/unknown", Cursor: len([]rune("/unknown"))})
	if ok {
		t.Fatalf("expected unknown command submission to remain unconsumed, got %#v", replacement)
	}

	replacement, ok = hooks.OnSubmit(ui.LineEditorSnapshot{Text: "/model ", Cursor: len([]rune("/model "))})
	if !ok {
		t.Fatal("expected argument-capable exact command submission to be consumed")
	}
	if !strings.HasPrefix(replacement.Text, "/model") {
		t.Fatalf("expected slash submission to keep /model command, got %#v", replacement)
	}
}

func TestChatComposerControllerTracksSnapshotBeforeCompletionRender(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	completion := newChatSlashCompletionController(session)
	composer := &chatComposerController{
		session:    session,
		prompt:     formatSessionUserPrompt(session),
		completion: completion,
	}

	composer.onChange(ui.LineEditorSnapshot{Text: "/m", Cursor: 2, PasteActive: true})

	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "/m" || !snapshot.PasteActive {
		t.Fatalf("expected interaction snapshot to be updated before completion render, got %#v", snapshot)
	}
	if !completion.editorPasteActive {
		t.Fatal("expected completion controller to observe paste-active snapshot")
	}
	if !completion.isPopupBlockedLocked() {
		t.Fatal("expected completion popup to be blocked by tracked paste state")
	}
}

func TestNormalizeChatComposerReadErrorInterruptsAndResetsPrompt(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("draft")

	err := normalizeChatComposerReadError(session, ui.ErrInteractiveInputInterrupted)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected interrupted composer read to become EOF, got %v", err)
	}
	if !session.IsInterrupted() {
		t.Fatal("expected interrupted composer read to interrupt the session")
	}
	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected interrupted composer read to reset prompt state, got %#v", snapshot)
	}
}

func TestNormalizeChatComposerReadErrorBacktrackDoesNotInterrupt(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("draft")

	err := normalizeChatComposerReadError(session, ui.ErrInteractiveInputBacktrackRequested)
	if !errors.Is(err, ui.ErrInteractiveInputBacktrackRequested) {
		t.Fatalf("expected backtrack error to pass through, got %v", err)
	}
	if session.IsInterrupted() {
		t.Fatal("did not expect backtrack Esc to interrupt the session")
	}
	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected backtrack Esc to reset prompt state, got %#v", snapshot)
	}
}

func TestChatBusyComposerCaptureTracksAndClearsNonPriorityPrompt(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	capture := newChatBusyComposerCapture(session, "> ", false)
	hooks := capture.hooks()
	if hooks.ResolveMaxVisibleRows == nil {
		t.Fatal("expected busy composer viewport budget to be resolved dynamically")
	}
	if hooks.OnTerminalWrite == nil {
		t.Fatal("expected busy composer to install the atomic terminal writer")
	}

	hooks.OnChange(ui.LineEditorSnapshot{Text: "queued while busy", Cursor: len([]rune("queued while busy"))})
	snapshot := coord.PromptInputSnapshot()
	if snapshot.Text != "queued while busy" {
		t.Fatalf("expected busy capture to track prompt text, got %#v", snapshot)
	}

	capture.ClearPrompt()
	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected busy capture clear to reset prompt text, got %#v", snapshot)
	}

	if !hooks.OnCancel(ui.LineEditorSnapshot{}) || !capture.Cancelled() {
		t.Fatal("expected busy capture cancel hook to consume input and mark cancellation")
	}
}

func TestChatBusyComposerCaptureDoesNotTrackPriorityPrompt(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("main draft")
	capture := newChatBusyComposerCapture(session, "approval> ", true)
	hooks := capture.hooks()

	hooks.OnChange(ui.LineEditorSnapshot{Text: "yes", Cursor: 3})
	capture.ClearPrompt()
	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "main draft" {
		t.Fatalf("expected priority capture not to mutate main prompt, got %#v", snapshot)
	}
}

func TestChatModalComposerPromptTracksAndClearsWhenSurfaceDisabled(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	prompt := newChatModalComposerPrompt(session, "select> ")
	hooks := prompt.hooks()

	prompt.initializePrompt()
	hooks.OnChange(ui.LineEditorSnapshot{Text: "choice", Cursor: 6})
	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "choice" {
		t.Fatalf("expected modal prompt to track transient input, got %#v", snapshot)
	}
	prompt.clearPrompt()
	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected modal prompt to clear transient input, got %#v", snapshot)
	}
}

func TestChatModalComposerPromptCancelReturnsPromptCancelled(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	prompt := newChatModalComposerPrompt(session, "select> ")
	hooks := prompt.hooks()

	if !hooks.OnCancel(ui.LineEditorSnapshot{}) {
		t.Fatal("expected modal cancel hook to consume cancellation")
	}
	err := prompt.normalizeReadError(nil)
	if !errors.Is(err, errChatInteractivePromptCancelled) {
		t.Fatalf("expected modal cancellation error, got %v", err)
	}
}

func TestChatSecretComposerPromptNormalizesInterruptsAndResetsPrompt(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("secret draft")

	composer := newChatSecretComposerPrompt(session, "key> ")
	err := normalizeChatComposerReadError(composer.session, ui.ErrInteractiveInputInterrupted)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected secret prompt interrupt to become EOF, got %v", err)
	}
	if !session.IsInterrupted() {
		t.Fatal("expected secret prompt interrupt to mark session interrupted")
	}
	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected secret prompt interrupt to reset prompt state, got %#v", snapshot)
	}
}

func TestChatTransientLineComposerNormalizesExitRequest(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("transient draft")

	composer := newChatTransientLineComposer(session)
	err := normalizeChatComposerReadError(composer.session, ui.ErrInteractiveInputExitRequested)
	if !errors.Is(err, ui.ErrInteractiveInputExitRequested) {
		t.Fatalf("expected transient line exit request to be preserved, got %v", err)
	}
	if !session.IsInterrupted() {
		t.Fatal("expected transient line exit request to mark session interrupted")
	}
	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected transient line exit request to reset prompt state, got %#v", snapshot)
	}
}

func TestChatAgentPanelComposerHooksDriveController(t *testing.T) {
	state := newChatAgentPanelModalState(3)
	controller := newChatAgentPanelModalController(&ChatSession{}, &state, "Agent Panel> ")
	composer := newChatAgentPanelComposer(&ChatSession{}, "Agent Panel> ", controller)
	hooks := composer.hooks()

	if !hooks.OnNavigate(ui.LineEditorSnapshot{}, 1) {
		t.Fatal("expected agent panel navigation to be consumed")
	}
	if state.Cursor != 0 {
		t.Fatalf("expected navigation without agents to keep cursor clamped, got %d", state.Cursor)
	}
	if !hooks.OnMove(ui.LineEditorSnapshot{}, 1) {
		t.Fatal("expected agent panel pane movement to be consumed")
	}
	if state.Pane != chatAgentPanelPaneMailbox {
		t.Fatalf("expected pane movement to focus mailbox, got %s", state.Pane.String())
	}
	if _, ok := hooks.OnSubmit(ui.LineEditorSnapshot{}); !ok {
		t.Fatal("expected agent panel submission to be consumed")
	}
	if !hooks.OnCancel(ui.LineEditorSnapshot{}) {
		t.Fatal("expected agent panel cancellation to be consumed")
	}
}

func TestParseChatAgentPanelOptionsSupportsExplicitClose(t *testing.T) {
	for _, argument := range []string{"panel close", "panel hide", "panel off"} {
		opts := parseChatAgentPanelOptions(argument, 8)
		if opts.Nav != "close" {
			t.Fatalf("expected %q to close the persistent panel, got %#v", argument, opts)
		}
	}
}

func TestParseChatAgentPanelOptionsSupportsFullDetail(t *testing.T) {
	opts := parseChatAgentPanelOptions("panel full 12", 8)
	if !opts.Full || opts.Limit != 12 || opts.Follow || opts.Nav != "" {
		t.Fatalf("expected explicit full detail options, got %#v", opts)
	}
}

func TestChatAgentPanelModalControllerDoesNotMarkRenderedWithoutSurface(t *testing.T) {
	state := newChatAgentPanelModalState(3)
	controller := newChatAgentPanelModalController(&ChatSession{}, &state, "Agent Panel> ")

	controller.Render()

	if controller.rendered {
		t.Fatal("expected agent panel controller not to mark rendered when overlay has no surface")
	}
}

func TestNormalizeChatAgentPanelComposerReadErrorClosesPanelWithoutInterruptingSession(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("panel draft")

	err := normalizeChatAgentPanelComposerReadError(session, ui.ErrInteractiveInputExitRequested)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected agent panel exit request to become EOF, got %v", err)
	}
	if session.IsInterrupted() {
		t.Fatal("expected agent panel exit request to close only the panel")
	}
	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected agent panel exit request to reset prompt state, got %#v", snapshot)
	}
}
