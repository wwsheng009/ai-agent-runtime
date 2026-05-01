package ui

import (
	"strings"
	"testing"
)

func TestNormalizePastedTextConvertsCRLFAndCR(t *testing.T) {
	got := NormalizePastedText("a\r\nb\rc")
	if got != "a\nb\nc" {
		t.Fatalf("unexpected normalized paste: %q", got)
	}
}

func TestComposerStateHandlePasteInsertsSmallPasteDirectly(t *testing.T) {
	composer := NewComposerState()
	composer.SetText("hello ")
	cursor := composer.HandlePasteAt(len([]rune(composer.Text())), "first\r\nsecond")

	if composer.Text() != "hello first\nsecond" {
		t.Fatalf("unexpected visible text: %q", composer.Text())
	}
	if cursor != len([]rune(composer.Text())) {
		t.Fatalf("expected cursor at end, got %d", cursor)
	}
	if composer.SubmitText() != "hello first\nsecond" {
		t.Fatalf("unexpected submitted text: %q", composer.SubmitText())
	}
}

func TestComposerStateLargePasteUsesPlaceholderAndExpandsOnSubmit(t *testing.T) {
	composer := NewComposerState()
	large := strings.Repeat("a", LargePasteCharThreshold+1)
	cursor := composer.HandlePasteAt(0, large)

	placeholder := "[Pasted Content 1001 chars]"
	if composer.Text() != placeholder {
		t.Fatalf("unexpected visible placeholder: %q", composer.Text())
	}
	if cursor != len([]rune(placeholder)) {
		t.Fatalf("expected cursor after placeholder, got %d", cursor)
	}
	if got := composer.SubmitText(); got != large {
		t.Fatalf("expected submit to expand paste, len=%d", len(got))
	}
}

func TestComposerStateLargePastePlaceholdersAreUnique(t *testing.T) {
	composer := NewComposerState()
	large := strings.Repeat("a", LargePasteCharThreshold+1)
	cursor := composer.HandlePasteAt(0, large)
	cursor = composer.HandlePasteAt(cursor, large)

	if !strings.Contains(composer.Text(), "[Pasted Content 1001 chars]") {
		t.Fatalf("expected first placeholder, got %q", composer.Text())
	}
	if !strings.Contains(composer.Text(), "[Pasted Content 1001 chars] #2") {
		t.Fatalf("expected second placeholder, got %q", composer.Text())
	}
	if got := composer.SubmitText(); got != large+large {
		t.Fatalf("expected both pending pastes to expand, len=%d", len(got))
	}
}

func TestComposerStateSetTextPrunesDeletedPendingPaste(t *testing.T) {
	composer := NewComposerState()
	large := strings.Repeat("a", LargePasteCharThreshold+1)
	composer.HandlePasteAt(0, large)

	composer.SetText("")
	composer.SetText("[Pasted Content 1001 chars]")

	if got := composer.SubmitText(); got != "[Pasted Content 1001 chars]" {
		t.Fatalf("expected deleted pending paste not to expand, got %q", got)
	}
	if pending := composer.PendingPastes(); len(pending) != 0 {
		t.Fatalf("expected pending pastes to be pruned, got %#v", pending)
	}
}
