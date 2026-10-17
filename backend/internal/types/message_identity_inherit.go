package types

import (
	"fmt"
	"os"
	"strings"
)

// MessageSubstanceKey identifies a message by what it renders: role, content and
// the tool-call signature. It mirrors the canonical storage key
// (chat.canonicalMessageSubstanceKey) so a rebuilt transcript can recognise a
// message that already has a durable row behind it.
func MessageSubstanceKey(msg Message) string {
	return strings.ToLower(strings.TrimSpace(msg.Role)) + "\x00" + msg.Content + "\x00" + toolCallSubstanceSignature(msg)
}

func toolCallSubstanceSignature(msg Message) string {
	if len(msg.ToolCalls) == 0 {
		return ""
	}
	var builder strings.Builder
	for index := range msg.ToolCalls {
		builder.WriteString(strings.TrimSpace(msg.ToolCalls[index].ID))
		builder.WriteByte('\x1f')
		builder.WriteString(strings.TrimSpace(msg.ToolCalls[index].Name))
		builder.WriteByte('\n')
	}
	return builder.String()
}

type inheritedIdentity struct {
	messageID string
	turnID    string
}

// InheritMessageIdentities copies message_id (and turn_id) from a previously
// durable transcript onto rebuilt messages that lost their metadata.
//
// Compaction, streaming assembly and request-prefix handling rebuild the
// transcript instead of reusing the stored message objects. Those rebuilt
// messages carry no metadata, so EnsureMessageIdentity mints a fresh msg_/turn_
// for them; the persistence layer can then no longer tell them apart from new
// messages and appends a duplicate copy of an already stored turn. Matching the
// rebuilt messages back onto the previous transcript by substance keeps the
// durable identity stable across rebuilds.
//
// Rules:
//   - a message that already carries a message_id keeps it;
//   - matching is one-to-one and in order: one previous message backs at most
//     one rebuilt message, so genuinely repeated content still queues up;
//   - user messages are skipped unless includeUserTurns is set, because a user
//     message starts a new turn: reusing an older identity would swallow a real
//     repeat ("继续" sent twice) instead of persisting it.
func InheritMessageIdentities(previous, rebuilt []Message, includeUserTurns bool) bool {
	if len(previous) == 0 || len(rebuilt) == 0 {
		return false
	}

	queues := make(map[string][]inheritedIdentity, len(previous))
	for index := range previous {
		messageID := MessageID(previous[index])
		if messageID == "" {
			continue
		}
		key := MessageSubstanceKey(previous[index])
		queues[key] = append(queues[key], inheritedIdentity{messageID: messageID, turnID: TurnID(previous[index])})
	}
	if len(queues) == 0 {
		return false
	}

	changed := false
	for index := range rebuilt {
		message := &rebuilt[index]
		if MessageID(*message) != "" {
			// A rebuilt row that already carries an id still consumes its
			// substance slot. The rebuilt window usually contains rows that were
			// durably persisted earlier in this very turn (mid-turn checkpoints
			// absorb them into the caller's session); without the consumption
			// the queue still backs a LATER row of the same substance — a new
			// turn whose tool result repeats an earlier one byte-for-byte then
			// inherits the OLD id/turn and the canonical alignment drops the
			// whole new turn (live-fire 2026-10-17).
			continue
		}
		if !includeUserTurns && isUserRole(message.Role) {
			continue
		}
		key := MessageSubstanceKey(*message)
		queue := queues[key]
		if len(queue) == 0 {
			continue
		}
		next := queue[0]
		queues[key] = queue[1:]
		if message.Metadata == nil {
			message.Metadata = NewMetadata()
		}
		message.Metadata.Set(MetadataKeyMessageID, next.messageID)
		if next.turnID != "" && strings.TrimSpace(message.Metadata.GetString(MetadataKeyTurnID, "")) == "" {
			message.Metadata.Set(MetadataKeyTurnID, next.turnID)
		}
		changed = true
	}
	return changed
}

// InheritMessageIdentitiesBeforeNewTurns is the ReplaceHistory variant of
// InheritMessageIdentities: inheritance stops at the first user turn that the
// previous transcript cannot back. Everything after that boundary is a NEW
// turn appended to the window — its messages must mint fresh identities,
// because a later turn can repeat an earlier row byte-for-byte (the same
// web_search query, the same response pool) and inheriting the old id then
// makes the persistence alignment treat the whole new turn as already stored
// (live-fire 2026-10-17: a second search turn stored 1 row instead of 3, and
// its final assistant inherited the previous turn_id).
func InheritMessageIdentitiesBeforeNewTurns(previous, rebuilt []Message) bool {
	// The boundary is the first rebuilt row the previous transcript cannot
	// back: inheritance is only sound over a CONTIGUOUS backed prefix. A hole
	// anywhere (a user turn appended since the last persist, or any other new
	// row) means everything after it is new and must mint fresh identities —
	// otherwise a later row that repeats an earlier one byte-for-byte inherits
	// the OLD id/turn behind the hole (live-fire 2026-10-17: user2 had already
	// been persisted by a mid-turn checkpoint, so the user-only boundary check
	// passed, and the new turn's tool result then inherited turn 1's identity).
	queues := make(map[string][]inheritedIdentity, len(previous))
	for index := range previous {
		messageID := MessageID(previous[index])
		if messageID == "" {
			continue
		}
		queues[MessageSubstanceKey(previous[index])] = append(queues[MessageSubstanceKey(previous[index])], inheritedIdentity{
			messageID: messageID,
			turnID:    TurnID(previous[index]),
		})
	}
	// One ordered pass: consume the backing queue row-by-row and stamp the
	// assignment in place. Rows the caller already stamped consume their slot
	// but keep their identity. The first unbacked row ends the backed prefix —
	// everything after it is new (delegated to EnsureMessageIdentity by the
	// caller, which mints fresh ids for id-less rows).
	cut := 0
	for index := range rebuilt {
		key := MessageSubstanceKey(rebuilt[index])
		if queue := queues[key]; len(queue) > 0 {
			cut = index + 1
			message := &rebuilt[index]
			if MessageID(*message) != "" {
				continue
			}
			next := queue[0]
			queues[key] = queue[1:]
			if message.Metadata == nil {
				message.Metadata = NewMetadata()
			}
			message.Metadata.Set(MetadataKeyMessageID, next.messageID)
			if next.turnID != "" && strings.TrimSpace(message.Metadata.GetString(MetadataKeyTurnID, "")) == "" {
				message.Metadata.Set(MetadataKeyTurnID, next.turnID)
			}
			continue
		}
		cut = index
		break
	}
	if os.Getenv("AICLI_DEBUG_IDENTITY_INHERIT") != "" {
		fmt.Printf("[identity-inherit] previous=%d rebuilt=%d cut=%d\n", len(previous), len(rebuilt), cut)
	}
	return cut > 0
}
