package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	httpclient "github.com/wwsheng009/ai-agent-runtime/internal/pkg/httpclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/spf13/cobra"
)

type testCommandOptions struct {
	Path        string
	Message     string
	MaxTokens   int
	Temperature float64
	Stream      bool
	TimeoutSec  int
}

type testCommandResult struct {
	ProviderName   string
	Provider       config.Provider
	Adapter        adapter.ProtocolAdapter
	Model          string
	URL            string
	Stream         bool
	RequestBody    []byte
	RequestHeaders map[string]string
	ResponseBody   []byte
	StatusCode     int
	Duration       time.Duration
}

// HandleTest 处理 test 命令
func HandleTest(cmd *cobra.Command, cfg *config.Config) {
	providerFlag, _ := cmd.Flags().GetString("provider")
	modelFlag, _ := cmd.Flags().GetString("model")
	messageFlag, _ := cmd.Flags().GetString("message")
	pathFlag, _ := cmd.Flags().GetString("path")
	maxTokens, _ := cmd.Flags().GetInt("max-tokens")
	temperature, _ := cmd.Flags().GetFloat64("temperature")
	streamFlag, _ := cmd.Flags().GetBool("stream")
	formatFlag, _ := cmd.Flags().GetString("format")
	timeoutFlag, _ := cmd.Flags().GetInt("timeout")
	outputOptions, err := resolveStructuredOutputOptions(cmd, formatFlag, "pretty", "json", "raw", "text")
	if err != nil {
		exitCommandError("test", "json", err, nil)
	}
	saveDir, _ := cmd.Flags().GetString("save")
	executeCommand("test", outputOptions, func() (*testCommandResult, map[string]interface{}, error) {
		return runTestCommand(cfg, providerFlag, modelFlag, testCommandOptions{
			Path:        pathFlag,
			Message:     messageFlag,
			MaxTokens:   maxTokens,
			Temperature: temperature,
			Stream:      streamFlag,
			TimeoutSec:  timeoutFlag,
		})
	}, func(result *testCommandResult, outputOptions structuredOutputOptions) {
		renderTestCommandResult(result, outputOptions, saveDir)
	})
}

func runTestCommand(cfg *config.Config, providerFlag, modelFlag string, opts testCommandOptions) (*testCommandResult, map[string]interface{}, error) {
	ctx, details, err := resolveProviderExecutionContext(cfg, providerFlag, modelFlag)
	if err != nil {
		return nil, details, err
	}

	apiPath := strings.TrimSpace(opts.Path)
	if apiPath == "" {
		apiPath = ctx.Adapter.GetAPIPath()
	}

	fullURL := buildProviderURL(ctx.Provider, apiPath, ctx.Model)
	requestConfig := adapter.RequestConfig{
		Model:       ctx.Model,
		Messages:    []map[string]interface{}{{"role": "user", "content": opts.Message}},
		Stream:      opts.Stream,
		MaxTokens:   opts.MaxTokens,
		Temperature: opts.Temperature,
		Timeout:     time.Duration(opts.TimeoutSec) * time.Second,
	}

	requestBody := ctx.Adapter.BuildRequest(requestConfig)
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, details, fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest("POST", fullURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, details, fmt.Errorf("failed to create request: %w", err)
	}

	adapterConfig := adapter.AdapterConfig{
		Type:    ctx.Provider.GetProtocol(),
		APIKey:  ctx.Provider.GetAPIKey(),
		Timeout: time.Duration(opts.TimeoutSec) * time.Second,
	}
	headers := ctx.Adapter.BuildHeaders(adapterConfig)
	requestHeaders := make(map[string]string, len(headers))
	for key, value := range headers {
		req.Header.Set(key, value)
		requestHeaders[key] = value
	}

	client := httpclient.GetHTTPClientWithProvider(cfg, &ctx.Provider)
	startTime := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, details, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, details, fmt.Errorf("failed to read response body: %w", err)
	}

	return &testCommandResult{
		ProviderName:   ctx.ProviderName,
		Provider:       ctx.Provider,
		Adapter:        ctx.Adapter,
		Model:          ctx.Model,
		URL:            fullURL,
		Stream:         opts.Stream,
		RequestBody:    bodyBytes,
		RequestHeaders: requestHeaders,
		ResponseBody:   responseBody,
		StatusCode:     resp.StatusCode,
		Duration:       time.Since(startTime),
	}, details, nil
}

