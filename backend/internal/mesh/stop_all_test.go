package mesh

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// stop --all 测试：批量停止的枚举口径、过滤、治理门禁与聚合退出码。
//
// 复用 stop_test.go 的靶进程基础设施（真实进程消失才算停掉）；CLI 走真实时钟
// （不带 Now seam），与 seedStopTarget 的真实 now 保持同一口径。
// ============================================================================

// seedStopAllNode 写一条可被 stop --all 枚举的档案：live 心跳 + 指定工作区 +
// 可选的 CapabilityStop（等价目标进程开了 --mesh-allow-stop）。
func seedStopAllNode(t *testing.T, paths Paths, nodeID, sessionID, workspace string, pid int, allowStop bool) {
	t.Helper()
	record := viewNodeRecord(nodeID, pid, NowUTC().Add(-5*time.Second), sessionID, workspace)
	if allowStop {
		record.Capabilities = append(record.Capabilities, CapabilityStop)
	}
	writeViewNode(t, paths, record)
}

// TestCLIStopAllStopsAllLiveNodes 验证批量停止真的把所有在线节点停掉，
// 且全部成功时退出码 0。
func TestCLIStopAllStopsAllLiveNodes(t *testing.T) {
	paths := testCLIPaths(t)
	targetA := startStopTarget(t)
	targetB := startStopTarget(t)
	seedStopAllNode(t, paths, "node-all-a", "session_all_a", "E:\\ws\\a", targetA.pid, true)
	seedStopAllNode(t, paths, "node-all-b", "session_all_b", "E:\\ws\\b", targetB.pid, true)

	cli := &CLI{Paths: &paths}
	code, stdout, stderr := runCLI(t, cli, "stop", "--all", "--force", "--wait", "10s")
	if code != ExitOK {
		t.Fatalf("stop --all exit = %d (stdout %q stderr %q)", code, stdout, stderr)
	}
	if processAlive(targetA.pid) || processAlive(targetB.pid) {
		t.Fatalf("两个靶进程都应退出（a alive=%v b alive=%v）",
			processAlive(targetA.pid), processAlive(targetB.pid))
	}
	for _, want := range []string{"node-all-a", "node-all-b", "stopped=2"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout 缺少 %q：\n%s", want, stdout)
		}
	}
}

// TestCLIStopAllRespectsGovernanceGate 锁死「批量也绕不过开关」（§9.2）：
// 未开启 --mesh-allow-stop 的节点只得到 refused，进程不被碰、不发请求。
func TestCLIStopAllRespectsGovernanceGate(t *testing.T) {
	paths := testCLIPaths(t)
	target := startStopTarget(t)
	seedStopAllNode(t, paths, "node-all-noswitch", "session_all_noswitch", "E:\\ws\\a", target.pid, false)

	cli := &CLI{Paths: &paths}
	code, _, stderr := runCLI(t, cli, "stop", "--all", "--force")
	if code != ExitRefused {
		t.Fatalf("stop --all exit = %d, want %d（stderr %q）", code, ExitRefused, stderr)
	}
	if !processAlive(target.pid) {
		t.Fatal("门禁拒绝时不得碰目标进程")
	}
	if !strings.Contains(stderr, StopCodeNotAllowed) {
		t.Fatalf("stderr 应含原因码 %q：\n%s", StopCodeNotAllowed, stderr)
	}
}

// TestCLIStopAllMixedGateIsPartialSuccess 验证「一个被拒、一个成功」的混合结果：
// 开关节点的进程真的停了，聚合退出码按最严重一档报 refused（6）。
func TestCLIStopAllMixedGateIsPartialSuccess(t *testing.T) {
	paths := testCLIPaths(t)
	targetOK := startStopTarget(t)
	targetRefused := startStopTarget(t)
	seedStopAllNode(t, paths, "node-all-mix-ok", "session_all_mix_ok", "E:\\ws\\a", targetOK.pid, true)
	seedStopAllNode(t, paths, "node-all-mix-refused", "session_all_mix_refused", "E:\\ws\\b", targetRefused.pid, false)

	cli := &CLI{Paths: &paths}
	code, stdout, _ := runCLI(t, cli, "stop", "--all", "--force", "--wait", "10s")
	if code != ExitRefused {
		t.Fatalf("exit = %d, want %d（refused 应为聚合退出码）", code, ExitRefused)
	}
	if processAlive(targetOK.pid) {
		t.Fatal("开了开关的节点应已停止（部分成功也是成功）")
	}
	if !processAlive(targetRefused.pid) {
		t.Fatal("没开开关的节点不得被碰")
	}
	if !strings.Contains(stdout, "refused=1") || !strings.Contains(stdout, "stopped=1") {
		t.Fatalf("汇总应包含 refused=1 与 stopped=1：\n%s", stdout)
	}
}

