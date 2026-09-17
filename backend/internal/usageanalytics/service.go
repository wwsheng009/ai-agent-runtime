package usageanalytics

import (
	"strings"
	"sync"
	"time"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// Options 是 Attach 的挂载参数。
type Options struct {
	// Config 数据库打开参数；Path 为空时使用 DefaultDBPath()。
	Config
	// Lookup 会话元数据补齐来源（best-effort，可为 nil）。
	Lookup SessionMetaLookup
	// History 消息/turn 反查来源，供缓存端点 message trace 使用（可为 nil）。
	History cacheanalytics.HistoryLookup
	// SupportsSSE 覆盖缓存能力位；默认 true。
	SupportsSSE bool
	// Now 时间源（测试注入）；nil 取 time.Now。
	Now func() time.Time
}

// Service 是进程内单例的分析服务：同一 SQLite 库同时承载
//   - 事件驱动写入（usage_requests / usage_sessions）；
//   - /usage 分析查询（ListSessions/Summarize/Dimensions/SessionUsage）；
//   - /sessions/{id}/cache 缓存端点（cacheanalytics.Source 实现）。
type Service struct {
	store     *Store
	collector *collector
	source    cacheanalytics.Source
	// cache 服务端短 TTL 查询缓存（§8.2）；只读降级路径不经过缓存。
	cache *analyticsQueryCache

	closeOnce sync.Once
}

// Attach 打开分析库、订阅事件总线并返回服务实例。
// bus 为 nil 时仍打开数据库（只读/查询形态），不订阅事件。
func Attach(bus *runtimeevents.Bus, opts Options) (*Service, error) {
	store, err := Open(opts.Config)
	if err != nil {
		return nil, err
	}
	service := &Service{store: store, cache: newAnalyticsQueryCache(opts.Now)}
	if bus != nil {
		service.collector = newCollector(store, opts.Lookup, opts.Now)
		service.collector.subscribe(bus)
	}
	service.source = NewCacheSource(store, opts.History, opts.SupportsSSE)
	return service, nil
}

// Close 取消事件订阅并关闭数据库（幂等，签名兼容 func() 清理钩子）。
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		if s.collector != nil {
			s.collector.close()
			s.collector = nil
		}
		_ = s.store.Close()
	})
}

// Store 返回底层分析库句柄（nil 安全）。
func (s *Service) Store() *Store {
	if s == nil {
		return nil
	}
	return s.store
}

// Source 返回缓存端点数据源（同库，cache.analytics.v1 契约不变）。
func (s *Service) Source() cacheanalytics.Source {
	if s == nil {
		return nil
	}
	return s.source
}

// DBPath 返回分析库路径。
func (s *Service) DBPath() string {
	if s == nil {
		return ""
	}
	return s.store.Path()
}

// Query 返回底层查询句柄；空库（只读降级）时返回 nil，调用方给出空结果。
func (s *Service) Query() *Store {
	if s == nil || s.store.Empty() {
		return nil
	}
	return s.store
}

// ============================================================================
// 查询委托：/api/runtime/analytics/* 直接调用（DB-only）。
// ============================================================================

// ListSessions 返回过滤 + 分页后的会话 rollup。
func (s *Service) ListSessions(q Query) (ListResult, error) {
	if store := s.Query(); store != nil {
		if cached, ok := s.cacheLookup("sessions", q); ok {
			return cached.(ListResult), nil
		}
		result, err := store.ListSessions(q)
		if err != nil {
			return ListResult{}, err
		}
		s.cachePut("sessions", q, result)
		return result, nil
	}
	return emptyListResult(q.Limit, q.Offset), nil
}

// Summarize 返回 group_by 聚合统计。
func (s *Service) Summarize(q Query) (SummaryResult, error) {
	if store := s.Query(); store != nil {
		if cached, ok := s.cacheLookup("summary", q); ok {
			return cached.(SummaryResult), nil
		}
		result, err := store.Summarize(q)
		if err != nil {
			return SummaryResult{}, err
		}
		s.cachePut("summary", q, result)
		return result, nil
	}
	return emptySummaryResult(q.GroupBy), nil
}

// Dimensions 返回 distinct 过滤维度。
func (s *Service) Dimensions(q Query) (DimensionsResult, error) {
	if store := s.Query(); store != nil {
		if cached, ok := s.cacheLookup("dimensions", q); ok {
			return cached.(DimensionsResult), nil
		}
		result, err := store.Dimensions(q)
		if err != nil {
			return DimensionsResult{}, err
		}
		s.cachePut("dimensions", q, result)
		return result, nil
	}
	return DimensionsResult{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		Providers:     []string{},
		Models:        []string{},
		Directories:   []string{},
		Projects:      []string{},
		Statuses:      []string{},
	}, nil
}

// SessionUsage 返回单会话明细（turns 由 usage_requests 分组派生）。
func (s *Service) SessionUsage(sessionID string) (SessionUsageDetail, error) {
	if store := s.Query(); store != nil {
		return store.SessionUsage(sessionID)
	}
	return SessionUsageDetail{}, errSessionNotFound(sessionID)
}

// ToolStats 返回工具维度聚合（schema v2；空库返回空数组）。
func (s *Service) ToolStats(q ToolStatsQuery) (ToolStatsResult, error) {
	if store := s.Query(); store != nil {
		return store.ToolStats(q)
	}
	return ToolStatsResult{SchemaVersion: SchemaVersion, GeneratedAt: time.Now().UTC(), Tools: []ToolStat{}}, nil
}

// ToolStatsDetail 返回单个工具的百分位耗时与错误码 Top-N。
func (s *Service) ToolStatsDetail(q ToolStatsQuery, toolName string) (ToolStat, error) {
	if store := s.Query(); store != nil {
		return store.ToolStatsDetail(q, toolName)
	}
	return ToolStat{ToolName: toolName}, nil
}

// SubagentStats 返回子代理维度聚合（schema v2；空库返回空数组）。
func (s *Service) SubagentStats(q SubagentStatsQuery) (SubagentStatsResult, error) {
	if store := s.Query(); store != nil {
		return store.SubagentStats(q)
	}
	return SubagentStatsResult{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		Summary: SubagentStatsSummary{
			FailureCategories: map[string]int{},
			Sources:           map[string]int{},
		},
		Subagents: []SubagentStat{},
	}, nil
}

// ErrorPatterns 返回失败模式 Top-N（schema v2；空库返回空数组）。
func (s *Service) ErrorPatterns(q ErrorPatternsQuery) (ErrorPatternsResult, error) {
	if store := s.Query(); store != nil {
		return store.ErrorPatterns(q)
	}
	return ErrorPatternsResult{SchemaVersion: SchemaVersion, GeneratedAt: time.Now().UTC(), Patterns: []ErrorPattern{}}, nil
}

// ============================================================================
// 路径解析
// ============================================================================

// ResolvePath 返回分析库路径：优先使用 runtime store 同目录，
// 未配置 runtime store 时回退默认位置
// （~/.aicli/sessions/runtime/usage_analytics.sqlite）。
func ResolvePath(runtimeStorePath string) string {
	if derived := PathFromRuntimeStore(runtimeStorePath); derived != "" {
		return derived
	}
	return DefaultDBPath()
}

// NormalizePath 清理并补全调用方传入的显式路径；空值返回默认路径。
func NormalizePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return DefaultDBPath()
	}
	return trimmed
}
