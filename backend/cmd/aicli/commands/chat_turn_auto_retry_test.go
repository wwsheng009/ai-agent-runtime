package commands

import (
	"context"
	"errors"
	"fmt"
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

// TestTurnAutoRetryReasonCoversDegenerateReplies 固化错误类别判据：empty_reply 与
// reasoning_only_reply 都属于「本轮没有任何用户可见输出」的退化类别（后者只有思维
// 链：正文为空、tool_calls=0，思维链不计入 emittedAnything，重跑不会重复展示半截
// 输出），因此纳入自动重跑；truncated_tool_call 仍排除（可能已渲染半截工具调用
// 标记，重跑会重复展示）。分类器先判 reasoning/truncated，再判 empty_reply。
func TestTurnAutoRetryReasonCoversDegenerateReplies(t *testing.T) {
	emptyReply := errors.New("empty_reply: stream ended without substantive output")
	reasoningOnly := errors.New("reasoning_only_empty_reply: stream ended with reasoning only and no substantive output: finish_reason=stop")

	require.Equal(t, "invalid_tool_arguments", turnAutoRetryReason(newMalformedTurnError()))
	require.Equal(t, "empty_reply", turnAutoRetryReason(emptyReply))
	require.Equal(t, "empty_reply", turnAutoRetryReason(fmt.Errorf("LLM call failed after retries: %w", emptyReply)))
	require.Equal(t, "reasoning_only_reply", turnAutoRetryReason(reasoningOnly))
	require.Equal(t, "reasoning_only_reply", turnAutoRetryReason(fmt.Errorf("LLM call failed after retries: %w", reasoningOnly)))

	require.Empty(t, turnAutoRetryReason(errors.New("truncated_tool_call: incomplete tool call markup in aggregated assistant response")))
	require.Empty(t, turnAutoRetryReason(errors.New("provider http error: 429 too many requests")))
	require.Empty(t, turnAutoRetryReason(nil))
}

// TestChatTurnToolExecutionsCountsAndResets 固化计数的 turn 语义：只增不减，
// 仅在新 turn 开始时清零（自动重跑不经过 reset 入口）。
func TestChatTurnToolExecutionsCountsAndResets(t *testing.T) {
	var nilSession *ChatSession
	require.Equal(t, 0, chatTurnToolExecutions(nilSession))
	resetChatTurnToolExecutions(nilSession)
	recordChatTurnToolExecution(nilSession)

	session := &ChatSession{}
	require.Equal(t, 0, chatTurnToolExecutions(session))
	recordChatTurnToolExecution(session)
	recordChatTurnToolExecution(session)
	require.Equal(t, 2, chatTurnToolExecutions(session))
	resetChatTurnToolExecutions(session)
	require.Equal(t, 0, chatTurnToolExecutions(session))
}

// TestMaybeAutoRetryDegenerateTurnSkipsWhenToolAlreadyExecuted 固化新增的前置
// 条件：本轮已真正执行过工具时，整轮重放会重复副作用，必须放弃自动重跑。
func TestMaybeAutoRetryDegenerateTurnSkipsWhenToolAlreadyExecuted(t *testing.T) {
	fastTurnAutoRetryDelays(t)
	t.Setenv(turnAutoRetryLimitEnv, "2")

	session := &ChatSession{NoInteractive: true}
	recordChatTurnToolExecution(session)
	executor := &fakeChatExecutor{}

	_, err, attempted := maybeAutoRetryDegenerateTurn(context.Background(), session, executor, "跑构建", newMalformedTurnError())

	require.False(t, attempted)
	require.Error(t, err)
	require.Empty(t, executor.prompts, "已执行过工具的轮次不允许整轮重放")
}

// TestMaybeAutoRetryDegenerateTurnStopsAfterAttemptExecutedTool 固化重跑期间的
// 工具执行：第 1 次重跑已经碰过工具，就不再安排下一次重放。
func TestMaybeAutoRetryDegenerateTurnStopsAfterAttemptExecutedTool(t *testing.T) {
	fastTurnAutoRetryDelays(t)
	t.Setenv(turnAutoRetryLimitEnv, "2")

	emptyReply := errors.New("empty_reply: stream ended without substantive output")
	session := &ChatSession{NoInteractive: true}
	executor := &fakeChatExecutor{}
	calls := 0
	executor.onCall = func(ctx context.Context, session *ChatSession, prompt string) (string, error) {
		calls++
		recordChatTurnToolExecution(session)
		return "", emptyReply
	}

	_, err, attempted := maybeAutoRetryDegenerateTurn(context.Background(), session, executor, "跑构建", emptyReply)

	require.True(t, attempted)
	require.Error(t, err)
	require.Equal(t, 1, calls, "重跑期间执行过工具后必须停止继续重放")
}

// TestMaybeAutoRetryDegenerateTurnRecoversEmptyReply 固化 empty_reply 的恢复：
// 空回复（未渲染内容、未执行工具）自动重跑一次后成功，用户不必手动重发。
func TestMaybeAutoRetryDegenerateTurnRecoversEmptyReply(t *testing.T) {
	fastTurnAutoRetryDelays(t)
	t.Setenv(turnAutoRetryLimitEnv, "2")

	emptyReply := errors.New("empty_reply: stream ended without substantive output")
	session := &ChatSession{NoInteractive: true}
	executor := &fakeChatExecutor{}
	calls := 0
	executor.onCall = func(ctx context.Context, session *ChatSession, prompt string) (string, error) {
		calls++
		if calls == 1 {
			return "", emptyReply
		}
		return "recovered output", nil
	}

	response, err, attempted := maybeAutoRetryDegenerateTurn(context.Background(), session, executor, "继续处理", emptyReply)

	require.True(t, attempted)
	require.NoError(t, err)
	require.Equal(t, "recovered output", response)
	require.Equal(t, 2, calls)
}
