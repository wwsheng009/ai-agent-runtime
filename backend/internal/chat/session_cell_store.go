package chat

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

// session_cell_store.go —— C 档「写穿冷 cell body」的落盘存储（任务 T1 / Spike-1）。
//
// 设计文档：docs/plan/aicli-transcript-residency-and-lazy-history-design-20261020.md §14.1(L3)/§14.2
//
// 本存储只保存**不透明 payload + 元数据**：scene 序列化由 UI 层负责
// （cmd/aicli/ui.CellBodyStore 适配器），internal/chat 不反向依赖 UI 类型。
//
// 表结构（PK = session_id + cell_id + revision）：
//
//	session_cells(session_id, cell_id, revision, source_fp, rows_at_gen, gen,
//	              encoding, payload BLOB, created_at)
//
// 语义要点：
//   - 写穿发生在逐出之前（设计 §14.3）：Put 成功才允许把 cell 降级为索引；
//   - Get 用于冷区回填（§14.5）；miss 由调用方 fail-closed 处理；
//   - 单连接（MaxOpenConns=1）与既有 store 口径一致，避免同库多连接自锁。
type SessionCellStore struct {
	db         *sql.DB
	fileBacked bool

	puts   atomic.Uint64
	gets   atomic.Uint64
	misses atomic.Uint64
	errors atomic.Uint64
}

// SessionCellRow 是一条写穿记录。
type SessionCellRow struct {
	SessionID string
	CellID    int64
	Revision  uint64
	SourceFP  uint64
	RowsAtGen int32
	Gen       uint64
	Encoding  string
	Payload   []byte
	CreatedAt time.Time
}

const sessionCellStoreTableSQL = `
CREATE TABLE IF NOT EXISTS session_cells (
    session_id  TEXT    NOT NULL,
    cell_id     INTEGER NOT NULL,
    revision    INTEGER NOT NULL,
    source_fp   INTEGER NOT NULL,
    rows_at_gen INTEGER NOT NULL,
    gen         INTEGER NOT NULL,
    encoding    TEXT    NOT NULL,
    payload     BLOB    NOT NULL,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (session_id, cell_id, revision)
)`

const sessionCellStoreIndexSQL = `
CREATE INDEX IF NOT EXISTS idx_session_cells_session
    ON session_cells(session_id, cell_id)`

// OpenSessionCellStore 打开（或创建）写穿存储。
//
// path 支持 ":memory:"（测试用）；busyTimeout <=0 时取 5000ms；cacheKiB <=0 时取 2048。
func OpenSessionCellStore(path string, busyTimeout time.Duration, cacheKiB int) (*SessionCellStore, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, fmt.Errorf("session cell store: empty path")
	}
	if busyTimeout <= 0 {
		busyTimeout = 5 * time.Second
	}
	if cacheKiB <= 0 {
		cacheKiB = 2048
	}
	fileBacked := trimmed != ":memory:" && !strings.Contains(trimmed, "mode=memory")
	db, err := sql.Open("sqlite3", trimmed)
	if err != nil {
		return nil, fmt.Errorf("session cell store: open %q: %w", trimmed, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &SessionCellStore{db: db, fileBacked: fileBacked}
	pragmas := []string{
		fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeout.Milliseconds()),
		"PRAGMA synchronous=NORMAL",
		fmt.Sprintf("PRAGMA cache_size=-%d", cacheKiB),
	}
	if fileBacked {
		pragmas = append(pragmas, "PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=256")
	}
	for _, statement := range pragmas {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("session cell store: pragma %q: %w", statement, err)
		}
	}
	for _, ddl := range []string{sessionCellStoreTableSQL, sessionCellStoreIndexSQL} {
		if _, err := db.Exec(ddl); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("session cell store: schema: %w", err)
		}
	}
	return store, nil
}

// PutCell 写入（或覆盖同一身份）一条写穿记录。payload 必须非空。
func (s *SessionCellStore) PutCell(ctx context.Context, row SessionCellRow) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("session cell store: not initialized")
	}
	if strings.TrimSpace(row.SessionID) == "" {
		s.puts.Add(1)
		s.errors.Add(1)
		return fmt.Errorf("session cell store: empty session id")
	}
	if len(row.Payload) == 0 {
		s.puts.Add(1)
		s.errors.Add(1)
		return fmt.Errorf("session cell store: cell %d revision %d has empty payload", row.CellID, row.Revision)
	}
	createdAt := row.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO session_cells
		   (session_id, cell_id, revision, source_fp, rows_at_gen, gen, encoding, payload, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.SessionID, row.CellID, int64(row.Revision), int64(row.SourceFP),
		int64(row.RowsAtGen), int64(row.Gen), row.Encoding, row.Payload, createdAt.UnixNano())
	if err != nil {
		s.errors.Add(1)
		s.puts.Add(1)
		return fmt.Errorf("session cell store: put cell %d: %w", row.CellID, err)
	}
	s.puts.Add(1)
	return nil
}

// GetCell 按身份读取一条记录；miss 返回 (zero, false, nil)。
func (s *SessionCellStore) GetCell(ctx context.Context, sessionID string, cellID int64, revision uint64) (SessionCellRow, bool, error) {
	if s == nil || s.db == nil {
		return SessionCellRow{}, false, fmt.Errorf("session cell store: not initialized")
	}
	s.gets.Add(1)
	var (
		row       SessionCellRow
		revisionV int64
		sourceFP  int64
		rowsAtGen int64
		genV      int64
		createdAt int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT cell_id, revision, source_fp, rows_at_gen, gen, encoding, payload, created_at
		   FROM session_cells WHERE session_id = ? AND cell_id = ? AND revision = ?`,
		sessionID, cellID, int64(revision)).
		Scan(&row.CellID, &revisionV, &sourceFP, &rowsAtGen, &genV, &row.Encoding, &row.Payload, &createdAt)
	if err == sql.ErrNoRows {
		s.misses.Add(1)
		return SessionCellRow{}, false, nil
	}
	if err != nil {
		s.errors.Add(1)
		return SessionCellRow{}, false, fmt.Errorf("session cell store: get cell %d: %w", cellID, err)
	}
	row.SessionID = sessionID
	row.Revision = uint64(revisionV)
	row.SourceFP = uint64(sourceFP)
	row.RowsAtGen = int32(rowsAtGen)
	row.Gen = uint64(genV)
	row.CreatedAt = time.Unix(0, createdAt)
	return row, true, nil
}

// SessionCellCount 返回会话的写穿记录数（诊断/证据用）。
func (s *SessionCellStore) SessionCellCount(ctx context.Context, sessionID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("session cell store: not initialized")
	}
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_cells WHERE session_id = ?`, sessionID).Scan(&count); err != nil {
		s.errors.Add(1)
		return 0, fmt.Errorf("session cell store: count: %w", err)
	}
	return count, nil
}

// SessionCellStoreStats 是写穿存储的计数器快照（映射到 residency 观测口径）。
type SessionCellStoreStats struct {
	Puts   uint64
	Gets   uint64
	Misses uint64
	Errors uint64
}

func (s *SessionCellStore) Stats() SessionCellStoreStats {
	if s == nil {
		return SessionCellStoreStats{}
	}
	return SessionCellStoreStats{
		Puts:   s.puts.Load(),
		Gets:   s.gets.Load(),
		Misses: s.misses.Load(),
		Errors: s.errors.Load(),
	}
}

// Close 关闭底层连接。
func (s *SessionCellStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
