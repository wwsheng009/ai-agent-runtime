package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"golang.org/x/term"
)

// ErrFullScreenUnavailable reports that the caller should use its plain-text fallback.
var ErrFullScreenUnavailable = errors.New("full-screen terminal UI is unavailable")

// FullScreenListItem describes one searchable row and its selected-item preview.
type FullScreenListItem struct {
	Title      string
	Detail     string
	Preview    string
	SearchText string
	Disabled   bool
}

// FullScreenListOptions configures the full-screen list header and items.
type FullScreenListOptions struct {
	Title        string
	Subtitle     string
	EmptyMessage string
	ConfirmLabel string
	Items        []FullScreenListItem

	// Optional Phase 4 hooks. Callers must not fmt.Println from these;
	// they update session memory / request redraw only.
	//
	// OnSelectionChanged fires with the original item index whenever the
	// highlighted row changes (including the initial frame).
	OnSelectionChanged func(index int)
	// OnCancel fires when the user aborts (Esc/q/interrupt).
	OnCancel func()
	// OnConfirm runs after Enter on an enabled item. A non-nil error keeps
	// the list open (caller may set item state); nil accepts and closes.
	OnConfirm func(index int) error
	// PreviewForItem, when set, replaces item.Preview for the highlighted row.
	PreviewForItem func(index int) string
}

// FullScreenListResult identifies the selected original item index.
type FullScreenListResult struct {
	Index     int
	Cancelled bool
}

type fullScreenListState struct {
	selected  int
	offset    int
	query     string
	searching bool
}

type fullScreenFrameLine struct {
	text         string
	selected     bool
	preformatted bool
}

const (
	minFullScreenListHeight    = 8
	fullScreenListPollInterval = 100 * time.Millisecond
)

type fullScreenListLoopHooks struct {
	refreshSize  func() (int, int)
	writeFrame   func(string) error
	readKey      func(context.Context) (editorKey, bool, error)
	colorProfile render.ColorProfile
}

type fullScreenListLifecycle struct {
	writer     io.Writer
	restoreRaw func() error
	// leaseManaged is true when the caller already holds an alternate-screen
	// lease whose Acquire/Release owns the DEC 1049 enter/exit sequences. The
	// list then skips its own screen sequences (writing them twice would
	// desync the alternate buffer) and keeps stdin raw-mode handling.
	leaseManaged bool
}

// CanUseFullScreenList reports whether the current process has an ANSI TTY.
func CanUseFullScreenList(terminal *Terminal) bool {
	if terminal == nil || !terminal.SupportsANSI() || !IsInteractiveTerminal() {
		return false
	}
	_, height := terminal.RefreshSize()
	return height >= minFullScreenListHeight
}

// SelectFullScreenList opens an alternate-screen list and restores the terminal on exit.
func SelectFullScreenList(ctx context.Context, terminal *Terminal, options FullScreenListOptions) (FullScreenListResult, error) {
	return selectFullScreenList(ctx, terminal, options, os.Stdin, os.Stdout, false)
}

// SelectFullScreenListWithLease opens an alternate-screen list while an
// alternate-screen lease is already active. The lease owns the DEC 1049
// enter/exit sequences (see FixedBottomSurface.AcquireAlternateScreen), so the
// list only manages stdin raw mode and the picker frames. Pass the lease the
// caller acquired; passing nil behaves exactly like SelectFullScreenList.
func SelectFullScreenListWithLease(ctx context.Context, terminal *Terminal, options FullScreenListOptions, lease ScreenLease) (FullScreenListResult, error) {
	return selectFullScreenListWithLease(ctx, terminal, options, os.Stdin, os.Stdout, lease)
}

func selectFullScreenList(ctx context.Context, terminal *Terminal, options FullScreenListOptions, reader io.Reader, writer io.Writer, leaseManaged bool) (FullScreenListResult, error) {
	return selectFullScreenListWithLeaseState(ctx, terminal, options, reader, writer, nil, leaseManaged)
}

func selectFullScreenListWithLease(ctx context.Context, terminal *Terminal, options FullScreenListOptions, reader io.Reader, writer io.Writer, lease ScreenLease) (FullScreenListResult, error) {
	return selectFullScreenListWithLeaseState(ctx, terminal, options, reader, writer, lease, lease != nil && lease.Active())
}

