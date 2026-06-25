package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReadInteractiveLine_HandlesArrowKeysAndEditing(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("ab\x1b[DZ\n"),
		&output,
		UserPromptText(0),
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

func TestDecodeInteractiveKeyMarksLoneCarriageReturnEnter(t *testing.T) {
	decoded, ok := decodeInteractiveKey([]byte{'\r'})
	if !ok {
		t.Fatal("expected lone carriage return to decode")
	}
	if decoded.key.kind != editorKeyEnter {
		t.Fatalf("expected enter key, got %#v", decoded.key)
	}
	if !decoded.key.fromCarriageReturn {
		t.Fatal("expected lone carriage return enter to be marked")
	}
	if decoded.consumed != 1 {
		t.Fatalf("expected one byte consumed, got %d", decoded.consumed)
	}

	decoded, ok = decodeInteractiveKey([]byte{'\r', '\n'})
	if !ok {
		t.Fatal("expected CRLF to decode")
	}
	if decoded.key.kind != editorKeyEnter {
		t.Fatalf("expected enter key for CRLF, got %#v", decoded.key)
	}
	if decoded.key.fromCarriageReturn {
		t.Fatal("expected CRLF enter not to request trailing LF drain")
	}
	if decoded.consumed != 2 {
		t.Fatalf("expected CRLF to consume two bytes, got %d", decoded.consumed)
	}
}

func TestDecodeInteractiveKey_CtrlVRequestsClipboardPaste(t *testing.T) {
	decoded, ok := decodeInteractiveKey([]byte{22})
	if !ok {
		t.Fatal("expected ctrl+v to decode")
	}
	if decoded.key.kind != editorKeyPasteClipboard {
		t.Fatalf("expected clipboard paste key, got %#v", decoded.key)
	}
	if decoded.consumed != 1 {
		t.Fatalf("expected ctrl+v to consume one byte, got %d", decoded.consumed)
	}
}

func TestNextInteractiveKeyCancelableReadDoesNotBlockWhenReadinessUnsupported(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()

	oldWait := waitForInteractiveInputReady
	waitForInteractiveInputReady = func(int, time.Duration) (bool, error) {
		return false, errInteractiveInputReadinessUnsupported
	}
	defer func() {
		waitForInteractiveInputReady = oldWait
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	result := make(chan error, 1)
	pending := make([]byte, 0, 16)
	go func() {
		_, _, err := nextInteractiveKey(ctx, reader, &pending, reader)
		result <- err
	}()

	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected context deadline, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancelable read blocked after unsupported readiness")
	}
}

func TestNextInteractiveKeyConsumesSpecialKeyBeforeReadiness(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()

	oldConsume := consumeSpecialInteractiveKey
	oldWait := waitForInteractiveInputReady
	consumeSpecialInteractiveKey = func(int) (editorKey, bool, error) {
		return editorKey{kind: editorKeyPasteClipboard, fromConsoleCtrlV: true}, true, nil
	}
	waitForInteractiveInputReady = func(int, time.Duration) (bool, error) {
		t.Fatal("special key should be consumed before readiness polling")
		return false, nil
	}
	t.Cleanup(func() {
		consumeSpecialInteractiveKey = oldConsume
		waitForInteractiveInputReady = oldWait
	})

	pending := make([]byte, 0, 16)
	key, ok, err := nextInteractiveKey(context.Background(), reader, &pending, reader)
	if err != nil {
		t.Fatalf("nextInteractiveKey returned error: %v", err)
	}
	if !ok || key.kind != editorKeyPasteClipboard || !key.fromConsoleCtrlV {
		t.Fatalf("expected console Ctrl+V paste key, got key=%#v ok=%v", key, ok)
	}
}

func TestSupportsCancelableInteractiveInputReadReflectsReadinessProbe(t *testing.T) {
	oldWait := waitForInteractiveInputReady
	waitForInteractiveInputReady = func(int, time.Duration) (bool, error) {
		return false, errInteractiveInputReadinessUnsupported
	}
	defer func() {
		waitForInteractiveInputReady = oldWait
	}()

	if SupportsCancelableInteractiveInputRead() {
		t.Fatal("expected unsupported readiness probe to disable cancelable interactive input")
	}
}

func TestInteractiveInputCarryoverRoundTripsBytes(t *testing.T) {
	_ = takeInteractiveInputCarryover()
	defer takeInteractiveInputCarryover()

	storeInteractiveInputCarryover([]byte("next"))
	storeInteractiveInputCarryover([]byte("-line"))

	got := takeInteractiveInputCarryover()
	if string(got) != "next-line" {
		t.Fatalf("expected carryover bytes to round-trip, got %q", string(got))
	}
	if leftover := takeInteractiveInputCarryover(); len(leftover) != 0 {
		t.Fatalf("expected carryover to be empty after take, got %q", string(leftover))
	}
}

func TestReadInteractiveLine_CtrlUClearsCurrentLine(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("hello\x15world\n"),
		&output,
		UserPromptText(0),
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
		UserPromptText(0),
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
		UserPromptText(0),
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
		UserPromptText(0),
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
	// 重绘会输出 `\x1b[K` 清除输入区。初始输入产生一次重绘，Ctrl+L 触发
	// 另外一次，因此应不少于 2 个 `\x1b[K`。
	if count := strings.Count(rendered, "\x1b[K"); count < 2 {
		t.Fatalf("expected ctrl+l to trigger an additional redraw, got %d clears in %q", count, rendered)
	}
}

func TestReadInteractiveLine_CtrlYYanksLastKilledText(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("hello world\x17\x19\n"),
		&output,
		UserPromptText(0),
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
		UserPromptText(0),
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
		UserPromptText(0),
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
		UserPromptText(0),
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
		UserPromptText(0),
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
		UserPromptText(0),
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
		UserPromptText(0),
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

func TestReadInteractiveLine_AltDeleteDeletesForwardWord(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("hello world\x1b[D\x1b[D\x1b[D\x1b[D\x1b[D\x1b[3;3~X\n"),
		&output,
		UserPromptText(0),
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "hello X" {
		t.Fatalf("expected alt+delete to delete the word after the cursor, got %q", line)
	}
}

func TestReadInteractiveLine_CtrlArrowMovesByWord(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("hello world\x1b[1;5D\x1b[1;5CX\n"),
		&output,
		UserPromptText(0),
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
		UserPromptText(0),
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
		UserPromptText(0),
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

func TestInputBoxAddToHistoryTrimsSkipsAdjacentDuplicatesAndLimitsSize(t *testing.T) {
	ib := NewInputBox(nil)

	ib.AddToHistory("  first  ")
	ib.AddToHistory("first")
	ib.AddToHistory("")
	ib.AddToHistory("second")

	if got := ib.GetHistorySize(); got != 2 {
		t.Fatalf("expected adjacent duplicate and empty history entries to be skipped, got size %d history=%#v", got, ib.GetHistory())
	}
	if first, ok := ib.GetHistoryAt(0); !ok || first != "first" {
		t.Fatalf("expected first trimmed history entry, got %q ok=%v", first, ok)
	}
	if second, ok := ib.GetHistoryAt(1); !ok || second != "second" {
		t.Fatalf("expected second history entry, got %q ok=%v", second, ok)
	}

	for i := 0; i < defaultInputHistoryLimit+5; i++ {
		ib.AddToHistory(fmt.Sprintf("item-%03d", i))
	}
	if got := ib.GetHistorySize(); got != defaultInputHistoryLimit {
		t.Fatalf("expected history to be capped at %d, got %d", defaultInputHistoryLimit, got)
	}
	oldest, ok := ib.GetHistoryAt(0)
	if !ok || oldest != "item-005" {
		t.Fatalf("expected oldest retained entry item-005, got %q ok=%v", oldest, ok)
	}
	if previous, ok := ib.PreviousHistory(); !ok || previous != "item-204" {
		t.Fatalf("expected history cursor to point after latest retained entry, got %q ok=%v", previous, ok)
	}
}

func TestReadInteractiveLine_HandlesBracketedPasteAsAtomicMultiLineInput(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("\x1b[200~first\nsecond\x1b[201~\n"),
		&output,
		UserPromptText(0),
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

func TestReadInteractiveLine_CtrlVInsertsClipboardText(t *testing.T) {
	oldClipboard := readInteractiveClipboardText
	readInteractiveClipboardText = func() (string, error) {
		return "first\r\nsecond", nil
	}
	t.Cleanup(func() {
		readInteractiveClipboardText = oldClipboard
	})

	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("\x16\n"),
		&output,
		UserPromptText(0),
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != "first\nsecond" {
		t.Fatalf("expected ctrl+v to insert normalized clipboard text, got %q", line)
	}
}

func TestReadInteractiveLine_CtrlVReportsPasteInactiveAfterRender(t *testing.T) {
	oldClipboard := readInteractiveClipboardText
	readInteractiveClipboardText = func() (string, error) {
		return "first\r\nsecond", nil
	}
	t.Cleanup(func() {
		readInteractiveClipboardText = oldClipboard
	})

	var output bytes.Buffer
	var snapshots []LineEditorSnapshot
	line, err := readInteractiveLineWithHooks(
		strings.NewReader("\x16\n"),
		&output,
		UserPromptText(0),
		nil,
		nil,
		&LineEditorHooks{
			OnChange: func(snapshot LineEditorSnapshot) {
				snapshots = append(snapshots, snapshot)
			},
		},
		true,
		false,
	)
	if err != nil {
		t.Fatalf("readInteractiveLineWithHooks: %v", err)
	}
	if line != "first\nsecond" {
		t.Fatalf("expected ctrl+v to insert normalized clipboard text, got %q", line)
	}

	sawPasteActive := false
	sawPasteInactive := false
	for _, snapshot := range snapshots {
		if snapshot.Text != "first\nsecond" {
			continue
		}
		if snapshot.PasteActive {
			sawPasteActive = true
		} else {
			sawPasteInactive = true
		}
	}
	if !sawPasteActive {
		t.Fatalf("expected ctrl+v insertion to publish a paste-active snapshot, got %#v", snapshots)
	}
	if !sawPasteInactive {
		t.Fatalf("expected ctrl+v insertion to publish a paste-inactive snapshot after render, got %#v", snapshots)
	}
}

func TestReadInteractiveLine_RendersMultilinePasteWithCarriageReturns(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("\x1b[200~first\nsecond\x1b[201~\n"),
		&output,
		UserPromptText(0),
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
		UserPromptText(0),
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
	prompt := UserPromptText(0)
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
	pastedBody := strings.Repeat("a", 96)
	if count := strings.Count(rendered, pastedBody); count != 1 {
		t.Fatalf("expected one bounded redraw after buffered paste, got %d content renders in %q", count, rendered)
	}
	if strings.Contains(rendered, cursorRestoreSequence) {
		t.Fatalf("expected redraw to avoid absolute \\x1b[u restore, got %q", rendered)
	}
}

func TestReadInteractiveLine_MultiLinePasteRendersBodyOnce(t *testing.T) {
	var output bytes.Buffer
	prompt := UserPromptText(0)
	output.WriteString(prompt)

	body := "line-01\nline-02\nline-03\nline-04\nline-05\nline-06\nline-07\nline-08\nline-09\nline-10\nline-11\nline-12"
	pastedInput := "\x1b[200~" + body + "\x1b[201~\n"
	line, err := readInteractiveLine(
		strings.NewReader(pastedInput),
		&output,
		prompt,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != body {
		t.Fatalf("expected pasted multiline input to stay intact, got %q", line)
	}

	rendered := output.String()
	rerenderedBody := strings.ReplaceAll(body, "\n", "\r\n")
	if count := strings.Count(rendered, rerenderedBody); count != 1 {
		t.Fatalf("expected multi-line paste body to be rendered once, got %d in %q", count, rendered)
	}
	if strings.Contains(rendered, cursorRestoreSequence) {
		t.Fatalf("expected redraw to use relative cursor motion, got absolute \\x1b[u in %q", rendered)
	}
}

func TestReadInteractiveLine_LargeBracketedPasteUsesPlaceholderButSubmitsFullText(t *testing.T) {
	var output bytes.Buffer
	prompt := UserPromptText(0)
	output.WriteString(prompt)
	output.WriteString(cursorSaveSequence)

	large := strings.Repeat("a", LargePasteCharThreshold+1)
	line, err := readInteractiveLine(
		strings.NewReader("\x1b[200~"+large+"\x1b[201~\n"),
		&output,
		prompt,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if line != large {
		t.Fatalf("expected submitted text to expand large paste, len=%d", len(line))
	}

	rendered := output.String()
	if !strings.Contains(rendered, "[Pasted Content 1001 chars]") {
		t.Fatalf("expected visible placeholder for large paste, got %q", rendered)
	}
	if strings.Contains(rendered, large) {
		t.Fatalf("expected rendered input to avoid full large paste")
	}
}

func TestReadInteractiveLine_LargePastePlaceholderTracksThroughTypedPrefix(t *testing.T) {
	var output bytes.Buffer
	large := strings.Repeat("a", LargePasteCharThreshold+1)
	placeholder := "[Pasted Content 1001 chars]"
	input := "\x1b[200~" + large + "\x1b[201~\x01" + placeholder + " \n"

	line, err := readInteractiveLine(
		strings.NewReader(input),
		&output,
		UserPromptText(0),
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	want := placeholder + " " + large
	if line != want {
		t.Fatalf("expected large paste to expand at tracked placeholder after typed prefix:\nwant len=%d\n got len=%d", len(want), len(line))
	}
}

func TestReadInteractiveLine_BuffersRapidPlainInputIntoSingleRedraw(t *testing.T) {
	var output bytes.Buffer
	prompt := UserPromptText(0)
	output.WriteString(prompt)
	output.WriteString(cursorSaveSequence)

	longInput := strings.Repeat("a", 96) + "\n"
	line, err := readInteractiveLineWithOptions(
		strings.NewReader(longInput),
		&output,
		prompt,
		nil,
		nil,
		true,
		true,
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
	pastedBody := strings.Repeat("a", 96)
	if count := strings.Count(rendered, pastedBody); count != 1 {
		t.Fatalf("expected one bounded redraw after buffered plain input, got %d content renders in %q", count, rendered)
	}
	if strings.Contains(rendered, cursorRestoreSequence) {
		t.Fatalf("expected redraw to avoid absolute \\x1b[u restore, got %q", rendered)
	}
}

func TestDefaultPasteBurstHoldFirstRunePlatformPolicy(t *testing.T) {
	got := defaultPasteBurstHoldFirstRune()
	if got {
		t.Fatalf("expected %s to echo the first rune without hold-first mode", runtime.GOOS)
	}
}

func TestReadInteractiveLine_ComposerNoHoldEchoesFirstRuneImmediately(t *testing.T) {
	readerR, readerW := io.Pipe()
	defer func() {
		_ = readerR.Close()
		_ = readerW.Close()
	}()

	output := &notifyingBuffer{
		notify: make(chan struct{}),
		match:  "x",
	}

	done := make(chan struct{})
	var (
		line string
		err  error
	)
	go func() {
		line, err = readInteractiveLineWithOptions(readerR, output, UserPromptText(0), nil, nil, true, false)
		close(done)
	}()

	if _, err := readerW.Write([]byte("x")); err != nil {
		t.Fatalf("write first rune: %v", err)
	}

	select {
	case <-output.notify:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for first rune to echo in no-hold composer")
	}

	if _, err := readerW.Write([]byte("\n")); err != nil {
		t.Fatalf("write newline: %v", err)
	}
	if err := readerW.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for no-hold composer to finish")
	}

	if err != nil {
		t.Fatalf("readInteractiveLineWithOptions: %v", err)
	}
	if line != "x" {
		t.Fatalf("expected no-hold composer line to submit x, got %q", line)
	}
}

func TestReadInteractiveLine_ComposerNoHoldKeepsLongPlainPasteVisible(t *testing.T) {
	readerR, readerW := io.Pipe()
	defer func() {
		_ = readerR.Close()
		_ = readerW.Close()
	}()

	pasted := "plain paste text that should stay visible"
	output := &notifyingBuffer{
		notify: make(chan struct{}),
		match:  pasted,
	}

	done := make(chan struct{})
	var (
		line string
		err  error
	)
	go func() {
		line, err = readInteractiveLineWithOptions(readerR, output, UserPromptText(0), nil, nil, true, false)
		close(done)
	}()

	if _, err := readerW.Write([]byte(pasted)); err != nil {
		t.Fatalf("write plain paste: %v", err)
	}

	select {
	case <-output.notify:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("timed out waiting for no-hold composer to render full plain paste; output=%q", output.String())
	}

	if _, err := readerW.Write([]byte("\n")); err != nil {
		t.Fatalf("write newline: %v", err)
	}
	if err := readerW.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for no-hold composer to finish")
	}

	if err != nil {
		t.Fatalf("readInteractiveLineWithOptions: %v", err)
	}
	if line != pasted {
		t.Fatalf("expected no-hold composer line to submit pasted text, got %q", line)
	}
}

func TestReadInteractiveLine_ComposerNoHoldReportsPlainPasteWindow(t *testing.T) {
	readerR, readerW := io.Pipe()
	defer func() {
		_ = readerR.Close()
		_ = readerW.Close()
	}()

	oldInterval := pasteBurstCharInterval
	oldIdle := pasteBurstActiveIdleTimeout
	pasteBurstCharInterval = 50 * time.Millisecond
	pasteBurstActiveIdleTimeout = 40 * time.Millisecond
	t.Cleanup(func() {
		pasteBurstCharInterval = oldInterval
		pasteBurstActiveIdleTimeout = oldIdle
	})

	var mu sync.Mutex
	var snapshots []LineEditorSnapshot
	done := make(chan struct{})
	var (
		line string
		err  error
	)
	go func() {
		line, err = readInteractiveLineWithHooks(
			readerR,
			io.Discard,
			UserPromptText(0),
			nil,
			nil,
			&LineEditorHooks{
				OnChange: func(snapshot LineEditorSnapshot) {
					mu.Lock()
					snapshots = append(snapshots, snapshot)
					mu.Unlock()
				},
			},
			true,
			false,
		)
		close(done)
	}()

	pasted := "/model --provider=openai"
	if _, err := readerW.Write([]byte(pasted)); err != nil {
		t.Fatalf("write plain paste: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	if _, err := readerW.Write([]byte("\n")); err != nil {
		t.Fatalf("write newline: %v", err)
	}
	if err := readerW.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("timed out waiting for no-hold composer to finish")
	}
	if err != nil {
		t.Fatalf("readInteractiveLineWithHooks: %v", err)
	}
	if line != pasted {
		t.Fatalf("expected no-hold composer line to submit pasted text, got %q", line)
	}

	mu.Lock()
	defer mu.Unlock()
	sawActive := false
	sawInactive := false
	for _, snapshot := range snapshots {
		if snapshot.Text != pasted {
			continue
		}
		if snapshot.PasteActive {
			sawActive = true
		} else if sawActive {
			sawInactive = true
		}
	}
	if !sawActive {
		t.Fatalf("expected rapid plain paste to publish paste-active snapshot, got %#v", snapshots)
	}
	if !sawInactive {
		t.Fatalf("expected rapid plain paste to publish paste-inactive snapshot, got %#v", snapshots)
	}
}

func TestReadInteractiveLine_TransientNoHoldEchoesFirstRuneImmediately(t *testing.T) {
	readerR, readerW := io.Pipe()
	defer func() {
		_ = readerR.Close()
		_ = readerW.Close()
	}()

	output := &notifyingBuffer{
		notify: make(chan struct{}),
	}

	done := make(chan struct{})
	var (
		line string
		err  error
	)
	go func() {
		line, err = readInteractiveLineWithOptions(readerR, output, UserPromptText(0), nil, nil, false, false)
		close(done)
	}()

	if _, err := readerW.Write([]byte("1")); err != nil {
		t.Fatalf("write first rune: %v", err)
	}

	select {
	case <-output.notify:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for first rune to echo in transient prompt")
	}

	if got := output.String(); !strings.Contains(got, "1") {
		t.Fatalf("expected transient prompt to echo first rune immediately, got %q", got)
	}

	if _, err := readerW.Write([]byte("\n")); err != nil {
		t.Fatalf("write newline: %v", err)
	}
	if err := readerW.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for transient prompt to finish")
	}

	if err != nil {
		t.Fatalf("readInteractiveLineWithOptions: %v", err)
	}
	if line != "1" {
		t.Fatalf("expected transient prompt line to submit 1, got %q", line)
	}
}

func TestReadInteractiveLine_RedrawDoesNotClearToScreenEnd(t *testing.T) {
	var output bytes.Buffer
	_, err := readInteractiveLine(
		strings.NewReader("first\nsecond\x1b[D\x1b[C\n"),
		&output,
		UserPromptText(0),
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine: %v", err)
	}
	if strings.Contains(output.String(), clearToEndSequence) {
		t.Fatalf("expected redraw to avoid clear-to-screen-end, got %q", output.String())
	}
}

func TestReadInteractiveLine_PreservesNonBracketedPasteNewlineWhenMoreInputIsPending(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("first\nsecond\x1b[D\x1b[C\n"),
		&output,
		UserPromptText(0),
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

func TestReadInteractiveLine_NoHoldPreservesNonBracketedPasteNewlineWhenMoreInputIsPending(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLineWithOptions(
		strings.NewReader("first\nsecond\n"),
		&output,
		UserPromptText(0),
		nil,
		nil,
		true,
		false,
	)
	if err != nil {
		t.Fatalf("readInteractiveLineWithOptions: %v", err)
	}
	if line != "first\nsecond" {
		t.Fatalf("expected no-hold non-bracketed pasted newline to stay intact, got %q", line)
	}
}

func TestReadInteractiveLine_NoHoldTreatsCRLFSubmitAsSubmit(t *testing.T) {
	var output bytes.Buffer
	line, err := readInteractiveLineWithOptions(
		&chunkedReader{chunks: [][]byte{[]byte("hello\r"), []byte("\n")}},
		&output,
		UserPromptText(0),
		nil,
		nil,
		true,
		false,
	)
	if err != nil {
		t.Fatalf("readInteractiveLineWithOptions: %v", err)
	}
	if line != "hello" {
		t.Fatalf("expected CRLF submit to return typed line, got %q", line)
	}
	if strings.Count(output.String(), "\r\n") != 1 {
		t.Fatalf("expected exactly one submit newline echo, got %q", output.String())
	}
}

func TestReadInteractiveLine_CtrlCOnEmptyLineRequestsExit(t *testing.T) {
	var output bytes.Buffer
	_, err := readInteractiveLine(
		strings.NewReader("\x03"),
		&output,
		UserPromptText(0),
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
		UserPromptText(0),
		nil,
		nil,
	)
	if !errors.Is(err, ErrInteractiveInputInterrupted) {
		t.Fatalf("expected interrupted error, got %v", err)
	}
}

func TestReadInteractiveLine_RestoresCursorBeforeSubmitEcho(t *testing.T) {
	var output bytes.Buffer
	restoreCalls := 0
	line, err := readInteractiveLineWithHooks(
		strings.NewReader("x\n"),
		&output,
		UserPromptText(0),
		nil,
		nil,
		&LineEditorHooks{
			OnBeforeRedraw: func(LineEditorSnapshot, LineEditorRenderSnapshot) {
				restoreCalls++
			},
		},
		true,
		false,
	)
	if err != nil {
		t.Fatalf("readInteractiveLineWithHooks: %v", err)
	}
	if line != "x" {
		t.Fatalf("expected submitted line x, got %q", line)
	}
	if restoreCalls < 2 {
		t.Fatalf("expected cursor restore before redraw and submit echo, got %d", restoreCalls)
	}
}

func TestReadInteractiveLine_CtrlDOnEmptyLineRequestsExit(t *testing.T) {
	var output bytes.Buffer
	_, err := readInteractiveLine(
		strings.NewReader("\x04"),
		&output,
		UserPromptText(0),
		nil,
		nil,
	)
	if !errors.Is(err, ErrInteractiveInputExitRequested) {
		t.Fatalf("expected exit request error, got %v", err)
	}
}

type notifyingBuffer struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	notify chan struct{}
	match  string
	sent   bool
}

func (b *notifyingBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	match := b.match
	if match == "" {
		match = "1"
	}
	if !b.sent && strings.Contains(b.buf.String(), match) {
		b.sent = true
		close(b.notify)
	}
	return n, err
}

func (b *notifyingBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestReadTransientLine_DoesNotAddToHistory(t *testing.T) {
	oldStdin := os.Stdin
	oldStdout := os.Stdout
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe stdin: %v", err)
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		t.Fatalf("os.Pipe stdout: %v", err)
	}
	defer func() {
		os.Stdin = oldStdin
		os.Stdout = oldStdout
		_ = stdinRead.Close()
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
	}()

	os.Stdin = stdinRead
	os.Stdout = stdoutWrite
	if _, err := stdinWrite.WriteString("choice\n"); err != nil {
		t.Fatalf("write transient stdin: %v", err)
	}
	if err := stdinWrite.Close(); err != nil {
		t.Fatalf("close transient stdin writer: %v", err)
	}

	ib := NewInputBox(nil)
	ib.AddToHistory("keep")

	line, err := ib.ReadTransientLine(nil)
	if err != nil {
		t.Fatalf("ReadTransientLine: %v", err)
	}
	if line != "choice" {
		t.Fatalf("expected transient line choice, got %q", line)
	}
	if got := ib.GetHistorySize(); got != 1 {
		t.Fatalf("expected transient read not to add history, got size %d", got)
	}
	if stored, ok := ib.GetHistoryAt(0); !ok || stored != "keep" {
		t.Fatalf("expected existing history to remain unchanged, got %q ok=%v", stored, ok)
	}
}

func TestReadTransientSecretPrompt_DoesNotAddToHistory(t *testing.T) {
	oldStdin := os.Stdin
	oldStdout := os.Stdout
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe stdin: %v", err)
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		t.Fatalf("os.Pipe stdout: %v", err)
	}
	defer func() {
		os.Stdin = oldStdin
		os.Stdout = oldStdout
		_ = stdinRead.Close()
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
	}()

	os.Stdin = stdinRead
	os.Stdout = stdoutWrite
	if _, err := stdinWrite.WriteString("secret-value\n"); err != nil {
		t.Fatalf("write secret stdin: %v", err)
	}
	if err := stdinWrite.Close(); err != nil {
		t.Fatalf("close secret stdin writer: %v", err)
	}

	ib := NewInputBox(nil)
	ib.AddToHistory("keep")

	line, err := ib.ReadTransientSecretPrompt("API key: ")
	if err != nil {
		t.Fatalf("ReadTransientSecretPrompt: %v", err)
	}
	if line != "secret-value" {
		t.Fatalf("expected secret value, got %q", line)
	}
	if got := ib.GetHistorySize(); got != 1 {
		t.Fatalf("expected secret read not to add history, got size %d", got)
	}
	if stored, ok := ib.GetHistoryAt(0); !ok || stored != "keep" {
		t.Fatalf("expected existing history to remain unchanged, got %q ok=%v", stored, ok)
	}
}
