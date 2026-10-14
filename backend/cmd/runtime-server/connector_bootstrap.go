package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/providers/feishu"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/providers/qqbot"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/providers/telegram"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/providers/wechat"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/restapi"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/statepath"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/storefile"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

// 本文件是 runtime-server 宿主侧的连接器接线（docs/bots/04-migration-roadmap.md P0
// 「宿主接线」、docs/bots/03-host-integration.md §6）：
//
//   - 配置读写在共享 config.yaml 的 `connectors:` 节（与 aicli 同一份文件）；
//   - 绑定/去重账本/轮询游标落在宿主数据目录的 state.json；
//   - 管理面 /api/connectors* 复用 skills 管理面鉴权口径；
//   - 公网 webhook /hooks/connectors/{id} 由各 Provider 自行验签。
func setupRuntimeConnectors(ctx context.Context, handler *runtimeapi.Handler, router *mux.Router, configPath string, logf func(format string, args ...any)) (*connectors.Service, error) {
	if handler == nil || router == nil {
		return nil, errors.New("connectors: runtime handler/router is not ready")
	}
	if logf == nil {
		logf = log.Printf
	}
	store := storefile.NewConfigStore(configPath, runtimeConnectorStatePath(logf))

	service := connectors.New(connectors.Deps{
		Store:   store,
		Backend: runtimeConnectorBackend{handler: handler, logf: logf},
		Logf:    logf,
		// 运行归属（07 §5.6）：本宿主身份用于认领/续约/释放；与 aicli 共用
		// config.yaml 时，同一通道的轮询只允许一处运行。
		HostID: "runtime-server",
		// Fix-S2：runtime-server 保持兼容（false，不 fail-closed），
		// 空白名单的可见性告警在下面单独发。
		RequireAllowedUsers: false,
	})
	if err := service.Register(telegram.New(telegram.WithLogger(logf))); err != nil {
		return nil, err
	}
	// QQ 官方机器人（WS gateway，切片 1：群@/C2C 文本 + 被动回复）。
	if err := service.Register(qqbot.New(qqbot.WithLogger(logf))); err != nil {
		return nil, err
	}
	// 微信（iLink bot，切片 2b：长轮询文本收发 + 扫码登录 action + typing；
	// 媒体降级为文本占位）。
	if err := service.Register(wechat.New(wechat.WithLogger(logf))); err != nil {
		return nil, err
	}
	// 飞书（切片 3：webhook 文本收发——challenge/事件验签/AES 解密/文本回复；
	// WS 长连接、卡片流式、媒体留待 P1 后续切片）。
	if err := service.Register(feishu.New(feishu.WithLogger(logf))); err != nil {
		return nil, err
	}

	// Fix-S2 可见性：runtime-server 不启用 fail-closed 闸（兼容既有部署），
	// 但「启用且白名单为空」意味着该连接器接受任意平台用户，必须可见。
	for _, cfg := range store.ListConfigs() {
		if cfg.Enabled && len(cfg.Policy.AllowedUsers) == 0 {
			logf("connector %s: enabled with empty policy.allowed_users; 该连接器不会限制平台用户（runtime-server 未启用 fail-closed 闸，仅告警）", cfg.ID)
		}
	}

	deps := restapi.Deps{
		Service:   service,
		Store:     store,
		Authorize: func(r *http.Request) error { return handler.AuthorizeConnectorAdmin(r) },
		Logf:      logf,
		HostID:    "runtime-server",
	}
	mountConnectorRoutes(router, deps)

	if err := service.Start(ctx); err != nil {
		return nil, err
	}
	// 注意力出站（FR-7/FR-8）：订阅 approval_requested / question_asked /
	// agent.turn.finished，映射为 AttentionNotice 异步出站到该会话的全部 IM 绑定
	// （NotifySession 内部反查绑定，无绑定即 no-op）。订阅随宿主 ctx
	// （进程生命周期 / close 时取消）退订。
	subscribeRuntimeConnectorAttention(ctx, handler, store, func(notifyCtx context.Context, notice connectors.AttentionNotice) error {
		return service.NotifyAttention(notifyCtx, notice)
	}, logf)
	return service, nil
}

