package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	aicliExecToolName          = "aicli_exec"
	aicliExecNestedDepthEnvVar = "AICLI_NESTED_EXEC_DEPTH"
	defaultAICLIExecTimeout    = 2 * time.Minute
)

// AICLIExecTool launches the aicli executable as a deterministic argv-based tool.
//
// This mirrors Codex's separation between SKILL.md prompt instructions and real
// program execution: SKILL.md can describe when to use aicli, while this tool
// owns cwd/env/argv/timeout/output capture.
type AICLIExecTool struct {
	*toolkit.BaseTool
}

func NewAICLIExecTool() *AICLIExecTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "交给子 aicli exec 的任务提示词。工具默认通过 stdin 传入 prompt，避免 Windows shell 命令长度和转义问题。",
			},
			"cwd": map[string]interface{}{
				"type":        "string",
				"description": "可选：子 aicli 进程工作目录。未指定时使用当前 aicli 进程工作目录；相对路径基于当前工作目录解析。",
			},
			"config": map[string]interface{}{
				"type":        "string",
				"description": "可选：显式传给根命令的 --config 路径。未指定时不注入父会话配置，让子 aicli 按自身默认顺序查找配置。",
			},
			"provider": map[string]interface{}{
				"type":        "string",
				"description": "可选：传给 aicli exec 的 --provider。",
			},
			"model": map[string]interface{}{
				"type":        "string",
				"description": "可选：传给 aicli exec 的 --model。",
			},
			"profile": map[string]interface{}{
				"type":        "string",
				"description": "可选：传给 aicli exec 的 --profile。",
			},
			"agent": map[string]interface{}{
				"type":        "string",
				"description": "可选：传给 aicli exec 的 --agent。",
			},
			"log_dir": map[string]interface{}{
				"type":        "string",
				"description": "可选：传给 aicli exec 的 --log-dir，用于指定子 aicli chat/debug/http artifact 日志目录。",
			},
			"session_dir": map[string]interface{}{
				"type":        "string",
				"description": "可选：传给 aicli exec 的 --session-dir，用于指定子 aicli 会话存储目录。",
			},
			"user": map[string]interface{}{
				"type":        "string",
				"description": "可选：传给 aicli exec 的 --user。",
			},
			"title": map[string]interface{}{
				"type":        "string",
				"description": "可选：传给 aicli exec 的 --title。",
			},
			"output": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"text", "json"},
				"description": "可选：传给 aicli exec 的 --output。默认 text。",
			},
			"json": map[string]interface{}{
				"type":        "boolean",
				"description": "可选：传给 aicli exec 的 --json，输出 JSONL 事件流。不能与 output=json 同时使用。",
			},
			"envelope": map[string]interface{}{
				"type":        "boolean",
				"description": "可选：传给 aicli exec 的 --envelope。",
			},
			"disable_tools": map[string]interface{}{
				"type":        "boolean",
				"description": "可选：是否传 --disable-tools。默认 true，避免非交互嵌套运行请求审批。",
			},
			"permission_mode": map[string]interface{}{
				"type":        "string",
				"description": "可选：传给 aicli exec 的 --permission-mode（default|accept_edits|plan|bypass_permissions）。",
			},
			"yolo": map[string]interface{}{
				"type":        "boolean",
				"description": "可选：传给 aicli exec 的 --yolo。仅在明确需要子 aicli 使用工具并绕过权限时使用。",
			},
			"skills_dir": map[string]interface{}{
				"type":        "array",
				"description": "可选：一个或多个 --skills-dir 路径。",
				"items": map[string]interface{}{
					"type": "string",
				},
			},
			"skills_mode": map[string]interface{}{
				"type":        "string",
				"description": "可选：传给 aicli exec 的 --skills-mode（auto|prefer|only）。",
			},
			"timeout": map[string]interface{}{
				"type":        "string",
				"description": "可选：整次子 aicli exec 超时，如 30s、2m、5m。默认 2m。",
			},
			"timeout_ms": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "可选：整次子 aicli exec 超时毫秒数。优先级高于 timeout。",
			},
			"request_timeout": map[string]interface{}{
				"type":        "string",
				"description": "可选：传给 aicli exec 的 --request-timeout。",
			},
			"debug_http": map[string]interface{}{
				"type":        "boolean",
				"description": "可选：传给 aicli exec 的 --debug-http，记录子请求 HTTP 调试信息。",
			},
			"fail_fast": map[string]interface{}{
				"type":        "boolean",
				"description": "可选：传给 aicli exec 的 --fail-fast，禁用自动重试以便定位首个错误。",
			},
			"executable_path": map[string]interface{}{
				"type":        "string",
				"description": "可选：显式 aicli 可执行文件路径。通常不需要；测试或多版本并存时使用。",
			},
			"allow_nested": map[string]interface{}{
				"type":        "boolean",
				"description": "可选：允许根 Agent 在已有嵌套 aicli_exec 子进程中再次运行。默认 false。该参数不能允许 spawn_agent 创建的子 Agent 再启动独立 aicli。",
			},
			"output_bytes_cap": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "可选：stdout/stderr 合并输出的保留上限（字节）。必须为正整数。若同时设置 disable_output_cap=true，为保证资源边界，以本参数为准。",
			},
			"disable_output_cap": map[string]interface{}{
				"type":        "boolean",
				"description": "可选：设为 true 时关闭工具输出 capture limit；若同时设置 output_bytes_cap，则保留后者的显式上限。",
			},
		},
		"required":             []string{"prompt"},
		"additionalProperties": false,
	}

	return &AICLIExecTool{
		BaseTool: toolkit.NewBaseTool(
			aicliExecToolName,
			"以确定性 argv 方式启动本机 aicli exec，并返回 stdout/stderr 合并输出。该工具不会从父 chat 会话注入 provider/model/config；未显式传 config 时，子 aicli 按自己的默认配置查找顺序运行。默认通过 stdin 传递 prompt，并默认 --disable-tools，适合在 skill 或 chat 内部稳定调用 aicli。",
			"1.0.0",
			parameters,
			true,
		),
	}
}

