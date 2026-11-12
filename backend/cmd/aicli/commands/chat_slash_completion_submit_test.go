package commands

import (
	"testing"
)

// TestChatSlashCompletionMemorySubcommandsSubmitOnEnter reproduces the TUI
// report: a fully typed /memory subcommand must execute, not be extended with
// another copy of the same token by the completion controller.
func TestChatSlashCompletionMemorySubcommandsSubmitOnEnter(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		"/memory",
		"/memory status",
		"/memory list",
		"/memory list ",
		"/memory add",
		"/memory search",
		"/memory note",
		"/memory flush",
		"/memory show",
		"/memory why",
		"/memory why-not",
		"/memory stale",
		"/memory revalidate",
		"/memory correct",
		"/memory forget",
		"/memory export",
		"/memory jobs",
		"/memory consolidate",
		"/memory mode",
		"/memory reset",
	} {
		controller := newChatSlashCompletionController(&ChatSession{})
		controller.UpdateAt(text, len([]rune(text)))
		if !controller.state.Active {
			t.Fatalf("%q: expected slash completion popup to be active", text)
		}

		nextText, nextCursor, consumed := controller.ApplySubmission(text, len([]rune(text)))
		if consumed {
			t.Fatalf("%q: Enter was consumed by completion, got %q", text, nextText)
		}
		if nextText != text || nextCursor != len([]rune(text)) {
			t.Fatalf("%q: submit path changed input to %q/%d", text, nextText, nextCursor)
		}
	}
}

// TestChatSlashCompletionAcceptedSubcommandSubmitsOnNextEnter pins the exact
// command sequence from the report: /memory li -> Tab/Enter completion to
// "/memory list " -> Enter must submit instead of appending "list" again.
func TestChatSlashCompletionAcceptedSubcommandSubmitsOnNextEnter(t *testing.T) {
	t.Parallel()

	controller := newChatSlashCompletionController(&ChatSession{})
	text := "/memory li"
	controller.UpdateAt(text, len([]rune(text)))

	text, cursor, completed := controller.ApplyCompletion(text, len([]rune(text)))
	if !completed {
		t.Fatal("expected /memory li completion to be accepted")
	}
	if text != "/memory list " {
		t.Fatalf("completion = %q, want /memory list with trailing space", text)
	}

	nextText, nextCursor, consumed := controller.ApplySubmission(text, cursor)
	if consumed {
		t.Fatalf("second Enter was consumed by completion, got %q", nextText)
	}
	if nextText != text || nextCursor != cursor {
		t.Fatalf("second Enter changed input to %q/%d", nextText, nextCursor)
	}
}

// TestChatSlashCompletionExplicitNavigationStillAcceptsOnEnter keeps the
// popup's arrow-key workflow: once the user explicitly moves the selection,
// Enter accepts that candidate even on an otherwise empty argument slot.
func TestChatSlashCompletionExplicitNavigationStillAcceptsOnEnter(t *testing.T) {
	t.Parallel()

	controller := newChatSlashCompletionController(&ChatSession{})
	text := "/memory "
	controller.UpdateAt(text, len([]rune(text)))
	if !controller.state.Active {
		t.Fatalf("expected /memory popup to be active, got %#v", controller.state)
	}
	if !controller.Navigate(1) {
		t.Fatal("expected navigation to consume the arrow key")
	}
	selected := controller.state.Candidates[controller.state.Selected].Command

	nextText, _, consumed := controller.ApplySubmission(text, len([]rune(text)))
	if !consumed {
		t.Fatal("explicitly selected candidate must be accepted by Enter")
	}
	if nextText != "/memory "+selected+" " {
		t.Fatalf("Enter completion = %q, want /memory %s ", nextText, selected)
	}
}

// TestChatSlashCompletionEmptyAssignmentStillAcceptsOnEnter keeps the
// assignment-value workflow intact: --model= has no value yet, so Enter still
// accepts a candidate instead of submitting an incomplete command.
func TestChatSlashCompletionEmptyAssignmentStillAcceptsOnEnter(t *testing.T) {
	t.Parallel()

	session := newSlashCompletionArgumentTestSession()
	controller := newChatSlashCompletionController(session)
	text := "/model --model="
	controller.UpdateAt(text, len([]rune(text)))
	if !controller.state.Active {
		t.Fatalf("expected --model= popup to be active, got %#v", controller.state)
	}

	nextText, _, consumed := controller.ApplySubmission(text, len([]rune(text)))
	if !consumed {
		t.Fatal("empty assignment value must still accept the selected candidate")
	}
	if nextText == text {
		t.Fatalf("assignment completion did not insert a value: %q", nextText)
	}
}
