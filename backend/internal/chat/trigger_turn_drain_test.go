package chat

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// P0-3b trigger_turn drain tests (docs/plan/
// supervision-parent-child-control-optimization-plan-20260917.md §3.3 改动 2).

type triggerDrainHarness struct {
	actor   *SessionActor
	store   *InMemoryRuntimeStore
	storage *InMemoryStorage
	session *Session
}

func newTriggerDrainHarness(t *testing.T) *triggerDrainHarness {
	t.Helper()
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)
	session, err := manager.CreateSession(ctx, "trigger-drain-user")
	require.NoError(t, err)

	store := NewInMemoryRuntimeStore(64)
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "gpt-4", MaxRetries: 1})
	provider := NewMockLLMProviderForChat()
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-4", provider.Name()))
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "trigger-drain-agent",
		Model:    "gpt-4",
		MaxSteps: 3,
	}, nil, runtime)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:            apiAgent,
		LLMRuntime:       runtime,
		SessionStore:     storage,
		StateStore:       store,
		EventStore:       store,
		TriggerTurnDrain: true,
	})
	require.NoError(t, err)
	t.Cleanup(actor.Stop)
	return &triggerDrainHarness{actor: actor, store: store, storage: storage, session: session}
}

func (h *triggerDrainHarness) queueTrigger(t *testing.T, id, from, body string) {
	t.Helper()
	mail := toolbroker.BuildAgentMailboxMessage(from, h.session.ID, body, true)
	if id != "" {
		mail.ID = id
	}
	_, _, err := h.store.AppendAgentControlMailbox(context.Background(), h.session.ID, mail)
	require.NoError(t, err)
}

func (h *triggerDrainHarness) events(t *testing.T) []runtimeevents.Event {
	t.Helper()
	events, err := h.store.ListEvents(context.Background(), h.session.ID, 0, 0)
	require.NoError(t, err)
	return events
}

