package chat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Local copies of sessionmeta permission keys to avoid import cycles
// (sessionmeta imports chat for some helpers).
const (
	planModePermissionModeKey          = "permission_mode"
	planModeRequestedPermissionModeKey = "requested_permission_mode"
	planModeEffectivePermissionModeKey = "effective_permission_mode"
	// planModeWorkspacePathKey mirrors sessionmeta.WorkspacePath without
	// importing sessionmeta (which imports chat).
	planModeWorkspacePathKey = "workspace_path"
)

// EnterPlanMode implements toolbroker.PlanModeController for mid-turn plan entry.
func (a *SessionActor) EnterPlanMode(ctx context.Context, sessionID string, args toolbroker.EnterPlanModeArgs) (*toolbroker.PlanModeResult, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Mid-turn tool calls run while SessionRunning; skip ensureReady busy check.
	if a.IsStopped() {
		return nil, ErrSessionActorStopped
	}
	if err := a.ensurePlanModeSessionID(sessionID); err != nil {
		return nil, err
	}

	session, err := a.loadSession(ctx)
	if err != nil {
		return nil, err
	}

	current := planmode.Load(session)
	previousMode := a.currentPermissionMode(session, ctx)
	// Nested enter while already active: keep original previous mode and refresh path.
	if planmode.IsActive(current) && strings.TrimSpace(current.PreviousMode) != "" {
		previousMode = current.PreviousMode
	}
	if strings.EqualFold(strings.TrimSpace(previousMode), string(runtimepolicy.ModePlan)) &&
		(!planmode.IsActive(current) || strings.TrimSpace(current.PreviousMode) == "") {
		previousMode = string(runtimepolicy.ModeDefault)
	}

	// plan_path may carry a list (first entry = primary artifact) and
	// plan_write_paths adds further allowlist entries; Enter unions the primary
	// path with the extras and dedupes them into State.WriteAllowPaths.
	state := planmode.EnterPlan(planmode.EnterOptions{
		PreviousMode:    previousMode,
		PlanPath:        args.PlanPath,
		WriteAllowPaths: args.PlanWritePaths,
		Workspace:       planModeWorkspacePath(session),
	})
	planmode.Save(session, state)
	a.applySessionPermissionMode(session, runtimepolicy.ModePlan)
	if err := a.persistSession(ctx, session); err != nil {
		return nil, err
	}

	if engine := a.agentPermissionEngine(); engine != nil {
		a.applyPlanModeStateToEngine(engine, state)
	}
	a.syncLivePermissionMode(ctx, string(runtimepolicy.ModePlan))
	a.publishPlanModeChanged(state, "enter")
	a.archivePlanModeArtifact(ctx, session, state, "enter", planmode.NormalizeExitSource(args.Source))

	return planModeResultFromState(state, string(runtimepolicy.ModePlan)), nil
}

