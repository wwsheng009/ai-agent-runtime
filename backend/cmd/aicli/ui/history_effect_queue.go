package ui

import (
	"errors"
	"sort"
)

var (
	ErrHistoryCommitFrozen          = errors.New("history commit queue is frozen by alternate screen lease")
	ErrHistoryProjectionUnknown     = errors.New("history projection is unknown; recovery is required before a new commit")
	ErrHistoryCommitRecoveryPending = errors.New("history commit has unresolved terminal delivery")
)

// HistoryEffectQueueState is the AppState-owned lifecycle record for native
// scrollback effects. Semantic transcript data remains in TranscriptState; the
// queue records only terminal delivery state and can be discarded/rebuilt with
// a controlled projection recovery.
type HistoryEffectQueueState struct {
	NextToken         uint64
	TerminalEpoch     uint64
	Frozen            bool
	ProjectionUnknown bool
	ledger            *HistoryCommitLedger
}

func (s HistoryEffectQueueState) Clone() HistoryEffectQueueState {
	s.ledger = s.ledger.Clone()
	return s
}

// Entries returns a token-ordered detached view for diagnostics, tests, and a
// future presenter. Callers cannot mutate the actor-owned ledger through it.
func (s HistoryEffectQueueState) Entries() []HistoryCommitEntry {
	return s.ledger.Entries()
}

// Pending returns the oldest-first commits eligible for a primary presenter.
// A frozen queue intentionally has no eligible effects even if semantic events
// keep enqueuing work while the alternate screen is visible.
func (s HistoryEffectQueueState) Pending() []HistoryCommit {
	if s.Frozen || s.ProjectionUnknown || s.hasUnresolvedTerminalDelivery() {
		return nil
	}
	if s.ledger == nil || !s.ledger.HasPending() {
		return nil
	}
	commits := make([]HistoryCommit, 0, s.ledger.pendingCount)
	for _, entry := range s.ledger.byToken {
		if entry.State == HistoryCommitPending {
			commits = append(commits, entry.Commit.Clone())
		}
	}
	if len(commits) > 1 {
		sort.Slice(commits, func(i, j int) bool { return commits[i].Token < commits[j].Token })
	}
	return commits
}

// HasPending is the cheap scheduler predicate. Unlike Pending it neither
// copies payload lines nor builds a detached view.
func (s HistoryEffectQueueState) HasPending() bool {
	return !s.Frozen && !s.ProjectionUnknown && !s.hasUnresolvedTerminalDelivery() &&
		s.ledger != nil && s.ledger.HasPending()
}

func (s *HistoryEffectQueueState) enqueue(commit HistoryCommit) error {
	if s == nil {
		return ErrInvalidHistoryCommit
	}
	if s.ledger == nil {
		s.ledger = NewHistoryCommitLedger()
	}
	autoToken := commit.Token == 0
	if autoToken {
		commit.Token = s.NextToken + 1
	}
	if err := s.ledger.Enqueue(commit); err != nil {
		return err
	}
	if autoToken || commit.Token > s.NextToken {
		s.NextToken = commit.Token
	}
	return nil
}

func (s *HistoryEffectQueueState) markInFlight(token, generation uint64) error {
	if s == nil || s.ledger == nil {
		return ErrCommitNotPending
	}
	if s.Frozen {
		return ErrHistoryCommitFrozen
	}
	if s.ProjectionUnknown {
		return ErrHistoryProjectionUnknown
	}
	if s.hasUnresolvedTerminalDelivery() {
		return ErrHistoryCommitRecoveryPending
	}
	entry, ok := s.ledger.Entry(token)
	if !ok || entry.Commit.LayoutGeneration != generation {
		return ErrStaleLayoutGeneration
	}
	// Native scrollback is ordered. Do not let a stale presenter claim a later
	// token while an earlier eligible effect has not reached a terminal result.
	if s.ledger.hasOlderPendingOrInFlight(token) {
		return ErrHistoryCommitOutOfOrder
	}
	return s.ledger.MarkInFlight(token)
}

func (s *HistoryEffectQueueState) rebasePending(commit HistoryCommit) error {
	if s == nil || s.ledger == nil {
		return ErrCommitNotPending
	}
	for _, entry := range s.ledger.byToken {
		current := entry.Commit
		if entry.State != HistoryCommitPending || current.Origin != commit.Origin ||
			current.CellID != commit.CellID ||
			(current.Origin != HistoryCommitActive && current.Revision != commit.Revision) ||
			current.SourceRange != commit.SourceRange ||
			current.FragmentID != commit.FragmentID {
			continue
		}
		return s.ledger.RebasePending(current.Token, commit)
	}
	return ErrCommitNotPending
}

func (s *HistoryEffectQueueState) invalidate(token uint64) error {
	if s == nil || s.ledger == nil {
		return ErrCommitNotPending
	}
	wasInFlight, err := s.ledger.Invalidate(token)
	if err != nil {
		return err
	}
	if wasInFlight {
		s.ProjectionUnknown = true
	}
	return nil
}

func (s *HistoryEffectQueueState) ack(token, frame, generation uint64) error {
	if s == nil || s.ledger == nil {
		return ErrCommitNotInFlight
	}
	return s.ledger.Ack(token, frame, generation)
}

