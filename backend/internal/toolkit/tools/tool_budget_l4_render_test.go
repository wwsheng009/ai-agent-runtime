package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// l4FoldMarker is the exact text the render layer (L4) appends after the shown
// head when it folds an oversized tool payload.
const l4FoldMarker = "[output truncated for history safety: showing the first "

// renderBudgetForBudgetTests holds the render-layer budget far below the
// smallest per-tool budget (glob/ls = 16 KiB) so every budget-owning payload in
// this file would be folded by L4 if the per-tool opt-out were lost. Without
// that gap the assertions below could pass vacuously.
const renderBudgetForBudgetTests = 4 * 1024

// budgetCase pairs one budget-owning tool with its own output budget and a
// fixture that produces more bytes than that budget.
type budgetCase struct {
	name   string
	budget int
	run    func(t *testing.T) *toolkit.ToolResult
}

// TestBudgetOwningToolsDoNotTriggerRenderLayerTruncation is the end-to-end
// proof for the per-tool budget contract: every budget-owning tool emits a
// payload that is larger than the render-layer budget (so L4 *would* fold it),
// the tool's own window still caps the payload, and the render layer must
// neither fold the payload nor count an l4_render truncation.
func TestBudgetOwningToolsDoNotTriggerRenderLayerTruncation(t *testing.T) {
	previousBudget := output.ModelToolTextByteBudget()
	output.SetModelToolTextByteBudget(renderBudgetForBudgetTests)
	t.Cleanup(func() { output.SetModelToolTextByteBudget(previousBudget) })

	workspace := t.TempDir()
	largeFile := writeBudgetFixtureFile(t, workspace)
	wideDir := writeBudgetFixtureDir(t, workspace)
	largeBody := strings.Repeat("0123456789abcdef", 16*1024) // 256 KiB
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(largeBody))
	}))
	defer server.Close()

	cases := []budgetCase{
		{
			name:   "view",
			budget: viewOutputBudgetBytes,
			run: func(t *testing.T) *toolkit.ToolResult {
				return executeBudgetTool(t, NewViewTool(), map[string]interface{}{
					"file_path": largeFile,
				})
			},
		},
		{
			name:   "grep",
			budget: grepOutputBudgetBytes,
			run: func(t *testing.T) *toolkit.ToolResult {
				return executeBudgetTool(t, NewGrepTool(), map[string]interface{}{
					"pattern": "budget fixture payload",
					"paths":   []string{largeFile},
				})
			},
		},
		{
			name:   "glob",
			budget: globOutputBudgetBytes,
			run: func(t *testing.T) *toolkit.ToolResult {
				return executeBudgetTool(t, NewGlobTool(), map[string]interface{}{
					"pattern": "*.go",
					"path":    wideDir,
					// Raise the default 100-result cap so the byte window, not
					// the result cap, is what bounds this payload.
					"limit": 1000,
				})
			},
		},
		{
			name:   "ls",
			budget: lsOutputBudgetBytes,
			run: func(t *testing.T) *toolkit.ToolResult {
				return executeBudgetTool(t, NewLsTool(), map[string]interface{}{
					"path": wideDir,
				})
			},
		},
		{
			name:   "fetch",
			budget: fetchOutputBudgetBytes,
			run: func(t *testing.T) *toolkit.ToolResult {
				return executeBudgetTool(t, NewFetchTool(), map[string]interface{}{
					"url":    server.URL,
					"format": "text",
				})
			},
		},
		{
			name:   "artifact_read",
			budget: artifactOutputBudgetBytes,
			run: func(t *testing.T) *toolkit.ToolResult {
				store := newArtifactReadTestStore(t)
				content := strings.Repeat("artifact payload line for budget proof\n", 8*1024)
				id := putArtifactForRead(t, store, "sess-budget", "shell", content)
				return executeArtifactRead(t, artifactReadContext(store, "sess-budget"), map[string]interface{}{
					"artifact_id": id,
				})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := renderLayerTruncations()

			result := tc.run(t)
			require.NotNil(t, result)
			require.True(t, result.Success, "tool %s must succeed: %v", tc.name, result.Error)

			// The tool declares that it owns the final shape of its payload.
			require.True(t, toolresult.SkipsRenderTruncation(result.Metadata),
				"tool %s must stamp %s", tc.name, toolresult.MetadataSkipRenderTruncationKey)

			// The tool's own window caps the payload...
			require.LessOrEqual(t, len(result.Content), tc.budget,
				"tool %s payload must stay within its own %d-byte budget", tc.name, tc.budget)
			// ...and it is still big enough that L4 would fold it if the
			// opt-out were missing, so the assertion below cannot pass vacuously.
			require.Greater(t, len(result.Content), renderBudgetForBudgetTests,
				"fixture for %s must exceed the render budget", tc.name)

			rendered := output.RenderToolResultContentForModel(result.Content, "",
				&output.Envelope{ToolName: tc.name, Metadata: result.Metadata})

			require.NotContains(t, rendered, l4FoldMarker,
				"render layer must not fold a payload owned by tool %s", tc.name)
			require.Greater(t, len(rendered), renderBudgetForBudgetTests,
				"tool %s payload must reach the model unfolded", tc.name)
			require.Equal(t, before, renderLayerTruncations(),
				"tool %s must not record an l4_render truncation", tc.name)

			t.Logf("%s: payload=%d bytes (own budget %d), rendered=%d bytes, render budget %d, l4_render delta 0",
				tc.name, len(result.Content), tc.budget, len(rendered), renderBudgetForBudgetTests)
		})
	}
}

