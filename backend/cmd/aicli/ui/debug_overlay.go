package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

// DebugOverlayRefreshTrigger tells a DebugOverlayRefreshFunc why it was invoked.
type DebugOverlayRefreshTrigger int

const (
	// DebugOverlayRefreshKey is the user-invoked refresh (the `r`/`R` key).
	DebugOverlayRefreshKey DebugOverlayRefreshTrigger = iota
	// DebugOverlayRefreshTick is the idle poll tick (≈75ms). Implementations
	// must stay cheap and report changed=false when nothing new is available.
	DebugOverlayRefreshTick
)

// DebugOverlayRefreshFunc re-projects the overlay content. Returning
// changed=true repaints the frame with the returned title and body.
type DebugOverlayRefreshFunc func(trigger DebugOverlayRefreshTrigger) (title, body string, changed bool)

// DebugOverlayOptions describes the content of a debug overlay.
//
// The body is captured once before the alternate screen is entered, so a
// static overlay never reads live session state mid-frame and needs no
// actor-owned semantic projection. Setting Refresh turns the snapshot viewer
// into a refreshable one: the captured body is the first frame and every later
// frame is a projection the callback produced.
type DebugOverlayOptions struct {
	// Title is a short human-readable header line.
	Title string
	// Body is the plain-text body (ANSI-free) shown on the alternate screen.
	Body string
	// Refresh, when non-nil, makes the overlay live: it is invoked for the
	// refresh key (`r`/`R`) and on every idle poll tick, and a changed=true
	// answer repaints the frame with the returned title and body. The callback
	// runs between frames on the viewer's own goroutine, so it must not block
	// (a slow callback freezes the screen) and must be safe to call from that
	// non-actor goroutine.
	Refresh DebugOverlayRefreshFunc
	// RefreshHint is the footer hint for the refresh key. It defaults to
	// "r 刷新" and is ignored while Refresh is nil.
	RefreshHint string
}

// debugOverlayDefaultRefreshHint is the footer hint used when a refreshable
// overlay does not name its own.
const debugOverlayDefaultRefreshHint = "r 刷新"

const debugOverlayPollInterval = 75 * time.Millisecond

// RunDebugOverlayWithLease renders a static text body on the alternate screen
// owned by lease and runs a minimal key loop until the user dismisses it
// (q/Q/Esc/Enter/Ctrl+C/Ctrl+D). It mirrors the transcript pager's
// lease-managed lifecycle but keeps no actor-owned semantic state: every frame
// is a pure projection of the captured body, and scroll state lives only in
// this loop.
func RunDebugOverlayWithLease(ctx context.Context, terminal *Terminal, options DebugOverlayOptions, lease ScreenLease) error {
	return runDebugOverlayWithLease(ctx, terminal, options, os.Stdin, os.Stdout, lease, lease != nil && lease.Active())
}

func runDebugOverlayWithLease(ctx context.Context, terminal *Terminal, options DebugOverlayOptions, reader io.Reader, writer io.Writer, lease ScreenLease, leaseManaged bool) error {
	if terminal == nil || !terminal.SupportsANSI() || reader == nil || writer == nil {
		return ErrFullScreenUnavailable
	}
	stdinFile, _ := reader.(*os.File)
	if stdinFile == nil || !term.IsTerminal(int(stdinFile.Fd())) {
		return ErrFullScreenUnavailable
	}
	_, height := terminal.RefreshSize()
	if height < minFullScreenListHeight {
		return fullScreenUnavailable("terminal height is too small", nil)
	}
	rawState, err := term.MakeRaw(int(stdinFile.Fd()))
	if err != nil {
		return fullScreenUnavailable("enable raw mode", err)
	}
	pending := takeInteractiveInputCarryover()
	defer func() { storeInteractiveInputCarryover(pending) }()

	lifecycle := debugOverlayLifecycle{
		writer:       writer,
		restoreRaw:   func() error { return term.Restore(int(stdinFile.Fd()), rawState) },
		leaseManaged: leaseManaged,
	}
	if err := lifecycle.enter(); err != nil {
		return fullScreenUnavailable("enter alternate screen", errors.Join(err, lifecycle.close()))
	}
	runErr := runDebugOverlayLoop(ctx, terminal, options, reader, stdinFile, &pending, lease, writer)
	if closeErr := lifecycle.close(); closeErr != nil {
		return fullScreenUnavailable("restore terminal", errors.Join(runErr, closeErr))
	}
	return runErr
}

type debugOverlayLifecycle struct {
	writer       io.Writer
	restoreRaw   func() error
	leaseManaged bool
}

