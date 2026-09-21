package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ---------------------------------------------------------------------------
// /web/api/screen 结构化 messages 窗口（msg_limit / msg_before）
//
// 背景：长会话（turn 很多）下完整 transcript 的 JSON 体积与前端 DOM 开销
// 随消息数线性增长。窗口化让前端只取最新一页，上滚时用 msg_before 游标
// 逐页向前加载；不传参数时保持完整 transcript 的历史行为。
// ---------------------------------------------------------------------------

// windowTestSnapshot 构造 10 条消息的快照（lines/text 用哨兵值，便于断言
// 未分页时不被重建）。
func windowTestSnapshot() *chatDebugScreenSnapshot {
	roles := []string{
		"user", "assistant", "tool", "reasoning", "system",
		"command", "diagnostic", "runtime", "user", "assistant",
	}
	msgs := make([]chatWebScreenMessage, 0, len(roles))
	for i, role := range roles {
		msgs = append(msgs, chatWebScreenMessage{Role: role, Content: fmt.Sprintf("m%d", i)})
	}
	return &chatDebugScreenSnapshot{
		Available: true,
		Messages:  msgs,
		Lines:     []string{"sentinel-line"},
		Text:      "sentinel-text",
	}
}

func TestWindowChatWebMessagesLatestWindow(t *testing.T) {
	snap := windowTestSnapshot()

	windowChatWebMessages(snap, chatWebMessageWindow{Limit: 3})

	if len(snap.Messages) != 3 {
		t.Fatalf("窗口内消息数 = %d, want 3", len(snap.Messages))
	}
	if got := snap.Messages[0].Content; got != "m7" {
		t.Fatalf("窗口首条 = %q, want m7", got)
	}
	if got := snap.Messages[2].Content; got != "m9" {
		t.Fatalf("窗口末条 = %q, want m9", got)
	}
	info := snap.MessageWindow
	if info == nil {
		t.Fatal("message_window 不应为空")
	}
	if info.Total != 10 || info.Start != 7 || info.End != 10 || !info.HasMore || info.Limit != 3 {
		t.Fatalf("message_window = %+v, want {Total:10 Start:7 End:10 HasMore:true Limit:3}", *info)
	}
	// 窗口激活时 lines/text 必须同步收缩（避免"裁了 messages 仍搬运全文"）。
	if snap.Lines == nil || len(snap.Lines) != 3 {
		t.Fatalf("lines 应随窗口重建为 3 行，实际 %v", snap.Lines)
	}
	if snap.Text != "m7\nuser> m8\nm9" {
		t.Fatalf("text 应随窗口重建，实际 %q", snap.Text)
	}
}

func TestWindowChatWebMessagesOlderPage(t *testing.T) {
	snap := windowTestSnapshot()

	// 前端已加载 [7,10)，上滚一页：取 [4,7)。
	windowChatWebMessages(snap, chatWebMessageWindow{Before: 7, Limit: 3})

	if len(snap.Messages) != 3 {
		t.Fatalf("窗口内消息数 = %d, want 3", len(snap.Messages))
	}
	if got := snap.Messages[0].Content; got != "m4" {
		t.Fatalf("窗口首条 = %q, want m4", got)
	}
	info := snap.MessageWindow
	if info == nil || info.Start != 4 || info.End != 7 || !info.HasMore {
		t.Fatalf("message_window = %+v, want {Start:4 End:7 HasMore:true}", info)
	}
	if got := snap.Messages[1].Content; got != "m5" {
		t.Fatalf("窗口次条 = %q, want m5", got)
	}
	if snap.Text != "[system] m4\ncmd> m5\n[diag] m6" {
		t.Fatalf("text 应随窗口重建，实际 %q", snap.Text)
	}
}

