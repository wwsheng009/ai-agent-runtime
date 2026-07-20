package commands

import (
	"strings"
	"testing"
	"time"

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

func TestChatPromptOverlayPriorityPromptsSerializeBeforeDisplayAndRead(t *testing.T) {
	session := &ChatSession{}
	_, firstCleanup, _ := showChatRuntimePriorityPrompt(session, []string{"first request"}, "first> ")

	secondShown := make(chan func(), 1)
	secondReadStarted := make(chan struct{}, 1)
	go func() {
		_, cleanup, _ := showChatRuntimePriorityPrompt(session, []string{"second request"}, "second> ")
		secondShown <- cleanup
		secondReadStarted <- struct{}{}
	}()

	select {
	case <-secondShown:
		t.Fatal("expected second priority prompt to remain blocked before display")
	case <-secondReadStarted:
		t.Fatal("expected second priority prompt not to enter its read phase")
	case <-time.After(50 * time.Millisecond):
	}

	firstCleanup()
	var secondCleanup func()
	select {
	case secondCleanup = <-secondShown:
	case <-time.After(time.Second):
		t.Fatal("expected second priority prompt to display after first cleanup")
	}
	select {
	case <-secondReadStarted:
	case <-time.After(time.Second):
		t.Fatal("expected second prompt read phase after display serialization")
	}
	secondCleanup()
}

func TestChatPromptOverlayPriorityPromptCleanupIsIdempotent(t *testing.T) {
	session := &ChatSession{}
	_, firstCleanup, _ := showChatRuntimePriorityPrompt(session, []string{"first"}, "first> ")
	firstCleanup()
	firstCleanup()

	_, secondCleanup, _ := showChatRuntimePriorityPrompt(session, []string{"second"}, "second> ")
	thirdShown := make(chan func(), 1)
	go func() {
		_, cleanup, _ := showChatRuntimePriorityPrompt(session, []string{"third"}, "third> ")
		thirdShown <- cleanup
	}()
	select {
	case <-thirdShown:
		t.Fatal("expected second prompt to retain the serialization lock")
	case <-time.After(50 * time.Millisecond):
	}
	secondCleanup()
	secondCleanup()
	select {
	case cleanup := <-thirdShown:
		cleanup()
	case <-time.After(time.Second):
		t.Fatal("expected exactly one unlock to release the third prompt")
	}
}

func TestPriorityPromptViewportUsesStructuralHeaderBodyAndFooter(t *testing.T) {
	viewport := priorityPromptViewport([]string{
		"queued input suspended",
		"approval request",
		"tool=shell",
		"reason=policy",
		"risk=high",
		"operation choices",
	})
	if got := strings.Join(viewport.HeaderLines, "\n"); got != "queued input suspended | approval request" {
		t.Fatalf("unexpected semantic header: %q", got)
	}
	if got := strings.Join(viewport.BodyLines, "\n"); got != "tool=shell | reason=policy | risk=high" {
		t.Fatalf("unexpected semantic body: %q", got)
	}
	if got := strings.Join(viewport.FooterLines, "\n"); got != "operation choices" {
		t.Fatalf("unexpected semantic footer: %q", got)
	}
}
