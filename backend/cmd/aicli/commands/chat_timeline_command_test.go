package commands

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimeagent "github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestTimelineCommandStaysOnUnifiedTerminalSession(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	session.Interaction = interaction

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(88, 24)
	interaction.SetSurface(surface)
	var terminal bytes.Buffer
	if !interaction.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	terminal.Reset()

	if dispatchChatCommand(session, "/timeline", false) {
		t.Fatal("/timeline unexpectedly requested chat exit")
	}
	// Guard the direct helper as well: internal callers must not regain the
	// legacy stdout path once TerminalSession owns the TTY.
	printChatTimeline(session, "/timeline 1")

	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	state := interaction.uiActor.AppState()
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	if strings.Count(transcript.String(), "Collab Timeline:") != 2 {
		t.Fatalf("expected dispatch and direct timeline cells, got:\n%s", transcript.String())
	}
	if !strings.Contains(transcript.String(), "<none>") {
		t.Fatalf("timeline semantic result is missing the empty-state row:\n%s", transcript.String())
	}
	if strings.Contains(transcript.String(), "正在迁移到统一渲染器") {
		t.Fatalf("/timeline was still rejected by the unified command gate:\n%s", transcript.String())
	}
	if !strings.Contains(terminal.String(), "Collab Timeline:") {
		t.Fatalf("TerminalSession did not render /timeline: %q", terminal.String())
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified /timeline populated legacy historyWindow: %#v", got)
	}
}

func TestCollabSnapshotCommandStaysOnUnifiedTerminalSession(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	session.Interaction = interaction

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(88, 24)
	interaction.SetSurface(surface)
	var terminal bytes.Buffer
	if !interaction.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	terminal.Reset()

	if dispatchChatCommand(session, "/collab", false) {
		t.Fatal("/collab unexpectedly requested chat exit")
	}
	printChatCollab(session, "/collab 1")
	if dispatchChatCommand(session, "/collab follow", false) {
		t.Fatal("/collab follow unexpectedly requested chat exit")
	}

	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	state := interaction.uiActor.AppState()
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	if strings.Count(transcript.String(), "Parent Mailbox Timeline:") != 2 {
		t.Fatalf("expected dispatch and direct collab snapshot cells, got:\n%s", transcript.String())
	}
	for _, marker := range []string{
		"<none>",
		"/collab follow 需要持续观察 effect",
	} {
		if !strings.Contains(transcript.String(), marker) {
			t.Fatalf("collab semantic transcript is missing %q:\n%s", marker, transcript.String())
		}
	}
	if strings.Contains(transcript.String(), "正在迁移到统一渲染器") {
		t.Fatalf("/collab snapshot was still rejected by the unified command gate:\n%s", transcript.String())
	}
	if !strings.Contains(terminal.String(), "Parent Mailbox Timeline:") {
		t.Fatalf("TerminalSession did not render /collab: %q", terminal.String())
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified /collab populated legacy historyWindow: %#v", got)
	}
}

func TestTrustStatusCommandStaysOnUnifiedTerminalSession(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{
		FolderTrust: foldertrust.Resolution{
			FeatureEnabled: false,
			Source:         "feature_off",
			WorkspaceKey:   "test-workspace",
		},
	}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	session.Interaction = interaction

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(88, 24)
	interaction.SetSurface(surface)
	var terminal bytes.Buffer
	if !interaction.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	terminal.Reset()

	if dispatchChatCommand(session, "/trust", false) {
		t.Fatal("/trust unexpectedly requested chat exit")
	}
	// The direct handler is protected by the same unified semantic result.
	if handleTrustCommand(session, "/trust status") {
		t.Fatal("direct unified /trust status unexpectedly requested chat exit")
	}
	if dispatchChatCommand(session, "/trust grant", false) {
		t.Fatal("/trust grant unexpectedly requested chat exit")
	}

	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	state := interaction.uiActor.AppState()
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	if strings.Count(transcript.String(), "Folder trust:") != 3 {
		t.Fatalf("expected dispatch, direct, and grant-status cells, got:\n%s", transcript.String())
	}
	for _, marker := range []string{
		"feature_off (project scope allowed)",
		"/trust grant 需要确认交互",
	} {
		if !strings.Contains(transcript.String(), marker) {
			t.Fatalf("trust semantic transcript is missing %q:\n%s", marker, transcript.String())
		}
	}
	if !strings.Contains(terminal.String(), "Folder trust:") {
		t.Fatalf("TerminalSession did not render /trust: %q", terminal.String())
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified /trust populated legacy historyWindow: %#v", got)
	}
}

