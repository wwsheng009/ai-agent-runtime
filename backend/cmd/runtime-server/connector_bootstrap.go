package main

import (
	"context"
	"errors"
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
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/storefile"
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
	store := storefile.NewConfigStore(configPath, runtimeConnectorStatePath(configPath))

	service := connectors.New(connectors.Deps{
		Store:   store,
		Backend: runtimeConnectorBackend{handler: handler, logf: logf},
		Logf:    logf,
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
	}
	// 管理面：先注册的 /api/runtime* 路由优先命中，这里只兜住 /api/connectors*。
	router.PathPrefix("/api/connectors").Handler(http.StripPrefix("/api", restapi.NewRouter(deps)))
	router.PathPrefix("/hooks/connectors").Handler(restapi.NewHooksRouter(deps))

	if err := service.Start(ctx); err != nil {
		return nil, err
	}
	return service, nil
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
// Fix-S4：优先用共享 config.yaml 所在目录推导（<configDir>/data/connectors/
// state.json），与 aicli 宿主同源——项目层配置或 --config 指定路径时，两个
// 宿主才能共享同一份账本。configPath 为空时回落到 DefaultAuthStorePath()
// 所在目录的推导（与 supervision / subagent-batches 同级的宿主数据目录）。
func runtimeConnectorStatePath(configPath string) string {
	if dir := strings.TrimSpace(configPath); dir != "" {
		return filepath.Join(filepath.Dir(dir), "data", "connectors", "state.json")
	}
	return filepath.Join(filepath.Dir(agentconfig.DefaultAuthStorePath()), "data", "connectors", "state.json")
}

// runtimeConnectorBackend 把 IM 消息投递到本进程的会话执行路径
// （runtimeapi.Handler.SubmitConnectorPrompt），并把 assistant 增量回推给连接器。
type runtimeConnectorBackend struct {
	handler *runtimeapi.Handler
	logf    func(format string, args ...any)
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
