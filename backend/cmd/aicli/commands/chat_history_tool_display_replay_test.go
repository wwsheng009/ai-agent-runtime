package commands

import (
	"strings"
	"testing"

	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// storedToolMessageFixture 复刻真实持久化形态：content 是模型面向原文
// （含 artifact 指针），metadata 携带 tool_invocation/tool_metadata。
func storedToolMessageFixture() runtimetypes.Message {
	content := strings.Join([]string{
		"Exit code: 0",
		"Shell: pwsh",
		"Workdir: E:\\projects\\ai\\ai-agent-runtime\\backend",
		"Wall time: 608ms",
		"Timed out: false",
		"Output:",
		"E2E_SNAPSHOT_OK",
		"",
		"Full raw output artifact_id: art_d00815f1e8534555acdfd64dca1a812c size=133 kind=text",
	}, "\n")
	metadata := runtimetypes.NewMetadata()
	metadata["tool_name"] = "shell"
	metadata["tool_source"] = "toolkit"
	metadata["tool_invocation"] = map[string]interface{}{
		"attempted_args": map[string]interface{}{"command": "echo E2E_SNAPSHOT_OK"},
	}
	metadata["tool_metadata"] = map[string]interface{}{
		"command":                 "echo E2E_SNAPSHOT_OK",
		"duration_ms":             608,
		"exit_code":               0,
		"output_kind":             "text",
		"output_capture_complete": true,
		"capture_limit_reached":   false,
		"shell_display":           "pwsh",
	}
	return runtimetypes.Message{
		Role:       "tool",
		Content:    content,
		ToolCallID: "call_00_FkGDA5eq5Wz3jmrsnec76430",
		Metadata:   metadata,
	}
}

// liveToolEventFixture 是实时链路等价的 ChatEvent（provider loop 携带完整
// Arguments 与工具元数据）。
func liveToolEventFixture(message runtimetypes.Message) runtimechatcore.ChatEvent {
	return runtimechatcore.ChatEvent{
		Type:       runtimechatcore.EventTool,
		Stage:      "tool_result",
		ToolName:   "shell",
		ToolCallID: message.ToolCallID,
		Arguments:  map[string]interface{}{"command": "echo E2E_SNAPSHOT_OK"},
		Output:     message.Content,
		Success:    true,
		Metadata: map[string]interface{}{
			"tool_name":               "shell",
			"tool_source":             "toolkit",
			"duration_ms":             608,
			"output_kind":             "text",
			"output_capture_complete": true,
			"capture_limit_reached":   false,
		},
	}
}

func toolCallItemHead(t *testing.T, bridge *chatRuntimeEventBridge) string {
	t.Helper()
	snapshot := bridge.renderModelSnapshot()
	if snapshot == nil {
		t.Fatal("render model snapshot is nil")
	}
	for _, item := range snapshot.Items {
		if item != nil && strings.EqualFold(string(item.Kind), "tool_call") {
			return item.Head
		}
	}
	t.Fatalf("no tool_call item in snapshot: %d items", len(snapshot.Items))
	return ""
}

func TestChatHistoryToolDisplayMatchesLiveCompactDisplay(t *testing.T) {
	message := storedToolMessageFixture()
	want := renderSharedChatToolEvent(liveToolEventFixture(message))
	if strings.TrimSpace(want) == "" {
		t.Fatal("live fixture produced empty display")
	}
	if !strings.Contains(want, "echo E2E_SNAPSHOT_OK") {
		t.Fatalf("live display lost command: %q", want)
	}
	if strings.Contains(want, "Full raw output artifact_id") {
		t.Fatalf("live display must not leak raw artifact pointer: %q", want)
	}

	got := chatHistoryToolDisplay(message, "shell", nil)
	if got != want {
		t.Fatalf("replay display mismatch\n live: %q\nreplay: %q", want, got)
	}
}

func TestPersistedHistorySeedUsesCompactDisplayAndMatchesLiveItem(t *testing.T) {
	message := storedToolMessageFixture()
	// 真实历史里工具名来自 assistant 消息的 tool_call；缺少这条记录时种子侧
	// 会把 toolName 退化成 callID，live/seed 就不再可比。装配完整历史形态。
	call := runtimetypes.ToolCall{
		ID:   message.ToolCallID,
		Name: "shell",
		Args: map[string]interface{}{"command": "echo E2E_SNAPSHOT_OK"},
	}
	units := buildPersistedHistorySeedUnits([]runtimetypes.Message{
		{Role: "assistant", ToolCalls: []runtimetypes.ToolCall{call}},
		message,
	})
	if len(units) != 1 || units[0].kind != persistedHistorySeedTool {
		t.Fatalf("unexpected seed units: %+v", units)
	}
	unit := units[0]
	if strings.TrimSpace(unit.toolDisplay) == "" {
		t.Fatal("seed unit has no compact display")
	}
	if strings.Contains(unit.toolDisplay, "Full raw output artifact_id") {
		t.Fatalf("seed display must not leak raw artifact pointer: %q", unit.toolDisplay)
	}

	// live 侧：请求 + 结构化 compact 块注入（与 renderToolChainEvent 相同入口）。
	liveBridge := newChatRuntimeEventBridge(&ChatSession{})
	liveBridge.submitToolRequested(unit.toolCallID, unit.toolName, map[string]interface{}{"command": "echo E2E_SNAPSHOT_OK"})
	liveBlock, ok := compactToolCompletedBlockForEvent(liveToolEventFixture(message))
	if !ok {
		t.Fatal("live fixture produced no compact block")
	}
	liveBridge.submitToolResultBlock(unit.toolCallID, unit.toolName, message.Content, "", true, liveBlock.head, liveBlock.content, liveBlock.ownStructure)
	liveHead := toolCallItemHead(t, liveBridge)
	if liveHead != unit.toolDisplay {
		t.Fatalf("live item head mismatch\nwant: %q\n got: %q", unit.toolDisplay, liveHead)
	}

	// 匹配必须命中 live item：否则 resume 会追加一份原文单元格（重复）。
	if matched := persistedHistoryUnitMatch(liveBridge.renderModelSnapshot(), unit, map[string]struct{}{}); matched == nil {
		t.Fatal("live compact item did not match persisted seed unit (would duplicate on resume)")
	}

	// 空模型（冷启动）种子：产生与 live 相同的终态头部。
	replayBridge := newChatRuntimeEventBridge(&ChatSession{})
	unit.apply(replayBridge)
	replayHead := toolCallItemHead(t, replayBridge)
	if replayHead != liveHead {
		t.Fatalf("replay item head mismatch\nlive: %q\nreplay: %q", liveHead, replayHead)
	}
	if strings.Contains(replayHead, "Full raw output artifact_id") {
		t.Fatalf("replay cell degraded to raw output: %q", replayHead)
	}
}

// TestStructuredToolBlockInjectionPreservesMarkdownBranchPrefixes 守住结构化
// 注入的另一条内容装配分支：markdown 渲染行自带 legacy "  " 前缀，持久化与
// 重建必须与 renderSharedChatToolEvent 逐字一致（前缀只在树形标记阶段剥离）。
func TestStructuredToolBlockInjectionPreservesMarkdownBranchPrefixes(t *testing.T) {
	event := runtimechatcore.ChatEvent{
		Type: runtimechatcore.EventTool, Stage: "tool_result",
		ToolName: "edit", ToolCallID: "call-md", Output: "# title\ntext", Success: true,
	}
	want := renderSharedChatToolEvent(event)
	if !strings.Contains(want, "  │  # title") || !strings.Contains(want, "  └  text") {
		t.Fatalf("fixture did not take the markdown tree branch: %q", want)
	}
	block, ok := compactToolCompletedBlockForEvent(event)
	if !ok {
		t.Fatal("markdown fixture produced no compact block")
	}
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.submitToolRequested(event.ToolCallID, event.ToolName, nil)
	bridge.submitToolResultBlock(event.ToolCallID, event.ToolName, event.Output, "", true, block.head, block.content, block.ownStructure)
	if got := toolCallItemHead(t, bridge); got != want {
		t.Fatalf("structured injection head mismatch\nwant: %q\n got: %q", want, got)
	}
}

// TestPersistedHistorySeedUsesStructuredToolBlock 固化 S3 契约：历史种子在
// 渲染结果与 toolDisplay 逐字一致时携带结构化块，导入 Scene 走
// SubmitToolResultBlock（与 live/事件日志同源），且种子头部不变。
func TestPersistedHistorySeedUsesStructuredToolBlock(t *testing.T) {
	message := storedToolMessageFixture()
	call := runtimetypes.ToolCall{
		ID:   message.ToolCallID,
		Name: "shell",
		Args: map[string]interface{}{"command": "echo E2E_SNAPSHOT_OK"},
	}
	units := buildPersistedHistorySeedUnits([]runtimetypes.Message{
		{Role: "assistant", ToolCalls: []runtimetypes.ToolCall{call}},
		message,
	})
	if len(units) != 1 || units[0].kind != persistedHistorySeedTool {
		t.Fatalf("unexpected seed units: %+v", units)
	}
	unit := units[0]
	if unit.toolBlock.head == "" {
		t.Fatalf("expected a structured tool block, got display=%q", unit.toolDisplay)
	}
	if rendered := unit.toolBlock.render(); rendered != unit.toolDisplay {
		t.Fatalf("structured block must render to the seed display\nblock: %q\nseed:  %q", rendered, unit.toolDisplay)
	}
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	unit.apply(bridge)
	if got := toolCallItemHead(t, bridge); got != unit.toolDisplay {
		t.Fatalf("seeded Scene head mismatch\nwant: %q\n got: %q", unit.toolDisplay, got)
	}
}
