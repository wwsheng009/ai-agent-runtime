package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

type doctorProviderReport struct {
	ConfigPath     string                     `json:"config_path,omitempty"`
	Provider       string                     `json:"provider"`
	Protocol       string                     `json:"protocol"`
	Model          string                     `json:"model"`
	RequestedModel string                     `json:"requested_model,omitempty"`
	ModelMapped    bool                       `json:"model_mapped,omitempty"`
	Endpoint       string                     `json:"endpoint"`
	HasAPIKey      bool                       `json:"has_api_key"`
	APIKeyCount    int                        `json:"api_key_count"`
	Timeout        string                     `json:"timeout"`
	RequestTimeout string                     `json:"request_timeout"`
	StartedAt      string                     `json:"started_at"`
	FinishedAt     string                     `json:"finished_at"`
	Cases          []doctorProviderCaseResult `json:"cases"`
	Findings       []string                   `json:"findings,omitempty"`
}

type doctorProviderCaseResult struct {
	Name             string                     `json:"name"`
	Description      string                     `json:"description"`
	Command          []string                   `json:"command"`
	OK               bool                       `json:"ok"`
	ExitCode         int                        `json:"exit_code"`
	DurationMs       int64                      `json:"duration_ms"`
	Error            string                     `json:"error,omitempty"`
	StdoutPreview    string                     `json:"stdout_preview,omitempty"`
	StderrPreview    string                     `json:"stderr_preview,omitempty"`
	LogDir           string                     `json:"log_dir,omitempty"`
	ChatLogFile      string                     `json:"chat_log_file,omitempty"`
	DebugLogFile     string                     `json:"debug_log_file,omitempty"`
	HTTPRequestFile  string                     `json:"http_request_file,omitempty"`
	HTTPResponseFile string                     `json:"http_response_file,omitempty"`
	HTTPRequest      *doctorHTTPArtifactSummary `json:"http_request,omitempty"`
	HTTPResponse     *doctorHTTPArtifactSummary `json:"http_response,omitempty"`
}

type doctorHTTPArtifactSummary struct {
	Phase              string `json:"phase,omitempty"`
	Protocol           string `json:"protocol,omitempty"`
	Model              string `json:"model,omitempty"`
	Method             string `json:"method,omitempty"`
	URL                string `json:"url,omitempty"`
	Attempt            int    `json:"attempt,omitempty"`
	MaxAttempts        int    `json:"max_attempts,omitempty"`
	ResponseStatusCode int    `json:"response_status_code,omitempty"`
	BodyBytes          int    `json:"body_bytes,omitempty"`
	BodyFormat         string `json:"body_format,omitempty"`
	BodyModel          string `json:"body_model,omitempty"`
	MessageCount       int    `json:"message_count,omitempty"`
	ToolCount          int    `json:"tool_count,omitempty"`
	Stream             *bool  `json:"stream,omitempty"`
	MaxTokens          int    `json:"max_tokens,omitempty"`
	OutputEffort       string `json:"output_effort,omitempty"`
	ThinkingEffort     string `json:"thinking_effort,omitempty"`
	SystemBytes        int    `json:"system_bytes,omitempty"`
	BodyPreview        string `json:"body_preview,omitempty"`
}

type doctorProviderOptions struct {
	ProviderFlag    string
	ModelFlag       string
	Message         string
	RequestTimeout  string
	Timeout         time.Duration
	OutputOptions   structuredOutputOptions
	IncludeYolo     bool
	IncludeToolChat bool
}

