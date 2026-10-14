package runtimeapi

// web inline 路径的交互通道控制器：ask_user_question / plan_enter / plan_exit。
// （docs/architecture/unified-tool-surface-registry.md §21——补齐三工具在
// runtime-server 上的执行承接。）
//
// 三工具的会话状态全部落在 durable 会话上下文（planmode.ContextKey /
// PendingQuestion / PendingTool），并同步到 runtime state——Web 面的
// `/runtime` 快照与 `answer_question` 命令因此成为统一的用户回答通道，
// 与 actor 路径（SessionActor.AskUserQuestion / handleAnswerQuestion）
// 的语义逐项对齐：
//
//   - ask_user_question：注册 pending_question → question_asked 事件 →
//     等 waiter；用户经 /runtime/commands `answer_question` 唤醒。请求
//     ctx 带 resume_on_disconnect 时以 WithoutCancel 承载等待（与回合
//     生命周期解耦）；否则随请求取消并清理挂起（与直连回合同生共死）。
//   - plan_enter/exit：planmode.Save 到 durable 会话上下文 + 会话落库；
//     permission_mode 落会话元数据（下一 turn 由 applyAgentExecutionPolicy
//     的会话状态读取生效；plan 收窄写权限的引擎语义由既有 plan 逻辑接管）。
//
// 与 SessionActor 同名实现的差异（如实登记）：actor 版通过 engine 直接
// 切换运行中 agent 的权限模式；inline 路径的 agent 每 turn 现建，权限在
// 构建 A 期从会话状态读取——因此这里只写会话状态，不触碰引擎。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// interactiveControllers 实现 toolbroker 的 UserInputHandler 与
// PlanModeController（web inline 路径）。handler 提供 durable 会话存取
// 与事件发布；waiters 是 ask_user_question 的跨请求回答通道。
type interactiveControllers struct {
	handler *Handler
	waiters *inlineQuestionWaiters
}

// inlineQuestionWaiters 是 handler 级的「问题 id → 应答通道」注册表：
// ask_user_question 挂起时注册，用户经 /runtime/commands `answer_question`
// 投递答案后由 AnswerInlineQuestion 唤醒。actor 的 questionWaiters 同语义。
type inlineQuestionWaiters struct {
	mu      map[string]chan string
	orderMu chan struct{}
}

func newInlineQuestionWaiters() *inlineQuestionWaiters {
	return &inlineQuestionWaiters{
		mu:      make(map[string]chan string),
		orderMu: make(chan struct{}, 1),
	}
}

func (w *inlineQuestionWaiters) lock()   { w.orderMu <- struct{}{} }
func (w *inlineQuestionWaiters) unlock() { <-w.orderMu }

func (w *inlineQuestionWaiters) register(id string) chan string {
	ch := make(chan string, 1)
	w.lock()
	w.mu[id] = ch
	w.unlock()
	return ch
}

func (w *inlineQuestionWaiters) resolve(id, answer string) bool {
	w.lock()
	ch, ok := w.mu[id]
	if ok {
		delete(w.mu, id)
	}
	w.unlock()
	if !ok {
		return false
	}
	ch <- answer
	return true
}

func (w *inlineQuestionWaiters) unregister(id string) {
	w.lock()
	delete(w.mu, id)
	w.unlock()
}

func (h *Handler) interactiveControllers() *interactiveControllers {
	if h == nil {
		return nil
	}
	if h.inlineQuestionWaiters == nil {
		h.inlineQuestionWaitersMu.Lock()
		if h.inlineQuestionWaiters == nil {
			h.inlineQuestionWaiters = newInlineQuestionWaiters()
		}
		h.inlineQuestionWaitersMu.Unlock()
	}
	return &interactiveControllers{handler: h, waiters: h.inlineQuestionWaiters}
}

// AnswerInlineQuestion 投递用户答案（/runtime/commands `answer_question` 的
// inline 分支）。返回是否命中挂起中的问题；未命中交由调用方回退 actor 路径。
func (h *Handler) AnswerInlineQuestion(questionID, answer string) bool {
	c := h.interactiveControllers()
	if c == nil {
		return false
	}
	return c.waiters.resolve(strings.TrimSpace(questionID), answer)
}

// ---------------------------------------------------------------------------
// toolbroker.UserInputHandler
// ---------------------------------------------------------------------------

