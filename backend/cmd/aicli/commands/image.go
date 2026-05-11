package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
)

const defaultImageCommandOutputDir = "generated-images"

type imageGenerateCommandRequest struct {
	Config            *config.Config
	Session           *ChatSession
	Prompt            string
	Provider          string
	Model             string
	Path              string
	N                 int
	Size              string
	Quality           string
	Background        string
	OutputFormat      string
	OutputCompression *int
	OutputDir         string
	Timeout           time.Duration
	Debug             bool
	DebugWriter       io.Writer
}

type imageGenerateCommandResult struct {
	Success   bool                   `json:"success"`
	Output    string                 `json:"output,omitempty"`
	OutputDir string                 `json:"output_dir,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Images    interface{}            `json:"images,omitempty"`
}

// NewImageCommand creates the direct image generation command.
func NewImageCommand(configProvider func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "image [prompt]",
		Aliases: []string{"img"},
		Short:   "直接调用图片生成工具",
		Long:    "直接调用 openai_image_generate，通过 OpenAI 兼容 /v1/images/generations 或 Codex 原生 image_generation 生成图片并保存到本地目录。",
		Example: `  aicli image "一只在月光下奔跑的猫"
	aicli image --provider SENSENOVA_IMAGE --model sensenova-u1-fast "海边日落照片"
	aicli image --prompt "产品海报，白底" --size 1024x1024 --output-dir ./out/images
  aicli image "生成一张壁纸" --json
  aicli image --provider SENSENOVA_IMAGE --debug --json "生成一张壁纸"`,
		Args: cobra.ArbitraryArgs,
		Run: func(cmd *cobra.Command, args []string) {
			HandleImageGenerate(cmd, args, configProvider)
		},
	}
	cmd.Flags().StringP("prompt", "p", "", "图片提示词；未指定时使用位置参数拼接")
	cmd.Flags().String("provider", "", "指定图片生成 provider 名称（如 OPENAI_IMAGE、SENSENOVA_IMAGE）")
	cmd.Flags().StringP("model", "m", "", "指定图像模型名称")
	cmd.Flags().String("path", "auto", "图片生成路径（auto|images_generations_api|api|codex_native|native）")
	cmd.Flags().Int("n", 0, "生成图片数量；0 表示使用工具默认值")
	cmd.Flags().String("size", "", "图片尺寸（如 1024x1024、1536x1024、auto）")
	cmd.Flags().String("quality", "", "图片质量（low|medium|high|auto）")
	cmd.Flags().String("background", "", "背景模式（transparent|opaque|auto）")
	cmd.Flags().String("output-format", "", "图片输出格式（png|jpeg|webp）")
	cmd.Flags().Int("output-compression", 0, "输出压缩级别（0-100），仅显式传入时生效")
	cmd.Flags().String("output-dir", defaultImageCommandOutputDir, "生成图片保存目录")
	cmd.Flags().Int("timeout", 0, "命令超时时间（秒）；0 表示不额外设置")
	cmd.Flags().Bool("debug", false, "向 stderr 输出图片生成调试过程（不影响 JSON stdout）")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "兼容选项：等价于 --output json")
	return cmd
}

func HandleImageGenerate(cmd *cobra.Command, args []string, configProvider func() *config.Config) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("image", "json", err, nil)
	}
	cfg := (*config.Config)(nil)
	if configProvider != nil {
		cfg = configProvider()
	}

	timeoutSec := 0
	if cmd != nil {
		timeoutSec, _ = cmd.Flags().GetInt("timeout")
	}
	req := imageGenerateCommandRequest{
		Config:       cfg,
		Prompt:       imagePromptFromFlags(cmd, args),
		Provider:     stringFlag(cmd, "provider"),
		Model:        stringFlag(cmd, "model"),
		Path:         stringFlag(cmd, "path"),
		N:            intFlag(cmd, "n"),
		Size:         stringFlag(cmd, "size"),
		Quality:      stringFlag(cmd, "quality"),
		Background:   stringFlag(cmd, "background"),
		OutputFormat: stringFlag(cmd, "output-format"),
		OutputDir:    stringFlag(cmd, "output-dir"),
		Timeout:      time.Duration(timeoutSec) * time.Second,
		Debug:        boolFlag(cmd, "debug"),
		DebugWriter:  os.Stderr,
	}
	if flagChanged(cmd, "output-compression") {
		value := intFlag(cmd, "output-compression")
		req.OutputCompression = &value
	}

	executeCommand("image", outputOptions, func() (*imageGenerateCommandResult, map[string]interface{}, error) {
		return runImageGenerateCommand(req)
	}, renderImageGenerateCommandResult)
}

func runImageGenerateCommand(req imageGenerateCommandRequest) (*imageGenerateCommandResult, map[string]interface{}, error) {
	prompt := strings.TrimSpace(req.Prompt)
	outputDir, err := resolveImageCommandOutputDir(req.OutputDir)
	result := &imageGenerateCommandResult{
		Success:   false,
		OutputDir: outputDir,
	}
	details := map[string]interface{}{
		"output_dir": outputDir,
	}
	if err != nil {
		return result, details, err
	}
	imageDebugf(req, "start prompt_chars=%d output_dir=%q", len([]rune(prompt)), outputDir)
	if prompt == "" {
		imageDebugf(req, "validation_failed error=%q", "prompt is required")
		return result, details, fmt.Errorf("prompt is required")
	}
	if req.N < 0 {
		imageDebugf(req, "validation_failed error=%q n=%d", "n must be zero or positive", req.N)
		return result, details, fmt.Errorf("n must be zero or positive")
	}
	if req.Timeout < 0 {
		imageDebugf(req, "validation_failed error=%q timeout=%s", "timeout must be zero or positive", req.Timeout)
		return result, details, fmt.Errorf("timeout must be zero or positive")
	}
	if req.Config != nil {
		config.SetGlobalConfig(req.Config)
	}

	ctx := context.Background()
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
		imageDebugf(req, "timeout=%s", req.Timeout)
	}
	ctx = toolctx.WithGeneratedImageOutputDir(ctx, outputDir)

	params := imageGenerateCommandParams(req, prompt)
	imageDebugf(req, "params %s", imageDebugParams(params))
	tool := tools.NewOpenAIImageGenerateTool(loadRuntimeToolConfig(req.Config, req.Session))
	imageDebugf(req, "calling tool=%s debug=%t", tool.Name(), req.Debug)
	toolResult, execErr := tool.Execute(ctx, params)
	if execErr != nil {
		imageDebugf(req, "tool_error error=%q", execErr.Error())
		return result, details, execErr
	}
	if toolResult == nil {
		imageDebugf(req, "tool_error error=%q", "image generation returned empty result")
		return result, details, fmt.Errorf("image generation returned empty result")
	}

	metadata := toolResult.MetadataWithOutputKind()
	result.Success = toolResult.Success
	result.Output = strings.TrimSpace(toolResult.Content)
	result.Metadata = metadata
	if len(metadata) > 0 {
		if outputDirValue, ok := metadata["output_dir"].(string); ok && strings.TrimSpace(outputDirValue) != "" {
			result.OutputDir = strings.TrimSpace(outputDirValue)
			details["output_dir"] = result.OutputDir
		}
		result.Images = metadata[llm.MetadataKeyGeneratedImages]
	}
	imageDebugf(
		req,
		"result success=%t provider=%q model=%q generated_count=%v",
		toolResult.Success,
		imageDebugMetadataString(metadata, "provider"),
		imageDebugMetadataString(metadata, "model"),
		imageDebugMetadataDisplay(metadata, "generated_count"),
	)
	for _, savedPath := range imageDebugSavedPaths(result.Images) {
		imageDebugf(req, "saved_path=%q", savedPath)
	}
	if !toolResult.Success {
		err := imageGenerateToolError(toolResult)
		imageDebugf(req, "failed error=%q", err.Error())
		return result, details, err
	}
	return result, details, nil
}

func imageGenerateCommandParams(req imageGenerateCommandRequest, prompt string) map[string]interface{} {
	params := map[string]interface{}{"prompt": prompt}
	if strings.TrimSpace(req.Provider) != "" {
		params["provider"] = strings.TrimSpace(req.Provider)
	}
	if strings.TrimSpace(req.Model) != "" {
		params["model"] = strings.TrimSpace(req.Model)
	}
	if path := strings.TrimSpace(req.Path); path != "" && !strings.EqualFold(path, "auto") {
		params["path"] = path
	}
	if req.N > 0 {
		params["n"] = req.N
	}
	if strings.TrimSpace(req.Size) != "" {
		params["size"] = strings.TrimSpace(req.Size)
	}
	if strings.TrimSpace(req.Quality) != "" {
		params["quality"] = strings.TrimSpace(req.Quality)
	}
	if strings.TrimSpace(req.Background) != "" {
		params["background"] = strings.TrimSpace(req.Background)
	}
	if strings.TrimSpace(req.OutputFormat) != "" {
		params["output_format"] = strings.TrimSpace(req.OutputFormat)
	}
	if req.OutputCompression != nil {
		params["output_compression"] = *req.OutputCompression
	}
	if req.Debug {
		params["debug"] = true
		if req.DebugWriter != nil {
			params["_debug_writer"] = req.DebugWriter
		}
	}
	return params
}

func imagePromptFromFlags(cmd *cobra.Command, args []string) string {
	if prompt := stringFlag(cmd, "prompt"); prompt != "" {
		return prompt
	}
	return strings.TrimSpace(strings.Join(args, " "))
}

func resolveImageCommandOutputDir(raw string) (string, error) {
	outputDir := strings.TrimSpace(raw)
	if outputDir == "" {
		outputDir = defaultImageCommandOutputDir
	}
	cleaned := filepath.Clean(outputDir)
	if filepath.IsAbs(cleaned) {
		return cleaned, nil
	}
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve output dir: %w", err)
	}
	return abs, nil
}

func imageGenerateToolError(result *toolkit.ToolResult) error {
	if result == nil {
		return fmt.Errorf("image generation failed")
	}
	if result.Error != nil {
		return result.Error
	}
	if message := strings.TrimSpace(result.Content); message != "" {
		return fmt.Errorf("%s", message)
	}
	return fmt.Errorf("image generation failed")
}

func renderImageGenerateCommandResult(result *imageGenerateCommandResult, outputOptions structuredOutputOptions) {
	if result == nil {
		return
	}
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("image", outputOptions.Envelope, result)
		return
	}
	if strings.TrimSpace(result.Output) != "" {
		fmt.Fprintln(os.Stdout, result.Output)
	}
	if strings.TrimSpace(result.OutputDir) != "" {
		fmt.Fprintf(os.Stdout, "Output dir: %s\n", result.OutputDir)
	}
}

func intFlag(cmd *cobra.Command, name string) int {
	if cmd == nil {
		return 0
	}
	value, _ := cmd.Flags().GetInt(name)
	return value
}

func flagChanged(cmd *cobra.Command, name string) bool {
	if cmd == nil {
		return false
	}
	flag := cmd.Flags().Lookup(name)
	return flag != nil && flag.Changed
}

func imageDebugf(req imageGenerateCommandRequest, format string, args ...interface{}) {
	if !req.Debug {
		return
	}
	writer := req.DebugWriter
	if writer == nil {
		writer = os.Stderr
	}
	fmt.Fprintf(writer, "[image-debug] "+format+"\n", args...)
}

func imageDebugParams(params map[string]interface{}) string {
	if len(params) == 0 {
		return ""
	}
	orderedKeys := []string{"provider", "model", "path", "n", "size", "quality", "background", "output_format", "output_compression", "debug"}
	parts := make([]string, 0, len(orderedKeys)+1)
	if prompt, ok := params["prompt"].(string); ok {
		parts = append(parts, fmt.Sprintf("prompt_chars=%d", len([]rune(prompt))))
	}
	for _, key := range orderedKeys {
		value, ok := params[key]
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%q", key, fmt.Sprint(value)))
	}
	return strings.Join(parts, " ")
}

func imageDebugSavedPaths(images interface{}) []string {
	switch typed := images.(type) {
	case []map[string]interface{}:
		return imageDebugSavedPathsFromMaps(typed)
	case []interface{}:
		paths := make([]string, 0, len(typed))
		for _, item := range typed {
			entry, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if savedPath := strings.TrimSpace(fmt.Sprint(entry["saved_path"])); savedPath != "" && savedPath != "<nil>" {
				paths = append(paths, savedPath)
			}
		}
		return paths
	default:
		return nil
	}
}

func imageDebugSavedPathsFromMaps(images []map[string]interface{}) []string {
	paths := make([]string, 0, len(images))
	for _, entry := range images {
		if savedPath := strings.TrimSpace(fmt.Sprint(entry["saved_path"])); savedPath != "" && savedPath != "<nil>" {
			paths = append(paths, savedPath)
		}
	}
	return paths
}

func imageDebugMetadataString(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "<nil>" {
		return ""
	}
	return text
}

func imageDebugMetadataDisplay(metadata map[string]interface{}, key string) string {
	value := imageDebugMetadataString(metadata, key)
	if value == "" {
		return "<none>"
	}
	return value
}
