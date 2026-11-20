package memorystore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// checkJobPurgeFenceByIDTx prevents legacy jobs that cannot prove their
// session incarnation from being claimed or completed after a purge. Jobs with
// structured generation identity use the normal generation-aware fence.
func checkJobPurgeFenceByIDTx(ctx context.Context, tx *sql.Tx, id, expectedProject string) error {
	query := `
SELECT kind, job_key, project_key, COALESCE(session_id, ''), session_generation,
       COALESCE(capture_receipt_id, '')
FROM memory_jobs WHERE id = ?`
	args := []interface{}{id}
	if strings.TrimSpace(expectedProject) != "" {
		query += " AND project_key = ?"
		args = append(args, strings.TrimSpace(expectedProject))
	}

	var kind, jobKey, projectKey, sessionID, receiptID string
	var generation int64
	if err := tx.QueryRowContext(ctx, query, args...).Scan(
		&kind, &jobKey, &projectKey, &sessionID, &generation, &receiptID,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("memorystore: read job purge identity: %w", err)
	}
	return checkJobPurgeFenceIdentityTx(
		ctx, tx, projectKey, kind, jobKey, sessionID, generation, receiptID,
	)
}

func checkJobPurgeFenceIdentityTx(
	ctx context.Context,
	tx *sql.Tx,
	projectKey, kind, jobKey, sessionID string,
	generation int64,
	receiptID string,
) error {
	sessionID = strings.TrimSpace(sessionID)
	kind = strings.TrimSpace(kind)
	if kind == "phase1_extract/v2" {
		if sessionID == "" && generation == 0 && strings.TrimSpace(receiptID) == "" {
			sessionID, sessionScoped, malformed := legacyJobSessionIdentity(projectKey, jobKey)
			if malformed {
				return fmt.Errorf("%w: malformed v2 session job key", ErrSessionPurgeGenerationMismatch)
			}
			if !sessionScoped {
				return nil
			}
			return checkLegacySessionPurgeFenceTx(ctx, tx, projectKey, sessionID)
		}
		// Older receipt-backed rows may carry a session and receipt but predate
		// the generation column being populated.  They cannot use the strict
		// incarnation fence, but they must still be blocked once that session
		// has any purge tombstone.
		if sessionID != "" && generation <= 0 && strings.TrimSpace(receiptID) != "" {
			return checkLegacySessionPurgeFenceTx(ctx, tx, projectKey, sessionID)
		}
		if sessionID == "" || generation <= 0 || strings.TrimSpace(receiptID) == "" {
			return fmt.Errorf("%w: v2 job lacks a complete session identity", ErrSessionPurgeGenerationMismatch)
		}
		return checkSessionPurgeGenerationTx(ctx, tx, projectKey, sessionID, generation)
	}

	if sessionID != "" {
		if generation > 0 {
			return checkSessionPurgeGenerationTx(ctx, tx, projectKey, sessionID, generation)
		}
		return checkLegacySessionPurgeFenceTx(ctx, tx, projectKey, sessionID)
	}
	if generation != 0 || strings.TrimSpace(receiptID) != "" {
		return fmt.Errorf("%w: job has incomplete session identity", ErrSessionPurgeGenerationMismatch)
	}

	sessionID, sessionScoped, malformed := legacyJobSessionIdentity(projectKey, jobKey)
	if malformed {
		return fmt.Errorf("%w: malformed legacy session job key", ErrSessionPurgeGenerationMismatch)
	}
	if !sessionScoped {
		return nil
	}
	return checkLegacySessionPurgeFenceTx(ctx, tx, projectKey, sessionID)
}

// legacyJobSessionIdentity recognizes only the stable session-key formats
// emitted by the legacy extractor and session rollup paths. A session-shaped
// but malformed or foreign-project-qualified key is treated as unsafe.
func legacyJobSessionIdentity(projectKey, jobKey string) (sessionID string, sessionScoped, malformed bool) {
	jobKey = strings.TrimSpace(jobKey)
	projectKey = strings.TrimSpace(projectKey)
	prefix := ""
	switch {
	case strings.HasPrefix(jobKey, "session:"):
		prefix = "session:"
	case projectKey != "" && strings.HasPrefix(jobKey, "project:"+projectKey+":session:"):
		prefix = "project:" + projectKey + ":session:"
	case strings.HasPrefix(jobKey, "project:") && strings.Contains(jobKey, ":session:"):
		return "", true, true
	default:
		return "", false, false
	}

	rest := strings.TrimPrefix(jobKey, prefix)
	separator := strings.IndexByte(rest, ':')
	if separator <= 0 || separator == len(rest)-1 {
		return "", true, true
	}
	return strings.TrimSpace(rest[:separator]), true, strings.TrimSpace(rest[:separator]) == ""
}

// A legacy job has no generation with which to distinguish a deleted
// incarnation from a recreated session using the same ID. Once any tombstone
// exists, it must therefore stay quarantined rather than being treated as
// evidence of a strict generation fence.
func checkLegacySessionPurgeFenceTx(ctx context.Context, tx *sql.Tx, projectKey, sessionID string) error {
	var exists int
	err := tx.QueryRowContext(ctx, `
SELECT 1 FROM memory_session_purges
WHERE project_key = ? AND session_id = ?
`, strings.TrimSpace(projectKey), strings.TrimSpace(sessionID)).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("memorystore: read legacy session purge fence: %w", err)
	}
	return fmt.Errorf("%w: legacy session job cannot prove incarnation for session %s",
		ErrSessionPurgeGenerationMismatch, sessionID)
}

// checkRecordSessionPurgeFenceTx applies the same fail-closed rule to generic
// record writers as to extraction jobs. A generation-qualified record must
// match the current incarnation; a legacy record without a generation cannot
// prove which incarnation it belongs to and is therefore rejected once any
// purge tombstone exists.
func checkRecordSessionPurgeFenceTx(
	ctx context.Context,
	tx *sql.Tx,
	record Record,
) error {
	sessionID := strings.TrimSpace(record.SourceSessionID)
	if sessionID == "" {
		return nil
	}
	if record.SourceSessionGeneration > 0 {
		return checkSessionPurgeGenerationTx(ctx, tx, record.ProjectKey,
			sessionID, record.SourceSessionGeneration)
	}
	return checkLegacySessionPurgeFenceTx(ctx, tx, record.ProjectKey, sessionID)
}
