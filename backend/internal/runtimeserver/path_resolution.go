package runtimeserver

import (
	"os"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

type PathResolution = runtimeexecutor.PathResolution

func ResolveUpwardPath(path string) string {
	return runtimeexecutor.ResolveUpwardPath(path)
}

func ResolveUpwardPaths(paths []string) []string {
	return runtimeexecutor.ResolveUpwardPaths(paths)
}

func ResolveUpwardPathDetail(path string) PathResolution {
	return runtimeexecutor.ResolveUpwardPathDetail(path)
}

func ResolveUpwardPathDetailInWorkdir(path, workdir string) PathResolution {
	return runtimeexecutor.ResolveUpwardPathDetailInWorkdir(path, workdir)
}

func pathExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
