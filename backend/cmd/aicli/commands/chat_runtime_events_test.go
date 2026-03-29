package commands

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/stretchr/testify/require"
)

func TestChatRuntimeEvents_RenderPlanningAndSubagentTimeline(t *testing.T) {
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: runtimechat.EventLLMRequestStarted, TraceID: "trace-1", Payload: map[string]interface{}{"model": "gpt-5.4"}}); got != "[thinking] contacting model=gpt-5.4" {
		t.Fatalf("unexpected llm started render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "llm.request.started", TraceID: "trace-1", Payload: map[string]interface{}{"model": "gpt-5.4"}}); got != "[thinking] contacting model=gpt-5.4" {
		t.Fatalf("unexpected dotted llm started render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: runtimechat.EventLLMRequestFinished, TraceID: "trace-1", Payload: map[string]interface{}{"success": true}}); got != "[thinking] model responded" {
		t.Fatalf("unexpected llm finished render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "llm.request.finished", TraceID: "trace-1", Payload: map[string]interface{}{"success": true}}); got != "[thinking] model responded" {
		t.Fatalf("unexpected dotted llm finished render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "planning.started"}); got != "[planning] started" {
		t.Fatalf("unexpected planning render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "subagent.completed", Payload: map[string]interface{}{"agent_id": "writer"}}); got != "[subagent] completed writer" {
		t.Fatalf("unexpected subagent render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "task.started", Payload: map[string]interface{}{"task_id": "task-1", "assignee": "planner"}}); got != "[task] started task-1 @planner" {
		t.Fatalf("unexpected task render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: runtimechat.EventMailboxReceived, Payload: map[string]interface{}{"team_id": "team-1", "message_id": "msg-1", "from_agent": "planner", "to_agent": "lead", "kind": "progress", "task_id": "task-1", "body": "Started task: Draft"}}); got != "[progress] planner -> lead task-1 Started task: Draft" {
		t.Fatalf("unexpected mailbox render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "team.completed", Payload: map[string]interface{}{"team_id": "team-1", "status": "done"}}); got != "[team] completed team-1 status=done" {
		t.Fatalf("unexpected team completion render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "team.summary", Payload: map[string]interface{}{"team_id": "team-1", "summary": "auto lead summary"}}); got != "[team summary] team-1 auto lead summary" {
		t.Fatalf("unexpected team summary render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: chatEventInputQueueDetected, Payload: map[string]interface{}{"queued_input_count": 2, "source": "stdin"}}); got != "[input] queued 2 line(s) from stdin" {
		t.Fatalf("unexpected input queue detected render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: chatEventInputQueueDrained, Payload: map[string]interface{}{}}); got != "[input] queued input drained" {
		t.Fatalf("unexpected input queue drained render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: chatEventInputQueueDiscarded, Payload: map[string]interface{}{"discarded_count": 1, "prompt_kind": "审批提示"}}); got != "[input] discarded 1 queued line(s) before 审批提示" {
		t.Fatalf("unexpected input queue discarded render: %q", got)
	}
}

func TestChatRuntimeEvents_DedupesStableTimelineEventsPerRun(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}

	bridge.BeginRun()
	event := runtimeevents.Event{
		Type:    "team.summary",
		Payload: map[string]interface{}{"team_id": "team-1", "summary": "auto lead summary"},
	}
	bridge.handleEvent(event)
	bridge.handleEvent(event)

	if len(rendered) != 1 {
		t.Fatalf("expected one rendered line after dedupe, got %d (%v)", len(rendered), rendered)
	}
}

func TestChatRuntimeEvents_RendersAsyncAssistantSummaryAfterTeamCompletion(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
		ActiveTeam:     &chatTeamBinding{TeamID: "team-1", AgentID: "lead"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}
	bridge.renderResponse = func(response string) {
		rendered = append(rendered, response)
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      "team.completed",
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"team_id": "team-1", "status": "done"},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"content": "Completed all work."},
	})

	if !containsAllChatTimelineLines(rendered, "[team] completed team-1 status=done", "[team summary] team-1 Completed all work.", "Completed all work.") {
		t.Fatalf("expected async summary fallback render, got %v", rendered)
	}
}

func TestChatRuntimeEvents_RendersAsyncAssistantSummaryWhenTeamAlreadyTerminalInStore(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:     "team-1",
		Status: team.TeamStatusDone,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if teamID == "" {
		t.Fatal("expected team id")
	}

	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "lead-session"},
		ActiveTeam:       &chatTeamBinding{TeamID: "team-1", AgentID: "lead"},
		LocalRuntimeHost: &localChatRuntimeHost{TeamStore: store},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}
	bridge.renderResponse = func(response string) {
		rendered = append(rendered, response)
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"content": "Completed all work from persisted terminal state."},
	})

	if !containsAllChatTimelineLines(rendered, "[team summary] team-1 Completed all work from persisted terminal state.", "Completed all work from persisted terminal state.") {
		t.Fatalf("expected async summary fallback render from terminal team store, got %v", rendered)
	}
}

func TestChatRuntimeEvents_RendersAsyncAssistantSummaryAfterPrimaryAssistantAlreadyRendered(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
		ActiveTeam:     &chatTeamBinding{TeamID: "team-1", AgentID: "lead"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}
	bridge.renderResponse = func(response string) {
		rendered = append(rendered, response)
	}

	bridge.BeginRun()
	bridge.MarkAssistantFinalRendered()
	bridge.handleEvent(runtimeevents.Event{
		Type:      "team.completed",
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"team_id": "team-1", "status": "done"},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"content": "Completed all work after the initial reply."},
	})

	if !containsAllChatTimelineLines(rendered,
		"[team] completed team-1 status=done",
		"[team summary] team-1 Completed all work after the initial reply.",
		"Completed all work after the initial reply.",
	) {
		t.Fatalf("expected async assistant summary to render after primary final message, got %v", rendered)
	}
}

func TestChatRuntimeEvents_RedrawsPromptAfterAsyncRenderWhenSessionIdle(t *testing.T) {
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "lead-session",
		Status:    runtimechat.SessionIdle,
	}))

	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "lead-session"},
		ActiveTeam:       &chatTeamBinding{TeamID: "team-1", AgentID: "lead"},
		LocalRuntimeHost: &localChatRuntimeHost{RuntimeStore: runtimeStore, TeamStore: nil},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}
	bridge.writePrompt = func() {
		rendered = append(rendered, "PROMPT")
	}

	bridge.BeginRun()
	bridge.EndRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      "team.completed",
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"team_id": "team-1", "status": "done"},
	})

	if !containsAllChatTimelineLines(rendered, "[team] completed team-1 status=done", "PROMPT") {
		t.Fatalf("expected prompt redraw after async event, got %v", rendered)
	}
}