func TestDebugReadOnlyCommandsStayOnUnifiedTerminalSession(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	session.Interaction = interaction

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(88, 24)
	interaction.SetSurface(surface)
	var terminal bytes.Buffer
	if !interaction.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	terminal.Reset()

	if dispatchChatCommand(session, "/debug", false) {
		t.Fatal("/debug unexpectedly requested chat exit")
	}
	if dispatchChatCommand(session, "/debug routing", false) {
		t.Fatal("/debug routing unexpectedly requested chat exit")
	}
	if handleDebugCommand(session, "/debug status") {
		t.Fatal("direct unified /debug status unexpectedly requested chat exit")
	}
	if handleDebugCommand(session, "/debug on") {
		t.Fatal("direct unified /debug on unexpectedly requested chat exit")
	}

	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	state := interaction.uiActor.AppState()
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	if strings.Count(transcript.String(), "Debug Mode:") != 3 {
		t.Fatalf("expected dispatch, direct status, and toggle cells, got:\n%s", transcript.String())
	}
	for _, marker := range []string{
		"Subagent Routing:",
	} {
		if !strings.Contains(transcript.String(), marker) {
			t.Fatalf("debug semantic transcript is missing %q:\n%s", marker, transcript.String())
		}
	}
	if !session.DebugMode {
		t.Fatal("unified /debug on did not update the debug-mode state")
	}
	if !strings.Contains(terminal.String(), "Debug Mode:") {
		t.Fatalf("TerminalSession did not render /debug: %q", terminal.String())
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified /debug populated legacy historyWindow: %#v", got)
	}
}

func TestAgentsSnapshotCommandStaysOnUnifiedTerminalSession(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	session.Interaction = interaction

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(88, 24)
	interaction.SetSurface(surface)
	var terminal bytes.Buffer
	if !interaction.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	terminal.Reset()

	if dispatchChatCommand(session, "/agents", false) {
		t.Fatal("/agents unexpectedly requested chat exit")
	}
	printChatAgents(session)
	handleChatAgentsCommand(session, "/agents panel")

	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	state := interaction.uiActor.AppState()
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	if strings.Count(transcript.String(), "Agent Graph:") != 2 {
		t.Fatalf("expected dispatch and direct agents snapshots, got:\n%s", transcript.String())
	}
	if !strings.Contains(transcript.String(), "/agents 的交互、发送和路由子命令尚未迁移") {
		t.Fatalf("agents semantic rejection missing:\n%s", transcript.String())
	}
	if !strings.Contains(terminal.String(), "Agent Graph:") {
		t.Fatalf("TerminalSession did not render /agents: %q", terminal.String())
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified /agents populated legacy historyWindow: %#v", got)
	}
}

func TestCompactCommandStaysOnUnifiedTerminalSession(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	originalRunner := runManualChatCompact
	t.Cleanup(func() { runManualChatCompact = originalRunner })
	runManualChatCompact = func(*ChatSession, string) (*chatCompactReport, error) {
		return &chatCompactReport{
			RequestedMode: compactruntime.ModeRemote,
			Result: &compactruntime.Result{
				Mode:              compactruntime.ModeRemote,
				TokenBefore:       900,
				TokenAfter:        120,
				CompactedMessages: 4,
			},
			Status: compactruntime.Status{Mode: compactruntime.ModeRemote},
		}, nil
	}

	session := &ChatSession{TokenCount: 5000, TurnContextTokenCount: 999}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	session.Interaction = interaction

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(88, 24)
	interaction.SetSurface(surface)
	var terminal bytes.Buffer
	if !interaction.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	terminal.Reset()

	if dispatchChatCommand(session, "/compact remote", false) {
		t.Fatal("/compact unexpectedly requested chat exit")
	}
	if handleCompactCommand(session, "/compact remote") {
		t.Fatal("direct unified /compact unexpectedly requested chat exit")
	}

	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	state := interaction.uiActor.AppState()
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	if strings.Count(transcript.String(), "压缩完成") != 2 {
		t.Fatalf("expected dispatch and direct compact cells, got:\n%s", transcript.String())
	}
	if !strings.Contains(terminal.String(), "压缩完成") {
		t.Fatalf("TerminalSession did not render /compact: %q", terminal.String())
	}
	if session.ContextTokenCount != 120 || session.TurnContextTokenCount != 0 {
		t.Fatalf("compact did not reconcile context usage: context=%d turn=%d", session.ContextTokenCount, session.TurnContextTokenCount)
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified /compact populated legacy historyWindow: %#v", got)
	}
}

