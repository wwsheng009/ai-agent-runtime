package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// webSearchChainRun is one full provider-chain execution for a single query.
type webSearchChainRun struct {
	query            string
	attempt          *webSearchAttempt
	attempts         []webSearchAttempt
	failures         []providerFailure
	budgetExhausted  bool
	timedOutProvider string
}

// runChain executes the provider chain once with the historical selection
// semantics: a quality-ok attempt returns immediately; otherwise the best
// scoring attempt is kept; total failure keeps only the failure list. The
// caller owns the chain budget (P1-2) and passes its deadline through ctx.
func (w *WebSearchTool) runChain(ctx context.Context, query string, count int, filter webSearchDomainFilter, options webSearchOptions) webSearchChainRun {
	run := webSearchChainRun{query: query}
	diagnostics := webSearchDiagnosticsFromContext(ctx)
	lastProvider := ""
	bestIndex := -1
	for _, provider := range w.providers {
		if ctx.Err() != nil {
			// Only a deadline counts as budget exhaustion; a cancellation (caller
			// cancel or strict-multi-query abort) stops the chain without the
			// budget flag.
			if ctx.Err() == context.DeadlineExceeded {
				run.budgetExhausted = true
			}
			break
		}
		lastProvider = provider.Name()
		if diagnostics != nil {
			diagnostics.finalHost = ""
		}
		providerResult, err := provider.Search(ctx, WebSearchRequest{Query: query, Count: count, TimeRange: options.timeRange, Language: options.language})
		if err != nil {
			run.failures = append(run.failures, providerFailure{name: provider.Name(), err: err})
			continue
		}
		source, results := providerResult.Source, providerResult.Results
		kept, dropped := filter.apply(results)
		finalHost := ""
		if diagnostics != nil {
			finalHost = diagnostics.finalHost
		}
		attempt := webSearchAttempt{
			name:        provider.Name(),
			source:      source,
			results:     kept,
			rawCount:    len(results),
			quality:     assessWebSearchQuality(query, kept),
			filteredOut: dropped,
			finalHost:   finalHost,
		}
		run.attempts = append(run.attempts, attempt)
		currentIndex := len(run.attempts) - 1
		if bestIndex < 0 || run.attempts[currentIndex].quality.Score > run.attempts[bestIndex].quality.Score {
			bestIndex = currentIndex
		}
		if run.attempts[currentIndex].quality.Tier == "ok" && len(run.attempts[currentIndex].results) > 0 {
			selected := run.attempts[currentIndex]
			run.attempt = &selected
			return run
		}
	}

	if run.budgetExhausted {
		// 停止换链本身就耗尽预算：补一条合成失败，保证错误分类（超时）与
		// next_action 可路由，而不是退化成 "web_search failed"。
		run.failures = append(run.failures, providerFailure{
			name: "budget",
			err:  fmt.Errorf("search chain budget exhausted: %w", ctx.Err()),
		})
	}
	if ctx.Err() == context.DeadlineExceeded {
		run.timedOutProvider = lastProvider
	}
	if bestIndex >= 0 {
		selected := run.attempts[bestIndex]
		run.attempt = &selected
	}
	return run
}

// finishSingleQuery formats one chain run exactly as the historical single-query
// path did (success of the selected attempt, or a structured failure).
func (w *WebSearchTool) finishSingleQuery(query string, count int, filter webSearchDomainFilter, options webSearchOptions, run webSearchChainRun, started time.Time) *toolkit.ToolResult {
	if run.attempt != nil {
		return w.stampBudgetMetadata(w.successSearchAttempt(query, *run.attempt, filter, run.attempts, run.failures, options, count), started, run.budgetExhausted, run.timedOutProvider)
	}
	return w.stampBudgetMetadata(w.failureSearchResult(query, run.failures), started, run.budgetExhausted, run.timedOutProvider)
}