func TestChatRuntimeEvents_DoesNotRedrawPromptWhileRunActive(t *testing.T) {
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "lead-session",
		Status:    runtimechat.SessionIdle,
	}))

	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "lead-session"},
		ActiveTeam:       &chatTeamBinding{TeamID: "team-1", AgentID: "lead"},
		LocalRuntimeHost: &localChatRuntimeHost{RuntimeStore: runtimeStore, TeamStore: nil},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}
	bridge.writePrompt = func() {
		rendered = append(rendered, "PROMPT")
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      "team.completed",
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"team_id": "team-1", "status": "done"},
	})

	if containsAllChatTimelineLines(rendered, "PROMPT") {
		t.Fatalf("expected no prompt redraw while run is active, got %v", rendered)
	}
	if !containsAllChatTimelineLines(rendered, "[team] completed team-1 status=done") {
		t.Fatalf("expected async event to still render, got %v", rendered)
	}
}

func TestChatRuntimeEvents_DoesNotRedrawPromptWhileTeamStillActiveAfterRun(t *testing.T) {
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "lead-session",
		Status:    runtimechat.SessionIdle,
	}))

	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()
	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:     "team-1",
		Status: team.TeamStatusActive,
	})
	require.NoError(t, err)

	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "lead-session"},
		ActiveTeam:       &chatTeamBinding{TeamID: teamID, AgentID: "lead"},
		LocalRuntimeHost: &localChatRuntimeHost{RuntimeStore: runtimeStore, TeamStore: store},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writePrompt = func() {
		rendered = append(rendered, "PROMPT")
	}

	bridge.BeginRun()
	bridge.EndRun()
	bridge.writePromptIfIdle()
	if containsAllChatTimelineLines(rendered, "PROMPT") {
		t.Fatalf("expected no prompt while team remains active, got %v", rendered)
	}

	require.NoError(t, store.UpdateTeamStatus(context.Background(), teamID, team.TeamStatusDone))
	bridge.writePromptIfIdle()
	if !containsAllChatTimelineLines(rendered, "PROMPT") {
		t.Fatalf("expected prompt after team completion, got %v", rendered)
	}
}

func TestTeamRunSettled_IgnoresAmbientTeamRunningPlaceholderState(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-1",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusFailed,
	})
	require.NoError(t, err)

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

	host := &localChatRuntimeHost{
		RuntimeStore: runtimeStore,
		TeamStore:    store,
	}
	settled, err := host.teamRunSettled(context.Background(), teamID)
	require.NoError(t, err)
	if !settled {
		t.Fatalf("expected ambient team-running placeholder state to be ignored")
	}
}

func TestTeamRunSettled_DoesNotIgnoreAmbientTeamRunningSessionWhileStillRunning(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-1",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusDone,
	})
	require.NoError(t, err)

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "lead-session",
		Status:    runtimechat.SessionRunning,
		AmbientRunMeta: &team.RunMeta{
			Team: &team.TeamRunMeta{
				TeamID: teamID,
			},
		},
	}))

	host := &localChatRuntimeHost{
		RuntimeStore: runtimeStore,
		TeamStore:    store,
	}
	settled, err := host.teamRunSettled(context.Background(), teamID)
	require.NoError(t, err)
	if settled {
		t.Fatalf("expected running ambient team session to keep team unsettled")
	}
}

