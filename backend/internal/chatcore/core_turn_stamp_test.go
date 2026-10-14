package chatcore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ExecuteRequest.TurnID 盖章回归（2026-10-14 回合身份对齐）：user 消息带上
// 调用方回合身份后，同回合 assistant 行继承同一 turn——durable 历史与 runtime
// 事件流共用同一 turn 空间，前端按 turn 匹配实时占位与历史投影不再落空。
func TestExecuteNonStream_ReActStampsTurnIDOnUserMessage(t *testing.T) {
	executor := &fakeAgentExecutor{
		config: &agent.Config{Name: "api-agent", MaxSteps: 4},
		reactResult: &agent.Result{
			Success: true,
			Output:  "react done",
			Steps:   1,
		},
	}
	baseSession := chat.NewSession("user-1")

	result, err := ExecuteNonStream(context.Background(), ExecuteRequest{
		Agent:         executor,
		LLMRuntime:    llm.NewLLMRuntime(nil),
		Session:       baseSession,
		SessionUserID: "user-1",
		Prompt:        "hello there",
		EnableReAct:   true,
		TurnID:        "turn_client_abc",
		ReActConfig: &agent.LoopReActConfig{
			MaxSteps:        2,
			EnableThought:   true,
			EnableToolCalls: false,
		},
	})
	require.NoError(t, err)

	messages := result.UpdatedSession.GetMessages()
	require.NotEmpty(t, messages)
	// 首条 user 消息必须保留调用方的 turn 身份（不被 EnsureMessageIdentity 覆写）。
	require.Equal(t, "user", messages[0].Role)
	require.Equal(t, "turn_client_abc", types.TurnID(messages[0]))
	// 循环追加的 assistant 行继承同一 turn（fake executor 内 ReplaceHistory 追加）。
	if len(messages) > 1 {
		require.Equal(t, "turn_client_abc", types.TurnID(messages[len(messages)-1]))
	}
}

// 空 TurnID 保持旧行为：EnsureMessageIdentity 自造 turn_<uuid>，不落空串。
func TestExecuteNonStream_ReActWithoutTurnIDKeepsLegacyMinting(t *testing.T) {
	executor := &fakeAgentExecutor{
		config: &agent.Config{Name: "api-agent", MaxSteps: 4},
		reactResult: &agent.Result{
			Success: true,
			Output:  "react done",
			Steps:   1,
		},
	}
	baseSession := chat.NewSession("user-1")

	result, err := ExecuteNonStream(context.Background(), ExecuteRequest{
		Agent:         executor,
		LLMRuntime:    llm.NewLLMRuntime(nil),
		Session:       baseSession,
		SessionUserID: "user-1",
		Prompt:        "hello there",
		EnableReAct:   true,
		ReActConfig: &agent.LoopReActConfig{
			MaxSteps:        2,
			EnableThought:   true,
			EnableToolCalls: false,
		},
	})
	require.NoError(t, err)

	messages := result.UpdatedSession.GetMessages()
	require.NotEmpty(t, messages)
	turnID := types.TurnID(messages[0])
	require.True(t, len(turnID) > len("turn_"), "legacy minting must produce a non-empty turn id")
	require.True(t, turnID != "turn_client_abc")
}
