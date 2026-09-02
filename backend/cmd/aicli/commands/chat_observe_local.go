package commands

import (
	"context"
	"fmt"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeobserve "github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

// ============================================================================
// aicli 本地 in-process 模式的 Runtime Observation Plane 服务。
//
// runtime-server 模式下 observe 端点由服务端提供（/api/runtime/observe/v1）。
// aicli 本地模式（无 runtime-server）通过本文件在进程内构建
// runtimeobserve.Service：复用 localChatRuntimeHost 上已有的 EventBus、
// SessionHub 与 RuntimeConfig.Observe，再把 /capabilities、/snapshot、
// /sessions/{id}、/events 挂到本机 pprof loopback HTTP 服务器上，使本地模式
// 也能提供完整的观察平面。
//
// 启用条件（本地模式）：RuntimeConfig.Observe.Enabled=true，或 pprof loopback
// 服务器已开启（--pprof / AICLI_PPROF / --debug）——即"默认随 --pprof on 开启"。
// 服务构建为惰性：首次被 HTTP 端点或 /debug display 触发时创建一次并缓存到
// host 上，host.Close() 时释放 collector 的 bus 订阅。
// ============================================================================

// localObserveSessionSource 把本地 host 的 SessionHub 活动 session actor 投影为
// 低敏 SessionSummary。只读取 StateSummary()（idle/running 等状态 + turn id），
// 不触碰 prompt、工具参数、tool receipt、checkpoint 内容等敏感或重量级数据。
// 与服务端 observeSessionSource（internal/api/skills/observe_handlers.go）同构。
type localObserveSessionSource struct {
	host *localChatRuntimeHost
}

func (o *localObserveSessionSource) hub() *runtimechat.SessionHub {
	if o == nil || o.host == nil {
		return nil
	}
	return o.host.SessionHub
}

func (o *localObserveSessionSource) ObservationSessionSummaries(ctx context.Context, limit int) ([]runtimeobserve.SessionSummary, error) {
	hub := o.hub()
	if hub == nil {
		return nil, fmt.Errorf("session hub not configured")
	}
	if limit <= 0 {
		limit = 50
	}
	ids := hub.ActiveSessionIDs(limit)
	out := make([]runtimeobserve.SessionSummary, 0, len(ids))
	for _, id := range ids {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			default:
			}
		}
		actor, ok := hub.Get(id)
		if !ok {
			continue
		}
		summary, ok := actor.StateSummary()
		if !ok {
			continue
		}
		out = append(out, localObserveProjectSession(summary))
	}
	return out, nil
}

func (o *localObserveSessionSource) ObservationSession(ctx context.Context, sessionID string) (runtimeobserve.SessionSummary, bool, error) {
	hub := o.hub()
	if hub == nil {
		return runtimeobserve.SessionSummary{}, false, nil
	}
	actor, ok := hub.Get(sessionID)
	if !ok {
		return runtimeobserve.SessionSummary{}, false, nil
	}
	summary, ok := actor.StateSummary()
	if !ok {
		return runtimeobserve.SessionSummary{}, false, nil
	}
	return localObserveProjectSession(summary), true, nil
}

// localObserveProjectSession 把 chat.RuntimeStateSummary 投影为低敏 SessionSummary。
// 只透传 status/turn id 等公开状态；trace/revision/last_event 等依赖权重的字段
// 在 v1 阶段以 0/空处理，避免暴露内部序列。
func localObserveProjectSession(s runtimechat.RuntimeStateSummary) runtimeobserve.SessionSummary {
	return runtimeobserve.SessionSummary{
		SessionID: s.SessionID,
		State:     string(s.Status),
		TurnID:    s.CurrentTurnID,
	}
}

// localObserveEnabled 判断本地 observe 服务是否应构建：RuntimeConfig.Observe.Enabled
// 显式开启，或 pprof loopback 服务器已开启（默认随 --pprof on 开启）。
func localObserveEnabled(host *localChatRuntimeHost) bool {
	if host == nil || host.RuntimeConfig == nil {
		return false
	}
	if host.RuntimeConfig.Observe.Enabled {
		return true
	}
	return chatDebugPprofBaseURL() != ""
}

// ensureLocalObserveService 惰性构建本地 observe service 并缓存到 host。
// 每次调用返回同一个实例；条件不满足时返回 nil。
func ensureLocalObserveService(host *localChatRuntimeHost) *runtimeobserve.Service {
	if host == nil {
		return nil
	}
	host.observeOnce.Do(func() {
		host.observeSvc = buildLocalObserveService(host)
	})
	return host.observeSvc
}

// buildLocalObserveService 构建本地 observe service（collector + service）。
// 需要 EventBus 与 SessionHub 均就绪；任一缺失返回 nil（服务不可用）。
func buildLocalObserveService(host *localChatRuntimeHost) *runtimeobserve.Service {
	if !localObserveEnabled(host) {
		return nil
	}
	if host == nil || host.EventBus == nil || host.SessionHub == nil {
		return nil
	}
	cfg := runtimeobserve.WithDefaults(host.RuntimeConfig.Observe)
	cfg.Enabled = true // 本地模式：服务在进程内真实启用
	redactor := runtimeobserve.NewRedactor(nil, "", cfg.RedactionProfile)
	projector := runtimeobserve.NewProjector(redactor, cfg.ExposeProviderRequestID, int(cfg.MaxEventBytes))
	collector := runtimeobserve.NewCollector(cfg, host.EventBus, projector)
	if collector == nil {
		return nil
	}
	collector.Start()
	svc := runtimeobserve.NewService(cfg, collector, redactor, nil, &localObserveSessionSource{host: host})
	host.cleanupFns = append(host.cleanupFns, func() {
		if svc != nil {
			svc.Close()
		}
	})
	return svc
}

// chatLocalObserveService 返回当前活动会话的本地 observe 服务；无会话或
// 服务不可用时返回 nil。
func chatLocalObserveService() *runtimeobserve.Service {
	session := chatDebugDisplaySession()
	if session == nil {
		return nil
	}
	return ensureLocalObserveService(session.LocalRuntimeHost)
}
