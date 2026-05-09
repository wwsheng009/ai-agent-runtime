package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// AgentControlMailboxRegistry adapts runtime/session mailbox storage to the
// shared AgentControl mailbox registry. SQLite/InMemory runtime stores can
// serve native registry rows; older stores are projected from mailbox readers
// or mailbox_received session events.
type AgentControlMailboxRegistry struct {
	Store      interface{}
	SessionIDs []string
}

var _ agentcontrol.MailboxRegistryReader = AgentControlMailboxRegistry{}
var _ agentcontrol.MailboxRegistrySequencer = AgentControlMailboxRegistry{}

// NewAgentControlMailboxRegistry creates a runtime/session mailbox registry
// projection.
func NewAgentControlMailboxRegistry(store interface{}, sessionIDs ...string) AgentControlMailboxRegistry {
	return AgentControlMailboxRegistry{Store: store, SessionIDs: sessionIDs}
}

// ListAgentControlMailboxRecords returns session-scoped AgentControl mailbox
// records.
func (r AgentControlMailboxRegistry) ListAgentControlMailboxRecords(ctx context.Context, filter agentcontrol.MailboxRecordFilter) ([]agentcontrol.MailboxRecord, error) {
	if r.Store == nil {
		return nil, nil
	}
	filter = filter.Normalize()
	if filter.Scope != "" && filter.Scope != agentcontrol.MailboxScopeSession {
		return nil, nil
	}
	sessionIDs := r.registrySessionIDs(filter.SessionID)
	if len(sessionIDs) == 0 {
		return nil, fmt.Errorf("session id is required")
	}
	useCombinedCursor := strings.TrimSpace(filter.SessionID) == ""
	records := make([]agentcontrol.MailboxRecord, 0)
	for index, sessionID := range sessionIDs {
		sessionFilter := filter
		sessionFilter.Scope = agentcontrol.MailboxScopeSession
		sessionFilter.SessionID = sessionID
		if useCombinedCursor {
			sessionFilter.AfterSeq = 0
			sessionFilter.Limit = 0
		}
		sessionRecords, err := r.listSessionMailboxRecords(ctx, sessionFilter)
		if err != nil {
			return nil, err
		}
		for _, record := range sessionRecords {
			if useCombinedCursor {
				sourceSeq := record.Seq
				record.SourceSeq = sourceSeq
				record.Seq = agentcontrol.CombinedMailboxSeq(index, sourceSeq, record.CreatedAt)
				if record.Seq <= filter.AfterSeq {
					continue
				}
			}
			records = append(records, record)
		}
	}
	sortRuntimeMailboxRecords(records)
	if filter.Limit > 0 && len(records) > filter.Limit {
		records = records[:filter.Limit]
	}
	return records, nil
}

// LastAgentControlMailboxRecordSeq returns the largest session-scoped mailbox
// registry sequence across configured sessions.
func (r AgentControlMailboxRegistry) LastAgentControlMailboxRecordSeq(ctx context.Context, filter agentcontrol.MailboxRecordFilter) (int64, error) {
	if r.Store == nil {
		return 0, nil
	}
	filter = filter.Normalize()
	if filter.Scope != "" && filter.Scope != agentcontrol.MailboxScopeSession {
		return 0, nil
	}
	sessionIDs := r.registrySessionIDs(filter.SessionID)
	if len(sessionIDs) == 0 {
		return 0, fmt.Errorf("session id is required")
	}
	if strings.TrimSpace(filter.SessionID) == "" {
		records, err := r.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
			Workflow: filter.Workflow,
			Scope:    filter.Scope,
		})
		if err != nil {
			return 0, err
		}
		var maxSeq int64
		for _, record := range records {
			if record.Seq > maxSeq {
				maxSeq = record.Seq
			}
		}
		return maxSeq, nil
	}
	var maxSeq int64
	for _, sessionID := range sessionIDs {
		sessionFilter := filter
		sessionFilter.Scope = agentcontrol.MailboxScopeSession
		sessionFilter.SessionID = sessionID
		seq, err := r.lastSessionMailboxRecordSeq(ctx, sessionFilter)
		if err != nil {
			return 0, err
		}
		if seq > maxSeq {
			maxSeq = seq
		}
	}
	return maxSeq, nil
}

