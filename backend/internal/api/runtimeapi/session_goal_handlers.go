// session_goal_handlers.go — 会话 goal 管理 HTTP 面（gap 2）。
//
// 与 CLI `/goal` 命令一一对应（set / pause / resume / complete / clear / budget），
// 让 web 控制台在不依赖 CLI 的前提下管理持久目标：
//
//	GET  /sessions/{id}/goal   读取当前 goal（含剩余预算）
//	POST /sessions/{id}/goal   {"action":"set|pause|resume|complete|clear|budget",
//	                            "objective":"...", "token_budget":N, "summary":"..."}
//
// 写路径复用 internal/goal 的校验、状态机与 GoalMergePolicy（actor=user），因此
// 与 CLI 完全同口径：complete 是终态（除非换新 goal）、budget_limited→active 仅在
// 预算提高到超过已用时成立、模型不可完成 paused/blocked 目标（用户可）。
//
// 边界说明：本端点只管理**状态**，不触发续跑。goal 的自动续跑循环当前由 CLI
// 驱动（aicliGoalContinuationExecutor），纯 web 会话不会自行续跑；控制台可在
// resume 后按需投递一条消息。模型侧工具面与 run 级指引注入见
// session_goal_tools.go / session_goal_guidance.go。
package runtimeapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

// goal 管理动作（与 CLI `/goal` 子命令同名）。
const (
	sessionGoalActionSet      = "set"
	sessionGoalActionPause    = "pause"
	sessionGoalActionResume   = "resume"
	sessionGoalActionComplete = "complete"
	sessionGoalActionClear    = "clear"
	sessionGoalActionBudget   = "budget"
)

type sessionGoalRequest struct {
	Action string `json:"action"`
	// Objective 仅 action=set 使用。
	Objective string `json:"objective"`
	// TokenBudget 仅 action=set|budget 使用（set 缺省时取 goal.defaultTokenBudget；
	// budget 必须显式给出，0 = 清除预算）。
	TokenBudget *int `json:"token_budget,omitempty"`
	// Summary 仅 action=complete 使用（完成证据摘要）。
	Summary string `json:"summary,omitempty"`
}

type sessionGoalResponse struct {
	SessionID       string                   `json:"session_id"`
	Goal            *runtimegoal.SessionGoal `json:"goal"`
	RemainingTokens interface{}              `json:"remaining_tokens"`
}

type sessionGoalMutationResponse struct {
	SessionID string                   `json:"session_id"`
	Action    string                   `json:"action"`
	Applied   bool                     `json:"applied"`
	Message   string                   `json:"message,omitempty"`
	Goal      *runtimegoal.SessionGoal `json:"goal"`
	// RemainingTokens 为 nil 表示无预算（与 get_goal 工具同形）。
	RemainingTokens interface{} `json:"remaining_tokens"`
}

// GetSessionGoal 返回会话当前 goal；无 goal 时为 null（200，不是 404）。
func (h *Handler) GetSessionGoal(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session manager not configured"))
		return
	}
	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}
	ctx, cancel := sessionStoreQueryContext(r)
	defer cancel()
	session, err := h.sessionManager.GetSession(ctx, sessionID)
	if err != nil {
		writeSessionStoreError(w, err)
		return
	}
	goal, ok, err := runtimegoal.NewMetadataStore().Get(session)
	if err != nil {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	resp := sessionGoalResponse{SessionID: sessionID}
	if ok && goal != nil {
		resp.Goal = goal
		resp.RemainingTokens = runtimegoal.RemainingTokens(*goal)
	}
	h.writeJSON(w, http.StatusOK, resp)
}

// UpdateSessionGoal 执行一次 goal 管理动作（见文件头注释）。
func (h *Handler) UpdateSessionGoal(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session manager not configured"))
		return
	}
	// 与既有会话写端点同级（回环 / admin token / admin role）。
	if err := h.authorizeSessionRoutingWrite(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	storage := h.sessionManager.GetStorage()
	if storage == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session storage not configured"))
		return
	}
	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}

	var req sessionGoalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"action is required (set|pause|resume|complete|clear|budget)"))
		return
	}

	ctx, cancel := sessionStoreQueryContext(r)
	defer cancel()
	session, err := h.sessionManager.GetSession(ctx, sessionID)
	if err != nil {
		writeSessionStoreError(w, err)
		return
	}

	store := runtimegoal.NewMetadataStore()
	current, hasGoal, err := store.Get(session)
	if err != nil {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	now := time.Now()

	// clear 不需要现成 goal；其余动作都需要。
	if action == sessionGoalActionClear {
		if hasGoal {
			if _, err := store.ClearPersistent(ctx, storage, sessionID); err != nil {
				writeSessionStoreError(w, err)
				return
			}
		}
		h.writeJSON(w, http.StatusOK, sessionGoalMutationResponse{
			SessionID: sessionID, Action: action, Applied: true, Message: "goal 已清除",
		})
		return
	}

	expected, message, errStatus, err := h.buildSessionGoalMutation(action, sessionID, current, hasGoal, req, now)
	if err != nil {
		h.writeError(w, errStatus, err)
		return
	}

	updated, err := store.PutPersistent(ctx, storage, sessionID, *expected, runtimegoal.MutationUser)
	if err != nil {
		writeSessionStoreError(w, err)
		return
	}
	persisted, applied := sessionGoalPersistedState(store, updated, *expected)

	resp := sessionGoalMutationResponse{
		SessionID: sessionID,
		Action:    action,
		Applied:   applied,
		Message:   message,
	}
	if persisted != nil {
		resp.Goal = persisted
		resp.RemainingTokens = runtimegoal.RemainingTokens(*persisted)
	}
	if !applied {
		// 合并策略保留了原状态（例如 complete 终态、budget_limited 未提高预算）。
		resp.Message = "未应用：" + sessionGoalNotAppliedReason(*expected, persisted)
	}
	h.writeJSON(w, http.StatusOK, resp)
}

