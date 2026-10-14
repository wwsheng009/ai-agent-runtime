// 本文件是 aicli 宿主侧的连接器装配（docs/bots/03-host-integration.md §6、
// docs/bots/04-migration-roadmap.md P0「宿主接线」）：
//
//   - 配置/账本复用 storefile：与 runtime-server 共享同一份 config.yaml 的
//     `connectors:` 节（密钥同目录 connectors.secrets.yaml，0600），
//     绑定/去重账本/轮询游标写同目录 data/connectors/state.json；
//   - Backend 把 IM 消息提交到本机 chat 会话：无绑定时按 IM 会话派生确定性
//     session id，由 localActorRegistry.ensureSession 自动建会话（与设计文档
//     「Binding 默认自动建会话」一致）；P1 再把流式增量接到 actor 事件。
//   - 管理面 REST 挂到 aicli loopback Web 服务器（与 /web/api/* 同一 mux），
//     鉴权沿用 ChatWebAuthGuard（回环/同源/写令牌）；本文件不注册任何路由，
//     只提供 MountLocalConnectorRoutes 由 cmd/aicli 调用。
package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
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
	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// startAICLIChatConnectors 解析共享 config.yaml 的写入路径并装配连接器宿主。
// 路径解析或装配失败都返回错误，由调用方记 warn——chat 启动不受影响
// （「默认零配置可用」：没配连接器时这里什么都不启动）。
func startAICLIChatConnectors(cfg *config.Config, host *localChatRuntimeHost) (*LocalConnectorHost, error) {
	configPath, _, err := resolveAICLIConfigWriteTarget(cfg, "")
	if err != nil {
		setLocalConnectorInitNote("解析共享 config.yaml 路径失败：" + err.Error())
		return nil, err
	}
	if strings.TrimSpace(configPath) == "" {
		setLocalConnectorInitNote("无法解析共享 config.yaml 路径（请检查 aicli 配置或使用 --config 指定）")
		return nil, errors.New("aicli connectors: 无法解析共享 config.yaml 路径")
	}
	// 没有配置文件 = 没有连接器可装配：整段惰性（也避免测试/一次性运行
	// 触发对共享配置的读取与轮询启动）。跳过时记录原因，供 /bots 如实报出。
	if _, statErr := os.Stat(configPath); statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			note := "未找到共享 config.yaml（解析路径：" + configPath + "）；连接器配置存于该文件，创建它并在其中配置 connectors 后重启会话"
			setLocalConnectorInitNote(note)
			logpkg.Infof("aicli connectors: skipped (%s)", note)
			return nil, nil
		}
		setLocalConnectorInitNote("读取共享 config.yaml 失败：" + statErr.Error())
		return nil, statErr
	}
	ctx := context.Background()
	if host != nil && host.lifecycleCtx != nil {
		// 随 chat 宿主生命周期停机（宿主 close 时 lifecycleCtx 取消）。
		ctx = host.lifecycleCtx
	}
	started, startErr := StartLocalConnectors(ctx, host, configPath, logpkg.Infof)
	if startErr != nil {
		setLocalConnectorInitNote("连接器装配失败：" + startErr.Error())
		return nil, startErr
	}
	setLocalConnectorInitNote("")
	return started, nil
}

// ── 路由挂载（cmd/aicli 的 loopback 服务器调用） ──────────────────────────

// MountLocalConnectorRoutes 把连接器管理面与 webhook 入口挂到 aicli 的
// loopback Web 服务器。服务未启动（非 chat 命令 / 装配失败）时请求得到
// 503，诚实表达"本次进程没有运行连接器"，而不是伪装成 404 或空实现。
func MountLocalConnectorRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	// 精确路径 + 子树都要注册：只注册 "/api/connectors/" 时 ServeMux 会把
	// "/api/connectors" 301 到带斜杠路径，POST 会被客户端降级成 GET。
	mux.Handle("/api/connectors", localConnectorRouteHandler{api: true})
	mux.Handle("/api/connectors/", localConnectorRouteHandler{api: true})
	mux.Handle("/hooks/connectors/", localConnectorRouteHandler{api: false})
}

