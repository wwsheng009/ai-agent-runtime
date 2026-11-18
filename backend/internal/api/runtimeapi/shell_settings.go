package runtimeapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/workspaceregistry"
)

// shell 段的服务端设置面（Web 设置页 / TUI）：
//
//	GET  /api/runtime/config/shell
//	PUT  /api/runtime/config/shell
//	POST /api/runtime/config/shell/probe
//
// 设计与验收见 docs/plan/windows-posix-shell-detection-and-config-plan-20261104.md §9。

// ShellSettingsProvider 读取当前生效的 shell 配置与写入目标文件路径。
type ShellSettingsProvider func() (runtimeexecutor.ShellConfig, string, error)

// ShellSettingsPersister 保存 shell 配置并返回实际写入的配置文件路径。
type ShellSettingsPersister func(next runtimeexecutor.ShellConfig) (string, error)

// ShellSettingsProber 强制重探候选链并返回完整矩阵（不修改配置）。
type ShellSettingsProber func(cfg runtimeexecutor.ShellConfig) (*runtimeexecutor.ShellResolution, error)

// ShellSettingsScope identifies the workspace/user scope of a shell settings
// request. Empty WorkspacePath keeps the historical process-global behavior.
type ShellSettingsScope struct {
	WorkspacePath string `json:"workspace_path,omitempty"`
	DirectoryID   string `json:"directory_id,omitempty"`
	Scope         string `json:"scope,omitempty"`
}

// ShellSettingsScopeProvider reads a scoped shell config and its write target.
type ShellSettingsScopeProvider func(scope ShellSettingsScope) (runtimeexecutor.ShellConfig, string, error)

// ShellSettingsScopePersister writes a scoped shell config.
type ShellSettingsScopePersister func(next runtimeexecutor.ShellConfig, scope ShellSettingsScope) (string, error)

// SetShellSettingsProvider 设置 shell 配置读取回调（runtime-server 接 RuntimeManager）。
func (h *Handler) SetShellSettingsProvider(provider ShellSettingsProvider) {
	h.shellSettingsProvider = provider
}

// SetShellSettingsPersister 设置 shell 配置持久化回调。
func (h *Handler) SetShellSettingsPersister(persister ShellSettingsPersister) {
	h.shellSettingsPersister = persister
}

// SetShellSettingsProber 设置 shell 探测回调。
func (h *Handler) SetShellSettingsProber(prober ShellSettingsProber) {
	h.shellSettingsProber = prober
}

// SetShellSettingsScopeProvider configures workspace-aware shell settings.
func (h *Handler) SetShellSettingsScopeProvider(provider ShellSettingsScopeProvider) {
	if h == nil {
		return
	}
	h.shellSettingsScopeProvider = provider
}

// SetShellSettingsScopePersister configures workspace-aware shell persistence.
func (h *Handler) SetShellSettingsScopePersister(persister ShellSettingsScopePersister) {
	if h == nil {
		return
	}
	h.shellSettingsScopePersister = persister
}

// shellSettingsRequest 是 PUT 的请求体。字段用指针表示「未提供」：只改用户动过的
// 键，避免把一次下拉框选择变成整段覆盖。
type shellSettingsRequest struct {
	AutoDetect     *bool              `json:"auto_detect"`
	Prefer         *[]string          `json:"prefer"`
	Path           *string            `json:"path"`
	ProbeTimeout   *string            `json:"probe_timeout"`
	StrictProbe    *bool              `json:"strict_probe"`
	ScriptDelivery *string            `json:"script_delivery"`
	Env            *map[string]string `json:"env"`
	WorkspacePath  *string            `json:"workspace_path"`
	DirectoryID    *string            `json:"directory_id"`
	Scope          *string            `json:"scope"`
}

