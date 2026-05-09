package agentcontrol

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCombinedMailboxRegistryUsesStableScopedCursor(t *testing.T) {
	ctx := context.Background()
	first := &testMailboxRegistrySource{records: []MailboxRecord{
		{Seq: 1, Scope: MailboxScopeSession, SessionID: "session-1", MessageID: "session-mail-1", CreatedAt: time.Unix(1, 0)},
		{Seq: 2, Scope: MailboxScopeSession, SessionID: "session-1", MessageID: "session-mail-2", CreatedAt: time.Unix(3, 0)},
	}}
	second := &testMailboxRegistrySource{records: []MailboxRecord{
		{Seq: 1, Scope: MailboxScopeTeam, TeamID: "team-1", MessageID: "team-mail-1", CreatedAt: time.Unix(2, 0)},
	}}
	registry := CombinedMailboxRegistry{Sources: []MailboxRegistrySource{first, second}}

	records, err := registry.ListAgentControlMailboxRecords(ctx, MailboxRecordFilter{})
	require.NoError(t, err)
	require.Len(t, records, 3)
	require.Equal(t, CombinedMailboxSeq(0, 1, time.Unix(1, 0)), records[0].Seq)
	require.Equal(t, CombinedMailboxSeq(1, 1, time.Unix(2, 0)), records[1].Seq)
	require.Equal(t, CombinedMailboxSeq(0, 2, time.Unix(3, 0)), records[2].Seq)

	afterFirst := records[0].Seq
	records, err = registry.ListAgentControlMailboxRecords(ctx, MailboxRecordFilter{AfterSeq: afterFirst})
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, "team-mail-1", records[0].MessageID)
	require.Equal(t, "session-mail-2", records[1].MessageID)

	seq, err := registry.LastAgentControlMailboxRecordSeq(ctx, MailboxRecordFilter{})
	require.NoError(t, err)
	require.Equal(t, CombinedMailboxSeq(0, 2, time.Unix(3, 0)), seq)
}

func TestCombinedMailboxRegistryHonorsScopeFilter(t *testing.T) {
	ctx := context.Background()
	registry := CombinedMailboxRegistry{Sources: []MailboxRegistrySource{
		&testMailboxRegistrySource{records: []MailboxRecord{{Seq: 1, Scope: MailboxScopeSession, SessionID: "session-1", MessageID: "session-mail"}}},
		&testMailboxRegistrySource{records: []MailboxRecord{{Seq: 1, Scope: MailboxScopeTeam, TeamID: "team-1", MessageID: "team-mail"}}},
	}}

	records, err := registry.ListAgentControlMailboxRecords(ctx, MailboxRecordFilter{Scope: MailboxScopeTeam})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "team-mail", records[0].MessageID)
	require.Equal(t, CombinedMailboxSeq(1, 1, time.Time{}), records[0].Seq)
}

func TestCombinedMailboxRegistryDoesNotSkipNewLowerSourceRows(t *testing.T) {
	ctx := context.Background()
	first := &testMailboxRegistrySource{records: []MailboxRecord{
		{Seq: 1, Scope: MailboxScopeSession, SessionID: "session-1", MessageID: "session-mail-1", CreatedAt: time.Unix(1, 0)},
	}}
	second := &testMailboxRegistrySource{records: []MailboxRecord{
		{Seq: 1, Scope: MailboxScopeTeam, TeamID: "team-1", MessageID: "team-mail-1", CreatedAt: time.Unix(2, 0)},
	}}
	registry := CombinedMailboxRegistry{Sources: []MailboxRegistrySource{first, second}}
	seq, err := registry.LastAgentControlMailboxRecordSeq(ctx, MailboxRecordFilter{})
	require.NoError(t, err)

	first.records = append(first.records, MailboxRecord{
		Seq:       2,
		Scope:     MailboxScopeSession,
		SessionID: "session-1",
		MessageID: "session-mail-2",
		CreatedAt: time.Unix(3, 0),
	})
	records, err := registry.ListAgentControlMailboxRecords(ctx, MailboxRecordFilter{AfterSeq: seq})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "session-mail-2", records[0].MessageID)
}

