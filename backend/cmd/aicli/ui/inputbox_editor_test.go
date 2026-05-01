package ui

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestReadInteractiveLine_HandlesArrowKeysAndEditing(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("ab\x1b[DZ\n"),
		&output,
		"你> ",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "aZb" {
		t.Fatalf("expected edited line aZb, got %q", line)
	}
}

func TestReadInteractiveLine_CtrlUClearsCurrentLine(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("hello\x15world\n"),
		&output,
		"你> ",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "world" {
		t.Fatalf("expected ctrl+u to clear current line before new input, got %q", line)
	}
}

func TestReadInteractiveLine_CtrlWDeletesPreviousWord(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("hello world\x17\n"),
		&output,
		"你> ",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "hello" {
		t.Fatalf("expected ctrl+w to delete the previous word, got %q", line)
	}
}

func TestReadInteractiveLine_CtrlKDeletesSuffixFromCursor(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("hello world\x1b[D\x1b[D\x1b[D\x0b\n"),
		&output,
		"你> ",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "hello wo" {
		t.Fatalf("expected ctrl+k to delete text from cursor to line end, got %q", line)
	}
}

func TestReadInteractiveLine_CtrlLRedrawsCurrentLine(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("abc\x0c\n"),
		&output,
		"你> ",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "abc" {
		t.Fatalf("expected ctrl+l to keep current input unchanged, got %q", line)
	}
	rendered := output.String()
	if count := strings.Count(rendered, cursorRestoreSequence); count < 2 {
		t.Fatalf("expected ctrl+l to trigger an additional redraw, got %d in %q", count, rendered)
	}
}

func TestReadInteractiveLine_CtrlYYanksLastKilledText(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("hello world\x17\x19\n"),
		&output,
		"你> ",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "hello world" {
		t.Fatalf("expected ctrl+y to restore the last killed text, got %q", line)
	}
}

func TestReadInteractiveLine_CtrlTTransposesLastTwoCharacters(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("abc\x14\n"),
		&output,
		"你> ",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "acb" {
		t.Fatalf("expected ctrl+t to transpose the last two characters, got %q", line)
	}
}

func TestReadInteractiveLine_CtrlRSearchesHistoryAndCyclesBackward(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("alpha\x12\x12\n"),
		&output,
		"你> ",
		[]string{"first alpha", "beta", "second alpha"},
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "first alpha" {
		t.Fatalf("expected ctrl+r to cycle backward through matching history, got %q", line)
	}
}

func TestReadInteractiveLine_CtrlPNavigateHistory(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("draft\x10\x0e\n"),
		&output,
		"你> ",
		[]string{"first", "second"},
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "draft" {
		t.Fatalf("expected ctrl+p/ctrl+n to navigate history and restore the draft, got %q", line)
	}
}

func TestReadInteractiveLine_AltBMovesBackwardByWord(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("hello world\x1bbX\n"),
		&output,
		"你> ",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "hello Xworld" {
		t.Fatalf("expected alt+b to move backward by word, got %q", line)
	}
}

func TestReadInteractiveLine_AltBackspaceDeletesPreviousWord(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("hello world\x1b\x7fX\n"),
		&output,
		"你> ",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "helloX" {
		t.Fatalf("expected alt+backspace to delete the previous word, got %q", line)
	}
}

func TestDecodeEscapeInteractiveKey_AltDDeletesForwardWord(t *testing.T) {
	decoded, ok := decodeEscapeInteractiveKey([]byte{27, 'd'})
	if !ok {
		t.Fatal("expected alt+d to decode as an interactive key")
	}
	if decoded.key.kind != editorKeyDeleteForwardWord {
		t.Fatalf("expected alt+d to map to delete-forward-word, got %#v", decoded.key.kind)
	}
	if decoded.consumed != 2 {
		t.Fatalf("expected alt+d to consume 2 bytes, got %d", decoded.consumed)
	}
}

func TestReadInteractiveLine_CtrlDeleteDeletesForwardWord(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("hello world\x1b[D\x1b[D\x1b[D\x1b[D\x1b[D\x1b[3;5~X\n"),
		&output,
		"你> ",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "hello X" {
		t.Fatalf("expected ctrl+delete to delete the word after the cursor, got %q", line)
	}
}

func TestReadInteractiveLine_CtrlArrowMovesByWord(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("hello world\x1b[1;5D\x1b[1;5CX\n"),
		&output,
		"你> ",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "hello worldX" {
		t.Fatalf("expected ctrl+arrow word movement to preserve the word boundary, got %q", line)
	}
}

func TestReadInteractiveLine_CtrlGAbortsReverseSearchAndRestoresDraft(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("alpha\x12\x12\x07\n"),
		&output,
		"你> ",
		[]string{"first alpha", "beta", "second alpha"},
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "alpha" {
		t.Fatalf("expected ctrl+g to abort reverse search and restore the draft, got %q", line)
	}
}

func TestFindReverseHistoryMatch_ReturnsMostRecentMatch(t *testing.T) {
	history := []string{"alpha", "beta alpha", "gamma alpha", "delta"}
	idx, match, ok := findReverseHistoryMatch(history, "alpha", len(history))
	if !ok {
		t.Fatal("expected a history match")
	}
	if idx != 2 || match != "gamma alpha" {
		t.Fatalf("unexpected match idx=%d match=%q", idx, match)
	}
}

func TestFindReverseHistoryMatch_ContinuesSearchingBackward(t *testing.T) {
	history := []string{"alpha", "beta alpha", "gamma alpha", "delta"}
	idx, match, ok := findReverseHistoryMatch(history, "alpha", 2)
	if !ok {
		t.Fatal("expected a history match")
	}
	if idx != 1 || match != "beta alpha" {
		t.Fatalf("unexpected match idx=%d match=%q", idx, match)
	}
}

