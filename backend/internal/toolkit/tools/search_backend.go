package tools

import (
	"strings"

	runtimeripgrep "github.com/wwsheng009/ai-agent-runtime/internal/ripgrep"
)

func annotateSearchBackend(metadata map[string]interface{}, engine, command, binaryPath string) {
	if metadata == nil {
		return
	}
	engine = strings.TrimSpace(engine)
	if engine == "" {
		engine = "builtin"
	}
	metadata["engine"] = engine
	metadata["execution_backend"] = engine
	if command = strings.TrimSpace(command); command != "" {
		metadata["backend_command"] = command
	}
	if binaryPath = strings.TrimSpace(binaryPath); binaryPath != "" {
		metadata["backend_path"] = binaryPath
		metadata["backend_source"] = runtimeripgrep.SourceForPath(binaryPath)
	}
}
