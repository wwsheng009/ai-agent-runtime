package team

import (
	"context"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// AgentControlMailboxWake adapts the team mailbox store to the shared
// AgentControl mailbox wake seam. Team remains the backing mailbox authority.
type AgentControlMailboxWake struct {
	Store Store
}

var _ agentcontrol.MailboxWakeSource = AgentControlMailboxWake{}

// NewAgentControlMailboxWake creates a mailbox wake projection over a team
// store.
func NewAgentControlMailboxWake(store Store) AgentControlMailboxWake {
	return AgentControlMailboxWake{Store: store}
}

// WatchAgentControlMailboxWake adapts the team mailbox watcher to the shared
// AgentControl mailbox wake seam.
func (w AgentControlMailboxWake) WatchAgentControlMailboxWake(ctx context.Context, filter agentcontrol.MailboxWakeFilter) (<-chan agentcontrol.MailboxWakeEvent, func()) {
	out := make(chan agentcontrol.MailboxWakeEvent, 1)
	if ctx == nil {
		ctx = context.Background()
	}
	if w.Store == nil {
		return out, func() {}
	}
	filter = filter.Normalize()
	if filter.Workflow != "" && filter.Workflow != agentcontrol.WorkflowSpawnTeam {
		return out, func() {}
	}
	watcher, ok := w.Store.(MailWatcherStore)
	if !ok {
		return out, func() {}
	}
	input, unwatch := watcher.WatchMail(ctx, filter.TeamID)
	done := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case message, ok := <-input:
				if !ok {
					return
				}
				event := mailMessageToAgentControlWake(message)
				if event.TeamID == "" || (filter.TeamID != "" && !strings.EqualFold(event.TeamID, filter.TeamID)) {
					continue
				}
				select {
				case out <- event:
				default:
				}
			}
		}
	}()
	return out, func() {
		once.Do(func() {
			close(done)
			unwatch()
		})
	}
}

// LastAgentControlMailboxWakeSeq adapts the team mailbox sequence to the
// shared AgentControl mailbox wake seam.
func (w AgentControlMailboxWake) LastAgentControlMailboxWakeSeq(ctx context.Context, filter agentcontrol.MailboxWakeFilter) (int64, error) {
	if w.Store == nil {
		return 0, nil
	}
	filter = filter.Normalize()
	if filter.Workflow != "" && filter.Workflow != agentcontrol.WorkflowSpawnTeam {
		return 0, nil
	}
	sequencer, ok := w.Store.(MailSequenceStore)
	if !ok {
		return 0, nil
	}
	return sequencer.LastMailSeq(ctx, filter.TeamID)
}

func mailMessageToAgentControlWake(message MailMessage) agentcontrol.MailboxWakeEvent {
	taskID := ""
	if message.TaskID != nil {
		taskID = *message.TaskID
	}
	return agentcontrol.MailboxWakeEvent{
		Seq:       message.Seq,
		Workflow:  agentcontrol.WorkflowSpawnTeam,
		TeamID:    strings.TrimSpace(message.TeamID),
		MessageID: strings.TrimSpace(message.ID),
		Kind:      strings.TrimSpace(message.Kind),
		FromAgent: strings.TrimSpace(message.FromAgent),
		ToAgent:   strings.TrimSpace(message.ToAgent),
		TaskID:    strings.TrimSpace(taskID),
		CreatedAt: message.CreatedAt,
	}.Normalize()
}
