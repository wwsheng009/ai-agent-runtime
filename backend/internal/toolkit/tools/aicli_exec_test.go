package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAICLIExecBuildArgv_DefaultsDoNotInjectConfig(t *testing.T) {
	req, err := parseAICLIExecRequest(map[string]interface{}{
		"prompt": "summarize this",
	})
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}

	got := buildAICLIExecArgv(req)
	want := []string{"exec", "--output", "text", "--disable-tools", "--timeout", "2m0s"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected argv:\n got: %#v\nwant: %#v", got, want)
	}
	if strings.Contains(strings.Join(got, "\x00"), "summarize this") {
		t.Fatalf("prompt should be passed on stdin, not argv: %#v", got)
	}
}

func TestAICLIExecBuildArgv_ExplicitConfigIsRootFlagBeforeExec(t *testing.T) {
	req, err := parseAICLIExecRequest(map[string]interface{}{
		"prompt":        "run review",
		"config":        `C:\Users\vince\.aicli\config.yaml`,
		"provider":      "mimo_anthropic",
		"model":         "mimo-v2.5-pro",
		"log_dir":       `E:\logs\aicli-child`,
		"session_dir":   `E:\sessions\aicli-child`,
		"user":          "tester",
		"title":         "skill bridge run",
		"output":        "json",
		"envelope":      true,
		"disable_tools": false,
		"timeout":       "5m",
		"debug_http":    true,
		"fail_fast":     true,
		"skills_dir":    []interface{}{`E:\projects\ai\ai-agent-runtime\.agents\skills`},
		"skills_mode":   "prefer",
	})
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}

	got := buildAICLIExecArgv(req)
	want := []string{
		"--config", `C:\Users\vince\.aicli\config.yaml`,
		"--envelope",
		"exec",
		"--provider", "mimo_anthropic",
		"--model", "mimo-v2.5-pro",
		"--log-dir", `E:\logs\aicli-child`,
		"--session-dir", `E:\sessions\aicli-child`,
		"--user", "tester",
		"--title", "skill bridge run",
		"--output", "json",
		"--skills-mode", "prefer",
		"--skills-dir", `E:\projects\ai\ai-agent-runtime\.agents\skills`,
		"--debug-http",
		"--fail-fast",
		"--timeout", "5m0s",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected argv:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestAICLIExecParseTimeoutMSOverridesTimeoutText(t *testing.T) {
	req, err := parseAICLIExecRequest(map[string]interface{}{
		"prompt":     "hello",
		"timeout":    "5m",
		"timeout_ms": float64(1500),
	})
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if req.Timeout != 1500*time.Millisecond {
		t.Fatalf("expected 1500ms timeout, got %v", req.Timeout)
	}
}

func TestAICLIExecRejectsRecursiveCallByDefault(t *testing.T) {
	t.Setenv(aicliExecNestedDepthEnvVar, "1")
	tool := NewAICLIExecTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"prompt": "nested",
	})
	if err != nil {
		t.Fatalf("execute returned unexpected transport error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected recursive call to fail, got %#v", result)
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "递归调用") {
		t.Fatalf("expected recursion error, got %#v", result)
	}
}