type localConnectorRouteHandler struct {
	api bool
}

func (h localConnectorRouteHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler := currentLocalConnectorHandler(h.api)
	if handler == nil {
		// web 页也可能先于宿主装配到达（页面打开时该会话尚未跑过 /bots、
		// 或启动期装配被跳过）：与 /bots 同款按需自举一次，成功即转发。
		if bootstrapped := bootstrapLocalConnectorsForWeb(); bootstrapped {
			handler = currentLocalConnectorHandler(h.api)
		}
		if handler == nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"error","reason":` + strconv.Quote(localConnectorsUnavailableReason()) + `}`))
			return
		}
	}
	if h.api {
		http.StripPrefix("/api", handler).ServeHTTP(w, r)
		return
	}
	handler.ServeHTTP(w, r)
}

var (
	localConnectorsStateMu sync.Mutex
	localConnectorsAPI     http.Handler
	localConnectorsHooks   http.Handler
	// localConnectorHostRef 是本进程当前已装配的连接器宿主（TUI /bots 副屏
	// 等本机管理面使用）；未装配时为 nil。与 handlers 同锁、同生命周期。
	localConnectorHostRef *LocalConnectorHost
	// localConnectorInitNote 记录连接器未装配/装配失败的具体原因（空 = 正常）：
	// 供 /bots 报出「为什么不可用」，而不是笼统的「未在本会话装配」。
	localConnectorInitNote string
)

func currentLocalConnectorHandler(api bool) http.Handler {
	localConnectorsStateMu.Lock()
	defer localConnectorsStateMu.Unlock()
	if api {
		return localConnectorsAPI
	}
	return localConnectorsHooks
}

func setLocalConnectorHandlers(api, hooks http.Handler) {
	localConnectorsStateMu.Lock()
	localConnectorsAPI, localConnectorsHooks = api, hooks
	localConnectorsStateMu.Unlock()
}

// currentLocalConnectorHost 返回当前会话已装配的连接器宿主（未装配时 nil）。
func currentLocalConnectorHost() *LocalConnectorHost {
	localConnectorsStateMu.Lock()
	defer localConnectorsStateMu.Unlock()
	return localConnectorHostRef
}

func setLocalConnectorHost(host *LocalConnectorHost) {
	localConnectorsStateMu.Lock()
	localConnectorHostRef = host
	localConnectorsStateMu.Unlock()
}

// setLocalConnectorInitNote 记录连接器装配状态说明（空串 = 正常）。
func setLocalConnectorInitNote(note string) {
	localConnectorsStateMu.Lock()
	localConnectorInitNote = strings.TrimSpace(note)
	localConnectorsStateMu.Unlock()
}

// bootstrapLocalConnectorsForWeb 尝试为当前 web 会话按需装配连接器宿主。
// 无活动会话 / 非交互会话 / 装配失败均返回 false（沿用兜底 503 + 原因）。
// 尝试前先清空全局 note：503 原因必须反映**本次**尝试的结果，而不是更早
// 尝试（如 /bots 目录守卫测试的探测）留下的陈旧状态。
func bootstrapLocalConnectorsForWeb() bool {
	if currentLocalConnectorHost() != nil {
		return true
	}
	session := chatWebSession()
	if session == nil {
		return false
	}
	setLocalConnectorInitNote("")
	host, err := startLocalConnectorsOnDemand(session)
	if err != nil || host == nil {
		return false
	}
	return true
}

// localConnectorsUnavailableReason 组装 503 兜底原因：始终以「连接器未在本
// 会话装配」为契约短语开头（页面文案与测试都锚定它），note 只作补充细节。
func localConnectorsUnavailableReason() string {
	if note := localConnectorsInitNote(); note != "" {
		return "连接器未在本会话装配：" + note
	}
	if chatWebSession() == nil {
		return "连接器未在本会话装配（当前没有活动的 aicli chat 会话）"
	}
	return "连接器未在本会话装配（需要交互式 aicli chat，且本会话具备装配条件）"
}

// localConnectorsInitNote 返回连接器未装配/装配失败的原因（空 = 已装配或未尝试）。
func localConnectorsInitNote() string {
	localConnectorsStateMu.Lock()
	defer localConnectorsStateMu.Unlock()
	return localConnectorInitNote
}

// ── 生命周期 ───────────────────────────────────────────────────────────────

// LocalConnectorHost 是 aicli 侧已启动的连接器装配句柄。
type LocalConnectorHost struct {
	service   *connectors.Service
	store     connectors.Store
	cancel    context.CancelFunc
	closeOnce sync.Once
}

// Service 返回本会话已启动的连接器服务（TUI /bots 等本机管理面使用）。
func (h *LocalConnectorHost) Service() *connectors.Service {
	if h == nil {
		return nil
	}
	return h.service
}

// Store 返回连接器配置/状态存储（与 Service 同源，供 /bots 读写配置）。
func (h *LocalConnectorHost) Store() connectors.Store {
	if h == nil {
		return nil
	}
	return h.store
}

// Close 停止全部轮询/队列并卸载管理面路由（幂等）。
func (h *LocalConnectorHost) Close() {
	if h == nil {
		return
	}
	h.closeOnce.Do(func() {
		if h.cancel != nil {
			h.cancel()
		}
		if h.service != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			h.service.Stop(ctx)
		}
		setLocalConnectorHandlers(nil, nil)
		setLocalConnectorHost(nil)
	})
}

// StartLocalConnectors 装配并启动 aicli 连接器宿主：
// storefile(configPath, <configDir>/data/connectors/state.json) + Telegram Provider
// + 本机 chat Backend，并发布管理面/webhook 路由供 loopback 服务器转发。
// 未配置任何连接器时是惰性的（不启动轮询，行为与现状一致）。
func StartLocalConnectors(parent context.Context, host *localChatRuntimeHost, configPath string, logf func(format string, args ...any)) (*LocalConnectorHost, error) {
	if host == nil {
		return nil, errors.New("aicli connectors: chat runtime host is required")
	}
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, errors.New("aicli connectors: config path is required")
	}
	if logf == nil {
		logf = log.Printf
	}
	if parent == nil {
		parent = context.Background()
	}

	// 状态真源（docs/bots/09-multi-instance-binding.md D1）：经 statepath 统一
	// 锚定用户级 ~/.aicli/data/connectors/state.json；禁止按 configPath 推导
	// （分层合并下 runtime-server/aicli 解析到不同分层，推导必然分叉）。
	statePath, statePathErr := statepath.CanonicalStatePath()
	if statePathErr != nil {
		return nil, fmt.Errorf("aicli connectors: resolve canonical state path: %w", statePathErr)
	}
	store := storefile.NewConfigStore(configPath, statePath)
	// 服务层日志：白名单 fail-closed 闸跳过启动是安全告警（消息含
	// "fail-closed"，见 connectors.Service.startConnector），升级为 warn；
	// 其余生命周期日志保持调用方给定的级别（默认 info）。
	serviceLogf := func(format string, args ...any) {
		if strings.Contains(format, "fail-closed") {
			logpkg.Warnf(format, args...)
			return
		}
		logf(format, args...)
	}
	service := connectors.New(connectors.Deps{
		Store:   store,
		Backend: localConnectorBackend{host: host, logf: logf},
		Logf:    serviceLogf,
		// 宿主身份（docs/bots/09 §7.5 切片 A）：实例唯一 HostID =
		// `aicli:<mesh-node-id>`（同机多 TUI 各自唯一，P2 NodeID 亲和的判定键）；
		// mesh 不可用/未入网时退回 `aicli:<pid>`。policy.runtime_owner 准入按
		// HostKind（connectors.HostMatchesPolicy）匹配，存量裸词配置不受影响。
		HostID: connectors.BuildHostID(connectors.HostKindAICLI, aicliHostInstanceID()),
		// Fix-S2：aicli 侧 fail-closed——启用中的连接器若没有非空
		// policy.allowed_users，跳过启动（不轮询），避免对任意平台用户开放。
		RequireAllowedUsers: true,
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

	deps := restapi.Deps{
		Service: service,
		Store:   store,
		// aicli 的管理面鉴权由 loopback 服务器统一承担（ChatWebAuthGuard：
		// 回环/同源/写令牌）；这里的路由只挂在同一 mux 上，不额外暴露面。
		Authorize: func(*http.Request) error { return nil },
		Logf:      logf,
		HostID:    connectors.BuildHostID(connectors.HostKindAICLI, aicliHostInstanceID()),
	}

	ctx, cancel := context.WithCancel(parent)
	if err := service.Start(ctx); err != nil {
		cancel()
		return nil, err
	}
	setLocalConnectorHandlers(restapi.NewRouter(deps), restapi.NewHooksRouter(deps))
	connectorHost := &LocalConnectorHost{service: service, store: store, cancel: cancel}
	setLocalConnectorHost(connectorHost)
	// 注意力出站（FR-8，docs/bots/09 §7.4.1）：与 runtime-server 同一共享实现
	// （internal/connectors.SubscribeAttention，三宿主单一实现点）——订阅
	// approval/question/turn.done，按 policy.notify（D5）过滤后出站到绑定 IM。
	// aicli 无 runtimeapi.Handler，订阅面直连本会话事件总线；托管挂起探测
	// （Parked）aicli 宿主暂无对应面，传 nil（按未挂起处理，completion 文案
	// 与无子任务场景一致）。
	if host.EventBus != nil {
		// aicli 事件总线按类型订阅（SubscribeCancelable(eventType, handler)），
		// 共享层的订阅面是单函数——按 AttentionEventTypes() 清单展开（单一来源：
		// 与共享层聚合订阅同一份清单，runtimeapi.Handler 同款三元组）。
		eventTypes := connectors.AttentionEventTypes()
		connectors.SubscribeAttention(ctx, connectors.AttentionDeps{
			Subscribe: func(handler func(runtimeevents.Event)) func() {
				unsubs := make([]func(), 0, len(eventTypes))
				for _, eventType := range eventTypes {
					if unsub := host.EventBus.SubscribeCancelable(eventType, handler); unsub != nil {
						unsubs = append(unsubs, unsub)
					}
				}
				if len(unsubs) == 0 {
					return nil
				}
				return func() {
					for _, unsub := range unsubs {
						unsub()
					}
				}
			},
			Store:  store,
			Notify: service.NotifyAttention,
			Logf:   logf,
		})
	}
	logf("aicli connectors: started (config=%s, state=%s)", configPath, statePath)
	return connectorHost, nil
}

// aicliHostInstanceID 返回本进程的实例段（HostID = aicli:<instance>）：
// mesh 已入网（main 包 startMeshHost 在进程早期执行）取 mesh node-id——它
// 跨重连稳定且同机多 TUI 唯一；mesh 不可用（--mesh=false / 无网格根目录）
// 退回 "pid-<pid>"（进程唯一，重启换新——与 ProcessNodeID 的不持久化语义一致，
// 正好匹配"重启后旧实例陈旧、新实例接管"的归属模型）。
func aicliHostInstanceID() string {
	if host := mesh.Current(); host != nil {
		if nodeID := strings.TrimSpace(host.NodeID()); nodeID != "" {
			return nodeID
		}
	}
	return fmt.Sprintf("pid-%d", os.Getpid())
}

// ── Backend：IM 消息 → 本机 chat 会话 ──────────────────────────────────────

type localConnectorBackend struct {
	host *localChatRuntimeHost
	logf func(format string, args ...any)
	// commands 是测试注入的命令面替身；nil 时由 host.ActorRegistry 适配。
	commands localConnectorCommandPlane
}

// localConnectorCommandPlane 是 IM 审批/提问命令依赖的 actor 响应入口：
// 生产实现是 localConnectorActorCommands（包装 localActorRegistry），
// 测试可注入替身验证命令转发（无需真实 actor 运行）。
type localConnectorCommandPlane interface {
	ResolveApproval(ctx context.Context, args toolbroker.ResolveAgentApprovalArgs) (*toolbroker.AgentApprovalResult, error)
	AnswerQuestion(ctx context.Context, sessionID, questionID, answer string) error
}

// localConnectorActorCommands 把 localActorRegistry 适配为命令面（与 TUI 同一入口）。
type localConnectorActorCommands struct {
	registry *localActorRegistry
}

func (c localConnectorActorCommands) ResolveApproval(ctx context.Context, args toolbroker.ResolveAgentApprovalArgs) (*toolbroker.AgentApprovalResult, error) {
	return c.registry.ResolveApproval(ctx, args)
}

func (c localConnectorActorCommands) AnswerQuestion(ctx context.Context, sessionID, questionID, answer string) error {
	return c.registry.ConnectorAnswerQuestion(ctx, sessionID, questionID, answer)
}

// commandPlane 解析实际使用的命令面（注入替身优先，其次 host.ActorRegistry）。
func (b localConnectorBackend) commandPlane() localConnectorCommandPlane {
	if b.commands != nil {
		return b.commands
	}
	if b.host == nil || b.host.ActorRegistry == nil {
		return nil
	}
	return localConnectorActorCommands{registry: b.host.ActorRegistry}
}

func (b localConnectorBackend) Name() string { return "aicli" }

// Handle 把一条 IM 消息提交为一次本机 chat turn，并把终态回复回吐给连接器。
// P0 只回终态（占位消息 + Final 编辑由 Service 负责）；流式增量（sink）在 P1
// 接入 actor 事件后补齐。
func (b localConnectorBackend) Handle(ctx context.Context, req connectors.BackendRequest, _ connectors.ReplySink) (connectors.BackendResult, error) {
	if b.host == nil || b.host.ActorRegistry == nil {
		return connectors.BackendResult{}, errors.New("aicli: 本机 chat 运行时未就绪（连接器需要运行在 aicli chat 会话内）")
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return connectors.BackendResult{}, nil
	}
	sessionID := strings.TrimSpace(req.Session.SessionID)
	if sessionID == "" {
		sessionID = localConnectorSessionID(req.ConnectorID, req.Chat)
	}

	// IM 入站没有交互式审批通道：与内部轮次一致使用 bypass_permissions。
	// （白名单/开关由连接器 Policy 在入站时先行过滤。）
	// D6（docs/bots/09 §7.4.2）：连接器策略路由非空时按回合覆盖 provider/
	// model/reasoning effort（与 team 任务路由同一 RunRouteOverride 通道，
	// 与 runtime-server bootstrap 同口径，归一化共用 RouteOverrideFromPolicy）；
	// 空策略走原路径不改变行为。
	runMeta := &team.RunMeta{PermissionMode: "bypass_permissions"}
	routeOverride := connectors.RouteOverrideFromPolicy(req.Policy.Route)
	var result *team.SessionResult
	var err error
	if routeOverride != nil {
		result, err = b.host.ActorRegistry.SubmitPromptWithRoute(ctx, sessionID, text, runMeta, routeOverride)
	} else {
		result, err = b.host.ActorRegistry.SubmitPrompt(ctx, sessionID, text, runMeta)
	}
	if result == nil {
		if err == nil {
			err = errors.New("aicli: empty session result")
		}
		return connectors.BackendResult{Session: connectors.SessionRef{SessionID: sessionID}}, err
	}
	out := connectors.BackendResult{Session: connectors.SessionRef{SessionID: sessionID}}
	if reply := strings.TrimSpace(result.Output); reply != "" {
		out.Reply = []connectors.OutboundMessage{{Text: reply}}
	}
	if err != nil {
		return out, err
	}
	if !result.Success {
		message := strings.TrimSpace(result.Error)
		if message == "" {
			message = "agent run failed"
		}
		return out, errors.New(message)
	}
	return out, nil
}

// localConnectorSessionID 由 IM 会话派生确定性本机会话 id：
// 同一 IM 会话在绑定账本丢失/重建后仍回到同一 agent 会话，避免每次重启
// 都新建会话。字符集收敛到 [A-Za-z0-9-_]（与 session 存储层的规范化一致），
// 长度截断到 80。
func localConnectorSessionID(connectorID, chat string) string {
	raw := strings.Trim(strings.TrimSpace(connectorID)+"-"+strings.TrimSpace(chat), "- ")
	if raw == "" {
		raw = "default"
	}
	var builder strings.Builder
	builder.WriteString("conn-")
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteByte('-')
		}
	}
	id := builder.String()
	if len(id) > 80 {
		id = id[:80]
	}
	return id
}

// ── 审批/提问命令（FR-7）：/approve /deny /answer 的 IM 闭环 ────────────────

// DecideApproval 处理 /approve 与 /deny：解析可选 request_id → 校验它确实是
// 当前挂起项 → 走 localActorRegistry.ResolveApproval（与 Web/TUI 同一 actor 入口）。
// 无挂起项/目标不匹配返回 Handled=false + 明确文案（不报错、不落 turn）。
func (b localConnectorBackend) DecideApproval(ctx context.Context, req connectors.CommandRequest) (connectors.CommandResult, error) {
	sessionID := b.commandSessionID(req)
	commands := b.commandPlane()
	if commands == nil {
		return connectors.CommandResult{}, errors.New("aicli: 本机 chat 运行时未就绪（连接器需要运行在 aicli chat 会话内）")
	}
	allow := !strings.EqualFold(strings.TrimSpace(req.Command), "deny")
	requested := ""
	if len(req.Args) > 0 {
		requested = strings.TrimSpace(req.Args[0])
	}
	pendingID, err := b.connectorPendingApprovalID(ctx, sessionID)
	if err != nil {
		return connectors.CommandResult{}, fmt.Errorf("resolve pending approval: %w", err)
	}
	if pendingID == "" {
		return localConnectorCommandNotice("当前没有待审批项（可能已由其它入口处理）。"), nil
	}
	if requested != "" && requested != pendingID {
		return localConnectorCommandNotice(fmt.Sprintf("request_id 与当前待审批项不匹配：当前待审批项为 %s。", pendingID)), nil
	}
	outcome, err := commands.ResolveApproval(ctx, toolbroker.ResolveAgentApprovalArgs{
		ID:        sessionID,
		RequestID: pendingID,
		Allow:     allow,
	})
	if err != nil {
		return connectors.CommandResult{}, fmt.Errorf("resolve approval %s: %w", pendingID, err)
	}
	if outcome == nil {
		return connectors.CommandResult{}, errors.New("aicli: empty approval result")
	}
	text := "已拒绝：" + pendingID
	if outcome.Allowed {
		text = "已批准：" + pendingID
		if outcome.Resumed {
			text += "（已恢复运行）"
		}
	} else if allow {
		// 批准决定已记录但未生效（原 run 已终态）：按事实报告，不谎报已批准。
		text = "审批未生效（原回合已终止）：" + pendingID
	}
	return connectors.CommandResult{Handled: true, Reply: []connectors.OutboundMessage{{Text: text}}}, nil
}

// AnswerQuestion 处理 /answer：Args[0] 为序号（1 = 当前挂起提问）或 question_id，
// 其余为回答内容（保留内部空白）。无挂起项/目标不匹配返回 Handled=false + 文案。
func (b localConnectorBackend) AnswerQuestion(ctx context.Context, req connectors.CommandRequest) (connectors.CommandResult, error) {
	sessionID := b.commandSessionID(req)
	commands := b.commandPlane()
	if commands == nil {
		return connectors.CommandResult{}, errors.New("aicli: 本机 chat 运行时未就绪（连接器需要运行在 aicli chat 会话内）")
	}
	pendingID, err := b.connectorPendingQuestionID(ctx, sessionID)
	if err != nil {
		return connectors.CommandResult{}, fmt.Errorf("resolve pending question: %w", err)
	}
	if pendingID == "" {
		return localConnectorCommandNotice("当前没有待回答的问题（可能已回答或已过期）。"), nil
	}
	ref := ""
	if len(req.Args) > 0 {
		ref = strings.TrimSpace(req.Args[0])
	}
	switch {
	case ref == "", ref == "1", ref == pendingID:
		// 单挂起提问模型：序号 1 与 question_id 都指向当前挂起项。
	default:
		return localConnectorCommandNotice(fmt.Sprintf("序号/问题 id 与当前待回答的问题不匹配（当前问题：%s；可用 /answer 1 <内容>）。", pendingID)), nil
	}
	answer := localConnectorAnswerText(req.Text, ref)
	if err := commands.AnswerQuestion(ctx, sessionID, pendingID, answer); err != nil {
		return connectors.CommandResult{}, fmt.Errorf("answer question %s: %w", pendingID, err)
	}
	text := "已提交回答：" + pendingID
	if strings.TrimSpace(answer) == "" {
		text += "（空回答）"
	}
	return connectors.CommandResult{Handled: true, Reply: []connectors.OutboundMessage{{Text: text}}}, nil
}

// commandSessionID 解析命令目标会话：优先绑定会话，未绑定时回落确定性派生 id
// （与 Handle 的自动建会话同源；查无挂起项时自然返回 Handled=false）。
func (b localConnectorBackend) commandSessionID(req connectors.CommandRequest) string {
	if sessionID := strings.TrimSpace(req.Session.SessionID); sessionID != "" {
		return sessionID
	}
	return localConnectorSessionID(req.ConnectorID, req.Chat)
}

// connectorPendingApprovalID / connectorPendingQuestionID 读本机会话 durable
// 运行态（与 runtime-server 同源口径）；RuntimeStore 未装配时回落到 actor 内存态。
func (b localConnectorBackend) connectorPendingApprovalID(ctx context.Context, sessionID string) (string, error) {
	state, err := b.connectorRuntimeState(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if state == nil || state.PendingApproval == nil {
		return "", nil
	}
	return strings.TrimSpace(state.PendingApproval.ID), nil
}

func (b localConnectorBackend) connectorPendingQuestionID(ctx context.Context, sessionID string) (string, error) {
	state, err := b.connectorRuntimeState(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if state == nil || state.PendingQuestion == nil {
		return "", nil
	}
	return strings.TrimSpace(state.PendingQuestion.ID), nil
}

func (b localConnectorBackend) connectorRuntimeState(ctx context.Context, sessionID string) (*runtimechat.RuntimeState, error) {
	if b.host == nil {
		return nil, errors.New("aicli: 本机 chat 运行时未就绪")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	if store := b.host.RuntimeStore; store != nil {
		return runtimechat.LoadRuntimeStateForInspection(ctx, store, sessionID)
	}
	if b.host.SessionHub == nil {
		return nil, errors.New("aicli: session hub not configured")
	}
	actor, err := b.host.SessionHub.GetOrCreate(sessionID)
	if err != nil {
		return nil, err
	}
	return actor.StateForInspection(), nil
}

// localConnectorCommandNotice 构造 Handled=false 的明确文案（核心会原样回给 IM）。
func localConnectorCommandNotice(text string) connectors.CommandResult {
	return connectors.CommandResult{Handled: false, Reply: []connectors.OutboundMessage{{Text: text}}}
}

// localConnectorAnswerText 从命令原文剥离首个参数（序号/问题 id），保留回答内容
// 的内部空白（Args 分词会丢空格，不能直接 join）。
func localConnectorAnswerText(text, first string) string {
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

// ConnectorAnswerQuestion 是 IM /answer 命令的 actor 入口：与 ResolveApproval
// 同源地解析目标会话后调用 actor.AnswerQuestion（与 TUI/Web 同一入口）。
func (r *localActorRegistry) ConnectorAnswerQuestion(ctx context.Context, sessionID, questionID, answer string) error {
	if r == nil || r.Host == nil || r.Host.SessionHub == nil {
		return errors.New("session hub not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session id is required")
	}
	questionID = strings.TrimSpace(questionID)
	if questionID == "" {
		return errors.New("question id is required")
	}
	resolvedSessionID, err := r.resolveLocalAgentTargetSessionID(ctx, sessionID)
	if err != nil {
		return err
	}
	sessionID = resolvedSessionID
	if err := r.ensureSession(ctx, sessionID); err != nil {
		return err
	}
	actor, err := r.Host.SessionHub.GetOrCreate(sessionID)
	if err != nil {
		return err
	}
	return actor.AnswerQuestion(ctx, questionID, answer)
}
