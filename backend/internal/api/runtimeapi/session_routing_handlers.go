package runtimeapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// 会话级路由管理 API（方案 §4.4/§4.5/§6.5）。
//
//	GET   /api/runtime/sessions/{id}/routing  只读投影（§6.1 同源）+ 面板元数据
//	PATCH /api/runtime/sessions/{id}/routing  写入/清除目标层（session|workspace|config）
//
// 语义要点：
//   - 写入成功后失效 actor（§4.5：下一 turn 生效，进行中 turn 不受影响）；
//   - 校验失败 400 且不落盘（§3.5 阶梯回退后仍非法才拒绝，U-3）；
//   - 子会话写入 409（M16），GET 仍可读继承结果；
//   - 层路由（v3）：session=会话 context，workspace=会话绑定 workspace 的
//     chat-prefs.yaml（N9），config=按 WritableLayer 路由的 .aicli/config.yaml；
//   - 鉴权与既有会话写端点同级别（M4）。

const (
	sessionRoutingLayerSession   = "session"
	sessionRoutingLayerWorkspace = "workspace"
	sessionRoutingLayerConfig    = "config"

	// sessionRoutingActorStopTimeout 限制失效等待，避免慢 actor 拖住 API。
	sessionRoutingActorStopTimeout = 5 * time.Second

	// sessionRoutingMaxBodyBytes 限制 PATCH /routing 请求体（补丁是小对象）。
	sessionRoutingMaxBodyBytes = 64 << 10
)

// sessionRoutingPatchRequest 是 PATCH /routing 的请求体（§3.2 补丁 + 层路由字段）。
type sessionRoutingPatchRequest struct {
	TargetLayer string `json:"target_layer,omitempty"`
	Clear       bool   `json:"clear,omitempty"`
	Confirm     bool   `json:"confirm,omitempty"`

	MainAgent   *agentconfig.AICLISessionMainAgentRoutingOverride `json:"main_agent,omitempty"`
	SubAgent    *agentconfig.AICLISessionSubAgentRoutingOverride  `json:"sub_agent,omitempty"`
	ClearFields []string                                          `json:"clear_fields,omitempty"`
	UpdatedBy   string                                            `json:"updated_by,omitempty"`
}

// sessionRoutingResponse 是 GET/PATCH /routing 的响应（§6.5）。
type sessionRoutingResponse struct {
	SessionID string `json:"session_id"`
	Scope     string `json:"scope"`

	TargetLayer      string `json:"target_layer"`
	TargetPath       string `json:"target_path,omitempty"`
	ActorInvalidated bool   `json:"actor_invalidated"`
	Updated          bool   `json:"updated"`

	Routing  agentconfig.RoutingStatusProjection `json:"routing"`
	SubAgent agentconfig.RoutingStatusProjection `json:"sub_agent"`
	Panel    agentconfig.RoutingPanelMetadata    `json:"panel"`
	Warnings []string                            `json:"warnings,omitempty"`
}

// GetSessionRouting 返回会话路由的只读投影与面板元数据（§4.4/§6.5）。
func (h *Handler) GetSessionRouting(w http.ResponseWriter, r *http.Request) {
	session, err := h.loadSessionForPlanMode(r)
	if err != nil {
		h.writePlanModeError(w, err)
		return
	}
	response := h.buildSessionRoutingResponse(session, "", false, true)
	h.writeJSON(w, http.StatusOK, response)
}

