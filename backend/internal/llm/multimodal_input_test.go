package llm

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4////fwAJ+wP+qC1oAAAAAElFTkSuQmCC"

func TestNewUserPromptMessage_AutoDetectsLocalImagePaths(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "diagram.png")
	writeTinyPNG(t, imagePath)

	message := NewUserPromptMessage("请分析这张图: " + imagePath)
	if message == nil {
		t.Fatal("expected user message")
	}
	if !MessageHasLocalInputImages(message) {
		t.Fatalf("expected local input image metadata, got %+v", message.Metadata)
	}

	images := ExtractLocalInputImages(mapFromMetadata(message.Metadata))
	if len(images) != 1 {
		t.Fatalf("expected 1 detected image, got %+v", images)
	}
	if images[0].ResolvedPath != filepath.Clean(imagePath) {
		t.Fatalf("expected resolved path %q, got %+v", filepath.Clean(imagePath), images[0])
	}
	if images[0].MimeType != "image/png" {
		t.Fatalf("expected image/png mime type, got %+v", images[0])
	}
}

func TestNewUserPromptMessageWithImages_RejectsExplicitNonImage(t *testing.T) {
	nonImagePath := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(nonImagePath, []byte("not an image"), 0644); err != nil {
		t.Fatalf("write notes.txt: %v", err)
	}

	if _, err := NewUserPromptMessageWithImages("请查看附件", []string{nonImagePath}); err == nil {
		t.Fatal("expected explicit non-image path to fail")
	}
}

func TestRuntimeMessagesToProtocolMessages_CodexEmbedsInputImageParts(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "diagram.png")
	writeTinyPNG(t, imagePath)

	message := NewUserPromptMessage("分析这张图 " + imagePath)
	protocolMessages := RuntimeMessagesToProtocolMessages([]types.Message{*message}, "codex")
	if len(protocolMessages) != 1 {
		t.Fatalf("expected one protocol message, got %#v", protocolMessages)
	}

	parts := decodeSliceOfMaps(protocolMessages[0]["content"])
	if len(parts) != 2 {
		t.Fatalf("expected text + image parts, got %#v", protocolMessages[0]["content"])
	}
	if parts[0]["type"] != "input_text" || parts[1]["type"] != "input_image" {
		t.Fatalf("unexpected codex content parts: %#v", parts)
	}
	imageURL, _ := parts[1]["image_url"].(string)
	if !strings.HasPrefix(imageURL, "data:image/png;base64,") {
		t.Fatalf("expected data URL image attachment, got %q", imageURL)
	}
}

func writeTinyPNG(t *testing.T, path string) {
	t.Helper()
	payload, err := base64.StdEncoding.DecodeString(tinyPNGBase64)
	if err != nil {
		t.Fatalf("decode tiny png: %v", err)
	}
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatalf("write tiny png: %v", err)
	}
}

func TestNewUserPromptMessageWithImages_ExplicitPaths(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "photo.png")
	writeTinyPNG(t, imagePath)

	message, err := NewUserPromptMessageWithImages("请分析附件图片", []string{imagePath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if message == nil {
		t.Fatal("expected user message")
	}
	if !MessageHasLocalInputImages(message) {
		t.Fatalf("expected local input image metadata, got %+v", message.Metadata)
	}
	images := ExtractLocalInputImages(mapFromMetadata(message.Metadata))
	if len(images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(images))
	}
	if images[0].Source != "explicit" {
		t.Fatalf("expected source=explicit, got %q", images[0].Source)
	}
}

func TestNewUserPromptMessageWithImages_MergesExplicitAndPrompt(t *testing.T) {
	image1 := filepath.Join(t.TempDir(), "a.png")
	image2 := filepath.Join(t.TempDir(), "b.png")
	writeTinyPNG(t, image1)
	writeTinyPNG(t, image2)

	message, err := NewUserPromptMessageWithImages("请看 "+image2, []string{image1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	images := ExtractLocalInputImages(mapFromMetadata(message.Metadata))
	if len(images) != 2 {
		t.Fatalf("expected 2 images (1 explicit + 1 prompt), got %d", len(images))
	}
}

func TestValidateLocalInputImagePaths_ValidPath(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "valid.png")
	writeTinyPNG(t, imagePath)

	warnings := ValidateLocalInputImagePaths([]string{imagePath})
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for valid image, got %v", warnings)
	}
}

