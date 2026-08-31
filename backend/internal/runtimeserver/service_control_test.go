package runtimeserver

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
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
	if err != nil {
		// Linux 无 lsof 时端口探测不可用；Windows netstat 始终可用。
		if runtime.GOOS == "windows" {
			require.NoError(t, err)
		}
		t.Skipf("listening port probe unavailable: %v", err)
	}
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

// 本测试进程自己绑定端口充当"受管服务"：监听者必然是本进程 PID。
// OS PID 复用会造成"记录 PID 存在但服务已死"的误判，端口交叉验证应免疫。
func TestResolveInstancePID(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("端口探测依赖 Windows netstat")
	}

	// 1. 记录 PID 与端口监听者一致 → 正常运行。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	addr := listener.Addr().String()
	selfPID := os.Getpid()

	pid, alive := ResolveInstancePID(selfPID, addr)
	require.True(t, alive)
	require.Equal(t, selfPID, pid)

	// 2. 记录 PID 陈旧（与监听者不符）→ 以端口监听者为准。
	pid, alive = ResolveInstancePID(999999, addr)
	require.True(t, alive)
	require.Equal(t, selfPID, pid)

	// 3. 端口无监听但记录 PID 仍"存在"（被系统复用的无关进程）→ 视为未运行。
	//    复用场景难复现，用"本进程活着但没监听"模拟：进程存在但该端口
	//    没有它，判定结果应一致（不杀、不误报）。
	require.NoError(t, listener.Close())
	pid, alive = ResolveInstancePID(selfPID, addr)
	require.False(t, alive)
	require.Zero(t, pid)

	// 4. 端口无监听且 PID 不存在 → 未运行。
	pid, alive = ResolveInstancePID(999999, addr)
	require.False(t, alive)
	require.Zero(t, pid)

	// 5. 无监听地址时退回进程存在性判断（兼容旧格式 PID 文件）。
	pid, alive = ResolveInstancePID(selfPID, "")
	require.True(t, alive)
	require.Equal(t, selfPID, pid)
	pid, alive = ResolveInstancePID(999999, "")
	require.False(t, alive)
	require.Zero(t, pid)

	// 6. 端口探测不可用（非法地址）→ 退回进程存在性判断，不误判"未运行"。
	pid, alive = ResolveInstancePID(selfPID, "not-a-valid-addr:::")
	require.True(t, alive)
	require.Equal(t, selfPID, pid)

	// 7. 非法记录 PID。
	pid, alive = ResolveInstancePID(0, addr)
	require.False(t, alive)
	require.Zero(t, pid)
}

func TestResolveStopTarget(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("端口探测依赖 Windows netstat")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	addr := listener.Addr().String()
	selfPID := os.Getpid()

	// 1. 记录 PID 与端口监听者一致 → 以记录 PID 为目标。
	pid, alive, err := ResolveStopTarget(selfPID, addr)
	require.NoError(t, err)
	require.True(t, alive)
	require.Equal(t, selfPID, pid)

	// 2. 记录 PID 陈旧但端口监听者存在：监听者身份不是 runtime-server
	//    （测试二进制名不含 "runtime-server"）→ 拒绝自动终止，err 非空。
	pid, alive, err = ResolveStopTarget(999999, addr)
	require.Error(t, err)
	require.False(t, alive)
	require.Zero(t, pid)

	// 3. 端口无监听者（监听者为本测试进程但端口已关闭）→ 未运行。
	require.NoError(t, listener.Close())
	pid, alive, err = ResolveStopTarget(selfPID, addr)
	require.NoError(t, err)
	require.False(t, alive)
	require.Zero(t, pid)

	// 4. 无监听地址 → 退回进程存在性判断。
	pid, alive, err = ResolveStopTarget(selfPID, "")
	require.NoError(t, err)
	require.True(t, alive)
	require.Equal(t, selfPID, pid)

	// 5. 非法记录 PID。
	pid, alive, err = ResolveStopTarget(0, addr)
	require.NoError(t, err)
	require.False(t, alive)
	require.Zero(t, pid)
}

func TestLooksLikeRuntimeServerProcess(t *testing.T) {
	// 测试二进制本身不是 runtime-server 构建。
	require.False(t, looksLikeRuntimeServerProcess(os.Getpid()))
	require.False(t, looksLikeRuntimeServerProcess(999999))
}

func TestTerminationTargetAlive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("端口探测依赖 Windows netstat")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	selfPID := os.Getpid()

	// 服务存活且端口仍由该 PID 监听。
	require.True(t, terminationTargetAlive(selfPID, addr))

	// 服务退出但 PID 被"复用"（本进程仍活着）→ 不再判定为存活。
	require.NoError(t, listener.Close())
	require.False(t, terminationTargetAlive(selfPID, addr))

	// 无监听地址时退回进程存在性判断。
	require.True(t, terminationTargetAlive(selfPID, ""))
	require.False(t, terminationTargetAlive(999999, ""))
}
