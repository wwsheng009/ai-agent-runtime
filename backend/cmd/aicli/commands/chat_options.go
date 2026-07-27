package commands

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

type chatCommandOptions struct {
	ProfileFlag   string
	AgentFlag     string
	ProviderFlag  string
	ModelFlag     string
	StreamFlag    bool
	StreamChanged bool
	// FastFlag / FastChanged mirror Stream: CLI override for Codex Fast mode.
	// Effective only when the resolved provider protocol is codex.
	FastFlag               bool
	FastChanged            bool
	NoInteractive          bool
	Message                string
	ImagePaths             []string
	LogDir                 string
	RequestTimeoutFlag     string
	ReasoningEffortFlag    string
	ReasoningEffortChanged bool
	DisableTools           bool
	HTTPDebug              bool
	FailFast               bool
	CLISkillDirs           []string
	CLISkillsTopK          int
	CLISkillsMode          string
	CLISkillsDebug         bool
	PermissionMode         runtimepolicy.Mode
	PermissionModeChanged  bool
	// CLIAllowTools / CLIDenyTools are product-facing permission overlays
	// (--allow-tool / --deny-tool). Applied after profile tool policy.
	CLIAllowTools []string
	CLIDenyTools  []string
	// TrustGrant is CLI --trust: durable grant of the current workspace before decide.
	TrustGrant               bool
	ApprovalReuseMode        chatApprovalReuseMode
	JSONOutput               bool
	OutputFlag               string
	JSONEnvelope             bool
	SessionIDFlag            string
	ResumeFlag               bool
	ListSessionsFlag         bool
	SessionDirFlag           string
	SessionUserFlag          string
	SessionTitleFlag         string
	SessionStateFlag         string
	SessionProviderFilter    string
	SessionModelFilter       string
	SessionCurrentDirOnly    bool
	SessionQueryFlag         string
	SessionLimitFlag         int
	RuntimeServerFlag        string
	RuntimeModeFlag          string
	RuntimeMode              string
	RuntimeServerURL         string
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
	fastFlag := false
	fastChanged := false
	if cmd.Flags().Lookup("fast") != nil {
		fastFlag, _ = cmd.Flags().GetBool("fast")
		fastChanged = cmd.Flags().Changed("fast")
	}
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
	cliAllowTools, _ := cmd.Flags().GetStringSlice("allow-tool")
	cliDenyTools, _ := cmd.Flags().GetStringSlice("deny-tool")
	trustGrant := false
	if cmd.Flags().Lookup("trust") != nil {
		trustGrant, _ = cmd.Flags().GetBool("trust")
	}
	yoloFlag, _ := cmd.Flags().GetBool("yolo")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	outputFlag, _ := cmd.Flags().GetString("output")
	sessionIDFlag, _ := cmd.Flags().GetString("session")
	resumeFlag, _ := cmd.Flags().GetBool("resume")
	listSessionsFlag, _ := cmd.Flags().GetBool("list-sessions")
	sessionDirFlag, _ := cmd.Flags().GetString("session-dir")
	sessionUserFlag, _ := cmd.Flags().GetString("user")
	sessionTitleFlag, _ := cmd.Flags().GetString("title")
	sessionStateFlag, _ := cmd.Flags().GetString("session-state")
	sessionProviderFilterFlag, _ := cmd.Flags().GetString("session-provider")
	sessionModelFilterFlag, _ := cmd.Flags().GetString("session-model")
	sessionCurrentDirOnly, _ := cmd.Flags().GetBool("cwd")
	sessionWorkspaceFilterExplicit := sessionCurrentDirOnly && cmd.Flags().Changed("cwd")
	sessionQueryFlag, _ := cmd.Flags().GetString("session-query")
	sessionLimitFlag, _ := cmd.Flags().GetInt("session-limit")
	runtimeServerFlag, _ := cmd.Flags().GetString("runtime-server")
	runtimeModeFlag, _ := cmd.Flags().GetString("runtime-mode")

	outputFormat, err := resolveChatOutputFormat(noInteractive, outputFlag, jsonOutput)
	if err != nil {
		return nil, err
	}
	runtimeMode, runtimeServerURL, err := resolveAICLIRuntimeExecution(cfg, runtimeServerFlag, runtimeModeFlag, cmd.Flags().Changed("runtime-server"), cmd.Flags().Changed("runtime-mode"))
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
	if sessionCurrentDirOnly {
		workspace := strings.TrimSpace(resolveLocalWorkspacePath(loadRuntimeToolConfig(cfg, nil), nil))
		if workspace == "" {
			currentDir, cwdErr := os.Getwd()
			if cwdErr != nil {
				return nil, fmt.Errorf("获取当前工作目录失败: %w", cwdErr)
			}
			workspace = currentDir
		}
		sessionFilter.Workspace = normalizeChatSessionWorkspace(workspace)
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
		FastFlag:               fastFlag,
		FastChanged:            fastChanged,
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
		PermissionModeChanged:  cmd.Flags().Changed("permission-mode") || yoloFlag,
		CLIAllowTools:          append([]string(nil), cliAllowTools...),
		CLIDenyTools:           append([]string(nil), cliDenyTools...),
		TrustGrant:             trustGrant,
		ApprovalReuseMode:      approvalReuseMode,
		JSONOutput:             jsonOutput,
		OutputFlag:             outputFlag,
		JSONEnvelope:           useJSONEnvelope(cmd),
		SessionIDFlag:          sessionIDFlag,
		ResumeFlag:             resumeFlag,
		ListSessionsFlag:       listSessionsFlag,
		SessionDirFlag:         sessionDirFlag,
		SessionUserFlag:        sessionUserFlag,
		SessionTitleFlag:       sessionTitleFlag,
		SessionStateFlag:       sessionStateFlag,
		SessionProviderFilter:  sessionProviderFilterFlag,
		SessionModelFilter:     sessionModelFilterFlag,
		SessionCurrentDirOnly:  sessionCurrentDirOnly,
		SessionQueryFlag:       sessionQueryFlag,
		SessionLimitFlag:       sessionLimitFlag,
		RuntimeServerFlag:      runtimeServerFlag,
		RuntimeModeFlag:        runtimeModeFlag,
		RuntimeMode:            runtimeMode,
		RuntimeServerURL:       runtimeServerURL,
		ProviderChanged:        cmd.Flags().Changed("provider"),
		ModelChanged:           cmd.Flags().Changed("model"),
		OutputFormat:           outputFormat,
		InputReader:            bufio.NewReader(os.Stdin),
		SessionFilter:          sessionFilter,
		SessionFeaturesRequested: listSessionsFlag || resumeFlag || strings.TrimSpace(sessionIDFlag) != "" || strings.TrimSpace(sessionDirFlag) != "" || strings.TrimSpace(sessionUserFlag) != "" ||
			sessionFilter.State != "" || sessionFilter.Provider != "" || sessionFilter.Model != "" || sessionWorkspaceFilterExplicit || sessionFilter.Query != "",
	}, nil
}

