package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func newExecResumeCommand(getCfg func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resume [SESSION_ID] [PROMPT]",
		Short: "恢复之前的会话",
		Long: `恢复指定会话，或使用 --last 恢复最近会话。

没有 PROMPT 时只输出最后一条 assistant 消息，不发送新 turn。`,
		Example: `  aicli exec resume --last
  aicli exec resume <session-id>
  aicli exec resume --last "继续上次的任务"`,
		Run: func(cmd *cobra.Command, args []string) {
			if err := runExecResume(cmd, getCfg(), args); err != nil {
				exitExecCommandError(cmd, err)
			}
		},
	}
	cmd.Flags().Bool("last", false, "恢复最近的会话")
	cmd.Flags().Bool("all", false, "显示所有会话（保留兼容，首版不改变筛选策略）")
	cmd.Flags().StringSliceP("image", "i", nil, "附加图片文件路径")
	cmd.Flags().StringP("prompt", "p", "", "恢复后发送的提示词")
	registerExecSharedFlags(cmd, map[string]bool{"prompt": true, "image": true})
	return cmd
}

func runExecResume(cmd *cobra.Command, cfg *config.Config, args []string) error {
	last, _ := cmd.Flags().GetBool("last")
	images, _ := cmd.Flags().GetStringSlice("image")
	prompt, _ := cmd.Flags().GetString("prompt")
	ephemeral, _ := cmd.Flags().GetBool("ephemeral")
	if ephemeral {
		return newExecExitError(execExitUsage, "RESUME_EPHEMERAL_UNSUPPORTED", fmt.Errorf("exec resume 不支持 --ephemeral"))
	}

	var sessionID string
	switch {
	case last:
		if len(args) > 0 {
			prompt = strings.Join(args, " ")
		}
	case len(args) > 0:
		sessionID = args[0]
		if len(args) > 1 {
			prompt = strings.Join(args[1:], " ")
		}
	default:
		return newExecExitError(execExitUsage, "NO_SESSION", fmt.Errorf("请指定 SESSION_ID，或使用 --last 恢复最近会话"))
	}

	opts, err := parseExecOptionsNoPrompt(cmd)
	if err != nil {
		return err
	}
	opts.Prompt = strings.TrimSpace(prompt)
	opts.ImagePaths = images
	opts.Ephemeral = false
	opts.Command = "resume"
	opts.ResumeArgs = &ExecResumeArgs{
		SessionID: sessionID,
		Last:      last,
		Prompt:    opts.Prompt,
		Images:    images,
	}
	processor := NewExecEventProcessor(opts.JSONMode, nil, opts.OutputLastMsg)
	session, cleanup, err := buildExecSessionWithResume(cfg, opts, processor)
	if err != nil {
		return err
	}
	defer cleanup()
	if strings.TrimSpace(opts.Prompt) == "" {
		return printExecResumeSummary(session)
	}
	return executeExecWithSignals(session)
}

func buildExecSessionWithResume(cfg *config.Config, opts *ExecOptions, processor ExecEventProcessor) (*ExecSession, func(), error) {
	if opts == nil || opts.ResumeArgs == nil {
		return nil, nil, newExecExitError(execExitUsage, "INVALID_RESUME", fmt.Errorf("resume args are required"))
	}
	chatOpts := buildExecChatOptions(opts)
	chatOpts.ResumeFlag = opts.ResumeArgs.Last
	chatOpts.SessionIDFlag = opts.ResumeArgs.SessionID
	chatOpts.SessionFeaturesRequested = true

	profileState, err := resolveChatProfileState(cfg, chatOpts)
	if err != nil {
		return nil, nil, newExecExitError(execExitUsage, "PROFILE_FAILED", err)
	}
	applyProfileDefaultsToChatOptions(chatOpts, profileState)
	persistenceState, err := prepareChatPersistence(chatOpts)
	if err != nil {
		return nil, nil, newExecExitError(execExitUsage, "RESUME_LOAD_FAILED", err)
	}
	if persistenceState.loadedRuntimeSession == nil {
		return nil, nil, newExecExitError(execExitUsage, "SESSION_NOT_FOUND", fmt.Errorf("未找到可恢复的会话"))
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

func printExecResumeSummary(session *ExecSession) error {
	if session == nil || session.ChatSession == nil {
		return newExecExitError(execExitExecutionFailed, "INVALID_SESSION", fmt.Errorf("会话未初始化"))
	}
	chatSession := session.ChatSession
	message := lastAssistantMessage(chatSession)
	processor := session.Processor
	threadID := generateThreadID()
	processor.OnThreadStarted(ThreadStartedEvent{
		ThreadID:  threadID,
		SessionID: currentRuntimeSessionID(chatSession),
		Model:     chatSession.Model,
		Provider:  chatSession.ProviderName,
	})
	processor.OnTurnCompleted(TurnCompletedEvent{TurnID: generateTurnID(), Status: "resumed", Usage: execTokenUsage(chatSession)})
	processor.SetFinalResult(ExecFinalResult{
		Status:    "resumed",
		Message:   message,
		SessionID: currentRuntimeSessionID(chatSession),
		Model:     chatSession.Model,
		Provider:  chatSession.ProviderName,
		Usage:     execTokenUsage(chatSession),
	})
	return processor.PrintFinalOutput(session.Options)
}

func lastAssistantMessage(session *ChatSession) string {
	if session == nil {
		return ""
	}
	messages := session.Messages
	if len(messages) == 0 && session.RuntimeSession != nil {
		messages = session.RuntimeSession.GetMessages()
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if isAssistantRuntimeMessage(messages[i]) && strings.TrimSpace(messages[i].Content) != "" {
			return strings.TrimSpace(messages[i].Content)
		}
	}
	return ""
}

func isAssistantRuntimeMessage(message runtimetypes.Message) bool {
	return strings.EqualFold(strings.TrimSpace(message.Role), "assistant")
}