func NewDoctorCommand(getCfg func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "诊断 aicli 本地配置、provider 和运行时调用链",
	}
	providerCmd := &cobra.Command{
		Use:   "provider",
		Short: "对指定 provider 执行可复现调用矩阵",
		Run: func(cmd *cobra.Command, args []string) {
			outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
			if err != nil {
				exitCommandError("doctor provider", "json", err, nil)
			}
			providerFlag, _ := cmd.Flags().GetString("provider")
			modelFlag, _ := cmd.Flags().GetString("model")
			message, _ := cmd.Flags().GetString("message")
			requestTimeout, _ := cmd.Flags().GetString("request-timeout")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			includeYolo, _ := cmd.Flags().GetBool("include-yolo")
			includeToolChat, _ := cmd.Flags().GetBool("include-tool-chat")
			report, details, err := runDoctorProvider(getCfg(), doctorProviderOptions{
				ProviderFlag:    providerFlag,
				ModelFlag:       modelFlag,
				Message:         message,
				RequestTimeout:  requestTimeout,
				Timeout:         timeout,
				OutputOptions:   outputOptions,
				IncludeYolo:     includeYolo,
				IncludeToolChat: includeToolChat,
			})
			if err != nil {
				exitCommandError("doctor provider", outputOptions.Format, err, details)
			}
			renderDoctorProviderReport(report, outputOptions)
		},
	}
	providerCmd.Flags().StringP("provider", "p", "", "指定 provider 名称，留空使用默认 provider")
	providerCmd.Flags().StringP("model", "m", "", "指定模型名称，留空使用 provider.default_model")
	providerCmd.Flags().StringP("message", "M", "查看当前日期，只输出日期", "诊断请求的用户消息")
	providerCmd.Flags().String("request-timeout", "30s", "传给 exec/chat 的单次 LLM 请求超时")
	providerCmd.Flags().Duration("timeout", 45*time.Second, "每个诊断用例的进程级超时")
	providerCmd.Flags().String("output", "", "输出格式（text|json）")
	providerCmd.Flags().Bool("json", false, "以 JSON 格式输出")
	providerCmd.Flags().Bool("include-yolo", true, "包含 exec --yolo 用例，用于观察工具调用链")
	providerCmd.Flags().Bool("include-tool-chat", true, "包含 chat 暴露 tools/skills 的用例")
	cmd.AddCommand(providerCmd)
	cmd.AddCommand(newDoctorSubagentRouteCommand(getCfg))
	return cmd
}

func runDoctorProvider(cfg *config.Config, opts doctorProviderOptions) (*doctorProviderReport, map[string]interface{}, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 45 * time.Second
	}
	if strings.TrimSpace(opts.RequestTimeout) == "" {
		opts.RequestTimeout = "30s"
	}
	if strings.TrimSpace(opts.Message) == "" {
		opts.Message = "查看当前日期，只输出日期"
	}
	resolved, details, err := resolveProviderExecutionContext(cfg, opts.ProviderFlag, opts.ModelFlag)
	if err != nil {
		return nil, details, err
	}

	exe, err := os.Executable()
	if err != nil {
		return nil, details, fmt.Errorf("resolve current executable: %w", err)
	}
	configPath := ""
	if cfg != nil {
		configPath = strings.TrimSpace(cfg.ConfigFilePath)
	}
	endpoint := buildProviderURL(resolved.Provider, resolved.Adapter.GetAPIPath(), resolved.Model)
	report := &doctorProviderReport{
		ConfigPath:     configPath,
		Provider:       resolved.ProviderName,
		Protocol:       resolved.Provider.GetProtocol(),
		Model:          resolved.Model,
		RequestedModel: resolved.RequestedModel,
		ModelMapped:    resolved.ModelMapped,
		Endpoint:       endpoint,
		HasAPIKey:      strings.TrimSpace(resolved.Provider.GetAPIKey()) != "",
		APIKeyCount:    countProviderAPIKeys(resolved.Provider),
		Timeout:        opts.Timeout.String(),
		RequestTimeout: opts.RequestTimeout,
		StartedAt:      time.Now().Format(time.RFC3339Nano),
	}

	for _, spec := range doctorProviderCases(configPath, resolved.ProviderName, resolved.Model, opts) {
		report.Cases = append(report.Cases, runDoctorProviderCase(exe, spec, opts.Timeout))
	}
	report.FinishedAt = time.Now().Format(time.RFC3339Nano)
	report.Findings = buildDoctorProviderFindings(report)
	return report, details, nil
}

type doctorProviderCaseSpec struct {
	Name        string
	Description string
	Args        []string
}

