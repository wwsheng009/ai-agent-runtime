package executor

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func boolPtr(value bool) *bool { return &value }

func TestShellConfigDefaults(t *testing.T) {
	var cfg ShellConfig
	if !cfg.ShellAutoDetect() {
		t.Fatalf("autodetect must default to true")
	}
	if !cfg.ShellStrictProbe() {
		t.Fatalf("strictProbe must default to true")
	}
	if got := cfg.EffectiveProbeTimeout(); got != DefaultShellProbeTimeout {
		t.Fatalf("probe timeout default = %s, want %s", got, DefaultShellProbeTimeout)
	}
	if got := cfg.EffectiveScriptDelivery(); got != ScriptDeliveryStdin {
		t.Fatalf("script delivery default = %q, want stdin", got)
	}

	// An explicit `prefer:` block must not flip autodetect off as a side
	// effect of YAML decoding.
	partial := ShellConfig{Prefer: []string{ShellIDGitBash}}
	if !partial.ShellAutoDetect() {
		t.Fatalf("prefer-only config must keep autodetect on")
	}
	if partial.ShellAutoDetect() == *(boolPtr(false)) {
		t.Fatalf("unreachable")
	}
	off := ShellConfig{AutoDetect: boolPtr(false)}
	if off.ShellAutoDetect() {
		t.Fatalf("explicit autoDetect=false must win")
	}
}

func TestValidateShellConfig(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ShellConfig
		wantErr string
	}{
		{name: "empty", cfg: ShellConfig{}},
		{name: "known ids", cfg: ShellConfig{Prefer: []string{"git-bash", "busybox", "powershell"}}},
		{name: "unknown id", cfg: ShellConfig{Prefer: []string{"fish"}}, wantErr: "unknown candidate"},
		{name: "bad delivery", cfg: ShellConfig{ScriptDelivery: "pipe"}, wantErr: "scriptDelivery"},
		{name: "negative timeout", cfg: ShellConfig{ProbeTimeout: -time.Second}, wantErr: "probeTimeout"},
		{name: "path is not a shell", cfg: ShellConfig{Path: `C:\tmp\evil.exe`}, wantErr: "known shell binary"},
		{name: "path is bash", cfg: ShellConfig{Path: `C:\Program Files\Git\bin\bash.exe`}},
		{name: "env path override", cfg: ShellConfig{Env: map[string]string{"PATH": "/x"}}, wantErr: "PATH"},
		{name: "env newline", cfg: ShellConfig{Env: map[string]string{"LANG": "a\nb"}}, wantErr: "newlines"},
		{name: "env bad key", cfg: ShellConfig{Env: map[string]string{"1BAD": "x"}}, wantErr: "match"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateShellConfig(&tc.cfg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want contains %q", err, tc.wantErr)
			}
		})
	}
}

func TestShellConfigFingerprintTracksResolutionInputs(t *testing.T) {
	base := ShellConfig{Prefer: []string{"git-bash"}}
	same := ShellConfig{Prefer: []string{" GIT-BASH "}}
	if base.Fingerprint() != same.Fingerprint() {
		t.Fatalf("fingerprint must normalise candidate ids")
	}
	different := ShellConfig{Prefer: []string{"git-bash", "cmd"}}
	if base.Fingerprint() == different.Fingerprint() {
		t.Fatalf("fingerprint must change with prefer order")
	}
}

func TestDeriveExecInvocationUsesStdinForLongPosixScripts(t *testing.T) {
	shell := Shell{Path: "/bin/bash", Type: ShellTypeBash}
	short := "echo hi"
	argv, stdin := shell.DeriveExecInvocation(short, ScriptDeliveryStdin)
	if stdin != "" {
		t.Fatalf("short command must stay on argv, got stdin %q", stdin)
	}
	assertShellArgs(t, argv, []string{"/bin/bash", "-c", short})

	long := strings.Repeat("echo line\n", ShellStdinScriptThreshold)
	argv, stdin = shell.DeriveExecInvocation(long, ScriptDeliveryStdin)
	assertShellArgs(t, argv, []string{"/bin/bash", "--noprofile", "--norc", "-s"})
	if stdin != long {
		t.Fatalf("long command must travel over stdin")
	}

	// PowerShell stays on -EncodedCommand until `-Command -` is verified.
	ps := Shell{Path: "pwsh", Type: ShellTypePwsh}
	argv, stdin = ps.DeriveExecInvocation(strings.Repeat("Write-Output x;", 2000), ScriptDeliveryStdin)
	if stdin != "" {
		t.Fatalf("PowerShell must not switch to stdin yet")
	}
	if !ShellArgsUseEncodedCommand(argv) {
		t.Fatalf("PowerShell long script must still use -EncodedCommand, got %#v", argv)
	}

	// argv delivery is the diagnostic mode: no stdin, always argv.
	argv, stdin = shell.DeriveExecInvocation(long, ScriptDeliveryArgv)
	if stdin != "" {
		t.Fatalf("argv delivery must never use stdin")
	}
	if argv[len(argv)-1] != long {
		t.Fatalf("argv delivery must append the script")
	}
}