func TestGlobalMailboxRegistryExposesNamedSources(t *testing.T) {
	ctx := context.Background()
	registry := NewGlobalMailboxRegistry(
		NamedMailboxRegistrySource{
			Name: "runtime_sessions",
			Source: &testMailboxRegistrySource{records: []MailboxRecord{
				{Seq: 1, Scope: MailboxScopeSession, SessionID: "session-1", MessageID: "session-mail-1", CreatedAt: time.Unix(1, 0)},
			}},
		},
		NamedMailboxRegistrySource{
			Name: "teams",
			Source: &testMailboxRegistrySource{records: []MailboxRecord{
				{Seq: 1, Scope: MailboxScopeTeam, TeamID: "team-1", MessageID: "team-mail-1", CreatedAt: time.Unix(2, 0)},
			}},
		},
		NamedMailboxRegistrySource{Name: "ignored"},
	)

	require.Equal(t, []string{"runtime_sessions", "teams"}, registry.SourceNames())
	records, err := registry.ListAgentControlMailboxRecords(ctx, MailboxRecordFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, "session-mail-1", records[0].MessageID)
	require.Equal(t, "team-mail-1", records[1].MessageID)

	latest, err := registry.LastAgentControlMailboxRecordSeq(ctx, MailboxRecordFilter{})
	require.NoError(t, err)
	require.Equal(t, records[1].Seq, latest)
}

func TestSQLiteGlobalMailboxRegistryStoreAppendAndList(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteGlobalMailboxRegistryStore(&GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	seq, err := store.AppendGlobalMailboxRecord(ctx, "runtime_sessions", MailboxRecord{
		Seq:               7,
		Workflow:          WorkflowSpawnTeam,
		Scope:             MailboxScopeSession,
		SessionID:         "session-1",
		SessionMailboxSeq: 3,
		TeamID:            "team-1",
		MessageID:         "session-mail-1",
		FromAgent:         "lead",
		ToAgent:           "member-1",
		Kind:              MailboxKindTeamLifecycle,
		Body:              "session body",
		Metadata:          map[string]interface{}{MetadataKeyWorkflow: WorkflowSpawnTeam},
		CreatedAt:         time.Unix(1, 0).UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), seq)

	records, err := store.ListAgentControlMailboxRecords(ctx, MailboxRecordFilter{Workflow: WorkflowSpawnTeam, Limit: 10})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, seq, records[0].Seq)
	require.Equal(t, "runtime_sessions", records[0].Source)
	require.Equal(t, int64(7), records[0].SourceSeq)
	require.Equal(t, MailboxScopeSession, records[0].Scope)
	require.Equal(t, "session-mail-1", records[0].MessageID)
	require.Equal(t, "session body", records[0].Body)
	require.Equal(t, WorkflowSpawnTeam, records[0].Metadata[MetadataKeyWorkflow])

	latest, err := store.LastAgentControlMailboxRecordSeq(ctx, MailboxRecordFilter{Workflow: WorkflowSpawnTeam})
	require.NoError(t, err)
	require.Equal(t, seq, latest)
}

func TestSQLiteGlobalMailboxRegistryStoreIdempotentSourceRows(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteGlobalMailboxRegistryStore(&GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	record := MailboxRecord{
		Seq:       1,
		Workflow:  WorkflowSpawnTeam,
		Scope:     MailboxScopeTeam,
		TeamID:    "team-1",
		TeamSeq:   5,
		MessageID: "team-mail-1",
		Kind:      MailboxKindTeamTaskLifecycle,
		Body:      "first body",
		CreatedAt: time.Unix(1, 0).UTC(),
	}
	firstSeq, err := store.AppendGlobalMailboxRecord(ctx, "teams", record)
	require.NoError(t, err)
	record.Body = "updated body"
	secondSeq, err := store.AppendGlobalMailboxRecord(ctx, "teams", record)
	require.NoError(t, err)
	require.Equal(t, firstSeq, secondSeq)

	records, err := store.ListAgentControlMailboxRecords(ctx, MailboxRecordFilter{Scope: MailboxScopeTeam})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "updated body", records[0].Body)
}

