package skill

import (
	"os"
	"path/filepath"
	"strings"
)

// defaultProjectRootMarkers 与 codex 的 DEFAULT_PROJECT_ROOT_MARKERS 对齐：
// repo 级 `.agents/skills` 的祖先扫描以「最近一个含 marker 的目录」为界，
// 不再一路扫到盘根。空切片表示禁用项目根探测（项目根=anchor）。
var defaultProjectRootMarkers = []string{".git"}

// DiscoverCodexCompatibleSkillDirs 返回一组按 Codex 兼容规则补充的 skills 目录。
//
// anchor 通常是当前工作区或 profile root；configFile 用于推导系统/管理层的
// `<config_folder>/skills` 目录。结果只包含实际存在的目录，并保持稳定去重。
//
// 为了贴近当前项目的 Codex 兼容语义，这里还会显式补充：
// - `~/.aicli/skills`
// - `~/.aicli/agents/skills`
// - `~/.aicli/.agents/skills`
func DiscoverCodexCompatibleSkillDirs(anchor string, configFile string) []string {
	return discoverCodexCompatibleSkillDirs(anchor, configFile, resolveSkillDiscoveryHomeDir())
}

// CodexSkillRoot 描述一个参与发现的 Codex 兼容 skill 根目录（SK-6/发现诊断用）。
type CodexSkillRoot struct {
	Path  string
	Scope string
}

// DiscoverCodexCompatibleSkillRoots 返回 anchor/configFile 下参与发现的根目录，
// 含 scope（repo|user|…），顺序与发现顺序一致；仅包含实际存在的目录。
func DiscoverCodexCompatibleSkillRoots(anchor string, configFile string) []CodexSkillRoot {
	specs := discoverCodexCompatibleSkillRootSpecs(anchor, configFile, resolveSkillDiscoveryHomeDir())
	roots := make([]CodexSkillRoot, 0, len(specs))
	for _, spec := range specs {
		if strings.TrimSpace(spec.Path) == "" {
			continue
		}
		roots = append(roots, CodexSkillRoot{Path: spec.Path, Scope: spec.Scope})
	}
	return roots
}

func resolveSkillDiscoveryHomeDir() string {
	homeDir := strings.TrimSpace(os.Getenv("USERPROFILE"))
	if homeDir == "" {
		homeDir = strings.TrimSpace(os.Getenv("HOME"))
	}
	if homeDir == "" {
		if resolvedHomeDir, err := os.UserHomeDir(); err == nil {
			homeDir = strings.TrimSpace(resolvedHomeDir)
		}
	}
	return homeDir
}

func discoverCodexCompatibleSkillDirs(anchor string, configFile string, homeDir string) []string {
	specs := discoverCodexCompatibleSkillRootSpecs(anchor, configFile, homeDir)
	dirs := make([]string, 0, len(specs))
	for _, spec := range specs {
		if strings.TrimSpace(spec.Path) == "" {
			continue
		}
		dirs = append(dirs, spec.Path)
	}
	return dirs
}

func discoverCodexCompatibleSkillRootSpecs(anchor string, configFile string, homeDir string) []codexSkillRootSpec {
	return discoverCodexCompatibleSkillRootSpecsWithMarkers(anchor, configFile, homeDir, defaultProjectRootMarkers)
}

