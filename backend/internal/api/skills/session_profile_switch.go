package skills

import (
	"context"
	stderrors "errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// ── 服务器侧 profile 会话级切换执行核心（Batch 12；D19/D21/D23 的 server 半程） ──
//
// 与 CLI 侧 `cmd/aicli/commands/chat_profile_switch.go` 的同构关系：
//   - 同一份 JSON 契约（字段名即前端契约，见 ProfileSwitchReport）；
//   - 同一套五阶段（解析 → 应用 → 失效 → 身份与持久化 → 报告）；
//   - 同一组失效动作：①prompt 冻结锚点 ②稳定工具面 ⑥会话 token 计数。
//
// 核实结论（V15/V19，2026-09-24；设计文档附录 D3/D4）决定的 server 差异：
//   - server 的 agent / 系统提示词 / 工具策略在 **actor 构建期**固化
//     （`buildSessionActor`，session_runtime_support.go:3889-4015），`PrepareRun`
//     只做会话租约（:4102-4104）——因此「下一 turn 生效」的权威路径是
//     **驱逐空闲 actor**，让下一次 `GetOrCreate` 按新 sessionmeta 重建；
//     仅写 sessionmeta 而不驱逐等于假开关；
//   - 在途 turn 绝不打断（D18/A3）：actor 有在途回合时只做失效与身份落地，
//     由下一次切换/会话空闲后的重建收敛（`in_flight_turn` 如实上报）；
//   - Web 直连路径（`POST /api/agent/chat`）不使用 actor，其「下一轮生效」由
//     AgentChat 的会话绑定回退保证（见 handler.go 的 effectiveProfile 解析）。
const (
	sessionProfileSwitchEffectiveAt = "next_turn"
	sessionProfileSwitchCacheNotice = "下一轮起生效；请求前缀变化将使 provider prompt cache 重建（首轮输入计费增加）"

	// 稳定工具面失效路径（D20 的 server 口径：无 hub 全量回退——server 的单会话
	// 失效始终是精确的，拿不到句柄时下一次 GetOrCreate 本就会重建）。
	sessionProfileSwitchScopeActor = "actor"
	sessionProfileSwitchScopeNone  = "none"

	// sessionProfileActorEvictTimeout 是驱逐空闲 actor 的等待上限：驱逐失败也
	// 不阻塞命令返回——actor 已从 hub 摘除，后台停止完成后下一次构建同样取新面。
	sessionProfileActorEvictTimeout = 5 * time.Second
)

// sessionProfileSwitchChanged 是切换报告的 changed 投影（D23）。与 CLI 侧
// `ProfileSwitchChanged` 逐字段同构：TUI 与前端渲染同一份结构，避免两套文案漂移。
type sessionProfileSwitchChanged struct {
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

// sessionProfileSwitchReport 是 `applySessionProfileSwitch` 的结构化输出（D23）。
// changed 的语义是「本次切换实际应用的生效面差异」：provider/model/permission
// 只报告不隐式应用（D30），因此这三项恒为 false，差异以 warnings 呈现。
type sessionProfileSwitchReport struct {
	From        string                       `json:"from"`
	To          string                       `json:"to"`
	Changed     sessionProfileSwitchChanged  `json:"changed"`
	EffectiveAt string                       `json:"effective_at"`
	CacheNotice string                       `json:"cache_notice"`
	Warnings    []string                     `json:"warnings,omitempty"`
	// InFlightTurn 记录切换发生时是否有在途 turn；在途 turn 的冻结前缀由
	// 存储层与活体 agent 配置保护（A3），切换本身仍然立即落地会话状态。
	InFlightTurn bool `json:"in_flight_turn"`
	// AnchorCleared 表示 prompt 冻结锚点是否被删除（下次 compose 重新组合 head）。
	AnchorCleared bool `json:"anchor_cleared"`
	// ToolSurfaceInvalidated/Scope 表示稳定工具面失效的实际路径：actor（精确
	// 单会话）| none（无活体 actor，下一次重建天然取新面）。
	ToolSurfaceInvalidated bool   `json:"tool_surface_invalidated"`
	ToolSurfaceScope       string `json:"tool_surface_scope,omitempty"`
	// ActorEvicted 表示空闲 actor 是否已被驱逐（下一次 turn 按新 sessionmeta
	// 重建，这是 server 侧「下一轮生效」的唯一权威路径，V15/V19）。
	ActorEvicted bool `json:"actor_evicted"`
	// ContextTokenCountReset 表示会话累计 token 计数已清零（⑥）。
	ContextTokenCountReset bool `json:"context_token_count_reset"`
}

// sessionProfileSwitchValidationError 标记「请求本身不合法 / profile 无法解析」
// 的错误，由命令层映射为 400；其它错误（存储失败等）按 500 处理。
type sessionProfileSwitchValidationError struct{ message string }

func (e *sessionProfileSwitchValidationError) Error() string { return e.message }

func newSessionProfileSwitchValidationError(format string, args ...interface{}) error {
	return &sessionProfileSwitchValidationError{message: fmt.Sprintf(format, args...)}
}

func isSessionProfileSwitchValidationError(err error) bool {
	var target *sessionProfileSwitchValidationError
	return stderrors.As(err, &target)
}

// sessionProfileSurfaceSnapshot 捕获切换前的生效面，作为 diff 基线。
//
// server 侧没有 CLI 的「会话内投影字段」（session.SystemPromptText 等），生效面
// 一律来自**当前绑定的解析结果**（与 buildSessionActor 的读取点同源）。绑定为空
// 或解析失败时基线为空面：diff 退化为「新增」，与「从无 profile 基线切入」一致。
type sessionProfileSurfaceSnapshot struct {
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
}

func sessionProfileSurfaceFromState(reference string, state *profileRuntimeState) sessionProfileSurfaceSnapshot {
	snapshot := sessionProfileSurfaceSnapshot{reference: strings.TrimSpace(reference)}
	if state == nil || state.Resolved == nil {
		return snapshot
	}
	snapshot.promptText = strings.TrimSpace(state.PromptText)
	snapshot.promptMode = strings.TrimSpace(state.Resolved.PromptMode)
	snapshot.skillAllow = append([]string(nil), state.Resolved.Skills.Allowlist...)
	snapshot.skillDeny = append([]string(nil), state.Resolved.Skills.Denylist...)
	snapshot.mcpUse = append([]string(nil), state.Resolved.MCPSelection.UseServers...)
	snapshot.mcpExclude = append([]string(nil), state.Resolved.MCPSelection.ExcludeServers...)
	if state.ToolPolicy != nil {
		snapshot.hasPolicy = true
		snapshot.readOnly = state.ToolPolicy.ReadOnly
		snapshot.toolNames = state.ToolPolicy.AllowedToolNames()
	}
	return snapshot
}

// buildSessionProfileSwitchChanged 计算旧/新生效面差异。skills/mcp 的 added/removed
// 按「暴露面」语义：added = 新增放行 ∪ 新增解除排除，removed = 取消放行 ∪ 新增排除。
func buildSessionProfileSwitchChanged(before sessionProfileSurfaceSnapshot, state *profileRuntimeState) sessionProfileSwitchChanged {
	changed := sessionProfileSwitchChanged{}
	if state == nil || state.Resolved == nil {
		return changed
	}
	after := sessionProfileSurfaceFromState("", state)

	changed.ToolsAdded = sessionProfileSwitchSetDifference(after.toolNames, before.toolNames)
	changed.ToolsRemoved = sessionProfileSwitchSetDifference(before.toolNames, after.toolNames)
	changed.SkillsAdded = sessionProfileSwitchMergeSets(
		sessionProfileSwitchSetDifference(after.skillAllow, before.skillAllow),
		sessionProfileSwitchSetDifference(before.skillDeny, after.skillDeny),
	)
	changed.SkillsRemoved = sessionProfileSwitchMergeSets(
		sessionProfileSwitchSetDifference(before.skillAllow, after.skillAllow),
		sessionProfileSwitchSetDifference(after.skillDeny, before.skillDeny),
	)
	changed.MCPAdded = sessionProfileSwitchMergeSets(
		sessionProfileSwitchSetDifference(after.mcpUse, before.mcpUse),
		sessionProfileSwitchSetDifference(before.mcpExclude, after.mcpExclude),
	)
	changed.MCPRemoved = sessionProfileSwitchMergeSets(
		sessionProfileSwitchSetDifference(before.mcpUse, after.mcpUse),
		sessionProfileSwitchSetDifference(after.mcpExclude, before.mcpExclude),
	)
	changed.PromptChanged = after.promptText != before.promptText || after.promptMode != before.promptMode
	// provider/model/permission 由既有显式命令落地（D30），切换本身不改动。
	changed.ProviderChanged = false
	changed.ModelChanged = false
	changed.PermissionModeChanged = false
	return changed
}

// sessionProfileSwitchSetDifference 返回 a 中不在 b 里的条目（去重、排序、忽略空白）。
func sessionProfileSwitchSetDifference(a, b []string) []string {
	if len(a) == 0 {
		return nil
	}
	excluded := make(map[string]struct{}, len(b))
	for _, item := range b {
		if key := sessionProfileSwitchKey(item); key != "" {
			excluded[key] = struct{}{}
		}
	}
	var result []string
	for _, item := range a {
		key := sessionProfileSwitchKey(item)
		if key == "" {
			continue
		}
		if _, ok := excluded[key]; ok {
			continue
		}
		result = append(result, key)
	}
	return sessionProfileSwitchNormalizeSet(result)
}

func sessionProfileSwitchMergeSets(sets ...[]string) []string {
	var merged []string
	for _, set := range sets {
		merged = append(merged, set...)
	}
	return sessionProfileSwitchNormalizeSet(merged)
}

func sessionProfileSwitchNormalizeSet(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		key := sessionProfileSwitchKey(value)
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

func sessionProfileSwitchKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// applySessionProfileSwitch 是 server 侧会话内 profile 热切换的唯一执行核心（D19/D21）。
// 五阶段（设计文档 §18.1）：解析 → 应用 → 失效 → 身份与持久化 → 报告。
// 解析失败显式返回错误，不静默回退到旧 profile（FR-5）。
func (h *Handler) applySessionProfileSwitch(ctx context.Context, sessionID, profileRef string) (*sessionProfileSwitchReport, error) {
	if h == nil || h.sessionManager == nil {
		return nil, fmt.Errorf("session manager not configured")
	}
	sessionID = chat.NormalizeSessionID(sessionID)
	if sessionID == "" {
		return nil, newSessionProfileSwitchValidationError("session id is required")
	}
	ref := strings.TrimSpace(profileRef)
	if ref == "" {
		return nil, newSessionProfileSwitchValidationError("profile 名称不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// 阶段 0：会话读取。session_history.sqlite 可能与并发 aicli 进程共享，写锁
	// 占用时 Get 会阻塞在 busy_timeout 上，因此带截止时间读取并快速失败。
	getCtx, getCancel := context.WithTimeout(ctx, sessionStoreQueryTimeout)
	session, err := h.sessionManager.Get(getCtx, sessionID)
	getCancel()
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, newSessionProfileSwitchValidationError("session %q not found", sessionID)
	}
	if session.Metadata.Context == nil {
		session.Metadata.Context = make(map[string]interface{})
	}
	meta := session.Metadata.Context

	workspacePath := sessionmeta.String(meta, sessionmeta.WorkspacePath)
	if worktreePath := sessionmeta.String(meta, toolbroker.AgentSessionContextWorktreePath); worktreePath != "" {
		workspacePath = worktreePath
	}
	agentID := sessionmeta.String(meta, sessionmeta.ProfileAgent)

	// 基线：当前绑定解析出的生效面。解析失败不是错误——它只说明基线为空面
	//（E2E-7：被删除的绑定不能让切换本身失败，只让 diff 退化为「新增」）。
	before := sessionProfileSurfaceSnapshot{reference: sessionmeta.String(meta, sessionmeta.ProfileRef)}
	if current, resolveErr := h.resolveProfileSessionState(before.reference, agentID, workspacePath); resolveErr == nil {
		before = sessionProfileSurfaceFromState(before.reference, current)
	}

	// 阶段 1：解析新 profile（与 actor 构建期同一函数，避免第二套解析路径）。
	state, err := h.resolveProfileSessionState(ref, agentID, workspacePath)
	if err != nil {
		return nil, newSessionProfileSwitchValidationError("profile %q 解析失败: %s", ref, err)
	}
	if state == nil || state.Resolved == nil {
		return nil, newSessionProfileSwitchValidationError("profile %q 未解析出有效绑定", ref)
	}

	report := &sessionProfileSwitchReport{
		From:         before.reference,
		To:           strings.TrimSpace(state.Reference),
		Changed:      buildSessionProfileSwitchChanged(before, state),
		EffectiveAt:  sessionProfileSwitchEffectiveAt,
		CacheNotice:  sessionProfileSwitchCacheNotice,
		InFlightTurn: h.sessionProfileTurnInFlight(sessionID),
	}

	// 阶段 2：应用生效面（sessionmeta 身份四键；与 AgentChat / actor 构建共用同一投影）。
	h.applyProfileSessionContext(session, state)
	report.Warnings = append(report.Warnings, sessionProfileSwitchDeferredWarnings(session, state)...)
	report.Warnings = append(report.Warnings, sessionProfileSwitchPolicyWarnings(before, state)...)

	// 阶段 3：失效一次到位（D19）——①锚点 ②稳定工具面 + 空闲 actor 驱逐 ⑥token 计数。
	report.AnchorCleared = clearSessionFrozenPromptAnchor(meta)
	report.ToolSurfaceScope, report.ToolSurfaceInvalidated, report.ActorEvicted = h.invalidateSessionProfileRuntime(ctx, sessionID, report.InFlightTurn)
	report.ContextTokenCountReset = resetSessionContextTokenCount(meta)
	if report.InFlightTurn && !report.ActorEvicted {
		report.Warnings = append(report.Warnings, sessionProfileSwitchDeferredWarning)
	}

	// 阶段 4：身份与持久化。写失败必须显式返回：报告不能声称一个没落地的切换。
	updateCtx, updateCancel := context.WithTimeout(ctx, sessionStoreQueryTimeout)
	err = h.sessionManager.Update(updateCtx, session)
	updateCancel()
	if err != nil {
		return nil, err
	}
	return report, nil
}

const sessionProfileSwitchDeferredWarning = "当前会话有在途 turn：本轮不打断（A3），actor 将在下一个命令入口按新 profile 重建"

// sessionProfileTurnInFlight 报告目标会话是否有在途 turn。它只用于报告标注：切换
// 本身仍然立即落地（会话状态是下一轮的事），在途前缀由冻结面保护（A3）。
func (h *Handler) sessionProfileTurnInFlight(sessionID string) bool {
	hub := h.peekSessionHub()
	if hub == nil {
		return false
	}
	actor, ok := hub.Get(sessionID)
	if !ok || actor == nil {
		return false
	}
	return actor.RunInFlight()
}

// invalidateSessionProfileRuntime 重置稳定工具面缓存并驱逐空闲 actor（②）。
//
// 返回值：scope（actor|none）、invalidated（稳定工具面是否真的清了）、evicted
//（actor 是否已被驱逐，下一次 GetOrCreate 将按新 sessionmeta 重建）。
//
// 为什么必须驱逐（V15/V19 核实结论）：server 的 agent / 系统提示词 / 工具策略在
// `buildSessionActor` 构建期固化，`PrepareRun` 只做租约；hub 复用在位 actor，
// 因此「只写 sessionmeta」不会在下一个 turn 生效。在途 turn 绝不打断（D18/A3）：
// 此时只失效并留下重建标记，由命令入口在 actor 空闲时兑现。
func (h *Handler) invalidateSessionProfileRuntime(ctx context.Context, sessionID string, inFlight bool) (scope string, invalidated bool, evicted bool) {
	hub := h.peekSessionHub()
	if hub == nil {
		return sessionProfileSwitchScopeNone, false, false
	}
	actor, ok := hub.Get(sessionID)
	if !ok || actor == nil {
		// 无活体 actor：下一次 GetOrCreate 本就会按新 sessionmeta 重建，天然取新面。
		h.clearPendingProfileSwitch(sessionID)
		return sessionProfileSwitchScopeNone, false, false
	}
	// ②稳定工具面：清空会话级缓存，使后续 turn 重新冻结工具前缀。在途 turn 的
	// 冻结前缀由 turn 内快照保护（A3），这里不会改写它。
	if err := actor.InvalidateStableToolSurface(ctx); err == nil {
		invalidated = true
	}
	scope = sessionProfileSwitchScopeActor
	if inFlight || actor.RunInFlight() {
		// 二次确认在途（判定与动作之间可能刚起跑）：登记重建标记，不驱逐。
		h.markPendingProfileSwitch(sessionID)
		return scope, invalidated, false
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), sessionProfileActorEvictTimeout)
	defer cancel()
	_ = hub.StopContext(stopCtx, sessionID)
	h.clearPendingProfileSwitch(sessionID)
	return scope, invalidated, true
}

// markPendingProfileSwitch / clearPendingProfileSwitch / takePendingProfileSwitch 维护
// 「切换撞上在途 turn」的延迟重建标记。标记只在进程内有效：actor 是进程内对象，
// 重启后不存在旧 actor，下一次构建本就会读到已落地的 sessionmeta。
func (h *Handler) markPendingProfileSwitch(sessionID string) {
	if h == nil {
		return
	}
	sessionID = chat.NormalizeSessionID(sessionID)
	if sessionID == "" {
		return
	}
	h.profileSwitchMu.Lock()
	defer h.profileSwitchMu.Unlock()
	if h.profileSwitchPending == nil {
		h.profileSwitchPending = make(map[string]string)
	}
	h.profileSwitchPending[sessionID] = sessionID
}

func (h *Handler) clearPendingProfileSwitch(sessionID string) {
	if h == nil {
		return
	}
	sessionID = chat.NormalizeSessionID(sessionID)
	if sessionID == "" {
		return
	}
	h.profileSwitchMu.Lock()
	defer h.profileSwitchMu.Unlock()
	delete(h.profileSwitchPending, sessionID)
}

func (h *Handler) hasPendingProfileSwitch(sessionID string) bool {
	if h == nil {
		return false
	}
	sessionID = chat.NormalizeSessionID(sessionID)
	if sessionID == "" {
		return false
	}
	h.profileSwitchMu.Lock()
	defer h.profileSwitchMu.Unlock()
	_, ok := h.profileSwitchPending[sessionID]
	return ok
}

// reconcilePendingProfileSwitch 在命令入口兑现延迟重建：actor 已空闲（或已不在
// 位）时清除标记并驱逐旧 actor，使紧随其后的 GetOrCreate 按新 profile 重建。
// 它只做一次进程内 map 查询，不给命令热路径增加存储读取。
func (h *Handler) reconcilePendingProfileSwitch(sessionID string) {
	if h == nil || !h.hasPendingProfileSwitch(sessionID) {
		return
	}
	hub := h.peekSessionHub()
	if hub == nil {
		h.clearPendingProfileSwitch(sessionID)
		return
	}
	actor, ok := hub.Get(sessionID)
	if !ok || actor == nil {
		// 旧 actor 已经不在位（空闲驱逐/进程重启）：下一次构建天然取新 profile。
		h.clearPendingProfileSwitch(sessionID)
		return
	}
	if actor.RunInFlight() {
		// 仍在途：保持标记，等下一个边界。
		return
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), sessionProfileActorEvictTimeout)
	defer cancel()
	_ = hub.StopContext(stopCtx, sessionID)
	h.clearPendingProfileSwitch(sessionID)
}

// clearSessionFrozenPromptAnchor 删除会话级 prompt 冻结锚点，使下一次 compose
// 重新组合 head（①）。锚点只在下一次 compose 被读取，因此在途 turn 的 head 不受影响。
func clearSessionFrozenPromptAnchor(meta map[string]interface{}) bool {
	if meta == nil {
		return false
	}
	if _, ok := sessionmeta.Value(meta, sessionmeta.SystemPromptFrozen); !ok {
		return false
	}
	sessionmeta.Delete(meta, sessionmeta.SystemPromptFrozen)
	return true
}

// resetSessionContextTokenCount 清零会话累计 token 计数（⑥，含 legacy 别名键）。
// 计数是「同一请求前缀的累计」口径：前缀随切换重建，旧计数会让下一轮误判配额。
func resetSessionContextTokenCount(meta map[string]interface{}) bool {
	if meta == nil {
		return false
	}
	reset := false
	for _, key := range []string{sessionmeta.ContextWindowCount, sessionmeta.LegacyAICLIContextWindowCount} {
		if _, ok := sessionmeta.Value(meta, key); !ok {
			continue
		}
		sessionmeta.Delete(meta, key)
		reset = true
	}
	return reset
}

// sessionProfileSwitchDeferredWarnings 报告 provider/model 的差异。切换不隐式改写
// 这两项：它们各有既有的权威切换路径（/provider、/model），在 profile 切换里再改
// 一次等于新增第二套路径（D30）。
func sessionProfileSwitchDeferredWarnings(session *chat.Session, state *profileRuntimeState) []string {
	if session == nil || state == nil || state.Resolved == nil {
		return nil
	}
	meta := session.Metadata.Context
	var warnings []string
	declaredProvider := strings.TrimSpace(firstNonEmptyString(state.Resolved.Provider, state.Resolved.DefaultProvider))
	if declaredProvider != "" && !strings.EqualFold(declaredProvider, sessionmeta.String(meta, sessionmeta.ProviderName)) {
		warnings = append(warnings, fmt.Sprintf(
			"profile 声明 provider %q，当前为 %q；切换不改 provider，需显式 /provider 应用",
			declaredProvider, sessionmeta.String(meta, sessionmeta.ProviderName)))
	}
	declaredModel := strings.TrimSpace(state.Resolved.Model)
	if declaredModel != "" && !strings.EqualFold(declaredModel, sessionmeta.String(meta, sessionmeta.Model)) {
		warnings = append(warnings, fmt.Sprintf(
			"profile 声明 model %q，当前为 %q；切换不改 model，需显式 /model 应用",
			declaredModel, sessionmeta.String(meta, sessionmeta.Model)))
	}
	return warnings
}

// sessionProfileSwitchPolicyWarnings 提示工具面安全属性的变化（read_only 翻转）。
// profile 只能收窄安全基线，因此这里的差异只用于告知，不阻断切换。
func sessionProfileSwitchPolicyWarnings(before sessionProfileSurfaceSnapshot, state *profileRuntimeState) []string {
	if state == nil || state.ToolPolicy == nil {
		return nil
	}
	if before.hasPolicy && before.readOnly != state.ToolPolicy.ReadOnly {
		return []string{fmt.Sprintf(
			"工具面 read_only 由 %t 变为 %t，下一轮起生效",
			before.readOnly, state.ToolPolicy.ReadOnly)}
	}
	return nil
}
