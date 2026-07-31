package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"
)

var ErrInteractiveInputInterrupted = errors.New("interactive input interrupted")
var ErrInteractiveInputExitRequested = errors.New("interactive input exit requested")

// ErrInteractiveInputBacktrackRequested signals bare Esc on an empty composer.
// Chat may open the user-turn backtrack picker; other callers should treat it as a no-op cancel.
var ErrInteractiveInputBacktrackRequested = errors.New("interactive input backtrack requested")
var errInteractiveInputReadinessUnsupported = errors.New("interactive input readiness unsupported")
var waitForInteractiveInputReady = platformWaitForInteractiveInputReady
var readInteractiveClipboardText = platformClipboardText
var consumeSpecialInteractiveKey = platformConsumeSpecialInteractiveKey

const (
	// ChatComposerMaxVisibleRows keeps multiline drafts useful without letting
	// the fixed composer consume the entire terminal output region.
	ChatComposerMaxVisibleRows    = 6
	bracketedPasteEnableSequence  = "\x1b[?2004h"
	bracketedPasteDisableSequence = "\x1b[?2004l"
	focusChangeEnableSequence     = "\x1b[?1004h"
	focusChangeDisableSequence    = "\x1b[?1004l"
	cursorSaveSequence            = "\x1b[s"
	cursorRestoreSequence         = "\x1b[u"
	cursorHideSequence            = "\x1b[?25l"
	cursorShowSequence            = "\x1b[?25h"
	clearToEndSequence            = "\x1b[J"
	escapeSequenceWait            = 30 * time.Millisecond
	trailingLineFeedDrainWait     = 12 * time.Millisecond
	bracketedPasteDisplayIdleWait = 60 * time.Millisecond
)

func defaultPasteBurstHoldFirstRune() bool {
	// The editor must echo the first visible rune immediately. Paste burst
	// detection can still retroactively coalesce rapid input once enough text is
	// available, but holding the first rune makes Windows Ctrl+V appear stuck
	// when the idle flush is delayed by console event ordering.
	return false
}

type editorKeyKind int

const (
	editorKeyIgnore editorKeyKind = iota
	editorKeyRune
	editorKeyEnter
	editorKeyInsertNewline
	editorKeyComplete
	editorKeyCancelPopup
	editorKeyBackspace
	editorKeyDelete
	editorKeyLeft
	editorKeyRight
	editorKeyUp
	editorKeyDown
	editorKeyPageUp
	editorKeyPageDown
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
	editorKeyPasteClipboard
	editorKeyInterrupt
	editorKeyEOF
	editorKeyFocusGained
	editorKeyFocusLost
)

type editorKey struct {
	kind               editorKeyKind
	r                  rune
	fromCarriageReturn bool
	fromConsoleCtrlV   bool
}

var interactiveInputCarryover struct {
	sync.Mutex
	bytes []byte
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
		_, _ = WriteTerminalText(os.Stdout, prompt)
	}
	if !IsInteractiveTerminal() {
		return readBufferedLine(os.Stdin)
	}
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	_, _ = WriteTerminalLine(os.Stdout, "")
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
		_, _ = WriteTerminalText(os.Stdout, bracketedPasteDisableSequence+focusChangeDisableSequence+cursorShowSequence)
		_ = term.Restore(fd, state)
	}()
	// 启用 bracketed paste 后，终端会给粘贴块加上明确边界，
	// 这样我们就能把块内换行当作文本而不是 Enter。
	// 同时启用 focus change reporting，用于 Codex 风格的失焦通知。
	_, _ = WriteTerminalText(os.Stdout, bracketedPasteEnableSequence+focusChangeEnableSequence)
	// 提示符已经由调用方渲染到屏幕上了。
	// 重绘时使用相对光标移动定位输入区，避免依赖会被滚动失效的
	// `\x1b[s` / `\x1b[u` 绝对锚点（多行粘贴触发滚动后会把同一段输入
	// 反复打印到错误行）。

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
	return ib.readPromptWithHooksContext(context.Background(), prompt, hooks, keepHistory, echoSubmit, useDefaultPrompt, holdFirstRune)
}

func (ib *InputBox) readPromptWithHooksContext(ctx context.Context, prompt string, hooks LineEditorHooks, keepHistory bool, echoSubmit bool, useDefaultPrompt bool, holdFirstRune bool) (string, error) {
	if ib == nil {
		return "", io.EOF
	}
	if ctx == nil {
		ctx = context.Background()
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
		_, _ = WriteTerminalText(os.Stdout, bracketedPasteDisableSequence+focusChangeDisableSequence+cursorShowSequence)
		_ = term.Restore(fd, state)
	}()
	_, _ = WriteTerminalText(os.Stdout, bracketedPasteEnableSequence+focusChangeEnableSequence)

	editorHistory := lineEditorHistory(ib.history, keepHistory)
	line, readErr := readInteractiveLineWithHooksContext(ctx, os.Stdin, os.Stdout, prompt, editorHistory, nil, &hooks, echoSubmit, holdFirstRune)
	if readErr == nil && keepHistory && strings.TrimSpace(line) != "" {
		ib.AddToHistory(line)
	}
	return line, readErr
}

