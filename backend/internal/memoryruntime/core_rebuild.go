package memoryruntime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

const (
	coreRebuildJobKind       = "core_rebuild"
	coreRebuildMaxJobRecords = 200
	coreRebuildMaxUsageRows  = 500
	coreRebuildMaxSourceIDs  = 64
	coreRebuildFreshUsageAge = 90 * 24 * time.Hour
	coreRebuildActor         = "system:core_rebuild"
)

// CoreRebuildResult reports one bounded candidate-generation attempt. A
// candidate, when present, is always pending human review and never active.
type CoreRebuildResult struct {
	JobID       string
	JobStatus   memorystore.JobStatus
	Candidate   *memorystore.CoreMemory
	SourceCount int
	UsageRows   int
	Skipped     string
}

type CoreRebuilder struct {
	Store    *memorystore.Store
	Config   Config
	WorkerID string
}

func NewCoreRebuilder(store *memorystore.Store, cfg Config) *CoreRebuilder {
	return &CoreRebuilder{
		Store: store, Config: cfg.normalized(), WorkerID: strings.TrimSpace(cfg.WorkerID),
	}
}

// Run coalesces manual requests into one durable project job, claims at most
// one job, and atomically commits the reviewable candidate with job success.
// It deliberately never promotes or replaces the active Core.
func (r *CoreRebuilder) Run(ctx context.Context) (CoreRebuildResult, error) {
	var result CoreRebuildResult
	if r == nil || r.Store == nil {
		return result, fmt.Errorf("memoryruntime: core rebuilder store is nil")
	}
	mode := strings.ToLower(strings.TrimSpace(r.Config.Mode))
	if mode != "shadow" && mode != "on" {
		return result, nil
	}
	if strings.TrimSpace(r.Store.ProjectKey()) == "" {
		return result, fmt.Errorf("memoryruntime: core rebuild project key is empty")
	}

	cfg := r.Config.normalized()
	maxRecords := cfg.CoreRebuildMaxRecords
	if maxRecords > coreRebuildMaxJobRecords {
		maxRecords = coreRebuildMaxJobRecords
	}
	if maxRecords > coreRebuildMaxSourceIDs {
		maxRecords = coreRebuildMaxSourceIDs
	}
	maxUsageRows := cfg.CoreRebuildMaxUsageRows
	if maxUsageRows > coreRebuildMaxUsageRows {
		maxUsageRows = coreRebuildMaxUsageRows
	}
	tokenBudget := cfg.CoreRebuildTokenBudget
	if tokenBudget > 4000 {
		tokenBudget = 4000
	}
	maxAttempts := cfg.MaxAttempts

	queued, err := r.Store.EnqueueCoreRebuild(ctx, maxAttempts)
	if err != nil {
		return result, err
	}
	result.JobID = queued.ID
	result.JobStatus = queued.Status
	if queued.Status == memorystore.JobStatusRunning && queued.LeaseUntil.After(time.Now().UTC()) {
		result.Skipped = "already_running"
		return result, nil
	}

	worker := strings.TrimSpace(r.WorkerID)
	if worker == "" {
		worker = "memorycorerebuilder"
	}
	now := time.Now().UTC()
	claimed, err := r.Store.ClaimJobs(ctx, memorystore.ClaimJobOptions{
		WorkerID: worker, Kind: coreRebuildJobKind, JobKey: queued.JobKey,
		ProjectKey: r.Store.ProjectKey(), Limit: 1, Now: now, Lease: cfg.Lease,
	})
	if err != nil {
		return result, err
	}
	if len(claimed) == 0 {
		result.Skipped = "not_claimed"
		return result, nil
	}
	job := claimed[0]
	result.JobStatus = job.Status

	fail := func(cause error) error {
		safeCause := safeMemoryError(cause)
		cleanupCtx, cancel := memoryJobCleanupContext(ctx)
		defer cancel()
		persistErr := r.Store.FailJobForWorkerAttempt(
			cleanupCtx, job.ID, job.WorkerID, job.Attempt,
			safeMemoryErrorText(safeCause), time.Now().UTC().Add(retryBackoff(job.Attempt)),
		)
		if persistErr != nil {
			return errors.Join(safeCause, fmt.Errorf("memoryruntime: persist core rebuild failure: %w", persistErr))
		}
		return safeCause
	}

	input, err := r.collectCoreRebuildInput(ctx, now, maxRecords, maxUsageRows)
	if err != nil {
		return result, fail(err)
	}
	result.SourceCount = len(input.sourceIDs)
	result.UsageRows = input.usageRows
	if len(input.sourceIDs) == 0 || strings.TrimSpace(input.content) == "" {
		commitCtx, cancel := memoryJobCommitContext(ctx)
		defer cancel()
		if err := r.Store.CompleteJobForWorkerAttempt(commitCtx,
			job.ID, job.WorkerID, job.Attempt, ""); err != nil {
			return result, fail(err)
		}
		result.JobStatus = memorystore.JobStatusSucceeded
		result.Skipped = "no_safe_sources"
		return result, nil
	}

	commitCtx, cancel := memoryJobCommitContext(ctx)
	defer cancel()
	candidate, err := r.Store.CreateCoreCandidateForWorker(commitCtx,
		memorystore.CoreMemoryCandidateOptions{
			ScopeKind: memorystore.ScopeProject, ScopeKey: r.Store.ProjectKey(),
			Content: input.content, SourceIDs: input.sourceIDs,
			GeneratedBy: coreRebuildActor, TokenBudget: tokenBudget,
			Reason: "bounded core rebuild from active summary, pinned records, and recent usage",
		}, job, input.watermark)
	if err != nil {
		return result, fail(err)
	}
	result.Candidate = &candidate
	result.JobStatus = memorystore.JobStatusSucceeded
	return result, nil
}

