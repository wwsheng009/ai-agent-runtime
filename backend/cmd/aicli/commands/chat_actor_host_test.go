package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestAcquireLocalChatSessionLeaseReclaimsStoppedLocalOwner(t *testing.T) {
	deadPID := 1 << 30
	if running, known := localChatProcessRunning(deadPID); !known || running {
		t.Skipf("cannot establish a stopped test PID: running=%v known=%v", running, known)
	}

	store := runtimechat.NewInMemoryRuntimeStore(32)
	ctx := context.Background()
	sessionID := "stale-local-session"
	_, err := store.AcquireLease(ctx, runtimechat.LeaseRequest{
		SessionID: sessionID,
		OwnerID:   "aicli-actor:" + localChatHostname() + ":stale",
		OwnerKind: localChatSessionActorLeaseOwnerKind,
		PID:       deadPID,
		Hostname:  localChatHostname(),
		TTL:       time.Minute,
	})
	if err != nil {
		t.Fatalf("seed stale lease: %v", err)
	}

	handle, err := acquireLocalChatSessionLease(ctx, store, sessionID)
	if err != nil {
		t.Fatalf("acquire after stopped owner: %v", err)
	}
	defer handle.Release(ctx)
	if lease := handle.Lease(); lease == nil || lease.OwnerID == "" || lease.OwnerID == "aicli-actor:"+localChatHostname()+":stale" {
		t.Fatalf("expected current process to own reclaimed lease, got %#v", lease)
	}
}

func TestAcquireLocalChatSessionLeasePreservesLiveConflict(t *testing.T) {
	store := runtimechat.NewInMemoryRuntimeStore(32)
	ctx := context.Background()
	sessionID := "live-local-session"
	_, err := store.AcquireLease(ctx, runtimechat.LeaseRequest{
		SessionID: sessionID,
		OwnerID:   "aicli-actor:" + localChatHostname() + ":live",
		OwnerKind: localChatSessionActorLeaseOwnerKind,
		PID:       os.Getpid(),
		Hostname:  localChatHostname(),
		TTL:       time.Minute,
	})
	if err != nil {
		t.Fatalf("seed live lease: %v", err)
	}

	_, err = acquireLocalChatSessionLease(ctx, store, sessionID)
	var conflict *runtimechat.LeaseConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected live owner conflict, got %v", err)
	}
}

func TestLocalChatSessionLeaseOwnerStoppedRequiresLocalAICLIOwner(t *testing.T) {
	deadPID := 1 << 30
	if running, known := localChatProcessRunning(deadPID); !known || running {
		t.Skipf("cannot establish a stopped test PID: running=%v known=%v", running, known)
	}
	base := runtimechat.SessionLease{
		SessionID:   "guarded-stale-session",
		OwnerID:     "stale-owner",
		OwnerKind:   localChatSessionActorLeaseOwnerKind,
		PID:         deadPID,
		Hostname:    localChatHostname(),
		ExpiresAt:   time.Now().Add(time.Minute),
		HeartbeatAt: time.Now(),
	}
	if !localChatSessionLeaseOwnerStopped(&base) {
		t.Fatal("expected stopped local aicli owner to be reclaimable")
	}
	remote := base
	remote.Hostname = "remote-host.example"
	if localChatSessionLeaseOwnerStopped(&remote) {
		t.Fatal("remote lease owner must not be reclaimed using a local PID lookup")
	}
	otherKind := base
	otherKind.OwnerKind = "runtime-server-actor"
	if localChatSessionLeaseOwnerStopped(&otherKind) {
		t.Fatal("non-aicli lease owner must not be reclaimed by the local CLI")
	}
}

type capturingLocalChatProvider struct {
	name      string
	responses []*runtimellm.LLMResponse
	requests  []*runtimellm.LLMRequest
	callCount int
}

func (p *capturingLocalChatProvider) Name() string {
	return p.name
}

func (p *capturingLocalChatProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	if req != nil {
		p.requests = append(p.requests, &runtimellm.LLMRequest{
			Provider:        req.Provider,
			Model:           req.Model,
			ReasoningEffort: req.ReasoningEffort,
		})
	}
	if p.callCount >= len(p.responses) {
		return &runtimellm.LLMResponse{Content: "ok", Model: p.name}, nil
	}
	response := p.responses[p.callCount]
	p.callCount++
	return response, nil
}

func (p *capturingLocalChatProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	return nil, nil
}

func (p *capturingLocalChatProvider) CountTokens(text string) int {
	return len(text) / 4
}

func (p *capturingLocalChatProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{
		MaxContextTokens: 128000,
		SupportsTools:    true,
	}
}

func (p *capturingLocalChatProvider) CheckHealth(ctx context.Context) error {
	return nil
}

func TestLocalChatRuntimeHost_MirrorsTeamSummaryIntoBaseSession(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	provider := &autoStartLocalOrchestrationProvider{teammateDelay: 50 * time.Millisecond}
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
		RequestTimeout:   10 * time.Second,
	}
	host.BaseSession = session

	_, err = session.ChatExecutor.Execute(context.Background(), session, "Create an auto-start team and let the planner finish the task")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := host.waitForTeamTerminal(waitCtx, "team-auto"); err != nil {
		t.Fatalf("waitForTeamTerminal: %v", err)
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
		t.Fatalf("expected mirrored team summary in base session history, got %+v", reloaded.History)
	}
}

func TestLocalTeamLifecycleService_RunSettledWaitsForDoneSummaryEvent(t *testing.T) {
	ctx := context.Background()
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	teamID, err := teamStore.CreateTeam(ctx, team.Team{ID: "summary-wait-team"})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if err := teamStore.UpdateTeamStatus(ctx, teamID, team.TeamStatusDone); err != nil {
		t.Fatalf("UpdateTeamStatus: %v", err)
	}

	host := &localChatRuntimeHost{
		TeamStore: teamStore,
		Orchestrator: &team.Orchestrator{
			LeadPlanner: &team.LeadPlanner{},
		},
	}
	lifecycle := newLocalTeamLifecycleService(host)

	settled, err := lifecycle.RunSettled(ctx, teamID)
	if err != nil {
		t.Fatalf("RunSettled without summary: %v", err)
	}
	if settled {
		t.Fatal("expected done team with configured lead planner to wait for team.summary event")
	}

	if _, err := teamStore.AppendTeamEvent(ctx, team.TeamEvent{
		Type:   "team.summary",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"summary": "done",
		},
	}); err != nil {
		t.Fatalf("AppendTeamEvent: %v", err)
	}

	settled, err = lifecycle.RunSettled(ctx, teamID)
	if err != nil {
		t.Fatalf("RunSettled with summary: %v", err)
	}
	if !settled {
		t.Fatal("expected done team to settle after team.summary event is persisted")
	}
}

func TestLocalChatRuntimeHost_TeamLifecyclePersistsParentMailbox(t *testing.T) {
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	host := &localChatRuntimeHost{
		EventStore: runtimeStore,
		EventBus:   runtimeevents.NewBusWithRetention(16),
		BaseSession: &ChatSession{
			RuntimeSession: &runtimechat.Session{ID: "root-session"},
		},
	}

	host.dispatchTeamLifecycleEvent(team.TeamEvent{
		Type:   "team.completed",
		TeamID: "team-1",
		Payload: map[string]interface{}{
			"status": "done",
		},
	}, true)

	events, err := runtimeStore.ListEvents(context.Background(), "root-session", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected team completed event and mailbox mirror, got %#v", events)
	}
	if events[0].Type != "team.completed" || events[0].Payload["status"] != "done" {
		t.Fatalf("unexpected lifecycle event: %#v", events[0])
	}
	if events[1].Type != runtimechat.EventMailboxReceived || events[1].Payload["kind"] != team.TeamLifecycleMailboxKind {
		t.Fatalf("unexpected lifecycle mailbox event: %#v", events[1])
	}
	messages, err := runtimeStore.ListMailbox(context.Background(), "root-session", 0, 10)
	if err != nil {
		t.Fatalf("ListMailbox: %v", err)
	}
	if len(messages) != 1 || messages[0].Kind != team.TeamLifecycleMailboxKind || messages[0].Metadata["event_type"] != "team.completed" {
		t.Fatalf("unexpected lifecycle mailbox substrate rows: %#v", messages)
	}
	if messages[0].Metadata["message_type"] != team.TeamLifecycleControlMessageType || messages[0].Metadata["status"] != "done" {
		t.Fatalf("unexpected lifecycle mailbox metadata: %#v", messages[0].Metadata)
	}
	controlMessages, err := runtimeStore.ListAgentControlMailbox(context.Background(), "root-session", 0, 10)
	if err != nil {
		t.Fatalf("ListAgentControlMailbox: %v", err)
	}
	if len(controlMessages) != 1 || controlMessages[0].Kind != team.TeamLifecycleMailboxKind || controlMessages[0].Metadata["message_type"] != team.TeamLifecycleControlMessageType {
		t.Fatalf("unexpected lifecycle agent-control rows: %#v", controlMessages)
	}
}

func TestConfigureLocalChatMailboxWriteThroughWritesRuntimeAndTeamRows(t *testing.T) {
	ctx := context.Background()
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()
	globalStore, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "agent_control_mailbox.sqlite"),
	})
	if err != nil {
		t.Fatalf("NewSQLiteGlobalMailboxRegistryStore: %v", err)
	}
	defer globalStore.Close()
	configureLocalChatMailboxWriteThrough(globalStore, runtimeStore, teamStore)

	if _, _, err := runtimeStore.AppendAgentControlMailbox(ctx, "root-session", team.MailMessage{
		ID:        "runtime-control",
		FromAgent: "child",
		ToAgent:   "root",
		Kind:      agentcontrol.MailboxKindSubagentCompleted,
		Body:      "done",
		Metadata: agentcontrol.Envelope{
			MessageType:     agentcontrol.MessageTypeSubagentCompleted,
			ControlAction:   agentcontrol.ActionAgentCompleted,
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			MailboxDelivery: agentcontrol.DeliverySessionMailbox,
			MailboxKind:     agentcontrol.MailboxKindSubagentCompleted,
		}.Metadata(),
	}); err != nil {
		t.Fatalf("AppendAgentControlMailbox: %v", err)
	}
	teamID, err := teamStore.CreateTeam(ctx, team.Team{ID: "team-1"})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := teamStore.InsertMail(ctx, team.MailMessage{
		ID:        "team-control",
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate",
		Kind:      agentcontrol.MailboxKindTeamTaskLifecycle,
		Body:      "team body",
	}); err != nil {
		t.Fatalf("InsertMail: %v", err)
	}

	runtimeRows, err := globalStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Workflow:  agentcontrol.WorkflowSpawnAgent,
		SessionID: "root-session",
	})
	if err != nil {
		t.Fatalf("List runtime global mailbox: %v", err)
	}
	if len(runtimeRows) != 1 || runtimeRows[0].Source != agentcontrol.MailboxSourceGlobal || runtimeRows[0].MessageID != "runtime-control" {
		t.Fatalf("unexpected runtime global mailbox rows: %#v", runtimeRows)
	}
	teamRows, err := globalStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
	})
	if err != nil {
		t.Fatalf("List team global mailbox: %v", err)
	}
	if len(teamRows) != 1 || teamRows[0].Source != agentcontrol.MailboxSourceGlobal || teamRows[0].MessageID != "team-control" {
		t.Fatalf("unexpected team global mailbox rows: %#v", teamRows)
	}
}

func TestConfigureLocalChatMailboxWriteThroughReconcilesExistingProjections(t *testing.T) {
	ctx := context.Background()
	runtimeStore, err := runtimechat.NewSQLiteRuntimeStore(&runtimechat.RuntimeStoreConfig{
		Path: filepath.Join(t.TempDir(), "runtime.db"),
	})
	if err != nil {
		t.Fatalf("NewSQLiteRuntimeStore: %v", err)
	}
	defer runtimeStore.Close()
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	if _, _, err := runtimeStore.AppendAgentControlMailbox(ctx, "local-runtime-session", team.MailMessage{
		ID:        "runtime-before-global",
		FromAgent: "child",
		ToAgent:   "root",
		Kind:      agentcontrol.MailboxKindSubagentCompleted,
		Body:      "runtime local first",
		Metadata: agentcontrol.Envelope{
			MessageType:     agentcontrol.MessageTypeSubagentCompleted,
			ControlAction:   agentcontrol.ActionAgentCompleted,
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			MailboxDelivery: agentcontrol.DeliverySessionMailbox,
			MailboxKind:     agentcontrol.MailboxKindSubagentCompleted,
		}.Metadata(),
	}); err != nil {
		t.Fatalf("AppendAgentControlMailbox: %v", err)
	}
	localTeamID, err := teamStore.CreateTeam(ctx, team.Team{ID: "local-team"})
	if err != nil {
		t.Fatalf("Create local team: %v", err)
	}
	if _, err := teamStore.InsertMail(ctx, team.MailMessage{
		ID:        "team-before-global",
		TeamID:    localTeamID,
		FromAgent: "lead",
		ToAgent:   "mate",
		Kind:      agentcontrol.MailboxKindTeamTaskLifecycle,
		Body:      "team local first",
	}); err != nil {
		t.Fatalf("InsertMail local first: %v", err)
	}
	globalTeamID, err := teamStore.CreateTeam(ctx, team.Team{ID: "global-team"})
	if err != nil {
		t.Fatalf("Create global team: %v", err)
	}

	globalStore, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "agent_control_mailbox.sqlite"),
	})
	if err != nil {
		t.Fatalf("NewSQLiteGlobalMailboxRegistryStore: %v", err)
	}
	defer globalStore.Close()
	runtimeGlobal, err := globalStore.AppendPrimaryGlobalMailboxRecord(ctx, agentcontrol.MailboxRecord{
		Workflow:  agentcontrol.WorkflowSpawnAgent,
		Scope:     agentcontrol.MailboxScopeSession,
		SessionID: "global-runtime-session",
		MessageID: "runtime-global-only",
		Kind:      agentcontrol.MailboxKindAgentMessage,
		Body:      "runtime global first",
		Metadata: agentcontrol.Envelope{
			MessageType:     agentcontrol.MessageTypeAgentMessage,
			ControlAction:   agentcontrol.ActionAgentMessage,
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			MailboxDelivery: agentcontrol.DeliverySessionMailbox,
			MailboxKind:     agentcontrol.MailboxKindAgentMessage,
		}.Metadata(),
		CreatedAt: time.Unix(40, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Append primary runtime global: %v", err)
	}
	teamGlobal, err := globalStore.AppendPrimaryGlobalMailboxRecord(ctx, agentcontrol.MailboxRecord{
		Workflow:  agentcontrol.WorkflowSpawnTeam,
		Scope:     agentcontrol.MailboxScopeTeam,
		TeamID:    globalTeamID,
		MessageID: "team-global-only",
		Kind:      agentcontrol.MailboxKindTeamTaskLifecycle,
		Body:      "team global first",
		CreatedAt: time.Unix(41, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Append primary team global: %v", err)
	}

	configureLocalChatMailboxWriteThrough(globalStore, runtimeStore, teamStore)

	deadline := time.Now().Add(3 * time.Second)
	for {
		runtimeRows, _ := globalStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
			Workflow:  agentcontrol.WorkflowSpawnAgent,
			SessionID: "local-runtime-session",
		})
		teamRows, _ := globalStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
			Workflow: agentcontrol.WorkflowSpawnTeam,
			TeamID:   localTeamID,
		})
		runtimeLocal, _ := runtimeStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
			Workflow:  agentcontrol.WorkflowSpawnAgent,
			SessionID: "global-runtime-session",
		})
		teamLocal, _ := teamStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
			Workflow: agentcontrol.WorkflowSpawnTeam,
			TeamID:   globalTeamID,
		})
		if len(runtimeRows) == 1 && len(teamRows) == 1 &&
			len(runtimeLocal) == 1 && runtimeLocal[0].GlobalSeq == runtimeGlobal.Seq &&
			len(teamLocal) == 1 && teamLocal[0].GlobalSeq == teamGlobal.Seq {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for mailbox projection reconcile: runtimeRows=%#v teamRows=%#v runtimeLocal=%#v teamLocal=%#v", runtimeRows, teamRows, runtimeLocal, teamLocal)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLocalChatRuntimeHost_TaskLifecyclePersistsTeammateMailbox(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-1",
		LeadSessionID: "root-session",
		Status:        team.TeamStatusActive,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.UpsertTeammate(context.Background(), team.Teammate{
		ID:        "mate-1",
		TeamID:    "team-1",
		SessionID: "mate-session",
		State:     team.TeammateStateBusy,
	}); err != nil {
		t.Fatalf("UpsertTeammate: %v", err)
	}

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	host := &localChatRuntimeHost{
		EventStore: runtimeStore,
		TeamStore:  store,
		BaseSession: &ChatSession{
			RuntimeSession: &runtimechat.Session{ID: "root-session"},
		},
	}

	host.dispatchTeamLifecycleEvent(team.TeamEvent{
		Type:   "task.completed",
		TeamID: "team-1",
		Payload: map[string]interface{}{
			"task_id":  "task-1",
			"assignee": "mate-1",
			"summary":  "done",
		},
	}, true)

	parentMessages, err := runtimeStore.ListMailbox(context.Background(), "root-session", 0, 10)
	if err != nil {
		t.Fatalf("ListMailbox parent: %v", err)
	}
	if len(parentMessages) != 0 {
		t.Fatalf("expected task lifecycle not to mirror into parent mailbox, got %#v", parentMessages)
	}
	messages, err := runtimeStore.ListMailbox(context.Background(), "mate-session", 0, 10)
	if err != nil {
		t.Fatalf("ListMailbox teammate: %v", err)
	}
	if len(messages) != 1 || messages[0].Kind != team.TaskLifecycleMailboxKind || messages[0].Metadata["event_type"] != "task.completed" {
		t.Fatalf("unexpected task lifecycle mailbox row: %#v", messages)
	}
	if messages[0].Metadata["message_type"] != team.TaskLifecycleControlMessageType || messages[0].Metadata["control_action"] != team.TaskLifecycleControlAction {
		t.Fatalf("unexpected task lifecycle metadata: %#v", messages[0].Metadata)
	}
	controlMessages, err := runtimeStore.ListAgentControlMailbox(context.Background(), "mate-session", 0, 10)
	if err != nil {
		t.Fatalf("ListAgentControlMailbox teammate: %v", err)
	}
	if len(controlMessages) != 1 || controlMessages[0].Kind != team.TaskLifecycleMailboxKind || controlMessages[0].Metadata["message_type"] != team.TaskLifecycleControlMessageType {
		t.Fatalf("unexpected task lifecycle agent-control rows: %#v", controlMessages)
	}
}

