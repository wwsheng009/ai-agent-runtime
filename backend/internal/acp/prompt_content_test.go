package acp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

var promptImageFixture = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00}

func TestExtractPromptContent_TextSelectionAndImage(t *testing.T) {
	blocks := []ContentBlock{
		TextContent("review this change"),
		{
			Type:     "resource",
			Resource: json.RawMessage(`{"uri":"file:///repo/main.go","mimeType":"text/x-go","text":"func main() {}"}`),
		},
		{
			Type:     "image",
			Data:     base64.StdEncoding.EncodeToString(promptImageFixture),
			MIMEType: "image/png",
		},
	}

	content, err := ExtractPromptContent(blocks)
	if err != nil {
		t.Fatalf("ExtractPromptContent failed: %v", err)
	}
	wantText := "review this change\n\nfunc main() {}"
	if content.Text != wantText {
		t.Fatalf("text = %q, want %q", content.Text, wantText)
	}
	if len(content.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(content.Images))
	}
	if !bytes.Equal(content.Images[0].Data, promptImageFixture) {
		t.Fatalf("image bytes = %v, want %v", content.Images[0].Data, promptImageFixture)
	}
	if content.Images[0].MIMEType != "image/png" {
		t.Fatalf("image mime = %q, want image/png", content.Images[0].MIMEType)
	}
}

func TestExtractPromptContent_ImageDataURLAndWhitespace(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(promptImageFixture)
	blocks := []ContentBlock{{
		Type: "image",
		Data: "data:image/jpeg;base64," + encoded[:4] + "\n" + encoded[4:],
	}}

	content, err := ExtractPromptContent(blocks)
	if err != nil {
		t.Fatalf("ExtractPromptContent failed: %v", err)
	}
	if len(content.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(content.Images))
	}
	if content.Images[0].MIMEType != "image/jpeg" {
		t.Fatalf("image mime = %q, want image/jpeg", content.Images[0].MIMEType)
	}
	if !bytes.Equal(content.Images[0].Data, promptImageFixture) {
		t.Fatalf("image bytes = %v, want %v", content.Images[0].Data, promptImageFixture)
	}
}

func TestExtractPromptContent_EmbeddedImageBlob(t *testing.T) {
	blocks := []ContentBlock{{
		Type: "resource",
		Resource: json.RawMessage(`{"uri":"file:///tmp/shot.png","mimeType":"image/png","blob":"` +
			base64.StdEncoding.EncodeToString(promptImageFixture) + `"}`),
	}}

	content, err := ExtractPromptContent(blocks)
	if err != nil {
		t.Fatalf("ExtractPromptContent failed: %v", err)
	}
	if content.Text != "" {
		t.Fatalf("text = %q, want empty", content.Text)
	}
	if len(content.Images) != 1 || content.Images[0].URI != "file:///tmp/shot.png" {
		t.Fatalf("images = %+v, want one image carrying the resource uri", content.Images)
	}
}

func TestExtractPromptContent_EmbeddedBinaryResourceKeepsPlaceholder(t *testing.T) {
	blocks := []ContentBlock{{
		Type:     "resource",
		Resource: json.RawMessage(`{"uri":"file:///tmp/blob.bin","mimeType":"application/octet-stream","blob":"AAECAw=="}`),
	}}

	content, err := ExtractPromptContent(blocks)
	if err != nil {
		t.Fatalf("ExtractPromptContent failed: %v", err)
	}
	if !strings.Contains(content.Text, "file:///tmp/blob.bin") {
		t.Fatalf("text = %q, want a labeled placeholder for the binary resource", content.Text)
	}
}

func TestExtractPromptContent_RejectsUnsupportedPayloads(t *testing.T) {
	cases := []struct {
		name   string
		blocks []ContentBlock
		want   string
	}{
		{
			name:   "audio",
			blocks: []ContentBlock{{Type: "audio", Data: "AAAA", MIMEType: "audio/wav"}},
			want:   "audio prompts are not supported",
		},
		{
			name:   "unknown type",
			blocks: []ContentBlock{{Type: "video", Data: "AAAA", MIMEType: "video/mp4"}},
			want:   "unsupported content type",
		},
		{
			name:   "svg image",
			blocks: []ContentBlock{{Type: "image", Data: base64.StdEncoding.EncodeToString([]byte("<svg/>")), MIMEType: "image/svg+xml"}},
			want:   "not a supported image type",
		},
		{
			name:   "missing mime",
			blocks: []ContentBlock{{Type: "image", Data: base64.StdEncoding.EncodeToString(promptImageFixture)}},
			want:   "not a supported image type",
		},
		{
			name:   "invalid base64",
			blocks: []ContentBlock{{Type: "image", Data: "not-base64!!", MIMEType: "image/png"}},
			want:   "not valid base64",
		},
		{
			name:   "empty data",
			blocks: []ContentBlock{{Type: "image", Data: "   ", MIMEType: "image/png"}},
			want:   "has no data",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ExtractPromptContent(tc.blocks)
			if err == nil {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want containing %q", err.Error(), tc.want)
			}
		})
	}
}

func TestExtractText_StaysLenientForUnsupportedBlocks(t *testing.T) {
	blocks := []ContentBlock{
		TextContent("keep me"),
		{Type: "image", Data: "not-base64!!", MIMEType: "image/png"},
		{Type: "audio", Data: "AAAA", MIMEType: "audio/wav"},
	}

	if got := ExtractText(blocks); got != "keep me" {
		t.Fatalf("ExtractText = %q, want %q", got, "keep me")
	}
}

func TestFullPromptCapabilities(t *testing.T) {
	caps := FullPromptCapabilities()
	if caps == nil {
		t.Fatal("FullPromptCapabilities returned nil")
	}
	if !caps.Image || !caps.EmbeddedContext {
		t.Fatalf("capabilities = %+v, want image and embeddedContext on", caps)
	}
	if caps.Audio {
		t.Fatalf("capabilities = %+v, audio must stay off (no runtime audio input path)", caps)
	}
	if DefaultAgentCapabilities().PromptCapabilities.Image {
		t.Fatal("DefaultAgentCapabilities must stay text-only so hosts opt in explicitly")
	}
}
