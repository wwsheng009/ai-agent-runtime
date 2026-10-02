package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxSkillScanDepth        = 6
	maxSkillDirsPerRoot      = 2000
	maxSkillEntriesPerRoot   = 20000
	codexSystemRootComponent = ".system"
)

// skillWalkLimits 对应 codex walk 的预算（max_depth / max_directories /
// max_entries）。条目预算与 codex MAX_SKILLS_ENTRIES_PER_ROOT=20000 对齐。
type skillWalkLimits struct {
	MaxDepth   int
	MaxDirs    int
	MaxEntries int
}

var defaultSkillWalkLimits = skillWalkLimits{
	MaxDepth:   maxSkillScanDepth,
	MaxDirs:    maxSkillDirsPerRoot,
	MaxEntries: maxSkillEntriesPerRoot,
}

type skillTreeEntry struct {
	Path  string
	Info  os.FileInfo
	Depth int
}

// walkSkillTree 以 BFS 方式遍历技能树。
//
// 规则：
//   - 只遍历可见目录
//   - 目录深度上限为 6，每个 root 最多 2000 个目录、20000 个条目
//     （与 codex MAX_SCAN_DEPTH / MAX_SKILLS_DIRS_PER_ROOT /
//     MAX_SKILLS_ENTRIES_PER_ROOT 对齐）
//   - system root 不跟随 symlink，其他 root 可选择跟随
//   - visitor 仅接收已确认的目录/文件，不负责过滤
//
// 触碰预算时通过返回的 warnings 上报（不再直接打印到 stdout）。
func walkSkillTree(root string, followSymlinks bool, visitor func(skillTreeEntry) error) ([]string, error) {
	return walkSkillTreeWithLimits(root, followSymlinks, defaultSkillWalkLimits, visitor)
}

func walkSkillTreeWithLimits(root string, followSymlinks bool, limits skillWalkLimits, visitor func(skillTreeEntry) error) ([]string, error) {
	root = canonicalizeSkillTreePath(root, followSymlinks)
	if root == "" {
		return nil, nil
	}

	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		if !followSymlinks {
			return nil, nil
		}
		rootInfo, err = os.Stat(root)
		if err != nil {
			return nil, err
		}
	}
	if !rootInfo.IsDir() {
		return nil, nil
	}

	type queuedDir struct {
		path  string
		depth int
	}

	queue := []queuedDir{{path: root, depth: 0}}
	visited := map[string]struct{}{root: struct{}{}}
	truncated := false
	entriesSeen := 0

walkLoop:
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		entries, err := os.ReadDir(current.path)
		if err != nil {
			// 保持扫描鲁棒性：单个目录读失败不阻断整棵树。
			continue
		}

		for _, entry := range entries {
			if limits.MaxEntries > 0 && entriesSeen >= limits.MaxEntries {
				truncated = true
				break walkLoop
			}
			entriesSeen++

			name := entry.Name()
			if isHiddenFileOrDir(name) {
				continue
			}

			path := filepath.Join(current.path, name)
			info, err := os.Lstat(path)
			if err != nil {
				continue
			}

			if info.Mode()&os.ModeSymlink != 0 {
				if !followSymlinks {
					continue
				}
				targetInfo, err := os.Stat(path)
				if err != nil || !targetInfo.IsDir() {
					continue
				}

				resolved := canonicalizeSkillTreePath(path, true)
				if resolved == "" {
					continue
				}
				if limits.MaxDepth > 0 && current.depth+1 > limits.MaxDepth {
					continue
				}
				if limits.MaxDirs > 0 && len(visited) >= limits.MaxDirs {
					truncated = true
					break walkLoop
				}
				if _, exists := visited[resolved]; exists {
					continue
				}
				visited[resolved] = struct{}{}
				if visitor != nil {
					if err := visitor(skillTreeEntry{
						Path:  resolved,
						Info:  targetInfo,
						Depth: current.depth + 1,
					}); err != nil {
						return nil, err
					}
				}
				queue = append(queue, queuedDir{path: resolved, depth: current.depth + 1})
				continue
			}

			if info.IsDir() {
				resolved := canonicalizeSkillTreePath(path, followSymlinks)
				if resolved == "" {
					continue
				}
				if limits.MaxDepth > 0 && current.depth+1 > limits.MaxDepth {
					continue
				}
				if limits.MaxDirs > 0 && len(visited) >= limits.MaxDirs {
					truncated = true
					break walkLoop
				}
				if _, exists := visited[resolved]; exists {
					continue
				}
				visited[resolved] = struct{}{}
				if visitor != nil {
					if err := visitor(skillTreeEntry{
						Path:  resolved,
						Info:  info,
						Depth: current.depth + 1,
					}); err != nil {
						return nil, err
					}
				}
				queue = append(queue, queuedDir{path: resolved, depth: current.depth + 1})
				continue
			}

			if visitor != nil {
				if err := visitor(skillTreeEntry{
					Path:  path,
					Info:  info,
					Depth: current.depth + 1,
				}); err != nil {
					return nil, err
				}
			}
		}
	}

	var warnings []string
	if truncated {
		warnings = append(warnings, fmt.Sprintf("skills scan reached its traversal limit (root: %s)", root))
	}
	return warnings, nil
}

func canonicalizeSkillTreePath(path string, followSymlinks bool) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return ""
	}
	if !followSymlinks {
		return path
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	resolved = filepath.Clean(resolved)
	if resolved == "" || resolved == "." {
		return path
	}
	return resolved
}

func isCodexSystemSkillRoot(path string) bool {
	path = filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	if path == "" || path == "." {
		return false
	}
	return strings.EqualFold(filepath.Base(path), codexSystemRootComponent) ||
		strings.HasSuffix(strings.ToLower(path), "/skills/"+codexSystemRootComponent)
}
