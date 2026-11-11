package memorystore

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

type migrationStats struct {
	Scanned  int
	Imported int
	Skipped  int
	Corrupt  int
}

func (s *Store) migrateNotesV2(ctx context.Context, db *sql.DB) (migrationStats, error) {
	var stats migrationStats
	file, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return stats, nil
		}
		return stats, fmt.Errorf("memorystore: open notes jsonl: %w", err)
	}
	defer file.Close()
	info, _ := file.Stat()
	runID := "mig_" + uuid.NewString()
	started := nowText()
	var sourceSize int64
	sourceMTime := ""
	if info != nil {
		sourceSize = info.Size()
		sourceMTime = info.ModTime().UTC().Format(time.RFC3339Nano)
	}
	var priorSucceeded int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM memory_migration_runs
WHERE source_path = ? AND source_size = ? AND source_mtime = ? AND status = 'succeeded'
`, s.path, sourceSize, sourceMTime).Scan(&priorSucceeded); err == nil && priorSucceeded > 0 {
		// The source file is unchanged and already migrated. This also keeps a
		// project reset from being undone by re-importing legacy notes on the
		// next store open (notes.jsonl itself is intentionally preserved).
		return stats, nil
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO memory_migration_runs (
    id, source_path, source_size, source_mtime, status, started_at
) VALUES (?, ?, ?, ?, 'running', ?)
`, runID, s.path, sourceSize, sourceMTime, started); err != nil {
		// Diagnostic table failure must not block migration semantics.
		runID = ""
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		stats.Scanned++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			stats.Skipped++
			continue
		}
		var note Note
		if err := json.Unmarshal([]byte(line), &note); err != nil {
			stats.Corrupt++
			continue
		}
		text := strings.TrimSpace(note.Text)
		if text == "" {
			stats.Corrupt++
			continue
		}
		createdAt := note.CreatedAt.UTC()
		if createdAt.IsZero() {
			createdAt = time.Unix(int64(lineNo), 0).UTC()
		}
		id := strings.TrimSpace(note.ID)
		if id == "" {
			id = newNoteID(note)
		}
		pinned := true
		record := Record{
			ID:              id,
			Scope:           ScopeProject,
			ProjectKey:      s.cfg.ProjectKey,
			Kind:            "reference",
			Status:          StatusActive,
			Text:            text,
			NormalizedText:  normalizeMemoryText(text),
			Tags:            normalizeTags(note.Tags),
			Confidence:      1.0,
			Pinned:          pinned,
			RetentionClass:  "permanent",
			SourceKind:      "manual",
			SourceSessionID: strings.TrimSpace(note.SessionID),
			SourceRefs:      []string{"manual:" + id},
			ContentHash:     memoryContentHash(string(ScopeProject), s.cfg.ProjectKey, "reference", text),
			Revision:        1,
			CreatedAt:       createdAt,
			UpdatedAt:       createdAt,
			Freshness:       FreshnessFresh,
			LastVerifiedAt:  createdAt,
			Metadata: map[string]interface{}{
				"legacy_source":    strings.TrimSpace(note.Source),
				"legacy_line":      lineNo,
				"migration_run_id": runID,
			},
		}
		if err := s.insertRecordTx(ctx, db, record, "system", note.SessionID); err != nil {
			if isSuppressedMemoryError(err) {
				stats.Skipped++
				continue
			}
			if isSecretMemoryError(err) {
				stats.Corrupt++
				continue
			}
			if isDuplicateMemoryError(err) {
				existingHash, exists, lookupErr := lookupMemoryContentHash(ctx, db, record.ID, s.cfg.ProjectKey)
				if lookupErr != nil {
					return stats, lookupErr
				}
				if !exists || existingHash == record.ContentHash {
					stats.Skipped++
					continue
				}
				// A legacy id collision with different content is recoverable:
				// retry with a new id and keep the original in metadata.
				record.ID = "mem_" + uuid.NewString()
				record.Metadata["legacy_id"] = id
				record.SourceRefs = []string{"manual:" + record.ID}
				if retryErr := s.insertRecordTx(ctx, db, record, "system", note.SessionID); retryErr != nil {
					if isDuplicateMemoryError(retryErr) || isSuppressedMemoryError(retryErr) {
						stats.Skipped++
						continue
					}
					return stats, retryErr
				}
				stats.Imported++
				continue
			}
			return stats, err
		}
		stats.Imported++
	}
	if err := scanner.Err(); err != nil {
		if runID != "" {
			_, _ = db.ExecContext(ctx, `UPDATE memory_migration_runs SET status = 'failed', error = ?, finished_at = ? WHERE id = ?`, err.Error(), nowText(), runID)
		}
		return stats, fmt.Errorf("memorystore: scan notes jsonl: %w", err)
	}
	if runID != "" {
		_, _ = db.ExecContext(ctx, `
UPDATE memory_migration_runs
SET scanned = ?, imported = ?, skipped = ?, corrupt = ?, status = 'succeeded', finished_at = ?
WHERE id = ?
`, stats.Scanned, stats.Imported, stats.Skipped, stats.Corrupt, nowText(), runID)
	}
	return stats, nil
}

func isDuplicateMemoryError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate memory")
}

func isSuppressedMemoryError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "content is suppressed")
}

func isSecretMemoryError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "secret-shaped content")
}

func lookupMemoryContentHash(ctx context.Context, db *sql.DB, id, projectKey string) (string, bool, error) {
	var hash string
	err := db.QueryRowContext(ctx, `
SELECT content_hash FROM memory_entries WHERE id = ? AND project_key = ?
`, id, projectKey).Scan(&hash)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return hash, true, nil
}

func ensureMemoryParent(path string) error {
	parent := filepath.Dir(path)
	if parent == "" || parent == "." {
		return nil
	}
	return os.MkdirAll(parent, 0o755)
}