func TestSanitizeInteractiveAsyncTeamLaunchResponse_StripsFollowUpDecisionBlock(t *testing.T) {
	raw := `已创建 3 个团队成员来并行探索 docs 目录文档，团队已在后台开始工作。

我会在他们完成后为你汇总：
- 每一组文档的核心内容
- 推荐优先阅读顺序

如果你愿意，我下一步可以继续：
1.. 等团队结果返回后给你总览总结
2.. 现在直接由我先快速浏览 docs 并给你一个即时概览`

	got := sanitizeInteractiveAsyncTeamLaunchResponse(raw)
	if strings.Contains(got, "如果你愿意，我下一步可以继续") {
		t.Fatalf("expected follow-up choice block to be removed, got %q", got)
	}
	if strings.Contains(got, "1.. 等团队结果返回后给你总览总结") {
		t.Fatalf("expected numbered options to be removed, got %q", got)
	}
	if !strings.Contains(got, "团队已在后台开始工作") {
		t.Fatalf("expected background execution notice to remain, got %q", got)
	}
	if !strings.Contains(got, "我会在他们完成后为你汇总") {
		t.Fatalf("expected automatic-summary promise to remain, got %q", got)
	}
}

func TestIsReadOnlyShellCommand(t *testing.T) {
	for _, command := range []string{
		"Get-ChildItem docs",
		"Get-ChildItem docs -Recurse | Select-String README",
		"rg team docs",
		"git diff -- docs",
		"type README.md",
	} {
		if !isReadOnlyShellCommand(command) {
			t.Fatalf("expected read-only shell command to be cacheable: %q", command)
		}
	}
	for _, command := range []string{
		"echo hi > out.txt",
		"Remove-Item temp.txt",
		"mkdir tmp",
		"git commit -m test",
		"cmd /c dir",
	} {
		if isReadOnlyShellCommand(command) {
			t.Fatalf("expected mutating or ambiguous shell command to require approval: %q", command)
		}
	}
}

func TestChatRuntimeEvents_RendersPermissionModeHintOnce(t *testing.T) {
	session := &ChatSession{
		PermissionMode:    runtimepolicy.ModeDefault,
		ApprovalReuseMode: chatApprovalReuseTeamReadOnlyShell,
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}

	bridge.maybeRenderPermissionModeHint("permission_mode_requires_approval")
	bridge.maybeRenderPermissionModeHint("permission_mode_requires_approval")

	if len(rendered) != 1 {
		t.Fatalf("expected one permission mode hint, got %v", rendered)
	}
	if !strings.Contains(rendered[0], "--yolo") || !strings.Contains(rendered[0], "--approval-reuse=team_readonly_shell") {
		t.Fatalf("unexpected permission mode hint: %q", rendered[0])
	}
}

func TestChatRuntimeEvents_WaitForCurrentEventsWaitsForLateArrivingEvents(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	done := make(chan struct{})
	go func() {
		defer close(done)
		bridge.run()
	}()
	defer func() {
		close(bridge.eventQueue)
		<-done
	}()

	bridge.BeginRun()
	bridge.Handle(runtimeevents.Event{Type: "llm.request.started"})
	go func() {
		time.Sleep(20 * time.Millisecond)
		bridge.Handle(runtimeevents.Event{Type: "tool.completed"})
	}()

	start := time.Now()
	bridge.WaitForCurrentEvents(300 * time.Millisecond)
	elapsed := time.Since(start)

	bridge.progressMu.Lock()
	processed := bridge.processedEvents
	enqueued := bridge.enqueuedEvents
	bridge.progressMu.Unlock()

	if processed < 2 || enqueued < 2 {
		t.Fatalf("expected late event to be included before return, enqueued=%d processed=%d", enqueued, processed)
	}
	if elapsed < 20*time.Millisecond {
		t.Fatalf("expected wait to stay pending for late event arrival, got %v", elapsed)
	}
}

func TestChatRuntimeEvents_RendersAssistantDeltaAndFinalizesWithoutRepeatingResponse(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var deltas []string
	finalized := 0
	renderedResponses := 0
	bridge.writeDelta = func(delta string) {
		deltas = append(deltas, delta)
	}
	bridge.finalizeDelta = func() {
		finalized++
	}
	bridge.renderResponse = func(response string) {
		renderedResponses++
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"delta": "Hello"},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"content": "Hello"},
	})

	if len(deltas) != 1 || deltas[0] != "Hello" {
		t.Fatalf("expected one rendered delta, got %v", deltas)
	}
	if finalized != 1 {
		t.Fatalf("expected one delta finalization, got %d", finalized)
	}
	if renderedResponses != 0 {
		t.Fatalf("expected final response fallback to stay suppressed, got %d renders", renderedResponses)
	}
	if !bridge.HasRenderedAssistantDelta() {
		t.Fatal("expected bridge to remember rendered assistant delta")
	}
	if !bridge.HasRenderedAssistantFinal() {
		t.Fatal("expected bridge to remember rendered assistant final output")
	}
}

func TestChatRuntimeEvents_MarksAssistantDeltaRenderedBeforeSlowWriteCompletes(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	bridge.writeDelta = func(delta string) {
		started <- struct{}{}
		<-release
	}

	done := make(chan struct{})
	go func() {
		bridge.handleEvent(runtimeevents.Event{
			Type:      runtimechat.EventAssistantDelta,
			SessionID: "lead-session",
			Payload:   map[string]interface{}{"delta": "Hello"},
		})
		close(done)
	}()

	<-started
	if !bridge.HasRenderedAssistantDelta() {
		t.Fatal("expected delta rendered flag to flip before slow write returns")
	}
	close(release)
	<-done
}

