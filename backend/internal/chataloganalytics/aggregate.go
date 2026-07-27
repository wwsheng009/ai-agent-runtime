package chataloganalytics

import (
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func loadAnalyticsRollups(root string, maxScan int) ([]SessionRollup, int, error) {
	if rollups, enabled, err := indexedRollups(root, maxScan); enabled {
		return rollups, len(rollups), err
	}
	dirs, err := DiscoverSessionDirs(root, maxScan)
	if err != nil {
		return nil, 0, err
	}
	rollups := make([]SessionRollup, 0, len(dirs))
	for _, dir := range dirs {
		rollup, _, _, loadErr := loadSessionRollup(dir, true)
		if loadErr == nil {
			rollups = append(rollups, rollup)
		}
	}
	return rollups, len(dirs), nil
}

// ListSessions returns recent session usage rollups with paging and overall totals.
func ListSessions(root string, query Query) (ListResult, error) {
	limit := normalizeLimit(query.Limit, defaultListLimit, maxListLimit)
	offset := normalizeOffset(query.Offset)
	maxScan := normalizeMaxScan(query.MaxScan)

	rollups, scanned, err := loadAnalyticsRollups(root, maxScan)
	if err != nil {
		return ListResult{}, err
	}

	matched := make([]SessionRollup, 0, len(rollups))
	for _, rollup := range rollups {
		if !matchRollup(rollup, query) {
			continue
		}
		matched = append(matched, rollup)
	}

	sort.SliceStable(matched, func(i, j int) bool {
		ti := rollupSortTime(matched[i])
		tj := rollupSortTime(matched[j])
		if ti.Equal(tj) {
			return matched[i].SessionID > matched[j].SessionID
		}
		return ti.After(tj)
	})

	totals := accumulateTotals(matched)
	total := len(matched)
	page := pageRollups(matched, offset, limit)

	window := dataWindowFor(matched)
	coverage := coverageFor(matched)
	partialReasons := aggregatePartialReasons(matched)
	return ListResult{
		SchemaVersion:  SchemaVersion,
		GeneratedAt:    time.Now(),
		Sessions:       page,
		Count:          len(page),
		Total:          total,
		Limit:          limit,
		Offset:         offset,
		Scanned:        scanned,
		Totals:         totals,
		DataWindow:     window,
		Coverage:       coverage,
		Partial:        len(partialReasons) > 0,
		PartialReasons: partialReasons,
	}, nil
}

// Dimensions returns distinct values for finite analytics filter dimensions.
func Dimensions(root string, maxScan int) (DimensionsResult, error) {
	rollups, _, err := loadAnalyticsRollups(root, normalizeMaxScan(maxScan))
	if err != nil {
		return DimensionsResult{}, err
	}
	providers := make([]string, 0, 16)
	models := make([]string, 0, 32)
	directories := make([]string, 0, 64)
	projects := make([]string, 0, 16)
	statuses := make([]string, 0, 8)
	for _, rollup := range rollups {
		providers = appendDimensionValue(providers, rollup.Provider)
		models = appendDimensionValue(models, rollup.Model)
		directories = appendDimensionValue(directories, rollup.Directory)
		projects = appendDimensionValue(projects, rollup.Project)
		statuses = appendDimensionValue(statuses, rollup.Status)
	}
	for _, values := range [][]string{providers, models, directories, projects, statuses} {
		sort.SliceStable(values, func(i, j int) bool {
			return strings.ToLower(values[i]) < strings.ToLower(values[j])
		})
	}
	return DimensionsResult{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Now(),
		Providers:     providers,
		Models:        models,
		Directories:   directories,
		Projects:      projects,
		Statuses:      statuses,
	}, nil
}

func appendDimensionValue(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if strings.EqualFold(existing, value) {
			return values
		}
	}
	return append(values, value)
}

// Summarize aggregates matched sessions into global totals and group buckets.
func Summarize(root string, query Query) (SummaryResult, error) {
	groupBy := normalizeGroupBy(query.GroupBy)
	limit := normalizeLimit(query.Limit, defaultSummaryLimit, maxSummaryLimit)
	maxScan := normalizeMaxScan(query.MaxScan)

	rollups, scanned, err := loadAnalyticsRollups(root, maxScan)
	if err != nil {
		return SummaryResult{}, err
	}

	matched := make([]SessionRollup, 0, len(rollups))
	for _, rollup := range rollups {
		if !matchRollup(rollup, query) {
			continue
		}
		matched = append(matched, rollup)
	}

	totals := accumulateTotals(matched)
	groups := groupRollups(matched, groupBy)

	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].TotalTokens == groups[j].TotalTokens {
			if groups[i].Sessions == groups[j].Sessions {
				return groups[i].Key < groups[j].Key
			}
			return groups[i].Sessions > groups[j].Sessions
		}
		return groups[i].TotalTokens > groups[j].TotalTokens
	})
	if len(groups) > limit {
		groups = groups[:limit]
	}

	window := dataWindowFor(matched)
	coverage := coverageFor(matched)
	partialReasons := aggregatePartialReasons(matched)
	return SummaryResult{
		SchemaVersion:  SchemaVersion,
		GeneratedAt:    time.Now(),
		GroupBy:        groupBy,
		Totals:         totals,
		Groups:         groups,
		Scanned:        scanned,
		Matched:        len(matched),
		DataWindow:     window,
		Coverage:       coverage,
		Partial:        len(partialReasons) > 0,
		PartialReasons: partialReasons,
	}, nil
}

