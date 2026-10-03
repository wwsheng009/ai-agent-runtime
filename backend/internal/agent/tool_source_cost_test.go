package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// countingToolSourceManager 同时提供便宜路径（ResolveToolSource）与昂贵回退
// （FindTool），并统计回退被走了几次。
//
// 内嵌 skill.MCPManager 让未覆写的方法保持未实现（被调用即 panic）。
type countingToolSourceManager struct {
	skill.MCPManager

	source    string
	findCalls int
}

func (m *countingToolSourceManager) ResolveToolSource(string) string { return m.source }

func (m *countingToolSourceManager) FindTool(toolName string) (skill.ToolInfo, error) {
	m.findCalls++
	return skill.ToolInfo{Name: toolName}, nil
}

// 回归（性能）：surface 实现了 ResolveToolSource 时，来源归类必须走便宜路径，
// 不得回落到 FindTool。
//
// 为什么这条值得锁：FindTool 的实现是"重建整张工具表再按名字扫一遍"
// （AgentAdapter.FindTool → Manager.ListTools → codeToolGate.sync → 知识层取数）。
// 实测一次回落 ~2.4s，而 computeAvailableTools 对 85 个工具逐个问来源 ——
// 退化成分钟级的"用户输入后长时间没有输出"。这里计数为 0 就是那条不退化的证据。
func TestResolveToolSourceForRequestPrefersCheapResolver(t *testing.T) {
	manager := &countingToolSourceManager{source: toolresult.SourceMCP}
	agent := &Agent{mcpManager: manager}

	assert.Equal(t, toolresult.SourceMCP, resolveToolSourceForRequest(agent, "playwright_click"))
	assert.Zero(t, manager.findCalls,
		"实现了 ResolveToolSource 时不得回落到 FindTool（昂贵回退）")
}

// 便宜路径报不出来源时仍按原语义回退到 FindTool —— 这是行为兜底，
// 不是性能路径；这里钉住它没有被一起删掉。
func TestResolveToolSourceForRequestFallsBackToFindTool(t *testing.T) {
	manager := &countingToolSourceManager{source: ""}
	agent := &Agent{mcpManager: manager}

	assert.Equal(t, toolresult.SourceToolkit, resolveToolSourceForRequest(agent, "view"),
		"FindTool 命中且 MCPName 为空时应归类为 toolkit")
	assert.Equal(t, 1, manager.findCalls)
}
