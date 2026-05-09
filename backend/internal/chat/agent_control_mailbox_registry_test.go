package chat

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func TestAgentControlMailboxRegistryProjectsEventStoreFallback(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryRuntimeStore(16)
	metadata := agentcontrol.Envelope{
		Workflow:        agentcontrol.WorkflowSpawnAgent,
		MessageType:     agentcontrol.MessageTypeAgentMessage,
		ControlAction:   agentcontrol.ActionAgentMessage,
		MailboxDelivery: agentcontrol.DeliverySessionMailbox,
		MailboxKind:     agentcontrol.MailboxKindAgentMessage,
	}.Metadata()
	_, err := store.AppendEvent(ctx, NewMailboxReceivedEvent("session-1", team.MailMessage{
		ID:        "mail-1",
		TeamID:    "team-1",
		FromAgent: "root",
		ToAgent:   "child",
		Kind:      agentcontrol.MailboxKindAgentMessage,
		Body:      "event fallback body",
		Metadata:  metadata,
	}))
	require.NoError(t, err)
	_, err = store.AppendEvent(ctx, NewMailboxReceivedEvent("session-1", team.MailMessage{
		ID:        "mail-legacy",
		FromAgent: "root",
		ToAgent:   "child",
		Kind:      "info",
		Body:      "legacy body",
	}))
	require.NoError(t, err)

	registry := NewAgentControlMailboxRegistry(eventStoreOnly{EventStore: store}, "session-1")
	records, err := registry.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:     agentcontrol.MailboxScopeSession,
		SessionID: "session-1",
		Workflow:  agentcontrol.WorkflowSpawnAgent,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "mail-1", records[0].MessageID)
	require.Equal(t, agentcontrol.MailboxScopeSession, records[0].Scope)
	require.Equal(t, "session-1", records[0].SessionID)
	require.Equal(t, "event fallback body", records[0].Body)
	require.Equal(t, agentcontrol.WorkflowSpawnAgent, records[0].Workflow)

	seq, err := registry.LastAgentControlMailboxRecordSeq(ctx, agentcontrol.MailboxRecordFilter{
		Scope:     agentcontrol.MailboxScopeSession,
		SessionID: "session-1",
		Workflow:  agentcontrol.WorkflowSpawnAgent,
	})
	require.NoError(t, err)
	require.Equal(t, records[0].Seq, seq)
}

func TestAgentControlMailboxRegistryEventFallbackUsesCombinedCursorAcrossSessions(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryRuntimeStore(16)
	metadata := agentcontrol.Envelope{
		Workflow:        agentcontrol.WorkflowSpawnAgent,
		MessageType:     agentcontrol.MessageTypeAgentMessage,
		ControlAction:   agentcontrol.ActionAgentMessage,
		MailboxDelivery: agentcontrol.DeliverySessionMailbox,
		MailboxKind:     agentcontrol.MailboxKindAgentMessage,
	}.Metadata()
	_, err := store.AppendEvent(ctx, NewMailboxReceivedEvent("session-1", team.MailMessage{
		ID:        "mail-session-1",
		FromAgent: "root",
		ToAgent:   "child-1",
		Kind:      agentcontrol.MailboxKindAgentMessage,
		Body:      "first session body",
		Metadata:  metadata,
		CreatedAt: time.Unix(100, 0).UTC(),
	}))
	require.NoError(t, err)

	registry := NewAgentControlMailboxRegistry(eventStoreOnly{EventStore: store}, "session-1", "session-2")
	firstBatch, err := registry.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:    agentcontrol.MailboxScopeSession,
		Workflow: agentcontrol.WorkflowSpawnAgent,
	})
	require.NoError(t, err)
	require.Len(t, firstBatch, 1)
	require.Equal(t, "session-1", firstBatch[0].SessionID)
	require.Equal(t, "mail-session-1", firstBatch[0].MessageID)
	require.Greater(t, firstBatch[0].Seq, firstBatch[0].SourceSeq)

	_, err = store.AppendEvent(ctx, NewMailboxReceivedEvent("session-2", team.MailMessage{
		ID:        "mail-session-2",
		FromAgent: "root",
		ToAgent:   "child-2",
		Kind:      agentcontrol.MailboxKindAgentMessage,
		Body:      "second session body",
		Metadata:  metadata,
		CreatedAt: time.Unix(101, 0).UTC(),
	}))
	require.NoError(t, err)

	afterFirst, err := registry.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:    agentcontrol.MailboxScopeSession,
		Workflow: agentcontrol.WorkflowSpawnAgent,
		AfterSeq: firstBatch[0].Seq,
	})
	require.NoError(t, err)
	require.Len(t, afterFirst, 1)
	require.Equal(t, "session-2", afterFirst[0].SessionID)
	require.Equal(t, "mail-session-2", afterFirst[0].MessageID)
	require.Greater(t, afterFirst[0].Seq, firstBatch[0].Seq)

	latest, err := registry.LastAgentControlMailboxRecordSeq(ctx, agentcontrol.MailboxRecordFilter{
		Scope:    agentcontrol.MailboxScopeSession,
		Workflow: agentcontrol.WorkflowSpawnAgent,
	})
	require.NoError(t, err)
	require.Equal(t, afterFirst[0].Seq, latest)
}

type eventStoreOnly struct {
	EventStore
}
