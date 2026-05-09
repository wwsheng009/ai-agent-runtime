package team

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

type agentControlWakeRegistryStore interface {
	WatchAgentControlWake(ctx context.Context, filter agentcontrol.WakeFilter) (<-chan agentcontrol.WakeEvent, func())
	LastAgentControlWakeSeq(ctx context.Context, filter agentcontrol.WakeFilter) (int64, error)
}

func taskWakeFromGenericWake(event agentcontrol.WakeEvent) agentcontrol.TaskWakeEvent {
	return agentcontrol.TaskWakeEvent{
		Seq:       event.Seq,
		Workflow:  event.Workflow,
		TeamID:    event.TeamID,
		TaskID:    event.TaskID,
		Kind:      event.EventKind,
		Status:    event.Status,
		CreatedAt: event.CreatedAt,
	}.Normalize()
}

func mailboxWakeFromGenericWake(event agentcontrol.WakeEvent) agentcontrol.MailboxWakeEvent {
	return agentcontrol.MailboxWakeEvent{
		Seq:       event.Seq,
		Workflow:  event.Workflow,
		TeamID:    event.TeamID,
		SessionID: event.SessionID,
		MessageID: event.MessageID,
		Kind:      event.EventKind,
		FromAgent: event.FromAgent,
		ToAgent:   event.ToAgent,
		TaskID:    event.TaskID,
		CreatedAt: event.CreatedAt,
	}.Normalize()
}
