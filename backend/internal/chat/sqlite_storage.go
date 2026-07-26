package chat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

// SQLiteSessionStorage keeps the immutable transcript separate from the
// bounded prompt projection. Normal Load calls only materialize the projection.
//
// Schema evolution uses PRAGMA user_version so already-migrated databases skip
// CREATE TABLE / ALTER TABLE inspection on every chat start. Bump
// sqliteSessionSchemaVersion whenever the on-disk schema shape changes.
const sqliteSessionSchemaVersion = 1

type SQLiteSessionStorage struct {
	db  *sql.DB
	cfg PersistentSessionStorageConfig
}

type encodedSessionMessage struct {
	message types.Message
	payload []byte
	size    int
}

type promptProjectionRow struct {
	position int
	encoded  encodedSessionMessage
}

type artifactWriteTracker struct {
	paths []string
}

type artifactWriteTrackerContextKey struct{}

func (s *SQLiteSessionStorage) beginWriteTx(ctx context.Context) (context.Context, *sql.Tx, *artifactWriteTracker, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ctx, nil, nil, err
	}
	tracker := &artifactWriteTracker{}
	return context.WithValue(ctx, artifactWriteTrackerContextKey{}, tracker), tx, tracker, nil
}

func trackCreatedArtifact(ctx context.Context, relativePath string) {
	tracker, _ := ctx.Value(artifactWriteTrackerContextKey{}).(*artifactWriteTracker)
	if tracker != nil && relativePath != "" {
		tracker.paths = append(tracker.paths, relativePath)
	}
}

func (s *SQLiteSessionStorage) rollbackWriteTx(tx *sql.Tx, tracker *artifactWriteTracker) {
	if tx != nil {
		_ = tx.Rollback()
	}
	if tracker == nil {
		return
	}
	baseDir := s.cfg.Dir
	if strings.TrimSpace(baseDir) == "" {
		baseDir = filepath.Dir(s.cfg.Path)
	}
	artifactRoot := filepath.Join(baseDir, "session-artifacts")
	for _, relativePath := range tracker.paths {
		target := filepath.Join(baseDir, relativePath)
		if pathWithin(artifactRoot, target) {
			_ = os.Remove(target)
		}
	}
}

