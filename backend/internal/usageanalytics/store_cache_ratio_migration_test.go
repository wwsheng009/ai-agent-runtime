package usageanalytics

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStoreRepairsMissingV5CacheRatioColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	store, err := Open(Config{Path: path})
	require.NoError(t, err)
	_, err = store.db.Exec(`INSERT INTO usage_requests
		(llm_request_id, session_id, cache_status, status, record_json)
		VALUES ('ratio-repair', 'session-1', 'hit', 'success',
		'{"cache_hit_ratio":0.25,"cache_write_ratio":0.5}')`)
	require.NoError(t, err)
	_, err = store.db.Exec("ALTER TABLE usage_requests DROP COLUMN cache_hit_ratio")
	require.NoError(t, err)
	_, err = store.db.Exec("ALTER TABLE usage_requests DROP COLUMN cache_write_ratio")
	require.NoError(t, err)
	_, err = store.db.Exec("PRAGMA user_version = 5")
	require.NoError(t, err)
	require.NoError(t, store.Close())

	reopened, err := Open(Config{Path: path})
	require.NoError(t, err)
	defer func() { _ = reopened.Close() }()

	var hitRatio, writeRatio float64
	err = reopened.db.QueryRow(`SELECT cache_hit_ratio, cache_write_ratio
		FROM usage_requests WHERE llm_request_id = 'ratio-repair'`).Scan(&hitRatio, &writeRatio)
	require.NoError(t, err)
	require.Equal(t, 0.25, hitRatio)
	require.Equal(t, 0.5, writeRatio)
}
