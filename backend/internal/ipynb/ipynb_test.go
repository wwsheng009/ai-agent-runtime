package ipynb

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"unicode/utf8"
)

// testImageBytes encodes a real 1x1 image: the renderer now validates that the
// decoded bytes are a complete container of the declared MIME, so signature-only
// fixtures are no longer representative (2026-09-27 review H10).
func testImageBytes(t *testing.T, format string) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	var err error
	switch format {
	case "png":
		err = png.Encode(&buf, img)
	case "gif":
		err = gif.Encode(&buf, img, nil)
	case "jpeg":
		err = jpeg.Encode(&buf, img, nil)
	default:
		t.Fatalf("unsupported test image format %q", format)
	}
	if err != nil {
		t.Fatalf("encode %s: %v", format, err)
	}
	return buf.Bytes()
}

func notebookJSON(t *testing.T, cells []map[string]interface{}) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]interface{}{
		"nbformat": 4,
		"cells":    cells,
	})
	if err != nil {
		t.Fatalf("marshal notebook: %v", err)
	}
	return raw
}

func TestRenderBytesTagsCellsAndOutputs(t *testing.T) {
	raw := notebookJSON(t, []map[string]interface{}{
		{"cell_type": "markdown", "source": []string{"# Title\n", "intro text\n"}},
		{
			"cell_type": "code",
			"source":    "print('hi')\n",
			"outputs": []map[string]interface{}{
				{"output_type": "stream", "name": "stdout", "text": []string{"hi\n"}},
				{"output_type": "execute_result", "data": map[string]interface{}{"text/plain": []string{"42"}}},
				{"output_type": "error", "ename": "ValueError", "evalue": "bad input"},
			},
		},
	})

	render, err := RenderBytes(raw)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"# %% [markdown] cell 1",
		"# Title",
		"# %% [code] cell 2",
		"```python\nprint('hi')\n```",
		"# output (stdout):\nhi",
		"# output (result):\n42",
		"# error: ValueError: bad input",
	} {
		if !strings.Contains(render.Markdown, want) {
			t.Fatalf("markdown missing %q:\n%s", want, render.Markdown)
		}
	}
	if render.Metadata["cells"] != 2 || render.Metadata["code_cells"] != 1 || render.Metadata["markdown_cells"] != 1 {
		t.Fatalf("unexpected metadata: %#v", render.Metadata)
	}
	// Cell order must be preserved: markdown header before the code header.
	if strings.Index(render.Markdown, "cell 1") > strings.Index(render.Markdown, "cell 2") {
		t.Fatalf("cell order broke: %s", render.Markdown)
	}
}

func TestRenderBytesFoldsLargeOutputToJQPointer(t *testing.T) {
	big := strings.Repeat("x", maxOutputChars+1)
	raw := notebookJSON(t, []map[string]interface{}{
		{"cell_type": "code", "source": "1", "outputs": []map[string]interface{}{
			{"output_type": "stream", "name": "stdout", "text": big},
		}},
		{"cell_type": "code", "source": "2", "outputs": []map[string]interface{}{
			{"output_type": "execute_result", "data": map[string]interface{}{"text/plain": []string{big}}},
		}},
	})

	render, err := RenderBytes(raw)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(render.Markdown, big) {
		t.Fatalf("large output must not be inlined")
	}
	if !strings.Contains(render.Markdown, `{"jq": ".cells[0].outputs[0].text", "note": "output omitted; use shell/jq to inspect"}`) {
		t.Fatalf("missing stream jq pointer:\n%s", render.Markdown)
	}
	if !strings.Contains(render.Markdown, `.cells[1].outputs[0].data[\"text/plain\"]`) {
		t.Fatalf("missing MIME jq pointer:\n%s", render.Markdown)
	}
	if render.Metadata["outputs_omitted"] != 2 {
		t.Fatalf("expected two folded outputs, got %#v", render.Metadata["outputs_omitted"])
	}
}

