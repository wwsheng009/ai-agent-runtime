package tools

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

const artifactReadNextOffsetPattern = `next_offset=([0-9]+)`

func newArtifactReadTestStore(t *testing.T) *artifact.Store {
	t.Helper()
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func putArtifactForRead(t *testing.T, store *artifact.Store, sessionID, toolName, content string) string {
	t.Helper()
	id, err := store.Put(context.Background(), artifact.Record{
		SessionID:  sessionID,
		ToolName:   toolName,
		ToolCallID: "call-artifact-read",
		Content:    content,
	})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(id, "art_"), "artifact id must use the art_ prefix: %q", id)
	return id
}

func artifactReadContext(store *artifact.Store, sessionID string) context.Context {
	return toolctx.WithArtifactStore(toolctx.WithSessionID(context.Background(), sessionID), store)
}

func executeArtifactRead(t *testing.T, ctx context.Context, params map[string]interface{}) *toolkit.ToolResult {
	t.Helper()
	result, err := NewArtifactReadTool().Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)
	return result
}

// splitArtifactReadWindow peels the window header off the tool output.
func splitArtifactReadWindow(t *testing.T, content string) string {
	t.Helper()
	idx := strings.Index(content, "\n\n")
	require.GreaterOrEqual(t, idx, 0, "artifact_read output must keep a blank line between header and payload")
	return content[idx+2:]
}

func TestArtifactReadReturnsFullContentForSmallRecord(t *testing.T) {
	store := newArtifactReadTestStore(t)
	content := "Exit code: 0\nOutput:\nE2E_SNAPSHOT_OK\n"
	id := putArtifactForRead(t, store, "sess-1", "shell", content)

	result := executeArtifactRead(t, artifactReadContext(store, "sess-1"), map[string]interface{}{
		"artifact_id": id,
	})

	require.True(t, result.Success, result.Error)
	require.Equal(t, toolresult.KindText, result.OutputKind)
	require.Equal(t, content, splitArtifactReadWindow(t, result.Content))
	require.Contains(t, result.Content, "artifact "+id)
	require.Contains(t, result.Content, "tool=shell")
	require.Contains(t, result.Content, "total_bytes="+strconv.Itoa(len(content)))
	require.Contains(t, result.Content, "eof=true")
	require.NotContains(t, result.Content, "next_offset=")
}

// TestArtifactReadOwnsItsWindow pins the L4 contract for the artifact pager:
// like view, artifact_read sizes every page against the model-visible byte
// budget and publishes its own continuation metadata (artifact_eof /
// artifact_next_offset), so it must declare the render-layer opt-out
// explicitly instead of relying on the budget arithmetic alone.
func TestArtifactReadOwnsItsWindow(t *testing.T) {
	store := newArtifactReadTestStore(t)
	content := strings.Repeat("x", artifactOutputBudgetBytes*2)
	id := putArtifactForRead(t, store, "sess-1", "shell", content)

	result := executeArtifactRead(t, artifactReadContext(store, "sess-1"), map[string]interface{}{
		"artifact_id": id,
	})

	require.True(t, result.Success, result.Error)
	require.Equal(t, false, result.Metadata["artifact_eof"], "precondition: expect a paged window, got %#v", result.Metadata)
	require.True(t, toolresult.SkipsRenderTruncation(result.Metadata),
		"artifact_read must declare the render-layer opt-out: %#v", result.Metadata)
	require.Equal(t, true, result.Metadata[toolresult.MetadataSkipRenderTruncationKey])
}

func TestArtifactReadPagesThroughByteWindows(t *testing.T) {
	store := newArtifactReadTestStore(t)
	content := strings.Repeat("0123456789abcdef", 700) + "\n结尾"
	id := putArtifactForRead(t, store, "sess-1", "grep", content)
	ctx := artifactReadContext(store, "sess-1")

	rebuilt := readAllArtifactPages(t, ctx, id, 1024)
	require.Equal(t, content, rebuilt)
}

func TestArtifactReadKeepsUTF8BoundariesAcrossTinyWindows(t *testing.T) {
	store := newArtifactReadTestStore(t)
	content := strings.Repeat("中文内容测试", 200)
	id := putArtifactForRead(t, store, "sess-1", "view", content)
	ctx := artifactReadContext(store, "sess-1")

	// limit=1 forces every window to snap back to a rune boundary; the reader
	// must still advance one full rune per page and never emit invalid UTF-8.
	offset := 0
	var rebuilt strings.Builder
	for page := 0; page < 10000; page++ {
		params := map[string]interface{}{"artifact_id": id, "limit": 1}
		if offset > 0 {
			params["offset"] = offset
		}
		result := executeArtifactRead(t, ctx, params)
		require.True(t, result.Success, result.Error)

		window := splitArtifactReadWindow(t, result.Content)
		require.True(t, utf8.ValidString(window), "window must not split a rune: %q", window)
		rebuilt.WriteString(window)

		if strings.Contains(result.Content, "eof=true") {
			break
		}
		next, ok := artifactReadNextOffset(result.Content)
		require.True(t, ok, "non-terminal window must carry next_offset: %q", result.Content)
		require.Greater(t, next, offset, "page chain must make progress")
		offset = next
	}

	require.Equal(t, content, rebuilt.String())
}

