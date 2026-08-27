package tools

import (
	"context"
	"encoding/base64"
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
			name: "chinese status 503",
			err:  errors.New("Bing 搜索错误 (状态码 503): upstream unavailable"),
			want: string(runtimeerrors.ErrAPIServerError),
		},
		{
			name: "chinese status 429",
			err:  errors.New("HTML 搜索错误 (状态码 429): too many"),
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

func TestParseWebSearchProviders(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "whitespace", in: "   ", want: ""},
		{name: "single bing", in: "bing", want: "bing"},
		{name: "reorder", in: "bing,duckduckgo", want: "bing,duckduckgo"},
		{name: "case and dedupe", in: "BING, bing, unknown, duckduckgo,", want: "bing,duckduckgo"},
		{name: "all unknown", in: "google,yahoo", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseWebSearchProviders(tc.in)
			if strings.Join(got, ",") != tc.want {
				t.Fatalf("parse(%q) = %v, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestWebSearchTool_BingParsesResultsAndDecodesRedirect(t *testing.T) {
	tool := NewWebSearchTool()
	tool.providers = []webSearchProvider{{name: "bing", search: tool.searchBingProvider}}
	tool.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "cn.bing.com" {
				t.Fatalf("expected cn.bing.com request, got %s", req.URL.Host)
			}
			body := `<ol id="b_results">` +
				`<li class="b_algo"><h2><a href="https://example.com/page" h="ID=SERP,1.1"><strong>示例</strong>标题</a></h2>` +
				`<div class="b_caption"><p>这是第一条摘要&amp;更多。</p></div></li>` +
				`<li class="b_algo"><h2><a href="https://cn.bing.com/ck/a?a=b&amp;u=a1a` + bingUParam("https://example.org/") + `&amp;ntb=1">跳转链接标题</a></h2>` +
				`<p>第二条摘要</p></li>` +
				`<li class="b_algo"><h2><a href="https://plain.example/">第三&amp;四</a></h2><p>三</p></li>` +
				`</ol>`
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}),
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "golang",
		"count": float64(5),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected bing success, got error %v", result.Error)
	}
	if result.Metadata["source"] != "bing" {
		t.Fatalf("expected source=bing, got %#v", result.Metadata["source"])
	}
	if result.Metadata["match_count"] != 3 {
		t.Fatalf("expected 3 results, got %#v", result.Metadata)
	}
	for _, want := range []string{"示例标题", "跳转链接标题", "https://example.org/", "https://example.com/page", "这是第一条摘要&更多。", "第三&四"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("expected %q in content, got %q", want, result.Content)
		}
	}
	// 尾随属性、未解码实体与跳转链接不应再出现。
	for _, unwanted := range []string{"ID=SERP", "/ck/a", "&amp;", "&ensp;"} {
		if strings.Contains(result.Content, unwanted) {
			t.Fatalf("expected no %q in content, got %q", unwanted, result.Content)
		}
	}
}

func TestWebSearchTool_ProviderChainFallsBackToBing(t *testing.T) {
	// 默认链 duckduckgo,bing：DDG 网络不可达 → 快速回退 Bing。
	tool := NewWebSearchTool()
	tool.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.Contains(req.URL.Host, "duckduckgo.com"):
				return nil, &url.Error{
					Op:  "Get",
					URL: req.URL.String(),
					Err: &net.OpError{
						Op:  "dial",
						Net: "tcp",
						Err: errors.New("connectex: A connection attempt failed because the connected party did not properly respond after a period of time"),
					},
				}
			case req.URL.Host == "cn.bing.com":
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`<li class="b_algo"><h2><a href="https://bing.example/">Bing 兜底标题</a></h2><p>兜底摘要</p></li>`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			default:
				t.Fatalf("unexpected request host: %s", req.URL.Host)
				return nil, nil
			}
		}),
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "Anthropic Claude models family 2026",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected fallback success via bing, got %v", result.Error)
	}
	if result.Metadata["source"] != "bing" {
		t.Fatalf("expected source=bing, got %#v", result.Metadata["source"])
	}
	if !strings.Contains(result.Content, "Bing 兜底标题") {
		t.Fatalf("expected bing content, got %q", result.Content)
	}
}

func TestWebSearchTool_BingCNFailsFallsBackToWWW(t *testing.T) {
	tool := NewWebSearchTool()
	tool.providers = []webSearchProvider{{name: "bing", search: tool.searchBingProvider}}
	tool.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Host {
			case "cn.bing.com":
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Body:       io.NopCloser(strings.NewReader("upstream unavailable")),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			case "www.bing.com":
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`<li class="b_algo"><h2><a href="https://www.example/">WWW 结果</a></h2><p>摘要</p></li>`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			default:
				t.Fatalf("unexpected request host: %s", req.URL.Host)
				return nil, nil
			}
		}),
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "windows terminal",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected www.bing.com fallback success, got %v", result.Error)
	}
	if result.Metadata["source"] != "bing" {
		t.Fatalf("expected source=bing, got %#v", result.Metadata["source"])
	}
	if !strings.Contains(result.Content, "WWW 结果") {
		t.Fatalf("expected www bing content, got %q", result.Content)
	}
}

