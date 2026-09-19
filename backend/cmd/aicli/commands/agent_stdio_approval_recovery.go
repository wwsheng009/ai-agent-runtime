package commands

import (
	"context"
	"fmt"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// acpRestoredApprovalWaitTimeout bounds how long a prompt waits for the run a
// restored approval resumes. That run is a real continuation of the interrupted
// turn, so it may stream for minutes; the cap only exists so a wedged run cannot
// block the prompt forever.
const acpRestoredApprovalWaitTimeout = 5 * time.Minute

// acpRestoredApprovalDecision is how a persisted approval should be resolved.
type acpRestoredApprovalDecision int

const (
	// acpRestoredApprovalAsk re-emits session/request_permission and applies the
	// client's answer.
	acpRestoredApprovalAsk acpRestoredApprovalDecision = iota
	// acpRestoredApprovalAllow resolves the approval without asking, because the
	// session's permission mode auto-approves every tool call (yolo).
	acpRestoredApprovalAllow
	// acpRestoredApprovalDeny resolves the approval as denied. The approval window
	// is over, so waiting for an answer that can no longer arrive would leave the
	// actor in waiting_approval forever.
	acpRestoredApprovalDeny
)

// decideACPRestoredApproval picks the resolution for an approval persisted by a
// previous process. A nil approval means there is nothing to decide. Bypass
// permissions wins over expiry: in yolo the approval should never have been
// requested, so honoring the mode is the whole point.
func decideACPRestoredApproval(mode runtimepolicy.Mode, approval *runtimechat.ApprovalRequest, now time.Time) acpRestoredApprovalDecision {
	if approval == nil {
		return acpRestoredApprovalAsk
	}
	if mode == runtimepolicy.ModeBypassPermissions {
		return acpRestoredApprovalAllow
	}
	if !approval.ExpiresAt.IsZero() && !now.Before(approval.ExpiresAt) {
		return acpRestoredApprovalDeny
	}
	return acpRestoredApprovalAsk
}

// reconcileACPRestoredApproval resolves an approval that a previous process
// persisted but never delivered to a client (session/load after a crash or a
// process replacement). The restored request has no waiter in this process, so
// nothing ever emits session/request_permission for it: the actor stays in
// waiting_approval, every prompt fails with the actor-ready timeout, and a yolo
// switch cannot help because the decision is read back from the persisted run
// meta. This closes that gap by re-asking the client, or by resolving the
// approval locally when the current mode no longer requires a question.
//
// It reports whether the resolution started a recovered run, i.e. whether the
// actor is busy again and the caller must let it settle before submitting the
// next prompt.
func reconcileACPRestoredApproval(ctx context.Context, hostSess *acpHostSession, chat *ChatSession) bool {
	if hostSess == nil || chat == nil {
		return false
	}
	actor, err := chatActorForSession(ctx, chat)
	if err != nil || actor == nil {
		if err != nil {
			writeSessionDebugInfo(chat, "[acp-approval] actor unavailable: "+err.Error(), false)
		}
		return false
	}
	pending := actor.PendingApproval()
	if pending == nil {
		return false
	}

	mode := chatSessionPermissionMode(chat)
	// The run that raised this approval froze its permission mode in the run
	// meta, so a mode switch made afterwards never reaches it. Align the live
	// engine with the client's current choice before resolving, otherwise yolo
	// would still prompt for every tool call the resumed run makes.
	if chat.RuntimeSession != nil && mode != "" && mode != runtimepolicy.ModePlan {
		if err := actor.SetPermissionMode(ctx, chat.RuntimeSession.ID, mode); err != nil {
			writeSessionDebugInfo(chat, "[acp-approval] apply permission mode failed: "+err.Error(), false)
		}
	}

	decision := decideACPRestoredApproval(mode, pending, time.Now())
	allow := decision == acpRestoredApprovalAllow
	if decision == acpRestoredApprovalAsk {
		if hostSess.bridge == nil {
			// Without a bridge there is no way to reach the client; leaving the
			// approval untouched keeps the previous behavior instead of guessing.
			return false
		}
		answer, err := hostSess.bridge.AskApproval(pending, nil)
		if err != nil {
			// The client never answered (prompt cancelled, transport error). Deny
			// so the actor is not left waiting on a request that no longer exists.
			writeSessionDebugInfo(chat, "[acp-approval] restored approval request failed: "+err.Error(), false)
			allow = false
		} else {
			allow = answer.Allowed
		}
	}

	if err := actor.ApproveToolWithArgs(ctx, pending.ID, allow, nil); err != nil {
		writeSessionDebugInfo(chat, "[acp-approval] resolve restored approval failed: "+err.Error(), false)
		return false
	}
	writeSessionDebugInfo(chat, fmt.Sprintf(
		"[acp-approval] resolved restored approval %s tool=%s allow=%v", pending.ID, pending.ToolName, allow,
	), false)
	return true
}

// waitForACPRecoveredRun lets the run started by reconcileACPRestoredApproval
// finish before the caller submits the user's next prompt. waitForAICLIActorReady
// keeps its short diagnostic timeout for every other busy case (a control-plane
// owner, a stale lease), so this wait is deliberately scoped to the recovery
// this process just started.
func waitForACPRecoveredRun(ctx context.Context, chat *ChatSession) {
	actor, err := chatActorForSession(ctx, chat)
	if err != nil || actor == nil {
		return
	}
	deadline := time.Now().Add(acpRestoredApprovalWaitTimeout)
	ticker := time.NewTicker(aicliActorReadyPollInterval)
	defer ticker.Stop()
	for {
		if state, ok := actor.StateSummary(); !ok || !state.Busy() {
			return
		}
		if !time.Now().Before(deadline) {
			writeSessionDebugInfo(chat, "[acp-approval] recovered run still busy after "+acpRestoredApprovalWaitTimeout.String(), false)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
