package chat

import (
	"errors"
	"fmt"
	"strings"
	"time"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Fork lineage context keys. A branch session records its parent/root/message
// lineage on Metadata.Context so persistence needs no schema migration.
// ContextForkOriginTitle snapshots the source session's display title at fork
// time so the lineage survives a later rename of the parent.
//
// These keys are intentionally distinct from the agent-control
// agent_parent_session_id key (internal/agentcontrol/registry.go), which marks
// a session as a spawned child agent in the agent-control panel.
const (
	ContextForkParentSessionID = "fork_parent_session_id"
	ContextForkRootSessionID   = "fork_root_session_id"
	ContextForkSourceMessageID = "fork_source_message_id"
	ContextForkOriginTitle     = "fork_origin_title"
	ContextForkCreatedAt       = "fork_created_at"
)

// Branch anchor sentinel errors. The API layer maps ErrBranchAnchorNotFound to
// 400 (selector does not resolve to a message) and ErrBranchAnchorNotTurnTail
// to 409 (the message exists but is not the end of a completed turn).
var (
	ErrBranchAnchorNotFound    = errors.New("branch anchor message not found")
	ErrBranchAnchorNotTurnTail = errors.New("branch anchor is not the last message of a completed turn")
)

// BranchRequest selects the fork anchor for a session branch.
type BranchRequest struct {
	// AnchorMessageID is the stable message id (message_id metadata) of a message
	// in the source session's visible history. Empty selects the whole session:
	// the prefix is every message and IncludeAnchor has no message to apply to.
	AnchorMessageID string
	// IncludeAnchor keeps the anchor message itself in the prefix. The caller
	// applies the default (true) before planning.
	IncludeAnchor bool
}

// BranchAnchor describes how the source history prefix was cut.
type BranchAnchor struct {
	// SourceMessageID echoes the requested anchor id ("" for whole-session).
	SourceMessageID string `json:"source_message_id"`
	// TurnIndex is the 0-based user-turn index the anchor belongs to (-1 when the
	// session has no user turns, e.g. a whole-session branch of empty history).
	TurnIndex int `json:"turn_index"`
	// Included reports whether the anchor message is part of the prefix.
	Included bool `json:"included"`
}

// BranchPlan is the pure outcome of PlanBranch: the new session's history
// prefix plus the resolved anchor metadata.
type BranchPlan struct {
	Anchor BranchAnchor
	Prefix []runtimetypes.Message
}

// PlanBranch computes the history prefix a branch session is seeded with.
//
// The anchor must be the last message of one completed user turn: it must not
// itself be a user message, and the following message must be a user message or
// the anchor must end the history. An anchor that cannot be resolved to a real
// message id returns ErrBranchAnchorNotFound; an anchor that resolves but is not
// the tail of its turn returns ErrBranchAnchorNotTurnTail. Neither case is
// silently truncated: the caller is expected to surface the error.
func PlanBranch(messages []runtimetypes.Message, req BranchRequest) (*BranchPlan, error) {
	anchorID := strings.TrimSpace(req.AnchorMessageID)
	if anchorID == "" {
		// No anchor message: the whole session is the prefix. IncludeAnchor has no
		// message to include/exclude, so it is deliberately ignored here.
		return &BranchPlan{
			Anchor: BranchAnchor{
				SourceMessageID: "",
				TurnIndex:       lastUserTurnIndex(messages),
				Included:        true,
			},
			Prefix: cloneBranchPrefix(messages, len(messages)),
		}, nil
	}

	index := -1
	for i := range messages {
		if runtimetypes.MessageID(messages[i]) == anchorID {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, fmt.Errorf("%w: message %q is not part of the session history", ErrBranchAnchorNotFound, anchorID)
	}

	turnIndex := -1
	turnEnd := -1
	for _, turn := range ListUserTurns(messages) {
		if index >= turn.MessageIndex && index < turn.EndMessageIndex {
			turnIndex = turn.Index
			turnEnd = turn.EndMessageIndex
			break
		}
	}
	if turnIndex < 0 || turnEnd <= 0 {
		return nil, fmt.Errorf("%w: message %q precedes the first user turn", ErrBranchAnchorNotTurnTail, anchorID)
	}
	if isUserRole(messages[index].Role) {
		return nil, fmt.Errorf("%w: message %q is a user message; the anchor must be the turn's last message",
			ErrBranchAnchorNotTurnTail, anchorID)
	}
	if index != turnEnd-1 {
		return nil, fmt.Errorf("%w: message %q is followed by %d more message(s) in turn %d",
			ErrBranchAnchorNotTurnTail, anchorID, turnEnd-1-index, turnIndex)
	}

	prefixLen := index + 1
	if !req.IncludeAnchor {
		prefixLen = index
	}
	return &BranchPlan{
		Anchor: BranchAnchor{
			SourceMessageID: anchorID,
			TurnIndex:       turnIndex,
			Included:        req.IncludeAnchor,
		},
		Prefix: cloneBranchPrefix(messages, prefixLen),
	}, nil
}

// lastUserTurnIndex returns the 0-based index of the last user turn, or -1 when
// the history contains no user message.
func lastUserTurnIndex(messages []runtimetypes.Message) int {
	turns := ListUserTurns(messages)
	if len(turns) == 0 {
		return -1
	}
	return turns[len(turns)-1].Index
}

// cloneBranchPrefix deep-copies the first length messages (defensively clamped).
// The source history is never mutated by the branch path.
func cloneBranchPrefix(messages []runtimetypes.Message, length int) []runtimetypes.Message {
	if length < 0 {
		length = 0
	}
	if length > len(messages) {
		length = len(messages)
	}
	prefix := make([]runtimetypes.Message, length)
	for i := 0; i < length; i++ {
		prefix[i] = *messages[i].Clone()
	}
	return prefix
}

// ForkRootSessionID returns the lineage root for a session: the recorded fork
// root when the session is itself a branch, otherwise the session id.
func ForkRootSessionID(session *Session) string {
	if session == nil {
		return ""
	}
	if root := contextStringValue(session.Metadata.Context, ContextForkRootSessionID); root != "" {
		return root
	}
	return strings.TrimSpace(session.ID)
}

// ApplyForkLineage records branch lineage on the receiver (a newly created
// session) using source as its direct parent. sourceMessageID is omitted when
// empty (whole-session branch without an explicit anchor). The origin title is
// snapshotted from source's display title (falling back to its id) so a branch
// keeps readable provenance even after the parent is renamed.
func (s *Session) ApplyForkLineage(source *Session, sourceMessageID string, createdAt time.Time) {
	if s == nil || source == nil {
		return
	}
	parentID := strings.TrimSpace(source.ID)
	if parentID == "" {
		return
	}
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	s.SetContext(ContextForkParentSessionID, parentID)
	s.SetContext(ContextForkRootSessionID, ForkRootSessionID(source))
	s.SetContext(ContextForkOriginTitle, forkOriginTitle(source))
	if id := strings.TrimSpace(sourceMessageID); id != "" {
		s.SetContext(ContextForkSourceMessageID, id)
	}
	s.SetContext(ContextForkCreatedAt, createdAt.UTC().Format(time.RFC3339))
}

// forkOriginTitle returns the source session's display title for fork lineage,
// falling back to its id when the source has no title yet. effectiveTitle()
// itself never falls back to the session id, so this fallback is load-bearing
// and matches the API layer's branchSourceTitle fallback.
func forkOriginTitle(source *Session) string {
	if source == nil {
		return ""
	}
	if preview := source.BuildPreview(); preview != nil {
		if title := strings.TrimSpace(preview.Title); title != "" {
			return title
		}
	}
	return strings.TrimSpace(source.ID)
}
