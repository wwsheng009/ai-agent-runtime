package commands

// Q2 决策钉（普查 §7.12）：`llm.request.started` 在**重放**里的读取面。
//
// 已确立的事实（代码 + 实测，见 §7.12）：
//   - `applyLLMStarted` 只做一件事：`beginAssistantRequest`（encoder.go:1541-1547）；
//   - `beginAssistantRequest` 的输入完全来自 `assistantRequestIdentityFromEvent`
//     （encoder.go:2651-2666），后者只读 payload 的 6 个键
//     （step / stream_id / llm_request_id / turn_id / logical_turn_id / trace_id）
//     加 envelope 的 TraceID；
//   - 该行的**唯一可观测作用**是登记 `latestRequestByScope` / `requestAliases`
//     （两个 map 全仓库只在 encoder.go 内被读写），且登记结果不进任何 Item。
//
// ⇒ 两种形态的结论（本文件把两者都钉住）：
//  1. **「保留行 + 只解这 6 个键」按构造等价**（§7.2-3 的紧结构体模式）——
//     即使 payload 里有 12 MB 的非 identity 内容也不影响渲染模型；
//  2. **「掏空 payload」不是普遍安全的**——登记键来自 payload，掏空后登记退化为
//     `scope:<scope>`，只有在"每个 scope 的第一条 fallback 行之前已有 identifying 行"
//     的排序下才看不出差别。危险排序下它与「整行丢弃」发散形态**完全相同**。
//
// 这是 L1.2 动 `llm.request.*` 的前置条件（§7.9 的「2-llm 悬空」用例）。
// 若本文件变红，先判断是"读取面变了"还是"登记语义变了"，**不要**直接改断言：
//   - TestStartedIdentityOnlyPayloadIsReplayEquivalent 红 ⇒ 有人给 started 加了新的读取面，
//     紧结构体解码必须同步扩键（否则 L1.2 上线后会丢信息）；
//   - TestStartedEmptyPayloadIsNotUniversallySafe 红 ⇒ 登记语义已变，此时"直接掏空 payload"
//     可能已成为安全选项（应重新评估 L1.2 的形态）。

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

const (
	startedScopeID  = "turn-started-dependency"
	startedStepID   = "1"
	startedStreamID = "stream-started-dependency"
	startedReqID    = "req-started-dependency"
)

// startedIdentityKeys 是 applyLLMStarted 的**完整读取面**（encoder.go:2651-2666）。
// 这份清单是契约：encoder 侧扩了读取面，这里必须同步。
var startedIdentityKeys = []string{"step", "stream_id", "llm_request_id", "turn_id", "logical_turn_id", "trace_id"}

// startedPayload 造一份接近真实的 started payload。
//
// identityOnly=false 时附带一批非 identity 键（model / messages / tools…）——
// 真实日志里 started 的 12.04 MB 几乎全在这些键上，而它们对渲染模型零贡献。
func startedPayload(identityOnly bool) map[string]interface{} {
	payload := map[string]interface{}{
		"step":            startedStepID,
		"stream_id":       startedStreamID,
		"llm_request_id":  startedReqID,
		"turn_id":         startedScopeID,
		"logical_turn_id": startedScopeID,
		"trace_id":        startedScopeID,
	}
	if identityOnly {
		// identityOnly：只保留 startedIdentityKeys 列出的键（= 紧结构体解码的产物）。
		out := make(map[string]interface{}, len(startedIdentityKeys))
		for _, key := range startedIdentityKeys {
			out[key] = payload[key]
		}
		return out
	}
	payload["model"] = "gpt-5.4"
	payload["message_count"] = 42
	payload["context_prompt_tokens"] = 12345
	payload["messages"] = []interface{}{map[string]interface{}{"role": "user", "content": "hello"}}
	payload["tools"] = []interface{}{map[string]interface{}{"name": "read_file"}}
	return payload
}

func startedEvent(payload map[string]interface{}) runtimeevents.Event {
	return runtimeevents.Event{Type: "llm.request.started", TraceID: startedScopeID, Payload: payload}
}

// startedIdentifyingRow 是能自行绑定身份的行（step / stream 齐备）。
func startedIdentifyingRow(text string) runtimeevents.Event {
	return runtimeevents.Event{
		Type: "assistant.reasoning",
		Payload: map[string]interface{}{
			"text": text, "reasoning": text, "delta": text,
			"step": startedStepID, "stream_id": startedStreamID,
			"llm_request_id": startedReqID, "turn_id": startedScopeID,
		},
	}
}

// startedFallbackRow 是"无 step、无 stream"的行：只能靠 latestRequestByScope 回落
// （encoder.go:2720-2753）。它的落点因此**依赖**同 scope 内此前的登记。
func startedFallbackRow(text string) runtimeevents.Event {
	return runtimeevents.Event{
		Type:    "assistant_message",
		Payload: map[string]interface{}{"content": text, "turn_id": startedScopeID},
	}
}

