package render

import (
	"container/list"
	"hash"
	"hash/fnv"
	"io"
	"strings"
	"sync"
)

const (
	defaultWrapCacheEntries       = 1024
	defaultWrapCachePayloadBytes  = 16 << 20
	defaultWrapCacheMaxEntryBytes = 1 << 20
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
// returned lines freely. Entry count and retained string bytes are independently
// bounded; the byte counter is not a complete heap-size estimate.
type wrapCacheMemo struct {
	mu            sync.Mutex
	byKey         map[wrapCacheKey]*list.Element
	lru           *list.List // Front = least recently used, Back = most recent
	max           int
	maxBytes      int
	maxEntryBytes int
	payloadBytes  int
	hits          uint64
	miss          uint64
	evictions     uint64
	skipped       uint64
}

type wrapCacheKey struct {
	width     int
	tabWidth  int
	breakWord bool
	hash      uint64
}

type wrapCacheEntry struct {
	key          wrapCacheKey
	expanded     []Span // expanded spans used to validate the hit
	wrapped      []Line
	payloadBytes int
}

var wrapMemo = &wrapCacheMemo{
	byKey:         make(map[wrapCacheKey]*list.Element),
	lru:           list.New(),
	max:           defaultWrapCacheEntries,
	maxBytes:      defaultWrapCachePayloadBytes,
	maxEntryBytes: defaultWrapCacheMaxEntryBytes,
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
	el, ok := wrapMemo.byKey[key]
	if !ok {
		wrapMemo.miss++
		wrapMemo.mu.Unlock()
		return nil, false, expanded, hash
	}
	entry := el.Value.(*wrapCacheEntry)
	if !expandedSpansEqual(expanded, entry.expanded) {
		// A fingerprint collision is a miss. Do not promote the unrelated
		// entry; wrapStore will replace it with the caller's exact content.
		wrapMemo.miss++
		wrapMemo.mu.Unlock()
		return nil, false, expanded, hash
	}
	wrapMemo.lru.MoveToBack(el)
	wrapMemo.hits++
	wrapMemo.mu.Unlock()
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
	payloadBytes := wrapRetainedStringBytes(expanded, wrapped)
	memo := wrapMemo
	memo.mu.Lock()
	if !memo.admitsLocked(payloadBytes) {
		memo.skipped++
		memo.mu.Unlock()
		return
	}
	memo.mu.Unlock()
	entry := &wrapCacheEntry{
		key:          key,
		expanded:     cloneOwnedSpans(expanded),
		wrapped:      cloneOwnedLines(wrapped),
		payloadBytes: payloadBytes,
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	if !memo.admitsLocked(payloadBytes) {
		memo.skipped++
		return
	}
	if el, ok := memo.byKey[key]; ok {
		previous := el.Value.(*wrapCacheEntry)
		el.Value = entry
		memo.payloadBytes += entry.payloadBytes - previous.payloadBytes
		memo.lru.MoveToBack(el)
		for memo.overLimitLocked() {
			memo.evictOldestLocked()
		}
		return
	}
	el := memo.lru.PushBack(entry)
	memo.byKey[key] = el
	memo.payloadBytes += entry.payloadBytes
	for memo.overLimitLocked() {
		memo.evictOldestLocked()
	}
}

func (m *wrapCacheMemo) admitsLocked(payloadBytes int) bool {
	return m.max > 0 &&
		(m.maxEntryBytes <= 0 || payloadBytes <= m.maxEntryBytes) &&
		(m.maxBytes <= 0 || payloadBytes <= m.maxBytes)
}

func (m *wrapCacheMemo) overLimitLocked() bool {
	return m.lru.Len() > m.max || (m.maxBytes > 0 && m.payloadBytes > m.maxBytes)
}

// evictOldestLocked drops one least-recently-used entry in O(1).
func (m *wrapCacheMemo) evictOldestLocked() {
	front := m.lru.Front()
	if front == nil {
		return
	}
	m.lru.Remove(front)
	entry := front.Value.(*wrapCacheEntry)
	delete(m.byKey, entry.key)
	m.payloadBytes -= entry.payloadBytes
	if m.payloadBytes < 0 {
		m.payloadBytes = 0
	}
	m.evictions++
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

func wrapRetainedStringBytes(expanded []Span, wrapped []Line) int {
	total := retainedSpanStringBytes(expanded)
	for _, line := range wrapped {
		total += len(line.Style.Role)
		total += retainedSpanStringBytes(line.Spans)
	}
	return total
}

func retainedSpanStringBytes(spans []Span) int {
	total := 0
	for _, span := range spans {
		total += len(span.Text) + len(span.Link) + len(span.Style.Role)
	}
	return total
}

// cloneOwnedSpans gives the cache compact string storage as well as its own
// slice, so a wrapped substring cannot pin an arbitrarily large source buffer.
func cloneOwnedSpans(spans []Span) []Span {
	out := make([]Span, len(spans))
	for index, span := range spans {
		span.Text = strings.Clone(span.Text)
		span.Link = strings.Clone(span.Link)
		span.Style.Role = strings.Clone(span.Style.Role)
		out[index] = span
	}
	return out
}

func cloneOwnedLines(lines []Line) []Line {
	out := make([]Line, len(lines))
	for index, line := range lines {
		line.Style.Role = strings.Clone(line.Style.Role)
		line.Spans = cloneOwnedSpans(line.Spans)
		out[index] = line
	}
	return out
}

// cloneLines deep-copies wrapped lines so callers can mutate them without
// corrupting the cache. Strings are immutable, so hit results may safely share
// the cache-owned compact string storage.
func cloneLines(lines []Line) []Line {
	out := make([]Line, len(lines))
	for i, line := range lines {
		out[i] = cloneLine(line)
	}
	return out
}
