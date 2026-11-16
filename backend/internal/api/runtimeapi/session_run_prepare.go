package runtimeapi

import (
	"context"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// prepareSessionAgentForRun applies the same turn-boundary state the CLI actor
// refreshes in localChatPrepareRunHook. The durable actor is reused across
// turns, so session metadata changes (/add-dir, plan mode, permission mode,
// frozen prompt) must be reflected without requiring an actor rebuild.
func (h *Handler) prepareSessionAgentForRun(ctx context.Context, apiAgent *agent.Agent, profileState *profileRuntimeState, baseSession bool, session *chat.Session) error {
	if apiAgent == nil || session == nil {
		return nil
	}
	sessionCtx := runtimehost.ExtractSessionContext(session)
	cfg := apiAgent.GetConfig()
	if cfg != nil {
		if cfg.Options == nil {
			cfg.Options = make(map[string]interface{})
		}
		// The frozen prompt is the provider cache prefix. Reapply it on every
		// turn; if this is the first run, anchor the composed prompt now.
		frozenPrompt := strings.TrimSpace(sessionCtx.FrozenSystemPrompt)
		if frozenPrompt != "" {
			cfg.SystemPrompt = frozenPrompt
		} else if prompt := strings.TrimSpace(cfg.SystemPrompt); prompt != "" {
			sessionmeta.Set(session.Metadata.Context, sessionmeta.SystemPromptFrozen, prompt)
			if h.sessionManager != nil {
				if err := h.sessionManager.Update(ctx, session); err != nil {
					logger.Warnf("session run prepare: persist frozen prompt %s: %v", session.ID, err)
				}
			}
		}

		if roots := sessionCtx.AllowedRoots; len(roots) > 0 {
			cfg.Options["allowed_roots"] = append([]string(nil), roots...)
		} else {
			delete(cfg.Options, "allowed_roots")
		}
		if roots := sessionCtx.ReadOnlyRoots; len(roots) > 0 {
			cfg.Options["read_only_roots"] = append([]string(nil), roots...)
		} else {
			delete(cfg.Options, "read_only_roots")
		}
	}

	if baseSession {
		h.applyRuntimeServerGoalGuidance(apiAgent, session)
	}
	applySessionPlanModeToAgent(apiAgent, session)
	if profileState == nil && !sessionCtx.DisableTools {
		if added := syncAPISessionToolPolicyAllowlist(apiAgent); len(added) > 0 {
			logger.Infof("API tool policy allowlist synced with live tool surface: +%d (%s)",
				len(added), strings.Join(added, ", "))
		}
	}
	return nil
}

// applySessionPlanModeToAgent reconciles the live engine on every run,
// including the transition out of plan mode.
func applySessionPlanModeToAgent(apiAgent *agent.Agent, session *chat.Session) {
	if apiAgent == nil || session == nil {
		return
	}
	engine := apiAgent.GetPermissionEngine()
	if engine == nil {
		engine = agent.NewPermissionEngine()
		apiAgent.SetPermissionEngine(engine)
	}
	runtimepolicy.EnsurePlanWriteAllowPaths(engine)

	state := planmode.Load(session)
	if planmode.IsActive(state) {
		planmode.ApplyToEngine(engine, state)
		return
	}
	modeText := planmode.EffectivePermissionMode(state)
	if state.Status == planmode.StatusExited {
		modeText = planmode.ResumeModeAfterExit(state)
	}
	if text := strings.TrimSpace(sessionPermissionMode(session)); text != "" {
		modeText = text
	}
	engine.Mode = parseSessionPlanPermissionMode(modeText)
}

// syncAPISessionToolPolicyAllowlist adds tools that became visible after the
// synthesized policy snapshot was built. Explicit profile allowlists are never
// widened; callers pass those through unchanged.
func syncAPISessionToolPolicyAllowlist(apiAgent *agent.Agent) []string {
	if apiAgent == nil {
		return nil
	}
	policy := apiAgent.GetToolExecutionPolicy()
	if policy == nil || !policy.AllowlistEnabled {
		return nil
	}
	candidates := make([]string, 0, 32)
	if surface := apiAgent.GetMCPManager(); surface != nil {
		for _, tool := range surface.ListTools() {
			if name := strings.TrimSpace(tool.Name); name != "" {
				candidates = append(candidates, name)
			}
		}
	}
	if broker := apiAgent.GetToolBroker(); broker != nil {
		for _, definition := range broker.Definitions() {
			if name := strings.TrimSpace(definition.Name); name != "" {
				candidates = append(candidates, name)
			}
		}
	}
	added := make([]string, 0)
	for _, name := range candidates {
		if policy.AllowedTools[name] || policy.DeniedTools[name] {
			continue
		}
		if policy.AllowedTools == nil {
			policy.AllowedTools = make(map[string]bool)
		}
		policy.AllowedTools[name] = true
		added = append(added, name)
	}
	if len(added) == 0 {
		return nil
	}
	sort.Strings(added)
	return added
}
