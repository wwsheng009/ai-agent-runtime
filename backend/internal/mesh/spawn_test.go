package mesh

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

const spawnTestToken = "spawn-token-0123456789abcdef"

// spawnTestOptions wires Spawn for a deterministic run: temp mesh root, fake
// clock, fake launcher, tiny wait budget. Nothing here touches a real process.
func spawnTestOptions(t *testing.T, paths Paths, clock *fakeClock, launch func(SpawnLaunchSpec) (int, error)) SpawnOptions {
	t.Helper()
	return SpawnOptions{
		Paths:        paths,
		Now:          clock.Now,
		SelfNodeID:   "node-spawner",
		PID:          4242,
		Executable:   "aicli-test",
		LogDir:       t.TempDir(),
		Launch:       launch,
		Wait:         300 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
		Token:        func() string { return spawnTestToken },
		WorkspaceFor: func(string) (string, bool) { return "", true },
	}
}

// spawnTestLiveNode starts a real host (this test process) serving sessionID,
// with an endpoint so the reuse path can hand back a URL.
func spawnTestLiveNode(t *testing.T, clock *fakeClock, sessionID string, port int) *Host {
	t.Helper()
	host := NewHost(HostConfig{Kind: "chat", Origin: "resume", Exe: "aicli.exe", Now: clock.Now})
	if err := host.Start(); err != nil {
		t.Fatalf("host.Start: %v", err)
	}
	t.Cleanup(host.Close)
	host.SetSession(&SessionInfo{ID: sessionID, Title: "demo", ActivatedAt: clock.Now()})
	host.SetEndpoint(EndpointInfo{
		Scheme:     "http",
		Host:       "127.0.0.1",
		Port:       port,
		Loopback:   true,
		BaseURL:    "http://127.0.0.1:" + strconv.Itoa(port),
		WebBaseURL: "http://127.0.0.1:" + strconv.Itoa(port) + "/web",
	}, AuthInfo{Mode: "token", Required: true, Token: "node-token-abcdef0123456789"})
	return host
}

// writeSpawnTestRecord drops a live-looking node record for sessionID (used to
// simulate the child finishing its startup). The pid must belong to a live
// process, otherwise BuildView classifies the record as stale/stopped — tests
// therefore declare os.Getpid(), i.e. this test binary.
func writeSpawnTestRecord(t *testing.T, paths Paths, clock *fakeClock, nodeID, sessionID string, pid, port int) {
	t.Helper()
	record := NodeRecord{
		SchemaVersion: SchemaVersion,
		NodeID:        nodeID,
		PID:           pid,
		Kind:          "chat",
		Process:       ProcessInfo{StartedAt: clock.Now(), Origin: "mesh", SpawnedBy: "node-spawner"},
		Endpoint: &EndpointInfo{
			Scheme:   "http",
			Host:     "127.0.0.1",
			Port:     port,
			Loopback: true,
			BaseURL:  "http://127.0.0.1:" + strconv.Itoa(port),
		},
		Auth:    &AuthInfo{Mode: "token", Required: true, Token: "child-token-0123456789abcd"},
		Session: &SessionInfo{ID: sessionID},
		Liveness: LivenessInfo{
			StartedAt:       clock.Now(),
			UpdatedAt:       clock.Now(),
			HeartbeatAt:     clock.Now(),
			HeartbeatTTLSec: int(DefaultHeartbeatTTL / time.Second),
			State:           string(NodeStateLive),
		},
	}
	if err := WriteNodeRecord(paths, record); err != nil {
		t.Fatalf("WriteNodeRecord: %v", err)
	}
}

// ---------------------------------------------------------------------------
// reuse
// ---------------------------------------------------------------------------

