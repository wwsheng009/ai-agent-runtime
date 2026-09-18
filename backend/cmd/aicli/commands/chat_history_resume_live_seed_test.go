package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestResumeSeedAfterLiveBusToolEventsKeepsSingleCompactCell 复现真实故障
// 场景：同一会话先经总线事件建立工具单元格（tool.started/progress/completed，
// 与生产 payload 同形），随后 resume 用持久化消息补齐历史。
//
// 断言：种子不得因为头部形态不同而再建一个单元格（重复），也不得引入原始
// 输出（artifact 指针）。
func TestResumeSeedAfterLiveBusToolEventsKeepsSingleCompactCell(t *testing.T) {
	stored := realPersistedToolMessage()
	assistant := runtimetypes.NewAssistantMessage("前面的说明")
	assistant.ToolCalls = []runtimetypes.ToolCall{{
		ID:   stored.ToolCallID,
		Name: "shell",
		Args: map[string]interface{}{"command": "echo E2E_SNAPSHOT_OK"},
	}}

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	bridge.submitAssistant("前面的说明")
	// --- live：总线事件（形态来自真机 runtime-events.jsonl） ---
	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type:     "tool.requested",
		ToolName: "shell",
		Payload: map[string]interface{}{
			"tool_call_id": stored.ToolCallID,
			"tool_name":    "shell",
			"arg_preview":  "command=echo E2E_SNAPSHOT_OK",
			"command_text": "echo E2E_SNAPSHOT_OK",
		},
	})
	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type:     "tool.progress",
		ToolName: "shell",
		Payload: map[string]interface{}{
			"tool_call_id": stored.ToolCallID,
			"tool_name":    "shell",
			"message":      "stdout",
		},
	})
	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type:     "tool.completed",
		ToolName: "shell",
		Payload: map[string]interface{}{
			"tool_call_id":            stored.ToolCallID,
			"tool_name":               "shell",
			"command_text":            "echo E2E_SNAPSHOT_OK",
			"arg_preview":             "command=echo E2E_SNAPSHOT_OK",
			"duration_ms":             608,
			"tool_source":             "toolkit",
			"output_kind":             "text",
			"output_capture_complete": true,
			"capture_limit_reached":   false,
			"summary":                 "Exit code: 0\nShell: pwsh\nWorkdir: E:\\projects\\ai\\ai-agent-runtime\\backend",
			"summary_lines":           []string{"Exit code: 0", "Shell: pwsh", "Workdir: E:\\projects\\ai\\ai-agent-runtime\\backend"},
		},
	})

	live := bridge.sceneSnapshot()
	if live == nil {
		t.Fatal("no scene snapshot after live bus events")
	}
	toolCells := 0
	var liveHead string
	for _, cell := range live.Cells {
		if cell.Kind == scene.KindToolChain {
			toolCells++
			liveHead = cell.Source
		}
	}
	if toolCells != 1 {
		t.Fatalf("live scene tool cells=%d want 1", toolCells)
	}
	if !strings.Contains(liveHead, "in 608ms") {
		// 载荷自带 duration_ms（生产由 agent 从 tool_metadata 提升）时，bridge
		// 不得用墙钟回退值覆盖，否则 live 与离线/重放耗时口径会分叉。
		t.Fatalf("live head ignored payload duration:\n%q", liveHead)
	}

	// --- resume：历史种子补齐（同一会话消息） ---
	bridge.seedPersistedHistory([]runtimetypes.Message{*assistant, stored}, "")

	after := bridge.sceneSnapshot()
	if after == nil {
		t.Fatal("no scene snapshot after resume seed")
	}
	toolCells = 0
	var afterHead string
	for _, cell := range after.Cells {
		if cell.Kind == scene.KindToolChain {
			toolCells++
			afterHead = cell.Source
		}
	}
	if toolCells != 1 {
		t.Fatalf("resume duplicated tool chain: cells=%d want 1\n%q", toolCells, afterHead)
	}
	if afterHead != liveHead {
		t.Fatalf("resume regressed the tool cell\n live: %q\nresume: %q", liveHead, afterHead)
	}
	if strings.Contains(afterHead, "Full raw output artifact_id") {
		t.Fatalf("resume cell leaked raw output:\n%q", afterHead)
	}
	if !strings.Contains(afterHead, "• Completed echo E2E_SNAPSHOT_OK") {
		t.Fatalf("resume cell lost the compact title:\n%q", afterHead)
	}
}
