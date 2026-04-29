package llm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolnames"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestBuildToolDefinitionsForRequest_CodexAddsImageGenerationWhenModelCapabilityAllows(t *testing.T) {
	defs := []types.ToolDefinition{
		{
			Name:        "bash",
			Description: "run shell",
			Parameters:  map[string]interface{}{"type": "object"},
		},
	}

	got := BuildToolDefinitionsForRequest(defs, "codex", "gpt-5.4", map[string]agentconfig.ModelCapabilitySpec{
		"gpt-5.4": {
			InputModalities: []string{"text", "image"},
			NativeTools: agentconfig.NativeToolCapabilities{
				ImageGeneration: true,
			},
		},
	}, false)

	toolList, ok := got.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, toolList, 2)

	var sawBash bool
	var sawImageGeneration bool
	for _, tool := range toolList {
		if tool["name"] == "bash" {
			sawBash = true
		}
		if tool["type"] == "image_generation" {
			sawImageGeneration = true
			require.Equal(t, "png", tool["output_format"])
		}
	}

	require.True(t, sawBash)
	require.True(t, sawImageGeneration)
}

func TestBuildToolDefinitionsForRequest_DoesNotAddImageGenerationWithoutTextImageSupport(t *testing.T) {
	got := BuildToolDefinitionsForRequest(nil, "codex", "gpt-5.4", map[string]agentconfig.ModelCapabilitySpec{
		"gpt-5.4": {
			InputModalities: []string{"text"},
			NativeTools: agentconfig.NativeToolCapabilities{
				ImageGeneration: true,
			},
		},
	}, false)

	require.Nil(t, got)
}

func TestBuildToolDefinitionsForRequest_CodexNativeImageHidesOpenAIImageGenerateTool(t *testing.T) {
	defs := []types.ToolDefinition{
		{
			Name:        "bash",
			Description: "run shell",
			Parameters:  map[string]interface{}{"type": "object"},
		},
		{
			Name:        toolnames.OpenAIImageGenerateToolName,
			Description: "generate image",
			Parameters:  map[string]interface{}{"type": "object"},
		},
		{
			Name:        toolnames.LegacyImageGenerateToolName,
			Description: "legacy generate image",
			Parameters:  map[string]interface{}{"type": "object"},
		},
	}

	got := BuildToolDefinitionsForRequest(defs, "codex", "gpt-5.4", map[string]agentconfig.ModelCapabilitySpec{
		"gpt-5.4": {
			InputModalities: []string{"text", "image"},
			NativeTools: agentconfig.NativeToolCapabilities{
				ImageGeneration: true,
			},
		},
	}, false)

	toolList, ok := got.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, toolList, 2)

	var sawBash bool
	var sawNativeImageGeneration bool
	for _, tool := range toolList {
		require.NotEqual(t, toolnames.OpenAIImageGenerateToolName, tool["name"])
		require.NotEqual(t, toolnames.LegacyImageGenerateToolName, tool["name"])
		if tool["name"] == "bash" {
			sawBash = true
		}
		if tool["type"] == "image_generation" {
			sawNativeImageGeneration = true
		}
	}

	require.True(t, sawBash)
	require.True(t, sawNativeImageGeneration)
}

func TestBuildToolDefinitionsForRequest_KeepsOpenAIImageGenerateToolWhenCodexNativeUnavailable(t *testing.T) {
	defs := []types.ToolDefinition{
		{
			Name:        toolnames.OpenAIImageGenerateToolName,
			Description: "generate image",
			Parameters:  map[string]interface{}{"type": "object"},
		},
	}

	got := BuildToolDefinitionsForRequest(defs, "codex", "gpt-5.4", map[string]agentconfig.ModelCapabilitySpec{
		"gpt-5.4": {
			InputModalities: []string{"text"},
			NativeTools: agentconfig.NativeToolCapabilities{
				ImageGeneration: true,
			},
		},
	}, false)

	toolList, ok := got.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, toolList, 1)
	require.Equal(t, toolnames.OpenAIImageGenerateToolName, toolList[0]["name"])
}

func TestProcessCodexAssistantImageGeneration_SavesImagesAndSanitizesReplayMetadata(t *testing.T) {
	outputDir := t.TempDir()
	msg := map[string]interface{}{
		"role": "assistant",
		"response_output_items": []map[string]interface{}{
			{
				"type":           "image_generation_call",
				"id":             "img:1",
				"status":         "generating",
				"action":         "generate",
				"background":     "opaque",
				"output_format":  "png",
				"quality":        "medium",
				"size":           "1024x1024",
				"revised_prompt": "draw a red square",
				"result":         "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4////fwAJ+wP9KobjigAAAABJRU5ErkJggg==",
			},
		},
	}

	images, err := ProcessCodexAssistantImageGeneration(msg, outputDir)
	require.NoError(t, err)
	require.Len(t, images, 1)

	image := images[0]
	require.Equal(t, "img:1", image.ID)
	require.Equal(t, filepath.Join(outputDir, "img_1.png"), image.SavedPath)

	bytes, readErr := os.ReadFile(image.SavedPath)
	require.NoError(t, readErr)
	require.NotEmpty(t, bytes)

	items := decodeSliceOfMaps(msg["response_output_items"])
	require.Len(t, items, 1)
	_, hasRawPayload := items[0]["result"]
	require.False(t, hasRawPayload)
	require.Equal(t, "img:1", items[0]["id"])
	require.Equal(t, "completed", items[0]["status"])
	require.Equal(t, "draw a red square", items[0]["revised_prompt"])
	_, hasAction := items[0]["action"]
	require.False(t, hasAction)
	_, hasBackground := items[0]["background"]
	require.False(t, hasBackground)
	_, hasOutputFormat := items[0]["output_format"]
	require.False(t, hasOutputFormat)
	_, hasQuality := items[0]["quality"]
	require.False(t, hasQuality)
	_, hasSize := items[0]["size"]
	require.False(t, hasSize)

	metadata := decodeMapAny(msg["metadata"])
	require.NotNil(t, metadata)
	generated := decodeSliceOfMaps(metadata[MetadataKeyGeneratedImages])
	require.Len(t, generated, 1)
	require.Equal(t, image.SavedPath, generated[0]["saved_path"])

	require.Equal(t, GeneratedImageSummary(images), msg["content"])
}

func TestGeneratedImageSummary_WrapsSavedPathAsMarkdownFileLink(t *testing.T) {
	savedPath := filepath.Join(t.TempDir(), "img_1.png")
	summary := GeneratedImageSummary([]GeneratedImage{{SavedPath: savedPath}})

	require.Equal(t,
		"Generated image saved to "+markdownLinkForLocalPath(savedPath),
		summary,
	)
	require.Contains(t, summary, "[")
	require.Contains(t, summary, "](file://")
}
