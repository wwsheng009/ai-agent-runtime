package commands

import (
	"fmt"
	"os"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// resolveAICLIConfigWriteTarget resolves the config file path that write
// operations should use.
//
// The returned boolean indicates whether the caller should create a starter
// file before writing when the target file does not yet exist.
func resolveAICLIConfigWriteTarget(cfg *config.Config, explicitPath string) (string, bool, error) {
	path := strings.TrimSpace(explicitPath)
	if path == "" && cfg != nil {
		path = strings.TrimSpace(cfg.ConfigFilePath)
	}
	if path != "" {
		path = config.ResolveWritableConfigPath(path)
		needsStarter, err := pathNeedsStarterFile(path)
		if err != nil {
			return "", false, err
		}
		return path, needsStarter, nil
	}

	if existing := config.ResolveConfigPath(config.DefaultConfigSearchPaths()); existing != "" {
		return existing, false, nil
	}

	return config.ResolveWritableConfigPath(""), true, nil
}

func pathNeedsStarterFile(path string) (bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return false, nil
	}
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return false, fmt.Errorf("config path exists as a directory: %s", path)
		}
		return false, nil
	} else if os.IsNotExist(err) {
		return true, nil
	} else {
		return false, err
	}
}

func ensureWritableAICLIConfigPath(cfg *config.Config, explicitPath string) (string, error) {
	path, needsStarter, err := resolveAICLIConfigWriteTarget(cfg, explicitPath)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("config path is required")
	}
	if needsStarter {
		if _, _, err := config.EnsureStarterConfigAtPath(path); err != nil {
			return "", err
		}
	}
	if cfg != nil {
		cfg.ConfigFilePath = path
	}
	return path, nil
}
