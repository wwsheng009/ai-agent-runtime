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
	plainInputBurstFlushDelay     = 12 * time.Millisecond
)

type editorKeyKind int

const (
	editorKeyIgnore editorKeyKind = iota
	editorKeyRune
	editorKeyEnter
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
	editorKeyRedraw
	editorKeyYank
	editorKeyTranspose
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

type plainInputBurst struct {
	runes    []rune
	deadline time.Time
}

func (b *plainInputBurst) empty() bool {
	return b == nil || len(b.runes) == 0
}

func (b *plainInputBurst) add(r rune, now time.Time) {
	if b == nil {
		return
	}
	b.runes = append(b.runes, r)
	b.deadline = now.Add(plainInputBurstFlushDelay)
}

func (b *plainInputBurst) containsNewline() bool {
	if b == nil {
		return false
	}
	for _, r := range b.runes {
		if r == '\n' {
			return true
		}
	}
	return false
}

func (b *plainInputBurst) flush() []rune {
	if b == nil || len(b.runes) == 0 {
		return nil
	}
	out := append([]rune(nil), b.runes...)
	b.runes = b.runes[:0]
	b.deadline = time.Time{}
	return out
}

// ReadWithHistoryPrompt reads a single line using a local line editor.
//
// The caller is expected to have already rendered the prompt when running in
// interactive chat mode. This method therefore only redraws the active line and
// keeps the history state on the InputBox.
func (ib *InputBox) ReadWithHistoryPrompt(prompt string, onChange func(string)) (string, error) {
	if ib == nil {
		return "", io.EOF
	}

	if prompt == "" {
		prompt = ib.GetPrompt()
	}

	// Keep history navigation stable even if the read is cancelled.
	ib.historyPos = len(ib.history)

	if onChange != nil {
		onChange("")
	}

	// Windows retains the current line-buffered behavior for now.
	if runtime.GOOS == "windows" || !IsInteractiveTerminal() {
		line, err := readBufferedLine(os.Stdin)
		if err == nil && strings.TrimSpace(line) != "" {
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

	line, readErr := readInteractiveLine(os.Stdin, os.Stdout, prompt, ib.history, onChange)
	if readErr == nil && strings.TrimSpace(line) != "" {
		ib.AddToHistory(line)
	}
	if onChange != nil {
		onChange("")
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
	if reader == nil {
		return "", io.EOF
	}
	if writer == nil {
		writer = io.Discard
	}
	_ = prompt

	line := make([]rune, 0, 64)
	cursor := 0
	historyPos := len(history)
	var draft []rune
	var promoteDraft func()
	// Bracketed paste arrives as a bounded burst; keep it in-memory until the
	// closing marker so the terminal only redraws once for the whole paste.
	var pasteBuffer []rune
	// Plain input that arrives in a rapid burst is buffered briefly so WSL and
	// other non-bracketed terminals do not redraw the prompt on every rune.
	var plainBurst plainInputBurst
	var yankBuffer []rune
	var reverseSearchQuery []rune
	var reverseSearchOriginal []rune
	reverseSearchStart := len(history)
	reverseSearchActive := false
	pending := make([]byte, 0, 16)
	pasteActive := false
	stdinFile, _ := reader.(*os.File)

	if onChange != nil {
		onChange("")
	}

	// 调用方会在提示符渲染完成后保存输入锚点。
	// 这里每次都回到锚点，只重绘可编辑输入区，避免折行残留。
	redraw := func() {
		termWidth := GetTerminalWidth()
		if termWidth <= 0 {
			termWidth = 80
		}
		promptWidth := terminalVisibleWidth(prompt)
		var builder strings.Builder
		builder.Grow(len(line)*4 + 24)
		builder.WriteString(cursorRestoreSequence)
		builder.WriteString(clearToEndSequence)
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
		fmt.Fprint(writer, builder.String())
	}

	notifyChange := func() {
		if onChange != nil {
			onChange(string(line))
		}
	}

	setLine := func(next []rune) {
		line = append(line[:0], next...)
		cursor = len(line)
		notifyChange()
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
		notifyChange()
		redraw()
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
			notifyChange()
			redraw()
			return
		}
		line[cursor-1], line[cursor] = line[cursor], line[cursor-1]
		cursor++
		notifyChange()
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

	flushPlainBurst := func() {
		if chars := plainBurst.flush(); len(chars) > 0 {
			insertRunes(chars)
		}
	}

	waitForPlainBurstWindow := func() error {
		if plainBurst.empty() || stdinFile == nil || len(pending) > 0 {
			return nil
		}
		timeout := time.Until(plainBurst.deadline)
		if timeout <= 0 {
			flushPlainBurst()
			return nil
		}
		ready, err := waitForInteractiveInputReady(int(stdinFile.Fd()), timeout)
		if err != nil {
			return err
		}
		if !ready {
			flushPlainBurst()
		}
		return nil
	}

	plainBurstEnterShouldInsertNewline := func() (bool, error) {
		if plainBurst.empty() {
			return false, nil
		}
		if plainBurst.containsNewline() || len(pending) > 0 {
			return true, nil
		}
		if stdinFile == nil {
			return false, nil
		}
		timeout := time.Until(plainBurst.deadline)
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
		if err := waitForPlainBurstWindow(); err != nil {
			return "", err
		}
		key, ok, readErr := nextInteractiveKey(reader, &pending)
		if readErr != nil {
			flushPlainBurst()
			return "", readErr
		}
		if !ok || key.kind == editorKeyIgnore {
			continue
		}
		if key.kind == editorKeyPasteStart {
			flushPlainBurst()
			clearReverseSearchState()
			pasteActive = true
			pasteBuffer = pasteBuffer[:0]
			continue
		}
		if key.kind == editorKeyPasteEnd {
			if pasteActive && len(pasteBuffer) > 0 {
				insertRunes(append([]rune(nil), pasteBuffer...))
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
					insertRunes(append([]rune(nil), pasteBuffer...))
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
			plainBurst.add(key.r, time.Now())
		case editorKeyEnter:
			if !plainBurst.empty() {
				// If more bytes are already queued, this Enter belongs to a
				// non-bracketed paste and must stay in the editable text.
				insertNewline, err := plainBurstEnterShouldInsertNewline()
				if err != nil {
					return "", err
				}
				if insertNewline {
					plainBurst.add('\n', time.Now())
					continue
				}
				flushPlainBurst()
			}
			fmt.Fprint(writer, "\r\n")
			if onChange != nil {
				onChange("")
			}
			return strings.TrimRight(string(line), "\r\n"), nil
		case editorKeyBackspace:
			flushPlainBurst()
			clearReverseSearchState()
			if cursor <= 0 {
				continue
			}
			promoteDraft()
			line = append(line[:cursor-1], line[cursor:]...)
			cursor--
			notifyChange()
			redraw()
		case editorKeyDelete:
			flushPlainBurst()
			clearReverseSearchState()
			if cursor >= len(line) {
				continue
			}
			promoteDraft()
			line = append(line[:cursor], line[cursor+1:]...)
			notifyChange()
			redraw()
		case editorKeyLeft:
			flushPlainBurst()
			clearReverseSearchState()
			if cursor > 0 {
				cursor--
				redraw()
			}
		case editorKeyRight:
			flushPlainBurst()
			clearReverseSearchState()
			if cursor < len(line) {
				cursor++
				redraw()
			}
		case editorKeyHome:
			flushPlainBurst()
			clearReverseSearchState()
			if cursor != 0 {
				cursor = 0
				redraw()
			}
		case editorKeyEnd:
			flushPlainBurst()
			clearReverseSearchState()
			if cursor != len(line) {
				cursor = len(line)
				redraw()
			}
		case editorKeyUp:
			flushPlainBurst()
			clearReverseSearchState()
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
			flushPlainBurst()
			clearReverseSearchState()
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
			flushPlainBurst()
			clearReverseSearchState()
			if len(line) == 0 {
				continue
			}
			promoteDraft()
			storeKill(append([]rune(nil), line...))
			line = line[:0]
			cursor = 0
			notifyChange()
			redraw()
		case editorKeyDeleteWord:
			flushPlainBurst()
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
			notifyChange()
			redraw()
		case editorKeyKillToEnd:
			flushPlainBurst()
			clearReverseSearchState()
			if cursor >= len(line) {
				continue
			}
			promoteDraft()
			storeKill(append([]rune(nil), line[cursor:]...))
			line = line[:cursor]
			notifyChange()
			redraw()
		case editorKeyRedraw:
			flushPlainBurst()
			redraw()
		case editorKeyYank:
			flushPlainBurst()
			clearReverseSearchState()
			yankKilledText()
		case editorKeyTranspose:
			flushPlainBurst()
			clearReverseSearchState()
			transposeChars()
		case editorKeyReverseSearch:
			flushPlainBurst()
			beginReverseSearch()
		case editorKeyAbortSearch:
			flushPlainBurst()
			abortReverseSearch()
		case editorKeyInterrupt:
			hadTypedContent := len(line) > 0 || !plainBurst.empty()
			plainBurst = plainInputBurst{}
			fmt.Fprint(writer, "\r\n")
			if onChange != nil {
				onChange("")
			}
			if !hadTypedContent {
				return "", ErrInteractiveInputExitRequested
			}
			return "", ErrInteractiveInputInterrupted
		case editorKeyEOF:
			flushPlainBurst()
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
			notifyChange()
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

func nextInteractiveKey(reader io.Reader, pending *[]byte) (editorKey, bool, error) {
	for {
		if decoded, ok := decodeInteractiveKey(*pending); ok {
			*pending = (*pending)[decoded.consumed:]
			return decoded.key, true, nil
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
		// Bare ESC or an alt-modified key. Drop the ESC and keep processing.
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
				return decodedInteractiveKey{key: editorKey{kind: editorKeyRight}, consumed: i + 1}, true
			case 'D':
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
