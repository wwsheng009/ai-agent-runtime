package runtimeapi

import (
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gorilla/mux"
	"gopkg.in/yaml.v3"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// Profiles API（实施方案 Batch 8 任务 1）。
//
// 端点族与语义（设计文档 §10.5 + §23 G1-G3）：
//
//	GET    /api/runtime/profiles                    列表（三来源 + 默认标注 + 解析状态）
//	GET    /api/runtime/profiles/{ref}              解析后视图（工具面/skills/mcp/prompts/agents/overrides/估算）
//	PUT    /api/runtime/profiles/{ref}              写回 profile.yaml（校验 + 原子写 + mtime 冲突）
//	POST   /api/runtime/profiles/{ref}/validate     校验（与 CLI `profile validate` 同一实现）
//	POST   /api/runtime/profiles/{ref}/preview      预览（只读解析 + 影响面，不写盘）
//	POST   /api/runtime/profiles/{ref}/default      设为默认（新会话；写 profiles.default_profile）
//	POST   /api/runtime/profiles/{ref}/apply        应用到当前会话（Batch 13 执行核心：显式 session_id，未知会话 404）
//	POST   /api/runtime/profiles                    创建（模板 / 复制 / 从会话固化三模式合一）
//	POST   /api/runtime/profiles/import             导入 zip 包为新 profile（Batch 13 G5/D28：先 validate、绝不自动激活）
//	POST   /api/runtime/profiles/{ref}/export       导出 zip 包（Batch 13 G5：只读，不写盘）
//	POST   /api/runtime/profiles/{ref}/duplicate    复制（等价创建端点 from_ref 模式）
//	POST   /api/runtime/profiles/{ref}/rename       重命名（同层）
//	POST   /api/runtime/profiles/{ref}/move         层级移动（user↔project）
//	DELETE /api/runtime/profiles/{ref}              删除（引用检查 + ?force=true）
//	GET    /api/runtime/profiles/{ref}/references   只读：列出可枚举的引用
//
// 权限：读端点与 `/mcps` 同级（回环 / admin token / admin role）；写端点显式声明
// 同一策略（authorizeProfileWrite）。写回**只动 profile 目录**，唯一例外是
// `default` 端点写 `profiles.default_profile`（走 agentconfig 的配置写锁与分层写路由）。
// export 为只读；import 先物化到临时目录跑同一 validate、再原子 rename 落位，
// 不写配置、不碰会话（D28）。

func (h *Handler) registerProfileRoutes(runtimeRouter *mux.Router) {
	runtimeRouter.HandleFunc("/profiles", h.ListRuntimeProfiles).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/profiles", h.CreateRuntimeProfile).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/profiles/import", h.ImportRuntimeProfile).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/profiles/{ref}", h.GetRuntimeProfile).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/profiles/{ref}", h.UpdateRuntimeProfile).Methods(http.MethodPut)
	runtimeRouter.HandleFunc("/profiles/{ref}", h.DeleteRuntimeProfile).Methods(http.MethodDelete)
	runtimeRouter.HandleFunc("/profiles/{ref}/validate", h.ValidateRuntimeProfile).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/profiles/{ref}/preview", h.PreviewRuntimeProfile).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/profiles/{ref}/default", h.SetRuntimeProfileDefault).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/profiles/{ref}/apply", h.ApplyRuntimeProfile).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/profiles/{ref}/export", h.ExportRuntimeProfile).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/profiles/{ref}/duplicate", h.DuplicateRuntimeProfile).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/profiles/{ref}/rename", h.RenameRuntimeProfile).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/profiles/{ref}/move", h.MoveRuntimeProfile).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/profiles/{ref}/references", h.GetRuntimeProfileReferences).Methods(http.MethodGet)
}

// ListRuntimeProfiles 返回可用 profile 清单。
func (h *Handler) ListRuntimeProfiles(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	// workspace 为可选参数（Batch 14 slice 5）：给出时清单附带 D29 工作区信任
	// 上下文与逐条 prompts 扣留标记；不给出时响应与既有完全一致（旧前端零变化）。
	workspace := strings.TrimSpace(r.URL.Query().Get("workspace"))
	if workspace == "" {
		workspace = strings.TrimSpace(r.URL.Query().Get("workspace_path"))
	}
	result, err := h.listRuntimeProfileEntries(workspace)
	if err != nil {
		// workspace 参数本身不可用（不存在/不是目录）是调用方输入错误：400，
		// 而不是让前端把它当成服务故障重试。绑定**文件**的问题不走这里
		// （它们留在 project_binding.error 里，列表照常返回）。
		if stderrors.Is(err, errRuntimeProfileWorkspaceInvalid) {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
			return
		}
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "failed to list profiles", err))
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

