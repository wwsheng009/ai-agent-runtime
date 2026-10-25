package mesh

import (
	"sync"
	"time"
)

// binding_view_cache.go —— BuildView 专用的 binding 短暂缓存。
//
// LoadBinding 本身按 (path, mtime, size) 缓存解析结果，但每次调用仍要 Stat。
// 一个 1,800 节点的目录中大量历史节点引用同一批 session binding；BuildView
// 在每个节点上重复 Stat 同一文件是纯浪费。
//
// 这里对有界 TTL 内的 view 读取直接复用结果。写路径（SaveBinding/TouchBinding）
// 会主动失效本缓存，因此本进程刚更新的绑定立即可见；跨进程更新最多延迟
// DefaultBindingViewTTL。端口选择/恢复等需要强一致的调用仍使用 LoadBinding。
const DefaultBindingViewTTL = 5 * time.Second

type bindingViewCacheEntry struct {
	binding   SessionBinding
	ok        bool
	expiresAt time.Time
}

var bindingViewCache sync.Map // path -> bindingViewCacheEntry

func loadBindingForView(paths Paths, sessionID string, now time.Time, ttl time.Duration) (SessionBinding, bool) {
	path := paths.BindingPath(sessionID)
	if path == "" {
		return SessionBinding{}, false
	}
	if now.IsZero() {
		now = NowUTC()
	}
	if ttl <= 0 {
		ttl = DefaultBindingViewTTL
	}
	if cached, found := bindingViewCache.Load(path); found {
		entry := cached.(bindingViewCacheEntry)
		if now.Before(entry.expiresAt) {
			return entry.binding, entry.ok
		}
	}
	binding, ok := LoadBinding(paths, sessionID)
	bindingViewCache.Store(path, bindingViewCacheEntry{
		binding:   binding,
		ok:        ok,
		expiresAt: now.Add(ttl),
	})
	return binding, ok
}

func invalidateBindingViewCache(path string) {
	if path != "" {
		bindingViewCache.Delete(path)
	}
}
