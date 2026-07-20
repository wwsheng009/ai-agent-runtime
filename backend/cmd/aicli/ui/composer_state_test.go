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

	placeholder := "[已粘贴 1001 字符 / 1 行]"
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

func TestComposerStateLargePastePlaceholderIncludesLineCount(t *testing.T) {
	composer := NewComposerState()
	large := strings.Repeat("a", LargePasteCharThreshold) + "\nsecond"

	composer.HandlePasteAt(0, large)

	want := "[已粘贴 1007 字符 / 2 行]"
	if got := composer.Text(); got != want {
		t.Fatalf("unexpected multiline paste placeholder: got %q want %q", got, want)
	}
	if got := composer.SubmitText(); got != large {
		t.Fatalf("expected multiline paste to expand on submit, len=%d", len(got))
	}
}

func TestComposerStateLargePastePlaceholdersAreUnique(t *testing.T) {
	composer := NewComposerState()
	large := strings.Repeat("a", LargePasteCharThreshold+1)
	cursor := composer.HandlePasteAt(0, large)
	cursor = composer.HandlePasteAt(cursor, large)

	if !strings.Contains(composer.Text(), "[已粘贴 1001 字符 / 1 行]") {
		t.Fatalf("expected first placeholder, got %q", composer.Text())
	}
	if !strings.Contains(composer.Text(), "[已粘贴 1001 字符 / 1 行] #2") {
		t.Fatalf("expected second placeholder, got %q", composer.Text())
	}
	if got := composer.SubmitText(); got != large+large {
		t.Fatalf("expected both pending pastes to expand, len=%d", len(got))
	}
}

func TestComposerStateLargePasteExpandsOnlyOnePlaceholderOccurrence(t *testing.T) {
	composer := NewComposerState()
	large := strings.Repeat("a", LargePasteCharThreshold+1)
	composer.HandlePasteAt(0, large)
	placeholder := composer.Text()
	composer.SetText(placeholder + " " + placeholder)

	got := composer.SubmitText()
	want := large + " " + placeholder
	if got != want {
		t.Fatalf("expected only one placeholder occurrence to expand, got len=%d", len(got))
	}
}

func TestComposerStateLargePasteExpandsTrackedPlaceholderWhenDuplicateTextInsertedBeforeIt(t *testing.T) {
	composer := NewComposerState()
	large := strings.Repeat("a", LargePasteCharThreshold+1)
	composer.HandlePasteAt(0, large)
	placeholder := composer.Text()

	composer.InsertTextAt(0, placeholder+" ")

	got := composer.SubmitText()
	want := placeholder + " " + large
	if got != want {
		t.Fatalf("expected only tracked placeholder to expand after duplicate prefix insertion:\nwant len=%d\n got len=%d", len(want), len(got))
	}
}

func TestComposerStateEditingInsideLargePastePlaceholderPrunesPendingPaste(t *testing.T) {
	composer := NewComposerState()
	large := strings.Repeat("a", LargePasteCharThreshold+1)
	composer.HandlePasteAt(0, large)

	composer.InsertTextAt(1, "x")

	if got := composer.SubmitText(); strings.Contains(got, large) {
		t.Fatalf("expected edited placeholder not to expand, got len=%d", len(got))
	}
	if pending := composer.PendingPastes(); len(pending) != 0 {
		t.Fatalf("expected edited placeholder to prune pending paste, got %#v", pending)
	}
}

func TestComposerStateDeletingLargePastePlaceholderPrunesPendingPaste(t *testing.T) {
	composer := NewComposerState()
	large := strings.Repeat("a", LargePasteCharThreshold+1)
	composer.HandlePasteAt(0, large)
	placeholderLen := len([]rune(composer.Text()))

	composer.DeleteRange(0, placeholderLen)

	if got := composer.SubmitText(); got != "" {
		t.Fatalf("expected deleted placeholder not to submit paste, got %q", got)
	}
	if pending := composer.PendingPastes(); len(pending) != 0 {
		t.Fatalf("expected deleted placeholder to prune pending paste, got %#v", pending)
	}
}

func TestComposerStateSetTextPrunesDeletedPendingPaste(t *testing.T) {
	composer := NewComposerState()
	large := strings.Repeat("a", LargePasteCharThreshold+1)
	composer.HandlePasteAt(0, large)

	composer.SetText("")
	composer.SetText("[已粘贴 1001 字符 / 1 行]")

	if got := composer.SubmitText(); got != "[已粘贴 1001 字符 / 1 行]" {
		t.Fatalf("expected deleted pending paste not to expand, got %q", got)
	}
	if pending := composer.PendingPastes(); len(pending) != 0 {
		t.Fatalf("expected pending pastes to be pruned, got %#v", pending)
	}
}
