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
	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify/channels"
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
) (*connectors.Service, gatewayAttentionHook, func(nodeID, sessionID, kind string) bool, error) {
	if router == nil || registry == nil || server == nil || owner == nil {
		return nil, nil, nil, errors.New("connectors: gateway router/registry/server/owner is not ready")
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
		return nil, nil, nil, err
	}
	if err := service.Register(qqbot.New(qqbot.WithLogger(logf))); err != nil {
		return nil, nil, nil, err
	}
	if err := service.Register(wechat.New(wechat.WithLogger(logf))); err != nil {
		return nil, nil, nil, err
	}
	if err := service.Register(feishu.New(feishu.WithLogger(logf))); err != nil {
		return nil, nil, nil, err
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
		return nil, nil, nil, err
	}
	// 注意力出站（FR-8，docs/bots/09 §7.4.1）：gateway 无 LLM 事件总线——
	// 节点心跳上报的 sess.Attention 投影是接入面。返回的 hook 供 main.go 在
	// tunnel OnSessionTransition 回调里调用（构造期接线，避免可变回调竞争）：
	// 把 attention 变化映射为 AttentionNotice，按 policy.notify（D5）过滤后
	// 出站到绑定 IM。交互闭环由 /approve /answer 命令经隧道回到 owner 节点。
	hook := newGatewayAttentionHook(ctx, store, service, logf)
	return service, hook, newIMMainChannelPredicate(store), nil
}

// newIMMainChannelPredicate 报告「normal 类通知的主通道是否为 IM」（06 §1 D6：
// 已绑定 IM 则 IM，否则 Web Push）。判定口径与投影 hook 的投递逐字同源：
// AttentionNoticeForProjection（waiting/未知 kind 不投）+ 逐连接器
// AcceptsNotice（含 completion 随 notify 开关语义）。store 为 nil 时返回 nil
// （调用方一律走 push 通道）。
func newIMMainChannelPredicate(store connectors.Store) func(nodeID, sessionID, kind string) bool {
	if store == nil {
		return nil
	}
	return func(_, sessionID, kind string) bool {
		notice, _, ok := connectors.AttentionNoticeForProjection(strings.TrimSpace(sessionID), strings.TrimSpace(kind))
		if !ok {
			return false
		}
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
				return true
			}
		}
		return false
	}
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
	return newGatewayAttentionHookNotify(ctx, store, service.NotifyAttentionScoped, logf)
}

// newGatewayAttentionHookNotify 是 hook 的可注入形态（测试用替身 notify；
// 签名带接受连接器集合——scope=all 未绑定事件回退广播定位投递目标）。
// 投递核心收敛到 channels.IM 适配器（notify 切片二共用同一实现）。
func newGatewayAttentionHookNotify(ctx context.Context, store connectors.Store, notifyScoped func(context.Context, []string, connectors.AttentionNotice) error, logf func(format string, args ...any)) gatewayAttentionHook {
	if notifyScoped == nil {
		return func(string, string, string, string) {}
	}
	im := &channels.IM{Store: store, NotifyScoped: notifyScoped}
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
		go func() {
			if err := im.Deliver(ctx, notify.Event{Kind: kind, Target: sessionID}); err != nil && logf != nil {
				logf("connector attention projection %s delivery failed (session=%s): %v", kind, sessionID, err)
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