// mountConnectorRoutes 把共享 restapi 契约挂到宿主 mux（三宿主同一挂载矩阵，
// 行为与 aicli / gateway 宿主一致）：
//
//   - 管理面 /api/connectors*：剥离 /api 前缀后交给 restapi.NewRouter；鉴权在
//     外层完成（runtime-server 在 Deps.Authorize 内注入
//     runtimeapi.Handler.AuthorizeConnectorAdmin，与 skills 管理面同一口径；
//     gateway 由 owner.RequireSession 包在外层）。先注册的 /api/runtime* 路由
//     优先命中，这里只兜住 /api/connectors*。
//   - 公网 webhook /hooks/connectors/*：直挂 NewHooksRouter，不走管理面鉴权，
//     由各 Provider 自行验签/挑战校验。
//
// 独立成函数是为了让挂载本身可测（connector_mount_test.go 用裸 mux 断言三条
// 入口可达），后续宿主接线改动不得偏离该形状。
func mountConnectorRoutes(router *mux.Router, deps restapi.Deps) {
	router.PathPrefix("/api/connectors").Handler(http.StripPrefix("/api", restapi.NewRouter(deps)))
	router.PathPrefix("/hooks/connectors").Handler(restapi.NewHooksRouter(deps))
}

// connectorServiceCloser 让 connectors.Service 满足 io.Closer（runtimeServerApp.close 用）。
type connectorServiceCloser struct {
	service *connectors.Service
}

// Close 停止轮询并释放连接器资源。
func (c connectorServiceCloser) Close() error {
	if c.service == nil {
		return nil
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.service.Stop(stopCtx)
	return nil
}

// runtimeConnectorStatePath 返回连接器运行时状态文件（绑定/游标/去重账本）。
// runtimeConnectorStatePath 返回 connectors 共享状态路径（docs/bots/
// 09-multi-instance-binding.md D1）：由 statepath.CanonicalStatePath 统一锚定
// 用户级 ~/.aicli/data/connectors/state.json，禁止按 configPath 推导（分层合并
// 下两宿主解析到不同分层，推导必然分叉；Fix-S4 方案已废止，见 03 §安全闸注记）。
// 解析失败时退回 DefaultAuthStorePath() 目录推导（可诊断的兜底，不静默换文件）。
func runtimeConnectorStatePath(logf func(format string, args ...any)) string {
	statePath, err := statepath.CanonicalStatePath()
	if err == nil {
		return statePath
	}
	fallback := filepath.Join(filepath.Dir(agentconfig.DefaultAuthStorePath()), "data", "connectors", "state.json")
	if logf != nil {
		logf("connectors: canonical state path unavailable (%v), falling back to %s", err, fallback)
	}
	return fallback
}

// runtimeConnectorBackend 把 IM 消息投递到本进程的会话执行路径
// （runtimeapi.Handler.SubmitConnectorPrompt），并把 assistant 增量回推给连接器。
type runtimeConnectorBackend struct {
	handler runtimeConnectorCommandHost
	logf    func(format string, args ...any)
}

// runtimeConnectorCommandHost 是 runtimeConnectorBackend 依赖的宿主命令面。
// 生产实现是 *runtimeapi.Handler；测试可注入假实现，无需完整 runtime 栈。
type runtimeConnectorCommandHost interface {
	SubmitConnectorPrompt(ctx context.Context, req runtimeapi.ConnectorTurnRequest) (*runtimeapi.ConnectorTurnResult, error)
	ConnectorPendingApprovalID(ctx context.Context, sessionID string) (string, error)
	ConnectorPendingQuestionID(ctx context.Context, sessionID string) (string, error)
	ConnectorDecideApproval(ctx context.Context, sessionID, requestID string, allow bool) (runtimeapi.ConnectorCommandOutcome, error)
	ConnectorAnswerQuestion(ctx context.Context, sessionID, questionID, answer string) error
	ConnectorSessionParked(sessionID string) bool
}

func (b runtimeConnectorBackend) Name() string { return "runtime-server" }

func (b runtimeConnectorBackend) Handle(ctx context.Context, req connectors.BackendRequest, sink connectors.ReplySink) (connectors.BackendResult, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return connectors.BackendResult{}, nil
	}
	var onDelta func(string)
	if sink != nil {
		onDelta = func(delta string) {
			if err := sink.Delta(ctx, delta); err != nil && b.logf != nil {
				b.logf("delta sink failed: %v", err)
			}
		}
	}
	turn, err := b.handler.SubmitConnectorPrompt(ctx, runtimeapi.ConnectorTurnRequest{
		SessionID: req.Session.SessionID,
		UserID:    req.UserID,
		Prompt:    text,
		OnDelta:   onDelta,
	})
	if turn == nil {
		return connectors.BackendResult{}, err
	}
	result := connectors.BackendResult{Session: connectors.SessionRef{SessionID: turn.SessionID}}
	if reply := strings.TrimSpace(turn.Reply); reply != "" {
		result.Reply = []connectors.OutboundMessage{{Text: reply}}
	}
	if err != nil {
		return result, err
	}
	if !turn.Success {
		message := strings.TrimSpace(turn.Error)
		if message == "" {
			message = "agent run failed"
		}
		return result, errors.New(message)
	}
	return result, nil
}

