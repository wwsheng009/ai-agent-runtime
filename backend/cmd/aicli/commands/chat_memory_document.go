package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
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
	if store.Mode() != "off" {
		lines = append(lines, "db="+store.DBPath()+" mode="+store.Mode())
		if report, statusErr := store.Status(context.Background()); statusErr == nil {
			lines = append(lines, fmt.Sprintf(
				"turn_digests: pending=%d done=%d deferred=%d failed=%d",
				report.TurnDigests["pending"], report.TurnDigests["done"],
				report.TurnDigests["deferred"], report.TurnDigests["failed"],
			))
			lines = append(lines, fmt.Sprintf(
				"turn_windows: pending=%d done=%d failed=%d invalidated=%d",
				report.TurnWindows["pending"], report.TurnWindows["done"],
				report.TurnWindows["failed"], report.TurnWindows["invalidated"],
			))
		}
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

func buildChatMemoryRecordDocument(record memorystore.Record) render.Document {
	lines := []string{
		fmt.Sprintf("%s kind=%s status=%s freshness=%s pinned=%t revision=%d use=%d",
			record.ID, record.Kind, record.Status, record.Freshness, record.Pinned, record.Revision, record.UseCount),
		fmt.Sprintf("source=%s session=%s created=%s updated=%s",
			record.SourceKind, record.SourceSessionID, record.CreatedAt.Format(time.RFC3339), record.UpdatedAt.Format(time.RFC3339)),
	}
	if !record.LastVerifiedAt.IsZero() {
		line := "last_verified=" + record.LastVerifiedAt.Format(time.RFC3339)
		if strings.TrimSpace(record.StaleReason) != "" {
			line += " stale_reason=" + record.StaleReason
		}
		lines = append(lines, line)
	}
	lines = append(lines, summarizeMemoryNote(record.Text, 400))
	return textLinesDocument(lines)
}

func buildChatMemoryWhyDocument(record memorystore.Record, usage []memorystore.UsageRecord) render.Document {
	lines := []string{
		fmt.Sprintf("%s source=%s session=%s pinned=%t revision=%d use=%d",
			record.ID, record.SourceKind, record.SourceSessionID, record.Pinned, record.Revision, record.UseCount),
		summarizeMemoryNote(record.Text, 400),
	}
	if len(usage) == 0 {
		lines = append(lines, "usage: （无）")
	} else {
		lines = append(lines, "usage:")
		for _, item := range usage {
			lines = append(lines, fmt.Sprintf("  %s session=%s turn=%s at=%s",
				item.UseKind, item.SessionID, item.TurnID, item.UsedAt.Format(time.RFC3339)))
		}
	}
	return textLinesDocument(lines)
}

func buildChatMemoryWhyNotDocument(query string, selection memorystore.Selection) render.Document {
	lines := []string{fmt.Sprintf("why-not %q: selected=%d recall=%d tokens=%d",
		query, len(selection.IDs), len(selection.Recall), selection.TokenEstimate)}
	if len(selection.Dropped) == 0 {
		lines = append(lines, "dropped: （无）")
	} else {
		for _, dropped := range selection.Dropped {
			lines = append(lines, fmt.Sprintf("  dropped id=%s reason=%s", dropped.ID, dropped.Reason))
		}
	}
	return textLinesDocument(lines)
}

func buildChatMemoryCorrectionDocument(record memorystore.Record) render.Document {
	return textLinesDocument([]string{
		fmt.Sprintf("提示: 已创建 correction id=%s supersedes=%s", record.ID, record.SupersedesID),
		summarizeMemoryNote(record.Text, 200),
	})
}

func buildChatMemoryStaleDocument(records []memorystore.Record) render.Document {
	if len(records) == 0 {
		return render.SingleLineDoc(render.TextSpan("stale/unverified: （无）"))
	}
	lines := make([]string, 0, len(records))
	for _, record := range records {
		lines = append(lines, fmt.Sprintf("%s freshness=%s reason=%s %s",
			record.ID, record.Freshness, record.StaleReason, summarizeMemoryNote(record.Text, 120)))
	}
	return textLinesDocument(lines)
}

func buildChatMemoryJobsDocument(jobs []memorystore.MemoryJob) render.Document {
	if len(jobs) == 0 {
		return render.SingleLineDoc(render.TextSpan("memory jobs: （无）"))
	}
	lines := make([]string, 0, len(jobs))
	for _, job := range jobs {
		lines = append(lines, fmt.Sprintf("%s kind=%s status=%s attempt=%d/%d err=%s",
			job.ID, job.Kind, job.Status, job.Attempt, job.MaxAttempts, job.LastError))
	}
	return textLinesDocument(lines)
}

func buildChatMemoryConsolidateDocument(result memoryruntime.ConsolidationResult) render.Document {
	model := strings.TrimSpace(result.SummaryModel)
	if model == "" {
		model = "-"
	}
	return textLinesDocument([]string{
		fmt.Sprintf(
			"consolidation: promoted=%d superseded=%d rebuilt=%d revision=%d tokens=%d",
			result.Promoted, result.Superseded, result.Rebuilt, result.Revision, result.TokenCount,
		),
		"summary_model=" + model,
	})
}
