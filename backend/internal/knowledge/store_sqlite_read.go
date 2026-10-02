package knowledge

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// rowScanner 抽象 *sql.Row 与 *sql.Rows 的公共部分，供扫描辅助函数复用。
type rowScanner interface {
	Scan(dest ...any) error
}

// FindSymbols 在符号表中解析名字。
//
// Name 默认做大小写敏感的子串匹配（Exact=true 时精确匹配），
// 使 FindSymbols("Open") 也能找到 OpenFile 这类辅助函数。
func (s *sqliteStore) FindSymbols(ctx context.Context, q SymbolQuery) ([]Symbol, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultQueryLimit
	}
	// Default symbol queries expose production symbols. Test-file symbols remain
	// persisted for reference resolution and diagnostics, but should not pollute
	// ordinary name searches (for example, Open -> TestOpenFile).
	where := []string{"s.deleted_at IS NULL", "s.is_test = 0", "f.deleted_at IS NULL"}
	var args []any
	if q.Name != "" {
		if q.Exact {
			where = append(where, "s.name = ?")
			args = append(args, q.Name)
		} else {
			where = append(where, `s.name LIKE ? ESCAPE '\'`)
			args = append(args, "%"+escapeLike(q.Name)+"%")
		}
	}
	if q.Kind != "" {
		where = append(where, "s.kind = ?")
		args = append(args, string(q.Kind))
	}
	if q.Lang != "" {
		where = append(where, "s.language = ?")
		args = append(args, q.Lang)
	}
	if q.PathPrefix != "" {
		where = append(where, `f.path LIKE ? ESCAPE '\'`)
		args = append(args, escapeLike(normalizeRelPath(q.PathPrefix))+"%")
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id, s.workspace_id, s.file_id, s.stable_key, s.name, s.qualified_name, s.kind,
		       s.language, COALESCE(s.owner_symbol_id, ''), COALESCE(s.signature, ''),
		       COALESCE(s.signature_hash, ''), COALESCE(s.content_hash, ''),
		       s.start_line, s.start_col, s.end_line, s.end_col, s.is_exported, s.is_test,
		       COALESCE(s.deleted_at, 0)
		FROM symbols s JOIN files f ON f.id = s.file_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY s.name, f.path, s.start_line
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("knowledge: find symbols: %w", err)
	}
	defer rows.Close()
	var out []Symbol
	for rows.Next() {
		sym, err := scanSymbol(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sym)
	}
	return out, rows.Err()
}

