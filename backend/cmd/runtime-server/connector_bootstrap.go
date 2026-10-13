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
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/providers/telegram"
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
	store := storefile.NewConfigStore(configPath, runtimeConnectorStatePath())

	service := connectors.New(connectors.Deps{
		Store:   store,
		Backend: runtimeConnectorBackend{handler: handler, logf: logf},
		Logf:    logf,
	})
	if err := service.Register(telegram.New(telegram.WithLogger(logf))); err != nil {
		return nil, err
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
// 与 supervision / subagent-batches 同级，位于宿主数据目录下。
func runtimeConnectorStatePath() string {
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
