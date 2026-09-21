package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/migrate"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// defaultQueryLimit 是查询的兜底上限：Limit <= 0 时使用。
const defaultQueryLimit = 50

// sqliteStore 是 Store 的 SQLite 实现。
//
// 写路径统一走 execWrite（IMMEDIATE 事务 + 锁重试），读路径直接查询。
// readOnly 角色（另一个进程持有 owner 锁）下所有写方法返回 ErrReadOnlyStore，
// 而不是静默失败——静默失败会让 shadow 指标失真。
type sqliteStore struct {
	db       *sql.DB
	path     string
	readOnly bool

	closeOnce sync.Once
	closeErr  error
}

// OpenStore 打开（必要时创建并迁移）knowledge.db。
//
// readOnly 为 true 时以 SQLite 只读方式打开并跳过迁移：读者永不写库，
// 因此不会与 owner 争锁（ADR-0001 单写者 / 多读者）。
func OpenStore(ctx context.Context, dsn string, readOnly bool) (Store, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, errors.New("knowledge: store path is empty")
	}
	if readOnly {
		return openReadOnlyStore(ctx, dsn)
	}
	if !sqliteutil.IsMemoryDSN(dsn) {
		if dir := filepath.Dir(dsn); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("knowledge: create store directory: %w", err)
			}
		}
	}
	db, err := sqliteutil.OpenFileCtx(ctx, dsn, true)
	if err != nil {
		return nil, fmt.Errorf("knowledge: open store: %w", err)
	}
	store := &sqliteStore{db: db, path: dsn}
	if err := store.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// openReadOnlyStore 先尝试 SQLite 只读 URI，失败则回退到普通打开（仍不写）。
//
// 回退路径存在的原因：部分平台/VFS 组合不支持 mode=ro URI，此时宁可打开
// 可写句柄也不让读者不可用——写方法仍由 readOnly 标志拦下。
func openReadOnlyStore(ctx context.Context, dsn string) (Store, error) {
	if !sqliteutil.IsMemoryDSN(dsn) {
		roDSN := "file:" + filepath.ToSlash(dsn) + "?mode=ro"
		if db, err := sqliteutil.OpenFileCtx(ctx, roDSN, false); err == nil {
			store := &sqliteStore{db: db, path: dsn, readOnly: true}
			if err := store.verifyInitialized(ctx); err != nil {
				_ = db.Close()
				return nil, err
			}
			return store, nil
		}
	}
	db, err := sqliteutil.OpenFileCtx(ctx, dsn, false)
	if err != nil {
		return nil, fmt.Errorf("knowledge: open store (read-only fallback): %w", err)
	}
	store := &sqliteStore{db: db, path: dsn, readOnly: true}
	if err := store.verifyInitialized(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// init 施加 schema PRAGMA 并应用迁移（幂等）。
func (s *sqliteStore) init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("knowledge: enable foreign keys: %w", err)
	}
	migrations, err := Migrations()
	if err != nil {
		return err
	}
	if err := migrate.Apply(ctx, s.db, migrations); err != nil {
		return fmt.Errorf("knowledge: apply migrations: %w", err)
	}
	return nil
}

// verifyInitialized 确认只读打开的库确实已经建好 schema。
func (s *sqliteStore) verifyInitialized(ctx context.Context) error {
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if version == 0 {
		return errors.New("knowledge: store is not initialized (no applied migrations)")
	}
	return nil
}

// SchemaVersion 返回 schema_migrations 的最高版本；未初始化的库返回 0。
func (s *sqliteStore) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version)
	if err != nil {
		if isMissingTable(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("knowledge: read schema version: %w", err)
	}
	return version, nil
}