func TestBuildLocalChatAgent_DefaultsCheckpointCaptureOff(t *testing.T) {
	session := &ChatSession{}
	host := &localChatRuntimeHost{
		Bootstrap: &runtimebootstrap.Manager{},
	}

	apiAgent := buildLocalChatAgent(session, host, nil, "", "", "")
	if apiAgent == nil {
		t.Fatal("expected agent")
	}
	if apiAgent.CheckpointCaptureEnabled() {
		t.Fatal("expected checkpoint capture to be disabled by default")
	}
	if manager := apiAgent.GetCheckpointManager(); manager != nil {
		t.Fatalf("expected no capture manager while disabled, got %#v", manager)
	}
	if manager := apiAgent.GetCheckpointRestoreManager(); manager == nil {
		t.Fatal("expected restore manager to remain available while capture is disabled")
	}
	if manager := apiAgent.GetCheckpointManager(); manager != nil {
		t.Fatal("restore access must not re-enable checkpoint capture")
	}
}

func TestBuildLocalChatAgent_AppliesCheckpointStorageConfig(t *testing.T) {
	session := &ChatSession{}
	host := &localChatRuntimeHost{
		Bootstrap: &runtimebootstrap.Manager{},
	}
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Checkpoint.Enabled = true
	runtimeConfig.Checkpoint.MaxFileBytes = 12345
	runtimeConfig.Checkpoint.StoreMode = "full"
	runtimeConfig.Checkpoint.ConversationSnapshot = true
	runtimeConfig.Checkpoint.MaxDiffBytes = 2048
	runtimeConfig.Checkpoint.MaxCheckpointsPerSession = 7

	apiAgent := buildLocalChatAgent(session, host, runtimeConfig, "", "", "")
	if apiAgent == nil {
		t.Fatal("expected agent")
	}
	manager := apiAgent.GetCheckpointManager()
	if manager == nil {
		t.Fatal("expected enabled checkpoint capture manager")
	}
	if manager.MaxFileBytes != 12345 || manager.StoreMode != "full" || !manager.ConversationSnapshot || manager.MaxDiffBytes != 2048 || manager.MaxCheckpointsPerSession != 7 {
		t.Fatalf("unexpected checkpoint configuration: %#v", manager)
	}
}

func TestBuildLocalChatAgent_PropagatesReasoningEffortToAgentOptions(t *testing.T) {
	session := &ChatSession{
		ReasoningEffort: "medium",
		Stream:          true,
	}
	host := &localChatRuntimeHost{
		Bootstrap: &runtimebootstrap.Manager{},
	}

	apiAgent := buildLocalChatAgent(session, host, nil, "", "", "")
	if apiAgent == nil {
		t.Fatal("expected agent")
	}

	cfg := apiAgent.GetConfig()
	if cfg == nil || cfg.Options == nil {
		t.Fatal("expected agent options")
	}
	if got := cfg.Options["reasoning_effort"]; got != "medium" {
		t.Fatalf("expected reasoning_effort=medium, got %#v", got)
	}
}

func TestBuildLocalChatAgent_PropagatesFailFastToAgentOptions(t *testing.T) {
	session := &ChatSession{RetryConfig: RetryConfig{DisableRetries: true}}
	host := &localChatRuntimeHost{Bootstrap: &runtimebootstrap.Manager{}}

	apiAgent := buildLocalChatAgent(session, host, nil, "", "", "")
	if apiAgent == nil || apiAgent.GetConfig() == nil {
		t.Fatal("expected agent config")
	}
	if got := apiAgent.GetConfig().Options[runtimellm.MetadataKeyDisableRetries]; got != true {
		t.Fatalf("expected disable_retries=true, got %#v", got)
	}
}

func TestBuildLocalChatAgent_UsesRequestedRouteOverrides(t *testing.T) {
	session := &ChatSession{
		ProviderName:    "base-provider",
		Model:           "base-model",
		ReasoningEffort: "low",
	}
	host := &localChatRuntimeHost{
		Bootstrap: &runtimebootstrap.Manager{},
	}

	apiAgent := buildLocalChatAgent(session, host, nil, "", "worker", "hard-model", "hard-provider", "high")
	if apiAgent == nil {
		t.Fatal("expected agent")
	}
	cfg := apiAgent.GetConfig()
	if cfg.Provider != "hard-provider" || cfg.Model != "hard-model" {
		t.Fatalf("expected requested provider/model, got provider=%q model=%q", cfg.Provider, cfg.Model)
	}
	if cfg.Options == nil || cfg.Options["reasoning_effort"] != "high" {
		t.Fatalf("expected requested reasoning_effort=high, got %#v", cfg.Options)
	}
}

func TestLocalChatRuntimeHostBuildSessionActorIgnoresStaleRequestedModelForBaseSession(t *testing.T) {
	ctx := context.Background()
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	runtimeSession.SetContext(toolbroker.AgentSessionContextRequestedModel, "gpt-5.5")
	if err := manager.Update(ctx, runtimeSession); err != nil {
		t.Fatalf("manager.Update: %v", err)
	}

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "mimo_anthropic",
		DefaultModel:    "mimo-v2.5-pro",
		MaxRetries:      0,
	})
	provider := &capturingLocalChatProvider{
		name: "mimo_anthropic",
		responses: []*runtimellm.LLMResponse{
			{Content: "ok", Model: "mimo-v2.5-pro"},
		},
	}
	if err := llmRuntime.RegisterProvider("mimo_anthropic", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("mimo-v2.5-pro", "mimo_anthropic"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("gpt-5.5", "mimo_anthropic"); err != nil {
		t.Fatalf("RegisterProviderAlias stale: %v", err)
	}
	bootstrapManager, err := runtimebootstrap.NewManager(&runtimebootstrap.Options{
		Config: runtimecfg.DefaultRuntimeConfig(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer bootstrapManager.Stop()
	if err := bootstrapManager.LLMRuntime().RegisterProvider("mimo_anthropic", provider); err != nil {
		t.Fatalf("bootstrap RegisterProvider: %v", err)
	}
	if err := bootstrapManager.LLMRuntime().RegisterProviderAlias("mimo-v2.5-pro", "mimo_anthropic"); err != nil {
		t.Fatalf("bootstrap RegisterProviderAlias: %v", err)
	}
	if err := bootstrapManager.LLMRuntime().RegisterProviderAlias("gpt-5.5", "mimo_anthropic"); err != nil {
		t.Fatalf("bootstrap RegisterProviderAlias stale: %v", err)
	}
	host := &localChatRuntimeHost{
		Bootstrap:    bootstrapManager,
		RuntimeStore: runtimechat.NewInMemoryRuntimeStore(64),
	}
	session := &ChatSession{
		ProviderName:     "mimo_anthropic",
		Model:            "mimo-v2.5-pro",
		RuntimeSession:   runtimeSession,
		SessionManager:   manager,
		LocalRuntimeHost: host,
	}

	actor, err := host.buildSessionActor(runtimeSession.ID, session, manager.GetStorage(), nil, "")
	if err != nil {
		t.Fatalf("buildSessionActor: %v", err)
	}
	if actor == nil {
		t.Fatal("expected actor")
	}
	result, err := actor.SubmitPrompt(ctx, "hello", nil)
	if err != nil {
		t.Fatalf("SubmitPrompt: %v", err)
	}
	if result == nil || strings.TrimSpace(result.Output) != "ok" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("expected one request, got %d", len(provider.requests))
	}
	if provider.requests[0].Model != "mimo-v2.5-pro" {
		t.Fatalf("expected base session model to follow ChatSession, got %q", provider.requests[0].Model)
	}
}

func TestLocalChatRuntimeHostBuildSessionActorUsesChildRouteContext(t *testing.T) {
	ctx := context.Background()
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(ctx, userID)
	if err != nil {
		t.Fatalf("manager.Create root: %v", err)
	}
	childSession, err := manager.Create(ctx, userID)
	if err != nil {
		t.Fatalf("manager.Create child: %v", err)
	}
	childSession.SetContext(sessionmeta.ProviderName, "hard-provider")
	childSession.SetContext(toolbroker.AgentSessionContextRequestedModel, "hard-model")
	childSession.SetContext(sessionmeta.ReasoningEffort, "high")
	if err := manager.Update(ctx, childSession); err != nil {
		t.Fatalf("manager.Update child: %v", err)
	}

	baseProvider := &capturingLocalChatProvider{name: "base-provider"}
	hardProvider := &capturingLocalChatProvider{
		name: "hard-provider",
		responses: []*runtimellm.LLMResponse{
			{Content: "hard ok", Model: "hard-model"},
		},
	}
	bootstrapManager, err := runtimebootstrap.NewManager(&runtimebootstrap.Options{
		Config: runtimecfg.DefaultRuntimeConfig(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer bootstrapManager.Stop()
	if err := bootstrapManager.LLMRuntime().RegisterProvider("base-provider", baseProvider); err != nil {
		t.Fatalf("Register base provider: %v", err)
	}
	if err := bootstrapManager.LLMRuntime().RegisterProvider("hard-provider", hardProvider); err != nil {
		t.Fatalf("Register hard provider: %v", err)
	}
	if err := bootstrapManager.LLMRuntime().RegisterProviderAlias("base-model", "base-provider"); err != nil {
		t.Fatalf("Register base alias: %v", err)
	}
	if err := bootstrapManager.LLMRuntime().RegisterProviderAlias("hard-model", "hard-provider"); err != nil {
		t.Fatalf("Register hard alias: %v", err)
	}
	host := &localChatRuntimeHost{
		Bootstrap:    bootstrapManager,
		RuntimeStore: runtimechat.NewInMemoryRuntimeStore(64),
	}
	session := &ChatSession{
		ProviderName:   "base-provider",
		Model:          "base-model",
		RuntimeSession: rootSession,
		SessionManager: manager,
	}

	actor, err := host.buildSessionActor(childSession.ID, session, manager.GetStorage(), nil, "")
	if err != nil {
		t.Fatalf("buildSessionActor: %v", err)
	}
	result, err := actor.SubmitPrompt(ctx, "hello", nil)
	if err != nil {
		t.Fatalf("SubmitPrompt: %v", err)
	}
	if result == nil || strings.TrimSpace(result.Output) != "hard ok" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(hardProvider.requests) != 1 {
		t.Fatalf("expected one hard-provider request, got %d", len(hardProvider.requests))
	}
	request := hardProvider.requests[0]
	if request.Provider != "hard-provider" || request.Model != "hard-model" || request.ReasoningEffort != "high" {
		t.Fatalf("unexpected routed request: %#v", request)
	}
	if len(baseProvider.requests) != 0 {
		t.Fatalf("base provider should not be used, got %#v", baseProvider.requests)
	}
}

func TestApplyLocalChildReadOnlyPolicyOverridesBypassPermissions(t *testing.T) {
	apiAgent := agent.NewAgentWithLLM(&agent.Config{Name: "read-only-child"}, nil, nil)
	apiAgent.SetToolExecutionPolicy(runtimepolicy.NewToolExecutionPolicy(nil, false))

	applyLocalChildReadOnlyPolicy(apiAgent, true)
	policy := apiAgent.GetToolExecutionPolicy()
	if policy == nil || !policy.ReadOnly {
		t.Fatalf("expected child read-only execution policy, got %#v", policy)
	}
	for _, toolName := range []string{"write_file", "edit", "apply_patch", "append_write"} {
		if err := policy.AllowTool(toolName); err == nil {
			t.Fatalf("expected read-only policy to block %s", toolName)
		}
	}
	// Shell tools stay visible under read-only; only non-readonly commands are blocked.
	for _, toolName := range []string{"shell", "bash", "execute_shell_command"} {
		if err := policy.AllowTool(toolName); err != nil {
			t.Fatalf("expected read-only policy to keep shell surface visible for %s: %v", toolName, err)
		}
	}
	if err := policy.AllowToolCall(runtimeskill.ToolInfo{Name: "shell"}, map[string]interface{}{"command": "git status"}); err != nil {
		t.Fatalf("expected read-only shell git status to be allowed: %v", err)
	}
	if err := policy.AllowToolCall(runtimeskill.ToolInfo{Name: "shell"}, map[string]interface{}{"command": "rm -rf /"}); err == nil {
		t.Fatal("expected read-only shell mutating command to be blocked")
	}
	// background_task stays out of the read-only child capability surface.
	if err := policy.AllowTool("background_task"); err == nil {
		t.Fatal("expected read-only child capability scope to block background_task")
	}
	// Control-plane tools must remain usable under read-only children so
	// investigation/coordination work is not bricked by capability scope.
	for _, toolName := range []string{"view", "spawn_agent", "spawn_team", "ask_user_question", "enter_plan_mode"} {
		if err := policy.AllowTool(toolName); err != nil {
			t.Fatalf("expected read-only child policy to allow %s: %v", toolName, err)
		}
	}
}

func TestApplyLocalChildDepthPolicyHidesUnavailableSpawnTools(t *testing.T) {
	apiAgent := agent.NewAgentWithLLM(&agent.Config{Name: "max-depth-child"}, nil, nil)
	apiAgent.SetToolExecutionPolicy(runtimepolicy.NewToolExecutionPolicy(nil, false))

	applyLocalChildDepthPolicy(apiAgent, 1, 1)
	policy := apiAgent.GetToolExecutionPolicy()
	for _, toolName := range []string{"spawn_agent", "spawn_subagents", "spawn_team"} {
		if err := policy.AllowTool(toolName); err == nil {
			t.Fatalf("expected max-depth policy to hide %s", toolName)
		}
	}
	if err := policy.AllowTool("send_message"); err != nil {
		t.Fatalf("expected existing-agent coordination to remain available: %v", err)
	}
}

func TestLocalActorRegistry_EnforcesAgentLimitsAndListsChildren(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.RuntimeConfig.Agents.MaxThreads = 1
	host.RuntimeConfig.Agents.MaxDepth = 1
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	registry := host.ActorRegistry

	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "child-1"}); err != nil {
		t.Fatalf("first spawn failed: %v", err)
	}
	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "child-2"}); err == nil || !strings.Contains(err.Error(), "thread limit") {
		t.Fatalf("expected max thread error, got %v", err)
	}
	if _, err := registry.Close(context.Background(), "child-1"); err != nil {
		t.Fatalf("close child-1: %v", err)
	}
	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "child-2"}); err != nil {
		t.Fatalf("spawn after close failed: %v", err)
	}
	if _, err := registry.Spawn(context.Background(), "child-2", toolbroker.SpawnAgentArgs{ID: "grandchild-1"}); err == nil ||
		!strings.Contains(err.Error(), "depth limit") ||
		!strings.Contains(err.Error(), `parent_session_id="child-2"`) ||
		!strings.Contains(err.Error(), "requested_child_depth=2") ||
		(!strings.Contains(err.Error(), "continue the work locally") &&
			!strings.Contains(err.Error(), "continue the work in the current agent")) ||
		!strings.Contains(err.Error(), "SPAWN_DEPTH_LIMIT") {
		t.Fatalf("expected actionable max depth pre-check error, got %v", err)
	}

	list, err := registry.List(context.Background(), rootSession.ID, toolbroker.ListAgentsArgs{})
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if list.Count != 1 || list.Agents[0].SessionID != "child-2" || list.Agents[0].Path != "/root/child-2" || list.Agents[0].Depth != 1 {
		t.Fatalf("unexpected active list: %#v", list)
	}
	list, err = registry.List(context.Background(), rootSession.ID, toolbroker.ListAgentsArgs{IncludeClosed: true})
	if err != nil {
		t.Fatalf("list closed agents: %v", err)
	}
	if list.Count != 2 {
		t.Fatalf("expected active and closed child agents, got %#v", list)
	}
}

