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
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
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

// TestChatDebugScreenTranscriptFallbackWithoutGeometry 复现 Win7 无 surface
// 启动形态：uiActor 存在、transcript 有语义 cell，但 geometry 为 0（没有终端
// 尺寸可派生布局）。此时必须回退到 transcriptFallbackText，从语义 cell 派生
// 纯文本，而不是返回 "no active terminal surface" 死信号。这是 web 客户端
// 屏幕镜像通道在 headless/后台服务下的数据平面权威来源（app_state.go 语义
// cell 存储，与几何无关）。
func TestChatDebugScreenTranscriptFallbackWithoutGeometry(t *testing.T) {
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
	// 不 Post Resize：geometry 保持 0，模拟无 surface / headless 启动。
	// 注入混合语义 cell（user + reasoning + assistant + tool）。
	const (
		userMarker   = "FALLBACK-USER-MARKER"
		reasonMarker = "FALLBACK-REASON-MARKER"
		asstMarker   = "FALLBACK-ASSISTANT-MARKER"
		toolMarker   = "FALLBACK-TOOL-MARKER"
	)
	if !actor.Post(ui.ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
		Revision: 1,
		Cells: []*scene.TranscriptCell{
			{ID: 1, Sequence: 1, Kind: scene.KindUser, Source: userMarker,
				Revision: 1, Phase: scene.CellCommitted},
			{ID: 2, Sequence: 2, Kind: scene.KindReasoning, Source: reasonMarker,
				Revision: 1, Phase: scene.CellCommitted},
			{ID: 3, Sequence: 3, Kind: scene.KindToolChain, Source: toolMarker,
				Revision: 1, Phase: scene.CellCommitted},
			{ID: 4, Sequence: 4, Kind: scene.KindAssistant, Source: asstMarker,
				Revision: 1, Phase: scene.CellCommitted},
		},
	}}) {
		t.Fatal("post transcript")
	}
	actor.WaitIdle()

	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	defer func() { chatDebugDisplaySessionProvider = old }()

	snap := BuildChatDebugScreenSnapshot()
	if !snap.Available {
		t.Fatalf("无 geometry 但有 transcript 时应 available=true，reason=%q", snap.Reason)
	}
	for _, marker := range []string{userMarker, reasonMarker, asstMarker, toolMarker} {
		if !strings.Contains(snap.Text, marker) {
			t.Fatalf("screen 文本应包含 %q，实际为 %q", marker, snap.Text)
		}
	}
	// 语义回退应带角色前缀，顺序与注入一致（对话时序）。
	if !strings.HasPrefix(snap.Text, "user> "+userMarker) {
		t.Fatalf("首个 cell 应为 user 前缀，实际为 %q", snap.Text)
	}
	// 无 geometry：Height 保持 0（回退通道不伪造终端帧尺寸）。
	if snap.Height != 0 {
		t.Fatalf("回退通道 height 应为 0，实际为 %d", snap.Height)
	}

	// 纯文本端点：不再输出 "Debug Screen:" 死信号。
	text := BuildChatDebugScreenText()
	if strings.Contains(text, "Debug Screen:") {
		t.Fatalf("回退通道不应输出 Debug Screen 提示，实际为 %q", text)
	}
	if !strings.Contains(text, asstMarker) {
		t.Fatalf("text 端点应包含 assistant 正文，实际为 %q", text)
	}
}

