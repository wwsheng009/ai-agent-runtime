package toolbroker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

type teamStoreWithoutEventSequence struct {
	team.Store
}

type teamStoreWithFailingEventSequence struct {
	team.Store
}

func (teamStoreWithFailingEventSequence) LastTeamEventSeq(context.Context, string) (int64, error) {
	return 0, errors.New("event sequence read failed")
}

func TestBrokerSpawnTeamCacheSafeSummaryIncludesTeamIDAndOmitsTaskIDsInEnvelope(t *testing.T) {
	store := newTeamStore(t)
	broker := &Broker{TeamStore: store}

	raw, meta, err := broker.Execute(context.Background(), "session-1", ToolTeamSpawn, map[string]interface{}{
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
	assert.Equal(t, ToolTeamSpawn, result.Operation)
	assert.Equal(t, result.TeamID, result.RunID)
	assert.Equal(t, "legacy_team_view", result.CompatibilityMode)
	assert.False(t, result.AutoStartRequested)
	assert.False(t, result.AutoStarted)
	assert.Equal(t, "not_requested", result.StartResult.Status)
	assert.Equal(t, "not_run", result.Preflight.Status)
	assert.Equal(t, "unsupported", result.Preflight.Capability)
	assert.Equal(t, "unsupported", result.Capabilities["product_run_resource"])
	assert.Equal(t, "unsupported", result.Capabilities["product_run_preflight"])
	assert.Equal(t, "unsupported", result.Capabilities["independent_run_history"])
	assert.Equal(t, "supported", result.Capabilities["team_event_cursor"])
	assert.Equal(t, result.EventSeq, meta["event_seq"])
	summary, ok := meta[cacheSafeSummaryMetadataKey].(string)
	require.True(t, ok)
	assert.Contains(t, summary, "Created Team")
	assert.Contains(t, summary, "compatibility Run ID")
	assert.Contains(t, summary, "mode=legacy_team_view")
	assert.Contains(t, summary, "Preflight status=not_run")
	assert.Contains(t, summary, "Independent product Run=unsupported")
	assert.Contains(t, summary, "Run history=unsupported")
	assert.Contains(t, summary, "Team event cursor=supported")
	assert.Contains(t, summary, result.TeamID)
	for _, taskID := range result.TaskIDs {
		assert.NotContains(t, summary, taskID)
	}

	envelope, err := output.NewGateway(nil).Process(context.Background(), output.RawToolResult{
		SessionID:  "session-1",
		ToolName:   ToolTeamSpawn,
		ToolCallID: "call-team-1",
		Content:    raw,
		Metadata: map[string]interface{}{
			"tool_metadata": meta,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, envelope)
	rendered := envelope.Render()
	assert.Contains(t, rendered, "Created Team")
	assert.Contains(t, rendered, "compatibility Run ID")
	assert.Contains(t, rendered, result.TeamID)
	for _, taskID := range result.TaskIDs {
		assert.NotContains(t, rendered, taskID)
	}
}

func TestBrokerSpawnTeamCacheSafeSummaryDoesNotSuggestCursorWhenUnsupported(t *testing.T) {
	baseStore := newTeamStore(t)
	broker := &Broker{TeamStore: teamStoreWithoutEventSequence{Store: baseStore}}

	raw, meta, err := broker.Execute(context.Background(), "session-1", ToolTeamSpawn, map[string]interface{}{
		"team_id":    "team-no-cursor",
		"auto_start": false,
	})
	require.NoError(t, err)
	result, ok := raw.(SpawnTeamResult)
	require.True(t, ok)
	assert.Equal(t, "unsupported", result.Capabilities["team_event_cursor"])
	assert.NotContains(t, meta, "event_seq")

	summary, ok := meta[cacheSafeSummaryMetadataKey].(string)
	require.True(t, ok)
	assert.Contains(t, summary, "Team event cursor=unsupported")
	assert.Contains(t, summary, "restart observation with team_wait(team_id=team-no-cursor)")
	assert.NotContains(t, summary, "after_seq=")
}

func TestBrokerSpawnTeamMarksFailedCursorReadUnavailable(t *testing.T) {
	baseStore := newTeamStore(t)
	broker := &Broker{TeamStore: teamStoreWithFailingEventSequence{Store: baseStore}}

	raw, meta, err := broker.Execute(context.Background(), "session-1", ToolTeamSpawn, map[string]interface{}{
		"team_id":    "team-cursor-read-failed",
		"auto_start": false,
	})
	require.NoError(t, err)
	result, ok := raw.(SpawnTeamResult)
	require.True(t, ok)
	assert.Equal(t, "unavailable", result.Capabilities["team_event_cursor"])
	assert.NotContains(t, meta, "event_seq")
	assert.Equal(t, result.Capabilities, meta["capabilities"])

	summary, ok := meta[cacheSafeSummaryMetadataKey].(string)
	require.True(t, ok)
	assert.Contains(t, summary, "Team event cursor=unavailable")
	assert.NotContains(t, summary, "after_seq=")
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

	raw, meta, err := broker.Execute(runCtx, "session-1", ToolTeamMailbox, map[string]interface{}{"limit": 5})
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
		ToolName:   ToolTeamMailbox,
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
