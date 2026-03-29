package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// FetchTool HTTP 内容获取工具
type FetchTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	maxSize int64
}

// NewFetchTool 创建 Fetch 工具
func NewFetchTool() *FetchTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "要获取内容的 URL",
			},
			"format": map[string]interface{}{
				"type":        "string",
				"description": "返回内容的格式: text（纯文本）、markdown（Markdown 格式）、html（原始 HTML）",
				"enum":        []string{"text", "markdown", "html"},
				"default":     "text",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "请求超时时间（秒），最大 120",
				"default":     30,
			},
		},
		"required": []string{"url"},
	}

	return &FetchTool{
		BaseTool: toolkit.NewBaseTool(
			"fetch",
			"获取 URL 内容，返回原始文本、HTML 或 Markdown 格式",
			"1.0.0",
			parameters,
			true,
		),
		maxSize: 5 * 1024 * 1024, // 5MB
	}
}

// Execute 实现 Tool 接口
func (f *FetchTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	// 解析 URL
	url, ok := params["url"].(string)
	if !ok || url == "" {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("url 参数缺失或为空"),
		}, nil
	}

	// 验证 URL
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("URL 必须以 http:// 或 https:// 开头"),
		}, nil
	}
	if err := f.checkURL(url); err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   err,
		}, nil
	}

	// 解析格式参数
	format := "text"
	if fmt, ok := params["format"].(string); ok && fmt != "" {
		format = fmt
	}
	if format != "text" && format != "markdown" && format != "html" {
		format = "text"
	}

	// 解析超时时间
	timeout := 30
	if t, ok := params["timeout"].(float64); ok && t > 0 {
		timeout = int(t)
		if timeout > 120 {
			timeout = 120
		}
	}

	// 创建带超时的请求上下文
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	// 创建请求
	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("创建请求失败: %w", err),
		}, nil
	}

	req.Header.Set("User-Agent", "AI-Gateway-Toolkit/1.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml,text/plain,*/*")

	// 发送请求
	client := &http.Client{
		Timeout: time.Duration(timeout) * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("请求失败: %w", err),
		}, nil
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("HTTP 错误: %s", resp.Status),
		}, nil
	}

	// 检查内容大小
	if resp.ContentLength > f.maxSize {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("内容太大: %d 字节（最大 %d 字节）", resp.ContentLength, f.maxSize),
		}, nil
	}

	// 读取内容（限制大小）
	limitedReader := io.LimitReader(resp.Body, f.maxSize+1)
	content, err := io.ReadAll(limitedReader)
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("读取内容失败: %w", err),
		}, nil
	}

	if int64(len(content)) > f.maxSize {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("内容超过最大限制 %d 字节", f.maxSize),
		}, nil
	}

	// 根据格式处理内容
	result := string(content)
	contentType := resp.Header.Get("Content-Type")

	switch format {
	case "html":
		// 返回原始 HTML
		// 不做处理
	case "markdown":
		// HTML 转 Markdown（简化实现）
		result = f.htmlToMarkdown(result, contentType)
	case "text":
		// 提取纯文本
		result = f.extractText(result, contentType)
	}

	return &toolkit.ToolResult{
		Success: true,
		Content: result,
		Metadata: map[string]interface{}{
			"url":          url,
			"format":       format,
			"size":         len(content),
			"status":       resp.Status,
			"content_type": contentType,
		},
	}, nil
}

// extractText 提取纯文本内容
func (f *FetchTool) extractText(content string, contentType string) string {
	// 如果是 JSON，直接返回
	if strings.Contains(contentType, "application/json") {
		return content
	}

	// 如果是纯文本，直接返回
	if strings.Contains(contentType, "text/plain") {
		return content
	}

	// 简单的 HTML 标签移除
	text := content
	// 移除 script 和 style 标签及其内容
	text = removeTagContent(text, "script")
	text = removeTagContent(text, "style")
	text = removeTagContent(text, "head")

	// 移除 HTML 标签
	text = stripHTMLTags(text)

	// 清理多余空白
	text = strings.TrimSpace(text)
	text = collapseWhitespace(text)

	return text
}

// htmlToMarkdown 简化的 HTML 转 Markdown
func (f *FetchTool) htmlToMarkdown(content string, contentType string) string {
	// 如果不是 HTML，直接返回
	if !strings.Contains(contentType, "text/html") {
		return content
	}

	md := content

	// 移除 script、style、head
	md = removeTagContent(md, "script")
	md = removeTagContent(md, "style")
	md = removeTagContent(md, "head")

	// 简单的标签转换
	// 标题
	md = convertTag(md, "h1", "# ", true)
	md = convertTag(md, "h2", "## ", true)
	md = convertTag(md, "h3", "### ", true)
	md = convertTag(md, "h4", "#### ", true)
	md = convertTag(md, "h5", "##### ", true)
	md = convertTag(md, "h6", "###### ", true)

	// 粗体和斜体
	md = convertTag(md, "strong", "**", false)
	md = convertTag(md, "b", "**", false)
	md = convertTag(md, "em", "*", false)
	md = convertTag(md, "i", "*", false)

	// 链接
	md = convertLinks(md)

	// 代码
	md = convertTag(md, "code", "`", false)

	// 列表
	md = convertListItems(md, "ul", "- ")
	md = convertListItems(md, "ol", "1. ")

	// 段落和换行
	md = convertParagraphs(md)

	// 移除剩余 HTML 标签
	md = stripHTMLTags(md)

	// 清理
	md = collapseWhitespace(md)

	return strings.TrimSpace(md)
}

