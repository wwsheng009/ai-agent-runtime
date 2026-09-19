package tools

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

func TestSourcegraphTool_EmptySearchMarksEmptySuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if !strings.Contains(string(body), "unlikely_token_xyz") {
			t.Fatalf("expected query payload in request, got %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"search": {
					"results": {
						"results": [],
						"limitHit": false,
						"approximateResultCount": "0"
					}
				}
			}
		}`))
	}))
	defer server.Close()

	tool := NewSourcegraphTool()
	tool.baseURL = server.URL
	tool.httpClient = server.Client()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "unlikely_token_xyz",
		"count": float64(5),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected empty-search success, got error %v", result.Error)
	}
	if result.Metadata["match_count"] != 0 || result.Metadata["returned_count"] != 0 {
		t.Fatalf("expected zero match counts, got %#v", result.Metadata)
	}
	if result.Metadata[toolresult.MetadataEmptyResultKey] != true {
		t.Fatalf("expected empty_result=true, got %#v", result.Metadata)
	}
	if result.Metadata[toolresult.MetadataOutcomeKey] != toolresult.OutcomeEmpty {
		t.Fatalf("expected outcome=empty, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "未找到") {
		t.Fatalf("expected no-match content, got %q", result.Content)
	}
}

func TestSourcegraphTool_DescriptionGuidesQuerySplitting(t *testing.T) {
	tool := NewSourcegraphTool()

	desc := tool.Description()
	if !strings.Contains(desc, "拆分") || !strings.Contains(desc, "每次只聚焦一个搜索目标") {
		t.Fatalf("expected sourcegraph description to guide query splitting, got %q", desc)
	}

	params := tool.Parameters()
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected properties in schema, got %#v", params)
	}
	querySchema, ok := props["query"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected query schema in properties, got %#v", props)
	}
	queryDesc, _ := querySchema["description"].(string)
	if !strings.Contains(queryDesc, "拆分") || !strings.Contains(queryDesc, "每次只聚焦一个搜索目标") {
		t.Fatalf("expected query description to guide query splitting, got %q", queryDesc)
	}
}

func TestSourcegraphTool_LargeFirewall403IsBoundedAndClassified(t *testing.T) {
	const tailMarker = "must-not-reach-the-model"
	largeBody := "<html><title>Sourcegraph - Firewall Block</title>" +
		strings.Repeat("x", 1<<20) + tailMarker
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Request-ID", "req-firewall-1")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, largeBody)
	}))
	defer server.Close()

	tool := NewSourcegraphTool()
	tool.baseURL = server.URL
	tool.httpClient = server.Client()

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "repo:test/example needle"})
	if err != nil {
		t.Fatalf("unexpected execute error: %v", err)
	}
	if result.Success || result.Error == nil {
		t.Fatalf("expected a failed tool result, got %#v", result)
	}
	if got := result.Error.Error(); len(got) > 6<<10 {
		t.Fatalf("inline error grew beyond the bounded preview: bytes=%d", len(got))
	} else {
		if !strings.Contains(got, "Sourcegraph HTTP 403") || !strings.Contains(got, "[truncated]") {
			t.Fatalf("expected status and truncation marker, got %q", got)
		}
		if strings.Contains(got, tailMarker) {
			t.Fatalf("large response tail leaked into the model-visible error")
		}
	}
	assertSourcegraphFailureMetadata(t, result.Metadata, "UPSTREAM_POLICY_DENIED", "upstream_firewall_block", false)
	if result.Metadata["body_preview_truncated"] != true {
		t.Fatalf("expected body_preview_truncated=true, got %#v", result.Metadata)
	}
	if result.Metadata["http_status"] != http.StatusForbidden ||
		result.Metadata["content_type"] != "text/html; charset=utf-8" ||
		result.Metadata["request_id"] != "req-firewall-1" {
		t.Fatalf("missing HTTP diagnostics: %#v", result.Metadata)
	}
}

func TestSourcegraphTool_HTTPRetryDisposition(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantCode   string
		wantClass  string
		wantRetry  bool
		retryAfter string
	}{
		{
			name:      "ordinary forbidden is terminal policy denial",
			status:    http.StatusForbidden,
			body:      "access denied by organization policy",
			wantCode:  "UPSTREAM_POLICY_DENIED",
			wantClass: "upstream_policy",
			wantRetry: false,
		},
		{
			name:       "rate limit is retryable",
			status:     http.StatusTooManyRequests,
			body:       `{"error":"rate limited"}`,
			wantCode:   "UPSTREAM_RATE_LIMITED",
			wantClass:  "upstream_rate_limit",
			wantRetry:  true,
			retryAfter: "7",
		},
		{
			name:      "server error is retryable",
			status:    http.StatusBadGateway,
			body:      "upstream unavailable",
			wantCode:  "UPSTREAM_UNAVAILABLE",
			wantClass: "upstream_server",
			wantRetry: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()

			tool := NewSourcegraphTool()
			tool.baseURL = server.URL
			tool.httpClient = server.Client()
			result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "needle"})
			if err != nil {
				t.Fatalf("unexpected execute error: %v", err)
			}
			assertSourcegraphFailureMetadata(t, result.Metadata, tc.wantCode, tc.wantClass, tc.wantRetry)
			if tc.retryAfter != "" && result.Metadata["retry_after"] != tc.retryAfter {
				t.Fatalf("retry_after=%#v want %q", result.Metadata["retry_after"], tc.retryAfter)
			}
		})
	}
}

func TestSourcegraphTool_TransportFailureClassification(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantCode  string
		wantClass string
	}{
		{
			name:      "connection failure",
			err:       errors.New("dial tcp: connection refused"),
			wantCode:  string(runtimeerrors.ErrNetworkUnavailable),
			wantClass: "network",
		},
		{
			name:      "context deadline",
			err:       context.DeadlineExceeded,
			wantCode:  string(runtimeerrors.ErrNetworkTimeout),
			wantClass: "timeout",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tool := NewSourcegraphTool()
			tool.httpClient = &http.Client{
				Timeout: time.Second,
				Transport: sourcegraphRoundTripFunc(func(*http.Request) (*http.Response, error) {
					return nil, tc.err
				}),
			}
			result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "needle"})
			if err != nil {
				t.Fatalf("unexpected execute error: %v", err)
			}
			assertSourcegraphFailureMetadata(t, result.Metadata, tc.wantCode, tc.wantClass, true)
		})
	}
}

func assertSourcegraphFailureMetadata(t *testing.T, metadata map[string]interface{}, wantCode, wantClass string, wantRetry bool) {
	t.Helper()
	if got, _ := metadata[toolresult.MetadataErrorCodeKey].(string); got != wantCode {
		t.Fatalf("error_code=%q want %q; metadata=%#v", got, wantCode, metadata)
	}
	if got, _ := metadata["failure_class"].(string); got != wantClass {
		t.Fatalf("failure_class=%q want %q; metadata=%#v", got, wantClass, metadata)
	}
	if got, _ := metadata[toolresult.MetadataRetryableKey].(bool); got != wantRetry {
		t.Fatalf("retryable=%v want %v; metadata=%#v", got, wantRetry, metadata)
	}
	if next, _ := metadata[toolresult.MetadataNextActionKey].(string); strings.TrimSpace(next) == "" {
		t.Fatalf("expected actionable next_action; metadata=%#v", metadata)
	}
}

type sourcegraphRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn sourcegraphRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
