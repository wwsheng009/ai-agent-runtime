package commands

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/spf13/cobra"
)

type chatCommandOptions struct {
	ProfileFlag              string
	AgentFlag                string
	ProviderFlag             string
	ModelFlag                string
	StreamFlag               bool
	StreamChanged            bool
	NoInteractive            bool
	Message                  string
	ImagePaths               []string
	LogDir                   string
	RequestTimeoutFlag       string
	ReasoningEffortFlag      string
	ReasoningEffortChanged   bool
	DisableTools             bool
	HTTPDebug                bool
	FailFast                 bool
	CLISkillDirs             []string
	CLISkillsTopK            int
	CLISkillsMode            string
	CLISkillsDebug           bool
	PermissionMode           runtimepolicy.Mode
	ApprovalReuseMode        chatApprovalReuseMode
	JSONOutput               bool
	OutputFlag               string
	JSONEnvelope             bool
	SessionIDFlag            string
	ResumeFlag               bool
	ListSessionsFlag         bool
	SessionDirFlag           string
	SessionTitleFlag         string
	SessionStateFlag         string
	SessionProviderFilter    string
	SessionModelFilter       string
	SessionQueryFlag         string
	SessionLimitFlag         int
	ProviderChanged          bool
	ModelChanged             bool
	OutputFormat             string
	InputReader              *bufio.Reader
	SessionFilter            ChatSessionListFilter
	SessionFeaturesRequested bool
}

func parseChatCommandOptions(cmd *cobra.Command, cfg *config.Config) (*chatCommandOptions, error) {
	if cmd == nil {
		return nil, fmt.Errorf("chat command is nil")
	}

	providerFlag, _ := cmd.Flags().GetString("provider")
	modelFlag, _ := cmd.Flags().GetString("model")
	profileFlag, _ := cmd.Flags().GetString("profile")
	agentFlag, _ := cmd.Flags().GetString("agent")
	streamFlag, _ := cmd.Flags().GetBool("stream")
	noInteractive, _ := cmd.Flags().GetBool("no-interactive")
	message, _ := cmd.Flags().GetString("message")
	imagePaths, _ := cmd.Flags().GetStringSlice("image")
	logDir, _ := cmd.Flags().GetString("log-dir")
	requestTimeoutFlag, _ := cmd.Flags().GetString("request-timeout")
	reasoningEffortFlag, _ := cmd.Flags().GetString("reasoning-effort")
	disableTools, _ := cmd.Flags().GetBool("disable-tools")
	httpDebug, _ := cmd.Flags().GetBool("debug-http")
	failFast, _ := cmd.Flags().GetBool("fail-fast")
	cliSkillDirs, _ := cmd.Flags().GetStringSlice("skills-dir")
	cliSkillsTopK, _ := cmd.Flags().GetInt("skills-top-k")
	cliSkillsMode, _ := cmd.Flags().GetString("skills-mode")
	cliSkillsDebug, _ := cmd.Flags().GetBool("skills-debug")
	permissionModeFlag, _ := cmd.Flags().GetString("permission-mode")
	approvalReuseFlag, _ := cmd.Flags().GetString("approval-reuse")
	yoloFlag, _ := cmd.Flags().GetBool("yolo")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	outputFlag, _ := cmd.Flags().GetString("output")
	sessionIDFlag, _ := cmd.Flags().GetString("session")
	resumeFlag, _ := cmd.Flags().GetBool("resume")
	listSessionsFlag, _ := cmd.Flags().GetBool("list-sessions")
	sessionDirFlag, _ := cmd.Flags().GetString("session-dir")
	sessionTitleFlag, _ := cmd.Flags().GetString("title")
	sessionStateFlag, _ := cmd.Flags().GetString("session-state")
	sessionProviderFilterFlag, _ := cmd.Flags().GetString("session-provider")
	sessionModelFilterFlag, _ := cmd.Flags().GetString("session-model")
	sessionQueryFlag, _ := cmd.Flags().GetString("session-query")
	sessionLimitFlag, _ := cmd.Flags().GetInt("session-limit")

	outputFormat, err := resolveChatOutputFormat(noInteractive, outputFlag, jsonOutput)
	if err != nil {
		return nil, err
	}
	permissionMode, err := parseChatPermissionMode(permissionModeFlag, yoloFlag)
	if err != nil {
		return nil, err
	}
	approvalReuseMode, err := parseChatApprovalReuseMode(approvalReuseFlag)
	if err != nil {
		return nil, err
	}

	sessionFilter := ChatSessionListFilter{
		State:    runtimechat.SessionState(strings.ToLower(strings.TrimSpace(sessionStateFlag))),
		Provider: strings.TrimSpace(sessionProviderFilterFlag),
		Model:    strings.TrimSpace(sessionModelFilterFlag),
		Query:    strings.TrimSpace(sessionQueryFlag),
		Limit:    sessionLimitFlag,
	}
	if sessionFilter.Protocol == "" && cmd.Flags().Changed("provider") && cfg != nil {
		if provider, ok := cfg.Providers.Items[strings.TrimSpace(providerFlag)]; ok {
			sessionFilter.Protocol = provider.GetProtocol()
		}
	}

	return &chatCommandOptions{
		ProfileFlag:            profileFlag,
		AgentFlag:              agentFlag,
		ProviderFlag:           providerFlag,
		ModelFlag:              modelFlag,
		StreamFlag:             streamFlag,
		StreamChanged:          cmd.Flags().Changed("stream"),
		NoInteractive:          noInteractive,
		Message:                message,
		ImagePaths:             imagePaths,
		LogDir:                 logDir,
		RequestTimeoutFlag:     requestTimeoutFlag,
		ReasoningEffortFlag:    reasoningEffortFlag,
		ReasoningEffortChanged: cmd.Flags().Changed("reasoning-effort"),
		DisableTools:           disableTools,
		HTTPDebug:              httpDebug,
		FailFast:               failFast,
		CLISkillDirs:           cliSkillDirs,
		CLISkillsTopK:          cliSkillsTopK,
		CLISkillsMode:          cliSkillsMode,
		CLISkillsDebug:         cliSkillsDebug,
		PermissionMode:         permissionMode,
		ApprovalReuseMode:      approvalReuseMode,
		JSONOutput:             jsonOutput,
		OutputFlag:             outputFlag,
		JSONEnvelope:           useJSONEnvelope(cmd),
		SessionIDFlag:          sessionIDFlag,
		ResumeFlag:             resumeFlag,
		ListSessionsFlag:       listSessionsFlag,
		SessionDirFlag:         sessionDirFlag,
		SessionTitleFlag:       sessionTitleFlag,
		SessionStateFlag:       sessionStateFlag,
		SessionProviderFilter:  sessionProviderFilterFlag,
		SessionModelFilter:     sessionModelFilterFlag,
		SessionQueryFlag:       sessionQueryFlag,
		SessionLimitFlag:       sessionLimitFlag,
		ProviderChanged:        cmd.Flags().Changed("provider"),
		ModelChanged:           cmd.Flags().Changed("model"),
		OutputFormat:           outputFormat,
		InputReader:            bufio.NewReader(os.Stdin),
		SessionFilter:          sessionFilter,
		SessionFeaturesRequested: listSessionsFlag || resumeFlag || strings.TrimSpace(sessionIDFlag) != "" || strings.TrimSpace(sessionDirFlag) != "" ||
			sessionFilter.State != "" || sessionFilter.Provider != "" || sessionFilter.Model != "" || sessionFilter.Query != "",
	}, nil
}

