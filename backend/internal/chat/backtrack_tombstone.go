package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	// ContextBacktrackAuditLog is the Metadata.Context key that stores a
	// bounded ring of lightweight backtrack tombstones. Physical history is
	// still truncated; these records only keep audit/summary metadata so
	// operators and UIs can explain what was removed.
	ContextBacktrackAuditLog = "backtrack_audit_log"

	// MaxBacktrackAuditLogEntries caps how many tombstones are retained per session.
	MaxBacktrackAuditLogEntries = 20

	// MaxBacktrackTombstoneIDs caps how many removed message/turn ids are stored
	// per tombstone (oldest removed first; overflow is dropped from the tail).
	MaxBacktrackTombstoneIDs = 32
)

// BacktrackTombstone is a durable audit summary of one physical history truncation.
// It intentionally does not retain full message bodies.
type BacktrackTombstone struct {
	ID                      string    `json:"id"`
	CreatedAt               time.Time `json:"created_at"`
	SessionID               string    `json:"session_id,omitempty"`
	Mode                    string    `json:"mode,omitempty"`
	Reason                  string    `json:"reason,omitempty"`
	UserTurnIndex           int       `json:"user_turn_index"`
	MessageIndex            int       `json:"message_index"`
	MessageID               string    `json:"message_id,omitempty"`
	AnchorPreview           string    `json:"anchor_preview,omitempty"`
	TruncatedToMessageCount int       `json:"truncated_to_message_count"`
	RemovedMessageCount     int       `json:"removed_message_count"`
	RemovedUserTurns        int       `json:"removed_user_turns"`
	// PriorMessageCount is the visible history length before truncation.
	PriorMessageCount int `json:"prior_message_count,omitempty"`
	// RemovedMessageIDs lists stable message_ids that were truncated (capped).
	RemovedMessageIDs []string `json:"removed_message_ids,omitempty"`
	// RemovedTurnIDs lists unique turn_ids that were truncated (capped).
	RemovedTurnIDs   []string `json:"removed_turn_ids,omitempty"`
	Edited           bool     `json:"edited,omitempty"`
	IncludeAnchor    bool     `json:"include_anchor,omitempty"`
	BaseCheckpointID string   `json:"base_checkpoint_id,omitempty"`
	// LaterCheckpointIDs lists checkpoints after the anchor at plan time (capped).
	LaterCheckpointIDs []string `json:"later_checkpoint_ids,omitempty"`
}

// buildBacktrackTombstone constructs a lightweight audit record for a planned truncation.
// removed are the messages that will leave the visible history.
func buildBacktrackTombstone(sessionID string, plan *backtrackPlan, removed []runtimetypes.Message, priorCount int) *BacktrackTombstone {
	if plan == nil {
		return nil
	}
	// Code-only mode does not truncate conversation history.
	if plan.Mode == BacktrackModeCode {
		return nil
	}
	if plan.RemovedMessageCount <= 0 && len(removed) == 0 {
		return nil
	}

	msgIDs, turnIDs := collectRemovedIdentities(removed)
	later := append([]string(nil), plan.LaterCheckpointIDs...)
	if len(later) > MaxBacktrackTombstoneIDs {
		later = later[:MaxBacktrackTombstoneIDs]
	}

	return &BacktrackTombstone{
		ID:                      "bt_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		CreatedAt:               time.Now().UTC(),
		SessionID:               strings.TrimSpace(sessionID),
		Mode:                    plan.Mode,
		Reason:                  BacktrackReasonUserTurn,
		UserTurnIndex:           plan.UserTurnIndex,
		MessageIndex:            plan.MessageIndex,
		MessageID:               plan.MessageID,
		AnchorPreview:           plan.AnchorPreview,
		TruncatedToMessageCount: plan.PrefixLen,
		RemovedMessageCount:     plan.RemovedMessageCount,
		RemovedUserTurns:        plan.RemovedUserTurns,
		PriorMessageCount:       priorCount,
		RemovedMessageIDs:       msgIDs,
		RemovedTurnIDs:          turnIDs,
		Edited:                  strings.TrimSpace(plan.EditedPrompt) != "",
		IncludeAnchor:           plan.IncludeAnchor,
		BaseCheckpointID:        plan.BaseCheckpointID,
		LaterCheckpointIDs:      later,
	}
}

