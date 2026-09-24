package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// new（CLI）—— 新建会话
// ---------------------------------------------------------------------------

// TestCLINewTranslatesRequest 验证 CLI 到 Spawn 的翻译：new_session 标记、审计
// 标签、工作区（--workspace 解析成绝对路径）、以及 --wait / --no-wait / --port。
func TestCLINewTranslatesRequest(t *testing.T) {
	paths := testCLIPaths(t)
	cli := testCLI(paths, newFakeClock())
	workspace := t.TempDir()

	var gotReq SpawnRequest
	var gotOpts SpawnOptions
	cli.Spawn = func(req SpawnRequest, opts SpawnOptions) SpawnResult {
		gotReq, gotOpts = req, opts
		return SpawnResult{
			Status:    SpawnStatusStarted,
			SessionID: "session_brand_new",
			NodeID:    "node-new",
			PID:       4321,
			Port:      56110,
			URL:       "http://127.0.0.1:56110/web?token=tok&session=session_brand_new",
		}
	}

	code, stdout, stderr := runCLI(t, cli, "new", "--workspace", workspace, "--port", "56110", "--wait", "2500ms")
	if code != ExitOK {
		t.Fatalf("new exit = %d (stderr %q)", code, stderr)
	}
	if !gotReq.NewSession {
		t.Fatal("new 必须置位 NewSession（子进程据此新建会话）")
	}
	if gotReq.SessionID != "" {
		t.Fatalf("session = %q, want empty: 新会话的 ID 由子进程生成", gotReq.SessionID)
	}
	if gotReq.Port != 56110 || gotReq.WaitMS != 2500 || gotReq.Origin != "cli" {
		t.Fatalf("req = %+v, want port 56110 / wait 2500 / origin cli", gotReq)
	}
	if gotOpts.Wait != 2500*time.Millisecond {
		t.Fatalf("wait = %s, want 2.5s", gotOpts.Wait)
	}
	if gotOpts.FireAndForget {
		t.Fatal("没有 --no-wait 时必须等就绪")
	}
	if gotOpts.Paths.Root != paths.Root {
		t.Fatalf("paths = %q, want %q", gotOpts.Paths.Root, paths.Root)
	}
	if !strings.HasPrefix(gotOpts.SelfNodeID, "cli-") {
		t.Fatalf("self node id = %q, want cli-<pid>（CLI 不是节点）", gotOpts.SelfNodeID)
	}
	// 新会话没有绑定可查：工作区由 CLI 明确给出（绝对路径，档案记的是子进程 cwd）。
	dir, ok := gotOpts.WorkspaceFor("")
	if !ok {
		t.Fatal("WorkspaceFor 必须给出工作区（新会话没有绑定兜底）")
	}
	wantAbs, err := filepath.Abs(workspace)
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if dir != wantAbs {
		t.Fatalf("workspace = %q, want %q", dir, wantAbs)
	}
	if !strings.Contains(stdout, "新会话 session_brand_new 已就绪") {
		t.Fatalf("人读输出应说明新会话:\n%s", stdout)
	}
	if !strings.Contains(stdout, "工作区："+wantAbs) {
		t.Fatalf("人读输出应给出工作区:\n%s", stdout)
	}
	if !strings.Contains(stdout, "/web?token=tok&session=session_brand_new") {
		t.Fatalf("人读输出必须给出窗口 URL:\n%s", stdout)
	}
}

// TestCLINewJSONShape：--json 与 open 同形状（§5.7 的 SpawnResult），外加
// workspace——脚本要靠它知道新会话落在哪个目录。
func TestCLINewJSONShape(t *testing.T) {
	paths := testCLIPaths(t)
	cli := testCLI(paths, newFakeClock())
	workspace := t.TempDir()
	cli.Spawn = func(SpawnRequest, SpawnOptions) SpawnResult {
		return SpawnResult{
			Status:    SpawnStatusStarted,
			SessionID: "session_json_new",
			NodeID:    "node-new",
			PID:       4322,
			Port:      56111,
			URL:       "http://127.0.0.1:56111/web?token=tok&session=session_json_new",
		}
	}

	code, stdout, stderr := runCLI(t, cli, "new", "--workspace", workspace, "--json")
	if code != ExitOK {
		t.Fatalf("new --json exit = %d (stderr %q)", code, stderr)
	}
	result := decodeJSON[newResult](t, stdout)
	if result.SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", result.SchemaVersion, SchemaVersion)
	}
	if result.Status != SpawnStatusStarted || result.SessionID != "session_json_new" {
		t.Fatalf("status/session = %q/%q", result.Status, result.SessionID)
	}
	wantAbs, _ := filepath.Abs(workspace)
	if result.Workspace != wantAbs {
		t.Fatalf("workspace = %q, want %q", result.Workspace, wantAbs)
	}
	if result.SpawnResult == nil || result.NodeID != "node-new" {
		t.Fatalf("嵌套的 SpawnResult 必须照常展开: %s", stdout)
	}
}

