package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// L1.1：会话恢复重放的便宜解析（trim / fast paths）。
//
// 背景（普查 L1.0，docs/e2e/resume-replay-census.md）：会话加载时
// replayEventLogWithLoadAuthorization 会对每一行做完整
// json.Unmarshal(runtimeevents.Event)。Event.Payload 是
// map[string]interface{}，逐行要分配 map 并对每个值做 interface 装箱；
// 61,465 行 / 91MB 的真实长会话实测（6 轮取最优）：
//
//	现行路径（Text()+[]byte() 双拷贝）  1010ms / 408MB 垃圾
//	零拷贝（行切片直接喂 json.Unmarshal） 965ms / 215MB 垃圾
//	全日志仅取 "type" 前缀                 6.5ms / 1.2MB
//	tool.progress 微型结构体解码          113ms / 7.4MB（全量解析同批 186ms）
//
// 本文件提供四条便宜路径，其余一切行（含所有未知类型）保持原路径：
//
//  1. 零拷贝行切分（eventLogNextLine）——行切片直接指向 raw，不再经过
//     Scanner.Text()/[]byte() 的两次拷贝；同时摆脱 bufio 64KB token 上限
//     （本机真实会话最长行 65,516B，距上限仅 20B，再长一点就会整段报
//     "token too long" 而中止恢复）。
//  2. Tier A 白名单（eventLogTrimAlwaysSkip）——编码器 classify 恒为
//     opNone、apply 为空实现，跳过解析对渲染模型零影响；apply 期仍用一个
//     仅含 Type 的事件走 Encode，保持 clock/EncodeCount/Tail 与实时路径一致。
//  3. tool.progress 便宜解析——applyToolProgress 在 upsert 分支只读
//     tool_call_id 与 toolProgressText 的 4 键优先级列表；解析期只解出这 5 个
//     键，apply 期再用编码器的真实状态确认"一定走 upsert 分支"
//     （EventEncoder.ToolProgressAttachable），否则回落全量解码。
//  4. llm.request.started 便宜解析（L1.2）——applyLLMStarted →
//     beginAssistantRequest → assistantRequestIdentityFromEvent
//     （encoder.go:2651-2666）只读 payload 的 6 个 identity 键 + envelope 的
//     TraceID；该 op 不产生任何 Item，登记结果（latestRequestByScope /
//     requestAliases）只影响**后续** fallback 行的落点。普查 §7.12 已用
//     非重言式用例证伪"掏空 payload"、并证明"只解这 6 键"按构造等价。
//     该类型占真实长会话字节数的 13.2%（12.04 MB / 91 MB），按字节计是
//     L1.1 之后最大的单一回收项。
//
// 不变量（由 chat_eventlog_trim_test.go 与普查文档共同维护）：
//
//	A. 白名单是显式常量，绝不从 isSilentSystemEventType 派生——后者含
//	   session_end / session_interrupted，它们不是 no-op（普查 §3 坑 1）。
//	B. 任何识别失败 / 未知类型 / 解码失败一律回落全量解析（宁可慢，不可丢）。
//	C. 便宜路径不得改变 apply 语义：Tier A 只跳过"读 payload"；tool.progress
//	   只在编码器确认 attachable 时用最小事件；llm.request.started 的 apply
//	   分支没有第二路径（读取面即 6 键 + envelope TraceID），因此不需要守卫
//	   ——它的等价性由 oracle 测试而非运行时判断保证。
//	D. 跳过 ≠ 失败：eventLogFailures 语义不变，另有独立计数（见
//	   chatRuntimeEventBridge.eventLogTrimStats）。
const (
	// eventLogTrimEnv 是恢复重放便宜路径的 kill-switch。默认开启；只有
	// 0/false/no/off（trim + lower）视为关闭（与 AICLI_SQLITE_WARMUP 同约定）。
	// 关闭后逐字节回到 M1 之前的解析路径，用于 A/B 归因与快速回滚。
	eventLogTrimEnv = "AICLI_RESUME_REPLAY_TRIM"

	// eventLogProgressFastType 是第一个做"便宜解码"的事件类型（L1.1）。它占
	// 真实长会话行数的 37.5%（23,045/61,465），且 apply 读取面收敛（见上）。
	eventLogProgressFastType = "tool.progress"

	// eventLogStartedFastType 是第二个便宜解码类型（L1.2）。占真实长会话
	// 字节数的 13.2%（12.04 MB / 91 MB）——按字节计是最大的单一回收项。
	// 读取面契约见文件头第 4 条与普查 §7.12。
	eventLogStartedFastType = "llm.request.started"
)

