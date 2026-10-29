package remotetools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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

func TestResolveShellUsesConfiguredProgram(t *testing.T) {
	program := "sh"
	wantType := runtimeexecutor.ShellTypeSh
	if runtime.GOOS == "windows" {
		program = "cmd"
		wantType = runtimeexecutor.ShellTypeCmd
	}
	shell, err := resolveShell(program)
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
	if _, err := resolveShell("aicli-no-such-shell-xyz"); err == nil {
		t.Fatal("expected an unknown shell to fail at startup")
	}
}

func TestResolveShellRejectsUnsupportedExecutable(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	if _, err := resolveShell(exe); err == nil {
		t.Fatalf("expected %q to be rejected as an unsupported shell", filepath.Base(exe))
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
	shell, err := resolveShell("")
	if err != nil {
		t.Fatalf("resolveShell: %v", err)
	}
	if shell.Type != runtimeexecutor.ShellTypePwsh && shell.Type != runtimeexecutor.ShellTypePowerShell {
		t.Skip("PowerShell is not the resolved shell")
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
