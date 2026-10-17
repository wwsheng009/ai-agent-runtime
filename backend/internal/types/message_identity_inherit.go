package types

import "strings"

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

	type inheritedIdentity struct {
		messageID string
		turnID    string
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
	// The boundary is the first user turn whose substance the previous
	// transcript cannot back (unconsumed queue): that user message opens a new
	// turn appended to the window, and everything after it — including a tool
	// result that repeats an earlier row byte-for-byte — belongs to the new
	// turn and must mint fresh identities.
	userQueue := make(map[string][]string)
	for index := range previous {
		if !isUserRole(previous[index].Role) {
			continue
		}
		if id := MessageID(previous[index]); id != "" {
			key := MessageSubstanceKey(previous[index])
			userQueue[key] = append(userQueue[key], id)
		}
	}
	cut := len(rebuilt)
	for index := range rebuilt {
		if !isUserRole(rebuilt[index].Role) {
			continue
		}
		key := MessageSubstanceKey(rebuilt[index])
		if queue := userQueue[key]; len(queue) > 0 {
			userQueue[key] = queue[1:]
			continue
		}
		cut = index
		break
	}
	return InheritMessageIdentities(previous, rebuilt[:cut], false)
}
