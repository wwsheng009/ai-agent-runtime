package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/providers/feishu"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/providers/qqbot"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/providers/telegram"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/providers/wechat"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/restapi"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/statepath"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/storefile"
	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
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
		// 宿主身份（docs/bots/09 §7.5 切片 A）：runtime-server 单进程部署即唯一
		// 实例，裸 kind HostID 已满足实例唯一（D2 NodeID 亲和的目标形态）；
		// policy.runtime_owner 准入按 HostKind（HostMatchesPolicy）匹配。
		HostID: "runtime-server",
		// Fix-S2：runtime-server 保持兼容（false，不 fail-closed），
		// 空白名单的可见性告警在下面单独发。
		RequireAllowedUsers: false,
		// P2 切片 B 后半（docs/bots/09 §7.2/§7.3）：mesh peers 视图与远程派发
		// 实装——runner 候选从 node record 档案读（runtime-server 尚未写自身
		// 档案，peers 含其它 aicli 节点；self 档案在入网切片补写后自动加入），
		// 派发 POST 对端 /api/connectors/dispatch（管理面令牌，与 restapi 鉴权
		// 同源）。mesh 档案不可读 = 单机模式（Peers 返回空）。
		Peers:          meshRunnerPeers(logf),
		RemoteDispatch: dispatchToRunnerPeer(logf),
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
		// runner 接收面（P2 切片 B 后半）：POST /connectors/dispatch 执行后端
		// （prompt 注入走 SubmitConnectorPrompt、命令回环走既有命令面）。
		Runner: runtimeConnectorRunner{handler: handler},
	}
	mountConnectorRoutes(router, deps)

	if err := service.Start(ctx); err != nil {
		return nil, err
	}
	// 注意力出站（FR-7/FR-8）：订阅 approval_requested / question_asked /
	// agent.turn.finished，映射为 AttentionNotice 异步出站到该会话的全部 IM 绑定
	// （NotifySession 内部反查绑定，无绑定即 no-op）。订阅随宿主 ctx
	// （进程生命周期 / close 时取消）退订。
	// 实现在 internal/connectors.SubscribeAttention（三宿主单一实现点，
	// docs/bots/09 §7.4.1；runtime-server 借 runtimeapi.Handler 的订阅面与
	// §6.12 托管挂起探测接入）。
	connectors.SubscribeAttention(ctx, connectors.AttentionDeps{
		Subscribe: handler.SubscribeConnectorAttentionEvents,
		Parked:    handler.ConnectorSessionParked,
		Store:     store,
		Notify: func(notifyCtx context.Context, notice connectors.AttentionNotice) error {
			return service.NotifyAttention(notifyCtx, notice)
		},
		Logf: logf,
	})
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

// runtimeConnectorRunner 是本宿主作为 runner 接收跨节点派发的执行面
// （P2 切片 B 后半，docs/bots/09 §7.3）：prompt 注入走 SubmitConnectorPrompt
// （与本地 Backend.Handle 同一执行路径）；命令回环（is_command）按命令词
// 路由到既有 ConnectorDecideApproval / ConnectorAnswerQuestion。
//UserID/白名单：runner 侧重查本机生效 policy.allowed_users 由 Service 层
// HandleInbound 语义保证（派发体携带原始 user_id，Dispatcher 前置已过滤；
// 二次防线见下 dispatchTurn 内 fail-closed 注释）。
type runtimeConnectorRunner struct {
	handler runtimeConnectorCommandHost
}

