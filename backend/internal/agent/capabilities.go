package agent

// capabilities.go — 子代理 spawn 准入期能力位协商（DSH 借鉴方案 P1-1'）。
//
// 设计来源：docs/plan/deepseek-harness-agent-borrowing-plan-review-v2-20261016.md
// §4 P1-1'。语义对齐 DSH 的 SubagentCapabilities（packages/subagent/subagent
// src/types.ts），但按 v2 方案 G1/G2/G7 升级：
//
//   - G1 组合而非复制：能力位由既有运行时特征推导（DefaultCapabilities），
//     不引入独立 provider registry；fork/schema 等新能力位默认关，由宿主
//     配置（agents.capabilities.*，后续 Phase 开通）或实施项落地时置 true。
//   - G2 契约优先于提示词：不支持的能力在 spawn 准入期以稳定错误码拒绝
//     （SUBAGENT_CAPABILITY_UNSUPPORTED），绝不运行时静默降级。
//   - G7 三层一致：Negotiate 的结果（CapabilityNegotiation）是准入判决、
//     工具措辞与 preflight/审计载荷的唯一数据源；refusals 逐字段给出可操作
//     的 next_action，父级可精准修正而不是整包失败后猜测。
//
// 本文件只有纯逻辑：不读配置、不碰 Agent、不 emit 事件——宿主装配与
// scheduler 接线在各自调用方。

