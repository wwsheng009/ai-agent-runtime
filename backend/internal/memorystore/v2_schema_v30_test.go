package memorystore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/migrate"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

func TestMemoryV30UpgradePreservesLegacyPurgeAndCaptureIdentity(t *testing.T) {
	ctx := context.Background()
	db, err := sqliteutil.OpenFile(filepath.Join(t.TempDir(), "memory-v29.db"), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	migrations := memoryMigrations()
	upToV29 := make([]migrate.Migration, 0, len(migrations))
	for _, migration := range migrations {
		if migration.Version <= 29 {
			upToV29 = append(upToV29, migration)
		}
	}
	require.NoError(t, migrate.Apply(ctx, db, upToV29))

	_, err = db.ExecContext(ctx, `
INSERT INTO memory_session_purges (
    project_key, session_id, generation, fenced_session_generation,
    purge_generation, state, reason, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, "legacy-project", "legacy-session", 7, 7, 7, "purged", "legacy", "2026-11-23T00:00:00Z", "2026-11-23T00:00:00Z")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
INSERT INTO memory_turn_capture_generations (
    project_key, capture_receipt_id, session_id, turn_id, generation, created_at
) VALUES (?, ?, ?, ?, ?, ?)
`, "legacy-project", "legacy-receipt", "legacy-session", "legacy-turn", 7, "2026-11-23T00:00:00Z")
	require.NoError(t, err)

	require.NoError(t, migrate.Apply(ctx, db, migrations))

	var tenantID string
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT tenant_id FROM memory_session_purges
WHERE project_key = ? AND session_id = ?
`, "legacy-project", "legacy-session").Scan(&tenantID))
	require.Empty(t, tenantID)
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT tenant_id FROM memory_turn_capture_generations
WHERE project_key = ? AND capture_receipt_id = ?
`, "legacy-project", "legacy-receipt").Scan(&tenantID))
	require.Empty(t, tenantID)

	_, err = db.ExecContext(ctx, `
INSERT INTO memory_session_purges (
    tenant_id, project_key, session_id, generation, fenced_session_generation,
    purge_generation, state, reason, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, "tenant-b", "legacy-project", "legacy-session", 1, 1, 1, "purged", "tenant-b",
		"2026-11-23T00:00:00Z", "2026-11-23T00:00:00Z")
	require.NoError(t, err, "v30 must allow the same project/session in another tenant")

	_, err = db.ExecContext(ctx, `
INSERT INTO memory_turn_capture_generations (
    tenant_id, project_key, capture_receipt_id, session_id, turn_id, generation, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
`, "tenant-b", "legacy-project", "legacy-receipt", "legacy-session", "legacy-turn", 1,
		"2026-11-23T00:00:00Z")
	require.NoError(t, err, "v30 must allow the same receipt identity in another tenant")

	var version int
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT COALESCE(MAX(version), 0) FROM schema_migrations
`).Scan(&version))
	require.GreaterOrEqual(t, version, 30)
}
