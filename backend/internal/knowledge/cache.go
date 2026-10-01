package knowledge

// compile 层缓存（06 §4 Phase 6 切片 2；04 §7.2「缓存命中率 ≥ 50%（compile 层）」、
// 04 §5 Phase 6 门槛「缓存命中 p95 < 50ms、未命中 p95 < 200ms」）。
//
// 设计要点：
//   - **键是输入的函数**：workspace + 计划输入 + 编译策略 + 知识版本 + 编译器版本
//     （CompileCacheVersion）。知识版本变化 = 天然未命中，不需要跨表失效广播；
//   - **缓存是加速器，不是可用性依赖**：任何 store 故障（reader 角色只读、
//     载荷损坏、锁冲突）都降级为直算（Degrade-Not-Fail）并计入 Errors；
//   - **窄接口**：CompileCacheStore 只含 cache_entries 需要的四个方法，单测无需
//     构造完整 Store（与 ExplorationNodeReader 同思路）；`*sqliteStore` 直接满足，
//     未实现该接口的 Store（测试假体）自动退化为"无缓存"；
//   - **可解释**：命中/未命中/错误计数与两侧时延分位由 CompileCacheMetrics 暴露，
//     供 04 §7.2 的缓存命中率与 04 §5 的 p95 门槛复现。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// CacheTypeCompile 是 cache_entries.cache_type 的 compile 层取值。
	CacheTypeCompile = "compile"
	// CacheTypeRetrieval 是检索层缓存类型（Phase 7 备用；本切片不写入）。
	CacheTypeRetrieval = "retrieval"
	// CacheTypeSummary 是摘要层缓存类型（Observation Compressor 备用）。
	CacheTypeSummary = "summary"

	// CompileCacheVersion 是**编译器版本**：编译语义（信任映射 / 冲突序 /
	// 过滤规则 / 渲染口径）变化时必须递增——键里包含它，旧载荷不会被新代码复用。
	CompileCacheVersion = "compile-v1"

	// DefaultCompileCacheTTL 是 compile 层条目的默认存活期。
	//
	// 版本已在键里，TTL 只用于兜底回收（库体积/陈旧策略），不承担正确性。
	DefaultCompileCacheTTL = 15 * time.Minute

	// defaultCompileCacheSamples 是时延分位样本上限（有界环形缓冲）。
	defaultCompileCacheSamples = 512
)

// cacheTypeOrder 是 cache_entries.cache_type 的取值闭集（0001_init.sql 注释）。
var cacheTypeOrder = []string{CacheTypeCompile, CacheTypeRetrieval, CacheTypeSummary}

// ValidCacheType 报告缓存类型是否在闭集内。
func ValidCacheType(cacheType string) bool {
	for _, known := range cacheTypeOrder {
		if cacheType == known {
			return true
		}
	}
	return false
}

// CacheEntry 是 cache_entries 一行的语义镜像。
//
// ExpiresAt 零值表示**永不过期**（对应 SQL 的 NULL）；compile 层写入时总是带 TTL。
type CacheEntry struct {
	ID               string    `json:"id"`
	WorkspaceID      string    `json:"workspace_id"`
	CacheKey         string    `json:"cache_key"`
	CacheType        string    `json:"cache_type"`
	PayloadJSON      string    `json:"payload_json"`
	KnowledgeVersion string    `json:"knowledge_version"`
	CreatedAt        time.Time `json:"created_at"`
	ExpiresAt        time.Time `json:"expires_at,omitempty"`
}

// Validate 校验落库必需字段（幂等；ID 可缺省，由 CacheEntryID 派生）。
func (e CacheEntry) Validate() error {
	if strings.TrimSpace(e.WorkspaceID) == "" {
		return fmt.Errorf("knowledge: cache entry: workspace_id is required")
	}
	if strings.TrimSpace(e.CacheKey) == "" {
		return fmt.Errorf("knowledge: cache entry: cache_key is required")
	}
	if !ValidCacheType(e.CacheType) {
		return fmt.Errorf("knowledge: cache entry: invalid cache_type %q", e.CacheType)
	}
	if strings.TrimSpace(e.PayloadJSON) == "" {
		return fmt.Errorf("knowledge: cache entry: payload_json is required")
	}
	return nil
}

