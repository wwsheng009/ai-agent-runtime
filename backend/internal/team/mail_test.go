package team

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLiteStoreListMailFilters(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	taskID := "task-1"
	_, err = store.InsertMail(ctx, MailMessage{
		TeamID:    teamID,
		FromAgent: "agent-a",
		ToAgent:   "agent-b",
		TaskID:    &taskID,
		Kind:      "info",
		Body:      "alpha",
	})
	require.NoError(t, err)
	_, err = store.InsertMail(ctx, MailMessage{
		TeamID:    teamID,
		FromAgent: "agent-b",
		ToAgent:   "agent-a",
		Kind:      "question",
		Body:      "beta",
	})
	require.NoError(t, err)

	messages, err := store.ListMail(ctx, MailFilter{
		TeamID:    teamID,
		FromAgent: "agent-a",
	})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Equal(t, "alpha", messages[0].Body)

	messages, err = store.ListMail(ctx, MailFilter{
		TeamID: teamID,
		Kind:   "question",
	})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Equal(t, "beta", messages[0].Body)

	messages, err = store.ListMail(ctx, MailFilter{
		TeamID: teamID,
		TaskID: taskID,
	})
	require.NoError(t, err)
	require.Len(t, messages, 1)

	since := time.Now().UTC().Add(time.Hour)
	messages, err = store.ListMail(ctx, MailFilter{
		TeamID: teamID,
		Since:  &since,
	})
	require.NoError(t, err)
	require.Len(t, messages, 0)
}

func TestMailboxReceiptsArePerAgentAndIncludeBroadcast(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	broadcastID, err := store.InsertMail(ctx, MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "*",
		Kind:      "info",
		Body:      "broadcast",
	})
	require.NoError(t, err)
	_, err = store.InsertMail(ctx, MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate-b",
		Kind:      "question",
		Body:      "direct",
	})
	require.NoError(t, err)

	require.NoError(t, store.RecordMailReceipt(ctx, MailReceipt{
		TeamID:    teamID,
		MessageID: broadcastID,
		AgentID:   "mate-a",
		AckedAt:   time.Now().UTC(),
	}))

	mailbox := NewMailboxService(store)

	unreadA, err := mailbox.ListUnread(ctx, teamID, "mate-a", 10)
	require.NoError(t, err)
	require.Len(t, unreadA, 0)

	unreadB, err := mailbox.ListUnread(ctx, teamID, "mate-b", 10)
	require.NoError(t, err)
	require.Len(t, unreadB, 2)
	bodies := []string{unreadB[0].Body, unreadB[1].Body}
	assert.Contains(t, bodies, "direct")
	assert.Contains(t, bodies, "broadcast")

	receipts, err := store.ListMailReceipts(ctx, teamID, broadcastID)
	require.NoError(t, err)
	require.Len(t, receipts, 1)
	assert.Equal(t, "mate-a", receipts[0].AgentID)

	globalUnread, err := store.ListMail(ctx, MailFilter{
		TeamID:     teamID,
		UnreadOnly: true,
	})
	require.NoError(t, err)
	require.Len(t, globalUnread, 2)

	allMessages, err := store.ListMail(ctx, MailFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, allMessages, 2)
	for _, message := range allMessages {
		assert.Nil(t, message.AckedAt)
	}
}