// removeTagContent 移除标签及其内容
func removeTagContent(content string, tagName string) string {
	// 简单实现：移除 <tag>...</tag>
	openTag := "<" + tagName
	closeTag := "</" + tagName + ">"

	for {
		start := strings.Index(content, openTag)
		if start == -1 {
			break
		}
		end := strings.Index(content, closeTag)
		if end == -1 || end <= start {
			break
		}
		content = content[:start] + content[end+len(closeTag):]
	}

	return content
}

// stripHTMLTags 移除所有 HTML 标签
func stripHTMLTags(content string) string {
	result := make([]byte, 0, len(content))
	inTag := false
	for i := 0; i < len(content); i++ {
		if content[i] == '<' {
			inTag = true
			continue
		}
		if content[i] == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result = append(result, content[i])
		}
	}
	return string(result)
}

// convertTag 转换 HTML 标签为 Markdown 格式
func convertTag(content string, tagName string, prefix string, isBlock bool) string {
	openTag := "<" + tagName + ">"
	openTagClose := "<" + tagName + " "
	closeTag := "</" + tagName + ">"

	result := content

	// 处理带属性的标签
	for {
		idx := strings.Index(result, openTagClose)
		if idx == -1 {
			break
		}
		end := strings.Index(result[idx:], ">")
		if end == -1 {
			break
		}
		result = result[:idx] + result[idx+end+1:]
	}

	// 处理简单标签
	if isBlock {
		// 块级元素：在前面加前缀
		result = strings.ReplaceAll(result, openTag, prefix)
	} else {
		// 行内元素：两边加标记
		result = strings.ReplaceAll(result, openTag, prefix)
	}
	result = strings.ReplaceAll(result, closeTag, "")

	return result
}

// convertLinks 转换链接标签
func convertLinks(content string) string {
	// 简化实现：只提取链接文本
	result := content
	for {
		start := strings.Index(result, "<a ")
		if start == -1 {
			break
		}
		end := strings.Index(result[start:], ">")
		if end == -1 {
			break
		}
		end += start
		closeIdx := strings.Index(result[end:], "</a>")
		if closeIdx == -1 {
			break
		}
		closeIdx += end

		// 提取 href 和文本
		tagAttrs := result[start+3 : end]
		linkText := result[end+1 : closeIdx]

		// 提取 href
		hrefStart := strings.Index(tagAttrs, "href=\"")
		if hrefStart == -1 {
			hrefStart = strings.Index(tagAttrs, "href='")
		}
		var href string
		if hrefStart != -1 {
			hrefStart += 6
			hrefEnd := strings.Index(tagAttrs[hrefStart:], "\"")
			if hrefEnd == -1 {
				hrefEnd = strings.Index(tagAttrs[hrefStart:], "'")
			}
			if hrefEnd != -1 {
				href = tagAttrs[hrefStart : hrefStart+hrefEnd]
			}
		}

		if href != "" {
			result = result[:start] + fmt.Sprintf("[%s](%s)", linkText, href) + result[closeIdx+4:]
		} else {
			result = result[:start] + linkText + result[closeIdx+4:]
		}
	}
	return result
}

// convertListItems 转换列表项
func convertListItems(content string, listTag string, prefix string) string {
	result := content
	openTag := "<" + listTag + ">"
	closeTag := "</" + listTag + ">"

	// 移除列表标签
	result = strings.ReplaceAll(result, openTag, "")
	result = strings.ReplaceAll(result, closeTag, "")

	// 转换列表项
	itemOpen := "<li>"
	itemClose := "</li>"
	result = strings.ReplaceAll(result, itemOpen, prefix)
	result = strings.ReplaceAll(result, itemClose, "\n")

	return result
}

// convertParagraphs 转换段落
func convertParagraphs(content string) string {
	result := content
	result = strings.ReplaceAll(result, "<p>", "\n")
	result = strings.ReplaceAll(result, "</p>", "\n")
	result = strings.ReplaceAll(result, "<br>", "\n")
	result = strings.ReplaceAll(result, "<br/>", "\n")
	result = strings.ReplaceAll(result, "<br />", "\n")
	return result
}

// collapseWhitespace 压缩空白字符
func collapseWhitespace(content string) string {
	result := make([]byte, 0, len(content))
	prevSpace := false
	for i := 0; i < len(content); i++ {
		c := content[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if !prevSpace {
				result = append(result, ' ')
				prevSpace = true
			}
		} else {
			result = append(result, c)
			prevSpace = false
		}
	}
	return string(result)
}
