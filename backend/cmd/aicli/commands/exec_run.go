package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

const (
	execExitExecutionFailed = 1
	execExitUsage           = 2
	execExitSchemaFailed    = 3
	execExitTimeout         = 124
	execExitInterrupted     = 130
)

type execExitError struct {
	Code string
	Err  error
	Exit int
}

func (e *execExitError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *execExitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func newExecExitError(exit int, code string, err error) error {
	if err == nil {
		return nil
	}
	return &execExitError{Code: code, Err: err, Exit: exit}
}

func execExitCode(err error) int {
	var execErr *execExitError
	if errors.As(err, &execErr) && execErr.Exit != 0 {
		return execErr.Exit
	}
	return execExitExecutionFailed
}

type ExecSession struct {
	Options     *ExecOptions
	Config      *config.Config
	Processor   ExecEventProcessor
	ChatSession *ChatSession
	SessionMgr  *runtimechat.SessionManager
	SessionID   string
	StartTime   time.Time
}

func runExec(cmd *cobra.Command, cfg *config.Config, args []string) error {
	opts, err := parseExecOptions(cmd, args)
	if err != nil {
		return err
	}
	if len(opts.ConfigOverrides) > 0 {
		if err := applyConfigOverrides(cfg, opts.ConfigOverrides); err != nil {
			return newExecExitError(execExitUsage, "CONFIG_OVERRIDE_FAILED", err)
		}
	}
	if restoreLogger := suppressChatConsoleLogger(cfg, &chatCommandOptions{NoInteractive: true, OutputFormat: opts.OutputFormat}); restoreLogger != nil {
		defer restoreLogger()
	}
	processor := NewExecEventProcessor(opts.JSONMode, nil, opts.OutputLastMsg)
	session, cleanup, err := buildExecSession(cfg, opts, processor)
	if err != nil {
		return err
	}
	defer cleanup()
	return executeExecWithSignals(session)
}

func parseExecOptions(cmd *cobra.Command, args []string) (*ExecOptions, error) {
	return parseExecOptionsInternal(cmd, args, true)
}

func parseExecOptionsNoPrompt(cmd *cobra.Command) (*ExecOptions, error) {
	return parseExecOptionsInternal(cmd, nil, false)
}

func parseExecOptionsInternal(cmd *cobra.Command, args []string, readPrompt bool) (*ExecOptions, error) {
	if cmd == nil {
		return nil, newExecExitError(execExitUsage, "INVALID_COMMAND", fmt.Errorf("exec command is nil"))
	}
	opts := &ExecOptions{}
	opts.ProfileFlag, _ = cmd.Flags().GetString("profile")
	opts.AgentFlag, _ = cmd.Flags().GetString("agent")
	opts.ProviderFlag, _ = cmd.Flags().GetString("provider")
	opts.ModelFlag, _ = cmd.Flags().GetString("model")
	opts.MaxTokens, _ = cmd.Flags().GetInt("max-tokens")
	opts.PromptFlag, _ = cmd.Flags().GetString("prompt")
	opts.StreamFlag, _ = cmd.Flags().GetBool("stream")
	opts.StreamChanged = cmd.Flags().Changed("stream")
	opts.ReasoningEffortFlag, _ = cmd.Flags().GetString("reasoning-effort")
	opts.RuntimeModeFlag, _ = cmd.Flags().GetString("runtime-mode")
	opts.RuntimeServerFlag, _ = cmd.Flags().GetString("runtime-server")

	opts.JSONMode, _ = cmd.Flags().GetBool("json")
	outputFlag, _ := cmd.Flags().GetString("output")
	if opts.JSONMode && strings.TrimSpace(outputFlag) != "" {
		return nil, newExecExitError(execExitUsage, "OUTPUT_MODE_CONFLICT", fmt.Errorf("--json 与 --output 不能同时使用"))
	}
	outputFormat, err := resolveChatOutputFormat(true, outputFlag, false)
	if err != nil {
		return nil, newExecExitError(execExitUsage, "INVALID_OUTPUT", err)
	}
	opts.OutputFormat = outputFormat
	opts.OutputLastMsg, _ = cmd.Flags().GetString("output-last-message")
	opts.OutputSchema, _ = cmd.Flags().GetString("output-schema")
	opts.JSONEnvelope = useJSONEnvelope(cmd)

	opts.Ephemeral, _ = cmd.Flags().GetBool("ephemeral")
	opts.SessionDir, _ = cmd.Flags().GetString("session-dir")
	opts.SessionUser, _ = cmd.Flags().GetString("user")
	opts.SessionTitle, _ = cmd.Flags().GetString("title")
	opts.ImagePaths, _ = cmd.Flags().GetStringSlice("image")
	opts.RequestTimeout, _ = cmd.Flags().GetString("request-timeout")
	opts.Timeout, _ = cmd.Flags().GetDuration("timeout")

	permissionModeFlag, _ := cmd.Flags().GetString("permission-mode")
	opts.YoloMode, _ = cmd.Flags().GetBool("yolo")
	permissionMode, err := parseChatPermissionMode(permissionModeFlag, opts.YoloMode)
	if err != nil {
		return nil, newExecExitError(execExitUsage, "INVALID_PERMISSION_MODE", err)
	}
	opts.PermissionMode = permissionMode
	approvalReuseFlag, _ := cmd.Flags().GetString("approval-reuse")
	approvalReuse, err := parseChatApprovalReuseMode(approvalReuseFlag)
	if err != nil {
		return nil, newExecExitError(execExitUsage, "INVALID_APPROVAL_REUSE", err)
	}
	opts.ApprovalReuse = approvalReuse

	opts.DisableTools, _ = cmd.Flags().GetBool("disable-tools")
	opts.CLISkillDirs, _ = cmd.Flags().GetStringSlice("skills-dir")
	opts.CLISkillsTopK, _ = cmd.Flags().GetInt("skills-top-k")
	opts.CLISkillsMode, _ = cmd.Flags().GetString("skills-mode")
	opts.CLISkillsDebug, _ = cmd.Flags().GetBool("skills-debug")
	opts.ConfigOverrides, _ = cmd.Flags().GetStringSlice("config-override")
	opts.HTTPDebug, _ = cmd.Flags().GetBool("debug-http")
	opts.FailFast, _ = cmd.Flags().GetBool("fail-fast")
	if readPrompt {
		opts.Prompt = buildExecPrompt(args, opts.PromptFlag)
	}
	return opts, nil
}

func buildExecPrompt(args []string, promptFlag string) string {
	argPrompt := strings.TrimSpace(strings.Join(args, " "))
	stdinContent, _ := readExecStdinIfPiped()
	promptFlag = strings.TrimSpace(promptFlag)
	switch {
	case argPrompt != "":
		return argPrompt
	case promptFlag != "" && stdinContent != "":
		return fmt.Sprintf("%s\n\n---\n%s", promptFlag, stdinContent)
	case promptFlag != "":
		return promptFlag
	case stdinContent != "":
		return stdinContent
	default:
		return ""
	}
}

func readExecStdinIfPiped() (string, error) {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if stat.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}
	reader := bufio.NewReader(os.Stdin)
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func buildExecSession(cfg *config.Config, opts *ExecOptions, processor ExecEventProcessor) (*ExecSession, func(), error) {
	runtimeMode, runtimeServerURL, err := resolveAICLIRuntimeExecution(cfg, opts.RuntimeServerFlag, opts.RuntimeModeFlag, strings.TrimSpace(opts.RuntimeServerFlag) != "", strings.TrimSpace(opts.RuntimeModeFlag) != "")
	if err != nil {
		return nil, nil, newExecExitError(execExitUsage, "INVALID_RUNTIME_MODE", err)
	}
	opts.RuntimeMode = runtimeMode
	opts.RuntimeServerURL = runtimeServerURL
	chatOpts := buildExecChatOptions(opts)
	profileState, err := resolveChatProfileState(cfg, chatOpts)
	if err != nil {
		return nil, nil, newExecExitError(execExitUsage, "PROFILE_FAILED", err)
	}
	applyProfileDefaultsToChatOptions(chatOpts, profileState)
	chatOpts.SessionFeaturesRequested = strings.TrimSpace(opts.SessionDir) != "" ||
		strings.TrimSpace(opts.SessionTitle) != "" ||
		(profileState != nil && profileState.Active())

	persistenceState, err := prepareExecPersistence(cfg, chatOpts, opts, profileState)
	if err != nil {
		return nil, nil, err
	}
	runtimeState, _, err := prepareChatRuntimeState(cfg, chatOpts, persistenceState.loadedRuntimeSession)
	if err != nil {
		return nil, nil, newExecExitError(execExitUsage, "RUNTIME_CONFIG_FAILED", err)
	}
	chatSession, cleanupSession, err := bootstrapChatSession(cfg, chatOpts, profileState, persistenceState, runtimeState)
	if err != nil {
		return nil, nil, newExecExitError(execExitExecutionFailed, "SESSION_BOOTSTRAP_FAILED", err)
	}
	chatSession.ExecEventBridge = newExecEventBridge(processor)
	session := &ExecSession{
		Options:     opts,
		Config:      cfg,
		Processor:   processor,
		ChatSession: chatSession,
		SessionMgr:  persistenceState.runtimeSessionManager,
		SessionID:   currentRuntimeSessionID(chatSession),
		StartTime:   time.Now(),
	}
	cleanup := func() {
		if cleanupSession != nil {
			cleanupSession()
		}
		if persistenceState.runtimeSessionManager != nil {
			persistenceState.runtimeSessionManager.Stop()
		}
	}
	return session, cleanup, nil
}

func buildExecChatOptions(opts *ExecOptions) *chatCommandOptions {
	return &chatCommandOptions{
		ProfileFlag:            opts.ProfileFlag,
		AgentFlag:              opts.AgentFlag,
		ProviderFlag:           opts.ProviderFlag,
		ModelFlag:              opts.ModelFlag,
		StreamFlag:             opts.StreamFlag,
		StreamChanged:          opts.StreamChanged,
		NoInteractive:          true,
		Message:                opts.Prompt,
		ImagePaths:             opts.ImagePaths,
		RequestTimeoutFlag:     opts.RequestTimeout,
		ReasoningEffortFlag:    opts.ReasoningEffortFlag,
		ReasoningEffortChanged: strings.TrimSpace(opts.ReasoningEffortFlag) != "",
		RuntimeServerFlag:      opts.RuntimeServerFlag,
		RuntimeModeFlag:        opts.RuntimeModeFlag,
		RuntimeMode:            opts.RuntimeMode,
		RuntimeServerURL:       opts.RuntimeServerURL,
		DisableTools:           opts.DisableTools,
		HTTPDebug:              opts.HTTPDebug,
		FailFast:               opts.FailFast,
		CLISkillDirs:           opts.CLISkillDirs,
		CLISkillsTopK:          opts.CLISkillsTopK,
		CLISkillsMode:          opts.CLISkillsMode,
		CLISkillsDebug:         opts.CLISkillsDebug,
		PermissionMode:         opts.PermissionMode,
		ApprovalReuseMode:      opts.ApprovalReuse,
		JSONOutput:             false,
		OutputFlag:             opts.OutputFormat,
		JSONEnvelope:           opts.JSONEnvelope,
		SessionDirFlag:         opts.SessionDir,
		SessionUserFlag:        opts.SessionUser,
		SessionTitleFlag:       opts.SessionTitle,
		ProviderChanged:        strings.TrimSpace(opts.ProviderFlag) != "",
		ModelChanged:           strings.TrimSpace(opts.ModelFlag) != "",
		OutputFormat:           opts.OutputFormat,
		InputReader:            bufio.NewReader(os.Stdin),
	}
}

func prepareExecPersistence(cfg *config.Config, chatOpts *chatCommandOptions, opts *ExecOptions, profileState *chatProfileState) (*chatPersistenceState, error) {
	if opts != nil && opts.Ephemeral {
		if strings.TrimSpace(opts.SessionDir) != "" || strings.TrimSpace(opts.SessionTitle) != "" {
			return nil, newExecExitError(execExitUsage, "EPHEMERAL_SESSION_CONFLICT", fmt.Errorf("--ephemeral 不能与 --session-dir 或 --title 同时使用"))
		}
		return &chatPersistenceState{}, nil
	}
	return prepareChatPersistence(cfg, chatOpts, profileState)
}

func executeExecWithSignals(session *ExecSession) error {
	if session == nil || session.Options == nil || session.ChatSession == nil {
		return newExecExitError(execExitExecutionFailed, "INVALID_SESSION", fmt.Errorf("exec session is nil"))
	}
	ctx, cancel := context.WithCancel(context.Background())
	if session.Options.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, session.Options.Timeout)
	}
	defer cancel()
	session.ChatSession.cancelCtx = ctx
	session.ChatSession.cancelFunc = cancel

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	go func() {
		<-sigChan
		session.ChatSession.interrupted.Store(true)
		cancel()
	}()
	return executeExec(ctx, session)
}

