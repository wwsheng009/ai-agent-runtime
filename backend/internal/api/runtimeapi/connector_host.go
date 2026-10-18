package runtimeapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// 本文件是 runtime-server 宿主接入 IM 连接器（docs/bots/06-target-architecture.md
// §3.2 / §4.2）所需的两个进程内入口：
//
//  1. AuthorizeConnectorAdmin：管理面 `/api/connectors*` 的鉴权口径，与 skills
//     管理面一致（admin token / 可信角色 / 环回访问），供宿主挂载
//     internal/connectors/restapi 时注入；
//  2. SubmitConnectorPrompt：把一条 IM 消息投递进既有会话（复用
//     /sessions/{id}/runtime/commands 的 submit_prompt 执行路径），并把
//     assistant 打字机增量以回调形式回推给连接器，避免宿主为每条 IM 消息自环
//     HTTP。

// ConnectorTurnRequest 是连接器宿主投递的一轮对话请求。
type ConnectorTurnRequest struct {
	// SessionID 为空时按 (UserID) 新建会话，否则续用既有会话。
	SessionID string
	// UserID 参与会话归属；为空时沿用会话管理者默认用户。
	UserID string
	// Prompt 是用户消息正文（必填）。
	Prompt string
	// OnDelta 可空；收到 assistant 增量文本时回调（同一 goroutine 串行）。
	OnDelta func(text string)
	// Route 是连接器级模型路由（D6）：配置后覆盖全局默认 provider/model/
	// reasoning effort，仅作用于本回合。零值 = 回落宿主全局默认。
	Route ConnectorRouteOverride
	// RegisterActivity（可选，S1b/L3，docs/bots/13 §4.2）：会话解析完成后、
	// 提交回合**之前**回调（返回的 done 在回合结束后调用）。宿主用它登记
	// 「IM 来源回合进行中」，内容镜像据此跳过 IM 回合（正文已由回复路径投递）。
	// 注意 sessionID 是 GetOrCreate 解析后的最终 id（auto-create 场景与请求里的
	// 空 SessionID 不同），登记必须用它。
	RegisterActivity func(sessionID string) (done func())
}

// ConnectorRouteOverride 是连接器策略中的模型路由（docs/bots/09 §5.5 D6）。
type ConnectorRouteOverride struct {
	Provider        string
	Model           string
	ReasoningEffort string
}

// ConnectorTurnResult 是一轮对话的终态。
type ConnectorTurnResult struct {
	SessionID string
	Reply     string
	Success   bool
	Error     string
}

// connectorDeltaEventTypes 覆盖总线上两种等价写法（点分隔形式另见
// internal/runtimeobserve/known_types.go）。
var connectorDeltaEventTypes = []string{"assistant_delta", "assistant.delta"}

// connectorDeltaPayloadKeys 是增量文本在事件载荷中的候选键。
var connectorDeltaPayloadKeys = []string{"delta", "text", "content"}

// AuthorizeConnectorAdmin 是连接器管理面的统一鉴权口径：与 skills 管理面相同
// （admin token / 可信角色 / 环回访问）。宿主挂载 restapi.NewRouter 时注入；
// 返回 nil 表示放行。
func (h *Handler) AuthorizeConnectorAdmin(r *http.Request) error {
	if h == nil {
		return errors.New(errors.ErrAgentPermission, "connector admin unavailable: handler is nil")
	}
	if h.searchAdminAccessMode(r) == "denied" {
		return errors.New(errors.ErrAgentPermission, "connector admin access denied")
	}
	return nil
}