func TestAppendAICLIExecDepthReplacesExistingValue(t *testing.T) {
	t.Setenv(aicliExecNestedDepthEnvVar, "2")

	got := appendAICLIExecDepth([]string{
		"PATH=x",
		aicliExecNestedDepthEnvVar + "=2",
	})
	want := []string{
		"PATH=x",
		aicliExecNestedDepthEnvVar + "=3",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected env:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestResolveAICLIExecutableExplicitMissing(t *testing.T) {
	missing := "definitely-missing-aicli-executable"
	if _, err := os.Stat(missing); err == nil {
		t.Fatalf("test fixture unexpectedly exists: %s", missing)
	}
	if _, err := resolveAICLIExecutable(missing); err == nil {
		t.Fatalf("expected missing explicit executable to fail")
	}
}

func TestAICLIExecExecute_InvokesExecutableWithStdinArgvCwdAndEnv(t *testing.T) {
	tmp := t.TempDir()
	fakeExe := buildFakeAICLIExecutable(t, tmp)
	workdir := filepath.Join(tmp, "work")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	tool := NewAICLIExecTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"executable_path": fakeExe,
		"prompt":          "hello from stdin",
		"cwd":             workdir,
		"config":          filepath.Join(tmp, "config.yaml"),
		"provider":        "test-provider",
		"model":           "test-model",
		"log_dir":         filepath.Join(tmp, "logs"),
		"session_dir":     filepath.Join(tmp, "sessions"),
		"user":            "tester",
		"title":           "child run",
		"disable_tools":   true,
		"output":          "text",
		"timeout":         "10s",
		"debug_http":      true,
		"fail_fast":       true,
	})
	if err != nil {
		t.Fatalf("Execute transport error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected successful result, got %#v", result)
	}

	var payload struct {
		Args        []string `json:"args"`
		Stdin       string   `json:"stdin"`
		CWD         string   `json:"cwd"`
		NestedDepth string   `json:"nested_depth"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Content)), &payload); err != nil {
		t.Fatalf("expected JSON payload from fake aicli, got %q: %v", result.Content, err)
	}
	if payload.Stdin != "hello from stdin" {
		t.Fatalf("expected prompt on stdin, got %q", payload.Stdin)
	}
	if filepath.Clean(payload.CWD) != filepath.Clean(workdir) {
		t.Fatalf("expected cwd %q, got %q", workdir, payload.CWD)
	}
	if payload.NestedDepth != "1" {
		t.Fatalf("expected nested depth env to be 1, got %q", payload.NestedDepth)
	}
	wantArgs := []string{
		"--config", filepath.Join(tmp, "config.yaml"),
		"exec",
		"--provider", "test-provider",
		"--model", "test-model",
		"--log-dir", filepath.Join(tmp, "logs"),
		"--session-dir", filepath.Join(tmp, "sessions"),
		"--user", "tester",
		"--title", "child run",
		"--output", "text",
		"--disable-tools",
		"--debug-http",
		"--fail-fast",
		"--timeout", "10s",
	}
	if !reflect.DeepEqual(payload.Args, wantArgs) {
		t.Fatalf("unexpected child argv:\n got: %#v\nwant: %#v", payload.Args, wantArgs)
	}
	if result.Metadata["config_explicit"] != true {
		t.Fatalf("expected config_explicit metadata, got %#v", result.Metadata["config_explicit"])
	}
	if result.Metadata["cwd"] != workdir {
		t.Fatalf("expected cwd metadata %q, got %#v", workdir, result.Metadata["cwd"])
	}
	if result.Metadata["log_dir"] != filepath.Join(tmp, "logs") {
		t.Fatalf("expected log_dir metadata, got %#v", result.Metadata["log_dir"])
	}
	if result.Metadata["session_dir"] != filepath.Join(tmp, "sessions") {
		t.Fatalf("expected session_dir metadata, got %#v", result.Metadata["session_dir"])
	}
	if result.Metadata["debug_http"] != true || result.Metadata["fail_fast"] != true {
		t.Fatalf("expected debug flags metadata, got %#v", result.Metadata)
	}
}

func TestAICLIExecExecute_LiveAICLIExecutable(t *testing.T) {
	exe := strings.TrimSpace(os.Getenv("AICLI_EXEC_LIVE_EXE"))
	if exe == "" {
		t.Skip("set AICLI_EXEC_LIVE_EXE to run live aicli_exec against a real aicli executable")
	}
	workdir := strings.TrimSpace(os.Getenv("AICLI_EXEC_LIVE_CWD"))
	if workdir == "" {
		workdir = filepath.Dir(exe)
	}

	tool := NewAICLIExecTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"executable_path": exe,
		"prompt":          "只回复 OK",
		"cwd":             workdir,
		"disable_tools":   true,
		"output":          "text",
		"timeout":         "30s",
	})
	if err != nil {
		t.Fatalf("Execute transport error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected live aicli_exec to succeed, got %#v", result)
	}
	if strings.TrimSpace(result.Content) != "OK" {
		t.Fatalf("expected live aicli output OK, got %q", result.Content)
	}
	if result.Metadata["config_explicit"] != false {
		t.Fatalf("expected config_explicit=false, got %#v", result.Metadata["config_explicit"])
	}
	if result.Metadata["disable_tools"] != true {
		t.Fatalf("expected disable_tools=true, got %#v", result.Metadata["disable_tools"])
	}
}

func buildFakeAICLIExecutable(t *testing.T, dir string) string {
	t.Helper()
	source := filepath.Join(dir, "fake_aicli.go")
	code := `package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

func main() {
	stdin, _ := io.ReadAll(os.Stdin)
	cwd, _ := os.Getwd()
	payload := map[string]interface{}{
		"args": os.Args[1:],
		"stdin": string(stdin),
		"cwd": cwd,
		"nested_depth": os.Getenv("AICLI_NESTED_EXEC_DEPTH"),
	}
	data, _ := json.Marshal(payload)
	fmt.Println(string(data))
}
`
	if err := os.WriteFile(source, []byte(code), 0o644); err != nil {
		t.Fatalf("write fake aicli source: %v", err)
	}
	exe := filepath.Join(dir, "fake-aicli")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", exe, source)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fake aicli executable: %v\n%s", err, string(output))
	}
	return exe
}