func doctorProviderCases(configPath, providerName, modelName string, opts doctorProviderOptions) []doctorProviderCaseSpec {
	rootArgs := make([]string, 0, 2)
	if strings.TrimSpace(configPath) != "" {
		rootArgs = append(rootArgs, "--config", configPath)
	}
	commonExec := append([]string{}, rootArgs...)
	commonExec = append(commonExec,
		"exec",
		"--provider", providerName,
		"--model", modelName,
		"--output", "text",
		"--timeout", opts.Timeout.String(),
		"--request-timeout", opts.RequestTimeout,
		"--fail-fast",
	)
	commonChat := append([]string{}, rootArgs...)
	commonChat = append(commonChat,
		"chat",
		"--provider", providerName,
		"--model", modelName,
		"--no-interactive",
		"--output", "text",
		"--request-timeout", opts.RequestTimeout,
		"--debug-http",
		"--fail-fast",
		"--message", opts.Message,
	)
	testTimeout := "30"
	if parsed, err := time.ParseDuration(opts.RequestTimeout); err == nil && parsed > 0 {
		testTimeout = strconv.Itoa(int(parsed.Round(time.Second).Seconds()))
	}
	commonTest := append([]string{}, rootArgs...)
	commonTest = append(commonTest,
		"test",
		"--provider", providerName,
		"--model", modelName,
		"--message", opts.Message,
		"--timeout", testTimeout,
		"--output", "json",
	)

	cases := []doctorProviderCaseSpec{
		{
			Name:        "test-direct",
			Description: "直接 adapter HTTP 调用，不经过 chat/exec actor 工具面",
			Args:        commonTest,
		},
		{
			Name:        "exec-disable-tools",
			Description: "headless exec，关闭 tools/skills，验证纯模型调用",
			Args: append(append([]string{}, commonExec...),
				"--disable-tools",
				opts.Message,
			),
		},
		{
			Name:        "chat-disable-tools",
			Description: "chat 非交互单轮，关闭 tools/skills，保留 chat 请求包装与 debug-http",
			Args: append(append([]string{}, commonChat...),
				"--disable-tools",
			),
		},
	}
	if opts.IncludeToolChat {
		cases = append(cases, doctorProviderCaseSpec{
			Name:        "chat-with-tools",
			Description: "chat 非交互单轮，暴露 tools/skills，用于定位 function calling/schema 兼容性",
			Args:        commonChat,
		})
	}
	if opts.IncludeYolo {
		cases = append(cases, doctorProviderCaseSpec{
			Name:        "exec-yolo",
			Description: "headless exec 允许工具调用，用于观察工具执行链和审批差异",
			Args: append(append([]string{}, commonExec...),
				"--debug-http",
				"--yolo",
				opts.Message,
			),
		})
	}
	return cases
}

func runDoctorProviderCase(exe string, spec doctorProviderCaseSpec, timeout time.Duration) doctorProviderCaseResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
	defer cancel()

	cmd := osexec.CommandContext(ctx, exe, spec.Args...)
	cmd.Dir = currentWorkingDirOrEmpty()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	result := doctorProviderCaseResult{
		Name:          spec.Name,
		Description:   spec.Description,
		Command:       append([]string{"aicli"}, spec.Args...),
		OK:            err == nil,
		ExitCode:      commandExitCode(err, ctx.Err()),
		DurationMs:    time.Since(start).Milliseconds(),
		Error:         commandErrorString(err, ctx.Err()),
		StdoutPreview: trimPreview(stdout.String(), 1200),
		StderrPreview: trimPreview(stderr.String(), 1200),
	}
	enrichDoctorProviderCaseArtifacts(&result, start)
	return result
}

func enrichDoctorProviderCaseArtifacts(result *doctorProviderCaseResult, start time.Time) {
	if result == nil {
		return
	}
	logDir := latestChatLogDirAfter(start.Add(-1 * time.Second))
	if logDir == "" {
		return
	}
	result.LogDir = logDir
	result.DebugLogFile = firstExistingPath(filepath.Join(logDir, "debug.log"))
	if matches, _ := filepath.Glob(filepath.Join(logDir, "chat_*.json")); len(matches) > 0 {
		sort.Strings(matches)
		result.ChatLogFile = matches[len(matches)-1]
	}
	runtimeDir := filepath.Join(logDir, "runtime-http")
	requestFile := latestArtifactFile(runtimeDir, "request")
	responseFile := latestArtifactFile(runtimeDir, "response")
	result.HTTPRequestFile = requestFile
	result.HTTPResponseFile = responseFile
	if requestFile != "" {
		result.HTTPRequest = summarizeDoctorHTTPArtifact(requestFile)
	}
	if responseFile != "" {
		result.HTTPResponse = summarizeDoctorHTTPArtifact(responseFile)
	}
	if result.HTTPRequest == nil && result.DebugLogFile != "" {
		request, response := summarizeDoctorChatDebugLog(result.DebugLogFile)
		result.HTTPRequest = request
		if result.HTTPResponse == nil {
			result.HTTPResponse = response
		}
	}
}