// GetRuntimeProfile 返回单个 profile 的解析后视图。
func (h *Handler) GetRuntimeProfile(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	ref := mux.Vars(r)["ref"]
	agent := strings.TrimSpace(r.URL.Query().Get("agent"))
	target, err := h.resolveRuntimeProfileTarget(ref)
	if err != nil {
		h.writeProfileTargetError(w, err)
		return
	}
	view, err := h.buildRuntimeProfileView(target, agent, false)
	if err != nil {
		h.writeProfileResolveError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, view)
}

// PreviewRuntimeProfile 与 GET 同构，但显式标注"未写盘"，用于编辑器保存前预览。
func (h *Handler) PreviewRuntimeProfile(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	ref := mux.Vars(r)["ref"]
	var req profilePreviewRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	target, err := h.resolveRuntimeProfileTarget(ref)
	if err != nil {
		h.writeProfileTargetError(w, err)
		return
	}
	view, err := h.buildRuntimeProfileView(target, strings.TrimSpace(req.Agent), true)
	if err != nil {
		h.writeProfileResolveError(w, err)
		return
	}
	// 预览可携带未落盘草稿：只做校验与影响面，不写盘（对齐 config/document/preview）。
	if strings.TrimSpace(req.YAML) != "" || req.Spec != nil {
		content, err := resolveProfileDraftContent(req.YAML, req.Spec)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, errors.Wrap(errors.ErrValidationFailed, "invalid profile draft", err))
			return
		}
		validation, err := validateProfileDraft(content)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, errors.Wrap(errors.ErrValidationFailed, "invalid profile draft", err))
			return
		}
		view["draft"] = map[string]interface{}{
			"provided":      true,
			"valid":         validation.Valid,
			"issues":        validation.Issues,
			"error_count":   validation.ErrorCount,
			"warning_count": validation.WarningCount,
			"spec":          validation.Spec,
		}
	}
	h.writeJSON(w, http.StatusOK, view)
}

// ValidateRuntimeProfile 与 CLI `profile validate` 共用实现（internal/profile）。
func (h *Handler) ValidateRuntimeProfile(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	ref := mux.Vars(r)["ref"]
	var req profilePreviewRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	target, err := h.resolveRuntimeProfileTarget(ref)
	if err != nil {
		h.writeProfileTargetError(w, err)
		return
	}
	agent := strings.TrimSpace(req.Agent)
	if strings.TrimSpace(req.YAML) != "" || req.Spec != nil {
		content, err := resolveProfileDraftContent(req.YAML, req.Spec)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, errors.Wrap(errors.ErrValidationFailed, "invalid profile draft", err))
			return
		}
		validation, err := validateProfileDraft(content)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, errors.Wrap(errors.ErrValidationFailed, "invalid profile draft", err))
			return
		}
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"ref":           target.Ref,
			"path":          filepath.Join(target.Root, "profile.yaml"),
			"draft":         true,
			"valid":         validation.Valid,
			"error_count":   validation.ErrorCount,
			"warning_count": validation.WarningCount,
			"issues":        validation.Issues,
		})
		return
	}

	validation, err := profilesys.ValidateProfileReference(target.Root, agent, h.profileResolveOptions(""))
	if err != nil {
		// 声明无法解析（ErrInvalidProfileSpec 等）时**报告**而不是失败：校验端点的
		// 职责是告诉调用方哪里不对（引用不存在才 404）。
		if stderrors.Is(err, profilesys.ErrProfileNotFound) || stderrors.Is(err, profilesys.ErrAgentNotFound) {
			h.writeProfileResolveError(w, err)
			return
		}
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"ref":           target.Ref,
			"path":          filepath.Join(target.Root, "profile.yaml"),
			"valid":         false,
			"error_count":   1,
			"warning_count": 0,
			"issues": []profilesys.ProfileSpecIssue{{
				Severity: profilesys.ProfileSpecIssueError,
				Path:     "profile.yaml",
				Message:  err.Error(),
			}},
		})
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"ref":           target.Ref,
		"name":          validation.ProfileName,
		"path":          filepath.Join(target.Root, "profile.yaml"),
		"root":          validation.ProfileRoot,
		"agent":         validation.AgentID,
		"valid":         validation.Valid,
		"error_count":   validation.ErrorCount,
		"warning_count": validation.WarningCount,
		"issues":        validation.Issues,
	})
}

