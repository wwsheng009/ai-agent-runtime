package commands

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/sessionpurge"
)

func TestParsePruneSessionsAge(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"30d", 30 * 24 * time.Hour},
		{"2w", 14 * 24 * time.Hour},
		{"72h", 72 * time.Hour},
		{"90m", 90 * time.Minute},
	} {
		got, err := parsePruneSessionsAge(tc.raw)
		require.NoError(t, err, tc.raw)
		require.Equal(t, tc.want, got, tc.raw)
	}
	for _, raw := range []string{"", "0d", "-1d", "abc"} {
		_, err := parsePruneSessionsAge(raw)
		if raw == "" {
			require.NoError(t, err)
			continue
		}
		require.Error(t, err, raw)
	}
}

func TestStoragePruneSessionsDryRunThenApply(t *testing.T) {
	root := t.TempDir()
	history := filepath.Join(root, "session_history.sqlite")
	db, err := sql.Open("sqlite3", history)
	require.NoError(t, err)
	for _, statement := range []string{
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, updated_at TEXT NOT NULL, expires_at TEXT)`,
		`CREATE TABLE session_messages (session_id TEXT, seq INTEGER, created_at TEXT)`,
		`CREATE TABLE session_prompt_messages (session_id TEXT, position INTEGER, created_at TEXT)`,
	} {
		_, err := db.Exec(statement)
		require.NoError(t, err)
	}
	old := time.Now().UTC().Add(-60 * 24 * time.Hour).Format(time.RFC3339Nano)
	recent := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	_, err = db.Exec(`INSERT INTO sessions (id, updated_at, expires_at) VALUES ('old', ?, NULL), ('recent', ?, NULL)`, old, recent)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO session_messages (session_id, seq, created_at) VALUES ('old', 1, ?), ('recent', 1, ?)`, old, recent)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	paths := sessionpurge.Paths{History: history}
	var out, errOut bytes.Buffer
	code := runStoragePruneSessions(storagePruneSessionsOptions{
		out:            &out,
		errOut:         &errOut,
		json:           true,
		includeOrphans: false,
		olderThan:      30 * 24 * time.Hour,
		paths:          &paths,
		timeout:        time.Minute,
	})
	require.Equal(t, statsExitOK, code, "stderr=%s", errOut.String())
	var planned sessionpurge.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &planned))
	require.Equal(t, "planned", planned.Status)
	require.Equal(t, 1, planned.CandidateCount)
	require.Equal(t, int64(1), countSQLite(t, history, `SELECT COUNT(*) FROM sessions WHERE id='old'`))

	out.Reset()
	errOut.Reset()
	code = runStoragePruneSessions(storagePruneSessionsOptions{
		out:            &out,
		errOut:         &errOut,
		json:           true,
		apply:          true,
		includeOrphans: false,
		olderThan:      30 * 24 * time.Hour,
		paths:          &paths,
		timeout:        time.Minute,
	})
	require.Equal(t, statsExitOK, code, "stderr=%s", errOut.String())
	var applied sessionpurge.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &applied))
	require.Equal(t, "pruned", applied.Status)
	require.Equal(t, int64(0), countSQLite(t, history, `SELECT COUNT(*) FROM sessions WHERE id='old'`))
	require.Equal(t, int64(0), countSQLite(t, history, `SELECT COUNT(*) FROM session_messages WHERE session_id='old'`))
	require.Equal(t, int64(1), countSQLite(t, history, `SELECT COUNT(*) FROM sessions WHERE id='recent'`))
}

func countSQLite(t *testing.T, path, query string) int64 {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	var value int64
	require.NoError(t, db.QueryRow(query).Scan(&value))
	return value
}
