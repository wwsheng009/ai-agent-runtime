package commands

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	httpclient "github.com/wwsheng009/ai-agent-runtime/internal/pkg/httpclient"
)

// PipeSession 管道会话状态
type PipeSession struct {
	ProviderName string
	Provider     config.Provider
	Adapter      adapter.ProtocolAdapter
	Model        string
	BaseURL      string
	HTTPClient   *http.Client
	Prompt       string
	BufferSize   int
	MaxTokens    int
	OutputFormat string
	JSONEnvelope bool
}

type pipeCommandOptions struct {
	Prompt       string
	ProviderFlag string
	ModelFlag    string
	BufferSize   int
	MaxTokens    int
	OutputFormat string
	JSONEnvelope bool
}

type pipeCommandResult struct {
	Response   string
	StatusCode int
	DurationMs int64
	Usage      map[string]interface{}
	Raw        json.RawMessage
}

// HandlePipe 处理 pipe 命令
func HandlePipe(cmd *cobra.Command, cfg *config.Config) {
	prompt, _ := cmd.Flags().GetString("prompt")
	providerFlag, _ := cmd.Flags().GetString("provider")
	modelFlag, _ := cmd.Flags().GetString("model")
	bufferSize, _ := cmd.Flags().GetInt("buffer")
	maxTokens, _ := cmd.Flags().GetInt("max-tokens")
	streamFlag, _ := cmd.Flags().GetBool("stream")
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("pipe", "json", err, nil)
	}
	if streamFlag && outputOptions.Format == "json" {
		exitCommandError("pipe", outputOptions.Format, fmt.Errorf("pipe --stream 暂不支持 --output json"), nil)
	}

	executeCommand("pipe", outputOptions, func() (*PipeSession, map[string]interface{}, error) {
		return runPipeCommand(cfg, pipeCommandOptions{
			Prompt:       prompt,
			ProviderFlag: providerFlag,
			ModelFlag:    modelFlag,
			BufferSize:   bufferSize,
			MaxTokens:    maxTokens,
			OutputFormat: outputOptions.Format,
			JSONEnvelope: outputOptions.Envelope,
		})
	}, func(session *PipeSession, outputOptions structuredOutputOptions) {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

		go func() {
			<-sigChan
			fmt.Fprintf(os.Stderr, "\n收到中断信号，正在退出...\n")
			os.Exit(0)
		}()

		if streamFlag {
			runStreamPipe(session)
		} else {
			runBufferedPipe(session)
		}
	})
}

func runPipeCommand(cfg *config.Config, opts pipeCommandOptions) (*PipeSession, map[string]interface{}, error) {
	details := map[string]interface{}{}

	stat, err := os.Stdin.Stat()
	if err != nil {
		return nil, details, fmt.Errorf("读取 stdin 状态失败: %w", err)
	}
	isPiped := (stat.Mode() & os.ModeCharDevice) == 0
	if !isPiped && strings.TrimSpace(opts.Prompt) == "" {
		return nil, details, fmt.Errorf("需要通过管道提供输入或使用 -p 指定提示词")
	}

	prompt := strings.TrimSpace(opts.Prompt)
	if prompt == "" {
		prompt = "请分析以下内容："
	}

	resolved, providerDetails, err := resolveProviderExecutionContext(cfg, opts.ProviderFlag, opts.ModelFlag)
	if err != nil {
		return nil, providerDetails, err
	}
	for key, value := range providerDetails {
		details[key] = value
	}
	details["prompt"] = prompt

	baseURL := buildProviderURL(resolved.Provider, resolved.Adapter.GetAPIPath(), resolved.Model)
	return &PipeSession{
		ProviderName: resolved.ProviderName,
		Provider:     resolved.Provider,
		Adapter:      resolved.Adapter,
		Model:        resolved.Model,
		BaseURL:      baseURL,
		HTTPClient:   httpclient.GetHTTPClientWithProvider(cfg, &resolved.Provider),
		Prompt:       prompt,
		BufferSize:   opts.BufferSize,
		MaxTokens:    opts.MaxTokens,
		OutputFormat: opts.OutputFormat,
		JSONEnvelope: opts.JSONEnvelope,
	}, details, nil
}

// runBufferedPipe 缓冲模式：读取所有输入后一次性发送
func runBufferedPipe(session *PipeSession) {
	// 读取所有 stdin 内容
	content, err := io.ReadAll(os.Stdin)
	if err != nil {
		exitCommandError("pipe", session.OutputFormat, fmt.Errorf("读取输入失败: %w", err), nil)
	}

	// 组合提示词和内容
	fullMessage := session.Prompt
	if len(content) > 0 {
		fullMessage = session.Prompt + "\n\n" + string(content)
	}

	// 发送请求
	result, err := sendPipeRequest(session, fullMessage, false)
	if err != nil {
		exitCommandError("pipe", session.OutputFormat, fmt.Errorf("请求失败: %w", err), nil)
	}

	renderPipeResponse(session, result)
}