func selectFullScreenListWithLeaseState(ctx context.Context, terminal *Terminal, options FullScreenListOptions, reader io.Reader, writer io.Writer, lease ScreenLease, leaseManaged bool) (FullScreenListResult, error) {
	if terminal == nil || !terminal.SupportsANSI() || reader == nil || writer == nil {
		return FullScreenListResult{}, ErrFullScreenUnavailable
	}
	if len(options.Items) == 0 {
		return FullScreenListResult{Index: -1, Cancelled: true}, nil
	}
	stdinFile, _ := reader.(*os.File)
	if stdinFile == nil || !term.IsTerminal(int(stdinFile.Fd())) {
		return FullScreenListResult{}, ErrFullScreenUnavailable
	}
	if _, height := terminal.RefreshSize(); height < minFullScreenListHeight {
		return FullScreenListResult{}, fullScreenUnavailable("terminal height is too small", nil)
	}
	colorProfile := render.NoColorProfile()
	if terminal.driver != nil {
		// Resolve any bounded OSC defaults before raw mode starts so the probe
		// cannot compete with the list's key decoder for stdin.
		colorProfile = terminal.driver.ColorProfile().ColorProfile
	}

	rawState, err := term.MakeRaw(int(stdinFile.Fd()))
	if err != nil {
		return FullScreenListResult{}, fullScreenUnavailable("enable raw mode", err)
	}

	pending := takeInteractiveInputCarryover()
	defer func() { storeInteractiveInputCarryover(pending) }()

	hooks := fullScreenListLoopHooks{
		refreshSize: terminal.RefreshSize,
		writeFrame: func(frame string) error {
			return writeLeaseManagedFullScreenText(lease, writer, frame)
		},
		readKey: func(readCtx context.Context) (editorKey, bool, error) {
			return nextFullScreenListKey(readCtx, reader, &pending, stdinFile)
		},
		colorProfile: colorProfile,
	}
	lifecycle := fullScreenListLifecycle{
		writer: writer,
		restoreRaw: func() error {
			return term.Restore(int(stdinFile.Fd()), rawState)
		},
		leaseManaged: leaseManaged,
	}
	result, key, err := runFullScreenListSession(ctx, options, hooks, lifecycle)
	if err == nil && key.kind == editorKeyEnter && shouldDrainTrailingLineFeedAfterSubmit(key, false, nil) {
		drainTrailingLineFeedAfterCarriageReturn(ctx, reader, &pending, stdinFile)
	}
	return result, err
}

func runFullScreenListSession(ctx context.Context, options FullScreenListOptions, hooks fullScreenListLoopHooks, lifecycle fullScreenListLifecycle) (FullScreenListResult, editorKey, error) {
	if err := lifecycle.enter(); err != nil {
		cleanupErr := lifecycle.close()
		return FullScreenListResult{}, editorKey{}, fullScreenUnavailable("enter alternate screen", errors.Join(err, cleanupErr))
	}

	result, key, runErr := runFullScreenListLoop(ctx, options, hooks)
	cleanupErr := lifecycle.close()
	if cleanupErr != nil {
		return FullScreenListResult{}, editorKey{}, fullScreenUnavailable("restore terminal", errors.Join(runErr, cleanupErr))
	}
	return result, key, runErr
}