func (h *Handler) resolveShellSettingsScope(workspacePath, directoryID, scope string) (ShellSettingsScope, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	directoryID = strings.TrimSpace(directoryID)
	scope = strings.ToLower(strings.TrimSpace(scope))
	if directoryID != "" {
		record, ok := h.workspaceDirectoryRegistry().Get(directoryID)
		if !ok {
			return ShellSettingsScope{}, fmt.Errorf("%w: unknown directory id %s", workspaceregistry.ErrNotFound, directoryID)
		}
		workspacePath = record.Path
	}
	if workspacePath != "" {
		normalized, err := workspaceregistry.NormalizePath(workspacePath)
		if err != nil {
			return ShellSettingsScope{}, err
		}
		workspacePath = normalized
	}
	if scope == "" {
		if workspacePath != "" {
			scope = "workspace"
		} else {
			scope = "user"
		}
	}
	switch scope {
	case "workspace":
		if workspacePath == "" {
			return ShellSettingsScope{}, fmt.Errorf("workspace_path or directory_id is required for workspace-scoped shell settings")
		}
	case "user", "global", "server":
		scope = "user"
		workspacePath = ""
	default:
		return ShellSettingsScope{}, fmt.Errorf("invalid shell settings scope %q (want workspace or user)", scope)
	}
	return ShellSettingsScope{WorkspacePath: workspacePath, DirectoryID: directoryID, Scope: scope}, nil
}

func shellSettingsScopeNeedsWorkspace(scope ShellSettingsScope) bool {
	return strings.EqualFold(strings.TrimSpace(scope.Scope), "workspace") && strings.TrimSpace(scope.WorkspacePath) != ""
}

func (h *Handler) loadShellSettings(scope ShellSettingsScope) (runtimeexecutor.ShellConfig, string, error) {
	if shellSettingsScopeNeedsWorkspace(scope) {
		if h.shellSettingsScopeProvider == nil {
			return runtimeexecutor.ShellConfig{}, "", fmt.Errorf("workspace-scoped shell settings are not configured")
		}
		return h.shellSettingsScopeProvider(scope)
	}
	if h.shellSettingsProvider == nil {
		return runtimeexecutor.ShellConfig{}, "", fmt.Errorf("shell settings provider is not configured")
	}
	return h.shellSettingsProvider()
}

func (h *Handler) persistShellSettings(next runtimeexecutor.ShellConfig, scope ShellSettingsScope) (string, error) {
	if shellSettingsScopeNeedsWorkspace(scope) {
		if h.shellSettingsScopePersister == nil {
			return "", fmt.Errorf("workspace-scoped shell settings are not configured")
		}
		path, err := h.shellSettingsScopePersister(next, scope)
		if err != nil {
			return path, err
		}
		h.invalidateWorkspaceShell(scope.WorkspacePath)
		h.invalidateSessionRuntimeToolSurfaces()
		h.markShellSessionsForRebuild(scope.WorkspacePath, false)
		return path, nil
	}
	if h.shellSettingsPersister == nil {
		return "", fmt.Errorf("shell settings persister is not configured")
	}
	path, err := h.shellSettingsPersister(next)
	if err == nil {
		h.invalidateSessionRuntimeToolSurfaces()
		h.markShellSessionsForRebuild("", true)
	}
	return path, err
}

// GetShellSettings 返回生效值、候选矩阵与写入目标。
//
// 只读：不改内存快照、不写文件。矩阵走带缓存的 probe，重复打开设置页不会每次
// 重新付一遍 Powershell 冷启动（2s+）。
func (h *Handler) GetShellSettings(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		return
	}
	query := r.URL.Query()
	scope, err := h.resolveShellSettingsScope(
		query.Get("workspace_path"),
		query.Get("directory_id"),
		query.Get("scope"),
	)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	cfg, path, err := h.loadShellSettings(scope)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid,
			"failed to load shell settings", err))
		return
	}
	resolution, resolveErr := runtimeexecutor.ProbeShellCandidatesCached(cfg)
	h.writeJSON(w, http.StatusOK, h.shellSettingsResponse(cfg, path, resolution, resolveErr, false, scope))
}

