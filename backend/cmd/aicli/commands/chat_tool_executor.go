package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type aicliToolExecutor struct {
	session       *ChatSession
	scopeProvider func() aicliLogScope
}

func (e *aicliToolExecutor) ExecuteTool(ctx context.Context, call runtimetypes.ToolCall) runtimechatcore.ToolResult {
	result := runtimechatcore.ToolResult{}
	if e == nil || e.session == nil {
		result.Error = "chat session is not configured"
		result.Content = result.Error
		return result
	}
	session := e.session
	scope := aicliLogScope{}
	if e.scopeProvider != nil {
		scope = e.scopeProvider()
	}

	if truncatedErr := toolArgsTruncatedError(call.Args); truncatedErr != "" {
		result.Error = truncatedErr
		result.Content = fmt.Sprintf("执行失败: %s", truncatedErr)
		if session.Logger != nil {
			session.Logger.LogToolCall(scope, call.ID, call.Name, call.Args)
			session.Logger.LogToolResult(scope, call.ID, call.Name, toolExecutionLogPayload(result.Content, nil), fmt.Errorf("%s", truncatedErr))
		}
		writeSessionDebugInfo(session, formatToolExecutionStartDebug(toolCallFromRuntime(call)), true)
		writeSessionDebugInfo(session, formatToolExecutionResultDebug(toolCallFromRuntime(call), "", fmt.Errorf("%s", truncatedErr), nil), true)
		return result
	}

	if session.Logger != nil {
		session.Logger.LogToolCall(scope, call.ID, call.Name, call.Args)
	}
	writeSessionDebugInfo(session, formatToolExecutionStartDebug(toolCallFromRuntime(call)), true)

	catalog := ensureFunctionCatalog(session)
	output, meta, err := catalog.ExecuteFunctionWithMeta(ctx, call.Name, call.Args)
	if err != nil {
		result.Error = err.Error()
		result.Content = fmt.Sprintf("执行失败: %v", err)
		result.Metadata = cloneFunctionSchema(meta)
		if session.Logger != nil {
			session.Logger.LogToolResult(scope, call.ID, call.Name, toolExecutionLogPayload(result.Content, meta), err)
		}
		writeSessionDebugInfo(session, formatToolExecutionResultDebug(toolCallFromRuntime(call), "", err, meta), true)
		return result
	}

	result.Content = output
	result.Metadata = cloneFunctionSchema(meta)
	if session.Logger != nil {
		session.Logger.LogToolResult(scope, call.ID, call.Name, toolExecutionLogPayload(output, meta), nil)
	}
	writeSessionDebugInfo(session, formatToolExecutionResultDebug(toolCallFromRuntime(call), output, nil, meta), true)
	return result
}

func toolArgsTruncatedError(args map[string]interface{}) string {
	if len(args) == 0 {
		return ""
	}
	parseErrValue, exists := args["_parse_error"]
	if !exists {
		return ""
	}
	parseErr := strings.TrimSpace(fmt.Sprint(parseErrValue))
	if parseErr == "" || parseErr == "<nil>" {
		return ""
	}
	return fmt.Sprintf("工具调用参数不完整或已被截断，请拆分后重试: %s", parseErr)
}

func toolCallFromRuntime(call runtimetypes.ToolCall) functions.ToolCall {
	return functions.ToolCall{
		ID:       call.ID,
		Function: call.Name,
		Args:     call.Args,
	}
}