func shouldPrintTestPreamble(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json", "raw", "text":
		return false
	default:
		return true
	}
}

type testResponsePayload struct {
	Provider   string                 `json:"provider,omitempty"`
	Protocol   string                 `json:"protocol,omitempty"`
	Model      string                 `json:"model,omitempty"`
	URL        string                 `json:"url,omitempty"`
	Stream     bool                   `json:"stream"`
	StatusCode int                    `json:"status_code"`
	DurationMs int64                  `json:"duration_ms"`
	Response   string                 `json:"response,omitempty"`
	Usage      map[string]interface{} `json:"usage,omitempty"`
	Raw        json.RawMessage        `json:"raw,omitempty"`
}

func renderTestResponse(formatFlag string, jsonEnvelope bool, providerName, protocol, modelName, fullURL string, stream bool, responseBody []byte, statusCode int, duration time.Duration) {
	switch formatFlag {
	case "json":
		payload := testResponsePayload{
			Provider:   providerName,
			Protocol:   protocol,
			Model:      modelName,
			URL:        fullURL,
			Stream:     stream,
			StatusCode: statusCode,
			DurationMs: duration.Milliseconds(),
			Response:   extractSimpleResponseText(responseBody),
			Usage:      extractUsageFromResponseBody(responseBody),
		}
		if json.Valid(responseBody) {
			payload.Raw = json.RawMessage(responseBody)
		}
		printCommandJSONOutput("test", jsonEnvelope, payload)
	case "raw":
		fmt.Println(string(responseBody))
	case "text":
		if content := extractSimpleResponseText(responseBody); content != "" {
			fmt.Println(content)
		} else {
			fmt.Println(string(responseBody))
		}
	default: // pretty
		// 尝试从响应中提取简短信息显示
		displaySimpleResponse(responseBody)
	}
}

func renderTestCommandResult(result *testCommandResult, outputOptions structuredOutputOptions, saveDir string) {
	if result == nil {
		return
	}

	if shouldPrintTestPreamble(outputOptions.Format) {
		fmt.Printf("Testing provider: %s\n", result.ProviderName)
		fmt.Printf("  Model:    %s\n", result.Model)
		fmt.Printf("  Protocol: %s\n", result.Provider.GetProtocol())
		fmt.Printf("  URL:      %s\n", result.URL)
		fmt.Printf("  Stream:   %v\n", result.Stream)
		fmt.Printf("  Request:  %s\n", string(result.RequestBody))
		fmt.Println()
		fmt.Printf("Response Status: %d (%s)\n", result.StatusCode, http.StatusText(result.StatusCode))
		fmt.Printf("Duration: %v\n", result.Duration)
		fmt.Println()
	}

	renderTestResponse(outputOptions.Format, outputOptions.Envelope, result.ProviderName, result.Provider.GetProtocol(), result.Model, result.URL, result.Stream, result.ResponseBody, result.StatusCode, result.Duration)

	if saveDir != "" {
		if err := saveTestData(saveDir, result.ProviderName, result.Provider.GetProtocol(), result.Model, result.URL, result.RequestHeaders, result.RequestBody, result.ResponseBody, result.StatusCode, result.Duration); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to save test data: %v\n", err)
		} else if shouldPrintTestPreamble(outputOptions.Format) {
			fmt.Printf("\nTest data saved to: %s\n", saveDir)
		}
	} else if shouldPrintTestPreamble(outputOptions.Format) {
		fmt.Println("(No save directory specified)")
	}
}