// runStreamPipe 流式模式：实时处理管道输入
func runStreamPipe(session *PipeSession) {
	reader := bufio.NewReader(os.Stdin)
	var buffer strings.Builder
	var mu sync.Mutex
	var lastSent time.Time
	minInterval := time.Second // 最小发送间隔

	// 后台 goroutine 定期发送
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				mu.Lock()
				if buffer.Len() > 0 && time.Since(lastSent) >= minInterval {
					content := buffer.String()
					buffer.Reset()
					lastSent = time.Now()
					mu.Unlock()

					// 发送请求
					fullMessage := session.Prompt + "\n\n" + content
					result, err := sendPipeRequest(session, fullMessage, true)
					if err != nil {
						fmt.Fprintf(os.Stderr, "\n[错误: %v]\n", err)
						continue
					}
					if result.Response != "" {
						renderPipeResponse(session, result)
					}
				} else {
					mu.Unlock()
				}
			}
		}
	}()

	// 读取 stdin
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				// 发送剩余内容
				mu.Lock()
				if buffer.Len() > 0 {
					content := buffer.String()
					buffer.Reset()
					mu.Unlock()
					fullMessage := session.Prompt + "\n\n" + content
					result, err := sendPipeRequest(session, fullMessage, true)
					if err == nil && result.Response != "" {
						renderPipeResponse(session, result)
					}
				} else {
					mu.Unlock()
				}
				break
			}
			fmt.Fprintf(os.Stderr, "读取失败: %v\n", err)
			break
		}

		mu.Lock()
		buffer.WriteString(line)
		mu.Unlock()
	}
}

// sendPipeRequest 发送管道请求
func sendPipeRequest(session *PipeSession, message string, stream bool) (*pipeCommandResult, error) {
	// 构建请求
	messages := []map[string]interface{}{
		{"role": "user", "content": message},
	}

	config := adapter.RequestConfig{
		Model:       session.Model,
		Messages:    messages,
		Stream:      stream,
		MaxTokens:   session.MaxTokens,
		Temperature: 0.7,
	}
	requestBody := session.Adapter.BuildRequest(config)

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	// 创建请求
	req, err := http.NewRequest("POST", session.BaseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	// 设置 headers
	adapterConfig := adapter.AdapterConfig{
		Type:    session.Provider.GetProtocol(),
		APIKey:  session.Provider.GetAPIKey(),
		Timeout: 120 * time.Second,
	}
	headers := session.Adapter.BuildHeaders(adapterConfig)
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// 发送请求
	startTime := time.Now()
	resp, err := session.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	result := &pipeCommandResult{
		StatusCode: resp.StatusCode,
		DurationMs: time.Since(startTime).Milliseconds(),
	}

	if stream {
		// 处理流式响应
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				if data == "[DONE]" {
					continue
				}
				var chunkData map[string]any
				if err := json.Unmarshal([]byte(data), &chunkData); err == nil {
					// 优先提取推理内容
					if reasoning := session.Adapter.ExtractStreamReasoning(chunkData); reasoning != "" {
						result.Response += reasoning
						fmt.Fprint(os.Stderr, reasoning) // 流式输出到 stderr
					} else {
						// 提取普通内容
						chunk := session.Adapter.ExtractStreamContent(chunkData)
						if chunk != "" {
							result.Response += chunk
							fmt.Fprint(os.Stdout, chunk) // 流式输出到 stdout
						}
					}
				}
			}
		}
	} else {
		// 处理普通响应
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("读取响应失败: %w", err)
		}
		assistantMsg, handleErr := session.Adapter.HandleResponse(false, bytes.NewReader(body), adapter.StreamCallbacks{})
		if handleErr == nil {
			assistantMsg = normalizePipeSessionAssistantMessage(session, assistantMsg)
			if content, ok := assistantMsg["content"].(string); ok && strings.TrimSpace(content) != "" {
				result.Response = content
			}
		}
		if strings.TrimSpace(result.Response) == "" {
			result.Response = extractSimpleResponseText(body)
		}
		result.Usage = extractUsageFromAnyResponseBody(body)
		if json.Valid(body) {
			result.Raw = json.RawMessage(body)
		}
	}

	return result, nil
}

type pipeResponsePayload struct {
	Response   string                 `json:"response"`
	Prompt     string                 `json:"prompt,omitempty"`
	Provider   string                 `json:"provider,omitempty"`
	Protocol   string                 `json:"protocol,omitempty"`
	Model      string                 `json:"model,omitempty"`
	Stream     bool                   `json:"stream"`
	StatusCode int                    `json:"status_code,omitempty"`
	DurationMs int64                  `json:"duration_ms,omitempty"`
	Usage      map[string]interface{} `json:"usage,omitempty"`
	Raw        json.RawMessage        `json:"raw,omitempty"`
}

func buildPipeResponsePayload(session *PipeSession, result *pipeCommandResult) pipeResponsePayload {
	payload := pipeResponsePayload{}
	if result != nil {
		payload.Response = result.Response
		payload.StatusCode = result.StatusCode
		payload.DurationMs = result.DurationMs
		payload.Usage = result.Usage
		payload.Raw = result.Raw
	}
	if session != nil {
		payload.Prompt = session.Prompt
		payload.Provider = session.ProviderName
		payload.Protocol = session.Provider.GetProtocol()
		payload.Model = session.Model
	}
	return payload
}

func renderPipeResponse(session *PipeSession, result *pipeCommandResult) {
	if result == nil {
		return
	}
	if session == nil {
		fmt.Println(result.Response)
		return
	}
	if session.OutputFormat == "json" {
		payload := buildPipeResponsePayload(session, result)
		payload.Stream = false
		printCommandJSONOutput("pipe", session.JSONEnvelope, payload)
		return
	}
	fmt.Println(result.Response)
}