// ackBatch confirms an ordered bootstrap transaction. The first commit was
// claimed before terminal I/O; later commits remain Pending until the same
// successful write proves their delivery. Advancing them here preserves the
// ledger's normal oldest-first ordering without giving TerminalSession effect
// ownership or accepting a stale batch after a semantic replacement.
func (s *HistoryEffectQueueState) ackBatch(commits []HistoryCommit, frame, generation uint64) error {
	if s == nil || s.ledger == nil || len(commits) == 0 {
		return ErrCommitNotInFlight
	}
	if s.Frozen {
		return ErrHistoryCommitFrozen
	}
	if s.ProjectionUnknown {
		return ErrHistoryProjectionUnknown
	}
	if s.hasUnresolvedTerminalDelivery() {
		return ErrHistoryCommitRecoveryPending
	}

	// Validate the complete delivered snapshot before changing any entry. The
	// terminal wrote this batch atomically, so a concurrent semantic rebase of
	// a later Pending token must not leave an Acked prefix and a retryable tail.
	previousToken := uint64(0)
	for index, commit := range commits {
		if commit.LayoutGeneration != generation || commit.Token == 0 || (previousToken != 0 && commit.Token <= previousToken) {
			return ErrStaleLayoutGeneration
		}
		entry, ok := s.ledger.Entry(commit.Token)
		if !ok || !historyCommitPresentationEqual(entry.Commit, commit) {
			return ErrCommitSourceChanged
		}
		if (index == 0 && entry.State != HistoryCommitInFlight) ||
			(index > 0 && entry.State != HistoryCommitPending) {
			return ErrCommitNotInFlight
		}
		previousToken = commit.Token
	}

	for index, commit := range commits {
		if index > 0 {
			if err := s.ledger.MarkInFlight(commit.Token); err != nil {
				return err
			}
		}
		if err := s.ack(commit.Token, frame, generation); err != nil {
			return err
		}
	}
	return nil
}

// markDeliveredBatchUnresolved records that the terminal may have accepted
// every row from a batch whose actor snapshot could no longer be acknowledged.
// None of those tokens may become retryable after a viewport-only recovery.
func (s *HistoryEffectQueueState) markDeliveredBatchUnresolved(commits []HistoryCommit, cause error) {
	if s == nil || s.ledger == nil {
		return
	}
	for _, delivered := range commits {
		entry, ok := s.ledger.byToken[delivered.Token]
		if !ok || entry.State == HistoryCommitAcked {
			continue
		}
		switch entry.State {
		case HistoryCommitPending:
			s.ledger.pendingCount--
			entry.State = HistoryCommitStateFailed
		case HistoryCommitInFlight, HistoryCommitStateFailed:
			entry.State = HistoryCommitStateFailed
		case HistoryCommitInvalidated:
			// Keep the invalidated identity, but strengthen its physical fact.
		}
		entry.Failure = cause
		entry.MayHavePartiallyWritten = true
		s.ledger.byToken[delivered.Token] = entry
	}
}

func (s *HistoryEffectQueueState) deferInFlight(token, generation uint64) error {
	if s == nil || s.ledger == nil {
		return ErrCommitNotInFlight
	}
	entry, ok := s.ledger.Entry(token)
	if !ok || entry.Commit.LayoutGeneration != generation {
		return ErrStaleLayoutGeneration
	}
	return s.ledger.DeferInFlight(token)
}

func (s *HistoryEffectQueueState) fail(token, generation uint64, err error, mayHavePartiallyWritten bool) error {
	if s == nil || s.ledger == nil {
		return ErrCommitNotInFlight
	}
	entry, ok := s.ledger.Entry(token)
	if !ok || entry.Commit.LayoutGeneration != generation {
		return ErrStaleLayoutGeneration
	}
	if err := s.ledger.Fail(token, err, mayHavePartiallyWritten); err != nil {
		return err
	}
	// Any failed terminal transaction means the cached physical projection can
	// no longer prove which bytes reached the terminal. Recovery must repaint
	// from semantic source instead of blind retrying the same handoff batch.
	s.ProjectionUnknown = true
	return nil
}

func (s *HistoryEffectQueueState) markProjectionKnown() {
	if s != nil {
		s.ProjectionUnknown = false
	}
}

// reconcileScrollback starts a new, explicitly proven native-scrollback
// epoch. It discards all old delivery records, including Acked records, because
// the terminal owner has reset or replaced the physical scrollback and must
// replan retained semantic transcript from source. Token allocation remains
// monotonic so stale callbacks from the retired epoch cannot acknowledge a new
// effect accidentally.
//
// The caller must first prove that the current generation has a Known primary
// projection and no active alternate-screen lease. Those checks deliberately
// live in the reducer beside HistoryProjectionRecovered rather than being
// guessed from queue-local state.
func (s *HistoryEffectQueueState) reconcileScrollback(epoch uint64) bool {
	if s == nil || epoch == 0 || epoch <= s.TerminalEpoch {
		return false
	}
	s.TerminalEpoch = epoch
	s.ledger = NewHistoryCommitLedger()
	return true
}

// hasTerminalRecordForSource reports whether this semantic range already has
// a lifecycle that must not be minted again. Invalidated pending work may be
// replanned if the same source becomes eligible later; an invalidated
// in-flight effect is different because it may already have written bytes and
// remains blocked behind projection recovery.
func (s HistoryEffectQueueState) hasTerminalRecordForSource(commit HistoryCommit) bool {
	return s.ledger.hasTerminalRecordForSource(historyCommitSourceIdentity(commit))
}

// hasUnresolvedTerminalDelivery prevents a recovered viewport from silently
// handing off a later cell after an earlier terminal transaction failed or was
// invalidated in flight. A full primary repaint can restore the visible frame,
// but it cannot prove what reached native scrollback; that range needs an
// explicit reconciliation policy before ordered history delivery resumes.
func (s HistoryEffectQueueState) hasUnresolvedTerminalDelivery() bool {
	return s.ledger.hasUnresolvedTerminalDelivery()
}