func TestChatRuntimeEvents_PreservesWhitespaceInAssistantDelta(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var deltas []string
	bridge.writeDelta = func(delta string) {
		deltas = append(deltas, delta)
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"delta": " world"},
	})

	if len(deltas) != 1 || deltas[0] != " world" {
		t.Fatalf("expected delta whitespace to be preserved, got %v", deltas)
	}
}

func TestChatRuntimeEvents_WaitForCurrentEventsDrainsQueuedAssistantDelta(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	bridge.startOnce.Do(func() {})
	go bridge.run()
	defer close(bridge.eventQueue)

	bridge.BeginRun()
	bridge.Handle(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"delta": "Hello"},
	})
	bridge.WaitForCurrentEvents(200 * time.Millisecond)

	if !bridge.HasRenderedAssistantDelta() {
		t.Fatal("expected queued assistant delta to be rendered after drain")
	}
}

func TestChatRuntimeEvents_SuppressesLLMFinishedLineDuringActiveAssistantStream(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var lines []string
	finalized := 0
	bridge.writeDelta = func(string) {}
	bridge.writeLine = func(line string) {
		lines = append(lines, line)
	}
	bridge.finalizeDelta = func() {
		finalized++
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"delta": "Hello"},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      "llm.request.finished",
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"success": true},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"content": "Hello"},
	})

	if finalized != 1 {
		t.Fatalf("expected finalization after assistant message, got %d", finalized)
	}
	for _, line := range lines {
		if strings.Contains(line, "model responded") {
			t.Fatalf("expected llm finished line to stay suppressed during active stream, got %v", lines)
		}
	}
}

func TestActorExecutor_AnswersPendingQuestionThroughCLIBridge(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	provider := &questionToolProvider{}
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(64),
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
	}
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "bridge-test",
			Provider: "test-provider",
			Model:    "test-model",
			MaxSteps: 4,
		}, nil, llmRuntime)
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: manager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})

	session := &ChatSession{
		ProviderName:     "test-provider",
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host.BaseSession = session

	var rendered bytes.Buffer
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(line string) {
		rendered.WriteString(line)
		rendered.WriteString("\n")
	}
	bridge.askQuestion = func(prompt string, suggestions []string, required bool) (string, error) {
		if prompt != "Need user input" {
			t.Fatalf("unexpected prompt: %q", prompt)
		}
		return "provided answer", nil
	}
	session.RuntimeEventBridge = bridge

	output, err := session.ChatExecutor.Execute(context.Background(), session, "trigger question")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if output != "question answered" {
		t.Fatalf("unexpected output: %q", output)
	}
	if provider.callCount.Load() < 2 {
		t.Fatalf("expected provider to be called twice, got %d", provider.callCount.Load())
	}
	if rendered.Len() == 0 {
		t.Fatal("expected runtime event output")
	}
}

func TestActorExecutor_AskUserQuestionAnswerSurvivesReducerAndStreamFallback(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	provider := &answerPreservingQuestionProvider{}
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(64),
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
	}
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "bridge-test",
			Provider: "test-provider",
			Model:    "test-model",
			MaxSteps: 4,
		}, nil, llmRuntime)
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: manager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})

	session := &ChatSession{
		ProviderName:     "test-provider",
		Model:            "test-model",
		Stream:           true,
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host.BaseSession = session

	var rendered bytes.Buffer
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(line string) {
		rendered.WriteString(line)
		rendered.WriteString("\n")
	}
	bridge.askQuestion = func(prompt string, suggestions []string, required bool) (string, error) {
		if prompt != "Need user input" {
			t.Fatalf("unexpected prompt: %q", prompt)
		}
		return "provided answer 42", nil
	}
	session.RuntimeEventBridge = bridge

	output, err := session.ChatExecutor.Execute(context.Background(), session, "trigger preserved answer")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if output != "answer survived: provided answer 42" {
		t.Fatalf("unexpected output: %q", output)
	}
	if !shouldDisplayFinalResponse(session, output) {
		t.Fatalf("expected actor stream fallback response to be displayable, got %q", output)
	}
	if !provider.answerObserved() {
		t.Fatalf("expected provider to observe preserved answer, saw content=%q metadata=%v", provider.toolContent(), provider.toolMetadata())
	}
	if !strings.Contains(rendered.String(), "[question] Need user input") {
		t.Fatalf("expected rendered question timeline, got %q", rendered.String())
	}

	reloaded, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	toolMessage := latestToolMessage(reloaded.History)
	if toolMessage == nil {
		t.Fatalf("expected persisted tool message, got %+v", reloaded.History)
	}
	if !strings.Contains(toolMessage.Content, "answer=provided answer 42") {
		t.Fatalf("expected persisted tool message to preserve answer, got %+v", toolMessage)
	}
	if toolMessage.Metadata.GetString("reducer", "") != "json_summary" {
		t.Fatalf("expected json_summary reducer metadata, got %+v", toolMessage.Metadata)
	}
}