func NewSQLiteSessionStorage(cfg PersistentSessionStorageConfig) (*SQLiteSessionStorage, error) {
	cfg = normalizePersistentSessionStorageConfig(cfg)
	if strings.TrimSpace(cfg.Path) == "" {
		return nil, fmt.Errorf("sqlite session storage path cannot be empty")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o755); err != nil {
		return nil, fmt.Errorf("create sqlite session directory: %w", err)
	}
	db, err := sql.Open("sqlite3", cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite session storage: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &SQLiteSessionStorage{db: db, cfg: cfg}
	if err := store.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if cfg.ImportLegacyJSON {
		if err := store.importLegacyJSONFiles(context.Background()); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return store, nil
}

func (s *SQLiteSessionStorage) Dir() string {
	if s == nil {
		return ""
	}
	return s.cfg.Dir
}

func (s *SQLiteSessionStorage) Path() string {
	if s == nil {
		return ""
	}
	return s.cfg.Path
}

// Snapshot creates a compact, transactionally consistent database image.
// VACUUM INTO includes committed WAL contents without loading the database into
// Go memory, which makes it suitable for live debug exports.
func (s *SQLiteSessionStorage) Snapshot(ctx context.Context, destinationPath string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite session storage is closed")
	}
	destinationPath = strings.TrimSpace(destinationPath)
	if destinationPath == "" {
		return fmt.Errorf("sqlite snapshot destination cannot be empty")
	}
	sourcePath, err := filepath.Abs(s.cfg.Path)
	if err != nil {
		return fmt.Errorf("resolve sqlite session storage path: %w", err)
	}
	destinationPath, err = filepath.Abs(destinationPath)
	if err != nil {
		return fmt.Errorf("resolve sqlite snapshot path: %w", err)
	}
	if strings.EqualFold(filepath.Clean(sourcePath), filepath.Clean(destinationPath)) {
		return fmt.Errorf("sqlite snapshot destination must differ from source")
	}
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return fmt.Errorf("create sqlite snapshot directory: %w", err)
	}
	if _, err := os.Stat(destinationPath); err == nil {
		return fmt.Errorf("sqlite snapshot destination already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect sqlite snapshot destination: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", destinationPath); err != nil {
		_ = os.Remove(destinationPath)
		return fmt.Errorf("create sqlite session snapshot: %w", err)
	}
	return nil
}

// SnapshotSession writes a database containing only the requested session.
// A dedicated connection and read transaction keep the three copied tables at
// one committed source revision without materializing rows in Go memory.
func (s *SQLiteSessionStorage) SnapshotSession(ctx context.Context, sessionID, destinationPath string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite session storage is closed")
	}
	sessionID = sanitizeSessionID(sessionID)
	if sessionID == "" {
		return ErrInvalidSession
	}
	destinationPath, err := s.prepareSnapshotDestination(destinationPath)
	if err != nil {
		return err
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open sqlite snapshot connection: %w", err)
	}
	defer connection.Close()
	attached := false
	committed := false
	defer func() {
		if attached {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
			_, _ = connection.ExecContext(context.Background(), "DETACH DATABASE snapshot")
		}
		if !committed {
			_ = os.Remove(destinationPath)
		}
	}()
	if _, err := connection.ExecContext(ctx, "ATTACH DATABASE ? AS snapshot", destinationPath); err != nil {
		return fmt.Errorf("attach sqlite session snapshot: %w", err)
	}
	attached = true
	if _, err := connection.ExecContext(ctx, "BEGIN"); err != nil {
		return fmt.Errorf("begin sqlite session snapshot: %w", err)
	}
	var exists int
	if err := connection.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ?`, sessionID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return ErrSessionNotFound
		}
		return fmt.Errorf("check snapshotted session: %w", err)
	}
	if _, err := connection.ExecContext(ctx, sqliteSessionSnapshotSchema); err != nil {
		return fmt.Errorf("create sqlite session snapshot schema: %w", err)
	}
	if err := copySQLiteSessionSnapshot(ctx, connection, sessionID); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit sqlite session snapshot: %w", err)
	}
	if _, err := connection.ExecContext(ctx, "DETACH DATABASE snapshot"); err != nil {
		return fmt.Errorf("detach sqlite session snapshot: %w", err)
	}
	attached = false
	committed = true
	return nil
}

func (s *SQLiteSessionStorage) prepareSnapshotDestination(destinationPath string) (string, error) {
	destinationPath = strings.TrimSpace(destinationPath)
	if destinationPath == "" {
		return "", fmt.Errorf("sqlite snapshot destination cannot be empty")
	}
	resolved, err := filepath.Abs(destinationPath)
	if err != nil {
		return "", fmt.Errorf("resolve sqlite snapshot path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return "", fmt.Errorf("create sqlite snapshot directory: %w", err)
	}
	if _, err := os.Stat(resolved); err == nil {
		return "", fmt.Errorf("sqlite snapshot destination already exists")
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect sqlite snapshot destination: %w", err)
	}
	return resolved, nil
}

func (s *SQLiteSessionStorage) CloseStorage() error {
	if s == nil || s.db == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.BusyTimeout)
	defer cancel()
	_, checkpointErr := s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	closeErr := s.db.Close()
	if closeErr != nil {
		return closeErr
	}
	if checkpointErr != nil {
		return fmt.Errorf("checkpoint sqlite session WAL: %w", checkpointErr)
	}
	return nil
}

func (s *SQLiteSessionStorage) userVersion(ctx context.Context) (int, error) {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read sqlite session schema version: %w", err)
	}
	return version, nil
}

func (s *SQLiteSessionStorage) init(ctx context.Context) error {
	// busy_timeout must be set before any other PRAGMA/query so a concurrent
	// aicli process holding the write lock does not fail the open path.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", s.cfg.BusyTimeout.Milliseconds())); err != nil {
		return fmt.Errorf("configure sqlite session storage: %w", err)
	}
	version, err := s.userVersion(ctx)
	if err != nil {
		return err
	}
	// Fast path: already-migrated DBs only need per-connection PRAGMAs.
	// Skip durable one-time settings (auto_vacuum / journal_mode / wal limits)
	// that are already persisted on the database file.
	if version >= sqliteSessionSchemaVersion {
		if err := s.applyConnectionPRAGMAs(ctx); err != nil {
			return err
		}
		return nil
	}
	if err := s.applyBootstrapPRAGMAs(ctx); err != nil {
		return err
	}
	if err := s.migrateSchema(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, sqliteSessionSchemaVersion)); err != nil {
		return fmt.Errorf("set sqlite session schema version: %w", err)
	}
	return nil
}

func (s *SQLiteSessionStorage) applyConnectionPRAGMAs(ctx context.Context) error {
	pragmas := []string{
		"PRAGMA synchronous=NORMAL",
		fmt.Sprintf("PRAGMA busy_timeout=%d", s.cfg.BusyTimeout.Milliseconds()),
		fmt.Sprintf("PRAGMA cache_size=-%d", s.cfg.SQLiteCacheKiB),
		"PRAGMA temp_store=FILE",
		// Keep mmap disabled by default so large session DBs do not pin hundreds
		// of MB of virtual address space into every short-lived aicli process.
		"PRAGMA mmap_size=0",
		"PRAGMA foreign_keys=ON",
	}
	return execSQLitePRAGMAs(ctx, s.db, pragmas)
}

func (s *SQLiteSessionStorage) applyBootstrapPRAGMAs(ctx context.Context) error {
	// Full setup used for first open / schema migration. Durable settings are
	// persisted on the file; subsequent opens use applyConnectionPRAGMAs.
	pragmas := []string{
		"PRAGMA auto_vacuum=INCREMENTAL",
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		fmt.Sprintf("PRAGMA busy_timeout=%d", s.cfg.BusyTimeout.Milliseconds()),
		fmt.Sprintf("PRAGMA cache_size=-%d", s.cfg.SQLiteCacheKiB),
		"PRAGMA temp_store=FILE",
		"PRAGMA mmap_size=0",
		"PRAGMA wal_autocheckpoint=256",
		"PRAGMA journal_size_limit=16777216",
		"PRAGMA foreign_keys=ON",
	}
	return execSQLitePRAGMAs(ctx, s.db, pragmas)
}

func execSQLitePRAGMAs(ctx context.Context, db *sql.DB, pragmas []string) error {
	for _, statement := range pragmas {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite session storage: %w", err)
		}
	}
	return nil
}

func (s *SQLiteSessionStorage) migrateSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, sqliteSessionSchema)
	if err != nil {
		return fmt.Errorf("initialize sqlite session storage: %w", err)
	}
	columns := []struct {
		name       string
		definition string
	}{
		{name: "preview_json", definition: "BLOB"},
		{name: "role", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "tool_call_count", definition: "INTEGER NOT NULL DEFAULT -1"},
		{name: "tool_result", definition: "INTEGER NOT NULL DEFAULT -1"},
		{name: "content_part_count", definition: "INTEGER NOT NULL DEFAULT -1"},
	}
	for _, column := range columns {
		if err := ensureSQLiteColumn(ctx, s.db, "session_messages", column.name, column.definition); err != nil {
			return err
		}
	}
	return nil
}

const sqliteSessionSchema = `
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    state TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    title_source TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    message_count INTEGER NOT NULL DEFAULT 0,
    head_offset INTEGER NOT NULL DEFAULT 0,
    tags_json BLOB NOT NULL DEFAULT '[]',
    metadata_json BLOB NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    expires_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_sessions_user_updated
ON sessions(user_id, updated_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_sessions_state_updated
ON sessions(state, updated_at DESC, id);

CREATE TABLE IF NOT EXISTS session_messages (
    session_id TEXT NOT NULL,
    seq INTEGER NOT NULL,
    payload_json BLOB,
    artifact_path TEXT,
	preview_json BLOB,
	role TEXT NOT NULL DEFAULT '',
	tool_call_count INTEGER NOT NULL DEFAULT -1,
	tool_result INTEGER NOT NULL DEFAULT -1,
	content_part_count INTEGER NOT NULL DEFAULT -1,
    byte_count INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY(session_id, seq),
    FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS session_prompt_messages (
    session_id TEXT NOT NULL,
    position INTEGER NOT NULL,
    payload_json BLOB NOT NULL,
    byte_count INTEGER NOT NULL,
    PRIMARY KEY(session_id, position),
    FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);
`

const sqliteSessionSnapshotSchema = `
CREATE TABLE snapshot.sessions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    state TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    title_source TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    message_count INTEGER NOT NULL DEFAULT 0,
    head_offset INTEGER NOT NULL DEFAULT 0,
    tags_json BLOB NOT NULL DEFAULT '[]',
    metadata_json BLOB NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    expires_at TEXT
);
CREATE TABLE snapshot.session_messages (
    session_id TEXT NOT NULL,
    seq INTEGER NOT NULL,
    payload_json BLOB,
    artifact_path TEXT,
    preview_json BLOB,
    role TEXT NOT NULL DEFAULT '',
    tool_call_count INTEGER NOT NULL DEFAULT -1,
    tool_result INTEGER NOT NULL DEFAULT -1,
    content_part_count INTEGER NOT NULL DEFAULT -1,
    byte_count INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY(session_id, seq)
);
CREATE TABLE snapshot.session_prompt_messages (
    session_id TEXT NOT NULL,
    position INTEGER NOT NULL,
    payload_json BLOB NOT NULL,
    byte_count INTEGER NOT NULL,
    PRIMARY KEY(session_id, position)
);
`

func copySQLiteSessionSnapshot(ctx context.Context, connection *sql.Conn, sessionID string) error {
	statements := []string{
		`INSERT INTO snapshot.sessions(
			id, user_id, state, title, title_source, summary, message_count, head_offset,
			tags_json, metadata_json, created_at, updated_at, expires_at
		) SELECT id, user_id, state, title, title_source, summary, message_count, head_offset,
		         tags_json, metadata_json, created_at, updated_at, expires_at
		  FROM main.sessions WHERE id = ?`,
		`INSERT INTO snapshot.session_messages(
			session_id, seq, payload_json, artifact_path, preview_json, role,
			tool_call_count, tool_result, content_part_count, byte_count, sha256, created_at
		) SELECT session_id, seq, payload_json, artifact_path, preview_json, role,
		         tool_call_count, tool_result, content_part_count, byte_count, sha256, created_at
		  FROM main.session_messages WHERE session_id = ?`,
		`INSERT INTO snapshot.session_prompt_messages(session_id, position, payload_json, byte_count)
		 SELECT session_id, position, payload_json, byte_count
		 FROM main.session_prompt_messages WHERE session_id = ?`,
	}
	for _, statement := range statements {
		if _, err := connection.ExecContext(ctx, statement, sessionID); err != nil {
			return fmt.Errorf("copy sqlite session snapshot rows: %w", err)
		}
	}
	return nil
}

func ensureSQLiteColumn(ctx context.Context, db *sql.DB, table, column, definition string) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return fmt.Errorf("inspect sqlite table %s: %w", table, err)
	}
	found := false
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue interface{}
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if strings.EqualFold(name, column) {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+column+` `+definition); err != nil {
		return fmt.Errorf("add sqlite column %s.%s: %w", table, column, err)
	}
	return nil
}

func (s *SQLiteSessionStorage) Save(ctx context.Context, session *Session) error {
	if session == nil {
		return ErrInvalidSession
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(session.ID) == "" {
		session.ID = generateSessionID()
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now()
	}
	session.UpdatedAt = time.Now()

	ctx, tx, tracker, err := s.beginWriteTx(ctx)
	if err != nil {
		return fmt.Errorf("begin save session: %w", err)
	}
	committed := false
	var storedProjection []types.Message
	defer func() {
		if !committed {
			s.rollbackWriteTx(tx, tracker)
		}
	}()

	exists, err := sessionExistsTx(ctx, tx, session.ID)
	if err != nil {
		return err
	}
	if exists {
		if err := s.updateSessionTx(ctx, tx, session, &storedProjection); err != nil {
			return err
		}
	} else {
		if err := s.createSessionTx(ctx, tx, session, &storedProjection); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit save session: %w", err)
	}
	committed = true
	applyStoredPromptProjection(session, storedProjection)
	return nil
}

func (s *SQLiteSessionStorage) Update(ctx context.Context, session *Session) error {
	if session == nil || strings.TrimSpace(session.ID) == "" {
		return ErrInvalidSession
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, tx, tracker, err := s.beginWriteTx(ctx)
	if err != nil {
		return fmt.Errorf("begin update session: %w", err)
	}
	committed := false
	var storedProjection []types.Message
	defer func() {
		if !committed {
			s.rollbackWriteTx(tx, tracker)
		}
	}()
	exists, err := sessionExistsTx(ctx, tx, session.ID)
	if err != nil {
		return err
	}
	if !exists {
		return ErrSessionNotFound
	}
	if err := s.updateSessionTx(ctx, tx, session, &storedProjection); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit update session: %w", err)
	}
	committed = true
	applyStoredPromptProjection(session, storedProjection)
	return nil
}

func (s *SQLiteSessionStorage) createSessionTx(ctx context.Context, tx *sql.Tx, session *Session, storedProjection *[]types.Message) error {
	if err := s.upsertSessionMetadataTx(ctx, tx, session, 0); err != nil {
		return err
	}
	for index := range session.History {
		if err := s.insertCanonicalMessageTx(ctx, tx, session.ID, index+1, session.History[index]); err != nil {
			return err
		}
	}
	count := len(session.History)
	session.CanonicalMessageCount = count
	session.HistoryLoaded = true
	session.Metadata.TotalTurns = count
	projection, err := s.buildHotProjection(session.History)
	if err != nil {
		return err
	}
	if err := replacePromptMessagesTx(ctx, tx, session.ID, projection); err != nil {
		return err
	}
	*storedProjection = encodedMessages(projection)
	return s.upsertSessionMetadataTx(ctx, tx, session, count)
}

func (s *SQLiteSessionStorage) updateSessionTx(ctx context.Context, tx *sql.Tx, session *Session, storedProjection *[]types.Message) error {
	count, err := canonicalMessageCountTx(ctx, tx, session.ID)
	if err != nil {
		return err
	}
	if session.HistoryLoaded {
		promptRows, prefixMatches, err := loadMatchingPromptRowsTx(ctx, tx, session.ID, session.History)
		if err != nil {
			return err
		}
		currentPromptCount := len(promptRows)
		appendOnly := prefixMatches && len(session.History) > currentPromptCount
		appendFrom := len(session.History)
		if appendOnly {
			appendFrom = currentPromptCount
		} else if declaredDelta := session.CanonicalMessageCount - count; declaredDelta > 0 && declaredDelta <= len(session.History) {
			appendFrom = len(session.History) - declaredDelta
		}
		if appendFrom < len(session.History) {
			incoming, err := encodeMessages(session.History[appendFrom:])
			if err != nil {
				return err
			}
			for index := range incoming {
				count++
				if err := s.insertCanonicalEncodedTx(ctx, tx, session.ID, count, incoming[index]); err != nil {
					return err
				}
			}
		}
		switch {
		case appendOnly:
			for index := appendFrom; index < len(session.History); index++ {
				promptRows, err = s.appendPromptMessageTx(ctx, tx, session.ID, promptRows, session.History[index])
				if err != nil {
					return err
				}
			}
			*storedProjection = promptRowMessages(promptRows)
		case prefixMatches && len(session.History) == currentPromptCount:
			// Metadata-only updates do not rewrite the bounded prompt projection.
			*storedProjection = session.History
		default:
			projection, err := s.buildHotProjection(session.History)
			if err != nil {
				return err
			}
			if err := replacePromptMessagesTx(ctx, tx, session.ID, projection); err != nil {
				return err
			}
			*storedProjection = encodedMessages(projection)
		}
	}
	session.CanonicalMessageCount = count
	session.Metadata.TotalTurns = count
	session.UpdatedAt = time.Now()
	return s.upsertSessionMetadataTx(ctx, tx, session, count)
}

func sessionExistsTx(ctx context.Context, tx *sql.Tx, sessionID string) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ?`, sessionID).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check session existence: %w", err)
	}
	return true, nil
}