func TestRetryCommandRestoresComposerThroughUnifiedPostCommitEffect(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	session.Interaction = interaction
	rememberChatTurnRecovery(session, "恢复这条失败请求", true)

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(88, 24)
	interaction.SetSurface(surface)
	var terminal bytes.Buffer
	if !interaction.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	terminal.Reset()

	if dispatchChatCommand(session, "/retry", false) {
		t.Fatal("/retry unexpectedly requested chat exit")

	}

	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	if got := interaction.PromptInputSnapshot().Text; got != "恢复这条失败请求" {
		t.Fatalf("retry did not restore actor-owned composer draft: %q", got)
	}
	state := interaction.uiActor.AppState()
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	for _, marker := range []string{"已恢复上一条中断消息", "工具可能已部分执行", "当前未执行任何操作"} {
		if !strings.Contains(transcript.String(), marker) {
			t.Fatalf("retry semantic result is missing %q:\n%s", marker, transcript.String())
		}
	}
	if !strings.Contains(terminal.String(), "已恢复上一条中断消息") {
		t.Fatalf("TerminalSession did not render /retry: %q", terminal.String())
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified /retry populated legacy historyWindow: %#v", got)
	}
}

func TestModelStatusCommandStaysOnUnifiedTerminalSession(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{ProviderName: "alpha", Model: "gpt-test", ReasoningEffort: "medium"}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	session.Interaction = interaction

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(88, 24)
	interaction.SetSurface(surface)
	var terminal bytes.Buffer
	if !interaction.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	terminal.Reset()

	if dispatchChatCommand(session, "/model status", false) {
		t.Fatal("/model status unexpectedly requested chat exit")
	}
	printRuntimeModelState(session)
	if dispatchChatCommand(session, "/model", false) {
		t.Fatal("/model unexpectedly requested chat exit")
	}

	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	state := interaction.uiActor.AppState()
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	if strings.Count(transcript.String(), "当前 provider: alpha") != 3 {
		t.Fatalf("expected dispatch status, direct status and bare-fallback status cells, got:\n%s", transcript.String())
	}
	// Bare /model has no interactive picker on this non-TTY test terminal, so it
	// degrades to the read-only status document instead of a legacy prompt or
	// the old migration fence.
	if !strings.Contains(transcript.String(), "当前模型: gpt-test") {
		t.Fatalf("bare /model did not fall back to the status document:\n%s", transcript.String())
	}
	if strings.Contains(transcript.String(), "正在迁移到统一渲染器") {
		t.Fatalf("bare /model still reports the migration fence:\n%s", transcript.String())
	}
	if !strings.Contains(terminal.String(), "当前 provider: alpha") {
		t.Fatalf("TerminalSession did not render /model status: %q", terminal.String())
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified /model status populated legacy historyWindow: %#v", got)
	}
}

func TestThemeQueriesStayOnUnifiedTerminalSession(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	previousPalette := ui.CurrentThemeName()
	previousMode := ui.CurrentThemeModeName()
	previousSyntax := ui.CurrentSyntaxThemeName()
	t.Cleanup(func() {
		_ = ui.ApplyThemeSelection(previousPalette, previousMode)
		_ = ui.SetSyntaxTheme(previousSyntax)
	})
	if err := ui.ApplyThemeSelection(ui.ThemePresetFocus, ui.ThemeModeDark); err != nil {
		t.Fatal(err)
	}

	session := &ChatSession{}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	session.Interaction = interaction

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(88, 24)
	interaction.SetSurface(surface)
	var terminal bytes.Buffer
	if !interaction.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	terminal.Reset()

	for _, command := range []string{"/theme status", "/theme list", "/theme preview", "/theme syntax"} {
		if dispatchChatCommand(session, command, false) {
			t.Fatalf("%s unexpectedly requested chat exit", command)
		}
	}
	if handleThemeCommand(session, "/theme preview", false) {
		t.Fatal("direct unified /theme preview unexpectedly requested chat exit")
	}
	if dispatchChatCommand(session, "/theme select", false) {
		t.Fatal("/theme select unexpectedly requested chat exit")
	}

	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	state := interaction.uiActor.AppState()
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	for _, marker := range []string{
		"当前明暗: dark",
		"明暗模式:",
		"主题预览: mode=dark",
		"func Hello(n int) string",
		"当前配色:",
	} {
		if !strings.Contains(transcript.String(), marker) {
			t.Fatalf("theme semantic transcript is missing %q:\n%s", marker, transcript.String())
		}
	}
	// /theme select has no interactive picker on this non-TTY test terminal, so
	// it degrades to the read-only status document instead of a legacy prompt
	// or the old migration fence.
	if strings.Contains(transcript.String(), "正在迁移到统一渲染器") {
		t.Fatalf("/theme still reports the migration fence:\n%s", transcript.String())
	}
	if strings.Contains(transcript.String(), "\x1b[") {
		t.Fatalf("theme document retained pre-encoded ANSI bytes:\n%q", transcript.String())
	}
	if !strings.Contains(terminal.String(), "主题预览: mode=dark") {
		t.Fatalf("TerminalSession did not render /theme preview: %q", terminal.String())
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified /theme query populated legacy historyWindow: %#v", got)
	}
}

