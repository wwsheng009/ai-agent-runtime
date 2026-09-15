// Package dedupe implements the pure duplicate-row detection used by the
// session-dedupe maintenance command.
//
// It deliberately carries no database, filesystem or runtime-chat dependency:
// the command layer reads rows, this package decides which rows are surplus,
// and only the caller performs a deletion. Keeping the rule pure makes it
// directly unit-testable and keeps the write path small enough to audit.
//
// The substance identity stays byte-for-byte compatible with
// chat.canonicalMessageSubstanceKey / chat.toolCallSignature in
// backend/internal/chat/sqlite_storage.go:
//
//	key = lower(trim(Role)) + "\x00" + Content + "\x00" + toolCallSignature
//	toolCallSignature = for each call: trim(ID) + "\x1f" + trim(Name) + "\n"
//
// Note that the reference signature intentionally ignores ToolCall.Args and
// Message.ContentParts, so two rows that differ only in those fields share a
// substance key. That is the requested judgement rule; it is documented here so
// the residual risk stays visible.
package dedupe

import (
	"encoding/json"
	"strings"
	"time"
)

// Row is the minimal projection of one session_messages row required by the
// planner. Seq and CreatedAt come from the row primary key / created_at column,
// Payload is the raw payload_json value (nil when the body was spilled to an
// artifact file that could not be read).
type Row struct {
	Seq       int64
	CreatedAt string
	Payload   []byte
}

// Message mirrors the subset of types.Message JSON needed for substance
// identity. Every other field (metadata, context_stage, content_parts, ...) is
// intentionally ignored.
type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls"`
	// Metadata carries the write identity. It is not part of the substance key:
	// it decides whether two equal-substance rows can come from the same write
	// at all (see Rule).
	Metadata Metadata `json:"metadata"`
}

// Metadata is the identity subset of types.Message metadata that the planner
// needs.
type Metadata struct {
	// TurnID groups every message one turn produced. A turn persists a given
	// message once, so two equal-substance rows inside one turn are a re-append.
	// The same text in two different turns is a repeat the user really made and
	// must survive.
	TurnID string `json:"turn_id"`
}

// ToolCall mirrors types.ToolCall. Args is part of the decoded payload but not
// part of the substance identity.
type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Group is one retained row plus the consecutive surplus rows that duplicate
// it. KeepSeq/KeepAt describe the row that stays, DropSeqs the rows that are
// surplus (always ordered by seq, always non-empty).
type Group struct {
	KeepSeq  int64
	KeepAt   time.Time
	DropSeqs []int64
}

// Plan is the complete, side-effect free decision for one session.
type Plan struct {
	// Groups lists every duplicate group in transcript order.
	Groups []Group
	// SurplusSeqs lists every row planned for deletion, ordered by seq.
	SurplusSeqs []int64
	// Scanned is the number of input rows.
	Scanned int
	// Skipped is the number of retained rows whose payload or timestamp could
	// not be interpreted; such a row is never deleted and it breaks the
	// duplicate chain around it.
	Skipped int
}

// Rule selects which equal-substance neighbours may be folded.
type Rule struct {
	// Window is the maximum age gap between the retained row and a surplus row.
	Window time.Duration
	// AnyTurn folds equal-substance neighbours of different turns as well. That
	// was the first rule and it also collapses rows the user really produced
	// twice — the same prompt re-sent after a failed answer, "继续" sent again a
	// minute later — so it is opt-in, not the default.
	AnyTurn bool
}

// SurplusCount returns the number of rows planned for deletion.
func (p Plan) SurplusCount() int { return len(p.SurplusSeqs) }

// SubstanceKey builds the duplicate identity of a decoded message. It must stay
// identical to chat.canonicalMessageSubstanceKey.
func SubstanceKey(message Message) string {
	return strings.ToLower(strings.TrimSpace(message.Role)) + "\x00" + message.Content + "\x00" + ToolCallSignature(message.ToolCalls)
}

// ToolCallSignature builds the tool-call part of the substance key. It must stay
// identical to chat.toolCallSignature.
func ToolCallSignature(toolCalls []ToolCall) string {
	if len(toolCalls) == 0 {
		return ""
	}
	var builder strings.Builder
	for index := range toolCalls {
		builder.WriteString(strings.TrimSpace(toolCalls[index].ID))
		builder.WriteByte('\x1f')
		builder.WriteString(strings.TrimSpace(toolCalls[index].Name))
		builder.WriteByte('\n')
	}
	return builder.String()
}