func (t *AICLIExecTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataKindKey:            runtimetypes.ToolKindExec,
		runtimetypes.ToolMetadataReadOnlyKey:        false,
		runtimetypes.ToolMetadataMutatesFSKey:       false,
		runtimetypes.ToolMetadataRequiresNetKey:     false,
		runtimetypes.ToolMetadataSupportsParallelKey: false,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassNever,
		"execution_model":                            "argv_process",
		"program":                                    "aicli",
	}
}

func (t *AICLIExecTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	req, err := parseAICLIExecRequest(params)
	if err != nil {
		return &toolkit.ToolResult{Success: false, OutputKind: toolresult.KindText, Error: err}, nil
	}
	if depth := toolctx.AgentDepth(ctx); depth > 0 {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("拒绝子 Agent 调用 aicli_exec：当前 Agent 深度为 %d；请由根 Agent 统一管理代理树和取消传播", depth),
		}, nil
	}
	if !req.AllowNested && currentAICLIExecDepth() > 0 {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("拒绝递归调用 aicli_exec：当前进程已处于嵌套 aicli_exec 中；确需递归时显式设置 allow_nested=true"),
		}, nil
	}

	exe, err := resolveAICLIExecutable(req.ExecutablePath)
	if err != nil {
		return &toolkit.ToolResult{Success: false, OutputKind: toolresult.KindText, Error: err}, nil
	}
	cwd, err := resolveAICLIExecWorkdir(req.CWD)
	if err != nil {
		return &toolkit.ToolResult{Success: false, OutputKind: toolresult.KindText, Error: err}, nil
	}
	argv := buildAICLIExecArgv(req)
	commandForMetadata := append([]string{exe}, argv...)

	cmdCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, exe, argv...)
	cmd.Dir = cwd
	cmd.Stdin = strings.NewReader(req.Prompt)
	cmd.Env = appendAICLIExecDepth(os.Environ())

	outputMirror := resolveToolTerminalOutputMirror(ctx)
	if outputMirror != nil {
		runtimeexecutor.PrepareCommandForLowLatencyOutput(cmd)
	}
	started := time.Now()
	capture, artifactPath, runErr, artifactErr := runtimeexecutor.CaptureCombinedOutputWithArtifactAndMirror(
		cmd,
		req.CaptureLimitBytes(),
		"toolkit",
		"aicli_exec",
		toolctx.ShellOutputArtifactDir(ctx),
		outputMirror,
	)
	artifactPath, artifactErr = ensureLargeHistoryOutputArtifact(
		capture,
		artifactPath,
		artifactErr,
		"toolkit",
		"aicli_exec",
		toolctx.ShellOutputArtifactDir(ctx),
	)
	duration := time.Since(started)

	metadata := buildAICLIExecMetadata(commandForMetadata, cwd, req, capture, artifactPath, artifactErr, duration, cmdCtx.Err() == context.DeadlineExceeded)
	if strings.EqualFold(req.Output, "json") && strings.TrimSpace(capture.Output) != "" {
		metadata["child_output_format"] = "json"
	}
	if runErr != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			runErr = fmt.Errorf("aicli exec 执行超时（超过 %v）", req.Timeout)
		}
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Content:    capture.Output,
			Metadata:   metadata,
			Error:      runErr,
		}, nil
	}

	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    capture.Output,
		Metadata:   metadata,
	}, nil
}

