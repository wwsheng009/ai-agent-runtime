package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeprofileinput "github.com/wwsheng009/ai-agent-runtime/internal/profileinput"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// Profile 热切换的固定语义（D18/D23）：切换在下一个 turn 边界生效，turn 内绝不打断。
const (
	profileSwitchEffectiveAt = "next_turn"
	profileSwitchCacheNotice = "下一轮起生效；请求前缀变化将使 provider prompt cache 重建（首轮输入计费增加）"
	// profileSwitchDeferredActorWarning：切换撞上在途 turn 时，旧 actor 必须留到该
	// turn 结束才驱逐（D18 绝不打断）；延迟兑现由回合入口的 reconcile 完成。
	profileSwitchDeferredActorWarning = "检测到在途 turn：旧 actor 保留到本轮结束，下一轮入口重建（新工具面自下一轮起生效）"
	// profileSwitchActorEvictTimeout 是驱逐空闲 actor 的等待上限（与 server 侧同量级）。
	profileSwitchActorEvictTimeout = 5 * time.Second
)

// ProfileSwitchChanged 是切换报告的 changed 投影（D23）。TUI 与前端渲染同一份
// 结构，避免两套文案漂移；JSON 字段名即前端契约。
type ProfileSwitchChanged struct {
	ToolsAdded            []string `json:"tools_added,omitempty"`
	ToolsRemoved          []string `json:"tools_removed,omitempty"`
	SkillsAdded           []string `json:"skills_added,omitempty"`
	SkillsRemoved         []string `json:"skills_removed,omitempty"`
	MCPAdded              []string `json:"mcp_added,omitempty"`
	MCPRemoved            []string `json:"mcp_removed,omitempty"`
	PromptChanged         bool     `json:"prompt_changed"`
	ProviderChanged       bool     `json:"provider_changed"`
	ModelChanged          bool     `json:"model_changed"`
	PermissionModeChanged bool     `json:"permission_mode_changed"`
}

// ProfileSwitchReport 是 `applyRuntimeProfileSwitch` 的结构化输出（D23）。
// changed 的语义是"本次切换实际应用的生效面差异"：provider/model/permission
// 只报告不隐式应用（见 D30），因此这三项恒为 false，差异以 warnings 呈现。
type ProfileSwitchReport struct {
	From        string               `json:"from"`
	To          string               `json:"to"`
	Changed     ProfileSwitchChanged `json:"changed"`
	EffectiveAt string               `json:"effective_at"`
	CacheNotice string               `json:"cache_notice"`
	Warnings    []string             `json:"warnings,omitempty"`
	// InFlightTurn 记录切换发生时是否有在途 turn；在途 turn 的冻结前缀由
	// 存储层与活体 agent 配置保护（A3），切换本身仍然立即落地会话状态。
	InFlightTurn bool `json:"in_flight_turn"`
	// AnchorCleared 表示 prompt 冻结锚点是否被删除（下次 compose 重新组合 head）。
	AnchorCleared bool `json:"anchor_cleared"`
	// ToolSurfaceInvalidated/Scope 表示稳定工具面失效的实际路径：
	// actor（精确单会话）| hub（全量回退）| none（无运行时宿主）。
	ToolSurfaceInvalidated bool   `json:"tool_surface_invalidated"`
	ToolSurfaceScope       string `json:"tool_surface_scope,omitempty"`
	// ActorEvicted 表示持有旧工具策略的会话 actor 已被驱逐：下一次
	// GetOrCreate 会按新会话状态重建，provider 请求面因此真正换面。
	//
	// 为什么必须有这一项：本地 chat 的 turn 由 actor 执行（executor_path=actor），
	// agent 的工具执行策略在 buildSessionActor 期由 session.ToolPolicy 固化
	// （chat_actor_host.go 的 buildLocalChatToolPolicy）。只清稳定工具面缓存不动
	// actor，等于"报告说收窄、请求面照旧"（A1 的假开关失效模式）。
	ActorEvicted bool `json:"actor_evicted,omitempty"`
	// ContextTokenCountReset 表示会话累计 token 计数已清零（⑥）。
	ContextTokenCountReset bool `json:"context_token_count_reset"`
}

