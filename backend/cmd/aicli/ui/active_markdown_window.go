package ui

import (
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

// 流式 Markdown 的窗口化尾部投影（P3-S4 结构化增量）。
//
// 问题：start==0（未发生交接）时 active band 每帧对整段源做 markdown 渲染；
// 长消息下这是 O(源) 的 parse + 行内处理 + chroma，帧时延随源增长（实测
// 240KB 源 p50 19.9ms / p95 57ms）。
//
// 方案：维护一个源后缀窗口（cut 起点），每帧只解析/渲染窗口（≈视口预算行），
// 并尝试把窗口起点前移，使窗口与源长度脱钩。窗口的正确性由两级校验保证：
//
//  1. 锚定（establish）：全量渲染一次作为基线，在基线尾部搜索候选边界
//     （“空行之后的行首”），候选必须满足「单独解析 source[candidate:] 的渲染
//     行序列 == 基线行序列的后缀」且覆盖视口预算——这一步同时证明了该偏移
//     是全量解析中的真实块边界。
//  2. 维护（maintain）：源只增（prefix 校验）时，重渲染 source[cut:] 并与窗口
//     做同样的后缀校验来前移 cut；追加只会落在窗口末端，而窗口之前的块边界
//     与渲染不再变化（引用式定义也只可能出现在窗口内部）。
//
// 任何校验失败都回退到全量路径（cut=0 或返回 false），正确性不依赖启发式。
const (
	activeMarkdownWindowAttempts = 4
	activeMarkdownWindowEntries  = 8
)

type activeMarkdownWindowEntry struct {
	source string
	cut    int
	width  int
	seq    uint64
}

type activeMarkdownWindowCache struct {
	mu      sync.Mutex
	entries map[scene.CellID]*activeMarkdownWindowEntry
	seq     uint64
}

var activeMarkdownWindows = &activeMarkdownWindowCache{entries: map[scene.CellID]*activeMarkdownWindowEntry{}}

func (c *activeMarkdownWindowCache) get(cellID scene.CellID) (string, int, int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[cellID]
	if entry == nil {
		return "", 0, 0, false
	}
	c.seq++
	entry.seq = c.seq
	return entry.source, entry.cut, entry.width, true
}

func (c *activeMarkdownWindowCache) put(cellID scene.CellID, source string, cut, width int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	c.entries[cellID] = &activeMarkdownWindowEntry{source: source, cut: cut, width: width, seq: c.seq}
	for len(c.entries) > activeMarkdownWindowEntries {
		var oldest scene.CellID
		oldestSeq := ^uint64(0)
		for id, entry := range c.entries {
			if entry.seq < oldestSeq {
				oldestSeq = entry.seq
				oldest = id
			}
		}
		delete(c.entries, oldest)
	}
}

// activeMarkdownWindowedTailLines 是 start==0 流式 markdown 的窗口化尾部路径。
// 返回的行序列是「全量渲染行序列的后缀」，调用方的 maxRows 裁剪语义不变；
// ok=false 仅出现在空渲染等退化情形（调用方按既有尾部路径处理）。
func activeMarkdownWindowedTailLines(cellID scene.CellID, source string, width int, theme style.ThemeContext, highlighter syntax.Highlighter, maxRows int) ([]render.Line, bool) {
	if cellID == 0 || maxRows < 1 {
		return nil, false
	}
	opts := activeBandMarkdownOptions(width, theme, highlighter)
	budget := maxRows + activeBandTailMarginLines

	if cachedSource, cut, cachedWidth, ok := activeMarkdownWindows.get(cellID); ok && cut > 0 && cachedWidth == width &&
		cut <= len(source) && strings.HasPrefix(source, cachedSource) {
		if lines, nextCut, advanced := activeMarkdownWindowMaintain(source, cut, opts, maxRows, budget); advanced {
			activeMarkdownWindows.put(cellID, source, nextCut, width)
			return cloneRenderLines(lines), true
		}
	}
	return activeMarkdownWindowEstablish(cellID, source, width, opts, maxRows, budget)
}

// activeMarkdownWindowMaintain 重渲染当前窗口并尝试前移边界。返回的 nextCut
// 可能等于 cut（未能前移，窗口仍是已锚定后缀）。
func activeMarkdownWindowMaintain(source string, cut int, opts markdown.Options, maxRows, budget int) ([]render.Line, int, bool) {
	windowSource := source[cut:]
	windowLines := activeMarkdownBandLines(markdown.Render(windowSource, opts))
	if len(windowLines) < maxRows {
		// 窗口覆盖不足（如宽度变化导致换行数下降）：回退重建。
		return nil, 0, false
	}
	nextCut, lines := validateActiveMarkdownWindow(windowSource, windowLines, opts, maxRows, budget)
	if nextCut <= 0 {
		return windowLines, cut, true
	}
	return lines, cut + nextCut, true
}

// activeMarkdownWindowEstablish 全量渲染一次作为锚定基线，并在尾部搜索经校验
// 的窗口边界；找不到时返回基线尾部（cut=0 缓存，后续帧继续走全量路径）。
func activeMarkdownWindowEstablish(cellID scene.CellID, source string, width int, opts markdown.Options, maxRows, budget int) ([]render.Line, bool) {
	full := activeMarkdownBandLines(markdown.Render(source, opts))
	if len(full) == 0 {
		return nil, false
	}
	cut, lines := validateActiveMarkdownWindow(source, full, opts, maxRows, budget)
	activeMarkdownWindows.put(cellID, source, cut, width)
	return cloneRenderLines(lines), true
}

// validateActiveMarkdownWindow 在 baseline（已锚定行序列）内从尾部搜索候选
// 边界：候选后缀必须重新渲染后与 baseline 的后缀逐行一致，且覆盖视口预算。
// 返回 (cut, lines)；cut<=0 表示没有可用窗口，lines 为 baseline 的尾部预算行。
func validateActiveMarkdownWindow(source string, baseline []render.Line, opts markdown.Options, maxRows, budget int) (int, []render.Line) {
	limit := len(source)
	for attempt := 0; attempt < activeMarkdownWindowAttempts; attempt++ {
		candidate := markdownWindowBoundaryBefore(source, limit, budget)
		if candidate <= 0 {
			break
		}
		limit = candidate
		suffix := activeMarkdownBandLines(markdown.Render(source[candidate:], opts))
		if len(suffix) < maxRows || len(suffix) > len(baseline) {
			continue
		}
		if !render.LinesEqual(suffix, baseline[len(baseline)-len(suffix):]) {
			continue
		}
		return candidate, suffix
	}
	tail := baseline
	if len(tail) > budget {
		tail = tail[len(tail)-budget:]
	}
	return 0, tail
}

// markdownWindowBoundaryBefore 返回 offset < limit 的最后一个「空行之后的行首」
// （行本身非空），且该行之后（含该行，直到源末尾）至少 minLines 个源行。
// 找不到返回 -1。
func markdownWindowBoundaryBefore(source string, limit, minLines int) int {
	if limit > len(source) {
		limit = len(source)
	}
	if minLines < 1 {
		minLines = 1
	}
	lines := 0
	lineEnd := limit
	for lineEnd > 0 {
		lineStart := strings.LastIndexByte(source[:lineEnd-1], '\n') + 1
		lines++
		if lineStart == 0 {
			break
		}
		prevEnd := lineStart - 1 // 上一行结尾的 '\n'
		prevStart := strings.LastIndexByte(source[:prevEnd], '\n') + 1
		if isBlankMarkdownSourceLine(source[prevStart:prevEnd]) && lines >= minLines {
			if !isBlankMarkdownSourceLine(source[lineStart:lineEnd]) {
				return lineStart
			}
		}
		lineEnd = lineStart
	}
	return -1
}

func isBlankMarkdownSourceLine(line string) bool {
	for index := 0; index < len(line); index++ {
		switch line[index] {
		case ' ', '\t', '\r':
		default:
			return false
		}
	}
	return true
}
