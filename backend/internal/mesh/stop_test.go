package mesh

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ============================================================================
// stop 测试（S16）
//
// 靶进程用「重跑测试二进制 + 环境变量」拉起（跨平台，不依赖外部命令）：
// 它平时只是睡，测试通过 terminateProcess 或假目标的 /exit 回调让它退出。
// 这样 force / graceful 两条路都验证的是**真实进程消失**，而不是桩。
// ============================================================================

// TestMeshStopHelperProcess 是 stop 测试的靶进程本体：只有被 startStopTarget
// 拉起（带 MESH_STOP_HELPER=1）时才睡，正常 `go test` 下直接跳过。
func TestMeshStopHelperProcess(t *testing.T) {
	if os.Getenv("MESH_STOP_HELPER") != "1" {
		t.Skip("helper process only")
	}
	time.Sleep(60 * time.Second)
}

// stopTarget 是一个可控的靶进程。
type stopTarget struct {
	pid int
}

// startStopTarget 拉起靶进程并登记清理（清理时无条件终止，避免泄漏）。
func startStopTarget(t *testing.T) *stopTarget {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMeshStopHelperProcess$")
	cmd.Env = append(os.Environ(), "MESH_STOP_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	target := &stopTarget{pid: cmd.Process.Pid}
	t.Cleanup(func() {
		_ = terminateProcess(target.pid)
		_ = cmd.Wait()
	})
	if !processAlive(target.pid) {
		t.Fatalf("helper pid %d 起来后应被视为存活", target.pid)
	}
	return target
}

// seedStopTarget 写一条「可停止」档案：活节点（pid = 靶进程）+ 可选的回环
// 控制面（baseURL 为空表示没有控制面，只能 --force）。
func seedStopTarget(t *testing.T, paths Paths, now time.Time, nodeID, sessionID string, pid int, baseURL, token string) NodeRecord {
	t.Helper()
	record := viewNodeRecord(nodeID, pid, now.Add(-5*time.Second), sessionID, "")
	if strings.TrimSpace(baseURL) != "" {
		parsed, err := url.Parse(baseURL)
		if err != nil {
			t.Fatalf("parse base url %q: %v", baseURL, err)
		}
		port, _ := strconv.Atoi(parsed.Port())
		record.Endpoint = &EndpointInfo{
			Scheme:     parsed.Scheme,
			Host:       parsed.Hostname(),
			Port:       port,
			Loopback:   true,
			BaseURL:    baseURL,
			WebBaseURL: baseURL + "/web",
		}
	}
	if token != "" {
		record.Auth = &AuthInfo{Mode: "loopback-dev", Required: true, Token: token, TokenSource: "random"}
	}
	writeViewNode(t, paths, record)
	return record
}

// stopCallCapture 记录假目标收到的 mesh/call 请求（只留最后一次）。
type stopCallCapture struct {
	mu    sync.Mutex
	path  string
	token string
	body  CallRequestBody
	count int
}

func (c *stopCallCapture) record(r *http.Request, payload []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++
	c.path = r.URL.Path
	c.token = r.Header.Get(CallTokenHeader)
	_ = json.Unmarshal(payload, &c.body)
}

func (c *stopCallCapture) snapshot() (string, string, CallRequestBody, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.path, c.token, c.body, c.count
}

// stopTargetServer 起一个假目标：记录请求；收到 op=input 且 prompt=/exit 时
// 让靶进程退出（模拟「目标真的执行了 /exit」），并回 ok 信封。
func stopTargetServer(t *testing.T, capture *stopCallCapture, target *stopTarget, nodeID string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := readAllLimited(r)
		capture.record(r, payload)
		var req CallRequestBody
		_ = json.Unmarshal(payload, &req)
		if strings.TrimSpace(req.Op) == "input" && strings.Contains(string(req.Args), StopExitPrompt) {
			_ = terminateProcess(target.pid)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CallEnvelope{
			SchemaVersion: SchemaVersion,
			Status:        CallStatusOK,
			NodeID:        nodeID,
			Op:            strings.TrimSpace(req.Op),
		})
	}))
	t.Cleanup(server.Close)
	return server
}

// readAllLimited 读请求体（测试用；上限沿用生产的调用体上限）。
func readAllLimited(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, CallMaxBodyBytes))
}

// ---------------------------------------------------------------------------
// 模式与解析
// ---------------------------------------------------------------------------

func TestStopModeNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", StopModeGraceful, true},
		{"graceful", StopModeGraceful, true},
		{" Graceful ", StopModeGraceful, true},
		{"force", StopModeForce, true},
		{"FORCE", StopModeForce, true},
		{"kill", "", false},
	}
	for _, tc := range cases {
		got, ok := NormalizeStopMode(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("NormalizeStopMode(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestStopHTTPStatusMapping(t *testing.T) {
	cases := map[string]int{
		StopStatusStopped:  http.StatusOK,
		StopStatusNotFound: http.StatusNotFound,
		StopStatusRefused:  http.StatusForbidden,
		StopStatusTimeout:  http.StatusGatewayTimeout,
		StopStatusError:    http.StatusInternalServerError,
	}
	for status, want := range cases {
		if got := StopHTTPStatus(status); got != want {
			t.Fatalf("StopHTTPStatus(%s) = %d, want %d", status, got, want)
		}
	}
}

func TestStopUnknownTargetIsNotFound(t *testing.T) {
	paths := testCLIPaths(t)
	result := Stop(context.Background(), paths, "", StopRequest{Target: "node-does-not-exist"})
	if result.Status != StopStatusNotFound {
		t.Fatalf("status = %s, want %s（%+v）", result.Status, StopStatusNotFound, result)
	}
	if result.Code != CallCodeTargetNotFound {
		t.Fatalf("code = %q, want %q", result.Code, CallCodeTargetNotFound)
	}
}

func TestStopBadModeFailsBeforeAnyWork(t *testing.T) {
	paths := testCLIPaths(t)
	target := startStopTarget(t)
	now := NowUTC()
	nodeID := "node-stop-badmode"
	seedStopTarget(t, paths, now, nodeID, "session_stop_badmode", target.pid, "", "")

	result := Stop(context.Background(), paths, "", StopRequest{Target: nodeID, Mode: "kill"})
	if result.Status != StopStatusError || result.Code != StopCodeBadMode {
		t.Fatalf("result = %+v, want error/%s", result, StopCodeBadMode)
	}
	if !processAlive(target.pid) {
		t.Fatal("mode 非法时不得终止任何进程")
	}
}

func TestStopSelfRefused(t *testing.T) {
	paths := testCLIPaths(t)
	now := NowUTC()
	nodeID := "node-stop-self"
	seedStopTarget(t, paths, now, nodeID, "session_stop_self", os.Getpid(), "", "")

	result := Stop(context.Background(), paths, nodeID, StopRequest{Target: nodeID, Mode: StopModeForce})
	if result.Status != StopStatusRefused || result.Code != StopCodeSelfRefused {
		t.Fatalf("result = %+v, want refused/%s", result, StopCodeSelfRefused)
	}
}

func TestStopAlreadyStoppedRecordIsIdempotent(t *testing.T) {
	paths := testCLIPaths(t)
	now := NowUTC()
	nodeID := "node-stop-stopped"
	record := viewNodeRecord(nodeID, os.Getpid(), now.Add(-time.Hour), "session_stop_stopped", "")
	record.Liveness.State = string(NodeStateStopped)
	writeViewNode(t, paths, record)

	result := Stop(context.Background(), paths, "", StopRequest{Target: nodeID, Mode: StopModeGraceful})
	if result.Status != StopStatusStopped || result.Code != StopCodeAlreadyStopped {
		t.Fatalf("result = %+v, want stopped/%s", result, StopCodeAlreadyStopped)
	}
}

// ---------------------------------------------------------------------------
// force：真的把进程杀掉
// ---------------------------------------------------------------------------

func TestStopForceTerminatesProcess(t *testing.T) {
	paths := testCLIPaths(t)
	target := startStopTarget(t)
	now := NowUTC()
	nodeID := "node-stop-force"
	seedStopTarget(t, paths, now, nodeID, "session_stop_force", target.pid, "", "")

	result := Stop(context.Background(), paths, "", StopRequest{
		Target: nodeID,
		Mode:   StopModeForce,
		Wait:   10 * time.Second,
	})
	if !result.OK() {
		t.Fatalf("result = %+v, want stopped", result)
	}
	if result.Graceful {
		t.Fatal("force 不得声称投递过 /exit")
	}
	if processAlive(target.pid) {
		t.Fatalf("pid %d 应已退出", target.pid)
	}
}

func TestStopDeadProcessIsIdempotent(t *testing.T) {
	paths := testCLIPaths(t)
	now := NowUTC()
	nodeID := "node-stop-dead"
	seedStopTarget(t, paths, now, nodeID, "session_stop_dead", deadPID(t), "", "")

	result := Stop(context.Background(), paths, "", StopRequest{Target: nodeID, Mode: StopModeForce})
	if !result.OK() {
		t.Fatalf("result = %+v, want stopped（已死进程按幂等成功）", result)
	}
	if result.Graceful {
		t.Fatal("进程本就不在运行时不得声称投递过 /exit")
	}
}

// ---------------------------------------------------------------------------
// graceful：投 /exit、等它自己收尾
// ---------------------------------------------------------------------------

func TestStopGracefulDeliversExitAndWaits(t *testing.T) {
	paths := testCLIPaths(t)
	target := startStopTarget(t)
	now := NowUTC()
	nodeID := "node-stop-graceful"
	capture := &stopCallCapture{}
	server := stopTargetServer(t, capture, target, nodeID)
	const token = "stop-token-abcdef"
	seedStopTarget(t, paths, now, nodeID, "session_stop_graceful", target.pid, server.URL, token)

	result := Stop(context.Background(), paths, "", StopRequest{
		Target: nodeID,
		Mode:   StopModeGraceful,
		Wait:   10 * time.Second,
	})
	if !result.OK() || !result.Graceful {
		t.Fatalf("result = %+v, want stopped+graceful", result)
	}
	if processAlive(target.pid) {
		t.Fatalf("pid %d 应已退出", target.pid)
	}
	path, gotToken, body, count := capture.snapshot()
	if count != 1 {
		t.Fatalf("目标收到的调用次数 = %d, want 1（不做无意义重试）", count)
	}
	if path != ChatWebMeshCallPath {
		t.Fatalf("调用路径 = %q, want %q", path, ChatWebMeshCallPath)
	}
	if gotToken != token {
		t.Fatalf("令牌头 = %q, want %q（令牌取自目标档案）", gotToken, token)
	}
	if body.Op != "input" {
		t.Fatalf("op = %q, want input", body.Op)
	}
	if !body.AllowWrite {
		t.Fatal("input 是写操作：stop 必须显式 allow_write=true")
	}
	var args struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(body.Args, &args); err != nil {
		t.Fatalf("args 解析失败: %v (%s)", err, string(body.Args))
	}
	if args.Prompt != StopExitPrompt {
		t.Fatalf("prompt = %q, want %q", args.Prompt, StopExitPrompt)
	}
}

func TestStopGracefulWithoutEndpointRefuses(t *testing.T) {
	paths := testCLIPaths(t)
	target := startStopTarget(t)
	now := NowUTC()
	nodeID := "node-stop-no-endpoint"
	seedStopTarget(t, paths, now, nodeID, "session_stop_no_endpoint", target.pid, "", "")

	result := Stop(context.Background(), paths, "", StopRequest{Target: nodeID, Mode: StopModeGraceful})
	if result.Status != StopStatusRefused || result.Code != CallCodeNoEndpoint {
		t.Fatalf("result = %+v, want refused/%s", result, CallCodeNoEndpoint)
	}
	if !processAlive(target.pid) {
		t.Fatal("graceful 失败时不得静默降级为杀进程")
	}
}

// ---------------------------------------------------------------------------
// CLI 入口（退出码与 --json 形状）
// ---------------------------------------------------------------------------

func TestCLIStopForceJSON(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	cli := testCLI(paths, clock)
	target := startStopTarget(t)
	nodeID := "node-cli-stop-force"
	seedStopTarget(t, paths, clock.Now(), nodeID, "session_cli_stop", target.pid, "", "")

	code, stdout, stderr := runCLI(t, cli, "stop", nodeID, "--force", "--json")
	if code != ExitOK {
		t.Fatalf("stop --force exit = %d (stderr %q)", code, stderr)
	}
	out := decodeJSON[stopResult](t, stdout)
	if out.Status != StopStatusStopped || out.Mode != StopModeForce {
		t.Fatalf("json = %+v, want stopped/force", out)
	}
	if out.SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", out.SchemaVersion, SchemaVersion)
	}
	if processAlive(target.pid) {
		t.Fatalf("pid %d 应已退出", target.pid)
	}
}

func TestCLIStopUsageAndNotFound(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	cli := testCLI(paths, clock)

	if code, _, _ := runCLI(t, cli, "stop"); code != ExitUsage {
		t.Fatalf("stop（缺目标）exit = %d, want %d", code, ExitUsage)
	}
	if code, _, _ := runCLI(t, cli, "stop", "node-nope", "--force"); code != ExitNotFound {
		t.Fatalf("stop（目标不存在）exit = %d, want %d", code, ExitNotFound)
	}
}

func TestCLIStopGracefulNoEndpointHintsForce(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	cli := testCLI(paths, clock)
	target := startStopTarget(t)
	nodeID := "node-cli-stop-graceful"
	seedStopTarget(t, paths, clock.Now(), nodeID, "session_cli_stop2", target.pid, "", "")

	code, _, stderr := runCLI(t, cli, "stop", nodeID)
	if code != ExitRefused {
		t.Fatalf("stop（无控制面）exit = %d, want %d (stderr %q)", code, ExitRefused, stderr)
	}
	if !strings.Contains(stderr, "--force") {
		t.Fatalf("stderr 应提示 --force：%q", stderr)
	}
	if !processAlive(target.pid) {
		t.Fatal("graceful 失败时不得终止进程")
	}
}