// ExitPlanMode implements toolbroker.PlanModeController for mid-turn plan exit.
func (a *SessionActor) ExitPlanMode(ctx context.Context, sessionID string, args toolbroker.ExitPlanModeArgs) (*toolbroker.PlanModeResult, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Mid-turn tool calls run while SessionRunning; skip ensureReady busy check.
	if a.IsStopped() {
		return nil, ErrSessionActorStopped
	}
	if err := a.ensurePlanModeSessionID(sessionID); err != nil {
		return nil, err
	}

	session, err := a.loadSession(ctx)
	if err != nil {
		return nil, err
	}

	current := planmode.Load(session)
	currentMode := a.currentPermissionMode(session, ctx)
	if !planmode.IsActive(current) && current.Status != planmode.StatusExited {
		// Allow exit when only bare permission_mode=plan is set (no durable state).
		if !strings.EqualFold(strings.TrimSpace(currentMode), string(runtimepolicy.ModePlan)) {
			return nil, fmt.Errorf("not in plan mode; call enter_plan_mode first")
		}
		current = planmode.Enter(string(runtimepolicy.ModeDefault), planmode.DefaultPlanPath)
	}

	decision, err := planmode.NormalizeExitDecision(args.Decision)
	if err != nil {
		return nil, err
	}
	source := planmode.NormalizeExitSource(args.Source)

	// Model-authored verdicts are requests, not decisions: approve/quit must be
	// confirmed by the host (Web panel / `/plan approve` / TUI) unless the host
	// explicitly runs with model autonomy (headless exec/ACP).
	if source == planmode.ExitSourceModel && !planmode.ModelMayDecideExit() {
		switch decision {
		case planmode.ExitApprove, planmode.ExitQuit:
			pending := planmode.RequestExitFrom(current, source, args.Notes)
			planmode.Save(session, pending)
			a.applySessionPermissionMode(session, runtimepolicy.ModePlan)
			if err := a.persistSession(ctx, session); err != nil {
				return nil, err
			}
			if engine := a.agentPermissionEngine(); engine != nil {
				a.applyPlanModeStateToEngine(engine, pending)
			}
			a.syncLivePermissionMode(ctx, string(runtimepolicy.ModePlan))
			a.publishPlanModeChanged(pending, string(planmode.ExitSourceModel)+":request_exit")
			a.archivePlanModeArtifact(ctx, session, pending, "request_exit", source)
			return planModeResultFromState(pending, string(runtimepolicy.ModePlan)), nil
		}
	}

	exited, err := planmode.Exit(current, decision, args.Notes)
	if err != nil {
		return nil, err
	}
	exited.LastExitSource = source

	resume := planmode.ResumeModeAfterExit(exited)
	mode := parsePlanPermissionMode(resume)

	if exited.ExitDecision == planmode.ExitRequestChanges {
		// Stay active for another revision pass while recording the decision.
		exited.Status = planmode.StatusActive
		exited.PendingExitRequest = false
		if source == planmode.ExitSourceUser && strings.TrimSpace(args.Notes) != "" {
			exited = planmode.RecordReviewNotes(exited, args.Notes)
		}
		planmode.Save(session, exited)
		a.applySessionPermissionMode(session, runtimepolicy.ModePlan)
		if err := a.persistSession(ctx, session); err != nil {
			return nil, err
		}
		if engine := a.agentPermissionEngine(); engine != nil {
			a.applyPlanModeStateToEngine(engine, exited)
		}
		a.syncLivePermissionMode(ctx, string(runtimepolicy.ModePlan))
		a.publishPlanModeChanged(exited, "request_changes")
		a.archivePlanModeArtifact(ctx, session, exited, "request_changes", source)
		return planModeResultFromState(exited, string(runtimepolicy.ModePlan)), nil
	}

	planmode.Save(session, exited)
	a.applySessionPermissionMode(session, mode)
	if err := a.persistSession(ctx, session); err != nil {
		return nil, err
	}
	if engine := a.agentPermissionEngine(); engine != nil {
		// Leaving plan: restore engine mode; clear plan force if we set it.
		engine.Mode = mode
		if mode != runtimepolicy.ModePlan {
			// Keep default plan allow paths configured for future re-entry, but
			// do not force ModePlan.
			runtimepolicy.EnsurePlanWriteAllowPaths(engine)
		}
	}
	// Leaving plan mode retires the tool-policy write exemption (§4.7). This
	// path does not go through applyPlanModeStateToEngine, so sync explicitly.
	a.syncPlanWriteExemption(exited)
	a.syncLivePermissionMode(ctx, string(mode))
	a.publishPlanModeChanged(exited, "exit")
	a.archivePlanModeArtifact(ctx, session, exited, string(exited.ExitDecision), source)
	return planModeResultFromState(exited, string(mode)), nil
}

// archivePlanModeArtifact persists the plan artifact index + review-round
// snapshot outside the workspace (docs/analysis/commandcode-plan-mode-design-
// borrowing-20260925.md §4.2). Best-effort by design: a store failure is
// published as a diagnostic event and never rolls back the state transition.
func (a *SessionActor) archivePlanModeArtifact(ctx context.Context, session *Session, state planmode.State, decision string, source planmode.ExitSource) {
	if a == nil || session == nil {
		return
	}
	_, err := planmode.ArchivePlan(ctx, planmode.ArchiveOptions{
		Store:     a.planArtifactStore(),
		SessionID: a.id,
		Workspace: planModeWorkspacePath(session),
		PlanPath:  state.PlanPath,
		Decision:  strings.TrimSpace(decision),
		Source:    string(source),
		Notes:     state.Notes,
	})
	if err != nil {
		a.publish(runtimeevents.Event{
			Type:      EventPlanArchiveFailed,
			SessionID: a.id,
			Payload: map[string]interface{}{
				"plan_path": state.PlanPath,
				"decision":  strings.TrimSpace(decision),
				"error":     err.Error(),
			},
		})
	}
}

// planArtifactStore returns the actor's plan artifact store, falling back to the
// process-wide default ($HOME/.aicli/plans or AICLI_PLANS_DIR).
func (a *SessionActor) planArtifactStore() *planstore.Store {
	if a != nil && a.planStore != nil {
		return a.planStore
	}
	return planmode.DefaultPlanStore()
}

