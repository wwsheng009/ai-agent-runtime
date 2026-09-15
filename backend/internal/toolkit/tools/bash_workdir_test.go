package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// Shell invocations walk the same chain as the file tools: the session-bound
// workspace root from ctx, then the tool-registered base path, and only then the
// process working directory. Anything else lets the sandbox clear a relative
// "workdir" against one directory while the command runs in another.
func TestWorkdirForExecutionPrefersSessionRoot(t *testing.T) {
	tool := NewBashTool()
	tool.SetBasePath(t.TempDir())

	base := t.TempDir()
	ctx := toolctx.WithWorkspaceRoot(context.Background(), base)

	got, err := tool.workdirForExecution(ctx, "")
	if err != nil {
		t.Fatalf("empty workdir: %v", err)
	}
	if want := filepath.Clean(base); got != want {
		t.Fatalf("expected default workdir %q, got %q", want, got)
	}

	got, err = tool.workdirForExecution(ctx, filepath.Join("sub", "dir"))
	if err != nil {
		t.Fatalf("relative workdir: %v", err)
	}
	if want := filepath.Clean(filepath.Join(base, "sub", "dir")); got != want {
		t.Fatalf("expected relative workdir joined to %q, got %q", want, got)
	}

	abs := filepath.Join(base, "absolute")
	got, err = tool.workdirForExecution(ctx, abs)
	if err != nil {
		t.Fatalf("absolute workdir: %v", err)
	}
	if got != filepath.Clean(abs) {
		t.Fatalf("expected absolute workdir %q, got %q", filepath.Clean(abs), got)
	}
}

// Without a context root the shell must still run where the file tools operate:
// the base path the toolkit was registered with, never the server process
// directory (which would break relative paths the policy already cleared).
func TestWorkdirForExecutionFallsBackToRegisteredBasePath(t *testing.T) {
	registered := t.TempDir()
	tool := NewBashTool()
	tool.SetBasePath(registered)

	got, err := tool.workdirForExecution(context.Background(), "")
	if err != nil {
		t.Fatalf("registered-base empty workdir: %v", err)
	}
	if want := filepath.Clean(registered); got != want {
		t.Fatalf("expected registered base path %q, got %q", want, got)
	}

	got, err = tool.workdirForExecution(context.Background(), filepath.Join("rel", "leaf"))
	if err != nil {
		t.Fatalf("registered-base relative workdir: %v", err)
	}
	if want := filepath.Clean(filepath.Join(registered, "rel", "leaf")); got != want {
		t.Fatalf("expected relative workdir joined to %q, got %q", want, got)
	}

	// An explicit absolute workdir is still honored as-is.
	abs := filepath.Join(t.TempDir(), "absolute")
	got, err = tool.workdirForExecution(context.Background(), abs)
	if err != nil {
		t.Fatalf("registered-base absolute workdir: %v", err)
	}
	if got != filepath.Clean(abs) {
		t.Fatalf("expected absolute workdir %q, got %q", filepath.Clean(abs), got)
	}
}

// A tool with nothing bound at all keeps the legacy process-directory behavior.
func TestWorkdirForExecutionFallsBackToProcessCWD(t *testing.T) {
	tool := NewBashTool()

	got, err := tool.workdirForExecution(context.Background(), "")
	if err != nil {
		t.Fatalf("no-base empty workdir: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("process cwd: %v", err)
	}
	if filepath.Clean(got) != filepath.Clean(cwd) {
		t.Fatalf("expected process cwd fallback %q, got %q", cwd, got)
	}

	got, err = tool.workdirForExecution(context.Background(), filepath.Join("rel", "leaf"))
	if err != nil {
		t.Fatalf("no-base relative workdir: %v", err)
	}
	if want := filepath.Clean(filepath.Join(cwd, "rel", "leaf")); got != want {
		t.Fatalf("expected relative workdir joined to cwd %q, got %q", want, got)
	}
}

// The model-facing shell result must describe the directory the command really
// ran in. Reporting the raw "workdir" argument left the "Workdir:" line blank
// whenever the caller omitted it and anchored relative-path hints to the server
// process directory instead of the session workspace root the executor used.
func TestBashExecuteReportsResolvedSessionWorkdir(t *testing.T) {
	root := t.TempDir()
	ctx := toolctx.WithWorkspaceRoot(context.Background(), root)

	tests := []struct {
		name string
		args map[string]interface{}
		want string
	}{
		{
			name: "omitted workdir reports session root",
			args: map[string]interface{}{"command": "echo hi"},
			want: filepath.Clean(root),
		},
		{
			name: "relative workdir reports the resolved directory",
			args: map[string]interface{}{
				"command": "echo hi",
				"workdir": filepath.Join("sub", "dir"),
			},
			want: filepath.Clean(filepath.Join(root, "sub", "dir")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewBashTool()
			inspector := &inspectExecuter{result: CommandExecutionResult{Output: "ok"}}
			tool.executer = inspector

			result, err := tool.Execute(ctx, tt.args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.Success {
				t.Fatalf("expected success, got error: %v", result.Error)
			}
			if got := inspector.lastConfig.workdir; got != tt.want {
				t.Fatalf("executor workdir = %q, want %q", got, tt.want)
			}
			if !strings.Contains(result.Content, "Workdir: "+tt.want) {
				t.Fatalf("expected model-facing header to report %q, got:\n%s", tt.want, result.Content)
			}
		})
	}
}
