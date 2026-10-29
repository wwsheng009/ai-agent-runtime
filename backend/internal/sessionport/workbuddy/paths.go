package workbuddy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type location struct {
	Home        string
	DBPath      string
	ProjectsDir string
}

// DefaultRoots returns the two WorkBuddy profile roots observed on Windows.
// The names are hints only; every instance still has to pass the SQLite +
// projects signature before it is listed as usable.
func DefaultRoots() []string {
	values := []string{
		strings.TrimSpace(os.Getenv("WORKBUDDY_HOME")),
		strings.TrimSpace(os.Getenv("WORKBUDDY_AI_HOME")),
	}
	home, err := os.UserHomeDir()
	if err == nil && strings.TrimSpace(home) != "" {
		values = append(values,
			filepath.Join(home, ".workbuddy"),
			filepath.Join(home, ".workbuddy-ai"),
		)
	}
	seen := map[string]struct{}{}
	roots := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		clean := filepath.Clean(value)
		key := strings.ToLower(clean)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		roots = append(roots, clean)
	}
	return roots
}

// DefaultHome returns the primary WorkBuddy data root.
func DefaultHome() string {
	if value := strings.TrimSpace(os.Getenv("WORKBUDDY_HOME")); value != "" {
		return filepath.Clean(value)
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".workbuddy")
}

// DefaultAIHome returns the WorkBuddy AI data root.
func DefaultAIHome() string {
	if value := strings.TrimSpace(os.Getenv("WORKBUDDY_AI_HOME")); value != "" {
		return filepath.Clean(value)
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".workbuddy-ai")
}

// InstanceRoot resolves the built-in instance aliases. Custom hash IDs cannot
// be reversed without the original --root, so callers must pass --root for them.
func InstanceRoot(instance string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(instance)) {
	case "":
		return "", nil
	case "workbuddy:default", "workbuddy":
		return DefaultHome(), nil
	case "workbuddy:ai", "workbuddy-ai:default", "workbuddy-ai":
		return DefaultAIHome(), nil
	default:
		return "", fmt.Errorf("未知 WorkBuddy 实例 %q；请用 --root 指定数据根，或使用 detect 返回的 workbuddy:default / workbuddy:ai", instance)
	}
}