func TestValidateLocalInputImagePaths_MissingPath(t *testing.T) {
	warnings := ValidateLocalInputImagePaths([]string{"/nonexistent/image.png"})
	if len(warnings) == 0 {
		t.Fatal("expected warning for missing image path")
	}
}

func TestValidateLocalInputImagePaths_NonImageFile(t *testing.T) {
	txtPath := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(txtPath, []byte("hello"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	warnings := ValidateLocalInputImagePaths([]string{txtPath})
	if len(warnings) == 0 {
		t.Fatal("expected warning for non-image file")
	}
}

func TestRuntimeMessagesToProtocolMessages_OpenAIEmbedsImageURL(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "diagram.png")
	writeTinyPNG(t, imagePath)

	message := NewUserPromptMessage("分析这张图 " + imagePath)
	protocolMessages := RuntimeMessagesToProtocolMessages([]types.Message{*message}, "openai")
	if len(protocolMessages) != 1 {
		t.Fatalf("expected one protocol message, got %#v", protocolMessages)
	}

	content, ok := protocolMessages[0]["content"]
	if !ok {
		t.Fatal("expected content field")
	}
	parts, ok := content.([]map[string]interface{})
	if !ok {
		// If it's just a string, the image wasn't embedded
		t.Fatalf("expected structured content parts for OpenAI with images, got %T: %v", content, content)
	}
	if len(parts) < 2 {
		t.Fatalf("expected at least text + image parts, got %d", len(parts))
	}
	hasImageURL := false
	for _, part := range parts {
		if part["type"] == "image_url" {
			hasImageURL = true
			imageURLObj, _ := part["image_url"].(map[string]interface{})
			url, _ := imageURLObj["url"].(string)
			if !strings.HasPrefix(url, "data:image/png;base64,") {
				t.Fatalf("expected data URL, got %q", url)
			}
		}
	}
	if !hasImageURL {
		t.Fatal("expected image_url part in OpenAI content")
	}
}

func TestPersistLocalInputImages_CopiesToArtifactDir(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "diagram.png")
	writeTinyPNG(t, imagePath)

	message, err := NewUserPromptMessageWithImages("分析图片", []string{imagePath})
	if err != nil {
		t.Fatalf("NewUserPromptMessageWithImages: %v", err)
	}
	if !MessageHasLocalInputImages(message) {
		t.Fatal("expected image metadata before persist")
	}

	artifactDir := filepath.Join(t.TempDir(), "artifacts", "images")
	if err := PersistLocalInputImages(message, artifactDir); err != nil {
		t.Fatalf("PersistLocalInputImages: %v", err)
	}

	images := ExtractLocalInputImages(mapFromMetadata(message.Metadata))
	if len(images) != 1 {
		t.Fatalf("expected 1 image after persist, got %d", len(images))
	}
	persistedPath := images[0].ResolvedPath
	if !strings.HasPrefix(persistedPath, artifactDir) {
		t.Fatalf("expected resolved path under %q, got %q", artifactDir, persistedPath)
	}
	if _, err := os.Stat(persistedPath); err != nil {
		t.Fatalf("persisted image file not found: %v", err)
	}
	if !strings.Contains(images[0].Source, "persisted") {
		t.Fatalf("expected source to contain 'persisted', got %q", images[0].Source)
	}
}

func TestPersistLocalInputImages_EmptyArtifactDir(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "test.png")
	writeTinyPNG(t, imagePath)

	message, err := NewUserPromptMessageWithImages("分析图片", []string{imagePath})
	if err != nil {
		t.Fatalf("NewUserPromptMessageWithImages: %v", err)
	}

	originalResolved := ExtractLocalInputImages(mapFromMetadata(message.Metadata))[0].ResolvedPath

	// Empty artifact dir should be a no-op
	if err := PersistLocalInputImages(message, ""); err != nil {
		t.Fatalf("PersistLocalInputImages with empty dir: %v", err)
	}

	images := ExtractLocalInputImages(mapFromMetadata(message.Metadata))
	if images[0].ResolvedPath != originalResolved {
		t.Fatalf("expected path unchanged, got %q vs %q", images[0].ResolvedPath, originalResolved)
	}
}