func TestActorExecutor_ApprovalThroughCLIBridgeExecutesToolOnceAndResumes(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	provider := &approvalToolProvider{}
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	mcpManager := &approvalCapturingMCPManager{}
	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(64),
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
	}
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "bridge-test",
			Provider: "test-provider",
			Model:    "test-model",
			MaxSteps: 4,
		}, mcpManager, llmRuntime)
		a.SetPermissionEngine(&agent.PermissionEngine{
			Callback: func(ctx context.Context, req runtimepolicy.EvalRequest) (runtimepolicy.Decision, string, error) {
				if req.ToolName == "team_echo" {
					return runtimepolicy.Decision{Type: runtimepolicy.DecisionAsk}, "manual approval", nil
				}
				return runtimepolicy.Decision{Type: runtimepolicy.DecisionAllow}, "", nil
			},
		})
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: manager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})

	session := &ChatSession{
		ProviderName:     "test-provider",
		Model:            "test-model",
		Stream:           true,
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
		ActiveTeam:       &chatTeamBinding{TeamID: "team-approval", AgentID: "mate-approval", TaskID: "task-approval"},
	}
	host.BaseSession = session

	var (
		rendered      bytes.Buffer
		approvalCalls atomic.Int32
	)
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(line string) {
		rendered.WriteString(line)
		rendered.WriteString("\n")
	}
	bridge.askApproval = func(toolName, reason string) (bool, error) {
		approvalCalls.Add(1)
		if toolName != "team_echo" {
			t.Fatalf("unexpected approval tool: %q", toolName)
		}
		if reason != "manual approval" {
			t.Fatalf("unexpected approval reason: %q", reason)
		}
		return true, nil
	}
	session.RuntimeEventBridge = bridge

	output, err := session.ChatExecutor.Execute(context.Background(), session, "trigger approval")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if output != "approval survived and resumed" {
		t.Fatalf("unexpected output: %q", output)
	}
	if !shouldDisplayFinalResponse(session, output) {
		t.Fatalf("expected actor stream fallback response to be displayable, got %q", output)
	}
	if approvalCalls.Load() != 1 {
		t.Fatalf("expected exactly one approval prompt, got %d", approvalCalls.Load())
	}
	if mcpManager.callCount != 1 {
		t.Fatalf("expected approved tool execution exactly once, got %d", mcpManager.callCount)
	}
	if mcpManager.lastMeta == nil || mcpManager.lastMeta.Team == nil {
		t.Fatalf("expected run meta on approved tool execution, got %+v", mcpManager.lastMeta)
	}
	if mcpManager.lastMeta.Team.TeamID != "team-approval" || mcpManager.lastMeta.Team.AgentID != "mate-approval" || mcpManager.lastMeta.Team.CurrentTaskID != "task-approval" {
		t.Fatalf("unexpected run meta on approved tool execution: %+v", mcpManager.lastMeta)
	}
	if !strings.Contains(rendered.String(), "[approval] team_echo") {
		t.Fatalf("expected approval timeline render, got %q", rendered.String())
	}
	if strings.Contains(rendered.String(), "[tool denied]") {
		t.Fatalf("expected no tool denial after approval, got %q", rendered.String())
	}

	reloaded, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	toolMessage := latestToolMessage(reloaded.History)
	if toolMessage == nil {
		t.Fatalf("expected persisted tool message, got %+v", reloaded.History)
	}
	if !strings.Contains(toolMessage.Content, "approved echo: hello") {
		t.Fatalf("expected persisted approved tool output, got %+v", toolMessage)
	}
}

func TestChatRuntimeEvents_SerializesConcurrentApprovalsAndQuestions(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	leadSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create lead: %v", err)
	}
	teammateSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create teammate: %v", err)
	}

	provider := &taggedQuestionToolProvider{}
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(64),
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
	}
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "bridge-test",
			Provider: "test-provider",
			Model:    "test-model",
			MaxSteps: 4,
		}, nil, llmRuntime)
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: manager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})

	session := &ChatSession{
		ProviderName:     "test-provider",
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   leadSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host.BaseSession = session

	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(string) {}
	var activePrompts atomic.Int32
	var maxConcurrent atomic.Int32
	started := make(chan string, 2)
	releaseFirst := make(chan struct{})
	var firstPrompt sync.Once
	bridge.askQuestion = func(prompt string, suggestions []string, required bool) (string, error) {
		current := activePrompts.Add(1)
		for {
			observed := maxConcurrent.Load()
			if current <= observed || maxConcurrent.CompareAndSwap(observed, current) {
				break
			}
		}
		started <- prompt
		firstPrompt.Do(func() {
			<-releaseFirst
		})
		activePrompts.Add(-1)
		return "provided answer", nil
	}
	session.RuntimeEventBridge = bridge
	bridge.start()

	leadErrCh := make(chan error, 1)
	go func() {
		_, execErr := session.ChatExecutor.Execute(context.Background(), session, "lead question")
		leadErrCh <- execErr
	}()

	var first string
	select {
	case first = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first question prompt")
	}
	if first != "Need user input: lead question" {
		t.Fatalf("unexpected first prompt: %q", first)
	}

	teammateActor, err := host.SessionHub.GetOrCreate(teammateSession.ID)
	if err != nil {
		t.Fatalf("GetOrCreate teammate actor: %v", err)
	}
	teammateErrCh := make(chan error, 1)
	go func() {
		_, submitErr := teammateActor.SubmitPrompt(context.Background(), "teammate question", nil)
		teammateErrCh <- submitErr
	}()

	select {
	case prompt := <-started:
		t.Fatalf("second prompt should stay queued until the first is answered, got %q", prompt)
	case <-time.After(200 * time.Millisecond):
	}

	close(releaseFirst)

	var second string
	select {
	case second = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued second prompt")
	}
	if second != "Need user input: teammate question" {
		t.Fatalf("unexpected second prompt: %q", second)
	}
	if maxConcurrent.Load() != 1 {
		t.Fatalf("expected prompts to stay serialized, max concurrency = %d", maxConcurrent.Load())
	}
	if err := <-leadErrCh; err != nil {
		t.Fatalf("lead Execute failed: %v", err)
	}
	if err := <-teammateErrCh; err != nil {
		t.Fatalf("teammate SubmitPrompt failed: %v", err)
	}
}

