package memorystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

const recordVersionSelectColumns = `
id, scope, project_key, kind, status, text, normalized_text, tags_json,
confidence, sensitivity, pinned, retention_class, source_kind, source_session_id,
source_session_generation, source_turn_id, source_refs_json, content_hash,
supersedes_id, revision, created_at, updated_at, last_used_at, use_count,
valid_from, valid_until, freshness_status, last_verified_at, revalidate_after,
 stale_reason, deleted_at, metadata_json, title, summary, canonical_id, superseded_by, archived,
 tenant_id, owner_id, agent_id, task_id, scope_kind, scope_key,
 trust_level, trust_reason, trusted_at, feedback_score
`

// RecordVersion is an immutable snapshot of a memory entry at one revision.
type RecordVersion struct {
	ID            string    `json:"id"`
	MemoryID      string    `json:"memory_id"`
	Version       int64     `json:"version"`
	ProjectKey    string    `json:"project_key"`
	ScopeKind     Scope     `json:"scope_kind"`
	ScopeKey      string    `json:"scope_key"`
	Record        Record    `json:"record"`
	ChangedBy     string    `json:"changed_by"`
	ChangeReason  string    `json:"change_reason"`
	ChangeType    string    `json:"change_type"`
	SourceEventID string    `json:"source_event_id,omitempty"`
	ValidFrom     time.Time `json:"valid_from,omitempty"`
	ValidUntil    time.Time `json:"valid_until,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// RecordRollbackOptions selects one explicit historical version. The expected
// revision is required so rollback can never silently overwrite a concurrent
// edit. RevertSupersede must be explicit when reopening a superseded record.
type RecordRollbackOptions struct {
	TargetVersion    int64
	ExpectedRevision int64
	Reason           string
	ChangedBy        string
	RevertSupersede  bool
}

func backfillRecordVersionBaselines(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
SELECT `+recordVersionSelectColumns+`
FROM memory_entries AS e
WHERE NOT EXISTS (
    SELECT 1 FROM memory_versions AS v
    WHERE v.memory_id = e.id AND v.tenant_id = e.tenant_id
      AND v.project_key = e.project_key AND v.version = e.revision
)
ORDER BY e.tenant_id, e.project_key, e.id
`)
	if err != nil {
		return err
	}
	var records []Record
	for rows.Next() {
		record, scanErr := scanRecordColumns(rows)
		if scanErr != nil {
			_ = rows.Close()
			return scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, record := range records {
		if record.Revision <= 0 {
			record.Revision = 1
		}
		body, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("marshal baseline for %s: %w", record.ID, err)
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		scopeKind := record.ScopeKind
		if normalizeScopeKind(scopeKind) == "" {
			scopeKind = normalizeScopeKind(record.Scope)
		}
		if normalizeScopeKind(scopeKind) == "" {
			scopeKind = ScopeProject
		}
		scopeKey := record.ScopeKey
		if scopeKey == "" {
			scopeKey = record.ProjectKey
		}
		_, err = tx.ExecContext(ctx, `
INSERT OR IGNORE INTO memory_versions (
    id, memory_id, version, project_key, scope_kind, scope_key, record_json,
    changed_by, change_reason, change_type, source_event_id, valid_from,
    valid_until, created_at, tenant_id, owner_id, agent_id, task_id
) VALUES (?, ?, ?, ?, ?, ?, ?, 'schema_migration', 'baseline snapshot',
          'baseline', NULL, ?, ?, ?, ?, ?, ?, ?)
`, "mver_"+uuid.NewString(), record.ID, record.Revision, record.ProjectKey,
			string(scopeKind), scopeKey, string(body),
			nullableTime(record.ValidFrom), nullableTime(record.ValidUntil), now,
			record.TenantID, record.OwnerID, record.AgentID, record.TaskID)
		if err != nil {
			return fmt.Errorf("insert baseline for %s: %w", record.ID, err)
		}
	}
	return tx.Commit()
}

func loadRecordTx(ctx context.Context, tx *sql.Tx, id, tenantID, projectKey string) (Record, error) {
	return scanRecordColumns(tx.QueryRowContext(ctx, `
SELECT `+recordVersionSelectColumns+`
FROM memory_entries
WHERE id = ? AND tenant_id = ? AND project_key = ?
`, strings.TrimSpace(id), strings.TrimSpace(tenantID), strings.TrimSpace(projectKey)))
}

func appendRecordVersionTx(
	ctx context.Context,
	tx *sql.Tx,
	record Record,
	changedBy, reason, changeType, sourceEventID string,
) error {
	changedBy = strings.TrimSpace(changedBy)
	if changedBy == "" {
		changedBy = "system"
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "record changed"
	}
	changeType = strings.TrimSpace(changeType)
	if changeType == "" {
		changeType = "update"
	}
	if record.Revision <= 0 {
		return fmt.Errorf("memorystore: record version requires a positive revision")
	}
	body, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("memorystore: marshal record version: %w", err)
	}
	createdAt := record.UpdatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO memory_versions (
    id, memory_id, version, project_key, scope_kind, scope_key, record_json,
    changed_by, change_reason, change_type, source_event_id, valid_from,
    valid_until, created_at, tenant_id, owner_id, agent_id, task_id,
    source_session_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, "mver_"+uuid.NewString(), record.ID, record.Revision, record.ProjectKey,
		string(firstNonEmpty(string(record.ScopeKind), string(record.Scope))),
		firstNonEmpty(record.ScopeKey, record.ProjectKey), string(body), changedBy, reason,
		changeType, nullableString(sourceEventID), nullableTime(record.ValidFrom),
		nullableTime(record.ValidUntil), createdAt.UTC().Format(time.RFC3339Nano),
		record.TenantID, record.OwnerID, record.AgentID, record.TaskID,
		nullableString(record.SourceSessionID))
	if err != nil {
		return fmt.Errorf("memorystore: append record version %s/%d: %w", record.ID, record.Revision, err)
	}
	return nil
}

