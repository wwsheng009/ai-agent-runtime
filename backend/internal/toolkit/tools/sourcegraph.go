package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/buildinfo"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const sourcegraphErrorBodyPreviewBytes int64 = 4 << 10

// SourcegraphTool 代码搜索工具
type SourcegraphTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	httpClient *http.Client
	baseURL    string
}

// NewSourcegraphTool 创建 Sourcegraph 搜索工具
func NewSourcegraphTool() *SourcegraphTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "搜索查询，使用 Sourcegraph 语法。例如: 'func main' 搜索函数, 'file:.go' 限制 Go 文件, 'repo:org/repo' 限定仓库。若查询条件很多，请拆分成多个更小的 query 调用，每次只聚焦一个搜索目标。",
			},
			"count": map[string]interface{}{
				"type":        "integer",
				"description": "返回结果数量（默认 10，最大 20）",
				"default":     10,
			},
			"context_window": map[string]interface{}{
				"type":        "integer",
				"description": "匹配行周围的上下文行数（默认 10）",
				"default":     10,
			},
		},
		"required": []string{"query"},
	}

	return &SourcegraphTool{
		BaseTool: toolkit.NewBaseTool(
			"sourcegraph",
			"使用 Sourcegraph 搜索公共代码仓库。支持: file:.go 限定文件类型, repo:org/repo 限定仓库, type:symbol 搜索符号定义。若查询条件很多，请拆分为多个更小的 sourcegraph 调用，每次只聚焦一个搜索目标。",
			"1.0.0",
			parameters,
			true,
		),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		baseURL: "https://sourcegraph.com/.api/graphql",
	}
}

func (s *SourcegraphTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataKindKey:             runtimetypes.ToolKindSearch,
		runtimetypes.ToolMetadataReadOnlyKey:         true,
		runtimetypes.ToolMetadataMutatesFSKey:        false,
		runtimetypes.ToolMetadataRequiresNetKey:      true,
		runtimetypes.ToolMetadataSupportsParallelKey: true,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassSafe,
	}
}

// GraphQLRequest GraphQL 请求结构
type GraphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables"`
}