// planModeWorkspacePath returns the session workspace root used to resolve
// relative plan paths when archiving.
func planModeWorkspacePath(session *Session) string {
	if session == nil {
		return ""
	}
	if session.Metadata.Context != nil {
		if raw, ok := session.Metadata.Context[planModeWorkspacePathKey]; ok {
			if text := strings.TrimSpace(fmt.Sprint(raw)); text != "" && text != "<nil>" {
				return text
			}
		}
	}
	if raw, ok := session.GetContext(planModeWorkspacePathKey); ok {
		if text := strings.TrimSpace(fmt.Sprint(raw)); text != "" && text != "<nil>" {
			return text
		}
	}
	return ""
}

// publishPlanModeChanged notifies clients that durable plan-mode state moved so
// they reload the plan surface instead of polling.
func (a *SessionActor) publishPlanModeChanged(state planmode.State, action string) {
	if a == nil {
		return
	}
	payload := map[string]interface{}{
		"session_id":      a.id,
		"action":          strings.TrimSpace(action),
		"status":          string(state.Status),
		"active":          planmode.IsActive(state),
		"plan_path":       state.PlanPath,
		"permission_mode": planmode.EffectivePermissionMode(state),
	}
	if state.PendingExitRequest {
		payload["pending_exit_request"] = true
	}
	if state.LastExitSource != "" {
		payload["exit_source"] = string(state.LastExitSource)
	}
	if state.ExitDecision != "" {
		payload["exit_decision"] = string(state.ExitDecision)
	}
	if state.ReviewRound > 0 {
		payload["review_round"] = state.ReviewRound
	}
	a.publish(runtimeevents.Event{
		Type:      EventPlanModeChanged,
		SessionID: a.id,
		Payload:   payload,
	})
}

// maybeAnnouncePlanReview is the run-end backstop (report §4.5): the model may
// stop without calling exit_plan_mode, which used to leave plan mode active
// with nothing telling the user that a reviewable revision exists.
//
// It fires only when ALL of the following hold:
//   - the run ended cleanly (caller guarantees: idle status, no exec error);
//   - durable plan mode is active and the model has not already asked for a
//     verdict (a pending exit request is its own signal);
//   - the session mode is default/plan — accept_edits and bypass skip review by
//     design (the same skip rule the plan-mode prompt follows);
//   - the workspace plan file holds non-empty content.
//
// Announcements are deduplicated per revision: the same plan body is announced
// once, and a rewritten body (new revision) announces again.
func (a *SessionActor) maybeAnnouncePlanReview(ctx context.Context, session *Session) {
	if a == nil || session == nil {
		return
	}
	state := planmode.Load(session)
	if !planmode.IsActive(state) || planmode.ExitRequested(state) {
		return
	}
	switch strings.TrimSpace(a.currentPermissionMode(session, ctx)) {
	case string(runtimepolicy.ModeDefault), string(runtimepolicy.ModePlan):
	default:
		return
	}
	content := planmode.ReadPlanArtifact(planModeWorkspacePath(session), state.PlanPath)
	if len(bytes.TrimSpace(content)) == 0 {
		return
	}
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:8])
	if previous, ok := a.planReviewHash.Load().(string); ok && previous == hash {
		return
	}
	a.planReviewHash.Store(hash)
	payload := map[string]interface{}{
		"session_id":         a.id,
		"plan_path":          state.PlanPath,
		"plan_hash":          hash,
		"plan_bytes":         len(content),
		"permission_mode":    planmode.EffectivePermissionMode(state),
		"request_changes":    "/plan request_changes <notes>",
		"approve":            "/plan approve",
		"quit":               "/plan quit",
		"review_entry_point": "/plan review",
	}
	if state.ReviewRound > 0 {
		payload["review_round"] = state.ReviewRound
	}
	a.publish(runtimeevents.Event{
		Type:      EventPlanReviewAvailable,
		SessionID: a.id,
		Payload:   payload,
	})
}

// consumePlanReviewNotes delivers user plan-review feedback (request_changes
// notes) into the current turn's request-only system channel and clears the
// durable copy so the feedback is never injected twice.
//
// Persisting the clear is part of the contract: when the durable write fails,
// the in-memory state is rolled back and the feedback stays pending for the
// next turn instead of being silently dropped.
func (a *SessionActor) consumePlanReviewNotes(ctx context.Context, session *Session) *runtimetypes.Message {
	if a == nil || session == nil {
		return nil
	}
	state := planmode.Load(session)
	if !planmode.IsActive(state) || strings.TrimSpace(state.PendingReviewNotes) == "" {
		return nil
	}
	previous := state
	cleared, notes := planmode.ConsumeReviewNotes(state)
	if strings.TrimSpace(notes) == "" {
		return nil
	}
	planmode.Save(session, cleared)
	if err := a.persistSession(ctx, session); err != nil {
		planmode.Save(session, previous)
		return nil
	}
	a.publishPlanModeChanged(cleared, "review_notes_delivered")
	return agent.NewSystemReminderMessage(agent.SystemReminder{
		Kind:    agent.ReminderKindPlanReview,
		Body:    agent.PlanReviewNotesBody(notes),
		Durable: false,
		Extra: runtimetypes.Metadata{
			"plan_mode":       true,
			"permission_mode": string(runtimepolicy.ModePlan),
			"review_round":    cleared.ReviewRound,
		},
	})
}

