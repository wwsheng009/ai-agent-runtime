package memorytaskbridge

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

type bridgeSessionReader struct {
	session *chat.Session
}

func (r bridgeSessionReader) GetSession(context.Context, string) (*chat.Session, error) {
	return r.session, nil
}

type bridgeSource struct {
	session memoryruntime.SessionRef
}

func (s bridgeSource) ListSessions(context.Context, string, int) ([]memoryruntime.SessionRef, error) {
	return []memoryruntime.SessionRef{s.session}, nil
}

func (s bridgeSource) ReadTranscript(context.Context, memoryruntime.SessionRef, int) (memoryruntime.Transcript, error) {
	return memoryruntime.Transcript{SessionID: s.session.ID}, nil
}

func (s bridgeSource) ReadSessionIdentity(context.Context, string) (memoryruntime.SessionIdentity, error) {
	return memoryruntime.SessionIdentity{Session: s.session}, nil
}

type bridgeConsumer struct {
	events []memoryruntime.TaskCompletionEvent
	store  *memorystore.Store
}

func (c *bridgeConsumer) ConsumeTaskCompletion(ctx context.Context, event memoryruntime.TaskCompletionEvent) error {
	c.events = append(c.events, event)
	return memoryruntime.NewStoreTaskCompletionConsumer(c.store).ConsumeTaskCompletion(ctx, event)
}

func TestConsumeTeamTaskCompletionCreatesEpisode(t *testing.T) {
	ctx := context.Background()
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })

	teamID, err := teamStore.CreateTeam(ctx, team.Team{
		WorkspaceID: "workspace", LeadSessionID: "task-session",
	})
	require.NoError(t, err)
	assignee := "mate-1"
	_, err = teamStore.UpsertTeammate(ctx, team.Teammate{
		ID: assignee, TeamID: teamID, Name: "worker", SessionID: "task-session",
	})
	require.NoError(t, err)
	taskID, err := teamStore.CreateTask(ctx, team.Task{
		TeamID: teamID, Title: "ship bridge", Goal: "wire task completion into memory",
		Status: team.TaskStatusDone, Assignee: &assignee,
		Summary: "episode bridge implemented",
	})
	require.NoError(t, err)

	store, err := memorystore.New(memorystore.Config{
		ProjectRoot: t.TempDir(), Mode: "shadow",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	session := memoryruntime.SessionRef{
		ID: "task-session", SessionGeneration: 7, WorkspacePath: store.ProjectKey(),
		State: "active", LastActivityAt: time.Now().UTC(),
	}
	runtime := memoryruntime.New(store, bridgeSource{session: session}, nil, nil, memoryruntime.Config{
		Mode: "shadow", Workspace: store.ProjectKey(), ProjectKey: store.ProjectKey(),
		RequireWorkspaceMatch: true,
	})
	consumer := &bridgeConsumer{store: store}
	runtime.TaskCompletions = consumer

	deps := Dependencies{
		TeamStore: teamStore,
		Sessions: bridgeSessionReader{session: &chat.Session{
			ID: "task-session", SessionGeneration: 7, State: chat.StateActive,
		}},
		RuntimeForSession: func(context.Context, string) (*memoryruntime.Runtime, error) {
			return runtime, nil
		},
	}
	accepted, err := ConsumeTeamEvent(ctx, team.TeamEvent{
		Type: "task.completed", TeamID: teamID,
		Payload: map[string]interface{}{
			"task_id": taskID, "assignee": assignee,
			"summary": "episode bridge implemented",
		},
		Timestamp: time.Now().UTC(),
	}, deps)
	require.NoError(t, err)
	require.True(t, accepted)
	require.Len(t, consumer.events, 1)
	require.Equal(t, taskID, consumer.events[0].TaskID)
	require.Equal(t, "success", consumer.events[0].Outcome)
	require.EqualValues(t, 7, consumer.events[0].SessionGeneration)

	episodes, err := store.ListEpisodes(ctx, memorystore.EpisodeFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, episodes, 1)
	require.Equal(t, taskID, episodes[0].TaskID)
	require.Equal(t, "task-session", episodes[0].SessionID)
	require.Equal(t, memorystore.EpisodeOutcomeSuccess, episodes[0].Outcome)
	require.Contains(t, episodes[0].SourceEventIDs, "team_task:"+teamID+":"+taskID+":success")

	var bridgeErr error
	bus := team.NewTeamEventBus()
	BindTeamEvents(bus, Dependencies{
		TeamStore:         teamStore,
		Sessions:          deps.Sessions,
		RuntimeForSession: deps.RuntimeForSession,
		OnError: func(err error) {
			bridgeErr = err
		},
	})
	bus.Publish(team.TeamEvent{
		Type: "team.task.failed", TeamID: teamID,
		Payload: map[string]interface{}{
			"task_id": taskID, "assignee": assignee, "error": "verification failed",
		},
		Timestamp: time.Now().UTC(),
	})
	require.NoError(t, bridgeErr)
	require.Len(t, consumer.events, 2)
	require.Equal(t, "failed", consumer.events[1].Outcome)
	episodes, err = store.ListEpisodes(ctx, memorystore.EpisodeFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, episodes, 2)
}