// startedCorpusSafeOrdering：identifying 行先到（登记已由它写满），fallback 行后到。
func startedCorpusSafeOrdering(started map[string]interface{}) []runtimeevents.Event {
	return []runtimeevents.Event{
		startedEvent(started),
		startedIdentifyingRow("think-1"),
		startedFallbackRow("fallback-after"),
		startedIdentifyingRow("think-2"),
		startedFallbackRow("fallback-last"),
	}
}

// startedCorpusFallbackFirst：**危险排序**——无 step 的 fallback 行先到，
// 此时该行的落点只由 started 的预登记决定。
func startedCorpusFallbackFirst(started map[string]interface{}) []runtimeevents.Event {
	return []runtimeevents.Event{
		startedEvent(started),
		startedFallbackRow("fallback-first"),
		startedIdentifyingRow("think-1"),
		startedFallbackRow("fallback-last"),
	}
}

// startedWithoutStartedEvent 返回剔除 started 行后的副本。
func startedWithoutStartedEvent(events []runtimeevents.Event) []runtimeevents.Event {
	out := make([]runtimeevents.Event, 0, len(events))
	for _, ev := range events {
		if ev.Type == "llm.request.started" {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// startedReplayModel 走与生产同形的重放路径（写 JSONL → replayEventLog）取渲染模型。
func startedReplayModel(t *testing.T, events []runtimeevents.Event) *encoding.RenderModel {
	t.Helper()
	arm := replayTrimArm(t, writeTrimReplayLog(t, events), true)
	model := arm.renderModelSnapshot()
	if model == nil {
		t.Fatal("nil render model")
	}
	return model
}

func startedModelJSON(t *testing.T, model *encoding.RenderModel) string {
	t.Helper()
	blob, err := json.Marshal(model)
	if err != nil {
		t.Fatalf("marshal render model: %v", err)
	}
	return string(blob)
}

// startedContractEqual 按仓库既有等价契约比较两个渲染模型
// （ID / Seq / Kind / Head / Status + Tail，**不含** clock 派生的 Created/Updated）。
func startedContractEqual(want, got *encoding.RenderModel) (bool, string) {
	if want == nil || got == nil {
		return false, "nil model"
	}
	if len(want.Items) != len(got.Items) {
		return false, "item count differs"
	}
	for i := range want.Items {
		w, g := want.Items[i], got.Items[i]
		if w == nil || g == nil {
			return false, "nil item"
		}
		if w.ID != g.ID {
			return false, "item " + w.ID + ": ID differs"
		}
		if w.Seq != g.Seq {
			return false, "item " + w.ID + ": Seq differs"
		}
		if w.Kind != g.Kind {
			return false, "item " + w.ID + ": Kind differs"
		}
		if w.Head != g.Head {
			return false, "item " + w.ID + ": Head differs"
		}
		if w.Status != g.Status {
			return false, "item " + w.ID + ": Status differs"
		}
	}
	wTail, gTail := want.Tail, got.Tail
	if (wTail == nil) != (gTail == nil) {
		return false, "Tail nil-ness differs"
	}
	if wTail != nil && gTail != nil && (wTail.ItemID != gTail.ItemID || wTail.Seq != gTail.Seq) {
		return false, "Tail differs"
	}
	return true, ""
}

func startedDescribeItems(model *encoding.RenderModel) string {
	var sb strings.Builder
	for i, it := range model.Items {
		if it == nil {
			continue
		}
		fmt.Fprintf(&sb, "\n    [%d] %s %s %s", i, it.ID, it.Kind, it.Head)
	}
	return sb.String()
}

// TestStartedIdentityOnlyPayloadIsReplayEquivalent 钉住 L1.2 的正确形态：
// 「保留行 + 只解 identity 的 6 个键」与「全量解码」产出的渲染模型**逐字节相同**
// （连 clock 派生的 Created/Updated 都不动，因为行数与 Encode 调用次数不变）。
func TestStartedIdentityOnlyPayloadIsReplayEquivalent(t *testing.T) {
	full := startedReplayModel(t, startedCorpusSafeOrdering(startedPayload(false)))
	identityOnly := startedReplayModel(t, startedCorpusSafeOrdering(startedPayload(true)))

	if fullJSON, gotJSON := startedModelJSON(t, full), startedModelJSON(t, identityOnly); fullJSON != gotJSON {
		t.Fatalf("只解 identity 键后渲染模型不再逐字节相同（items %d vs %d）\n  full:%s\n  identity-only:%s",
			len(full.Items), len(identityOnly.Items), startedDescribeItems(full), startedDescribeItems(identityOnly))
	}
}

// TestStartedEmptyPayloadIsNotUniversallySafe 钉住**为什么不能**把 started 的 payload 直接掏空。
//
// 危险排序下：带 payload 时 fallback-first 落进 step cell（与 think-1 同格），
// 掏空 payload 后登记退化为 scope:<scope>，该行另起一格 —— 且发散形态与
// 「整行丢弃 started」**完全一致**，这正好证明 payload 里的 identity 字段
// 就是登记的依据（payload 不可整体丢弃，只可"只读 6 键"）。
func TestStartedEmptyPayloadIsNotUniversallySafe(t *testing.T) {
	full := startedReplayModel(t, startedCorpusFallbackFirst(startedPayload(false)))
	emptied := startedReplayModel(t, startedCorpusFallbackFirst(map[string]interface{}{}))
	without := startedReplayModel(t, startedWithoutStartedEvent(startedCorpusFallbackFirst(startedPayload(false))))

	if equal, _ := startedContractEqual(full, emptied); equal {
		t.Fatalf("掏空 started payload 后仍与全量等价（items=%d）：登记语义已变，"+
			"此时「直接掏空 payload」可能已成为安全选项，应重新评估 L1.2 的形态而不是改本测试", len(full.Items))
	}
	if equal, why := startedContractEqual(emptied, without); !equal {
		t.Fatalf("掏空 payload 与整行丢弃的发散形态不同（%s）：emptied=%d items, without=%d items"+
			"\n  emptied:%s\n  without:%s",
			why, len(emptied.Items), len(without.Items), startedDescribeItems(emptied), startedDescribeItems(without))
	}
	t.Logf("危险排序下：全量 payload=%d items，掏空 payload=%d items（与整行丢弃同形）——"+
		"故 L1.2 只能走「只解 identity 键」，不能掏空 payload", len(full.Items), len(emptied.Items))
}

// ---------------------------------------------------------------------------
// L1.2 oracle：紧结构体便宜解码 == 全量解码
// ---------------------------------------------------------------------------

// TestEventLogStartedFastValuesMatchFullDecode 钉住 L1.2 设计的前提：
// 紧结构体解出的 6 个值必须与全量解码 map 里的值**逐字相同**（类型也相同）。
//
// 这是"便宜路径不需要复刻 payloadString"这一论断的直接证据：转换仍由编码器
// 在 apply 期用同一份 payloadString 执行，两臂因此同源。
func TestEventLogStartedFastValuesMatchFullDecode(t *testing.T) {
	lines := []string{
		// 真实写侧形状：step 是数字（internal/agent/loop.go:2038）。
		`{"type":"llm.request.started","trace_id":"trace-env","payload":{"step":1,"stream_id":"s-1","llm_request_id":"r-1","turn_id":"t-1","logical_turn_id":"t-1","trace_id":"t-1"}}`,
		// step 是字符串（部分调用方）。
		`{"type":"llm.request.started","trace_id":"trace-env","payload":{"step":"2","stream_id":"s-2","llm_request_id":"r-2","turn_id":"t-2"}}`,
		// 非 identity 键（含 12 MB 级的 messages）必须不参与解码。
		`{"type":"llm.request.started","trace_id":"trace-env","payload":{"step":3,"model":"gpt-5","messages":[{"role":"user","content":"x"}],"tools":[{"name":"read_file"}]}}`,
		// 空 payload：与"键缺失"同形。
		`{"type":"llm.request.started","trace_id":"trace-env","payload":{}}`,
	}
	for _, line := range lines {
		fast, ok := decodeEventLogStartedFast([]byte(line))
		if !ok {
			t.Fatalf("便宜解码失败（该形状必须走便宜路径）: %s", line)
		}
		var full runtimeevents.Event
		if err := json.Unmarshal([]byte(line), &full); err != nil {
			t.Fatalf("全量解码失败: %v", err)
		}
		for _, key := range startedIdentityKeys {
			want := full.Payload[key]
			var got interface{}
			switch key {
			case "step":
				got = fast.Step
			case "stream_id":
				got = fast.StreamID
			case "llm_request_id":
				got = fast.RequestID
			case "turn_id":
				got = fast.TurnID
			case "logical_turn_id":
				got = fast.LogicalTurnID
			case "trace_id":
				got = fast.TraceIDKey
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("键 %q 的值与全量解码不同: cheap=%#v (%T) full=%#v (%T)\n  line=%s",
					key, got, got, want, want, line)
			}
		}
		if fast.EnvelopeTraceID != full.TraceID {
			t.Fatalf("envelope TraceID 不同: cheap=%q full=%q", fast.EnvelopeTraceID, full.TraceID)
		}
	}
}

// TestEventLogStartedFastHitsRealShape 是"便宜路径在真实日志上真的命中"的
// 防退化断言：写侧把 step 写成数字，紧结构体若用 string 声明，这里会 ok=false
// （且线上只会表现为"永远回落"，计数器看不出异常）。
func TestEventLogStartedFastHitsRealShape(t *testing.T) {
	line := []byte(`{"type":"llm.request.started","trace_id":"t","payload":{"step":1,"stream_id":"s"}}`)
	fast, ok := decodeEventLogStartedFast(line)
	if !ok {
		t.Fatal("真实形状（数字 step）未命中便宜路径：紧结构体的值类型不得为 string")
	}
	if _, isFloat := fast.Step.(float64); !isFloat {
		t.Fatalf("step 解出 %T，期望 float64（与全量解码一致）", fast.Step)
	}
}

// TestEventLogStartedFastFailsClosed 钉住回落契约：任何识别/解码失败都必须
// 返回 ok=false（调用方据此回落全量解析），绝不能"猜一个空值"继续。
func TestEventLogStartedFastFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"other-type", `{"type":"llm.request.finished","payload":{"step":"1"}}`},
		{"truncated-json", `{"type":"llm.request.started","payload":{"step":"1"`},
		{"not-json", `garbage`},
		{"injection-without-type", `{"foo":"bar"}`},
		// envelope 的 trace_id 非字符串：全量解码对同一行同样失败（Event.TraceID
		// 是 string），故必须回落以保持错误语义与行号。
		{"envelope-trace-not-string", `{"type":"llm.request.started","trace_id":7,"payload":{}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := decodeEventLogStartedFast([]byte(tc.line)); ok {
				t.Fatalf("便宜解码对 %s 返回 ok=true，必须 fail-closed", tc.line)
			}
		})
	}
}

// TestEventLogStartedFastMatchesFullDecode 是 L1.2 的 oracle：在**登记会被
// 后续 fallback 行消费**的语料上（否则测试会退化成"两臂都没内容可丢"的空跑），
// 最小事件与全量解码事件必须产出**逐字节相同**的渲染模型。
func TestEventLogStartedFastMatchesFullDecode(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]interface{}
	}{
		{"numeric-step", map[string]interface{}{
			"step": 1, "stream_id": startedStreamID, "llm_request_id": startedReqID,
			"turn_id": startedScopeID, "logical_turn_id": startedScopeID, "trace_id": startedScopeID,
		}},
		{"string-step", map[string]interface{}{
			"step": startedStepID, "stream_id": startedStreamID, "llm_request_id": startedReqID,
			"turn_id": startedScopeID,
		}},
		{"empty-payload", map[string]interface{}{}},
		{"non-identity-keys-present", map[string]interface{}{
			"step": 2, "stream_id": startedStreamID, "llm_request_id": startedReqID,
			"turn_id": startedScopeID,
			"model":    "gpt-5", "message_count": 42, "context_prompt_tokens": 12345,
			"messages": []interface{}{map[string]interface{}{"role": "user", "content": "x"}},
			"tools":    []interface{}{map[string]interface{}{"name": "read_file"}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 危险排序：fallback 行紧跟 started，它的落点只能由 started 的
			// 预登记决定（文件头 §2 的"整行丢弃"发散形态）。两臂走**生产同形**
			// 的重放路径（写 JSONL → replayEventLog），而不是手搓编码器。
			events := []runtimeevents.Event{
				startedEvent(tc.payload),
				startedFallbackRow("fallback-first"),
				startedIdentifyingRow("think-1"),
				startedFallbackRow("fallback-last"),
			}
			logPath := writeTrimReplayLog(t, events)
			cheapArm := replayTrimArm(t, logPath, true)
			fullArm := replayTrimArm(t, logPath, false)

			// 便宜路径必须真的命中，否则本用例退化成 A/A 空跑。
			if _, _, cheapStarted, _ := cheapArm.eventLogTrimStats(); cheapStarted == 0 {
				t.Fatalf("便宜路径未命中（payload=%v），用例失效", tc.payload)
			}
			if _, _, fullStarted, _ := fullArm.eventLogTrimStats(); fullStarted != 0 {
				t.Fatal("B 臂不应命中便宜路径")
			}

			fullModel, cheapModel := fullArm.renderModelSnapshot(), cheapArm.renderModelSnapshot()
			if fullJSON, cheapJSON := startedModelJSON(t, fullModel), startedModelJSON(t, cheapModel); fullJSON != cheapJSON {
				t.Fatalf("最小事件与全量解码的渲染模型不再逐字节相同（items %d vs %d）\n  full:%s\n  cheap:%s",
					len(fullModel.Items), len(cheapModel.Items),
					startedDescribeItems(fullModel), startedDescribeItems(cheapModel))
			}
		})
	}
}
