# 01 — 符号身份：稳定符号 ID 与重命名追踪

> 定位：**supplement（extension schema 事实源）**，不是第 5 份并列设计文档（`README.md` §7）。
> 来源：由 `03_agent_harness_supplement.md` 于 **2026-09-28 拆分**迁入（`06` §9 待办 #9 / `03` §21 自身的拆分建议）。
> **章节号沿用 `03` 原始编号**（因此本文件内编号可能不连续），用于解析拆分前的历史引用；映射见 `../03_agent_harness_supplement.md` §0。
> 事实源边界：core schema 以 `02_agent_harness_technical_design_spec_sqlite.md` 为准；extension schema 以本目录 `supplement/*` 为准；决策以 `../adr/` 为准；落地计划与验收门槛以 `../04_completeness_review_and_optimized_plan.md` 为准。

---

# 1. 稳定符号 ID 与重命名追踪

## 1.1 问题

原方案中 `symbols.id` 是 TEXT PRIMARY KEY，但没有定义生成策略。  
文件修改、符号移动、重命名后，引用、缓存、Context 容易失效或错乱。

## 1.2 设计原则

稳定符号 ID 不应只依赖行号，也不应只依赖 `qualified_name`。

推荐：

```text
stable_key =
  language
+ kind
+ namespace/package/module
+ owner_qualified_name
+ qualified_name
+ signature_hash
```

对于支持重载的语言，必须把 `signature_hash` 纳入。

## 1.3 建议 DDL

```sql
ALTER TABLE symbols ADD COLUMN stable_key TEXT;
ALTER TABLE symbols ADD COLUMN signature_hash TEXT;
ALTER TABLE symbols ADD COLUMN owner_symbol_id TEXT;
ALTER TABLE symbols ADD COLUMN content_hash TEXT;
ALTER TABLE symbols ADD COLUMN language_extension_json TEXT;

CREATE UNIQUE INDEX idx_symbols_stable_key
ON symbols(workspace_id, stable_key);

CREATE TABLE symbol_aliases (
    id TEXT PRIMARY KEY,
    symbol_id TEXT NOT NULL,
    alias_key TEXT NOT NULL,
    alias_type TEXT NOT NULL,
    valid_from_version INTEGER,
    valid_to_version INTEGER,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(symbol_id)
        REFERENCES symbols(id)
        ON DELETE CASCADE
);

CREATE INDEX idx_symbol_aliases_symbol
ON symbol_aliases(symbol_id);

CREATE INDEX idx_symbol_aliases_key
ON symbol_aliases(alias_key);

CREATE TABLE symbol_renames (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    old_symbol_id TEXT,
    new_symbol_id TEXT,
    old_qualified_name TEXT,
    new_qualified_name TEXT,
    file_id TEXT,
    detected_by TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 0.5,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE INDEX idx_symbol_renames_workspace
ON symbol_renames(workspace_id);
```

## 1.4 规则

```text
1. 符号内容变化但语义身份未变：stable_key 不变，version 增加。
2. 符号重命名：新增 symbol_aliases，必要时写 symbol_renames。
3. 符号移动：stable_key 尽量不变，file_id 更新。
4. 符号删除：不立即物理删除，标记 deleted_at / status。
5. 引用和缓存必须绑定 symbol_version，而不是只绑定 symbol_id。
```

---