// Build applies a plain window rule: equal substance plus a small enough age
// gap, whatever turn the rows belong to. It is kept for the loose semantics;
// the command line tool uses BuildWithRule so the turn identity is honoured.
func Build(rows []Row, window time.Duration) Plan {
	return BuildWithRule(rows, Rule{Window: window, AnyTurn: true})
}

// BuildWithRule decides which rows are surplus duplicates.
//
// Rows are expected in transcript order (seq ascending). A row is surplus when
// its substance equals the substance of the last *retained* row, its created_at
// is within Rule.Window of that retained row's created_at, and the rule allows
// folding the two turns together (Rule.AnyTurn or one shared turn id). The
// retained row is the anchor: surplus rows never become an anchor themselves,
// so a run of N identical rows collapses to exactly one row.
//
// Rows whose payload cannot be decoded, or whose created_at cannot be parsed,
// are always kept, are reported in Skipped, and reset the anchor, so an
// unreadable row can never cause a neighbouring row to be folded.
func BuildWithRule(rows []Row, rule Rule) Plan {
	window := rule.Window
	plan := Plan{Scanned: len(rows)}
	if window < 0 {
		window = -window
	}

	var (
		haveAnchor   bool
		anchorKey    string
		anchorAt     time.Time
		anchorTurn   string
		currentGroup = -1
	)
	for _, row := range rows {
		message, at, ok := decodeRow(row)
		if !ok {
			plan.Skipped++
			haveAnchor = false
			currentGroup = -1
			continue
		}
		key := SubstanceKey(message)
		if haveAnchor && key == anchorKey && withinWindow(anchorAt, at, window) &&
			sameWrite(anchorTurn, message.Metadata.TurnID, rule.AnyTurn) {
			plan.Groups[currentGroup].DropSeqs = append(plan.Groups[currentGroup].DropSeqs, row.Seq)
			plan.SurplusSeqs = append(plan.SurplusSeqs, row.Seq)
			continue
		}
		haveAnchor = true
		anchorKey = key
		anchorAt = at
		anchorTurn = message.Metadata.TurnID
		plan.Groups = append(plan.Groups, Group{KeepSeq: row.Seq, KeepAt: at})
		currentGroup = len(plan.Groups) - 1
	}
	return plan
}

// sameWrite reports whether a candidate row may be folded onto its anchor.
//
// With AnyTurn the substance and the window decide. Otherwise both rows must
// name the same turn: a turn persists each of its messages once, so an
// equal-substance neighbour inside one turn is the re-append this tool exists
// for. Rows without a turn id cannot be attributed to a write at all, so they
// are never folded — a genuine repeated prompt carries a different turn id and
// survives.
func sameWrite(anchorTurn, candidateTurn string, anyTurn bool) bool {
	if anyTurn {
		return true
	}
	anchorTurn = strings.TrimSpace(anchorTurn)
	candidateTurn = strings.TrimSpace(candidateTurn)
	if anchorTurn == "" || candidateTurn == "" {
		return false
	}
	return anchorTurn == candidateTurn
}

// decodeRow parses one row into the decoded message plus its timestamp. ok is
// false when the payload or the timestamp cannot be interpreted, which makes
// the row ineligible for folding in either direction.
func decodeRow(row Row) (Message, time.Time, bool) {
	var message Message
	if len(row.Payload) == 0 {
		return Message{}, time.Time{}, false
	}
	if err := json.Unmarshal(row.Payload, &message); err != nil {
		return Message{}, time.Time{}, false
	}
	at, ok := parseCreatedAt(row.CreatedAt)
	if !ok {
		return Message{}, time.Time{}, false
	}
	return message, at, true
}

// createdAtLayouts lists the accepted created_at encodings. The runtime writes
// time.RFC3339Nano, the other layouts keep legacy/odd rows comparable instead
// of silently skipping them.
var createdAtLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05",
}

func parseCreatedAt(value string) (time.Time, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, false
	}
	for _, layout := range createdAtLayouts {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// withinWindow reports whether two timestamps are at most window apart. The
// absolute difference is used so an out-of-order or clock-skewed timestamp
// inside the window still collapses instead of splitting the group.
func withinWindow(anchor, candidate time.Time, window time.Duration) bool {
	diff := anchor.Sub(candidate)
	if diff < 0 {
		diff = -diff
	}
	return diff <= window
}
