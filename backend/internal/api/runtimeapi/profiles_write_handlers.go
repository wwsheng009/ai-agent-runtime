package runtimeapi

import (
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gorilla/mux"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// ---------------------------------------------------------------------------
// 写端点：创建 / 复制 / 更新 / 应用
// ---------------------------------------------------------------------------

// profileCreateRequest 是创建/复制端点的请求体（§23 G1：三模式合一入口）。
type profileCreateRequest struct {
	// Name 是新建 profile 的名字（同时是目录名与 profile.name）。
	Name string `json:"name"`
	// Layer ∈ {user, project}；缺省 user（与 G1「落到 user 层（默认）」一致）。
	Layer string `json:"layer,omitempty"`
	// Root 显式覆盖落盘目录（与 layer 同时给出时以 root 为准）。
	Root string `json:"root,omitempty"`
	// Template 模板名（与 FromRef 互斥；都缺省时用内置默认模板）。
	Template string `json:"template,omitempty"`
	// FromRef 复制来源（duplicate 模式）。
	FromRef string `json:"from_ref,omitempty"`
	// FromSession 会话固化（D24 差分固化 / D35 落地口径）——Batch 13 slice 6 接线，
	// 与 template / from_ref 互斥（三模式合一入口）。
	FromSession string `json:"from_session,omitempty"`
	// Agent 模板的 default_agent（缺省 TemplateDefaultAgent）。
	Agent string `json:"agent,omitempty"`
	// Force 目标目录已存在且非空时覆盖（复制模式下先清空目标）。
	Force bool `json:"force,omitempty"`
	// Use 注册到 config `profiles.items[name].root`。
	Use bool `json:"use,omitempty"`
	// SetDefault 同时写入 config `profiles.default_profile`（只影响新会话，D26）。
	SetDefault bool `json:"set_default,omitempty"`
}

// profileUpdateRequest 是 PUT /profiles/{ref} 的请求体。
type profileUpdateRequest struct {
	// YAML 原样落盘（保留注释与排版）；与 Spec 同时给出时以 YAML 为准。
	YAML string `json:"yaml,omitempty"`
	// Spec 结构化草稿（无 YAML 时序列化后落盘）。
	Spec map[string]interface{} `json:"spec,omitempty"`
	// ExpectedMtime 是客户端读到的 mtime（R24/V25 冲突检测）：不一致即 409，
	// 不合并、不静默覆盖。
	ExpectedMtime string `json:"expected_mtime,omitempty"`
}

// profileApplyRequest 是 POST /profiles/{ref}/apply 的请求体（D26：应用到当前会话）。
type profileApplyRequest struct {
	// SessionID 是目标会话，**必填**。apply 只影响显式给出的这一个会话（A12：
	// default 不变、别的会话不变），服务端不推断「当前会话」——设置页（/runtime-config）
	// 没有会话上下文，推断必然要在多个会话里挑一个，那是把别人的会话切走。
	// 会话内立即切换走 composer `/profile`（Batch 12 的 set_profile 命令，同一执行核心）。
	SessionID string `json:"session_id,omitempty"`
}

// CreateRuntimeProfile 创建 profile：模板 / 复制 / 从会话固化三模式（G1 三模式合一入口）。
func (h *Handler) CreateRuntimeProfile(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeProfileWrite(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	var req profileCreateRequest
	if r.Body == nil || json.NewDecoder(r.Body).Decode(&req) != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid create request body"))
		return
	}
	h.createRuntimeProfile(w, r, req)
}

// createRuntimeProfile 是创建端点族的共享实现（POST /profiles 与
// POST /profiles/{ref}/duplicate 走同一段代码，避免两条路径语义漂移）。
func (h *Handler) createRuntimeProfile(w http.ResponseWriter, r *http.Request, req profileCreateRequest) {
	name := strings.TrimSpace(req.Name)
	if err := profilesys.ValidateProfileName(name); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	fromSession := strings.TrimSpace(req.FromSession)
	fromRef := strings.TrimSpace(req.FromRef)
	template := strings.TrimSpace(req.Template)
	if fromRef != "" && template != "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"template 与 from_ref 互斥：复制用 from_ref，模板新建用 template"))
		return
	}
	if fromSession != "" && (fromRef != "" || template != "") {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"from_session 与 template/from_ref 互斥：固化用 from_session，复制用 from_ref，模板新建用 template"))
		return
	}
	if fromSession != "" && req.Force {
		// 与 CLI 同一条纪律（D35）：差分固化不覆盖既有目录——半覆盖
		//（profile.yaml 被换掉、agents/ 与 prompts/ 仍是旧的）比拒绝更糟。
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"save-as 不提供 force：差分固化不覆盖既有 profile，请换名或先删除目标"))
		return
	}

	absRoot, layer, err := h.resolveRuntimeProfileCreateRoot(name, req.Layer, req.Root)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	if err := assertRuntimeProfileCreateTarget(absRoot, req.Force); err != nil {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrConfigInvalid, err.Error()))
		return
	}

	mode := "template"
	files := []string{}
	saveAsExtra := map[string]interface{}{}
	switch {
	case fromSession != "":
		// 从会话固化（save-as 差分固化）：先渲染（此时不落盘），再走与模板创建
		// 同一段落盘路径，避免两条路径的写盘纪律漂移。
		mode = "save_as"
		result, renderErr := h.renderRuntimeProfileSaveAs(r, fromSession, name, req.Agent)
		if renderErr != nil {
			switch {
			case isRuntimeProfileSaveAsValidationError(renderErr):
				h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, renderErr.Error()))
			case stderrors.Is(renderErr, chat.ErrSessionNotFound):
				// 未知会话是客户端语义（404），与 apply / handler.go 的会话读取一致。
				h.writeError(w, http.StatusNotFound, renderErr)
			default:
				h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "save-as profile failed", renderErr))
			}
			return
		}
		files, err = writeRuntimeProfileSaveAsFiles(absRoot, result.files)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "write profile file failed", err))
			return
		}
		saveAsExtra = map[string]interface{}{
			"from_session": fromSession,
			"agent":        result.agent,
			"baseline":     result.baseline,
			"surface":      runtimeProfileSaveAsSurfaceSummary(result.surface),
			"omitted":      result.omitted,
		}
	case fromRef != "":
		mode = "duplicate"
		source, err := h.resolveRuntimeProfileTarget(fromRef)
		if err != nil {
			h.writeProfileTargetError(w, err)
			return
		}
		if sameFilePath(source.Root, absRoot) {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
				"复制目标与来源相同："+absRoot))
			return
		}
		if err := copyRuntimeProfileTree(source.Root, absRoot, req.Force); err != nil {
			h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "duplicate profile failed", err))
			return
		}
		files = listRuntimeProfileFiles(absRoot)
	default:
		if template == "" {
			template = defaultRuntimeProfileTemplate()
		}
		rendered, err := profilesys.RenderTemplate(template, name, strings.TrimSpace(req.Agent))
		if err != nil {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
			return
		}
		relPaths := make([]string, 0, len(rendered))
		for rel := range rendered {
			relPaths = append(relPaths, rel)
		}
		sort.Strings(relPaths)
		for _, rel := range relPaths {
			target := filepath.Join(absRoot, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "create profile dir failed", err))
				return
			}
			if err := os.WriteFile(target, rendered[rel], 0o644); err != nil {
				h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "write profile file failed", err))
				return
			}
		}
		files = relPaths
	}

	configPath := ""
	registered := false
	defaultSet := false
	if req.Use || req.SetDefault {
		configPath, err = h.profileConfigWritePath()
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "resolve config path failed", err))
			return
		}
		update := agentconfig.ProfilesConfigUpdate{}
		register := req.Use || !h.runtimeProfileResolvableByName(name, absRoot)
		if register {
			update.ItemName = name
			update.ItemRoot = absRoot
		}
		if req.SetDefault {
			update.DefaultProfile = name
		}
		if err := agentconfig.UpdateProfilesConfig(configPath, update); err != nil {
			h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "write profiles config failed", err))
			return
		}
		registered = register
		defaultSet = req.SetDefault
		h.syncAICLIProfilesSnapshot(func(profiles *agentconfig.ProfilesConfig) {
			if register {
				if profiles.Items == nil {
					profiles.Items = map[string]agentconfig.ProfileConfig{}
				}
				profiles.Items[name] = agentconfig.ProfileConfig{Root: absRoot}
			}
			if req.SetDefault {
				profiles.DefaultProfile = name
			}
		})
	}

	entry := runtimeProfileEntry{
		Ref:       name,
		Name:      name,
		Source:    "created",
		Layer:     layer,
		Path:      absRoot,
		IsDefault: defaultSet,
		Writable:  true,
	}
	describeRuntimeProfileEntry(&entry)
	response := map[string]interface{}{
		"created":             true,
		"mode":                mode,
		"template":            template,
		"from_ref":            fromRef,
		"name":                name,
		"root":                absRoot,
		"layer":               layer,
		"files":               files,
		"profile":             entry,
		"config_path":         configPath,
		"registered":          registered,
		"default_profile_set": defaultSet,
		// D26 文案口径：default 只影响新会话；要影响当前会话需 apply（Batch 13）。
		"affects": "new_sessions_only",
	}
	for key, value := range saveAsExtra {
		response[key] = value
	}
	h.writeJSON(w, http.StatusCreated, response)
}

