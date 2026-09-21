package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"
	llm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	llmadapter "github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
)

const (
	// defaultTurnAutoRetryLimit 限制同一轮用户消息在「无副作用退化失败」后的自动
	// 重跑次数（不含首次执行）。取 2 与 agent loop 的「工具 schema 回注」预算一致：
	// 模型偶尔产出非法工具参数时，用户不必再手动 /retry 或重发。
	defaultTurnAutoRetryLimit = 2

	// turnAutoRetryLimitEnv 允许用户/运维调整自动重跑次数（0 表示关闭）。
	turnAutoRetryLimitEnv = "AICLI_TURN_AUTO_RETRY_LIMIT"
)

// 退避参数做成变量以便测试覆盖（生产值：800ms 起、指数放大、封顶 5s），
// 避免连续轰炸同一个正在退化的 provider。
var (
	turnAutoRetryBaseDelay = 800 * time.Millisecond
	turnAutoRetryMaxDelay  = 5 * time.Second
)

// turnAutoRetryReason 判定本轮错误是否属于「无副作用、可自动重跑」的退化采样，
// 返回非空 reason 表示允许自动重跑。三类：
//
//   - invalid_tool_arguments（*MalformedToolCallError）：模型返回的工具参数不是
//     JSON 对象，该次调用从未执行。
//   - reasoning_only_reply：模型只输出了思维链（reasoning），正文为空、工具调用
//     为 0。reasoning 不计入 streamEmissionState.emittedAnything（只统计正文与
//     图片），所以没有渲染过任何实质内容、也没有可执行的调用；agent loop 的
//     反馈回注耗尽后，turn 级有界重跑是最后的恢复通道（重放会重新展示思维链
//     投影，代价可接受且有界）。
//   - empty_reply：聚合校验层在确认「无 content、无 tool_calls、无 reasoning」后
//     才丢弃响应（reasoning_only_empty_reply 与 truncated_tool_call 都在它之前
//     分流），所以既没有渲染过任何内容、也没有可执行的调用；它又不在输出预算
//     升级集合里（isOutputBudgetEscalationReason），重采样没有可用的杠杆，
//     turn 级有界重跑是唯一的恢复通道。
//
// 其它退化类别（truncated_tool_call、stream_interrupted）可能已经渲染过部分
// 流式输出，仍交给内层重采样与 /retry 处理，避免自动重跑重复展示半截输出。
//
// 注意：本判据只回答「这类错误是否无副作用」，是否真的可以重放还要看
// shouldAutoRetryTurnError 里的工具执行计数——整轮重放会重放已执行过的工具。
func turnAutoRetryReason(err error) string {
	var malformed *llmadapter.MalformedToolCallError
	if errors.As(err, &malformed) {
		return "invalid_tool_arguments"
	}
	if llm.IsReasoningOnlyReplyError(err) {
		return "reasoning_only_reply"
	}
	if llm.IsEmptyReplyError(err) {
		return "empty_reply"
	}
	return ""
}

