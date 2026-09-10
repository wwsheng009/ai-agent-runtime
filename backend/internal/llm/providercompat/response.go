package providercompat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
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
// chunk before the protocol adapter accumulates it. Configured response
// markers are stripped from delta content and reasoning first.
func (c Chain) NormalizeStreamChunk(chunk map[string]interface{}) map[string]interface{} {
	chunk = stripStreamChunkMarkers(c.ctx.ResponseMarkers, chunk)
	for _, adapter := range c.adapters {
		if normalized, ok := adapter.NormalizeStreamChunk(c.ctx, chunk); ok {
			chunk = normalized
		}
	}
	return chunk
}

// stripStreamChunkMarkers removes configured literal markers from the delta
// content, reasoning_content and reasoning fields of every choice in an
// OpenAI-style streaming chunk. The chunk is returned unchanged when markers
// is empty or nothing matched. In-place mutation is safe: the chunk was
// freshly decoded from a single SSE data line and is not shared.
func stripStreamChunkMarkers(markers []string, chunk map[string]interface{}) map[string]interface{} {
	if len(markers) == 0 || len(chunk) == 0 {
		return chunk
	}
	choices, ok := chunk["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return chunk
	}
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]interface{})
		if !ok {
			continue
		}
		delta, ok := choice["delta"].(map[string]interface{})
		if !ok {
			continue
		}
		for _, key := range []string{"content", "reasoning_content", "reasoning"} {
			value, ok := delta[key].(string)
			if !ok {
				continue
			}
			if cleaned := agentconfig.StripMarkers(value, markers); cleaned != value {
				delta[key] = cleaned
			}
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
	return NormalizeStreamReadCloser(ctx, reader)
}

// NormalizeStreamReadCloser is the lifecycle-aware form of
// NormalizeStreamReader. Closing the returned reader unblocks a normalizer
// goroutine that is waiting to write into its pipe and closes the source when
// the source exposes an io.Closer.
func NormalizeStreamReadCloser(ctx Context, reader io.Reader) io.ReadCloser {
	if reader == nil {
		return nil
	}
	chain := NewChain(ctx)
	if len(chain.adapters) == 0 {
		return &passthroughReadCloser{Reader: reader, source: closerFor(reader)}
	}

	pipeReader, pipeWriter := io.Pipe()
	result := &normalizedStreamReadCloser{PipeReader: pipeReader, source: closerFor(reader)}
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
	return result
}

type passthroughReadCloser struct {
	io.Reader
	source io.Closer
	once   sync.Once
	err    error
}

func (r *passthroughReadCloser) Close() error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		if r.source != nil {
			r.err = r.source.Close()
		}
	})
	return r.err
}

type normalizedStreamReadCloser struct {
	*io.PipeReader
	source io.Closer
	once   sync.Once
	err    error
}

func (r *normalizedStreamReadCloser) Close() error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		pipeErr := r.PipeReader.Close()
		if r.source != nil {
			r.err = r.source.Close()
		}
		if r.err == nil {
			r.err = pipeErr
		}
	})
	return r.err
}

func closerFor(reader io.Reader) io.Closer {
	closer, _ := reader.(io.Closer)
	return closer
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