// TestCLIStopAllWorkspaceFilter 验证 --workspace 只收窄目标范围，其它工作区
// 的节点不受影响。
func TestCLIStopAllWorkspaceFilter(t *testing.T) {
	paths := testCLIPaths(t)
	targetA := startStopTarget(t)
	targetB := startStopTarget(t)
	seedStopAllNode(t, paths, "node-all-ws-a", "session_all_ws_a", "E:\\ws\\alpha", targetA.pid, true)
	seedStopAllNode(t, paths, "node-all-ws-b", "session_all_ws_b", "E:\\ws\\beta", targetB.pid, true)

	cli := &CLI{Paths: &paths}
	code, stdout, _ := runCLI(t, cli, "stop", "--all", "--workspace", "E:\\ws\\alpha", "--force", "--wait", "10s")
	if code != ExitOK {
		t.Fatalf("stop --all --workspace exit = %d（stdout %q）", code, stdout)
	}
	if processAlive(targetA.pid) {
		t.Fatal("alpha 工作区的节点应已停止")
	}
	if !processAlive(targetB.pid) {
		t.Fatal("beta 工作区的节点不得被碰")
	}
	if !strings.Contains(stdout, "skipped=1") {
		t.Fatalf("汇总应报告 skipped=1：\n%s", stdout)
	}
}

// TestCLIStopAllSkipsNonLive 验证枚举口径只针对在线节点：stopped 档案（死 pid）
// 不被尝试，也不影响幂等成功。
func TestCLIStopAllSkipsNonLive(t *testing.T) {
	paths := testCLIPaths(t)
	target := startStopTarget(t)
	seedStopAllNode(t, paths, "node-all-live", "session_all_live", "E:\\ws\\a", target.pid, true)
	stopped := viewNodeRecord("node-all-stopped", deadPID(t), NowUTC().Add(-time.Hour), "session_all_stopped", "E:\\ws\\b")
	stopped.Liveness.State = string(NodeStateStopped)
	writeViewNode(t, paths, stopped)

	cli := &CLI{Paths: &paths}
	code, stdout, _ := runCLI(t, cli, "stop", "--all", "--force", "--wait", "10s")
	if code != ExitOK {
		t.Fatalf("stop --all exit = %d（stdout %q）", code, stdout)
	}
	if processAlive(target.pid) {
		t.Fatal("在线节点应已停止")
	}
	if strings.Contains(stdout, "node-all-stopped") {
		t.Fatalf("非在线节点不应出现在结果里：\n%s", stdout)
	}
	if !strings.Contains(stdout, "skipped=1") {
		t.Fatalf("汇总应报告 skipped=1：\n%s", stdout)
	}
}

// TestCLIStopAllEmptyMeshIsIdempotentSuccess 验证空网格（或过滤后无目标）时
// 退出码 0，人读输出说明无事可做，JSON 给空 results。
func TestCLIStopAllEmptyMeshIsIdempotentSuccess(t *testing.T) {
	paths := testCLIPaths(t)
	cli := &CLI{Paths: &paths}

	code, stdout, _ := runCLI(t, cli, "stop", "--all", "--force")
	if code != ExitOK {
		t.Fatalf("空网格 exit = %d, want %d", code, ExitOK)
	}
	if !strings.Contains(stdout, "没有在线节点") {
		t.Fatalf("人读输出应说明无事可做：\n%s", stdout)
	}

	code, stdout, _ = runCLI(t, cli, "stop", "--all", "--json", "--force")
	if code != ExitOK {
		t.Fatalf("空网格 --json exit = %d, want %d", code, ExitOK)
	}
	var payload stopAllResult
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, stdout)
	}
	if payload.SchemaVersion != SchemaVersion || payload.Mode != StopModeForce {
		t.Fatalf("envelope = %+v, want schema %d / mode %s", payload, SchemaVersion, StopModeForce)
	}
	if payload.Results == nil || len(payload.Results) != 0 {
		t.Fatalf("results 应为空数组，got %+v", payload.Results)
	}
}