func TestChatRuntimeEvents_ReusesReadOnlyShellApprovalWithinSameTeamRun(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	provider := &cachedShellApprovalProvider{}
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	mcpManager := &shellApprovalCapturingMCPManager{}
	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(64),
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
	}
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "bridge-test",
			Provider: "test-provider",
			Model:    "test-model",
			MaxSteps: 6,
		}, mcpManager, llmRuntime)
		a.SetPermissionEngine(&agent.PermissionEngine{
			Callback: func(ctx context.Context, req runtimepolicy.EvalRequest) (runtimepolicy.Decision, string, error) {
				switch req.ToolName {
				case "bash", "execute_shell_command":
					return runtimepolicy.Decision{Type: runtimepolicy.DecisionAsk}, "manual approval", nil
				default:
					return runtimepolicy.Decision{Type: runtimepolicy.DecisionAllow}, "", nil
				}
			},
		})
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: manager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})

	session := &ChatSession{
		ProviderName:      "test-provider",
		Model:             "test-model",
		Stream:            true,
		SessionManager:    manager,
		RuntimeSession:    runtimeSession,
		SessionUserID:     userID,
		SessionDir:        dir,
		LocalRuntimeHost:  host,
		ChatExecutor:      newAICLIActorChatExecutor(),
		ApprovalReuseMode: chatApprovalReuseTeamReadOnlyShell,
		ActiveTeam:        &chatTeamBinding{TeamID: "team-approval", AgentID: "lead", TaskID: "task-approval"},
	}
	host.BaseSession = session

	var (
		rendered      bytes.Buffer
		approvalCalls atomic.Int32
	)
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(line string) {
		rendered.WriteString(line)
		rendered.WriteString("\n")
	}
	bridge.askApproval = func(toolName, reason string) (bool, error) {
		approvalCalls.Add(1)
		if reason != "manual approval" {
			t.Fatalf("unexpected approval reason: %q", reason)
		}
		return true, nil
	}
	session.RuntimeEventBridge = bridge

	output, err := session.ChatExecutor.Execute(context.Background(), session, "trigger cached shell approvals")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if output != "shell approvals reused" {
		t.Fatalf("unexpected output: %q", output)
	}
	if approvalCalls.Load() != 1 {
		t.Fatalf("expected a single interactive approval prompt, got %d", approvalCalls.Load())
	}
	if mcpManager.callCount != 2 {
		t.Fatalf("expected both shell tools to execute, got %d", mcpManager.callCount)
	}
	if !strings.Contains(rendered.String(), "[approval] execute_shell_command") {
		t.Fatalf("expected initial approval line, got %q", rendered.String())
	}
	if !strings.Contains(rendered.String(), "[approval] auto-approved bash") {
		t.Fatalf("expected cached auto-approval line for bash, got %q", rendered.String())
	}
}

func TestChatRuntimeEvents_ApprovalReusePersistsAcrossTurnsForSameTeam(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseTeamReadOnlyShell,
		ActiveTeam:        &chatTeamBinding{TeamID: "team-1", AgentID: "lead"},
	})
	bridge.BeginRun()

	approval := &runtimechat.ApprovalRequest{
		ToolName: "bash",
		ArgsJSON: []byte(`{"command":"Get-ChildItem docs"}`),
	}
	key := bridge.autoApprovalGrantKey("session-1", approval)
	if key == "" {
		t.Fatal("expected non-empty team-scoped approval key")
	}
	bridge.rememberApprovalGrant(key)

	bridge.BeginRun()
	if !bridge.hasApprovalGrant(key) {
		t.Fatalf("expected approval grant to persist across turns for same team")
	}
}

func TestChatRuntimeEvents_ApprovalReuseDoesNotApplyWithoutActiveTeam(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	approval := &runtimechat.ApprovalRequest{
		ToolName: "bash",
		ArgsJSON: []byte(`{"command":"Get-ChildItem docs"}`),
	}
	if key := bridge.autoApprovalGrantKey("session-1", approval); key != "" {
		t.Fatalf("expected no approval reuse scope without active team, got %q", key)
	}
}

type questionToolProvider struct {
	callCount atomic.Int32
}

func (p *questionToolProvider) Name() string { return "test-provider" }

func (p *questionToolProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	p.callCount.Add(1)
	for _, message := range req.Messages {
		if message.Role == "tool" {
			return &runtimellm.LLMResponse{
				Content: "question answered",
				Model:   req.Model,
			}, nil
		}
	}
	return &runtimellm.LLMResponse{
		Model: req.Model,
		ToolCalls: []runtimetypes.ToolCall{
			{
				ID:   "call-1",
				Name: toolbroker.ToolAskUserQuestion,
				Args: map[string]interface{}{
					"prompt":   "Need user input",
					"required": true,
				},
			},
		},
	}, nil
}

