package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	httpclient "github.com/wwsheng009/ai-agent-runtime/internal/pkg/httpclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/spf13/cobra"
)

type contextCommandResult struct {
	Mode               string `json:"mode"`
	Provider           string `json:"provider"`
	Protocol           string `json:"protocol"`
	Model              string `json:"model"`
	ConfiguredMaxLimit int    `json:"configured_max_limit,omitempty"`
	TimeoutSeconds     int    `json:"timeout_seconds"`
	Retries            int    `json:"retries"`
	MaxSupportedTokens int    `json:"max_supported_tokens"`
	RecommendedTokens  int    `json:"recommended_tokens"`
	LastSuccessTokens  int    `json:"last_success_tokens,omitempty"`
	FirstFailureTokens int    `json:"first_failure_tokens,omitempty"`
	ReachedTestLimit   bool   `json:"reached_test_limit,omitempty"`
	ActualOutputTokens int    `json:"actual_output_tokens,omitempty"`
	Error              string `json:"error,omitempty"`
}

type contextCommandOptions struct {
	Start         int
	End           int
	Step          int
	MaxOutputOnly bool
	TimeoutSec    int
	Retries       int
}

// HandleContext 处理 context 测试命令
func HandleContext(cmd *cobra.Command, cfg *config.Config) {
	providerFlag, _ := cmd.Flags().GetString("provider")
	modelFlag, _ := cmd.Flags().GetString("model")
	startFlag, _ := cmd.Flags().GetInt("start")
	endFlag, _ := cmd.Flags().GetInt("end")
	stepFlag, _ := cmd.Flags().GetInt("step")
	maxOutputOnly, _ := cmd.Flags().GetBool("max-output-only")
	timeoutFlag, _ := cmd.Flags().GetInt("timeout")
	retryFlag, _ := cmd.Flags().GetInt("retries")
	outputOptions, err := resolveStructuredOutputOptions(cmd, "pretty", "pretty", "text", "json")
	if err != nil {
		exitCommandError("context", "json", err, nil)
	}
	executeStructuredCommand("context", outputOptions, func() (*contextCommandResult, map[string]interface{}, error) {
		return runContextCommand(cfg, providerFlag, modelFlag, contextCommandOptions{
			Start:         startFlag,
			End:           endFlag,
			Step:          stepFlag,
			MaxOutputOnly: maxOutputOnly,
			TimeoutSec:    timeoutFlag,
			Retries:       retryFlag,
		}, outputOptions.Format == "pretty")
	}, nil, func(report *contextCommandResult) {
		renderContextReport(report, outputOptions.Format, outputOptions.Envelope)
	})
}

func runContextCommand(cfg *config.Config, providerFlag, modelFlag string, opts contextCommandOptions, verbose bool) (*contextCommandResult, map[string]interface{}, error) {
	resolved, details, err := resolveProviderExecutionContext(cfg, providerFlag, modelFlag)
	if err != nil {
		return nil, details, err
	}

	providerName := resolved.ProviderName
	provider := resolved.Provider
	modelName := resolved.Model

	if resolved.ModelMapped {
		if verbose {
			fmt.Printf("模型映射: %s -> %s\n", resolved.RequestedModel, resolved.Model)
		}
	}

	if verbose {
		fmt.Println("================================================================================")
		fmt.Println("                    上下文窗口和最大输出测试")
		fmt.Println("================================================================================")
		fmt.Printf("Provider:       %s\n", providerName)
		fmt.Printf("Model:          %s\n", modelName)
		fmt.Printf("Type:           %s\n", provider.GetProtocol())
		fmt.Printf("Max Tokens:     %d\n", provider.MaxTokensLimit)
		fmt.Printf("Timeout:        %d seconds\n", opts.TimeoutSec)
		fmt.Printf("Retries:        %d\n", opts.Retries)
		fmt.Println()
	}

	var report *contextCommandResult
	if opts.MaxOutputOnly {
		report = testMaxOutputTokens(cfg, provider, resolved.Adapter, modelName, opts.Start, opts.End, opts.Step, opts.TimeoutSec, opts.Retries, verbose)
	} else {
		report = testContextWindow(cfg, provider, resolved.Adapter, modelName, opts.Start, opts.End, opts.Step, opts.TimeoutSec, opts.Retries, verbose)
	}
	if report != nil {
		report.Provider = providerName
	}
	return report, details, nil
}