func canonicalMessageCountTx(ctx context.Context, tx *sql.Tx, sessionID string) (int, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT message_count FROM sessions WHERE id = ?`, sessionID).Scan(&count); err != nil {
		return 0, fmt.Errorf("load canonical message count: %w", err)
	}
	return count, nil
}

func (s *SQLiteSessionStorage) upsertSessionMetadataTx(ctx context.Context, tx *sql.Tx, session *Session, count int) error {
	metadataJSON, err := json.Marshal(session.Metadata)
	if err != nil {
		return fmt.Errorf("encode session metadata: %w", err)
	}
	tagsJSON, err := json.Marshal(session.Metadata.Tags)
	if err != nil {
		return fmt.Errorf("encode session tags: %w", err)
	}
	var expiresAt interface{}
	if session.ExpiresAt != nil {
		expiresAt = session.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO sessions (
			id, user_id, state, title, title_source, summary, message_count,
			head_offset, tags_json, metadata_json, created_at, updated_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			user_id = excluded.user_id,
			state = excluded.state,
			title = excluded.title,
			title_source = excluded.title_source,
			summary = excluded.summary,
			message_count = excluded.message_count,
			head_offset = excluded.head_offset,
			tags_json = excluded.tags_json,
			metadata_json = excluded.metadata_json,
			updated_at = excluded.updated_at,
			expires_at = excluded.expires_at
	`, session.ID, session.UserID, session.State, session.Metadata.Title,
		session.Metadata.TitleSource, session.Metadata.Summary, count,
		session.HeadOffset, tagsJSON, metadataJSON,
		session.CreatedAt.UTC().Format(time.RFC3339Nano),
		session.UpdatedAt.UTC().Format(time.RFC3339Nano), expiresAt)
	if err != nil {
		return fmt.Errorf("save session metadata: %w", err)
	}
	return nil
}

func (s *SQLiteSessionStorage) Load(ctx context.Context, sessionID string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessionID = sanitizeSessionID(sessionID)
	if sessionID == "" {
		return nil, ErrInvalidSession
	}
	session, err := scanSQLiteSession(s.db.QueryRowContext(ctx, `
		SELECT id, user_id, state, title, title_source, summary, message_count,
		       head_offset, tags_json, metadata_json, created_at, updated_at, expires_at
		FROM sessions WHERE id = ?
	`, sessionID))
	if err == sql.ErrNoRows {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load sqlite session: %w", err)
	}
	history, err := s.loadPromptMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	session.History = history
	session.HistoryLoaded = true
	if session.HeadOffset > len(history) {
		session.HeadOffset = len(history)
	}
	return session, nil
}

type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanSQLiteSession(row rowScanner) (*Session, error) {
	var session Session
	var state string
	var title, titleSource, summary string
	var tagsJSON, metadataJSON []byte
	var createdAt, updatedAt string
	var expiresAt sql.NullString
	if err := row.Scan(
		&session.ID, &session.UserID, &state, &title, &titleSource, &summary,
		&session.CanonicalMessageCount, &session.HeadOffset, &tagsJSON,
		&metadataJSON, &createdAt, &updatedAt, &expiresAt,
	); err != nil {
		return nil, err
	}
	if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &session.Metadata); err != nil {
			return nil, fmt.Errorf("decode session metadata: %w", err)
		}
	}
	session.State = SessionState(state)
	session.Metadata.Title = title
	session.Metadata.TitleSource = titleSource
	session.Metadata.Summary = summary
	session.Metadata.TotalTurns = session.CanonicalMessageCount
	if len(tagsJSON) > 0 {
		_ = json.Unmarshal(tagsJSON, &session.Metadata.Tags)
	}
	session.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	session.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	if expiresAt.Valid && strings.TrimSpace(expiresAt.String) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, expiresAt.String)
		if err != nil {
			return nil, fmt.Errorf("decode session expiry: %w", err)
		}
		session.ExpiresAt = &parsed
	}
	return &session, nil
}

func (s *SQLiteSessionStorage) loadPromptMessages(ctx context.Context, sessionID string) ([]types.Message, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT payload_json FROM session_prompt_messages
		WHERE session_id = ? ORDER BY position ASC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query prompt projection: %w", err)
	}
	defer rows.Close()
	var history []types.Message
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan prompt projection: %w", err)
		}
		var message types.Message
		if err := json.Unmarshal(payload, &message); err != nil {
			return nil, fmt.Errorf("decode prompt projection: %w", err)
		}
		history = append(history, message)
	}
	return history, rows.Err()
}

func encodeMessages(messages []types.Message) ([]encodedSessionMessage, error) {
	encoded := make([]encodedSessionMessage, 0, len(messages))
	for index := range messages {
		payload, err := json.Marshal(messages[index])
		if err != nil {
			return nil, fmt.Errorf("encode message %d: %w", index, err)
		}
		encoded = append(encoded, encodedSessionMessage{
			message: messages[index],
			payload: payload,
			size:    len(payload),
		})
	}
	return encoded, nil
}

func encodedMessages(messages []encodedSessionMessage) []types.Message {
	result := make([]types.Message, len(messages))
	for index := range messages {
		result[index] = messages[index].message
	}
	return result
}

