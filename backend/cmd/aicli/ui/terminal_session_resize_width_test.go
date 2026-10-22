package ui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// 需求：终端宽度变化后，宿主会对已交付行做 reflow，旧 resident 行模型不再
// 可信。此后历史交付必须改为 append-only（HandoffPlan），既不能按旧绝对行号
// 覆盖可能仍然存在的行，也不能用旧宽度的文本前缀去裁剪“已交付”判断。
// 判据：每个语义标记在 scrollback + 可见屏幕中出现且只出现一次。
func TestTerminalSessionWidthResizeKeepsHistoryExactlyOnce(t *testing.T) {
	const (
		height       = 6
		outputBottom = 3
	)
	initialWidth := 28
	resizedWidth := 18

	var output bytes.Buffer
	session := NewTerminalSession(&output)
	if result := session.Flush(terminalSessionPlan(1, initialWidth, height, outputBottom, LeaseState{})); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}

	first := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "WIDTH-ONE"}}},
		render.Line{Spans: []render.Span{{Text: "WIDTH-TWO"}}},
	)
	if result := session.CommitHistory(first); result.Err != nil || result.Deferred {
		t.Fatalf("initial history = %#v", result)
	}
	if !session.historyProjectionStarted {
		t.Fatal("history projection was not initialized")
	}

	// Width-only resize: the host reflows the delivered rows, so the session
	// must stop claiming the retained row list describes the region.
	if result := session.Flush(terminalSessionPlan(2, resizedWidth, height, outputBottom, LeaseState{})); result.Err != nil {
		t.Fatalf("width resize frame = %#v", result)
	}
	if !session.historyTailUnknown {
		t.Fatal("width resize did not invalidate the resident history row model")
	}
	// The textual dedup proof is keyed on the pre-reflow wrapping, so it must be
	// discarded; the resident row list is retained only for capacity accounting.
	if len(session.historyStreamTailRows) != 0 || len(session.historyTailCells) != 0 {
		t.Fatalf("width resize kept an unprovable dedup proof: stream=%d cells=%d",
			len(session.historyStreamTailRows), len(session.historyTailCells))
	}
	if len(session.historyTailRows) != 0 {
		t.Fatalf("width resize kept %d pre-reflow resident rows", len(session.historyTailRows))
	}

	second := terminalSessionCommit(2,
		render.Line{Spans: []render.Span{{Text: "WIDTH-THREE"}}},
		render.Line{Spans: []render.Span{{Text: "WIDTH-FOUR"}}},
	)
	second.Token = 2
	if result := session.CommitHistory(second); result.Err != nil || result.Deferred {
		t.Fatalf("post-resize history = %#v", result)
	}
	if !session.historyTailUnknown {
		// 2 行追加不足以替换整个 resident 区（容量 3），flag 仍应保持，避免
		// 过早恢复“绝对行可写”的假设。
		t.Fatalf("resident region was replaced by 2 rows but capacity is %d", outputBottom)
	}

	raw := output.String()
	for _, marker := range []string{"WIDTH-ONE", "WIDTH-TWO", "WIDTH-THREE", "WIDTH-FOUR"} {
		if count := strings.Count(raw, marker); count != 1 {
			t.Fatalf("history marker %q crossed the writer %d times, want exactly once", marker, count)
		}
	}

	// 按新宽度重放物理字节：已交付历史 + viewport 不得出现重复语义行。
	screen := vt.NewScreen(resizedWidth, height)
	screen.Feed(raw)
	counts := map[string]int{}
	for _, line := range screen.ScrollbackLines() {
		counts[strings.TrimSpace(line)]++
	}
	for row := 1; row <= screen.Height(); row++ {
		counts[strings.TrimSpace(screen.Line(row))]++
	}
	for _, marker := range []string{"WIDTH-ONE", "WIDTH-TWO", "WIDTH-THREE", "WIDTH-FOUR"} {
		if counts[marker] != 1 {
			t.Fatalf("semantic marker %q count=%d, want 1\n%s", marker, counts[marker], screen.Dump())
		}
	}
}

