package chat

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type runtimeTurnToolSurfaceSnapshot struct {
	actor  *SessionActor
	turnID string
}

func (a *SessionActor) turnToolSurfaceSnapshot(turnID string) agent.TurnToolSurfaceSnapshot {
	turnID = strings.TrimSpace(turnID)
	if a == nil || turnID == "" {
		return nil
	}
	return &runtimeTurnToolSurfaceSnapshot{
		actor:  a,
		turnID: turnID,
	}
}

func (s *runtimeTurnToolSurfaceSnapshot) LoadTurnToolSurface(ctx context.Context) ([]types.ToolDefinition, bool, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	if s == nil || s.actor == nil || s.turnID == "" {
		return nil, false, nil
	}
	s.actor.mu.RLock()
	state := s.actor.state
	if state == nil {
		s.actor.mu.RUnlock()
		return nil, false, nil
	}

	// Turn-local freeze always wins. Mid-turn eligibility changes must not
	// rewrite tools once the active turn has already frozen a surface.
	if strings.TrimSpace(state.CurrentTurnID) == s.turnID && state.FrozenTurnToolsSet {
		tools := cloneRuntimeToolDefinitions(state.FrozenTurnTools)
		s.actor.mu.RUnlock()
		return tools, true, nil
	}

	if !state.StableToolSurfaceSet {
		s.actor.mu.RUnlock()
		return nil, false, nil
	}

	storedBinding := strings.TrimSpace(state.StableToolSurfaceBinding)
	tools := cloneRuntimeToolDefinitions(state.StableToolSurface)
	completionRequirement := ""
	if state.CurrentRunMeta != nil {
		completionRequirement = strings.TrimSpace(state.CurrentRunMeta.CompletionRequirement)
	}
	if completionRequirement == "" && state.AmbientRunMeta != nil {
		completionRequirement = strings.TrimSpace(state.AmbientRunMeta.CompletionRequirement)
	}
	s.actor.mu.RUnlock()

	// Legacy states without a binding keep reusing the stable surface until the
	// next freeze rewrites them with an eligibility key. One exception is an old
	// impossible completion surface: complete_task must not keep a cache which
	// contains neither of its terminal outcome tools after resume/upgrade.
	if storedBinding == "" {
		if legacyCompletionSurfaceMissingOutcome(completionRequirement, tools) {
			if err := s.actor.updateState(ctx, func(state *RuntimeState) error {
				if state == nil || !state.StableToolSurfaceSet || strings.TrimSpace(state.StableToolSurfaceBinding) != "" {
					return nil
				}
				if !legacyCompletionSurfaceMissingOutcome(completionRequirement, state.StableToolSurface) {
					return nil
				}
				clearStableToolSurface(state)
				state.UpdatedAt = time.Now().UTC()
				return nil
			}); err != nil {
				return nil, false, err
			}
			return nil, false, nil
		}
		return tools, true, nil
	}

	currentBinding := s.currentEligibilityKey(ctx)
	if currentBinding == "" || currentBinding == storedBinding {
		return tools, true, nil
	}

	// Policy / MCP / permission changed since the surface was frozen: drop the
	// durable cache so the next think step re-freezes under the new binding.
	if err := s.actor.updateState(ctx, func(state *RuntimeState) error {
		if state == nil || !state.StableToolSurfaceSet {
			return nil
		}
		if strings.TrimSpace(state.StableToolSurfaceBinding) != storedBinding {
			return nil
		}
		clearStableToolSurface(state)
		state.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		return nil, false, err
	}
	return nil, false, nil
}

func legacyCompletionSurfaceMissingOutcome(requirement string, tools []types.ToolDefinition) bool {
	if !agent.RequiresCompleteTask(requirement) {
		return false
	}
	for _, tool := range tools {
		switch strings.ToLower(strings.TrimSpace(tool.Name)) {
		case "report_task_outcome", "block_current_task":
			return false
		}
	}
	return true
}

func (s *runtimeTurnToolSurfaceSnapshot) SaveTurnToolSurface(ctx context.Context, tools []types.ToolDefinition) error {
	if s == nil || s.actor == nil || s.turnID == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	binding := s.currentEligibilityKey(ctx)
	fingerprint := agent.ToolDefinitionsFingerprint(tools)
	return s.actor.updateState(ctx, func(state *RuntimeState) error {
		if strings.TrimSpace(state.CurrentTurnID) != s.turnID {
			return nil
		}
		// Freeze-once for the active turn: later saves must not rewrite the
		// tools prefix mid-turn (prompt-cache + tool-call continuity).
		if state.FrozenTurnToolsSet {
			return nil
		}
		ownedTools := cloneRuntimeToolDefinitions(tools)
		state.StableToolSurface = ownedTools
		state.StableToolSurfaceSet = true
		state.StableToolSurfaceBinding = binding
		state.StableToolSurfaceFingerprint = fingerprint
		state.FrozenTurnTools = ownedTools
		state.FrozenTurnToolsSet = true
		state.UpdatedAt = time.Now().UTC()
		return nil
	})
}

func (s *runtimeTurnToolSurfaceSnapshot) currentEligibilityKey(ctx context.Context) string {
	if s == nil || s.actor == nil || s.actor.agent == nil {
		return ""
	}
	permissionMode := ""
	s.actor.mu.RLock()
	if s.actor.state != nil {
		if s.actor.state.CurrentRunMeta != nil {
			permissionMode = strings.TrimSpace(s.actor.state.CurrentRunMeta.PermissionMode)
		}
		if permissionMode == "" && s.actor.state.AmbientRunMeta != nil {
			permissionMode = strings.TrimSpace(s.actor.state.AmbientRunMeta.PermissionMode)
		}
	}
	s.actor.mu.RUnlock()
	return s.actor.agent.EligibilityKeyForAgent(ctx, permissionMode)
}

func resetFrozenTurnTools(state *RuntimeState) {
	if state == nil {
		return
	}
	state.FrozenTurnTools = nil
	state.FrozenTurnToolsSet = false
}

func clearStableToolSurface(state *RuntimeState) {
	if state == nil {
		return
	}
	state.StableToolSurface = nil
	state.StableToolSurfaceSet = false
	state.StableToolSurfaceBinding = ""
	state.StableToolSurfaceFingerprint = ""
}
