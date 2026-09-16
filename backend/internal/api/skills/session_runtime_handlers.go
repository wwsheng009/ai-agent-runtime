package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	sessionRuntimeSubmitSyncWaitBudget = 100 * time.Millisecond
	sessionRuntimeSubmitPollInterval   = 10 * time.Millisecond
	// 尾部优先窗口（tail-first 回放）的默认/上限页大小：首屏只回放最近一页，
	// 旧内容由用户上滚按需逐页向前取。
	sessionRuntimeEventWindowDefault = 200
	sessionRuntimeEventWindowMax     = 1000
)

type sessionRuntimeSubmitOutcome struct {
	result *agent.Result
	err    error
}

type sessionRuntimeCommandRequest struct {
	Type                 string                 `json:"type"`
	Prompt               string                 `json:"prompt,omitempty"`
	ContinuationMetadata map[string]interface{} `json:"continuation_metadata,omitempty"`
	StripMetadataKeys    []string               `json:"strip_metadata_keys,omitempty"`
	RunMeta              *team.RunMeta          `json:"run_meta,omitempty"`
	RequestID            string                 `json:"request_id,omitempty"`
	Allow                *bool                  `json:"allow,omitempty"`
	PatchedArgs          json.RawMessage        `json:"patched_args,omitempty"`
	// TurnID 可选：interrupt 命令据此做回合身份校验 —— 只有与当前在途回合
	// 一致才取消（建议 3 契约）。空值表示「取消当前在途回合」，不做身份约束。
	TurnID string `json:"turn_id,omitempty"`
	QuestionID           string                 `json:"question_id,omitempty"`
	Answer               string                 `json:"answer,omitempty"`
	CheckpointID         string                 `json:"checkpoint_id,omitempty"`
	Mode                 string                 `json:"mode,omitempty"`
}

func (h *Handler) SpawnSessionAgent(w http.ResponseWriter, r *http.Request) {
	controller := h.getAgentSessionController()
	if controller == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "agent session controller not configured"))
		return
	}
	parentSessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if parentSessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}
	var req toolbroker.SpawnAgentArgs
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	result, err := controller.Spawn(r.Context(), parentSessionID, req)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	statusCode := http.StatusCreated
	if result != nil && result.Queued {
		statusCode = http.StatusAccepted
	}
	h.writeJSON(w, statusCode, map[string]interface{}{
		"agent": result,
	})
}

func (h *Handler) GetSessionAgentStatus(w http.ResponseWriter, r *http.Request) {
	controller := h.getAgentSessionController()
	if controller == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "agent session controller not configured"))
		return
	}
	agentID := strings.TrimSpace(mux.Vars(r)["agent_id"])
	if agentID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "agent id is required"))
		return
	}
	result, err := controller.snapshot(r.Context(), agentID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"agent": result,
	})
}

func (h *Handler) SendSessionAgentInput(w http.ResponseWriter, r *http.Request) {
	controller := h.getAgentSessionController()
	if controller == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "agent session controller not configured"))
		return
	}
	agentID := strings.TrimSpace(mux.Vars(r)["agent_id"])
	if agentID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "agent id is required"))
		return
	}
	var req toolbroker.SendAgentInputArgs
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	req.ID = firstNonEmptyString(req.ID, agentID)
	result, err := controller.SendInput(r.Context(), req)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	h.writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"agent": result,
	})
}

func (h *Handler) WaitSessionAgents(w http.ResponseWriter, r *http.Request) {
	controller := h.getAgentSessionController()
	if controller == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "agent session controller not configured"))
		return
	}
	parentSessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if parentSessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}
	var req toolbroker.WaitAgentArgs
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	if sessionAgentWaitHasNoTarget(req) {
		req.SessionID = parentSessionID
		req.MailboxOnly = true
	}
	result, err := controller.Wait(r.Context(), req)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"result": result,
	})
}

func sessionAgentWaitHasNoTarget(req toolbroker.WaitAgentArgs) bool {
	return strings.TrimSpace(req.ID) == "" &&
		strings.TrimSpace(req.SessionID) == "" &&
		len(req.IDs) == 0 &&
		len(req.SessionIDs) == 0
}

