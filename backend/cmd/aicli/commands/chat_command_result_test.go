package commands

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

func TestTryExecuteStructuredChatCommandDebugDisplay(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{
		ProviderName: "openai",
		Model:        "gpt-test",
	}

	for _, command := range []string{"/debug display", "/debug show", "/debug info"} {
		result, handled, err := tryExecuteStructuredChatCommand(session, command)
		if err != nil {
			t.Fatalf("%s returned error: %v", command, err)
		}
		if !handled {
			t.Fatalf("%s was not handled as a structured command", command)
		}
		if result.Action != CommandContinue {
			t.Fatalf("%s action=%v want CommandContinue", command, result.Action)
		}
		if len(result.Blocks) != 1 {
			t.Fatalf("%s blocks=%d want 1", command, len(result.Blocks))
		}
		plain := ui.RenderDocumentPlain(result.Document())
		for _, marker := range []string{
			"会话文件与目录:",
			"运行时调试:",
			"Subagent Routing:",
			"AgentControl Registry:",
			"Agent Graph:",
			"Mailbox Pending:",
		} {
			if !strings.Contains(plain, marker) {
				t.Fatalf("%s document missing %q:\n%s", command, marker, plain)
			}
		}
		if strings.HasPrefix(plain, "\n") || strings.HasSuffix(plain, "\n") {
			t.Fatalf("%s document owns a top-level boundary blank: %q", command, plain)
		}
	}

	for _, command := range []string{"/debug", "/debug status", "/debug routing", "/debug display --output x"} {
		if _, handled, err := tryExecuteStructuredChatCommand(session, command); err != nil || handled {
			t.Fatalf("%s structured match=(%t, %v), want legacy", command, handled, err)
		}
	}
}

func TestTryExecuteStructuredChatCommandMigratesFiniteComposerCommands(t *testing.T) {
	session := &ChatSession{
		PermissionMode:    "default",
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
		ImagePaths:        []string{"first.png", "second.png"},
	}

	assertDocument := func(command, want string) {
		t.Helper()
		result, handled, err := tryExecuteStructuredChatCommand(session, command)
		if err != nil || !handled {
			t.Fatalf("%s structured match=(%t, %v), want handled", command, handled, err)
		}
		if got := ui.RenderDocumentPlain(result.Document()); !strings.Contains(got, want) {
			t.Fatalf("%s document missing %q: %q", command, want, got)
		}
	}

	assertDocument("/queue", "当前 queued input: 0 pending")
	assertDocument("/attach", "待发送图片附件 (2):")
	assertDocument("/attach remove 1", "已移除图片附件: first.png")
	if got := session.ImagePaths; len(got) != 1 || got[0] != "second.png" {
		t.Fatalf("/attach remove left paths=%#v", got)
	}
	assertDocument("/attach clear", "已清空 1 个待发送图片附件")
	if len(session.ImagePaths) != 0 {
		t.Fatalf("/attach clear left paths=%#v", session.ImagePaths)
	}

	assertDocument("/permission-mode", "当前 permission-mode: default")
	assertDocument("/permission-mode plan", "已切换到 permission-mode=plan")
	if session.PermissionMode != "plan" {
		t.Fatalf("permission mode=%q want plan", session.PermissionMode)
	}
	assertDocument("/permission-mode bypass_permissions", "需要确认交互")
	assertDocument("/approval-reuse", "当前 approval-reuse: session_readonly_shell")
	assertDocument("/approval-reuse off", "已切换到 approval-reuse=off")
}

func TestCommandResultMergesBlocksIntoOneAtomicCommandCell(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	var output bytes.Buffer
	coord.SetWriter(&output)
	result := CommandResult{Blocks: []RenderBlock{
		{Document: render.SingleLineDoc(render.TextSpan("first-block"))},
		{Document: render.SingleLineDoc(render.TextSpan("second-block"))},
	}}
	if err := renderChatCommandResult(session, result, false); err != nil {
		t.Fatalf("renderChatCommandResult: %v", err)
	}
	if coord.commandCellSequence != 1 {
		t.Fatalf("command cell sequence=%d want 1", coord.commandCellSequence)
	}
	if coord.lastCommandCellID != "command:1" {
		t.Fatalf("command cell id=%q want command:1", coord.lastCommandCellID)
	}
	if got := output.String(); strings.Count(got, "first-block") != 1 || strings.Count(got, "second-block") != 1 {
		t.Fatalf("merged command output was not emitted exactly once:\n%q", got)
	}
}

func TestDispatchChatCommandDebugDisplayDoesNotWriteRawStdout(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{
		ProviderName: "openai",
		Model:        "gpt-test",
	}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	var retained bytes.Buffer
	coord.SetWriter(&retained)
	raw := captureStdout(t, func() {
		if dispatchChatCommand(session, "/debug display", false) {
			t.Fatal("/debug display unexpectedly requested chat exit")
		}
	})
	if raw != "" {
		t.Fatalf("structured /debug display wrote raw stdout:\n%q", raw)
	}
	if coord.commandCellSequence != 1 {
		t.Fatalf("structured /debug display committed %d cells, want 1", coord.commandCellSequence)
	}
	output := retained.String()
	if count := strings.Count(output, "会话文件与目录:"); count != 1 {
		t.Fatalf("debug marker count=%d want 1:\n%s", count, output)
	}
	if !strings.Contains(output, "Mailbox Pending:") {
		t.Fatalf("atomic debug cell is missing its tail:\n%s", output)
	}
}

func TestUnifiedInteractiveLegacyCommandsAreFencedBeforeLegacyHandlers(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 20)
	surface.SetPhysicalWritesEnabled(false)
	coord.SetSurface(surface)
	var terminal bytes.Buffer
	if !coord.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	terminal.Reset()

	commands := []struct {
		input string
		name  string
	}{
		{input: "/rewind 0", name: "/backtrack"},
		{input: "/resume", name: "/resume"},
		{input: "/login", name: "/login"},
		{input: "/agents panel", name: "/agents 的交互、发送和路由子命令尚未迁移到统一渲染命令通道。"},
	}
	raw := captureStdout(t, func() {
		for _, test := range commands {
			if dispatchChatCommand(session, test.input, false) {
				t.Fatalf("%s unexpectedly requested chat exit", test.input)
			}
		}
	})
	if raw != "" {
		t.Fatalf("unified legacy command fence wrote raw stdout:\n%q", raw)
	}

	coord.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coord)
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified command fence populated legacy historyWindow: %#v", got)
	}
	snapshot := bridge.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != len(commands) {
		count := 0
		if snapshot != nil {
			count = len(snapshot.Cells)
		}
		t.Fatalf("semantic command cells=%d want %d", count, len(commands))
	}
	for index, test := range commands {
		marker := "错误: " + test.name + " 正在迁移到统一渲染器，已拒绝旧终端直写"
		if test.input == "/agents panel" {
			marker = "错误: " + test.name
		}
		if !strings.Contains(snapshot.Cells[index].Source, marker) {
			t.Fatalf("cell[%d] for %s did not contain fence marker %q: %+v", index, test.input, marker, snapshot.Cells[index])
		}
	}
	if !strings.Contains(terminal.String(), "已拒绝旧终端直写") {
		t.Fatalf("TerminalSession did not render the semantic fence: %q", terminal.String())
	}
}

