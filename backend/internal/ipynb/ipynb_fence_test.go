package ipynb

import (
	"encoding/base64"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// TestNotebookFenceForLongBacktickRuns: fence selection must be linear in the
// cell source. The old implementation grew the fence one backtick at a time and
// re-searched the source each round, allocating megabytes for a few KiB of
// backticks (2026-09-27 review).
func TestNotebookFenceForLongBacktickRuns(t *testing.T) {
	for _, n := range []int{4096, 8192} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			source := strings.Repeat("`", n)
			raw := []byte(fmt.Sprintf(`{"nbformat":4,"cells":[{"cell_type":"code","source":%q,"outputs":[]}]}`, source))
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			render, err := RenderBytes(raw)
			runtime.ReadMemStats(&after)
			if err != nil {
				t.Fatal(err)
			}
			allocated := after.TotalAlloc - before.TotalAlloc
			if allocated > uint64(len(raw))*64 {
				t.Fatalf("fence selection is not linear: %d bytes allocated for %d input bytes", allocated, len(raw))
			}
			fence := strings.Repeat("`", n+1)
			if !strings.Contains(render.Markdown, fence+"python") {
				t.Fatalf("expected a %d-backtick fence, got %q", n+1, firstLine(render.Markdown, 80))
			}
			if !strings.Contains(render.Markdown, "\n"+fence+"\n") {
				t.Fatal("expected the matching closing fence")
			}
		})
	}
}

// TestNotebookFenceForMixedRuns: only the longest run determines the fence, and
// short runs must not grow it.
func TestNotebookFenceForMixedRuns(t *testing.T) {
	cases := []struct {
		source string
		fence  string
	}{
		{source: "a `b` c", fence: "```"},
		{source: "```short```", fence: "````"},
		{source: "x ````` y `` z", fence: "``````"},
	}
	for _, tc := range cases {
		if got := notebookFenceFor(tc.source); got != tc.fence {
			t.Fatalf("notebookFenceFor(%q) = %q, want %q", tc.source, got, tc.fence)
		}
	}
}

func firstLine(text string, limit int) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	if len(text) > limit {
		return text[:limit]
	}
	return text
}

// TestRenderBytesNotesUnsupportedImageOutput: an output whose only
// representation is a MIME this renderer cannot attach (image/svg+xml) used to
// disappear with no note and outputs_omitted left at zero (2026-09-27 review).
func TestRenderBytesNotesUnsupportedImageOutput(t *testing.T) {
	raw := notebookJSON(t, []map[string]interface{}{
		{"cell_type": "code", "source": "plot()", "outputs": []map[string]interface{}{
			{"output_type": "display_data", "data": map[string]interface{}{"image/svg+xml": "<svg/>"}},
		}},
	})
	render, err := RenderBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(render.Markdown, "image/svg+xml") || !strings.Contains(render.Markdown, "unsupported image MIME") {
		t.Fatalf("unsupported image output must leave a note, got %q", render.Markdown)
	}
	if render.Metadata["outputs_omitted"] == nil || render.Metadata["outputs_omitted"].(int) == 0 {
		t.Fatalf("unsupported output must be counted as omitted, got %#v", render.Metadata["outputs_omitted"])
	}
}

// TestRenderBytesRecordsImagePlaceholderLine: the windowed reader needs to know
// which markdown line each image belongs to; without it, view had to attach the
// whole notebook's images regardless of the delivered window (2026-09-27 review).
func TestRenderBytesRecordsImagePlaceholderLine(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	raw := notebookJSON(t, []map[string]interface{}{
		{"cell_type": "markdown", "source": []string{"# head\n"}},
		{"cell_type": "code", "source": "plot()", "outputs": []map[string]interface{}{
			{"output_type": "display_data", "data": map[string]interface{}{"image/png": payload}},
		}},
	})
	render, err := RenderBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(render.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(render.Images))
	}
	lines := strings.Split(render.Markdown, "\n")
	line := render.Images[0].Line
	if line < 0 || line >= len(lines) || !strings.Contains(lines[line], "# [image]") {
		t.Fatalf("Line=%d does not point at the image placeholder:\n%s", line, render.Markdown)
	}
}

// TestRenderBytesSkipsInvalidImagePayload: a successful base64 decode is not a
// valid image. "eA==" decodes to the single byte 'x' and used to be attached as
// a .png with image_passthrough=true (2026-09-27 review).
func TestRenderBytesSkipsInvalidImagePayload(t *testing.T) {
	raw := notebookJSON(t, []map[string]interface{}{
		{"cell_type": "code", "source": "plot()", "outputs": []map[string]interface{}{
			{"output_type": "display_data", "data": map[string]interface{}{"image/png": "eA=="}},
		}},
	})
	render, err := RenderBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(render.Images) != 0 {
		t.Fatalf("an invalid PNG payload must not be attached, got %d", len(render.Images))
	}
	if !strings.Contains(render.Markdown, "不是有效图片") {
		t.Fatalf("expected the invalid-image note, got %q", render.Markdown)
	}
}
