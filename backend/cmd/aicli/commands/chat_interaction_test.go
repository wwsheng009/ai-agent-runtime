package commands

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/motion"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type synchronizedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestChatInteractionCoordinator_RendersPromptAndAsyncLineOnSameWriter(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.PrintPrompt()
	coord.RenderAsyncLine("[task] started task-1 @planner")

	rendered := output.String()
	if !strings.Contains(rendered, ui.UserPromptText(0)) {
		t.Fatalf("expected prompt in output, got %q", rendered)
	}
	if !strings.Contains(rendered, ui.FormatAssistantSupplementBlock("[task] started task-1 @planner")) {
		t.Fatalf("expected async line in output, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_RenderAsyncLineClearsVisiblePromptInInteractiveMode(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.promptAdvanceFn = func() bool { return false }
	output := &terminalCaptureWriter{}
	coord.SetWriter(output)

	coord.PrintPrompt()
	coord.RenderAsyncLine("[tool] view")

	rendered := output.String()
	if strings.Contains(rendered, ui.UserPromptText(0)) {
		t.Fatalf("expected prompt to be cleared before async line, got %q", rendered)
	}
	if !strings.Contains(rendered, strings.TrimRight(ui.FormatAssistantSupplementBlock("[tool] view"), " ")) {
		t.Fatalf("expected async line in output, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_RenderSubmittedUserInputWritesUserBlock(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderSubmittedUserInput("第一个问题")

	rendered := output.String()
	if !strings.Contains(rendered, ui.FormatUserMessage("第一个问题")) {
		t.Fatalf("expected submitted user input to render as user message, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_WaitingStateBlocksCommandInput(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord

	if !chatInputCommandAllowed(session, "/help") {
		t.Fatal("expected slash commands to be accepted while ready")
	}

	coord.StartWaiting()
	if got := coord.currentSurfaceStateForTest(); got != "Waiting" {
		t.Fatalf("expected waiting state after prompt submission, got %q", got)
	}
	if chatInputCommandAllowed(session, "/help") {
		t.Fatal("expected slash commands to be blocked while waiting")
	}
	if !chatInputCommandAllowed(session, "normal prompt") {
		t.Fatal("expected normal prompts not to be treated as slash commands")
	}

	coord.ClearWaiting()
	if got := coord.currentSurfaceStateForTest(); got != "Ready" {
		t.Fatalf("expected ready state after waiting clears, got %q", got)
	}
	if !chatInputCommandAllowed(session, "/help") {
		t.Fatal("expected slash commands to be accepted after ready")
	}
}

func TestChatInteractionCoordinator_AgentStageOverridesLegacyThinkingState(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)

	coord.StartThinking()
	coord.SetAgentStageDetail(chatAgentStageToolRunning, "shell_command")
	if got := coord.currentSurfaceStateForTest(); got != "Tool shell_command" {
		t.Fatalf("expected tool-running stage, got %q", got)
	}
	if got := coord.AgentStage(); got != chatAgentStageToolRunning {
		t.Fatalf("expected stored agent stage, got %q", got)
	}
	if got := coord.AgentStageDetail(); got != "shell_command" {
		t.Fatalf("expected stored stage detail, got %q", got)
	}
	if coord.IsReady() {
		t.Fatal("expected an active agent stage to block Ready")
	}

	coord.ClearAgentStage()
	if got := coord.currentSurfaceStateForTest(); got != "Thinking" {
		t.Fatalf("expected legacy thinking state after clearing stage, got %q", got)
	}
}

func TestChatInteractionCoordinator_AgentStageLabels(t *testing.T) {
	tests := []struct {
		stage chatAgentStage
		want  string
	}{
		{chatAgentStagePlanning, "Planning"},
		{chatAgentStageToolRunning, "Tool running"},
		{chatAgentStageAwaitingApproval, "Awaiting approval"},
		{chatAgentStageAwaitingAnswer, "Awaiting answer"},
		{chatAgentStageStopping, "Stopping"},
		// Terminal stages surface as Ready (Codex-aligned), not sticky Completed/Failed.
		{chatAgentStageCompleted, "Ready"},
		{chatAgentStageFailed, "Ready"},
		{chatAgentStageIdle, "Ready"},
	}
	for _, tt := range tests {
		t.Run(string(tt.stage), func(t *testing.T) {
			coord := newChatInteractionCoordinator(&ChatSession{})
			coord.SetAgentStage(tt.stage)
			if got := coord.currentSurfaceStateForTest(); got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestChatInteractionCoordinator_AgentStagePrecedesStreamingState(t *testing.T) {
	coord := newChatInteractionCoordinator(&ChatSession{})
	coord.mu.Lock()
	coord.streamingActive = true
	coord.mu.Unlock()

	coord.SetAgentStage(chatAgentStageAwaitingApproval)
	if got := coord.currentSurfaceStateForTest(); got != "Awaiting approval" {
		t.Fatalf("expected action-required stage to override streaming, got %q", got)
	}
	coord.ClearAgentStage()
	if got := coord.currentSurfaceStateForTest(); got != "Streaming" {
		t.Fatalf("expected streaming state after clearing explicit stage, got %q", got)
	}
}

func TestStartWaiting_PreservesPromptDraft(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord

	coord.SetPromptInput("draft while busy")
	coord.StartWaiting()

	snapshot := coord.PromptInputSnapshot()
	if snapshot.Text != "draft while busy" {
		t.Fatalf("expected waiting transition to preserve prompt draft, got %q", snapshot.Text)
	}
}

func TestStartThinking_PreservesVisiblePromptDraft(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.promptAdvanceFn = func() bool { return false }
	output := &terminalCaptureWriter{}
	coord.SetWriter(output)

	coord.PrintPrompt()
	coord.SetPromptInput("draft while thinking")
	coord.StartThinking()

	snapshot := coord.PromptInputSnapshot()
	if snapshot.Text != "draft while thinking" {
		t.Fatalf("expected thinking transition to preserve prompt draft, got %q", snapshot.Text)
	}
}

func TestSchedulePromptRedraw_RestoresPromptDraft(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.promptDelay = 10 * time.Millisecond
	output := &synchronizedBuffer{}
	coord.SetWriter(output)

	coord.SetPromptInput("draft after ready")
	coord.SchedulePromptRedraw()

	require.Eventually(t, func() bool {
		return strings.Contains(output.String(), ui.UserPromptText(0)+"draft after ready")
	}, 200*time.Millisecond, 10*time.Millisecond)
}

func TestChatInteractionCoordinator_PrintPrompt_InsertsBlankLineAfterCompletedBlock(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAssistant("第一轮回复")
	coord.PrintPrompt()

	rendered := output.String()
	if !strings.Contains(rendered, "第一轮回复") || !strings.Contains(rendered, "\n\n"+ui.UserPromptText(0)) {
		t.Fatalf("expected prompt redraw to keep one blank line after completed block, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_RenderAsyncLine_KeepsGapAcrossPromptRedraw(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.promptAdvanceFn = func() bool { return false }
	output := &terminalCaptureWriter{}
	coord.SetWriter(output)

	coord.RenderAsyncLine("• Running grep path=E:/projects/ai/ai-agent-runtime/backend")
	coord.PrintPrompt()
	coord.RenderAsyncLine("• Completed grep path=E:/projects/ai/ai-agent-runtime/backend")

	rendered := output.String()
	if !strings.Contains(rendered, "• Running grep path=E:/projects/ai/ai-agent-runtime/backend") {
		t.Fatalf("expected first tool timeline line, got %q", rendered)
	}
	if !strings.Contains(rendered, "• Running grep path=E:/projects/ai/ai-agent-runtime/backend\n\n• Completed grep path=E:/projects/ai/ai-agent-runtime/backend") {
		t.Fatalf("expected second tool timeline line to preserve blank-line gap after prompt redraw, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_RenderAsyncLine_KeepsGapWhenPromptVisibleAfterAsyncLine(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.promptAdvanceFn = func() bool { return false }
	output := &terminalCaptureWriter{}
	coord.SetWriter(output)

	coord.RenderAsyncLine("• Running git ls-files --others --exclude-standard\n  workdir: E:/projects/ai/ai-agent-runtime")
	coord.mu.Lock()
	coord.promptVisible = true
	coord.promptAfterBlockGap = false
	coord.completeBlockOutput = true
	coord.lastCompletedAsyncLine = true
	coord.mu.Unlock()
	coord.RenderAsyncLine("• Completed git ls-files --others --exclude-standard in 868ms\n  workdir: E:/projects/ai/ai-agent-runtime")

	rendered := output.String()
	if !strings.Contains(rendered, "  workdir: E:/projects/ai/ai-agent-runtime\n\n• Completed git ls-files --others --exclude-standard in 868ms") {
		t.Fatalf("expected completed tool timeline line to keep a blank-line gap after prompt clear, got %q", rendered)
	}
}

func TestFinishInteractiveReadPromptState_PreservesDraftForQueuedInput(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord

	coord.SetPromptInput("next draft")
	session.lastInteractiveInputQueued = true
	finishChatInteractiveReadPromptState(session, nil)

	snapshot := coord.PromptInputSnapshot()
	if snapshot.Text != "next draft" {
		t.Fatalf("expected queued input read to preserve prompt draft, got %q", snapshot.Text)
	}
	if session.lastInteractiveInputQueued {
		t.Fatal("expected queued input marker to reset after prompt state handling")
	}
}

func (c *chatInteractionCoordinator) currentSurfaceStateForTest() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.currentSurfaceStateLocked()
}

func TestRenderSubmittedUserInputEchoSkipsLegacyPromptPath(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)
	session.Interaction = coord

	renderSubmittedUserInputEcho(session, "第一个问题")

	if output.String() != "" {
		t.Fatalf("expected submitted input echo to be gated to fixed-bottom surface, got %q", output.String())
	}
}

func TestChatInteractionCoordinator_ClearPromptKeepsComposerDraft(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.promptAdvanceFn = func() bool { return false }
	output := &terminalCaptureWriter{}
	coord.SetWriter(output)

	draft := "typed while streaming"
	coord.PrintPrompt()
	coord.SetPromptInput(draft)
	// Turn boundaries and async output clear the prompt rows before writing.
	coord.ClearPrompt()

	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != draft || snapshot.Cursor != len([]rune(draft)) {
		t.Fatalf("expected prompt clear to keep the composer draft, got %#v", snapshot)
	}

	coord.PrintPrompt()
	if rendered := output.String(); !strings.Contains(rendered, draft) {
		t.Fatalf("expected the preserved draft to be repainted with the prompt, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_ClearPromptClearsWrappedInput(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.promptAdvanceFn = func() bool { return false }
	output := &terminalCaptureWriter{}
	coord.SetWriter(output)

	longInput := strings.Repeat("x", ui.GetTerminalWidth()+12)
	coord.PrintPrompt()
	fmt.Fprint(output, longInput)
	coord.SetPromptInput(longInput)
	coord.ClearPrompt()

	rendered := output.String()
	if rendered != "" {
		t.Fatalf("expected wrapped prompt to be fully cleared, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_ClearPromptClearsMultilineInput(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.promptAdvanceFn = func() bool { return false }
	output := &terminalCaptureWriter{}
	coord.SetWriter(output)

	input := "Session File: /tmp/session.json\nSession Store: /tmp/sessions\nChat Log File: /tmp/chat.json"
	coord.PrintPrompt()
	fmt.Fprint(output, strings.ReplaceAll(input, "\n", "\r\n"))
	coord.SetPromptInput(input)
	coord.ClearPrompt()

	rendered := output.String()
	if rendered != "" {
		t.Fatalf("expected multiline prompt to be fully cleared, got %q", rendered)
	}
}

func TestInteractivePromptCursorRowUsesSnapshotCursor(t *testing.T) {
	prompt := "> "
	input := strings.Repeat("x", 25)
	termWidth := 10

	if got := interactivePromptCursorRow(prompt, input, 0, termWidth); got != 0 {
		t.Fatalf("expected cursor at input start to remain on first row, got %d", got)
	}
	if got := interactivePromptCursorRow(prompt, input, len([]rune(input)), termWidth); got != 2 {
		t.Fatalf("expected cursor at wrapped input end to be on third row, got %d", got)
	}
}

func TestInteractivePromptPositionPreWrapsWideRuneAtRightEdge(t *testing.T) {
	prompt := ">>>"
	input := "你a"

	row, col := interactivePromptCursorPosition(prompt, input, len([]rune(input)), 4)
	if row != 1 || col != 3 {
		t.Fatalf("expected wide rune to pre-wrap before the last narrow column, row=%d col=%d", row, col)
	}
	if rows := interactivePromptDisplayRows(prompt+input, 4); rows != 2 {
		t.Fatalf("expected prompt and wide input to occupy two rows, got %d", rows)
	}
}

func TestNotifyChatInputDraftState_IsSilent(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	output := &terminalCaptureWriter{}
	coord.SetWriter(output)
	session.Interaction = coord

	notifyChatInputDraftState(session, true, 2, "first\nsecond")

	rendered := output.String()
	if rendered != "" {
		t.Fatalf("expected draft state notification to be silent, got %q", rendered)
	}
}

func TestPrepareInteractiveRead_HoldsPromptWhileDraftExists(t *testing.T) {
	session := &ChatSession{}
	session.InputQueue = newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	session.InputQueue.stageDraft("first\nsecond")

	showPrompt, notice, err := prepareInteractiveRead(session)
	if err != nil {
		t.Fatalf("prepareInteractiveRead: %v", err)
	}
	if showPrompt {
		t.Fatal("expected prompt to remain hidden while draft exists")
	}
	if notice != "" {
		t.Fatalf("expected no notice while draft exists, got %q", notice)
	}
}

func TestPrepareInteractiveRead_HoldsPromptWhileConfirmedDraftExists(t *testing.T) {
	session := &ChatSession{}
	session.InputQueue = newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	session.InputQueue.stageDraft("first\nsecond")
	if !session.InputQueue.confirmDraft() {
		t.Fatal("expected staged draft to become a ready submission")
	}

	showPrompt, notice, err := prepareInteractiveRead(session)
	if err != nil {
		t.Fatalf("prepareInteractiveRead: %v", err)
	}
	if showPrompt {
		t.Fatal("expected prompt to remain hidden while confirmed draft exists")
	}
	if notice != "" {
		t.Fatalf("expected no notice while confirmed draft exists, got %q", notice)
	}

	line, err := chatInteractiveReadLine(session, context.Background())
	if err != nil {
		t.Fatalf("chatInteractiveReadLine: %v", err)
	}
	if strings.TrimSpace(line) != "first\nsecond" {
		t.Fatalf("unexpected confirmed draft text: %q", line)
	}
}

func TestChatSession_InterruptClearsPromptAndDraftState(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)
	session.Interaction = coord
	session.InputQueue = newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	session.InputQueue.stageDraft("first\nsecond")

	coord.PrintPrompt()
	if !strings.Contains(output.String(), ui.UserPromptText(0)) {
		t.Fatalf("expected initial prompt to render, got %q", output.String())
	}

	session.Interrupt()

	if session.InputQueue.hasDraft() {
		t.Fatal("expected interrupt to clear staged draft")
	}
	if session.InputQueue.hasReadySubmission() {
		t.Fatal("expected interrupt to clear ready submission")
	}

	showPrompt, notice, err := prepareInteractiveRead(session)
	if err != nil {
		t.Fatalf("prepareInteractiveRead after interrupt: %v", err)
	}
	if !showPrompt {
		t.Fatal("expected prompt to become visible again after interrupt")
	}
	if notice != "" {
		t.Fatalf("expected no queued-input notice after interrupt, got %q", notice)
	}

	coord.PrintPrompt()
	if strings.Count(output.String(), ui.UserPromptText(0)) != 2 {
		t.Fatalf("expected prompt to be redrawn after interrupt, got %q", output.String())
	}
}

func TestChatInteractiveReadLine_ResetsPromptStateOnEOF(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	oldStdin := os.Stdin
	defer func() {
		chatIsInteractiveTerminal = oldInteractive
		os.Stdin = oldStdin
	}()

	chatIsInteractiveTerminal = func() bool { return true }

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	os.Stdin = reader

	session := &ChatSession{
		InputBox: ui.NewInputBox(nil),
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)
	session.Interaction = coord

	coord.PrintPrompt()
	if !coord.promptVisible {
		t.Fatal("expected prompt to be visible before read")
	}

	_, readErr := chatInteractiveReadLine(session, context.Background())
	if !errors.Is(readErr, io.EOF) {
		t.Fatalf("expected EOF, got %v", readErr)
	}
	if coord.promptVisible {
		t.Fatal("expected prompt state to be reset after EOF")
	}
}

func TestChatInteractiveReadLine_ResetsPromptStateOnQueueEOF(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	defer func() {
		chatIsInteractiveTerminal = oldInteractive
	}()

	chatIsInteractiveTerminal = func() bool { return false }

	session := &ChatSession{
		InputQueue: newChatInputQueue(bufio.NewReader(strings.NewReader(""))),
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)
	session.Interaction = coord

	coord.PrintPrompt()
	if !coord.promptVisible {
		t.Fatal("expected prompt to be visible before queue EOF read")
	}

	result := make(chan error, 1)
	go func() {
		_, err := chatInteractiveReadLine(session, context.Background())
		result <- err
	}()

	select {
	case err := <-result:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("expected EOF from queue read, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for queue EOF read")
	}

	if coord.promptVisible {
		t.Fatal("expected prompt state to be reset after queue EOF")
	}
}

func TestChatInteractiveReadLine_ResetsPromptStateOnQueueError(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	defer func() {
		chatIsInteractiveTerminal = oldInteractive
	}()

	chatIsInteractiveTerminal = func() bool { return false }

	readErr := errors.New("boom")
	session := &ChatSession{
		InputQueue: newChatInputQueue(bufio.NewReader(errorReader{err: readErr})),
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)
	session.Interaction = coord

	coord.PrintPrompt()
	if !coord.promptVisible {
		t.Fatal("expected prompt to be visible before queue error read")
	}

	result := make(chan error, 1)
	go func() {
		_, err := chatInteractiveReadLine(session, context.Background())
		result <- err
	}()

	select {
	case err := <-result:
		if !errors.Is(err, readErr) {
			t.Fatalf("expected queue error %v, got %v", readErr, err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for queue error read")
	}

	if coord.promptVisible {
		t.Fatal("expected prompt state to be reset after queue error")
	}
}

type errorReader struct {
	err error
}

func (r errorReader) Read(p []byte) (int, error) {
	return 0, r.err
}

func TestChatInteractionCoordinator_RenderAsyncLineSupportsMultilineToolSummary(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAsyncLine("[tool done] ls path=docs\n  目录: docs\n  📁 aicli/ · 📁 architecture/\n  统计: 0 个文件, 2 个目录")

	rendered := output.String()
	expected := ui.FormatAssistantSupplementBlock("[tool done] ls path=docs\n  目录: docs\n  📁 aicli/ · 📁 architecture/\n  统计: 0 个文件, 2 个目录")
	if !strings.Contains(rendered, expected) {
		t.Fatalf("expected multiline async line in output, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_RenderAsyncLineSeparatesAdjacentBlocks(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAsyncLine("[tool done] first")
	coord.RenderAsyncLine("[tool done] second")

	rendered := output.String()
	expected := ui.FormatAssistantSupplementBlock("[tool done] first") + "\n\n" + ui.FormatAssistantSupplementBlock("[tool done] second")
	if !strings.Contains(rendered, expected) {
		t.Fatalf("expected blank line between adjacent async blocks, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_PrintPromptSuppressesWhileActiveTeamRunning(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:     "team-prompt",
		Status: team.TeamStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "lead-session"},
		ActiveTeam:       &chatTeamBinding{TeamID: teamID, AgentID: "lead"},
		LocalRuntimeHost: &localChatRuntimeHost{TeamStore: store},
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.PrintPrompt()
	if output.String() != "" {
		t.Fatalf("expected no prompt while team is active, got %q", output.String())
	}

	if err := store.UpdateTeamStatus(context.Background(), teamID, team.TeamStatusDone); err != nil {
		t.Fatalf("UpdateTeamStatus: %v", err)
	}
	coord.PrintPrompt()
	if !strings.Contains(output.String(), ui.UserPromptText(0)) {
		t.Fatalf("expected prompt after team completion, got %q", output.String())
	}
}

func TestChatInteractionCoordinator_PrintPromptUsesAmbientRuntimeTeamBinding(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:     "team-ambient",
		Status: team.TeamStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "lead-session",
		Status:    runtimechat.SessionIdle,
		AmbientRunMeta: &team.RunMeta{
			Team: &team.TeamRunMeta{
				TeamID:  teamID,
				AgentID: "lead",
			},
		},
	}))

	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "lead-session"},
		LocalRuntimeHost: &localChatRuntimeHost{RuntimeStore: runtimeStore, TeamStore: store},
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.PrintPrompt()
	if output.String() != "" {
		t.Fatalf("expected no prompt while ambient runtime team is active, got %q", output.String())
	}
	if session.ActiveTeam == nil || session.ActiveTeam.TeamID != teamID {
		t.Fatalf("expected ambient runtime team binding to hydrate ActiveTeam, got %+v", session.ActiveTeam)
	}

	require.NoError(t, store.UpdateTeamStatus(context.Background(), teamID, team.TeamStatusDone))
	coord.PrintPrompt()
	if !strings.Contains(output.String(), ui.UserPromptText(0)) {
		t.Fatalf("expected prompt after ambient runtime team completion, got %q", output.String())
	}
}

func TestChatInteractionCoordinator_PrintPromptUsesTeamStoreLeadBindingFallback(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-store-fallback",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "lead-session"},
		LocalRuntimeHost: &localChatRuntimeHost{TeamStore: store},
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.PrintPrompt()
	if output.String() != "" {
		t.Fatalf("expected no prompt while team-store fallback binding is active, got %q", output.String())
	}
	if session.ActiveTeam == nil || session.ActiveTeam.TeamID != teamID {
		t.Fatalf("expected team-store fallback to hydrate ActiveTeam, got %+v", session.ActiveTeam)
	}

	require.NoError(t, store.UpdateTeamStatus(context.Background(), teamID, team.TeamStatusDone))
	coord.PrintPrompt()
	if !strings.Contains(output.String(), ui.UserPromptText(0)) {
		t.Fatalf("expected prompt after team-store fallback completion, got %q", output.String())
	}
}

func TestDiscardPendingInteractiveInput_ResetsReaderAndFlushesConsoleBuffer(t *testing.T) {
	session := &ChatSession{InputReader: bufio.NewReader(strings.NewReader("stale\n"))}
	oldDiscard := discardPendingConsoleInput
	oldNewReader := newChatInputReader
	oldShouldDiscard := shouldDiscardPendingInput
	defer func() {
		discardPendingConsoleInput = oldDiscard
		newChatInputReader = oldNewReader
		shouldDiscardPendingInput = oldShouldDiscard
	}()

	flushed := 0
	discardPendingConsoleInput = func() (int, error) {
		flushed++
		return 3, nil
	}
	sentinel := bufio.NewReader(strings.NewReader(""))
	newChatInputReader = func() *bufio.Reader { return sentinel }
	shouldDiscardPendingInput = func() bool { return true }

	discarded := discardPendingInteractiveInput(session)
	if flushed != 1 {
		t.Fatalf("expected one console flush, got %d", flushed)
	}
	if discarded != 3 {
		t.Fatalf("expected discarded count 3, got %d", discarded)
	}
	if session.InputReader != sentinel {
		t.Fatalf("expected input reader to reset")
	}
}

func TestDiscardPendingInteractiveInput_SkipsConsoleFlushForLineEditor(t *testing.T) {
	session := &ChatSession{
		InputBox:    ui.NewInputBox(nil),
		InputReader: bufio.NewReader(strings.NewReader("stale\n")),
	}
	oldDiscard := discardPendingConsoleInput
	oldNewReader := newChatInputReader
	oldShouldDiscard := shouldDiscardPendingInput
	oldInteractive := chatIsInteractiveTerminal
	defer func() {
		discardPendingConsoleInput = oldDiscard
		newChatInputReader = oldNewReader
		shouldDiscardPendingInput = oldShouldDiscard
		chatIsInteractiveTerminal = oldInteractive
	}()

	flushed := 0
	discardPendingConsoleInput = func() (int, error) {
		flushed++
		return 3, nil
	}
	sentinel := bufio.NewReader(strings.NewReader(""))
	newChatInputReader = func() *bufio.Reader { return sentinel }
	shouldDiscardPendingInput = func() bool { return true }
	chatIsInteractiveTerminal = func() bool { return true }

	discarded := discardPendingInteractiveInput(session)
	if discarded != 0 {
		t.Fatalf("expected no discarded input for line editor, got %d", discarded)
	}
	if flushed != 0 {
		t.Fatalf("expected console buffer not to flush for line editor, got %d", flushed)
	}
	if session.InputReader == sentinel {
		t.Fatalf("expected shared reader to remain untouched")
	}
}

func TestDiscardPendingInteractiveInputForPriorityPrompt_ReturnsNotice(t *testing.T) {
	session := &ChatSession{InputReader: bufio.NewReader(strings.NewReader("stale\n"))}
	oldDiscard := discardPendingConsoleInput
	oldShouldDiscard := shouldDiscardPendingInput
	oldNewReader := newChatInputReader
	defer func() {
		discardPendingConsoleInput = oldDiscard
		shouldDiscardPendingInput = oldShouldDiscard
		newChatInputReader = oldNewReader
	}()

	discardPendingConsoleInput = func() (int, error) { return 1, nil }
	shouldDiscardPendingInput = func() bool { return true }
	newChatInputReader = func() *bufio.Reader { return bufio.NewReader(strings.NewReader("")) }

	notice := discardPendingInteractiveInputForPriorityPrompt(session, "审批提示")
	if !strings.Contains(notice, "审批提示") || !strings.Contains(notice, "丢弃") {
		t.Fatalf("expected priority prompt discard notice, got %q", notice)
	}
}

func TestDiscardPendingInteractiveInputForPriorityPrompt_DrainsQueuedLines(t *testing.T) {
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
		LocalRuntimeHost: &localChatRuntimeHost{
			RuntimeStore: runtimeStore,
			EventStore:   runtimeStore,
		},
		InputQueue: &chatInputQueue{
			lines: make(chan chatQueuedInput, 4),
			errs:  make(chan error, 1),
		},
	}
	session.InputQueue.lines <- chatQueuedInput{Text: "hello\n", Source: "stdin"}
	session.InputQueue.lines <- chatQueuedInput{Text: "world\n", Source: "stdin"}

	notice := discardPendingInteractiveInputForPriorityPrompt(session, "问题提示")
	if !strings.Contains(notice, "问题提示") {
		t.Fatalf("expected question notice, got %q", notice)
	}
	if lenQueuedInteractiveInput(session) != 0 {
		t.Fatalf("expected queued lines to be drained")
	}
	events, err := runtimeSessionEvents(runtimeStore, "lead-session")
	if err != nil {
		t.Fatalf("runtimeSessionEvents: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Type != chatEventInputQueueDiscarded {
		t.Fatalf("expected discarded diagnostic event, got %+v", events)
	}
}

func TestPrepareInteractiveRead_PrefersQueuedInputAfterTeamSettles(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:     "team-wait",
		Status: team.TeamStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "lead-session",
		Status:    runtimechat.SessionIdle,
		AmbientRunMeta: &team.RunMeta{
			Team: &team.TeamRunMeta{
				TeamID: teamID,
			},
		},
	}))

	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "lead-session"},
		LocalRuntimeHost: &localChatRuntimeHost{RuntimeStore: runtimeStore, TeamStore: store},
	}

	oldDiscard := discardPendingConsoleInput
	oldPendingCount := pendingConsoleInputCount
	oldPendingLineInput := pendingConsoleLineInput
	oldPendingTextInput := pendingConsoleTextInput
	oldShouldDiscard := shouldDiscardPendingInput
	oldNewReader := newChatInputReader
	defer func() {
		discardPendingConsoleInput = oldDiscard
		pendingConsoleInputCount = oldPendingCount
		pendingConsoleLineInput = oldPendingLineInput
		pendingConsoleTextInput = oldPendingTextInput
		shouldDiscardPendingInput = oldShouldDiscard
		newChatInputReader = oldNewReader
	}()
	discardPendingConsoleInput = func() (int, error) { return 2, nil }
	pendingConsoleInputCount = func() (int, error) { return 2, nil }
	pendingConsoleLineInput = func() (bool, error) { return true, nil }
	pendingConsoleTextInput = func() (bool, error) { return false, nil }
	shouldDiscardPendingInput = func() bool { return true }
	newChatInputReader = func() *bufio.Reader { return bufio.NewReader(strings.NewReader("")) }

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = store.UpdateTeamStatus(context.Background(), teamID, team.TeamStatusDone)
	}()

	showPrompt, notice, err := prepareInteractiveRead(session)
	if err != nil {
		t.Fatalf("prepareInteractiveRead: %v", err)
	}
	if showPrompt {
		t.Fatal("expected queued input to suppress prompt")
	}
	if !strings.Contains(notice, "现将优先处理这些输入") {
		t.Fatalf("expected queued-input notice, got %q", notice)
	}
}

func TestPrepareInteractiveRead_IgnoresConsoleNoiseWithoutUserInput(t *testing.T) {
	session := &ChatSession{}

	oldDiscard := discardPendingConsoleInput
	oldPendingCount := pendingConsoleInputCount
	oldPendingLineInput := pendingConsoleLineInput
	oldPendingTextInput := pendingConsoleTextInput
	oldShouldDiscard := shouldDiscardPendingInput
	defer func() {
		discardPendingConsoleInput = oldDiscard
		pendingConsoleInputCount = oldPendingCount
		pendingConsoleLineInput = oldPendingLineInput
		pendingConsoleTextInput = oldPendingTextInput
		shouldDiscardPendingInput = oldShouldDiscard
	}()

	discardPendingConsoleInput = func() (int, error) { return 0, nil }
	pendingConsoleInputCount = func() (int, error) { return 4, nil }
	pendingConsoleLineInput = func() (bool, error) { return false, nil }
	pendingConsoleTextInput = func() (bool, error) { return false, nil }
	shouldDiscardPendingInput = func() bool { return true }

	showPrompt, notice, err := prepareInteractiveRead(session)
	if err != nil {
		t.Fatalf("prepareInteractiveRead: %v", err)
	}
	if !showPrompt {
		t.Fatal("expected prompt to remain visible when only console noise is pending")
	}
	if notice != "" {
		t.Fatalf("expected no notice for console noise, got %q", notice)
	}
}

func TestPrepareInteractiveRead_PrefersSessionQueueWithoutPrompt(t *testing.T) {
	session := &ChatSession{
		InputQueue: &chatInputQueue{
			lines: make(chan chatQueuedInput, 4),
			errs:  make(chan error, 1),
		},
	}
	session.InputQueue.lines <- chatQueuedInput{Text: "queued line\n", Source: "stdin"}

	showPrompt, notice, err := prepareInteractiveRead(session)
	if err != nil {
		t.Fatalf("prepareInteractiveRead: %v", err)
	}
	if showPrompt {
		t.Fatal("expected queued session input to suppress prompt")
	}
	if !strings.Contains(notice, "1 条后台任务期间的预输入内容") {
		t.Fatalf("expected queued notice, got %q", notice)
	}

	line, err := chatInteractiveReadLine(session, context.Background())
	if err != nil {
		t.Fatalf("chatInteractiveReadLine: %v", err)
	}
	if normalizeQueuedInputLine(line) != "queued line" {
		t.Fatalf("unexpected queued line: %q", line)
	}
}

func TestPrepareInteractiveRead_SuppressesNoticeForEchoedQueuedInput(t *testing.T) {
	session := &ChatSession{
		InputQueue: &chatInputQueue{
			lines: make(chan chatQueuedInput, 4),
			errs:  make(chan error, 1),
		},
		queuedInputEchoed: true,
	}
	session.InputQueue.lines <- chatQueuedInput{Text: "queued line\n", Source: "stdin"}

	showPrompt, notice, err := prepareInteractiveRead(session)
	if err != nil {
		t.Fatalf("prepareInteractiveRead: %v", err)
	}
	if showPrompt {
		t.Fatal("expected echoed queued input to suppress prompt")
	}
	if notice != "" {
		t.Fatalf("expected no notice for already echoed queued input, got %q", notice)
	}

	_, err = chatInteractiveReadLine(session, context.Background())
	if err != nil {
		t.Fatalf("chatInteractiveReadLine: %v", err)
	}
	showPrompt, notice, err = prepareInteractiveRead(session)
	if err != nil {
		t.Fatalf("prepareInteractiveRead after drain: %v", err)
	}
	if !showPrompt {
		t.Fatal("expected prompt to resume after echoed queue drains")
	}
	if session.queuedInputEchoed {
		t.Fatal("expected echoed queue marker to reset after drain")
	}
	if notice != "" {
		t.Fatalf("expected no notice after drain, got %q", notice)
	}
}

func TestPrepareInteractiveRead_EmitsQueuedNoticeOnlyOncePerDrain(t *testing.T) {
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
		LocalRuntimeHost: &localChatRuntimeHost{
			RuntimeStore: runtimeStore,
			EventStore:   runtimeStore,
		},
		InputQueue: &chatInputQueue{
			lines: make(chan chatQueuedInput, 4),
			errs:  make(chan error, 1),
		},
	}
	session.InputQueue.lines <- chatQueuedInput{Text: "first\n", Source: "stdin"}
	session.InputQueue.lines <- chatQueuedInput{Text: "second\n", Source: "stdin"}

	showPrompt, notice, err := prepareInteractiveRead(session)
	if err != nil {
		t.Fatalf("prepareInteractiveRead first: %v", err)
	}
	if showPrompt {
		t.Fatal("expected queued input to suppress prompt on first drain step")
	}
	if !strings.Contains(notice, "2 条后台任务期间的预输入内容") {
		t.Fatalf("expected initial queued count notice, got %q", notice)
	}

	_, err = chatInteractiveReadLine(session, context.Background())
	if err != nil {
		t.Fatalf("chatInteractiveReadLine first: %v", err)
	}

	showPrompt, notice, err = prepareInteractiveRead(session)
	if err != nil {
		t.Fatalf("prepareInteractiveRead second: %v", err)
	}
	if showPrompt {
		t.Fatal("expected second queued input to suppress prompt")
	}
	if notice != "" {
		t.Fatalf("expected no repeated notice while draining same queued batch, got %q", notice)
	}

	_, err = chatInteractiveReadLine(session, context.Background())
	if err != nil {
		t.Fatalf("chatInteractiveReadLine second: %v", err)
	}

	showPrompt, notice, err = prepareInteractiveRead(session)
	if err != nil {
		t.Fatalf("prepareInteractiveRead after drain: %v", err)
	}
	if !showPrompt {
		t.Fatal("expected prompt to resume after queue drains")
	}
	if notice != "" {
		t.Fatalf("expected no notice after queue drains, got %q", notice)
	}

	events, err := runtimeSessionEvents(runtimeStore, "lead-session")
	if err != nil {
		t.Fatalf("runtimeSessionEvents: %v", err)
	}
	var seenDetected, seenDrained bool
	for _, event := range events {
		switch event.Type {
		case chatEventInputQueueDetected:
			seenDetected = true
		case chatEventInputQueueDrained:
			seenDrained = true
		}
	}
	if !seenDetected || !seenDrained {
		t.Fatalf("expected detected and drained diagnostic events, got %+v", events)
	}
}

func TestChatInputQueue_PriorityReadSkipsNormalQueuedLines(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("priority\n")))
	queue.lines <- chatQueuedInput{Text: "normal\n", Source: "stdin"}

	line, err := queue.readPriorityLine(context.Background())
	if err != nil {
		t.Fatalf("readPriorityLine: %v", err)
	}
	if normalizeQueuedInputLine(line) != "priority" {
		t.Fatalf("expected priority line, got %q", line)
	}

	line, err = queue.readLine(context.Background())
	if err != nil {
		t.Fatalf("readLine: %v", err)
	}
	if normalizeQueuedInputLine(line) != "normal" {
		t.Fatalf("expected queued normal line to remain, got %q", line)
	}
}

func TestChatInteractiveReadPriorityLine_UsesSessionQueue(t *testing.T) {
	session := &ChatSession{
		InputQueue: newChatInputQueue(bufio.NewReader(strings.NewReader("answer\n"))),
	}
	session.InputQueue.lines <- chatQueuedInput{Text: "queued\n", Source: "stdin"}

	line, err := chatInteractiveReadPriorityLine(session, context.Background())
	if err != nil {
		t.Fatalf("chatInteractiveReadPriorityLine: %v", err)
	}
	if normalizeQueuedInputLine(line) != "answer" {
		t.Fatalf("expected priority answer, got %q", line)
	}
	if lenQueuedInteractiveInput(session) != 1 {
		t.Fatalf("expected queued normal line to remain after priority read")
	}
}

func TestChatInputQueue_PriorityReadUsesExternalCaptureWithoutStartingPump(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("normal\n")))
	queue.setExternalInputCaptureActive(true)

	result := make(chan string, 1)
	errs := make(chan error, 1)
	go func() {
		line, err := queue.readPriorityLine(context.Background())
		if err != nil {
			errs <- err
			return
		}
		result <- line
	}()

	require.Eventually(t, queue.isPriorityMode, time.Second, 10*time.Millisecond)
	queue.routeLine(chatQueuedInput{Text: "priority\n", Source: "test"})

	select {
	case err := <-errs:
		t.Fatalf("readPriorityLine returned error: %v", err)
	case line := <-result:
		if normalizeQueuedInputLine(line) != "priority" {
			t.Fatalf("expected priority line, got %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for priority line")
	}

	if line, ok := queue.readAvailableLine(); ok {
		t.Fatalf("did not expect stdin pump to consume normal reader line, got %q", line)
	}
}

func TestPriorityPromptBypassesInactiveQueueWhenLineEditorOwnsStdin(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	chatIsInteractiveTerminal = func() bool { return true }
	defer func() {
		chatIsInteractiveTerminal = oldInteractive
	}()

	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("queued-from-pump\n")))
	session := &ChatSession{
		InputBox:   ui.NewInputBox(nil),
		InputQueue: queue,
	}

	if shouldRoutePriorityPromptThroughQueue(session) {
		t.Fatal("expected inactive queue to be bypassed while line editor owns stdin")
	}

	select {
	case item := <-queue.lines:
		t.Fatalf("priority prompt should not start stdin pump, got queued item %q", item.Text)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestPriorityPromptUsesQueueWhenExternalCaptureOwnsStdin(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	chatIsInteractiveTerminal = func() bool { return true }
	defer func() {
		chatIsInteractiveTerminal = oldInteractive
	}()

	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("normal\n")))
	queue.setExternalInputCaptureActive(true)
	session := &ChatSession{
		InputBox:   ui.NewInputBox(nil),
		InputQueue: queue,
	}

	if !shouldRoutePriorityPromptThroughQueue(session) {
		t.Fatal("expected priority prompt to use queue while external capture owns stdin")
	}
}

func TestStartBusyQueuedInputCaptureSkipsUnsupportedCancelableStdin(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	chatIsInteractiveTerminal = func() bool { return true }
	defer func() {
		chatIsInteractiveTerminal = oldInteractive
	}()

	oldSupports := supportsCancelableInteractiveInputRead
	supportsCancelableInteractiveInputRead = func() bool { return false }
	defer func() {
		supportsCancelableInteractiveInputRead = oldSupports
	}()

	session := &ChatSession{
		InputBox:    ui.NewInputBox(nil),
		Interaction: newChatInteractionCoordinator(&ChatSession{}),
	}
	session.Interaction.session = session

	stop := startBusyQueuedInputCapture(session)
	defer stop()

	if session.InputQueue != nil && session.InputQueue.hasExternalInputCaptureActive() {
		t.Fatal("expected busy input capture to stay inactive without cancellable stdin")
	}
}

func TestChatInputQueue_PriorityReadReceivesExternalCaptureError(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.setExternalInputCaptureActive(true)

	errs := make(chan error, 1)
	go func() {
		_, err := queue.readPriorityLine(context.Background())
		errs <- err
	}()

	require.Eventually(t, queue.isPriorityMode, time.Second, 10*time.Millisecond)
	queue.signalReadError(ui.ErrInteractiveInputInterrupted)

	select {
	case err := <-errs:
		if !errors.Is(err, ui.ErrInteractiveInputInterrupted) {
			t.Fatalf("expected interrupted error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for priority read error")
	}
}

func TestRunChatLoop_DrainsQueuedLinesAfterTeamSettlesBeforePrompt(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-runloop",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.lines <- chatQueuedInput{Text: "hello\n", Source: "stdin"}
	queue.lines <- chatQueuedInput{Text: "/exit\n", Source: "stdin"}

	executor := &fakeChatExecutor{output: "queued response"}
	session := &ChatSession{
		Provider:         config.Provider{Protocol: "codex"},
		cancelCtx:        context.Background(),
		ChatExecutor:     executor,
		Logger:           NewChatLogger("codex_ee", "codex", "gpt-5.4", false, "https://example.com"),
		Formatter:        formatter.NewMarkdownFormatter(false),
		Interaction:      newChatInteractionCoordinator(&ChatSession{}),
		InputQueue:       queue,
		RuntimeSession:   &runtimechat.Session{ID: "lead-session"},
		ActiveTeam:       &chatTeamBinding{TeamID: teamID, AgentID: "lead"},
		LocalRuntimeHost: &localChatRuntimeHost{TeamStore: store},
	}
	session.Interaction.session = session
	var output bytes.Buffer
	session.Interaction.SetWriter(&output)

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = store.UpdateTeamStatus(context.Background(), teamID, team.TeamStatusDone)
	}()

	runChatLoop(session, false, "")

	if !executor.called || executor.prompt != "hello" {
		t.Fatalf("expected queued hello to be sent before exit, got called=%v prompt=%q", executor.called, executor.prompt)
	}
	rendered := output.String()
	if !strings.Contains(rendered, "现将优先处理这些输入") {
		t.Fatalf("expected queued-input notice, got %q", rendered)
	}
	if strings.Contains(rendered, ui.UserPromptText(0)) {
		t.Fatalf("expected no prompt before queued lines drain, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_FlushesBufferedStreamBeforeThinking(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAssistantDelta("Analyzing the code...")
	if output.String() != "" {
		t.Fatalf("expected delta to stay buffered before flush, got %q", output.String())
	}

	// StartThinking interrupts the stream and should flush the buffered content.
	coord.StartThinking()

	rendered := output.String()
	if !strings.Contains(rendered, "Analyzing the code...") {
		t.Fatalf("expected buffered delta to be flushed before thinking, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_FlushesBufferedStreamBeforeAsyncLine(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAssistantDelta("Partial analysis")
	if output.String() != "" {
		t.Fatalf("expected delta to stay buffered before flush, got %q", output.String())
	}

	// RenderAsyncLine interrupts the stream and should flush the buffered content.
	coord.RenderAsyncLine("[tool] view")

	rendered := output.String()
	if !strings.Contains(rendered, "Partial analysis") {
		t.Fatalf("expected buffered delta to be flushed before async line, got %q", rendered)
	}
	if !strings.Contains(rendered, "[tool] view") {
		t.Fatalf("expected async line after flush, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_ClearsThinkingBeforeAssistantResponse(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.StartThinking()
	coord.RenderAssistant("done")

	rendered := output.String()
	if !strings.Contains(rendered, "done") {
		t.Fatalf("expected assistant response in output, got %q", rendered)
	}
	if strings.Contains(rendered, "助手正在思考...") {
		t.Fatalf("expected no visible thinking placeholder, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_ClearPromptAdvancesLineForBufferedWriters(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.PrintPrompt()
	coord.ClearPrompt()
	coord.StartThinking()

	rendered := output.String()
	if !strings.Contains(rendered, ui.FormatUserPromptWithAttachments(0)+"\r\n") {
		t.Fatalf("expected buffered prompt to advance to next line, got %q", rendered)
	}
	if strings.Contains(rendered, "助手正在思考...") {
		t.Fatalf("expected no visible thinking placeholder after prompt advance, got %q", rendered)
	}
}

func TestBuildChatSurfaceStatusModelIdleUsesTypedSegmentsWithoutReady(t *testing.T) {
	model := buildChatSurfaceStatusModelForWidthAndInputMode(
		&ChatSession{Model: "gpt-5.6-sol"},
		"Ready",
		200,
		chatInputModeChat,
	)
	if !model.HideState {
		t.Fatal("expected idle model-first status to hide the implicit Ready state")
	}
	foundModel := false
	for _, segment := range model.Segments {
		if segment.Kind == style.StatusSegModel && segment.Text == "gpt-5.6-sol" && segment.Role == style.RoleAccent {
			foundModel = true
			break
		}
	}
	if !foundModel {
		t.Fatalf("expected typed model segment, got %#v", model.Segments)
	}
	plain := style.StatusLineDocument(model, 200).PlainText()
	if !strings.Contains(plain, "gpt-5.6-sol") || strings.Contains(plain, "Ready") {
		t.Fatalf("unexpected idle typed status: %q", plain)
	}
}

func TestBuildChatSurfaceStatusModelUsesTypedApprovalState(t *testing.T) {
	model := buildChatSurfaceStatusModelForWidthAndInputMode(
		&ChatSession{Model: "gpt-5.6-sol"},
		"Streaming",
		200,
		chatInputModeApproval,
	)
	if model.HideState || model.State != style.RunWaiting || model.StateText != "等待审批" || model.StateRole != style.RoleApproval {
		t.Fatalf("unexpected approval state model: %#v", model)
	}
	plain := style.StatusLineDocument(model, 200).PlainText()
	if !strings.HasPrefix(plain, "等待审批 · ") || !strings.Contains(plain, "gpt-5.6-sol") {
		t.Fatalf("unexpected approval typed status: %q", plain)
	}
}

func TestBuildChatSurfaceStatusLine_PrioritizesAgentContextOverDiagnostics(t *testing.T) {
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	workspaceRoot := t.TempDir()
	nested := filepath.Join(workspaceRoot, "alpha", "beta", "gamma", "delta", "epsilon")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("os.MkdirAll: %v", err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("os.Chdir: %v", err)
	}
	defer func() {
		_ = os.Chdir(previousWD)
	}()

	previousLookup := chatStatusGitBranchLookup
	chatStatusGitBranchLookup = func(string) string { return "feat/status-bar" }
	defer func() { chatStatusGitBranchLookup = previousLookup }()
	resetChatStatusGitBranchCacheForTest()

	// Use a short absolute ProfileRoot so the full cwd segment can fit width budgets.
	// The path does not need to exist on disk for status rendering.
	shortRoot := filepath.Clean(`E:\proj`)

	session := &ChatSession{
		Model:             "gpt-5.4-code",
		ReasoningEffort:   "HIGH",
		ProviderName:      "openai",
		ProfileName:       "ai-gateway",
		ProfileRoot:       shortRoot,
		MsgCount:          17,
		TokenCount:        28640,
		ContextTokenCount: 28640,
		InputTokenCount:   332_000_000,
		OutputTokenCount:  970_000,
		Stream:            false,
		FastMode:          false,
		Provider: config.Provider{
			Protocol: "codex",
			ModelCapabilities: map[string]config.ModelCapabilitySpec{
				"gpt-5.4-code": {
					MaxContextTokens:      128000,
					AutoCompactTokenLimit: 96000,
				},
			},
		},
	}

	status := buildChatSurfaceStatusLineForWidth(session, "Thinking", 220)

	for _, want := range []string{
		"思考",
		"gpt-5.4-code high",
		"openai",
		// used=28640 / window=128000 with Codex baseline → ~14%
		"Context 14% used",
		shortRoot,
		"ai-gateway",
		"feat/status-bar",
		"128K window",
		"332M in",
		"970K out",
		"Fast off",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("expected status line to contain %q, got %q", want, status)
		}
	}
	if !strings.Contains(status, chatSurfaceStatusSeparator) {
		t.Fatalf("expected Codex-style separator %q, got %q", chatSurfaceStatusSeparator, status)
	}
	// Provider appears as a bare name after the model segment; keep legacy labeled forms out.
	for _, unwanted := range []string{"权限", "输入 ", "模型 ", "上下文 ", "目录 ", "目标 ", "reasoning_effort", "msgs 17"} {
		if strings.Contains(status, unwanted) {
			t.Fatalf("expected status line to omit legacy diagnostic %q, got %q", unwanted, status)
		}
	}
	// Ensure provider sits immediately after model (before context/cwd diagnostics).
	modelIdx := strings.Index(status, "gpt-5.4-code high")
	providerIdx := strings.Index(status, "openai")
	contextIdx := strings.Index(status, "Context 14% used")
	if modelIdx < 0 || providerIdx < 0 || contextIdx < 0 || !(modelIdx < providerIdx && providerIdx < contextIdx) {
		t.Fatalf("expected model · provider · context order, got %q", status)
	}
}

func TestChatStatusProjectRedundantWithDirectory(t *testing.T) {
	t.Parallel()

	same := chatStatusSegment{
		full:    filepath.Clean(`E:\projects\ai\ai-agent-runtime`),
		compact: "ai-agent-runtime",
	}
	project := chatStatusSegment{full: "ai-agent-runtime", compact: "ai-agent-runtime"}
	if !chatStatusProjectRedundantWithDirectory(same, project) {
		t.Fatalf("expected project to be redundant when equal to cwd basename")
	}

	nested := chatStatusSegment{
		full:    filepath.Clean(`E:\projects\ai\ai-agent-runtime\backend`),
		compact: "backend",
	}
	if chatStatusProjectRedundantWithDirectory(nested, project) {
		t.Fatalf("expected nested cwd to keep distinct project segment")
	}
	if chatStatusProjectRedundantWithDirectory(chatStatusSegment{}, project) {
		t.Fatalf("expected project to remain when cwd segment is empty")
	}
	if !chatStatusProjectRedundantWithDirectory(same, chatStatusSegment{}) {
		t.Fatalf("expected empty project to be treated as redundant")
	}
}

func TestBuildChatSurfaceStatusLine_DedupesProjectWhenSameAsDirectory(t *testing.T) {
	previousLookup := chatStatusGitBranchLookup
	chatStatusGitBranchLookup = func(string) string { return "main" }
	defer func() { chatStatusGitBranchLookup = previousLookup }()
	resetChatStatusGitBranchCacheForTest()

	root := filepath.Clean(`E:\projects\ai\ai-agent-runtime`)
	session := &ChatSession{
		Model:       "gpt-5.4-code",
		ProfileRoot: root,
		ProfileName: "ai-agent-runtime",
	}
	status := buildChatSurfaceStatusLineForWidth(session, "Ready", 200)

	dup := "ai-agent-runtime" + chatSurfaceStatusSeparator + "ai-agent-runtime"
	if strings.Contains(status, dup) {
		t.Fatalf("expected project segment deduped when equal to cwd, got %q", status)
	}
	if !strings.Contains(status, "ai-agent-runtime") {
		t.Fatalf("expected cwd name still present once, got %q", status)
	}
	if !strings.Contains(status, "main") {
		t.Fatalf("expected branch still present, got %q", status)
	}
}

func TestBuildChatSurfaceStatusLine_KeepsDistinctProjectWhenCwdNested(t *testing.T) {
	previousLookup := chatStatusGitBranchLookup
	chatStatusGitBranchLookup = func(string) string { return "feat/nested" }
	defer func() { chatStatusGitBranchLookup = previousLookup }()
	resetChatStatusGitBranchCacheForTest()

	workspace := t.TempDir()
	repoRoot := filepath.Join(workspace, "ai-agent-runtime")
	nested := filepath.Join(repoRoot, "backend", "cmd")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("os.MkdirAll: %v", err)
	}
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("os.Mkdir .git: %v", err)
	}

	session := &ChatSession{
		Model:       "gpt-5.4-code",
		ProfileRoot: nested,
		// ProfileName intentionally empty so project resolves from git root.
	}
	status := buildChatSurfaceStatusLineForWidth(session, "Ready", 220)

	if !strings.Contains(status, "ai-agent-runtime") {
		t.Fatalf("expected project name from git root, got %q", status)
	}
	// Compact cwd should be the leaf directory when not equal to project.
	if !strings.Contains(status, "cmd") && !strings.Contains(status, nested) {
		t.Fatalf("expected nested cwd segment, got %q", status)
	}
	// Order is cwd then project when both are kept.
	if !strings.Contains(status, "cmd"+chatSurfaceStatusSeparator+"ai-agent-runtime") &&
		!strings.Contains(status, nested+chatSurfaceStatusSeparator+"ai-agent-runtime") {
		t.Fatalf("expected distinct cwd · project pair, got %q", status)
	}
}

func TestBuildChatSurfaceStatusLine_RespectsWidthAndKeepsRequiredState(t *testing.T) {
	queue := newChatInputQueue(nil)
	queue.routeLine(chatQueuedInput{Text: "first\n", Source: "stdin"})
	queue.routeLine(chatQueuedInput{Text: "second\n", Source: "stdin"})
	session := &ChatSession{
		PermissionMode:    runtimepolicy.ModeBypassPermissions,
		InputQueue:        queue,
		Model:             "gpt-5.4-code",
		ContextTokenCount: 28640,
		Provider: config.Provider{ModelCapabilities: map[string]config.ModelCapabilitySpec{
			"gpt-5.4-code": {MaxContextTokens: 128000},
		}},
	}

	for _, width := range []int{40, 80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			status := buildChatSurfaceStatusLineForWidth(session, "Awaiting approval", width)
			if got := ui.DisplayWidth(status); got > width {
				t.Fatalf("status width %d exceeds budget %d: %q", got, width, status)
			}
			hasApproval := strings.Contains(status, "等待审批") || strings.Contains(status, "审批")
			if !hasApproval {
				t.Fatalf("expected approval modal hint at width %d, got %q", width, status)
			}
			if !strings.Contains(status, "2") {
				t.Fatalf("expected queue count at width %d, got %q", width, status)
			}
			if strings.Contains(status, "权限") {
				t.Fatalf("permission mode should not appear on surface status, got %q", status)
			}
		})
	}
}

func TestBuildChatSurfaceStatusLine_ShowsModalHints(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  string
	}{
		{name: "planning", state: "Planning", want: "规划中"},
		{name: "awaiting answer", state: "Awaiting answer", want: "等待回答"},
		{name: "tool running", state: "Tool running", want: "执行工具"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := buildChatSurfaceStatusLineForWidth(&ChatSession{Model: "gpt-5.4-code"}, tt.state, 120)
			if !strings.Contains(status, tt.want) {
				t.Fatalf("expected modal/state hint %q in status, got %q", tt.want, status)
			}
			if strings.Contains(status, "权限") || strings.Contains(status, "输入 ") {
				t.Fatalf("legacy permission/input segments should be absent, got %q", status)
			}
		})
	}
}

func TestBuildChatSurfaceStatusLine_ShowsPlanModeState(t *testing.T) {
	inactive := buildChatSurfaceStatusLineForWidth(&ChatSession{Model: "gpt-5.4-code"}, "Ready", 120)
	if !strings.Contains(inactive, "Plan OFF") {
		t.Fatalf("expected inactive plan-mode status, got %q", inactive)
	}

	active := buildChatSurfaceStatusLineForWidth(&ChatSession{Model: "gpt-5.4-code", PermissionMode: runtimepolicy.ModePlan}, "Ready", 120)
	if !strings.Contains(active, "Plan ON") {
		t.Fatalf("expected active plan-mode status, got %q", active)
	}

	narrow := buildChatSurfaceStatusLineForWidth(&ChatSession{PermissionMode: runtimepolicy.ModePlan}, "Ready", 16)
	if !strings.Contains(narrow, "Plan ON") {
		t.Fatalf("expected narrow status to keep compact plan-mode state, got %q", narrow)
	}
}

func TestBuildChatSurfaceStatusLine_ExplicitModalInputModeOverridesAgentState(t *testing.T) {
	status := buildChatSurfaceStatusLineForWidthAndInputMode(
		&ChatSession{Model: "gpt-5.4-code"},
		"Ready",
		120,
		chatInputModeSelection,
	)
	if !strings.Contains(status, "选择选项") {
		t.Fatalf("expected selection modal hint, got %q", status)
	}
	if strings.Contains(status, "立即发送") || strings.Contains(status, "输入 ") {
		t.Fatalf("selection modal must not advertise legacy input destination, got %q", status)
	}
}

func TestPushChatComposerInputMode_SameModeLeaseReleaseCannotOverwriteNewOwner(t *testing.T) {
	session := &ChatSession{}
	interaction := newChatInteractionCoordinator(session)
	session.Interaction = interaction

	releaseOlder := pushChatComposerInputMode(session, chatInputModeSelection)
	olderGeneration := interaction.inputLease.generation
	releaseNewer := pushChatComposerInputMode(session, chatInputModeSelection)
	newerGeneration := interaction.inputLease.generation
	if newerGeneration <= olderGeneration {
		t.Fatalf("expected unique increasing lease generations, older=%d newer=%d", olderGeneration, newerGeneration)
	}

	releaseOlder()
	if got := interaction.InputMode(); got != chatInputModeSelection {
		t.Fatalf("older release must not overwrite newer same-mode lease, got %q", got)
	}
	releaseNewer()
	if got := interaction.InputMode(); got != chatInputModeChat {
		t.Fatalf("expected input mode to return to chat after both releases, got %q", got)
	}

	// Cleanup closures are deliberately idempotent.
	releaseOlder()
	releaseNewer()
	if got := interaction.InputMode(); got != chatInputModeChat {
		t.Fatalf("duplicate release changed input mode to %q", got)
	}
}

func TestBuildChatSurfaceStatusLine_ShowsConfirmationInputDestination(t *testing.T) {
	status := buildChatSurfaceStatusLineForWidthAndInputMode(
		&ChatSession{Model: "gpt-5.4-code"},
		"Ready",
		120,
		chatInputModeConfirmation,
	)
	if !strings.Contains(status, "确认操作") {
		t.Fatalf("expected confirmation modal hint, got %q", status)
	}
	if strings.Contains(status, "立即发送") || strings.Contains(status, "输入 ") {
		t.Fatalf("confirmation prompt must not advertise legacy input destination, got %q", status)
	}
}

func TestBuildChatSurfaceStatusLine_LocalizesAgentStates(t *testing.T) {
	tests := map[string]string{
		"Waiting":           "等待",
		"Thinking":          "思考",
		"Streaming":         "输出中",
		"Planning":          "规划中",
		"Tool running":      "执行工具",
		"Awaiting approval": "等待审批",
		"Awaiting answer":   "等待回答",
		"Stopping":          "停止中",
	}
	for state, want := range tests {
		t.Run(state, func(t *testing.T) {
			status := buildChatSurfaceStatusLineForWidth(&ChatSession{}, state, 120)
			if !strings.Contains(status, want) {
				t.Fatalf("expected localized state %q, got %q", want, status)
			}
		})
	}
	// Ready/Completed/Failed omit Chinese idle labels in the model-first bar.
	// Codex-aligned terminal stages are treated as Ready for the surface.
	for _, state := range []string{"Ready", "Completed", "Failed"} {
		t.Run(state+"_omits_idle_label", func(t *testing.T) {
			status := buildChatSurfaceStatusLineForWidth(&ChatSession{
				Provider: config.Provider{Protocol: "codex"},
				FastMode: false,
			}, state, 120)
			if strings.Contains(status, "就绪") || strings.Contains(status, "已完成") || strings.Contains(status, "失败") {
				t.Fatalf("expected idle state %q to omit Chinese label, got %q", state, status)
			}
			if !strings.Contains(status, "Fast off") {
				t.Fatalf("expected idle status to still show Fast segment for codex, got %q", status)
			}
		})
	}
}

func TestBuildChatSurfaceStatusLine_ToolDetailDegradesAtNarrowWidth(t *testing.T) {
	session := &ChatSession{PermissionMode: runtimepolicy.ModeDefault}
	wide := buildChatSurfaceStatusLineForWidth(session, "Tool shell_command", 120)
	if !strings.Contains(wide, "执行工具 shell_command") {
		t.Fatalf("expected wide status to include tool detail, got %q", wide)
	}
	narrow := buildChatSurfaceStatusLineForWidth(session, "Tool shell_command", 40)
	if !strings.Contains(narrow, "执行工具") || strings.Contains(narrow, "shell_command") {
		t.Fatalf("expected narrow status to keep stage and omit tool detail, got %q", narrow)
	}
}

func TestBuildChatSurfaceStatusLine_IncludesContextWindowWhenOnlyWindowIsKnown(t *testing.T) {
	session := &ChatSession{
		ContextWindowTokenCount: 128000,
	}

	status := buildChatSurfaceStatusLineForWidth(session, "Ready", 80)
	if !strings.Contains(status, "Context 0% used") {
		t.Fatalf("expected status line to include context window summary, got %q", status)
	}
	if !strings.Contains(status, "128K window") {
		t.Fatalf("expected status line to include window size, got %q", status)
	}
	if strings.Contains(status, "Thinking") {
		t.Fatalf("expected status line to keep supplied state only, got %q", status)
	}
}

func TestBuildChatSurfaceStatusLine_OmitsMessageCountFromComposer(t *testing.T) {
	session := &ChatSession{
		MsgCount:           2,
		StatusMessageCount: 37,
	}

	status := buildChatSurfaceStatusLineForWidth(session, "Ready", 120)
	if strings.Contains(status, "msgs ") {
		t.Fatalf("expected message count to stay in /status diagnostics, got %q", status)
	}
}

func TestBuildChatPromptNoticeLine_IncludesQueuedInputState(t *testing.T) {
	queue := newChatInputQueue(nil)
	queue.routeLine(chatQueuedInput{Text: "queued\n", Source: "stdin"})
	session := &ChatSession{
		InputQueue:       queue,
		queuedInputDrain: true,
	}

	notice := buildChatPromptNoticeLineForWidth(session, "Thinking", 80)
	if !strings.Contains(notice, "队列 1") || !strings.Contains(notice, "就绪后 /queue") {
		t.Fatalf("expected prompt notice to include queued input state, got %q", notice)
	}
	if !strings.Contains(notice, "  - queued") {
		t.Fatalf("expected prompt notice to include queued message preview, got %q", notice)
	}
}

func TestBuildChatPromptNoticeLine_IncludesReadySubmissionPreview(t *testing.T) {
	queue := newChatInputQueue(nil)
	queue.readyText = "confirmed draft\n"
	session := &ChatSession{
		InputQueue: queue,
	}

	notice := buildChatPromptNoticeLineForWidth(session, "Thinking", 80)
	if !strings.Contains(notice, "队列 1") || !strings.Contains(notice, "就绪后 /queue") {
		t.Fatalf("expected prompt notice to include ready submission state, got %q", notice)
	}
	if !strings.Contains(notice, "  - confirmed draft") {
		t.Fatalf("expected prompt notice to include ready submission preview, got %q", notice)
	}
}

func TestBuildChatPromptNoticeLine_QueueWidthMatrix(t *testing.T) {
	queue := newChatInputQueue(nil)
	queue.routeLine(chatQueuedInput{Text: strings.Repeat("很长的排队消息", 12) + "\n", Source: "stdin"})
	queue.routeLine(chatQueuedInput{Text: "second queued message\n", Source: "stdin"})
	session := &ChatSession{InputQueue: queue, queuedInputDrain: true}

	for _, width := range []int{40, 80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			notice := buildChatPromptNoticeLineForWidth(session, "Thinking", width)
			lines := strings.Split(notice, "\n")
			if len(lines) > 3 {
				t.Fatalf("expected bounded composer context rows, got %d: %q", len(lines), notice)
			}
			for _, line := range lines {
				if got := ui.DisplayWidth(line); got > width {
					t.Fatalf("notice width %d exceeds budget %d: %q", got, width, line)
				}
			}
			if !strings.Contains(lines[0], "队列 2") || !strings.Contains(lines[0], "/queue") {
				t.Fatalf("expected queue count and deferred management entry, got %q", notice)
			}
			if strings.Contains(lines[0], "/queue clear") {
				t.Fatalf("did not expect busy composer to advertise unavailable queue clear, got %q", notice)
			}
			if width == 40 && len(lines) != 1 {
				t.Fatalf("expected narrow queue context to suppress previews, got %q", notice)
			}
		})
	}
}

func TestBuildChatPromptNoticeLine_AttachmentWidthMatrix(t *testing.T) {
	session := &ChatSession{ImagePaths: []string{
		filepath.Join("very", "deep", "directory", "product-dashboard-reference-image.png"),
		filepath.Join("another", "long", "directory", "error-state.png"),
	}}

	for _, width := range []int{40, 80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			notice := buildChatPromptNoticeLineForWidth(session, "Ready", width)
			if got := ui.DisplayWidth(notice); got > width {
				t.Fatalf("attachment context width %d exceeds budget %d: %q", got, width, notice)
			}
			if !strings.Contains(notice, "图片") || !strings.Contains(notice, "2") || !strings.Contains(notice, "/attach") {
				t.Fatalf("expected attachment count and management entry, got %q", notice)
			}
			if width == 120 && (!strings.Contains(notice, "product-dashboard-reference-image.png") || !strings.Contains(notice, "remove N")) {
				t.Fatalf("expected wide context to expose attachment identity and removal action, got %q", notice)
			}
		})
	}
}