func TestArtifactReadRejectsUnknownRecord(t *testing.T) {
	store := newArtifactReadTestStore(t)
	missing := "art_00000000000000000000000000000000"

	result := executeArtifactRead(t, artifactReadContext(store, "sess-1"), map[string]interface{}{
		"artifact_id": missing,
	})

	require.False(t, result.Success)
	require.Error(t, result.Error)
	require.Contains(t, result.Error.Error(), "artifact not found: "+missing)
}

func TestArtifactReadRequiresRuntimeContext(t *testing.T) {
	result := executeArtifactRead(t, context.Background(), map[string]interface{}{
		"artifact_id": "art_00000000000000000000000000000000",
	})

	require.False(t, result.Success)
	require.Error(t, result.Error)
	require.Contains(t, result.Error.Error(), "artifact store 不可用")
}

func TestArtifactReadRejectsCrossSessionRead(t *testing.T) {
	store := newArtifactReadTestStore(t)
	id := putArtifactForRead(t, store, "sess-owner", "shell", "secret output")

	result := executeArtifactRead(t, artifactReadContext(store, "sess-other"), map[string]interface{}{
		"artifact_id": id,
	})

	require.False(t, result.Success)
	require.Error(t, result.Error)
	require.Contains(t, result.Error.Error(), "属于会话 sess-owner")
}

func TestArtifactReadAcceptsPastedPointerTail(t *testing.T) {
	store := newArtifactReadTestStore(t)
	content := "raw middle bytes"
	id := putArtifactForRead(t, store, "sess-1", "shell", content)
	ctx := artifactReadContext(store, "sess-1")

	for _, raw := range []string{
		id + " size=16 kind=text; read the full raw output via artifact_read(artifact_id=<id>); never pass this id to task_output",
		`"` + id + `";`,
		strings.ToUpper(id),
	} {
		result := executeArtifactRead(t, ctx, map[string]interface{}{"artifact_id": raw})
		require.True(t, result.Success, "artifact_id=%q failed: %v", raw, result.Error)
		require.Equal(t, content, splitArtifactReadWindow(t, result.Content))
	}
}

func TestArtifactReadWindowStaysWithinToolBudget(t *testing.T) {
	budget := artifactOutputBudgetBytes
	if budget <= artifactReadMinLimitBytes+artifactReadHeaderReserveBytes {
		t.Skipf("configured budget %d is too small for the no-cascade guarantee", budget)
	}

	store := newArtifactReadTestStore(t)
	content := strings.Repeat("x", budget*4)
	id := putArtifactForRead(t, store, "sess-1", "shell", content)

	result := executeArtifactRead(t, artifactReadContext(store, "sess-1"), map[string]interface{}{
		"artifact_id": id,
		"limit":       1 << 20,
	})

	require.True(t, result.Success, result.Error)
	require.LessOrEqual(t, len(result.Content), budget, "a max-size window must stay inside the tool's own budget")
	require.Contains(t, result.Content, "eof=false")
	require.True(t, toolresult.SkipsRenderTruncation(result.Metadata))
}

// TestArtifactReadDefaultWindowEqualsMaxLimit pins P1-2: the default page size
// must track the max-window cap so budget-sized artifacts dereference in a
// single read (no follow-up page hop).
func TestArtifactReadDefaultWindowEqualsMaxLimit(t *testing.T) {
	if got, want := artifactReadDefaultLimitBytes(), artifactReadMaxLimitBytes(); got != want {
		t.Fatalf("default window %d must equal max window %d", got, want)
	}

	store := newArtifactReadTestStore(t)
	content := strings.Repeat("y", artifactReadMaxLimitBytes()*2)
	id := putArtifactForRead(t, store, "sess-1", "shell", content)

	result := executeArtifactRead(t, artifactReadContext(store, "sess-1"), map[string]interface{}{"artifact_id": id})
	require.True(t, result.Success, result.Error)
	window := splitArtifactReadWindow(t, result.Content)
	require.Equal(t, artifactReadMaxLimitBytes(), len(window), "default read must cover a full max window")
	require.Contains(t, result.Content, "eof=false")
}

func artifactReadNextOffset(content string) (int, bool) {
	match := regexp.MustCompile(artifactReadNextOffsetPattern).FindStringSubmatch(content)
	if len(match) != 2 {
		return 0, false
	}
	value, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	return value, true
}

func readAllArtifactPages(t *testing.T, ctx context.Context, id string, limit int) string {
	t.Helper()
	var rebuilt strings.Builder
	offset := 0
	for page := 0; page < 10000; page++ {
		params := map[string]interface{}{"artifact_id": id, "limit": limit}
		if offset > 0 {
			params["offset"] = offset
		}
		result := executeArtifactRead(t, ctx, params)
		require.True(t, result.Success, result.Error)
		rebuilt.WriteString(splitArtifactReadWindow(t, result.Content))
		if strings.Contains(result.Content, "eof=true") {
			return rebuilt.String()
		}
		next, ok := artifactReadNextOffset(result.Content)
		require.True(t, ok, "non-terminal window must carry next_offset: %q", result.Content)
		require.Greater(t, next, offset, "page chain must make progress")
		offset = next
	}
	t.Fatal("artifact_read did not reach eof within the page budget")
	return ""
}
