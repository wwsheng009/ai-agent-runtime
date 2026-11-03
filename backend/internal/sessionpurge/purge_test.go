package sessionpurge

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunPurgesAcrossRelatedStoresWithProtection(t *testing.T) {
	fixture := newPurgeFixture(t)
	opts := Options{
		OlderThan:      30 * 24 * time.Hour,
		IncludeOrphans: true,
		IncludeTeam:    true,
		DryRun:         true,
		Now:            fixture.now,
	}

	dryReport, err := Run(context.Background(), fixture.paths, opts)
	require.NoError(t, err)
	require.Equal(t, "planned", dryReport.Status)
	require.Equal(t, 5, dryReport.CandidateCount, "old-session/orphan-runtime/orphan-usage/orphan-artifact/old-team")
	require.Equal(t, 6, dryReport.ProtectedCandidates)
	require.Contains(t, dryReport.CandidateSamples, "old-session")
	require.Contains(t, dryReport.CandidateSamples, "orphan-runtime")
	require.Contains(t, dryReport.CandidateSamples, "orphan-usage")
	require.Contains(t, dryReport.CandidateSamples, "orphan-artifact")
	require.Contains(t, dryReport.CandidateSamples, "old-team")
	require.Equal(t, int64(1), queryInt(t, fixture.paths.History, `SELECT COUNT(*) FROM sessions WHERE id='old-session'`))
	require.Equal(t, int64(1), queryInt(t, fixture.paths.Runtime, `SELECT COUNT(*) FROM session_events WHERE session_id='old-session'`))
	require.Equal(t, int64(1), queryInt(t, fixture.paths.Artifacts, `SELECT COUNT(*) FROM blobs WHERE id='blob-old'`))

	opts.DryRun = false
	applyReport, err := Run(context.Background(), fixture.paths, opts)
	require.NoError(t, err)
	require.Equal(t, "pruned", applyReport.Status)
	require.NotEmpty(t, applyReport.Stores)

	// history 级联
	require.Zero(t, queryInt(t, fixture.paths.History, `SELECT COUNT(*) FROM sessions WHERE id='old-session'`))
	require.Zero(t, queryInt(t, fixture.paths.History, `SELECT COUNT(*) FROM session_messages WHERE session_id='old-session'`))
	require.Zero(t, queryInt(t, fixture.paths.History, `SELECT COUNT(*) FROM session_prompt_messages WHERE session_id='old-session'`))
	require.Zero(t, queryInt(t, fixture.paths.History, `SELECT COUNT(*) FROM sessions WHERE id='old-team'`))

	// runtime 级联
	for _, table := range []string{
		"session_events", "session_tool_receipts", "cache_requests",
		"session_runtime_state", "agent_control_mailbox_records",
	} {
		require.Zero(t, queryInt(t, fixture.paths.Runtime, `SELECT COUNT(*) FROM `+table+` WHERE session_id='old-session'`), table)
	}
	require.Zero(t, queryInt(t, fixture.paths.Runtime, `SELECT COUNT(*) FROM session_events WHERE session_id='orphan-runtime'`))
	require.Equal(t, int64(1), queryInt(t, fixture.paths.Runtime, `SELECT COUNT(*) FROM session_runtime_state WHERE session_id='recent-orphan'`))

	// analytics 级联
	require.Zero(t, queryInt(t, fixture.paths.UsageAnalytics, `SELECT COUNT(*) FROM usage_sessions WHERE session_id='old-session'`))
	require.Zero(t, queryInt(t, fixture.paths.UsageAnalytics, `SELECT COUNT(*) FROM usage_sessions WHERE session_id='orphan-usage'`))
	require.Zero(t, queryInt(t, fixture.paths.UsageAnalytics, `SELECT COUNT(*) FROM usage_requests WHERE session_id='old-session'`))
	require.Zero(t, queryInt(t, fixture.paths.UsageAnalytics, `SELECT COUNT(*) FROM usage_subagents WHERE child_session_id='old-session'`))

	// artifacts 级联：旧 checkpoint/blob 删除，共享 blob 保留
	require.Zero(t, queryInt(t, fixture.paths.Artifacts, `SELECT COUNT(*) FROM artifacts WHERE session_id='old-session'`))
	require.Zero(t, queryInt(t, fixture.paths.Artifacts, `SELECT COUNT(*) FROM artifacts WHERE session_id='orphan-artifact'`))
	require.Zero(t, queryInt(t, fixture.paths.Artifacts, `SELECT COUNT(*) FROM checkpoints WHERE id='cp-old'`))
	require.Zero(t, queryInt(t, fixture.paths.Artifacts, `SELECT COUNT(*) FROM blobs WHERE id='blob-old'`))
	require.Equal(t, int64(1), queryInt(t, fixture.paths.Artifacts, `SELECT COUNT(*) FROM blobs WHERE id='blob-shared'`))
	require.Equal(t, int64(1), queryInt(t, fixture.paths.Artifacts, `SELECT COUNT(*) FROM checkpoints WHERE id='cp-recent'`))

	// agent-control 级联
	require.Zero(t, queryInt(t, fixture.paths.AgentControl, `SELECT COUNT(*) FROM agent_control_agents WHERE agent_id='a-old'`))
	require.Zero(t, queryInt(t, fixture.paths.AgentControl, `SELECT COUNT(*) FROM agent_control_agent_wake_events WHERE agent_id='a-old'`))
	require.Equal(t, int64(1), queryInt(t, fixture.paths.AgentControl, `SELECT COUNT(*) FROM agent_control_agents WHERE agent_id='a-child'`))

	// background 级联
	require.Zero(t, queryInt(t, fixture.paths.Background, `SELECT COUNT(*) FROM background_jobs WHERE id='job-old'`))
	require.Zero(t, queryInt(t, fixture.paths.Background, `SELECT COUNT(*) FROM background_job_events WHERE job_id='job-old'`))
	require.Equal(t, int64(1), queryInt(t, fixture.paths.Background, `SELECT COUNT(*) FROM background_jobs WHERE id='job-running'`))

	// schedule 级联
	require.Zero(t, queryInt(t, fixture.paths.Schedule, `SELECT COUNT(*) FROM schedule_tasks WHERE id='task-old'`))
	require.Zero(t, queryInt(t, fixture.paths.Schedule, `SELECT COUNT(*) FROM schedule_delivery_history WHERE schedule_id='task-old'`))
	require.Zero(t, queryInt(t, fixture.paths.Schedule, `SELECT COUNT(*) FROM schedule_delivery_intents WHERE schedule_id='task-old'`))
	require.Zero(t, queryInt(t, fixture.paths.Schedule, `SELECT COUNT(*) FROM schedule_pending_messages WHERE session_id='old-session'`))
	require.Equal(t, int64(1), queryInt(t, fixture.paths.Schedule, `SELECT COUNT(*) FROM schedule_tasks WHERE id='task-protected'`))

	// team 级联
	require.Zero(t, queryInt(t, fixture.paths.Team, `SELECT COUNT(*) FROM teams WHERE id='team-old'`))
	require.Zero(t, queryInt(t, fixture.paths.Team, `SELECT COUNT(*) FROM team_events WHERE team_id='team-old'`))
	require.Zero(t, queryInt(t, fixture.paths.Team, `SELECT COUNT(*) FROM teammates WHERE session_id='old-team'`))
	require.Equal(t, int64(1), queryInt(t, fixture.paths.Team, `SELECT COUNT(*) FROM teams WHERE id='team-protected'`))

	// 保护会话一律保留
	for _, sessionID := range []string{
		"protected-session", "leased-session", "background-protected",
		"schedule-protected", "agent-parent", "team-protected",
	} {
		require.Equal(t, int64(1), queryInt(t, fixture.paths.History, `SELECT COUNT(*) FROM sessions WHERE id='`+sessionID+`'`), sessionID)
	}

	// 幂等：第二次 apply 不应报错，也不应再有可删候选。
	secondReport, err := Run(context.Background(), fixture.paths, opts)
	require.NoError(t, err)
	require.Zero(t, secondReport.CandidateCount)
}

func TestRunSkipsTeamStoreByDefault(t *testing.T) {
	fixture := newPurgeFixture(t)
	report, err := Run(context.Background(), fixture.paths, Options{
		OlderThan:      30 * 24 * time.Hour,
		IncludeOrphans: true,
		IncludeTeam:    false,
		DryRun:         true,
		Now:            fixture.now,
	})
	require.NoError(t, err)
	require.Contains(t, report.Warnings, "team store skipped; pass --include-team to include teams/teammates")
	require.Contains(t, report.CandidateSamples, "old-team")
	for _, store := range report.Stores {
		require.NotEqual(t, "team", store.Name)
	}
}
