package runtimeserver

import (
	"fmt"
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

// LoadRuntimeAgentConfig loads the runtime config that the server actually runs on.
//
// 与 config document 服务同源：合并模式开启（默认）且存在多层时，使用分层合并
// 结果；否则退回单文件（保持 --config / merge off 的既有语义）。
// 否则会出现「配置页按分层合并展示 N 个 provider，运行时只注册单文件层的 1 个」
// 的分叉。
func LoadRuntimeAgentConfig(configPath string) (*agentconfig.Config, AgentConfigSnapshotInfo, error) {
	info := ResolveAgentConfigSnapshotInfo(configPath)
	format := detectConfigDocumentFormat(info.ActivePath)
	raw, err := loadRuntimeAgentConfigRaw(info, format)
	if err != nil {
		return nil, info, err
	}

	// Merge system presets (built-in + system directory + ~/.aicli/presets.yaml)
	// below the runtime config document. MergeWithPresets returns nil when no
	// preset layer is deployed, keeping the plain document unchanged.
	if mergedRaw, err := agentconfig.MergeWithPresets(raw); err != nil {
		return nil, info, fmt.Errorf("merge system presets: %w", err)
	} else if mergedRaw != nil {
		raw = mergedRaw
	}

	cfg, err := decodeConfigDocumentAgentConfig(raw, format)
	if err != nil {
		return nil, info, err
	}
	agentconfig.SetGlobalConfig(cfg)
	return cfg, info, nil
}

// loadRuntimeAgentConfigRaw 返回运行时生效的配置原文：
//   - 合并模式开启且磁盘上确实存在多层 → 分层合并结果（与 config document 服务
//     的展示完全一致，运行时才能注册到同一批 provider）；
//   - 其余情况 → 单文件读取（byte-identical，保持既有行为）。
func loadRuntimeAgentConfigRaw(
	info AgentConfigSnapshotInfo,
	format string,
) ([]byte, error) {
	if agentconfig.MergeModeFromEnv() == agentconfig.MergeModeOn {
		merged, err := agentconfig.LoadMergedConfigDocument()
		if err != nil {
			return nil, err
		}
		if merged != nil && merged.MultiLayer() {
			return merged.MergedYAML, nil
		}
	}
	effectiveDocument, err := loadEffectiveConfigDocument(
		info.BasePath,
		info.SnapshotPath,
		format,
	)
	if err != nil {
		return nil, err
	}
	return effectiveDocument.Raw, nil
}

func fileExists(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
