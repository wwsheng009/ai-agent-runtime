package agent

// subagent_handoff.go — 子代理结构化交接报告契约（DSH 借鉴方案 P1-2'）。
//
// 设计来源：docs/plan/deepseek-harness-agent-borrowing-plan-review-v2-20261016.md
// §4 P1-2'。状态机与字段约束移植 DSH Ralph handoff（packages/experimental/
// tool-ralph src/index.ts 的 validateReport/readReport），通用化为普通子代理
// 的可选交接通道：
//
//   - HandoffIn：父级发给子级的上一轮交接（写入任务包，随 prompt 注入）；
//   - HandoffOut：子级返回的交接（经状态机校验后进入回执与 goal 联动）。
//
// 校验纪律（v2 方案 G2/G3）：
//   - 状态机三态（continue/complete/blocked）字段约束逐条强制；
//   - 所有自由文本非空且 trim 相等（拒绝空白字符差异导致的语义漂移）；
//   - 序列化长度上限 MaxHandoffChars（默认 16K，对齐 DSH），超限报错含
//     实测值，绝不静默截断；
//   - DecodeHandoff 是跨 run 边界的第二道防线：键集合精确匹配、字段类型
//     逐一断言；第一道防线是 P1-2' 的 submit_report 工具参数校验（后续
//     loop 接线），两层独立生效。

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// HandoffStatus 交接报告状态机（与 DSH Ralph 对齐）。
type HandoffStatus string

const (
	// HandoffContinue 仍有有用工作，下一轮接续。
	HandoffContinue HandoffStatus = "continue"
	// HandoffComplete 目标已达成，Evidence 为证。
	HandoffComplete HandoffStatus = "complete"
	// HandoffBlocked 无法继续推进，需人工或外部状态变化。
	HandoffBlocked HandoffStatus = "blocked"
)

// MaxHandoffChars 单份 handoff 的序列化长度上限（对齐 DSH maxHandoffChars
// 默认值 16384）。宿主可经 agents.handoff.maxChars 覆盖（装配层接线），
// 本常量是未配置时的缺省。
const MaxHandoffChars = 16384

// Handoff 是跨任务/跨轮的规范化交接报告。作为 SubagentTask / SubagentResult
// 的可选字段；空指针表示"不使用 handoff"（完全向后兼容）。
// 序列化纪律：Evidence/NextSteps/Blocker 不用 omitempty——wire 契约要求
// 五键齐全（DecodeHandoff 的键集合精确匹配以此为前提），空列表序列化为
// null，由 decodeHandoffStringList 归一为空切片。
type Handoff struct {
	Status    HandoffStatus `json:"status" yaml:"status"`
	Summary   string        `json:"summary" yaml:"summary"`
	Evidence  []string      `json:"evidence" yaml:"evidence"`
	NextSteps []string      `json:"next_steps" yaml:"next_steps"`
	Blocker   string        `json:"blocker" yaml:"blocker"`
}

// Clone 返回深拷贝，防止共享 slice 被调用方意外修改。
func (h Handoff) Clone() Handoff {
	clone := h
	if h.Evidence != nil {
		clone.Evidence = append([]string(nil), h.Evidence...)
	}
	if h.NextSteps != nil {
		clone.NextSteps = append([]string(nil), h.NextSteps...)
	}
	return clone
}

// normalizedHandoffText 要求非空且 trim 相等（与 DSH normalizedText 一致）。
func normalizedHandoffText(value string) bool {
	return len(value) > 0 && value == strings.TrimSpace(value)
}

// normalizedHandoffList 要求每一项都是 normalizedHandoffText。
func normalizedHandoffList(values []string) bool {
	for _, value := range values {
		if !normalizedHandoffText(value) {
			return false
		}
	}
	return true
}

