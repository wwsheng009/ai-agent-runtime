package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestChatDebugDisplaySnapshotNoSession 验证无会话时快照返回 available=false。
func TestChatDebugDisplaySnapshotNoSession(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	// 确保 provider 返回 nil
	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return nil }
	defer func() { chatDebugDisplaySessionProvider = old }()

	snap := BuildChatDebugDisplaySnapshot()
	if snap.Available {
		t.Fatal("无会话时应返回 available=false")
	}
	if snap.Reason != "no active chat session" {
		t.Fatalf("reason 应为 'no active chat session'，实际为 %q", snap.Reason)
	}
	if snap.Session != nil {
		t.Fatal("无会话时 session 字段应为 nil")
	}
}

// TestChatDebugDisplaySnapshotWithBridge 验证有 bridge 的快照内容。
func TestChatDebugDisplaySnapshotWithBridge(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	bridge.session = session
	session.RuntimeEventBridge = bridge

	// 注入一些编码事件
	bridge.renderEncoder.Encode(runtimeevents.Event{Type: runtimechat.EventLLMRequestStarted, Payload: map[string]interface{}{
		"turn_id": "turn-1", "stream_id": "stream-1",
	}})
	bridge.renderEncoder.Encode(runtimeevents.Event{Type: runtimechat.EventAssistantDelta, Payload: map[string]interface{}{
		"turn_id": "turn-1", "stream_id": "stream-1", "delta": "hi", "sequence": uint64(1),
	}})

	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	defer func() { chatDebugDisplaySessionProvider = old }()

	snap := BuildChatDebugDisplaySnapshot()
	if !snap.Available {
		t.Fatal("有会话时应返回 available=true")
	}
	if snap.Session == nil {
		t.Fatal("有会话时应返回 session 信息")
	}
	if snap.Encoder == nil {
		t.Fatal("有 bridge 时应返回 encoder 信息")
	}
	if snap.Encoder.EncodeCount != 2 {
		t.Fatalf("EncodeCount 应为 2，实际为 %d", snap.Encoder.EncodeCount)
	}
	if snap.Encoder.ModelItems != 1 {
		t.Fatalf("ModelItems 应为 1（两个编码事件合并为一个 item），实际为 %d", snap.Encoder.ModelItems)
	}
	if len(snap.Encoder.ModelItemsTail) != 1 {
		t.Fatalf("ModelItemsTail 应为 1 项，实际为 %d", len(snap.Encoder.ModelItemsTail))
	}
	if snap.Encoder.Tail == nil {
		t.Fatal("应有 tail 信息")
	}
	if snap.Scene != nil {
		t.Log("scene 不为 nil（有 bridge 时包含场景信息）")
	}
}

// TestChatDebugDisplaySnapshotBridgeMissing 验证无 bridge 时 encoder 和 scene 为 nil。
func TestChatDebugDisplaySnapshotBridgeMissing(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{}
	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	defer func() { chatDebugDisplaySessionProvider = old }()

	snap := BuildChatDebugDisplaySnapshot()
	if !snap.Available {
		t.Fatal("有会话时应返回 available=true")
	}
	if snap.Encoder != nil {
		t.Fatal("无 bridge 时 encoder 应为 nil")
	}
	if snap.Scene != nil {
		t.Fatal("无 bridge 时 scene 应为 nil")
	}
}

// TestMarshalChatDebugDisplayJSON 验证 JSON 序列化输出有效。
func TestMarshalChatDebugDisplayJSON(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return nil }
	defer func() { chatDebugDisplaySessionProvider = old }()

	body, err := MarshalChatDebugDisplayJSON()
	if err != nil {
		t.Fatalf("MarshalChatDebugDisplayJSON 失败: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("输出不应为空")
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if av, ok := parsed["available"]; !ok || av != false {
		t.Fatal("JSON 应包含 available=false")
	}
}

// TestBuildChatDebugDisplayText 验证纯文本输出。
func TestBuildChatDebugDisplayText(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return nil }
	defer func() { chatDebugDisplaySessionProvider = old }()

	text := BuildChatDebugDisplayText()
	if !strings.Contains(text, "no active chat session") {
		t.Fatalf("文本应包含 'no active chat session'，实际为 %q", text)
	}
}

