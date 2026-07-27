package tools

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestWebSearchTool_EmptySearchMarksEmptySuccess(t *testing.T) {
	tool := NewWebSearchTool()
	tool.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			host := req.URL.Host
			path := req.URL.Path
			switch {
			case strings.Contains(host, "api.duckduckgo.com"):
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"AbstractText":"","Heading":"","RelatedTopics":[]}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			case strings.Contains(host, "html.duckduckgo.com") || strings.Contains(path, "/html"):
				// Empty HTML payload: zero hits is success, not transport failure.
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`<html><body></body></html>`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			default:
				t.Fatalf("unexpected request host/path: %s %s", host, path)
				return nil, nil
			}
		}),
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "unlikely_token_xyz_no_hits",
		"count": float64(3),
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

func TestWebSearchTool_EmptyInstantKeepsEmptyWhenHTMLFails(t *testing.T) {
	tool := NewWebSearchTool()
	tool.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Host, "api.duckduckgo.com") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"AbstractText":"","Heading":"","RelatedTopics":[]}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       io.NopCloser(strings.NewReader("upstream unavailable")),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}),
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "unlikely_token_xyz_no_hits",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected empty success from Instant Answer, got error %v", result.Error)
	}
	if result.Metadata["source"] != "duckduckgo" {
		t.Fatalf("expected source=duckduckgo empty success, got %#v", result.Metadata)
	}
	if result.Metadata[toolresult.MetadataOutcomeKey] != toolresult.OutcomeEmpty {
		t.Fatalf("expected outcome=empty, got %#v", result.Metadata)
	}
}

func TestWebSearchTool_DescriptionGuidesQuerySplitting(t *testing.T) {
	tool := NewWebSearchTool()

	desc := tool.Description()
	if !strings.Contains(desc, "拆分") || !strings.Contains(desc, "每次只聚焦一个搜索目标") {
		t.Fatalf("expected web_search description to guide query splitting, got %q", desc)
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

func TestWebSearchTool_NetworkFailureIsStructured(t *testing.T) {
	// Live residual: connectex / dial failures were bare TOOL_EXECUTION with
	// generic next_action. Models then spam-retried the same query.
	tool := NewWebSearchTool()
	tool.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, &url.Error{
				Op:  "Get",
				URL: req.URL.String(),
				Err: &net.OpError{
					Op:  "dial",
					Net: "tcp",
					Err: errors.New("connectex: A connection attempt failed because the connected party did not properly respond after a period of time"),
				},
			}
		}),
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "Anthropic Claude models family 2026",
		"count": float64(5),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success || result.Error == nil {
		t.Fatalf("expected network failure, got success=%v err=%v", result.Success, result.Error)
	}
	if !strings.Contains(result.Error.Error(), "搜索失败") {
		t.Fatalf("expected 搜索失败 wrapper, got %v", result.Error)
	}
	code, _ := result.Metadata[toolresult.MetadataErrorCodeKey].(string)
	if code != string(runtimeerrors.ErrNetworkUnavailable) {
		t.Fatalf("error_code=%q want NETWORK_UNAVAILABLE meta=%#v", code, result.Metadata)
	}
	next, _ := result.Metadata[toolresult.MetadataNextActionKey].(string)
	if !strings.Contains(next, "NETWORK_UNAVAILABLE") || !strings.Contains(next, "backoff") {
		t.Fatalf("expected network next_action, got %q", next)
	}
	if !strings.Contains(next, "Anthropic Claude") {
		t.Fatalf("expected query in next_action, got %q", next)
	}
	if result.Metadata["failure_class"] != "network" {
		t.Fatalf("failure_class=%#v", result.Metadata["failure_class"])
	}
	// Diagnose should treat as retryable network, not opaque TOOL_EXECUTION.
	diag := toolresult.Diagnose("web_search", "call-net", result.Error.Error(), result.Metadata)
	if diag.ErrorCode != string(runtimeerrors.ErrNetworkUnavailable) {
		t.Fatalf("diagnose code=%q want NETWORK_UNAVAILABLE", diag.ErrorCode)
	}
	if !diag.Retryable {
		t.Fatalf("network failure should be retryable: %#v", diag)
	}
}

func TestWebSearchTool_HTTPServerErrorIsStructured(t *testing.T) {
	tool := NewWebSearchTool()
	tool.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			// Instant fails with connection error; HTML returns 503 body via error path.
			// Force both paths to fail with a 503 status error string by returning
			// a transport error that mentions service unavailable after status.
			if strings.Contains(req.URL.Host, "api.duckduckgo.com") {
				return nil, errors.New("Get api: HTTP 503 service unavailable")
			}
			return nil, errors.New("Get html: HTTP 503 service unavailable")
		}),
	}
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "test query",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure, got success meta=%#v", result.Metadata)
	}
	code, _ := result.Metadata[toolresult.MetadataErrorCodeKey].(string)
	if code != string(runtimeerrors.ErrAPIServerError) {
		t.Fatalf("error_code=%q want API_SERVER_ERROR meta=%#v", code, result.Metadata)
	}
}

func TestClassifyWebSearchFailureCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "connectex",
			err:  errors.New(`Get "https://html.duckduckgo.com/html/?q=x": dial tcp 1.2.3.4:443: connectex: A connection attempt failed`),
			want: string(runtimeerrors.ErrNetworkUnavailable),
		},
		{
			name: "timeout",
			err:  errors.New("context deadline exceeded"),
			want: string(runtimeerrors.ErrNetworkTimeout),
		},
		{
			name: "typed url dial",
			err: &url.Error{
				Op:  "Get",
				URL: "https://example.com",
				Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("no such host")},
			},
			want: string(runtimeerrors.ErrNetworkUnavailable),
		},
		{
			name: "rate limit",
			err:  errors.New("HTTP 429 rate limit exceeded"),
			want: string(runtimeerrors.ErrAPIRateLimit),
		},
		{
			name: "unknown",
			err:  errors.New("unexpected parse failure"),
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyWebSearchFailureCode(tc.err)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
