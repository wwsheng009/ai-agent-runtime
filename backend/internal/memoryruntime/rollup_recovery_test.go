package memoryruntime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestRecoveryRollupOnlyWorkSurvivesFailureAndRestart(t *testing.T) {
	for _, trigger := range []Trigger{TriggerStartup, TriggerIdle} {
		for _, providerFailure := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/provider_failure=%t", trigger, providerFailure), func(t *testing.T) {
				ctx := context.Background()
				rt, source, store := newRecoveryRuntime(t, 2, 4)
				source.session.SessionGeneration = 1
				rt.Config.TurnDigestWindowSummaryEnabled = true
				rt.Config.TurnDigestWindowTurns = 2
				extractCalls := 0
				extractor := recoveryExtractorFunc(func(context.Context, SessionRef, Transcript) (ExtractionResult, error) {
					extractCalls++
					return ExtractionResult{}, nil
				})
				rt.Extractor = extractor
				summarizer := &fakeWindowSummarizer{err: errors.New("rollup provider unavailable")}
				rt.WindowSummarizer = summarizer
				digest, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
					SessionID: source.session.ID, SessionGeneration: 1, TurnID: "turn", MessageSeqEnd: 2,
					Outcome: "success", AssistantText: "a durable decision",
				})
				require.NoError(t, err)
				if providerFailure {
					first, err := rt.RunSessionEnd(ctx, source.session.ID)
					require.NoError(t, err)
					require.Equal(t, 1, first.RollupFailed)
					require.Equal(t, 1, summarizer.calls)
				} else {
					// Crash boundary: extraction and its acknowledgement are
					// committed, but no L1 window/job has been created yet.
					first, err := rt.RunSession(ctx, TriggerSessionEnd, source.session.ID)
					require.NoError(t, err)
					require.True(t, first.Completed)
					_, err = store.MarkTurnDigestsProcessed(ctx, source.session.ID, digest.DigestSeq, "")
					require.NoError(t, err)
				}
				pending, err := store.ListPendingTurnDigestSessions(ctx, "", 20)
				require.NoError(t, err)
				require.Empty(t, pending, "extraction discovery cannot find this remaining L1 work")

				memoryCfg := memorystore.Config{Root: filepath.Dir(store.DBPath()), ProjectKey: store.ProjectKey(), Mode: "on"}
				now, cfg := rt.now(), rt.Config
				require.NoError(t, store.Close())
				store, err = memorystore.New(memoryCfg)
				require.NoError(t, err)
				t.Cleanup(func() { _ = store.Close() })
				rt = New(store, source, extractor, fakeModes{}, cfg)
				rt.Clock = func() time.Time { return now }
				summarizer.err = nil
				summarizer.result = WindowSummary{Text: "Recovered durable window decision"}
				rt.WindowSummarizer = summarizer
				if providerFailure {
					backoff, err := rt.RunPendingTurnDigests(ctx, trigger)
					require.NoError(t, err)
					require.Zero(t, backoff.RolledUp)
					require.Equal(t, 1, summarizer.calls, "rollup retry_at remains authoritative")
				}
				rt.Clock = func() time.Time { return now.Add(time.Minute) }
				recovered, err := rt.RunPendingTurnDigests(ctx, trigger)
				require.NoError(t, err)
				require.Empty(t, recovered.Errors)
				require.Zero(t, recovered.Processed, "rollup recovery must not re-extract canonical source")
				require.Equal(t, 1, recovered.RolledUp)
				require.Equal(t, 1, extractCalls)
				ready, err := store.ListTurnDigestsForRollup(ctx, source.session.ID, 20)
				require.NoError(t, err)
				require.Empty(t, ready)
				windows, err := store.ListTurnWindows(ctx, source.session.ID, 20)
				require.NoError(t, err)
				require.Len(t, windows, 1)
				require.Equal(t, memorystore.TurnWindowDone, windows[0].Status)
				require.EqualValues(t, 1, windows[0].SummaryRevision)
				records, err := store.ListRecords(ctx, memorystore.RecordFilter{Status: memorystore.StatusCandidate, Limit: 20})
				require.NoError(t, err)
				require.Len(t, records.Items, 1)
				require.Equal(t, "turn_window", records.Items[0].SourceKind)
				calls := summarizer.calls
				again, err := rt.RunPendingTurnDigests(ctx, trigger)
				require.NoError(t, err)
				require.Zero(t, again.RolledUp)
				require.Equal(t, calls, summarizer.calls)
			})
		}
	}
}