// FindRefs 解析指向某个符号身份的引用点。
//
// 给出 ToSymbolName 时同时匹配两种情况：
//   - 已解析的引用：to_symbol_id 指向一个同名符号；
//   - 未解析的引用：to_symbol_name 等于该名字（stdlib / 第三方 / 尚未索引的目标）。
//
// 两条路径都必要：只看 to_symbol_id 会漏掉所有外部引用，只看名字会漏掉
// 名字被重命名但身份仍绑定的历史引用。
func (s *sqliteStore) FindRefs(ctx context.Context, q RefQuery) ([]Reference, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultQueryLimit
	}

	where := []string{"f.deleted_at IS NULL"}
	var args []any
	switch {
	case q.ToSymbolID != "":
		where = append(where, "r.to_symbol_id = ?")
		args = append(args, q.ToSymbolID)
	case q.ToSymbolName != "":
		ids, err := s.symbolIDsByName(ctx, q.ToSymbolName)
		if err != nil {
			return nil, err
		}
		if len(ids) > 0 {
			where = append(where,
				"(r.to_symbol_id IN ("+placeholders(len(ids))+") OR r.to_symbol_name = ?)")
			for _, id := range ids {
				args = append(args, id)
			}
			args = append(args, q.ToSymbolName)
		} else {
			where = append(where, "r.to_symbol_name = ?")
			args = append(args, q.ToSymbolName)
		}
	}
	if q.FromSymbolID != "" {
		where = append(where, "r.from_symbol_id = ?")
		args = append(args, q.FromSymbolID)
	}
	if q.Kind != "" {
		where = append(where, "r.kind = ?")
		args = append(args, string(q.Kind))
	}
	if q.PathPrefix != "" {
		where = append(where, `f.path LIKE ? ESCAPE '\'`)
		args = append(args, escapeLike(normalizeRelPath(q.PathPrefix))+"%")
	}
	args = append(args, limit)

	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.workspace_id, COALESCE(r.from_symbol_id, ''), COALESCE(r.to_symbol_id, ''),
		       COALESCE(r.to_symbol_name, ''), COALESCE(r.to_symbol_version, 0), r.kind,
		       r.file_id, f.path, r.line, r.col,
		       COALESCE(r.snippet, ''), r.confidence, r.source,
		       COALESCE(fs.qualified_name, '')
		FROM refs r
		JOIN files f ON f.id = r.file_id
		LEFT JOIN symbols fs ON fs.id = r.from_symbol_id
		`+clause+`
		ORDER BY f.path, r.line, r.col
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("knowledge: find refs: %w", err)
	}
	defer rows.Close()
	var out []Reference
	for rows.Next() {
		ref, err := scanReference(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// symbolIDsByName 返回同名（未删除）符号的 id 列表，用于把引用查询从名字
// 解析到身份。
func (s *sqliteStore) symbolIDsByName(ctx context.Context, name string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id FROM symbols s JOIN files f ON f.id = s.file_id
		WHERE s.name = ? AND s.deleted_at IS NULL AND f.deleted_at IS NULL
		LIMIT ?`, name, defaultQueryLimit)
	if err != nil {
		return nil, fmt.Errorf("knowledge: resolve ref target: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Search 做全文检索。
//
// FTS5 可用时在 symbols_fts 上检索：查询串按 token 引号化后做前缀匹配，
// 因此用户输入的 FTS 语法字符（-、"、* 等）不会引发语法错误，只会被当作
// 普通字符。FTS5 不可用（纯 Go SQLite 构建未编译该模块）时退化为 LIKE 扫描，
// 见 searchLike。
func (s *sqliteStore) Search(ctx context.Context, q SearchQuery) ([]SearchHit, error) {
	if !s.ftsEnabled {
		return s.searchLike(ctx, q)
	}
	match := ftsMatchQuery(q.Text)
	if match == "" {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultQueryLimit
	}
	where := []string{"symbols_fts MATCH ?", "s.deleted_at IS NULL", "f.deleted_at IS NULL"}
	args := []any{match}
	if q.Lang != "" {
		where = append(where, "s.language = ?")
		args = append(args, q.Lang)
	}
	if q.PathPrefix != "" {
		where = append(where, `f.path LIKE ? ESCAPE '\'`)
		args = append(args, escapeLike(normalizeRelPath(q.PathPrefix))+"%")
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id, s.name, s.qualified_name, s.kind, f.path, s.language, s.start_line,
		       COALESCE(s.signature, ''), bm25(symbols_fts)
		FROM symbols_fts
		JOIN symbols s ON s.rowid = symbols_fts.rowid
		JOIN files f ON f.id = s.file_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY bm25(symbols_fts)
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("knowledge: search symbols: %w", err)
	}
	defer rows.Close()
	var out []SearchHit
	for rows.Next() {
		var (
			hit  SearchHit
			kind string
			bm25 float64
		)
		if err := rows.Scan(&hit.SymbolID, &hit.Name, &hit.QualifiedName, &kind, &hit.Path,
			&hit.Language, &hit.Line, &hit.Signature, &bm25); err != nil {
			return nil, fmt.Errorf("knowledge: scan search hit: %w", err)
		}
		hit.Kind = SymbolKind(kind)
		hit.Score = -bm25 // bm25 越小越相关；对外统一为"越大越相关"
		out = append(out, hit)
	}
	return out, rows.Err()
}

// searchLike 是 FTS5 不可用时的降级检索。
//
// 语义与 FTS 路径对齐：token 之间 AND、大小写不敏感的子串匹配、同样的
// 语言/路径前缀/删除位过滤；排序退化为"名字、路径、行号"，Score 按命中列
// （name > qualified_name > 仅签名）给出，仍只在本次结果集内具备相对意义。
// 与 unicode61 的差别：LIKE 的 ASCII 大小写不折叠在非 ASCII 文本上退化为
// 精确子串匹配。
func (s *sqliteStore) searchLike(ctx context.Context, q SearchQuery) ([]SearchHit, error) {
	tokens := searchTokens(q.Text)
	if len(tokens) == 0 {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultQueryLimit
	}
	where := []string{"s.deleted_at IS NULL", "f.deleted_at IS NULL"}
	args := make([]any, 0, len(tokens)*3+3)
	for _, token := range tokens {
		pattern := "%" + escapeLike(token) + "%"
		where = append(where,
			`(s.name LIKE ? ESCAPE '\' OR COALESCE(s.qualified_name, '') LIKE ? ESCAPE '\' OR COALESCE(s.signature, '') LIKE ? ESCAPE '\')`)
		args = append(args, pattern, pattern, pattern)
	}
	if q.Lang != "" {
		where = append(where, "s.language = ?")
		args = append(args, q.Lang)
	}
	if q.PathPrefix != "" {
		where = append(where, `f.path LIKE ? ESCAPE '\'`)
		args = append(args, escapeLike(normalizeRelPath(q.PathPrefix))+"%")
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id, s.name, COALESCE(s.qualified_name, ''), s.kind, f.path, s.language, s.start_line,
		       COALESCE(s.signature, '')
		FROM symbols s
		JOIN files f ON f.id = s.file_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY s.name, f.path, s.start_line
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("knowledge: search symbols (like fallback): %w", err)
	}
	defer rows.Close()
	var out []SearchHit
	for rows.Next() {
		var (
			hit  SearchHit
			kind string
		)
		if err := rows.Scan(&hit.SymbolID, &hit.Name, &hit.QualifiedName, &kind, &hit.Path,
			&hit.Language, &hit.Line, &hit.Signature); err != nil {
			return nil, fmt.Errorf("knowledge: scan search hit: %w", err)
		}
		hit.Kind = SymbolKind(kind)
		hit.Score = likeHitScore(hit, tokens)
		out = append(out, hit)
	}
	return out, rows.Err()
}

// likeHitScore 给出降级检索的命中分：name 命中 > qualified_name 命中 > 其他列
// 命中。只用于同一结果集内的相对排序，不可跨实现比较。
func likeHitScore(hit SearchHit, tokens []string) float64 {
	for _, token := range tokens {
		needle := strings.ToLower(token)
		if strings.Contains(strings.ToLower(hit.Name), needle) {
			return 1.0
		}
		if strings.Contains(strings.ToLower(hit.QualifiedName), needle) {
			return 0.5
		}
	}
	return 0.25
}

// RecordInvalidation 记录一次索引失效事件。
func (s *sqliteStore) RecordInvalidation(ctx context.Context, ev InvalidationEvent) error {
	if ev.WorkspaceID == "" || ev.Reason == "" {
		return fmt.Errorf("knowledge: invalidation event requires workspace_id and reason")
	}
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now()
	}
	if ev.ID == "" {
		ev.ID = EventID(ev.WorkspaceID, ev.Reason, ev.CreatedAt.UnixNano())
	}
	return s.execWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO invalidation_events (id, workspace_id, reason, scope_json, created_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(id) DO NOTHING
		`, ev.ID, ev.WorkspaceID, ev.Reason, nullIfEmpty(ev.ScopeJSON), unixMillis(ev.CreatedAt))
		return err
	})
}

// Stats 返回行数汇总与最近索引时间。
func (s *sqliteStore) Stats(ctx context.Context, workspaceID string) (Stats, error) {
	var stats Stats
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		return stats, err
	}
	stats.SchemaVersion = version
	row := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM files   WHERE workspace_id = ? AND deleted_at IS NULL),
			(SELECT COUNT(*) FROM symbols WHERE workspace_id = ? AND deleted_at IS NULL),
			(SELECT COUNT(*) FROM refs r JOIN files f ON f.id = r.file_id
			  WHERE r.workspace_id = ? AND f.deleted_at IS NULL),
			COALESCE(
				(SELECT MAX(j.finished_at) FROM index_jobs j
				  WHERE j.workspace_id = ? AND j.kind = 'light'
				    AND j.status = 'done' AND j.finished_at > 0
				    -- 预算截断的 light 运行**不算**对账：collectIndexableFiles
				    -- 撞到 maxIndexFiles 就 fs.SkipAll 提前结束遍历，文件总数里
				    -- 仍有大量文件本次根本没被看过，而 indexer 只按 err==nil 记
				    -- status='done'（见 indexer.go 的 FinishIndexJob defer），
				    -- 截断信号 result.Truncated 不落表。这类运行若被当成完整对账，
				    -- 就会把未校验文件的陈旧度清零——和 incremental 那条同源的 fail-open。
				    -- 截断时 files_total 恰好等于 maxIndexFiles；文件数正好等于
				    -- 上限的边界情形也落到"不算对账"这一侧（fail-closed，可接受）。
				    AND j.files_total < ?),
				(SELECT COALESCE(MAX(indexed_at), 0) FROM files
				  WHERE workspace_id = ? AND deleted_at IS NULL)
			)
	`, workspaceID, workspaceID, workspaceID, workspaceID, maxIndexFiles, workspaceID)

	if err := row.Scan(&stats.Files, &stats.Symbols, &stats.Refs, &stats.IndexedAt); err != nil {
		return stats, fmt.Errorf("knowledge: read stats: %w", err)
	}
	return stats, nil
}