func (h *Handler) ListSessionAgentEvents(w http.ResponseWriter, r *http.Request) {
	controller := h.getAgentSessionController()
	if controller == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "agent session controller not configured"))
		return
	}
	parentSessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if parentSessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}
	agentID := strings.TrimSpace(mux.Vars(r)["agent_id"])
	after := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("after_seq")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid after_seq value"))
			return
		}
		after = parsed
	}
	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	waitMs := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("wait_ms")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid wait_ms value"))
			return
		}
		waitMs = parsed
	}
	result, err := controller.ReadEvents(r.Context(), toolbroker.ReadAgentEventsArgs{
		ID:          agentID,
		SessionID:   parentSessionID,
		AfterSeq:    after,
		Limit:       limit,
		WaitMs:      waitMs,
		MailboxOnly: agentID == "",
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"result": result,
	})
}

// ListSessionAgentControlMailbox lists durable AgentControl mailbox rows for a
// session without converting them through legacy runtime event shape.
func (h *Handler) ListSessionAgentControlMailbox(w http.ResponseWriter, r *http.Request) {
	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}
	store := h.getSessionEventStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session event store not configured"))
		return
	}
	reader, ok := store.(chat.AgentControlMailboxReaderStore)
	if !ok || reader == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "agent control mailbox reader not configured"))
		return
	}
	after := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("after_seq")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid after_seq value"))
			return
		}
		after = parsed
	}
	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	if limit <= 0 {
		limit = 20
	}
	waitMs := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("wait_ms")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid wait_ms value"))
			return
		}
		waitMs = parsed
	}

	// 共享数据库（session_history.sqlite）可能被并发 aicli CLI 进程持写锁，
	// mailbox 存储打开/读取会阻塞在 sqlite busy_timeout。waitMs==0（前端
	// fetchRuntimeJson，10s AbortSignal.timeout）时必须带截止时间快速失败，
	// 否则前端显示 "signal timed out"。waitMs>0（SSE 长轮询）保留原语义，
	// 由 AbortController 控制生命周期。
	ctx := r.Context()
	cancel := func() {}
	if waitMs > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(waitMs)*time.Millisecond)
	} else {
		ctx, cancel = sessionStoreQueryContext(r)
	}
	defer cancel()

	var mailboxWake <-chan team.MailMessage
	unwatch := func() {}
	if watcher, ok := store.(chat.AgentControlMailboxWatcherStore); ok && watcher != nil {
		mailboxWake, unwatch = watcher.WatchAgentControlMailbox(ctx, sessionID)
	}
	defer unwatch()

	for {
		messages, err := reader.ListAgentControlMailbox(ctx, sessionID, after, limit)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		latestSeq := int64(0)
		if sequencer, ok := store.(chat.AgentControlMailboxSequenceStore); ok && sequencer != nil {
			seq, err := sequencer.LastAgentControlMailboxSeq(ctx, sessionID)
			if err != nil {
				h.writeError(w, http.StatusInternalServerError, err)
				return
			}
			latestSeq = seq
		} else if len(messages) > 0 {
			latestSeq = messages[len(messages)-1].Seq
		}
		if len(messages) > 0 || waitMs == 0 {
			h.writeJSON(w, http.StatusOK, map[string]interface{}{
				"result": map[string]interface{}{
					"session_id":   sessionID,
					"messages":     messages,
					"count":        len(messages),
					"latest_seq":   latestSeq,
					"source":       "agent_control_mailbox",
					"after_seq":    after,
					"control_only": true,
				},
			})
			return
		}
		select {
		case <-ctx.Done():
			h.writeJSON(w, http.StatusOK, map[string]interface{}{
				"result": map[string]interface{}{
					"session_id":   sessionID,
					"messages":     []team.MailMessage{},
					"count":        0,
					"latest_seq":   latestSeq,
					"source":       "agent_control_mailbox",
					"after_seq":    after,
					"control_only": true,
					"timed_out":    true,
				},
			})
			return
		case <-mailboxWake:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (h *Handler) CloseSessionAgent(w http.ResponseWriter, r *http.Request) {
	controller := h.getAgentSessionController()
	if controller == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "agent session controller not configured"))
		return
	}
	agentID := strings.TrimSpace(mux.Vars(r)["agent_id"])
	if agentID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "agent id is required"))
		return
	}
	result, err := controller.Close(r.Context(), agentID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"agent": result,
	})
}

