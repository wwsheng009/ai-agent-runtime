package knowledge

// cache_entries 的 SQLite 读写（06 §4 Phase 6 切片 2）。
//
// 不变量：
//   - 写路径统一走 execWrite（IMMEDIATE 事务 + 锁重试）；reader 角色硬失败
//     ErrReadOnlyStore（CompileCache.Do 会降级为直算并计入 Errors）；
//   - 唯一键 (workspace_id, cache_type, cache_key) 由 idx_cache_key 保证；
//     写入以 ON CONFLICT 做 upsert，主键由 CacheEntryID 稳定派生；
//   - 读路径只用 s.db（reader 可用）；expires_at 为空表示永不过期。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 编译期断言：sqliteStore 直接满足 compile 层缓存的窄接口。
var _ CompileCacheStore = (*sqliteStore)(nil)

// GetCacheEntry 按唯一键读取缓存条目；不存在返回 (零值, false, nil)。
func (s *sqliteStore) GetCacheEntry(ctx context.Context, workspaceID, cacheType, cacheKey string) (CacheEntry, bool, error) {
	if s == nil || s.db == nil {
		return CacheEntry{}, false, errors.New("knowledge: get cache entry: store is not open")
	}
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(cacheKey) == "" {
		return CacheEntry{}, false, errors.New("knowledge: get cache entry: workspace_id and cache_key are required")
	}
	if !ValidCacheType(cacheType) {
		return CacheEntry{}, false, fmt.Errorf("knowledge: get cache entry: invalid cache_type %q", cacheType)
	}
	var (
		entry     CacheEntry
		createdMS int64
		expiresMS int64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, workspace_id, cache_key, cache_type, payload_json, knowledge_version,
		       created_at, COALESCE(expires_at, 0)
		FROM cache_entries
		WHERE workspace_id = ? AND cache_type = ? AND cache_key = ?
	`, workspaceID, cacheType, cacheKey).Scan(
		&entry.ID, &entry.WorkspaceID, &entry.CacheKey, &entry.CacheType, &entry.PayloadJSON,
		&entry.KnowledgeVersion, &createdMS, &expiresMS,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CacheEntry{}, false, nil
		}
		return CacheEntry{}, false, fmt.Errorf("knowledge: get cache entry: %w", err)
	}
	entry.CreatedAt = timeFromUnixMillis(createdMS)
	entry.ExpiresAt = timeFromUnixMillis(expiresMS)
	return entry, true, nil
}

// PutCacheEntry 以 upsert 语义写入缓存条目（同键覆盖）。
func (s *sqliteStore) PutCacheEntry(ctx context.Context, entry CacheEntry) error {
	if s == nil || s.db == nil {
		return errors.New("knowledge: put cache entry: store is not open")
	}
	if err := entry.Validate(); err != nil {
		return fmt.Errorf("knowledge: put cache entry: %w", err)
	}
	if strings.TrimSpace(entry.ID) == "" {
		entry.ID = CacheEntryID(entry.WorkspaceID, entry.CacheType, entry.CacheKey)
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	err := s.execWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO cache_entries
				(id, workspace_id, cache_key, cache_type, payload_json, knowledge_version, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(workspace_id, cache_type, cache_key) DO UPDATE SET
				payload_json = excluded.payload_json,
				knowledge_version = excluded.knowledge_version,
				created_at = excluded.created_at,
				expires_at = excluded.expires_at
		`,
			entry.ID, entry.WorkspaceID, entry.CacheKey, entry.CacheType, entry.PayloadJSON,
			entry.KnowledgeVersion, unixMillis(entry.CreatedAt), nullIfZeroInt64(unixMillis(entry.ExpiresAt)))
		return err
	})
	if err != nil {
		return fmt.Errorf("knowledge: put cache entry: %w", err)
	}
	return nil
}

// DeleteCacheEntry 删除一条缓存条目；不存在不是错误（幂等）。
func (s *sqliteStore) DeleteCacheEntry(ctx context.Context, workspaceID, cacheType, cacheKey string) error {
	if s == nil || s.db == nil {
		return errors.New("knowledge: delete cache entry: store is not open")
	}
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(cacheKey) == "" {
		return errors.New("knowledge: delete cache entry: workspace_id and cache_key are required")
	}
	if !ValidCacheType(cacheType) {
		return fmt.Errorf("knowledge: delete cache entry: invalid cache_type %q", cacheType)
	}
	err := s.execWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM cache_entries WHERE workspace_id = ? AND cache_type = ? AND cache_key = ?`,
			workspaceID, cacheType, cacheKey)
		return err
	})
	if err != nil {
		return fmt.Errorf("knowledge: delete cache entry: %w", err)
	}
	return nil
}

// PurgeExpiredCacheEntries 清理 expires_at 已过的条目，最多 limit 条；返回清理条数。
//
// expires_at 为空（NULL）的条目永不清理；now 为零值时取当前时刻。
func (s *sqliteStore) PurgeExpiredCacheEntries(ctx context.Context, now time.Time, limit int) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("knowledge: purge expired cache entries: store is not open")
	}
	if now.IsZero() {
		now = time.Now()
	}
	if limit <= 0 {
		limit = defaultQueryLimit
	}
	removed := int64(0)
	err := s.execWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			DELETE FROM cache_entries
			WHERE id IN (
				SELECT id FROM cache_entries
				WHERE expires_at IS NOT NULL AND expires_at <= ?
				LIMIT ?
			)
		`, unixMillis(now), limit)
		if err != nil {
			return err
		}
		removed, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("knowledge: purge expired cache entries: %w", err)
	}
	return int(removed), nil
}