func TestWindowChatWebMessagesFullTranscriptUnchanged(t *testing.T) {
	snap := windowTestSnapshot()

	// 不传参数：完整 transcript（历史行为），lines/text 保持原派生结果。
	windowChatWebMessages(snap, chatWebMessageWindow{})

	if len(snap.Messages) != 10 {
		t.Fatalf("未分页时应返回完整 messages，实际 %d", len(snap.Messages))
	}
	info := snap.MessageWindow
	if info == nil || info.Total != 10 || info.Start != 0 || info.End != 10 || info.HasMore {
		t.Fatalf("message_window = %+v, want {Total:10 Start:0 End:10 HasMore:false}", info)
	}
	if snap.Text != "sentinel-text" || len(snap.Lines) != 1 || snap.Lines[0] != "sentinel-line" {
		t.Fatalf("未分页时不应重建 lines/text，实际 lines=%v text=%q", snap.Lines, snap.Text)
	}
}

func TestWindowChatWebMessagesClampAndEmpty(t *testing.T) {
	// before 超过总数 → 归一为总数；limit 超过可用条数 → 从头取。
	snap := windowTestSnapshot()
	windowChatWebMessages(snap, chatWebMessageWindow{Before: 999, Limit: 999})
	if len(snap.Messages) != 10 || snap.MessageWindow.Start != 0 || snap.MessageWindow.HasMore {
		t.Fatalf("越界参数应钳制到完整窗口，实际 window=%+v len=%d", snap.MessageWindow, len(snap.Messages))
	}

	snap = windowTestSnapshot()
	windowChatWebMessages(snap, chatWebMessageWindow{Before: 3, Limit: 999})
	if snap.MessageWindow.Start != 0 || snap.MessageWindow.End != 3 || snap.MessageWindow.HasMore {
		t.Fatalf("limit 超过 before 时应从头取，实际 %+v", snap.MessageWindow)
	}

	// 纯文本快照（无 messages）：不做任何改动。
	empty := &chatDebugScreenSnapshot{Available: true, Text: "only-text"}
	windowChatWebMessages(empty, chatWebMessageWindow{Limit: 1})
	if empty.MessageWindow != nil || empty.Text != "only-text" {
		t.Fatalf("无 messages 时不应写入窗口元信息，实际 %+v text=%q", empty.MessageWindow, empty.Text)
	}
}

func TestChatWebMessageWindowParam(t *testing.T) {
	cases := []struct {
		url  string
		want chatWebMessageWindow
	}{
		{ChatWebAPIScreenPath + "?msg_limit=20&msg_before=50", chatWebMessageWindow{Before: 50, Limit: 20}},
		{ChatWebAPIScreenPath + "?msg_limit=9999", chatWebMessageWindow{Limit: chatWebMessageWindowMaxLimit}},
		{ChatWebAPIScreenPath + "?msg_limit=abc&msg_before=-3", chatWebMessageWindow{}},
		{ChatWebAPIScreenPath + "?msg_limit=0&msg_before=0", chatWebMessageWindow{}},
		{ChatWebAPIScreenPath, chatWebMessageWindow{}},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.url, nil)
		if got := chatWebMessageWindowParam(req); got != tc.want {
			t.Fatalf("%s → %+v, want %+v", tc.url, got, tc.want)
		}
	}
	if got := chatWebMessageWindowParam(nil); got != (chatWebMessageWindow{}) {
		t.Fatalf("nil 请求应返回空窗口，实际 %+v", got)
	}
}