// TestChatDebugScreenBridgeSceneFallback 复现无 surface + 无 uiActor 同步的
// 启动形态（Win7 降级 / headless：unifiedRenderer 未启用，postTranscript
// 被 UnifiedRendererEnabled 门控，uiActor 收不到快照）。此时屏幕镜像必须
// 从 bridge 的 Scene 快照回退——bridge 订阅 EventBus，事件流到达即构建
// Scene（applyChangeSet），不依赖 unifiedRenderer 门控。
func TestChatDebugScreenBridgeSceneFallback(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	bridge.session = session
	session.RuntimeEventBridge = bridge

	// 通过 encode + applyChangeSet 构建 Scene（模拟事件流到达 bridge）。
	const marker = "BRIDGE-SCENE-MARKER"
	bridge.renderMu.Lock()
	bridge.applyChangeSet(bridge.renderEncoder.Encode(runtimeevents.Event{
		Type: runtimechat.EventLLMRequestStarted,
		Payload: map[string]interface{}{
			"turn_id": "turn-1", "stream_id": "stream-1",
		},
	}))
	bridge.applyChangeSet(bridge.renderEncoder.Encode(runtimeevents.Event{
		Type: runtimechat.EventAssistantDelta,
		Payload: map[string]interface{}{
			"turn_id": "turn-1", "stream_id": "stream-1", "delta": marker, "sequence": uint64(1),
		},
	}))
	bridge.renderMu.Unlock()

	// 无 surface、无 uiActor、无 Interaction：仅 bridge Scene 有内容。
	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	defer func() { chatDebugDisplaySessionProvider = old }()

	snap := BuildChatDebugScreenSnapshot()
	if !snap.Available {
		t.Fatalf("仅 bridge Scene 有内容时应 available=true，reason=%q", snap.Reason)
	}
	if !strings.Contains(snap.Text, marker) {
		t.Fatalf("screen 文本应包含 bridge Scene 内容 %q，实际为 %q", marker, snap.Text)
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

// TestChatDebugScreenSessionTranscriptFallback 复现 Win7 降级形态下点击会话
// 后的场景：无 surface、无 uiActor（unifiedRenderer 未启用）、无 bridge
// Scene（历史消息不重放 live events）。resume 后历史消息只存在于
// session.Messages / RuntimeSession.History，屏幕镜像必须直接从会话
// transcript 兜底派生，否则 web 页面无法加载历史消息。
func TestChatDebugScreenSessionTranscriptFallback(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{
		Messages: []types.Message{
			{Role: "user", Content: "你好"},
			{Role: "assistant", Content: "你好！我是助手。"},
			{Role: "tool", Content: "tool-call-result"},
			{Role: "system", Content: "system-note"},
			{Role: "assistant", Content: "  "},
		},
	}

	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	defer func() { chatDebugDisplaySessionProvider = old }()

	snap := BuildChatDebugScreenSnapshot()
	if !snap.Available {
		t.Fatalf("仅会话 transcript 有内容时应 available=true，reason=%q", snap.Reason)
	}
	for _, want := range []string{"user> 你好", "你好！我是助手。", "[tool] tool-call-result", "[system] system-note"} {
		if !strings.Contains(snap.Text, want) {
			t.Fatalf("screen 文本应包含 %q，实际为 %q", want, snap.Text)
		}
	}

	// RuntimeSession.History 兜底路径。
	session.Messages = nil
	session.RuntimeSession = &runtimechat.Session{
		History: []types.Message{
			{Role: "user", Content: "history-question"},
			{Role: "assistant", Content: "history-answer"},
		},
	}
	snap = BuildChatDebugScreenSnapshot()
	if !snap.Available {
		t.Fatalf("RuntimeSession.History 有内容时应 available=true，reason=%q", snap.Reason)
	}
	if !strings.Contains(snap.Text, "history-question") || !strings.Contains(snap.Text, "history-answer") {
		t.Fatalf("screen 文本应包含 RuntimeSession.History 内容，实际为 %q", snap.Text)
	}

	// 全空：应回退到 "no active terminal surface" 死信号。
	session.RuntimeSession = nil
	snap = BuildChatDebugScreenSnapshot()
	if snap.Available {
		t.Fatal("会话内容为空且无 surface 时应 available=false")
	}
	if snap.Reason != "no active terminal surface" {
		t.Fatalf("空会话 reason 应为 no active terminal surface，实际为 %q", snap.Reason)
	}
}

// TestChatWebScreenSnapshotFullTranscriptNotViewportClipped 验证 web 客户端
// 屏幕端点（buildChatWebScreenSnapshot）返回完整语义 transcript，而不是像
// BuildChatDebugScreenSnapshot（第一优先派生通道）那样按终端视口高度裁剪。
//
// 缺陷复现：resume 历史会话后，完整历史注入 uiActor transcript，但
// LayoutAppScreen 只保留最后 OutputBottomRow 行（终端视口），web 页面因此
// 只显示最后一个 turn。web 端必须始终从语义 cells 派生全文，与 geometry 无关。
func TestChatWebScreenSnapshotFullTranscriptNotViewportClipped(t *testing.T) {
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
	// 小视口（高 4 行）：debug 派生帧必然裁剪掉更早的 turn 内容。
	if !actor.Post(ui.Resize{Width: 80, Height: 4, Generation: 1}) {
		t.Fatal("post resize")
	}

	const firstMarker = "WEB-FIRST-TURN-MARKER"
	const lastMarker = "WEB-LAST-TURN-MARKER"
	cells := []*scene.TranscriptCell{
		{ID: 1, Sequence: 1, Kind: scene.KindUser, Source: firstMarker,
			Revision: 1, Phase: scene.CellCommitted},
	}
	// 中间填充足够多的多行 cell，使视口无法容纳（模拟长历史会话）。
	id := 2
	for i := 0; i < 12; i++ {
		cells = append(cells,
			&scene.TranscriptCell{ID: scene.CellID(id), Sequence: uint64(id), Kind: scene.KindAssistant,
				Source:   "filler-line-1\nfiller-line-2\nfiller-line-3",
				Revision: 1, Phase: scene.CellCommitted})
		id++
	}
	cells = append(cells,
		&scene.TranscriptCell{ID: scene.CellID(id), Sequence: uint64(id), Kind: scene.KindAssistant,
			Source:   lastMarker + "\ntail",
			Revision: 1, Phase: scene.CellCommitted})

	if !actor.Post(ui.ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
		Revision: 1,
		Cells:    cells,
	}}) {
		t.Fatal("post transcript")
	}
	actor.WaitIdle()

	old := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	defer func() { chatDebugDisplaySessionProvider = old }()

	// debug 派生帧：受视口裁剪，最早的 turn marker 丢失。
	debugSnap := BuildChatDebugScreenSnapshot()
	if !debugSnap.Available {
		t.Fatalf("debug screen 应 available=true，reason=%q", debugSnap.Reason)
	}
	if strings.Contains(debugSnap.Text, firstMarker) {
		t.Fatalf("debug 视口帧不应包含早期 turn %q（已被视口裁剪），实际 text=%q", firstMarker, debugSnap.Text)
	}

	// web 快照：完整 transcript，最早与最后的 marker 都必须存在。
	webSnap := buildChatWebScreenSnapshot()
	if !webSnap.Available {
		t.Fatalf("web screen 应 available=true，reason=%q", webSnap.Reason)
	}
	if !strings.Contains(webSnap.Text, firstMarker) {
		t.Fatalf("web screen 应包含早期 turn %q，实际 text=%q", firstMarker, webSnap.Text)
	}
	if !strings.Contains(webSnap.Text, lastMarker) {
		t.Fatalf("web screen 应包含最后 turn %q，实际 text=%q", lastMarker, webSnap.Text)
	}
	// 结构化消息（role + content）必须完整派生：首个 user 消息与最后的
	// assistant 消息都应按 role 归位，供 web 客户端做气泡渲染。
	if len(webSnap.Messages) == 0 {
		t.Fatal("web screen 应派生结构化 Messages，实际为空")
	}
	var firstUser *chatWebScreenMessage
	var lastAssistant *chatWebScreenMessage
	for i := range webSnap.Messages {
		m := &webSnap.Messages[i]
		if m.Role == "user" && strings.Contains(m.Content, firstMarker) && firstUser == nil {
			firstUser = m
		}
		if m.Role == "assistant" && strings.Contains(m.Content, lastMarker) {
			lastAssistant = m
		}
	}
	if firstUser == nil {
		t.Fatalf("Messages 应包含 role=user 且内容含 %q 的消息，实际 messages=%v",
			firstMarker, webSnap.Messages)
	}
	if lastAssistant == nil {
		t.Fatalf("Messages 应包含 role=assistant 且内容含 %q 的消息，实际 messages=%v",
			lastMarker, webSnap.Messages)
	}
}