// eventLogTypePrefixBytes 是 json.Marshal(runtimeevents.Event) 的前缀：
// Event 的 Type 是结构体首字段（internal/events/bus.go），因此事件行
// 恒以 {"type":"<type>" 开头。注入记录（eventLogInjection）没有 type 字段，
// 前缀取不到 → 回落原路径。
var eventLogTypePrefixBytes = []byte(`{"type":"`)

// eventLogTrimDisabledValue 只有明确的关闭值才返回 true。
func eventLogTrimDisabledValue(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}

// eventLogTrimDisabled 读取 kill-switch（每次重放只判定一次）。
func eventLogTrimDisabled() bool {
	return eventLogTrimDisabledValue(os.Getenv(eventLogTrimEnv))
}

// eventLogTrimAlwaysSkip 是 Tier A 白名单：这些类型的 classify 恒为 opNone
// （encoder.go 的 isSilentSystemEventType 或 "llm.retry" 显式分支），
// apply(opNone) 为空实现，payload 与其余字段均不参与判定。
//
// ⚠️ 与 isSilentSystemEventType 的差集是**故意的**，不得"对齐":
// session_end / session_interrupted 也在 silent 名单里，但 classify 在
// silent 检查之前就把它们判成 opSessionEnd（收尾所有未完成流式项），
// 跳过会让崩溃/中断会话的转录永远停在 mutable 状态。
// 新增静默类型时，必须同时评估这里是否要加入（保守起见：不加）。
var eventLogTrimAlwaysSkip = map[string]struct{}{
	runtimechat.EventSessionStart:          {},
	runtimechat.EventSessionCompactSkipped: {},
	runtimechat.EventContextReconciled:     {},
	"planning.started":                     {},
	"subagent.batch.started":               {},
	"subagent.started":                     {},
	"task.started":                         {},
	"team.task.started":                    {},
	"context.tool_schema.frozen":           {},
	"tool.reduced":                         {},
	// llm.retry 不在 isSilentSystemEventType 里，但 classify 有独立分支
	// 直接返回 opNone（retry 信息走动态状态区，不进 transcript）——这里
	// 与 classify 对齐，而非与 silent 名单对齐。
	"llm.retry": {},
}

// eventLogTypePrefix 从行首读取事件类型；识别失败返回 ""（调用方必须回落
// 全量解析）。只接受长度 ≤64 且不含转义的类型名——真实类型名都远小于此，
// 超长/异常一律当识别失败处理。
func eventLogTypePrefix(line []byte) string {
	if !bytes.HasPrefix(line, eventLogTypePrefixBytes) {
		return ""
	}
	rest := line[len(eventLogTypePrefixBytes):]
	end := bytes.IndexByte(rest, '"')
	if end <= 0 || end > 64 {
		return ""
	}
	if bytes.IndexByte(rest[:end], '\\') >= 0 {
		return ""
	}
	return string(rest[:end])
}

// eventLogNextLine 手工切分下一行（零拷贝）。返回的切片直接指向 raw，
// 在 raw 生命周期内有效——这正是取消 bufio.Scanner 的目的（Scanner.Bytes()
// 的切片会在后续 Scan 中随缓冲区滑动失效）。
// 语义等价于原 strings.TrimSpace(scanner.Text()) 后再判空。
func eventLogNextLine(raw []byte, start int) (line []byte, next int) {
	if start >= len(raw) {
		return nil, len(raw)
	}
	if idx := bytes.IndexByte(raw[start:], '\n'); idx >= 0 {
		return bytes.TrimSpace(raw[start : start+idx]), start + idx + 1
	}
	return bytes.TrimSpace(raw[start:]), len(raw)
}

