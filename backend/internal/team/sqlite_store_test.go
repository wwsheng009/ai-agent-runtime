package team

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLiteStoreBlockTask(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	assignee := "mate-1"
	leaseUntil := time.Now().UTC().Add(5 * time.Minute)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID:     teamID,
		Title:      "blocked-task",
		Status:     TaskStatusRunning,
		Assignee:   &assignee,
		LeaseUntil: &leaseUntil,
		Version:    7,
	})
	require.NoError(t, err)

	err = store.BlockTask(ctx, taskID, "waiting for review")
	require.NoError(t, err)

	task, err := store.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, TaskStatusBlocked, task.Status)
	assert.Equal(t, "waiting for review", task.Summary)
	assert.Nil(t, task.LeaseUntil)
	require.NotNil(t, task.Assignee)
	assert.Equal(t, assignee, *task.Assignee)
	assert.Equal(t, int64(8), task.Version)
}

func TestSQLiteStoreWithImmediateTxRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	errBoom := store.WithImmediateTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO teams (
				id, workspace_id, lead_session_id, status, strategy, max_teammates, max_writers, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, "team-tx", "", "", string(TeamStatusActive), "", 0, 0, formatTime(time.Now().UTC()), formatTime(time.Now().UTC()))
		require.NoError(t, err)
		return assert.AnError
	})
	require.ErrorIs(t, errBoom, assert.AnError)

	teamRecord, err := store.GetTeam(ctx, "team-tx")
	require.NoError(t, err)
	assert.Nil(t, teamRecord)
}
