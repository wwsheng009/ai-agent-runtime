package memoryruntime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type Consolidator struct {
	Store    *memorystore.Store
	Config   Config
	Clock    func() time.Time
	WorkerID string
}

func NewConsolidator(store *memorystore.Store, cfg Config) *Consolidator {
	return &Consolidator{Store: store, Config: cfg.normalized(), Clock: time.Now}
}

func (c *Consolidator) now() time.Time {
	if c != nil && c.Clock != nil {
		return c.Clock().UTC()
	}
	return time.Now().UTC()
}

func (c *Consolidator) Run(ctx context.Context) (ConsolidationResult, error) {
	var result ConsolidationResult
	if c == nil || c.Store == nil {
		return result, fmt.Errorf("memoryruntime: consolidator store is nil")
	}
	mode := strings.ToLower(strings.TrimSpace(c.Config.Mode))
	if mode != "shadow" && mode != "on" || c.Config.ConsolidationDisabled {
		return result, nil
	}
	phase2, err := c.runPhase2(ctx)
	if err != nil {
		return result, err
	}
	result = phase2
	rebuilt, err := c.RebuildStaleSummaries(ctx)
	if err != nil {
		return result, err
	}
	result.Rebuilt = rebuilt.Rebuilt
	result.RebuildJobs = rebuilt.RebuildJobs
	if result.SummaryID == "" && rebuilt.SummaryID != "" {
		result.SummaryID = rebuilt.SummaryID
		result.Revision = rebuilt.Revision
		result.SummaryText = rebuilt.SummaryText
		result.TokenCount = rebuilt.TokenCount
	}
	return result, nil
}

func (c *Consolidator) runPhase2(ctx context.Context) (ConsolidationResult, error) {
	var result ConsolidationResult
	if c == nil || c.Store == nil {
		return result, fmt.Errorf("memoryruntime: consolidator store is nil")
	}
	mode := strings.ToLower(strings.TrimSpace(c.Config.Mode))
	if mode != "shadow" && mode != "on" || c.Config.ConsolidationDisabled {
		return result, nil
	}
	now := c.now()
	interval := c.Config.ConsolidationInterval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	jobKey := "project:" + c.Store.ProjectKey() + ":" + now.Truncate(interval).Format("200601021504")
	job, err := c.Store.EnsureJob(ctx, memorystore.MemoryJob{
		Kind: "phase2_consolidate", JobKey: jobKey, ProjectKey: c.Store.ProjectKey(),
		MaxAttempts: c.Config.MaxAttempts,
	})
	if err != nil {
		return result, err
	}
	switch job.Status {
	case memorystore.JobStatusSucceeded, memorystore.JobStatusRunning, memorystore.JobStatusFailed:
		return result, nil
	}
	worker := strings.TrimSpace(c.WorkerID)
	if worker == "" {
		worker = "memoryconsolidator"
	}
	claimed, err := c.Store.ClaimJobs(ctx, memorystore.ClaimJobOptions{
		WorkerID: worker, Kind: "phase2_consolidate", JobKey: jobKey, Limit: 1, Now: now,
	})
	if err != nil || len(claimed) == 0 {
		return result, err
	}
	fail := func(err error) (ConsolidationResult, error) {
		_ = c.Store.FailJob(ctx, claimed[0].ID, err.Error(), now.Add(retryBackoff(claimed[0].Attempt)))
		return result, err
	}

	page, err := c.Store.ListRecords(ctx, memorystore.RecordFilter{
		Status: memorystore.StatusCandidate, Limit: c.Config.MaxConsolidationRecords,
	})
	if err != nil {
		return fail(err)
	}
	for _, record := range page.Items {
		status := memorystore.StatusActive
		if _, err := c.Store.UpdateRecord(ctx, record.ID, memorystore.RecordPatch{Status: &status}); err != nil {
			return fail(err)
		}
		result.Promoted++
	}

	active, err := c.activeFreshRecords(ctx)
	if err != nil {
		return fail(err)
	}
	if len(active) == 0 {
		_ = c.Store.CompleteJob(ctx, claimed[0].ID, "")
		return result, nil
	}
	sort.SliceStable(active, func(i, j int) bool {
		if active[i].Pinned != active[j].Pinned {
			return active[i].Pinned
		}
		if active[i].UseCount != active[j].UseCount {
			return active[i].UseCount > active[j].UseCount
		}
		if !active[i].LastUsedAt.Equal(active[j].LastUsedAt) {
			return active[i].LastUsedAt.After(active[j].LastUsedAt)
		}
		if active[i].Confidence != active[j].Confidence {
			return active[i].Confidence > active[j].Confidence
		}
		return active[i].UpdatedAt.After(active[j].UpdatedAt)
	})
	active, superseded, ambiguous := resolveConflicts(active)
	for _, id := range superseded {
		status := memorystore.StatusSuperseded
		if _, err := c.Store.UpdateRecord(ctx, id, memorystore.RecordPatch{Status: &status}); err != nil {
			return fail(err)
		}
	}
	result.Superseded = len(superseded)
	if len(ambiguous) > 0 {
		if err := c.Store.MarkConflictOpenQuestion(ctx, ambiguous, "same_priority_conflict"); err != nil {
			return fail(err)
		}
		result.OpenQuestions = len(uniqueRecordIDs(ambiguous))
	}
	if len(active) == 0 {
		_ = c.Store.CompleteJob(ctx, claimed[0].ID, "")
		return result, nil
	}
	text, ids, tokens := deterministicSummary(active, c.Config.SummaryTokenBudget)
	if strings.TrimSpace(text) == "" || len(ids) == 0 {
		_ = c.Store.CompleteJob(ctx, claimed[0].ID, "")
		return result, nil
	}
	summary, err := c.Store.CompileSummary(ctx, memorystore.Summary{
		Text: text, TokenEstimate: tokens, Model: "deterministic",
		Metadata: map[string]interface{}{"compiler": "deterministic_v1"},
	}, ids)
	if err != nil {
		return fail(err)
	}
	if err := c.Store.CompleteJob(ctx, claimed[0].ID, summary.InputWatermark); err != nil {
		return result, err
	}
	result.SummaryID = summary.ID
	result.Revision = summary.Revision
	result.SummaryText = summary.Text
	result.TokenCount = summary.TokenEstimate
	return result, nil
}

