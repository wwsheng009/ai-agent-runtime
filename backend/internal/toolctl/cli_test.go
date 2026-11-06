package toolctl

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/shelljob"
)

// stubShellJobRuntime keeps CLI contract tests focused on policy and argument
// handling instead of spawning a real background job. Production toolctl still
// leaves the shell tool fail-closed unless a host injects a runtime.
type stubShellJobRuntime struct {
	output string
}

func (*stubShellJobRuntime) SubmitShell(context.Context, string, string, string) (string, string, error) {
	return "job_test", "running", nil
}

func (*stubShellJobRuntime) WaitShell(context.Context, string, time.Duration) (shelljob.Snapshot, error) {
	exitCode := 0
	return shelljob.Snapshot{ID: "job_test", Status: "completed", ExitCode: &exitCode}, nil
}

func (s *stubShellJobRuntime) ReadShell(context.Context, string, int64, int) (shelljob.Output, error) {
	exitCode := 0
	return shelljob.Output{
		Output:     s.output,
		NextOffset: int64(len(s.output)),
		Status:     "completed",
		ExitCode:   &exitCode,
	}, nil
}

func (*stubShellJobRuntime) CancelShell(context.Context, string) error { return nil }

func (*stubShellJobRuntime) ForgetShell(context.Context, string, string) error { return nil }

func newTestCLI(t *testing.T) (*CLI, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte("hello\nworld\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	return &CLI{
		Stdout:          stdout,
		Stderr:          stderr,
		shellJobRuntime: &stubShellJobRuntime{output: "ok\n"},
	}, stdout, stderr, workspace
}

