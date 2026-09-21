//go:build windows && !win7compat

package transport

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWindowsShimWithSpacedArgsDeliversExactArgv 是本缺陷的端到端回归护栏（计划 §5.5.2）。
//
// 场景完全复刻真实 MCP 启动链：cmd.exe → 含空格的目录/文件名 .cmd 垫片 → node。
// 断言最终进程收到的 argv 与传入逐字节一致。原实现（os/exec argv 拼装 + 隐式
// cmd 引号规则）在这里必然失败（首个 token 被截断成 "C:\Program"），修复后通过。
func TestWindowsShimWithSpacedArgsDeliversExactArgv(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available on PATH")
	}

	base := t.TempDir()
	scriptDir := filepath.Join(base, "dir with space") // 目录名含空格
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	dumpPath := filepath.Join(base, "argv.json")
	dumpScript := filepath.Join(scriptDir, "dump-argv.js")
	dumpSource := `const fs=require("fs");` +
		`fs.writeFileSync(process.env.AICLI_ARGV_DUMP, JSON.stringify(process.argv.slice(2)));`
	if err := os.WriteFile(dumpScript, []byte(dumpSource), 0o644); err != nil {
		t.Fatalf("write dump script: %v", err)
	}

	shim := filepath.Join(scriptDir, "fake launcher.cmd") // 文件名也含空格
	shimSource := "@echo off\r\n" + `"` + node + `" "%~dp0dump-argv.js" %*` + "\r\n"
	if err := os.WriteFile(shim, []byte(shimSource), 0o644); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	want := []string{
		"-y",
		"--userDataDir", `C:\Users\demo user\Edge\User Data`,
		"--logFile", filepath.Join(base, "log with space.log"),
		"--literal-percent", `%PATH%`,
		"--ampersand", `a&b`,
		"--quoted", `say "hi"`,
		"--trailing-backslash", `C:\temp dir\`,
	}

	rc := resolveStdioCommand(shim, want)
	if rc.Path != "cmd.exe" {
		t.Fatalf("Path = %q, want cmd.exe", rc.Path)
	}
	if !rc.RawCmdLineExplicit() {
		t.Fatalf("RawCmdLine must be set, got %+v", rc)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd, guard, guardErr := newStdioCommandGuard(ctx, rc)
	if guard != nil {
		defer guard.Close()
	}
	if guardErr != nil {
		t.Logf("process guard degraded (non-fatal): %v", guardErr)
	}
	cmd.Env = append(os.Environ(), "AICLI_ARGV_DUMP="+dumpPath)
	cmd.Dir = base

	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v (stderr=%s)", err, stderr.String())
	}

	data, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("read argv dump: %v (stderr=%s)", err, stderr.String())
	}
	var got []string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal argv: %v (raw=%s)", err, data)
	}
	if len(got) != len(want) {
		t.Fatalf("argv length = %d, want %d\ngot : %#v\nwant: %#v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q\nfull got: %#v", i, got[i], want[i], got)
		}
	}
}

// 反向护栏：RawCmdLine 为空时（错误地回退到 os/exec argv 拼装）argv 必然被
// cmd.exe 截断 —— 证明上面的 e2e 真的能捕获该缺陷。
func TestWindowsShimArgvWrappingTruncatesSpacedPath(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available on PATH")
	}
	base := t.TempDir()
	scriptDir := filepath.Join(base, "dir with space")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dumpPath := filepath.Join(base, "argv.json")
	if err := os.WriteFile(filepath.Join(scriptDir, "dump-argv.js"),
		[]byte(`require("fs").writeFileSync(process.env.AICLI_ARGV_DUMP, JSON.stringify(process.argv.slice(2)));`), 0o644); err != nil {
		t.Fatalf("write dump script: %v", err)
	}
	shim := filepath.Join(scriptDir, "fake launcher.cmd")
	if err := os.WriteFile(shim, []byte("@echo off\r\n"+"\""+node+"\" \"%~dp0dump-argv.js\" %*\r\n"), 0o644); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	// 旧行为：cmd.exe /c <script> 作为 argv 交给 os/exec 拼装。
	cmd := exec.Command("cmd.exe", "/c", shim, "--userDataDir", `C:\Users\demo user\Edge\User Data`)
	cmd.Env = append(os.Environ(), "AICLI_ARGV_DUMP="+dumpPath)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	_ = cmd.Run()

	data, err := os.ReadFile(dumpPath)
	if err == nil {
		var got []string
		_ = json.Unmarshal(data, &got)
		if len(got) == 2 && got[1] == `C:\Users\demo user\Edge\User Data` {
			t.Fatalf("expected legacy argv wrapping to be broken, but argv arrived intact: %#v", got)
		}
		t.Logf("legacy wrapping produced argv=%#v (broken as expected)", got)
		return
	}
	t.Logf("legacy wrapping failed before writing argv (expected): %v; stderr=%s", err, stderr.String())
}