func TestSkillCatalogQueriesStayOnUnifiedTerminalSession(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	catalog.RegisterSkillFunction(&SkillFunction{
		functionName: "skill__imagegen",
		skill: &runtimeskill.Skill{
			Name:        "imagegen",
			Description: "Generate images from a prompt",
		},
	})

	session := &ChatSession{FunctionCatalog: catalog, FunctionRegistry: registry}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	session.Interaction = interaction

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(88, 24)
	interaction.SetSurface(surface)
	var terminal bytes.Buffer
	if !interaction.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	terminal.Reset()

	for _, command := range []string{"/skills image", "/skills list"} {
		if dispatchChatCommand(session, command, false) {
			t.Fatalf("%s unexpectedly requested chat exit", command)
		}
	}
	if handleSkillsMenuCommand(session, "/skills image") {
		t.Fatal("direct unified /skills query unexpectedly requested chat exit")
	}
	if dispatchChatCommand(session, "/skills", false) {
		t.Fatal("bare /skills unexpectedly requested chat exit")
	}

	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	state := interaction.uiActor.AppState()
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	if count := strings.Count(transcript.String(), "Skill Catalog: total=1"); count != 4 {
		t.Fatalf("expected dispatch, list, direct, and bare-fallback catalog cells; count=%d:\n%s", count, transcript.String())
	}
	for _, marker := range []string{
		"Filter: image",
		"imagegen",
	} {
		if !strings.Contains(transcript.String(), marker) {
			t.Fatalf("skills semantic transcript is missing %q:\n%s", marker, transcript.String())
		}
	}
	// Bare /skills has no interactive picker on this non-TTY test terminal, so
	// it degrades to the read-only catalog report instead of a legacy prompt or
	// the old migration fence.
	if strings.Contains(transcript.String(), "正在迁移到统一渲染器") {
		t.Fatalf("bare /skills still reports the migration fence:\n%s", transcript.String())
	}
	if !strings.Contains(terminal.String(), "Skill Catalog: total=1") {
		t.Fatalf("TerminalSession did not render /skills catalog: %q", terminal.String())
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified /skills catalog populated legacy historyWindow: %#v", got)
	}
}