// 稳定工具面失效路径（D20）。
const (
	profileSwitchSurfaceScopeActor = "actor"
	profileSwitchSurfaceScopeHub   = "hub"
	profileSwitchSurfaceScopeNone  = "none"
)

// chatProfileSurfaceSnapshot 捕获切换前的生效面，作为 diff 基线。
type chatProfileSurfaceSnapshot struct {
	reference  string
	promptText string
	promptMode string
	toolNames  []string
	readOnly   bool
	hasPolicy  bool
	skillAllow []string
	skillDeny  []string
	mcpUse     []string
	mcpExclude []string
	provider   string
	model      string
}

// snapshotChatProfileSurface 读取当前会话的生效面。所有字段都取自会话自身的
// 权威字段（与 chat_setup 的启动投影同源），不重新解析 profile。
func snapshotChatProfileSurface(session *ChatSession) chatProfileSurfaceSnapshot {
	if session == nil {
		return chatProfileSurfaceSnapshot{}
	}
	snapshot := chatProfileSurfaceSnapshot{
		reference:  strings.TrimSpace(session.ProfileReference),
		promptText: strings.TrimSpace(session.SystemPromptText),
		promptMode: strings.TrimSpace(session.ProfilePromptMode),
		provider:   strings.TrimSpace(session.ProviderName),
		model:      strings.TrimSpace(session.Model),
		skillAllow: append([]string(nil), session.ProfileSkillSelection.Allowlist...),
		skillDeny:  append([]string(nil), session.ProfileSkillSelection.Denylist...),
		mcpUse:     append([]string(nil), session.ProfileMCPSelection.UseServers...),
		mcpExclude: append([]string(nil), session.ProfileMCPSelection.ExcludeServers...),
	}
	if session.ToolPolicy != nil {
		snapshot.hasPolicy = true
		snapshot.readOnly = session.ToolPolicy.ReadOnly
		snapshot.toolNames = session.ToolPolicy.AllowedToolNames()
	}
	return snapshot
}

// buildProfileSwitchChanged 计算旧/新生效面差异。skills/mcp 的 added/removed 按
// "暴露面"语义：added = 新增放行 ∪ 新增解除排除，removed = 取消放行 ∪ 新增排除。
func buildProfileSwitchChanged(before chatProfileSurfaceSnapshot, state *chatProfileState) ProfileSwitchChanged {
	changed := ProfileSwitchChanged{}
	if state == nil || !state.Active() {
		return changed
	}
	var (
		afterTools      []string
		afterSkillAllow []string
		afterSkillDeny  []string
		afterMCPUse     []string
		afterMCPExclude []string
		afterPrompt     string
		afterPromptMode string
	)
	if state.ToolPolicy != nil {
		afterTools = state.ToolPolicy.AllowedToolNames()
	}
	afterSkillAllow = append([]string(nil), state.Resolved.Skills.Allowlist...)
	afterSkillDeny = append([]string(nil), state.Resolved.Skills.Denylist...)
	afterMCPUse = append([]string(nil), state.Resolved.MCPSelection.UseServers...)
	afterMCPExclude = append([]string(nil), state.Resolved.MCPSelection.ExcludeServers...)
	afterPrompt = strings.TrimSpace(state.PromptText)
	afterPromptMode = strings.TrimSpace(state.Resolved.PromptMode)

	changed.ToolsAdded = stringSetDifference(afterTools, before.toolNames)
	changed.ToolsRemoved = stringSetDifference(before.toolNames, afterTools)
	changed.SkillsAdded = mergeStringSets(
		stringSetDifference(afterSkillAllow, before.skillAllow),
		stringSetDifference(before.skillDeny, afterSkillDeny),
	)
	changed.SkillsRemoved = mergeStringSets(
		stringSetDifference(before.skillAllow, afterSkillAllow),
		stringSetDifference(afterSkillDeny, before.skillDeny),
	)
	changed.MCPAdded = mergeStringSets(
		stringSetDifference(afterMCPUse, before.mcpUse),
		stringSetDifference(before.mcpExclude, afterMCPExclude),
	)
	changed.MCPRemoved = mergeStringSets(
		stringSetDifference(before.mcpUse, afterMCPUse),
		stringSetDifference(afterMCPExclude, before.mcpExclude),
	)
	changed.PromptChanged = afterPrompt != before.promptText || afterPromptMode != before.promptMode
	// provider/model/permission 由既有显式命令落地（D30），切换本身不改动。
	changed.ProviderChanged = false
	changed.ModelChanged = false
	changed.PermissionModeChanged = false
	return changed
}