func (h *Handler) ResumeSessionAgent(w http.ResponseWriter, r *http.Request) {
	controller := h.getAgentSessionController()
	if controller == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "agent session controller not configured"))
		return
	}
	agentID := strings.TrimSpace(mux.Vars(r)["agent_id"])
	if agentID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "agent id is required"))
		return
	}
	result, err := controller.Resume(r.Context(), agentID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"agent": result,
	})
}

// GetSessionRuntimeState returns the session actor runtime state.
//
// 契约（P2-1A 快照消费方按此实现）：
//   - 200 {state, active_turn, ...execution_route} —— 该会话有 durable runtime state；
//   - 200 {session_id, state: null, active_turn}   —— 会话存在但从未进入 durable session
//     actor（例如只经无状态 `/api/agent/chat` 的 web 会话）。空快照是正常终态：
//     不生成状态行、也不伪造未决审批 / 提问；
//   - 404 SESSION_NOT_FOUND             —— 会话不存在（已删除 / 未知 id），
//     与会话读取端点（/turns、/backtrack/audit、/history）同一约定；
//   - 503 STORE_UNAVAILABLE             —— 会话存储不可用（锁定 / 超时）。
//
// `active_turn`（P4-刷新续传）：本进程此刻在该会话上执行的在途回合
// （{session_id, turn_id, source, detached, started_at}），无则为 null。
// 页面刷新后据此重新挂载在途回合身份，让 runtime/stream 上落库的增量帧
// 继续渲染到同一条 streaming 消息。
func (h *Handler) GetSessionRuntimeState(w http.ResponseWriter, r *http.Request) {
	store := h.getSessionRuntimeStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session runtime store not configured"))
		return
	}

	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}

	// runtime state store 可能后置在共享的 session_history.sqlite 上
	// （win7 配置下与 aicli 共享主库），读取带截止时间快速失败，
	// 避免前端 fetchRuntimeJson 10s 超时显示 "signal timed out"。
	queryCtx, queryCancel := sessionStoreQueryContext(r)
	defer queryCancel()
	state, err := store.LoadState(queryCtx, sessionID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if state == nil {
		// 「会话不存在」与「会话存在但从未进入 durable session actor」是两种
		// 不同终态，旧实现都用 404 表达：调用方无法区分，只能把 404 当作常规
		// 空态吞掉——于是每个只经无状态 /api/agent/chat 的 web 会话都会在浏览器
		// 控制台留下一条误导性的 404（shared.ts:247 的 performRuntimeJsonFetch）。
		//
		// 这里显式区分：会话存在（sessionManager 可解析，与 /sessions/{id}、
		// /sessions 列表同源）→ 200 + 显式空快照；只有真正不存在的会话才 404。
		if h.sessionManager == nil {
			// 没有会话存储可判定存在性时保持历史语义，不把未知当作空态。
			h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "session runtime state not found"))
			return
		}
		session, loadErr := h.sessionManager.GetSession(queryCtx, sessionID)
		if loadErr != nil {
			// 会话不存在 → 404 SESSION_NOT_FOUND；存储被占用 / 超时 → 503。
			writeSessionStoreError(w, loadErr)
			return
		}
		if session == nil {
			h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "session runtime state not found"))
			return
		}
		h.writeJSON(w, http.StatusOK, h.attachSessionExecutionRoute(r.Context(), sessionID, map[string]interface{}{
			"session_id":  sessionID,
			"state":       nil,
			"active_turn": h.activeTurnSnapshotPayload(sessionID),
		}))
		return
	}

	payload := map[string]interface{}{
		"state":       state,
		"active_turn": h.activeTurnSnapshotPayload(sessionID),
	}
	h.writeJSON(w, http.StatusOK, h.attachSessionExecutionRoute(r.Context(), sessionID, payload))
}

// activeTurnSnapshotPayload 返回 /runtime 快照里的 `active_turn` 字段：
// 无在途回合时显式 null（消费方按 `active_turn == null` 判定终态）。
func (h *Handler) activeTurnSnapshotPayload(sessionID string) interface{} {
	entry, ok := h.getActiveTurnRegistry().get(sessionID)
	if !ok {
		return nil
	}
	return entry
}

// ListSessionRuntimeTools returns the current runtime-server tool surface for a session.
func (h *Handler) ListSessionRuntimeTools(w http.ResponseWriter, r *http.Request) {
	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}

	tools := []runtimetypes.ToolDefinition{}
	if !h.sessionRuntimeToolsDisabled(r.Context(), sessionID) {
		tools = h.sessionRuntimeToolDefinitions(r.Context(), sessionID)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id": sessionID,
		"tools":      tools,
		"count":      len(tools),
		"source":     "runtime_server",
	})
}