// DuplicateRuntimeProfile 复制（等价于 POST /profiles 的 from_ref 模式）。
func (h *Handler) DuplicateRuntimeProfile(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeProfileWrite(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	ref := mux.Vars(r)["ref"]
	var req profileCreateRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	req.FromRef = ref
	req.Template = ""
	req.FromSession = ""
	if strings.TrimSpace(req.Name) == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "name is required"))
		return
	}
	// 复制走同一实现（避免两条路径语义漂移）。
	h.createRuntimeProfile(w, r, req)
}

// UpdateRuntimeProfile 写回 profile.yaml：校验 → 原子写 → 返回新视图。
func (h *Handler) UpdateRuntimeProfile(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeProfileWrite(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	ref := mux.Vars(r)["ref"]
	var req profileUpdateRequest
	if r.Body == nil || json.NewDecoder(r.Body).Decode(&req) != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid update request body"))
		return
	}
	target, err := h.resolveRuntimeProfileTarget(ref)
	if err != nil {
		h.writeProfileTargetError(w, err)
		return
	}
	profileFile := filepath.Join(target.Root, "profile.yaml")
	if !profileRootHasProfileYAML(target.Root) {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrConfigNotFound,
			"profile.yaml 不存在："+profileFile))
		return
	}
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
	if !validation.Valid {
		// 不写盘：校验失败必须零副作用（与 §17.2「失败零状态改动」同纪律）。
		h.writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"updated":       false,
			"valid":         false,
			"error_count":   validation.ErrorCount,
			"warning_count": validation.WarningCount,
			"issues":        validation.Issues,
			"error":         "profile.yaml 校验失败，未写入磁盘",
		})
		return
	}
	currentMtime := profileFileMtime(profileFile)
	if expected := strings.TrimSpace(req.ExpectedMtime); expected != "" && expected != currentMtime {
		// R24/V25：文件在客户端读取后被改动过——拒绝并提示 reload，不合并。
		h.writeJSON(w, http.StatusConflict, map[string]interface{}{
			"updated":        false,
			"error":          "profile.yaml 已被其他写入者修改（mtime 冲突），请 reload 后重试",
			"expected_mtime": expected,
			"current_mtime":  currentMtime,
		})
		return
	}
	if err := writeProfileYAMLAtomic(profileFile, content); err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "write profile.yaml failed", err))
		return
	}
	view, err := h.buildRuntimeProfileView(target, "", false)
	if err != nil {
		h.writeProfileResolveError(w, err)
		return
	}
	nameMismatch := strings.TrimSpace(validation.Spec.Profile.Name) != "" &&
		strings.TrimSpace(validation.Spec.Profile.Name) != target.Ref
	result := map[string]interface{}{
		"updated":       true,
		"profile":       view,
		"mtime":         profileFileMtime(profileFile),
		"warning_count": validation.WarningCount,
		"issues":        validation.Issues,
		"name_mismatch": nameMismatch,
	}
	if nameMismatch {
		// 改名不是 PUT 的职责（目录名/注册项/引用都要跟着动）——给出可执行提示。
		result["hint"] = "profile.name 与 ref 不一致：改名请用 POST /api/runtime/profiles/" + target.Ref + "/rename"
	}
	h.writeJSON(w, http.StatusOK, result)
}

