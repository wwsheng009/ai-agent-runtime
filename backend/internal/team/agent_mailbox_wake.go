package team

import (
	"context"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// AgentControlMailboxWake adapts mailbox wake notifications to the shared
// AgentControl mailbox wake seam. Stores with the generic AgentControl wake
// registry are preferred; team-native mailbox rows remain a compatibility
// fallback.
type AgentControlMailboxWake struct {
	Store Store
}

var _ agentcontrol.MailboxWakeSource = AgentControlMailboxWake{}

// NewAgentControlMailboxWake creates a mailbox wake projection over a team
// store.
func NewAgentControlMailboxWake(store Store) AgentControlMailboxWake {
	return AgentControlMailboxWake{Store: store}
}

// WatchAgentControlMailboxWake subscribes to AgentControl mailbox wake events.
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
	if watcher, ok := w.Store.(agentControlWakeRegistryStore); ok {
		input, unwatch := watcher.WatchAgentControlWake(ctx, agentcontrol.WakeFilter{
			Workflow: agentcontrol.WorkflowSpawnTeam,
			Kind:     agentcontrol.WakeKindMailbox,
			TeamID:   filter.TeamID,
		})
		return w.watchGenericAgentControlMailboxWake(ctx, filter, input, unwatch, out)
	}
	if watcher, ok := w.Store.(AgentControlMailboxWatcherStore); ok {
		input, unwatch := watcher.WatchAgentControlMailboxSignals(ctx, agentcontrol.WorkflowSpawnTeam, filter.TeamID)
		return w.watchAgentControlMailboxWake(ctx, filter, input, unwatch, out)
	}
	watcher, ok := w.Store.(MailWatcherStore)
	if !ok {
		return out, func() {}
	}
	input, unwatch := watcher.WatchMail(ctx, filter.TeamID)
	return w.watchAgentControlMailboxWake(ctx, filter, input, unwatch, out)
}

func (w AgentControlMailboxWake) watchGenericAgentControlMailboxWake(ctx context.Context, filter agentcontrol.MailboxWakeFilter, input <-chan agentcontrol.WakeEvent, unwatch func(), out chan agentcontrol.MailboxWakeEvent) (<-chan agentcontrol.MailboxWakeEvent, func()) {
	done := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case event, ok := <-input:
				if !ok {
					return
				}
				if event.Kind != agentcontrol.WakeKindMailbox {
					continue
				}
				wake := mailboxWakeFromGenericWake(event)
				if wake.TeamID == "" || (filter.TeamID != "" && !strings.EqualFold(wake.TeamID, filter.TeamID)) {
					continue
				}
				select {
				case out <- wake:
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

func (w AgentControlMailboxWake) watchAgentControlMailboxWake(ctx context.Context, filter agentcontrol.MailboxWakeFilter, input <-chan MailMessage, unwatch func(), out chan agentcontrol.MailboxWakeEvent) (<-chan agentcontrol.MailboxWakeEvent, func()) {
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

// LastAgentControlMailboxWakeSeq returns the AgentControl mailbox wake
// high-water mark, preferring the generic AgentControl wake registry.
func (w AgentControlMailboxWake) LastAgentControlMailboxWakeSeq(ctx context.Context, filter agentcontrol.MailboxWakeFilter) (int64, error) {
	if w.Store == nil {
		return 0, nil
	}
	filter = filter.Normalize()
	if filter.Workflow != "" && filter.Workflow != agentcontrol.WorkflowSpawnTeam {
		return 0, nil
	}
	if sequencer, ok := w.Store.(agentControlWakeRegistryStore); ok {
		return sequencer.LastAgentControlWakeSeq(ctx, agentcontrol.WakeFilter{
			Workflow: agentcontrol.WorkflowSpawnTeam,
			Kind:     agentcontrol.WakeKindMailbox,
			TeamID:   filter.TeamID,
		})
	}
	if sequencer, ok := w.Store.(AgentControlMailboxSequenceStore); ok {
		return sequencer.LastAgentControlMailboxSignalSeq(ctx, agentcontrol.WorkflowSpawnTeam, filter.TeamID)
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
	seq := message.Seq
	if message.ControlSeq > 0 {
		seq = message.ControlSeq
	}
	return agentcontrol.MailboxWakeEvent{
		Seq:       seq,
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