func (h *Handler) sessionRuntimeToolDefinitions(ctx context.Context, sessionID string) []runtimetypes.ToolDefinition {
	if h == nil {
		return []runtimetypes.ToolDefinition{}
	}
	if store := h.getSessionRuntimeStore(); store != nil {
		if state, err := store.LoadState(ctx, strings.TrimSpace(sessionID)); err == nil && state != nil && state.StableToolSurfaceSet {
			return cloneSessionRuntimeToolDefinitions(state.StableToolSurface)
		}
	}
	surface := h.runtimeServerToolSurfaceForSession(ctx, sessionID, h.mcpManager, true)
	if surface == nil {
		return []runtimetypes.ToolDefinition{}
	}
	infos := surface.ListTools()
	tools := make([]runtimetypes.ToolDefinition, 0, len(infos))
	seen := make(map[string]bool, len(infos))
	for _, info := range infos {
		name := strings.TrimSpace(info.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		tools = append(tools, runtimetypes.ToolDefinition{
			Name:        name,
			Description: info.Description,
			Parameters:  cloneAnyMap(info.InputSchema),
			Metadata:    cloneAnyMap(info.Metadata),
		})
	}
	sort.SliceStable(tools, func(i, j int) bool {
		return strings.TrimSpace(tools[i].Name) < strings.TrimSpace(tools[j].Name)
	})
	return tools
}

func cloneSessionRuntimeToolDefinitions(input []runtimetypes.ToolDefinition) []runtimetypes.ToolDefinition {
	if len(input) == 0 {
		return []runtimetypes.ToolDefinition{}
	}
	output := make([]runtimetypes.ToolDefinition, 0, len(input))
	for _, tool := range input {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		output = append(output, runtimetypes.ToolDefinition{
			Name:        name,
			Description: tool.Description,
			Parameters:  cloneAnyMap(tool.Parameters),
			Metadata:    cloneAnyMap(tool.Metadata),
		})
	}
	sort.SliceStable(output, func(i, j int) bool {
		return strings.TrimSpace(output[i].Name) < strings.TrimSpace(output[j].Name)
	})
	return output
}

func (h *Handler) sessionRuntimeToolsDisabled(ctx context.Context, sessionID string) bool {
	if h == nil || h.sessionManager == nil {
		return false
	}
	// 共享 session_history.sqlite 可能被并发 aicli 进程锁住；查询超时时
	// 保守返回 false（工具不被禁用），避免拖垮主响应。
	queryCtx, cancel := context.WithTimeout(ctx, sessionStoreQueryTimeout)
	defer cancel()
	session, err := h.sessionManager.Get(queryCtx, strings.TrimSpace(sessionID))
	if err != nil || session == nil {
		return false
	}
	disabled, ok := sessionmeta.Bool(session.Metadata.Context, sessionmeta.DisableTools)
	return ok && disabled
}

// ListSessionRuntimeEvents returns session runtime events.
func (h *Handler) ListSessionRuntimeEvents(w http.ResponseWriter, r *http.Request) {
	store := h.getSessionEventStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session event store not configured"))
		return
	}

	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}

	after := int64(0)
	rawAfter := strings.TrimSpace(r.URL.Query().Get("after"))
	if rawAfter == "" {
		rawAfter = strings.TrimSpace(r.URL.Query().Get("after_seq"))
	}
	if rawAfter != "" {
		parsed, err := strconv.ParseInt(rawAfter, 10, 64)
		if err != nil || parsed < 0 {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid after value"))
			return
		}
		after = parsed
	}

	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	// 尾部优先窗口（tail-first 回放）：`tail=1` 取最新一页；`before_seq=N` 取
	// seq < N 的上一页（排他上界，与 /history 的游标语义一致）。默认（都不传）
	// 仍是原有的 after 升序续拉，既有调用方行为不变。
	tailWindow := false
	beforeSeq := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("before_seq")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid before_seq value"))
			return
		}
		beforeSeq = parsed
		tailWindow = true
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("tail")); raw != "" {
		switch strings.ToLower(raw) {
		case "1", "true", "yes":
			tailWindow = true
		case "0", "false", "no":
		default:
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid tail value"))
			return
		}
	}
	waitMs := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("wait_ms")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid wait_ms value"))
			return
		}
		waitMs = parsed
	}

	// 同 ListSessionAgentControlMailbox：waitMs==0 时带截止时间快速失败，
	// 避免 event store 打开/读取在共享数据库锁竞争时阻塞超过前端
	// AbortSignal.timeout(10s)，表现为 "signal timed out"。
	ctx := r.Context()
	cancel := func() {}
	if waitMs > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(waitMs)*time.Millisecond)
	} else {
		ctx, cancel = sessionStoreQueryContext(r)
	}
	defer cancel()

	var eventWake <-chan runtimeevents.Event
	unwatch := func() {}
	if watcher, ok := store.(chat.EventWatcherStore); ok && watcher != nil {
		eventWake, unwatch = watcher.WatchEvents(ctx, sessionID)
	}
	defer unwatch()

	// 窗口模式是一次性历史读取（无 live 等待语义）：首屏只取最近一页，用户上滚
	// 时用 next_before_seq 逐页向前。大会话（实测 2288 条 / 2.25MB）因此不必在
	// 打开时把整份事件日志重放一遍。
	if tailWindow {
		windowStore, ok := store.(chat.EventWindowStore)
		if !ok {
			h.writeError(w, http.StatusNotImplemented, errors.New(errors.ErrConfigInvalid, "session event store does not support windowed reads"))
			return
		}
		windowLimit := limit
		if windowLimit <= 0 {
			windowLimit = sessionRuntimeEventWindowDefault
		}
		if windowLimit > sessionRuntimeEventWindowMax {
			windowLimit = sessionRuntimeEventWindowMax
		}
		upper := beforeSeq
		if upper <= 0 {
			upper = math.MaxInt64
		}
		// 多取一条判断「还有更早一页」（与 /history 的 has_more 同语义）：多出的
		// 那条丢弃，省掉为一次布尔判断再发一次请求。
		events, err := windowStore.ListEventsBefore(ctx, sessionID, upper, windowLimit+1)
		if err != nil {
			writeSessionStoreError(w, err)
			return
		}
		hasMore := len(events) > windowLimit
		if hasMore {
			events = events[len(events)-windowLimit:]
		}
		latestSeq := int64(0)
		if sequenceStore, ok := store.(chat.EventSequenceStore); ok {
			seq, err := sequenceStore.LastEventSeq(ctx, sessionID)
			if err != nil {
				writeSessionStoreError(w, err)
				return
			}
			latestSeq = seq
		}
		firstSeq := int64(0)
		lastSeq := int64(0)
		if len(events) > 0 {
			firstSeq = agentEventSeq(events[0])
			lastSeq = agentEventSeq(events[len(events)-1])
		}
		for _, event := range events {
			if seq := agentEventSeq(event); seq > latestSeq {
				latestSeq = seq
			}
		}
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"events":          buildSessionRuntimeEventViews(events),
			"count":           len(events),
			"latest_seq":      latestSeq,
			"after_seq":       after,
			"first_seq":       firstSeq,
			"last_seq":        lastSeq,
			"has_more":        hasMore,
			"next_before_seq": firstSeq,
		})
		return
	}

	for {
		events, err := store.ListEvents(ctx, sessionID, after, limit)
		if err != nil {
			// 截止时间到期（共享数据库锁竞争）映射为 503，与其余
			// session store handler 语义一致，前端可明确提示重试。
			writeSessionStoreError(w, err)
			return
		}
		latestSeq := int64(0)
		if sequenceStore, ok := store.(chat.EventSequenceStore); ok {
			seq, err := sequenceStore.LastEventSeq(ctx, sessionID)
			if err != nil {
				writeSessionStoreError(w, err)
				return
			}
			latestSeq = seq
		}
		for _, event := range events {
			if seq := agentEventSeq(event); seq > latestSeq {
				latestSeq = seq
			}
		}

		if len(events) > 0 || waitMs == 0 {
			h.writeJSON(w, http.StatusOK, map[string]interface{}{
				"events":     buildSessionRuntimeEventViews(events),
				"count":      len(events),
				"latest_seq": latestSeq,
				"after_seq":  after,
			})
			return
		}

		select {
		case <-ctx.Done():
			h.writeJSON(w, http.StatusOK, map[string]interface{}{
				"events":     []map[string]interface{}{},
				"count":      0,
				"latest_seq": latestSeq,
				"after_seq":  after,
				"timed_out":  true,
			})
			return
		case <-eventWake:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// ListSessionToolReceipts returns persisted tool receipts for a session.
func (h *Handler) ListSessionToolReceipts(w http.ResponseWriter, r *http.Request) {
	store := h.getSessionToolReceiptStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session tool receipt store not configured"))
		return
	}

	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}

	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	toolCallID := strings.TrimSpace(r.URL.Query().Get("tool_call_id"))
	if toolCallID != "" {
		// tool receipt store 可能后置在共享库上；读取带截止时间快速失败，
		// 避免前端 fetchRuntimeJson 10s 超时显示 "signal timed out"。
		queryCtx, queryCancel := sessionStoreQueryContext(r)
		defer queryCancel()
		receipt, err := store.GetToolReceipt(queryCtx, sessionID, toolCallID)
		if err != nil {
			writeSessionStoreError(w, err)
			return
		}
		receipts := make([]chat.ToolExecutionReceipt, 0, 1)
		if receipt != nil {
			receipts = append(receipts, *receipt)
		}
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"receipts": receipts,
			"count":    len(receipts),
		})
		return
	}

	receipts, err := store.ListToolReceipts(r.Context(), sessionID, limit)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"receipts": receipts,
		"count":    len(receipts),
	})
}