func (r AgentControlMailboxRegistry) registrySessionIDs(filterSessionID string) []string {
	if sessionID := strings.TrimSpace(filterSessionID); sessionID != "" {
		return []string{sessionID}
	}
	out := make([]string, 0, len(r.SessionIDs))
	seen := map[string]struct{}{}
	for _, sessionID := range r.SessionIDs {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		key := strings.ToLower(sessionID)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, sessionID)
	}
	return out
}

func (r AgentControlMailboxRegistry) listSessionMailboxRecords(ctx context.Context, filter agentcontrol.MailboxRecordFilter) ([]agentcontrol.MailboxRecord, error) {
	if reader, ok := r.Store.(agentcontrol.MailboxRegistryReader); ok && reader != nil {
		return reader.ListAgentControlMailboxRecords(ctx, filter)
	}
	if reader, ok := r.Store.(AgentControlMailboxReaderStore); ok && reader != nil {
		messages, err := reader.ListAgentControlMailbox(ctx, filter.SessionID, filter.AfterSeq, filter.Limit)
		if err != nil {
			return nil, err
		}
		return runtimeMailboxRecordsFromMessages(filter.SessionID, messages, filter), nil
	}
	if reader, ok := r.Store.(MailboxReaderStore); ok && reader != nil {
		messages, err := reader.ListMailbox(ctx, filter.SessionID, filter.AfterSeq, filter.Limit)
		if err != nil {
			return nil, err
		}
		return runtimeMailboxRecordsFromMessages(filter.SessionID, messages, filter), nil
	}
	if reader, ok := r.Store.(EventStore); ok && reader != nil {
		events, err := reader.ListEvents(ctx, filter.SessionID, filter.AfterSeq, filter.Limit)
		if err != nil {
			return nil, err
		}
		return runtimeMailboxRecordsFromEvents(filter.SessionID, events, filter), nil
	}
	return nil, nil
}

func (r AgentControlMailboxRegistry) lastSessionMailboxRecordSeq(ctx context.Context, filter agentcontrol.MailboxRecordFilter) (int64, error) {
	if sequencer, ok := r.Store.(agentcontrol.MailboxRegistrySequencer); ok && sequencer != nil {
		return sequencer.LastAgentControlMailboxRecordSeq(ctx, filter)
	}
	if sequencer, ok := r.Store.(AgentControlMailboxSequenceStore); ok && sequencer != nil {
		return sequencer.LastAgentControlMailboxSeq(ctx, filter.SessionID)
	}
	records, err := r.listSessionMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:     agentcontrol.MailboxScopeSession,
		SessionID: filter.SessionID,
		Workflow:  filter.Workflow,
	})
	if err != nil {
		return 0, err
	}
	var maxSeq int64
	for _, record := range records {
		if record.Seq > maxSeq {
			maxSeq = record.Seq
		}
	}
	return maxSeq, nil
}

func runtimeMailboxRecordsFromMessages(sessionID string, messages []team.MailMessage, filter agentcontrol.MailboxRecordFilter) []agentcontrol.MailboxRecord {
	records := make([]agentcontrol.MailboxRecord, 0, len(messages))
	for _, message := range messages {
		if !IsAgentControlMailboxMessage(message) {
			continue
		}
		record := mailboxRecordFromRuntimeMessage(sessionID, message)
		if filter.Workflow != "" && record.Workflow != filter.Workflow {
			continue
		}
		if filter.TeamID != "" && !strings.EqualFold(record.TeamID, filter.TeamID) {
			continue
		}
		if record.Seq <= filter.AfterSeq {
			continue
		}
		records = append(records, record)
	}
	return records
}