// ValidateHandoff 状态机校验（移植 DSH validateReport 语义）：
//   - Summary 必须非空且 trim 相等；
//   - Evidence/NextSteps 每项必须非空且 trim 相等；
//   - Blocker 必须是 trim 相等的字符串（空串合法，仅限非 blocked 状态）；
//   - continue：至少一条 NextSteps 且 Blocker 为空；
//   - complete：至少一条 Evidence、无 NextSteps、Blocker 为空；
//   - blocked：Blocker 必须具体（非空白）；
//   - JSON 序列化长度 ≤ maxChars，超限报错（含实测值，不静默截断）。
func ValidateHandoff(h Handoff, maxChars int) error {
	if maxChars <= 0 {
		maxChars = MaxHandoffChars
	}
	if !normalizedHandoffText(h.Summary) {
		return fmt.Errorf("handoff summary must be non-empty and trimmed (no leading/trailing whitespace)")
	}
	if !normalizedHandoffList(h.Evidence) {
		return fmt.Errorf("handoff evidence entries must be non-empty and trimmed")
	}
	if !normalizedHandoffList(h.NextSteps) {
		return fmt.Errorf("handoff next_steps entries must be non-empty and trimmed")
	}
	if h.Blocker != strings.TrimSpace(h.Blocker) {
		return fmt.Errorf("handoff blocker must be trimmed")
	}
	switch h.Status {
	case HandoffContinue:
		if len(h.NextSteps) == 0 {
			return fmt.Errorf("a continuing handoff requires at least one next_steps entry")
		}
		if h.Blocker != "" {
			return fmt.Errorf("a continuing handoff must have an empty blocker")
		}
	case HandoffComplete:
		if len(h.Evidence) == 0 {
			return fmt.Errorf("a complete handoff requires at least one evidence entry")
		}
		if len(h.NextSteps) != 0 {
			return fmt.Errorf("a complete handoff must have no next_steps entries")
		}
		if h.Blocker != "" {
			return fmt.Errorf("a complete handoff must have an empty blocker")
		}
	case HandoffBlocked:
		if !normalizedHandoffText(h.Blocker) {
			return fmt.Errorf("a blocked handoff requires a concrete blocker")
		}
	default:
		return fmt.Errorf("handoff status %q is invalid (expected continue|complete|blocked)", h.Status)
	}
	encoded, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("handoff is not serializable: %w", err)
	}
	if len(encoded) > maxChars {
		return fmt.Errorf("handoff exceeds max_chars (%d > %d)", len(encoded), maxChars)
	}
	return nil
}

// DecodeHandoff 防御解码：从子级输出（任意来源的 map）重建 Handoff，
// 期望状态为 expectedStatus。键集合精确匹配——缺失字段与多余字段同样
// 报错，不静默忽略（移植 DSH readReport 的防御边界纪律）。校验通过后
// 返回规范化 Handoff；失败返回带实测值的错误。
//
// 该函数是跨 run 边界的第二道防线：即使上游（submit_report 工具参数
// 校验）被绕过或未接线，宿主侧仍不落未校验数据。
func DecodeHandoff(raw map[string]interface{}, expectedStatus HandoffStatus, maxChars int) (Handoff, error) {
	if maxChars <= 0 {
		maxChars = MaxHandoffChars
	}
	if raw == nil {
		return Handoff{}, fmt.Errorf("handoff decode failed: no structured report provided")
	}
	// 键集合精确匹配（对齐 DSH readReport 的 sort+join 比对）。
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sortStrings(keys)
	wantKeys := "blocker,evidence,next_steps,status,summary"
	if gotKeys := strings.Join(keys, ","); gotKeys != wantKeys {
		return Handoff{}, fmt.Errorf(
			"handoff decode failed: unexpected field set %q (expected %q)",
			gotKeys, wantKeys,
		)
	}
	statusValue, _ := raw["status"].(string)
	if HandoffStatus(statusValue) != expectedStatus {
		return Handoff{}, fmt.Errorf(
			"handoff decode failed: status %q does not match expected %q",
			statusValue, expectedStatus,
		)
	}
	summary, _ := raw["summary"].(string)
	blocker, _ := raw["blocker"].(string)
	evidence, ok := decodeHandoffStringList(raw["evidence"])
	if !ok {
		return Handoff{}, fmt.Errorf("handoff decode failed: evidence must be an array of strings")
	}
	nextSteps, ok := decodeHandoffStringList(raw["next_steps"])
	if !ok {
		return Handoff{}, fmt.Errorf("handoff decode failed: next_steps must be an array of strings")
	}
	handoff := Handoff{
		Status:    expectedStatus,
		Summary:   summary,
		Evidence:  evidence,
		NextSteps: nextSteps,
		Blocker:   blocker,
	}
	if err := ValidateHandoff(handoff, maxChars); err != nil {
		return Handoff{}, fmt.Errorf("handoff decode failed: %w", err)
	}
	return handoff, nil
}