// UpdateSessionRouting 写入或清除目标层的路由覆盖（§4.4）。
func (h *Handler) UpdateSessionRouting(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session manager not configured"))
		return
	}
	if err := h.authorizeSessionRoutingWrite(r); err != nil {
		// 与既有会话写端点同级别的拒绝语义：未授权 = 403（M4/U-10）。
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	session, err := h.loadSessionForPlanMode(r)
	if err != nil {
		h.writePlanModeError(w, err)
		return
	}
	sessionID := strings.TrimSpace(session.ID)
	if sessionID == "" {
		sessionID = runtimechat.NormalizeSessionID(mux.Vars(r)["id"])
	}

	var request sessionRoutingPatchRequest
	// 严格解码：未知字段通常意味着调用方拼错了键，静默忽略会写出与调用方
	// 意图不符的配置（例如把 main_agent 拼错后触发「空补丁=清除」）。
	r.Body = http.MaxBytesReader(w, r.Body, sessionRoutingMaxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&request); decodeErr != nil && decodeErr != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}

	layer := strings.ToLower(strings.TrimSpace(request.TargetLayer))
	if layer == "" {
		layer = sessionRoutingLayerSession
	}
	switch layer {
	case sessionRoutingLayerSession, sessionRoutingLayerWorkspace, sessionRoutingLayerConfig:
	default:
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"unsupported target_layer "+strings.TrimSpace(request.TargetLayer)+" (session|workspace|config)"))
		return
	}

	// 子会话写入拒绝（M16）：子 Agent 路由由父会话 sub 作用域在 spawn 时解析。
	if sessionIsChildAgent(session) {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed,
			"session is a child agent: routing overrides are managed by the parent session"))
		return
	}

	patch := &agentconfig.SessionRoutingPatch{
		MainAgent:   request.MainAgent,
		SubAgent:    request.SubAgent,
		ClearFields: request.ClearFields,
		UpdatedBy:   request.UpdatedBy,
	}
	// §4.5：clear=true 或空补丁 = 清除该层覆盖。
	clearRequested := request.Clear || !patch.HasContent()

	workspacePath := sessionRoutingWorkspacePath(session)
	workspacePrefs, workspacePrefsErr := agentconfig.LoadWorkspaceRoutingPreferencesForPath(workspacePath)
	if workspacePrefsErr != nil {
		// 读取失败不能按「无工作区覆盖」继续：那会跳过落盘前校验并写出一份
		// 与工作区现状不符的配置（I-6：投影与落盘必须以同一份事实为准）。
		h.writeError(w, http.StatusInternalServerError, errors.New(errors.ErrConfigInvalid,
			"read workspace routing preferences failed: "+workspacePrefsErr.Error()))
		return
	}

	ctx, cancel := sessionStoreQueryContext(r)
	defer cancel()

	targetPath := ""
	switch layer {
	case sessionRoutingLayerSession:
		// §3.4/M11：同一会话的「读-改-写」必须在同一把会话锁内完成。锁外读到的
		// 会话只用于鉴权/计划模式判定，写回基准必须是锁内重读的最新快照——否则
		// 并发 PATCH 会各自基于同一份旧快照落盘，后写者静默覆盖前者的字段
		// （U-2 丢更新）。TUI 本地写入取的是同一把锁（LockSessionWrite）。
		unlockSession := runtimechat.LockSessionWrite(sessionID)
		// 兜底释放：释放函数幂等，任何提前返回都不会漏放锁。
		defer unlockSession()
		fresh, reloadErr := h.reloadSessionForRoutingWrite(ctx, sessionID)
		if reloadErr != nil {
			h.writePlanModeError(w, reloadErr)
			return
		}
		if fresh != nil {
			session = fresh
		}
		merged, clearAll, applyErr := h.applySessionRoutingOverride(session, patch, clearRequested)
		if applyErr != nil {
			h.writeError(w, http.StatusInternalServerError, applyErr)
			return
		}
		if !clearAll {
			if err := h.validateResolvedSessionRouting(merged, workspacePrefs); err != nil {
				h.writePlanModeError(w, err)
				return
			}
		}
		if err := h.sessionManager.Update(ctx, session); err != nil {
			writeSessionStoreError(w, err)
			return
		}
		// 落盘完成即释放：后续 actor 失效与事件发布不再占用会话写锁。
		unlockSession()
	case sessionRoutingLayerWorkspace:
		if clearRequested {
			patch = &agentconfig.SessionRoutingPatch{ClearFields: []string{"main_agent", "sub_agent"}}
		}
		if err := h.validateWorkspaceRoutingWrite(workspacePath, patch); err != nil {
			h.writePlanModeError(w, err)
			return
		}
		if err := agentconfig.UpdateWorkspaceRoutingSection(workspacePath, patch); err != nil {
			h.writePlanModeError(w, err)
			return
		}
		targetPath = agentconfig.WorkspaceRoutingTargetPath(workspacePath)
	case sessionRoutingLayerConfig:
		path, layerKind := agentconfig.AICLIConfigWriteTargetForRouting()
		if strings.TrimSpace(path) == "" {
			h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "config write target unavailable"))
			return
		}
		if !request.Confirm {
			// §5.4/I-2：config 层写入需二次确认（影响所有会话）。
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
				"config layer write requires confirm=true (affects all sessions); target="+path+" ("+layerKind+")"))
			return
		}
		if err := h.writeConfigRoutingSection(path, patch, clearRequested); err != nil {
			h.writePlanModeError(w, err)
			return
		}
		targetPath = path
	}

	actorInvalidated := h.invalidateSessionRoutingActor(sessionID)

	// 事件顺序（§7.1）：落盘 → 发布 → 失效已在上一步完成。
	response := h.buildSessionRoutingResponse(session, layer, true, actorInvalidated)
	response.TargetPath = targetPath
	h.publishSessionRoutingChanged(sessionID, response)
	h.writeJSON(w, http.StatusOK, response)
}