// applyRuntimeProfileSwitch 是会话内 profile 热切换的唯一执行核心（D19/D21）。
// 五阶段（设计文档 §18.1）：解析 → 应用 → 失效 → 身份与持久化 → 报告。
// 解析失败显式返回错误，不静默回退到旧 profile（FR-5）。
func applyRuntimeProfileSwitch(session *ChatSession, ref string) (*ProfileSwitchReport, error) {
	if session == nil {
		return nil, fmt.Errorf("chat session is required")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("profile 名称不能为空")
	}
	if session.Config == nil {
		return nil, fmt.Errorf("chat config is required to resolve profile %q", ref)
	}

	// 阶段 1：解析新 profile。
	// 解析输入固定为未叠加覆盖的基线配置（见 profileResolutionConfig）：
	// 否则上一个 profile 的 runtime.overrides 会渗进新 profile 的解析。
	state, err := resolveChatProfileState(profileResolutionConfig(session), &chatCommandOptions{ProfileFlag: ref})
	if err != nil {
		return nil, err
	}
	if state == nil || !state.Active() {
		return nil, fmt.Errorf("profile %q 未解析出有效绑定", ref)
	}

	before := snapshotChatProfileSurface(session)
	report := &ProfileSwitchReport{
		From:         before.reference,
		To:           strings.TrimSpace(state.Reference),
		Changed:      buildProfileSwitchChanged(before, state),
		EffectiveAt:  profileSwitchEffectiveAt,
		CacheNotice:  profileSwitchCacheNotice,
		InFlightTurn: chatSessionTurnInFlight(session),
	}

	// 阶段 2：应用生效面（与启动路径共用同一投影函数，避免双份逻辑漂移）。
	applyProfileStateToChatSession(session, state)
	for _, warning := range state.SandboxWarnings {
		if text := strings.TrimSpace(warning); text != "" {
			report.Warnings = append(report.Warnings, text)
		}
	}
	report.Warnings = append(report.Warnings, profileSwitchDeferredDefaultWarnings(session, state)...)
	report.Warnings = append(report.Warnings, profileSwitchToolPolicyWarnings(before, state)...)
	// 在途 turn 时不驱逐 actor：旧 agent 的工具策略必须留到本轮结束（D18/A3），
	// 报告显式说明"下一轮入口重建"，而不是让用户以为本轮就换了面。
	if report.InFlightTurn {
		report.Warnings = append(report.Warnings, profileSwitchDeferredActorWarning)
	}
	// D29（Batch 14）：未信任工作区的项目级 profile 被扣留 prompts 时，切换报告必须
	// 显式呈现（否则用户看到"已切换"却少了提示词层 = 假开关）。
	if notice := profilePromptSuppressionNotice(session); notice != "" {
		report.Warnings = append(report.Warnings, notice)
	}

	// 阶段 3：失效一次到位（D19）——①锚点 ②稳定工具面 ③turn 面（存储层在
	// 在途 turn 时天然保留）④旧 actor 驱逐（工具策略在 actor 构建期固化）
	// ⑥token 计数。失效动作不出本函数。
	report.AnchorCleared = clearFrozenChatSystemPromptAnchor(session)
	report.ToolSurfaceScope, report.ToolSurfaceInvalidated, report.ActorEvicted =
		invalidateChatProfileRuntime(session, report.InFlightTurn)
	session.ContextWindowTokenCount = 0
	report.ContextTokenCountReset = true

	// 阶段 4：身份与持久化（sessionmeta + sync）。
	applyChatProfileIdentityToRuntimeSession(session, state)
	warnIfChatSessionSyncFails(session, "switch profile", syncRuntimeSessionFromChat(session))

	// 阶段 5：状态栏刷新（与 /model、/provider 切换后的呈现一致）。
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
	return report, nil
}