func TestSpawnReusesLiveNodeWithoutLaunching(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	host := spawnTestLiveNode(t, clock, "sess-1", 55124)

	launched := false
	opts := spawnTestOptions(t, paths, clock, func(SpawnLaunchSpec) (int, error) {
		launched = true
		return 0, errors.New("must not launch")
	})
	result := Spawn(SpawnRequest{SessionID: "sess-1", Origin: "web"}, opts)

	if launched {
		t.Fatal("launcher was called although a live node exists")
	}
	if result.Status != SpawnStatusReused {
		t.Fatalf("status = %q (reason %q), want %q", result.Status, result.Reason, SpawnStatusReused)
	}
	if result.NodeID != host.NodeID() {
		t.Fatalf("node_id = %q, want %q", result.NodeID, host.NodeID())
	}
	if result.Port != 55124 {
		t.Fatalf("port = %d, want 55124", result.Port)
	}
	if !strings.Contains(result.URL, "token=node-token-abcdef0123456789") {
		t.Fatalf("url %q must carry the node record token", result.URL)
	}
	if !strings.Contains(result.URL, "session=sess-1") {
		t.Fatalf("url %q must carry the session deep link", result.URL)
	}
}

func TestSpawnStaleNodeIsNotReused(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	// A live pid but an ancient heartbeat: the record must not count as
	// "running" (§7.5), so spawn still starts a node.
	writeSpawnTestRecord(t, paths, clock, "node-stale", "sess-stale", os.Getpid(), 55125)
	clock.Advance(2 * DefaultHeartbeatTTL)

	opts := spawnTestOptions(t, paths, clock, func(spec SpawnLaunchSpec) (int, error) {
		writeSpawnTestRecord(t, paths, clock, "node-fresh", "sess-stale", os.Getpid(), 55126)
		return 999, nil
	})
	result := Spawn(SpawnRequest{SessionID: "sess-stale"}, opts)

	if result.Status != SpawnStatusStarted {
		t.Fatalf("status = %q (reason %q), want %q", result.Status, result.Reason, SpawnStatusStarted)
	}
	// The node record is the authority for the pid (it is the child's own
	// report), while the port comes from its endpoint.
	if result.Port != 55126 || result.PID != os.Getpid() {
		t.Fatalf("pid/port = %d/%d, want %d/55126", result.PID, result.Port, os.Getpid())
	}
}

// ---------------------------------------------------------------------------
// start
// ---------------------------------------------------------------------------

func TestSpawnStartsDetachedNode(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	var captured SpawnLaunchSpec
	opts := spawnTestOptions(t, paths, clock, func(spec SpawnLaunchSpec) (int, error) {
		captured = spec
		writeSpawnTestRecord(t, paths, clock, "node-child", "sess-2", os.Getpid(), 56001)
		return 777, nil
	})
	result := Spawn(SpawnRequest{SessionID: "sess-2", Origin: "web"}, opts)

	if result.Status != SpawnStatusStarted {
		t.Fatalf("status = %q (reason %q), want %q", result.Status, result.Reason, SpawnStatusStarted)
	}
	if result.PID != os.Getpid() || result.NodeID != "node-child" || result.Port != 56001 {
		t.Fatalf("pid/node/port = %d/%s/%d, want %d/node-child/56001", result.PID, result.NodeID, result.Port, os.Getpid())
	}
	if !strings.Contains(result.URL, "session=sess-2") || !strings.Contains(result.URL, "token=child-token-0123456789abcd") {
		t.Fatalf("url = %q, want session deep link + record token", result.URL)
	}
	// §5.7 command line, and the parent handshake env of §3.1.
	wantArgs := []string{"resume", "sess-2", "--pprof", "--web-host", "127.0.0.1", "--web-token", spawnTestToken}
	if strings.Join(captured.Args, " ") != strings.Join(wantArgs, " ") {
		t.Fatalf("args = %v, want %v", captured.Args, wantArgs)
	}
	if !containsEnv(captured.Env, spawnEnvSpawnedBy+"=node-spawner") {
		t.Fatalf("env %v must announce the spawning node", captured.Env)
	}
	if captured.Executable != "aicli-test" {
		t.Fatalf("executable = %q", captured.Executable)
	}
	if captured.LogPath == "" || !strings.Contains(captured.LogPath, "mesh-spawn-sess-2.log") {
		t.Fatalf("log path = %q", captured.LogPath)
	}
}

