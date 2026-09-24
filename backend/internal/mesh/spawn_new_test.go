package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// new（SpawnRequest.NewSession）—— 新建会话
// ---------------------------------------------------------------------------

// newSessionSpawnOptions 是 `new` 的最小装配：临时网格根、假时钟、假启动器。
// 与 spawnTestOptions 的差别只有工作区解析——新会话没有绑定可查，工作区必须由
// 调用方给出（CLI 用 --workspace 或当前目录）。
func newSessionSpawnOptions(t *testing.T, paths Paths, clock *fakeClock, workspace string, launch func(SpawnLaunchSpec) (int, error)) SpawnOptions {
	t.Helper()
	opts := spawnTestOptions(t, paths, clock, launch)
	opts.WorkspaceFor = func(string) (string, bool) { return workspace, true }
	return opts
}

// TestSpawnNewSessionLaunchesChatAndLearnsSessionID 是 `new` 的主路径：命令行是
// `aicli chat`（没有会话 ID），会话 ID 从子进程的档案里读回来。
func TestSpawnNewSessionLaunchesChatAndLearnsSessionID(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	workspace := t.TempDir()
	var captured SpawnLaunchSpec
	opts := newSessionSpawnOptions(t, paths, clock, workspace, func(spec SpawnLaunchSpec) (int, error) {
		captured = spec
		// 子进程起来后自己写档案：会话 ID 由它生成。档案里的 pid 必须等于启动器
		// 返回的 pid（`new` 就靠这一条把档案认成「我们刚拉起的子进程」），而档案
		// 又必须属于一个活进程（否则 BuildView 判 stale），所以两边都用本测试进程。
		pid := os.Getpid()
		writeSpawnTestRecord(t, paths, clock, "node-new", "session_new_1", pid, 56100)
		return pid, nil
	})

	result := Spawn(SpawnRequest{NewSession: true, Origin: "cli"}, opts)

	if result.Status != SpawnStatusStarted {
		t.Fatalf("status = %q (code %q, reason %q), want started", result.Status, result.Code, result.Reason)
	}
	if result.SessionID != "session_new_1" {
		t.Fatalf("session = %q, want the id the child published", result.SessionID)
	}
	if result.NodeID != "node-new" || result.Port != 56100 {
		t.Fatalf("node/port = %q/%d, want node-new/56100", result.NodeID, result.Port)
	}
	if !strings.Contains(result.URL, "session=session_new_1") || !strings.Contains(result.URL, "token=child-token-0123456789abcd") {
		t.Fatalf("url = %q, want the new session deep link + record token", result.URL)
	}
	// `new` 的命令行：chat 无会话 ID，其余旗标与 open 相同（§5.7 的窗口契约）。
	wantArgs := []string{"chat", "--pprof", "--web-host", "127.0.0.1", "--web-token", spawnTestToken}
	if strings.Join(captured.Args, " ") != strings.Join(wantArgs, " ") {
		t.Fatalf("args = %v, want %v", captured.Args, wantArgs)
	}
	if captured.Dir != workspace {
		t.Fatalf("dir = %q, want the requested workspace %q", captured.Dir, workspace)
	}
	if !containsEnv(captured.Env, spawnEnvSpawnedBy+"=node-spawner") {
		t.Fatalf("env %v must announce the spawning node", captured.Env)
	}
	// 会话 ID 还不存在，日志按「本次调用」命名：两次 new 不能共用一个日志。
	if base := filepath.Base(captured.LogPath); !strings.HasPrefix(base, spawnLogPrefix+"new-") {
		t.Fatalf("log path = %q, want a per-invocation mesh-spawn-new-*.log", captured.LogPath)
	}
}

// TestSpawnNewSessionTakesNoLease：单飞租约以会话 ID 为键，新会话还没有 ID。
// CLI/父进程不占租约（也不写档案/binding）——那是子进程自己的事。
func TestSpawnNewSessionTakesNoLease(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	opts := newSessionSpawnOptions(t, paths, clock, t.TempDir(), func(SpawnLaunchSpec) (int, error) {
		pid := os.Getpid()
		writeSpawnTestRecord(t, paths, clock, "node-new", "session_new_lease", pid, 56101)
		return pid, nil
	})

	result := Spawn(SpawnRequest{NewSession: true}, opts)
	if result.Status != SpawnStatusStarted {
		t.Fatalf("status = %q (reason %q)", result.Status, result.Reason)
	}
	if result.Lease != "" {
		t.Fatalf("lease = %q, want none: 新会话没有可抢的会话键", result.Lease)
	}
	entries, err := os.ReadDir(paths.Leases)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir(%s): %v", paths.Leases, err)
	}
	if len(entries) != 0 {
		t.Fatalf("leases = %d file(s), want 0", len(entries))
	}
}