// CacheEntryID 返回 cache_entries 行的稳定主键（与 idx_cache_key 的唯一键同形）。
func CacheEntryID(workspaceID, cacheType, cacheKey string) string {
	return "ce_" + digest(workspaceID, cacheType, cacheKey)
}

// CompileCacheStore 是 compile 层缓存所需的最小 store 读/写接口。
//
// 读方法在 owner/reader 两种角色都可用；写方法在 reader 角色由 store 硬失败
// （ErrReadOnlyStore），调用方（CompileCache.Do）会降级为直算并计数。
type CompileCacheStore interface {
	// GetCacheEntry 按唯一键 (workspace_id, cache_type, cache_key) 读取；
	// 不存在返回 (零值, false, nil)。
	GetCacheEntry(ctx context.Context, workspaceID, cacheType, cacheKey string) (CacheEntry, bool, error)
	// PutCacheEntry 以 upsert 语义写入（同键覆盖）。
	PutCacheEntry(ctx context.Context, entry CacheEntry) error
	// DeleteCacheEntry 删除一条；不存在不是错误（幂等）。
	DeleteCacheEntry(ctx context.Context, workspaceID, cacheType, cacheKey string) error
	// PurgeExpiredCacheEntries 清理 expires_at 已过的条目，最多 limit 条；
	// 返回清理条数。ExpiresAt 为空（NULL）的条目永不清理。
	PurgeExpiredCacheEntries(ctx context.Context, now time.Time, limit int) (int, error)
}

// CompileCacheKeyInput 是 compile 层缓存键的输入。
//
// 归一化：Query/TaskID/SessionID 做 TrimSpace；Mode/Scope 做 TrimSpace+ToLower；
// 其余字段原样。**知识版本必须来自 Layer.ObserveVersion 的观测 token**（含
// `#pendingN` 未稳定标记时键自然变化，旧载荷不会被复用）。
type CompileCacheKeyInput struct {
	WorkspaceID      string  `json:"workspace_id"`
	TaskID           string  `json:"task_id,omitempty"`
	SessionID        string  `json:"session_id,omitempty"`
	Query            string  `json:"query"`
	Scope            string  `json:"scope,omitempty"`
	Write            bool    `json:"write,omitempty"`
	Mode             string  `json:"mode,omitempty"`
	TokenBudget      int     `json:"token_budget,omitempty"`
	ConfidenceFloor  float64 `json:"confidence_floor,omitempty"`
	KnowledgeVersion string  `json:"knowledge_version"`
}