// executeMultiQuery runs 2..maxWebSearchQueries chains concurrently under the
// shared chain budget (P2-3) and merges the successful ones. Default mode keeps
// partial successes; webSearch.strictMultiQuery aborts the whole call on the
// first failed query and cancels its siblings (DSH-style).
func (w *WebSearchTool) executeMultiQuery(ctx context.Context, queries []string, count int, filter webSearchDomainFilter, options webSearchOptions, started time.Time) *toolkit.ToolResult {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	runs := make([]webSearchChainRun, len(queries))
	var waitGroup sync.WaitGroup
	var firstFailureLock sync.Mutex
	firstFailureIndex := -1
	for index := range queries {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			run := w.runChain(ctx, queries[index], count, filter, options)
			runs[index] = run
			if w.strictMultiQuery && run.attempt == nil {
				firstFailureLock.Lock()
				if firstFailureIndex == -1 {
					firstFailureIndex = index
				}
				firstFailureLock.Unlock()
				cancel()
			}
		}(index)
	}
	waitGroup.Wait()

	budgetExhausted := false
	timedOutProvider := ""
	for _, run := range runs {
		if run.budgetExhausted {
			budgetExhausted = true
		}
		if timedOutProvider == "" {
			timedOutProvider = run.timedOutProvider
		}
	}

	if w.strictMultiQuery && firstFailureIndex >= 0 {
		failed := runs[firstFailureIndex]
		result := w.failureSearchResult(failed.query, failed.failures)
		result.Metadata["queries"] = append([]string(nil), queries...)
		return w.stampBudgetMetadata(result, started, budgetExhausted, timedOutProvider)
	}

	successes := 0
	queryFailures := map[string]interface{}{}
	var mergedFailures []providerFailure
	for _, run := range runs {
		if run.attempt != nil {
			successes++
		}
		for _, failure := range run.failures {
			mergedFailures = append(mergedFailures, providerFailure{name: run.query + ":" + failure.name, err: failure.err})
		}
		if run.attempt == nil && len(run.failures) > 0 {
			parts := make([]string, 0, len(run.failures))
			for _, failure := range run.failures {
				parts = append(parts, failure.err.Error())
			}
			queryFailures[run.query] = strings.Join(parts, "; ")
		}
	}

	if successes == 0 {
		result := w.failureSearchResult(queries[0], mergedFailures)
		result.Metadata["queries"] = append([]string(nil), queries...)
		if len(queryFailures) > 0 {
			result.Metadata["query_failures"] = queryFailures
		}
		return w.stampBudgetMetadata(result, started, budgetExhausted, timedOutProvider)
	}

	merged, perQuerySources, providerSources, truncated := mergeWebSearchRuns(queries, runs, count)
	combined := webSearchAttempt{
		name:        "merged",
		source:      strings.Join(providerSources, ","),
		results:     merged,
		rawCount:    len(merged),
		quality:     assessWebSearchQuality(strings.Join(queries, " "), merged),
		filteredOut: 0,
	}
	result := w.successSearchAttempt(strings.Join(queries, " "), combined, filter, dedupeWebSearchAttempts(runs), mergedFailures, options, count)
	if result.Metadata == nil {
		result.Metadata = map[string]interface{}{}
	}
	result.Metadata["queries"] = append([]string(nil), queries...)
	result.Metadata["merged_count"] = len(merged)
	result.Metadata["per_query_sources"] = perQuerySources
	result.Metadata["provider_sources"] = providerSources
	if truncated {
		result.Metadata["truncated"] = true
	}
	if len(queryFailures) > 0 {
		result.Metadata["query_failures"] = queryFailures
	}
	return w.stampBudgetMetadata(result, started, budgetExhausted, timedOutProvider)
}

// dedupeWebSearchAttempts keeps the first attempt per provider name so the
// merged result's providers_attempted list stays a set, not a per-query bag.
func dedupeWebSearchAttempts(runs []webSearchChainRun) []webSearchAttempt {
	seen := make(map[string]struct{})
	out := make([]webSearchAttempt, 0, len(runs))
	for _, run := range runs {
		for _, attempt := range run.attempts {
			if _, exists := seen[attempt.name]; exists {
				continue
			}
			seen[attempt.name] = struct{}{}
			out = append(out, attempt)
		}
	}
	return out
}