func TestSpawnUsesBindingWorkspaceAndLeavesPortToChild(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	workspace := t.TempDir()
	if err := TouchBinding(paths, BindingUpdate{
		SessionID:     "sess-3",
		Host:          "127.0.0.1",
		Port:          55130,
		NodeID:        "node-previous",
		WorkspacePath: workspace,
	}); err != nil {
		t.Fatalf("TouchBinding: %v", err)
	}

	var captured SpawnLaunchSpec
	opts := spawnTestOptions(t, paths, clock, func(spec SpawnLaunchSpec) (int, error) {
		captured = spec
		writeSpawnTestRecord(t, paths, clock, "node-child", "sess-3", os.Getpid(), 55130)
		return 888, nil
	})
	opts.WorkspaceFor = nil // exercise the binding fallback
	result := Spawn(SpawnRequest{SessionID: "sess-3"}, opts)

	if result.Status != SpawnStatusStarted {
		t.Fatalf("status = %q (reason %q)", result.Status, result.Reason)
	}
	// 绑定端口不由父进程钉成显式 --web-port：子进程自己按 S3 粘性端口复用，
	// 被占时回退随机（显式端口会让 bind 冲突变成致命启动错误）。
	for _, arg := range captured.Args {
		if arg == "--web-port" {
			t.Fatalf("args = %v, 不得把 binding 端口写成显式 --web-port", captured.Args)
		}
	}
	if captured.Dir != workspace {
		t.Fatalf("dir = %q, want the binding workspace %q", captured.Dir, workspace)
	}
}

// TestSpawnDoesNotPinOccupiedBindingPort 回归线上 504 现场：binding 端口被活节点
// 占用（这里用真实 listener 模拟——线上占用者通常是刚服务过该会话、随后切到别
// 的会话但仍监听同一端口的父节点）时，spawn 仍不得把它写成显式 --web-port，
// 否则子进程 bind 失败即退出，父进程等满预算后只能报 not_running（HTTP 504）。
func TestSpawnDoesNotPinOccupiedBindingPort(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	workspace := t.TempDir()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	occupied := listener.Addr().(*net.TCPAddr).Port

	if err := TouchBinding(paths, BindingUpdate{
		SessionID:     "sess-occupied",
		Host:          "127.0.0.1",
		Port:          occupied,
		NodeID:        "node-holder",
		WorkspacePath: workspace,
	}); err != nil {
		t.Fatalf("TouchBinding: %v", err)
	}

	var captured SpawnLaunchSpec
	opts := spawnTestOptions(t, paths, clock, func(spec SpawnLaunchSpec) (int, error) {
		captured = spec
		writeSpawnTestRecord(t, paths, clock, "node-child", "sess-occupied", os.Getpid(), 55131)
		return 889, nil
	})
	opts.WorkspaceFor = nil
	result := Spawn(SpawnRequest{SessionID: "sess-occupied"}, opts)

	if result.Status != SpawnStatusStarted {
		t.Fatalf("status = %q (reason %q)", result.Status, result.Reason)
	}
	for _, arg := range captured.Args {
		if arg == "--web-port" {
			t.Fatalf("绑定端口 %d 已被占用，args = %v 不得把它钉成显式 --web-port", occupied, captured.Args)
		}
	}
	if !containsArgPair(captured.Args, "--web-host", "127.0.0.1") {
		t.Fatalf("其余启动参数不得改变: %v", captured.Args)
	}
}

