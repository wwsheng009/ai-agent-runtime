package commands

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 回归：aicli resume 一个 TeamStore 中残留 stale active team 的会话时，
// 启动早期探测（restoreLocalRuntimeHostTeamState → reconcileStaleAmbientTeams）
// 必须把"实际已结束"的团队置为终止态，否则主循环第一轮
// waitForTeamTerminal 永久阻塞，prompt `>` 与输入区域永不渲染。
func TestResumeUnifiedPromptRenderedAfterStaleAmbientTeam(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	chatIsInteractiveTerminal = func() bool { return true }
	t.Cleanup(func() { chatIsInteractiveTerminal = oldInteractive })
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })

	const sessionID = "sess-stale-team"
	_, err = teamStore.CreateTeam(context.Background(), team.Team{
		ID:            "team-stale-1",
		LeadSessionID: sessionID,
		Status:        team.TeamStatusActive,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	})
	require.NoError(t, err)
	// 终态任务：团队实际已跑完，只是退出时 terminal 状态未持久化（进程
	// 异常退出/状态写入竞争），TeamStore 残留 active record。
	_, err = teamStore.CreateTask(context.Background(), team.Task{
		ID:        "task-1",
		TeamID:    "team-stale-1",
		Title:     "stale task",
		Goal:      "stale task",
		Status:    team.TaskStatusDone,
		Version:   1,
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	session := &ChatSession{
		Provider:       config.Provider{Protocol: "codex"},
		cancelCtx:      context.Background(),
		ChatExecutor:   &fakeChatExecutor{output: "resumed"},
		Formatter:      formatter.NewMarkdownFormatter(false),
		RuntimeSession: &runtimechat.Session{ID: sessionID},
		LocalRuntimeHost: &localChatRuntimeHost{
			TeamStore:     teamStore,
			TeamLifecycle: newLocalTeamLifecycleService(&localChatRuntimeHost{TeamStore: teamStore}),
		},
	}
	session.LocalRuntimeHost.BaseSession = session
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 22)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}

	history := make([]runtimetypes.Message, 0, 48)
	for index := 0; index < 24; index++ {
		history = append(history,
			*runtimetypes.NewUserMessage(fmt.Sprintf("continue the previous task %02d", index)),
			*runtimetypes.NewAssistantMessage(fmt.Sprintf("resumed answer %02d", index)),
		)
	}
	if err := replaceRuntimeMessages(session, history); err != nil {
		t.Fatalf("restore canonical history: %v", err)
	}

	presentChatStartupSession(session, &chatCommandOptions{OutputFormat: "interactive"}, session.RuntimeSession)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	// 启动早期 stale 探测（restoreLocalRuntimeHostTeamState 中、sync loops 之前）
	reconcileStaleAmbientTeams(session)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	// 团队必须被判定终止，且主循环第一轮不再阻塞等待其 terminal。
	record, err := teamStore.GetTeam(context.Background(), "team-stale-1")
	require.NoError(t, err)
	if record == nil || !team.IsTerminalTeamStatus(record.Status) {
		t.Fatalf("stale active team not reconciled to terminal state, got %+v", record)
	}
	binding := resolvedInteractiveTeamBinding(session)
	if binding != nil {
		if session.LocalRuntimeHost.teamLifecycleService().Pending(context.Background(), binding.TeamID) {
			t.Fatalf("stale team still pending after startup reconcile")
		}
	}

	// 主循环第一轮：不得阻塞（用超时防挂死），并且 prompt 必须渲染。
	type result struct {
		showPrompt bool
		notice     string
		err        error
	}
	done := make(chan result, 1)
	go func() {
		showPrompt, notice, err := prepareInteractiveRead(session)
		done <- result{showPrompt, notice, err}
	}()
	select {
	case r := <-done:
		require.NoError(t, r.err)
		require.Empty(t, r.notice)
		if !r.showPrompt {
			t.Fatalf("resume with stale active team: showPrompt=false (prompt never rendered)")
		}
		session.Interaction.PrintPrompt()
		coordinator.waitUIActorIdle()
		awaitUnifiedPresenterIdle(t, coordinator)
		state := coordinator.uiActor.AppState()
		if !state.Bottom.PromptVisible || strings.TrimSpace(state.Bottom.PromptLine) == "" {
			t.Fatalf("AppState prompt not rendered after resume: %+v", state.Bottom)
		}
		assertResumeBottomPhysicallyRendered(t, terminal.String(), state, 72, 22)
	case <-time.After(3 * time.Second):
		t.Fatalf("prepareInteractiveRead blocked >3s waiting for stale active team terminal — prompt never rendered")
	}
}

