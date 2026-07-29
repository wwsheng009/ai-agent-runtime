package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

func renderTestFullScreenListFrame(options FullScreenListOptions, state fullScreenListState, matches []int, width, height int) string {
	return renderFullScreenListFrameWithProfile(
		options, state, matches, width, height, render.TrueColorProfile(),
	)
}

func TestFullScreenListNavigationAndSelection(t *testing.T) {
	items := make([]FullScreenListItem, 12)
	for index := range items {
		items[index] = FullScreenListItem{Title: "session"}
	}
	matches := fullScreenListMatches(items, "")
	state := fullScreenListState{}

	if _, done := applyFullScreenListKey(&state, editorKey{kind: editorKeyDown}, items, matches, 12); done || state.selected != 1 {
		t.Fatalf("expected Down to select item 2, got selected=%d done=%v", state.selected, done)
	}
	if _, done := applyFullScreenListKey(&state, editorKey{kind: editorKeyPageDown}, items, matches, 12); done || state.selected != 5 {
		t.Fatalf("expected PageDown to move one visible page, got selected=%d done=%v", state.selected, done)
	}
	if _, done := applyFullScreenListKey(&state, editorKey{kind: editorKeyEnd}, items, matches, 12); done || state.selected != 11 {
		t.Fatalf("expected End to select the final item, got selected=%d done=%v", state.selected, done)
	}
	result, done := applyFullScreenListKey(&state, editorKey{kind: editorKeyEnter}, items, matches, 12)
	if !done || result.Cancelled || result.Index != 11 {
		t.Fatalf("expected Enter to select original item 11, got %#v done=%v", result, done)
	}
}

func TestFullScreenListSearchFiltersAndKeepsOriginalIndex(t *testing.T) {
	items := []FullScreenListItem{
		{Title: "Build release", SearchText: "session-one"},
		{Title: "Fix resume", SearchText: "session-two"},
		{Title: "Review docs", SearchText: "session-three"},
	}
	state := fullScreenListState{}
	all := fullScreenListMatches(items, "")
	_, _ = applyFullScreenListKey(&state, editorKey{kind: editorKeyRune, r: '/'}, items, all, 24)
	_, _ = applyFullScreenListKey(&state, editorKey{kind: editorKeyRune, r: 'r'}, items, all, 24)
	_, _ = applyFullScreenListKey(&state, editorKey{kind: editorKeyRune, r: 'e'}, items, all, 24)
	if !state.searching || state.query != "re" {
		t.Fatalf("expected active search query, got %#v", state)
	}

	matches := fullScreenListMatches(items, state.query)
	if len(matches) != 3 {
		t.Fatalf("expected title/detail search to return three matching items, got %v", matches)
	}
	state.query = "resume"
	matches = fullScreenListMatches(items, state.query)
	if len(matches) != 1 || matches[0] != 1 {
		t.Fatalf("expected filtered item to retain original index 1, got %v", matches)
	}
	result, done := applyFullScreenListKey(&state, editorKey{kind: editorKeyEnter}, items, matches, 24)
	if !done || result.Index != 1 {
		t.Fatalf("expected Enter to return original filtered index, got %#v done=%v", result, done)
	}
}

func TestFullScreenListSkipsDisabledItems(t *testing.T) {
	items := []FullScreenListItem{
		{Title: "当前 · Live title（不可选）", Disabled: true},
		{Title: "Previous session"},
		{Title: "Older session"},
	}
	matches := fullScreenListMatches(items, "")
	state := fullScreenListState{}
	state.clampToEnabled(items, matches, 8)
	if state.selected != 1 {
		t.Fatalf("expected initial selection to skip disabled current row, got %d", state.selected)
	}

	if _, done := applyFullScreenListKey(&state, editorKey{kind: editorKeyUp}, items, matches, 24); done || state.selected != 2 {
		t.Fatalf("expected Up to wrap past disabled row onto last enabled item, got selected=%d done=%v", state.selected, done)
	}
	state.selected = 0
	result, done := applyFullScreenListKey(&state, editorKey{kind: editorKeyEnter}, items, matches, 24)
	if done {
		t.Fatalf("expected Enter on disabled row to stay open, got %#v done=%v", result, done)
	}
	state.selected = 1
	result, done = applyFullScreenListKey(&state, editorKey{kind: editorKeyEnter}, items, matches, 24)
	if !done || result.Index != 1 {
		t.Fatalf("expected Enter on enabled row to select original index 1, got %#v done=%v", result, done)
	}
}