// turnAutoRetryLimit 返回本次运行的自动重跑上限；环境变量显式配置优先。
func turnAutoRetryLimit() int {
	raw := strings.TrimSpace(os.Getenv(turnAutoRetryLimitEnv))
	if raw == "" {
		return defaultTurnAutoRetryLimit
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 {
		return defaultTurnAutoRetryLimit
	}
	return parsed
}

// shouldAutoRetryTurnError 汇总自动重跑的前置条件：无副作用的退化错误、未被用户
// 中断、且确实配置了重跑预算。executor 由调用方单独校验。
func shouldAutoRetryTurnError(session *ChatSession, err error) bool {
	if session == nil || err == nil {
		return false
	}
	if turnAutoRetryReason(err) == "" {
		return false
	}
	if session.IsInterrupted() {
		return false
	}
	// 本轮已经真正执行过工具：重放整条用户消息会重复执行这些调用，与 /retry
	// 「可能已部分执行工具时不自动执行」的既有规则一致，这里必须放弃自动重跑，
	// 交回错误路径让用户检查后再手动恢复。
	if chatTurnToolExecutions(session) > 0 {
		return false
	}
	return turnAutoRetryLimit() > 0
}

// maybeAutoRetryDegenerateTurn 在 sendMessage 的错误分支里做有界自动重跑。
// 返回 attempted=true 表示已经重跑过：此时 response/err 是最后一次尝试的结果，
// err==nil 表示重跑成功，本轮按成功处理（不再走错误渲染与 /retry 建议）。
// 每次尝试都复用 executor.Execute 的完整链路（含内层重采样与 schema 回注），
// 但重跑次数有界、退避可被 Esc/Ctrl+C 取消，总 provider 调用次数仍然收敛。
func maybeAutoRetryDegenerateTurn(ctx context.Context, session *ChatSession, executor aicliChatExecutor, userMessage string, turnErr error) (string, error, bool) {
	if executor == nil || !shouldAutoRetryTurnError(session, turnErr) {
		return "", turnErr, false
	}
	// A canceled turn context (user interrupt, external cancel, deadline) must
	// never enter the retry loop: the abort guards below are flag-based and a
	// remote cancel may not have set the session flag (plan doc P3-9).
	aborted := func() string {
		if session.IsInterrupted() {
			return "interrupted"
		}
		if ctx != nil && ctx.Err() != nil {
			return "context_done"
		}
		return ""
	}
	if abortReason := aborted(); abortReason != "" {
		writeSessionDebugInfo(session, fmt.Sprintf("[turn] auto retry skipped reason=%s", abortReason), false)
		return "", turnErr, false
	}
	limit := turnAutoRetryLimit()
	reason := turnAutoRetryReason(turnErr)
	lastResponse := ""
	lastErr := turnErr
	for attempt := 1; attempt <= limit; attempt++ {
		if abortReason := aborted(); abortReason != "" {
			writeSessionDebugInfo(session, fmt.Sprintf("[turn] auto retry aborted reason=%s attempt=%d", abortReason, attempt), false)
			return lastResponse, lastErr, true
		}
		// 上一次尝试可能已经执行过工具（重跑期间错误类别仍属退化）：再重放只会
		// 重复副作用，立即停在这里，保留最后一条错误交回既有错误路径。
		if chatTurnToolExecutions(session) > 0 {
			writeSessionDebugInfo(session, fmt.Sprintf("[turn] auto retry aborted reason=tool_executed attempt=%d", attempt), false)
			return lastResponse, lastErr, true
		}
		delay := turnAutoRetryDelay(attempt)
		writeSessionDebugInfo(session, fmt.Sprintf("[turn] auto retry scheduled attempt=%d limit=%d reason=%s backoff=%s", attempt, limit, reason, delay), false)
		renderTurnAutoRetryNotice(session, reason, attempt, limit, delay)
		if waitErr := waitTurnAutoRetryDelay(ctx, session, delay); waitErr != nil {
			writeSessionDebugInfo(session, fmt.Sprintf("[turn] auto retry aborted reason=%s attempt=%d", waitErr.Error(), attempt), false)
			return lastResponse, lastErr, true
		}
		if abortReason := aborted(); abortReason != "" {
			writeSessionDebugInfo(session, fmt.Sprintf("[turn] auto retry aborted reason=%s attempt=%d", abortReason, attempt), false)
			return lastResponse, lastErr, true
		}
		attemptCtx, cancel := turnAutoRetryAttemptContext(ctx, session)
		response, err := executor.Execute(attemptCtx, session, userMessage)
		cancel()
		if err == nil {
			writeSessionDebugInfo(session, fmt.Sprintf("[turn] auto retry succeeded attempt=%d reason=%s", attempt, reason), false)
			return response, nil, true
		}
		lastResponse, lastErr = response, err
		if abortReason := aborted(); abortReason != "" {
			writeSessionDebugInfo(session, fmt.Sprintf("[turn] auto retry aborted reason=%s attempt=%d", abortReason, attempt), false)
			return lastResponse, lastErr, true
		}
		// 错误类别已经变化（例如额度、网络、协议错误）：继续重跑同一轮没有依据，
		// 交回 sendMessage 的原有错误路径处理。
		if turnAutoRetryReason(err) == "" {
			writeSessionDebugInfo(session, fmt.Sprintf("[turn] auto retry stopped attempt=%d error=%q", attempt, err.Error()), false)
			return lastResponse, err, true
		}
	}
	writeSessionDebugInfo(session, fmt.Sprintf("[turn] auto retry exhausted limit=%d reason=%s last_error=%q", limit, reason, lastErr.Error()), false)
	renderTurnAutoRetryExhausted(session, limit)
	return lastResponse, lastErr, true
}

// turnAutoRetryDelay 指数退避并封顶，保证自动重跑不会形成紧密循环。
func turnAutoRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := turnAutoRetryBaseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= turnAutoRetryMaxDelay {
			return turnAutoRetryMaxDelay
		}
	}
	if delay > turnAutoRetryMaxDelay {
		return turnAutoRetryMaxDelay
	}
	return delay
}

