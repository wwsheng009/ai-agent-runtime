package supervision

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeProgressSource struct {
	groups []ProgressGroup
	err    error
	last   ProgressRequest
	calls  int
}

func (f *fakeProgressSource) ListProgress(_ context.Context, req ProgressRequest) ([]ProgressGroup, error) {
	f.calls++
	f.last = req
	if f.err != nil {
		return nil, f.err
	}
	return f.groups, nil
}

// P0-B acceptance: inside the parent turn a batch that stands at 2/3 completed
// with one child still running must be visible as exactly that — not only when
// something fails.
func TestBuildDigest_ProgressRollupVisibleInTurn(t *testing.T) {
	store := testDigestStore(t, "supervision-progress-visible")
	ctx := context.Background()

	_, err := store.UpsertNotification(ctx, testNotification("child-1", 1))
	require.NoError(t, err)

	now := time.Now()
	source := &fakeProgressSource{groups: []ProgressGroup{
		{
			GroupID:        "batch-b-123",
			Total:          3,
			Completed:      2,
			Running:        1,
			LastProgressAt: now.Add(-12 * time.Second),
			RunningTasks: []ProgressTask{{
				TaskID:         "worker-3",
				ChildSessionID: "session-worker-3",
				State:          "running",
				LastProgressAt: now.Add(-12 * time.Second),
				LastMessage:    "bash: go test ./internal/supervision/",
			}},
		},
		{
			GroupID:   "batch-b-456",
			Total:     3,
			Completed: 3,
			Terminal:  true,
		},
	}}

	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		Progress:              source,
	})
	require.NoError(t, err)

	// Lifecycle rows are unchanged: progress is additive, never a replacement.
	require.Equal(t, 1, digest.CriticalUnresolved)
	require.Contains(t, digest.Text, "child-1")

	// The rollup itself ("\nprogress:" — not the unrelated
	// "auto_actions_in_progress:" counter line).
	require.Contains(t, digest.Text, "\nprogress:")
	require.Contains(t, digest.Text, "batch-b-123: 2/3 completed, 1 running")
	require.Contains(t, digest.Text, "worker-3: running; session=session-worker-3; last progress 12s ago; bash: go test ./internal/supervision/")
	require.Contains(t, digest.Text, "batch-b-456: 3/3 completed (terminal")
	// 收敛提示必须是可执行路径：终态 batch 的 done 行是 resolution=closed，
	// evaluator 对这类行只允许 inspect，control_descendant 必然被拒
	// （plan §4.1.1/§7.1）——提示文本只能指向 broker 工具 close_agent。
	require.Contains(t, digest.Text, "close the finished children with close_agent")
	require.NotContains(t, digest.Text, "control_descendant")
	require.Len(t, digest.Progress, 2)
	require.Equal(t, 2, digest.Progress[0].Completed)
	require.Equal(t, 1, digest.Progress[0].Running)
	require.Equal(t, "batch-b-123", digest.Progress[0].GroupID)
	require.False(t, digest.ProgressTruncated)
	require.Equal(t, 1, source.calls, "one projection per digest")
	require.Equal(t, "root-session-1", source.last.RootScopeID)
	require.Equal(t, "root-session-1", source.last.ParentSessionID)
	require.Equal(t, defaultDigestLimit, source.last.RowLimit)
}

// Unwired hosts (P0-B off, or a host with no batch surface yet) must inject
// byte-identical text: the rollup is opt-in.
func TestBuildDigest_ProgressUnwiredKeepsTextIdentical(t *testing.T) {
	store := testDigestStore(t, "supervision-progress-unwired")
	ctx := context.Background()

	_, err := store.UpsertNotification(ctx, testNotification("child-1", 1))
	require.NoError(t, err)

	baseline, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
	})
	require.NoError(t, err)

	empty := &fakeProgressSource{}
	wired, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		Progress:              empty,
	})
	require.NoError(t, err)

	require.NotContains(t, baseline.Text, "\nprogress:")
	require.Equal(t, baseline.Text, wired.Text, "an idle rollup must not change the injected text")
	require.Empty(t, wired.Progress)
	require.False(t, wired.ProgressTruncated)
	require.Equal(t, 1, empty.calls, "the source is consulted, it just has nothing to say")

	summaries, truncated, err := BuildProgressSummary(ctx, nil, ProgressRequest{}, 0, time.Now())
	require.NoError(t, err)
	require.Nil(t, summaries)
	require.False(t, truncated)
}

// A failing projection must never cost the parent its critical rows.
func TestBuildDigest_ProgressSourceFailureIsBestEffort(t *testing.T) {
	store := testDigestStore(t, "supervision-progress-failure")
	ctx := context.Background()

	_, err := store.UpsertNotification(ctx, testNotification("child-1", 1))
	require.NoError(t, err)

	source := &fakeProgressSource{err: errors.New("batch store unavailable")}
	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		Progress:              source,
	})
	require.NoError(t, err)
	require.Equal(t, 1, digest.CriticalUnresolved)
	require.Contains(t, digest.Text, "child-1")
	require.NotContains(t, digest.Text, "\nprogress:")
	require.Empty(t, digest.Progress)
}

// Token budget: both the group list and the per-group running list are bounded,
// and truncation is announced instead of silently dropping rows.
func TestBuildProgressSummary_BoundsGroupsAndTasks(t *testing.T) {
	ctx := context.Background()
	groups := make([]ProgressGroup, 0, 5)
	for i := 0; i < 5; i++ {
		groups = append(groups, ProgressGroup{
			GroupID: "batch-" + string(rune('a'+i)),
			Total:   9,
			Running: 9,
			RunningTasks: []ProgressTask{
				{ChildSessionID: "w-1"},
				{ChildSessionID: "w-2"},
				{ChildSessionID: "w-3"},
				{ChildSessionID: "w-4"},
				{ChildSessionID: "w-5"},
			},
		})
	}
	source := &fakeProgressSource{groups: groups}

	summaries, truncated, err := BuildProgressSummary(ctx, source, ProgressRequest{RowLimit: 2}, 2, time.Now())
	require.NoError(t, err)
	require.True(t, truncated)
	require.Len(t, summaries, 2, "group budget")
	require.Len(t, summaries[0].RunningTasks, 2, "per-group row budget")
	require.True(t, summaries[0].Truncated)

	digest := &Digest{Progress: summaries, ProgressTruncated: truncated}
	text := formatProgressText(digest, time.Now())
	require.Contains(t, text, "more running children omitted")
	require.Contains(t, text, "more batches omitted")
}

func TestFormatProgressAge(t *testing.T) {
	require.Equal(t, "just now", formatProgressAge(200*time.Millisecond))
	require.Equal(t, "12s", formatProgressAge(12*time.Second))
	require.Equal(t, "3m", formatProgressAge(3*time.Minute))
	require.Equal(t, "2h", formatProgressAge(2*time.Hour))
}
