package planmode

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
)

func writePlanFile(t *testing.T, workspace, relPath, content string) {
	t.Helper()
	abs := filepath.Join(workspace, filepath.FromSlash(relPath))
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
}

func TestArchivePlanRecordsRoundsAndStatus(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	store := planstore.NewStore(t.TempDir())
	ctx := context.Background()
	planPath := "docs/feature-plan.md"

	// enter before the model wrote the file: record only, no snapshot.
	record, err := ArchivePlan(ctx, ArchiveOptions{
		Store:     store,
		SessionID: "session-1",
		Workspace: workspace,
		PlanPath:  planPath,
		Decision:  "enter",
		Source:    string(ExitSourceUser),
	})
	require.NoError(t, err)
	assert.Equal(t, planstore.StatusPending, record.Status)
	assert.Equal(t, 0, record.Version)

	// request_changes snapshots the plan body and keeps pending status.
	writePlanFile(t, workspace, planPath, "# Plan v1\n")
	record, err = ArchivePlan(ctx, ArchiveOptions{
		Store:     store,
		SessionID: "session-1",
		Workspace: workspace,
		PlanPath:  planPath,
		Decision:  "request_changes",
		Source:    string(ExitSourceUser),
		Notes:     "add rollback risks",
		Title:     "feature plan",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, record.Version)
	assert.Equal(t, planstore.StatusPending, record.Status)
	require.Len(t, record.Rounds, 1)
	assert.Equal(t, "add rollback risks", record.Rounds[0].Notes)

	// approve snapshots the revision and flips the status.
	writePlanFile(t, workspace, planPath, "# Plan v2\n")
	record, err = ArchivePlan(ctx, ArchiveOptions{
		Store:     store,
		Workspace: workspace,
		PlanPath:  planPath,
		Decision:  "approve",
		Source:    string(ExitSourceUser),
	})
	require.NoError(t, err)
	assert.Equal(t, 2, record.Version)
	assert.Equal(t, planstore.StatusApproved, record.Status)

	latest, err := store.ReadLatest(record.ID)
	require.NoError(t, err)
	assert.Equal(t, "# Plan v2\n", string(latest))

	// quit marks the artifact as not implemented.
	record, err = ArchivePlan(ctx, ArchiveOptions{
		Store:     store,
		Workspace: workspace,
		PlanPath:  planPath,
		Decision:  "quit",
		Source:    string(ExitSourceUser),
	})
	require.NoError(t, err)
	assert.Equal(t, planstore.StatusNotImplemented, record.Status)
	assert.Equal(t, 3, record.Version)
}

func TestArchivePlanRejectsWorkspaceEscapeWithoutContent(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	// A readable file just outside the workspace, reachable via "../".
	outside := filepath.Join(filepath.Dir(workspace), "secret-"+filepath.Base(workspace)+".md")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o644))
	t.Cleanup(func() { _ = os.Remove(outside) })
	store := planstore.NewStore(t.TempDir())

	record, err := ArchivePlan(context.Background(), ArchiveOptions{
		Store:     store,
		Workspace: workspace,
		PlanPath:  filepath.Join("..", filepath.Base(outside)),
		Decision:  "approve",
	})
	require.NoError(t, err)
	assert.Equal(t, 0, record.Version, "escaping relative paths must not be archived")
	assert.Empty(t, record.Rounds)
}
