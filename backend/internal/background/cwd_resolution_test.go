package background

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

func TestResolveJobCwdAnchorsRelativeAndEmptyToSessionRoot(t *testing.T) {
	root := t.TempDir()
	ctx := toolctx.WithWorkspaceRoot(context.Background(), root)

	// An omitted cwd must inherit the session workspace, not the server
	// process directory the policy never approved.
	require.Equal(t, filepath.Clean(root), resolveJobCwd(ctx, ""))
	require.Equal(t, filepath.Clean(filepath.Join(root, "sub", "dir")), resolveJobCwd(ctx, filepath.Join("sub", "dir")))
	require.Equal(t, filepath.Clean(filepath.Join(root, "sub")), resolveJobCwd(ctx, "  sub  "))

	absolute := filepath.Join(t.TempDir(), "absolute")
	require.Equal(t, filepath.Clean(absolute), resolveJobCwd(ctx, absolute))
}

func TestResolveJobCwdKeepsLegacyBehaviorWithoutSessionRoot(t *testing.T) {
	ctx := context.Background()

	require.Equal(t, "", resolveJobCwd(ctx, "   "))
	require.Equal(t, filepath.Join("rel", "leaf"), resolveJobCwd(ctx, filepath.Join("rel", "leaf")))
}

func TestResolveJobCwdNormalizesRelativeSessionRoot(t *testing.T) {
	relativeRoot := filepath.Join("relative", "root")
	want, err := filepath.Abs(relativeRoot)
	require.NoError(t, err)

	ctx := toolctx.WithWorkspaceRoot(context.Background(), relativeRoot)
	require.Equal(t, filepath.Clean(want), resolveJobCwd(ctx, ""))
	require.Equal(t, filepath.Clean(filepath.Join(want, "leaf")), resolveJobCwd(ctx, "leaf"))
}

func TestSubmitShellAnchorsJobCwdToSessionRoot(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(Config{MaxConcurrentJobs: 1})
	defer func() { require.NoError(t, manager.Close()) }()

	ctx := toolctx.WithWorkspaceRoot(context.Background(), root)
	job, err := manager.SubmitShell(ctx, "session-cwd", BackgroundTaskArgs{Command: shellEchoCommand("cwd")})
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, filepath.Clean(root), job.Cwd)

	// Restart/retry rebuilds the request from the job, so the anchored cwd must
	// survive that round trip instead of degrading back to a bare relative path.
	require.Equal(t, filepath.Clean(root), requestFromJob(*job).Cwd)
}
