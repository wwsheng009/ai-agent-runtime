package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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
		{
			// Real execution: the platform shell prints a ~39 KiB fixture file,
			// which is more than the shell's own 32 KiB window. The payload
			// lives on disk because passing it on the command line would exceed
			// the Windows command line limit.
			name:   "bash",
			budget: shellOutputBudgetBytes,
			run: func(t *testing.T) *toolkit.ToolResult {
				return executeBudgetTool(t, NewBashTool(), map[string]interface{}{
					"command": shellPrintFileCommand(t, workspace),
				})
			},
		},
		{
			// aicli_exec shares ownShellOutputWindow with bash but cannot run a
			// real child process in a unit test, so the window contract is
			// exercised through the same entry point the tool's Execute uses.
			name:   "aicli_exec",
			budget: shellOutputBudgetBytes,
			run: func(t *testing.T) *toolkit.ToolResult {
				return ownShellOutputWindow(context.Background(), "aicli_exec", &toolkit.ToolResult{
					Success:    true,
					OutputKind: toolresult.KindText,
					Content:    strings.Repeat("aicli child output line for budget proof\n", 4*1024),
					Metadata:   map[string]interface{}{"command": "aicli exec"},
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

// TestShellToolsOwnTheirOutputWindow pins the shell window contract end to end:
// the tool - not the render layer - folds the captured stream, the fold notice
// states the arithmetic, the complete capture is archived *before* the fold so
// artifact_read can page the omitted tail, and the result is marked as a window
// of that record (artifact_source_id) so nothing archives it twice.
func TestShellToolsOwnTheirOutputWindow(t *testing.T) {
	store := newArtifactReadTestStore(t)
	sessionID := "sess-shell-window"
	ctx := artifactReadContext(store, sessionID)
	full := strings.Repeat("shell capture line for the window proof\n", 4096) // ~164 KiB

	for _, toolName := range []string{"bash", "aicli_exec"} {
		t.Run(toolName, func(t *testing.T) {
			before := renderLayerTruncations()

			result := ownShellOutputWindow(ctx, toolName, &toolkit.ToolResult{
				Success:    true,
				OutputKind: toolresult.KindText,
				Content:    full,
				Metadata:   map[string]interface{}{"command": "echo fixture"},
			})

			require.NotNil(t, result)
			require.True(t, result.Success)
			require.True(t, toolresult.SkipsRenderTruncation(result.Metadata),
				"shell output must opt out of render-layer folding: %#v", result.Metadata)
			require.Equal(t, shellOutputBudgetBytes, result.Metadata[toolresult.MetadataModelVisibleBudgetKey])

			// The tool folded head-only at its own window...
			require.LessOrEqual(t, len(result.Content), shellOutputBudgetBytes)
			require.Greater(t, len(result.Content), renderBudgetForBudgetTests)
			require.True(t, strings.HasPrefix(result.Content, full[:1024]),
				"the folded body must start with the head of the capture")
			require.Contains(t, result.Content, "[output window: showing the first ")
			require.Contains(t, result.Content, "of "+strconv.Itoa(len(full))+" bytes of the captured output")
			require.Equal(t, true, result.Metadata["truncated"])
			require.Equal(t, len(full), result.Metadata["output_window_total_bytes"])

			// ...and published the record that holds the rest.
			id, _ := result.Metadata["artifact_id"].(string)
			require.True(t, strings.HasPrefix(id, "art_"), "folded shell output must be archived: %#v", result.Metadata)
			require.Equal(t, id, result.Metadata["artifact_source_id"],
				"the folded body is a window of its own record, so the gateway must not re-archive it")

			read := executeArtifactRead(t, ctx, map[string]interface{}{
				"artifact_id": id,
				"offset":      shellOutputBudgetBytes,
				"limit":       2048,
			})
			require.True(t, read.Success, read.Error)
			require.Equal(t, full[shellOutputBudgetBytes:shellOutputBudgetBytes+2048],
				splitArtifactReadWindow(t, read.Content),
				"artifact_read must page the bytes the fold omitted")

			// The render layer sees a tool-owned payload: no second fold, no
			// l4_render truncation, and the pointer to the archived capture.
			rendered := output.RenderToolResultContentForModel(result.Content, "",
				&output.Envelope{ToolName: toolName, Metadata: result.Metadata})
			require.NotContains(t, rendered, l4FoldMarker)
			require.Contains(t, rendered, id)
			require.Contains(t, rendered, "size="+strconv.Itoa(len(full)),
				"the pointer must describe the archived capture, not the folded body")
			require.Equal(t, before, renderLayerTruncations())
		})
	}
}

// TestShellToolsStampOwnershipOnEveryExecutePath proves the wiring: the
// ownership stamp comes from the tool's own Execute, so no shell return path
// (single command, batch, or a failed aicli_exec call) can fall back to
// render-layer folding.
func TestShellToolsStampOwnershipOnEveryExecutePath(t *testing.T) {
	t.Run("bash single command", func(t *testing.T) {
		result := executeBudgetTool(t, NewBashTool(), map[string]interface{}{
			"command": "echo shell-window-wiring",
		})
		require.True(t, toolresult.SkipsRenderTruncation(result.Metadata),
			"bash single-command path must stamp the opt-out: %#v", result.Metadata)
	})

	t.Run("bash batch", func(t *testing.T) {
		result := executeBudgetTool(t, NewBashTool(), map[string]interface{}{
			"commands": []interface{}{
				map[string]interface{}{"command": "echo one"},
				map[string]interface{}{"command": "echo two"},
			},
		})
		require.True(t, toolresult.SkipsRenderTruncation(result.Metadata),
			"bash batch path must stamp the opt-out: %#v", result.Metadata)
	})

	t.Run("aicli_exec failure path", func(t *testing.T) {
		result := executeBudgetTool(t, NewAICLIExecTool(), map[string]interface{}{
			"prompt":          "noop",
			"executable_path": filepath.Join(t.TempDir(), "missing-aicli"),
		})
		require.False(t, result.Success, "precondition: the missing executable must fail the call")
		require.True(t, toolresult.SkipsRenderTruncation(result.Metadata),
			"aicli_exec failure path must stamp the opt-out: %#v", result.Metadata)
	})
}

// shellPrintFileCommand writes a payload larger than the shell window into a
// fixture file and returns the short platform-appropriate command that prints
// it. The payload lives on disk because handing ~39 KiB to PowerShell on the
// command line exceeds the Windows command line limit.
func shellPrintFileCommand(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "shell-window-fixture.txt")
	payload := strings.Repeat("shell-budget-fixture-line\n", 1500) // ~39 KiB
	require.NoError(t, os.WriteFile(path, []byte(payload), 0o644))
	if runtime.GOOS == "windows" {
		return "Get-Content " + path
	}
	return "cat " + path
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
