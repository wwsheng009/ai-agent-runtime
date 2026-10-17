package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// perQueryFakeProvider answers each query from a map; unknown queries fail.
func perQueryFakeProvider(answers map[string][]DuckDuckGoResult) WebSearchProvider {
	return newWebSearchProvider("fake", func(ctx context.Context, query string, count int) (string, []DuckDuckGoResult, error) {
		results, ok := answers[query]
		if !ok {
			return "", nil, fmt.Errorf("no answer for %q", query)
		}
		return "fake", results, nil
	}, nil)
}

func TestParseWebSearchQueries(t *testing.T) {
	cases := []struct {
		name    string
		params  map[string]interface{}
		want    string
		wantErr bool
	}{
		{name: "query only", params: map[string]interface{}{"query": "a"}, want: "a"},
		{name: "queries array", params: map[string]interface{}{"queries": []interface{}{"a", "b"}}, want: "a,b"},
		{name: "queries dedupe keeps order", params: map[string]interface{}{"queries": []interface{}{"a", "b", "a"}}, want: "a,b"},
		{name: "queries wins over query", params: map[string]interface{}{"query": "x", "queries": []interface{}{"a"}}, want: "a"},
		{name: "over bound", params: map[string]interface{}{"queries": []interface{}{"a", "b", "c", "d", "e"}}, wantErr: true},
		{name: "blank item", params: map[string]interface{}{"queries": []interface{}{"a", "  "}}, wantErr: true},
		{name: "empty array", params: map[string]interface{}{"queries": []interface{}{}}, wantErr: true},
		{name: "missing", params: map[string]interface{}{}, wantErr: true},
		{name: "blank query", params: map[string]interface{}{"query": " "}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseWebSearchQueries(tc.params, webSearchMaxQueries)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(got, ",") != tc.want {
				t.Fatalf("queries = %v, want %q", got, tc.want)
			}
		})
	}
}

func TestMergeWebSearchRunsRoundRobin(t *testing.T) {
	runs := []webSearchChainRun{
		{query: "a", attempt: &webSearchAttempt{name: "first", source: "bing", results: []DuckDuckGoResult{
			{Title: "a1", URL: "https://a1.example/"},
			{Title: "a2", URL: "https://a2.example/"},
		}}},
		{query: "b", attempt: &webSearchAttempt{name: "second", source: "duckduckgo", results: []DuckDuckGoResult{
			{Title: "b1", URL: "https://b1.example/"},
			{Title: "b2-dup", URL: "https://b1.example/"},
		}}},
	}

	merged, perQuery, sources, truncated := mergeWebSearchRuns([]string{"a", "b"}, runs, 10)
	if len(merged) != 3 || merged[0].Title != "a1" || merged[1].Title != "b1" || merged[2].Title != "a2" {
		t.Fatalf("round-robin merge = %#v", merged)
	}
	if perQuery["a"] != 2 || perQuery["b"] != 1 {
		t.Fatalf("perQuery = %#v", perQuery)
	}
	if strings.Join(sources, ",") != "bing,duckduckgo" {
		t.Fatalf("providerSources = %v", sources)
	}
	if truncated {
		t.Fatalf("truncated must stay false when the cap is not reached")
	}

	capped, _, _, cappedTruncated := mergeWebSearchRuns([]string{"a", "b"}, runs, 2)
	if len(capped) != 2 || !cappedTruncated {
		t.Fatalf("cap enforcement: results=%d truncated=%v", len(capped), cappedTruncated)
	}
}

func TestExecuteMultiQueryMergesPartially(t *testing.T) {
	tool := NewWebSearchTool()
	tool.providers = []WebSearchProvider{perQueryFakeProvider(map[string][]DuckDuckGoResult{
		"q1": {{Title: "q1 result one", URL: "https://one.example/1"}, {Title: "q1 result two", URL: "https://two.example/2"}},
		"q2": {{Title: "q2 result", URL: "https://three.example/3"}},
	})}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"queries": []interface{}{"q1", "q2"},
		"count":   float64(5),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected merged success, got: %v", result.Error)
	}
	if result.Metadata["source"] != "fake" {
		t.Fatalf("source = %v", result.Metadata["source"])
	}
	if result.Metadata["merged_count"] != 3 {
		t.Fatalf("merged_count = %v", result.Metadata["merged_count"])
	}
	queries, _ := result.Metadata["queries"].([]string)
	if strings.Join(queries, ",") != "q1,q2" {
		t.Fatalf("queries metadata = %v", result.Metadata["queries"])
	}
	providerSources, _ := result.Metadata["provider_sources"].([]string)
	if strings.Join(providerSources, ",") != "fake" {
		t.Fatalf("provider_sources = %v", result.Metadata["provider_sources"])
	}
	perQuery, _ := result.Metadata["per_query_sources"].(map[string]int)
	if perQuery["q1"] != 2 || perQuery["q2"] != 1 {
		t.Fatalf("per_query_sources = %#v", result.Metadata["per_query_sources"])
	}
	for _, want := range []string{"q1 result one", "q1 result two", "q2 result"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("merged content missing %q: %q", want, result.Content)
		}
	}
}