func latestChatLogDirAfter(start time.Time) string {
	root := defaultAICLIChatLogRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(start) {
			continue
		}
		candidates = append(candidates, candidate{
			path: filepath.Join(root, entry.Name()),
			mod:  info.ModTime(),
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].mod.After(candidates[j].mod)
	})
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0].path
}

func latestArtifactFile(dir, phase string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		if !strings.Contains(name, "_"+phase+"_") || !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{
			path: filepath.Join(dir, entry.Name()),
			mod:  info.ModTime(),
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].mod.After(candidates[j].mod)
	})
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0].path
}

func summarizeDoctorHTTPArtifact(path string) *doctorHTTPArtifactSummary {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	summary := &doctorHTTPArtifactSummary{
		Phase:              stringMapValue(payload, "phase"),
		Protocol:           stringMapValue(payload, "protocol"),
		Model:              stringMapValue(payload, "model"),
		Method:             stringMapValue(payload, "method"),
		URL:                stringMapValue(payload, "url"),
		Attempt:            intMapValue(payload, "attempt"),
		MaxAttempts:        intMapValue(payload, "max_attempts"),
		ResponseStatusCode: intMapValue(payload, "response_status_code"),
		BodyBytes:          intMapValue(payload, "body_bytes"),
		BodyFormat:         stringMapValue(payload, "body_format"),
		BodyPreview:        trimPreview(stringMapValue(payload, "body_preview"), 600),
	}
	body, _ := payload["body_json"].(map[string]interface{})
	if len(body) > 0 {
		summary.BodyModel = stringMapValue(body, "model")
		summary.MessageCount = lenInterfaceSlice(body["messages"])
		summary.ToolCount = lenInterfaceSlice(body["tools"])
		if stream, ok := body["stream"].(bool); ok {
			summary.Stream = &stream
		}
		summary.MaxTokens = intMapValue(body, "max_tokens")
		summary.SystemBytes = len(stringMapValue(body, "system"))
		if outputConfig, ok := body["output_config"].(map[string]interface{}); ok {
			summary.OutputEffort = stringMapValue(outputConfig, "effort")
		}
		if thinking, ok := body["thinking"].(map[string]interface{}); ok {
			summary.ThinkingEffort = stringMapValue(thinking, "effort")
		}
	}
	return summary
}

func summarizeDoctorChatDebugLog(path string) (*doctorHTTPArtifactSummary, *doctorHTTPArtifactSummary) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	lines := strings.Split(string(raw), "\n")
	request := &doctorHTTPArtifactSummary{Phase: "request"}
	response := &doctorHTTPArtifactSummary{Phase: "response"}
	var requestSeen, responseSeen bool
	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case strings.Contains(line, "[http-debug] POST "):
			request.Method = "POST"
			request.URL = strings.TrimSpace(line[strings.Index(line, "[http-debug] POST ")+len("[http-debug] POST "):])
			response.URL = request.URL
		case strings.Contains(line, "[http-debug] disable_retries="):
			status := intAfterMarker(line, "final_status=")
			if status > 0 {
				response.ResponseStatusCode = status
				responseSeen = true
			}
		case strings.HasPrefix(line, "[http-debug] request_body_bytes="):
			request.BodyBytes = intAfterMarker(line, "request_body_bytes=")
			requestSeen = true
		case strings.HasPrefix(line, "[http-debug] request_body="):
			bodyText := strings.TrimSpace(strings.TrimPrefix(line, "[http-debug] request_body="))
			request.BodyPreview = trimPreview(bodyText, 600)
			fillDoctorRequestBodySummary(request, bodyText)
			requestSeen = true
		case strings.HasPrefix(line, "[http-debug] attempt="):
			response.ResponseStatusCode = intAfterMarker(line, "status=")
			response.BodyBytes = intAfterMarker(line, "response_bytes=")
			response.BodyPreview = trimPreview(stringBetweenMarkers(line, "preview=\"", "\""), 600)
			responseSeen = true
		}
	}
	if request.URL == "" && request.BodyBytes == 0 && request.BodyModel == "" {
		requestSeen = false
	}
	if !requestSeen {
		request = nil
	}
	if !responseSeen {
		response = nil
	}
	return request, response
}

