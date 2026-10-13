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
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/providers/qqbot"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/providers/telegram"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/providers/wechat"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/restapi"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/storefile"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// startAICLIChatConnectors 解析共享 config.yaml 的写入路径并装配连接器宿主。
// 路径解析或装配失败都返回错误，由调用方记 warn——chat 启动不受影响
// （「默认零配置可用」：没配连接器时这里什么都不启动）。
func startAICLIChatConnectors(cfg *config.Config, host *localChatRuntimeHost) (*LocalConnectorHost, error) {
	configPath, _, err := resolveAICLIConfigWriteTarget(cfg, "")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(configPath) == "" {
		return nil, errors.New("aicli connectors: 无法解析共享 config.yaml 路径")
	}
	// 没有配置文件 = 没有连接器可装配：整段惰性（也避免测试/一次性运行
	// 触发对共享配置的读取与轮询启动）。
	if _, statErr := os.Stat(configPath); statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, statErr
	}
	ctx := context.Background()
	if host != nil && host.lifecycleCtx != nil {
		// 随 chat 宿主生命周期停机（宿主 close 时 lifecycleCtx 取消）。
		ctx = host.lifecycleCtx
	}
	return StartLocalConnectors(ctx, host, configPath, logpkg.Infof)
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
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"error","reason":"连接器未在本会话装配（需要运行 aicli chat 且连接器初始化成功）"}`))
		return
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

// ── 生命周期 ───────────────────────────────────────────────────────────────

// LocalConnectorHost 是 aicli 侧已启动的连接器装配句柄。
type LocalConnectorHost struct {
	service   *connectors.Service
	cancel    context.CancelFunc
	closeOnce sync.Once
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

	statePath := filepath.Join(filepath.Dir(configPath), "data", "connectors", "state.json")
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

	deps := restapi.Deps{
		Service: service,
		Store:   store,
		// aicli 的管理面鉴权由 loopback 服务器统一承担（ChatWebAuthGuard：
		// 回环/同源/写令牌）；这里的路由只挂在同一 mux 上，不额外暴露面。
		Authorize: func(*http.Request) error { return nil },
		Logf:      logf,
	}

	ctx, cancel := context.WithCancel(parent)
	if err := service.Start(ctx); err != nil {
		cancel()
		return nil, err
	}
	setLocalConnectorHandlers(restapi.NewRouter(deps), restapi.NewHooksRouter(deps))
	logf("aicli connectors: started (config=%s, state=%s)", configPath, statePath)
	return &LocalConnectorHost{service: service, cancel: cancel}, nil
}

// ── Backend：IM 消息 → 本机 chat 会话 ──────────────────────────────────────

type localConnectorBackend struct {
	host *localChatRuntimeHost
	logf func(format string, args ...any)
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
	result, err := b.host.ActorRegistry.SubmitPrompt(ctx, sessionID, text, &team.RunMeta{PermissionMode: "bypass_permissions"})
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
