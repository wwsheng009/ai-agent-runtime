package commands

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestResumeFirstMessageDoesNotReplayHistory 复现 2026-10-09 现场：
// `aicli resume` 装载完成（历史各出现一次）后，用户发送第一条消息，历史消息
// 被再次 replay（物理终端流里历史标记出现第二次 / Scene 里出现重复历史 cell）。
//
// 走生产路径：
//  1. 启动恢复：replaceRuntimeMessages(canonical) + presentChatStartupSession
//     （loaded handle 形态，与 chat.go 启动调用一致）→ printResumeSuccess
//     → seed 历史 → ReplaceTranscriptAction（装载标记）→ TerminalSession 写入。
//  2. 首条消息：RenderSubmittedUserInput（live 用户 echo，与
//     chat_transcript_renderer.go 的 RenderUser 一致）→ bridge 发布用户 cell 快照
//     → bridge.BeginRun + 一轮 runtime 事件（assistant delta/message/session end）
//     → 首个 delta 触发全量 ReplaceTranscriptAction。
//
// 断言：整个物理字节流里每条历史消息的标记恰好出现一次，且 Scene 中历史 cell
// 不重复；首条消息与首轮回答各恰好一次。
func TestResumeFirstMessageDoesNotReplayHistory(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	chatIsInteractiveTerminal = func() bool { return true }
	t.Cleanup(func() { chatIsInteractiveTerminal = oldInteractive })
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const (
		sessionID = "sess-resume-first-message-replay"
		width     = 96
		height    = 24
	)
	historyMarkers := make([]string, 0, 6)
	var history []runtimetypes.Message
	for index := 0; index < 3; index++ {
		user := fmt.Sprintf("HISTORY-USER-%02d", index)
		assistant := fmt.Sprintf("HISTORY-ASSISTANT-%02d", index)
		history = append(history,
			*runtimetypes.NewUserMessage(user),
			*runtimetypes.NewAssistantMessage(assistant),
		)
		historyMarkers = append(historyMarkers, user, assistant)
	}

	loaded := runtimechat.NewSession("tester")
	loaded.ID = sessionID
	loaded.ReplaceHistory(history)

	session := &ChatSession{
		Provider:       config.Provider{Protocol: "codex"},
		cancelCtx:      context.Background(),
		ChatExecutor:   &fakeChatExecutor{output: "resumed"},
		Formatter:      formatter.NewMarkdownFormatter(false),
		RuntimeSession: loaded,
		Stream:         true,
	}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	if err := replaceRuntimeMessages(session, loaded.GetMessages()); err != nil {
		t.Fatalf("restore runtime messages: %v", err)
	}

	// 阶段 1：启动恢复（生产调用形态，见 chat.go 的 presentChatStartupSession）。
	presentChatStartupSession(session, &chatCommandOptions{OutputFormat: "interactive"}, loaded)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	for _, marker := range historyMarkers {
		if count := strings.Count(terminal.String(), marker); count != 1 {
			t.Fatalf("resume precondition: history marker %q count=%d, want exactly 1\nstream tail:\n%s",
				marker, count, tailString(terminal.String(), 2000))
		}
	}

	// 阶段 2：发送第一条消息（用户 echo + 一轮 runtime 事件），与真实运行一致。
	const (
		turnID   = "turn-first-after-resume"
		streamID = "stream-first-after-resume"
		question = "FIRST-QUESTION-AFTER-RESUME"
		answer   = "FIRST-ANSWER-AFTER-RESUME"
	)
	bridge.startProcessor()
	defer close(bridge.eventQueue)
	bridge.BeginRun()
	coordinator.RenderSubmittedUserInput(question)
	for _, event := range []runtimeevents.Event{
		{Type: runtimechat.EventSessionStart, SessionID: sessionID, Payload: map[string]interface{}{"turn_id": turnID}},
		{Type: runtimechat.EventLLMRequestStarted, SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "step": 1,
		}},
		{Type: runtimechat.EventAssistantDelta, SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "step": 1, "sequence": uint64(1), "delta": answer,
		}},
		{Type: runtimechat.EventLLMRequestFinished, SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "step": 1, "success": true,
		}},
		{Type: runtimechat.EventAssistantMessage, SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "content": answer,
		}},
		{Type: runtimechat.EventSessionEnd, SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "success": true,
		}},
	} {
		bridge.Handle(event)
	}
	bridge.WaitForCurrentEvents(5 * time.Second)
	bridge.EndRun()
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	// 断言 1：物理字节流中历史消息不得再次出现。
	stream := terminal.String()
	for _, marker := range historyMarkers {
		if count := strings.Count(stream, marker); count != 1 {
			t.Fatalf("first message after resume replayed history: marker %q count=%d, want exactly 1\nstream tail:\n%s",
				marker, count, tailString(stream, 3000))
		}
	}
	// 断言 2：首条消息与首轮回答各恰好一次。
	if count := strings.Count(stream, question); count != 1 {
		t.Fatalf("first question marker count=%d, want exactly 1\nstream tail:\n%s", count, tailString(stream, 3000))
	}
	if count := strings.Count(stream, answer); count != 1 {
		t.Fatalf("first answer marker count=%d, want exactly 1\nstream tail:\n%s", count, tailString(stream, 3000))
	}
	// 断言 3：Scene 中历史 cell 不重复。
	state := coordinator.uiActor.AppState()
	for _, marker := range historyMarkers {
		assertTranscriptSourceCount(t, state.Transcript.Cells, marker, 1)
	}
	assertTranscriptSourceCount(t, state.Transcript.Cells, question, 1)
	assertTranscriptSourceCount(t, state.Transcript.Cells, answer, 1)
}