func runFullScreenListLoop(ctx context.Context, options FullScreenListOptions, hooks fullScreenListLoopHooks) (FullScreenListResult, editorKey, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if hooks.refreshSize == nil || hooks.writeFrame == nil || hooks.readKey == nil {
		return FullScreenListResult{}, editorKey{}, fullScreenUnavailable("full-screen loop is not configured", nil)
	}

	state := fullScreenListState{}
	dirty := true
	lastWidth, lastHeight := -1, -1
	lastNotifiedIndex := -2 // force initial OnSelectionChanged
	for {
		if err := ctx.Err(); err != nil {
			return FullScreenListResult{}, editorKey{}, err
		}
		width, height := hooks.refreshSize()
		if height < minFullScreenListHeight {
			return FullScreenListResult{}, editorKey{}, fullScreenUnavailable("terminal height is too small", nil)
		}
		matches := fullScreenListMatches(options.Items, state.query)
		state.clampToEnabled(options.Items, matches, fullScreenListPageSize(height))
		if len(matches) > 0 {
			curIdx := matches[state.selected]
			if curIdx != lastNotifiedIndex {
				lastNotifiedIndex = curIdx
				if options.OnSelectionChanged != nil {
					options.OnSelectionChanged(curIdx)
				}
				// Preview may depend on selection; force redraw.
				dirty = true
			}
		}
		if dirty || width != lastWidth || height != lastHeight {
			frame := renderFullScreenListFrameWithProfile(
				options, state, matches, width, height, hooks.colorProfile,
			)
			if err := hooks.writeFrame(frame); err != nil {
				return FullScreenListResult{}, editorKey{}, fullScreenUnavailable("write frame", err)
			}
			lastWidth, lastHeight = width, height
			dirty = false
		}

		key, ok, err := hooks.readKey(ctx)
		if err != nil {
			return FullScreenListResult{}, editorKey{}, err
		}
		if !ok {
			continue
		}
		result, done := applyFullScreenListKey(&state, key, options.Items, matches, height)
		if done {
			if result.Cancelled {
				if options.OnCancel != nil {
					options.OnCancel()
				}
				return result, key, nil
			}
			if options.OnConfirm != nil && result.Index >= 0 {
				if confErr := options.OnConfirm(result.Index); confErr != nil {
					// Keep list open; surface error in subtitle if possible.
					if options.Subtitle == "" {
						options.Subtitle = confErr.Error()
					} else {
						options.Subtitle = confErr.Error()
					}
					dirty = true
					continue
				}
			}
			return result, key, nil
		}
		dirty = true
	}
}

func nextFullScreenListKey(ctx context.Context, reader io.Reader, pending *[]byte, stdinFile *os.File) (editorKey, bool, error) {
	pollCtx, cancel := context.WithTimeout(ctx, fullScreenListPollInterval)
	defer cancel()
	key, ok, err := nextInteractiveKey(pollCtx, reader, pending, stdinFile)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return editorKey{}, false, nil
	}
	return key, ok, err
}

func (lifecycle fullScreenListLifecycle) enter() error {
	if lifecycle.leaseManaged {
		// The lease already entered the alternate screen atomically with the
		// primary flush suspension; writing the sequence again would tear the
		// current alternate buffer.
		return nil
	}
	return writeFullScreenSequences(lifecycle.writer,
		"\x1b[?1049h",
		"\x1b[r",
		"\x1b[?25l",
		"\x1b[2J",
		"\x1b[H",
	)
}

func (lifecycle fullScreenListLifecycle) close() error {
	var writeErr error
	if !lifecycle.leaseManaged {
		// The lease will emit the exit sequence during Release together with
		// the primary repaint; only stdin raw mode is restored here.
		writeErr = writeFullScreenSequences(lifecycle.writer,
			"\x1b[?25h",
			"\x1b[r",
			"\x1b[?1049l",
		)
	}
	var rawErr error
	if lifecycle.restoreRaw != nil {
		rawErr = lifecycle.restoreRaw()
	}
	return errors.Join(writeErr, rawErr)
}

func writeFullScreenSequences(writer io.Writer, sequences ...string) error {
	var writeErr error
	for _, sequence := range sequences {
		writeErr = errors.Join(writeErr, writeFullScreenText(writer, sequence))
	}
	return writeErr
}

func writeFullScreenText(writer io.Writer, value string) error {
	written, err := WriteTerminalText(writer, value)
	if err == nil && written != len(value) {
		err = io.ErrShortWrite
	}
	return err
}

func fullScreenUnavailable(operation string, err error) error {
	if err == nil {
		return fmt.Errorf("%w: %s", ErrFullScreenUnavailable, operation)
	}
	return fmt.Errorf("%w: %s: %v", ErrFullScreenUnavailable, operation, err)
}