// TestChatDebugDisplaySnapshotWithBridgeText 验证有 bridge 时纯文本输出包含编码器统计。
func TestChatDebugDisplaySnapshotWithBridgeText(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	bridge.session = session
	session.RuntimeEventBridge = bridge

	bridge.renderEncoder.Encode(runtimeevents.Event{Type: runtimechat.EventLLMRequestStarted, Payload: map[string]interface{}{
		"turn_id": "turn-1", "stream_id": "stream-1",
	}})

	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	defer func() { chatDebugDisplaySessionProvider = old }()

	text := BuildChatDebugDisplayText()
	want := "available"
	if !strings.Contains(text, want) {
		t.Fatalf("文本应包含 %q", want)
	}
}

// TestChatDebugDisplayActiveCellRanges verifies the extended active_cell block
// exposes Stable/Enqueued/Acked source boundaries: the signature of a growing
// active band whose acknowledged prefix never advances toward a commit.
func TestChatDebugDisplayActiveCellRanges(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	coordinator.SetWriter(&bytes.Buffer{})
	actor := coordinator.ensureUIActor()
	if actor == nil {
		t.Fatal("expected UI actor")
	}
	if !actor.Post(ui.SetActiveCellAction{Active: ui.ActiveCellState{
		CellID:   41,
		Revision: 7,
		Kind:     scene.KindAssistant,
		Phase:    ui.ActiveCellMutable,
		Source:   "partial streamed body text",
		Stable:   ui.SourceRange{Start: 0, End: 22},
		Enqueued: ui.SourceRange{Start: 0, End: 22},
		Acked:    ui.SourceRange{Start: 0, End: 8},
	}}) {
		t.Fatal("post active cell mount")
	}
	actor.WaitIdle()

	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	defer func() { chatDebugDisplaySessionProvider = old }()

	snap := BuildChatDebugDisplaySnapshot()
	if !snap.Available {
		t.Fatal("有会话时应返回 available=true")
	}
	if snap.AppState == nil || snap.AppState.ActiveCell == nil {
		t.Fatal("app_state.active_cell 应为非 nil")
	}
	ac := snap.AppState.ActiveCell
	if ac.Phase != "mutable" {
		t.Fatalf("phase 应为 mutable，实际为 %q", ac.Phase)
	}
	if ac.StableEnd != 22 {
		t.Fatalf("stable_end 应为 22，实际为 %d", ac.StableEnd)
	}
	if ac.EnqueuedEnd != 22 {
		t.Fatalf("enqueued_end 应为 22，实际为 %d", ac.EnqueuedEnd)
	}
	if ac.AckedEnd != 8 {
		t.Fatalf("acked_end 应为 8，实际为 %d", ac.AckedEnd)
	}
	if ac.CommitBlocked {
		t.Fatal("commit_blocked 应为 false")
	}
}