func TestHandleChatWebAPIScreenJSONMessageWindow(t *testing.T) {
	session := &ChatSession{
		Messages: []types.Message{
			{Role: "user", Content: "q1"},
			{Role: "assistant", Content: "a1"},
			{Role: "user", Content: "q2"},
			{Role: "assistant", Content: "a2"},
			{Role: "user", Content: "q3"},
			{Role: "assistant", Content: "a3"},
		},
	}
	withWebTestSession(t, session)

	type screenWindowResponse struct {
		Messages []chatWebScreenMessage    `json:"messages"`
		Window   *chatWebMessageWindowInfo `json:"message_window"`
		Text     string                    `json:"text"`
	}

	// 1) 最新一页：msg_limit=2 → [4,6)。
	req := httptest.NewRequest(http.MethodGet, ChatWebAPIScreenPath+"?format=json&msg_limit=2", nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPIScreen(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var parsed screenWindowResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("screen JSON invalid: %v", err)
	}
	if len(parsed.Messages) != 2 || parsed.Messages[0].Content != "q3" || parsed.Messages[1].Content != "a3" {
		t.Fatalf("最新一页 messages = %+v, want [q3 a3]", parsed.Messages)
	}
	if parsed.Window == nil || parsed.Window.Total != 6 || parsed.Window.Start != 4 ||
		parsed.Window.End != 6 || !parsed.Window.HasMore {
		t.Fatalf("message_window = %+v, want {Total:6 Start:4 End:6 HasMore:true}", parsed.Window)
	}
	if !strings.Contains(parsed.Text, "user> q3") || strings.Contains(parsed.Text, "q1") {
		t.Fatalf("text 应与窗口同源，实际 %q", parsed.Text)
	}

	// 2) 上滚一页：msg_before=4&msg_limit=2 → [2,4)。
	req = httptest.NewRequest(http.MethodGet, ChatWebAPIScreenPath+"?format=json&msg_limit=2&msg_before=4", nil)
	rec = httptest.NewRecorder()
	HandleChatWebAPIScreen(rec, req)
	parsed = screenWindowResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("screen JSON invalid: %v", err)
	}
	if len(parsed.Messages) != 2 || parsed.Messages[0].Content != "q2" || parsed.Messages[1].Content != "a2" {
		t.Fatalf("更早一页 messages = %+v, want [q2 a2]", parsed.Messages)
	}
	if parsed.Window == nil || parsed.Window.Start != 2 || parsed.Window.End != 4 || !parsed.Window.HasMore {
		t.Fatalf("message_window = %+v, want {Start:2 End:4 HasMore:true}", parsed.Window)
	}

	// 3) 不传参数：完整 transcript 兼容路径。
	req = httptest.NewRequest(http.MethodGet, ChatWebAPIScreenPath+"?format=json", nil)
	rec = httptest.NewRecorder()
	HandleChatWebAPIScreen(rec, req)
	parsed = screenWindowResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("screen JSON invalid: %v", err)
	}
	if len(parsed.Messages) != 6 {
		t.Fatalf("未分页 messages = %d, want 6", len(parsed.Messages))
	}
	if parsed.Window == nil || parsed.Window.Start != 0 || parsed.Window.HasMore {
		t.Fatalf("未分页 message_window = %+v, want {Start:0 HasMore:false}", parsed.Window)
	}

	// 4) 文本视图同样支持窗口（?tail 仍作用于窗口后的文本）。
	req = httptest.NewRequest(http.MethodGet, ChatWebAPIScreenPath+"?msg_limit=1", nil)
	rec = httptest.NewRecorder()
	HandleChatWebAPIScreen(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "a3") || strings.Contains(body, "q1") {
		t.Fatalf("文本视图窗口应只含最新消息，实际 %q", body)
	}
}

// ---------------------------------------------------------------------------
// 窗口化提取（O(窗口)）与「先取全量再切片」的等价性
//
// buildChatWebScreenSnapshotFor 在窗口激活时先计数、再按区间提取，避免构造
// 全量 messages/lines。以下用例锁定它与历史路径（全量快照 + windowChatWebMessages）
// 在消息、分页元信息、lines/text 上完全一致，并守住「不保留全量底层数组」的
// 内存约束。
// ---------------------------------------------------------------------------

// windowEquivalenceSessionMessages 覆盖各 role、空正文与工具 compact 投影兜底。
func windowEquivalenceSessionMessages() []types.Message {
	toolMessage := realPersistedToolMessage()
	assistant := types.NewAssistantMessage("前面说明")
	assistant.ToolCalls = []types.ToolCall{{
		ID:   toolMessage.ToolCallID,
		Name: "shell",
		Args: map[string]interface{}{"command": "echo E2E_SNAPSHOT_OK"},
	}}
	return []types.Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "   "}, // 空正文：不计入消息索引空间
		{Role: "system", Content: "sys1"},
		*assistant,
		toolMessage,
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
	}
}

