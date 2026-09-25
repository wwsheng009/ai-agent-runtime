package commands

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// L1.1 恢复重放便宜路径的等价性测试（chat_eventlog_trim.go）。
//
// 本文件是 L1.1 的**放行条件**：便宜路径只允许更快，不允许产生任何可观察差异。
// 四组等价性测试 + A/B 重放门禁：
//
//	TestEventLogNextLineMatchesBufioScanner     行切分（含 lineIndex 语义）
//	TestEventLogTypePrefixIsFailSafeOracle      类型前缀（读错即回落）
//	TestEventLogTrimAlwaysSkipIsExactlyOpNone   白名单 ⟺ classify == opNone
//	TestEventLogProgressFastMatchesFullDecode   tool.progress 便宜解码 oracle
//	TestReplayTrimToolProgressGuards            守卫双向一致性（encoder 侧契约）
//	TestEventLogReplayTrimABEquivalence         同一日志 A/B 重放三臂等价（门禁）
//
// 术语：A 臂 = 便宜路径开启（默认）；B 臂 = AICLI_RESUME_REPLAY_TRIM=0（旧路径）。

// ---------------------------------------------------------------------------
// 行切分等价性
// ---------------------------------------------------------------------------

// referenceScanLines 复刻 L1.1 之前的行切分语义（旧实现逐字）：
//
//	lineIndex := 0
//	for scanner.Scan() {
//	    lineIndex++
//	    line := strings.TrimSpace(scanner.Text())
//	    if line == "" { continue }
//	}
//
// 返回非空行文本序列。bufio 64KB token 上限导致的错误在此显式失败，
// 因为本测试的语料不该触发它（超长行由 TestEventLogNextLineBeyondBufioLimit
// 单独覆盖）。
func referenceScanLines(t *testing.T, raw []byte) []string {
	t.Helper()
	var out []string
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			out = append(out, line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reference scanner: %v", err)
	}
	return out
}

// trimScanLines 是便宜路径的行切分（等价于重放循环中的调用方式）。
func trimScanLines(raw []byte) []string {
	var out []string
	for start := 0; start < len(raw); {
		line, next := eventLogNextLine(raw, start)
		start = next
		if len(line) == 0 {
			continue
		}
		out = append(out, string(line))
	}
	return out
}

// TestEventLogNextLineMatchesBufioScanner 断言零拷贝行切分与旧的
// bufio.Scanner + strings.TrimSpace 语义逐行等价（含 \r\n、空行、
// 无尾换行、纯空白行等边界）。
func TestEventLogNextLineMatchesBufioScanner(t *testing.T) {
	corpus := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"only-newline", "\n"},
		{"only-crlf", "\r\n"},
		{"single-no-newline", "a"},
		{"single-with-newline", "a\n"},
		{"crlf", "a\r\nb\r\n"},
		{"mixed-eol", "a\r\nb\nc"},
		{"blank-lines", "a\n\n\nb"},
		{"leading-blank", "\n\na"},
		{"trailing-blanks", "a\n\n\n"},
		{"spaced", "  spaced  \n\ttabbed\t\n"},
		{"whitespace-only-lines", "\n   \n\t\n a \n"},
		{"cr-only-not-separator", "a\rb\n"},
		{"json-lines", "{\"type\":\"x\"}\n{\"type\":\"y\"}\n"},
		{"no-trailing-newline-last-line", "{\"a\":1}\n{\"b\":2}"},
	}
	for _, tc := range corpus {
		t.Run(tc.name, func(t *testing.T) {
			want := referenceScanLines(t, []byte(tc.raw))
			got := trimScanLines([]byte(tc.raw))
			if len(want) != len(got) {
				t.Fatalf("line count: got %d want %d\n got=%q\nwant=%q", len(got), len(want), got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("line %d: got %q want %q", i, got[i], want[i])
				}
			}
		})
	}
}

// TestEventLogNextLineConsumesEveryPhysicalLine 钉住 lineIndex 语义：
// 旧实现先 lineIndex++ 再判空，因此**空行也占用行号**，错误信息里的
// "event log line N" 是物理行号。便宜路径必须保持一致（否则用户按错误
// 信息定位日志会差行）。A/B 对照由 referenceScanLines 的调用方式体现：
// 这里直接对同一语料统计"被消费的物理行数"。
func TestEventLogNextLineConsumesEveryPhysicalLine(t *testing.T) {
	raw := []byte("{\"a\":1}\n\n{\"b\":2}\n   \n{\"c\":3}")
	// 物理行：1 {"a":1} / 2 空 / 3 {"b":2} / 4 空白 / 5 {"c":3}
	wantIndexes := map[string]int{"{\"a\":1}": 1, "{\"b\":2}": 3, "{\"c\":3}": 5}
	lineIndex := 0
	seen := map[string]int{}
	for start := 0; start < len(raw); {
		lineIndex++
		line, next := eventLogNextLine(raw, start)
		start = next
		if len(line) == 0 {
			continue
		}
		seen[string(line)] = lineIndex
	}
	if lineIndex != 5 {
		t.Fatalf("consumed %d physical lines, want 5", lineIndex)
	}
	for text, want := range wantIndexes {
		if got := seen[text]; got != want {
			t.Fatalf("line %q reported index %d, want %d", text, got, want)
		}
	}
}

