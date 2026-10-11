package renderengine

import (
	"container/list"
	"fmt"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

const (
	defaultRenderCacheMaxDocs  = 256
	defaultRenderCacheMaxBytes = 64 * 1024 * 1024
)

// DocKey 是 RenderCache 的内容寻址键（阶段 D §4.6）：
// 源码 hash + width + theme 指纹 + mode。
//
// mode 区分两组选项来源：ActiveBand（ActiveBandBodyOptions，hide
// fallback + 限流 highlighter）与 scrollback/assistant
// （DefaultOptions/AssistantBodyOptions）。收敛到单一路径后删除。
type DocKey struct {
	Hash  uint64
	Width int
	Theme string
	Mode  string
}

// RenderCache 是共享 markdown Document 缓存：ActiveBand 与 scrollback
// replay 共用同一实例，同一段源码只做一次 goldmark 解析。
//
// 与冻结清单 §8 一致：禁止新增 per-stream 私有 markdown 缓存
// （markdownDoc* 模式），统一走这里。
type RenderCache struct {
	mu         sync.Mutex
	docs       map[DocKey]*cachedDoc
	flights    map[cacheFlightKey]chan struct{}
	lru        *list.List // 简单 LRU：Front=最旧，Back=最近使用；元素值为 DocKey
	max        int
	maxBytes   int
	bytes      int
	generation uint64
	hits       uint64
	miss       uint64
	evict      uint64
}

// cacheFlightKey keeps source in addition to DocKey so an extremely unlikely
// hash collision cannot make two different documents wait on each other.
type cacheFlightKey struct {
	key    DocKey
	source string
}

type cachedDoc struct {
	source string // 二次校验，防止 hash 碰撞返回错误文档
	doc    render.Document
	bytes  int
	// lruElem 是 P1-6 的 O(1) LRU 元素指针（c.lru 中，Value 为 DocKey）。
	lruElem *list.Element
}

// NewRenderCache 创建具有默认 64 MiB 估算字节预算的缓存；maxDocs<=0 时取
// 默认条目上限。大文档的最终内存边界不能只靠条目数控制。
func NewRenderCache(maxDocs int) *RenderCache {
	return NewRenderCacheWithBudget(maxDocs, defaultRenderCacheMaxBytes)
}

// NewRenderCacheWithBudget 创建具有显式条目与估算字节预算的缓存。任一参数
// 非正时使用生产默认值。预算是确定性的逻辑估算（源码、IR 文本、IR 结构），
// 适合容量控制与测试，而不是 Go runtime heap 的精确采样。
func NewRenderCacheWithBudget(maxDocs, maxBytes int) *RenderCache {
	if maxDocs <= 0 {
		maxDocs = defaultRenderCacheMaxDocs
	}
	if maxBytes <= 0 {
		maxBytes = defaultRenderCacheMaxBytes
	}
	return &RenderCache{
		docs:     make(map[DocKey]*cachedDoc, maxDocs/2),
		flights:  make(map[cacheFlightKey]chan struct{}),
		lru:      list.New(),
		max:      maxDocs,
		maxBytes: maxBytes,
	}
}

// Render 返回 (mode, source, opts) 对应的文档；hit 表示缓存命中。
// 命中路径只做 hash + 查找，不做任何 goldmark 解析。
func (c *RenderCache) Render(mode, source string, opts markdown.Options) (render.Document, bool) {
	key := docKey(mode, source, opts)
	flightKey := cacheFlightKey{key: key, source: source}

	for {
		c.mu.Lock()
		if cd, ok := c.docs[key]; ok && cd.source == source {
			c.hits++
			c.touchLocked(cd)
			c.mu.Unlock()
			return cd.doc, true
		}
		if done := c.flights[flightKey]; done != nil {
			c.mu.Unlock()
			<-done
			// The owner published the result, or Reset invalidated its generation.
			// Re-check under lock in either case so callers never observe a stale
			// pre-reset document.
			continue
		}

		done := make(chan struct{})
		c.flights[flightKey] = done
		generation := c.generation
		c.miss++
		c.mu.Unlock()

		// Markdown parsing and syntax highlighting are deliberately outside the
		// cache mutex. A long document must not block unrelated cache hits,
		// theme lookups, or LRU bookkeeping for other streams.
		doc, panicValue := renderDocument(source, opts)

		c.mu.Lock()
		if panicValue == nil && c.generation == generation {
			c.insertLocked(key, source, doc)
		}
		delete(c.flights, flightKey)
		close(done)
		c.mu.Unlock()
		if panicValue != nil {
			panic(panicValue)
		}
		return doc, false
	}
}

func renderDocument(source string, opts markdown.Options) (doc render.Document, panicValue interface{}) {
	defer func() {
		panicValue = recover()
	}()
	return markdown.Render(source, opts), nil
}

// HitRate 返回命中率（0..1）；没有请求时返回 0。
func (c *RenderCache) HitRate() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := c.hits + c.miss
	if total == 0 {
		return 0
	}
	return float64(c.hits) / float64(total)
}

// Stats 返回命中/未命中/驱逐计数快照。
func (c *RenderCache) Stats() (hits, misses, evictions uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.miss, c.evict
}

// Len 返回当前缓存条目数。
func (c *RenderCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.docs)
}

// Bytes returns the current deterministic byte estimate of cached documents.
func (c *RenderCache) Bytes() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

// MaxBytes returns the configured estimated byte budget.
func (c *RenderCache) MaxBytes() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxBytes
}

