//go:build ignore

// gatecheck 用**真实配置文件**驱动远程工具判定，打印每个工具在各 permission
// mode 下的结论，并自检「远程与本地同名工具结论必须一致」这条不变式。
//
// 它补的是纯单测覆盖不到的一段：remote-targets.yaml 声明 → remotetargets.Expand
// → 真实 capability 解析 → 真实 mode 判定表。单测里两边都是手写 map，任何一个
// 桥接写错都不会被发现。
//
// 2026-10-22 起它不再检查 per-target grant（allowRead/allowWrite/allowShell），
// 因为那层已删除：远程工具与本地工具走同一条判定链，mode 是唯一答案。本脚本
// 的核心断言也随之变成「远程结论 == 本地结论」——一旦出现分歧，就说明有人又给
// 远程单独加了一层控制。
package main

import (
	"fmt"
	"os"
	"strings"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/remotetargets"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// probeTools 覆盖读 / 文件写 / 执行 / 后台四类。
var probeTools = []string{
	"ls", "view", "glob", "grep",
	"write", "edit", "apply_patch", "append_write",
	"shell", "job_start", "job_input",
	"job_output", "job_list", "job_cancel",
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: go run ./scripts/gatecheck.go <dir containing .aicli/remote-targets.yaml>")
		os.Exit(2)
	}
	workspace := os.Args[1]

	path := remotetargets.FindConfigFile(workspace)
	if path == "" {
		fmt.Printf("FAIL no remote-targets.yaml under %s\n", workspace)
		os.Exit(1)
	}
	fmt.Printf("config: %s\n", path)

	expanded, err := remotetargets.LoadFile(path)
	if err != nil {
		fmt.Printf("FAIL load: %v\n", err)
		os.Exit(1)
	}
	for _, warning := range expanded.Warnings {
		fmt.Printf("note: %s\n", warning)
	}

	serverName := ""
	for name := range expanded.Servers {
		if serverName == "" || name < serverName {
			serverName = name
		}
	}
	if serverName == "" {
		fmt.Println("FAIL the declaration produced no servers")
		os.Exit(1)
	}
	prefix := "remote_" + serverName

	modes := runtimepolicy.SupportedModes()

	fmt.Printf("\n%-14s", "tool")
	for _, mode := range modes {
		fmt.Printf("%-16s", string(mode))
	}
	fmt.Println()
	fmt.Println(strings.Repeat("-", 14+16*len(modes)))

	failures := 0
	for _, tool := range probeTools {
		fmt.Printf("%-14s", tool)
		remoteInfo := remoteTool(serverName, prefix, tool)
		for _, mode := range modes {
			remoteVerdict := decide(mode, remoteInfo)
			// 同名的本地工具：不携带任何 MCP 身份，走 builtin 路径。
			localVerdict := decideLocal(mode, tool)
			if remoteVerdict != localVerdict {
				fmt.Printf("FAIL %s/%s: remote=%s local=%s\n", tool, mode, remoteVerdict, localVerdict)
				failures++
			}
			fmt.Printf("%-16s", remoteVerdict)
		}
		fmt.Println()
	}

	if failures > 0 {
		fmt.Printf("\nFAIL %d remote/local divergence(s): a remote tool must be governed exactly like the local one\n", failures)
		os.Exit(1)
	}
	fmt.Println("\nREMOTE==LOCAL VERDICTS OK（mode 是唯一判定层，远程没有额外控制）")
}

// remoteTool 构造远程工具进入策略时的形态，含 MCP adapter 打上的身份 metadata。
//
// mcp_raw_tool_name 是关键：分类按裸工具名查注册表，缺了它就会退回子串启发式，
// 于是 job_start 这类「名字里没有 shell 字样但会起进程」的工具会被误判为只读。
func remoteTool(server, prefix, tool string) skill.ToolInfo {
	return skill.ToolInfo{
		Name:           prefix + "__" + tool,
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

func decide(mode runtimepolicy.Mode, info skill.ToolInfo) string {
	caps := runtimepolicy.DefaultCapabilityResolver{}.Resolve(
		runtimepolicy.EvalRequest{ToolName: info.Name, ToolInfo: &info})
	return string(runtimepolicy.ModeDecision(mode, caps))
}

func decideLocal(mode runtimepolicy.Mode, tool string) string {
	caps := runtimepolicy.DefaultCapabilityResolver{}.Resolve(
		runtimepolicy.EvalRequest{ToolName: tool})
	return string(runtimepolicy.ModeDecision(mode, caps))
}