func (p *questionToolProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (p *questionToolProvider) CountTokens(text string) int { return len(text) }

func (p *questionToolProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{
		SupportsTools: true,
	}
}

func (p *questionToolProvider) CheckHealth(ctx context.Context) error { return nil }

type answerPreservingQuestionProvider struct {
	mu          sync.Mutex
	toolMsg     string
	toolMeta    runtimetypes.Metadata
	answerFound bool
}

func (p *answerPreservingQuestionProvider) Name() string { return "test-provider" }

func (p *answerPreservingQuestionProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	for _, message := range req.Messages {
		if message.Role != "tool" {
			continue
		}
		p.mu.Lock()
		p.toolMsg = strings.TrimSpace(message.Content)
		p.toolMeta = message.Metadata.Clone()
		p.answerFound = strings.Contains(message.Content, "answer=provided answer 42")
		p.mu.Unlock()
		if strings.Contains(message.Content, "answer=provided answer 42") {
			return &runtimellm.LLMResponse{
				Content: "answer survived: provided answer 42",
				Model:   req.Model,
			}, nil
		}
		return &runtimellm.LLMResponse{
			Content: "answer missing after reducer",
			Model:   req.Model,
		}, nil
	}
	return &runtimellm.LLMResponse{
		Model: req.Model,
		ToolCalls: []runtimetypes.ToolCall{
			{
				ID:   "call-preserve-answer",
				Name: toolbroker.ToolAskUserQuestion,
				Args: map[string]interface{}{
					"prompt":   "Need user input",
					"required": true,
				},
			},
		},
	}, nil
}

func (p *answerPreservingQuestionProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (p *answerPreservingQuestionProvider) CountTokens(text string) int { return len(text) }

func (p *answerPreservingQuestionProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{SupportsTools: true}
}

func (p *answerPreservingQuestionProvider) CheckHealth(ctx context.Context) error { return nil }

type approvalToolProvider struct {
	callCount atomic.Int32
}

type cachedShellApprovalProvider struct {
	callCount atomic.Int32
}

func (p *approvalToolProvider) Name() string { return "test-provider" }

func (p *cachedShellApprovalProvider) Name() string { return "test-provider" }

func (p *approvalToolProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	p.callCount.Add(1)
	for _, message := range req.Messages {
		if message.Role == "tool" {
			return &runtimellm.LLMResponse{
				Content: "approval survived and resumed",
				Model:   req.Model,
			}, nil
		}
	}
	return &runtimellm.LLMResponse{
		Model: req.Model,
		ToolCalls: []runtimetypes.ToolCall{
			{
				ID:   "call-approval-1",
				Name: "team_echo",
				Args: map[string]interface{}{"message": "hello"},
			},
		},
	}, nil
}

func (p *cachedShellApprovalProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	p.callCount.Add(1)
	toolCount := 0
	for _, message := range req.Messages {
		if message.Role == "tool" {
			toolCount++
		}
	}
	switch toolCount {
	case 0:
		return &runtimellm.LLMResponse{
			Model: req.Model,
			ToolCalls: []runtimetypes.ToolCall{
				{
					ID:   "call-shell-1",
					Name: "execute_shell_command",
					Args: map[string]interface{}{"command": "Get-ChildItem docs"},
				},
			},
		}, nil
	case 1:
		return &runtimellm.LLMResponse{
			Model: req.Model,
			ToolCalls: []runtimetypes.ToolCall{
				{
					ID:   "call-shell-2",
					Name: "bash",
					Args: map[string]interface{}{"command": "Get-Content README.md"},
				},
			},
		}, nil
	default:
		return &runtimellm.LLMResponse{
			Content: "shell approvals reused",
			Model:   req.Model,
		}, nil
	}
}

func (p *approvalToolProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (p *cachedShellApprovalProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (p *approvalToolProvider) CountTokens(text string) int { return len(text) }

func (p *cachedShellApprovalProvider) CountTokens(text string) int { return len(text) }

func (p *approvalToolProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{SupportsTools: true}
}

func (p *cachedShellApprovalProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{SupportsTools: true}
}

func (p *approvalToolProvider) CheckHealth(ctx context.Context) error { return nil }

func (p *cachedShellApprovalProvider) CheckHealth(ctx context.Context) error { return nil }

type approvalCapturingMCPManager struct {
	lastMeta  *team.RunMeta
	callCount int
}

type shellApprovalCapturingMCPManager struct {
	callCount int
}

func (m *approvalCapturingMCPManager) FindTool(toolName string) (runtimeskill.ToolInfo, error) {
	if toolName != "team_echo" {
		return runtimeskill.ToolInfo{}, fmt.Errorf("tool not found: %s", toolName)
	}
	return runtimeskill.ToolInfo{
		Name:          toolName,
		Description:   "Echo tool for approval CLI tests",
		MCPName:       "test-mcp",
		MCPTrustLevel: "local",
		ExecutionMode: "local_mcp",
		Enabled:       true,
	}, nil
}

func (m *approvalCapturingMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	runCtx, ok := ctx.(context.Context)
	if !ok {
		return nil, fmt.Errorf("unexpected context type %T", ctx)
	}
	meta, ok := team.GetRunMeta(runCtx)
	if !ok || meta == nil {
		return nil, fmt.Errorf("run meta missing")
	}
	m.lastMeta = meta.Clone()
	m.callCount++
	return "approved echo: " + fmt.Sprint(args["message"]), nil
}

func (m *approvalCapturingMCPManager) ListTools() []runtimeskill.ToolInfo {
	info, _ := m.FindTool("team_echo")
	return []runtimeskill.ToolInfo{info}
}

func (m *shellApprovalCapturingMCPManager) FindTool(toolName string) (runtimeskill.ToolInfo, error) {
	switch toolName {
	case "bash", "execute_shell_command":
		return runtimeskill.ToolInfo{
			Name:          toolName,
			Description:   "Shell tool for approval cache tests",
			MCPName:       "test-mcp",
			MCPTrustLevel: "local",
			ExecutionMode: "local_mcp",
			Enabled:       true,
		}, nil
	default:
		return runtimeskill.ToolInfo{}, fmt.Errorf("tool not found: %s", toolName)
	}
}

func (m *shellApprovalCapturingMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	m.callCount++
	return fmt.Sprintf("%s ok: %v", toolName, args["command"]), nil
}

func (m *shellApprovalCapturingMCPManager) ListTools() []runtimeskill.ToolInfo {
	info1, _ := m.FindTool("execute_shell_command")
	info2, _ := m.FindTool("bash")
	return []runtimeskill.ToolInfo{info1, info2}
}

func (p *answerPreservingQuestionProvider) answerObserved() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.answerFound
}

func (p *answerPreservingQuestionProvider) toolContent() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.toolMsg
}

func (p *answerPreservingQuestionProvider) toolMetadata() runtimetypes.Metadata {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.toolMeta.Clone()
}

type taggedQuestionToolProvider struct{}

func (p *taggedQuestionToolProvider) Name() string { return "test-provider" }

func (p *taggedQuestionToolProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	for _, message := range req.Messages {
		if message.Role == "tool" {
			return &runtimellm.LLMResponse{
				Content: "question answered",
				Model:   req.Model,
			}, nil
		}
	}
	prompt := ""
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			prompt = strings.TrimSpace(req.Messages[i].Content)
			break
		}
	}
	return &runtimellm.LLMResponse{
		Model: req.Model,
		ToolCalls: []runtimetypes.ToolCall{
			{
				ID:   "call-1",
				Name: toolbroker.ToolAskUserQuestion,
				Args: map[string]interface{}{
					"prompt":   "Need user input: " + prompt,
					"required": true,
				},
			},
		},
	}, nil
}

