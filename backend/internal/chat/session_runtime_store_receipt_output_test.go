package chat

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func TestToolReceiptRecordedEventStoresOutputReferenceAndHydrates(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: filepath.Join(t.TempDir(), "runtime.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	const sessionID = "receipt-output-session"
	const toolCallID = "call-large-output"
	content := strings.Repeat("large-output-", 256)
	messageJSON, err := json.Marshal(map[string]interface{}{
		"role":         "tool",
		"tool_call_id": toolCallID,
		"content":      content,
	})
	require.NoError(t, err)
	require.NoError(t, store.SaveToolReceipt(ctx, ToolExecutionReceipt{
		SessionID:   sessionID,
		ToolCallID:  toolCallID,
		ToolName:    "shell",
		MessageJSON: messageJSON,
		CreatedAt:   time.Now().UTC(),
	}))

	actor := &SessionActor{id: sessionID, eventStore: store}
	actor.publishToolReceiptEvent(EventToolReceiptRecorded, "turn-1", "receipt_store", ToolExecutionReceipt{
		SessionID:   sessionID,
		ToolCallID:  toolCallID,
		ToolName:    "shell",
		MessageJSON: messageJSON,
		CreatedAt:   time.Now().UTC(),
	})

	var raw string
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT payload_json FROM session_events
		WHERE session_id = ? AND type = ?
	`, sessionID, EventToolReceiptRecorded).Scan(&raw))
	require.NotContains(t, raw, `"output":`, "large receipt output must not be duplicated inline")
	require.Contains(t, raw, `"output_ref":"tool_receipt"`)
	require.Contains(t, raw, `"output_preview":`)

	events, err := store.ListEvents(ctx, sessionID, 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, content, events[0].Payload["output"])
	require.NotContains(t, events[0].Payload, "output_ref")
	require.NotContains(t, events[0].Payload, "output_preview")

	before, err := store.ListEventsBefore(ctx, sessionID, 2, 10)
	require.NoError(t, err)
	require.Len(t, before, 1)
	require.Equal(t, content, before[0].Payload["output"])
}

func TestInMemoryToolReceiptEventHydratesOutputReference(t *testing.T) {
	store := NewInMemoryRuntimeStore(8)
	const sessionID = "receipt-memory-session"
	const toolCallID = "call-memory-output"
	content := strings.Repeat("memory-output-", 128)
	messageJSON, err := json.Marshal(map[string]interface{}{
		"role":         "tool",
		"tool_call_id": toolCallID,
		"content":      content,
	})
	require.NoError(t, err)
	require.NoError(t, store.SaveToolReceipt(context.Background(), ToolExecutionReceipt{
		SessionID:   sessionID,
		ToolCallID:  toolCallID,
		ToolName:    "shell",
		MessageJSON: messageJSON,
		CreatedAt:   time.Now().UTC(),
	}))
	_, err = store.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      EventToolReceiptRecorded,
		SessionID: sessionID,
		Payload: map[string]interface{}{
			"tool_call_id":   toolCallID,
			"tool_name":      "shell",
			"output_ref":     toolReceiptOutputRef,
			"output_bytes":   len(content),
			"output_preview": content[:maxInlineToolReceiptOutputBytes],
		},
	})
	require.NoError(t, err)

	events, err := store.ListEvents(context.Background(), sessionID, 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, content, events[0].Payload["output"])
}
