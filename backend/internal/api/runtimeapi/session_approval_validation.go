package runtimeapi

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

// approvalFeedbackMaxRunes caps the free-text rejection note forwarded to the
// model (§4.8). The note travels into tool errors and event payloads, so an
// unbounded string would bloat both.
const approvalFeedbackMaxRunes = 2000

// validateApproveToolDecision rejects malformed §4.8 approval options before
// they reach the actor. An unknown remember scope is a client bug (typo) and
// must not be silently downgraded to "session"; an empty scope means once.
func validateApproveToolDecision(decision chat.ApproveToolDecision) error {
	switch strings.ToLower(strings.TrimSpace(decision.RememberScope)) {
	case "", "once", "session", "project":
	default:
		return errors.New(errors.ErrValidationFailed, `remember_scope must be one of "once", "session", "project"`)
	}
	if len([]rune(strings.TrimSpace(decision.Feedback))) > approvalFeedbackMaxRunes {
		return errors.New(errors.ErrValidationFailed, "feedback is too long (max 2000 characters)")
	}
	return nil
}
