package commands

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

// This file owns the renderer-neutral documents for the structured /memory
// path. It never touches the terminal: owned interactive dispatch renders the
// documents through the interaction coordinator, while plain/JSON projections
// use their own renderer. Store open/append/list/search errors stay on the
// legacy path so the error message remains visible in every mode.

type chatMemoryRuntimeStatus struct {
	Ready      bool
	Trigger    string
	Generation bool
	TurnDigest bool
	Digest     memoryruntime.DigestStats
}

func chatMemoryRuntimeStatusForSession(session *ChatSession) chatMemoryRuntimeStatus {
	host := localChatRuntimeHostOf(session)
	if host == nil || host.MemoryRuntime == nil || host.RuntimeConfig == nil {
		return chatMemoryRuntimeStatus{}
	}
	generation := host.RuntimeConfig.Memory.Generation
	return chatMemoryRuntimeStatus{
		Ready:      true,
		Trigger:    generation.Trigger,
		Generation: generation.Enabled,
		TurnDigest: generation.TurnDigest.Enabled,
		Digest:     host.MemoryRuntime.DigestStats(),
	}
}

// buildChatMemoryStatusDocument renders the /memory status view. It returns
// ready=false for store read errors (legacy error path); "项目记忆: 未配置"
// only applies to a nil store, which the structured branch never reaches.
func buildChatMemoryStatusDocument(store *memorystore.Store, runtimeStatus chatMemoryRuntimeStatus) (render.Document, bool) {
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
		if runtimeStatus.Ready {
			lines = append(lines, fmt.Sprintf(
				"runtime=ready trigger=%s generation=%t digest=%t",
				runtimeStatus.Trigger, runtimeStatus.Generation, runtimeStatus.TurnDigest,
			))
			stats := runtimeStatus.Digest
			line := fmt.Sprintf(
				"digest_runtime: written=%d skipped=%d failed=%d",
				stats.Written, stats.Skipped, stats.Failed,
			)
			if stats.LastSkipReason != "" {
				line += " last_skip=" + stats.LastSkipReason
			}
			if stats.LastError != "" {
				line += " last_error=present"
			}
			if !stats.LastWrittenAt.IsZero() {
				line += " last_write=" + stats.LastWrittenAt.Format(time.RFC3339)
			}
			lines = append(lines, line)
			if stats.LastMaintenanceStage != "" {
				outcome := "failed"
				if stats.LastMaintenanceResult == "" || stats.LastMaintenanceResult == "ok" {
					outcome = "ok"
				}
				lines = append(lines, fmt.Sprintf(
					"last_maintenance: at=%s stage=%s result=%s",
					stats.LastMaintenanceAt.Format(time.RFC3339), stats.LastMaintenanceStage, outcome,
				))
			}
		} else {
			lines = append(lines, "runtime=not_initialized (自动抽取未挂载)")
		}
	}
	if report, statusErr := store.Status(context.Background()); statusErr == nil {
		lines = append(lines, memoryStoreStatusLines(report)...)
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

func memoryStoreStatusLines(report memorystore.StatusReport) []string {
	lines := []string{
		fmt.Sprintf("scope=%s schema=%d", report.Scope, report.SchemaVersion),
		fmt.Sprintf("memory: total=%d manual=%d active=%d candidate=%d superseded=%d stale=%d",
			report.Total, report.ManualCount, report.ByStatus["active"], report.ByStatus["candidate"],
			report.ByStatus["superseded"], report.ByFreshness["stale"]),
		fmt.Sprintf("search_backend=%s fallback=%t", valueOr(report.SearchBackend, "unknown"), report.Fallback),
		fmt.Sprintf("turn_digests: pending=%d done=%d deferred=%d failed=%d pending_sessions=%d backlog_batches=%d oldest_age_seconds=%d",
			report.TurnDigests["pending"], report.TurnDigests["done"], report.TurnDigests["deferred"],
			report.TurnDigests["failed"], report.PendingDigestSessions,
			report.PendingDigestBacklogBatches, report.OldestPendingDigestAgeSeconds),
		fmt.Sprintf("turn_windows: pending=%d done=%d failed=%d invalidated=%d",
			report.TurnWindows["pending"], report.TurnWindows["done"],
			report.TurnWindows["failed"], report.TurnWindows["invalidated"]),
		fmt.Sprintf("extraction_backlog: pending_jobs=%d pending_sessions=%d oldest_age_seconds=%d dead_letter=%d last_extraction_at=%s",
			report.PendingExtractionJobs, report.PendingExtractionSessions,
			report.OldestPendingJobAgeSeconds, report.DeadLetterJobs,
			valueOr(report.LastExtractionAt, "never")),
	}
	if report.FallbackReason != "" {
		lines = append(lines, "fallback_reason=present")
	}
	kinds := make([]string, 0, len(report.JobsByKind))
	for kind := range report.JobsByKind {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		counts := report.JobsByKind[kind]
		lines = append(lines, fmt.Sprintf(
			"jobs_by_kind: kind=%s pending=%d running=%d succeeded=%d failed=%d dead_letter=%d",
			kind, counts["pending"], counts["running"], counts["succeeded"], counts["failed"], counts["dead_letter"],
		))
	}
	for _, job := range report.ExtractionJobs {
		line := fmt.Sprintf("extraction_job: kind=%s id=%s status=%s attempt=%d priority=%d",
			job.Kind, job.ID, job.Status, job.Attempt, job.Priority)
		if job.JobKey != "" {
			line += " job_key=" + job.JobKey
		}
		if job.SessionID != "" {
			line += " session_id=" + job.SessionID
		}
		if job.AvailableAt != "" {
			line += " available_at=" + job.AvailableAt
		}
		if job.LeaseUntil != "" {
			line += " lease_until=" + job.LeaseUntil
		}
		if job.TimeoutAt != "" {
			line += " deadline_at=" + job.TimeoutAt
		}
		if job.DeadLetterAt != "" {
			line += " dead_letter_at=" + job.DeadLetterAt
		}
		if job.DeadLetterReason != "" {
			line += " dead_letter_reason=present"
		}
		if job.LastError != "" {
			line += " last_error=present"
		}
		lines = append(lines, line)
	}
	return lines
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
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
// sourceKinds is optional so existing render callers keep their output while
// filtered CLI requests can make the selected provenance explicit.
func buildChatMemoryListDocument(notes []memorystore.Note, limit int, root string, sourceKinds ...string) render.Document {
	if len(notes) == 0 {
		return render.SingleLineDoc(render.TextSpan("项目记忆为空。使用 /memory add <text> 写入。"))
	}
	title := fmt.Sprintf("最近 %d 条项目记忆（root=%s）:", len(notes), root)
	if len(sourceKinds) > 0 && strings.TrimSpace(sourceKinds[0]) != "" {
		title = fmt.Sprintf("最近 %d 条项目记忆（source_kind=%s root=%s）:",
			len(notes), safeMemorySourceKindLabel(sourceKinds[0]), root)
	}
	lines := []string{title}
	for i, note := range notes {
		tags := ""
		if len(note.Tags) > 0 {
			tags = " [" + strings.Join(note.Tags, ", ") + "]"
		}
		if sourceKind := safeMemorySourceKindLabel(note.Source); sourceKind != "" && !strings.EqualFold(sourceKind, "manual") {
			tags = " [source=" + sourceKind + "]" + tags
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
func buildChatMemorySearchDocument(hits []memorystore.SearchHit, query string, sourceKinds ...string) render.Document {
	if len(hits) == 0 {
		return render.SingleLineDoc(render.TextSpan(fmt.Sprintf("未找到与 %q 匹配的项目记忆。", query)))
	}
	title := fmt.Sprintf("项目记忆搜索 %q → %d 条:", query, len(hits))
	if len(sourceKinds) > 0 && strings.TrimSpace(sourceKinds[0]) != "" {
		title = fmt.Sprintf("项目记忆搜索 %q（source_kind=%s）→ %d 条:",
			query, safeMemorySourceKindLabel(sourceKinds[0]), len(hits))
	}
	lines := []string{title}
	for i, hit := range hits {
		sourceKind := safeMemorySourceKindLabel(hit.Note.Source)
		sourceLabel := ""
		if sourceKind != "" && !strings.EqualFold(sourceKind, "manual") {
			sourceLabel = " source=" + sourceKind
		}
		lines = append(lines,
			fmt.Sprintf("%d. score=%.2f id=%s%s", i+1, hit.Score, hit.Note.ID, sourceLabel),
			"   "+summarizeMemoryNote(hit.Note.Text, 200),
		)
	}
	return textLinesDocument(lines)
}

func safeMemorySourceKindLabel(value string) string {
	value, _ = memoryredact.Redact(strings.TrimSpace(value))
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if runes := []rune(value); len(runes) > 64 {
		value = string(runes[:63]) + "…"
	}
	return value
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

func buildChatMemoryHistoryDocument(id string, versions []memorystore.RecordVersion, truncated ...bool) render.Document {
	id = safeMemoryHistoryField(id, 120)
	if len(versions) == 0 {
		return textLinesDocument([]string{fmt.Sprintf("memory history id=%s: （无版本）", id)})
	}
	lines := []string{fmt.Sprintf("memory history id=%s versions=%d", id, len(versions))}
	if len(truncated) > 0 && truncated[0] {
		lines[0] += " (showing latest revisions; more history available)"
	}
	for _, version := range versions {
		lines = append(lines, fmt.Sprintf(
			"v=%d type=%s status=%s revision=%d by=%s reason=%s valid=%s",
			version.Version,
			safeMemoryHistoryField(version.ChangeType, 40),
			safeMemoryHistoryField(string(version.Record.Status), 40),
			version.Record.Revision,
			safeMemoryHistoryField(version.ChangedBy, 80),
			safeMemoryHistoryField(version.ChangeReason, 160),
			memoryHistoryIntervalLabel(version.Record.ValidFrom, version.Record.ValidUntil),
		))
		if title := safeMemoryHistoryField(version.Record.Title, 120); title != "" {
			lines = append(lines, "  title: "+title)
		}
		text := safeMemoryHistoryField(version.Record.Text, 320)
		if text != "" {
			lines = append(lines, "  text: "+text)
		}
	}
	return textLinesDocument(lines)
}

func buildChatMemoryRollbackDocument(record memorystore.Record) render.Document {
	lines := []string{
		fmt.Sprintf("提示: 已回滚 id=%s revision=%d status=%s",
			safeMemoryHistoryField(record.ID, 120), record.Revision,
			safeMemoryHistoryField(string(record.Status), 40)),
		"valid=" + memoryHistoryIntervalLabel(record.ValidFrom, record.ValidUntil),
	}
	if title := safeMemoryHistoryField(record.Title, 120); title != "" {
		lines = append(lines, "title: "+title)
	}
	if text := safeMemoryHistoryField(record.Text, 400); text != "" {
		lines = append(lines, text)
	}
	return textLinesDocument(lines)
}

func safeMemoryHistoryField(value string, maxRunes int) string {
	value, _ = memoryredact.Redact(strings.TrimSpace(value))
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if maxRunes > 0 {
		runes := []rune(value)
		if len(runes) > maxRunes {
			value = string(runes[:maxRunes-1]) + "…"
		}
	}
	return value
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
	preview, _ := memoryredact.Redact(strings.TrimSpace(query))
	preview = safeMemoryHistoryField(preview, 160)
	lines := []string{fmt.Sprintf("why-not %q: selected=%d recall=%d tokens=%d",
		preview, len(selection.IDs), len(selection.Recall), selection.TokenEstimate)}
	appendMemoryCategoryBudgetLines(&lines, selection.CategoryBudget, selection.CategoryUsed)
	if len(selection.Dropped) == 0 {
		lines = append(lines, "dropped: （无）")
	} else {
		for _, dropped := range selection.Dropped {
			line := fmt.Sprintf("  dropped id=%s reason=%s",
				safeMemoryHistoryField(dropped.ID, 120),
				safeMemoryHistoryField(dropped.Reason, 64))
			if strings.TrimSpace(dropped.Category) != "" {
				line += " category=" + safeMemoryHistoryField(dropped.Category, 32)
			}
			lines = append(lines, line)
		}
	}
	return textLinesDocument(lines)
}

func appendMemoryCategoryBudgetLines(lines *[]string, budgets, used map[string]int) {
	if lines == nil || len(budgets) == 0 {
		return
	}
	keys := make(map[string]struct{}, len(budgets)+len(used))
	for key := range budgets {
		keys[key] = struct{}{}
	}
	for key := range used {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	parts := make([]string, 0, len(ordered))
	for _, key := range ordered {
		parts = append(parts, fmt.Sprintf("%s=%d/%d",
			safeMemoryHistoryField(key, 32), used[key], budgets[key]))
	}
	if len(parts) > 0 {
		*lines = append(*lines, "category_budget_used: "+strings.Join(parts, " "))
	}
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
		line := fmt.Sprintf("%s kind=%s status=%s attempt=%d/%d",
			job.ID, job.Kind, job.Status, job.Attempt, job.MaxAttempts)
		if strings.TrimSpace(job.LastError) != "" {
			line += " err=present"
		}
		lines = append(lines, line)
	}
	return textLinesDocument(lines)
}

func buildChatMemoryJobsSummaryDocument(jobs []memorystore.MemoryJob, summary *memorystore.JobOperatorSummary) render.Document {
	lines := []string{}
	if summary == nil {
		lines = append(lines, "memory job summary: unavailable")
	} else {
		lines = append(lines, fmt.Sprintf(
			"memory job summary: total=%d pending_due=%d pending_deferred=%d leased=%d retryable=%d blocked=%d dead_letter=%d oldest_pending_age_seconds=%d",
			summary.TotalJobs, summary.PendingDueJobs, summary.PendingDeferredJobs,
			summary.LeasedJobs, summary.RetryableJobs, summary.BlockedJobs,
			summary.DeadLetterJobs, summary.OldestPendingAgeSeconds,
		))
		appendJobCountGroupLines(&lines, "kind", summary.ByKind)
		appendJobCountGroupLines(&lines, "status", summary.ByStatus)
		appendJobCountGroupLines(&lines, "priority", summary.ByPriority)
	}
	if len(jobs) == 0 {
		lines = append(lines, "memory jobs: （无）")
		return textLinesDocument(lines)
	}
	lines = append(lines, "recent memory jobs:")
	for _, job := range jobs {
		line := fmt.Sprintf("%s kind=%s status=%s attempt=%d/%d",
			job.ID, job.Kind, job.Status, job.Attempt, job.MaxAttempts)
		if strings.TrimSpace(job.LastError) != "" {
			line += " err=present"
		}
		lines = append(lines, line)
	}
	return textLinesDocument(lines)
}

func appendJobCountGroupLines(lines *[]string, dimension string, groups memorystore.JobCountGroups) {
	keys := make([]string, 0, len(groups.Counts))
	for key := range groups.Counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 && groups.OmittedGroups == 0 {
		return
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, groups.Counts[key]))
	}
	line := "jobs_by_" + dimension + ":"
	if len(parts) > 0 {
		line += " " + strings.Join(parts, " ")
	}
	if groups.OmittedGroups > 0 || groups.OmittedJobs > 0 {
		line += fmt.Sprintf(" omitted_groups=%d omitted_jobs=%d", groups.OmittedGroups, groups.OmittedJobs)
	}
	*lines = append(*lines, line)
}

func buildChatMemoryDigestReplayDocument(report memoryruntime.DigestReplayReport) render.Document {
	lines := []string{
		fmt.Sprintf("digest recovery preview (dry-run): count=%d limit=%d", report.Count, report.Limit),
	}
	classes := make([]string, 0, len(report.ByClass))
	for class := range report.ByClass {
		classes = append(classes, string(class))
	}
	sort.Strings(classes)
	for _, class := range classes {
		lines = append(lines, fmt.Sprintf("  %s=%d", class, report.ByClass[memorystore.DigestReplayClassification(class)]))
	}
	if len(report.Items) == 0 {
		lines = append(lines, "items: （无）")
		return textLinesDocument(lines)
	}
	lines = append(lines, "items (oldest first):")
	for _, item := range report.Items {
		line := fmt.Sprintf("  digest=%s class=%s", item.DigestID, item.Classification)
		if item.Reason != "" {
			line += " reason=" + item.Reason
		}
		if item.JobID != "" {
			line += " job=" + item.JobID
		}
		lines = append(lines, line)
	}
	return textLinesDocument(lines)
}

func buildChatMemoryDigestReplayResultDocument(result memorystore.DigestReplayResult) render.Document {
	lines := []string{
		fmt.Sprintf("digest replay: digest=%s classification=%s", result.DigestID, result.Classification),
	}
	if result.JobID != "" {
		lines = append(lines, "job="+result.JobID)
	}
	if result.Reason != "" {
		lines = append(lines, "reason="+result.Reason)
	}
	return textLinesDocument(lines)
}

func buildChatMemoryJobDeadlineRepairDocument(result memorystore.JobDeadlineRepairResult) render.Document {
	mode := "applied"
	if result.DryRun {
		mode = "dry-run"
	}
	lines := []string{
		fmt.Sprintf("memory job deadline repair (%s): scanned=%d eligible=%d updated=%d skipped=%d",
			mode, result.Scanned, result.Eligible, result.Updated, result.Skipped),
	}
	for _, item := range result.Items {
		outcome := "planned"
		if item.Applied {
			outcome = "updated"
		} else if item.Skipped {
			outcome = "skipped"
		}
		line := fmt.Sprintf("  %s kind=%s status=%s deadline=%s outcome=%s",
			item.ID, item.Kind, item.Status, item.DeadlineAt.UTC().Format(time.RFC3339), outcome)
		if item.Reason != "" {
			line += " reason=" + item.Reason
		}
		lines = append(lines, line)
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
