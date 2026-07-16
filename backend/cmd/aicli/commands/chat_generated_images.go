package commands

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

func currentGeneratedImageArtifactDir(session *ChatSession) string {
	if session == nil {
		return ""
	}
	if session.Logger != nil {
		if sessionDir := session.Logger.SessionDirPath(); strings.TrimSpace(sessionDir) != "" {
			return resolveAbsoluteChatPath(filepath.Join(sessionDir, "generated-images"))
		}
	}
	artifactRoot := currentRuntimeSessionArtifactRoot(session)
	if artifactRoot == "" {
		return ""
	}
	return resolveAbsoluteChatPath(filepath.Join(artifactRoot, "generated-images"))
}

func generatedImageToolContext(ctx context.Context, session *ChatSession) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if session == nil {
		return ctx
	}
	if sessionID := currentRuntimeSessionID(session); strings.TrimSpace(sessionID) != "" {
		ctx = toolctx.WithSessionID(ctx, sessionID)
	}
	if outputDir := currentGeneratedImageArtifactDir(session); strings.TrimSpace(outputDir) != "" {
		ctx = toolctx.WithGeneratedImageOutputDir(ctx, outputDir)
	}
	if outputDir := currentLocalShellArtifactDir(session); strings.TrimSpace(outputDir) != "" {
		ctx = toolctx.WithShellOutputArtifactDir(ctx, outputDir)
	}
	return ctx
}