// SessionUsage returns one session's rollup plus debug LLM step usage.
func SessionUsage(root, sessionID string) (SessionUsageDetail, error) {
	if detail, enabled, err := indexedSessionUsage(root, sessionID); enabled {
		return detail, err
	}
	dir, ok, err := ResolveSessionDir(root, sessionID)
	if err != nil {
		return SessionUsageDetail{}, err
	}
	if !ok {
		return SessionUsageDetail{}, errSessionNotFound(sessionID)
	}

	rollup, steps, evidence, loadErr := loadSessionRollup(dir, true)
	if loadErr != nil {
		return SessionUsageDetail{}, loadErr
	}

	turns := buildTurns(steps, evidence)
	return buildSessionUsageDetail(rollup, steps, turns), nil
}

type notFoundError struct {
	sessionID string
}

func (e notFoundError) Error() string {
	return "session not found: " + e.sessionID
}

func errSessionNotFound(sessionID string) error {
	return notFoundError{sessionID: strings.TrimSpace(sessionID)}
}

// IsNotFound reports whether err is a missing session error.
func IsNotFound(err error) bool {
	_, ok := err.(notFoundError)
	return ok
}

func normalizeGroupBy(groupBy string) string {
	switch strings.ToLower(strings.TrimSpace(groupBy)) {
	case "", "day":
		return "day"
	case "provider":
		return "provider"
	case "model":
		return "model"
	case "directory":
		return "directory"
	case "project":
		return "project"
	case "status":
		return "status"
	default:
		return "day"
	}
}

func rollupSortTime(rollup SessionRollup) time.Time {
	if !rollup.LastObservedAt.IsZero() {
		return rollup.LastObservedAt
	}
	if !rollup.EndTime.IsZero() {
		return rollup.EndTime
	}
	if !rollup.StartTime.IsZero() {
		return rollup.StartTime
	}
	return time.Time{}
}

func matchTimeWindow(ts time.Time, from, to time.Time) bool {
	if ts.IsZero() {
		// Keep unknown timestamps when no hard bound is required; caller may still include.
		return from.IsZero() && to.IsZero()
	}
	if !from.IsZero() && ts.Before(from) {
		return false
	}
	if !to.IsZero() && !ts.Before(to) {
		return false
	}
	return true
}

func matchRollup(rollup SessionRollup, query Query) bool {
	if !matchTimeWindow(rollup.StartTime, query.From, query.To) {
		// fall back to last observed for ongoing sessions without start
		if rollup.StartTime.IsZero() {
			if !matchTimeWindow(rollupSortTime(rollup), query.From, query.To) {
				return false
			}
		} else {
			return false
		}
	}
	if !matchDirectoryFilter(rollup.Directory, query.Directory) {
		return false
	}
	if project := strings.TrimSpace(query.Project); project != "" {
		if !strings.EqualFold(filepath.Clean(rollup.Project), filepath.Clean(project)) {
			return false
		}
	}
	if provider := strings.TrimSpace(query.Provider); provider != "" {
		if !strings.EqualFold(strings.TrimSpace(rollup.Provider), provider) {
			return false
		}
	}
	if model := strings.TrimSpace(query.Model); model != "" {
		if !strings.EqualFold(strings.TrimSpace(rollup.Model), model) {
			return false
		}
	}
	if status := strings.TrimSpace(query.Status); status != "" {
		if !strings.EqualFold(strings.TrimSpace(rollup.Status), status) {
			return false
		}
	}
	if q := strings.ToLower(strings.TrimSpace(query.Query)); q != "" {
		haystack := strings.ToLower(strings.Join([]string{
			rollup.SessionID,
			rollup.RuntimeSessionID,
			rollup.Title,
			rollup.Provider,
			rollup.Model,
			rollup.Directory,
			rollup.Project,
			rollup.RelPath,
			rollup.Status,
			rollup.Protocol,
		}, " "))
		if !strings.Contains(haystack, q) {
			return false
		}
	}
	return true
}