func resolveChatProviderName(cfg *config.Config, opts *chatCommandOptions, loadedRuntimeSession *runtimechat.Session) string {
	if opts == nil {
		return ""
	}

	providerName := opts.ProviderFlag
	if !opts.ProviderChanged && loadedRuntimeSession != nil {
		if storedProvider := runtimeSessionContextString(loadedRuntimeSession, chatRuntimeContextProviderName); storedProvider != "" {
			providerName = storedProvider
		}
	}
	if providerName == "" && !opts.NoInteractive {
		providerName = selectProviderWithReader(cfg, chatOptionInputReader(opts))
	}
	if providerName == "" && cfg != nil {
		providerName = cfg.Providers.DefaultProvider
	}
	return providerName
}

func resolveChatModelName(provider config.Provider, opts *chatCommandOptions, loadedRuntimeSession *runtimechat.Session) string {
	if opts == nil {
		return provider.DefaultModel
	}

	modelName := opts.ModelFlag
	if !opts.ModelChanged && loadedRuntimeSession != nil {
		if storedModel := runtimeSessionContextString(loadedRuntimeSession, chatRuntimeContextModel); storedModel != "" {
			modelName = storedModel
		}
	}
	if modelName == "" && !opts.NoInteractive {
		modelName = selectModelWithReader(provider, chatOptionInputReader(opts))
	}
	if modelName == "" {
		modelName = provider.DefaultModel
	}
	return modelName
}

func resolveChatStreamMode(opts *chatCommandOptions, loadedRuntimeSession *runtimechat.Session) bool {
	if opts == nil {
		return false
	}

	shouldStream := opts.StreamFlag
	if !opts.StreamChanged && loadedRuntimeSession != nil {
		if storedStream, ok := runtimeSessionContextBool(loadedRuntimeSession, chatRuntimeContextStream); ok {
			shouldStream = storedStream
		}
	}
	if !opts.StreamChanged && loadedRuntimeSession == nil && !opts.NoInteractive {
		shouldStream = selectStreamModeWithReader(chatOptionInputReader(opts))
	}
	return shouldStream
}
