# 13 — FTS5 多语言检索

> 定位：**supplement（extension schema 事实源）**，不是第 5 份并列设计文档（`README.md` §7）。
> 来源：由 `03_agent_harness_supplement.md` 于 **2026-09-28 拆分**迁入（`06` §9 待办 #9 / `03` §21 自身的拆分建议）。
> **章节号沿用 `03` 原始编号**（因此本文件内编号可能不连续），用于解析拆分前的历史引用；映射见 `../03_agent_harness_supplement.md` §0。
> 事实源边界：core schema 以 `02_agent_harness_technical_design_spec_sqlite.md` 为准；extension schema 以本目录 `supplement/*` 为准；决策以 `../adr/` 为准；落地计划与验收门槛以 `../04_completeness_review_and_optimized_plan.md` 为准。

---

# 13. FTS5 多语言检索

## 13.1 问题

SQLite FTS5 默认分词对中文、下划线、驼峰不友好。

## 13.2 建议

```sql
CREATE VIRTUAL TABLE symbol_fts
USING fts5(
    name,
    qualified_name,
    signature,
    summary,
    normalized_name,
    content='symbols',
    content_rowid='rowid'
);
```

## 13.3 预处理

```text
原始：ToolExecutor.execute
normalized：tool executor execute

原始：getHTTPResponse
normalized：get http response

原始：用户服务
normalized：用户 服务
```

## 13.4 查询语法

```text
kind:method language:go scope:backend timeout
symbol:ToolExecutor.execute
file:executor.go
```

## 13.5 触发器

```sql
CREATE TRIGGER symbols_ai AFTER INSERT ON symbols BEGIN
  INSERT INTO symbol_fts(rowid, name, qualified_name, signature, summary, normalized_name)
  VALUES (new.rowid, new.name, new.qualified_name, new.signature, NULL, new.qualified_name);
END;

CREATE TRIGGER symbols_ad AFTER DELETE ON symbols BEGIN
  INSERT INTO symbol_fts(symbol_fts, rowid, name, qualified_name, signature, summary, normalized_name)
  VALUES('delete', old.rowid, old.name, old.qualified_name, old.signature, NULL, old.qualified_name);
END;

CREATE TRIGGER symbols_au AFTER UPDATE ON symbols BEGIN
  INSERT INTO symbol_fts(symbol_fts, rowid, name, qualified_name, signature, summary, normalized_name)
  VALUES('delete', old.rowid, old.name, old.qualified_name, old.signature, NULL, old.qualified_name);
  INSERT INTO symbol_fts(rowid, name, qualified_name, signature, summary, normalized_name)
  VALUES (new.rowid, new.name, new.qualified_name, new.signature, NULL, new.qualified_name);
END;
```

---
