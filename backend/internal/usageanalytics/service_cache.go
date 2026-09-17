package usageanalytics

import (
	"sync"
	"time"
)

// ============================================================================
// 服务端查询缓存（方案 §8.2）：短 TTL + 写入代数惰性失效。
//
// 只缓存只读聚合路径（ListSessions / Summarize / Dimensions）；
// 不缓存单会话明细（SessionUsage）与错误模式等低频/明细路径。
//
// 失效策略：
//   - TTL 5s：多进程写入（其他 aicli 进程）本进程感知不到代数变化，兜底；
//   - 写入代数：本进程请求终态落库后 statsGen 自增，立即失效，避免读到旧页。
// ============================================================================

const (
	analyticsCacheTTL        = 5 * time.Second
	analyticsCacheMaxEntries = 256
)

type analyticsCacheKey struct {
	op string
	q  Query
}

type analyticsCacheEntry struct {
	value      interface{}
	expiresAt  time.Time
	generation uint64
}

// analyticsQueryCache 是进程内单例缓存；now 可注入（测试）。
type analyticsQueryCache struct {
	mu      sync.Mutex
	entries map[analyticsCacheKey]analyticsCacheEntry
	now     func() time.Time
}

func newAnalyticsQueryCache(now func() time.Time) *analyticsQueryCache {
	if now == nil {
		now = time.Now
	}
	return &analyticsQueryCache{
		entries: make(map[analyticsCacheKey]analyticsCacheEntry),
		now:     now,
	}
}

func (c *analyticsQueryCache) get(key analyticsCacheKey, generation uint64) (interface{}, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if entry.generation != generation || !c.now().Before(entry.expiresAt) {
		delete(c.entries, key)
		return nil, false
	}
	return entry.value, true
}

func (c *analyticsQueryCache) put(key analyticsCacheKey, generation uint64, value interface{}) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= analyticsCacheMaxEntries {
		now := c.now()
		for existing, entry := range c.entries {
			if !now.Before(entry.expiresAt) {
				delete(c.entries, existing)
			}
		}
		// 仍然超限：整体清空（分析库键空间有限，不做 LRU 驱逐）。
		if len(c.entries) >= analyticsCacheMaxEntries {
			c.entries = make(map[analyticsCacheKey]analyticsCacheEntry)
		}
	}
	c.entries[key] = analyticsCacheEntry{
		value:      value,
		expiresAt:  c.now().Add(analyticsCacheTTL),
		generation: generation,
	}
}

// lookup 缓存读；未命中/过期/代数变化返回 ok=false。
func (s *Service) cacheLookup(op string, q Query) (interface{}, bool) {
	if s == nil || s.cache == nil || s.store == nil {
		return nil, false
	}
	return s.cache.get(analyticsCacheKey{op: op, q: q}, s.store.statsGen.Load())
}

// cachePut 缓存写。
func (s *Service) cachePut(op string, q Query, value interface{}) {
	if s == nil || s.cache == nil || s.store == nil {
		return
	}
	s.cache.put(analyticsCacheKey{op: op, q: q}, s.store.statsGen.Load(), value)
}
