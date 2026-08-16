package render

import (
	"hash"
	"hash/fnv"
	"io"
	"sync"
)

// wrapCacheMemo memoizes Wrap results keyed by the line's expanded content
// fingerprint, terminal width, and wrap options.
//
// Streaming renders re-wrap the same stable lines on every frame (only the
// tail of the viewport changes per chunk), so the memo turns the uniseg hot
// path into a fingerprint pass for unchanged lines.
//
// Correctness contract: the fingerprint is only a lookup hint. On a hit the
// cached entry is re-validated against the caller's expanded spans, so even a
// hash collision can never return the wrong wrapping; it degrades to a miss.
// Stored wrapped lines are cloned on every hit so callers may mutate the
// returned lines freely.
type wrapCacheMemo struct {
	mu    sync.Mutex
	byKey map[wrapCacheKey]*wrapCacheEntry
	max   int
	seq   uint64
	hits  uint64
	miss  uint64
}

type wrapCacheKey struct {
	width     int
	tabWidth  int
	breakWord bool
	hash      uint64
}

type wrapCacheEntry struct {
	expanded []Span // expanded spans used to validate the hit
	wrapped  []Line
	lastUsed uint64
}

var wrapMemo = &wrapCacheMemo{
	byKey: make(map[wrapCacheKey]*wrapCacheEntry),
	max:   1024,
}

// wrapCached looks up a memoized Wrap result. Returns ok=false on a miss or
// when the fingerprint collides with different content (validated). The
// expanded spans and fingerprint are returned so a miss can be stored without
// re-walking the line.
func wrapCached(line Line, width int, opts WrapOptions) ([]Line, bool, []Span, uint64) {
	tabWidth := opts.TabWidth
	if tabWidth <= 0 {
		tabWidth = 4
	}
	expanded, hash := expandLineSpans(line, tabWidth)
	key := wrapCacheKey{
		width:     width,
		tabWidth:  tabWidth,
		breakWord: opts.BreakWord,
		hash:      hash,
	}
	wrapMemo.mu.Lock()
	entry, ok := wrapMemo.byKey[key]
	if ok {
		wrapMemo.seq++
		entry.lastUsed = wrapMemo.seq
		wrapMemo.hits++
	}
	wrapMemo.mu.Unlock()
	if !ok {
		return nil, false, expanded, hash
	}
	if !expandedSpansEqual(expanded, entry.expanded) {
		return nil, false, expanded, hash // fingerprint collision with different content
	}
	return cloneLines(entry.wrapped), true, expanded, hash
}

// wrapStore records a freshly computed Wrap result so later renders of the
// same line can skip the grapheme segmentation entirely. The stored copy is
// cloned: callers keep ownership of the result they computed and may mutate
// it freely without corrupting the cache.
func wrapStore(expanded []Span, hash uint64, width int, opts WrapOptions, wrapped []Line) {
	tabWidth := opts.TabWidth
	if tabWidth <= 0 {
		tabWidth = 4
	}
	key := wrapCacheKey{
		width:     width,
		tabWidth:  tabWidth,
		breakWord: opts.BreakWord,
		hash:      hash,
	}
	wrapMemo.mu.Lock()
	wrapMemo.miss++
	wrapMemo.seq++
	wrapMemo.byKey[key] = &wrapCacheEntry{
		expanded: cloneSpans(expanded),
		wrapped:  cloneLines(wrapped),
		lastUsed: wrapMemo.seq,
	}
	if len(wrapMemo.byKey) > wrapMemo.max {
		wrapMemo.evictOldestLocked()
	}
	wrapMemo.mu.Unlock()
}

// evictOldestLocked drops the single least-recently-used entry. The map is
// bounded by wrapCacheMemo.max, so eviction runs only when the cache is full
// and the linear scan over at most max entries is amortized across renders.
func (m *wrapCacheMemo) evictOldestLocked() {
	if len(m.byKey) == 0 {
		return
	}
	var oldestKey wrapCacheKey
	oldestUsed := ^uint64(0)
	for key, entry := range m.byKey {
		if entry.lastUsed < oldestUsed {
			oldestUsed = entry.lastUsed
			oldestKey = key
		}
	}
	delete(m.byKey, oldestKey)
}

var (
	sep0  = []byte{0}
	sepFF = []byte{0xff}
)

// expandLineSpans expands tabs and fingerprints the line in a single pass.
// The expanded spans double as the authoritative content for hit validation.
func expandLineSpans(line Line, tabWidth int) ([]Span, uint64) {
	h := fnv.New64a()
	expanded := make([]Span, 0, len(line.Spans))
	for _, span := range line.Spans {
		text := expandTabs(span.Text, tabWidth)
		expanded = append(expanded, Span{Text: text, Style: span.Style, Link: span.Link})
		io.WriteString(h, text)
		h.Write(sep0)
		writeStyleHash(h, span.Style)
		io.WriteString(h, span.Link)
		h.Write(sepFF)
	}
	return expanded, h.Sum64()
}

// writeStyleHash folds a Style's presentation fields into the fingerprint.
// Role is included as text so identical colors with distinct roles stay
// distinct cache entries.
func writeStyleHash(h hash.Hash64, s Style) {
	var buf [11]byte
	buf[0] = byte(s.Foreground.Kind)
	buf[1] = s.Foreground.Index
	buf[2] = s.Foreground.R
	buf[3] = s.Foreground.G
	buf[4] = s.Foreground.B
	buf[5] = byte(s.Background.Kind)
	buf[6] = s.Background.Index
	buf[7] = s.Background.R
	buf[8] = s.Background.G
	buf[9] = s.Background.B
	if s.Bold {
		buf[10] |= 1
	}
	if s.Dim {
		buf[10] |= 2
	}
	if s.Italic {
		buf[10] |= 4
	}
	if s.Underline {
		buf[10] |= 8
	}
	if s.Reverse {
		buf[10] |= 16
	}
	h.Write(buf[:])
	io.WriteString(h, s.Role)
	h.Write(sep0)
}

// expandedSpansEqual validates a cache hit against the caller's content.
func expandedSpansEqual(a, b []Span) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Text != b[i].Text || a[i].Link != b[i].Link || a[i].Style != b[i].Style {
			return false
		}
	}
	return true
}

// cloneSpans copies a span slice so the cache owns its validation content.
func cloneSpans(spans []Span) []Span {
	return append([]Span(nil), spans...)
}

// cloneLines deep-copies wrapped lines so callers can mutate them without
// corrupting the cache. Spans are value types, so one slice copy per line
// suffices.
func cloneLines(lines []Line) []Line {
	out := make([]Line, len(lines))
	for i, line := range lines {
		out[i] = cloneLine(line)
	}
	return out
}
