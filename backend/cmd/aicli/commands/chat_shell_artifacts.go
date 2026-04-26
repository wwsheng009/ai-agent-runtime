package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type localShellArtifactWriter struct {
	path string
	file *os.File
}

func openLocalShellArtifactWriter(session *ChatSession, command string) (*localShellArtifactWriter, error) {
	dir := currentLocalShellArtifactDir(session)
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建本地 shell artifact 目录失败: %w", err)
	}
	path := nextLocalShellArtifactPath(session, dir, command)
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("创建本地 shell artifact 文件失败: %w", err)
	}
	recordLocalShellArtifactPath(session, path)
	return &localShellArtifactWriter{
		path: path,
		file: file,
	}, nil
}

func (w *localShellArtifactWriter) Path() string {
	if w == nil {
		return ""
	}
	return w.path
}

func (w *localShellArtifactWriter) WriteChunk(chunk []byte) error {
	if w == nil || w.file == nil || len(chunk) == 0 {
		return nil
	}
	_, err := w.file.Write(chunk)
	return err
}

func (w *localShellArtifactWriter) Close() error {
	if w == nil || w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *localShellArtifactWriter) Abort() error {
	if w == nil {
		return nil
	}
	path := w.path
	closeErr := w.Close()
	removeErr := error(nil)
	if strings.TrimSpace(path) != "" {
		removeErr = os.Remove(path)
		if os.IsNotExist(removeErr) {
			removeErr = nil
		}
	}
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

func currentLocalShellArtifactDir(session *ChatSession) string {
	if session == nil {
		return ""
	}
	if session.Logger != nil {
		if dir := session.Logger.LocalShellArtifactDir(); dir != "" {
			return resolveAbsoluteChatPath(dir)
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
	return resolveAbsoluteChatPath(filepath.Join(filepath.Dir(sessionPath), baseName+".artifacts", "local-shell"))
}

func currentLastLocalShellArtifactPath(session *ChatSession) string {
	if session == nil {
		return ""
	}
	session.localShellArtifactMu.Lock()
	defer session.localShellArtifactMu.Unlock()
	return resolveAbsoluteChatPath(session.lastLocalShellArtifactPath)
}

func nextLocalShellArtifactPath(session *ChatSession, dir string, command string) string {
	if session == nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	session.localShellArtifactMu.Lock()
	defer session.localShellArtifactMu.Unlock()
	session.localShellArtifactCounter++
	filename := fmt.Sprintf("%03d_%s.txt", session.localShellArtifactCounter, localShellArtifactCommandLabel(command))
	return filepath.Join(dir, filename)
}

func recordLocalShellArtifactPath(session *ChatSession, path string) {
	if session == nil {
		return
	}
	session.localShellArtifactMu.Lock()
	defer session.localShellArtifactMu.Unlock()
	session.lastLocalShellArtifactPath = path
}

func localShellArtifactCommandLabel(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return "command"
	}
	fields := strings.Fields(command)
	label := "command"
	if len(fields) > 0 {
		label = strings.ToLower(fields[0])
	}
	if strings.TrimSpace(label) == "" {
		label = "command"
	}
	replacer := strings.NewReplacer(
		"<", "_",
		">", "_",
		":", "_",
		"\"", "_",
		"/", "_",
		"\\", "_",
		"|", "_",
		"?", "_",
		"*", "_",
		" ", "_",
	)
	label = replacer.Replace(label)
	label = strings.Trim(label, "._-")
	if label == "" {
		label = "command"
	}
	if len(label) > 32 {
		label = label[:32]
	}
	return label
}
