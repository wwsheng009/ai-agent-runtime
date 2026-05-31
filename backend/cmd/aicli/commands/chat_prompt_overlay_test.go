package commands

import (
	"strings"
	"testing"

	"github.com/fatih/color"
)

func TestChatPromptOverlayClearSelectionPopupResetsPromptWithoutSurface(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("stale popup input")

	clearRuntimeSelectionPopup(session)

	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected selection popup cleanup to reset prompt without surface, got %#v", snapshot)
	}
}

func TestChatPromptOverlayShowSelectionPopupDoesNotClearPromptWithoutSurface(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("main draft")

	showRuntimeSelectionPopup(session, []string{"option"}, "select> ")

	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "main draft" {
		t.Fatalf("expected non-surface popup show to leave draft intact, got %#v", snapshot)
	}
}

func TestChatPromptOverlayClearComposerPreviewResetsPromptWithoutSurface(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("secret draft")

	clearRuntimeComposerPrompt(session)

	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected composer preview cleanup to reset prompt without surface, got %#v", snapshot)
	}
}

func TestChatPromptOverlayPendingPasteDraftFallsBackWithoutSurface(t *testing.T) {
	overlay := newChatPromptOverlay(&ChatSession{})

	if overlay.showPendingPasteDraft(2, "first\nsecond") {
		t.Fatal("expected pending paste draft preview to fall back without surface")
	}
	if overlay.clearPendingPasteDraft() {
		t.Fatal("expected pending paste draft cleanup to fall back without surface")
	}
}

func TestChatPromptOverlayPriorityPromptFallbackOutputContract(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = oldNoColor }()

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("approval draft")

	var readPrompt string
	var transient bool
	output := captureStdout(t, func() {
		var cleanup func()
		readPrompt, cleanup, transient = showChatRuntimePriorityPrompt(session, []string{
			"[approval] command=git status",
		}, "[approval] allow bash? [y/N]: \n")
		cleanup()
	})

	if transient {
		t.Fatal("expected fallback priority prompt to be persistent")
	}
	if readPrompt != "[approval] allow bash? [y/N]: " {
		t.Fatalf("expected sanitized readable prompt, got %q", readPrompt)
	}
	if !strings.Contains(output, "[approval] command=git status") || !strings.Contains(output, "[approval] allow bash? [y/N]: ") {
		t.Fatalf("expected fallback output to include approval details and prompt, got %q", output)
	}
	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected priority prompt fallback to clear active draft before output, got %#v", snapshot)
	}
}
