package usageanalytics

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

const (
	defaultToolRecordCompactBatch = 200
	// The 16 KiB cap applies to new writes. Historical rows often contain
	// unnecessary multi-KiB payloads that are below that cap, so the one-time
	// rewrite starts much lower.
	defaultToolRecordCompactMax = 1024
)

// ToolRecordCompactOptions controls the one-pass rewrite of oversized tool
// payloads written before analytics records were thinned.
type ToolRecordCompactOptions struct {
	MaxBytes  int
	BatchSize int
	DryRun    bool
}

// ToolRecordCompactStats reports the selected rows and payload bytes.
type ToolRecordCompactStats struct {
	Scanned     int64 `json:"scanned"`
	Rewritten   int64 `json:"rewritten"`
	Skipped     int64 `json:"skipped"`
	BytesBefore int64 `json:"bytes_before"`
	BytesAfter  int64 `json:"bytes_after"`
	DryRun      bool  `json:"dry_run"`
}

// CompactToolRecords rewrites oversized usage_tool_calls.record_json rows to
// the same compact allowlist used for new ingestion. The rowid cursor makes the
// operation a single forward pass instead of rescanning rewritten batches.
func (s *Store) CompactToolRecords(ctx context.Context, opts ToolRecordCompactOptions) (ToolRecordCompactStats, error) {
	var result ToolRecordCompactStats
	if s == nil || s.db == nil || s.empty {
		return result, fmt.Errorf("compact tool records: store is unavailable")
	}
	if !opts.DryRun && s.readOnly {
		return result, fmt.Errorf("compact tool records: store is not writable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultToolRecordCompactMax
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = defaultToolRecordCompactBatch
	}
	result.DryRun = opts.DryRun

	var lastRowID int64
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		batch, selected, nextRowID, err := s.compactToolRecordBatch(ctx, lastRowID, maxBytes, batchSize, opts.DryRun)
		if err != nil {
			return result, err
		}
		result.Scanned += batch.Scanned
		result.Rewritten += batch.Rewritten
		result.Skipped += batch.Skipped
		result.BytesBefore += batch.BytesBefore
		result.BytesAfter += batch.BytesAfter
		lastRowID = nextRowID
		if selected < int64(batchSize) {
			return result, nil
		}
	}
}

func (s *Store) compactToolRecordBatch(
	ctx context.Context,
	afterRowID int64,
	maxBytes int,
	batchSize int,
	dryRun bool,
) (ToolRecordCompactStats, int64, int64, error) {
	var batch ToolRecordCompactStats
	query := `SELECT rowid, record_json FROM usage_tool_calls
WHERE rowid > ? AND record_json IS NOT NULL AND length(record_json) > ?
ORDER BY rowid LIMIT ?`

	if dryRun {
		rows, err := s.readDB().QueryContext(ctx, query, afterRowID, maxBytes, batchSize)
		if err != nil {
			return batch, 0, afterRowID, fmt.Errorf("scan tool records: %w", err)
		}
		defer rows.Close()
		selected, nextRowID, err := scanToolRecordBatch(rows, nil, &batch)
		return batch, selected, nextRowID, err
	}

	var selected, nextRowID int64
	err := s.withWriteTx(func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, query, afterRowID, maxBytes, batchSize)
		if err != nil {
			return fmt.Errorf("scan tool records: %w", err)
		}
		updates := make([]toolRecordUpdate, 0, batchSize)
		selected, nextRowID, err = scanToolRecordBatch(rows, &updates, &batch)
		if err != nil {
			return err
		}
		for _, update := range updates {
			if _, err := tx.ExecContext(ctx,
				"UPDATE usage_tool_calls SET record_json = ? WHERE rowid = ?",
				update.recordJSON, update.rowID,
			); err != nil {
				return fmt.Errorf("rewrite tool record rowid=%d: %w", update.rowID, err)
			}
		}
		return nil
	})
	if err != nil {
		return ToolRecordCompactStats{}, 0, afterRowID, err
	}
	return batch, selected, nextRowID, nil
}

type toolRecordUpdate struct {
	rowID      int64
	recordJSON []byte
}

func scanToolRecordBatch(rows *sql.Rows, updates *[]toolRecordUpdate, stats *ToolRecordCompactStats) (int64, int64, error) {
	defer rows.Close()
	var selected, nextRowID int64
	for rows.Next() {
		var rowID int64
		var raw []byte
		if err := rows.Scan(&rowID, &raw); err != nil {
			return selected, nextRowID, fmt.Errorf("scan tool record: %w", err)
		}
		selected++
		nextRowID = rowID
		stats.Scanned++
		stats.BytesBefore += int64(len(raw))

		thin, ok := compactToolRecordPayload(raw)
		if !ok || len(thin) >= len(raw) {
			stats.Skipped++
			stats.BytesAfter += int64(len(raw))
			continue
		}
		stats.Rewritten++
		stats.BytesAfter += int64(len(thin))
		if updates != nil {
			*updates = append(*updates, toolRecordUpdate{rowID: rowID, recordJSON: thin})
		}
	}
	if err := rows.Err(); err != nil {
		return selected, nextRowID, fmt.Errorf("read tool records: %w", err)
	}
	return selected, nextRowID, nil
}

func compactToolRecordPayload(raw []byte) ([]byte, bool) {
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return nil, false
	}
	thin := analyticsToolRecordJSON(payload)
	if thin == "" {
		return nil, false
	}
	return []byte(thin), true
}
