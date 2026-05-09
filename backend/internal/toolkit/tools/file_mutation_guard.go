package tools

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

const (
	maxInlineFileMutationFieldBytes   = 64 * 1024
	maxInlineFileMutationPayloadBytes = 128 * 1024
)

type inlineMutationSegment struct {
	Name  string
	Value string
}

func truncatedToolArgsResult(params map[string]interface{}) (*toolkit.ToolResult, bool) {
	if len(params) == 0 {
		return nil, false
	}
	parseErrValue, exists := params["_parse_error"]
	if !exists {
		return nil, false
	}
	parseErr := strings.TrimSpace(fmt.Sprint(parseErrValue))
	if parseErr == "" || parseErr == "<nil>" {
		return nil, false
	}
	return &toolkit.ToolResult{
		Success:    false,
		OutputKind: toolresult.KindText,
		Error:      fmt.Errorf("工具调用参数不完整或已被截断，请拆分后重试: %s", parseErr),
	}, true
}

func validateInlineFileMutationPayload(toolName string, segments ...inlineMutationSegment) error {
	totalBytes := 0
	for _, segment := range segments {
		if segment.Name == "" {
			continue
		}
		size := len([]byte(segment.Value))
		totalBytes += size
		if size > maxInlineFileMutationFieldBytes {
			return fmt.Errorf(
				"%s 工具文本参数过大：%s 为 %d 字节，超过单段限制 %d 字节。请拆分为多个更小的调用，每次只处理一个较小写入块或一个局部替换目标",
				toolName,
				segment.Name,
				size,
				maxInlineFileMutationFieldBytes,
			)
		}
	}
	if totalBytes > maxInlineFileMutationPayloadBytes {
		return fmt.Errorf(
			"%s 工具文本参数总大小过大：%d 字节，超过总限制 %d 字节。请拆分为多个更小的调用，每次只聚焦一个文件和一个较小编辑块",
			toolName,
			totalBytes,
			maxInlineFileMutationPayloadBytes,
		)
	}
	return nil
}