func applyFullScreenListKey(state *fullScreenListState, key editorKey, items []FullScreenListItem, matches []int, height int) (FullScreenListResult, bool) {
	if state == nil {
		return FullScreenListResult{Index: -1, Cancelled: true}, true
	}
	pageSize := fullScreenListPageSize(height)
	if state.searching {
		switch key.kind {
		case editorKeyRune:
			if key.r >= 32 && key.r != 127 {
				state.query += string(key.r)
				state.selected, state.offset = 0, 0
			}
			return FullScreenListResult{}, false
		case editorKeyBackspace, editorKeyDelete:
			state.query = trimLastRune(state.query)
			state.selected, state.offset = 0, 0
			return FullScreenListResult{}, false
		case editorKeyCancelPopup:
			if state.query != "" {
				state.query = ""
				state.selected, state.offset = 0, 0
			} else {
				state.searching = false
			}
			return FullScreenListResult{}, false
		}
	}

	switch key.kind {
	case editorKeyEnter:
		if len(matches) == 0 {
			return FullScreenListResult{}, false
		}
		itemIndex := matches[state.selected]
		if fullScreenListItemDisabled(items, itemIndex) {
			// Current/disabled rows stay visible for confirmation but are not
			// selectable targets. Keep the list open so callers can cancel.
			return FullScreenListResult{}, false
		}
		return FullScreenListResult{Index: itemIndex}, true
	case editorKeyCancelPopup, editorKeyInterrupt, editorKeyEOF:
		return FullScreenListResult{Index: -1, Cancelled: true}, true
	case editorKeyUp:
		moveFullScreenListSelection(state, items, matches, -1)
	case editorKeyDown:
		moveFullScreenListSelection(state, items, matches, 1)
	case editorKeyPageUp, editorKeyLeft:
		state.selected = max(0, state.selected-pageSize)
		state.snapToEnabled(items, matches, -1)
	case editorKeyPageDown, editorKeyRight:
		state.selected = min(len(matches)-1, state.selected+pageSize)
		state.snapToEnabled(items, matches, 1)
	case editorKeyHome:
		state.selected = 0
		state.snapToEnabled(items, matches, 1)
	case editorKeyEnd:
		state.selected = len(matches) - 1
		state.snapToEnabled(items, matches, -1)
	case editorKeyRune:
		switch key.r {
		case 'q', 'Q':
			return FullScreenListResult{Index: -1, Cancelled: true}, true
		case 'j':
			moveFullScreenListSelection(state, items, matches, 1)
		case 'k':
			moveFullScreenListSelection(state, items, matches, -1)
		case 'g':
			state.selected = 0
			state.snapToEnabled(items, matches, 1)
		case 'G':
			state.selected = len(matches) - 1
			state.snapToEnabled(items, matches, -1)
		case '/':
			state.searching = true
		default:
			// 直接输入即搜索：非导航可打印字符立即进入搜索模式，
			// 恢复 legacy picker “输入关键词即过滤”的体验。搜索以
			// j/k/g/G/q 开头的词时先按 / 再输入即可。
			if key.r >= 32 && key.r != 127 {
				state.searching = true
				state.query = string(key.r)
				state.selected, state.offset = 0, 0
			}
		}
	}
	state.clampToEnabled(items, matches, pageSize)
	return FullScreenListResult{}, false
}

func (state *fullScreenListState) clamp(count, pageSize int) {
	if state == nil {
		return
	}
	if count <= 0 {
		state.selected, state.offset = 0, 0
		return
	}
	if state.selected < 0 {
		state.selected = count - 1
	}
	if state.selected >= count {
		state.selected = 0
	}
	if pageSize < 1 {
		pageSize = 1
	}
	if state.selected < state.offset {
		state.offset = state.selected
	}
	if state.selected >= state.offset+pageSize {
		state.offset = state.selected - pageSize + 1
	}
	maxOffset := count - pageSize
	if maxOffset < 0 {
		maxOffset = 0
	}
	if state.offset > maxOffset {
		state.offset = maxOffset
	}
}

func (state *fullScreenListState) clampToEnabled(items []FullScreenListItem, matches []int, pageSize int) {
	if state == nil {
		return
	}
	state.clamp(len(matches), pageSize)
	state.snapToEnabled(items, matches, 1)
	state.clamp(len(matches), pageSize)
}