// compareChatWebSnapshots 断言窗口化快照与全量切片快照逐字段一致。
func compareChatWebSnapshots(t *testing.T, label string, got, want *chatDebugScreenSnapshot) {
	t.Helper()
	if got.Available != want.Available || got.Reason != want.Reason {
		t.Fatalf("%s: available=%v/%q, want %v/%q", label, got.Available, got.Reason, want.Available, want.Reason)
	}
	if len(got.Messages) != len(want.Messages) {
		t.Fatalf("%s: messages=%d, want %d", label, len(got.Messages), len(want.Messages))
	}
	for i := range got.Messages {
		if got.Messages[i] != want.Messages[i] {
			t.Fatalf("%s: messages[%d]=%+v, want %+v", label, i, got.Messages[i], want.Messages[i])
		}
	}
	if len(got.Lines) != len(want.Lines) {
		t.Fatalf("%s: lines=%d, want %d (%q vs %q)", label, len(got.Lines), len(want.Lines), got.Lines, want.Lines)
	}
	for i := range got.Lines {
		if got.Lines[i] != want.Lines[i] {
			t.Fatalf("%s: lines[%d]=%q, want %q", label, i, got.Lines[i], want.Lines[i])
		}
	}
	if got.Text != want.Text {
		t.Fatalf("%s: text=%q, want %q", label, got.Text, want.Text)
	}
	if (got.MessageWindow == nil) != (want.MessageWindow == nil) {
		t.Fatalf("%s: message_window=%+v, want %+v", label, got.MessageWindow, want.MessageWindow)
	}
	if got.MessageWindow != nil && *got.MessageWindow != *want.MessageWindow {
		t.Fatalf("%s: message_window=%+v, want %+v", label, *got.MessageWindow, *want.MessageWindow)
	}
}

func TestBuildChatWebScreenSnapshotWindowedMatchesFullThenSlice(t *testing.T) {
	session := &ChatSession{Messages: windowEquivalenceSessionMessages()}
	withWebTestSession(t, session)

	windows := []chatWebMessageWindow{
		{},                        // 未激活：完整 transcript（历史行为）
		{Limit: 1},                // 最新一条
		{Limit: 3},                // 最新一页
		{Before: 4, Limit: 2},     // 上滚中间页
		{Before: 1, Limit: 5},     // 首页（limit 被 before 钳制）
		{Before: 999, Limit: 999}, // 越界参数钳制到全量
		{Limit: 100},              // limit 超过总量
	}
	for _, window := range windows {
		full := buildChatWebScreenSnapshotFull()
		want := *full
		want.Messages = append([]chatWebScreenMessage(nil), full.Messages...)
		windowChatWebMessages(&want, window)

		got := buildChatWebScreenSnapshotFor(window)
		if !window.active() {
			// handler 路径：窗口未激活时由 windowChatWebMessages 补写分页元信息
			// （messages/lines/text 保持全量，不重建）。
			windowChatWebMessages(got, window)
		}
		label := fmt.Sprintf("window=%+v", window)
		compareChatWebSnapshots(t, label, got, &want)

		if window.active() {
			// 窗口化提取不得保留全量底层数组：旧实现是先构造全量再切片，
			// 其 cap 必然 >= total。
			if total := got.MessageWindow.Total; cap(got.Messages) >= total && total > len(got.Messages) {
				t.Fatalf("%s: cap(messages)=%d 与 total=%d 同阶，窗口化提取未生效", label, cap(got.Messages), total)
			}
		}
	}
}

// sliceChatWebMessages 复刻 windowChatWebMessages 的切片语义（越界/空区间 → nil），
// 作为区间提取函数的对照实现。
func sliceChatWebMessages(full []chatWebScreenMessage, start, end int) []chatWebScreenMessage {
	if start < 0 || end <= start || start >= len(full) {
		return nil
	}
	if end > len(full) {
		end = len(full)
	}
	return append([]chatWebScreenMessage(nil), full[start:end]...)
}

