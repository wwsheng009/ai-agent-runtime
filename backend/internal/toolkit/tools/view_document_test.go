package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/docread"
)

func withDocreadOverrides(
	t *testing.T,
	detect func(string, []byte) docread.Probe,
	render func(context.Context, string) (docread.DocumentRender, error),
) {
	t.Helper()
	originalDetect, originalRender := docreadDetect, docreadRender
	if detect != nil {
		docreadDetect = detect
	}
	if render != nil {
		docreadRender = render
	}
	t.Cleanup(func() {
		docreadDetect, docreadRender = originalDetect, originalRender
	})
}

func seedPDFStub(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("%PDF-1.4\nstub body\n"), 0o644); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return path
}

// TestViewDocumentRendersThroughSharedWindow: a converted document feeds the
// normal readLines window, so offset/limit/line numbers behave like text.
func TestViewDocumentRendersThroughSharedWindow(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	seedPDFStub(t, root, "report.pdf")
	withDocreadOverrides(t,
		func(string, []byte) docread.Probe {
			return docread.Probe{Kind: "pdf", MIME: "application/pdf", Converter: "pdftotext", Supported: true}
		},
		func(context.Context, string) (docread.DocumentRender, error) {
			var lines strings.Builder
			for i := 1; i <= 30; i++ {
				fmt.Fprintf(&lines, "line-%d\n", i)
			}
			return docread.DocumentRender{
				Markdown: lines.String(),
				Metadata: map[string]interface{}{
					"doc_kind":      "pdf",
					"doc_mime":      "application/pdf",
					"doc_degraded":  false,
					"doc_converter": "pdftotext",
					"doc_pages":     3,
				},
			}, nil
		})

	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{
		"file_path": "report.pdf",
		"limit":     float64(10),
	})
	if !strings.Contains(result.Content, "1: line-1") {
		t.Fatalf("expected windowed markdown, got %q", result.Content)
	}
	if result.Metadata["doc_kind"] != "pdf" || result.Metadata["doc_pages"] != 3 || result.Metadata["doc_converter"] != "pdftotext" {
		t.Fatalf("expected document metadata, got %#v", result.Metadata)
	}
	if result.Metadata["is_truncated"] != true || result.Metadata["suggested_next_offset"] != 10 {
		t.Fatalf("expected continuation through the shared window, got %#v", result.Metadata)
	}
}

// TestViewDocumentWithoutConverterReturnsMIMENote: no extractor is a note, not
// a failure, and the render entry point must not even be called.
func TestViewDocumentWithoutConverterReturnsMIMENote(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "deck.pptx"), []byte("PK\x03\x04stub"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	renderCalled := false
	withDocreadOverrides(t,
		func(string, []byte) docread.Probe {
			return docread.Probe{Kind: "pptx", MIME: "application/vnd.openxmlformats-officedocument.presentationml.presentation"}
		},
		func(context.Context, string) (docread.DocumentRender, error) {
			renderCalled = true
			return docread.DocumentRender{}, nil
		})

	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "deck.pptx"})
	if renderCalled {
		t.Fatalf("renderer must not run without a converter")
	}
	if result.Metadata["doc_degraded"] != true || result.Metadata["doc_reason"] != "no_converter" {
		t.Fatalf("expected a degraded note, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "download") || !strings.Contains(result.Content, "pptx") {
		t.Fatalf("expected MIME note with recovery route, got %q", result.Content)
	}
}

// TestViewScannedPDFReportsPagesAndRecovery: page statistics become an
// actionable note instead of silent empty output (§3.9-5).
func TestViewScannedPDFReportsPagesAndRecovery(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	seedPDFStub(t, root, "scan.pdf")
	withDocreadOverrides(t,
		func(string, []byte) docread.Probe {
			return docread.Probe{Kind: "pdf", MIME: "application/pdf", Converter: "pdftotext", Supported: true}
		},
		func(context.Context, string) (docread.DocumentRender, error) {
			return docread.DocumentRender{
				Markdown: "page one text\n",
				Metadata: map[string]interface{}{
					"doc_kind":               "pdf",
					"doc_degraded":           false,
					"doc_pages":              5,
					"doc_pages_without_text": []int{2, 3},
					"doc_mime":               "application/pdf",
				},
			}, nil
		})

	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "scan.pdf"})
	if !strings.Contains(result.Content, "以下页面没有文本层") || !strings.Contains(result.Content, "2, 3") {
		t.Fatalf("expected the page list note, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "pdftoppm") {
		t.Fatalf("expected the image-channel recovery route, got %q", result.Content)
	}
}