func TestBuildChatPromptNoticeLine_PrioritizesQueueAndCapsCombinedRows(t *testing.T) {
	queue := newChatInputQueue(nil)
	queue.routeLine(chatQueuedInput{Text: "queued follow-up\n", Source: "stdin"})
	session := &ChatSession{
		InputQueue: queue,
		ImagePaths: []string{filepath.Join("images", "reference.png")},
	}

	notice := buildChatPromptNoticeLineForWidth(session, "Ready", 120)
	lines := strings.Split(notice, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected queue, attachment and one preview row, got %q", notice)
	}
	if !strings.Contains(lines[0], "队列 1") || !strings.Contains(lines[1], "图片 1") {
		t.Fatalf("expected deterministic queue-first context priority, got %q", notice)
	}
	if !strings.Contains(lines[2], "queued follow-up") {
		t.Fatalf("expected remaining row budget to show one queue preview, got %q", notice)
	}

	running := buildChatPromptNoticeLineForWidth(session, "Planning", 120)
	if strings.Contains(running, "图片") {
		t.Fatalf("expected in-flight attachments to stay out of pending composer context, got %q", running)
	}
}

func TestFinishSuccessfulChatSendClearsAndRefreshesAttachmentContext(t *testing.T) {
	notifier := &chatTitleNotifier{
		snapshot: chatTitleSnapshot{baseState: chatTitleIdle},
		wake:     make(chan struct{}, 1),
	}
	session := &ChatSession{
		ImagePaths:    []string{filepath.Join("images", "reference.png")},
		TitleNotifier: notifier,
	}
	coord := newChatInteractionCoordinator(session)
	coord.agentStage = chatAgentStagePlanning
	session.Interaction = coord

	if before := buildChatPromptNoticeLineForWidth(session, "Ready", 80); !strings.Contains(before, "图片 1") {
		t.Fatalf("expected pending attachment context before successful send, got %q", before)
	}
	finishSuccessfulChatSend(session, "", true)

	if len(session.ImagePaths) != 0 {
		t.Fatalf("expected successful send to clear attachments, got %#v", session.ImagePaths)
	}
	if after := buildChatPromptNoticeLineForWidth(session, "Ready", 80); after != "" {
		t.Fatalf("expected attachment context to clear after successful send, got %q", after)
	}
	if state := notifier.currentSnapshot().baseState; state != chatTitleRunning {
		t.Fatalf("expected successful send to refresh the composer coordinator, title state=%v", state)
	}
}