func TestSQLiteGlobalMailboxRegistryStoreAppendPrimaryAssignsGlobalSeq(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteGlobalMailboxRegistryStore(&GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	record, err := store.AppendPrimaryGlobalMailboxRecord(ctx, MailboxRecord{
		Workflow:  WorkflowSpawnAgent,
		Scope:     MailboxScopeSession,
		SessionID: "session-1",
		MessageID: "primary-mail-1",
		Kind:      MailboxKindSubagentCompleted,
		Body:      "created globally first",
		CreatedAt: time.Unix(10, 0).UTC(),
	})
	require.NoError(t, err)
	require.Positive(t, record.Seq)
	require.Equal(t, record.Seq, record.GlobalSeq)
	require.Equal(t, MailboxSourceGlobal, record.Source)
	require.Positive(t, record.SourceSeq)

	records, err := store.ListAgentControlMailboxRecords(ctx, MailboxRecordFilter{
		Workflow:  WorkflowSpawnAgent,
		SessionID: "session-1",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, record.Seq, records[0].Seq)
	require.Equal(t, record.Seq, records[0].GlobalSeq)
}

func TestSQLiteGlobalMailboxRegistryStoreAppendPrimaryIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteGlobalMailboxRegistryStore(&GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	first, err := store.AppendPrimaryGlobalMailboxRecord(ctx, MailboxRecord{
		Workflow:  WorkflowSpawnTeam,
		Scope:     MailboxScopeTeam,
		TeamID:    "team-1",
		MessageID: "primary-team-mail",
		Body:      "first body",
		CreatedAt: time.Unix(11, 0).UTC(),
	})
	require.NoError(t, err)
	second, err := store.AppendPrimaryGlobalMailboxRecord(ctx, MailboxRecord{
		Workflow:  WorkflowSpawnTeam,
		Scope:     MailboxScopeTeam,
		TeamID:    "team-1",
		MessageID: "primary-team-mail",
		TeamSeq:   3,
		Body:      "updated body",
		CreatedAt: time.Unix(12, 0).UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, first.Seq, second.Seq)

	records, err := store.ListAgentControlMailboxRecords(ctx, MailboxRecordFilter{
		Workflow: WorkflowSpawnTeam,
		TeamID:   "team-1",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, int64(3), records[0].TeamSeq)
	require.Equal(t, "updated body", records[0].Body)
}

func TestSQLiteGlobalMailboxRegistryMaterializeSkipsProjectedRows(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteGlobalMailboxRegistryStore(&GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	primary, err := store.AppendPrimaryGlobalMailboxRecord(ctx, MailboxRecord{
		Workflow:  WorkflowSpawnTeam,
		Scope:     MailboxScopeTeam,
		TeamID:    "team-1",
		MessageID: "already-global",
		Body:      "global primary",
		CreatedAt: time.Unix(13, 0).UTC(),
	})
	require.NoError(t, err)

	count, err := store.MaterializeMailboxRecords(ctx, []NamedMailboxRegistrySource{
		{
			Name: "teams",
			Source: &testMailboxRegistrySource{records: []MailboxRecord{
				{Seq: 1, GlobalSeq: primary.Seq, Workflow: WorkflowSpawnTeam, Scope: MailboxScopeTeam, TeamID: "team-1", MessageID: "already-global", CreatedAt: time.Unix(13, 0).UTC()},
			}},
		},
	}, MailboxRecordFilter{Workflow: WorkflowSpawnTeam})
	require.NoError(t, err)
	require.Equal(t, int64(1), count)

	records, err := store.ListAgentControlMailboxRecords(ctx, MailboxRecordFilter{
		Workflow: WorkflowSpawnTeam,
		TeamID:   "team-1",
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, primary.Seq, records[0].Seq)
}

func TestSQLiteGlobalMailboxRegistryMaterializeFromSources(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteGlobalMailboxRegistryStore(&GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	sources := []NamedMailboxRegistrySource{
		{
			Name: "runtime_sessions",
			Source: &testMailboxRegistrySource{records: []MailboxRecord{
				{Seq: 1, Workflow: WorkflowSpawnTeam, Scope: MailboxScopeSession, SessionID: "session-1", MessageID: "session-mail-1", CreatedAt: time.Unix(1, 0).UTC()},
			}},
		},
		{
			Name: "teams",
			Source: &testMailboxRegistrySource{records: []MailboxRecord{
				{Seq: 1, Workflow: WorkflowSpawnTeam, Scope: MailboxScopeTeam, TeamID: "team-1", MessageID: "team-mail-1", CreatedAt: time.Unix(2, 0).UTC()},
			}},
		},
	}
	count, err := store.MaterializeMailboxRecords(ctx, sources, MailboxRecordFilter{Workflow: WorkflowSpawnTeam})
	require.NoError(t, err)
	require.Equal(t, int64(2), count)
	count, err = store.MaterializeMailboxRecords(ctx, sources, MailboxRecordFilter{Workflow: WorkflowSpawnTeam})
	require.NoError(t, err)
	require.Equal(t, int64(2), count)

	records, err := store.ListAgentControlMailboxRecords(ctx, MailboxRecordFilter{Workflow: WorkflowSpawnTeam})
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, int64(1), records[0].Seq)
	require.Equal(t, int64(2), records[1].Seq)
	require.Equal(t, "session-mail-1", records[0].MessageID)
	require.Equal(t, "team-mail-1", records[1].MessageID)
}

func TestSQLiteGlobalMailboxRegistryStoreWatchesMailboxWake(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteGlobalMailboxRegistryStore(&GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	wake, unwatch := store.WatchAgentControlMailboxWake(watchCtx, MailboxWakeFilter{
		Workflow: WorkflowSpawnTeam,
		TeamID:   "team-1",
	})
	defer unwatch()

	seq, err := store.AppendGlobalMailboxRecord(ctx, "teams", MailboxRecord{
		Seq:       11,
		Workflow:  WorkflowSpawnTeam,
		Scope:     MailboxScopeTeam,
		TeamID:    "team-1",
		TeamSeq:   11,
		MessageID: "team-mail-11",
		FromAgent: "member-1",
		ToAgent:   "lead",
		TaskID:    "task-1",
		Kind:      MailboxKindTeamTaskLifecycle,
		Body:      "task done",
		CreatedAt: time.Unix(11, 0).UTC(),
	})
	require.NoError(t, err)

	select {
	case event := <-wake:
		require.Equal(t, seq, event.Seq)
		require.Equal(t, WorkflowSpawnTeam, event.Workflow)
		require.Equal(t, "team-1", event.TeamID)
		require.Equal(t, "team-mail-11", event.MessageID)
		require.Equal(t, MailboxKindTeamTaskLifecycle, event.Kind)
		require.Equal(t, "member-1", event.FromAgent)
		require.Equal(t, "lead", event.ToAgent)
		require.Equal(t, "task-1", event.TaskID)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for global mailbox wake")
	}

	latest, err := store.LastAgentControlMailboxWakeSeq(ctx, MailboxWakeFilter{
		Workflow: WorkflowSpawnTeam,
		TeamID:   "team-1",
	})
	require.NoError(t, err)
	require.Equal(t, seq, latest)
}

func TestSQLiteGlobalMailboxRegistryStoreDoesNotWakeOnIdempotentRefresh(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteGlobalMailboxRegistryStore(&GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	wake, unwatch := store.WatchAgentControlMailboxWake(watchCtx, MailboxWakeFilter{
		Workflow: WorkflowSpawnTeam,
		TeamID:   "team-1",
	})
	defer unwatch()

	record := MailboxRecord{
		Seq:       1,
		Workflow:  WorkflowSpawnTeam,
		Scope:     MailboxScopeTeam,
		TeamID:    "team-1",
		TeamSeq:   1,
		MessageID: "team-mail-1",
		Kind:      MailboxKindTeamTaskLifecycle,
		Body:      "initial body",
		CreatedAt: time.Unix(1, 0).UTC(),
	}
	seq, err := store.AppendGlobalMailboxRecord(ctx, "teams", record)
	require.NoError(t, err)
	select {
	case event := <-wake:
		require.Equal(t, seq, event.Seq)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial global mailbox wake")
	}

	record.Body = "refreshed body"
	refreshedSeq, err := store.AppendGlobalMailboxRecord(ctx, "teams", record)
	require.NoError(t, err)
	require.Equal(t, seq, refreshedSeq)

	select {
	case event := <-wake:
		t.Fatalf("unexpected duplicate wake for idempotent refresh: %+v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSQLiteGlobalMailboxRegistryStoreWakeHonorsSessionFilter(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteGlobalMailboxRegistryStore(&GlobalMailboxStoreConfig{
		Path: filepath.Join(t.TempDir(), "global-mailbox.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	wake, unwatch := store.WatchAgentControlMailboxWake(watchCtx, MailboxWakeFilter{
		Workflow:  WorkflowSpawnTeam,
		SessionID: "session-1",
	})
	defer unwatch()

	_, err = store.AppendGlobalMailboxRecord(ctx, "runtime_sessions", MailboxRecord{
		Seq:       1,
		Workflow:  WorkflowSpawnTeam,
		Scope:     MailboxScopeSession,
		SessionID: "session-2",
		MessageID: "session-mail-2",
		Kind:      MailboxKindTeamLifecycle,
		Body:      "other session",
		CreatedAt: time.Unix(2, 0).UTC(),
	})
	require.NoError(t, err)
	seq, err := store.AppendGlobalMailboxRecord(ctx, "runtime_sessions", MailboxRecord{
		Seq:       2,
		Workflow:  WorkflowSpawnTeam,
		Scope:     MailboxScopeSession,
		SessionID: "session-1",
		MessageID: "session-mail-1",
		Kind:      MailboxKindTeamLifecycle,
		Body:      "matching session",
		CreatedAt: time.Unix(3, 0).UTC(),
	})
	require.NoError(t, err)

	select {
	case event := <-wake:
		require.Equal(t, seq, event.Seq)
		require.Equal(t, "session-1", event.SessionID)
		require.Equal(t, "session-mail-1", event.MessageID)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session-filtered global mailbox wake")
	}
}

func TestGlobalMailboxRegistryPrefersDurableSource(t *testing.T) {
	ctx := context.Background()
	durable := &testMailboxRegistrySource{records: []MailboxRecord{
		{Seq: 1, Source: "global", SourceSeq: 10, Workflow: WorkflowSpawnTeam, Scope: MailboxScopeTeam, TeamID: "team-1", MessageID: "global-mail"},
	}}
	registry := NewGlobalMailboxRegistryWithDurable(
		NamedMailboxRegistrySource{Name: "global", Source: durable},
		NamedMailboxRegistrySource{Name: "teams", Source: &testMailboxRegistrySource{records: []MailboxRecord{
			{Seq: 1, Workflow: WorkflowSpawnTeam, Scope: MailboxScopeTeam, TeamID: "team-1", MessageID: "local-mail"},
		}}},
	)

	require.Equal(t, []string{"global", "teams"}, registry.SourceNames())
	records, err := registry.ListAgentControlMailboxRecords(ctx, MailboxRecordFilter{Workflow: WorkflowSpawnTeam})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "global-mail", records[0].MessageID)
	latest, err := registry.LastAgentControlMailboxRecordSeq(ctx, MailboxRecordFilter{Workflow: WorkflowSpawnTeam})
	require.NoError(t, err)
	require.Equal(t, int64(1), latest)
}

type testMailboxRegistrySource struct {
	records []MailboxRecord
}

func (s *testMailboxRegistrySource) ListAgentControlMailboxRecords(ctx context.Context, filter MailboxRecordFilter) ([]MailboxRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	filter = filter.Normalize()
	out := make([]MailboxRecord, 0, len(s.records))
	for _, record := range s.records {
		if filter.Scope != "" && record.Scope != filter.Scope {
			continue
		}
		if filter.SessionID != "" && record.SessionID != filter.SessionID {
			continue
		}
		if filter.TeamID != "" && record.TeamID != filter.TeamID {
			continue
		}
		if record.Seq <= filter.AfterSeq {
			continue
		}
		out = append(out, record)
	}
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (s *testMailboxRegistrySource) LastAgentControlMailboxRecordSeq(ctx context.Context, filter MailboxRecordFilter) (int64, error) {
	records, err := s.ListAgentControlMailboxRecords(ctx, filter)
	if err != nil {
		return 0, err
	}
	var maxSeq int64
	for _, record := range records {
		if record.Seq > maxSeq {
			maxSeq = record.Seq
		}
	}
	return maxSeq, nil
}