// ApplyRuntimeProfile 把 profile 应用到指定会话（D26/D21：与 Batch 12 的会话内切换
// 共用 `applySessionProfileSwitch` 同一执行核心，五阶段与失效动作完全一致）。
func (h *Handler) ApplyRuntimeProfile(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeProfileWrite(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	ref := mux.Vars(r)["ref"]
	target, err := h.resolveRuntimeProfileTarget(ref)
	if err != nil {
		h.writeProfileTargetError(w, err)
		return
	}
	var req profileApplyRequest
	if r.Body != nil {
		// 空体是合法输入（下面 session_id 校验会给出可执行提示），只有真正的
		// 解析失败才算请求体损坏。
		if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil && !stderrors.Is(decodeErr, io.EOF) {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid apply request body"))
			return
		}
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"session_id 必填：apply 只作用于显式指定的会话（A12），服务端不推断「当前会话」；"+
				"会话内立即切换请用 composer `/profile`（同一执行核心）"))
		return
	}
	report, switchErr := h.applySessionProfileSwitch(r.Context(), sessionID, target.Ref)
	switch {
	case switchErr == nil:
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":            true,
			"session_id":    sessionID,
			"profile":       target.Ref,
			"switch_report": report,
		})
	case isSessionProfileSwitchValidationError(switchErr):
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, switchErr.Error()))
	case h.writeSessionLeaseConflict(w, switchErr):
	case stderrors.Is(switchErr, chat.ErrSessionNotFound):
		// 未知会话是客户端语义（404），与 handler.go 的会话读取口径一致——不落 500，
		// 否则前端会把「会话 id 传错」当成可重试的服务故障。
		h.writeError(w, http.StatusNotFound, switchErr)
	default:
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "apply profile failed", switchErr))
	}
}