func TestSpawnFireAndForgetSkipsReadinessWait(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	opts := spawnTestOptions(t, paths, clock, func(SpawnLaunchSpec) (int, error) { return 1234, nil })
	opts.FireAndForget = true
	opts.Wait = 20 * time.Millisecond
	result := Spawn(SpawnRequest{SessionID: "sess-nw"}, opts)

	if result.Status != SpawnStatusStarted {
		t.Fatalf("status = %q (reason %q), want %q", result.Status, result.Reason, SpawnStatusStarted)
	}
	if result.PID != 1234 {
		t.Fatalf("pid = %d, want 1234", result.PID)
	}
	if !strings.Contains(result.Reason, "--no-wait") {
		t.Fatalf("reason = %q, want a --no-wait hint", result.Reason)
	}
}

// ---------------------------------------------------------------------------
// failure states
// ---------------------------------------------------------------------------

func TestSpawnLaunchFailureCarriesRedactedLogTail(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	opts := spawnTestOptions(t, paths, clock, func(SpawnLaunchSpec) (int, error) {
		return 0, errors.New("exec: executable file not found in %PATH%")
	})
	logPath := filepath.Join(opts.LogDir, spawnLogPrefix+"sess-4"+spawnLogSuffix)
	body := "Info: aicli resume sess-4 --pprof --web-token " + spawnTestToken + "\nboom: cannot open config\n"
	if err := os.WriteFile(logPath, []byte(body), SpawnLogFilePerm); err != nil {
		t.Fatalf("write log: %v", err)
	}
	result := Spawn(SpawnRequest{SessionID: "sess-4"}, opts)

	if result.Status != SpawnStatusFailed {
		t.Fatalf("status = %q, want %q", result.Status, SpawnStatusFailed)
	}
	if !strings.Contains(result.Reason, "not found") {
		t.Fatalf("reason = %q, want the launch error", result.Reason)
	}
	if len(result.LogTail) == 0 {
		t.Fatal("log_tail is empty, want the failure tail")
	}
	tail := strings.Join(result.LogTail, "\n")
	if strings.Contains(tail, spawnTestToken) {
		t.Fatalf("log_tail leaked the token: %q", tail)
	}
	if !strings.Contains(tail, HintToken(spawnTestToken)) {
		t.Fatalf("log_tail %q should carry the redaction hint %q", tail, HintToken(spawnTestToken))
	}
}

func TestSpawnNotRunningAfterWaitBudget(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	opts := spawnTestOptions(t, paths, clock, func(SpawnLaunchSpec) (int, error) { return 4321, nil })
	opts.Wait = 120 * time.Millisecond
	logPath := filepath.Join(opts.LogDir, spawnLogPrefix+"sess-5"+spawnLogSuffix)
	if err := os.WriteFile(logPath, []byte("starting up, no record yet\n"), SpawnLogFilePerm); err != nil {
		t.Fatalf("write log: %v", err)
	}
	result := Spawn(SpawnRequest{SessionID: "sess-5"}, opts)

	if result.Status != SpawnStatusNotRunning {
		t.Fatalf("status = %q (reason %q), want %q", result.Status, result.Reason, SpawnStatusNotRunning)
	}
	if result.PID != 4321 {
		t.Fatalf("pid = %d, want the launched pid 4321", result.PID)
	}
	if !strings.Contains(result.Reason, "no live node") {
		t.Fatalf("reason = %q", result.Reason)
	}
	if len(result.LogTail) == 0 {
		t.Fatal("log_tail is empty, want the launch log tail")
	}
}

