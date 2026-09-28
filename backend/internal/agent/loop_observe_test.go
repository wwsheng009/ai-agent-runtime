package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// observeMCPManager 是 loop.act 走 MCP 分支的最小 mock：grep 调用返回一段
// 文本结果，用于验证 OnToolObserved 能拿到最终 output（Phase 1 shadow 拦截）。
type observeMCPManager struct{ calls int }

func (m *observeMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{
		Name:    toolName,
		Enabled: true,
		MCPName: "mock-mcp",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern": map[string]interface{}{"type": "string"},
			},
		},
		Metadata: map[string]interface{}{"retry_class": "safe"},
	}, nil
}

func (m *observeMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	m.calls++
	return "backend/a.go:10:hello world", nil
}

func (m *observeMCPManager) ListTools() []skill.ToolInfo {
	info, _ := m.FindTool("grep")
	return []skill.ToolInfo{info}
}

func TestAct_OnToolObservedReceivesGrepResult(t *testing.T) {
	mcp := &observeMCPManager{}
	ag := &Agent{config: &Config{Name: "observe-test"}, mcpManager: mcp}

	type observed struct {
		sessionID string
		name      string
		output    string
		err       string
	}
	var (
		mu   sync.Mutex
		seen []observed
	)
	loop := NewReActLoop(ag, nil, &LoopReActConfig{
		MaxSteps: 5,
		OnToolObserved: func(_ context.Context, sessionID string, call types.ToolCall, output string, toolErr string) {
			mu.Lock()
			seen = append(seen, observed{sessionID: sessionID, name: call.Name, output: output, err: toolErr})
			mu.Unlock()
		},
	})

	results, err := loop.act(context.Background(), "trace-observe", "session-observe", 1, 0, nil, []types.ToolCall{{
		ID:   "tc-observe",
		Name: "grep",
		Args: map[string]interface{}{"pattern": "hello"},
	}}, nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, 1, mcp.calls, "grep 应真实执行一次")
	require.Empty(t, strings.TrimSpace(results[0].Error))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, seen, 1, "OnToolObserved 应收到一次回调")
	require.Equal(t, "session-observe", seen[0].sessionID)
	require.Equal(t, "grep", seen[0].name)
	require.Contains(t, seen[0].output, "hello world")
	require.Empty(t, seen[0].err)
}

// 契约：nil hook / nil loop / 空 config 都是 no-op，不得 panic。
func TestObserveToolResult_NilSafe(t *testing.T) {
	(&ReActLoop{}).observeToolResult(context.Background(), "s", types.ToolCall{}, toolExecutionResult{})
	(&ReActLoop{config: &LoopReActConfig{}}).observeToolResult(context.Background(), "s", types.ToolCall{}, toolExecutionResult{})
}
