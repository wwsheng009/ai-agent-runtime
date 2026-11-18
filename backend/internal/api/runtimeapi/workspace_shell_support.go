package runtimeapi

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// SetWorkspaceShellResolver injects the runtime-server workspace shell resolver.
// A nil resolver keeps the process-wide compatibility behavior (CLI/tests).
func (h *Handler) SetWorkspaceShellResolver(resolver WorkspaceShellRuntimeResolver) {
	if h == nil {
		return
	}
	h.workspaceShell = resolver
}

// resolveSessionShellRuntime selects the shell runtime for one session.
//
// profileRuntimePath wins when the session profile explicitly declares a
// runtime.yaml; otherwise the workspace stack and the user/global fallback are
// resolved by the injected resolver. Errors are returned so the execution path
// can fail closed on strictProbe, while callers that only need display can use
// the returned runtime's fallback shell.
func (h *Handler) resolveSessionShellRuntime(workspacePath, profileRuntimePath string) (*runtimeexecutor.ShellRuntime, string, error) {
	if h == nil || h.workspaceShell == nil {
		runtime, err := runtimeexecutor.CurrentShellRuntime()
		return runtime, "", err
	}
	return h.workspaceShell.Resolve(strings.TrimSpace(workspacePath), strings.TrimSpace(profileRuntimePath))
}

// sessionShellResolution resolves the shell for an existing session/profile
// pair and falls back to the global resolver when no workspace resolver is
// wired.
func (h *Handler) sessionShellResolution(workspacePath string, profileState *profileRuntimeState) (*runtimeexecutor.ShellRuntime, string, error) {
	profileRuntimePath := ""
	if profileState != nil {
		profileRuntimePath = strings.TrimSpace(profileState.RuntimePath)
	}
	return h.resolveSessionShellRuntime(workspacePath, profileRuntimePath)
}

// invalidateWorkspaceShell drops the cached runtime for one workspace after a
// shell settings save or a workspace trust change.
func (h *Handler) invalidateWorkspaceShell(workspacePath string) {
	if h == nil || h.workspaceShell == nil {
		return
	}
	type invalidator interface {
		Invalidate(workspacePath string)
	}
	if cache, ok := h.workspaceShell.(invalidator); ok {
		cache.Invalidate(workspacePath)
	}
}

// markShellSessionsForRebuild flags actor sessions affected by a shell
// settings save so their next turn rebuilds the agent with the new runtime.
// An empty workspacePath targets unbound sessions; all=true targets every
// session (global fallback change).
// It is bounded and best-effort: the settings write already succeeded, and a
// session list failure must not turn it into an error.
func (h *Handler) markShellSessionsForRebuild(workspacePath string, all bool) {
	if h == nil || h.sessionManager == nil {
		return
	}
	workspacePath = strings.TrimSpace(workspacePath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sessions, err := h.sessionManager.ListAll(ctx, 0, 0)
	if err != nil {
		return
	}
	for _, session := range sessions {
		if session == nil {
			continue
		}
		sessionWorkspace := sessionWorkspaceDir(session)
		if !all && workspacePath == "" && sessionWorkspace != "" {
			continue
		}
		if !all && workspacePath != "" && !sameWorkspacePath(sessionWorkspace, workspacePath) {
			continue
		}
		h.markPendingActorRebuild(chat.NormalizeSessionID(session.ID), "runtime_refresh:shell_settings")
	}
}