func (l debugOverlayLifecycle) enter() error {
	if l.leaseManaged {
		return nil
	}
	return writeFullScreenSequences(l.writer,
		"\x1b[?1049h", "\x1b[r", "\x1b[?25l", "\x1b[2J", "\x1b[H")
}

func (l debugOverlayLifecycle) close() error {
	var writeErr error
	if !l.leaseManaged {
		writeErr = writeFullScreenSequences(l.writer, "\x1b[?25h", "\x1b[r", "\x1b[?1049l")
	}
	if l.restoreRaw != nil {
		writeErr = errors.Join(writeErr, l.restoreRaw())
	}
	return writeErr
}

// debugOverlayState is the local scroll state of the loop. It is deliberately
// not shared with the actor: the debug overlay is a read-only snapshot viewer.
type debugOverlayState struct {
	offset int
}

func runDebugOverlayLoop(ctx context.Context, terminal *Terminal, options DebugOverlayOptions, reader io.Reader, stdinFile *os.File, pending *[]byte, lease ScreenLease, writer io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if terminal == nil || reader == nil || stdinFile == nil || pending == nil || writer == nil {
		return fullScreenUnavailable("debug overlay is not configured", nil)
	}
	title := options.Title
	bodyLines := splitDebugOverlayBody(options.Body)
	refreshHint := ""
	if options.Refresh != nil {
		refreshHint = debugOverlayRefreshHint(options.RefreshHint)
	}
	state := debugOverlayState{}
	lastWidth, lastHeight := -1, -1
	dirty := true
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		width, height := terminal.RefreshSize()
		if height < minFullScreenListHeight {
			return fullScreenUnavailable("terminal height is too small", nil)
		}
		// Poll the refresh source between frames: a background job that lands
		// while the viewer is open updates the screen without another key press.
		if options.Refresh != nil {
			nextTitle, nextBody, changed, ok := invokeDebugOverlayRefresh(options.Refresh, DebugOverlayRefreshTick)
			if !ok {
				// A panicking callback would otherwise tear down the TUI (and
				// skip the terminal restore) once per tick: keep the last good
				// frame and stop refreshing.
				options.Refresh = nil
				refreshHint = ""
			} else if changed {
				title, bodyLines = nextTitle, splitDebugOverlayBody(nextBody)
				dirty = true
			}
		}
		if dirty || width != lastWidth || height != lastHeight {
			state.offset = clampDebugOverlayOffset(state.offset, len(wrapDebugOverlayBody(bodyLines, width)), height)
			if err := writeLeaseManagedFullScreenText(lease, writer,
				renderDebugOverlayFrame(title, bodyLines, state.offset, width, height, refreshHint)); err != nil {
				return fullScreenUnavailable("write debug overlay frame", err)
			}
			lastWidth, lastHeight = width, height
			dirty = false
		}
		key, ok, err := readDebugOverlayKey(ctx, reader, pending, stdinFile)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if debugOverlayKeyCloses(key) {
			return nil
		}
		if options.Refresh != nil && debugOverlayRefreshKey(key) {
			// The refresh key re-projects through the same callback that owns
			// the refresh trigger; the frame is always repainted so the key
			// visibly reacts even when the projected text is unchanged.
			nextTitle, nextBody, _, refreshOK := invokeDebugOverlayRefresh(options.Refresh, DebugOverlayRefreshKey)
			if !refreshOK {
				options.Refresh = nil
				refreshHint = ""
				continue
			}
			title, bodyLines = nextTitle, splitDebugOverlayBody(nextBody)
			dirty = true
			continue
		}
		next := applyDebugOverlayKey(state.offset, len(wrapDebugOverlayBody(bodyLines, width)), height, key)
		if next != state.offset {
			state.offset = next
			dirty = true
		}
	}
}

// splitDebugOverlayBody 把捕获的正文切成行：CRLF 归一化后按 \n 切分。
func splitDebugOverlayBody(body string) []string {
	return strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
}

// debugOverlayRefreshKey reports whether the key asks for a content refresh.
// `r` and `R` are the refresh keys (a shifted press or caps lock must not
// silently turn the shortcut off).
func debugOverlayRefreshKey(key editorKey) bool {
	return key.kind == editorKeyRune && (key.r == 'r' || key.r == 'R')
}

// debugOverlayRefreshHint 归一化页脚里的刷新提示。
func debugOverlayRefreshHint(hint string) string {
	if hint = strings.TrimSpace(hint); hint == "" {
		return debugOverlayDefaultRefreshHint
	}
	return hint
}