// ── 审批/提问命令（FR-7）：/approve /deny /answer 的 IM 闭环 ────────────────
//
// 核心只做命令解析与绑定补齐；这里负责把参数解释为宿主语义：
//   - approve/deny：可选 request_id；空 = 当前挂起项（由 durable runtime state 解析）；
//   - answer：Args[0] 为序号（1 = 当前挂起提问）或 question_id，其余为回答内容；
//   - 无绑定会话 / 无挂起项 / 目标不匹配 → Handled=false + 明确文案（不报错、不落 turn）。

// DecideApproval 处理 /approve 与 /deny。
func (b runtimeConnectorBackend) DecideApproval(ctx context.Context, req connectors.CommandRequest) (connectors.CommandResult, error) {
	if b.handler == nil {
		return connectors.CommandResult{}, errors.New("runtime-server: session handler is not ready")
	}
	sessionID := strings.TrimSpace(req.Session.SessionID)
	if sessionID == "" {
		return connectorCommandNotice("当前 IM 会话尚未绑定 agent 会话：先发送一条普通消息建立会话，再处理审批。"), nil
	}
	allow := !strings.EqualFold(strings.TrimSpace(req.Command), "deny")
	requested := ""
	if len(req.Args) > 0 {
		requested = strings.TrimSpace(req.Args[0])
	}
	pendingID, err := b.handler.ConnectorPendingApprovalID(ctx, sessionID)
	if err != nil {
		return connectors.CommandResult{}, fmt.Errorf("resolve pending approval: %w", err)
	}
	if pendingID == "" {
		return connectorCommandNotice("当前没有待审批项（可能已由其它入口处理）。"), nil
	}
	if requested != "" && requested != pendingID {
		return connectorCommandNotice(fmt.Sprintf("request_id 与当前待审批项不匹配：当前待审批项为 %s。", pendingID)), nil
	}
	outcome, err := b.handler.ConnectorDecideApproval(ctx, sessionID, pendingID, allow)
	if err != nil {
		return connectors.CommandResult{}, fmt.Errorf("resolve approval %s: %w", pendingID, err)
	}
	text := "已拒绝：" + outcome.RequestID
	if outcome.Allowed {
		text = "已批准：" + outcome.RequestID
		if outcome.Resumed {
			text += "（已恢复运行）"
		}
	} else if allow {
		// 批准决定已记录但未生效（原 run 已终态）：按事实报告，不谎报已批准。
		text = "审批未生效（原回合已终止）：" + outcome.RequestID
	}
	return connectors.CommandResult{Handled: true, Reply: []connectors.OutboundMessage{{Text: text}}}, nil
}

// AnswerQuestion 处理 /answer。
func (b runtimeConnectorBackend) AnswerQuestion(ctx context.Context, req connectors.CommandRequest) (connectors.CommandResult, error) {
	if b.handler == nil {
		return connectors.CommandResult{}, errors.New("runtime-server: session handler is not ready")
	}
	sessionID := strings.TrimSpace(req.Session.SessionID)
	if sessionID == "" {
		return connectorCommandNotice("当前 IM 会话尚未绑定 agent 会话：先发送一条普通消息建立会话，再回答问题。"), nil
	}
	pendingID, err := b.handler.ConnectorPendingQuestionID(ctx, sessionID)
	if err != nil {
		return connectors.CommandResult{}, fmt.Errorf("resolve pending question: %w", err)
	}
	if pendingID == "" {
		return connectorCommandNotice("当前没有待回答的问题（可能已回答或已过期）。"), nil
	}
	ref := ""
	if len(req.Args) > 0 {
		ref = strings.TrimSpace(req.Args[0])
	}
	switch {
	case ref == "", ref == "1", ref == pendingID:
		// 单挂起提问模型：序号 1 与 question_id 都指向当前挂起项。
	default:
		return connectorCommandNotice(fmt.Sprintf("序号/问题 id 与当前待回答的问题不匹配（当前问题：%s；可用 /answer 1 <内容>）。", pendingID)), nil
	}
	answer := connectorCommandAnswerText(req.Text, ref)
	if err := b.handler.ConnectorAnswerQuestion(ctx, sessionID, pendingID, answer); err != nil {
		return connectors.CommandResult{}, fmt.Errorf("answer question %s: %w", pendingID, err)
	}
	text := "已提交回答：" + pendingID
	if strings.TrimSpace(answer) == "" {
		text += "（空回答）"
	}
	return connectors.CommandResult{Handled: true, Reply: []connectors.OutboundMessage{{Text: text}}}, nil
}