// discoverCodexCompatibleSkillRootSpecsWithMarkers 是发现逻辑的实现体。
// projectMarkers 对应 codex 的 `project_root_markers`：缺省只认 .git；
// 空切片表示禁用项目根探测（此时项目根=anchor，与 codex 语义一致）。
func discoverCodexCompatibleSkillRootSpecsWithMarkers(anchor string, configFile string, homeDir string, projectMarkers []string) []codexSkillRootSpec {
	seen := make(map[string]struct{})
	result := make([]codexSkillRootSpec, 0, 8)

	addDir := func(dir string, scope string) {
		dir = canonicalizeSkillTreePath(dir, true)
		if dir == "" {
			return
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return
		}
		if _, exists := seen[dir]; exists {
			return
		}
		seen[dir] = struct{}{}
		result = append(result, codexSkillRootSpec{
			Path:  dir,
			Scope: scope,
		})
	}

	// 用户级 ~/.agents/skills 固定按 user scope 处理（见下方 homeDir 块），
	// 不参与 repo 祖先扫描：否则当工作目录位于 home 之下时，用户目录会被
	// 认作 repo 根，抢到比工作目录更高的发现优先级。
	userAgentsSkillsDir := ""
	if trimmedHome := strings.TrimSpace(homeDir); trimmedHome != "" {
		userAgentsSkillsDir = canonicalizeSkillTreePath(filepath.Join(trimmedHome, ".agents", "skills"), true)
	}

	addAncestorAgentsSkillDirs := func(base string) {
		// 由近及远：越靠近工作目录的 .agents/skills 优先级越高（发现顺序即
		// 优先级，同名遮蔽诊断也据此判定）。扫描范围以项目根为界：没有
		// marker（缺省 .git）时项目根就是 anchor 自身，不再把仓库外的父目录
		// .agents/skills 当成当前项目的技能（对齐 codex host_roots.rs）。
		for _, dir := range projectAgentsSkillDirs(base, projectMarkers) {
			if userAgentsSkillsDir != "" && strings.EqualFold(filepath.Clean(dir), userAgentsSkillsDir) {
				continue
			}
			addDir(dir, CodexSkillScopeRepo)
		}
	}

	if trimmedAnchor := strings.TrimSpace(anchor); trimmedAnchor != "" {
		addDir(filepath.Join(trimmedAnchor, "skills"), CodexSkillScopeRepo)
		addAncestorAgentsSkillDirs(trimmedAnchor)
	}

	if homeDir != "" {
		// Agent Skills 标准的用户级目录：~/.agents/skills（显式发现，不依赖
		// cwd 是否位于 home 之下）。
		addDir(filepath.Join(homeDir, ".agents", "skills"), CodexSkillScopeUser)
		addDir(filepath.Join(homeDir, ".aicli", "skills"), CodexSkillScopeUser)
		addDir(filepath.Join(homeDir, ".aicli", "agents", "skills"), CodexSkillScopeUser)
		// aicli home 下的 Agent Skills 兼容布局：~/.aicli/.agents/skills。
		addDir(filepath.Join(homeDir, ".aicli", ".agents", "skills"), CodexSkillScopeUser)
	}

	if configFile = strings.TrimSpace(configFile); configFile != "" {
		addDir(filepath.Join(filepath.Dir(configFile), "skills"), CodexSkillScopeRepo)
	}

	return result
}

// codexProjectRoot 沿 anchor 向上找到第一个包含任一 marker 的目录，语义对齐
// codex host_roots.rs 的 find_project_root；未命中时返回 anchor 本身。
func codexProjectRoot(anchor string, markers []string) string {
	if strings.TrimSpace(anchor) == "" || len(markers) == 0 {
		return anchor
	}
	for dir := anchor; ; {
		for _, marker := range markers {
			if strings.TrimSpace(marker) == "" {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return anchor
		}
		dir = parent
	}
}

// projectAgentsSkillDirs 返回 base 所属项目内实际存在的 `.agents/skills`，
// 由近及远（base → project root，含两端）。项目根由 projectMarkers 界定；
// projectMarkers 为空或未命中时只包含 base 自身。
func projectAgentsSkillDirs(base string, projectMarkers []string) []string {
	base = canonicalizeSkillTreePath(strings.TrimSpace(base), true)
	if base == "" {
		return nil
	}
	if info, err := os.Stat(base); err == nil && !info.IsDir() {
		base = filepath.Dir(base)
	}
	projectRoot := codexProjectRoot(base, projectMarkers)

	seen := make(map[string]struct{})
	dirs := make([]string, 0, 4)
	for dir := base; dir != ""; {
		candidate := canonicalizeSkillTreePath(filepath.Join(dir, ".agents", "skills"), true)
		if candidate != "" {
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				if _, exists := seen[candidate]; !exists {
					seen[candidate] = struct{}{}
					dirs = append(dirs, candidate)
				}
			}
		}
		if dir == projectRoot {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return dirs
}

// ProjectAgentsSkillDirs 返回 anchor 所属项目内实际存在的 `.agents/skills`
// 目录，由近及远（anchor → project root）。项目根由默认 marker（.git）界定；
// 未命中 marker 时只含 anchor 自身。供需要自行拼装发现顺序的调用方（CLI）
// 复用，保证与 DiscoverCodexCompatibleSkillDirs 使用同一套项目边界。
func ProjectAgentsSkillDirs(anchor string) []string {
	return projectAgentsSkillDirs(anchor, defaultProjectRootMarkers)
}
