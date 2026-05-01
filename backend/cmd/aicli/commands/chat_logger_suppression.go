package commands

import (
	"fmt"
	"os"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// suppressChatConsoleLogger temporarily switches the global logger to file-only.
// Chat turns emit a lot of transport/debug logs, and they should not interleave
// with the interactive chat UI.
func suppressChatConsoleLogger(cfg *config.Config) func() {
	if cfg == nil {
		return nil
	}

	original := cfg.Log
	if strings.TrimSpace(original.FilePath) == "" {
		return nil
	}

	if strings.EqualFold(strings.TrimSpace(original.Output), "file") && !original.EnableConsole {
		return nil
	}

	nextCfg := original
	nextCfg.Output = "file"
	nextCfg.EnableConsole = false

	if err := logpkg.InitLogger(&nextCfg); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 无法切换 chat 日志为文件模式: %v\n", err)
		return nil
	}

	return func() {
		if err := logpkg.InitLogger(&original); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: 无法恢复原始日志配置: %v\n", err)
		}
	}
}
