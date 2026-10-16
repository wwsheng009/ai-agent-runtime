package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// ---------------------------------------------------------------------------
// P2-4 second batch: Exa / Perplexity provider tests. Request mapping, result
// mapping, answer sections, availability gating, and the multi-query answer
// merge path share the newAPITestTool fixture from web_search_api_test.go.
// ---------------------------------------------------------------------------

func TestExaProviderRequestAndMapping(t *testing.T) {
	tool := newAPITestTool(t, "exa", nil, func(cfg *runtimecfg.RuntimeConfig) {
		cfg.WebSearch.API.Exa.APIKey = "exa-key"
	})
	var captured *http.Request
	var capturedBody string
	tool.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		captured = req
		body, _ := io.ReadAll(req.Body)
		capturedBody = string(body)
		payload := `{"results":[
			{"title":"Exa 结果一","url":"https://exa.example/1","publishedDate":"2026-10-02","highlights":["亮点 <b>一</b> &amp; 片段"],"text":"全文文本"},
			{"title":"Exa 结果二","url":"https://exa.example/2","publishedDate":"2026-10-03","highlights":[],"text":"回退文本"}
		]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload))}, nil
	})}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":      "exa query",
		"count":      float64(2),
		"time_range": "week",
		"language":   "zh-CN",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success || result.Metadata["source"] != exaProviderName {
		t.Fatalf("exa result = %+v / %v", result.Metadata, result.Error)
	}
	if captured == nil || captured.Method != http.MethodPost || captured.URL.Host != "api.exa.ai" || captured.URL.Path != "/search" {
		t.Fatalf("request = %#v", captured)
	}
	if captured.Header.Get("x-api-key") != "exa-key" {
		t.Fatalf("api key header = %#v", captured.Header)
	}
	for _, want := range []string{`"numResults":2`, `"maxCharacters":1000`, `"numSentences":3`, `"exa query"`} {
		if !strings.Contains(capturedBody, want) {
			t.Fatalf("body missing %s: %s", want, capturedBody)
		}
	}
	if !strings.Contains(result.Content, "亮点 一 & 片段") || strings.Contains(result.Content, "<b>") {
		t.Fatalf("highlight cleanup wrong: %q", result.Content)
	}
	if !strings.Contains(result.Content, "回退文本") {
		t.Fatalf("text fallback missing: %q", result.Content)
	}
	if result.Metadata["time_range_supported"] != false || result.Metadata["language_supported"] != false {
		t.Fatalf("exa capability flags = %#v", result.Metadata)
	}
	sources, _ := result.Metadata["sources"].([]map[string]interface{})
	if len(sources) != 2 || sources[0]["published_at"] != "2026-10-02" || sources[1]["published_at"] != "2026-10-03" {
		t.Fatalf("sources projection = %#v", result.Metadata["sources"])
	}
	if strings.Contains(result.Content, "### ") {
		t.Fatalf("exa must not render answer sections: %q", result.Content)
	}
}

func TestPerplexityProviderAnswerAndSources(t *testing.T) {
	tool := newAPITestTool(t, "perplexity", nil, func(cfg *runtimecfg.RuntimeConfig) {
		cfg.WebSearch.API.Perplexity.APIKey = "pplx-key"
	})
	var captured *http.Request
	var capturedBody string
	tool.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		captured = req
		body, _ := io.ReadAll(req.Body)
		capturedBody = string(body)
		payload := `{"choices":[{"message":{"content":"这是生成的答案。"}}],
			"search_results":[
				{"title":"来源甲","url":"https://pplx.example/1","date":"2026-10-04"},
				{"title":"来源乙","url":"https://pplx.example/2"}
			],
			"citations":["https://pplx.example/1","https://pplx.example/3"]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload))}, nil
	})}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "perplexity 问题"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success || result.Metadata["source"] != perplexityProviderName {
		t.Fatalf("perplexity result = %+v / %v", result.Metadata, result.Error)
	}
	if captured == nil || captured.Method != http.MethodPost || captured.URL.Host != "api.perplexity.ai" || captured.URL.Path != "/chat/completions" {
		t.Fatalf("request = %#v", captured)
	}
	if captured.Header.Get("authorization") != "Bearer pplx-key" {
		t.Fatalf("auth header = %#v", captured.Header)
	}
	for _, want := range []string{`"model":"sonar"`, `"content":"perplexity 问题"`} {
		if !strings.Contains(capturedBody, want) {
			t.Fatalf("body missing %s: %s", want, capturedBody)
		}
	}
	if !strings.Contains(result.Content, "### perplexity 问题") || !strings.Contains(result.Content, "这是生成的答案。") {
		t.Fatalf("answer section missing: %q", result.Content)
	}
	if result.Metadata["has_answer"] != true {
		t.Fatalf("has_answer metadata missing: %#v", result.Metadata)
	}
	answered, _ := result.Metadata["answer_queries"].([]string)
	if len(answered) != 1 || answered[0] != "perplexity 问题" {
		t.Fatalf("answer_queries = %#v", result.Metadata["answer_queries"])
	}
	sources, _ := result.Metadata["sources"].([]map[string]interface{})
	// search_results win over citations (strict precedence): only the two
	// search_results rows are projected, the citation-only URL is not merged.
	if len(sources) != 2 || sources[0]["title"] != "来源甲" || sources[0]["published_at"] != "2026-10-04" {
		t.Fatalf("sources projection = %#v", result.Metadata["sources"])
	}
}