// AskUserQuestion implements toolbroker.UserInputHandler（web inline 版）。
// 语义对齐 SessionActor.AskUserQuestion：注册 pending_question +
// PendingTool → question_asked 事件 → 等 answer_question。
func (c *interactiveControllers) AskUserQuestion(ctx context.Context, req toolbroker.UserQuestionRequest) (string, error) {
	if c == nil || c.handler == nil {
		return "", fmt.Errorf("interactive controllers are not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return "", fmt.Errorf("session id is required for ask_user_question")
	}
	questionID := strings.TrimSpace(req.ID)
	if questionID == "" {
		questionID = fmt.Sprintf("question_%d", time.Now().UnixNano())
	}

	session := c.handler.sessionByID(ctx, sessionID)
	if session == nil {
		return "", fmt.Errorf("session %q not found", sessionID)
	}

	pending := &toolbroker.UserQuestionRequest{
		ID:          questionID,
		SessionID:   sessionID,
		ToolCallID:  strings.TrimSpace(req.ToolCallID),
		Prompt:      req.Prompt,
		Suggestions: append([]string(nil), req.Suggestions...),
		Required:    req.Required,
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   req.ExpiresAt,
	}
	if err := c.handler.syncInlinePendingQuestion(ctx, sessionID, session, pending); err != nil {
		return "", err
	}

	waiter := c.waiters.register(questionID)
	// 退出路径无条件清挂起（幂等）：answer 唤醒 / ctx 取消 / 异常都清——
	// runtime state 的 PendingQuestion 不能在唤醒后残留（§21 测试锁定的语义）。
	defer func() {
		c.waiters.unregister(questionID)
		_ = c.handler.syncInlinePendingQuestion(context.Background(), sessionID, session, nil)
	}()

	c.handler.publishSessionRuntimeEvent("question_asked", "", sessionID, map[string]interface{}{
		"question_id": questionID,
		"prompt":      req.Prompt,
		"required":    req.Required,
		"suggestions": req.Suggestions,
		"source":      "inline_web",
	})

	select {
	case answer := <-waiter:
		return answer, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// syncInlinePendingQuestion 把 inline 挂起的问题写入 durable 会话上下文与
// runtime 快照（/runtime 的 pending_question 快照供前端呈现；nil = 清除）。
func (h *Handler) syncInlinePendingQuestion(ctx context.Context, sessionID string, session *chat.Session, pending *toolbroker.UserQuestionRequest) error {
	if h == nil || h.sessionManager == nil || session == nil {
		return fmt.Errorf("session persistence is not configured")
	}
	if session.Metadata.Context == nil {
		session.Metadata.Context = map[string]interface{}{}
	}
	var pendingTool *chat.PendingToolInvocation
	if pending != nil {
		// PendingTool 是 turn 恢复的载体（actor 侧 newPendingToolInvocation 同语义）：
		// 记录工具与参数，answer 后由模型侧继续。
		pendingTool = &chat.PendingToolInvocation{
			ToolName: toolbroker.ToolAskUserQuestion,
		}
	}
	state := h.inlineRuntimeState(sessionID)
	if state != nil {
		state.PendingQuestion = pending
		state.PendingTool = pendingTool
		if pending != nil {
			state.Status = chat.SessionWaitingInput
		} else {
			state.Status = chat.SessionRunning
		}
		state.UpdatedAt = time.Now().UTC()
		if err := h.getSessionRuntimeStore().SaveState(ctx, state); err != nil {
			return fmt.Errorf("persist inline pending question: %w", err)
		}
	}
	if err := h.sessionManager.Update(ctx, session); err != nil {
		return fmt.Errorf("persist session pending question: %w", err)
	}
	return nil
}

func (h *Handler) inlineRuntimeState(sessionID string) *chat.RuntimeState {
	store := h.getSessionRuntimeStore()
	if store == nil {
		return nil
	}
	state, err := store.LoadState(context.Background(), strings.TrimSpace(sessionID))
	if err != nil || state == nil {
		state = &chat.RuntimeState{SessionID: strings.TrimSpace(sessionID)}
	}
	return state
}

// ---------------------------------------------------------------------------
// toolbroker.PlanModeController
// ---------------------------------------------------------------------------

func (c *interactiveControllers) loadSessionForPlan(ctx context.Context, sessionID string) (*chat.Session, error) {
	session := c.handler.sessionByID(ctx, strings.TrimSpace(sessionID))
	if session == nil {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	return session, nil
}

func planModeResultFromWebState(state planmode.State, permissionMode string) *toolbroker.PlanModeResult {
	return &toolbroker.PlanModeResult{
		Active:             planmode.IsActive(state),
		Status:             string(state.Status),
		PlanPath:           state.PlanPath,
		PermissionMode:     strings.TrimSpace(permissionMode),
		PreviousMode:       state.PreviousMode,
		ExitDecision:       string(state.ExitDecision),
		Notes:              state.Notes,
		WriteAllowPaths:    append([]string(nil), state.WriteAllowPaths...),
		EnteredAt:          state.EnteredAt,
		ExitedAt:           state.ExitedAt,
		PendingExitRequest: state.PendingExitRequest,
		ExitSource:         string(state.LastExitSource),
		ReviewRound:        state.ReviewRound,
		PendingReviewNotes: state.PendingReviewNotes,
		ReopenedFrom:       state.ReopenedFrom,
		ReopenedVersion:    state.ReopenedVersion,
	}
}

// EnterPlanMode implements toolbroker.PlanModeController（web inline 版）。
// planmode 状态落 durable 会话上下文；permission_mode 落会话元数据——
// 下一 turn 的 applyAgentExecutionPolicy 从会话状态读取 plan 收窄。
func (c *interactiveControllers) EnterPlanMode(ctx context.Context, sessionID string, args toolbroker.EnterPlanModeArgs) (*toolbroker.PlanModeResult, error) {
	if c == nil || c.handler == nil {
		return nil, fmt.Errorf("interactive controllers are not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := c.loadSessionForPlan(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	current := planmode.Load(session)
	previousMode := strings.TrimSpace(sessionmetaPlanPermissionMode(session))
	// 嵌套 enter：保持原 previous mode，刷新路径。
	if planmode.IsActive(current) && strings.TrimSpace(current.PreviousMode) != "" {
		previousMode = current.PreviousMode
	}
	if strings.EqualFold(previousMode, string(runtimepolicy.ModePlan)) &&
		(!planmode.IsActive(current) || strings.TrimSpace(current.PreviousMode) == "") {
		previousMode = string(runtimepolicy.ModeDefault)
	}

	state := planmode.EnterPlan(planmode.EnterOptions{
		PreviousMode:    previousMode,
		PlanPath:        args.PlanPath,
		WriteAllowPaths: args.PlanWritePaths,
		Workspace:       sessionWorkspaceDir(session),
	})
	planmode.Save(session, state)
	sessionmetaSetPlanPermissionMode(session, string(runtimepolicy.ModePlan))
	if err := c.handler.persistInlinePlanState(ctx, session); err != nil {
		return nil, err
	}

	c.handler.publishSessionRuntimeEvent("plan_mode_changed", "", session.ID, map[string]interface{}{
		"status":     string(state.Status),
		"plan_path":  state.PlanPath,
		"mode":       string(runtimepolicy.ModePlan),
		"source":     "inline_web",
		"transition": "enter",
	})
	return planModeResultFromWebState(state, string(runtimepolicy.ModePlan)), nil
}

// ExitPlanMode implements toolbroker.PlanModeController（web inline 版）。
// 与 actor 侧同语义：request_changes 保持 active 并记轮次；approve/quit 还原
// previous mode。模型来源的 approve/quit 在 web 面同样走 pending approval
// 卡片确认（planmode.ModelMayDecideExit 门控与 actor 一致）。
func (c *interactiveControllers) ExitPlanMode(ctx context.Context, sessionID string, args toolbroker.ExitPlanModeArgs) (*toolbroker.PlanModeResult, error) {
	if c == nil || c.handler == nil {
		return nil, fmt.Errorf("interactive controllers are not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := c.loadSessionForPlan(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	current := planmode.Load(session)
	currentMode := strings.TrimSpace(sessionmetaPlanPermissionMode(session))
	if !planmode.IsActive(current) && current.Status != planmode.StatusExited {
		if !strings.EqualFold(currentMode, string(runtimepolicy.ModePlan)) {
			return nil, fmt.Errorf("not in plan mode; call enter_plan_mode first")
		}
		current = planmode.Enter(string(runtimepolicy.ModeDefault), planmode.DefaultPlanPath)
	}

	decision, err := planmode.NormalizeExitDecision(args.Decision)
	if err != nil {
		return nil, err
	}
	source := planmode.NormalizeExitSource(args.Source)
	// 模型裁决须用户确认（web 面的确认通道是 pending approval 卡片）。
	if source == planmode.ExitSourceModel && !planmode.ModelMayDecideExit() {
		switch decision {
		case planmode.ExitApprove, planmode.ExitQuit:
			return nil, fmt.Errorf(
				"plan exit decision %q requires user confirmation on the web approval surface; the session stays in plan mode",
				decision)
		}
	}

	exited, err := planmode.Exit(current, decision, args.Notes)
	if err != nil {
		return nil, err
	}
	exited.LastExitSource = source

	resume := planmode.ResumeModeAfterExit(exited)
	mode := parsePlanPermissionMode(resume)
	if mode == runtimepolicy.ModeBypassPermissions && runtimepolicy.BypassDisabledForWorkspace(sessionWorkspaceDir(session)) {
		mode = runtimepolicy.ModeDefault
	}

	transition := "exit"
	if exited.ExitDecision == planmode.ExitRequestChanges {
		// request_changes：保持 plan 活跃再走一轮修订，记录轮次与用户笔记。
		exited.Status = planmode.StatusActive
		exited.PendingExitRequest = false
		if source == planmode.ExitSourceUser && strings.TrimSpace(args.Notes) != "" {
			exited = planmode.RecordReviewNotes(exited, args.Notes)
		}
		planmode.Save(session, exited)
		sessionmetaSetPlanPermissionMode(session, string(runtimepolicy.ModePlan))
		transition = "request_changes"
	} else {
		planmode.Save(session, exited)
		sessionmetaSetPlanPermissionMode(session, string(mode))
	}
	if err := c.handler.persistInlinePlanState(ctx, session); err != nil {
		return nil, err
	}

	c.handler.publishSessionRuntimeEvent("plan_mode_changed", "", session.ID, map[string]interface{}{
		"status":     string(exited.Status),
		"decision":   string(exited.ExitDecision),
		"mode":       string(mode),
		"source":     source,
		"transition": transition,
	})
	return planModeResultFromWebState(exited, string(mode)), nil
}

// ---------------------------------------------------------------------------
// 会话元数据的 plan permission mode 读写（inline 侧的 applySessionPermissionMode
// 等价物：下一 turn 的会话状态读取生效）。
// ---------------------------------------------------------------------------

const inlinePlanPermissionModeKey = "plan_permission_mode"

func sessionmetaPlanPermissionMode(session *chat.Session) string {
	if session == nil {
		return ""
	}
	if session.Metadata.Context == nil {
		return ""
	}
	if raw, ok := session.Metadata.Context[inlinePlanPermissionModeKey].(string); ok {
		return strings.TrimSpace(raw)
	}
	return ""
}

func sessionmetaSetPlanPermissionMode(session *chat.Session, mode string) {
	if session == nil {
		return
	}
	if session.Metadata.Context == nil {
		session.Metadata.Context = map[string]interface{}{}
	}
	session.Metadata.Context[inlinePlanPermissionModeKey] = strings.TrimSpace(mode)
}

// persistInlinePlanState 把 plan 状态所在的 durable 会话落库（会话上下文已由
// planmode.Save / sessionmetaSetPlanPermissionMode 就地更新）。
func (h *Handler) persistInlinePlanState(ctx context.Context, session *chat.Session) error {
	if h == nil || h.sessionManager == nil || session == nil {
		return fmt.Errorf("session persistence is not configured")
	}
	if err := h.sessionManager.Update(ctx, session); err != nil {
		return fmt.Errorf("persist plan mode session: %w", err)
	}
	return nil
}

// parsePlanPermissionMode 与 internal/chat.plan_mode_tools.go 同语义：web 侧
// 独立一份（chat 包的该函数未导出，避免为它开导出口）。
func parsePlanPermissionMode(raw string) runtimepolicy.Mode {
	switch runtimepolicy.Mode(strings.ToLower(strings.TrimSpace(raw))) {
	case runtimepolicy.ModeAcceptEdits:
		return runtimepolicy.ModeAcceptEdits
	case runtimepolicy.ModePlan:
		return runtimepolicy.ModePlan
	case runtimepolicy.ModeBypassPermissions:
		return runtimepolicy.ModeBypassPermissions
	case runtimepolicy.ModeDontAsk:
		return runtimepolicy.ModeDontAsk
	default:
		return runtimepolicy.ModeDefault
	}
}
