package artifact

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

func TestStoreHealthProbePassesOnHealthyFileStore(t *testing.T) {
	t.Setenv(sqliteutil.HealthProbeEnv, "quick")
	path := filepath.Join(t.TempDir(), "artifacts.sqlite")

	store, err := NewStore(&StoreConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.Put(context.Background(), Record{
		SessionID: "health-session",
		ToolName:  "probe",
		Content:   "healthy",
	})
	require.NoError(t, err)
}

func TestStoreHealthProbeFailsClosedBeforeMigrationOnCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifacts.sqlite")
	seedCorruptArtifactStoreDB(t, path)

	t.Setenv(sqliteutil.HealthProbeEnv, "quick")
	store, err := NewStore(&StoreConfig{Path: path})
	require.NoError(t, err, "path-backed stores open lazily")
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.Put(context.Background(), Record{
		SessionID: "health-session",
		ToolName:  "probe",
		Content:   "must not be written",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "artifact store health probe")
	require.Contains(t, err.Error(), "quick_check")
}

// seedCorruptArtifactStoreDB creates a valid rollback-journal database and
// damages two data pages.  Keeping the fixture independent of the artifact
// migrations makes it prove that the health gate runs before schema writes.
func seedCorruptArtifactStoreDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	var journalMode string
	require.NoError(t, db.QueryRow("PRAGMA journal_mode=DELETE").Scan(&journalMode))
	_, err = db.Exec(`CREATE TABLE payload (id INTEGER PRIMARY KEY, body BLOB)`)
	require.NoError(t, err)
	for i := 0; i < 64; i++ {
		_, err = db.Exec(`INSERT INTO payload (body) VALUES (?)`, bytes.Repeat([]byte("x"), 4096))
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(3*4096))
	garbage := bytes.Repeat([]byte{0xff}, 4096)
	handle, err := os.OpenFile(path, os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = handle.WriteAt(garbage, 4096)
	require.NoError(t, err)
	_, err = handle.WriteAt(garbage, info.Size()-4096)
	require.NoError(t, err)
	require.NoError(t, handle.Close())

	// Make the precondition explicit: the fixture must be malformed before the
	// artifact store gets a chance to open it.
	check, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer func() { _ = check.Close() }()
	var result string
	err = check.QueryRow("PRAGMA quick_check").Scan(&result)
	if err == nil {
		require.NotEqual(t, "ok", strings.TrimSpace(strings.ToLower(result)))
	}
}