func TestBusyboxPrefixShapesArgv(t *testing.T) {
	shell := Shell{Path: `C:\vendor\busybox\busybox.exe`, Type: ShellTypeSh, Prefix: []string{"sh"}, ID: ShellIDBusybox}
	assertShellArgs(t, shell.DeriveExecArgs("echo hi", false),
		[]string{`C:\vendor\busybox\busybox.exe`, "sh", "-c", "echo hi"})
	if got := shell.Dialect; got != "" {
		t.Fatalf("dialect is set by the resolver, not the literal")
	}
	if DialectFor(ShellTypeSh, ShellIDBusybox) != ShellDialectPosixNarrow {
		t.Fatalf("busybox must be posix-narrow")
	}
}

func TestResolvePlanOrder(t *testing.T) {
	cfg := ShellConfig{Prefer: []string{"powershell", "git-bash"}}
	plan := resolvePlan(cfg)
	if len(plan) != 2 || plan[0].id != ShellIDPowerShell || plan[1].id != ShellIDGitBash {
		t.Fatalf("prefer order must be preserved, got %#v", plan)
	}
	// No tail append: reproducibility beats a longer chain.
	if plan[len(plan)-1].id != ShellIDGitBash {
		t.Fatalf("prefer chain must not append defaults")
	}

	locked := ShellConfig{AutoDetect: boolPtr(false)}
	if plan := resolvePlan(locked); len(plan) != 0 {
		t.Fatalf("autoDetect=false without prefer must yield an empty plan, got %#v", plan)
	}
}

func TestMergeEnvOverrides(t *testing.T) {
	base := []string{"PATH=/bin", "LANG=en_US.UTF-8", "NO_COLOR="}
	merged := MergeEnvOverrides(base, map[string]string{"LANG": "C.UTF-8", "LC_ALL": "C.UTF-8"})
	joined := strings.Join(merged, "|")
	if !strings.Contains(joined, "LANG=C.UTF-8") || !strings.Contains(joined, "LC_ALL=C.UTF-8") {
		t.Fatalf("overrides not applied: %s", joined)
	}
	if strings.Contains(joined, "LANG=en_US.UTF-8") {
		t.Fatalf("old value survived: %s", joined)
	}
	if !strings.Contains(joined, "PATH=/bin") {
		t.Fatalf("unrelated entries must be preserved: %s", joined)
	}
	if count := strings.Count(joined, "LANG="); count != 1 {
		t.Fatalf("duplicate keys after merge: %s", joined)
	}
}

// TestProbeRealGitBash is a machine-dependent smoke test: it only asserts that
// the resolver never selects the WSL stub and that a POSIX shell is found when
// one is installed.
func TestProbeRealGitBash(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only")
	}
	cfg := ShellConfig{Prefer: []string{ShellIDGitBash, ShellIDMSYSBash, ShellIDBusybox, ShellIDPowerShell, ShellIDCmd}}
	resolution, err := ResolveShellForce(cfg)
	if err != nil {
		t.Fatalf("resolution failed: %v", err)
	}
	if resolution.Shell.ID == ShellIDWSL {
		t.Fatalf("WSL must never win implicitly")
	}
	for _, candidate := range resolution.Candidates {
		if candidate.Status == ShellCandidatePass && isWSLBashPath(candidate.Path) && candidate.ID != ShellIDWSL {
			t.Fatalf("candidate %s passed on the WSL stub: %s", candidate.ID, candidate.Path)
		}
	}
	if resolution.Shell.Type == "" || resolution.Shell.Path == "" {
		t.Fatalf("resolver returned an empty shell: %#v", resolution.Shell)
	}
	t.Logf("selected %s (%s) dialect=%s probe=%dms", resolution.Shell.ID, resolution.Shell.Path, resolution.Shell.Dialect, resolution.ProbeMS)
}