func promptRowMessages(rows []promptProjectionRow) []types.Message {
	result := make([]types.Message, len(rows))
	for index := range rows {
		result[index] = rows[index].encoded.message
	}
	return result
}

func applyStoredPromptProjection(session *Session, projection []types.Message) {
	if session == nil || !session.HistoryLoaded {
		return
	}
	session.History = projection
	if session.HeadOffset > len(session.History) {
		session.HeadOffset = len(session.History)
	}
}

func loadMatchingPromptRowsTx(ctx context.Context, tx *sql.Tx, sessionID string, history []types.Message) ([]promptProjectionRow, bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT position, payload_json, byte_count FROM session_prompt_messages
		WHERE session_id = ? ORDER BY position ASC
	`, sessionID)
	if err != nil {
		return nil, false, fmt.Errorf("query current prompt projection: %w", err)
	}
	defer rows.Close()
	result := make([]promptProjectionRow, 0, min(len(history), 16))
	for rows.Next() {
		var position, byteCount int
		var payload []byte
		if err := rows.Scan(&position, &payload, &byteCount); err != nil {
			return nil, false, fmt.Errorf("scan current prompt projection: %w", err)
		}
		index := len(result)
		if index >= len(history) {
			return nil, false, nil
		}
		incoming, err := json.Marshal(history[index])
		if err != nil {
			return nil, false, fmt.Errorf("encode message %d: %w", index, err)
		}
		if !bytes.Equal(payload, incoming) {
			return nil, false, nil
		}
		result = append(result, promptProjectionRow{
			position: position,
			encoded: encodedSessionMessage{
				message: history[index],
				size:    byteCount,
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return result, true, nil
}

func loadPromptRowsTx(ctx context.Context, tx *sql.Tx, sessionID string) ([]promptProjectionRow, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT position, payload_json, byte_count FROM session_prompt_messages
		WHERE session_id = ? ORDER BY position ASC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query prompt projection rows: %w", err)
	}
	defer rows.Close()
	var result []promptProjectionRow
	for rows.Next() {
		var row promptProjectionRow
		if err := rows.Scan(&row.position, &row.encoded.payload, &row.encoded.size); err != nil {
			return nil, fmt.Errorf("scan prompt projection row: %w", err)
		}
		if err := json.Unmarshal(row.encoded.payload, &row.encoded.message); err != nil {
			return nil, fmt.Errorf("decode prompt projection row: %w", err)
		}
		row.encoded.payload = nil
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *SQLiteSessionStorage) appendPromptMessageTx(ctx context.Context, tx *sql.Tx, sessionID string, rows []promptProjectionRow, message types.Message) ([]promptProjectionRow, error) {
	newMessage, err := s.encodeHotMessage(message)
	if err != nil {
		return nil, err
	}
	nextPosition := 0
	if len(rows) > 0 {
		nextPosition = rows[len(rows)-1].position + 1
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO session_prompt_messages(session_id, position, payload_json, byte_count)
		VALUES (?, ?, ?, ?)
	`, sessionID, nextPosition, newMessage.payload, newMessage.size); err != nil {
		return nil, fmt.Errorf("append prompt projection: %w", err)
	}
	rows = append(rows, promptProjectionRow{position: nextPosition, encoded: newMessage})
	selected := make(map[int]struct{})
	usedBytes := 0
	add := func(index int) {
		if index < 0 || index >= len(rows) {
			return
		}
		if _, exists := selected[index]; exists {
			return
		}
		encoded := rows[index].encoded
		if len(selected) > 0 && (len(selected) >= s.cfg.HotHistoryMessages || usedBytes+encoded.size > s.cfg.HotHistoryBytes) {
			return
		}
		selected[index] = struct{}{}
		usedBytes += encoded.size
	}
	add(len(rows) - 1)
	for index := range rows {
		role := strings.ToLower(strings.TrimSpace(rows[index].encoded.message.Role))
		if role == "system" || role == "developer" {
			add(index)
		}
	}
	for index := len(rows) - 1; index >= 0; index-- {
		if strings.EqualFold(rows[index].encoded.message.Metadata.GetString("context_stage", ""), "compaction") {
			add(index)
			break
		}
	}
	for index := len(rows) - 1; index >= 0; index-- {
		add(index)
		if len(selected) >= s.cfg.HotHistoryMessages || usedBytes >= s.cfg.HotHistoryBytes {
			break
		}
	}
	deleteStatement, err := tx.PrepareContext(ctx, `DELETE FROM session_prompt_messages WHERE session_id = ? AND position = ?`)
	if err != nil {
		return nil, fmt.Errorf("prepare prompt projection trim: %w", err)
	}
	defer deleteStatement.Close()
	projection := make([]promptProjectionRow, 0, len(selected))
	for index := range rows {
		if _, keep := selected[index]; keep {
			projection = append(projection, rows[index])
			continue
		}
		if _, err := deleteStatement.ExecContext(ctx, sessionID, rows[index].position); err != nil {
			return nil, fmt.Errorf("trim prompt projection: %w", err)
		}
	}
	return projection, nil
}

