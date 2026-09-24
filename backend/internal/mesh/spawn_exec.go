package mesh

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ============================================================================
// mesh/spawn 的进程拉起（S9 要点 2）
//
// 子进程必须比发起它的请求活得更久，所以这里刻意用「分离启动」而不是普通
// exec：自己的进程组 / 会话 + 不继承父进程 stdio + 独立日志文件。
//
// stdout/stderr 落到 <aicli home>/logs/mesh-spawn-<sid>.log（0600）：aicli 启动
// 时会把命令行打进日志，而命令行里有写令牌 —— 回传前由 redactSpawnTail 兜底
// （M7），日志文件本身按 0600 收紧。
// ============================================================================

// spawnEnvSpawnedBy mirrors cmd/aicli's meshEnvSpawnedBy: the child records its
// parent node in the node record (§3.1) and reports origin=mesh (§7.2).
const spawnEnvSpawnedBy = "AICLI_MESH_SPAWNED_BY"

// launchDetached starts spec.Executable and returns its pid without ever
// waiting for it. A launch failure (missing binary, bad cwd, ...) is returned
// so Spawn can answer `failed` with the log tail.
func launchDetached(spec SpawnLaunchSpec) (int, error) {
	executable := strings.TrimSpace(spec.Executable)
	if executable == "" {
		return 0, fmt.Errorf("empty executable")
	}
	stdout, closeStdout, err := openSpawnLog(spec.LogPath)
	if err != nil {
		return 0, err
	}
	defer closeStdout()

	cmd := exec.Command(executable, spec.Args...)
	cmd.Dir = strings.TrimSpace(spec.Dir)
	cmd.Env = append([]string(nil), spec.Env...)
	if len(cmd.Env) == 0 {
		cmd.Env = os.Environ()
	}
	// nil stdin = the null device: a detached node must never read from the
	// terminal that spawned it (it would steal keystrokes from the TUI).
	cmd.Stdin = nil
	cmd.Stdout = stdout
	cmd.Stderr = stdout
	cmd.SysProcAttr = spawnSysProcAttr()
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start %s: %w", executable, err)
	}
	pid := cmd.Process.Pid
	// Release (not Wait): the node is deliberately orphaned, and reaping it
	// here would either block the HTTP handler or kill it on the next GC.
	if err := cmd.Process.Release(); err != nil {
		return pid, fmt.Errorf("release pid %d: %w", pid, err)
	}
	return pid, nil
}

// openSpawnLog opens the launch log (0600). Without a log path the child's
// output goes to the null device instead of into our own stdout.
func openSpawnLog(path string) (*os.File, func(), error) {
	path = strings.TrimSpace(path)
	if path == "" {
		devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err != nil {
			return nil, nil, fmt.Errorf("open %s: %w", os.DevNull, err)
		}
		return devNull, func() { _ = devNull.Close() }, nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, SpawnLogFilePerm)
	if err != nil {
		return nil, nil, fmt.Errorf("open log %s: %w", path, err)
	}
	// Windows ignores the mode bits of OpenFile; tighten explicitly so the log
	// (which may hold prompts) is never group/world readable.
	_ = os.Chmod(path, SpawnLogFilePerm)
	return file, func() { _ = file.Close() }, nil
}

// spawnEnvForChild is the child environment: ours plus the parent node id, so
// the spawned node shows up with origin=mesh and spawned_by=<parent>.
func spawnEnvForChild(selfNodeID string) []string {
	env := os.Environ()
	selfNodeID = strings.TrimSpace(selfNodeID)
	if selfNodeID == "" {
		return env
	}
	prefix := spawnEnvSpawnedBy + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		out = append(out, entry)
	}
	return append(out, prefix+selfNodeID)
}
