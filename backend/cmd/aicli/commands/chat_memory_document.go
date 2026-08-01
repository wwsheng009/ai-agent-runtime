package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

// This file owns the renderer-neutral documents for the structured /memory
// path. It never touches the terminal: owned interactive dispatch renders the
// documents through the interaction coordinator, while plain/JSON projections
// use their own renderer. Store open/append/list/search errors stay on the
// legacy path so the error message remains visible in every mode.

// buildChatMemoryStatusDocument renders the /memory status view. It returns
// ready=false for store read errors (legacy error path); "项目记忆: 未配置"
// only applies to a nil store, which the structured branch never reaches.
func buildChatMemoryStatusDocument(store *memorystore.Store) (render.Document, bool) {
	if store == nil {
		return render.SingleLineDoc(render.TextSpan("项目记忆: 未配置")), true
	}
	notes, err := store.List(5)
	if err != nil {
		return render.Document{}, false
	}
	all, _ := store.List(0)
	lines := []string{
		"项目记忆 root=" + store.Root(),
		"notes=" + store.Path() + " total=" + strconv.Itoa(len(all)),
	}
	if len(notes) == 0 {
		lines = append(lines,
			"最近笔记: （空）",
			"提示: /memory add <text> 写入跨会话笔记",
		)
		return textLinesDocument(lines), true
	}
	lines = append(lines, "最近笔记:")
	for i, note := range notes {
		lines = append(lines, fmt.Sprintf("  %d. %s — %s", i+1, note.ID, summarizeMemoryNote(note.Text, 120)))
	}
	return textLinesDocument(lines), true
}

// buildChatMemoryAddDocument renders the /memory add confirmation, mirroring
// the legacy "提示: 已写入项目记忆 id=..." two-line output.
func buildChatMemoryAddDocument(note memorystore.Note) render.Document {
	return textLinesDocument([]string{
		fmt.Sprintf("提示: 已写入项目记忆 id=%s", note.ID),
		summarizeMemoryNote(note.Text, 160),
	})
}

// buildChatMemoryListDocument renders /memory list output. An empty store
// renders the legacy hint line as the successful result of the operation.
func buildChatMemoryListDocument(notes []memorystore.Note, limit int, root string) render.Document {
	if len(notes) == 0 {
		return render.SingleLineDoc(render.TextSpan("项目记忆为空。使用 /memory add <text> 写入。"))
	}
	lines := []string{fmt.Sprintf("最近 %d 条项目记忆（root=%s）:", len(notes), root)}
	for i, note := range notes {
		tags := ""
		if len(note.Tags) > 0 {
			tags = " [" + strings.Join(note.Tags, ", ") + "]"
		}
		lines = append(lines,
			fmt.Sprintf("%d. %s%s", i+1, note.ID, tags),
			"   "+summarizeMemoryNote(note.Text, 200),
		)
	}
	return textLinesDocument(lines)
}

// buildChatMemorySearchDocument renders /memory search output. A zero-hit
// search renders the legacy "未找到" line as the successful result.
func buildChatMemorySearchDocument(hits []memorystore.SearchHit, query string) render.Document {
	if len(hits) == 0 {
		return render.SingleLineDoc(render.TextSpan(fmt.Sprintf("未找到与 %q 匹配的项目记忆。", query)))
	}
	lines := []string{fmt.Sprintf("项目记忆搜索 %q → %d 条:", query, len(hits))}
	for i, hit := range hits {
		lines = append(lines,
			fmt.Sprintf("%d. score=%.2f id=%s", i+1, hit.Score, hit.Note.ID),
			"   "+summarizeMemoryNote(hit.Note.Text, 200),
		)
	}
	return textLinesDocument(lines)
}
