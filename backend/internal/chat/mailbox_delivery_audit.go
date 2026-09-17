package chat

import (
	"context"
	"strings"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// 单发指令投递审计（P0-3a 改动 4，方案 §3.3/§7.3）。字段命名对齐批量路径
// subagent_batch_coordinator.go 的 mailbox_delivery_status /
// mailbox_delivery_error：成功投递写在目标 mailbox 消息的 metadata 上（
// mailbox_received 事件与 read_mailbox_digest 都能看到），发起方会话再留一条
// mailbox_delivery 事件，失败也能被 /debug 观察到。该审计不进入 supervision
// control 动作管线，它只是消息记录。

const (
	// MailboxDeliveryStatusDelivered 表示消息已写入目标会话 mailbox。
	MailboxDeliveryStatusDelivered = "delivered"
	// MailboxDeliveryStatusQueued 表示消息等待子会话消费（trigger_turn）。
	MailboxDeliveryStatusQueued = "queued"
	// MailboxDeliveryStatusFailed 表示投递失败，目标未收到消息。
	MailboxDeliveryStatusFailed = "failed"
)

// MailboxDeliveryAudit 描述一次单发指令投递的结果。
type MailboxDeliveryAudit struct {
	FromSessionID   string
	TargetSessionID string
	Tool            string
	MessageID       string
	Status          string
	Error           string
	Delivered       bool
	Queued          bool
	Triggered       bool
	Duplicate       bool
}

// AnnotateAgentMailboxDelivery stamps the delivery status onto the outgoing
// mailbox message metadata so the durable mailbox row itself carries the audit
// fields (read_mailbox_digest / mailbox_received consumers see them).
func AnnotateAgentMailboxDelivery(message *team.MailMessage, status string, deliveryErr error, duplicate bool) {
	if message == nil {
		return
	}
	if message.Metadata == nil {
		message.Metadata = map[string]interface{}{}
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = MailboxDeliveryStatusDelivered
	}
	message.Metadata["mailbox_delivery_status"] = status
	if duplicate {
		message.Metadata["mailbox_delivery_duplicate"] = true
	}
	if deliveryErr != nil {
		message.Metadata["mailbox_delivery_error"] = deliveryErr.Error()
	}
}

// AppendMailboxDeliveryAudit writes one best-effort audit event to the
// initiating session's event stream. It never fails the delivery itself.
func AppendMailboxDeliveryAudit(ctx context.Context, store EventStore, bus runtimeevents.Publisher, sessionID string, audit MailboxDeliveryAudit) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	payload := map[string]interface{}{
		"mailbox_delivery_status": audit.Status,
		"target_session_id":       strings.TrimSpace(audit.TargetSessionID),
		"from_session_id":         strings.TrimSpace(audit.FromSessionID),
		"tool":                    strings.TrimSpace(audit.Tool),
		"delivered":               audit.Delivered,
		"queued":                  audit.Queued,
		"triggered":               audit.Triggered,
		"duplicate":               audit.Duplicate,
	}
	if id := strings.TrimSpace(audit.MessageID); id != "" {
		payload["message_id"] = id
	}
	if errText := strings.TrimSpace(audit.Error); errText != "" {
		payload["mailbox_delivery_error"] = errText
	}
	event := runtimeevents.Event{
		Type:      EventMailboxDelivery,
		SessionID: sessionID,
		Payload:   payload,
		Timestamp: time.Now().UTC(),
	}
	if store != nil {
		if seq, err := store.AppendEvent(ctx, event); err == nil {
			payload["seq"] = seq
		}
	}
	if bus != nil {
		bus.Publish(event)
	}
}

// MailboxDeliveryResultMessageID returns the durable message id assigned by the
// mailbox store (empty when the live fallback path was used).
func MailboxDeliveryResultMessageID(delivery MailboxDeliveryResult) string {
	if delivery.Event.Payload == nil {
		return ""
	}
	value, _ := delivery.Event.Payload["message_id"].(string)
	return strings.TrimSpace(value)
}
