//go:build ignore

// Command permissionlayers shows how the remaining permission layers combine for
// the targets in one `remote-targets.yaml`, by evaluating the real code paths on
// a real configuration.
//
// Layers (2026-10-22, after the local per-target grant was removed):
//
//	L1 endpoint auth   bearer token / client certificate      (target side; HTTP 401)
//	L2 capability      profile + tools + readOnly             (target side: what tools/list returns)
//	L3 session mode    --permission-mode / --allow-tool / --deny-tool  (this command)
//	L4 target policy   workspace jail + allowedCommands       (target side, per call)
//
// There is deliberately no per-target allowRead/allowWrite/allowShell table any
// more: a remote tool is classified by its own attributes and gated by the
// session mode exactly like a local tool, so the mode answer IS the L3 answer.
// Printing a second, hand-maintained verdict was how the old layer contradicted
// the mode (BlockRemoteWrites vetoed writes that accept_edits had already allowed).
//
// This command covers L2 (as expanded) and L3. L1 is exercised by curl, L4 by
// actually calling the tool.
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/remotetargets"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// probeTools are the tool names a remote target commonly serves, ordered from
// harmless to destructive so the ladder reads top to bottom.
var probeTools = []string{
	"ls", "view", "glob", "grep",
	"write", "edit", "apply_patch", "append_write",
	"shell", "job_start", "job_input",
	"job_output", "job_list", "job_cancel",
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: go run ./scripts/permission_layers.go <dir with .aicli/remote-targets.yaml>")
		os.Exit(2)
	}
	workspace := os.Args[1]

	path := remotetargets.FindConfigFile(workspace)
	if path == "" {
		fmt.Printf("no remote-targets.yaml under %s\n", workspace)
		os.Exit(1)
	}
	expanded, err := remotetargets.LoadFile(path)
	if err != nil {
		fmt.Printf("load: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("declaration: %s\n", path)

	if len(expanded.Warnings) > 0 {
		fmt.Println()
		fmt.Println("=== declaration warnings ===")
		for _, warning := range expanded.Warnings {
			fmt.Printf("  %s\n", warning)
		}
	}

	fmt.Println()
	fmt.Println("=== L2: server entries produced by the expansion ===")
	names := make([]string, 0, len(expanded.Servers))
	for name := range expanded.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		server := expanded.Servers[name]
		fmt.Printf("%s: type=%s url=%q scope=%s trust=%s\n",
			name, server.Type, server.URL, server.ExecutionScope, server.TrustLevel)
		if len(server.Headers) > 0 {
			fmt.Printf("   headers: %v\n", server.Headers)
		}
		fmt.Printf("   note: the tool family is decided by the RUNNING server's profile, not by this file\n")
	}

	fmt.Println()
	fmt.Println("=== L3: session mode verdict, per capability class ===")
	// Column width has to clear both the longest mode name (`bypass_permissions`,
	// 18) and the longest verdict this table prints (`ask(background_task)`, 20),
	// plus a gap. Anything narrower makes the columns collide and the table stops
	// being readable — which is the only thing this command is for.
	const verdictWidth = 24
	fmt.Printf("%-14s", "tool")
	modes := runtimepolicy.SupportedModes()
	for _, mode := range modes {
		fmt.Printf("%-*s", verdictWidth, string(mode))
	}
	fmt.Println()
	fmt.Println(strings.Repeat("-", 14+verdictWidth*len(modes)))

	for _, tool := range probeTools {
		fmt.Printf("%-14s", tool)
		for _, mode := range modes {
			info := remoteToolInfo("srv", tool)
			decision := verdictFor(mode, info)
			fmt.Printf("%-*s", verdictWidth, decision)
		}
		fmt.Println()
	}

	fmt.Println()
	fmt.Println("=== what actually gates a target today ===")
	fmt.Println("  restrict by session : --permission-mode plan|accept_edits|dont_ask|bypass_permissions")
	fmt.Println("  restrict by surface : --deny-tool \"remote_<target>_*\"   (wildcard, per target)")
	fmt.Println("  restrict by target  : the target's own profile: readonly|standard|ops|custom")
	fmt.Println("  `#depth` in --remote is a PROFILE REQUEST: applied by ssh-stdio (the server")
	fmt.Println("  starts with it), reported as not applied for http (that server is already running).")
}

// remoteToolInfo builds the ToolInfo a remote server's tool reaches the policy
// with, including the identity metadata the MCP adapter stamps.
//
// The raw name matters: classification consults the registry tables by the bare
// tool name, so omitting it would reproduce the old misclassification where
// `remote_prod__job_start` looked like a read.
func remoteToolInfo(server, tool string) skill.ToolInfo {
	return skill.ToolInfo{
		Name:           "remote_" + server + "__" + tool,
		MCPName:        server,
		ExecutionScope: "remote",
		ExecutionMode:  "remote_mcp",
		MCPTrustLevel:  "trusted_remote",
		Metadata: map[string]interface{}{
			"mcp_name":          server,
			"mcp_raw_tool_name": tool,
		},
	}
}

// verdictFor resolves the tool's capabilities through the real resolver and asks
// the real mode table, so this diagnostic can never disagree with the engine.
func verdictFor(mode runtimepolicy.Mode, info skill.ToolInfo) string {
	caps := runtimepolicy.DefaultCapabilityResolver{}.Resolve(
		runtimepolicy.EvalRequest{ToolName: info.Name, ToolInfo: &info})
	decision := runtimepolicy.ModeDecision(mode, caps)
	label := string(decision)
	if len(caps) > 0 && decision != runtimepolicy.DecisionAllow {
		label = fmt.Sprintf("%s(%s)", label, caps[0])
	}
	return label
}
