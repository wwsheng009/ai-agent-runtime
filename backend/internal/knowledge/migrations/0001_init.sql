-- 0001_init.sql — Knowledge Layer v1 schema (knowledge.db)
--
-- 事实源：docs/knowledge_Layer/04_completeness_review_and_optimized_plan.md §4.3
-- （v1 最小数据模型，16 张）。本文件是该 DDL 的唯一落盘位置，Go 代码不再内联 DDL。
--
-- 两处与 §4.3 原文的有意差异，均为原文自身不自洽处，已在 CHANGELOG 记录：
--   1) 不创建 schema_migrations：该表由 internal/migrate 统一创建并维护
--      （version INTEGER PRIMARY KEY / name TEXT / applied_at TEXT），
--      §4.3 中的 (version, applied_at, checksum) 版本会与之冲突。
--   2) symbols_fts 的列集为 (name, qualified_name, signature)：§4.3 多出的
--      summary 列在 symbols 表中并不存在，而 FTS5 external-content 表要求
--      列名与被索引表一致，否则同步触发器与查询都会报错。
--   3) refs 增加 to_symbol_name 列：§4.3 的 refs 只有 to_symbol_id，而同一节又
--      规定"to_symbol_id 为空表示仅凭名字猜测存在同名声明"——名字无法回读时，
--      未解析引用（stdlib / 第三方 / 尚未索引的符号）既查不到也修不了。
--      该列记录使用点字面写下的目标名，是 to_symbol_id 之外的独立事实。
--
-- 表顺序即依赖顺序：workspaces → files → symbols → refs → …（外键 ON DELETE CASCADE）。

