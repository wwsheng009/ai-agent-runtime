package agent

import (
	"context"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type turnToolSurfaceSnapshotKey struct{}

// TurnToolSurfaceSnapshot provides a stable tool surface for the lifetime of an active turn.
type TurnToolSurfaceSnapshot interface {
	LoadTurnToolSurface(ctx context.Context) ([]types.ToolDefinition, bool, error)
	// LoadTurnToolSurfaceNames returns only the advertised names, avoiding a
	// deep schema clone on every surface-mismatch / search-exemption check.
	LoadTurnToolSurfaceNames(ctx context.Context) ([]string, bool, error)
	SaveTurnToolSurface(ctx context.Context, tools []types.ToolDefinition) error
}

// sessionStableToolSurfaceSnapshot is an optional capability implemented by
// snapshots whose cached schema is reused across multiple user turns. A
// session-stable surface must not be selected from a goal-specific projection,
// otherwise the first short prompt can permanently remove tools needed later.
type sessionStableToolSurfaceSnapshot interface {
	StableAcrossTurns() bool
	CanRefreshStableToolSurface() bool
}

type inMemoryTurnToolSurfaceSnapshot struct {
	mu    sync.RWMutex
	set   bool
	tools []types.ToolDefinition
	names []string
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

// frozenTurnToolSurface returns the already-frozen turn tool surface when one
// exists. Compact callers use this to keep the same tools prefix as chat.
func frozenTurnToolSurface(ctx context.Context) []types.ToolDefinition {
	snapshot, ok := TurnToolSurfaceSnapshotFromContext(ctx)
	if !ok || snapshot == nil {
		return nil
	}
	tools, cached, err := snapshot.LoadTurnToolSurface(ctx)
	if err != nil || !cached || len(tools) == 0 {
		return nil
	}
	return cloneToolDefinitions(tools)
}

// toolAdvertisedInTurn reports whether name appeared in the frozen tool surface
// shipped to the model for this turn. It is the single "did the model see it?"
// predicate shared by the search_tool exemption and the surface-mismatch
// accounting; aliases and wildcard spellings are normalized by the registry.
//
// A missing/uncached snapshot yields false. Callers that must distinguish
// "absent" from "unknown" (the denial classifier) check the surface length
// themselves and keep the legacy fatal path when it is unknown.
func toolAdvertisedInTurn(ctx context.Context, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, advertised := range frozenTurnToolSurfaceNames(ctx) {
		if toolregistry.MatchPattern(advertised, name) || toolregistry.MatchPattern(name, advertised) {
			return true
		}
	}
	return false
}

// toolSurfaceKnown reports whether a frozen turn surface exists. A missing
// surface means "unknown", not "empty": denial classifiers must keep the
// legacy fatal path instead of treating every denial as a surface mismatch.
func toolSurfaceKnown(ctx context.Context) bool {
	return len(frozenTurnToolSurfaceNames(ctx)) > 0
}

// frozenTurnToolSurfaceNames returns the cached advertised names without
// cloning their schemas. The full definitions are only needed by callers that
// actually re-send the tool surface (compact); mismatch accounting and the
// search-tool exemption must stay cheap.
func frozenTurnToolSurfaceNames(ctx context.Context) []string {
	snapshot, ok := TurnToolSurfaceSnapshotFromContext(ctx)
	if !ok || snapshot == nil {
		return nil
	}
	names, cached, err := snapshot.LoadTurnToolSurfaceNames(ctx)
	if err != nil || !cached || len(names) == 0 {
		return nil
	}
	return names
}

func ensureTurnToolSurfaceSnapshot(ctx context.Context) context.Context {
	if snapshot, ok := TurnToolSurfaceSnapshotFromContext(ctx); ok && snapshot != nil {
		return ctx
	}
	return WithTurnToolSurfaceSnapshot(ctx, &inMemoryTurnToolSurfaceSnapshot{})
}

func sessionStableToolSurfaceState(snapshot TurnToolSurfaceSnapshot) (stable bool, refreshable bool) {
	policy, ok := snapshot.(sessionStableToolSurfaceSnapshot)
	if !ok || policy == nil || !policy.StableAcrossTurns() {
		return false, false
	}
	return true, policy.CanRefreshStableToolSurface()
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

func (s *inMemoryTurnToolSurfaceSnapshot) LoadTurnToolSurfaceNames(ctx context.Context) ([]string, bool, error) {
	if err := contextErr(ctx); err != nil {
		return nil, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.set {
		return nil, false, nil
	}
	return append([]string(nil), s.names...), true, nil
}

func (s *inMemoryTurnToolSurfaceSnapshot) SaveTurnToolSurface(ctx context.Context, tools []types.ToolDefinition) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Once frozen for a turn, do not rewrite the tool surface. Mid-turn mutations
	// would reorder/remove tool schemas and break prompt-cache prefixes.
	if s.set {
		return nil
	}
	s.tools = cloneToolDefinitions(tools)
	s.names = toolSurfaceDefinitionNames(tools)
	s.set = true
	return nil
}

func toolSurfaceDefinitionNames(tools []types.ToolDefinition) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if name := strings.TrimSpace(tool.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
