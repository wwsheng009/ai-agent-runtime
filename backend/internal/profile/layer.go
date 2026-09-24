package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LayerRoot 返回层根目录（G1/G2 只允许 user 与 project 两层）。
//
// 规则本体只此一份：API（internal/api/skills 的 profile 写端点）与 CLI
// （aicli profile create/import/move）共用。两个入口各写一套"层根在哪"会立刻
// 分叉——一个写 <home>/.aicli/profiles、另一个写 ./profiles，而冲突检查、
// 引用重定向都建立在这个路径上。
func LayerRoot(layer string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(layer)) {
	case "user":
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", fmt.Errorf("无法定位用户配置目录：%v", err)
		}
		return filepath.Join(home, ".aicli", "profiles"), nil
	case "project":
		cwd, err := os.Getwd()
		if err != nil || strings.TrimSpace(cwd) == "" {
			return "", fmt.Errorf("无法定位当前工作目录：%v", err)
		}
		return filepath.Join(cwd, ".aicli", "profiles"), nil
	default:
		return "", fmt.Errorf("layer 只支持 user / project：%q", layer)
	}
}

// LayerNames 返回标准层的**发现优先级顺序**：项目层优先于用户层
// （项目内同名 profile 覆盖用户层，与目录型配置"项目优先"的既有语义一致）。
//
// 顺序只此一份：CLI/TUI 的 `profile list` 与 profile 引用解析共用，
// 避免"列表里 project 在前、解析时 user 生效"这类静默分叉。
func LayerNames() []string {
	return []string{"project", "user"}
}

// LayerRootForWorkspace 返回把 project 层绑定到**指定工作区**时的层根。
//
// 服务端（runtime-server）的进程 cwd 不等于会话工作区，用 LayerRoot("project")
// 会把层根解析到服务进程的启动目录上，因此按工作区参数解析。user 层与
// LayerRoot 同源（工作区不影响用户层）。
func LayerRootForWorkspace(layer, workspace string) (string, error) {
	if strings.ToLower(strings.TrimSpace(layer)) != "project" {
		return LayerRoot(layer)
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return "", fmt.Errorf("工作区为空，无法定位 project 层根")
	}
	return filepath.Join(workspace, ".aicli", "profiles"), nil
}

// LayerProfile 描述标准层根（LayerRoot）下的一个 profile 目录。
type LayerProfile struct {
	Layer string // user | project
	Name  string // 目录名（= 引用名）
	Root  string // profile 根目录
}

// LayerForRoot 判定 root 属于哪个标准层（"" 表示不在标准层内，如自定义 root）。
// 规则与 LayerRoot 同源：只比较路径前缀，不解析符号链接。
func LayerForRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	for _, layer := range LayerNames() {
		base, err := LayerRoot(layer)
		if err != nil || strings.TrimSpace(base) == "" {
			continue
		}
		if pathWithin(base, root) {
			return layer
		}
	}
	return ""
}

// LayerProfiles 枚举标准层根下含 profile.yaml 的 profile 目录，
// 顺序 = LayerNames（project → user），层内按目录名升序。
//
// 单层不可定位/不可读时**跳过该层**而不报错：层是 profile 的补充发现来源，
// 一个读不到的层根（权限、临时目录被清理）不该让"config 注册项可用"的会话
// 整体解析失败。层根的缺失/不可读不等于"该 profile 不存在"，因此调用方在
// 解析失败时仍会如实报错（不静默降级为"未找到"）。
func LayerProfiles() []LayerProfile {
	found := make([]LayerProfile, 0, 4)
	for _, layer := range LayerNames() {
		base, err := LayerRoot(layer)
		if err != nil || strings.TrimSpace(base) == "" {
			continue
		}
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() {
				names = append(names, entry.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			root := filepath.Join(base, name)
			if !hasProfileFile(root) {
				continue
			}
			found = append(found, LayerProfile{Layer: layer, Name: name, Root: root})
		}
	}
	return found
}

// RegisterLayerFallbacks 把标准层下的 profile 注册进 registry，作为**兜底**来源。
//
// 优先级（高 → 低）：config 注册项 > profiles.root 下可用的同名目录 >
// project 层 > user 层。前两者仍然优先，既有语义不变；只有"config/root 都给不出
// 可用目录"的名字才由层目录补齐。
//
// 为什么需要它：TUI/API 的 create/duplicate/import/move 把 profile 落在层根
// （LayerRoot 是唯一写目标），而 registry 只认 config 注册项与 profiles.root。
// 不补这一层就会出现"创建成功 → /profile use 报未知 profile"的写读分叉。
//
// 返回被注册的层 profile（按注册顺序），供调用方做"来源=层"的展示与测试断言。
func RegisterLayerFallbacks(registry *Registry) []LayerProfile {
	if registry == nil {
		return nil
	}
	known := make(map[string]struct{})
	for _, entry := range registry.Entries() {
		known[entry.Name] = struct{}{}
	}
	defaultRoot := strings.TrimSpace(registry.DefaultRoot())
	added := make([]LayerProfile, 0, 4)
	for _, candidate := range LayerProfiles() {
		if _, exists := known[candidate.Name]; exists {
			continue
		}
		// profiles.root 下存在同名可用目录时以 root 为准（显式配置优先于层发现）。
		if defaultRoot != "" && hasProfileFile(filepath.Join(defaultRoot, candidate.Name)) {
			continue
		}
		if err := registry.Register(candidate.Name, candidate.Root); err != nil {
			continue
		}
		known[candidate.Name] = struct{}{}
		added = append(added, candidate)
	}
	return added
}

// hasProfileFile 判断目录下是否有可读的 profile.yaml（发现层的存在性口径，
// 与 CLI `profile list`、API 清单同一判据：没有 profile.yaml 的目录不是 profile）。
func hasProfileFile(root string) bool {
	root = strings.TrimSpace(root)
	if root == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(root, "profile.yaml"))
	return err == nil && !info.IsDir()
}

// pathWithin 判断 child 是否位于 parent 之内（路径前缀比较，不解析符号链接）。
func pathWithin(parent, child string) bool {
	parent = strings.TrimSpace(parent)
	child = strings.TrimSpace(child)
	if parent == "" || child == "" {
		return false
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