func TestSpawnSingleFlightDoesNotLaunchTwice(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	// Somebody else holds the spawn lease for this session.
	outcome := Acquire(paths, LeasePurposeSpawn, "sess-6", LeaseOwner{NodeID: "node-other", PID: os.Getpid()}, AcquireOptions{Now: clock.Now()})
	if !outcome.Acquired {
		t.Fatalf("pre-acquire failed: %+v", outcome)
	}
	launched := false
	opts := spawnTestOptions(t, paths, clock, func(SpawnLaunchSpec) (int, error) {
		launched = true
		return 0, errors.New("must not launch")
	})
	opts.Wait = 100 * time.Millisecond
	result := Spawn(SpawnRequest{SessionID: "sess-6"}, opts)

	if launched {
		t.Fatal("single flight violated: a second process was launched")
	}
	if result.Status != SpawnStatusNotRunning {
		t.Fatalf("status = %q, want %q", result.Status, SpawnStatusNotRunning)
	}
	if !strings.Contains(result.Reason, "another process is starting") {
		t.Fatalf("reason = %q", result.Reason)
	}
	if result.Lease == "" {
		t.Fatal("lease outcome must be reported")
	}
}

func TestSpawnMissingWorkspaceFails(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	launched := false
	opts := spawnTestOptions(t, paths, clock, func(SpawnLaunchSpec) (int, error) {
		launched = true
		return 0, errors.New("must not launch")
	})
	opts.WorkspaceFor = func(string) (string, bool) {
		return filepath.Join(t.TempDir(), "gone"), true
	}
	result := Spawn(SpawnRequest{SessionID: "sess-7"}, opts)

	if launched {
		t.Fatal("launcher ran with a missing workspace")
	}
	if result.Status != SpawnStatusFailed {
		t.Fatalf("status = %q, want %q", result.Status, SpawnStatusFailed)
	}
	if !strings.Contains(result.Reason, "workspace directory not found") {
		t.Fatalf("reason = %q", result.Reason)
	}
}

func TestSpawnWithoutMeshRootIsFailClosed(t *testing.T) {
	clock := newFakeClock()
	launched := false
	opts := spawnTestOptions(t, Paths{Source: PathSourceUnset}, clock, func(SpawnLaunchSpec) (int, error) {
		launched = true
		return 0, errors.New("must not launch")
	})
	result := Spawn(SpawnRequest{SessionID: "sess-8"}, opts)

	if launched {
		t.Fatal("launcher ran without a mesh root")
	}
	if result.Status != SpawnStatusFailed {
		t.Fatalf("status = %q, want %q", result.Status, SpawnStatusFailed)
	}
	if !strings.Contains(result.Reason, "mesh root unavailable") {
		t.Fatalf("reason = %q", result.Reason)
	}
}

func TestSpawnRejectsEmptySessionID(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	opts := spawnTestOptions(t, paths, clock, func(SpawnLaunchSpec) (int, error) {
		t.Fatal("launcher must not run for an invalid session id")
		return 0, nil
	})
	result := Spawn(SpawnRequest{SessionID: "   "}, opts)

	if result.Status != SpawnStatusFailed || !strings.Contains(result.Reason, "invalid session_id") {
		t.Fatalf("result = %+v, want a failed/invalid session_id", result)
	}
}

// TestSpawnLogPathUsesSanitizedKey pins the file-name rule: a session id with
// separators can never escape the log directory (SanitizeKey is the same rule
// the lease and binding files use, so all three agree on one key).
func TestSpawnLogPathUsesSanitizedKey(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	dir := t.TempDir()
	opts := spawnTestOptions(t, paths, clock, func(SpawnLaunchSpec) (int, error) {
		// The child "starts" but never publishes a record: the wait must lapse.
		return 111, nil
	})
	opts.LogDir = dir
	opts.Wait = 20 * time.Millisecond
	result := Spawn(SpawnRequest{SessionID: "../../escape"}, opts)

	logPath := opts.logPath("../../escape")
	if filepath.Dir(logPath) != dir {
		t.Fatalf("log path %q escaped the log dir %q", logPath, dir)
	}
	if !strings.Contains(filepath.Base(logPath), ".._.._escape") {
		t.Fatalf("log base %q, want the sanitized key", filepath.Base(logPath))
	}
	if result.Status != SpawnStatusNotRunning {
		t.Fatalf("status = %q (reason %q), want %q", result.Status, result.Reason, SpawnStatusNotRunning)
	}
}