func fillDoctorRequestBodySummary(summary *doctorHTTPArtifactSummary, bodyText string) {
	if summary == nil || strings.TrimSpace(bodyText) == "" {
		return
	}
	var body map[string]interface{}
	if err := json.Unmarshal([]byte(bodyText), &body); err != nil {
		return
	}
	summary.BodyFormat = "json"
	summary.BodyModel = stringMapValue(body, "model")
	summary.MessageCount = lenInterfaceSlice(body["messages"])
	summary.ToolCount = lenInterfaceSlice(body["tools"])
	if stream, ok := body["stream"].(bool); ok {
		summary.Stream = &stream
	}
	summary.MaxTokens = intMapValue(body, "max_tokens")
	summary.SystemBytes = len(stringMapValue(body, "system"))
	if outputConfig, ok := body["output_config"].(map[string]interface{}); ok {
		summary.OutputEffort = stringMapValue(outputConfig, "effort")
	}
	if thinking, ok := body["thinking"].(map[string]interface{}); ok {
		summary.ThinkingEffort = stringMapValue(thinking, "effort")
	}
}

func renderDoctorProviderReport(report *doctorProviderReport, outputOptions structuredOutputOptions) {
	if report == nil {
		return
	}
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("doctor provider", outputOptions.Envelope, report)
		return
	}
	fmt.Println("AICLI Provider Doctor")
	fmt.Printf("Config:          %s\n", chatDebugValueOrNone(report.ConfigPath))
	fmt.Printf("Provider:        %s\n", report.Provider)
	fmt.Printf("Protocol:        %s\n", report.Protocol)
	fmt.Printf("Model:           %s\n", report.Model)
	if report.ModelMapped {
		fmt.Printf("Requested Model: %s\n", report.RequestedModel)
	}
	fmt.Printf("Endpoint:        %s\n", report.Endpoint)
	fmt.Printf("Auth Keys:       %d (has_api_key=%v)\n", report.APIKeyCount, report.HasAPIKey)
	fmt.Printf("Timeout:         %s\n", report.Timeout)
	fmt.Printf("Request Timeout: %s\n", report.RequestTimeout)
	fmt.Println()

	for _, item := range report.Cases {
		status := "PASS"
		if !item.OK {
			status = "FAIL"
		}
		fmt.Printf("[%s] %s (%dms, exit=%d)\n", status, item.Name, item.DurationMs, item.ExitCode)
		fmt.Printf("  %s\n", item.Description)
		if item.Error != "" {
			fmt.Printf("  Error: %s\n", item.Error)
		}
		if item.StdoutPreview != "" {
			fmt.Printf("  Stdout: %s\n", oneLinePreview(item.StdoutPreview, 180))
		}
		if item.StderrPreview != "" {
			fmt.Printf("  Stderr: %s\n", oneLinePreview(item.StderrPreview, 180))
		}
		if item.LogDir != "" {
			fmt.Printf("  Log Dir: %s\n", item.LogDir)
		}
		if item.HTTPRequest != nil {
			fmt.Printf("  Request: model=%s stream=%s tools=%d thinking=%s output_effort=%s url=%s\n",
				firstNonEmptyChatValue(item.HTTPRequest.BodyModel, item.HTTPRequest.Model),
				boolPointerString(item.HTTPRequest.Stream),
				item.HTTPRequest.ToolCount,
				item.HTTPRequest.ThinkingEffort,
				item.HTTPRequest.OutputEffort,
				item.HTTPRequest.URL,
			)
		}
		if item.HTTPResponse != nil {
			fmt.Printf("  Response: status=%d bytes=%d preview=%s\n",
				item.HTTPResponse.ResponseStatusCode,
				item.HTTPResponse.BodyBytes,
				oneLinePreview(item.HTTPResponse.BodyPreview, 180),
			)
		}
		fmt.Println()
	}
	if len(report.Findings) > 0 {
		fmt.Println("Findings:")
		for _, finding := range report.Findings {
			fmt.Printf("- %s\n", finding)
		}
	}
}

