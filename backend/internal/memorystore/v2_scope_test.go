package memorystore

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newScopedTestStore(t *testing.T, projectKey, tenantID string) *Store {
	t.Helper()
	store, err := New(Config{
		Root:       t.TempDir(),
		Mode:       "on",
		ProjectKey: projectKey,
		Scope: ScopeContext{
			TenantID:   tenantID,
			ProjectKey: projectKey,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func TestMemoryScopeUserIsolationAndGrantLifecycle(t *testing.T) {
	store := newScopedTestStore(t, "scope-project", "tenant-a")
	baseCtx := context.Background()
	aliceCtx := WithMemoryScope(baseCtx, ScopeContext{OwnerID: "alice"})
	bobCtx := WithMemoryScope(baseCtx, ScopeContext{OwnerID: "bob"})

	shared, err := store.AppendWithContext(aliceCtx, AppendNoteOptions{
		Text: "shared project scope baseline",
	})
	require.NoError(t, err)
	alice, err := store.AppendWithContext(aliceCtx, AppendNoteOptions{
		Text:      "alice-only marker",
		ScopeKind: string(ScopeUser),
	})
	require.NoError(t, err)
	bob, err := store.AppendWithContext(bobCtx, AppendNoteOptions{
		Text:      "bob-only marker",
		ScopeKind: string(ScopeUser),
	})
	require.NoError(t, err)

	require.NotEqual(t, alice.ID, bob.ID)
	require.NotEqual(t, alice.Text, bob.Text)

	aliceRecord, err := store.GetRecord(aliceCtx, alice.ID)
	require.NoError(t, err)
	require.Equal(t, ScopeUser, aliceRecord.ScopeKind)
	require.Equal(t, "alice", aliceRecord.ScopeKey)
	require.Equal(t, "alice", aliceRecord.OwnerID)
	require.Equal(t, "tenant-a", aliceRecord.TenantID)

	_, err = store.GetRecord(bobCtx, alice.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)

	bobPage, err := store.ListRecords(bobCtx, RecordFilter{Limit: 20})
	require.NoError(t, err)
	require.Len(t, bobPage.Items, 2)
	ids := recordIDs(bobPage.Items)
	require.Contains(t, ids, shared.ID)
	require.Contains(t, ids, bob.ID)
	require.NotContains(t, ids, alice.ID)

	alicePage, err := store.ListRecords(aliceCtx, RecordFilter{Limit: 20})
	require.NoError(t, err)
	require.Len(t, alicePage.Items, 2)
	ids = recordIDs(alicePage.Items)
	require.Contains(t, ids, shared.ID)
	require.Contains(t, ids, alice.ID)
	require.NotContains(t, ids, bob.ID)

	search, err := store.SearchRecords(bobCtx, RecordSearchQuery{Text: "alice-only", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, search.Items)

	grant, err := store.CreateMemoryScopeGrant(baseCtx, MemoryScopeGrant{
		GrantorID:   "admin",
		GranteeType: "user",
		GranteeID:   "bob",
		ScopeKind:   ScopeUser,
		ScopeKey:    "alice",
		Permission:  string(ScopePermissionRead),
	})
	require.NoError(t, err)

	_, err = store.GetRecord(bobCtx, alice.ID)
	require.NoError(t, err)
	text := "bob attempted a read-grant write"
	_, err = store.UpdateRecord(bobCtx, alice.ID, RecordPatch{Text: &text})
	require.Error(t, err)

	writeGrant, err := store.CreateMemoryScopeGrant(baseCtx, MemoryScopeGrant{
		GrantorID:   "admin",
		GranteeType: "user",
		GranteeID:   "bob",
		ScopeKind:   ScopeUser,
		ScopeKey:    "alice",
		Permission:  string(ScopePermissionWrite),
	})
	require.NoError(t, err)
	updated, err := store.UpdateRecord(bobCtx, alice.ID, RecordPatch{Text: &text})
	require.NoError(t, err)
	require.Equal(t, text, updated.Text)

	require.NoError(t, store.RevokeMemoryScopeGrant(baseCtx, grant.ID))
	require.NoError(t, store.RevokeMemoryScopeGrant(baseCtx, writeGrant.ID))
	_, err = store.GetRecord(bobCtx, alice.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestMemoryScopeAgentSessionTaskIsolation(t *testing.T) {
	store := newScopedTestStore(t, "scope-project-agent", "")
	baseCtx := context.Background()

	agentCtx := WithMemoryScope(baseCtx, ScopeContext{OwnerID: "alice", AgentID: "agent-1"})
	otherAgentCtx := WithMemoryScope(baseCtx, ScopeContext{OwnerID: "alice", AgentID: "agent-2"})
	sessionCtx := WithMemoryScope(baseCtx, ScopeContext{SessionID: "session-1"})
	taskCtx := WithMemoryScope(baseCtx, ScopeContext{TaskID: "task-1"})

	projectRecord, err := store.AppendWithContext(baseCtx, AppendNoteOptions{Text: "project marker"})
	require.NoError(t, err)
	agentRecord, err := store.AppendWithContext(agentCtx, AppendNoteOptions{
		Text: "agent marker", ScopeKind: string(ScopeAgent),
	})
	require.NoError(t, err)
	sessionRecord, err := store.AppendWithContext(sessionCtx, AppendNoteOptions{
		Text: "session marker", ScopeKind: string(ScopeSession),
	})
	require.NoError(t, err)
	taskRecord, err := store.AppendWithContext(taskCtx, AppendNoteOptions{
		Text: "task marker", ScopeKind: string(ScopeTask),
	})
	require.NoError(t, err)

	_, err = store.GetRecord(otherAgentCtx, agentRecord.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = store.GetRecord(sessionCtx, sessionRecord.ID)
	require.NoError(t, err)
	_, err = store.GetRecord(baseCtx, sessionRecord.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = store.GetRecord(taskCtx, taskRecord.ID)
	require.NoError(t, err)
	_, err = store.GetRecord(baseCtx, taskRecord.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)

	combinedCtx := WithMemoryScope(baseCtx, ScopeContext{
		OwnerID: "alice", AgentID: "agent-1", SessionID: "session-1", TaskID: "task-1",
	})
	page, err := store.ListRecords(combinedCtx, RecordFilter{Limit: 20})
	require.NoError(t, err)
	ids := recordIDs(page.Items)
	require.Contains(t, ids, projectRecord.ID)
	require.Contains(t, ids, agentRecord.ID)
	require.Contains(t, ids, sessionRecord.ID)
	require.Contains(t, ids, taskRecord.ID)
}

func TestMemoryScopeTenantAndMissingContextFailClosed(t *testing.T) {
	store := newScopedTestStore(t, "scope-tenant-project", "tenant-a")
	ctx := context.Background()
	record, err := store.AppendWithContext(ctx, AppendNoteOptions{Text: "tenant-a project memory"})
	require.NoError(t, err)

	_, err = store.ListRecords(WithMemoryScope(ctx, ScopeContext{
		TenantID: "tenant-b", OwnerID: "alice",
	}), RecordFilter{Limit: 10})
	require.ErrorContains(t, err, "does not match store tenant")

	_, _, err = buildMemoryScopeVisibility(ScopeContext{}, "", ScopePermissionRead, time.Now().UTC())
	require.ErrorIs(t, err, ErrMemoryScopeContextMissing)

	_, err = store.GetRecord(WithMemoryScope(ctx, ScopeContext{TenantID: "tenant-b"}), record.ID)
	require.ErrorContains(t, err, "does not match store tenant")
}

func TestMemoryScopeExpiredGrantIsInvisible(t *testing.T) {
	store := newScopedTestStore(t, "scope-expired-grant", "")
	baseCtx := context.Background()
	aliceCtx := WithMemoryScope(baseCtx, ScopeContext{OwnerID: "alice"})
	bobCtx := WithMemoryScope(baseCtx, ScopeContext{OwnerID: "bob"})

	alice, err := store.AppendWithContext(aliceCtx, AppendNoteOptions{
		Text: "expired grant marker", ScopeKind: string(ScopeUser),
	})
	require.NoError(t, err)
	_, err = store.CreateMemoryScopeGrant(baseCtx, MemoryScopeGrant{
		GrantorID:   "admin",
		GranteeType: "user",
		GranteeID:   "bob",
		ScopeKind:   ScopeUser,
		ScopeKey:    "alice",
		Permission:  string(ScopePermissionRead),
		ExpiresAt:   time.Now().UTC().Add(-time.Minute),
	})
	require.NoError(t, err)

	_, err = store.GetRecord(bobCtx, alice.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestMemoryScopeInjectionUsesCallerChain(t *testing.T) {
	store := newScopedTestStore(t, "scope-injection", "")
	baseCtx := context.Background()
	aliceCtx := WithMemoryScope(baseCtx, ScopeContext{OwnerID: "alice"})
	bobCtx := WithMemoryScope(baseCtx, ScopeContext{OwnerID: "bob"})

	shared, err := store.AppendWithContext(baseCtx, AppendNoteOptions{
		Text: "shared injection marker for project recall",
	})
	require.NoError(t, err)
	alice, err := store.AppendWithContext(aliceCtx, AppendNoteOptions{
		Text: "alice injection marker for scoped recall", ScopeKind: string(ScopeUser),
	})
	require.NoError(t, err)
	bob, err := store.AppendWithContext(bobCtx, AppendNoteOptions{
		Text: "bob injection marker must stay private", ScopeKind: string(ScopeUser),
	})
	require.NoError(t, err)

	selection, err := store.SelectForInjectV2(InjectOptions{
		Query: "injection marker", Limit: 10, TokenBudget: 2000,
		Scope: ScopeContext{OwnerID: "alice"},
	})
	require.NoError(t, err)
	require.Contains(t, selection.IDs, shared.ID)
	require.Contains(t, selection.IDs, alice.ID)
	require.NotContains(t, selection.IDs, bob.ID)

	projectOnly, err := store.SelectForInjectV2(InjectOptions{
		Query: "injection marker", Limit: 10, TokenBudget: 2000,
	})
	require.NoError(t, err)
	require.Contains(t, projectOnly.IDs, shared.ID)
	require.NotContains(t, projectOnly.IDs, alice.ID)
	require.NotContains(t, projectOnly.IDs, bob.ID)
}

func TestMemoryScopeUsageIsolation(t *testing.T) {
	store := newScopedTestStore(t, "scope-usage", "")
	baseCtx := context.Background()
	aliceCtx := WithMemoryScope(baseCtx, ScopeContext{OwnerID: "alice"})
	bobCtx := WithMemoryScope(baseCtx, ScopeContext{OwnerID: "bob"})

	alice, err := store.AppendWithContext(aliceCtx, AppendNoteOptions{
		Text: "alice usage marker", ScopeKind: string(ScopeUser),
	})
	require.NoError(t, err)

	result, err := store.RecordExposure(aliceCtx, Exposure{
		MemoryIDs: []string{alice.ID}, SessionID: "session-alice", UseKind: UsageRead,
	})
	require.NoError(t, err)
	require.Equal(t, 1, result.Recorded)

	result, err = store.RecordExposure(bobCtx, Exposure{
		MemoryIDs: []string{alice.ID}, SessionID: "session-bob", UseKind: UsageRead,
	})
	require.NoError(t, err)
	require.Equal(t, 0, result.Recorded)
	require.Equal(t, 1, result.Skipped)

	bobUsage, err := store.ListUsage(bobCtx, UsageFilter{MemoryID: alice.ID, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, bobUsage)
	aliceUsage, err := store.ListUsage(aliceCtx, UsageFilter{MemoryID: alice.ID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, aliceUsage, 1)
}

func TestMemoryScopeSchemaAndBackfillRepair(t *testing.T) {
	store := newScopedTestStore(t, "scope-backfill", "")
	version, err := store.SchemaVersion()
	require.NoError(t, err)
	require.Equal(t, 33, version)

	record, err := store.Append(AppendNoteOptions{Text: "backfill marker"})
	require.NoError(t, err)
	db, err := store.ensureV2()
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE memory_entries SET scope_key = '' WHERE id = ?`, record.ID)
	require.NoError(t, err)
	require.NoError(t, backfillMemoryScopeColumns(context.Background(), db))

	var scopeKind, scopeKey string
	require.NoError(t, db.QueryRow(`
SELECT scope_kind, scope_key FROM memory_entries WHERE id = ?
`, record.ID).Scan(&scopeKind, &scopeKey))
	require.Equal(t, "project", scopeKind)
	require.Equal(t, store.ProjectKey(), scopeKey)

	legacyUser, err := store.Append(AppendNoteOptions{Text: "legacy user scope must fail closed"})
	require.NoError(t, err)
	_, err = db.Exec(`
UPDATE memory_entries
SET scope = 'user', scope_kind = 'project', scope_key = '', owner_id = ''
WHERE id = ?
`, legacyUser.ID)
	require.NoError(t, err)
	require.NoError(t, backfillMemoryScopeColumns(context.Background(), db))
	var legacyKind, legacyKey string
	require.NoError(t, db.QueryRow(`
SELECT scope_kind, scope_key FROM memory_entries WHERE id = ?
`, legacyUser.ID).Scan(&legacyKind, &legacyKey))
	require.Equal(t, "user", legacyKind)
	require.Empty(t, legacyKey)
	_, err = store.GetRecord(context.Background(), legacyUser.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)

	var grants int
	require.NoError(t, db.QueryRow(`SELECT COUNT(1) FROM memory_scope_grants`).Scan(&grants))
	require.Equal(t, 0, grants)
	_, err = store.CreateMemoryScopeGrant(context.Background(), MemoryScopeGrant{
		GrantorID:   "admin",
		GranteeType: "user",
		GranteeID:   "alice",
		ScopeKind:   ScopeUser,
		ScopeKey:    "alice",
		Permission:  "read",
	})
	require.NoError(t, err)
}
