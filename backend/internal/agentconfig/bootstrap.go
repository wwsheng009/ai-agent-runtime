package agentconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const starterConfigRelativePath = ".aicli/config.yaml"

var userHomeDir = os.UserHomeDir

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

// DefaultConfigSearchPaths returns the default config lookup order for aicli.
// Callers should treat the first existing file as authoritative.
func DefaultConfigSearchPaths() []string {
	paths := make([]string, 0, 4)
	if home, err := userHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".aicli", "config.yaml"))
	}
	paths = append(paths,
		filepath.Join(".aicli", "config.yaml"),
		"aicli.yaml",
		filepath.Join("configs", "config.yaml"),
	)
	return paths
}

// DefaultDotEnvSearchPaths returns the default .env file lookup order for aicli.
// The order is derived from DefaultConfigSearchPaths so .env lookup stays in
// sync with config file lookup:
//  1. $HOME/.aicli/.env (user-level)
//  2. .aicli/.env (project-level)
//  3. .env (current directory)
//  4. configs/.env (legacy)
//
// Callers should treat the first existing file as authoritative.
func DefaultDotEnvSearchPaths() []string {
	return DotEnvSearchPathsForConfigPaths(DefaultConfigSearchPaths())
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
// The helper intentionally keeps the generated file minimal:
// - provider-related sections stay empty so users can fill them in later
// - non-provider settings continue to rely on code defaults
func EnsureStarterConfigFile(configPath string) (string, bool, error) {
	configPath = normalizeConfigPath(configPath)
	if configPath != "" {
		return configPath, false, nil
	}

	if globalPath, err := ResolveGlobalConfigPath(); err == nil && strings.TrimSpace(globalPath) != "" {
		return EnsureStarterConfigAtPath(globalPath)
	}
	return EnsureStarterConfigAtPath(filepath.Clean(starterConfigRelativePath))
}

// EnsureStarterConfigAtPath creates a starter config at the specified path when
// the file is absent. Existing files are preserved as-is.
func EnsureStarterConfigAtPath(configPath string) (string, bool, error) {
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
	return strings.TrimSpace(`
# Auto-generated starter config for aicli.
# Add providers under providers.items, then set providers.default_provider when ready.
aicli:
  chat:
    stream: true
providers:
  default_provider: ""
  items: {}
`) + "\n"
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
