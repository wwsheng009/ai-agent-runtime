package commands

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/fatih/color"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
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
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
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
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
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

func TestChatInteractionCoordinator_ClearPromptClearsWrappedInput(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
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
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
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

func TestNotifyChatInputDraftState_IsSilent(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()
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

func TestBuildChatSurfaceStatusLine_IncludesReasoningEffortContextAndCurrentDirectory(t *testing.T) {
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

	session := &ChatSession{
		Model:             "gpt-5.4-code",
		ReasoningEffort:   "HIGH",
		ProviderName:      "openai",
		MsgCount:          17,
		TokenCount:        28640,
		ContextTokenCount: 28640,
		Provider: config.Provider{
			ModelCapabilities: map[string]config.ModelCapabilitySpec{
				"gpt-5.4-code": {
					MaxContextTokens:      128000,
					AutoCompactTokenLimit: 96000,
				},
			},
		},
	}

	status := buildChatSurfaceStatusLine(session, "Thinking")

	for _, want := range []string{
		"Thinking",
		"model gpt-5.4-code",
		"reasoning_effort " + runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort),
		"ctx 128000 used 28640 22%",
		"openai",
		"msgs 17",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("expected status line to contain %q, got %q", want, status)
		}
	}
	if strings.Contains(status, "tokens ") {
		t.Fatalf("expected status line to omit duplicate tokens field, got %q", status)
	}
	if strings.Contains(status, "4096") {
		t.Fatalf("expected ctx used to avoid small output-limit fallback, got %q", status)
	}
	wantCwd := filepath.Clean(nested)
	if !strings.Contains(status, "cwd "+wantCwd) {
		t.Fatalf("expected cwd to keep full absolute path %q, got %q", wantCwd, status)
	}
	if strings.Contains(status, "...") {
		t.Fatalf("expected cwd to avoid ellipsis shortening, got %q", status)
	}
}

func TestBuildChatSurfaceStatusLine_IncludesContextWindowWhenOnlyWindowIsKnown(t *testing.T) {
	session := &ChatSession{
		ContextWindowTokenCount: 128000,
	}

	status := buildChatSurfaceStatusLine(session, "Ready")
	if !strings.Contains(status, "ctx 128000 used 0 0%") {
		t.Fatalf("expected status line to include context window summary, got %q", status)
	}
	if strings.Contains(status, "Thinking") {
		t.Fatalf("expected status line to keep supplied state only, got %q", status)
	}
}

func TestBuildChatSurfaceStatusLine_UsesLiveStatusMessageCount(t *testing.T) {
	session := &ChatSession{
		MsgCount:           2,
		StatusMessageCount: 37,
	}

	status := buildChatSurfaceStatusLine(session, "Ready")
	if !strings.Contains(status, "msgs 37") {
		t.Fatalf("expected status line to use live context message count, got %q", status)
	}
	if strings.Contains(status, "msgs 2") {
		t.Fatalf("expected status line not to fall back to turn count when live count exists, got %q", status)
	}
}

func TestBuildChatPromptNoticeLine_IncludesQueuedInputState(t *testing.T) {
	queue := newChatInputQueue(nil)
	queue.routeLine(chatQueuedInput{Text: "queued\n", Source: "stdin"})
	session := &ChatSession{
		InputQueue:       queue,
		queuedInputDrain: true,
	}

	notice := buildChatPromptNoticeLine(session)
	if !strings.Contains(notice, "• Message to be submitted after next tool call (press esc to interrupt and send immediately)") {
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

	notice := buildChatPromptNoticeLine(session)
	if !strings.Contains(notice, "• Message to be submitted after next tool call (press esc to interrupt and send immediately)") {
		t.Fatalf("expected prompt notice to include ready submission state, got %q", notice)
	}
	if !strings.Contains(notice, "  - confirmed draft") {
		t.Fatalf("expected prompt notice to include ready submission preview, got %q", notice)
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
	status := buildChatSurfaceStatusLine(session, "Ready")
	if !strings.Contains(status, "goal paused") {
		t.Fatalf("expected status line to include goal status, got %q", status)
	}
}

func TestBuildChatSurfaceStatusLine_OmitsGoalStatusWhenUnset(t *testing.T) {
	status := buildChatSurfaceStatusLine(&ChatSession{RuntimeSession: runtimechat.NewSession("test-user")}, "Ready")
	if strings.Contains(status, "goal ") {
		t.Fatalf("expected status line to omit missing goal, got %q", status)
	}
}

func TestBuildChatSurfaceStatusLine_FallsBackToDefaultContextWindowWhenNoCapabilityExists(t *testing.T) {
	session := &ChatSession{
		TokenCount:        500000,
		ContextTokenCount: 28640,
	}

	status := buildChatSurfaceStatusLine(session, "Ready")
	if !strings.Contains(status, "ctx 256000 used 28640 11%") {
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
	session := &ChatSession{
		Model:        "gpt-5.4-code",
		ProviderName: "openai",
		Messages:     messages,
		Provider: config.Provider{
			ModelCapabilities: map[string]config.ModelCapabilitySpec{
				"gpt-5.4-code": {
					MaxContextTokens: 128000,
				},
			},
		},
	}

	status := buildChatSurfaceStatusLine(session, "Ready")
	wantUsed := countChatContextTokensForMessages(session, messages)
	if !strings.Contains(status, fmt.Sprintf("ctx 128000 used %d", wantUsed)) {
		t.Fatalf("expected status line to fall back to history context estimate %d, got %q", wantUsed, status)
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
	if !strings.Contains(rendered, "# Title") {
		t.Fatalf("expected streamed markdown content in output, got %q", rendered)
	}
	if !strings.Contains(rendered, ui.FormatAssistantMessage("# Title")) {
		t.Fatalf("expected formatted assistant message after rewrite, got %q", rendered)
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
	if !strings.Contains(rendered, "列名1 │ 列名2 │ 列名3") {
		t.Fatalf("expected assistant renderer to format indented table, got %q", rendered)
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
	if !strings.Contains(rendered, "列名1 │ 列名2 │ 列名3") {
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
		"│ 这是一条引用",
		"• 苹果",
		"• 香蕉",
		"• 樱桃",
		"名称 │ 值",
		"package main",
		"fmt.Println(\"hello\")",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected mixed markdown render to contain %q, got %q", expected, rendered)
		}
	}
	if strings.Contains(rendered, "```go") {
		t.Fatalf("expected fenced go block to be rendered as code content, got %q", rendered)
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
	if !strings.Contains(rendered, ui.FormatAssistantMessage("# Title")) {
		t.Fatalf("expected finalized formatted markdown output, got %q", rendered)
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
	if !strings.Contains(rendered, ui.FormatAssistantMessage("# Title")) {
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
	if !strings.Contains(rendered, ui.FormatAssistantMessage(first+"  \n如果你愿意，我可以继续帮你做代码、写作、检索、分析等任务。")) {
		t.Fatalf("expected buffered stream to finalize as one formatted message, got %q", rendered)
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
	if !strings.Contains(rendered, ui.FormatAssistantMessage(first+second+third)) {
		t.Fatalf("expected buffered plain text to finalize as one formatted message, got %q", rendered)
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
	if !strings.Contains(rendered, ui.FormatAssistantMessage("第一段。\n\n第二段。")) {
		t.Fatalf("expected stream completion to render one formatted final response, got %q", rendered)
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
