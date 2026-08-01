package renderengine

import (
	"fmt"
	"hash/fnv"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
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
	mu    sync.Mutex
	docs  map[DocKey]*cachedDoc
	lru   []DocKey // 简单 LRU：尾=最近使用
	max   int
	hits  uint64
	miss  uint64
	evict uint64
}

type cachedDoc struct {
	source string // 二次校验，防止 hash 碰撞返回错误文档
	doc    render.Document
}

// NewRenderCache 创建容量为 maxDocs 的缓存；maxDocs<=0 时取 256。
func NewRenderCache(maxDocs int) *RenderCache {
	if maxDocs <= 0 {
		maxDocs = 256
	}
	return &RenderCache{
		docs: make(map[DocKey]*cachedDoc, maxDocs/2),
		max:  maxDocs,
	}
}

// Render 返回 (mode, source, opts) 对应的文档；hit 表示缓存命中。
// 命中路径只做 hash + 查找，不做任何 goldmark 解析。
func (c *RenderCache) Render(mode, source string, opts markdown.Options) (render.Document, bool) {
	key := docKey(mode, source, opts)

	c.mu.Lock()
	defer c.mu.Unlock()

	if cd, ok := c.docs[key]; ok && cd.source == source {
		c.hits++
		c.touchLocked(key)
		return cd.doc, true
	}
	c.miss++
	doc := markdown.Render(source, opts)
	c.insertLocked(key, source, doc)
	return doc, false
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

// Reset 清空缓存与指标（测试/诊断用）。
func (c *RenderCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.docs = make(map[DocKey]*cachedDoc, c.max/2)
	c.lru = c.lru[:0]
	c.hits, c.miss, c.evict = 0, 0, 0
}

func (c *RenderCache) touchLocked(key DocKey) {
	for i, k := range c.lru {
		if k == key {
			copy(c.lru[i:], c.lru[i+1:])
			c.lru[len(c.lru)-1] = key
			return
		}
	}
}

func (c *RenderCache) insertLocked(key DocKey, source string, doc render.Document) {
	if _, ok := c.docs[key]; !ok {
		if len(c.lru) >= c.max {
			oldest := c.lru[0]
			c.lru = c.lru[1:]
			delete(c.docs, oldest)
			c.evict++
		}
		c.lru = append(c.lru, key)
	}
	c.touchLocked(key)
	c.docs[key] = &cachedDoc{source: source, doc: doc}
}

func docKey(mode, source string, opts markdown.Options) DocKey {
	h := fnv.New64a()
	_, _ = h.Write([]byte(mode))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(source))
	return DocKey{
		Hash:  h.Sum64(),
		Width: opts.Width,
		Theme: themeFingerprint(opts.SyntaxTheme, opts.Theme),
		Mode:  mode,
	}
}

// themeFingerprint 覆盖影响文档结构（而非仅 ANSI 编码）的主题因子：
// Chroma 语法主题（opts.SyntaxTheme）、ThemeContext 内的语法主题/终端色深/
// 超链接策略。调色板只影响 Role -> 颜色编码，不影响 Document 本身，
// 不参与指纹。
func themeFingerprint(syntaxTheme string, t style.ThemeContext) string {
	return fmt.Sprintf("%s|%s|%v|%t", syntaxTheme, t.SyntaxName, t.Terminal.ColorProfile, t.UseHyperlink)
}

// sharedCache 是进程级共享实例；ActiveStreamController 与
// formatter.FormatDocument 默认都走它。
var sharedCache = NewRenderCache(256)

// SharedRenderCache 返回进程级共享 RenderCache 实例。
func SharedRenderCache() *RenderCache {
	return sharedCache
}