func TestLocalActorRegistrySpawnPersistsDifficultyRouteContext(t *testing.T) {
	ctx := context.Background()
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(ctx, userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	enabled := true
	host.BaseSession = &ChatSession{
		ProviderName:     "base-provider",
		Model:            "base-model",
		ReasoningEffort:  "low",
		RuntimeSession:   rootSession,
		SessionUserID:    userID,
		LocalRuntimeHost: host,
		Config: &agentconfig.Config{
			AICLI: &agentconfig.AICLIConfig{
				Subagents: &agentconfig.AICLISubagentsConfig{
					Routing: &agentconfig.AICLISubagentRoutingConfig{
						Enabled:           &enabled,
						DefaultDifficulty: "normal",
						Levels: map[string]agentconfig.AICLISubagentRouteProfile{
							"hard": {
								Provider:        "hard-provider",
								Model:           "hard-model",
								ReasoningEffort: "high",
							},
						},
					},
				},
			},
		},
	}

	result, err := host.ActorRegistry.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{
		ID:                  "route-child",
		AgentType:           "worker",
		Difficulty:          "hard",
		DifficultyRationale: "provider-sensitive",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if result.Provider != "hard-provider" || result.Model != "hard-model" || result.ReasoningEffort != "high" ||
		result.Difficulty != "hard" || result.DifficultyRationale != "provider-sensitive" || result.RouteSource != "difficulty_level" {
		t.Fatalf("unexpected route status: %#v", result)
	}
	stored, err := manager.Get(ctx, "route-child")
	if err != nil {
		t.Fatalf("manager.Get child: %v", err)
	}
	if got := agentcontrol.ContextString(stored, sessionmeta.ProviderName); got != "hard-provider" {
		t.Fatalf("expected stored provider hard-provider, got %q", got)
	}
	if got := agentcontrol.ContextString(stored, toolbroker.AgentSessionContextRequestedModel); got != "hard-model" {
		t.Fatalf("expected stored model hard-model, got %q", got)
	}
	if got := agentcontrol.ContextString(stored, sessionmeta.ReasoningEffort); got != "high" {
		t.Fatalf("expected stored reasoning high, got %q", got)
	}
}

func TestLocalActorRegistrySpawnWorktreeIsolationBindsAndCleansUp(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := initLocalIsolationTestRepo(t)
	ctx := context.Background()
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(ctx, userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.RuntimeConfig.Workspace.Root = repo
	host.BaseSession = &ChatSession{
		RuntimeSession:   rootSession,
		SessionUserID:    userID,
		LocalRuntimeHost: host,
	}

	result, err := host.ActorRegistry.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{
		ID:        "wt-child",
		Isolation: "worktree",
	})
	if err != nil {
		t.Fatalf("Spawn worktree: %v", err)
	}
	if result.Isolation != "worktree" {
		t.Fatalf("expected isolation worktree, got %#v", result)
	}
	if strings.TrimSpace(result.WorktreePath) == "" {
		t.Fatalf("expected worktree_path in spawn result, got %#v", result)
	}
	if filepath.Clean(result.WorktreePath) == filepath.Clean(repo) {
		t.Fatalf("worktree path must not equal main repo root: %s", result.WorktreePath)
	}
	if _, err := os.Stat(result.WorktreePath); err != nil {
		t.Fatalf("worktree path missing after spawn: %v", err)
	}

	stored, err := manager.Get(ctx, "wt-child")
	if err != nil {
		t.Fatalf("manager.Get child: %v", err)
	}
	if got := agentcontrol.ContextString(stored, toolbroker.AgentSessionContextIsolation); got != "worktree" {
		t.Fatalf("expected stored isolation worktree, got %q", got)
	}
	if got := agentcontrol.ContextString(stored, toolbroker.AgentSessionContextWorktreePath); got != result.WorktreePath {
		t.Fatalf("stored worktree_path=%q want %q", got, result.WorktreePath)
	}
	if got := agentcontrol.ContextString(stored, sessionmeta.WorkspacePath); got != result.WorktreePath {
		t.Fatalf("stored workspace_path=%q want %q", got, result.WorktreePath)
	}
	if got := agentcontrol.ContextString(stored, toolbroker.AgentSessionContextWorktreeRepoRoot); filepath.Clean(got) != filepath.Clean(repo) {
		t.Fatalf("stored worktree_repo_root=%q want %q", got, repo)
	}
	if got := isolationWritePathsFromContext(stored); len(got) != 1 || filepath.Clean(got[0]) != filepath.Clean(result.WorktreePath) {
		t.Fatalf("expected default write_paths claim of isolation root %q, got %#v", result.WorktreePath, got)
	}

	// Child mutation must stay isolated from the main tree until parent apply_agent_worktree.
	isolatedFile := filepath.Join(result.WorktreePath, "isolated-only.txt")
	if err := os.WriteFile(isolatedFile, []byte("child-only\n"), 0o644); err != nil {
		t.Fatalf("write isolated file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "isolated-only.txt")); !os.IsNotExist(err) {
		t.Fatalf("main tree should not see isolated-only.txt, err=%v", err)
	}

	if _, err := host.ActorRegistry.Close(ctx, "wt-child"); err != nil {
		t.Fatalf("Close worktree child: %v", err)
	}
	if _, err := os.Stat(result.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree removed after close, err=%v path=%s", err, result.WorktreePath)
	}
}

func TestLocalActorRegistryApplyAndDiscardWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := initLocalIsolationTestRepo(t)
	ctx := context.Background()
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(ctx, userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.RuntimeConfig.Workspace.Root = repo
	host.BaseSession = &ChatSession{
		RuntimeSession:   rootSession,
		SessionUserID:    userID,
		LocalRuntimeHost: host,
	}

	applyChild, err := host.ActorRegistry.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{
		ID:        "wt-apply-child",
		Isolation: "worktree",
	})
	if err != nil {
		t.Fatalf("Spawn apply child: %v", err)
	}
	applyFile := filepath.Join(applyChild.WorktreePath, "applied.txt")
	if err := os.WriteFile(applyFile, []byte("land-me\n"), 0o644); err != nil {
		t.Fatalf("write apply child file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "applied.txt")); !os.IsNotExist(err) {
		t.Fatalf("main tree must not see applied.txt before apply, err=%v", err)
	}

	applyResult, err := host.ActorRegistry.ApplyWorktree(ctx, toolbroker.ApplyAgentWorktreeArgs{
		ID: "wt-apply-child",
	})
	if err != nil {
		t.Fatalf("ApplyWorktree: %v", err)
	}
	if applyResult == nil || !applyResult.Applied || !applyResult.Removed || applyResult.Kept {
		t.Fatalf("unexpected apply result: %#v", applyResult)
	}
	data, err := os.ReadFile(filepath.Join(repo, "applied.txt"))
	if err != nil {
		t.Fatalf("read applied main-tree file: %v", err)
	}
	if string(data) != "land-me\n" {
		t.Fatalf("unexpected applied content: %q", data)
	}
	if _, err := os.Stat(applyChild.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected apply worktree removed by default, err=%v path=%s", err, applyChild.WorktreePath)
	}
	storedApply, err := manager.Get(ctx, "wt-apply-child")
	if err != nil {
		t.Fatalf("manager.Get apply child: %v", err)
	}
	if got := agentcontrol.ContextString(storedApply, toolbroker.AgentSessionContextWorktreePath); got != "" {
		t.Fatalf("expected worktree_path cleared after apply, got %q", got)
	}
	if got := agentcontrol.ContextString(storedApply, toolbroker.AgentSessionContextWorktreeDisposition); got != toolbroker.WorktreeDispositionApplied {
		t.Fatalf("expected disposition applied, got %q", got)
	}

	discardChild, err := host.ActorRegistry.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{
		ID:        "wt-discard-child",
		Isolation: "worktree",
	})
	if err != nil {
		t.Fatalf("Spawn discard child: %v", err)
	}
	if err := os.WriteFile(filepath.Join(discardChild.WorktreePath, "discard-me.txt"), []byte("drop\n"), 0o644); err != nil {
		t.Fatalf("write discard child file: %v", err)
	}

	discardResult, err := host.ActorRegistry.DiscardWorktree(ctx, toolbroker.DiscardAgentWorktreeArgs{
		ID: "wt-discard-child",
	})
	if err != nil {
		t.Fatalf("DiscardWorktree: %v", err)
	}
	if discardResult == nil || !discardResult.Discarded || !discardResult.Removed {
		t.Fatalf("unexpected discard result: %#v", discardResult)
	}
	if _, err := os.Stat(filepath.Join(repo, "discard-me.txt")); !os.IsNotExist(err) {
		t.Fatalf("main tree must not receive discarded file, err=%v", err)
	}
	if _, err := os.Stat(discardChild.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected discard worktree removed, err=%v path=%s", err, discardChild.WorktreePath)
	}
	storedDiscard, err := manager.Get(ctx, "wt-discard-child")
	if err != nil {
		t.Fatalf("manager.Get discard child: %v", err)
	}
	if got := agentcontrol.ContextString(storedDiscard, toolbroker.AgentSessionContextWorktreePath); got != "" {
		t.Fatalf("expected worktree_path cleared after discard, got %q", got)
	}
	if got := agentcontrol.ContextString(storedDiscard, toolbroker.AgentSessionContextWorktreeDisposition); got != toolbroker.WorktreeDispositionDiscarded {
		t.Fatalf("expected disposition discarded, got %q", got)
	}

	keepChild, err := host.ActorRegistry.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{
		ID:        "wt-keep-child",
		Isolation: "worktree",
	})
	if err != nil {
		t.Fatalf("Spawn keep child: %v", err)
	}
	if err := os.WriteFile(filepath.Join(keepChild.WorktreePath, "kept.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("write keep child file: %v", err)
	}
	keepResult, err := host.ActorRegistry.ApplyWorktree(ctx, toolbroker.ApplyAgentWorktreeArgs{
		ID:   "wt-keep-child",
		Keep: true,
	})
	if err != nil {
		t.Fatalf("ApplyWorktree keep: %v", err)
	}
	if keepResult == nil || !keepResult.Applied || !keepResult.Kept || keepResult.Removed {
		t.Fatalf("unexpected keep apply result: %#v", keepResult)
	}
	if _, err := os.Stat(keepChild.WorktreePath); err != nil {
		t.Fatalf("keep=true must preserve worktree path: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(repo, "kept.txt")); err != nil || string(data) != "keep\n" {
		t.Fatalf("expected kept.txt applied, data=%q err=%v", data, err)
	}
	storedKeep, err := manager.Get(ctx, "wt-keep-child")
	if err != nil {
		t.Fatalf("manager.Get keep child: %v", err)
	}
	if got := agentcontrol.ContextString(storedKeep, toolbroker.AgentSessionContextWorktreePath); got != keepChild.WorktreePath {
		t.Fatalf("keep=true should retain worktree_path, got %q want %q", got, keepChild.WorktreePath)
	}
	if got := agentcontrol.ContextString(storedKeep, toolbroker.AgentSessionContextWorktreeDisposition); got != toolbroker.WorktreeDispositionApplied {
		t.Fatalf("expected disposition applied with keep, got %q", got)
	}
	if _, err := host.ActorRegistry.Close(ctx, "wt-keep-child"); err != nil {
		t.Fatalf("Close keep child: %v", err)
	}
}

func TestLocalActorRegistrySpawnWorktreeFailsClosedOutsideGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	nonGit := t.TempDir()
	ctx := context.Background()
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(ctx, userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.RuntimeConfig.Workspace.Root = nonGit
	host.BaseSession = &ChatSession{
		RuntimeSession:   rootSession,
		SessionUserID:    userID,
		LocalRuntimeHost: host,
	}

	_, err = host.ActorRegistry.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{
		ID:        "wt-fail-child",
		Isolation: "worktree",
	})
	if err == nil {
		t.Fatal("expected worktree spawn to fail outside git repo")
	}
	if !strings.Contains(err.Error(), "no main-tree fallback") && !strings.Contains(err.Error(), "git") {
		t.Fatalf("expected fail-closed isolation error, got: %v", err)
	}
	if _, getErr := manager.Get(ctx, "wt-fail-child"); getErr == nil {
		t.Fatal("failed isolation spawn must not leave a child session")
	}
}

func TestLocalActorRegistrySpawnDefaultsIsolationNone(t *testing.T) {
	ctx := context.Background()
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(ctx, userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.BaseSession = &ChatSession{
		RuntimeSession:   rootSession,
		SessionUserID:    userID,
		LocalRuntimeHost: host,
	}

	result, err := host.ActorRegistry.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "none-child"})
	if err != nil {
		t.Fatalf("Spawn default: %v", err)
	}
	if result.Isolation != "none" {
		t.Fatalf("expected default isolation none, got %#v", result)
	}
	if result.WorktreePath != "" {
		t.Fatalf("did not expect worktree_path for isolation none, got %#v", result)
	}
	stored, err := manager.Get(ctx, "none-child")
	if err != nil {
		t.Fatalf("manager.Get child: %v", err)
	}
	if got := agentcontrol.ContextString(stored, toolbroker.AgentSessionContextIsolation); got != "none" {
		t.Fatalf("expected stored isolation none, got %q", got)
	}
	if got := agentcontrol.ContextString(stored, toolbroker.AgentSessionContextWorktreePath); got != "" {
		t.Fatalf("did not expect stored worktree_path, got %q", got)
	}
}

func isolationWritePathsFromContext(session interface {
	GetContext(key string) (interface{}, bool)
}) []string {
	if session == nil {
		return nil
	}
	value, ok := session.GetContext(toolbroker.AgentSessionContextWritePaths)
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				if trimmed := strings.TrimSpace(text); trimmed != "" {
					out = append(out, trimmed)
				}
			}
		}
		return out
	case string:
		if trimmed := strings.TrimSpace(typed); trimmed != "" {
			return []string{trimmed}
		}
	}
	return nil
}

func initLocalIsolationTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "seed")
	return dir
}

func TestLocalActorRegistrySpawnKeepsLegacyRouteContextWhenRoutingDisabled(t *testing.T) {
	ctx := context.Background()
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(ctx, userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.BaseSession = &ChatSession{
		ProviderName:     "base-provider",
		Model:            "base-model",
		ReasoningEffort:  "low",
		RuntimeSession:   rootSession,
		SessionUserID:    userID,
		LocalRuntimeHost: host,
	}

	result, err := host.ActorRegistry.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "legacy-child"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if result.Provider != "" || result.Model != "" || result.ReasoningEffort != "" {
		t.Fatalf("routing disabled should not persist provider/model/reasoning overrides, got %#v", result)
	}
	stored, err := manager.Get(ctx, "legacy-child")
	if err != nil {
		t.Fatalf("manager.Get child: %v", err)
	}
	if got := agentcontrol.ContextString(stored, sessionmeta.ProviderName); got != "" {
		t.Fatalf("expected no stored provider override, got %q", got)
	}
	if got := agentcontrol.ContextString(stored, toolbroker.AgentSessionContextRequestedModel); got != "" {
		t.Fatalf("expected no stored model override, got %q", got)
	}
	if got := agentcontrol.ContextString(stored, sessionmeta.ReasoningEffort); got != "" {
		t.Fatalf("expected no stored reasoning override, got %q", got)
	}
}

func TestLocalActorRegistrySpawnKeepsLegacyModelOverrideWhenRoutingDisabled(t *testing.T) {
	ctx := context.Background()
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(ctx, userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.BaseSession = &ChatSession{
		ProviderName:     "base-provider",
		Model:            "base-model",
		ReasoningEffort:  "low",
		RuntimeSession:   rootSession,
		SessionUserID:    userID,
		LocalRuntimeHost: host,
	}

	result, err := host.ActorRegistry.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{
		ID:              "legacy-model-child",
		Model:           "legacy-child-model",
		Provider:        "ignored-provider",
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if result.Provider != "" || result.Model != "legacy-child-model" || result.ReasoningEffort != "" {
		t.Fatalf("routing disabled should preserve only legacy model override, got %#v", result)
	}
	stored, err := manager.Get(ctx, "legacy-model-child")
	if err != nil {
		t.Fatalf("manager.Get child: %v", err)
	}
	if got := agentcontrol.ContextString(stored, sessionmeta.ProviderName); got != "" {
		t.Fatalf("expected no stored provider override, got %q", got)
	}
	if got := agentcontrol.ContextString(stored, toolbroker.AgentSessionContextRequestedModel); got != "legacy-child-model" {
		t.Fatalf("expected stored model legacy-child-model, got %q", got)
	}
	if got := agentcontrol.ContextString(stored, sessionmeta.ReasoningEffort); got != "" {
		t.Fatalf("expected no stored reasoning override, got %q", got)
	}
}

func TestLocalActorRegistry_WritesAndClosesAgentRegistryStore(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()
	agentStore, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agent_control_agents.sqlite"),
	})
	if err != nil {
		t.Fatalf("NewSQLiteGlobalAgentRegistryStore: %v", err)
	}
	defer agentStore.Close()

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.AgentRegistryStore = agentStore
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}

	if _, err := host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "durable-child"}); err != nil {
		t.Fatalf("spawn durable child: %v", err)
	}
	records, err := agentStore.ListAgentControlAgents(context.Background(), agentcontrol.AgentFilter{
		RootSessionID: rootSession.ID,
		IncludeClosed: true,
	})
	if err != nil {
		t.Fatalf("ListAgentControlAgents: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected root and child agent records, got %#v", records)
	}
	var foundChild bool
	for _, record := range records {
		if record.SessionID == "durable-child" && record.AgentPath == "/root/durable-child" && record.Workflow == agentcontrol.WorkflowSpawnAgent {
			foundChild = true
		}
	}
	if !foundChild {
		t.Fatalf("expected durable child registry row, got %#v", records)
	}

	if _, err := host.ActorRegistry.Close(context.Background(), "/root/durable-child"); err != nil {
		t.Fatalf("close durable child: %v", err)
	}
	closed, err := agentStore.ListAgentControlAgents(context.Background(), agentcontrol.AgentFilter{
		SessionID:     "durable-child",
		IncludeClosed: true,
	})
	if err != nil {
		t.Fatalf("ListAgentControlAgents closed: %v", err)
	}
	if len(closed) != 1 || !closed[0].Closed() {
		t.Fatalf("expected durable child row to be closed, got %#v", closed)
	}
}

