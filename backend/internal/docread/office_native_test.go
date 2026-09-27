package docread

import (
	"strings"
	"testing"
)

// TestExtractPresentationTextPPTX: slide text comes from the OOXML parts in
// slide order, so pptx renders on a host without any converter (H13).
func TestExtractPresentationTextPPTX(t *testing.T) {
	path := docreadTestZip(t, t.TempDir(), "deck.pptx", [][2]string{
		{"[Content_Types].xml", "<Types/>"},
		{"ppt/presentation.xml", "<p:presentation/>"},
		{"ppt/slides/slide2.xml", `<p:sld xmlns:a="a"><p:cSld><a:p><a:r><a:t>second slide</a:t></a:r></a:p></p:cSld></p:sld>`},
		{"ppt/slides/slide1.xml", `<p:sld xmlns:a="a"><p:cSld><a:p><a:r><a:t>first </a:t></a:r><a:r><a:t>slide</a:t></a:r></a:p><a:p><a:r><a:t>bullet</a:t></a:r></a:p></p:cSld></p:sld>`},
	})
	text, err := extractPresentationText(path, "pptx")
	if err != nil {
		t.Fatalf("extractPresentationText: %v", err)
	}
	if !strings.Contains(text, "## Slide 1") || !strings.Contains(text, "first slide") || !strings.Contains(text, "bullet") {
		t.Fatalf("slide 1 text missing: %q", text)
	}
	if strings.Index(text, "first slide") > strings.Index(text, "second slide") {
		t.Fatalf("slides must keep numeric order: %q", text)
	}
}

// TestExtractPresentationTextRejectsEmptyContainers: an unreadable container
// must fail so Render degrades instead of returning an empty document.
func TestExtractPresentationTextRejectsEmptyContainers(t *testing.T) {
	path := docreadTestZip(t, t.TempDir(), "empty.pptx", [][2]string{
		{"[Content_Types].xml", "<Types/>"},
		{"ppt/presentation.xml", "<p:presentation/>"},
	})
	if _, err := extractPresentationText(path, "pptx"); err == nil {
		t.Fatal("a pptx without slides must fail")
	}
}
