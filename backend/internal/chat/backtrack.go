package chat

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/checkpoint"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Backtrack modes for SessionActor.Backtrack.
const (
	BacktrackModeConversation = "conversation"
	BacktrackModeBoth         = "both"
	BacktrackModeCode         = "code"
)

// BacktrackReasonUserTurn is the event payload reason for user-turn backtrack.
const BacktrackReasonUserTurn = "user_turn_backtrack"

// UserTurn describes one user-authored turn in the visible session history.
type UserTurn struct {
	// Index is the 0-based index among user messages in visible history.
	Index int `json:"index"`
	// MessageIndex is the index of the user message in visible history.
	MessageIndex int `json:"message_index"`
	// Preview is a short text preview of the user message content.
	Preview string `json:"preview"`
	// EndMessageIndex is exclusive end of this turn in visible history
	// (next user message index, or len(messages)).
	EndMessageIndex int `json:"end_message_index"`
	// MessageID is the stable message metadata id when available.
	MessageID string `json:"message_id,omitempty"`
	// TurnID is the stable turn metadata id when available (user message turn).
	TurnID string `json:"turn_id,omitempty"`
	// HasLaterMutation is true when at least one checkpoint has MessageCount > MessageIndex.
	HasLaterMutation bool `json:"has_later_mutation,omitempty"`
	// CheckpointIDs lists later checkpoints (MessageCount > MessageIndex), oldest first.
	CheckpointIDs []string `json:"checkpoint_ids,omitempty"`
	// BaseCheckpointID is the latest checkpoint with MessageCount <= MessageIndex.
	// Used as the code restore target for mode=both/code.
	BaseCheckpointID string `json:"base_checkpoint_id,omitempty"`
}

// BacktrackRequest selects a user-turn anchor and describes how to rewind.
type BacktrackRequest struct {
	// UserTurnIndex selects the Nth user message (0-based) in visible history.
	UserTurnIndex *int `json:"user_turn_index,omitempty"`
	// MessageIndex selects a visible-history absolute message index that must be a user message.
	MessageIndex *int `json:"message_index,omitempty"`
	// MessageID selects by stable message metadata id when available.
	MessageID string `json:"message_id,omitempty"`
	// Mode controls restore scope: conversation (default), both, or code.
	Mode string `json:"mode,omitempty"`
	// EditPrompt, when non-empty, is the replacement prompt text associated with the anchor.
	// History is truncated before the anchor; the text is returned for composer prefill
	// and optionally auto-submitted.
	EditPrompt string `json:"edit_prompt,omitempty"`
	// AutoSubmit, when true, appends EditPrompt (or the original anchor text when
	// EditPrompt is empty) as a new user message and starts a new turn after truncation.
	AutoSubmit bool `json:"auto_submit,omitempty"`
	// IncludeAnchor keeps the original anchor user message in history.
	// Ignored when EditPrompt is set or AutoSubmit is true (anchor is replaced by the new prompt).
	IncludeAnchor bool `json:"include_anchor,omitempty"`
	// PreviewOnly builds the plan without mutating session state.
	PreviewOnly bool `json:"preview_only,omitempty"`
}

// BacktrackResult is the outcome of a user-turn backtrack (or preview).
type BacktrackResult struct {
	SessionID               string   `json:"session_id"`
	Mode                    string   `json:"mode"`
	UserTurnIndex           int      `json:"user_turn_index"`
	MessageIndex            int      `json:"message_index"`
	MessageID               string   `json:"message_id,omitempty"`
	TruncatedToMessageCount int      `json:"truncated_to_message_count"`
	RemovedMessageCount     int      `json:"removed_message_count"`
	RemovedUserTurns        int      `json:"removed_user_turns"`
	AnchorPreview           string   `json:"anchor_preview,omitempty"`
	EditedPrompt            string   `json:"edited_prompt,omitempty"`
	ComposerPrompt          string   `json:"composer_prompt,omitempty"`
	IncludeAnchor           bool     `json:"include_anchor,omitempty"`
	AutoSubmitted           bool     `json:"auto_submitted,omitempty"`
	PreviewOnly             bool     `json:"preview_only,omitempty"`
	BaseCheckpointID        string   `json:"base_checkpoint_id,omitempty"`
	LaterCheckpointIDs      []string `json:"later_checkpoint_ids,omitempty"`
	// Tombstone is the durable audit summary written on successful conversation truncation.
	// Preview-only requests leave this empty; code-only mode also omits it.
	Tombstone     *BacktrackTombstone       `json:"tombstone,omitempty"`
	CodeRestore   *checkpoint.RestoreResult `json:"code_restore,omitempty"`
	Warnings      []string                  `json:"warnings,omitempty"`
	EventsEmitted []string                  `json:"events_emitted,omitempty"`
}

