package skills

import (
	stderrors "errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// errRuntimeProfileNotFound / errRuntimeProfileBadRef 是 handler 层的状态码判据
// （404 vs 400），避免为一次查找引入第二套错误码体系。
var (
	errRuntimeProfileNotFound = stderrors.New("runtime profile not found")
	errRuntimeProfileBadRef   = stderrors.New("invalid runtime profile reference")
	// errRuntimeProfileWorkspaceInvalid 表示请求给出的 workspace 参数本身不可用
	// （为空以外的非法形态：路径不存在、不是目录）。这是**调用方输入错误**（400），
	// 与"绑定文件内容有错"（仍是 200 + project_binding.error）刻意分开：前者重试
	// 同样的参数永远不会成功，后者是工作区里的事实，应当展示而不是让整页列表失败。
	errRuntimeProfileWorkspaceInvalid = stderrors.New("invalid profile workspace parameter")
)

// Profiles API 的文件系统层（Batch 8 任务 1）。
//
// 设计约束（与实施方案 §4 Batch 8 对齐）：
//   - 解析**不新建逻辑**：一律走 `internal/profile` 的 Load/Resolve/Validate；
//   - 发现来源与 CLI `profile list` 同一套词汇（config / root / default / path），
//     避免出现"UI 与 CLI 各有一套来源名"的第二套方言；
//   - 写回只动 profile.yaml 与 profile 目录，**绝不触碰全局 config.yaml**
//     （唯一例外是 `POST /{ref}/default`，它写的是 `profiles.default_profile`，
//     走既有 config 文档写服务）。

// runtimeProfileEntry 是列表视图的一行。
type runtimeProfileEntry struct {
	Ref          string `json:"ref"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Source       string `json:"source"`
	Layer        string `json:"layer,omitempty"`
	Path         string `json:"path"`
	Valid        bool   `json:"valid"`
	Error        string `json:"error,omitempty"`
	IsDefault    bool   `json:"is_default"`
	DefaultAgent string `json:"default_agent,omitempty"`
	// Writable 标注 API 能否写回该 profile（当前：有 profile.yaml 的目录都可写；
	// 保留字段用于后续内置只读 profile 的只读标记）。
	Writable bool `json:"writable"`
	// PromptSuppressed / PromptSuppressionReason 是 D29 门控在清单上的投影
	// （Batch 14 slice 5）：工作区未信任时，项目层 profile 的 prompts 被扣留，
	// 前端据此显示"部分内容未应用"徽标。只有**确有可扣留内容**的项目层 profile
	// 才会置位——没有 prompt 的 profile 不制造假警告（与运行期门控同一判据）。
	PromptSuppressed        bool   `json:"prompt_suppressed,omitempty"`
	PromptSuppressionReason string `json:"prompt_suppression_reason,omitempty"`
	// IsBound 标注该条目名就是本工作区 `.aicli/profile` 指针指向的 profile
	// （FR-14 只读发现）。它**不是** default，也不代表已激活：绑定只是候选，
	// 应用仍要用户显式发起（会话内 `/profile`）。
	IsBound bool `json:"is_bound,omitempty"`
}

type runtimeProfileListResult struct {
	Profiles       []runtimeProfileEntry `json:"profiles"`
	Count          int                   `json:"count"`
	DefaultProfile string                `json:"default_profile,omitempty"`
	DefaultRoot    string                `json:"default_root,omitempty"`
	Source         string                `json:"source"`
	// SessionSwitch 是**能力广告**（R20）：true 表示本后端支持会话级
	// profile 切换（`POST /runtime/sessions/{id}/runtime/commands` 的
	// `set_profile` 命令）。前端 composer 只在为 true 时注册 `/profile`
	// 命令——旧后端（无 Batch 12 执行核心）不返回该字段，命令不注册，
	// 而不是注册后执行时报错。字段随清单端点一并返回，避免前端多一次探测。
	SessionSwitch bool `json:"session_switch"`
	// WorkspacePath / WorkspaceTrusted / WorkspaceTrustFeatureEnabled 是 D29 工作区
	// 信任上下文（Batch 14 slice 5，Q22 前端闭环的数据面）：仅当调用方在请求里
	// 显式给出 `workspace` 参数时填充。`workspace_path` 为空表示"本次请求未声明
	// 工作区"，前端据此不渲染信任提示（旧调用零变化）。
	WorkspacePath                string `json:"workspace_path,omitempty"`
	WorkspaceTrusted             bool   `json:"workspace_trusted"`
	WorkspaceTrustFeatureEnabled bool   `json:"workspace_trust_feature_enabled"`
	// ProjectBinding 是本工作区 `.aicli/profile` 的只读发现结果（FR-14）：
	// 仅当调用方显式给出 workspace 参数时填充（否则字段省略，旧调用零变化）。
	//
	// 绑定错误**留在本字段**而不是让整个列表失败（除 workspace 参数本身非法外），
	// 这样"上一个项目留了个指向不存在 profile 的绑定文件"不会把设置页打成空页。
	ProjectBinding *profilesys.ProjectProfileBinding `json:"project_binding,omitempty"`
}

// runtimeProfileTarget 是一次 ref → root 的解析结果。
type runtimeProfileTarget struct {
	Ref    string
	Root   string
	Source string
	Layer  string
}

// authorizeProfileWrite 与既有会话/技能写端点同级（M4：回环 / admin token / admin role）。
func (h *Handler) authorizeProfileWrite(r *http.Request) error {
	if h.hasValidSearchAdminToken(r) || h.hasTrustedAdminRole(r) || isLoopbackRequest(r) {
		return nil
	}
	return errors.New(errors.ErrAgentPermission,
		"profile write endpoints require loopback access, valid admin token, or admin role")
}

// profileConfigSnapshot 返回宿主 aicli 配置里的 profiles 节（可能为 nil）。
func (h *Handler) profileConfigSnapshot() *profilesConfigView {
	cfg := h.aicliConfigSnapshot()
	view := &profilesConfigView{}
	if cfg != nil && cfg.Profiles != nil {
		view.Root = strings.TrimSpace(cfg.Profiles.Root)
		view.DefaultProfile = strings.TrimSpace(cfg.Profiles.DefaultProfile)
		for name, item := range cfg.Profiles.Items {
			view.Items = append(view.Items, profilesys.Entry{Name: name, Root: strings.TrimSpace(item.Root)})
		}
		sort.Slice(view.Items, func(i, j int) bool { return view.Items[i].Name < view.Items[j].Name })
	}
	if view.Root == "" && h.profileRegistry != nil {
		view.Root = h.profileRegistry.DefaultRoot()
	}
	if view.DefaultProfile == "" {
		view.DefaultProfile = strings.TrimSpace(h.profileDefaultRef)
	}
	if len(view.Items) == 0 && h.profileRegistry != nil {
		view.Items = h.profileRegistry.Entries()
	}
	return view
}

// profilesConfigView 是宿主配置中 profiles 节的只读投影。
type profilesConfigView struct {
	Root           string
	DefaultProfile string
	Items          []profilesys.Entry
}

func (v *profilesConfigView) itemRoot(name string) (string, bool) {
	if v == nil {
		return "", false
	}
	for _, item := range v.Items {
		if item.Name == name {
			return item.Root, true
		}
	}
	return "", false
}

// listRuntimeProfileEntries 枚举四来源（config 注册项 / default root 子目录 /
// 标准层根（user|project）/ 默认值补行），并标注解析状态。排序：registered 优先，
// 其余按名字。去重顺序即优先级：config > root > project 层 > user 层。
//
// workspace 非空时额外标注 D29 工作区信任上下文与逐条 prompts 扣留标记
// （Batch 14 slice 5）；为空时行为与既有完全一致。
func (h *Handler) listRuntimeProfileEntries(workspace string) (*runtimeProfileListResult, error) {
	view := h.profileConfigSnapshot()
	result := &runtimeProfileListResult{
		Profiles:       make([]runtimeProfileEntry, 0, 8),
		DefaultProfile: view.DefaultProfile,
		DefaultRoot:    view.Root,
		Source:         "runtime",
		// R20 能力广告：本二进制编译进了 Batch 12 的 `set_profile` 执行核心
		// （session_profile_switch.go），因此清单端点声明会话级切换可用。这是
		// **构建级能力**而非本次请求级状态——旧后端没有该执行核心，也不返回
		// 该字段（前端 readBoolean 缺省 false ⇒ 不注册 `/profile`）。
		SessionSwitch: true,
	}
	seen := make(map[string]struct{})

	addEntry := func(entry runtimeProfileEntry) {
		if _, exists := seen[entry.Name]; exists {
			return
		}
		seen[entry.Name] = struct{}{}
		result.Profiles = append(result.Profiles, entry)
	}

	for _, item := range view.Items {
		entry := runtimeProfileEntry{
			Ref:       item.Name,
			Name:      item.Name,
			Source:    "config",
			Layer:     profileLayerForRoot(item.Root),
			Path:      item.Root,
			IsDefault: item.Name == view.DefaultProfile,
			Writable:  strings.TrimSpace(item.Root) != "",
		}
		if strings.TrimSpace(item.Root) == "" {
			entry.Valid = false
			entry.Error = "root 未配置"
		} else {
			describeRuntimeProfileEntry(&entry)
		}
		addEntry(entry)
	}

	if view.Root != "" {
		subdirs, err := os.ReadDir(view.Root)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("read profiles root %s: %w", view.Root, err)
			}
		} else {
			names := make([]string, 0, len(subdirs))
			for _, dir := range subdirs {
				if dir.IsDir() {
					names = append(names, dir.Name())
				}
			}
			sort.Strings(names)
			for _, name := range names {
				root := filepath.Join(view.Root, name)
				if !profileRootHasProfileYAML(root) {
					continue
				}
				entry := runtimeProfileEntry{
					Ref:       name,
					Name:      name,
					Source:    "root",
					Layer:     profileLayerForRoot(root),
					Path:      root,
					IsDefault: name == view.DefaultProfile,
					Writable:  true,
				}
				describeRuntimeProfileEntry(&entry)
				addEntry(entry)
			}
		}
	}

	// 层来源（G4 写点的读侧，与 CLI `profile list` 共用 profilesys 的层枚举）：
	// create/move/import 的落盘目标是标准层根，清单必须与 config/root 同列可见，
	// 否则"创建成功但清单里查不到、切不了"（写读分叉）。
	//
	// 请求给出 workspace 时 project 层按**该工作区**枚举（LayerProfilesForWorkspace，
	// FR-14）：服务进程 cwd 不等于会话工作区，用无参 LayerProfiles() 会列出 server
	// 启动目录的项目层 profile——既是错误发现，也是跨工作区信息泄漏。
	for _, layerProfile := range profilesys.LayerProfilesForWorkspace(workspace) {
		entry := runtimeProfileEntry{
			Ref:       layerProfile.Name,
			Name:      layerProfile.Name,
			Source:    "layer",
			Layer:     layerProfile.Layer,
			Path:      layerProfile.Root,
			IsDefault: layerProfile.Name == view.DefaultProfile,
			Writable:  true,
		}
		describeRuntimeProfileEntry(&entry)
		addEntry(entry)
	}

	// 默认值指向的 profile 未出现在任何来源时补一行（与 CLI `profile list` 同语义：
	// "默认值指向不存在的 profile"必须可见，不能被静默吞掉）。
	if view.DefaultProfile != "" {
		if _, exists := seen[view.DefaultProfile]; !exists {
			root := ""
			if view.Root != "" {
				root = filepath.Join(view.Root, view.DefaultProfile)
			}
			entry := runtimeProfileEntry{
				Ref:       view.DefaultProfile,
				Name:      view.DefaultProfile,
				Source:    "default",
				Layer:     profileLayerForRoot(root),
				Path:      root,
				IsDefault: true,
				Writable:  root != "",
			}
			if root == "" {
				entry.Valid = false
				entry.Error = "root 未配置"
			} else {
				describeRuntimeProfileEntry(&entry)
			}
			addEntry(entry)
		}
	}

	// FR-14：workspace 声明时读取该工作区的项目绑定（只读指针，不激活、不改 default）。
	// 绑定文件本身的错误（YAML/ref/目标缺失）留在 metadata 里；只有 workspace 参数
	// 本身不可用才升级成 400（调用方输入错误，不该伪装成"工作区没有绑定"）。
	if strings.TrimSpace(workspace) != "" {
		binding, err := profilesys.LoadProjectProfileBinding(workspace)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errRuntimeProfileWorkspaceInvalid, err)
		}
		result.ProjectBinding = binding
		markBoundRuntimeProfileEntry(result)
	}

	h.annotateWorkspaceTrust(result, workspace)

	result.Count = len(result.Profiles)
	return result, nil
}

// markBoundRuntimeProfileEntry 把"这个名字就是本工作区绑定目标"标注到清单条目上。
//
// 只在绑定有效时标注：绑定指向不存在的 profile 时，清单里没有可信的对应条目，
// 不能靠名字猜一个（那会把别的层的同名 profile 说成"已绑定"）。
// 名字命中即标注（不限制 layer）：binding 只约束**目标目录**必须是本工作区项目层，
// 而条目来源仍按既有去重优先级（config > root > project > user）如实展示；若同名的
// config 条目压过项目层，用户在这里看到"已绑定"但解析落到 config——这是既有优先级
// 语义，不是绑定引入的新行为，首版不额外做来源仲裁。
func markBoundRuntimeProfileEntry(result *runtimeProfileListResult) {
	if result == nil || result.ProjectBinding == nil {
		return
	}
	binding := result.ProjectBinding
	if !binding.Valid || strings.TrimSpace(binding.Ref) == "" {
		return
	}
	for i := range result.Profiles {
		if result.Profiles[i].Name == binding.Ref {
			result.Profiles[i].IsBound = true
		}
	}
}

// annotateWorkspaceTrust 把 D29 工作区信任结论与逐条 prompts 扣留标记写进清单。
//
// 只在调用方显式给出 workspace 时工作：未给出时不填任何字段（旧调用零变化）。
// 判定与运行期门控**同源**（`profilesys.EvaluateProjectPromptGate`，与
// profile_support.go 的 `ApplyProjectPromptGate` 是同一函数），因此清单上显示的
// "未应用"与实际生效面不会分叉。
//
// 成本：信任特性关闭或工作区已信任时零额外开销；仅"未信任"时对有效条目各做一次
// 解析（项目层 profile 通常 0-1 个）。
func (h *Handler) annotateWorkspaceTrust(result *runtimeProfileListResult, workspace string) {
	if result == nil {
		return
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return
	}
	res := workspaceFolderTrust(workspace)
	result.WorkspacePath = workspace
	result.WorkspaceTrusted = res.Trusted
	result.WorkspaceTrustFeatureEnabled = res.FeatureEnabled
	if res.Trusted {
		return
	}
	for i := range result.Profiles {
		entry := &result.Profiles[i]
		if !entry.Valid || strings.TrimSpace(entry.Path) == "" {
			continue
		}
		if suppressed, reason := projectPromptSuppression(entry.Path, workspace); suppressed {
			entry.PromptSuppressed = true
			entry.PromptSuppressionReason = reason
		}
	}
	// 绑定目标同样要如实报告扣留：绑定卡片上的"部分内容未应用"必须与实际
	// 解析（ApplyProjectPromptGate）同源，否则用户会以为绑定已完整生效。
	if binding := result.ProjectBinding; binding != nil && binding.Valid {
		if suppressed, reason := projectPromptSuppression(binding.Root, workspace); suppressed {
			binding.PromptSuppressed = true
			binding.PromptSuppressionReason = reason
		}
	}
}

// projectPromptSuppression 用与运行期**同一函数**（EvaluateProjectPromptGate）
// 判定某个 profile root 在未信任工作区下是否有 prompts 被扣留。解析失败返回
// false：条目本身已由 valid/error 报告解析问题，这里不制造第二套诊断。
func projectPromptSuppression(root, workspace string) (bool, string) {
	root = strings.TrimSpace(root)
	if root == "" {
		return false, ""
	}
	resolved, err := profilesys.Resolve(profilesys.ResolveOptions{Root: root})
	if err != nil {
		return false, ""
	}
	gate := profilesys.EvaluateProjectPromptGate(resolved, workspace, false)
	return gate.Suppressed, gate.Reason
}

// describeRuntimeProfileEntry 填充 valid/error/description/default_agent。
func describeRuntimeProfileEntry(entry *runtimeProfileEntry) {
	if entry == nil {
		return
	}
	if strings.TrimSpace(entry.Path) == "" {
		entry.Valid = false
		entry.Error = "root 未配置"
		return
	}
	if !profileRootHasProfileYAML(entry.Path) {
		entry.Valid = false
		entry.Error = "profile.yaml 不存在"
		return
	}
	spec, err := profilesys.LoadProfile(entry.Path)
	if err != nil {
		entry.Valid = false
		entry.Error = err.Error()
		return
	}
	entry.Description = strings.TrimSpace(spec.Profile.Description)
	entry.DefaultAgent = strings.TrimSpace(spec.Profile.DefaultAgent)
	for _, issue := range profilesys.ValidateProfileSpec(spec) {
		if issue.Severity == profilesys.ProfileSpecIssueError {
			entry.Valid = false
			entry.Error = fmt.Sprintf("%s: %s", issue.Path, issue.Message)
			return
		}
	}
	entry.Valid = true
}

// resolveRuntimeProfileTarget 把 API 的 {ref} 解析为具体 root。
//
// API 只接受**名字**（config 注册名 / default root 子目录名）：路径引用是 CLI
// 语义（`aicli profile show .\path`），且路径含分隔符无法作为单段路由参数出现，
// 因此这里显式拒绝并给出可执行的提示，而不是静默失败。
func (h *Handler) resolveRuntimeProfileTarget(ref string) (runtimeProfileTarget, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return runtimeProfileTarget{}, fmt.Errorf("%w: profile reference is required", errRuntimeProfileBadRef)
	}
	if strings.ContainsAny(ref, "/\\:") {
		return runtimeProfileTarget{}, fmt.Errorf("%w: profiles API 只接受 profile 名（路径引用请使用 CLI：aicli profile show <path>）",
			errRuntimeProfileBadRef)
	}

	view := h.profileConfigSnapshot()
	if root, ok := view.itemRoot(ref); ok {
		if strings.TrimSpace(root) == "" {
			return runtimeProfileTarget{}, fmt.Errorf("%w: profile %q 的 root 未配置", errRuntimeProfileNotFound, ref)
		}
		return runtimeProfileTarget{Ref: ref, Root: root, Source: "config", Layer: profileLayerForRoot(root)}, nil
	}
	if view.Root != "" {
		candidate := filepath.Join(view.Root, ref)
		if profileRootHasProfileYAML(candidate) {
			return runtimeProfileTarget{Ref: ref, Root: candidate, Source: "root", Layer: profileLayerForRoot(candidate)}, nil
		}
	}
	if h.profileRegistry != nil {
		for _, entry := range h.profileRegistry.Entries() {
			if entry.Name == ref {
				return runtimeProfileTarget{Ref: ref, Root: entry.Root, Source: "config", Layer: profileLayerForRoot(entry.Root)}, nil
			}
		}
		if root, err := h.profileRegistry.Resolve(ref); err == nil && profileRootHasProfileYAML(root) {
			return runtimeProfileTarget{Ref: ref, Root: root, Source: "default", Layer: profileLayerForRoot(root)}, nil
		}
	}
	// 层兜底：与 TUI `/profile` 的解析侧同源（profilesys.RegisterLayerFallbacks）。
	// 优先级不变：config 注册项 > default root 子目录 > project 层 > user 层。
	for _, layerProfile := range profilesys.LayerProfiles() {
		if layerProfile.Name == ref {
			return runtimeProfileTarget{Ref: ref, Root: layerProfile.Root, Source: "layer", Layer: layerProfile.Layer}, nil
		}
	}
	return runtimeProfileTarget{}, fmt.Errorf("%w: profile %q 未找到（可查 GET /api/runtime/profiles 的可用清单）",
		errRuntimeProfileNotFound, ref)
}

// profileRootHasProfileYAML 判定目录是否为 profile root。
func profileRootHasProfileYAML(root string) bool {
	root = strings.TrimSpace(root)
	if root == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(root, "profile.yaml"))
	return err == nil && !info.IsDir()
}

// profileLayerForRoot 归类 profile 所在层（展示用，不参与任何安全判定）：
//   - user：用户配置目录（<home>/.aicli 下）
//   - project：当前工作目录的 .aicli 下
//   - custom：其他注册路径（无法归层时如实标注，不猜）
func profileLayerForRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	clean := filepath.Clean(root)
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		if pathWithinDir(filepath.Join(home, ".aicli"), clean) {
			return "user"
		}
	}
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		if pathWithinDir(filepath.Join(cwd, ".aicli"), clean) {
			return "project"
		}
	}
	return "custom"
}

func pathWithinDir(parent, target string) bool {
	rel, err := filepath.Rel(parent, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, "..")
}

// profileFileMtime 返回 RFC3339Nano 的 mtime（空字符串表示文件不存在）。
func profileFileMtime(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return info.ModTime().UTC().Format(time.RFC3339Nano)
}

// writeProfileYAMLAtomic 原子写 profile.yaml（同目录临时文件 + rename）。
// 注释与排版由调用方决定：raw YAML 原样落盘，不做重新序列化。
func writeProfileYAMLAtomic(path string, data []byte) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("profile path is required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create profile directory: %w", err)
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	}
	temp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp profile file: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if err := temp.Chmod(mode); err != nil {
		return fmt.Errorf("prepare temp profile file mode: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("write temp profile file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync temp profile file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temp profile file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace profile file: %w", err)
	}
	return nil
}