// tailString 截取尾部字节用于失败时定位重放位置。
func tailString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}

// TestResumeFirstMessageLazyBridgeStartDoesNotReplayHistory 复现 2026-10-09 真机
// 主现场（回归护栏）：能力面（runtime host / EventBus）在首帧之后异步装载
// （chat.go:prepareChatCapabilitiesAsync），resume 装载时 session.LocalRuntimeHost
// 仍为 nil。若事件日志重放只挂在 bridge.start() 的 host 守卫后面，resume 时不会
// 执行；等用户发送第一条消息、actor 首次 ensureChatRuntimeEventBridge 挂上 host
// 后才重放——resetCanonicalHistoryProjectionLocked 换新 Scene/encoder（新 cell
// 身份），ledger 里已交付的 canonical 身份不再匹配，规划重新铸造整段历史并追加
// 进原生 scrollback，即「发送第一条消息后历史消息再次 replay」。
//
// 断言：重放必须在装载阶段（首条消息之前、canonical seed 之前）完成，且首条
// 消息边界不得让历史再次落进终端投影。
func TestResumeFirstMessageLazyBridgeStartDoesNotReplayHistory(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	chatIsInteractiveTerminal = func() bool { return true }
	t.Cleanup(func() { chatIsInteractiveTerminal = oldInteractive })
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const (
		sessionID     = "sess-resume-lazy-bridge-start"
		width, height = 96, 24
		historyTurns  = 4
	)
	var history []runtimetypes.Message
	historyMarkers := make([]string, 0, historyTurns*2)
	for index := 0; index < historyTurns; index++ {
		user := fmt.Sprintf("LAZY-HISTORY-USER-%02d", index)
		assistant := fmt.Sprintf("LAZY-HISTORY-ASSISTANT-%02d", index)
		history = append(history,
			*runtimetypes.NewUserMessage(user),
			*runtimetypes.NewAssistantMessage(assistant),
		)
		historyMarkers = append(historyMarkers, user, assistant)
	}

	// 事件日志 fixture：同一会话的完整历史（用户输入注入 + assistant 终态事件），
	// 供首条消息边界那次延迟的 bridge.start() 全量重放。
	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")
	fixture := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: sessionID}})
	fixture.eventLogPathOverride = logPath
	for index := 0; index < historyTurns; index++ {
		fixture.submitUserInput(fmt.Sprintf("LAZY-HISTORY-USER-%02d", index))
		fixture.encodeRenderModelEvent(runtimeevents.Event{
			Type:      runtimechat.EventAssistantMessage,
			SessionID: sessionID,
			Payload: map[string]interface{}{
				"turn_id": fmt.Sprintf("turn-history-%02d", index),
				"content": fmt.Sprintf("LAZY-HISTORY-ASSISTANT-%02d", index),
			},
		})
	}

	session := &ChatSession{
		Provider:       config.Provider{Protocol: "codex"},
		cancelCtx:      context.Background(),
		ChatExecutor:   &fakeChatExecutor{output: "resumed"},
		Formatter:      formatter.NewMarkdownFormatter(false),
		RuntimeSession: &runtimechat.Session{ID: sessionID},
		Stream:         true,
	}
	bridge := newChatRuntimeEventBridge(session)
	bridge.eventLogPathOverride = logPath
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	if err := replaceRuntimeMessages(session, history); err != nil {
		t.Fatalf("restore runtime messages: %v", err)
	}

	// resume 装载：能力面尚未就绪（LocalRuntimeHost == nil），start() 空转，
	// canonical 历史照常 seed + 交付。
	presentChatStartupSession(session, &chatCommandOptions{OutputFormat: "interactive"}, session.RuntimeSession)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	if _, _, replayed, _ := bridge.eventLogStats(); replayed == 0 {
		t.Fatalf("resume load did not replay the session event log before the first message; " +
			"a late replay (first turn) resets the Scene identities and replays the delivered history")
	}
	for _, marker := range historyMarkers {
		if count := strings.Count(terminal.String(), marker); count != 1 {
			t.Fatalf("resume precondition: history marker %q count=%d, want exactly 1", marker, count)
		}
	}

	// 首条消息边界：能力面异步装载完成（host/EventBus 就绪），actor 首次
	// ensureChatRuntimeEventBridge → start() 执行事件日志全量重放。
	session.LocalRuntimeHost = &localChatRuntimeHost{EventBus: runtimeevents.NewBus()}
	ensureChatRuntimeEventBridge(session)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	// 断言：重放不得把历史再次写入终端投影。
	screen := vt.NewScreen(width, height)
	screen.Feed(terminal.String())
	projected := strings.Join(append(screen.ScrollbackLines(), screen.Lines(1, height)...), "\n")
	for _, marker := range historyMarkers {
		if count := strings.Count(projected, marker); count != 1 {
			t.Fatalf("lazy bridge start at the first message replayed history: marker %q count=%d, want exactly 1\nprojection tail:\n%s",
				marker, count, tailString(projected, 4000))
		}
	}
}

