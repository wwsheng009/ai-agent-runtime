package commands

import (
	"context"
	"fmt"
	"os"
	"strings"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageledger"
)

// ============================================================================
// aicli 本地 in-process 模式的 LLM 缓存分析服务（cache.analytics.v1）。
//
// 与 observeSvc 同模式：Collector 挂在 localChatRuntimeHost.EventBus 上惰性
// 构建一次，HTTP（/web/api/cache/*）与 TUI（/usage 命令）共用同一个
// cacheanalytics.Source，采集与聚合只做一次（方案 §6.1）。
//
// 与 observe 不同：collector 无 gate（记录为低敏 token/缓存统计，不含
// prompt/completion 原文，环形缓冲 1000 条封顶），TUI /usage 在默认模式下
// 也依赖它，因此只要 EventBus 就绪即挂载。
// ============================================================================

// localCacheHistoryLookup 把 host.SessionStore（runtimechat.SessionStorage）
// 适配为 cacheanalytics.HistoryLookup：由消息 role/turn 反查 message_id，
// 支撑"缓存失效 → 反查请求历史与原始记录"的兜底路径（方案 §5.2）。
type localCacheHistoryLookup struct {
	store runtimechat.SessionStorage
}

func (h *localCacheHistoryLookup) load(sessionID string) (*runtimechat.Session, bool) {
	if h == nil || h.store == nil || strings.TrimSpace(sessionID) == "" {
		return nil, false
	}
	session, err := h.store.Load(context.Background(), sessionID)
	if err != nil || session == nil {
		return nil, false
	}
	return session, true
}

// MessageContext 返回消息在历史中的 role/turn/相邻消息。
func (h *localCacheHistoryLookup) MessageContext(sessionID, messageID string) (cacheanalytics.MessageContext, bool) {
	session, ok := h.load(sessionID)
	if !ok {
		return cacheanalytics.MessageContext{}, false
	}
	history := session.History
	for i := range history {
		if runtimetypes.MessageID(history[i]) != messageID {
			continue
		}
		ctx := cacheanalytics.MessageContext{
			MessageID: messageID,
			Role:      strings.ToLower(strings.TrimSpace(history[i].Role)),
			TurnID:    runtimetypes.TurnID(history[i]),
		}
		if i > 0 {
			ctx.PrevMessageID = runtimetypes.MessageID(history[i-1])
		}
		if i+1 < len(history) {
			ctx.NextMessageID = runtimetypes.MessageID(history[i+1])
		}
		return ctx, true
	}
	return cacheanalytics.MessageContext{}, false
}

// AssistantMessageIDByTurn 返回该 turn 最后一条 assistant 消息的 message_id
// （assistant 消息 turn_id 继承自触发 user 消息，直接匹配）。
func (h *localCacheHistoryLookup) AssistantMessageIDByTurn(sessionID, turnID string) (string, bool) {
	session, ok := h.load(sessionID)
	if !ok {
		return "", false
	}
	for i := len(session.History) - 1; i >= 0; i-- {
		msg := session.History[i]
		if !strings.EqualFold(strings.TrimSpace(msg.Role), "assistant") {
			continue
		}
		if runtimetypes.TurnID(msg) != turnID {
			continue
		}
		if id := runtimetypes.MessageID(msg); id != "" {
			return id, true
		}
		return "", false
	}
	return "", false
}

// UserMessageIDByTurn 返回触发该 turn 的 user 消息 message_id。
func (h *localCacheHistoryLookup) UserMessageIDByTurn(sessionID, turnID string) (string, bool) {
	session, ok := h.load(sessionID)
	if !ok {
		return "", false
	}
	for i := range session.History {
		msg := session.History[i]
		if !strings.EqualFold(strings.TrimSpace(msg.Role), "user") {
			continue
		}
		if runtimetypes.TurnID(msg) != turnID {
			continue
		}
		if id := runtimetypes.MessageID(msg); id != "" {
			return id, true
		}
		return "", false
	}
	return "", false
}

