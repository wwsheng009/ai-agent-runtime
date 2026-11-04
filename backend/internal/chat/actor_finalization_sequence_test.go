package chat

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

type finalizationSequenceProbe struct {
	*InMemoryRuntimeStore

	mu     sync.Mutex
	seq    int64
	step   int64
	reads  int
	onRead func(read int)
}

func (p *finalizationSequenceProbe) LastEventSeq(_ context.Context, _ string) (int64, error) {
	p.mu.Lock()
	seq := p.seq
	p.seq += p.step
	p.reads++
	read := p.reads
	onRead := p.onRead
	p.mu.Unlock()
	if onRead != nil {
		onRead(read)
	}
	return seq, nil
}

type failingFinalizationSequenceStore struct {
	*InMemoryRuntimeStore
}

func (s *failingFinalizationSequenceStore) LastEventSeq(context.Context, string) (int64, error) {
	return 0, errors.New("sequence unavailable")
}

type injectingFinalizationSequenceStore struct {
	*InMemoryRuntimeStore

	mu     sync.Mutex
	reads  int
	inject func()
}

func (s *injectingFinalizationSequenceStore) LastEventSeq(ctx context.Context, sessionID string) (int64, error) {
	seq, err := s.InMemoryRuntimeStore.LastEventSeq(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.reads++
	read := s.reads
	inject := s.inject
	s.mu.Unlock()
	if read == 1 && inject != nil {
		inject()
	}
	return seq, nil
}

// TestFinalizationSequenceGateObservesQuietWindow covers the normal path:
// obligations are read against a stable durable event high-water mark.
func TestFinalizationSequenceGateObservesQuietWindow(t *testing.T) {
	h := newResumeEpisodeHarness(t)
	h.actor.eventStore = &finalizationSequenceProbe{InMemoryRuntimeStore: h.store}

	gate := h.actor.evaluateFinalizationSequenceGate(context.Background(), "turn-stable", "ep_stable")

	require.True(t, gate.SequenceAvailable)
	require.True(t, gate.SequenceChecked)
	require.True(t, gate.SequenceStable)
	require.Equal(t, 1, gate.SequenceAttempts)
	require.Equal(t, gate.SequenceBefore, gate.SequenceAfter)
	require.Empty(t, gate.SuspendedTurnID)
}

// TestFinalizationSequenceGateRetriesMovingSequence proves that movement during
// the quiet window forces bounded re-reads instead of one optimistic finalization.
func TestFinalizationSequenceGateRetriesMovingSequence(t *testing.T) {
	h := newResumeEpisodeHarness(t)
	h.actor.eventStore = &finalizationSequenceProbe{
		InMemoryRuntimeStore: h.store,
		step:                 1,
	}

	gate := h.actor.evaluateFinalizationSequenceGate(context.Background(), "turn-unstable", "ep_unstable")

	require.True(t, gate.SequenceAvailable)
	require.True(t, gate.SequenceChecked)
	require.False(t, gate.SequenceStable)
	require.Equal(t, finalizationSequenceAttempts, gate.SequenceAttempts)
	require.NotEqual(t, gate.SequenceBefore, gate.SequenceAfter)
	require.Empty(t, gate.SuspendedTurnID)
}

// TestFinalizationSequenceGateRevalidatesNewSuspension covers the race the gate
// exists for: a tail event materializes a required obligation after the first
// suspension read. The second read must win and keep the turn parked.
func TestFinalizationSequenceGateRevalidatesNewSuspension(t *testing.T) {
	h := newResumeEpisodeHarness(t)
	ctx := context.Background()
	probe := &finalizationSequenceProbe{
		InMemoryRuntimeStore: h.store,
		step:                 1,
	}
	probe.onRead = func(read int) {
		if read != 1 {
			return
		}
		require.NoError(t, h.batches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
			TurnID:        "turn-late-obligation",
			EpisodeID:     "ep_late",
			SessionID:     h.session.ID,
			RootScopeID:   h.session.ID,
			ObligationIDs: []string{"batch-late"},
			ResumeQueue:   []string{"batch-late"},
		}))
	}
	h.actor.eventStore = probe

	gate := h.actor.evaluateFinalizationSequenceGate(ctx, "turn-late-obligation", "ep_late")

	require.True(t, gate.SequenceAvailable)
	require.Equal(t, "turn-late-obligation", gate.SuspendedTurnID)
	require.Equal(t, "ep_late", gate.SuspendedEpisodeID)
	require.Equal(t, 1, gate.SequenceAttempts,
		"a materialized suspension must win before entering the next quiet window")
}

