package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// TestChatDebugScreenSnapshotNoSession 验证无会话时快照返回 available=false。
func TestChatDebugScreenSnapshotNoSession(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return nil }
	defer func() { chatDebugDisplaySessionProvider = old }()

	snap := BuildChatDebugScreenSnapshot()
	if snap.Available {
		t.Fatal("无会话时应返回 available=false")
	}
	if snap.Reason != "no active chat session" {
		t.Fatalf("reason 应为 'no active chat session'，实际为 %q", snap.Reason)
	}
}

// TestChatDebugScreenSnapshotNoSurface 验证有会话但无 surface 时返回 available=false。
func TestChatDebugScreenSnapshotNoSurface(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return &ChatSession{} }
	defer func() { chatDebugDisplaySessionProvider = old }()

	snap := BuildChatDebugScreenSnapshot()
	if snap.Available {
		t.Fatal("无 surface 时应返回 available=false")
	}
	if snap.Reason != "no active terminal surface" {
		t.Fatalf("reason 应为 'no active terminal surface'，实际为 %q", snap.Reason)
	}
}

// TestChatDebugScreenSnapshotWithSurface 验证有 surface 的会话返回完整快照。
func TestChatDebugScreenSnapshotWithSurface(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)

	session := &ChatSession{Surface: surface}
	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	defer func() { chatDebugDisplaySessionProvider = old }()

	snap := BuildChatDebugScreenSnapshot()
	if !snap.Available {
		t.Fatalf("有 surface 时应返回 available=true，reason=%q", snap.Reason)
	}
	if snap.Width != 80 {
		t.Fatalf("width 应为 80，实际为 %d", snap.Width)
	}
	if snap.Height != 24 {
		t.Fatalf("height 应为 24，实际为 %d", snap.Height)
	}
	if len(snap.Lines) != 24 {
		t.Fatalf("lines 应为 24 行，实际为 %d", len(snap.Lines))
	}
	if snap.Text == "" {
		t.Fatal("text 不应为空")
	}
	// 验证行数：每行以 \n 分隔，应有 24 行（末尾 \n 使 Split 得到 25 项，
	// 但 lines 字段是去掉行尾空白的）
	for i, line := range snap.Lines {
		_ = i
		if len(line) > 80 {
			t.Fatalf("line[%d] 长度 %d 超过 width 80", i, len(line))
		}
	}

	// 验证 Text 与 Lines 一致
	expectedText := strings.Join(snap.Lines, "\n")
	if snap.Text != expectedText {
		t.Fatal("Text 应与 Lines 拼接结果一致")
	}
}

// TestBuildChatDebugScreenText 验证纯文本输出。
func TestBuildChatDebugScreenText(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	// 无会话
	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return nil }
	defer func() { chatDebugDisplaySessionProvider = old }()

	text := BuildChatDebugScreenText()
	if !strings.Contains(text, "no active chat session") {
		t.Fatalf("无会话时文本应包含 'no active chat session'，实际为 %q", text)
	}

	// 有 surface
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	chatDebugDisplaySessionProvider = func() *ChatSession {
		return &ChatSession{Surface: surface}
	}
	text = BuildChatDebugScreenText()
	if !strings.HasSuffix(text, "\n") {
		t.Fatal("有 surface 时文本末尾应有换行")
	}
	// 24 行 + 末尾换行 = 24 个 \n
	if strings.Count(text, "\n") != 24 {
		t.Fatalf("应有 24 行，实际有 %d 个换行", strings.Count(text, "\n"))
	}
}

// TestChatDebugScreenPrefersDerivedAppState 验证在 Presenter 迁移模式下，
// /web/api/screen 优先从 UIController 的 AppState 派生完整文本帧，而不是
// 读取为空的 legacy surface 合成帧。该场景复现"SSE 日志正常但页面正文
// 空白"的缺陷：legacy 帧只保留状态行，派生通道才有会话正文。
func TestChatDebugScreenPrefersDerivedAppState(t *testing.T) {
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
	// 挂载几何：派生文本帧必须基于 AppState.Geometry 才能产生行。
	if !actor.Post(ui.Resize{Width: 80, Height: 12, Generation: 1}) {
		t.Fatal("post resize")
	}
	// 注入一个已提交的 assistant 正文 cell，作为派生通道的会话内容。
	const marker = "DERIVED-SCREEN-TEXT-MARKER"
	if !actor.Post(ui.ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
		Revision: 1,
		Cells: []*scene.TranscriptCell{{
			ID: 1, Sequence: 1, Kind: scene.KindAssistant, Source: marker,
			Revision: 1, Phase: scene.CellCommitted,
		}},
	}}) {
		t.Fatal("post transcript")
	}
	actor.WaitIdle()

	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	defer func() { chatDebugDisplaySessionProvider = old }()

	snap := BuildChatDebugScreenSnapshot()
	if !snap.Available {
		t.Fatalf("有 uiActor 时应返回 available=true，reason=%q", snap.Reason)
	}
	if !strings.Contains(snap.Text, marker) {
		t.Fatalf("screen 文本应包含派生通道内容 %q，实际为 %q", marker, snap.Text)
	}
	if snap.Height != 12 {
		t.Fatalf("height 应来自派生几何 12，实际为 %d", snap.Height)
	}
}

// TestMarshalChatDebugScreenJSON 验证 JSON 序列化输出有效。
func TestMarshalChatDebugScreenJSON(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	// 无会话
	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return nil }
	defer func() { chatDebugDisplaySessionProvider = old }()

	body, err := MarshalChatDebugScreenJSON()
	if err != nil {
		t.Fatalf("MarshalChatDebugScreenJSON 失败: %v", err)
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

	// 有 surface
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	chatDebugDisplaySessionProvider = func() *ChatSession {
		return &ChatSession{Surface: surface}
	}
	body, err = MarshalChatDebugScreenJSON()
	if err != nil {
		t.Fatalf("MarshalChatDebugScreenJSON 失败: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("输出不应为空")
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if av, ok := parsed["available"]; !ok || av != true {
		t.Fatal("有 surface 时 JSON 应包含 available=true")
	}
	if w, ok := parsed["width"]; !ok || w.(float64) != 80 {
		t.Fatalf("width 应为 80，实际为 %v", w)
	}
	if h, ok := parsed["height"]; !ok || h.(float64) != 24 {
		t.Fatalf("height 应为 24，实际为 %v", h)
	}
}