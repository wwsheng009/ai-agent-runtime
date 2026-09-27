package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// TestNotebookWithImageKeepsTextAtModelBoundary pins finding H5: a notebook
// render with image attachments is text plus images; marking it structured made
// the model-facing reducer replace the whole body with a generic envelope
// summary, so the model got the images but none of the notebook text.
func TestNotebookWithImageKeepsTextAtModelBoundary(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	const marker = "NOTEBOOK-TEXT-MUST-REACH-THE-MODEL"
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	writeNotebook(t, filepath.Join(root, "analysis.ipynb"), []map[string]interface{}{
		{"cell_type": "markdown", "source": []string{"# " + marker + "\n"}},
		{"cell_type": "code", "source": "plot()", "outputs": []map[string]interface{}{
			{"output_type": "display_data", "data": map[string]interface{}{
				"image/png": base64.StdEncoding.EncodeToString(buf.Bytes()),
			}},
		}},
	})

	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "analysis.ipynb"})
	if result.OutputKind != toolresult.KindText {
		t.Fatalf("a notebook render is text plus attachments, got output kind %q", result.OutputKind)
	}
	if result.Metadata[toolresult.MetadataImagePassthroughKey] != true {
		t.Fatalf("expected the image passthrough contract, got %#v", result.Metadata)
	}
	envelope := &output.Envelope{ToolName: "view", ToolCallID: "call-h5", Metadata: result.MetadataWithOutputKind()}
	modelContent := output.RenderToolResultContentForModel(result.Content, "", envelope)
	if !strings.Contains(modelContent, marker) {
		t.Fatalf("the notebook text window must reach the model, got %q", modelContent)
	}
}

// TestHeaderOnlyImageIsNotAttached pins finding H10 on the plain image path: a
// file with a valid PNG header but no pixel data must not be advertised as an
// attachable image (a strict provider would reject the whole request).
func TestHeaderOnlyImageIsNotAttached(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "broken.png"), headerOnlyPNG(t, 1, 1), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newViewToolAt(t, root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "broken.png"})
	if err != nil || result == nil {
		t.Fatalf("view: %v %+v", err, result)
	}
	if result.Metadata[toolresult.MetadataImagePassthroughKey] == true {
		t.Fatalf("an incomplete PNG must not be attached, got %#v", result.Metadata)
	}
	if strings.TrimSpace(result.Content) == "" {
		t.Fatalf("refusal must stay recoverable (MIME note), got %+v", result)
	}
}

// headerOnlyPNG builds signature + IHDR + IEND with no IDAT: png.DecodeConfig
// reports the dimensions, but the image has no pixel data.
func headerOnlyPNG(t *testing.T, width, height uint32) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], width)
	binary.BigEndian.PutUint32(ihdr[4:8], height)
	ihdr[8], ihdr[9] = 8, 6
	writeReviewPNGChunk(&buf, "IHDR", ihdr)
	writeReviewPNGChunk(&buf, "IEND", nil)
	return buf.Bytes()
}

func writeReviewPNGChunk(buf *bytes.Buffer, kind string, data []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(data)))
	buf.Write(length[:])
	buf.WriteString(kind)
	buf.Write(data)
	crc := crc32.NewIEEE()
	_, _ = crc.Write([]byte(kind))
	_, _ = crc.Write(data)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc.Sum32())
	buf.Write(sum[:])
}