// ---------------------------------------------------------------------------
// token hygiene (M7) + small units
// ---------------------------------------------------------------------------

func TestSpawnJournalNeverHoldsTheToken(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	host := NewHost(HostConfig{Kind: "chat", Origin: "web", Exe: "aicli.exe", Now: clock.Now})
	if err := host.Start(); err != nil {
		t.Fatalf("host.Start: %v", err)
	}
	t.Cleanup(host.Close)
	host.Journal().SetSecrets(spawnTestToken)

	opts := spawnTestOptions(t, paths, clock, func(SpawnLaunchSpec) (int, error) {
		writeSpawnTestRecord(t, paths, clock, "node-child", "sess-9", os.Getpid(), 55140)
		return 555, nil
	})
	opts.Journal = host.Journal()
	result := Spawn(SpawnRequest{SessionID: "sess-9", Origin: "web"}, opts)
	if result.Status != SpawnStatusStarted {
		t.Fatalf("status = %q (reason %q)", result.Status, result.Reason)
	}

	raw, err := os.ReadFile(paths.JournalPath(host.NodeID()))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if strings.Contains(string(raw), spawnTestToken) {
		t.Fatalf("journal leaked the token:\n%s", raw)
	}
	kinds := journalKinds(t, paths, host.NodeID())
	if countKind(kinds, JournalSpawnRequested) != 1 || countKind(kinds, JournalSpawnCompleted) != 1 {
		t.Fatalf("journal kinds = %v, want one requested + one completed", kinds)
	}
	// The returned URL is the one place a token原文 is allowed (M7), and after a
	// real start it is the *node record's* token (the child's own report), not
	// the one we put on its command line.
	if !strings.Contains(result.URL, "token=child-token-0123456789abcd") {
		t.Fatalf("url = %q, want the record token for the browser hand-off", result.URL)
	}
	if !strings.Contains(result.URL, "session=sess-9") {
		t.Fatalf("url = %q, want the session deep link", result.URL)
	}
}

