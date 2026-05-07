package agentcontrol

import "testing"

func TestEnvelopeMetadata(t *testing.T) {
	metadata := Envelope{
		MessageType:     MessageTypeTeamTaskAssignment,
		ControlAction:   ActionTaskAssign,
		Workflow:        WorkflowSpawnTeam,
		MailboxDelivery: DeliverySessionMailbox,
		MailboxKind:     MailboxKindTeamTaskAssignment,
	}.Metadata()

	if metadata["message_type"] != MessageTypeTeamTaskAssignment ||
		metadata["control_action"] != ActionTaskAssign ||
		metadata["workflow"] != WorkflowSpawnTeam ||
		metadata["mailbox_delivery"] != DeliverySessionMailbox ||
		metadata["mailbox_kind"] != MailboxKindTeamTaskAssignment {
		t.Fatalf("unexpected envelope metadata: %#v", metadata)
	}
}

func TestApplyEnvelopePreservesDomainMetadata(t *testing.T) {
	metadata := ApplyEnvelope(map[string]interface{}{
		"team_id": "team-1",
	}, Envelope{
		MessageType:     MessageTypeSubagentCompleted,
		ControlAction:   ActionAgentCompleted,
		Workflow:        WorkflowSpawnAgent,
		MailboxDelivery: DeliverySessionMailbox,
		MailboxKind:     MailboxKindSubagentCompleted,
	})

	if metadata["team_id"] != "team-1" ||
		metadata["message_type"] != MessageTypeSubagentCompleted ||
		metadata["control_action"] != ActionAgentCompleted ||
		metadata["workflow"] != WorkflowSpawnAgent ||
		metadata["mailbox_delivery"] != DeliverySessionMailbox ||
		metadata["mailbox_kind"] != MailboxKindSubagentCompleted {
		t.Fatalf("unexpected applied metadata: %#v", metadata)
	}
}

func TestEnvelopeMetadataHelpers(t *testing.T) {
	metadata := map[string]interface{}{
		MetadataKeyMessageType:     " " + MessageTypeAgentMessage + " ",
		MetadataKeyControlAction:   ActionAgentMessage,
		MetadataKeyWorkflow:        WorkflowSpawnAgent,
		MetadataKeyMailboxDelivery: DeliverySessionMailbox,
		MetadataKeyMailboxKind:     MailboxKindAgentMessage,
		"non_string":               123,
	}

	if got := MetadataString(metadata, MetadataKeyMessageType); got != MessageTypeAgentMessage {
		t.Fatalf("unexpected message type: %q", got)
	}
	if got := MetadataString(metadata, "non_string"); got != "" {
		t.Fatalf("expected empty non-string metadata, got %q", got)
	}
	if !HasEnvelopeMetadata(metadata) {
		t.Fatalf("expected envelope metadata to be detected")
	}
	if HasEnvelopeMetadata(map[string]interface{}{"team_id": "team-1"}) {
		t.Fatalf("expected non-envelope metadata to be ignored")
	}
}