// TestSpawnNewSessionNeverReuses：同一个工作区已有活节点时，`new` 仍然要开新
// 会话——这正是它与 `open` 的分界（open 复用，new 从不复用）。
func TestSpawnNewSessionNeverReuses(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	workspace := t.TempDir()
	seedLiveNode(t, paths, clock.Now(), "node-old", "session_old", workspace)

	launched := false
	opts := newSessionSpawnOptions(t, paths, clock, workspace, func(SpawnLaunchSpec) (int, error) {
		launched = true
		pid := os.Getpid()
		writeSpawnTestRecord(t, paths, clock, "node-new", "session_new_2", pid, 56102)
		return pid, nil
	})

	result := Spawn(SpawnRequest{NewSession: true}, opts)
	if !launched {
		t.Fatal("new 必须启动新进程：同工作区的活节点不得被复用")
	}
	if result.Status != SpawnStatusStarted || result.SessionID != "session_new_2" {
		t.Fatalf("status/session = %q/%q, want started/session_new_2", result.Status, result.SessionID)
	}
}

// TestSpawnNewSessionRejectsSessionID：会话 ID 由子进程生成，调用方给了就是
// 用错了 API（而不是悄悄忽略）。
func TestSpawnNewSessionRejectsSessionID(t *testing.T) {
	paths := testMeshPaths(t)
	opts := newSessionSpawnOptions(t, paths, newFakeClock(), t.TempDir(), func(SpawnLaunchSpec) (int, error) {
		t.Fatal("参数非法时不得启动进程")
		return 0, nil
	})
	result := Spawn(SpawnRequest{NewSession: true, SessionID: "session_x"}, opts)
	if result.Status != SpawnStatusFailed || !strings.Contains(result.Reason, "session_id must be empty") {
		t.Fatalf("result = %+v, want failed with a session_id explanation", result)
	}
}

