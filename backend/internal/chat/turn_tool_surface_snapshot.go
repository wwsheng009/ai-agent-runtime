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
	defer s.actor.mu.RUnlock()
	state := s.actor.state
	if state == nil {
		return nil, false, nil
	}
	if state.StableToolSurfaceSet {
		return cloneRuntimeToolDefinitions(state.StableToolSurface), true, nil
	}
	if strings.TrimSpace(state.CurrentTurnID) != s.turnID || !state.FrozenTurnToolsSet {
		return nil, false, nil
	}
	return cloneRuntimeToolDefinitions(state.FrozenTurnTools), true, nil
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
	return s.actor.updateState(ctx, func(state *RuntimeState) error {
		if strings.TrimSpace(state.CurrentTurnID) != s.turnID {
			return nil
		}
		ownedTools := cloneRuntimeToolDefinitions(tools)
		state.StableToolSurface = ownedTools
		state.StableToolSurfaceSet = true
		state.FrozenTurnTools = ownedTools
		state.FrozenTurnToolsSet = true
		state.UpdatedAt = time.Now().UTC()
		return nil
	})
}

func resetFrozenTurnTools(state *RuntimeState) {
	if state == nil {
		return
	}
	state.FrozenTurnTools = nil
	state.FrozenTurnToolsSet = false
}
