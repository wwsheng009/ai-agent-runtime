# 07 — 安全、隐私、沙箱与审计

> 定位：**supplement（extension schema 事实源）**，不是第 5 份并列设计文档（`README.md` §7）。
> 来源：由 `03_agent_harness_supplement.md` 于 **2026-09-28 拆分**迁入（`06` §9 待办 #9 / `03` §21 自身的拆分建议）。
> **章节号沿用 `03` 原始编号**（因此本文件内编号可能不连续），用于解析拆分前的历史引用；映射见 `../03_agent_harness_supplement.md` §0。
> 事实源边界：core schema 以 `02_agent_harness_technical_design_spec_sqlite.md` 为准；extension schema 以本目录 `supplement/*` 为准；决策以 `../adr/` 为准；落地计划与验收门槛以 `../04_completeness_review_and_optimized_plan.md` 为准。

---

# 7. 安全、隐私、沙箱与审计

## 7.1 必须补充

```text
敏感文件忽略
密钥扫描
上下文脱敏
工具权限
LSP / parser 子进程沙箱
SQLite 注入防护
路径遍历防护
多租户隔离
审计日志
提示注入防护
```

## 7.2 建议 DDL

```sql
CREATE TABLE security_redactions (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    file_id TEXT,
    symbol_id TEXT,
    redaction_type TEXT NOT NULL,
    pattern TEXT,
    replacement TEXT,
    reason TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE tool_permissions (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    permission_scope TEXT NOT NULL,
    allowed INTEGER NOT NULL DEFAULT 0,
    constraints_json TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE audit_log (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    task_id TEXT,
    actor TEXT,
    action TEXT NOT NULL,
    target_type TEXT,
    target_id TEXT,
    metadata_json TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);
```

## 7.3 默认忽略

```text
.git
.env
.env.*
*.pem
*.key
*.crt
secrets/
credentials/
node_modules/
vendor/
dist/
build/
target/
.venv/
__pycache__/
generated/
```

## 7.4 上下文安全规则

```text
1. 代码内容进入 LLM 前必须标记为 data，不是 instruction。
2. 工具输出必须带 source、version、confidence。
3. 不允许代码注释覆盖 system/developer instruction。
4. 敏感字段进入 Context 前必须 redaction。
5. 每次工具调用写 audit_log。
```

---