func TestReadInteractiveLine_RestoresDraftAfterHistoryNavigation(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("draft\x1b[A\x1b[B\n"),
		&output,
		"你> ",
		[]string{"first", "second"},
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "draft" {
		t.Fatalf("expected draft to be restored after history navigation, got %q", line)
	}
}

func TestReadInteractiveLine_HandlesBracketedPasteAsAtomicMultiLineInput(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("\x1b[200~first\nsecond\x1b[201~\n"),
		&output,
		"你> ",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "first\nsecond" {
		t.Fatalf("expected pasted multiline input to stay intact, got %q", line)
	}
}

func TestReadInteractiveLine_RendersMultilinePasteWithCarriageReturns(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("\x1b[200~first\nsecond\x1b[201~\n"),
		&output,
		"你> ",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "first\nsecond" {
		t.Fatalf("expected pasted multiline input to stay intact, got %q", line)
	}
	rendered := output.String()
	if !strings.Contains(rendered, "first\r\nsecond") {
		t.Fatalf("expected redraw to render multiline input with CRLF, got %q", rendered)
	}
	if strings.Contains(rendered, "first\nsecond") {
		t.Fatalf("expected redraw not to use bare LF for multiline input, got %q", rendered)
	}
}

func TestReadInteractiveLine_OnChangeReceivesRealMultilineInput(t *testing.T) {
	var output bytes.Buffer
	var changes []string
	line, err := readInteractiveLine(
		strings.NewReader("\x1b[200~first\nsecond\x1b[201~\n"),
		&output,
		"你> ",
		nil,
		func(text string) {
			changes = append(changes, text)
		},
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "first\nsecond" {
		t.Fatalf("expected pasted multiline input to stay intact, got %q", line)
	}
	for _, change := range changes {
		if change == "first\nsecond" {
			return
		}
	}
	t.Fatalf("expected onChange to receive real newline-preserving input, got %#v", changes)
}

func TestReadInteractiveLine_BuffersBracketedPasteIntoSingleRedraw(t *testing.T) {
	var output bytes.Buffer
	prompt := "你> "
	output.WriteString(prompt)
	output.WriteString(cursorSaveSequence)

	longInput := "\x1b[200~" + strings.Repeat("a", 96) + "\x1b[201~\n"
	line, err := readInteractiveLine(
		strings.NewReader(longInput),
		&output,
		prompt,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	expectedLine := strings.TrimSuffix(strings.TrimRight(longInput, "\r\n"), "\x1b[201~")
	expectedLine = strings.TrimPrefix(expectedLine, "\x1b[200~")
	if line != expectedLine {
		t.Fatalf("unexpected line: %q", line)
	}

	rendered := output.String()
	if count := strings.Count(rendered, prompt); count != 1 {
		t.Fatalf("expected prompt to be rendered once, got %d in %q", count, rendered)
	}
	if count := strings.Count(rendered, cursorRestoreSequence); count != 1 {
		t.Fatalf("expected one redraw after buffered paste, got %d in %q", count, rendered)
	}
}

func TestReadInteractiveLine_BuffersRapidPlainInputIntoSingleRedraw(t *testing.T) {
	var output bytes.Buffer
	prompt := "你> "
	output.WriteString(prompt)
	output.WriteString(cursorSaveSequence)

	longInput := strings.Repeat("a", 96) + "\n"
	line, err := readInteractiveLine(
		strings.NewReader(longInput),
		&output,
		prompt,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != strings.Repeat("a", 96) {
		t.Fatalf("unexpected line: %q", line)
	}

	rendered := output.String()
	if count := strings.Count(rendered, prompt); count != 1 {
		t.Fatalf("expected prompt to be rendered once, got %d in %q", count, rendered)
	}
	if count := strings.Count(rendered, cursorRestoreSequence); count != 1 {
		t.Fatalf("expected one redraw after buffered plain input, got %d in %q", count, rendered)
	}
}

func TestReadInteractiveLine_PreservesNonBracketedPasteNewlineWhenMoreInputIsPending(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("first\nsecond\x1b[D\x1b[C\n"),
		&output,
		"你> ",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "first\nsecond" {
		t.Fatalf("expected non-bracketed pasted newline to stay intact, got %q", line)
	}
}

func TestReadInteractiveLine_CtrlCOnEmptyLineRequestsExit(t *testing.T) {
	var output bytes.Buffer
	_, err := readInteractiveLine(
		strings.NewReader("\x03"),
		&output,
		"你> ",
		nil,
		nil,
	)
	if !errors.Is(err, ErrInteractiveInputExitRequested) {
		t.Fatalf("expected exit request error, got %v", err)
	}
}

func TestReadInteractiveLine_CtrlCWithTypedContentCancelsInput(t *testing.T) {
	var output bytes.Buffer
	_, err := readInteractiveLine(
		strings.NewReader("hello\x03"),
		&output,
		"你> ",
		nil,
		nil,
	)
	if !errors.Is(err, ErrInteractiveInputInterrupted) {
		t.Fatalf("expected interrupted error, got %v", err)
	}
}

func TestReadInteractiveLine_CtrlDOnEmptyLineRequestsExit(t *testing.T) {
	var output bytes.Buffer
	_, err := readInteractiveLine(
		strings.NewReader("\x04"),
		&output,
		"你> ",
		nil,
		nil,
	)
	if !errors.Is(err, ErrInteractiveInputExitRequested) {
		t.Fatalf("expected exit request error, got %v", err)
	}
}
