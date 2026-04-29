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
	codexSystemRootComponent = ".system"
)

type skillTreeEntry struct {
	Path  string
	Info  os.FileInfo
	Depth int
}

// walkSkillTree 以 BFS 方式遍历技能树。
//
// 规则：
// - 只遍历可见目录
// - 目录深度上限为 6
// - 每个 root 最多遍历 2000 个目录
// - system root 不跟随 symlink，其他 root 可选择跟随
// - visitor 仅接收已确认的目录/文件，不负责过滤
func walkSkillTree(root string, followSymlinks bool, visitor func(skillTreeEntry) error) error {
	root = canonicalizeSkillTreePath(root, followSymlinks)
	if root == "" {
		return nil
	}

	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		if !followSymlinks {
			return nil
		}
		rootInfo, err = os.Stat(root)
		if err != nil {
			return err
		}
	}
	if !rootInfo.IsDir() {
		return nil
	}

	type queuedDir struct {
		path  string
		depth int
	}

	queue := []queuedDir{{path: root, depth: 0}}
	visited := map[string]struct{}{root: struct{}{}}
	truncated := false

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		entries, err := os.ReadDir(current.path)
		if err != nil {
			// 保持扫描鲁棒性：单个目录读失败不阻断整棵树。
			continue
		}

		for _, entry := range entries {
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
				if current.depth+1 > maxSkillScanDepth {
					continue
				}
				if len(visited) >= maxSkillDirsPerRoot {
					truncated = true
					continue
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
						return err
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
				if current.depth+1 > maxSkillScanDepth {
					continue
				}
				if len(visited) >= maxSkillDirsPerRoot {
					truncated = true
					continue
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
						return err
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
					return err
				}
			}
		}
	}

	if truncated {
		fmt.Printf("Warning: skill scan truncated after %d directories (root: %s)\n", maxSkillDirsPerRoot, root)
	}
	return nil
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
