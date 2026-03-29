package artifact

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	if s == nil || s.db == nil {
		return "", "", fmt.Errorf("artifact store is not initialized")
	}
	hash := sha256.Sum256(data)
	sum := hex.EncodeToString(hash[:])

	var existingID string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM blobs WHERE sha256 = ?`, sum).Scan(&existingID); err == nil {
		return existingID, sum, nil
	} else if err != sql.ErrNoRows {
		return "", "", fmt.Errorf("lookup blob: %w", err)
	}

	id := "blob_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO blobs (id, sha256, encoding, data)
		VALUES (?, ?, ?, ?)
	`, id, sum, "raw", data)
	if err != nil {
		return "", "", fmt.Errorf("insert blob: %w", err)
	}
	return id, sum, nil
}

// SaveCheckpointFiles stores checkpoint file metadata.
func (s *Store) SaveCheckpointFiles(ctx context.Context, checkpointID string, files []CheckpointFile) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("artifact store is not initialized")
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
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("artifact store is not initialized")
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
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("artifact store is not initialized")
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