func TestPerplexityFallsBackToCitations(t *testing.T) {
	tool := newAPITestTool(t, "perplexity", nil, func(cfg *runtimecfg.RuntimeConfig) {
		cfg.WebSearch.API.Perplexity.APIKey = "pplx-key"
	})
	tool.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		payload := `{"choices":[{"message":{"content":"答案正文"}}],"citations":["https://cite.example/a","https://cite.example/b"]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload))}, nil
	})}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "cite fallback",
		"count": float64(1),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Error)
	}
	sources, _ := result.Metadata["sources"].([]map[string]interface{})
	if len(sources) != 1 || sources[0]["url"] != "https://cite.example/a" {
		t.Fatalf("citation fallback projection = %#v", result.Metadata["sources"])
	}
	if _, present := sources[0]["title"]; present {
		t.Fatalf("citation-only rows must not invent a title: %#v", sources[0])
	}
}

func TestPerplexityAnswerOnlySucceeds(t *testing.T) {
	tool := newAPITestTool(t, "perplexity", nil, func(cfg *runtimecfg.RuntimeConfig) {
		cfg.WebSearch.API.Perplexity.APIKey = "pplx-key"
	})
	tool.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		payload := `{"choices":[{"message":{"content":"只有答案。"}}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload))}, nil
	})}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "solo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("answer-only response must succeed: %v", result.Error)
	}
	if result.Metadata["count"] != 0 || result.Metadata["has_answer"] != true {
		t.Fatalf("answer-only metadata = %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "### solo") || !strings.Contains(result.Content, "只有答案。") {
		t.Fatalf("answer section missing: %q", result.Content)
	}
	if strings.Contains(result.Content, "🔍 搜索结果") {
		t.Fatalf("answer-only content must not render an empty sources header: %q", result.Content)
	}
	if _, present := result.Metadata[toolresult.MetadataNextActionKey]; present {
		t.Fatalf("answer-bearing success must not carry LOW_SEARCH_QUALITY next_action: %#v", result.Metadata)
	}
}

func TestPerplexityModelOverride(t *testing.T) {
	tool := newAPITestTool(t, "perplexity", nil, func(cfg *runtimecfg.RuntimeConfig) {
		cfg.WebSearch.API.Perplexity.APIKey = "pplx-key"
		cfg.WebSearch.API.Perplexity.Model = "sonar-pro"
	})
	if tool.perplexity.model != "sonar-pro" {
		t.Fatalf("model = %q", tool.perplexity.model)
	}
	var capturedBody string
	tool.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		capturedBody = string(body)
		payload := `{"choices":[{"message":{"content":"ok"}}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload))}, nil
	})}

	if _, err := tool.Execute(context.Background(), map[string]interface{}{"query": "model probe"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(capturedBody, `"model":"sonar-pro"`) {
		t.Fatalf("model override missing from body: %s", capturedBody)
	}
}

func TestPerplexityMultiQueryMergesAnswerSections(t *testing.T) {
	tool := newAPITestTool(t, "perplexity", nil, func(cfg *runtimecfg.RuntimeConfig) {
		cfg.WebSearch.API.Perplexity.APIKey = "pplx-key"
	})
	tool.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(req.Body)
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil || len(payload.Messages) == 0 {
			return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		answer, _ := json.Marshal("解答-" + payload.Messages[0].Content)
		body := fmt.Sprintf(`{"choices":[{"message":{"content":%s}}]}`, answer)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"queries": []interface{}{"q1", "q2"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Error)
	}
	for _, want := range []string{"### q1", "### q2", "解答-q1", "解答-q2"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("content missing %q: %q", want, result.Content)
		}
	}
	if result.Metadata["has_answer"] != true {
		t.Fatalf("has_answer metadata missing: %#v", result.Metadata)
	}
	answered, _ := result.Metadata["answer_queries"].([]string)
	if len(answered) != 2 || answered[0] != "q1" || answered[1] != "q2" {
		t.Fatalf("answer_queries = %#v", result.Metadata["answer_queries"])
	}
	if _, present := result.Metadata["queries"]; !present {
		t.Fatalf("multi-query metadata missing: %#v", result.Metadata)
	}
}

func TestAPIProviderUnauthorizedMapsToAPIUnauthorized(t *testing.T) {
	tool := newAPITestTool(t, "exa", nil, func(cfg *runtimecfg.RuntimeConfig) {
		cfg.WebSearch.API.Exa.APIKey = "bad-key"
	})
	tool.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(strings.NewReader(`{"error":"invalid api key"}`)),
		}, nil
	})}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "auth probe"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected auth failure")
	}
	if got := result.Metadata["error_code"]; got != string(runtimeerrors.ErrAPIUnauthorized) {
		t.Fatalf("error_code = %v, want API_UNAUTHORIZED", got)
	}
	if result.Metadata["failure_class"] != "auth" || result.Metadata["failure_provider"] != exaProviderName {
		t.Fatalf("failure metadata = %#v", result.Metadata)
	}
}