type coreRebuildInput struct {
	content   string
	sourceIDs []string
	usageRows int
	watermark string
}

func (r *CoreRebuilder) collectCoreRebuildInput(
	ctx context.Context,
	now time.Time,
	maxRecords, maxUsageRows int,
) (coreRebuildInput, error) {
	var input coreRebuildInput
	summary, err := r.Store.GatedActiveSummary(ctx, now)
	if err != nil {
		return input, err
	}
	if summary != nil {
		if memoryredact.DetectSecret(summary.Text) != "" ||
			!validCoreSummarySources(summary.RecordIDs) {
			summary = nil
		}
	}
	summaryPoints := []string(nil)
	if summary != nil {
		summaryPoints = coreRebuildSummaryPoints(summary.Text, 6)
		if len(summaryPoints) == 0 {
			summary = nil
		}
	}

	recordPage, err := r.Store.ListRecords(ctx, memorystore.RecordFilter{
		Status: memorystore.StatusActive, PinnedOnly: true, Limit: maxRecords * 2,
	})
	if err != nil {
		return input, err
	}
	pinnedRecords := make([]memorystore.Record, 0, len(recordPage.Items))
	for _, record := range recordPage.Items {
		if !coreRebuildRecordEligible(record, now, true) {
			continue
		}
		pinnedRecords = append(pinnedRecords, record)
	}

	usage, err := r.Store.ListUsage(ctx, memorystore.UsageFilter{Limit: maxUsageRows})
	if err != nil {
		return input, err
	}
	usageCount := make(map[string]int, len(usage))
	usageLatest := make(map[string]time.Time, len(usage))
	for _, row := range usage {
		if row.UsedAt.IsZero() || row.UsedAt.After(now) || now.Sub(row.UsedAt) > coreRebuildFreshUsageAge {
			continue
		}
		if row.UseKind != memorystore.UsageCited && row.UseKind != memorystore.UsageRead &&
			row.UseKind != memorystore.UsageInjectedOnly {
			continue
		}
		input.usageRows++
		usageCount[row.MemoryID]++
		if row.UsedAt.After(usageLatest[row.MemoryID]) {
			usageLatest[row.MemoryID] = row.UsedAt
		}
	}
	recordOrder := func(records []memorystore.Record) {
		sort.SliceStable(records, func(i, j int) bool {
			left, right := records[i], records[j]
			if usageCount[left.ID] != usageCount[right.ID] {
				return usageCount[left.ID] > usageCount[right.ID]
			}
			if !usageLatest[left.ID].Equal(usageLatest[right.ID]) {
				return usageLatest[left.ID].After(usageLatest[right.ID])
			}
			if left.Confidence != right.Confidence {
				return left.Confidence > right.Confidence
			}
			if !left.UpdatedAt.Equal(right.UpdatedAt) {
				return left.UpdatedAt.After(right.UpdatedAt)
			}
			return left.ID < right.ID
		})
	}
	recordOrder(pinnedRecords)

	// Usage rows are ordered newest-first by the store. Rank their distinct
	// memory IDs by bounded recent frequency, then fetch only a bounded number
	// of current records. This allows high-use non-pinned memories to contribute
	// without scanning the whole project table.
	usageIDs := make([]string, 0, len(usageCount))
	for id, count := range usageCount {
		if count > 0 {
			usageIDs = append(usageIDs, id)
		}
	}
	sort.Slice(usageIDs, func(i, j int) bool {
		left, right := usageIDs[i], usageIDs[j]
		if usageCount[left] != usageCount[right] {
			return usageCount[left] > usageCount[right]
		}
		if !usageLatest[left].Equal(usageLatest[right]) {
			return usageLatest[left].After(usageLatest[right])
		}
		return left < right
	})
	usageRecords := make([]memorystore.Record, 0, minInt(len(usageIDs), maxRecords*2))
	for _, id := range usageIDs {
		if len(usageRecords) >= maxRecords*2 {
			break
		}
		record, getErr := r.Store.GetRecord(ctx, id)
		if errors.Is(getErr, sql.ErrNoRows) {
			continue
		}
		if getErr != nil {
			return input, getErr
		}
		if !coreRebuildRecordEligible(record, now, false) || record.Pinned {
			continue
		}
		usageRecords = append(usageRecords, record)
	}
	recordOrder(usageRecords)

	// Keep some room for both manually pinned stable facts and high-use
	// derived records. If one class is sparse, fill the unused slots from the
	// other class, while preserving deterministic ordering.
	records := make([]memorystore.Record, 0, maxRecords)
	selectedRecords := make(map[string]struct{}, maxRecords)
	addRecords := func(source []memorystore.Record, limit int) {
		for _, record := range source {
			if len(records) >= maxRecords || limit == 0 {
				return
			}
			if _, exists := selectedRecords[record.ID]; exists {
				continue
			}
			selectedRecords[record.ID] = struct{}{}
			records = append(records, record)
			if limit > 0 {
				limit--
			}
		}
	}
	pinnedQuota := (maxRecords + 1) / 2
	addRecords(pinnedRecords, pinnedQuota)
	addRecords(usageRecords, maxRecords-len(records))
	addRecords(pinnedRecords, maxRecords-len(records))

	sourceIDs := make([]string, 0, coreRebuildMaxSourceIDs)
	seen := make(map[string]struct{}, coreRebuildMaxSourceIDs)
	if summary != nil {
		for _, id := range summary.RecordIDs {
			sourceIDs = appendCoreSourceID(sourceIDs, seen, id)
			if len(sourceIDs) >= coreRebuildMaxSourceIDs {
				break
			}
		}
	}

	tokenBudget := r.Config.CoreRebuildTokenBudget
	if tokenBudget <= 0 {
		tokenBudget = 1200
	}
	if tokenBudget > 4000 {
		tokenBudget = 4000
	}
	var content strings.Builder
	content.WriteString("# Project Core candidate (human review required)\n")
	if summary != nil {
		summaryBudget := minInt(tokenBudget/2, 600)
		if summaryBudget < 1 {
			summaryBudget = 1
		}
		summaryBody := strings.Builder{}
		for _, point := range summaryPoints {
			candidate := summaryBody.String() + "- " + point + "\n"
			if memorystore.EstimateTokens(candidate) > summaryBudget {
				break
			}
			summaryBody.WriteString("- ")
			summaryBody.WriteString(point)
			summaryBody.WriteByte('\n')
		}
		section := "## Active project themes\n" + summaryBody.String()
		if summaryBody.Len() > 0 && memorystore.EstimateTokens(content.String()+section) <= tokenBudget {
			content.WriteString(section)
			input.watermark = "summary:" + summary.ContentHash
		} else {
			// Do not cite a summary that did not fit into the generated content.
			sourceIDs = sourceIDs[:0]
			clear(seen)
			summary = nil
		}
	}
	hasPinnedSection := false
	hasUsageSection := false
	for _, record := range records {
		if len(sourceIDs) >= coreRebuildMaxSourceIDs {
			break
		}
		line := coreRebuildRecordLine(record)
		if line == "" || memoryredact.DetectSecret(line) != "" {
			continue
		}
		candidateIDs := append([]string(nil), sourceIDs...)
		candidateSeen := cloneStringSet(seen)
		candidateIDs = appendCoreSourceID(candidateIDs, candidateSeen, record.ID)
		section := "- " + line + " (id=" + record.ID + ")\n"
		header := "## Recent derived evidence\n"
		headerWritten := &hasUsageSection
		if record.Pinned {
			header = "## Stable pinned records\n"
			headerWritten = &hasPinnedSection
		}
		addition := section
		if !*headerWritten {
			addition = header + section
		}
		if memorystore.EstimateTokens(content.String()+addition) > tokenBudget {
			continue
		}
		content.WriteString(addition)
		*headerWritten = true
		sourceIDs, seen = candidateIDs, candidateSeen
	}

	input.content = strings.TrimSpace(content.String())
	if len(sourceIDs) == 0 || strings.TrimSpace(input.content) == "# Project Core candidate (human review required)" {
		input.sourceIDs = nil
		input.content = ""
		return input, nil
	}
	input.sourceIDs = sourceIDs
	hash := sha256.New()
	_, _ = hash.Write([]byte(strings.TrimSpace(input.watermark) + "\n"))
	for _, id := range sourceIDs {
		_, _ = hash.Write([]byte(id + "\n"))
	}
	input.watermark = "core-v1:" + hex.EncodeToString(hash.Sum(nil))
	return input, nil
}

