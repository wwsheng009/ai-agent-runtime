package artifact

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// CheckpointFile captures per-file checkpoint metadata.
type CheckpointFile struct {
	ID           string
	CheckpointID string
	Path         string
	Op           string
	BeforeBlobID string
	AfterBlobID  string
	BeforeHash   string
	AfterHash    string
	DiffText     string
}

// SaveBlob persists content in the blobs table, deduplicated by sha256.
func (s *Store) SaveBlob(ctx context.Context, data []byte) (string, string, error) {
	if s == nil {
		return "", "", fmt.Errorf("artifact store is not initialized")
	}
	if err := s.ensure(); err != nil {
		return "", "", err
	}
	hash := sha256.Sum256(data)
	sum := hex.EncodeToString(hash[:])

	id := "blob_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO blobs (id, sha256, encoding, data)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(sha256) DO NOTHING
	`, id, sum, "raw", data)
	if err != nil {
		return "", "", fmt.Errorf("insert blob: %w", err)
	}
	if inserted, rowsErr := result.RowsAffected(); rowsErr == nil && inserted > 0 {
		return id, sum, nil
	}

	var existingID string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM blobs WHERE sha256 = ?`, sum).Scan(&existingID); err != nil {
		return "", "", fmt.Errorf("lookup deduplicated blob: %w", err)
	}
	return existingID, sum, nil
}

// SaveCheckpointFiles stores checkpoint file metadata.
func (s *Store) SaveCheckpointFiles(ctx context.Context, checkpointID string, files []CheckpointFile) error {
	if s == nil {
		return fmt.Errorf("artifact store is not initialized")
	}
	if err := s.ensure(); err != nil {
		return err
	}
	if strings.TrimSpace(checkpointID) == "" {
		return fmt.Errorf("checkpoint id is required")
	}
	if len(files) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin checkpoint_files tx: %w", err)
	}
	for _, file := range files {
		if file.Path == "" {
			continue
		}
		if file.ID == "" {
			file.ID = "chkfile_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
		if file.CheckpointID == "" {
			file.CheckpointID = checkpointID
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO checkpoint_files (
				id, checkpoint_id, path, op, before_blob_id, after_blob_id, before_hash, after_hash, diff_text
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, file.ID, file.CheckpointID, file.Path, file.Op, nullIfEmpty(file.BeforeBlobID), nullIfEmpty(file.AfterBlobID),
			nullIfEmpty(file.BeforeHash), nullIfEmpty(file.AfterHash), file.DiffText); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert checkpoint_file: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit checkpoint_files tx: %w", err)
	}
	return nil
}

// LoadBlob returns blob data by id.
func (s *Store) LoadBlob(ctx context.Context, blobID string) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("artifact store is not initialized")
	}
	skipEmpty, err := s.ensureForRead()
	if err != nil {
		return nil, err
	}
	if skipEmpty {
		return nil, nil
	}
	blobID = strings.TrimSpace(blobID)
	if blobID == "" {
		return nil, fmt.Errorf("blob id is required")
	}
	row := s.db.QueryRowContext(ctx, `SELECT data FROM blobs WHERE id = ?`, blobID)
	var data []byte
	if err := row.Scan(&data); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("load blob: %w", err)
	}
	return data, nil
}