func TestTranscriptFallbackMessagesRangeMatchesFull(t *testing.T) {
	cells := []scene.TranscriptCell{
		{Kind: scene.KindUser, Source: "u1"},
		{Kind: scene.KindAssistant}, // 空 Source：不计入
		{Kind: scene.KindToolChain, Source: "t1"},
		{Kind: scene.KindReasoning, Source: "r1"},
		{Kind: scene.KindSystem},
		{Kind: scene.KindDiagnostic, Source: "d1"},
	}
	full := transcriptFallbackMessages(cells)
	if full == nil {
		t.Fatal("cells 全量提取不应为空")
	}
	if got := countTranscriptCellMessages(cells); got != len(full) {
		t.Fatalf("countTranscriptCellMessages=%d, want %d", got, len(full))
	}
	for start := 0; start <= len(full)+1; start++ {
		for end := start; end <= len(full)+1; end++ {
			got := transcriptFallbackMessagesRange(cells, start, end)
			want := sliceChatWebMessages(full, start, end)
			if len(got) != len(want) {
				t.Fatalf("[%d,%d): len=%d, want %d", start, end, len(got), len(want))
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("[%d,%d): [%d]=%+v, want %+v", start, end, i, got[i], want[i])
				}
			}
		}
	}
}

func TestTranscriptFallbackSnapshotMessagesRangeMatchesFull(t *testing.T) {
	valueCells := []scene.TranscriptCell{
		{Kind: scene.KindUser, Source: "u1"},
		{Kind: scene.KindAssistant},
		{Kind: scene.KindToolChain, Source: "t1"},
		{Kind: scene.KindRuntimeEvent, Source: "e1"},
		{Kind: scene.KindSupplement, Source: "s1"},
	}
	snap := &scene.Snapshot{Cells: []*scene.TranscriptCell{
		nil, // nil cell 必须与空 Source 一样被跳过
		&valueCells[0],
		&valueCells[1],
		&valueCells[2],
		&valueCells[3],
		&valueCells[4],
	}}

	full := transcriptFallbackSnapshotMessages(snap)
	if len(full) != 4 {
		t.Fatalf("快照全量提取=%d, want 4: %#v", len(full), full)
	}
	if got := countTranscriptSnapshotMessages(snap); got != len(full) {
		t.Fatalf("countTranscriptSnapshotMessages=%d, want %d", got, len(full))
	}
	for start := 0; start <= len(full)+1; start++ {
		for end := start; end <= len(full)+1; end++ {
			got := transcriptFallbackSnapshotMessagesRange(snap, start, end)
			want := sliceChatWebMessages(full, start, end)
			if len(got) != len(want) {
				t.Fatalf("[%d,%d): len=%d, want %d", start, end, len(got), len(want))
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("[%d,%d): [%d]=%+v, want %+v", start, end, i, got[i], want[i])
				}
			}
		}
	}
}

func TestSessionTranscriptMessagesRangeMatchesFull(t *testing.T) {
	messages := windowEquivalenceSessionMessages()
	full := sessionTranscriptFallbackMessages(&ChatSession{Messages: messages})
	if full == nil {
		t.Fatal("会话 transcript 全量提取不应为空")
	}
	if got := countSessionTranscriptMessages(messages); got != len(full) {
		t.Fatalf("countSessionTranscriptMessages=%d, want %d", got, len(full))
	}
	for start := 0; start <= len(full)+1; start++ {
		for end := start; end <= len(full)+1; end++ {
			got := sessionTranscriptMessagesRange(messages, start, end)
			want := sliceChatWebMessages(full, start, end)
			if len(got) != len(want) {
				t.Fatalf("[%d,%d): len=%d, want %d", start, end, len(got), len(want))
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("[%d,%d): [%d]=%+v, want %+v", start, end, i, got[i], want[i])
				}
			}
		}
	}
}