func (state *fullScreenListState) snapToEnabled(items []FullScreenListItem, matches []int, direction int) {
	if state == nil || len(matches) == 0 {
		return
	}
	if direction == 0 {
		direction = 1
	}
	if !fullScreenListItemDisabled(items, matches[state.selected]) {
		return
	}
	for step := 1; step < len(matches); step++ {
		for _, dir := range []int{direction, -direction} {
			candidate := state.selected + dir*step
			if candidate < 0 || candidate >= len(matches) {
				continue
			}
			if !fullScreenListItemDisabled(items, matches[candidate]) {
				state.selected = candidate
				return
			}
		}
	}
}

func moveFullScreenListSelection(state *fullScreenListState, items []FullScreenListItem, matches []int, delta int) {
	if state == nil || len(matches) == 0 || delta == 0 {
		return
	}
	start := state.selected
	for step := 0; step < len(matches); step++ {
		state.selected += delta
		if state.selected < 0 {
			state.selected = len(matches) - 1
		}
		if state.selected >= len(matches) {
			state.selected = 0
		}
		if !fullScreenListItemDisabled(items, matches[state.selected]) {
			return
		}
		if state.selected == start {
			return
		}
	}
}

func fullScreenListItemDisabled(items []FullScreenListItem, index int) bool {
	if index < 0 || index >= len(items) {
		return true
	}
	return items[index].Disabled
}

func fullScreenListPageSize(height int) int {
	pageSize := height - fullScreenListPreviewRows(height) - 6
	if pageSize < 1 {
		return 1
	}
	return pageSize
}

func fullScreenListPreviewRows(height int) int {
	switch {
	case height >= 18:
		return 4
	case height >= 14:
		return 3
	case height >= 10:
		return 2
	default:
		return 1
	}
}

func trimLastRune(value string) string {
	if value == "" {
		return ""
	}
	_, size := utf8.DecodeLastRuneInString(value)
	if size <= 0 {
		return ""
	}
	return value[:len(value)-size]
}