type aicliExecRequest struct {
	Prompt            string
	CWD               string
	Config            string
	Provider          string
	Model             string
	Profile           string
	Agent             string
	LogDir            string
	SessionDir        string
	User              string
	Title             string
	Output            string
	JSON              bool
	Envelope          bool
	DisableTools      bool
	PermissionMode    string
	Yolo              bool
	SkillsDir         []string
	SkillsMode        string
	Timeout           time.Duration
	RequestTimeout    string
	DebugHTTP         bool
	FailFast          bool
	ExecutablePath    string
	AllowNested       bool
	OutputBytesCap    int
	HasOutputBytesCap bool
	DisableOutputCap  bool
}

func parseAICLIExecRequest(params map[string]interface{}) (aicliExecRequest, error) {
	req := aicliExecRequest{
		Output:       "text",
		DisableTools: true,
		Timeout:      defaultAICLIExecTimeout,
	}
	req.Prompt = strings.TrimSpace(extractString(params["prompt"]))
	if req.Prompt == "" {
		return req, fmt.Errorf("prompt 参数不能为空")
	}
	req.CWD = extractString(params["cwd"])
	req.Config = extractString(params["config"])
	req.Provider = extractString(params["provider"])
	req.Model = extractString(params["model"])
	req.Profile = extractString(params["profile"])
	req.Agent = extractString(params["agent"])
	req.LogDir = extractString(params["log_dir"])
	req.SessionDir = extractString(params["session_dir"])
	req.User = extractString(params["user"])
	req.Title = extractString(params["title"])
	req.ExecutablePath = extractString(params["executable_path"])
	req.PermissionMode = extractString(params["permission_mode"])
	req.SkillsMode = extractString(params["skills_mode"])
	req.RequestTimeout = extractString(params["request_timeout"])
	if output := strings.ToLower(extractString(params["output"])); output != "" {
		switch output {
		case "text", "json":
			req.Output = output
		default:
			return req, fmt.Errorf("output 仅支持 text|json，当前值: %s", output)
		}
	}
	if value, ok, err := optionalBool(params, "json"); err != nil {
		return req, err
	} else if ok {
		req.JSON = value
	}
	if req.JSON && req.Output == "json" {
		return req, fmt.Errorf("json=true 不能与 output=json 同时使用")
	}
	if value, ok, err := optionalBool(params, "envelope"); err != nil {
		return req, err
	} else if ok {
		req.Envelope = value
	}
	if value, ok, err := optionalBool(params, "disable_tools"); err != nil {
		return req, err
	} else if ok {
		req.DisableTools = value
	}
	if value, ok, err := optionalBool(params, "yolo"); err != nil {
		return req, err
	} else if ok {
		req.Yolo = value
	}
	if value, ok, err := optionalBool(params, "allow_nested"); err != nil {
		return req, err
	} else if ok {
		req.AllowNested = value
	}
	if value, ok, err := optionalBool(params, "debug_http"); err != nil {
		return req, err
	} else if ok {
		req.DebugHTTP = value
	}
	if value, ok, err := optionalBool(params, "fail_fast"); err != nil {
		return req, err
	} else if ok {
		req.FailFast = value
	}
	req.SkillsDir = extractStringList(params["skills_dir"])
	if timeoutMS, ok, err := optionalPositiveInt(params, "timeout_ms"); err != nil {
		return req, err
	} else if ok {
		req.Timeout = time.Duration(timeoutMS) * time.Millisecond
	} else if timeoutText := extractString(params["timeout"]); timeoutText != "" {
		parsed, err := time.ParseDuration(timeoutText)
		if err != nil || parsed <= 0 {
			return req, fmt.Errorf("timeout 参数无效: %q", timeoutText)
		}
		req.Timeout = parsed
	}
	if capBytes, ok, err := optionalPositiveInt(params, "output_bytes_cap"); err != nil {
		return req, err
	} else if ok {
		req.OutputBytesCap = capBytes
		req.HasOutputBytesCap = true
	}
	if value, ok, err := optionalBool(params, "disable_output_cap"); err != nil {
		return req, err
	} else if ok {
		req.DisableOutputCap = value
	}
	if req.DisableOutputCap && req.HasOutputBytesCap {
		req.DisableOutputCap = false
	}
	return req, nil
}