// clearFrozenChatSystemPromptAnchor 删除会话级 prompt 冻结锚点，使下一次 compose
// 重新组合 head（①）。锚点只在下一次 compose 被读取，因此在途 turn 的 head 不受影响。
func clearFrozenChatSystemPromptAnchor(session *ChatSession) bool {
	ctx := frozenChatSystemPromptContext(session.RuntimeSession, session)
	if ctx == nil {
		return false
	}
	if _, ok := sessionmeta.Value(ctx, sessionmeta.SystemPromptFrozen); !ok {
		return false
	}
	sessionmeta.Delete(ctx, sessionmeta.SystemPromptFrozen)
	return true
}

// invalidateChatProfileRuntime 重置稳定工具面缓存（②）并驱逐持有旧工具策略的
// 空闲 actor（④）。actor 句柄可得时走精确单会话失效，否则退化为 hub 全量失效（D20）。
//
// 返回值：scope（actor|hub|none）、invalidated（稳定工具面是否真的清了）、
// evicted（actor 是否已被驱逐，下一次 GetOrCreate 按新会话状态重建）。
//
// 为什么必须驱逐（本地实测结论）：本地 chat 的回合由 actor 执行，agent 的工具
// 执行策略在 buildSessionActor 期由 session.ToolPolicy 固化（buildLocalChatToolPolicy
// → SetToolExecutionPolicy）。只清稳定工具面缓存不会让 agent 换策略——下一个 turn
// 的 provider 请求仍带着切换前的工具面（`/profile use` 后 45 → 45，启动路径同
// profile 是 26）。在途 turn 绝不打断：此时只登记延迟重建标记，由回合入口的
// reconcilePendingChatProfileRebuild 在 actor 空闲时兑现。
func invalidateChatProfileRuntime(session *ChatSession, inFlight bool) (scope string, invalidated bool, evicted bool) {
	resetStableSharedToolSurface(session)
	hub := chatSessionRuntimeHub(session)
	if hub == nil {
		clearPendingChatProfileRebuild(session)
		return profileSwitchSurfaceScopeNone, false, false
	}
	ctx := context.Background()
	sessionID := currentRuntimeSessionID(session)
	actor, ok := hub.Get(sessionID)
	if !ok || actor == nil {
		// 无活体 actor：下一次 GetOrCreate 本就会按新会话状态重建，天然取新面。
		if hub.InvalidateStableToolSurfaces(ctx) > 0 {
			clearPendingChatProfileRebuild(session)
			return profileSwitchSurfaceScopeHub, true, false
		}
		clearPendingChatProfileRebuild(session)
		return profileSwitchSurfaceScopeNone, false, false
	}
	if err := actor.InvalidateStableToolSurface(ctx); err == nil {
		invalidated = true
	}
	if inFlight || actor.RunInFlight() {
		// 二次确认在途（判定与动作之间可能刚起跑）：登记延迟重建，不驱逐。
		markPendingChatProfileRebuild(session)
		return profileSwitchSurfaceScopeActor, invalidated, false
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), profileSwitchActorEvictTimeout)
	defer cancel()
	_ = hub.StopContext(stopCtx, sessionID)
	clearPendingChatProfileRebuild(session)
	return profileSwitchSurfaceScopeActor, invalidated, true
}