// backtrackPlan is the pure planning result used by preview and apply paths.
type backtrackPlan struct {
	Mode                string
	UserTurnIndex       int
	MessageIndex        int
	MessageID           string
	PrefixLen           int
	RemovedMessageCount int
	RemovedUserTurns    int
	AnchorPreview       string
	AnchorContent       string
	EditedPrompt        string
	ComposerPrompt      string
	IncludeAnchor       bool
	AutoSubmit          bool
	SubmitPrompt        string
	BaseCheckpointID    string
	LaterCheckpointIDs  []string
	HasLaterMutation    bool
	Prefix              []runtimetypes.Message
	Warnings            []string
}

// ListUserTurns returns user turns from visible history messages.
// Optional checkpoints annotate mutation coverage for UI/CLI selection.
func ListUserTurns(messages []runtimetypes.Message, checkpoints ...[]artifact.Checkpoint) []UserTurn {
	if len(messages) == 0 {
		return nil
	}
	var cps []artifact.Checkpoint
	if len(checkpoints) > 0 {
		cps = checkpoints[0]
	}
	turns := make([]UserTurn, 0)
	for i, msg := range messages {
		if !isUserRole(msg.Role) {
			continue
		}
		turn := UserTurn{
			Index:           len(turns),
			MessageIndex:    i,
			Preview:         previewMessageContent(msg.Content, 120),
			EndMessageIndex: len(messages),
			MessageID:       messageIDFromMessage(msg),
			TurnID:          runtimetypes.TurnID(msg),
		}
		annotateUserTurnCheckpoints(&turn, cps)
		turns = append(turns, turn)
	}
	for i := 0; i+1 < len(turns); i++ {
		turns[i].EndMessageIndex = turns[i+1].MessageIndex
	}
	return turns
}

func isUserRole(role string) bool {
	return strings.EqualFold(strings.TrimSpace(role), "user")
}

func previewMessageContent(content string, limit int) string {
	content = strings.Join(strings.Fields(strings.TrimSpace(content)), " ")
	if content == "" {
		return "(empty)"
	}
	if limit <= 0 {
		return content
	}
	runes := []rune(content)
	if len(runes) <= limit {
		return content
	}
	return string(runes[:limit]) + "…"
}

func messageIDFromMessage(msg runtimetypes.Message) string {
	return runtimetypes.MessageID(msg)
}

func normalizeBacktrackMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return BacktrackModeConversation, nil
	}
	switch mode {
	case BacktrackModeConversation, BacktrackModeBoth, BacktrackModeCode:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported backtrack mode: %s", mode)
	}
}

func resolveUserTurn(messages []runtimetypes.Message, req BacktrackRequest) (UserTurn, error) {
	turns := ListUserTurns(messages)
	if len(turns) == 0 {
		return UserTurn{}, fmt.Errorf("session has no user turns")
	}

	if id := strings.TrimSpace(req.MessageID); id != "" {
		for _, turn := range turns {
			if turn.MessageID == id {
				if req.UserTurnIndex != nil && *req.UserTurnIndex != turn.Index {
					return UserTurn{}, fmt.Errorf("user_turn_index %d does not match message_id %q", *req.UserTurnIndex, id)
				}
				if req.MessageIndex != nil && *req.MessageIndex != turn.MessageIndex {
					return UserTurn{}, fmt.Errorf("message_index %d does not match message_id %q", *req.MessageIndex, id)
				}
				return turn, nil
			}
		}
		// Synthetic frontend ids (e.g. session-history-N) or stale ids: fall back
		// to index selectors when present so backtrack still works pre-backfill.
		if req.UserTurnIndex != nil || req.MessageIndex != nil {
			turn, err := resolveUserTurnByIndex(messages, turns, req)
			if err == nil {
				return turn, nil
			}
		}
		return UserTurn{}, fmt.Errorf("user turn not found for message_id %q", id)
	}

	return resolveUserTurnByIndex(messages, turns, req)
}