func TestDispatchChatCommandDebugDisplaySurvivesOwnedViewportRepaints(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const width, height = 100, 120
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	session := &ChatSession{
		ProviderName: "openai",
		Model:        "gpt-test",
		Surface:      surface,
	}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord
	coord.SetSurface(surface)
	screen := newScreenVT(width, height)
	feed := func(paint func()) {
		t.Helper()
		screen.feed(captureSurfaceStdout(t, func() {
			coord.SetWriter(os.Stdout)
			paint()
		}))
	}

	feed(func() {
		coord.PrintPrompt()
	})
	feed(func() {
		if dispatchChatCommand(session, "/debug display", false) {
			t.Fatal("/debug display unexpectedly requested chat exit")
		}
	})
	assertSingleDebugCommandMarker(t, "initial command frame", surface, screen)

	feed(func() {
		surface.SetStatusModels(style.StatusLineModel{State: style.RunReady}, nil)
		surface.ShowPrompt("> ")
	})
	assertSingleDebugCommandMarker(t, "status and prompt repaint", surface, screen)

	feed(func() {
		surface.SetActiveBand([]string{"• Running structured command check", "  retained active row"})
	})
	assertSingleDebugCommandMarker(t, "active band growth", surface, screen)

	feed(func() {
		surface.ClearActiveBand()
	})
	assertSingleDebugCommandMarker(t, "active band shrink", surface, screen)

	surface.EnableForTest(88, height)
	if frame := commandResultFrameText(surface); strings.Count(frame, "Mailbox Pending:") != 1 {
		t.Fatalf("resize recompose lost or duplicated debug command marker:\n%s", frame)
	}
}

func TestTryExecuteStructuredChatCommandGoal(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}

	for _, command := range []string{"/goal", "/goal status"} {
		result, handled, err := tryExecuteStructuredChatCommand(session, command)
		if err != nil || !handled {
			t.Fatalf("%s structured match=(%t, %v), want handled", command, handled, err)
		}
		if result.SendObjective != "" {
			t.Fatalf("%s unexpectedly set SendObjective %q", command, result.SendObjective)
		}
		plain := ui.RenderDocumentPlain(result.Document())
		if !strings.Contains(plain, "当前会话未设置 goal") {
			t.Fatalf("%s document missing empty-goal marker:\n%s", command, plain)
		}
	}

	for _, command := range []string{"/goal --json", "/goal status --json", "/goal ship it --json"} {
		if _, handled, err := tryExecuteStructuredChatCommand(session, command); err != nil || handled {
			t.Fatalf("%s structured match=(%t, %v), want legacy", command, handled, err)
		}
	}

	for _, command := range []string{"/goal clear", "/goal pause", "/goal resume", "/goal complete", "/goal ship it"} {
		if _, handled, err := tryExecuteStructuredChatCommand(session, command); err != nil || handled {
			t.Fatalf("%s without persistence structured match=(%t, %v), want legacy", command, handled, err)
		}
	}

	persistent, cleanup := newGoalCommandTestSession(t)
	defer cleanup()

	result, handled, err := tryExecuteStructuredChatCommand(persistent, "/goal implement structured output")
	if err != nil || !handled {
		t.Fatalf("/goal set structured match=(%t, %v), want handled", handled, err)
	}
	if result.SendObjective != "implement structured output" {
		t.Fatalf("/goal set SendObjective=%q, want the objective", result.SendObjective)
	}
	plain := ui.RenderDocumentPlain(result.Document())
	if !strings.Contains(plain, "Goal 已设置") || !strings.Contains(plain, "implement structured output") {
		t.Fatalf("/goal set document missing confirmation or objective:\n%s", plain)
	}

	result, handled, err = tryExecuteStructuredChatCommand(persistent, "/goal pause")
	if err != nil || !handled {
		t.Fatalf("/goal pause structured match=(%t, %v), want handled", handled, err)
	}
	if !strings.Contains(ui.RenderDocumentPlain(result.Document()), "Goal 已暂停") {
		t.Fatalf("/goal pause document missing confirmation:\n%s", ui.RenderDocumentPlain(result.Document()))
	}
	assertGoalStatus(t, persistent, runtimegoal.StatusPaused)

	result, handled, err = tryExecuteStructuredChatCommand(persistent, "/goal complete")
	if err != nil || !handled {
		t.Fatalf("/goal complete structured match=(%t, %v), want handled", handled, err)
	}
	assertGoalStatus(t, persistent, runtimegoal.StatusComplete)

	if _, handled, err := tryExecuteStructuredChatCommand(persistent, "/goal resume"); err != nil || handled {
		t.Fatalf("/goal resume after complete structured match=(%t, %v), want legacy rejection", handled, err)
	}

	result, handled, err = tryExecuteStructuredChatCommand(persistent, "/goal")
	if err != nil || !handled {
		t.Fatalf("/goal status after mutations structured match=(%t, %v), want handled", handled, err)
	}
	plain = ui.RenderDocumentPlain(result.Document())
	if !strings.Contains(plain, "Goal:") || !strings.Contains(plain, "implement structured output") {
		t.Fatalf("/goal status document missing goal summary:\n%s", plain)
	}
}

func TestTryExecuteStructuredChatCommandMemory(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	root := t.TempDir()
	session := &ChatSession{
		ProfileRoot: root,
		RuntimeSession: &runtimechat.Session{
			ID: "memory-cmd-test",
		},
	}

	result, handled, err := tryExecuteStructuredChatCommand(session, "/memory status")
	if err != nil || !handled {
		t.Fatalf("/memory status structured match=(%t, %v), want handled", handled, err)
	}
	plain := ui.RenderDocumentPlain(result.Document())
	if !strings.Contains(plain, "项目记忆 root=") || !strings.Contains(plain, "total=0") {
		t.Fatalf("/memory status document missing root/total:\n%s", plain)
	}

	result, handled, err = tryExecuteStructuredChatCommand(session, "/memory add Prefer worktree isolation for parallel agents")
	if err != nil || !handled {
		t.Fatalf("/memory add structured match=(%t, %v), want handled", handled, err)
	}
	plain = ui.RenderDocumentPlain(result.Document())
	if !strings.Contains(plain, "已写入项目记忆") || !strings.Contains(plain, "worktree isolation") {
		t.Fatalf("/memory add document missing confirmation:\n%s", plain)
	}

	result, handled, err = tryExecuteStructuredChatCommand(session, "/memory list 5")
	if err != nil || !handled {
		t.Fatalf("/memory list structured match=(%t, %v), want handled", handled, err)
	}
	plain = ui.RenderDocumentPlain(result.Document())
	if !strings.Contains(plain, "最近 1 条项目记忆") || !strings.Contains(plain, "worktree isolation") {
		t.Fatalf("/memory list document missing note:\n%s", plain)
	}

	result, handled, err = tryExecuteStructuredChatCommand(session, "/memory search worktree")
	if err != nil || !handled {
		t.Fatalf("/memory search structured match=(%t, %v), want handled", handled, err)
	}
	if !strings.Contains(ui.RenderDocumentPlain(result.Document()), "worktree") {
		t.Fatalf("/memory search document missing hit:\n%s", ui.RenderDocumentPlain(result.Document()))
	}

	emptyRoot := t.TempDir()
	emptySession := &ChatSession{
		ProfileRoot: emptyRoot,
		RuntimeSession: &runtimechat.Session{
			ID: "memory-cmd-test-empty",
		},
	}
	result, handled, err = tryExecuteStructuredChatCommand(emptySession, "/memory search anything")
	if err != nil || !handled {
		t.Fatalf("/memory search no-hit structured match=(%t, %v), want handled", handled, err)
	}
	if !strings.Contains(ui.RenderDocumentPlain(result.Document()), "未找到") {
		t.Fatalf("/memory search no-hit document missing message:\n%s", ui.RenderDocumentPlain(result.Document()))
	}

	for _, command := range []string{"/memory add", "/memory search", "/memory bogus"} {
		if _, handled, err := tryExecuteStructuredChatCommand(session, command); err != nil || handled {
			t.Fatalf("%s structured match=(%t, %v), want legacy usage", command, handled, err)
		}
	}
	if _, handled, err := tryExecuteStructuredChatCommand(nil, "/memory status"); err != nil || handled {
		t.Fatalf("nil-session /memory status structured match=(%t, %v), want legacy", handled, err)
	}
}