// reconcilePendingChatProfileRebuild 在回合入口兑现"切换撞上在途 turn"的延迟重建：
// 旧 turn 结束、actor 空闲后驱逐它，让本轮 GetOrCreate 按新会话状态重建 agent
// （工具策略、系统提示词、provider/model 一并取新值）。
//
// 标记是会话级进程内字段：actor 也是进程内对象，进程重启后不存在旧 actor，
// 下一次构建本就会读到已落地的会话状态。
func reconcilePendingChatProfileRebuild(session *ChatSession) {
	if session == nil || !session.profileRebuildPending {
		return
	}
	hub := chatSessionRuntimeHub(session)
	if hub == nil {
		clearPendingChatProfileRebuild(session)
		return
	}
	sessionID := currentRuntimeSessionID(session)
	actor, ok := hub.Get(sessionID)
	if !ok || actor == nil {
		clearPendingChatProfileRebuild(session)
		return
	}
	if actor.RunInFlight() {
		// 仍然忙（例如后台唤醒的回合）：留到下一个边界再兑现。
		return
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), profileSwitchActorEvictTimeout)
	defer cancel()
	_ = hub.StopContext(stopCtx, sessionID)
	clearPendingChatProfileRebuild(session)
}

func markPendingChatProfileRebuild(session *ChatSession) {
	if session == nil {
		return
	}
	session.profileRebuildPending = true
}

func clearPendingChatProfileRebuild(session *ChatSession) {
	if session == nil {
		return
	}
	session.profileRebuildPending = false
}

// chatSessionRuntimeHub 返回会话的本地运行时 hub（未装配时 nil）。
func chatSessionRuntimeHub(session *ChatSession) *runtimechat.SessionHub {
	if session == nil || session.LocalRuntimeHost == nil {
		return nil
	}
	return session.LocalRuntimeHost.SessionHub
}

// chatSessionTurnInFlight 报告目标会话是否有在途 turn。它只用于报告标注：切换
// 本身仍然立即落地（会话状态是下一轮的事），在途前缀由冻结面保护（A3）。
func chatSessionTurnInFlight(session *ChatSession) bool {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil {
		return false
	}
	actor, ok := session.LocalRuntimeHost.SessionHub.Get(currentRuntimeSessionID(session))
	if !ok || actor == nil {
		return false
	}
	return actor.RunInFlight()
}

// applyChatProfileIdentityToRuntimeSession 写入 sessionmeta 的 profile 身份（⑩），
// 供 resume 与前端展示读取；随后由 syncRuntimeSessionFromChat 持久化。
func applyChatProfileIdentityToRuntimeSession(session *ChatSession, state *chatProfileState) {
	if session == nil || state == nil || !state.Active() {
		return
	}
	ctx := frozenChatSystemPromptContext(session.RuntimeSession, session)
	if ctx == nil {
		return
	}
	sessionmeta.Set(ctx, sessionmeta.ProfileRef, strings.TrimSpace(state.Reference))
	sessionmeta.Set(ctx, sessionmeta.ProfileName, strings.TrimSpace(state.Resolved.ProfileName))
	sessionmeta.Set(ctx, sessionmeta.ProfileAgent, strings.TrimSpace(state.Resolved.AgentID))
	sessionmeta.Set(ctx, sessionmeta.ProfileRoot, strings.TrimSpace(state.Resolved.ProfileRoot))
}

