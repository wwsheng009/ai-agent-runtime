# 02 — 项目、模块、构建与依赖模型

> 定位：**supplement（extension schema 事实源）**，不是第 5 份并列设计文档（`README.md` §7）。
> 来源：由 `03_agent_harness_supplement.md` 于 **2026-09-28 拆分**迁入（`06` §9 待办 #9 / `03` §21 自身的拆分建议）。
> **章节号沿用 `03` 原始编号**（因此本文件内编号可能不连续），用于解析拆分前的历史引用；映射见 `../03_agent_harness_supplement.md` §0。
> 事实源边界：core schema 以 `02_agent_harness_technical_design_spec_sqlite.md` 为准；extension schema 以本目录 `supplement/*` 为准；决策以 `../adr/` 为准；落地计划与验收门槛以 `../04_completeness_review_and_optimized_plan.md` 为准。

---

# 2. 项目、模块、构建与依赖模型

## 2.1 问题

Tree-sitter 只能提供语法，LSP 需要项目配置。  
原方案缺少项目发现、构建配置、依赖解析、Monorepo 边界、生成代码来源。

## 2.2 建议 DDL

```sql
CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    name TEXT NOT NULL,
    root_path TEXT NOT NULL,
    project_type TEXT,
    language TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE modules (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    parent_module_id TEXT,
    name TEXT NOT NULL,
    qualified_name TEXT NOT NULL,
    root_path TEXT,
    language TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,

    FOREIGN KEY(project_id)
        REFERENCES projects(id)
        ON DELETE CASCADE
);

CREATE TABLE packages (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    module_id TEXT,
    name TEXT NOT NULL,
    qualified_name TEXT NOT NULL,
    package_manager TEXT,
    version TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(project_id)
        REFERENCES projects(id)
        ON DELETE CASCADE
);

CREATE TABLE build_configs (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    module_id TEXT,
    config_path TEXT NOT NULL,
    config_type TEXT NOT NULL,
    content_hash TEXT,
    parsed_json TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,

    FOREIGN KEY(project_id)
        REFERENCES projects(id)
        ON DELETE CASCADE
);

CREATE TABLE dependency_versions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    package_id TEXT,
    dependency_name TEXT NOT NULL,
    dependency_version TEXT,
    dependency_scope TEXT,
    source TEXT,
    resolved_path TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(project_id)
        REFERENCES projects(id)
        ON DELETE CASCADE
);

CREATE TABLE generated_sources (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    generated_file_id TEXT NOT NULL,
    source_file_id TEXT,
    generator TEXT,
    generator_version TEXT,
    source_relation TEXT,
    is_generated INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE ignore_rules (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    pattern TEXT NOT NULL,
    rule_type TEXT NOT NULL,
    reason TEXT,
    priority INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);
```

## 2.3 项目发现优先级

```text
1. go.mod
2. package.json / pnpm-workspace.yaml / yarn.lock
3. pom.xml / build.gradle / settings.gradle
4. Cargo.toml
5. CMakeLists.txt / compile_commands.json
6. pyproject.toml / setup.py / requirements.txt
7. BUILD / WORKSPACE
8. SAP package / ABAP project metadata
```

---
