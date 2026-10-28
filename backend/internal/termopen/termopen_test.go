package termopen

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// newTestLauncher 装配一个只依赖注入点的 Launcher：LookPath 由 have 表决定，
// Getenv 由 env 决定，进程启动由调用方注入（测试从不真开窗口）。
func newTestLauncher(goos string, have map[string]bool, env map[string]string) *Launcher {
	return &Launcher{
		GOOS: goos,
		LookPath: func(name string) (string, error) {
			if have[name] {
				return "/usr/local/bin/" + name, nil
			}
			return "", exec.ErrNotFound
		},
		Getenv:  func(key string) string { return env[key] },
		Environ: func() []string { return []string{"PATH=/usr/local/bin", "HOME=/home/u"} },
	}
}

func testRequest() Request {
	return Request{
		Title:  "aicli · demo",
		Dir:    "E:/projects/demo",
		Binary: "E:/tools/aicli.exe",
		Args:   []string{"resume", "session_1", "--web", "--yolo"},
	}
}

func TestDetectWindowsPrefersWindowsTerminalThenNewConsole(t *testing.T) {
	withWT := newTestLauncher("windows", map[string]bool{"wt.exe": true}, nil)
	backend, err := withWT.Detect()
	if err != nil {
		t.Fatalf("detect with wt.exe: %v", err)
	}
	if backend != BackendWindowsTerminal {
		t.Fatalf("backend = %q, want %q", backend, BackendWindowsTerminal)
	}

	// 没有 Windows Terminal 时也必须能开窗：原生新控制台在同一台机器上就是终端。
	plain := newTestLauncher("windows", map[string]bool{}, nil)
	backend, err = plain.Detect()
	if err != nil {
		t.Fatalf("detect without wt.exe: %v", err)
	}
	if backend != BackendNewConsole {
		t.Fatalf("backend = %q, want %q", backend, BackendNewConsole)
	}
}

func TestDetectLinuxFollowsCandidateOrder(t *testing.T) {
	have := map[string]bool{"kitty": true, "gnome-terminal": true}
	launcher := newTestLauncher("linux", have, nil)
	backend, err := launcher.Detect()
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	// gnome_terminal 在顺序表里排在 kitty 之前：桌面上「用户看到的默认终端」优先。
	if backend != BackendGnomeTerminal {
		t.Fatalf("backend = %q, want %q", backend, BackendGnomeTerminal)
	}

	onlyKitty := newTestLauncher("linux", map[string]bool{"kitty": true}, nil)
	backend, err = onlyKitty.Detect()
	if err != nil || backend != BackendKitty {
		t.Fatalf("backend = %q err = %v, want %q", backend, err, BackendKitty)
	}
}

func TestDetectEnvironmentOverride(t *testing.T) {
	named := newTestLauncher("linux", map[string]bool{"alacritty": true}, map[string]string{EnvTerminal: "alacritty"})
	if backend, err := named.Detect(); err != nil || backend != BackendAlacritty {
		t.Fatalf("named override backend = %q err = %v", backend, err)
	}

	// AICLI_TERMINAL 指向任意程序：走 custom 形态，但存在性必须先验证。
	custom := newTestLauncher("linux", map[string]bool{"/opt/my-term": true}, map[string]string{EnvTerminal: "/opt/my-term"})
	backend, err := custom.Detect()
	if err != nil || backend != BackendCustom {
		t.Fatalf("custom backend = %q err = %v", backend, err)
	}
	plan, err := custom.Build(Request{Binary: "aicli", Args: []string{"chat"}})
	if err != nil {
		t.Fatalf("custom build: %v", err)
	}
	if plan.Path != "/usr/local/bin//opt/my-term" && !strings.Contains(plan.Path, "my-term") {
		t.Fatalf("custom plan path = %q, want the AICLI_TERMINAL program", plan.Path)
	}
	if strings.Join(plan.Args, " ") != "-e sh -c aicli chat" {
		t.Fatalf("custom args = %q", strings.Join(plan.Args, " "))
	}

	missing := newTestLauncher("linux", map[string]bool{}, map[string]string{EnvTerminal: "/opt/nope"})
	if _, err := missing.Detect(); err == nil || !strings.Contains(err.Error(), EnvTerminal) {
		t.Fatalf("missing override err = %v, want an actionable AICLI_TERMINAL error", err)
	}
}

func TestDetectStrictRefusesSilentFallback(t *testing.T) {
	launcher := newTestLauncher("linux", map[string]bool{}, map[string]string{EnvTerminalStrict: "1"})
	if _, err := launcher.Detect(); err == nil {
		t.Fatal("strict detect with no terminal must fail")
	}
	lenient := newTestLauncher("linux", map[string]bool{}, nil)
	if _, err := lenient.Detect(); err == nil {
		t.Fatal("expected an error when no terminal exists at all")
	}
}

func TestBuildGnomeTerminalArgvShape(t *testing.T) {
	launcher := newTestLauncher("linux", map[string]bool{"gnome-terminal": true}, nil)
	plan, err := launcher.Build(testRequest())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := []string{
		"--working-directory=E:/projects/demo",
		"--title=aicli · demo",
		"--", "E:/tools/aicli.exe", "resume", "session_1", "--web", "--yolo",
	}
	if strings.Join(plan.Args, "|") != strings.Join(want, "|") {
		t.Fatalf("args = %q, want %q", plan.Args, want)
	}
	if plan.Backend != BackendGnomeTerminal {
		t.Fatalf("backend = %q", plan.Backend)
	}
	// argv 形态的后端不产出 ShellCommand：UI 拿它去解释窗口会误导。
	if plan.ShellCommand != "" {
		t.Fatalf("ShellCommand = %q, want empty for argv backends", plan.ShellCommand)
	}
}

