package aiclipaths

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultSessionsDir returns the default persisted chat session directory.
func DefaultSessionsDir() string {
	return defaultAICLIDir("sessions")
}

// DefaultChatLogsDir returns the default persisted chat log directory.
func DefaultChatLogsDir() string {
	return defaultAICLIDir("chat-logs")
}

// DefaultLogsDir returns the default global log directory (~/.aicli/logs).
func DefaultLogsDir() string {
	return defaultAICLIDir("logs")
}

// DefaultMeshDir returns the mesh root directory (~/.aicli/mesh) that holds
// node records, session bindings, leases and the journal.
//
// Note: this is only the user-home layout. internal/mesh.ResolvePaths is the
// authoritative resolver — it also honours AICLI_MESH_DIR / AICLI_HOME and is
// fail-closed when no home resolves, whereas defaultAICLIDir falls back to a
// CWD-relative "./.aicli/<name>" path. Callers that need the real mesh root
// must use internal/mesh.ResolvePaths, not this helper.
func DefaultMeshDir() string {
	return defaultAICLIDir("mesh")
}

// DefaultAICLIDir returns the user-level ~/.aicli directory (falling back to
// ./.aicli when no home resolves). Callers that need an environment override
// (for example AICLI_HOME, which internal/mesh honours for the mesh root) must
// apply it themselves before calling this helper.
func DefaultAICLIDir() string {
	return defaultAICLIDir("")
}

// ResolveConfigFilePath resolves a config file path with the following priority
// (highest first):
//  1. explicitPath, when it is a real override (see below)
//  2. ./.aicli/<filename> (project-level override in CWD)
//  3. $HOME/.aicli/<filename> (user-level default)
//  4. CWD upward search for <filename> in searchPaths
//  5. Executable directory upward search
//  6. explicitPath (when no candidate exists) or portableDefault
//
// A value that equals the bare filename, the portable default, or one of the
// searchPaths counts as a convention path, not as an explicit override: values
// copied from older templates (for example backend/configs/<filename>) must not
// shadow the ./.aicli/ and ~/.aicli/ lookups.
//
// A leading "~" in explicitPath is expanded to the current user's home
// directory; other explicit values are returned exactly as configured.
func ResolveConfigFilePath(filename, explicitPath, portableDefault string, searchPaths []string) string {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return strings.TrimSpace(explicitPath)
	}

	explicitPath = expandExplicitConfigPath(explicitPath)
	if explicitPath != "" && !isConventionConfigPath(explicitPath, filename, portableDefault, searchPaths) {
		return explicitPath
	}

	// Project-level override: ./.aicli/<filename> in CWD
	if cwd, err := os.Getwd(); err == nil {
		projectConfig := filepath.Join(cwd, ".aicli", filename)
		if info, err := os.Stat(projectConfig); err == nil && !info.IsDir() {
			return filepath.Clean(projectConfig)
		}
	}

	// User-level default: $HOME/.aicli/<filename>
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		userConfig := filepath.Join(home, ".aicli", filename)
		if info, err := os.Stat(userConfig); err == nil && !info.IsDir() {
			return userConfig
		}
	}

	// CWD upward search
	if cwd, err := os.Getwd(); err == nil {
		if resolved := resolveDefaultConfigPathFromBase(cwd, filename, searchPaths); resolved != "" {
			return resolved
		}
	}

	// Executable directory upward search
	if executable, err := os.Executable(); err == nil {
		if resolved := resolveDefaultConfigPathFromBase(filepath.Dir(executable), filename, searchPaths); resolved != "" {
			return resolved
		}
	}

	if explicitPath != "" {
		return explicitPath
	}
	return portableDefault
}

// expandExplicitConfigPath expands a leading "~" while leaving every other
// configured value byte-for-byte intact (callers may compare or display it).
func expandExplicitConfigPath(explicitPath string) string {
	trimmed := strings.TrimSpace(explicitPath)
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, "~\\") {
		return ExpandUserPath(trimmed)
	}
	return trimmed
}

// isConventionConfigPath reports whether candidate is one of the well-known
// default locations rather than a deliberate override.
func isConventionConfigPath(candidate, filename, portableDefault string, searchPaths []string) bool {
	cleaned := filepath.Clean(strings.TrimSpace(candidate))
	if cleaned == "." {
		return true
	}
	if cleaned == filepath.Clean(filename) || cleaned == filepath.Clean(portableDefault) {
		return true
	}
	for _, relativePath := range searchPaths {
		if cleaned == filepath.Clean(strings.TrimSpace(relativePath)) {
			return true
		}
	}
	return false
}