// TestChatDebugDisplayExecutorAndProjection verifies the new executor and
// projection blocks surface the recovery-loop diagnostics and the physical
// terminal projection summary on the same /debug/chat/status snapshot.
func TestChatDebugDisplayExecutorAndProjection(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	// A bare TerminalSession is enough for ProjectionState(); the executor
	// diag block reads the package-global provider.
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	coordinator.SetWriter(&bytes.Buffer{})
	var terminalOutput bytes.Buffer
	session.TerminalSession = ui.NewTerminalSession(&terminalOutput)

	// SetExecutorDiagProvider accepts nil; resetting on cleanup guarantees
	// the package-global provider does not leak into other tests.
	ui.SetExecutorDiagProvider(func() ui.ExecutorRecoveryDiag {
		return ui.ExecutorRecoveryDiag{
			Diagnosis:      "healthy",
			TotalRecoveries: 12,
			BackoffEngaged:  0,
			ArmedBackoff:    1,
			LastGeneration:  3,
			Entries: []ui.ExecutorRecoveryDiagEntry{{
				Seq: 1, Branch: "scheduled", Generation: 3,
				Revision: 5, RevisionAfter: 6,
			}},
		}
	})
	t.Cleanup(func() { ui.SetExecutorDiagProvider(nil) })

	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	defer func() { chatDebugDisplaySessionProvider = old }()

	snap := BuildChatDebugDisplaySnapshot()
	if snap.Projection == nil {
		t.Fatal("projection 应为非 nil（有 TerminalSession）")
	}
	if snap.Projection.HistoryKnown == false {
		t.Log("projection history_known=false（未 flush，符合预期）")
	}
	if snap.Executor == nil {
		t.Fatal("executor 应为非 nil（provider 已注册）")
	}
	if snap.Executor.Diagnosis != "healthy" {
		t.Fatalf("executor.diagnosis 应为 healthy，实际为 %q", snap.Executor.Diagnosis)
	}
	if snap.Executor.TotalRecoveries != 12 {
		t.Fatalf("executor.total_recoveries 应为 12，实际为 %d", snap.Executor.TotalRecoveries)
	}
	if snap.Executor.LastEntry == nil {
		t.Fatal("executor.last_entry 应为非 nil")
	}
	if snap.Executor.LastEntry.RevisionAfter != 6 {
		t.Fatalf("last_entry.revision_after 应为 6，实际为 %d", snap.Executor.LastEntry.RevisionAfter)
	}

	body, err := MarshalChatDebugDisplayJSON()
	if err != nil {
		t.Fatalf("MarshalChatDebugDisplayJSON 失败: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if _, ok := parsed["executor"]; !ok {
		t.Fatal("JSON 应包含 executor 块")
	}
	if _, ok := parsed["projection"]; !ok {
		t.Fatal("JSON 应包含 projection 块")
	}
}

// TestChatDebugDisplayNewSections verifies the new files/runtime/routing/
// components/agents sections are present in the snapshot.
func TestChatDebugDisplayNewSections(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{
		ProviderName: "test-provider",
		Model:        "test-model",
		ProfileName:  "test-profile",
		OutputFormat: "text",
	}
	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	defer func() { chatDebugDisplaySessionProvider = old }()

	body, err := MarshalChatDebugDisplayJSON()
	if err != nil {
		t.Fatalf("MarshalChatDebugDisplayJSON failed: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	// Verify new sections exist.
	for _, section := range []string{"session", "files", "runtime", "routing", "components", "agents"} {
		if _, ok := parsed[section]; !ok {
			t.Fatalf("JSON should contain %q section", section)
		}
	}

	// Verify session fields from the top Session Info block.
	sess, ok := parsed["session"].(map[string]interface{})
	if !ok {
		t.Fatal("session should be a map")
	}
	if sess["provider"] != "test-provider" {
		t.Fatalf("session.provider should be 'test-provider', got %q", sess["provider"])
	}
	if sess["model"] != "test-model" {
		t.Fatalf("session.model should be 'test-model', got %q", sess["model"])
	}
	if sess["profile"] != "test-profile" {
		t.Fatalf("session.profile should be 'test-profile', got %q", sess["profile"])
	}

	// Verify runtime section.
	runtime, ok := parsed["runtime"].(map[string]interface{})
	if !ok {
		t.Fatal("runtime should be a map")
	}
	if runtime["output_format"] != "text" {
		t.Fatalf("runtime.output_format should be 'text', got %q", runtime["output_format"])
	}

	// Verify components section.
	components, ok := parsed["components"].(map[string]interface{})
	if !ok {
		t.Fatal("components should be a map")
	}
	if components["runtime_core"] != "<none>" {
		t.Fatalf("components.runtime_core should be '<none>', got %q", components["runtime_core"])
	}

	// Verify agents section has registry/mailbox keys.
	agents, ok := parsed["agents"].(map[string]interface{})
	if !ok {
		t.Fatal("agents should be a map")
	}
	if _, ok := agents["registry"]; !ok {
		t.Fatal("agents.registry should exist")
	}
	if _, ok := agents["mailbox"]; !ok {
		t.Fatal("agents.mailbox should exist")
	}
}
