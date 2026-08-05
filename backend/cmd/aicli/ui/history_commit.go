package ui

import (
	"errors"
	"fmt"
	"sort"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

var (
	ErrInvalidHistoryCommit    = errors.New("invalid history commit")
	ErrDuplicateCommitToken    = errors.New("duplicate history commit token")
	ErrDuplicateCommitRange    = errors.New("duplicate history commit range")
	ErrCommitNotPending        = errors.New("history commit is not pending")
	ErrCommitNotInFlight       = errors.New("history commit is not in flight")
	ErrHistoryCommitOutOfOrder = errors.New("history commit is not the oldest eligible effect")
	ErrDuplicateCommitAck      = errors.New("duplicate history commit acknowledgement")
	ErrStaleLayoutGeneration   = errors.New("stale history commit layout generation")
	ErrCommitSourceChanged     = errors.New("history commit source identity changed")
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

// HistoryCommitOrigin distinguishes immutable transcript history from a
// stable prefix handed off while its semantic cell is still mutable. Active
// effects deliberately survive append-only source revisions; transcript
// effects retain the exact finalized revision as part of their identity.
type HistoryCommitOrigin uint8

const (
	HistoryCommitTranscript HistoryCommitOrigin = iota
	HistoryCommitActive
)

func (r DisplayRange) Valid() bool {
	return r.Start >= 0 && r.End >= r.Start
}

// HistoryCommit is the typed terminal effect for moving finalized display rows
// into native scrollback. Token, cell identity, revision, both ranges, and
// layout generation are all required; text is payload, never identity.
type HistoryCommit struct {
	Token       uint64
	Origin      HistoryCommitOrigin
	CellID      scene.CellID
	Revision    uint64
	SourceRange SourceRange
	// FragmentID is zero for source-preserving commits. Rich renderers use a
	// stable non-zero ordinal to identify a physical presentation fragment when
	// a single semantic source range produces several display rows.
	FragmentID       uint64
	DisplayRange     DisplayRange
	LayoutGeneration uint64
	Lines            []render.Line
}

func (c HistoryCommit) Valid() bool {
	return c.Token != 0 && c.Origin <= HistoryCommitActive &&
		c.CellID != 0 && c.LayoutGeneration != 0 &&
		c.SourceRange.Valid() && c.SourceRange.End > c.SourceRange.Start &&
		c.DisplayRange.Valid() && c.DisplayRange.End > c.DisplayRange.Start
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
	HistoryCommitStateFailed
	HistoryCommitInvalidated
)

func (s HistoryCommitState) String() string {
	switch s {
	case HistoryCommitPending:
		return "pending"
	case HistoryCommitInFlight:
		return "in_flight"
	case HistoryCommitAcked:
		return "acked"
	case HistoryCommitStateFailed:
		return "failed"
	case HistoryCommitInvalidated:
		return "invalidated"
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
	origin           HistoryCommitOrigin
	cellID           scene.CellID
	revision         uint64
	sourceStart      int
	sourceEnd        int
	fragmentID       uint64
	displayStart     int
	displayEnd       int
	layoutGeneration uint64
}

type historyCommitSourceKey struct {
	origin      HistoryCommitOrigin
	cellID      scene.CellID
	revision    uint64
	sourceStart int
	sourceEnd   int
	fragmentID  uint64
}

func historyCommitSourceIdentity(commit HistoryCommit) historyCommitSourceKey {
	revision := commit.Revision
	if commit.Origin == HistoryCommitActive {
		// Mutable source revisions are delivery fences, not new semantic cells.
		// An append-only update must not mint the same stable range again.
		revision = 0
	}
	return historyCommitSourceKey{
		origin:      commit.Origin,
		cellID:      commit.CellID,
		revision:    revision,
		sourceStart: commit.SourceRange.Start,
		sourceEnd:   commit.SourceRange.End,
		fragmentID:  commit.FragmentID,
	}
}

func historyCommitKey(c HistoryCommit) historyCommitRangeKey {
	return historyCommitRangeKey{
		origin:           c.Origin,
		cellID:           c.CellID,
		revision:         c.Revision,
		sourceStart:      c.SourceRange.Start,
		sourceEnd:        c.SourceRange.End,
		fragmentID:       c.FragmentID,
		displayStart:     c.DisplayRange.Start,
		displayEnd:       c.DisplayRange.End,
		layoutGeneration: c.LayoutGeneration,
	}
}

// HistoryCommitLedger is reducer-owned effect progress. It deliberately has
// no terminal I/O and does not infer identity from line text or hashes.
type HistoryCommitLedger struct {
	byToken      map[uint64]HistoryCommitEntry
	byRange      map[historyCommitRangeKey]uint64
	bySource     map[historyCommitSourceKey]map[uint64]struct{}
	pendingCount int
}

func NewHistoryCommitLedger() *HistoryCommitLedger {
	return &HistoryCommitLedger{
		byToken:  make(map[uint64]HistoryCommitEntry),
		byRange:  make(map[historyCommitRangeKey]uint64),
		bySource: make(map[historyCommitSourceKey]map[uint64]struct{}),
	}
}

func (l *HistoryCommitLedger) Enqueue(commit HistoryCommit) error {
	if l == nil {
		return fmt.Errorf("%w: nil ledger", ErrInvalidHistoryCommit)
	}
	if l.byToken == nil {
		l.byToken = make(map[uint64]HistoryCommitEntry)
	}
	if l.byRange == nil {
		l.byRange = make(map[historyCommitRangeKey]uint64)
	}
	if l.bySource == nil {
		l.bySource = make(map[historyCommitSourceKey]map[uint64]struct{})
	}
	if !commit.Valid() {
		return ErrInvalidHistoryCommit
	}
	if _, exists := l.byToken[commit.Token]; exists {
		return ErrDuplicateCommitToken
	}
	key := historyCommitKey(commit)
	if token, exists := l.byRange[key]; exists {
		previous := l.byToken[token]
		if previous.State != HistoryCommitInvalidated || previous.MayHavePartiallyWritten {
			return ErrDuplicateCommitRange
		}
	}
	l.byToken[commit.Token] = HistoryCommitEntry{Commit: commit.Clone(), State: HistoryCommitPending}
	l.byRange[key] = commit.Token
	sourceKey := historyCommitSourceIdentity(commit)
	if l.bySource[sourceKey] == nil {
		l.bySource[sourceKey] = make(map[uint64]struct{})
	}
	l.bySource[sourceKey][commit.Token] = struct{}{}
	l.pendingCount++
	return nil
}

func (l *HistoryCommitLedger) MarkInFlight(token uint64) error {
	entry, ok := l.entry(token)
	if !ok || entry.State != HistoryCommitPending {
		return ErrCommitNotPending
	}
	entry.State = HistoryCommitInFlight
	l.byToken[token] = entry
	l.pendingCount--
	return nil
}

// DeferInFlight returns an effect to Pending only when its presenter did not
// begin a terminal write. The token and immutable payload remain unchanged, so
// retrying after a lease/recovery boundary cannot mint a second handoff.
func (l *HistoryCommitLedger) DeferInFlight(token uint64) error {
	entry, ok := l.entry(token)
	if !ok || entry.State != HistoryCommitInFlight {
		return ErrCommitNotInFlight
	}
	entry.State = HistoryCommitPending
	entry.AckFrame = 0
	entry.Failure = nil
	entry.MayHavePartiallyWritten = false
	l.byToken[token] = entry
	l.pendingCount++
	return nil
}

// RebasePending updates only the display payload of an unstarted effect after
// a layout generation change. Token and semantic source identity are retained;
// resize therefore never creates a second history handoff token.
func (l *HistoryCommitLedger) RebasePending(token uint64, replacement HistoryCommit) error {
	entry, ok := l.entry(token)
	if !ok || entry.State != HistoryCommitPending {
		return ErrCommitNotPending
	}
	current := entry.Commit
	if current.Origin != replacement.Origin || current.CellID != replacement.CellID ||
		(current.Origin != HistoryCommitActive && current.Revision != replacement.Revision) ||
		current.SourceRange != replacement.SourceRange || current.FragmentID != replacement.FragmentID {
		return ErrCommitSourceChanged
	}
	replacement.Token = token
	if !replacement.Valid() {
		return ErrInvalidHistoryCommit
	}
	oldKey := historyCommitKey(current)
	newKey := historyCommitKey(replacement)
	if existing, exists := l.byRange[newKey]; exists && existing != token {
		return ErrDuplicateCommitRange
	}
	delete(l.byRange, oldKey)
	entry.Commit = replacement.Clone()
	l.byToken[token] = entry
	l.byRange[newKey] = token
	return nil
}

// Invalidate prevents a pending or in-flight effect from being consumed after
// transcript replacement. An in-flight invalidation means terminal bytes may
// already have reached the old projection and therefore requires recovery.
func (l *HistoryCommitLedger) Invalidate(token uint64) (wasInFlight bool, err error) {
	entry, ok := l.entry(token)
	if !ok || (entry.State != HistoryCommitPending && entry.State != HistoryCommitInFlight) {
		return false, ErrCommitNotPending
	}
	wasInFlight = entry.State == HistoryCommitInFlight
	entry.State = HistoryCommitInvalidated
	// An in-flight transaction can already have written a prefix before a
	// semantic replacement or geometry barrier reaches the actor. Preserve that
	// fact on the ledger entry so later planning never treats this identity as a
	// harmless pending cancellation.
	if wasInFlight {
		entry.MayHavePartiallyWritten = true
	} else {
		l.pendingCount--
	}
	l.byToken[token] = entry
	return wasInFlight, nil
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
	// Finalized transcript source can always rebuild its payload. A mutable
	// handoff retains its small delivered fragment until finalization so the
	// finalized planner can prove and skip that already-physical rich prefix.
	if entry.Commit.Origin == HistoryCommitTranscript {
		entry.Commit.Lines = nil
	}
	l.byToken[token] = entry
	return nil
}

func (l *HistoryCommitLedger) Fail(token uint64, err error, mayHavePartiallyWritten bool) error {
	entry, ok := l.entry(token)
	if !ok || entry.State != HistoryCommitInFlight {
		return ErrCommitNotInFlight
	}
	entry.State = HistoryCommitStateFailed
	entry.Failure = err
	entry.MayHavePartiallyWritten = mayHavePartiallyWritten
	l.byToken[token] = entry
	return nil
}

func (l *HistoryCommitLedger) Entry(token uint64) (HistoryCommitEntry, bool) {
	entry, ok := l.entry(token)
	return entry.Clone(), ok
}

// Entries returns a token-ordered detached ledger view. Ordering is part of
// the terminal-effect contract: a presenter must consume the oldest eligible
// pending commit before newer history ranges.
func (l *HistoryCommitLedger) Entries() []HistoryCommitEntry {
	if l == nil || len(l.byToken) == 0 {
		return nil
	}
	tokens := l.orderedTokens()
	entries := make([]HistoryCommitEntry, 0, len(tokens))
	for _, token := range tokens {
		entry := l.byToken[token]
		entries = append(entries, entry.Clone())
	}
	return entries
}

// HasPending is allocation-free and is intended for scheduler decisions. The
// detached Entries view is deliberately reserved for diagnostics and callers
// that actually need payload ownership.
func (l *HistoryCommitLedger) HasPending() bool {
	return l != nil && l.pendingCount > 0
}

func (l *HistoryCommitLedger) hasTerminalRecordForSource(key historyCommitSourceKey) bool {
	if l == nil {
		return false
	}
	for token := range l.bySource[key] {
		entry, ok := l.byToken[token]
		if !ok {
			continue
		}
		switch entry.State {
		case HistoryCommitPending, HistoryCommitInFlight, HistoryCommitAcked, HistoryCommitStateFailed:
			return true
		case HistoryCommitInvalidated:
			if entry.MayHavePartiallyWritten {
				return true
			}
		}
	}
	return false
}

func (l *HistoryCommitLedger) hasUnresolvedTerminalDelivery() bool {
	if l == nil {
		return false
	}
	for _, entry := range l.byToken {
		switch entry.State {
		case HistoryCommitStateFailed:
			return true
		case HistoryCommitInvalidated:
			if entry.MayHavePartiallyWritten {
				return true
			}
		}
	}
	return false
}

func (l *HistoryCommitLedger) hasOlderPendingOrInFlight(token uint64) bool {
	if l == nil {
		return false
	}
	for earlierToken, entry := range l.byToken {
		if earlierToken >= token {
			continue
		}
		if entry.State == HistoryCommitPending || entry.State == HistoryCommitInFlight {
			return true
		}
	}
	return false
}

func (l *HistoryCommitLedger) orderedTokens() []uint64 {
	if l == nil || len(l.byToken) == 0 {
		return nil
	}
	tokens := make([]uint64, 0, len(l.byToken))
	for token := range l.byToken {
		tokens = append(tokens, token)
	}
	sort.Slice(tokens, func(i, j int) bool { return tokens[i] < tokens[j] })
	return tokens
}

// Clone returns an independent ledger. AppState snapshots must never retain
// actor-owned maps or render-line slices.
func (l *HistoryCommitLedger) Clone() *HistoryCommitLedger {
	clone := NewHistoryCommitLedger()
	if l == nil {
		return clone
	}
	for token, entry := range l.byToken {
		clone.byToken[token] = entry.Clone()
	}
	for key, token := range l.byRange {
		clone.byRange[key] = token
	}
	for key, tokens := range l.bySource {
		cloneTokens := make(map[uint64]struct{}, len(tokens))
		for token := range tokens {
			cloneTokens[token] = struct{}{}
		}
		clone.bySource[key] = cloneTokens
	}
	clone.pendingCount = l.pendingCount
	return clone
}

func (l *HistoryCommitLedger) entry(token uint64) (HistoryCommitEntry, bool) {
	if l == nil || l.byToken == nil {
		return HistoryCommitEntry{}, false
	}
	entry, ok := l.byToken[token]
	return entry, ok
}
