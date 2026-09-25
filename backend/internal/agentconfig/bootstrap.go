package agentconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

var (
	starterConfigRelativePath = filepath.Join(".aicli", aiclipaths.DefaultConfigFileName)
	userHomeDir               = os.UserHomeDir
)

// UserHomeDirForTest returns the current home-directory resolver.
// Tests can snapshot this before swapping in a deterministic resolver.
func UserHomeDirForTest() func() (string, error) {
	return userHomeDir
}

// SetUserHomeDirForTest replaces the home-directory resolver used by the
// config bootstrap helpers. It is intended for tests only.
func SetUserHomeDirForTest(resolver func() (string, error)) {
	if resolver == nil {
		userHomeDir = os.UserHomeDir
		return
	}
	userHomeDir = resolver
}

// DefaultConfigSearchPaths returns the default config lookup order for aicli,
// highest precedence first. Callers should treat the first existing file as
// authoritative in single-file mode.
//
// It is derived from ConfigLayerStack so aicli and runtime-server always agree
// on precedence; see that function for the individual layer roles. On non-main
// build profiles (for example win7compat) the profile-specific filename leads
// and the standard filename (config.yaml) follows as a compatibility fallback,
// so a win7compat binary discovers the same config file that a standard
// runtime-server / web UI writes to instead of creating an isolated profile
// config that silently diverges.
func DefaultConfigSearchPaths() []string {
	return ConfigLayerSearchPaths()
}

func defaultConfigSearchNames() []string {
	names := []string{aiclipaths.DefaultConfigFileName}
	if aiclipaths.StandardConfigFileName != aiclipaths.DefaultConfigFileName {
		names = append(names, aiclipaths.StandardConfigFileName)
	}
	return names
}

// DefaultDotEnvSearchPaths returns the default .env file lookup order for aicli.
// The order is derived from DefaultConfigSearchPaths so .env lookup stays in
// sync with config file lookup:
//  1. .aicli/.env (project-level)
//  2. $HOME/.aicli/.env (user-level)
//  3. .env (current directory)
//  4. configs/.env (legacy)
//
// Callers should treat the first existing file as authoritative.
func DefaultDotEnvSearchPaths() []string {
	return DotEnvSearchPathsForConfigPaths(DefaultConfigSearchPaths())
}

// StartupDotEnvSearchPaths returns .env candidates for early process startup.
// An explicitly selected --config/-c file takes precedence, followed by the
// directories of the supplied default config candidates.
//
// Config flags must be inspected before the command framework parses them
// because .env values are needed during command construction.
func StartupDotEnvSearchPaths(args, defaultConfigPaths []string) []string {
	configPaths := make([]string, 0, len(defaultConfigPaths)+1)
	if explicitPath := ExplicitConfigPathFromArgs(args); explicitPath != "" {
		configPaths = append(configPaths, explicitPath)
	}
	configPaths = append(configPaths, defaultConfigPaths...)
	return DotEnvSearchPathsForConfigPaths(configPaths)
}

// ExplicitConfigPathFromArgs extracts the last --config/-c value that appears
// before "--". Separate, equals, and attached shorthand forms (for example
// -cconfig.yaml) are supported. Returning the last value matches pflag/cobra
// behavior for repeated flags.
func ExplicitConfigPathFromArgs(args []string) string {
	var configPath string
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "--" {
			break
		}
		switch arg {
		case "--config", "-c":
			if index+1 < len(args) {
				index++
				configPath = strings.TrimSpace(args[index])
			}
		default:
			switch {
			case strings.HasPrefix(arg, "--config="):
				configPath = strings.TrimSpace(strings.TrimPrefix(arg, "--config="))
			case strings.HasPrefix(arg, "-c="):
				configPath = strings.TrimSpace(strings.TrimPrefix(arg, "-c="))
			case strings.HasPrefix(arg, "-c") && len(arg) > len("-c"):
				configPath = strings.TrimSpace(strings.TrimPrefix(arg, "-c"))
			}
		}
	}
	return configPath
}

// DotEnvSearchPathsForConfigPaths maps each config file candidate to a .env
// candidate in the same directory, preserving order and removing duplicates.
func DotEnvSearchPathsForConfigPaths(configPaths []string) []string {
	paths := make([]string, 0, len(configPaths))
	seen := make(map[string]struct{}, len(configPaths))
	for _, configPath := range configPaths {
		configPath = normalizeConfigPath(configPath)
		if configPath == "" {
			continue
		}
		envPath := filepath.Join(filepath.Dir(configPath), ".env")
		key := filepath.Clean(envPath)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		paths = append(paths, envPath)
	}
	return paths
}

