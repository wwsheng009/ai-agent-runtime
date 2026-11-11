package memorystore

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// ImportResult is the bounded, secret-free summary returned by an admin import.
type ImportResult struct {
	Scanned   int      `json:"scanned"`
	Imported  int      `json:"imported"`
	Summaries int      `json:"summaries"`
	Skipped   int      `json:"skipped"`
	Corrupt   int      `json:"corrupt"`
	DryRun    bool     `json:"dry_run"`
	Errors    []string `json:"errors,omitempty"`
}

type importEnvelope struct {
	Type    string          `json:"type"`
	Record  json.RawMessage `json:"record"`
	Summary json.RawMessage `json:"summary"`
}

type parsedImport struct {
	records   []Record
	summaries []Summary
}

// ImportJSONL validates and imports the export format produced by
// ExportJSONL. Records are inserted in one transaction; summary lines are
// compiled as a fresh active revision only after their source records exist.
// Imported project keys are remapped to the destination project (backup
// restore across checkout paths), duplicates/suppressed content is skipped,
// and dry-run performs the same validation then rolls the transaction back.
func (s *Store) ImportJSONL(ctx context.Context, body []byte, dryRun bool) (ImportResult, error) {
	result := ImportResult{DryRun: dryRun}
	parsed, result, err := parseImportJSONL(body, result)
	if err != nil {
		return result, err
	}
	db, err := s.ensureV2()
	if err != nil {
		return result, err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	for _, raw := range parsed.records {
		record, normalizeErr := s.normalizeImportedRecord(raw, now)
		if normalizeErr != nil {
			result.Skipped++
			addImportError(&result, normalizeErr)
			continue
		}
		var existing int
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_entries WHERE id = ? AND project_key = ?
`, record.ID, s.cfg.ProjectKey).Scan(&existing); err != nil {
			return result, err
		}
		if existing > 0 {
			result.Skipped++
			continue
		}
		if err := s.insertRecordSQLTx(ctx, tx, record, "admin", record.SourceSessionID); err != nil {
			if isDuplicateMemoryError(err) || isSuppressedMemoryError(err) || isSecretMemoryError(err) {
				result.Skipped++
				if isSecretMemoryError(err) {
					addImportError(&result, err)
				}
				continue
			}
			return result, err
		}
		if record.Status == StatusDeleted {
			if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO memory_suppressions (
    id, scope, project_key, content_hash, reason, source_memory_id, created_at, metadata_json
) VALUES (?, ?, ?, ?, 'deleted', ?, ?, '{}')
`, "sup_"+uuid.NewString(), string(record.Scope), record.ProjectKey, record.ContentHash,
				record.ID, now.Format(time.RFC3339Nano)); err != nil {
				return result, err
			}
		}
		if err := insertEventTx(ctx, tx, record.ID, "imported", "admin", record.SourceSessionID, record.Revision, map[string]interface{}{
			"source_kind": record.SourceKind,
		}); err != nil {
			return result, err
		}
		result.Imported++
	}
	if dryRun {
		return result, nil
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	for _, summary := range parsed.summaries {
		imported, err := s.importSummary(ctx, summary)
		if err != nil {
			result.Skipped++
			addImportError(&result, err)
			continue
		}
		if !imported {
			result.Skipped++
			continue
		}
		result.Summaries++
	}
	return result, nil
}

func parseImportJSONL(body []byte, result ImportResult) (parsedImport, ImportResult, error) {
	var parsed parsedImport
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		result.Scanned++
		var envelope importEnvelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			result.Corrupt++
			addImportError(&result, fmt.Errorf("line %d: invalid JSON", lineNo))
			continue
		}
		switch strings.ToLower(strings.TrimSpace(envelope.Type)) {
		case "record":
			var record Record
			if err := json.Unmarshal(envelope.Record, &record); err != nil {
				result.Corrupt++
				addImportError(&result, fmt.Errorf("line %d: invalid record", lineNo))
				continue
			}
			parsed.records = append(parsed.records, record)
		case "summary":
			var summary Summary
			if err := json.Unmarshal(envelope.Summary, &summary); err != nil {
				result.Corrupt++
				addImportError(&result, fmt.Errorf("line %d: invalid summary", lineNo))
				continue
			}
			parsed.summaries = append(parsed.summaries, summary)
		case "":
			// Accept a bare record line for hand-authored recovery files.
			var record Record
			if err := json.Unmarshal([]byte(line), &record); err != nil || strings.TrimSpace(record.Text) == "" {
				result.Corrupt++
				addImportError(&result, fmt.Errorf("line %d: missing type/record", lineNo))
				continue
			}
			parsed.records = append(parsed.records, record)
		default:
			result.Skipped++
			addImportError(&result, fmt.Errorf("line %d: unsupported type %q", lineNo, envelope.Type))
		}
	}
	if err := scanner.Err(); err != nil {
		return parsed, result, fmt.Errorf("memorystore: read import body: %w", err)
	}
	return parsed, result, nil
}

