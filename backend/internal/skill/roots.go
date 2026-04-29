package skill

import (
	"os"
	"path/filepath"
	"strings"
)

// DiscoverCodexCompatibleSkillDirs 返回一组按 Codex 兼容规则补充的 skills 目录。
//
// anchor 通常是当前工作区或 profile root；configFile 用于推导系统/管理层的
// `<config_folder>/skills` 目录。结果只包含实际存在的目录，并保持稳定去重。
//
// 为了贴近当前项目的 Codex 兼容语义，这里还会显式补充：
// - `~/.aicli/skills`
// - `~/.aicli/agents/skills`
func DiscoverCodexCompatibleSkillDirs(anchor string, configFile string) []string {
	homeDir := strings.TrimSpace(os.Getenv("USERPROFILE"))
	if homeDir == "" {
		homeDir = strings.TrimSpace(os.Getenv("HOME"))
	}
	if homeDir == "" {
		if resolvedHomeDir, err := os.UserHomeDir(); err == nil {
			homeDir = strings.TrimSpace(resolvedHomeDir)
		}
	}

	return discoverCodexCompatibleSkillDirs(anchor, configFile, homeDir)
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

	addAncestorAgentsSkillDirs := func(base string) {
		base = canonicalizeSkillTreePath(base, true)
		if base == "" {
			return
		}
		info, err := os.Stat(base)
		if err == nil && !info.IsDir() {
			base = filepath.Dir(base)
		}

		ancestors := make([]string, 0, 8)
		for dir := base; dir != ""; {
			ancestors = append(ancestors, dir)
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}

		for i := len(ancestors) - 1; i >= 0; i-- {
			addDir(filepath.Join(ancestors[i], ".agents", "skills"), CodexSkillScopeRepo)
		}
	}

	if trimmedAnchor := strings.TrimSpace(anchor); trimmedAnchor != "" {
		addDir(filepath.Join(trimmedAnchor, "skills"), CodexSkillScopeRepo)
		addAncestorAgentsSkillDirs(trimmedAnchor)
	}

	if homeDir != "" {
		addDir(filepath.Join(homeDir, ".aicli", "skills"), CodexSkillScopeUser)
		addDir(filepath.Join(homeDir, ".aicli", "agents", "skills"), CodexSkillScopeUser)
	}

	if configFile = strings.TrimSpace(configFile); configFile != "" {
		addDir(filepath.Join(filepath.Dir(configFile), "skills"), CodexSkillScopeRepo)
	}

	return result
}