// connectorCommandNotice 构造 Handled=false 的明确文案（核心会原样回给 IM）。
func connectorCommandNotice(text string) connectors.CommandResult {
	return connectors.CommandResult{Handled: false, Reply: []connectors.OutboundMessage{{Text: text}}}
}

// connectorCommandAnswerText 从命令原文中剥离首个参数（序号/问题 id），保留
// 回答内容的内部空白（Args 分词会丢空格，不能直接 join）。
func connectorCommandAnswerText(text, first string) string {
	text = strings.TrimSpace(text)
	if first == "" {
		return text
	}
	if strings.HasPrefix(text, first) {
		rest := text[len(first):]
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n' || rest[0] == '\r' {
			return strings.TrimSpace(rest)
		}
	}
	fields := strings.Fields(text)
	if len(fields) <= 1 {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
}

// ── 注意力出站（FR-8）：运行时事件 → AttentionNotice → IM ────────────────────

const (
	// connectorAttentionQueueSize 是注意力出站缓冲：事件回调不阻塞事件总线
	// 发布方，队列满时丢弃并记日志（IM 出站非可靠队列，后续事件仍会到达）。
	connectorAttentionQueueSize = 64
	// connectorAttentionDedupeCap 是事件级去重窗口（进程内）。
	connectorAttentionDedupeCap = 1024
)

// subscribeRuntimeConnectorAttention 订阅审批/提问/回合结束事件并异步出站到 IM。
// store 非空时按各连接器的 policy.notify（D5）过滤：enabled=false 的连接器不收
// 任何事件通知；scope=sessions（默认）只收绑定到该 bot 的会话，scope=all 收
// 宿主全部会话事件（docs/bots/09-multi-instance-binding.md §7.4.1）。
//
// 去重说明：同一事件（approval/question 的 request id、回合结束的 turn id/时间戳）
// 只出站一次——进程内窗口去重，保证重复订阅/重复投递不会推两条。与 Web Push/ntfy
// 的跨通道统一去重不在本切片（runtime-server 未装配 push/ntfy；gateway 的 notify
// 差分在 gateway 宿主），留给 notify.Dispatcher 切片；此处的键与 gateway_attention
// 使用的事件身份同源，后续可迁移到共享去重账本。
//
// 返回退订函数；ctx 取消（宿主 close）时自动退订并停止 worker。
func subscribeRuntimeConnectorAttention(ctx context.Context, handler *runtimeapi.Handler, store connectors.Store, notify func(context.Context, connectors.AttentionNotice) error, logf func(string, ...any)) func() {
	if handler == nil || notify == nil {
		return func() {}
	}
	dedupe := newConnectorAttentionDeduper(connectorAttentionDedupeCap)
	queue := make(chan connectors.AttentionNotice, connectorAttentionQueueSize)
	stop := make(chan struct{})
	var stopOnce sync.Once
	stopWorker := func() { stopOnce.Do(func() { close(stop) }) }
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case notice := <-queue:
				if err := notify(ctx, notice); err != nil && logf != nil {
					logf("connector attention %s delivery failed (session=%s): %v", notice.Kind, notice.Session.SessionID, err)
				}
			}
		}
	}()
	unsubscribe := handler.SubscribeConnectorAttentionEvents(func(event runtimeevents.Event) {
		notice, key, ok := connectorAttentionNoticeForEvent(event, handler.ConnectorSessionParked)
		if !ok || !dedupe.first(key) {
			return
		}
		// D5 过滤：逐连接器判定（一个事件可能命中多个 bot 的通知开关）。
		if store != nil {
			accepted := false
			for _, cfg := range store.ListConfigs() {
				if !cfg.Enabled {
					continue
				}
				if cfg.Policy.AcceptsNotice(notice, func(session connectors.SessionRef) bool {
					// scope=sessions 的语义：事件会话已绑定到本 bot（按 SessionRef
					// 反查绑定表；绑定数几十~几百，全表扫描可行，见 Store 契约）。
					for _, binding := range store.ListBindings(cfg.ID) {
						if binding.Session == session {
							return true
						}
					}
					return false
				}) {
					accepted = true
					break
				}
			}
			if !accepted {
				return
			}
		}
		select {
		case queue <- notice:
		default:
			if logf != nil {
				logf("connector attention queue full, dropping %s notice (session=%s)", notice.Kind, notice.Session.SessionID)
			}
		}
	})
	if unsubscribe == nil {
		// 事件总线不可用：无订阅，worker 由 ctx/stop 回收。
		return func() { stopWorker() }
	}
	var unsubOnce sync.Once
	unsub := func() {
		unsubOnce.Do(func() {
			unsubscribe()
			stopWorker()
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			unsub()
		case <-stop:
		}
	}()
	return unsub
}