// ---------------------------------------------------------------------------
// session 层

// reloadSessionForRoutingWrite 在会话级写锁内重读会话记录，作为「读-改-写」的
// 写回基准（M11）。会话记录不存在时返回 nil，调用方沿用锁外读到的快照。
func (h *Handler) reloadSessionForRoutingWrite(ctx context.Context, sessionID string) (*runtimechat.Session, error) {
	if h == nil || h.sessionManager == nil || strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	fresh, err := h.sessionManager.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return fresh, nil
}

// applySessionRoutingOverride 把补丁合并进会话 context（M11 串行化：读-改-写）。
// 返回合并结果、「是否已整节清除」与错误。
//
// 编码失败必须报错：按「已清除」返回会静默保留旧覆盖、跳过落盘前校验，
// 而响应仍报成功（旧覆盖静默保留 + 假成功）。
func (h *Handler) applySessionRoutingOverride(session *runtimechat.Session, patch *agentconfig.SessionRoutingPatch, clearRequested bool) (*agentconfig.AICLISessionRoutingOverride, bool, error) {
	if session == nil {
		return nil, true, nil
	}
	base, err := agentconfig.DecodeSessionRoutingOverride(sessionRoutingOverrideRaw(session))
	if err != nil {
		// 损坏的覆盖按「无覆盖」处理（B5：解析恒非 nil，不因覆盖损坏而失败）。
		base = nil
	}
	if clearRequested {
		if session.Metadata.Context != nil {
			delete(session.Metadata.Context, agentconfig.SessionRoutingOverrideContextKey)
		}
		return nil, true, nil
	}
	merged := agentconfig.MergeSessionRoutingOverride(base, patch, time.Now().UTC(), patchUpdatedBy(patch))
	if merged == nil {
		if session.Metadata.Context != nil {
			delete(session.Metadata.Context, agentconfig.SessionRoutingOverrideContextKey)
		}
		return nil, true, nil
	}
	encoded, encodeErr := agentconfig.EncodeSessionRoutingOverride(merged)
	if encodeErr != nil {
		return nil, false, errors.New(errors.ErrConfigInvalid, "encode session routing override failed: "+encodeErr.Error())
	}
	session.SetContext(agentconfig.SessionRoutingOverrideContextKey, encoded)
	return merged, false, nil
}