// resolveDefaultConfigPathFromBase searches upward from baseDir for filename
// in the given searchPaths (relative subpaths).
func resolveDefaultConfigPathFromBase(baseDir, filename string, searchPaths []string) string {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return ""
	}
	if absolute, err := filepath.Abs(baseDir); err == nil {
		baseDir = absolute
	}
	baseDir = filepath.Clean(baseDir)

	paths := append([]string{filepath.Join(".aicli", filename)}, searchPaths...)
	for dir := baseDir; ; {
		for _, relativePath := range paths {
			candidate := filepath.Join(dir, relativePath)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return filepath.Clean(candidate)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// DefaultRuntimeConfigSearchPaths returns the legacy repository/bundle layouts
// that older templates used to name runtime.yaml:
//
//	configs/runtime.yaml
//	backend/configs/runtime.yaml
//
// They are listed here so convention values can still be *recognized* (see
// IsRuntimeConfigConventionPath): a config.yaml carrying one of these values
// must not shadow the .aicli layers, and must not be reported as a missing
// user-configured file. They are never searched or loaded — the development
// directory is not a configuration source for aicli processes.
func DefaultRuntimeConfigSearchPaths() []string {
	return []string{
		filepath.FromSlash(DefaultRuntimeConfigRelativePath),
		filepath.Join("backend", "configs", DefaultRuntimeConfigFileName),
	}
}

// ResolveRuntimeConfigBootstrapPath locates the runtime.yaml aicli loads at
// startup (chat runtime tools, skills runtime, runtime-server), highest
// priority first:
//
//  1. an explicit, non-convention override — returned even when the file does
//     not exist so callers can report the misconfiguration;
//  2. ./.aicli/runtime.yaml — project layer of the current workspace;
//  3. ~/.aicli/runtime.yaml — user layer.
//
// The repository layouts (configs/runtime.yaml, backend/configs/runtime.yaml)
// are deliberately NOT searched: they are development-directory artifacts, and
// auto-loading them let a dev checkout silently drive user processes. Values
// naming those layouts are still treated as convention values (see
// IsRuntimeConfigConventionPath) so they fall through to the .aicli layers.
//
// Returns "" when no layer exists on disk; callers then use the built-in
// defaults without reporting a missing optional config.
func ResolveRuntimeConfigBootstrapPath(configPath string) string {
	configPath = expandExplicitConfigPath(configPath)
	if configPath != "" && !IsRuntimeConfigConventionPath(configPath) {
		return configPath
	}

	// Project-level layer: ./.aicli/runtime.yaml in CWD
	if cwd, err := os.Getwd(); err == nil {
		projectConfig := filepath.Join(cwd, ".aicli", DefaultRuntimeConfigFileName)
		if info, err := os.Stat(projectConfig); err == nil && !info.IsDir() {
			return filepath.Clean(projectConfig)
		}
	}

	// User-level layer: $HOME/.aicli/runtime.yaml
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		userConfig := filepath.Join(home, ".aicli", DefaultRuntimeConfigFileName)
		if info, err := os.Stat(userConfig); err == nil && !info.IsDir() {
			return filepath.Clean(userConfig)
		}
	}

	return ""
}

// IsRuntimeConfigConventionPath reports whether a configured
// skills_runtime.config_file value is one of the conventional runtime.yaml
// locations (bare filename, configs/runtime.yaml, backend/configs/runtime.yaml,
// or ".") instead of a deliberate user override.
//
// Convention values are source-tree layouts: they are only meaningful when a
// matching file exists on disk (typically a development checkout). Callers
// must not treat a missing convention value as a missing required config, and
// must not report it as a user-configured path that failed to load.
func IsRuntimeConfigConventionPath(configPath string) bool {
	return isConventionConfigPath(
		configPath,
		DefaultRuntimeConfigFileName,
		DefaultRuntimeConfigRelativePath,
		DefaultRuntimeConfigSearchPaths(),
	)
}

// DefaultMCPConfigFileName is the conventional global MCP config filename.
const DefaultMCPConfigFileName = "mcp.yaml"

// DefaultMCPConfigRelativePath uses forward slashes so generated YAML stays
// portable across platforms.
const DefaultMCPConfigRelativePath = "configs/" + DefaultMCPConfigFileName

// ResolveMCPConfigPath resolves the effective global MCP config path:
// ./.aicli/mcp.yaml > ~/.aicli/mcp.yaml > explicit override > upward search
// (configs/mcp.yaml) > executable directory > configs/mcp.yaml.
//
// An empty (unset) explicit path is discovery mode: the documented workspace >
// user > upward-search order runs on its own, so a project ./.aicli/mcp.yaml is
// honoured without also writing aicli.mcp.config_file. When no candidate exists
// on disk the path stays "" so callers keep their "MCP not configured" semantics
// instead of silently loading a directory-wide default.
func ResolveMCPConfigPath(explicitPath string) string {
	return ResolveMCPConfigPathDetailed(explicitPath).Path
}

// MCPConfigCandidate describes one candidate location and its current state.
// It exists for observability output (startup logs, /api/runtime/mcps) so users
// can tell which mcp.yaml was picked and why a different file won.
type MCPConfigCandidate struct {
	Path   string
	Source string
	Exists bool
}

// MCPConfigResolution is the detailed result of MCP config path resolution:
// the effective path, the priority layer that produced it, and every candidate
// that was considered (in priority order).
type MCPConfigResolution struct {
	Path       string
	Source     string
	Candidates []MCPConfigCandidate
}

// ResolveMCPConfigPathDetailed mirrors ResolveMCPConfigPath and additionally
// reports the winning layer (explicit/project/user/upward/executable/default)
// plus the candidate list with per-candidate existence checks.
func ResolveMCPConfigPathDetailed(explicitPath string) MCPConfigResolution {
	resolution := MCPConfigResolution{}
	filename := DefaultMCPConfigFileName
	portableDefault := DefaultMCPConfigRelativePath
	searchPaths := []string{DefaultMCPConfigRelativePath}

	explicit := expandExplicitConfigPath(explicitPath)
	// Unset aicli.mcp.config_file is discovery mode (see ResolveMCPConfigPath):
	// the documented priority chain still runs, and the resolution stays empty
	// when no candidate exists instead of inventing the portable default path.
	discoverOnly := explicit == ""
	realOverride := explicit != "" && !isConventionConfigPath(explicit, filename, portableDefault, searchPaths)

	seen := map[string]bool{}
	addCandidate := func(path, source string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		cleaned := filepath.Clean(path)
		if seen[cleaned] {
			return
		}
		seen[cleaned] = true
		exists := false
		if info, err := os.Stat(cleaned); err == nil && !info.IsDir() {
			exists = true
		}
		resolution.Candidates = append(resolution.Candidates, MCPConfigCandidate{Path: cleaned, Source: source, Exists: exists})
	}

	if !discoverOnly {
		addCandidate(explicit, "explicit")
	}
	// local 层（个人为当前项目追加的私有配置）优先级最高：CommandCode 语义为
	// local > project > user，且该文件位于用户目录（不随仓库分发、不受 foldertrust
	// 项目门限制）。只有写入过 --scope local 时才会存在。
	if cwd, err := os.Getwd(); err == nil {
		if localPath, err := LocalMCPConfigPath(cwd); err == nil {
			addCandidate(localPath, "local")
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		addCandidate(filepath.Join(cwd, ".aicli", filename), "project")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		addCandidate(filepath.Join(home, ".aicli", filename), "user")
	}
	if cwd, err := os.Getwd(); err == nil {
		addCandidate(resolveDefaultConfigPathFromBase(cwd, filename, searchPaths), "upward")
	}
	if executable, err := os.Executable(); err == nil {
		addCandidate(resolveDefaultConfigPathFromBase(filepath.Dir(executable), filename, searchPaths), "executable")
	}
	addCandidate(portableDefault, "default")

	switch {
	case realOverride:
		resolution.Path, resolution.Source = explicit, "explicit"
	default:
		if path, source := firstExistingMCPConfigCandidate(filename, searchPaths); path != "" {
			resolution.Path, resolution.Source = path, source
		} else if !discoverOnly {
			resolution.Path, resolution.Source = explicit, "explicit"
		}
	}
	return resolution
}

// firstExistingMCPConfigCandidate reports the first existing config among the
// non-explicit priority layers, mirroring ResolveConfigFilePath's ordering.
func firstExistingMCPConfigCandidate(filename string, searchPaths []string) (string, string) {
	if cwd, err := os.Getwd(); err == nil {
		if localPath, err := LocalMCPConfigPath(cwd); err == nil {
			if path := firstExistingConfigFile(localPath); path != "" {
				return path, "local"
			}
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		if path := firstExistingConfigFile(filepath.Join(cwd, ".aicli", filename)); path != "" {
			return path, "project"
		}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		if path := firstExistingConfigFile(filepath.Join(home, ".aicli", filename)); path != "" {
			return path, "user"
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		if path := resolveDefaultConfigPathFromBase(cwd, filename, searchPaths); path != "" {
			return path, "upward"
		}
	}
	if executable, err := os.Executable(); err == nil {
		if path := resolveDefaultConfigPathFromBase(filepath.Dir(executable), filename, searchPaths); path != "" {
			return path, "executable"
		}
	}
	return "", ""
}

func firstExistingConfigFile(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return filepath.Clean(path)
	}
	return ""
}

// ProjectScopeSlug 把项目根路径规范化为可安全用作目录名的短标识：
// 仅保留 [a-z0-9_-]，其余字符折叠为单个 "-"，并去掉首尾 "-"。
//
// 与 planstore 的内部 slug 规则一致（那里为包内私有）；这里单独实现，
// 避免 aiclipaths 依赖具体业务包。全大写/中文等无法成词时返回 ""，
// 由调用方追加稳定的哈希后缀兜底（见 LocalMCPConfigPath）。
func ProjectScopeSlug(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = strings.ReplaceAll(path, "\\", "/")

	var builder strings.Builder
	pendingDash := false
	for _, r := range strings.ToLower(path) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			builder.WriteRune(r)
			pendingDash = false
			continue
		}
		if builder.Len() > 0 && !pendingDash {
			builder.WriteByte('-')
			pendingDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

// LocalMCPConfigPath 返回项目私有（local scope）的 MCP 配置路径：
// ~/.aicli/projects/<slug>/mcp.yaml。
//
// 该层既不进入版本库，也不受项目级 foldertrust 门限制——适合个人在本机
// 为某个项目追加私有 server。slug 为空（路径规范化后无可用字符）时用
// 路径哈希兜底，保证不同项目不会共用一个目录。
func LocalMCPConfigPath(projectRoot string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("解析用户主目录失败: %w", err)
	}
	slug := ProjectScopeSlug(projectRoot)
	if slug == "" {
		sum := sha256.Sum256([]byte(strings.TrimSpace(projectRoot)))
		slug = "project-" + hex.EncodeToString(sum[:8])
	}
	return filepath.Join(home, ".aicli", "projects", slug, DefaultMCPConfigFileName), nil
}

// DatePartition returns year/month/day path segments for t in local time.
// Zero times fall back to time.Now() so callers always get a usable partition.
func DatePartition(t time.Time) (year, month, day string) {
	if t.IsZero() {
		t = time.Now()
	}
	t = t.Local()
	return t.Format("2006"), t.Format("01"), t.Format("02")
}

// JoinDatePartition joins root/YYYY/MM/DD and optional trailing path elements.
// This mirrors Codex's sessions/YYYY/MM/DD layout for easier filesystem browsing.
func JoinDatePartition(root string, t time.Time, elems ...string) string {
	year, month, day := DatePartition(t)
	parts := make([]string, 0, 4+len(elems))
	parts = append(parts, root, year, month, day)
	parts = append(parts, elems...)
	return filepath.Join(parts...)
}

// ParseTimestampedSessionIDTime extracts a local timestamp from common session IDs:
//   - session id: session_20060102150405_<suffix> (runtime + chat log)
//   - legacy chat log: 20060102_150405.000_<suffix>
func ParseTimestampedSessionIDTime(sessionID string) (time.Time, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return time.Time{}, false
	}

	if strings.HasPrefix(sessionID, "session_") {
		rest := strings.TrimPrefix(sessionID, "session_")
		stamp, _, _ := strings.Cut(rest, "_")
		if t, err := time.ParseInLocation("20060102150405", stamp, time.Local); err == nil {
			return t, true
		}
		return time.Time{}, false
	}

	// chat log session ids: 20060102_150405.000_<suffix>
	parts := strings.SplitN(sessionID, "_", 3)
	if len(parts) < 2 {
		return time.Time{}, false
	}
	stamp := parts[0] + "_" + parts[1]
	if t, err := time.ParseInLocation("20060102_150405.000", stamp, time.Local); err == nil {
		return t, true
	}
	if t, err := time.ParseInLocation("20060102_150405", parts[0]+"_"+strings.Split(parts[1], ".")[0], time.Local); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// ExpandUserPath expands a leading "~" to the current user's home directory.
// It intentionally only supports the current-user forms "~", "~/..." and "~\...".
func ExpandUserPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, "~\\") {
		return filepath.Clean(path)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return filepath.Clean(path)
	}
	if path == "~" {
		return filepath.Clean(homeDir)
	}
	return filepath.Join(homeDir, strings.TrimLeft(path[2:], "/\\"))
}

func defaultAICLIDir(name string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return filepath.Join(".", ".aicli", name)
	}
	return filepath.Join(homeDir, ".aicli", name)
}