func TestLocalActorRegistry_CompletionClosesDurableAgentRecord(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()
	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()
	agentStore, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{Path: filepath.Join(t.TempDir(), "agent_control.sqlite")})
	if err != nil {
		t.Fatalf("NewSQLiteGlobalAgentRegistryStore: %v", err)
	}
	defer agentStore.Close()

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.AgentRegistryStore = agentStore
	if _, err := host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "completed-child"}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	host.EventBus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: "completed-child",
		Payload:   map[string]interface{}{"success": true},
	})

	records, err := agentStore.ListAgentControlAgents(context.Background(), agentcontrol.AgentFilter{SessionID: "completed-child", IncludeClosed: true})
	if err != nil {
		t.Fatalf("ListAgentControlAgents: %v", err)
	}
	if len(records) != 1 || !records[0].Closed() {
		t.Fatalf("expected completion to close durable child record, got %#v", records)
	}
}

func TestLocalActorRegistry_ConcurrentSpawnAgentReservationsUseUniqueSessions(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()
	agentStore, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agent_control_agents.sqlite"),
	})
	if err != nil {
		t.Fatalf("NewSQLiteGlobalAgentRegistryStore: %v", err)
	}
	defer agentStore.Close()

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.AgentRegistryStore = agentStore
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.RuntimeConfig.Agents.MaxThreads = 4
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, childID := range []string{"agent-A", "agent-B"} {
		childID := childID
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, spawnErr := host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: childID})
			errs <- spawnErr
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent spawn failed: %v", err)
		}
	}
	records, err := agentStore.ListAgentControlAgents(context.Background(), agentcontrol.AgentFilter{
		RootSessionID: rootSession.ID,
	})
	if err != nil {
		t.Fatalf("ListAgentControlAgents: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("expected root and two child agent records, got %#v", records)
	}
}

func TestLocalActorRegistry_SpawnClosesStaleSessionBinding(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()
	agentStore, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agent_control_agents.sqlite"),
	})
	if err != nil {
		t.Fatalf("NewSQLiteGlobalAgentRegistryStore: %v", err)
	}
	defer agentStore.Close()
	if _, err := agentStore.UpsertAgentControlAgent(context.Background(), agentcontrol.AgentRecord{
		AgentID:       "root:stale-child",
		RootSessionID: "stale-child",
		SessionID:     "stale-child",
		AgentPath:     "/root",
		AgentType:     agentcontrol.AgentTypeRoot,
		Status:        agentcontrol.AgentStatusActive,
	}); err != nil {
		t.Fatalf("upsert stale root binding: %v", err)
	}

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.AgentRegistryStore = agentStore
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.RuntimeConfig.Agents.MaxThreads = 4
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}

	if _, err := host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "stale-child"}); err != nil {
		t.Fatalf("spawn with stale session binding failed: %v", err)
	}
	stale, err := agentStore.ListAgentControlAgents(context.Background(), agentcontrol.AgentFilter{
		AgentID:       "root:stale-child",
		IncludeClosed: true,
	})
	if err != nil {
		t.Fatalf("ListAgentControlAgents stale: %v", err)
	}
	if len(stale) != 1 || !stale[0].Closed() {
		t.Fatalf("expected stale root binding to be closed, got %#v", stale)
	}
	child, err := agentStore.ListAgentControlAgents(context.Background(), agentcontrol.AgentFilter{
		SessionID:     "stale-child",
		IncludeClosed: false,
	})
	if err != nil {
		t.Fatalf("ListAgentControlAgents child: %v", err)
	}
	if len(child) != 1 || child[0].AgentPath != "/root/stale-child" || child[0].RootSessionID != rootSession.ID {
		t.Fatalf("expected active child binding under current root, got %#v", child)
	}
}

func TestLocalActorRegistry_RegistrySpawnLimitUsesDurableStore(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()
	agentStore, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agent_control_agents.sqlite"),
	})
	if err != nil {
		t.Fatalf("NewSQLiteGlobalAgentRegistryStore: %v", err)
	}
	defer agentStore.Close()
	if _, err := agentStore.UpsertAgentControlAgent(context.Background(), agentcontrol.AgentRecord{
		AgentID:       "root:" + rootSession.ID,
		RootSessionID: rootSession.ID,
		SessionID:     rootSession.ID,
		AgentPath:     "/root",
		AgentType:     agentcontrol.AgentTypeRoot,
		Status:        agentcontrol.AgentStatusActive,
	}); err != nil {
		t.Fatalf("upsert root record: %v", err)
	}
	externalSession := runtimechat.NewSession(userID)
	externalSession.ID = "external-child"
	externalSession.SetContext(toolbroker.AgentSessionContextParentSessionID, rootSession.ID)
	externalSession.SetContext(toolbroker.AgentSessionContextRootSessionID, rootSession.ID)
	if err := manager.GetStorage().Save(context.Background(), externalSession); err != nil {
		t.Fatalf("save external child session: %v", err)
	}
	if _, err := agentStore.UpsertAgentControlAgent(context.Background(), agentcontrol.AgentRecord{
		AgentID:         "external-child",
		RootSessionID:   rootSession.ID,
		ParentAgentID:   "root:" + rootSession.ID,
		ParentSessionID: rootSession.ID,
		SessionID:       externalSession.ID,
		AgentPath:       "/root/external-child",
		Depth:           1,
		AgentType:       agentcontrol.AgentTypeChild,
		Workflow:        agentcontrol.WorkflowSpawnAgent,
		Status:          agentcontrol.AgentStatusActive,
	}); err != nil {
		t.Fatalf("upsert external child record: %v", err)
	}

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.AgentRegistryStore = agentStore
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.RuntimeConfig.Agents.MaxThreads = 1
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}

	if _, err := host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "local-child"}); err == nil || !strings.Contains(err.Error(), "thread limit") {
		t.Fatalf("expected durable registry max thread error, got %v", err)
	}
}

func TestLocalActorRegistry_RegistryProjectionDoesNotReopenClosedAgent(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()
	agentStore, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agent_control_agents.sqlite"),
	})
	if err != nil {
		t.Fatalf("NewSQLiteGlobalAgentRegistryStore: %v", err)
	}
	defer agentStore.Close()

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.AgentRegistryStore = agentStore
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	if _, err := host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "closed-projection-child"}); err != nil {
		t.Fatalf("spawn child: %v", err)
	}
	if _, err := agentStore.CloseAgentControlAgentSubtree(context.Background(), rootSession.ID, "/root/closed-projection-child", time.Now().UTC()); err != nil {
		t.Fatalf("CloseAgentControlAgentSubtree: %v", err)
	}
	if _, err := host.ActorRegistry.List(context.Background(), rootSession.ID, toolbroker.ListAgentsArgs{}); err != nil {
		t.Fatalf("list agents: %v", err)
	}
	records, err := agentStore.ListAgentControlAgents(context.Background(), agentcontrol.AgentFilter{
		SessionID:     "closed-projection-child",
		IncludeClosed: true,
	})
	if err != nil {
		t.Fatalf("ListAgentControlAgents: %v", err)
	}
	if len(records) != 1 || !records[0].Closed() {
		t.Fatalf("expected list/materialize to preserve closed durable row, got %#v", records)
	}
}

func TestLocalActorRegistry_MaterializeMarksMissingSessionBindingStale(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()
	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()
	agentStore, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{Path: filepath.Join(t.TempDir(), "agent_control_agents.sqlite")})
	if err != nil {
		t.Fatalf("NewSQLiteGlobalAgentRegistryStore: %v", err)
	}
	defer agentStore.Close()
	if _, err := agentStore.UpsertAgentControlAgent(context.Background(), agentcontrol.AgentRecord{
		AgentID:         "missing-child",
		RootSessionID:   rootSession.ID,
		ParentAgentID:   "root:" + rootSession.ID,
		ParentSessionID: rootSession.ID,
		SessionID:       "missing-child-session",
		AgentPath:       "/root/missing-child",
		AgentType:       agentcontrol.AgentTypeChild,
		Workflow:        agentcontrol.WorkflowSpawnAgent,
		Status:          agentcontrol.AgentStatusActive,
	}); err != nil {
		t.Fatalf("upsert missing child: %v", err)
	}

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.AgentRegistryStore = agentStore
	host.BaseSession = &ChatSession{RuntimeSession: rootSession, SessionUserID: userID}
	if err := host.ActorRegistry.materializeLocalAgentRegistry(context.Background()); err != nil {
		t.Fatalf("materializeLocalAgentRegistry: %v", err)
	}
	records, err := agentStore.ListAgentControlAgents(context.Background(), agentcontrol.AgentFilter{AgentID: "missing-child", IncludeClosed: true})
	if err != nil {
		t.Fatalf("ListAgentControlAgents: %v", err)
	}
	if len(records) != 1 || records[0].Status != agentcontrol.AgentStatusStale || !records[0].Closed() {
		t.Fatalf("expected missing child binding to be stale, got %#v", records)
	}
}

func TestLocalActorRegistry_MaterializeMarksExpiredRunningLeaseStale(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()
	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	childSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create child: %v", err)
	}
	childSession.SetContext(toolbroker.AgentSessionContextParentSessionID, rootSession.ID)
	childSession.SetContext(toolbroker.AgentSessionContextRootSessionID, rootSession.ID)
	childSession.SetContext(toolbroker.AgentSessionContextPath, "/root/expired-child")
	if err := manager.Update(context.Background(), childSession); err != nil {
		t.Fatalf("manager.Update child: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()
	agentStore, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{Path: filepath.Join(t.TempDir(), "agent_control_agents.sqlite")})
	if err != nil {
		t.Fatalf("NewSQLiteGlobalAgentRegistryStore: %v", err)
	}
	defer agentStore.Close()
	if _, err := agentStore.UpsertAgentControlAgent(context.Background(), agentcontrol.AgentRecord{
		AgentID:         childSession.ID,
		RootSessionID:   rootSession.ID,
		ParentAgentID:   "root:" + rootSession.ID,
		ParentSessionID: rootSession.ID,
		SessionID:       childSession.ID,
		AgentPath:       "/root/expired-child",
		AgentType:       agentcontrol.AgentTypeChild,
		Workflow:        agentcontrol.WorkflowSpawnAgent,
		Status:          agentcontrol.AgentStatusActive,
	}); err != nil {
		t.Fatalf("upsert child: %v", err)
	}
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	if err := runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{SessionID: childSession.ID, Status: runtimechat.SessionRunning, UpdatedAt: time.Now().UTC().Add(-time.Minute)}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if _, err := runtimeStore.AcquireLease(context.Background(), runtimechat.LeaseRequest{SessionID: childSession.ID, OwnerID: "expired-owner", OwnerKind: "test", TTL: time.Second, Now: time.Now().UTC().Add(-time.Minute)}); err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.AgentRegistryStore = agentStore
	host.RuntimeStore = runtimeStore
	host.BaseSession = &ChatSession{RuntimeSession: rootSession, SessionUserID: userID}
	if err := host.ActorRegistry.materializeLocalAgentRegistry(context.Background()); err != nil {
		t.Fatalf("materializeLocalAgentRegistry: %v", err)
	}
	records, err := agentStore.ListAgentControlAgents(context.Background(), agentcontrol.AgentFilter{AgentID: childSession.ID, IncludeClosed: true})
	if err != nil {
		t.Fatalf("ListAgentControlAgents: %v", err)
	}
	if len(records) != 1 || records[0].Status != agentcontrol.AgentStatusStale {
		t.Fatalf("expected expired running child binding to be stale, got %#v", records)
	}
}

func TestLocalActorRegistry_ListIncludesTeamTeammateSessions(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	teamID, err := teamStore.CreateTeam(context.Background(), team.Team{
		ID:            "team-alpha",
		LeadSessionID: rootSession.ID,
		Status:        team.TeamStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := teamStore.UpsertTeammate(context.Background(), team.Teammate{
		ID:        "member-1",
		TeamID:    teamID,
		Name:      "Documentation Reviewer",
		Profile:   "documentation-reviewer",
		SessionID: "mate-session",
		State:     team.TeammateStateIdle,
	}); err != nil {
		t.Fatalf("UpsertTeammate: %v", err)
	}
	assignee := "member-1"
	if _, err := teamStore.CreateTask(context.Background(), team.Task{
		ID:       "task-docs",
		TeamID:   teamID,
		Title:    "Review docs",
		Status:   team.TaskStatusRunning,
		Assignee: &assignee,
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	mateSession := runtimechat.NewSession(userID)
	mateSession.ID = "mate-session"
	if err := manager.GetStorage().Save(context.Background(), mateSession); err != nil {
		t.Fatalf("Save mate session: %v", err)
	}

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	registry := host.ActorRegistry

	list, err := registry.List(context.Background(), rootSession.ID, toolbroker.ListAgentsArgs{})
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if list.Count != 1 {
		t.Fatalf("expected team teammate in agent list, got %#v", list)
	}
	agent := list.Agents[0]
	if agent.SessionID != "mate-session" || agent.ParentSessionID != rootSession.ID {
		t.Fatalf("unexpected teammate identity: %#v", agent)
	}
	if agent.Path != "/root/teams/team-alpha/member-1" || agent.Depth != 1 || agent.AgentType != "documentation-reviewer" {
		t.Fatalf("unexpected teammate metadata: %#v", agent)
	}
	if agent.TeamID != teamID || agent.TeammateID != "member-1" || agent.CurrentTaskID != "task-docs" || agent.CurrentTaskStatus != string(team.TaskStatusRunning) {
		t.Fatalf("unexpected teammate task projection: %#v", agent)
	}

	filtered, err := registry.List(context.Background(), rootSession.ID, toolbroker.ListAgentsArgs{PathPrefix: "/root/teams/team-alpha"})
	if err != nil {
		t.Fatalf("list agents with path prefix: %v", err)
	}
	if filtered.Count != 1 || filtered.Agents[0].SessionID != "mate-session" {
		t.Fatalf("expected path-prefix filtered teammate, got %#v", filtered)
	}

	reloaded, err := manager.Get(context.Background(), "mate-session")
	if err != nil {
		t.Fatalf("reload mate session: %v", err)
	}
	if parent, ok := reloaded.GetContext(toolbroker.AgentSessionContextParentSessionID); !ok || parent != rootSession.ID {
		t.Fatalf("expected persisted teammate parent context, got %#v", reloaded.Metadata.Context)
	}
}

func TestLocalActorRegistry_SyncTeamTeammateAgentWritesRegistry(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()
	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()
	agentStore, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agent_control_agents.sqlite"),
	})
	if err != nil {
		t.Fatalf("NewSQLiteGlobalAgentRegistryStore: %v", err)
	}
	defer agentStore.Close()
	teamID, err := teamStore.CreateTeam(context.Background(), team.Team{
		ID:            "team-sync",
		LeadSessionID: rootSession.ID,
		Status:        team.TeamStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.AgentRegistryStore = agentStore
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	mate := team.Teammate{
		ID:        "member-1",
		TeamID:    teamID,
		Name:      "Reviewer",
		Profile:   "reviewer",
		SessionID: "mate-session",
		State:     team.TeammateStateIdle,
	}
	if err := host.ActorRegistry.SyncTeamTeammateAgent(context.Background(), nil, mate); err != nil {
		t.Fatalf("SyncTeamTeammateAgent: %v", err)
	}
	records, err := agentStore.ListAgentControlAgents(context.Background(), agentcontrol.AgentFilter{
		TeamID:     teamID,
		TeammateID: "member-1",
	})
	if err != nil {
		t.Fatalf("ListAgentControlAgents: %v", err)
	}
	if len(records) != 1 || records[0].AgentID != "team:team-sync:member-1" || records[0].SessionID != "mate-session" {
		t.Fatalf("expected teammate registry row, got %#v", records)
	}
}

func TestLocalActorRegistry_UsesConfiguredDefaultForkTurns(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	rootSession.AddMessage(*runtimetypes.NewUserMessage("first parent turn"))
	rootSession.AddMessage(*runtimetypes.NewAssistantMessage("last parent turn"))
	if err := manager.Update(context.Background(), rootSession); err != nil {
		t.Fatalf("manager.Update: %v", err)
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.RuntimeConfig.Agents.DefaultForkTurns = "1"
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	registry := host.ActorRegistry

	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "fork-default-child"}); err != nil {
		t.Fatalf("spawn with default fork_turns failed: %v", err)
	}
	child, err := manager.Get(context.Background(), "fork-default-child")
	if err != nil {
		t.Fatalf("load forked child: %v", err)
	}
	messages := child.GetMessages()
	if len(messages) != 1 || messages[0].Content != "last parent turn" {
		t.Fatalf("expected default fork_turns=1 to copy last parent turn, got %#v", messages)
	}

	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "fork-none-child", ForkTurns: "none"}); err != nil {
		t.Fatalf("spawn with explicit fork_turns=none failed: %v", err)
	}
	child, err = manager.Get(context.Background(), "fork-none-child")
	if err != nil {
		t.Fatalf("load non-forked child: %v", err)
	}
	if messages := child.GetMessages(); len(messages) != 0 {
		t.Fatalf("expected explicit fork_turns=none to override default, got %#v", messages)
	}
}

func TestLocalActorRegistry_SpawnRejectsCompleteTaskCompletionRequirement(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}

	_, err = host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{
		ID:                    "reject-complete-child",
		Message:               "inspect",
		CompletionRequirement: "complete_task",
	})
	if err == nil || !strings.Contains(err.Error(), "use spawn_team") {
		t.Fatalf("expected actionable complete_task rejection, got %v", err)
	}
	if _, getErr := manager.Get(context.Background(), "reject-complete-child"); getErr == nil {
		t.Fatal("rejected complete_task spawn must not leave a child session")
	}
}