// RebuildStaleSummaries processes idempotent summary_rebuild jobs. A stale
// active summary is never injected; this worker compiles a new revision from
// the current active+fresh record set. If no records remain, the old revision
// is retired so it cannot linger as an active stale summary.
func (c *Consolidator) RebuildStaleSummaries(ctx context.Context) (ConsolidationResult, error) {
	var result ConsolidationResult
	if c == nil || c.Store == nil {
		return result, fmt.Errorf("memoryruntime: consolidator store is nil")
	}
	mode := strings.ToLower(strings.TrimSpace(c.Config.Mode))
	if mode != "shadow" && mode != "on" || c.Config.ConsolidationDisabled {
		return result, nil
	}
	now := c.now()
	worker := strings.TrimSpace(c.WorkerID)
	if worker == "" {
		worker = "memoryconsolidator"
	}
	if _, err := c.Store.EnqueueStaleSummaryRebuilds(ctx); err != nil {
		return result, err
	}
	for {
		claimed, err := c.Store.ClaimJobs(ctx, memorystore.ClaimJobOptions{
			WorkerID: worker, Kind: "summary_rebuild", Limit: 1, Now: now,
		})
		if err != nil {
			return result, err
		}
		if len(claimed) == 0 {
			return result, nil
		}
		job := claimed[0]
		summary, err := c.Store.ActiveSummary(ctx)
		if err != nil {
			_ = c.Store.FailJob(ctx, job.ID, err.Error(), now.Add(retryBackoff(job.Attempt)))
			return result, err
		}
		if summary == nil || summary.Freshness == memorystore.FreshnessFresh {
			if err := c.Store.CompleteJob(ctx, job.ID, ""); err != nil {
				return result, err
			}
			result.RebuildJobs++
			continue
		}
		records, err := c.activeFreshRecords(ctx)
		if err != nil {
			_ = c.Store.FailJob(ctx, job.ID, err.Error(), now.Add(retryBackoff(job.Attempt)))
			return result, err
		}
		if len(records) == 0 {
			if _, err := c.Store.RetireActiveSummary(ctx); err != nil {
				_ = c.Store.FailJob(ctx, job.ID, err.Error(), now.Add(retryBackoff(job.Attempt)))
				return result, err
			}
			if err := c.Store.CompleteJob(ctx, job.ID, ""); err != nil {
				return result, err
			}
			result.RebuildJobs++
			continue
		}
		text, ids, tokens := deterministicSummary(records, c.Config.SummaryTokenBudget)
		if strings.TrimSpace(text) == "" || len(ids) == 0 {
			if err := c.Store.CompleteJob(ctx, job.ID, ""); err != nil {
				return result, err
			}
			result.RebuildJobs++
			continue
		}
		rebuilt, err := c.Store.CompileSummary(ctx, memorystore.Summary{
			Text: text, TokenEstimate: tokens, Model: "deterministic",
			Metadata: map[string]interface{}{"compiler": "deterministic_v1", "reason": "summary_rebuild"},
		}, ids)
		if err != nil {
			if rebuilt.ID == "" {
				_ = c.Store.FailJob(ctx, job.ID, err.Error(), now.Add(retryBackoff(job.Attempt)))
				return result, err
			}
		}
		if err := c.Store.CompleteJob(ctx, job.ID, rebuilt.InputWatermark); err != nil {
			return result, err
		}
		result.RebuildJobs++
		result.Rebuilt++
		result.SummaryID = rebuilt.ID
		result.Revision = rebuilt.Revision
		result.SummaryText = rebuilt.Text
		result.TokenCount = rebuilt.TokenEstimate
	}
}