// waitTurnAutoRetryDelay 可取消等待：Esc/Ctrl+C 中断（cancelCtx 取消）或 turn
// ctx 结束时立即返回，不做伪等待。
func waitTurnAutoRetryDelay(ctx context.Context, session *ChatSession, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	var turnDone <-chan struct{}
	if ctx != nil {
		turnDone = ctx.Done()
	}
	var sessionDone <-chan struct{}
	if session != nil && session.cancelCtx != nil {
		sessionDone = session.cancelCtx.Done()
	}
	select {
	case <-timer.C:
		return nil
	case <-turnDone:
		return context.Canceled
	case <-sessionDone:
		return context.Canceled
	}
}

// turnAutoRetryAttemptContext 为每次自动重跑重建请求超时预算，语义与首次执行
// 一致（同一个 turn deadline 配置），但不复用已经取消/耗尽的旧 ctx。
func turnAutoRetryAttemptContext(ctx context.Context, session *ChatSession) (context.Context, context.CancelFunc) {
	base := context.Background()
	if session != nil && session.cancelCtx != nil && session.cancelCtx.Err() == nil {
		base = session.cancelCtx
	} else if ctx != nil && ctx.Err() == nil {
		base = ctx
	}
	if session != nil && session.RequestTimeout > 0 {
		return runtimeexecution.WithTimeoutSource(base, session.RequestTimeout, runtimeexecution.TimeoutSourceChatTurnDeadline)
	}
	return base, func() {}
}

func renderTurnAutoRetryNotice(session *ChatSession, reason string, attempt, limit int, delay time.Duration) {
	detail := "模型本轮工具参数非法（工具未执行，无副作用）"
	if reason == "reasoning_only_reply" {
		detail = "模型本轮只输出了思维链（正文为空、未执行工具）"
	} else if reason == "empty_reply" {
		detail = "模型本轮回复为空（未渲染内容、未执行工具）"
	}
	message := fmt.Sprintf("[turn] %s，自动重跑 %d/%d（退避 %.1fs；Esc 可取消）", detail, attempt, limit, delay.Seconds())
	renderTurnAutoRetryMessage(session, message)
}

func renderTurnAutoRetryExhausted(session *ChatSession, limit int) {
	message := fmt.Sprintf("[turn] 自动重跑已达上限(%d)，仍未能完成本轮；可按提示 /retry 恢复上一条消息", limit)
	renderTurnAutoRetryMessage(session, message)
}

func renderTurnAutoRetryMessage(session *ChatSession, message string) {
	writeSessionDebugInfo(session, message, false)
	if !shouldRenderInteractiveOutput(session) {
		return
	}
	if session.Interaction != nil {
		session.Interaction.RenderLocalSupplement(message)
		return
	}
	printDirectInteractiveOutput(session, message+"\n")
}
