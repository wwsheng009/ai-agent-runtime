package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// P1-2b：historyStreamTailRows / historyTopAligned 降级为 writer 私有 ack 证明
// 的不变量钉。
//
// 事实源是「物理写成功」：一次 acked 的历史交付向 stream tail append 一次，
// 半写/失败必须整族失效（stream tail、resident tail、cell provenance、
// topAligned、投影已知位一起复位），topAligned 只在语义行真正越过 row 1 进入
// native scrollback 后置位并 sticky，直到显式 scrollback reset。这些字段不得
// 回喂 reducer，因此本测试驱动真实事务并断言字段状态机与屏幕事实一致，而不是
// 只对纯函数做单测。
func TestTerminalSessionStreamTailProofTracksAckedWrites(t *testing.T) {
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

	// 1) finalized 贴底插入：stream tail 与 resident tail 同步记录本批 3 行。
	finalized := terminalSessionCommit(1, line("fin-01"), line("fin-02"), line("fin-03"))
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &finalized}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("finalized commit = %#v", result)
	}
	if got := len(session.historyStreamTailRows); got != 3 {
		t.Fatalf("stream tail after finalized commit = %d rows, want 3", got)
	}
	if _, ok := session.historyTailCells[uint64(scene.CellID(7))]; !ok {
		t.Fatal("acked finalized delivery must record cell provenance")
	}
	if session.historyTopAligned {
		t.Fatal("bottom-anchored insert without overflow must not top-align")
	}
	if session.historyInsertionContinuesScrollback(height, outputBottom) {
		t.Fatal("resident tail non-empty must not claim scrollback continuation")
	}

	// 2) active 归档：行只进 stream tail（writer 私有证明），resident 模型不动，
	//    底部锚定不变 —— 两者是不同证明，这正是不能把 stream tail 当镜像删掉的原因。
	active := terminalSessionCommit(1, line("act-01"), line("act-02"), line("act-03"), line("act-04"), line("act-05"))
	active.Origin = HistoryCommitActive
	active.Token = 2
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &active}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("active archive = %#v", result)
	}
	// stream tail 是有界证明：上界 = outputBottom，保留的是最近写入的后缀。
	if got := len(session.historyStreamTailRows); got != outputBottom {
		t.Fatalf("stream tail after active archive = %d rows, want %d (bounded suffix)", got, outputBottom)
	}
	streamTail := strings.Join(session.historyStreamTailRows, "\n")
	if !strings.Contains(streamTail, "act-01") || !strings.Contains(streamTail, "act-05") || strings.Contains(streamTail, "fin-") {
		t.Fatalf("bounded stream tail must keep the newest written suffix: %q", streamTail)
	}
	if got := len(session.historyTailRows); got != 3 {
		t.Fatalf("resident tail after active archive = %d rows, want 3", got)
	}
	if session.historyTopAligned {
		t.Fatal("active archive with bottom anchor must not top-align")
	}

	// 3) finalized 溢出：语义行越过 row 1 进入 scrollback 后 topAligned 置位。
	overflow := terminalSessionCommit(1, line("msg-01"), line("msg-02"), line("msg-03"), line("msg-04"))
	overflow.Token = 3
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &overflow}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("overflow commit = %#v", result)
	}
	if !session.historyTopAligned {
		t.Fatal("finalized overflow past row 1 must top-align")
	}
	if got := len(session.historyStreamTailRows); got != outputBottom {
		t.Fatalf("stream tail after overflow = %d rows, want %d (bounded suffix)", got, outputBottom)
	}
	if streamTail := strings.Join(session.historyStreamTailRows, "\n"); !strings.Contains(streamTail, "msg-04") {
		t.Fatalf("overflow rows missing from bounded stream tail: %q", streamTail)
	}

	// 4) sticky：溢出后的短插入不回落，直到显式 reset。
	next := terminalSessionCommit(1, line("msg-05"))
	next.Token = 4
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &next}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("sticky commit = %#v", result)
	}
	if !session.historyTopAligned {
		t.Fatal("top-aligned state must be sticky across later short inserts")
	}
	if got := len(session.historyStreamTailRows); got != outputBottom {
		t.Fatalf("stream tail after sticky insert = %d rows, want %d (bounded suffix)", got, outputBottom)
	}
	if streamTail := strings.Join(session.historyStreamTailRows, "\n"); !strings.Contains(streamTail, "msg-05") {
		t.Fatalf("sticky insert missing from bounded stream tail: %q", streamTail)
	}

	// 5) 显式 scrollback reset：整族证明一起失效。
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, resetScrollback: true}); result.Frame.Err != nil {
		t.Fatalf("scrollback reset = %#v", result)
	}
	if session.historyStreamTailRows != nil || session.historyTailCells != nil {
		t.Fatalf("reset retained stream proof: tail=%d cells=%d",
			len(session.historyStreamTailRows), len(session.historyTailCells))
	}
	if session.historyTopAligned {
		t.Fatal("reset must clear the sticky top-align state")
	}
	if !session.historyProjectionKnown {
		t.Fatal("reset must re-establish a known projection")
	}
}

