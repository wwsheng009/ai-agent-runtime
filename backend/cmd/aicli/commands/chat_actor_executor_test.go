package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func TestPrepareAICLIActorRuntimeContextDoesNotAttachRetainedOutputMirror(t *testing.T) {
	ctx := prepareAICLIActorRuntimeContext(context.Background(), &ChatSession{})
	if mirror := runtimeexecutor.OutputMirrorFromContext(ctx); mirror != nil {
		t.Fatalf("actor runtime context attached raw output mirror %T; runtime tool.progress owns ActiveBand", mirror)
	}
}

func TestSubmitAICLIActorPromptWaitsForBackgroundRunAndReadyUsesActorState(t *testing.T) {
	provider := runtimellm.NewMockProvider("mock", 150*time.Millisecond)
	provider.SetResponse("background", "background complete")
	provider.SetResponse("foreground", "foreground complete")
	hub := buildTestSessionHubWithProvider(t, provider)
	t.Cleanup(hub.StopAll)
	actor, err := hub.GetOrCreate("session-1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	backgroundDone := make(chan error, 1)
	go func() {
		_, runErr := actor.SubmitPrompt(context.Background(), "background", nil)
		backgroundDone <- runErr
	}()
	waitForTestActorBusy(t, actor)

	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "session-1"},
		LocalRuntimeHost: &localChatRuntimeHost{SessionHub: hub},
	}
	coord := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord
	if coord.IsReady() {
		t.Fatal("UI reported Ready while the authoritative session actor was running")
	}
	if shouldDisplayInteractivePrompt(session) {
		t.Fatal("interactive prompt was exposed while the authoritative session actor was running")
	}
	coord.mu.Lock()
	surfaceStatus := coord.currentSurfaceStateLocked()
	coord.mu.Unlock()
	if surfaceStatus.kind == chatSurfaceStatusIdle {
		t.Fatal("status line projected Ready while the authoritative session actor was running")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := submitAICLIActorPrompt(ctx, actor, "foreground", nil, runtimechat.SubmitPromptOption{})
	if err != nil {
		t.Fatalf("serialized foreground submit failed: %v", err)
	}
	if result == nil || !result.Success || result.Output == "" {
		t.Fatalf("unexpected foreground result: %#v", result)
	}
	if err := <-backgroundDone; err != nil {
		t.Fatalf("background run failed: %v", err)
	}
	if !coord.IsReady() {
		t.Fatal("UI did not return to Ready after both actor turns completed")
	}
}

func waitForTestActorBusy(t *testing.T, actor *runtimechat.SessionActor) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if state, ok := actor.StateSummary(); ok && state.Busy() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	state, _ := actor.StateSummary()
	t.Fatalf("actor did not enter a busy state: %+v", state)
}

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

func TestCurrentRunMetaForSession_ForcesNoneCompletionRequirementFromRuntimeContext(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "child-session"},
	}
	session.RuntimeSession.SetContext(toolbroker.AgentSessionContextCompletionRequirement, "complete_task")

	runMeta := currentRunMetaForSession(session)
	if runMeta == nil {
		t.Fatalf("expected run meta for completion requirement")
	}
	if runMeta.CompletionRequirement != "none" {
		t.Fatalf("ordinary session must not re-enter complete_task from context, got %+v", runMeta)
	}
	if runMeta.Team != nil {
		t.Fatalf("ordinary child session should not gain team run meta, got %+v", runMeta.Team)
	}
}