func (s *Store) normalizeImportedRecord(record Record, now time.Time) (Record, error) {
	sourceProjectKey := strings.TrimSpace(record.ProjectKey)
	if sourceProjectKey != "" && sourceProjectKey != s.cfg.ProjectKey {
		if record.Metadata == nil {
			record.Metadata = map[string]interface{}{}
		}
		record.Metadata["imported_from_project_key"] = sourceProjectKey
	}
	record.ProjectKey = s.cfg.ProjectKey
	if record.Scope == "" {
		record.Scope = ScopeProject
	}
	if record.Scope != ScopeProject {
		return Record{}, fmt.Errorf("memorystore: import only accepts project scope")
	}
	record.Text = strings.TrimSpace(record.Text)
	if record.Text == "" {
		return Record{}, fmt.Errorf("memorystore: import record text is required")
	}
	record.Kind = strings.TrimSpace(record.Kind)
	if record.Kind == "" {
		return Record{}, fmt.Errorf("memorystore: import record kind is required")
	}
	if record.Status == "" {
		record.Status = StatusActive
	}
	switch record.Status {
	case StatusCandidate, StatusActive, StatusSuperseded, StatusDeleted, StatusExpired:
	default:
		return Record{}, fmt.Errorf("memorystore: invalid import record status %q", record.Status)
	}
	if record.Freshness == "" {
		record.Freshness = FreshnessFresh
	}
	switch record.Freshness {
	case FreshnessFresh, FreshnessStale, FreshnessUnverified:
	default:
		return Record{}, fmt.Errorf("memorystore: invalid import freshness %q", record.Freshness)
	}
	if record.Confidence < 0 || record.Confidence > 1 {
		return Record{}, fmt.Errorf("memorystore: import confidence must be between 0 and 1")
	}
	if record.ID == "" {
		record.ID = "mem_" + uuid.NewString()
	}
	record.NormalizedText = normalizeMemoryText(record.Text)
	record.ContentHash = memoryContentHash(string(record.Scope), record.ProjectKey, record.Kind, record.Text)
	record.Tags = normalizeTags(record.Tags)
	record.SourceKind = firstNonEmpty(record.SourceKind, "import")
	record.Sensitivity = normalizeSensitivity(record.Sensitivity)
	record.RetentionClass = firstNonEmpty(record.RetentionClass, "standard")
	if len(record.SourceRefs) == 0 {
		record.SourceRefs = []string{"import:" + record.ID}
	}
	if record.Revision <= 0 {
		record.Revision = 1
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.CreatedAt
	}
	if record.SourceObservedAt.IsZero() {
		record.SourceObservedAt = now
	}
	if record.SourceUpdatedAt.IsZero() {
		record.SourceUpdatedAt = record.UpdatedAt
	}
	if record.Status == StatusDeleted {
		if record.DeletedAt.IsZero() {
			record.DeletedAt = now
		}
	} else {
		record.DeletedAt = time.Time{}
	}
	if record.Metadata == nil {
		record.Metadata = map[string]interface{}{}
	}
	if _, exists := record.Metadata["imported_at"]; !exists {
		record.Metadata["imported_at"] = now.Format(time.RFC3339Nano)
	}
	return record, nil
}

func (s *Store) importSummary(ctx context.Context, summary Summary) (bool, error) {
	if summary.Scope != "" && summary.Scope != ScopeProject {
		return false, fmt.Errorf("memorystore: import only accepts project-summary scope")
	}
	if summary.Status != "" && summary.Status != "active" {
		return false, fmt.Errorf("memorystore: only active summary revisions are imported")
	}
	if summary.Freshness != "" && summary.Freshness != FreshnessFresh {
		return false, fmt.Errorf("memorystore: only fresh summary revisions are imported")
	}
	if len(summary.RecordIDs) == 0 || strings.TrimSpace(summary.Text) == "" {
		return false, fmt.Errorf("memorystore: import summary requires text and record_ids")
	}
	db, err := s.ensureV2()
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(summary.Text)))
	var existing int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_summaries
WHERE project_key = ? AND summary_kind = 'long_term' AND status = 'active'
  AND freshness_status = 'fresh' AND content_hash = ?
`, s.cfg.ProjectKey, hex.EncodeToString(sum[:])).Scan(&existing); err != nil {
		return false, err
	}
	if existing > 0 {
		return false, nil
	}
	metadata := summary.Metadata
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	if summary.ProjectKey != "" && summary.ProjectKey != s.cfg.ProjectKey {
		metadata["imported_from_project_key"] = summary.ProjectKey
	}
	metadata["imported_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.CompileSummary(ctx, Summary{
		Text: summary.Text, Model: firstNonEmpty(summary.Model, "import"), Metadata: metadata,
	}, summary.RecordIDs)
	if err != nil {
		return false, err
	}
	return true, nil
}

func addImportError(result *ImportResult, err error) {
	if result == nil || err == nil || len(result.Errors) >= 20 {
		return
	}
	result.Errors = append(result.Errors, err.Error())
}