// SubmitConnectorPrompt 以进程内方式投递一轮 IM 对话：
//   - 会话不存在时按 UserID 新建（与 POST /api/runtime/sessions 同源）；
//   - 复用 sessionActorClient.submitPrompt，执行路径与 runtime 命令面一致
//     （含 supervision preflight）；
//   - OnDelta 非空时订阅运行时事件总线，把 assistant 增量回推给连接器。
func (h *Handler) SubmitConnectorPrompt(ctx context.Context, req ConnectorTurnRequest) (*ConnectorTurnResult, error) {
	if h == nil || h.sessionManager == nil {
		return nil, fmt.Errorf("runtime session manager is not configured")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, fmt.Errorf("prompt is empty")
	}

	session, err := h.sessionManager.GetOrCreate(ctx, strings.TrimSpace(req.UserID), strings.TrimSpace(req.SessionID))
	if err != nil {
		return nil, fmt.Errorf("get or create connector session: %w", err)
	}
	sessionID := session.ID
	turn := &ConnectorTurnResult{SessionID: sessionID}
	if req.RegisterActivity != nil {
		if done := req.RegisterActivity(sessionID); done != nil {
			defer done()
		}
	}

	if req.OnDelta != nil {
		if unsubscribe := h.subscribeConnectorDeltas(sessionID, req.OnDelta); unsubscribe != nil {
			defer unsubscribe()
		}
	}

	client := &sessionActorClient{hub: h.getSessionHub(), handler: h}
	// IM 入站没有交互式审批通道：与 aicli 宿主同语义显式使用
	// bypass_permissions。若沿用默认权限模式，无审批 responder 的回合会挂起/
	// 失败。用户准入由连接器 Policy 白名单先行过滤；开启 fail-closed 闸的宿主
	// （connectors.Deps.RequireAllowedUsers=true，如 aicli）还会在启动期拒绝
	// 白名单为空的连接器。
	// D6：连接器策略路由非空时按回合覆盖（RunRouteOverride 是既有机制，
	// team 任务的 TaskExecutionRoute 走同一通道；空值项由宿主默认回落）。
	var submitOpts []runtimechat.SubmitPromptOption
	if route := req.Route; strings.TrimSpace(route.Provider) != "" ||
		strings.TrimSpace(route.Model) != "" ||
		strings.TrimSpace(route.ReasoningEffort) != "" {
		submitOpts = append(submitOpts, runtimechat.SubmitPromptOption{
			RouteOverride: &runtimechat.RunRouteOverride{
				Provider:        strings.TrimSpace(route.Provider),
				Model:           strings.TrimSpace(route.Model),
				ReasoningEffort: strings.TrimSpace(route.ReasoningEffort),
			},
		})
	}
	result, err := client.submitPrompt(ctx, sessionID, prompt, &team.RunMeta{PermissionMode: "bypass_permissions"}, submitOpts...)
	if result != nil {
		turn.Reply = strings.TrimSpace(result.Output)
		turn.Success = result.Success
		if !result.Success {
			turn.Error = strings.TrimSpace(result.Error)
		}
	}
	if err != nil {
		if turn.Error == "" {
			turn.Error = err.Error()
		}
		if turn.Reply == "" {
			return turn, fmt.Errorf("submit connector prompt: %w", err)
		}
	}
	return turn, nil
}

// subscribeConnectorDeltas 订阅目标会话的 assistant 增量；返回退订函数。
// 总线不可用时返回 nil（连接器退化为仅终态回复）。
func (h *Handler) subscribeConnectorDeltas(sessionID string, onDelta func(text string)) func() {
	bus := h.getRuntimeEventBus()
	if bus == nil {
		return nil
	}
	unsubscribes := make([]func(), 0, len(connectorDeltaEventTypes))
	for _, eventType := range connectorDeltaEventTypes {
		unsub := bus.SubscribeCancelable(eventType, func(event runtimeevents.Event) {
			if strings.TrimSpace(event.SessionID) != sessionID {
				return
			}
			if text := streamCoalesceFirstPayloadString(event.Payload, connectorDeltaPayloadKeys...); text != "" {
				onDelta(text)
			}
		})
		if unsub != nil {
			unsubscribes = append(unsubscribes, unsub)
		}
	}
	if len(unsubscribes) == 0 {
		return nil
	}
	return func() {
		for _, unsub := range unsubscribes {
			unsub()
		}
	}
}

// ── 审批/提问命令执行面（FR-7）：IM /approve /deny /answer 的宿主侧入口 ──────
//
// 与 REST `/sessions/{id}/runtime/commands` 的 approve_tool / answer_question
// 同一语义：hub.GetOrCreate → actor.ApproveToolWithDecision / actor.AnswerQuestion。
// 挂起项解析统一读 durable runtime state（与 GET /sessions/{id}/runtime 同源），
// 保证 IM 命令看到与 Web 控制台一致的事实。

// ConnectorCommandOutcome 是一次 IM 审批命令在会话 actor 上的执行结果。
type ConnectorCommandOutcome struct {
	SessionID  string
	RequestID  string
	Allowed    bool
	Resumed    bool
	Resolution string
}