func renderFullScreenListFrameWithProfile(
	options FullScreenListOptions,
	state fullScreenListState,
	matches []int,
	width, height int,
	profile render.ColorProfile,
) string {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	if height < minFullScreenListHeight {
		return renderCompactFullScreenListFrame(options, state, matches, width, height)
	}
	pageSize := fullScreenListPageSize(height)
	previewRows := fullScreenListPreviewRows(height)
	state.clamp(len(matches), pageSize)
	lines := make([]fullScreenFrameLine, height)

	title := strings.TrimSpace(options.Title)
	if title == "" {
		title = "选择项目"
	}
	lines[0].text = "  " + title
	subtitle := strings.TrimSpace(options.Subtitle)
	if subtitle == "" {
		subtitle = fmt.Sprintf("共 %d 项", len(options.Items))
	}
	if state.query != "" || state.searching {
		query := state.query
		if query == "" {
			query = "输入关键词"
		}
		subtitle += fmt.Sprintf("  |  搜索: %s  |  %d 个结果", query, len(matches))
	}
	lines[1].text = "  " + subtitle
	lines[2].text = strings.Repeat("─", width)

	listStart := 3
	if len(matches) == 0 {
		lines[listStart].text = "  " + fullScreenListEmptyMessage(options)
	} else {
		end := min(state.offset+pageSize, len(matches))
		for visibleIndex := state.offset; visibleIndex < end; visibleIndex++ {
			itemIndex := matches[visibleIndex]
			selected := visibleIndex == state.selected
			row := listStart + visibleIndex - state.offset
			lines[row] = fullScreenFrameLine{
				text:         renderFullScreenListItem(options.Items[itemIndex], fullScreenListItemNumber(options.Items, matches, visibleIndex), selected, width),
				selected:     selected,
				preformatted: true,
			}
		}
	}

	detailRow := listStart + pageSize
	lines[detailRow].text = strings.Repeat("─", width)
	if len(matches) > 0 {
		itemIndex := matches[state.selected]
		item := options.Items[itemIndex]
		preview := strings.TrimSpace(item.Preview)
		if options.PreviewForItem != nil {
			if dyn := strings.TrimSpace(options.PreviewForItem(itemIndex)); dyn != "" {
				preview = dyn
			}
		}
		if preview == "" {
			preview = strings.TrimSpace(item.Title)
		}
		previewLines := wrapFullScreenText(preview, max(1, width-2), previewRows)
		// Preserve app-rendered SGR previews and explicit line boundaries.
		if strings.Contains(preview, "\n") || strings.ContainsRune(preview, '\x1b') {
			raw := strings.Split(preview, "\n")
			previewLines = raw
			if len(previewLines) > previewRows {
				previewLines = previewLines[:previewRows]
			}
			for i, pl := range previewLines {
				if strings.ContainsRune(pl, '\x1b') {
					previewLines[i] = fitFullScreenPreformattedTextWithProfile(
						pl, max(1, width-2), profile,
					)
				} else {
					previewLines[i] = fitFullScreenText(pl, max(1, width-2))
				}
			}
		}
		for index, previewLine := range previewLines {
			if detailRow+1+index >= height-2 {
				break
			}
			lines[detailRow+1+index].text = "  " + previewLine
			// Preformatted so ANSI in rich previews is not re-wrapped destructively.
			if strings.Contains(previewLine, "\x1b") {
				lines[detailRow+1+index].preformatted = true
			}
		}
	}
	position := fmt.Sprintf("%d/%d", state.selected+1, len(matches))
	if len(matches) == 0 {
		position = "0/0"
	}
	lines[height-2].text = fmt.Sprintf("  %s  ↑↓/j/k 移动  PgUp/PgDn 翻页  Home/End 首尾  输入字符搜索", position)
	if state.searching {
		lines[height-1].text = "  输入关键词进行筛选（模糊匹配）  Backspace 删除  Esc 清除/退出搜索  Enter " + fullScreenListConfirmLabel(options)
	} else {
		lines[height-1].text = "  Enter " + fullScreenListConfirmLabel(options) + "  Esc/q 取消"
	}

	var builder strings.Builder
	builder.WriteString("\x1b[H")
	for row, line := range lines {
		builder.WriteString("\x1b[2K")
		text := line.text
		if line.preformatted {
			text = fitFullScreenPreformattedTextWithProfile(text, width, profile)
		} else {
			text = fitFullScreenText(text, width)
		}
		if line.selected {
			builder.WriteString("\x1b[7m" + text + "\x1b[0m")
		} else {
			builder.WriteString(text)
		}
		if row < len(lines)-1 {
			builder.WriteString("\r\n")
		}
	}
	return builder.String()
}

func renderCompactFullScreenListFrame(options FullScreenListOptions, state fullScreenListState, matches []int, width, height int) string {
	state.clampToEnabled(options.Items, matches, 1)
	lines := make([]string, height)
	title := strings.TrimSpace(options.Title)
	if title == "" {
		title = "选择项目"
	}
	lines[0] = title
	if height > 1 {
		if len(matches) == 0 {
			lines[1] = fullScreenListEmptyMessage(options)
		} else {
			item := options.Items[matches[state.selected]]
			lines[1] = fmt.Sprintf("> %s %s", fullScreenListItemNumber(options.Items, matches, state.selected), item.Title)
		}
	}
	if height > 2 {
		lines[height-1] = "↑↓ 移动  Enter " + fullScreenListConfirmLabel(options) + "  Esc 取消"
	}
	var builder strings.Builder
	builder.WriteString("\x1b[H")
	for row, line := range lines {
		builder.WriteString("\x1b[2K")
		builder.WriteString(fitFullScreenText(line, width))
		if row < len(lines)-1 {
			builder.WriteString("\r\n")
		}
	}
	return builder.String()
}

func fullScreenListEmptyMessage(options FullScreenListOptions) string {
	if message := strings.TrimSpace(options.EmptyMessage); message != "" {
		return message
	}
	return "没有匹配项"
}

func fullScreenListConfirmLabel(options FullScreenListOptions) string {
	if label := strings.TrimSpace(options.ConfirmLabel); label != "" {
		return label
	}
	return "选择选中项"
}