func coreRebuildSummaryPoints(text string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	points := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimLeft(line, "-*• \t")
		if index := strings.LastIndex(line, " (id="); index >= 0 {
			line = strings.TrimSpace(line[:index])
		}
		line = strings.Join(strings.Fields(line), " ")
		if line == "" || memoryredact.DetectSecret(line) != "" {
			continue
		}
		if runes := []rune(line); len(runes) > 180 {
			line = string(runes[:177]) + "..."
		}
		key := strings.ToLower(line)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		points = append(points, line)
		if len(points) >= limit {
			break
		}
	}
	if len(points) == 0 {
		fallback := strings.Join(strings.Fields(text), " ")
		if fallback != "" && memoryredact.DetectSecret(fallback) == "" {
			if runes := []rune(fallback); len(runes) > 180 {
				fallback = string(runes[:177]) + "..."
			}
			points = append(points, fallback)
		}
	}
	return points
}

func coreRebuildRecordEligible(record memorystore.Record, now time.Time, requirePinned bool) bool {
	if record.Status != memorystore.StatusActive || (requirePinned && !record.Pinned) || record.Archived ||
		!record.DeletedAt.IsZero() || record.Freshness != memorystore.FreshnessFresh ||
		strings.EqualFold(strings.TrimSpace(record.Sensitivity), "private") {
		return false
	}
	if !record.ValidUntil.IsZero() && !record.ValidUntil.After(now) {
		return false
	}
	return memoryredact.DetectSecret(record.Text+" "+record.Title+" "+record.Summary) == ""
}

