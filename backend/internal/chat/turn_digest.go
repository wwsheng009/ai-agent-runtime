package chat

import (
	"context"
	"time"

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
	CaptureReceiptID   string
}

// TurnCaptureReceipt is the durable handoff between a terminal turn commit and
// asynchronous memory extraction. It contains identity and bounded diagnostic
// metadata only; the canonical transcript remains the source of truth.
type TurnCaptureReceipt struct {
	ProjectKey string
	ID         string
	SessionID  string
	// SessionGeneration identifies the canonical session incarnation that
	// produced this receipt. It is the cross-store purge fence token: a memory
	// worker from an older incarnation must not commit after the session ID is
	// recreated.
	SessionGeneration  int64
	TurnID             string
	MessageSeqStart    int64
	MessageSeqEnd      int64
	CanonicalRevision  int64
	RangeHash          string
	TraceID            string
	UserMessageID      string
	AssistantMessageID string
	Outcome            string
	Steps              int
	UsageJSON          string
	State              string
	CreatedAt          time.Time
	MaterializedAt     time.Time
	LastError          string
	AttemptCount       int
	RetryAt            time.Time
}

// SessionStorageTurnCaptureCommitter commits a terminal session snapshot and
// its capture receipt in the same canonical-store transaction.
type SessionStorageTurnCaptureCommitter interface {
	UpdateWithTurnCapture(context.Context, *Session, TurnCaptureReceipt) (TurnCaptureReceipt, error)
	SaveWithTurnCapture(context.Context, *Session, TurnCaptureReceipt) (TurnCaptureReceipt, error)
}

// SessionStorageTurnCaptureReceiptStore is the canonical outbox repository.
// Receipts are acknowledged only after digest/job materialization succeeds in
// the memory database.
type SessionStorageTurnCaptureReceiptStore interface {
	ListTurnCaptureReceipts(context.Context, int) ([]TurnCaptureReceipt, error)
	ListTurnCaptureReceiptsForSession(context.Context, string, int) ([]TurnCaptureReceipt, error)
	ListMaterializedTurnCaptureReceipts(context.Context, int) ([]TurnCaptureReceipt, error)
	ListMaterializedTurnCaptureReceiptsPage(context.Context, *TurnCaptureReceipt, int) ([]TurnCaptureReceipt, error)
	ListMaterializedTurnCaptureReceiptsForSession(context.Context, string, int) ([]TurnCaptureReceipt, error)
	ListMaterializedTurnCaptureReceiptsForSessionPage(context.Context, string, *TurnCaptureReceipt, int) ([]TurnCaptureReceipt, error)
	HasTurnCaptureReceiptsForSession(context.Context, string) (bool, error)
	GetTurnCaptureReceipt(context.Context, string) (TurnCaptureReceipt, error)
	ValidateTurnCaptureReceipt(context.Context, TurnCaptureReceipt) error
	MarkTurnCaptureReceiptMaterialized(context.Context, string) error
	DeferTurnCaptureReceipt(context.Context, string, string, time.Time) error
}

// SessionStorageTurnCaptureReceiptGenerationStore is an optional extension for
// queries that must not cross a reused session ID's incarnation boundary.
type SessionStorageTurnCaptureReceiptGenerationStore interface {
	ListTurnCaptureReceiptsForSessionGeneration(context.Context, string, int64, int) ([]TurnCaptureReceipt, error)
	HasTurnCaptureReceiptsForSessionGeneration(context.Context, string, int64) (bool, error)
}

type SessionStorageMaterializedTurnCaptureReceiptGenerationPageStore interface {
	ListMaterializedTurnCaptureReceiptsForSessionGenerationPage(context.Context, string, int64, *TurnCaptureReceipt, int) ([]TurnCaptureReceipt, error)
}

// TurnDigestSink is injected by a host. It runs only after the canonical
// receipt is committed; implementations must not invoke an LLM inline.
type TurnDigestSink interface {
	RecordTurnDigest(ctx context.Context, digest TurnDigest) error
}

type turnCaptureContextKey struct{}

func withTurnCaptureReceipt(ctx context.Context, receipt *TurnCaptureReceipt) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, turnCaptureContextKey{}, receipt)
}

func turnCaptureReceiptFromContext(ctx context.Context) (*TurnCaptureReceipt, bool) {
	if ctx == nil {
		return nil, false
	}
	receipt, ok := ctx.Value(turnCaptureContextKey{}).(*TurnCaptureReceipt)
	return receipt, ok && receipt != nil
}
