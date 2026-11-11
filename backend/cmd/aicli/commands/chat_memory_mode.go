package commands

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// setChatMemoryMode persists memory.mode through the same layered-config
// writer used by the config UI. Explicit single-file session configs are
// rewritten through RuntimeManager; normal .aicli/runtime.yaml stacks receive
// a leaf update so unrelated keys and comments in other layers survive.
func setChatMemoryMode(session *ChatSession, mode string) (string, int, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "off", "shadow", "on":
	default:
		return "", 0, fmt.Errorf("memory mode must be off, shadow, or on")
	}
	configPath := ""
	if session != nil {
		configPath = resolveRuntimeToolConfigPath(session.Config, session)
	}
	if configPath != "" && !chatRuntimeConfigLayerMatch(configPath) {
		manager := runtimecfg.NewRuntimeManager(configPath)
		if err := manager.Load(); err != nil {
			return configPath, 0, err
		}
		config := manager.Get()
		if config == nil {
			return configPath, 0, fmt.Errorf("runtime config is empty")
		}
		if strings.EqualFold(strings.TrimSpace(config.Memory.Mode), mode) {
			return configPath, 0, nil
		}
		config.Memory.Mode = mode
		if err := manager.Update(config); err != nil {
			return configPath, 0, err
		}
		if err := manager.Save(); err != nil {
			return configPath, 0, err
		}
		return configPath, 1, nil
	}

	document, err := agentconfig.LoadMergedRuntimeConfigDocument()
	if err != nil {
		return "", 0, err
	}
	if document == nil {
		return "", 0, fmt.Errorf("runtime config document is unavailable")
	}
	changed, err := document.ApplyDocumentPathChange("memory.mode", mode, document.SourcePath)
	if err != nil {
		return "", 0, err
	}
	target := document.WriteLayerKindFor("memory.mode")
	if target == "" {
		target = "runtime config"
	}
	return target, changed, nil
}
