package toolbroker

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func TestBrokerSpawnTeamCacheSafeSummaryIncludesTeamIDAndOmitsTaskIDsInEnvelope(t *testing.T) {
	store := newTeamStore(t)
	broker := &Broker{TeamStore: store}

	raw, meta, err := broker.Execute(context.Background(), "session-1", ToolSpawnTeam, map[string]interface{}{
		"auto_start": false,
		"teammates": []interface{}{
			map[string]interface{}{"name": "planner"},
		},
		"tasks": []interface{}{
			map[string]interface{}{"title": "draft plan", "goal": "create task plan"},
		},
	})
	require.NoError(t, err)

	result, ok := raw.(SpawnTeamResult)
	require.True(t, ok)
	require.NotEmpty(t, result.TeamID)
	summary, ok := meta[cacheSafeSummaryMetadataKey].(string)
	require.True(t, ok)
	assert.Contains(t, summary, "Created team run")
	assert.Contains(t, summary, result.TeamID)
	for _, taskID := range result.TaskIDs {
		assert.NotContains(t, summary, taskID)
	}

	envelope, err := output.NewGateway(nil).Process(context.Background(), output.RawToolResult{
		SessionID:  "session-1",
		ToolName:   ToolSpawnTeam,
		ToolCallID: "call-team-1",
		Content:    raw,
		Metadata: map[string]interface{}{
			"tool_metadata": meta,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, envelope)
	assert.Contains(t, envelope.Render(), "Created team run")
	assert.Contains(t, envelope.Render(), result.TeamID)
	for _, taskID := range result.TaskIDs {
		assert.NotContains(t, envelope.Render(), taskID)
	}
}

func TestBrokerReadMailboxDigestCacheSafeSummaryPreservesDigestWithoutMessageIDs(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()

	teamID, err := store.CreateTeam(ctx, team.Team{})
	require.NoError(t, err)
	_, err = store.InsertMail(ctx, team.MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "*",
		Kind:      "info",
		Body:      "broadcast update",
	})
	require.NoError(t, err)
	directID, err := store.InsertMail(ctx, team.MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate-1",
		Kind:      "question",
		Body:      "direct ask",
	})
	require.NoError(t, err)

	broker := &Broker{TeamStore: store}
	runCtx := team.WithRunMeta(ctx, &team.RunMeta{
		Team: &team.TeamRunMeta{
			TeamID:  teamID,
			AgentID: "mate-1",
		},
	})

	raw, meta, err := broker.Execute(runCtx, "session-1", ToolReadMailboxDigest, map[string]interface{}{"limit": 5})
	require.NoError(t, err)

	result, ok := raw.(ReadMailboxDigestResult)
	require.True(t, ok)
	require.Contains(t, result.MessageIDs, directID)
	summary, ok := meta[cacheSafeSummaryMetadataKey].(string)
	require.True(t, ok)
	assert.Contains(t, summary, "broadcast update")
	assert.Contains(t, summary, "direct ask")
	assert.NotContains(t, summary, directID)

	envelope, err := output.NewGateway(nil).Process(ctx, output.RawToolResult{
		SessionID:  "session-1",
		ToolName:   ToolReadMailboxDigest,
		ToolCallID: "call-mail-1",
		Content:    raw,
		Metadata: map[string]interface{}{
			"tool_metadata": meta,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, envelope)
	rendered := envelope.Render()
	assert.True(t, strings.Contains(rendered, "broadcast update") || strings.Contains(rendered, "direct ask"))
	assert.NotContains(t, rendered, directID)
}