func coreRebuildRecordLine(record memorystore.Record) string {
	title := strings.Join(strings.Fields(strings.TrimSpace(record.Title)), " ")
	text := strings.Join(strings.Fields(strings.TrimSpace(record.Summary)), " ")
	if text == "" {
		text = strings.Join(strings.Fields(strings.TrimSpace(record.Text)), " ")
	}
	if text == "" {
		return ""
	}
	if title != "" && !strings.EqualFold(title, text) {
		text = title + ": " + text
	}
	if runes := []rune(text); len(runes) > 240 {
		text = string(runes[:237]) + "..."
	}
	return text
}

func appendCoreSourceID(ids []string, seen map[string]struct{}, raw string) []string {
	id := strings.TrimSpace(raw)
	if id == "" {
		return ids
	}
	if _, exists := seen[id]; exists {
		return ids
	}
	seen[id] = struct{}{}
	return append(ids, id)
}

func validCoreSummarySources(ids []string) bool {
	if len(ids) == 0 || len(ids) > coreRebuildMaxSourceIDs {
		return false
	}
	seen := make(map[string]struct{}, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" || len(id) > 256 {
			return false
		}
		if _, exists := seen[id]; exists {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func cloneStringSet(source map[string]struct{}) map[string]struct{} {
	clone := make(map[string]struct{}, len(source)+1)
	for key := range source {
		clone[key] = struct{}{}
	}
	return clone
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