func replacePromptMessagesTx(ctx context.Context, tx *sql.Tx, sessionID string, messages []encodedSessionMessage) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_prompt_messages WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("clear prompt projection: %w", err)
	}
	statement, err := tx.PrepareContext(ctx, `
		INSERT INTO session_prompt_messages(session_id, position, payload_json, byte_count)
		VALUES (?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare prompt projection insert: %w", err)
	}
	defer statement.Close()
	for index := range messages {
		if _, err := statement.ExecContext(ctx, sessionID, index, messages[index].payload, messages[index].size); err != nil {
			return fmt.Errorf("insert prompt projection message %d: %w", index, err)
		}
	}
	return nil
}

func (s *SQLiteSessionStorage) insertCanonicalMessageTx(ctx context.Context, tx *sql.Tx, sessionID string, seq int, message types.Message) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode canonical message %d: %w", seq, err)
	}
	return s.insertCanonicalEncodedTx(ctx, tx, sessionID, seq, encodedSessionMessage{
		message: message,
		payload: payload,
		size:    len(payload),
	})
}

func (s *SQLiteSessionStorage) insertCanonicalEncodedTx(ctx context.Context, tx *sql.Tx, sessionID string, seq int, encoded encodedSessionMessage) error {
	sum := sha256.Sum256(encoded.payload)
	digest := hex.EncodeToString(sum[:])
	var inlinePayload interface{} = encoded.payload
	var artifactPath interface{}
	if encoded.size > s.cfg.MaxInlineMessageBytes {
		relativePath, created, err := s.writeMessageArtifact(sessionID, digest, encoded.payload)
		if err != nil {
			return err
		}
		if created {
			trackCreatedArtifact(ctx, relativePath)
		}
		inlinePayload = nil
		artifactPath = relativePath
	}
	preview, err := s.encodeHotMessage(encoded.message)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO session_messages(
			session_id, seq, payload_json, artifact_path, preview_json, byte_count, sha256, created_at,
			role, tool_call_count, tool_result, content_part_count
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, sessionID, seq, inlinePayload, artifactPath, preview.payload, encoded.size, digest,
		time.Now().UTC().Format(time.RFC3339Nano), encoded.message.Role, len(encoded.message.ToolCalls),
		boolToSQLiteInt(strings.EqualFold(strings.TrimSpace(encoded.message.Role), "tool")), len(encoded.message.ContentParts))
	if err != nil {
		return fmt.Errorf("insert canonical message %d: %w", seq, err)
	}
	return nil
}

func boolToSQLiteInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *SQLiteSessionStorage) writeMessageArtifact(sessionID, digest string, payload []byte) (string, bool, error) {
	baseDir := s.cfg.Dir
	if strings.TrimSpace(baseDir) == "" {
		baseDir = filepath.Dir(s.cfg.Path)
	}
	relativePath := filepath.Join("session-artifacts", sanitizeSessionID(sessionID), digest+".json")
	absolutePath := filepath.Join(baseDir, relativePath)
	if _, err := os.Stat(absolutePath); err == nil {
		return relativePath, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o755); err != nil {
		return "", false, fmt.Errorf("create session artifact directory: %w", err)
	}
	temporaryPath := absolutePath + ".tmp"
	if err := os.WriteFile(temporaryPath, payload, 0o600); err != nil {
		return "", false, fmt.Errorf("write session message artifact: %w", err)
	}
	if err := os.Rename(temporaryPath, absolutePath); err != nil {
		_ = os.Remove(temporaryPath)
		if _, statErr := os.Stat(absolutePath); statErr == nil {
			return relativePath, false, nil
		}
		return "", false, fmt.Errorf("publish session message artifact: %w", err)
	}
	return relativePath, true, nil
}

func (s *SQLiteSessionStorage) buildHotProjection(messages []types.Message) ([]encodedSessionMessage, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	selected := make(map[int]encodedSessionMessage)
	usedBytes := 0
	add := func(index int) error {
		if index < 0 || index >= len(messages) {
			return nil
		}
		if _, exists := selected[index]; exists {
			return nil
		}
		encoded, err := s.encodeHotMessage(messages[index])
		if err != nil {
			return err
		}
		if len(selected) > 0 && (len(selected) >= s.cfg.HotHistoryMessages || usedBytes+encoded.size > s.cfg.HotHistoryBytes) {
			return nil
		}
		selected[index] = encoded
		usedBytes += encoded.size
		return nil
	}

	// The newest message is mandatory. Stable instructions and the latest
	// compaction checkpoint are then added as anchors before filling the tail.
	if err := add(len(messages) - 1); err != nil {
		return nil, err
	}
	for index := range messages {
		role := strings.ToLower(strings.TrimSpace(messages[index].Role))
		if role == "system" || role == "developer" {
			if err := add(index); err != nil {
				return nil, err
			}
		}
	}
	for index := len(messages) - 1; index >= 0; index-- {
		if strings.EqualFold(messages[index].Metadata.GetString("context_stage", ""), "compaction") {
			if err := add(index); err != nil {
				return nil, err
			}
			break
		}
	}
	for index := len(messages) - 1; index >= 0; index-- {
		if err := add(index); err != nil {
			return nil, err
		}
		if len(selected) >= s.cfg.HotHistoryMessages || usedBytes >= s.cfg.HotHistoryBytes {
			break
		}
	}
	indexes := make([]int, 0, len(selected))
	for index := range selected {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	projection := make([]encodedSessionMessage, 0, len(indexes))
	for _, index := range indexes {
		projection = append(projection, selected[index])
	}
	return projection, nil
}

func (s *SQLiteSessionStorage) encodeHotMessage(message types.Message) (encodedSessionMessage, error) {
	payload, err := json.Marshal(message)
	if err != nil {
		return encodedSessionMessage{}, fmt.Errorf("encode hot message: %w", err)
	}
	if len(payload) <= s.cfg.MaxHotMessageBytes {
		return encodedSessionMessage{message: message, payload: payload, size: len(payload)}, nil
	}

	bounded := *message.Clone()
	originalBytes := len(payload)
	contentBudget := s.cfg.MaxHotMessageBytes / 2
	if contentBudget < 1024 {
		contentBudget = 1024
	}
	bounded.Content = truncateUTF8Middle(bounded.Content, contentBudget)
	for index := range bounded.ContentParts {
		part := &bounded.ContentParts[index]
		part.Text = truncateUTF8Middle(part.Text, contentBudget/2)
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(part.ImageURL)), "data:") {
			part.ImageURL = ""
		}
	}
	for index := range bounded.ToolCalls {
		arguments, marshalErr := json.Marshal(bounded.ToolCalls[index].Args)
		if marshalErr == nil && len(arguments) > contentBudget/2 {
			bounded.ToolCalls[index].Args = map[string]interface{}{
				"_session_storage_omitted": true,
				"_byte_count":              len(arguments),
			}
		}
	}
	if bounded.Metadata == nil {
		bounded.Metadata = types.NewMetadata()
	}
	bounded.Metadata["session_storage_truncated"] = true
	bounded.Metadata["session_storage_original_bytes"] = originalBytes
	payload, err = json.Marshal(bounded)
	if err != nil {
		return encodedSessionMessage{}, fmt.Errorf("encode bounded hot message: %w", err)
	}
	if len(payload) > s.cfg.MaxHotMessageBytes {
		minimal := types.Message{
			Role:       truncateUTF8Middle(bounded.Role, 256),
			Content:    truncateUTF8Middle(bounded.Content, max(s.cfg.MaxHotMessageBytes-2048, 256)),
			ToolCallID: truncateUTF8Middle(bounded.ToolCallID, 512),
			Metadata: types.Metadata{
				"session_storage_truncated":      true,
				"session_storage_original_bytes": originalBytes,
			},
		}
		if stage := bounded.Metadata.GetString("context_stage", ""); stage != "" {
			minimal.Metadata["context_stage"] = truncateUTF8Middle(stage, 256)
		}
		toolCallLimit := min(len(bounded.ToolCalls), 16)
		minimal.ToolCalls = make([]types.ToolCall, 0, toolCallLimit)
		for index := 0; index < toolCallLimit; index++ {
			minimal.ToolCalls = append(minimal.ToolCalls, types.ToolCall{
				ID:   truncateUTF8Middle(bounded.ToolCalls[index].ID, 512),
				Name: truncateUTF8Middle(bounded.ToolCalls[index].Name, 512),
				Args: map[string]interface{}{"_session_storage_omitted": true},
			})
		}
		bounded = minimal
		payload, err = json.Marshal(bounded)
		if err != nil {
			return encodedSessionMessage{}, fmt.Errorf("encode minimal hot message: %w", err)
		}
	}
	if len(payload) > s.cfg.MaxHotMessageBytes {
		bounded.ToolCalls = nil
		bounded.Content = truncateUTF8Middle(bounded.Content, max(s.cfg.MaxHotMessageBytes-1024, 64))
		payload, err = json.Marshal(bounded)
		if err != nil {
			return encodedSessionMessage{}, fmt.Errorf("encode final hot message: %w", err)
		}
	}
	return encodedSessionMessage{message: bounded, payload: payload, size: len(payload)}, nil
}

func truncateUTF8Middle(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	marker := "\n...[session storage omitted middle content]...\n"
	if maxBytes <= len(marker)+8 {
		return string([]rune(value)[:min(len([]rune(value)), maxBytes/4)])
	}
	available := maxBytes - len(marker)
	headBytes := available / 2
	tailBytes := available - headBytes
	head := value[:headBytes]
	for !utf8.ValidString(head) && len(head) > 0 {
		head = head[:len(head)-1]
	}
	tail := value[len(value)-tailBytes:]
	for !utf8.ValidString(tail) && len(tail) > 0 {
		tail = tail[1:]
	}
	return head + marker + tail
}

func (s *SQLiteSessionStorage) AddMessage(ctx context.Context, sessionID string, message interface{}) error {
	msg, ok := message.(types.Message)
	if !ok {
		return ErrInvalidMessageType
	}
	return s.AddMessageWithLimit(ctx, sessionID, msg, s.cfg.HotHistoryMessages)
}

func (s *SQLiteSessionStorage) AddMessageWithLimit(ctx context.Context, sessionID string, message types.Message, maxHistory int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, tx, tracker, err := s.beginWriteTx(ctx)
	if err != nil {
		return fmt.Errorf("begin append session message: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			s.rollbackWriteTx(tx, tracker)
		}
	}()
	session, err := loadSQLiteSessionMetadataTx(ctx, tx, sessionID)
	if err == sql.ErrNoRows {
		return ErrSessionNotFound
	}
	if err != nil {
		return err
	}
	promptRows, err := loadPromptRowsTx(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	session.History = make([]types.Message, 0, len(promptRows))
	for index := range promptRows {
		session.History = append(session.History, promptRows[index].encoded.message)
	}
	session.HistoryLoaded = true
	count := session.CanonicalMessageCount + 1
	if err := s.insertCanonicalMessageTx(ctx, tx, sessionID, count, message); err != nil {
		return err
	}
	session.AddMessage(message)
	session.CanonicalMessageCount = count
	session.Metadata.TotalTurns = count
	_ = maxHistory // The store's byte and message budgets are authoritative.
	projectionRows, projectionErr := s.appendPromptMessageTx(ctx, tx, sessionID, promptRows, message)
	if projectionErr != nil {
		return projectionErr
	}
	session.History = make([]types.Message, 0, len(projectionRows))
	for index := range projectionRows {
		session.History = append(session.History, projectionRows[index].encoded.message)
	}
	if session.HeadOffset > len(session.History) {
		session.HeadOffset = len(session.History)
	}
	if err := s.upsertSessionMetadataTx(ctx, tx, session, count); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit append session message: %w", err)
	}
	committed = true
	return nil
}

func loadSQLiteSessionMetadataTx(ctx context.Context, tx *sql.Tx, sessionID string) (*Session, error) {
	return scanSQLiteSession(tx.QueryRowContext(ctx, `
		SELECT id, user_id, state, title, title_source, summary, message_count,
		       head_offset, tags_json, metadata_json, created_at, updated_at, expires_at
		FROM sessions WHERE id = ?
	`, sanitizeSessionID(sessionID)))
}

func (s *SQLiteSessionStorage) GetRecentMessages(ctx context.Context, sessionID string, limit int) ([]types.Message, error) {
	page, err := s.GetMessagePage(ctx, sessionID, 0, limit)
	if err != nil {
		return nil, err
	}
	return page.Messages, nil
}

func (s *SQLiteSessionStorage) GetMessagePage(ctx context.Context, sessionID string, beforeSeq, limit int) (*SessionHistoryPage, error) {
	if limit <= 0 || limit > s.cfg.HistoryPageMessages {
		limit = s.cfg.HistoryPageMessages
	}
	sessionID = sanitizeSessionID(sessionID)
	if sessionID == "" {
		return nil, ErrInvalidSession
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT message_count FROM sessions WHERE id = ?`, sessionID).Scan(&total); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("load history page message count: %w", err)
	}
	upperBound := total + 1
	if beforeSeq > 0 && beforeSeq < upperBound {
		upperBound = beforeSeq
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT seq, payload_json, artifact_path, preview_json, byte_count, sha256
		FROM session_messages
		WHERE session_id = ? AND seq < ?
		ORDER BY seq DESC
		LIMIT ?
	`, sessionID, upperBound, limit)
	if err != nil {
		return nil, fmt.Errorf("query canonical messages: %w", err)
	}
	defer rows.Close()
	messages := make([]types.Message, 0, limit)
	sequences := make([]int, 0, limit)
	usedBytes := 0
	for rows.Next() {
		var sequence int
		var inline []byte
		var artifact sql.NullString
		var preview []byte
		var byteCount int
		var digest string
		if err := rows.Scan(&sequence, &inline, &artifact, &preview, &byteCount, &digest); err != nil {
			return nil, fmt.Errorf("scan canonical message: %w", err)
		}
		payload := preview
		remainingBytes := s.cfg.HistoryPageBytes - usedBytes
		if byteCount <= remainingBytes || len(preview) == 0 {
			payload, err = s.readCanonicalPayload(inline, artifact, byteCount, digest)
			if err != nil {
				return nil, err
			}
		} else if len(messages) > 0 && len(preview) > remainingBytes {
			break
		}
		var message types.Message
		if err := json.Unmarshal(payload, &message); err != nil {
			return nil, fmt.Errorf("decode canonical message: %w", err)
		}
		messages = append(messages, message)
		sequences = append(sequences, sequence)
		usedBytes += len(payload)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
		sequences[left], sequences[right] = sequences[right], sequences[left]
	}
	page := &SessionHistoryPage{Messages: messages, Total: total}
	if len(sequences) > 0 {
		page.FirstSeq = sequences[0]
		page.LastSeq = sequences[len(sequences)-1]
		page.HasMore = page.FirstSeq > 1
		if page.HasMore {
			page.NextBeforeSeq = page.FirstSeq
		}
	}
	return page, nil
}

func (s *SQLiteSessionStorage) MessageCount(ctx context.Context, sessionID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT message_count FROM sessions WHERE id = ?`, sanitizeSessionID(sessionID)).Scan(&count)
	if err == sql.ErrNoRows {
		return 0, ErrSessionNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("load session message count: %w", err)
	}
	return count, nil
}