// Execute 实现 Tool 接口
func (s *SourcegraphTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	// 解析查询
	query, ok := params["query"].(string)
	if !ok || query == "" {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("query 参数缺失或为空"),
		}, nil
	}

	// 解析结果数量
	count := 10
	if c, ok := params["count"].(float64); ok && c > 0 {
		count = int(c)
		if count > 20 {
			count = 20
		}
	}

	// 解析上下文窗口
	contextWindow := 10
	if cw, ok := params["context_window"].(float64); ok && cw > 0 {
		contextWindow = int(cw)
	}

	// 构建 GraphQL 查询
	graphqlQuery := `
query Search($query: String!, $first: Int!) {
	search(query: $query, version: V2, first: $first) {
		results {
			results {
				... on FileMatch {
					file {
						path
						repository {
							name
						}
					}
					lineMatches {
						preview
						lineNumber
						surroundingContent
					}
				}
			}
			limitHit
			approximateResultCount
		}
	}
}`

	vars := map[string]interface{}{
		"query": query,
		"first": count,
	}

	// 发送请求
	reqBody := GraphQLRequest{
		Query:     graphqlQuery,
		Variables: vars,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("序列化请求失败: %w", err),
		}, nil
	}
	if err := s.checkURL(s.baseURL); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.baseURL, bytes.NewReader(jsonBody))
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("创建请求失败: %w", err),
		}, nil
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", buildinfo.UserAgent())

	resp, err := s.httpClient.Do(req)
	if err != nil {
		code := string(runtimeerrors.ErrNetworkUnavailable)
		failureClass := "network"
		nextAction := "Check Sourcegraph/network availability, then retry with bounded backoff."
		if errors.Is(err, context.DeadlineExceeded) || isSourcegraphTimeout(err) {
			code = string(runtimeerrors.ErrNetworkTimeout)
			failureClass = "timeout"
			nextAction = "The Sourcegraph request timed out; narrow the query or retry after the upstream recovers."
		}
		return toolResultFailureWithCode(
			fmt.Errorf("Sourcegraph request failed: %w", err),
			code,
			nextAction,
			map[string]interface{}{
				"failure_class": failureClass,
				"retryable":     true,
			},
		), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		preview, truncated, readErr := readSourcegraphErrorPreview(resp.Body)
		if readErr != nil {
			return toolResultFailureWithCode(
				fmt.Errorf("failed to read Sourcegraph HTTP %d error response: %w", resp.StatusCode, readErr),
				string(runtimeerrors.ErrNetworkUnavailable),
				"Retry after checking Sourcegraph/network availability.",
				sourcegraphHTTPFailureMetadata(resp, "response_read", true, truncated),
			), nil
		}
		code, failureClass, retryable, nextAction := classifySourcegraphHTTPFailure(resp.StatusCode, preview)
		metadata := sourcegraphHTTPFailureMetadata(resp, failureClass, retryable, truncated)
		return toolResultFailureWithCode(
			fmt.Errorf("Sourcegraph HTTP %d: %s", resp.StatusCode, preview),
			code,
			nextAction,
			metadata,
		), nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("读取响应失败: %w", err),
		}, nil
	}

	// 解析响应
	var result struct {
		Data struct {
			Search struct {
				Results struct {
					Results []struct {
						File struct {
							Path       string `json:"path"`
							Repository struct {
								Name string `json:"name"`
							} `json:"repository"`
						} `json:"file"`
						LineMatches []struct {
							Preview            string `json:"preview"`
							LineNumber         int    `json:"lineNumber"`
							SurroundingContent string `json:"surroundingContent"`
						} `json:"lineMatches"`
					} `json:"results"`
					LimitHit               bool   `json:"limitHit"`
					ApproximateResultCount string `json:"approximateResultCount"`
				} `json:"results"`
			} `json:"search"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("解析响应失败: %w", err),
		}, nil
	}

	if len(result.Errors) > 0 {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("API 错误: %s", result.Errors[0].Message),
		}, nil
	}

	// 构建结果
	var output strings.Builder
	matches := result.Data.Search.Results.Results
	returnedCount := len(matches)
	if returnedCount == 0 {
		output.WriteString("未找到匹配的内容")
	} else {
		approx := strings.TrimSpace(result.Data.Search.Results.ApproximateResultCount)
		if approx == "" {
			approx = fmt.Sprintf("%d", returnedCount)
		}
		output.WriteString(fmt.Sprintf("搜索结果 (共约 %s 个匹配):\n\n", approx))
	}

	for i, fileMatch := range matches {
		if i >= count {
			break
		}

		repoName := fileMatch.File.Repository.Name
		filePath := fileMatch.File.Path

		output.WriteString(fmt.Sprintf("📁 %s\n", repoName))
		output.WriteString(fmt.Sprintf("   📄 %s\n", filePath))

		for _, lineMatch := range fileMatch.LineMatches {
			output.WriteString(fmt.Sprintf("   L%d: %s\n", lineMatch.LineNumber, strings.TrimSpace(lineMatch.Preview)))
			if lineMatch.SurroundingContent != "" && contextWindow > 0 {
				lines := strings.Split(lineMatch.SurroundingContent, "\n")
				if len(lines) > 1 {
					start := len(lines)/2 - contextWindow/2
					if start < 0 {
						start = 0
					}
					end := start + contextWindow
					if end > len(lines) {
						end = len(lines)
					}
					for j := start; j < end; j++ {
						if j != len(lines)/2 {
							output.WriteString(fmt.Sprintf("      %s\n", strings.TrimSpace(lines[j])))
						}
					}
				}
			}
		}
		output.WriteString("\n")
	}

	if result.Data.Search.Results.LimitHit {
		output.WriteString("⚠️ 结果已达到限制，可能还有更多匹配\n")
	}

	// Cap reported counts to the request limit so metadata matches rendered output.
	if returnedCount > count {
		returnedCount = count
	}
	metadata := map[string]interface{}{
		"query":             query,
		"count":             returnedCount,
		"match_count":       returnedCount,
		"returned_count":    returnedCount,
		"result_count":      returnedCount,
		"approximate_total": result.Data.Search.Results.ApproximateResultCount,
		"limit_hit":         result.Data.Search.Results.LimitHit,
	}
	// True no-match success: stamp empty disposition so the model broadens
	// the query instead of treating a successful empty search as a hard failure.
	if returnedCount == 0 {
		toolresult.MarkEmptySuccess(metadata)
	}

	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    output.String(),
		Metadata:   metadata,
	}, nil
}

func readSourcegraphErrorPreview(body io.Reader) (preview string, truncated bool, err error) {
	if body == nil {
		return "", false, nil
	}
	data, err := io.ReadAll(io.LimitReader(body, sourcegraphErrorBodyPreviewBytes+1))
	if err != nil {
		return "", false, err
	}
	if int64(len(data)) > sourcegraphErrorBodyPreviewBytes {
		data = data[:sourcegraphErrorBodyPreviewBytes]
		truncated = true
	}
	preview = strings.TrimSpace(string(data))
	if preview == "" {
		preview = http.StatusText(http.StatusBadGateway)
	}
	if truncated {
		preview += " … [truncated]"
	}
	return preview, truncated, nil
}

func classifySourcegraphHTTPFailure(statusCode int, preview string) (code, failureClass string, retryable bool, nextAction string) {
	switch statusCode {
	case http.StatusUnauthorized:
		return "UPSTREAM_AUTHENTICATION_FAILED", "upstream_authentication", false,
			"Verify Sourcegraph credentials before retrying."
	case http.StatusForbidden:
		failureClass = "upstream_policy"
		if strings.Contains(strings.ToLower(preview), "firewall block") {
			failureClass = "upstream_firewall_block"
		}
		return "UPSTREAM_POLICY_DENIED", failureClass, false,
			"Sourcegraph denied the request by access policy; do not retry unchanged. Use another code-search source or update the network policy."
	case http.StatusTooManyRequests:
		return "UPSTREAM_RATE_LIMITED", "upstream_rate_limit", true,
			"Respect Retry-After/reset when present, then retry once or use another code-search source."
	default:
		if statusCode >= http.StatusInternalServerError {
			return "UPSTREAM_UNAVAILABLE", "upstream_server", true,
				"Retry with bounded backoff, then use another code-search source if Sourcegraph remains unavailable."
		}
		return "UPSTREAM_HTTP_ERROR", "upstream_http", false,
			"Inspect the HTTP status and bounded response preview; correct the request before retrying."
	}
}

func sourcegraphHTTPFailureMetadata(resp *http.Response, failureClass string, retryable, truncated bool) map[string]interface{} {
	metadata := map[string]interface{}{
		"failure_class":          failureClass,
		"retryable":              retryable,
		"http_status":            resp.StatusCode,
		"content_type":           strings.TrimSpace(resp.Header.Get("Content-Type")),
		"body_preview_truncated": truncated,
	}
	if requestID := sourcegraphRequestID(resp.Header); requestID != "" {
		metadata["request_id"] = requestID
	}
	if retryAfter := strings.TrimSpace(resp.Header.Get("Retry-After")); retryAfter != "" {
		metadata["retry_after"] = retryAfter
	}
	return metadata
}

func sourcegraphRequestID(header http.Header) string {
	for _, key := range []string{"X-Request-ID", "X-Trace-ID", "Traceparent"} {
		if value := strings.TrimSpace(header.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func isSourcegraphTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// buildSearchURL 构建搜索 URL（备用方法，使用 REST API）
func (s *SourcegraphTool) buildSearchURL(query string) string {
	u, _ := url.Parse("https://sourcegraph.com/.api/search")
	q := u.Query()
	q.Set("q", query)
	u.RawQuery = q.Encode()
	return u.String()
}