func TestLocalActorRegistry_ForkedChildDropsParentCompleteTaskContext(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	rootSession.SetContext(toolbroker.AgentSessionContextCompletionRequirement, "complete_task")
	rootSession.SetContext(toolbroker.AgentSessionContextPermissionMode, "bypass_permissions")
	if err := manager.Update(context.Background(), rootSession); err != nil {
		t.Fatalf("manager.Update: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	forkAll := true

	if _, err := host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{
		ID:          "fork-none-complete-child",
		ForkContext: &forkAll,
	}); err != nil {
		t.Fatalf("spawn forked child: %v", err)
	}
	child, err := manager.Get(context.Background(), "fork-none-complete-child")
	if err != nil {
		t.Fatalf("load forked child: %v", err)
	}
	if got := agentcontrol.ContextString(child, toolbroker.AgentSessionContextCompletionRequirement); got != "none" {
		t.Fatalf("expected forked child completion_requirement=none, got %q", got)
	}
	if got := agentcontrol.ContextString(child, toolbroker.AgentSessionContextPermissionMode); got != "bypass_permissions" {
		t.Fatalf("expected forked child to keep permission mode, got %q", got)
	}
	runMeta := host.ActorRegistry.localAgentRunMeta(context.Background(), child.ID)
	if runMeta == nil || runMeta.CompletionRequirement != "none" {
		t.Fatalf("expected follow-up/resume run meta to force none, got %+v", runMeta)
	}
}

func TestLocalActorRegistry_CloseAgentPathClosesSubtree(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.RuntimeConfig.Agents.MaxDepth = 2
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	registry := host.ActorRegistry

	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "close-parent"}); err != nil {
		t.Fatalf("spawn parent: %v", err)
	}
	if _, err := registry.Spawn(context.Background(), "close-parent", toolbroker.SpawnAgentArgs{ID: "close-child"}); err != nil {
		t.Fatalf("spawn child: %v", err)
	}
	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "close-sibling"}); err != nil {
		t.Fatalf("spawn sibling: %v", err)
	}

	result, err := registry.Close(context.Background(), "/root/close-parent")
	if err != nil {
		t.Fatalf("close path: %v", err)
	}
	if result.ClosedCount != 2 {
		t.Fatalf("expected close subtree to close parent and child, got %#v", result)
	}
	if !reflect.DeepEqual(result.ClosedSessionIDs, []string{"close-parent", "close-child"}) {
		t.Fatalf("unexpected closed sessions: %#v", result.ClosedSessionIDs)
	}

	parent, err := manager.Get(context.Background(), "close-parent")
	if err != nil {
		t.Fatalf("load close-parent: %v", err)
	}
	child, err := manager.Get(context.Background(), "close-child")
	if err != nil {
		t.Fatalf("load close-child: %v", err)
	}
	sibling, err := manager.Get(context.Background(), "close-sibling")
	if err != nil {
		t.Fatalf("load close-sibling: %v", err)
	}
	if parent.State != runtimechat.StateClosed || child.State != runtimechat.StateClosed {
		t.Fatalf("expected parent and child closed, got parent=%s child=%s", parent.State, child.State)
	}
	if sibling.State == runtimechat.StateClosed {
		t.Fatalf("expected sibling to stay open, got %s", sibling.State)
	}

	list, err := registry.List(context.Background(), rootSession.ID, toolbroker.ListAgentsArgs{IncludeClosed: true})
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if list.Count != 3 {
		t.Fatalf("expected closed subtree plus open sibling in list, got %#v", list)
	}
}

func TestLocalActorRegistry_AgentPathTargetsResolveToSession(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	provider := runtimellm.NewMockProvider("mock", 0)
	provider.SetResponse("read status", "status ok")
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "mock",
		DefaultModel:    "mock-model",
	})
	if err := llmRuntime.RegisterProvider("mock", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("mock-model", "mock"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	registry := host.ActorRegistry

	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "path-target"}); err != nil {
		t.Fatalf("spawn path target: %v", err)
	}
	messageResult, err := registry.SendMessage(context.Background(), rootSession.ID, toolbroker.AgentMessageArgs{
		Target:  "/root/path-target",
		Message: "hello by path",
	})
	if err != nil {
		t.Fatalf("send message by path: %v", err)
	}
	if messageResult.TargetSessionID != "path-target" {
		t.Fatalf("expected path to resolve to session id, got %#v", messageResult)
	}

	waitResult, err := registry.Wait(context.Background(), toolbroker.WaitAgentArgs{ID: "/root/path-target", TimeoutMs: 100})
	if err != nil {
		t.Fatalf("wait by path: %v", err)
	}
	if waitResult.MatchedSessionID != "path-target" {
		t.Fatalf("expected wait path to resolve, got %#v", waitResult)
	}

	if _, err := registry.Resume(context.Background(), "/root/path-target"); err != nil {
		t.Fatalf("resume by path: %v", err)
	}
}

func TestLocalActorRegistry_WaitUsesEventStoreWakeup(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	registry := host.ActorRegistry

	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "event-wait-child"}); err != nil {
		t.Fatalf("spawn event wait child: %v", err)
	}
	if err := host.RuntimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "event-wait-child",
		Status:    runtimechat.SessionRunning,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save running state: %v", err)
	}

	resultCh := make(chan *toolbroker.AgentWaitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, waitErr := registry.Wait(context.Background(), toolbroker.WaitAgentArgs{ID: "event-wait-child", TimeoutMs: 2000})
		if waitErr != nil {
			errCh <- waitErr
			return
		}
		resultCh <- result
	}()

	time.Sleep(100 * time.Millisecond)
	if err := host.RuntimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "event-wait-child",
		Status:    runtimechat.SessionIdle,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save idle state: %v", err)
	}
	if _, err := host.EventStore.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: "event-wait-child",
		Payload:   map[string]interface{}{"success": true},
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	select {
	case waitErr := <-errCh:
		t.Fatalf("wait failed: %v", waitErr)
	case result := <-resultCh:
		if result == nil || result.MatchedSessionID != "event-wait-child" || result.ReadyCount != 1 {
			t.Fatalf("unexpected wait result: %#v", result)
		}
		if len(result.ReadyIDs) != 1 || result.ReadyIDs[0] != "event-wait-child" || result.WaitedMs <= 0 {
			t.Fatalf("expected actionable ready diagnostics: %#v", result)
		}
		if !strings.HasPrefix(result.NextAction, "consume_ready_outputs") {
			t.Fatalf("expected ready next_action guidance, got %#v", result.NextAction)
		}
	case <-time.After(450 * time.Millisecond):
		t.Fatal("wait did not wake from event store append")
	}
}

func TestLocalActorRegistry_ReadEventsUsesEventStoreWakeup(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	registry := host.ActorRegistry

	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "event-read-child"}); err != nil {
		t.Fatalf("spawn event read child: %v", err)
	}

	resultCh := make(chan *toolbroker.AgentEventsResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, readErr := registry.ReadEvents(context.Background(), toolbroker.ReadAgentEventsArgs{
			ID:       "/root/event-read-child",
			AfterSeq: 0,
			Limit:    20,
			WaitMs:   2000,
		})
		if readErr != nil {
			errCh <- readErr
			return
		}
		resultCh <- result
	}()

	time.Sleep(100 * time.Millisecond)
	event := runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "event-read-child",
		Payload:   map[string]interface{}{"content": "event read done"},
	}
	if _, err := host.EventStore.AppendEvent(context.Background(), event); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	select {
	case readErr := <-errCh:
		t.Fatalf("read events failed: %v", readErr)
	case result := <-resultCh:
		if result == nil || result.SessionID != "event-read-child" || result.Count != 1 {
			t.Fatalf("unexpected read result: %#v", result)
		}
		if len(result.Events) != 1 || result.Events[0].Type != runtimechat.EventAssistantMessage {
			t.Fatalf("unexpected events: %#v", result.Events)
		}
	case <-time.After(450 * time.Millisecond):
		t.Fatal("read_agent_events did not wake from event store append")
	}
}

func TestLocalActorRegistry_WaitWithoutTargetUsesParentMailbox(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.BaseSession = &ChatSession{RuntimeSession: rootSession, SessionUserID: userID}
	registry := host.ActorRegistry

	resultCh := make(chan *toolbroker.AgentWaitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, waitErr := registry.Wait(context.Background(), toolbroker.WaitAgentArgs{
			SessionID:   rootSession.ID,
			MailboxOnly: true,
			TimeoutMs:   2000,
		})
		if waitErr != nil {
			errCh <- waitErr
			return
		}
		resultCh <- result
	}()

	time.Sleep(100 * time.Millisecond)
	if err := registry.deliverAgentMailboxEvent(context.Background(), rootSession.ID, team.MailMessage{
		FromAgent: "child-1",
		ToAgent:   "parent",
		Kind:      "agent_message",
		Body:      "parent mailbox hello",
	}); err != nil {
		t.Fatalf("deliverAgentMailboxEvent: %v", err)
	}

	select {
	case waitErr := <-errCh:
		t.Fatalf("wait failed: %v", waitErr)
	case result := <-resultCh:
		if result == nil || result.Event == nil || result.Event.Type != runtimechat.EventMailboxReceived {
			t.Fatalf("unexpected mailbox wait result: %#v", result)
		}
		if result.LatestSeq != 1 || result.Event.Seq != 1 || result.Event.Payload["mailbox_seq"] != int64(1) {
			t.Fatalf("expected mailbox substrate sequence 1, got result=%#v payload=%#v", result, result.Event.Payload)
		}
		if result.Event.Payload["body"] != "parent mailbox hello" {
			t.Fatalf("unexpected mailbox event payload: %#v", result.Event.Payload)
		}
		if result.NextAction != "consume_mailbox_events" || result.WaitedMs <= 0 {
			t.Fatalf("expected actionable mailbox diagnostics: %#v", result)
		}
	case <-time.After(450 * time.Millisecond):
		t.Fatal("wait_agent did not wake from parent mailbox event")
	}
}

func TestLocalActorRegistry_ReadEventsWithoutTargetUsesParentMailbox(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.BaseSession = &ChatSession{RuntimeSession: rootSession, SessionUserID: userID}
	registry := host.ActorRegistry

	resultCh := make(chan *toolbroker.AgentEventsResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, readErr := registry.ReadEvents(context.Background(), toolbroker.ReadAgentEventsArgs{
			SessionID:   rootSession.ID,
			MailboxOnly: true,
			AfterSeq:    0,
			Limit:       20,
			WaitMs:      2000,
		})
		if readErr != nil {
			errCh <- readErr
			return
		}
		resultCh <- result
	}()

	time.Sleep(100 * time.Millisecond)
	if _, err := host.EventStore.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: rootSession.ID,
		Payload:   map[string]interface{}{"content": "not mailbox"},
	}); err != nil {
		t.Fatalf("AppendEvent non-mailbox: %v", err)
	}
	if err := registry.deliverAgentMailboxEvent(context.Background(), rootSession.ID, team.MailMessage{
		FromAgent: "child-1",
		ToAgent:   "parent",
		Kind:      "agent_message",
		Body:      "parent mailbox event read hello",
	}); err != nil {
		t.Fatalf("deliverAgentMailboxEvent: %v", err)
	}

	select {
	case readErr := <-errCh:
		t.Fatalf("read failed: %v", readErr)
	case result := <-resultCh:
		if result == nil || result.SessionID != rootSession.ID || result.Count != 1 {
			t.Fatalf("unexpected mailbox read result: %#v", result)
		}
		if len(result.Events) != 1 || result.Events[0].Type != runtimechat.EventMailboxReceived {
			t.Fatalf("unexpected mailbox events: %#v", result.Events)
		}
		if result.LatestSeq != 1 || result.Events[0].Seq != 1 || result.Events[0].Payload["mailbox_seq"] != int64(1) {
			t.Fatalf("expected mailbox substrate sequence 1, got result=%#v payload=%#v", result, result.Events[0].Payload)
		}
		if result.Events[0].Payload["body"] != "parent mailbox event read hello" {
			t.Fatalf("unexpected mailbox payload: %#v", result.Events[0].Payload)
		}
	case <-time.After(450 * time.Millisecond):
		t.Fatal("read_agent_events did not wake from parent mailbox event")
	}
}

func TestLocalActorRegistry_ReadEventsWithoutTargetMergesAgentControlMailbox(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.BaseSession = &ChatSession{RuntimeSession: rootSession, SessionUserID: userID}
	registry := host.ActorRegistry

	if err := registry.deliverAgentMailboxEvent(context.Background(), rootSession.ID, team.MailMessage{
		FromAgent: "child-1",
		ToAgent:   "parent",
		Kind:      "info",
		Body:      "legacy mailbox",
	}); err != nil {
		t.Fatalf("deliver legacy mailbox: %v", err)
	}
	controlMessage := toolbroker.BuildAgentMailboxMessage("child-2", "parent", "control mailbox", false)
	if err := registry.deliverAgentMailboxEvent(context.Background(), rootSession.ID, controlMessage); err != nil {
		t.Fatalf("deliver control mailbox: %v", err)
	}

	result, err := registry.ReadEvents(context.Background(), toolbroker.ReadAgentEventsArgs{
		SessionID:   rootSession.ID,
		MailboxOnly: true,
		AfterSeq:    0,
		Limit:       20,
	})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if result == nil || result.Count != 2 || result.LatestSeq != 2 {
		t.Fatalf("unexpected merged mailbox result: %#v", result)
	}
	if result.Events[0].Payload["body"] != "legacy mailbox" || result.Events[1].Payload["body"] != "control mailbox" {
		t.Fatalf("unexpected merged event order: %#v", result.Events)
	}
	if result.Events[1].Payload["metadata"] == nil {
		t.Fatalf("expected control mailbox metadata, got %#v", result.Events[1].Payload)
	}
}

func TestLocalActorRegistry_ReadEventsWithoutTargetUsesCompletionMailboxWithoutDisplayMirror(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.BaseSession = &ChatSession{RuntimeSession: rootSession, SessionUserID: userID}
	registry := host.ActorRegistry
	completion := toolbroker.BuildSubagentCompletionMailboxMessage(rootSession.ID, "completion-child", "/root/completion-child", "worker", runtimechat.EventSessionEnd, map[string]interface{}{
		"status": "done",
	})
	controlStore, ok := host.EventStore.(runtimechat.AgentControlMailboxStore)
	if !ok {
		t.Fatalf("expected AgentControl mailbox store, got %T", host.EventStore)
	}
	if _, _, err := controlStore.AppendAgentControlMailbox(context.Background(), rootSession.ID, completion); err != nil {
		t.Fatalf("AppendAgentControlMailbox: %v", err)
	}
	storedEvents, err := host.EventStore.ListEvents(context.Background(), rootSession.ID, 0, 20)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	for _, event := range storedEvents {
		if event.Type == "subagent.completed" {
			t.Fatalf("test setup should not write display mirror event, got %#v", event)
		}
	}

	result, err := registry.ReadEvents(context.Background(), toolbroker.ReadAgentEventsArgs{
		SessionID:   rootSession.ID,
		MailboxOnly: true,
		AfterSeq:    0,
		Limit:       20,
	})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if result == nil || result.Count != 1 || result.LatestSeq != 1 {
		t.Fatalf("unexpected completion mailbox result: %#v", result)
	}
	event := result.Events[0]
	if event.Type != runtimechat.EventMailboxReceived || event.Payload["kind"] != toolbroker.SubagentCompletionMailboxKind {
		t.Fatalf("expected completion mailbox event, got %#v", event)
	}
	metadata, ok := event.Payload["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected completion metadata, got %#v", event.Payload)
	}
	if metadata["control_action"] != toolbroker.SubagentCompletionAction || metadata["message_type"] != toolbroker.SubagentCompletionMessageType {
		t.Fatalf("unexpected completion metadata: %#v", metadata)
	}
}

