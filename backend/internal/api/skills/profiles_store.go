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

// listRuntimeProfileEntries 枚举三来源（config 注册项 / default root 子目录 /
// 默认值补行），并标注解析状态。排序：registered 优先，其余按名字。
func (h *Handler) listRuntimeProfileEntries() (*runtimeProfileListResult, error) {
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

	result.Count = len(result.Profiles)
	return result, nil
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
