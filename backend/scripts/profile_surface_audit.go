//go:build ignore

// Command profile_surface_audit proves what a "remote profile" actually removes.
//
// It answers three questions with real code paths:
//
//  1. Does tools.denylist block every local tool that collides with a remote one?
//  2. Does the deny list accidentally block the PREFIXED remote tool?
//  3. Does mcp.use_servers actually narrow the config to one server?
//
// Run from backend/:
//
//	go run ./scripts/profile_surface_audit.go
package main

import (
	"fmt"
	"os"
	"sort"

	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	profileinput "github.com/wwsheng009/ai-agent-runtime/internal/profileinput"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

// localDenylist is the profile's tools.denylist, kept in sync with
// .tmp/prof/profiles/remote/profile.yaml.
var localDenylist = []string{
	"view", "ls", "glob", "grep", "read", "rg",
	"write", "edit", "apply_patch", "append_write", "multiedit",
	"shell", "bash", "execute_shell_command",
	"job_start", "job_output", "job_cancel", "job_list", "job_search",
	"job_monitor", "job_control", "job_input",
	"download", "aicli_exec", "todos",
}

func main() {
	failures := 0
	fail := func(format string, args ...interface{}) {
		failures++
		fmt.Printf("FAIL: "+format+"\n", args...)
	}

	policy := runtimepolicy.NewToolExecutionPolicy(nil, false)
	policy.DeniedTools = map[string]bool{}
	for _, name := range localDenylist {
		policy.DeniedTools[name] = true
	}

	// 1) every local toolkit tool must be denied.
	fmt.Println("=== 1. 本地 toolkit 工具是否全部被 deny ===")
	localOnly := runtimetools.NewDefaultManager(nil)
	localNames := make([]string, 0)
	for _, d := range localOnly.ListTools() {
		localNames = append(localNames, d.Name)
	}
	sort.Strings(localNames)

	leaked := 0
	for _, name := range localNames {
		if policy.DeniesTool(name) {
			continue
		}
		// These do not touch the target filesystem, so leaving them in does not
		// create the "wrote to the wrong machine" hazard this audit is about.
		// They are listed explicitly rather than silently ignored so the reason
		// is visible in the output.
		if benignLocalTool(name) {
			fmt.Printf("  KEEP   %s（不触碰本地文件/进程，可保留）\n", name)
			continue
		}
		leaked++
		fail("本地工具 %q 没有被 deny 拦掉", name)
	}
	fmt.Printf("本地工具 total=%d blocked=%d leaked=%d\n", len(localNames), len(localNames)-leaked, leaked)

	// 2) the prefixed remote tool must survive.
	fmt.Println()
	fmt.Println("=== 2. 带前缀的远程工具是否被误伤 ===")
	for _, tool := range []string{"view", "ls", "write", "edit", "apply_patch", "shell", "job_start"} {
		remoteName := "remote_wsl__" + tool
		if policy.DeniesTool(remoteName) {
			fail("远程工具 %q 被 deny 误伤", remoteName)
			continue
		}
		fmt.Printf("  ALLOW  %s\n", remoteName)
	}

	// 3) mcp.use_servers narrows the config.
	fmt.Println()
	fmt.Println("=== 3. mcp.use_servers 是否能把配置裁到单个 server ===")
	cfg := &mcpconfig.Config{MCPServers: map[string]mcpconfig.MCPConfig{
		"wsl":   {Name: "wsl", Type: "streamable", URL: "http://127.0.0.1:18774/mcp", Enabled: true},
		"other": {Name: "other", Type: "streamable", URL: "http://127.0.0.1:19998/mcp", Enabled: true},
	}}
	selection := profileinput.ResolvedMCPSelection{UseServers: []string{"wsl"}}
	filtered, dropped := profileinput.ApplyMCPSelection(cfg, selection)
	names := make([]string, 0, len(filtered.MCPServers))
	for name := range filtered.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Printf("kept=%v dropped=%v\n", names, dropped)
	if len(names) != 1 || names[0] != "wsl" {
		fail("use_servers 没有把配置裁到单个 server: %v", names)
	}
	if len(cfg.MCPServers) != 2 {
		fail("ApplyMCPSelection 修改了输入配置（必须返回 clone）")
	}

	// 4) tools the profile CANNOT remove.
	fmt.Println()
	fmt.Println("=== 4. profile.tools.denylist 能拦住的非 Manager 来源 ===")
	goalNames := make([]string, 0)
	for _, cap := range runtimegoal.Capabilities() {
		goalNames = append(goalNames, cap.Name)
	}
	sort.Strings(goalNames)
	unprotected := 0
	for _, name := range goalNames {
		if policy.DeniesTool(name) {
			fmt.Printf("  DENY   %s (goal，已列入 denylist)\n", name)
			continue
		}
		// goal tools only read/write local session state; they cannot reach the
		// target. Reported, not failed.
		fmt.Printf("  KEEP   %s (goal，仅操作会话状态)\n", name)
		unprotected++
	}

	var broker toolbroker.Broker
	for _, def := range broker.Definitions() {
		if policy.DeniesTool(def.Name) {
			fmt.Printf("  DENY   %s (broker)\n", def.Name)
			continue
		}
		// ask_user_question is pure interaction; everything else in the broker
		// family spawns or inspects local processes and MUST be denied.
		if benignBrokerTool(def.Name) {
			fmt.Printf("  KEEP   %s (broker，纯交互)\n", def.Name)
			continue
		}
		unprotected++
		fail("broker 工具 %q 未列入 denylist（会操作本地进程）", def.Name)
	}
	_ = unprotected

	fmt.Println()
	if failures > 0 {
		fmt.Printf("RESULT: %d failure(s)\n", failures)
		os.Exit(1)
	}
	fmt.Println("RESULT: profile 能裁掉全部本地 toolkit 工具，且不误伤远程工具")
	fmt.Println("        遗留：未列入 denylist 的 goal/broker 工具仍可调用")
}

// benignBrokerTool reports whether a broker tool is safe to keep in an
// exclusively-remote session. Only pure interaction qualifies: anything that can
// start, read, or stop a LOCAL process would be indistinguishable from the
// remote job_* family to the model.
func benignBrokerTool(name string) bool {
	return name == "ask_user_question"
}

// benignLocalTool reports whether a local tool is harmless to keep in a
// remote-targeted session.
//
// The hazard being audited is "the model believes it operated the remote host but
// actually touched the local one". A tool qualifies as benign only when it cannot
// read or write local files and cannot start local processes.
func benignLocalTool(name string) bool {
	switch name {
	case
		"artifact_read",      // reads the local artifact store, not the filesystem
		"fetch",              // network only
		"web_search",         // network only
		"sourcegraph",        // network only
		"list_mcp_resources": // MCP metadata; already removed when toolkit == nil
		return true
	}
	return false
}
