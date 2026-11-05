package sessionpurge

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQueryReferencesExistingTablePropagatesDatabaseError(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "closed.sqlite"))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = queryReferencesExistingTable(context.Background(), db, `SELECT session_id, MAX(updated_at) FROM sessions GROUP BY session_id`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "database is closed")
}
