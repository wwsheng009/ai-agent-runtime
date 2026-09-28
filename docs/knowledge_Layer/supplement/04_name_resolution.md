# 04 — 名称解析与导入解析

> 定位：**supplement（extension schema 事实源）**，不是第 5 份并列设计文档（`README.md` §7）。
> 来源：由 `03_agent_harness_supplement.md` 于 **2026-09-28 拆分**迁入（`06` §9 待办 #9 / `03` §21 自身的拆分建议）。
> **章节号沿用 `03` 原始编号**（因此本文件内编号可能不连续），用于解析拆分前的历史引用；映射见 `../03_agent_harness_supplement.md` §0。
> 事实源边界：core schema 以 `02_agent_harness_technical_design_spec_sqlite.md` 为准；extension schema 以本目录 `supplement/*` 为准；决策以 `../adr/` 为准；落地计划与验收门槛以 `../04_completeness_review_and_optimized_plan.md` 为准。

---

# 4. 名称解析与导入解析

## 4.1 问题

动态语言和跨文件符号解析必须定义作用域、导入、包解析、重载消歧和动态候选。

## 4.2 建议 DDL

```sql
CREATE TABLE import_bindings (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    file_id TEXT NOT NULL,
    import_id TEXT,
    local_name TEXT NOT NULL,
    imported_name TEXT,
    module_path TEXT,
    resolved_file_id TEXT,
    resolved_symbol_id TEXT,
    resolution_status TEXT NOT NULL DEFAULT 'UNKNOWN',
    confidence REAL NOT NULL DEFAULT 0.5,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE name_resolutions (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    file_id TEXT NOT NULL,
    source_symbol_id TEXT,
    reference_id TEXT,
    raw_name TEXT NOT NULL,
    resolved_symbol_id TEXT,
    candidate_symbol_ids_json TEXT,
    resolution_status TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 0.5,
    resolver TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE scope_bindings (
    id TEXT PRIMARY KEY,
    file_id TEXT NOT NULL,
    scope_symbol_id TEXT,
    name TEXT NOT NULL,
    symbol_id TEXT,
    binding_type TEXT NOT NULL,
    start_line INTEGER,
    end_line INTEGER
);
```

## 4.3 解析状态

```text
RESOLVED
CANDIDATE
AMBIGUOUS
UNRESOLVED
DYNAMIC
EXTERNAL
GENERATED
```

## 4.4 解析来源优先级

```text
LSP
Custom Semantic Adapter
Tree-sitter + Scope Resolver
FTS5
Regex / Text Search
```

---
