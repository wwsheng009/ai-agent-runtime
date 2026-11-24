package memorystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

const (
	maxExtractionDiagnosticReasonRunes  = 256
	maxExtractionDiagnosticSummaryRunes = 1200
	maxExtractionDiagnosticsPerWrite    = 100
)

// ListRecordSources returns project-scoped provenance without exposing a
// legacy quote that was accidentally equal to the extracted memory text.
func (s *Store) ListRecordSources(ctx context.Context, memoryID string) ([]RecordSource, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	memoryID = strings.TrimSpace(memoryID)
	if memoryID == "" {
		return nil, fmt.Errorf("memorystore: memory id is required")
	}
	rows, err := db.QueryContext(ctx, `
SELECT src.id, src.memory_id, src.source_kind, src.source_id,
       COALESCE(src.source_type, ''), COALESCE(src.source_trust, ''),
       COALESCE(src.session_id, ''), COALESCE(src.source_session_generation, 0),
       COALESCE(src.turn_id, ''), COALESCE(src.source_window, ''),
       COALESCE(src.message_ids_json, '[]'), COALESCE(src.source_hash, ''),
       COALESCE(src.source_revision, 0), COALESCE(src.source_updated_at, ''),
       COALESCE(src.source_observed_at, ''),
       CASE
         WHEN TRIM(COALESCE(src.quote, '')) = TRIM(e.text) THEN ''
         ELSE COALESCE(src.quote, '')
       END,
       src.created_at
FROM memory_sources src
JOIN memory_entries e ON e.id = src.memory_id
WHERE src.memory_id = ? AND e.tenant_id = ? AND e.project_key = ?
ORDER BY src.created_at DESC, src.id DESC
`, memoryID, s.tenantID(), s.cfg.ProjectKey)
	if err != nil {
		return nil, fmt.Errorf("memorystore: list record sources: %w", err)
	}
	defer rows.Close()

	out := make([]RecordSource, 0, 2)
	for rows.Next() {
		var (
			source                          RecordSource
			messageIDsJSON                  string
			sourceUpdatedAt, sourceObserved string
			quote, createdAt                string
		)
		if err := rows.Scan(
			&source.ID, &source.MemoryID, &source.SourceKind, &source.SourceID,
			&source.SourceType, &source.SourceTrust, &source.SessionID,
			&source.SessionGeneration, &source.TurnID, &source.SourceWindow,
			&messageIDsJSON, &source.SourceHash, &source.SourceRevision,
			&sourceUpdatedAt, &sourceObserved, &quote, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("memorystore: scan record source: %w", err)
		}
		_ = json.Unmarshal([]byte(messageIDsJSON), &source.MessageIDs)
		source.SourceUpdatedAt = parseTime(sourceUpdatedAt)
		source.SourceObservedAt = parseTime(sourceObserved)
		source.CreatedAt = parseTime(createdAt)
		source.Quote, _ = memoryredact.Redact(strings.TrimSpace(quote))
		out = append(out, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memorystore: list record sources rows: %w", err)
	}
	return out, nil
}

// RecordExtractionDiagnostics persists bounded ignored-candidate reasons after
// an extraction attempt. It intentionally accepts only operator-safe strings;
// provider diagnostics and transcript text are not part of this API.
func (s *Store) RecordExtractionDiagnostics(ctx context.Context, diagnostics []ExtractionDiagnostic) error {
	if len(diagnostics) == 0 {
		return nil
	}
	if len(diagnostics) > maxExtractionDiagnosticsPerWrite {
		diagnostics = diagnostics[:maxExtractionDiagnosticsPerWrite]
	}
	db, err := s.ensureV2()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		return fmt.Errorf("memorystore: begin extraction diagnostics: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertExtractionDiagnosticsTx(ctx, tx, s.cfg.ProjectKey, diagnostics); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("memorystore: commit extraction diagnostics: %w", err)
	}
	return nil
}

func insertExtractionDiagnosticsTx(
	ctx context.Context,
	tx *sql.Tx,
	projectKey string,
	diagnostics []ExtractionDiagnostic,
) error {
	projectKey = strings.TrimSpace(projectKey)
	if len(diagnostics) > maxExtractionDiagnosticsPerWrite {
		diagnostics = diagnostics[:maxExtractionDiagnosticsPerWrite]
	}
	for _, diagnostic := range diagnostics {
		jobID := boundedEvidenceIdentifier(diagnostic.JobID)
		reason := sanitizeDiagnosticReason(diagnostic.Reason)
		if jobID == "" || reason == "" {
			continue
		}
		if requestedProject := strings.TrimSpace(diagnostic.ProjectKey); requestedProject != "" &&
			requestedProject != projectKey {
			return fmt.Errorf("memorystore: extraction diagnostic project scope mismatch")
		}
		sourceIDs := make([]string, 0, len(diagnostic.SourceIDs))
		for _, sourceID := range uniqueStrings(diagnostic.SourceIDs) {
			if sourceID = boundedEvidenceIdentifier(sourceID); sourceID != "" {
				sourceIDs = append(sourceIDs, sourceID)
				if len(sourceIDs) >= 32 {
					break
				}
			}
		}
		sourceIDsJSON, _ := json.Marshal(sourceIDs)
		createdAt := diagnostic.CreatedAt.UTC()
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO memory_extraction_diagnostics (
    id, project_key, job_id, session_id, session_generation, turn_id,
    reason, summary, source_ids_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, "diag_"+uuid.NewString(), projectKey, jobID,
			boundedEvidenceIdentifier(diagnostic.SessionID), diagnostic.SessionGeneration,
			boundedEvidenceIdentifier(diagnostic.TurnID), reason,
			sanitizeEvidenceText(diagnostic.Summary, maxExtractionDiagnosticSummaryRunes),
			string(sourceIDsJSON), createdAt.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("memorystore: insert extraction diagnostic: %w", err)
		}
	}
	return nil
}

// ListExtractionDiagnostics reads only the current project's bounded
// extraction diagnostics. It is intended for status/operator APIs.
func (s *Store) ListExtractionDiagnostics(ctx context.Context, jobID string, limit int) ([]ExtractionDiagnostic, error) {
	db, err := s.ensureV2()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > maxExtractionDiagnosticsPerWrite {
		limit = maxExtractionDiagnosticsPerWrite
	}
	args := []interface{}{s.tenantID(), s.cfg.ProjectKey, s.cfg.ProjectKey}
	query := `
SELECT d.id, d.project_key, d.job_id, d.session_id, d.session_generation, d.turn_id,
       d.reason, d.summary, d.source_ids_json, d.created_at
FROM memory_extraction_diagnostics d
JOIN memory_jobs j
  ON j.id = d.job_id AND j.tenant_id = ? AND j.project_key = ?
WHERE d.project_key = ?`
	if jobID = strings.TrimSpace(jobID); jobID != "" {
		query += " AND d.job_id = ?"
		args = append(args, jobID)
	}
	query += " ORDER BY d.created_at DESC, d.id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("memorystore: list extraction diagnostics: %w", err)
	}
	defer rows.Close()

	out := make([]ExtractionDiagnostic, 0, limit)
	for rows.Next() {
		var (
			diagnostic  ExtractionDiagnostic
			sourceJSON  string
			createdAt   string
			summaryText string
		)
		if err := rows.Scan(
			&diagnostic.ID, &diagnostic.ProjectKey, &diagnostic.JobID,
			&diagnostic.SessionID, &diagnostic.SessionGeneration,
			&diagnostic.TurnID, &diagnostic.Reason, &summaryText,
			&sourceJSON, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("memorystore: scan extraction diagnostic: %w", err)
		}
		diagnostic.Summary, _ = memoryredact.Redact(summaryText)
		_ = json.Unmarshal([]byte(sourceJSON), &diagnostic.SourceIDs)
		diagnostic.CreatedAt = parseTime(createdAt)
		out = append(out, diagnostic)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memorystore: list extraction diagnostics rows: %w", err)
	}
	return out, nil
}

func sanitizeEvidenceText(text string, maxRunes int) string {
	text, _ = memoryredact.Redact(strings.TrimSpace(text))
	text = strings.Join(strings.Fields(text), " ")
	if maxRunes > 0 && utf8.RuneCountInString(text) > maxRunes {
		text = string([]rune(text)[:maxRunes])
	}
	return text
}

func sanitizeDiagnosticReason(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	var b strings.Builder
	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-', r == ':':
			b.WriteRune(r)
		case r == ' ' || r == '\t' || r == '\n':
			if b.Len() > 0 && b.Len() < maxExtractionDiagnosticReasonRunes {
				b.WriteByte('_')
			}
		}
		if b.Len() >= maxExtractionDiagnosticReasonRunes {
			break
		}
	}
	return strings.Trim(b.String(), "_:-")
}

func boundedEvidenceIdentifier(text string) string {
	text = strings.TrimSpace(text)
	if len(text) == 0 || len(text) > 256 {
		return ""
	}
	return text
}
