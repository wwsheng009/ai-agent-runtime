package chat

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
)

// A4（2026-09-27）：被真实取消（用户中断/显式停止/父被取消）或失败的 run，其终态
// session_end 载荷此前只有 status/error，子代理已经产出的工作成果随执行一起消失
// （实测三次：828k / 946k / 880k input tokens，durable result 只有 16 runes）。
// 这里在终态载荷上补一份**有界**的部分产物，供父代理/巡检直接消费，而不是去
// token 级事件流里大海捞针。
//
// 取源优先级：
//  1. 会话历史里最后一条非空 assistant 消息 —— agent 型子代理几乎总在"发出工具
//     调用之后"被取消，此时该轮的 assistant 分析/计划/产出消息已在历史中；这是
//     真正的产物。
//  2. result.Output —— 仅作兜底（首次 LLM 调用就被取消、历史里还没有内容时，流式
//     产生的部分回答在这里）。注意取消时 result.Output 常常是宿主生成的罐头停止
//     提示（"当前运行已停止；已保留 N 条工具观察…"），因此它不能优先于历史产物。
//
// partial_steps 统计历史中已完成的工具结果条数，让父代理判断"工作推进到了哪一步"。
const partialProductSummaryRunes = 2000

// interruptedRunPartialProductWait 是中断路径为"补一份产物"愿意等待的上限。
// 中断（ESC/停摆）必须立即生效：加载超过该预算就放弃补产物。200ms 是按感知延迟
// 定的硬上限——本机 SQLite 快照读取通常 <20ms，只有存储被大写入占住时才会触顶。
const interruptedRunPartialProductWait = 200 * time.Millisecond

// enrichSessionInterruptedPayload 尽力为中断型终态（用户中断/停摆超时）补一份
// 有界产物。取源优先级：
//  1. run 自己维护的产物快照（notePartialProduct）——运行期会话尚未权威落库，
//     存储快照在中断时可能只有用户 prompt（真机实测），因此它是主取源；
//  2. 最近一次持久化快照——仅当**没有活动 run**（会话已挂起/已终态时收到 ESC）时
//     兜底；有活动 run 时以 run 快照为准（其权威性高于可能滞后的存储快照）。
//
// 两条取源都 fail-open，绝不阻塞中断路径；仅确有产物时新增 partial_* 键。
func (a *SessionActor) enrichSessionInterruptedPayload(payload map[string]interface{}, run *sessionRunControl) {
	if payload == nil {
		return
	}
	summary, source, steps := "", "", 0
	if product := run.partialProductSnapshot(); product != nil {
		summary, source, steps = product.summary, product.source, product.steps
	} else if run == nil && a != nil {
		summary, source, steps = a.loadStoredPartialProduct()
	}
	if strings.TrimSpace(summary) == "" {
		return
	}
	payload["partial_summary"] = summary
	payload["partial_source"] = source
	payload["partial_steps"] = steps
}

// loadStoredPartialProduct 是中断路径的兜底取源：只读加载最近一次持久化快照，
// 带硬性时间上限（interruptedRunPartialProductWait），失败/超时返回空。
func (a *SessionActor) loadStoredPartialProduct() (summary, source string, steps int) {
	if a == nil || a.sessionStore == nil {
		return "", "", 0
	}
	type partialProduct struct {
		summary string
		source  string
		steps   int
	}
	done := make(chan partialProduct, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), interruptedRunPartialProductWait)
		defer cancel()
		session, err := a.sessionStore.Load(ctx, a.id)
		if err != nil || session == nil {
			done <- partialProduct{}
			return
		}
		loadedSummary, loadedSource, loadedSteps := partialRunProduct(nil, session)
		done <- partialProduct{summary: loadedSummary, source: loadedSource, steps: loadedSteps}
	}()
	select {
	case product := <-done:
		return product.summary, product.source, product.steps
	case <-time.After(interruptedRunPartialProductWait):
		// 加载比中断本身还慢：放弃兜底产物，中断路径优先。
		return "", "", 0
	}
}

// partialRunProduct never fails and never mutates the session; it returns
// source="" when there is nothing to salvage (steps is still reported).
func partialRunProduct(result *agent.Result, session *Session) (summary string, source string, steps int) {
	steps = sessionToolResultCount(session)
	if content := lastAssistantMessage(session); content != "" {
		return clipPartialProduct(content), "last_assistant_message", steps
	}
	if result != nil {
		if content := strings.TrimSpace(result.Output); content != "" {
			return clipPartialProduct(content), "result_output", steps
		}
	}
	return "", "", steps
}

// pendingBatchRecoveryPayload 构造 resume 型终态（恢复未完成工具批）的载荷。
// 这条路径同样会以取消/失败收场（context canceled / deadline exceeded / 恢复
// 被取代），因此与常规终态一致地带上已产出的部分产物：键名与取值规则和
// session_end 主路径完全相同。steps 保持既有语义（恢复路径恒为 0），仅在确有
// 产物时新增 partial_* 键，不改动原有载荷形状。
func pendingBatchRecoveryPayload(turnID string, execErr error, status SessionStatus, session *Session) map[string]interface{} {
	payload := map[string]interface{}{
		"turn_id":  strings.TrimSpace(turnID),
		"resume":   true,
		"success":  false,
		"steps":    0,
		"error":    errorString(execErr),
		"duration": int64(0),
		"status":   status,
	}
	// 该路径没有本轮的 agent.Result（恢复在工具批中途中断），产物只能来自历史；
	// partialRunProduct 对 nil result 已做兜底。
	if summary, source, partialSteps := partialRunProduct(nil, session); summary != "" {
		payload["partial_summary"] = summary
		payload["partial_source"] = source
		payload["partial_steps"] = partialSteps
	}
	return payload
}

func sessionToolResultCount(session *Session) int {
	if session == nil {
		return 0
	}
	count := 0
	for _, message := range session.visibleHistory() {
		if message.Role == "tool" {
			count++
		}
	}
	return count
}

func lastAssistantMessage(session *Session) string {
	if session == nil {
		return ""
	}
	history := session.visibleHistory()
	for index := len(history) - 1; index >= 0; index-- {
		message := history[index]
		if message.Role != "assistant" {
			continue
		}
		if content := strings.TrimSpace(message.Content); content != "" {
			return content
		}
	}
	return ""
}

// clipPartialProduct bounds the salvage to partialProductSummaryRunes runes. The
// cap is deliberately larger than the 512-rune snapshot summary: A4 exists to
// carry real work product, not a one-line teaser, while staying far below any
// history budget.
func clipPartialProduct(content string) string {
	runes := []rune(content)
	if len(runes) <= partialProductSummaryRunes {
		return content
	}
	return strings.TrimSpace(string(runes[:partialProductSummaryRunes])) + "…"
}