func TestBuildChatSurfaceStatusLine_IncludesGoalStatusWhenSet(t *testing.T) {
	runtimeSession := runtimechat.NewSession("test-user")
	goal, err := runtimegoal.NewSessionGoal(runtimeSession.ID, "ship goal status", time.Now())
	if err != nil {
		t.Fatalf("NewSessionGoal: %v", err)
	}
	goal.Status = runtimegoal.StatusPaused
	if err := runtimegoal.NewMetadataStore().Put(runtimeSession, goal); err != nil {
		t.Fatalf("goal store Put: %v", err)
	}

	session := &ChatSession{RuntimeSession: runtimeSession}
	status := buildChatSurfaceStatusLineForWidth(session, "Ready", 120)
	if !strings.Contains(status, "Goal paused (/goal resume)") {
		t.Fatalf("expected surface status line to include Codex goal indicator, got %q", status)
	}
}

func TestBuildChatSurfaceStatusLine_OmitsGoalStatusWhenUnset(t *testing.T) {
	status := buildChatSurfaceStatusLine(&ChatSession{RuntimeSession: runtimechat.NewSession("test-user")}, "Ready")
	if strings.Contains(status, "Goal ") || strings.Contains(status, "Pursuing goal") || strings.Contains(status, "目标 ") {
		t.Fatalf("expected status line to omit missing goal, got %q", status)
	}
}

