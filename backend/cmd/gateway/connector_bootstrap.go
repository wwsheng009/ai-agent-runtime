package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/providers/feishu"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/providers/qqbot"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/providers/telegram"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/providers/wechat"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/restapi"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/storejson"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/auth"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
)

// 本文件是 gateway 宿主侧的连接器接线（docs/bots/03-host-integration.md §6/§7、
// docs/bots/04-migration-roadmap.md P1「宿主接线」）：
//
//   - 配置（含密钥）落 <data-dir>/connectors.json，运行时状态落
//     <data-dir>/connectors-state.json（storejson，0600）；
//   - Backend=gateway_node：消息经反向隧道投递到在线节点的 /web/api/invoke；
//   - 管理面 /api/connectors* 挂 owner.RequireSession（与 console 同一鉴权口径）；
//   - 公网 webhook /hooks/connectors/{id} 不鉴权，由各 Provider 自行验签；
//   - 先注册连接器路由、后注册 console 的 "/" 兜底，保证 API 不被静态页吞掉。
//
// 失败只返回 error 由调用方 warn（与 runtime-server 周边接线同风格）：连接器
// 接线问题不应阻断网关的隧道/console 主功能。
func setupGatewayConnectors(
	ctx context.Context,
	router *mux.Router,
	owner *auth.Owner,
	registry *tunnel.Registry,
	server *tunnel.Server,
	dataDir string,
	logf func(format string, args ...any),
) (*connectors.Service, gatewayAttentionHook, error) {
	if router == nil || registry == nil || server == nil || owner == nil {
		return nil, nil, errors.New("connectors: gateway router/registry/server/owner is not ready")
	}
	if logf == nil {
		logf = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "gateway: "+format+"\n", args...)
		}
	}
	store := storejson.New(dataDir)

	service := connectors.New(connectors.Deps{
		Store:   store,
		Backend: newGatewayNodeBackend(registry, server, store),
		Logf:    logf,
		// 运行归属（07 §5.6）：网关持有轮询连接器时，aicli/runtime-server 宿主的
		// 同名连接器会因心跳互斥而让出轮询，避免重复消费平台消息。
		HostID: "gateway",
		// 与 runtime-server 同口径：不启用 fail-closed 白名单闸（保持对既有
		// 配置的兼容），仅在启动时对「启用且白名单为空」的连接器发可见性告警。
		RequireAllowedUsers: false,
	})
	if err := service.Register(telegram.New(telegram.WithLogger(logf))); err != nil {
		return nil, nil, err
	}
	if err := service.Register(qqbot.New(qqbot.WithLogger(logf))); err != nil {
		return nil, nil, err
	}
	if err := service.Register(wechat.New(wechat.WithLogger(logf))); err != nil {
		return nil, nil, err
	}
	if err := service.Register(feishu.New(feishu.WithLogger(logf))); err != nil {
		return nil, nil, err
	}

	for _, cfg := range store.ListConfigs() {
		if cfg.Enabled && len(cfg.Policy.AllowedUsers) == 0 {
			logf("connector %s: enabled with empty policy.allowed_users; 该连接器不会限制平台用户（gateway 未启用 fail-closed 闸，仅告警）", cfg.ID)
		}
	}

	deps := restapi.Deps{
		Service: service,
		Store:   store,
		// 鉴权发生在外层中间件：/api/connectors 整体包在 owner.RequireSession 里，
		// 走到 restapi 时已通过 owner 会话校验，因此这里传恒 nil 放行；绝不把
		// 裸 restapi.NewRouter（Authorize=nil → 403）直接挂到路由上。
		Authorize: func(*http.Request) error { return nil },
		Logf:      logf,
		HostID:    "gateway",
	}
	router.PathPrefix("/api/connectors").Handler(owner.RequireSession(http.StripPrefix("/api", restapi.NewRouter(deps))))
	router.PathPrefix("/hooks/connectors").Handler(restapi.NewHooksRouter(deps))

	if err := service.Start(ctx); err != nil {
		// 半途启动的轮询实例随宿主一起停掉，避免调用方只记 warn 时泄漏。
		stopGatewayConnectors(service)
		return nil, nil, err
	}
	// 注意力出站（FR-8，docs/bots/09 §7.4.1）：gateway 无 LLM 事件总线——
	// 节点心跳上报的 sess.Attention 投影是接入面。返回的 hook 供 main.go 在
	// tunnel OnSessionTransition 回调里调用（构造期接线，避免可变回调竞争）：
	// 把 attention 变化映射为 AttentionNotice，按 policy.notify（D5）过滤后
	// 出站到绑定 IM。交互闭环由 /approve /answer 命令经隧道回到 owner 节点。
	hook := newGatewayAttentionHook(ctx, store, service, logf)
	return service, hook, nil
}

// gatewayAttentionHook 是 OnSessionTransition 的连接器侧处理器：把节点上报的
// attention 投影（新进入/变化）映射为 AttentionNotice 并按 D5 出站到绑定 IM。
// 同一 (session, kind) 只推一次（进程内去重；attention 消失清记录，下次再进
// 入视为新事件——与 push.go seen 快照同语义）。零值安全。
type gatewayAttentionHook func(nodeID, sessionID, state, attention string)

func newGatewayAttentionHook(ctx context.Context, store connectors.Store, service *connectors.Service, logf func(format string, args ...any)) gatewayAttentionHook {
	if service == nil {
		return func(string, string, string, string) {}
	}
	return newGatewayAttentionHookNotify(ctx, store, service.NotifyAttention, logf)
}

// newGatewayAttentionHookNotify 是 hook 的可注入形态（测试用替身 notify）。
func newGatewayAttentionHookNotify(ctx context.Context, store connectors.Store, notify func(context.Context, connectors.AttentionNotice) error, logf func(format string, args ...any)) gatewayAttentionHook {
	if notify == nil {
		return func(string, string, string, string) {}
	}
	var mu sync.Mutex
	notified := map[string]string{} // sessionID -> last notified attention kind
	return func(_ string, sessionID, _, attention string) {
		kind := strings.TrimSpace(attention)
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			return
		}
		if kind == "" {
			// attention 消失（已处理/已查看）：清去重记录，下次再进入可再推。
			mu.Lock()
			delete(notified, sessionID)
			mu.Unlock()
			return
		}
		mu.Lock()
		if notified[sessionID] == kind {
			mu.Unlock()
			return
		}
		notified[sessionID] = kind
		mu.Unlock()
		notice, _, ok := connectors.AttentionNoticeForProjection(sessionID, kind)
		if !ok {
			return
		}
		// D5 过滤与出站共用 connectors 的口径：这里手动走一遍 AcceptsNotice
		// （共享 SubscribeAttention 以 LLM 事件为输入，gateway 是投影输入）。
		if store != nil {
			accepted := false
			for _, cfg := range store.ListConfigs() {
				if !cfg.Enabled {
					continue
				}
				if cfg.Policy.AcceptsNotice(notice, func(session connectors.SessionRef) bool {
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
		go func() {
			if err := notify(ctx, notice); err != nil && logf != nil {
				logf("connector attention projection %s delivery failed (session=%s): %v", notice.Kind, sessionID, err)
			}
		}()
	}
}

// stopGatewayConnectors 停机时停止轮询并释放运行归属（尽力，不阻塞退出）。
func stopGatewayConnectors(service *connectors.Service) {
	if service == nil {
		return
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	service.Stop(stopCtx)
}
