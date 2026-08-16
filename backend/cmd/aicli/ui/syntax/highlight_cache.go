package syntax

import (
	"hash/fnv"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// highlightCacheMemo memoizes ChromaHighlighter.Highlight results keyed by
// the code block's content fingerprint, language, filename, resolved theme,
// and budget.
//
// Streaming renders re-highlight stable code prefixes on every chunk: the
// active-band suffix projection renders the same prefix twice per chunk (full
// + prefix validation), and the handoff planner re-renders it per row, so
// without the memo chroma lexing (regexp2) and token→line conversion repeat
// on every state reduce.
//
// Correctness contract: the fingerprint is only a lookup hint; every hit is
// re-validated against the caller's code, so a hash collision can only
// degrade to a miss, never return the wrong highlighting. Stored lines are
// cloned on every hit so callers may mutate returned lines freely.
type highlightCacheMemo struct {
	mu         sync.Mutex
	byKey      map[highlightCacheKey]*highlightCacheEntry
	seq        uint64
	maxEntries int
	maxBytes   int
	bytes      int
	hits       uint64
	miss       uint64
}

type highlightCacheKey struct {
	codeHash uint64
	language string
	filename string
	theme    string
	budget   time.Duration
	limits   Limits
}

type highlightCacheEntry struct {
	code     string // authoritative content for hit validation
	lines    []render.Line
	meta     HighlightMeta
	size     int
	lastUsed uint64
}

var (
	highlightMemo = &highlightCacheMemo{
		byKey:      make(map[highlightCacheKey]*highlightCacheEntry),
		maxEntries: 256,
		maxBytes:   8 * 1024 * 1024,
	}
	// highlightMaxEntryBytes skips caching pathological blocks; their plain
	// fallback path is cheap anyway.
	highlightMaxEntryBytes = 64 * 1024
)

// highlightCached looks up a memoized Highlight result. Returns ok=false on a
// miss or when the fingerprint collides with different code (validated).
func highlightCached(code, language, filename, theme string, budget time.Duration, limits Limits) ([]render.Line, HighlightMeta, bool) {
	key := makeHighlightKey(code, language, filename, theme, budget, limits)
	highlightMemo.mu.Lock()
	entry, ok := highlightMemo.byKey[key]
	if ok {
		highlightMemo.seq++
		entry.lastUsed = highlightMemo.seq
		highlightMemo.hits++
	}
	highlightMemo.mu.Unlock()
	if !ok {
		return nil, HighlightMeta{}, false
	}
	if entry.code != code {
		return nil, HighlightMeta{}, false // fingerprint collision with different content
	}
	return cloneHighlightLines(entry.lines), entry.meta, true
}

// highlightStore records a freshly computed Highlight result.
// highlightStore records a freshly computed Highlight result. Time-dependent
// fallbacks (budget exhaustion) must not be stored: a transient slow render
// would otherwise pin the degraded plain output forever.
func highlightStore(code, language, filename, theme string, budget time.Duration, limits Limits, lines []render.Line, meta HighlightMeta) {
	size := entrySize(code, lines)
	if size > highlightMaxEntryBytes {
		return
	}
	key := makeHighlightKey(code, language, filename, theme, budget, limits)
	highlightMemo.mu.Lock()
	highlightMemo.miss++
	highlightMemo.seq++
	if prev, exists := highlightMemo.byKey[key]; exists {
		highlightMemo.bytes -= prev.size
	}
	highlightMemo.byKey[key] = &highlightCacheEntry{
		code:     code,
		lines:    cloneHighlightLines(lines),
		meta:     meta,
		size:     size,
		lastUsed: highlightMemo.seq,
	}
	highlightMemo.bytes += size
	for len(highlightMemo.byKey) > highlightMemo.maxEntries || highlightMemo.bytes > highlightMemo.maxBytes {
		if !highlightMemo.evictOldestLocked() {
			break
		}
	}
	highlightMemo.mu.Unlock()
}

// evictOldestLocked drops the least-recently-used entry, returning false when
// the cache is empty.
func (m *highlightCacheMemo) evictOldestLocked() bool {
	if len(m.byKey) == 0 {
		return false
	}
	var oldestKey highlightCacheKey
	oldestUsed := ^uint64(0)
	for key, entry := range m.byKey {
		if entry.lastUsed < oldestUsed {
			oldestUsed = entry.lastUsed
			oldestKey = key
		}
	}
	m.bytes -= m.byKey[oldestKey].size
	delete(m.byKey, oldestKey)
	return true
}

func makeHighlightKey(code, language, filename, theme string, budget time.Duration, limits Limits) highlightCacheKey {
	h := fnv.New64a()
	h.Write([]byte(code))
	return highlightCacheKey{
		codeHash: h.Sum64(),
		language: language,
		filename: filename,
		theme:    theme,
		budget:   budget,
		limits:   limits,
	}
}

// entrySize approximates the retained bytes of one entry: the code text plus
// the rendered lines' text and per-line structural overhead.
func entrySize(code string, lines []render.Line) int {
	size := len(code)
	for _, line := range lines {
		size += 24 // Line slice header + Style fields approximation
		for _, span := range line.Spans {
			size += len(span.Text) + len(span.Link) + 48
		}
	}
	return size
}

// cloneHighlightLines deep-copies rendered lines so callers can mutate them
// without corrupting the cache. Spans are value types, so one slice copy per
// line suffices.
func cloneHighlightLines(lines []render.Line) []render.Line {
	out := make([]render.Line, len(lines))
	for i, line := range lines {
		if len(line.Spans) > 0 {
			line.Spans = append([]render.Span(nil), line.Spans...)
		}
		out[i] = line
	}
	return out
}