// fullScreenListItemNumber returns the visible rank label for a matched row.
// Disabled rows use "[·]" so they do not consume selectable ranks; enabled rows
// are numbered 1..N among currently matched enabled items only. Selection still
// maps through the original item index stored in matches.
func fullScreenListItemNumber(items []FullScreenListItem, matches []int, visibleIndex int) string {
	if visibleIndex < 0 || visibleIndex >= len(matches) {
		return "[·]"
	}
	itemIndex := matches[visibleIndex]
	if fullScreenListItemDisabled(items, itemIndex) {
		return "[·]"
	}
	rank := 0
	for index := 0; index <= visibleIndex; index++ {
		if !fullScreenListItemDisabled(items, matches[index]) {
			rank++
		}
	}
	return fmt.Sprintf("[%d]", rank)
}

func renderFullScreenListItem(item FullScreenListItem, indexLabel string, selected bool, width int) string {
	marker := "  "
	if selected {
		marker = "> "
	}
	if item.Disabled {
		marker = "· "
	}
	number := strings.TrimSpace(indexLabel)
	if number == "" {
		number = "[·]"
	}
	number += " "
	detail := strings.TrimSpace(item.Detail)
	detailWidth := min(32, max(12, width/3))
	detail = fitFullScreenText(detail, detailWidth)
	titleWidth := width - DisplayWidth(marker) - DisplayWidth(number) - DisplayWidth(detail) - 3
	if titleWidth < 1 {
		titleWidth = 1
	}
	title := fitFullScreenText(strings.TrimSpace(item.Title), titleWidth)
	line := marker + number + padFullScreenText(title, titleWidth) + "   " + detail
	line = padFullScreenText(fitFullScreenText(line, width), width)
	return line
}

func fitFullScreenText(value string, width int) string {
	value = strings.Join(strings.Fields(SanitizeTerminalText(value)), " ")
	return truncateFullScreenText(value, width)
}

func fitFullScreenPreformattedTextWithProfile(
	value string,
	width int,
	profile render.ColorProfile,
) string {
	lines := render.ANSIToLines(value)
	if len(lines) == 0 {
		return ""
	}
	line := render.Truncate(lines[0], width, "…")
	return (render.ANSIBackend{Profile: profile}).Render(render.LinesDoc(line))
}

func wrapFullScreenText(value string, width, limit int) []string {
	if width < 1 || limit < 1 {
		return nil
	}
	remaining := strings.Join(strings.Fields(SanitizeTerminalText(value)), " ")
	if remaining == "" {
		return nil
	}
	lines := make([]string, 0, limit)
	for remaining != "" && len(lines) < limit {
		if DisplayWidth(remaining) <= width {
			lines = append(lines, remaining)
			break
		}
		if len(lines) == limit-1 {
			lines = append(lines, truncateFullScreenText(remaining, width))
			break
		}
		head, tail := splitFullScreenText(remaining, width)
		if head == "" {
			lines = append(lines, truncateFullScreenText(remaining, width))
			break
		}
		lines = append(lines, strings.TrimSpace(head))
		remaining = strings.TrimSpace(tail)
	}
	return lines
}

func splitFullScreenText(value string, width int) (string, string) {
	used, end := 0, 0
	for index, r := range value {
		runeWidth := DisplayWidth(string(r))
		if used+runeWidth > width {
			return value[:end], value[index:]
		}
		used += runeWidth
		_, size := utf8.DecodeRuneInString(value[index:])
		end = index + size
	}
	return value, ""
}

func truncateFullScreenText(value string, width int) string {
	if width <= 0 || DisplayWidth(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	target := width - 1
	var builder strings.Builder
	used := 0
	for _, r := range value {
		runeWidth := DisplayWidth(string(r))
		if used+runeWidth > target {
			break
		}
		builder.WriteRune(r)
		used += runeWidth
	}
	return builder.String() + "…"
}

func padFullScreenText(value string, width int) string {
	if padding := width - DisplayWidth(value); padding > 0 {
		return value + strings.Repeat(" ", padding)
	}
	return value
}