func buildAICLIExecArgv(req aicliExecRequest) []string {
	args := make([]string, 0, 24)
	if req.Config != "" {
		args = append(args, "--config", req.Config)
	}
	if req.Envelope {
		args = append(args, "--envelope")
	}
	args = append(args, "exec")
	appendStringFlag := func(name, value string) {
		if strings.TrimSpace(value) != "" {
			args = append(args, name, strings.TrimSpace(value))
		}
	}
	appendStringFlag("--provider", req.Provider)
	appendStringFlag("--model", req.Model)
	appendStringFlag("--profile", req.Profile)
	appendStringFlag("--agent", req.Agent)
	appendStringFlag("--log-dir", req.LogDir)
	appendStringFlag("--session-dir", req.SessionDir)
	appendStringFlag("--user", req.User)
	appendStringFlag("--title", req.Title)
	if req.JSON {
		args = append(args, "--json")
	} else {
		appendStringFlag("--output", req.Output)
	}
	appendStringFlag("--permission-mode", req.PermissionMode)
	appendStringFlag("--skills-mode", req.SkillsMode)
	appendStringFlag("--request-timeout", req.RequestTimeout)
	for _, dir := range req.SkillsDir {
		appendStringFlag("--skills-dir", dir)
	}
	if req.DisableTools {
		args = append(args, "--disable-tools")
	}
	if req.Yolo {
		args = append(args, "--yolo")
	}
	if req.DebugHTTP {
		args = append(args, "--debug-http")
	}
	if req.FailFast {
		args = append(args, "--fail-fast")
	}
	if req.Timeout > 0 {
		args = append(args, "--timeout", req.Timeout.String())
	}
	return args
}

func resolveAICLIExecutable(explicit string) (string, error) {
	explicit = strings.TrimSpace(explicit)
	if explicit != "" {
		if info, err := os.Stat(explicit); err == nil && !info.IsDir() {
			return explicit, nil
		}
		return "", fmt.Errorf("指定的 aicli executable_path 不存在或不是文件: %s", explicit)
	}
	if current, err := os.Executable(); err == nil {
		base := strings.ToLower(filepath.Base(current))
		if base == "aicli" || base == "aicli.exe" {
			return current, nil
		}
	}
	if path, err := exec.LookPath("aicli"); err == nil {
		return path, nil
	}
	if path, err := exec.LookPath("aicli.exe"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("未找到 aicli 可执行文件；请确认 aicli 在 PATH 中，或传入 executable_path")
}

func resolveAICLIExecWorkdir(cwd string) (string, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return os.Getwd()
	}
	if filepath.IsAbs(cwd) {
		return filepath.Clean(cwd), nil
	}
	base, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("获取当前工作目录失败: %w", err)
	}
	return filepath.Clean(filepath.Join(base, cwd)), nil
}