// testContextWindow 测试上下文窗口（跃进式测试）
// 策略：从起点开始，按倍数跃进（2x, 4x, 8x...），失败后二分精确定位
func testContextWindow(cfg *config.Config, provider config.Provider, llmAdapter adapter.ProtocolAdapter, modelName string, start, end, step, timeout, retries int, verbose bool) *contextCommandResult {
	report := &contextCommandResult{
		Mode:               "context_window",
		Protocol:           provider.GetProtocol(),
		Model:              modelName,
		ConfiguredMaxLimit: provider.MaxTokensLimit,
		TimeoutSeconds:     timeout,
		Retries:            retries,
	}
	if verbose {
		fmt.Println("测试模式: 上下文窗口 (Context Window)")
		fmt.Println("说明: 跃进式测试，快速定位最大输入 tokens")
		fmt.Println("================================================================================")
		fmt.Println()
	}

	// 如果未指定结束值，使用 provider 的限制
	maxLimit := end
	if maxLimit == 0 {
		maxLimit = provider.MaxTokensLimit
		if maxLimit == 0 {
			maxLimit = 128000 // 默认最大值
		}
	}

	// 起始值
	currentInput := start
	if currentInput == 0 {
		currentInput = 1000
	}

	// 阶段1：跃进测试（倍增）
	if verbose {
		fmt.Println("【阶段1】跃进测试（快速逼近边界）")
		fmt.Println()
	}

	lastSuccess := 0
	firstFail := 0

	for currentInput <= maxLimit {
		if verbose {
			fmt.Printf("[跃进 %d tokens] ", currentInput)
		}

		testText := generateTestText(currentInput)
		requestBody := buildContextWindowRequestBody(llmAdapter, modelName, testText)

		_, err := sendTestRequest(cfg, provider, llmAdapter, requestBody, timeout, retries)
		if err != nil {
			if verbose {
				fmt.Printf("✗ 失败\n")
			}
			firstFail = currentInput
			report.Error = err.Error()
			break
		} else {
			if verbose {
				fmt.Printf("✓ 成功\n")
			}
			lastSuccess = currentInput
		}

		// 倍增跃进
		currentInput *= 2
	}

	// 如果全部成功，说明可以处理到 maxLimit
	if firstFail == 0 {
		report.MaxSupportedTokens = lastSuccess
		report.RecommendedTokens = int(float64(lastSuccess) * 0.9)
		report.LastSuccessTokens = lastSuccess
		report.ReachedTestLimit = true
		return report
	}

	// 阶段2：二分精确定位
	if verbose {
		fmt.Println()
		fmt.Println("【阶段2】二分精确测试（定位边界）")
		fmt.Printf("搜索范围: %d - %d tokens\n", lastSuccess, firstFail)
		fmt.Println()
	}

	var maxError error
	preciseMax := binarySearchMaxInputWithProgress(cfg, provider, llmAdapter, modelName, lastSuccess, firstFail, timeout, retries, &maxError, verbose)
	if preciseMax <= 0 {
		preciseMax = lastSuccess
	}
	report.MaxSupportedTokens = preciseMax
	report.RecommendedTokens = int(float64(preciseMax) * 0.9)
	report.LastSuccessTokens = lastSuccess
	report.FirstFailureTokens = firstFail
	if maxError != nil {
		report.Error = maxError.Error()
	}
	return report
}

