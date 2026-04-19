package runtimeserver

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadWriteInstanceInfo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-server.pid")
	info := InstanceInfo{
		PID:        4321,
		ListenAddr: "127.0.0.1:8101",
		ConfigPath: "./configs/config.yaml",
		Cwd:        "E:/projects/ai/ai-agent-runtime/backend",
		StartedAt:  time.Unix(1700000000, 0).UTC(),
	}

	require.NoError(t, WriteInstanceInfo(path, info))

	loaded, err := ReadInstanceInfo(path)
	require.NoError(t, err)
	require.Equal(t, info.PID, loaded.PID)
	require.Equal(t, info.ListenAddr, loaded.ListenAddr)
	require.Equal(t, info.ConfigPath, loaded.ConfigPath)
	require.Equal(t, info.Cwd, loaded.Cwd)
	require.True(t, info.StartedAt.Equal(loaded.StartedAt))
}

func TestReadInstanceInfo_PlainPIDFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-server.pid")
	require.NoError(t, os.WriteFile(path, []byte("9876\n"), 0o644))

	loaded, err := ReadInstanceInfo(path)
	require.NoError(t, err)
	require.Equal(t, 9876, loaded.PID)
}

func TestRemoveInstanceInfoIfPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-server.pid")
	require.NoError(t, WriteInstanceInfo(path, InstanceInfo{PID: 1001}))

	require.NoError(t, RemoveInstanceInfoIfPID(path, 2002))
	_, err := os.Stat(path)
	require.NoError(t, err)

	require.NoError(t, RemoveInstanceInfoIfPID(path, 1001))
	_, err = os.Stat(path)
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))
}

func TestFindListeningPID_FreePortReturnsZero(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())

	pid, err := findListeningPIDByPort(port)
	require.NoError(t, err)
	require.Zero(t, pid)
}

func TestPrepareStartCommandUsesManagedBinaryPathWhenGoRunBinaryDetected(t *testing.T) {
	root := t.TempDir()
	mainFile := filepath.Join(root, "cmd", "runtime-server", "main.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(mainFile), 0o755))
	require.NoError(t, os.WriteFile(mainFile, []byte("package main\n"), 0o644))
	goMod := filepath.Join(root, "go.mod")
	require.NoError(t, os.WriteFile(goMod, []byte("module example.com/test\n\ngo 1.24.0\n"), 0o644))

	executable := filepath.Join(os.TempDir(), "go-build123", "b001", "exe", "main.exe")
	require.True(t, shouldUseGoRunLauncher(executable, root))
	require.Equal(t, filepath.Join(root, "logs", managedRuntimeServerBinaryName()), filepath.Join(root, "logs", managedRuntimeServerBinaryName()))
}

func TestPrepareStartCommandLeavesNormalExecutableUntouched(t *testing.T) {
	command, args, err := PrepareStartCommand(filepath.Join("E:", "tools", managedRuntimeServerBinaryName()), "E:\\workspace", []string{"serve"})
	require.NoError(t, err)
	require.Equal(t, filepath.Join("E:", "tools", managedRuntimeServerBinaryName()), command)
	require.Equal(t, []string{"serve"}, args)
}

func TestParseWindowsNetstatListeningPID(t *testing.T) {
	output := []byte(`
  Proto  Local Address          Foreign Address        State           PID
  TCP    0.0.0.0:8101           0.0.0.0:0              LISTENING       36484
  TCP    127.0.0.1:8129         0.0.0.0:0              LISTENING       31752
`)

	pid, err := parseWindowsNetstatListeningPID(output, 8101)
	require.NoError(t, err)
	require.Equal(t, 36484, pid)

	pid, err = parseWindowsNetstatListeningPID(output, 9999)
	require.NoError(t, err)
	require.Zero(t, pid)
}