func TestWaitForAICLIActorReady_FollowsLiveRunInsteadOfTimingOut(t *testing.T) {
	// A long provider delay keeps the background turn running well past the
	// stale-state budget. That run is live in this process, so the wait must
	// follow it to completion instead of failing the user's prompt.
	provider := runtimellm.NewMockProvider("mock", 2*time.Second)
	provider.SetResponse("background", "background complete")
	hub := buildTestSessionHubWithProvider(t, provider)
	t.Cleanup(hub.StopAll)
	actor, err := hub.GetOrCreate("session-1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	bgDone := make(chan error, 1)
	go func() {
		_, runErr := actor.SubmitPrompt(context.Background(), "background", nil)
		bgDone <- runErr
	}()
	waitForTestActorBusy(t, actor)

	origTimeout := aicliActorReadyWaitTimeout
	aicliActorReadyWaitTimeout = 150 * time.Millisecond
	defer func() { aicliActorReadyWaitTimeout = origTimeout }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := waitForAICLIActorReady(ctx, actor); err != nil {
		t.Fatalf("live run must be followed to completion, got: %v", err)
	}
	if state, ok := actor.StateSummary(); ok && state.Busy() {
		t.Fatalf("actor should be idle once the run finished, got %+v", state)
	}
	if err := <-bgDone; err != nil {
		t.Fatalf("background run failed: %v", err)
	}
}

func TestWaitForAICLIActorReady_TimesOutForBusyStateWithoutLiveRun(t *testing.T) {
	// A busy state with no in-process run is the leftover of a previous process:
	// nothing will ever finish it, so the poll must give up with a diagnostic
	// instead of hanging forever.
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "mock",
		DefaultModel:    "mock-model",
	})
	if err := llmRuntime.RegisterProvider("mock", runtimellm.NewMockProvider("mock", 10*time.Millisecond)); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	stateStore := runtimechat.NewInMemoryRuntimeStore(8)
	if err := stateStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID:     "session-stale",
		Status:        runtimechat.SessionRunning,
		CurrentTurnID: "turn-stale",
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "test-actor",
		Provider: "mock",
		Model:    "mock-model",
		MaxSteps: 4,
	}, nil, llmRuntime)
	actor, err := runtimechat.NewSessionActor("session-stale", runtimechat.SessionActorConfig{
		Agent:      apiAgent,
		LLMRuntime: llmRuntime,
		StateStore: stateStore,
	})
	if err != nil {
		t.Fatalf("NewSessionActor: %v", err)
	}
	if state, ok := actor.StateSummary(); !ok || !state.Busy() {
		t.Fatalf("pre-seeded running state must stay busy, got %+v", state)
	}
	if actor.RunInFlight() {
		t.Fatal("restored state must not report an in-process run")
	}

	origTimeout := aicliActorReadyWaitTimeout
	aicliActorReadyWaitTimeout = 120 * time.Millisecond
	defer func() { aicliActorReadyWaitTimeout = origTimeout }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = waitForAICLIActorReady(ctx, actor)
	if err == nil {
		t.Fatal("expected timeout error while a busy state has no live run")
	}
	if !strings.Contains(err.Error(), "等待就绪超时") {
		t.Fatalf("expected diagnostic timeout error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "status=") {
		t.Fatalf("expected status diagnostic in error message, got: %v", err)
	}
	if isACPCancelError(err) {
		t.Fatalf("actor readiness timeout must not be classified as cancellation: %v", err)
	}
}

func TestWaitForAICLIActorReady_ReturnsImmediatelyWhenActorIdle(t *testing.T) {
	provider := runtimellm.NewMockProvider("mock", 10*time.Millisecond)
	hub := buildTestSessionHubWithProvider(t, provider)
	t.Cleanup(hub.StopAll)
	actor, err := hub.GetOrCreate("session-1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitForAICLIActorReady(ctx, actor); err != nil {
		t.Fatalf("idle actor should be ready immediately, got: %v", err)
	}
}

// A mid-turn plan transition (enter_plan_mode / exit_plan_mode) is published on
// RunMeta by the session actor, while the control-plane live source exists only
// to carry an in-flight session mode switch. The source must therefore stay
// silent while the CLI mode is unchanged: reporting the unchanged submit-time
// value shadows the actor's update and keeps exit_plan_mode's writes denied
// (observed live 2026-09-22: status=exited but the same-turn write/apply_patch
// still returned plan_mode_write_path_not_allowed / mode:plan_denies_non_readonly).
func TestWithLivePermissionModeSourceDefersToRunMetaUntilModeChanges(t *testing.T) {
	session := &ChatSession{PermissionMode: runtimepolicy.ModePlan}
	ctx := withLivePermissionModeSource(context.Background(), session)
	source, ok := team.PermissionModeSourceFromContext(ctx)
	if !ok || source == nil {
		t.Fatal("live permission mode source missing")
	}
	if got := source(); got != "" {
		t.Fatalf("unchanged CLI mode must defer to RunMeta, got %q", got)
	}

	// An in-flight control-plane switch still wins from the next evaluation.
	setChatPermissionMode(session, runtimepolicy.ModeAcceptEdits)
	if got := source(); got != string(runtimepolicy.ModeAcceptEdits) {
		t.Fatalf("changed CLI mode = %q, want %q", got, runtimepolicy.ModeAcceptEdits)
	}
}

// The deferral must not depend on a pre-set CLI mode: a session that never had
// one still lets an in-flight switch through once the control plane writes it.
func TestWithLivePermissionModeSourceReportsFirstInFlightSwitch(t *testing.T) {
	session := &ChatSession{}
	ctx := withLivePermissionModeSource(context.Background(), session)
	source, ok := team.PermissionModeSourceFromContext(ctx)
	if !ok || source == nil {
		t.Fatal("live permission mode source missing")
	}
	if got := source(); got != "" {
		t.Fatalf("unset CLI mode must defer to RunMeta, got %q", got)
	}
	setChatPermissionMode(session, runtimepolicy.ModeBypassPermissions)
	if got := source(); got != string(runtimepolicy.ModeBypassPermissions) {
		t.Fatalf("in-flight switch = %q, want %q", got, runtimepolicy.ModeBypassPermissions)
	}
}