// ResolveDotEnvPath returns the first existing .env file from paths.
// When nothing exists it returns an empty string.
func ResolveDotEnvPath(paths []string) string {
	for _, p := range paths {
		p = normalizeConfigPath(p)
		if p == "" {
			continue
		}
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// ResolveConfigPath returns the first existing config file from paths.
// When nothing exists it returns an empty string.
func ResolveConfigPath(paths []string) string {
	for _, p := range paths {
		p = normalizeConfigPath(p)
		if p == "" {
			continue
		}
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// ResolveWritableConfigPath returns the target config path that write operations
// should use. It never creates the file by itself.
func ResolveWritableConfigPath(configPath string) string {
	configPath = normalizeConfigPath(configPath)
	if configPath != "" {
		return configPath
	}
	// New configs default to the user-level path (decision D4): both the CLI and
	// the server create and look up $HOME/.aicli/<config>, and project-level
	// files only appear when explicitly created (`aicli init --project`).
	if globalPath, err := ResolveGlobalConfigPath(); err == nil && strings.TrimSpace(globalPath) != "" {
		return globalPath
	}
	return ResolveProjectConfigPath()
}

// ResolveProjectConfigPath returns the project-level config path
// (./.aicli/<config>), used by `aicli init --project` and as the last-resort
// fallback when no user home directory is available.
func ResolveProjectConfigPath() string {
	return filepath.Clean(starterConfigRelativePath)
}

// ResolveGlobalConfigPath returns the canonical user-level config path under
// the current home directory.
func ResolveGlobalConfigPath() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("user home directory is empty")
	}
	return filepath.Join(home, starterConfigRelativePath), nil
}

// EnsureStarterConfigFile returns an existing config path or creates a starter
// config when no config file can be found.
//
// Search order (highest first):
//  1. Explicit configPath (if non-empty)
//  2. ./.aicli/<config> (project-level override)
//  3. $HOME/.aicli/<config> (user-level default)
//
// When no config exists, a starter file is created at the user-level path
// when the home directory is available; otherwise it falls back to the
// project-level path.
func EnsureStarterConfigFile(configPath string) (string, bool, error) {
	configPath = normalizeConfigPath(configPath)
	if configPath != "" {
		return configPath, false, nil
	}

	// Project-level override: ./.aicli/<config> (return existing without creating)
	projectPath := filepath.Clean(starterConfigRelativePath)
	if info, err := os.Stat(projectPath); err == nil && !info.IsDir() {
		return projectPath, false, nil
	}

	// User-level default: $HOME/.aicli/<config> (create if missing)
	if globalPath, err := ResolveGlobalConfigPath(); err == nil && strings.TrimSpace(globalPath) != "" {
		return EnsureStarterConfigAtPath(globalPath)
	}
	return EnsureStarterConfigAtPath(projectPath)
}

// EnsureStarterConfigAtPath creates a starter config at the specified path when
// the file is absent. Existing files are preserved as-is.
//
// starter 创建也走同一把配置文件写锁（方案 §12 R4）：它是「检查是否存在 → 写入」，
// 与其它读-改-写并发时，后写者可能用 starter 覆盖刚写入的用户内容。配置写事务在
// 锁内改用 ensureStarterConfigAtPathLocked，避免同一路径重入自锁。
func EnsureStarterConfigAtPath(configPath string) (string, bool, error) {
	configPath = normalizeConfigPath(configPath)
	if configPath == "" {
		return "", false, fmt.Errorf("starter config path is required")
	}
	unlock := LockConfigFileWrite(configPath)
	defer unlock()
	return ensureStarterConfigAtPathLocked(configPath)
}

// ensureStarterConfigAtPathLocked 假定调用方已持有 configPath 的写锁。
func ensureStarterConfigAtPathLocked(configPath string) (string, bool, error) {
	configPath = normalizeConfigPath(configPath)
	if configPath == "" {
		return "", false, fmt.Errorf("starter config path is required")
	}
	if info, err := os.Stat(configPath); err == nil {
		if info.IsDir() {
			return "", false, fmt.Errorf("starter config path exists as a directory: %s", configPath)
		}
		return configPath, false, nil
	} else if !os.IsNotExist(err) {
		return "", false, fmt.Errorf("check starter config %s: %w", configPath, err)
	}

	if err := writeFileAtomic(configPath, []byte(defaultStarterConfigYAML())); err != nil {
		return "", false, err
	}
	return configPath, true, nil
}

func defaultStarterConfigYAML() string {
	return strings.TrimSpace(fmt.Sprintf(`
# Auto-generated starter config for aicli.
# Add providers under providers.items, then set providers.default_provider when ready.
# Add shared upstream request headers under providers.headers when required.
# Header values support session templates: {session_id} {parent_session_id}
# {user_id} {project_id} {provider} {model} {client}. Example for the
# opencode.ai gateway (https://opencode.ai/zen/go):
#   providers:
#     items:
#       opencode.ai:
#         headers:
#           x-opencode-session: "{session_id}"
#           x-opencode-project: "{project_id}"
#           x-opencode-request: "{user_id}"
#           x-opencode-client: "{client}"
aicli:
  chat:
    stream: true
# skills_runtime.enabled 默认 true（显式写出以便用户发现开关；改成 false 可关闭
# skills 加载与 chat 的 skill catalog）。
skills_runtime:
  enabled: true
  config_file: %s
providers:
  default_provider: ""
  headers: {}
  items: {}
`, aiclipaths.DefaultRuntimeConfigRelativePath)) + "\n"
}

func normalizeConfigPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" {
		if home, err := userHomeDir(); err == nil && home != "" {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") || strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		if home, err := userHomeDir(); err == nil && home != "" {
			return filepath.Join(home, strings.TrimLeft(path[2:], "/\\"))
		}
	}
	return filepath.Clean(path)
}
