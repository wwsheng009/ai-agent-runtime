package ui

const (
	// Bootstrap batching is a scheduling bound, not a semantic truncation:
	// every admitted prefix is acknowledged before the executor claims the next
	// token. The already-claimed head is always included even when it alone
	// exceeds a bound because HistoryCommit has no partial-token ACK state.
	terminalHistoryBatchMaxCommits   = 256
	terminalHistoryBatchMaxRows      = 2048
	terminalHistoryBatchMaxSpans     = 8192
	terminalHistoryBatchMaxTextBytes = 4 << 20
)

type terminalSessionScheduleSnapshot struct {
	projectionUnknown      bool
	reconciliationRequired bool
	recoveryActionable     bool
	pendingToken           uint64
	pendingGeneration      uint64
	stateRevision          uint64
	stateGeneration        uint64
}

type terminalSessionControllerSnapshot struct {
	appState               AppState
	projectionUnknown      bool
	reconciliationRequired bool
	claimed                *HistoryCommit
	bootstrap              []HistoryCommit
}

// terminalSessionSchedule reads only queue scalars and the oldest token
// identity. The executor does not need a detached transcript or cloned history
// payload before it has successfully claimed that token.
func (c *UIController) terminalSessionSchedule() terminalSessionScheduleSnapshot {
	if c == nil {
		return terminalSessionScheduleSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	effects := c.state.HistoryEffects
	snapshot := terminalSessionScheduleSnapshot{
		projectionUnknown:      effects.ProjectionUnknown,
		reconciliationRequired: effects.ReconciliationRequired,
		recoveryActionable:     terminalHistoryRecoveryActionable(c.state),
		stateRevision:          c.revision,
		stateGeneration:        c.state.LayoutGeneration,
	}
	if !effects.HasPending() || effects.ledger == nil {
		return snapshot
	}
	// Use the ordered token cache so the schedule predicate avoids a full-map
	// scan on every executor cycle. The first pending entry in ascending order
	// is the oldest eligible claim.
	for _, token := range effects.ledger.orderedTokens() {
		entry, ok := effects.ledger.byToken[token]
		if !ok || entry.State != HistoryCommitPending {
			continue
		}
		snapshot.pendingToken = token
		snapshot.pendingGeneration = entry.Commit.LayoutGeneration
		break
	}
	return snapshot
}

// terminalSessionSnapshot detaches exactly the data consumed by one physical
// transaction. Finalized transcript payloads reach the terminal exclusively
// through claimed HistoryCommit values, so the mutable viewport snapshot does
// not clone the complete transcript or the complete effect ledger.
func (c *UIController) terminalSessionSnapshot(claimedToken uint64) terminalSessionControllerSnapshot {
	if c == nil {
		return terminalSessionControllerSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := terminalSessionControllerSnapshot{
		appState:               terminalViewportAppState(c.state.AppState),
		projectionUnknown:      c.state.HistoryEffects.ProjectionUnknown,
		reconciliationRequired: c.state.HistoryEffects.ReconciliationRequired,
	}
	if claimedToken != 0 {
		snapshot.claimed, snapshot.bootstrap = terminalSessionClaimedBatchLocked(c.state, claimedToken)
	}
	return snapshot
}

func terminalViewportAppState(state AppState) AppState {
	effects := state.HistoryEffects
	return AppState{
		Revision:                     state.Revision,
		Theme:                        cloneThemeContext(state.Theme),
		Active:                       state.Active.Clone(),
		SemanticActiveCellProjection: state.SemanticActiveCellProjection,
		Bottom:                       state.Bottom.Clone(),
		Geometry:                     state.Geometry,
		Lease:                        state.Lease,
		HistoryEffects: HistoryEffectQueueState{
			NextToken:              effects.NextToken,
			TerminalEpoch:          effects.TerminalEpoch,
			Frozen:                 effects.Frozen,
			ProjectionUnknown:      effects.ProjectionUnknown,
			ReconciliationRequired: effects.ReconciliationRequired,
		},
		LayoutGeneration: state.LayoutGeneration,
	}
}

// terminalSessionClaimedBatchLocked returns a detached, bounded pending prefix
// while the actor mutex still protects the ledger. Bounding aggregate commits,
// rows, spans, and retained text prevents aggregate idle backlog from becoming
// one uninterruptible clone/prepare/write operation. A single claimed token is
// still atomic; the executor acknowledges each prefix and immediately claims
// the next oldest token.
func terminalSessionClaimedBatchLocked(state UIControllerState, token uint64) (*HistoryCommit, []HistoryCommit) {
	ledger := state.HistoryEffects.ledger
	if ledger == nil {
		return nil, nil
	}
	entry, ok := ledger.byToken[token]
	if !ok || entry.State != HistoryCommitInFlight || entry.Commit.LayoutGeneration != state.LayoutGeneration {
		return nil, nil
	}
	claimed := entry.Commit.Clone()
	commits := []HistoryCommit{claimed.Clone()}
	var budget terminalHistoryBatchBudget
	if !budget.admit(entry.Commit) {
		return &claimed, commits
	}
	for _, nextToken := range ledger.orderedTokens() {
		if nextToken <= token {
			continue
		}
		next := ledger.byToken[nextToken]
		if next.State == HistoryCommitPending && next.Commit.LayoutGeneration == state.LayoutGeneration {
			if !budget.admit(next.Commit) {
				break
			}
			commits = append(commits, next.Commit.Clone())
		}
	}
	return &claimed, commits
}

type terminalHistoryBatchBudget struct {
	commits   int
	rows      int
	spans     int
	textBytes int
}

func (b *terminalHistoryBatchBudget) admit(commit HistoryCommit) bool {
	if b == nil || b.commits >= terminalHistoryBatchMaxCommits ||
		len(commit.Lines) > terminalHistoryBatchMaxRows-b.rows {
		return false
	}
	remainingSpans := terminalHistoryBatchMaxSpans - b.spans
	remainingText := terminalHistoryBatchMaxTextBytes - b.textBytes
	addedSpans := 0
	addedText := 0
	for _, line := range commit.Lines {
		if len(line.Style.Role) > remainingText-addedText {
			return false
		}
		addedText += len(line.Style.Role)
		if len(line.Spans) > remainingSpans-addedSpans {
			return false
		}
		for _, span := range line.Spans {
			for _, value := range [...]string{span.Text, span.Link, span.Style.Role} {
				if len(value) > remainingText-addedText {
					return false
				}
				addedText += len(value)
			}
		}
		addedSpans += len(line.Spans)
	}
	b.commits++
	b.rows += len(commit.Lines)
	b.spans += addedSpans
	b.textBytes += addedText
	return true
}

func (c *UIController) terminalSessionHasPending() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.HistoryEffects.HasPending()
}

// terminalSessionHasActionableWork reports whether the executor may continue
// immediately after one transaction: an eligible pending history token or a
// lease/freeze-unblocked recovery obligation. Executor-published actions advance
// the actor revision, so revision comparison cannot distinguish them from
// external frame intents; coalesced presenter requests already own the
// external-wake path through Request().
func (c *UIController) terminalSessionHasActionableWork() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.HistoryEffects.HasPending() || terminalHistoryRecoveryActionable(c.state)
}