func (a *SessionActor) ensurePlanModeSessionID(sessionID string) error {
	if a == nil {
		return fmt.Errorf("session actor is not configured")
	}
	requested := strings.TrimSpace(sessionID)
	if requested == "" || requested == a.id {
		return nil
	}
	return fmt.Errorf("plan mode tools only operate on the current session (%s), got %s", a.id, requested)
}

func (a *SessionActor) agentPermissionEngine() *runtimepolicy.Engine {
	if a == nil || a.agent == nil {
		return nil
	}
	return a.agent.GetPermissionEngine()
}

func (a *SessionActor) currentPermissionMode(session *Session, ctx context.Context) string {
	if session != nil {
		if raw, ok := session.GetContext(planModePermissionModeKey); ok {
			if text := strings.TrimSpace(fmt.Sprint(raw)); text != "" && text != "<nil>" {
				return text
			}
		}
		if raw, ok := session.GetContext(planModeEffectivePermissionModeKey); ok {
			if text := strings.TrimSpace(fmt.Sprint(raw)); text != "" && text != "<nil>" {
				return text
			}
		}
	}
	if meta, ok := team.GetRunMeta(ctx); ok && meta != nil {
		if text := strings.TrimSpace(meta.PermissionMode); text != "" {
			return text
		}
	}
	if a != nil {
		if state := a.State(); state != nil && state.CurrentRunMeta != nil {
			if text := strings.TrimSpace(state.CurrentRunMeta.PermissionMode); text != "" {
				return text
			}
		}
	}
	if engine := a.agentPermissionEngine(); engine != nil {
		if text := strings.TrimSpace(string(engine.Mode)); text != "" {
			return text
		}
	}
	return string(runtimepolicy.ModeDefault)
}

func (a *SessionActor) applySessionPermissionMode(session *Session, mode runtimepolicy.Mode) {
	if session == nil {
		return
	}
	text := string(mode)
	session.SetContext(planModePermissionModeKey, text)
	session.SetContext(planModeRequestedPermissionModeKey, text)
	session.SetContext(planModeEffectivePermissionModeKey, text)
}

func (a *SessionActor) syncLivePermissionMode(ctx context.Context, mode string) {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return
	}
	// Mutate the in-context RunMeta pointer so subsequent tool evaluations in
	// the same turn see the updated permission mode via permissionModeFromContext.
	if meta, ok := team.GetRunMeta(ctx); ok && meta != nil {
		meta.PermissionMode = mode
	}
	if a == nil {
		return
	}
	_ = a.updateState(ctx, func(state *RuntimeState) error {
		if state == nil {
			return nil
		}
		if state.CurrentRunMeta == nil {
			state.CurrentRunMeta = &team.RunMeta{PermissionMode: mode}
			return nil
		}
		state.CurrentRunMeta.PermissionMode = mode
		return nil
	})
}

func planModeResultFromState(state planmode.State, permissionMode string) *toolbroker.PlanModeResult {
	return &toolbroker.PlanModeResult{
		Active:             planmode.IsActive(state),
		Status:             string(state.Status),
		PlanPath:           state.PlanPath,
		PermissionMode:     strings.TrimSpace(permissionMode),
		PreviousMode:       state.PreviousMode,
		ExitDecision:       string(state.ExitDecision),
		Notes:              state.Notes,
		WriteAllowPaths:    append([]string(nil), state.WriteAllowPaths...),
		EnteredAt:          state.EnteredAt,
		ExitedAt:           state.ExitedAt,
		PendingExitRequest: state.PendingExitRequest,
		ExitSource:         string(state.LastExitSource),
		ReviewRound:        state.ReviewRound,
		PendingReviewNotes: state.PendingReviewNotes,
		ReopenedFrom:       state.ReopenedFrom,
		ReopenedVersion:    state.ReopenedVersion,
	}
}

func parsePlanPermissionMode(raw string) runtimepolicy.Mode {
	switch runtimepolicy.Mode(strings.ToLower(strings.TrimSpace(raw))) {
	case runtimepolicy.ModeAcceptEdits:
		return runtimepolicy.ModeAcceptEdits
	case runtimepolicy.ModePlan:
		return runtimepolicy.ModePlan
	case runtimepolicy.ModeBypassPermissions:
		return runtimepolicy.ModeBypassPermissions
	default:
		return runtimepolicy.ModeDefault
	}
}
