# 09 — 动态候选与不确定图

> 定位：**supplement（extension schema 事实源）**，不是第 5 份并列设计文档（`README.md` §7）。
> 来源：由 `03_agent_harness_supplement.md` 于 **2026-09-28 拆分**迁入（`06` §9 待办 #9 / `03` §21 自身的拆分建议）。
> **章节号沿用 `03` 原始编号**（因此本文件内编号可能不连续），用于解析拆分前的历史引用；映射见 `../03_agent_harness_supplement.md` §0。
> 事实源边界：core schema 以 `02_agent_harness_technical_design_spec_sqlite.md` 为准；extension schema 以本目录 `supplement/*` 为准；决策以 `../adr/` 为准；落地计划与验收门槛以 `../04_completeness_review_and_optimized_plan.md` 为准。

---

# 9. 动态候选与不确定图

## 9.1 问题

动态语言、反射、依赖注入、回调、事件、RPC 无法静态确定调用目标。

## 9.2 建议 DDL

```sql
CREATE TABLE call_candidates (
    id TEXT PRIMARY KEY,
    call_id TEXT NOT NULL,
    candidate_symbol_id TEXT,
    candidate_name TEXT,
    confidence REAL NOT NULL DEFAULT 0.5,
    reason TEXT,

    FOREIGN KEY(call_id)
        REFERENCES calls(id)
        ON DELETE CASCADE
);

CREATE TABLE reference_candidates (
    id TEXT PRIMARY KEY,
    reference_id TEXT NOT NULL,
    candidate_symbol_id TEXT,
    candidate_name TEXT,
    confidence REAL NOT NULL DEFAULT 0.5,
    reason TEXT,

    FOREIGN KEY(reference_id)
        REFERENCES references(id)
        ON DELETE CASCADE
);
```

## 9.3 解析状态

```text
RESOLVED
CANDIDATE
AMBIGUOUS
DYNAMIC
EXTERNAL
UNRESOLVED
```

## 9.4 置信度示例

```json
{
  "call": "obj.execute()",
  "resolved_target": null,
  "candidates": [
    {"symbol": "Foo.execute", "confidence": 0.63},
    {"symbol": "Bar.execute", "confidence": 0.41}
  ],
  "resolution_status": "CANDIDATE"
}
```

---
