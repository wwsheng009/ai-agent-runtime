package ui

type terminalSessionScheduleSnapshot struct {
	projectionUnknown bool
	pendingToken      uint64
	pendingGeneration uint64
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
	snapshot := terminalSessionScheduleSnapshot{projectionUnknown: effects.ProjectionUnknown}
	if !effects.HasPending() || effects.ledger == nil {
		return snapshot
	}
	for token, entry := range effects.ledger.byToken {
		if entry.State != HistoryCommitPending || (snapshot.pendingToken != 0 && token >= snapshot.pendingToken) {
			continue
		}
		snapshot.pendingToken = token
		snapshot.pendingGeneration = entry.Commit.LayoutGeneration
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

// terminalSessionClaimedBatchLocked returns detached payloads while the actor
// mutex still protects the ledger. It preserves the token-ordered bootstrap
// rule without cloning every Acked or invalidated ledger entry.
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
	for _, nextToken := range ledger.orderedTokens() {
		if nextToken <= token {
			continue
		}
		next := ledger.byToken[nextToken]
		if next.State == HistoryCommitPending && next.Commit.LayoutGeneration == state.LayoutGeneration {
			commits = append(commits, next.Commit.Clone())
		}
	}
	return &claimed, commits
}

func (c *UIController) terminalSessionHasPending() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.HistoryEffects.HasPending()
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
