package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// A2 第一刀（停铸 active）：交付分支与 Origin 无关，active-origin 交付与
// transcript-origin 一样按 resident 插入语义参与滚动；不再存在"归档到
// scrollback 但不拥有 resident 行"的特例。以下用例固定统一后的文档顺序与
// 恰好一次语义（terminalActiveHistoryArchiveANSI 留作死代码，第二刀删除）。

// terminalScreenDocument 返回历史区（rows 1..capacity）与 native scrollback 的
// 非空行序列；composer band 位于 outputBottom 之下，不参与历史文档顺序。
func terminalScreenDocument(screen *vt.Screen, capacity int) []string {
	rows := append(append([]string(nil), screen.ScrollbackLines()...), screen.Lines(1, capacity)...)
	document := make([]string, 0, len(rows))
	for _, row := range rows {
		row = strings.TrimRight(row, " ")
		if strings.TrimSpace(row) == "" {
			continue
		}
		document = append(document, row)
	}
	return document
}

func TestTerminalSessionActiveOriginOverflowUsesResidentInsertion(t *testing.T) {
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
	active.Token = 2
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &active}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("active history transaction = %#v", result)
	}

	screen := vt.NewScreen(24, 5)
	screen.Feed(output.String())
	want := []string{"final-one", "final-two", "final-three", "active-head-one", "active-head-two"}
	if got := terminalScreenDocument(screen, 3); !equalTrimmedLines(got, want) {
		t.Fatalf("unified insertion document = %q, want %q\n%s", got, want, screen.Dump())
	}
}

func TestTerminalSessionActiveOriginOverflowKeepsDocumentOrder(t *testing.T) {
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
	active.Token = 2
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &active}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("active history transaction = %#v", result)
	}

	screen := vt.NewScreen(24, 6)
	screen.Feed(output.String())
	want := []string{"keep-me", "archive-one", "archive-two", "archive-three", "archive-four", "archive-five"}
	if got := terminalScreenDocument(screen, 2); !equalTrimmedLines(got, want) {
		t.Fatalf("unified insertion document = %q, want %q\n%s", got, want, screen.Dump())
	}
}

func TestTerminalSessionActiveOriginInsertionAdvancesResidentModel(t *testing.T) {
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
	active.Token = 2
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &active}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("active delivery = %#v", result)
	}
	if got := len(session.historyTailRows); got != outputBottom {
		t.Fatalf("resident tail after active delivery = %d rows, want %d (unified insertion owns rows)", got, outputBottom)
	}

	next := terminalSessionCommit(1, line("msg-01"), line("msg-02"))
	next.Token = 3
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &next}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("next finalized commit = %#v", result)
	}

	screen := vt.NewScreen(width, height)
	screen.Feed(output.String())
	want := []string{
		"fin-01", "fin-02", "fin-03",
		"act-01", "act-02", "act-03", "act-04", "act-05",
		"msg-01", "msg-02",
	}
	if got := terminalScreenDocument(screen, outputBottom); !equalTrimmedLines(got, want) {
		t.Fatalf("unified insertion document = %q, want %q\n%s", got, want, screen.Dump())
	}
}

func TestTerminalSessionActiveOriginInsertionKeepsResidentContinuity(t *testing.T) {
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
	active.Token = 2
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &active}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("active delivery = %#v", result)
	}
	next := terminalSessionCommit(1, line("fin-01"), line("fin-02"))
	next.Token = 3
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &next}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("finalized continuation = %#v", result)
	}

	screen := vt.NewScreen(width, height)
	screen.Feed(output.String())
	want := []string{"act-01", "act-02", "act-03", "act-04", "act-05", "fin-01", "fin-02"}
	if got := terminalScreenDocument(screen, outputBottom); !equalTrimmedLines(got, want) {
		t.Fatalf("unified insertion document = %q, want %q\n%s", got, want, screen.Dump())
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
