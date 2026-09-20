package transport

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Windows 上 npx / uvx 等启动器是 .cmd 垫片：必须包装成 `cmd.exe /c <script>`，
// 否则 CreateProcess 会以 "not a valid application" 失败（O6）。
func TestResolveStdioCommandWrapsWindowsBatchShim(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only shim resolution")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-npx.cmd")
	if err := os.WriteFile(script, []byte("@echo off\r\n"), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	command, args := resolveStdioCommand(script, []string{"-y", "some-mcp-server"})
	if command != "cmd.exe" {
		t.Fatalf("command = %q, want cmd.exe", command)
	}
	want := []string{"/c", script, "-y", "some-mcp-server"}
	if len(args) != len(want) {
		t.Fatalf("args = %+v, want %+v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args = %+v, want %+v", args, want)
		}
	}
}

func TestResolveStdioCommandResolvesShimThroughPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only shim resolution")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "aicli-fake-launcher.cmd")
	if err := os.WriteFile(script, []byte("@echo off\r\n"), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	command, args := resolveStdioCommand("aicli-fake-launcher", []string{"serve"})
	if command != "cmd.exe" {
		t.Fatalf("command = %q, want cmd.exe (resolved shim)", command)
	}
	want := []string{"/c", script, "serve"}
	if len(args) != len(want) {
		t.Fatalf("args = %+v, want %+v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args = %+v, want %+v", args, want)
		}
	}
}

func TestResolveStdioCommandLeavesExecutablesAlone(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool.exe")
	if err := os.WriteFile(exe, []byte("stub"), 0o755); err != nil {
		t.Fatalf("write exe: %v", err)
	}
	command, args := resolveStdioCommand(exe, []string{"--flag"})
	if command != exe || len(args) != 1 || args[0] != "--flag" {
		t.Fatalf("resolve(exe) = (%q, %+v), want (%q, [--flag])", command, args, exe)
	}
}

func TestResolveStdioCommandKeepsUnknownCommandVerbatim(t *testing.T) {
	// 解析失败时必须保留原命令，让启动错误以原始形式暴露给用户。
	command, args := resolveStdioCommand("aicli-definitely-not-installed-xyz", []string{"a"})
	if runtime.GOOS == "windows" {
		if command != "aicli-definitely-not-installed-xyz" {
			t.Fatalf("command = %q, want verbatim", command)
		}
	}
	if len(args) != 1 || args[0] != "a" {
		t.Fatalf("args = %+v", args)
	}
}
