package toolbroker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// viewWindowEvents mixes progress, tool lifecycle, assistant text and the
// lifecycle events the tool_progress view must never drop (P1-5 方案 3).
func viewWindowEvents() []AgentEventItem {
	return []AgentEventItem{
		{Seq: 1, Type: "assistant.reasoning"},
		{Seq: 2, Type: "tool.progress", ToolName: "bash"},
		{Seq: 3, Type: "tool.requested", ToolName: "rg"},
		{Seq: 4, Type: "assistant.message"},
		{Seq: 5, Type: "approval_requested"},
		{Seq: 6, Type: "session_end"},
	}
}

func TestNormalizeAgentEventsView(t *testing.T) {
	assert.Equal(t, AgentEventsViewAll, NormalizeAgentEventsView(""))
	assert.Equal(t, AgentEventsViewAll, NormalizeAgentEventsView("all"))
	assert.Equal(t, AgentEventsViewAll, NormalizeAgentEventsView("  ALL "))
	assert.Equal(t, AgentEventsViewAll, NormalizeAgentEventsView("typo"))
	assert.Equal(t, AgentEventsViewToolProgress, NormalizeAgentEventsView(" tool_progress "))
	assert.Equal(t, AgentEventsViewToolProgress, NormalizeAgentEventsView("TOOL_PROGRESS"))
}

func TestKeepsAgentEventForView_DefaultKeepsEverything(t *testing.T) {
	for _, view := range []string{"", "all", "unknown-view"} {
		assert.True(t, KeepsAgentEventForView(view, "assistant.message"), "view %q must not filter", view)
	}
}

func TestKeepsAgentEventForView_ToolProgressRules(t *testing.T) {
	assert.True(t, KeepsAgentEventForView("tool_progress", "tool.progress"))
	assert.True(t, KeepsAgentEventForView("tool_progress", "tool.requested"))
	assert.True(t, KeepsAgentEventForView("tool_progress", " TOOL.COMPLETED "))
	assert.True(t, KeepsAgentEventForView("tool_progress", "session_end"))
	assert.True(t, KeepsAgentEventForView("tool_progress", "agent.reclaimed"))
	assert.True(t, KeepsAgentEventForView("tool_progress", "approval_resolved"))
	assert.False(t, KeepsAgentEventForView("tool_progress", "assistant.message"))
	assert.False(t, KeepsAgentEventForView("tool_progress", "assistant.reasoning"))
	assert.False(t, KeepsAgentEventForView("tool_progress", ""))
}

func TestApplyAgentEventsView_ToolProgressProjectsWindow(t *testing.T) {
	result := &AgentEventsResult{SessionID: "child-1", Events: viewWindowEvents(), Count: 6}

	got := ApplyAgentEventsView(result, "tool_progress")

	require.NotNil(t, got)
	assert.Equal(t, AgentEventsViewToolProgress, got.View)
	assert.Equal(t, 4, got.Count)
	assert.Equal(t, 2, got.Filtered)
	types := make([]string, 0, len(got.Events))
	for _, event := range got.Events {
		types = append(types, event.Type)
	}
	assert.Equal(t, []string{"tool.progress", "tool.requested", "approval_requested", "session_end"}, types)
}

func TestApplyAgentEventsView_AllAndUnknownAreNoOps(t *testing.T) {
	for _, view := range []string{"", "all", "typo"} {
		result := &AgentEventsResult{SessionID: "child-1", Events: viewWindowEvents(), Count: 6}

		got := ApplyAgentEventsView(result, view)

		require.NotNil(t, got)
		assert.Empty(t, got.View, "view %q must not stamp a projection", view)
		assert.Zero(t, got.Filtered)
		assert.Equal(t, 6, got.Count)
		assert.Len(t, got.Events, 6)
	}
}

func TestApplyAgentEventsView_EmptyWindowKeepsViewMarker(t *testing.T) {
	result := &AgentEventsResult{SessionID: "child-1"}

	got := ApplyAgentEventsView(result, "tool_progress")

	require.NotNil(t, got)
	assert.Equal(t, AgentEventsViewToolProgress, got.View)
	assert.Zero(t, got.Filtered)
	assert.Zero(t, got.Count)
	assert.Empty(t, got.Events)
}

func TestApplyAgentEventsView_NilResult(t *testing.T) {
	assert.Nil(t, ApplyAgentEventsView(nil, "tool_progress"))
}
