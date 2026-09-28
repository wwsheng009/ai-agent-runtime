# 15 — 变更管理边界补充

> 定位：**supplement（extension schema 事实源）**，不是第 5 份并列设计文档（`README.md` §7）。
> 来源：由 `03_agent_harness_supplement.md` 于 **2026-09-28 拆分**迁入（`06` §9 待办 #9 / `03` §21 自身的拆分建议）。
> **章节号沿用 `03` 原始编号**（因此本文件内编号可能不连续），用于解析拆分前的历史引用；映射见 `../03_agent_harness_supplement.md` §0。
> 事实源边界：core schema 以 `02_agent_harness_technical_design_spec_sqlite.md` 为准；extension schema 以本目录 `supplement/*` 为准；决策以 `../adr/` 为准；落地计划与验收门槛以 `../04_completeness_review_and_optimized_plan.md` 为准。

---

# 15. 变更管理边界补充

## 15.1 必须覆盖

```text
文件创建
文件修改
文件删除
文件重命名
文件移动
文件复制
分支切换
merge
rebase
cherry-pick
子模块
Git LFS
编码变化
换行符变化
符号链接
权限变化
```

## 15.2 建议 DDL

```sql
CREATE TABLE change_events (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    path TEXT,
    old_path TEXT,
    new_path TEXT,
    old_hash TEXT,
    new_hash TEXT,
    source TEXT,
    detected_at INTEGER NOT NULL,
    processed_at INTEGER,
    status TEXT NOT NULL DEFAULT 'PENDING',

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE git_sync_state (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    repository_id TEXT NOT NULL,
    indexed_commit TEXT,
    current_commit TEXT,
    indexed_branch TEXT,
    current_branch TEXT,
    working_tree_hash TEXT,
    last_sync_at INTEGER,
    status TEXT,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);
```

---

## 15.3 索引任务表 `index_jobs`（extension schema）

> **来源（2026-09-28）**：本表 DDL 原先错放在 `04_completeness_review_and_optimized_plan.md` §4.3
> （ADR-0007 证据 3 的 **DDL 越位**）。ADR-0007 §4.3 要求迁入 extension schema；该 ADR 于
> 2026-09-28 被 Accept 后执行迁移，`04` 相应段落改为引用（只保留用途与验收指标）。
> **注意**：与 §15.2 不同，本表是 **v1（Phase 1）交付**，不是 v2+ 推迟项。

```sql
CREATE TABLE index_jobs (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL,
    kind          TEXT NOT NULL,           -- light|deep|full_rebuild|gc
    status        TEXT NOT NULL,           -- queued|running|done|failed|cancelled
    files_total   INTEGER NOT NULL DEFAULT 0,
    files_done    INTEGER NOT NULL DEFAULT 0,
    error         TEXT,
    started_at    INTEGER,
    finished_at   INTEGER,
    FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
);
CREATE INDEX idx_index_jobs_ws ON index_jobs(workspace_id, status);
```

**语义**

- 本表是 owner（单写者）的**持久化写队列**：`kind=light` 由 agent 编辑入队，`deep` 由 debounce + 定期 reconcile 入队，`full_rebuild` / `gc` 由显式维护命令入队；owner 内单 goroutine 串行消费（`04` §4.7）。
- **不得绕过本表直接写 `symbols`**：任何变更路径（agent 编辑钩子 / `git diff` / fsnotify）都必须先写 `index_jobs`，再串行执行（`04` §4.1 的 Incremental 原则修改）。
- 任务必须**幂等、可重放**（本目录 `06_cache_consistency.md` §6.4 规则 5）。

**验收指标**：见 `04` §7.4（首次全量索引、单文件增量 p95）与 §7.6（校准口径）；本表 `started_at` / `finished_at` / `files_total` / `files_done` 为数据来源。