// TestViewDocumentRenderFailureIsRecoverable: a broken converter run fails with
// the converter error plus a recovery route.
func TestViewDocumentRenderFailureIsRecoverable(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	seedPDFStub(t, root, "broken.pdf")
	withDocreadOverrides(t,
		func(string, []byte) docread.Probe {
			return docread.Probe{Kind: "pdf", MIME: "application/pdf", Converter: "pdftotext", Supported: true}
		},
		func(context.Context, string) (docread.DocumentRender, error) {
			return docread.DocumentRender{}, errors.New("pdftotext: exit status 1")
		})

	tool := newViewToolAt(t, root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "broken.pdf"})
	if err != nil {
		t.Fatalf("unexpected tool error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected a structured failure, got %+v", result)
	}
	if !strings.Contains(result.Error.Error(), "文档渲染失败") || !strings.Contains(result.Error.Error(), "download") {
		t.Fatalf("expected converter error plus recovery route, got %v", result.Error)
	}
}

// TestViewEmptyDocumentGetsExplicitNote: a converted document with no body must
// not render as a blank success.
func TestViewEmptyDocumentGetsExplicitNote(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	seedPDFStub(t, root, "empty.pdf")
	withDocreadOverrides(t,
		func(string, []byte) docread.Probe {
			return docread.Probe{Kind: "pdf", MIME: "application/pdf", Converter: "pdftotext", Supported: true}
		},
		func(context.Context, string) (docread.DocumentRender, error) {
			return docread.DocumentRender{
				Markdown: "   \n",
				Metadata: map[string]interface{}{"doc_kind": "pdf", "doc_degraded": false},
			}, nil
		})

	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "empty.pdf"})
	if !strings.Contains(result.Content, "没有可抽取的正文") {
		t.Fatalf("expected an explicit empty-document note, got %q", result.Content)
	}
}

// TestViewDocumentInnerUnsupportedKeepsRecoveryRoute: when the inner Render
// disagrees with the outer probe and reports ErrUnsupported, the note must
// still carry the download/convert route.
func TestViewDocumentInnerUnsupportedKeepsRecoveryRoute(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	seedPDFStub(t, root, "vanished.pdf")
	withDocreadOverrides(t,
		func(string, []byte) docread.Probe {
			return docread.Probe{Kind: "pdf", MIME: "application/pdf", Converter: "pdftotext", Supported: true}
		},
		func(context.Context, string) (docread.DocumentRender, error) {
			return docread.DocumentRender{}, docread.ErrUnsupported
		})

	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "vanished.pdf"})
	if result.Metadata["doc_degraded"] != true {
		t.Fatalf("expected a degraded note, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "download") || !strings.Contains(result.Content, "pdftotext") {
		t.Fatalf("expected the converter recovery route, got %q", result.Content)
	}
}

// TestViewDocumentTailReportsAbsoluteOffset: tail windows must publish the
// resolved absolute offset, exactly like the text path.
func TestViewDocumentTailReportsAbsoluteOffset(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	seedPDFStub(t, root, "tail.pdf")
	withDocreadOverrides(t,
		func(string, []byte) docread.Probe {
			return docread.Probe{Kind: "pdf", MIME: "application/pdf", Converter: "pdftotext", Supported: true}
		},
		func(context.Context, string) (docread.DocumentRender, error) {
			var lines strings.Builder
			for i := 1; i <= 50; i++ {
				fmt.Fprintf(&lines, "line-%d\n", i)
			}
			return docread.DocumentRender{
				Markdown: lines.String(),
				Metadata: map[string]interface{}{"doc_kind": "pdf", "doc_degraded": false},
			}, nil
		})

	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{
		"file_path": "tail.pdf",
		"offset":    float64(-5),
	})
	if result.Metadata["tail"] != true {
		t.Fatalf("expected tail semantics, got %#v", result.Metadata)
	}
	if result.Metadata["offset"] != 45 {
		t.Fatalf("expected the resolved absolute offset 45, got %#v", result.Metadata["offset"])
	}
	if !strings.Contains(result.Content, "line-50") {
		t.Fatalf("expected the file tail, got %q", result.Content)
	}
}

// TestViewNonPDFEmptyDocumentGetsExplicitNote: the empty-body note must not be
// PDF-specific — a docx whose converter returns nothing still owes the model an
// explanation plus the download route (review Q5 uncovered branch).
func TestViewNonPDFEmptyDocumentGetsExplicitNote(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	path := filepath.Join(root, "empty.docx")
	if err := os.WriteFile(path, []byte("PK\x03\x04stub"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	withDocreadOverrides(t,
		func(string, []byte) docread.Probe {
			return docread.Probe{Kind: "docx", MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Converter: "pandoc", Supported: true}
		},
		func(context.Context, string) (docread.DocumentRender, error) {
			return docread.DocumentRender{
				Markdown: "",
				Metadata: map[string]interface{}{
					"doc_kind":      "docx",
					"doc_mime":      "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
					"doc_degraded":  false,
					"doc_converter": "pandoc",
				},
			}, nil
		})

	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{"file_path": "empty.docx"})
	if result.Metadata["doc_kind"] != "docx" {
		t.Fatalf("expected doc_kind metadata, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "没有可抽取的正文") || !strings.Contains(result.Content, "download") {
		t.Fatalf("expected the explicit empty-document note with a recovery route, got %q", result.Content)
	}
}