func TestBuildNewConsoleRunsTheBinaryItself(t *testing.T) {
	launcher := newTestLauncher("windows", map[string]bool{}, nil)
	plan, err := launcher.Build(testRequest())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if plan.Path != "E:/tools/aicli.exe" {
		t.Fatalf("path = %q, want the target binary itself", plan.Path)
	}
	if strings.Join(plan.Args, " ") != "resume session_1 --web --yolo" {
		t.Fatalf("args = %q", plan.Args)
	}
	if plan.Dir != "E:/projects/demo" {
		t.Fatalf("dir = %q", plan.Dir)
	}
	// 环境必须被展开成具体值：executor 的 Unix 分支把 nil Env 当作空环境。
	joined := strings.Join(plan.Env, ";")
	if !strings.Contains(joined, "PATH=/usr/local/bin") {
		t.Fatalf("env = %q, want the process environment", joined)
	}
}

func TestBuildTerminalAppEscapesShellCommand(t *testing.T) {
	launcher := newTestLauncher("darwin", map[string]bool{"osascript": true}, nil)
	req := Request{
		Dir:    "/tmp/it's a dir",
		Binary: "/usr/local/bin/aicli",
		Args:   []string{"resume", "session_1", "--prompt", "say \"hi\""},
	}
	plan, err := launcher.Build(req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if plan.Backend != BackendTerminalApp {
		t.Fatalf("backend = %q", plan.Backend)
	}
	joined := strings.Join(plan.Args, " ")
	// 单引号 token 必须被 '\'' 展开，并在进入 AppleScript 双引号串时再转义一层反斜杠。
	if !strings.Contains(joined, `'/tmp/it'\\''s a dir'`) {
		t.Fatalf("dir not POSIX-quoted: %q", joined)
	}
	if !strings.Contains(joined, `"say \\\"hi\\\""`) && !strings.Contains(joined, `say \"hi\"`) {
		t.Fatalf("prompt token not escaped for AppleScript: %q", joined)
	}
	if strings.Contains(joined, "it's a dir &&") {
		t.Fatalf("raw apostrophe survived quoting: %q", joined)
	}
	if plan.ShellCommand == "" {
		t.Fatal("shell backends must report their embedded command line")
	}
}

func TestBuildRejectsEmptyBinary(t *testing.T) {
	launcher := newTestLauncher("linux", map[string]bool{"gnome-terminal": true}, nil)
	if _, err := launcher.Build(Request{Dir: "/tmp"}); err == nil {
		t.Fatal("empty binary must be rejected")
	}
}

func TestLaunchUsesInjectedStarterAndMergesEnv(t *testing.T) {
	launcher := newTestLauncher("linux", map[string]bool{"kitty": true}, nil)
	var captured runtimeexecutor.DetachedLaunch
	launcher.Start = func(spec runtimeexecutor.DetachedLaunch) (*runtimeexecutor.DetachedProcess, error) {
		captured = spec
		return &runtimeexecutor.DetachedProcess{PID: 4242, Mode: runtimeexecutor.DetachModeSetsid}, nil
	}
	req := testRequest()
	req.Dir = "/srv/demo"
	req.Env = []string{"AICLI_SESSION_LAUNCHED_BY=gateway"}
	result, err := launcher.Launch(req)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if result.PID != 4242 || result.Mode != runtimeexecutor.DetachModeSetsid {
		t.Fatalf("result = %+v", result)
	}
	if captured.Dir != "/srv/demo" {
		t.Fatalf("captured dir = %q", captured.Dir)
	}
	if captured.Path != "/usr/local/bin/kitty" {
		t.Fatalf("captured path = %q", captured.Path)
	}
	joined := strings.Join(captured.Env, ";")
	if !strings.Contains(joined, "AICLI_SESSION_LAUNCHED_BY=gateway") || !strings.Contains(joined, "HOME=/home/u") {
		t.Fatalf("captured env = %q, want process env plus the request override", joined)
	}
}

func TestLaunchPropagatesStartFailure(t *testing.T) {
	launcher := newTestLauncher("linux", map[string]bool{"kitty": true}, nil)
	launcher.Start = func(runtimeexecutor.DetachedLaunch) (*runtimeexecutor.DetachedProcess, error) {
		return nil, errors.New("CreateProcess: nope")
	}
	if _, err := launcher.Launch(testRequest()); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v, want the platform failure surfaced", err)
	}
}

func TestShellCommandAndQuoting(t *testing.T) {
	shell := ShellCommand(Request{Dir: "/srv/a b", Binary: "/usr/bin/aicli", Args: []string{"resume", "s_1", "--theme", "dark mode"}})
	if !strings.HasPrefix(shell, "cd '/srv/a b' && ") {
		t.Fatalf("shell = %q", shell)
	}
	if !strings.Contains(shell, "'dark mode'") {
		t.Fatalf("space-bearing value must be quoted: %q", shell)
	}
	if !strings.Contains(shell, "'--theme'") {
		t.Fatalf("flag-looking token must be quoted: %q", shell)
	}
}

func TestNormalizeBackendNameAcceptsSpellingVariants(t *testing.T) {
	for _, value := range []string{"gnome-terminal", "GNOME_TERMINAL", "Gnome.Terminal"} {
		if got := normalizeBackendName(value); got != BackendGnomeTerminal {
			t.Fatalf("normalize(%q) = %q", value, got)
		}
	}
}