// EnsureWorkspace 以 upsert 语义登记工作区，返回既有或新建的 id。
//
// 先按 root_path 查既有行，避免 UNIQUE(root_path) 与调用方传入的 id 冲突：
// 同一工作区被两个不同 id 路径注册时，以磁盘上的既有行为准。
func (s *sqliteStore) EnsureWorkspace(ctx context.Context, ws Workspace) (string, error) {
	if strings.TrimSpace(ws.RootPath) == "" {
		return "", errors.New("knowledge: workspace root path is required")
	}
	if ws.ID == "" {
		ws.ID = WorkspaceID(ws.RootPath)
	}
	if err := s.execWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var existing string
		err := tx.QueryRowContext(ctx, `SELECT id FROM workspaces WHERE root_path = ?`, ws.RootPath).Scan(&existing)
		switch {
		case err == nil:
			ws.ID = existing
			_, err = tx.ExecContext(ctx, `UPDATE workspaces SET updated_at = ? WHERE id = ?`, unixMillis(ws.UpdatedAt), ws.ID)
			return err
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.ExecContext(ctx,
				`INSERT INTO workspaces (id, root_path, created_at, updated_at) VALUES (?, ?, ?, ?)`,
				ws.ID, ws.RootPath, unixMillis(ws.CreatedAt), unixMillis(ws.UpdatedAt))
			return err
		default:
			return err
		}
	}); err != nil {
		return "", err
	}
	return ws.ID, nil
}

// UpsertFile 记录文件身份与内容哈希。
func (s *sqliteStore) UpsertFile(ctx context.Context, rec FileRecord) (string, error) {
	if rec.WorkspaceID == "" || strings.TrimSpace(rec.Path) == "" {
		return "", errors.New("knowledge: file record requires workspace_id and path")
	}
	rec.Path = normalizeRelPath(rec.Path)
	if rec.ID == "" {
		rec.ID = FileID(rec.WorkspaceID, rec.Path)
	}
	if rec.IndexState == "" {
		rec.IndexState = IndexUnknown
	}
	if rec.IndexedAt.IsZero() {
		rec.IndexedAt = time.Now()
	}
	err := s.execWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO files (id, workspace_id, path, language, size, mtime_ns, content_hash,
			                   is_test, is_generated, index_state, indexed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				language     = excluded.language,
				size         = excluded.size,
				mtime_ns     = excluded.mtime_ns,
				content_hash = excluded.content_hash,
				is_test      = excluded.is_test,
				is_generated = excluded.is_generated,
				index_state  = excluded.index_state,
				indexed_at   = excluded.indexed_at
		`,
			rec.ID, rec.WorkspaceID, rec.Path, rec.Language, rec.Size, rec.MTimeNS, rec.ContentHash,
			boolToInt(rec.IsTest), boolToInt(rec.IsGenerated), string(rec.IndexState), unixMillis(rec.IndexedAt))
		return err
	})
	if err != nil {
		return "", err
	}
	return rec.ID, nil
}

// DeleteFile 删除文件及其派生行（外键 CASCADE）。
func (s *sqliteStore) DeleteFile(ctx context.Context, workspaceID, path string) error {
	return s.execWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM files WHERE workspace_id = ? AND path = ?`,
			workspaceID, normalizeRelPath(path))
		return err
	})
}