func TestExecuteMultiQueryPartialFailureMergesByDefault(t *testing.T) {
	tool := NewWebSearchTool()
	tool.providers = []WebSearchProvider{perQueryFakeProvider(map[string][]DuckDuckGoResult{
		"good": {{Title: "good result", URL: "https://good.example/1"}},
	})}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"queries": []interface{}{"good", "missing"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("partial success must merge by default, got: %v", result.Error)
	}
	if result.Metadata["merged_count"] != 1 {
		t.Fatalf("merged_count = %v", result.Metadata["merged_count"])
	}
	queryFailures, _ := result.Metadata["query_failures"].(map[string]interface{})
	if !strings.Contains(fmt.Sprint(queryFailures["missing"]), "missing") {
		t.Fatalf("query_failures = %#v", result.Metadata["query_failures"])
	}
	providerErrors, _ := result.Metadata["provider_errors"].(map[string]string)
	if !strings.Contains(providerErrors["missing:fake"], "no answer") {
		t.Fatalf("provider_errors must carry the prefixed failure: %#v", result.Metadata["provider_errors"])
	}
}

func TestExecuteMultiQueryStrictAbortsAndCancels(t *testing.T) {
	cfg := &runtimecfg.RuntimeConfig{}
	cfg.WebSearch.StrictMultiQuery = true
	tool := NewWebSearchToolWithConfig(cfg)
	if !tool.strictMultiQuery {
		t.Fatalf("strictMultiQuery config not applied")
	}
	tool.providers = []WebSearchProvider{perQueryFakeProvider(map[string][]DuckDuckGoResult{
		"good": {{Title: "good result", URL: "https://good.example/1"}},
	})}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"queries": []interface{}{"good", "missing"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("strict mode must fail when any query fails")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "missing") {
		t.Fatalf("failure should name the failed query: %v", result.Error)
	}
	if strings.Contains(result.Content, "good result") {
		t.Fatalf("strict mode must not merge sibling results: %q", result.Content)
	}
	if _, ok := result.Metadata["queries"]; !ok {
		t.Fatalf("queries metadata missing on strict failure: %#v", result.Metadata)
	}
}

func TestExecuteMultiQuerySingleElementMatchesSingleQuery(t *testing.T) {
	answers := map[string][]DuckDuckGoResult{
		"only": {{Title: "only result", URL: "https://only.example/1"}},
	}
	arrayTool := NewWebSearchTool()
	arrayTool.providers = []WebSearchProvider{perQueryFakeProvider(answers)}
	arrayResult, err := arrayTool.Execute(context.Background(), map[string]interface{}{
		"queries": []interface{}{"only"},
	})
	if err != nil {
		t.Fatalf("array form: %v", err)
	}

	singleTool := NewWebSearchTool()
	singleTool.providers = []WebSearchProvider{perQueryFakeProvider(answers)}
	singleResult, err := singleTool.Execute(context.Background(), map[string]interface{}{"query": "only"})
	if err != nil {
		t.Fatalf("single form: %v", err)
	}

	if arrayResult.Content != singleResult.Content {
		t.Fatalf("single-element queries must match query form:\narray=%q\nsingle=%q", arrayResult.Content, singleResult.Content)
	}
	if arrayResult.Metadata["source"] != singleResult.Metadata["source"] {
		t.Fatalf("source mismatch: %v vs %v", arrayResult.Metadata["source"], singleResult.Metadata["source"])
	}
}