// ConnectorPendingApprovalID 返回会话当前挂起的审批 request_id（无则空串）。
func (h *Handler) ConnectorPendingApprovalID(ctx context.Context, sessionID string) (string, error) {
	state, err := h.connectorRuntimeState(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if state == nil || state.PendingApproval == nil {
		return "", nil
	}
	return strings.TrimSpace(state.PendingApproval.ID), nil
}

// ConnectorPendingQuestionID 返回会话当前挂起的提问 question_id（无则空串）。
func (h *Handler) ConnectorPendingQuestionID(ctx context.Context, sessionID string) (string, error) {
	state, err := h.connectorRuntimeState(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if state == nil || state.PendingQuestion == nil {
		return "", nil
	}
	return strings.TrimSpace(state.PendingQuestion.ID), nil
}

// ConnectorSessionParked 报告会话是否持有 durable 挂起 turn（§6.12）：此时
// agent.turn.finished 只代表当前 run 收尾，不代表托管 turn 完成（与
// gateway_attention.go 的 completed/waiting 判定同源）。
func (h *Handler) ConnectorSessionParked(sessionID string) bool {
	return h.sessionHasParkedTurnRecord(sessionID)
}

// connectorRuntimeState 读取会话 durable 运行态；runtime store 未装配时回落到
// 进程内 actor 状态（与 ResolveApproval 的会话解析口径一致）。
func (h *Handler) connectorRuntimeState(ctx context.Context, sessionID string) (*runtimechat.RuntimeState, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	if store := h.getSessionRuntimeStore(); store != nil {
		return store.LoadState(ctx, sessionID)
	}
	hub := h.getSessionHub()
	if hub == nil {
		return nil, fmt.Errorf("session hub not configured")
	}
	actor, err := hub.GetOrCreate(sessionID)
	if err != nil {
		return nil, err
	}
	return actor.StateForInspection(), nil
}

// ConnectorDecideApproval 批准/拒绝一个已确认挂起的审批（requestID 必填）：
// 与 REST approve_tool 同一调用路径（hub → actor.ApproveToolWithDecision）。
func (h *Handler) ConnectorDecideApproval(ctx context.Context, sessionID, requestID string, allow bool) (ConnectorCommandOutcome, error) {
	outcome := ConnectorCommandOutcome{
		SessionID: strings.TrimSpace(sessionID),
		RequestID: strings.TrimSpace(requestID),
		Allowed:   allow,
	}
	if h == nil {
		return outcome, fmt.Errorf("handler is nil")
	}
	if outcome.SessionID == "" {
		return outcome, fmt.Errorf("session id is required")
	}
	if outcome.RequestID == "" {
		return outcome, fmt.Errorf("request id is required")
	}
	hub := h.getSessionHub()
	if hub == nil {
		return outcome, fmt.Errorf("session hub not configured")
	}
	actor, err := hub.GetOrCreate(outcome.SessionID)
	if err != nil {
		return outcome, err
	}
	if err := actor.ApproveToolWithDecision(ctx, outcome.RequestID, runtimechat.ApproveToolDecision{Allow: allow}); err != nil {
		return outcome, err
	}
	resolution, resumed := actor.ApprovalOutcome(outcome.RequestID)
	outcome.Resolution = resolution
	outcome.Resumed = resumed
	if toolbroker.ApprovalResolutionNotApplied(resolution) {
		// 决定已记录但未生效（原 run 已终态）：按事实报告未批准，不谎报已生效。
		outcome.Allowed = false
	}
	return outcome, nil
}

// ConnectorAnswerQuestion 回答一个已确认挂起的提问（questionID 必填）：
// 与 REST answer_question 同一调用路径（hub → actor.AnswerQuestion）。
func (h *Handler) ConnectorAnswerQuestion(ctx context.Context, sessionID, questionID, answer string) error {
	if h == nil {
		return fmt.Errorf("handler is nil")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	questionID = strings.TrimSpace(questionID)
	if questionID == "" {
		return fmt.Errorf("question id is required")
	}
	hub := h.getSessionHub()
	if hub == nil {
		return fmt.Errorf("session hub not configured")
	}
	actor, err := hub.GetOrCreate(sessionID)
	if err != nil {
		return err
	}
	return actor.AnswerQuestion(ctx, questionID, answer)
}

// ConnectorBacktrack 把 IM 会话回退到指定 user turn（与 TUI /backtrack 同一
// actor 实现）。语义与 REST POST /sessions/{id}/backtrack 一致：
//   - 需要会话写租约（与正在进行的 turn 互斥）；
//   - 只做 conversation 模式（IM 无代码 diff 上下文）；
//   - 调用方负责把 runtimechat.BacktrackRequest 的 selector 换算成
//     UserTurnIndex（IM 侧 /undo 默认回退最近一轮）。
func (h *Handler) ConnectorBacktrack(ctx context.Context, sessionID string, req runtimechat.BacktrackRequest) (*runtimechat.BacktrackResult, error) {
	if h == nil {
		return nil, fmt.Errorf("handler is nil")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	hub := h.getSessionHub()
	if hub == nil {
		return nil, fmt.Errorf("session hub not configured")
	}
	actor, err := hub.GetOrCreate(sessionID)
	if err != nil {
		return nil, err
	}
	req.PreviewOnly = false
	req.AutoSubmit = false
	if strings.TrimSpace(req.Mode) == "" {
		req.Mode = runtimechat.BacktrackModeConversation
	}
	leaseHandle, leaseErr := h.acquireSessionLease(ctx, sessionID, sessionActorLeaseOwnerKind, "connector-undo")
	if leaseErr != nil {
		return nil, leaseErr
	}
	if leaseHandle != nil {
		defer func() { _ = leaseHandle.Release(context.Background()) }()
	}
	result, _, err := actor.Backtrack(ctx, req)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ConnectorListUserTurns 返回会话当前可见历史的 user turn 列表（只读，
// 不需要租约；与 GET /sessions/{id}/turns 同源）。
func (h *Handler) ConnectorListUserTurns(ctx context.Context, sessionID string) ([]runtimechat.UserTurn, error) {
	if h == nil {
		return nil, fmt.Errorf("handler is nil")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	session, err := h.readSessionSnapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	checkpoints := h.readSessionCheckpoints(ctx, sessionID)
	return runtimechat.ListUserTurns(session.GetMessages(), checkpoints), nil
}

// connectorAttentionEventTypes 是连接器注意力出站（FR-7/FR-8）订阅的事件类型：
// 审批请求、提问请求、回合结束（completion）与工作类（S4 聚合订阅：工具/子代理）。
// 事件名与 gateway_attention.go 的状态投影、共享层 connectors.AttentionEventTypes()
// 同源，避免两套 attention 语义漂移（改动需与 internal/connectors 同步）。
var connectorAttentionEventTypes = []string{
	runtimechat.EventApprovalRequested,
	runtimechat.EventQuestionAsked,
	runtimeobserve.EventAgentTurnDone,
	// 工作类（docs/bots/13 §3.3）：仅 digest 形态投递（见 connectors 订阅求解）。
	"tool.requested",
	"tool.completed",
	"tool.denied",
	"subagent.completed",
	"subagent.batch.completed",
}

// SubscribeConnectorAttentionEvents 订阅连接器注意力出站所需的事件，返回退订
// 函数；事件总线不可用时返回 nil（连接器退化为仅命令/回复闭环）。订阅方负责
// 退订（宿主 close / ctx 取消时调用返回的函数）。
func (h *Handler) SubscribeConnectorAttentionEvents(handler func(runtimeevents.Event)) func() {
	if h == nil || handler == nil {
		return nil
	}
	bus := h.getRuntimeEventBus()
	if bus == nil {
		return nil
	}
	unsubscribes := make([]func(), 0, len(connectorAttentionEventTypes))
	for _, eventType := range connectorAttentionEventTypes {
		if unsub := bus.SubscribeCancelable(eventType, handler); unsub != nil {
			unsubscribes = append(unsubscribes, unsub)
		}
	}
	if len(unsubscribes) == 0 {
		return nil
	}
	return func() {
		for _, unsub := range unsubscribes {
			unsub()
		}
	}
}

// connectorSessionOutputEventTypes 是会话输出事件的订阅清单（含点分隔别名）：
// 终态正文（assistant_message，S1b 内容镜像）+ assistant 增量（assistant_delta，
// S4 流式订阅）——两个镜像共用同一订阅面，订阅方按事件类型自行过滤。
var connectorSessionOutputEventTypes = []string{
	runtimechat.EventAssistantMessage, "assistant.message",
	runtimechat.EventAssistantDelta, "assistant.delta",
}

// SubscribeConnectorSessionOutput 订阅会话终态正文事件（docs/bots/13 §8 S1b），
// 返回退订函数；事件总线不可用时返回 nil（连接器退化为仅回复/命令闭环）。
// 订阅方负责退订（宿主 close / ctx 取消时调用返回的函数）。
func (h *Handler) SubscribeConnectorSessionOutput(handler func(runtimeevents.Event)) func() {
	if h == nil || handler == nil {
		return nil
	}
	bus := h.getRuntimeEventBus()
	if bus == nil {
		return nil
	}
	unsubscribes := make([]func(), 0, len(connectorSessionOutputEventTypes))
	for _, eventType := range connectorSessionOutputEventTypes {
		if unsub := bus.SubscribeCancelable(eventType, handler); unsub != nil {
			unsubscribes = append(unsubscribes, unsub)
		}
	}
	if len(unsubscribes) == 0 {
		return nil
	}
	return func() {
		for _, unsub := range unsubscribes {
			unsub()
		}
	}
}
