# 03 — 类型系统：继承、实现、重载、泛型

> 定位：**supplement（extension schema 事实源）**，不是第 5 份并列设计文档（`README.md` §7）。
> 来源：由 `03_agent_harness_supplement.md` 于 **2026-09-28 拆分**迁入（`06` §9 待办 #9 / `03` §21 自身的拆分建议）。
> **章节号沿用 `03` 原始编号**（因此本文件内编号可能不连续），用于解析拆分前的历史引用；映射见 `../03_agent_harness_supplement.md` §0。
> 事实源边界：core schema 以 `02_agent_harness_technical_design_spec_sqlite.md` 为准；extension schema 以本目录 `supplement/*` 为准；决策以 `../adr/` 为准；落地计划与验收门槛以 `../04_completeness_review_and_optimized_plan.md` 为准。

---

# 3. 类型、继承、实现、重载、泛型

## 3.1 问题

原方案提到 `inheritance`、`type_graph`，但缺少完整数据模型。  
缺少这些，`code.impact`、`code.trace`、`find_implementations` 会不准。

## 3.2 建议 DDL

```sql
CREATE TABLE type_relations (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    from_symbol_id TEXT NOT NULL,
    to_symbol_id TEXT,
    relation_type TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 0.5,
    source TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE INDEX idx_type_relations_from
ON type_relations(from_symbol_id);

CREATE INDEX idx_type_relations_to
ON type_relations(to_symbol_id);

CREATE TABLE inheritance_edges (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    child_symbol_id TEXT NOT NULL,
    parent_symbol_id TEXT,
    parent_name TEXT,
    relation_type TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 0.5,
    source TEXT,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE implementation_edges (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    implementation_symbol_id TEXT NOT NULL,
    interface_symbol_id TEXT,
    interface_name TEXT,
    confidence REAL NOT NULL DEFAULT 0.5,
    source TEXT,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE overloads (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    symbol_id TEXT NOT NULL,
    overload_group_key TEXT NOT NULL,
    signature_hash TEXT NOT NULL,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE generic_params (
    id TEXT PRIMARY KEY,
    symbol_id TEXT NOT NULL,
    name TEXT NOT NULL,
    bounds_json TEXT,
    variance TEXT,
    ordinal INTEGER,

    FOREIGN KEY(symbol_id)
        REFERENCES symbols(id)
        ON DELETE CASCADE
);

CREATE TABLE parameters (
    id TEXT PRIMARY KEY,
    symbol_id TEXT NOT NULL,
    name TEXT,
    type_text TEXT,
    type_symbol_id TEXT,
    default_value TEXT,
    ordinal INTEGER,
    is_variadic INTEGER NOT NULL DEFAULT 0,

    FOREIGN KEY(symbol_id)
        REFERENCES symbols(id)
        ON DELETE CASCADE
);

CREATE TABLE local_variables (
    id TEXT PRIMARY KEY,
    symbol_id TEXT NOT NULL,
    file_id TEXT NOT NULL,
    name TEXT NOT NULL,
    type_text TEXT,
    start_line INTEGER,
    end_line INTEGER,

    FOREIGN KEY(symbol_id)
        REFERENCES symbols(id)
        ON DELETE CASCADE
);

CREATE TABLE annotations (
    id TEXT PRIMARY KEY,
    symbol_id TEXT,
    file_id TEXT,
    name TEXT NOT NULL,
    arguments_json TEXT,
    start_line INTEGER,
    end_line INTEGER
);
```

## 3.3 关系类型

```text
EXTENDS
IMPLEMENTS
IMPLEMENTS_INTERFACE
OVERRIDES
OVERLOADS
TYPE_ALIAS
GENERIC_BOUND
RETURNS
PARAMETER_TYPE
FIELD_TYPE
ANNOTATED_BY
```

---
