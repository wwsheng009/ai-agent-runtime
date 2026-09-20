package commands

import (
	"encoding/json"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// todoSnapshotMetadataKey mirrors internal/agent's tool-scoped protocol_result
// key. It is duplicated as a literal because the agent package keeps it
// unexported and the ACP bridge must not depend on agent internals.
const todoSnapshotMetadataKey = "todo_snapshot"

// acpTodoItem is the wire shape of one todos tool item. Decoding goes through a
// JSON round trip so typed values, []interface{} and JSON-decoded maps all
// decode the same way (the same trick internal/agent uses when it builds the
// snapshot).
type acpTodoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

// acpPlanEntriesFromRuntimeEvent maps the task list carried by a finished
// `todos` tool event onto ACP plan entries.
//
// The todos tool result metadata is the authoritative snapshot
// (payload.protocol_result.metadata.todo_snapshot) and is what the web task
// panel renders, so the ACP plan mirrors that instead of the text summary.
// ACP has no active-form concept, so entries use the task description; todos
// carry no priority, so every entry stays "medium" and clients keep the
// model-authored order.
func acpPlanEntriesFromRuntimeEvent(event runtimeevents.Event) ([]acp.PlanEntry, bool) {
	if !strings.EqualFold(strings.TrimSpace(runtimeEventToolName(event)), "todos") {
		return nil, false
	}
	return acpPlanEntriesFromTodoSnapshot(protocolResultTodoSnapshot(event.Payload))
}

// protocolResultTodoSnapshot returns the raw todo_snapshot value of a tool
// event payload, or nil when the payload does not carry one.
func protocolResultTodoSnapshot(payload map[string]interface{}) interface{} {
	if len(payload) == 0 {
		return nil
	}
	result, ok := payload["protocol_result"].(map[string]interface{})
	if !ok {
		return nil
	}
	metadata, ok := result["metadata"].(map[string]interface{})
	if !ok {
		return nil
	}
	snapshot, ok := metadata[todoSnapshotMetadataKey]
	if !ok {
		return nil
	}
	return snapshot
}

// acpPlanEntriesFromTodoSnapshot decodes both shapes that carry todos: the live
// snapshot object {"items": [...]} and the cold-history list itself. It returns
// ok=false when nothing decodable is present; per-entry validation (blank
// content, unknown status) is left to acp.PlanUpdate so there is a single
// normalization point.
func acpPlanEntriesFromTodoSnapshot(raw interface{}) ([]acp.PlanEntry, bool) {
	if raw == nil {
		return nil, false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}
	if strings.HasPrefix(strings.TrimSpace(string(encoded)), "[") {
		var items []acpTodoItem
		if err := json.Unmarshal(encoded, &items); err != nil {
			return nil, false
		}
		return acpPlanEntriesFromTodoItems(items)
	}
	var snapshot struct {
		Items []acpTodoItem `json:"items"`
	}
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return nil, false
	}
	return acpPlanEntriesFromTodoItems(snapshot.Items)
}

func acpPlanEntriesFromTodoItems(items []acpTodoItem) ([]acp.PlanEntry, bool) {
	if len(items) == 0 {
		return nil, false
	}
	entries := make([]acp.PlanEntry, 0, len(items))
	for _, item := range items {
		entries = append(entries, acp.PlanEntry{
			Content:  item.Content,
			Status:   item.Status,
			Priority: acp.PlanPriorityMedium,
		})
	}
	return entries, true
}

// replayTodoSnapshot extracts the todos list stored with a replayed tool
// message: nested under metadata.tool_metadata (the live agent shape) with the
// flat metadata.todos spelling kept for older records. The structural round
// trip normalizes typed in-process values and JSON-reloaded maps alike.
func replayTodoSnapshot(metadata runtimetypes.Metadata) interface{} {
	if len(metadata) == 0 {
		return nil
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil
	}
	var bag map[string]interface{}
	if err := json.Unmarshal(encoded, &bag); err != nil {
		return nil
	}
	if nested, ok := bag["tool_metadata"].(map[string]interface{}); ok {
		if todos, hasTodos := nested["todos"]; hasTodos && todos != nil {
			return todos
		}
	}
	if todos, hasTodos := bag["todos"]; hasTodos && todos != nil {
		return todos
	}
	return nil
}

// lastReplayPlanEntries finds the newest todos snapshot in replayed history.
// Plan state has no durable record of its own, so a freshly attached client
// rebuilds it from the most recent tool result that carries one; only the last
// snapshot matters because clients replace the whole plan on every update.
func lastReplayPlanEntries(messages []runtimetypes.Message) ([]acp.PlanEntry, bool) {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			continue
		}
		raw := replayTodoSnapshot(message.Metadata)
		if raw == nil {
			continue
		}
		if entries, ok := acpPlanEntriesFromTodoSnapshot(raw); ok {
			return entries, true
		}
	}
	return nil, false
}

// emitPlanUpdate sends one plan session/update and suppresses byte-identical
// repeats: clients replace their whole plan per update, so re-emitting the same
// list (for example when the model re-runs todos with no change) is pure noise.
// The fingerprint is deliberately session-scoped rather than reset per prompt,
// because the plan outlives a single turn.
func (b *acpEventBridge) emitPlanUpdate(update acp.SessionUpdate) error {
	if b == nil || len(update.Entries) == 0 {
		return nil
	}
	fingerprint := acpPlanFingerprint(update.Entries)
	b.mu.Lock()
	duplicate := fingerprint != "" && b.lastPlanFingerprint == fingerprint
	if !duplicate {
		b.lastPlanFingerprint = fingerprint
	}
	b.mu.Unlock()
	if duplicate {
		return nil
	}
	return b.sessionUpdate(update)
}

func acpPlanFingerprint(entries []acp.PlanEntry) string {
	encoded, err := json.Marshal(entries)
	if err != nil {
		return ""
	}
	return string(encoded)
}
