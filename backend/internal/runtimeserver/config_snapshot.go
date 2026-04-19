package runtimeserver

import (
	"os"
	"path/filepath"
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// AgentConfigSnapshotInfo describes the startup config file and its runtime snapshot.
type AgentConfigSnapshotInfo struct {
	BasePath       string
	SnapshotPath   string
	ActivePath     string
	SnapshotExists bool
}

// ResolveAgentConfigSnapshotInfo resolves the runtime snapshot file location for a startup config path.
func ResolveAgentConfigSnapshotInfo(configPath string) AgentConfigSnapshotInfo {
	configPath = strings.TrimSpace(configPath)
	info := AgentConfigSnapshotInfo{
		BasePath: configPath,
	}
	if configPath == "" {
		return info
	}

	ext := filepath.Ext(configPath)
	dir := filepath.Dir(configPath)
	base := strings.TrimSuffix(filepath.Base(configPath), ext)
	if ext == "" {
		ext = ".yaml"
	}
	info.SnapshotPath = filepath.Join(dir, base+".runtime.snapshot"+ext)
	info.ActivePath = info.BasePath
	if fileExists(info.SnapshotPath) {
		info.ActivePath = info.SnapshotPath
		info.SnapshotExists = true
	}
	return info
}

// LoadRuntimeAgentConfig loads the effective runtime config, preferring the runtime snapshot when present.
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
	return cfg, info, err
}

func fileExists(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
