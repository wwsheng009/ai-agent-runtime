package ui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"
)

var ErrInteractiveInputInterrupted = errors.New("interactive input interrupted")
var ErrInteractiveInputExitRequested = errors.New("interactive input exit requested")

const (
	bracketedPasteEnableSequence  = "\x1b[?2004h"
	bracketedPasteDisableSequence = "\x1b[?2004l"
	cursorSaveSequence            = "\x1b[s"
	cursorRestoreSequence         = "\x1b[u"
	clearToEndSequence            = "\x1b[J"
	escapeSequenceWait            = 30 * time.Millisecond
)

func defaultPasteBurstHoldFirstRune() bool {
	// Unix PTY / WSL 下 poll readiness 对 raw stdin 的行为更容易受终端
	// 与多路复用器影响。首字符不能依赖异步 flush，否则会表现为输入被
	// 缓存但不回显；Windows console 继续使用 hold-first 以保持现有
	// paste burst 聚合路径。
	return runtime.GOOS == "windows"
}

type editorKeyKind int

const (
	editorKeyIgnore editorKeyKind = iota
	editorKeyRune
	editorKeyEnter
	editorKeyComplete
	editorKeyCancelPopup
	editorKeyBackspace
	editorKeyDelete
	editorKeyLeft
	editorKeyRight
	editorKeyUp
	editorKeyDown
	editorKeyHome
	editorKeyEnd
	editorKeyClearLine
	editorKeyDeleteWord
	editorKeyKillToEnd
	editorKeyDeleteForwardWord
	editorKeyRedraw
	editorKeyYank
	editorKeyTranspose
	editorKeyBackwardWord
	editorKeyForwardWord
	editorKeyReverseSearch
	editorKeyAbortSearch
	editorKeyPasteStart
	editorKeyPasteEnd
	editorKeyInterrupt
	editorKeyEOF
)

type editorKey struct {
	kind editorKeyKind
	r    rune
}

// ReadWithHistoryPrompt reads a single line using a local line editor.
//
// The caller is expected to have already rendered the prompt when running in
// interactive chat mode. This method therefore only redraws the active line and
// keeps the history state on the InputBox.
func (ib *InputBox) ReadWithHistoryPrompt(prompt string, onChange func(string)) (string, error) {
	return ib.readPrompt(prompt, onChange, true, true, true, defaultPasteBurstHoldFirstRune())
}

// ReadTransientPrompt reads a single line with the same editing surface as
// ReadWithHistoryPrompt, but it does not add the submitted text to history,
// suppresses the final submit newline echo, and keeps the first character
// visible immediately for modal prompts.
func (ib *InputBox) ReadTransientPrompt(prompt string, onChange func(string)) (string, error) {
	return ib.readPrompt(prompt, onChange, false, false, true, false)
}

// ReadTransientSecretPrompt reads a secret value for modal prompts. Interactive
// terminals use the platform password reader so the submitted text is not
// echoed or added to history; non-interactive input stays line-buffered for
// tests and piped usage.
func (ib *InputBox) ReadTransientSecretPrompt(prompt string) (string, error) {
	if ib == nil {
		return "", io.EOF
	}
	ib.historyPos = len(ib.history)
	if prompt != "" {
		fmt.Fprint(os.Stdout, prompt)
	}
	if !IsInteractiveTerminal() {
		return readBufferedLine(os.Stdin)
	}
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stdout)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// ReadTransientLine reads a transient response without a visible prompt label.
// This is used by modal questions that already printed their own prompt text.
func (ib *InputBox) ReadTransientLine(onChange func(string)) (string, error) {
	return ib.readPrompt("", onChange, false, false, false, false)
}

func (ib *InputBox) readPrompt(prompt string, onChange func(string), keepHistory bool, echoSubmit bool, useDefaultPrompt bool, holdFirstRune bool) (string, error) {
	if ib == nil {
		return "", io.EOF
	}

	if prompt == "" && useDefaultPrompt {
		prompt = ib.GetPrompt()
	}

	// Keep history navigation stable even if the read is cancelled.
	ib.historyPos = len(ib.history)

	if onChange != nil {
		onChange("")
	}

	// Non-interactive terminals keep line-buffered behavior.
	if !IsInteractiveTerminal() {
		line, err := readBufferedLine(os.Stdin)
		if err == nil && keepHistory && strings.TrimSpace(line) != "" {
			ib.AddToHistory(line)
		}
		if onChange != nil {
			onChange("")
		}
		return line, err
	}

	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		line, readErr := readBufferedLine(os.Stdin)
		if readErr == nil && strings.TrimSpace(line) != "" {
			ib.AddToHistory(line)
		}
		if onChange != nil {
			onChange("")
		}
		return line, readErr
	}
	defer func() {
		fmt.Fprint(os.Stdout, bracketedPasteDisableSequence)
		_ = term.Restore(fd, state)
	}()
	// 启用 bracketed paste 后，终端会给粘贴块加上明确边界，
	// 这样我们就能把块内换行当作文本而不是 Enter。
	fmt.Fprint(os.Stdout, bracketedPasteEnableSequence)
	// 提示符已经由调用方渲染到屏幕上了。
	// 这里保存当前光标位置，后续重绘只刷新输入区，避免折行后重复残留。
	fmt.Fprint(os.Stdout, cursorSaveSequence)

	line, readErr := readInteractiveLineWithOptions(os.Stdin, os.Stdout, prompt, ib.history, onChange, echoSubmit, holdFirstRune)
	if readErr == nil && keepHistory && strings.TrimSpace(line) != "" {
		ib.AddToHistory(line)
	}
	if onChange != nil {
		onChange("")
	}
	return line, readErr
}

