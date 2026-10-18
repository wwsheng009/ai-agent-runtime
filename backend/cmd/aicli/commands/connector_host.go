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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/configaudit"
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
	// 进程单例（2026-10-15 重复投递复盘）：已有宿主（例如 web 懒自举或按需
	// 装配先于 chat 启动完成）直接复用，绝不再装配第二套事件订阅。
	if existing := currentLocalConnectorHost(); existing != nil && existing.Service() != nil {
		setLocalConnectorInitNote("")
		return existing, nil
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
	// 启动审计（2026-10-15 真机 qq-bot1）：候选配置层之间出现同 id 密钥分叉时
	// 告警——层选择随启动目录变化，分叉会让「重启后错误复现」难以自查。
	configaudit.WarnSecretsDivergence(config.ConfigLayerSearchPaths(), logpkg.Warnf)
	ctx := context.Background()
	if host != nil && host.lifecycleCtx != nil {
		// 随 chat 宿主生命周期停机（宿主 close 时 lifecycleCtx 取消）。
		ctx = host.lifecycleCtx
	}
	started, _, startErr := ensureLocalConnectorHost(ctx, host, configPath, logpkg.Infof)
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

// localConnectorStartMu 序列化连接器宿主的装配（进程单例闸）：
// chat 启动路径（startAICLIChatConnectors）、TUI /bots 按需路径
// （startLocalConnectorsOnDemand）与 web 懒自举（bootstrapLocalConnectorsForWeb）
// 可能在启动窗口内并发到达；check-then-act 竞态会装配出两套宿主，两套事件订阅
// 随后对同一事件各投递一次（2026-10-15 qq-bot1 content/notice 双份复盘）。
var localConnectorStartMu sync.Mutex

// aicliConnectorTurnActivity 是进程级「IM 来源回合进行中」登记器（L3 判据，
// docs/bots/13 §4.2）：由本进程装配的全部宿主实例共享——即使宿主被意外重复
// 装配，任一实例的镜像都能看到进行中的 IM 回合，避免回复路径与镜像双发。
var aicliConnectorTurnActivity = connectors.NewTurnActivityTracker()

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

// ensureLocalConnectorHost 是连接器宿主的进程单例装配闸：持锁后复查全局引用，
// 已有宿主直接复用（reused=true，不重复装配/不重复注册事件订阅），否则串行
// 装配一套新宿主。返回 (host, reused, err)。
func ensureLocalConnectorHost(ctx context.Context, host *localChatRuntimeHost, configPath string, logf func(format string, args ...any)) (*LocalConnectorHost, bool, error) {
	localConnectorStartMu.Lock()
	defer localConnectorStartMu.Unlock()
	if existing := currentLocalConnectorHost(); existing != nil && existing.Service() != nil && existing.Store() != nil {
		return existing, true, nil
	}
	started, err := StartLocalConnectors(ctx, host, configPath, logf)
	if err != nil {
		return nil, false, err
	}
	return started, false, nil
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
	// 服务层日志：RequireAllowedUsers=true 的宿主（需要更严部署的显式配置）
	// 在 fail-closed 闸跳过启动时会产生含 "fail-closed" 的安全告警，升级为
	// warn；其余生命周期日志保持调用方给定的级别（默认 info）。
	// 2026-10-15 起默认宿主（含 aicli）停用该闸：空白名单 = 不限制用户，
	// 启动不再依赖 allowed_users。
	serviceLogf := func(format string, args ...any) {
		if strings.Contains(format, "fail-closed") {
			logpkg.Warnf(format, args...)
			return
		}
		logf(format, args...)
	}
	// S1a 内容镜像：IM 来源回合登记器（L3）与后端共享（docs/bots/13 §4.2；
	// 共享实现随 S1b 提为 connectors.TurnActivityTracker）。进程级单例：
	// 即便宿主被意外重复装配，任一实例的镜像都能看到进行中的 IM 回合。
	turns := aicliConnectorTurnActivity
	// 宿主实例身份在 Service 亲和判定、Backend 会话引用回写与 runner 回执三处
	// 必须同一取值：单点计算后注入，避免各处独立计算漂移（2026-10-17 真机
	// qq-bot1：Backend 未回填 NodeID 导致绑定被抹空）。
	hostID := connectors.BuildHostID(connectors.HostKindAICLI, aicliHostInstanceID())
	backend := localConnectorBackend{host: host, logf: logf, turns: turns, hostID: hostID}
	service := connectors.New(connectors.Deps{
		Store:   store,
		Backend: backend,
		Logf:    serviceLogf,
		// 宿主身份（docs/bots/09 §7.5 切片 A）：实例唯一 HostID =
		// `aicli:<mesh-node-id>`（同机多 TUI 各自唯一，P2 NodeID 亲和的判定键）；
		// mesh 不可用/未入网时退回 `aicli:<pid>`。policy.runtime_owner 准入按
		// HostKind（connectors.HostMatchesPolicy）匹配，存量裸词配置不受影响。
		HostID: hostID,
		// 白名单策略（2026-10-15 调整）：allowed_users 为空 = 不限制（放行所有
		// 平台用户），非空 = 仅命中清单的用户可对话。此前 aicli 侧 fail-closed
		//（空白名单跳过启动）会让新连接器既不轮询也无法通过 IM 首条消息拿到
		// 平台用户标识来补白名单（如 QQ 的 user_openid 对用户不可预知），运维
		// 死锁；现与 runtime-server/gateway 同口径停用该闸（RequireAllowedUsers
		// 仍保留，需要更严部署的宿主可显式置回 true）。
		RequireAllowedUsers: false,
		// P2 切片 B 后半（docs/bots/09 §7.2/§7.3）：mesh peers 视图与远程派发
		// 实装（读 node record 档案 + POST 对端 /api/connectors/dispatch）。
		Peers:          aicliMeshRunnerPeers(logf),
		RemoteDispatch: aicliDispatchToRunnerPeer(logf),
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
		HostID:    hostID,
		// runner 接收面（P2 切片 B 后半）：POST /connectors/dispatch 执行后端
		// （prompt 注入走 ActorRegistry、命令回环走 commandPlane）。
		Runner: localConnectorRunner{backend: backend},
		// 注意力接收面（P2 切片 D）：POST /connectors/attention 本地 D5 过滤
		// + 出站（InletAttention 共享实现）。
		AttentionInlet: connectors.InletAttention(store, service.NotifyAttentionScoped),
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
		// 与共享层聚合订阅同一份清单，含 approval/question/completion 与工作类事件；
		// runtimeapi.Handler 走同款清单 SubscribeConnectorAttentionEvents）。
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
			Store: store,
			// D5 完整投递（2026-10-14 修复）：绑定会话精确投递，scope=all 未绑定
			// 事件回退广播到接受连接器的绑定 chats。
			NotifyScoped: service.NotifyAttentionScoped,
			// digest 聚合投递口（S4，13 §7.1）：窗口内 completion 合并为一条广播。
			NotifyBroadcast: service.NotifyAttentionBroadcast,
			// 成功投递审计（S4，13 §7.4）：逐连接器落 ConnectorLog。
			Audit: service.RecordAttentionDelivery,
			// digest 聚合广播成功投递审计（S4 指标面切片）：批内会话数落 ConnectorLog。
			AuditBroadcast: service.RecordAttentionDigestDelivery,
			// 跨宿主扇出（P2 切片 D）：scope 意愿命中的事件转发给活 peers。
			Forward: aicliForwardAttentionToPeers(logf),
			Logf:    logf,
		})

		// S1a 内容镜像（docs/bots/13 §8）：绑定会话的终态正文作为副本投递到
		// 该 bot 的绑定 chats（订阅平面；policy.content_mirror 默认关）。
		// L3：IM 来源回合由 turns 登记并被跳过（正文已由回复路径投递）；
		// 投递经 service.EnqueueOutbound（per-chat 队列，与 reply 同键保序，
		// 13 §3.7#3）。
		mirrorEventTypes := []string{runtimechat.EventAssistantMessage, "assistant.message"}
		connectors.SubscribeContentMirror(ctx, connectors.ContentMirrorDeps{
			Subscribe: func(handler func(runtimeevents.Event)) func() {
				unsubs := make([]func(), 0, len(mirrorEventTypes))
				for _, eventType := range mirrorEventTypes {
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
			Store:      store,
			TurnActive: turns.Active,
			Audit:      service.RecordSubscriptionDelivery,
			Deliver: func(deliverCtx context.Context, connectorID, chat, text string) error {
				return service.EnqueueOutbound(deliverCtx, connectorID, chat, []connectors.OutboundMessage{{Text: text}})
			},
			Logf: logf,
		})

		// S4 流式订阅（docs/bots/13 §7.1）：assistant 增量 → 占位消息 + 节流编辑
		// （≥1s），终态正文做最终编辑；provider 不支持流式时退化为一次性 content
		// 投递（经 per-chat 队列）。L3 与 content 镜像同（IM 回合整回合跳过）。
		streamEventTypes := []string{
			runtimechat.EventAssistantDelta, "assistant.delta",
			runtimechat.EventAssistantMessage, "assistant.message",
		}
		connectors.SubscribeStreamMirror(ctx, connectors.StreamMirrorDeps{
			Subscribe: func(handler func(runtimeevents.Event)) func() {
				unsubs := make([]func(), 0, len(streamEventTypes))
				for _, eventType := range streamEventTypes {
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
			Store:      store,
			TurnActive: turns.Active,
			Start:      service.NewSubscriptionStream,
			Deliver: func(deliverCtx context.Context, connectorID, chat, text string) error {
				return service.EnqueueOutbound(deliverCtx, connectorID, chat, []connectors.OutboundMessage{{Text: text}})
			},
			Audit: service.RecordStreamDelivery,
			Logf:  logf,
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

// localConnectorRunner 是 aicli 宿主作为 runner 接收跨节点派发的执行面
// （P2 切片 B 后半，docs/bots/09 §7.3）：prompt 注入复用 localConnectorBackend
// 的 Handle 路径（auto-create/续用语义同源）；命令回环（is_command）复用
// commandPlane（ResolveApproval/AnswerQuestion，与 IM 命令同一入口）。
type localConnectorRunner struct {
	backend localConnectorBackend
}

func (r localConnectorRunner) DispatchTurn(ctx context.Context, req connectors.DispatchRequest) (connectors.DispatchResult, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return connectors.DispatchResult{}, errors.New("aicli runner: 空 prompt")
	}
	sessionID := strings.TrimSpace(req.Session.SessionID)
	if sessionID == "" {
		// auto-create：按 (connector, chat) 派生确定性会话（与本地 Backend 同口径）。
		sessionID = localConnectorSessionID(req.ConnectorID, req.Chat)
	}
	// 回执 Session.NodeID 必须是本 runner 的实例唯一 HostID（不是裸 "aicli"）：
	// poller 侧 processMessage 用它回写绑定（service.go 回写路径），形态错乱会
	// 污染绑定账本的下一次路由匹配（2026-10-15 qq-bot1 第三轮复盘）。
	nodeID := r.backend.localHostID()
	if req.Command {
		return r.dispatchCommand(ctx, req, sessionID)
	}
	result, err := r.backend.Handle(ctx, connectors.BackendRequest{
		ConnectorID: req.ConnectorID,
		Chat:        req.Chat,
		Session:     connectors.SessionRef{NodeID: nodeID, SessionID: sessionID},
		Text:        text,
		UserID:      req.UserID,
		UserName:    req.UserName,
		MessageID:   req.MessageID,
	}, nil)
	if err != nil {
		return connectors.DispatchResult{Session: connectors.SessionRef{NodeID: nodeID, SessionID: sessionID}}, err
	}
	return connectors.DispatchResult{
		Reply:   result.Reply,
		Session: connectors.SessionRef{NodeID: nodeID, SessionID: sessionID},
	}, nil
}

// dispatchCommand 解析派发命令（/approve /deny /answer），路由到
// DecideApproval/AnswerQuestion（与 IM 命令同一入口：pending 解析、
// request_id 校验、文案组装全部复用）。
func (r localConnectorRunner) dispatchCommand(ctx context.Context, req connectors.DispatchRequest, sessionID string) (connectors.DispatchResult, error) {
	fields := strings.Fields(strings.TrimSpace(req.Text))
	if len(fields) == 0 {
		return connectors.DispatchResult{}, errors.New("aicli runner: 空命令")
	}
	ok := func(reply connectors.CommandResult, err error) (connectors.DispatchResult, error) {
		if err != nil {
			return connectors.DispatchResult{}, err
		}
		text := ""
		if len(reply.Reply) > 0 {
			text = reply.Reply[0].Text
		}
		return connectors.DispatchResult{
			Session: connectors.SessionRef{NodeID: connectors.HostKindAICLI, SessionID: sessionID},
			Reply:   []connectors.OutboundMessage{{Text: text}},
		}, nil
	}
	switch strings.ToLower(strings.TrimPrefix(fields[0], "/")) {
	case "approve", "deny":
		// 复用 DecideApproval（含 pending 解析/request_id 校验/文案组装，
		// 与 IM 命令同一入口）。
		return ok(r.backend.DecideApproval(ctx, connectors.CommandRequest{
			ConnectorID: req.ConnectorID,
			Chat:        req.Chat,
			Command:     fields[0],
			Args:        fields[1:],
			Session:     connectors.SessionRef{NodeID: connectors.HostKindAICLI, SessionID: sessionID},
		}))
	case "answer":
		if len(fields) < 3 {
			return connectors.DispatchResult{}, errors.New("/answer 需要序号与回答内容")
		}
		return ok(r.backend.AnswerQuestion(ctx, connectors.CommandRequest{
			ConnectorID: req.ConnectorID,
			Chat:        req.Chat,
			Command:     "answer",
			Args:        fields[1:],
			Session:     connectors.SessionRef{NodeID: connectors.HostKindAICLI, SessionID: sessionID},
		}))
	default:
		return connectors.DispatchResult{}, fmt.Errorf("aicli runner: 不支持的派发命令 %s", fields[0])
	}
}

// ── Backend：IM 消息 → 本机 chat 会话 ──────────────────────────────────────

type localConnectorBackend struct {
	host *localChatRuntimeHost
	logf func(format string, args ...any)
	// commands 是测试注入的命令面替身；nil 时由 host.ActorRegistry 适配。
	commands localConnectorCommandPlane
	// turns 是 IM 来源回合登记器（S1a/S1b 内容镜像的 L3 判据，docs/bots/13 §4.2；
	// 共享实现 connectors.TurnActivityTracker）。
	turns *connectors.TurnActivityTracker
	// hostID 是本宿主实例身份（`aicli:<mesh-node-id>`，与 Service.Deps.HostID
	// 同一取值）：Handle 返回的 SessionRef 必须携带它——空 NodeID 会把绑定
	// 归属抹空（2026-10-17 真机 qq-bot1：绑定 NodeID 被逐条消息抹空，下一条
	// 消息落进假 R2a 接管）。未显式注入（测试直连构造）时回退进程级计算。
	hostID string
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

// localHostID 返回本宿主实例身份（HostID 全形）。未显式注入（测试直连构造）
// 时回退进程级身份计算，保证 sessionRef 携带的 NodeID 永不为空——空 NodeID
// 会把绑定归属抹空（2026-10-17 真机 qq-bot1 复盘）。
func (b localConnectorBackend) localHostID() string {
	if id := strings.TrimSpace(b.hostID); id != "" {
		return id
	}
	return connectors.BuildHostID(connectors.HostKindAICLI, aicliHostInstanceID())
}

// sessionRef 组装带宿主身份的会话引用（Handle 的全部返回路径共用，保证
// 连接器回写绑定时拿到的 NodeID 与本宿主 Service 的 HostID 一致）。
func (b localConnectorBackend) sessionRef(sessionID string) connectors.SessionRef {
	return connectors.SessionRef{NodeID: b.localHostID(), SessionID: sessionID}
}

func (b localConnectorBackend) Name() string { return "aicli" }

// Backtrack 实现 connectors.BacktrackBackend（/undo）：与 TUI /backtrack
// 走同一 actor 入口（SessionHub.GetOrCreate → actor.Backtrack）。
//
// TurnsBefore 语义：保留最近 N 轮 → 回退到倒数第 N+1 轮。
func (b localConnectorBackend) Backtrack(ctx context.Context, req connectors.BacktrackRequest) (connectors.BacktrackResult, error) {
	if b.host == nil || b.host.SessionHub == nil {
		return connectors.BacktrackResult{}, errors.New("aicli: 本机 chat 运行时未就绪")
	}
	sessionID := strings.TrimSpace(req.Session.SessionID)
	if sessionID == "" {
		return connectors.BacktrackResult{}, errors.New("aicli: session is not bound")
	}
	actor, err := b.host.SessionHub.GetOrCreate(sessionID)
	if err != nil {
		return connectors.BacktrackResult{}, err
	}
	// 先读当前可见历史，换算 anchor 下标（与 TUI 的 turns 列表同源）。
	turns, err := actor.ListTurns(ctx)
	if err != nil {
		return connectors.BacktrackResult{}, err
	}
	index, ok := connectors.ResolveUndoTurnIndex(len(turns), req.TurnsBefore)
	if !ok {
		return connectors.BacktrackResult{Handled: false}, nil
	}
	anchor := turns[index]
	result, _, err := actor.Backtrack(ctx, runtimechat.BacktrackRequest{
		UserTurnIndex: runtimechat.IntPtr(anchor.Index),
		Mode:          runtimechat.BacktrackModeConversation,
	})
	if err != nil {
		return connectors.BacktrackResult{}, err
	}
	if result == nil {
		return connectors.BacktrackResult{Handled: false}, nil
	}
	return connectors.BacktrackResult{
		Handled:       true,
		RemovedTurns:  result.RemovedUserTurns,
		AnchorPreview: result.AnchorPreview,
	}, nil
}

// Handle 把一条 IM 消息提交为一次本机 chat turn，并把流式增量与终态回复回吐给
// 连接器：增量经会话事件总线（assistant_delta）转发给 ReplySink（P1，2026-10-15
// 接入），占位消息 Final 与失败账本由 Service 负责收尾。
func (b localConnectorBackend) Handle(ctx context.Context, req connectors.BackendRequest, sink connectors.ReplySink) (connectors.BackendResult, error) {
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
	// L3（docs/bots/13 §4.2）：登记本会话的 IM 来源回合——内容镜像订阅据此
	// 跳过该回合（正文已由回复路径投递）。本地入站与 mesh runner 派发共用
	// 本函数，两条 IM 来源路径都被覆盖。
	if b.turns != nil {
		endTurn := b.turns.Begin(sessionID)
		defer endTurn()
	}
	// P1 流式增量（2026-10-15）：本会话的 assistant 打字机增量回推连接器——
	// ReplyMode=stream 且通道支持时由 Service 编辑占位消息；增量失败不阻断
	// 回合（终稿投递仍会尝试）。总线不可用/无 sink 时零变化。
	if sink != nil {
		unsubscribeDeltas := connectors.SubscribeAssistantDeltas(b.host.EventBus, sessionID, func(text string) {
			_ = sink.Delta(ctx, text)
		})
		defer unsubscribeDeltas()
	}
	// IM 输入投影（2026-10-15）：绑定会话恰为本进程当前展示的 TUI 会话时，
	// 把用户输入写入 transcript（KindUser 块，与 TUI 直输/Web 注入同款渲染），
	// 满足「TUI 显示 IM 用户的输入信息」；助手输出经同一事件管线显示，终态
	// 回复仍由本函数返回、由连接器作为副本回传 bot（input 显示 + 输出副本）。
	projectIMInputToDisplayedSession(sessionID, text)

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
	// 会话执行租约冲突（docs/bots/09 §5.6 运行归属的会话级对偶）：目标绑定
	// 会话正被另一个存活进程的 actor 持有（最常见 = 该会话在另一个 aicli
	// 实例里打开着）。给 IM 可行动的提示，而不是原始 "already owned by ..."。
	var leaseConflict *runtimechat.LeaseConflictError
	if errors.As(err, &leaseConflict) {
		return connectors.BackendResult{Session: b.sessionRef(sessionID)},
			errors.New(localConnectorLeaseConflictText(sessionID, leaseConflict.Lease))
	}
	if result == nil {
		if err == nil {
			err = errors.New("aicli: empty session result")
		}
		return connectors.BackendResult{Session: b.sessionRef(sessionID)}, err
	}
	out := connectors.BackendResult{Session: b.sessionRef(sessionID)}
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

// projectIMInputToDisplayedSession 把 IM 文本投影进「恰为本进程当前展示会话」的
// transcript（2026-10-15，Handle 的可见性契约）：仅当目标会话 == 展示会话时才
// 写入，避免把消息错投到用户眼前的其他会话；写入通道与 TUI 直输/Web 注入同款
// （KindUser 块）——纯显示，不触发执行；回合仍由 Handle 提交且仅提交一次。
func projectIMInputToDisplayedSession(sessionID, text string) {
	displayed := chatWebSession()
	if displayed == nil || strings.TrimSpace(currentRuntimeSessionID(displayed)) != strings.TrimSpace(sessionID) {
		return
	}
	if bridge := displayed.RuntimeEventBridge; bridge != nil {
		bridge.submitUserInput(text)
	}
}

// localConnectorLeaseConflictText 把会话租约冲突翻译成 IM 可行动的提示：
// 指明占用者进程与本进程的关系，给出「等 TTL 过期 / 关闭占用实例 / 改绑」
// 三条出路。占用者存活判定复用 acquireLocalChatSessionLease 的同款口径。
func localConnectorLeaseConflictText(sessionID string, lease *runtimechat.SessionLease) string {
	holder := "未知持有者"
	expires := ""
	if lease != nil {
		holder = strings.TrimSpace(lease.OwnerID)
		if !lease.ExpiresAt.IsZero() {
			expires = "，租约 " + lease.ExpiresAt.Local().Format("15:04:05") + " 过期"
		}
		if localChatSessionLeaseOwnerStopped(lease) {
			// 同主机同类型持有者已死：TTL 过期后本进程即可接管，稍后重试即可。
			return fmt.Sprintf("会话 %s 的执行租约由已退出的进程持有（%s）%s；稍后重试即可自动接管。",
				sessionID, holder, expires)
		}
	}
	return fmt.Sprintf("会话 %s 正被另一个 aicli 实例执行（%s%s）；请在该实例处理或关闭后重试，或用 /bots bind <连接器id> 把机器人改绑到其它会话。",
		sessionID, holder, expires)
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

// ── P2 切片 B 后半：mesh peers 视图与远程派发（docs/bots/09 §7.2/§7.3）─────
// 与 runtime-server 同构（单一语义两宿主落点：peers 从 node record 档案读、
// 派发 POST 对端 /api/connectors/dispatch；本机自身档案由 mesh Host 写入，
// 其 NodeID 即 aicliHostInstanceID 的 instance 段）。

// meshChatNodeKind 是 mesh 档案中 aicli chat 节点的 Kind 值（internal/mesh
// host.go NewHost 的缺省 Kind）。与 connectors.MeshPeerKindAICLI（"aicli"，
// HostID 前缀词表）不同源——mesh 的 Kind 描述节点产品形态（chat），连接器
// 的 HostKind 描述宿主种类。peers 装配必须同时接受两者。
const meshChatNodeKind = "chat"

func aicliMeshRunnerPeers(logf func(format string, args ...any)) func() []connectors.RunnerPeer {
	return func() []connectors.RunnerPeer {
		paths := mesh.ResolvePaths()
		if !paths.Enabled() {
			return nil
		}
		view := mesh.BuildView(paths, mesh.ViewOptions{})
		self := aicliHostInstanceID()
		out := make([]connectors.RunnerPeer, 0, len(view.Nodes))
		for _, node := range view.Nodes {
			kind := strings.ToLower(strings.TrimSpace(node.Kind))
			// mesh 档案的 aicli chat 节点 Kind 是 "chat"（host.go NewHost 缺省
			// Kind=chat；架构词表与 connectors.MeshPeerKindAICLI 的 "aicli" 不
			// 同源）。不认 "chat" 会把全部 aicli 节点剔出 runner 候选——peers
			// 恒空、R1 亲和派发无目标、永远退化为 R2a 本机接管（2026-10-15
			// 真机 qq-bot1 第二轮复盘：bind 写了 NodeID 也不派发）。
			if kind != connectors.MeshPeerKindAICLI && kind != connectors.MeshPeerKindRuntimeServer && kind != meshChatNodeKind {
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
		_ = self
		_ = logf
		return out
	}
}

func aicliDispatchToRunnerPeer(logf func(format string, args ...any)) func(context.Context, connectors.DispatchRequest) (connectors.DispatchResult, error) {
	client := &http.Client{Timeout: connectors.DispatchCallTimeout}
	return func(ctx context.Context, req connectors.DispatchRequest) (connectors.DispatchResult, error) {
		baseURL, token := aicliMeshPeerLookup(req.Target.NodeID)
		if baseURL == "" {
			return connectors.DispatchResult{}, fmt.Errorf("节点 %s 无可达 endpoint（档案缺失或掉线）", req.Target.NodeID)
		}
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
			return connectors.DispatchResult{}, fmt.Errorf("节点 %s 返回 HTTP %d：%s", req.Target.NodeID, resp.StatusCode, aicliDispatchErrorLine(string(data)))
		}
		var wire struct {
			Reply   []connectors.OutboundMessage `json:"reply"`
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

func aicliMeshPeerLookup(nodeID string) (baseURL, token string) {
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

func aicliDispatchErrorLine(body string) string {
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

// aicliForwardAttentionToPeers 把注意力广播转发给全部活 peers 的
// /api/connectors/attention（P2 切片 D，scope=all 跨宿主扇出的发起侧；
// 与 runtime-server forwardAttentionToPeers 同构——单一语义两宿主落点）。
func aicliForwardAttentionToPeers(logf func(format string, args ...any)) func(connectors.AttentionNotice) {
	client := &http.Client{Timeout: 10 * time.Second}
	return func(notice connectors.AttentionNotice) {
		paths := mesh.ResolvePaths()
		if !paths.Enabled() {
			return
		}
		view := mesh.BuildView(paths, mesh.ViewOptions{})
		payload, err := json.Marshal(map[string]any{
			"kind":    notice.Kind,
			"session": map[string]string{"node_id": notice.Session.NodeID, "session_id": notice.Session.SessionID},
			"text":    notice.Text,
		})
		if err != nil {
			return
		}
		for _, node := range view.Nodes {
			if node.State != mesh.NodeStateLive || node.Endpoint == nil || strings.TrimSpace(node.Endpoint.BaseURL) == "" {
				continue
			}
			kind := strings.ToLower(strings.TrimSpace(node.Kind))
			if kind != connectors.MeshPeerKindAICLI && kind != connectors.MeshPeerKindRuntimeServer {
				continue
			}
			go func(baseURL, token, nodeID string) {
				httpReq, err := http.NewRequest(http.MethodPost,
					strings.TrimRight(baseURL, "/")+"/api/connectors/attention", bytes.NewReader(payload))
				if err != nil {
					return
				}
				httpReq.Header.Set("Content-Type", "application/json")
				if token != "" {
					httpReq.Header.Set("X-AICLI-Token", token)
				}
				resp, err := client.Do(httpReq)
				if err != nil {
					if logf != nil {
						logf("connector attention forward to %s failed: %v", nodeID, err)
					}
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK && logf != nil {
					logf("connector attention forward to %s: HTTP %d", nodeID, resp.StatusCode)
				}
			}(node.Endpoint.BaseURL, aicliMeshPeerToken(node.NodeID), node.NodeID)
		}
	}
}

func aicliMeshPeerToken(nodeID string) string {
	_, token := aicliMeshPeerLookup(nodeID)
	return token
}