// TestSpawnNewSessionRequiresARealWorkspace：新会话没有绑定可查，工作区要么由
// 调用方明确给出，要么当场失败（绝不猜一个目录）。
func TestSpawnNewSessionRequiresARealWorkspace(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	noLaunch := func(SpawnLaunchSpec) (int, error) {
		t.Fatal("工作区不成立时不得启动进程")
		return 0, nil
	}

	t.Run("empty", func(t *testing.T) {
		opts := spawnTestOptions(t, paths, clock, noLaunch)
		opts.WorkspaceFor = func(string) (string, bool) { return "  ", true }
		result := Spawn(SpawnRequest{NewSession: true}, opts)
		if result.Status != SpawnStatusFailed || result.Code != SpawnCodeWorkspaceMissing {
			t.Fatalf("result = %+v, want failed/%s", result, SpawnCodeWorkspaceMissing)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		opts := spawnTestOptions(t, paths, clock, noLaunch)
		opts.WorkspaceFor = func(string) (string, bool) { return "", false }
		result := Spawn(SpawnRequest{NewSession: true}, opts)
		if result.Status != SpawnStatusFailed || result.Code != SpawnCodeWorkspaceMissing {
			t.Fatalf("result = %+v, want failed/%s", result, SpawnCodeWorkspaceMissing)
		}
	})

	t.Run("deleted", func(t *testing.T) {
		opts := newSessionSpawnOptions(t, paths, clock, filepath.Join(t.TempDir(), "gone"), noLaunch)
		result := Spawn(SpawnRequest{NewSession: true}, opts)
		if result.Status != SpawnStatusFailed || result.Code != SpawnCodeWorkspaceMissing {
			t.Fatalf("result = %+v, want failed/%s", result, SpawnCodeWorkspaceMissing)
		}
		if !strings.Contains(result.Reason, "workspace directory not found") {
			t.Fatalf("reason = %q, want a readable path", result.Reason)
		}
	})
}

// TestSpawnNewSessionNoWaitReportsOnlyWhatIsKnown：--no-wait 时不谎报会话 ID
// （此刻只有子进程知道），只给 pid/端口与「怎么找回它」。
func TestSpawnNewSessionNoWaitReportsOnlyWhatIsKnown(t *testing.T) {
	paths := testMeshPaths(t)
	opts := newSessionSpawnOptions(t, paths, newFakeClock(), t.TempDir(), func(SpawnLaunchSpec) (int, error) {
		return 4321, nil
	})
	opts.FireAndForget = true
	opts.Wait = 20 * time.Millisecond

	result := Spawn(SpawnRequest{NewSession: true, Port: 56103}, opts)
	if result.Status != SpawnStatusStarted {
		t.Fatalf("status = %q (reason %q), want started", result.Status, result.Reason)
	}
	if result.SessionID != "" {
		t.Fatalf("session = %q, want empty: 档案还没出现，ID 未知", result.SessionID)
	}
	if result.PID != 4321 || result.Port != 56103 {
		t.Fatalf("pid/port = %d/%d, want 4321/56103", result.PID, result.Port)
	}
	if !strings.Contains(result.URL, "token=") || strings.Contains(result.URL, "session=") {
		t.Fatalf("url = %q, want a token-only URL（会话 ID 未知）", result.URL)
	}
	if !strings.Contains(result.Reason, "aicli-mesh ls") {
		t.Fatalf("reason = %q, want a pointer to `ls`", result.Reason)
	}
}

// TestSpawnNewSessionTimeoutCarriesLaunchLog：子进程起来了但一直没写档案时，
// 结果是 not_running + 超时码 + 已脱敏的日志尾部（与 open 同一套诊断）。
func TestSpawnNewSessionTimeoutCarriesLaunchLog(t *testing.T) {
	paths := testMeshPaths(t)
	opts := newSessionSpawnOptions(t, paths, newFakeClock(), t.TempDir(), func(spec SpawnLaunchSpec) (int, error) {
		if err := os.WriteFile(spec.LogPath, []byte("provider not configured: set OPENAI_API_KEY\n"), SpawnLogFilePerm); err != nil {
			t.Fatalf("write log: %v", err)
		}
		return 9999, nil
	})
	opts.Wait = 40 * time.Millisecond
	opts.PollInterval = 5 * time.Millisecond

	result := Spawn(SpawnRequest{NewSession: true}, opts)
	if result.Status != SpawnStatusNotRunning || result.Code != SpawnCodeTimeout {
		t.Fatalf("status/code = %q/%q, want not_running/%s", result.Status, result.Code, SpawnCodeTimeout)
	}
	if !strings.Contains(strings.Join(result.LogTail, "\n"), "provider not configured") {
		t.Fatalf("log tail = %v, want the child's own words", result.LogTail)
	}
	if strings.Contains(strings.Join(result.LogTail, "\n"), spawnTestToken) {
		t.Fatalf("log tail must be redacted: %v", result.LogTail)
	}
}

// TestSpawnNewSessionFailsLoudlyWithoutMeshRoot：没有网格根就没有可等的档案，
// 宁可失败也不谎报 started（与 open 同一条 fail-closed 规则）。
func TestSpawnNewSessionFailsLoudlyWithoutMeshRoot(t *testing.T) {
	opts := SpawnOptions{
		Paths:        Paths{Source: PathSourceUnset},
		Executable:   "aicli-test",
		Wait:         20 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
		WorkspaceFor: func(string) (string, bool) { return t.TempDir(), true },
		Launch: func(SpawnLaunchSpec) (int, error) {
			t.Fatal("网格根不可用时不得启动进程")
			return 0, nil
		},
	}
	result := Spawn(SpawnRequest{NewSession: true}, opts)
	if result.Status != SpawnStatusFailed || result.Code != SpawnCodeMeshDisabled {
		t.Fatalf("result = %+v, want failed/%s", result, SpawnCodeMeshDisabled)
	}
}

// TestLiveNodeForPIDIgnoresSessionlessRecords：子进程先写档案、后写会话 ID。
// 没有会话 ID 的记录不算就绪，否则 new 会返回一个空会话。
func TestLiveNodeForPIDIgnoresSessionlessRecords(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	writeSpawnTestRecord(t, paths, clock, "node-boot", "", os.Getpid(), 56104)

	if node, ok := liveNodeForPID(paths, os.Getpid(), clock.Now(), DefaultHeartbeatTTL); ok {
		t.Fatalf("node = %+v, want no match before the child publishes its session", node)
	}
	writeSpawnTestRecord(t, paths, clock, "node-boot", "session_late", os.Getpid(), 56104)
	node, ok := liveNodeForPID(paths, os.Getpid(), clock.Now(), DefaultHeartbeatTTL)
	if !ok || sessionIDOf(node) != "session_late" {
		t.Fatalf("node = %+v ok=%v, want the session the child published", node, ok)
	}
}