func (ib *InputBox) readPromptWithHooks(prompt string, hooks LineEditorHooks, keepHistory bool, echoSubmit bool, useDefaultPrompt bool, holdFirstRune bool) (string, error) {
	if ib == nil {
		return "", io.EOF
	}

	if prompt == "" && useDefaultPrompt {
		prompt = ib.GetPrompt()
	}

	// Keep history navigation stable even if the read is cancelled.
	ib.historyPos = len(ib.history)

	if !IsInteractiveTerminal() {
		line, err := readBufferedLine(os.Stdin)
		if err == nil && keepHistory && strings.TrimSpace(line) != "" {
			ib.AddToHistory(line)
		}
		return line, err
	}

	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		line, readErr := readBufferedLine(os.Stdin)
		if readErr == nil && keepHistory && strings.TrimSpace(line) != "" {
			ib.AddToHistory(line)
		}
		return line, readErr
	}
	defer func() {
		fmt.Fprint(os.Stdout, bracketedPasteDisableSequence)
		_ = term.Restore(fd, state)
	}()
	fmt.Fprint(os.Stdout, bracketedPasteEnableSequence)
	fmt.Fprint(os.Stdout, cursorSaveSequence)

	line, readErr := readInteractiveLineWithHooks(os.Stdin, os.Stdout, prompt, ib.history, nil, &hooks, echoSubmit, holdFirstRune)
	if readErr == nil && keepHistory && strings.TrimSpace(line) != "" {
		ib.AddToHistory(line)
	}
	return line, readErr
}

func readBufferedLine(reader io.Reader) (string, error) {
	if reader == nil {
		return "", io.EOF
	}
	bufReader := bufio.NewReader(reader)
	line, err := bufReader.ReadString('\n')
	if line != "" {
		return strings.TrimRight(line, "\r\n"), nil
	}
	if err != nil {
		return "", err
	}
	return "", nil
}

func readInteractiveLine(reader io.Reader, writer io.Writer, prompt string, history []string, onChange func(string)) (string, error) {
	return readInteractiveLineWithOptions(reader, writer, prompt, history, onChange, true, defaultPasteBurstHoldFirstRune())
}

func readInteractiveLineWithOptions(reader io.Reader, writer io.Writer, prompt string, history []string, onChange func(string), echoSubmit bool, holdFirstRune bool) (string, error) {
	return readInteractiveLineWithHooks(reader, writer, prompt, history, onChange, nil, echoSubmit, holdFirstRune)
}

