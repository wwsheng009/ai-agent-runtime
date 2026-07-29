package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/checkpoint"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ListTurns returns user-turn anchors for the session's visible history.
func (a *SessionActor) ListTurns(ctx context.Context) ([]UserTurn, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := a.loadSession(ctx)
	if err != nil {
		return nil, err
	}
	return ListUserTurns(session.GetMessages(), a.listSessionCheckpoints(ctx)), nil
}

// ListBacktrackAudit returns durable backtrack tombstones for the session (oldest first).
func (a *SessionActor) ListBacktrackAudit(ctx context.Context) ([]BacktrackTombstone, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := a.loadSession(ctx)
	if err != nil {
		return nil, err
	}
	return ListBacktrackTombstones(session), nil
}

// PreviewBacktrack plans a user-turn backtrack without mutating session state.
func (a *SessionActor) PreviewBacktrack(ctx context.Context, req BacktrackRequest) (*BacktrackResult, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is nil")
	}
	req.PreviewOnly = true
	req.AutoSubmit = false
	return a.backtrackCommand(ctx, req)
}

// Backtrack applies (or previews) a user-turn backtrack.
// When req.AutoSubmit is true and PreviewOnly is false, a new prompt is submitted
// after truncation using ComposerPrompt (edit_prompt or original anchor text).
// Auto-submit runs outside the actor command handler so approvals remain responsive.
func (a *SessionActor) Backtrack(ctx context.Context, req BacktrackRequest) (*BacktrackResult, *agent.Result, error) {
	if a == nil {
		return nil, nil, fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	autoSubmit := req.AutoSubmit && !req.PreviewOnly
	req.AutoSubmit = false // mutation path never starts a run itself
	if autoSubmit {
		req.IncludeAnchor = false
	}

	result, err := a.backtrackCommand(ctx, req)
	if err != nil {
		return result, nil, err
	}
	if result == nil {
		return nil, nil, fmt.Errorf("backtrack returned empty result")
	}
	if !autoSubmit {
		return result, nil, nil
	}

	prompt := strings.TrimSpace(result.ComposerPrompt)
	if prompt == "" {
		prompt = strings.TrimSpace(result.EditedPrompt)
	}
	if prompt == "" {
		result.Warnings = append(result.Warnings, "auto_submit skipped: empty composer prompt")
		return result, nil, nil
	}
	submitResult, submitErr := a.SubmitPrompt(ctx, prompt, nil)
	if submitErr != nil {
		result.Warnings = append(result.Warnings, "auto_submit failed: "+submitErr.Error())
		return result, submitResult, submitErr
	}
	result.AutoSubmitted = true
	return result, submitResult, nil
}

func (a *SessionActor) backtrackCommand(ctx context.Context, req BacktrackRequest) (*BacktrackResult, error) {
	a.Start()
	reply := make(chan BacktrackCommandResult, 1)
	cmd := BacktrackTo{Ctx: ctx, Request: req, Reply: reply}
	if err := a.send(ctx, cmd); err != nil {
		return nil, err
	}
	select {
	case res := <-reply:
		return res.Result, res.Err
	case <-a.done:
		return nil, ErrSessionActorStopped
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *SessionActor) handleBacktrackTo(cmd BacktrackTo) {
	if cmd.Reply == nil {
		return
	}
	ctx := cmd.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	if !cmd.Request.PreviewOnly {
		if err := a.ensureReady(); err != nil {
			cmd.Reply <- BacktrackCommandResult{Err: err}
			return
		}
	}

	session, err := a.loadSession(ctx)
	if err != nil {
		cmd.Reply <- BacktrackCommandResult{Err: err}
		return
	}
	messages := session.GetMessages()
	checkpoints := a.listSessionCheckpoints(ctx)

	plan, err := planBacktrackWithCheckpoints(messages, checkpoints, cmd.Request)
	if err != nil {
		cmd.Reply <- BacktrackCommandResult{Err: err}
		return
	}
	planned := plan.toResult(a.id, cmd.Request.PreviewOnly)

	if cmd.Request.PreviewOnly {
		cmd.Reply <- BacktrackCommandResult{Result: planned}
		return
	}

	_ = a.updateState(ctx, func(state *RuntimeState) error {
		state.Status = SessionRewinding
		state.UpdatedAt = time.Now().UTC()
		return nil
	})

	eventsEmitted := make([]string, 0, 4)
	startPayload := backtrackEventPayload(planned, nil)
	a.publish(runtimeevents.Event{
		Type:      EventBacktrackStarted,
		SessionID: a.id,
		Payload:   startPayload,
	})
	eventsEmitted = append(eventsEmitted, EventBacktrackStarted)
	// Compatibility signal for existing rewind consumers.
	a.publish(runtimeevents.Event{
		Type:      EventRewindStarted,
		SessionID: a.id,
		Payload:   startPayload,
	})
	eventsEmitted = append(eventsEmitted, EventRewindStarted)

	var applyErr error
	mode := planned.Mode

	// Conversation truncation first (conversation + both). Code-only leaves history intact.
	if mode == BacktrackModeConversation || mode == BacktrackModeBoth {
		tombstone, err := a.applyBacktrackHistory(ctx, session, plan, messages)
		if err != nil {
			applyErr = err
		} else if tombstone != nil {
			planned.Tombstone = cloneBacktrackTombstone(tombstone)
		}
	}

	// Code restore after conversation is durable.
	if applyErr == nil && (mode == BacktrackModeBoth || mode == BacktrackModeCode) {
		codeResult, codeErr, warnings := a.applyBacktrackCodeRestore(ctx, planned.BaseCheckpointID)
		planned.CodeRestore = codeResult
		if len(warnings) > 0 {
			planned.Warnings = append(planned.Warnings, warnings...)
		}
		if codeErr != nil {
			// Conversation remains truncated; surface partial failure.
			planned.Warnings = append(planned.Warnings, "code restore failed: "+codeErr.Error())
		}
	}

	status := SessionIdle
	if applyErr != nil {
		status = SessionStopped
	}
	_ = a.updateState(ctx, func(state *RuntimeState) error {
		clearBacktrackRuntimeState(state)
		state.Status = status
		if planned.BaseCheckpointID != "" && (mode == BacktrackModeBoth || mode == BacktrackModeCode) {
			state.CurrentCheckpointID = planned.BaseCheckpointID
		}
		state.UpdatedAt = time.Now().UTC()
		return nil
	})

	finishPayload := backtrackEventPayload(planned, applyErr)
	a.publish(runtimeevents.Event{
		Type:      EventBacktrackFinished,
		SessionID: a.id,
		Payload:   finishPayload,
	})
	eventsEmitted = append(eventsEmitted, EventBacktrackFinished)
	a.publish(runtimeevents.Event{
		Type:      EventRewindFinished,
		SessionID: a.id,
		Payload:   finishPayload,
	})
	eventsEmitted = append(eventsEmitted, EventRewindFinished)

	if a.agent != nil {
		if hookMgr := a.agent.GetHookManager(); hookMgr != nil {
			hookMgr.DispatchAsync(ctx, runtimehooks.EventBacktrackCompleted, finishPayload)
			hookMgr.DispatchAsync(ctx, runtimehooks.EventRewindCompleted, finishPayload)
		}
	}
	planned.EventsEmitted = eventsEmitted
	cmd.Reply <- BacktrackCommandResult{Result: planned, Err: applyErr}
}

func (a *SessionActor) applyBacktrackHistory(ctx context.Context, session *Session, plan *backtrackPlan, prior []runtimetypes.Message) (*BacktrackTombstone, error) {
	if session == nil {
		return nil, fmt.Errorf("session is nil")
	}
	if plan == nil {
		return nil, fmt.Errorf("backtrack plan is nil")
	}

	prefix := plan.Prefix
	priorCount := len(prior)
	var removed []runtimetypes.Message
	if plan.PrefixLen < priorCount {
		removed = prior[plan.PrefixLen:]
	}
	tombstone := buildBacktrackTombstone(a.id, plan, removed, priorCount)

	// Physical truncate: ReplaceHistory + clear head offset.
	session.ReplaceHistory(prefix)
	session.SetHeadOffset(0)
	// Drop stale observed context token counts after history rewrite.
	if session.Metadata.Context != nil {
		delete(session.Metadata.Context, aicliRuntimeContextTokenCountKey)
	}
	// Durable audit summary (no full bodies) survives the physical truncate.
	if tombstone != nil {
		_ = AppendBacktrackTombstone(session, tombstone)
	}
	if err := a.persistSession(ctx, session); err != nil {
		return nil, err
	}
	if err := a.updateState(ctx, func(state *RuntimeState) error {
		state.HeadOffset = 0
		state.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		return nil, err
	}
	return tombstone, nil
}

func (a *SessionActor) applyBacktrackCodeRestore(ctx context.Context, baseCheckpointID string) (*checkpoint.RestoreResult, error, []string) {
	baseCheckpointID = strings.TrimSpace(baseCheckpointID)
	if baseCheckpointID == "" {
		return nil, nil, []string{"no file checkpoints at or before anchor; code restore skipped"}
	}
	if a.agent == nil {
		return nil, fmt.Errorf("agent is not configured"), nil
	}
	checkpointMgr := a.agent.GetCheckpointRestoreManager()
	if checkpointMgr == nil {
		return nil, fmt.Errorf("checkpoint manager is not configured"), nil
	}
	result, err := checkpointMgr.Restore(ctx, checkpoint.RestoreRequest{
		SessionID:    a.id,
		CheckpointID: baseCheckpointID,
		Mode:         checkpoint.RestoreCode,
	})
	return result, err, nil
}

func (a *SessionActor) listSessionCheckpoints(ctx context.Context) []artifact.Checkpoint {
	if a == nil || a.agent == nil {
		return nil
	}
	mgr := a.agent.GetCheckpointRestoreManager()
	if mgr == nil || mgr.Store == nil {
		return nil
	}
	checkpoints, err := mgr.Store.ListCheckpoints(ctx, a.id, 0, 0)
	if err != nil {
		return nil
	}
	return checkpoints
}

func clearBacktrackRuntimeState(state *RuntimeState) {
	if state == nil {
		return
	}
	state.CurrentTurnID = ""
	state.CurrentCheckpointID = ""
	state.CurrentRunMeta = nil
	resetFrozenTurnTools(state)
	state.PendingTool = nil
	state.PendingApproval = nil
	state.PendingQuestion = nil
	state.HeadOffset = 0
}

func backtrackEventPayload(result *BacktrackResult, err error) map[string]interface{} {
	payload := map[string]interface{}{
		"reason": BacktrackReasonUserTurn,
	}
	if result != nil {
		payload["session_id"] = result.SessionID
		payload["mode"] = result.Mode
		payload["user_turn_index"] = result.UserTurnIndex
		payload["message_index"] = result.MessageIndex
		payload["truncated_to_message_count"] = result.TruncatedToMessageCount
		payload["removed_message_count"] = result.RemovedMessageCount
		payload["removed_user_turns"] = result.RemovedUserTurns
		payload["edited"] = strings.TrimSpace(result.EditedPrompt) != ""
		payload["include_anchor"] = result.IncludeAnchor
		payload["auto_submitted"] = result.AutoSubmitted
		if result.MessageID != "" {
			payload["message_id"] = result.MessageID
		}
		if result.BaseCheckpointID != "" {
			payload["base_checkpoint_id"] = result.BaseCheckpointID
		}
		if result.CodeRestore != nil {
			payload["code_restore"] = map[string]interface{}{
				"checkpoint_id": result.CodeRestore.CheckpointID,
				"applied_paths": result.CodeRestore.AppliedPaths,
				"errors":        result.CodeRestore.Errors,
			}
		}
		if result.Tombstone != nil {
			if tomb := result.Tombstone.toEventMap(); tomb != nil {
				payload["tombstone"] = tomb
				if id := strings.TrimSpace(result.Tombstone.ID); id != "" {
					payload["tombstone_id"] = id
				}
			}
		}
		if len(result.Warnings) > 0 {
			payload["warnings"] = append([]string(nil), result.Warnings...)
		}
	}
	if err != nil {
		payload["error"] = err.Error()
	} else {
		payload["error"] = ""
	}
	return payload
}
