package contextmgr

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestCompactMessagesRetainsLatestAndCriticalHandoffInfo(t *testing.T) {
	history := make([]types.Message, 0, 32)
	for index := 1; index <= 16; index++ {
		history = append(history, *types.NewUserMessage(fmt.Sprintf("user request %d %s", index, strings.Repeat("u", 80))))
	}

	goal := types.NewDeveloperMessage("Persistent goal: keep compact handoff usable after compression.")
	goal.Metadata["context_stage"] = "active_goal"
	todos := types.NewAssistantMessage("Current todos:\n- [ ] Preserve path references\n- [ ] Preserve failures")
	todos.Metadata["context_stage"] = "todo_state"
	recall := types.NewAssistantMessage("stale recall that should not survive local compact")
	recall.Metadata["context_stage"] = "recall"
	assistant := types.NewAssistantMessage("Decision: prefer latest user goals over early filler. Next step: verify compact retention.")
	assistant.ToolCalls = []types.ToolCall{{
		ID:   "call-1",
		Name: "view",
		Args: map[string]interface{}{"path": "backend/internal/contextmgr/compact.go"},
	}}
	tool := types.NewToolMessage("call-1", "failed to open backend/internal/contextmgr/compact.go: permission denied")
	tool.Metadata["tool_error"] = "permission denied"

	history = append(history,
		*goal,
		*todos,
		*recall,
		*types.NewUserMessage("Do not drop constraints. Keep the final objective."),
		*assistant,
		*tool,
	)

	message := compactMessages(history)
	require.NotNil(t, message)
	content := message.Content

	require.Contains(t, content, "user request 16")
	require.NotContains(t, content, "\n- user request 1 ")
	require.Contains(t, content, "Durable session context:")
	require.Contains(t, content, "keep compact handoff usable")
	require.Contains(t, content, "Preserve path references")
	require.NotContains(t, content, "stale recall that should not survive local compact")
	require.Contains(t, content, "Constraints and preferences:")
	require.Contains(t, content, "Do not drop constraints")
	require.Contains(t, content, "Key decisions:")
	require.Contains(t, content, "prefer latest user goals")
	require.Contains(t, content, "Critical references:")
	require.Contains(t, content, "backend/internal/contextmgr/compact.go")
	require.Contains(t, content, "Failures to account for:")
	require.Contains(t, content, "permission denied")
	require.Contains(t, content, "Remaining work:")
	require.Contains(t, content, "verify compact retention")
}