func TestRenderBytesDecodesImageOutputs(t *testing.T) {
	payload := testImageBytes(t, "png")
	raw := notebookJSON(t, []map[string]interface{}{
		{"cell_type": "code", "source": "plot()", "outputs": []map[string]interface{}{
			{"output_type": "display_data", "data": map[string]interface{}{
				"image/png":  base64.StdEncoding.EncodeToString(payload),
				"text/plain": []string{"<Figure>"},
			}},
		}},
	})

	render, err := RenderBytes(raw)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(render.Images) != 1 {
		t.Fatalf("expected one decoded image, got %#v", render.Images)
	}
	image := render.Images[0]
	if image.MIME != "image/png" || image.Cell != 0 || image.Output != 0 || string(image.Data) != string(payload) {
		t.Fatalf("unexpected image output: %+v", image)
	}
	if !strings.Contains(render.Markdown, fmt.Sprintf("# [image] cell 1 output 0: image/png (%d bytes)", len(payload))) {
		t.Fatalf("missing image marker:\n%s", render.Markdown)
	}
	if render.Metadata["images"] != 1 {
		t.Fatalf("expected image count metadata, got %#v", render.Metadata)
	}
}

func TestRenderBytesRejectsInvalidNotebook(t *testing.T) {
	if _, err := RenderBytes([]byte("{not json")); err == nil {
		t.Fatal("expected a JSON error")
	}
	if _, err := RenderBytes([]byte(`{"nbformat":4}`)); err == nil {
		t.Fatal("expected a missing-cells error")
	}
}

func TestRenderBytesEmptyNotebook(t *testing.T) {
	render, err := RenderBytes([]byte(`{"nbformat":4,"cells":[]}`))
	if err != nil {
		t.Fatalf("empty notebook must render, got %v", err)
	}
	if render.Markdown != "" || render.Metadata["cells"] != 0 || render.Metadata["images"] != 0 {
		t.Fatalf("unexpected empty render: %#v %q", render.Metadata, render.Markdown)
	}
}

// TestRenderBytesFoldedPointerMatchesTheChosenField: with an empty text/plain
// and a long text/markdown, the folded jq pointer must address text/markdown —
// the old code keyed off existence, not the field that was rendered.
func TestRenderBytesFoldedPointerMatchesTheChosenField(t *testing.T) {
	big := strings.Repeat("長", maxOutputChars+1)
	raw := notebookJSON(t, []map[string]interface{}{
		{"cell_type": "code", "source": "1", "outputs": []map[string]interface{}{
			{"output_type": "execute_result", "data": map[string]interface{}{
				"text/plain":    []string{""},
				"text/markdown": big,
			}},
		}},
	})
	render, err := RenderBytes(raw)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(render.Markdown, `.data[\"text/markdown\"]`) {
		t.Fatalf("pointer must address the folded field:\n%s", render.Markdown)
	}
	if strings.Contains(render.Markdown, `.data[\"text/plain\"]`) {
		t.Fatalf("pointer must not address the empty field:\n%s", render.Markdown)
	}
}

// TestRenderBytesCountsRunesForTheFoldThreshold: the documented 10,000 limit
// is characters; 6,000 CJK runes must stay inline even though they exceed
// 10,000 bytes.
func TestRenderBytesCountsRunesForTheFoldThreshold(t *testing.T) {
	inlineText := strings.Repeat("中", 6000) // 18,000 bytes, 6,000 runes
	raw := notebookJSON(t, []map[string]interface{}{
		{"cell_type": "code", "source": "1", "outputs": []map[string]interface{}{
			{"output_type": "stream", "name": "stdout", "text": inlineText},
		}},
	})
	render, err := RenderBytes(raw)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(render.Markdown, inlineText) {
		t.Fatalf("6000 runes must stay inline (bytes are not runes)")
	}
	if render.Metadata["outputs_omitted"] != 0 {
		t.Fatalf("nothing should be folded, got %#v", render.Metadata["outputs_omitted"])
	}
}

func TestRenderBytesToleratesUTF8BOM(t *testing.T) {
	raw := append([]byte{0xEF, 0xBB, 0xBF}, notebookJSON(t, []map[string]interface{}{
		{"cell_type": "markdown", "source": "# BOM\n"},
	})...)
	render, err := RenderBytes(raw)
	if err != nil {
		t.Fatalf("BOM-prefixed notebook must parse, got %v", err)
	}
	if !strings.Contains(render.Markdown, "# BOM") {
		t.Fatalf("unexpected render: %q", render.Markdown)
	}
}

