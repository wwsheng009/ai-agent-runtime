// Package channels 把具体通知通道（WebPush/ntfy/IM…）适配为 notify.Channel
// （docs/bots/06-target-architecture.md §3：`notify/channels/{webpush,ntfy}.go`
// 适配器、行为不变）。适配器只做协议映射，不持有策略（路由/去重由
// notify.Dispatcher 承担）。
package channels

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/push"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
)

// Broadcaster 是 Web Push 广播能力（*push.Manager 满足；测试用替身）。
type Broadcaster interface {
	Broadcast(ctx context.Context, n push.Notification) int
}

// WebPush 适配 Web Push 通道：向全部订阅端点广播（与 gateway pushNotifier
// 原路径同语义——投递计数由 Manager 内部处理，不向上返回错误）。
type WebPush struct {
	Manager Broadcaster
}

// Name 实现 notify.Channel。
func (w *WebPush) Name() string { return "webpush" }

// Deliver 实现 notify.Channel：Manager 未配置时静默跳过（与旧 dispatch 一致）。
func (w *WebPush) Deliver(ctx context.Context, ev notify.Event) error {
	if w == nil || w.Manager == nil {
		return nil
	}
	w.Manager.Broadcast(ctx, ev.Notification)
	return nil
}