func runtimeMailboxRecordsFromEvents(sessionID string, events []runtimeevents.Event, filter agentcontrol.MailboxRecordFilter) []agentcontrol.MailboxRecord {
	records := make([]agentcontrol.MailboxRecord, 0, len(events))
	for _, event := range events {
		if event.Type != EventMailboxReceived {
			continue
		}
		record, ok := mailboxRecordFromRuntimeEvent(sessionID, event)
		if !ok {
			continue
		}
		if filter.Workflow != "" && record.Workflow != filter.Workflow {
			continue
		}
		if filter.TeamID != "" && !strings.EqualFold(record.TeamID, filter.TeamID) {
			continue
		}
		if record.Seq <= filter.AfterSeq {
			continue
		}
		records = append(records, record)
	}
	return records
}

func mailboxRecordFromRuntimeEvent(sessionID string, event runtimeevents.Event) (agentcontrol.MailboxRecord, bool) {
	metadata, _ := event.Payload["metadata"].(map[string]interface{})
	if !agentcontrol.HasEnvelopeMetadata(metadata) {
		return agentcontrol.MailboxRecord{}, false
	}
	seq := payloadInt64(event.Payload["seq"])
	if seq <= 0 {
		seq = payloadInt64(event.Payload["mailbox_seq"])
	}
	taskID := payloadString(event.Payload["task_id"])
	record := agentcontrol.MailboxRecord{
		Seq:               seq,
		Workflow:          agentcontrol.MetadataString(metadata, agentcontrol.MetadataKeyWorkflow),
		Scope:             agentcontrol.MailboxScopeSession,
		SessionID:         strings.TrimSpace(sessionID),
		SessionMailboxSeq: payloadInt64(event.Payload["mailbox_seq"]),
		TeamID:            payloadString(event.Payload["team_id"]),
		MessageID:         payloadString(event.Payload["message_id"]),
		FromAgent:         payloadString(event.Payload["from_agent"]),
		ToAgent:           payloadString(event.Payload["to_agent"]),
		TaskID:            taskID,
		Kind:              payloadString(event.Payload["kind"]),
		Body:              payloadString(event.Payload["body"]),
		Metadata:          cloneMetadataMap(metadata),
		CreatedAt:         event.Timestamp,
	}
	if rawCreated, ok := event.Payload["created_at"]; ok {
		if createdAt, ok := payloadTime(rawCreated); ok {
			record.CreatedAt = createdAt
		}
	}
	return record.Normalize(), true
}

func cloneMetadataMap(metadata map[string]interface{}) map[string]interface{} {
	if len(metadata) == 0 {
		return map[string]interface{}{}
	}
	cloned := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func payloadString(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func payloadInt64(value interface{}) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case jsonNumber:
		parsed, _ := typed.Int64()
		return parsed
	default:
		return 0
	}
}

type jsonNumber interface {
	Int64() (int64, error)
}

func payloadTime(value interface{}) (time.Time, bool) {
	switch typed := value.(type) {
	case time.Time:
		return typed, true
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, typed)
		return parsed, err == nil
	default:
		return time.Time{}, false
	}
}

func sortRuntimeMailboxRecords(records []agentcontrol.MailboxRecord) {
	if len(records) < 2 {
		return
	}
	for i := 1; i < len(records); i++ {
		for j := i; j > 0 && runtimeMailboxRecordLess(records[j], records[j-1]); j-- {
			records[j], records[j-1] = records[j-1], records[j]
		}
	}
}

func runtimeMailboxRecordLess(left, right agentcontrol.MailboxRecord) bool {
	if left.Seq != right.Seq {
		return left.Seq < right.Seq
	}
	if left.SessionID != right.SessionID {
		return left.SessionID < right.SessionID
	}
	return left.MessageID < right.MessageID
}
