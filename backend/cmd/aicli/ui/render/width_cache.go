package render

import "sync"

// widthCacheMemo memoizes Width results keyed by exact text.
//
// Streaming renders measure the same stable strings on every frame (line
// widths, truncation budgets, marker widths), so the memo turns uniseg's
// grapheme segmentation hot path into a map lookup for unchanged text.
//
// Unlike wrapCacheMemo there is no fingerprint: the string itself is the key,
// so a hit is always exact and there is nothing to re-validate. The map is
// bounded (evictOldestLocked runs when full) so long-running sessions do not
// grow without limit.
type widthCacheMemo struct {
	mu    sync.Mutex
	byKey map[string]widthCacheEntry
	max   int
	seq   uint64
	hits  uint64
	miss  uint64
}

type widthCacheEntry struct {
	w        int
	lastUsed uint64
}

var widthMemo = &widthCacheMemo{
	byKey: make(map[string]widthCacheEntry),
	max:   2048,
}

// get returns the cached width for text, if present.
func (m *widthCacheMemo) get(text string) (int, bool) {
	m.mu.Lock()
	entry, ok := m.byKey[text]
	if ok {
		m.seq++
		entry.lastUsed = m.seq
		m.byKey[text] = entry
		m.hits++
	}
	m.mu.Unlock()
	return entry.w, ok
}

// store records a freshly computed width. The bound is checked on insert, so
// eviction runs only when the cache is full; the linear scan over at most max
// entries is amortized across measurements.
func (m *widthCacheMemo) store(text string, w int) {
	m.mu.Lock()
	m.miss++
	m.seq++
	m.byKey[text] = widthCacheEntry{w: w, lastUsed: m.seq}
	if len(m.byKey) > m.max {
		m.evictOldestLocked()
	}
	m.mu.Unlock()
}

// evictOldestLocked drops the single least-recently-used entry.
func (m *widthCacheMemo) evictOldestLocked() {
	if len(m.byKey) == 0 {
		return
	}
	var oldestKey string
	oldestUsed := ^uint64(0)
	for key, entry := range m.byKey {
		if entry.lastUsed < oldestUsed {
			oldestUsed = entry.lastUsed
			oldestKey = key
		}
	}
	delete(m.byKey, oldestKey)
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
