package contextpack

import (
	"context"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionProviderBuildUsesSessionSnapshot(t *testing.T) {
	provider := NewSessionProvider(2)
	input := &Input{
		Session: &SessionSnapshot{
			ID:         "session-1",
			UserID:     "user-1",
			State:      "active",
			Tags:       []string{"alpha", "beta"},
			Context:    map[string]interface{}{"mode": "test"},
			TotalTurns: 3,
			LastAgent:  "agent-1",
			LastSkill:  "skill-1",
			LastModel:  "model-1",
			RecentMessages: []types.Message{
				*types.NewUserMessage("first"),
				*types.NewAssistantMessage("second"),
				*types.NewUserMessage("third"),
			},
			UpdatedAt: time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC),
		},
	}

	result, err := provider.Build(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "session-1", result["id"])
	assert.Equal(t, "user-1", result["user_id"])
	assert.Equal(t, "active", result["state"])
	assert.Equal(t, 3, result["total_turns"])
	assert.Equal(t, "agent-1", result["last_agent"])
	assert.Equal(t, "skill-1", result["last_skill"])
	assert.Equal(t, "model-1", result["last_model"])
	assert.Equal(t, "2026-03-15T10:00:00Z", result["updated_at"])

	tags, ok := result["tags"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"alpha", "beta"}, tags)

	ctxMap, ok := result["context"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "test", ctxMap["mode"])

	recent, ok := result["recent_messages"].([]types.Message)
	require.True(t, ok)
	require.Len(t, recent, 2)
	assert.Equal(t, "second", recent[0].Content)
	assert.Equal(t, "third", recent[1].Content)
}
