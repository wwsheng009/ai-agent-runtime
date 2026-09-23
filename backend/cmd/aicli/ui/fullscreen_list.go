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
	// Leading renders before the title in its own aligned column, for example
	// "1天前 【2轮/10条】". Empty keeps the historical title-first layout.
	Leading    string
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
	// OnDelete, when non-nil, enables the delete keys (x/X/Delete) on the
	// highlighted row. The list itself never mutates items or writes state:
	// pressing a delete key closes the list with DeleteRequested=true so the
	// caller can confirm the action, persist it (for example to the config
	// file), and reopen the list with refreshed items.
	OnDelete func(index int) error
	// PreviewForItem, when set, replaces item.Preview for the highlighted row.
	PreviewForItem func(index int) string

	// FreeTextMode switches the picker into single-value input: the item list is
	// not rendered and typed characters edit FreeTextValue (prefilled when set).
	// Enter submits the trimmed text through OnConfirmText -- a non-nil error
	// keeps the input open and is shown in the subtitle (inline validation), so
	// callers can reuse their write-path validator without losing the user's
	// input. Esc/q cancels. Callers use this for fields whose value has no
	// catalog (for example routing max_tokens/temperature/thinking_effort).
	FreeTextMode  bool
	FreeTextValue string
	FreeTextHint  string
	// OnConfirmText validates/consumes the submitted text. It runs only in
	// FreeTextMode; nil accepts any text.
	OnConfirmText func(text string) error

	// PageLoader, when set, turns Items into a lazily extended window: the list
	// keeps the caller's first page and asks the loader for the next page only
	// when the user reaches the end of what is loaded, and for a fresh first
	// page when the debounced search query changes. Large catalogs therefore
	// never need to be materialized up front, and the result index always names
	// a row the loader has already produced (index-aligned with the loaded
	// window, exactly like a pre-built Items slice).
	PageLoader FullScreenListPageLoader
	// SearchDebounce overrides the quiet period before a changed query reloads
	// the first page (default 180ms). Zero keeps the default.
	SearchDebounce time.Duration
}

// FullScreenListResult identifies the selected original item index.
type FullScreenListResult struct {
	Index     int
	Cancelled bool
	// DeleteRequested reports that the user pressed a delete key (x/X/Delete)
	// on the highlighted enabled row. Index names the row. The list has
	// already closed; the caller owns confirmation and persistence.
	DeleteRequested bool
	// Text carries the submitted value in FreeTextMode (trimmed). Index is -1
	// for text submissions.
	Text string
}