// TestFinalizationSequenceGateRevalidatesSuspensionAfterStableSequence covers
// the separate-durable-plane race: the batch ledger can gain a suspension while
// the runtime event sequence stays flat. The post-window obligation read must
// still park the turn.
func TestFinalizationSequenceGateRevalidatesSuspensionAfterStableSequence(t *testing.T) {
	h := newResumeEpisodeHarness(t)
	ctx := context.Background()
	probe := &finalizationSequenceProbe{InMemoryRuntimeStore: h.store}
	probe.onRead = func(read int) {
		if read != 2 {
			return
		}
		require.NoError(t, h.batches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
			TurnID:        "turn-quiet-suspension",
			EpisodeID:     "ep_quiet",
			SessionID:     h.session.ID,
			RootScopeID:   h.session.ID,
			ObligationIDs: []string{"batch-quiet"},
			ResumeQueue:   []string{"batch-quiet"},
		}))
	}
	h.actor.eventStore = probe

	gate := h.actor.evaluateFinalizationSequenceGate(ctx, "turn-quiet-suspension", "ep_quiet")

	require.True(t, gate.SequenceStable, "the event sequence itself did not move")
	require.Equal(t, 1, gate.SequenceAttempts)
	require.Equal(t, "turn-quiet-suspension", gate.SuspendedTurnID)
	require.Equal(t, "ep_quiet", gate.SuspendedEpisodeID)
}

func TestFinalizationSequenceGateReadFailureIsAuditable(t *testing.T) {
	h := newResumeEpisodeHarness(t)
	h.actor.eventStore = &failingFinalizationSequenceStore{InMemoryRuntimeStore: h.store}

	gate := h.actor.evaluateFinalizationSequenceGate(context.Background(), "turn-error", "ep_error")

	require.True(t, gate.SequenceAvailable)
	require.True(t, gate.SequenceChecked)
	require.False(t, gate.SequenceStable)
	require.Contains(t, gate.SequenceError, "sequence unavailable")
	require.Empty(t, gate.SuspendedTurnID)
}

func TestFinalizationSequenceGateSkipsUnsupportedStore(t *testing.T) {
	h := newResumeEpisodeHarness(t)
	h.actor.eventStore = nil

	gate := h.actor.evaluateFinalizationSequenceGate(context.Background(), "turn-legacy", "ep_legacy")

	require.False(t, gate.SequenceAvailable)
	require.False(t, gate.SequenceChecked)
	require.True(t, gate.SequenceStable)
	require.Zero(t, gate.SequenceAttempts)
	require.Empty(t, gate.SuspendedTurnID)
}

// TestFinalizationSequenceGateEmitsRetryAuditEvent exercises the actor wiring:
// one event lands inside the quiet window, the gate retries once, and the turn
// still finalizes with durable audit evidence.
func TestFinalizationSequenceGateEmitsRetryAuditEvent(t *testing.T) {
	h := newResumeEpisodeHarness(t)
	ctx := context.Background()
	injected := false
	h.actor.eventStore = &injectingFinalizationSequenceStore{
		InMemoryRuntimeStore: h.store,
		inject: func() {
			injected = true
			_, err := h.store.AppendEvent(ctx, runtimeevents.Event{
				Type:      "test.finalization.tail",
				SessionID: h.session.ID,
				Payload:   map[string]interface{}{"reason": "simulated tail append"},
			})
			require.NoError(t, err)
		},
	}

	result, err := h.actor.SubmitPrompt(ctx, "finish after a tail event", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, injected, "the test must actually move the durable sequence")

	state := h.actor.StateForInspection()
	require.NotNil(t, state)
	require.Equal(t, ParentTurnPhaseFinalized, state.TurnPhase)
	require.Equal(t, result.TurnID, state.FinalizedTurnID)

	events, err := h.store.ListEvents(ctx, h.session.ID, 0, 0)
	require.NoError(t, err)
	var retryEvent *runtimeevents.Event
	for index := range events {
		if events[index].Type == EventParentTurnFinalizationSequenceRetry {
			retryEvent = &events[index]
			break
		}
	}
	require.NotNil(t, retryEvent, "sequence movement must leave durable audit evidence")
	require.Equal(t, true, retryEvent.Payload["stable"])
	require.Equal(t, 2, retryEvent.Payload["attempts"])
}
