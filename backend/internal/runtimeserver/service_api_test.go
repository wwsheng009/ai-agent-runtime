package runtimeserver

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLocalRuntimeServiceControlStatusIgnoresStalePIDFileForCurrentProcessState(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "runtime-server.pid")
	currentPID := os.Getpid()
	stalePID := currentPID + 100000

	require.NoError(t, WriteInstanceInfo(pidFile, InstanceInfo{
		PID:        stalePID,
		ListenAddr: "127.0.0.1:9999",
		ConfigPath: "./configs/stale.yaml",
		Cwd:        filepath.Join(t.TempDir(), "stale"),
		StartedAt:  time.Unix(1700000000, 0).UTC(),
	}))

	executable, err := os.Executable()
	require.NoError(t, err)
	cwd := t.TempDir()
	control := NewLocalRuntimeServiceControl(
		executable,
		cwd,
		pidFile,
		filepath.Join(cwd, "config.yaml"),
		"127.0.0.1:8101",
	)

	status, err := control.Status()
	require.NoError(t, err)
	require.True(t, status.Running)
	require.Equal(t, currentPID, status.PID)
	require.Equal(t, "127.0.0.1:8101", status.ListenAddr)
	require.Equal(t, resolveAbsolutePath(filepath.Join(cwd, "config.yaml")), status.ConfigPath)
	require.Equal(t, resolveAbsolutePath(cwd), status.Cwd)
	require.Empty(t, status.StartedAt)
	require.Contains(t, status.Note, strconv.Itoa(stalePID))
	require.Contains(t, status.Note, strconv.Itoa(currentPID))
}

func TestLocalRuntimeServiceControlStatusUsesMatchingPIDFileMetadata(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "runtime-server.pid")
	currentPID := os.Getpid()
	startedAt := time.Unix(1700000000, 0).UTC()
	pidConfigPath := filepath.Join(t.TempDir(), "runtime-config.yaml")
	pidCwd := filepath.Join(t.TempDir(), "instance-cwd")

	require.NoError(t, WriteInstanceInfo(pidFile, InstanceInfo{
		PID:        currentPID,
		ListenAddr: "127.0.0.1:9101",
		ConfigPath: pidConfigPath,
		Cwd:        pidCwd,
		StartedAt:  startedAt,
	}))

	executable, err := os.Executable()
	require.NoError(t, err)
	control := NewLocalRuntimeServiceControl(
		executable,
		t.TempDir(),
		pidFile,
		filepath.Join(t.TempDir(), "fallback-config.yaml"),
		"127.0.0.1:8101",
	)

	status, err := control.Status()
	require.NoError(t, err)
	require.True(t, status.Running)
	require.Equal(t, currentPID, status.PID)
	require.Equal(t, "127.0.0.1:9101", status.ListenAddr)
	require.Equal(t, resolveAbsolutePath(pidConfigPath), status.ConfigPath)
	require.Equal(t, resolveAbsolutePath(pidCwd), status.Cwd)
	require.Equal(t, startedAt.Format(time.RFC3339), status.StartedAt)
	require.False(t, strings.Contains(status.Note, "不一致"))
}
