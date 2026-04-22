package toolbroker

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type fakeSessionContextStore struct {
	values map[string]map[string]interface{}
}

func newFakeSessionContextStore() *fakeSessionContextStore {
	return &fakeSessionContextStore{values: make(map[string]map[string]interface{})}
}

func (s *fakeSessionContextStore) LoadContextValue(ctx context.Context, sessionID, key string) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.values == nil {
		return nil, nil
	}
	if bySession := s.values[sessionID]; bySession != nil {
		return bySession[key], nil
	}
	return nil, nil
}

func (s *fakeSessionContextStore) SaveContextValue(ctx context.Context, sessionID, key string, value interface{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.values == nil {
		s.values = make(map[string]map[string]interface{})
	}
	if s.values[sessionID] == nil {
		s.values[sessionID] = make(map[string]interface{})
	}
	s.values[sessionID][key] = value
	return nil
}

func TestBrokerBackgroundTaskPersistsStableJobAliasAcrossBrokerInstances(t *testing.T) {
	ctx := context.Background()
	store := newFakeSessionContextStore()
	manager := background.NewManager(background.Config{
		LogDir: filepath.Join(t.TempDir(), "logs"),
	})

	brokerOne := &Broker{
		Background:          manager,
		SessionContextStore: store,
	}
	raw, meta, err := brokerOne.ExecuteToolCall(ctx, "parent-session", types.ToolCall{
		ID:   "call-bg-1",
		Name: ToolBackgroundTask,
		Args: map[string]interface{}{
			"command": "echo ok",
		},
	})
	require.NoError(t, err)

	result, ok := raw.(BackgroundTaskResult)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(result.JobID, backgroundJobAliasPrefix), "expected stable alias, got %q", result.JobID)
	actualJobID, ok := meta["job_id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, actualJobID)
	assert.NotEqual(t, actualJobID, result.JobID)

	brokerTwo := &Broker{
		Background:          manager,
		SessionContextStore: store,
	}
	outputRaw, outputMeta, err := brokerTwo.Execute(ctx, "parent-session", ToolTaskOutput, map[string]interface{}{
		"job_id": result.JobID,
	})
	require.NoError(t, err)

	output, ok := outputRaw.(TaskOutputResult)
	require.True(t, ok)
	assert.Equal(t, result.JobID, output.JobID)
	assert.Equal(t, actualJobID, outputMeta["job_id"])
	assert.Equal(t, result.JobID, outputMeta["job_alias"])
}

func TestBrokerAgentAliasesPersistAcrossBrokerInstances(t *testing.T) {
	ctx := context.Background()
	store := newFakeSessionContextStore()
	controller := &fakeAgentSessionController{}

	brokerOne := &Broker{
		AgentSessions:       controller,
		SessionContextStore: store,
	}
	spawnRaw, spawnMeta, err := brokerOne.ExecuteToolCall(ctx, "parent-session", types.ToolCall{
		ID:   "call-agent-1",
		Name: ToolSpawnAgent,
		Args: map[string]interface{}{
			"message": "inspect repo",
		},
	})
	require.NoError(t, err)

	spawnResult, ok := spawnRaw.(*AgentStatusResult)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(spawnResult.SessionID, agentSessionAliasPrefix), "expected stable alias, got %q", spawnResult.SessionID)
	assert.Equal(t, "child-1", spawnMeta["session_id"])
	assert.Equal(t, spawnResult.SessionID, spawnMeta["session_alias"])

	brokerTwo := &Broker{
		AgentSessions:       controller,
		SessionContextStore: store,
	}
	_, sendMeta, err := brokerTwo.Execute(ctx, "parent-session", ToolSendInput, map[string]interface{}{
		"id":      spawnResult.SessionID,
		"message": "continue",
	})
	require.NoError(t, err)
	assert.Equal(t, "child-1", controller.lastInput.ID)
	assert.Equal(t, spawnResult.SessionID, sendMeta["session_alias"])

	waitRaw, waitMeta, err := brokerTwo.Execute(ctx, "parent-session", ToolWaitAgent, map[string]interface{}{
		"id":         spawnResult.SessionID,
		"timeout_ms": 1000,
	})
	require.NoError(t, err)
	waitResult, ok := waitRaw.(*AgentWaitResult)
	require.True(t, ok)
	assert.Equal(t, "child-1", controller.lastWait.ID)
	assert.Equal(t, spawnResult.SessionID, waitResult.MatchedSessionID)
	assert.Equal(t, "child-1", waitMeta["session_id"])
	assert.Equal(t, spawnResult.SessionID, waitMeta["session_alias"])

	eventsRaw, eventsMeta, err := brokerTwo.Execute(ctx, "parent-session", ToolReadAgentEvents, map[string]interface{}{
		"id": spawnResult.SessionID,
	})
	require.NoError(t, err)
	eventsResult, ok := eventsRaw.(*AgentEventsResult)
	require.True(t, ok)
	assert.Equal(t, "child-1", controller.lastRead.ID)
	assert.Equal(t, spawnResult.SessionID, eventsResult.SessionID)
	require.Len(t, eventsResult.Events, 1)
	assert.Equal(t, spawnResult.SessionID, eventsResult.Events[0].SessionID)
	assert.Equal(t, "child-1", eventsMeta["session_id"])
	assert.Equal(t, spawnResult.SessionID, eventsMeta["session_alias"])

	_, closeMeta, err := brokerTwo.Execute(ctx, "parent-session", ToolCloseAgent, map[string]interface{}{
		"id": spawnResult.SessionID,
	})
	require.NoError(t, err)
	assert.Equal(t, "child-1", controller.lastClose)
	assert.Equal(t, spawnResult.SessionID, closeMeta["session_alias"])

	_, resumeMeta, err := brokerTwo.Execute(ctx, "parent-session", ToolResumeAgent, map[string]interface{}{
		"id": spawnResult.SessionID,
	})
	require.NoError(t, err)
	assert.Equal(t, "child-1", controller.lastResume)
	assert.Equal(t, spawnResult.SessionID, resumeMeta["session_alias"])
}

func TestBrokerAgentCacheSafeSummaryOmitsDynamicTurnIDs(t *testing.T) {
	ctx := context.Background()
	store := newFakeSessionContextStore()
	controller := &fakeAgentSessionController{}
	broker := &Broker{
		AgentSessions:       controller,
		SessionContextStore: store,
	}

	raw, meta, err := broker.ExecuteToolCall(ctx, "parent-session", types.ToolCall{
		ID:   "call-agent-summary-1",
		Name: ToolSpawnAgent,
		Args: map[string]interface{}{
			"message": "inspect repo",
		},
	})
	require.NoError(t, err)

	envelope, err := output.NewGateway(nil).Process(ctx, output.RawToolResult{
		SessionID:  "parent-session",
		ToolName:   ToolSpawnAgent,
		ToolCallID: "call-agent-summary-1",
		Content:    raw,
		Metadata: map[string]interface{}{
			"tool_metadata": meta,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, envelope)
	rendered := envelope.Render()
	assert.Contains(t, rendered, "Child agent")
	assert.NotContains(t, rendered, "turn-dynamic-123")
	assert.NotContains(t, rendered, "toolcall-dynamic-456")
}
