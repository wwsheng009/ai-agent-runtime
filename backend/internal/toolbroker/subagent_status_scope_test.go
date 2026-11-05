package toolbroker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

func TestParseSubagentStatusArgsScope(t *testing.T) {
	parsed, err := parseSubagentStatusArgs(map[string]interface{}{"scope": "current_turn"})
	require.NoError(t, err)
	require.Equal(t, "current_turn", parsed.Scope)

	parsed, err = parseSubagentStatusArgs(map[string]interface{}{})
	require.NoError(t, err)
	require.Equal(t, "auto", parsed.Scope)

	parsed, err = parseSubagentStatusArgs(map[string]interface{}{"scope": "all"})
	require.NoError(t, err)
	require.Equal(t, "all", parsed.Scope)

	parsed, err = parseSubagentStatusArgs(map[string]interface{}{"scope": "current_batch", "batch_id": "batch_1"})
	require.NoError(t, err)
	require.Equal(t, "current_batch", parsed.Scope)
	require.Equal(t, "batch_1", parsed.BatchID)

	_, err = parseSubagentStatusArgs(map[string]interface{}{"scope": "current_batch"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "batch_id is required")

	_, err = parseSubagentStatusArgs(map[string]interface{}{"scope": "historical"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "scope must be auto, all, current_turn or current_batch")
}

func TestSubagentStatusCurrentTurnSkipsHistoricalDigest(t *testing.T) {
	controller := &fakeSupervisionController{
		snapshot: &supervision.Snapshot{
			Descendants: []supervision.SnapshotItem{{
				ID:               "batch_1",
				SupervisionState: supervision.SupervisionRunning,
			}},
		},
	}
	broker := &Broker{Supervision: controller}
	raw, _, err := broker.Execute(context.Background(), "parent-1", ToolSubagentStatus, map[string]interface{}{
		"scope":          "current_turn",
		"include_digest": true,
	})
	require.NoError(t, err)
	payload, ok := raw.(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "current_turn", controller.descendantsReq.Scope)
	require.Contains(t, payload["digest_skipped"], "current_turn")
	require.NotContains(t, payload, "digest")
}

func TestSubagentStatusAutoUsesResolvedFilterScopeForDigest(t *testing.T) {
	controller := &fakeSupervisionController{
		snapshot: &supervision.Snapshot{
			FilterScope: "all",
		},
		digest: &supervision.Digest{},
	}
	broker := &Broker{Supervision: controller}
	raw, _, err := broker.Execute(context.Background(), "parent-1", ToolSubagentStatus, map[string]interface{}{
		"include_digest": true,
	})
	require.NoError(t, err)
	payload, ok := raw.(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "auto", controller.descendantsReq.Scope)
	require.Equal(t, "all", payload["filter_scope"])
	require.Contains(t, payload, "digest", "auto resolving to all must keep the historical digest")
	require.NotContains(t, payload, "digest_skipped")

	controller.snapshot = &supervision.Snapshot{FilterScope: "current_turn"}
	raw, _, err = broker.Execute(context.Background(), "parent-1", ToolSubagentStatus, map[string]interface{}{
		"include_digest": true,
	})
	require.NoError(t, err)
	payload, ok = raw.(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "current_turn", payload["filter_scope"])
	require.Contains(t, payload["digest_skipped"], "current_turn")
	require.NotContains(t, payload, "digest")
}
