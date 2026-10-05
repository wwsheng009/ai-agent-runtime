package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// A still-mutable cell's overflow handoff must cross the physical writer
// without becoming a resident of the primary history region: the finalized
// tail the layout keeps above the active band must not be evicted by the live
// cell that follows it.
func TestTerminalSessionActiveOverflowArchivesBeyondRetainedHistoryTail(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	plan := terminalSessionPlan(1, 24, 5, 3, LeaseState{})
	if result := session.Flush(plan); result.Err != nil || !result.FullRepaint {
		t.Fatalf("initial frame = %#v", result)
	}

	finalized := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "final-one"}}},
		render.Line{Spans: []render.Span{{Text: "final-two"}}},
		render.Line{Spans: []render.Span{{Text: "final-three"}}},
	)
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &finalized}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("finalized history transaction = %#v", result)
	}

	active := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "active-head-one"}}},
		render.Line{Spans: []render.Span{{Text: "active-head-two"}}},
	)
	active.Origin = HistoryCommitActive
	active.Token = 2
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &active}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("active history transaction = %#v", result)
	}

	screen := vt.NewScreen(24, 5)
	screen.Feed(output.String())
	if got := strings.Join(screen.Lines(1, 3), "\n"); got != "final-one\nfinal-two\nfinal-three" {
		t.Fatalf("finalized tail displaced by active overflow = %q\n%s", got, screen.Dump())
	}
	scrollback := strings.Join(screen.ScrollbackLines(), "\n")
	for _, want := range []string{"active-head-one", "active-head-two"} {
		if count := strings.Count(scrollback, want); count != 1 {
			t.Fatalf("active overflow row %q archived %d times, want exactly once:\n%s", want, count, screen.Dump())
		}
	}
	if strings.Contains(scrollback, "final-") {
		t.Fatalf("archiving leaked retained finalized rows into native scrollback: %q", scrollback)
	}
}

// An archive longer than the history region must cross the writer in document
// order. An active archive is fire-and-forget: the finalized stream has not
// overflowed, so the retained tail keeps its bottom anchor instead of being
// moved to row one (which would push every later live message mid-screen).
func TestTerminalSessionActiveOverflowArchiveChunksLargerThanRegion(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	plan := terminalSessionPlan(1, 24, 6, 2, LeaseState{})
	if result := session.Flush(plan); result.Err != nil || !result.FullRepaint {
		t.Fatalf("initial frame = %#v", result)
	}

	finalized := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "keep-me"}}})
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &finalized}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("finalized history transaction = %#v", result)
	}

	active := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "archive-one"}}},
		render.Line{Spans: []render.Span{{Text: "archive-two"}}},
		render.Line{Spans: []render.Span{{Text: "archive-three"}}},
		render.Line{Spans: []render.Span{{Text: "archive-four"}}},
		render.Line{Spans: []render.Span{{Text: "archive-five"}}},
	)
	active.Origin = HistoryCommitActive
	active.Token = 2
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &active}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("active history transaction = %#v", result)
	}

	screen := vt.NewScreen(24, 6)
	screen.Feed(output.String())
	if got := strings.TrimRight(screen.Line(2), " "); got != "keep-me" {
		t.Fatalf("retained tail after oversized archive = %q, want bottom-anchored keep-me\n%s", got, screen.Dump())
	}
	if got := screen.Line(1); strings.TrimSpace(got) != "" {
		t.Fatalf("row 1 above the bottom-anchored tail must be empty, got %q\n%s", got, screen.Dump())
	}
	want := []string{"archive-one", "archive-two", "archive-three", "archive-four", "archive-five"}
	if got := screen.ScrollbackLines(); !equalTrimmedLines(got, want) {
		t.Fatalf("archived order = %q, want %q\n%s", got, want, screen.Dump())
	}
}