func TestRenderBytesSkipsOversizedImageBeforeDecoding(t *testing.T) {
	originalSingle, originalTotal := maxImageBytes, maxTotalImageBytes
	maxImageBytes, maxTotalImageBytes = 8, 1<<20
	t.Cleanup(func() { maxImageBytes, maxTotalImageBytes = originalSingle, originalTotal })

	payload := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x01}, 64))
	raw := notebookJSON(t, []map[string]interface{}{
		{"cell_type": "code", "source": "plot()", "outputs": []map[string]interface{}{
			{"output_type": "display_data", "data": map[string]interface{}{"image/png": payload}},
		}},
	})
	render, err := RenderBytes(raw)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(render.Images) != 0 {
		t.Fatalf("oversized image must not be decoded/attached, got %d", len(render.Images))
	}
	if !strings.Contains(render.Markdown, "超过 8 字节上限未附加") {
		t.Fatalf("expected the size note, got %q", render.Markdown)
	}
}

func TestRenderBytesEnforcesTotalImageBudget(t *testing.T) {
	payloadBytes := testImageBytes(t, "png")
	originalSingle, originalTotal := maxImageBytes, maxTotalImageBytes
	maxImageBytes, maxTotalImageBytes = 1<<20, len(payloadBytes)+1
	t.Cleanup(func() { maxImageBytes, maxTotalImageBytes = originalSingle, originalTotal })

	payload := base64.StdEncoding.EncodeToString(payloadBytes)
	raw := notebookJSON(t, []map[string]interface{}{
		{"cell_type": "code", "source": "a", "outputs": []map[string]interface{}{
			{"output_type": "display_data", "data": map[string]interface{}{"image/png": payload}},
			{"output_type": "display_data", "data": map[string]interface{}{"image/png": payload}},
		}},
	})
	render, err := RenderBytes(raw)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(render.Images) != 1 {
		t.Fatalf("only the first image fits the total budget, got %d", len(render.Images))
	}
	if !strings.Contains(render.Markdown, fmt.Sprintf("notebook 图片总量超过 %d 字节上限未附加", len(payloadBytes)+1)) {
		t.Fatalf("expected the total-budget note, got %q", render.Markdown)
	}
}

func TestTruncateRunesKeepsValidUTF8(t *testing.T) {
	message := strings.Repeat("错", 400)
	truncated := truncateRunes(message, 300)
	if !utf8.ValidString(truncated) {
		t.Fatalf("truncation must keep valid UTF-8")
	}
	if utf8.RuneCountInString(truncated) != 300 {
		t.Fatalf("expected 300 runes, got %d", utf8.RuneCountInString(truncated))
	}
}

// TestRenderBytesHandlesUpdateDisplayDataAndGIF: update_display_data is a legal
// notebook output type, and the image channel supports gif.
func TestRenderBytesHandlesUpdateDisplayDataAndGIF(t *testing.T) {
	gifPayload := base64.StdEncoding.EncodeToString(testImageBytes(t, "gif"))
	raw := notebookJSON(t, []map[string]interface{}{
		{"cell_type": "code", "source": "animate()", "outputs": []map[string]interface{}{
			{"output_type": "update_display_data", "data": map[string]interface{}{
				"image/gif":  gifPayload,
				"text/plain": []string{"<Animation>"},
			}},
		}},
	})
	render, err := RenderBytes(raw)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(render.Images) != 1 || render.Images[0].MIME != "image/gif" {
		t.Fatalf("update_display_data gif must decode, got %#v", render.Images)
	}
	if !strings.Contains(render.Markdown, "# output (result):") {
		t.Fatalf("update_display_data text must render, got %q", render.Markdown)
	}
}

// TestRenderBytesRejectsNullCells: an explicit null must fail with the missing
// field diagnosis rather than rendering an empty notebook.
func TestRenderBytesRejectsNullCells(t *testing.T) {
	_, err := RenderBytes([]byte(`{"nbformat":4,"cells":null}`))
	if err == nil || !strings.Contains(err.Error(), "cells") {
		t.Fatalf("expected a cells-field error, got %v", err)
	}
}

// TestRenderBytesBoundsCellWork: rendering work is capped and the omission is
// reported in metadata instead of silently dropping cells.
func TestRenderBytesBoundsCellWork(t *testing.T) {
	cells := make([]map[string]interface{}, 0, maxCells+1)
	for i := 0; i <= maxCells; i++ {
		cells = append(cells, map[string]interface{}{"cell_type": "code", "source": "x\n"})
	}
	render, err := RenderBytes(notebookJSON(t, cells))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := render.Metadata["notebook_cells_omitted"]; got != 1 {
		t.Fatalf("expected one omitted cell, got %#v", got)
	}
}