func (s *SQLiteSessionStorage) StreamMessages(ctx context.Context, sessionID string, visit func(seq int, message types.Message) error) error {
	if visit == nil {
		return fmt.Errorf("history visitor cannot be nil")
	}
	sessionID = sanitizeSessionID(sessionID)
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ?`, sessionID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return ErrSessionNotFound
		}
		return fmt.Errorf("check streamed session existence: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT seq, payload_json, artifact_path, byte_count, sha256
		FROM session_messages WHERE session_id = ? ORDER BY seq ASC
	`, sessionID)
	if err != nil {
		return fmt.Errorf("stream canonical messages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sequence, byteCount int
		var inline []byte
		var artifact sql.NullString
		var digest string
		if err := rows.Scan(&sequence, &inline, &artifact, &byteCount, &digest); err != nil {
			return fmt.Errorf("scan streamed canonical message: %w", err)
		}
		payload, err := s.readCanonicalPayload(inline, artifact, byteCount, digest)
		if err != nil {
			return err
		}
		var message types.Message
		if err := json.Unmarshal(payload, &message); err != nil {
			return fmt.Errorf("decode streamed canonical message %d: %w", sequence, err)
		}
		if err := visit(sequence, message); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func (s *SQLiteSessionStorage) StreamMessageJSON(ctx context.Context, sessionID string, visit func(seq int, info CanonicalMessageInfo, payload io.Reader) error) error {
	if visit == nil {
		return fmt.Errorf("canonical JSON visitor cannot be nil")
	}
	sessionID = sanitizeSessionID(sessionID)
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ?`, sessionID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return ErrSessionNotFound
		}
		return fmt.Errorf("check streamed session existence: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT seq, payload_json, artifact_path, preview_json, byte_count, sha256,
		       role, tool_call_count, tool_result, content_part_count
		FROM session_messages WHERE session_id = ? ORDER BY seq ASC
	`, sessionID)
	if err != nil {
		return fmt.Errorf("stream canonical message JSON: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sequence, byteCount, toolCalls, toolResult, contentParts int
		var inline, preview []byte
		var artifact sql.NullString
		var digest, role string
		if err := rows.Scan(&sequence, &inline, &artifact, &preview, &byteCount, &digest,
			&role, &toolCalls, &toolResult, &contentParts); err != nil {
			return fmt.Errorf("scan canonical message JSON: %w", err)
		}
		info := CanonicalMessageInfo{
			Role:             role,
			RoleKnown:        strings.TrimSpace(role) != "",
			ToolCallCount:    max(toolCalls, 0),
			ToolResult:       toolResult == 1,
			ContentPartCount: max(contentParts, 0),
			StatsKnown:       toolCalls >= 0 && toolResult >= 0 && contentParts >= 0,
		}
		if !info.StatsKnown && len(preview) > 0 {
			var message types.Message
			if err := json.Unmarshal(preview, &message); err == nil {
				info.Role = message.Role
				info.RoleKnown = strings.TrimSpace(message.Role) != ""
			}
		}
		if len(inline) > 0 {
			if err := validateCanonicalPayload(inline, byteCount, digest); err != nil {
				return fmt.Errorf("validate canonical message %d: %w", sequence, err)
			}
			if err := visit(sequence, info, bytes.NewReader(inline)); err != nil {
				return err
			}
			continue
		}
		if !artifact.Valid || strings.TrimSpace(artifact.String) == "" {
			return fmt.Errorf("canonical message %d payload is missing", sequence)
		}
		if err := s.streamCanonicalArtifact(ctx, artifact.String, byteCount, digest,
			func(reader io.Reader) error { return visit(sequence, info, reader) }); err != nil {
			return fmt.Errorf("stream canonical message %d artifact: %w", sequence, err)
		}
	}
	return rows.Err()
}

func (s *SQLiteSessionStorage) streamCanonicalArtifact(ctx context.Context, relativePath string, byteCount int, digest string, visit func(io.Reader) error) error {
	baseDir := s.cfg.Dir
	if strings.TrimSpace(baseDir) == "" {
		baseDir = filepath.Dir(s.cfg.Path)
	}
	file, err := os.Open(filepath.Join(baseDir, relativePath))
	if err != nil {
		return err
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil {
		return err
	} else if byteCount > 0 && info.Size() != int64(byteCount) {
		return fmt.Errorf("canonical message artifact size mismatch")
	}
	hasher := sha256.New()
	reader := io.TeeReader(&contextBoundReader{ctx: ctx, reader: file}, hasher)
	if err := visit(reader); err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return err
	}
	if digest != "" && hex.EncodeToString(hasher.Sum(nil)) != digest {
		return fmt.Errorf("canonical message artifact checksum mismatch")
	}
	return nil
}

type contextBoundReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextBoundReader) Read(payload []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(payload)
}

func (s *SQLiteSessionStorage) readCanonicalPayload(inline []byte, artifact sql.NullString, byteCount int, digest string) ([]byte, error) {
	if len(inline) > 0 {
		if err := validateCanonicalPayload(inline, byteCount, digest); err != nil {
			return nil, err
		}
		return inline, nil
	}
	if !artifact.Valid || strings.TrimSpace(artifact.String) == "" {
		return nil, fmt.Errorf("canonical message payload is missing")
	}
	baseDir := s.cfg.Dir
	if strings.TrimSpace(baseDir) == "" {
		baseDir = filepath.Dir(s.cfg.Path)
	}
	payload, err := os.ReadFile(filepath.Join(baseDir, artifact.String))
	if err != nil {
		return nil, fmt.Errorf("read canonical message artifact: %w", err)
	}
	if err := validateCanonicalPayload(payload, byteCount, digest); err != nil {
		return nil, err
	}
	return payload, nil
}

func validateCanonicalPayload(payload []byte, byteCount int, digest string) error {
	if byteCount > 0 && len(payload) != byteCount {
		return fmt.Errorf("canonical message payload size mismatch")
	}
	sum := sha256.Sum256(payload)
	if digest != "" && hex.EncodeToString(sum[:]) != digest {
		return fmt.Errorf("canonical message payload checksum mismatch")
	}
	return nil
}

func (s *SQLiteSessionStorage) GetMessages(ctx context.Context, sessionID string) ([]interface{}, error) {
	messages, err := s.GetRecentMessages(ctx, sessionID, s.cfg.HotHistoryMessages)
	if err != nil {
		return nil, err
	}
	result := make([]interface{}, len(messages))
	for index := range messages {
		result[index] = messages[index]
	}
	return result, nil
}

func (s *SQLiteSessionStorage) List(ctx context.Context, userID string) ([]*Session, error) {
	return s.listMetadata(ctx, `WHERE user_id = ?`, []interface{}{userID}, 0, 0)
}

func (s *SQLiteSessionStorage) ListMetadataPage(ctx context.Context, userID string, limit, offset int) ([]*Session, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.listMetadata(ctx, `WHERE user_id = ?`, []interface{}{userID}, limit, offset)
}

func (s *SQLiteSessionStorage) ListWithState(ctx context.Context, userID string, state SessionState) ([]*Session, error) {
	return s.listMetadata(ctx, `WHERE user_id = ? AND state = ?`, []interface{}{userID, state}, 0, 0)
}

func (s *SQLiteSessionStorage) ListByTags(ctx context.Context, userID string, tags []string) ([]*Session, error) {
	if len(tags) == 0 {
		return nil, ErrInvalidTags
	}
	sessions, err := s.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	filtered := sessions[:0]
	for _, session := range sessions {
		matches := true
		for _, tag := range tags {
			if !session.HasTag(tag) {
				matches = false
				break
			}
		}
		if matches {
			filtered = append(filtered, session)
		}
	}
	return filtered, nil
}

func (s *SQLiteSessionStorage) ListAll(ctx context.Context, limit, offset int) ([]*Session, error) {
	return s.listMetadata(ctx, "", nil, limit, offset)
}

func (s *SQLiteSessionStorage) listMetadata(ctx context.Context, where string, args []interface{}, limit, offset int) ([]*Session, error) {
	query := `
		SELECT id, user_id, state, title, title_source, summary, message_count,
		       head_offset, tags_json, metadata_json, created_at, updated_at, expires_at
		FROM sessions ` + where + ` ORDER BY updated_at DESC, id ASC`
	if limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		args = append(args, limit, max(offset, 0))
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sqlite sessions: %w", err)
	}
	defer rows.Close()
	var sessions []*Session
	for rows.Next() {
		session, err := scanSQLiteSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan sqlite session listing: %w", err)
		}
		session.History = nil
		session.HistoryLoaded = false
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *SQLiteSessionStorage) ListPreviews(ctx context.Context, userID string, limit, offset int) ([]*SessionPreview, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, state, title, summary, message_count,
		       tags_json, created_at, updated_at
		FROM sessions WHERE user_id = ?
		ORDER BY updated_at DESC, id ASC LIMIT ? OFFSET ?
	`, userID, limit, max(offset, 0))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	previews := make([]*SessionPreview, 0, limit)
	for rows.Next() {
		var preview SessionPreview
		var state, createdAt, updatedAt string
		var tagsJSON []byte
		if err := rows.Scan(&preview.ID, &preview.UserID, &state, &preview.Title,
			&preview.Summary, &preview.MessageCount, &tagsJSON, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan sqlite session preview: %w", err)
		}
		preview.State = SessionState(state)
		preview.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		preview.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		if len(tagsJSON) > 0 {
			if err := json.Unmarshal(tagsJSON, &preview.Tags); err != nil {
				return nil, fmt.Errorf("decode sqlite session preview tags: %w", err)
			}
		}
		previews = append(previews, &preview)
	}
	return previews, rows.Err()
}

func (s *SQLiteSessionStorage) Delete(ctx context.Context, sessionID string) error {
	return s.deleteSession(ctx, sessionID, true)
}

func (s *SQLiteSessionStorage) deleteSession(ctx context.Context, sessionID string, reclaim bool) error {
	sessionID = sanitizeSessionID(sessionID)
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sessionID)
	if err != nil {
		return fmt.Errorf("delete sqlite session: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrSessionNotFound
	}
	baseDir := s.cfg.Dir
	if strings.TrimSpace(baseDir) == "" {
		baseDir = filepath.Dir(s.cfg.Path)
	}
	artifactRoot := filepath.Join(baseDir, "session-artifacts")
	artifactDir := filepath.Join(artifactRoot, sessionID)
	if !pathWithin(artifactRoot, artifactDir) {
		return fmt.Errorf("refusing to remove session artifact path outside artifact root")
	}
	if err := os.RemoveAll(artifactDir); err != nil {
		return fmt.Errorf("remove session artifacts: %w", err)
	}
	if reclaim {
		s.reclaimFreePages(ctx)
	}
	return nil
}

func (s *SQLiteSessionStorage) ClearMessages(ctx context.Context, sessionID string) error {
	sessionID = sanitizeSessionID(sessionID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin clear session history: %w", err)
	}
	defer tx.Rollback()
	exists, err := sessionExistsTx(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	if !exists {
		return ErrSessionNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_messages WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("clear canonical session messages: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_prompt_messages WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("clear prompt projection: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE sessions SET message_count = 0, head_offset = 0, summary = '', updated_at = ?
		WHERE id = ?
	`, time.Now().UTC().Format(time.RFC3339Nano), sessionID); err != nil {
		return fmt.Errorf("update cleared session metadata: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit clear session history: %w", err)
	}
	baseDir := s.cfg.Dir
	if strings.TrimSpace(baseDir) == "" {
		baseDir = filepath.Dir(s.cfg.Path)
	}
	root := filepath.Join(baseDir, "session-artifacts")
	target := filepath.Join(root, sessionID)
	if pathWithin(root, target) {
		_ = os.RemoveAll(target)
	}
	s.reclaimFreePages(ctx)
	return nil
}