func TestTryExecuteStructuredChatCommandStream(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}

	result, handled, err := tryExecuteStructuredChatCommand(session, "/stream status")
	if err != nil || !handled {
		t.Fatalf("/stream status structured match=(%t, %v), want handled", handled, err)
	}
	plain := ui.RenderDocumentPlain(result.Document())
	if !strings.Contains(plain, "当前输出模式: 普通 (normal)") || !strings.Contains(plain, "配置默认: (未设置)") {
		t.Fatalf("/stream status document missing mode lines:\n%s", plain)
	}

	result, handled, err = tryExecuteStructuredChatCommand(session, "/stream on")
	if err != nil || !handled {
		t.Fatalf("/stream on structured match=(%t, %v), want handled", handled, err)
	}
	if !session.Stream {
		t.Fatal("/stream on did not set session.Stream")
	}
	if !strings.Contains(ui.RenderDocumentPlain(result.Document()), "已切换到流式模式") {
		t.Fatalf("/stream on document missing confirmation:\n%s", ui.RenderDocumentPlain(result.Document()))
	}

	result, handled, err = tryExecuteStructuredChatCommand(session, "/stream")
	if err != nil || !handled {
		t.Fatalf("/stream toggle structured match=(%t, %v), want handled", handled, err)
	}
	if session.Stream {
		t.Fatal("/stream toggle did not flip session.Stream back off")
	}
	if !strings.Contains(ui.RenderDocumentPlain(result.Document()), "已切换到普通模式") {
		t.Fatalf("/stream toggle document missing confirmation:\n%s", ui.RenderDocumentPlain(result.Document()))
	}

	result, handled, err = tryExecuteStructuredChatCommand(session, "/stream bogus")
	if err != nil || !handled {
		t.Fatalf("/stream bogus structured match=(%t, %v), want handled", handled, err)
	}
	if !strings.Contains(ui.RenderDocumentPlain(result.Document()), "用法: /stream") {
		t.Fatalf("/stream bogus document missing usage:\n%s", ui.RenderDocumentPlain(result.Document()))
	}
	result, handled, err = tryExecuteStructuredChatCommand(nil, "/stream status")
	if err != nil || !handled {
		t.Fatalf("nil-session /stream status structured match=(%t, %v), want handled", handled, err)
	}
	if !strings.Contains(ui.RenderDocumentPlain(result.Document()), "当前没有活动会话") {
		t.Fatalf("nil-session /stream document missing error:\n%s", ui.RenderDocumentPlain(result.Document()))
	}
}

func TestTryExecuteStructuredChatCommandFastAndReasoning(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	fast, cfgPath := newFastCommandSession(t, "codex")
	result, handled, err := tryExecuteStructuredChatCommand(fast, "/fast on")
	if err != nil || !handled {
		t.Fatalf("/fast on structured match=(%t, %v), want handled", handled, err)
	}
	if !fast.FastMode {
		t.Fatal("/fast on did not set FastMode")
	}
	if plain := ui.RenderDocumentPlain(result.Document()); !strings.Contains(plain, "已开启 Fast 模式") {
		t.Fatalf("/fast on document missing confirmation:\n%s", plain)
	}
	if stored := loadFastModePreference(t, cfgPath); stored == nil || !*stored {
		t.Fatalf("/fast on did not persist fast_mode=true: %#v", stored)
	}

	result, handled, err = tryExecuteStructuredChatCommand(fast, "/fast status")
	if err != nil || !handled {
		t.Fatalf("/fast status structured match=(%t, %v), want handled", handled, err)
	}
	if plain := ui.RenderDocumentPlain(result.Document()); !strings.Contains(plain, "当前 Fast 模式: on") {
		t.Fatalf("/fast status document missing mode:\n%s", plain)
	}

	unsupported := &ChatSession{}
	result, handled, err = tryExecuteStructuredChatCommand(unsupported, "/fast on")
	if err != nil || !handled {
		t.Fatalf("unsupported /fast structured match=(%t, %v), want handled", handled, err)
	}
	if plain := ui.RenderDocumentPlain(result.Document()); !strings.Contains(plain, "仅支持 codex") {
		t.Fatalf("unsupported /fast document missing protocol error:\n%s", plain)
	}

	reasoning := &ChatSession{SuppressReasoningOutput: true}
	result, handled, err = tryExecuteStructuredChatCommand(reasoning, "/reasoning on")
	if err != nil || !handled {
		t.Fatalf("/reasoning on structured match=(%t, %v), want handled", handled, err)
	}
	if reasoning.SuppressReasoningOutput {
		t.Fatal("/reasoning on did not enable reasoning output")
	}
	if plain := ui.RenderDocumentPlain(result.Document()); !strings.Contains(plain, "当前 reasoning: on") {
		t.Fatalf("/reasoning on document missing status:\n%s", plain)
	}

	result, handled, err = tryExecuteStructuredChatCommand(reasoning, "/reasoning invalid")
	if err != nil || !handled {
		t.Fatalf("invalid /reasoning structured match=(%t, %v), want handled", handled, err)
	}
	if plain := ui.RenderDocumentPlain(result.Document()); !strings.Contains(plain, "用法: /reasoning") {
		t.Fatalf("invalid /reasoning document missing usage:\n%s", plain)
	}
}