func TestLocalActorRegistry_SendMessagePersistsMailboxWithoutTargetActor(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.BaseSession = &ChatSession{RuntimeSession: rootSession, SessionUserID: userID}
	registry := host.ActorRegistry
	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "mailbox-child"}); err != nil {
		t.Fatalf("spawn mailbox child: %v", err)
	}
	host.SessionHub.Stop("mailbox-child")

	result, err := registry.SendMessage(context.Background(), rootSession.ID, toolbroker.AgentMessageArgs{
		Target:  "/root/mailbox-child",
		Message: "durable hello",
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if result == nil || result.TargetSessionID != "mailbox-child" || !result.Delivered || result.Triggered {
		t.Fatalf("unexpected send result: %#v", result)
	}
	if _, ok := host.SessionHub.Get("mailbox-child"); ok {
		t.Fatal("send_message should persist mailbox event without starting target actor")
	}

	events, err := host.EventStore.ListEvents(context.Background(), "mailbox-child", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected durable mailbox event, got %#v", events)
	}
	event := events[0]
	if event.Type != runtimechat.EventMailboxReceived || event.Payload["kind"] != "agent_message" || event.Payload["body"] != "durable hello" {
		t.Fatalf("unexpected mailbox event: %#v", event)
	}
	metadata, ok := event.Payload["metadata"].(map[string]interface{})
	if !ok || metadata["target_session_id"] != "mailbox-child" || metadata["trigger_turn"] != false {
		t.Fatalf("unexpected mailbox metadata: %#v", event.Payload)
	}
	if metadata["message_type"] != toolbroker.AgentMailboxMessageType ||
		metadata["control_action"] != toolbroker.AgentMailboxMessageAction ||
		metadata["workflow"] != toolbroker.AgentMailboxWorkflow ||
		metadata["mailbox_kind"] != toolbroker.AgentMailboxMessageKind {
		t.Fatalf("expected agent-control mailbox metadata, got %#v", metadata)
	}
}

func TestHandleCommand_AgentsSendPersistsMailboxMessage(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	session := &ChatSession{
		RuntimeSession:   rootSession,
		SessionUserID:    userID,
		LocalRuntimeHost: host,
	}
	host.BaseSession = session
	if _, err := host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "send-child"}); err != nil {
		t.Fatalf("spawn send child: %v", err)
	}
	host.SessionHub.Stop("send-child")

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents send /root/send-child please inspect docs", false); quit {
			t.Fatal("agents send command should not quit")
		}
	})
	if !strings.Contains(output, "Agent Message: sent target=send-child mode=delivered") {
		t.Fatalf("unexpected agents send output:\n%s", output)
	}
	messages, err := host.EventStore.(runtimechat.MailboxReaderStore).ListMailbox(context.Background(), "send-child", 0, 10)
	if err != nil {
		t.Fatalf("ListMailbox: %v", err)
	}
	if len(messages) != 1 || messages[0].Kind != "agent_message" || messages[0].Body != "please inspect docs" {
		t.Fatalf("unexpected child mailbox messages: %#v", messages)
	}
	if _, ok := host.SessionHub.Get("send-child"); ok {
		t.Fatal("agents send should persist mailbox without starting stopped target actor")
	}
}

func TestHandleCommand_AgentsTargetProvidesDefaultSendTarget(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	session := &ChatSession{
		RuntimeSession:   rootSession,
		SessionManager:   manager,
		SessionUserID:    userID,
		LocalRuntimeHost: host,
	}
	host.BaseSession = session
	if _, err := host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "target-child"}); err != nil {
		t.Fatalf("spawn target child: %v", err)
	}
	host.SessionHub.Stop("target-child")

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/agents target /root/target-child", false); quit {
			t.Fatal("agents target command should not quit")
		}
	})
	if !strings.Contains(output, "Selected Agent Target: /root/target-child") {
		t.Fatalf("unexpected agents target output:\n%s", output)
	}
	if session.SelectedAgentTarget != "/root/target-child" {
		t.Fatalf("expected selected target to be set, got %q", session.SelectedAgentTarget)
	}
	stored, err := manager.Get(context.Background(), rootSession.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	if got := runtimeSessionContextString(stored, chatRuntimeContextSelectedAgent); got != "/root/target-child" {
		t.Fatalf("expected selected target context, got %q", got)
	}

	output = captureStdout(t, func() {
		if quit := handleCommand(session, "/agents send inspect selected target", false); quit {
			t.Fatal("agents send command should not quit")
		}
	})
	if !strings.Contains(output, "Agent Message: sent target=target-child mode=delivered") {
		t.Fatalf("unexpected agents send output:\n%s", output)
	}
	messages, err := host.EventStore.(runtimechat.MailboxReaderStore).ListMailbox(context.Background(), "target-child", 0, 10)
	if err != nil {
		t.Fatalf("ListMailbox: %v", err)
	}
	if len(messages) != 1 || messages[0].Body != "inspect selected target" {
		t.Fatalf("unexpected mailbox messages: %#v", messages)
	}
}

func TestLocalActorRegistry_FollowupTaskPersistsMailboxWhenTargetBusy(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.BaseSession = &ChatSession{RuntimeSession: rootSession, SessionUserID: userID}
	registry := host.ActorRegistry
	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "busy-followup-child"}); err != nil {
		t.Fatalf("spawn busy followup child: %v", err)
	}
	if _, err := host.SessionHub.GetOrCreate("busy-followup-child"); err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if err := host.RuntimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "busy-followup-child",
		Status:    runtimechat.SessionRunning,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	result, err := registry.FollowupTask(context.Background(), rootSession.ID, toolbroker.AgentMessageArgs{
		Target:  "/root/busy-followup-child",
		Message: "queue while busy",
	})
	if err != nil {
		t.Fatalf("FollowupTask: %v", err)
	}
	if result == nil || result.TargetSessionID != "busy-followup-child" || !result.Delivered || result.Triggered {
		t.Fatalf("unexpected followup result: %#v", result)
	}

	events, err := host.EventStore.ListEvents(context.Background(), "busy-followup-child", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected durable busy followup mailbox event, got %#v", events)
	}
	event := events[0]
	if event.Type != runtimechat.EventMailboxReceived || event.Payload["kind"] != "followup_task" || event.Payload["body"] != "queue while busy" {
		t.Fatalf("unexpected followup mailbox event: %#v", event)
	}
	metadata, ok := event.Payload["metadata"].(map[string]interface{})
	if !ok || metadata["target_session_id"] != "busy-followup-child" || metadata["trigger_turn"] != true {
		t.Fatalf("unexpected followup metadata: %#v", event.Payload)
	}
	if metadata["message_type"] != toolbroker.AgentMailboxFollowupMessageType ||
		metadata["control_action"] != toolbroker.AgentMailboxFollowupAction ||
		metadata["workflow"] != toolbroker.AgentMailboxWorkflow ||
		metadata["mailbox_kind"] != toolbroker.AgentMailboxFollowupKind {
		t.Fatalf("expected agent-control followup metadata, got %#v", metadata)
	}
}

func TestLocalActorRegistry_MirrorsChildCompletionToParentEvents(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	hookPayloads := make(chan map[string]interface{}, 2)
	hookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode hook payload: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		hookPayloads <- payload
		_, _ = w.Write([]byte(`{"action":"continue"}`))
	}))
	defer hookServer.Close()
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Hooks = []runtimehooks.HookConfig{
		{
			ID:    "local-spawn-agent-start",
			Event: runtimehooks.EventSubagentStart,
			Exec:  runtimehooks.ExecConfig{Type: "http", URL: hookServer.URL},
		},
		{
			ID:    "local-spawn-agent-stop",
			Event: runtimehooks.EventSubagentStop,
			Exec:  runtimehooks.ExecConfig{Type: "http", URL: hookServer.URL},
		},
	}
	host.RuntimeConfig = runtimeConfig
	enabled := true
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
		Config: &agentconfig.Config{
			AICLI: &agentconfig.AICLIConfig{
				Subagents: &agentconfig.AICLISubagentsConfig{
					Routing: &agentconfig.AICLISubagentRoutingConfig{
						Enabled:           &enabled,
						DefaultDifficulty: "normal",
						Levels: map[string]agentconfig.AICLISubagentRouteProfile{
							"hard": {
								Provider:        "completion-provider",
								Model:           "completion-model",
								ReasoningEffort: "high",
							},
						},
					},
				},
			},
		},
	}
	registry := host.ActorRegistry

	_, err = registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{
		ID:                  "completion-child",
		AgentType:           "worker",
		Difficulty:          "hard",
		DifficultyRationale: "completion audit",
	})
	if err != nil {
		t.Fatalf("spawn completion child: %v", err)
	}
	startHook := waitLocalHookPayload(t, hookPayloads)
	if startHook["session_id"] != "completion-child" ||
		startHook["parent_session_id"] != rootSession.ID ||
		startHook["path"] != "/root/completion-child" ||
		startHook["agent_type"] != "worker" ||
		startHook["difficulty"] != "hard" ||
		startHook["route_provider"] != "completion-provider" ||
		startHook["route_model"] != "completion-model" ||
		startHook["route_reasoning_effort"] != "high" ||
		startHook["route_source"] != "difficulty_level" {
		t.Fatalf("unexpected local subagent start hook payload: %#v", startHook)
	}
	childEnd := runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: "completion-child",
		TraceID:   "trace-child-complete",
		Payload: map[string]interface{}{
			"success":            true,
			"steps":              3,
			"seq":                int64(44),
			"usage_total_tokens": 1200,
		},
	}
	host.EventBus.Publish(childEnd)
	stopHook := waitLocalHookPayload(t, hookPayloads)
	if stopHook["session_id"] != "completion-child" ||
		stopHook["source_event_type"] != runtimechat.EventSessionEnd ||
		stopHook["status"] != string(runtimechat.SessionIdle) ||
		stopHook["success"] != true ||
		stopHook["agent_type"] != "worker" ||
		stopHook["difficulty"] != "hard" ||
		stopHook["route_model"] != "completion-model" ||
		intPayloadValue(stopHook, "steps") != 3 ||
		intPayloadValue(stopHook, "usage_total_tokens") != 1200 {
		t.Fatalf("unexpected local subagent stop hook payload: %#v", stopHook)
	}

	events, err := host.EventStore.ListEvents(context.Background(), rootSession.ID, 0, 20)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected parent completion event and mailbox event, got %#v", events)
	}
	mailboxEvent := events[0]
	if mailboxEvent.Type != runtimechat.EventMailboxReceived || mailboxEvent.SessionID != rootSession.ID {
		t.Fatalf("unexpected completion mailbox event: %#v", mailboxEvent)
	}
	if mailboxEvent.Payload["kind"] != "subagent.completed" || mailboxEvent.Payload["from_agent"] != "completion-child" || mailboxEvent.Payload["to_agent"] != "parent" {
		t.Fatalf("unexpected completion mailbox payload: %#v", mailboxEvent.Payload)
	}
	metadata, ok := mailboxEvent.Payload["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected completion mailbox metadata, got %#v", mailboxEvent.Payload)
	}
	if metadata["session_id"] != "completion-child" ||
		metadata["path"] != "/root/completion-child" ||
		metadata["agent_type"] != "worker" ||
		metadata["event_seq"] != int64(44) ||
		metadata["message_type"] != agentcontrol.MessageTypeSubagentCompleted ||
		metadata["control_action"] != agentcontrol.ActionAgentCompleted ||
		metadata["workflow"] != agentcontrol.WorkflowSpawnAgent ||
		metadata["mailbox_delivery"] != agentcontrol.DeliverySessionMailbox ||
		metadata["mailbox_kind"] != agentcontrol.MailboxKindSubagentCompleted ||
		metadata["difficulty"] != "hard" ||
		metadata["difficulty_source"] != "explicit" ||
		metadata["difficulty_rationale"] != "completion audit" ||
		metadata["route_provider"] != "completion-provider" ||
		metadata["route_model"] != "completion-model" ||
		metadata["route_reasoning_effort"] != "high" ||
		metadata["route_source"] != "difficulty_level" ||
		intPayloadValue(metadata, "usage_total_tokens") != 1200 {
		t.Fatalf("unexpected completion mailbox metadata: %#v", metadata)
	}
	event := events[1]
	if event.Type != "subagent.completed" || event.SessionID != rootSession.ID {
		t.Fatalf("unexpected mirrored event: %#v", event)
	}
	if event.Payload["session_id"] != "completion-child" || event.Payload["path"] != "/root/completion-child" || event.Payload["agent_type"] != "worker" {
		t.Fatalf("unexpected mirrored payload: %#v", event.Payload)
	}
	if event.Payload["status"] != string(runtimechat.SessionIdle) || event.Payload["success"] != true {
		t.Fatalf("unexpected completion status payload: %#v", event.Payload)
	}
	if event.Payload["source_event_seq"] != int64(44) {
		t.Fatalf("expected source event seq on display mirror, got %#v", event.Payload)
	}
	if event.Payload["display_mirror"] != true ||
		event.Payload["mirror_source"] != toolbroker.SubagentCompletionMirrorSource ||
		event.Payload["mailbox_delivery_status"] != "delivered" ||
		event.Payload["message_type"] != agentcontrol.MessageTypeSubagentCompleted ||
		event.Payload["control_action"] != agentcontrol.ActionAgentCompleted ||
		event.Payload["difficulty"] != "hard" ||
		event.Payload["difficulty_source"] != "explicit" ||
		event.Payload["difficulty_rationale"] != "completion audit" ||
		event.Payload["route_provider"] != "completion-provider" ||
		event.Payload["route_model"] != "completion-model" ||
		event.Payload["route_reasoning_effort"] != "high" ||
		event.Payload["route_source"] != "difficulty_level" ||
		intPayloadValue(event.Payload, "usage_total_tokens") != 1200 {
		t.Fatalf("expected display mirror metadata, got %#v", event.Payload)
	}
}

func TestLocalActorRegistry_PersistsCompletionMailboxWithoutParentActor(t *testing.T) {
	eventStore := runtimechat.NewInMemoryRuntimeStore(16)
	host := &localChatRuntimeHost{
		EventStore: eventStore,
		EventBus:   runtimeevents.NewBusWithRetention(16),
	}
	registry := newLocalActorRegistry(host)

	registry.deliverSubagentCompletionMailbox(context.Background(), "parent-session", "child-session", "/root/child-session", "worker", runtimechat.EventSessionEnd, map[string]interface{}{
		"status":  string(runtimechat.SessionIdle),
		"success": true,
		"seq":     int64(7),
	})

	events, err := eventStore.ListEvents(context.Background(), "parent-session", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected durable mailbox event without parent actor, got %#v", events)
	}
	event := events[0]
	if event.Type != runtimechat.EventMailboxReceived || event.Payload["kind"] != "subagent.completed" {
		t.Fatalf("unexpected mailbox event: %#v", event)
	}
	metadata, ok := event.Payload["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected mailbox metadata, got %#v", event.Payload)
	}
	if metadata["session_id"] != "child-session" ||
		metadata["event_seq"] != int64(7) ||
		metadata["message_type"] != agentcontrol.MessageTypeSubagentCompleted ||
		metadata["control_action"] != agentcontrol.ActionAgentCompleted ||
		metadata["mailbox_kind"] != agentcontrol.MailboxKindSubagentCompleted {
		t.Fatalf("unexpected mailbox metadata: %#v", metadata)
	}
}

func waitLocalHookPayload(t *testing.T, payloads <-chan map[string]interface{}) map[string]interface{} {
	t.Helper()
	select {
	case payload := <-payloads:
		return payload
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for local hook payload")
		return nil
	}
}