func TestRenderFullScreenListFrameUsesWholeViewport(t *testing.T) {
	options := FullScreenListOptions{
		Title:        "恢复历史会话",
		Subtitle:     "最近更新优先，共 2 个可恢复会话",
		EmptyMessage: "没有匹配的历史会话",
		ConfirmLabel: "恢复选中会话",
		Items: []FullScreenListItem{
			{Title: "First session", Detail: "3分钟前  2轮/4条", Preview: "First preview"},
			{Title: "Second session", Detail: "昨天  5轮/12条", Preview: "Second preview"},
		},
	}
	matches := fullScreenListMatches(options.Items, "")
	frame := renderTestFullScreenListFrame(options, fullScreenListState{selected: 1}, matches, 80, 16)
	if count := strings.Count(frame, "\x1b[2K"); count != 16 {
		t.Fatalf("expected all 16 viewport rows to be redrawn, got %d", count)
	}
	for _, expected := range []string{"恢复历史会话", "Second session", "Second preview", "Enter 恢复选中会话", "\x1b[7m"} {
		if !strings.Contains(frame, expected) {
			t.Fatalf("expected frame to contain %q, got %q", expected, frame)
		}
	}
}

func TestRenderFullScreenListFrameHandlesSmallViewport(t *testing.T) {
	options := FullScreenListOptions{
		Title: "恢复历史会话",
		Items: []FullScreenListItem{{
			Title: "A very long session title that must be truncated",
		}},
	}
	frame := renderTestFullScreenListFrame(options, fullScreenListState{}, []int{0}, 18, 4)
	if count := strings.Count(frame, "\x1b[2K"); count != 4 {
		t.Fatalf("expected compact renderer to stay within four rows, got %d", count)
	}
	for _, line := range strings.Split(strings.TrimPrefix(frame, "\x1b[H"), "\r\n") {
		plain := strings.TrimPrefix(line, "\x1b[2K")
		if width := DisplayWidth(plain); width > 18 {
			t.Fatalf("compact line exceeds viewport width: width=%d line=%q", width, plain)
		}
	}
}

func TestRunFullScreenListLoopRejectsInsufficientHeightBeforeRendering(t *testing.T) {
	writes := 0
	_, _, err := runFullScreenListLoop(context.Background(), FullScreenListOptions{
		Items: []FullScreenListItem{{Title: "hidden choice"}},
	}, fullScreenListLoopHooks{
		refreshSize: func() (int, int) { return 80, minFullScreenListHeight - 1 },
		writeFrame: func(string) error {
			writes++
			return nil
		},
		readKey: func(context.Context) (editorKey, bool, error) {
			return editorKey{kind: editorKeyEnter}, true, nil
		},
	})
	if !errors.Is(err, ErrFullScreenUnavailable) {
		t.Fatalf("expected insufficient height to reject full screen, got %v", err)
	}
	if writes != 0 {
		t.Fatalf("expected no frame for an unusable viewport, got %d writes", writes)
	}
}