func buildDoctorProviderFindings(report *doctorProviderReport) []string {
	if report == nil {
		return nil
	}
	var findings []string
	if !report.HasAPIKey {
		findings = append(findings, "当前 provider 没有解析到 API key；优先检查 .env、api_key/api_keys 或 api_key_ref。")
	}
	caseByName := map[string]doctorProviderCaseResult{}
	for _, item := range report.Cases {
		caseByName[item.Name] = item
	}
	if c, ok := caseByName["test-direct"]; ok && !c.OK {
		findings = append(findings, "test-direct 失败：基础 URL、API key、模型名或上游可用性本身存在问题，chat/skill 不是第一嫌疑。")
	}
	if c, ok := caseByName["exec-disable-tools"]; ok && c.OK {
		findings = append(findings, "exec-disable-tools 成功：默认配置解析和纯模型调用链可用。")
	}
	if c, ok := caseByName["chat-disable-tools"]; ok && c.OK {
		findings = append(findings, "chat-disable-tools 成功：chat 包装路径在关闭 tools/skills 时可用。")
	}
	toolCase, toolOK := caseByName["chat-with-tools"]
	noToolCase, noToolOK := caseByName["chat-disable-tools"]
	if toolOK && noToolOK && noToolCase.OK && !toolCase.OK {
		findings = append(findings, "只有 chat-with-tools 失败：重点检查 tools/skills schema、工具数量、thinking/stream 与上游 function calling 的兼容性。")
	}
	if toolOK && toolCase.OK && toolCase.HTTPRequest != nil && toolCase.HTTPRequest.ToolCount > 0 {
		findings = append(findings, fmt.Sprintf("chat-with-tools 成功且暴露了 %d 个工具，说明当前工具 schema 本次被上游接受；若用户仍偶发失败，更像上游路由/渠道波动。", toolCase.HTTPRequest.ToolCount))
	}
	if yoloCase, ok := caseByName["exec-yolo"]; ok && yoloCase.OK {
		findings = append(findings, "exec-yolo 成功：允许工具执行时没有触发本地审批阻塞。")
	}
	for _, item := range report.Cases {
		if item.HTTPResponse != nil && item.HTTPResponse.ResponseStatusCode >= 400 {
			findings = append(findings, fmt.Sprintf("%s 返回 HTTP %d：查看 %s 可获得完整脱敏请求/响应摘要。", item.Name, item.HTTPResponse.ResponseStatusCode, item.LogDir))
		}
	}
	return uniqueDoctorFindings(findings)
}

func countProviderAPIKeys(provider config.Provider) int {
	count := 0
	if strings.TrimSpace(provider.GetAPIKey()) != "" {
		count++
	}
	for _, key := range provider.APIKeys {
		if strings.TrimSpace(key) != "" {
			count++
		}
	}
	return count
}

func commandExitCode(err error, ctxErr error) int {
	if ctxErr != nil {
		return -1
	}
	if err == nil {
		return 0
	}
	var exitErr *osexec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func commandErrorString(err error, ctxErr error) string {
	if ctxErr != nil {
		return ctxErr.Error()
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

func defaultAICLIChatLogRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".aicli", "chat-logs")
}

func currentWorkingDirOrEmpty() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func firstExistingPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return ""
}

func stringMapValue(values map[string]interface{}, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func intMapValue(values map[string]interface{}, key string) int {
	if values == nil {
		return 0
	}
	value, ok := values[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		n, _ := typed.Int64()
		return int(n)
	default:
		n, _ := strconv.Atoi(fmt.Sprintf("%v", typed))
		return n
	}
}

func lenInterfaceSlice(value interface{}) int {
	switch typed := value.(type) {
	case []interface{}:
		return len(typed)
	case []map[string]interface{}:
		return len(typed)
	default:
		return 0
	}
}

func trimPreview(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "...(truncated)"
}

func oneLinePreview(value string, limit int) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.Join(strings.Fields(value), " ")
	return trimPreview(value, limit)
}

func boolPointerString(value *bool) string {
	if value == nil {
		return "<none>"
	}
	if *value {
		return "true"
	}
	return "false"
}

func intAfterMarker(line, marker string) int {
	index := strings.Index(line, marker)
	if index < 0 {
		return 0
	}
	rest := line[index+len(marker):]
	end := 0
	for end < len(rest) {
		ch := rest[end]
		if ch < '0' || ch > '9' {
			break
		}
		end++
	}
	if end == 0 {
		return 0
	}
	value, _ := strconv.Atoi(rest[:end])
	return value
}

func stringBetweenMarkers(line, startMarker, endMarker string) string {
	start := strings.Index(line, startMarker)
	if start < 0 {
		return ""
	}
	rest := line[start+len(startMarker):]
	end := strings.Index(rest, endMarker)
	if end < 0 {
		return rest
	}
	return rest[:end]
}

func uniqueDoctorFindings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
