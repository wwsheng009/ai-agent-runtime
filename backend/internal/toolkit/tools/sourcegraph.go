package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/toolkit"
)

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
				"description": "搜索查询，使用 Sourcegraph 语法。例如: 'func main' 搜索函数, 'file:.go' 限制 Go 文件, 'repo:org/repo' 限定仓库",
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
			"使用 Sourcegraph 搜索公共代码仓库。支持: file:.go 限定文件类型, repo:org/repo 限定仓库, type:symbol 搜索符号定义",
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
			Success: false,
			Error:   fmt.Errorf("query 参数缺失或为空"),
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
			Success: false,
			Error:   fmt.Errorf("序列化请求失败: %w", err),
		}, nil
	}
	if err := s.checkURL(s.baseURL); err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   err,
		}, nil
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.baseURL, bytes.NewReader(jsonBody))
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("创建请求失败: %w", err),
		}, nil
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "AI-Gateway-Toolkit/1.0")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("请求失败: %w", err),
		}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("读取响应失败: %w", err),
		}, nil
	}

	if resp.StatusCode != 200 {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("API 错误 (状态码 %d): %s", resp.StatusCode, string(body)),
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
			Success: false,
			Error:   fmt.Errorf("解析响应失败: %w", err),
		}, nil
	}

	if len(result.Errors) > 0 {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("API 错误: %s", result.Errors[0].Message),
		}, nil
	}

	// 构建结果
	var output strings.Builder
	output.WriteString(fmt.Sprintf("搜索结果 (共约 %s 个匹配):\n\n", result.Data.Search.Results.ApproximateResultCount))

	for i, fileMatch := range result.Data.Search.Results.Results {
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

	return &toolkit.ToolResult{
		Success: true,
		Content: output.String(),
		Metadata: map[string]interface{}{
			"query":             query,
			"count":             len(result.Data.Search.Results.Results),
			"approximate_total": result.Data.Search.Results.ApproximateResultCount,
			"limit_hit":         result.Data.Search.Results.LimitHit,
		},
	}, nil
}

// buildSearchURL 构建搜索 URL（备用方法，使用 REST API）
func (s *SourcegraphTool) buildSearchURL(query string) string {
	u, _ := url.Parse("https://sourcegraph.com/.api/search")
	q := u.Query()
	q.Set("q", query)
	u.RawQuery = q.Encode()
	return u.String()
}