// profilePreviewRequest 是 validate/preview 的请求体（全部字段可选）。
type profilePreviewRequest struct {
	Agent string                 `json:"agent,omitempty"`
	YAML  string                 `json:"yaml,omitempty"`
	Spec  map[string]interface{} `json:"spec,omitempty"`
}

// profileResolveOptions 组装解析选项（全局层路径与宿主一致，避免 UI 与运行时
// 解析出不同的文件集）。
func (h *Handler) profileResolveOptions(agent string) profilesys.ResolveOptions {
	return profilesys.ResolveOptions{
		Agent:             strings.TrimSpace(agent),
		GlobalRuntimePath: strings.TrimSpace(h.profileGlobalRuntimePath),
		GlobalMCPPath:     strings.TrimSpace(h.profileGlobalMCPPath),
		GlobalSkillDirs:   append([]string(nil), h.profileGlobalSkillDirs...),
	}
}

// buildRuntimeProfileView 组装解析后视图（GET 与 preview 共用）。
func (h *Handler) buildRuntimeProfileView(target runtimeProfileTarget, agent string, preview bool) (map[string]interface{}, error) {
	options := h.profileResolveOptions(agent)
	options.Root = target.Root
	resolved, err := profilesys.Resolve(options)
	if err != nil {
		return nil, err
	}
	spec, err := profilesys.LoadProfile(target.Root)
	if err != nil {
		return nil, err
	}
	validation, err := profilesys.ValidateProfileReference(target.Root, agent, h.profileResolveOptions(""))
	if err != nil {
		if stderrors.Is(err, profilesys.ErrProfileNotFound) || stderrors.Is(err, profilesys.ErrAgentNotFound) {
			return nil, err
		}
		validation = &profilesys.ReferenceValidation{
			Valid:      false,
			ErrorCount: 1,
			Issues: []profilesys.ProfileSpecIssue{{
				Severity: profilesys.ProfileSpecIssueError,
				Path:     "profile.yaml",
				Message:  err.Error(),
			}},
		}
	}

	profileFile := filepath.Join(target.Root, "profile.yaml")
	// UI 编辑分组（Batch 8 前端契约）：只读生效面由后端单点推导，见
	// profiles_view_groups.go；顶层 overrides/mcp_servers 保留兼容形态。
	groups := buildRuntimeProfileViewGroups(target, spec, resolved)
	view := map[string]interface{}{
		"ref":           target.Ref,
		"name":          strings.TrimSpace(spec.Profile.Name),
		"description":   strings.TrimSpace(spec.Profile.Description),
		"source":        target.Source,
		"layer":         target.Layer,
		"path":          target.Root,
		"profile_file":  profileFile,
		"mtime":         profileFileMtime(profileFile),
		"valid":         validation.Valid,
		"error_count":   validation.ErrorCount,
		"warning_count": validation.WarningCount,
		"issues":        validation.Issues,
		"spec":          spec,
		"resolved":      resolved,
		"overrides":     groups["overrides"],
		"tools":         groups["tools"],
		"skills":        groups["skills"],
		"mcp":           groups["mcp"],
		"prompts":       groups["prompts"],
		"agents":        groups["agents"],
		"preferences":   groups["preferences"],
		"estimate":      buildRuntimeProfileEstimate(spec, resolved),
		"mcp_servers":   buildRuntimeProfileMCPServers(resolved),
		"preview":       preview,
		"default_agent": strings.TrimSpace(spec.Profile.DefaultAgent),
		"prompt_mode":   resolved.PromptMode,
		"write_target":  profileFile,
	}
	return view, nil
}

// buildRuntimeProfileOverrides 返回覆盖键清单与来源标记（复用解析期 origins 结构）。
func buildRuntimeProfileOverrides(spec *profilesys.ProfileSpec) map[string]interface{} {
	if spec == nil {
		return map[string]interface{}{"keys": []string{}, "origins": map[string]string{}}
	}
	keys := profilesys.OverrideKeyList(spec.Runtime.Overrides)
	origins := profilesys.OverrideOrigins(spec.Runtime.Overrides)
	if keys == nil {
		keys = []string{}
	}
	if origins == nil {
		origins = map[string]string{}
	}
	return map[string]interface{}{
		"keys":    keys,
		"origins": origins,
		"count":   len(keys),
	}
}