// 现场缺陷回归：finalized 尾部贴底锚定时，active 溢出归档只把 mutable 前缀
// 送进 native scrollback，不得把 finalized 尾部搬到顶部；随后的 finalized
// 消息必须贴底追加（紧邻 band/composer），而不是插到屏幕中部、下方留白。
func TestTerminalSessionActiveArchiveKeepsBottomAnchorForLaterMessages(t *testing.T) {
	const width, height, outputBottom = 24, 8, 5
	line := func(text string) render.Line {
		return render.Line{Spans: []render.Span{{Text: text}}}
	}
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	plan := terminalSessionPlan(1, width, height, outputBottom, LeaseState{})
	if result := session.Flush(plan); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}

	finalized := terminalSessionCommit(1, line("fin-01"), line("fin-02"), line("fin-03"))
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &finalized}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("finalized commit = %#v", result)
	}

	active := terminalSessionCommit(1,
		line("act-01"), line("act-02"), line("act-03"), line("act-04"), line("act-05"),
	)
	active.Origin = HistoryCommitActive
	active.Token = 2
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &active}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("active archive = %#v", result)
	}

	screen := vt.NewScreen(width, height)
	screen.Feed(output.String())
	if got := strings.TrimRight(screen.Line(3), " "); got != "fin-01" {
		t.Fatalf("row 3 after archive = %q, want fin-01 (finalized tail must keep its bottom anchor)\n%s", got, screen.Dump())
	}
	if got := strings.TrimRight(screen.Line(5), " "); got != "fin-03" {
		t.Fatalf("row 5 after archive = %q, want fin-03\n%s", got, screen.Dump())
	}
	if got := strings.TrimSpace(screen.Line(1)); got != "" {
		t.Fatalf("row 1 after archive = %q, want blank headroom above the bottom-anchored tail\n%s", got, screen.Dump())
	}
	if sb := strings.Join(screen.ScrollbackLines(), "\n"); !strings.Contains(sb, "act-01") || !strings.Contains(sb, "act-05") || strings.Contains(sb, "fin-") {
		t.Fatalf("archive must hold the mutable prefix only, scrollback = %q\n%s", sb, screen.Dump())
	}

	next := terminalSessionCommit(1, line("msg-01"), line("msg-02"))
	next.Token = 3
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &next}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("next finalized commit = %#v", result)
	}

	screen = vt.NewScreen(width, height)
	screen.Feed(output.String())
	if got := strings.TrimRight(screen.Line(4), " "); got != "msg-01" {
		t.Fatalf("row 4 after next commit = %q, want msg-01 appended at the bottom\n%s", got, screen.Dump())
	}
	if got := strings.TrimRight(screen.Line(5), " "); got != "msg-02" {
		t.Fatalf("row 5 after next commit = %q, want msg-02 appended at the bottom\n%s", got, screen.Dump())
	}
	if sb := strings.Join(screen.ScrollbackLines(), "\n"); strings.Contains(sb, "fin-") || strings.Contains(sb, "msg-") {
		t.Fatalf("finalized rows must not be evicted into scrollback, scrollback = %q\n%s", sb, screen.Dump())
	}
}

func equalTrimmedLines(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if strings.TrimRight(got[index], " ") != want[index] {
			return false
		}
	}
	return true
}

// active 归档把 mutable 前缀送入 native scrollback 后，resident 模型为空；
// 随后 finalized 续写必须从 row 1 起与归档流连续，不得贴底在 scrollback
// 与可见行之间留下空档（a75d1c89 回归的会话级契约）。
func TestTerminalSessionInsertionContinuesArchivedScrollback(t *testing.T) {
	const width, height, outputBottom = 24, 8, 5
	line := func(text string) render.Line {
		return render.Line{Spans: []render.Span{{Text: text}}}
	}
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	plan := terminalSessionPlan(1, width, height, outputBottom, LeaseState{})
	if result := session.Flush(plan); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}

	active := terminalSessionCommit(1,
		line("act-01"), line("act-02"), line("act-03"), line("act-04"), line("act-05"),
	)
	active.Origin = HistoryCommitActive
	active.Token = 2
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &active}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("active archive = %#v", result)
	}

	next := terminalSessionCommit(1, line("fin-01"), line("fin-02"))
	next.Token = 3
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &next}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("finalized continuation = %#v", result)
	}

	screen := vt.NewScreen(width, height)
	screen.Feed(output.String())
	if got := strings.TrimRight(screen.Line(1), " "); got != "fin-01" {
		t.Fatalf("row 1 after finalized continuation = %q, want fin-01 (must continue archived scrollback)\n%s", got, screen.Dump())
	}
	if got := strings.TrimRight(screen.Line(2), " "); got != "fin-02" {
		t.Fatalf("row 2 after finalized continuation = %q, want fin-02\n%s", got, screen.Dump())
	}
	if got := strings.TrimSpace(screen.Line(3)); got != "" {
		t.Fatalf("row 3 below the continuation must be blank, got %q\n%s", got, screen.Dump())
	}
	sb := strings.Join(screen.ScrollbackLines(), "\n")
	if !strings.Contains(sb, "act-01") || !strings.Contains(sb, "act-05") {
		t.Fatalf("archived prefix missing from scrollback: %q\n%s", sb, screen.Dump())
	}
}