func TestSpawnArgsOmitZeroPort(t *testing.T) {
	cases := []struct {
		name     string
		port     int
		token    string
		want     []string
		wantPort bool
	}{
		{
			name:  "random port",
			port:  0,
			token: "tok-1234567890abcdef",
			want:  []string{"resume", "sid", "--pprof", "--web-host", "127.0.0.1", "--web-token", "tok-1234567890abcdef"},
		},
		{
			name:  "sticky port",
			port:  55124,
			token: "tok-1234567890abcdef",
			want:  []string{"resume", "sid", "--pprof", "--web-host", "127.0.0.1", "--web-port", "55124", "--web-token", "tok-1234567890abcdef"},
		},
		{
			name:  "no token (dev mode)",
			port:  0,
			token: "",
			want:  []string{"resume", "sid", "--pprof", "--web-host", "127.0.0.1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := spawnArgs("sid", tc.port, tc.token)
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("args = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReadSpawnLogTailKeepsWholeLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.log")
	var builder strings.Builder
	for i := 0; i < 4000; i++ {
		builder.WriteString("line-")
		builder.WriteString(strings.Repeat("x", 32))
		builder.WriteString("\n")
	}
	builder.WriteString("tail-a\n")
	builder.WriteString("tail-b\n")
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	tail := readSpawnLogTail(path, 2)
	if len(tail) != 2 || tail[0] != "tail-a" || tail[1] != "tail-b" {
		t.Fatalf("tail = %v, want the last two whole lines", tail)
	}
	if missing := readSpawnLogTail(filepath.Join(dir, "absent.log"), 5); missing != nil {
		t.Fatalf("missing log tail = %v, want nil", missing)
	}
	if empty := readSpawnLogTail(path, 0); empty != nil {
		t.Fatalf("zero-line tail = %v, want nil", empty)
	}
}

func TestRedactSpawnTailIsIdempotent(t *testing.T) {
	lines := []string{"cmd --web-token " + spawnTestToken, "clean line"}
	once := redactSpawnTail(lines, spawnTestToken)
	twice := redactSpawnTail(once, spawnTestToken)
	if strings.Join(once, "\n") != strings.Join(twice, "\n") {
		t.Fatalf("second redaction changed the text: %v -> %v", once, twice)
	}
	if strings.Contains(strings.Join(once, "\n"), spawnTestToken) {
		t.Fatalf("redaction left the token: %v", once)
	}
	if redactSpawnTail(lines, "")[0] != lines[0] {
		t.Fatal("an empty token must leave the lines untouched")
	}
}

func TestNewSpawnTokenIsHexAndLongEnough(t *testing.T) {
	token := NewSpawnToken()
	if len(token) != SpawnTokenBytes*2 {
		t.Fatalf("token length = %d, want %d", len(token), SpawnTokenBytes*2)
	}
	for _, r := range token {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("token %q is not lowercase hex", token)
		}
	}
	if NewSpawnToken() == token {
		t.Fatal("two tokens are identical; the RNG is not being consulted")
	}
}

// ---------------------------------------------------------------------------
// spawn executable resolution (AICLI_BIN / self / sibling / PATH)
// ---------------------------------------------------------------------------

// spawnTestFileInfo is the minimal os.FileInfo the stat seam hands back.
type spawnTestFileInfo struct {
	name string
	dir  bool
}

func (f spawnTestFileInfo) Name() string       { return f.name }
func (f spawnTestFileInfo) Size() int64        { return 1 }
func (f spawnTestFileInfo) Mode() os.FileMode  { return 0o755 }
func (f spawnTestFileInfo) ModTime() time.Time { return time.Time{} }
func (f spawnTestFileInfo) IsDir() bool        { return f.dir }
func (f spawnTestFileInfo) Sys() any           { return nil }

// stubSpawnExecutableSeams replaces the process-level inputs of the resolution
// (our own path, stat, PATH); without this the four rules are untestable.
func stubSpawnExecutableSeams(t *testing.T, exe string, files, dirs []string, pathHit string) {
	t.Helper()
	oldExe, oldStat, oldLook := spawnExecutablePath, spawnStatFile, spawnLookPath
	t.Cleanup(func() { spawnExecutablePath, spawnStatFile, spawnLookPath = oldExe, oldStat, oldLook })

	fs := make(map[string]bool, len(files)+len(dirs))
	for _, name := range files {
		fs[name] = false
	}
	for _, name := range dirs {
		fs[name] = true
	}
	spawnExecutablePath = func() (string, error) { return exe, nil }
	spawnStatFile = func(name string) (os.FileInfo, error) {
		isDir, ok := fs[name]
		if !ok {
			return nil, os.ErrNotExist
		}
		return spawnTestFileInfo{name: filepath.Base(name), dir: isDir}, nil
	}
	spawnLookPath = func(string) (string, error) {
		if pathHit == "" {
			return "", os.ErrNotExist
		}
		return pathHit, nil
	}
}

// TestResolveSpawnExecutableRules 锁定四条规则的顺序与来源标签：
// AICLI_BIN → 自身（仅当就叫 aicli）→ 同目录 aicli<ext> → PATH。
func TestResolveSpawnExecutableRules(t *testing.T) {
	root := t.TempDir()
	override := filepath.Join(root, "other", "aicli-2x.exe")
	selfAICLI := filepath.Join(root, "tools", "aicli", "aicli.exe")
	selfRenamed := filepath.Join(root, "tools", "aicli-2x", "aicli-2x.exe")
	sibling := filepath.Join(root, "tools", "aicli-2x", "aicli.exe")
	pathHit := filepath.Join(root, "path", "aicli.exe")

	cases := []struct {
		name     string
		override string
		self     string
		files    []string
		dirs     []string
		pathHit  string
		wantPath string
		wantSrc  string
		wantErr  bool
	}{
		{
			name:     "AICLI_BIN wins over self/sibling/PATH",
			override: override,
			self:     selfRenamed,
			files:    []string{override, sibling},
			pathHit:  pathHit,
			wantPath: override,
			wantSrc:  SpawnExecutableSourceEnv,
		},
		{
			name:     "a broken AICLI_BIN never falls back to self/sibling/PATH",
			override: filepath.Join(root, "other", "absent.exe"),
			self:     selfRenamed,
			files:    []string{sibling},
			pathHit:  pathHit,
			wantErr:  true,
		},
		{
			name:     "an AICLI_BIN pointing at a directory is refused",
			override: filepath.Join(root, "other"),
			self:     selfRenamed,
			dirs:     []string{filepath.Join(root, "other")},
			files:    []string{sibling},
			pathHit:  pathHit,
			wantErr:  true,
		},
		{
			name:     "self when the executable already is aicli",
			self:     selfAICLI,
			wantPath: selfAICLI,
			wantSrc:  SpawnExecutableSourceSelf,
		},
		{
			name:     "a renamed binary (aicli-2x.exe) uses the co-located aicli.exe",
			self:     selfRenamed,
			files:    []string{sibling},
			pathHit:  pathHit,
			wantPath: sibling,
			wantSrc:  SpawnExecutableSourceSibling,
		},
		{
			name:     "a renamed binary without a sibling falls back to PATH",
			self:     selfRenamed,
			pathHit:  pathHit,
			wantPath: pathHit,
			wantSrc:  SpawnExecutableSourcePath,
		},
		{
			name: "nothing found is not an error, just an empty resolution",
			self: selfRenamed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubSpawnExecutableSeams(t, tc.self, tc.files, tc.dirs, tc.pathHit)
			t.Setenv(SpawnExecutableEnv, tc.override)
			got, err := ResolveSpawnExecutable()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolution = %+v, want an error for the broken override", got)
				}
				if !strings.Contains(err.Error(), SpawnExecutableEnv) {
					t.Fatalf("error %q should name %s", err, SpawnExecutableEnv)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveSpawnExecutable: %v", err)
			}
			if got.Path != tc.wantPath || got.Source != tc.wantSrc {
				t.Fatalf("resolution = %+v, want path %q source %q", got, tc.wantPath, tc.wantSrc)
			}
		})
	}
}

