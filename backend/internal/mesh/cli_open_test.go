package mesh

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// open (§5.7 的本地写法)
// ---------------------------------------------------------------------------

func TestCLIOpenReusesLiveNode(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	cli := testCLI(paths, clock)
	seedLiveNode(t, paths, now, "node-open-1", "session_open", `E:\ws\open`)

	code, stdout, stderr := runCLI(t, cli, "open", "session_open", "--json")
	if code != ExitOK {
		t.Fatalf("open exit = %d (stderr %q)", code, stderr)
	}
	result := decodeJSON[openResult](t, stdout)
	if result.Status != SpawnStatusReused {
		t.Fatalf("status = %q, want reused（已有活节点不得再拉一个）", result.Status)
	}
	if result.NodeID != "node-open-1" {
		t.Fatalf("node_id = %q, want node-open-1", result.NodeID)
	}
	if !strings.Contains(result.URL, "token=0f3a-secret-token") {
		t.Fatalf("url 必须带目标档案里的令牌: %q", result.URL)
	}
	if !strings.Contains(result.URL, "session=session_open") {
		t.Fatalf("url 必须带会话深链: %q", result.URL)
	}
	if result.SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", result.SchemaVersion, SchemaVersion)
	}
}

func TestCLIOpenHumanOutputPrintsURL(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	cli := testCLI(paths, clock)
	seedLiveNode(t, paths, clock.Now(), "node-open-2", "session_open2", `E:\ws\open2`)

	code, stdout, stderr := runCLI(t, cli, "open", "session_open2")
	if code != ExitOK {
		t.Fatalf("open exit = %d (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "复用节点 node-open-2") {
		t.Fatalf("人读输出应说明复用了哪个节点:\n%s", stdout)
	}
	if !strings.Contains(stdout, "/web?token=") {
		t.Fatalf("人读输出必须给出窗口 URL:\n%s", stdout)
	}
}

// TestCLIOpenTranslatesRequest 验证 CLI 到 Spawn 的翻译：会话解析、审计标签、
// 合成租约 owner（CLI 不是节点）、以及 --wait / --no-wait / --port 的落点。
func TestCLIOpenTranslatesRequest(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	cli := testCLI(paths, clock)

	var gotReq SpawnRequest
	var gotOpts SpawnOptions
	cli.Spawn = func(req SpawnRequest, opts SpawnOptions) SpawnResult {
		gotReq, gotOpts = req, opts
		return SpawnResult{Status: SpawnStatusStarted, SessionID: req.SessionID, NodeID: "node-new", URL: "http://127.0.0.1:1/web"}
	}

	code, stdout, stderr := runCLI(t, cli, "open", "session_fresh", "--port", "55130", "--wait", "2500ms", "--no-wait")
	if code != ExitOK {
		t.Fatalf("open exit = %d (stderr %q)", code, stderr)
	}
	if gotReq.SessionID != "session_fresh" || gotReq.Port != 55130 || gotReq.WaitMS != 2500 {
		t.Fatalf("req = %+v, want session_fresh/55130/2500", gotReq)
	}
	if gotReq.Origin != "cli" {
		t.Fatalf("origin = %q, want cli（审计标签区分 CLI 与 web）", gotReq.Origin)
	}
	if gotOpts.Wait != 2500*time.Millisecond {
		t.Fatalf("wait = %s, want 2.5s", gotOpts.Wait)
	}
	if !gotOpts.FireAndForget {
		t.Fatal("--no-wait 必须变成 FireAndForget")
	}
	if gotOpts.Paths.Root != paths.Root {
		t.Fatalf("paths = %q, want %q", gotOpts.Paths.Root, paths.Root)
	}
	if !strings.HasPrefix(gotOpts.SelfNodeID, "cli-") {
		t.Fatalf("self node id = %q, want cli-<pid>（CLI 不是节点，不冒充节点身份）", gotOpts.SelfNodeID)
	}
	if gotOpts.PID <= 0 {
		t.Fatalf("pid = %d, want the CLI process pid", gotOpts.PID)
	}
	if !strings.Contains(stdout, "已拉起节点 node-new") {
		t.Fatalf("人读输出应说明拉起了节点:\n%s", stdout)
	}
}

// TestCLIOpenExitCodes 验证 §5.7 四态到 §7.3 退出码的映射。
func TestCLIOpenExitCodes(t *testing.T) {
	cases := []struct {
		status string
		want   int
	}{
		{SpawnStatusReused, ExitOK},
		{SpawnStatusStarted, ExitOK},
		{SpawnStatusNotRunning, ExitUnreachable},
		{SpawnStatusFailed, ExitFailure},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			paths := testCLIPaths(t)
			cli := testCLI(paths, newFakeClock())
			cli.Spawn = func(req SpawnRequest, _ SpawnOptions) SpawnResult {
				return SpawnResult{Status: tc.status, Code: SpawnCodeTimeout, SessionID: req.SessionID, Reason: "why " + tc.status}
			}
			code, _, stderr := runCLI(t, cli, "open", "session_x")
			if code != tc.want {
				t.Fatalf("exit = %d, want %d (%s)", code, tc.want, stderr)
			}
			if tc.want != ExitOK && !strings.Contains(stderr, "why "+tc.status) {
				t.Fatalf("失败原因必须给人看: %q", stderr)
			}
		})
	}
}

