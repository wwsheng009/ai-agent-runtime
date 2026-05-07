package agentcontrol

import (
	"context"
	"strings"
	"time"
)

// MailboxWakeFilter identifies the workflow-scoped mailbox wake stream a
// scheduler or orchestrator wants to consume.
type MailboxWakeFilter struct {
	Workflow string
	TeamID   string
	SessionID string
}

// MailboxWakeEvent is the storage-neutral mailbox wake signal exposed through
// AgentControl. Seq is scoped by the backing mailbox stream represented by the
// same filter.
type MailboxWakeEvent struct {
	Seq       int64     `json:"seq,omitempty"`
	Workflow  string    `json:"workflow,omitempty"`
	TeamID    string    `json:"team_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	FromAgent string    `json:"from_agent,omitempty"`
	ToAgent   string    `json:"to_agent,omitempty"`
	TaskID    string    `json:"task_id,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// MailboxWakeWatcher exposes mailbox wake notifications through AgentControl
// instead of a workflow-specific mailbox watcher type.
type MailboxWakeWatcher interface {
	WatchAgentControlMailboxWake(ctx context.Context, filter MailboxWakeFilter) (<-chan MailboxWakeEvent, func())
}

// MailboxWakeSequencer exposes the durable high-water mark for an
// AgentControl mailbox wake stream.
type MailboxWakeSequencer interface {
	LastAgentControlMailboxWakeSeq(ctx context.Context, filter MailboxWakeFilter) (int64, error)
}

// MailboxWakeSource is the combined watcher/sequence substrate consumed by
// orchestrators that need durable mailbox wake semantics.
type MailboxWakeSource interface {
	MailboxWakeWatcher
	MailboxWakeSequencer
}

// Normalize trims mailbox wake filter fields.
func (f MailboxWakeFilter) Normalize() MailboxWakeFilter {
	f.Workflow = strings.TrimSpace(f.Workflow)
	f.TeamID = strings.TrimSpace(f.TeamID)
	f.SessionID = strings.TrimSpace(f.SessionID)
	return f
}

// Normalize trims mailbox wake event fields.
func (e MailboxWakeEvent) Normalize() MailboxWakeEvent {
	e.Workflow = strings.TrimSpace(e.Workflow)
	e.TeamID = strings.TrimSpace(e.TeamID)
	e.SessionID = strings.TrimSpace(e.SessionID)
	e.MessageID = strings.TrimSpace(e.MessageID)
	e.Kind = strings.TrimSpace(e.Kind)
	e.FromAgent = strings.TrimSpace(e.FromAgent)
	e.ToAgent = strings.TrimSpace(e.ToAgent)
	e.TaskID = strings.TrimSpace(e.TaskID)
	return e
}
