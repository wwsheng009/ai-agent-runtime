package chat

import (
	"context"
	"encoding/json"

	"github.com/ai-gateway/ai-agent-runtime/internal/agent"
	"github.com/ai-gateway/ai-agent-runtime/internal/checkpoint"
	runtimeevents "github.com/ai-gateway/ai-agent-runtime/internal/events"
	"github.com/ai-gateway/ai-agent-runtime/internal/team"
)

// Command represents a session actor command.
type Command interface {
	isCommand()
}

// SubmitPrompt triggers a new prompt execution.
type SubmitPrompt struct {
	Ctx     context.Context
	Prompt  string
	RunMeta *team.RunMeta
	Reply   chan SubmitResult
}

// SubmitResult carries the outcome of SubmitPrompt.
type SubmitResult struct {
	Result *agent.Result
	Err    error
}

// ApproveTool responds to a pending approval request.
type ApproveTool struct {
	Ctx         context.Context
	RequestID   string
	Allow       bool
	PatchedArgs json.RawMessage
	Reply       chan error
}

// AnswerQuestion answers a pending user question request.
type AnswerQuestion struct {
	Ctx        context.Context
	QuestionID string
	Answer     string
	Reply      chan error
}

// Interrupt attempts to stop an active execution.
type Interrupt struct {
	Ctx   context.Context
	Reply chan error
}

// RewindResult carries the outcome of a checkpoint rewind.
type RewindResult struct {
	Result *checkpoint.RestoreResult
	Err    error
}

// RewindTo requests a rewind to a checkpoint.
type RewindTo struct {
	Ctx          context.Context
	CheckpointID string
	Mode         string
	Reply        chan RewindResult
}

// DeliverMailboxMessage notifies the actor of a mailbox message.
type DeliverMailboxMessage struct {
	Ctx     context.Context
	Message team.MailMessage
	Reply   chan error
}

// SubscribeEvents attaches a channel to the runtime event bus.
type SubscribeEvents struct {
	Ctx       context.Context
	EventType string
	Ch        chan runtimeevents.Event
	Reply     chan error
}

func (SubmitPrompt) isCommand()          {}
func (ApproveTool) isCommand()           {}
func (AnswerQuestion) isCommand()        {}
func (Interrupt) isCommand()             {}
func (RewindTo) isCommand()              {}
func (DeliverMailboxMessage) isCommand() {}
func (SubscribeEvents) isCommand()       {}