// eventLogProgressFast 是 tool.progress 的便宜解码结果：只取
// applyToolProgress 在 upsert 分支会读的键。
//
// ⚠️ 键集合是**契约**：与 encoder.go 的 toolCallID（payload["tool_call_id"]）
// 和 toolProgressText（message→progress→detail→status 取首个非空）一一对应。
// 若 applyToolProgress 将来新增读取键，必须同步这里并补 oracle 测试
// （TestEventLogProgressFastMatchesFullDecode / 真实语料用例）。
type eventLogProgressFast struct {
	CallID   string `json:"tool_call_id"`
	Message  string `json:"message"`
	Progress string `json:"progress"`
	Detail   string `json:"detail"`
	Status   string `json:"status"`
}

type eventLogProgressEnvelope struct {
	Type    string               `json:"type"`
	Payload eventLogProgressFast `json:"payload"`
}

// eventLogTrimEntry 是便宜路径行在重放 entries 里的载荷（entry.trim）。
// kind 非空 = Tier A 跳过解析的分类行；否则为 tool.progress 便宜解析行
// （rawLine 保留原始字节，apply 期守卫失败时回落全量解码）。
type eventLogTrimEntry struct {
	kind      string
	rawLine   []byte
	lineIndex int
	callID    string
	detail    string
	// started 非空 = llm.request.started 便宜解析行（L1.2）。三个分支互斥：
	// kind 非空 → Tier A；started 非空 → identity 最小事件；两者皆空且
	// trim 非空 → tool.progress。rawLine/lineIndex 仅在需要回落全量解码时使用。
	started *eventLogStartedFast
}

// decodeEventLogProgressFast 便宜解码一行 tool.progress。失败（含损坏 JSON、
// 字段类型不符）返回 ok=false，调用方必须回落全量解析以保持报错语义。
func decodeEventLogProgressFast(line []byte) (eventLogProgressFast, bool) {
	var env eventLogProgressEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return eventLogProgressFast{}, false
	}
	if env.Type != eventLogProgressFastType {
		return eventLogProgressFast{}, false
	}
	return env.Payload, true
}

// progressText 复刻 encoder.toolProgressText 的优先级序列。
func (f eventLogProgressFast) progressText() string {
	for _, value := range []string{f.Message, f.Progress, f.Detail, f.Status} {
		if value != "" {
			return value
		}
	}
	return ""
}

// eventLogProgressEvent 用便宜解码结果构造最小事件：Payload 只含
// applyToolProgress 在 upsert 分支真正读取的键（tool_call_id + 文本）。
//
// 调用方必须先用 EventEncoder.ToolProgressAttachable 确认 apply 会走 upsert
// 分支；否则 applyToolProgress 会回落 applySystem，而 applySystem 会读
// payload 的其它键——那时必须改用全量解码的事件。
//
// detail 已是 toolProgressText 的优先级结果（message→progress→detail→status
// 取首个非空），故这里放回优先级最高的 "message" 键：重放侧读取同一函数，
// 结果逐字相同，不必保留原始键名。
func eventLogProgressEvent(callID, detail string) runtimeevents.Event {
	payload := make(map[string]interface{}, 2)
	if callID != "" {
		payload["tool_call_id"] = callID
	}
	if detail != "" {
		payload["message"] = detail
	}
	return runtimeevents.Event{Type: eventLogProgressFastType, Payload: payload}
}

// ---------------------------------------------------------------------------
// L1.2：llm.request.started 便宜解码
// ---------------------------------------------------------------------------

