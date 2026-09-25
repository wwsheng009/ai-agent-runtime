package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// sessionConfirmationMetaLabels 是曾经跟在会话切换确认（/resume、/load、/new）
// 后面进入 TUI 信息流的会话元数据行。这三条命令都只给一行确认摘要；这些行属于
// /session、/debug display 这类按需视图，不得再回到信息流。
var sessionConfirmationMetaLabels = []string{
	"Session:",
	"Session File:",
	"Session Store:",
	"Chat Log File:",
	"Debug Log File:",
	"HTTP Artifact Dir:",
	"Shell Artifact Dir:",
	"Title:",
	"Compact Gen:",
	"Compact Root:",
	"Compact Root ID:",
	"History:",
}

// newSessionConfirmationMetaFixture 构造一个「旧实现必然打印全部元数据行」的
// 会话：标题、compact 血统、消息计数、会话目录全部非空。
func newSessionConfirmationMetaFixture(t *testing.T) *ChatSession {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "session_switch_meta_fixture"
	runtimeSession.State = runtimechat.StateActive
	runtimeSession.Metadata.Title = "会话切换确认夹具"
	runtimeSession.Metadata.Context = map[string]interface{}{
		runtimechat.ContextCompactGeneration:    13,
		runtimechat.ContextCompactRootTitle:     "会话切换确认夹具",
		runtimechat.ContextCompactRootSessionID: runtimeSession.ID,
	}
	runtimeSession.ReplaceHistory([]runtimetypes.Message{
		{Role: "user", Content: "继续上次任务", Metadata: runtimetypes.NewMetadata()},
		{Role: "assistant", Content: "好的，我先回顾上下文。", Metadata: runtimetypes.NewMetadata()},
	})

	session := &ChatSession{
		SessionUserID:  "tester",
		SessionDir:     t.TempDir(),
		RuntimeSession: runtimeSession,
	}
	session.Messages = runtimeSession.History
	return session
}

func assertSessionConfirmationStreamClean(t *testing.T, stream string) {
	t.Helper()
	for _, label := range sessionConfirmationMetaLabels {
		if strings.Contains(stream, label) {
			t.Fatalf("session confirmation leaked session meta row %q into the message stream:\n%s", label, stream)
		}
	}
}

func TestBuildChatResumeDocumentOmitsSessionMetaBlock(t *testing.T) {
	session := newSessionConfirmationMetaFixture(t)

	plain := ui.RenderDocumentPlain(buildChatResumeDocument(session))
	for _, marker := range []string{"已恢复历史会话", "会话切换确认夹具", "compact #13", "1轮/2条消息"} {
		if !strings.Contains(plain, marker) {
			t.Fatalf("resume confirmation lost %q:\n%s", marker, plain)
		}
	}
	assertSessionConfirmationStreamClean(t, plain)
}

func TestPrintResumeSuccessUnifiedOmitsSessionMetaBlock(t *testing.T) {
	session := newSessionConfirmationMetaFixture(t)
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	interaction := newTestChatInteractionCoordinator(t, session)
	session.Interaction = interaction

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(88, 40)
	interaction.SetSurface(surface)
	var terminal bytes.Buffer
	if !interaction.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)

	printResumeSuccess(session)

	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)

	state := interaction.uiActor.AppState()
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	text := transcript.String()
	if !strings.Contains(text, "已恢复历史会话") {
		t.Fatalf("unified resume confirmation missing from transcript:\n%s", text)
	}
	if !strings.Contains(text, "继续上次任务") {
		t.Fatalf("unified resume did not replay history:\n%s", text)
	}
	assertSessionConfirmationStreamClean(t, text)
}