func TestBuildChatSurfaceStatusLine_IncludesActiveGoalUsage(t *testing.T) {
	runtimeSession := runtimechat.NewSession("test-user")
	goal, err := runtimegoal.NewSessionGoal(runtimeSession.ID, "finish remaining polish", time.Now())
	if err != nil {
		t.Fatalf("NewSessionGoal: %v", err)
	}
	goal.Status = runtimegoal.StatusActive
	goal.TokenBudget = 10000
	goal.TokensUsed = 2500
	if err := runtimegoal.NewMetadataStore().Put(runtimeSession, goal); err != nil {
		t.Fatalf("goal store Put: %v", err)
	}

	session := &ChatSession{RuntimeSession: runtimeSession}
	status := buildChatSurfaceStatusLineForWidth(session, "Ready", 120)
	if !strings.Contains(status, "Pursuing goal (2.5K / 10.0K)") {
		t.Fatalf("expected active goal usage indicator, got %q", status)
	}
}

func TestBuildChatSurfaceStatusLine_IncludesCompletedGoal(t *testing.T) {
	runtimeSession := runtimechat.NewSession("test-user")
	goal, err := runtimegoal.NewSessionGoal(runtimeSession.ID, "finish remaining polish", time.Now())
	if err != nil {
		t.Fatalf("NewSessionGoal: %v", err)
	}
	goal.Status = runtimegoal.StatusComplete
	goal.TokenBudget = 8000
	goal.TokensUsed = 4200
	if err := runtimegoal.NewMetadataStore().Put(runtimeSession, goal); err != nil {
		t.Fatalf("goal store Put: %v", err)
	}

	session := &ChatSession{RuntimeSession: runtimeSession}
	status := buildChatSurfaceStatusLineForWidth(session, "Ready", 120)
	if !strings.Contains(status, "Goal achieved (4.2K tokens)") {
		t.Fatalf("expected completed goal indicator, got %q", status)
	}
}

