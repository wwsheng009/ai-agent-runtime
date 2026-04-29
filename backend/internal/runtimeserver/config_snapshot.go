package runtimeserver

import (
	"os"
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// AgentConfigSnapshotInfo describes the startup config file.
type AgentConfigSnapshotInfo struct {
	BasePath       string
	SnapshotPath   string
	ActivePath     string
	SnapshotExists bool
}

// ResolveAgentConfigSnapshotInfo returns the base config path as the only active runtime config.
func ResolveAgentConfigSnapshotInfo(configPath string) AgentConfigSnapshotInfo {
	configPath = strings.TrimSpace(configPath)
	info := AgentConfigSnapshotInfo{
		BasePath:   configPath,
		ActivePath: configPath,
	}
	return info
}

// LoadRuntimeAgentConfig loads the runtime config from the selected base config file only.
func LoadRuntimeAgentConfig(configPath string) (*agentconfig.Config, AgentConfigSnapshotInfo, error) {
	info := ResolveAgentConfigSnapshotInfo(configPath)
	format := detectConfigDocumentFormat(info.ActivePath)
	effectiveDocument, err := loadEffectiveConfigDocument(
		info.BasePath,
		info.SnapshotPath,
		format,
	)
	if err != nil {
		return nil, info, err
	}

	cfg, err := decodeConfigDocumentAgentConfig(effectiveDocument.Raw, format)
	if err != nil {
		return nil, info, err
	}
	agentconfig.SetGlobalConfig(cfg)
	return cfg, info, nil
}

func fileExists(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