// buildRuntimeProfileEstimate 给出**声明口径**的估算（与 CLI `profile show` 同一
// 估算函数，避免两套数字）。注意：这里估算的是 profile 声明的选择列表与 prompt
// 文件体积，不是运行时工具 schema 全量（工具 schema 的权威来源在运行时目录）。
func buildRuntimeProfileEstimate(spec *profilesys.ProfileSpec, resolved *profilesys.ResolvedAgent) map[string]interface{} {
	estimate := map[string]interface{}{
		"basis": "profile_declarations",
	}
	if resolved == nil {
		return estimate
	}
	estimate["tool_allow_count"] = len(resolved.ToolPolicy.Allowlist)
	estimate["tool_deny_count"] = len(resolved.ToolPolicy.Denylist)
	estimate["skill_allow_count"] = len(resolved.Skills.Allowlist)
	estimate["skill_deny_count"] = len(resolved.Skills.Denylist)
	estimate["skill_dir_count"] = len(resolved.SkillDirs)
	if tokens, err := profilesys.EstimateJSONTokens(resolved.ToolPolicy.Allowlist); err == nil {
		estimate["tool_allow_tokens"] = tokens
	}
	if tokens, err := profilesys.EstimateJSONTokens(resolved.ToolPolicy.Denylist); err == nil {
		estimate["tool_deny_tokens"] = tokens
	}
	if spec != nil {
		if tokens, err := profilesys.EstimateJSONTokens(spec.Runtime.Overrides); err == nil {
			estimate["override_tokens"] = tokens
		}
	}
	promptFiles := []struct {
		label string
		path  string
	}{
		{"system", resolved.Prompts.System},
		{"role", resolved.Prompts.Role},
		{"tools", resolved.Prompts.Tools},
	}
	promptTokens := 0
	files := make([]map[string]interface{}, 0, len(promptFiles))
	for _, file := range promptFiles {
		path := strings.TrimSpace(file.path)
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		tokens := profilesys.EstimateFileTokens(int(info.Size()))
		promptTokens += tokens
		files = append(files, map[string]interface{}{
			"kind":   file.label,
			"path":   path,
			"bytes":  info.Size(),
			"tokens": tokens,
		})
	}
	estimate["prompt_files"] = files
	estimate["prompt_tokens"] = promptTokens
	// UI 估算卡（前端契约）：tool_count/tool_tokens/total_tokens 与
	// tool_allow_tokens/prompt_tokens 同源，不引入第二套估算。
	toolTokens := 0
	if tokens, ok := estimate["tool_allow_tokens"].(int); ok {
		toolTokens = tokens
	}
	estimate["tool_count"] = len(resolved.ToolPolicy.Allowlist)
	estimate["tool_tokens"] = toolTokens
	estimate["total_tokens"] = toolTokens + promptTokens
	return estimate
}