func executeExec(ctx context.Context, session *ExecSession) error {
	opts := session.Options
	processor := session.Processor
	chatSession := session.ChatSession
	processor.PrintConfigSummary(opts, chatSession.Model, chatSession.ProviderName)

	threadID := generateThreadID()
	processor.OnThreadStarted(ThreadStartedEvent{
		ThreadID:  threadID,
		SessionID: currentRuntimeSessionID(chatSession),
		Model:     chatSession.Model,
		Provider:  chatSession.ProviderName,
		Ephemeral: opts.Ephemeral,
	})
	if strings.TrimSpace(opts.Prompt) == "" {
		err := fmt.Errorf("未提供提示词。请通过参数、-p 或 stdin 提供提示词")
		processor.OnError(ErrorEvent{Message: err.Error(), Code: "NO_PROMPT"})
		return newExecExitError(execExitUsage, "NO_PROMPT", err)
	}

	turnID := generateTurnID()
	processor.OnTurnStarted(TurnStartedEvent{TurnID: turnID, Prompt: opts.Prompt})
	started := time.Now()
	response, err := sendMessage(chatSession, opts.Prompt)
	duration := time.Since(started)
	if err != nil {
		return handleExecTurnError(ctx, processor, turnID, duration, opts, err)
	}
	if strings.TrimSpace(opts.OutputSchema) != "" {
		if err := validateExecFinalMessageSchema(opts.OutputSchema, response); err != nil {
			processor.OnTurnFailed(TurnFailedEvent{TurnID: turnID, Error: err.Error(), Code: "SCHEMA_VALIDATION_FAILED"})
			processor.OnError(ErrorEvent{Message: err.Error(), Code: "SCHEMA_VALIDATION_FAILED"})
			return newExecExitError(execExitSchemaFailed, "SCHEMA_VALIDATION_FAILED", err)
		}
	}
	usage := execTokenUsage(chatSession)
	processor.OnTurnCompleted(TurnCompletedEvent{
		TurnID:     turnID,
		Status:     "completed",
		Usage:      usage,
		DurationMs: duration.Milliseconds(),
	})
	result := ExecFinalResult{
		Status:     "completed",
		Message:    response,
		SessionID:  currentRuntimeSessionID(chatSession),
		Model:      chatSession.Model,
		Provider:   chatSession.ProviderName,
		Usage:      usage,
		DurationMs: duration.Milliseconds(),
	}
	processor.SetFinalResult(result)
	if err := processor.PrintFinalOutput(opts); err != nil {
		return newExecExitError(execExitExecutionFailed, "OUTPUT_FAILED", err)
	}
	flushExecSession(chatSession)
	finalizeChatSession(chatSession)
	return nil
}

