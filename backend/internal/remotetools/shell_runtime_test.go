package remotetools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

func TestNormalizeResolvesShellAtStartup(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if strings.TrimSpace(cfg.ResolvedShell.Path) == "" || cfg.ResolvedShell.Type == "" {
		t.Fatalf("ResolvedShell = %+v, want a concrete shell", cfg.ResolvedShell)
	}
}

func TestNormalizeHonorsPreferAndReportsResolution(t *testing.T) {
	preferred := runtimeexecutor.ShellIDSh
	if runtime.GOOS == "windows" {
		preferred = runtimeexecutor.ShellIDCmd
	}
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Shell.Prefer = []string{preferred}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if cfg.ResolvedShell.ID != preferred {
		t.Fatalf("resolved shell = %+v, want preferred %q", cfg.ResolvedShell, preferred)
	}
	if cfg.ResolvedShellResolution == nil || cfg.ResolvedShellResolution.Shell.ID != preferred {
		t.Fatalf("resolution matrix = %+v, want preferred %q", cfg.ResolvedShellResolution, preferred)
	}
}

func TestNormalizeRejectsUnknownPrefer(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Shell.Prefer = []string{"aicli-no-such-candidate"}
	if err := cfg.Normalize(); err == nil {
		t.Fatal("expected Normalize to reject an unknown shell.prefer candidate")
	}
}

func TestNormalizeAutoDetectFalseFailsWithoutSelector(t *testing.T) {
	disabled := false
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Shell.AutoDetect = &disabled
	if err := cfg.Normalize(); err == nil {
		t.Fatal("expected autoDetect=false without path/prefer to fail")
	}
}

func TestNormalizeStrictProbeFalseKeepsFallback(t *testing.T) {
	disabled := false
	permissive := false
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Shell.AutoDetect = &disabled
	cfg.Shell.StrictProbe = &permissive
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if strings.TrimSpace(cfg.ResolvedShell.Path) == "" {
		t.Fatalf("permissive resolution returned no fallback shell: %+v", cfg.ResolvedShell)
	}
}

func TestResolveShellUsesConfiguredProgram(t *testing.T) {
	program := "sh"
	wantType := runtimeexecutor.ShellTypeSh
	if runtime.GOOS == "windows" {
		program = "cmd"
		wantType = runtimeexecutor.ShellTypeCmd
	}
	shell, err := resolveShell(ShellConfig{Program: program})
	if err != nil {
		t.Fatalf("resolveShell(%q): %v", program, err)
	}
	if shell.Type != wantType {
		t.Fatalf("shell type = %q, want %q", shell.Type, wantType)
	}
	if strings.TrimSpace(shell.Path) == "" {
		t.Fatal("resolved shell path is empty")
	}
}

func TestResolveShellRejectsUnknownProgram(t *testing.T) {
	if _, err := resolveShell(ShellConfig{Program: "aicli-no-such-shell-xyz"}); err == nil {
		t.Fatal("expected an unknown shell to fail at startup")
	}
}

func TestResolveShellRejectsUnsupportedExecutable(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	if _, err := resolveShell(ShellConfig{Path: exe}); err == nil {
		t.Fatalf("expected %q to be rejected as an unsupported shell", filepath.Base(exe))
	}
}

func TestResolveShellUsesSharedAutoDetectChain(t *testing.T) {
	want, err := runtimeexecutor.ResolveShell(runtimeexecutor.ShellConfig{})
	if err != nil {
		t.Fatalf("executor.ResolveShell: %v", err)
	}
	got, err := resolveShell(ShellConfig{})
	if err != nil {
		t.Fatalf("resolveShell: %v", err)
	}
	wantShell := want.Shell
	ensureResolvedShellIdentity(&wantShell)
	if got.ID != wantShell.ID || got.Path != wantShell.Path || got.Type != wantShell.Type {
		t.Fatalf("remote-tools shell = %+v, want shared resolver %+v", got, wantShell)
	}
}

func TestResolveShellAcceptsCandidateIDAsProgram(t *testing.T) {
	if _, err := runtimeexecutor.ResolveShell(runtimeexecutor.ShellConfig{
		Prefer: []string{runtimeexecutor.ShellIDGitBash},
	}); err != nil {
		t.Skipf("git-bash is not available: %v", err)
	}
	shell, err := resolveShell(ShellConfig{Program: runtimeexecutor.ShellIDGitBash})
	if err != nil {
		t.Fatalf("resolveShell(git-bash): %v", err)
	}
	if shell.ID != runtimeexecutor.ShellIDGitBash || shell.Type != runtimeexecutor.ShellTypeBash {
		t.Fatalf("resolved shell = %+v, want git-bash/bash", shell)
	}
}

func TestResolveShellRejectsUnknownPrefer(t *testing.T) {
	if _, err := resolveShell(ShellConfig{Prefer: []string{"aicli-no-such-candidate"}}); err == nil {
		t.Fatal("expected an unknown shell.prefer candidate to fail")
	}
}

func TestResolveShellRejectsConflictingSelectors(t *testing.T) {
	if _, err := resolveShell(ShellConfig{
		Path:   "cmd",
		Prefer: []string{runtimeexecutor.ShellIDCmd},
	}); err == nil {
		t.Fatal("expected shell.path + shell.prefer to be rejected")
	}
	if _, err := resolveShell(ShellConfig{
		Program: "cmd",
		Prefer:  []string{runtimeexecutor.ShellIDCmd},
	}); err == nil {
		t.Fatal("expected shell.program + shell.prefer to be rejected")
	}
}

