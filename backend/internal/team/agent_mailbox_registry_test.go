package team

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

func TestAgentControlMailboxRegistryProjectsNonSQLiteTeamStore(t *testing.T) {
	ctx := context.Background()
	sqliteStore := newTestStore(t)
	store := teamStoreOnly{Store: sqliteStore}

	teamID, err := sqliteStore.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	taskID := "task-1"
	messageID, err := sqliteStore.InsertMail(ctx, MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate-a",
		TaskID:    &taskID,
		Kind:      "info",
		Body:      "fallback projection body",
		Metadata: map[string]interface{}{
			"source": "fallback",
		},
	})
	require.NoError(t, err)

	registry := NewAgentControlMailboxRegistry(store)
	records, err := registry.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:    agentcontrol.MailboxScopeTeam,
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, messageID, records[0].MessageID)
	require.Equal(t, agentcontrol.MailboxScopeTeam, records[0].Scope)
	require.Equal(t, agentcontrol.WorkflowSpawnTeam, records[0].Workflow)
	require.Equal(t, teamID, records[0].TeamID)
	require.Equal(t, int64(1), records[0].TeamSeq)
	require.Equal(t, "fallback projection body", records[0].Body)
	require.Equal(t, "fallback", records[0].Metadata["source"])

	seq, err := registry.LastAgentControlMailboxRecordSeq(ctx, agentcontrol.MailboxRecordFilter{
		Scope:    agentcontrol.MailboxScopeTeam,
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
	})
	require.NoError(t, err)
	require.Equal(t, records[0].Seq, seq)
}

type teamStoreOnly struct {
	Store
}
