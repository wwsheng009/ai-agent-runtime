package chat

import (
	"context"
	"strings"
	"time"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// RetireDurablePendingInteraction closes a persisted approval/question when no
// live actor owns the session (for example after a process restart). It returns
// the terminal events the host must publish so the durable notification and the
// runtime event stream converge with the cleared state.
//
// The function deliberately does not fabricate an allow decision: approvals are
// retired as run_terminated and questions as run_terminated with an empty
// answer, matching the actor's close/interrupt escape hatch.
func RetireDurablePendingInteraction(ctx context.Context, store RuntimeStateStore, sessionID, reason string) ([]runtimeevents.Event, bool, error) {
	if store == nil || strings.TrimSpace(sessionID) == "" {
		return nil, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state, err := store.LoadState(ctx, sessionID)
	if err != nil || state == nil || !state.HasPendingInteraction() {
		return nil, false, err
	}
	if strings.TrimSpace(state.SessionID) == "" {
		state.SessionID = strings.TrimSpace(sessionID)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = ApprovalResolutionRunTerminated
	}
	events := durablePendingRetirementEvents(state, reason, ApprovalResolutionRunTerminated, reason)
	stopRecoveredRuntimeState(state)
	if err := store.SaveState(ctx, state); err != nil {
		return nil, false, err
	}
	return events, true, nil
}

// ExpireDurableApproval retires an approval whose window has elapsed without a
// live waiter. Approvals without an expiry timestamp are left untouched (there
// is no evidence they can no longer be decided).
func ExpireDurableApproval(ctx context.Context, store RuntimeStateStore, sessionID string, now time.Time) ([]runtimeevents.Event, bool, error) {
	if store == nil || strings.TrimSpace(sessionID) == "" {
		return nil, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	state, err := store.LoadState(ctx, sessionID)
	if err != nil || state == nil || state.PendingApproval == nil {
		return nil, false, err
	}
	if strings.TrimSpace(state.SessionID) == "" {
		state.SessionID = strings.TrimSpace(sessionID)
	}
	approval := state.PendingApproval
	if approval.ExpiresAt.IsZero() || now.Before(approval.ExpiresAt) {
		return nil, false, nil
	}
	expiryErr := approvalExpiredError(approval)
	payload := approvalResolvedEventPayload(state, approval.ID, false)
	payload["resolution"] = ApprovalResolutionExpired
	payload["resumed"] = false
	payload["error_code"] = string(runtimeerrors.ErrApprovalExpired)
	if expiryErr != nil {
		payload["error_message"] = expiryErr.Message
	}
	event := runtimeevents.Event{
		Type:      EventApprovalResolved,
		SessionID: strings.TrimSpace(sessionID),
		Payload:   payload,
		Timestamp: now.UTC(),
	}
	stopRecoveredRuntimeState(state)
	if err := store.SaveState(ctx, state); err != nil {
		return nil, false, err
	}
	return []runtimeevents.Event{event}, true, nil
}

func durablePendingRetirementEvents(state *RuntimeState, reason, approvalResolution, questionResolution string) []runtimeevents.Event {
	if state == nil {
		return nil
	}
	now := time.Now().UTC()
	events := make([]runtimeevents.Event, 0, 2)
	if state.PendingApproval != nil {
		requestID := strings.TrimSpace(state.PendingApproval.ID)
		if requestID != "" {
			payload := approvalResolvedEventPayload(state, requestID, false)
			payload["resolution"] = approvalResolution
			payload["resumed"] = false
			if reason != "" {
				payload["run_terminal_reason"] = reason
			}
			events = append(events, runtimeevents.Event{
				Type:      EventApprovalResolved,
				SessionID: state.SessionID,
				Payload:   payload,
				Timestamp: now,
			})
		}
	}
	if state.PendingQuestion != nil {
		questionID := strings.TrimSpace(state.PendingQuestion.ID)
		if questionID != "" {
			events = append(events, runtimeevents.Event{
				Type:      EventQuestionAnswered,
				SessionID: state.SessionID,
				Payload: map[string]interface{}{
					"question_id": questionID,
					"answer":      "",
					"resolution":  questionResolution,
				},
				Timestamp: now,
			})
		}
	}
	return events
}
