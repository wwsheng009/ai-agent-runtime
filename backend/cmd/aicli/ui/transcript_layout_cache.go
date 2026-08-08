package ui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// cellRowsCache 缓存 transcript cell 的物理行布局（[]AppScreenRow）。
// 键为精确内容寻址（source + cell/presentation kind + document + width +
// theme 指纹），
// 与 renderengine.RenderCache 同模式：同一 cell 未变时每次 reduce 直接复
// 用上次布局，把全量重布局从 O(N) 降为 O(Δ)；内容变化/换宽/换主题自动
// 失效。测试隔离天然成立（不同场景即使复用 cellID 也不互相污染）。
//
// 共享安全：缓存返回的 rows 为只读共享引用；消费方（渲染
// appTranscriptRenderLine 先 clone、HistoryCommit 比较 historyRenderLineEquivalent
// 只读）都不原地修改 AppScreenRow / render.Line。
type cellRowsCache struct {
	mu       sync.Mutex
	entries  map[cellLayoutKey]cachedCellRows
	order    []cellLayoutKey // FIFO 逐出（尾部为最近写入）
	max      int
	maxBytes int
	bytes    int
}

type cellLayoutKey struct {
	source           string
	cellKind         scene.CellKind
	presentationKind scene.PresentationKind
	document         string
	width            int
	themeFp          string
}

type cachedCellRows struct {
	rows  []AppScreenRow
	bytes int
}

const (
	cellRowsCacheMax      = 1024
	cellRowsCacheMaxBytes = 64 * 1024 * 1024
)

var sharedCellRows = &cellRowsCache{
	entries:  make(map[cellLayoutKey]cachedCellRows),
	max:      cellRowsCacheMax,
	maxBytes: cellRowsCacheMaxBytes,
}

// cellLayoutKeyFor 派生 cell 的布局缓存键。
func cellLayoutKeyFor(cell scene.TranscriptCell, width int, themeFp string) cellLayoutKey {
	key := cellLayoutKey{
		source:           cell.Source,
		cellKind:         cell.Kind,
		presentationKind: cell.Presentation.Kind,
		width:            width,
		themeFp:          themeFp,
	}
	if cell.Presentation.Kind == scene.PresentationDocument {
		// Source is canonical replay text, but PresentationDocument may change
		// independently. Keep the exact structured input instead of trusting a
		// hash collision as semantic equality.
		if encoded, err := json.Marshal(cell.Presentation.Document); err == nil {
			key.document = string(encoded)
		} else {
			key.document = fmt.Sprintf("%#v", cell.Presentation.Document)
		}
	}
	return key
}

func (c *cellRowsCache) get(key cellLayoutKey) []AppScreenRow {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil
	}
	return entry.rows
}

func (c *cellRowsCache) put(key cellLayoutKey, rows []AppScreenRow) {
	if len(rows) == 0 {
		return
	}
	// CellID belongs to the consuming transcript occurrence, not to the cached
	// layout. Clearing it lets identical content safely share rows across cells;
	// appendCachedCellRows restores the current owner on the copied row values.
	for index := range rows {
		rows[index].CellID = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// 同键已有条目：移除旧计数与顺序位。
	if prev, ok := c.entries[key]; ok {
		c.bytes -= prev.bytes
		for i, k := range c.order {
			if k == key {
				c.order = append(c.order[:i], c.order[i+1:]...)
				break
			}
		}
	}
	entry := cachedCellRows{rows: rows, bytes: estimateCellRowsBytes(key, rows)}
	c.entries[key] = entry
	c.order = append(c.order, key)
	c.bytes += entry.bytes
	// FIFO 逐出直到回到预算内（至少保留一个条目）。
	for len(c.order) > c.max || (c.bytes > c.maxBytes && len(c.order) > 1) {
		oldest := c.order[0]
		c.order = c.order[1:]
		if e, ok := c.entries[oldest]; ok {
			c.bytes -= e.bytes
			delete(c.entries, oldest)
		}
	}
}

// estimateCellRowsBytes 粗略估算 rows 占用（用于逐出预算）。
func estimateCellRowsBytes(key cellLayoutKey, rows []AppScreenRow) int {
	bytes := len(key.source) + len(key.document) + len(key.themeFp) + 64
	for _, row := range rows {
		bytes += len(row.Text) + 32
		for _, span := range row.RenderLine.Spans {
			bytes += len(span.Text) + 64
		}
	}
	return bytes
}

// themeFingerprint 返回主题指纹：主题切换（palette/variant/syntax/terminal
// 变化）会使结构化布局结果改变，参与缓存键。
func themeFingerprint(theme style.ThemeContext) string {
	profile := theme.Terminal.ColorProfile
	return fmt.Sprintf(
		"%q|%d|%q|%t|%d|%t|%t|%d|%s|%s|%t",
		theme.Palette.Name,
		theme.Palette.Variant,
		theme.SyntaxName,
		profile.Enabled,
		profile.Depth,
		profile.Hyperlinks,
		profile.Forced,
		theme.Terminal.Background,
		terminalRGBFingerprint(theme.Terminal.DefaultFG),
		terminalRGBFingerprint(theme.Terminal.DefaultBG),
		theme.UseHyperlink,
	)
}

func terminalRGBFingerprint(value *style.RGB) string {
	if value == nil {
		return "nil"
	}
	return strconv.Itoa(int(value.R)) + "," + strconv.Itoa(int(value.G)) + "," + strconv.Itoa(int(value.B))
}