func TestChatSurfaceGoalUsage_IncludesLiveActiveTurnElapsed(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	// Codex: baseline = max(observed_at, active_turn_started_at).
	// Turn started before last observation: only post-observation seconds accrue.
	goal := &runtimegoal.SessionGoal{
		Status:          runtimegoal.StatusActive,
		TimeUsedSeconds: 60,
		UpdatedAt:       now.Add(-60 * time.Second),
	}
	usage := chatSurfaceGoalUsage(goal, now.Add(-3*time.Minute), now)
	if usage != "2m" {
		t.Fatalf("expected live accrual from observed_at, got %q", usage)
	}

	// Idle gap before turn start must not count.
	goal.UpdatedAt = now.Add(-5 * time.Minute)
	usage = chatSurfaceGoalUsage(goal, now.Add(-60*time.Second), now)
	if usage != "2m" {
		t.Fatalf("expected baseline max(observed_at, turn_start), got %q", usage)
	}
}

func TestChatSurfaceGoalUsage_ActiveWithoutBudgetAlwaysShowsElapsed(t *testing.T) {
	goal := &runtimegoal.SessionGoal{
		Status:          runtimegoal.StatusActive,
		TimeUsedSeconds: 0,
	}
	usage := chatSurfaceGoalUsage(goal, time.Time{}, time.Now())
	if usage != "0s" {
		t.Fatalf("expected Codex-style 0s elapsed for active unbudgeted goal, got %q", usage)
	}
}

func TestFormatChatSurfaceGoalElapsed_MatchesCodex(t *testing.T) {
	cases := map[int64]string{
		0:                             "0s",
		59:                            "59s",
		60:                            "1m",
		30 * 60:                       "30m",
		90 * 60:                       "1h 30m",
		2 * 60 * 60:                   "2h",
		24*60*60 - 1:                  "23h 59m",
		24 * 60 * 60:                  "1d 0h 0m",
		2*24*60*60 + 23*60*60 + 42*60: "2d 23h 42m",
	}
	for seconds, want := range cases {
		if got := formatChatSurfaceGoalElapsed(seconds); got != want {
			t.Fatalf("formatChatSurfaceGoalElapsed(%d)=%q, want %q", seconds, got, want)
		}
	}
}

func TestMarkChatGoalStatusActiveTurnStarted_IsStickyWithinTurn(t *testing.T) {
	session := &ChatSession{}
	markChatGoalStatusActiveTurnStarted(session)
	first := chatGoalStatusActiveTurnStartedAt(session)
	if first.IsZero() {
		t.Fatal("expected active turn start timestamp")
	}
	markChatGoalStatusActiveTurnStarted(session)
	second := chatGoalStatusActiveTurnStartedAt(session)
	if !first.Equal(second) {
		t.Fatalf("expected sticky turn start across auto-continuation, first=%v second=%v", first, second)
	}
	clearChatGoalStatusActiveTurnStarted(session)
	if !chatGoalStatusActiveTurnStartedAt(session).IsZero() {
		t.Fatal("expected cleared active turn start")
	}
}

func TestStartWaitingAndClearWaiting_TrackGoalStatusActiveTurn(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord

	coord.StartWaiting()
	if chatGoalStatusActiveTurnStartedAt(session).IsZero() {
		t.Fatal("expected StartWaiting to mark goal-status active turn start")
	}
	// Nested mark from auto-continuation style refresh must remain sticky.
	markChatGoalStatusActiveTurnStarted(session)
	started := chatGoalStatusActiveTurnStartedAt(session)

	coord.ClearWaiting()
	if !chatGoalStatusActiveTurnStartedAt(session).IsZero() {
		t.Fatal("expected ClearWaiting to clear goal-status active turn start")
	}
	if started.IsZero() {
		t.Fatal("expected non-zero sticky start before clear")
	}
}

func TestBuildChatSurfaceStatusLine_IncludesActiveGoalLiveElapsed(t *testing.T) {
	runtimeSession := runtimechat.NewSession("test-user")
	now := time.Now()
	goal, err := runtimegoal.NewSessionGoal(runtimeSession.ID, "live elapsed", now.Add(-5*time.Minute))
	if err != nil {
		t.Fatalf("NewSessionGoal: %v", err)
	}
	goal.Status = runtimegoal.StatusActive
	goal.TimeUsedSeconds = 60
	goal.UpdatedAt = now.Add(-5 * time.Minute)
	if err := runtimegoal.NewMetadataStore().Put(runtimeSession, goal); err != nil {
		t.Fatalf("goal store Put: %v", err)
	}

	session := &ChatSession{RuntimeSession: runtimeSession}
	session.goalStatusActiveTurnStartedAt = now.Add(-90 * time.Second)
	status := buildChatSurfaceStatusLineForWidth(session, "Thinking", 120)
	if !strings.Contains(status, "Pursuing goal (2m)") {
		t.Fatalf("expected live active-goal elapsed indicator, got %q", status)
	}
}

func TestBuildChatSurfaceStatusLine_FallsBackToDefaultContextWindowWhenNoCapabilityExists(t *testing.T) {
	session := &ChatSession{
		TokenCount:        500000,
		ContextTokenCount: 28640,
	}

	status := buildChatSurfaceStatusLine(session, "Ready")
	// used=28640 / provider default 256000 with Codex baseline → ~7%
	if !strings.Contains(status, "Context 7% used") {
		t.Fatalf("expected default context window summary, got %q", status)
	}
	if strings.Contains(status, "8000") {
		t.Fatalf("expected status line to avoid small output-limit fallback, got %q", status)
	}
}

func TestBuildChatSurfaceStatusLine_UsesZeroWhenCountersAreMissing(t *testing.T) {
	messages := []runtimetypes.Message{
		*runtimetypes.NewSystemMessage("system prompt"),
		*runtimetypes.NewUserMessage("用户问题"),
		*runtimetypes.NewAssistantMessage("模型回答"),
	}
	previousLookup := chatStatusGitBranchLookup
	chatStatusGitBranchLookup = func(string) string { return "" }
	defer func() { chatStatusGitBranchLookup = previousLookup }()
	resetChatStatusGitBranchCacheForTest()

	session := &ChatSession{
		Model:        "gpt-5.4-code",
		ProviderName: "openai",
		ProfileRoot:  filepath.Clean(`E:\proj`),
		ProfileName:  "proj",
		Messages:     messages,
		Provider: config.Provider{
			ModelCapabilities: map[string]config.ModelCapabilitySpec{
				"gpt-5.4-code": {
					MaxContextTokens: 128000,
				},
			},
		},
	}

	// Force a wide budget so trailing segments (Fast/window) stay visible while we
	// assert the history-based context percent.
	status := buildChatSurfaceStatusLineForWidth(session, "Ready", 200)
	wantUsed := countChatContextTokensForMessages(session, messages)
	wantPercent := chatStatusContextUsedPercent(wantUsed, 128000)
	wantFull := fmt.Sprintf("Context %d%% used", wantPercent)
	wantCompact := fmt.Sprintf("Ctx %d%%", wantPercent)
	if !strings.Contains(status, wantFull) && !strings.Contains(status, wantCompact) {
		t.Fatalf("expected status line to fall back to history context estimate %d%%, got %q", wantPercent, status)
	}
}

func TestBuildChatSurfaceStatusLine_IncludesProvider(t *testing.T) {
	previousLookup := chatStatusGitBranchLookup
	chatStatusGitBranchLookup = func(string) string { return "" }
	defer func() { chatStatusGitBranchLookup = previousLookup }()
	resetChatStatusGitBranchCacheForTest()

	// Prefer EffectiveProvider over ProviderName.
	status := buildChatSurfaceStatusLineForWidth(&ChatSession{
		Model:             "gpt-5.4-code",
		ProviderName:      "configured-alias",
		EffectiveProvider: "canonical-provider",
		ProfileRoot:       filepath.Clean(`E:\proj`),
		ProfileName:       "proj",
	}, "Ready", 120)
	if !strings.Contains(status, "canonical-provider") {
		t.Fatalf("expected effective provider in status line, got %q", status)
	}
	if strings.Contains(status, "configured-alias") {
		t.Fatalf("expected status line to prefer effective provider over alias, got %q", status)
	}

	// Fall back to ProviderName when EffectiveProvider is empty.
	status = buildChatSurfaceStatusLineForWidth(&ChatSession{
		Model:        "gpt-5.4-code",
		ProviderName: "openai",
		ProfileRoot:  filepath.Clean(`E:\proj`),
		ProfileName:  "proj",
	}, "Ready", 120)
	if !strings.Contains(status, "openai") {
		t.Fatalf("expected provider name in status line, got %q", status)
	}

	// Fall back to protocol when no provider name is configured.
	status = buildChatSurfaceStatusLineForWidth(&ChatSession{
		Model:       "gpt-5.4-code",
		ProfileRoot: filepath.Clean(`E:\proj`),
		ProfileName: "proj",
		Provider:    config.Provider{Protocol: "anthropic"},
	}, "Ready", 120)
	if !strings.Contains(status, "anthropic") {
		t.Fatalf("expected protocol fallback provider in status line, got %q", status)
	}
}

func TestBuildChatSurfaceStatusLine_ShowsFastOnWhenStreaming(t *testing.T) {
	previousLookup := chatStatusGitBranchLookup
	chatStatusGitBranchLookup = func(string) string { return "" }
	defer func() { chatStatusGitBranchLookup = previousLookup }()
	resetChatStatusGitBranchCacheForTest()

	status := buildChatSurfaceStatusLineForWidth(&ChatSession{
		Stream:      true,
		FastMode:    true,
		Model:       "gpt-5.4-code",
		ProfileRoot: filepath.Clean(`E:\proj`),
		ProfileName: "proj",
		Provider:    config.Provider{Protocol: "codex"},
	}, "Ready", 120)
	if !strings.Contains(status, "Fast on") {
		t.Fatalf("expected Fast on when FastMode enabled on codex, got %q", status)
	}
	// Stream alone must not imply Fast on.
	streamOnly := buildChatSurfaceStatusLineForWidth(&ChatSession{
		Stream:   true,
		FastMode: false,
		Model:    "gpt-5.4-code",
		Provider: config.Provider{Protocol: "codex"},
	}, "Ready", 120)
	if !strings.Contains(streamOnly, "Fast off") {
		t.Fatalf("expected Fast off when Stream on but FastMode off, got %q", streamOnly)
	}
	// Non-codex protocols omit the Fast segment entirely.
	nonCodex := buildChatSurfaceStatusLineForWidth(&ChatSession{
		Stream:   true,
		FastMode: true,
		Model:    "gpt-5.4-code",
		Provider: config.Provider{Protocol: "openai"},
	}, "Ready", 120)
	if strings.Contains(nonCodex, "Fast on") || strings.Contains(nonCodex, "Fast off") {
		t.Fatalf("expected Fast segment omitted for non-codex protocol, got %q", nonCodex)
	}
}