func accumulateTotals(rollups []SessionRollup) GlobalTotals {
	var totals GlobalTotals
	var weightedResponse int64
	var responseSamples int64

	for _, rollup := range rollups {
		totals.Sessions++
		totals.TotalRequests += rollup.TotalRequests
		totals.TotalResponses += rollup.TotalResponses
		totals.TotalToolCalls += rollup.TotalToolCalls
		totals.LLMRequests += rollup.LLMRequests
		totals.LLMSuccesses += rollup.LLMSuccesses
		totals.LLMErrors += rollup.LLMErrors
		totals.Turns += rollup.TurnCount
		totals.FailedTurns += rollup.FailedTurns
		totals.RecoveredTurns += rollup.RecoveredTurns
		totals.ToolResultsObserved += rollup.ToolResultsObserved
		totals.ToolErrors += rollup.ToolErrors
		totals.TotalDurationMs += rollup.TotalDurationMs
		totals.TotalTokens += rollup.TotalTokens
		totals.PromptTokens += rollup.PromptTokens
		totals.CompletionTokens += rollup.CompletionTokens
		totals.CachedTokens += rollup.CachedTokens
		totals.ReasoningTokens += rollup.ReasoningTokens
		if rollup.AverageResponseTimeMs > 0 {
			weightedResponse += rollup.AverageResponseTimeMs
			responseSamples++
		}
	}
	if responseSamples > 0 {
		totals.AverageResponseTimeMs = weightedResponse / responseSamples
	}
	return totals
}

func groupKeyFor(rollup SessionRollup, groupBy string) string {
	switch groupBy {
	case "provider":
		if v := strings.TrimSpace(rollup.Provider); v != "" {
			return v
		}
		return "(unknown)"
	case "model":
		if v := strings.TrimSpace(rollup.Model); v != "" {
			return v
		}
		return "(unknown)"
	case "directory":
		if v := strings.TrimSpace(rollup.Directory); v != "" {
			return v
		}
		return "."
	case "project":
		if v := strings.TrimSpace(rollup.Project); v != "" {
			return v
		}
		return "(unknown)"
	case "status":
		if v := strings.TrimSpace(rollup.Status); v != "" {
			return v
		}
		return "(unknown)"
	case "day":
		fallthrough
	default:
		ts := rollup.StartTime
		if ts.IsZero() {
			ts = rollupSortTime(rollup)
		}
		if ts.IsZero() {
			// try directory date partition
			if parts := strings.Split(strings.Trim(rollup.Directory, "/"), "/"); len(parts) >= 3 {
				return strings.Join(parts[:3], "-")
			}
			return "(unknown)"
		}
		return ts.In(time.Local).Format("2006-01-02")
	}
}

func groupRollups(rollups []SessionRollup, groupBy string) []GroupBucket {
	index := make(map[string]int, len(rollups))
	groups := make([]GroupBucket, 0, 16)
	responseSamples := make(map[string]int64, 16)
	responseSums := make(map[string]int64, 16)

	for _, rollup := range rollups {
		key := groupKeyFor(rollup, groupBy)
		idx, ok := index[key]
		if !ok {
			idx = len(groups)
			index[key] = idx
			groups = append(groups, GroupBucket{Key: key})
		}
		bucket := &groups[idx]
		bucket.Sessions++
		bucket.TotalRequests += rollup.TotalRequests
		bucket.TotalResponses += rollup.TotalResponses
		bucket.TotalToolCalls += rollup.TotalToolCalls
		bucket.LLMRequests += rollup.LLMRequests
		bucket.LLMSuccesses += rollup.LLMSuccesses
		bucket.LLMErrors += rollup.LLMErrors
		bucket.Turns += rollup.TurnCount
		bucket.FailedTurns += rollup.FailedTurns
		bucket.RecoveredTurns += rollup.RecoveredTurns
		bucket.ToolResultsObserved += rollup.ToolResultsObserved
		bucket.ToolErrors += rollup.ToolErrors
		bucket.TotalDurationMs += rollup.TotalDurationMs
		bucket.TotalTokens += rollup.TotalTokens
		bucket.PromptTokens += rollup.PromptTokens
		bucket.CompletionTokens += rollup.CompletionTokens
		bucket.CachedTokens += rollup.CachedTokens
		bucket.ReasoningTokens += rollup.ReasoningTokens
		if rollup.AverageResponseTimeMs > 0 {
			responseSums[key] += rollup.AverageResponseTimeMs
			responseSamples[key]++
		}
	}

	for i := range groups {
		key := groups[i].Key
		if samples := responseSamples[key]; samples > 0 {
			groups[i].AverageResponseTimeMs = responseSums[key] / samples
		}
	}
	return groups
}

