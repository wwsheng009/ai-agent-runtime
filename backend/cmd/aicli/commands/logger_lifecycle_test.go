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
	require.False(t, persisted.LastObservedAt.IsZero())
	require.Zero(t, persisted.EndTime)
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
