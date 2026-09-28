# 06 — 版本向量、缓存失效与一致性

> 定位：**supplement（extension schema 事实源）**，不是第 5 份并列设计文档（`README.md` §7）。
> 来源：由 `03_agent_harness_supplement.md` 于 **2026-09-28 拆分**迁入（`06` §9 待办 #9 / `03` §21 自身的拆分建议）。
> **章节号沿用 `03` 原始编号**（因此本文件内编号可能不连续），用于解析拆分前的历史引用；映射见 `../03_agent_harness_supplement.md` §0。
> 事实源边界：core schema 以 `02_agent_harness_technical_design_spec_sqlite.md` 为准；extension schema 以本目录 `supplement/*` 为准；决策以 `../adr/` 为准；落地计划与验收门槛以 `../04_completeness_review_and_optimized_plan.md` 为准。

---

# 6. 版本向量、缓存失效与一致性

## 6.1 问题

原方案有 cache key 和 dependency，但缺少统一版本向量、事务边界、幂等索引、事件 outbox、消费者 offset。

## 6.2 版本向量

```text
version_vector =
  workspace_version
+ repository_version
+ commit_hash
+ working_tree_hash
+ file_content_hash
+ symbol_version
+ parser_version
+ adapter_version
+ lsp_server_version
+ build_config_hash
+ ignore_rules_hash
```

## 6.3 建议 DDL

```sql
CREATE TABLE version_vectors (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    repository_id TEXT,
    commit_hash TEXT,
    working_tree_hash TEXT,
    parser_version TEXT,
    adapter_versions_json TEXT,
    lsp_versions_json TEXT,
    build_config_hash TEXT,
    ignore_rules_hash TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE cache_dependencies (
    id TEXT PRIMARY KEY,
    cache_key TEXT NOT NULL,
    dependency_type TEXT NOT NULL,
    dependency_id TEXT NOT NULL,
    dependency_version TEXT,
    created_at INTEGER NOT NULL
);

CREATE INDEX idx_cache_dependencies_cache
ON cache_dependencies(cache_key);

CREATE TABLE events_outbox (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    aggregate_type TEXT,
    aggregate_id TEXT,
    payload_json TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING',
    created_at INTEGER NOT NULL,
    published_at INTEGER,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE consumer_offsets (
    consumer_name TEXT PRIMARY KEY,
    last_event_id TEXT,
    last_event_created_at INTEGER,
    updated_at INTEGER NOT NULL
);
```

## 6.4 一致性规则

```text
1. 索引写入必须事务化。
2. 文件版本、符号版本、引用、图边在同一事务提交。
3. 缓存失效通过事件 outbox 异步传播。
4. 读路径必须校验版本向量。
5. 索引任务必须幂等，可重放。
6. Context 编译必须记录依赖的版本向量。
```

---