func TestRenderFullScreenListFrameUsesConfigurableGenericCopy(t *testing.T) {
	options := FullScreenListOptions{
		Title:        "Choose a workspace",
		EmptyMessage: "No matching workspaces",
		ConfirmLabel: "open selected workspace",
	}
	frame := renderTestFullScreenListFrame(options, fullScreenListState{}, nil, 80, 12)
	for _, expected := range []string{"No matching workspaces", "Enter open selected workspace"} {
		if !strings.Contains(frame, expected) {
			t.Fatalf("expected generic frame to contain %q, got %q", expected, frame)
		}
	}
	for _, resumeSpecific := range []string{"历史会话", "Enter 恢复"} {
		if strings.Contains(frame, resumeSpecific) {
			t.Fatalf("did not expect resume-specific copy %q, got %q", resumeSpecific, frame)
		}
	}
}

func TestRenderFullScreenListFrameWrapsPreviewAcrossRows(t *testing.T) {
	options := FullScreenListOptions{Items: []FullScreenListItem{{
		Title:   "preview item",
		Preview: "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda",
	}}}
	wrapped := wrapFullScreenText(options.Items[0].Preview, 26, fullScreenListPreviewRows(14))
	if len(wrapped) < 2 {
		t.Fatalf("expected preview to wrap over multiple rows, got %v", wrapped)
	}
	frame := renderTestFullScreenListFrame(options, fullScreenListState{}, []int{0}, 28, 14)
	for _, line := range wrapped {
		if !strings.Contains(frame, line) {
			t.Fatalf("expected wrapped preview line %q in frame, got %q", line, frame)
		}
	}
}

func TestRunFullScreenListLoopRedrawsOnResizeWithoutKey(t *testing.T) {
	sizes := [][2]int{{80, 12}, {96, 16}}
	sizeIndex := 0
	frames := make([]string, 0, len(sizes))
	readCount := 0
	result, _, err := runFullScreenListLoop(context.Background(), FullScreenListOptions{
		Items: []FullScreenListItem{{Title: "resizable"}},
	}, fullScreenListLoopHooks{
		refreshSize: func() (int, int) {
			size := sizes[min(sizeIndex, len(sizes)-1)]
			sizeIndex++
			return size[0], size[1]
		},
		writeFrame: func(frame string) error {
			frames = append(frames, frame)
			return nil
		},
		readKey: func(context.Context) (editorKey, bool, error) {
			readCount++
			if readCount == 1 {
				return editorKey{}, false, nil
			}
			return editorKey{kind: editorKeyCancelPopup}, true, nil
		},
	})
	if err != nil || !result.Cancelled {
		t.Fatalf("expected resize tick followed by cancellation, result=%#v err=%v", result, err)
	}
	if len(frames) != 2 {
		t.Fatalf("expected initial and resized frames, got %d", len(frames))
	}
	if strings.Count(frames[0], "\x1b[2K") != 12 || strings.Count(frames[1], "\x1b[2K") != 16 {
		t.Fatalf("expected frames to track viewport heights, got %d and %d rows", strings.Count(frames[0], "\x1b[2K"), strings.Count(frames[1], "\x1b[2K"))
	}
}

func TestRunFullScreenListSessionRestoresTerminalOnWriteFailures(t *testing.T) {
	tests := []struct {
		name       string
		failAt     int
		restoreErr error
	}{
		{name: "enter alternate screen", failAt: 1},
		{name: "frame", failAt: 6},
		{name: "exit alternate screen", failAt: 9},
		{name: "raw restore", restoreErr: errors.New("restore raw failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := &failAtFullScreenWriter{failAt: test.failAt}
			restoreCalls := 0
			lifecycle := fullScreenListLifecycle{
				writer: writer,
				restoreRaw: func() error {
					restoreCalls++
					return test.restoreErr
				},
			}
			hooks := fullScreenListLoopHooks{
				refreshSize: func() (int, int) { return 80, 12 },
				writeFrame: func(frame string) error {
					return writeFullScreenText(writer, frame)
				},
				readKey: func(context.Context) (editorKey, bool, error) {
					return editorKey{kind: editorKeyCancelPopup}, true, nil
				},
			}
			_, _, err := runFullScreenListSession(context.Background(), FullScreenListOptions{
				Items: []FullScreenListItem{{Title: "item"}},
			}, hooks, lifecycle)
			if !errors.Is(err, ErrFullScreenUnavailable) {
				t.Fatalf("expected write or restore failure to return ErrFullScreenUnavailable, got %v", err)
			}
			if restoreCalls != 1 {
				t.Fatalf("expected raw mode to be restored exactly once, got %d", restoreCalls)
			}
			if !writer.saw("\x1b[?25h") || !writer.saw("\x1b[?1049l") {
				t.Fatalf("expected best-effort cursor and alternate-screen restore, writes=%q", writer.writes)
			}
		})
	}
}

