package agent

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func writeAgentTestPNG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create png: %v", err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
}

func TestMessageBuilderInjectsImagePassthroughUserMessage(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "shot.png")
	writeAgentTestPNG(t, imagePath)

	builder := NewMessageBuilder(nil)
	calls := []types.ToolCall{{ID: "call_view_1", Name: "view", Args: map[string]interface{}{"file_path": "shot.png"}}}
	results := []ToolResultPayload{{
		ToolCallID: "call_view_1",
		Content:    "图片文件: shot.png",
		Metadata: types.Metadata{
			toolresult.MetadataImagePassthroughKey: true,
			toolresult.MetadataImagePathKey:        imagePath,
			toolresult.MetadataImageMimeTypeKey:    "image/png",
		},
	}}
	builder.AppendToolResults(calls, results)

	history := builder.Messages()
	if len(history) != 2 {
		t.Fatalf("expected tool message + injected user message, got %d: %#v", len(history), history)
	}
	if history[0].Role != "tool" {
		t.Fatalf("expected first message role tool, got %q", history[0].Role)
	}
	if history[1].Role != "user" {
		t.Fatalf("expected injected user message after tool results, got %q", history[1].Role)
	}
	if !llm.MessageHasLocalInputImages(&history[1]) {
		t.Fatalf("expected input_images metadata, got %#v", history[1].Metadata)
	}
	images := llm.ExtractLocalInputImages(map[string]interface{}(history[1].Metadata))
	if len(images) != 1 || images[0].MimeType != "image/png" {
		t.Fatalf("unexpected extracted images: %#v", images)
	}
}

func TestMessageBuilderSkipsImageInjectionWithoutMetadata(t *testing.T) {
	builder := NewMessageBuilder(nil)
	calls := []types.ToolCall{{ID: "call_1", Name: "view", Args: map[string]interface{}{"file_path": "README.md"}}}
	results := []ToolResultPayload{{ToolCallID: "call_1", Content: "file content"}}
	builder.AppendToolResults(calls, results)

	history := builder.Messages()
	if len(history) != 1 || history[0].Role != "tool" {
		t.Fatalf("expected a single tool message, got %#v", history)
	}
}

func TestMessageBuilderSkipsMissingPassthroughFile(t *testing.T) {
	builder := NewMessageBuilder(nil)
	calls := []types.ToolCall{{ID: "call_1", Name: "view", Args: map[string]interface{}{"file_path": "gone.png"}}}
	results := []ToolResultPayload{{
		ToolCallID: "call_1",
		Content:    "图片文件: gone.png",
		Metadata: types.Metadata{
			toolresult.MetadataImagePassthroughKey: true,
			toolresult.MetadataImagePathKey:        filepath.Join(t.TempDir(), "gone.png"),
		},
	}}
	builder.AppendToolResults(calls, results)

	history := builder.Messages()
	if len(history) != 1 {
		t.Fatalf("missing image file must not inject a user message, got %#v", history)
	}
}