// TestEventLogNextLineBeyondBufioLimit 证明 64KB token 上限已消除：
// 同一输入下 bufio.Scanner 必然报 token too long，而便宜路径正常切分。
// 真实会话最长行 65,516B（距 64KB 上限仅 20B），这是**恢复被整段中止**
// 的隐患，不是性能优化。
func TestEventLogNextLineBeyondBufioLimit(t *testing.T) {
	blob := strings.Repeat("x", 200*1024)
	raw := []byte("{\"type\":\"custom.blob\",\"payload\":{\"blob\":\"" + blob + "\"}}\n{\"type\":\"after\"}\n")

	ref := bufio.NewScanner(bytes.NewReader(raw))
	var refErr error
	for ref.Scan() {
	}
	refErr = ref.Err()
	if refErr == nil {
		t.Fatal("前置条件不成立：bufio.Scanner 未在 200KB 行上报错")
	}
	if !strings.Contains(refErr.Error(), "token too long") {
		t.Fatalf("bufio 错误 = %v, 期望 token too long", refErr)
	}

	got := trimScanLines(raw)
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2", len(got))
	}
	if !strings.HasPrefix(got[0], "{\"type\":\"custom.blob\"") || !strings.HasSuffix(got[0], "\"}}") {
		t.Fatal("超长行切分结果损坏")
	}
	if got[1] != "{\"type\":\"after\"}" {
		t.Fatalf("超长行之后的行 = %q", got[1])
	}
	if !bytes.Contains([]byte(got[0]), []byte(blob)) {
		t.Fatal("超长行内容不完整")
	}
}

// ---------------------------------------------------------------------------
// 类型前缀：失败即回落
// ---------------------------------------------------------------------------

