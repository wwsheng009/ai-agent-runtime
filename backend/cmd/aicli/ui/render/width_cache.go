package render

import (
	"container/list"
	"strings"
	"sync"
)

const (
	defaultWidthCacheEntries     = 32768
	defaultWidthCacheKeyBytes    = 8 << 20
	defaultWidthCacheMaxKeyBytes = 64 << 10
)

// widthCacheMemo memoizes Width results keyed by exact text.
//
// Streaming renders measure the same stable strings on every frame (line
// widths, truncation budgets, marker widths), so the memo turns uniseg's
// grapheme segmentation hot path into a map lookup for unchanged text.
//
// Unlike wrapCacheMemo there is no fingerprint: the string itself is the key,
// so a hit is always exact and there is nothing to re-validate. The cache is
// a bounded LRU (map + container/list): eviction is O(1). Both entry count and
// retained key bytes are bounded; keys are cloned on admission so a short
// substring cannot pin an arbitrarily large source buffer.
//
// The previous implementation evicted the single oldest entry by scanning the
// whole map on every insert once the cache was full — O(max) per insert. In
// production (2026-08-26) that made evictOldestLocked the #1 CPU hotspot
// (~33%) while rendering a large pending history batch, starving the renderer.
// LRU eviction is O(1) per insert regardless of fill level.
type widthCacheMemo struct {
	mu    sync.Mutex
	byKey map[string]*list.Element
	lru   *list.List // Front = least recently used, Back = most recent
	max   int
	// maxBytes bounds the sum of admitted key lengths. It deliberately does
	// not claim to be a complete heap-size measurement; max still bounds map
	// and list overhead independently.
	maxBytes    int
	maxKeyBytes int
	keyBytes    int
	hits        uint64
	miss        uint64
	evictions   uint64
	skipped     uint64
}

// widthCacheEntry is the list.Value payload; it keeps the key so eviction can
// remove the map entry without a second lookup keyed by element.
type widthCacheEntry struct {
	key string
	w   int
}

var widthMemo = &widthCacheMemo{
	byKey:       make(map[string]*list.Element),
	lru:         list.New(),
	max:         defaultWidthCacheEntries,
	maxBytes:    defaultWidthCacheKeyBytes,
	maxKeyBytes: defaultWidthCacheMaxKeyBytes,
}

// get returns the cached width for text, if present. A hit refreshes recency
// (move-to-back), keeping the LRU order accurate across streaming frames.
// The width value is read while holding the lock: the entry pointer is shared
// with store (which may refresh it), so touching it after unlock would race.
func (m *widthCacheMemo) get(text string) (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.byKey[text]; ok {
		m.lru.MoveToBack(el)
		m.hits++
		return el.Value.(*widthCacheEntry).w, true
	}
	m.miss++
	return 0, false
}

// store records a freshly computed width. Re-storing an existing key updates
// the width and refreshes recency without growing the cache. The bound is
// enforced on insert by evicting the least-recently-used entry, which is O(1)
// with the list and never scans the whole map.
func (m *widthCacheMemo) store(text string, w int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.byKey[text]; ok {
		el.Value.(*widthCacheEntry).w = w
		m.lru.MoveToBack(el)
		return
	}
	if m.max <= 0 ||
		(m.maxKeyBytes > 0 && len(text) > m.maxKeyBytes) ||
		(m.maxBytes > 0 && len(text) > m.maxBytes) {
		m.skipped++
		return
	}
	// A map key normally retains the caller's backing storage. Own a compact
	// copy so cached fragments cannot retain a much larger streaming buffer.
	key := strings.Clone(text)
	el := m.lru.PushBack(&widthCacheEntry{key: key, w: w})
	m.byKey[key] = el
	m.keyBytes += len(key)
	for m.overLimitLocked() {
		m.evictOldestLocked()
	}
}

func (m *widthCacheMemo) overLimitLocked() bool {
	return m.lru.Len() > m.max || (m.maxBytes > 0 && m.keyBytes > m.maxBytes)
}

// evictOldestLocked drops one least-recently-used entry in O(1). An insertion
// may call it more than once when the key-byte budget, rather than entry count,
// is the active bound.
func (m *widthCacheMemo) evictOldestLocked() {
	front := m.lru.Front()
	if front == nil {
		return
	}
	m.lru.Remove(front)
	entry := front.Value.(*widthCacheEntry)
	delete(m.byKey, entry.key)
	m.keyBytes -= len(entry.key)
	if m.keyBytes < 0 {
		m.keyBytes = 0
	}
	m.evictions++
}

// fastASCIIWidth returns the width of a string consisting solely of printable
// ASCII (0x20..0x7e), each of which occupies exactly one terminal cell. This
// covers the vast majority of measure calls (markdown prose, code, status
// text) and avoids both the map lookup and uniseg for them. Any control or
// non-ASCII byte falls through to the cached uniseg path.
func fastASCIIWidth(text string) (int, bool) {
	w := 0
	for i := 0; i < len(text); i++ {
		if b := text[i]; b < 0x20 || b >= 0x7f {
			return 0, false
		}
		w++
	}
	return w, true
}
