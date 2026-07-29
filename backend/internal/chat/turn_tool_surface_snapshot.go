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

func (s *runtimeTurnToolSurfaceSnapshot) StableAcrossTurns() bool {
	return true
}

func (s *runtimeTurnToolSurfaceSnapshot) CanRefreshStableToolSurface() bool {
	if s == nil || s.actor == nil || s.turnID == "" {
		return false
	}
	s.actor.mu.RLock()
	defer s.actor.mu.RUnlock()
	state := s.actor.state
	return state != nil &&
		strings.TrimSpace(state.CurrentTurnID) == s.turnID &&
		!state.FrozenTurnToolsSet
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

	tools := cloneRuntimeToolDefinitions(state.StableToolSurface)
	s.actor.mu.RUnlock()

	// Session-stable tool schemas are immutable for a prompt-cache lane.
	// Permission / policy / MCP changes are enforced at execution time; compact
	// keeps the same surface and disables calls via tool_choice rather than
	// starting a tool-schema epoch. The agent loop may replace a legacy
	// goal-projected surface once at a new-turn boundary; other capability changes
	// still require a new session or an explicitly-created cache generation.
	return tools, true, nil
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