// terminalHistoryRecoveryActionable distinguishes a recovery obligation from
// work that may execute now. Alternate-screen leases and frozen history queues
// retain the obligation but must not make the executor spin on Deferred plans.
func terminalHistoryRecoveryActionable(state UIControllerState) bool {
	effects := state.HistoryEffects
	return !state.Lease.Active && !effects.Frozen &&
		(effects.ProjectionUnknown || effects.ReconciliationRequired)
}

// terminalHistoryRecoveryObligationPending reports whether a recovery
// obligation (ProjectionUnknown or ReconciliationRequired) is still present in
// the live controller state. It is used by the executor to detect a
// non-converging scrollback reset: a flush that succeeded at the terminal
// layer but left the recovery obligation in place must be treated like a
// failure for backoff purposes, otherwise the worker re-enters the full
// reset+replay every cycle with no yield (the reported continuous replay).
func (c *UIController) terminalHistoryRecoveryObligationPending() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.HistoryEffects.ProjectionUnknown || c.state.HistoryEffects.ReconciliationRequired
}

func (c *UIController) terminalSessionCommitAckedAndHasPending(token uint64) bool {
	if c == nil || token == 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.state.HistoryEffects.ledger.entry(token)
	return ok && entry.State == HistoryCommitAcked && c.state.HistoryEffects.HasPending()
}