// ---------------------------------------------------------------------------
// 创建/复制辅助
// ---------------------------------------------------------------------------

// runtimeProfileLayerRoot 返回层根目录（G1/G2 只允许 user 与 project 两层）。
// 规则本体在 internal/profile（CLI 的 create/import/move 共用同一份），这里只是
// 包内别名，避免 API 与 CLI 对"层根在哪"各写一套。
func runtimeProfileLayerRoot(layer string) (string, error) {
	return profilesys.LayerRoot(layer)
}

// resolveRuntimeProfileCreateRoot 解析新建 profile 的落盘目录与层名。
func (h *Handler) resolveRuntimeProfileCreateRoot(name, layer, explicitRoot string) (string, string, error) {
	layer = strings.ToLower(strings.TrimSpace(layer))
	if layer == "" {
		layer = "user"
	}
	root := strings.TrimSpace(explicitRoot)
	if root == "" {
		layerRoot, err := runtimeProfileLayerRoot(layer)
		if err != nil {
			return "", "", err
		}
		root = filepath.Join(layerRoot, name)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve profile root %s: %w", root, err)
	}
	absRoot = filepath.Clean(absRoot)
	if err := assertRuntimeProfileRootSafe(absRoot); err != nil {
		return "", "", err
	}
	if explicitRoot != "" {
		layer = profileLayerForRoot(absRoot)
	}
	return absRoot, layer, nil
}

// assertRuntimeProfileRootSafe 拒绝把 profile 落在层根/文件系统根这类位置上
// （否则 delete/move 会把整个 profiles 根目录搬走）。
func assertRuntimeProfileRootSafe(root string) error {
	if strings.TrimSpace(root) == "" {
		return fmt.Errorf("profile root is required")
	}
	clean := filepath.Clean(root)
	if filepath.Dir(clean) == clean {
		return fmt.Errorf("profile root 不能是文件系统根：%s", clean)
	}
	for _, layer := range []string{"user", "project"} {
		if layerRoot, err := runtimeProfileLayerRoot(layer); err == nil && sameFilePath(layerRoot, clean) {
			return fmt.Errorf("profile root 不能是层根目录：%s", clean)
		}
	}
	return nil
}