func (r runtimeConnectorRunner) DispatchTurn(ctx context.Context, req connectors.DispatchRequest) (connectors.DispatchResult, error) {
	sessionID := strings.TrimSpace(req.Session.SessionID)
	if req.Command {
		return r.dispatchCommand(ctx, req)
	}
	turn, err := r.handler.SubmitConnectorPrompt(ctx, runtimeapi.ConnectorTurnRequest{
		SessionID: sessionID,
		UserID:    strings.TrimSpace(req.UserID),
		Prompt:    strings.TrimSpace(req.Text),
	})
	if err != nil {
		return connectors.DispatchResult{}, err
	}
	if turn == nil {
		return connectors.DispatchResult{}, errors.New("runtime-server: empty dispatch turn result")
	}
	result := connectors.DispatchResult{
		Session: connectors.SessionRef{NodeID: connectors.HostKindRuntimeServer, SessionID: turn.SessionID},
	}
	if reply := strings.TrimSpace(turn.Reply); reply != "" {
		result.Reply = []connectors.OutboundMessage{{Text: reply}}
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

// dispatchCommand 解析派发体里的命令文本（"/approve [id]" / "/deny [id]" /
// "/answer <1|id> <内容>"），路由到既有命令面。与 runtimeapi connector 命令
// 契约同形（poller 侧 Commander 已把命令词归一，这里做兜底解析）。
func (r runtimeConnectorRunner) dispatchCommand(ctx context.Context, req connectors.DispatchRequest) (connectors.DispatchResult, error) {
	sessionID := strings.TrimSpace(req.Session.SessionID)
	fields := strings.Fields(strings.TrimSpace(req.Text))
	if len(fields) == 0 {
		return connectors.DispatchResult{}, errors.New("空命令")
	}
	switch strings.ToLower(strings.TrimPrefix(fields[0], "/")) {
	case "approve", "deny":
		allow := !strings.EqualFold(strings.TrimPrefix(fields[0], "/"), "deny")
		requestID := ""
		if len(fields) > 1 {
			requestID = fields[1]
		}
		outcome, err := r.handler.ConnectorDecideApproval(ctx, sessionID, requestID, allow)
		if err != nil {
			return connectors.DispatchResult{}, err
		}
		// 文案与 aicli DecideApproval 同风格（事实报告，不谎报）。
		text := "已拒绝：" + outcome.RequestID
		if outcome.Allowed {
			text = "已批准：" + outcome.RequestID
			if outcome.Resumed {
				text += "（已恢复运行）"
			}
		} else if allow {
			text = "审批未生效（原回合已终止）：" + outcome.RequestID
		}
		return connectors.DispatchResult{
			Session: connectors.SessionRef{NodeID: connectors.HostKindRuntimeServer, SessionID: sessionID},
			Reply:   []connectors.OutboundMessage{{Text: text}},
		}, nil
	case "answer":
		if len(fields) < 3 {
			return connectors.DispatchResult{}, errors.New("/answer 需要序号与回答内容")
		}
		questionID, answer := fields[1], strings.TrimSpace(strings.Join(fields[2:], " "))
		if err := r.handler.ConnectorAnswerQuestion(ctx, sessionID, questionID, answer); err != nil {
			return connectors.DispatchResult{}, err
		}
		return connectors.DispatchResult{
			Session: connectors.SessionRef{NodeID: connectors.HostKindRuntimeServer, SessionID: sessionID},
			Reply:   []connectors.OutboundMessage{{Text: "已回答。"}},
		}, nil
	default:
		return connectors.DispatchResult{}, fmt.Errorf("不支持的派发命令：%s", fields[0])
	}
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
	// D6：连接器策略路由按回合覆盖（与 aicli 同口径，归一化共用
	// connectors.RouteOverrideFromPolicy——零策略为 nil，此处零值结构体等价）。
	routeOverride := connectors.RouteOverrideFromPolicy(req.Policy.Route)
	routeReq := runtimeapi.ConnectorRouteOverride{}
	if routeOverride != nil {
		routeReq = runtimeapi.ConnectorRouteOverride{
			Provider:        routeOverride.Provider,
			Model:           routeOverride.Model,
			ReasoningEffort: routeOverride.ReasoningEffort,
		}
	}
	turn, err := b.handler.SubmitConnectorPrompt(ctx, runtimeapi.ConnectorTurnRequest{
		SessionID: req.Session.SessionID,
		UserID:    req.UserID,
		Prompt:    text,
		OnDelta:   onDelta,
		Route:     routeReq,
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

// meshPeerLookup 按 NodeID 从 mesh 档案取 endpoint 与写令牌（派发请求的
// 携带凭据；令牌只在内存流转，不落日志）。档案不可读返回空（调用方报
// 明确错误，由 Service 回落本机）。
func meshPeerLookup(nodeID string) (baseURL, token string) {
	paths := mesh.ResolvePaths()
	if !paths.Enabled() {
		return "", ""
	}
	view := mesh.BuildView(paths, mesh.ViewOptions{})
	for _, node := range view.Nodes {
		if !strings.EqualFold(strings.TrimSpace(node.NodeID), strings.TrimSpace(nodeID)) {
			continue
		}
		if node.Endpoint == nil || strings.TrimSpace(node.Endpoint.BaseURL) == "" {
			return "", ""
		}
		if node.Record != nil && node.Record.Auth != nil {
			return node.Endpoint.BaseURL, strings.TrimSpace(node.Record.Auth.Token)
		}
		return node.Endpoint.BaseURL, ""
	}
	return "", ""
}

func meshPeerBaseURL(nodeID string) string {
	base, _ := meshPeerLookup(nodeID)
	return base
}

func meshPeerToken(nodeID string) string {
	_, token := meshPeerLookup(nodeID)
	return token
}

// dispatchErrorLine 把错误响应体压成单行短文本（不把整页错误塞进 IM 回复）。
func dispatchErrorLine(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return "（空响应体）"
	}
	if line, _, ok := strings.Cut(body, "\n"); ok {
		body = strings.TrimSpace(line)
	}
	if len(body) > 200 {
		body = body[:200] + "…"
	}
	return body
}

// ── 注意力出站（FR-8）：运行时事件 → AttentionNotice → IM ────────────────────


// ── P2 切片 B 后半：mesh peers 视图与远程派发（docs/bots/09 §7.2/§7.3）─────

// meshRunnerPeers 从 mesh node record 档案装配 runner 候选（只读 BuildView；
// 本机 runtime-server 未写自身档案，peers 天然只含其它节点）。档案不可读 =
// 空 peers = 单机模式（Service.decideRoute 恒本机，行为与历史一致）。
// 候选过滤：kind ∈ {aicli, runtime-server}（连接器 runner 词表）、state=live、
// 有可达 endpoint。
func meshRunnerPeers(logf func(format string, args ...any)) func() []connectors.RunnerPeer {
	return func() []connectors.RunnerPeer {
		paths := mesh.ResolvePaths()
		if !paths.Enabled() {
			return nil
		}
		view := mesh.BuildView(paths, mesh.ViewOptions{})
		out := make([]connectors.RunnerPeer, 0, len(view.Nodes))
		for _, node := range view.Nodes {
			kind := strings.ToLower(strings.TrimSpace(node.Kind))
			if kind != connectors.MeshPeerKindAICLI && kind != connectors.MeshPeerKindRuntimeServer {
				continue
			}
			if node.State != mesh.NodeStateLive {
				continue
			}
			if node.Endpoint == nil || strings.TrimSpace(node.Endpoint.BaseURL) == "" {
				continue
			}
			alive := true
			out = append(out, connectors.RunnerPeer{
				HostKind: kind,
				NodeID:   node.NodeID,
				Alive:    alive,
			})
		}
		_ = logf
		return out
	}
}

// dispatchToRunnerPeer 把派发请求 POST 到目标节点的
// /api/connectors/dispatch（管理面端点；鉴权用节点档案里的写令牌——与
// 隧道 agent 注入 X-AICLI-Token 的口径一致，令牌不出 mesh 档案）。
// v1 只回终态；网络/HTTP 错误原样返回（Service.dispatchRemote 负责回落本机）。
func dispatchToRunnerPeer(logf func(format string, args ...any)) func(context.Context, connectors.DispatchRequest) (connectors.DispatchResult, error) {
	client := &http.Client{Timeout: connectors.DispatchCallTimeout}
	return func(ctx context.Context, req connectors.DispatchRequest) (connectors.DispatchResult, error) {
		baseURL := meshPeerBaseURL(req.Target.NodeID)
		if baseURL == "" {
			return connectors.DispatchResult{}, fmt.Errorf("节点 %s 无可达 endpoint（档案缺失或掉线）", req.Target.NodeID)
		}
		token := meshPeerToken(req.Target.NodeID)
		body := map[string]any{
			"connector_id": req.ConnectorID,
			"chat":         req.Chat,
			"text":         req.Text,
			"user_id":      req.UserID,
			"user_name":    req.UserName,
			"message_id":   req.MessageID,
			"session_ref":  map[string]string{"node_id": req.Target.NodeID, "session_id": req.Session.SessionID},
			"is_command":   req.Command,
		}
		payload, err := json.Marshal(body)
		if err != nil {
			return connectors.DispatchResult{}, err
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			strings.TrimRight(baseURL, "/")+"/api/connectors/dispatch", bytes.NewReader(payload))
		if err != nil {
			return connectors.DispatchResult{}, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if token != "" {
			httpReq.Header.Set("X-AICLI-Token", token)
		}
		resp, err := client.Do(httpReq)
		if err != nil {
			return connectors.DispatchResult{}, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return connectors.DispatchResult{}, err
		}
		if resp.StatusCode != http.StatusOK {
			return connectors.DispatchResult{}, fmt.Errorf("节点 %s 返回 HTTP %d：%s", req.Target.NodeID, resp.StatusCode, dispatchErrorLine(string(data)))
		}
		var wire struct {
			Reply []connectors.OutboundMessage `json:"reply"`
			Session struct {
				NodeID    string `json:"node_id"`
				SessionID string `json:"session_id"`
			} `json:"session"`
			Success bool   `json:"success"`
			Error   string `json:"error,omitempty"`
		}
		if err := json.Unmarshal(data, &wire); err != nil {
			return connectors.DispatchResult{}, fmt.Errorf("节点 %s 响应不是合法 JSON：%w", req.Target.NodeID, err)
		}
		if !wire.Success {
			return connectors.DispatchResult{}, fmt.Errorf("节点 %s 派发失败：%s", req.Target.NodeID, strings.TrimSpace(wire.Error))
		}
		return connectors.DispatchResult{
			Reply:   wire.Reply,
			Session: connectors.SessionRef{NodeID: wire.Session.NodeID, SessionID: wire.Session.SessionID},
		}, nil
	}
}