// 需求：终端变矮（viewport 上移）时，旧实现直接返回空转换，viewpoint 重画
// 会覆盖仍然 resident 的历史行，随后 resident 模型继续按旧行数工作。修复后
// 必须切换到 append-only：不得向视口上方写内容，且追加的历史行保持唯一。
func TestTerminalSessionHeightShrinkDoesNotOverwriteResidentHistory(t *testing.T) {
	const (
		width = 24
	)
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	if result := session.Flush(terminalSessionPlan(1, width, 8, 5, LeaseState{})); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}
	if result := session.CommitHistory(terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "SHRINK-ONE"}}},
		render.Line{Spans: []render.Span{{Text: "SHRINK-TWO"}}},
	)); result.Err != nil || result.Deferred {
		t.Fatalf("initial history = %#v", result)
	}

	before := output.Len()
	// 8 行终端缩到 5 行：viewport Top 6 -> 3，压缩掉 3 行历史容量。
	if result := session.Flush(terminalSessionPlan(2, width, 5, 2, LeaseState{})); result.Err != nil {
		t.Fatalf("height shrink = %#v", result)
	}
	if !session.historyTailUnknown {
		t.Fatal("height shrink did not invalidate the resident history row model")
	}
	for _, address := range terminalRowAddresses(output.String()[before:]) {
		if address[0] < 3 {
			t.Fatalf("shrink wrote content into resident history row %d", address[0])
		}
	}

	before = output.Len()
	commit := terminalSessionCommit(2, render.Line{Spans: []render.Span{{Text: "SHRINK-THREE"}}})
	commit.Token = 2
	if result := session.CommitHistory(commit); result.Err != nil || result.Deferred {
		t.Fatalf("post-shrink history = %#v", result)
	}
	if !strings.Contains(output.String()[before:], "\x1b[1;2r") {
		t.Fatalf("post-shrink history did not append through the scroll region: %q", output.String()[before:])
	}

	raw := output.String()
	for _, marker := range []string{"SHRINK-ONE", "SHRINK-TWO", "SHRINK-THREE"} {
		if count := strings.Count(raw, marker); count != 1 {
			t.Fatalf("marker %q crossed the writer %d times, want exactly once", marker, count)
		}
	}
}

// 需求：宽度变化后 historyTailUnknown 生效期间，viewport 继续上移时也不能
// 让重画覆盖 resident 历史；必须按 viewport 位移量把对应行滚出（不写内容）。
func TestTerminalSessionUnprovableTailScrollsViewportShiftOut(t *testing.T) {
	const width = 24
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	if result := session.Flush(terminalSessionPlan(1, width, 10, 7, LeaseState{})); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}
	if result := session.CommitHistory(terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "UNPROVABLE-ONE"}}},
		render.Line{Spans: []render.Span{{Text: "UNPROVABLE-TWO"}}},
		render.Line{Spans: []render.Span{{Text: "UNPROVABLE-THREE"}}},
	)); result.Err != nil || result.Deferred {
		t.Fatalf("initial history = %#v", result)
	}

	// Width change invalidates the resident model (height unchanged).
	if result := session.Flush(terminalSessionPlan(2, 20, 10, 7, LeaseState{})); result.Err != nil {
		t.Fatalf("width resize = %#v", result)
	}
	if !session.historyTailUnknown {
		t.Fatal("width resize did not invalidate the tail model")
	}

	// Height-only shrink afterwards: the viewport moves up two rows. Since the
	// resident count is unknown, exactly those two rows must be scrolled out.
	before := output.Len()
	if result := session.Flush(terminalSessionPlan(3, 20, 10, 5, LeaseState{})); result.Err != nil {
		t.Fatalf("height shrink = %#v", result)
	}
	transition := output.String()[before:]
	if !strings.Contains(transition, "\x1b[1;5r") {
		t.Fatalf("shrink did not scroll the viewport shift out: %q", transition)
	}
	if strings.Count(transition, "\r\n") != 2 {
		t.Fatalf("shrink scrolled %d rows, want 2: %q", strings.Count(transition, "\r\n"), transition)
	}
}