// profileSwitchDeferredDefaultWarnings 报告 provider/model/permission 的差异。
// 切换不隐式改写这三项：它们各有既有的权威切换路径（/provider、/model、权限
// 覆盖层），在 profile 切换里再改一次等于新增第二套路径（D30）。
func profileSwitchDeferredDefaultWarnings(session *ChatSession, state *chatProfileState) []string {
	if session == nil || state == nil || !state.Active() {
		return nil
	}
	var warnings []string
	declaredProvider := strings.TrimSpace(firstNonEmptyChatValue(state.Resolved.Provider, state.Resolved.DefaultProvider))
	if declaredProvider != "" && !strings.EqualFold(declaredProvider, strings.TrimSpace(session.ProviderName)) {
		warnings = append(warnings, fmt.Sprintf(
			"profile 声明 provider %q，当前为 %q；切换不改 provider，需显式 /provider 应用",
			declaredProvider, strings.TrimSpace(session.ProviderName)))
	}
	declaredModel := strings.TrimSpace(state.Resolved.Model)
	if declaredModel != "" && !strings.EqualFold(declaredModel, strings.TrimSpace(session.Model)) {
		warnings = append(warnings, fmt.Sprintf(
			"profile 声明 model %q，当前为 %q；切换不改 model，需显式 /model 应用",
			declaredModel, strings.TrimSpace(session.Model)))
	}
	if declaredMode := strings.TrimSpace(string(state.PermissionMode)); declaredMode != "" {
		currentMode := strings.TrimSpace(chatSessionStoredPermissionMode(session))
		if !strings.EqualFold(declaredMode, currentMode) {
			warnings = append(warnings, fmt.Sprintf(
				"profile 声明 permission_mode %q，当前为 %q；切换不改权限模式，需显式切换权限应用",
				declaredMode, currentMode))
		}
	}
	return warnings
}

// chatSessionStoredPermissionMode 读取会话已落地的权限模式（sessionmeta 权威键）。
func chatSessionStoredPermissionMode(session *ChatSession) string {
	if session == nil {
		return ""
	}
	ctx := frozenChatSystemPromptContext(session.RuntimeSession, session)
	if ctx == nil {
		return ""
	}
	return sessionmeta.String(ctx, sessionmeta.PermissionMode)
}

// profileSwitchToolPolicyWarnings 提示工具面安全属性的变化（read_only 翻转）。
// profile 只能收窄安全基线，因此这里的差异只用于告知，不阻断切换。
func profileSwitchToolPolicyWarnings(before chatProfileSurfaceSnapshot, state *chatProfileState) []string {
	if state == nil || !state.Active() || state.ToolPolicy == nil {
		return nil
	}
	if before.hasPolicy && before.readOnly != state.ToolPolicy.ReadOnly {
		return []string{fmt.Sprintf(
			"工具面 read_only 由 %t 变为 %t，下一轮起生效",
			before.readOnly, state.ToolPolicy.ReadOnly)}
	}
	return nil
}

// stringSetDifference 返回 a 中不在 b 里的条目（去重、排序、忽略空白）。
func stringSetDifference(a, b []string) []string {
	if len(a) == 0 {
		return nil
	}
	excluded := make(map[string]struct{}, len(b))
	for _, item := range b {
		if key := normalizeProfileSwitchKey(item); key != "" {
			excluded[key] = struct{}{}
		}
	}
	var result []string
	for _, item := range a {
		key := normalizeProfileSwitchKey(item)
		if key == "" {
			continue
		}
		if _, ok := excluded[key]; ok {
			continue
		}
		result = append(result, key)
	}
	return normalizeStringSet(result)
}

// mergeStringSets 合并两个集合（去重、排序）。
func mergeStringSets(sets ...[]string) []string {
	var merged []string
	for _, set := range sets {
		merged = append(merged, set...)
	}
	return normalizeStringSet(merged)
}

// normalizeStringSet 去重并排序，保证报告输出稳定（便于测试与前端 diff 渲染）。
func normalizeStringSet(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		key := normalizeProfileSwitchKey(value)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	if len(result) == 0 {
		return nil
	}
	sort.Strings(result)
	return result
}

