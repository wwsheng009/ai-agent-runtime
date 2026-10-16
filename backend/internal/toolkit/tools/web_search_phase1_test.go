package tools

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

func TestWebSearchOutputCarriesTrustNoticeAndCitation(t *testing.T) {
	tool := NewWebSearchTool()
	out := tool.formatResults([]DuckDuckGoResult{{Title: "T", URL: "https://e.example", Snippet: "S"}})
	if !strings.HasPrefix(out, webSearchExternalContentNotice) {
		t.Fatalf("output must start with the trust notice: %q", out)
	}
	if !strings.Contains(out, webSearchCitationInstruction) {
		t.Fatalf("output must carry the citation instruction: %q", out)
	}

	empty := tool.successSearchResultWithCounts("q", nil, "bing", webSearchDomainFilter{}, 0, 0, 5, nil)
	if empty.Metadata["trust"] != "untrusted" {
		t.Fatalf("trust metadata missing: %#v", empty.Metadata)
	}
	if !strings.HasPrefix(empty.Content, webSearchExternalContentNotice) {
		t.Fatalf("empty-result content must carry the notice: %q", empty.Content)
	}
}

func TestWebSearchTimeoutConfiguration(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv(webSearchChainTimeoutEnv, "")
		t.Setenv(webSearchRequestTimeoutEnv, "")
		tool := NewWebSearchToolWithConfig(&runtimecfg.RuntimeConfig{})
		if tool.chainTimeout != defaultWebSearchChainTimeout || tool.requestTimeout != defaultWebSearchRequestTimeout {
			t.Fatalf("defaults = %s/%s", tool.chainTimeout, tool.requestTimeout)
		}
		if tool.httpClient.Timeout != defaultWebSearchRequestTimeout {
			t.Fatalf("http client timeout = %s", tool.httpClient.Timeout)
		}
	})
	t.Run("env overrides and clamps", func(t *testing.T) {
		t.Setenv(webSearchChainTimeoutEnv, "900000")
		t.Setenv(webSearchRequestTimeoutEnv, "5000")
		tool := NewWebSearchTool()
		if tool.chainTimeout != maxWebSearchChainTimeout {
			t.Fatalf("chain timeout = %s, want clamp %s", tool.chainTimeout, maxWebSearchChainTimeout)
		}
		if tool.requestTimeout != 5*time.Second {
			t.Fatalf("request timeout = %s", tool.requestTimeout)
		}
	})
	t.Run("invalid env falls back to config", func(t *testing.T) {
		t.Setenv(webSearchChainTimeoutEnv, "abc")
		cfg := &runtimecfg.RuntimeConfig{}
		cfg.WebSearch.ChainTimeoutMs = 45000
		tool := NewWebSearchToolWithConfig(cfg)
		if tool.chainTimeout != 45*time.Second {
			t.Fatalf("chain timeout = %s, want 45s from config", tool.chainTimeout)
		}
	})
}

func TestWebSearchChainBudgetExhaustion(t *testing.T) {
	tool := NewWebSearchTool()
	tool.chainTimeout = 150 * time.Millisecond
	secondCalled := false
	tool.providers = []WebSearchProvider{
		newWebSearchProvider("slow", func(ctx context.Context, query string, count int) (string, []DuckDuckGoResult, error) {
			<-ctx.Done()
			return "", nil, ctx.Err()
		}, nil),
		newWebSearchProvider("second", func(context.Context, string, int) (string, []DuckDuckGoResult, error) {
			secondCalled = true
			return "second", []DuckDuckGoResult{{Title: "T", URL: "https://e.example"}}, nil
		}, nil),
	}

	started := time.Now()
	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "budget"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("chain budget not enforced, took %s", elapsed)
	}
	if result.Success {
		t.Fatalf("expected failure when the budget exhausts before any success")
	}
	if secondCalled {
		t.Fatalf("second provider must not run after the chain budget is exhausted")
	}
	if result.Metadata["budget_ms"] != int64(150) {
		t.Fatalf("budget_ms = %v", result.Metadata["budget_ms"])
	}
	if result.Metadata["timed_out_provider"] != "slow" {
		t.Fatalf("timed_out_provider = %v", result.Metadata["timed_out_provider"])
	}
	if result.Metadata["budget_exhausted"] != true {
		t.Fatalf("budget_exhausted missing: %#v", result.Metadata)
	}
	if got := result.Metadata["error_code"]; got != string(runtimeerrors.ErrNetworkTimeout) {
		t.Fatalf("error_code = %v, want NETWORK_TIMEOUT", got)
	}
}

func TestWebSearchRetryAfterAndFailureProvider(t *testing.T) {
	tool := NewWebSearchTool()
	tool.providers = []WebSearchProvider{newWebSearchProvider("bing", func(context.Context, string, int) (string, []DuckDuckGoResult, error) {
		return "", nil, webSearchHTTPStatusError(http.StatusTooManyRequests,
			http.Header{"Retry-After": []string{"7"}},
			"Bing 搜索错误 (状态码 429): slow down")
	}, nil)}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "rate-limited"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected rate-limit failure")
	}
	if got := result.Metadata["error_code"]; got != string(runtimeerrors.ErrAPIRateLimit) {
		t.Fatalf("error_code = %v, want API_RATE_LIMIT", got)
	}
	if result.Metadata["failure_provider"] != "bing" {
		t.Fatalf("failure_provider = %v", result.Metadata["failure_provider"])
	}
	if result.Metadata["retry_after_ms"] != int64(7000) {
		t.Fatalf("retry_after_ms = %v", result.Metadata["retry_after_ms"])
	}
	next, _ := result.Metadata["next_action"].(string)
	if !strings.Contains(next, "7s") {
		t.Fatalf("next_action should include the Retry-After hint: %q", next)
	}
}

func TestParseRetryAfter(t *testing.T) {
	header := http.Header{}
	header.Set("Retry-After", "12")
	if got := parseRetryAfter(header); got != 12*time.Second {
		t.Fatalf("seconds form = %s", got)
	}
	header.Set("Retry-After", time.Now().Add(30*time.Second).UTC().Format(http.TimeFormat))
	if got := parseRetryAfter(header); got < 20*time.Second || got > 31*time.Second {
		t.Fatalf("http-date form = %s", got)
	}
	header.Set("Retry-After", "invalid")
	if got := parseRetryAfter(header); got != 0 {
		t.Fatalf("invalid form = %s, want 0", got)
	}
	if got := parseRetryAfter(http.Header{}); got != 0 {
		t.Fatalf("missing header = %s, want 0", got)
	}
}