// decodeHandoffStringList 宽进严出地读取字符串数组：nil 空接口视为空数组
//（JSON null / 缺省），非 []string 形态一律拒绝。
func decodeHandoffStringList(value interface{}) ([]string, bool) {
	if value == nil {
		return []string{}, true
	}
	list, ok := value.([]string)
	if ok {
		return list, true
	}
	rawList, ok := value.([]interface{})
	if !ok {
		return nil, false
	}
	decoded := make([]string, 0, len(rawList))
	for _, item := range rawList {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		decoded = append(decoded, text)
	}
	return decoded, true
}

// sortStrings 就地排序（小切片，避免为三处使用引入 sort 依赖面）。
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// ValidateHandoffOrWarn 对可选 handoff 字段做"降级而非拒绝"的包装：
// 子级返回了无效 handoff 时，回执不应整体失败（其余产出可能仍然有效），
// 而是丢弃 handoff 并返回告警文案，供 RouteWarnings 通道提示父级。
// 与准入期 CheckCapabilities 的"硬拒"形成纪律分层：请求位协商是硬边界，
// 产出物校验是降级边界。
func ValidateHandoffOrWarn(h *Handoff, maxChars int) (clean *Handoff, warning string) {
	if h == nil {
		return nil, ""
	}
	if err := ValidateHandoff(*h, maxChars); err != nil {
		return nil, fmt.Sprintf("handoff_invalid:%v", err)
	}
	return h, ""
}

// handoffCandidate 是从子级文本输出解析出的候选交接：
// raw 为 nil 表示输出不是 JSON 对象形态（不进入 handoff 通道）。
type handoffCandidate struct {
	raw    map[string]interface{}
	status HandoffStatus
}

// decodeHandoffCandidate 把子级最终文本输出解析为候选 handoff。
// 纪律（G2 契约优先的过渡形态）：只有输出整体是一个 JSON 对象、且携带
// status 字段时才视为候选——普通文本汇报（绝大多数现状）不进入该通道，
// 保持零行为变化。status 非法时报错由 DecodeHandoff 的键集合/状态校验
// 兜底，这里只做形态识别。
func decodeHandoffCandidate(output string) handoffCandidate {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return handoffCandidate{}
	}
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return handoffCandidate{}
	}
	statusValue, _ := raw["status"].(string)
	return handoffCandidate{raw: raw, status: HandoffStatus(statusValue)}
}

// ---------------------------------------------------------------------------
// submit_report 结构化回执契约（P1-2' 收尾）
// ---------------------------------------------------------------------------

// SubmitReportToolName 是结构化回执契约的工具名。该工具由 loop 在任务携带
// report_schema 时注入（不是注册表工具）：执行分支在 whitelist/权限门之前
// 处理，因此不受工具面裁剪影响（loop 合成工具，语义与 predict_task_difficulty
// 同族的 meta 工具，但按需注入而非全局可见）。
const SubmitReportToolName = "submit_report"