// SubmitSessionRuntimeCommand submits a session actor command.
func (h *Handler) SubmitSessionRuntimeCommand(w http.ResponseWriter, r *http.Request) {
	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}

	var req sessionRuntimeCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}

	commandType := strings.ToLower(strings.TrimSpace(req.Type))
	if commandType == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "command type is required"))
		return
	}

	// 建议 3（后端 cancel 契约）：interrupt 先作用在「本进程在途回合」上。
	//
	// 为什么必须在 hub/actor 之前：
	//   - web 直连回合（POST /api/agent/chat，resume_on_disconnect 的 detached
	//     续传）根本不在 actor 里 —— 它只登记在 activeTurnRegistry，先查注册表才
	//     能取消到真回合；
	//   - GetOrCreate 会按需新建 durable actor（一次 stop 不该凭空建出 idle actor），
	//     且它可能因会话租约被别的宿主持有而报冲突 —— 那种情况下本进程的在途回合
	//     其实是可停的，不该因为拿不到 actor 就 503/409 拒掉停止请求。
	if commandType == "interrupt" {
		result := h.getActiveTurnRegistry().cancel(sessionID, strings.TrimSpace(req.TurnID), activeTurnCancelSourceUserInterrupt)
		switch result.Reason {
		case activeTurnCancelReasonTurnMismatch:
			// 迟到的 stop 打到新回合上是最危险的误伤：拒绝，并回带当前在途回合，
			// 让调用方自行决定是否重发（不要静默取消一个它没打算取消的回合）。
			h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed,
				fmt.Sprintf("turn_id does not match the active turn (%s)", result.Turn.TurnID)))
			return
		case activeTurnCancelReasonCancelled, activeTurnCancelReasonAlreadyCancelled:
			h.writeJSON(w, http.StatusOK, sessionTurnInterruptPayload(result, sessionTurnInterruptChannelActiveTurn))
			return
		}
		// no_active_turn / not_cancelable → 落回下面的 durable actor 路径。
	}

	hub := h.getSessionHub()
	if hub == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session hub not configured"))
		return
	}

	actor, err := hub.GetOrCreate(sessionID)
	if err != nil {
		if h.writeSessionLeaseConflict(w, err) {
			return
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	switch commandType {
	case "submit_prompt", "submit":
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "prompt is required"))
			return
		}
		prompt, err = h.injectSupervisionPreflight(r.Context(), sessionID, prompt, req.RunMeta)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		result, state, err, completed := submitSessionPrompt(actor, r.Context(), prompt, req.RunMeta)
		if err != nil {
			if h.writeSessionLeaseConflict(w, err) {
				return
			}
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !completed {
			payload := map[string]interface{}{
				"ok":      true,
				"pending": true,
				"state":   state,
			}
			h.writeJSON(w, http.StatusAccepted, h.attachSessionExecutionRoute(r.Context(), sessionID, payload))
			return
		}
		payload := map[string]interface{}{
			"result": result,
		}
		h.writeJSON(w, http.StatusOK, h.attachSessionExecutionRoute(r.Context(), sessionID, payload))
		return

	case "continue", "continue_session":
		continuationPrompt, err := h.injectSupervisionPreflight(r.Context(), sessionID, req.Prompt, req.RunMeta)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		result, state, err, completed := submitSessionContinue(actor, r.Context(), req.RunMeta, chat.ContinueOption{
			ContinuationPrompt:   continuationPrompt,
			ContinuationMetadata: cloneAnyMap(req.ContinuationMetadata),
			StripMetadataKeys:    append([]string(nil), req.StripMetadataKeys...),
		})
		if err != nil {
			if h.writeSessionLeaseConflict(w, err) {
				return
			}
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !completed {
			payload := map[string]interface{}{
				"ok":      true,
				"pending": true,
				"state":   state,
			}
			h.writeJSON(w, http.StatusAccepted, h.attachSessionExecutionRoute(r.Context(), sessionID, payload))
			return
		}
		payload := map[string]interface{}{
			"result": result,
		}
		h.writeJSON(w, http.StatusOK, h.attachSessionExecutionRoute(r.Context(), sessionID, payload))
		return

	case "approve_tool", "approve":
		requestID := strings.TrimSpace(req.RequestID)
		if requestID == "" {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "request_id is required"))
			return
		}
		if req.Allow == nil {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "allow is required"))
			return
		}
		if err := actor.ApproveToolWithArgs(r.Context(), requestID, *req.Allow, req.PatchedArgs); err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}

	case "answer_question", "answer":
		questionID := strings.TrimSpace(req.QuestionID)
		if questionID == "" {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "question_id is required"))
			return
		}
		if err := actor.AnswerQuestion(r.Context(), questionID, req.Answer); err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}

	case "interrupt":
		// durable actor 路径：注册表没有可取消的 web 回合时的回退（aicli chat
		// actor / 已建 actor 的会话）。cancelled 按取消前的运行态如实上报，
		// 不把「本来就空闲」谎报成「已停止」。
		cancelled := sessionActorRunActive(actor)
		if err := actor.Interrupt(r.Context()); err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		reason := activeTurnCancelReasonNoActiveTurn
		if cancelled {
			reason = activeTurnCancelReasonCancelled
		}
		h.writeJSON(w, http.StatusOK, sessionTurnInterruptPayload(
			activeTurnCancelResult{Cancelled: cancelled, Reason: reason},
			sessionTurnInterruptChannelSessionActor,
		))
		return

	case "rewind_to", "rewind":
		checkpointID := strings.TrimSpace(req.CheckpointID)
		if checkpointID == "" {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "checkpoint_id is required"))
			return
		}
		// Rewind rewrites session history; the run-scoped lease is taken
		// explicitly while the actor is idle (see buildSessionActor).
		leaseHandle, leaseErr := h.acquireSessionLease(r.Context(), sessionID, sessionActorLeaseOwnerKind, "command-rewind")
		if leaseErr != nil {
			if h.writeSessionLeaseConflict(w, leaseErr) {
				return
			}
			h.writeError(w, http.StatusInternalServerError, leaseErr)
			return
		}
		if leaseHandle != nil {
			defer func() {
				_ = leaseHandle.Release(context.Background())
			}()
		}
		result, err := actor.Rewind(r.Context(), checkpointID, strings.TrimSpace(req.Mode))
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":     true,
			"result": result,
		})
		return

	default:
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "unsupported command type"))
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true,
	})
}

