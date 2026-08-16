package markdown

import (
	"hash/fnv"
	"sync"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// sharedGoldmark returns the process-wide goldmark instance. goldmark.Markdown
// is safe for concurrent Parse calls, and constructing it registers extensions
// and builds the parser pipeline, so one shared instance avoids paying that
// setup cost on every Render call.
var (
	goldmarkOnce sync.Once
	goldmarkInst goldmark.Markdown
)

func sharedGoldmark() goldmark.Markdown {
	goldmarkOnce.Do(func() {
		goldmarkInst = goldmark.New(
			goldmark.WithExtensions(
				// GFM includes table, strikethrough, linkify, and task list.
				extension.GFM,
			),
			goldmark.WithParserOptions(
				parser.WithAutoHeadingID(),
			),
		)
	})
	return goldmarkInst
}

// astCacheMemo memoizes goldmark parse results keyed by the source text.
//
// Streaming renders parse the same stable prefix (source[:start]) on every
// chunk — once for the full-source render and once for prefix validation —
// and the handoff planner re-parses it per row. The parse is the dominant
// cost of the markdown pipeline, so caching the AST turns those repeated
// parses into a fingerprint lookup.
//
// Correctness contract: the fingerprint is only a lookup hint; every hit is
// re-validated against the caller's source, so a hash collision can only
// degrade to a miss. Cached ASTs are shared read-only: renderers traverse but
// never mutate the tree, and each renderer keeps its own source bytes for
// inline segment access.
type astCacheMemo struct {
	mu         sync.Mutex
	byKey      map[uint64]*astCacheEntry
	seq        uint64
	maxEntries int
	maxBytes   int
	bytes      int
	hits       uint64
	miss       uint64
}

type astCacheEntry struct {
	source   string // authoritative content for hit validation
	doc      *ast.Document
	lastUsed uint64
}

var sharedASTCache = &astCacheMemo{
	byKey:      make(map[uint64]*astCacheEntry),
	maxEntries: 128,
	maxBytes:   16 * 1024 * 1024,
}

// parseCached parses source, reusing a memoized AST when the identical source
// was parsed before.
func parseCached(source string) *ast.Document {
	hash := fnvHash(source)
	sharedASTCache.mu.Lock()
	entry, ok := sharedASTCache.byKey[hash]
	if ok {
		sharedASTCache.seq++
		entry.lastUsed = sharedASTCache.seq
		if entry.source == source {
			sharedASTCache.hits++
			sharedASTCache.mu.Unlock()
			return entry.doc
		}
		// Fingerprint collision with different content: fall through and
		// re-parse; the store below overwrites the collided entry.
	}
	sharedASTCache.mu.Unlock()

	reader := text.NewReader([]byte(source))
	doc := sharedGoldmark().Parser().Parse(reader)
	document, ok := doc.(*ast.Document)
	if !ok {
		// Parse always returns *ast.Document for a complete source; fall back
		// to a fresh empty document rather than caching a malformed tree.
		document = ast.NewDocument()
	}

	sharedASTCache.mu.Lock()
	sharedASTCache.miss++
	sharedASTCache.seq++
	if prev, exists := sharedASTCache.byKey[hash]; exists {
		sharedASTCache.bytes -= len(prev.source)
	}
	sharedASTCache.byKey[hash] = &astCacheEntry{
		source:   source,
		doc:      document,
		lastUsed: sharedASTCache.seq,
	}
	sharedASTCache.bytes += len(source)
	for len(sharedASTCache.byKey) > sharedASTCache.maxEntries || sharedASTCache.bytes > sharedASTCache.maxBytes {
		if !sharedASTCache.evictOldestLocked() {
			break
		}
	}
	sharedASTCache.mu.Unlock()
	return document
}

// evictOldestLocked drops the least-recently-used entry, returning false when
// the cache is empty.
func (c *astCacheMemo) evictOldestLocked() bool {
	if len(c.byKey) == 0 {
		return false
	}
	var oldestKey uint64
	oldestUsed := ^uint64(0)
	for key, entry := range c.byKey {
		if entry.lastUsed < oldestUsed {
			oldestUsed = entry.lastUsed
			oldestKey = key
		}
	}
	c.bytes -= len(c.byKey[oldestKey].source)
	delete(c.byKey, oldestKey)
	return true
}

func fnvHash(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

// AstCacheStats exposes cache accounting for tests and diagnostics.
func AstCacheStats() (entries int, bytes int, hits uint64, miss uint64) {
	sharedASTCache.mu.Lock()
	defer sharedASTCache.mu.Unlock()
	return len(sharedASTCache.byKey), sharedASTCache.bytes, sharedASTCache.hits, sharedASTCache.miss
}
