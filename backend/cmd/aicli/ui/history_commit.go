package ui

import (
	"errors"
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

var (
	ErrInvalidHistoryCommit  = errors.New("invalid history commit")
	ErrDuplicateCommitToken  = errors.New("duplicate history commit token")
	ErrDuplicateCommitRange  = errors.New("duplicate history commit range")
	ErrCommitNotPending      = errors.New("history commit is not pending")
	ErrCommitNotInFlight     = errors.New("history commit is not in flight")
	ErrDuplicateCommitAck    = errors.New("duplicate history commit acknowledgement")
	ErrStaleLayoutGeneration = errors.New("stale history commit layout generation")
)

// SourceRange identifies a half-open range in one semantic cell source.
// It is not a physical terminal-row range.
type SourceRange struct {
	Start int
	End   int
}

func (r SourceRange) Valid() bool {
	return r.Start >= 0 && r.End >= r.Start
}

// DisplayRange identifies the half-open derived display-row range emitted by
// one layout generation.
type DisplayRange struct {
	Start int
	End   int
}

func (r DisplayRange) Valid() bool {
	return r.Start >= 0 && r.End >= r.Start
}

// HistoryCommit is the typed terminal effect for moving finalized display rows
// into native scrollback. Token, cell identity, revision, both ranges, and
// layout generation are all required; text is payload, never identity.
type HistoryCommit struct {
	Token            uint64
	CellID           scene.CellID
	Revision         uint64
	SourceRange      SourceRange
	DisplayRange     DisplayRange
	LayoutGeneration uint64
	Lines            []render.Line
}

func (c HistoryCommit) Valid() bool {
	return c.Token != 0 && c.CellID != 0 && c.SourceRange.Valid() && c.DisplayRange.Valid()
}

func (c HistoryCommit) Clone() HistoryCommit {
	c.Lines = cloneRenderLines(c.Lines)
	return c
}

// HistoryCommitState records terminal-effect progress separately from source
// and physical projection state.
type HistoryCommitState uint8

const (
	HistoryCommitPending HistoryCommitState = iota
	HistoryCommitInFlight
	HistoryCommitAcked
	HistoryCommitFailed
)

func (s HistoryCommitState) String() string {
	switch s {
	case HistoryCommitPending:
		return "pending"
	case HistoryCommitInFlight:
		return "in_flight"
	case HistoryCommitAcked:
		return "acked"
	case HistoryCommitFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// HistoryCommitEntry is an immutable snapshot of one ledger item.
type HistoryCommitEntry struct {
	Commit                  HistoryCommit
	State                   HistoryCommitState
	AckFrame                uint64
	MayHavePartiallyWritten bool
	Failure                 error
}

func (e HistoryCommitEntry) Clone() HistoryCommitEntry {
	e.Commit = e.Commit.Clone()
	return e
}

type historyCommitRangeKey struct {
	cellID           scene.CellID
	revision         uint64
	sourceStart      int
	sourceEnd        int
	displayStart     int
	displayEnd       int
	layoutGeneration uint64
}

func historyCommitKey(c HistoryCommit) historyCommitRangeKey {
	return historyCommitRangeKey{
		cellID:           c.CellID,
		revision:         c.Revision,
		sourceStart:      c.SourceRange.Start,
		sourceEnd:        c.SourceRange.End,
		displayStart:     c.DisplayRange.Start,
		displayEnd:       c.DisplayRange.End,
		layoutGeneration: c.LayoutGeneration,
	}
}

// HistoryCommitLedger is reducer-owned effect progress. It deliberately has
// no terminal I/O and does not infer identity from line text or hashes.
type HistoryCommitLedger struct {
	byToken map[uint64]HistoryCommitEntry
	byRange map[historyCommitRangeKey]uint64
}

func NewHistoryCommitLedger() *HistoryCommitLedger {
	return &HistoryCommitLedger{
		byToken: make(map[uint64]HistoryCommitEntry),
		byRange: make(map[historyCommitRangeKey]uint64),
	}
}

func (l *HistoryCommitLedger) Enqueue(commit HistoryCommit) error {
	if l == nil {
		return fmt.Errorf("%w: nil ledger", ErrInvalidHistoryCommit)
	}
	if !commit.Valid() {
		return ErrInvalidHistoryCommit
	}
	if _, exists := l.byToken[commit.Token]; exists {
		return ErrDuplicateCommitToken
	}
	key := historyCommitKey(commit)
	if _, exists := l.byRange[key]; exists {
		return ErrDuplicateCommitRange
	}
	l.byToken[commit.Token] = HistoryCommitEntry{Commit: commit.Clone(), State: HistoryCommitPending}
	l.byRange[key] = commit.Token
	return nil
}

func (l *HistoryCommitLedger) MarkInFlight(token uint64) error {
	entry, ok := l.entry(token)
	if !ok || entry.State != HistoryCommitPending {
		return ErrCommitNotPending
	}
	entry.State = HistoryCommitInFlight
	l.byToken[token] = entry
	return nil
}

// Ack accepts a terminal effect only for the layout generation that produced
// it. A stale acknowledgement remains in-flight for the caller's recovery
// policy; it never advances semantic handoff progress.
func (l *HistoryCommitLedger) Ack(token, frame, currentGeneration uint64) error {
	entry, ok := l.entry(token)
	if !ok {
		return ErrCommitNotInFlight
	}
	if entry.State == HistoryCommitAcked {
		return ErrDuplicateCommitAck
	}
	if entry.State != HistoryCommitInFlight {
		return ErrCommitNotInFlight
	}
	if entry.Commit.LayoutGeneration != currentGeneration {
		return ErrStaleLayoutGeneration
	}
	entry.State = HistoryCommitAcked
	entry.AckFrame = frame
	entry.Failure = nil
	entry.MayHavePartiallyWritten = false
	l.byToken[token] = entry
	return nil
}

func (l *HistoryCommitLedger) Fail(token uint64, err error, mayHavePartiallyWritten bool) error {
	entry, ok := l.entry(token)
	if !ok || entry.State != HistoryCommitInFlight {
		return ErrCommitNotInFlight
	}
	entry.State = HistoryCommitFailed
	entry.Failure = err
	entry.MayHavePartiallyWritten = mayHavePartiallyWritten
	l.byToken[token] = entry
	return nil
}

func (l *HistoryCommitLedger) Entry(token uint64) (HistoryCommitEntry, bool) {
	entry, ok := l.entry(token)
	return entry.Clone(), ok
}

func (l *HistoryCommitLedger) entry(token uint64) (HistoryCommitEntry, bool) {
	if l == nil || l.byToken == nil {
		return HistoryCommitEntry{}, false
	}
	entry, ok := l.byToken[token]
	return entry, ok
}
