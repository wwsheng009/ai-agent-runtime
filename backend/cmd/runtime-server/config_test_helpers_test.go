package main

import (
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func defaultRuntimeServerDotEnvSearchPaths() []string {
	return config.DotEnvSearchPathsForConfigPaths(defaultRuntimeServerConfigSearchPaths())
}