-- 1. 工作区（id 复用 workspaceregistry，不新造主键）
CREATE TABLE workspaces (
    id           TEXT PRIMARY KEY,
    root_path    TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_workspaces_root ON workspaces(root_path);

-- 2. 文件
CREATE TABLE files (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL,
    path          TEXT NOT NULL,          -- workspace 相对路径，统一 '/' 分隔、大小写规范化
    language      TEXT,
    size          INTEGER NOT NULL DEFAULT 0,
    mtime_ns      INTEGER,
    content_hash  TEXT NOT NULL,          -- sha256
    is_test       INTEGER NOT NULL DEFAULT 0,
    is_generated  INTEGER NOT NULL DEFAULT 0,
    index_state   TEXT NOT NULL DEFAULT 'unknown', -- unknown|light|deep|stale|error
    indexed_at    INTEGER,
    FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX idx_files_ws_path ON files(workspace_id, path);
CREATE INDEX idx_files_ws_hash ON files(workspace_id, content_hash);
CREATE INDEX idx_files_ws_state ON files(workspace_id, index_state);

-- 3. 符号（stable_key 见 §4.4）
CREATE TABLE symbols (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT NOT NULL,
    file_id           TEXT NOT NULL,
    stable_key        TEXT NOT NULL,
    name              TEXT NOT NULL,
    qualified_name    TEXT NOT NULL,
    kind              TEXT NOT NULL,       -- func|method|struct|interface|class|type|const|var|test
    language          TEXT NOT NULL,
    owner_symbol_id   TEXT,
    signature         TEXT,
    signature_hash    TEXT,
    content_hash      TEXT,
    start_line        INTEGER NOT NULL,
    start_col         INTEGER NOT NULL DEFAULT 0,
    end_line          INTEGER NOT NULL,
    end_col           INTEGER NOT NULL DEFAULT 0,
    is_exported       INTEGER NOT NULL DEFAULT 0,
    is_test           INTEGER NOT NULL DEFAULT 0,
    deleted_at        INTEGER,
    FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY(file_id) REFERENCES files(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX idx_symbols_stable ON symbols(workspace_id, stable_key);
CREATE INDEX idx_symbols_name ON symbols(workspace_id, name);
CREATE INDEX idx_symbols_qualified ON symbols(workspace_id, qualified_name);
CREATE INDEX idx_symbols_file ON symbols(file_id);

-- 4. 符号版本（用于引用/缓存绑版本，落实 03 §1.4 规则 5）
CREATE TABLE symbol_versions (
    id              TEXT PRIMARY KEY,
    symbol_id       TEXT NOT NULL,
    version         INTEGER NOT NULL,
    content_hash    TEXT NOT NULL,
    signature_hash  TEXT,
    adapter_version TEXT NOT NULL,
    valid_from      INTEGER NOT NULL,
    valid_to        INTEGER,
    FOREIGN KEY(symbol_id) REFERENCES symbols(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX idx_symbol_versions ON symbol_versions(symbol_id, version);

-- 5. 符号别名/重命名（P0，来自 03 §1.3，必须进 v1，否则引用修复无据可依）
CREATE TABLE symbol_aliases (
    id           TEXT PRIMARY KEY,
    symbol_id    TEXT NOT NULL,
    alias_key    TEXT NOT NULL,
    alias_type   TEXT NOT NULL,           -- rename|move|overload
    created_at   INTEGER NOT NULL,
    FOREIGN KEY(symbol_id) REFERENCES symbols(id) ON DELETE CASCADE
);
CREATE INDEX idx_symbol_aliases_key ON symbol_aliases(alias_key);

-- 6. 引用
CREATE TABLE refs (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT NOT NULL,
    from_symbol_id    TEXT,
    to_symbol_id      TEXT,
    to_symbol_name    TEXT,                -- 使用点写下的目标名（to_symbol_id 为空时唯一线索）
    to_symbol_version INTEGER,
    kind              TEXT NOT NULL,       -- reference|call|import|implement
    file_id           TEXT NOT NULL,
    line              INTEGER NOT NULL,
    col               INTEGER NOT NULL DEFAULT 0,
    snippet           TEXT,
    confidence        REAL NOT NULL DEFAULT 0.5,
    source            TEXT NOT NULL,       -- lsp|tree-sitter|heuristic|fts
    FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY(file_id) REFERENCES files(id) ON DELETE CASCADE
);
CREATE INDEX idx_refs_to ON refs(workspace_id, to_symbol_id);
CREATE INDEX idx_refs_to_name ON refs(workspace_id, to_symbol_name);
CREATE INDEX idx_refs_from ON refs(workspace_id, from_symbol_id);
CREATE INDEX idx_refs_file ON refs(file_id);

-- 7. 探索会话 / 节点 / 边（多轮复用，最高 ROI）
CREATE TABLE exploration_sessions (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    session_id   TEXT NOT NULL,
    task_id      TEXT,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
);
CREATE INDEX idx_explore_sessions ON exploration_sessions(workspace_id, session_id, task_id);

CREATE TABLE exploration_nodes (
    id              TEXT PRIMARY KEY,
    exploration_id  TEXT NOT NULL,
    node_type       TEXT NOT NULL,         -- file|symbol|query|answer
    target          TEXT NOT NULL,
    file_id         TEXT,
    symbol_id       TEXT,
    summary         TEXT,
    confidence      REAL NOT NULL DEFAULT 0.5,
    knowledge_version TEXT NOT NULL,
    created_at      INTEGER NOT NULL,
    last_used_at    INTEGER,
    use_count       INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY(exploration_id) REFERENCES exploration_sessions(id) ON DELETE CASCADE
);
CREATE INDEX idx_explore_nodes_target ON exploration_nodes(exploration_id, target);

CREATE TABLE exploration_edges (
    id            TEXT PRIMARY KEY,
    exploration_id TEXT NOT NULL,
    from_node_id  TEXT NOT NULL,
    to_node_id    TEXT NOT NULL,
    edge_type     TEXT NOT NULL,           -- calls|references|contains|derived_from
    weight        REAL NOT NULL DEFAULT 1.0,
    FOREIGN KEY(exploration_id) REFERENCES exploration_sessions(id) ON DELETE CASCADE
);
CREATE INDEX idx_explore_edges_from ON exploration_edges(from_node_id);

-- 8. 上下文快照 / 条目（可解释性 + stale 拒绝注入）
CREATE TABLE context_snapshots (
    id                TEXT PRIMARY KEY,
    session_id        TEXT NOT NULL,
    task_id           TEXT,
    workspace_id      TEXT,
    knowledge_version TEXT,
    compiler_version  TEXT NOT NULL,
    budget_json       TEXT,
    created_at        INTEGER NOT NULL
);
CREATE INDEX idx_ctx_snapshots_session ON context_snapshots(session_id, created_at);

CREATE TABLE context_items (
    id            TEXT PRIMARY KEY,
    snapshot_id   TEXT NOT NULL,
    item_type     TEXT NOT NULL,           -- symbol|file_region|exploration|fact|note|tool_result
    ref_id        TEXT,
    source        TEXT NOT NULL,           -- lsp|tree-sitter|heuristic|memory|artifact|fact
    trust         TEXT NOT NULL DEFAULT 'medium', -- high|medium|low|untrusted
    tokens        INTEGER NOT NULL DEFAULT 0,
    reason        TEXT,
    stale         INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY(snapshot_id) REFERENCES context_snapshots(id) ON DELETE CASCADE
);
CREATE INDEX idx_ctx_items_snapshot ON context_items(snapshot_id);

-- 9. 缓存（只保留持久层；进程内缓存不进表）
CREATE TABLE cache_entries (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL,
    cache_key     TEXT NOT NULL,
    cache_type    TEXT NOT NULL,           -- retrieval|compile|summary
    payload_json  TEXT NOT NULL,
    knowledge_version TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER
);
CREATE UNIQUE INDEX idx_cache_key ON cache_entries(workspace_id, cache_type, cache_key);

CREATE TABLE invalidation_events (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL,
    reason        TEXT NOT NULL,           -- file_changed|git_sync|adapter_upgraded|schema_upgraded|manual
    scope_json    TEXT,
    created_at    INTEGER NOT NULL
);
CREATE INDEX idx_invalidation_ws ON invalidation_events(workspace_id, created_at);

-- 10. 索引任务（可观测 + 可重试 + 串行化）
--
-- 注：§4.3 已标注本段 DDL 越位（ADR-0007 §4.3），ADR-0007 被 Accept 后迁移到
-- extension schema；迁移完成前此处为唯一落盘副本。
CREATE TABLE index_jobs (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL,
    kind          TEXT NOT NULL,           -- light|deep|full_rebuild|gc
    status        TEXT NOT NULL,           -- queued|running|done|failed|cancelled
    files_total   INTEGER NOT NULL DEFAULT 0,
    files_done    INTEGER NOT NULL DEFAULT 0,
    error         TEXT,
    started_at    INTEGER,
    finished_at   INTEGER,
    FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
);
CREATE INDEX idx_index_jobs_ws ON index_jobs(workspace_id, status);

-- 11. FTS5（多语言检索，与 symbols 同步）
CREATE VIRTUAL TABLE symbols_fts USING fts5(
    name, qualified_name, signature,
    content='symbols', content_rowid='rowid',
    tokenize='unicode61'
);

-- FTS5 external-content 同步触发器：symbols 的任何写入都必须同步到 symbols_fts，
-- 否则检索结果会静默失真（§4.3 要求"与 symbols 同步"）。
CREATE TRIGGER symbols_fts_ai AFTER INSERT ON symbols BEGIN
    INSERT INTO symbols_fts(rowid, name, qualified_name, signature)
    VALUES (new.rowid, new.name, new.qualified_name, COALESCE(new.signature, ''));
END;
CREATE TRIGGER symbols_fts_ad AFTER DELETE ON symbols BEGIN
    INSERT INTO symbols_fts(symbols_fts, rowid, name, qualified_name, signature)
    VALUES ('delete', old.rowid, old.name, old.qualified_name, COALESCE(old.signature, ''));
END;
CREATE TRIGGER symbols_fts_au AFTER UPDATE ON symbols BEGIN
    INSERT INTO symbols_fts(symbols_fts, rowid, name, qualified_name, signature)
    VALUES ('delete', old.rowid, old.name, old.qualified_name, COALESCE(old.signature, ''));
    INSERT INTO symbols_fts(rowid, name, qualified_name, signature)
    VALUES (new.rowid, new.name, new.qualified_name, COALESCE(new.signature, ''));
END;