// scanFile 把一行 files 记录读成 FileRecord。
func scanFile(row rowScanner) (FileRecord, error) {
	var (
		rec        FileRecord
		language   string
		isTest     int
		isGen      int
		indexState string
		indexedAt  int64
		deletedAt  int64
	)
	if err := row.Scan(&rec.ID, &rec.WorkspaceID, &rec.Path, &language, &rec.Size, &rec.MTimeNS,
		&rec.ContentHash, &isTest, &isGen, &indexState, &indexedAt, &deletedAt); err != nil {
		return FileRecord{}, err
	}
	rec.Language = language
	rec.IsTest = isTest != 0
	rec.IsGenerated = isGen != 0
	rec.IndexState = IndexState(indexState)
	rec.IndexedAt = timeFromUnixMillis(indexedAt)
	rec.DeletedAt = deletedAt
	return rec, nil
}

// scanSymbol 把一行 symbols 记录读成 Symbol。
func scanSymbol(row rowScanner) (Symbol, error) {
	var (
		sym     Symbol
		kind    string
		isExp   int
		isTest  int
		deleted int64
	)
	if err := row.Scan(&sym.ID, &sym.WorkspaceID, &sym.FileID, &sym.StableKey, &sym.Name,
		&sym.QualifiedName, &kind, &sym.Language, &sym.OwnerSymbolID, &sym.Signature,
		&sym.SignatureHash, &sym.ContentHash,
		&sym.Range.Start.Line, &sym.Range.Start.Column, &sym.Range.End.Line, &sym.Range.End.Column,
		&isExp, &isTest, &deleted); err != nil {
		return Symbol{}, fmt.Errorf("knowledge: scan symbol: %w", err)
	}
	sym.Kind = SymbolKind(kind)
	sym.IsExported = isExp != 0
	sym.IsTest = isTest != 0
	sym.DeletedAt = deleted
	return sym, nil
}