func resolveUserTurnByIndex(messages []runtimetypes.Message, turns []UserTurn, req BacktrackRequest) (UserTurn, error) {
	switch {
	case req.UserTurnIndex != nil && req.MessageIndex != nil:
		turnIdx := *req.UserTurnIndex
		msgIdx := *req.MessageIndex
		if turnIdx < 0 || turnIdx >= len(turns) {
			return UserTurn{}, fmt.Errorf("user_turn_index %d out of range [0, %d)", turnIdx, len(turns))
		}
		turn := turns[turnIdx]
		if turn.MessageIndex != msgIdx {
			return UserTurn{}, fmt.Errorf("message_index %d does not match user_turn_index %d (expected %d)", msgIdx, turnIdx, turn.MessageIndex)
		}
		return turn, nil
	case req.UserTurnIndex != nil:
		turnIdx := *req.UserTurnIndex
		if turnIdx < 0 || turnIdx >= len(turns) {
			return UserTurn{}, fmt.Errorf("user_turn_index %d out of range [0, %d)", turnIdx, len(turns))
		}
		return turns[turnIdx], nil
	case req.MessageIndex != nil:
		msgIdx := *req.MessageIndex
		if msgIdx < 0 || msgIdx >= len(messages) {
			return UserTurn{}, fmt.Errorf("message_index %d out of range [0, %d)", msgIdx, len(messages))
		}
		if !isUserRole(messages[msgIdx].Role) {
			return UserTurn{}, fmt.Errorf("user turn not found for message_index %d (not a user message)", msgIdx)
		}
		for _, turn := range turns {
			if turn.MessageIndex == msgIdx {
				return turn, nil
			}
		}
		return UserTurn{}, fmt.Errorf("user turn not found for message_index %d", msgIdx)
	default:
		return UserTurn{}, fmt.Errorf("user_turn_index, message_index, or message_id is required")
	}
}

// PlanBacktrack is the public pure planner used by tests and non-actor callers.
// It returns the result metadata and the history prefix that would be applied.
func PlanBacktrack(sessionID string, messages []runtimetypes.Message, checkpoints []artifact.Checkpoint, req BacktrackRequest) (*BacktrackResult, []runtimetypes.Message, error) {
	plan, err := planBacktrackWithCheckpoints(messages, checkpoints, req)
	if err != nil {
		return nil, nil, err
	}
	result := plan.toResult(sessionID, req.PreviewOnly)
	prefix := append([]runtimetypes.Message(nil), plan.Prefix...)
	return result, prefix, nil
}

func planBacktrack(messages []runtimetypes.Message, req BacktrackRequest) (*backtrackPlan, error) {
	return planBacktrackWithCheckpoints(messages, nil, req)
}

