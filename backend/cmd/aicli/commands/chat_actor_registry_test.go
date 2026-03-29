package commands

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func TestLocalActorRegistry_SubmitPromptUsesSessionHub(t *testing.T) {
	registry := newLocalActorRegistry(&localChatRuntimeHost{
		SessionHub: buildTestSessionHub(t),
	})

	result, err := registry.SubmitPrompt(context.Background(), "session-1", "inspect", nil)
	if err != nil {
		t.Fatalf("SubmitPrompt failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected session result")
	}
	if result.Output != "inspection complete" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

func TestLocalActorRegistry_DispatchTeamMailboxMessageRoutesToActor(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-1",
		LeadSessionID: "session-1",
		Status:        team.TeamStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	host := &localChatRuntimeHost{
		SessionHub: buildTestSessionHub(t),
		TeamStore:  store,
	}
	registry := newLocalActorRegistry(host)

	actor, err := host.SessionHub.GetOrCreate("session-1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	eventCh := make(chan runtimeevents.Event, 1)
	if err := actor.SubscribeEvents(context.Background(), runtimechat.EventMailboxReceived, eventCh); err != nil {
		t.Fatalf("SubscribeEvents: %v", err)
	}

	err = registry.DispatchTeamMailboxMessage(context.Background(), team.MailMessage{
		ID:        "mail-1",
		TeamID:    teamID,
		FromAgent: "planner",
		ToAgent:   "lead",
		Kind:      "progress",
		Body:      "hello lead",
	})
	if err != nil {
		t.Fatalf("DispatchTeamMailboxMessage failed: %v", err)
	}

	select {
	case event := <-eventCh:
		if event.Type != runtimechat.EventMailboxReceived {
			t.Fatalf("unexpected event type: %s", event.Type)
		}
		if got := event.Payload["message_id"]; got != "mail-1" {
			t.Fatalf("unexpected message id: %v", got)
		}
		if got := event.Payload["from_agent"]; got != "planner" {
			t.Fatalf("unexpected from agent: %v", got)
		}
		if got := event.Payload["kind"]; got != "progress" {
			t.Fatalf("unexpected kind: %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for mailbox event")
	}
}

func TestLocalActorRegistry_AllocatesMissingTeammateSessionIDs(t *testing.T) {
	resolved := ensureTeammateSessionIDs("team-1", []toolbroker.SpawnTeammateSpec{
		{Name: "writer"},
		{ID: "reviewer"},
		{Name: "writer"},
	})

	if len(resolved) != 3 {
		t.Fatalf("unexpected teammate count: %d", len(resolved))
	}
	if resolved[0].SessionID == "" || resolved[1].SessionID == "" || resolved[2].SessionID == "" {
		t.Fatalf("expected missing session ids to be filled: %+v", resolved)
	}
	if resolved[1].SessionID != "team-1__reviewer" {
		t.Fatalf("expected deterministic session id, got %q", resolved[1].SessionID)
	}
	if resolved[0].SessionID == resolved[2].SessionID {
		t.Fatalf("expected duplicate teammate names to get unique session ids: %+v", resolved)
	}
}

func TestLocalActorRegistry_RewritesCurrentTeammateSessionPlaceholder(t *testing.T) {
	resolved := ensureTeammateSessionIDs("team-1", []toolbroker.SpawnTeammateSpec{
		{ID: "writer", SessionID: "current"},
	})

	if len(resolved) != 1 {
		t.Fatalf("unexpected teammate count: %d", len(resolved))
	}
	if resolved[0].SessionID != "team-1__writer" {
		t.Fatalf("expected current placeholder to be replaced, got %+v", resolved[0])
	}
}

func buildTestSessionHub(t *testing.T) *runtimechat.SessionHub {
	t.Helper()

	sessionStore := runtimechat.NewInMemoryStorage()
	session := runtimechat.NewSession("tester")
	session.ID = "session-1"
	if err := sessionStore.Save(context.Background(), session); err != nil {
		t.Fatalf("sessionStore.Save: %v", err)
	}

	provider := runtimellm.NewMockProvider("mock", 0)
	provider.SetResponse("inspect", "inspection complete")
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
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "test-actor",
			Provider: "mock",
			Model:    "mock-model",
			MaxSteps: 4,
		}, nil, llmRuntime)
		a.SetToolExecutionPolicy(runtimepolicy.NewToolExecutionPolicy([]string{}, false))
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: sessionStore,
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     runtimeevents.NewBusWithRetention(32),
		})
	})
}