// Reset 清空缓存与指标（测试/诊断用）。
func (c *RenderCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.docs = make(map[DocKey]*cachedDoc, c.max/2)
	c.lru.Init()
	c.bytes = 0
	c.generation++
	c.hits, c.miss, c.evict = 0, 0, 0
}

// touchLocked O(1)（P1-6）：元素指针随 cachedDoc 携带，MoveToBack 无需扫描；
// 原 []DocKey 扫描+移位在 maxDocs=256 下是命中路径的每次 O(n) 开销。
func (c *RenderCache) touchLocked(cd *cachedDoc) {
	if cd != nil && cd.lruElem != nil {
		c.lru.MoveToBack(cd.lruElem)
	}
}

func (c *RenderCache) insertLocked(key DocKey, source string, doc render.Document) {
	bytes := estimateCachedDocumentBytes(source, doc)
	if bytes > c.maxBytes {
		// Keep existing hot entries when one document alone cannot fit. The
		// caller still receives the freshly rendered document, just not a cache
		// hit on the next request.
		return
	}
	if current, ok := c.docs[key]; ok {
		c.bytes -= current.bytes
		delete(c.docs, key)
		c.removeLocked(current)
	}
	for len(c.docs) >= c.max || c.bytes+bytes > c.maxBytes {
		if !c.evictOldestLocked() {
			break
		}
	}
	cd := &cachedDoc{source: source, doc: doc, bytes: bytes, lruElem: c.lru.PushBack(key)}
	c.docs[key] = cd
	c.bytes += bytes
}

func (c *RenderCache) evictOldestLocked() bool {
	elem := c.lru.Front()
	if elem == nil {
		return false
	}
	oldest := elem.Value.(DocKey)
	c.lru.Remove(elem)
	if doc, ok := c.docs[oldest]; ok {
		doc.lruElem = nil
		c.bytes -= doc.bytes
		delete(c.docs, oldest)
		c.evict++
	}
	return true
}

func (c *RenderCache) removeLocked(cd *cachedDoc) {
	if cd != nil && cd.lruElem != nil {
		c.lru.Remove(cd.lruElem)
		cd.lruElem = nil
	}
}

func estimateCachedDocumentBytes(source string, doc render.Document) int {
	// The source and renderer usually retain distinct string storage. Weight
	// text twice, then add a conservative per-node allowance for Go slices and
	// structured styles. This gives a stable admission budget without unsafe or
	// runtime heap inspection.
	bytes := 256 + 2*len(source)
	for _, block := range doc.Blocks {
		bytes += 96
		for _, line := range block.Lines {
			bytes += 96
			for _, span := range line.Spans {
				bytes += 112 + 2*len(span.Text) + 2*len(span.Link) + 2*len(span.Style.Role)
			}
		}
	}
	return bytes
}

func docKey(mode, source string, opts markdown.Options) DocKey {
	return DocKey{
		// P1-6：FNV-1a 直接扫字符串，不做 []byte(source) 转换——原实现经
		// io.Writer 接口调用，编译器无法证明不逃逸，每次 Render 命中都把
		// 整段源复制成 []byte。字节流与旧实现逐字节一致（mode + 0x00 + source）。
		Hash:  fnv1aStrings(mode, "\x00", source),
		Width: opts.Width,
		Theme: themeFingerprint(opts.SyntaxTheme, opts.Theme),
		Mode:  mode,
	}
}

func fnv1aStrings(parts ...string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for _, part := range parts {
		for i := 0; i < len(part); i++ {
			h ^= uint64(part[i])
			h *= prime64
		}
	}
	return h
}

// themeFingerprintMemo 记忆最近一次指纹输入：主题因子进程级几乎不变，
// 原实现每次 Render 都 fmt.Sprintf 分配一个新串（P1-6）。比较只覆盖指纹
// 依赖的四个因子——ThemeContext 含 Palette，整体不可比较。
var themeFingerprintMemo struct {
	mu          sync.Mutex
	syntaxTheme string
	syntaxName  string
	colorProf   render.ColorProfile
	hyperlink   bool
	fp          string
	valid       bool
}

// themeFingerprint 覆盖影响文档结构（而非仅 ANSI 编码）的主题因子：
// Chroma 语法主题（opts.SyntaxTheme）、ThemeContext 内的语法主题/终端色深/
// 超链接策略。调色板只影响 Role -> 颜色编码，不影响 Document 本身，
// 不参与指纹。
func themeFingerprint(syntaxTheme string, t style.ThemeContext) string {
	memo := &themeFingerprintMemo
	memo.mu.Lock()
	if memo.valid && memo.syntaxTheme == syntaxTheme &&
		memo.syntaxName == t.SyntaxName &&
		memo.colorProf == t.Terminal.ColorProfile &&
		memo.hyperlink == t.UseHyperlink {
		fp := memo.fp
		memo.mu.Unlock()
		return fp
	}
	memo.mu.Unlock()

	fp := fmt.Sprintf("%s|%s|%v|%t", syntaxTheme, t.SyntaxName, t.Terminal.ColorProfile, t.UseHyperlink)

	memo.mu.Lock()
	memo.syntaxTheme = syntaxTheme
	memo.syntaxName = t.SyntaxName
	memo.colorProf = t.Terminal.ColorProfile
	memo.hyperlink = t.UseHyperlink
	memo.fp = fp
	memo.valid = true
	memo.mu.Unlock()
	return fp
}

// sharedCache 是进程级共享实例；ActiveStreamController 与
// formatter.FormatDocument 默认都走它。
var sharedCache = NewRenderCache(256)

// SharedRenderCache 返回进程级共享 RenderCache 实例。
func SharedRenderCache() *RenderCache {
	return sharedCache
}
