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
func suppressChatConsoleLogger(cfg *config.Config, opts *chatCommandOptions) func() {
	if cfg == nil {
		return nil
	}

	original := cfg.Log
	if strings.EqualFold(strings.TrimSpace(original.Output), "file") && !original.EnableConsole && strings.TrimSpace(original.FilePath) != "" {
		return nil
	}

	nextCfg := original
	nextCfg.Output = "file"
	nextCfg.EnableConsole = false

	fallbackFilePath := ""
	if strings.TrimSpace(nextCfg.FilePath) == "" {
		var err error
		fallbackFilePath, err = prepareChatConsoleLogFilePath(opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: 无法准备 chat 日志文件: %v\n", err)
			return nil
		}
		nextCfg.FilePath = fallbackFilePath
	}

	if err := logpkg.InitLogger(&nextCfg); err != nil {
		if fallbackFilePath != "" {
			_ = os.Remove(fallbackFilePath)
		}
		fmt.Fprintf(os.Stderr, "Warning: 无法切换 chat 日志为文件模式: %v\n", err)
		return nil
	}

	return func() {
		if err := logpkg.InitLogger(&original); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: 无法恢复原始日志配置: %v\n", err)
		}
	}
}

func prepareChatConsoleLogFilePath(opts *chatCommandOptions) (string, error) {
	logDir := ""
	if opts != nil {
		logDir = strings.TrimSpace(opts.LogDir)
	}
	if logDir == "" {
		logDir = resolveDefaultChatLogDir()
	}
	if logDir == "" {
		return "", fmt.Errorf("chat log dir is empty")
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", fmt.Errorf("创建 chat 日志目录失败: %w", err)
	}

	file, err := os.CreateTemp(logDir, "chat-console-*.log")
	if err != nil {
		return "", fmt.Errorf("创建 chat console 日志文件失败: %w", err)
	}
	path := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("关闭 chat console 日志文件失败: %w", closeErr)
	}
	return path, nil
}
