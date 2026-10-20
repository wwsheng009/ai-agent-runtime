package runtimeapi

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/plugins"
)

type harnessHookDTO struct {
	ID             string `json:"id"`
	Event          string `json:"event"`
	HandlerType    string `json:"handler_type"`
	ExecutionMode  string `json:"execution_mode"`
	Matcher        string `json:"matcher,omitempty"`
	Command        string `json:"command,omitempty"`
	URL            string `json:"url,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	StatusMessage  string `json:"status_message,omitempty"`
	Source         string `json:"source,omitempty"`
	SourcePath     string `json:"source_path,omitempty"`
	Enabled        bool   `json:"enabled"`
}

type harnessHooksResponse struct {
	WorkspacePath string                   `json:"workspace_path"`
	Hooks         []harnessHookDTO         `json:"hooks"`
	Sources       []string                 `json:"sources,omitempty"`
	Warnings      []string                 `json:"warnings,omitempty"`
	RecentRuns    []runtimehooks.RunRecord `json:"recent_runs,omitempty"`
	Count         int                      `json:"count"`
}

type harnessHookUpdateRequest struct {
	ID         string `json:"id"`
	SourcePath string `json:"source_path"`
	Enabled    bool   `json:"enabled"`
}

// GetHarnessHooks lists the effective hook handlers for a workspace. It is a
// read-only diagnostic surface (no handler execution).
func (h *Handler) GetHarnessHooks(w http.ResponseWriter, r *http.Request) {
	workspace := strings.TrimSpace(r.URL.Query().Get("workspace_path"))
	if workspace == "" && h.runtimeConfig != nil {
		workspace = strings.TrimSpace(h.runtimeConfig.Workspace.Root)
	}
	trust := workspaceFolderTrust(workspace)
	sources := h.hostHookSources(workspace, trust.Trusted, nil)
	var all []runtimehooks.HookConfig
	if h.runtimeConfig != nil {
		all = append(all, h.runtimeConfig.Hooks...)
	}
	sources.Base = all
	all, discovered := runtimehooks.ResolveForHostFromEnv(sources)

	out := make([]harnessHookDTO, 0, len(all))
	for _, hook := range all {
		executionMode := "sync"
		if hook.Async {
			executionMode = "async"
		}
		handlerType := strings.ToLower(strings.TrimSpace(hook.Exec.Type))
		if handlerType == "" {
			handlerType = "unknown"
		}
		out = append(out, harnessHookDTO{
			ID:            hook.ID,
			Event:         string(hook.Event),
			HandlerType:   handlerType,
			ExecutionMode: executionMode,
			Matcher:       hook.Matcher,
			Command:       hook.Exec.Command,
			URL:           hook.Exec.URL,
			// Report the effective default so the API cannot show 0 for a
			// handler that actually runs with a 10s/5s budget.
			TimeoutSeconds: int(hook.EffectiveTimeout().Seconds()),
			StatusMessage:  hook.StatusMessage,
			Source:         hook.Source,
			SourcePath:     hook.SourcePath,
			Enabled:        hook.Enabled == nil || *hook.Enabled,
		})
	}
	h.writeJSON(w, http.StatusOK, harnessHooksResponse{
		WorkspacePath: workspace,
		Hooks:         out,
		Sources:       discovered.Sources,
		Warnings:      discovered.Warnings,
		RecentRuns:    h.recentHookRuns(),
		Count:         len(out),
	})
}

// UpdateHarnessHook toggles a handler's enabled state in its source hooks
// file. Only files discovered for this workspace are writable.
func (h *Handler) UpdateHarnessHook(w http.ResponseWriter, r *http.Request) {
	var req harnessHookUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	req.SourcePath = strings.TrimSpace(req.SourcePath)
	if req.ID == "" || req.SourcePath == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "id and source_path are required"))
		return
	}

	workspace := strings.TrimSpace(r.URL.Query().Get("workspace_path"))
	if workspace == "" && h.runtimeConfig != nil {
		workspace = strings.TrimSpace(h.runtimeConfig.Workspace.Root)
	}
	mergedHooks, discovered := h.resolveHookSources(workspace)
	allowed := discovered.Sources
	target, err := filepath.Abs(req.SourcePath)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid source_path"))
		return
	}
	permitted := false
	for _, source := range allowed {
		if abs, err := filepath.Abs(source); err == nil && abs == target {
			permitted = true
			break
		}
	}
	if !permitted {
		h.writeError(w, http.StatusForbidden, errors.New(errors.ErrValidationFailed, "source_path is not a discovered hooks file"))
		return
	}
	// Only user/project hooks are user-toggleable. Plugin/managed handlers are
	// governed by their own trust/state stores.
	toggleAllowed := false
	for _, hook := range mergedHooks {
		if hook.ID == req.ID && (hook.Source == "user" || hook.Source == "project") {
			toggleAllowed = true
			break
		}
	}
	if !toggleAllowed {
		h.writeError(w, http.StatusForbidden, errors.New(errors.ErrValidationFailed, "only user/project hooks can be toggled"))
		return
	}
	if err := runtimehooks.SetHookEnabled(target, req.ID, req.Enabled); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.GetHarnessHooks(w, r)
}

// hookSourcePaths returns the discovered hooks files for the workspace. It is
// the write allowlist.
func (h *Handler) resolveHookSources(workspace string) ([]runtimehooks.HookConfig, runtimehooks.DiscoverResult) {
	trust := workspaceFolderTrust(workspace)
	sources := h.hostHookSources(workspace, trust.Trusted, nil)
	return runtimehooks.ResolveForHostFromEnv(sources)
}

// hostHookSources collects only environment-specific inputs. All ordering,
// managed-only suppression, and discovery rules live in internal/hooks.
func (h *Handler) hostHookSources(workspace string, trusted bool, base []runtimehooks.HookConfig) runtimehooks.HostSources {
	sources := runtimehooks.HostSources{
		Base:        base,
		ProjectRoot: strings.TrimSpace(workspace),
		SkipProject: !trusted,
	}
	if runtimehooks.AllowManagedHooksOnlyFromEnv() {
		return sources
	}
	catalog, err := plugins.Discover(plugins.DiscoverOptions{
		ProjectRoot:     sources.ProjectRoot,
		SkipProjectRoot: sources.SkipProject,
		State:           plugins.NewStateStore(""),
	})
	if err == nil && catalog != nil {
		for _, file := range catalog.ActiveHooksFiles() {
			sources.PluginFiles = append(sources.PluginFiles, runtimehooks.PluginHookFile{
				Path:     file.Path,
				PluginID: file.PluginID,
			})
		}
		sources.LegacyPluginHooks = catalog.ActiveHooks()
	}
	return sources
}