func TestBuildLocalChatAgent_DisablesWorkspaceContextByDefaultForActorChat(t *testing.T) {
	session := &ChatSession{}
	host := &localChatRuntimeHost{
		Bootstrap: &runtimebootstrap.Manager{},
	}

	workspaceRoot := t.TempDir()
	apiAgent := buildLocalChatAgent(session, host, nil, workspaceRoot, "", "")
	if apiAgent == nil {
		t.Fatal("expected agent")
	}

	cfg := apiAgent.GetConfig()
	if cfg == nil {
		t.Fatal("expected agent config")
	}
	if cfg.Options == nil {
		t.Fatal("expected options with tool_base_path when workspace root is known")
	}
	// Tool path resolution stays available even when workspace context scanning is off.
	if got := cfg.Options["tool_base_path"]; got != workspaceRoot {
		t.Fatalf("expected tool_base_path=%q, got %#v", workspaceRoot, got)
	}
	if got := cfg.Options["workspace_path"]; got != nil {
		t.Fatalf("expected workspace_path to be disabled by default, got %#v", got)
	}
	if got := cfg.Options["context_workspace_mode"]; got != nil {
		t.Fatalf("expected context_workspace_mode to be disabled by default, got %#v", got)
	}
}

func TestBuildLocalChatAgent_UsesSignalsWorkspaceContextWhenWorkspaceEnabled(t *testing.T) {
	session := &ChatSession{}
	host := &localChatRuntimeHost{
		Bootstrap: &runtimebootstrap.Manager{},
	}
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Workspace.Enabled = true

	apiAgent := buildLocalChatAgent(session, host, runtimeConfig, t.TempDir(), "", "")
	if apiAgent == nil {
		t.Fatal("expected agent")
	}

	cfg := apiAgent.GetConfig()
	if cfg == nil || cfg.Options == nil {
		t.Fatal("expected agent options")
	}
	if got := cfg.Options["workspace_path"]; got == nil {
		t.Fatal("expected workspace_path when workspace is enabled")
	}
	if got := cfg.Options["context_workspace_mode"]; got != contextmgr.WorkspaceModeSignals {
		t.Fatalf("expected context_workspace_mode=signals, got %#v", got)
	}
	if got := cfg.Options["context_min_workspace_query_length"]; got != 4 {
		t.Fatalf("expected context_min_workspace_query_length=4, got %#v", got)
	}
}

func TestBuildLocalChatAgent_UsesConfiguredWorkspaceModeForActorChat(t *testing.T) {
	session := &ChatSession{}
	host := &localChatRuntimeHost{
		Bootstrap: &runtimebootstrap.Manager{},
	}
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Context.WorkspaceMode = contextmgr.WorkspaceModeBroad

	apiAgent := buildLocalChatAgent(session, host, runtimeConfig, t.TempDir(), "", "")
	if apiAgent == nil {
		t.Fatal("expected agent")
	}

	cfg := apiAgent.GetConfig()
	if cfg == nil || cfg.Options == nil {
		t.Fatal("expected agent options")
	}
	if got := cfg.Options["workspace_path"]; got == nil {
		t.Fatal("expected workspace_path when workspace mode is configured")
	}
	if got := cfg.Options["context_workspace_mode"]; got != contextmgr.WorkspaceModeBroad {
		t.Fatalf("expected context_workspace_mode=broad, got %#v", got)
	}
}

func TestBuildLocalChatAgent_PropagatesRuntimeWorkspaceOptions(t *testing.T) {
	session := &ChatSession{}
	host := &localChatRuntimeHost{
		Bootstrap: &runtimebootstrap.Manager{},
	}
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Workspace.Include = []string{"*.go", "*.ts"}
	runtimeConfig.Workspace.Exclude = []string{"node_modules", "vendor", ".git"}
	runtimeConfig.Workspace.MaxFileSize = 1234
	runtimeConfig.Workspace.MaxChunkSize = 321
	runtimeConfig.Workspace.ChunkOverlap = 12

	apiAgent := buildLocalChatAgent(session, host, runtimeConfig, t.TempDir(), "", "")
	if apiAgent == nil {
		t.Fatal("expected agent")
	}

	cfg := apiAgent.GetConfig()
	if cfg == nil || cfg.Options == nil {
		t.Fatal("expected agent options")
	}
	if got := cfg.Options["workspace_max_file_size"]; got != int64(1234) {
		t.Fatalf("expected workspace_max_file_size=1234, got %#v", got)
	}
	if got := cfg.Options["workspace_max_chunk_size"]; got != 321 {
		t.Fatalf("expected workspace_max_chunk_size=321, got %#v", got)
	}
	if got := cfg.Options["workspace_chunk_overlap"]; got != 12 {
		t.Fatalf("expected workspace_chunk_overlap=12, got %#v", got)
	}
	if got := cfg.Options["workspace_include"]; !reflect.DeepEqual(got, []string{"*.go", "*.ts"}) {
		t.Fatalf("expected workspace_include to be propagated, got %#v", got)
	}
	if got := cfg.Options["workspace_exclude"]; !reflect.DeepEqual(got, []string{"node_modules", "vendor", ".git"}) {
		t.Fatalf("expected workspace_exclude to be propagated, got %#v", got)
	}
}

func TestBuildLocalChatAgent_UsesCappedDefaultMaxTokens(t *testing.T) {
	session := &ChatSession{
		Provider: agentconfig.Provider{
			Protocol:       "anthropic",
			MaxTokensLimit: 131072,
			ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
				"claude-opus-4-7": {MaxTokens: 128000},
			},
		},
		Model: "claude-opus-4-7",
	}
	host := &localChatRuntimeHost{
		Bootstrap: &runtimebootstrap.Manager{},
	}

	apiAgent := buildLocalChatAgent(session, host, nil, "", "", "")
	if apiAgent == nil {
		t.Fatal("expected agent")
	}

	cfg := apiAgent.GetConfig()
	if cfg == nil {
		t.Fatal("expected agent config")
	}
	// Provider max_tokens_limit is a hard ceiling, not the default request budget.
	if cfg.DefaultMaxTokens != runtimellm.CappedDefaultMaxTokens {
		t.Fatalf("expected DefaultMaxTokens=%d (capped default), got %d", runtimellm.CappedDefaultMaxTokens, cfg.DefaultMaxTokens)
	}
}

func TestBuildLocalChatLoopConfig_PropagatesReasoningEffort(t *testing.T) {
	config := buildLocalChatLoopConfig(nil, &ChatSession{
		ReasoningEffort: "high",
	})
	if config == nil {
		t.Fatal("expected loop config")
	}
	if got := config.ReasoningEffort; got != "high" {
		t.Fatalf("expected loop reasoning_effort=high, got %#v", got)
	}
}

func TestBuildLocalChatLoopConfig_PropagatesParallelToolConfig(t *testing.T) {
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Agent.EnableParallelTools = true
	runtimeConfig.Agent.MaxParallelToolCalls = 4

	config := buildLocalChatLoopConfig(runtimeConfig, &ChatSession{})
	if config == nil {
		t.Fatal("expected loop config")
	}
	if !config.EnableParallelTools {
		t.Fatal("expected parallel tools to be enabled")
	}
	if config.MaxParallelToolCalls != 4 {
		t.Fatalf("expected MaxParallelToolCalls=4, got %d", config.MaxParallelToolCalls)
	}
}

func TestApplyLocalChatCompletionRequirement_ExplicitOverridesAgentType(t *testing.T) {
	config := &agent.LoopReActConfig{}
	applyLocalChatCompletionRequirement(config, &ChatSession{ProfileAgent: "explore"}, "explore", "complete_task", "")
	if config.CompletionRequirement != agent.CompletionRequirementCompleteTask {
		t.Fatalf("expected explicit complete_task, got %#v", config.CompletionRequirement)
	}
}

func TestApplyLocalChatCompletionRequirement_EmptyDefaultsToNone(t *testing.T) {
	config := &agent.LoopReActConfig{}
	applyLocalChatCompletionRequirement(config, &ChatSession{}, "", "", "")
	if config.CompletionRequirement != agent.CompletionRequirementNone {
		t.Fatalf("expected none default, got %#v", config.CompletionRequirement)
	}
}

func TestLocalTeamLifecycleService_SyncLoopsFiltersToBaseLeadSession(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-current",
		LeadSessionID: "current-session",
		Status:        team.TeamStatusActive,
	}); err != nil {
		t.Fatalf("CreateTeam current: %v", err)
	}
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-old",
		LeadSessionID: "old-session",
		Status:        team.TeamStatusActive,
	}); err != nil {
		t.Fatalf("CreateTeam old: %v", err)
	}

	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "current-session"
	host := &localChatRuntimeHost{
		TeamStore: store,
		EventBus:  runtimeevents.NewBusWithRetention(16),
		BaseSession: &ChatSession{
			RuntimeSession: runtimeSession,
		},
		Orchestrator: team.NewOrchestrator(store, nil, nil),
	}
	host.Orchestrator.TickInterval = time.Hour
	lifecycle := newLocalTeamLifecycleService(host)
	host.TeamLifecycle = lifecycle
	defer lifecycle.StopLoops()

	lifecycle.SyncLoops()

	if !lifecycle.hasTeamLoop("team-current") {
		t.Fatal("expected current lead team loop to start")
	}
	if lifecycle.hasTeamLoop("team-old") {
		t.Fatal("expected old lead team loop to stay stopped")
	}
	recent := host.EventBus.Recent(16)
	var started runtimeevents.Event
	for _, event := range recent {
		if event.Type == "team.orchestrator.loop.started" {
			started = event
			break
		}
	}
	if started.Type == "" {
		t.Fatalf("expected loop started event, got %+v", recent)
	}
	if started.Payload["team_id"] != "team-current" || started.Payload["start_reason"] != "sync_missing_loop" {
		t.Fatalf("unexpected loop started payload: %+v", started.Payload)
	}

	if err := store.UpdateTeamStatus(context.Background(), "team-current", team.TeamStatusPaused); err != nil {
		t.Fatalf("UpdateTeamStatus paused: %v", err)
	}
	lifecycle.SyncLoops()
	if lifecycle.hasTeamLoop("team-current") {
		t.Fatal("expected paused team loop to stop")
	}
	recent = host.EventBus.Recent(16)
	var stopped runtimeevents.Event
	for _, event := range recent {
		if event.Type == "team.orchestrator.loop.stopped" && event.Payload["stop_reason"] == "team_not_active" {
			stopped = event
			break
		}
	}
	if stopped.Type == "" {
		t.Fatalf("expected loop stopped event, got %+v", recent)
	}
	if stopped.Payload["team_id"] != "team-current" {
		t.Fatalf("unexpected loop stopped payload: %+v", stopped.Payload)
	}
}

func TestLocalTeamLifecycleService_SyncLoopsRepairsActiveTeamWithoutTeammates(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-repair",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusActive,
		MaxTeammates:  2,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	for _, id := range []string{"task-a", "task-b", "task-c"} {
		if _, err := store.CreateTask(context.Background(), team.Task{
			ID:     id,
			TeamID: teamID,
			Title:  id,
			Status: team.TaskStatusPending,
		}); err != nil {
			t.Fatalf("CreateTask %s: %v", id, err)
		}
	}

	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "lead-session"
	host := &localChatRuntimeHost{
		TeamStore: store,
		BaseSession: &ChatSession{
			RuntimeSession: runtimeSession,
		},
		Orchestrator: team.NewOrchestrator(store, nil, nil),
	}
	host.Orchestrator.TickInterval = time.Hour
	lifecycle := newLocalTeamLifecycleService(host)
	host.TeamLifecycle = lifecycle
	defer lifecycle.StopLoops()

	lifecycle.SyncLoops()

	teammates, err := store.ListTeammates(context.Background(), teamID)
	if err != nil {
		t.Fatalf("ListTeammates: %v", err)
	}
	if len(teammates) != 2 {
		t.Fatalf("expected repaired teammate records capped by max_teammates, got %+v", teammates)
	}
	if teammates[0].ID != "mate-1" || teammates[0].SessionID != "team-repair__mate_1" {
		t.Fatalf("unexpected first repaired teammate: %+v", teammates[0])
	}
	if teammates[1].ID != "mate-2" || teammates[1].SessionID != "team-repair__mate_2" {
		t.Fatalf("unexpected second repaired teammate: %+v", teammates[1])
	}
}

func TestLocalChatRuntimeHost_DispatchTeamLifecycleEventUsesTeamLeadSession(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-old",
		LeadSessionID: "old-session",
		Status:        team.TeamStatusActive,
	}); err != nil {
		t.Fatalf("CreateTeam old: %v", err)
	}
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-current",
		LeadSessionID: "current-session",
		Status:        team.TeamStatusActive,
	}); err != nil {
		t.Fatalf("CreateTeam current: %v", err)
	}

	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "current-session"
	eventStore := runtimechat.NewInMemoryRuntimeStore(16)
	lifecycle := &recordingTeamLifecycleService{}
	host := &localChatRuntimeHost{
		EventBus:      runtimeevents.NewBusWithRetention(16),
		EventStore:    eventStore,
		TeamStore:     store,
		TeamLifecycle: lifecycle,
		BaseSession: &ChatSession{
			RuntimeSession: runtimeSession,
		},
	}

	host.dispatchTeamLifecycleEvent(team.TeamEvent{
		Type:   "task.failed",
		TeamID: "team-old",
		Payload: map[string]interface{}{
			"task_id": "task-old",
			"summary": "stale event",
		},
	}, true)

	oldEvents, err := eventStore.ListEvents(context.Background(), "old-session", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents old: %v", err)
	}
	if len(oldEvents) != 1 || oldEvents[0].SessionID != "old-session" {
		t.Fatalf("expected stale event persisted to old lead session, got %+v", oldEvents)
	}
	currentEvents, err := eventStore.ListEvents(context.Background(), "current-session", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents current: %v", err)
	}
	if len(currentEvents) != 0 {
		t.Fatalf("expected no stale event in current session, got %+v", currentEvents)
	}
	if len(lifecycle.applied) != 0 {
		t.Fatalf("expected foreign team event not to apply to current lifecycle, got %+v", lifecycle.applied)
	}
	if recent := host.EventBus.Recent(10); len(recent) != 0 {
		t.Fatalf("expected foreign team event not to publish to current bus, got %+v", recent)
	}

	host.dispatchTeamLifecycleEvent(team.TeamEvent{
		Type:   team.TaskRouteResolvedEvent,
		TeamID: "team-old",
		Payload: map[string]interface{}{
			"task_id":        "task-old-route",
			"route_provider": "openai",
			"route_model":    "gpt-test",
		},
	}, true)

	oldEvents, err = eventStore.ListEvents(context.Background(), "old-session", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents old after route event: %v", err)
	}
	if len(oldEvents) != 2 || oldEvents[1].Type != team.TaskRouteResolvedEvent || oldEvents[1].SessionID != "old-session" {
		t.Fatalf("expected foreign route event persisted to old lead session, got %+v", oldEvents)
	}
	if len(lifecycle.applied) != 0 {
		t.Fatalf("expected foreign route event not to apply to current lifecycle, got %+v", lifecycle.applied)
	}
	recent := host.EventBus.Recent(10)
	if len(recent) != 1 || recent[0].Type != team.TaskRouteResolvedEvent || recent[0].SessionID != "old-session" {
		t.Fatalf("expected foreign route event published to runtime bus once, got %+v", recent)
	}

	host.dispatchTeamLifecycleEvent(team.TeamEvent{
		Type:   "task.completed",
		TeamID: "team-current",
		Payload: map[string]interface{}{
			"task_id": "task-current",
			"summary": "done",
		},
	}, true)

	currentEvents, err = eventStore.ListEvents(context.Background(), "current-session", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents current after dispatch: %v", err)
	}
	if len(currentEvents) != 1 || currentEvents[0].SessionID != "current-session" {
		t.Fatalf("expected current team event in current session, got %+v", currentEvents)
	}
	if len(lifecycle.applied) != 1 || lifecycle.applied[0].Type != "task.completed" {
		t.Fatalf("expected current team event applied once, got %+v", lifecycle.applied)
	}
}

