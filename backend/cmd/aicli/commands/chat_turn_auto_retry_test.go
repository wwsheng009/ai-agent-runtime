package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	llmadapter "github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
)

// newMalformedTurnError 构造 CLI 场景下 invalid_tool_arguments 的包装错误
// （runtime 层 retry_exhausted 包装后 errors.As 仍可达，见 chat_retry_command.go）。
func newMalformedTurnError() error {
	return &llmadapter.MalformedToolCallError{
		Kind:    "openai_stream_protocol_error",
		Code:    "invalid_tool_arguments",
		Message: "openai_stream_protocol_error: code=invalid_tool_arguments: tool call 0 (shell) has incomplete or non-object JSON arguments",
	}
}

// fastTurnAutoRetryDelays 跳过真实退避等待，测试只验证次数与语义。
func fastTurnAutoRetryDelays(t *testing.T) {
	t.Helper()
	previousBase, previousMax := turnAutoRetryBaseDelay, turnAutoRetryMaxDelay
	turnAutoRetryBaseDelay = 0
	turnAutoRetryMaxDelay = 0
	t.Cleanup(func() {
		turnAutoRetryBaseDelay, turnAutoRetryMaxDelay = previousBase, previousMax
	})
}

// TestMaybeAutoRetryDegenerateTurnSucceedsOnSecondAttempt 固化：无副作用的非法
// 工具参数错误在 CLI 层自动重跑一次并成功，调用方按成功处理（不再提示用户 /retry）。
func TestMaybeAutoRetryDegenerateTurnSucceedsOnSecondAttempt(t *testing.T) {
	fastTurnAutoRetryDelays(t)
	t.Setenv(turnAutoRetryLimitEnv, "2")

	session := &ChatSession{NoInteractive: true}
	executor := &fakeChatExecutor{}
	calls := 0
	executor.onCall = func(ctx context.Context, session *ChatSession, prompt string) (string, error) {
		calls++
		if calls == 1 {
			return "", newMalformedTurnError()
		}
		return "recovered output", nil
	}

	response, err, attempted := maybeAutoRetryDegenerateTurn(context.Background(), session, executor, "写文件", newMalformedTurnError())

	require.True(t, attempted)
	require.NoError(t, err)
	require.Equal(t, "recovered output", response)
	require.Equal(t, 2, calls)
	require.Equal(t, []string{"写文件", "写文件"}, executor.prompts, "每次重跑都复用同一条用户消息")
}

// TestMaybeAutoRetryDegenerateTurnStopsWhenErrorClassChanges 固化：重跑期间错误
// 类别变化（如额度/传输错误）时立即停止，不再继续重跑同一轮。
func TestMaybeAutoRetryDegenerateTurnStopsWhenErrorClassChanges(t *testing.T) {
	fastTurnAutoRetryDelays(t)
	t.Setenv(turnAutoRetryLimitEnv, "2")

	session := &ChatSession{NoInteractive: true}
	executor := &fakeChatExecutor{}
	calls := 0
	rateLimited := errors.New("provider http error: 429 too many requests")
	executor.onCall = func(ctx context.Context, session *ChatSession, prompt string) (string, error) {
		calls++
		return "", rateLimited
	}

	_, err, attempted := maybeAutoRetryDegenerateTurn(context.Background(), session, executor, "写文件", newMalformedTurnError())

	require.True(t, attempted)
	require.ErrorIs(t, err, rateLimited)
	require.Equal(t, 1, calls, "非退化错误不再触发后续重跑")
}

// TestMaybeAutoRetryDegenerateTurnRespectsLimit 固化：上限用尽后返回最后一次
// 退化错误，交给 sendMessage 的既有错误路径（渲染 + /retry 建议）。
func TestMaybeAutoRetryDegenerateTurnRespectsLimit(t *testing.T) {
	fastTurnAutoRetryDelays(t)
	t.Setenv(turnAutoRetryLimitEnv, "1")

	session := &ChatSession{NoInteractive: true}
	executor := &fakeChatExecutor{}
	calls := 0
	executor.onCall = func(ctx context.Context, session *ChatSession, prompt string) (string, error) {
		calls++
		return "", newMalformedTurnError()
	}

	_, err, attempted := maybeAutoRetryDegenerateTurn(context.Background(), session, executor, "写文件", newMalformedTurnError())

	require.True(t, attempted)
	require.Error(t, err)
	require.Equal(t, 1, calls)
}

// TestMaybeAutoRetryDegenerateTurnSkipsWhenDisabledOrInterrupted 固化前置条件：
// 上限为 0、错误类别不符、或会话已中断时不重跑。
func TestMaybeAutoRetryDegenerateTurnSkipsWhenDisabledOrInterrupted(t *testing.T) {
	fastTurnAutoRetryDelays(t)

	t.Run("limit disabled", func(t *testing.T) {
		t.Setenv(turnAutoRetryLimitEnv, "0")
		session := &ChatSession{NoInteractive: true}
		executor := &fakeChatExecutor{}
		_, err, attempted := maybeAutoRetryDegenerateTurn(context.Background(), session, executor, "写文件", newMalformedTurnError())
		require.False(t, attempted)
		require.Error(t, err)
		require.Empty(t, executor.prompts)
	})

	t.Run("other error class", func(t *testing.T) {
		t.Setenv(turnAutoRetryLimitEnv, "2")
		session := &ChatSession{NoInteractive: true}
		executor := &fakeChatExecutor{}
		rateLimited := errors.New("provider http error: 429 too many requests")
		_, _, attempted := maybeAutoRetryDegenerateTurn(context.Background(), session, executor, "写文件", rateLimited)
		require.False(t, attempted)
		require.Empty(t, executor.prompts)
	})

	t.Run("interrupted session", func(t *testing.T) {
		t.Setenv(turnAutoRetryLimitEnv, "2")
		session := &ChatSession{NoInteractive: true}
		session.interrupted.Store(true)
		executor := &fakeChatExecutor{}
		_, _, attempted := maybeAutoRetryDegenerateTurn(context.Background(), session, executor, "写文件", newMalformedTurnError())
		require.False(t, attempted)
		require.Empty(t, executor.prompts)
	})
}

// TestTurnAutoRetryDelayIsExponentialAndCapped 固化退避上界，避免自动重跑形成
// 紧密循环。
func TestTurnAutoRetryDelayIsExponentialAndCapped(t *testing.T) {
	fastTurnAutoRetryDelays(t)
	turnAutoRetryBaseDelay = 800 * time.Millisecond
	turnAutoRetryMaxDelay = 5 * time.Second

	require.Equal(t, 800*time.Millisecond, turnAutoRetryDelay(1))
	require.Equal(t, 1600*time.Millisecond, turnAutoRetryDelay(2))
	require.Equal(t, 3200*time.Millisecond, turnAutoRetryDelay(3))
	require.Equal(t, 5*time.Second, turnAutoRetryDelay(4))
	require.Equal(t, 5*time.Second, turnAutoRetryDelay(9))
	require.Equal(t, 800*time.Millisecond, turnAutoRetryDelay(0))
}
