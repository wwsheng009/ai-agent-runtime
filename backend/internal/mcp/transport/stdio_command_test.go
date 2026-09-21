package transport

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeTempShim(t *testing.T, dir, name string) string {
	t.Helper()
	script := filepath.Join(dir, name)
	if err := os.WriteFile(script, []byte("@echo off\r\n"), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	return script
}

// Windows 上 npx / uvx 等启动器是 .cmd 垫片：必须包装成
// `cmd.exe /d /s /c "<整条命令行>"`，并携带 RawCmdLine 让调用方绕过 os/exec
// 的 argv 拼装，否则参数含空格时 cmd.exe 会把首 token 截断（计划 §5.1）。
func TestResolveStdioCommandWrapsWindowsBatchShim(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only shim resolution")
	}
	script := writeTempShim(t, t.TempDir(), "fake-npx.cmd")

	rc := resolveStdioCommand(script, []string{"-y", "some-mcp-server"})
	if rc.Path != "cmd.exe" {
		t.Fatalf("Path = %q, want cmd.exe", rc.Path)
	}
	if !rc.RawCmdLineExplicit() {
		t.Fatalf("RawCmdLine must be set for .cmd shims, got %+v", rc)
	}
	wantPrefix := []string{"/d", "/s", "/c"}
	if len(rc.Args) != len(wantPrefix)+1 {
		t.Fatalf("Args = %+v, want /d /s /c <command-line>", rc.Args)
	}
	for i, want := range wantPrefix {
		if rc.Args[i] != want {
			t.Fatalf("Args = %+v, want prefix %+v", rc.Args, wantPrefix)
		}
	}
	if wantPayload := strings.TrimPrefix(rc.RawCmdLine, "cmd.exe /d /s /c "); rc.Args[3] != wantPayload {
		t.Fatalf("Args[3] must mirror RawCmdLine payload: %q vs %q", rc.Args[3], wantPayload)
	}
	if !strings.Contains(rc.RawCmdLine, "-y") || !strings.Contains(rc.RawCmdLine, "some-mcp-server") {
		t.Fatalf("RawCmdLine lost args: %q", rc.RawCmdLine)
	}
}

func TestResolveStdioCommandResolvesShimThroughPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only shim resolution")
	}
	dir := t.TempDir()
	_ = writeTempShim(t, dir, "aicli-fake-launcher.cmd")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	rc := resolveStdioCommand("aicli-fake-launcher", []string{"serve"})
	if rc.Path != "cmd.exe" {
		t.Fatalf("Path = %q, want cmd.exe (resolved shim)", rc.Path)
	}
	if !strings.Contains(rc.RawCmdLine, "serve") {
		t.Fatalf("RawCmdLine = %q, want it to contain the arg", rc.RawCmdLine)
	}
}

func TestResolveStdioCommandLeavesExecutablesAlone(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool.exe")
	if err := os.WriteFile(exe, []byte("stub"), 0o755); err != nil {
		t.Fatalf("write exe: %v", err)
	}
	rc := resolveStdioCommand(exe, []string{"--flag"})
	if rc.Path != exe || len(rc.Args) != 1 || rc.Args[0] != "--flag" {
		t.Fatalf("resolve(exe) = (%q, %+v), want (%q, [--flag])", rc.Path, rc.Args, exe)
	}
	if rc.RawCmdLineExplicit() {
		t.Fatalf(".exe must not use raw command line: %+v", rc)
	}
}

func TestResolveStdioCommandKeepsUnknownCommandVerbatim(t *testing.T) {
	// 解析失败时必须保留原命令，让启动错误以原始形式暴露给用户。
	rc := resolveStdioCommand("aicli-definitely-not-installed-xyz", []string{"a"})
	if runtime.GOOS == "windows" {
		if rc.Path != "aicli-definitely-not-installed-xyz" {
			t.Fatalf("Path = %q, want verbatim", rc.Path)
		}
	}
	if len(rc.Args) != 1 || rc.Args[0] != "a" {
		t.Fatalf("Args = %+v", rc.Args)
	}
}

// 参数含空格时，必须作为「单个被引号包裹的整体」出现在 RawCmdLine 里，
// 这正是原实现（Go argv 拼装）失败的场景。
func TestResolveStdioCommandKeepsSpacedArgAsSingleToken(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only shim resolution")
	}
	script := writeTempShim(t, t.TempDir(), "fake-npx.cmd")
	spaced := `C:\Users\demo user\Edge\User Data`

	rc := resolveStdioCommand(script, []string{"--userDataDir", spaced})
	if wantArg := `^"C:\Users\demo^ user\Edge\User^ Data^"`; !strings.Contains(rc.RawCmdLine, wantArg) {
		t.Fatalf("RawCmdLine = %q, want the spaced path preserved as one token", rc.RawCmdLine)
	}
	if strings.Count(rc.RawCmdLine, `"`)%2 != 0 {
		t.Fatalf("RawCmdLine has unbalanced quotes: %q", rc.RawCmdLine)
	}
}

func TestEscapeCmdArgumentQuotesAndEscapes(t *testing.T) {
	cases := []struct {
		name string
		arg  string
		want string
	}{
		{"plain", "plain", `^"plain^"`},
		{"space", "a b", `^"a^ b^"`},
		{"empty", "", `^"^"`},
		{"trailing-backslash", `C:\dir\`, `^"C:\dir\\^"`},
		{"double-backslash-before-quote", `a\\"b`, `^"a\\\\\^"b^"`},
		{"embedded-quote", `say "hi"`, `^"say^ \^"hi\^"^"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapeCmdArgument(tc.arg, false); got != tc.want {
				t.Fatalf("escapeCmdArgument(%q) = %q, want %q", tc.arg, got, tc.want)
			}
		})
	}
}

func TestEscapeCmdCommandEscapesMetaChars(t *testing.T) {
	got := escapeCmdCommand(`C:\Program Files\nodejs\npx.cmd`)
	want := `C:\Program^ Files\nodejs\npx.cmd`
	if got != want {
		t.Fatalf("escapeCmdCommand = %q, want %q", got, want)
	}
}