// testMaxOutputTokens 测试最大输出 tokens
func testMaxOutputTokens(cfg *config.Config, provider config.Provider, llmAdapter adapter.ProtocolAdapter, modelName string, start, end, step, timeout, retries int, verbose bool) *contextCommandResult {
	report := &contextCommandResult{
		Mode:               "max_output",
		Protocol:           provider.GetProtocol(),
		Model:              modelName,
		ConfiguredMaxLimit: provider.MaxTokensLimit,
		TimeoutSeconds:     timeout,
		Retries:            retries,
	}
	if verbose {
		fmt.Println("测试模式: 最大输出 (Max Output Tokens)")
		fmt.Println("说明: 测试模型能生成的最大输出 tokens")
		fmt.Println("================================================================================")
		fmt.Println()
	}

	// 如果配置有限制，使用配置的限制
	configuredLimit := provider.MaxTokensLimit
	if configuredLimit > 0 && end > configuredLimit {
		end = configuredLimit
		if verbose {
			fmt.Printf("注意: 配置文件限制最大 tokens 为 %d\n", configuredLimit)
			fmt.Println()
		}
	}

	maxOutputTokens := 0
	var maxError error

	currentMaxTokens := start
	if currentMaxTokens == 0 {
		currentMaxTokens = 1000
	}

	for currentMaxTokens <= end {
		if verbose {
			fmt.Printf("[测试 %d output tokens] ", currentMaxTokens)
		}

		requestBody := buildMaxOutputRequestBody(llmAdapter, modelName, currentMaxTokens)

		// 发送请求
		_, actualTokens, err := sendOutputTestRequest(cfg, provider, llmAdapter, requestBody, timeout, retries)
		if err != nil {
			if verbose {
				fmt.Printf("✗ 失败: %v\n", err)
			}
			report.Error = err.Error()

			if maxOutputTokens == 0 && currentMaxTokens > start {
				maxOutputTokens = currentMaxTokens - step
				maxError = err
			}

			// 二分查找
			if currentMaxTokens > start {
				preciseMax := binarySearchMaxOutput(cfg, provider, llmAdapter, modelName, currentMaxTokens-step+1, currentMaxTokens-1, timeout, retries)
				if preciseMax > 0 {
					maxOutputTokens = preciseMax
					break
				}
			}
			break
		} else {
			if verbose {
				fmt.Printf("✓ 成功 (实际生成: %d tokens)\n", actualTokens)
			}
			maxOutputTokens = currentMaxTokens
			report.ActualOutputTokens = actualTokens
		}

		currentMaxTokens += step
	}

	report.MaxSupportedTokens = maxOutputTokens
	report.RecommendedTokens = int(float64(maxOutputTokens) * 0.9)
	if maxError != nil && report.Error == "" {
		report.Error = maxError.Error()
	}
	return report
}

// sendTestRequest 发送测试请求
func sendTestRequest(cfg *config.Config, provider config.Provider, llmAdapter adapter.ProtocolAdapter, requestBody map[string]interface{}, timeout, retries int) (bool, error) {
	_, err := sendContextRequest(cfg, provider, llmAdapter, requestBody, timeout, retries)
	if err != nil {
		return false, err
	}
	return true, nil
}

// sendOutputTestRequest 发送输出测试请求
func sendOutputTestRequest(cfg *config.Config, provider config.Provider, llmAdapter adapter.ProtocolAdapter, requestBody map[string]interface{}, timeout, retries int) (bool, int, error) {
	body, err := sendContextRequest(cfg, provider, llmAdapter, requestBody, timeout, retries)
	if err != nil {
		return false, 0, err
	}
	return true, extractCompletionTokensFromResponseBody(body), nil
}

