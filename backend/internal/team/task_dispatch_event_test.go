package team

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildTaskLifecycleMailboxMessageUsesAgentControlEnvelope(t *testing.T) {
	message := BuildTaskLifecycleMailboxMessage(TeamEvent{
		Type:   "task.completed",
		TeamID: "team-1",
		Payload: map[string]interface{}{
			"task_id":  "task-1",
			"assignee": "mate-1",
			"summary":  "done",
		},
	})

	require.Equal(t, TaskLifecycleMailboxKind, message.Kind)
	require.Equal(t, "team-1", message.TeamID)
	require.Equal(t, "mate-1", message.ToAgent)
	require.NotNil(t, message.TaskID)
	require.Equal(t, "task-1", *message.TaskID)
	require.Equal(t, "done", message.Body)
	require.Equal(t, TaskLifecycleControlMessageType, message.Metadata["message_type"])
	require.Equal(t, TaskLifecycleControlAction, message.Metadata["control_action"])
	require.Equal(t, TaskAssignmentWorkflow, message.Metadata["workflow"])
	require.Equal(t, taskDispatchMailboxDeliveryAgentSubstr, message.Metadata["mailbox_delivery"])
	require.Equal(t, TaskLifecycleMailboxKind, message.Metadata["mailbox_kind"])
	require.Equal(t, "task.completed", message.Metadata["event_type"])
	require.Equal(t, "mate-1", message.Metadata["assignee"])
}
