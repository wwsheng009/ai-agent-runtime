package main

import (
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/push"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
)

// 通知策略内核已抽取到 internal/notify（docs/bots/06-target-architecture.md §2.2、
// docs/bots/04-migration-roadmap.md P-1/N2）。本文件保留网关侧的薄适配层：
// 函数名与类型别名保持不变，push.go / main.go / 既有测试无需改动，行为逐字保持。
//
// 后续阶段（P1）通道抽象落地后，pushNotifier 会退化为 notify.Dispatcher 的一个
// 宿主适配器；届时这些薄包装会被直接调用替代。

// quietRange 与内核共享同一类型（别名），pushNotifier 的字段类型保持不变。
type quietRange = notify.QuietRange

// isHighPriorityAttention 报告该注意力是否应绕过合并与免打扰。
func isHighPriorityAttention(attention string) bool {
	return notify.IsHighPriority(attention)
}

// parseQuietHours 解析 --notify-quiet-hours（"HH:MM-HH:MM" 逗号分隔）。
func parseQuietHours(spec string) ([]quietRange, error) {
	return notify.ParseQuietHours(spec)
}

// inQuietHours 报告 t 是否落在任一免打扰窗口内。
func inQuietHours(ranges []quietRange, t time.Time) bool {
	return notify.InQuietHours(ranges, t)
}

// coalesceNotifications 合并同类低优先通知（"N sessions completed"）。
func coalesceNotifications(attention string, items []push.Notification) push.Notification {
	return notify.Coalesce(attention, items)
}
