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
		{
			Version: 3,
			Name:    "memory_v3_turn_digests",
			UpSQL: `
CREATE TABLE IF NOT EXISTS memory_turn_digests (
    digest_seq      INTEGER PRIMARY KEY AUTOINCREMENT,
    id              TEXT NOT NULL UNIQUE,
    scope           TEXT NOT NULL DEFAULT 'project',
    project_key     TEXT NOT NULL,
    session_id      TEXT NOT NULL,
    turn_id         TEXT NOT NULL,
    trace_id        TEXT,
    parent_session_id TEXT,
    agent_type      TEXT,
    user_message_id TEXT,
    assistant_message_id TEXT,
    user_text       TEXT NOT NULL DEFAULT '',
    assistant_text  TEXT NOT NULL DEFAULT '',
    text_truncated  INTEGER NOT NULL DEFAULT 0,
    outcome         TEXT NOT NULL DEFAULT 'success',
    steps           INTEGER NOT NULL DEFAULT 0,
    usage_json      TEXT NOT NULL DEFAULT '{}',
    metadata_json   TEXT NOT NULL DEFAULT '{}',
    digest_hash     TEXT NOT NULL DEFAULT '',
    digest_revision INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'pending',
    process_job_id  TEXT,
    last_error      TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    processed_at    TEXT,
    UNIQUE(project_key, session_id, turn_id)
);

CREATE INDEX IF NOT EXISTS idx_memory_turn_digests_pending
  ON memory_turn_digests(project_key, session_id, status, digest_seq);

CREATE INDEX IF NOT EXISTS idx_memory_turn_digests_session
  ON memory_turn_digests(session_id, digest_seq);

CREATE INDEX IF NOT EXISTS idx_memory_turn_digests_created
  ON memory_turn_digests(created_at);
`,
		},
		{
			Version: 4,
			Name:    "memory_v4_turn_digest_message_seq",
			UpSQL: `
ALTER TABLE memory_turn_digests
  ADD COLUMN message_seq_end INTEGER NOT NULL DEFAULT 0;
`,
		},
		{
			Version: 5,
			Name:    "memory_v5_window_source_identity",
			UpSQL: `
ALTER TABLE memory_sources
  ADD COLUMN source_window TEXT;
`,
		},
		{
			Version: 6,
			Name:    "memory_v6_turn_windows",
			UpSQL: `
ALTER TABLE memory_turn_digests
  ADD COLUMN rollup_status TEXT NOT NULL DEFAULT 'pending';

ALTER TABLE memory_turn_digests
  ADD COLUMN rollup_window_id TEXT;

ALTER TABLE memory_turn_digests
  ADD COLUMN rolled_up_at TEXT;

-- Rows created before rollup existed have already been through the old
-- extraction lifecycle. Mark them skipped so retention is not blocked forever
-- by an L1 window that can no longer be reconstructed.
UPDATE memory_turn_digests
SET rollup_status = 'skipped'
WHERE status IN ('done', 'skipped', 'invalidated', 'failed');

CREATE TABLE IF NOT EXISTS memory_turn_windows (
    window_seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    id                  TEXT NOT NULL UNIQUE,
    scope               TEXT NOT NULL DEFAULT 'project',
    project_key         TEXT NOT NULL,
    session_id          TEXT NOT NULL,
    window_index        INTEGER NOT NULL DEFAULT 0,
    start_digest_seq    INTEGER NOT NULL,
    end_digest_seq      INTEGER NOT NULL,
    start_message_seq   INTEGER NOT NULL DEFAULT 0,
    end_message_seq     INTEGER NOT NULL DEFAULT 0,
    turn_count          INTEGER NOT NULL DEFAULT 0,
    summary_text        TEXT NOT NULL DEFAULT '',
    summary_hash        TEXT NOT NULL DEFAULT '',
    summary_revision    INTEGER NOT NULL DEFAULT 0,
    source_hash         TEXT NOT NULL DEFAULT '',
    source_revision     INTEGER NOT NULL DEFAULT 0,
    status              TEXT NOT NULL DEFAULT 'pending',
    model               TEXT,
    usage_json          TEXT NOT NULL DEFAULT '{}',
    metadata_json       TEXT NOT NULL DEFAULT '{}',
    last_error          TEXT,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    processed_at        TEXT,
    UNIQUE(project_key, session_id, start_digest_seq, end_digest_seq)
);

CREATE INDEX IF NOT EXISTS idx_memory_turn_windows_session
  ON memory_turn_windows(project_key, session_id, window_seq);

CREATE INDEX IF NOT EXISTS idx_memory_turn_windows_status
  ON memory_turn_windows(project_key, status, window_seq);

CREATE INDEX IF NOT EXISTS idx_memory_turn_digests_rollup
  ON memory_turn_digests(project_key, session_id, status, rollup_status, digest_seq);
`,
		},
		{
			Version: 7,
			Name:    "memory_v7_project_scoped_audit_usage",
			UpSQL: `
ALTER TABLE memory_events
  ADD COLUMN project_key TEXT NOT NULL DEFAULT '';

ALTER TABLE memory_usage
  ADD COLUMN project_key TEXT NOT NULL DEFAULT '';

-- Backfill the owner project without requiring the source row to survive
-- future tombstone cleanup. Summaries also emit audit events, so resolve both
-- entry and summary ids.
UPDATE memory_events
SET project_key = COALESCE(
    (SELECT project_key FROM memory_entries WHERE id = memory_events.memory_id),
    (SELECT project_key FROM memory_summaries WHERE id = memory_events.memory_id),
    ''
)
WHERE project_key = '';

UPDATE memory_usage
SET project_key = COALESCE(
    (SELECT project_key FROM memory_entries WHERE id = memory_usage.memory_id),
    ''
)
WHERE project_key = '';

CREATE INDEX IF NOT EXISTS idx_memory_events_project_created
  ON memory_events(project_key, created_at);

CREATE INDEX IF NOT EXISTS idx_memory_usage_project_used
  ON memory_usage(project_key, used_at);
`,
		},
		{
			Version: 8,
			Name:    "memory_v8_runtime_metrics",
			UpSQL: `
CREATE TABLE IF NOT EXISTS memory_runtime_metrics (
    project_key TEXT NOT NULL,
    metric      TEXT NOT NULL,
    value       INTEGER NOT NULL DEFAULT 0,
    updated_at  TEXT NOT NULL,
    PRIMARY KEY(project_key, metric)
);
			`,
		},
		{
			Version: 9,
			Name:    "memory_v9_turn_capture_identity",
			UpSQL: `
ALTER TABLE memory_turn_digests
  ADD COLUMN message_seq_start INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memory_turn_digests
  ADD COLUMN capture_receipt_id TEXT NOT NULL DEFAULT '';
ALTER TABLE memory_turn_digests
  ADD COLUMN source_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memory_turn_digests
  ADD COLUMN range_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE memory_turn_digests
  ADD COLUMN enqueued_at TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_memory_turn_digests_capture_receipt
  ON memory_turn_digests(project_key, capture_receipt_id)
  WHERE capture_receipt_id <> '';
CREATE INDEX IF NOT EXISTS idx_memory_turn_digests_capture_pending
  ON memory_turn_digests(project_key, capture_receipt_id, status);
`,
		},
		{
			Version: 10,
			Name:    "memory_v10_turn_capture_output_receipts",
			UpSQL: `
CREATE TABLE IF NOT EXISTS memory_turn_output_receipts (
    id                  TEXT PRIMARY KEY,
    project_key         TEXT NOT NULL,
    session_id          TEXT NOT NULL,
    turn_id             TEXT NOT NULL,
    capture_receipt_id  TEXT NOT NULL,
    digest_id           TEXT NOT NULL,
    job_id              TEXT NOT NULL UNIQUE,
    source_revision     INTEGER NOT NULL,
    range_hash          TEXT NOT NULL,
    output_hash         TEXT NOT NULL,
    candidate_count     INTEGER NOT NULL DEFAULT 0,
    created_at          TEXT NOT NULL,
    UNIQUE(project_key, capture_receipt_id, source_revision, range_hash)
);
CREATE INDEX IF NOT EXISTS idx_memory_turn_output_receipts_session
  ON memory_turn_output_receipts(project_key, session_id, turn_id, created_at);
`,
		},
		{
			Version: 11,
			Name:    "memory_v11_maintenance_leases",
			UpSQL: `
CREATE TABLE IF NOT EXISTS memory_maintenance_leases (
    project_key     TEXT PRIMARY KEY,
    owner_id        TEXT NOT NULL,
    owner_kind      TEXT NOT NULL,
    acquired_at_ms  INTEGER NOT NULL,
    heartbeat_at_ms INTEGER NOT NULL,
    expires_at_ms   INTEGER NOT NULL,
    fencing_token   INTEGER NOT NULL DEFAULT 1
);
`,
		},
		{
			Version: 12,
			Name:    "memory_v12_session_purge_fences",
			UpSQL: `
CREATE TABLE IF NOT EXISTS memory_session_purges (
    project_key TEXT NOT NULL,
    session_id  TEXT NOT NULL,
    generation  INTEGER NOT NULL,
    state       TEXT NOT NULL DEFAULT 'purged',
    reason      TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    PRIMARY KEY(project_key, session_id)
);

CREATE INDEX IF NOT EXISTS idx_memory_session_purges_generation
  ON memory_session_purges(project_key, generation);

-- Capture generations are separate from the digest projection so existing
-- digest readers do not need a wider SELECT/scan contract. They bind a
-- canonical receipt and its session incarnation to every materialized job.
CREATE TABLE IF NOT EXISTS memory_turn_capture_generations (
    project_key        TEXT NOT NULL,
    capture_receipt_id TEXT NOT NULL,
    session_id         TEXT NOT NULL,
    turn_id            TEXT NOT NULL,
    generation         INTEGER NOT NULL DEFAULT 0,
    created_at         TEXT NOT NULL,
    PRIMARY KEY(project_key, capture_receipt_id)
);

CREATE INDEX IF NOT EXISTS idx_memory_turn_capture_generations_session
  ON memory_turn_capture_generations(project_key, session_id, generation);
`,
		},
		{
			Version: 13,
			Name:    "memory_v13_generation_qualified_turn_capture",
			UpSQL: `
-- The original table-level UNIQUE(project_key, session_id, turn_id) made a
-- changed canonical revision collide with the first capture of that turn.
-- Rebuild while preserving digest_seq and every persisted legacy projection.
ALTER TABLE memory_turn_digests RENAME TO memory_turn_digests_v12;
DROP INDEX IF EXISTS idx_memory_turn_digests_pending;
DROP INDEX IF EXISTS idx_memory_turn_digests_session;
DROP INDEX IF EXISTS idx_memory_turn_digests_created;
DROP INDEX IF EXISTS idx_memory_turn_digests_rollup;
DROP INDEX IF EXISTS idx_memory_turn_digests_capture_receipt;
DROP INDEX IF EXISTS idx_memory_turn_digests_capture_pending;
CREATE TABLE memory_turn_digests (
    digest_seq      INTEGER PRIMARY KEY AUTOINCREMENT,
    id              TEXT NOT NULL UNIQUE,
    scope           TEXT NOT NULL DEFAULT 'project',
    project_key     TEXT NOT NULL,
    session_id      TEXT NOT NULL,
    session_generation INTEGER NOT NULL DEFAULT 0,
    turn_id         TEXT NOT NULL,
    trace_id        TEXT,
    parent_session_id TEXT,
    agent_type      TEXT,
    user_message_id TEXT,
    assistant_message_id TEXT,
    user_text       TEXT NOT NULL DEFAULT '',
    assistant_text  TEXT NOT NULL DEFAULT '',
    text_truncated  INTEGER NOT NULL DEFAULT 0,
    outcome         TEXT NOT NULL DEFAULT 'success',
    steps           INTEGER NOT NULL DEFAULT 0,
    usage_json      TEXT NOT NULL DEFAULT '{}',
    metadata_json   TEXT NOT NULL DEFAULT '{}',
    digest_hash     TEXT NOT NULL DEFAULT '',
    digest_revision INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'pending',
    process_job_id  TEXT,
    last_error      TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    processed_at    TEXT,
    message_seq_end INTEGER NOT NULL DEFAULT 0,
    rollup_status   TEXT NOT NULL DEFAULT 'pending',
    rollup_window_id TEXT,
    rolled_up_at    TEXT,
    message_seq_start INTEGER NOT NULL DEFAULT 0,
    capture_receipt_id TEXT NOT NULL DEFAULT '',
    source_revision INTEGER NOT NULL DEFAULT 0,
    range_hash      TEXT NOT NULL DEFAULT '',
    enqueued_at     TEXT
);
INSERT INTO memory_turn_digests (
    digest_seq, id, scope, project_key, session_id, session_generation, turn_id,
    trace_id, parent_session_id, agent_type, user_message_id, assistant_message_id,
    user_text, assistant_text, text_truncated, outcome, steps, usage_json,
    metadata_json, digest_hash, digest_revision, status, process_job_id, last_error,
    created_at, updated_at, processed_at, message_seq_end, rollup_status,
    rollup_window_id, rolled_up_at, message_seq_start, capture_receipt_id,
    source_revision, range_hash, enqueued_at
)
SELECT digest_seq, id, scope, project_key, session_id, 0, turn_id,
       trace_id, parent_session_id, agent_type, user_message_id, assistant_message_id,
       user_text, assistant_text, text_truncated, outcome, steps, usage_json,
       metadata_json, digest_hash, digest_revision, status, process_job_id, last_error,
       created_at, updated_at, processed_at, message_seq_end, rollup_status,
       rollup_window_id, rolled_up_at, message_seq_start, capture_receipt_id,
       source_revision, range_hash, enqueued_at
FROM memory_turn_digests_v12;
DROP TABLE memory_turn_digests_v12;

CREATE UNIQUE INDEX idx_memory_turn_digests_legacy_turn
  ON memory_turn_digests(project_key, session_id, turn_id)
  WHERE capture_receipt_id = '';
CREATE UNIQUE INDEX idx_memory_turn_digests_capture_receipt
  ON memory_turn_digests(project_key, capture_receipt_id)
  WHERE capture_receipt_id <> '';
CREATE INDEX idx_memory_turn_digests_capture_pending
  ON memory_turn_digests(project_key, capture_receipt_id, status);
CREATE INDEX idx_memory_turn_digests_pending
  ON memory_turn_digests(project_key, session_id, status, digest_seq);
CREATE INDEX idx_memory_turn_digests_session
  ON memory_turn_digests(session_id, digest_seq);
CREATE INDEX idx_memory_turn_digests_created
  ON memory_turn_digests(created_at);
CREATE INDEX idx_memory_turn_digests_rollup
  ON memory_turn_digests(project_key, session_id, status, rollup_status, digest_seq);

-- A v2 job carries a structured identity; a v2 row migrated without its full
-- identity is quarantined rather than being claimed by an unsafe worker.
ALTER TABLE memory_jobs ADD COLUMN session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE memory_jobs ADD COLUMN session_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memory_jobs ADD COLUMN capture_receipt_id TEXT NOT NULL DEFAULT '';
ALTER TABLE memory_jobs ADD COLUMN source_start_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memory_jobs ADD COLUMN source_end_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memory_jobs ADD COLUMN source_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memory_jobs ADD COLUMN source_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE memory_jobs ADD COLUMN available_at TEXT;
ALTER TABLE memory_jobs ADD COLUMN deadline_at TEXT;
ALTER TABLE memory_jobs ADD COLUMN heartbeat_at TEXT;
ALTER TABLE memory_jobs ADD COLUMN deferred_until TEXT;
ALTER TABLE memory_jobs ADD COLUMN dead_letter_at TEXT;
ALTER TABLE memory_jobs ADD COLUMN dead_letter_reason TEXT;
ALTER TABLE memory_jobs ADD COLUMN repair_only INTEGER NOT NULL DEFAULT 0;

UPDATE memory_jobs
SET session_id = (
        SELECT COALESCE((
            SELECT d.session_id FROM memory_turn_digests d
            WHERE d.process_job_id = memory_jobs.id AND d.capture_receipt_id <> ''
            LIMIT 1
        ), '')
    ),
    session_generation = COALESCE((
        SELECT g.generation
        FROM memory_turn_digests d
        JOIN memory_turn_capture_generations g
          ON g.project_key = d.project_key AND g.capture_receipt_id = d.capture_receipt_id
        WHERE d.process_job_id = memory_jobs.id AND d.capture_receipt_id <> ''
        LIMIT 1
    ), 0),
    capture_receipt_id = COALESCE((
        SELECT d.capture_receipt_id FROM memory_turn_digests d
        WHERE d.process_job_id = memory_jobs.id AND d.capture_receipt_id <> ''
        LIMIT 1
    ), ''),
    source_start_seq = COALESCE((
        SELECT d.message_seq_start FROM memory_turn_digests d
        WHERE d.process_job_id = memory_jobs.id AND d.capture_receipt_id <> ''
        LIMIT 1
    ), 0),
    source_end_seq = COALESCE((
        SELECT d.message_seq_end FROM memory_turn_digests d
        WHERE d.process_job_id = memory_jobs.id AND d.capture_receipt_id <> ''
        LIMIT 1
    ), 0),
    source_revision = COALESCE((
        SELECT d.source_revision FROM memory_turn_digests d
        WHERE d.process_job_id = memory_jobs.id AND d.capture_receipt_id <> ''
        LIMIT 1
    ), 0),
    source_hash = COALESCE((
        SELECT d.range_hash FROM memory_turn_digests d
        WHERE d.process_job_id = memory_jobs.id AND d.capture_receipt_id <> ''
        LIMIT 1
    ), ''),
    repair_only = CASE
        WHEN kind = 'phase1_extract/v2' AND (
            COALESCE((
                SELECT g.generation
                FROM memory_turn_digests d
                JOIN memory_turn_capture_generations g
                  ON g.project_key = d.project_key AND g.capture_receipt_id = d.capture_receipt_id
                WHERE d.process_job_id = memory_jobs.id AND d.capture_receipt_id <> ''
                LIMIT 1
            ), 0) <= 0
        ) THEN 1 ELSE repair_only
    END
WHERE kind = 'phase1_extract/v2';

CREATE INDEX idx_memory_jobs_capture_identity
  ON memory_jobs(project_key, session_id, session_generation, capture_receipt_id);
CREATE INDEX idx_memory_jobs_available
  ON memory_jobs(project_key, kind, status, available_at, priority DESC, created_at);

CREATE TABLE IF NOT EXISTS memory_session_extraction_leases (
    project_key TEXT NOT NULL,
    session_id TEXT NOT NULL,
    session_generation INTEGER NOT NULL,
    owner_id TEXT NOT NULL,
    lease_until TEXT NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 1,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(project_key, session_id, session_generation)
);

ALTER TABLE memory_session_purges
  ADD COLUMN fenced_session_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memory_session_purges
  ADD COLUMN purge_generation INTEGER NOT NULL DEFAULT 0;
UPDATE memory_session_purges
SET fenced_session_generation = generation,
    purge_generation = generation
WHERE fenced_session_generation = 0 OR purge_generation = 0;
`,
		},
		{
			Version: 14,
			Name:    "memory_v14_record_source_session_generation",
			UpSQL: `
ALTER TABLE memory_entries
  ADD COLUMN source_session_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memory_sources
  ADD COLUMN source_session_generation INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_memory_entries_source_session_generation
  ON memory_entries(project_key, source_session_id, source_session_generation);
CREATE INDEX idx_memory_sources_session_generation
  ON memory_sources(session_id, source_session_generation, memory_id);
`,
		},
		{
			Version: 15,
			Name:    "memory_v15_generation_qualified_turn_identity",
			UpSQL: `
DROP INDEX IF EXISTS idx_memory_turn_digests_legacy_turn;
CREATE UNIQUE INDEX idx_memory_turn_digests_legacy_turn
  ON memory_turn_digests(project_key, session_id, session_generation, turn_id)
  WHERE capture_receipt_id = '';
`,
		},
		{
			Version: 16,
			Name:    "memory_v16_generation_qualified_turn_windows",
			UpSQL: `
-- Existing windows have no trustworthy incarnation identity. Keep them as
-- generation zero (legacy/quarantined) rather than inferring from digest seq.
DROP INDEX IF EXISTS idx_memory_turn_windows_session;
DROP INDEX IF EXISTS idx_memory_turn_windows_status;
ALTER TABLE memory_turn_windows RENAME TO memory_turn_windows_v15;
CREATE TABLE memory_turn_windows (
    window_seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    id                  TEXT NOT NULL UNIQUE,
    scope               TEXT NOT NULL DEFAULT 'project',
    project_key         TEXT NOT NULL,
    session_id          TEXT NOT NULL,
    session_generation  INTEGER NOT NULL DEFAULT 0,
    window_index        INTEGER NOT NULL DEFAULT 0,
    start_digest_seq    INTEGER NOT NULL,
    end_digest_seq      INTEGER NOT NULL,
    start_message_seq   INTEGER NOT NULL DEFAULT 0,
    end_message_seq     INTEGER NOT NULL DEFAULT 0,
    turn_count          INTEGER NOT NULL DEFAULT 0,
    summary_text        TEXT NOT NULL DEFAULT '',
    summary_hash        TEXT NOT NULL DEFAULT '',
    summary_revision    INTEGER NOT NULL DEFAULT 0,
    source_hash         TEXT NOT NULL DEFAULT '',
    source_revision     INTEGER NOT NULL DEFAULT 0,
    status              TEXT NOT NULL DEFAULT 'pending',
    model               TEXT,
    usage_json          TEXT NOT NULL DEFAULT '{}',
    metadata_json       TEXT NOT NULL DEFAULT '{}',
    last_error          TEXT,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    processed_at        TEXT,
    UNIQUE(project_key, session_id, session_generation, start_digest_seq, end_digest_seq)
);
INSERT INTO memory_turn_windows (
    window_seq, id, scope, project_key, session_id, session_generation, window_index,
    start_digest_seq, end_digest_seq, start_message_seq, end_message_seq, turn_count,
    summary_text, summary_hash, summary_revision, source_hash, source_revision, status,
    model, usage_json, metadata_json, last_error, created_at, updated_at, processed_at
)
SELECT window_seq, id, scope, project_key, session_id, 0, window_index,
       start_digest_seq, end_digest_seq, start_message_seq, end_message_seq, turn_count,
       summary_text, summary_hash, summary_revision, source_hash, source_revision, status,
       model, usage_json, metadata_json, last_error, created_at, updated_at, processed_at
FROM memory_turn_windows_v15;
DROP TABLE memory_turn_windows_v15;
CREATE INDEX idx_memory_turn_windows_session
  ON memory_turn_windows(project_key, session_id, session_generation, window_seq);
CREATE INDEX idx_memory_turn_windows_status
  ON memory_turn_windows(project_key, status, window_seq);
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