// TestRenderLayerStillFoldsUnstampedOversizedPayload is the negative control:
// without the per-tool opt-out the very same oversized payload is folded and
// counted, which proves the assertions above detect L4 folding at all.
func TestRenderLayerStillFoldsUnstampedOversizedPayload(t *testing.T) {
	previousBudget := output.ModelToolTextByteBudget()
	output.SetModelToolTextByteBudget(renderBudgetForBudgetTests)
	t.Cleanup(func() { output.SetModelToolTextByteBudget(previousBudget) })

	payload := strings.Repeat("unstamped payload line\n", 2*1024) // 42 KiB
	before := renderLayerTruncations()

	rendered := output.RenderToolResultContentForModel(payload, "",
		&output.Envelope{ToolName: "grep"})

	require.Contains(t, rendered, l4FoldMarker,
		"unstamped oversized payload must still be folded by the render layer")
	require.LessOrEqual(t, len(rendered), renderBudgetForBudgetTests,
		"folded payload must fit the render budget")
	require.Equal(t, before+1, renderLayerTruncations(),
		"the fold must be counted exactly once at l4_render")
}

// renderLayerTruncations reads the l4_render counter from the tool-efficiency
// snapshot. Tests in this package do not call t.Parallel(), so reading the
// process-wide registry is safe.
func renderLayerTruncations() float64 {
	return observability.SnapshotToolEfficiency().ArtifactFlow.Truncations.ByLayer[observability.TruncationLayerRender]
}

// executeBudgetTool runs one tool with the given parameters and fails the test
// on an execution error (an unsuccessful *result* is asserted by the caller).
func executeBudgetTool(t *testing.T, tool toolkit.Tool, params map[string]interface{}) *toolkit.ToolResult {
	t.Helper()
	result, err := tool.Execute(context.Background(), params)
	require.NoError(t, err)
	require.NotNil(t, result)
	return result
}

// writeBudgetFixtureFile writes a text file large enough to overflow the view
// and grep windows (32 KiB each) several times over. Lines are padded because
// grep caps how many matches it reports: a few hundred long matching lines
// overflow the byte window where thousands of short ones would not.
func writeBudgetFixtureFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "budget_fixture.txt")
	filler := strings.Repeat("x", 1500)
	var builder strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&builder, "line %04d: budget fixture payload %s\n", i, filler)
	}
	require.NoError(t, os.WriteFile(path, []byte(builder.String()), 0o644))
	return path
}

// writeBudgetFixtureDir writes enough long-named entries that a directory
// listing (ls) and a path glob (glob) both overflow their 16 KiB windows.
func writeBudgetFixtureDir(t *testing.T, dir string) string {
	t.Helper()
	wide := filepath.Join(dir, "wide")
	require.NoError(t, os.MkdirAll(wide, 0o755))
	for i := 0; i < 600; i++ {
		name := fmt.Sprintf("budget_fixture_entry_%04d_with_a_deliberately_long_file_name.go", i)
		require.NoError(t, os.WriteFile(filepath.Join(wide, name), []byte("package fixture\n"), 0o644))
	}
	return wide
}