// 需求：宽度变化后，新的历史行必须始终经由滚动区追加，不得用绝对行号写入
// 可能仍由 reflow 内容占用的行。
func TestTerminalSessionWidthResizeAppendsHistoryThroughScrollRegion(t *testing.T) {
	const (
		height       = 7
		outputBottom = 4
	)
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	if result := session.Flush(terminalSessionPlan(1, 26, height, outputBottom, LeaseState{})); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}
	if result := session.CommitHistory(terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "APPEND-ONE"}}},
	)); result.Err != nil || result.Deferred {
		t.Fatalf("initial history = %#v", result)
	}

	if result := session.Flush(terminalSessionPlan(2, 20, height, outputBottom, LeaseState{})); result.Err != nil {
		t.Fatalf("width resize = %#v", result)
	}
	before := output.Len()
	commit := terminalSessionCommit(2, render.Line{Spans: []render.Span{{Text: "APPEND-TWO"}}})
	commit.Token = 2
	if result := session.CommitHistory(commit); result.Err != nil || result.Deferred {
		t.Fatalf("post-resize history = %#v", result)
	}
	written := output.String()[before:]

	// HandoffPlan 的唯一写入点是滚动区底部行；任何其它绝对行地址都意味着
	// 又回到了“假设某行空着”的旧模型。
	for _, address := range terminalRowAddresses(written) {
		if address[0] != outputBottom {
			t.Fatalf("post-resize history addressed row %d directly: %q", address[0], written)
		}
	}
	if !strings.Contains(written, fmt.Sprintf("\x1b[1;%dr", outputBottom)) {
		t.Fatalf("post-resize history did not use the scroll region: %q", written)
	}
}

// 需求：ProjectionState 必须暴露 historyTailUnknown，供 /debug 判断是否仍
// 处于 resize 后的 append-only 窗口。
func TestProjectionStateReportsHistoryTailUnknown(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	if result := session.Flush(terminalSessionPlan(1, 24, 6, 3, LeaseState{})); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}
	if state := session.ProjectionState(); state.HistoryTailUnknown {
		t.Fatal("fresh session reported an unknown tail model")
	}
	if result := session.Flush(terminalSessionPlan(2, 18, 6, 3, LeaseState{})); result.Err != nil {
		t.Fatalf("width resize = %#v", result)
	}
	if state := session.ProjectionState(); !state.HistoryTailUnknown {
		t.Fatal("width resize did not surface the unknown tail model")
	}
}

// 需求：historyTailUnknown 不能永久粘住——当写入方自己的滚动追加已经替换完
// 整个 resident 区，绝对放置重新可证明，flag 必须自行清掉（否则每次缩放都会
// 让会话永久退化为 append-only，历史区永远无法被复用）。
func TestTerminalSessionAppendOnlyWindowClosesAfterRegionReplaced(t *testing.T) {
	const (
		height       = 8
		outputBottom = 3
	)
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	if result := session.Flush(terminalSessionPlan(1, 24, height, outputBottom, LeaseState{})); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}
	if result := session.Flush(terminalSessionPlan(2, 18, height, outputBottom, LeaseState{})); result.Err != nil {
		t.Fatalf("width resize = %#v", result)
	}
	if !session.historyTailUnknown {
		t.Fatal("width resize did not open the append-only window")
	}

	// 3 行追加 = resident 区容量，窗口应关闭。
	for token := uint64(1); token <= outputBottom; token++ {
		commit := terminalSessionCommit(2, render.Line{Spans: []render.Span{{Text: fmt.Sprintf("FILL-%d", token)}}})
		commit.Token = token
		if result := session.CommitHistory(commit); result.Err != nil || result.Deferred {
			t.Fatalf("fill commit %d = %#v", token, result)
		}
	}
	if session.historyTailUnknown {
		t.Fatalf("append-only window stayed open after %d/%d rows", outputBottom, outputBottom)
	}

	// 窗口关闭后，新的历史交付仍然 exactly-once（resident 模型重新可信，
	// 后续 payload 依据它做容量/去重判断，而不是一直退化在 append-only 分支）。
	before := output.Len()
	commit := terminalSessionCommit(2, render.Line{Spans: []render.Span{{Text: "AFTER-WINDOW"}}})
	commit.Token = 9
	if result := session.CommitHistory(commit); result.Err != nil || result.Deferred {
		t.Fatalf("post-window commit = %#v", result)
	}
	written := output.String()[before:]
	if !strings.Contains(written, "\x1b[1;3r") {
		t.Fatalf("post-window delivery left the scroll region: %q", written)
	}
	if !strings.Contains(written, "AFTER-WINDOW") {
		t.Fatalf("post-window delivery lost its payload: %q", written)
	}
	if len(session.historyTailRows) == 0 || len(session.historyTailRows) > outputBottom {
		t.Fatalf("post-window resident model is not authoritative: rows=%d capacity=%d",
			len(session.historyTailRows), outputBottom)
	}
}