func (s *SQLiteSessionStorage) reclaimFreePages(ctx context.Context) {
	if s == nil || s.db == nil {
		return
	}
	_, _ = s.db.ExecContext(ctx, "PRAGMA incremental_vacuum(256)")
}

func pathWithin(root, target string) bool {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absoluteTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(absoluteRoot, absoluteTarget)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (s *SQLiteSessionStorage) Close(ctx context.Context, sessionID string) error {
	session, err := s.Load(ctx, sessionID)
	if err != nil {
		return err
	}
	session.MarkClosed()
	return s.Update(ctx, session)
}

func (s *SQLiteSessionStorage) Cleanup(ctx context.Context, after time.Time) (int, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	threshold := after.UTC().Format(time.RFC3339Nano)
	removed := 0
	for {
		rows, err := s.db.QueryContext(ctx, `
			SELECT id FROM sessions
			WHERE (expires_at IS NOT NULL AND expires_at < ?)
			   OR (expires_at IS NULL AND updated_at < ?)
			ORDER BY updated_at ASC, id ASC
			LIMIT 128
		`, now, threshold)
		if err != nil {
			return removed, fmt.Errorf("query expired sessions: %w", err)
		}
		ids := make([]string, 0, 128)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return removed, err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return removed, err
		}
		if len(ids) == 0 {
			break
		}
		for _, id := range ids {
			if err := s.deleteSession(ctx, id, false); err != nil && err != ErrSessionNotFound {
				return removed, err
			}
			removed++
		}
	}
	if removed > 0 {
		s.reclaimFreePages(ctx)
	}
	return removed, nil
}