// 半写是「本会话的已交付行证明」唯一不可信的路径：stream tail / provenance /
// topAligned / 投影已知位必须一起复位，否则后续 dedup 会基于不可信证明裁行。
func TestTerminalSessionStreamTailProofResetsOnPartialWrite(t *testing.T) {
	writer := &terminalSessionShortWriter{}
	session := NewTerminalSession(writer)
	plan := terminalSessionPlan(1, 28, 6, 4, LeaseState{})
	if result := session.Flush(plan); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}

	first := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "PROOF-ONE"}}})
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &first}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("first history transaction = %#v", result)
	}
	if len(session.historyStreamTailRows) == 0 || len(session.historyTailCells) == 0 {
		t.Fatal("successful delivery must establish the stream proof")
	}

	writer.short = true
	second := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "PROOF-TWO"}}})
	second.Token = 2
	result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &second})
	if result.History == nil || !result.History.MayHavePartiallyWritten {
		t.Fatalf("partial history transaction = %#v", result)
	}
	if session.historyStreamTailRows != nil || session.historyTailCells != nil {
		t.Fatalf("partial write retained stream proof: tail=%d cells=%d",
			len(session.historyStreamTailRows), len(session.historyTailCells))
	}
	if session.historyTopAligned || session.historyProjectionKnown {
		t.Fatalf("partial write retained derived state: topAligned=%t known=%t",
			session.historyTopAligned, session.historyProjectionKnown)
	}
	if got := session.historyInsertionContinuesScrollback(6, 4); got {
		t.Fatalf("partial write must not claim scrollback continuation (got %t)", got)
	}
}

// stream tail 的 append-only 契约：同一 projection 内成功交付只能追加，不能
// 重写历史（dedup 裁剪只影响本批 payload，不改已证明的行）。
func TestTerminalSessionStreamTailAppendOnlyAcrossDeliveries(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	plan := terminalSessionPlan(1, 24, 8, 5, LeaseState{})
	if result := session.Flush(plan); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}
	commit := func(token uint64, text string) HistoryCommit {
		c := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: text}}})
		c.Token = token
		return c
	}
	first := commit(1, "append-one")
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &first}); result.History == nil || result.History.Err != nil {
		t.Fatalf("first append = %#v", result)
	}
	before := append([]string(nil), session.historyStreamTailRows...)
	second := commit(2, "append-two")
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &second}); result.History == nil || result.History.Err != nil {
		t.Fatalf("second append = %#v", result)
	}
	after := session.historyStreamTailRows
	if len(after) != len(before)+1 {
		t.Fatalf("stream tail length = %d, want %d", len(after), len(before)+1)
	}
	for index, row := range before {
		if after[index] != row {
			t.Fatalf("stream tail rewrote proven row %d: %q -> %q", index, row, after[index])
		}
	}
	if !strings.Contains(after[len(after)-1], "append-two") {
		t.Fatalf("stream tail last row = %q, want the new delivery", after[len(after)-1])
	}
}

// P1-2b §2.3：writer frame 身份只有一个分配点（confirmWriteLocked）。
// viewport 帧与 history-only 交付共享同一计数器，成功写各 +1；deferred /
// 失败写不得推进，否则帧号与物理写序列脱钩。
func TestTerminalSessionWriterFrameIsSingleAllocationPoint(t *testing.T) {
	writer := &terminalSessionShortWriter{}
	session := NewTerminalSession(writer)
	plan := terminalSessionPlan(1, 24, 6, 4, LeaseState{})
	if result := session.Flush(plan); result.Err != nil || result.Frame != 1 {
		t.Fatalf("initial frame = %#v", result)
	}
	if result := session.Flush(plan); result.Err != nil || result.Frame != 2 || session.frame != 2 {
		t.Fatalf("second viewport frame = %#v (session.frame=%d)", result, session.frame)
	}

	// 陈旧 generation 的 history-only 交付必须 Deferred，帧号不动。
	stale := terminalSessionCommit(999, render.Line{Spans: []render.Span{{Text: "STALE"}}})
	if result := session.CommitHistory(stale); !result.Deferred || result.Err != nil {
		t.Fatalf("stale history commit = %#v", result)
	}
	if session.frame != 2 {
		t.Fatalf("deferred commit advanced the writer frame to %d", session.frame)
	}

	// 成功的 history-only 路径与 viewport 路径共享同一帧号序列。
	confirmed := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "CONFIRMED"}}})
	if result := session.CommitHistory(confirmed); result.Err != nil || result.Deferred || result.Frame != 3 {
		t.Fatalf("history-only commit = %#v (session.frame=%d)", result, session.frame)
	}
	if session.frame != 3 {
		t.Fatalf("history-only commit left session.frame=%d, want 3", session.frame)
	}

	// 半写失败不得推进帧号。
	writer.short = true
	failing := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "FAILING"}}})
	failing.Token = 2
	if result := session.CommitHistory(failing); result.Err == nil {
		t.Fatalf("short history commit unexpectedly succeeded: %#v", result)
	}
	if session.frame != 3 {
		t.Fatalf("failed commit advanced the writer frame to %d", session.frame)
	}
}