// TestEventLogTypePrefixIsFailSafeOracle 断言前缀扫描的两个方向：
//  1. 对 json.Marshal(runtimeevents.Event) 的真实产物，前缀结果 == 全量解码的 Type；
//  2. 对一切"形状不符"的输入返回 ""（→ 调用方必须回落全量解析）。
//
// 方向 2 是安全性核心：宁可回落（慢），也绝不误判（错）。
func TestEventLogTypePrefixIsFailSafeOracle(t *testing.T) {
	events := []runtimeevents.Event{
		{Type: runtimechat.EventSessionStart},
		{Type: "tool.progress", Payload: map[string]interface{}{"tool_call_id": "c1", "message": "m"}},
		{Type: runtimechat.EventToolFinished, SessionID: "s", Payload: map[string]interface{}{"tool_call_id": "c1"}},
		{Type: "context.tool_schema.frozen"},
		{Type: "llm.retry", Payload: map[string]interface{}{"attempt": 2}},
		{Type: runtimechat.EventAssistantMessage, Payload: map[string]interface{}{"text": "hi"}},
	}
	for _, ev := range events {
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal %s: %v", ev.Type, err)
		}
		if got := eventLogTypePrefix(raw); got != ev.Type {
			t.Fatalf("prefix(%s) = %q, want %q", raw, got, ev.Type)
		}
		// 与全量解码交叉验证（不是自证：另一条独立路径）。
		var full runtimeevents.Event
		if err := json.Unmarshal(raw, &full); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		if eventLogTypePrefix(raw) != full.Type {
			t.Fatalf("prefix 与全量解码不一致: %q vs %q", eventLogTypePrefix(raw), full.Type)
		}
	}

	failSafe := []struct {
		name string
		line string
	}{
		{"empty", ""},
		{"injection-record", `{"user_input":"hello"}`},
		{"leading-space", ` {"type":"tool.progress"}`},
		{"spaced-separator", `{"type" : "tool.progress"}`},
		{"reordered-fields", `{"session_id":"s","type":"tool.progress"}`},
		{"truncated", `{"type":"tool.pro`},
		{"non-json", `not json at all`},
		{"escaped-type", `{"type":"tool\u002eprogress"}`},
		{"type-key-not-first-value", `{"type":null}`},
		{"too-long-type", `{"type":"` + strings.Repeat("a", 65) + `"}`},
	}
	for _, tc := range failSafe {
		t.Run(tc.name, func(t *testing.T) {
			if got := eventLogTypePrefix([]byte(tc.line)); got != "" {
				t.Fatalf("prefix(%q) = %q, 期望空串（必须回落全量解析）", tc.line, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// tool.progress 便宜解码 oracle
// ---------------------------------------------------------------------------

// newTrimTestEncoderWithOpenTool 返回一个已登记且**未终态**工具 cell 的编码器。
// 两个编码器喂入同一事件序列后状态逐字相同，因此可分别喂"全量事件"与
// "最小事件"再比较模型——这是不依赖重推导 toolProgressText 的行为等价性证明。
func newTrimTestEncoderWithOpenTool(callID string) *encoding.EventEncoder {
	enc := newChatRuntimeEventBridge(&ChatSession{}).renderEncoder
	enc.Encode(runtimeevents.Event{
		Type:    "tool.requested",
		Payload: map[string]interface{}{"tool_call_id": callID, "tool_name": "trim-test"},
	})
	return enc
}

// progressEventForLog 把事件按"日志往返"方式还原：marshal 再 unmarshal，
// 与旧重放路径喂给编码器的对象逐字相同（数值变 float64 等）。
func progressEventForLog(t *testing.T, ev runtimeevents.Event) runtimeevents.Event {
	t.Helper()
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round runtimeevents.Event
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return round
}

// TestEventLogProgressFastMatchesFullDecode 断言：在 ToolProgressAttachable
// 为真的状态下，最小事件（只带 tool_call_id + 优先级最高的文本键）与全量事件
// 产生的渲染模型**逐项相同**；且便宜解码读出的 callID / 文本与全量一致。
func TestEventLogProgressFastMatchesFullDecode(t *testing.T) {
	const callID = "call-trim-1"
	variants := []struct {
		name    string
		payload map[string]interface{}
	}{
		{"message-only", map[string]interface{}{"tool_call_id": callID, "message": "from message"}},
		{"progress-only", map[string]interface{}{"tool_call_id": callID, "progress": "from progress"}},
		{"detail-only", map[string]interface{}{"tool_call_id": callID, "detail": "from detail"}},
		{"status-only", map[string]interface{}{"tool_call_id": callID, "status": "from status"}},
		{"priority-message-wins", map[string]interface{}{
			"tool_call_id": callID, "message": "msg", "progress": "prog", "detail": "det", "status": "stat",
		}},
		{"priority-skips-empty-message", map[string]interface{}{
			"tool_call_id": callID, "message": "", "progress": "prog", "detail": "det",
		}},
		{"priority-detail-before-status", map[string]interface{}{
			"tool_call_id": callID, "detail": "det", "status": "stat",
		}},
		{"extra-keys-ignored-in-upsert", map[string]interface{}{
			"tool_call_id": callID, "message": "m",
			"tool_name": "other", "args": map[string]interface{}{"x": 1}, "display_head": "DH",
		}},
		{"null-values", map[string]interface{}{
			"tool_call_id": callID, "message": nil, "progress": "p",
		}},
	}
	for _, tc := range variants {
		t.Run(tc.name, func(t *testing.T) {
			ev := progressEventForLog(t, runtimeevents.Event{Type: eventLogProgressFastType, Payload: tc.payload})
			raw, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			fast, ok := decodeEventLogProgressFast(raw)
			if !ok {
				t.Fatalf("便宜解码失败（payload=%v），该形状必须走便宜路径", tc.payload)
			}
			if fast.CallID != callID {
				t.Fatalf("fast.CallID = %q, want %q", fast.CallID, callID)
			}

			// 全量臂。
			fullEnc := newTrimTestEncoderWithOpenTool(callID)
			if !fullEnc.ToolProgressAttachable(fast.CallID) {
				t.Fatal("前置条件：工具 cell 应为可 attach")
			}
			fullEnc.Encode(ev)

			// 便宜臂（最小事件）。
			cheapEnc := newTrimTestEncoderWithOpenTool(callID)
			cheapEnc.Encode(eventLogProgressEvent(fast.CallID, fast.progressText()))

			assertRenderModelEquivalent(t, fullEnc.Snapshot(), cheapEnc.Snapshot())
		})
	}
}

// TestEventLogProgressFastDecodeFailsClosed 断言一切"非字符串 / 形状不符"的
// payload 都让便宜解码返回 ok=false（调用方因此回落全量解析）。
// 这些情况下便宜路径**不允许**猜值。
//
// 注意 `{"type":"tool.progress"}`（完全无 payload）**不在此列**：它的形状
// 确实就是 tool.progress，便宜解码返回 ok=true 且 callID 为空。安全网在
// 重放侧的守卫而非解码器，由 TestEventLogProgressFastEmptyCallIDCaughtByGuard
// 单独覆盖——这是"宁可慢，不可丢"的边界。
func TestEventLogProgressFastDecodeFailsClosed(t *testing.T) {
	const callID = "call-trim-2"
	cases := []struct {
		name string
		line string
	}{
		{"numeric-progress", `{"type":"tool.progress","payload":{"tool_call_id":"` + callID + `","progress":42}}`},
		{"bool-message", `{"type":"tool.progress","payload":{"tool_call_id":"` + callID + `","message":true}}`},
		{"object-message", `{"type":"tool.progress","payload":{"tool_call_id":"` + callID + `","message":{"a":1}}`},
		{"array-detail", `{"type":"tool.progress","payload":{"tool_call_id":"` + callID + `","detail":["a"]}}`},
		{"payload-not-object", `{"type":"tool.progress","payload":"str"}`},
		{"other-type", `{"type":"tool.completed","payload":{"tool_call_id":"` + callID + `","message":"m"}}`},
		{"truncated-json", `{"type":"tool.progress","payload":{"tool_call_id":"` + callID + `"`},
		{"not-json", `garbage`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := decodeEventLogProgressFast([]byte(tc.line)); ok {
				t.Fatalf("便宜解码对 %s 返回 ok=true，必须 fail-closed", tc.line)
			}
		})
	}
}

// TestEventLogProgressFastEmptyCallIDCaughtByGuard 闭合"无 payload"的缺口：
// 便宜解码 ok=true 且 callID 为空时，重放侧的 ToolProgressAttachable 必然为假，
// 因此仍会回落全量解析——最小事件（payload 只剩两个键）绝不会被误用。
func TestEventLogProgressFastEmptyCallIDCaughtByGuard(t *testing.T) {
	fast, ok := decodeEventLogProgressFast([]byte(`{"type":"tool.progress"}`))
	if !ok {
		t.Fatal("形状确实是 tool.progress，便宜解码应当成功")
	}
	if fast.CallID != "" || fast.progressText() != "" {
		t.Fatalf("无 payload 时应解出空 callID/空文本，得到 callID=%q text=%q",
			fast.CallID, fast.progressText())
	}
	enc := newChatRuntimeEventBridge(&ChatSession{}).renderEncoder
	if enc.ToolProgressAttachable(fast.CallID) {
		t.Fatal("空 callID 必须被守卫拒绝（否则最小事件会丢掉 payload 的其余键）")
	}
}

// TestEventLogProgressEventKeepsUpsertResultStable 钉住最小事件的键集合：
// 只有 tool_call_id 与 message 两个键（applyToolProgress 在 upsert 分支的
// 读取面）。新增键意味着 apply 侧读取面扩大 → 本测试失败要求同步评估。
func TestEventLogProgressEventKeepsUpsertResultStable(t *testing.T) {
	ev := eventLogProgressEvent("c9", "text")
	if ev.Type != eventLogProgressFastType {
		t.Fatalf("Type = %q", ev.Type)
	}
	if len(ev.Payload) != 2 {
		t.Fatalf("payload 键数 = %d (%v)，期望 2（tool_call_id + message）", len(ev.Payload), ev.Payload)
	}
	if ev.Payload["tool_call_id"] != "c9" || ev.Payload["message"] != "text" {
		t.Fatalf("payload = %v", ev.Payload)
	}
	if empty := eventLogProgressEvent("", ""); len(empty.Payload) != 0 {
		t.Fatalf("空参数应产出空 payload，得到 %v", empty.Payload)
	}
}

// ---------------------------------------------------------------------------
// 守卫：ToolProgressAttachable 必须与 applyToolProgress 的落点一致
// ---------------------------------------------------------------------------

// TestReplayTrimToolProgressGuards 双向验证 encoder.ToolProgressAttachable
// 与 applyToolProgress 实际分支的一致性（encoder.go 的注释把本测试列为契约）。
//
// 可观察差异：upsert 分支**不新增 item**（就地更新既有 tool cell）；
// 回落 applySystem 分支**会 append 一个新 item**。因此
// "ToolProgressAttachable == 编码后 item 数不变" 就是守卫的同构性判据。
//
// 方向性很关键：applyToolProgress 的读取面在 upsert 分支收敛、在 applySystem
// 分支发散，所以只有该判据为真时，重放才能安全地丢弃 payload 的其它键。
func TestReplayTrimToolProgressGuards(t *testing.T) {
	const callID = "call-guard-1"
	progressPayload := func() map[string]interface{} {
		return map[string]interface{}{"tool_call_id": callID, "message": "working"}
	}

	t.Run("unknown-call-id-routes-to-system", func(t *testing.T) {
		enc := newChatRuntimeEventBridge(&ChatSession{}).renderEncoder
		if enc.ToolProgressAttachable(callID) {
			t.Fatal("未登记 call 不得 attachable")
		}
		before := len(enc.Snapshot().Items)
		enc.Encode(runtimeevents.Event{Type: eventLogProgressFastType, Payload: progressPayload()})
		if after := len(enc.Snapshot().Items); after != before+1 {
			t.Fatalf("守卫为假时应回落 applySystem 并新增 item：before=%d after=%d", before, after)
		}
	})

	t.Run("open-call-upserts-in-place", func(t *testing.T) {
		enc := newTrimTestEncoderWithOpenTool(callID)
		if !enc.ToolProgressAttachable(callID) {
			t.Fatal("已登记未终态 call 必须 attachable")
		}
		before := len(enc.Snapshot().Items)
		enc.Encode(runtimeevents.Event{Type: eventLogProgressFastType, Payload: progressPayload()})
		if after := len(enc.Snapshot().Items); after != before {
			t.Fatalf("守卫为真时应就地 upsert，不得新增 item：before=%d after=%d", before, after)
		}
	})

	t.Run("terminal-call-routes-to-system", func(t *testing.T) {
		enc := newTrimTestEncoderWithOpenTool(callID)
		enc.Encode(runtimeevents.Event{
			Type:    runtimechat.EventToolFinished,
			Payload: map[string]interface{}{"tool_call_id": callID, "display_head": "done"},
		})
		if enc.ToolProgressAttachable(callID) {
			t.Fatal("终态 call 不得 attachable")
		}
		before := len(enc.Snapshot().Items)
		enc.Encode(runtimeevents.Event{Type: eventLogProgressFastType, Payload: progressPayload()})
		if after := len(enc.Snapshot().Items); after != before+1 {
			t.Fatalf("终态后应回落 applySystem 并新增 item：before=%d after=%d", before, after)
		}
	})

	t.Run("empty-and-nil-call-id", func(t *testing.T) {
		enc := newTrimTestEncoderWithOpenTool(callID)
		if enc.ToolProgressAttachable("") {
			t.Fatal("空 callID 不得 attachable")
		}
		var nilEnc *encoding.EventEncoder
		if nilEnc.ToolProgressAttachable(callID) {
			t.Fatal("nil encoder 不得 attachable")
		}
	})
}

// ---------------------------------------------------------------------------
// Tier A 白名单 ⟺ classify == opNone
// ---------------------------------------------------------------------------

// assertRenderItemsEquivalent 只比较 Item 序列（身份 / 顺序 / Kind / 文本 /
// 状态），**忽略 Tail**。
//
// 为什么不能直接用 assertRenderModelEquivalent：Encode 对 opNone 事件虽不产生
// 任何 Item 变更，却仍会把 model.Tail 从 nil 置为 &{ItemID:"" Seq:0}（编码器
// 既有行为，与本改动无关，两臂同样受影响）。这恰好从反面证明了 Tier A
// **不能**跳过 Encode 调用——省掉它会让 Tail/clock 与实时路径分叉。
func assertRenderItemsEquivalent(t *testing.T, want, got *encoding.RenderModel) {
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
		if w.ID != g.ID || w.Seq != g.Seq || w.Kind != g.Kind || w.Head != g.Head {
			t.Fatalf("item %d 不一致:\n want (%s #%d %s %q)\n got  (%s #%d %s %q)",
				i, w.ID, w.Seq, w.Kind, w.Head, g.ID, g.Seq, g.Kind, g.Head)
		}
		if w.Status != g.Status {
			t.Fatalf("item %d 状态不一致: want %v got %v", i, w.Status, g.Status)
		}
	}
}

// countItemKind 统计渲染模型中某类 Kind 的单元格数量。
func countItemKind(model *encoding.RenderModel, kind encoding.ItemKind) int {
	if model == nil {
		return 0
	}
	n := 0
	for _, it := range model.Items {
		if it != nil && it.Kind == kind {
			n++
		}
	}
	return n
}

// wantTrimAlwaysSkip 是 Tier A 白名单的**评审清单**：任何增删都必须同时改
// 这里与 chat_eventlog_trim.go，并在提交说明里给出 classify 依据。
//
// 与 isSilentSystemEventType 的差集是故意的（见 chat_eventlog_trim.go 注释）：
// session_end / session_interrupted 也"静默"，但 classify 在静默检查之前就把
// 它们判成 opSessionEnd（收尾所有未完成流式项），跳过会让崩溃/中断会话的
// 转录永远停在 mutable 状态。
var wantTrimAlwaysSkip = []string{
	runtimechat.EventSessionStart,
	runtimechat.EventSessionCompactSkipped,
	runtimechat.EventContextReconciled,
	"planning.started",
	"subagent.batch.started",
	"subagent.started",
	"task.started",
	"team.task.started",
	"context.tool_schema.frozen",
	"tool.reduced",
	"llm.retry",
}

// TestEventLogTrimAlwaysSkipIsExactlyOpNone 用编码器自身的可观察行为验证
// 白名单里每个类型都是 opNone：Encode 仍被调用（EncodeCount+1）但不产生
// 任何 Item 变更、也不计入 UnknownCount。Tier A 跳过解析的正当性即在此。
func TestEventLogTrimAlwaysSkipIsExactlyOpNone(t *testing.T) {
	got := make([]string, 0, len(eventLogTrimAlwaysSkip))
	for evType := range eventLogTrimAlwaysSkip {
		got = append(got, evType)
	}
	sort.Strings(got)
	want := append([]string(nil), wantTrimAlwaysSkip...)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("Tier A 白名单成员变更：\n got = %q\n want = %q\n"+
			"新增前必须确认 classify 返回 opNone（见 chat_eventlog_trim.go 的不变量 A）", got, want)
	}

	for _, evType := range wantTrimAlwaysSkip {
		t.Run(evType, func(t *testing.T) {
			enc := newChatRuntimeEventBridge(&ChatSession{}).renderEncoder
			before := enc.Snapshot()
			beforeStats := enc.Stats()

			cs := enc.Encode(runtimeevents.Event{Type: evType})

			after := enc.Snapshot()
			afterStats := enc.Stats()
			if cs != nil && len(cs.Changes) != 0 {
				t.Fatalf("%s 不是 opNone：跳过解析会丢失 %d 个变更", evType, len(cs.Changes))
			}
			// 只比较 Item：Tail 的变化是 Encode 的既有副作用，见
			// assertRenderItemsEquivalent 的说明。
			assertRenderItemsEquivalent(t, before, after)
			if afterStats.EncodeCount != beforeStats.EncodeCount+1 {
				t.Fatalf("%s EncodeCount %d → %d：Tier A 仍须逐条 Encode（clock/Tail 对齐）",
					evType, beforeStats.EncodeCount, afterStats.EncodeCount)
			}
			if afterStats.UnknownCount != beforeStats.UnknownCount {
				t.Fatalf("%s 使 UnknownCount 增加，说明它并非已知静默类型", evType)
			}
		})
	}
}

// TestEventLogTrimExcludesTerminalSessionEvents 钉住"故意差集"：
// session_end / session_interrupted 永不进入 Tier A，且它们确实不是 no-op。
func TestEventLogTrimExcludesTerminalSessionEvents(t *testing.T) {
	for _, banned := range []string{runtimechat.EventSessionEnd, runtimechat.EventSessionInterrupted} {
		if _, ok := eventLogTrimAlwaysSkip[banned]; ok {
			t.Fatalf("%s 不得进入 Tier A 白名单：classify 先判 opSessionEnd（收尾未完成流），"+
				"跳过会让崩溃/中断会话的转录永远停在 mutable", banned)
		}
		if isChatRenderDataPlaneSuppressedEvent(banned) {
			t.Fatalf("前置假设失效：%s 现已被渲染数据面抑制，需重新评估 Tier A 分析", banned)
		}
	}

	// 行为证据：开一条未完成流后，session_end 必须产生收尾变更。
	// 推理事件的具体 payload 键由编码器内部约定，这里同时填多个候选键，
	// 只要求"确实建立了可变流式项"（下面前置条件即为此把关）。
	enc := newChatRuntimeEventBridge(&ChatSession{}).renderEncoder
	enc.Encode(runtimeevents.Event{
		Type: runtimechat.EventAssistantReasoning,
		Payload: map[string]interface{}{
			"text": "thinking", "reasoning": "thinking", "delta": "thinking", "content": "thinking",
		},
	})
	opened := enc.Snapshot()
	if len(opened.Items) == 0 {
		t.Fatal("前置条件不成立：assistant.reasoning 未建立渲染项（payload 键约定可能已变更）")
	}
	cs := enc.Encode(runtimeevents.Event{Type: runtimechat.EventSessionEnd})
	if cs == nil || len(cs.Changes) == 0 {
		t.Fatal("session_end 未收尾未完成流：它不可能是 opNone，故不得进入 Tier A")
	}
}

// ---------------------------------------------------------------------------
// A/B 重放门禁
// ---------------------------------------------------------------------------

// trimReplayCorpus 使用的三个工具调用身份（断言侧按同名引用）。
const (
	// §5 用例 2（悬空尾部）：只 started、永不收尾。progress 因此全部
	// attachable 走便宜分支，且累积文本会留在最终模型里可断言。
	trimCorpusOpenCall = "call-open-1"
	// 正常收尾，但收尾后仍有晚到 progress：守卫为假 → 必须回落全量解码。
	trimCorpusClosedCall = "call-closed-1"
	// §5 用例 3（回执独存）：无 started/finished，只有回执行 → 必须重建一行。
	trimCorpusReceiptCall = "call-receipt-1"
)

// trimReplayCorpus 构造覆盖便宜路径全部分支的可重放日志：
// Tier A（session_start / planning.started / llm.retry / tool_schema.frozen）、
// tool.progress 便宜分支（悬空尾部 call）、tool.progress 回落分支（终态后晚到）、
// 回执独存重建、全量路径（tool.requested / tool.completed / 未知类型）、
// 以及渲染数据面抑制类型（dynamic_status）。
//
// 悬空尾部（§5 用例 2）是刻意设计：applyToolProgress 的 upsert 分支只保留
// 「首行 + 最新一条 detail」，收尾事件的 display_head 又会覆盖整段，因此只有
// **不收尾**的调用才能把便宜分支的文本留在最终快照里供断言（否则用例会静默
// 退化成"两臂都没内容可丢"的空跑）。
func trimReplayCorpus() []runtimeevents.Event {
	evs := []runtimeevents.Event{
		{Type: runtimechat.EventSessionStart, SessionID: "s-ab"},
		{Type: "planning.started", SessionID: "s-ab"},
		// §5 用例 2：悬空尾部——只 started，后面没有 completed/receipt。
		{Type: runtimechat.EventToolStarted, SessionID: "s-ab", Payload: map[string]interface{}{
			"tool_call_id": trimCorpusOpenCall, "tool_name": "shell",
		}},
	}
	// L1.2：llm.request.started 便宜分支，刻意用**真实形状**（step 是数字，
	// 见 internal/agent/loop.go:2038），并附带 12 MB 级别的非 identity 键
	// （messages）以证明它们不参与解码。紧跟一条**无 step/stream 的 fallback
	// 行**：它的落点由 started 的登记决定——若便宜路径丢了 identity，
	// 两臂的 item 身份就会发散。这条语料因此同时钉住"便宜分支被命中"与
	// "登记语义未变"，避免退化成空跑。
	evs = append(evs,
		runtimeevents.Event{Type: eventLogStartedFastType, TraceID: "trace-ab", Payload: map[string]interface{}{
			"step":            1,
			"stream_id":       "stream-ab",
			"llm_request_id":  "req-ab",
			"turn_id":         "turn-ab",
			"logical_turn_id": "turn-ab",
			"trace_id":        "trace-ab",
			"model":           "gpt-5",
			"messages":        []interface{}{map[string]interface{}{"role": "user", "content": "ignored"}},
		}},
		runtimeevents.Event{Type: runtimechat.EventAssistantMessage, Payload: map[string]interface{}{
			"content": "started-fallback", "turn_id": "turn-ab",
		}},
	)
	for i := 0; i < 8; i++ {
		evs = append(evs, runtimeevents.Event{Type: eventLogProgressFastType, Payload: map[string]interface{}{
			"tool_call_id": trimCorpusOpenCall,
			"message":      fmt.Sprintf("step %d", i),
			// progress 键优先级低于 message：两臂都必须忽略它。
			"progress":  fmt.Sprintf("ignored progress %d", i),
			"tool_name": "shell",
		}})
	}
	// §5 用例 1（正常会话，含收尾）：started → progress → completed。
	evs = append(evs,
		runtimeevents.Event{Type: runtimechat.EventToolStarted, SessionID: "s-ab", Payload: map[string]interface{}{
			"tool_call_id": trimCorpusClosedCall, "tool_name": "shell",
		}},
		runtimeevents.Event{Type: eventLogProgressFastType, Payload: map[string]interface{}{
			"tool_call_id": trimCorpusClosedCall, "message": "working",
		}},
		runtimeevents.Event{Type: runtimechat.EventToolFinished, SessionID: "s-ab", Payload: map[string]interface{}{
			"tool_call_id": trimCorpusClosedCall, "display_head": "done",
		}},
	)
	// 终态之后：守卫为假 → 重放必须回落全量解码（late 文本仍须进模型）。
	for i := 0; i < 3; i++ {
		evs = append(evs, runtimeevents.Event{Type: eventLogProgressFastType, Payload: map[string]interface{}{
			"tool_call_id": trimCorpusClosedCall, "message": fmt.Sprintf("late %d", i),
		}})
	}
	// §5 用例 3：回执独存——没有原始 started/finished，重放必须以回执重建一行。
	evs = append(evs, runtimeevents.Event{
		Type: runtimechat.EventToolReceiptRecorded, SessionID: "s-ab",
		Payload: map[string]interface{}{
			"tool_call_id": trimCorpusReceiptCall, "tool_name": "shell",
			"output": "receipt output", "success": true,
		},
	})
	return append(evs,
		runtimeevents.Event{Type: "llm.retry", Payload: map[string]interface{}{"attempt": 2}},
		runtimeevents.Event{Type: "context.tool_schema.frozen"},
		runtimeevents.Event{Type: "tool.reduced", Payload: map[string]interface{}{"reducer": "r"}},
		runtimeevents.Event{Type: runtimechat.EventContextReconciled},
		runtimeevents.Event{Type: chatWebDynamicStatusBusEvent, Payload: map[string]interface{}{"status": "busy"}},
		runtimeevents.Event{Type: "custom.unknown.event", Payload: map[string]interface{}{"x": 1}},
		runtimeevents.Event{Type: runtimechat.EventSessionEnd, SessionID: "s-ab"},
	)
}

// writeTrimReplayLog 把事件写成与生产同形的 JSONL（每行一个 json.Marshal(Event)）。
func writeTrimReplayLog(t *testing.T, events []runtimeevents.Event) string {
	t.Helper()
	var buf bytes.Buffer
	for _, ev := range events {
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal %s: %v", ev.Type, err)
		}
		buf.Write(raw)
		buf.WriteByte('\n')
	}
	path := filepath.Join(t.TempDir(), "runtime-events.jsonl")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	return path
}

// replayTrimArm 在同一日志上跑一个重放臂（trimEnabled 控制 kill-switch）。
func replayTrimArm(t *testing.T, logPath string, trimEnabled bool) *chatRuntimeEventBridge {
	t.Helper()
	if trimEnabled {
		t.Setenv(eventLogTrimEnv, "1")
	} else {
		t.Setenv(eventLogTrimEnv, "0")
	}
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.eventLogPathOverride = logPath
	if _, err := bridge.replayEventLog(); err != nil {
		t.Fatalf("replay(trim=%v): %v", trimEnabled, err)
	}
	return bridge
}

// joinedItemHeads 汇总渲染模型里所有单元格的 Head（用于断言可见文本）。
func joinedItemHeads(model *encoding.RenderModel) string {
	if model == nil {
		return ""
	}
	var sb strings.Builder
	for _, it := range model.Items {
		if it == nil {
			continue
		}
		sb.WriteString(it.Head)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// TestEventLogReplayTrimABEquivalence 是 L1.1 的门禁：同一条日志在
// A 臂（便宜路径开启）与 B 臂（kill-switch 关闭 = 旧路径）下重放，
// 必须得到逐项相同的渲染模型与相同的编码统计，且两臂各自的分支覆盖
// 必须可观测（否则测试会静默退化为空跑）。
func TestEventLogReplayTrimABEquivalence(t *testing.T) {
	logPath := writeTrimReplayLog(t, trimReplayCorpus())

	armA := replayTrimArm(t, logPath, true)
	armB := replayTrimArm(t, logPath, false)

	// 1) 模型等价（身份 / 顺序 / Kind / Tail）。
	assertRenderModelEquivalent(t, armB.renderModelSnapshot(), armA.renderModelSnapshot())
	assertRenderModelSeqMonotonic(t, armA.renderModelSnapshot())

	// 2) 编码统计等价：Tier A 跳过解析也不能少调一次 Encode。
	statsA, statsB := armA.renderEncoderStats(), armB.renderEncoderStats()
	if statsA.EncodeCount != statsB.EncodeCount {
		t.Fatalf("EncodeCount 不一致: A=%d B=%d", statsA.EncodeCount, statsB.EncodeCount)
	}
	if statsA.UnknownCount != statsB.UnknownCount {
		t.Fatalf("UnknownCount 不一致: A=%d B=%d", statsA.UnknownCount, statsB.UnknownCount)
	}
	if statsA.EncodeCount == 0 {
		t.Fatal("前置条件不成立：日志一条都没进编码器")
	}

	// 3) 分支覆盖：A 臂四条便宜路径都要被真实命中，B 臂必须全为 0。
	trimmed, cheapProgress, cheapStarted, cheapFallback := armA.eventLogTrimStats()
	if trimmed == 0 {
		t.Fatal("A 臂未命中任何 Tier A 跳过行，语料失效")
	}
	if cheapProgress == 0 {
		t.Fatal("A 臂未命中 tool.progress 便宜解析，语料失效")
	}
	if cheapStarted == 0 {
		t.Fatal("A 臂未命中 llm.request.started 便宜解析（L1.2），语料失效")
	}
	if cheapFallback == 0 {
		t.Fatal("A 臂未命中守卫回落（终态后晚到的 progress），语料失效")
	}
	if bt, bp, bs, bf := armB.eventLogTrimStats(); bt != 0 || bp != 0 || bs != 0 || bf != 0 {
		t.Fatalf("B 臂不应走便宜路径，得到 trimmed=%d cheap_progress=%d cheap_started=%d fallback=%d", bt, bp, bs, bf)
	}

	// 4) 可见文本证据：便宜分支与回落分支的文本都必须真正进模型。
	//
	// 悬空尾部调用的单元格是"首行 + 最新一条 detail"（upsert 分支语义），
	// 因此只断言最后一条 step；这已经足以证明便宜分支的文本进了模型。
	modelA := armA.renderModelSnapshot()
	heads := joinedItemHeads(modelA)
	for _, want := range []string{"step 7", "late 0", "late 1", "late 2", "done"} {
		if !strings.Contains(heads, want) {
			t.Fatalf("模型缺少 %q（便宜路径或回落路径丢内容）\n--- heads ---\n%s", want, heads)
		}
	}
	// 便宜路径只保留优先级最高的 message：progress 键应被忽略（与全量一致）。
	if strings.Contains(heads, "ignored progress") {
		t.Fatalf("message 优先级未生效，progress 键文本泄漏进模型\n--- heads ---\n%s", heads)
	}
	// 5) §5 用例 1/2/3 的调用都要各自成行：悬空尾部、正常收尾、回执独存重建。
	if n := countItemKind(modelA, encoding.KindToolCall); n != 3 {
		t.Fatalf("工具单元格数 = %d，期望 3（悬空尾部 / 正常收尾 / 回执独存重建）\n--- heads ---\n%s", n, heads)
	}
	if n := countItemKind(armB.renderModelSnapshot(), encoding.KindToolCall); n != 3 {
		t.Fatalf("B 臂工具单元格数 = %d，期望 3", n)
	}
}

// TestEventLogReplayTrimKillSwitch 钉住 kill-switch 取值语义
// （与 AICLI_SQLITE_WARMUP 同约定：只有明确的关闭值才关闭）。
func TestEventLogReplayTrimKillSwitch(t *testing.T) {
	cases := []struct {
		value    string
		disabled bool
	}{
		{"", false}, {"1", false}, {"true", false}, {"TRUE", false},
		{"yes", false}, {"on", false}, {"garbage", false},
		{"0", true}, {"false", true}, {"FALSE", true},
		{"no", true}, {"off", true}, {" Off ", true},
	}
	for _, tc := range cases {
		if got := eventLogTrimDisabledValue(tc.value); got != tc.disabled {
			t.Fatalf("eventLogTrimDisabledValue(%q) = %v, want %v", tc.value, got, tc.disabled)
		}
	}
}

// TestEventLogReplayTrimMatchesLegacyOnCorruptLog 断言损坏日志下两臂的
// **错误语义与行号**一致：前缀扫描识别失败即回落，绝不吞掉解析错误。
func TestEventLogReplayTrimMatchesLegacyOnCorruptLog(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"truncated-json", "{\"type\":\"tool.progress\",\"payload\":{"},
		{"not-json", "this is not json"},
		{"empty-injection", "{\"foo\":\"bar\"}"},
		{"wrong-typed-field", "{\"type\":\"tool.progress\",\"trace_id\":123}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 第 1 行合法（含空行占位），第 2 行空行，第 3 行损坏 → 行号应为 3。
			body := "{\"type\":\"session_start\"}\n\n" + tc.body + "\n"
			path := filepath.Join(t.TempDir(), "runtime-events.jsonl")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}

			run := func(trimEnabled bool) (uint64, error) {
				if trimEnabled {
					t.Setenv(eventLogTrimEnv, "1")
				} else {
					t.Setenv(eventLogTrimEnv, "0")
				}
				bridge := newChatRuntimeEventBridge(&ChatSession{})
				bridge.eventLogPathOverride = path
				n, err := bridge.replayEventLog()
				if err == nil {
					t.Fatalf("trim=%v: 损坏日志必须报错", trimEnabled)
				}
				_, _, _, failures := bridge.eventLogStats()
				if failures != 1 {
					t.Fatalf("trim=%v: failures=%d want 1", trimEnabled, failures)
				}
				return n, err
			}

			countA, errA := run(true)
			countB, errB := run(false)
			if errA.Error() != errB.Error() {
				t.Fatalf("错误不一致：\n A=%v\n B=%v", errA, errB)
			}
			if countA != countB {
				t.Fatalf("已重放行数不一致: A=%d B=%d", countA, countB)
			}
			if !strings.Contains(errA.Error(), "event log line 3") {
				t.Fatalf("错误未指向物理行 3（空行占用行号）：%v", errA)
			}
		})
	}
}

