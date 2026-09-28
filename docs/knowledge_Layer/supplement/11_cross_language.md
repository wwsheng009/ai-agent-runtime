# 11 — 跨语言与 IDL

> 定位：**supplement（extension schema 事实源）**，不是第 5 份并列设计文档（`README.md` §7）。
> 来源：由 `03_agent_harness_supplement.md` 于 **2026-09-28 拆分**迁入（`06` §9 待办 #9 / `03` §21 自身的拆分建议）。
> **章节号沿用 `03` 原始编号**（因此本文件内编号可能不连续），用于解析拆分前的历史引用；映射见 `../03_agent_harness_supplement.md` §0。
> 事实源边界：core schema 以 `02_agent_harness_technical_design_spec_sqlite.md` 为准；extension schema 以本目录 `supplement/*` 为准；决策以 `../adr/` 为准；落地计划与验收门槛以 `../04_completeness_review_and_optimized_plan.md` 为准。

---

# 11. 跨语言与 IDL

## 11.1 建议 DDL

```sql
CREATE TABLE cross_language_links (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    from_symbol_id TEXT,
    to_symbol_id TEXT,
    from_file_id TEXT,
    to_file_id TEXT,
    relation_type TEXT NOT NULL,
    protocol TEXT,
    contract_id TEXT,
    confidence REAL NOT NULL DEFAULT 0.5,
    source TEXT,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE idl_contracts (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    contract_type TEXT NOT NULL,
    name TEXT NOT NULL,
    file_id TEXT,
    content_hash TEXT,
    metadata_json TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE api_endpoints (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    contract_id TEXT,
    method TEXT,
    path TEXT,
    handler_symbol_id TEXT,
    request_schema TEXT,
    response_schema TEXT
);

CREATE TABLE rpc_methods (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    contract_id TEXT,
    service_name TEXT,
    method_name TEXT,
    handler_symbol_id TEXT
);

CREATE TABLE message_topics (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    topic_name TEXT,
    producer_symbol_id TEXT,
    consumer_symbol_id TEXT,
    schema_id TEXT
);

CREATE TABLE db_schema_links (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    symbol_id TEXT,
    table_name TEXT,
    column_name TEXT,
    relation_type TEXT,
    confidence REAL NOT NULL DEFAULT 0.5
);
```

## 11.2 关系类型

```text
HTTP_CALL
RPC_CALL
MESSAGE
DATABASE
FILE
PROCESS
CLI
GRAPHQL
PROTOBUF
OPENAPI
```

---