func readInteractiveLineWithHooks(reader io.Reader, writer io.Writer, prompt string, history []string, onChange func(string), hooks *LineEditorHooks, echoSubmit bool, holdFirstRune bool) (string, error) {
	if reader == nil {
		return "", io.EOF
	}
	if writer == nil {
		writer = io.Discard
	}
	_ = prompt

	line := make([]rune, 0, 64)
	composer := NewComposerState()
	cursor := 0
	historyPos := len(history)
	var draft []rune
	var promoteDraft func()
	// Bracketed paste arrives as a bounded burst; keep it in-memory until the
	// closing marker so the terminal only redraws once for the whole paste.
	var pasteBuffer []rune
	// Plain input that arrives in a rapid burst is classified by the same
	// PasteBurst state machine used by the higher-level composer.
	var pasteBurst PasteBurst
	var yankBuffer []rune
	var reverseSearchQuery []rune
	var reverseSearchOriginal []rune
	reverseSearchStart := len(history)
	reverseSearchActive := false
	pending := make([]byte, 0, 16)
	pasteActive := false
	stdinFile, _ := reader.(*os.File)
	lastRenderedRows := 1
	var redraw func()
	snapshot := func() LineEditorSnapshot {
		return LineEditorSnapshot{
			Text:        string(line),
			Cursor:      cursor,
			Prompt:      prompt,
			HistoryPos:  historyPos,
			PasteActive: pasteActive,
		}
	}
	emitChange := func() {
		if onChange != nil {
			onChange(string(line))
		}
		if hooks != nil && hooks.OnChange != nil {
			hooks.OnChange(snapshot())
		}
	}
	applyReplacement := func(repl LineEditorReplacement) {
		promoteDraft()
		line = []rune(repl.Text)
		cursor = repl.Cursor
		if cursor < 0 {
			cursor = len(line)
		}
		if cursor > len(line) {
			cursor = len(line)
		}
		emitChange()
		redraw()
	}

	if onChange != nil {
		onChange("")
	}
	if hooks != nil && hooks.OnChange != nil {
		hooks.OnChange(snapshot())
	}

	// 调用方会在提示符渲染完成后保存输入锚点。
	// 这里每次都回到锚点，只重绘可编辑输入区，避免折行残留。
	redraw = func() {
		termWidth := GetTerminalWidth()
		if termWidth <= 0 {
			termWidth = 80
		}
		promptWidth := terminalVisibleWidth(prompt)
		renderedRows := interactiveInputDisplayRows(line, promptWidth, termWidth)
		clearRows := renderedRows
		if lastRenderedRows > clearRows {
			clearRows = lastRenderedRows
		}
		var builder strings.Builder
		builder.Grow(len(line)*4 + 24)
		builder.WriteString(cursorRestoreSequence)
		appendClearInteractiveInputRows(&builder, clearRows)
		builder.WriteString(renderInteractiveInputForTerminal(line))
		if cursor < len(line) {
			endPos := interactiveInputVisualPosition(line, len(line), promptWidth, termWidth)
			cursorPos := interactiveInputVisualPosition(line, cursor, promptWidth, termWidth)
			if rowsUp := endPos.row - cursorPos.row; rowsUp > 0 {
				fmt.Fprintf(&builder, "\x1b[%dA", rowsUp)
			}
			builder.WriteByte('\r')
			if cursorPos.col > 0 {
				fmt.Fprintf(&builder, "\x1b[%dC", cursorPos.col)
			}
		}
		lastRenderedRows = renderedRows
		fmt.Fprint(writer, builder.String())
	}

	setLine := func(next []rune) {
		line = append(line[:0], next...)
		cursor = len(line)
		emitChange()
		redraw()
	}

	insertRunes := func(chars []rune) {
		if len(chars) == 0 {
			return
		}
		reverseSearchActive = false
		reverseSearchQuery = reverseSearchQuery[:0]
		reverseSearchStart = len(history)
		promoteDraft()
		line = append(line[:cursor], append(chars, line[cursor:]...)...)
		cursor += len(chars)
		emitChange()
		redraw()
	}

	insertPastedText := func(text string) {
		text = NormalizePastedText(text)
		if text == "" {
			return
		}
		reverseSearchActive = false
		reverseSearchQuery = reverseSearchQuery[:0]
		reverseSearchStart = len(history)
		promoteDraft()
		composer.SetText(string(line))
		cursor = composer.HandlePasteAt(cursor, text)
		line = []rune(composer.Text())
		emitChange()
		redraw()
	}

	insertTypedRune := func(ch rune) {
		if ch == 0 {
			return
		}
		reverseSearchActive = false
		reverseSearchQuery = reverseSearchQuery[:0]
		reverseSearchStart = len(history)
		promoteDraft()
		line = append(line[:cursor], append([]rune{ch}, line[cursor:]...)...)
		cursor++
		emitChange()
		redraw()
	}

	submittedText := func() string {
		composer.SetText(string(line))
		return strings.TrimRight(composer.SubmitText(), "\r\n")
	}

	storeKill := func(chars []rune) {
		if len(chars) == 0 {
			return
		}
		yankBuffer = append(yankBuffer[:0], chars...)
	}

	yankKilledText := func() {
		if len(yankBuffer) == 0 {
			return
		}
		insertRunes(append([]rune(nil), yankBuffer...))
	}

	transposeChars := func() {
		if len(line) < 2 {
			return
		}
		promoteDraft()
		if cursor == 0 {
			return
		}
		if cursor >= len(line) {
			line[len(line)-2], line[len(line)-1] = line[len(line)-1], line[len(line)-2]
			cursor = len(line)
			emitChange()
			redraw()
			return
		}
		line[cursor-1], line[cursor] = line[cursor], line[cursor-1]
		cursor++
		emitChange()
		redraw()
	}

	moveBackwardWord := func() {
		if cursor <= 0 || len(line) == 0 {
			return
		}
		promoteDraft()
		nextCursor := previousWordStart(line, cursor)
		if nextCursor == cursor {
			return
		}
		cursor = nextCursor
		emitChange()
		redraw()
	}

	moveForwardWord := func() {
		if cursor >= len(line) || len(line) == 0 {
			return
		}
		promoteDraft()
		nextCursor := nextWordEnd(line, cursor)
		if nextCursor == cursor {
			return
		}
		cursor = nextCursor
		emitChange()
		redraw()
	}

	deleteForwardWord := func() {
		if cursor >= len(line) || len(line) == 0 {
			return
		}
		start := cursor
		end := nextWordEnd(line, cursor)
		if end <= start {
			return
		}
		promoteDraft()
		storeKill(append([]rune(nil), line[start:end]...))
		line = append(line[:start], line[end:]...)
		emitChange()
		redraw()
	}

	clearReverseSearchState := func() {
		reverseSearchActive = false
		reverseSearchQuery = reverseSearchQuery[:0]
		reverseSearchOriginal = reverseSearchOriginal[:0]
		reverseSearchStart = len(history)
	}

	beginReverseSearch := func() {
		if !reverseSearchActive {
			reverseSearchOriginal = append(reverseSearchOriginal[:0], line...)
			reverseSearchQuery = append(reverseSearchQuery[:0], line...)
			reverseSearchStart = len(history)
			reverseSearchActive = true
		}
		if len(history) == 0 {
			return
		}
		idx, match, ok := findReverseHistoryMatch(history, string(reverseSearchQuery), reverseSearchStart)
		if !ok {
			return
		}
		reverseSearchStart = idx
		setLine([]rune(match))
	}

	abortReverseSearch := func() {
		if !reverseSearchActive {
			return
		}
		setLine(append([]rune(nil), reverseSearchOriginal...))
		clearReverseSearchState()
	}

	flushPasteBurst := func() {
		switch result := pasteBurst.FlushIfDue(time.Now()); result.Kind {
		case FlushResultPaste:
			insertPastedText(result.Text)
		case FlushResultTyped:
			insertTypedRune(result.Ch)
		}
	}

	flushPasteBurstBeforeModifiedInput := func() {
		if pasted := pasteBurst.FlushBeforeModifiedInput(); pasted != "" {
			insertPastedText(pasted)
		}
	}

	waitForPasteBurstWindow := func() error {
		if !pasteBurst.IsActive() || stdinFile == nil || len(pending) > 0 {
			return nil
		}
		timeout := time.Until(pasteBurst.Deadline())
		if timeout <= 0 {
			flushPasteBurst()
			return nil
		}
		ready, err := waitForInteractiveInputReady(int(stdinFile.Fd()), timeout)
		if err != nil {
			return err
		}
		if !ready {
			flushPasteBurst()
		}
		return nil
	}

	pasteBurstEnterShouldInsertNewline := func() (bool, error) {
		if !pasteBurst.IsActive() {
			return false, nil
		}
		if pasteBurst.ContainsNewline() || len(pending) > 0 {
			return true, nil
		}
		if stdinFile == nil {
			return false, nil
		}
		timeout := time.Until(pasteBurst.Deadline())
		if timeout <= 0 {
			return false, nil
		}
		return waitForInteractiveInputReady(int(stdinFile.Fd()), timeout)
	}

	plainPasteEnterShouldInsertNewline := func() (bool, error) {
		if holdFirstRune {
			return false, nil
		}
		if len(pending) > 0 {
			return true, nil
		}
		if stdinFile == nil {
			return false, nil
		}
		timeout := time.Until(pasteBurst.PlainContinuationDeadline())
		if timeout <= 0 {
			return false, nil
		}
		return waitForInteractiveInputReady(int(stdinFile.Fd()), timeout)
	}

	promoteDraft = func() {
		if historyPos == len(history) {
			return
		}
		draft = append(draft[:0], line...)
	}

	for {
		if err := waitForPasteBurstWindow(); err != nil {
			return "", err
		}
		key, ok, readErr := nextInteractiveKey(reader, &pending, stdinFile)
		if readErr != nil {
			flushPasteBurstBeforeModifiedInput()
			return "", readErr
		}
		if !ok || key.kind == editorKeyIgnore {
			continue
		}
		if key.kind == editorKeyPasteStart {
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			pasteActive = true
			pasteBuffer = pasteBuffer[:0]
			continue
		}
		if key.kind == editorKeyPasteEnd {
			if pasteActive && len(pasteBuffer) > 0 {
				insertPastedText(string(pasteBuffer))
				pasteBuffer = pasteBuffer[:0]
			}
			pasteActive = false
			continue
		}
		if pasteActive && key.kind == editorKeyEnter {
			// 粘贴块里的换行是文本内容，不应触发提交。
			key.kind = editorKeyRune
			key.r = '\n'
		}
		if pasteActive {
			switch key.kind {
			case editorKeyRune:
				pasteBuffer = append(pasteBuffer, key.r)
			case editorKeyComplete:
				pasteBuffer = append(pasteBuffer, '\t')
			case editorKeyEnter:
				pasteBuffer = append(pasteBuffer, '\n')
			case editorKeyInterrupt:
				fmt.Fprint(writer, "\r\n")
				if onChange != nil {
					onChange("")
				}
				return "", ErrInteractiveInputInterrupted
			case editorKeyEOF:
				if len(pasteBuffer) > 0 {
					insertPastedText(string(pasteBuffer))
					pasteBuffer = pasteBuffer[:0]
				}
				if len(line) == 0 {
					if onChange != nil {
						onChange("")
					}
					return "", io.EOF
				}
			default:
				continue
			}
			continue
		}

		switch key.kind {
		case editorKeyRune:
			clearReverseSearchState()
			if unicode.IsControl(key.r) && key.r != '\n' {
				continue
			}
			now := time.Now()
			if key.r == '\n' {
				insertTypedRune('\n')
				pasteBurst.ClearWindowAfterNonChar()
				continue
			}
			var decision CharDecision
			if holdFirstRune {
				decision = pasteBurst.OnPlainChar(key.r, now)
			} else {
				decision = pasteBurst.OnPlainCharNoHold(now)
			}
			switch decision.Kind {
			case CharDecisionBufferAppend:
				pasteBurst.AppendCharToBuffer(key.r, now)
				continue
			case CharDecisionBeginBuffer:
				safeCursor := cursor
				if safeCursor < 0 {
					safeCursor = 0
				}
				if safeCursor > len(line) {
					safeCursor = len(line)
				}
				before := string(line[:safeCursor])
				if grab := pasteBurst.DecideBeginBuffer(now, before, decision.RetroChars); grab != nil {
					if grab.StartByte < safeCursor {
						line = append(line[:grab.StartByte], line[safeCursor:]...)
						cursor = grab.StartByte
						emitChange()
						redraw()
					}
					pasteBurst.AppendCharToBuffer(key.r, now)
					continue
				}
				insertTypedRune(key.r)
				continue
			case CharDecisionBeginBufferFromPending:
				pasteBurst.AppendCharToBuffer(key.r, now)
				continue
			case CharDecisionRetainFirstChar:
				continue
			}
			insertTypedRune(key.r)
		case editorKeyComplete:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			if hooks != nil && hooks.OnComplete != nil {
				if repl, ok := hooks.OnComplete(snapshot()); ok {
					applyReplacement(repl)
					continue
				}
			}
		case editorKeyCancelPopup:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			if hooks != nil && hooks.OnCancelPopup != nil && hooks.OnCancelPopup(snapshot()) {
				continue
			}
		case editorKeyEnter:
			if pasteBurst.IsActive() {
				// If more bytes are already queued, this Enter belongs to a
				// non-bracketed paste and must stay in the editable text.
				insertNewline, err := pasteBurstEnterShouldInsertNewline()
				if err != nil {
					return "", err
				}
				if insertNewline {
					now := time.Now()
					if pasteBurst.HasPendingFirstChar() && !pasteBurst.HasBufferedText() {
						if pasteBurst.BeginBufferFromPending(now) {
							pasteBurst.AppendCharToBuffer('\n', now)
							continue
						}
					}
					if pasteBurst.HasBufferedText() {
						pasteBurst.AppendCharToBuffer('\n', now)
						continue
					}
					insertTypedRune('\n')
					pasteBurst.ClearWindowAfterNonChar()
					continue
				}
				flushPasteBurstBeforeModifiedInput()
			}
			insertPlainPasteNewline, err := plainPasteEnterShouldInsertNewline()
			if err != nil {
				return "", err
			}
			if insertPlainPasteNewline {
				now := time.Now()
				pasteBurst.ClearWindowAfterNonChar()
				insertTypedRune('\n')
				pasteBurst.ExtendWindow(now)
				continue
			}
			if hooks != nil && hooks.OnSubmit != nil {
				if repl, ok := hooks.OnSubmit(snapshot()); ok {
					applyReplacement(repl)
					continue
				}
			}
			if echoSubmit {
				fmt.Fprint(writer, "\r\n")
			}
			if onChange != nil {
				onChange("")
			}
			return submittedText(), nil
		case editorKeyBackspace:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			if cursor <= 0 {
				continue
			}
			promoteDraft()
			line = append(line[:cursor-1], line[cursor:]...)
			cursor--
			emitChange()
			redraw()
		case editorKeyDelete:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			if cursor >= len(line) {
				continue
			}
			promoteDraft()
			line = append(line[:cursor], line[cursor+1:]...)
			emitChange()
			redraw()
		case editorKeyLeft:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			if cursor > 0 {
				cursor--
				emitChange()
				redraw()
			}
		case editorKeyRight:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			if cursor < len(line) {
				cursor++
				emitChange()
				redraw()
			}
		case editorKeyHome:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			if cursor != 0 {
				cursor = 0
				emitChange()
				redraw()
			}
		case editorKeyEnd:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			if cursor != len(line) {
				cursor = len(line)
				emitChange()
				redraw()
			}
		case editorKeyUp:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			if hooks != nil && hooks.OnNavigate != nil && hooks.OnNavigate(snapshot(), -1) {
				continue
			}
			if len(history) == 0 {
				continue
			}
			if historyPos == len(history) {
				draft = append(draft[:0], line...)
			}
			if historyPos > 0 {
				historyPos--
				setLine([]rune(history[historyPos]))
			}
		case editorKeyDown:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			if hooks != nil && hooks.OnNavigate != nil && hooks.OnNavigate(snapshot(), 1) {
				continue
			}
			if len(history) == 0 {
				continue
			}
			if historyPos < len(history)-1 {
				historyPos++
				setLine([]rune(history[historyPos]))
				continue
			}
			if historyPos == len(history)-1 {
				historyPos = len(history)
				if draft != nil {
					setLine(append([]rune(nil), draft...))
				} else {
					setLine(nil)
				}
			}
		case editorKeyClearLine:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			if len(line) == 0 {
				continue
			}
			promoteDraft()
			storeKill(append([]rune(nil), line...))
			line = line[:0]
			cursor = 0
			emitChange()
			redraw()
		case editorKeyDeleteWord:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			if cursor <= 0 || len(line) == 0 {
				continue
			}
			start := deletePreviousWordStart(line, cursor)
			if start >= cursor {
				continue
			}
			promoteDraft()
			storeKill(append([]rune(nil), line[start:cursor]...))
			line = append(line[:start], line[cursor:]...)
			cursor = start
			emitChange()
			redraw()
		case editorKeyKillToEnd:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			if cursor >= len(line) {
				continue
			}
			promoteDraft()
			storeKill(append([]rune(nil), line[cursor:]...))
			line = line[:cursor]
			emitChange()
			redraw()
		case editorKeyDeleteForwardWord:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			deleteForwardWord()
		case editorKeyRedraw:
			flushPasteBurstBeforeModifiedInput()
			redraw()
		case editorKeyYank:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			yankKilledText()
		case editorKeyTranspose:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			transposeChars()
		case editorKeyBackwardWord:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			moveBackwardWord()
		case editorKeyForwardWord:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			moveForwardWord()
		case editorKeyReverseSearch:
			flushPasteBurstBeforeModifiedInput()
			beginReverseSearch()
		case editorKeyAbortSearch:
			flushPasteBurstBeforeModifiedInput()
			abortReverseSearch()
		case editorKeyInterrupt:
			flushPasteBurstBeforeModifiedInput()
			hadTypedContent := len(line) > 0 || pasteBurst.IsActive()
			pasteBurst.ClearAfterExplicitPaste()
			if echoSubmit {
				fmt.Fprint(writer, "\r\n")
			}
			if onChange != nil {
				onChange("")
			}
			if !hadTypedContent {
				return "", ErrInteractiveInputExitRequested
			}
			return "", ErrInteractiveInputInterrupted
		case editorKeyEOF:
			flushPasteBurstBeforeModifiedInput()
			if len(line) == 0 {
				if onChange != nil {
					onChange("")
				}
				return "", ErrInteractiveInputExitRequested
			}
			if cursor >= len(line) {
				continue
			}
			promoteDraft()
			line = append(line[:cursor], line[cursor+1:]...)
			emitChange()
			redraw()
		}
	}
}

