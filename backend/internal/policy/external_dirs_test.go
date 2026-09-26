package policy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// externalDirTestEngine builds an engine whose temp exemption does not cover
// t.TempDir() paths, so "outside" targets created by the test stay gated.
func externalDirTestEngine(t *testing.T, mode Mode) (*Engine, *recordingApprovalHandler, *[]string) {
	t.Helper()
	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: true}}
	admitted := make([]string, 0, 1)
	engine := &Engine{
		Mode:                 mode,
		AskHandler:           handler,
		Policy:               NewToolExecutionPolicy(nil, false),
		ExternalDirTempRoots: []string{filepath.Join(t.TempDir(), "not-a-temp-root")},
		ApproveExternalDir: func(dirs []string) {
			admitted = append(admitted, dirs...)
		},
	}
	return engine, handler, &admitted
}

func TestExternalDirGateAsksAndAdmitsOutsidePath(t *testing.T) {
	workspace := t.TempDir()
	outsideDir := t.TempDir()
	target := filepath.Join(outsideDir, "notes.txt")

	engine, handler, admitted := externalDirTestEngine(t, ModeDefault)
	ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)
	decision, err := engine.Evaluate(ctx, EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": target},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageAsk, decision.Stage)
	assert.Equal(t, 1, handler.calls, "an outside write must be admitted by the user")
	assert.Equal(t, []string{canonicalExternalPath(outsideDir)}, *admitted)
	assert.Equal(t, []string{canonicalExternalPath(outsideDir)}, decision.ExternalDirs)
}

func TestExternalDirGateAbstainsInsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	engine, handler, admitted := externalDirTestEngine(t, ModeDefault)
	ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)

	read, err := engine.Evaluate(ctx, EvalRequest{
		ToolName: "view",
		Args:     map[string]interface{}{"file_path": "README.md"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, read.Type)
	assert.Equal(t, StageReadonlyAuto, read.Stage, "workspace reads keep the read-only fast lane")
	assert.Empty(t, read.ExternalDirs)

	write, err := engine.Evaluate(ctx, EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": filepath.Join(workspace, "a.txt")},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, write.Type)
	assert.Equal(t, 1, handler.calls, "inside writes still follow the mode ask")
	assert.Empty(t, write.ExternalDirs)
	assert.Empty(t, *admitted, "no external directory is admitted for inside paths")
}

func TestExternalDirGateSilentlyExemptsTempDir(t *testing.T) {
	workspace := t.TempDir()
	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: true}}
	engine := &Engine{
		Mode:       ModeDefault,
		AskHandler: handler,
		Policy:     NewToolExecutionPolicy(nil, false),
	}
	ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)
	read, err := engine.Evaluate(ctx, EvalRequest{
		ToolName: "view",
		Args:     map[string]interface{}{"file_path": filepath.Join(t.TempDir(), "cache.txt")},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, read.Type)
	assert.Equal(t, StageReadonlyAuto, read.Stage, "temp reads are silent in every mode")
	assert.Equal(t, 0, handler.calls)
}

func TestExternalDirGateDontAskDeniesOutsidePath(t *testing.T) {
	workspace := t.TempDir()
	engine, handler, admitted := externalDirTestEngine(t, ModeDontAsk)
	ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)
	decision, err := engine.Evaluate(ctx, EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": filepath.Join(t.TempDir(), "outside.txt")},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Equal(t, StageExternalDir, decision.Stage)
	assert.Contains(t, decision.Reason, "external_dir:admit")
	assert.Equal(t, 0, handler.calls)
	assert.Empty(t, *admitted)
}

func TestExternalDirGateBypassAdmitsSilently(t *testing.T) {
	workspace := t.TempDir()
	outsideDir := t.TempDir()
	engine, handler, admitted := externalDirTestEngine(t, ModeBypassPermissions)
	ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)
	decision, err := engine.Evaluate(ctx, EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": filepath.Join(outsideDir, "yolo.txt")},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageExternalDir, decision.Stage)
	assert.Contains(t, decision.Reason, "admit_bypass")
	assert.Equal(t, 0, handler.calls, "bypass grants the directory without prompting")
	assert.Equal(t, []string{canonicalExternalPath(outsideDir)}, *admitted)
}

