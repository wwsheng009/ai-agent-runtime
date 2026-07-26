package chat

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// Local copies of sessionmeta permission keys to avoid import cycles
// (sessionmeta imports chat for some helpers).
const (
	planModePermissionModeKey          = "permission_mode"
	planModeRequestedPermissionModeKey = "requested_permission_mode"
	planModeEffectivePermissionModeKey = "effective_permission_mode"
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

	state := planmode.Enter(previousMode, args.PlanPath)
	planmode.Save(session, state)
	a.applySessionPermissionMode(session, runtimepolicy.ModePlan)
	if err := a.persistSession(ctx, session); err != nil {
		return nil, err
	}

	if engine := a.agentPermissionEngine(); engine != nil {
		a.applyPlanModeStateToEngine(engine, state)
	}
	a.syncLivePermissionMode(ctx, string(runtimepolicy.ModePlan))

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

	exited, err := planmode.Exit(current, planmode.ExitDecision(args.Decision), args.Notes)
	if err != nil {
		return nil, err
	}

	resume := planmode.ResumeModeAfterExit(exited)
	mode := parsePlanPermissionMode(resume)

	if exited.ExitDecision == planmode.ExitRequestChanges {
		// Stay active for another revision pass while recording the decision.
		exited.Status = planmode.StatusActive
		exited.PendingExitRequest = false
		planmode.Save(session, exited)
		a.applySessionPermissionMode(session, runtimepolicy.ModePlan)
		if err := a.persistSession(ctx, session); err != nil {
			return nil, err
		}
		if engine := a.agentPermissionEngine(); engine != nil {
			a.applyPlanModeStateToEngine(engine, exited)
		}
		a.syncLivePermissionMode(ctx, string(runtimepolicy.ModePlan))
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
	a.syncLivePermissionMode(ctx, string(mode))
	return planModeResultFromState(exited, string(mode)), nil
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
		Active:          planmode.IsActive(state),
		Status:          string(state.Status),
		PlanPath:        state.PlanPath,
		PermissionMode:  strings.TrimSpace(permissionMode),
		PreviousMode:    state.PreviousMode,
		ExitDecision:    string(state.ExitDecision),
		Notes:           state.Notes,
		WriteAllowPaths: append([]string(nil), state.WriteAllowPaths...),
		EnteredAt:       state.EnteredAt,
		ExitedAt:        state.ExitedAt,
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
