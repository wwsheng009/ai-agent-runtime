// /undo：把 IM 会话回退一轮，并尽力撤回机器人最近一条消息。
//
// 设计（与 TUI /backtrack 对齐）：
//   - 语义 = 回退「最近一轮」：删除最后一轮用户提问及其后的助手回复；
//     `/undo <N>` 表示保留最近 N 轮（N=1 即删除最近两轮）。
//   - 顺序：**先回退历史，再撤回消息**。理由：
//     历史回退失败可重试且不产生副作用；若先撤回，历史回退失败会留下
//     「消息已消失但 agent 仍记得」的不一致。
//   - 撤回是 best-effort：历史回退成功后撤回失败只记日志并在回执里说明，
//     不影响回退本身（官方撤回窗口仅 2 分钟，超时是常态）。
//   - 宿主未实现 BacktrackBackend（当前 gateway）时明确提示，不静默。
package connectors

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// handleUndo 执行 /undo。
func (s *Service) handleUndo(ctx context.Context, cfg Config, provider Provider, msg InboundMessage, args string) error {
	backend, ok := s.deps.Backend.(BacktrackBackend)
	if !ok {
		return provider.Send(ctx, cfg, msg.Chat, []OutboundMessage{{
			Text: "当前宿主不支持会话回退（/undo）：请在 aicli 或 runtime-server 中使用。",
		}})
	}
	binding, _ := s.deps.Store.GetBinding(cfg.ID, msg.Chat)
	if binding.Session.IsZero() {
		return provider.Send(ctx, cfg, msg.Chat, []OutboundMessage{{
			Text: "当前会话尚未建立对话，没有可回退的轮次。",
		}})
	}

	turnsBefore, err := parseUndoTurns(args)
	if err != nil {
		return provider.Send(ctx, cfg, msg.Chat, []OutboundMessage{{Text: err.Error()}})
	}

	result, err := backend.Backtrack(ctx, BacktrackRequest{
		ConnectorID: cfg.ID,
		Chat:        msg.Chat,
		Session:     binding.Session,
		TurnsBefore: turnsBefore,
	})
	if err != nil {
		s.recordLog(LogEntry{
			Level:       LogLevelError,
			Direction:   LogDirectionIn,
			ConnectorID: cfg.ID,
			Chat:        msg.Chat,
			Summary:     "会话回退失败（/undo）：" + err.Error(),
		}, logCounters{})
		return provider.Send(ctx, cfg, msg.Chat, []OutboundMessage{{
			Text: "回退失败：" + err.Error(),
		}})
	}
	if !result.Handled {
		return provider.Send(ctx, cfg, msg.Chat, []OutboundMessage{{
			Text: "没有可回退的轮次（已是会话起点）。",
		}})
	}

	reply := strings.TrimSpace(result.Reply)
	if reply == "" {
		reply = fmt.Sprintf("已回退 %d 轮", maxInt(result.RemovedTurns, 1))
		if preview := strings.TrimSpace(result.AnchorPreview); preview != "" {
			reply += "（" + summarizePreview(preview) + "）"
		}
	}
	// 撤回机器人最近一条消息（best-effort，不阻断回退）。
	if recallNote := s.recallLastMessage(ctx, cfg, provider, msg.Chat); recallNote != "" {
		reply += "\n" + recallNote
	}
	return provider.Send(ctx, cfg, msg.Chat, []OutboundMessage{{Text: reply}})
}

// recallLastMessage 尝试撤回机器人最近一条消息，返回给用户的说明（可空）。
func (s *Service) recallLastMessage(ctx context.Context, cfg Config, provider Provider, chat string) string {
	retractor, ok := provider.(MessageRetractor)
	if !ok {
		return ""
	}
	messageID, ok := retractor.LastRecallableMessage(chat)
	if !ok {
		return "（最近一条回复已超过 2 分钟撤回时限，未撤回）"
	}
	if err := retractor.RetractMessage(ctx, cfg, chat, messageID); err != nil {
		s.recordLog(LogEntry{
			Level:       LogLevelWarn,
			Direction:   LogDirectionOut,
			ConnectorID: cfg.ID,
			Chat:        chat,
			Summary:     "撤回消息失败（历史已回退）：" + err.Error(),
		}, logCounters{})
		return "（历史已回退，但消息撤回失败）"
	}
	return ""
}

// parseUndoTurns 解析 /undo 的轮数参数：空=0（回退最近一轮），仅接受非负整数。
func parseUndoTurns(args string) (int, error) {
	raw := strings.TrimSpace(args)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("参数无效：/undo [轮数]，轮数必须是非负整数")
	}
	return value, nil
}

// summarizePreview 截断预览文本（回执里不放大内容）。
func summarizePreview(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > 40 {
		return string(runes[:40]) + "…"
	}
	return string(runes)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ResolveUndoTurnIndex 把「保留最近 N 轮」换算成要回退到的 turn 下标。
//
//	totalTurns = 当前 user turn 总数
//	turnsBefore = 要保留的最近轮数（0 = 回退最近一轮）
//
// 返回 -1/ok=false 表示没有可回退的轮次（例如只有一轮且 turnsBefore=0）。
//
// 示例（total=3）：turnsBefore=0 → 2（删除第 3 轮）；1 → 1；2 → 0；3 → 无。
func ResolveUndoTurnIndex(totalTurns, turnsBefore int) (int, bool) {
	if turnsBefore < 0 {
		turnsBefore = 0
	}
	index := totalTurns - 1 - turnsBefore
	if index < 0 {
		return 0, false
	}
	return index, true
}