func handleExecTurnError(ctx context.Context, processor ExecEventProcessor, turnID string, duration time.Duration, opts *ExecOptions, err error) error {
	code := "EXECUTION_FAILED"
	exitCode := execExitExecutionFailed
	switch {
	case ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		code = "TIMEOUT"
		exitCode = execExitTimeout
	case ctx != nil && errors.Is(ctx.Err(), context.Canceled):
		code = "INTERRUPTED"
		exitCode = execExitInterrupted
		processor.OnTurnCompleted(TurnCompletedEvent{TurnID: turnID, Status: "interrupted", DurationMs: duration.Milliseconds()})
		processor.OnError(ErrorEvent{Message: err.Error(), Code: code})
		return newExecExitError(exitCode, code, err)
	}
	processor.OnTurnFailed(TurnFailedEvent{TurnID: turnID, Error: err.Error(), Code: code})
	processor.OnError(ErrorEvent{Message: err.Error(), Code: code})
	return newExecExitError(exitCode, code, err)
}

func execTokenUsage(session *ChatSession) TokenUsage {
	if session == nil {
		return TokenUsage{}
	}
	outputTokens := session.TokenCount - session.ContextTokenCount
	if outputTokens < 0 {
		outputTokens = 0
	}
	return TokenUsage{
		InputTokens:  session.ContextTokenCount,
		OutputTokens: outputTokens,
		TotalTokens:  session.TokenCount,
	}
}

func flushExecSession(session *ChatSession) {
	if session == nil || session.Logger == nil {
		return
	}
	if session.Logger.logDir != "" {
		if err := session.Logger.FlushSession(); err != nil {
			writeChatLogSaveError(session, err)
		}
		return
	}
	writeChatLogBufferedMarker(session)
}