type interactiveInputPosition struct {
	row int
	col int
}

func renderInteractiveInputForTerminal(line []rune) string {
	if len(line) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(line))
	for _, r := range line {
		switch r {
		case '\r', '\n':
			// Raw mode disables terminal output post-processing on Unix, so a
			// bare '\n' moves down without returning to column 0.
			builder.WriteString("\r\n")
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func appendClearInteractiveInputRows(builder *strings.Builder, rows int) {
	if builder == nil {
		return
	}
	if rows < 1 {
		rows = 1
	}
	for i := 0; i < rows; i++ {
		builder.WriteString("\x1b[K")
		if i < rows-1 {
			builder.WriteString("\x1b[1B\r")
		}
	}
	builder.WriteString(cursorRestoreSequence)
}

func interactiveInputDisplayRows(line []rune, startCol, termWidth int) int {
	pos := interactiveInputVisualPosition(line, len(line), startCol, termWidth)
	return pos.row + 1
}

func interactiveInputVisualPosition(line []rune, cursor, startCol, termWidth int) interactiveInputPosition {
	if termWidth <= 0 {
		termWidth = 80
	}
	if startCol < 0 {
		startCol = 0
	}
	if startCol >= termWidth {
		startCol = startCol % termWidth
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(line) {
		cursor = len(line)
	}

	pos := interactiveInputPosition{col: startCol}
	for _, r := range line[:cursor] {
		switch r {
		case '\r', '\n':
			pos.row++
			pos.col = 0
			continue
		}
		width := DisplayWidth(string(r))
		if width <= 0 {
			continue
		}
		pos.col += width
		if pos.col >= termWidth {
			pos.row += pos.col / termWidth
			pos.col %= termWidth
		}
	}
	return pos
}

func terminalVisibleWidth(text string) int {
	return DisplayWidth(stripTerminalEscapeSequences(text))
}

func stripTerminalEscapeSequences(text string) string {
	if text == "" || !strings.ContainsRune(text, '\x1b') {
		return text
	}
	var builder strings.Builder
	builder.Grow(len(text))
	for i := 0; i < len(text); {
		if text[i] != '\x1b' {
			r, size := utf8.DecodeRuneInString(text[i:])
			builder.WriteRune(r)
			i += size
			continue
		}
		consumed := consumeTerminalEscapeSequence(text[i:])
		if consumed <= 0 {
			i++
			continue
		}
		i += consumed
	}
	return builder.String()
}

func consumeTerminalEscapeSequence(text string) int {
	if len(text) < 2 || text[0] != '\x1b' {
		return 0
	}
	switch text[1] {
	case '[':
		for i := 2; i < len(text); i++ {
			b := text[i]
			if b >= 0x40 && b <= 0x7e {
				return i + 1
			}
		}
	case ']':
		for i := 2; i < len(text); i++ {
			if text[i] == '\a' {
				return i + 1
			}
			if text[i] == '\x1b' && i+1 < len(text) && text[i+1] == '\\' {
				return i + 2
			}
		}
	default:
		return 2
	}
	return len(text)
}

func nextInteractiveKey(reader io.Reader, pending *[]byte, stdinFile *os.File) (editorKey, bool, error) {
	for {
		if decoded, ok := decodeInteractiveKey(*pending); ok {
			*pending = (*pending)[decoded.consumed:]
			return decoded.key, true, nil
		}
		if len(*pending) == 1 && (*pending)[0] == '\x1b' {
			if stdinFile == nil {
				*pending = (*pending)[:0]
				return editorKey{kind: editorKeyCancelPopup}, true, nil
			}
			ready, err := waitForInteractiveInputReady(int(stdinFile.Fd()), escapeSequenceWait)
			if err != nil {
				return editorKey{}, false, err
			}
			if !ready {
				*pending = (*pending)[:0]
				return editorKey{kind: editorKeyCancelPopup}, true, nil
			}
		}

		var buf [64]byte
		n, err := reader.Read(buf[:])
		if n > 0 {
			*pending = append(*pending, buf[:n]...)
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(*pending) == 0 {
					return editorKey{kind: editorKeyEOF}, true, nil
				}
				if len(*pending) == 1 && (*pending)[0] == '\x1b' {
					*pending = (*pending)[:0]
					return editorKey{kind: editorKeyCancelPopup}, true, nil
				}
				// Best-effort drain of any remaining bytes when the source ends.
				*pending = (*pending)[1:]
				continue
			}
			return editorKey{}, false, err
		}
	}
}

type decodedInteractiveKey struct {
	key      editorKey
	consumed int
}

func decodeInteractiveKey(pending []byte) (decodedInteractiveKey, bool) {
	if len(pending) == 0 {
		return decodedInteractiveKey{}, false
	}

	switch pending[0] {
	case '\r':
		if len(pending) >= 2 && pending[1] == '\n' {
			return decodedInteractiveKey{key: editorKey{kind: editorKeyEnter}, consumed: 2}, true
		}
		return decodedInteractiveKey{key: editorKey{kind: editorKeyEnter}, consumed: 1}, true
	case '\n':
		return decodedInteractiveKey{key: editorKey{kind: editorKeyEnter}, consumed: 1}, true
	case '\t':
		return decodedInteractiveKey{key: editorKey{kind: editorKeyComplete}, consumed: 1}, true
	case '\b', 127:
		return decodedInteractiveKey{key: editorKey{kind: editorKeyBackspace}, consumed: 1}, true
	case 1:
		return decodedInteractiveKey{key: editorKey{kind: editorKeyHome}, consumed: 1}, true
	case 2:
		return decodedInteractiveKey{key: editorKey{kind: editorKeyLeft}, consumed: 1}, true
	case 3:
		return decodedInteractiveKey{key: editorKey{kind: editorKeyInterrupt}, consumed: 1}, true
	case 4:
		return decodedInteractiveKey{key: editorKey{kind: editorKeyEOF}, consumed: 1}, true
	case 5:
		return decodedInteractiveKey{key: editorKey{kind: editorKeyEnd}, consumed: 1}, true
	case 6:
		return decodedInteractiveKey{key: editorKey{kind: editorKeyRight}, consumed: 1}, true
	case 14:
		return decodedInteractiveKey{key: editorKey{kind: editorKeyDown}, consumed: 1}, true
	case 16:
		return decodedInteractiveKey{key: editorKey{kind: editorKeyUp}, consumed: 1}, true
	case 18:
		return decodedInteractiveKey{key: editorKey{kind: editorKeyReverseSearch}, consumed: 1}, true
	case 20:
		return decodedInteractiveKey{key: editorKey{kind: editorKeyTranspose}, consumed: 1}, true
	case 11:
		return decodedInteractiveKey{key: editorKey{kind: editorKeyKillToEnd}, consumed: 1}, true
	case 23:
		return decodedInteractiveKey{key: editorKey{kind: editorKeyDeleteWord}, consumed: 1}, true
	case 25:
		return decodedInteractiveKey{key: editorKey{kind: editorKeyYank}, consumed: 1}, true
	case 12:
		return decodedInteractiveKey{key: editorKey{kind: editorKeyRedraw}, consumed: 1}, true
	case 21:
		return decodedInteractiveKey{key: editorKey{kind: editorKeyClearLine}, consumed: 1}, true
	case 7:
		return decodedInteractiveKey{key: editorKey{kind: editorKeyAbortSearch}, consumed: 1}, true
	case 27:
		return decodeEscapeInteractiveKey(pending)
	}

	if !utf8.FullRune(pending) {
		return decodedInteractiveKey{}, false
	}
	r, size := utf8.DecodeRune(pending)
	if r == utf8.RuneError && size == 1 {
		return decodedInteractiveKey{key: editorKey{kind: editorKeyIgnore}, consumed: 1}, true
	}
	return decodedInteractiveKey{key: editorKey{kind: editorKeyRune, r: r}, consumed: size}, true
}

func decodeEscapeInteractiveKey(pending []byte) (decodedInteractiveKey, bool) {
	if len(pending) < 2 {
		return decodedInteractiveKey{}, false
	}
	if pending[1] != '[' && pending[1] != 'O' {
		switch pending[1] {
		case 'b', 'B':
			return decodedInteractiveKey{key: editorKey{kind: editorKeyBackwardWord}, consumed: 2}, true
		case 'f', 'F':
			return decodedInteractiveKey{key: editorKey{kind: editorKeyForwardWord}, consumed: 2}, true
		case 'd', 'D':
			return decodedInteractiveKey{key: editorKey{kind: editorKeyDeleteForwardWord}, consumed: 2}, true
		case '\b', 127:
			return decodedInteractiveKey{key: editorKey{kind: editorKeyDeleteWord}, consumed: 2}, true
		}
		// Bare ESC or an unhandled alt-modified key. Drop the ESC and keep processing.
		return decodedInteractiveKey{key: editorKey{kind: editorKeyIgnore}, consumed: 1}, true
	}

	for i := 2; i < len(pending); i++ {
		b := pending[i]
		if !isEscapeFinalByte(b) {
			continue
		}
		switch pending[1] {
		case '[':
			switch b {
			case 'A':
				return decodedInteractiveKey{key: editorKey{kind: editorKeyUp}, consumed: i + 1}, true
			case 'B':
				return decodedInteractiveKey{key: editorKey{kind: editorKeyDown}, consumed: i + 1}, true
			case 'C':
				if isWordMovementModifierSequence(pending[2:i]) {
					return decodedInteractiveKey{key: editorKey{kind: editorKeyForwardWord}, consumed: i + 1}, true
				}
				return decodedInteractiveKey{key: editorKey{kind: editorKeyRight}, consumed: i + 1}, true
			case 'D':
				if isWordMovementModifierSequence(pending[2:i]) {
					return decodedInteractiveKey{key: editorKey{kind: editorKeyBackwardWord}, consumed: i + 1}, true
				}
				return decodedInteractiveKey{key: editorKey{kind: editorKeyLeft}, consumed: i + 1}, true
			case 'H':
				return decodedInteractiveKey{key: editorKey{kind: editorKeyHome}, consumed: i + 1}, true
			case 'F':
				return decodedInteractiveKey{key: editorKey{kind: editorKeyEnd}, consumed: i + 1}, true
			case '~':
				switch string(pending[2:i]) {
				case "1", "7":
					return decodedInteractiveKey{key: editorKey{kind: editorKeyHome}, consumed: i + 1}, true
				case "3":
					return decodedInteractiveKey{key: editorKey{kind: editorKeyDelete}, consumed: i + 1}, true
				case "3;3", "3;5":
					return decodedInteractiveKey{key: editorKey{kind: editorKeyDeleteForwardWord}, consumed: i + 1}, true
				case "4", "8":
					return decodedInteractiveKey{key: editorKey{kind: editorKeyEnd}, consumed: i + 1}, true
				case "200":
					return decodedInteractiveKey{key: editorKey{kind: editorKeyPasteStart}, consumed: i + 1}, true
				case "201":
					return decodedInteractiveKey{key: editorKey{kind: editorKeyPasteEnd}, consumed: i + 1}, true
				default:
					return decodedInteractiveKey{key: editorKey{kind: editorKeyIgnore}, consumed: i + 1}, true
				}
			}
		case 'O':
			switch b {
			case 'A':
				return decodedInteractiveKey{key: editorKey{kind: editorKeyUp}, consumed: i + 1}, true
			case 'B':
				return decodedInteractiveKey{key: editorKey{kind: editorKeyDown}, consumed: i + 1}, true
			case 'C':
				return decodedInteractiveKey{key: editorKey{kind: editorKeyRight}, consumed: i + 1}, true
			case 'D':
				return decodedInteractiveKey{key: editorKey{kind: editorKeyLeft}, consumed: i + 1}, true
			case 'H':
				return decodedInteractiveKey{key: editorKey{kind: editorKeyHome}, consumed: i + 1}, true
			case 'F':
				return decodedInteractiveKey{key: editorKey{kind: editorKeyEnd}, consumed: i + 1}, true
			}
		}

		return decodedInteractiveKey{key: editorKey{kind: editorKeyIgnore}, consumed: i + 1}, true
	}

	return decodedInteractiveKey{}, false
}

func isWordMovementModifierSequence(params []byte) bool {
	if len(params) == 0 {
		return false
	}
	text := string(params)
	return strings.Contains(text, ";3") || strings.Contains(text, ";5")
}

func isEscapeFinalByte(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || b == '~'
}

func deletePreviousWordStart(line []rune, cursor int) int {
	start := cursor
	for start > 0 && unicode.IsSpace(line[start-1]) {
		start--
	}
	for start > 0 && !unicode.IsSpace(line[start-1]) {
		start--
	}
	for start > 0 && unicode.IsSpace(line[start-1]) {
		start--
	}
	return start
}

func previousWordStart(line []rune, cursor int) int {
	start := cursor
	for start > 0 && unicode.IsSpace(line[start-1]) {
		start--
	}
	for start > 0 && !unicode.IsSpace(line[start-1]) {
		start--
	}
	return start
}

func nextWordEnd(line []rune, cursor int) int {
	end := cursor
	for end < len(line) && unicode.IsSpace(line[end]) {
		end++
	}
	for end < len(line) && !unicode.IsSpace(line[end]) {
		end++
	}
	return end
}

func findReverseHistoryMatch(history []string, query string, before int) (int, string, bool) {
	if len(history) == 0 {
		return 0, "", false
	}
	if before > len(history) {
		before = len(history)
	}
	if before < 0 {
		before = len(history)
	}
	if query == "" {
		for idx := before - 1; idx >= 0; idx-- {
			if history[idx] != "" {
				return idx, history[idx], true
			}
		}
		return 0, "", false
	}
	for idx := before - 1; idx >= 0; idx-- {
		if strings.Contains(history[idx], query) {
			return idx, history[idx], true
		}
	}
	return 0, "", false
}
