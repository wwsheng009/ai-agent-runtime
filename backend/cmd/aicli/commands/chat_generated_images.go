package commands

import (
	"path/filepath"
	"strings"
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
	sessionPath := currentRuntimeSessionPath(session)
	if sessionPath == "" {
		return ""
	}
	baseName := strings.TrimSuffix(filepath.Base(sessionPath), filepath.Ext(sessionPath))
	if baseName == "" {
		return ""
	}
	return resolveAbsoluteChatPath(filepath.Join(filepath.Dir(sessionPath), baseName+".artifacts", "generated-images"))
}
