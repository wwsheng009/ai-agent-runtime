package commands

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

func TestChatLoggerFlushReportsActiveDurationAndObservation(t *testing.T) {
	logger := NewChatLogger("provider", "openai", "model", false, "")
	logger.sessionLog.StartTime = time.Now().Add(-2 * time.Second)
	require.NoError(t, logger.SetLogDir(t.TempDir()))

	require.NoError(t, logger.FlushSession())
	require.Equal(t, "active", logger.sessionLog.Status)
	require.True(t, logger.sessionLog.EndTime.IsZero())
	require.False(t, logger.sessionLog.LastObservedAt.IsZero())
	require.GreaterOrEqual(t, logger.CurrentSummary().TotalDurationMs, int64(1000))

	payload, err := os.ReadFile(logger.SessionLogPath())
	require.NoError(t, err)
	var persisted ChatSessionLog
	require.NoError(t, json.Unmarshal(payload, &persisted))
	require.Equal(t, "active", persisted.Status)
	require.NotEmpty(t, persisted.WorkingDirectory)
	require.NotEmpty(t, persisted.ProjectPath)
	require.False(t, persisted.LastObservedAt.IsZero())
	require.Zero(t, persisted.EndTime)
}

func TestNewChatLoggerCapturesWorkingDirectoryAndProjectRoot(t *testing.T) {
	previous, err := os.Getwd()
	require.NoError(t, err)
	project := filepath.Join(t.TempDir(), "project")
	workingDirectory := filepath.Join(project, "backend", "internal")
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(workingDirectory, 0o755))
	require.NoError(t, os.Chdir(workingDirectory))
	t.Cleanup(func() { _ = os.Chdir(previous) })

	logger := NewChatLogger("provider", "openai", "model", false, "")
	require.Equal(t, filepath.Clean(workingDirectory), logger.sessionLog.WorkingDirectory)
	require.Equal(t, filepath.Clean(project), logger.sessionLog.ProjectPath)

	logger.logDir = ""
	require.NoError(t, logger.RotateSession())
	require.Equal(t, filepath.Clean(workingDirectory), logger.sessionLog.WorkingDirectory)
	require.Equal(t, filepath.Clean(project), logger.sessionLog.ProjectPath)
}

func TestChatLoggerCapturesRuntimeSessionTitleAndFirstPrompt(t *testing.T) {
	logger := NewChatLogger("provider", "openai", "model", false, "")
	runtimeSession := runtimechat.NewSession("user")
	runtimeSession.ID = "session-runtime-title"
	runtimeSession.UpdateTitle("Usage analytics investigation")
	session := &ChatSession{Logger: logger, RuntimeSession: runtimeSession}

	beginChatUserTurn(session, "First user prompt")
	syncChatLoggerSessionMetadata(session)

	require.Equal(t, "First user prompt", logger.sessionLog.InitialMessage)
	require.Equal(t, runtimeSession.ID, logger.sessionLog.RuntimeSessionID)
	require.Equal(t, "Usage analytics investigation", logger.sessionLog.Title)
}

func TestChatLoggerFailSessionPersistsTerminalFailure(t *testing.T) {
	logger := NewChatLogger("provider", "openai", "model", false, "")
	require.NoError(t, logger.SetLogDir(t.TempDir()))
	terminalErr := errors.New("provider quota exhausted")

	require.NoError(t, logger.FailSession(terminalErr))
	require.Equal(t, "failed", logger.sessionLog.Status)
	require.False(t, logger.sessionLog.EndTime.IsZero())
	require.Contains(t, logger.sessionLog.TerminationReason, terminalErr.Error())
	require.GreaterOrEqual(t, logger.CurrentSummary().TotalDurationMs, int64(0))

	payload, err := os.ReadFile(filepath.Clean(logger.SessionLogPath()))
	require.NoError(t, err)
	var persisted ChatSessionLog
	require.NoError(t, json.Unmarshal(payload, &persisted))
	require.Equal(t, "failed", persisted.Status)
	require.Contains(t, persisted.TerminationReason, "quota")
}

func TestChatLoggerSaveSessionInfersFailureFromLastResponse(t *testing.T) {
	logger := NewChatLogger("provider", "openai", "model", false, "")
	require.NoError(t, logger.SetLogDir(t.TempDir()))
	terminalErr := errors.New("HTTP 403 insufficient quota")
	logger.LogResponse(aicliLogScope{TurnID: "turn-1", RequestID: "request-1"}, nil, nil, false, terminalErr, 10)

	require.NoError(t, logger.SaveSession())
	require.Equal(t, "failed", logger.sessionLog.Status)
	require.Contains(t, logger.sessionLog.TerminationReason, "insufficient quota")
}

func TestFinalizeChatSessionWithErrorPersistsFailure(t *testing.T) {
	logger := NewChatLogger("provider", "openai", "model", false, "")
	require.NoError(t, logger.SetLogDir(t.TempDir()))
	session := &ChatSession{NoInteractive: true, Logger: logger}

	finalizeChatSessionWithError(session, errors.New("stream interrupted"))

	require.Equal(t, "failed", logger.sessionLog.Status)
	require.True(t, strings.Contains(logger.sessionLog.TerminationReason, "stream interrupted"))
	require.False(t, logger.sessionLog.EndTime.IsZero())
}

func TestChatLoggerRotateSessionStartsFreshArtifactLayout(t *testing.T) {
	logger := NewChatLogger("provider", "openai", "model", true, "https://example.com")
	logDir := t.TempDir()
	require.NoError(t, logger.SetLogDir(logDir))

	oldSessionID := logger.sessionID
	oldSessionDir := logger.SessionDirPath()
	oldLogPath := logger.SessionLogPath()
	require.NotEmpty(t, oldSessionDir)

	logger.LogRequest(aicliLogScope{TurnID: "turn-1", RequestID: "req-1"}, map[string]string{"hello": "world"})
	require.NoError(t, logger.FlushSession())
	_, err := os.Stat(oldLogPath)
	require.NoError(t, err)

	require.NoError(t, logger.RotateSession())

	require.NotEqual(t, oldSessionID, logger.sessionID)
	require.NotEqual(t, oldSessionDir, logger.SessionDirPath())
	require.NotEqual(t, oldLogPath, logger.SessionLogPath())
	require.Equal(t, "active", logger.sessionLog.Status)
	require.True(t, logger.sessionLog.EndTime.IsZero())
	require.Empty(t, logger.sessionLog.Messages)
	require.Equal(t, "provider", logger.sessionLog.Provider)
	require.Equal(t, "openai", logger.sessionLog.Protocol)
	require.Equal(t, "model", logger.sessionLog.Model)
	require.True(t, logger.sessionLog.Stream)
	require.Equal(t, "https://example.com", logger.sessionLog.BaseURL)
	require.Zero(t, logger.totalRequests)

	for _, path := range []string{
		logger.SessionDirPath(),
		logger.RuntimeHTTPArtifactDir(),
		logger.LocalShellArtifactDir(),
		logger.DebugLogPath(),
	} {
		info, statErr := os.Stat(path)
		require.NoError(t, statErr, path)
		if path == logger.DebugLogPath() {
			require.False(t, info.IsDir(), path)
			continue
		}
		require.True(t, info.IsDir(), path)
	}

	// Previous session remains on disk after rotation.
	_, err = os.Stat(oldLogPath)
	require.NoError(t, err)
}
