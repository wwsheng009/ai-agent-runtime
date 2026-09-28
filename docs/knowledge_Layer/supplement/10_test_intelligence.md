# 10 — 测试智能

> 定位：**supplement（extension schema 事实源）**，不是第 5 份并列设计文档（`README.md` §7）。
> 来源：由 `03_agent_harness_supplement.md` 于 **2026-09-28 拆分**迁入（`06` §9 待办 #9 / `03` §21 自身的拆分建议）。
> **章节号沿用 `03` 原始编号**（因此本文件内编号可能不连续），用于解析拆分前的历史引用；映射见 `../03_agent_harness_supplement.md` §0。
> 事实源边界：core schema 以 `02_agent_harness_technical_design_spec_sqlite.md` 为准；extension schema 以本目录 `supplement/*` 为准；决策以 `../adr/` 为准；落地计划与验收门槛以 `../04_completeness_review_and_optimized_plan.md` 为准。

---

# 10. 测试智能

## 10.1 建议 DDL

```sql
CREATE TABLE tests (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    file_id TEXT NOT NULL,
    symbol_id TEXT,
    test_name TEXT NOT NULL,
    test_framework TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE test_symbols (
    test_id TEXT NOT NULL,
    symbol_id TEXT NOT NULL,
    relation_type TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 0.5,

    PRIMARY KEY(test_id, symbol_id, relation_type),

    FOREIGN KEY(test_id)
        REFERENCES tests(id)
        ON DELETE CASCADE,

    FOREIGN KEY(symbol_id)
        REFERENCES symbols(id)
        ON DELETE CASCADE
);

CREATE TABLE test_runs (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    task_id TEXT,
    test_id TEXT,
    status TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    finished_at INTEGER,
    output_hash TEXT,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE coverage_evidence (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    symbol_id TEXT,
    file_id TEXT,
    test_run_id TEXT,
    covered INTEGER NOT NULL DEFAULT 0,
    coverage_count INTEGER,
    evidence_source TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);
```

## 10.2 API

```text
code.tests(symbol)
code.impact(symbol, include_tests=true)
code.diff(include_tests=true)
```

---