// invokeDebugOverlayRefresh 调用刷新回调并隔离 panic：备用屏持有 raw mode 与
// lease 管理的视口，回调里的一次 panic 会直接炸掉整个 TUI 并跳过终端恢复，因此
// 这里降级为「保留上一帧 + 停用后续刷新」（ok=false）。
func invokeDebugOverlayRefresh(refresh DebugOverlayRefreshFunc, trigger DebugOverlayRefreshTrigger) (title, body string, changed, ok bool) {
	if refresh == nil {
		return "", "", false, false
	}
	defer func() {
		if recover() != nil {
			title, body, changed, ok = "", "", false, false
		}
	}()
	title, body, changed = refresh(trigger)
	return title, body, changed, true
}

func readDebugOverlayKey(readCtx context.Context, reader io.Reader, pending *[]byte, stdinFile *os.File) (editorKey, bool, error) {
	pollCtx, cancel := context.WithTimeout(readCtx, debugOverlayPollInterval)
	defer cancel()
	key, ok, readErr := nextInteractiveKey(pollCtx, reader, pending, stdinFile)
	if errors.Is(readErr, context.DeadlineExceeded) && readCtx.Err() == nil {
		return editorKey{}, false, nil
	}
	return key, ok, readErr
}

func debugOverlayKeyCloses(key editorKey) bool {
	switch key.kind {
	case editorKeyCancelPopup, editorKeyInterrupt, editorKeyEOF, editorKeyTranspose, editorKeyEnter:
		return true
	case editorKeyRune:
		return key.r == 'q' || key.r == 'Q'
	default:
		return false
	}
}

func applyDebugOverlayKey(offset, totalRows, height int, key editorKey) int {
	if totalRows < 1 {
		return 0
	}
	maxOffset := max(0, totalRows-debugOverlayViewportRows(height))
	viewportRows := debugOverlayViewportRows(height)
	switch key.kind {
	case editorKeyUp:
		return clampDebugOverlayOffset(offset-1, totalRows, height)
	case editorKeyDown:
		return clampDebugOverlayOffset(offset+1, totalRows, height)
	case editorKeyPageUp, editorKeyLeft:
		return clampDebugOverlayOffset(offset-viewportRows, totalRows, height)
	case editorKeyPageDown, editorKeyRight:
		return clampDebugOverlayOffset(offset+viewportRows, totalRows, height)
	case editorKeyHome:
		return 0
	case editorKeyEnd:
		return maxOffset
	case editorKeyRune:
		switch key.r {
		case 'j':
			return clampDebugOverlayOffset(offset+1, totalRows, height)
		case 'k':
			return clampDebugOverlayOffset(offset-1, totalRows, height)
		case 'g':
			return 0
		case 'G':
			return maxOffset
		}
	}
	return offset
}

func clampDebugOverlayOffset(offset, totalRows, height int) int {
	if totalRows < 1 {
		return 0
	}
	maxOffset := max(0, totalRows-debugOverlayViewportRows(height))
	if offset < 0 {
		return 0
	}
	if offset > maxOffset {
		return maxOffset
	}
	return offset
}

func debugOverlayViewportRows(height int) int {
	rows := height - 2
	if rows < 1 {
		return 1
	}
	return rows
}

// wrapDebugOverlayBody wraps the captured body to the terminal content width.
// Blank lines are preserved so section spacing survives wrapping. The content
// width reserves 2 columns for the indent prefix plus 1 column for the right
// edge scrollbar, so body rows and the scrollbar never overlap.
func wrapDebugOverlayBody(lines []string, width int) []string {
	if width < 1 {
		width = 1
	}
	contentWidth := max(1, width-3)
	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			wrapped = append(wrapped, "")
			continue
		}
		// A fill-only line (e.g. the full-width ═══ separator from the session
		// info panel) must never wrap: wrapping a repeated-glyph line leaves a
		// 3-column sliver remnant row. Clip it to the content width instead.
		if fill, ok := debugOverlayFillGlyph(line); ok && DisplayWidth(line) > contentWidth {
			fillWidth := DisplayWidth(fill)
			count := contentWidth / fillWidth
			if count < 1 {
				count = 1
			}
			wrapped = append(wrapped, strings.Repeat(fill, count))
			continue
		}
		parts := wrapTranscriptPagerText(line, contentWidth)
		if len(parts) == 0 {
			parts = []string{""}
		}
		wrapped = append(wrapped, parts...)
	}
	return wrapped
}