// UpdateShellSettings 保存 shell 配置。
//
// 非法值一律 400 且**不改内存**：先用 ValidateShellConfig 校验完整结果，
// 再交给 persister（它先改内存快照，落盘失败会回滚）。
func (h *Handler) UpdateShellSettings(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		return
	}
	var req shellSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}
	scopeValue := ""
	if req.Scope != nil {
		scopeValue = *req.Scope
	}
	workspacePath := ""
	if req.WorkspacePath != nil {
		workspacePath = *req.WorkspacePath
	}
	directoryID := ""
	if req.DirectoryID != nil {
		directoryID = *req.DirectoryID
	}
	scope, err := h.resolveShellSettingsScope(workspacePath, directoryID, scopeValue)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}

	current, _, err := h.loadShellSettings(scope)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid,
			"failed to load shell settings", err))
		return
	}
	next := current
	if req.AutoDetect != nil {
		value := *req.AutoDetect
		next.AutoDetect = &value
	}
	if req.StrictProbe != nil {
		value := *req.StrictProbe
		next.StrictProbe = &value
	}
	if req.Prefer != nil {
		prefer := make([]string, 0, len(*req.Prefer))
		for _, id := range *req.Prefer {
			if trimmed := strings.ToLower(strings.TrimSpace(id)); trimmed != "" {
				prefer = append(prefer, trimmed)
			}
		}
		next.Prefer = prefer
	}
	if req.Path != nil {
		next.Path = strings.TrimSpace(*req.Path)
	}
	if req.ProbeTimeout != nil {
		timeout, parseErr := parseShellProbeTimeout(*req.ProbeTimeout)
		if parseErr != nil {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, parseErr.Error()))
			return
		}
		next.ProbeTimeout = timeout
	}
	if req.ScriptDelivery != nil {
		next.ScriptDelivery = runtimeexecutor.ScriptDelivery(strings.ToLower(strings.TrimSpace(*req.ScriptDelivery)))
	}
	if req.Env != nil {
		next.Env = cloneStringMap(*req.Env)
	}

	if err := runtimeexecutor.ValidateShellConfig(&next); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}

	path, err := h.persistShellSettings(next, scope)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid,
			"failed to persist shell settings", err))
		return
	}

	// 全局配置变了，下一次执行必须重新探测（以及让提示词的 shell 描述同步更新）。
	// 工作区配置只失效该 workspace 的解析缓存，不能改写其他会话。
	if !shellSettingsScopeNeedsWorkspace(scope) {
		runtimeexecutor.InvalidateShellResolution()
	}

	resolution, resolveErr := runtimeexecutor.ProbeShellCandidatesCached(next)
	h.publishSkillsChangedEvent(r, map[string]interface{}{
		"action":      "shell-settings-update",
		"status":      "success",
		"prefer":      next.NormalizedPrefer(),
		"config_file": strings.TrimSpace(path),
		"changed_by":  requestChangedBy(r),
	})
	h.writeJSON(w, http.StatusOK, h.shellSettingsResponse(next, path, resolution, resolveErr, true, scope))
}

// ProbeShellSettings 强制重探并返回新矩阵，不修改配置（设置页「检测」按钮）。
func (h *Handler) ProbeShellSettings(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		return
	}
	query := r.URL.Query()
	scope, err := h.resolveShellSettingsScope(
		query.Get("workspace_path"),
		query.Get("directory_id"),
		query.Get("scope"),
	)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	var req shellSettingsRequest
	bodyDecoded := false
	if r.Body != nil {
		decodeErr := json.NewDecoder(r.Body).Decode(&req)
		if decodeErr != nil && decodeErr != io.EOF {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
				"failed to parse request body"))
			return
		}
		if decodeErr == nil {
			bodyDecoded = true
			bodyWorkspace := ""
			if req.WorkspacePath != nil {
				bodyWorkspace = *req.WorkspacePath
			}
			bodyDirectory := ""
			if req.DirectoryID != nil {
				bodyDirectory = *req.DirectoryID
			}
			bodyScope := ""
			if req.Scope != nil {
				bodyScope = *req.Scope
			}
			if bodyWorkspace != "" || bodyDirectory != "" || bodyScope != "" {
				scope, err = h.resolveShellSettingsScope(bodyWorkspace, bodyDirectory, bodyScope)
				if err != nil {
					h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
					return
				}
			}
		}
	}
	cfg, path, err := h.loadShellSettings(scope)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid,
			"failed to load shell settings", err))
		return
	}
	probeCfg := cloneShellConfigForProbe(cfg)
	if bodyDecoded {
		// 允许携带「草稿」配置探测：设置页在保存前就能预览顺序调整的效果。
		if req.Prefer != nil {
			prefer := make([]string, 0, len(*req.Prefer))
			for _, id := range *req.Prefer {
				if trimmed := strings.ToLower(strings.TrimSpace(id)); trimmed != "" {
					prefer = append(prefer, trimmed)
				}
			}
			probeCfg.Prefer = prefer
		}
		if req.Path != nil {
			probeCfg.Path = strings.TrimSpace(*req.Path)
		}
		if req.AutoDetect != nil {
			probeCfg.AutoDetect = req.AutoDetect
		}
		if req.StrictProbe != nil {
			probeCfg.StrictProbe = req.StrictProbe
		}
		if req.ProbeTimeout != nil {
			timeout, parseErr := parseShellProbeTimeout(*req.ProbeTimeout)
			if parseErr != nil {
				h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, parseErr.Error()))
				return
			}
			probeCfg.ProbeTimeout = timeout
		}
		if req.ScriptDelivery != nil {
			probeCfg.ScriptDelivery = runtimeexecutor.ScriptDelivery(
				strings.ToLower(strings.TrimSpace(*req.ScriptDelivery)),
			)
		}
		if req.Env != nil {
			probeCfg.Env = cloneStringMap(*req.Env)
		}
	}
	if err := runtimeexecutor.ValidateShellConfig(&probeCfg); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}

	var resolution *runtimeexecutor.ShellResolution
	var resolveErr error
	if h.shellSettingsProber != nil {
		resolution, resolveErr = h.shellSettingsProber(probeCfg)
	} else {
		resolution, resolveErr = runtimeexecutor.ProbeShellCandidates(probeCfg)
	}
	h.writeJSON(w, http.StatusOK, h.shellSettingsResponse(probeCfg, path, resolution, resolveErr, false, scope))
}