func planBacktrackWithCheckpoints(messages []runtimetypes.Message, checkpoints []artifact.Checkpoint, req BacktrackRequest) (*backtrackPlan, error) {
	mode, err := normalizeBacktrackMode(req.Mode)
	if err != nil {
		return nil, err
	}
	turn, err := resolveUserTurn(messages, req)
	if err != nil {
		return nil, err
	}
	// Re-annotate with provided checkpoints (resolveUserTurn uses unannotated list).
	annotateUserTurnCheckpoints(&turn, checkpoints)

	anchor := messages[turn.MessageIndex]
	anchorContent := strings.TrimSpace(anchor.Content)
	editPrompt := strings.TrimSpace(req.EditPrompt)

	includeAnchor := req.IncludeAnchor && editPrompt == "" && !req.AutoSubmit
	prefixLen := turn.MessageIndex
	if includeAnchor {
		prefixLen = turn.MessageIndex + 1
	}
	if prefixLen < 0 {
		prefixLen = 0
	}
	if prefixLen > len(messages) {
		prefixLen = len(messages)
	}

	prefix := make([]runtimetypes.Message, prefixLen)
	for i := 0; i < prefixLen; i++ {
		prefix[i] = *messages[i].Clone()
	}

	removedMessages := len(messages) - prefixLen
	if removedMessages < 0 {
		removedMessages = 0
	}
	totalTurns := len(ListUserTurns(messages))
	removedUserTurns := totalTurns - turn.Index
	if includeAnchor {
		removedUserTurns = totalTurns - turn.Index - 1
	}
	if removedUserTurns < 0 {
		removedUserTurns = 0
	}

	composerPrompt := editPrompt
	if composerPrompt == "" {
		composerPrompt = anchorContent
	}

	submitPrompt := ""
	if req.AutoSubmit {
		submitPrompt = composerPrompt
		if strings.TrimSpace(submitPrompt) == "" {
			return nil, fmt.Errorf("auto_submit requires a non-empty edit_prompt or anchor content")
		}
	}

	plan := &backtrackPlan{
		Mode:                mode,
		UserTurnIndex:       turn.Index,
		MessageIndex:        turn.MessageIndex,
		MessageID:           turn.MessageID,
		PrefixLen:           prefixLen,
		RemovedMessageCount: removedMessages,
		RemovedUserTurns:    removedUserTurns,
		AnchorPreview:       turn.Preview,
		AnchorContent:       anchorContent,
		EditedPrompt:        editPrompt,
		ComposerPrompt:      composerPrompt,
		IncludeAnchor:       includeAnchor,
		AutoSubmit:          req.AutoSubmit,
		SubmitPrompt:        submitPrompt,
		BaseCheckpointID:    turn.BaseCheckpointID,
		LaterCheckpointIDs:  append([]string(nil), turn.CheckpointIDs...),
		HasLaterMutation:    turn.HasLaterMutation,
		Prefix:              prefix,
	}

	switch mode {
	case BacktrackModeBoth, BacktrackModeCode:
		if turn.BaseCheckpointID == "" {
			plan.Warnings = append(plan.Warnings, "no file checkpoints at or before anchor; code restore skipped")
		} else if !turn.HasLaterMutation {
			plan.Warnings = append(plan.Warnings, "no file checkpoints after anchor; code restore may be a no-op")
		}
	}

	return plan, nil
}

func (p *backtrackPlan) toResult(sessionID string, previewOnly bool) *BacktrackResult {
	if p == nil {
		return nil
	}
	return &BacktrackResult{
		SessionID:               sessionID,
		Mode:                    p.Mode,
		UserTurnIndex:           p.UserTurnIndex,
		MessageIndex:            p.MessageIndex,
		MessageID:               p.MessageID,
		TruncatedToMessageCount: p.PrefixLen,
		RemovedMessageCount:     p.RemovedMessageCount,
		RemovedUserTurns:        p.RemovedUserTurns,
		AnchorPreview:           p.AnchorPreview,
		EditedPrompt:            p.EditedPrompt,
		ComposerPrompt:          p.ComposerPrompt,
		IncludeAnchor:           p.IncludeAnchor,
		AutoSubmitted:           false,
		PreviewOnly:             previewOnly,
		BaseCheckpointID:        p.BaseCheckpointID,
		LaterCheckpointIDs:      append([]string(nil), p.LaterCheckpointIDs...),
		Warnings:                append([]string(nil), p.Warnings...),
	}
}

func annotateUserTurnCheckpoints(turn *UserTurn, checkpoints []artifact.Checkpoint) {
	if turn == nil || len(checkpoints) == 0 {
		return
	}
	sorted := append([]artifact.Checkpoint(nil), checkpoints...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ci, cj := checkpointMessageCount(sorted[i]), checkpointMessageCount(sorted[j])
		if ci != cj {
			return ci < cj
		}
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
		}
		return sorted[i].ID < sorted[j].ID
	})
	later := make([]string, 0)
	var baseID string
	for _, cp := range sorted {
		id := strings.TrimSpace(cp.ID)
		if id == "" {
			continue
		}
		count := checkpointMessageCount(cp)
		if count <= turn.MessageIndex {
			baseID = id
			continue
		}
		later = append(later, id)
	}
	turn.BaseCheckpointID = baseID
	turn.CheckpointIDs = later
	turn.HasLaterMutation = len(later) > 0
}

func checkpointMessageCount(cp artifact.Checkpoint) int {
	if cp.MessageCount > 0 {
		return cp.MessageCount
	}
	if cp.Metadata == nil {
		return 0
	}
	switch v := cp.Metadata["message_count"].(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	case float32:
		return int(v)
	case string:
		var n int
		_, _ = fmt.Sscanf(strings.TrimSpace(v), "%d", &n)
		return n
	default:
		return 0
	}
}

// IntPtr is a small helper for callers building BacktrackRequest selectors.
func IntPtr(v int) *int {
	return &v
}

func intPtr(v int) *int { return IntPtr(v) }
