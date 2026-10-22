//go:build ignore

// Command local_tool_exemption_audit enumerates every tool source that can reach
// the model, so "what does exclusive mode still leak?" is answered by evidence
// rather than by reading call sites.
//
// Run from backend/:
//
//	go run ./scripts/local_tool_exemption_audit.go
package main

import (
	"fmt"
	"sort"

	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

func main() {
	fmt.Println("=== 1. toolkit:本地内置工具（toolkit != nil 时的工具面）===")
	localOnly := runtimetools.NewDefaultManager(nil)
	localNames := descriptorNames(localOnly.ListTools())
	fmt.Printf("count=%d\n", len(localNames))
	for _, name := range localNames {
		fmt.Printf("  %s\n", name)
	}

	fmt.Println()
	fmt.Println("=== 2. 注册表 Essential 标记（仅供参考，不再是 allowlist 豁免）===")
	essential := essentialToolNames()
	fmt.Printf("count=%d\n", len(essential))
	for _, name := range essential {
		fmt.Printf("  %s\n", name)
	}

	fmt.Println()
	fmt.Println("=== 3. goal functions：绕过 tools.Manager 直接注册进 catalog ===")
	goalNames := goalToolNames()
	fmt.Printf("count=%d\n", len(goalNames))
	for _, name := range goalNames {
		fmt.Printf("  %s\n", name)
	}

	fmt.Println()
	fmt.Println("=== 4. broker 编排面定义（job/team/subagent/schedule/plan）===")
	brokerNames := brokerToolNames()
	fmt.Printf("count=%d\n", len(brokerNames))
	for _, name := range brokerNames {
		fmt.Printf("  %s\n", name)
	}

	fmt.Println()
	fmt.Println("=== 5. 独占模式(toolkit=nil)下 tools.Manager 的贡献 ===")
	remoteOnly := runtimetools.NewRemoteOnlyManager(nil)
	names := remoteOnly.ListTools()
	fmt.Printf("tools.Manager(toolkit=nil, mcp=nil) => %d tools\n", len(names))
	fmt.Println("=> 工具管理器本身不贡献任何工具（本地内置与元工具均已消失）")

	fmt.Println()
	fmt.Println("=== 6. 结论：仍需在 catalog 层拦截的来源 ===")
	fmt.Printf("goal functions 会经由 registerGoalFunctions 独立注册: %v\n", goalNames)
	fmt.Println("broker 工具只在 host 注入了 Broker 时才进面（job_start 等）")
}

func descriptorNames(descriptors []runtimetools.ToolDescriptor) []string {
	names := make([]string, 0, len(descriptors))
	for _, d := range descriptors {
		name := d.Name
		if identity, ok := d.Metadata["mcp_name"].(string); ok && identity != "" {
			name = name + " (mcp:" + identity + ")"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func essentialToolNames() []string {
	names := make([]string, 0)
	for _, entry := range toolregistry.Entries() {
		if entry.Essential {
			names = append(names, entry.Name)
		}
	}
	sort.Strings(names)
	return names
}

func goalToolNames() []string {
	names := make([]string, 0)
	for _, cap := range runtimegoal.Capabilities() {
		names = append(names, cap.Name)
	}
	sort.Strings(names)
	return names
}

// brokerToolNames returns the broker's declared tool names.
//
// Definitions() is a method because a Broker instance decides which families are
// available (team/schedule/supervision depend on injected stores). Passing a zero
// Broker yields the always-available core, which is enough to audit the names.
func brokerToolNames() []string {
	var b toolbroker.Broker
	definitions := b.Definitions()
	names := make([]string, 0, len(definitions))
	for _, def := range definitions {
		if name := def.Name; name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