func TestTryExecuteStructuredChatCommandReasoningEffort(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg, cfgPath := testModelCommandConfig(t)
	session := &ChatSession{
		ProviderName:    "beta",
		Provider:        cfg.Providers.Items["beta"],
		Model:           "beta-model",
		ReasoningEffort: "low",
		Config:          cfg,
	}

	result, handled, err := tryExecuteStructuredChatCommand(session, "/reasoning_effort max")
	if err != nil || !handled {
		t.Fatalf("/reasoning_effort max structured match=(%t, %v), want handled", handled, err)
	}
	if session.ReasoningEffort != "max" {
		t.Fatalf("reasoning effort=%q want max", session.ReasoningEffort)
	}
	if plain := ui.RenderDocumentPlain(result.Document()); !strings.Contains(plain, "当前 reasoning_effort: max") {
		t.Fatalf("set document missing status:\n%s", plain)
	}
	loaded, err := agentconfig.InitGlobalConfig(cfgPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if loaded.AICLI == nil || loaded.AICLI.Chat == nil || loaded.AICLI.Chat.ReasoningEffort != "max" {
		t.Fatalf("structured set did not persist max: %+v", loaded.AICLI)
	}

	result, handled, err = tryExecuteStructuredChatCommand(session, "/reasoning-effort clear")
	if err != nil || !handled {
		t.Fatalf("/reasoning-effort clear structured match=(%t, %v), want handled", handled, err)
	}
	if session.ReasoningEffort != "" {
		t.Fatalf("reasoning effort=%q want empty", session.ReasoningEffort)
	}
	if plain := ui.RenderDocumentPlain(result.Document()); !strings.Contains(plain, "当前 reasoning_effort: (无)") {
		t.Fatalf("clear document missing status:\n%s", plain)
	}

	result, handled, err = tryExecuteStructuredChatCommand(session, "/reasoning_effort select")
	if err != nil || !handled {
		t.Fatalf("/reasoning_effort select structured match=(%t, %v), want handled", handled, err)
	}
	if plain := ui.RenderDocumentPlain(result.Document()); !strings.Contains(plain, "需要选择器交互") {
		t.Fatalf("select document missing migration guard:\n%s", plain)
	}
}

func TestDispatchChatCommandFiniteModesDoNotWriteRawStdout(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	var retained bytes.Buffer
	coord.SetWriter(&retained)
	raw := captureStdout(t, func() {
		for _, command := range []string{"/stream status", "/s", "/n", "/fast status", "/reasoning off", "/reasoning_effort status"} {
			if dispatchChatCommand(session, command, false) {
				t.Fatalf("%s unexpectedly requested chat exit", command)
			}
		}
	})
	if raw != "" {
		t.Fatalf("finite mode commands wrote raw stdout:\n%q", raw)
	}
	for _, marker := range []string{"当前输出模式:", "已切换到流式模式", "已切换到普通模式", "当前 Fast 模式:", "当前 reasoning: off", "当前 reasoning_effort:"} {
		if count := strings.Count(retained.String(), marker); count != 1 {
			t.Fatalf("marker %q count=%d want 1:\n%s", marker, count, retained.String())
		}
	}
}

func TestDispatchChatCommandGoalDoesNotWriteRawStdout(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	var retained bytes.Buffer
	coord.SetWriter(&retained)
	raw := captureStdout(t, func() {
		if dispatchChatCommand(session, "/goal status", false) {
			t.Fatal("/goal status unexpectedly requested chat exit")
		}
	})
	if raw != "" {
		t.Fatalf("structured /goal status wrote raw stdout:\n%q", raw)
	}
	if coord.commandCellSequence != 1 {
		t.Fatalf("structured /goal status committed %d cells, want 1", coord.commandCellSequence)
	}
	if count := strings.Count(retained.String(), "当前会话未设置 goal"); count != 1 {
		t.Fatalf("goal marker count=%d want 1:\n%s", count, retained.String())
	}
}

func TestDispatchChatCommandGoalSetCommitsCellThenSendsObjective(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session, cleanup := newGoalCommandTestSession(t)
	defer cleanup()
	executor := &fakeChatExecutor{output: "llm accepted goal request"}
	session.ChatExecutor = executor
	session.cancelCtx = context.Background()

	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord
	var retained bytes.Buffer
	coord.SetWriter(&retained)

	if dispatchChatCommand(session, "/goal implement request dispatch", false) {
		t.Fatal("/goal set unexpectedly requested chat exit")
	}
	if !executor.called {
		t.Fatal("expected /goal <objective> to send objective to executor")
	}
	if executor.prompt != "implement request dispatch" {
		t.Fatalf("expected objective prompt, got %q", executor.prompt)
	}
	if coord.commandCellSequence != 1 {
		t.Fatalf("structured /goal set committed %d cells, want 1", coord.commandCellSequence)
	}
	if !strings.Contains(retained.String(), "Goal 已设置") {
		t.Fatalf("confirmation cell missing:\n%s", retained.String())
	}
}

func TestDispatchChatCommandMemoryDoesNotWriteRawStdout(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	root := t.TempDir()
	session := &ChatSession{
		ProfileRoot: root,
		RuntimeSession: &runtimechat.Session{
			ID: "memory-cmd-test",
		},
	}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	var retained bytes.Buffer
	coord.SetWriter(&retained)
	raw := captureStdout(t, func() {
		if dispatchChatCommand(session, "/memory add Prefer worktree isolation for parallel agents", false) {
			t.Fatal("/memory add unexpectedly requested chat exit")
		}
	})
	if raw != "" {
		t.Fatalf("structured /memory add wrote raw stdout:\n%q", raw)
	}
	if coord.commandCellSequence != 1 {
		t.Fatalf("structured /memory add committed %d cells, want 1", coord.commandCellSequence)
	}
	if count := strings.Count(retained.String(), "已写入项目记忆"); count != 1 {
		t.Fatalf("memory add marker count=%d want 1:\n%s", count, retained.String())
	}
}

func TestDispatchChatCommandStreamDoesNotWriteRawStdout(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	var retained bytes.Buffer
	coord.SetWriter(&retained)
	raw := captureStdout(t, func() {
		if dispatchChatCommand(session, "/stream status", false) {
			t.Fatal("/stream status unexpectedly requested chat exit")
		}
	})
	if raw != "" {
		t.Fatalf("structured /stream status wrote raw stdout:\n%q", raw)
	}
	if coord.commandCellSequence != 1 {
		t.Fatalf("structured /stream status committed %d cells, want 1", coord.commandCellSequence)
	}
	if count := strings.Count(retained.String(), "当前输出模式:"); count != 1 {
		t.Fatalf("stream marker count=%d want 1:\n%s", count, retained.String())
	}
}

func TestDispatchChatCommandStreamSurvivesOwnedViewportRepaints(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const width, height = 100, 120
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	session := &ChatSession{
		Surface: surface,
	}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord
	coord.SetSurface(surface)
	screen := newScreenVT(width, height)
	feed := func(paint func()) {
		t.Helper()
		screen.feed(captureSurfaceStdout(t, func() {
			coord.SetWriter(os.Stdout)
			paint()
		}))
	}

	feed(func() {
		coord.PrintPrompt()
	})
	feed(func() {
		if dispatchChatCommand(session, "/stream status", false) {
			t.Fatal("/stream status unexpectedly requested chat exit")
		}
	})
	assertSingleStreamCommandMarker(t, "initial command frame", surface, screen)

	feed(func() {
		surface.SetStatusModels(style.StatusLineModel{State: style.RunReady}, nil)
		surface.ShowPrompt("> ")
	})
	assertSingleStreamCommandMarker(t, "status and prompt repaint", surface, screen)

	feed(func() {
		surface.SetActiveBand([]string{"• Running structured command check", "  retained active row"})
	})
	assertSingleStreamCommandMarker(t, "active band growth", surface, screen)

	feed(func() {
		surface.ClearActiveBand()
	})
	assertSingleStreamCommandMarker(t, "active band shrink", surface, screen)

	surface.EnableForTest(88, height)
	if frame := commandResultFrameText(surface); strings.Count(frame, "当前输出模式:") != 1 {
		t.Fatalf("resize recompose lost or duplicated stream command marker:\n%s", frame)
	}
}

func TestTryExecuteStructuredChatCommandTitle(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "title-command-session"
	session := &ChatSession{RuntimeSession: runtimeSession}

	result, handled, err := tryExecuteStructuredChatCommand(session, "/title Structured title")
	if err != nil {
		t.Fatalf("/title returned error: %v", err)
	}
	if !handled {
		t.Fatal("/title was not handled as a structured command")
	}
	if runtimeSession.Metadata.Title != "Structured title" {
		t.Fatalf("title = %q, want Structured title", runtimeSession.Metadata.Title)
	}
	if got := ui.RenderDocumentPlain(result.Document()); got != "会话标题已更新" {
		t.Fatalf("title confirmation = %q, want exact confirmation", got)
	}

	for _, command := range []string{"/title", "/rename "} {
		if _, handled, err := tryExecuteStructuredChatCommand(session, command); err != nil || handled {
			t.Fatalf("%q structured match=(%t, %v), want legacy validation path", command, handled, err)
		}
	}
}

func TestDispatchChatCommandTitleDoesNotWriteRawStdout(t *testing.T) {
	runtimeSession := runtimechat.NewSession("tester")
	session := &ChatSession{RuntimeSession: runtimeSession}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	var retained bytes.Buffer
	coord.SetWriter(&retained)
	raw := captureStdout(t, func() {
		if dispatchChatCommand(session, "/title No raw stdout", false) {
			t.Fatal("/title unexpectedly requested chat exit")
		}
	})
	if raw != "" {
		t.Fatalf("structured /title wrote raw stdout:\n%q", raw)
	}
	if coord.commandCellSequence != 1 {
		t.Fatalf("structured /title committed %d cells, want 1", coord.commandCellSequence)
	}
	if count := strings.Count(retained.String(), "会话标题已更新"); count != 1 {
		t.Fatalf("title marker count=%d want 1:\n%s", count, retained.String())
	}
}

func TestDispatchChatCommandTitleSurvivesOwnedViewportRepaints(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const width, height = 100, 80
	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "title-repaint-session"
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	session := &ChatSession{
		RuntimeSession: runtimeSession,
		Surface:        surface,
	}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord
	coord.SetSurface(surface)
	screen := newScreenVT(width, height)
	feed := func(paint func()) {
		t.Helper()
		screen.feed(captureSurfaceStdout(t, func() {
			coord.SetWriter(os.Stdout)
			paint()
		}))
	}
	assertSingleTitle := func(stage string) {
		t.Helper()
		frame := commandResultFrameText(surface)
		if count := strings.Count(frame, "会话标题已更新"); count != 1 {
			t.Fatalf("%s composed frame marker count=%d want 1:\n%s", stage, count, frame)
		}
		if rows := screen.RowsContaining("会话标题已更新"); len(rows) != 1 {
			t.Fatalf("%s physical marker rows=%v want one:\n%s", stage, rows, screen.dump())
		}
	}

	feed(func() { coord.PrintPrompt() })
	feed(func() {
		if dispatchChatCommand(session, "/rename Renamed title", false) {
			t.Fatal("/rename unexpectedly requested chat exit")
		}
	})
	assertSingleTitle("initial command frame")

	feed(func() {
		surface.SetStatusModels(style.StatusLineModel{State: style.RunReady}, nil)
		surface.ShowPrompt("> ")
	})
	assertSingleTitle("status and prompt repaint")

	feed(func() {
		surface.SetActiveBand([]string{"• Running title command check", "  retained active row"})
	})
	assertSingleTitle("active band growth")

	feed(func() { surface.ClearActiveBand() })
	assertSingleTitle("active band shrink")

	surface.EnableForTest(88, height)
	assertSingleTitle("resize recompose")
	if runtimeSession.Metadata.Title != "Renamed title" {
		t.Fatalf("runtime title = %q, want Renamed title", runtimeSession.Metadata.Title)
	}
}

func TestStructuredCommandHandlersHaveNoDirectTerminalWriter(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	for _, name := range []string{
		"chat_command_result.go",
		"chat_debug_document.go",
		"chat_fast_document.go",
		"chat_status_document.go",
		"chat_load_document.go",
		"chat_goal_document.go",
		"chat_memory_document.go",
		"chat_reasoning_document.go",
		"chat_simple_command_document.go",
		"chat_stream_document.go",
		"chat_title_document.go",
		"chat_unified_command_gate.go",
	} {
		sourcePath := filepath.Join(filepath.Dir(currentFile), name)
		source, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatalf("read %s: %v", sourcePath, err)
		}
		text := string(source)
		for _, forbidden := range []string{
			"fmt.Print",
			"log.Print",
			"os.Stdout",
			"os.Stderr",
			"ui.Print",
			"Terminal.",
			"WriteTerminalText",
		} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s contains forbidden direct writer %q", name, forbidden)
			}
		}
	}
}

