package team

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

func TestAgentControlMailboxWakeWatchesTeamMailbox(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(ctx, Team{ID: "team-1"})
	require.NoError(t, err)

	source := NewAgentControlMailboxWake(store)
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	wake, unwatch := source.WatchAgentControlMailboxWake(watchCtx, agentcontrol.MailboxWakeFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
	})
	defer unwatch()

	taskID := "task-1"
	messageID, err := store.InsertMail(ctx, MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "orchestrator",
		TaskID:    &taskID,
		Kind:      "wake",
		Body:      "ready task added",
	})
	require.NoError(t, err)

	select {
	case event := <-wake:
		require.Equal(t, int64(1), event.Seq)
		require.Equal(t, agentcontrol.WorkflowSpawnTeam, event.Workflow)
		require.Equal(t, teamID, event.TeamID)
		require.Equal(t, messageID, event.MessageID)
		require.Equal(t, "wake", event.Kind)
		require.Equal(t, "lead", event.FromAgent)
		require.Equal(t, "orchestrator", event.ToAgent)
		require.Equal(t, taskID, event.TaskID)
	case <-time.After(time.Second):
		t.Fatal("expected AgentControl mailbox wake event")
	}

	seq, err := source.LastAgentControlMailboxWakeSeq(ctx, agentcontrol.MailboxWakeFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), seq)

	unsupportedSeq, err := source.LastAgentControlMailboxWakeSeq(ctx, agentcontrol.MailboxWakeFilter{
		Workflow: agentcontrol.WorkflowSpawnAgent,
		TeamID:   teamID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(0), unsupportedSeq)
}