// displaySimpleResponse 显示简化的响应信息
func displaySimpleResponse(responseBody []byte) {
	var result map[string]interface{}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		// JSON 解析失败，直接显示原始内容
		fmt.Println("Response:")
		fmt.Println(string(responseBody))
		return
	}

	// 显示错误信息（如果有）
	if errorInfo, ok := result["error"]; ok {
		fmt.Printf("Error: %v\n", errorInfo)
		return
	}

	// 显示内容
	fmt.Println("Response:")

	// OpenAI 格式
	if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if msg, ok := choice["message"].(map[string]interface{}); ok {
				if content, ok := msg["content"].(string); ok && content != "" {
					fmt.Println(content)
				} else if reasoning, ok := msg["reasoning_content"].(string); ok {
					fmt.Printf("--- Thinking ---\n%s\n--- End Thinking ---\n", reasoning)
				}
			}
		}
	}

	// Anthropic 格式
	if content, ok := result["content"].([]interface{}); ok && len(content) > 0 {
		for _, c := range content {
			if item, ok := c.(map[string]interface{}); ok {
				if item["type"] == "text" {
					if text, ok := item["text"].(string); ok {
						fmt.Println(text)
						break
					}
				}
			}
		}
	}

	// 显示 usage
	if usage, ok := result["usage"].(map[string]interface{}); ok {
		fmt.Println()
		fmt.Println("Usage:")
		fmt.Printf("  Prompt tokens:     %v\n", usage["prompt_tokens"])
		fmt.Printf("  Completion tokens: %v\n", usage["completion_tokens"])
		fmt.Printf("  Total tokens:      %v\n", usage["total_tokens"])
	}
}

func extractSimpleResponseText(responseBody []byte) string {
	var result map[string]interface{}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return strings.TrimSpace(string(responseBody))
	}

	if errorInfo, ok := result["error"]; ok && errorInfo != nil {
		return strings.TrimSpace(fmt.Sprintf("%v", errorInfo))
	}

	if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if msg, ok := choice["message"].(map[string]interface{}); ok {
				if content, ok := msg["content"].(string); ok && content != "" {
					return strings.TrimSpace(content)
				}
				if reasoning, ok := msg["reasoning_content"].(string); ok && reasoning != "" {
					return strings.TrimSpace(reasoning)
				}
			}
		}
	}

	if content, ok := result["content"].([]interface{}); ok && len(content) > 0 {
		for _, c := range content {
			if item, ok := c.(map[string]interface{}); ok && item["type"] == "text" {
				if text, ok := item["text"].(string); ok && text != "" {
					return strings.TrimSpace(text)
				}
			}
		}
	}

	if output, ok := result["output"].([]interface{}); ok && len(output) > 0 {
		for _, item := range output {
			itemMap, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			contentItems, ok := itemMap["content"].([]interface{})
			if !ok {
				continue
			}
			for _, contentItem := range contentItems {
				contentMap, ok := contentItem.(map[string]interface{})
				if !ok {
					continue
				}
				if text, ok := contentMap["text"].(string); ok && text != "" {
					return strings.TrimSpace(text)
				}
			}
		}
	}

	return ""
}

// saveTestData 保存测试数据到文件（保存原始数据）
func saveTestData(saveDir, providerName, protocolType, modelName, fullURL string, requestHeaders map[string]string, requestBody, responseBody []byte, statusCode int, duration time.Duration) error {
	// 创建目录
	if err := os.MkdirAll(saveDir, 0755); err != nil {
		return fmt.Errorf("创建保存目录失败: %w", err)
	}

	// 生成文件名
	timestamp := time.Now().Format("20060102_150405")
	safeModelName := strings.ReplaceAll(modelName, "/", "_")
	filename := fmt.Sprintf("%s_%s_%s_%s.json", providerName, protocolType, safeModelName, timestamp)
	filePath := filepath.Join(saveDir, filename)

	// 构建测试数据结构（保存原始数据）
	testData := map[string]interface{}{
		"metadata": map[string]interface{}{
			"timestamp":   timestamp,
			"provider":    providerName,
			"protocol":    protocolType,
			"model":       modelName,
			"url":         fullURL,
			"status_code": statusCode,
			"duration_ms": duration.Milliseconds(),
		},
		"request": map[string]interface{}{
			"headers": requestHeaders,
			"body":    json.RawMessage(requestBody),
		},
		"response": map[string]interface{}{
			"body": json.RawMessage(responseBody),
		},
	}

	// 序列化
	data, err := json.MarshalIndent(testData, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化测试数据失败: %w", err)
	}

	// 写入文件
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}

	return nil
}