func lineEditorHistory(history []string, enabled bool) []string {
	if !enabled {
		return nil
	}
	return history
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
	return readInteractiveLineWithHooksContext(context.Background(), reader, writer, prompt, history, onChange, hooks, echoSubmit, holdFirstRune)
}

// SupportsCancelableInteractiveInputRead reports whether the current stdin can
// be polled before a raw-mode read. Background readers must require this so a
// context cancellation cannot leave a stale goroutine blocked inside Read.
func SupportsCancelableInteractiveInputRead() bool {
	if os.Stdin == nil {
		return false
	}
	_, err := waitForInteractiveInputReady(int(os.Stdin.Fd()), 0)
	return err == nil
}

func readInteractiveLineWithHooksContext(ctx context.Context, reader io.Reader, writer io.Writer, prompt string, history []string, onChange func(string), hooks *LineEditorHooks, echoSubmit bool, holdFirstRune bool) (string, error) {
	if reader == nil {
		return "", io.EOF
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if writer == nil {
		writer = io.Discard
	}
	_ = prompt

	initialText := ""
	initialCursor := 0
	if hooks != nil {
		initialText = NormalizePastedText(hooks.InitialText)
		initialCursor = hooks.InitialCursor
	}
	line := []rune(initialText)
	composer := NewComposerState()
	composer.SetText(initialText)
	cursor := initialCursor
	if cursor < 0 || cursor > len(line) {
		cursor = len(line)
	}
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
	pending := takeInteractiveInputCarryover()
	if pending == nil {
		pending = make([]byte, 0, 16)
	}
	pasteActive := false
	pasteInsertActive := false
	stdinFile, _ := reader.(*os.File)
	lastRenderedRows := 1
	// lastCursorRow / lastCursorCol 记录上一次重绘结束时光标的相对坐标
	// （行偏移基于“输入锚点行”，列基于终端视口的列号，与 interactiveInputVisualPosition 一致）。
	// 通过相对运动（\x1b[<n>A/B + \r + \x1b[<n>C）回到目标位置，
	// 避免依赖会因终端滚动而失效的绝对保存点。
	lastCursorRow := 0
	lastCursorCol := terminalVisibleWidth(prompt)
	// lastRenderedLine 是上一次完整重绘时使用的内容快照。
	// 当后续 redraw 发现 line 与快照按 rune 完全相同、终端宽度也未变时，
	// 只发一次光标增量，不再清屏 + 重写整段内容（极大降低长输入下的闪烁）。
	var lastRenderedLine []rune
	lastRenderedHasContent := false
	lastRenderedTermWidth := 0
	lastRenderedPromptWidth := 0
	lastRenderedViewportStart := 0
	viewportStartRow := 0
	preferredVisualCol := -1
	var redraw func()
	maxVisibleRows := func() int {
		if hooks == nil {
			return 0
		}
		if hooks.ResolveMaxVisibleRows != nil {
			return hooks.ResolveMaxVisibleRows()
		}
		return hooks.MaxVisibleRows
	}
	snapshot := func() LineEditorSnapshot {
		termWidth := GetTerminalWidth()
		if termWidth <= 0 {
			termWidth = 80
		}
		viewport := calculateInteractiveInputViewport(line, cursor, terminalVisibleWidth(prompt), termWidth, maxVisibleRows(), viewportStartRow)
		viewportStartRow = viewport.startRow
		logicalLine, logicalLines := interactiveInputLogicalPosition(line, cursor)
		return LineEditorSnapshot{
			Text:             string(line),
			Cursor:           cursor,
			Prompt:           prompt,
			HistoryPos:       historyPos,
			PasteActive:      pasteActive || pasteInsertActive || pasteBurst.IsActive(),
			DisplayRows:      viewport.total,
			CursorDisplayRow: viewport.startRow + viewport.cursor.row,
			ViewportStart:    viewport.startRow,
			ViewportRows:     viewport.rows,
			LogicalLine:      logicalLine,
			LogicalLines:     logicalLines,
		}
	}
	renderSnapshot := func() LineEditorRenderSnapshot {
		return LineEditorRenderSnapshot{
			LastCursorRow: lastCursorRow,
			LastCursorCol: lastCursorCol,
			ViewportStart: lastRenderedViewportStart,
		}
	}
	terminalWritePrefix := func(render LineEditorRenderSnapshot) string {
		if hooks == nil {
			return ""
		}
		if hooks.OnBeforeTerminalWrite != nil {
			return hooks.OnBeforeTerminalWrite(snapshot(), render)
		}
		if hooks.OnBeforeRedraw != nil {
			hooks.OnBeforeRedraw(snapshot(), render)
		}
		return ""
	}
	writeEditorText := func(text string, render LineEditorRenderSnapshot) {
		if text == "" {
			return
		}
		if hooks != nil && hooks.OnTerminalWrite != nil && hooks.OnTerminalWrite(snapshot(), render, writer, text) {
			return
		}
		if prefix := terminalWritePrefix(render); prefix != "" {
			_, _ = WriteTerminalText(writer, cursorHideSequence+prefix+text+cursorShowSequence)
			return
		}
		_, _ = WriteTerminalText(writer, text)
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
		composer.ReplaceText(repl.Text)
		line = []rune(composer.Text())
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

	// 重绘策略：用相对运动定位回输入锚点，再清空已渲染区域并写入当前内容。
	// 这样即便多行粘贴触发终端滚动，锚点也跟随内容一起上移，不会出现
	// 把同一段输入反复打印到错位行的现象（绝对 `\x1b[s`/`\x1b[u` 在滚动后
	// 会把光标恢复到一个早已被滚走的视口坐标）。
	redraw = func() {
		renderBefore := renderSnapshot()
		termWidth := GetTerminalWidth()
		if termWidth <= 0 {
			termWidth = 80
		}
		promptWidth := terminalVisibleWidth(prompt)
		resolvedMaxVisibleRows := maxVisibleRows()
		viewport := calculateInteractiveInputViewport(line, cursor, promptWidth, termWidth, resolvedMaxVisibleRows, viewportStartRow)
		viewportStartRow = viewport.startRow
		cursorPos := viewport.cursor

		// Fast path: 内容、终端宽度、提示符宽度都未变化时，只发光标增量。
		// 这能让方向键 / Home / End / 历史导航等"光标-only"操作跳过整段
		// `\x1b[K` + 重写流程，长粘贴下也不会再有逐行闪烁。
		if lastRenderedHasContent &&
			termWidth == lastRenderedTermWidth &&
			promptWidth == lastRenderedPromptWidth &&
			viewport.startRow == lastRenderedViewportStart &&
			runesEqual(line, lastRenderedLine) {
			if cursorPos.row == lastCursorRow && cursorPos.col == lastCursorCol {
				return
			}
			var builder strings.Builder
			builder.Grow(24)
			if dr := cursorPos.row - lastCursorRow; dr > 0 {
				fmt.Fprintf(&builder, "\x1b[%dB", dr)
			} else if dr < 0 {
				fmt.Fprintf(&builder, "\x1b[%dA", -dr)
			}
			builder.WriteByte('\r')
			if cursorPos.col > 0 {
				fmt.Fprintf(&builder, "\x1b[%dC", cursorPos.col)
			}
			lastCursorRow = cursorPos.row
			lastCursorCol = cursorPos.col
			writeEditorText(builder.String(), renderBefore)
			return
		}

		renderedRows := viewport.rows
		clearRows := renderedRows
		if lastRenderedRows > clearRows {
			clearRows = lastRenderedRows
		}
		var builder strings.Builder
		builder.Grow(len(line)*4 + 64)
		// 1) 从“上次光标位置”相对地回到输入锚点行（提示符所在行）。
		if lastCursorRow > 0 {
			fmt.Fprintf(&builder, "\x1b[%dA", lastCursorRow)
		}
		// 2) 移到当前视口的起始列。有限高视口会重绘提示符，因此从
		//    第 0 列开始；旧的无限高模式仍从提示符之后开始。
		builder.WriteByte('\r')
		if resolvedMaxVisibleRows <= 0 && promptWidth > 0 {
			fmt.Fprintf(&builder, "\x1b[%dC", promptWidth)
		}
		// 3) 清掉锚点行从输入起点开始的旧内容；并按需向下逐行清理。
		builder.WriteString("\x1b[K")
		for i := 1; i < clearRows; i++ {
			builder.WriteString("\x1b[1B\r\x1b[K")
		}
		// 4) 回到锚点行 + 输入起点，再写入新内容。
		if clearRows > 1 {
			fmt.Fprintf(&builder, "\x1b[%dA", clearRows-1)
		}
		builder.WriteByte('\r')
		if resolvedMaxVisibleRows <= 0 && promptWidth > 0 {
			fmt.Fprintf(&builder, "\x1b[%dC", promptWidth)
		}
		if resolvedMaxVisibleRows > 0 {
			builder.WriteString(renderInteractiveInputViewport(prompt, line, termWidth, viewport.startRow, viewport.rows))
		} else {
			builder.WriteString(renderInteractiveInputForTerminal(line))
		}
		endPos := interactiveInputVisualPosition(line, len(line), promptWidth, termWidth)
		endPos.row -= viewport.startRow
		if endPos.row >= viewport.rows {
			endPos.row = viewport.rows - 1
			endPos.col = termWidth - 1
		}
		if endPos.row < 0 {
			endPos.row = 0
			endPos.col = 0
		}
		if cursor < len(line) {
			if rowsUp := endPos.row - cursorPos.row; rowsUp > 0 {
				fmt.Fprintf(&builder, "\x1b[%dA", rowsUp)
			}
			builder.WriteByte('\r')
			if cursorPos.col > 0 {
				fmt.Fprintf(&builder, "\x1b[%dC", cursorPos.col)
			}
			lastCursorRow = cursorPos.row
			lastCursorCol = cursorPos.col
		} else {
			lastCursorRow = endPos.row
			lastCursorCol = endPos.col
		}
		lastRenderedRows = renderedRows
		lastRenderedHasContent = true
		lastRenderedTermWidth = termWidth
		lastRenderedPromptWidth = promptWidth
		lastRenderedViewportStart = viewport.startRow
		lastRenderedLine = append(lastRenderedLine[:0], line...)
		writeEditorText(builder.String(), renderBefore)
	}
	if hooks != nil && hooks.RedrawInitialText && len(line) > 0 {
		// A fixed composer can be restarted with a draft that survived a prompt
		// clear. Repaint it now instead of waiting for the next key to make the
		// editor's render cache and the terminal agree again.
		redraw()
	}

	setLine := func(next []rune) {
		composer.ReplaceText(string(next))
		line = []rune(composer.Text())
		cursor = len(line)
		emitChange()
		redraw()
	}

	insertRunes := func(chars []rune) {
		if len(chars) == 0 {
			return
		}
		preferredVisualCol = -1
		reverseSearchActive = false
		reverseSearchQuery = reverseSearchQuery[:0]
		reverseSearchStart = len(history)
		promoteDraft()
		cursor = composer.InsertTextAt(cursor, string(chars))
		line = []rune(composer.Text())
		emitChange()
		redraw()
	}

	insertPastedText := func(text string) bool {
		text = NormalizePastedText(text)
		if text == "" {
			return false
		}
		preferredVisualCol = -1
		reverseSearchActive = false
		reverseSearchQuery = reverseSearchQuery[:0]
		reverseSearchStart = len(history)
		promoteDraft()
		pasteInsertActive = true
		cursor = composer.HandlePasteAt(cursor, text)
		line = []rune(composer.Text())
		emitChange()
		redraw()
		pasteInsertActive = false
		if !pasteActive {
			emitChange()
		}
		return true
	}

	insertTypedRune := func(ch rune) {
		if ch == 0 {
			return
		}
		preferredVisualCol = -1
		reverseSearchActive = false
		reverseSearchQuery = reverseSearchQuery[:0]
		reverseSearchStart = len(history)
		promoteDraft()
		cursor = composer.InsertTextAt(cursor, string(ch))
		line = []rune(composer.Text())
		emitChange()
		redraw()
	}

	submittedText := func() string {
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
			composer.SetText(string(line))
			cursor = len(line)
			emitChange()
			redraw()
			return
		}
		line[cursor-1], line[cursor] = line[cursor], line[cursor-1]
		composer.SetText(string(line))
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

	moveVisualLine := func(delta int) bool {
		if delta == 0 || len(line) == 0 {
			return false
		}
		termWidth := GetTerminalWidth()
		if termWidth <= 0 {
			termWidth = 80
		}
		promptWidth := terminalVisibleWidth(prompt)
		current := interactiveInputVisualPosition(line, cursor, promptWidth, termWidth)
		targetRow := current.row + delta
		totalRows := interactiveInputDisplayRows(line, promptWidth, termWidth)
		if targetRow < 0 || targetRow >= totalRows {
			preferredVisualCol = -1
			return false
		}
		if preferredVisualCol < 0 {
			preferredVisualCol = current.col
			if current.row == 0 {
				preferredVisualCol -= promptWidth
			}
			if preferredVisualCol < 0 {
				preferredVisualCol = 0
			}
		}
		targetCol := preferredVisualCol
		if targetRow == 0 {
			targetCol += promptWidth
		}
		nextCursor, ok := interactiveInputCursorAtVisualRow(line, promptWidth, termWidth, targetRow, targetCol)
		if !ok || nextCursor == cursor {
			return false
		}
		promoteDraft()
		cursor = nextCursor
		emitChange()
		redraw()
		return true
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
		cursor = composer.DeleteRange(start, end)
		line = []rune(composer.Text())
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
		case FlushResultPasteInactive:
			emitChange()
		}
	}

	flushPasteBurstBeforeModifiedInput := func() {
		clearNoHoldActivity := pasteBurst.HasNoHoldActivity() && !pasteBurst.HasBufferedText() && !pasteBurst.HasPendingFirstChar()
		if pasted := pasteBurst.FlushBeforeModifiedInput(); pasted != "" {
			insertPastedText(pasted)
			return
		}
		if clearNoHoldActivity {
			emitChange()
		}
	}

	waitForPasteBurstWindow := func() error {
		for pasteBurst.IsActive() && stdinFile != nil && len(pending) == 0 {
			timeout := time.Until(pasteBurst.Deadline())
			if timeout <= 0 {
				flushPasteBurst()
				return nil
			}
			ready, err := waitForInteractiveInputReady(int(stdinFile.Fd()), timeout)
			if err != nil {
				if errors.Is(err, errInteractiveInputReadinessUnsupported) {
					time.Sleep(timeout)
					continue
				}
				return err
			}
			if ready {
				return nil
			}
			// poll(2) accepts millisecond timeouts and can return just before a
			// sub-millisecond remainder expires. Re-check the deadline instead of
			// falling through to a blocking key read with an unflushed buffer.
		}
		return nil
	}

	waitForBracketedPasteDisplay := func() error {
		if !pasteActive || len(pasteBuffer) == 0 || stdinFile == nil || len(pending) > 0 {
			return nil
		}
		deadline := time.Now().Add(bracketedPasteDisplayIdleWait)
		for pasteActive && len(pasteBuffer) > 0 && len(pending) == 0 {
			timeout := time.Until(deadline)
			if timeout <= 0 {
				insertPastedText(string(pasteBuffer))
				pasteBuffer = pasteBuffer[:0]
				return nil
			}
			ready, err := waitForInteractiveInputReady(int(stdinFile.Fd()), timeout)
			if err != nil {
				if errors.Is(err, errInteractiveInputReadinessUnsupported) {
					time.Sleep(timeout)
					continue
				}
				return err
			}
			if ready {
				return nil
			}
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
		ready, err := waitForInteractiveInputReady(int(stdinFile.Fd()), timeout)
		if errors.Is(err, errInteractiveInputReadinessUnsupported) {
			time.Sleep(timeout)
			return false, nil
		}
		return ready, err
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
		ready, err := waitForInteractiveInputReady(int(stdinFile.Fd()), timeout)
		if errors.Is(err, errInteractiveInputReadinessUnsupported) {
			time.Sleep(timeout)
			return false, nil
		}
		return ready, err
	}

	prependPendingBytes := func(data []byte) {
		if len(data) == 0 {
			return
		}
		if len(pending) == 0 {
			pending = append(pending[:0], data...)
			return
		}
		merged := make([]byte, 0, len(data)+len(pending))
		merged = append(merged, data...)
		merged = append(merged, pending...)
		pending = merged
	}

	readReadyInteractiveBytes := func() ([]byte, error) {
		if reader == nil {
			return nil, nil
		}
		var buf [64]byte
		n, err := reader.Read(buf[:])
		if n > 0 {
			return append([]byte(nil), buf[:n]...), nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		return nil, nil
	}

	classifyCarriageReturnEnter := func(timeout time.Duration) (bool, bool, error) {
		if stdinFile == nil {
			return false, false, nil
		}
		if len(pending) > 0 {
			if pending[0] != '\n' {
				return true, false, nil
			}
			if len(pending) == 1 {
				pending = pending[:0]
				return false, true, nil
			}
			pending = pending[1:]
			return true, false, nil
		}
		if timeout <= 0 {
			return false, false, nil
		}
		ready, err := waitForInteractiveInputReady(int(stdinFile.Fd()), timeout)
		if err != nil {
			if errors.Is(err, errInteractiveInputReadinessUnsupported) {
				time.Sleep(timeout)
				return false, false, nil
			}
			return false, false, err
		}
		if !ready {
			return false, false, nil
		}
		data, err := readReadyInteractiveBytes()
		if err != nil {
			return false, false, err
		}
		if len(data) == 0 {
			return false, false, nil
		}
		if data[0] != '\n' {
			prependPendingBytes(data)
			return true, false, nil
		}
		remainder := append([]byte(nil), data[1:]...)
		if len(remainder) == 0 {
			ready, err = waitForInteractiveInputReady(int(stdinFile.Fd()), trailingLineFeedDrainWait)
			if err != nil {
				if errors.Is(err, errInteractiveInputReadinessUnsupported) {
					time.Sleep(trailingLineFeedDrainWait)
					ready = false
				} else {
					return false, false, err
				}
			}
			if ready {
				more, err := readReadyInteractiveBytes()
				if err != nil {
					return false, false, err
				}
				remainder = append(remainder, more...)
			}
		}
		if len(remainder) == 0 {
			return false, true, nil
		}
		prependPendingBytes(remainder)
		return true, false, nil
	}

	promoteDraft = func() {
		if historyPos == len(history) {
			return
		}
		draft = append(draft[:0], line...)
	}

	for {
		if err := ctx.Err(); err != nil {
			flushPasteBurstBeforeModifiedInput()
			return "", err
		}
		if err := waitForPasteBurstWindow(); err != nil {
			return "", err
		}
		if err := waitForBracketedPasteDisplay(); err != nil {
			return "", err
		}
		key, ok, readErr := nextInteractiveKey(ctx, reader, &pending, stdinFile)
		if readErr != nil {
			flushPasteBurstBeforeModifiedInput()
			return "", readErr
		}
		if !ok || key.kind == editorKeyIgnore {
			continue
		}
		if key.kind == editorKeyFocusGained {
			SetTerminalFocused(true)
			continue
		}
		if key.kind == editorKeyFocusLost {
			SetTerminalFocused(false)
			continue
		}
		if key.kind != editorKeyUp && key.kind != editorKeyDown && key.kind != editorKeyPageUp && key.kind != editorKeyPageDown {
			preferredVisualCol = -1
		}
		if key.kind == editorKeyPasteStart {
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			pasteActive = true
			pasteBuffer = pasteBuffer[:0]
			continue
		}
		if key.kind == editorKeyPasteEnd {
			wasPasteActive := pasteActive
			if pasteActive && len(pasteBuffer) > 0 {
				insertPastedText(string(pasteBuffer))
				pasteBuffer = pasteBuffer[:0]
			}
			pasteActive = false
			if wasPasteActive {
				emitChange()
			}
			continue
		}
		if pasteActive && (key.kind == editorKeyEnter || key.kind == editorKeyInsertNewline) {
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
			case editorKeyPasteClipboard:
				if pasted, err := readInteractiveClipboardText(); err == nil && pasted != "" {
					pasteBuffer = append(pasteBuffer, []rune(pasted)...)
				}
			case editorKeyInterrupt:
				writeEditorText("\r\n", renderSnapshot())
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
		case editorKeyPasteClipboard:
			hadBufferedPaste := pasteBurst.HasBufferedText()
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			if key.fromConsoleCtrlV && hadBufferedPaste {
				continue
			}
			if pasted, err := readInteractiveClipboardText(); err == nil && pasted != "" {
				insertPastedText(pasted)
			}
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
						cursor = composer.DeleteRange(grab.StartByte, safeCursor)
						line = []rune(composer.Text())
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
		case editorKeyInsertNewline:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			insertTypedRune('\n')
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
			if hooks != nil && hooks.OnCancel != nil && hooks.OnCancel(snapshot()) {
				if onChange != nil {
					onChange("")
				}
				return "", nil
			}
			if hooks != nil && hooks.OnCancelPopup != nil && hooks.OnCancelPopup(snapshot()) {
				continue
			}
			// Bare Esc with empty draft: signal chat to open user-turn backtrack selection.
			// Keep typed drafts untouched so Esc never silently discards composer text.
			if len(line) == 0 && !pasteBurst.IsActive() {
				if onChange != nil {
					onChange("")
				}
				return "", ErrInteractiveInputBacktrackRequested
			}
		case editorKeyEnter:
			suppressSubmitDrain := false
			handledCarriageReturn := false
			if key.fromCarriageReturn {
				timeout := time.Until(pasteBurst.PlainContinuationDeadline())
				if pasteBurst.IsActive() {
					timeout = time.Until(pasteBurst.Deadline())
				}
				insertNewline, consumedTrailingLF, err := classifyCarriageReturnEnter(timeout)
				if err != nil {
					return "", err
				}
				handledCarriageReturn = true
				if consumedTrailingLF {
					suppressSubmitDrain = true
					key.fromCarriageReturn = false
				}
				if insertNewline {
					now := time.Now()
					if pasteBurst.IsActive() {
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
					}
					pasteBurst.ClearWindowAfterNonChar()
					insertTypedRune('\n')
					pasteBurst.ExtendWindow(now)
					continue
				}
			}
			if !handledCarriageReturn && pasteBurst.IsActive() {
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
			if !handledCarriageReturn {
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
			}
			if hooks != nil && hooks.OnSubmit != nil {
				if repl, ok := hooks.OnSubmit(snapshot()); ok {
					applyReplacement(repl)
					continue
				}
			}
			if echoSubmit && (hooks == nil || !hooks.SuppressSubmitEcho) {
				writeEditorText("\r\n", renderSnapshot())
			}
			if onChange != nil {
				onChange("")
			}
			if !suppressSubmitDrain && shouldDrainTrailingLineFeedAfterSubmit(key, echoSubmit, line) {
				drainTrailingLineFeedAfterCarriageReturn(ctx, reader, &pending, stdinFile)
			}
			return submittedText(), nil
		case editorKeyBackspace:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			if cursor <= 0 {
				continue
			}
			promoteDraft()
			cursor = composer.DeleteRange(cursor-1, cursor)
			line = []rune(composer.Text())
			emitChange()
			redraw()
		case editorKeyDelete:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			if cursor >= len(line) {
				continue
			}
			promoteDraft()
			cursor = composer.DeleteRange(cursor, cursor+1)
			line = []rune(composer.Text())
			emitChange()
			redraw()
		case editorKeyLeft:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			preferredVisualCol = -1
			if hooks != nil && hooks.OnMove != nil && hooks.OnMove(snapshot(), -1) {
				continue
			}
			if cursor > 0 {
				cursor--
				emitChange()
				redraw()
			}
		case editorKeyRight:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			preferredVisualCol = -1
			if hooks != nil && hooks.OnMove != nil && hooks.OnMove(snapshot(), 1) {
				continue
			}
			if cursor < len(line) {
				cursor++
				emitChange()
				redraw()
			}
		case editorKeyHome:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			preferredVisualCol = -1
			if cursor != 0 {
				cursor = 0
				emitChange()
				redraw()
			}
		case editorKeyEnd:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			preferredVisualCol = -1
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
			if moveVisualLine(-1) {
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
			if moveVisualLine(1) {
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
		case editorKeyPageUp:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			rows := maxVisibleRows()
			if rows <= 0 {
				rows = ChatComposerMaxVisibleRows
			}
			steps := rows - 1
			if steps < 1 {
				steps = 1
			}
			for i := 0; i < steps && moveVisualLine(-1); i++ {
			}
		case editorKeyPageDown:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			rows := maxVisibleRows()
			if rows <= 0 {
				rows = ChatComposerMaxVisibleRows
			}
			steps := rows - 1
			if steps < 1 {
				steps = 1
			}
			for i := 0; i < steps && moveVisualLine(1); i++ {
			}
		case editorKeyClearLine:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			if len(line) == 0 {
				continue
			}
			promoteDraft()
			storeKill(append([]rune(nil), line...))
			cursor = composer.DeleteRange(0, len(line))
			line = []rune(composer.Text())
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
			cursor = composer.DeleteRange(start, cursor)
			line = []rune(composer.Text())
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
			cursor = composer.DeleteRange(cursor, len(line))
			line = []rune(composer.Text())
			emitChange()
			redraw()
		case editorKeyDeleteForwardWord:
			flushPasteBurstBeforeModifiedInput()
			clearReverseSearchState()
			deleteForwardWord()
		case editorKeyRedraw:
			flushPasteBurstBeforeModifiedInput()
			lastRenderedHasContent = false
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
				writeEditorText("\r\n", renderSnapshot())
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
				// Ctrl+D at EOL is a no-op on a live TTY. When the input source is already
				// exhausted (piped/test readers with no stdinFile), looping would hang; submit.
				if stdinFile == nil {
					if echoSubmit {
						writeEditorText("\r\n", renderSnapshot())
					}
					if onChange != nil {
						onChange("")
					}
					return string(line), nil
				}
				continue
			}
			promoteDraft()
			cursor = composer.DeleteRange(cursor, cursor+1)
			line = []rune(composer.Text())
			emitChange()
			redraw()
		}
	}
}

type interactiveInputPosition struct {
	row int
	col int
}

type interactiveInputViewport struct {
	startRow int
	rows     int
	total    int
	cursor   interactiveInputPosition
}

func calculateInteractiveInputViewport(line []rune, cursor, startCol, termWidth, maxRows, previousStart int) interactiveInputViewport {
	cursorPos := interactiveInputVisualPosition(line, cursor, startCol, termWidth)
	totalRows := interactiveInputDisplayRows(line, startCol, termWidth)
	visibleRows := totalRows
	if maxRows > 0 && visibleRows > maxRows {
		visibleRows = maxRows
	}
	start := boundedInteractiveInputViewportStart(totalRows, cursorPos.row, visibleRows, previousStart)
	cursorPos.row -= start
	return interactiveInputViewport{startRow: start, rows: visibleRows, total: totalRows, cursor: cursorPos}
}

func boundedInteractiveInputViewportStart(totalRows, cursorRow, visibleRows, previousStart int) int {
	if totalRows < 1 {
		totalRows = 1
	}
	if visibleRows < 1 || visibleRows >= totalRows {
		return 0
	}
	maxStart := totalRows - visibleRows
	start := previousStart
	if start < 0 {
		start = 0
	}
	if start > maxStart {
		start = maxStart
	}
	if cursorRow < start {
		start = cursorRow
	} else if cursorRow >= start+visibleRows {
		start = cursorRow - visibleRows + 1
	}
	if start < 0 {
		return 0
	}
	if start > maxStart {
		return maxStart
	}
	return start
}

func renderInteractiveInputViewport(prompt string, line []rune, termWidth, startRow, rows int) string {
	visualRows := interactiveInputVisualRows(prompt, line, termWidth)
	if len(visualRows) == 0 || rows <= 0 || startRow >= len(visualRows) {
		return ""
	}
	if startRow < 0 {
		startRow = 0
	}
	end := startRow + rows
	if end > len(visualRows) {
		end = len(visualRows)
	}
	return strings.Join(visualRows[startRow:end], "\r\n")
}

func interactiveInputVisualRows(prompt string, line []rune, termWidth int) []string {
	if termWidth <= 0 {
		termWidth = 80
	}
	rows := []strings.Builder{{}}
	rows[0].WriteString(prompt)
	col := terminalVisibleWidth(prompt)
	if col >= termWidth {
		col %= termWidth
	}
	newRow := func() {
		rows = append(rows, strings.Builder{})
		col = 0
	}
	for _, r := range line {
		if r == '\r' || r == '\n' {
			newRow()
			continue
		}
		width := DisplayWidth(string(r))
		if width <= 0 {
			rows[len(rows)-1].WriteRune(r)
			continue
		}
		if col > 0 && col+width > termWidth {
			newRow()
		}
		rows[len(rows)-1].WriteRune(r)
		col += width
		if col >= termWidth {
			newRow()
		}
	}
	out := make([]string, len(rows))
	for i := range rows {
		out[i] = rows[i].String()
	}
	return out
}

func interactiveInputCursorAtVisualRow(line []rune, startCol, termWidth, targetRow, targetCol int) (int, bool) {
	bestCursor := -1
	bestDistance := int(^uint(0) >> 1)
	for candidate := 0; candidate <= len(line); candidate++ {
		pos := interactiveInputVisualPosition(line, candidate, startCol, termWidth)
		if pos.row != targetRow {
			continue
		}
		distance := pos.col - targetCol
		if distance < 0 {
			distance = -distance
		}
		if distance < bestDistance {
			bestCursor = candidate
			bestDistance = distance
		}
	}
	return bestCursor, bestCursor >= 0
}

func interactiveInputLogicalPosition(line []rune, cursor int) (int, int) {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(line) {
		cursor = len(line)
	}
	current := 1
	for _, r := range line[:cursor] {
		if r == '\r' || r == '\n' {
			current++
		}
	}
	total := current
	for _, r := range line[cursor:] {
		if r == '\r' || r == '\n' {
			total++
		}
	}
	return current, total
}

func runesEqual(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i, r := range a {
		if r != b[i] {
			return false
		}
	}
	return true
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
		if pos.col > 0 && pos.col+width > termWidth {
			pos.row++
			pos.col = 0
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

func nextInteractiveKey(ctx context.Context, reader io.Reader, pending *[]byte, stdinFile *os.File) (editorKey, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cancelable := ctx.Done() != nil
	for {
		if err := ctx.Err(); err != nil {
			return editorKey{}, false, err
		}
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
				if errors.Is(err, errInteractiveInputReadinessUnsupported) {
					time.Sleep(escapeSequenceWait)
					*pending = (*pending)[:0]
					return editorKey{kind: editorKeyCancelPopup}, true, nil
				}
				return editorKey{}, false, err
			}
			if !ready {
				*pending = (*pending)[:0]
				return editorKey{kind: editorKeyCancelPopup}, true, nil
			}
		}

		if stdinFile != nil && len(*pending) == 0 {
			key, ok, _ := consumeSpecialInteractiveKey(int(stdinFile.Fd()))
			if ok {
				return key, true, nil
			}
		}

		if stdinFile != nil {
			ready, err := waitForInteractiveInputReady(int(stdinFile.Fd()), 50*time.Millisecond)
			if err != nil {
				if errors.Is(err, errInteractiveInputReadinessUnsupported) {
					if cancelable {
						time.Sleep(50 * time.Millisecond)
						continue
					}
					ready = true
				} else {
					return editorKey{}, false, err
				}
			}
			if !ready {
				continue
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

func shouldDrainTrailingLineFeedAfterSubmit(key editorKey, _ bool, _ []rune) bool {
	return runtime.GOOS == "windows" && key.fromCarriageReturn
}

// Windows terminals may deliver Enter as CR followed by LF. If the editor
// submits on the CR and restores cooked mode first, the LF can echo as a stray
// newline and leave the next prompt apparently unfocused.
func drainTrailingLineFeedAfterCarriageReturn(ctx context.Context, reader io.Reader, pending *[]byte, stdinFile *os.File) {
	if reader == nil || pending == nil || stdinFile == nil {
		return
	}
	if len(*pending) > 0 {
		return
	}
	if ctx != nil && ctx.Err() != nil {
		return
	}
	ready, err := waitForInteractiveInputReady(int(stdinFile.Fd()), trailingLineFeedDrainWait)
	if err != nil || !ready {
		return
	}
	var buf [64]byte
	n, err := reader.Read(buf[:])
	if n <= 0 || err != nil && !errors.Is(err, io.EOF) {
		return
	}
	if buf[0] == '\n' {
		if n > 1 {
			storeInteractiveInputCarryover(buf[1:n])
		}
		return
	}
	storeInteractiveInputCarryover(buf[:n])
}

func takeInteractiveInputCarryover() []byte {
	interactiveInputCarryover.Lock()
	defer interactiveInputCarryover.Unlock()
	if len(interactiveInputCarryover.bytes) == 0 {
		return nil
	}
	out := append([]byte(nil), interactiveInputCarryover.bytes...)
	interactiveInputCarryover.bytes = interactiveInputCarryover.bytes[:0]
	return out
}

func storeInteractiveInputCarryover(data []byte) {
	if len(data) == 0 {
		return
	}
	interactiveInputCarryover.Lock()
	defer interactiveInputCarryover.Unlock()
	interactiveInputCarryover.bytes = append(interactiveInputCarryover.bytes, data...)
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
		return decodedInteractiveKey{key: editorKey{kind: editorKeyEnter, fromCarriageReturn: true}, consumed: 1}, true
	case '\n':
		return decodedInteractiveKey{key: editorKey{kind: editorKeyEnter}, consumed: 1}, true
	case 15:
		// Ctrl+O is the portable control-key fallback for inserting a newline.
		// Ctrl+J is indistinguishable from a terminal LF/Enter submission.
		return decodedInteractiveKey{key: editorKey{kind: editorKeyInsertNewline}, consumed: 1}, true
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
	case 22:
		return decodedInteractiveKey{key: editorKey{kind: editorKeyPasteClipboard}, consumed: 1}, true
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
		case '\r', '\n':
			return decodedInteractiveKey{key: editorKey{kind: editorKeyInsertNewline}, consumed: 2}, true
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
			case 'u':
				if isModifiedEnterSequence(string(pending[2:i])) {
					return decodedInteractiveKey{key: editorKey{kind: editorKeyInsertNewline}, consumed: i + 1}, true
				}
				return decodedInteractiveKey{key: editorKey{kind: editorKeyIgnore}, consumed: i + 1}, true
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
			case 'I':
				return decodedInteractiveKey{key: editorKey{kind: editorKeyFocusGained}, consumed: i + 1}, true
			case 'O':
				return decodedInteractiveKey{key: editorKey{kind: editorKeyFocusLost}, consumed: i + 1}, true
			case '~':
				switch string(pending[2:i]) {
				case "13;2", "27;2;13", "27;3;13", "27;5;13":
					return decodedInteractiveKey{key: editorKey{kind: editorKeyInsertNewline}, consumed: i + 1}, true
				case "1", "7":
					return decodedInteractiveKey{key: editorKey{kind: editorKeyHome}, consumed: i + 1}, true
				case "5":
					return decodedInteractiveKey{key: editorKey{kind: editorKeyPageUp}, consumed: i + 1}, true
				case "6":
					return decodedInteractiveKey{key: editorKey{kind: editorKeyPageDown}, consumed: i + 1}, true
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

func isModifiedEnterSequence(params string) bool {
	parts := strings.Split(params, ";")
	if len(parts) < 2 || parts[0] != "13" {
		return false
	}
	return parts[1] == "2" || parts[1] == "3" || parts[1] == "5"
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
