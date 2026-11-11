package memorystore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/migrate"
)

func initMemorySchema(ctx context.Context, db *sql.DB) error {
	migrations := []migrate.Migration{
		{
			Version: 1,
			Name:    "memory_v2_core",
			UpSQL: `
CREATE TABLE IF NOT EXISTS memory_entries (
    id                  TEXT PRIMARY KEY,
    scope               TEXT NOT NULL,
    project_key         TEXT NOT NULL DEFAULT '',
    kind                TEXT NOT NULL,
    status              TEXT NOT NULL,
    text                TEXT NOT NULL,
    normalized_text     TEXT NOT NULL,
    tags_json           TEXT NOT NULL DEFAULT '[]',
    confidence          REAL NOT NULL DEFAULT 0.0,
    sensitivity         TEXT NOT NULL DEFAULT 'internal',
    pinned              INTEGER NOT NULL DEFAULT 0,
    retention_class     TEXT NOT NULL DEFAULT 'standard',
    source_kind         TEXT NOT NULL,
    source_session_id   TEXT,
    source_turn_id      TEXT,
    source_refs_json    TEXT NOT NULL DEFAULT '[]',
    content_hash        TEXT NOT NULL,
    supersedes_id       TEXT,
    revision            INTEGER NOT NULL DEFAULT 1,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    last_used_at        TEXT,
    use_count           INTEGER NOT NULL DEFAULT 0,
    valid_from          TEXT,
    valid_until         TEXT,
    freshness_status    TEXT NOT NULL DEFAULT 'fresh',
    last_verified_at    TEXT,
    revalidate_after    TEXT,
    stale_reason        TEXT,
    deleted_at          TEXT,
    metadata_json       TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_memory_entries_scope_status
  ON memory_entries(scope, project_key, status, updated_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_memory_entries_active_hash
  ON memory_entries(scope, project_key, content_hash)
  WHERE status IN ('candidate', 'active');

CREATE INDEX IF NOT EXISTS idx_memory_entries_kind
  ON memory_entries(scope, project_key, kind, status);

CREATE INDEX IF NOT EXISTS idx_memory_entries_usage
  ON memory_entries(scope, project_key, status, use_count DESC, last_used_at DESC);

CREATE INDEX IF NOT EXISTS idx_memory_entries_freshness
  ON memory_entries(scope, project_key, freshness_status, revalidate_after);

CREATE TABLE IF NOT EXISTS memory_sources (
    id              TEXT PRIMARY KEY,
    memory_id       TEXT NOT NULL,
    source_kind     TEXT NOT NULL,
    source_id       TEXT NOT NULL,
    session_id      TEXT,
    turn_id         TEXT,
    message_ids_json TEXT NOT NULL DEFAULT '[]',
    artifact_refs_json TEXT NOT NULL DEFAULT '[]',
    fact_refs_json  TEXT NOT NULL DEFAULT '[]',
    knowledge_refs_json TEXT NOT NULL DEFAULT '[]',
    source_hash     TEXT NOT NULL DEFAULT '',
    source_revision INTEGER,
    source_updated_at TEXT,
    source_observed_at TEXT,
    quote           TEXT,
    created_at      TEXT NOT NULL,
    FOREIGN KEY(memory_id) REFERENCES memory_entries(id) ON DELETE CASCADE,
    UNIQUE(memory_id, source_kind, source_id, source_hash)
);

CREATE TABLE IF NOT EXISTS memory_events (
    id          TEXT PRIMARY KEY,
    memory_id   TEXT NOT NULL,
    event_type  TEXT NOT NULL,
    actor       TEXT NOT NULL,
    session_id  TEXT,
    revision    INTEGER NOT NULL,
    payload_json TEXT NOT NULL DEFAULT '{}',
    created_at  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_memory_events_memory
  ON memory_events(memory_id, created_at DESC);

CREATE TABLE IF NOT EXISTS memory_usage (
    id          TEXT PRIMARY KEY,
    memory_id   TEXT NOT NULL,
    session_id  TEXT NOT NULL,
    turn_id     TEXT,
    message_id  TEXT,
    used_at     TEXT NOT NULL,
    use_kind    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_memory_usage_memory
  ON memory_usage(memory_id, used_at DESC);

CREATE INDEX IF NOT EXISTS idx_memory_usage_session
  ON memory_usage(session_id, used_at DESC);

CREATE TABLE IF NOT EXISTS memory_suppressions (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    project_key     TEXT NOT NULL DEFAULT '',
    content_hash    TEXT NOT NULL,
    reason          TEXT NOT NULL,
    source_memory_id TEXT,
    created_at      TEXT NOT NULL,
    expires_at      TEXT,
    metadata_json   TEXT NOT NULL DEFAULT '{}',
    UNIQUE(scope, project_key, content_hash, reason)
);

CREATE INDEX IF NOT EXISTS idx_memory_suppressions_lookup
  ON memory_suppressions(scope, project_key, content_hash, reason);

CREATE TABLE IF NOT EXISTS memory_recall_traces (
    id              TEXT PRIMARY KEY,
    session_id      TEXT NOT NULL,
    turn_id         TEXT,
    project_key     TEXT NOT NULL,
    query_hash      TEXT NOT NULL,
    query_preview   TEXT,
    summary_revision INTEGER,
    candidate_count INTEGER NOT NULL DEFAULT 0,
    filtered_count  INTEGER NOT NULL DEFAULT 0,
    selected_ids_json TEXT NOT NULL DEFAULT '[]',
    dropped_json    TEXT NOT NULL DEFAULT '[]',
    token_estimate  INTEGER NOT NULL DEFAULT 0,
    token_budget    INTEGER NOT NULL DEFAULT 0,
    budget_source   TEXT,
    injected        INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_memory_recall_traces_session
  ON memory_recall_traces(session_id, created_at DESC);

CREATE TABLE IF NOT EXISTS memory_jobs (
    id              TEXT PRIMARY KEY,
    kind            TEXT NOT NULL,
    job_key         TEXT NOT NULL,
    scope           TEXT NOT NULL,
    project_key     TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL,
    priority        INTEGER NOT NULL DEFAULT 0,
    worker_id       TEXT,
    lease_until     TEXT,
    attempt         INTEGER NOT NULL DEFAULT 0,
    max_attempts    INTEGER NOT NULL DEFAULT 3,
    retry_at        TEXT,
    started_at      TEXT,
    finished_at     TEXT,
    last_error      TEXT,
    input_watermark TEXT,
    last_success_watermark TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    UNIQUE(kind, scope, project_key, job_key)
);

CREATE INDEX IF NOT EXISTS idx_memory_jobs_claim
  ON memory_jobs(status, retry_at, priority DESC, created_at);

CREATE TABLE IF NOT EXISTS memory_summaries (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    project_key     TEXT NOT NULL DEFAULT '',
    summary_kind    TEXT NOT NULL DEFAULT 'long_term',
    revision        INTEGER NOT NULL,
    status          TEXT NOT NULL,
    freshness_status TEXT NOT NULL DEFAULT 'fresh',
    summary         TEXT NOT NULL,
    summary_tokens  INTEGER NOT NULL DEFAULT 0,
    source_count    INTEGER NOT NULL DEFAULT 0,
    input_watermark TEXT,
    generated_at    TEXT NOT NULL,
    model           TEXT,
    content_hash    TEXT NOT NULL,
    record_ids_json TEXT NOT NULL DEFAULT '[]',
    metadata_json   TEXT NOT NULL DEFAULT '{}',
    UNIQUE(scope, project_key, summary_kind, revision)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_memory_summaries_active
  ON memory_summaries(scope, project_key, summary_kind)
  WHERE status = 'active';

CREATE TABLE IF NOT EXISTS memory_summary_items (
    summary_id      TEXT NOT NULL,
    memory_id       TEXT NOT NULL,
    rank            INTEGER NOT NULL,
    token_estimate  INTEGER NOT NULL DEFAULT 0,
    included_reason TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(summary_id, memory_id),
    FOREIGN KEY(summary_id) REFERENCES memory_summaries(id) ON DELETE CASCADE,
    FOREIGN KEY(memory_id) REFERENCES memory_entries(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_memory_summary_items_memory
  ON memory_summary_items(memory_id);

CREATE TABLE IF NOT EXISTS memory_session_modes (
    session_id      TEXT PRIMARY KEY,
    scope           TEXT NOT NULL DEFAULT 'project',
    project_key     TEXT NOT NULL DEFAULT '',
    mode            TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    metadata_json   TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_memory_session_modes_project
  ON memory_session_modes(project_key, mode, updated_at DESC);

CREATE TABLE IF NOT EXISTS memory_migration_runs (
    id            TEXT PRIMARY KEY,
    source_path   TEXT NOT NULL,
    source_size   INTEGER NOT NULL DEFAULT 0,
    source_mtime  TEXT,
    scanned       INTEGER NOT NULL DEFAULT 0,
    imported      INTEGER NOT NULL DEFAULT 0,
    skipped       INTEGER NOT NULL DEFAULT 0,
    corrupt       INTEGER NOT NULL DEFAULT 0,
    status        TEXT NOT NULL,
    error         TEXT,
    started_at    TEXT NOT NULL,
    finished_at   TEXT
);
`,
		},
		{
			Version: 2,
			Name:    "memory_v2_session_modes_project_scope",
			UpSQL: `
CREATE TABLE memory_session_modes_v2 (
    project_key     TEXT NOT NULL DEFAULT '',
    session_id      TEXT NOT NULL,
    scope           TEXT NOT NULL DEFAULT 'project',
    mode            TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    metadata_json   TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY(project_key, session_id)
);

INSERT OR REPLACE INTO memory_session_modes_v2 (
    project_key, session_id, scope, mode, updated_at, metadata_json
)
SELECT project_key, session_id, scope, mode, updated_at, metadata_json
FROM memory_session_modes;

DROP TABLE memory_session_modes;
ALTER TABLE memory_session_modes_v2 RENAME TO memory_session_modes;

CREATE INDEX IF NOT EXISTS idx_memory_session_modes_project
  ON memory_session_modes(project_key, mode, updated_at DESC);
`,
		},
	}
	if err := migrate.Apply(ctx, db, migrations); err != nil {
		return fmt.Errorf("memory v2 schema: %w", err)
	}
	// 04 §3.6 contract: expose the generic migration ledger under the memory
	// name for status/readiness queries. The write ledger remains
	// schema_migrations (internal/migrate).
	if _, err := db.ExecContext(ctx, `
CREATE VIEW IF NOT EXISTS memory_schema_version AS
SELECT version, applied_at FROM schema_migrations
`); err != nil {
		// Older SQLite builds can reject views created from a table while it is
		// locked; schema version is diagnostic only, so do not fail the store.
		if !strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return fmt.Errorf("memory schema version view: %w", err)
		}
	}
	return nil
}