func buildAICLIExecMetadata(command []string, cwd string, req aicliExecRequest, capture runtimeexecutor.CombinedOutputCapture, artifactPath string, artifactErr error, duration time.Duration, timedOut bool) map[string]interface{} {
	metadata := map[string]interface{}{
		"command":                       command,
		"argv":                          command,
		"cwd":                           cwd,
		"duration_ms":                   duration.Milliseconds(),
		"timeout_ms":                    req.Timeout.Milliseconds(),
		"timed_out":                     timedOut,
		"prompt_bytes":                  len(req.Prompt),
		"config_explicit":               strings.TrimSpace(req.Config) != "",
		"disable_tools":                 req.DisableTools,
		"output_size":                   len(capture.Output),
		"captured_output_bytes":         capture.RetainedBytes,
		"retained_output_bytes":         capture.RetainedBytes,
		"total_output_bytes":            capture.TotalBytes,
		"total_output_lines":            capture.TotalLines,
		"output_capture_complete":       !capture.Truncated,
		"output_truncated":              capture.Truncated,
		"capture_limit_reached":         capture.Truncated,
		"output_capture_limit_disabled": capture.CaptureLimitDisabled,
		"executed_at":                   time.Now().Unix(),
		"nested_depth":                  currentAICLIExecDepth(),
	}
	if !capture.CaptureLimitDisabled && capture.CaptureLimitBytes > 0 {
		metadata["output_capture_limit_bytes"] = capture.CaptureLimitBytes
	}
	if capture.OmittedBytes > 0 {
		metadata["omitted_output_bytes"] = capture.OmittedBytes
	}
	if strings.TrimSpace(artifactPath) != "" {
		metadata["raw_output_artifact_path"] = artifactPath
	}
	if artifactErr != nil {
		metadata["raw_output_artifact_error"] = artifactErr.Error()
	}
	if req.Config != "" {
		metadata["config"] = req.Config
	}
	if req.Provider != "" {
		metadata["provider"] = req.Provider
	}
	if req.Model != "" {
		metadata["model"] = req.Model
	}
	if req.LogDir != "" {
		metadata["log_dir"] = req.LogDir
	}
	if req.SessionDir != "" {
		metadata["session_dir"] = req.SessionDir
	}
	if req.DebugHTTP {
		metadata["debug_http"] = true
	}
	if req.FailFast {
		metadata["fail_fast"] = true
	}
	return metadata
}

func (r aicliExecRequest) CaptureLimitBytes() int {
	if r.DisableOutputCap {
		return runtimeexecutor.DisableRetainedOutputLimit
	}
	if r.HasOutputBytesCap {
		return r.OutputBytesCap
	}
	return runtimeexecutor.DefaultRetainedOutputBytes
}

func currentAICLIExecDepth() int {
	value := strings.TrimSpace(os.Getenv(aicliExecNestedDepthEnvVar))
	if value == "" {
		return 0
	}
	depth, err := strconv.Atoi(value)
	if err != nil || depth < 0 {
		return 0
	}
	return depth
}

func appendAICLIExecDepth(env []string) []string {
	nextDepth := currentAICLIExecDepth() + 1
	prefix := aicliExecNestedDepthEnvVar + "="
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			out = append(out, prefix+strconv.Itoa(nextDepth))
			replaced = true
			continue
		}
		out = append(out, item)
	}
	if !replaced {
		out = append(out, prefix+strconv.Itoa(nextDepth))
	}
	return out
}

func optionalBool(params map[string]interface{}, key string) (bool, bool, error) {
	if params == nil {
		return false, false, nil
	}
	raw, ok := params[key]
	if !ok || raw == nil {
		return false, false, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, true, fmt.Errorf("%s 参数必须是布尔值", key)
	}
	return value, true, nil
}

func optionalPositiveInt(params map[string]interface{}, key string) (int, bool, error) {
	if params == nil {
		return 0, false, nil
	}
	raw, ok := params[key]
	if !ok || raw == nil {
		return 0, false, nil
	}
	if isNumericZero(raw) {
		return 0, false, nil
	}
	value, err := extractPositiveInt(raw)
	if err != nil {
		return 0, true, fmt.Errorf("%s 参数无效: %w", key, err)
	}
	return value, true, nil
}
