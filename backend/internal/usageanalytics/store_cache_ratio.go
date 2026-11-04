package usageanalytics

import (
	"database/sql"
	"fmt"
)

// cacheRatioSchemaVersion promotes cache ratios out of record_json. Querying
// them as REAL columns keeps request filters and summaries from parsing every
// JSON payload.
const cacheRatioSchemaVersion = 5

func (s *Store) migrateCacheRatioV5() error {
	if s == nil || s.readOnly || s.empty || s.db == nil {
		return nil
	}
	version, err := s.schemaVersion()
	if err != nil {
		return err
	}
	if version >= cacheRatioSchemaVersion {
		hit, hitErr := s.hasColumn("usage_requests", "cache_hit_ratio")
		write, writeErr := s.hasColumn("usage_requests", "cache_write_ratio")
		if hitErr != nil {
			return hitErr
		}
		if writeErr != nil {
			return writeErr
		}
		if hit && write {
			s.markCacheRatioColumnsReady()
			return nil
		}
	}
	if err := s.withWriteTx(func(tx *sql.Tx) error {
		// Very old v1 fixtures predate cache_status/record_json. The cache-ratio
		// migration needs those inputs, so repair them before backfilling.
		for _, column := range []struct {
			name string
			ddl  string
		}{
			{"cache_status", "TEXT NOT NULL DEFAULT ''"},
			{"status", "TEXT NOT NULL DEFAULT ''"},
			{"record_json", "BLOB NOT NULL DEFAULT ''"},
		} {
			if columnExistsTx(tx, "usage_requests", column.name) {
				continue
			}
			if _, err := tx.Exec("ALTER TABLE usage_requests ADD COLUMN " + column.name + " " + column.ddl); err != nil {
				return fmt.Errorf("migrate usage analytics db: add usage_requests.%s: %w", column.name, err)
			}
		}
		for _, column := range []string{"cache_hit_ratio", "cache_write_ratio"} {
			if columnExistsTx(tx, "usage_requests", column) {
				continue
			}
			if _, err := tx.Exec("ALTER TABLE usage_requests ADD COLUMN " + column + " REAL"); err != nil {
				return fmt.Errorf("migrate usage analytics db: add usage_requests.%s: %w", column, err)
			}
		}
		if _, err := tx.Exec(`UPDATE usage_requests SET
  cache_hit_ratio = CASE WHEN cache_status IN ('hit','write','confirmed_miss','reported_zero')
    AND json_valid(record_json) THEN CAST(json_extract(record_json, '$.cache_hit_ratio') AS REAL) END,
  cache_write_ratio = CASE WHEN cache_status <> 'not_reported' AND status <> 'error'
    AND json_valid(record_json) THEN CAST(json_extract(record_json, '$.cache_write_ratio') AS REAL) END
WHERE cache_hit_ratio IS NULL OR cache_write_ratio IS NULL`); err != nil {
			return fmt.Errorf("backfill usage_requests cache ratios: %w", err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", cacheRatioSchemaVersion)); err != nil {
			return fmt.Errorf("set usage analytics user_version=%d: %w", cacheRatioSchemaVersion, err)
		}
		return nil
	}); err != nil {
		return err
	}
	s.markCacheRatioColumnsReady()
	return nil
}

func (s *Store) markCacheRatioColumnsReady() {
	if s == nil {
		return
	}
	s.cacheRatioColumnOnce.Do(func() { s.cacheRatioColumns = true })
}

func (s *Store) cacheRatioColumnsReady() bool {
	if s == nil || s.db == nil || s.empty {
		return false
	}
	s.cacheRatioColumnOnce.Do(func() {
		hit, hitErr := s.hasColumn("usage_requests", "cache_hit_ratio")
		write, writeErr := s.hasColumn("usage_requests", "cache_write_ratio")
		s.cacheRatioColumns = hitErr == nil && writeErr == nil && hit && write
	})
	return s.cacheRatioColumns
}

func (s *Store) cacheHitRatioSQLExpr(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	if s.cacheRatioColumnsReady() {
		return "CASE WHEN " + reportedCacheStatusSQL(prefix+"cache_status") +
			" THEN COALESCE(" + prefix + "cache_hit_ratio, CASE WHEN json_valid(" +
			prefix + "record_json) THEN json_extract(" + prefix +
			"record_json, '$.cache_hit_ratio') END) END"
	}
	return "CASE WHEN " + reportedCacheStatusSQL(prefix+"cache_status") +
		" AND json_valid(" + prefix + "record_json) THEN json_extract(" +
		prefix + "record_json, '$.cache_hit_ratio') END"
}