// debugOverlayFillGlyph reports the repeated glyph when line is a fill line
// made of a single non-space glyph (spaces are allowed between repeats). The
// session info panel's thick separator is such a line.
func debugOverlayFillGlyph(line string) (string, bool) {
	var fill rune
	for _, r := range line {
		if r == ' ' {
			continue
		}
		if fill == 0 {
			fill = r
			continue
		}
		if r != fill {
			return "", false
		}
	}
	if fill == 0 {
		return "", false
	}
	return string(fill), true
}

// debugOverlayScrollbarCells returns one glyph per viewport row: "█" for the
// thumb and "░" for the track, positioned by the current scroll offset. It
// returns nil when the body fits the viewport and no scrolling is possible.
func debugOverlayScrollbarCells(totalRows, viewportRows, offset int) []string {
	if totalRows <= 0 || viewportRows <= 0 || totalRows <= viewportRows {
		return nil
	}
	thumbSize := max(1, viewportRows*viewportRows/totalRows)
	maxThumbStart := viewportRows - thumbSize
	maxOffset := totalRows - viewportRows
	thumbStart := 0
	if maxOffset > 0 {
		thumbStart = offset * maxThumbStart / maxOffset
	}
	if thumbStart < 0 {
		thumbStart = 0
	}
	if thumbStart > maxThumbStart {
		thumbStart = maxThumbStart
	}
	cells := make([]string, viewportRows)
	for index := range cells {
		cells[index] = "░"
	}
	for index := thumbStart; index < thumbStart+thumbSize && index < viewportRows; index++ {
		cells[index] = "█"
	}
	return cells
}

// renderDebugOverlayFrame renders one complete alternate-screen frame: a
// header line, the visible slice of the wrapped body with a right-edge
// scrollbar, and a footer with dismiss/scroll hints and the cursor position.
// refreshHint, when non-empty, advertises the refresh key in that footer (it is
// empty for static overlays).
func renderDebugOverlayFrame(title string, bodyLines []string, offset, width, height int, refreshHint string) string {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	wrapped := wrapDebugOverlayBody(bodyLines, width)
	viewportRows := debugOverlayViewportRows(height)
	offset = clampDebugOverlayOffset(offset, len(wrapped), height)
	scrollbar := debugOverlayScrollbarCells(len(wrapped), viewportRows, offset)
	start := offset
	end := min(start+viewportRows, len(wrapped))

	lines := make([]string, height)
	label := "Debug"
	if strings.TrimSpace(title) != "" {
		label += "  " + title
	}
	lines[0] = label
	for index := start; index < end && index-start+1 < height; index++ {
		text := wrapped[index]
		if strings.TrimSpace(text) != "" {
			text = "  " + text
		}
		lines[index-start+1] = text
	}
	if height > 1 {
		lines[height-1] = debugOverlayFooter(offset, len(wrapped), refreshHint)
	}
	var builder strings.Builder
	builder.WriteString("\x1b[H")
	for row, line := range lines {
		builder.WriteString("\x1b[2K")
		if row > 0 && row < height-1 {
			// Body rows are already pre-wrapped; keep their alignment spaces
			// instead of running them through fitFullScreenText (which collapses
			// whitespace and would flatten aligned key/value columns). When
			// scrolling is active the body text is padded to width-1 so the
			// scrollbar always sits in the terminal's rightmost column, never
			// directly after the text.
			text := line
			if scrollbar != nil {
				bodyWidth := max(1, width-1)
				text = padFullScreenText(truncateFullScreenText(line, bodyWidth), bodyWidth) + scrollbar[row-1]
			} else {
				text = truncateFullScreenText(line, width)
			}
			builder.WriteString(text)
		} else {
			builder.WriteString(fitFullScreenText(line, width))
		}
		if row < len(lines)-1 {
			builder.WriteString("\r\n")
		}
	}
	return builder.String()
}

// debugOverlayFooter 是页脚提示：位置 + 滚动/关闭键；可刷新屏再追加刷新键。
func debugOverlayFooter(offset, total int, refreshHint string) string {
	footer := debugOverlayPosition(offset, total) + "  j/k 或 ↑/↓ 滚动 · q 或 Esc 关闭"
	if hint := strings.TrimSpace(refreshHint); hint != "" {
		footer += " · " + hint
	}
	return footer
}

func debugOverlayPosition(offset, total int) string {
	if total <= 0 {
		return "0/0"
	}
	return strings.TrimSpace(strings.Join([]string{strconv.Itoa(offset + 1), strconv.Itoa(total)}, "/"))
}
