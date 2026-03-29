package team

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReconcileTerminalTeamStateDoesNotDuplicateSummarySideEffects(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{
		Status:        TeamStatusActive,
		LeadSessionID: "lead-session",
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		ID:      "task-1",
		TeamID:  teamID,
		Title:   "done task",
		Status:  TaskStatusDone,
		Summary: "finished",
	})
	require.NoError(t, err)

	planner := &LeadPlanner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "final team summary",
			},
		},
		Store: store,
	}
	mailbox := NewMailboxService(store)

	first, err := ReconcileTerminalTeamState(ctx, TerminalTeamServices{
		Store:   store,
		Planner: planner,
		Mailbox: mailbox,
	}, teamID)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.True(t, first.Terminal)
	assert.Equal(t, TeamStatusDone, first.Status)
	assert.Equal(t, "final team summary", first.Summary)

	second, err := ReconcileTerminalTeamState(ctx, TerminalTeamServices{
		Store:   store,
		Planner: planner,
		Mailbox: mailbox,
	}, teamID)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.True(t, second.Terminal)
	assert.Equal(t, TeamStatusDone, second.Status)
	assert.Empty(t, second.Summary)

	events, err := store.ListTeamEvents(ctx, TeamEventFilter{TeamID: teamID})
	require.NoError(t, err)

	eventCounts := map[string]int{}
	for _, event := range events {
		eventCounts[event.Type]++
	}
	assert.Equal(t, 1, eventCounts["team.completed"])
	assert.Equal(t, 1, eventCounts["team.summary"])

	messages, err := store.ListMail(ctx, MailFilter{
		TeamID: teamID,
		Kind:   "done",
	})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, "lead", messages[0].FromAgent)
	assert.Equal(t, "*", messages[0].ToAgent)
	assert.Equal(t, "final team summary", messages[0].Body)
}