func sendContextRequest(cfg *config.Config, provider config.Provider, llmAdapter adapter.ProtocolAdapter, requestBody map[string]interface{}, timeout, retries int) ([]byte, error) {
	// 从 requestBody 中提取 model 用于 URL 占位符替换
	modelName := ""
	if m, ok := requestBody["model"].(string); ok {
		modelName = m
	}

	// 构造完整 URL
	fullURL := buildProviderURL(provider, llmAdapter.GetAPIPath(), modelName)

	// 序列化请求体
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	client := httpclient.GetHTTPClientWithProvider(cfg, &provider)
	headers := llmAdapter.BuildHeaders(adapter.AdapterConfig{
		Type:    provider.GetProtocol(),
		APIKey:  provider.GetAPIKey(),
		Timeout: time.Duration(timeout) * time.Second,
	})

	attempts := retries + 1
	if attempts <= 0 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
		req, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewReader(bodyBytes))
		if err != nil {
			cancel()
			return nil, fmt.Errorf("create request: %w", err)
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			lastErr = fmt.Errorf("request failed: %w", err)
		} else {
			body, _ := readAllBody(resp.Body)
			resp.Body.Close()
			cancel()

			if resp.StatusCode == http.StatusOK {
				return body, nil
			}

			var errResp map[string]interface{}
			if json.Unmarshal(body, &errResp) == nil {
				if errorInfo, ok := errResp["error"].(map[string]interface{}); ok {
					lastErr = fmt.Errorf("%v", errorInfo["message"])
				} else {
					lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
				}
			} else {
				lastErr = fmt.Errorf("status %d", resp.StatusCode)
			}
		}

		if attempt < attempts {
			time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
		}
	}

	return nil, lastErr
}

// generateTestText 生成指定 token 数的测试文本
// 注意: 这只是估算，实际 token 数可能因模型而异
func generateTestText(tokens int) string {
	// 平均每个单词约 1.3 tokens，每个字符约 0.3 tokens
	const avgCharsPerToken = 4
	charCount := tokens * avgCharsPerToken

	// 生成重复的句子
	sentence := "This is a test sentence to measure token count. "
	repeat := charCount / len(sentence)
	var builder strings.Builder
	for i := 0; i < repeat; i++ {
		builder.WriteString(sentence)
	}
	return builder.String()
}

// buildContextWindowRequestBody 构建上下文窗口测试请求体
func buildContextWindowRequestBody(llmAdapter adapter.ProtocolAdapter, modelName, testText string) map[string]interface{} {
	return llmAdapter.BuildRequest(adapter.RequestConfig{
		Model:       modelName,
		Messages:    []map[string]interface{}{{"role": "user", "content": testText}},
		Stream:      false,
		MaxTokens:   10,
		Temperature: 0.1,
	})
}

func buildMaxOutputRequestBody(llmAdapter adapter.ProtocolAdapter, modelName string, maxTokens int) map[string]interface{} {
	return llmAdapter.BuildRequest(adapter.RequestConfig{
		Model: modelName,
		Messages: []map[string]interface{}{
			{"role": "system", "content": "You are a helpful assistant. Please generate a very long detailed response."},
			{"role": "user", "content": "Write a very detailed story about a magical adventure."},
		},
		Stream:      false,
		MaxTokens:   maxTokens,
		Temperature: 0.7,
	})
}

// binarySearchMaxInputWithProgress 二分查找最大输入（带进度显示）
func binarySearchMaxInputWithProgress(cfg *config.Config, provider config.Provider, llmAdapter adapter.ProtocolAdapter, modelName string, low, high, timeout, retries int, maxError *error, verbose bool) int {
	maxInput := 0

	for low <= high {
		mid := (low + high) / 2
		if verbose {
			fmt.Printf("[二分 %d tokens] ", mid)
		}

		testText := generateTestText(mid)
		requestBody := buildContextWindowRequestBody(llmAdapter, modelName, testText)

		success, err := sendTestRequest(cfg, provider, llmAdapter, requestBody, timeout, retries)
		if success {
			if verbose {
				fmt.Printf("✓ 成功\n")
			}
			maxInput = mid
			low = mid + 1
		} else {
			if verbose {
				fmt.Printf("✗ 失败\n")
			}
			*maxError = err
			high = mid - 1
		}
	}

	return maxInput
}

