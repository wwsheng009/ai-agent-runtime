package tools

import (
	"fmt"
	"strings"
)

// parseWebSearchQueries parses the P2-3 query surface: an explicit `queries`
// array wins over the historical single `query`. The bound is checked before
// duplicate collapsing (DSH order); duplicates are then removed preserving the
// first occurrence.
func parseWebSearchQueries(params map[string]interface{}, maxQueries int) ([]string, error) {
	if raw, ok := params["queries"]; ok && raw != nil {
		queries, err := parseWebSearchQueryList(raw)
		if err != nil {
			return nil, err
		}
		if len(queries) == 0 {
			return nil, fmt.Errorf("queries 至少需要一条查询")
		}
		if len(queries) > maxQueries {
			return nil, fmt.Errorf("queries 最多支持 %d 条（收到 %d 条）", maxQueries, len(queries))
		}
		deduped := make([]string, 0, len(queries))
		seen := make(map[string]struct{}, len(queries))
		for _, query := range queries {
			if _, exists := seen[query]; exists {
				continue
			}
			seen[query] = struct{}{}
			deduped = append(deduped, query)
		}
		return deduped, nil
	}
	query, ok := params["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query 参数缺失或为空")
	}
	return []string{query}, nil
}

func parseWebSearchQueryList(raw interface{}) ([]string, error) {
	switch value := raw.(type) {
	case []string:
		return validateWebSearchQueryList(value)
	case []interface{}:
		list := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("queries 的每一项必须是字符串")
			}
			list = append(list, text)
		}
		return validateWebSearchQueryList(list)
	case string:
		// Tolerate a JSON-encoded single value.
		return validateWebSearchQueryList([]string{value})
	default:
		return nil, fmt.Errorf("queries 必须是字符串数组")
	}
}

func validateWebSearchQueryList(list []string) ([]string, error) {
	out := make([]string, 0, len(list))
	for _, query := range list {
		if strings.TrimSpace(query) == "" {
			return nil, fmt.Errorf("每条 query 必须为非空字符串")
		}
		out = append(out, query)
	}
	return out, nil
}

// attemptedWebSearchArgs preserves the caller's query shape for diagnostics.
func attemptedWebSearchArgs(params map[string]interface{}) map[string]interface{} {
	args := map[string]interface{}{}
	if query, ok := params["query"]; ok {
		args["query"] = query
	}
	if queries, ok := params["queries"]; ok {
		args["queries"] = queries
	}
	return args
}

// mergeWebSearchRuns merges the selected attempts of the successful queries:
// round-robin by rank across queries (query order preserved), URL first-seen
// dedupe, capped at maxResults. It also reports per-query kept counts, the
// unique provider sources that participated, and whether the cap truncated.
func mergeWebSearchRuns(queries []string, runs []webSearchChainRun, maxResults int) ([]DuckDuckGoResult, map[string]int, []string, bool) {
	selected := make([]*webSearchAttempt, len(runs))
	maxRank := 0
	perQuery := make(map[string]int, len(queries))
	providerSources := make([]string, 0, len(runs))
	seenSources := make(map[string]struct{}, len(runs))
	for index, run := range runs {
		selected[index] = run.attempt
		if run.attempt == nil {
			continue
		}
		if len(run.attempt.results) > maxRank {
			maxRank = len(run.attempt.results)
		}
		if run.attempt.source != "" {
			if _, exists := seenSources[run.attempt.source]; !exists {
				seenSources[run.attempt.source] = struct{}{}
				providerSources = append(providerSources, run.attempt.source)
			}
		}
	}

	merged := make([]DuckDuckGoResult, 0, maxRank*len(runs))
	seenURLs := make(map[string]struct{})
	truncated := false
merge:
	for rank := 0; rank < maxRank; rank++ {
		for index, attempt := range selected {
			if attempt == nil || rank >= len(attempt.results) {
				continue
			}
			result := attempt.results[rank]
			if result.URL != "" {
				if _, exists := seenURLs[result.URL]; exists {
					continue
				}
				seenURLs[result.URL] = struct{}{}
			}
			if maxResults > 0 && len(merged) >= maxResults {
				truncated = true
				break merge
			}
			merged = append(merged, result)
			perQuery[queries[index]]++
		}
	}
	return merged, perQuery, providerSources, truncated
}
