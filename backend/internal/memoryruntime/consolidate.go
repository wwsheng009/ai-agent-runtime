package memoryruntime

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
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
		result.SummaryModel = rebuilt.SummaryModel
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
		WorkerID: worker, Kind: "phase2_consolidate", JobKey: jobKey,
		ProjectKey: c.Store.ProjectKey(), Limit: 1, Now: now,
	})
	if err != nil || len(claimed) == 0 {
		return result, err
	}
	fail := func(err error) (ConsolidationResult, error) {
		if persistErr := failMemoryJobForWorker(ctx, c.Store, claimed[0].ID, claimed[0].WorkerID, err.Error(), now.Add(retryBackoff(claimed[0].Attempt))); persistErr != nil {
			return result, errors.Join(err, persistErr)
		}
		return result, err
	}
	completeJob := func(watermark string) error {
		commitCtx, cancel := memoryJobCommitContext(ctx)
		defer cancel()
		return c.Store.CompleteJobForWorker(commitCtx, claimed[0].ID, claimed[0].WorkerID, watermark)
	}

	page, err := c.Store.ListRecords(ctx, memorystore.RecordFilter{
		Status: memorystore.StatusCandidate, Limit: c.Config.MaxConsolidationRecords,
	})
	if err != nil {
		return fail(err)
	}
	for _, record := range page.Items {
		if record.Freshness != memorystore.FreshnessFresh || !record.DeletedAt.IsZero() ||
			record.Sensitivity == "private" || secretBlocksMemory(c.Config, record.Text) {
			continue
		}
		if record.Confidence < c.Config.MinCandidateConfidence {
			continue
		}
		if _, err := c.Store.PromoteCandidate(ctx, record.ID); err != nil {
			return fail(err)
		}
		result.Promoted++
	}

	active, err := c.activeFreshRecords(ctx)
	if err != nil {
		return fail(err)
	}
	if len(active) == 0 {
		if err := completeJob(""); err != nil {
			return fail(err)
		}
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
	active, supersessions, ambiguous := resolveConflicts(active)
	for _, resolution := range supersessions {
		if _, err := c.Store.SupersedeRecord(ctx, resolution.loserID, resolution.winnerID, "consolidation_priority"); err != nil {
			return fail(err)
		}
	}
	result.Superseded = len(supersessions)
	if len(ambiguous) > 0 {
		if err := c.Store.MarkConflictOpenQuestion(ctx, ambiguous, "same_priority_conflict"); err != nil {
			return fail(err)
		}
		result.OpenQuestions = len(uniqueRecordIDs(ambiguous))
	}
	if len(active) == 0 {
		if err := completeJob(""); err != nil {
			return fail(err)
		}
		return result, nil
	}
	summaryRecords := summaryEligible(active)
	input := c.prepareSummary(summaryRecords)
	summary, err := c.reusableSummary(ctx, input)
	if err != nil {
		return fail(err)
	}
	if summary == nil {
		text, ids, tokens, model, metadata := c.buildPreparedSummary(ctx, input, reasonPhase2)
		if strings.TrimSpace(text) == "" || len(ids) == 0 {
			if err := completeJob(""); err != nil {
				return fail(err)
			}
			return result, nil
		}
		commitCtx, cancel := memoryJobCommitContext(ctx)
		compiled, err := c.Store.CompileSummary(commitCtx, memorystore.Summary{
			Text: text, TokenEstimate: tokens, Model: model, Metadata: metadata,
		}, ids)
		cancel()
		if err != nil {
			return fail(err)
		}
		summary = &compiled
	}
	if err := completeJob(summary.InputWatermark); err != nil {
		return fail(err)
	}
	result.SummaryID = summary.ID
	result.Revision = summary.Revision
	result.SummaryText = summary.Text
	result.SummaryModel = summary.Model
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
			WorkerID: worker, Kind: "summary_rebuild",
			ProjectKey: c.Store.ProjectKey(), Limit: 1, Now: now,
		})
		if err != nil {
			return result, err
		}
		if len(claimed) == 0 {
			return result, nil
		}
		job := claimed[0]
		fail := func(cause error) (ConsolidationResult, error) {
			safeCause := safeMemoryError(cause)
			if persistErr := failMemoryJobForWorker(ctx, c.Store, job.ID, job.WorkerID, safeMemoryErrorText(safeCause), now.Add(retryBackoff(job.Attempt))); persistErr != nil {
				return result, errors.Join(safeCause, persistErr)
			}
			return result, safeCause
		}
		complete := func(watermark string) error {
			commitCtx, cancel := memoryJobCommitContext(ctx)
			defer cancel()
			return c.Store.CompleteJobForWorker(commitCtx, job.ID, job.WorkerID, watermark)
		}
		summary, err := c.Store.ActiveSummary(ctx)
		if err != nil {
			return fail(err)
		}
		if summary == nil || summary.Freshness == memorystore.FreshnessFresh {
			if err := complete(""); err != nil {
				return fail(err)
			}
			result.RebuildJobs++
			continue
		}
		records, err := c.activeFreshRecords(ctx)
		if err != nil {
			return fail(err)
		}
		summaryRecords := summaryEligible(records)
		if len(summaryRecords) == 0 {
			commitCtx, cancel := memoryJobCommitContext(ctx)
			_, retireErr := c.Store.RetireActiveSummary(commitCtx)
			cancel()
			if retireErr != nil {
				return fail(retireErr)
			}
			if err := complete(""); err != nil {
				return fail(err)
			}
			result.RebuildJobs++
			continue
		}
		text, ids, tokens, model, metadata := c.buildSummary(ctx, summaryRecords, reasonSummaryRebuild)
		if strings.TrimSpace(text) == "" || len(ids) == 0 {
			if err := complete(""); err != nil {
				return fail(err)
			}
			result.RebuildJobs++
			continue
		}
		commitCtx, cancel := memoryJobCommitContext(ctx)
		rebuilt, err := c.Store.CompileSummary(commitCtx, memorystore.Summary{
			Text: text, TokenEstimate: tokens, Model: model, Metadata: metadata,
		}, ids)
		cancel()
		if err != nil {
			if rebuilt.ID == "" {
				return fail(err)
			}
		}
		if err := complete(rebuilt.InputWatermark); err != nil {
			return fail(err)
		}
		result.RebuildJobs++
		result.Rebuilt++
		result.SummaryID = rebuilt.ID
		result.Revision = rebuilt.Revision
		result.SummaryText = rebuilt.Text
		result.SummaryModel = rebuilt.Model
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
	now := c.now()
	records := make([]memorystore.Record, 0, len(page.Items))
	for _, record := range page.Items {
		if record.Freshness != memorystore.FreshnessFresh || !record.DeletedAt.IsZero() ||
			record.Sensitivity == "private" || secretBlocksMemory(c.Config, record.Text) {
			continue
		}
		if !record.ValidUntil.IsZero() && !record.ValidUntil.After(now) {
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

const (
	reasonPhase2         = "phase2_consolidation"
	reasonSummaryRebuild = "summary_rebuild"
)

// buildSummary prefers the configured LLM summarizer but keeps the
// deterministic compiler as a fail-open fallback. Consolidation must never
// fail just because the summary model is unavailable or returns unusable text.
func (c *Consolidator) buildSummary(ctx context.Context, records []memorystore.Record, reason string) (string, []string, int, string, map[string]interface{}) {
	if c == nil {
		return "", nil, 0, "deterministic", map[string]interface{}{"compiler": "deterministic_v1", "reason": reason}
	}
	return c.buildPreparedSummary(ctx, c.prepareSummary(records), reason)
}

func (c *Consolidator) buildPreparedSummary(ctx context.Context, input preparedProjectSummary, reason string) (string, []string, int, string, map[string]interface{}) {
	text, ids, tokens, budget := input.text, input.ids, input.tokens, input.budget
	metadata := map[string]interface{}{
		"compiler": "deterministic_v1",
		"reason":   reason,
	}
	if input.fingerprint != "" {
		metadata[summaryInputFingerprintKey] = input.fingerprint
		metadata["summary_template_version"] = projectSummaryTemplateVersion
	}
	if c.Config.Summarizer == nil || len(ids) == 0 {
		return text, ids, tokens, "deterministic", metadata
	}
	selectedRecords := input.records
	if len(selectedRecords) == 0 {
		return text, ids, tokens, "deterministic", metadata
	}
	output, err := c.Config.Summarizer.SummarizeProject(ctx, ProjectSummaryInput{
		ProjectKey: c.Store.ProjectKey(), TokenBudget: budget, Records: selectedRecords,
	})
	if err != nil {
		metadata["fallback_reason"] = safeSummaryFallbackReason(err.Error())
		return text, ids, tokens, "deterministic", metadata
	}
	generated := strings.TrimSpace(output.Text)
	if generated == "" {
		metadata["fallback_reason"] = "empty_summary"
		return text, ids, tokens, "deterministic", metadata
	}
	if rule := memoryredact.DetectSecret(generated); rule != "" {
		metadata["fallback_reason"] = "secret:" + rule
		return text, ids, tokens, "deterministic", metadata
	}
	if invalid := projectSummaryValidationError(generated); invalid != "" {
		metadata["fallback_reason"] = invalid
		return text, ids, tokens, "deterministic", metadata
	}
	if memorystore.EstimateTokens(generated) > budget {
		generated = truncateSummaryToTokenBudget(generated, budget)
	}
	if strings.TrimSpace(generated) == "" {
		metadata["fallback_reason"] = "empty_after_truncation"
		return text, ids, tokens, "deterministic", metadata
	}
	model := strings.TrimSpace(output.Model)
	if model == "" {
		model = firstNonEmpty(c.Config.ConsolidationModel, "llm")
	}
	metadata["compiler"] = "llm_v1"
	metadata["summary_model"] = model
	if output.Usage != (TokenUsage{}) {
		metadata["usage"] = map[string]interface{}{
			"prompt_tokens":     output.Usage.PromptTokens,
			"completion_tokens": output.Usage.CompletionTokens,
			"total_tokens":      output.Usage.TotalTokens,
		}
	}
	return generated, ids, memorystore.EstimateTokens(generated), model, metadata
}

func projectSummaryValidationError(text string) string {
	if !utf8.ValidString(text) {
		return "invalid_utf8"
	}
	if !strings.Contains(text, "## ") {
		return "missing_headings"
	}
	if !strings.Contains(text, "id=") {
		return "missing_source_ids"
	}
	return ""
}

func safeSummaryFallbackReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if redacted, _ := memoryredact.Redact(reason); redacted != "" {
		reason = redacted
	}
	if runes := []rune(reason); len(runes) > 240 {
		reason = string(runes[:237]) + "..."
	}
	return reason
}

func filterRecordsByIDs(records []memorystore.Record, ids []string) []memorystore.Record {
	byID := make(map[string]memorystore.Record, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	out := make([]memorystore.Record, 0, len(ids))
	for _, id := range ids {
		if record, ok := byID[id]; ok {
			out = append(out, record)
		}
	}
	return out
}

func truncateSummaryToTokenBudget(text string, maxTokens int) string {
	if maxTokens <= 0 || memorystore.EstimateTokens(text) <= maxTokens {
		return strings.TrimSpace(text)
	}
	runes := []rune(strings.TrimSpace(text))
	low, high := 0, len(runes)
	for low < high {
		mid := (low + high + 1) / 2
		candidate := strings.TrimSpace(string(runes[:mid])) + "…"
		if memorystore.EstimateTokens(candidate) <= maxTokens {
			low = mid
		} else {
			high = mid - 1
		}
	}
	if low == 0 {
		return ""
	}
	return strings.TrimSpace(string(runes[:low])) + "…"
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
	byKind := map[string][]memorystore.Record{}
	eligible := summaryEligible(records)
	sort.SliceStable(eligible, func(i, j int) bool {
		left, right := eligible[i], eligible[j]
		if leftKind, rightKind := kindOrder(left.Kind), kindOrder(right.Kind); leftKind != rightKind {
			return leftKind < rightKind
		}
		if left.Confidence != right.Confidence {
			return left.Confidence > right.Confidence
		}
		if left.UseCount != right.UseCount {
			return left.UseCount > right.UseCount
		}
		if !left.LastUsedAt.Equal(right.LastUsedAt) {
			return left.LastUsedAt.After(right.LastUsedAt)
		}
		if !left.UpdatedAt.Equal(right.UpdatedAt) {
			return left.UpdatedAt.After(right.UpdatedAt)
		}
		return left.ID < right.ID
	})
	seen := make(map[string]struct{}, len(eligible))
	for _, record := range eligible {
		key := summaryBulletKey(record)
		if key != "" {
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
		}
		byKind[record.Kind] = append(byKind[record.Kind], record)
	}
	type bucket struct {
		title    string
		items    []memorystore.Record
		selected []memorystore.Record
		header   bool
	}
	buckets := make([]*bucket, 0, len(headings))
	for _, heading := range headings {
		items := byKind[heading.kind]
		if len(items) == 0 {
			continue
		}
		buckets = append(buckets, &bucket{title: heading.title, items: items})
	}
	if len(buckets) == 0 {
		return "", nil, 0
	}
	used := memorystore.EstimateTokens("v1\n")
	// First pass: every kind with a fitting bullet gets one, so a high-priority
	// kind cannot starve the rest of the summary.
	for _, bucket := range buckets {
		if len(bucket.items) == 0 {
			continue
		}
		header := "## " + bucket.title + "\n"
		line := "- " + summarizeRecord(bucket.items[0]) + "\n"
		cost := memorystore.EstimateTokens(header) + memorystore.EstimateTokens(line)
		if used+cost > tokenBudget {
			continue
		}
		bucket.header = true
		bucket.selected = append(bucket.selected, bucket.items[0])
		used += cost
	}
	// Second pass: spend the remaining budget in priority order.
	for _, bucket := range buckets {
		if !bucket.header {
			continue
		}
		for _, record := range bucket.items[1:] {
			line := "- " + summarizeRecord(record) + "\n"
			cost := memorystore.EstimateTokens(line)
			if used+cost > tokenBudget {
				break
			}
			bucket.selected = append(bucket.selected, record)
			used += cost
		}
	}
	render := func() (string, []string) {
		var b strings.Builder
		b.WriteString("v1\n")
		ids := make([]string, 0, len(records))
		for _, bucket := range buckets {
			if len(bucket.selected) == 0 {
				continue
			}
			b.WriteString("## ")
			b.WriteString(bucket.title)
			b.WriteByte('\n')
			for _, record := range bucket.selected {
				b.WriteString("- ")
				b.WriteString(summarizeRecord(record))
				b.WriteByte('\n')
				ids = append(ids, record.ID)
			}
		}
		return strings.TrimRight(b.String(), "\n"), ids
	}
	text, ids := render()
	// The token estimator is not strictly additive across small fragments;
	// trim the lowest-priority tails until the rendered document itself fits.
	for memorystore.EstimateTokens(text) > tokenBudget {
		removed := false
		for i := len(buckets) - 1; i >= 0; i-- {
			if len(buckets[i].selected) <= 1 {
				continue
			}
			buckets[i].selected = buckets[i].selected[:len(buckets[i].selected)-1]
			removed = true
			break
		}
		if !removed {
			for i := len(buckets) - 1; i >= 0; i-- {
				if len(buckets[i].selected) == 0 {
					continue
				}
				buckets[i].selected = buckets[i].selected[:len(buckets[i].selected)-1]
				removed = true
				break
			}
		}
		if !removed {
			break
		}
		text, ids = render()
	}
	return text, ids, memorystore.EstimateTokens(text)
}

func summaryBulletKey(record memorystore.Record) string {
	// Technical punctuation carries entity identity (C++/C#, v1.2/v1-2).
	// Normalize case and whitespace only; stripping symbols can hide distinct
	// records that correctly survived conflict resolution.
	text := strings.Join(strings.Fields(strings.ToLower(record.Text)), " ")
	if text == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(record.Kind)) + "\x00" + text
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

type memorySupersession struct {
	loserID  string
	winnerID string
}

func resolveConflicts(records []memorystore.Record) ([]memorystore.Record, []memorySupersession, []string) {
	groups := map[string][]memorystore.Record{}
	for _, record := range records {
		subject := conflictSubjectKey(record)
		if subject == "" {
			continue
		}
		groups[subject] = append(groups[subject], record)
	}
	superseded := make([]memorySupersession, 0)
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
				superseded = append(superseded, memorySupersession{loserID: record.ID, winnerID: top.ID})
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

// Only a terminal scalar assignment to a known setting provides enough evidence
// to ignore its value. In particular, "port 8000" can identify an endpoint, while
// "Server 1 port is 8000" assigns a setting to a separately identified entity.
// Keep the entire entity prefix, including case, digits and punctuation.
var conflictSettingAssignment = regexp.MustCompile(`^(.+) (?i:(timeout|ttl|interval|delay|port|retry count|max retries))(?: (?i:is(?: set to)?) | ?[:=] ?)([0-9]+(?:\.[0-9]+)?) ?([a-zA-Zµμ]*)\.?$`)

// conflictSubjectKey is a destructive-conflict gate, not a fuzzy retrieval key.
// By default only whitespace is normalized: numbers, technical punctuation,
// case, intent words and plurals may all carry identity or change a claim.
// Mask a scalar only when its entity, property, assignment and unit are clear;
// all other apparent similarities remain separate, regardless of priority.
func conflictSubjectKey(record memorystore.Record) string {
	text := strings.Join(strings.Fields(record.Text), " ")
	if text == "" {
		return ""
	}
	if match := conflictSettingAssignment.FindStringSubmatch(text); match != nil {
		entity, property, value, unit := match[1], strings.ToLower(match[2]), match[3], strings.ToLower(match[4])
		if conflictSettingEntity(entity) && conflictSettingValue(property, value, unit) {
			// A separate namespace prevents literal placeholder-like text from
			// colliding with an inferred setting. Units are not converted.
			return "setting\x00" + entity + "\x00" + property + "\x00" + unit
		}
	}
	// Kind is intentionally not part of the key: a manual decision and a
	// derived fact may contain the same claim.
	return "text\x00" + text
}

func conflictSettingEntity(entity string) bool {
	// "The timeout" and "its timeout" do not identify which entity owns the
	// setting. Ignore such modifiers only for this evidence check, never in
	// the key itself ("this API" and "that API" must still remain distinct).
	for _, token := range strings.Fields(entity) {
		switch strings.ToLower(token) {
		case "a", "an", "the", "this", "that", "these", "those", "it", "its",
			"my", "our", "your", "their", "default", "configured", "current":
			continue
		}
		for _, r := range token {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return true
			}
		}
	}
	return false
}

func conflictSettingValue(property, value, unit string) bool {
	switch property {
	case "port", "retry count", "max retries":
		// Decimal versions, endpoint addresses and unit-suffixed identifiers
		// are not integer setting values.
		return unit == "" && !strings.Contains(value, ".")
	case "timeout", "ttl", "interval", "delay":
		switch unit {
		case "", "ns", "us", "µs", "μs", "ms", "s", "sec", "secs", "second", "seconds",
			"min", "mins", "minute", "minutes", "h", "hr", "hrs", "hour", "hours",
			"d", "day", "days":
			return true
		}
	}
	return false
}

// summaryEligible excludes pinned/manual records: those are rendered from the
// independent stable budget, so including them in the summary would duplicate
// the same claim in the prompt.
func summaryEligible(records []memorystore.Record) []memorystore.Record {
	out := make([]memorystore.Record, 0, len(records))
	for _, record := range records {
		if record.Sensitivity == "private" || memoryredact.DetectSecret(record.Text) != "" {
			continue
		}
		if record.Pinned || strings.EqualFold(record.SourceKind, "manual") {
			continue
		}
		out = append(out, record)
	}
	return out
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