// connectorAttentionNoticeForEvent 把运行时事件映射为 IM 注意力通知；第二个
// 返回值是事件级去重键，ok=false 表示该事件不产生通知。
func connectorAttentionNoticeForEvent(event runtimeevents.Event, parked func(sessionID string) bool) (connectors.AttentionNotice, string, bool) {
	sessionID := strings.TrimSpace(event.SessionID)
	if sessionID == "" {
		return connectors.AttentionNotice{}, "", false
	}
	session := connectors.SessionRef{SessionID: sessionID}
	switch event.Type {
	case runtimechat.EventApprovalRequested:
		requestID := connectorEventPayloadString(event, "request_id")
		text := "⏳ 有待审批的工具调用"
		if tool := connectorEventPayloadString(event, "tool_name"); tool != "" {
			text += "：" + tool
		}
		if reason := connectorEventPayloadString(event, "reason"); reason != "" {
			text += "（" + reason + "）"
		}
		if requestID != "" {
			text += "\n回复 /approve " + requestID + " 或 /deny " + requestID
		} else {
			text += "\n回复 /approve 或 /deny"
		}
		return connectors.AttentionNotice{Kind: connectors.AttentionKindApproval, Session: session, Text: text},
			"approval|" + sessionID + "|" + connectorAttentionIdentity(requestID, event), true
	case runtimechat.EventQuestionAsked:
		questionID := connectorEventPayloadString(event, "question_id")
		text := "❓ 有待回答的提问"
		if prompt := connectorEventPayloadString(event, "prompt"); prompt != "" {
			text += "：" + prompt
		}
		text += "\n回复 /answer 1 <内容>"
		return connectors.AttentionNotice{Kind: connectors.AttentionKindQuestion, Session: session, Text: text},
			"question|" + sessionID + "|" + connectorAttentionIdentity(questionID, event), true
	case runtimeobserve.EventAgentTurnDone:
		text := "✅ 会话回合已完成"
		if parked != nil && parked(sessionID) {
			// §6.12 托管挂起：run 结束 ≠ 托管 turn 结束（与 gateway_attention 同口径）。
			text = "⏳ 回合已收尾，仍在等待子任务（完成后会继续推进）"
		}
		return connectors.AttentionNotice{Kind: connectors.AttentionKindCompletion, Session: session, Text: text},
			"completion|" + sessionID + "|" + connectorAttentionIdentity(connectorEventPayloadString(event, "turn_id"), event), true
	default:
		return connectors.AttentionNotice{}, "", false
	}
}

// connectorAttentionIdentity 返回事件级去重身份：优先业务 id（request/question/
// turn id），缺失时回落到事件时间戳（同一 Event 重复投递仍能被拦截）。
func connectorAttentionIdentity(id string, event runtimeevents.Event) string {
	if id = strings.TrimSpace(id); id != "" {
		return id
	}
	if !event.Timestamp.IsZero() {
		return event.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	return event.Type
}

// connectorEventPayloadString 读取事件载荷中的字符串字段（容忍非字符串值）。
func connectorEventPayloadString(event runtimeevents.Event, key string) string {
	if event.Payload == nil {
		return ""
	}
	value, ok := event.Payload[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

// connectorAttentionDeduper 是进程内事件级去重窗口（FIFO 淘汰）。
type connectorAttentionDeduper struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	order []string
	limit int
}

func newConnectorAttentionDeduper(limit int) *connectorAttentionDeduper {
	if limit <= 0 {
		limit = connectorAttentionDedupeCap
	}
	return &connectorAttentionDeduper{seen: make(map[string]struct{}, limit), limit: limit}
}

// first 报告 key 是否首次出现；非首次返回 false（空 key 视为首次，调用方
// 由映射层保证 key 非空）。
func (d *connectorAttentionDeduper) first(key string) bool {
	if d == nil || key == "" {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[key]; ok {
		return false
	}
	d.seen[key] = struct{}{}
	d.order = append(d.order, key)
	if len(d.order) > d.limit {
		oldest := d.order[0]
		d.order = d.order[1:]
		delete(d.seen, oldest)
	}
	return true
}