// SessionExists 会话是否存在于历史存储。
func (h *localCacheHistoryLookup) SessionExists(sessionID string) bool {
	_, ok := h.load(sessionID)
	return ok
}

// ensureLocalCacheService 惰性构建本地缓存分析服务并缓存到 host。
// 每次调用返回同一个实例；EventBus 缺失时返回 nil。
func ensureLocalCacheService(host *localChatRuntimeHost) *cacheanalytics.Service {
	if host == nil {
		return nil
	}
	host.cacheOnce.Do(func() {
		host.cacheSvc = buildLocalCacheService(host)
	})
	return host.cacheSvc
}

func buildLocalCacheService(host *localChatRuntimeHost) *cacheanalytics.Service {
	if host == nil || host.EventBus == nil {
		return nil
	}
	var history cacheanalytics.HistoryLookup
	if host.SessionStore != nil {
		history = &localCacheHistoryLookup{store: host.SessionStore}
	}
	// Phase 3：终态记录持久化镜像（session_runtime.sqlite cache_requests，
	// 方案 §375）。SQLiteRuntimeStore 实现 cacheanalytics.RequestStore；
	// InMemoryRuntimeStore 不实现，断言失败时保持纯内存行为（v1 不变）。
	var store cacheanalytics.RequestStore
	if runtimeStore, ok := host.RuntimeStore.(cacheanalytics.RequestStore); ok {
		store = runtimeStore
	}
	service := cacheanalytics.Attach(host.EventBus, cacheanalytics.Options{Store: store}, history)
	if service == nil {
		return nil
	}
	host.cleanupFns = append(host.cleanupFns, service.Close)

	// Generic usage ledger: EventBus-attached recorder for token_usage_history,
	// shared with runtime-server (docs §04:69 / §1064 / 06 §338). Captures
	// llm.request.finished events from the agent loop that the skillsapi handler
	// path does not see.
	if host.ledgerEnabled && host.ledgerDSN != "" {
		if ledgerStore, lerr := usageledger.NewSQLiteStore(&usageledger.Config{
			Driver: host.ledgerDriver,
			DSN:    host.ledgerDSN,
		}); lerr == nil {
			ledgerSvc := usageledger.NewService(ledgerStore)
			if ledgerSvc != nil {
				ledgerSvc.Attach(host.EventBus)
				host.ledgerSvc = ledgerSvc
				host.cleanupFns = append(host.cleanupFns, ledgerSvc.Close)
			} else {
				_ = ledgerStore.Close()
			}
		} else {
			usageAttachWarn("本地用量账本服务初始化失败（dsn=%s），token_usage_history 不记录：%v", host.ledgerDSN, lerr)
		}
	}

	return service
}

// chatLocalCacheService 返回当前活动会话的缓存分析服务；无会话时返回 nil。
func chatLocalCacheService() *cacheanalytics.Service {
	session := chatDebugDisplaySession()
	if session == nil {
		return nil
	}
	return ensureLocalCacheService(session.LocalRuntimeHost)
}

// ============================================================================
// 统一用量分析（usage_analytics.sqlite）：与 runtime server 同一形态，
// EventBus 事件实时写入，缓存端点与 TUI 分析读同一数据库。
// ============================================================================

// localSessionMetaLookup 把 host.SessionStore 适配为
// usageanalytics.SessionMetaLookup（best-effort 补齐 title/项目/模型）。
type localSessionMetaLookup struct {
	store runtimechat.SessionStorage
}

