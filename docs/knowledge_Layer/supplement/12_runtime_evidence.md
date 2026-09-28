# 12 — 运行时证据接口

> 定位：**supplement（extension schema 事实源）**，不是第 5 份并列设计文档（`README.md` §7）。
> 来源：由 `03_agent_harness_supplement.md` 于 **2026-09-28 拆分**迁入（`06` §9 待办 #9 / `03` §21 自身的拆分建议）。
> **章节号沿用 `03` 原始编号**（因此本文件内编号可能不连续），用于解析拆分前的历史引用；映射见 `../03_agent_harness_supplement.md` §0。
> 事实源边界：core schema 以 `02_agent_harness_technical_design_spec_sqlite.md` 为准；extension schema 以本目录 `supplement/*` 为准；决策以 `../adr/` 为准；落地计划与验收门槛以 `../04_completeness_review_and_optimized_plan.md` 为准。

---

# 12. 运行时证据接口

## 12.1 定位

v1 可不实现，但 UCM 和 API 应预留。

## 12.2 建议 DDL

```sql
CREATE TABLE runtime_evidence (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    evidence_type TEXT NOT NULL,
    source TEXT,
    file_id TEXT,
    symbol_id TEXT,
    payload_json TEXT,
    confidence REAL NOT NULL DEFAULT 0.5,
    observed_at INTEGER,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE runtime_edges (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    from_symbol_id TEXT,
    to_symbol_id TEXT,
    relation_type TEXT NOT NULL,
    call_count INTEGER,
    latency_ms REAL,
    confidence REAL NOT NULL DEFAULT 0.5,
    evidence_id TEXT
);

CREATE TABLE runtime_symbol_stats (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    symbol_id TEXT NOT NULL,
    call_count INTEGER,
    error_count INTEGER,
    avg_latency_ms REAL,
    last_seen_at INTEGER,
    evidence_source TEXT
);
```

## 12.3 合并策略

```text
Static Evidence
+ Runtime Evidence
+ Git Evidence
= Ranked Code Knowledge
```

静态图与运行时图冲突时：

```text
1. 保留两者。
2. 标记 source。
3. 运行时证据提高候选边 confidence。
4. 不直接删除静态边。
```

---
