package diff

import (
	"container/list"
	"reflect"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

const (
	defaultRenderTextCacheMaxEntries = 64
	defaultRenderTextCacheMaxBytes   = 16 * 1024 * 1024
)

var sharedRenderTextCache = newRenderTextCache(defaultRenderTextCacheMaxEntries, defaultRenderTextCacheMaxBytes)

// renderTextCache memoizes RenderText IR documents. diff parsing and Chroma
// highlighting are expensive and deterministic for a given source + options;
// transcript frames repeatedly render the same finalized diff cell.
type renderTextCache struct {
	mu         sync.Mutex
	docs       map[renderTextCacheKey]*cachedRenderText
	lru        *list.List
	max        int
	maxBytes   int
	bytes      int
	generation uint64
}

type cachedRenderText struct {
	doc     render.Document
	bytes   int
	lruElem *list.Element
}

type renderTextCacheKey struct {
	source string

	width             int
	headerLabel       string
	showLineNo        bool
	syntaxHighlight   bool
	syntaxTheme       string
	highlightLimits   syntax.Limits
	gutterWidth       int
	paletteName       string
	paletteVariant    style.Variant
	themeSyntaxName   string
	useHyperlink      bool
	profile           render.ColorProfile
	background        style.BackgroundKind
	defaultFG         style.RGB
	defaultBG         style.RGB
	defaultFGSet      bool
	defaultBGSet      bool
	highlighterToken  uintptr
	highlighterBudget time.Duration
	highlighterLimits syntax.Limits
	highlighterTheme  string
}

func newRenderTextCache(max, maxBytes int) *renderTextCache {
	if max <= 0 {
		max = defaultRenderTextCacheMaxEntries
	}
	if maxBytes <= 0 {
		maxBytes = defaultRenderTextCacheMaxBytes
	}
	return &renderTextCache{
		docs:     make(map[renderTextCacheKey]*cachedRenderText, max),
		lru:      list.New(),
		max:      max,
		maxBytes: maxBytes,
	}
}

// get returns an independent document clone so callers may mutate rows/spans
// without corrupting the cached IR.
func (c *renderTextCache) get(key renderTextCacheKey) (render.Document, bool) {
	if c == nil {
		return render.Document{}, false
	}
	c.mu.Lock()
	entry, ok := c.docs[key]
	if !ok {
		c.mu.Unlock()
		return render.Document{}, false
	}
	if entry.lruElem != nil {
		c.lru.MoveToBack(entry.lruElem)
	}
	// Keep a shallow immutable reference and clone outside the lock: large
	// documents should not serialize all cache lookups while spans are copied.
	doc := entry.doc
	c.mu.Unlock()
	return doc.Clone(), true
}

func (c *renderTextCache) put(key renderTextCacheKey, doc render.Document) {
	if c == nil {
		return
	}
	bytes := estimateRenderTextDocumentBytes(key.source, doc)
	if bytes > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if current, ok := c.docs[key]; ok {
		c.bytes -= current.bytes
		if current.lruElem != nil {
			c.lru.Remove(current.lruElem)
		}
		delete(c.docs, key)
	}
	for len(c.docs) >= c.max || c.bytes+bytes > c.maxBytes {
		if !c.evictOldestLocked() {
			break
		}
	}
	entry := &cachedRenderText{doc: doc, bytes: bytes, lruElem: c.lru.PushBack(key)}
	c.docs[key] = entry
	c.bytes += bytes
}

func (c *renderTextCache) evictOldestLocked() bool {
	elem := c.lru.Front()
	if elem == nil {
		return false
	}
	key := elem.Value.(renderTextCacheKey)
	c.lru.Remove(elem)
	if entry, ok := c.docs[key]; ok {
		c.bytes -= entry.bytes
		delete(c.docs, key)
	}
	return true
}

func (c *renderTextCache) Reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.docs = make(map[renderTextCacheKey]*cachedRenderText, c.max)
	c.lru.Init()
	c.bytes = 0
	c.generation++
}

func (c *renderTextCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.docs)
}

// renderTextCacheKeyFor builds a complete deterministic key for every option
// that can change the rendered IR. Custom highlighters are deliberately not
// cached: their identity and mutable state are not representable in a plain
// comparable key.
func renderTextCacheKeyFor(text string, opts RenderOptions) (renderTextCacheKey, bool) {
	if opts.Highlighter != nil {
		return renderTextCacheKey{}, false
	}
	token, budget, limits, defaultTheme := defaultHighlighterIdentity()
	if token == 0 {
		return renderTextCacheKey{}, false
	}
	profile := opts.Theme.Terminal
	key := renderTextCacheKey{
		source:            text,
		width:             opts.Width,
		headerLabel:       opts.HeaderLabel,
		showLineNo:        opts.ShowLineNo,
		syntaxHighlight:   opts.SyntaxHighlight,
		syntaxTheme:       opts.SyntaxTheme,
		highlightLimits:   opts.HighlightLimits,
		gutterWidth:       opts.GutterWidth,
		paletteName:       opts.Theme.Palette.Name,
		paletteVariant:    opts.Theme.Palette.Variant,
		themeSyntaxName:   opts.Theme.SyntaxName,
		useHyperlink:      opts.Theme.UseHyperlink,
		profile:           profile.ColorProfile,
		background:        profile.Background,
		highlighterToken:  token,
		highlighterBudget: budget,
		highlighterLimits: limits,
		highlighterTheme:  defaultTheme,
	}
	if profile.DefaultFG != nil {
		key.defaultFG = *profile.DefaultFG
		key.defaultFGSet = true
	}
	if profile.DefaultBG != nil {
		key.defaultBG = *profile.DefaultBG
		key.defaultBGSet = true
	}
	return key, true
}

func defaultHighlighterIdentity() (uintptr, time.Duration, syntax.Limits, string) {
	value := reflect.ValueOf(syntax.Default)
	if !value.IsValid() {
		return 0, 0, syntax.Limits{}, ""
	}
	var token uintptr
	switch value.Kind() {
	case reflect.Ptr, reflect.UnsafePointer, reflect.Chan, reflect.Func, reflect.Map, reflect.Slice:
		token = value.Pointer()
	default:
		return 0, 0, syntax.Limits{}, ""
	}
	chroma, ok := syntax.Default.(*syntax.ChromaHighlighter)
	if !ok {
		return token, 0, syntax.Limits{}, ""
	}
	return token, chroma.Budget, chroma.Limits, chroma.DefaultTheme
}

func estimateRenderTextDocumentBytes(source string, doc render.Document) int {
	bytes := 256 + len(source)
	for _, block := range doc.Blocks {
		bytes += 48
		for _, line := range block.Lines {
			bytes += 48
			for _, span := range line.Spans {
				bytes += len(span.Text) + len(span.Link) + 64
			}
		}
	}
	return bytes
}
