package ui

import (
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// P1-5（docs/plan/aicli-stream-pipeline-performance-audit-and-optimization-plan-20261011.md §3）：
// LooksLikeMarkdown 是逐 delta / 逐帧的全源线性启发式扫描（无分配但 O(源)，
// 活跃 cell 的 Source 随流增长）。按 cell 缓存判定结果，只在源**增长**超过
// 阈值时重估——语义上只是把 md/plain 格式切换的感知延迟限制在半个阈值内。
// 命中条件包含前后缀指纹（切片 memcmp，零拷贝）：生产路径 Source 只追加，
// 指纹等价于内容未变；同时挡住同长度换内容的场景（测试复用 CellID 等）。
// 收缩（快照替换类）立即重估。cellID==0（无身份回退路径）直接透传不缓存。
const (
	looksLikeMarkdownRecheckBytes = 512
	looksLikeMarkdownFingerprint  = 64
)

type looksLikeMarkdownEntry struct {
	sourceLen int
	head      string // source 前 64 字节切片（共享底层，零拷贝）
	tail      string // source 末 64 字节切片
	result    bool
}

var looksLikeMarkdownMemo struct {
	mu      sync.Mutex
	entries map[scene.CellID]looksLikeMarkdownEntry
}

// looksLikeMarkdownThrottled 是 markdown.LooksLikeMarkdown 的节流包装。
// 条目有界：超限整体清空（判定是纯函数，清空只多扫一次）。
func looksLikeMarkdownThrottled(cellID scene.CellID, source string) bool {
	if cellID == 0 {
		return markdown.LooksLikeMarkdown(source)
	}
	looksLikeMarkdownMemo.mu.Lock()
	entry, ok := looksLikeMarkdownMemo.entries[cellID]
	if ok {
		if len(source) >= entry.sourceLen && len(source)-entry.sourceLen < looksLikeMarkdownRecheckBytes &&
			looksLikeMarkdownSameEnds(source, entry) {
			result := entry.result
			looksLikeMarkdownMemo.mu.Unlock()
			return result
		}
	}
	looksLikeMarkdownMemo.mu.Unlock()

	result := markdown.LooksLikeMarkdown(source)

	looksLikeMarkdownMemo.mu.Lock()
	if len(looksLikeMarkdownMemo.entries) > 64 {
		looksLikeMarkdownMemo.entries = make(map[scene.CellID]looksLikeMarkdownEntry)
	}
	if looksLikeMarkdownMemo.entries == nil {
		looksLikeMarkdownMemo.entries = make(map[scene.CellID]looksLikeMarkdownEntry)
	}
	looksLikeMarkdownMemo.entries[cellID] = newLooksLikeMarkdownEntry(source, result)
	looksLikeMarkdownMemo.mu.Unlock()
	return result
}

func looksLikeMarkdownSameEnds(source string, entry looksLikeMarkdownEntry) bool {
	headLen := len(entry.head)
	if headLen == 0 || headLen > len(source) || source[:headLen] != entry.head {
		return false
	}
	tailStart := len(source) - len(entry.tail)
	if tailStart < 0 || source[tailStart:] != entry.tail {
		return false
	}
	return true
}

func newLooksLikeMarkdownEntry(source string, result bool) looksLikeMarkdownEntry {
	head := source
	if len(head) > looksLikeMarkdownFingerprint {
		head = head[:looksLikeMarkdownFingerprint]
	}
	tail := source
	if len(tail) > looksLikeMarkdownFingerprint {
		tail = tail[len(tail)-looksLikeMarkdownFingerprint:]
	}
	return looksLikeMarkdownEntry{sourceLen: len(source), head: head, tail: tail, result: result}
}