// validateResolvedSessionRouting 在落盘前跑 §3.5 阶梯解析：回退后仍非法才拒绝（U-3）。
func (h *Handler) validateResolvedSessionRouting(override *agentconfig.AICLISessionRoutingOverride, workspacePrefs *agentconfig.AICLIWorkspaceRoutingPreferences) error {
	resolution := agentconfig.ResolveMainAgentRouting(h.aicliConfigSnapshot(), override, workspacePrefs, nil)
	if main := resolution.Effective; main != nil {
		if _, err := agentconfig.ValidateMainAgentRoutingConfig(main); err != nil {
			return errors.New(errors.ErrValidationFailed, "invalid main agent routing: "+err.Error())
		}
	}
	if sub := resolution.EffectiveSub; sub != nil {
		if _, err := agentconfig.ValidateSubagentRoutingConfig("aicli.subagents.routing", sub); err != nil {
			return errors.New(errors.ErrValidationFailed, "invalid sub agent routing: "+err.Error())
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// workspace 层（§5.4/N9）

func (h *Handler) validateWorkspaceRoutingWrite(workspacePath string, patch *agentconfig.SessionRoutingPatch) error {
	if patch == nil {
		return nil
	}
	existing, _ := agentconfig.LoadWorkspaceRoutingPreferencesForPath(workspacePath)
	var baseMain *agentconfig.AICLIMainAgentRoutingConfig
	var baseSub *agentconfig.AICLISubagentRoutingConfig
	if existing != nil {
		baseMain = existing.MainAgent
		baseSub = existing.SubAgent
	}
	if patch.MainAgent != nil {
		candidate := agentconfig.MaterializeMainAgentRoutingConfig(baseMain, patch.MainAgent)
		if _, err := agentconfig.ValidateMainAgentRoutingConfig(candidate); err != nil {
			return errors.New(errors.ErrValidationFailed, "invalid main agent routing: "+err.Error())
		}
	}
	if patch.SubAgent != nil {
		candidate := agentconfig.MaterializeSubagentRoutingConfig(baseSub, patch.SubAgent)
		if _, err := agentconfig.ValidateSubagentRoutingConfig("aicli.subagents.routing", candidate); err != nil {
			return errors.New(errors.ErrValidationFailed, "invalid sub agent routing: "+err.Error())
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// config 层（§5.4：按层路由 + 写前校验，失败不落盘）

// writeConfigRoutingSection 写入 config 层的 routing 节（§5.4：写前校验，失败不落盘）。
//
// 语义：
//   - 补丁**未触碰**的节保持文件原值不动（2026-09-22 修正：此前「只改 sub_agent」
//     或「只清一个字段」的补丁会连带抹掉 `aicli.main_agent.routing`）；
//   - clear_fields 在 config 层按 §3.5.1 键路径做减法，清空到无覆盖时整节删除，
//     解析回到下一层（§3.5.2 阶梯回退）；
//   - 落盘成功后同步刷新进程内快照：投影（GET/PATCH 回显）与主 Agent 接线都读快照，
//     不刷新会一直显示旧配置、且配置要等重启才生效。
func (h *Handler) writeConfigRoutingSection(configPath string, patch *agentconfig.SessionRoutingPatch, clearRequested bool) error {
	cfg := h.aicliConfigSnapshot()
	if clearRequested {
		if err := agentconfig.UpdateAICLIRoutingSection(configPath, nil, true, nil, true); err != nil {
			return errors.New(errors.ErrConfigInvalid, "clear routing config failed: "+err.Error())
		}
		h.syncAICLIRoutingSnapshot(nil, true, nil, true)
		return nil
	}
	if patch == nil {
		return nil
	}

	mainSet := patch.MainAgent != nil || len(patch.ClearFields) > 0
	var mainConfig *agentconfig.AICLIMainAgentRoutingConfig
	if mainSet {
		// Materialize 先深拷贝 base（INV-A5：绝不原地改写快照），再按非 nil 字段覆盖。
		base := agentconfig.EffectiveMainAgentRoutingConfig(cfg)
		mainConfig = agentconfig.MaterializeMainAgentRoutingConfig(base, patch.MainAgent)
		agentconfig.ClearMainAgentRoutingConfigFields(mainConfig, patch.ClearFields)
		if agentconfig.IsEmptyMainAgentRoutingConfig(mainConfig) {
			mainConfig = nil // 字段级清除后已无覆盖：删除该节。
		} else if _, err := agentconfig.ValidateMainAgentRoutingConfig(mainConfig); err != nil {
			return errors.New(errors.ErrValidationFailed, "invalid main agent routing: "+err.Error())
		}
	}

	subSet := patch.SubAgent != nil
	var subConfig *agentconfig.AICLISubagentRoutingConfig
	if subSet {
		base := configSubagentRouting(cfg)
		subConfig = agentconfig.MaterializeSubagentRoutingConfig(base, patch.SubAgent)
		if _, err := agentconfig.ValidateSubagentRoutingConfig("aicli.subagents.routing", subConfig); err != nil {
			return errors.New(errors.ErrValidationFailed, "invalid sub agent routing: "+err.Error())
		}
	}

	if err := agentconfig.UpdateAICLIRoutingSection(configPath, mainConfig, mainSet, subConfig, subSet); err != nil {
		return errors.New(errors.ErrConfigInvalid, "write routing config failed: "+err.Error())
	}
	h.syncAICLIRoutingSnapshot(mainConfig, mainSet, subConfig, subSet)
	return nil
}

// syncAICLIRoutingSnapshot 把刚落盘的 config 路由节同步进 handler 的进程内快照。
//
// 与 UpdateAICLIRoutingSection 写入文件的值同源（同一 base + 同一补丁），因此快照
// 与文件不会漂移。不刷新的话，GET/PATCH 投影会回显旧配置，主 Agent 接线
// （mainAgentRoutingConfig → buildSessionActor）也要等进程重启才生效。
func (h *Handler) syncAICLIRoutingSnapshot(main *agentconfig.AICLIMainAgentRoutingConfig, mainSet bool, sub *agentconfig.AICLISubagentRoutingConfig, subSet bool) {
	if h == nil || (!mainSet && !subSet) {
		return
	}
	next := cloneAICLIRoutingConfig(h.aicliConfigSnapshot())
	if next == nil {
		// 快照未接线（nil）：保持现状，不凭空造一份只含 routing 的配置。
		return
	}
	if next.AICLI == nil {
		next.AICLI = &agentconfig.AICLIConfig{}
	}
	if mainSet {
		if main == nil {
			next.AICLI.MainAgent = nil
		} else {
			if next.AICLI.MainAgent == nil {
				next.AICLI.MainAgent = &agentconfig.AICLIMainAgentConfig{}
			}
			next.AICLI.MainAgent.Routing = cloneMainAgentRoutingConfigForHandler(main)
		}
	}
	if subSet {
		if next.AICLI.Subagents == nil {
			next.AICLI.Subagents = &agentconfig.AICLISubagentsConfig{}
		}
		next.AICLI.Subagents.Routing = cloneAgentRoutingConfig(sub)
	}
	h.SetAICLIConfig(next)
}

// ---------------------------------------------------------------------------
// 失效与事件

// invalidateSessionRoutingActor 失效会话 actor（§4.5）。
//
// 返回 true 表示「已无陈旧 actor」：hub 未持有 actor（冷会话）或成功停止。
// 停止超时/失败时返回 false，但**不回滚**已落盘的写入（§4.4）。
// hub 未接线（nil）时同样返回 false：这表示「无法证明已无陈旧 actor」，
// 与上面「hub 已确认无 actor」不是一回事。
func (h *Handler) invalidateSessionRoutingActor(sessionID string) bool {
	hub := h.getSessionHub()
	if hub == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionRoutingActorStopTimeout)
	defer cancel()
	return hub.StopContext(ctx, sessionID) == nil
}

// publishSessionRoutingChanged 发布 §7.1 事件（失效信号 + 摘要，权威仍是 GET /routing）。
func (h *Handler) publishSessionRoutingChanged(sessionID string, response sessionRoutingResponse) {
	if h == nil {
		return
	}
	payload := map[string]interface{}{
		"session_id":     strings.TrimSpace(sessionID),
		"routing":        response.Routing,
		"sub_agent":      response.SubAgent,
		"effective_from": agentconfig.RoutingEffectiveFromNextTurn,
		"revision":       response.Routing.Revision,
		"target_layer":   response.TargetLayer,
	}
	if strings.TrimSpace(response.TargetPath) != "" {
		payload["target_path"] = response.TargetPath
	}
	h.publishSessionRuntimeEvent(runtimeevents.EventSessionRoutingChanged, "", sessionID, payload)
}

// ---------------------------------------------------------------------------
// 投影与元数据

func (h *Handler) buildSessionRoutingResponse(session *runtimechat.Session, targetLayer string, updated bool, actorInvalidated bool) sessionRoutingResponse {
	workspacePath := sessionRoutingWorkspacePath(session)
	workspacePrefs, workspacePrefsErr := agentconfig.LoadWorkspaceRoutingPreferencesForPath(workspacePath)
	override, _ := agentconfig.DecodeSessionRoutingOverride(sessionRoutingOverrideRaw(session))
	cfg := h.aicliConfigSnapshot()

	resolution := agentconfig.ResolveMainAgentRouting(cfg, override, workspacePrefs, nil)
	revision := ""
	if override != nil {
		revision = agentconfig.FormatRoutingRevision(override.UpdatedAt, override.UpdatedBy)
	}
	main := agentconfig.ProjectRoutingStatus(resolution, "", revision)
	sub := agentconfig.ProjectSubAgentRoutingStatus(resolution, "", revision)

	childSession := sessionIsChildAgent(session)
	configPath, configLayer := agentconfig.AICLIConfigWriteTargetForRouting()
	writableLayers := []string{sessionRoutingLayerSession, sessionRoutingLayerWorkspace, sessionRoutingLayerConfig}
	if childSession {
		writableLayers = []string{}
	}
	panel := agentconfig.RoutingPanelMetadata{
		Scope:              "main",
		ChildSession:       childSession,
		SessionOverride:    override != nil,
		WorkspaceOverride:  workspaceRoutingPresent(workspacePrefs),
		ConfigOverride:     routingSourcePresent(resolution.Sources, agentconfig.RoutingSourceConfig),
		WorkspacePath:      workspacePath,
		WorkspacePrefsPath: agentconfig.WorkspaceRoutingTargetPath(workspacePath),
		ConfigPath:         configPath,
		ConfigLayer:        configLayer,
		WritableLayers:     writableLayers,
		Levels:             agentconfig.BuildRoutingLevelSummaries(resolution, "main"),
		SubAgent:           &sub,
	}

	warnings := main.Warnings
	if workspacePrefsErr != nil {
		// 读取失败不静默：投影会少掉工作区覆盖（source 归属随之变化），
		// 至少让 warnings 说出来，而不是伪造一份「一致」的投影（I-6）。
		warnings = append(append([]string(nil), warnings...),
			"工作区 routing 偏好读取失败，投影不含工作区覆盖: "+workspacePrefsErr.Error())
	}

	return sessionRoutingResponse{
		SessionID:        sessionIDOf(session),
		Scope:            "main",
		TargetLayer:      targetLayer,
		ActorInvalidated: actorInvalidated,
		Updated:          updated,
		Routing:          main,
		SubAgent:         sub,
		Panel:            panel,
		Warnings:         warnings,
	}
}

func sessionRoutingOverrideRaw(session *runtimechat.Session) string {
	if session == nil {
		return ""
	}
	return sessionmeta.String(session.Metadata.Context, agentconfig.SessionRoutingOverrideContextKey)
}

// sessionRoutingWorkspacePath 以会话绑定 workspace 路径为准（N9），worktree 优先。
func sessionRoutingWorkspacePath(session *runtimechat.Session) string {
	if session == nil {
		return ""
	}
	if worktreePath := sessionmeta.String(session.Metadata.Context, toolbroker.AgentSessionContextWorktreePath); worktreePath != "" {
		return worktreePath
	}
	return sessionmeta.String(session.Metadata.Context, sessionmeta.WorkspacePath)
}

// sessionIsChildAgent 与 actor 构建期的子会话判据同口径（§6.3 配置隔离）。
func sessionIsChildAgent(session *runtimechat.Session) bool {
	if session == nil {
		return false
	}
	if agentType := sessionmeta.String(session.Metadata.Context, toolbroker.AgentSessionContextAgentType); strings.TrimSpace(agentType) != "" {
		return true
	}
	if readOnly, ok := sessionmeta.Bool(session.Metadata.Context, toolbroker.AgentSessionContextReadOnly); ok && readOnly {
		return true
	}
	if depth, ok := sessionmeta.Int(session.Metadata.Context, toolbroker.AgentSessionContextDepth); ok && depth > 0 {
		return true
	}
	return false
}

func workspaceRoutingPresent(prefs *agentconfig.AICLIWorkspaceRoutingPreferences) bool {
	if prefs == nil {
		return false
	}
	return prefs.MainAgent != nil || prefs.SubAgent != nil
}

func routingSourcePresent(sources map[string]agentconfig.RoutingSource, source agentconfig.RoutingSource) bool {
	for _, value := range sources {
		if value == source {
			return true
		}
	}
	return false
}

func patchUpdatedBy(patch *agentconfig.SessionRoutingPatch) string {
	if patch == nil {
		return ""
	}
	return strings.TrimSpace(patch.UpdatedBy)
}

// configSubagentRouting 取配置层的子 Agent 路由节（无配置返回 nil）。
func configSubagentRouting(cfg *agentconfig.Config) *agentconfig.AICLISubagentRoutingConfig {
	if cfg == nil || cfg.AICLI == nil || cfg.AICLI.Subagents == nil {
		return nil
	}
	return cfg.AICLI.Subagents.Routing
}

// authorizeSessionRoutingWrite 与既有会话写端点同级（M4：回环 / admin token / admin role）。
func (h *Handler) authorizeSessionRoutingWrite(r *http.Request) error {
	if h.hasValidSearchAdminToken(r) || h.hasTrustedAdminRole(r) || isLoopbackRequest(r) {
		return nil
	}
	return errors.New(errors.ErrAgentPermission, "session routing endpoints require loopback access, valid admin token, or admin role")
}