// buildRuntimeProfileMCPServers 计算 use/exclude 之后的 server 选择（连接状态与
// 工具数不在这里猜：活状态由 `/api/runtime/mcps` 与 `/mcps/{name}/tools` 提供）。
func buildRuntimeProfileMCPServers(resolved *profilesys.ResolvedAgent) []map[string]interface{} {
	if resolved == nil {
		return []map[string]interface{}{}
	}
	names, err := profilesys.LoadMCPServerNames(resolved.MCPConfig)
	if err != nil {
		names = nil
	}
	useSet := make(map[string]struct{}, len(resolved.MCPSelection.UseServers))
	for _, name := range resolved.MCPSelection.UseServers {
		useSet[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	excludeSet := make(map[string]struct{}, len(resolved.MCPSelection.ExcludeServers))
	for _, name := range resolved.MCPSelection.ExcludeServers {
		excludeSet[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	servers := make([]map[string]interface{}, 0, len(names))
	for _, name := range names {
		key := strings.ToLower(name)
		_, excluded := excludeSet[key]
		_, used := useSet[key]
		if len(useSet) > 0 {
			used = used && !excluded
		} else {
			used = !excluded
		}
		servers = append(servers, map[string]interface{}{
			"name":     name,
			"used":     used,
			"excluded": excluded,
		})
	}
	return servers
}

// buildRuntimeProfileAgents 返回 profile 声明的 agent 清单（V7：agents.ts 是运行时
// 子代理身份图，不是 profile 级清单，因此这里读 spec.agents）。
func buildRuntimeProfileAgents(spec *profilesys.ProfileSpec) []map[string]interface{} {
	if spec == nil || len(spec.Agents) == 0 {
		return []map[string]interface{}{}
	}
	ids := make([]string, 0, len(spec.Agents))
	for id := range spec.Agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	agents := make([]map[string]interface{}, 0, len(ids))
	for _, id := range ids {
		agent := spec.Agents[id]
		agents = append(agents, map[string]interface{}{
			"id":       id,
			"provider": strings.TrimSpace(agent.Provider),
			"model":    strings.TrimSpace(agent.Model),
			"tools": map[string]interface{}{
				"allowlist": agent.Tools.Allowlist,
				"denylist":  agent.Tools.Denylist,
			},
		})
	}
	return agents
}

// resolveProfileDraftContent 把请求里的草稿（raw YAML 或结构化 spec）转成待校验的
// YAML 字节。两者都给时以 raw 为准（raw 保留注释与排版，是用户显式输入）。
func resolveProfileDraftContent(rawYAML string, spec map[string]interface{}) ([]byte, error) {
	if strings.TrimSpace(rawYAML) != "" {
		return []byte(rawYAML), nil
	}
	if spec == nil {
		return nil, fmt.Errorf("profile draft is empty")
	}
	content, err := yaml.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("marshal profile spec: %w", err)
	}
	return content, nil
}

// validateProfileDraft 解析并校验草稿（不写盘）。
func validateProfileDraft(content []byte) (*profilesys.ReferenceValidation, error) {
	spec := &profilesys.ProfileSpec{}
	if err := yaml.Unmarshal(content, spec); err != nil {
		return nil, fmt.Errorf("parse profile yaml: %w", err)
	}
	result := &profilesys.ReferenceValidation{
		Valid:  true,
		Spec:   spec,
		Issues: make([]profilesys.ProfileSpecIssue, 0, 4),
	}
	for _, issue := range profilesys.ValidateProfileSpec(spec) {
		result.Issues = append(result.Issues, issue)
		if issue.Severity == profilesys.ProfileSpecIssueError {
			result.ErrorCount++
		} else {
			result.WarningCount++
		}
	}
	result.Valid = result.ErrorCount == 0
	return result, nil
}

// writeProfileTargetError 把 ref → root 解析失败映射为状态码。
func (h *Handler) writeProfileTargetError(w http.ResponseWriter, err error) {
	switch {
	case stderrors.Is(err, errRuntimeProfileNotFound):
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrConfigNotFound, err.Error()))
	case stderrors.Is(err, errRuntimeProfileBadRef):
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
	default:
		h.writeError(w, http.StatusInternalServerError, err)
	}
}

// writeProfileResolveError 把解析失败映射为状态码（引用不存在 → 404）。
func (h *Handler) writeProfileResolveError(w http.ResponseWriter, err error) {
	switch {
	case stderrors.Is(err, profilesys.ErrProfileNotFound), stderrors.Is(err, profilesys.ErrAgentNotFound):
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrConfigNotFound, err.Error()))
	case stderrors.Is(err, profilesys.ErrInvalidProfileSpec):
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrConfigInvalid, err.Error()))
	default:
		h.writeError(w, http.StatusInternalServerError, err)
	}
}

// profileConfigWritePath 解析配置写目标（与 CLI ensureWritableAICLIConfigPath 同口径：
// cfg.ConfigFilePath → 现有配置搜索路径 → 可写默认路径），并在文件缺失时落 starter。
func (h *Handler) profileConfigWritePath() (string, error) {
	cfg := h.aicliConfigSnapshot()
	path := ""
	if cfg != nil {
		path = strings.TrimSpace(cfg.ConfigFilePath)
	}
	if path != "" {
		path = agentconfig.ResolveWritableConfigPath(path)
	} else if existing := agentconfig.ResolveConfigPath(agentconfig.DefaultConfigSearchPaths()); existing != "" {
		path = existing
	} else {
		path = agentconfig.ResolveWritableConfigPath("")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("config path is required")
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if _, _, err := agentconfig.EnsureStarterConfigAtPath(path); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if cfg != nil {
		cfg.ConfigFilePath = path
	}
	return path, nil
}
