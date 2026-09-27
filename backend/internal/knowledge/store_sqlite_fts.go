package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// 本文件负责 symbols_fts 的"尽力而为"生命周期：FTS5 可用时建表并挂同步
// 触发器，不可用时安静降级（Search 退化为 LIKE 扫描，见 store_sqlite_read.go）。
//
// 为什么不在迁移里建 FTS5：FTS5 是 SQLite 的可选模块。部分构建（例如纯 Go 的
// ncruces/go-sqlite3，未编译 fts5）执行 `CREATE VIRTUAL TABLE ... USING fts5`
// 会返回 "no such module: fts5"，若它出现在迁移 SQL 里，整个迁移连同
// knowledge.db 的打开都会失败。artifact/catalog 两个 store 的既有做法就是
// "建 FTS 失败 → 关掉 FTS、走 LIKE"，这里保持同一口径。

// symbolsFTSColumns 是 symbols_fts 索引的列集，必须与迁移里的 symbols 表列名
// 一致（external-content 表要求列名可映射）。
const symbolsFTSColumns = `name, qualified_name, signature`

// createSymbolsFTSSQL 创建 external-content 的 symbols_fts。
const createSymbolsFTSSQL = `
	CREATE VIRTUAL TABLE IF NOT EXISTS symbols_fts USING fts5(
		` + symbolsFTSColumns + `,
		content='symbols', content_rowid='rowid',
		tokenize='unicode61'
	)
`

// symbolsFTSIndexSQL 是 external-content 表的存量重建命令（'rebuild' 从
// symbols 表重新灌索引）。
const symbolsFTSIndexSQL = `INSERT INTO symbols_fts(symbols_fts) VALUES('rebuild')`

// symbolsFTSTriggerSQL 是 external-content 同步触发器：symbols 的任何写入都
// 必须同步到 symbols_fts，否则检索结果会静默失真（§4.3"与 symbols 同步"）。
var symbolsFTSTriggerSQL = []string{
	`CREATE TRIGGER IF NOT EXISTS symbols_fts_ai AFTER INSERT ON symbols BEGIN
		INSERT INTO symbols_fts(rowid, name, qualified_name, signature)
		VALUES (new.rowid, new.name, new.qualified_name, COALESCE(new.signature, ''));
	END`,
	`CREATE TRIGGER IF NOT EXISTS symbols_fts_ad AFTER DELETE ON symbols BEGIN
		INSERT INTO symbols_fts(symbols_fts, rowid, name, qualified_name, signature)
		VALUES ('delete', old.rowid, old.name, old.qualified_name, COALESCE(old.signature, ''));
	END`,
	`CREATE TRIGGER IF NOT EXISTS symbols_fts_au AFTER UPDATE ON symbols BEGIN
		INSERT INTO symbols_fts(symbols_fts, rowid, name, qualified_name, signature)
		VALUES ('delete', old.rowid, old.name, old.qualified_name, COALESCE(old.signature, ''));
		INSERT INTO symbols_fts(rowid, name, qualified_name, signature)
		VALUES (new.rowid, new.name, new.qualified_name, COALESCE(new.signature, ''));
	END`,
}

// ensureFTS 在迁移之后准备检索索引，并记录本次连接是否可用 FTS5。
//
// FTS5 可用：建表（幂等）+ 挂触发器；表是本次新建的则 rebuild 存量符号。
// FTS5 不可用：不返回错误，改为清理旧库可能遗留的 FTS5 表/触发器——遗留的
// 触发器会在每次写 symbols 时报 "no such module: fts5"，把整个写路径拖垮。
// 索引是派生数据，清理后仍可从 symbols 重建，因此这里可以安全丢弃。
func (s *sqliteStore) ensureFTS(ctx context.Context) error {
	existed, err := s.tableExists(ctx, "symbols_fts")
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, createSymbolsFTSSQL); err != nil {
		return s.dropUnusableFTS(ctx)
	}
	for _, ddl := range symbolsFTSTriggerSQL {
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("knowledge: create fts sync trigger: %w", err)
		}
	}
	if !existed {
		if _, err := s.db.ExecContext(ctx, symbolsFTSIndexSQL); err != nil {
			return fmt.Errorf("knowledge: rebuild fts index: %w", err)
		}
	}
	s.ftsEnabled = true
	return nil
}

// probeFTS 在只读打开时探测 FTS5 是否真的可用：表存在且模块可用。
//
// 只读路径不建表也不迁移，但既要避免在无 FTS5 的环境里用 MATCH（报
// "no such module"），也要在 FTS5 可用时不丢掉索引检索能力。
func (s *sqliteStore) probeFTS(ctx context.Context) bool {
	if !s.tableExistsQuiet(ctx, "symbols_fts") {
		return false
	}
	// 真实查询一次：表存在但模块不可用时，这一步才会报 "no such module: fts5"。
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM symbols_fts`).Scan(&n); err != nil {
		return false
	}
	return true
}

// dropUnusableFTS 清理 FTS5 不可用时残留的符号索引对象。
//
// 先删触发器再删表：触发器删除不依赖 fts5 模块，必须先成功，否则写 symbols
// 仍会因触发器体内的 fts5 语句失败。DROP TABLE 在模块缺失时可能失败（虚拟表
// 的销毁需要模块），此时保留孤立的空壳表——它不参与读写，只等 FTS5 恢复后
// 被 ensureFTS 复用。
func (s *sqliteStore) dropUnusableFTS(ctx context.Context) error {
	for _, object := range []string{"symbols_fts_ai", "symbols_fts_ad", "symbols_fts_au"} {
		if _, err := s.db.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+object); err != nil {
			return fmt.Errorf("knowledge: drop stale fts trigger %s: %w", object, err)
		}
	}
	_, _ = s.db.ExecContext(ctx, "DROP TABLE IF EXISTS symbols_fts")
	return nil
}

// tableExists 报告表（含虚拟表）是否存在。
func (s *sqliteStore) tableExists(ctx context.Context, name string) (bool, error) {
	var found string
	err := s.db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&found)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, fmt.Errorf("knowledge: check table %s: %w", name, err)
	}
}

// tableExistsQuiet 是 tableExists 的只读探测版本：任何错误都按"不存在"处理，
// 因为探测失败时调用方会退回 LIKE 路径，语义安全。
func (s *sqliteStore) tableExistsQuiet(ctx context.Context, name string) bool {
	ok, err := s.tableExists(ctx, name)
	return err == nil && ok
}