// normalizeProfileSwitchKey 统一报告中的标识口径（去空白、大小写归一）。
func normalizeProfileSwitchKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// applyRuntimeProfileDetach 是 `/profile off` 的执行核心：把会话从任何 profile 绑定
// 退回"无 profile 基线"（等价启动时不带 --profile，§17.1）。它是 applyRuntimeProfileSwitch
// 的姊妹函数，共用同一套失效动作（①锚点 ②稳定工具面 ⑥token 计数 ⑩身份），
// 避免命令层复制失效逻辑（D20/D21）。
//
// 与 switch 的差异只有两点：生效面被清空而不是被替换；工具面回落到"无策略"，
// 随后重跑项目/CLI 权限覆盖层（chat_permissions_overlay.go），使基线语义与
// 启动路径一致（否则项目 permissions.yaml 会被一并丢弃）。
func applyRuntimeProfileDetach(session *ChatSession) (*ProfileSwitchReport, error) {
	if session == nil {
		return nil, fmt.Errorf("chat session is required")
	}
	before := snapshotChatProfileSurface(session)
	report := &ProfileSwitchReport{
		From:         before.reference,
		To:           "",
		Changed:      ProfileSwitchChanged{},
		EffectiveAt:  profileSwitchEffectiveAt,
		CacheNotice:  profileSwitchCacheNotice,
		InFlightTurn: chatSessionTurnInFlight(session),
	}
	// 差异报告：当前有、基线无 = 全部移除。
	report.Changed.ToolsRemoved = append(report.Changed.ToolsRemoved, before.toolNames...)
	report.Changed.SkillsRemoved = append(report.Changed.SkillsRemoved, before.skillAllow...)
	report.Changed.SkillsRemoved = append(report.Changed.SkillsRemoved, before.skillDeny...)
	report.Changed.MCPRemoved = append(report.Changed.MCPRemoved, before.mcpUse...)
	report.Changed.MCPRemoved = append(report.Changed.MCPRemoved, before.mcpExclude...)
	report.Changed.PromptChanged = strings.TrimSpace(before.promptText) != ""

	// 阶段 2：清空 profile 生效面（字段与 applyProfileStateToChatSession 一一对应）。
	session.ProfileReference = ""
	session.ProfileName = ""
	session.ProfileAgent = ""
	session.ProfileRoot = ""
	session.AgentSourcePath = ""
	session.AgentSource = ""
	session.SystemPromptText = ""
	session.RuntimeConfigPath = ""
	session.MCPConfigPath = ""
	session.ResolvedSkillDirs = nil
	session.ProfileSkillSelection = runtimeprofileinput.ResolvedSkillSelection{}
	session.ProfileMCPSelection = runtimeprofileinput.ResolvedMCPSelection{}
	session.ProfilePromptMode = ""
	session.ProfilePromptSuppressed = false
	session.ProfilePromptSuppressionReason = ""
	session.ProfileContext = nil
	session.ToolPolicy = nil
	session.BaseToolPolicy = nil
	// 基线之上仍然保留项目/CLI 的权限覆盖（与启动路径的叠加顺序一致）。
	applyChatPermissionsOverlay(session, "")
	if session.FunctionCatalog != nil {
		session.FunctionCatalog.SetToolPolicy(session.ToolPolicy)
	}

	// 阶段 3：失效一次到位（与 switch 同款动作集）。
	report.AnchorCleared = clearFrozenChatSystemPromptAnchor(session)
	report.ToolSurfaceScope, report.ToolSurfaceInvalidated, report.ActorEvicted =
		invalidateChatProfileRuntime(session, report.InFlightTurn)
	session.ContextWindowTokenCount = 0
	report.ContextTokenCountReset = true

	// 阶段 4：身份与持久化——删除 sessionmeta 中的 profile 身份键（⑩）。
	if ctx := frozenChatSystemPromptContext(session.RuntimeSession, session); ctx != nil {
		sessionmeta.Delete(ctx, sessionmeta.ProfileRef)
		sessionmeta.Delete(ctx, sessionmeta.ProfileName)
		sessionmeta.Delete(ctx, sessionmeta.ProfileAgent)
		sessionmeta.Delete(ctx, sessionmeta.ProfileRoot)
	}
	warnIfChatSessionSyncFails(session, "detach profile", syncRuntimeSessionFromChat(session))

	// 阶段 5：状态栏刷新。
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
	return report, nil
}