type failAtFullScreenWriter struct {
	failAt int
	calls  int
	writes []string
}

func (writer *failAtFullScreenWriter) Write(value []byte) (int, error) {
	writer.calls++
	writer.writes = append(writer.writes, string(value))
	if writer.calls == writer.failAt {
		return 0, errors.New("test write failed")
	}
	return len(value), nil
}

func (writer *failAtFullScreenWriter) saw(value string) bool {
	for _, write := range writer.writes {
		if write == value {
			return true
		}
	}
	return false
}

func TestRenderFullScreenListFrameSanitizesDataBeforeAddingTrustedStyle(t *testing.T) {
	options := FullScreenListOptions{
		Title: "恢复\x1b[2J历史会话",
		Items: []FullScreenListItem{{
			Title:   "Session\x1b]0;owned\x07",
			Preview: "Preview\x1b[2J should stay visible",
		}},
	}
	frame := renderTestFullScreenListFrame(options, fullScreenListState{}, []int{0}, 80, 12)
	for _, unsafe := range []string{"\x1b[2J", "\x1b]0;owned\x07"} {
		if strings.Contains(frame, unsafe) {
			t.Fatalf("expected untrusted terminal sequence %q to be removed, got %q", unsafe, frame)
		}
	}
	if !strings.Contains(frame, "恢复历史会话") || !strings.Contains(frame, "Preview should stay visible") {
		t.Fatalf("expected sanitized data to remain readable, got %q", frame)
	}
	if !strings.Contains(frame, "\x1b[7m") || !strings.Contains(frame, "\x1b[0m") {
		t.Fatalf("expected renderer-owned selection style to remain, got %q", frame)
	}
}

func TestRenderFullScreenListFrameKeepsSafeRichPreviewSGR(t *testing.T) {
	rich := fitFullScreenPreformattedTextWithProfile(
		"\x1b[31mred\x1b[0m safe", 38, render.TrueColorProfile(),
	)
	if !strings.Contains(rich, "\x1b[31m") {
		t.Fatalf("preformatted helper lost SGR: %q", rich)
	}
	options := FullScreenListOptions{
		Title: "主题",
		Items: []FullScreenListItem{{
			Title:   "dracula",
			Preview: "\x1b[31mred\x1b[0m\x1b[2J safe",
		}},
	}
	frame := renderTestFullScreenListFrame(options, fullScreenListState{}, []int{0}, 40, 12)
	if !strings.Contains(frame, "\x1b[31m") || !strings.Contains(frame, "red") {
		t.Fatalf("safe preview SGR was lost: %q", frame)
	}
	if strings.Contains(frame, "\x1b[2J") {
		t.Fatalf("dangerous preview CSI leaked: %q", frame)
	}
}

