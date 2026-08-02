package commands

import (
	"time"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

type ExecOptions struct {
	Prompt        string
	PromptFlag    string
	ProfileFlag   string
	AgentFlag     string
	ProviderFlag  string
	ModelFlag     string
	MaxTokens     int
	StreamFlag    bool
	StreamChanged bool
	// FastFlag / FastChanged mirror Stream: CLI override for Codex Fast mode.
	// Effective only when the resolved provider protocol is codex.
	FastFlag            bool
	FastChanged         bool
	ReasoningEffortFlag string
	RuntimeModeFlag     string
	RuntimeServerFlag   string
	RuntimeMode         string
	RuntimeServerURL    string

	JSONMode      bool
	OutputFormat  string
	OutputLastMsg string
	OutputSchema  string
	JSONEnvelope  bool

	Ephemeral      bool
	LogDir         string
	SessionDir     string
	SessionUser    string
	SessionTitle   string
	ImagePaths     []string
	RequestTimeout string
	Timeout        time.Duration

	PermissionMode runtimepolicy.Mode
	ApprovalReuse  chatApprovalReuseMode
	YoloMode       bool
	// CLIAllowTools / CLIDenyTools from --allow-tool / --deny-tool product surface.
	CLIAllowTools []string
	CLIDenyTools  []string
	// TrustGrant is CLI --trust for folder trust durable grant (R2).
	TrustGrant bool

	DisableTools   bool
	EnableTools    bool
	CLISkillDirs   []string
	CLISkillsTopK  int
	CLISkillsMode  string
	CLISkillsDebug bool

	ConfigOverrides []string

	HTTPDebug bool
	FailFast  bool

	Command    string
	ResumeArgs *ExecResumeArgs
	ReviewArgs *ExecReviewArgs
}

type ExecResumeArgs struct {
	SessionID string
	Last      bool
	All       bool
	Prompt    string
	Images    []string
}

type ExecReviewArgs struct {
	Uncommitted bool
	BaseBranch  string
	CommitSHA   string
	CommitTitle string
	Prompt      string
}