func TestCLIOpenArgumentErrors(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	cli := testCLI(paths, clock)
	cli.Spawn = func(SpawnRequest, SpawnOptions) SpawnResult {
		t.Fatal("非法参数不得触达 Spawn")
		return SpawnResult{}
	}

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no target", []string{"open"}, ExitUsage},
		{"two targets", []string{"open", "a", "b"}, ExitUsage},
		{"unknown flag", []string{"open", "s1", "--nope"}, ExitUsage},
		{"bad port", []string{"open", "s1", "--port", "70000"}, ExitUsage},
		{"non numeric port", []string{"open", "s1", "--port", "abc"}, ExitUsage},
		{"bad wait", []string{"open", "s1", "--wait", "soon"}, ExitUsage},
		{"path-like target", []string{"open", `..\evil`}, ExitNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, cli, tc.args...)
			if code != tc.want {
				t.Fatalf("exit = %d, want %d (%s)", code, tc.want, stderr)
			}
		})
	}
}

// TestCLIOpenFailClosedWithoutMeshRoot：网格根不可用时绝不猜目录（fail-closed，
// 与 lease/gc 同一条规则）。
func TestCLIOpenFailClosedWithoutMeshRoot(t *testing.T) {
	cli := &CLI{Version: "test-version", Paths: &Paths{}}
	cli.Spawn = func(SpawnRequest, SpawnOptions) SpawnResult {
		t.Fatal("网格根不可用时不得拉起")
		return SpawnResult{}
	}
	code, _, stderr := runCLI(t, cli, "open", "session_x")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d (%s)", code, ExitFailure, stderr)
	}
	if !strings.Contains(stderr, "网格根目录不可用") {
		t.Fatalf("应说明网格根不可用: %q", stderr)
	}
}

func TestCLIOpenHelp(t *testing.T) {
	paths := testCLIPaths(t)
	cli := testCLI(paths, newFakeClock())
	code, stdout, _ := runCLI(t, cli, "open", "--help")
	if code != ExitOK {
		t.Fatalf("open --help exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "aicli-mesh open") {
		t.Fatalf("用法里应有 open:\n%s", stdout)
	}
}

// TestCLIOpenTakeoverFlag 锁定 S15 的 CLI 入口：--takeover 透传到
// SpawnRequest.Takeover（接管语义由 Spawn 与子进程负责），人读输出说明
// 「旧节点继续运行」，且不带开关时保持零值（绝不顺手抢租约）。
func TestCLIOpenTakeoverFlag(t *testing.T) {
	paths := testCLIPaths(t)
	cli := testCLI(paths, newFakeClock())

	var gotReq SpawnRequest
	cli.Spawn = func(req SpawnRequest, _ SpawnOptions) SpawnResult {
		gotReq = req
		return SpawnResult{Status: SpawnStatusStarted, SessionID: req.SessionID, NodeID: "node-new"}
	}

	code, stdout, stderr := runCLI(t, cli, "open", "session_takeover", "--takeover")
	if code != ExitOK {
		t.Fatalf("open --takeover exit = %d (stderr %q)", code, stderr)
	}
	if !gotReq.Takeover {
		t.Fatalf("--takeover 必须透传 SpawnRequest.Takeover: %+v", gotReq)
	}
	if !strings.Contains(stdout, "接管") || !strings.Contains(stdout, "旧节点继续运行") {
		t.Fatalf("人读输出应说明接管语义:\n%s", stdout)
	}

	code, _, stderr = runCLI(t, cli, "open", "session_plain")
	if code != ExitOK {
		t.Fatalf("open exit = %d (stderr %q)", code, stderr)
	}
	if gotReq.Takeover {
		t.Fatalf("不带 --takeover 时不得置位: %+v", gotReq)
	}

	if _, usage, _ := runCLI(t, cli, "open", "--help"); !strings.Contains(usage, "--takeover") {
		t.Fatalf("用法里应列出 --takeover:\n%s", usage)
	}
}
