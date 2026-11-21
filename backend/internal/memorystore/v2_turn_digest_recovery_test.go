package memorystore

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPreviousTurnDigestMessageSeqEndAfterBacktrack(t *testing.T) {
	for _, status := range []TurnDigestStatus{
		TurnDigestPending, TurnDigestProcessing, TurnDigestDone,
		TurnDigestFailed, TurnDigestSkipped, TurnDigestDeferred,
	} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.Background()
			store, err := New(Config{Root: t.TempDir(), Mode: "on"})
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			generation := int64(1)

			kept, err := store.RecordTurnDigest(ctx, TurnDigest{
				SessionID: "session-backtrack", SessionGeneration: generation, TurnID: "kept",
				MessageSeqEnd: 300, Status: status,
			})
			require.NoError(t, err)
			removed, err := store.RecordTurnDigest(ctx, TurnDigest{
				SessionID: kept.SessionID, SessionGeneration: generation, TurnID: "removed",
				MessageSeqEnd: 500, Status: TurnDigestDone, RollupStatus: TurnDigestRollupDone,
			})
			require.NoError(t, err)
			previousEnd, err := store.PreviousTurnDigestMessageSeqEnd(ctx, kept.SessionID, removed.DigestSeq+1)
			require.NoError(t, err)
			require.EqualValues(t, 500, previousEnd)

			result, err := store.InvalidateSessionRange(ctx, kept.SessionID, SessionRangeInvalidation{
				CutoffMessageSeq:  300,
				BeforeMessageSeq:  500,
				SessionGeneration: &generation,
			}, "backtrack")
			require.NoError(t, err)
			require.EqualValues(t, 1, result.Digests)
			removed, err = store.GetTurnDigestByTurn(ctx, removed.SessionID, removed.TurnID)
			require.NoError(t, err)
			require.Equal(t, TurnDigestInvalidated, removed.Status)
			require.Equal(t, TurnDigestRollupSkipped, removed.RollupStatus)
			surviving, err := store.GetTurnDigestByTurn(ctx, kept.SessionID, kept.TurnID)
			require.NoError(t, err)
			require.Equal(t, status, surviving.Status, "retained coverage must survive in every live state")

			next, err := store.RecordTurnDigest(ctx, TurnDigest{
				SessionID: kept.SessionID, SessionGeneration: generation, TurnID: "new-branch", MessageSeqEnd: 304,
			})
			require.NoError(t, err)
			require.Greater(t, next.DigestSeq, removed.DigestSeq)
			previousEnd, err = store.PreviousTurnDigestMessageSeqEnd(ctx, next.SessionID, next.DigestSeq)
			require.NoError(t, err)
			require.EqualValues(t, 300, previousEnd, "only the retained branch may advance the watermark")

			window, _, err := store.EnsureTurnWindow(ctx, TurnWindow{
				SessionID: next.SessionID, SessionGeneration: generation, WindowIndex: 1,
				StartDigestSeq: next.DigestSeq, EndDigestSeq: next.DigestSeq,
				StartMessageSeq: previousEnd + 1, EndMessageSeq: next.MessageSeqEnd,
				TurnCount: 1, SourceHash: TurnWindowHash([]TurnDigest{next}),
				SourceRevision: next.DigestSeq,
			})
			require.NoError(t, err)
			require.EqualValues(t, 301, window.StartMessageSeq, "the next window must be 301-304, not 501-304")
			require.EqualValues(t, 304, window.EndMessageSeq)
		})
	}
}

func TestPruneTurnDigestsProjectScopeAndRetention(t *testing.T) {
	ctx := context.Background()
	sharedDB, err := filepath.Abs(filepath.Join(t.TempDir(), "shared-turn-digests.db"))
	require.NoError(t, err)
	storeA, err := New(Config{ProjectRoot: t.TempDir(), StorePath: sharedDB, Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = storeA.Close() })
	storeB, err := New(Config{ProjectRoot: t.TempDir(), StorePath: sharedDB, Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = storeB.Close() })
	require.NotEqual(t, storeA.ProjectKey(), storeB.ProjectKey())
	require.Equal(t, storeA.v2Path, storeB.v2Path)

	cutoff := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	type fixture struct {
		digest TurnDigest
		prune  bool
	}
	var fixtures []fixture
	for _, scope := range []Scope{ScopeProject, ScopeUser, ScopeProfile} {
		for _, status := range []TurnDigestStatus{
			TurnDigestPending, TurnDigestProcessing, TurnDigestDone, TurnDigestFailed,
			TurnDigestSkipped, TurnDigestDeferred, TurnDigestInvalidated,
		} {
			for _, rollup := range []TurnDigestRollupStatus{
				TurnDigestRollupPending, TurnDigestRollupDone, TurnDigestRollupFailed, TurnDigestRollupSkipped,
			} {
				terminal := status == TurnDigestDone || status == TurnDigestSkipped || status == TurnDigestInvalidated
				rolledUp := rollup == TurnDigestRollupDone || rollup == TurnDigestRollupSkipped
				fixtures = append(fixtures, fixture{
					digest: TurnDigest{
						Scope: scope, SessionID: "shared-session",
						TurnID: fmt.Sprintf("%s-%s-%s", scope, status, rollup),
						Status: status, RollupStatus: rollup, CreatedAt: cutoff.Add(-time.Hour),
					},
					prune: terminal && rolledUp,
				})
			}
		}
		for _, age := range []time.Duration{0, time.Hour} {
			fixtures = append(fixtures, fixture{digest: TurnDigest{
				Scope: scope, SessionID: "shared-session", TurnID: fmt.Sprintf("%s-retained-age-%s", scope, age),
				Status: TurnDigestDone, RollupStatus: TurnDigestRollupDone, CreatedAt: cutoff.Add(age),
			}})
		}
	}

	seed := func(store *Store) []TurnDigest {
		t.Helper()
		var rows []TurnDigest
		for _, fixture := range fixtures {
			digest, err := store.RecordTurnDigest(ctx, fixture.digest)
			require.NoError(t, err)
			rows = append(rows, digest)
		}
		return rows
	}
	rowsA, rowsB := seed(storeA), seed(storeB)
	assertRows := func(store *Store, rows []TurnDigest, pruned bool) {
		t.Helper()
		for i, before := range rows {
			after, err := store.GetTurnDigestByTurn(ctx, before.SessionID, before.TurnID)
			if pruned && fixtures[i].prune {
				require.ErrorIs(t, err, sql.ErrNoRows, before.TurnID)
				continue
			}
			require.NoError(t, err, before.TurnID)
			require.Equal(t, before, after, "protected row must remain unchanged: %s", before.TurnID)
		}
	}

	// All scopes use the configured project key, like the other digest APIs.
	// For each scope, only three terminal states x two finished rollup states
	// qualify; pending/deferred work, unfinished rollups and recent rows survive.
	pruned, err := storeA.PruneTurnDigests(ctx, cutoff)
	require.NoError(t, err)
	require.EqualValues(t, 18, pruned)
	assertRows(storeA, rowsA, true)
	assertRows(storeB, rowsB, false)
	pruned, err = storeA.PruneTurnDigests(ctx, cutoff)
	require.NoError(t, err)
	require.Zero(t, pruned, "retention must be idempotent")

	pruned, err = storeB.PruneTurnDigests(ctx, cutoff)
	require.NoError(t, err)
	require.EqualValues(t, 18, pruned)
	assertRows(storeA, rowsA, true)
	assertRows(storeB, rowsB, true)
}
