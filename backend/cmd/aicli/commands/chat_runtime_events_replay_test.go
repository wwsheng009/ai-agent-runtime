package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// testRuntimeEventLogEvents 返回一组互不冲突的 system 事件
// （applySystem 每次 append 新 item，模型稳定增长）。
func testRuntimeEventLogEvents() []runtimeevents.Event {
	return []runtimeevents.Event{
		{
			Type:      runtimechat.EventQuestionAsked,
			SessionID: "session-1",
			TraceID:   "trace-1",
			Payload:   map[string]interface{}{"prompt": "choose a model"},
		},
		{
			Type:      runtimechat.EventApprovalRequested,
			SessionID: "session-1",
			TraceID:   "trace-2",
			Payload:   map[string]interface{}{"request_id": "req-1"},
		},
		{
			Type:      runtimechat.EventApprovalResolved,
			SessionID: "session-1",
			TraceID:   "trace-3",
			Payload:   map[string]interface{}{"request_id": "req-1", "allow": true},
		},
	}
}

// assertRenderModelEquivalent 断言两个模型身份与顺序一致（ID/Seq/Kind/Tail）。
func assertRenderModelEquivalent(t *testing.T, want, got *encoding.RenderModel) {
	t.Helper()
	if want == nil || got == nil {
		t.Fatalf("nil model: want=%v got=%v", want, got)
	}
	if len(want.Items) != len(got.Items) {
		t.Fatalf("item count=%d want %d", len(got.Items), len(want.Items))
	}
	for i := range want.Items {
		w, g := want.Items[i], got.Items[i]
		if w == nil || g == nil {
			t.Fatalf("item %d nil: want=%v got=%v", i, w, g)
		}
		if w.ID != g.ID || w.Seq != g.Seq || w.Kind != g.Kind {
			t.Fatalf("item %d mismatch: want (%s #%d %s) got (%s #%d %s)",
				i, w.ID, w.Seq, w.Kind, g.ID, g.Seq, g.Kind)
		}
	}
	if want.Tail == nil || got.Tail == nil {
		if want.Tail != got.Tail {
			t.Fatalf("tail mismatch: want=%+v got=%+v", want.Tail, got.Tail)
		}
		return
	}
	if want.Tail.ItemID != got.Tail.ItemID || want.Tail.Seq != got.Tail.Seq {
		t.Fatalf("tail mismatch: want (%s #%d) got (%s #%d)",
			want.Tail.ItemID, want.Tail.Seq, got.Tail.ItemID, got.Tail.Seq)
	}
}

// assertRenderModelSeqMonotonic 断言重建模型 Seq 严格单调递增（重放顺序稳定）。
func assertRenderModelSeqMonotonic(t *testing.T, model *encoding.RenderModel) {
	t.Helper()
	if model == nil {
		t.Fatal("nil model")
	}
	last := uint64(0)
	for i, it := range model.Items {
		if it == nil {
			t.Fatalf("item %d nil", i)
		}
		if i > 0 && it.Seq <= last {
			t.Fatalf("Seq 非单调: item %d seq=%d <= prev %d", i, it.Seq, last)
		}
		last = it.Seq
	}
}

