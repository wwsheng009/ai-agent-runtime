package providercompat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	llmadapter "github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const assistantReasoningDetailsKey = "reasoning_details"

// NormalizeProcessResult applies provider-specific fixes to a protocol-parsed
// process result.
func (c Chain) NormalizeProcessResult(result *llmadapter.ProcessResult) *llmadapter.ProcessResult {
	if result == nil {
		return nil
	}
	for _, adapter := range c.adapters {
		adapter.NormalizeProcessResult(c.ctx, result)
	}
	return result
}

// NormalizeStreamChunk applies provider-specific fixes to one streaming JSON
// chunk before the protocol adapter accumulates it.
func (c Chain) NormalizeStreamChunk(chunk map[string]interface{}) map[string]interface{} {
	for _, adapter := range c.adapters {
		if normalized, ok := adapter.NormalizeStreamChunk(c.ctx, chunk); ok {
			chunk = normalized
		}
	}
	return chunk
}

// NormalizeProcessResult applies provider-specific process result fixes.
func NormalizeProcessResult(ctx Context, result *llmadapter.ProcessResult) *llmadapter.ProcessResult {
	return NewChain(ctx).NormalizeProcessResult(result)
}

// NormalizeStreamChunk applies provider-specific streaming chunk fixes.
func NormalizeStreamChunk(ctx Context, chunk map[string]interface{}) map[string]interface{} {
	return NewChain(ctx).NormalizeStreamChunk(chunk)
}

// NormalizeStreamReader wraps an SSE stream and normalizes each data JSON chunk
// as it is read. Non-SSE lines and non-JSON data payloads pass through.
func NormalizeStreamReader(ctx Context, reader io.Reader) io.Reader {
	if reader == nil {
		return nil
	}
	chain := NewChain(ctx)
	if len(chain.adapters) == 0 {
		return reader
	}

	pipeReader, pipeWriter := io.Pipe()
	go func() {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 1024*1024), 20*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if data, ok := strings.CutPrefix(line, "data:"); ok {
				data = strings.TrimSpace(data)
				if data != "" && data != "[DONE]" {
					var chunk map[string]interface{}
					if err := json.Unmarshal([]byte(data), &chunk); err == nil {
						normalized := chain.NormalizeStreamChunk(chunk)
						if payload, err := json.Marshal(normalized); err == nil {
							line = "data: " + string(payload)
						}
					}
				}
			}
			if _, err := fmt.Fprintln(pipeWriter, line); err != nil {
				_ = pipeWriter.CloseWithError(err)
				return
			}
		}
		if err := scanner.Err(); err != nil {
			_ = pipeWriter.CloseWithError(err)
			return
		}
		_ = pipeWriter.Close()
	}()
	return pipeReader
}

func processResultFromAssistantMessage(message map[string]interface{}) (*llmadapter.ProcessResult, bool) {
	if len(message) == 0 {
		return nil, false
	}
	result := &llmadapter.ProcessResult{}
	if content, ok := message["content"].(string); ok {
		result.Content = content
	}
	if reasoning, ok := message["reasoning_content"].(string); ok {
		result.Reasoning = reasoning
		result.ReasoningPresent = true
	} else if reasoning, ok := message["reasoning"].(string); ok {
		result.Reasoning = reasoning
		result.ReasoningPresent = true
	}
	if block := types.ReasoningBlockFromMap(message[assistantReasoningDetailsKey]); block != nil {
		result.ReasoningBlock = block
	}
	if toolCalls := decodeSliceOfMaps(message["tool_calls"]); len(toolCalls) > 0 {
		result.ToolCalls = toolCalls
		result.HasToolCalls = true
	}
	return result, true
}

func assistantMessageFromProcessResult(message map[string]interface{}, result *llmadapter.ProcessResult) map[string]interface{} {
	if result == nil {
		return message
	}
	normalized := cloneMapStringAny(message)
	normalized["content"] = result.Content
	if result.HasToolCalls || len(result.ToolCalls) > 0 {
		normalized["tool_calls"] = result.ToolCalls
	}
	if result.ReasoningPresent {
		normalized["reasoning_content"] = result.Reasoning
	}
	if result.ReasoningBlock != nil {
		normalized[assistantReasoningDetailsKey] = result.ReasoningBlock.ToMap()
	}
	return normalized
}

func decodeSliceOfMaps(value interface{}) []map[string]interface{} {
	switch typed := value.(type) {
	case []map[string]interface{}:
		return typed
	case []interface{}:
		result := make([]map[string]interface{}, 0, len(typed))
		for _, item := range typed {
			if mapped, ok := item.(map[string]interface{}); ok {
				result = append(result, mapped)
			}
		}
		return result
	default:
		return nil
	}
}