func TestFitFullScreenPreformattedTextUsesNegotiatedColorDepth(t *testing.T) {
	rich := "\x1b[38;2;255;64;32mred\x1b[0m safe"

	plain := fitFullScreenPreformattedTextWithProfile(rich, 38, render.NoColorProfile())
	if strings.ContainsRune(plain, '\x1b') {
		t.Fatalf("no-color rich preview contains ESC: %q", plain)
	}
	if plain != "red safe" {
		t.Fatalf("no-color rich preview = %q, want %q", plain, "red safe")
	}

	ansi16 := fitFullScreenPreformattedTextWithProfile(rich, 38, render.ColorProfile{
		Enabled: true,
		Depth:   render.ColorANSI16,
	})
	if !strings.ContainsRune(ansi16, '\x1b') {
		t.Fatalf("ANSI-16 rich preview has no SGR: %q", ansi16)
	}
	if strings.Contains(ansi16, "\x1b[38;2;") || strings.Contains(ansi16, "\x1b[38;5;") {
		t.Fatalf("ANSI-16 rich preview contains higher-depth color: %q", ansi16)
	}
	if render.ANSIToPlain(ansi16) != "red safe" {
		t.Fatalf("ANSI-16 rich preview changed plain content: %q", ansi16)
	}
}

func TestDecodeInteractiveKeyRecognizesPageNavigation(t *testing.T) {
	for sequence, expected := range map[string]editorKeyKind{
		"\x1b[5~": editorKeyPageUp,
		"\x1b[6~": editorKeyPageDown,
	} {
		decoded, ok := decodeInteractiveKey([]byte(sequence))
		if !ok || decoded.key.kind != expected || decoded.consumed != len(sequence) {
			t.Fatalf("decode %q: got %#v ok=%v", sequence, decoded, ok)
		}
	}
}

func TestFullScreenListNumbersEnabledMatchesContiguously(t *testing.T) {
	options := FullScreenListOptions{
		Title: "恢复历史会话",
		Items: []FullScreenListItem{
			{Title: "当前 · Live title（不可选）", Disabled: true, Detail: "1分钟前"},
			{Title: "Alpha history", Detail: "3分钟前", SearchText: "alpha"},
			{Title: "Beta skipped", Detail: "4分钟前", SearchText: "beta"},
			{Title: "Gamma history", Detail: "5分钟前", SearchText: "gamma"},
		},
	}

	allMatches := fullScreenListMatches(options.Items, "")
	if got := fullScreenListItemNumber(options.Items, allMatches, 0); got != "[·]" {
		t.Fatalf("expected disabled current row to use [·], got %q", got)
	}
	if got := fullScreenListItemNumber(options.Items, allMatches, 1); got != "[1]" {
		t.Fatalf("expected first history row to start at [1], got %q", got)
	}
	if got := fullScreenListItemNumber(options.Items, allMatches, 3); got != "[3]" {
		t.Fatalf("expected third enabled row to be [3], got %q", got)
	}

	frame := renderTestFullScreenListFrame(options, fullScreenListState{selected: 1}, allMatches, 80, 16)
	for _, expected := range []string{"[·] 当前 · Live title（不可选）", "[1] Alpha history", "[2] Beta skipped", "[3] Gamma history"} {
		if !strings.Contains(frame, expected) {
			t.Fatalf("expected frame to contain %q, got %q", expected, frame)
		}
	}
	if strings.Contains(frame, "[4] ") {
		t.Fatalf("did not expect original item index numbering after disabled current row, got %q", frame)
	}

	filtered := fullScreenListMatches(options.Items, "history")
	if len(filtered) != 2 || filtered[0] != 1 || filtered[1] != 3 {
		t.Fatalf("expected filtered matches to keep original indexes [1 3], got %v", filtered)
	}
	if got := fullScreenListItemNumber(options.Items, filtered, 0); got != "[1]" {
		t.Fatalf("expected filtered first enabled match to renumber as [1], got %q", got)
	}
	if got := fullScreenListItemNumber(options.Items, filtered, 1); got != "[2]" {
		t.Fatalf("expected filtered second enabled match to renumber as [2], got %q", got)
	}

	state := fullScreenListState{selected: 1}
	result, done := applyFullScreenListKey(&state, editorKey{kind: editorKeyEnter}, options.Items, filtered, 24)
	if !done || result.Index != 3 {
		t.Fatalf("expected Enter to return original item index 3, got %#v done=%v", result, done)
	}
}
