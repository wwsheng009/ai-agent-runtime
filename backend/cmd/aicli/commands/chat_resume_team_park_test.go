package commands

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// 回归：resume 上一进程遗留的"团队仍在执行"（active + 未终态任务）会话时，
// 交互式启动必须把该团队停放到 paused 并回到等待输入状态，而不是启动即重新
// 拉起 team lifecycle loop 继续执行 —— 后者会让 interactiveTeamPending 恒为
// true，主循环阻塞在 waitForTeamTerminal，composer `>` 永不渲染。
func TestResumeInteractiveParksUnfinishedAmbientTeam(t *testing.T) {
	teamStore, session, teamID := newResumeParkedTeamFixture(t, false)
	lifecycle := session.LocalRuntimeHost.teamLifecycleService().(*localTeamLifecycleService)
	require.True(t, lifecycle.Pending(context.Background(), teamID),
		"test precondition: unfinished team must be pending before resume")

	restoreLocalRuntimeHostTeamState(session)

	record, err := teamStore.GetTeam(context.Background(), teamID)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, team.TeamStatusPaused, record.Status, "resume must park the restored team instead of re-running it")
	require.False(t, lifecycle.hasTeamLoop(teamID), "resume must not restart the parked team loop")
	require.False(t, lifecycle.Pending(context.Background(), teamID), "parked team must not block the composer")
	require.Nil(t, chatSessionActiveTeam(session), "parked team binding must be cleared")

	state, err := session.LocalRuntimeHost.RuntimeStore.LoadState(context.Background(), session.RuntimeSession.ID)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Nil(t, state.AmbientRunMeta, "stale ambient run metadata must not keep the team bound")

	showPrompt, notice, err := prepareInteractiveRead(session)
	require.NoError(t, err)
	require.Empty(t, notice)
	require.True(t, showPrompt, "resume must end in the waiting-input state with the composer prompt available")

	// 停放而非取消：任务被标记为 cancelled（不再处于执行态），团队壳保留。
	tasks, err := teamStore.ListTasks(context.Background(), team.TaskFilter{TeamID: teamID})
	require.NoError(t, err)
	require.NotEmpty(t, tasks)
	for _, task := range tasks {
		require.Equal(t, team.TaskStatusCancelled, task.Status)
	}
}

// 对照：headless/JSON resume 保持原有语义（遗留团队继续跑到终态）。
func TestResumeHeadlessKeepsUnfinishedAmbientTeamActive(t *testing.T) {
	teamStore, session, teamID := newResumeParkedTeamFixture(t, true)
	lifecycle := session.LocalRuntimeHost.teamLifecycleService().(*localTeamLifecycleService)

	if parkedID, suspended := suspendRestoredAmbientTeamForInteractiveResume(session); suspended {
		t.Fatalf("headless resume must not park the restored team, parked=%q", parkedID)
	}

	record, err := teamStore.GetTeam(context.Background(), teamID)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, team.TeamStatusActive, record.Status)
	require.True(t, lifecycle.Pending(context.Background(), teamID))
}

// 会话内 /resume：恢复到"上一进程遗留 active 团队"的会话时，同样必须落回
// 等待输入状态，并在恢复确认上给出提示。
func TestInSessionResumeParksUnfinishedAmbientTeam(t *testing.T) {
	teamStore, session, teamID := newResumeParkedTeamFixture(t, false)

	parkRestoredTeamAfterInteractiveResume(session)

	record, err := teamStore.GetTeam(context.Background(), teamID)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, team.TeamStatusPaused, record.Status)

	notice := chatResumeTeamNotice(session)
	require.NotEmpty(t, notice, "in-session /resume must explain that the team was parked")
	require.True(t, strings.Contains(notice, teamID), "notice must name the parked team: %q", notice)

	showPrompt, promptNotice, err := prepareInteractiveRead(session)
	require.NoError(t, err)
	require.Empty(t, promptNotice)
	require.True(t, showPrompt)
}

// 同一 ChatSession 连续 /resume 多个会话时，一次"没有停放任何团队"的恢复
// 不得把上一次的停放提示再显示一遍。
func TestInSessionResumeClearsStaleParkedTeamNotice(t *testing.T) {
	teamStore, session, teamID := newResumeParkedTeamFixture(t, false)
	setChatResumeTeamNotice(session, "stale parked-team notice")

	require.NoError(t, teamStore.UpdateTeamStatus(context.Background(), teamID, team.TeamStatusPaused))
	require.NoError(t, session.LocalRuntimeHost.RuntimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: session.RuntimeSession.ID,
		Status:    runtimechat.SessionIdle,
	}))
	session.ActiveTeam = nil

	parkRestoredTeamAfterInteractiveResume(session)

	require.Empty(t, chatResumeTeamNotice(session),
		"a resume that parks nothing must not repeat the previous parked-team notice")
}

func newResumeParkedTeamFixture(t *testing.T, headless bool) (*team.SQLiteStore, *ChatSession, string) {
	t.Helper()
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })

	const (
		sessionID = "sess-unfinished-team"
		teamID    = "team-unfinished"
	)
	_, err = teamStore.CreateTeam(context.Background(), team.Team{
		ID:            teamID,
		LeadSessionID: sessionID,
		Status:        team.TeamStatusActive,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = teamStore.CreateTask(context.Background(), team.Task{
		ID:        "task-running",
		TeamID:    teamID,
		Title:     "unfinished task",
		Goal:      "unfinished task",
		Status:    team.TaskStatusRunning,
		Version:   1,
		CreatedAt: time.Now().UTC(),
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
	host.TeamClaims = team.NewPathClaimManager(teamStore, t.TempDir())

	session := &ChatSession{
		cancelCtx:        context.Background(),
		RuntimeSession:   &runtimechat.Session{ID: sessionID},
		LocalRuntimeHost: host,
		ActiveTeam:       &chatTeamBinding{TeamID: teamID, AgentID: "lead"},
		NoInteractive:    headless,
	}
	host.BaseSession = session
	return teamStore, session, teamID
}
