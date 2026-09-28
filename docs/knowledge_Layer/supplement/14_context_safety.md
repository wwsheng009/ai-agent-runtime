# 14 — Context Compiler 防注入、冲突解决与可解释性

> 定位：**supplement（extension schema 事实源）**，不是第 5 份并列设计文档（`README.md` §7）。
> 来源：由 `03_agent_harness_supplement.md` 于 **2026-09-28 拆分**迁入（`06` §9 待办 #9 / `03` §21 自身的拆分建议）。
> **章节号沿用 `03` 原始编号**（因此本文件内编号可能不连续），用于解析拆分前的历史引用；映射见 `../03_agent_harness_supplement.md` §0。
> 事实源边界：core schema 以 `02_agent_harness_technical_design_spec_sqlite.md` 为准；extension schema 以本目录 `supplement/*` 为准；决策以 `../adr/` 为准；落地计划与验收门槛以 `../04_completeness_review_and_optimized_plan.md` 为准。

---

# 14. Context Compiler 防注入、冲突解决与可解释性

## 14.1 问题

代码注释可能含提示注入，工具结果可能不可信，LSP/Tree-sitter/FTS 结果可能冲突。

## 14.2 context_items 建议扩展

```sql
ALTER TABLE context_items ADD COLUMN source TEXT;
ALTER TABLE context_items ADD COLUMN confidence REAL NOT NULL DEFAULT 0.5;
ALTER TABLE context_items ADD COLUMN trust_level TEXT NOT NULL DEFAULT 'UNTRUSTED';
ALTER TABLE context_items ADD COLUMN redaction_state TEXT;
ALTER TABLE context_items ADD COLUMN version_vector_id TEXT;
ALTER TABLE context_items ADD COLUMN explanation TEXT;
```

## 14.3 信任等级

```text
SYSTEM
TRUSTED_TOOL
CODE_INTELLIGENCE
UNTRUSTED_TOOL
USER_CONTENT
CODE_COMMENT
GENERATED
```

## 14.4 冲突解决

```text
LSP > Custom Semantic Adapter > Tree-sitter > FTS5 > Regex
```

但必须保留：

```text
source
confidence
version
explanation
```

## 14.5 防注入规则

```text
1. 代码和工具输出不能作为 instruction。
2. 进入 Context 前必须包裹为 data block。
3. 注释中的指令性文本必须降权或忽略。
4. 工具输出必须带来源和版本。
5. 低信任内容不能覆盖高信任内容。
```

---