// GetCheckpointFiles returns checkpoint file metadata for a checkpoint.
func (s *Store) GetCheckpointFiles(ctx context.Context, checkpointID string) ([]CheckpointFile, error) {
	if s == nil {
		return nil, fmt.Errorf("artifact store is not initialized")
	}
	skipEmpty, err := s.ensureForRead()
	if err != nil {
		return nil, err
	}
	if skipEmpty {
		return nil, nil
	}
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return nil, fmt.Errorf("checkpoint id is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, checkpoint_id, path, op, before_blob_id, after_blob_id, before_hash, after_hash, diff_text
		FROM checkpoint_files
		WHERE checkpoint_id = ?
		ORDER BY path ASC
	`, checkpointID)
	if err != nil {
		return nil, fmt.Errorf("list checkpoint_files: %w", err)
	}
	defer rows.Close()

	files := make([]CheckpointFile, 0)
	for rows.Next() {
		var (
			file         CheckpointFile
			beforeBlobID sql.NullString
			afterBlobID  sql.NullString
			beforeHash   sql.NullString
			afterHash    sql.NullString
			diffText     sql.NullString
		)
		if err := rows.Scan(&file.ID, &file.CheckpointID, &file.Path, &file.Op, &beforeBlobID, &afterBlobID, &beforeHash, &afterHash, &diffText); err != nil {
			return nil, fmt.Errorf("scan checkpoint_file: %w", err)
		}
		if beforeBlobID.Valid {
			file.BeforeBlobID = beforeBlobID.String
		}
		if afterBlobID.Valid {
			file.AfterBlobID = afterBlobID.String
		}
		if beforeHash.Valid {
			file.BeforeHash = beforeHash.String
		}
		if afterHash.Valid {
			file.AfterHash = afterHash.String
		}
		if diffText.Valid {
			file.DiffText = diffText.String
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return files, nil
}

func nullIfEmpty(value string) interface{} {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

// PruneSessionCheckpoints retains the newest keep checkpoints for a session and
// removes candidate blobs referenced by pruned rows once no checkpoint file or
// conversation snapshot still references them. It is not a global orphan sweep.
// It intentionally does not VACUUM: pruning stops logical growth and frees pages
// for SQLite reuse; callers may compact the database separately during downtime.
func (s *Store) PruneSessionCheckpoints(ctx context.Context, sessionID string, keep int) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("artifact store is not initialized")
	}
	if keep < 0 {
		return 0, fmt.Errorf("checkpoint retention cannot be negative")
	}
	if keep == 0 {
		return 0, nil
	}
	if err := s.ensure(); err != nil {
		return 0, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, fmt.Errorf("session id is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin checkpoint prune tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT id, metadata_json
		FROM checkpoints
		WHERE session_id = ?
		ORDER BY created_at DESC, rowid DESC
		LIMIT -1 OFFSET ?
	`, sessionID, keep)
	if err != nil {
		return 0, fmt.Errorf("list checkpoints to prune: %w", err)
	}

	checkpointIDs := make([]string, 0)
	candidateBlobs := make(map[string]struct{})
	for rows.Next() {
		var checkpointID, metadataJSON string
		if err := rows.Scan(&checkpointID, &metadataJSON); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan checkpoint to prune: %w", err)
		}
		checkpointIDs = append(checkpointIDs, checkpointID)
		for _, blobID := range checkpointMetadataBlobIDs(metadataJSON) {
			candidateBlobs[blobID] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("iterate checkpoints to prune: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(checkpointIDs) == 0 {
		return 0, nil
	}

	for _, checkpointID := range checkpointIDs {
		blobRows, queryErr := tx.QueryContext(ctx, `
			SELECT before_blob_id, after_blob_id
			FROM checkpoint_files
			WHERE checkpoint_id = ?
		`, checkpointID)
		if queryErr != nil {
			return 0, fmt.Errorf("list checkpoint blobs: %w", queryErr)
		}
		for blobRows.Next() {
			var beforeID, afterID sql.NullString
			if scanErr := blobRows.Scan(&beforeID, &afterID); scanErr != nil {
				_ = blobRows.Close()
				return 0, fmt.Errorf("scan checkpoint blob: %w", scanErr)
			}
			if beforeID.Valid && strings.TrimSpace(beforeID.String) != "" {
				candidateBlobs[beforeID.String] = struct{}{}
			}
			if afterID.Valid && strings.TrimSpace(afterID.String) != "" {
				candidateBlobs[afterID.String] = struct{}{}
			}
		}
		if err := blobRows.Err(); err != nil {
			_ = blobRows.Close()
			return 0, fmt.Errorf("iterate checkpoint blobs: %w", err)
		}
		if err := blobRows.Close(); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM checkpoint_files WHERE checkpoint_id = ?`, checkpointID); err != nil {
			return 0, fmt.Errorf("delete checkpoint files: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM checkpoints WHERE id = ?`, checkpointID); err != nil {
			return 0, fmt.Errorf("delete checkpoint: %w", err)
		}
	}

	referencedConversationBlobs := make(map[string]struct{})
	metadataRows, err := tx.QueryContext(ctx, `
		SELECT metadata_json
		FROM checkpoints
		WHERE metadata_json LIKE '%conversation_blob_id%'
	`)
	if err != nil {
		return 0, fmt.Errorf("list retained conversation blob references: %w", err)
	}
	for metadataRows.Next() {
		var metadataJSON string
		if err := metadataRows.Scan(&metadataJSON); err != nil {
			_ = metadataRows.Close()
			return 0, fmt.Errorf("scan retained conversation blob reference: %w", err)
		}
		for _, blobID := range checkpointMetadataBlobIDs(metadataJSON) {
			referencedConversationBlobs[blobID] = struct{}{}
		}
	}
	if err := metadataRows.Err(); err != nil {
		_ = metadataRows.Close()
		return 0, fmt.Errorf("iterate retained conversation blob references: %w", err)
	}
	if err := metadataRows.Close(); err != nil {
		return 0, err
	}

	for blobID := range candidateBlobs {
		if strings.TrimSpace(blobID) == "" {
			continue
		}
		var referenced int
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM checkpoint_files
				WHERE before_blob_id = ? OR after_blob_id = ?
			)
		`, blobID, blobID).Scan(&referenced); err != nil {
			return 0, fmt.Errorf("check checkpoint blob references: %w", err)
		}
		if referenced != 0 {
			continue
		}
		if _, ok := referencedConversationBlobs[blobID]; ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM blobs WHERE id = ?`, blobID); err != nil {
			return 0, fmt.Errorf("delete orphan checkpoint blob: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit checkpoint prune tx: %w", err)
	}
	return len(checkpointIDs), nil
}

func checkpointMetadataBlobIDs(metadataJSON string) []string {
	if strings.TrimSpace(metadataJSON) == "" {
		return nil
	}
	var metadata map[string]interface{}
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		return nil
	}
	blobID, _ := metadata["conversation_blob_id"].(string)
	blobID = strings.TrimSpace(blobID)
	if blobID == "" {
		return nil
	}
	return []string{blobID}
}