func resolveChatProviderName(cfg *config.Config, opts *chatCommandOptions, loadedRuntimeSession *runtimechat.Session) string {
	providerName, _ := resolveChatProviderChoice(cfg, opts, loadedRuntimeSession)
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
	stream, _ := resolveChatStreamChoice(nil, opts, loadedRuntimeSession)
	return stream
}

// resolveChatFastModeChoice restores Fast mode preference.
// Priority: --fast flag > session metadata > config aicli.chat.fast_mode > default false.
// Fast is only effective for protocol=codex; callers gate request/status on protocol.
func resolveChatFastModeChoice(cfg *config.Config, opts *chatCommandOptions, loadedRuntimeSession *runtimechat.Session) bool {
	if opts != nil && opts.FastChanged {
		return opts.FastFlag
	}
	if loadedRuntimeSession != nil {
		if stored, ok := runtimeSessionContextBool(loadedRuntimeSession, chatRuntimeContextFastMode); ok {
			return stored
		}
	}
	if cfg != nil && cfg.AICLI != nil && cfg.AICLI.Chat != nil && cfg.AICLI.Chat.FastMode != nil {
		return *cfg.AICLI.Chat.FastMode
	}
	return false
}

func resolveChatStreamChoice(cfg *config.Config, opts *chatCommandOptions, loadedRuntimeSession *runtimechat.Session) (bool, chatPreferenceSource) {
	if opts == nil {
		return false, chatPreferenceSourceDefault
	}

	if opts.StreamChanged {
		return opts.StreamFlag, chatPreferenceSourceFlag
	}

	if loadedRuntimeSession != nil {
		if storedStream, ok := runtimeSessionContextBool(loadedRuntimeSession, chatRuntimeContextStream); ok {
			return storedStream, chatPreferenceSourceSession
		}
	}

	if cfg != nil && cfg.AICLI != nil && cfg.AICLI.Chat != nil && cfg.AICLI.Chat.Stream != nil {
		return *cfg.AICLI.Chat.Stream, chatPreferenceSourceConfig
	}

	if loadedRuntimeSession == nil && !opts.NoInteractive {
		return selectStreamModeWithReader(chatOptionInputReader(opts)), chatPreferenceSourceInteractive
	}

	return opts.StreamFlag, chatPreferenceSourceDefault
}