// scanReference 把一行 refs 记录读成 Reference。
func scanReference(row rowScanner) (Reference, error) {
	var (
		ref    Reference
		kind   string
		source string
	)
	if err := row.Scan(&ref.ID, &ref.WorkspaceID, &ref.FromSymbolID, &ref.ToSymbolID,
		&ref.ToSymbolName, &ref.ToSymbolVersion, &kind, &ref.FileID, &ref.Path, &ref.Line, &ref.Col, &ref.Snippet,
		&ref.Confidence, &source, &ref.FromSymbolName); err != nil {
		return Reference{}, fmt.Errorf("knowledge: scan ref: %w", err)
	}
	ref.Kind = RefKind(kind)
	ref.Source = RefSource(source)
	return ref, nil
}

// escapeLike 转义 LIKE 模式中的通配符，配合 ESCAPE '\' 使用。
func escapeLike(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(s)
}

// placeholders 返回 "?, ?, ?" 形式的占位符串。
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// ftsMatchQuery 把自由文本转成安全的 FTS5 前缀查询。
//
// 每个 token 用双引号包裹（FTS5 的字符串字面量），再追加 * 做前缀匹配；
// 引号本身被剥离，因此不存在注入 FTS 语法的可能。返回空串表示无有效 token。
func ftsMatchQuery(text string) string {
	tokens := searchTokens(text)
	quoted := make([]string, 0, len(tokens))
	for _, token := range tokens {
		quoted = append(quoted, `"`+token+`"*`)
	}
	return strings.Join(quoted, " ")
}

// searchTokens 把自由文本切成检索 token：去掉纯语法字符，保留其余原文。
// FTS 与 LIKE 两条检索路径共用，保证两者对同一输入的切分一致。
func searchTokens(text string) []string {
	fields := strings.Fields(text)
	tokens := make([]string, 0, len(fields))
	for _, field := range fields {
		cleaned := strings.Trim(field, `"'*(){}[]:^`)
		if cleaned == "" {
			continue
		}
		tokens = append(tokens, cleaned)
	}
	return tokens
}

// isMissingTable 判断错误是否为"表不存在"（未迁移的库）。
func isMissingTable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such table") || strings.Contains(msg, "no such virtual table")
}

// unixMillis 把时间转为 unix 毫秒；零值时间返回 0。
func unixMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// timeFromUnixMillis 把 unix 毫秒还原为时间；0 返回零值时间。
func timeFromUnixMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// boolToInt 把 bool 写成 SQLite 的 0/1。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullIfEmpty 把空串写成 NULL（用于可空 TEXT 列）。
func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// nullIfZeroInt 把 0 写成 NULL（用于可空 INTEGER 列）。
func nullIfZeroInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

// nullIfZeroInt64 把 0 写成 NULL（用于可空 INTEGER 列）。
func nullIfZeroInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