func collectRemovedIdentities(removed []runtimetypes.Message) (messageIDs []string, turnIDs []string) {
	if len(removed) == 0 {
		return nil, nil
	}
	messageIDs = make([]string, 0, minInt(len(removed), MaxBacktrackTombstoneIDs))
	seenTurns := make(map[string]struct{}, len(removed))
	for _, msg := range removed {
		if len(messageIDs) < MaxBacktrackTombstoneIDs {
			if id := runtimetypes.MessageID(msg); id != "" {
				messageIDs = append(messageIDs, id)
			}
		}
		if tid := runtimetypes.TurnID(msg); tid != "" {
			if _, ok := seenTurns[tid]; ok {
				continue
			}
			seenTurns[tid] = struct{}{}
			if len(turnIDs) < MaxBacktrackTombstoneIDs {
				turnIDs = append(turnIDs, tid)
			}
		}
	}
	if len(messageIDs) == 0 {
		messageIDs = nil
	}
	if len(turnIDs) == 0 {
		turnIDs = nil
	}
	return messageIDs, turnIDs
}

// AppendBacktrackTombstone records a tombstone on the session metadata ring buffer.
// Returns true when the session was mutated.
func AppendBacktrackTombstone(session *Session, tombstone *BacktrackTombstone) bool {
	if session == nil || tombstone == nil {
		return false
	}
	if strings.TrimSpace(tombstone.ID) == "" {
		tombstone.ID = "bt_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if tombstone.CreatedAt.IsZero() {
		tombstone.CreatedAt = time.Now().UTC()
	}
	if strings.TrimSpace(tombstone.SessionID) == "" {
		tombstone.SessionID = session.ID
	}
	if strings.TrimSpace(tombstone.Reason) == "" {
		tombstone.Reason = BacktrackReasonUserTurn
	}

	entries := ListBacktrackTombstones(session)
	entries = append(entries, *tombstone)
	if len(entries) > MaxBacktrackAuditLogEntries {
		entries = entries[len(entries)-MaxBacktrackAuditLogEntries:]
	}
	encoded, err := encodeBacktrackTombstones(entries)
	if err != nil {
		return false
	}
	session.SetContext(ContextBacktrackAuditLog, encoded)
	return true
}

// ListBacktrackTombstones returns durable audit tombstones oldest-first.
func ListBacktrackTombstones(session *Session) []BacktrackTombstone {
	if session == nil || session.Metadata.Context == nil {
		return nil
	}
	raw, ok := session.Metadata.Context[ContextBacktrackAuditLog]
	if !ok || raw == nil {
		return nil
	}
	entries, err := decodeBacktrackTombstones(raw)
	if err != nil {
		return nil
	}
	return entries
}

// LatestBacktrackTombstone returns the most recent audit tombstone when present.
func LatestBacktrackTombstone(session *Session) *BacktrackTombstone {
	entries := ListBacktrackTombstones(session)
	if len(entries) == 0 {
		return nil
	}
	last := entries[len(entries)-1]
	return &last
}

func encodeBacktrackTombstones(entries []BacktrackTombstone) ([]interface{}, error) {
	if len(entries) == 0 {
		return []interface{}{}, nil
	}
	// Round-trip through JSON so Metadata.Context stays JSON-serializable maps/slices.
	raw, err := json.Marshal(entries)
	if err != nil {
		return nil, err
	}
	var encoded []interface{}
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

func decodeBacktrackTombstones(raw interface{}) ([]BacktrackTombstone, error) {
	switch v := raw.(type) {
	case []BacktrackTombstone:
		return append([]BacktrackTombstone(nil), v...), nil
	case []*BacktrackTombstone:
		out := make([]BacktrackTombstone, 0, len(v))
		for _, item := range v {
			if item == nil {
				continue
			}
			out = append(out, *item)
		}
		return out, nil
	default:
		// Accept []interface{}, []map[string]interface{}, JSON string, etc.
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("marshal backtrack tombstones: %w", err)
		}
		var out []BacktrackTombstone
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, fmt.Errorf("decode backtrack tombstones: %w", err)
		}
		return out, nil
	}
}

func (t *BacktrackTombstone) toEventMap() map[string]interface{} {
	if t == nil {
		return nil
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return map[string]interface{}{
			"id":         t.ID,
			"created_at": t.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]interface{}{
			"id":         t.ID,
			"created_at": t.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	return out
}

func cloneBacktrackTombstone(src *BacktrackTombstone) *BacktrackTombstone {
	if src == nil {
		return nil
	}
	cp := *src
	if src.RemovedMessageIDs != nil {
		cp.RemovedMessageIDs = append([]string(nil), src.RemovedMessageIDs...)
	}
	if src.RemovedTurnIDs != nil {
		cp.RemovedTurnIDs = append([]string(nil), src.RemovedTurnIDs...)
	}
	if src.LaterCheckpointIDs != nil {
		cp.LaterCheckpointIDs = append([]string(nil), src.LaterCheckpointIDs...)
	}
	return &cp
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