// TestSpawnFailsLoudlyOnBrokenBinOverride：AICLI_BIN 指错时 Spawn 必须失败并报
// mesh_spawn_bin_unavailable，且**不启动任何进程**（绝不悄悄换一个二进制）。
func TestSpawnFailsLoudlyOnBrokenBinOverride(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	t.Setenv(SpawnExecutableEnv, filepath.Join(t.TempDir(), "absent.exe"))

	launched := false
	opts := spawnTestOptions(t, paths, clock, func(SpawnLaunchSpec) (int, error) {
		launched = true
		return 4242, nil
	})
	opts.Executable = "" // 走解析路径（helper 默认注入假二进制）

	result := Spawn(SpawnRequest{SessionID: "session_bin"}, opts)
	if launched {
		t.Fatal("a broken AICLI_BIN must not launch anything")
	}
	if result.Status != SpawnStatusFailed || result.Code != SpawnCodeBinUnavailable {
		t.Fatalf("result = %+v, want failed/%s", result, SpawnCodeBinUnavailable)
	}
	if !strings.Contains(result.Reason, SpawnExecutableEnv) {
		t.Fatalf("reason %q should name %s", result.Reason, SpawnExecutableEnv)
	}
}

// ---------------------------------------------------------------------------
// small assertion helpers
// ---------------------------------------------------------------------------

func containsEnv(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

func containsArgPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
