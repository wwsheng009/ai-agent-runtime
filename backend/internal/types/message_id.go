package types

import (
	"strings"

	"github.com/google/uuid"
)

// Metadata keys for stable chat message / turn identity.
// Stored in Message.Metadata so existing persistence schemas stay compatible.
const (
	MetadataKeyMessageID = "message_id"
	MetadataKeyTurnID    = "turn_id"
	// MetadataKeyLegacyMessageID is accepted as a read fallback only.
	MetadataKeyLegacyMessageID = "id"
)

// MessageID returns the stable message id from metadata, if present.
func MessageID(msg Message) string {
	return messageIDFromMetadata(msg.Metadata)
}

// TurnID returns the stable turn id from metadata, if present.
func TurnID(msg Message) string {
	if msg.Metadata == nil {
		return ""
	}
	return strings.TrimSpace(msg.Metadata.GetString(MetadataKeyTurnID, ""))
}

// EnsureMessageIdentity guarantees message_id (and turn_id for user messages).
// It mutates msg in place and returns whether any metadata was written.
//
// Rules:
//   - message_id: keep existing message_id / legacy id; otherwise generate msg_<uuid>
//   - turn_id for user: keep existing; otherwise generate turn_<uuid>
//   - turn_id for non-user: keep existing; else inherit previousTurnID when provided
func EnsureMessageIdentity(msg *Message, previousTurnID string) bool {
	if msg == nil {
		return false
	}
	changed := false
	if msg.Metadata == nil {
		msg.Metadata = NewMetadata()
		changed = true
	}

	messageID := messageIDFromMetadata(msg.Metadata)
	if messageID == "" {
		messageID = "msg_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		msg.Metadata.Set(MetadataKeyMessageID, messageID)
		changed = true
	} else if strings.TrimSpace(msg.Metadata.GetString(MetadataKeyMessageID, "")) == "" {
		// Promote legacy "id" into canonical message_id without regenerating.
		msg.Metadata.Set(MetadataKeyMessageID, messageID)
		changed = true
	}

	turnID := strings.TrimSpace(msg.Metadata.GetString(MetadataKeyTurnID, ""))
	if turnID == "" {
		if isUserRole(msg.Role) {
			turnID = "turn_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			msg.Metadata.Set(MetadataKeyTurnID, turnID)
			changed = true
		} else if prev := strings.TrimSpace(previousTurnID); prev != "" {
			msg.Metadata.Set(MetadataKeyTurnID, prev)
			changed = true
		}
	}
	return changed
}

// EnsureHistoryMessageIdentities assigns stable ids across a history slice.
// User messages mint a new turn_id when missing; subsequent assistant/tool/system
// messages inherit the nearest preceding turn_id. Returns whether any message changed.
func EnsureHistoryMessageIdentities(messages []Message) bool {
	if len(messages) == 0 {
		return false
	}
	changed := false
	previousTurnID := ""
	for i := range messages {
		if EnsureMessageIdentity(&messages[i], previousTurnID) {
			changed = true
		}
		if tid := TurnID(messages[i]); tid != "" {
			previousTurnID = tid
		}
	}
	return changed
}

func messageIDFromMetadata(meta Metadata) string {
	if meta == nil {
		return ""
	}
	if id := strings.TrimSpace(meta.GetString(MetadataKeyMessageID, "")); id != "" {
		return id
	}
	return strings.TrimSpace(meta.GetString(MetadataKeyLegacyMessageID, ""))
}

func isUserRole(role string) bool {
	return strings.EqualFold(strings.TrimSpace(role), "user")
}
