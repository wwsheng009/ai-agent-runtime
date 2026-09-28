# 08 — 评估基准与黄金任务集

> 定位：**supplement（extension schema 事实源）**，不是第 5 份并列设计文档（`README.md` §7）。
> 来源：由 `03_agent_harness_supplement.md` 于 **2026-09-28 拆分**迁入（`06` §9 待办 #9 / `03` §21 自身的拆分建议）。
> **章节号沿用 `03` 原始编号**（因此本文件内编号可能不连续），用于解析拆分前的历史引用；映射见 `../03_agent_harness_supplement.md` §0。
> 事实源边界：core schema 以 `02_agent_harness_technical_design_spec_sqlite.md` 为准；extension schema 以本目录 `supplement/*` 为准；决策以 `../adr/` 为准；落地计划与验收门槛以 `../04_completeness_review_and_optimized_plan.md` 为准。

---

# 8. 评估基准与黄金任务集

## 8.1 问题

原方案有 Telemetry 指标，但没有 ground truth，无法验证精度和收益。

## 8.2 建议 DDL

```sql
CREATE TABLE eval_projects (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    language TEXT,
    repo_url TEXT,
    commit_hash TEXT,
    root_path TEXT,
    created_at INTEGER NOT NULL
);

CREATE TABLE eval_tasks (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    task_type TEXT NOT NULL,
    prompt TEXT NOT NULL,
    expected_symbols_json TEXT,
    expected_files_json TEXT,
    expected_edges_json TEXT,
    metadata_json TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(project_id)
        REFERENCES eval_projects(id)
        ON DELETE CASCADE
);

CREATE TABLE eval_golden_symbols (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    qualified_name TEXT NOT NULL,
    file_path TEXT,
    relevance REAL NOT NULL DEFAULT 1.0,

    FOREIGN KEY(task_id)
        REFERENCES eval_tasks(id)
        ON DELETE CASCADE
);

CREATE TABLE eval_runs (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    mode TEXT NOT NULL,
    model TEXT,
    started_at INTEGER NOT NULL,
    finished_at INTEGER,
    status TEXT,

    FOREIGN KEY(task_id)
        REFERENCES eval_tasks(id)
        ON DELETE CASCADE
);

CREATE TABLE eval_results (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    precision REAL,
    recall REAL,
    f1 REAL,
    tokens_total INTEGER,
    exploration_tokens INTEGER,
    tool_calls INTEGER,
    files_read INTEGER,
    cache_hit_rate REAL,
    task_success INTEGER,
    metadata_json TEXT,

    FOREIGN KEY(run_id)
        REFERENCES eval_runs(id)
        ON DELETE CASCADE
);
```

## 8.3 基准任务类型

```text
symbol_lookup
definition_lookup
reference_lookup
callers_lookup
callees_lookup
impact_analysis
trace_path
cross_language_trace
test_discovery
edit_timeout
rename_symbol
```

## 8.4 对比模式

```text
A: 传统 grep/read_file Agent
B: Harness + Code Knowledge + Context Compiler
```

---