func TestRecoveryRollupRespectsEligibility(t *testing.T) {
	for _, gate := range []string{"mode_off", "generation_off", "digest_off", "workspace", "subagent", "ephemeral", "disabled", "polluted", "too_old", "startup_active", "idle_active"} {
		t.Run(gate, func(t *testing.T) {
			ctx := context.Background()
			rt, source, store := newRecoveryRuntime(t, 2, 4)
			rt.Config.TurnDigestWindowSummaryEnabled = true
			summarizer := &fakeWindowSummarizer{result: WindowSummary{Text: "must remain gated"}}
			rt.WindowSummarizer = summarizer
			_, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
				SessionID: source.session.ID, TurnID: "turn", MessageSeqEnd: 2,
				Outcome: "success", AssistantText: "answer", Status: memorystore.TurnDigestDone,
			})
			require.NoError(t, err)
			trigger := TriggerStartup
			switch gate {
			case "mode_off":
				rt.Config.Mode = "off"
			case "generation_off":
				rt.Config.GenerationDisabled = true
			case "digest_off":
				rt.Config.TurnDigestEnabled = false
			case "workspace":
				source.session.WorkspacePath = "/other"
			case "subagent":
				source.session.IsSubagent = true
			case "ephemeral":
				source.session.Ephemeral = true
			case "disabled":
				rt.Modes = fakeModes{source.session.ID: memorystore.SessionModeDisabled}
			case "polluted":
				rt.Modes = fakeModes{source.session.ID: memorystore.SessionModePolluted}
			case "too_old":
				source.session.LastActivityAt = rt.now().Add(-rt.Config.MaxRolloutAge - time.Hour)
			case "startup_active":
				source.session.State = "active"
			case "idle_active":
				trigger = TriggerIdle
				source.session.State = "active"
				source.session.LastActivityAt = rt.now()
			}
			result, err := rt.RunPendingTurnDigests(ctx, trigger)
			require.NoError(t, err)
			require.Zero(t, result.RolledUp)
			require.Zero(t, summarizer.calls)
			digest, err := store.GetTurnDigestByTurn(ctx, source.session.ID, "turn")
			require.NoError(t, err)
			require.Equal(t, memorystore.TurnDigestRollupPending, digest.RollupStatus)
		})
	}
}

func TestRecoveryRollupDiscoveryAdvancesPastIneligibleFirstPage(t *testing.T) {
	ctx := context.Background()
	rt, _, store := newRecoveryRuntime(t, 2, 4)
	rt.Config.MaxSessionsPerRun = 1
	rt.Config.TurnDigestWindowSummaryEnabled = true
	source := &fakeSource{}
	modes := fakeModes{}
	for index := 0; index < 12; index++ {
		id := fmt.Sprintf("session-%02d", index)
		source.sessions = append(source.sessions, SessionRef{
			ID: id, SessionGeneration: 1, WorkspacePath: "/workspace", State: "closed", LastActivityAt: rt.now(), CanonicalCount: 2,
		})
		if index < 11 {
			modes[id] = memorystore.SessionModeDisabled
		}
		_, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
			SessionID: id, SessionGeneration: 1, TurnID: "turn", MessageSeqEnd: 2, Outcome: "success",
			AssistantText: "answer", Status: memorystore.TurnDigestDone,
		})
		require.NoError(t, err)
	}
	rt.Source, rt.Modes = source, modes
	summarizer := &fakeWindowSummarizer{result: WindowSummary{Text: "Unblocked late session summary"}}
	rt.WindowSummarizer = summarizer
	first, err := rt.RunPendingTurnDigests(ctx, TriggerStartup)
	require.NoError(t, err)
	require.Zero(t, first.RolledUp)
	require.Zero(t, summarizer.calls)
	second, err := rt.RunPendingTurnDigests(ctx, TriggerStartup)
	require.NoError(t, err)
	require.Empty(t, second.Errors)
	require.Equal(t, 1, second.RolledUp)
	require.Equal(t, 1, summarizer.calls)
	require.Equal(t, "session-11", summarizer.inputs[0].Session.ID)
}

func TestRecoveryIdleRunRepairsRollupOnlyWork(t *testing.T) {
	ctx := context.Background()
	rt, source, store := newRecoveryRuntime(t, 2, 4)
	source.session.SessionGeneration = 1
	rt.Config.TurnDigestWindowSummaryEnabled = true
	summarizer := &fakeWindowSummarizer{result: WindowSummary{Text: "Independent idle rollup summary"}}
	rt.WindowSummarizer = summarizer
	_, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
		SessionID: source.session.ID, SessionGeneration: 1, TurnID: "turn", MessageSeqEnd: 2, Outcome: "success",
		AssistantText: "answer", Status: memorystore.TurnDigestDone,
	})
	require.NoError(t, err)
	result, err := rt.Run(ctx, TriggerIdle)
	require.NoError(t, err)
	require.Empty(t, result.Errors)
	require.Zero(t, result.Processed)
	require.Equal(t, 1, result.RolledUp)
	require.Zero(t, source.readCalls)
}

func TestRecoveryDisabledSummaryReleasesAcknowledgedDigests(t *testing.T) {
	ctx := context.Background()
	rt, source, store := newRecoveryRuntime(t, 2, 4)
	_, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
		SessionID: source.session.ID, TurnID: "turn", MessageSeqEnd: 2,
		Outcome: "success", Status: memorystore.TurnDigestDone,
	})
	require.NoError(t, err)
	_, err = rt.RunPendingTurnDigests(ctx, TriggerStartup)
	require.NoError(t, err)
	digest, err := store.GetTurnDigestByTurn(ctx, source.session.ID, "turn")
	require.NoError(t, err)
	require.Equal(t, memorystore.TurnDigestRollupSkipped, digest.RollupStatus)
}
