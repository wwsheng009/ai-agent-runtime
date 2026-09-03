package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestDispatchChatCommandSimpleDocumentsStayOnUnifiedTerminalSession(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	t.Cleanup(manager.Stop)

	session := &ChatSession{
		SessionManager: manager,
		SessionUserID:  userID,
	}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(88, 24)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	terminal.Reset()

	// stdout is process-global and other terminal-focused tests use asynchronous
	// painters. Observe the owned renderers instead of capturing stdout, which
	// would make unrelated ANSI paint traffic look like a command regression.
	// /new 是会话边界：它必须清空渲染数据面，因此 /new 之前的简单命令文档
	// 只在其前验证，/new 之后的文档在清空后的 transcript 上验证。
	for _, command := range []string{"/help", "/function", "/functions", "/sessions"} {
		if dispatchChatCommand(session, command, false) {
			t.Fatalf("%s unexpectedly requested chat exit", command)
		}
	}

	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	commit := func() string {
		var transcript strings.Builder
		for _, cell := range coordinator.uiActor.AppState().Transcript.Cells {
			transcript.WriteString(cell.Source)
			transcript.WriteByte('\n')
		}
		return transcript.String()
	}
	transcript := commit()
	for _, marker := range []string{
		"可用命令:",
		"错误: 需要指定 function 名称",
		"错误: 需要提供 prompt 预览最终暴露集合",
		"暂无可用会话",
	} {
		if !strings.Contains(transcript, marker) {
			t.Fatalf("semantic transcript is missing %q after simple commands:\n%s", marker, transcript)
		}
	}

	if dispatchChatCommand(session, "/new", false) {
		t.Fatal("/new unexpectedly requested chat exit")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	transcript = commit()
	if strings.Contains(transcript, "可用命令:") {
		t.Fatalf("/new did not clear pre-new command documents from the render plane:\n%s", transcript)
	}
	for _, marker := range []string{
		"已创建新会话",
	} {
		if !strings.Contains(transcript, marker) {
			t.Fatalf("semantic transcript is missing %q after /new:\n%s", marker, transcript)
		}
	}

	for _, command := range []string{"/session", "/history"} {
		if dispatchChatCommand(session, command, false) {
			t.Fatalf("%s unexpectedly requested chat exit", command)
		}
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	transcript = commit()
	for _, marker := range []string{
		"当前会话",
		"当前会话暂无历史消息",
	} {
		if !strings.Contains(transcript, marker) {
			t.Fatalf("semantic transcript is missing %q after /new:\n%s", marker, transcript)
		}
	}
	if !strings.Contains(terminal.String(), "当前会话暂无历史消息") {
		t.Fatalf("TerminalSession did not render the last simple command result: %q", terminal.String())
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("simple unified commands populated legacy historyWindow: %#v", got)
	}
}

func TestTryExecuteStructuredChatCommandHistoryReplaysInsteadOfFallingThrough(t *testing.T) {
	session := &ChatSession{Messages: nil}
	result, handled, err := tryExecuteStructuredChatCommand(session, "/history")
	if err != nil || !handled {
		t.Fatalf("/history structured match=(%t, %v), want handled", handled, err)
	}
	plain := ui.RenderDocumentPlain(result.Document())
	if !strings.Contains(plain, "当前会话暂无历史消息") {
		t.Fatalf("/history empty document missing message: %q", plain)
	}
	if _, handled, err := tryExecuteStructuredChatCommand(session, "/history extra"); err != nil || handled {
		t.Fatalf("/history extra structured match=(%t, %v), want legacy unknown-command behavior", handled, err)
	}
}

func TestTryExecuteStructuredChatCommandHistoryOpensUnifiedTranscriptView(t *testing.T) {
	session := &ChatSession{}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	if err := replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("show prior work"),
		*runtimetypes.NewAssistantMessage("prior answer"),
	}); err != nil {
		t.Fatalf("replace runtime messages: %v", err)
	}

	result, handled, err := tryExecuteStructuredChatCommand(session, "/history")
	if err != nil || !handled {
		t.Fatalf("/history structured match=(%t, %v), want handled", handled, err)
	}
	if !result.OpenTranscript {
		t.Fatalf("unified /history did not request the transcript reader: %+v", result)
	}
	if strings.TrimSpace(ui.RenderDocumentPlain(result.Document())) != "" {
		t.Fatalf("unified /history should open a reader, not append a duplicate command document: %q", ui.RenderDocumentPlain(result.Document()))
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.AppState()
	for _, want := range []string{"show prior work", "prior answer"} {
		found := false
		for _, cell := range state.Transcript.Cells {
			if cell.Source == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("unified /history did not reconcile %q into Scene: %+v", want, state.Transcript.Cells)
		}
	}
	for _, cell := range state.Transcript.Cells {
		if cell.Kind == scene.KindAssistant && strings.HasPrefix(cell.Source, ui.AssistantStreamMarker()) {
			t.Fatalf("history/replay assistant source contains event chrome: %+v", cell)
		}
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified /history populated legacy historyWindow: %#v", got)
	}
}