// sessionTurnInterruptChannel* 标明 interrupt 命令实际作用在哪条执行通道上，
// 便于前端/日志区分「真的停住了进程内在途回合」与「回退到 durable actor」。
const (
	sessionTurnInterruptChannelActiveTurn   = "active_turn"
	sessionTurnInterruptChannelSessionActor = "session_actor"
)

// sessionTurnInterruptPayload 是 interrupt 命令的统一响应（建议 3 契约）。
//
// 字段语义（调用方据此决定下一步，而不是只看 HTTP 状态码）：
//   - cancelled：本次调用是否真的触发了取消。重复 stop → false +
//     reason=already_cancelled（同一回合已取消，仍属成功语义，不该报错）；
//   - reason：cancelled / already_cancelled / no_active_turn / not_cancelable；
//   - channel：取消作用在进程内在途回合还是 durable actor；
//   - turn / turn_id / cancel_source：被取消回合的身份与取消来源；
//     无在途回合时整组缺省，避免把「没有回合」编造成一个身份。
func sessionTurnInterruptPayload(result activeTurnCancelResult, channel string) map[string]interface{} {
	payload := map[string]interface{}{
		"ok":        true,
		"cancelled": result.Cancelled,
		"reason":    string(result.Reason),
		"channel":   channel,
	}
	turn := result.Turn
	if strings.TrimSpace(turn.TurnID) != "" {
		payload["turn_id"] = turn.TurnID
		payload["turn"] = turn
		if source := strings.TrimSpace(turn.CancelSource); source != "" {
			payload["cancel_source"] = source
		}
	}
	return payload
}

