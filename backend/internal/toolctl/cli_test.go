package toolctl

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestCLI(t *testing.T) (*CLI, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte("hello\nworld\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	return &CLI{Stdout: stdout, Stderr: stderr}, stdout, stderr, workspace
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
