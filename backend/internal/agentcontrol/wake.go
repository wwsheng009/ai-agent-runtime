package agentcontrol

import (
	"context"
	"strings"
	"time"
)

const (
	// WakeKindMailbox identifies mailbox-driven scheduler wake events.
	WakeKindMailbox = "mailbox"
	// WakeKindTask identifies task lifecycle scheduler wake events.
	WakeKindTask = "task"
)

// WakeFilter identifies a generic AgentControl wake stream. Workflow adapters
// can expose mailbox and task wake rows through this single registry while
// keeping typed MailboxWakeSource and TaskWakeSource APIs for orchestrators.
type WakeFilter struct {
	Workflow  string
	Kind      string
	TeamID    string
	SessionID string
}

// WakeEvent is the storage-neutral AgentControl wake registry row shared by
// mailbox and task wake adapters.
type WakeEvent struct {
	Seq       int64     `json:"seq,omitempty"`
	Workflow  string    `json:"workflow,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	TeamID    string    `json:"team_id,omitempty"`
	TeamSeq   int64     `json:"team_seq,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
	TaskID    string    `json:"task_id,omitempty"`
	EventKind string    `json:"event_kind,omitempty"`
	Status    string    `json:"status,omitempty"`
	FromAgent string    `json:"from_agent,omitempty"`
	ToAgent   string    `json:"to_agent,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// WakeWatcher exposes generic AgentControl wake notifications.
type WakeWatcher interface {
	WatchAgentControlWake(ctx context.Context, filter WakeFilter) (<-chan WakeEvent, func())
}

// WakeSequencer exposes the generic AgentControl wake high-water mark.
type WakeSequencer interface {
	LastAgentControlWakeSeq(ctx context.Context, filter WakeFilter) (int64, error)
}

// WakeSource combines generic wake watch and sequence reads.
type WakeSource interface {
	WakeWatcher
	WakeSequencer
}

// Normalize trims generic wake filter fields.
func (f WakeFilter) Normalize() WakeFilter {
	f.Workflow = strings.TrimSpace(f.Workflow)
	f.Kind = strings.TrimSpace(f.Kind)
	f.TeamID = strings.TrimSpace(f.TeamID)
	f.SessionID = strings.TrimSpace(f.SessionID)
	return f
}

// Normalize trims generic wake event fields.
func (e WakeEvent) Normalize() WakeEvent {
	e.Workflow = strings.TrimSpace(e.Workflow)
	e.Kind = strings.TrimSpace(e.Kind)
	e.TeamID = strings.TrimSpace(e.TeamID)
	e.SessionID = strings.TrimSpace(e.SessionID)
	e.MessageID = strings.TrimSpace(e.MessageID)
	e.TaskID = strings.TrimSpace(e.TaskID)
	e.EventKind = strings.TrimSpace(e.EventKind)
	e.Status = strings.TrimSpace(e.Status)
	e.FromAgent = strings.TrimSpace(e.FromAgent)
	e.ToAgent = strings.TrimSpace(e.ToAgent)
	return e
}
