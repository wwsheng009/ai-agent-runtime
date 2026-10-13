package runtimeapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
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

	if req.OnDelta != nil {
		if unsubscribe := h.subscribeConnectorDeltas(sessionID, req.OnDelta); unsubscribe != nil {
			defer unsubscribe()
		}
	}

	client := &sessionActorClient{hub: h.getSessionHub(), handler: h}
	result, err := client.submitPrompt(ctx, sessionID, prompt, nil)
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