// shellSettingsResponse 组装 GET/PUT/PROBE 同构的响应体。
func (h *Handler) shellSettingsResponse(
	cfg runtimeexecutor.ShellConfig,
	path string,
	resolution *runtimeexecutor.ShellResolution,
	resolveErr error,
	updated bool,
	scope ShellSettingsScope,
) map[string]interface{} {
	payload := map[string]interface{}{
		"auto_detect":       cfg.ShellAutoDetect(),
		"prefer":            runtimeexecutor.ShellCandidateChain(cfg),
		"configured_prefer": cfg.NormalizedPrefer(),
		"path":              strings.TrimSpace(cfg.Path),
		"probe_timeout":     cfg.EffectiveProbeTimeout().String(),
		"strict_probe":      cfg.ShellStrictProbe(),
		"script_delivery":   string(cfg.EffectiveScriptDelivery()),
		"config_path":       strings.TrimSpace(path),
		"writable":          strings.TrimSpace(path) != "",
		"supported_ids":     runtimeexecutor.ShellCandidateIDs(),
		"updated":           updated,
		"scope":             scope.Scope,
	}
	if strings.TrimSpace(scope.WorkspacePath) != "" {
		payload["workspace_path"] = scope.WorkspacePath
	}
	if strings.TrimSpace(scope.DirectoryID) != "" {
		payload["directory_id"] = scope.DirectoryID
	}
	if len(cfg.Env) > 0 {
		payload["env"] = cfg.Env
	}
	if resolution != nil {
		payload["candidates"] = resolution.Candidates
		if strings.TrimSpace(resolution.Shell.Path) != "" {
			payload["effective"] = map[string]interface{}{
				"id":       resolution.Shell.ID,
				"path":     resolution.Shell.Path,
				"version":  resolution.Shell.Version,
				"dialect":  string(resolution.Shell.Dialect),
				"source":   resolution.Shell.Source,
				"probe_ms": resolution.ProbeMS,
			}
		}
	}
	if resolveErr != nil {
		payload["error"] = resolveErr.Error()
	}
	return payload
}

// parseShellProbeTimeout accepts Go durations ("3s", "500ms") and a bare
// millisecond count, so the UI can send what the user typed.
func parseShellProbeTimeout(raw string) (time.Duration, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, nil
	}
	if timeout, err := time.ParseDuration(trimmed); err == nil {
		if timeout < 0 {
			return 0, fmt.Errorf("probe_timeout must not be negative")
		}
		return timeout, nil
	}
	var millis int
	if _, err := fmt.Sscanf(trimmed, "%d", &millis); err == nil && millis > 0 {
		return time.Duration(millis) * time.Millisecond, nil
	}
	return 0, fmt.Errorf("probe_timeout must be a Go duration such as 3s, got %q", trimmed)
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

// cloneShellConfigForProbe copies the slices/maps a probe config may mutate so a
// draft request can never alias the live config.
func cloneShellConfigForProbe(cfg runtimeexecutor.ShellConfig) runtimeexecutor.ShellConfig {
	cfg.Prefer = append([]string(nil), cfg.Prefer...)
	cfg.Env = cloneStringMap(cfg.Env)
	return cfg
}