// TestBuildChatWebScreenSnapshotWindowedAllocatesWindowOnly 用长会话锁定
// 「窗口化提取不随总量分配」：5000 条消息取 40 条时，返回切片的 cap 必须与
// 窗口同阶（旧实现为全量切片的子切片，cap >= total），且响应元信息不被二次裁剪。
func TestBuildChatWebScreenSnapshotWindowedAllocatesWindowOnly(t *testing.T) {
	const total = 5000
	messages := make([]types.Message, 0, total)
	for i := 0; i < total; i++ {
		role := "assistant"
		if i%2 == 0 {
			role = "user"
		}
		messages = append(messages, types.Message{Role: role, Content: fmt.Sprintf("m%d", i)})
	}
	withWebTestSession(t, &ChatSession{Messages: messages})

	snap := buildChatWebScreenSnapshotFor(chatWebMessageWindow{Limit: 40})
	if snap.MessageWindow == nil || snap.MessageWindow.Total != total {
		t.Fatalf("message_window = %+v, want Total=%d", snap.MessageWindow, total)
	}
	if len(snap.Messages) != 40 || len(snap.Lines) != 40 {
		t.Fatalf("窗口内 messages=%d lines=%d, want 40/40", len(snap.Messages), len(snap.Lines))
	}
	if cap(snap.Messages) >= total {
		t.Fatalf("cap(messages)=%d 与 total=%d 同阶：窗口化提取未生效（仍保留全量底层数组）", cap(snap.Messages), total)
	}
	if got := snap.Messages[39].Content; got != fmt.Sprintf("m%d", total-1) {
		t.Fatalf("窗口末条 = %q, want m%d", got, total-1)
	}

	// JSON 路径不得对已裁剪的快照二次套用窗口（否则 message_window.Total 会退化为窗口长度）。
	body, err := marshalChatWebScreenJSONWindow(chatWebMessageWindow{Before: total - 10, Limit: 5})
	if err != nil {
		t.Fatalf("marshalChatWebScreenJSONWindow: %v", err)
	}
	var parsed struct {
		Messages []chatWebScreenMessage    `json:"messages"`
		Window   *chatWebMessageWindowInfo `json:"message_window"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("screen JSON invalid: %v", err)
	}
	if parsed.Window == nil || parsed.Window.Total != total || parsed.Window.Start != total-15 || parsed.Window.End != total-10 {
		t.Fatalf("message_window = %+v, want {Total:%d Start:%d End:%d}", parsed.Window, total, total-15, total-10)
	}
	if len(parsed.Messages) != 5 || parsed.Messages[0].Content != fmt.Sprintf("m%d", total-15) {
		t.Fatalf("messages = %+v, want 5 条自 m%d", parsed.Messages, total-15)
	}
}

// BenchmarkChatWebScreenSnapshotWindowVsFull 量化长会话下取一页窗口的两种方式：
//   - windowed：窗口化提取（先计数再按区间提取，O(窗口)）
//   - full_then_slice：历史实现（构造全量快照后再切片，O(总量)）
//
// 运行：go test ./cmd/aicli/commands/ -run '^$' -bench WindowVsFull -benchmem
func BenchmarkChatWebScreenSnapshotWindowVsFull(b *testing.B) {
	const total = 5000
	messages := make([]types.Message, 0, total)
	for i := 0; i < total; i++ {
		role := "assistant"
		if i%2 == 0 {
			role = "user"
		}
		messages = append(messages, types.Message{Role: role, Content: fmt.Sprintf("m%d 长会话正文内容", i)})
	}
	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return &ChatSession{Messages: messages} }
	b.Cleanup(func() { chatDebugDisplaySessionProvider = old })

	window := chatWebMessageWindow{Limit: 40}
	b.Run("windowed", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if snap := buildChatWebScreenSnapshotFor(window); len(snap.Messages) != 40 {
				b.Fatalf("窗口内消息数 = %d, want 40", len(snap.Messages))
			}
		}
	})
	b.Run("full_then_slice", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			snap := buildChatWebScreenSnapshotFull()
			windowChatWebMessages(snap, window)
			if len(snap.Messages) != 40 {
				b.Fatalf("窗口内消息数 = %d, want 40", len(snap.Messages))
			}
		}
	})
}
