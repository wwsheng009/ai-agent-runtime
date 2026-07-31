package commands

import (
	"bytes"
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

func TestStructuredCommandHandlersHaveNoDirectTerminalWriter(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	for _, name := range []string{"chat_command_result.go", "chat_debug_document.go"} {
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
		{File: "chat_debug.go", Func: "printChatCollab", Kind: "fmt.Print", Count: 5},
		{File: "chat_debug.go", Func: "printChatDebugAgentControl", Kind: "fmt.Print", Count: 3},
		{File: "chat_debug.go", Func: "printChatDebugAgentGraph", Kind: "fmt.Print", Count: 2},
		{File: "chat_debug.go", Func: "printChatDebugMailbox", Kind: "fmt.Print", Count: 2},
		{File: "chat_debug.go", Func: "printChatDebugRoutingSummary", Kind: "fmt.Print", Count: 1},
		{File: "chat_debug.go", Func: "printChatRouteLevels", Kind: "fmt.Print", Count: 1},
		{File: "chat_debug.go", Func: "printChatRouteRoles", Kind: "fmt.Print", Count: 1},
		{File: "chat_debug.go", Func: "printChatRoutingConfigSummary", Kind: "fmt.Print", Count: 1},
		{File: "chat_debug.go", Func: "printChatTimeline", Kind: "fmt.Print", Count: 3},
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
		{File: "chat_fast_command.go", Func: "persistFastCommandPreference", Kind: "fmt.Fprint(os.Std*)", Count: 2},
		{File: "chat_fast_command.go", Func: "printFastCommandStatus", Kind: "fmt.Print", Count: 6},
		{File: "chat_folder_trust.go", Func: "handleTrustCommand", Kind: "fmt.Print", Count: 5},
		{File: "chat_folder_trust.go", Func: "printFolderTrustStatus", Kind: "fmt.Print", Count: 4},
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
		{File: "chat_plan_command.go", Func: "printPlanModeStatus", Kind: "fmt.Print", Count: 12},
		{File: "chat_preferences.go", Func: "persistChatPreferencesIfNeeded", Kind: "fmt.Fprint(os.Std*)", Count: 2},
		{File: "chat_provider_turn.go", Func: "method Complete", Kind: "fmt.Fprint(os.Std*)", Count: 2},
		{File: "chat_reasoning_command.go", Func: "applyReasoningCommand", Kind: "fmt.Print", Count: 3},
		{File: "chat_reasoning_command.go", Func: "applyReasoningEffortCommandSelection", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_reasoning_command.go", Func: "handleReasoningEffortCommand", Kind: "fmt.Print", Count: 7},
		{File: "chat_reasoning_command.go", Func: "persistReasoningEffortCommandPreference", Kind: "fmt.Fprint(os.Std*)", Count: 2},
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
		{File: "chat_slash_help.go", Func: "printChatSlashHelp", Kind: "fmt.Print", Count: 1},
		{File: "chat_startup_timing.go", Func: "method flush", Kind: "fmt.Fprint(os.Std*)", Count: 2},
		{File: "chat_status.go", Func: "handleStatusCommand", Kind: "fmt.Print", Count: 2},
		{File: "chat_status.go", Func: "printChatStatus", Kind: "fmt.Print", Count: 2},
		{File: "chat_stream_command.go", Func: "applyStreamCommand", Kind: "fmt.Print", Count: 5},
		{File: "chat_stream_command.go", Func: "applyStreamShortcut", Kind: "fmt.Print", Count: 3},
		{File: "chat_stream_command.go", Func: "persistStreamCommandPreference", Kind: "fmt.Fprint(os.Std*)", Count: 2},
		{File: "chat_stream_command.go", Func: "printStreamCommandStatus", Kind: "fmt.Print", Count: 5},
		{File: "chat_surface_output.go", Func: "method showPriorityPrompt", Kind: "fmt.Print", Count: 3},
		{File: "chat_surface_output.go", Func: "printDirectInteractiveOutput", Kind: "fmt.Print", Count: 1},
		{File: "chat_surface_output.go", Func: "writeChatLogBufferedMarker", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_surface_output.go", Func: "writeChatLogSaveError", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_system_output.go", Func: "method Write", Kind: "ui.WriteTerminal*", Count: 1},
		{File: "chat_system_output.go", Func: "method writeOutputTextLocked", Kind: "ui.WriteTerminal*", Count: 1},
		{File: "chat_team_binding.go", Func: "validateAmbientTeamBinding", Kind: "fmt.Fprint(os.Std*)", Count: 2},
		{File: "chat_theme_command.go", Func: "applyThemeCommandSelection", Kind: "fmt.Print", Count: 2},
		{File: "chat_theme_command.go", Func: "handleThemeCommand", Kind: "fmt.Print", Count: 9},
		{File: "chat_theme_command.go", Func: "persistThemeCommandPreference", Kind: "fmt.Fprint(os.Std*)", Count: 2},
		{File: "chat_theme_command.go", Func: "printThemeCommandList", Kind: "fmt.Print", Count: 8},
		{File: "chat_theme_command.go", Func: "printThemeCommandPreview", Kind: "fmt.Print", Count: 6},
		{File: "chat_theme_command.go", Func: "printThemeCommandStatus", Kind: "fmt.Print", Count: 12},
		{File: "chat_theme_command.go", Func: "printThemeConfigDefaults", Kind: "fmt.Print", Count: 3},
		{File: "chat_tool_debug.go", Func: "writeSessionDebugInfo", Kind: "fmt.Fprint(os.Std*)", Count: 1},
		{File: "chat_transcript_renderer.go", Func: "method RenderSupplement", Kind: "fmt.Print", Count: 1},
		{File: "chat_unix.go", Func: "setupSignalHandler", Kind: "fmt.Print", Count: 3},
		{File: "chat_windows.go", Func: "setupSignalHandler", Kind: "fmt.Print", Count: 2},
		{File: "command.go", Func: "confirmBypassPermissionModeChange", Kind: "fmt.Print", Count: 4},
		{File: "command.go", Func: "confirmClearConversationHistory", Kind: "fmt.Print", Count: 4},
		{File: "command.go", Func: "executeShellCommandDetailed", Kind: "fmt.Print", Count: 18},
		{File: "command.go", Func: "handleApprovalReuseCommand", Kind: "fmt.Print", Count: 4},
		{File: "command.go", Func: "handleCommand", Kind: "fmt.Print", Count: 37},
		{File: "command.go", Func: "handleCompactCommand", Kind: "fmt.Print", Count: 4},
		{File: "command.go", Func: "handleImageAttachmentCommand", Kind: "fmt.Print", Count: 11},
		{File: "command.go", Func: "handlePermissionModeCommand", Kind: "fmt.Print", Count: 4},
		{File: "command.go", Func: "handleQueueCommand", Kind: "fmt.Print", Count: 5},
		{File: "command.go", Func: "printApprovalReuseStatus", Kind: "fmt.Print", Count: 5},
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