// TestChatRuntimeEventBridge_EventLogPersistAndReplayRebuildsModel 验证 P5 数据面
// 闭环：进入编码器的事件持久化为 append-only 日志；新 bridge 重放日志后重建模型
// 与实时编码模型等价（身份/顺序/Tail 一致，Seq 单调）。
func TestChatRuntimeEventBridge_EventLogPersistAndReplayRebuildsModel(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")

	bridge1 := newChatRuntimeEventBridge(&ChatSession{})
	bridge1.eventLogPathOverride = logPath
	for _, ev := range testRuntimeEventLogEvents() {
		bridge1.encodeRenderModelEvent(ev)
	}

	path, count, replayed, failures := bridge1.eventLogStats()
	if path != logPath || count != 3 || replayed != 0 || failures != 0 {
		t.Fatalf("stats after encode: path=%q count=%d replayed=%d failures=%d", path, count, replayed, failures)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	if lines := len(strings.Split(strings.TrimSpace(string(raw)), "\n")); lines != 3 {
		t.Fatalf("event log lines=%d want 3", lines)
	}

	// 新 bridge（模拟会话重启）重放日志重建模型。
	bridge2 := newChatRuntimeEventBridge(&ChatSession{})
	bridge2.eventLogPathOverride = logPath
	replayedCount, err := bridge2.replayEventLog()
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replayedCount != 3 {
		t.Fatalf("replayed=%d want 3", replayedCount)
	}
	assertRenderModelEquivalent(t, bridge1.renderModelSnapshot(), bridge2.renderModelSnapshot())
	assertRenderModelSeqMonotonic(t, bridge2.renderModelSnapshot())
	_, _, replayed2, _ := bridge2.eventLogStats()
	if replayed2 != 3 {
		t.Fatalf("stats replayed=%d want 3", replayed2)
	}
}

// TestChatRuntimeEventBridge_EventLogAppendAfterRestart 验证重启后的新事件继续
// 追加到同一日志，且再重启重放仍与实时模型等价（含新事件）。
func TestChatRuntimeEventBridge_EventLogAppendAfterRestart(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")

	bridge1 := newChatRuntimeEventBridge(&ChatSession{})
	bridge1.eventLogPathOverride = logPath
	for _, ev := range testRuntimeEventLogEvents() {
		bridge1.encodeRenderModelEvent(ev)
	}

	bridge2 := newChatRuntimeEventBridge(&ChatSession{})
	bridge2.eventLogPathOverride = logPath
	if _, err := bridge2.replayEventLog(); err != nil {
		t.Fatalf("replay: %v", err)
	}
	bridge2.encodeRenderModelEvent(runtimeevents.Event{
		Type:      runtimechat.EventQuestionAnswered,
		SessionID: "session-1",
		TraceID:   "trace-4",
		Payload:   map[string]interface{}{"prompt": "choose a model", "answer": "gpt-test"},
	})

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	if lines := len(strings.Split(strings.TrimSpace(string(raw)), "\n")); lines != 4 {
		t.Fatalf("event log lines=%d want 4", lines)
	}

	bridge3 := newChatRuntimeEventBridge(&ChatSession{})
	bridge3.eventLogPathOverride = logPath
	if replayed, err := bridge3.replayEventLog(); err != nil || replayed != 4 {
		t.Fatalf("replay after append: replayed=%d err=%v", replayed, err)
	}
	assertRenderModelEquivalent(t, bridge2.renderModelSnapshot(), bridge3.renderModelSnapshot())
	assertRenderModelSeqMonotonic(t, bridge3.renderModelSnapshot())
}

// TestChatRuntimeEventBridge_EventLogDisabledWithoutLogger 验证无日志路径时
// 编码不落盘、不失败、不 panic。
func TestChatRuntimeEventBridge_EventLogDisabledWithoutLogger(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	for _, ev := range testRuntimeEventLogEvents() {
		bridge.encodeRenderModelEvent(ev)
	}
	path, count, replayed, failures := bridge.eventLogStats()
	if path != "" || count != 0 || replayed != 0 || failures != 0 {
		t.Fatalf("stats without logger: path=%q count=%d replayed=%d failures=%d", path, count, replayed, failures)
	}
	if tail := bridge.renderModelTail(); tail == nil {
		t.Fatal("model tail should exist even without event log")
	}
}

// TestChatRuntimeEventBridge_EventLogReplayToleratesMissingFile 验证日志不存在时
// 重放静默成功（返回 0 事件）。
func TestChatRuntimeEventBridge_EventLogReplayToleratesMissingFile(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.eventLogPathOverride = filepath.Join(t.TempDir(), "absent.jsonl")
	replayed, err := bridge.replayEventLog()
	if err != nil {
		t.Fatalf("replay missing log: %v", err)
	}
	if replayed != 0 {
		t.Fatalf("replayed=%d want 0", replayed)
	}
}

// TestChatRuntimeEventBridge_EventLogReplayRejectsCorruptLine 验证损坏日志行返回
// 明确错误而非静默吞掉。
func TestChatRuntimeEventBridge_EventLogReplayRejectsCorruptLine(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")
	if err := os.WriteFile(logPath, []byte("not-json\n"), 0o644); err != nil {
		t.Fatalf("write corrupt log: %v", err)
	}
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.eventLogPathOverride = logPath
	replayed, err := bridge.replayEventLog()
	if err == nil {
		t.Fatalf("corrupt log replay err=nil replayed=%d", replayed)
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("corrupt log error missing line info: %v", err)
	}
}