// TestChatInteractiveDirectWriterInventory is the P0 baseline gate for the
// unified-render migration. It deliberately records current legacy writers
// rather than treating them as safe: every direct terminal writer reachable
// from chat*.go or command.go must be listed in this migration-debt baseline.
//
// The gate fails when a new writer is introduced or a legacy writer count
// changes. Migrating a writer means removing its entry; do not add entries for
// new interactive features. Plain/JSON compatibility and startup/shutdown paths
// remain to be classified by the P0 migration ledger.
func TestChatInteractiveDirectWriterInventory(t *testing.T) {
	got := collectChatDirectWriters(t)
	want := chatDirectWriterInventory()
	if diff := diffDirectWriterInventory(want, got); diff != "" {
		t.Fatalf("chat direct-writer inventory changed (-want +got):\n%s", diff)
	}
}

type chatDirectWriter struct {
	File string
	Func string
	Kind string
	Line int
}

type chatDirectWriterInventoryEntry struct {
	File  string
	Func  string
	Kind  string
	Count int
}

func (writer chatDirectWriter) String() string {
	return fmt.Sprintf("%s\t%s\t%s\t%d", writer.File, writer.Func, writer.Kind, writer.Line)
}

func (writer chatDirectWriter) inventoryKey() string {
	return writer.File + "\t" + writer.Func + "\t" + writer.Kind
}

