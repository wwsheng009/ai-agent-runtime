package commands

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/fatih/color"
	"github.com/stretchr/testify/require"
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
	if !strings.Contains(rendered, "你> ") {
		t.Fatalf("expected prompt in output, got %q", rendered)
	}
	if !strings.Contains(rendered, ui.IndentAssistantContent("[task] started task-1 @planner")) {
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
	if strings.Contains(rendered, "你> ") {
		t.Fatalf("expected prompt to be cleared before async line, got %q", rendered)
	}
	if !strings.Contains(rendered, strings.TrimRight(ui.IndentAssistantContent("[tool] view"), " ")) {
		t.Fatalf("expected async line in output, got %q", rendered)
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
	if !strings.Contains(output.String(), "你> ") {
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
	if !strings.Contains(output.String(), "你> ") {
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
	if !strings.Contains(output.String(), "你> ") {
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
	oldShouldDiscard := shouldDiscardPendingInput
	oldNewReader := newChatInputReader
	defer func() {
		discardPendingConsoleInput = oldDiscard
		pendingConsoleInputCount = oldPendingCount
		shouldDiscardPendingInput = oldShouldDiscard
		newChatInputReader = oldNewReader
	}()
	discardPendingConsoleInput = func() (int, error) { return 2, nil }
	pendingConsoleInputCount = func() (int, error) { return 2, nil }
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
	if strings.Contains(rendered, "你> ") {
		t.Fatalf("expected no prompt before queued lines drain, got %q", rendered)
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
	if !strings.Contains(rendered, ui.IndentAssistantContent("助手正在思考...")) {
		t.Fatalf("expected thinking text in output, got %q", rendered)
	}
	if !strings.Contains(rendered, "done") {
		t.Fatalf("expected assistant response in output, got %q", rendered)
	}
	if !strings.Contains(rendered, "\r   \r") {
		t.Fatalf("expected thinking clear sequence before assistant response, got %q", rendered)
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
	if !strings.Contains(rendered, "你> \n") {
		t.Fatalf("expected buffered prompt to advance to next line, got %q", rendered)
	}
	if !strings.Contains(rendered, "\n"+ui.IndentAssistantContent("助手正在思考...")) {
		t.Fatalf("expected thinking line after prompt advance, got %q", rendered)
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
		return strings.Count(output.String(), "你> ") == 1
	}, 200*time.Millisecond, 10*time.Millisecond)

	rendered := output.String()
	if strings.Count(rendered, "你> ") != 1 {
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

func TestChatInteractionCoordinator_DoesNotRedrawPromptDuringStreaming(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.promptDelay = 10 * time.Millisecond
	output := &synchronizedBuffer{}
	coord.SetWriter(output)

	coord.RenderAssistantDelta("Hello")
	coord.SchedulePromptRedraw()

	require.Never(t, func() bool {
		return strings.Contains(output.String(), "你> ")
	}, 80*time.Millisecond, 10*time.Millisecond)

	rendered := output.String()
	if strings.Contains(rendered, "你> ") {
		t.Fatalf("expected no prompt redraw during active stream, got %q", rendered)
	}

	coord.FinalizeAssistantDelta()
	coord.SchedulePromptRedraw()

	require.Eventually(t, func() bool {
		return strings.Count(output.String(), "你> ") == 1
	}, 200*time.Millisecond, 10*time.Millisecond)

	rendered = output.String()
	if strings.Count(rendered, "你> ") != 1 {
		t.Fatalf("expected one prompt redraw after stream finalization, got %q", rendered)
	}
}

type terminalCaptureWriter struct {
	lines  []string
	line   []rune
	cursor int
}

func (w *terminalCaptureWriter) Write(p []byte) (int, error) {
	for _, r := range string(p) {
		switch r {
		case '\r':
			w.cursor = 0
		case '\n':
			w.lines = append(w.lines, strings.TrimRight(string(w.line), " "))
			w.line = nil
			w.cursor = 0
		default:
			for len(w.line) < w.cursor {
				w.line = append(w.line, ' ')
			}
			if w.cursor < len(w.line) {
				w.line[w.cursor] = r
			} else {
				w.line = append(w.line, r)
			}
			w.cursor++
		}
	}
	return len(p), nil
}

func (w *terminalCaptureWriter) String() string {
	lines := append([]string(nil), w.lines...)
	if len(w.line) > 0 {
		lines = append(lines, strings.TrimRight(string(w.line), " "))
	}
	return strings.Join(lines, "\n")
}
