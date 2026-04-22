package commands

import (
	"context"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func TestRenderAsyncTeamLaunchNotice_RendersForNewRunningTeam(t *testing.T) {
	session := &ChatSession{
		ActiveTeam: &chatTeamBinding{TeamID: "team-docs", AgentID: "lead"},
		LocalRuntimeHost: &localChatRuntimeHost{
			TeamLifecycle: &recordingTeamLifecycleService{pendingResult: true},
		},
		RuntimeEventBridge: newChatRuntimeEventBridge(&ChatSession{}),
	}
	var rendered []string
	session.RuntimeEventBridge.session = session
	session.RuntimeEventBridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}

	renderAsyncTeamLaunchNotice(session, "")

	if len(rendered) != 1 {
		t.Fatalf("expected exactly one rendered line, got %v", rendered)
	}
	if rendered[0] != "• [team] team-docs 已在后台开始执行；我会继续接收进展，并在完成后自动总结结果。" {
		t.Fatalf("unexpected rendered line: %q", rendered[0])
	}
}

func TestRenderAsyncTeamLaunchNotice_SkipsWhenExistingTeamOrNoLoop(t *testing.T) {
	session := &ChatSession{
		ActiveTeam: &chatTeamBinding{TeamID: "team-docs", AgentID: "lead"},
		LocalRuntimeHost: &localChatRuntimeHost{
			TeamLifecycle: &recordingTeamLifecycleService{pendingResult: true},
		},
		RuntimeEventBridge: newChatRuntimeEventBridge(&ChatSession{}),
	}
	var rendered []string
	session.RuntimeEventBridge.session = session
	session.RuntimeEventBridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}

	renderAsyncTeamLaunchNotice(session, "team-docs")
	if len(rendered) != 0 {
		t.Fatalf("expected no rendered line for same active team, got %v", rendered)
	}

	session.LocalRuntimeHost = &localChatRuntimeHost{}
	renderAsyncTeamLaunchNotice(session, "")
	if len(rendered) != 0 {
		t.Fatalf("expected no rendered line without a running team loop, got %v", rendered)
	}
}

func TestCurrentRunMetaForSession_IncludesActiveTeamOnlyWhilePending(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-meta",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	session := &ChatSession{
		PermissionMode:   runtimepolicy.ModeDefault,
		RuntimeSession:   &runtimechat.Session{ID: "lead-session"},
		ActiveTeam:       &chatTeamBinding{TeamID: teamID, AgentID: "lead", TaskID: "task-1"},
		LocalRuntimeHost: &localChatRuntimeHost{TeamStore: store},
	}
	runMeta := currentRunMetaForSession(session)
	if runMeta == nil || runMeta.Team == nil || runMeta.Team.TeamID != teamID {
		t.Fatalf("expected pending active team to be included in run meta, got %+v", runMeta)
	}

	if err := store.UpdateTeamStatus(context.Background(), teamID, team.TeamStatusDone); err != nil {
		t.Fatalf("UpdateTeamStatus: %v", err)
	}
	runMeta = currentRunMetaForSession(session)
	if runMeta != nil && runMeta.Team != nil {
		t.Fatalf("expected terminal team binding to be excluded from run meta, got %+v", runMeta)
	}
}

func TestCurrentRunMetaForSession_PreservesExplicitActiveTeamWithoutTeamStore(t *testing.T) {
	session := &ChatSession{
		PermissionMode: runtimepolicy.ModeDefault,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
		ActiveTeam:     &chatTeamBinding{TeamID: "team-explicit", AgentID: "mate-1", TaskID: "task-1"},
	}

	runMeta := currentRunMetaForSession(session)
	if runMeta == nil || runMeta.Team == nil {
		t.Fatalf("expected explicit active team to be included when terminal state cannot be resolved, got %+v", runMeta)
	}
	if runMeta.Team.TeamID != "team-explicit" || runMeta.Team.AgentID != "mate-1" || runMeta.Team.CurrentTaskID != "task-1" {
		t.Fatalf("unexpected explicit active team run meta: %+v", runMeta.Team)
	}
}