func TestWebSearchTool_AllProvidersFailStampsAttempts(t *testing.T) {
	// 整条链失败时，失败信息应记录所有尝试过的 provider 及各自错误，
	// 并按链顺序取第一个可分类的错误（此处 DDG connectex → network）。
	tool := NewWebSearchTool()
	tool.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Host, "duckduckgo.com") {
				return nil, &url.Error{
					Op:  "Get",
					URL: req.URL.String(),
					Err: &net.OpError{
						Op:  "dial",
						Net: "tcp",
						Err: errors.New("connectex: A connection attempt failed"),
					},
				}
			}
			return nil, &url.Error{Op: "Get", URL: req.URL.String(), Err: errors.New("dial tcp: i/o timeout")}
		}),
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "query xyz",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure, got success %#v", result.Metadata)
	}
	code, _ := result.Metadata[toolresult.MetadataErrorCodeKey].(string)
	if code != string(runtimeerrors.ErrNetworkUnavailable) {
		t.Fatalf("error_code=%q want NETWORK_UNAVAILABLE meta=%#v", code, result.Metadata)
	}
	attempted, _ := result.Metadata["providers_attempted"].([]string)
	if len(attempted) != 2 || attempted[0] != "duckduckgo" || attempted[1] != "bing" {
		t.Fatalf("expected providers_attempted=[duckduckgo bing], got %#v", result.Metadata["providers_attempted"])
	}
	perProvider, _ := result.Metadata["provider_errors"].(map[string]string)
	if perProvider["duckduckgo"] == "" || perProvider["bing"] == "" {
		t.Fatalf("expected per-provider errors, got %#v", result.Metadata["provider_errors"])
	}
}

// bingUParam 构造 Bing /ck/ 跳转链接的 u 参数（a1a + base64url，无 padding）。
func bingUParam(target string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(target))
}

func TestDecodeBingRedirect(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain link untouched",
			in:   "https://example.com/page",
			want: "https://example.com/page",
		},
		{
			name: "a1a prefixed u param",
			in:   "https://cn.bing.com/ck/a?a=b&u=a1a" + bingUParam("https://example.org/") + "&ntb=1",
			want: "https://example.org/",
		},
		{
			name: "bare u param without prefix",
			in:   "https://cn.bing.com/ck/a?u=" + bingUParam("https://example.org/"),
			want: "https://example.org/",
		},
		{
			name: "garbage u param falls back to raw",
			in:   "https://cn.bing.com/ck/a?u=%00%01%02notb64",
			want: "https://cn.bing.com/ck/a?u=%00%01%02notb64",
		},
		{
			name: "decodable but non-URL falls back to raw",
			in:   "https://cn.bing.com/ck/a?u=" + bingUParam("garbage-not-a-url"),
			want: "https://cn.bing.com/ck/a?u=" + bingUParam("garbage-not-a-url"),
		},
		{
			name: "missing u param untouched",
			in:   "https://cn.bing.com/ck/a?x=1",
			want: "https://cn.bing.com/ck/a?x=1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeBingRedirect(tc.in); got != tc.want {
				t.Fatalf("decodeBingRedirect(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNewWebSearchTool_ProvidersFromEnv(t *testing.T) {
	t.Run("default chain", func(t *testing.T) {
		tool := NewWebSearchTool()
		names := make([]string, 0, len(tool.providers))
		for _, p := range tool.providers {
			names = append(names, p.name)
		}
		if strings.Join(names, ",") != "duckduckgo,bing" {
			t.Fatalf("default providers = %v, want [duckduckgo bing]", names)
		}
	})

	t.Run("env overrides", func(t *testing.T) {
		t.Setenv(webSearchProvidersEnv, "bing")
		tool := NewWebSearchTool()
		if len(tool.providers) != 1 || tool.providers[0].name != "bing" {
			t.Fatalf("env providers = %v, want [bing]", tool.providers)
		}
	})

	t.Run("invalid env falls back to default", func(t *testing.T) {
		t.Setenv(webSearchProvidersEnv, "google")
		tool := NewWebSearchTool()
		if len(tool.providers) != 2 || tool.providers[0].name != "duckduckgo" {
			t.Fatalf("invalid env providers = %v, want default chain", tool.providers)
		}
	})
}
