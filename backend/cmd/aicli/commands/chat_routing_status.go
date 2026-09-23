package commands

import (
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// TUI 侧路由投影与状态栏段（方案 §6.1/§6.2）。
//
// 单一投影纪律（§6.1）：状态栏段、`/routing show`、`/routing doctor` 三处都只
// 消费本文件的 chatRoutingStatusProjection；字段一律来自 agentconfig 的
// ProjectRoutingStatus / BuildRoutingLevelSummaries，禁止各自拼装。

// chatSessionRoutingOverrideRaw 读取会话 context 里的路由覆盖 JSON（§3.4）。
// 键缺失/空值返回空串，调用方按「无覆盖」处理（B5）。
func chatSessionRoutingOverrideRaw(session *ChatSession) string {
	if session == nil {
		return ""
	}
	return strings.TrimSpace(runtimeSessionContextString(session.RuntimeSession, sessionmeta.LegacyAICLIRoutingOverride))
}

// chatSessionRoutingWorkspacePath 取会话绑定的 workspace 路径（N9）：
// workspace 层写入与读取都以该路径为准，不随当前 shell 的 cwd 漂移。
func chatSessionRoutingWorkspacePath(session *ChatSession) string {
	if session == nil {
		return ""
	}
	return strings.TrimSpace(runtimeSessionContextString(session.RuntimeSession, sessionmeta.WorkspacePath))
}

// chatRoutingResolutionForSession 解析当前会话的主 Agent 路由（§4.1 五层）。
// 解析器恒非 nil（B5）：关闭态 Effective 为 nil，投影据此显示 route:off。
func chatRoutingResolutionForSession(session *ChatSession) agentconfig.RoutingResolution {
	var cfg *agentconfig.Config
	if session != nil {
		cfg = session.Config
	}
	override, _ := agentconfig.DecodeSessionRoutingOverride(chatSessionRoutingOverrideRaw(session))
	// N9：workspace 层以会话绑定路径为准（无绑定时回退 cwd），与写入路径同源。
	workspace := chatRoutingWorkspacePreferences(session)
	return agentconfig.ResolveMainAgentRouting(cfg, override, workspace, nil)
}

// chatRoutingRevision 投影会话覆盖的写入端信息（§3.4 M11）。
func chatRoutingRevision(session *ChatSession) string {
	override, _ := agentconfig.DecodeSessionRoutingOverride(chatSessionRoutingOverrideRaw(session))
	if override == nil {
		return ""
	}
	return agentconfig.FormatRoutingRevision(override.UpdatedAt, override.UpdatedBy)
}

// chatRoutingStatusProjection 是 §6.1 的唯一投影函数（TUI 侧）。
// level 为当前 turn 生效档位；TUI 在 turn 之外无法得知难度判定结果，传空串，
// 投影按 default_difficulty 兜底（§6.2：未判定档位时只显示 baseline）。
func chatRoutingStatusProjection(session *ChatSession) agentconfig.RoutingStatusProjection {
	res := chatRoutingResolutionForSession(session)
	return agentconfig.ProjectRoutingStatus(res, "", chatRoutingRevision(session))
}

// chatRoutingLevelSummaries 产出逐级表格行（面板 / `/routing show` / doctor 共用）。
func chatRoutingLevelSummaries(session *ChatSession, scope string) []agentconfig.RoutingLevelSummary {
	return agentconfig.BuildRoutingLevelSummaries(chatRoutingResolutionForSession(session), scope)
}

// chatRoutingLevelSummaryFor 在逐级摘要里查找某档位（找不到返回 false）。
// §10.2 I-3：面板/补全的 reasoning 候选按「该级生效 model」过滤时用它取档位行。
func chatRoutingLevelSummaryFor(session *ChatSession, scope, level string) (agentconfig.RoutingLevelSummary, bool) {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		return agentconfig.RoutingLevelSummary{}, false
	}
	for _, row := range chatRoutingLevelSummaries(session, scope) {
		if strings.EqualFold(strings.TrimSpace(row.Level), level) {
			return row, true
		}
	}
	return agentconfig.RoutingLevelSummary{}, false
}

// chatRoutingSourceSuffix 把来源投影为状态栏后缀（§6.2）：
// session=*、workspace=~、config/default/derived=无后缀。
func chatRoutingSourceSuffix(source string) string {
	switch agentconfig.RoutingSource(strings.TrimSpace(source)) {
	case agentconfig.RoutingSourceSession:
		return "*"
	case agentconfig.RoutingSourceWorkspace:
		return "~"
	default:
		return ""
	}
}