type fullScreenListState struct {
	selected  int
	offset    int
	query     string
	searching bool
	// Paging status, filled by the loop for the renderer when PageLoader is
	// active. Read-only for navigation and filtering.
	paging  bool
	loaded  int
	hasMore bool
	loadErr string
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
	// now is the clock used for the search debounce; nil means time.Now.
	now func() time.Time
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
	// A paged list starts empty on purpose: its first page arrives inside the
	// loop, so an empty Items slice must not be mistaken for "nothing to pick".
	if len(options.Items) == 0 && !options.FreeTextMode && options.PageLoader == nil {
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
	if options.FreeTextMode {
		// Free-text mode reuses the query buffer as the value being edited; the
		// item list and its filtering are bypassed entirely.
		state.query = options.FreeTextValue
	}
	now := hooks.now
	if now == nil {
		now = time.Now
	}
	// A paged list owns its window: page 0 is either adopted from the caller's
	// preload (so opening the list does not repeat the query) or fetched here
	// before the first frame.
	var pager *fullScreenListPager
	if !options.FreeTextMode && options.PageLoader != nil {
		pager = newFullScreenListPager(options.PageLoader, options.SearchDebounce, now)
		pager.noteQuery(state.query)
		if pager.seed() {
			options.Items = pager.itemsSnapshot()
		} else if loadErr := pager.load(ctx, 0, state.query); loadErr == nil {
			options.Items = pager.itemsSnapshot()
		}
	}
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
		if pager != nil {
			// Debounced search: the query the user typed is reloaded from the
			// loader (page 0) once the quiet window closes, so filtering stays
			// instant on the loaded window and complete once it settles.
			if query, ok := pager.dueQuery(); ok {
				if loadErr := pager.load(ctx, 0, query); loadErr == nil {
					options.Items = pager.itemsSnapshot()
					state.selected, state.offset = 0, 0
				}
				dirty = true
			}
		}
		var matches []int
		if !options.FreeTextMode {
			matches = fullScreenListMatches(options.Items, state.query)
			state.clampToEnabled(options.Items, matches, fullScreenListPageSize(height))
		}
		if pager != nil {
			state.paging = true
			state.loaded = len(options.Items)
			state.hasMore = pager.hasMore
			state.loadErr = ""
			if pager.lastErr != nil {
				state.loadErr = pager.lastErr.Error()
			}
		}
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
		result, done := FullScreenListResult{}, false
		if options.FreeTextMode {
			result, done = applyFullScreenFreeTextKey(&state, key)
		} else {
			result, done = applyFullScreenListKey(&state, key, options.Items, matches, height)
		}
		if done {
			if options.FreeTextMode {
				if options.OnConfirmText != nil {
					if confErr := options.OnConfirmText(result.Text); confErr != nil {
						// Inline validation feedback: keep the input open with the
						// reason in the subtitle so the value can be corrected
						// without re-entering the panel.
						options.Subtitle = confErr.Error()
						dirty = true
						continue
					}
				}
				return result, key, nil
			}
			if result.DeleteRequested {
				if options.OnDelete == nil {
					// Delete keys are only meaningful when the caller opted in.
					// Keep the list open so unconfigured callers are unaffected.
					dirty = true
					continue
				}
				return result, key, nil
			}
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
		if pager != nil {
			pager.noteQuery(state.query)
			if pager.shouldPrefetch(state, len(matches)) {
				// Lazy next page: only when the user reached the end of the
				// loaded window, and always bounded by the loader.
				if loadErr := pager.load(ctx, len(options.Items), pager.loadedQuery); loadErr == nil {
					pager.countAutoLoad(len(matches))
					options.Items = pager.itemsSnapshot()
				}
				dirty = true
				continue
			}
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
		case 'x', 'X':
			if len(matches) > 0 {
				return FullScreenListResult{Index: matches[state.selected], DeleteRequested: true}, true
			}
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
	case editorKeyDelete:
		// The Delete key is a delete action outside search mode; inside search
		// mode it is handled above as "trim query character".
		if len(matches) > 0 {
			return FullScreenListResult{Index: matches[state.selected], DeleteRequested: true}, true
		}
	}
	state.clampToEnabled(items, matches, pageSize)
	return FullScreenListResult{}, false
}

// applyFullScreenFreeTextKey handles one key in FreeTextMode: printable runes
// edit the value, Backspace/Delete drop the last rune, Enter submits the trimmed
// text, Esc/q/interrupt/EOF cancels. Navigation keys are inert (there is no
// list to move through).
func applyFullScreenFreeTextKey(state *fullScreenListState, key editorKey) (FullScreenListResult, bool) {
	if state == nil {
		return FullScreenListResult{Index: -1, Cancelled: true}, true
	}
	switch key.kind {
	case editorKeyRune:
		if key.r >= 32 && key.r != 127 {
			state.query += string(key.r)
		}
	case editorKeyBackspace, editorKeyDelete:
		state.query = trimLastRune(state.query)
	case editorKeyEnter:
		return FullScreenListResult{Index: -1, Text: strings.TrimSpace(state.query)}, true
	case editorKeyCancelPopup, editorKeyInterrupt, editorKeyEOF:
		return FullScreenListResult{Index: -1, Cancelled: true}, true
	}
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
	if options.FreeTextMode {
		return renderFullScreenFreeTextFrame(options, state, width, height, profile)
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
	if state.paging {
		// Paged catalogs report how much of the result set is materialized
		// instead of a total that would require scanning everything.
		status := fmt.Sprintf("已加载 %d 项", state.loaded)
		if state.hasMore {
			status += "（滚动到底自动加载更多）"
		}
		subtitle += "  |  " + status
	}
	if state.loadErr != "" {
		subtitle += "  |  加载失败: " + state.loadErr
	}
	lines[1].text = "  " + subtitle
	lines[2].text = strings.Repeat("─", width)

	listStart := 3
	if len(matches) == 0 {
		lines[listStart].text = "  " + fullScreenListEmptyMessage(options)
	} else {
		end := min(state.offset+pageSize, len(matches))
		leadingWidth := fullScreenListLeadingWidth(options.Items, matches, state.offset, end)
		for visibleIndex := state.offset; visibleIndex < end; visibleIndex++ {
			itemIndex := matches[visibleIndex]
			selected := visibleIndex == state.selected
			row := listStart + visibleIndex - state.offset
			lines[row] = fullScreenFrameLine{
				text:         renderFullScreenListItem(options.Items[itemIndex], fullScreenListItemNumber(options.Items, matches, visibleIndex), selected, width, leadingWidth),
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
		// Preserve app-rendered SGR previews and explicit line boundaries. Plain
		// multi-line previews (metadata blocks) wrap into the remaining rows so a
		// narrow terminal shows the whole line instead of truncating it.
		if strings.Contains(preview, "\n") || strings.ContainsRune(preview, '\x1b') {
			previewLines = nil
			for _, raw := range strings.Split(preview, "\n") {
				if len(previewLines) >= previewRows {
					break
				}
				if strings.ContainsRune(raw, '\x1b') {
					previewLines = append(previewLines, fitFullScreenPreformattedTextWithProfile(
						raw, max(1, width-2), profile,
					))
					continue
				}
				previewLines = append(previewLines, wrapFullScreenText(raw, max(1, width-2), previewRows-len(previewLines))...)
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

	return composeFullScreenFrame(lines, width, profile)
}

// composeFullScreenFrame renders pre-sized frame lines into one alternate-screen
// update (clear-line per row, optional inverse-video row) so the list and the
// single-value input frames share identical writer behavior.
func composeFullScreenFrame(lines []fullScreenFrameLine, width int, profile render.ColorProfile) string {
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

// renderFullScreenFreeTextFrame draws the single-value input view. Inline
// validation errors reach this frame through options.Subtitle: the loop rewrites
// the subtitle and redraws when OnConfirmText returns an error, so the layout
// always keeps a dedicated subtitle row above the input.
func renderFullScreenFreeTextFrame(options FullScreenListOptions, state fullScreenListState, width, height int, profile render.ColorProfile) string {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	lines := make([]fullScreenFrameLine, height)
	title := strings.TrimSpace(options.Title)
	if title == "" {
		title = "输入值"
	}
	lines[0].text = "  " + title
	// Validation errors arrive through the subtitle and can be long (for example
	// the parser's full key path in a rollback reason), so the subtitle spans up
	// to two rows; the input row stays at a fixed position regardless of length.
	if height > 1 {
		subtitle := strings.TrimSpace(options.Subtitle)
		if subtitle == "" {
			subtitle = "输入内容后按 Enter 确认，Esc 取消"
		}
		for index, line := range wrapFullScreenText(subtitle, max(1, width-2), 2) {
			row := 1 + index
			if row >= height-3 {
				break
			}
			lines[row].text = "  " + line
		}
	}
	if height > 3 {
		lines[3].text = strings.Repeat("─", width)
	}
	if height > 4 {
		// The alternate screen hides the hardware cursor; the block marks the
		// insertion point and makes the prefill/typed value visible.
		lines[4].text = "  > " + state.query + "▌"
	}
	if hint := strings.TrimSpace(options.FreeTextHint); hint != "" {
		if rows := max(0, height-2-5); rows > 0 {
			for index, line := range wrapFullScreenText(hint, max(1, width-2), rows) {
				lines[5+index].text = "  " + line
			}
		}
	}
	if height > 5 {
		lines[height-2].text = fmt.Sprintf("  已输入 %d 字符", utf8.RuneCountInString(state.query))
	}
	if height > 4 {
		lines[height-1].text = "  Enter " + fullScreenListConfirmLabel(options) + "  Backspace 删除  Esc 取消"
	}
	return composeFullScreenFrame(lines, width, profile)
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

// fullScreenListLeadingMaxWidth caps the aligned leading column so a long
// metadata prefix can never crowd the title out of the row.
const fullScreenListLeadingMaxWidth = 24

// fullScreenListLeadingWidth returns the aligned width of the leading metadata
// column for the rows currently on screen, so titles line up within a frame
// without rows outside the visible window shifting the column while scrolling.
func fullScreenListLeadingWidth(items []FullScreenListItem, matches []int, start, end int) int {
	width := 0
	for visibleIndex := start; visibleIndex < end && visibleIndex < len(matches); visibleIndex++ {
		itemIndex := matches[visibleIndex]
		if itemIndex < 0 || itemIndex >= len(items) {
			continue
		}
		leading := strings.TrimSpace(items[itemIndex].Leading)
		if leading == "" {
			continue
		}
		if itemWidth := DisplayWidth(leading); itemWidth > width {
			width = itemWidth
		}
	}
	if width > fullScreenListLeadingMaxWidth {
		return fullScreenListLeadingMaxWidth
	}
	return width
}

func renderFullScreenListItem(item FullScreenListItem, indexLabel string, selected bool, width, leadingWidth int) string {
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
	leading := ""
	if leadingWidth > 0 {
		leading = padFullScreenText(fitFullScreenText(strings.TrimSpace(item.Leading), leadingWidth), leadingWidth) + " "
	}
	titleWidth := width - DisplayWidth(marker) - DisplayWidth(number) - DisplayWidth(leading) - DisplayWidth(detail) - 3
	if titleWidth < 1 {
		titleWidth = 1
	}
	title := fitFullScreenText(strings.TrimSpace(item.Title), titleWidth)
	line := marker + number + leading + padFullScreenText(title, titleWidth) + "   " + detail
	if leadingWidth > 0 {
		// A leading column exists to keep titles aligned, so the historical
		// whitespace normalization (which collapses the row into a plain
		// suffix chain) is skipped; every part is sanitized above already.
		return padFullScreenText(truncateFullScreenText(SanitizeTerminalText(line), width), width)
	}
	return padFullScreenText(fitFullScreenText(line, width), width)
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
		runeWidth := render.RuneWidth(r)
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
		runeWidth := render.RuneWidth(r)
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