func TestBuildChatSurfaceStatusLine_IncludesTokenAndWindowSegments(t *testing.T) {
	session := &ChatSession{
		Model:             "gpt-5.6-sol",
		ReasoningEffort:   "xhigh",
		ProviderName:      "codex_ee",
		ContextTokenCount: 100000,
		InputTokenCount:   332_000_000,
		OutputTokenCount:  970_000,
		FastMode:          false,
		Provider: config.Provider{
			Protocol: "codex",
			ModelCapabilities: map[string]config.ModelCapabilitySpec{
				"gpt-5.6-sol": {MaxContextTokens: 258000},
			},
		},
	}
	status := buildChatSurfaceStatusLineForWidth(session, "Ready", 160)
	for _, want := range []string{
		"gpt-5.6-sol xhigh",
		"codex_ee",
		"258K window",
		"332M in",
		"970K out",
		"Fast off",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("expected status line to contain %q, got %q", want, status)
		}
	}
}

func TestResolveChatStatusUsedTokens_UsesCumulativeTokenCount(t *testing.T) {
	session := &ChatSession{
		TokenCount:        500000,
		ContextTokenCount: 28640,
		Messages:          []runtimetypes.Message{*runtimetypes.NewUserMessage("ignored")},
		Provider: config.Provider{
			ModelCapabilities: map[string]config.ModelCapabilitySpec{
				"gpt-5.4-code": {MaxContextTokens: 128000},
			},
		},
	}

	if got := resolveChatStatusUsedTokens(session); got != 28640 {
		t.Fatalf("expected active context token count, got %d", got)
	}
}

func TestResolveChatStatusUsedTokens_UsesTokenCountOnly(t *testing.T) {
	session := &ChatSession{
		TokenCount: 500000,
	}

	if got := resolveChatStatusUsedTokens(session); got != 0 {
		t.Fatalf("expected cumulative API token count to be ignored for context used, got %d", got)
	}
}

func TestResolveChatStatusUsedTokens_IgnoresTurnAggregateOnly(t *testing.T) {
	session := &ChatSession{
		TurnContextTokenCount: 500000,
	}

	if got := resolveChatStatusUsedTokens(session); got != 0 {
		t.Fatalf("expected turn aggregate token count to be ignored, got %d", got)
	}
}

func TestResolveChatStatusUsedTokens_ReturnsZeroWhenCountersMissing(t *testing.T) {
	messages := []runtimetypes.Message{
		*runtimetypes.NewSystemMessage("system prompt"),
		*runtimetypes.NewUserMessage("用户问题"),
		*runtimetypes.NewAssistantMessage("模型回答"),
	}
	session := &ChatSession{
		Messages: messages,
		Provider: config.Provider{
			ModelCapabilities: map[string]config.ModelCapabilitySpec{
				"gpt-5.4-code": {MaxContextTokens: 128000},
			},
		},
	}

	want := countChatContextTokensForMessages(session, messages)
	if got := resolveChatStatusUsedTokens(session); got != want {
		t.Fatalf("expected history context token estimate %d when explicit context token count is missing, got %d", want, got)
	}
}

func TestResolveChatStatusCurrentDirectory_UsesProfileRootWhenPresent(t *testing.T) {
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	baseDir := t.TempDir()
	if err := os.Chdir(baseDir); err != nil {
		t.Fatalf("os.Chdir: %v", err)
	}
	defer func() {
		_ = os.Chdir(previousWD)
	}()

	session := &ChatSession{ProfileRoot: filepath.Join("profiles", "demo")}
	got := resolveChatStatusCurrentDirectory(session)
	want := filepath.Clean(filepath.Join(baseDir, "profiles", "demo"))
	if got != want {
		t.Fatalf("expected current directory %q, got %q", want, got)
	}
}

func TestChatInteractionCoordinator_AdvanceAfterPromptWhenStdinIsPiped(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.writer = os.Stdout

	originalStdin := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() {
		os.Stdin = originalStdin
		_ = reader.Close()
		_ = writer.Close()
	}()
	os.Stdin = reader

	if !coord.shouldAdvanceAfterPromptLocked() {
		t.Fatal("expected prompt advance when stdin is piped")
	}
}