// FileByPath 返回已存储的文件记录；ok=false 表示未知。
func (s *sqliteStore) FileByPath(ctx context.Context, workspaceID, path string) (FileRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, workspace_id, path, COALESCE(language, ''), size, COALESCE(mtime_ns, 0),
		       content_hash, is_test, is_generated, index_state, COALESCE(indexed_at, 0)
		FROM files WHERE workspace_id = ? AND path = ?
	`, workspaceID, normalizeRelPath(path))
	rec, err := scanFile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return FileRecord{}, false, nil
	}
	if err != nil {
		return FileRecord{}, false, fmt.Errorf("knowledge: read file record: %w", err)
	}
	return rec, true, nil
}

// ReplaceSymbols 原子替换一个文件的符号集合（FTS 触发器随之同步）。
//
// stable_key 是 workspace 级唯一身份（idx_symbols_stable）。正常情况下
// namespace 分量保证同 workspace 内不撞键；一旦仍然撞上，说明两个文件产出了
// 同一个身份——这正是 04 §4.4 定义的"adapter 缺陷"。此时不能整份符号写入失败
// （那会连 refs 一起丢掉），而是保留该身份行、把宿主改为本次文件，并把冲突记进
// invalidation_events（reason=adapter_conflict），让状态面能解释这份降级。
func (s *sqliteStore) ReplaceSymbols(ctx context.Context, fileID string, syms []Symbol) error {
	if strings.TrimSpace(fileID) == "" {
		return errors.New("knowledge: ReplaceSymbols requires file_id")
	}
	return s.execWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM symbols WHERE file_id = ?`, fileID); err != nil {
			return err
		}
		var (
			conflicts  []string
			conflictWS string
		)
		for _, sym := range syms {
			if sym.ID == "" {
				sym.ID = SymbolID(sym.WorkspaceID, sym.StableKey)
			}
			if sym.Kind == "" {
				sym.Kind = SymbolUnknown
			}
			taken, err := symbolIdentityTaken(ctx, tx, sym)
			if err != nil {
				return err
			}
			if taken {
				conflicts = append(conflicts, sym.StableKey)
				conflictWS = sym.WorkspaceID
				if err := updateSymbolByIdentity(ctx, tx, fileID, sym); err != nil {
					return err
				}
				continue
			}
			if err := insertSymbol(ctx, tx, fileID, sym); err != nil {
				return err
			}
		}
		if len(conflicts) > 0 {
			return recordAdapterConflict(ctx, tx, conflictWS, fileID, conflicts)
		}
		return nil
	})
}

// symbolIdentityTaken 报告 (workspace_id, stable_key) 是否已被别的文件占用。
// 走 idx_symbols_stable 唯一索引，代价是一次索引查找。
func symbolIdentityTaken(ctx context.Context, tx *sql.Tx, sym Symbol) (bool, error) {
	var existing string
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM symbols WHERE workspace_id = ? AND stable_key = ?`,
		sym.WorkspaceID, sym.StableKey).Scan(&existing)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, err
	}
}

// insertSymbol 写入一行符号。
func insertSymbol(ctx context.Context, tx *sql.Tx, fileID string, sym Symbol) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO symbols (id, workspace_id, file_id, stable_key, name, qualified_name, kind,
		                     language, owner_symbol_id, signature, signature_hash, content_hash,
		                     start_line, start_col, end_line, end_col, is_exported, is_test, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		sym.ID, sym.WorkspaceID, fileID, sym.StableKey, sym.Name, sym.QualifiedName, string(sym.Kind),
		sym.Language, nullIfEmpty(sym.OwnerSymbolID), nullIfEmpty(sym.Signature), nullIfEmpty(sym.SignatureHash),
		nullIfEmpty(sym.ContentHash), sym.Range.Start.Line, sym.Range.Start.Column,
		sym.Range.End.Line, sym.Range.End.Column, boolToInt(sym.IsExported), boolToInt(sym.IsTest),
		nullIfZeroInt64(sym.DeletedAt))
	return err
}

// updateSymbolByIdentity 把已存在的同身份行改挂到本次文件（id 由 stable_key 派生，
// 因此两边的主键必然一致，refs 指向的 symbol_id 不会失效）。
func updateSymbolByIdentity(ctx context.Context, tx *sql.Tx, fileID string, sym Symbol) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE symbols SET file_id = ?, name = ?, qualified_name = ?, kind = ?, language = ?,
		                   signature = ?, signature_hash = ?, content_hash = ?,
		                   start_line = ?, start_col = ?, end_line = ?, end_col = ?,
		                   is_exported = ?, is_test = ?, deleted_at = ?
		WHERE workspace_id = ? AND stable_key = ?
	`,
		fileID, sym.Name, sym.QualifiedName, string(sym.Kind), sym.Language,
		nullIfEmpty(sym.Signature), nullIfEmpty(sym.SignatureHash), nullIfEmpty(sym.ContentHash),
		sym.Range.Start.Line, sym.Range.Start.Column, sym.Range.End.Line, sym.Range.End.Column,
		boolToInt(sym.IsExported), boolToInt(sym.IsTest), nullIfZeroInt64(sym.DeletedAt),
		sym.WorkspaceID, sym.StableKey)
	return err
}

