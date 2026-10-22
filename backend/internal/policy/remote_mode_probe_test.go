package policy

import (
	"testing"

	"github.com/stretchr/testify/require"

	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetype "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// remoteMCPTool builds the ToolInfo a real MCP adapter advertises for a remote
// tool: prefixed callable name, plus the identity metadata withMCPToolIdentity
// stamps from the server's own tool list.
func remoteMCPTool(prefixed, rawTool string) runtimeskill.ToolInfo {
	return runtimeskill.ToolInfo{
		Name:           prefixed,
		MCPName:        "prod",
		ExecutionScope: "remote",
		ExecutionMode:  "remote_mcp",
		MCPTrustLevel:  "trusted_remote",
		Metadata: map[string]interface{}{
			"mcp_name":          "prod",
			"mcp_raw_tool_name": rawTool,
		},
	}
}

func resolveRemote(prefixed, rawTool string) []Capability {
	tool := remoteMCPTool(prefixed, rawTool)
	return DefaultCapabilityResolver{}.Resolve(EvalRequest{ToolName: prefixed, ToolInfo: &tool})
}

// localCaps resolves a bare tool name with no MCP identity, the way a builtin or
// local toolkit tool reaches the resolver.
func localCaps(name string) []Capability {
	return DefaultCapabilityResolver{}.Resolve(EvalRequest{ToolName: name})
}

// TestRemoteToolsClassifyByDeclaredName locks the core fix: a prefixed remote
// tool is classified by the bare tool name its server declared, so it lands in the
// same capability bucket as the local equivalent and the permission mode can gate
// it identically.
func TestRemoteToolsClassifyByDeclaredName(t *testing.T) {
	cases := []struct {
		prefixed, raw string
		want          []Capability
	}{
		{"remote_prod__view", "view", []Capability{CapReadOnly}},
		{"remote_prod__write", "write", []Capability{CapWriteFS}},
		{"remote_prod__shell", "shell", []Capability{CapExecShell}},
		// job_start used to fall through to read_only (no write/shell marker in the
		// name); classified by its declared name it now reaches the control-plane
		// table and is gated as a background task.
		{"remote_prod__job_start", "job_start", []Capability{CapBackgroundTask}},
		{"remote_prod__job_input", "job_input", []Capability{CapBackgroundTask}},
	}
	for _, tc := range cases {
		got := resolveRemote(tc.prefixed, tc.raw)
		t.Logf("%-22s (raw=%-10s) -> %v", tc.prefixed, tc.raw, got)
		require.ElementsMatch(t, tc.want, got, tc.prefixed)
	}
}

// TestRemoteBackgroundTaskIsNotPlanAllowed proves the reclassification actually
// changes the mode verdict: a remote job_start must not be waved through by plan
// mode, which only allows read_only/ask_user capabilities.
func TestRemoteBackgroundTaskIsNotPlanAllowed(t *testing.T) {
	caps := resolveRemote("remote_prod__job_start", "job_start")
	require.Equal(t, modeDecision(ModePlan, caps), DecisionDeny, "plan mode must deny a remote background task")
	require.Equal(t, modeDecision(ModeDontAsk, caps), DecisionDeny)
	require.Equal(t, modeDecision(ModeDefault, caps), DecisionAsk)
}

// TestRemoteToolWithoutIdentityStillUsesHeuristics proves the fallback is
// unchanged: an MCP tool whose server gave no raw name keeps the prior behaviour
// instead of being silently reclassified or dropped.
func TestRemoteToolWithoutIdentityStillUsesHeuristics(t *testing.T) {
	tool := runtimeskill.ToolInfo{
		Name:           "some_vendor__run_job",
		MCPName:        "some_vendor",
		ExecutionScope: "remote",
	}
	caps := DefaultCapabilityResolver{}.Resolve(EvalRequest{ToolName: tool.Name, ToolInfo: &tool})
	// No mcp_raw_tool_name -> the prefixed name is classified as-is. run_job is not
	// write/shell-marked, so it keeps the read_only default. Nothing throws, nothing
	// is empty.
	require.NotEmpty(t, caps)
	require.Contains(t, caps, CapReadOnly)
}

// TestRemoteToolMetadataKindWins ensures an explicit taxonomy in metadata is never
// overridden by the raw-name substitution: the prefixed spelling's own evidence
// keeps priority.
func TestRemoteToolMetadataKindWins(t *testing.T) {
	tool := runtimeskill.ToolInfo{
		Name:           "remote_prod__thing",
		MCPName:        "prod",
		ExecutionScope: "remote",
		Metadata: map[string]interface{}{
			"mcp_raw_tool_name":   "view",
			runtimetype.ToolMetadataKindKey: runtimetype.ToolKindEdit,
		},
	}
	caps := DefaultCapabilityResolver{}.Resolve(EvalRequest{ToolName: tool.Name, ToolInfo: &tool})
	require.Contains(t, caps, CapWriteFS, "explicit metadata kind must classify the tool, not the raw name")
}

// TestRemoteAndLocalClassifyIdentically is the actual acceptance test for the
// user-facing rule: a remote tool must be governed exactly like its local
// counterpart, with no extra per-target switch to discover.
//
// It compares the prefixed remote spelling (carrying the server's declared raw
// name) against the bare local name. Any divergence means the permission mode
// would answer differently for the same capability on the two sides.
func TestRemoteAndLocalClassifyIdentically(t *testing.T) {
	// The bare local spelling resolves with no MCP identity metadata at all, which
	// is how a builtin/local toolkit tool reaches the resolver.
	local := func(name string) []Capability {
		return DefaultCapabilityResolver{}.Resolve(EvalRequest{ToolName: name})
	}

	remoteOnly := []string{"job_start", "job_output", "job_cancel", "apply_patch", "glob", "grep", "ls", "view", "write", "edit", "shell"}
	for _, raw := range remoteOnly {
		want := local(raw)
		got := resolveRemote("remote_prod__"+raw, raw)
		require.ElementsMatch(t, want, got,
			"remote %s must classify exactly like local %s", raw, raw)
		require.NotEmpty(t, want, "local %s must not resolve to nothing", raw)
	}
}

// TestRemoteJobStartNeedsBackgroundTask pins the specific escalation that the
// per-target layer used to cover by hand and that is now covered by taxonomy:
// starting a process on another machine is a background task, not a read.
func TestRemoteJobStartNeedsBackgroundTask(t *testing.T) {
	caps := resolveRemote("remote_prod__job_start", "job_start")
	require.Contains(t, caps, CapBackgroundTask)
	require.NotContains(t, caps, CapReadOnly,
		"job_start must not be classified as a read, or plan mode would allow it")

	// Same verdict as the local equivalent, so one mode governs both.
	require.Equal(t,
		modeDecision(ModePlan, localCaps("job_start")),
		modeDecision(ModePlan, caps),
	)
}