// trimStartedBenchLine 造一行与真实长会话同形的 llm.request.started：
// §1 表 1 的实测均值是 4.7 KB/行（12.04 MB / 2,561 行），其中 identity 6 键
// 之外全是 messages / tools 这类大键——便宜路径根本不解码它们。
func trimStartedBenchLine() []byte {
	messages := make([]interface{}, 0, 40)
	for i := 0; i < 40; i++ {
		messages = append(messages, map[string]interface{}{
			"role": "user", "content": strings.Repeat("x", 100),
		})
	}
	ev := runtimeevents.Event{
		Type:    eventLogStartedFastType,
		TraceID: "trace-bench",
		Payload: map[string]interface{}{
			"step": 7, "stream_id": "stream-bench", "llm_request_id": "req-bench",
			"turn_id": "turn-bench", "logical_turn_id": "turn-bench", "trace_id": "turn-bench",
			"model": "gpt-5.4", "messages": messages,
			"tools": []interface{}{map[string]interface{}{"name": "read_file"}},
		},
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		panic(err)
	}
	return raw
}

// BenchmarkEventLogStartedFastDecode 量化 L1.2 的单行收益（同一行的
// 全量解码 vs 便宜解码）。字节/op 用 SetBytes 报出，便于按真实日志
// 12.04 MB 直接折算。
func BenchmarkEventLogStartedFastDecode(b *testing.B) {
	line := trimStartedBenchLine()
	b.SetBytes(int64(len(line)))
	b.Run("full", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var ev runtimeevents.Event
			if err := json.Unmarshal(line, &ev); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("cheap", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, ok := decodeEventLogStartedFast(line); !ok {
				b.Fatal("便宜解码未命中")
			}
		}
	})
}