func TestExternalDirGateAdmittedRootsAbstain(t *testing.T) {
	workspace := t.TempDir()
	outsideDir := t.TempDir()
	target := filepath.Join(outsideDir, "already-admitted.txt")

	engine, handler, admitted := externalDirTestEngine(t, ModeDefault)
	ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)
	ctx = toolctx.WithAllowedRoots(ctx, []string{outsideDir})
	decision, err := engine.Evaluate(ctx, EvalRequest{
		ToolName: "view",
		Args:     map[string]interface{}{"file_path": target},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageReadonlyAuto, decision.Stage, "an admitted root is no longer external")
	assert.Equal(t, 0, handler.calls)
	assert.Empty(t, *admitted)
}

func TestExternalDirGateReadOnlyRootsExemptReadsOnly(t *testing.T) {
	workspace := t.TempDir()
	skillsDir := t.TempDir()
	target := filepath.Join(skillsDir, "skill.md")

	engine, handler, admitted := externalDirTestEngine(t, ModeDefault)
	engine.ExternalReadOnlyRoots = []string{skillsDir}
	ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)
	read, err := engine.Evaluate(ctx, EvalRequest{
		ToolName: "view",
		Args:     map[string]interface{}{"file_path": target},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, read.Type)
	assert.Equal(t, StageReadonlyAuto, read.Stage, "registered skill dirs are read-only exempt")
	assert.Equal(t, 0, handler.calls)

	write, err := engine.Evaluate(ctx, EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": target, "content": "x"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, write.Type)
	assert.Equal(t, StageAsk, write.Stage, "writes into an exempt root still need admission")
	assert.Equal(t, 1, handler.calls)
	assert.Equal(t, []string{canonicalExternalPath(skillsDir)}, *admitted)
}

func TestExternalDirGateCoversShellCwd(t *testing.T) {
	workspace := t.TempDir()
	outsideDir := t.TempDir()
	engine, _, _ := externalDirTestEngine(t, ModeDontAsk)
	ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)
	decision, err := engine.Evaluate(ctx, EvalRequest{
		ToolName: "shell",
		Args:     map[string]interface{}{"command": "ls", "cwd": outsideDir},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Equal(t, StageExternalDir, decision.Stage, "an outside shell cwd is gated")
}

func TestExternalDirGateIgnoresPathsInsideShellCommands(t *testing.T) {
	workspace := t.TempDir()
	engine, handler, admitted := externalDirTestEngine(t, ModeDefault)
	ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)
	decision, err := engine.Evaluate(ctx, EvalRequest{
		ToolName: "shell",
		Args:     map[string]interface{}{"command": "cat /etc/hosts"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.NotEqual(t, StageExternalDir, decision.Stage)
	assert.Equal(t, 0, handler.calls, "absolute paths inside a command string are not mined (§7)")
	assert.NotContains(t, decision.Reason, "external_dir")
	assert.Empty(t, *admitted)
}

func TestExternalDirGateDisabled(t *testing.T) {
	workspace := t.TempDir()
	engine, handler, admitted := externalDirTestEngine(t, ModeDefault)
	engine.DisableExternalDirGate = true
	ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)
	decision, err := engine.Evaluate(ctx, EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": filepath.Join(t.TempDir(), "outside.txt")},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageAsk, decision.Stage, "mode ask still applies; only the directory gate is off")
	assert.Equal(t, 1, handler.calls)
	assert.Empty(t, *admitted)
}

func TestExternalDirGateSkippedWithoutWorkspaceRoot(t *testing.T) {
	engine, handler, admitted := externalDirTestEngine(t, ModeDefault)
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": filepath.Join(t.TempDir(), "outside.txt")},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Empty(t, *admitted, "no workspace boundary means no external-directory gate")
	assert.Equal(t, 1, handler.calls)
}

func TestPathInsideRoot(t *testing.T) {
	root := t.TempDir()
	assert.True(t, pathInsideRoot(root, root))
	assert.True(t, pathInsideRoot(root, filepath.Join(root, "a", "b.txt")))
	assert.False(t, pathInsideRoot(root, filepath.Dir(root)))
	assert.False(t, pathInsideRoot(root, filepath.Join(filepath.Dir(root), "sibling")))
	assert.False(t, pathInsideRoot("", root))
	assert.False(t, pathInsideRoot(root, ""))
	if os.PathSeparator == '\\' {
		assert.True(t, pathInsideRoot(strings.ToUpper(root), filepath.Join(root, "child")),
			"Windows containment must be case-insensitive")
	}
}