import (
	"fmt"
	"strings"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

// SubagentCapabilities 声明当前部署在 spawn 准入期支持的可选请求位。
// 零值 = 全部不支持（最保守）；nil 指针在 Negotiate 中视为零值。
//
// 字段语义（true = 该请求字段会在本次 spawn 中生效）：
//   - AgentOptions：任务携带 provider/model/reasoning_effort 路由覆盖
//     （现状已支持，经 modelrouting，故默认推导为 true）。
//   - OutputSchema：任务携带 ReportSchema，要求子级以结构化回执交付
//     （P1-2' submit_report 契约开通后置 true）。
//   - DepthLimit：任务级 depth 覆盖（现状由 scheduler config 决定，
//     任务包尚无 depth 字段，保留位以对齐 DSH 词汇）。
//   - ToolFilter：任务携带 tools_whitelist（现状已支持，默认 true）。
//   - Persona：任务引用的 agentdef 解析出 persona 影子提示词
//     （agentdef bridge 已支持，默认 true）。
//   - ForkContext：任务请求 context=fork 继承父会话上下文快照
//     （P2-1' 开通前恒 false）。
type SubagentCapabilities struct {
	AgentOptions bool `json:"agent_options,omitempty" yaml:"agent_options,omitempty"`
	OutputSchema bool `json:"output_schema,omitempty" yaml:"output_schema,omitempty"`
	DepthLimit   bool `json:"depth_limit,omitempty" yaml:"depth_limit,omitempty"`
	ToolFilter   bool `json:"tool_filter,omitempty" yaml:"tool_filter,omitempty"`
	Persona      bool `json:"persona,omitempty" yaml:"persona,omitempty"`
	ForkContext  bool `json:"fork_context,omitempty" yaml:"fork_context,omitempty"`
}

// DefaultCapabilities 从既有运行时特征推导默认能力位：现状已支持的可选
// 请求字段为 true；新能力位（fork/结构化回执）在对应实施项落地并接线前
// 保持 false——这是"默认关 + 一键灰度"（G4）的静态形态。
func DefaultCapabilities() SubagentCapabilities {
	return SubagentCapabilities{
		AgentOptions: true,
		OutputSchema: false, // P1-2' 落地后置 true
		DepthLimit:   false, // 任务级 depth 覆盖尚未接入任务包
		ToolFilter:   true,
		Persona:      true,
		ForkContext:  false, // P2-1' 落地后置 true
	}
}

// subagentTaskContextFork 是 SubagentTask.Context 的 fork 取值；fresh 与空
// 等价（现状语义）。与 scheduler.go 的任务字段定义同包共享，故在此声明常量。
const subagentTaskContextFork = "fork"

// 子级上下文模式的公开常量：spawn_subagents 任务包的 context 字段取值。
const (
	SubagentContextFresh = "fresh"
	SubagentContextFork  = "fork"
)

// CapabilityRefusal 描述一个被拒绝的能力请求字段：稳定错误码 + 可操作
// 建议（G7）。Reason 面向父级模型，Code 用于机器路由。
type CapabilityRefusal struct {
	Field  string
	Code   runtimeerrors.ErrorCode
	Reason string
}

// CapabilityNegotiation 是一次 spawn 请求的能力协商结果：既是准入判决
// （Refusals 为空 = 全过），也是工具措辞与审计载荷的唯一数据源（G7）。
// Granted 始终回填（即使存在 refusal），调用方据此渲染可用的措辞子集。
type CapabilityNegotiation struct {
	Granted  SubagentCapabilities
	Refusals []CapabilityRefusal
}

// OK 报告本次协商是否全部通过。
func (n CapabilityNegotiation) OK() bool { return len(n.Refusals) == 0 }

// Err 把首个 refusal 转为携带完整明细的 error（scheduler 用它产出
// subagentAdmissionFailureResult 与 emitSubagentDenied 载荷）。
func (n CapabilityNegotiation) Err(taskID string) error {
	if n.OK() {
		return nil
	}
	first := n.Refusals[0]
	details := make([]string, 0, len(n.Refusals))
	for _, refusal := range n.Refusals {
		details = append(details, fmt.Sprintf("%s: %s", refusal.Field, refusal.Reason))
	}
	return runtimeerrors.New(
		first.Code,
		fmt.Sprintf("task %s requests unsupported capability (%s)", taskID, strings.Join(details, "; ")),
	)
}

// SubagentRequestWants 从任务包检测请求了哪些可选能力位。检测只看字段的
// "是否非空/是否请求"，不做语义校验（那是 resolveChildToolSurface 等既有
// 校验器的职责）；capability 协商只回答"部署是否支持该字段生效"。
func SubagentRequestWants(task SubagentTask) SubagentCapabilities {
	wants := SubagentCapabilities{}
	if strings.TrimSpace(task.Provider) != "" ||
		strings.TrimSpace(task.Model) != "" ||
		strings.TrimSpace(task.ReasoningEffort) != "" {
		wants.AgentOptions = true
	}
	if task.ReportSchema != nil && !task.ReportSchemaGranted {
		// 部署自有契约（iterate_goal 内核的 round schema）不参与能力协商：
		// 它不是模型请求的字段，而是已启用工具的内部编排，契约校验仍在
		// submit_report 收口。
		wants.OutputSchema = true
	}
	if len(task.ToolsWhitelist) > 0 {
		wants.ToolFilter = true
	}
	// Persona 检测交给 agentdef bridge 后置判定（Resolve 阶段才知道绑定是否
	// 带 persona）；这里不预判，避免与 agentdef 解析重复。字段保留在
	// SubagentCapabilities 供桥接路径回填 refusal。
	if strings.TrimSpace(task.Context) == subagentTaskContextFork {
		wants.ForkContext = true
	}
	return wants
}

// Negotiate 一次性校验任务的可选请求位与部署能力位（G7：spawn 准入期
// 单次调用，结果随任务传递）。caps 传 nil 等效零值（全不支持）。
//
// 注意：AgentOptions/ToolFilter 默认推导为 true，因此只有宿主显式降级
// （如运行时降级开关）才会触发 refusal；ForkContext/OutputSchema 默认
// false，未开通时请求即拒——这是"默认安全"而不是"默认拒绝一切"。
func Negotiate(caps *SubagentCapabilities, task SubagentTask) CapabilityNegotiation {
	granted := DefaultCapabilities()
	if caps != nil {
		granted = *caps
	}
	wants := SubagentRequestWants(task)
	negotiation := CapabilityNegotiation{Granted: granted}

	refuse := func(field string, supported, requested bool, action string) {
		if !requested || supported {
			return
		}
		negotiation.Refusals = append(negotiation.Refusals, CapabilityRefusal{
			Field: field,
			Code:  runtimeerrors.ErrSubagentCapabilityUnsupported,
			Reason: fmt.Sprintf(
				"deployment does not grant capability %q; next_action=%s",
				field, action,
			),
		})
	}

	// 路由覆盖：fork 子级即使未来开通也强制继承父级路由（KV Cache 保持，
	// P2-1'），所以 fork 请求同时携带路由覆盖时按 fork 的 refusal 优先——
	// 单独的路由覆盖走标准 AgentOptions 位。
	if wants.ForkContext && !granted.ForkContext {
		negotiation.Refusals = append(negotiation.Refusals, CapabilityRefusal{
			Field: "fork_context",
			Code:  runtimeerrors.ErrSubagentCapabilityUnsupported,
			Reason: "deployment does not grant capability \"fork_context\"; " +
				"next_action=use_fresh_context_or_enable_agents.capabilities.fork_context",
		})
	}
	refuse("provider_override", granted.AgentOptions, wants.AgentOptions,
		"drop_provider_model_fields")
	refuse("report_schema", granted.OutputSchema, wants.OutputSchema,
		"drop_report_schema_or_enable_agents.capabilities.output_schema")
	refuse("tools_whitelist", granted.ToolFilter, wants.ToolFilter,
		"drop_tools_whitelist_or_enable_agents.capabilities.tool_filter")
	// fork 且带路由覆盖：无论 AgentOptions 位状态如何都拒绝——fork 的
	// KV Cache 保持约束（G2）禁止子级覆盖路由。
	if wants.ForkContext && granted.ForkContext {
		if wants.AgentOptions {
			negotiation.Refusals = append(negotiation.Refusals, CapabilityRefusal{
				Field: "provider_override",
				Code:  runtimeerrors.ErrSubagentCapabilityUnsupported,
				Reason: "fork_context children inherit the parent route for KV-cache reuse; " +
					"next_action=drop_provider_model_fields_or_use_fresh_context",
			})
		}
	}
	return negotiation
}
