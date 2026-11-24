package runtimeapi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/memoryassembly"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// canonicalMemoryWorkspacePath is shared by the runtime registry and the API
// workspace resolver. Symlinked paths resolve to one key, so two spellings can
// never open separate stores for the same workspace.
func canonicalMemoryWorkspacePath(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return ""
	}
	if absolute, err := filepath.Abs(workspace); err == nil {
		workspace = absolute
	}
	workspace = filepath.Clean(workspace)
	if real, err := filepath.EvalSymlinks(workspace); err == nil {
		workspace = filepath.Clean(real)
	}
	return workspace
}

func memoryWorkspaceKey(workspace string) string {
	key := canonicalMemoryWorkspacePath(workspace)
	if goruntime.GOOS == "windows" {
		return strings.ToLower(key)
	}
	return key
}

func (h *Handler) ensureMemoryRuntimeRegistryLocked() {
	if h.memoryRuntimes == nil {
		h.memoryRuntimes = make(map[string]*memoryruntime.Runtime)
	}
	if h.memoryRuntimeBootErrors == nil {
		h.memoryRuntimeBootErrors = make(map[string]string)
	}
}

func (h *Handler) registerMemoryRuntimeLocked(runtime *memoryruntime.Runtime, makeDefault bool) {
	if h == nil || runtime == nil {
		return
	}
	h.ensureMemoryRuntimeRegistryLocked()
	key := memoryWorkspaceKey(runtime.Config.Workspace)
	if key != "" {
		h.memoryRuntimes[key] = runtime
		delete(h.memoryRuntimeBootErrors, key)
	}
	if makeDefault || h.memoryRuntime == nil {
		h.memoryRuntime = runtime
		h.memoryRuntimeBootError = ""
	}
}

// MemoryRuntimeForWorkspace returns the runtime registered for one canonical
// workspace. It never falls back to a different workspace's runtime.
func (h *Handler) MemoryRuntimeForWorkspace(workspace string) *memoryruntime.Runtime {
	if h == nil {
		return nil
	}
	key := memoryWorkspaceKey(workspace)
	h.memoryRuntimeMu.RLock()
	defer h.memoryRuntimeMu.RUnlock()
	if key == "" {
		return h.memoryRuntime
	}
	if runtime := h.memoryRuntimes[key]; runtime != nil {
		return runtime
	}
	if h.memoryRuntime != nil && memoryWorkspaceKey(h.memoryRuntime.Config.Workspace) == key {
		return h.memoryRuntime
	}
	return nil
}

// memoryRuntimeSnapshot returns a stable copy of every registered runtime.
// Callers must not hold the registry lock while a runtime does work.
func (h *Handler) memoryRuntimeSnapshot() map[string]*memoryruntime.Runtime {
	if h == nil {
		return nil
	}
	h.memoryRuntimeMu.RLock()
	defer h.memoryRuntimeMu.RUnlock()
	snapshot := make(map[string]*memoryruntime.Runtime, len(h.memoryRuntimes)+1)
	for key, runtime := range h.memoryRuntimes {
		if runtime != nil {
			snapshot[key] = runtime
		}
	}
	if h.memoryRuntime != nil {
		key := memoryWorkspaceKey(h.memoryRuntime.Config.Workspace)
		if key != "" {
			snapshot[key] = h.memoryRuntime
		}
	}
	return snapshot
}

func (h *Handler) memoryRuntimeBootErrorForWorkspace(workspace string) string {
	if h == nil {
		return ""
	}
	key := memoryWorkspaceKey(workspace)
	h.memoryRuntimeMu.RLock()
	defer h.memoryRuntimeMu.RUnlock()
	if key != "" && h.memoryRuntimeBootErrors != nil {
		if message := h.memoryRuntimeBootErrors[key]; message != "" {
			return message
		}
	}
	if key == "" || (h.memoryRuntime != nil && memoryWorkspaceKey(h.memoryRuntime.Config.Workspace) == key) {
		return h.memoryRuntimeBootError
	}
	return ""
}

func (h *Handler) setMemoryRuntimeBootErrorForWorkspace(workspace string, err error) {
	if h == nil {
		return
	}
	message := ""
	if err != nil {
		message = sanitizeMemoryDiagnosticText(err.Error())
	}
	key := memoryWorkspaceKey(workspace)
	h.memoryRuntimeMu.Lock()
	h.ensureMemoryRuntimeRegistryLocked()
	if key != "" {
		if message == "" {
			delete(h.memoryRuntimeBootErrors, key)
		} else {
			h.memoryRuntimeBootErrors[key] = message
		}
	}
	if key == "" || (h.memoryRuntime == nil && key == memoryWorkspaceKey(h.configuredMemoryWorkspace())) {
		h.memoryRuntimeBootError = message
	}
	h.memoryRuntimeMu.Unlock()
}

func (h *Handler) configuredMemoryWorkspace() string {
	if h == nil || h.runtimeConfig == nil {
		return ""
	}
	return canonicalMemoryWorkspacePath(h.runtimeConfig.Workspace.Root)
}

func (h *Handler) memorySessionWorkspace(sessionID string) string {
	if h == nil || h.sessionManager == nil || strings.TrimSpace(sessionID) == "" {
		return ""
	}
	session, err := h.sessionManager.Get(context.Background(), strings.TrimSpace(sessionID))
	if err != nil || session == nil {
		return ""
	}
	return canonicalMemoryWorkspacePath(sessionmeta.String(session.Metadata.Context, sessionmeta.WorkspacePath))
}

// EnableMemoryRuntimeForWorkspace builds and registers one runtime per
// canonical workspace. Builds are serialized, but the registry lock is not held
// while the store/runtime is constructed.
func (h *Handler) EnableMemoryRuntimeForWorkspace(workspace string) (*memoryruntime.Runtime, error) {
	if h == nil || h.runtimeConfig == nil {
		return nil, nil
	}
	if strings.TrimSpace(workspace) == "" {
		workspace = h.configuredMemoryWorkspace()
		if workspace == "" {
			if cwd, err := os.Getwd(); err == nil {
				workspace = canonicalMemoryWorkspacePath(cwd)
			}
		}
	}
	workspace = canonicalMemoryWorkspacePath(workspace)
	if runtime := h.MemoryRuntimeForWorkspace(workspace); runtime != nil {
		return runtime, nil
	}

	h.memoryRuntimeBuildMu.Lock()
	defer h.memoryRuntimeBuildMu.Unlock()
	if runtime := h.MemoryRuntimeForWorkspace(workspace); runtime != nil {
		return runtime, nil
	}
	runtime, err := memoryassembly.BuildFromRuntimeConfig(memoryassembly.BuildOptions{
		RuntimeConfig:  h.runtimeConfig,
		Workspace:      workspace,
		SessionManager: h.sessionManager,
		LLMRuntime:     h.llmRuntime,
		WorkerID:       fmt.Sprintf("runtime-server-%d", os.Getpid()),
	})
	if err != nil {
		h.setMemoryRuntimeBootErrorForWorkspace(workspace, err)
		return nil, err
	}
	if runtime == nil {
		h.setMemoryRuntimeBootErrorForWorkspace(workspace, nil)
		return nil, nil
	}
	makeDefault := h.configuredMemoryWorkspace() == "" ||
		memoryWorkspaceKey(h.configuredMemoryWorkspace()) == memoryWorkspaceKey(workspace)
	h.memoryRuntimeMu.Lock()
	h.registerMemoryRuntimeLocked(runtime, makeDefault)
	h.memoryRuntimeMu.Unlock()
	return runtime, nil
}