// assertRuntimeProfileCreateTarget 镜像 CLI ensureProfileCreateTarget 的语义：
// 目录已存在且非空时，无 force 直接冲突（不静默混入旧文件）。
func assertRuntimeProfileCreateTarget(root string, force bool) error {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat profile root %s: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("目标已存在且不是目录：%s", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read profile root %s: %w", root, err)
	}
	if len(entries) > 0 && !force {
		return fmt.Errorf("目标目录已存在且非空：%s（使用 force=true 覆盖）", root)
	}
	return nil
}

// defaultRuntimeProfileTemplate 返回内置默认模板名（与 CLI `profile create`
// 缺省一致：优先 "coding"，模板集变化时退化为字典序首个，不硬编码第二份清单）。
func defaultRuntimeProfileTemplate() string {
	names := profilesys.TemplateNames()
	for _, name := range names {
		if name == "coding" {
			return name
		}
	}
	if len(names) > 0 {
		return names[0]
	}
	return "coding"
}

// copyRuntimeProfileTree 深拷贝 profile 目录（force=true 时先清空目标）。
func copyRuntimeProfileTree(src, dst string, force bool) error {
	src = filepath.Clean(strings.TrimSpace(src))
	dst = filepath.Clean(strings.TrimSpace(dst))
	if !profileRootHasProfileYAML(src) {
		return fmt.Errorf("来源不是 profile 目录（缺少 profile.yaml）：%s", src)
	}
	if err := assertRuntimeProfileRootSafe(dst); err != nil {
		return err
	}
	if force {
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("clear target %s: %w", dst, err)
		}
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("create target %s: %w", dst, err)
	}
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil // 跳过符号链接/设备文件：复制 profile 不搬运特殊文件。
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// listRuntimeProfileFiles 列出 profile 目录下的相对文件清单（删除确认/复制回执用）。
func listRuntimeProfileFiles(root string) []string {
	files := make([]string, 0, 8)
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if rel, relErr := filepath.Rel(root, path); relErr == nil {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// sameFilePath 比较两个路径是否指向同一位置（Windows 大小写不敏感）。
func sameFilePath(a, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	if a == "" || b == "" {
		return false
	}
	if strings.EqualFold(a, b) {
		return true
	}
	return a == b
}

// runtimeProfileResolvableByName 判定 name 是否已能被 profiles.root 直接解析
// （决定 set-default 是否需要同时注册 items 项，避免写出不可解析的 default）。
func (h *Handler) runtimeProfileResolvableByName(name, root string) bool {
	view := h.profileConfigSnapshot()
	if strings.TrimSpace(view.Root) == "" {
		return false
	}
	return sameFilePath(filepath.Join(view.Root, name), root)
}

// syncAICLIProfilesSnapshot 把刚落盘的 profiles 节同步进进程内快照（与
// syncAICLIRoutingSnapshot 同纪律：不刷新的话 GET 会回显旧值）。
func (h *Handler) syncAICLIProfilesSnapshot(mutate func(*agentconfig.ProfilesConfig)) {
	if h == nil {
		return
	}
	current := h.aicliConfigSnapshot()
	if current == nil {
		return // 快照未接线：保持现状，不凭空造配置。
	}
	next := *current
	if current.Profiles == nil {
		next.Profiles = &agentconfig.ProfilesConfig{}
	} else {
		cloned := &agentconfig.ProfilesConfig{
			Root:           current.Profiles.Root,
			DefaultProfile: current.Profiles.DefaultProfile,
		}
		if len(current.Profiles.Items) > 0 {
			cloned.Items = make(map[string]agentconfig.ProfileConfig, len(current.Profiles.Items))
			for name, item := range current.Profiles.Items {
				cloned.Items[name] = item
			}
		}
		next.Profiles = cloned
	}
	if mutate != nil {
		mutate(next.Profiles)
	}
	h.SetAICLIConfig(&next)
}
