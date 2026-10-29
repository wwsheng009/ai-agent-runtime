package shellexec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// TestShellExecHelperProcess is a re-exec helper. It keeps the test portable:
// the same binary provides a child with controlled stdout, stderr, exit code,
// environment and lifetime.
func TestShellExecHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_SHELLEXEC_HELPER") != "1" {
		return
	}
	mode := os.Getenv("SHELLEXEC_HELPER_MODE")
	switch mode {
	case "echo":
		fmt.Fprintln(os.Stdout, "stdout-marker")
		fmt.Fprintln(os.Stderr, "stderr-marker")
		os.Exit(0)
	case "exit3":
		fmt.Fprintln(os.Stdout, "before-exit")
		os.Exit(3)
	case "env":
		fmt.Fprintln(os.Stdout, "allowed="+os.Getenv("SHELLEXEC_ALLOWED"))
		fmt.Fprintln(os.Stdout, "secret="+os.Getenv("SHELLEXEC_SECRET"))
		os.Exit(0)
	case "sleep":
		time.Sleep(60 * time.Second)
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

func helperInvocation() func(string) (string, []string) {
	return func(string) (string, []string) {
		return os.Args[0], []string{"-test.run=TestShellExecHelperProcess", "--"}
	}
}

func helperEnv(mode string, extra ...string) []string {
	env := append([]string(nil), os.Environ()...)
	env = append(env,
		"GO_WANT_SHELLEXEC_HELPER=1",
		"SHELLEXEC_HELPER_MODE="+mode,
	)
	return append(env, extra...)
}

func TestRunCapturesStdoutStderrAndExitCode(t *testing.T) {
	result, err := Run(context.Background(), Options{
		Command:         "echo",
		Env:             helperEnv("echo"),
		Invocation:      helperInvocation(),
		DisableArtifact: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Started {
		t.Fatal("expected the helper process to start")
	}
	if result.ExitCode != 0 || result.RunError != nil {
		t.Fatalf("exit=%d err=%v", result.ExitCode, result.RunError)
	}
	if !strings.Contains(result.Capture.Output, "stdout-marker") ||
		!strings.Contains(result.Capture.Output, "stderr-marker") {
		t.Fatalf("combined output missing a stream: %q", result.Capture.Output)
	}
}

func TestRunReportsNonZeroExitAsProcessResult(t *testing.T) {
	result, err := Run(context.Background(), Options{
		Command:         "exit3",
		Env:             helperEnv("exit3"),
		Invocation:      helperInvocation(),
		DisableArtifact: true,
	})
	if err == nil {
		t.Fatal("expected a process error for exit 3")
	}
	if result.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3 (err=%v)", result.ExitCode, err)
	}
	if !strings.Contains(result.Capture.Output, "before-exit") {
		t.Fatalf("expected partial output before exit, got %q", result.Capture.Output)
	}
}

func TestRunPassesOnlyTheProvidedEnvironment(t *testing.T) {
	t.Setenv("SHELLEXEC_SECRET", "leaked")
	// Build the child environment from scratch: helperEnv would copy the parent
	// environment and mask the allowlist contract this test pins.
	env := []string{
		"GO_WANT_SHELLEXEC_HELPER=1",
		"SHELLEXEC_HELPER_MODE=env",
		"SHELLEXEC_ALLOWED=visible",
	}
	result, err := Run(context.Background(), Options{
		Command:         "env",
		Env:             env,
		Invocation:      helperInvocation(),
		DisableArtifact: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	output := result.Capture.Output
	if !strings.Contains(output, "allowed=visible") {
		t.Fatalf("allowlisted variable missing: %q", output)
	}
	if strings.Contains(output, "secret=leaked") {
		t.Fatalf("non-allowlisted variable leaked: %q", output)
	}
}

func TestRunReportsTheSelectedShell(t *testing.T) {
	selected := runtimeexecutor.Shell{Path: "custom-shell", Type: runtimeexecutor.ShellTypeBash}
	result, err := Run(context.Background(), Options{
		Command:         "echo",
		Env:             helperEnv("echo"),
		Invocation:      helperInvocation(),
		Shell:           selected,
		DisableArtifact: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Shell != selected {
		t.Fatalf("result shell = %+v, want %+v", result.Shell, selected)
	}
}

func TestPrefixPowerShellArgsUsesSelectedShell(t *testing.T) {
	args := []string{"-NoProfile", "-NonInteractive", "-Command", "Get-Location"}
	got := prefixPowerShellArgs(args, runtimeexecutor.Shell{Type: runtimeexecutor.ShellTypePwsh}, "pwsh")
	if len(got) != len(args) {
		t.Fatalf("prefixPowerShellArgs changed arg count: %#v", got)
	}
	if !strings.HasPrefix(got[len(got)-1], runtimeexecutor.PowerShellCommandPrefix) {
		t.Fatalf("PowerShell UTF-8 prefix missing: %#v", got)
	}
}

func TestRunTimeoutTerminatesTheTree(t *testing.T) {
	if runtime.GOOS == "windows" && os.Getenv("CI") == "" {
		// The helper is still valid on Windows; the Job Object path is covered
		// by executor tests. Keep this core test fast on local Windows runs.
		t.Log("running timeout test on Windows helper process")
	}
	start := time.Now()
	result, err := Run(context.Background(), Options{
		Command:         "sleep",
		Env:             helperEnv("sleep"),
		Invocation:      helperInvocation(),
		Timeout:         500 * time.Millisecond,
		DisableArtifact: true,
	})
	elapsed := time.Since(start)
	if elapsed > 20*time.Second {
		t.Fatalf("timeout did not bound the call: %s", elapsed)
	}
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !result.TimedOut {
		t.Fatalf("expected TimedOut=true, got %+v", result)
	}
	if !result.GuardReport.TreeKill {
		t.Fatalf("expected the process tree to be killed, got %+v", result.GuardReport)
	}
}

func TestRunCancellationTerminatesTheTree(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(400 * time.Millisecond)
		cancel()
	}()
	result, err := Run(ctx, Options{
		Command:         "sleep",
		Env:             helperEnv("sleep"),
		Invocation:      helperInvocation(),
		DisableArtifact: true,
	})
	if err == nil {
		t.Fatal("expected a cancellation error")
	}
	if !result.Cancelled {
		t.Fatalf("expected Cancelled=true, got %+v", result)
	}
	if !result.GuardReport.TreeKill {
		t.Fatalf("expected the process tree to be killed, got %+v", result.GuardReport)
	}
}

func TestRunArtifactTeeIsOptional(t *testing.T) {
	root := t.TempDir()
	payload := strings.Repeat("artifact line\n", 200)

	withoutArtifact, err := Run(context.Background(), Options{
		Command:         "echo",
		Env:             helperEnv("echo"),
		Invocation:      helperInvocation(),
		CaptureLimit:    64,
		ArtifactRoot:    root,
		DisableArtifact: true,
	})
	if err != nil {
		t.Fatalf("Run without artifact: %v", err)
	}
	if withoutArtifact.ArtifactPath != "" {
		t.Fatalf("DisableArtifact must not write a path, got %q", withoutArtifact.ArtifactPath)
	}

	// Use a real command so the artifact tee receives a known payload.
	script := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(script, []byte(payload), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	command := "cat " + shellQuote(script)
	if runtime.GOOS == "windows" {
		command = "type " + script
	}
	withArtifact, err := Run(context.Background(), Options{
		Command:       command,
		CaptureLimit:  128,
		ArtifactRoot:  root,
		ArtifactScope: "shellexec-test",
	})
	if err != nil {
		t.Fatalf("Run with artifact: %v", err)
	}
	if withArtifact.ArtifactPath == "" {
		t.Fatal("expected a raw-output artifact path")
	}
	data, readErr := os.ReadFile(withArtifact.ArtifactPath)
	if readErr != nil {
		t.Fatalf("read artifact: %v", readErr)
	}
	if !strings.Contains(string(data), "artifact line") {
		t.Fatalf("artifact does not hold the complete capture: %q", string(data))
	}
}

func TestFoldKeepsHeadAndTail(t *testing.T) {
	text := strings.Repeat("h", 4096) + "\nMIDDLE\n" + strings.Repeat("t", 4096)
	folded, stats, didFold := Fold(text, 1024)
	if !didFold {
		t.Fatal("expected a fold")
	}
	if len(folded) > 1024 {
		t.Fatalf("folded length %d exceeds budget", len(folded))
	}
	if stats.HeadBytes <= 0 || stats.TailBytes <= 0 {
		t.Fatalf("expected both ends: %+v", stats)
	}
	if strings.Contains(folded, "MIDDLE") {
		t.Fatal("middle content leaked into the window")
	}
	if !strings.HasPrefix(folded, "hhhh") || !strings.HasSuffix(folded, "tttt") {
		t.Fatalf("expected head and tail, got %q", folded)
	}
}

func shellQuote(value string) string {
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
