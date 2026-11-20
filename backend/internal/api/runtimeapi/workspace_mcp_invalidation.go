package runtimeapi

import (
	"context"
	"strings"
)

const workspaceMCPInvalidationActorLimit = 10000

// invalidateWorkspaceMCPRuntimeSurface invalidates the workspace manager/cache
// and the stable tool surfaces of already-running sessions bound to the same
// workspace. It deliberately inspects the existing hub instead of calling
// GetOrCreate, so a workspace settings request cannot create empty actors.
func (h *Handler) invalidateWorkspaceMCPRuntimeSurface(
	ctx context.Context,
	scope *sessionMCPAdminScope,
) {
	if h == nil || scope == nil || !scope.WorkspaceScoped {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	workspace := strings.TrimSpace(scope.Workspace)
	if workspace == "" {
		return
	}

	if h.workspaceMCP != nil {
		h.workspaceMCP.invalidateWorkspace(workspace, scope.ReadPath, scope.WritePath)
	}

	// Keep the existing durable cache invalidation semantics for MCP mutations.
	// The in-memory actor pass below narrows the active-actor work to this
	// workspace, while the durable store currently exposes only a process-wide
	// invalidator.
	h.invalidateSessionRuntimeToolSurfaces()

	hub := h.peekSessionHub()
	if hub == nil || h.sessionManager == nil {
		return
	}
	ids := hub.ActiveSessionIDs(workspaceMCPInvalidationActorLimit)
	for _, sessionID := range ids {
		actor, ok := hub.Get(sessionID)
		if !ok || actor == nil {
			continue
		}
		sessionCtx, cancel := context.WithTimeout(ctx, sessionStoreQueryTimeout)
		session, err := h.sessionManager.GetMetadata(sessionCtx, sessionID)
		cancel()
		if err != nil || session == nil {
			continue
		}
		if !sameAbsPath(sessionWorkspaceDir(session), workspace) {
			continue
		}
		invalidateCtx, invalidateCancel := context.WithTimeout(ctx, sessionStoreQueryTimeout)
		_ = actor.InvalidateStableToolSurface(invalidateCtx)
		invalidateCancel()
	}
}

// invalidateSessionOrWorkspaceMCPRuntimeSurface keeps session-only mutations
// narrow while routing workspace-persistent mutations through the shared
// workspace invalidation path.
func (h *Handler) invalidateSessionOrWorkspaceMCPRuntimeSurface(
	ctx context.Context,
	sessionID string,
	scope *sessionMCPAdminScope,
) {
	if scope != nil && scope.WorkspaceScoped {
		h.invalidateWorkspaceMCPRuntimeSurface(ctx, scope)
		return
	}
	h.invalidateSessionMCPRuntimeSurface(sessionID)
}
