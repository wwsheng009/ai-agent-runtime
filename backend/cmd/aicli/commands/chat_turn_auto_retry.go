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
// 返回非空 reason 表示允许自动重跑。目前只认 invalid_tool_arguments：模型返回的
// 工具参数不是 JSON 对象，工具从未执行，重跑不会重复任何工具副作用。
// 其它退化类别（reasoning_only_empty_reply、truncated_tool_call）可能已经渲染过
// 部分流式输出，仍交给内层重采样与 /retry 处理，避免自动重跑重复展示半截输出。
func turnAutoRetryReason(err error) string {
	var malformed *llmadapter.MalformedToolCallError
	if errors.As(err, &malformed) {
		return "invalid_tool_arguments"
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
	limit := turnAutoRetryLimit()
	reason := turnAutoRetryReason(turnErr)
	lastResponse := ""
	lastErr := turnErr
	for attempt := 1; attempt <= limit; attempt++ {
		if session.IsInterrupted() {
			writeSessionDebugInfo(session, fmt.Sprintf("[turn] auto retry aborted reason=interrupted attempt=%d", attempt), false)
			return lastResponse, lastErr, true
		}
		delay := turnAutoRetryDelay(attempt)
		writeSessionDebugInfo(session, fmt.Sprintf("[turn] auto retry scheduled attempt=%d limit=%d reason=%s backoff=%s", attempt, limit, reason, delay), false)
		renderTurnAutoRetryNotice(session, attempt, limit, delay)
		if waitErr := waitTurnAutoRetryDelay(ctx, session, delay); waitErr != nil {
			writeSessionDebugInfo(session, fmt.Sprintf("[turn] auto retry aborted reason=%s attempt=%d", waitErr.Error(), attempt), false)
			return lastResponse, lastErr, true
		}
		if session.IsInterrupted() {
			writeSessionDebugInfo(session, fmt.Sprintf("[turn] auto retry aborted reason=interrupted attempt=%d", attempt), false)
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
		if session.IsInterrupted() {
			writeSessionDebugInfo(session, fmt.Sprintf("[turn] auto retry aborted reason=interrupted attempt=%d", attempt), false)
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

func renderTurnAutoRetryNotice(session *ChatSession, attempt, limit int, delay time.Duration) {
	message := fmt.Sprintf("[turn] 模型本轮工具参数非法（工具未执行，无副作用），自动重跑 %d/%d（退避 %.1fs；Esc 可取消）", attempt, limit, delay.Seconds())
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
