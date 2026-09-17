package events

import "strings"

// SessionStoreTypeAlias 归一化"总线事件 → 会话事件库"落盘时的类型名。
//
// 目前只有两个别名：`tool.requested` 落盘为 `tool_started`、`tool.completed`
// 落盘为 `tool_finished`（历史约定，见 internal/chat/events.go 的
// EventToolStarted/EventToolFinished）。前端 trajectory/reducer 与 chat SSE
// 回放依赖这两个落盘名（frontend/src/types/runtime/event-contract.test.ts 有
// 显式断言），因此别名映射必须保持唯一实现、且只在这里维护：
// api/skills 的 A 通道桥与 aicli 本地 A 通道桥都调用本函数。
//
// 本文件不 import internal/chat（chat 依赖 internal/events，反向 import 会成环），
// 故使用字面量并在此注释指向常量真源。
func SessionStoreTypeAlias(eventType string) string {
	switch strings.TrimSpace(eventType) {
	case "tool.requested":
		return "tool_started"
	case "tool.completed":
		return "tool_finished"
	}
	return eventType
}