func TestExecuteMultiQueryCancellationJoins(t *testing.T) {
	tool := NewWebSearchTool()
	tool.providers = []WebSearchProvider{newWebSearchProvider("slow", func(ctx context.Context, query string, count int) (string, []DuckDuckGoResult, error) {
		<-ctx.Done()
		return "", nil, ctx.Err()
	}, nil)}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	result, err := tool.Execute(ctx, map[string]interface{}{"queries": []interface{}{"a", "b"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancellation did not join promptly: %s", elapsed)
	}
	if result.Success {
		t.Fatalf("canceled multi-query must not report success")
	}
}

// P3-2：混合取消——不可取消调用自旋退出→结果保留；可取消阻塞调用→终止。
// 断言：已完成查询的结果进入合并输出，被取消查询进 cancelled_queries，
// elapsed 远小于链预算（join 不等待不可取消调用的完整时长）。
func TestExecuteMultiQueryExternalCancellationKeepsFinishedQueries(t *testing.T) {
	tool := NewWebSearchTool()
	tool.providers = []WebSearchProvider{
		perQueryFakeProvider(map[string][]DuckDuckGoResult{
			"spin": {{Title: "spin result", URL: "https://spin.example/1"}},
		}),
		newWebSearchProvider("blocking", func(ctx context.Context, query string, count int) (string, []DuckDuckGoResult, error) {
			<-ctx.Done()
			return "", nil, fmt.Errorf("wrapped: %w", ctx.Err())
		}, nil),
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	result, err := tool.Execute(ctx, map[string]interface{}{"queries": []interface{}{"spin", "blocking"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancellation did not join promptly: %s", elapsed)
	}
	if !result.Success {
		t.Fatalf("finished queries must survive the cancellation: %v", result.Error)
	}
	if result.Metadata["merged_count"] != 1 {
		t.Fatalf("merged_count = %v", result.Metadata["merged_count"])
	}
	cancelled, _ := result.Metadata["cancelled_queries"].([]string)
	if len(cancelled) != 1 || cancelled[0] != "blocking" {
		t.Fatalf("cancelled_queries = %#v", result.Metadata["cancelled_queries"])
	}
	if !strings.Contains(result.Content, "spin result") {
		t.Fatalf("finished result missing from merged content: %q", result.Content)
	}
}

// P3-2：全部查询被取消 → 结构化失败携带 cancelled_queries；query_failures
// 不得把取消误报成真实失败。
func TestExecuteMultiQueryExternalCancellationFailsWithCancelledQueries(t *testing.T) {
	tool := NewWebSearchTool()
	tool.providers = []WebSearchProvider{newWebSearchProvider("slow", func(ctx context.Context, query string, count int) (string, []DuckDuckGoResult, error) {
		<-ctx.Done()
		return "", nil, ctx.Err()
	}, nil)}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	result, err := tool.Execute(ctx, map[string]interface{}{"queries": []interface{}{"a", "b"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("fully cancelled multi-query must fail")
	}
	cancelled, _ := result.Metadata["cancelled_queries"].([]string)
	if len(cancelled) != 2 || cancelled[0] != "a" || cancelled[1] != "b" {
		t.Fatalf("cancelled_queries = %#v", result.Metadata["cancelled_queries"])
	}
	if _, present := result.Metadata["query_failures"]; present {
		t.Fatalf("cancellation must not be reported as query_failures: %#v", result.Metadata["query_failures"])
	}
}

// P3-2：ctx 预先取消 → 两个查询都未执行，全部进 cancelled_queries；
// query_failures 不把「从未执行」误报成真实失败。
func TestExecuteMultiQueryPreCancelledMarksNeverExecutedQueries(t *testing.T) {
	tool := NewWebSearchTool()
	tool.providers = []WebSearchProvider{perQueryFakeProvider(map[string][]DuckDuckGoResult{})}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := tool.Execute(ctx, map[string]interface{}{"queries": []interface{}{"m1", "m2"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("pre-cancelled multi-query must fail")
	}
	cancelled, _ := result.Metadata["cancelled_queries"].([]string)
	if len(cancelled) != 2 || cancelled[0] != "m1" || cancelled[1] != "m2" {
		t.Fatalf("cancelled_queries = %#v", result.Metadata["cancelled_queries"])
	}
	if _, present := result.Metadata["query_failures"]; present {
		t.Fatalf("never-executed query must not be reported as a real failure: %#v", result.Metadata["query_failures"])
	}
}

// P3-2：外部取消与真实失败竞态时，确定性区分：立即真实失败的查询保持
// query_failures 原口径，被取消的阻塞查询进 cancelled_queries。
func TestExecuteMultiQueryGenuineFailureSurvivesExternalCancellation(t *testing.T) {
	tool := NewWebSearchTool()
	// 单 provider 按查询分流（链上每个查询会走完整个链，多 provider 会让
	// genuine 也追加 Canceled 失败、破坏确定性）：genuine 立即真实失败
	// （先于取消完成，不观察 ctx）；blocking 阻塞到取消。
	tool.providers = []WebSearchProvider{newWebSearchProvider("mixed", func(ctx context.Context, query string, count int) (string, []DuckDuckGoResult, error) {
		if query == "genuine" {
			return "", nil, fmt.Errorf("no answer for %q", query)
		}
		<-ctx.Done()
		return "", nil, ctx.Err()
	}, nil)}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	result, err := tool.Execute(ctx, map[string]interface{}{"queries": []interface{}{"genuine", "blocking"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure (no successful query)")
	}
	cancelled, _ := result.Metadata["cancelled_queries"].([]string)
	if len(cancelled) != 1 || cancelled[0] != "blocking" {
		t.Fatalf("cancelled_queries = %#v", result.Metadata["cancelled_queries"])
	}
	queryFailures, _ := result.Metadata["query_failures"].(map[string]interface{})
	if !strings.Contains(fmt.Sprint(queryFailures["genuine"]), "genuine") {
		t.Fatalf("genuine failure must stay in query_failures: %#v", result.Metadata["query_failures"])
	}
}
