package channels

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/ntfy"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/push"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
)

// Ntfy 适配 ntfy 通道（映射语义与 gateway 原 ntfyMessage 逐字同源）。
type Ntfy struct {
	Sender *ntfy.Sender
}

// Name 实现 notify.Channel。
func (n *Ntfy) Name() string { return "ntfy" }

// Deliver 实现 notify.Channel：未配置/未启用时静默跳过（与旧 dispatch 的
// ntfyEnabled 判定一致）。
func (n *Ntfy) Deliver(ctx context.Context, ev notify.Event) error {
	if n == nil || n.Sender == nil || !n.Sender.Enabled() {
		return nil
	}
	return n.Sender.Send(ctx, NtfyMessage(ev.Notification))
}

// NtfyMessage 把通知映射为 ntfy 消息：高紧急性（Urgency=high）→ max priority +
// warning 标签；其余保持默认。与 cmd/gateway 原实现逐字一致。
func NtfyMessage(n push.Notification) ntfy.Message {
	priority := ntfy.PriorityDefault
	tags := []string{"white_check_mark"}
	if n.Urgency == "high" {
		priority = ntfy.PriorityMax
		tags = []string{"warning"}
	}
	return ntfy.Message{Title: n.Title, Body: n.Body, URL: n.URL, Priority: priority, Tags: tags}
}
