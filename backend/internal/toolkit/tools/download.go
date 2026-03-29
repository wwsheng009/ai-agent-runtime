package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// DownloadTool 文件下载工具
type DownloadTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	httpClient *http.Client
	maxSize    int64
	maxRetry   int
}

// NewDownloadTool 创建下载工具
func NewDownloadTool() *DownloadTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "要下载的文件 URL",
			},
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "保存到本地的文件路径（绝对路径或相对路径）",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "请求超时时间（秒），最大 600",
				"default":     60,
			},
		},
		"required": []string{"url", "file_path"},
	}

	return &DownloadTool{
		BaseTool: toolkit.NewBaseTool(
			"download",
			"从 URL 下载文件到本地，支持大文件流式下载，自动创建父目录",
			"1.0.0",
			parameters,
			true,
		),
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		maxSize:  100 * 1024 * 1024, // 100MB
		maxRetry: 3,
	}
}

// Execute 实现 Tool 接口
func (d *DownloadTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	// 解析参数
	url, ok := params["url"].(string)
	if !ok || url == "" {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("url 参数缺失或为空"),
		}, nil
	}

	filePath, ok := params["file_path"].(string)
	if !ok || filePath == "" {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("file_path 参数缺失或为空"),
		}, nil
	}

	// 解析超时时间
	timeout := 60
	if t, ok := params["timeout"].(float64); ok && t > 0 {
		timeout = int(t)
		if timeout > 600 {
			timeout = 600
		}
	}

	// 验证 URL
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("URL 必须以 http:// 或 https:// 开头"),
		}, nil
	}
	if err := d.checkURL(url); err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   err,
		}, nil
	}
	resolvedPath := d.resolvePath(filePath)

	// 转换为绝对路径
	absPath, err := filepath.Abs(resolvedPath)
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("无法解析文件路径: %w", err),
		}, nil
	}
	if err := d.checkPath(runtimeexecutor.OpWrite, absPath); err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   err,
		}, nil
	}

	// 创建父目录
	parentDir := filepath.Dir(absPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("无法创建父目录: %w", err),
		}, nil
	}

	// 带重试的下载
	var written int64
	var lastErr error
	for attempt := 0; attempt <= d.maxRetry; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return &toolkit.ToolResult{
					Success: false,
					Error:   ctx.Err(),
				}, nil
			case <-time.After(time.Second * time.Duration(attempt)):
				// 重试延迟
			}
		}

		written, lastErr = d.doDownload(ctx, url, absPath, timeout)
		if lastErr == nil {
			break
		}

		// 如果不是可重试错误，直接退出
		if !d.isRetryableError(lastErr) {
			break
		}
	}

	if lastErr != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("下载失败（尝试 %d 次）: %w", d.maxRetry+1, lastErr),
		}, nil
	}

	return &toolkit.ToolResult{
		Success: true,
		Content: fmt.Sprintf("下载成功: %s (%d 字节)", absPath, written),
		Metadata: map[string]interface{}{
			"url":           url,
			"file_path":     absPath,
			"size":          written,
			"mutated_paths": []string{absPath},
		},
	}, nil
}

// doDownload 执行单次下载
func (d *DownloadTool) doDownload(ctx context.Context, url, absPath string, timeout int) (int64, error) {
	// 创建带超时的请求上下文
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	// 创建请求
	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return 0, fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("User-Agent", "AI-Gateway-Toolkit/1.0")

	// 发送请求
	client := &http.Client{
		Timeout: time.Duration(timeout) * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("HTTP 错误: %s", resp.Status)
	}

	// 检查文件大小
	if resp.ContentLength > d.maxSize {
		return 0, fmt.Errorf("文件太大: %d 字节（最大 %d 字节）", resp.ContentLength, d.maxSize)
	}

	// 创建目标文件
	file, err := os.Create(absPath)
	if err != nil {
		return 0, fmt.Errorf("无法创建文件: %w", err)
	}
	defer file.Close()

	// 流式下载
	written, err := io.Copy(file, resp.Body)
	if err != nil {
		os.Remove(absPath) // 清理失败的文件
		return written, err
	}

	return written, nil
}

// isRetryableError 检查是否可重试错误
func (d *DownloadTool) isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	retryablePatterns := []string{
		"timeout",
		"connection refused",
		"connection reset",
		"temporary failure",
		"too many requests",
		"service unavailable",
		"bad gateway",
		"gateway timeout",
	}

	errLower := strings.ToLower(errStr)
	for _, pattern := range retryablePatterns {
		if strings.Contains(errLower, pattern) {
			return true
		}
	}

	return false
}