func TestCommandPolicyUsesResolvedShellDialect(t *testing.T) {
	base := ShellConfig{
		AllowedCommands:    []string{"*"},
		DenyShellMetachars: true,
	}
	bashPolicy, err := NewCommandPolicyForShell(base, runtimeexecutor.Shell{
		Path: "bash",
		Type: runtimeexecutor.ShellTypeBash,
	}, time.Second)
	if err != nil {
		t.Fatalf("bash policy: %v", err)
	}
	if rule, err := bashPolicy.CheckCommand(`echo a\;b`); err != nil {
		t.Fatalf("bash should treat \\; as a literal semicolon: %v (rule %s)", err, rule)
	}

	psPolicy, err := NewCommandPolicyForShell(base, runtimeexecutor.Shell{
		Path: "pwsh",
		Type: runtimeexecutor.ShellTypePwsh,
	}, time.Second)
	if err != nil {
		t.Fatalf("powershell policy: %v", err)
	}
	if _, err := psPolicy.CheckCommand(`echo a\;b`); err == nil {
		t.Fatal("PowerShell must reject the escaped semicolon as a real separator")
	}
}

func TestShellInvocationForSelectedShells(t *testing.T) {
	cases := []struct {
		name    string
		shell   runtimeexecutor.Shell
		command string
		want    []string
	}{
		{
			name:    "cmd",
			shell:   runtimeexecutor.Shell{Path: `C:\Windows\System32\cmd.exe`, Type: runtimeexecutor.ShellTypeCmd},
			command: "whoami",
			want:    []string{"/d", "/s", "/c", "whoami"},
		},
		{
			name:    "powershell",
			shell:   runtimeexecutor.Shell{Path: "pwsh", Type: runtimeexecutor.ShellTypePwsh},
			command: "Get-Location",
			want:    []string{"-NoProfile", "-NonInteractive", "-Command", wrapPowerShellExitCode("Get-Location")},
		},
		{
			name:    "posix",
			shell:   runtimeexecutor.Shell{Path: "/bin/bash", Type: runtimeexecutor.ShellTypeBash},
			command: "pwd",
			want:    []string{"-c", "pwd"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			program, args := shellInvocation(tc.shell, tc.command)
			if program != tc.shell.Path {
				t.Fatalf("program = %q, want %q", program, tc.shell.Path)
			}
			if strings.Join(args, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("args = %#v, want %#v", args, tc.want)
			}
		})
	}
}

func TestShellPromptHintNamesSelectedShell(t *testing.T) {
	cases := []struct {
		shell runtimeexecutor.Shell
		want  []string
	}{
		{
			shell: runtimeexecutor.Shell{Path: "pwsh.exe", Type: runtimeexecutor.ShellTypePwsh},
			want:  []string{"pwsh.exe", "PowerShell", "Get-Location", "Select-Object"},
		},
		{
			shell: runtimeexecutor.Shell{Path: "cmd.exe", Type: runtimeexecutor.ShellTypeCmd},
			want:  []string{"cmd.exe", "cmd.exe syntax", "%VAR%"},
		},
		{
			shell: runtimeexecutor.Shell{Path: "/bin/bash", Type: runtimeexecutor.ShellTypeBash},
			want:  []string{"bash", "POSIX"},
		},
	}
	for _, tc := range cases {
		hint := shellPromptHint(tc.shell)
		for _, want := range tc.want {
			if !strings.Contains(hint, want) {
				t.Fatalf("hint %q lacks %q", hint, want)
			}
		}
	}
}

func TestWindowsShellRawCmdLineQuotesShellPath(t *testing.T) {
	got := windowsShellRawCmdLine(`C:\Program Files\cmd.exe`, `echo "hi"`)
	want := `"C:\Program Files\cmd.exe" /d /s /c "echo "hi""`
	if got != want {
		t.Fatalf("raw cmdline = %q, want %q", got, want)
	}
}

func TestPowerShellInvocationPreservesRealExitCode(t *testing.T) {
	// The production default is now the shared auto-detect chain (git-bash on
	// this class of Windows host), so pin PowerShell explicitly to keep this
	// regression covered.
	shell, err := resolveShell(ShellConfig{Prefer: []string{runtimeexecutor.ShellIDPwsh}})
	if err != nil {
		shell, err = resolveShell(ShellConfig{Prefer: []string{runtimeexecutor.ShellIDPowerShell}})
	}
	if err != nil {
		t.Skipf("PowerShell is not available: %v", err)
	}
	cases := []struct {
		name    string
		command string
		want    int
	}{
		{name: "native exit code", command: "cmd /c exit 3", want: 3},
		{name: "cmdlet failure", command: `Get-Item "C:\aicli-definitely-missing-file-xyz"`, want: 1},
		{name: "success", command: "Write-Output ok", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			program, args := shellInvocation(shell, tc.command)
			cmd := exec.Command(program, args...)
			cmd.Env = os.Environ()
			_ = cmd.Run()
			got := 0
			if cmd.ProcessState != nil {
				got = cmd.ProcessState.ExitCode()
			}
			if got != tc.want {
				t.Fatalf("exit code = %d, want %d", got, tc.want)
			}
		})
	}
}