func TestResumeReconcilePausesEmptyActiveAmbientTeam(t *testing.T) {
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })

	const (
		sessionID = "sess-empty-team"
		teamID    = "team-empty"
	)
	_, err = teamStore.CreateTeam(context.Background(), team.Team{
		ID:            teamID,
		LeadSessionID: sessionID,
		Status:        team.TeamStatusActive,
	})
	require.NoError(t, err)

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: sessionID,
		Status:    runtimechat.SessionRunning,
		AmbientRunMeta: &team.RunMeta{
			Team: &team.TeamRunMeta{TeamID: teamID, AgentID: "lead"},
		},
	}))
	host := &localChatRuntimeHost{
		TeamStore:    teamStore,
		RuntimeStore: runtimeStore,
	}
	lifecycle := newLocalTeamLifecycleService(host)
	t.Cleanup(lifecycle.StopLoops)
	host.TeamLifecycle = lifecycle
	session := &ChatSession{
		cancelCtx:        context.Background(),
		RuntimeSession:   &runtimechat.Session{ID: sessionID},
		LocalRuntimeHost: host,
		ActiveTeam: &chatTeamBinding{
			TeamID:  teamID,
			AgentID: "lead",
		},
	}
	host.BaseSession = session

	if !lifecycle.Pending(context.Background(), teamID) {
		t.Fatal("test precondition: empty active team must be pending before startup reconciliation")
	}
	restoreLocalRuntimeHostTeamState(session)

	record, err := teamStore.GetTeam(context.Background(), teamID)
	require.NoError(t, err)
	if record == nil || record.Status != team.TeamStatusPaused {
		t.Fatalf("empty recovered team status = %+v, want paused", record)
	}
	if lifecycle.Pending(context.Background(), teamID) {
		t.Fatal("empty recovered team still blocks the interactive prompt")
	}
	state, err := runtimeStore.LoadState(context.Background(), sessionID)
	require.NoError(t, err)
	if state == nil || state.AmbientRunMeta != nil {
		t.Fatalf("stale ambient run metadata was not cleared: %+v", state)
	}
	showPrompt, notice, err := prepareInteractiveRead(session)
	require.NoError(t, err)
	require.Empty(t, notice)
	if !showPrompt {
		t.Fatal("empty recovered team prevented the first resumed prompt")
	}
}

// 对照：resume 一个没有团队的普通会话时 prompt 必须正常渲染。
func TestResumeUnifiedPromptRenderedPlainHistory(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	chatIsInteractiveTerminal = func() bool { return true }
	t.Cleanup(func() { chatIsInteractiveTerminal = oldInteractive })
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{
		Provider:     config.Provider{Protocol: "codex"},
		cancelCtx:    context.Background(),
		ChatExecutor: &fakeChatExecutor{output: "resumed"},
		Formatter:    formatter.NewMarkdownFormatter(false),
	}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 22)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}

	if err := replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("continue the previous task"),
		*runtimetypes.NewAssistantMessage("resumed answer"),
	}); err != nil {
		t.Fatalf("restore canonical history: %v", err)
	}

	presentChatStartupSession(session, &chatCommandOptions{OutputFormat: "interactive"}, nil)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	showPrompt, notice, err := prepareInteractiveRead(session)
	require.NoError(t, err)
	require.Empty(t, notice)
	if !showPrompt {
		t.Fatalf("plain resume: showPrompt=false")
	}
	session.Interaction.PrintPrompt()
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	state := coordinator.uiActor.AppState()
	if !state.Bottom.PromptVisible || strings.TrimSpace(state.Bottom.PromptLine) == "" {
		t.Fatalf("plain resume: AppState prompt not rendered: %+v", state.Bottom)
	}
	assertResumeBottomPhysicallyRendered(t, terminal.String(), state, 72, 22)
}

func assertResumeBottomPhysicallyRendered(t *testing.T, output string, state ui.AppState, width, height int) {
	t.Helper()
	screen := newScreenVT(width, height)
	screen.feed(output)
	frame := ui.ComposeAppTextLayout(state)
	for _, row := range frame.Rows {
		if row.Owner != renderengine.RowOwnerPrompt && row.Owner != renderengine.RowOwnerStatus {
			continue
		}
		if got := screen.line(row.Row); got != row.Text {
			t.Fatalf("resume: physical row %d (%v) = %q, want %q\nscreen:\n%s",
				row.Row, row.Owner, got, row.Text, screen.dump())
		}
	}
}