// recordAdapterConflict 记录一次 stable_key 冲突（04 §4.4 的歧义规则）。
// 一次 ReplaceSymbols 最多记一条事件，避免同一份缺陷在状态面上刷屏。
func recordAdapterConflict(ctx context.Context, tx *sql.Tx, workspaceID, fileID string, stableKeys []string) error {
	const maxSampleKeys = 5
	sample := stableKeys
	if len(sample) > maxSampleKeys {
		sample = sample[:maxSampleKeys]
	}
	scope, err := json.Marshal(map[string]any{
		"file_id":     fileID,
		"conflicts":   len(stableKeys),
		"stable_keys": sample,
	})
	if err != nil {
		return err
	}
	now := time.Now()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO invalidation_events (id, workspace_id, reason, scope_json, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, EventID(workspaceID, InvalidationReasonAdapterConflict, now.UnixNano()), workspaceID,
		InvalidationReasonAdapterConflict, string(scope), now.UnixNano())
	return err
}

// ReplaceRefs 原子替换一个文件的出向引用集合。
func (s *sqliteStore) ReplaceRefs(ctx context.Context, fileID string, refs []Reference) error {
	if strings.TrimSpace(fileID) == "" {
		return errors.New("knowledge: ReplaceRefs requires file_id")
	}
	return s.execWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM refs WHERE file_id = ?`, fileID); err != nil {
			return err
		}
		for _, ref := range refs {
			if ref.ID == "" {
				ref.ID = RefID(ref.WorkspaceID, fileID, ref.Line, ref.Col, ref.Kind)
			}
			if ref.Kind == "" {
				ref.Kind = RefReference
			}
			if ref.Source == "" {
				ref.Source = SourceBuiltin
			}
			if ref.Confidence <= 0 {
				ref.Confidence = ConfidenceFromSource(ref.Source).Score()
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO refs (id, workspace_id, from_symbol_id, to_symbol_id, to_symbol_name, to_symbol_version,
				                  kind, file_id, line, col, snippet, confidence, source)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`,
				ref.ID, ref.WorkspaceID, nullIfEmpty(ref.FromSymbolID), nullIfEmpty(ref.ToSymbolID),
				nullIfEmpty(ref.ToSymbolName), nullIfZeroInt(ref.ToSymbolVersion), string(ref.Kind), fileID, ref.Line, ref.Col,
				nullIfEmpty(ref.Snippet), ref.Confidence, string(ref.Source)); err != nil {
				return err
			}
		}
		return nil
	})
}

// execWrite 在 IMMEDIATE 事务中执行写操作，并对 SQLITE_BUSY 做有界重试。
//
// 只读角色直接返回 ErrReadOnlyStore；这是刻意的硬失败：让 shadow 指标
// 记录"读者试图写入"的异常，而不是悄悄吞掉。
func (s *sqliteStore) execWrite(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	if s.readOnly {
		return ErrReadOnlyStore
	}
	return sqliteutil.RetryLockedCtx(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, sqliteutil.WriteTxOptions)
		if err != nil {
			return err
		}
		if err := fn(ctx, tx); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	})
}

// Close 释放句柄；幂等。
func (s *sqliteStore) Close() error {
	s.closeOnce.Do(func() {
		if s.db != nil {
			s.closeErr = s.db.Close()
		}
	})
	return s.closeErr
}
