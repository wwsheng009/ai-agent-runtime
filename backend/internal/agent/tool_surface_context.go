package agent

import (
	"context"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type turnToolSurfaceSnapshotKey struct{}

// TurnToolSurfaceSnapshot provides a stable tool surface for the lifetime of an active turn.
type TurnToolSurfaceSnapshot interface {
	LoadTurnToolSurface(ctx context.Context) ([]types.ToolDefinition, bool, error)
	SaveTurnToolSurface(ctx context.Context, tools []types.ToolDefinition) error
}

type inMemoryTurnToolSurfaceSnapshot struct {
	mu    sync.RWMutex
	set   bool
	tools []types.ToolDefinition
}

// WithTurnToolSurfaceSnapshot annotates ctx with a turn-scoped tool surface snapshot.
func WithTurnToolSurfaceSnapshot(ctx context.Context, snapshot TurnToolSurfaceSnapshot) context.Context {
	if snapshot == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, turnToolSurfaceSnapshotKey{}, snapshot)
}

// TurnToolSurfaceSnapshotFromContext returns the turn-scoped tool surface snapshot, if any.
func TurnToolSurfaceSnapshotFromContext(ctx context.Context) (TurnToolSurfaceSnapshot, bool) {
	if ctx == nil {
		return nil, false
	}
	snapshot, ok := ctx.Value(turnToolSurfaceSnapshotKey{}).(TurnToolSurfaceSnapshot)
	if !ok || snapshot == nil {
		return nil, false
	}
	return snapshot, true
}

func ensureTurnToolSurfaceSnapshot(ctx context.Context) context.Context {
	if snapshot, ok := TurnToolSurfaceSnapshotFromContext(ctx); ok && snapshot != nil {
		return ctx
	}
	return WithTurnToolSurfaceSnapshot(ctx, &inMemoryTurnToolSurfaceSnapshot{})
}

func (s *inMemoryTurnToolSurfaceSnapshot) LoadTurnToolSurface(ctx context.Context) ([]types.ToolDefinition, bool, error) {
	if err := contextErr(ctx); err != nil {
		return nil, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.set {
		return nil, false, nil
	}
	return cloneToolDefinitions(s.tools), true, nil
}

func (s *inMemoryTurnToolSurfaceSnapshot) SaveTurnToolSurface(ctx context.Context, tools []types.ToolDefinition) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools = cloneToolDefinitions(tools)
	s.set = true
	return nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
