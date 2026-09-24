package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// runMeshAliasForTest 执行 `aicli mesh ...` 并返回退出码与两个输出流。
// 退出码 0 表示没有触发退出钩子（与 export_command_test.go 同一惯例）。
func runMeshAliasForTest(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	original := meshAliasExitHook
	code := meshAliasExitOK
	meshAliasExitHook = func(exitCode int) { code = exitCode }
	t.Cleanup(func() { meshAliasExitHook = original })

	cmd := NewMeshCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute %v: %v", args, err)
	}
	return code, stdout.String(), stderr.String()
}

// meshAliasTestPaths 把网格根指向临时目录，避免碰到真实网格。
func meshAliasTestPaths(t *testing.T) mesh.Paths {
	t.Helper()
	t.Setenv(mesh.EnvMeshDir, t.TempDir())
	paths := mesh.ResolvePaths()
	if !paths.Enabled() {
		t.Fatalf("expected an enabled mesh layout, got %+v", paths)
	}
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	return paths
}

func TestMeshAliasLsJSON(t *testing.T) {
	meshAliasTestPaths(t)

	code, stdout, stderr := runMeshAliasForTest(t, []string{"ls", "--json"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	var payload struct {
		SchemaVersion int   `json:"schema_version"`
		Nodes         []any `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("ls --json 不是合法 JSON: %v\n%s", err, stdout)
	}
	if payload.SchemaVersion != mesh.SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", payload.SchemaVersion, mesh.SchemaVersion)
	}
	if len(payload.Nodes) != 0 {
		t.Fatalf("空网格应有 0 个节点，得到 %d", len(payload.Nodes))
	}
}

// 别名必须把参数（含 --flag）原样透传：这里用 watch 的旗标组合验证，
// 它是网格工具里旗标最多、且最容易被 cobra 吃掉位置参数的命令。
func TestMeshAliasPassesFlagsVerbatim(t *testing.T) {
	paths := meshAliasTestPaths(t)
	writeAliasJournal(t, paths, "node-4242-20260924T100000Z", []mesh.JournalEntry{
		{
			TS:        time.Now().UTC().Add(-time.Minute),
			NodeID:    "node-4242-20260924T100000Z",
			Seq:       1,
			Kind:      mesh.JournalSessionActivated,
			SessionID: "session_alias",
		},
	})

	code, stdout, stderr := runMeshAliasForTest(t, []string{"watch", "--once", "--since", "1h", "--json"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	var payload struct {
		Events []mesh.JournalEntry `json:"events"`
		Counts struct {
			Events int `json:"events"`
			Nodes  int `json:"nodes"`
		} `json:"counts"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("watch --json 不是合法 JSON: %v\n%s", err, stdout)
	}
	if len(payload.Events) != 1 || payload.Counts.Nodes != 1 {
		t.Fatalf("events = %d / nodes = %d, want 1 / 1", len(payload.Events), payload.Counts.Nodes)
	}
}

func TestMeshAliasExitCodePropagatesTargetNotFound(t *testing.T) {
	meshAliasTestPaths(t)

	code, stdout, stderr := runMeshAliasForTest(t, []string{"show", "nope"})
	if code != mesh.ExitNotFound {
		t.Fatalf("exit = %d, want %d（目标不存在必须透传，不能被 cobra 压成 1）", code, mesh.ExitNotFound)
	}
	if stdout != "" {
		t.Fatalf("错误信息不该写到 stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "nope") {
		t.Fatalf("stderr 应提到目标: %q", stderr)
	}
}

func TestMeshAliasExitCodePropagatesUsageError(t *testing.T) {
	meshAliasTestPaths(t)

	code, _, stderr := runMeshAliasForTest(t, []string{"nope"})
	if code != mesh.ExitUsage {
		t.Fatalf("exit = %d, want %d", code, mesh.ExitUsage)
	}
	if !strings.Contains(stderr, "未知子命令") {
		t.Fatalf("stderr 应说明未知子命令: %q", stderr)
	}
}

// `aicli mesh --help` 与 `aicli-mesh --help` 同一条路径：用法写 stdout、退出码 0。
func TestMeshAliasHelpExitsZero(t *testing.T) {
	meshAliasTestPaths(t)

	code, stdout, _ := runMeshAliasForTest(t, []string{"--help"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "aicli-mesh") || !strings.Contains(stdout, "aicli-mesh watch") {
		t.Fatalf("用法应列出子命令（含 watch）: %q", stdout)
	}
}

// 别名报的是 aicli 自身的版本（main 注入），不是 mesh.CLI 的 dev 兜底。
func TestMeshAliasVersionReportsHostBinaryVersion(t *testing.T) {
	meshAliasTestPaths(t)
	original := chatStatusVersion
	chatStatusVersion = "9.9.9-test"
	t.Cleanup(func() { chatStatusVersion = original })

	code, stdout, stderr := runMeshAliasForTest(t, []string{"version", "--json"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "9.9.9-test") {
		t.Fatalf("version --json 应带宿主二进制版本: %q", stdout)
	}
}

// writeAliasJournal 在网格的 journal 目录写一个节点的日志文件。
func writeAliasJournal(t *testing.T, paths mesh.Paths, nodeID string, entries []mesh.JournalEntry) {
	t.Helper()
	var builder strings.Builder
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal journal entry: %v", err)
		}
		builder.Write(data)
		builder.WriteString("\n")
	}
	target := paths.JournalPath(nodeID)
	if target == "" {
		t.Fatalf("journal path unavailable for %q", nodeID)
	}
	if err := os.WriteFile(target, []byte(builder.String()), 0o600); err != nil {
		t.Fatalf("write journal: %v", err)
	}
}
