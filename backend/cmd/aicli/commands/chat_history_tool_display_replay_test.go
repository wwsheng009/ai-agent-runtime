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
	units := buildPersistedHistorySeedUnits([]runtimetypes.Message{message})
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

	// live 侧：请求 + compact 显示注入（与 renderToolChainEvent 相同入口）。
	liveBridge := newChatRuntimeEventBridge(&ChatSession{})
	liveBridge.submitToolRequested(unit.toolCallID, unit.toolName, map[string]interface{}{"command": "echo E2E_SNAPSHOT_OK"})
	liveBridge.submitToolResultDisplay(unit.toolCallID, unit.toolName, message.Content, "", true, unit.toolDisplay)
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
