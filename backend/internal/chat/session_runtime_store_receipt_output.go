package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

const (
	maxInlineToolReceiptOutputBytes = 512
	toolReceiptOutputRef            = "tool_receipt"
)

func toolReceiptEventNeedsOutput(event *runtimeevents.Event) bool {
	if event == nil || event.Type != EventToolReceiptRecorded || event.Payload == nil {
		return false
	}
	if output, ok := event.Payload["output"].(string); ok && output != "" {
		return false
	}
	ref, _ := event.Payload["output_ref"].(string)
	return strings.TrimSpace(ref) == toolReceiptOutputRef
}

func applyToolReceiptOutput(event *runtimeevents.Event, output string) {
	if event == nil || output == "" {
		return
	}
	if event.Payload == nil {
		event.Payload = map[string]interface{}{}
	}
	event.Payload["output"] = output
	delete(event.Payload, "output_ref")
	delete(event.Payload, "output_bytes")
	delete(event.Payload, "output_preview")
}

// hydrateSQLiteToolReceiptOutputs restores bounded tool output referenced by
// tool_receipt_recorded events. It is best-effort: a pruned receipt leaves the
// preview/metadata intact instead of failing the event page.
func (s *SQLiteRuntimeStore) hydrateSQLiteToolReceiptOutputs(ctx context.Context, queryer *sql.DB, sessionID string, events []runtimeevents.Event) {
	if s == nil {
		return
	}
	toolIDs := make([]string, 0)
	for index := range events {
		if !toolReceiptEventNeedsOutput(&events[index]) {
			continue
		}
		if toolCallID, ok := events[index].Payload["tool_call_id"].(string); ok && strings.TrimSpace(toolCallID) != "" {
			toolIDs = append(toolIDs, strings.TrimSpace(toolCallID))
		}
	}
	if len(toolIDs) == 0 {
		return
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(toolIDs)), ",")
	args := make([]interface{}, 0, len(toolIDs)+1)
	args = append(args, sessionID)
	for _, toolCallID := range toolIDs {
		args = append(args, toolCallID)
	}
	rows, err := queryer.QueryContext(ctx, fmt.Sprintf(`
		SELECT tool_call_id, message_json
		FROM session_tool_receipts
		WHERE session_id = ? AND tool_call_id IN (%s)
	`, placeholders), args...)
	if err != nil {
		return
	}
	defer rows.Close()
	outputs := make(map[string]string, len(toolIDs))
	for rows.Next() {
		var toolCallID string
		var messageJSON []byte
		if err := rows.Scan(&toolCallID, &messageJSON); err != nil {
			return
		}
		if output := extractToolOutput(json.RawMessage(messageJSON)); output != "" {
			outputs[toolCallID] = output
		}
	}
	if err := rows.Err(); err != nil {
		return
	}
	for index := range events {
		if !toolReceiptEventNeedsOutput(&events[index]) {
			continue
		}
		toolCallID, _ := events[index].Payload["tool_call_id"].(string)
		if output := outputs[strings.TrimSpace(toolCallID)]; output != "" {
			applyToolReceiptOutput(&events[index], output)
		}
	}
}

func (s *InMemoryRuntimeStore) hydrateInMemoryToolReceiptOutputs(sessionID string, events []runtimeevents.Event) {
	if s == nil {
		return
	}
	receipts := s.receipts[sessionID]
	if len(receipts) == 0 {
		return
	}
	for index := range events {
		if !toolReceiptEventNeedsOutput(&events[index]) {
			continue
		}
		toolCallID, _ := events[index].Payload["tool_call_id"].(string)
		receipt, ok := receipts[strings.TrimSpace(toolCallID)]
		if !ok {
			continue
		}
		if output := extractToolOutput(receipt.MessageJSON); output != "" {
			applyToolReceiptOutput(&events[index], output)
		}
	}
}