// chatDirectWriterInventory is the grouped P0 audit baseline. It is a debt
// ledger and regression fence, not an authorization for new raw output. Keep
// exact lines in the scanner only: source movement must not churn this baseline.
func chatDirectWriterInventory() []chatDirectWriterInventoryEntry {
	return []chatDirectWriterInventoryEntry{
		{File: "chat.go", Func: "loadRuntimeToolConfig", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat.go", Func: "renderChatResponse", Kind: "fmt.Print", Count: 1},
		{File: "chat.go", Func: "runChatLoop", Kind: "fmt.Print", Count: 2},
		{File: "chat_actor_host.go", Func: "buildLocalChatAgentControlRegistryService", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_actor_host.go", Func: "buildLocalChatGlobalAgentStore", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_actor_host.go", Func: "buildLocalChatGlobalMailboxStore", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_actor_host.go", Func: "buildLocalChatRuntimeStores", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_actor_host.go", Func: "loadLocalChatRuntimeConfig", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_backtrack_command.go", Func: "handleBacktrackAuditList", Kind: "fmt.Print", Count: 7},
		{File: "chat_backtrack_command.go", Func: "handleBacktrackCommand", Kind: "fmt.Print", Count: 18},
		{File: "chat_backtrack_command.go", Func: "listChatBacktrackTurns", Kind: "fmt.Print", Count: 4},
		{File: "chat_backtrack_command.go", Func: "printBacktrackTombstone", Kind: "fmt.Print", Count: 4},
		{File: "chat_backtrack_command.go", Func: "printBacktrackUsage", Kind: "fmt.Print", Count: 7},
		{File: "chat_backtrack_command.go", Func: "printChatBacktrackResult", Kind: "fmt.Print", Count: 10},
		{File: "chat_backtrack_select.go", Func: "handleInteractiveBacktrackSelect", Kind: "fmt.Print", Count: 14},
		{File: "chat_backtrack_select.go", Func: "promptBacktrackMode", Kind: "fmt.Print", Count: 2},
		{File: "chat_backtrack_select.go", Func: "readBacktrackTurnPickPlain", Kind: "fmt.Print", Count: 3},
		{File: "chat_bootstrap.go", Func: "prepareChatPersistence", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_bootstrap.go", Func: "prepareChatRuntimeState", Kind: "fmt.Fprint(os.Std*)", Count: 3},
		{File: "chat_command_output.go", Func: "writeLegacyChatDebugDisplay", Kind: "io.WriteString(os.Std*)", Count: 1},
		{File: "chat_core.go", Func: "method Finalize", Kind: "fmt.Print", Count: 5},
		{File: "chat_core.go", Func: "method Handle", Kind: "fmt.Print", Count: 4},
		{File: "chat_core.go", Func: "method clearSpinner", Kind: "fmt.Print", Count: 1},
		{File: "chat_core.go", Func: "method flushAssistantTurnForToolBatch", Kind: "fmt.Print", Count: 1},
		{File: "chat_debug.go", Func: "handleChatAgentTargetCommand", Kind: "fmt.Print", Count: 3},
		{File: "chat_debug.go", Func: "handleChatAgentsCommand", Kind: "fmt.Print", Count: 6},
		{File: "chat_debug.go", Func: "pickChatAgent", Kind: "fmt.Print", Count: 3},
		{File: "chat_debug.go", Func: "printChatAgentMessageResult", Kind: "fmt.Print", Count: 2},
		{File: "chat_debug.go", Func: "printChatAgentPanel", Kind: "fmt.Print", Count: 4},
		{File: "chat_debug.go", Func: "printChatAgentRoutingUsage", Kind: "fmt.Print", Count: 1},
		{File: "chat_debug.go", Func: "printChatAgents", Kind: "fmt.Print", Count: 3},
		{File: "chat_debug.go", Func: "printChatCollab", Kind: "fmt.Print", Count: 1},
		{File: "chat_debug.go", Func: "printChatDebugAgentControl", Kind: "fmt.Print", Count: 3},
		{File: "chat_debug.go", Func: "printChatDebugAgentGraph", Kind: "fmt.Print", Count: 2},
		{File: "chat_debug.go", Func: "printChatDebugMailbox", Kind: "fmt.Print", Count: 2},
		{File: "chat_debug.go", Func: "printChatDebugRoutingSummary", Kind: "fmt.Print", Count: 1},
		{File: "chat_debug.go", Func: "printChatRouteLevels", Kind: "fmt.Print", Count: 1},
		{File: "chat_debug.go", Func: "printChatRouteRoles", Kind: "fmt.Print", Count: 1},
		{File: "chat_debug.go", Func: "printChatRoutingConfigSummary", Kind: "fmt.Print", Count: 1},
		{File: "chat_debug.go", Func: "printChatTimeline", Kind: "fmt.Print", Count: 1},
		{File: "chat_debug.go", Func: "readChatAgentPickerChoice", Kind: "fmt.Print", Count: 5},
		{File: "chat_debug_archive.go", Func: "handleDebugCommand", Kind: "fmt.Print", Count: 2},
		{File: "chat_debug_archive.go", Func: "printChatDebugArchiveResult", Kind: "fmt.Print", Count: 1},
		{File: "chat_debug_archive.go", Func: "printChatDebugModeStatus", Kind: "fmt.Print", Count: 1},
		{File: "chat_debug_archive.go", Func: "printChatDebugUsage", Kind: "fmt.Print", Count: 1},
		{File: "chat_debug_archive.go", Func: "setChatDebugMode", Kind: "fmt.Print", Count: 1},
		{File: "chat_export_command.go", Func: "exportInteractiveSelect", Kind: "fmt.Print", Count: 11},
		{File: "chat_export_command.go", Func: "exportSelectedSession", Kind: "fmt.Print", Count: 1},
		{File: "chat_export_command.go", Func: "handleExportCommand", Kind: "fmt.Print", Count: 4},
		{File: "chat_export_command.go", Func: "printChatExportResult", Kind: "fmt.Print", Count: 1},
		{File: "chat_export_command.go", Func: "readExportFormatChoice", Kind: "fmt.Print", Count: 3},
		{File: "chat_export_command.go", Func: "readExportMenuChoice", Kind: "fmt.Print", Count: 3},
		{File: "chat_fast_command.go", Func: "applyFastCommand", Kind: "fmt.Print", Count: 6},
		{File: "chat_fast_command.go", Func: "persistFastCommandPreference", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_fast_command.go", Func: "printFastCommandStatus", Kind: "fmt.Print", Count: 6},
		{File: "chat_folder_trust.go", Func: "handleTrustCommand", Kind: "fmt.Print", Count: 5},
		{File: "chat_folder_trust.go", Func: "printFolderTrustStatus", Kind: "fmt.Print", Count: 1},
		{File: "chat_goal_auto_continue.go", Func: "reportGoalAutoContinuationLimitReached", Kind: "fmt.Print", Count: 1},
		{File: "chat_goal_auto_continue.go", Func: "reportGoalAutoContinuationWarning", Kind: "fmt.Print", Count: 1},
		{File: "chat_http.go", Func: "emitRetryNotice", Kind: "fmt.Print", Count: 1},
		{File: "chat_http_debug.go", Func: "newRuntimeHTTPDebugReporter", Kind: "fmt.Fprint(os.Std*)", Count: 2},
		{File: "chat_image_command.go", Func: "handleImageGenerationCommand", Kind: "fmt.Print", Count: 4},
		{File: "chat_interaction.go", Func: "clearPromptDisplayRows", Kind: "ui.WriteTerminal*", Count: 1},
		{File: "chat_interaction.go", Func: "method writeFormatLocked", Kind: "ui.WriteTerminal*", Count: 1},
		{File: "chat_interaction.go", Func: "method writeTextLocked", Kind: "ui.WriteTerminal*", Count: 1},
		{File: "chat_logger_suppression.go", Func: "suppressChatConsoleLogger", Kind: "fmt.Fprint(os.Std*)", Count: 3},
		{File: "chat_login_command.go", Func: "finishChatLoginTextPrompt", Kind: "fmt.Print", Count: 1},
		{File: "chat_login_command.go", Func: "handleLoginCommand", Kind: "fmt.Print", Count: 3},
		{File: "chat_login_command.go", Func: "method PromptSecret", Kind: "fmt.Print", Count: 1},
		{File: "chat_login_command.go", Func: "method PromptText", Kind: "fmt.Print", Count: 2},
		{File: "chat_login_command.go", Func: "refreshLoginSessionIfNeeded", Kind: "fmt.Print", Count: 3},
		{File: "chat_memory_command.go", Func: "handleMemoryCommand", Kind: "fmt.Print", Count: 15},
		{File: "chat_memory_command.go", Func: "printProjectMemoryStatus", Kind: "fmt.Print", Count: 8},
		{File: "chat_model_command.go", Func: "executeModelCommand", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_model_command.go", Func: "persistModelCommandPreferences", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_model_command.go", Func: "printModelCommandMappingNotice", Kind: "fmt.Print", Count: 1},
		{File: "chat_model_command.go", Func: "printModelCommandProviderPickerLegacyPage", Kind: "fmt.Print", Count: 3},
		{File: "chat_model_command.go", Func: "promptModelCommandProviderSelectionLegacy", Kind: "fmt.Print", Count: 2},
		{File: "chat_model_switch.go", Func: "handleModelCommand", Kind: "fmt.Print", Count: 3},
		{File: "chat_model_switch.go", Func: "printRuntimeModelPickerLegacyPage", Kind: "fmt.Print", Count: 4},
		{File: "chat_model_switch.go", Func: "promptRuntimeModelSelectionLegacy", Kind: "fmt.Print", Count: 2},
		{File: "chat_model_switch.go", Func: "selectRuntimeReasoningEffortLegacy", Kind: "fmt.Print", Count: 5},
		{File: "chat_plan_command.go", Func: "exitChatPlanModeCommand", Kind: "fmt.Print", Count: 5},
		{File: "chat_plan_command.go", Func: "handlePlanCommand", Kind: "fmt.Print", Count: 7},
		{File: "chat_plan_command.go", Func: "printPlanModeStatus", Kind: "fmt.Print", Count: 1},
		{File: "chat_preferences.go", Func: "persistChatPreferencesIfNeeded", Kind: "fmt.Fprint(os.Std*)", Count: 2},
		{File: "chat_provider_turn.go", Func: "method Complete", Kind: "fmt.Fprint(os.Std*)", Count: 2},
		{File: "chat_reasoning_command.go", Func: "applyReasoningCommand", Kind: "fmt.Print", Count: 3},
		{File: "chat_reasoning_command.go", Func: "applyReasoningEffortCommandSelection", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_reasoning_command.go", Func: "handleReasoningEffortCommand", Kind: "fmt.Print", Count: 7},
		{File: "chat_reasoning_command.go", Func: "persistReasoningEffortCommandPreference", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_reasoning_command.go", Func: "printReasoningCommandStatus", Kind: "fmt.Print", Count: 2},
		{File: "chat_reasoning_command.go", Func: "printReasoningEffortCommandStatus", Kind: "fmt.Print", Count: 1},
		{File: "chat_resume_command.go", Func: "handleResumeCommand", Kind: "fmt.Print", Count: 5},
		{File: "chat_resume_command.go", Func: "printResumeSuccess", Kind: "fmt.Print", Count: 1},
		{File: "chat_resume_command.go", Func: "readHistoricalSessionPickWithCurrent", Kind: "fmt.Print", Count: 3},
		{File: "chat_resume_command.go", Func: "resumeInteractiveSelect", Kind: "fmt.Print", Count: 6},
		{File: "chat_resume_command.go", Func: "resumeLatestAndPrint", Kind: "fmt.Print", Count: 2},
		{File: "chat_retry_command.go", Func: "handleRetryCommand", Kind: "fmt.Print", Count: 9},
		{File: "chat_runtime_events.go", Func: "newChatRuntimeEventBridge", Kind: "fmt.Print", Count: 6},
		{File: "chat_runtime_server.go", Func: "configureRuntimeServerChatExecutor", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_runtime_server.go", Func: "prepareRuntimeServerChatPersistence", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_selection_output.go", Func: "printChatSelectionBlankLine", Kind: "ui.WriteTerminal*", Count: 1},
		{File: "chat_selection_output.go", Func: "printChatSelectionLine", Kind: "ui.WriteTerminal*", Count: 1},
		{File: "chat_selection_output.go", Func: "writeChatParts", Kind: "ui.WriteTerminal*", Count: 2},
		{File: "chat_send.go", Func: "sendMessage", Kind: "fmt.Print", Count: 2},
		{File: "chat_session.go", Func: "applyRuntimeSessionExecutionContext", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_session.go", Func: "printChatSessionSummaries", Kind: "fmt.Print", Count: 5},
		{File: "chat_session.go", Func: "warnIfChatSessionSyncFails", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_setup.go", Func: "buildChatSession", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_setup.go", Func: "emitChatSandboxWarning", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_setup.go", Func: "finalizeChatSessionWithError", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_setup.go", Func: "initializeChatCapabilities", Kind: "fmt.Fprint(os.Std*)", Count: 3},
		{File: "chat_setup.go", Func: "printChatExitResumeHint", Kind: "fmt.Print", Count: 1},
		{File: "chat_setup.go", Func: "printChatSessionPreamble", Kind: "fmt.Fprint(os.Std*)", Count: 2},
		{File: "chat_skills_command.go", Func: "handleSkillsMenuCommand", Kind: "fmt.Print", Count: 10},
		{File: "chat_skills_command.go", Func: "printSkillCatalogReport", Kind: "fmt.Print", Count: 1},
		{File: "chat_skills_command.go", Func: "promptSkillCatalogSelection", Kind: "fmt.Print", Count: 1},
		{File: "chat_skills_command.go", Func: "promptSkillExecutionInput", Kind: "fmt.Print", Count: 1},
		{File: "chat_startup_timing.go", Func: "method flush", Kind: "fmt.Fprint(os.Std*)", Count: 2},
		{File: "chat_status.go", Func: "handleStatusCommand", Kind: "fmt.Print", Count: 2},
		{File: "chat_status.go", Func: "printChatStatus", Kind: "fmt.Print", Count: 2},
		{File: "chat_stream_command.go", Func: "applyStreamCommand", Kind: "fmt.Print", Count: 5},
		{File: "chat_stream_command.go", Func: "applyStreamShortcut", Kind: "fmt.Print", Count: 3},
		{File: "chat_stream_command.go", Func: "persistStreamCommandPreference", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_stream_command.go", Func: "printStreamCommandStatus", Kind: "fmt.Print", Count: 5},
		{File: "chat_surface_output.go", Func: "method showPriorityPrompt", Kind: "fmt.Print", Count: 3},
		{File: "chat_surface_output.go", Func: "printDirectInteractiveOutput", Kind: "fmt.Print", Count: 1},
		{File: "chat_surface_output.go", Func: "writeChatLogBufferedMarker", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_surface_output.go", Func: "writeChatLogSaveError", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_system_output.go", Func: "method Write", Kind: "ui.WriteTerminal*", Count: 1},
		{File: "chat_system_output.go", Func: "method writeOutputTextLocked", Kind: "ui.WriteTerminal*", Count: 1},
		{File: "chat_team_binding.go", Func: "validateAmbientTeamBinding", Kind: "fmt.Fprint(os.Std*)", Count: 2},
		{File: "chat_theme_command.go", Func: "applyThemeCommandSelection", Kind: "fmt.Print", Count: 2},
		{File: "chat_theme_command.go", Func: "handleThemeCommand", Kind: "fmt.Print", Count: 6},
		{File: "chat_theme_command.go", Func: "persistThemeCommandPreference", Kind: "fmt.Fprint(os.Std*)", Count: 2},
		{File: "chat_theme_command.go", Func: "printThemeCommandList", Kind: "fmt.Print", Count: 8},
		{File: "chat_theme_command.go", Func: "printThemeCommandPreview", Kind: "fmt.Print", Count: 6},
		{File: "chat_theme_command.go", Func: "printThemeCommandStatus", Kind: "fmt.Print", Count: 12},
		{File: "chat_theme_command.go", Func: "printThemeConfigDefaults", Kind: "fmt.Print", Count: 3},
		{File: "chat_tool_debug.go", Func: "writeSessionDebugInfo", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_transcript_renderer.go", Func: "method RenderSupplement", Kind: "fmt.Print", Count: 1},
		{File: "chat_unix.go", Func: "setupSignalHandler", Kind: "fmt.Print", Count: 3},
		{File: "chat_windows.go", Func: "setupSignalHandler", Kind: "fmt.Print", Count: 2},
		{File: "command.go", Func: "executeShellCommandDetailed", Kind: "fmt.Print", Count: 18},
	}
}

func collectChatDirectWriters(t *testing.T) []chatDirectWriter {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	commandsDir := filepath.Dir(currentFile)
	paths, err := filepath.Glob(filepath.Join(commandsDir, "chat*.go"))
	if err != nil {
		t.Fatalf("glob chat sources: %v", err)
	}
	paths = append(paths, filepath.Join(commandsDir, "command.go"))
	sort.Strings(paths)

	fset := token.NewFileSet()
	var writers []chatDirectWriter
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", filepath.Base(path), err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := fn.Name.Name
			if fn.Recv != nil {
				name = "method " + name
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if kind := chatDirectWriterKind(call); kind != "" {
					writers = append(writers, chatDirectWriter{
						File: filepath.Base(path),
						Func: name,
						Kind: kind,
						Line: fset.Position(call.Pos()).Line,
					})
				}
				return true
			})
		}
	}
	sort.Slice(writers, func(i, j int) bool {
		return writers[i].String() < writers[j].String()
	})
	return writers
}

func chatDirectWriterKind(call *ast.CallExpr) string {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == "fmt" {
		switch selector.Sel.Name {
		case "Print", "Printf", "Println":
			return "fmt.Print"
		case "Fprint", "Fprintf", "Fprintln":
			if len(call.Args) > 0 && isChatStdStream(call.Args[0]) {
				return "fmt.Fprint(os.Std*)"
			}
		}
	}
	if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == "ui" &&
		strings.HasPrefix(selector.Sel.Name, "WriteTerminal") {
		return "ui.WriteTerminal*"
	}
	if (selector.Sel.Name == "Write" || selector.Sel.Name == "WriteString") &&
		isChatStdStream(selector.X) {
		return "os.Std*.Write*"
	}
	if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == "io" &&
		selector.Sel.Name == "WriteString" && len(call.Args) > 0 &&
		isChatStdStream(call.Args[0]) {
		return "io.WriteString(os.Std*)"
	}
	return ""
}

func isChatStdStream(expr ast.Expr) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	return ok && ident.Name == "os" &&
		(selector.Sel.Name == "Stdout" || selector.Sel.Name == "Stderr")
}

func diffDirectWriterInventory(want []chatDirectWriterInventoryEntry, got []chatDirectWriter) string {
	wantSet := make(map[string]int, len(want))
	for _, entry := range want {
		wantSet[entry.File+"\t"+entry.Func+"\t"+entry.Kind] = entry.Count
	}
	gotSet := make(map[string]int, len(got))
	gotLines := make(map[string][]int, len(got))
	for _, writer := range got {
		key := writer.inventoryKey()
		gotSet[key]++
		gotLines[key] = append(gotLines[key], writer.Line)
	}

	keys := make(map[string]bool, len(wantSet)+len(gotSet))
	for key := range wantSet {
		keys[key] = true
	}
	for key := range gotSet {
		keys[key] = true
	}
	var ordered []string
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)

	var lines []string
	for _, key := range ordered {
		if expected, actual := wantSet[key], gotSet[key]; expected != actual {
			lineText := ""
			if actual > 0 {
				lineText = fmt.Sprintf(" (actual lines: %v)", gotLines[key])
			}
			lines = append(lines, fmt.Sprintf("%s\twant=%d got=%d%s", key, expected, actual, lineText))
		}
	}
	return strings.Join(lines, "\n")
}

func assertSingleDebugCommandMarker(t *testing.T, stage string, surface *ui.FixedBottomSurface, screen *screenVT) {
	t.Helper()
	const marker = "Mailbox Pending:"
	frame := commandResultFrameText(surface)
	if count := strings.Count(frame, marker); count != 1 {
		t.Fatalf("%s composed frame marker count=%d want 1:\n%s", stage, count, frame)
	}
	if rows := screen.RowsContaining(marker); len(rows) != 1 {
		t.Fatalf("%s physical screen marker rows=%v want one:\n%s", stage, rows, screen.dump())
	}
}

func assertSingleStreamCommandMarker(t *testing.T, stage string, surface *ui.FixedBottomSurface, screen *screenVT) {
	t.Helper()
	const marker = "当前输出模式:"
	frame := commandResultFrameText(surface)
	if count := strings.Count(frame, marker); count != 1 {
		t.Fatalf("%s composed frame marker count=%d want 1:\n%s", stage, count, frame)
	}
	if rows := screen.RowsContaining(marker); len(rows) != 1 {
		t.Fatalf("%s physical screen marker rows=%v want one:\n%s", stage, rows, screen.dump())
	}
}

func commandResultFrameText(surface *ui.FixedBottomSurface) string {
	var text strings.Builder
	for _, row := range surface.ComposedFrameForTest() {
		for _, cell := range row {
			if !cell.Cont {
				text.WriteString(cell.Text)
			}
		}
		text.WriteByte('\n')
	}
	return text.String()
}