func resolveLocation(root, dbPath, projectsDir string) (location, error) {
	var loc location
	var err error

	if strings.TrimSpace(dbPath) != "" {
		loc.DBPath, err = filepath.Abs(strings.TrimSpace(dbPath))
		if err != nil {
			return location{}, fmt.Errorf("解析 WorkBuddy 数据库路径失败: %w", err)
		}
		loc.Home = filepath.Dir(loc.DBPath)
	}
	if strings.TrimSpace(projectsDir) != "" {
		loc.ProjectsDir, err = filepath.Abs(strings.TrimSpace(projectsDir))
		if err != nil {
			return location{}, fmt.Errorf("解析 WorkBuddy projects 目录失败: %w", err)
		}
		if loc.Home == "" {
			loc.Home = filepath.Dir(loc.ProjectsDir)
		}
	}

	raw := strings.TrimSpace(root)
	if raw == "" {
		if loc.Home == "" {
			raw = DefaultHome()
		} else {
			raw = loc.Home
		}
	}
	if raw == "" {
		return location{}, fmt.Errorf("无法确定 WorkBuddy 数据根：请设置 WORKBUDDY_HOME 或传 --root")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return location{}, fmt.Errorf("解析 WorkBuddy 数据根失败: %w", err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil {
		return location{}, fmt.Errorf("WorkBuddy 路径不可读: %w", err)
	}

	if info.IsDir() {
		loc = mergeLocation(loc, abs)
	} else {
		if !isWorkBuddyJSONL(abs) && !strings.EqualFold(filepath.Base(abs), "workbuddy.db") {
			return location{}, fmt.Errorf("WorkBuddy 路径不是目录、workbuddy.db 或 session JSONL: %s", abs)
		}
		loc = mergeLocation(loc, abs)
	}

	if loc.DBPath == "" {
		loc.DBPath = filepath.Join(loc.Home, "workbuddy.db")
	}
	if loc.ProjectsDir == "" {
		loc.ProjectsDir = filepath.Join(loc.Home, "projects")
	}
	if !fileExists(loc.DBPath) {
		return location{}, fmt.Errorf("未找到 WorkBuddy 数据库: %s（非标准目录请同时传 --db 和 --projects-dir）", loc.DBPath)
	}
	if info, err := os.Stat(loc.ProjectsDir); err != nil || !info.IsDir() {
		return location{}, fmt.Errorf("未找到 WorkBuddy projects 目录: %s（非标准目录请显式传 --projects-dir）", loc.ProjectsDir)
	}
	loc.Home = filepath.Clean(loc.Home)
	loc.DBPath = filepath.Clean(loc.DBPath)
	loc.ProjectsDir = filepath.Clean(loc.ProjectsDir)
	return loc, nil
}

func mergeLocation(current location, path string) location {
	info, err := os.Stat(path)
	if err == nil && !info.IsDir() {
		if strings.EqualFold(filepath.Base(path), "workbuddy.db") {
			if current.DBPath == "" {
				current.DBPath = filepath.Clean(path)
			}
			if current.Home == "" {
				current.Home = filepath.Dir(path)
			}
			return current
		}
		if isWorkBuddyJSONL(path) {
			if current.Home == "" {
				current.Home = guessHomeFromSessionFile(path)
			}
			if current.ProjectsDir == "" {
				current.ProjectsDir = filepath.Dir(path)
				if strings.EqualFold(filepath.Base(current.ProjectsDir), "projects") {
					current.ProjectsDir = filepath.Dir(path)
				} else {
					current.ProjectsDir = filepath.Dir(current.ProjectsDir)
				}
			}
			return current
		}
	}
	if info != nil && info.IsDir() {
		base := strings.ToLower(filepath.Base(path))
		switch base {
		case "projects":
			if current.ProjectsDir == "" {
				current.ProjectsDir = filepath.Clean(path)
			}
			if current.Home == "" {
				current.Home = filepath.Dir(path)
			}
			return current
		case "sessions":
			if current.Home == "" {
				current.Home = filepath.Dir(path)
			}
			if current.ProjectsDir == "" {
				current.ProjectsDir = filepath.Join(current.Home, "projects")
			}
			return current
		}
		if fileExists(filepath.Join(path, "workbuddy.db")) || dirExists(filepath.Join(path, "projects")) {
			if current.Home == "" {
				current.Home = filepath.Clean(path)
			}
			if current.DBPath == "" {
				current.DBPath = filepath.Join(path, "workbuddy.db")
			}
			if current.ProjectsDir == "" {
				current.ProjectsDir = filepath.Join(path, "projects")
			}
			return current
		}
		// Accept a project directory (the parent is projects/), or walk up to a
		// home containing the expected signature. This is what makes
		// --root D:\AgentData\projects\my-project and --root ...\session.jsonl work.
		if strings.EqualFold(filepath.Base(filepath.Dir(path)), "projects") {
			current.ProjectsDir = filepath.Dir(path)
			if current.Home == "" {
				current.Home = filepath.Dir(current.ProjectsDir)
			}
			return current
		}
		parent := path
		for depth := 0; depth < 4; depth++ {
			parent = filepath.Dir(parent)
			if parent == filepath.Dir(parent) {
				break
			}
			if fileExists(filepath.Join(parent, "workbuddy.db")) && dirExists(filepath.Join(parent, "projects")) {
				if current.Home == "" {
					current.Home = filepath.Clean(parent)
				}
				if current.DBPath == "" {
					current.DBPath = filepath.Join(parent, "workbuddy.db")
				}
				if current.ProjectsDir == "" {
					current.ProjectsDir = filepath.Join(parent, "projects")
				}
				return current
			}
		}
	}
	return current
}

func guessHomeFromSessionFile(path string) string {
	project := filepath.Dir(path)
	if strings.EqualFold(filepath.Base(project), "projects") {
		return filepath.Dir(project)
	}
	if strings.EqualFold(filepath.Base(filepath.Dir(project)), "projects") {
		return filepath.Dir(filepath.Dir(project))
	}
	return filepath.Dir(project)
}

func isWorkBuddyJSONL(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return strings.HasSuffix(base, ".jsonl") && base != "file-rollback.ndjson" && !strings.HasSuffix(base, ".file-rollback.ndjson")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func instanceID(home string) string {
	clean := filepath.Clean(home)
	switch strings.ToLower(filepath.Base(clean)) {
	case ".workbuddy":
		return agentID + ":default"
	case ".workbuddy-ai":
		return agentID + ":ai"
	}
	sum := sha256.Sum256([]byte(strings.ToLower(clean)))
	return agentID + ":" + hex.EncodeToString(sum[:4])
}

type sessionFile struct {
	Path    string
	Size    int64
	ModTime int64
}

// scanProjectFiles indexes projects/*/<session-id>.jsonl. It deliberately
// ignores *.file-rollback.ndjson and tool-result sidecars.
func scanProjectFiles(projectsDir string) map[string][]sessionFile {
	result := map[string][]sessionFile{}
	_ = filepath.WalkDir(projectsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil {
			return nil
		}
		if entry.IsDir() {
			rel, relErr := filepath.Rel(projectsDir, path)
			if relErr == nil && rel != "." && len(strings.Split(filepath.ToSlash(rel), "/")) >= 2 {
				return filepath.SkipDir
			}
			return nil
		}
		if !isWorkBuddyJSONL(path) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		base := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if strings.TrimSpace(base) == "" {
			return nil
		}
		result[base] = append(result[base], sessionFile{
			Path:    path,
			Size:    info.Size(),
			ModTime: info.ModTime().UnixMilli(),
		})
		return nil
	})
	for id := range result {
		files := result[id]
		sort.Slice(files, func(i, j int) bool {
			if files[i].ModTime != files[j].ModTime {
				return files[i].ModTime > files[j].ModTime
			}
			return files[i].Path < files[j].Path
		})
		result[id] = files
	}
	return result
}

func newestProjectFile(files map[string][]sessionFile, id string) (sessionFile, bool) {
	entries := files[id]
	if len(entries) == 0 {
		return sessionFile{}, false
	}
	return entries[0], true
}

func projectSlug(cwd string) string {
	clean := filepath.ToSlash(strings.TrimSpace(cwd))
	if clean == "" || clean == "." {
		clean = "unknown"
	}
	if len(clean) >= 2 && clean[1] == ':' {
		rest := strings.TrimLeft(clean[2:], "/\\")
		clean = strings.ToLower(clean[:1]) + "-" + rest
	}
	clean = strings.ReplaceAll(clean, ":", "-")
	clean = strings.ReplaceAll(clean, "/", "-")
	clean = strings.ReplaceAll(clean, "\\", "-")
	clean = strings.Trim(clean, "- ")
	clean = strings.ReplaceAll(clean, "..", "_")
	if clean == "" {
		clean = "unknown"
	}
	// Keep the resulting directory comfortably below Windows path limits. The
	// hash suffix avoids collisions when two long paths share a prefix.
	if len([]rune(clean)) > 180 {
		sum := sha256.Sum256([]byte(cwd))
		runes := []rune(clean)
		clean = string(runes[:160]) + "-" + hex.EncodeToString(sum[:6])
	}
	return clean
}

func safeSessionID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("WorkBuddy session ID 为空")
	}
	if strings.ContainsAny(value, `\/:*?"<>|`) || value == "." || value == ".." {
		return "", fmt.Errorf("WorkBuddy session ID 含有非法路径字符: %q", value)
	}
	return value, nil
}