// TestCLINewDefaultWorkspaceIsCWD：不给 --workspace 就是「在这里开一个新会话」。
func TestCLINewDefaultWorkspaceIsCWD(t *testing.T) {
	paths := testCLIPaths(t)
	cli := testCLI(paths, newFakeClock())
	var gotOpts SpawnOptions
	cli.Spawn = func(_ SpawnRequest, opts SpawnOptions) SpawnResult {
		gotOpts = opts
		return SpawnResult{Status: SpawnStatusStarted, SessionID: "session_cwd", URL: "http://127.0.0.1:1/web"}
	}

	code, _, stderr := runCLI(t, cli, "new")
	if code != ExitOK {
		t.Fatalf("new exit = %d (stderr %q)", code, stderr)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	wantAbs, _ := filepath.Abs(cwd)
	dir, ok := gotOpts.WorkspaceFor("")
	if !ok || dir != wantAbs {
		t.Fatalf("workspace = %q ok=%v, want the caller's cwd %q", dir, ok, wantAbs)
	}
}

// TestCLINewExitCodes 验证 §5.7 四态到 §7.3 退出码的映射（与 open 同一张表）。
func TestCLINewExitCodes(t *testing.T) {
	cases := []struct {
		status string
		want   int
	}{
		{SpawnStatusStarted, ExitOK},
		{SpawnStatusNotRunning, ExitUnreachable},
		{SpawnStatusFailed, ExitFailure},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			paths := testCLIPaths(t)
			cli := testCLI(paths, newFakeClock())
			cli.Spawn = func(SpawnRequest, SpawnOptions) SpawnResult {
				return SpawnResult{Status: tc.status, Code: SpawnCodeTimeout, Reason: "why " + tc.status}
			}
			code, _, stderr := runCLI(t, cli, "new")
			if code != tc.want {
				t.Fatalf("exit = %d, want %d (%s)", code, tc.want, stderr)
			}
			if tc.want != ExitOK && !strings.Contains(stderr, "why "+tc.status) {
				t.Fatalf("失败原因必须给人看: %q", stderr)
			}
		})
	}
}

// TestCLINewNoWaitSaysWhatIsUnknown：--no-wait 时不印一个空会话 ID，而是明说
// 「ID 待查」并指向 ls。
func TestCLINewNoWaitSaysWhatIsUnknown(t *testing.T) {
	paths := testCLIPaths(t)
	cli := testCLI(paths, newFakeClock())
	var gotOpts SpawnOptions
	cli.Spawn = func(_ SpawnRequest, opts SpawnOptions) SpawnResult {
		gotOpts = opts
		return SpawnResult{
			Status: SpawnStatusStarted,
			PID:    4323,
			Port:   56112,
			URL:    "http://127.0.0.1:56112/web?token=tok",
			Reason: "started pid 4323 without waiting for readiness (--no-wait); the child generates the session id — run `aicli-mesh ls` to find it",
		}
	}

	code, stdout, stderr := runCLI(t, cli, "new", "--no-wait")
	if code != ExitOK {
		t.Fatalf("new --no-wait exit = %d (stderr %q)", code, stderr)
	}
	if !gotOpts.FireAndForget {
		t.Fatal("--no-wait 必须变成 FireAndForget")
	}
	if !strings.Contains(stdout, "已启动新会话的节点进程") {
		t.Fatalf("人读输出应如实说明只启动了进程:\n%s", stdout)
	}
	if !strings.Contains(stderr, "aicli-mesh ls") {
		t.Fatalf("提示应指向 ls:\n%s", stderr)
	}
}

func TestCLINewArgumentErrors(t *testing.T) {
	paths := testCLIPaths(t)
	cli := testCLI(paths, newFakeClock())
	cli.Spawn = func(SpawnRequest, SpawnOptions) SpawnResult {
		t.Fatal("非法参数不得触达 Spawn")
		return SpawnResult{}
	}
	file := filepath.Join(t.TempDir(), "not-a-dir.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"positional", []string{"new", "session_x"}, ExitUsage},
		{"unknown flag", []string{"new", "--nope"}, ExitUsage},
		{"bad port", []string{"new", "--port", "70000"}, ExitUsage},
		{"non numeric port", []string{"new", "--port", "abc"}, ExitUsage},
		{"bad wait", []string{"new", "--wait", "soon"}, ExitUsage},
		{"missing bin", []string{"new", "--bin", filepath.Join(t.TempDir(), "absent.exe")}, ExitUsage},
		{"missing workspace", []string{"new", "--workspace", filepath.Join(t.TempDir(), "gone")}, ExitUsage},
		{"workspace is a file", []string{"new", "--workspace", file}, ExitUsage},
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

// TestCLINewFailClosedWithoutMeshRoot：网格根不可用时绝不猜目录（fail-closed）。
func TestCLINewFailClosedWithoutMeshRoot(t *testing.T) {
	cli := &CLI{Version: "test-version", Paths: &Paths{}}
	cli.Spawn = func(SpawnRequest, SpawnOptions) SpawnResult {
		t.Fatal("网格根不可用时不得拉起")
		return SpawnResult{}
	}
	code, _, stderr := runCLI(t, cli, "new")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d (%s)", code, ExitFailure, stderr)
	}
	if !strings.Contains(stderr, "网格根目录不可用") {
		t.Fatalf("应说明网格根不可用: %q", stderr)
	}
}

func TestCLINewHelp(t *testing.T) {
	paths := testCLIPaths(t)
	cli := testCLI(paths, newFakeClock())
	code, stdout, _ := runCLI(t, cli, "new", "--help")
	if code != ExitOK {
		t.Fatalf("new --help exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "aicli-mesh new") {
		t.Fatalf("用法里应有 new:\n%s", stdout)
	}
}