// SessionMeta 返回会话元数据；ok=false 表示未知（保留库中已有值）。
func (l *localSessionMetaLookup) SessionMeta(sessionID string) (usageanalytics.SessionMeta, bool) {
	if l == nil || l.store == nil || strings.TrimSpace(sessionID) == "" {
		return usageanalytics.SessionMeta{}, false
	}
	session, err := l.store.Load(context.Background(), sessionID)
	if err != nil || session == nil {
		return usageanalytics.SessionMeta{}, false
	}
	meta := usageanalytics.SessionMeta{
		Title:    strings.TrimSpace(session.Metadata.Title),
		Model:    strings.TrimSpace(session.Metadata.LastModel),
		Provider: localContextString(session.Metadata.Context, "provider"),
		Protocol: localContextString(session.Metadata.Context, "protocol"),
	}
	workspace := localContextString(session.Metadata.Context, "workspace_path", "working_directory", "project_path", "cwd")
	meta.WorkingDirectory = workspace
	meta.ProjectPath = workspace
	if meta.Model == "" {
		meta.Model = localContextString(session.Metadata.Context, "model")
	}
	if session.State != "" {
		meta.Status = string(session.State)
	}
	return meta, true
}

func localContextString(values map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if values == nil {
			return ""
		}
		raw, ok := values[key]
		if !ok {
			continue
		}
		if typed, ok := raw.(string); ok {
			if trimmed := strings.TrimSpace(typed); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// usageAttachWarn 记录一次本地统一用量分析服务挂载失败。此前失败完全静默：
// 一旦启动期 attach 失败（例如多进程竞争 sqlite 写锁），该进程整个生命周期
// 都不会写入 usage_requests，缓存视图静默为空且没有任何线索。
var usageAttachWarn = func(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Warning: usage analytics "+format+"\n", args...)
}

// ensureLocalUsageService 启动期构建本地统一分析服务并缓存到 host。
//
// 与 cacheOnce 不同：构建失败不缓存（返回 nil 且不置位缓存），后续调用
// 会重试，避免一次瞬时失败（锁竞争、磁盘短暂不可用）导致整个进程生命周期
// 内用量分析始终不可用。
func ensureLocalUsageService(host *localChatRuntimeHost) *usageanalytics.Service {
	if host == nil {
		return nil
	}
	return host.ensureUsageService()
}

// ensureUsageService 惰性构建并缓存本地统一用量分析服务；构建失败不缓存，
// 后续调用可重试（并发调用者共享同一次构建，避免重复 attach 同一 EventBus）。
func (h *localChatRuntimeHost) ensureUsageService() *usageanalytics.Service {
	if h == nil {
		return nil
	}
	h.usageMu.Lock()
	defer h.usageMu.Unlock()
	if h.usageSvc != nil {
		return h.usageSvc
	}
	if service := buildLocalUsageService(h); service != nil {
		h.usageSvc = service
	}
	return h.usageSvc
}

func buildLocalUsageService(host *localChatRuntimeHost) *usageanalytics.Service {
	if host == nil || host.EventBus == nil {
		return nil
	}
	var lookup usageanalytics.SessionMetaLookup
	var history cacheanalytics.HistoryLookup
	if host.SessionStore != nil {
		lookup = &localSessionMetaLookup{store: host.SessionStore}
		history = &localCacheHistoryLookup{store: host.SessionStore}
	}
	path := usageanalytics.DefaultDBPath()
	if withPath, ok := host.RuntimeStore.(interface{ Path() string }); ok && withPath != nil {
		if derived := usageanalytics.PathFromRuntimeStore(withPath.Path()); derived != "" {
			path = derived
		}
	}
	service, err := usageanalytics.Attach(host.EventBus, usageanalytics.Options{
		Config:  usageanalytics.Config{Path: path},
		Lookup:  lookup,
		History: history,
	})
	if err != nil || service == nil {
		if err == nil {
			err = fmt.Errorf("attach 返回空服务")
		}
		usageAttachWarn("本地用量分析服务挂载失败（path=%s），本次运行的分析视图可能为空：%v", path, err)
		return nil
	}
	host.cleanupFns = append(host.cleanupFns, service.Close)
	return service
}

// chatLocalUsageService 返回当前活动会话的统一分析服务；无会话时返回 nil。
func chatLocalUsageService() *usageanalytics.Service {
	session := chatDebugDisplaySession()
	if session == nil {
		return nil
	}
	return ensureLocalUsageService(session.LocalRuntimeHost)
}
