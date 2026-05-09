package team

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
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

func TestSQLiteStoreInsertMailWritesGlobalRegistry(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	globalStore, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = globalStore.Close() })
	store.SetGlobalMailboxWriter(globalStore)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	messageID, err := store.InsertMail(ctx, MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "member-1",
		Kind:      agentcontrol.MailboxKindTeamTaskLifecycle,
		Body:      "task lifecycle",
		CreatedAt: time.Unix(11, 0).UTC(),
	})
	require.NoError(t, err)

	records, err := globalStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, agentcontrol.MailboxSourceGlobal, records[0].Source)
	assert.Positive(t, records[0].SourceSeq)
	assert.Equal(t, messageID, records[0].MessageID)
	assert.Equal(t, agentcontrol.MailboxScopeTeam, records[0].Scope)
	assert.Equal(t, int64(1), records[0].TeamSeq)

	localMessages, err := store.ListMail(ctx, MailFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, localMessages, 1)
	assert.Equal(t, records[0].Seq, localMessages[0].GlobalSeq)

	localRecords, err := store.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
	})
	require.NoError(t, err)
	require.Len(t, localRecords, 1)
	assert.Equal(t, records[0].Seq, localRecords[0].GlobalSeq)

	count, err := globalStore.MaterializeMailboxRecords(ctx, []agentcontrol.NamedMailboxRegistrySource{
		{Name: agentcontrol.MailboxSourceTeams, Source: NewAgentControlMailboxRegistry(store)},
	}, agentcontrol.MailboxRecordFilter{Workflow: agentcontrol.WorkflowSpawnTeam})
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	records, err = globalStore.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
}

func TestSQLiteStoreListMailAfterSeqReturnsLaterMessages(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	firstID, err := store.InsertMail(ctx, MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate-a",
		Kind:      "info",
		Body:      "first",
	})
	require.NoError(t, err)
	secondID, err := store.InsertMail(ctx, MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate-a",
		Kind:      "info",
		Body:      "second",
	})
	require.NoError(t, err)

	all, err := store.ListMail(ctx, MailFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Equal(t, secondID, all[0].ID)
	require.Equal(t, firstID, all[1].ID)
	require.Greater(t, all[0].Seq, all[1].Seq)

	later, err := store.ListMail(ctx, MailFilter{
		TeamID:   teamID,
		AfterSeq: all[1].Seq,
	})
	require.NoError(t, err)
	require.Len(t, later, 1)
	assert.Equal(t, secondID, later[0].ID)
	assert.Equal(t, all[0].Seq, later[0].Seq)
}

func TestSQLiteStoreListMailPrefersAgentControlMessages(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	messageID, err := store.InsertMail(ctx, MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate-a",
		Kind:      "info",
		Body:      "agent-control body",
		Metadata: map[string]interface{}{
			"source": "agent-control",
		},
	})
	require.NoError(t, err)

	assertSQLiteTableMissing(t, store.db, "team_mailbox_messages")
	assertSQLiteTableMissing(t, store.db, "agent_control_mailbox_messages")

	messages, err := store.ListMail(ctx, MailFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, messageID, messages[0].ID)
	assert.Equal(t, "agent-control body", messages[0].Body)
	assert.Equal(t, "info", messages[0].Kind)
	assert.Equal(t, "agent-control", messages[0].Metadata["source"])
	assert.Equal(t, int64(1), messages[0].Seq)
	assert.Equal(t, int64(1), messages[0].ControlSeq)

	var unifiedRows int
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM agent_control_mailbox_records
		WHERE scope = ? AND team_id = ?
	`, agentcontrol.MailboxScopeTeam, teamID).Scan(&unifiedRows))
	assert.Equal(t, 1, unifiedRows)

	records, err := store.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:    agentcontrol.MailboxScopeTeam,
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, messageID, records[0].MessageID)
	assert.Equal(t, agentcontrol.MailboxScopeTeam, records[0].Scope)
	assert.Equal(t, agentcontrol.WorkflowSpawnTeam, records[0].Workflow)
	assert.Equal(t, teamID, records[0].TeamID)
	assert.Equal(t, int64(1), records[0].Seq)
	assert.Equal(t, int64(1), records[0].TeamSeq)
	assert.Equal(t, "agent-control body", records[0].Body)

	recordSeq, err := store.LastAgentControlMailboxRecordSeq(ctx, agentcontrol.MailboxRecordFilter{
		Scope:    agentcontrol.MailboxScopeTeam,
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), recordSeq)
}

func TestSQLiteStoreRecordMailReceiptDoesNotRequireMirrorRows(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	messageID, err := store.InsertMail(ctx, MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate-a",
		Kind:      "info",
		Body:      "agent-control body",
	})
	require.NoError(t, err)

	assertSQLiteTableMissing(t, store.db, "team_mailbox_messages")
	assertSQLiteTableMissing(t, store.db, "agent_control_mailbox_messages")

	ackedAt := time.Date(2026, 5, 8, 10, 30, 0, 0, time.UTC)
	require.NoError(t, store.AckMail(ctx, teamID, messageID, ackedAt))

	records, err := store.ListAgentControlMailboxRecords(ctx, agentcontrol.MailboxRecordFilter{
		Scope:    agentcontrol.MailboxScopeTeam,
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.NotNil(t, records[0].AckedAt)
	assert.Equal(t, formatTime(ackedAt), formatTime(*records[0].AckedAt))

	unread, err := store.ListMail(ctx, MailFilter{
		TeamID:     teamID,
		UnreadOnly: true,
	})
	require.NoError(t, err)
	require.Len(t, unread, 0)

	receipts, err := store.ListMailReceipts(ctx, teamID, messageID)
	require.NoError(t, err)
	require.Len(t, receipts, 1)
	assert.Equal(t, globalMailReceiptAgent, receipts[0].AgentID)
}

func assertSQLiteTableMissing(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow(`
		SELECT COUNT(*)
		FROM sqlite_master
		WHERE type = 'table' AND name = ?
	`, table).Scan(&count))
	assert.Equal(t, 0, count)
}

func TestMailboxServiceWaitUsesDurableSequenceAndWake(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)

	firstID, err := store.InsertMail(ctx, MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate-a",
		Kind:      "info",
		Body:      "first",
	})
	require.NoError(t, err)
	firstBatch, err := store.ListMail(ctx, MailFilter{TeamID: teamID, AfterSeq: 0})
	require.NoError(t, err)
	require.Len(t, firstBatch, 1)
	require.Equal(t, firstID, firstBatch[0].ID)
	firstSeq := firstBatch[0].Seq

	mailbox := NewMailboxService(store)
	waitDone := make(chan []MailMessage, 1)
	waitErr := make(chan error, 1)
	go func() {
		messages, err := mailbox.Wait(ctx, MailWatchRequest{
			TeamID:           teamID,
			ToAgent:          "mate-a",
			AfterSeq:         firstSeq,
			Limit:            4,
			UnreadOnly:       true,
			IncludeBroadcast: true,
			Timeout:          2 * time.Second,
		})
		if err != nil {
			waitErr <- err
			return
		}
		waitDone <- messages
	}()

	time.Sleep(25 * time.Millisecond)
	secondID, err := store.InsertMail(ctx, MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate-a",
		Kind:      "info",
		Body:      "second",
	})
	require.NoError(t, err)

	select {
	case err := <-waitErr:
		require.NoError(t, err)
	case messages := <-waitDone:
		require.Len(t, messages, 1)
		assert.Equal(t, secondID, messages[0].ID)
		assert.Greater(t, messages[0].Seq, firstSeq)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("mailbox wait did not wake from inserted message")
	}
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
