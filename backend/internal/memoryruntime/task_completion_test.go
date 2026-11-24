package memoryruntime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type recordingTaskCompletionConsumer struct {
	calls  int
	events []TaskCompletionEvent
}

func (c *recordingTaskCompletionConsumer) ConsumeTaskCompletion(_ context.Context, event TaskCompletionEvent) error {
	c.calls++
	c.events = append(c.events, event)
	return nil
}

func taskCompletionRuntime(t *testing.T) (*Runtime, *recordingTaskCompletionConsumer) {
	t.Helper()
	source := &fakeSource{sessions: []SessionRef{{
		ID: "task-session", SessionGeneration: 3, WorkspacePath: "/workspace",
		State: "active", LastActivityAt: time.Now().UTC(),
	}}}
	runtime, store := newRuntimeForTest(t, "shadow", source, &fakeExtractor{})
	runtime.Config.GenerationTrigger = string(TriggerTaskCompletion)
	consumer := &recordingTaskCompletionConsumer{}
	runtime.TaskCompletions = consumer
	_ = store
	return runtime, consumer
}

func validTaskCompletionEvent(runtime *Runtime) TaskCompletionEvent {
	return TaskCompletionEvent{
		Durable:           true,
		SourceEventID:     "team-event:team-1:42",
		SourceEventIDs:    []string{"team-event:team-1:42", "agent-control:task-1"},
		TaskID:            "task-1",
		TeamID:            "team-1",
		SessionID:         "task-session",
		SessionGeneration: 3,
		ProjectKey:        runtime.Store.ProjectKey(),
		Outcome:           "completed",
		Problem:           "bounded problem",
		Result:            "bounded result",
		EndedAt:           time.Now().UTC(),
	}
}

func TestHandleTaskCompletionRequiresDurableSourceIdentity(t *testing.T) {
	runtime, consumer := taskCompletionRuntime(t)

	event := validTaskCompletionEvent(runtime)
	event.Durable = false
	_, err := runtime.HandleTaskCompletionEvent(context.Background(), event)
	require.ErrorContains(t, err, "not marked durable")
	require.Zero(t, consumer.calls)

	event = validTaskCompletionEvent(runtime)
	event.SourceEventID = ""
	_, err = runtime.HandleTaskCompletionEvent(context.Background(), event)
	require.ErrorContains(t, err, "identity is incomplete")
	require.Zero(t, consumer.calls)
}

func TestHandleTaskCompletionValidatesGenerationAndDelegatesBoundedEvent(t *testing.T) {
	runtime, consumer := taskCompletionRuntime(t)
	event := validTaskCompletionEvent(runtime)

	result, err := runtime.HandleTaskCompletionEvent(context.Background(), event)
	require.NoError(t, err)
	require.True(t, result.Accepted)
	require.Equal(t, "durable_event_accepted", result.Reason)
	require.Equal(t, 1, consumer.calls)
	require.Len(t, consumer.events, 1)
	require.Equal(t, "success", consumer.events[0].Outcome)
	require.Equal(t, event.SourceEventIDs, consumer.events[0].SourceEventIDs)

	event.SessionGeneration = 2
	result, err = runtime.HandleTaskCompletionEvent(context.Background(), event)
	require.NoError(t, err)
	require.False(t, result.Accepted)
	require.Equal(t, "session_generation_mismatch", result.Reason)
	require.Equal(t, 1, consumer.calls)
}

func TestHandleTaskCompletionIsIndependentOfExtractionTrigger(t *testing.T) {
	runtime, consumer := taskCompletionRuntime(t)
	runtime.Config.GenerationTrigger = string(TriggerSessionEnd)

	result, err := runtime.HandleTaskCompletionEvent(context.Background(), validTaskCompletionEvent(runtime))
	require.NoError(t, err)
	require.True(t, result.Accepted,
		"durable task Episodes must not depend on the LLM extraction trigger")
	require.Equal(t, 1, consumer.calls)
}

func TestHandleTaskCompletionResolvesSessionGeneration(t *testing.T) {
	runtime, consumer := taskCompletionRuntime(t)
	event := validTaskCompletionEvent(runtime)
	event.SessionGeneration = 0

	result, err := runtime.HandleTaskCompletionEvent(context.Background(), event)
	require.NoError(t, err)
	require.True(t, result.Accepted)
	require.Equal(t, 1, consumer.calls)
	require.EqualValues(t, 3, consumer.events[0].SessionGeneration,
		"the canonical session identity supplies the generation when the event omits it")
}

func TestStoreTaskCompletionConsumerCreatesDurableEpisode(t *testing.T) {
	runtime, store := newRuntimeForTest(t, "shadow", &fakeSource{sessions: []SessionRef{{
		ID: "task-session", SessionGeneration: 3, WorkspacePath: "/workspace",
		State: "active", LastActivityAt: time.Now().UTC(),
	}}}, &fakeExtractor{})
	runtime.Config.GenerationTrigger = string(TriggerTaskCompletion)
	runtime.TaskCompletions = NewStoreTaskCompletionConsumer(store)

	event := validTaskCompletionEvent(runtime)
	result, err := runtime.HandleTaskCompletionEvent(context.Background(), event)
	require.NoError(t, err)
	require.True(t, result.Accepted)

	episodes, err := store.ListEpisodes(context.Background(), memorystore.EpisodeFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, episodes, 1)
	require.Equal(t, event.TaskID, episodes[0].TaskID)
	require.Equal(t, event.SessionID, episodes[0].SessionID)
	require.Equal(t, event.SourceEventIDs, episodes[0].SourceEventIDs)
	require.Equal(t, memorystore.EpisodeOutcome("success"), episodes[0].Outcome)
}
