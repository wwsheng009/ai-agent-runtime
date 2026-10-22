package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestPromptMessageFingerprintDistinguishesFieldBoundaries 锁定长度前缀语义：
// 没有边界标记时 {role:"ab", content:"c"} 与 {role:"a", content:"bc"} 会拼出
// 相同字节流，必须可区分。
func TestPromptMessageFingerprintDistinguishesFieldBoundaries(t *testing.T) {
	left := []types.Message{*types.NewUserMessage("c")}
	left[0].Role = "ab"
	right := []types.Message{*types.NewUserMessage("bc")}
	right[0].Role = "a"

	require.NotEqual(t, promptMessageFingerprint(left), promptMessageFingerprint(right))
}

// TestPromptMessageFingerprintDeterministicAcrossMapOrder 锁定 map 键排序：
// Go map 迭代顺序随机，同一 tool args 必须产生同一指纹。
func TestPromptMessageFingerprintDeterministicAcrossMapOrder(t *testing.T) {
	build := func(keys []string) string {
		message := types.NewAssistantMessage("")
		message.ToolCalls = []types.ToolCall{{
			ID:   "call-1",
			Type: "function",
			Name: "demo",
			Args: map[string]interface{}{},
		}}
		for _, key := range keys {
			message.ToolCalls[0].Args[key] = key
		}
		return promptMessageFingerprint([]types.Message{*message})
	}

	forward := build([]string{"alpha", "beta", "gamma", "delta", "epsilon"})
	backward := build([]string{"epsilon", "delta", "gamma", "beta", "alpha"})
	require.Equal(t, forward, backward, "同样的 args 内容必须给出同样的指纹")

	// 内容不同必须不同。
	changed := build([]string{"alpha", "beta", "gamma", "delta", "zeta"})
	require.NotEqual(t, forward, changed)
}

// TestPromptMessageFingerprintStableForIdenticalHistory 锁定重复计算稳定性：
// 同一份历史多次调用必须返回同一值（热路径会调用 4~6 次并做相等比较）。
func TestPromptMessageFingerprintStableForIdenticalHistory(t *testing.T) {
	history := fingerprintBenchHistory(50)
	first := promptMessageFingerprint(history)
	for i := 0; i < 5; i++ {
		require.Equal(t, first, promptMessageFingerprint(history))
	}
	require.NotEmpty(t, first)
	require.Empty(t, promptMessageFingerprint(nil))
}

// TestPromptMessageFingerprintTracksContentPartsAndToolCalls 锁定字段覆盖：
// 指纹必须覆盖 ContentParts 与 ToolCalls（旧实现曾因漏字段导致误判重复轮）。
func TestPromptMessageFingerprintTracksContentPartsAndToolCalls(t *testing.T) {
	base := types.NewAssistantMessage("same")
	baseParts := *base
	baseParts.ContentParts = []types.ContentPart{{Type: "text", Text: "a"}}
	changedParts := *base
	changedParts.ContentParts = []types.ContentPart{{Type: "text", Text: "b"}}

	require.NotEqual(t,
		promptMessageFingerprint([]types.Message{baseParts}),
		promptMessageFingerprint([]types.Message{changedParts}))

	baseCall := *base
	baseCall.ToolCalls = []types.ToolCall{{ID: "1", Name: "tool", RawInput: `{"a":1}`}}
	changedCall := *base
	changedCall.ToolCalls = []types.ToolCall{{ID: "1", Name: "tool", RawInput: `{"a":2}`}}
	require.NotEqual(t,
		promptMessageFingerprint([]types.Message{baseCall}),
		promptMessageFingerprint([]types.Message{changedCall}))
}

// fingerprintBenchHistory 构造一份贴近生产形状的历史：混合正文、reasoning、
// 工具调用与工具结果。
func fingerprintBenchHistory(count int) []types.Message {
	history := make([]types.Message, 0, count)
	body := make([]byte, 0, 2048)
	for len(body) < 2048 {
		body = append(body, "The quick brown fox jumps over the lazy dog. "...)
	}
	for index := 0; index < count; index++ {
		switch index % 4 {
		case 0:
			history = append(history, *types.NewUserMessage(fmt.Sprintf("user turn %d\n%s", index, body)))
		case 1:
			message := types.NewAssistantMessage(fmt.Sprintf("assistant turn %d <tag>&escape", index))
			message.Metadata["reasoning_details"] = map[string]interface{}{
				"text": fmt.Sprintf("reasoning %d: %s", index, body),
			}
			message.ToolCalls = []types.ToolCall{{
				ID:   fmt.Sprintf("call-%d", index),
				Type: "function",
				Name: "shell",
				Args: map[string]interface{}{
					"command": fmt.Sprintf("echo %d", index),
					"cwd":     `E:\projects\ai\ai-agent-runtime`,
					"nested":  map[string]interface{}{"b": 2, "a": 1},
				},
			}}
			history = append(history, *message)
		case 2:
			history = append(history, *types.NewToolMessage(fmt.Sprintf("call-%d", index-1), string(body)))
		default:
			history = append(history, *types.NewAssistantMessage(fmt.Sprintf("done %d", index)))
		}
	}
	return history
}

// promptMessageFingerprintLegacy 是修复前的实现，仅用于 A/B 基准对照。
func promptMessageFingerprintLegacy(messages []types.Message) string {
	if len(messages) == 0 {
		return ""
	}
	type fingerprintToolCall struct {
		ID       string                 `json:"id,omitempty"`
		Type     string                 `json:"type,omitempty"`
		Name     string                 `json:"name,omitempty"`
		Args     map[string]interface{} `json:"arguments,omitempty"`
		RawInput string                 `json:"input,omitempty"`
	}
	type fingerprintMessage struct {
		Role         string                `json:"role,omitempty"`
		Content      string                `json:"content,omitempty"`
		ContentParts []types.ContentPart   `json:"content_parts,omitempty"`
		ToolCalls    []fingerprintToolCall `json:"tool_calls,omitempty"`
		ToolCallID   string                `json:"tool_call_id,omitempty"`
	}
	payload := make([]fingerprintMessage, 0, len(messages))
	for _, message := range messages {
		item := fingerprintMessage{
			Role:         message.Role,
			Content:      message.Content,
			ContentParts: append([]types.ContentPart(nil), message.ContentParts...),
			ToolCallID:   message.ToolCallID,
		}
		if len(message.ToolCalls) > 0 {
			item.ToolCalls = make([]fingerprintToolCall, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				item.ToolCalls = append(item.ToolCalls, fingerprintToolCall{
					ID:       call.ID,
					Type:     call.Type,
					Name:     call.Name,
					Args:     call.Args,
					RawInput: call.RawInput,
				})
			}
		}
		payload = append(payload, item)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum[:])
}

func BenchmarkPromptMessageFingerprint(b *testing.B) {
	history := fingerprintBenchHistory(200)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = promptMessageFingerprint(history)
	}
}

func BenchmarkPromptMessageFingerprintLegacy(b *testing.B) {
	history := fingerprintBenchHistory(200)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = promptMessageFingerprintLegacy(history)
	}
}
