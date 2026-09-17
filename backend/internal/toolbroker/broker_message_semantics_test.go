package toolbroker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// P0-3a/M5 instruction-delivery return contract (docs/plan/
// supervision-parent-child-control-optimization-plan-20260917.md §3.3).

// messageSemanticsController lets each test pin the host-side result that the
// broker relays, so the v1 (default off) and v2 field projections are explicit.
type messageSemanticsController struct {
	*fakeAgentSessionController
	sendResult   *AgentMessageResult
	followResult *AgentMessageResult
	inputResult  *AgentStatusResult
	inputErr     error
}

func (c *messageSemanticsController) SendMessage(_ context.Context, _ string, _ AgentMessageArgs) (*AgentMessageResult, error) {
	return c.sendResult, nil
}

func (c *messageSemanticsController) FollowupTask(_ context.Context, _ string, _ AgentMessageArgs) (*AgentMessageResult, error) {
	return c.followResult, nil
}

func (c *messageSemanticsController) SendInput(_ context.Context, _ SendAgentInputArgs) (*AgentStatusResult, error) {
	return c.inputResult, c.inputErr
}

func TestBrokerAgentMessageV1ResultKeepsLegacyFields(t *testing.T) {
	controller := &messageSemanticsController{
		fakeAgentSessionController: &fakeAgentSessionController{},
		sendResult:                 &AgentMessageResult{TargetSessionID: "child-1", Delivered: true},
	}
	broker := &Broker{AgentSessions: controller}

	raw, meta, err := broker.Execute(context.Background(), "parent-session", ToolSendMessage, map[string]interface{}{
		"target":  "child-1",
		"message": "legacy note",
	})
	require.NoError(t, err)
	require.Equal(t, true, meta["delivered"])
	if _, exists := meta["queued"]; exists {
		t.Fatalf("v1 summary must not gain queued: %#v", meta)
	}
	if _, exists := meta["duplicate"]; exists {
		t.Fatalf("v1 summary must not gain duplicate: %#v", meta)
	}
	encoded, err := json.Marshal(raw)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), `"queued"`)
	require.NotContains(t, string(encoded), `"duplicate"`)
}

func TestBrokerAgentMessageV2ResultSurfacesQueuedAndDuplicate(t *testing.T) {
	controller := &messageSemanticsController{
		fakeAgentSessionController: &fakeAgentSessionController{},
		followResult: &AgentMessageResult{
			TargetSessionID: "child-1",
			Delivered:       true,
			Queued:          true,
			Duplicate:       true,
		},
	}
	broker := &Broker{AgentSessions: controller}

	raw, meta, err := broker.Execute(context.Background(), "parent-session", ToolFollowupTask, map[string]interface{}{
		"target":  "child-1",
		"message": "queued while busy",
	})
	require.NoError(t, err)
	require.Equal(t, true, meta["delivered"])
	require.Equal(t, true, meta["queued"])
	require.Equal(t, true, meta["duplicate"])
	require.Equal(t, false, meta["triggered"])
	result, ok := raw.(*AgentMessageResult)
	require.True(t, ok)
	require.True(t, result.Queued)
	require.True(t, result.Duplicate)
	require.False(t, result.Triggered)
}

func TestBrokerSendInputV2ResultSurfacesDeliveryFields(t *testing.T) {
	controller := &messageSemanticsController{
		fakeAgentSessionController: &fakeAgentSessionController{},
		inputResult: &AgentStatusResult{
			ID:        "child-1",
			SessionID: "child-1",
			Status:    "queued",
			Queued:    true,
			Delivered: true,
			Triggered: true,
			Duplicate: true,
		},
	}
	broker := &Broker{AgentSessions: controller}

	raw, meta, err := broker.Execute(context.Background(), "parent-session", ToolSendInput, map[string]interface{}{
		"id":      "child-1",
		"message": "continue",
	})
	require.NoError(t, err)
	require.Equal(t, true, meta["queued"])
	require.Equal(t, true, meta["delivered"])
	require.Equal(t, true, meta["triggered"])
	require.Equal(t, true, meta["duplicate"])
	result, ok := raw.(*AgentStatusResult)
	require.True(t, ok)
	require.True(t, result.Delivered)
	require.True(t, result.Triggered)
	require.True(t, result.Duplicate)
}

func TestBrokerSendInputV1ResultOmitsV2Fields(t *testing.T) {
	controller := &messageSemanticsController{
		fakeAgentSessionController: &fakeAgentSessionController{},
		inputResult:                &AgentStatusResult{ID: "child-1", SessionID: "child-1", Status: "idle", Queued: true},
	}
	broker := &Broker{AgentSessions: controller}

	raw, meta, err := broker.Execute(context.Background(), "parent-session", ToolSendInput, map[string]interface{}{
		"id":      "child-1",
		"message": "continue",
	})
	require.NoError(t, err)
	require.Equal(t, true, meta["queued"])
	if _, exists := meta["delivered"]; exists {
		t.Fatalf("v1 send_input summary must not gain delivered: %#v", meta)
	}
	encoded, err := json.Marshal(raw)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), `"delivered"`)
	require.NotContains(t, string(encoded), `"triggered"`)
	require.NotContains(t, string(encoded), `"duplicate"`)
}

func TestBrokerInstructionToolDescriptionsMatchV2Semantics(t *testing.T) {
	broker := &Broker{AgentSessions: &fakeAgentSessionController{}}
	descriptions := map[string]string{}
	for _, def := range broker.Definitions() {
		switch def.Name {
		case ToolSendMessage, ToolFollowupTask, ToolSendInput:
			descriptions[def.Name] = def.Description
		}
	}
	for _, name := range []string{ToolSendMessage, ToolFollowupTask, ToolSendInput} {
		description := descriptions[name]
		require.NotEmpty(t, description, "%s definition missing", name)
		require.Contains(t, description, "delivered/queued/triggered/duplicate", "%s must document the return fields", name)
		require.Contains(t, description, "closed", "%s must document the terminal error", name)
	}
	require.Contains(t, descriptions[ToolSendMessage], "without starting a new turn")
	require.Contains(t, descriptions[ToolFollowupTask], "trigger_turn=true")
	require.Contains(t, descriptions[ToolSendInput], "interrupt=false")
	require.False(t, strings.Contains(descriptions[ToolSendMessage], "interrupt"), "send_message must not imply interruption")
}
