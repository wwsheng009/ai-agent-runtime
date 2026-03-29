package commands

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func TestAICLIChatActorExecutor_SpawnTeamBindsSessionForFollowupTeamTool(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	provider := &localOrchestrationProvider{}
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

	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)

	session := &ChatSession{
		ProviderName:     "test-provider",
		PermissionMode:   runtimepolicy.ModeDefault,
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host.BaseSession = session

	_, err = session.ChatExecutor.Execute(context.Background(), session, "Create a team and use spawn_team now")
	if err != nil {
		t.Fatalf("first Execute failed: %v", err)
	}
	if session.ActiveTeam == nil {
		t.Fatal("expected active team binding after spawn_team")
	}
	if session.ActiveTeam.TeamID != "team-1" || session.ActiveTeam.AgentID != "lead" {
		t.Fatalf("unexpected active team after spawn: %+v", session.ActiveTeam)
	}

	response, err := session.ChatExecutor.Execute(context.Background(), session, "Read the current task spec")
	if err != nil {
		t.Fatalf("second Execute failed: %v", err)
	}
	if !strings.Contains(response, "task_id") {
		t.Fatalf("expected task id in follow-up response, got %q", response)
	}
}

func TestAICLIChatActorExecutor_RestoresAmbientBindingAfterRestart(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	provider := &localOrchestrationProvider{}
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

	host1 := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	session1 := &ChatSession{
		ProviderName:     "test-provider",
		PermissionMode:   runtimepolicy.ModeDefault,
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host1,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host1.BaseSession = session1

	if _, err := session1.ChatExecutor.Execute(context.Background(), session1, "Create a team and use spawn_team now"); err != nil {
		t.Fatalf("first Execute failed: %v", err)
	}

	reloaded, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	host2 := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	session2 := &ChatSession{
		ProviderName:     "test-provider",
		PermissionMode:   runtimepolicy.ModeDefault,
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   reloaded,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host2,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host2.BaseSession = session2
	if err := restoreChatStateFromRuntimeSession(session2, reloaded); err != nil {
		t.Fatalf("restoreChatStateFromRuntimeSession: %v", err)
	}
	validateAmbientTeamBinding(session2, teamStore)
	if session2.ActiveTeam == nil || session2.ActiveTeam.TeamID != "team-1" {
		t.Fatalf("expected restored active team binding, got %+v", session2.ActiveTeam)
	}

	response, err := session2.ChatExecutor.Execute(context.Background(), session2, "Read the current task spec")
	if err != nil {
		t.Fatalf("restored Execute failed: %v", err)
	}
	if !strings.Contains(response, "task_id") {
		t.Fatalf("expected restored task context, got %q", response)
	}
}

func TestFinalizeChatSession_NoInteractiveDrainsAutoStartedTeamLoop(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	provider := &autoStartLocalOrchestrationProvider{teammateDelay: 150 * time.Millisecond}
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

	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	session := &ChatSession{
		ProviderName:     "test-provider",
		PermissionMode:   runtimepolicy.ModeDefault,
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
		NoInteractive:    true,
		RequestTimeout:   2 * time.Second,
	}
	host.BaseSession = session

	response, err := session.ChatExecutor.Execute(context.Background(), session, "Create an auto-start team and let the planner finish the task")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(response, "team-auto") {
		t.Fatalf("expected spawn response, got %q", response)
	}

	finalizeChatSession(session)

	task, err := teamStore.GetTask(context.Background(), "task-auto")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task == nil {
		t.Fatal("expected auto-start task to exist")
	}
	if task.Status != team.TaskStatusDone {
		t.Fatalf("expected auto-start task done, got %+v", task)
	}
	if task.Summary != "auto smoke finished" {
		t.Fatalf("expected persisted task summary, got %+v", task)
	}

	teamRecord, err := teamStore.GetTeam(context.Background(), "team-auto")
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if teamRecord == nil || teamRecord.Status != team.TeamStatusDone {
		t.Fatalf("expected team done after finalize drain, got %+v", teamRecord)
	}

	reloaded, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	var foundSummary bool
	for _, message := range reloaded.History {
		if message.Role == "assistant" && strings.Contains(message.Content, "auto lead summary") {
			foundSummary = true
			break
		}
	}
	if !foundSummary {
		t.Fatalf("expected lead summary in persisted history, got %+v", reloaded.History)
	}
}

func TestAICLIChatActorExecutor_InteractiveAutoStartRendersTeamTimeline(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	provider := &autoStartLocalOrchestrationProvider{teammateDelay: 100 * time.Millisecond}
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

	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	session := &ChatSession{
		ProviderName:     "test-provider",
		PermissionMode:   runtimepolicy.ModeDefault,
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host.BaseSession = session

	var (
		linesMu sync.Mutex
		lines   []string
	)
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(line string) {
		linesMu.Lock()
		defer linesMu.Unlock()
		lines = append(lines, line)
	}
	session.RuntimeEventBridge = bridge

	response, err := session.ChatExecutor.Execute(context.Background(), session, "Create an auto-start team and let the planner finish the task")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(response, "team-auto") {
		t.Fatalf("expected spawn response, got %q", response)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := host.waitForTeamTerminal(waitCtx, "team-auto"); err != nil {
		t.Fatalf("waitForTeamTerminal: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot := func() []string {
			linesMu.Lock()
			defer linesMu.Unlock()
			cloned := make([]string, len(lines))
			copy(cloned, lines)
			return cloned
		}()
		if containsAllChatTimelineLines(snapshot,
			"[task] started task-auto @planner",
			"[progress] planner -> lead task-auto Started task: Auto task",
			"[done] planner -> lead task-auto auto smoke finished",
			"[task] completed task-auto @planner auto smoke finished",
			"[team] completed team-auto status=done",
			"[team summary] team-auto auto lead summary",
		) && containsOrderedChatTimelineLines(snapshot,
			"[task] completed task-auto @planner auto smoke finished",
			"[team] completed team-auto status=done",
			"[team summary] team-auto auto lead summary",
		) {
			return
		}
		if time.Now().After(deadline) {
			teamEvents, listErr := teamStore.ListTeamEvents(context.Background(), team.TeamEventFilter{TeamID: "team-auto"})
			t.Fatalf("timed out waiting for timeline lines, got %v; recent runtime events=%v; team events=%v; team events err=%v", snapshot, host.EventBus.Recent(20), teamEvents, listErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAICLIChatActorExecutor_AutoStartQueuedTasksStaySerializedPerTeammate(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	provider := &serialAutoStartLocalOrchestrationProvider{teammateDelay: 120 * time.Millisecond}
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

	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	session := &ChatSession{
		ProviderName:     "test-provider",
		PermissionMode:   runtimepolicy.ModeDefault,
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host.BaseSession = session

	var (
		linesMu sync.Mutex
		lines   []string
	)
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(line string) {
		linesMu.Lock()
		defer linesMu.Unlock()
		lines = append(lines, line)
	}
	session.RuntimeEventBridge = bridge

	response, err := session.ChatExecutor.Execute(context.Background(), session, "Create a serial auto-start team and let queued planner tasks finish")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(response, "team-serial") {
		t.Fatalf("expected serial spawn response, got %q", response)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := host.waitForTeamTerminal(waitCtx, "team-serial"); err != nil {
		t.Fatalf("waitForTeamTerminal: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		snapshot := func() []string {
			linesMu.Lock()
			defer linesMu.Unlock()
			cloned := make([]string, len(lines))
			copy(cloned, lines)
			return cloned
		}()
		if containsOrderedChatTimelinePrefixes(snapshot,
			"[task] started task-serial-1 @planner",
			"[task] completed task-serial-1 @planner",
			"[task] started task-serial-2 @planner",
			"[task] completed task-serial-2 @planner",
			"[team] completed team-serial status=done",
			"[team summary] team-serial serial lead summary",
		) && containsAllChatTimelineLines(snapshot,
			"[team] completed team-serial status=done",
			"[team summary] team-serial serial lead summary",
		) {
			break
		}
		if time.Now().After(deadline) {
			teamEvents, listErr := teamStore.ListTeamEvents(context.Background(), team.TeamEventFilter{TeamID: "team-serial"})
			reloaded, loadErr := manager.Get(context.Background(), runtimeSession.ID)
			t.Fatalf("timed out waiting for serialized queued task timeline, got %v; runtime events=%v; team events=%v; team events err=%v; session history=%v; session err=%v", snapshot, host.EventBus.Recent(30), teamEvents, listErr, reloaded.History, loadErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	task1, err := teamStore.GetTask(context.Background(), "task-serial-1")
	if err != nil {
		t.Fatalf("GetTask task-serial-1: %v", err)
	}
	task2, err := teamStore.GetTask(context.Background(), "task-serial-2")
	if err != nil {
		t.Fatalf("GetTask task-serial-2: %v", err)
	}
	if task1 == nil || task2 == nil {
		t.Fatalf("expected queued tasks to exist, got task1=%+v task2=%+v", task1, task2)
	}
	if task1.Status != team.TaskStatusDone || task2.Status != team.TaskStatusDone {
		t.Fatalf("expected both queued tasks done, got task1=%+v task2=%+v", task1, task2)
	}
	if strings.TrimSpace(task1.Summary) == "" || strings.TrimSpace(task2.Summary) == "" {
		t.Fatalf("expected non-empty queued task summaries, got task1=%+v task2=%+v", task1, task2)
	}
	if strings.Contains(strings.ToLower(task1.Summary), "session is busy") || strings.Contains(strings.ToLower(task2.Summary), "session is busy") {
		t.Fatalf("expected no busy-session failure summaries, got task1=%+v task2=%+v", task1, task2)
	}

	teamRecord, err := teamStore.GetTeam(context.Background(), "team-serial")
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if teamRecord == nil || teamRecord.Status != team.TeamStatusDone {
		t.Fatalf("expected serial team done, got %+v", teamRecord)
	}

	if provider.callCount("task-serial-1") != 1 || provider.callCount("task-serial-2") != 1 {
		t.Fatalf("expected exactly one teammate run per queued task, got task-serial-1=%d task-serial-2=%d", provider.callCount("task-serial-1"), provider.callCount("task-serial-2"))
	}

	linesMu.Lock()
	snapshot := append([]string(nil), lines...)
	linesMu.Unlock()
	if containsChatTimelinePrefix(snapshot, "[task] failed ") {
		t.Fatalf("expected no task failure timeline entries, got %v", snapshot)
	}

	waitForChatTestCondition(t, 2*time.Second, func() bool {
		events, listErr := teamStore.ListTeamEvents(context.Background(), team.TeamEventFilter{TeamID: "team-serial"})
		return listErr == nil &&
			hasTeamEventType(events, "team.completed") &&
			hasTeamEventType(events, "team.summary")
	}, func() string {
		events, listErr := teamStore.ListTeamEvents(context.Background(), team.TeamEventFilter{TeamID: "team-serial"})
		return fmt.Sprintf("team events=%v; team events err=%v; runtime events=%v", events, listErr, host.EventBus.Recent(30))
	})

	events, err := teamStore.ListTeamEvents(context.Background(), team.TeamEventFilter{TeamID: "team-serial"})
	if err != nil {
		t.Fatalf("ListTeamEvents: %v", err)
	}
	for _, event := range events {
		if event.Type == "task.failed" {
			t.Fatalf("expected no task.failed events, got %+v", events)
		}
	}

	reloaded, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	for _, message := range reloaded.History {
		if strings.Contains(strings.ToLower(message.Content), "session is busy") {
			t.Fatalf("expected no busy-session text in lead history, got %+v", reloaded.History)
		}
	}
}

func TestAICLIChatActorExecutor_PersistsActiveTeamBindingWhenTurnEndsWithEmptyReply(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	provider := &emptyReplyAfterSpawnTeamProvider{}
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

	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	session := &ChatSession{
		ProviderName:     "test-provider",
		PermissionMode:   runtimepolicy.ModeDefault,
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host.BaseSession = session

	_, err = session.ChatExecutor.Execute(context.Background(), session, "Create a failing auto-start team")
	if err == nil || !strings.Contains(err.Error(), "空回复") {
		t.Fatalf("expected empty reply error, got %v", err)
	}
	if session.ActiveTeam == nil || session.ActiveTeam.TeamID != "team-empty" {
		t.Fatalf("expected active team binding to survive error path, got %+v", session.ActiveTeam)
	}

	reloaded, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	if got := runtimeSessionContextString(reloaded, chatRuntimeContextActiveTeamID); got != "team-empty" {
		t.Fatalf("expected persisted active team id after error path, got %q", got)
	}
}

func TestAICLIChatActorExecutor_AutoStartTeamPublishesSingleTerminalEvents(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	provider := &autoStartLocalOrchestrationProvider{teammateDelay: 80 * time.Millisecond}
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

	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	session := &ChatSession{
		ProviderName:     "test-provider",
		PermissionMode:   runtimepolicy.ModeDefault,
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host.BaseSession = session

	response, err := session.ChatExecutor.Execute(context.Background(), session, "Create an auto-start team and let the planner finish the task")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(response, "team-auto") {
		t.Fatalf("expected spawn response, got %q", response)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := host.waitForTeamTerminal(waitCtx, "team-auto"); err != nil {
		t.Fatalf("waitForTeamTerminal: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		events, err := runtimeSessionEvents(runtimeStoreFromHost(host), runtimeSession.ID)
		if err == nil {
			completed := 0
			summaries := 0
			for _, event := range events {
				if event.Type == "team.completed" {
					completed++
				}
				if event.Type == "team.summary" {
					summaries++
				}
			}
			if completed == 1 && summaries == 1 {
				return
			}
		}
		if time.Now().After(deadline) {
			events, listErr := runtimeSessionEvents(runtimeStoreFromHost(host), runtimeSession.ID)
			t.Fatalf("expected exactly one terminal team event pair, got events=%v err=%v", events, listErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAICLIChatActorExecutor_AutoStartTeamMarksBaseSessionRunningUntilSettled(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	provider := &autoStartLocalOrchestrationProvider{teammateDelay: 120 * time.Millisecond}
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

	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	session := &ChatSession{
		ProviderName:     "test-provider",
		PermissionMode:   runtimepolicy.ModeDefault,
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host.BaseSession = session

	response, err := session.ChatExecutor.Execute(context.Background(), session, "Create an auto-start team and let the planner finish the task")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(response, "team-auto") {
		t.Fatalf("expected spawn response, got %q", response)
	}

	state, err := runtimeStoreFromHost(host).LoadState(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("LoadState after execute: %v", err)
	}
	if state == nil || state.Status != runtimechat.SessionIdle || strings.TrimSpace(state.CurrentTurnID) != "" || state.AmbientRunMeta == nil || state.AmbientRunMeta.Team == nil || state.AmbientRunMeta.Team.TeamID != "team-auto" {
		t.Fatalf("expected base session runtime state to stay idle with ambient team metadata while team is pending, got %+v", state)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := host.waitForTeamTerminal(waitCtx, "team-auto"); err != nil {
		t.Fatalf("waitForTeamTerminal: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		state, err = runtimeStoreFromHost(host).LoadState(context.Background(), runtimeSession.ID)
		if err == nil && state != nil && state.Status == runtimechat.SessionIdle {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected base session runtime state to return idle after team settled, got %+v err=%v", state, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAICLIChatActorExecutor_AutoStartTeamClosesNonLeadTeammateSessionAfterTerminal(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	provider := &autoStartLocalOrchestrationProvider{teammateDelay: 150 * time.Millisecond}
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

	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	session := &ChatSession{
		ProviderName:     "test-provider",
		PermissionMode:   runtimepolicy.ModeDefault,
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host.BaseSession = session

	response, err := session.ChatExecutor.Execute(context.Background(), session, "Create an auto-start team and let the planner finish the task")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(response, "team-auto") {
		t.Fatalf("expected spawn response, got %q", response)
	}

	teammates, err := teamStore.ListTeammates(context.Background(), "team-auto")
	if err != nil {
		t.Fatalf("ListTeammates: %v", err)
	}
	if len(teammates) != 1 {
		t.Fatalf("expected exactly one teammate, got %+v", teammates)
	}
	teammate := teammates[0]
	if strings.TrimSpace(teammate.SessionID) == "" {
		t.Fatalf("expected teammate session id, got %+v", teammate)
	}
	teammateSessionID := strings.TrimSpace(teammate.SessionID)

	startDeadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := host.SessionHub.Get(teammateSessionID); ok {
			break
		}
		if time.Now().After(startDeadline) {
			t.Fatalf("expected teammate actor %q to be created before terminal cleanup", teammateSessionID)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := host.SessionHub.Get(runtimeSession.ID); !ok {
		t.Fatalf("expected lead actor %q to remain registered", runtimeSession.ID)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := host.waitForTeamTerminal(waitCtx, "team-auto"); err != nil {
		t.Fatalf("waitForTeamTerminal: %v", err)
	}

	cleanupDeadline := time.Now().Add(2 * time.Second)
	for {
		_, actorExists := host.SessionHub.Get(teammateSessionID)
		teammateSession, loadErr := manager.Get(context.Background(), teammateSessionID)
		if !actorExists && loadErr == nil && teammateSession != nil && teammateSession.State == runtimechat.StateClosed {
			break
		}
		if time.Now().After(cleanupDeadline) {
			state := "<nil>"
			if teammateSession != nil {
				state = string(teammateSession.State)
			}
			t.Fatalf("expected teammate cleanup after terminal state: actorExists=%v sessionState=%s loadErr=%v", actorExists, state, loadErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, ok := host.SessionHub.Get(runtimeSession.ID); !ok {
		t.Fatal("expected lead actor to remain after teammate cleanup")
	}
	leadSession, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get(lead): %v", err)
	}
	if leadSession.State == runtimechat.StateClosed {
		t.Fatalf("expected lead session to remain open, got %s", leadSession.State)
	}

	teammates, err = teamStore.ListTeammates(context.Background(), "team-auto")
	if err != nil {
		t.Fatalf("ListTeammates after cleanup: %v", err)
	}
	if len(teammates) != 1 {
		t.Fatalf("expected teammate record to remain for summary stats, got %+v", teammates)
	}
	if teammates[0].State != team.TeammateStateIdle {
		t.Fatalf("expected teammate state to remain idle after cleanup, got %+v", teammates[0])
	}
}

func TestAICLIChatActorExecutor_FailedAutoStartTeamClosesNonLeadTeammateSessionAfterTerminal(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	provider := &failedAutoStartLocalOrchestrationProvider{teammateDelay: 150 * time.Millisecond}
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

	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	session := &ChatSession{
		ProviderName:     "test-provider",
		PermissionMode:   runtimepolicy.ModeDefault,
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host.BaseSession = session

	response, err := session.ChatExecutor.Execute(context.Background(), session, "Create a failed auto-start team and let the planner fail the task")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(response, "team-failed") {
		t.Fatalf("expected spawn response, got %q", response)
	}

	teammates, err := teamStore.ListTeammates(context.Background(), "team-failed")
	if err != nil {
		t.Fatalf("ListTeammates: %v", err)
	}
	if len(teammates) != 1 {
		t.Fatalf("expected exactly one teammate, got %+v", teammates)
	}
	teammate := teammates[0]
	if strings.TrimSpace(teammate.SessionID) == "" {
		t.Fatalf("expected teammate session id, got %+v", teammate)
	}
	teammateSessionID := strings.TrimSpace(teammate.SessionID)

	startDeadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := host.SessionHub.Get(teammateSessionID); ok {
			break
		}
		if time.Now().After(startDeadline) {
			t.Fatalf("expected teammate actor %q to be created before failed terminal cleanup", teammateSessionID)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := host.SessionHub.Get(runtimeSession.ID); !ok {
		t.Fatalf("expected lead actor %q to remain registered", runtimeSession.ID)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := host.waitForTeamTerminal(waitCtx, "team-failed"); err != nil {
		t.Fatalf("waitForTeamTerminal: %v", err)
	}

	cleanupDeadline := time.Now().Add(2 * time.Second)
	for {
		_, actorExists := host.SessionHub.Get(teammateSessionID)
		teammateSession, loadErr := manager.Get(context.Background(), teammateSessionID)
		if !actorExists && loadErr == nil && teammateSession != nil && teammateSession.State == runtimechat.StateClosed {
			break
		}
		if time.Now().After(cleanupDeadline) {
			state := "<nil>"
			if teammateSession != nil {
				state = string(teammateSession.State)
			}
			t.Fatalf("expected teammate cleanup after failed terminal state: actorExists=%v sessionState=%s loadErr=%v", actorExists, state, loadErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	teamRecord, err := teamStore.GetTeam(context.Background(), "team-failed")
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if teamRecord == nil || teamRecord.Status != team.TeamStatusFailed {
		t.Fatalf("expected failed team terminal state, got %+v", teamRecord)
	}

	task, err := teamStore.GetTask(context.Background(), "task-failed")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task == nil || task.Status != team.TaskStatusFailed {
		t.Fatalf("expected failed task, got %+v", task)
	}
	if strings.TrimSpace(task.Summary) != "Could not complete the task." {
		t.Fatalf("expected failed task summary, got %+v", task)
	}

	if _, ok := host.SessionHub.Get(runtimeSession.ID); !ok {
		t.Fatal("expected lead actor to remain after failed teammate cleanup")
	}
	leadSession, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get(lead): %v", err)
	}
	if leadSession.State == runtimechat.StateClosed {
		t.Fatalf("expected lead session to remain open, got %s", leadSession.State)
	}

	teammates, err = teamStore.ListTeammates(context.Background(), "team-failed")
	if err != nil {
		t.Fatalf("ListTeammates after cleanup: %v", err)
	}
	if len(teammates) != 1 {
		t.Fatalf("expected teammate record to remain for summary stats, got %+v", teammates)
	}
	if teammates[0].State != team.TeammateStateIdle {
		t.Fatalf("expected teammate state to remain idle after failed cleanup, got %+v", teammates[0])
	}

	events, err := runtimeSessionEvents(runtimeStoreFromHost(host), runtimeSession.ID)
	if err != nil {
		t.Fatalf("runtimeSessionEvents: %v", err)
	}
	for _, event := range events {
		if event.Type == "team.summary" {
			t.Fatalf("expected no team.summary for failed terminal team, got %+v", events)
		}
	}
}

func containsAllChatTimelineLines(lines []string, expected ...string) bool {
	for _, want := range expected {
		found := false
		for _, line := range lines {
			if strings.TrimSpace(line) == strings.TrimSpace(want) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func containsChatTimelinePrefix(lines []string, prefix string) bool {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return false
	}
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return true
		}
	}
	return false
}

func runtimeStoreFromHost(host *localChatRuntimeHost) runtimechat.RuntimeStateStore {
	if host == nil {
		return nil
	}
	return host.RuntimeStore
}

func runtimeSessionEvents(store runtimechat.RuntimeStateStore, sessionID string) ([]runtimeevents.Event, error) {
	eventStore, ok := store.(runtimechat.EventStore)
	if !ok || eventStore == nil {
		return nil, nil
	}
	return eventStore.ListEvents(context.Background(), sessionID, 0, 128)
}

func newLocalOrchestrationTestHost(t *testing.T, manager *runtimechat.SessionManager, userID string, llmRuntime *runtimellm.LLMRuntime, teamStore team.Store) *localChatRuntimeHost {
	t.Helper()

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(64),
		RuntimeStore: runtimeStore,
		EventStore:   runtimeStore,
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
		TeamStore:    teamStore,
		TeamClaims:   team.NewPathClaimManager(teamStore, t.TempDir()),
	}
	host.TeamLifecycle = newLocalTeamLifecycleService(host)
	host.ActorRegistry = newLocalActorRegistry(host)
	host.Orchestrator = team.NewOrchestrator(host.TeamStore, host.TeamClaims, nil)
	if host.Orchestrator != nil {
		mailbox := team.NewMailboxService(host.TeamStore)
		host.Orchestrator.Mailbox = mailbox
		host.Orchestrator.Dispatcher = host.ActorRegistry
		host.Orchestrator.Runner = &team.TeammateRunner{
			Sessions: host.ActorRegistry,
			Mailbox:  mailbox,
			Context:  team.NewContextBuilder(teamStore),
		}
		host.Orchestrator.LeadPlanner = &team.LeadPlanner{
			Sessions:    host.ActorRegistry,
			Store:       teamStore,
			Mailbox:     mailbox,
			AutoPersist: true,
		}
	}
	host.bindTeamLifecycleEvents()
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "local-orchestration-test",
			Provider: "test-provider",
			Model:    "test-model",
			MaxSteps: 4,
		}, nil, llmRuntime)
		a.SetToolExecutionPolicy(runtimepolicy.NewToolExecutionPolicy(nil, false))
		broker := &toolbroker.Broker{
			TeamStore:            host.TeamStore,
			TeamClaims:           host.TeamClaims,
			TeamDispatcher:       host.ActorRegistry,
			TeamLifecycleChanged: host.syncTeamLifecycleLoops,
		}
		if host.Orchestrator != nil {
			broker.TeamPlanner = host.Orchestrator.LeadPlanner
		}
		a.SetToolBroker(broker)
		if ctxMgr := a.GetContextManager(); ctxMgr != nil {
			ctxMgr.TeamContext = team.NewContextBuilder(teamStore)
		}
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: manager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})
	return host
}

type localOrchestrationProvider struct{}

func (p *localOrchestrationProvider) Name() string { return "test-provider" }

func (p *localOrchestrationProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		message := req.Messages[i]
		switch message.Role {
		case "tool":
			switch strings.TrimSpace(message.ToolCallID) {
			case "call-spawn":
				return &runtimellm.LLMResponse{Content: "Team created", Model: req.Model}, nil
			case "call-read":
				return &runtimellm.LLMResponse{
					Content: message.Metadata.GetString("task_id", message.Content),
					Model:   req.Model,
				}, nil
			}
		case "user":
			lastUser := message.Content
			switch {
			case strings.Contains(lastUser, "Create a team"):
				return &runtimellm.LLMResponse{
					Model: req.Model,
					ToolCalls: []runtimetypes.ToolCall{
						{
							ID:   "call-spawn",
							Name: toolbroker.ToolSpawnTeam,
							Args: map[string]interface{}{
								"team_id":    "team-1",
								"auto_start": false,
								"tasks": []interface{}{
									map[string]interface{}{
										"id":    "task-1",
										"title": "Write summary",
										"goal":  "Write summary",
									},
								},
							},
						},
					},
				}, nil
			case strings.Contains(lastUser, "Read the current task spec"):
				return &runtimellm.LLMResponse{
					Model: req.Model,
					ToolCalls: []runtimetypes.ToolCall{
						{
							ID:   "call-read",
							Name: toolbroker.ToolReadTaskSpec,
							Args: map[string]interface{}{},
						},
					},
				}, nil
			default:
				return &runtimellm.LLMResponse{Content: "no-op", Model: req.Model}, nil
			}
		}
	}
	return &runtimellm.LLMResponse{Content: "no-op", Model: req.Model}, nil
}

func (p *localOrchestrationProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (p *localOrchestrationProvider) CountTokens(text string) int { return len(text) }

func (p *localOrchestrationProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{SupportsTools: true}
}

func (p *localOrchestrationProvider) CheckHealth(ctx context.Context) error { return nil }

type autoStartLocalOrchestrationProvider struct {
	teammateDelay time.Duration
}

func (p *autoStartLocalOrchestrationProvider) Name() string { return "test-provider" }

func (p *autoStartLocalOrchestrationProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		message := req.Messages[i]
		switch message.Role {
		case "tool":
			switch strings.TrimSpace(message.ToolCallID) {
			case "call-spawn-auto":
				return &runtimellm.LLMResponse{Content: "team-auto task-auto", Model: req.Model}, nil
			case "call-report-auto":
				return &runtimellm.LLMResponse{
					Content: "Completed without file changes.\n\n```json\n{\"task_status\":\"done\",\"summary\":\"auto smoke finished\"}\n```",
					Model:   req.Model,
				}, nil
			}
		case "user":
			lastUser := message.Content
			switch {
			case strings.Contains(lastUser, "Create an auto-start team"):
				return &runtimellm.LLMResponse{
					Model: req.Model,
					ToolCalls: []runtimetypes.ToolCall{
						{
							ID:   "call-spawn-auto",
							Name: toolbroker.ToolSpawnTeam,
							Args: map[string]interface{}{
								"team_id":    "team-auto",
								"auto_start": true,
								"teammates": []interface{}{
									map[string]interface{}{
										"id":      "planner",
										"name":    "Planner",
										"profile": "planner",
									},
								},
								"tasks": []interface{}{
									map[string]interface{}{
										"id":           "task-auto",
										"title":        "Auto task",
										"goal":         "Complete automatically",
										"assignee":     "planner",
										"deliverables": []interface{}{"summary"},
									},
								},
							},
						},
					},
				}, nil
			case strings.Contains(lastUser, "You are teammate"):
				if p.teammateDelay > 0 {
					time.Sleep(p.teammateDelay)
				}
				return &runtimellm.LLMResponse{
					Model: req.Model,
					ToolCalls: []runtimetypes.ToolCall{
						{
							ID:   "call-report-auto",
							Name: toolbroker.ToolReportTaskOutcome,
							Args: map[string]interface{}{
								"task_status": "done",
								"summary":     "auto smoke finished",
							},
						},
					},
				}, nil
			case strings.Contains(lastUser, "You are the team lead. Provide a concise final summary"):
				return &runtimellm.LLMResponse{
					Content: "auto lead summary",
					Model:   req.Model,
				}, nil
			default:
				return &runtimellm.LLMResponse{Content: "no-op", Model: req.Model}, nil
			}
		}
	}
	return &runtimellm.LLMResponse{Content: "no-op", Model: req.Model}, nil
}

func (p *autoStartLocalOrchestrationProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (p *autoStartLocalOrchestrationProvider) CountTokens(text string) int { return len(text) }

func (p *autoStartLocalOrchestrationProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{SupportsTools: true}
}

func (p *autoStartLocalOrchestrationProvider) CheckHealth(ctx context.Context) error { return nil }

type emptyReplyAfterSpawnTeamProvider struct{}

func (p *emptyReplyAfterSpawnTeamProvider) Name() string { return "test-provider" }

func (p *emptyReplyAfterSpawnTeamProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		message := req.Messages[i]
		switch message.Role {
		case "tool":
			if strings.TrimSpace(message.ToolCallID) == "call-spawn-empty" {
				return &runtimellm.LLMResponse{Model: req.Model}, nil
			}
		case "user":
			if strings.Contains(message.Content, "Create a failing auto-start team") {
				return &runtimellm.LLMResponse{
					Model: req.Model,
					ToolCalls: []runtimetypes.ToolCall{
						{
							ID:   "call-spawn-empty",
							Name: toolbroker.ToolSpawnTeam,
							Args: map[string]interface{}{
								"team_id":    "team-empty",
								"auto_start": true,
								"teammates": []interface{}{
									map[string]interface{}{
										"id":      "planner",
										"name":    "Planner",
										"profile": "planner",
									},
								},
								"tasks": []interface{}{
									map[string]interface{}{
										"id":           "task-empty",
										"title":        "Empty task",
										"goal":         "Finish in background after empty lead reply",
										"assignee":     "planner",
										"deliverables": []interface{}{"summary"},
									},
								},
							},
						},
					},
				}, nil
			}
			if strings.Contains(message.Content, "You are teammate") {
				return &runtimellm.LLMResponse{
					Model: req.Model,
					ToolCalls: []runtimetypes.ToolCall{
						{
							ID:   "call-report-empty",
							Name: toolbroker.ToolReportTaskOutcome,
							Args: map[string]interface{}{
								"task_status": "done",
								"summary":     "empty reply teammate finished",
							},
						},
					},
				}, nil
			}
			if strings.Contains(message.Content, "You are the team lead. Provide a concise final summary") {
				return &runtimellm.LLMResponse{
					Content: "empty reply lead summary",
					Model:   req.Model,
				}, nil
			}
		}
	}
	return &runtimellm.LLMResponse{Content: "no-op", Model: req.Model}, nil
}

func (p *emptyReplyAfterSpawnTeamProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (p *emptyReplyAfterSpawnTeamProvider) CountTokens(text string) int { return len(text) }

func (p *emptyReplyAfterSpawnTeamProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{SupportsTools: true}
}

func (p *emptyReplyAfterSpawnTeamProvider) CheckHealth(ctx context.Context) error { return nil }

type failedAutoStartLocalOrchestrationProvider struct {
	teammateDelay time.Duration
}

func (p *failedAutoStartLocalOrchestrationProvider) Name() string { return "test-provider" }

func (p *failedAutoStartLocalOrchestrationProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		message := req.Messages[i]
		switch message.Role {
		case "tool":
			switch strings.TrimSpace(message.ToolCallID) {
			case "call-spawn-failed":
				return &runtimellm.LLMResponse{Content: "team-failed task-failed", Model: req.Model}, nil
			case "call-report-failed":
				return &runtimellm.LLMResponse{
					Content: "Could not complete the task.\n\n```json\n{\"task_status\":\"failed\",\"summary\":\"auto smoke failed\"}\n```",
					Model:   req.Model,
				}, nil
			}
		case "user":
			lastUser := message.Content
			switch {
			case strings.Contains(lastUser, "Create a failed auto-start team"):
				return &runtimellm.LLMResponse{
					Model: req.Model,
					ToolCalls: []runtimetypes.ToolCall{
						{
							ID:   "call-spawn-failed",
							Name: toolbroker.ToolSpawnTeam,
							Args: map[string]interface{}{
								"team_id":    "team-failed",
								"auto_start": true,
								"teammates": []interface{}{
									map[string]interface{}{
										"id":      "planner",
										"name":    "Planner",
										"profile": "planner",
									},
								},
								"tasks": []interface{}{
									map[string]interface{}{
										"id":           "task-failed",
										"title":        "Failed task",
										"goal":         "Fail automatically",
										"assignee":     "planner",
										"deliverables": []interface{}{"failure summary"},
									},
								},
							},
						},
					},
				}, nil
			case strings.Contains(lastUser, "You are teammate"):
				if p.teammateDelay > 0 {
					time.Sleep(p.teammateDelay)
				}
				return &runtimellm.LLMResponse{
					Model: req.Model,
					ToolCalls: []runtimetypes.ToolCall{
						{
							ID:   "call-report-failed",
							Name: toolbroker.ToolReportTaskOutcome,
							Args: map[string]interface{}{
								"task_status": "failed",
								"summary":     "auto smoke failed",
							},
						},
					},
				}, nil
			default:
				return &runtimellm.LLMResponse{Content: "no-op", Model: req.Model}, nil
			}
		}
	}
	return &runtimellm.LLMResponse{Content: "no-op", Model: req.Model}, nil
}

func (p *failedAutoStartLocalOrchestrationProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (p *failedAutoStartLocalOrchestrationProvider) CountTokens(text string) int { return len(text) }

func (p *failedAutoStartLocalOrchestrationProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{SupportsTools: true}
}

func (p *failedAutoStartLocalOrchestrationProvider) CheckHealth(ctx context.Context) error {
	return nil
}

type serialAutoStartLocalOrchestrationProvider struct {
	teammateDelay time.Duration

	mu         sync.Mutex
	taskCounts map[string]int
}

func (p *serialAutoStartLocalOrchestrationProvider) Name() string { return "test-provider" }

func (p *serialAutoStartLocalOrchestrationProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		message := req.Messages[i]
		switch message.Role {
		case "tool":
			switch strings.TrimSpace(message.ToolCallID) {
			case "call-spawn-serial":
				return &runtimellm.LLMResponse{Content: "team-serial task-serial-1 task-serial-2", Model: req.Model}, nil
			case "call-report-serial":
				return &runtimellm.LLMResponse{
					Content: "Queued serial task finished.\n\n```json\n{\"task_status\":\"done\",\"summary\":\"serial queued task finished\"}\n```",
					Model:   req.Model,
				}, nil
			}
		case "user":
			lastUser := message.Content
			switch {
			case strings.Contains(lastUser, "Create a serial auto-start team"):
				return &runtimellm.LLMResponse{
					Model: req.Model,
					ToolCalls: []runtimetypes.ToolCall{
						{
							ID:   "call-spawn-serial",
							Name: toolbroker.ToolSpawnTeam,
							Args: map[string]interface{}{
								"team_id":    "team-serial",
								"auto_start": true,
								"teammates": []interface{}{
									map[string]interface{}{
										"id":      "planner",
										"name":    "Planner",
										"profile": "planner",
									},
								},
								"tasks": []interface{}{
									map[string]interface{}{
										"id":           "task-serial-1",
										"title":        "Serial task 1",
										"goal":         "Finish the first queued planner task",
										"assignee":     "planner",
										"deliverables": []interface{}{"summary"},
									},
									map[string]interface{}{
										"id":           "task-serial-2",
										"title":        "Serial task 2",
										"goal":         "Finish the second queued planner task after task 1",
										"assignee":     "planner",
										"deliverables": []interface{}{"summary"},
									},
								},
							},
						},
					},
				}, nil
			case strings.Contains(lastUser, "You are teammate"):
				taskID := serialTaskIDFromPrompt(lastUser)
				if taskID == "" {
					taskID = "unknown"
				}
				p.recordTaskCall(taskID)
				if p.teammateDelay > 0 {
					time.Sleep(p.teammateDelay)
				}
				summary := "serial queued task finished"
				switch taskID {
				case "task-serial-1":
					summary = "serial task 1 done"
				case "task-serial-2":
					summary = "serial task 2 done"
				}
				return &runtimellm.LLMResponse{
					Model: req.Model,
					ToolCalls: []runtimetypes.ToolCall{
						{
							ID:   "call-report-serial",
							Name: toolbroker.ToolReportTaskOutcome,
							Args: map[string]interface{}{
								"task_status": "done",
								"summary":     summary,
							},
						},
					},
				}, nil
			case strings.Contains(lastUser, "You are the team lead. Provide a concise final summary"):
				return &runtimellm.LLMResponse{
					Content: "serial lead summary",
					Model:   req.Model,
				}, nil
			default:
				return &runtimellm.LLMResponse{Content: "no-op", Model: req.Model}, nil
			}
		}
	}
	return &runtimellm.LLMResponse{Content: "no-op", Model: req.Model}, nil
}

func (p *serialAutoStartLocalOrchestrationProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (p *serialAutoStartLocalOrchestrationProvider) CountTokens(text string) int { return len(text) }

func (p *serialAutoStartLocalOrchestrationProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{SupportsTools: true}
}

func (p *serialAutoStartLocalOrchestrationProvider) CheckHealth(ctx context.Context) error {
	return nil
}

func (p *serialAutoStartLocalOrchestrationProvider) recordTaskCall(taskID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.taskCounts == nil {
		p.taskCounts = make(map[string]int)
	}
	p.taskCounts[strings.TrimSpace(taskID)]++
}

func (p *serialAutoStartLocalOrchestrationProvider) callCount(taskID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.taskCounts == nil {
		return 0
	}
	return p.taskCounts[strings.TrimSpace(taskID)]
}

func serialTaskIDFromPrompt(prompt string) string {
	for _, candidate := range []string{"task-serial-1", "task-serial-2"} {
		if strings.Contains(prompt, candidate) {
			return candidate
		}
	}
	return ""
}