func TestListDefaultsToReadOnly(t *testing.T) {
	cli, stdout, stderr, workspace := newTestCLI(t)
	code := cli.Run([]string{"list", "--workspace", workspace, "--json"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var defs []toolDescription
	if err := json.Unmarshal(stdout.Bytes(), &defs); err != nil {
		t.Fatal(err)
	}
	for _, def := range defs {
		if def.Name == "write" || def.Name == "shell" {
			t.Fatalf("read-only listing exposed %q", def.Name)
		}
	}
}

func TestRunViewReturnsJSON(t *testing.T) {
	cli, stdout, stderr, workspace := newTestCLI(t)
	code := cli.Run([]string{
		"run", "view",
		"--workspace", workspace,
		"--args-json", `{"file_path":"hello.txt"}`,
		"--json",
	})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var result resultView
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || !strings.Contains(result.Output, "hello") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestRunAliasResolvesToCanonicalTool(t *testing.T) {
	cli, stdout, stderr, workspace := newTestCLI(t)
	code := cli.Run([]string{
		"run", "read",
		"--workspace", workspace,
		"--args-json", `{"file_path":"hello.txt"}`,
		"--json",
	})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var result resultView
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Tool != "view" || !result.OK {
		t.Fatalf("unexpected alias result: %+v", result)
	}
}

func TestWriteRequiresExplicitFlag(t *testing.T) {
	cli, _, _, workspace := newTestCLI(t)
	code := cli.Run([]string{
		"run", "write",
		"--workspace", workspace,
		"--args-json", `{"file_path":"out.txt","content":"x"}`,
	})
	if code != ExitPolicy {
		t.Fatalf("exit=%d, want %d", code, ExitPolicy)
	}
	if _, err := os.Stat(filepath.Join(workspace, "out.txt")); !os.IsNotExist(err) {
		t.Fatal("write must not happen without --allow-write")
	}
}

func TestWriteWithFlagApplies(t *testing.T) {
	cli, _, stderr, workspace := newTestCLI(t)
	code := cli.Run([]string{
		"run", "write",
		"--workspace", workspace,
		"--allow-write",
		"--args-json", `{"file_path":"out.txt","content":"written"}`,
	})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(workspace, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "written" {
		t.Fatalf("content=%q", data)
	}
}

func TestUnknownToolIsPolicyExit(t *testing.T) {
	cli, _, _, workspace := newTestCLI(t)
	code := cli.Run([]string{"run", "missing", "--workspace", workspace})
	if code != ExitPolicy {
		t.Fatalf("exit=%d, want %d", code, ExitPolicy)
	}
}

func TestInvalidJSONIsUsageExit(t *testing.T) {
	cli, _, _, workspace := newTestCLI(t)
	code := cli.Run([]string{"run", "view", "--workspace", workspace, "--args-json", "{"})
	if code != ExitUsage {
		t.Fatalf("exit=%d, want %d", code, ExitUsage)
	}
}

// TestShellRequiresYesForApproval pins the two-step CLI contract for the exec
// family: --allow-command exposes the tool, --yes resolves the ordinary
// approval ask. Neither flag can bypass the engine's hard gates.
func TestShellRequiresYesForApproval(t *testing.T) {
	cli, _, stderr, workspace := newTestCLI(t)
	// `git init` is a write-classified shell command: default mode asks for it,
	// which is exactly the ask --yes is meant to resolve.
	command := `{"command":"git init probe"}`
	code := cli.Run([]string{
		"run", "shell",
		"--workspace", workspace,
		"--allow-command",
		"--args-json", command,
	})
	if code != ExitPolicy {
		t.Fatalf("exit=%d, want %d (stderr=%s)", code, ExitPolicy, stderr.String())
	}
	if !strings.Contains(stderr.String(), "approval_required") {
		t.Fatalf("expected approval guidance, got %q", stderr.String())
	}

	code = cli.Run([]string{
		"run", "shell",
		"--workspace", workspace,
		"--allow-command",
		"--yes",
		"--args-json", command,
		"--json",
	})
	if code != ExitOK {
		t.Fatalf("exit=%d, want %d (stderr=%s)", code, ExitOK, stderr.String())
	}
}

// TestHardAskIsNotApprovedByYes proves --yes is not a blanket bypass: a
// sensitive write still requires explicit confirmation the CLI does not have.
func TestHardAskIsNotApprovedByYes(t *testing.T) {
	cli, _, stderr, workspace := newTestCLI(t)
	code := cli.Run([]string{
		"run", "write",
		"--workspace", workspace,
		"--allow-write",
		"--yes",
		"--args-json", `{"file_path":".env","content":"TOKEN=x"}`,
	})
	if code != ExitPolicy {
		t.Fatalf("exit=%d, want %d (stderr=%s)", code, ExitPolicy, stderr.String())
	}
	if !strings.Contains(stderr.String(), "hard_ask") && !strings.Contains(stderr.String(), "sensitive_write") {
		t.Fatalf("expected a hard-ask/sensitive-write refusal, got %q", stderr.String())
	}
}

// TestPermissionModePlanDeniesWrites pins the shared engine mode semantics.
func TestPermissionModePlanDeniesWrites(t *testing.T) {
	cli, _, stderr, workspace := newTestCLI(t)
	code := cli.Run([]string{
		"run", "write",
		"--workspace", workspace,
		"--allow-write",
		"--yes",
		"--permission-mode", "plan",
		"--args-json", `{"file_path":"out.txt","content":"x"}`,
	})
	if code != ExitPolicy {
		t.Fatalf("exit=%d, want %d (stderr=%s)", code, ExitPolicy, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workspace, "out.txt")); !os.IsNotExist(err) {
		t.Fatal("plan mode must not write")
	}
}

// TestJSONContractIncludesErrorFields pins the documented result envelope.
func TestJSONContractIncludesErrorFields(t *testing.T) {
	cli, stdout, _, workspace := newTestCLI(t)
	code := cli.Run([]string{
		"run", "view",
		"--workspace", workspace,
		"--args-json", `{"file_path":"missing.txt"}`,
		"--json",
	})
	if code == ExitOK {
		t.Fatal("expected a failure exit")
	}
	var result map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v (raw=%s)", err, stdout.String())
	}
	for _, key := range []string{"tool", "ok", "error_code", "next_action", "retriable"} {
		if _, exists := result[key]; !exists {
			t.Errorf("JSON result missing %q: %s", key, stdout.String())
		}
	}
	if result["tool"] != "view" || result["ok"] != false {
		t.Fatalf("unexpected result: %s", stdout.String())
	}
}

// TestListExcludeToolsNarrowsTheSurface pins the compatibility flag used by
// the legacy toolkit-mcp-server wrapper.
func TestListExcludeToolsNarrowsTheSurface(t *testing.T) {
	cli, stdout, stderr, workspace := newTestCLI(t)
	code := cli.Run([]string{"list", "--workspace", workspace, "--json", "--exclude-tools", "list_dir,glob"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var defs []toolDescription
	if err := json.Unmarshal(stdout.Bytes(), &defs); err != nil {
		t.Fatal(err)
	}
	for _, def := range defs {
		if def.Name == "list_dir" || def.Name == "glob" {
			t.Fatalf("excluded tool %q is still listed", def.Name)
		}
	}
}

// TestServeRejectsUnknownTransport proves the CLI fails closed on a transport
// typo instead of silently starting a different surface.
func TestServeRejectsUnknownTransport(t *testing.T) {
	cli, _, stderr, workspace := newTestCLI(t)
	code := cli.Run([]string{"serve", "--workspace", workspace, "--mcp", "carrier-pigeon"})
	if code != ExitUsage {
		t.Fatalf("exit=%d, want %d (stderr=%s)", code, ExitUsage, stderr.String())
	}
}

// TestServeHTTPRequiresTokenForNonLoopback pins the transport security
// contract: an unauthenticated non-loopback listener is refused before bind.
func TestServeHTTPRequiresTokenForNonLoopback(t *testing.T) {
	cli, _, stderr, workspace := newTestCLI(t)
	code := cli.Run([]string{
		"serve", "--workspace", workspace,
		"--mcp", "http",
		"--listen", "0.0.0.0:0",
	})
	if code != ExitUsage {
		t.Fatalf("exit=%d, want %d (stderr=%s)", code, ExitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "requires") {
		t.Fatalf("expected an auth/TLS requirement, got %q", stderr.String())
	}
}

// TestDefaultProfileStaysReadOnlyCore pins that the default CLI does not
// silently pick up the local-only network/image/stateful tools.
func TestDefaultProfileStaysReadOnlyCore(t *testing.T) {
	cli, stdout, stderr, workspace := newTestCLI(t)
	code := cli.Run([]string{"list", "--workspace", workspace, "--json"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var defs []toolDescription
	if err := json.Unmarshal(stdout.Bytes(), &defs); err != nil {
		t.Fatal(err)
	}
	for _, def := range defs {
		switch def.Name {
		case "download", "web_search", "sourcegraph", "openai_image_generate", "todos", "aicli_exec":
			t.Fatalf("default profile exposed local-only tool %q", def.Name)
		}
	}
}

// TestOpsProfileRequiresCapabilityFlagsForLocalOnlyTools pins the explicit
// opt-in boundary for the compatibility surface.
func TestOpsProfileRequiresCapabilityFlagsForLocalOnlyTools(t *testing.T) {
	cli, stdout, stderr, workspace := newTestCLI(t)
	code := cli.Run([]string{"list", "--workspace", workspace, "--profile", "ops", "--json"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var defs []toolDescription
	if err := json.Unmarshal(stdout.Bytes(), &defs); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, def := range defs {
		seen[def.Name] = true
	}
	if seen["download"] || seen["openai_image_generate"] || seen["aicli_exec"] {
		t.Fatalf("ops without opt-in flags exposed write/exec local tools: %v", seen)
	}
	if !seen["web_search"] || !seen["fetch"] {
		t.Fatalf("ops should expose read-only network helpers: %v", seen)
	}
}