func TestChatInteractionCoordinator_DebouncesPromptRedraw(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.promptDelay = 10 * time.Millisecond
	output := &synchronizedBuffer{}
	coord.SetWriter(output)

	coord.SchedulePromptRedraw()
	coord.SchedulePromptRedraw()
	coord.SchedulePromptRedraw()

	require.Eventually(t, func() bool {
		return strings.Count(output.String(), ui.UserPromptText(0)) == 1
	}, 200*time.Millisecond, 10*time.Millisecond)

	rendered := output.String()
	if strings.Count(rendered, ui.UserPromptText(0)) != 1 {
		t.Fatalf("expected exactly one prompt redraw, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_FinalizeAssistantDelta_ReformatsMarkdown(t *testing.T) {
	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAssistantDelta("# Title\n")
	coord.FinalizeAssistantDelta()

	rendered := output.String()
	// Goldmark path renders ATX h1 as "▶ Title" (not raw "# Title").
	if !strings.Contains(rendered, "Title") {
		t.Fatalf("expected streamed markdown content in output, got %q", rendered)
	}
	if !strings.Contains(rendered, "▶") && !strings.Contains(rendered, "# Title") {
		t.Fatalf("expected formatted heading marker after rewrite, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_RenderAssistant_FormatsIndentedTable(t *testing.T) {
	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAssistant("下面是一个 Markdown 表格格式示例：\n\n  | 列名1 | 列名2 | 列名3 |\n  |------|------|------|\n  | 数据A | 数据B | 数据C |\n  | 数据D | 数据E | 数据F |")

	rendered := output.String()
	if !strings.Contains(rendered, "列名1") || !strings.Contains(rendered, "数据A") {
		t.Fatalf("expected assistant renderer to format indented table, got %q", rendered)
	}
	if !strings.Contains(rendered, "│") && !strings.Contains(rendered, "列名1:") {
		t.Fatalf("expected table grid or records layout, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_FinalizeAssistantDelta_FormatsOnlyIndentedTable(t *testing.T) {
	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAssistantDelta("  | 列名1 | 列名2 | 列名3 |\n")
	coord.RenderAssistantDelta("  |------|------|------|\n")
	coord.RenderAssistantDelta("  | 数据A | 数据B | 数据C |\n")
	coord.RenderAssistantDelta("  | 数据D | 数据E | 数据F |")
	coord.FinalizeAssistantDelta()

	rendered := output.String()
	if !strings.Contains(rendered, "列名1") || !strings.Contains(rendered, "数据A") {
		t.Fatalf("expected finalized delta path to format indented table, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_RenderAssistant_FormatsMixedMarkdownDocument(t *testing.T) {
	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	input := "# 混合 Markdown 验证\n\n> 这是一条引用\n\n- 苹果\n- 香蕉\n- 樱桃\n\n| 名称 | 值 |\n| ---- | ---- |\n| A | 1 |\n| B | 2 |\n\n```go\npackage main\nimport \"fmt\"\nfmt.Println(\"hello\")\n```"
	coord.RenderAssistant(input)

	rendered := output.String()
	for _, expected := range []string{
		"混合 Markdown 验证",
		"这是一条引用",
		"• 苹果",
		"• 香蕉",
		"• 樱桃",
		"名称",
		"package main",
		"fmt.Println",
		"hello",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected mixed markdown render to contain %q, got %q", expected, rendered)
		}
	}
	if strings.Contains(rendered, "```go") {
		t.Fatalf("expected fenced go block to be rendered as code content, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_ActiveBandOnSurfaceDuringMarkdownStream(t *testing.T) {
	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coord.SetSurface(surface)

	coord.RenderAssistantDelta("# Title\n\n")
	coord.RenderAssistantDelta("Hello stable paragraph.\n")
	if !strings.Contains(output.String(), "Title") || strings.Contains(output.String(), "Hello stable paragraph") {
		t.Fatalf("surface path should commit only the completed markdown block, got %q", output.String())
	}
	if coord.activeStream == nil || !coord.activeStream.Active() {
		t.Fatal("expected active stream controller to be active")
	}
	coord.RefreshActiveStreamViewport()
	band := surface.ActiveBandLines()
	if len(band) == 0 {
		t.Fatal("expected active band lines on enabled surface")
	}
	joined := strings.Join(band, "\n")
	if !strings.Contains(joined, "Hello") && !strings.Contains(joined, "Title") && !strings.Contains(joined, "assistant") {
		t.Fatalf("expected band to mirror stream viewport, got %q", joined)
	}

	coord.FinalizeAssistantDelta()
	if coord.activeStream.Active() {
		t.Fatal("active stream should clear after finalize")
	}
	if len(surface.ActiveBandLines()) != 0 {
		t.Fatalf("active band should clear after finalize, got %v", surface.ActiveBandLines())
	}
	if !strings.Contains(output.String(), "Hello") && !strings.Contains(output.String(), "Title") {
		t.Fatalf("expected finalized transcript commit, got %q", output.String())
	}
	if strings.Count(output.String(), "Title") != 1 {
		t.Fatalf("stable markdown prefix should commit exactly once, got %q", output.String())
	}
}

func TestChatInteractionCoordinator_ActiveBandDeliversCoalescedFinalFrame(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	coord.activeStream.Policy = motion.NewPolicy(motion.Config{
		Forced:      motion.ForceMode(motion.ModeOff),
		Interactive: true,
	})
	var output bytes.Buffer
	coord.SetWriter(&output)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(40, 24)
	coord.SetSurface(surface)

	coord.RenderAssistantDelta("one\ntwo\nthree\nfour\nfive\nsix\nseven\n")
	coord.RenderAssistantDelta("coalesced-final-row\n")
	waitForActiveBandText(t, surface, "coalesced-final-row", time.Second)
	if !strings.Contains(output.String(), "one") || strings.Contains(output.String(), "coalesced-final-row") {
		t.Fatalf("only overflowed stable rows should enter scrollback before finalize, got %q", output.String())
	}
}

func TestChatInteractionCoordinator_ActiveBandPromotesLongMarkdownList(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false)}
	coord := newChatInteractionCoordinator(session)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coord.SetSurface(surface)

	var output bytes.Buffer
	coord.SetWriter(&output)
	captureSurfaceStdout(t, func() {
		for i := 1; i <= 20; i++ {
			coord.RenderAssistantDelta(fmt.Sprintf("- item %02d\n", i))
		}
	})

	committed := output.String()
	if !strings.Contains(committed, "item 01") || strings.Contains(committed, "item 20") {
		t.Fatalf("expected older list items in scrollback and newest item in tail, got %q", committed)
	}
	band := strings.Join(surface.ActiveBandLines(), "\n")
	if !strings.Contains(band, "item 20") {
		t.Fatalf("newest list item missing from ActiveBand: %q", band)
	}

	captureSurfaceStdout(t, func() {
		coord.FinalizeAssistantDelta()
	})
	rendered := output.String()
	for i := 1; i <= 20; i++ {
		want := fmt.Sprintf("item %02d", i)
		if strings.Count(rendered, want) != 1 {
			t.Fatalf("list item %q should render exactly once, got %q", want, rendered)
		}
	}
}

func TestChatInteractionCoordinator_ActiveBandSpinnerAdvancesWithoutDelta(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	coord.activeStream.Policy = motion.NewPolicy(motion.Config{
		Forced:      motion.ForceMode(motion.ModeFull),
		Interactive: true,
		Frames:      []string{"a", "b"},
		Interval:    40 * time.Millisecond,
	})
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(40, 24)
	coord.SetSurface(surface)
	coord.RenderAssistantDelta("spinner body that is long enough to stream\n")
	initial := strings.Join(surface.ActiveBandLines(), "\n")

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current := strings.Join(surface.ActiveBandLines(), "\n")
		if current != "" && current != initial {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("active band activity marker did not advance without a new delta: %q", initial)
}

func TestChatInteractionCoordinator_FinalizeUsesActiveStreamSource(t *testing.T) {
	session := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false)}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coord.SetSurface(surface)

	coord.RenderAssistantDelta("controller-owned final content\n")
	coord.streamBuffer.Reset() // Simulate a missing duplicate coordinator snapshot.
	coord.FinalizeAssistantDelta()
	if !strings.Contains(output.String(), "controller-owned final content") {
		t.Fatalf("finalize should consolidate the active stream source, got %q", output.String())
	}
	if coord.activeStream.Active() || len(surface.ActiveBandLines()) != 0 {
		t.Fatal("finalize should release the active cell and clear its viewport")
	}
}

func TestChatInteractionCoordinator_ToolRunningPaintsActiveBand(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coord.SetSurface(surface)

	coord.SetAgentStageDetail(chatAgentStageToolRunning, "shell_command")
	if output.String() != "" {
		t.Fatalf("tool active band must not write scrollback, got %q", output.String())
	}
	band := surface.ActiveBandLines()
	if len(band) == 0 {
		t.Fatal("expected tool-running active band lines")
	}
	joined := strings.Join(band, "\n")
	if !strings.Contains(strings.ToLower(joined), "shell") && !strings.Contains(joined, "shell_command") {
		t.Fatalf("expected tool name in active band, got %q", joined)
	}
	if !coord.activeStream.IsToolActive() {
		t.Fatal("expected active stream tool cell")
	}

	// Progress with same detail should not thrash or clear the band.
	coord.SetAgentStageDetail(chatAgentStageToolRunning, "shell_command")
	if len(surface.ActiveBandLines()) == 0 {
		t.Fatal("expected band to remain after identical tool progress")
	}

	coord.SetAgentStageDetail(chatAgentStageToolRunning, "view_file")
	joined = strings.Join(surface.ActiveBandLines(), "\n")
	if !strings.Contains(strings.ToLower(joined), "view") && !strings.Contains(joined, "view_file") {
		t.Fatalf("expected updated tool name in active band, got %q", joined)
	}

	coord.ClearAgentStage()
	if len(surface.ActiveBandLines()) != 0 {
		t.Fatalf("expected active band cleared after idle stage, got %v", surface.ActiveBandLines())
	}
	if coord.activeStream.IsToolActive() {
		t.Fatal("tool cell should cancel when stage returns idle")
	}
}

func TestChatInteractionCoordinator_ToolFinishIsScopedByCallID(t *testing.T) {
	coord := newChatInteractionCoordinator(&ChatSession{})
	t.Cleanup(coord.Shutdown)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coord.SetSurface(surface)

	coord.SetToolAgentStage("call-1", "shell compiling")
	coord.SetToolAgentStage("call-2", "view reading")
	coord.FinishToolAgentStage("call-1", "shell")
	joined := strings.Join(surface.ActiveBandLines(), "\n")
	if !strings.Contains(strings.ToLower(joined), "view") || strings.Contains(strings.ToLower(joined), "shell") {
		t.Fatalf("late finish for old call cleared or replaced newer tool: %q", joined)
	}
	if coord.AgentStage() != chatAgentStageToolRunning {
		t.Fatalf("newer tool should remain running, stage=%q", coord.AgentStage())
	}

	coord.FinishToolAgentStage("call-2", "view")
	if len(surface.ActiveBandLines()) != 0 || coord.activeStream.IsToolActive() {
		t.Fatalf("finishing last call should clear ActiveBand, got %v", surface.ActiveBandLines())
	}
	if coord.AgentStage() != chatAgentStagePlanning {
		t.Fatalf("active run should return to planning after its last tool, stage=%q", coord.AgentStage())
	}
}

func TestChatInteractionCoordinator_ToolProgressUpdatesActiveBand(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coord.SetSurface(surface)

	coord.SetAgentStageDetail(chatAgentStageToolRunning, "shell 10% starting")
	if output.String() != "" {
		t.Fatalf("tool progress band must not write scrollback, got %q", output.String())
	}
	joined := strings.Join(surface.ActiveBandLines(), "\n")
	if !strings.Contains(strings.ToLower(joined), "shell") {
		t.Fatalf("expected tool name in band, got %q", joined)
	}
	if !strings.Contains(joined, "10%") {
		t.Fatalf("expected progress text in band, got %q", joined)
	}
	if coord.activeStream.ToolName() != "shell" {
		t.Fatalf("ToolName=%q want shell", coord.activeStream.ToolName())
	}
	if coord.activeStream.ToolProgress() != "10% starting" {
		t.Fatalf("ToolProgress=%q", coord.activeStream.ToolProgress())
	}

	// Same tool, richer progress should update the band body in place.
	coord.SetAgentStageDetail(chatAgentStageToolRunning, "shell 45% downloading")
	if output.String() != "" {
		t.Fatalf("progress update must not write scrollback, got %q", output.String())
	}
	waitForActiveBandText(t, surface, "45%", time.Second)
	joined = strings.Join(surface.ActiveBandLines(), "\n")
	if !strings.Contains(joined, "45%") {
		t.Fatalf("expected updated progress in band, got %q", joined)
	}
	if strings.Contains(joined, "10%") {
		t.Fatalf("stale progress should be replaced, got %q", joined)
	}
	if coord.activeStream.ToolName() != "shell" {
		t.Fatalf("tool name should stay shell, got %q", coord.activeStream.ToolName())
	}

	// Identical name+progress should keep the band without clearing.
	coord.SetAgentStageDetail(chatAgentStageToolRunning, "shell 45% downloading")
	if len(surface.ActiveBandLines()) == 0 {
		t.Fatal("identical progress must keep active band")
	}

	coord.ClearAgentStage()
	if len(surface.ActiveBandLines()) != 0 {
		t.Fatalf("expected band cleared after idle, got %v", surface.ActiveBandLines())
	}
}

func waitForActiveBandText(t *testing.T, surface *ui.FixedBottomSurface, expected string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		joined := strings.Join(surface.ActiveBandLines(), "\n")
		if strings.Contains(joined, expected) {
			return joined
		}
		if time.Now().After(deadline) {
			t.Fatalf("active band did not contain %q before timeout; got %q", expected, joined)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSplitToolStageDetail(t *testing.T) {
	name, progress := splitToolStageDetail("shell 45% downloading")
	if name != "shell" || progress != "45% downloading" {
		t.Fatalf("got name=%q progress=%q", name, progress)
	}
	name, progress = splitToolStageDetail("view_file")
	if name != "view_file" || progress != "" {
		t.Fatalf("got name=%q progress=%q", name, progress)
	}
	name, progress = splitToolStageDetail("  ")
	if name != "" || progress != "" {
		t.Fatalf("empty detail got name=%q progress=%q", name, progress)
	}
}

func TestActiveStreamStableScrollbackCuts(t *testing.T) {
	markdownSource := "# One\n\nParagraph two.\n\nMutable tail"
	if got, want := markdownStableScrollbackCut(markdownSource, 0, len(markdownSource)), len("# One\n\nParagraph two.\n\n"); got != want {
		t.Fatalf("markdownStableScrollbackCut=%d want %d", got, want)
	}
	plainSource := "one\ntwo\nthree\nfour\nfive\n"
	if got, want := plainStableScrollbackCut(plainSource, 0, 80, 4), len("one\ntwo\n"); got != want {
		t.Fatalf("plainStableScrollbackCut=%d want %d", got, want)
	}
	if got := plainStableScrollbackCut("very-long-line-without-newline", 0, 8, 3); got != 0 {
		t.Fatalf("partial line must remain mutable, cut=%d", got)
	}
	listSource := "- one\n- two\n- three\n"
	if got, want := markdownStableScrollbackCut(listSource, 0, len(listSource)), len("- one\n- two\n"); got != want {
		t.Fatalf("list stable cut=%d want %d", got, want)
	}
	nestedList := "- parent\n  - child\n- next parent\n"
	if got, want := markdownStableScrollbackCut(nestedList, 0, len(nestedList)), len("- parent\n  - child\n"); got != want {
		t.Fatalf("nested list stable cut=%d want %d", got, want)
	}
	fencedSource := "````go\ncode\n```\nstill code\n````\nafter\n"
	if got, want := markdownStableScrollbackCut(fencedSource, 0, len(fencedSource)), len("````go\ncode\n```\nstill code\n````\n"); got != want {
		t.Fatalf("fenced stable cut=%d want %d", got, want)
	}
}

func TestChatInteractionCoordinator_AgentStageDetailKeepsProgressBudget(t *testing.T) {
	coord := newChatInteractionCoordinator(&ChatSession{})
	// Longer than the old 48 budget, under the 96 ActiveBand-oriented budget.
	detail := "shell 45% downloading large artifact from remote cache mirror xyz"
	if ui.DisplayWidth(detail) <= 48 {
		t.Fatalf("test detail should exceed old 48 budget, width=%d", ui.DisplayWidth(detail))
	}
	if ui.DisplayWidth(detail) > chatAgentStageDetailMaxWidth {
		t.Fatalf("test detail should fit new budget %d, width=%d", chatAgentStageDetailMaxWidth, ui.DisplayWidth(detail))
	}

	coord.SetAgentStageDetail(chatAgentStageToolRunning, detail)
	if got := coord.AgentStageDetail(); got != detail {
		t.Fatalf("stage detail truncated too early: got %q want %q", got, detail)
	}
	if got := coord.currentSurfaceStateForTest(); got != "Tool "+detail {
		t.Fatalf("surface state lost progress budget, got %q", got)
	}

	// Over-budget details still compact rather than unbounded growth.
	tooLong := detail + " extra-tail-that-forces-truncation-aaaaaaaa"
	coord.SetAgentStageDetail(chatAgentStageToolRunning, tooLong)
	got := coord.AgentStageDetail()
	if got == "" || got == tooLong {
		t.Fatalf("expected over-budget detail to compact, got %q", got)
	}
	if ui.DisplayWidth(got) > chatAgentStageDetailMaxWidth {
		t.Fatalf("compacted detail width %d exceeds max %d (%q)", ui.DisplayWidth(got), chatAgentStageDetailMaxWidth, got)
	}
}

func TestChatInteractionCoordinator_ToolBandYieldsToAssistantStream(t *testing.T) {
	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coord.SetSurface(surface)

	coord.SetAgentStageDetail(chatAgentStageToolRunning, "shell_command")
	if len(surface.ActiveBandLines()) == 0 {
		t.Fatal("expected tool band before assistant stream")
	}

	coord.RenderAssistantDelta("Hello stable plain response.\n")
	coord.RefreshActiveStreamViewport()
	if coord.activeStream.IsToolActive() {
		t.Fatal("assistant stream should replace tool cell")
	}
	band := surface.ActiveBandLines()
	joined := strings.Join(band, "\n")
	if !strings.Contains(joined, "Hello") && !strings.Contains(joined, "assistant") {
		t.Fatalf("expected assistant band content, got %q", joined)
	}

	// Tool stage updates must not clobber an in-progress assistant band.
	coord.SetAgentStageDetail(chatAgentStageToolRunning, "other_tool")
	joined = strings.Join(surface.ActiveBandLines(), "\n")
	if coord.activeStream.IsToolActive() {
		t.Fatal("tool stage must not replace active assistant stream cell")
	}
	if strings.Contains(strings.ToLower(joined), "other_tool") || strings.Contains(joined, "OtherTool") {
		t.Fatalf("tool stage must not replace active assistant band, got %q", joined)
	}
}

func TestChatInteractionCoordinator_LivePlainTextStreamsStableChunksWithoutSurface(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.liveStreamFn = func() bool { return true }
	coord.streamRuneDelay = 0
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAssistantDelta("First complete sentence. ")
	first := output.String()
	if !strings.Contains(first, "First complete sentence.") {
		t.Fatalf("expected first plain chunk to stream live, got %q", first)
	}

	coord.RenderAssistantDelta("Second complete sentence.")
	full := output.String()
	if !strings.Contains(full, "Second complete sentence.") {
		t.Fatalf("expected second plain chunk to append live, got %q", full)
	}
	if strings.Count(full, "First complete sentence.") != 1 {
		t.Fatalf("expected first chunk once (no duplicate), got %q", full)
	}
	if coord.surface != nil {
		t.Fatal("expected no surface for plain live-stream path")
	}
	coord.FinalizeAssistantDelta()
	if !strings.HasSuffix(output.String(), "\n") {
		t.Fatalf("expected finalize newline, got %q", output.String())
	}
}

func TestChatInteractionCoordinator_RenderAssistantDelta_RewritesMarkdownIncrementally(t *testing.T) {
	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAssistantDelta("#")
	coord.RenderAssistantDelta(" Title")

	if output.String() != "" {
		t.Fatalf("expected markdown stream to stay buffered before finalize, got %q", output.String())
	}

	coord.FinalizeAssistantDelta()
	rendered := output.String()
	if !strings.Contains(rendered, "Title") {
		t.Fatalf("expected finalized formatted markdown output, got %q", rendered)
	}
	if !strings.Contains(rendered, "▶") && !strings.Contains(rendered, "#") {
		t.Fatalf("expected heading formatting, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_RenderAssistantDelta_BuffersMarkdownLead(t *testing.T) {
	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAssistantDelta("#")
	if output.String() != "" {
		t.Fatalf("expected markdown lead to stay buffered until classification, got %q", output.String())
	}

	coord.RenderAssistantDelta(" Title")
	if output.String() != "" {
		t.Fatalf("expected markdown lead to remain buffered until finalize, got %q", output.String())
	}

	coord.FinalizeAssistantDelta()
	rendered := output.String()
	if !strings.Contains(rendered, "Title") {
		t.Fatalf("expected buffered markdown lead to finalize as formatted markdown, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_RenderAssistantDelta_UpgradesTextStreamToMarkdown(t *testing.T) {
	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAssistantDelta("下面给你一个示例：\n\n")
	coord.RenderAssistantDelta("| 字段 | 类型 |\n")
	coord.RenderAssistantDelta("| ---- | ---- |\n| id | int |")

	if output.String() != "" {
		t.Fatalf("expected mixed text+markdown stream to stay buffered before finalize, got %q", output.String())
	}

	coord.FinalizeAssistantDelta()
	rendered := output.String()
	if !strings.Contains(rendered, "字段") || !strings.Contains(rendered, "类型") {
		t.Fatalf("expected finalized markdown table output, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_RenderAssistantDelta_BuffersPartialTableUntilFinalize(t *testing.T) {
	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAssistantDelta("| 项目 | 值")
	coord.RenderAssistantDelta(" |\n| --- | --- |\n| A | 1 |\n| B | 2 |")

	if output.String() != "" {
		t.Fatalf("expected partial table stream to stay buffered before finalize, got %q", output.String())
	}

	coord.FinalizeAssistantDelta()
	rendered := output.String()
	if !strings.Contains(rendered, "项目 │ 值") || !strings.Contains(rendered, "A    │ 1") || !strings.Contains(rendered, "B    │ 2") {
		t.Fatalf("expected finalized table output, got %q", rendered)
	}
	if strings.Contains(rendered, "| 项目 | 值 |") {
		t.Fatalf("expected raw markdown table syntax to be formatted away, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_RenderAssistantDelta_DedupesSnapshotStyleChunks(t *testing.T) {
	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	first := "我是通过 API 提供服务的 AI 助手。当前这个环境里我看不到底层精确模型代号，所以不能可靠地告诉你具体是哪个版本。"
	second := first + "  \n如果你愿意，我可以继续帮你做代码、写作、检索、分析等任务。"

	coord.RenderAssistantDelta(first)
	coord.RenderAssistantDelta(second)
	coord.FinalizeAssistantDelta()

	rendered := output.String()
	if strings.Count(rendered, first) != 1 {
		t.Fatalf("expected snapshot-style prefix to render once, got %q", rendered)
	}
	if !strings.Contains(rendered, "如果你愿意，我可以继续帮你做代码、写作、检索、分析等任务。") {
		t.Fatalf("expected new suffix to remain visible, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_RenderAssistant_StripsAsyncTeamChoiceTail(t *testing.T) {
	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAssistant(`团队已经在后台运行。我可以在他们完成后为你汇总：
1.. 结构
2.. 重点

如果你愿意，我下一步就继续帮你直接输出一版人工汇总。`)

	rendered := output.String()
	if strings.Contains(rendered, "如果你愿意，我下一步就继续帮你直接输出一版人工汇总。") {
		t.Fatalf("expected async team choice tail to be removed, got %q", rendered)
	}
	if !strings.Contains(rendered, "团队已经在后台运行") {
		t.Fatalf("expected background team notice to remain, got %q", rendered)
	}
	if !strings.Contains(rendered, "完成后为你汇总") {
		t.Fatalf("expected automatic summary promise to remain, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_RenderAssistantDelta_KeepsPlainTextModeAfterBacktickNewlines(t *testing.T) {
	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	first := "流式终端渲染是分块到达的，渲染器在没有完整上下文时就要输出。"
	second := "比如同时出现 `\\r\\n` 和 `\\n`，就会引起光标位置异常。\n\n"
	third := "格式错乱通常来自状态不一致，比如 Markdown 或 ANSI 控制序列被拆开。"

	coord.RenderAssistantDelta(first)
	coord.RenderAssistantDelta(second)
	coord.RenderAssistantDelta(third)
	coord.FinalizeAssistantDelta()

	rendered := output.String()
	if strings.Count(rendered, first) != 1 {
		t.Fatalf("expected first plain-text paragraph once, got %q", rendered)
	}
	if strings.Count(rendered, third) != 1 {
		t.Fatalf("expected trailing plain-text paragraph once, got %q", rendered)
	}
	// Inline backticks make IsMarkdown true, so final output may be markdown-rendered
	// rather than Theme.ColorizeAssistant of the raw concatenation.
	if !strings.Contains(rendered, first) || !strings.Contains(rendered, third) {
		t.Fatalf("expected buffered content retained after finalize, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_EstimateStreamFlushTimeoutScalesWithContent(t *testing.T) {
	coord := newChatInteractionCoordinator(&ChatSession{})
	coord.streamRuneDelay = 2 * time.Millisecond

	shortTimeout := coord.EstimateStreamFlushTimeout("short")
	longTimeout := coord.EstimateStreamFlushTimeout(strings.Repeat("长", 400))

	if longTimeout <= shortTimeout {
		t.Fatalf("expected longer content timeout to grow, got short=%v long=%v", shortTimeout, longTimeout)
	}
	if longTimeout > 10*time.Second {
		t.Fatalf("expected timeout to stay capped, got %v", longTimeout)
	}
}

func TestChatInteractionCoordinator_CompleteAssistantResponse_AppendsMissingPlainTextSuffix(t *testing.T) {
	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAssistantDelta("第一段。")
	if !coord.CompleteAssistantResponse("第一段。\n\n第二段。") {
		t.Fatal("expected stream completion to succeed")
	}

	rendered := output.String()
	if strings.Count(rendered, "第一段。") != 1 {
		t.Fatalf("expected first paragraph once, got %q", rendered)
	}
	if !strings.Contains(rendered, "第二段。") {
		t.Fatalf("expected missing suffix to be appended, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_RenderAssistantDelta_BuffersShortPlainTextUntilFinalize(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAssistantDelta("Hello")
	if output.String() != "" {
		t.Fatalf("expected short plain text to stay buffered before activation, got %q", output.String())
	}

	coord.FinalizeAssistantDelta()
	if !strings.Contains(output.String(), "Hello") {
		t.Fatalf("expected finalized plain text to be written, got %q", output.String())
	}
}

func TestChatInteractionCoordinator_RenderAssistantDelta_StreamsImmediatelyWhenLiveOutputEnabled(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.liveStreamFn = func() bool { return true }
	coord.streamRuneDelay = 0
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAssistantDelta("Hello")

	rendered := output.String()
	if !strings.Contains(rendered, ui.AssistantContentIndent()+"Hello") {
		t.Fatalf("expected live stream output immediately, got %q", rendered)
	}
	if strings.Contains(rendered, "\n") {
		t.Fatalf("expected live stream to avoid premature newline, got %q", rendered)
	}

	coord.FinalizeAssistantDelta()
	if !strings.HasSuffix(output.String(), "\n") {
		t.Fatalf("expected live stream finalize to append newline, got %q", output.String())
	}
}

func TestChatInteractionCoordinator_CompleteAssistantResponse_BuffersMarkdownWhenLiveOutputEnabled(t *testing.T) {
	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	coord := newChatInteractionCoordinator(session)
	coord.liveStreamFn = func() bool { return true }
	coord.streamRuneDelay = 0
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAssistantDelta("- 第一项\n")
	coord.RenderAssistantDelta("- 第二项")

	if output.String() != "" {
		t.Fatalf("expected markdown stream to stay buffered before completion, got %q", output.String())
	}

	if !coord.CompleteAssistantResponse("- 第一项\n- 第二项") {
		t.Fatal("expected markdown stream completion to succeed")
	}

	rendered := output.String()
	if !strings.Contains(rendered, "• 第一项") || !strings.Contains(rendered, "• 第二项") {
		t.Fatalf("expected finalized markdown output, got %q", rendered)
	}
	if strings.Contains(rendered, "- 第一项") || strings.Contains(rendered, "- 第二项") {
		t.Fatalf("expected raw markdown list syntax to be formatted away, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_CompleteAssistantResponse_UpgradesLiveIntroToMarkdown(t *testing.T) {
	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	coord := newChatInteractionCoordinator(session)
	coord.liveStreamFn = func() bool { return true }
	coord.streamRuneDelay = 0
	var output bytes.Buffer
	coord.SetWriter(&output)

	intro := "下面是列表：\n\n"
	markdown := "- 第一项\n- 第二项"
	coord.RenderAssistantDelta(intro)

	if !strings.Contains(output.String(), "下面是列表：") {
		t.Fatalf("expected plain intro to stream immediately, got %q", output.String())
	}

	coord.RenderAssistantDelta(markdown)
	if strings.Contains(output.String(), "- 第一项") || strings.Contains(output.String(), "- 第二项") {
		t.Fatalf("expected markdown suffix to stay buffered after upgrade, got %q", output.String())
	}

	if !coord.CompleteAssistantResponse(intro + markdown) {
		t.Fatal("expected upgraded markdown stream completion to succeed")
	}

	rendered := output.String()
	if strings.Count(rendered, "下面是列表：") != 1 {
		t.Fatalf("expected intro to render once, got %q", rendered)
	}
	if !strings.Contains(rendered, "• 第一项") || !strings.Contains(rendered, "• 第二项") {
		t.Fatalf("expected markdown suffix to be formatted, got %q", rendered)
	}
	if strings.Contains(rendered, "- 第一项") || strings.Contains(rendered, "- 第二项") {
		t.Fatalf("expected raw markdown suffix to be formatted away, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_CompleteAssistantResponse_FormatsLiveInlineMarkdownSuffix(t *testing.T) {
	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	coord := newChatInteractionCoordinator(session)
	coord.liveStreamFn = func() bool { return true }
	coord.streamRuneDelay = 0
	var output bytes.Buffer
	coord.SetWriter(&output)

	prefix := "This is "
	suffix := "**bold**"
	coord.RenderAssistantDelta(prefix)
	coord.RenderAssistantDelta(suffix)

	if strings.Contains(output.String(), "**bold**") {
		t.Fatalf("expected inline markdown suffix to stay buffered, got %q", output.String())
	}

	if !coord.CompleteAssistantResponse(prefix + suffix) {
		t.Fatal("expected inline markdown stream completion to succeed")
	}

	rendered := output.String()
	if !strings.Contains(rendered, "This is bold") {
		t.Fatalf("expected inline markdown suffix to continue the streamed line, got %q", rendered)
	}
	if strings.Contains(rendered, "**bold**") {
		t.Fatalf("expected raw inline markdown syntax to be formatted away, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_FinalizeReasoningDelta_FormatsBufferedMarkdownAssistantStream(t *testing.T) {
	session := &ChatSession{
		Formatter: formatter.NewMarkdownFormatter(false),
	}
	coord := newChatInteractionCoordinator(session)
	coord.liveStreamFn = func() bool { return true }
	coord.streamRuneDelay = 0
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.reasoningActive = true
	coord.RenderAssistantDelta("- 第一项\n")
	coord.RenderAssistantDelta("- 第二项")

	if output.String() != "" {
		t.Fatalf("expected assistant stream to stay buffered during reasoning, got %q", output.String())
	}

	coord.FinalizeReasoningDelta()

	if strings.Contains(output.String(), "• 第一项") || strings.Contains(output.String(), "• 第二项") {
		t.Fatalf("expected assistant markdown to remain buffered after reasoning finalization, got %q", output.String())
	}
	if !strings.Contains(output.String(), chatToolDivider("end reasoning")) {
		t.Fatalf("expected reasoning divider to be rendered, got %q", output.String())
	}

	if !coord.CompleteAssistantResponse("- 第一项\n- 第二项") {
		t.Fatal("expected markdown stream completion after reasoning to succeed")
	}

	rendered := output.String()
	if !strings.Contains(rendered, "• 第一项") || !strings.Contains(rendered, "• 第二项") {
		t.Fatalf("expected finalized markdown output after completion, got %q", rendered)
	}
	if strings.Contains(rendered, "- 第一项") || strings.Contains(rendered, "- 第二项") {
		t.Fatalf("expected raw markdown list syntax to be formatted away after completion, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_RenderAssistantDelta_PreservesLeadingWhitespaceBetweenChunks(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.liveStreamFn = func() bool { return true }
	coord.streamRuneDelay = 0
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAssistantDelta("Hello")
	coord.RenderAssistantDelta(" world.")
	coord.RenderAssistantDelta(" Next")

	rendered := output.String()
	if !strings.Contains(rendered, ui.AssistantContentIndent()+"Hello world. Next") {
		t.Fatalf("expected streamed assistant text to preserve leading whitespace, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_RenderAssistantDelta_IsolatesRTLTextInLiveStream(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.liveStreamFn = func() bool { return true }
	coord.streamRuneDelay = 0
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderAssistantDelta("这些改动 هنوز在工作区里，尚未提交")

	rendered := output.String()
	if !strings.Contains(rendered, "\u2066هنوز\u2069") {
		t.Fatalf("expected RTL run to be isolated in live stream, got %q", rendered)
	}
	if !strings.Contains(rendered, "这些改动") || !strings.Contains(rendered, "尚未提交") {
		t.Fatalf("expected surrounding CJK text to remain visible, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_RenderReasoningDelta_StreamsImmediatelyWhenLiveOutputEnabled(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.liveStreamFn = func() bool { return true }
	coord.streamRuneDelay = 0
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderReasoningDelta(&runtimetypes.ReasoningBlock{
		Provider:   "nvidia",
		Format:     "openai_compatible",
		Summary:    "先输出 reasoning，再输出正文。",
		Streamable: true,
		Visibility: runtimetypes.ReasoningVisibilitySummary,
	})

	rendered := output.String()
	if !strings.Contains(rendered, chatToolDivider("reasoning")) {
		t.Fatalf("expected reasoning divider, got %q", rendered)
	}
	if strings.Contains(rendered, "[reasoning]") {
		t.Fatalf("expected default reasoning metadata line to be suppressed, got %q", rendered)
	}
	if !strings.Contains(rendered, ui.AssistantContentIndent()+"  先输出 reasoning，再输出正文。") {
		t.Fatalf("expected reasoning content to stream immediately, got %q", rendered)
	}

	coord.FinalizeReasoningDelta()
	if !strings.Contains(output.String(), chatToolDivider("end reasoning")) {
		t.Fatalf("expected reasoning finalize divider, got %q", output.String())
	}
}

func TestChatInteractionCoordinator_CompleteReasoningResponse_SuppressesMetadataOnlyBlock(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.reasoningActive = true

	if !coord.CompleteReasoningResponse(&runtimetypes.ReasoningBlock{
		Provider: "CODEX_LOCAL",
		Format:   "openai_responses",
	}) {
		t.Fatal("expected reasoning completion to be handled")
	}

	if output.String() != "" {
		t.Fatalf("expected metadata-only reasoning block to be suppressed, got %q", output.String())
	}
}

func TestChatInteractionCoordinator_RenderReasoningDelta_PreservesLeadingWhitespaceBetweenChunks(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.liveStreamFn = func() bool { return true }
	coord.streamRuneDelay = 0
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderReasoningDelta(&runtimetypes.ReasoningBlock{
		Provider:   "nvidia",
		Format:     "stream_delta",
		Summary:    "The",
		Streamable: true,
		Visibility: runtimetypes.ReasoningVisibilitySummary,
	})
	coord.RenderReasoningDelta(&runtimetypes.ReasoningBlock{
		Provider:   "nvidia",
		Format:     "stream_delta",
		Summary:    " user",
		Streamable: true,
		Visibility: runtimetypes.ReasoningVisibilitySummary,
	})

	rendered := output.String()
	if !strings.Contains(rendered, ui.AssistantContentIndent()+"  The user") {
		t.Fatalf("expected streamed reasoning to preserve leading whitespace, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_DefersAssistantTextUntilReasoningCompletes(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.liveStreamFn = func() bool { return true }
	coord.streamRuneDelay = 0
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderReasoningDelta(&runtimetypes.ReasoningBlock{
		Provider:   "nvidia",
		Format:     "stream_delta",
		Summary:    "先确认问题。",
		Streamable: true,
		Visibility: runtimetypes.ReasoningVisibilitySummary,
	})
	coord.RenderAssistantDelta("Hello")
	coord.RenderReasoningDelta(&runtimetypes.ReasoningBlock{
		Provider:   "nvidia",
		Format:     "stream_delta",
		Summary:    " 即可。",
		Streamable: true,
		Visibility: runtimetypes.ReasoningVisibilitySummary,
	})

	rendered := output.String()
	if strings.Contains(rendered, ui.AssistantContentIndent()+"Hello") {
		t.Fatalf("expected assistant text to stay buffered while reasoning is active, got %q", rendered)
	}
	if strings.Count(rendered, chatToolDivider("reasoning")) != 1 {
		t.Fatalf("expected a single reasoning block before finalize, got %q", rendered)
	}

	coord.FinalizeReasoningDelta()

	rendered = output.String()
	if strings.Count(rendered, chatToolDivider("reasoning")) != 1 {
		t.Fatalf("expected reasoning output to remain a single block, got %q", rendered)
	}
	if !strings.Contains(rendered, ui.AssistantContentIndent()+"  先确认问题。 即可。") {
		t.Fatalf("expected reasoning chunks to stay contiguous, got %q", rendered)
	}
	if !strings.Contains(rendered, chatToolDivider("end reasoning")+"\n"+ui.AssistantContentIndent()+"Hello") {
		t.Fatalf("expected buffered assistant text after reasoning block, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_DoesNotRedrawPromptDuringStreaming(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.promptDelay = 10 * time.Millisecond
	output := &synchronizedBuffer{}
	coord.SetWriter(output)

	coord.RenderAssistantDelta("Hello")
	coord.SchedulePromptRedraw()

	require.Never(t, func() bool {
		return strings.Contains(output.String(), ui.UserPromptText(0))
	}, 80*time.Millisecond, 10*time.Millisecond)

	rendered := output.String()
	if strings.Contains(rendered, ui.UserPromptText(0)) {
		t.Fatalf("expected no prompt redraw during active stream, got %q", rendered)
	}

	coord.FinalizeAssistantDelta()
	coord.SchedulePromptRedraw()

	require.Eventually(t, func() bool {
		return strings.Count(output.String(), ui.UserPromptText(0)) == 1
	}, 200*time.Millisecond, 10*time.Millisecond)

	rendered = output.String()
	if strings.Count(rendered, ui.UserPromptText(0)) != 1 {
		t.Fatalf("expected one prompt redraw after stream finalization, got %q", rendered)
	}
}

func TestChatInteractionCoordinator_SchedulePromptRedraw_InsertsBlankLineAfterCompletedBlock(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.promptDelay = 10 * time.Millisecond
	output := &synchronizedBuffer{}
	coord.SetWriter(output)

	coord.RenderAssistant("第一轮回复")
	coord.SchedulePromptRedraw()

	require.Eventually(t, func() bool {
		rendered := output.String()
		return strings.Contains(rendered, "第一轮回复") && strings.Contains(rendered, "\n\n"+ui.UserPromptText(0))
	}, 200*time.Millisecond, 10*time.Millisecond)
}

type terminalCaptureWriter struct {
	rows     []string
	row      int
	col      int
	savedRow int
	savedCol int
	hasSaved bool
}

func (w *terminalCaptureWriter) Write(p []byte) (int, error) {
	for i := 0; i < len(p); {
		if p[i] == '\x1b' {
			if consumed := w.consumeEscape(p[i:]); consumed > 0 {
				i += consumed
				continue
			}
		}
		r, size := utf8.DecodeRune(p[i:])
		switch r {
		case '\r':
			w.col = 0
		case '\n':
			w.row++
			w.col = 0
		default:
			w.writeRune(r)
		}
		i += size
	}
	return len(p), nil
}

func (w *terminalCaptureWriter) String() string {
	rows := append([]string(nil), w.rows...)
	for len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	for i, row := range rows {
		rows[i] = strings.TrimRight(row, " ")
	}
	return strings.Join(rows, "\n")
}

func (w *terminalCaptureWriter) consumeEscape(p []byte) int {
	if len(p) < 2 {
		return 0
	}
	if p[1] != '[' {
		switch p[1] {
		case '7':
			w.savedRow = w.row
			w.savedCol = w.col
			w.hasSaved = true
			return 2
		case '8':
			if w.hasSaved {
				w.row = w.savedRow
				w.col = w.savedCol
			}
			return 2
		default:
			return 1
		}
	}

	j := 2
	for j < len(p) {
		b := p[j]
		if (b >= '0' && b <= '9') || b == ';' || b == '?' {
			j++
			continue
		}
		break
	}
	if j >= len(p) {
		return 0
	}

	final := p[j]
	param := 0
	if j > 2 {
		if parsed, err := strconv.Atoi(strings.TrimLeft(strings.TrimRight(string(p[2:j]), ";?"), "?")); err == nil {
			param = parsed
		}
	}
	if param <= 0 {
		param = 1
	}

	switch final {
	case 'A':
		w.row -= param
		if w.row < 0 {
			w.row = 0
		}
	case 'B':
		w.row += param
	case 'C':
		w.col += param
	case 'D':
		w.col -= param
		if w.col < 0 {
			w.col = 0
		}
	case 'K':
		w.clearLineFromCursor()
	case 'J':
		w.clearScreenFromCursor()
	case 's':
		w.savedRow = w.row
		w.savedCol = w.col
		w.hasSaved = true
	case 'u':
		if w.hasSaved {
			w.row = w.savedRow
			w.col = w.savedCol
		}
	}

	return j + 1
}

func (w *terminalCaptureWriter) ensureRow(row int) {
	for len(w.rows) <= row {
		w.rows = append(w.rows, "")
	}
}

func (w *terminalCaptureWriter) writeRune(r rune) {
	if w.row < 0 {
		w.row = 0
	}
	if w.col < 0 {
		w.col = 0
	}
	w.ensureRow(w.row)
	current := []rune(w.rows[w.row])
	for len(current) < w.col {
		current = append(current, ' ')
	}
	if w.col < len(current) {
		current[w.col] = r
	} else {
		current = append(current, r)
	}
	w.rows[w.row] = string(current)
	width := ui.DisplayWidth(string(r))
	if width <= 0 {
		width = 1
	}
	w.col += width
	termWidth := ui.GetTerminalWidth()
	if termWidth <= 0 {
		termWidth = 80
	}
	if w.col >= termWidth {
		w.row++
		w.col = 0
	}
}

func (w *terminalCaptureWriter) clearLineFromCursor() {
	w.ensureRow(w.row)
	current := []rune(w.rows[w.row])
	if w.col < len(current) {
		current = current[:w.col]
	}
	w.rows[w.row] = string(current)
}

func (w *terminalCaptureWriter) clearScreenFromCursor() {
	w.clearLineFromCursor()
	if w.row+1 < len(w.rows) {
		w.rows = w.rows[:w.row+1]
	}
}