func TestLocalChatRuntimeHost_TeamCompletedClosesNonLeadTeammateSessions(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	const (
		teamID               = "team-cleanup"
		leadSessionID        = "lead-session"
		startedMateSessionID = "mate-started-session"
		idleMateSessionID    = "mate-idle-session"
	)
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            teamID,
		LeadSessionID: leadSessionID,
		Status:        team.TeamStatusDone,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.UpsertTeammate(context.Background(), team.Teammate{
		ID:        "mate-started",
		TeamID:    teamID,
		SessionID: startedMateSessionID,
		State:     team.TeammateStateBusy,
	}); err != nil {
		t.Fatalf("UpsertTeammate started: %v", err)
	}
	if _, err := store.UpsertTeammate(context.Background(), team.Teammate{
		ID:        "mate-idle",
		TeamID:    teamID,
		SessionID: idleMateSessionID,
		State:     team.TeammateStateIdle,
	}); err != nil {
		t.Fatalf("UpsertTeammate idle: %v", err)
	}

	sessionStore := runtimechat.NewInMemoryStorage()
	for _, sessionID := range []string{leadSessionID, startedMateSessionID, idleMateSessionID} {
		session := runtimechat.NewSession("tester")
		session.ID = sessionID
		if err := sessionStore.Save(context.Background(), session); err != nil {
			t.Fatalf("sessionStore.Save(%s): %v", sessionID, err)
		}
	}

	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(16),
		SessionStore: sessionStore,
		TeamStore:    store,
	}
	host.SessionHub = buildCleanupTestSessionHub(t, host, sessionStore)
	host.ActorRegistry = newLocalActorRegistry(host)

	leadActor, err := host.SessionHub.GetOrCreate(leadSessionID)
	if err != nil {
		t.Fatalf("GetOrCreate lead: %v", err)
	}
	leadActor.Start()
	startedMateActor, err := host.SessionHub.GetOrCreate(startedMateSessionID)
	if err != nil {
		t.Fatalf("GetOrCreate started mate: %v", err)
	}
	startedMateActor.Start()
	if _, err := host.SessionHub.GetOrCreate(idleMateSessionID); err != nil {
		t.Fatalf("GetOrCreate idle mate: %v", err)
	}

	host.publishTeamLifecycleEvent(team.TeamEvent{
		Type:   "team.completed",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"status": string(team.TeamStatusDone),
		},
	})
	host.publishTeamLifecycleEvent(team.TeamEvent{
		Type:   "team.completed",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"status": string(team.TeamStatusDone),
		},
	})

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, startedExists := host.SessionHub.Get(startedMateSessionID)
		_, idleExists := host.SessionHub.Get(idleMateSessionID)
		startedSession, startedErr := sessionStore.Load(context.Background(), startedMateSessionID)
		idleSession, idleErr := sessionStore.Load(context.Background(), idleMateSessionID)
		if !startedExists && !idleExists &&
			startedErr == nil && startedSession != nil && startedSession.State == runtimechat.StateClosed &&
			idleErr == nil && idleSession != nil && idleSession.State == runtimechat.StateClosed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for teammate cleanup: startedExists=%v idleExists=%v startedState=%v idleState=%v startedErr=%v idleErr=%v",
				startedExists, idleExists,
				sessionStateString(startedSession), sessionStateString(idleSession),
				startedErr, idleErr,
			)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, ok := host.SessionHub.Get(leadSessionID); !ok {
		t.Fatal("expected lead actor to remain active in session hub")
	}
	leadSession, err := sessionStore.Load(context.Background(), leadSessionID)
	if err != nil {
		t.Fatalf("sessionStore.Load(lead): %v", err)
	}
	if leadSession.State == runtimechat.StateClosed {
		t.Fatalf("expected lead session to remain open, got state %s", leadSession.State)
	}

	teammates, err := store.ListTeammates(context.Background(), teamID)
	if err != nil {
		t.Fatalf("ListTeammates: %v", err)
	}
	if len(teammates) != 2 {
		t.Fatalf("expected two teammates, got %+v", teammates)
	}
	states := map[string]team.TeammateState{}
	for _, teammate := range teammates {
		states[teammate.ID] = teammate.State
	}
	if states["mate-started"] != team.TeammateStateBusy {
		t.Fatalf("expected started teammate state to remain busy, got %+v", teammates)
	}
	if states["mate-idle"] != team.TeammateStateIdle {
		t.Fatalf("expected idle teammate state to remain idle, got %+v", teammates)
	}
}

func TestLocalTeamLifecycleService_DelaysTeammateCleanupUntilRuntimeIdle(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	const (
		teamID        = "team-cleanup-waits"
		leadSessionID = "lead-session"
		mateSessionID = "mate-session"
	)
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            teamID,
		LeadSessionID: leadSessionID,
		Status:        team.TeamStatusDone,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.UpsertTeammate(context.Background(), team.Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: mateSessionID,
		State:     team.TeammateStateIdle,
	}); err != nil {
		t.Fatalf("UpsertTeammate: %v", err)
	}

	sessionStore := runtimechat.NewInMemoryStorage()
	for _, sessionID := range []string{leadSessionID, mateSessionID} {
		session := runtimechat.NewSession("tester")
		session.ID = sessionID
		if err := sessionStore.Save(context.Background(), session); err != nil {
			t.Fatalf("sessionStore.Save(%s): %v", sessionID, err)
		}
	}

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	if err := runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: mateSessionID,
		Status:    runtimechat.SessionRunning,
	}); err != nil {
		t.Fatalf("SaveState running: %v", err)
	}

	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(16),
		RuntimeStore: runtimeStore,
		SessionStore: sessionStore,
		TeamStore:    store,
	}
	host.SessionHub = buildCleanupTestSessionHub(t, host, sessionStore)
	host.ActorRegistry = newLocalActorRegistry(host)
	lifecycle := newLocalTeamLifecycleService(host)
	host.TeamLifecycle = lifecycle

	if _, err := host.SessionHub.GetOrCreate(mateSessionID); err != nil {
		t.Fatalf("GetOrCreate mate: %v", err)
	}

	lifecycle.closeTerminalTeammatesAsync(teamID)
	time.Sleep(250 * time.Millisecond)

	if _, exists := host.SessionHub.Get(mateSessionID); !exists {
		t.Fatal("expected running teammate actor to stay open while runtime state is running")
	}
	mateSession, err := sessionStore.Load(context.Background(), mateSessionID)
	if err != nil {
		t.Fatalf("sessionStore.Load mate: %v", err)
	}
	if mateSession.State == runtimechat.StateClosed {
		t.Fatalf("expected running teammate session to stay open, got %s", mateSession.State)
	}

	if err := runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: mateSessionID,
		Status:    runtimechat.SessionIdle,
	}); err != nil {
		t.Fatalf("SaveState idle: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, exists := host.SessionHub.Get(mateSessionID)
		closedSession, loadErr := sessionStore.Load(context.Background(), mateSessionID)
		if !exists && loadErr == nil && closedSession != nil && closedSession.State == runtimechat.StateClosed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for cleanup after idle: exists=%v session=%+v err=%v", exists, closedSession, loadErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLocalChatRuntimeHost_ReplayedTerminalEventClosesNonLeadTeammateSessions(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	const (
		teamID        = "team-replay-cleanup"
		leadSessionID = "lead-session"
		mateSessionID = "mate-session"
	)
	if _, err := store.CreateTeam(context.Background(), team.Team{
		ID:            teamID,
		LeadSessionID: leadSessionID,
		Status:        team.TeamStatusDone,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.UpsertTeammate(context.Background(), team.Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: mateSessionID,
		State:     team.TeammateStateIdle,
	}); err != nil {
		t.Fatalf("UpsertTeammate: %v", err)
	}
	if _, err := store.AppendTeamEvent(context.Background(), team.TeamEvent{
		Type:   "team.completed",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"status": string(team.TeamStatusDone),
		},
	}); err != nil {
		t.Fatalf("AppendTeamEvent: %v", err)
	}

	sessionStore := runtimechat.NewInMemoryStorage()
	for _, sessionID := range []string{leadSessionID, mateSessionID} {
		session := runtimechat.NewSession("tester")
		session.ID = sessionID
		if err := sessionStore.Save(context.Background(), session); err != nil {
			t.Fatalf("sessionStore.Save(%s): %v", sessionID, err)
		}
	}

	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(16),
		SessionStore: sessionStore,
		TeamStore:    store,
	}
	host.SessionHub = buildCleanupTestSessionHub(t, host, sessionStore)
	host.ActorRegistry = newLocalActorRegistry(host)

	if _, err := host.SessionHub.GetOrCreate(leadSessionID); err != nil {
		t.Fatalf("GetOrCreate lead: %v", err)
	}
	if _, err := host.SessionHub.GetOrCreate(mateSessionID); err != nil {
		t.Fatalf("GetOrCreate mate: %v", err)
	}

	host.replayStoredTerminalTeamLifecycleEvents(teamID)

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, mateExists := host.SessionHub.Get(mateSessionID)
		mateSession, mateErr := sessionStore.Load(context.Background(), mateSessionID)
		if !mateExists && mateErr == nil && mateSession != nil && mateSession.State == runtimechat.StateClosed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for replayed teammate cleanup: mateExists=%v mateState=%v mateErr=%v",
				mateExists, sessionStateString(mateSession), mateErr,
			)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, ok := host.SessionHub.Get(leadSessionID); !ok {
		t.Fatal("expected lead actor to remain active after replayed cleanup")
	}
	leadSession, err := sessionStore.Load(context.Background(), leadSessionID)
	if err != nil {
		t.Fatalf("sessionStore.Load(lead): %v", err)
	}
	if leadSession.State == runtimechat.StateClosed {
		t.Fatalf("expected lead session to remain open, got state %s", leadSession.State)
	}
}

func TestLocalChatRuntimeHost_DelegatesToConfiguredTeamLifecycleService(t *testing.T) {
	lifecycle := &recordingTeamLifecycleService{runSettledResult: true}
	host := &localChatRuntimeHost{
		EventBus:      runtimeevents.NewBusWithRetention(8),
		TeamLifecycle: lifecycle,
		SessionStore:  runtimechat.NewInMemoryStorage(),
	}

	host.publishTeamLifecycleEvent(team.TeamEvent{
		Type:   "team.completed",
		TeamID: "team-1",
		Payload: map[string]interface{}{
			"status": string(team.TeamStatusDone),
		},
	})
	host.replayStoredTerminalTeamLifecycleEvents("team-2")
	if err := host.waitForTeamTerminal(context.Background(), "team-3"); err != nil {
		t.Fatalf("waitForTeamTerminal: %v", err)
	}
	settled, err := host.teamRunSettled(context.Background(), "team-4")
	if err != nil {
		t.Fatalf("teamRunSettled: %v", err)
	}
	if !settled {
		t.Fatal("expected delegated runSettled result")
	}
	host.syncTeamLifecycleLoops()
	host.stopTeamLifecycleLoops()

	if len(lifecycle.applied) != 1 {
		t.Fatalf("expected one delegated runtime event, got %+v", lifecycle.applied)
	}
	if lifecycle.applied[0].Type != "team.completed" {
		t.Fatalf("unexpected delegated event: %+v", lifecycle.applied[0])
	}
	if got := payloadStringValue(lifecycle.applied[0].Payload["team_id"]); got != "team-1" {
		t.Fatalf("expected delegated team id, got %q", got)
	}
	if len(lifecycle.replayedTeamIDs) != 1 || lifecycle.replayedTeamIDs[0] != "team-2" {
		t.Fatalf("expected replay delegation for team-2, got %+v", lifecycle.replayedTeamIDs)
	}
	if len(lifecycle.waitedTeamIDs) != 1 || lifecycle.waitedTeamIDs[0] != "team-3" {
		t.Fatalf("expected wait delegation for team-3, got %+v", lifecycle.waitedTeamIDs)
	}
	if len(lifecycle.settledTeamIDs) != 1 || lifecycle.settledTeamIDs[0] != "team-4" {
		t.Fatalf("expected runSettled delegation for team-4, got %+v", lifecycle.settledTeamIDs)
	}
	if lifecycle.syncCalls != 1 {
		t.Fatalf("expected sync delegation once, got %d", lifecycle.syncCalls)
	}
	if lifecycle.stopCalls != 1 {
		t.Fatalf("expected stop delegation once, got %d", lifecycle.stopCalls)
	}
}

func buildCleanupTestSessionHub(t *testing.T, host *localChatRuntimeHost, sessionStore runtimechat.SessionStorage) *runtimechat.SessionHub {
	t.Helper()

	provider := runtimellm.NewMockProvider("mock", 0)
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "mock",
		DefaultModel:    "mock-model",
	})
	if err := llmRuntime.RegisterProvider("mock", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("mock-model", "mock"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	return runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
		apiAgent := agent.NewAgentWithLLM(&agent.Config{
			Name:     "cleanup-test",
			Provider: "mock",
			Model:    "mock-model",
			MaxSteps: 1,
		}, nil, llmRuntime)
		apiAgent.SetToolExecutionPolicy(runtimepolicy.NewToolExecutionPolicy([]string{}, false))
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        apiAgent,
			LLMRuntime:   llmRuntime,
			SessionStore: sessionStore,
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})
}

func sessionStateString(session *runtimechat.Session) string {
	if session == nil {
		return "<nil>"
	}
	return string(session.State)
}

type recordingTeamLifecycleService struct {
	applied          []runtimeevents.Event
	replayedTeamIDs  []string
	waitedTeamIDs    []string
	settledTeamIDs   []string
	pendingTeamIDs   []string
	syncCalls        int
	stopCalls        int
	runSettledResult bool
	runSettledErr    error
	waitErr          error
	pendingResult    bool
}

func (c *recordingTeamLifecycleService) Apply(event runtimeevents.Event) {
	c.applied = append(c.applied, event)
}

func (c *recordingTeamLifecycleService) PublishStoredTerminalEvents(teamID string) {
	c.replayedTeamIDs = append(c.replayedTeamIDs, teamID)
}

func (c *recordingTeamLifecycleService) WaitForTerminal(ctx context.Context, teamID string) error {
	c.waitedTeamIDs = append(c.waitedTeamIDs, teamID)
	return c.waitErr
}

func (c *recordingTeamLifecycleService) RunSettled(ctx context.Context, teamID string) (bool, error) {
	c.settledTeamIDs = append(c.settledTeamIDs, teamID)
	return c.runSettledResult, c.runSettledErr
}

func (c *recordingTeamLifecycleService) Pending(ctx context.Context, teamID string) bool {
	c.pendingTeamIDs = append(c.pendingTeamIDs, teamID)
	return c.pendingResult
}

func (c *recordingTeamLifecycleService) SyncLoops() {
	c.syncCalls++
}

func (c *recordingTeamLifecycleService) StopLoops() {
	c.stopCalls++
}

func TestFindGitRoot(t *testing.T) {
	// The repo itself has a .git at the root.
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// testFile is backend/cmd/aicli/commands/chat_actor_host_test.go
	// Walk up to the git root (E:\projects\ai\ai-agent-runtime)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", "..", "..", ".."))

	got := findGitRoot(filepath.Dir(testFile))
	if filepath.Clean(got) != repoRoot {
		t.Fatalf("findGitRoot from test dir = %q, want %q", got, repoRoot)
	}

	got = findGitRoot(filepath.Join(t.TempDir(), "a", "b"))
	if got != "" {
		t.Fatalf("findGitRoot in temp dir = %q, want empty", got)
	}
}
func TestLocalChatPrepareRunHookFreezesSystemPromptAcrossRuns(t *testing.T) {
	llmRuntime := runtimellm.NewLLMRuntime(nil)
	runtimeSession := &runtimechat.Session{ID: "freeze-session"}
	session := &ChatSession{
		RuntimeSession: runtimeSession,
		PermissionMode: runtimepolicy.ModeDefault,
	}
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "test-agent",
		Provider:     "test-provider",
		Model:        "test-model",
		SystemPrompt: "",
	}, nil, llmRuntime)

	// First prepare anchors the outbound head while the workspace root is not
	// resolved yet (the observed turn-1 state).
	hook := localChatPrepareRunHook(apiAgent, session, "", true)
	if hook == nil {
		t.Fatal("expected prepare hook for base session")
	}
	ctx := context.Background()
	if err := hook(ctx, runtimeSession, false); err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	first := apiAgent.GetConfig().SystemPrompt
	if strings.TrimSpace(first) == "" {
		t.Fatal("expected composed system prompt on first prepare")
	}
	if strings.Contains(first, "Current workspace root:") {
		t.Fatalf("first prepare must not carry the later-resolved workspace paragraph, got %q", first)
	}

	// A later run resolves the workspace root only now. The provider-facing
	// instruction head must stay byte-identical: rewriting messages[0] here is
	// exactly the turn-boundary prefix break that invalidated the provider
	// prompt cache mid-session.
	hook2 := localChatPrepareRunHook(apiAgent, session, `E:\projects\ai\ai-gateway`, true)
	if err := hook2(ctx, runtimeSession, true); err != nil {
		t.Fatalf("second prepare: %v", err)
	}
	second := apiAgent.GetConfig().SystemPrompt
	if second != first {
		t.Fatalf("system prompt must be frozen across prepare runs: first=%q second=%q", first, second)
	}
	if strings.Contains(second, "Current workspace root:") {
		t.Fatalf("later workspace resolution must not rewrite the frozen head, got %q", second)
	}

	// The anchor is persisted on the session context so actor rebuilds after
	// warmup/model reselection reuse the identical frozen head.
	anchored := sessionmeta.String(runtimeSession.Metadata.Context, sessionmeta.SystemPromptFrozen)
	if anchored != first {
		t.Fatalf("expected session-anchored frozen prompt, got %q want %q", anchored, first)
	}
}

func TestLocalChatRuntimeHostCloseWaitsForSubagentOperation(t *testing.T) {
	host := &localChatRuntimeHost{}
	cleanupDone := make(chan struct{})
	host.cleanupFns = []func(){func() { close(cleanupDone) }}

	release, ok := host.beginSubagentOperation()
	if !ok {
		t.Fatal("beginSubagentOperation unexpectedly rejected an open host")
	}
	closeDone := make(chan struct{})
	go func() {
		host.Close()
		close(closeDone)
	}()

	select {
	case <-cleanupDone:
		t.Fatal("host cleanup ran while subagent operation was still active")
	case <-time.After(25 * time.Millisecond):
	}
	release()

	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("host Close did not finish after subagent operation released")
	}
	select {
	case <-cleanupDone:
	default:
		t.Fatal("host cleanup did not run after Close")
	}
}