// CompileCacheKey 返回确定性缓存键（sha256 hex；含编译器版本）。
func CompileCacheKey(in CompileCacheKeyInput) string {
	normalized := struct {
		CompilerVersion  string  `json:"compiler_version"`
		WorkspaceID      string  `json:"workspace_id"`
		TaskID           string  `json:"task_id"`
		SessionID        string  `json:"session_id"`
		Query            string  `json:"query"`
		Scope            string  `json:"scope"`
		Write            bool    `json:"write"`
		Mode             string  `json:"mode"`
		TokenBudget      int     `json:"token_budget"`
		ConfidenceFloor  float64 `json:"confidence_floor"`
		KnowledgeVersion string  `json:"knowledge_version"`
	}{
		CompilerVersion:  CompileCacheVersion,
		WorkspaceID:      strings.TrimSpace(in.WorkspaceID),
		TaskID:           strings.TrimSpace(in.TaskID),
		SessionID:        strings.TrimSpace(in.SessionID),
		Query:            strings.TrimSpace(in.Query),
		Scope:            strings.ToLower(strings.TrimSpace(in.Scope)),
		Write:            in.Write,
		Mode:             strings.ToLower(strings.TrimSpace(in.Mode)),
		TokenBudget:      in.TokenBudget,
		ConfidenceFloor:  in.ConfidenceFloor,
		KnowledgeVersion: strings.TrimSpace(in.KnowledgeVersion),
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		// 结构体字段全部可序列化，此处不可达；保守回退到版本前缀，保证不 panic。
		payload = []byte(CompileCacheVersion)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// CompileFunc 是编译内核签名（默认 CompilePlan）。
type CompileFunc func(CompileRequest) CompileResult

// CompileCacheSnapshot 是一次缓存指标的只读快照。
type CompileCacheSnapshot struct {
	Hits   int64 `json:"hits"`
	Misses int64 `json:"misses"`
	Errors int64 `json:"errors"`
	// 时延分位（毫秒，最近邻取法；无样本为 0）。
	HitP50MS  float64 `json:"hit_p50_ms"`
	HitP95MS  float64 `json:"hit_p95_ms"`
	MissP50MS float64 `json:"miss_p50_ms"`
	MissP95MS float64 `json:"miss_p95_ms"`
	// HitRate 是命中率（Hits / (Hits+Misses)）；无样本为 0。
	HitRate float64 `json:"hit_rate"`
}

// CompileCacheMetrics 记录 compile 层缓存的命中/未命中/错误与时延样本（有界）。
type CompileCacheMetrics struct {
	mu          sync.Mutex
	hits        int64
	misses      int64
	errors      int64
	hitSamples  []time.Duration
	missSamples []time.Duration
	sampleCap   int
}

// NewCompileCacheMetrics 返回带默认样本上限的指标记录器。
func NewCompileCacheMetrics() *CompileCacheMetrics {
	return &CompileCacheMetrics{sampleCap: defaultCompileCacheSamples}
}

func (m *CompileCacheMetrics) recordHit(latency time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hits++
	m.hitSamples = appendSample(m.hitSamples, latency, m.cap())
}

func (m *CompileCacheMetrics) recordMiss(latency time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.misses++
	m.missSamples = appendSample(m.missSamples, latency, m.cap())
}

// RecordError 记录一次缓存路径故障（降级直算）。
func (m *CompileCacheMetrics) RecordError() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errors++
}

func (m *CompileCacheMetrics) cap() int {
	if m.sampleCap <= 0 {
		return defaultCompileCacheSamples
	}
	return m.sampleCap
}

// Snapshot 返回只读快照（分位用最近邻取法，样本不足时取到最大值）。
func (m *CompileCacheMetrics) Snapshot() CompileCacheSnapshot {
	if m == nil {
		return CompileCacheSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := CompileCacheSnapshot{
		Hits:      m.hits,
		Misses:    m.misses,
		Errors:    m.errors,
		HitP50MS:  durationPercentileMS(m.hitSamples, 0.50),
		HitP95MS:  durationPercentileMS(m.hitSamples, 0.95),
		MissP50MS: durationPercentileMS(m.missSamples, 0.50),
		MissP95MS: durationPercentileMS(m.missSamples, 0.95),
	}
	if total := snapshot.Hits + snapshot.Misses; total > 0 {
		snapshot.HitRate = float64(snapshot.Hits) / float64(total)
	}
	return snapshot
}

// appendSample 追加样本并保持有界（丢弃最旧）。
func appendSample(samples []time.Duration, value time.Duration, capacity int) []time.Duration {
	if capacity <= 0 {
		capacity = defaultCompileCacheSamples
	}
	samples = append(samples, value)
	if len(samples) > capacity {
		samples = samples[len(samples)-capacity:]
	}
	return samples
}

// durationPercentileMS 返回时延分位（毫秒，最近邻取法：ceil(p*n)-1）。
func durationPercentileMS(samples []time.Duration, p float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	ordered := make([]time.Duration, len(samples))
	copy(ordered, samples)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := int(float64(len(ordered))*p+0.999999) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(ordered) {
		index = len(ordered) - 1
	}
	return float64(ordered[index]) / float64(time.Millisecond)
}

// CompileCache 是「规划输入 → 编译结果」的缓存执行器。
//
// 用法（切片 3 接线）：key := CompileCacheKeyInput{...观测到的知识版本...}；
// result := cache.Do(ctx, key, CompileRequest{Plan: plan, ...})。
type CompileCache struct {
	// Store 是缓存存储；nil 时 Do 恒为直算（无缓存）。
	Store CompileCacheStore
	// Now 便于测试注入时钟；nil 时用 time.Now。
	Now func() time.Time
	// TTL 是写入条目的存活期；<=0 取 DefaultCompileCacheTTL。
	TTL time.Duration
	// Compile 是编译内核；nil 时用 CompilePlan。
	Compile CompileFunc
	// Metrics 是命中/时延记录器；nil 时懒建。
	Metrics *CompileCacheMetrics
}

// NewCompileCache 返回带默认指标记录器的缓存执行器。
func NewCompileCache(store CompileCacheStore) *CompileCache {
	return &CompileCache{Store: store, Metrics: NewCompileCacheMetrics()}
}

func (c *CompileCache) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *CompileCache) ttl() time.Duration {
	if c != nil && c.TTL > 0 {
		return c.TTL
	}
	return DefaultCompileCacheTTL
}

func (c *CompileCache) metrics() *CompileCacheMetrics {
	if c.Metrics == nil {
		c.Metrics = NewCompileCacheMetrics()
	}
	return c.Metrics
}

// Do 执行一次带缓存的编译：命中直接返回（CacheHit=true）；未命中执行编译并回填。
//
// 降级口径（Degrade-Not-Fail）：Store 为 nil、workspace 为空、读失败、载荷损坏、
// 版本不符、已过期、写失败——全部回退直算，绝不向调用方冒泡缓存错误。
// 过期/版本不符的条目会尽力删除（失败不影响主路径）。
func (c *CompileCache) Do(ctx context.Context, key CompileCacheKeyInput, req CompileRequest) CompileResult {
	if ctx == nil {
		ctx = context.Background()
	}
	started := time.Now()
	compile := CompilePlan
	if c != nil && c.Compile != nil {
		compile = c.Compile
	}
	// 无缓存可用：直算（不计入命中/未命中，避免污染命中率口径）。
	if c == nil || c.Store == nil || strings.TrimSpace(key.WorkspaceID) == "" {
		return compile(req)
	}

	cacheKey := CompileCacheKey(key)
	now := c.now()
	entry, found, err := c.Store.GetCacheEntry(ctx, key.WorkspaceID, CacheTypeCompile, cacheKey)
	switch {
	case err != nil:
		c.metrics().RecordError()
	case found:
		if usable, _ := compileCacheEntryUsable(entry, key, now); usable {
			if result, ok := decodeCompileResult(entry.PayloadJSON); ok {
				result.CacheHit = true
				c.metrics().recordHit(time.Since(started))
				return result
			}
			c.metrics().RecordError() // 载荷损坏：降级直算
		} else {
			_ = c.Store.DeleteCacheEntry(ctx, key.WorkspaceID, CacheTypeCompile, cacheKey)
		}
	}

	result := compile(req)
	if payload, err := json.Marshal(result); err != nil {
		c.metrics().RecordError()
	} else {
		entry := CacheEntry{
			ID:               CacheEntryID(key.WorkspaceID, CacheTypeCompile, cacheKey),
			WorkspaceID:      strings.TrimSpace(key.WorkspaceID),
			CacheKey:         cacheKey,
			CacheType:        CacheTypeCompile,
			PayloadJSON:      string(payload),
			KnowledgeVersion: strings.TrimSpace(key.KnowledgeVersion),
			CreatedAt:        now,
			ExpiresAt:        now.Add(c.ttl()),
		}
		if err := c.Store.PutCacheEntry(ctx, entry); err != nil {
			c.metrics().RecordError()
		}
	}
	c.metrics().recordMiss(time.Since(started))
	return result
}

// compileCacheEntryUsable 判定已读条目是否可用于本次请求。
//
// 键已包含版本，此处再做一次显式守卫（防御：手工写入/旧键格式/时钟回拨）。
func compileCacheEntryUsable(entry CacheEntry, key CompileCacheKeyInput, now time.Time) (bool, string) {
	if strings.TrimSpace(entry.PayloadJSON) == "" {
		return false, "empty_payload"
	}
	if entry.KnowledgeVersion != strings.TrimSpace(key.KnowledgeVersion) {
		return false, "version_mismatch"
	}
	if !entry.ExpiresAt.IsZero() && !now.Before(entry.ExpiresAt) {
		return false, "expired"
	}
	return true, ""
}

// decodeCompileResult 反序列化缓存载荷；失败返回 ok=false（调用方降级直算）。
func decodeCompileResult(payload string) (CompileResult, bool) {
	var result CompileResult
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		return CompileResult{}, false
	}
	return result, true
}