func (s *SQLiteSessionStorage) ArchiveIdleSessions(ctx context.Context, before time.Time, batchSize int) (int, error) {
	if batchSize <= 0 || batchSize > 512 {
		batchSize = 128
	}
	archived := 0
	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	for {
		result, err := s.db.ExecContext(ctx, `
			UPDATE sessions
			SET state = ?, updated_at = ?
			WHERE id IN (
				SELECT id FROM sessions
				WHERE state = ? AND updated_at < ?
				ORDER BY updated_at ASC, id ASC
				LIMIT ?
			)
		`, StateIdle, updatedAt, StateActive, before.UTC().Format(time.RFC3339Nano), batchSize)
		if err != nil {
			return archived, fmt.Errorf("archive idle sqlite sessions: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return archived, err
		}
		archived += int(affected)
		if affected < int64(batchSize) {
			return archived, nil
		}
	}
}

func (s *SQLiteSessionStorage) GetStatistics(ctx context.Context, userID string) (*SessionStatistics, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT state, message_count, tags_json
		FROM sessions WHERE user_id = ?
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stats := &SessionStatistics{Tags: make(map[string]int)}
	for rows.Next() {
		var state SessionState
		var messageCount int
		var tagsJSON []byte
		if err := rows.Scan(&state, &messageCount, &tagsJSON); err != nil {
			return nil, fmt.Errorf("scan sqlite session statistics: %w", err)
		}
		stats.Total++
		stats.TotalMessages += messageCount
		switch state {
		case StateActive:
			stats.Active++
		case StateIdle:
			stats.Idle++
		case StateClosed:
			stats.Closed++
		case StateArchived:
			stats.Archived++
		}
		var tags []string
		if len(tagsJSON) > 0 {
			if err := json.Unmarshal(tagsJSON, &tags); err != nil {
				return nil, fmt.Errorf("decode sqlite session statistic tags: %w", err)
			}
		}
		for _, tag := range tags {
			stats.Tags[tag]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return stats, nil
}

type legacyHotItem struct {
	seq     int
	message types.Message
	size    int
}

type legacyHotCollector struct {
	store        *SQLiteSessionStorage
	instructions []legacyHotItem
	compaction   *legacyHotItem
	tail         []legacyHotItem
	tailBytes    int
}

// sessionDirMayContainLegacyJSON performs a cheap top-level scan. When the
// sessions directory has already been fully migrated (no remaining .json
// files), import is a no-op and we avoid even opening the directory repeatedly
// beyond this short ReadDir. This is the common case after the first SQLite
// migration.
func sessionDirMayContainLegacyJSON(dir string) (bool, error) {
	directory, err := os.Open(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer directory.Close()
	for {
		entries, readErr := directory.ReadDir(64)
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
				return true, nil
			}
		}
		if readErr == io.EOF {
			return false, nil
		}
		if readErr != nil {
			return false, readErr
		}
		if len(entries) == 0 {
			return false, nil
		}
	}
}

func (s *SQLiteSessionStorage) importLegacyJSONFiles(ctx context.Context) error {
	if strings.TrimSpace(s.cfg.Dir) == "" {
		return nil
	}
	mayContain, err := sessionDirMayContainLegacyJSON(s.cfg.Dir)
	if err != nil {
		return fmt.Errorf("scan legacy session directory: %w", err)
	}
	if !mayContain {
		return nil
	}
	directory, err := os.Open(s.cfg.Dir)
	if err != nil {
		return fmt.Errorf("scan legacy session directory: %w", err)
	}
	defer directory.Close()
	for {
		entries, readErr := directory.ReadDir(128)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
				continue
			}
			sessionID := sanitizeSessionID(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
			if sessionID == "" {
				continue
			}
			var exists int
			err := s.db.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ?`, sessionID).Scan(&exists)
			if err == nil {
				continue
			}
			if err != sql.ErrNoRows {
				return fmt.Errorf("check legacy session import: %w", err)
			}
			if err := s.importLegacyJSONFile(ctx, filepath.Join(s.cfg.Dir, entry.Name()), sessionID); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("scan legacy session directory: %w", readErr)
		}
	}
}

func (s *SQLiteSessionStorage) importLegacyJSONFile(ctx context.Context, path, sessionID string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open legacy session %s: %w", path, err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("decode legacy session %s: expected JSON object", path)
	}
	now := time.Now()
	session := &Session{
		ID:            sessionID,
		State:         StateActive,
		CreatedAt:     now,
		UpdatedAt:     now,
		HistoryLoaded: true,
		Metadata: SessionMetadata{
			Tags:    []string{},
			Context: make(map[string]interface{}),
		},
	}
	ctx, tx, tracker, err := s.beginWriteTx(ctx)
	if err != nil {
		return fmt.Errorf("begin legacy session import: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			s.rollbackWriteTx(tx, tracker)
		}
	}()
	if err := s.upsertSessionMetadataTx(ctx, tx, session, 0); err != nil {
		return err
	}
	collector := &legacyHotCollector{store: s}
	messageCount := 0
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode legacy session field: %w", err)
		}
		name, _ := nameToken.(string)
		switch name {
		case "id":
			var ignored string
			err = decoder.Decode(&ignored)
		case "userId":
			err = decoder.Decode(&session.UserID)
		case "state":
			err = decoder.Decode(&session.State)
		case "headOffset":
			err = decoder.Decode(&session.HeadOffset)
		case "metadata":
			err = decoder.Decode(&session.Metadata)
		case "createdAt":
			err = decoder.Decode(&session.CreatedAt)
		case "updatedAt":
			err = decoder.Decode(&session.UpdatedAt)
		case "expiresAt":
			err = decoder.Decode(&session.ExpiresAt)
		case "history":
			err = s.decodeLegacyHistory(ctx, decoder, tx, sessionID, collector, &messageCount)
		default:
			var ignored json.RawMessage
			err = decoder.Decode(&ignored)
		}
		if err != nil {
			return fmt.Errorf("decode legacy session %s field %s: %w", path, name, err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("finish legacy session %s: %w", path, err)
	}
	session.CanonicalMessageCount = messageCount
	session.Metadata.TotalTurns = messageCount
	projection, err := collector.projection()
	if err != nil {
		return err
	}
	if err := replacePromptMessagesTx(ctx, tx, sessionID, projection); err != nil {
		return err
	}
	if err := s.upsertSessionMetadataTx(ctx, tx, session, messageCount); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit legacy session import %s: %w", path, err)
	}
	committed = true
	return nil
}

func (s *SQLiteSessionStorage) decodeLegacyHistory(ctx context.Context, decoder *json.Decoder, tx *sql.Tx, sessionID string, collector *legacyHotCollector, count *int) error {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return fmt.Errorf("expected history array")
	}
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var message types.Message
		if err := decoder.Decode(&message); err != nil {
			return err
		}
		(*count)++
		if err := s.insertCanonicalMessageTx(ctx, tx, sessionID, *count, message); err != nil {
			return err
		}
		if err := collector.add(*count, message); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func (c *legacyHotCollector) add(seq int, message types.Message) error {
	encoded, err := c.store.encodeHotMessage(message)
	if err != nil {
		return err
	}
	item := legacyHotItem{seq: seq, message: encoded.message, size: encoded.size}
	role := strings.ToLower(strings.TrimSpace(message.Role))
	if role == "system" || role == "developer" {
		c.instructions = append(c.instructions, item)
		if len(c.instructions) > 16 {
			c.instructions = append([]legacyHotItem(nil), c.instructions[len(c.instructions)-16:]...)
		}
	}
	if strings.EqualFold(message.Metadata.GetString("context_stage", ""), "compaction") {
		copyItem := item
		c.compaction = &copyItem
	}
	c.tail = append(c.tail, item)
	c.tailBytes += item.size
	for len(c.tail) > c.store.cfg.HotHistoryMessages || c.tailBytes > c.store.cfg.HotHistoryBytes {
		if len(c.tail) <= 1 {
			break
		}
		c.tailBytes -= c.tail[0].size
		c.tail[0] = legacyHotItem{}
		c.tail = c.tail[1:]
	}
	return nil
}

func (c *legacyHotCollector) projection() ([]encodedSessionMessage, error) {
	bySequence := make(map[int]types.Message)
	for _, item := range c.instructions {
		bySequence[item.seq] = item.message
	}
	if c.compaction != nil {
		bySequence[c.compaction.seq] = c.compaction.message
	}
	for _, item := range c.tail {
		bySequence[item.seq] = item.message
	}
	sequences := make([]int, 0, len(bySequence))
	for sequence := range bySequence {
		sequences = append(sequences, sequence)
	}
	sort.Ints(sequences)
	messages := make([]types.Message, 0, len(sequences))
	for _, sequence := range sequences {
		messages = append(messages, bySequence[sequence])
	}
	return c.store.buildHotProjection(messages)
}