func (p *taggedQuestionToolProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (p *taggedQuestionToolProvider) CountTokens(text string) int { return len(text) }

func (p *taggedQuestionToolProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{SupportsTools: true}
}

func (p *taggedQuestionToolProvider) CheckHealth(ctx context.Context) error { return nil }

func latestToolMessage(history []runtimetypes.Message) *runtimetypes.Message {
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Role != "tool" {
			continue
		}
		cloned := history[index]
		return &cloned
	}
	return nil
}

func TestChatRuntimeEvents_NonInteractiveQuestionReturnsError(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	provider := &questionToolProvider{}
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(64),
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
	}
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "bridge-test",
			Provider: "test-provider",
			Model:    "test-model",
			MaxSteps: 4,
		}, nil, llmRuntime)
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: manager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})

	session := &ChatSession{
		NoInteractive:    true,
		ProviderName:     "test-provider",
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host.BaseSession = session

	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	_, err = session.ChatExecutor.Execute(context.Background(), session, "trigger question")
	if err == nil {
		t.Fatal("expected non-interactive question to fail")
	}
	if !strings.Contains(err.Error(), "--no-interactive") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestChatRuntimeEvents_NonInteractiveApprovalReturnsError(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	provider := &approvalToolProvider{}
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	mcpManager := &approvalCapturingMCPManager{}
	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(64),
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
	}
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "bridge-test",
			Provider: "test-provider",
			Model:    "test-model",
			MaxSteps: 4,
		}, mcpManager, llmRuntime)
		a.SetPermissionEngine(&agent.PermissionEngine{
			Callback: func(ctx context.Context, req runtimepolicy.EvalRequest) (runtimepolicy.Decision, string, error) {
				if req.ToolName == "team_echo" {
					return runtimepolicy.Decision{Type: runtimepolicy.DecisionAsk}, "manual approval", nil
				}
				return runtimepolicy.Decision{Type: runtimepolicy.DecisionAllow}, "", nil
			},
		})
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: manager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})

	session := &ChatSession{
		NoInteractive:    true,
		ProviderName:     "test-provider",
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host.BaseSession = session

	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	_, err = session.ChatExecutor.Execute(context.Background(), session, "trigger approval")
	if err == nil {
		t.Fatal("expected non-interactive approval to fail")
	}
	if !strings.Contains(err.Error(), "--no-interactive") {
		t.Fatalf("unexpected error: %v", err)
	}
	if mcpManager.callCount != 0 {
		t.Fatalf("expected denied approval path to skip tool execution, got %d", mcpManager.callCount)
	}
}