// sessionActorRunActive 判断 durable actor 此刻是否有在途运行。
//
// 等待审批/等待输入也算在途（两者都有活跃 run 与挂起的工具调用），Interrupt 会
// 把它们一并收敛成 SessionStopped —— 因此必须计入 cancelled，否则「停止」明明
// 停住了却报告 no_active_turn。
func sessionActorRunActive(actor *chat.SessionActor) bool {
	if actor == nil {
		return false
	}
	state := actor.StateForInspection()
	if state == nil {
		return false
	}
	switch state.Status {
	case chat.SessionRunning, chat.SessionWaitingApproval, chat.SessionWaitingInput:
		return true
	default:
		return false
	}
}

func submitSessionPrompt(actor *chat.SessionActor, requestCtx context.Context, prompt string, runMeta *team.RunMeta) (*agent.Result, *chat.RuntimeState, error, bool) {
	if actor == nil {
		return nil, nil, errors.New(errors.ErrConfigInvalid, "session actor not configured"), false
	}

	runCtx := context.WithoutCancel(requestCtx)
	resultCh := make(chan sessionRuntimeSubmitOutcome, 1)
	go func() {
		result, err := actor.SubmitPrompt(runCtx, prompt, runMeta)
		resultCh <- sessionRuntimeSubmitOutcome{result: result, err: err}
	}()

	timer := time.NewTimer(sessionRuntimeSubmitSyncWaitBudget)
	defer timer.Stop()
	ticker := time.NewTicker(sessionRuntimeSubmitPollInterval)
	defer ticker.Stop()

	for {
		select {
		case outcome := <-resultCh:
			return outcome.result, nil, outcome.err, true
		case <-ticker.C:
			state := actor.StateForInspection()
			if state == nil {
				continue
			}
			switch state.Status {
			case chat.SessionWaitingApproval, chat.SessionWaitingInput:
				return nil, state, nil, false
			}
		case <-timer.C:
			state := actor.StateForInspection()
			if state == nil {
				state = &chat.RuntimeState{
					Status: chat.SessionRunning,
				}
			} else if state.Status == chat.SessionIdle {
				state.Status = chat.SessionRunning
			}
			return nil, state, nil, false
		}
	}
}