// TestCLIStopAllJSONShape 验证 --json 输出契约：schema_version / mode /
// results[]（node_id + 单目标结果字段）/ counts。
func TestCLIStopAllJSONShape(t *testing.T) {
	paths := testCLIPaths(t)
	targetOK := startStopTarget(t)
	targetRefused := startStopTarget(t)
	seedStopAllNode(t, paths, "node-all-json-ok", "session_all_json_ok", "E:\\ws\\a", targetOK.pid, true)
	seedStopAllNode(t, paths, "node-all-json-refused", "session_all_json_refused", "E:\\ws\\b", targetRefused.pid, false)

	cli := &CLI{Paths: &paths}
	code, stdout, _ := runCLI(t, cli, "stop", "--all", "--force", "--wait", "10s", "--json")
	if code != ExitRefused {
		t.Fatalf("exit = %d, want %d", code, ExitRefused)
	}
	var payload stopAllResult
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, stdout)
	}
	if payload.SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", payload.SchemaVersion, SchemaVersion)
	}
	if payload.Mode != StopModeForce {
		t.Fatalf("mode = %q, want %q", payload.Mode, StopModeForce)
	}
	if len(payload.Results) != 2 {
		t.Fatalf("results 数量 = %d, want 2", len(payload.Results))
	}
	byNode := map[string]stopAllEntry{}
	for _, entry := range payload.Results {
		byNode[entry.NodeID] = entry
	}
	okEntry, hasOK := byNode["node-all-json-ok"]
	refusedEntry, hasRefused := byNode["node-all-json-refused"]
	if !hasOK || !hasRefused {
		t.Fatalf("results 缺少节点行：%+v", payload.Results)
	}
	if okEntry.Status != StopStatusStopped || !okEntry.OK() {
		t.Fatalf("ok 节点结果 = %+v, want stopped", okEntry.StopResult)
	}
	if okEntry.PID != targetOK.pid {
		t.Fatalf("ok 节点 pid = %d, want %d", okEntry.PID, targetOK.pid)
	}
	if refusedEntry.Status != StopStatusRefused || refusedEntry.Code != StopCodeNotAllowed {
		t.Fatalf("refused 节点结果 = %+v", refusedEntry.StopResult)
	}
	if payload.Counts.Total != 2 || payload.Counts.Stopped != 1 || payload.Counts.Refused != 1 {
		t.Fatalf("counts = %+v, want total=2 stopped=1 refused=1", payload.Counts)
	}
}

// TestCLIStopAllGracefulDeliversExitToEveryNode 验证批量优雅停止：每个有控制面
// 的在线节点都收到 /exit 并自行退出。
func TestCLIStopAllGracefulDeliversExitToEveryNode(t *testing.T) {
	paths := testCLIPaths(t)
	targetA := startStopTarget(t)
	targetB := startStopTarget(t)
	captureA := &stopCallCapture{}
	captureB := &stopCallCapture{}
	serverA := stopTargetServer(t, captureA, targetA, "node-all-g-a")
	serverB := stopTargetServer(t, captureB, targetB, "node-all-g-b")
	recordA := seedStopTarget(t, paths, NowUTC(), "node-all-g-a", "session_all_g_a", targetA.pid, serverA.URL, "token-a-123456")
	recordB := seedStopTarget(t, paths, NowUTC(), "node-all-g-b", "session_all_g_b", targetB.pid, serverB.URL, "token-b-123456")
	recordA.Workspace = &WorkspaceInfo{Path: "E:\\ws\\a", Name: "a"}
	recordB.Workspace = &WorkspaceInfo{Path: "E:\\ws\\b", Name: "b"}
	writeViewNode(t, paths, recordA)
	writeViewNode(t, paths, recordB)

	cli := &CLI{Paths: &paths}
	code, stdout, stderr := runCLI(t, cli, "stop", "--all", "--wait", "10s")
	if code != ExitOK {
		t.Fatalf("stop --all exit = %d（stdout %q stderr %q）", code, stdout, stderr)
	}
	if processAlive(targetA.pid) || processAlive(targetB.pid) {
		t.Fatalf("两个靶进程都应退出（a alive=%v b alive=%v）",
			processAlive(targetA.pid), processAlive(targetB.pid))
	}
	for _, capture := range []*stopCallCapture{captureA, captureB} {
		_, _, body, count := capture.snapshot()
		if count != 1 {
			t.Fatalf("每个节点应恰好收到 1 次投递，实际 %d 次", count)
		}
		if !strings.Contains(string(body.Args), StopExitPrompt) {
			t.Fatalf("投递载荷应含 %q：%s", StopExitPrompt, body.Args)
		}
	}
	if !strings.Contains(stdout, "已优雅退出") {
		t.Fatalf("人读输出应逐节点报告优雅退出：\n%s", stdout)
	}
}

// TestCLIStopAllUsageErrors 锁死批量形态的用法约束：--all 不接受位置参数，
// 单目标形态不认 --workspace。
func TestCLIStopAllUsageErrors(t *testing.T) {
	paths := testCLIPaths(t)
	cli := &CLI{Paths: &paths}

	code, _, stderr := runCLI(t, cli, "stop", "--all", "node-something")
	if code != ExitUsage {
		t.Fatalf("--all 带位置参数 exit = %d, want %d（stderr %q）", code, ExitUsage, stderr)
	}

	code, _, _ = runCLI(t, cli, "stop")
	if code != ExitUsage {
		t.Fatalf("stop 无目标 exit = %d, want %d", code, ExitUsage)
	}
}

// TestStopAllSeverityRanking 直接锁严重度排名：timeout > error > refused。
func TestStopAllSeverityRanking(t *testing.T) {
	cases := []struct {
		status string
		rank   int
	}{
		{StopStatusTimeout, 3},
		{StopStatusError, 2},
		{StopStatusRefused, 1},
		{StopStatusNotFound, 0},
		{StopStatusStopped, 0},
	}
	for _, tc := range cases {
		if got := stopAllSeverity(tc.status); got != tc.rank {
			t.Fatalf("stopAllSeverity(%s) = %d, want %d", tc.status, got, tc.rank)
		}
	}
}
