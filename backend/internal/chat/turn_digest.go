package chat

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TurnDigest is a lightweight, bounded record of one completed turn. It is
// produced by the session actor at the terminal assistant_message boundary and
// consumed by the memory write path. It must never trigger an LLM call by
// itself.
type TurnDigest struct {
	SessionID          string
	TurnID             string
	TraceID            string
	Prompt             string
	AssistantOutput    string
	Outcome            string // success | interrupted | error | continuation | empty
	Steps              int
	Usage              *types.TokenUsage
	UserMessageID      string
	AssistantMessageID string
	MessageSeqEnd      int64
	AssistantStreamID  string
}

// TurnDigestSink is injected by a host. Implementations must be best-effort and
// must not block or fail the turn.
type TurnDigestSink interface {
	RecordTurnDigest(ctx context.Context, digest TurnDigest) error
}
