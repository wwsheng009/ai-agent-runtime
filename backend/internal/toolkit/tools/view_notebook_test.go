package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

func writeNotebook(t *testing.T, path string, cells []map[string]interface{}) {
	t.Helper()
	raw, err := json.Marshal(map[string]interface{}{"nbformat": 4, "cells": cells})
	if err != nil {
		t.Fatalf("marshal notebook: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write notebook: %v", err)
	}
}

// TestViewNotebookRendersCellsAndImages pins §3.10: tagged cells, decoded image
// outputs surfaced through the passthrough contract, and the source file left
// untouched.
func TestViewNotebookRendersCellsAndImages(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	pngPayload := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00}
	writeNotebook(t, filepath.Join(root, "analysis.ipynb"), []map[string]interface{}{
		{"cell_type": "markdown", "source": []string{"# Results\n"}},
		{"cell_type": "code", "source": "plot()", "outputs": []map[string]interface{}{
			{"output_type": "display_data", "data": map[string]interface{}{
				"image/png": base64.StdEncoding.EncodeToString(pngPayload),
			}},
		}},
	})

	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "analysis.ipynb"})
	if !strings.Contains(result.Content, "# %% [markdown] cell 1") ||
		!strings.Contains(result.Content, "# %% [code] cell 2") {
		t.Fatalf("expected tagged cells, got %q", result.Content)
	}
	if result.Metadata["doc_kind"] != "ipynb" || result.Metadata["notebook_cells"] != 2 {
		t.Fatalf("expected notebook metadata, got %#v", result.Metadata)
	}
	if result.Metadata[toolresult.MetadataImagePassthroughKey] != true {
		t.Fatalf("expected image passthrough, got %#v", result.Metadata)
	}
	paths, ok := result.Metadata[toolresult.MetadataImagePathsKey].([]string)
	if !ok || len(paths) != 1 {
		t.Fatalf("expected one persisted image path, got %#v", result.Metadata[toolresult.MetadataImagePathsKey])
	}
	persisted, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatalf("persisted image unreadable: %v", err)
	}
	if string(persisted) != string(pngPayload) {
		t.Fatalf("persisted bytes differ from the notebook output")
	}
}

// TestViewNotebookPagesThroughTheSharedWindow: a large notebook obeys
// offset/limit and publishes continuation metadata like any text file.
func TestViewNotebookPagesThroughTheSharedWindow(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	cells := make([]map[string]interface{}, 0, 30)
	for i := 0; i < 30; i++ {
		cells = append(cells, map[string]interface{}{
			"cell_type": "code",
			"source":    fmt.Sprintf("print(%d)\n", i),
		})
	}
	writeNotebook(t, filepath.Join(root, "big.ipynb"), cells)
	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{
		"file_path": "big.ipynb",
		"limit":     float64(10),
	})
	if result.Metadata["is_truncated"] != true {
		t.Fatalf("expected a truncated first window, got %#v", result.Metadata)
	}
	if linesRead, _ := result.Metadata["lines_read"].(int); linesRead > 10 || linesRead == 0 {
		t.Fatalf("expected at most 10 rendered lines, got %#v", result.Metadata["lines_read"])
	}
	if result.Metadata["suggested_next_offset"] != 10 {
		t.Fatalf("expected continuation metadata, got %#v", result.Metadata)
	}
}

// TestViewNotebookInvalidJSONIsRecoverable: a broken .ipynb fails with a
// recovery route instead of a silent text dump.
func TestViewNotebookInvalidJSONIsRecoverable(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "broken.ipynb"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write broken notebook: %v", err)
	}
	tool := newViewToolAt(t, root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "broken.ipynb"})
	if err != nil {
		t.Fatalf("unexpected tool error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected a structured failure, got %+v", result)
	}
	if !strings.Contains(result.Error.Error(), "shell/jq") {
		t.Fatalf("expected a recovery route, got %v", result.Error)
	}
}

// TestViewNotebookTooLargeDegradesToNote: the whole-file read is bounded; a
// notebook above the cap yields an explicit note with a shell/jq route.
func TestViewNotebookTooLargeDegradesToNote(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	originalCap := viewNotebookMaxBytes
	viewNotebookMaxBytes = 32
	t.Cleanup(func() { viewNotebookMaxBytes = originalCap })

	root := t.TempDir()
	writeNotebook(t, filepath.Join(root, "huge.ipynb"), []map[string]interface{}{
		{"cell_type": "markdown", "source": "# padding padding padding"},
	})
	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "huge.ipynb"})
	if result.Metadata["doc_reason"] != "notebook_too_large" || result.Metadata["doc_degraded"] != true {
		t.Fatalf("expected the size degradation, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "shell/jq") {
		t.Fatalf("expected a recovery route, got %q", result.Content)
	}
}