func countEventType(events []runtimeevents.Event, eventType string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func findEventType(events []runtimeevents.Event, eventType string) *runtimeevents.Event {
	for index := range events {
		if events[index].Type == eventType {
			return &events[index]
		}
	}
	return nil
}

func (h *triggerDrainHarness) history(t *testing.T) []runtimetypes.Message {
	t.Helper()
	session, err := h.storage.Load(context.Background(), h.session.ID)
	require.NoError(t, err)
	require.NotNil(t, session)
	return session.History
}

func historyContainsUserText(history []runtimetypes.Message, fragments ...string) bool {
	for _, message := range history {
		if message.Role != "user" {
			continue
		}
		matched := true
		for _, fragment := range fragments {
			if !strings.Contains(message.Content, fragment) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func TestTriggerTurnDrainConsumesMergedInstructionsExactlyOnce(t *testing.T) {
	h := newTriggerDrainHarness(t)
	ctx := context.Background()
	h.queueTrigger(t, "trigger-1", "parent-a", "please run the first check")
	h.queueTrigger(t, "trigger-2", "parent-b", "then run the second check")
	// A plain send_message (trigger_turn=false) must never start a turn.
	plain := toolbroker.BuildAgentMailboxMessage("parent-a", h.session.ID, "just fyi", false)
	_, _, err := h.store.AppendAgentControlMailbox(ctx, h.session.ID, plain)
	require.NoError(t, err)

	h.actor.drainTriggerTurnMailbox()

	require.Eventually(t, func() bool {
		return countEventType(h.events(t), EventTriggerTurnConsumed) == 1
	}, 5*time.Second, 20*time.Millisecond, "drain must publish exactly one consumed event")
	consumed := findEventType(h.events(t), EventTriggerTurnConsumed)
	require.NotNil(t, consumed)
	require.ElementsMatch(t, []string{"trigger-1", "trigger-2"}, payloadStringList(consumed.Payload["message_ids"]))
	require.EqualValues(t, 2, consumed.Payload["count"])

	require.Eventually(t, func() bool {
		return historyContainsUserText(h.history(t), "please run the first check", "then run the second check")
	}, 5*time.Second, 20*time.Millisecond, "merged trigger prompt must reach the child session")
	// Sources are annotated when more than one instruction is merged.
	require.True(t, historyContainsUserText(h.history(t), "from: parent-a", "from: parent-b", "instruction 1/2", "instruction 2/2"))

	// Idempotency: a repeated drain has zero side effects.
	// Wait for the first turn to fully complete (session idle) so the LLM
	// response is already in history and any run-end drain has settled.
	require.Eventually(t, func() bool {
		state, ok := h.actor.StateSummary()
		return ok && !state.Busy()
	}, 5*time.Second, 20*time.Millisecond, "first turn must complete before idempotency check")
	historyBefore := len(h.history(t))
	h.actor.drainTriggerTurnMailbox()
	// Allow any async run-end drain to settle, then verify idempotency holds.
	require.Eventually(t, func() bool {
		return countEventType(h.events(t), EventTriggerTurnConsumed) == 1
	}, 5*time.Second, 20*time.Millisecond, "idempotent drain must not publish a second consumed event")
	require.Equal(t, historyBefore, len(h.history(t)))
}

func TestTriggerTurnDrainRestartLedgerSkipsAlreadyConsumedMessages(t *testing.T) {
	h := newTriggerDrainHarness(t)
	ctx := context.Background()
	h.queueTrigger(t, "trigger-restart", "parent-a", "do not trigger twice after restart")
	// Simulate a drain that already consumed this row before the actor restarted.
	_, err := h.store.AppendEvent(ctx, runtimeevents.Event{
		Type:      EventTriggerTurnConsumed,
		SessionID: h.session.ID,
		Payload: map[string]interface{}{
			"message_ids": []string{"trigger-restart"},
			"mailbox_seq": int64(1),
		},
	})
	require.NoError(t, err)

	h.actor.drainTriggerTurnMailbox()
	time.Sleep(300 * time.Millisecond)

	require.Equal(t, 1, countEventType(h.events(t), EventTriggerTurnConsumed), "seeded marker must be the only consumed event")
	require.False(t, historyContainsUserText(h.history(t), "do not trigger twice after restart"))
}

func TestTriggerTurnDrainRateLimitAndLoopGuardKeepMessagesInMailbox(t *testing.T) {
	h := newTriggerDrainHarness(t)
	ctx := context.Background()
	h.queueTrigger(t, "trigger-rate", "parent-a", "rate limited instruction")

	// Same child auto-triggered just now: the 30s floor rejects this drain.
	h.actor.triggerTurnMu.Lock()
	h.actor.triggerTurnLastAutoAt = time.Now().UTC()
	h.actor.triggerTurnMu.Unlock()
	h.actor.drainTriggerTurnMailbox()
	// A second immediate attempt is throttled: no duplicated dropped event.
	h.actor.drainTriggerTurnMailbox()

	require.Equal(t, 0, countEventType(h.events(t), EventTriggerTurnConsumed))
	require.Equal(t, 1, countEventType(h.events(t), EventTriggerTurnDropped))
	dropped := findEventType(h.events(t), EventTriggerTurnDropped)
	require.NotNil(t, dropped)
	require.Equal(t, "min_interval", dropped.Payload["reason"])
	require.ElementsMatch(t, []string{"trigger-rate"}, payloadStringList(dropped.Payload["message_ids"]))
	require.False(t, historyContainsUserText(h.history(t), "rate limited instruction"))

	// The instruction stays durable in the mailbox for a later retry.
	messages, err := h.store.ListMailbox(ctx, h.session.ID, 0, 10)
	require.NoError(t, err)
	found := false
	for _, message := range messages {
		if strings.TrimSpace(message.ID) == "trigger-rate" {
			found = true
		}
	}
	require.True(t, found, "rate-limited instruction must not be dropped from the mailbox")

	// After the cooldown the consecutive-turn ceiling is the next gate.
	h.actor.triggerTurnMu.Lock()
	h.actor.triggerTurnLastAutoAt = time.Now().UTC().Add(-time.Minute)
	h.actor.triggerTurnConsecutive = TriggerTurnDrainMaxConsecutive
	h.actor.triggerTurnMu.Unlock()
	h.actor.drainTriggerTurnMailbox()
	require.Equal(t, 2, countEventType(h.events(t), EventTriggerTurnDropped))
	require.Equal(t, 0, countEventType(h.events(t), EventTriggerTurnConsumed))
	last := h.events(t)
	dropped = findEventType(last[len(last)-1:], EventTriggerTurnDropped)
	require.NotNil(t, dropped)
	require.Equal(t, "consecutive_limit", dropped.Payload["reason"])
}

func TestTriggerTurnDrainWithoutPendingMessagesIsNoop(t *testing.T) {
	h := newTriggerDrainHarness(t)
	plain := toolbroker.BuildAgentMailboxMessage("parent-a", h.session.ID, "no trigger here", false)
	_, _, err := h.store.AppendAgentControlMailbox(context.Background(), h.session.ID, plain)
	require.NoError(t, err)

	h.actor.drainTriggerTurnMailbox()
	time.Sleep(200 * time.Millisecond)

	require.Equal(t, 0, countEventType(h.events(t), EventTriggerTurnConsumed))
	require.Equal(t, 0, countEventType(h.events(t), EventTriggerTurnDropped))
	require.False(t, historyContainsUserText(h.history(t), "no trigger here"))
}

func TestMailboxMessageRequestsTriggerTurnAcceptsBoolAndString(t *testing.T) {
	require.True(t, MailboxMessageRequestsTriggerTurn(team.MailMessage{Metadata: map[string]interface{}{"trigger_turn": true}}))
	require.True(t, MailboxMessageRequestsTriggerTurn(team.MailMessage{Metadata: map[string]interface{}{"trigger_turn": "true"}}))
	require.False(t, MailboxMessageRequestsTriggerTurn(team.MailMessage{Metadata: map[string]interface{}{"trigger_turn": false}}))
	require.False(t, MailboxMessageRequestsTriggerTurn(team.MailMessage{Metadata: map[string]interface{}{"trigger_turn": "false"}}))
	require.False(t, MailboxMessageRequestsTriggerTurn(team.MailMessage{Metadata: map[string]interface{}{}}))
	require.False(t, MailboxMessageRequestsTriggerTurn(team.MailMessage{}))
}

func TestBuildTriggerTurnPromptAnnotatesAndBoundsSources(t *testing.T) {
	long := strings.Repeat("x", triggerTurnPromptPerMessageRuneCap+10)
	prompt := buildTriggerTurnPrompt([]team.MailMessage{
		{ID: "m1", FromAgent: "parent-a", Kind: "followup_task", Body: "first body"},
		{ID: "m2", FromAgent: "parent-b", Kind: "agent_message", Body: long},
	})
	require.Contains(t, prompt, "instruction 1/2")
	require.Contains(t, prompt, "instruction 2/2")
	require.Contains(t, prompt, "from: parent-a")
	require.Contains(t, prompt, "from: parent-b")
	require.Contains(t, prompt, "message_id: m1")
	require.Contains(t, prompt, "first body")
	require.Contains(t, prompt, "[truncated]")
	require.Less(t, len([]rune(prompt)), 3*triggerTurnPromptPerMessageRuneCap)
}

func TestSelectTriggerTurnMessagesSkipsConsumedAndHighWaterRows(t *testing.T) {
	messages := []team.MailMessage{
		{ID: "id-consumed", SessionMailboxSeq: 1, Body: "one", Metadata: map[string]interface{}{"trigger_turn": true}},
		{ID: "id-high-water", SessionMailboxSeq: 2, Body: "two", Metadata: map[string]interface{}{"trigger_turn": true}},
		{ID: "id-pending", SessionMailboxSeq: 3, Body: "three", Metadata: map[string]interface{}{"trigger_turn": true}},
		{ID: "id-plain", SessionMailboxSeq: 4, Body: "plain", Metadata: map[string]interface{}{"trigger_turn": false}},
	}
	pending := selectTriggerTurnMessages(messages, map[string]struct{}{"id-consumed": {}}, 2)
	require.Len(t, pending, 1)
	require.Equal(t, "id-pending", pending[0].ID)
}

// Busy followup → run 结束自动起 turn 的端到端用例（方案 §3.3 验收）：
// 第一条 run 卡在 provider 时投递 trigger_turn 指令，run 结束后 drain 消费并
// 自动提交第二条 turn，且只消费一次。
func TestTriggerTurnDrainStartsTurnAfterBusyRunEnds(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)
	session, err := manager.CreateSession(ctx, "trigger-drain-busy-user")
	require.NoError(t, err)

	store := NewInMemoryRuntimeStore(64)
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "gpt-4", MaxRetries: 1})
	provider := &blockingFirstCallProvider{
		MockLLMProviderForChat: NewMockLLMProviderForChat(),
		entered:                make(chan struct{}),
		release:                make(chan struct{}),
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-4", provider.Name()))
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "trigger-drain-busy-agent",
		Model:    "gpt-4",
		MaxSteps: 3,
	}, nil, runtime)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:            apiAgent,
		LLMRuntime:       runtime,
		SessionStore:     storage,
		StateStore:       store,
		EventStore:       store,
		TriggerTurnDrain: true,
	})
	require.NoError(t, err)
	t.Cleanup(actor.Stop)

	firstDone := make(chan error, 1)
	go func() {
		_, submitErr := actor.SubmitPrompt(ctx, "first task", nil)
		firstDone <- submitErr
	}()
	select {
	case <-provider.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first run did not reach the provider")
	}

	// A followup_task arrives while the child is busy.
	mail := toolbroker.BuildAgentMailboxMessage("parent-session", session.ID, "second task queued while busy", true)
	mail.ID = "trigger-busy-1"
	_, _, err = store.AppendAgentControlMailbox(ctx, session.ID, mail)
	require.NoError(t, err)

	close(provider.release)
	select {
	case submitErr := <-firstDone:
		require.NoError(t, submitErr)
	case <-time.After(10 * time.Second):
		t.Fatal("first run did not finish")
	}

	loadHistory := func() []runtimetypes.Message {
		loaded, loadErr := storage.Load(ctx, session.ID)
		require.NoError(t, loadErr)
		require.NotNil(t, loaded)
		return loaded.History
	}
	require.Eventually(t, func() bool {
		return historyContainsUserText(loadHistory(), "second task queued while busy")
	}, 10*time.Second, 25*time.Millisecond, "the drain must start a new turn for the queued instruction")
	require.Eventually(t, func() bool {
		events, listErr := store.ListEvents(ctx, session.ID, 0, 0)
		require.NoError(t, listErr)
		return countEventType(events, EventTriggerTurnConsumed) == 1
	}, 10*time.Second, 25*time.Millisecond, "the drain must consume the instruction exactly once")

	actor.triggerTurnMu.Lock()
	consecutive := actor.triggerTurnConsecutive
	actor.triggerTurnMu.Unlock()
	require.Equal(t, 1, consecutive, "one automatic turn was started")

	// The queued instruction must not leak into a second automatic turn.
	require.Eventually(t, func() bool {
		events, listErr := store.ListEvents(ctx, session.ID, 0, 0)
		require.NoError(t, listErr)
		return countEventType(events, EventAssistantMessage) >= 2
	}, 10*time.Second, 25*time.Millisecond, "the automatic turn must complete a model response")
	time.Sleep(300 * time.Millisecond)
	events, err := store.ListEvents(ctx, session.ID, 0, 0)
	require.NoError(t, err)
	require.Equal(t, 1, countEventType(events, EventTriggerTurnConsumed))
	require.Equal(t, 0, countEventType(events, EventTriggerTurnDropped))
}