func submitSessionContinue(actor *chat.SessionActor, requestCtx context.Context, runMeta *team.RunMeta, opts chat.ContinueOption) (*agent.Result, *chat.RuntimeState, error, bool) {
	if actor == nil {
		return nil, nil, errors.New(errors.ErrConfigInvalid, "session actor not configured"), false
	}

	runCtx := context.WithoutCancel(requestCtx)
	resultCh := make(chan sessionRuntimeSubmitOutcome, 1)
	go func() {
		result, err := actor.Continue(runCtx, runMeta, opts)
		resultCh <- sessionRuntimeSubmitOutcome{result: result, err: err}
	}()

	timer := time.NewTimer(sessionRuntimeSubmitSyncWaitBudget)
	defer timer.Stop()
	ticker := time.NewTicker(sessionRuntimeSubmitPollInterval)
	defer ticker.Stop()

	for {
		select {
		case outcome := <-resultCh:
			return outcome.result, nil, outcome.err, true
		case <-ticker.C:
			state := actor.StateForInspection()
			if state == nil {
				continue
			}
			switch state.Status {
			case chat.SessionWaitingApproval, chat.SessionWaitingInput:
				return nil, state, nil, false
			}
		case <-timer.C:
			state := actor.StateForInspection()
			if state == nil {
				state = &chat.RuntimeState{
					Status: chat.SessionRunning,
				}
			} else if state.Status == chat.SessionIdle {
				state.Status = chat.SessionRunning
			}
			return nil, state, nil, false
		}
	}
}
