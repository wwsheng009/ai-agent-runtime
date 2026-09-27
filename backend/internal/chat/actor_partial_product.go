package chat

import (
	"strings"

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