// buildSessionGoalMutation 计算目标 goal（尚未持久化），并给出成功文案。
//
// 校验与状态机完全复用 internal/goal：CLI 与 HTTP 两条路径不各自实现一套规则。
func (h *Handler) buildSessionGoalMutation(action, sessionID string, current *runtimegoal.SessionGoal, hasGoal bool, req sessionGoalRequest, now time.Time) (*runtimegoal.SessionGoal, string, int, error) {
	switch action {
	case sessionGoalActionSet:
		objective := strings.TrimSpace(req.Objective)
		if objective == "" {
			return nil, "", http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "objective is required for action=set")
		}
		goal, err := runtimegoal.NewSessionGoal(sessionID, objective, now)
		if err != nil {
			return nil, "", http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error())
		}
		budget := h.sessionGoalDefaultTokenBudget()
		if req.TokenBudget != nil {
			budget = *req.TokenBudget
		}
		if budget < 0 {
			return nil, "", http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "token_budget must be >= 0 (0 = no budget)")
		}
		goal.TokenBudget = budget
		return &goal, "goal 已设置", 0, nil
	case sessionGoalActionPause, sessionGoalActionResume, sessionGoalActionComplete:
		if !hasGoal || current == nil {
			return nil, "", http.StatusNotFound, errors.New(errors.ErrValidationFailed, "当前会话未设置 goal")
		}
		target := runtimegoal.StatusPaused
		message := "Goal 已暂停"
		switch action {
		case sessionGoalActionResume:
			target = runtimegoal.StatusActive
			message = "Goal 已恢复"
		case sessionGoalActionComplete:
			target = runtimegoal.StatusComplete
			message = "Goal 已标记完成"
		}
		if err := runtimegoal.ValidateStatusTransition(current.Status, target); err != nil {
			return nil, "", http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error())
		}
		updated := *current
		runtimegoal.ApplyUserStatusUpdate(&updated, target, "user", req.Summary, now)
		return &updated, message, 0, nil
	case sessionGoalActionBudget:
		if !hasGoal || current == nil {
			return nil, "", http.StatusNotFound, errors.New(errors.ErrValidationFailed, "当前会话未设置 goal")
		}
		if req.TokenBudget == nil {
			return nil, "", http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "token_budget is required for action=budget")
		}
		budget := *req.TokenBudget
		if budget < 0 {
			return nil, "", http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "token_budget must be >= 0 (0 = clear budget)")
		}
		updated := *current
		updated.TokenBudget = budget
		updated.UpdatedAt = now
		message := "Token 预算已设置"
		if budget == 0 {
			message = "Token 预算已清除"
		}
		// 与 CLI `/goal budget` 同口径：预算提高到超过已用时自动恢复为 active
		//（GoalMergePolicy 仅在 new.TokenBudget > new.TokensUsed 时放行该转换）。
		if updated.Status == runtimegoal.StatusBudgetLimited && budget > updated.TokensUsed {
			updated.Status = runtimegoal.StatusActive
			message += "（预算已提高，goal 恢复 active）"
		}
		return &updated, message, 0, nil
	default:
		return nil, "", http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"unknown action "+action+" (set|pause|resume|complete|clear|budget)")
	}
}

// sessionGoalPersistedState 读取写入后的真实 goal 并判断请求是否生效
// （GoalMergePolicy 可能按守卫保留旧状态）。
func sessionGoalPersistedState(store runtimegoal.MetadataStore, session *chat.Session, expected runtimegoal.SessionGoal) (*runtimegoal.SessionGoal, bool) {
	if session == nil {
		return nil, false
	}
	persisted, ok, err := store.Get(session)
	if err != nil || !ok || persisted == nil {
		return nil, false
	}
	applied := persisted.GoalID == expected.GoalID &&
		persisted.Status == expected.Status &&
		persisted.TokenBudget == expected.TokenBudget
	return persisted, applied
}

// sessionGoalNotAppliedReason 给出「未生效」的可读原因（合并策略守卫）。
func sessionGoalNotAppliedReason(expected runtimegoal.SessionGoal, persisted *runtimegoal.SessionGoal) string {
	if persisted == nil {
		return "当前会话未设置 goal"
	}
	if persisted.GoalID != expected.GoalID {
		return "已完成的 goal 不能被覆盖；如需新目标请 action=set"
	}
	if persisted.Status != expected.Status {
		if persisted.Status == runtimegoal.StatusBudgetLimited && expected.Status == runtimegoal.StatusActive {
			return "预算未提高到超过已用，budget_limited 保持"
		}
		return "状态保持 " + string(persisted.Status)
	}
	if persisted.TokenBudget == 0 {
		return "预算保持 0（无预算）"
	}
	return "预算保持 " + strconv.Itoa(persisted.TokenBudget)
}

// sessionGoalDefaultTokenBudget 返回服务端配置的 goal.defaultTokenBudget
// （新建 goal 的缺省预算；0 = 无预算），与 CLI `/goal set` 同口径。
func (h *Handler) sessionGoalDefaultTokenBudget() int {
	if h == nil {
		return 0
	}
	cfg := h.resolveRuntimeConfig(UsageScope{})
	if cfg == nil {
		return 0
	}
	if cfg.Goal.DefaultTokenBudget > 0 {
		return cfg.Goal.DefaultTokenBudget
	}
	return 0
}