// appendCurrentRecordVersionTx reloads the written row from the same
// transaction so snapshots reflect database-normalized values, not stale
// pre-update structs.
func (s *Store) appendCurrentRecordVersionTx(
	ctx context.Context,
	tx *sql.Tx,
	id, projectKey, changedBy, reason, changeType, sourceEventID string,
) error {
	record, err := loadRecordTx(ctx, tx, id, s.tenantID(), projectKey)
	if err != nil {
		return err
	}
	return appendRecordVersionTx(ctx, tx, record, changedBy, reason, changeType, sourceEventID)
}

// purgeRecordVersionsTx is the narrowly scoped erasure escape hatch for
// explicit privacy operations (session purge, tombstone retention, and reset).
// Normal callers still hit the append-only trigger. The guard is removed
// before this helper returns, including when the delete statement fails.
func purgeRecordVersionsTx(
	ctx context.Context,
	tx *sql.Tx,
	projectKey string,
	deleteSQL string,
	args ...interface{},
) error {
	projectKey = strings.TrimSpace(projectKey)
	if projectKey == "" {
		return fmt.Errorf("memorystore: version purge requires project key")
	}
	if strings.TrimSpace(deleteSQL) == "" {
		return fmt.Errorf("memorystore: version purge requires delete statement")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO memory_version_purge_guards(project_key, created_at)
VALUES (?, ?)
`, projectKey, now); err != nil {
		return fmt.Errorf("memorystore: enable version purge guard: %w", err)
	}
	if _, err := tx.ExecContext(ctx, deleteSQL, args...); err != nil {
		if _, cleanupErr := tx.ExecContext(context.Background(), `
 DELETE FROM memory_version_purge_guards WHERE project_key = ?
`, projectKey); cleanupErr != nil {
			return fmt.Errorf("memorystore: purge record versions: %w (disable purge guard: %v)", err, cleanupErr)
		}
		return fmt.Errorf("memorystore: purge record versions: %w", err)
	}
	if _, err := tx.ExecContext(context.Background(), `
 DELETE FROM memory_version_purge_guards WHERE project_key = ?
`, projectKey); err != nil {
		return fmt.Errorf("memorystore: disable version purge guard: %w", err)
	}
	return nil
}

// ListRecordVersions returns immutable revisions for one record in the
// current project only. The record lookup intentionally preserves the same
// project isolation and not-found behavior as GetRecord.
func (s *Store) ListRecordVersions(ctx context.Context, id string) ([]RecordVersion, error) {
	versions, _, err := s.ListRecordVersionsPage(ctx, id, 0)
	return versions, err
}

// ListRecordVersionsPage returns the most recent bounded history window in
// ascending revision order. A zero limit preserves the unbounded legacy API.
// Positive limits must not exceed 500.
func (s *Store) ListRecordVersionsPage(ctx context.Context, id string, limit int) ([]RecordVersion, bool, error) {
	if limit < 0 || limit > 500 {
		return nil, false, fmt.Errorf("memorystore: version history limit must be between 1 and 500")
	}
	if _, err := s.GetRecord(ctx, id); err != nil {
		return nil, false, err
	}
	db, err := s.ensureV2()
	if err != nil {
		return nil, false, err
	}
	query := `
SELECT id, memory_id, version, project_key, scope_kind, scope_key, record_json,
       changed_by, change_reason, change_type, COALESCE(source_event_id, ''),
       valid_from, valid_until, created_at
FROM memory_versions
WHERE memory_id = ? AND tenant_id = ? AND project_key = ?
`
	args := []interface{}{strings.TrimSpace(id), s.tenantID(), s.cfg.ProjectKey}
	if limit == 0 {
		query += "ORDER BY version ASC"
	} else {
		query += "ORDER BY version DESC LIMIT ?"
		args = append(args, limit+1)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	versions := make([]RecordVersion, 0)
	for rows.Next() {
		version, err := scanRecordVersion(rows)
		if err != nil {
			return nil, false, err
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := false
	if limit > 0 {
		hasMore = len(versions) > limit
		if hasMore {
			versions = versions[:limit]
		}
		for left, right := 0, len(versions)-1; left < right; left, right = left+1, right-1 {
			versions[left], versions[right] = versions[right], versions[left]
		}
	}
	return versions, hasMore, nil
}

// ListRecordsAsOf returns the latest record snapshots committed by asOf whose
// fact-validity interval contains asOf. created_at is parsed in Go rather than
// compared as SQLite text because RFC3339Nano's optional fractional seconds do
// not sort lexically across values within the same second. This is a
// project-scoped query surface for historical facts; audit consumers that need
// deleted/invalid snapshots should use ListRecordVersions instead.
func (s *Store) ListRecordsAsOf(ctx context.Context, filter RecordFilter, asOf time.Time) (RecordPage, error) {
	if asOf.IsZero() {
		return RecordPage{}, fmt.Errorf("memorystore: as-of time is required")
	}
	if filter.Scope != "" && filter.Scope != ScopeProject {
		return RecordPage{}, fmt.Errorf("memorystore: as-of listing supports project scope only")
	}
	if projectKey := strings.TrimSpace(filter.ProjectKey); projectKey != "" &&
		projectKey != s.cfg.ProjectKey {
		return RecordPage{}, fmt.Errorf("memorystore: as-of listing is scoped to the current project")
	}
	db, err := s.ensureV2()
	if err != nil {
		return RecordPage{}, err
	}
	// A whole-second exclusive upper bound is safe for the UTC RFC3339 values
	// we persist, while exact fractional ordering remains a Go comparison.
	// Querying through the end of the as-of second avoids scanning later
	// history without relying on RFC3339Nano's non-sortable variable precision.
	query := `
SELECT memory_id, version, created_at, record_json
FROM memory_versions
WHERE tenant_id = ? AND project_key = ?`
	args := []interface{}{s.tenantID(), s.cfg.ProjectKey}
	if asOf.UTC().Year() < 9999 {
		upperBound := asOf.UTC().Truncate(time.Second).Add(time.Second).Format(time.RFC3339)
		query += " AND created_at < ?"
		args = append(args, upperBound)
	}
	query += " ORDER BY memory_id, version"
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return RecordPage{}, err
	}

	type historicalSnapshot struct {
		version int64
		record  Record
	}
	latestByID := make(map[string]historicalSnapshot)
	for rows.Next() {
		var memoryID, createdAt, body string
		var version int64
		if err := rows.Scan(&memoryID, &version, &createdAt, &body); err != nil {
			_ = rows.Close()
			return RecordPage{}, err
		}
		created := parseTime(createdAt)
		if created.IsZero() {
			_ = rows.Close()
			return RecordPage{}, fmt.Errorf("memorystore: invalid created_at in record version %s/%d", memoryID, version)
		}
		if created.After(asOf) {
			continue
		}
		if current, ok := latestByID[memoryID]; ok && current.version >= version {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(body), &record); err != nil {
			_ = rows.Close()
			return RecordPage{}, fmt.Errorf("memorystore: decode as-of record snapshot: %w", err)
		}
		if record.ID != memoryID || record.Revision != version {
			_ = rows.Close()
			return RecordPage{}, fmt.Errorf("memorystore: inconsistent as-of record snapshot %s/%d", memoryID, version)
		}
		latestByID[memoryID] = historicalSnapshot{version: version, record: record}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return RecordPage{}, err
	}
	if err := rows.Close(); err != nil {
		return RecordPage{}, err
	}

	records := make([]Record, 0, len(latestByID))
	for _, snapshot := range latestByID {
		record := snapshot.record
		// Defend project/scope isolation even if a malformed historical
		// snapshot was imported before the version writer enforced it.
		if record.TenantID != s.tenantID() || record.ProjectKey != s.cfg.ProjectKey || record.Scope != ScopeProject {
			continue
		}
		validFrom := record.ValidFrom
		if validFrom.IsZero() {
			validFrom = record.CreatedAt
		}
		if !validFrom.IsZero() && validFrom.After(asOf) {
			continue
		}
		if !record.ValidUntil.IsZero() && !record.ValidUntil.After(asOf) {
			continue
		}
		if !recordMatchesAsOfFilter(record, filter) {
			continue
		}
		records = append(records, record)
	}

	sort.Slice(records, func(i, j int) bool {
		if records[i].UpdatedAt.Equal(records[j].UpdatedAt) {
			return records[i].ID > records[j].ID
		}
		return records[i].UpdatedAt.After(records[j].UpdatedAt)
	})
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := 0
	if strings.TrimSpace(filter.Cursor) != "" {
		offset, err = strconv.Atoi(strings.TrimSpace(filter.Cursor))
		if err != nil || offset < 0 {
			return RecordPage{}, fmt.Errorf("memorystore: invalid as-of cursor")
		}
	}
	if offset > len(records) {
		offset = len(records)
	}
	end := offset + limit
	next := ""
	if end < len(records) {
		next = strconv.Itoa(end)
	} else {
		end = len(records)
	}
	items := append([]Record(nil), records[offset:end]...)
	return RecordPage{Items: items, NextCursor: next, Count: len(items)}, nil
}

func recordMatchesAsOfFilter(record Record, filter RecordFilter) bool {
	status := filter.Status
	if status == "" {
		status = StatusActive
	}
	if record.Status != status {
		return false
	}
	if !filter.IncludeArchived && record.Archived {
		return false
	}
	if filter.Kind != "" && record.Kind != filter.Kind {
		return false
	}
	if sourceKind := strings.ToLower(strings.TrimSpace(filter.SourceKind)); sourceKind != "" &&
		strings.ToLower(strings.TrimSpace(record.SourceKind)) != sourceKind {
		return false
	}
	if filter.SourceSessionID != "" && record.SourceSessionID != strings.TrimSpace(filter.SourceSessionID) {
		return false
	}
	if filter.SourceSessionGeneration != nil &&
		record.SourceSessionGeneration != *filter.SourceSessionGeneration {
		return false
	}
	if tag := strings.ToLower(strings.TrimSpace(filter.Tag)); tag != "" {
		found := false
		for _, candidate := range record.Tags {
			if strings.ToLower(candidate) == tag {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if query := strings.ToLower(strings.TrimSpace(filter.Query)); query != "" {
		if !strings.Contains(strings.ToLower(record.Text), query) &&
			!strings.Contains(strings.ToLower(strings.Join(record.Tags, " ")), query) {
			return false
		}
	}
	return true
}

func scanRecordVersion(row rowScanner) (RecordVersion, error) {
	var (
		version                                       RecordVersion
		scopeKind, scopeKey, recordJSON               sql.NullString
		sourceEventID, validFrom, validUntil, created sql.NullString
	)
	if err := row.Scan(
		&version.ID, &version.MemoryID, &version.Version, &version.ProjectKey,
		&scopeKind, &scopeKey, &recordJSON, &version.ChangedBy,
		&version.ChangeReason, &version.ChangeType, &sourceEventID,
		&validFrom, &validUntil, &created,
	); err != nil {
		return RecordVersion{}, err
	}
	version.ScopeKind = Scope(scopeKind.String)
	version.ScopeKey = scopeKey.String
	version.SourceEventID = sourceEventID.String
	version.ValidFrom = parseTime(validFrom.String)
	version.ValidUntil = parseTime(validUntil.String)
	version.CreatedAt = parseTime(created.String)
	if err := json.Unmarshal([]byte(recordJSON.String), &version.Record); err != nil {
		return RecordVersion{}, fmt.Errorf("memorystore: decode record version %s: %w", version.ID, err)
	}
	return version, nil
}

// RollbackRecord restores an explicit historical state as a new revision.
// Record identity, project/scope, provenance, and usage counters are retained
// from the current row; user-editable content and lifecycle state are restored
// from the selected snapshot.
func (s *Store) RollbackRecord(ctx context.Context, id string, opts RecordRollbackOptions) (Record, error) {
	id = strings.TrimSpace(id)
	opts.Reason = strings.TrimSpace(opts.Reason)
	if id == "" || opts.TargetVersion <= 0 || opts.ExpectedRevision <= 0 || opts.Reason == "" {
		return Record{}, fmt.Errorf("memorystore: rollback requires record id, target version, expected revision, and reason")
	}
	if utf8.RuneCountInString(opts.Reason) > 500 || memoryredact.DetectSecret(opts.Reason) != "" {
		return Record{}, fmt.Errorf("memorystore: rollback reason is too long or contains secret-shaped content")
	}
	opts.ChangedBy = strings.TrimSpace(opts.ChangedBy)
	if utf8.RuneCountInString(opts.ChangedBy) > 120 || strings.ContainsAny(opts.ChangedBy, "\r\n") || memoryredact.DetectSecret(opts.ChangedBy) != "" {
		return Record{}, fmt.Errorf("memorystore: invalid rollback actor")
	}
	db, err := s.ensureV2()
	if err != nil {
		return Record{}, err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateMaintenanceLeaseTx(ctx, tx, s.cfg.ProjectKey); err != nil {
		return Record{}, err
	}
	current, err := loadRecordTx(ctx, tx, id, s.tenantID(), s.cfg.ProjectKey)
	if err != nil {
		return Record{}, err
	}
	if current.Revision != opts.ExpectedRevision {
		return Record{}, fmt.Errorf("memorystore: record %s changed concurrently (expected revision %d, got %d)", id, opts.ExpectedRevision, current.Revision)
	}
	if err := checkRecordSessionPurgeFenceTx(ctx, tx, current); err != nil {
		return Record{}, err
	}
	var versionJSON string
	err = tx.QueryRowContext(ctx, `
SELECT record_json FROM memory_versions
WHERE memory_id = ? AND tenant_id = ? AND project_key = ? AND version = ?
`, id, s.tenantID(), s.cfg.ProjectKey, opts.TargetVersion).Scan(&versionJSON)
	if err != nil {
		return Record{}, err
	}
	var target Record
	if err := json.Unmarshal([]byte(versionJSON), &target); err != nil {
		return Record{}, fmt.Errorf("memorystore: decode rollback target: %w", err)
	}
	if target.ID != current.ID || target.ProjectKey != current.ProjectKey ||
		target.Scope != current.Scope || target.Kind != current.Kind {
		return Record{}, fmt.Errorf("memorystore: rollback target has incompatible record identity")
	}

	if current.Status == StatusSuperseded && target.Status == StatusActive {
		if !opts.RevertSupersede {
			return Record{}, fmt.Errorf("memorystore: reopening a superseded record requires revert_supersede")
		}
		if err := s.rollbackSupersedingSuccessorTx(ctx, tx, current, opts); err != nil {
			return Record{}, err
		}
	} else if target.Status == StatusSuperseded && target.SupersededBy != "" {
		var successorStatus string
		if err := tx.QueryRowContext(ctx, `
SELECT status FROM memory_entries WHERE id = ? AND tenant_id = ? AND project_key = ?
`, target.SupersededBy, s.tenantID(), s.cfg.ProjectKey).Scan(&successorStatus); err != nil {
			return Record{}, fmt.Errorf("memorystore: rollback target successor is unavailable: %w", err)
		}
		if successorStatus != string(StatusActive) {
			return Record{}, fmt.Errorf("memorystore: rollback target successor is no longer active")
		}
	}

	restored := target
	restored.ID = current.ID
	restored.Scope = current.Scope
	restored.ScopeKind = current.ScopeKind
	restored.ScopeKey = current.ScopeKey
	restored.TenantID = current.TenantID
	restored.OwnerID = current.OwnerID
	restored.AgentID = current.AgentID
	restored.TaskID = current.TaskID
	restored.ProjectKey = current.ProjectKey
	restored.Kind = current.Kind
	restored.CreatedAt = current.CreatedAt
	restored.SourceKind = current.SourceKind
	restored.SourceSessionID = current.SourceSessionID
	restored.SourceSessionGeneration = current.SourceSessionGeneration
	restored.SourceTurnID = current.SourceTurnID
	restored.SourceRefs = append([]string(nil), current.SourceRefs...)
	restored.SourceType = current.SourceType
	restored.SourceTrust = current.SourceTrust
	restored.SourceHash = current.SourceHash
	restored.SourceRevision = current.SourceRevision
	restored.SourceUpdatedAt = current.SourceUpdatedAt
	restored.SourceObservedAt = current.SourceObservedAt
	restored.SourceMessageIDs = append([]string(nil), current.SourceMessageIDs...)
	restored.SourceQuote = current.SourceQuote
	restored.LastUsedAt = current.LastUsedAt
	restored.UseCount = current.UseCount
	restored.Revision = current.Revision + 1
	restored.UpdatedAt = time.Now().UTC()
	if restored.Status == StatusActive {
		semanticChange := restored.Text != current.Text ||
			restored.Title != current.Title ||
			restored.Summary != current.Summary ||
			restored.CanonicalID != current.CanonicalID
		if current.Status != StatusActive || semanticChange {
			// Rollback is a new fact transition, not time travel. Reopening an
			// inactive claim or restoring different active content begins a new
			// valid interval at the rollback commit time.
			restored.ValidFrom = restored.UpdatedAt
			restored.ValidUntil = time.Time{}
		} else {
			// A non-semantic rollback (for example tags/pinning) must preserve
			// the currently open fact interval rather than an older snapshot's
			// interval metadata.
			restored.ValidFrom = current.ValidFrom
			restored.ValidUntil = current.ValidUntil
		}
		restored.DeletedAt = time.Time{}
	} else {
		if current.Status == StatusActive || restored.ValidUntil.IsZero() {
			restored.ValidUntil = restored.UpdatedAt
		}
		if restored.Status == StatusDeleted && current.Status != StatusDeleted {
			restored.DeletedAt = restored.UpdatedAt
		}
	}
	restored.NormalizedText = normalizeMemoryText(restored.Text)
	restored.ContentHash = memoryRecordContentHash(restored)
	restored.Sensitivity = normalizeSensitivity(restored.Sensitivity)
	if err := validateRecordMetadata(&restored); err != nil {
		return Record{}, err
	}
	if rule := memoryredact.DetectSecret(restored.Text); rule != "" {
		return Record{}, fmt.Errorf("memorystore: rollback target contains secret-shaped content (%s)", rule)
	}

	tagsJSON, _ := json.Marshal(restored.Tags)
	metadataJSON, _ := json.Marshal(restored.Metadata)
	now := restored.UpdatedAt.Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `
UPDATE memory_entries SET
    status = ?, text = ?, normalized_text = ?, tags_json = ?, confidence = ?,
    sensitivity = ?, pinned = ?, retention_class = ?, content_hash = ?,
    supersedes_id = ?, revision = ?, updated_at = ?, valid_from = ?, valid_until = ?,
    freshness_status = ?, last_verified_at = ?, revalidate_after = ?, stale_reason = ?,
    deleted_at = ?, metadata_json = ?, title = ?, summary = ?, canonical_id = ?,
    superseded_by = ?, archived = ?
WHERE id = ? AND tenant_id = ? AND project_key = ? AND revision = ?
`, string(restored.Status), restored.Text, restored.NormalizedText, string(tagsJSON),
		restored.Confidence, restored.Sensitivity, boolInt(restored.Pinned),
		restored.RetentionClass, restored.ContentHash, nullableString(restored.SupersedesID),
		restored.Revision, now, nullableTime(restored.ValidFrom), nullableTime(restored.ValidUntil),
		string(restored.Freshness), nullableTime(restored.LastVerifiedAt),
		nullableTime(restored.RevalidateAfter), nullableString(restored.StaleReason),
		nullableTime(restored.DeletedAt), string(metadataJSON), restored.Title,
		restored.Summary, restored.CanonicalID, nullableString(restored.SupersededBy),
		boolInt(restored.Archived), current.ID, s.tenantID(), s.cfg.ProjectKey, current.Revision)
	if err != nil {
		return Record{}, err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return Record{}, fmt.Errorf("memorystore: record %s changed concurrently", id)
	}
	if restored.Status == StatusDeleted {
		if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO memory_suppressions (
    id, scope, project_key, content_hash, reason, source_memory_id, created_at, metadata_json,
    tenant_id, owner_id, agent_id, task_id, scope_kind, scope_key
) VALUES (?, ?, ?, ?, 'deleted', ?, ?, '{}', ?, ?, ?, ?, ?, ?)
`, "sup_"+uuid.NewString(), string(restored.Scope), restored.ProjectKey,
			restored.ContentHash, restored.ID, now, restored.TenantID, restored.OwnerID,
			restored.AgentID, restored.TaskID, string(restored.ScopeKind), restored.ScopeKey); err != nil {
			return Record{}, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
DELETE FROM memory_suppressions
WHERE source_memory_id = ? AND tenant_id = ? AND project_key = ? AND reason = 'deleted'
`, restored.ID, s.tenantID(), restored.ProjectKey); err != nil {
			return Record{}, err
		}
	}
	if restored.Status == StatusActive && current.Status == StatusSuperseded &&
		strings.TrimSpace(current.SupersededBy) != "" {
		// Explicitly restoring an active historical claim also reverses the
		// correction suppression created when that claim was superseded.
		if _, err := tx.ExecContext(ctx, `
DELETE FROM memory_suppressions
WHERE source_memory_id = ? AND tenant_id = ? AND project_key = ? AND reason = 'corrected'
`, restored.ID, s.tenantID(), restored.ProjectKey); err != nil {
			return Record{}, err
		}
	}
	actor := firstNonEmpty(opts.ChangedBy, "user")
	eventID, err := insertEventIDTx(ctx, tx, restored.ProjectKey, restored.ID,
		"rolled_back", actor, restored.SourceSessionID, restored.Revision,
		map[string]interface{}{"reason": opts.Reason, "target_version": opts.TargetVersion})
	if err != nil {
		return Record{}, err
	}
	if err := s.appendCurrentRecordVersionTx(ctx, tx, restored.ID, restored.ProjectKey,
		actor, opts.Reason, "rollback", eventID); err != nil {
		return Record{}, err
	}
	if err := s.markSummariesStaleForRecordTx(ctx, tx, restored.ID, "record_rolled_back"); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return restored, nil
}

func (s *Store) rollbackSupersedingSuccessorTx(ctx context.Context, tx *sql.Tx, record Record, opts RecordRollbackOptions) error {
	if strings.TrimSpace(record.SupersededBy) == "" {
		return fmt.Errorf("memorystore: superseded record has no successor to revert")
	}
	successor, err := loadRecordTx(ctx, tx, record.SupersededBy, s.tenantID(), record.ProjectKey)
	if err != nil {
		return fmt.Errorf("memorystore: load superseding successor: %w", err)
	}
	if successor.Status != StatusActive || successor.SupersedesID != record.ID {
		return fmt.Errorf("memorystore: superseding successor changed; rollback it separately first")
	}
	linkRevision, err := supersedingLinkRevisionTx(ctx, tx, successor.ID, s.tenantID(), record.ProjectKey, record.ID)
	if err != nil {
		return err
	}
	if linkRevision == 0 || successor.Revision != linkRevision {
		return fmt.Errorf("memorystore: superseding successor changed; rollback it separately first")
	}
	if err := checkRecordSessionPurgeFenceTx(ctx, tx, successor); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `
UPDATE memory_entries
SET status = 'superseded', superseded_by = NULL,
    supersedes_id = CASE WHEN supersedes_id = ? THEN NULL ELSE supersedes_id END,
    valid_until = ?,
    revision = revision + 1, updated_at = ?
WHERE id = ? AND tenant_id = ? AND project_key = ? AND revision = ? AND status = 'active'
`, record.ID, now, now, successor.ID, s.tenantID(), record.ProjectKey, successor.Revision)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("memorystore: successor %s changed concurrently", successor.ID)
	}
	eventID, err := insertEventIDTx(ctx, tx, successor.ProjectKey, successor.ID,
		"rollback_successor_invalidated", firstNonEmpty(opts.ChangedBy, "user"), successor.SourceSessionID,
		successor.Revision+1, map[string]interface{}{
			"reason": opts.Reason, "reverted_record_id": record.ID,
		})
	if err != nil {
		return err
	}
	if err := s.appendCurrentRecordVersionTx(ctx, tx, successor.ID, successor.ProjectKey,
		firstNonEmpty(opts.ChangedBy, "user"), opts.Reason, "rollback", eventID); err != nil {
		return err
	}
	if err := s.markSummariesStaleForRecordTx(ctx, tx, successor.ID, "rollback_successor_invalidated"); err != nil {
		return err
	}
	return nil
}

func supersedingLinkRevisionTx(
	ctx context.Context,
	tx *sql.Tx,
	successorID, tenantID, projectKey, supersedesID string,
) (int64, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT version, record_json
FROM memory_versions
WHERE memory_id = ? AND tenant_id = ? AND project_key = ?
ORDER BY version ASC
`, successorID, tenantID, projectKey)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var linkRevision int64
	for rows.Next() {
		var version int64
		var body string
		if err := rows.Scan(&version, &body); err != nil {
			return 0, err
		}
		var snapshot Record
		if err := json.Unmarshal([]byte(body), &snapshot); err != nil {
			return 0, fmt.Errorf("memorystore: decode successor version %s/%d: %w", successorID, version, err)
		}
		if linkRevision == 0 && snapshot.SupersedesID == supersedesID {
			linkRevision = version
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return linkRevision, nil
}