func coverageFor(rollups []SessionRollup) Coverage {
	coverage := Coverage{Sessions: len(rollups)}
	for _, rollup := range rollups {
		if rollup.TotalTokens > 0 || rollup.UsageQuality != "missing" {
			coverage.SessionsWithUsage++
		}
		expected := rollup.TotalRequests
		if expected < rollup.LLMRequests {
			expected = rollup.LLMRequests
		}
		coverage.LLMRequests += expected
		coverage.LLMRequestsWithUsage += rollup.LLMRequestsWithUsage
		coverage.ToolResultsObserved += rollup.ToolResultsObserved
		coverage.DroppedMessages += rollup.DroppedMessages
	}
	if coverage.Sessions > 0 {
		coverage.UsageSessionRate = float64(coverage.SessionsWithUsage) / float64(coverage.Sessions)
	}
	if coverage.LLMRequests > 0 {
		coverage.UsageRequestRate = float64(coverage.LLMRequestsWithUsage) / float64(coverage.LLMRequests)
		if coverage.UsageRequestRate > 1 {
			coverage.UsageRequestRate = 1
		}
	}
	return coverage
}

func dataWindowFor(rollups []SessionRollup) DataWindow {
	var window DataWindow
	for _, rollup := range rollups {
		start := rollup.StartTime
		end := rollupSortTime(rollup)
		if window.From.IsZero() || (!start.IsZero() && start.Before(window.From)) {
			window.From = start
		}
		if end.After(window.To) {
			window.To = end
		}
	}
	return window
}

func aggregatePartialReasons(rollups []SessionRollup) []string {
	set := make(map[string]struct{})
	for _, rollup := range rollups {
		for _, reason := range rollup.PartialReasons {
			set[reason] = struct{}{}
		}
	}
	reasons := make([]string, 0, len(set))
	for reason := range set {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	return reasons
}

func errorCategoryCounts(steps []StepUsage) map[string]int {
	counts := make(map[string]int)
	for _, step := range steps {
		if category := strings.TrimSpace(step.ErrorCategory); category != "" {
			counts[category]++
		}
	}
	return counts
}

func buildDiagnostics(rollup SessionRollup, turns []TurnUsage) []Diagnostic {
	diagnostics := make([]Diagnostic, 0, 6)
	if rollup.LLMErrors > 0 {
		rate := float64(rollup.LLMErrors) / float64(max(rollup.LLMRequests, 1))
		severity := "info"
		if rate >= 0.05 {
			severity = "warning"
		}
		diagnostics = append(diagnostics, Diagnostic{Code: "llm_failures", Severity: severity, Count: rollup.LLMErrors, Rate: rate})
	}
	if rollup.FailedTurns > 0 {
		diagnostics = append(diagnostics, Diagnostic{Code: "failed_turns", Severity: "error", Count: rollup.FailedTurns, Rate: float64(rollup.FailedTurns) / float64(max(rollup.TurnCount, 1))})
	}
	if rollup.ToolErrors > 0 {
		severity := "info"
		rate := float64(rollup.ToolErrors) / float64(max(rollup.ToolResultsObserved, 1))
		if rate >= 0.05 {
			severity = "warning"
		}
		diagnostics = append(diagnostics, Diagnostic{Code: "tool_errors_observed", Severity: severity, Count: rollup.ToolErrors, Rate: rate})
	}
	for _, turn := range turns {
		if turn.MaxContextUtilization >= 0.8 {
			diagnostics = append(diagnostics, Diagnostic{Code: "context_pressure", Severity: "warning", Count: 1, Rate: turn.MaxContextUtilization, TurnID: turn.TurnID})
		}
	}
	if rollup.ReconciliationStatus == "mismatch" {
		diagnostics = append(diagnostics, Diagnostic{Code: "usage_reconciliation_mismatch", Severity: "warning", Count: abs(rollup.ReconciliationDelta)})
	}
	if rollup.Partial {
		diagnostics = append(diagnostics, Diagnostic{Code: "partial_diagnostic_evidence", Severity: "info", Count: len(rollup.PartialReasons)})
	}
	return diagnostics
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func pageRollups(rollups []SessionRollup, offset, limit int) []SessionRollup {
	if offset >= len(rollups) {
		return []SessionRollup{}
	}
	end := offset + limit
	if end > len(rollups) {
		end = len(rollups)
	}
	page := make([]SessionRollup, end-offset)
	copy(page, rollups[offset:end])
	return page
}