// chatRoutingStatusEngaged 报告状态栏是否应出现路由段（§6.2 / §6.4 U-4 可见性）。
// 任一层显式配置了路由（会话覆盖 / workspace 偏好 / 全局配置）就显示——包括
// 「配置了但关闭」的 route:off（用户要能看出路由被关掉）；四层都没有任何配置时
// 不显示，保持「默认关闭零行为变化」（§8.1），也避免默认会话白占状态栏宽度。
func chatRoutingStatusEngaged(session *ChatSession) bool {
	if session == nil {
		return false
	}
	// 判定「有没有生效字段」而不是「context 里有没有那段 JSON」：只有
	// updated_at/updated_by 的历史残留不该让状态栏显示 route:off（P1-5）。
	if override, err := agentconfig.DecodeSessionRoutingOverride(chatSessionRoutingOverrideRaw(session)); err == nil &&
		override != nil && (override.HasMainAgentFields() || override.HasSubAgentFields()) {
		return true
	}
	if prefs := chatRoutingWorkspacePreferences(session); prefs != nil {
		return true
	}
	cfg := session.Config
	if cfg == nil || cfg.AICLI == nil || cfg.AICLI.MainAgent == nil {
		return false
	}
	return cfg.AICLI.MainAgent.Routing != nil
}

// chatRoutingUIDisabled 报告路由 UI 入口是否被紧急开关关闭（§8.3 / §11 U-6）：
// `AICLI_DISABLE_ROUTING_UI=1` 时仅隐藏 UI 入口（`/routing` 命令、命令目录/补全、
// 状态栏段），解析器、宿主接线与 `/api/runtime/sessions/{id}/routing` API 全部
// 保持生效——这是灰度回退开关，不是功能开关。
func chatRoutingUIDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AICLI_DISABLE_ROUTING_UI"))) {
	case "1", "true", "on", "yes", "y", "enabled":
		return true
	default:
		return false
	}
}

// chatSurfaceRoutingStatusSegment 组装 §6.2 状态栏段：
//
//	route:hard · claude-opus-4 · xhigh · *      （启用且已判定/兜底档位）
//	route:off                                    （已配置但关闭）
//	route:-                                      （启用但档位未判定且无兜底）
//	（空段）                                      （四层都没有任何路由配置）
func chatSurfaceRoutingStatusSegment(session *ChatSession) chatStatusSegment {
	if session == nil || chatRoutingUIDisabled() {
		return chatStatusSegment{}
	}
	projection := chatRoutingStatusProjection(session)
	if !projection.Enabled {
		if !chatRoutingStatusEngaged(session) {
			return chatStatusSegment{}
		}
		return chatStatusSegment{full: "route:off", compact: "route:off"}
	}
	level := strings.TrimSpace(projection.Level)
	if level == "" {
		return chatStatusSegment{full: "route:-", compact: "route:-"}
	}
	parts := []string{"route:" + level}
	if model := strings.TrimSpace(projection.Model); model != "" {
		parts = append(parts, model)
	}
	if effort := strings.TrimSpace(projection.Reasoning); effort != "" {
		parts = append(parts, effort)
	}
	full := strings.Join(parts, " · ")
	if suffix := chatRoutingSourceSuffix(projection.Source); suffix != "" {
		full = full + " · " + suffix
	}

	compactParts := []string{"rt:" + level}
	if model := strings.TrimSpace(projection.Model); model != "" {
		compactParts = append(compactParts, compactStatusValue(model, 16))
	}
	compact := strings.Join(compactParts, " · ")
	if suffix := chatRoutingSourceSuffix(projection.Source); suffix != "" {
		compact = compact + " · " + suffix
	}
	return chatStatusSegment{full: full, compact: compact}
}

// chatRoutingSubReadOnlyNotes 是 §3.1/§5.6 要求的子 Agent 作用域只读说明：
//   - team 未单独配置 aicli.teams.routing 时按 EffectiveTeamRoutingConfig 的回落链
//     继承子 Agent 路由——必须显式说明，避免用户误以为 team 有独立策略；
//   - task_types/roles 覆盖表（task_type → difficulty → profile）首版只读：这里只
//     报告「是否存在」与优先级，不提供编辑入口（键空间大、歧义多，§5.6/§11）。
func chatRoutingSubReadOnlyNotes(session *ChatSession) []string {
	if session == nil {
		return nil
	}
	cfg := session.Config
	notes := make([]string, 0, 2)
	if cfg == nil || cfg.AICLI == nil || cfg.AICLI.Teams == nil || cfg.AICLI.Teams.Routing == nil {
		notes = append(notes, "team: 未单独配置 aicli.teams.routing，继承 sub_agent 路由（只读）")
	} else {
		notes = append(notes, "team: 已单独配置 aicli.teams.routing（只读展示，首版不提供编辑）")
	}
	var sub *agentconfig.AICLISubagentRoutingConfig
	if cfg != nil && cfg.AICLI != nil && cfg.AICLI.Subagents != nil {
		sub = cfg.AICLI.Subagents.Routing
	}
	if sub != nil && (len(sub.TaskTypes) > 0 || len(sub.Roles) > 0) {
		notes = append(notes, "task_types/roles: 已配置（只读展示；按 task_type → difficulty 覆盖 levels，首版不提供编辑）")
	}
	return notes
}