func (c *Consolidator) activeFreshRecords(ctx context.Context) ([]memorystore.Record, error) {
	page, err := c.Store.ListRecords(ctx, memorystore.RecordFilter{
		Status: memorystore.StatusActive, Limit: c.Config.MaxConsolidationRecords,
	})
	if err != nil {
		return nil, err
	}
	cutoff := c.now().Add(-time.Duration(c.Config.MaxConsolidationUnusedDays) * 24 * time.Hour)
	records := make([]memorystore.Record, 0, len(page.Items))
	for _, record := range page.Items {
		if record.Freshness != memorystore.FreshnessFresh || !record.DeletedAt.IsZero() {
			continue
		}
		lastTouched := record.UpdatedAt
		if !record.LastUsedAt.IsZero() {
			lastTouched = record.LastUsedAt
		}
		if !lastTouched.IsZero() && lastTouched.Before(cutoff) {
			continue
		}
		records = append(records, record)
	}
	return records, nil
}

func deterministicSummary(records []memorystore.Record, tokenBudget int) (string, []string, int) {
	if tokenBudget <= 0 {
		tokenBudget = 800
	}
	if tokenBudget > 1200 {
		tokenBudget = 1200
	}
	headings := []struct {
		kind  string
		title string
	}{
		{"preference", "User preferences"},
		{"decision", "Project decisions"},
		{"workflow", "Workflows"},
		{"pitfall", "Pitfalls and corrections"},
		{"correction", "Corrections"},
		{"open_question", "Open questions"},
		{"reference", "References"},
		{"fact", "Facts"},
	}
	var b strings.Builder
	b.WriteString("v1\n")
	ids := make([]string, 0, len(records))
	used := memorystore.EstimateTokens(b.String())
	byKind := map[string][]memorystore.Record{}
	for _, record := range records {
		byKind[record.Kind] = append(byKind[record.Kind], record)
	}
	for _, heading := range headings {
		items := byKind[heading.kind]
		if len(items) == 0 {
			continue
		}
		header := "## " + heading.title + "\n"
		headerCost := memorystore.EstimateTokens(header)
		if used+headerCost > tokenBudget {
			break
		}
		b.WriteString(header)
		used += headerCost
		for _, record := range items {
			line := "- " + summarizeRecord(record)
			cost := memorystore.EstimateTokens(line)
			if used+cost > tokenBudget {
				break
			}
			b.WriteString(line)
			b.WriteByte('\n')
			used += cost
			ids = append(ids, record.ID)
		}
	}
	return strings.TrimRight(b.String(), "\n"), ids, used
}

func summarizeRecord(record memorystore.Record) string {
	text := strings.Join(strings.Fields(record.Text), " ")
	if runes := []rune(text); len(runes) > 240 {
		text = string(runes[:236]) + " ..."
	}
	return "[" + record.Kind + "] " + text + " (id=" + record.ID + ")"
}

func kindOrder(kind string) int {
	switch kind {
	case "preference":
		return 0
	case "decision":
		return 1
	case "workflow":
		return 2
	case "pitfall":
		return 3
	case "correction":
		return 4
	case "open_question":
		return 5
	case "reference":
		return 6
	case "fact":
		return 7
	default:
		return 8
	}
}

func resolveConflicts(records []memorystore.Record) ([]memorystore.Record, []string, []string) {
	groups := map[string][]memorystore.Record{}
	for _, record := range records {
		subject := strings.ToLower(strings.Join(strings.Fields(record.Text), " "))
		if subject == "" {
			continue
		}
		groups[subject] = append(groups[subject], record)
	}
	superseded := make([]string, 0)
	ambiguous := make([]string, 0)
	drop := make(map[string]struct{})
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool {
			left, right := conflictPriority(group[i]), conflictPriority(group[j])
			if left != right {
				return left > right
			}
			return group[i].UpdatedAt.After(group[j].UpdatedAt)
		})
		top := group[0]
		topPriority := conflictPriority(top)
		for _, record := range group[1:] {
			if conflictPriority(record) == topPriority {
				ambiguous = append(ambiguous, top.ID, record.ID)
			}
			if record.Pinned || strings.EqualFold(record.SourceKind, "manual") {
				continue
			}
			if conflictPriority(record) < topPriority {
				superseded = append(superseded, record.ID)
				drop[record.ID] = struct{}{}
			}
			// Same-priority ambiguity is preserved. The docs' executable
			// fallback is open_question; M3 does not silently merge it.
		}
	}
	if len(superseded) == 0 {
		return records, nil, ambiguous
	}
	out := make([]memorystore.Record, 0, len(records)-len(superseded))
	for _, record := range records {
		if _, ok := drop[record.ID]; ok {
			continue
		}
		out = append(out, record)
	}
	return out, superseded, ambiguous
}

func conflictPriority(record memorystore.Record) int {
	if strings.EqualFold(record.Kind, "correction") {
		return 6
	}
	if strings.EqualFold(record.SourceKind, "manual") {
		if record.Pinned {
			return 5
		}
		return 4
	}
	if strings.EqualFold(record.Kind, "fact") && record.Confidence >= 0.8 {
		return 3
	}
	if record.Confidence >= 0.7 {
		return 2
	}
	return 1
}

func uniqueRecordIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