// TestViewEmptyNotebookGetsExplicitNote: an empty notebook must not render as a
// blank success.
func TestViewEmptyNotebookGetsExplicitNote(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	writeNotebook(t, filepath.Join(root, "empty.ipynb"), []map[string]interface{}{})
	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "empty.ipynb"})
	if !strings.Contains(result.Content, "没有可渲染的 cell") {
		t.Fatalf("expected an explicit empty-notebook note, got %q", result.Content)
	}
}

// TestViewNotebookWindowMetadataParity: derived renders publish the same
// continuation fields as the plain text path (total lines, byte-budget flag).
func TestViewNotebookWindowMetadataParity(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	cells := []map[string]interface{}{}
	for i := 0; i < 6; i++ {
		cells = append(cells, map[string]interface{}{"cell_type": "code", "source": fmt.Sprintf("print(%d)\n", i)})
	}
	writeNotebook(t, filepath.Join(root, "meta.ipynb"), cells)
	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "meta.ipynb"})
	if result.Metadata["total_lines"] == nil {
		t.Fatalf("expected total_lines like the text path, got %#v", result.Metadata)
	}
	if result.Metadata["eof"] != true || result.Metadata["is_truncated"] != false {
		t.Fatalf("expected a complete window, got %#v", result.Metadata)
	}
}

// TestViewNotebookDeduplicatesIdenticalImages: the same image output twice must
// yield one attachment and one honest count.
func TestViewNotebookDeduplicatesIdenticalImages(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	// 完整 8 字节 PNG 签名：渲染层现在会校验"解码结果确实是所声明 MIME 的容器"，
	// 截断的伪签名只是夹具问题，不是去重语义。
	payload := base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	writeNotebook(t, filepath.Join(root, "dup.ipynb"), []map[string]interface{}{
		{"cell_type": "code", "source": "a", "outputs": []map[string]interface{}{
			{"output_type": "display_data", "data": map[string]interface{}{"image/png": payload}},
			{"output_type": "display_data", "data": map[string]interface{}{"image/png": payload}},
		}},
	})
	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "dup.ipynb"})
	paths, ok := result.Metadata[toolresult.MetadataImagePathsKey].([]string)
	if !ok || len(paths) != 1 {
		t.Fatalf("identical images must collapse to one path, got %#v", result.Metadata[toolresult.MetadataImagePathsKey])
	}
	if note, _ := result.Metadata[toolresult.MetadataImageNoteKey].(string); !strings.Contains(note, "1 张") {
		t.Fatalf("expected an honest attachment count, got %q", note)
	}
}

// TestViewNotebookAttachesOnlyDeliveredWindowImages: an image far below the
// window used to be attached anyway, so the model received bytes with no
// corresponding text in the window (2026-09-27 review).
func TestViewNotebookAttachesOnlyDeliveredWindowImages(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	payload := base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	cells := []map[string]interface{}{
		{"cell_type": "markdown", "source": []string{"# head\n"}},
	}
	for i := 0; i < 40; i++ {
		cells = append(cells, map[string]interface{}{
			"cell_type": "code",
			"source":    fmt.Sprintf("step_%d()\n", i),
		})
	}
	cells = append(cells, map[string]interface{}{
		"cell_type": "code",
		"source":    "plot()\n",
		"outputs": []map[string]interface{}{
			{"output_type": "display_data", "data": map[string]interface{}{"image/png": payload}},
		},
	})
	writeNotebook(t, filepath.Join(root, "tail-image.ipynb"), cells)

	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{
		"file_path": "tail-image.ipynb",
		"offset":    0,
		"limit":     20,
	})
	if _, ok := result.Metadata[toolresult.MetadataImagePathsKey]; ok {
		t.Fatalf("an image outside the window must not be attached, got %#v", result.Metadata[toolresult.MetadataImagePathsKey])
	}
	if result.Metadata["notebook_images_outside_window"] == nil {
		t.Fatalf("expected the outside-window image count, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "不在本次窗口内") {
		t.Fatalf("expected an honest outside-window note, got %q", result.Content)
	}

	// 同一张图在完整窗口下仍然附加（防止过度收紧）。
	full := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "tail-image.ipynb"})
	paths, ok := full.Metadata[toolresult.MetadataImagePathsKey].([]string)
	if !ok || len(paths) != 1 {
		t.Fatalf("the full window must attach the image, got %#v", full.Metadata[toolresult.MetadataImagePathsKey])
	}
}

// TestViewNotebookSniffedFalsePositiveFallsBackToText: a JSON config that only
// looks notebook-shaped must not turn into a hard failure.
func TestViewNotebookSniffedFalsePositiveFallsBackToText(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	content := `{"nbformat": 4, "cells": "not-an-array"}`
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "config.json"})
	if !strings.Contains(result.Content, "not-an-array") {
		t.Fatalf("expected the plain text fallback, got %q", result.Content)
	}
}