// eventLogStartedFast 是 llm.request.started 的便宜解码结果：只取
// assistantRequestIdentityFromEvent（encoder.go:2651-2666）会读的 6 个
// payload 键，外加 envelope 的 TraceID。
//
// ⚠️ 值类型必须是 interface{} 而不是 string，理由是实测的写侧形状：
//   - internal/agent/loop.go:2038 把 "step" 写成**数字**（`for step := 1; …`
//     的 int），真实日志里因此是 `"step":1`。紧结构体若声明为 string，
//     json.Unmarshal 会因类型不符返回错误，便宜路径在真实日志上**永不命中**
//     ——而且只能靠"回落"掩盖，计数器上看不出异常。
//   - interface{} 解出的值与全量解码 map[string]interface{} 的值**逐字相同**
//     （同一 decoder、同一 JSON token：字符串→string、数字→float64、
//     布尔→bool、对象/数组→map/slice、null/缺失→nil）。
//     于是 payloadString 的转换（string / json.Number / float64 / bool /
//     json.Marshal）仍**只由编码器实现一份**，在 apply 期执行——便宜路径
//     不需要复刻任何转换语义，也就不存在"两份实现漂移"的风险。
//
// 键集合是契约：encoder 侧扩读取面时这里必须同步。
// chat_eventlog_started_dependency_test.go 的
// TestStartedIdentityOnlyPayloadIsReplayEquivalent 会因读取面变化而变红。
type eventLogStartedFast struct {
	Step          interface{} `json:"step"`
	StreamID      interface{} `json:"stream_id"`
	RequestID     interface{} `json:"llm_request_id"`
	TurnID        interface{} `json:"turn_id"`
	LogicalTurnID interface{} `json:"logical_turn_id"`
	TraceIDKey    interface{} `json:"trace_id"`

	// EnvelopeTraceID 是**envelope** 的 trace_id（与 payload 的 trace_id 是
	// 两个不同的 scope 来源，二者都会被 appendUniqueIdentity 收进 scopes）。
	// 由 decodeEventLogStartedFast 从外层填入，不参与本结构体的 JSON 解码。
	EnvelopeTraceID string `json:"-"`
}

// eventLogStartedEnvelope 是整行的紧结构体：envelope 只取 type 与 trace_id，
// payload 只取 6 个 identity 键。真实行里 payload 的 12 MB 内容（messages /
// tools / prompt 层）根本不进入解码路径。
//
// envelope 的 trace_id 声明为 string：若日志里它是非字符串，本解码失败并
// 回落全量解析，而全量解析对同一行同样会失败（Event.TraceID 是 string）
// ——错误语义与行号因此逐字保持。
type eventLogStartedEnvelope struct {
	Type    string              `json:"type"`
	TraceID string              `json:"trace_id"`
	Payload eventLogStartedFast `json:"payload"`
}

// decodeEventLogStartedFast 便宜解码一行 llm.request.started。失败（类型不符、
// 损坏 JSON、envelope 字段类型不符）返回 ok=false，调用方必须回落全量解析。
func decodeEventLogStartedFast(line []byte) (eventLogStartedFast, bool) {
	var env eventLogStartedEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return eventLogStartedFast{}, false
	}
	if env.Type != eventLogStartedFastType {
		return eventLogStartedFast{}, false
	}
	env.Payload.EnvelopeTraceID = env.TraceID
	return env.Payload, true
}

// eventLogStartedEvent 用便宜解码结果构造最小事件：Payload 只含
// applyLLMStarted 真正读取的 6 个 identity 键，envelope 只保留 TraceID。
//
// 与 tool.progress 不同，这里**没有 apply 期守卫**：opLLMStarted 的 apply
// 分支（applyLLMStarted → beginAssistantRequest）按构造只有一个无条件路径，
// 读取面就是这 6 键 + envelope TraceID，不存在"守卫为假时读更多键"的第二
// 分支。等价性因此由读取面契约 + oracle 测试
// （TestEventLogStartedFastMatchesFullDecode，语料含"登记被后续 fallback 行
// 消费"的危险排序）保证；若 applyLLMStarted 将来扩面，该测试会变红，而不是
// 静默丢信息。
//
// 值直接透传 interface{}：不做任何字符串转换，payloadString 仍由编码器执行。
func eventLogStartedEvent(f eventLogStartedFast) runtimeevents.Event {
	payload := make(map[string]interface{}, 6)
	payload["step"] = f.Step
	payload["stream_id"] = f.StreamID
	payload["llm_request_id"] = f.RequestID
	payload["turn_id"] = f.TurnID
	payload["logical_turn_id"] = f.LogicalTurnID
	payload["trace_id"] = f.TraceIDKey
	return runtimeevents.Event{
		Type:    eventLogStartedFastType,
		TraceID: f.EnvelopeTraceID,
		Payload: payload,
	}
}