func TestBacktrackQueriesStayOnUnifiedTerminalSession(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ctx := context.Background()
	storage := runtimechat.NewInMemoryStorage()
	runtimeSession := runtimechat.NewSession("backtrack-query-user")
	runtimeSession.ID = "unified-backtrack-query"
	runtimeSession.AddMessage(*runtimetypes.NewUserMessage("first prompt"))
	runtimeSession.AddMessage(*runtimetypes.NewAssistantMessage("first answer"))
	runtimeSession.AddMessage(*runtimetypes.NewUserMessage("second prompt"))
	if err := storage.Save(ctx, runtimeSession); err != nil {
		t.Fatalf("save backtrack session: %v", err)
	}
	hub := runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        runtimeagent.NewAgent(&runtimeagent.Config{Name: "backtrack-query"}, nil),
			SessionStore: storage,
			StateStore:   runtimechat.NewInMemoryRuntimeStore(8),
		})
	})
	t.Cleanup(hub.StopAll)

	session := &ChatSession{
		RuntimeSession:   runtimeSession,
		LocalRuntimeHost: &localChatRuntimeHost{SessionHub: hub},
	}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	session.Interaction = interaction

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(88, 80)
	interaction.SetSurface(surface)
	var terminal bytes.Buffer
	if !interaction.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	terminal.Reset()

	for _, command := range []string{
		"/backtrack list",
		"/backtrack audit",
		`/backtrack 1 --edit "second revised"`,
	} {
		if dispatchChatCommand(session, command, false) {
			t.Fatalf("%s unexpectedly requested chat exit", command)
		}
	}
	if handleBacktrackCommand(session, "/backtrack list") {
		t.Fatal("direct unified /backtrack list unexpectedly requested chat exit")
	}
	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	beforeApply := interaction.uiActor.AppState()
	var queryTranscript strings.Builder
	for _, cell := range beforeApply.Transcript.Cells {
		queryTranscript.WriteString(cell.Source)
		queryTranscript.WriteByte('\n')
	}
	for _, marker := range []string{
		"[1] msg#2 second prompt",
		"backtrack audit: (empty)",
		"backtrack preview: turn=1",
		"edit: second revised",
	} {
		if !strings.Contains(queryTranscript.String(), marker) {
			t.Fatalf("backtrack query semantic transcript is missing %q:\n%s", marker, queryTranscript.String())
		}
	}
	if dispatchChatCommand(session, "/backtrack 1 --apply", false) {
		t.Fatal("/backtrack apply unexpectedly requested chat exit")
	}

	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	state := interaction.uiActor.AppState()
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	for _, marker := range []string{
		"已回退到 user turn 1：上方旧消息已失效",
		"backtrack apply: turn=1",
	} {
		if !strings.Contains(transcript.String(), marker) {
			t.Fatalf("backtrack semantic transcript is missing %q:\n%s", marker, transcript.String())
		}
	}
	if !strings.Contains(terminal.String(), "backtrack preview: turn=1") {
		t.Fatalf("TerminalSession did not render /backtrack preview: %q", terminal.String())
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified /backtrack query populated legacy historyWindow: %#v", got)
	}
}

func TestDirectResumeTargetsStayOnUnifiedTerminalSession(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	manager := newLoadCommandTestManager(t, "resume-unified-session")
	session := &ChatSession{
		SessionManager: manager,
		SessionUserID:  "tester",
	}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	session.Interaction = interaction

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(88, 80)
	interaction.SetSurface(surface)
	var terminal bytes.Buffer
	if !interaction.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	terminal.Reset()

	if dispatchChatCommand(session, "/resume latest", false) {
		t.Fatal("/resume latest unexpectedly requested chat exit")
	}
	// The direct compatibility entry point must route its finite current-id
	// response through the same retained command-cell boundary.
	if handleResumeCommand(session, "/resume resume-unified-session") {
		t.Fatal("direct unified /resume unexpectedly requested chat exit")
	}
	// This test runs without a real TTY, so bare /resume remains fail-closed;
	// the compatible ANSI-TTY path is covered by the typed picker state tests.
	if dispatchChatCommand(session, "/resume", false) {
		t.Fatal("bare /resume unexpectedly requested chat exit")
	}

	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	state := interaction.uiActor.AppState()
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	for _, marker := range []string{
		"已恢复历史会话: Load fixture",
		"我先检查目录。",
		"当前已经在该会话中，无需恢复",
		"错误: /resume 正在迁移到统一渲染器，已拒绝旧终端直写",
	} {
		if !strings.Contains(transcript.String(), marker) {
			t.Fatalf("resume semantic transcript is missing %q:\n%s", marker, transcript.String())
		}
	}
	if !strings.Contains(terminal.String(), "已恢复历史会话: Load fixture") {
		t.Fatalf("TerminalSession did not render /resume latest: %q", terminal.String())
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified /resume populated legacy historyWindow: %#v", got)
	}
}

func TestResumePickerRequestCarriesCwdFilter(t *testing.T) {
	base := ChatSessionListFilter{
		Query: "keep-query",
		Limit: 7,
	}
	target, filter, err := parseResumeCommandArgument("--cwd", base, &ChatSession{})
	if err != nil {
		t.Fatalf("parse /resume --cwd: %v", err)
	}
	if target != "" {
		t.Fatalf("cwd picker target=%q want empty", target)
	}
	if strings.TrimSpace(filter.Workspace) == "" {
		t.Fatal("cwd picker did not resolve a workspace filter")
	}
	if filter.Query != base.Query || filter.Limit != base.Limit {
		t.Fatalf("cwd picker lost existing filters: got %#v want query=%q limit=%d", filter, base.Query, base.Limit)
	}

	result := newResumePickerCommandResult(filter)
	if result.OpenResumePicker == nil {
		t.Fatalf("/resume --cwd did not produce a typed picker request: %#v", result)
	}
	if result.OpenResumePicker.Filter != filter {
		t.Fatalf("picker request did not preserve parsed filter: got %#v want %#v", result.OpenResumePicker.Filter, filter)
	}
}