// submitReportToolDefinition 由请求的 report_schema 生成 submit_report 工具：
// 参数即 schema 本身，使模型以工具调用交付结构化回执，而不是自由文本约定
// （G2 契约优先）。description 明确一次性与终结语义。
func submitReportToolDefinition(schema map[string]interface{}) types.ToolDefinition {
	parameters := map[string]interface{}{
		"type": "object",
	}
	if schema != nil {
		parameters = cloneInterfaceMap(schema)
		if _, ok := parameters["type"]; !ok {
			parameters["type"] = "object"
		}
	}
	return types.ToolDefinition{
		Name: SubmitReportToolName,
		Description: "Submit the final structured report for this task and end it. " +
			"Call this exactly once, when the task's goal is fulfilled or definitively blocked. " +
			"The arguments must match the required report schema; the host validates them and " +
			"the accepted report becomes your result. After a successful submission, stop working and reply with one short confirmation line.",
		Parameters: parameters,
	}
}

// validateStructuredReport 是最小充分的结构化回执校验（G2）：对象形态、
// required 键齐全、additionalProperties=false 时拒绝未声明键。不实现完整
// JSON Schema 是因为提交跨越模型边界，契约面越窄越可预测；更深校验由
// 消费端（scheduler decode / 宿主）按业务需要叠加。
func validateStructuredReport(schema map[string]interface{}, args map[string]interface{}) error {
	if args == nil {
		return fmt.Errorf("submit_report requires an object argument")
	}
	if schema == nil {
		return nil
	}
	if declared, ok := schema["type"].(string); ok && strings.TrimSpace(declared) != "" && strings.TrimSpace(declared) != "object" {
		// schema 顶层不是 object 时，本契约无法承载——视为部署配置错误。
		return fmt.Errorf("report_schema must be an object-rooted schema, got type %q", declared)
	}
	if rawRequired, ok := schema["required"]; ok {
		required, err := stringListValue(rawRequired)
		if err != nil {
			return fmt.Errorf("report_schema required is invalid: %w", err)
		}
		for _, name := range required {
			if _, present := args[name]; !present {
				return fmt.Errorf("submit_report is missing required field %q", name)
			}
		}
	}
	if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
		if rawProperties, ok := schema["properties"].(map[string]interface{}); ok {
			for key := range args {
				if _, declared := rawProperties[key]; !declared {
					return fmt.Errorf("submit_report contains undeclared field %q (additionalProperties=false)", key)
				}
			}
		}
	}
	return nil
}

// stringListValue 宽进严出地读取字符串数组（[]string / []interface{}）。
func stringListValue(value interface{}) ([]string, error) {
	switch typed := value.(type) {
	case []string:
		return typed, nil
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("entry %v is not a string", item)
			}
			out = append(out, text)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected an array of strings, got %T", value)
	}
}

// handoffReportSchema 返回 Handoff 的 JSON Schema（object-rooted，五键
// 必需、additionalProperties=false）。供部署自有契约使用（iterate_goal
// 的每轮报告经 submit_report 交付，而不是靠最终文本约定）。
func handoffReportSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"status":     map[string]interface{}{"type": "string", "enum": []string{"continue", "complete", "blocked"}},
			"summary":    map[string]interface{}{"type": "string"},
			"evidence":   map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"next_steps": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"blocker":    map[string]interface{}{"type": "string"},
		},
		"required":             []string{"status", "summary", "evidence", "next_steps", "blocker"},
		"additionalProperties": false,
	}
}

// handleSubmitReport 校验并捕获一次结构化回执提交（run 作用域）。返回给
// 模型的确认行；捕获的报告由 run() 收尾时覆盖为最终 Output，scheduler 的
// decode 路径据此拿到结构化交付物。重复提交被拒——已接受的回执是终态。
func (loop *ReActLoop) handleSubmitReport(args map[string]interface{}) (string, error) {
	if loop == nil || loop.reportSchema == nil {
		return "", fmt.Errorf("%s is unavailable for this run", SubmitReportToolName)
	}
	if loop.reportSubmitted != nil {
		return "", fmt.Errorf("%s was already accepted for this run; the accepted report is final — stop calling it and end your turn", SubmitReportToolName)
	}
	if err := validateStructuredReport(loop.reportSchema, args); err != nil {
		return "", err
	}
	loop.reportSubmitted = cloneInterfaceMap(args)
	return "structured report accepted", nil
}