// TestResumeWindowedBackfillFirstMessageDoesNotReplayHistory 复现窗口化装载
// （多页大会话）下的同款现场：resume 首帧只同步装载最新一页并把原生 scrollback
// 交付挂起（DeferHistoryDelivery，等后台补齐较早页后一次性按序铸出）。
//
// 若在补齐完成前发送第一条消息，首个 assistant 增量会走 causal 快照路径
// （chat_ui_actor.go 的 postCausalUIActionWithContext，携带的
// ReplaceTranscriptAction 没有 DeferHistoryDelivery 字段 = false），把挂起
// 提前释放：已装载的最新一页历史在「发送第一条消息」这一刻被整段铸出到原生
// scrollback（用户可见的历史再次 replay）；随后补齐的较早页前插到已交付内容
// 之前，又会让已确认前缀失效（ReconciliationRequired）。
//
// 断言：补齐完成后，每条历史消息在终端投影里恰好出现一次，且按时间升序排列。
func TestResumeWindowedBackfillFirstMessageDoesNotReplayHistory(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	chatIsInteractiveTerminal = func() bool { return true }
	t.Cleanup(func() { chatIsInteractiveTerminal = oldInteractive })
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const (
		width, height = 100, 24
		messageCount  = 150 // 超过单页（HistoryPageMessages=100）→ 触发窗口化装载
	)

	storage, err := runtimechat.NewSQLiteSessionStorage(runtimechat.DefaultPersistentSessionStorageConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("new sqlite storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.CloseStorage() })
	manager := runtimechat.NewSessionManager(storage, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	t.Cleanup(manager.Stop)

	ctx := context.Background()
	loaded, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	markers := make([]string, 0, messageCount)
	messages := make([]runtimetypes.Message, 0, messageCount)
	for index := 0; index < messageCount; index++ {
		marker := fmt.Sprintf("WINDOW-HIST-%03d", index)
		markers = append(markers, marker)
		if index%2 == 0 {
			messages = append(messages, *runtimetypes.NewUserMessage(marker))
		} else {
			messages = append(messages, *runtimetypes.NewAssistantMessage(marker))
		}
	}
	loaded.ReplaceHistory(messages)
	if err := storage.Save(ctx, loaded); err != nil {
		t.Fatalf("save session: %v", err)
	}

	session := &ChatSession{
		Provider:       config.Provider{Protocol: "codex"},
		cancelCtx:      context.Background(),
		ChatExecutor:   &fakeChatExecutor{output: "resumed"},
		Formatter:      formatter.NewMarkdownFormatter(false),
		RuntimeSession: loaded,
		Stream:         true,
		SessionManager: manager,
		SessionUserID:  "tester",
	}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	if err := replaceRuntimeMessages(session, loaded.GetMessages()); err != nil {
		t.Fatalf("restore runtime messages: %v", err)
	}

	// 生产启动形态：同步装载最新一页 + 登记较早页后台补齐（交付挂起）。
	loadResumeCanonicalHistoryForStartup(session, loaded.ID)
	if !session.resumeHistoryBackfillInFlightNow() {
		t.Fatalf("windowed resume did not hold native scrollback delivery: %+v", session.resumeHistorySnapshot())
	}
	presentChatStartupSession(session, &chatCommandOptions{OutputFormat: "interactive"}, loaded)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	// 发送第一条消息（补齐仍在飞），与真实运行一致。
	const (
		turnID   = "turn-windowed-first"
		streamID = "stream-windowed-first"
		question = "WINDOW-FIRST-QUESTION"
		answer   = "WINDOW-FIRST-ANSWER"
	)
	bridge.startProcessor()
	defer close(bridge.eventQueue)
	bridge.BeginRun()
	coordinator.RenderSubmittedUserInput(question)
	for _, event := range []runtimeevents.Event{
		{Type: runtimechat.EventSessionStart, SessionID: loaded.ID, Payload: map[string]interface{}{"turn_id": turnID}},
		{Type: runtimechat.EventLLMRequestStarted, SessionID: loaded.ID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "step": 1,
		}},
		{Type: runtimechat.EventAssistantDelta, SessionID: loaded.ID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "step": 1, "sequence": uint64(1), "delta": answer,
		}},
		{Type: runtimechat.EventLLMRequestFinished, SessionID: loaded.ID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "step": 1, "success": true,
		}},
		{Type: runtimechat.EventAssistantMessage, SessionID: loaded.ID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "content": answer,
		}},
		{Type: runtimechat.EventSessionEnd, SessionID: loaded.ID, Payload: map[string]interface{}{
			"turn_id": turnID, "success": true,
		}},
	} {
		bridge.Handle(event)
	}
	bridge.WaitForCurrentEvents(5 * time.Second)
	bridge.EndRun()
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	// 后台补齐收尾：前插较早页 + 释放挂起 + 装载收尾的授权式替换
	// （与 completeDeferredResumeHistoryLoad 的顺序一致）。
	remaining := messageCount - len(session.resumeHistorySnapshot())
	if remaining <= 0 {
		t.Fatalf("precondition: expected an older page to backfill, loaded=%d", len(session.resumeHistorySnapshot()))
	}
	earlierPage := &runtimechat.SessionHistoryPage{
		Messages: append([]runtimetypes.Message(nil), messages[:remaining]...),
	}
	if !session.prependResumeHistoryPage(session.resumeHistoryGenerationValue(), earlierPage) {
		t.Fatal("prepend earlier page failed")
	}
	session.clearResumeHistoryBackfillInFlight()
	printVisibleSessionLoadHistory(session, "")
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	// 终端投影（scrollback + 视口）：每条历史消息恰好一次，且按时间升序。
	screen := vt.NewScreen(width, height)
	screen.Feed(terminal.String())
	projected := strings.Join(append(screen.ScrollbackLines(), screen.Lines(1, height)...), "\n")
	lastAt := -1
	for _, marker := range markers {
		count := strings.Count(projected, marker)
		if count != 1 {
			t.Fatalf("windowed resume + first message replayed history: marker %q count=%d, want exactly 1\nprojection tail:\n%s",
				marker, count, tailString(projected, 4000))
		}
		at := strings.Index(projected, marker)
		if at < lastAt {
			t.Fatalf("history marker %q rendered out of order (at=%d, previous=%d)\nprojection tail:\n%s",
				marker, at, lastAt, tailString(projected, 4000))
		}
		lastAt = at
	}
	if count := strings.Count(projected, question); count != 1 {
		t.Fatalf("first question marker count=%d, want exactly 1", count)
	}
	if count := strings.Count(projected, answer); count != 1 {
		t.Fatalf("first answer marker count=%d, want exactly 1", count)
	}
}
