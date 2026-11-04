package background

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestJobOriginContextRoundTrip(t *testing.T) {
	origin := JobOrigin{
		RootScopeID:        "root",
		ParentSessionID:    "sess",
		ParentTurnID:       "turn",
		ParentEpisodeID:    "ep",
		SettlementID:       "settle",
		SettlementCohortID: "cohort",
		FinalizationPolicy: string(JobPolicyRequired),
	}
	ctx := WithJobOrigin(context.Background(), origin)
	require.Equal(t, origin, JobOriginFromContext(ctx))
}

func TestSubmitShellPersistsJobOrigin(t *testing.T) {
	manager := NewManager(Config{})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	ctx := WithJobOrigin(context.Background(), JobOrigin{
		RootScopeID:        "root",
		ParentSessionID:    "sess",
		ParentTurnID:       "turn-origin",
		ParentEpisodeID:    "ep-origin",
		SettlementID:       "settle-origin",
		SettlementCohortID: "cohort-origin",
		FinalizationPolicy: string(JobPolicyDetached),
	})
	job, err := manager.SubmitShell(ctx, "sess", BackgroundTaskArgs{Command: shellEchoCommand("origin")})
	require.NoError(t, err)
	require.NotNil(t, job)

	origin := JobOriginFromMetadata(job.Metadata)
	require.Equal(t, "root", origin.RootScopeID)
	require.Equal(t, "sess", origin.ParentSessionID)
	require.Equal(t, "turn-origin", origin.ParentTurnID)
	require.Equal(t, "ep-origin", origin.ParentEpisodeID)
	require.Equal(t, string(JobPolicyDetached), origin.FinalizationPolicy)
}

func TestSubmitShellWithoutOriginKeepsLegacyMetadata(t *testing.T) {
	manager := NewManager(Config{})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	job, err := manager.SubmitShell(context.Background(), "sess-legacy", BackgroundTaskArgs{Command: shellEchoCommand("legacy")})
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Empty(t, JobOriginFromMetadata(job.Metadata).ParentTurnID)
	require.Empty(t, JobOriginFromMetadata(job.Metadata).ParentEpisodeID)
}

func TestParseJobFinalizationPolicy(t *testing.T) {
	cases := []struct {
		in   string
		want JobFinalizationPolicy
		ok   bool
	}{
		{in: "", want: JobPolicyDetached, ok: true},
		{in: "detached", want: JobPolicyDetached, ok: true},
		{in: " REQUIRED ", want: JobPolicyRequired, ok: true},
		{in: "explicit-followup", want: JobPolicyExplicitFollowup, ok: true},
		{in: "explicit_followup", want: JobPolicyExplicitFollowup, ok: true},
		{in: "sometimes", ok: false},
	}
	for _, tc := range cases {
		got, ok := ParseJobFinalizationPolicy(tc.in)
		require.Equalf(t, tc.ok, ok, "ParseJobFinalizationPolicy(%q)", tc.in)
		require.Equalf(t, tc.want, got, "ParseJobFinalizationPolicy(%q)", tc.in)
	}
}

func TestManagerBackgroundJobTerminalAndDurability(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "background.sqlite")
	manager := NewManager(Config{StorePath: path})
	t.Cleanup(func() { _ = manager.Close() })
	require.True(t, manager.JobStoreDurable(), "a path-backed job store must be resumable after restart")

	job, err := manager.SubmitShell(ctx, "sess-obligation", BackgroundTaskArgs{Command: shellEchoCommand("obligation")})
	require.NoError(t, err)

	terminal, found, err := manager.BackgroundJobTerminal(ctx, job.ID)
	require.NoError(t, err)
	require.True(t, found)

	done, timedOut, err := manager.WaitJob(ctx, job.ID, 10*time.Second)
	require.NoError(t, err)
	require.False(t, timedOut)
	require.NotNil(t, done)
	terminal, found, err = manager.BackgroundJobTerminal(ctx, job.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, terminal)

	terminal, found, err = manager.BackgroundJobTerminal(ctx, "job-missing")
	require.NoError(t, err)
	require.False(t, found)
	require.False(t, terminal)
}

func TestManagerJobStoreDurability(t *testing.T) {
	memory := NewManager(Config{})
	t.Cleanup(func() { _ = memory.Close() })
	require.False(t, memory.JobStoreDurable(), "the in-memory fallback cannot settle a durable suspension after restart")

	durable := NewManager(Config{StorePath: filepath.Join(t.TempDir(), "background.sqlite")})
	t.Cleanup(func() { _ = durable.Close() })
	require.True(t, durable.JobStoreDurable())
}