// binarySearchMaxOutput 二分查找最大输出
func binarySearchMaxOutput(cfg *config.Config, provider config.Provider, llmAdapter adapter.ProtocolAdapter, modelName string, low, high, timeout, retries int) int {
	maxOutput := 0

	for low <= high {
		mid := (low + high) / 2
		requestBody := buildMaxOutputRequestBody(llmAdapter, modelName, mid)
		success, _, _ := sendOutputTestRequest(cfg, provider, llmAdapter, requestBody, timeout, retries)
		if success {
			maxOutput = mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}

	return maxOutput
}

// buildProviderURL 构建 provider URL
// 逻辑：
//   - 如果请求协议与上游 provider 类型一致：base_url + api_path + request_path
//   - 如果协议不一致：使用 forward_url 或 path_mappings 进行路径转换
func buildProviderURL(provider config.Provider, requestPath string, model ...string) string {
	if len(model) > 0 {
		return config.BuildUpstreamURLWithPath(provider, requestPath, "", model[0])
	}
	return config.BuildUpstreamURLWithPath(provider, requestPath, "", "")
}

// readAllBody 读取响应体
func readAllBody(r io.Reader) ([]byte, error) {
	return io.ReadAll(r)
}

func extractCompletionTokensFromResponseBody(body []byte) int {
	usage := extractUsageFromAnyResponseBody(body)
	if len(usage) == 0 {
		return 0
	}
	if completionTokens, ok := usage["completion_tokens"].(float64); ok {
		return int(completionTokens)
	}
	if outputTokens, ok := usage["output_tokens"].(float64); ok {
		return int(outputTokens)
	}
	if completionTokens, ok := usage["completion_tokens"].(int); ok {
		return completionTokens
	}
	if outputTokens, ok := usage["output_tokens"].(int); ok {
		return outputTokens
	}
	return 0
}

func renderContextReport(report *contextCommandResult, outputFormat string, jsonEnvelope bool) {
	if report == nil {
		return
	}

	switch outputFormat {
	case "json":
		printCommandJSONOutput("context", jsonEnvelope, report)
	case "text":
		fmt.Printf("mode=%s\n", report.Mode)
		fmt.Printf("provider=%s\n", report.Provider)
		fmt.Printf("protocol=%s\n", report.Protocol)
		fmt.Printf("model=%s\n", report.Model)
		fmt.Printf("max_supported_tokens=%d\n", report.MaxSupportedTokens)
		fmt.Printf("recommended_tokens=%d\n", report.RecommendedTokens)
		if report.ActualOutputTokens > 0 {
			fmt.Printf("actual_output_tokens=%d\n", report.ActualOutputTokens)
		}
		if report.Error != "" {
			fmt.Printf("error=%s\n", report.Error)
		}
	default:
		fmt.Println("================================================================================")
		fmt.Println("测试结果")
		fmt.Println("================================================================================")
		switch report.Mode {
		case "max_output":
			fmt.Printf("最大输出 tokens: %d tokens\n", report.MaxSupportedTokens)
		default:
			if report.ReachedTestLimit {
				fmt.Printf("最大上下文窗口: >= %d tokens（达到测试上限）\n", report.MaxSupportedTokens)
			} else {
				fmt.Printf("最大上下文窗口: %d tokens\n", report.MaxSupportedTokens)
			}
		}
		if report.ActualOutputTokens > 0 {
			fmt.Printf("实际生成 tokens: %d tokens\n", report.ActualOutputTokens)
		}
		if report.Error != "" {
			fmt.Printf("错误信息: %s\n", report.Error)
		}
		fmt.Printf("测试建议: 建议最大输入设置为 %d tokens (保留10%%安全余量)\n", report.RecommendedTokens)
		fmt.Println()
	}
}
