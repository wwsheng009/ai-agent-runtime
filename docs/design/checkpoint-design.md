# Checkpoint（还原点）设计方案

日期：2026-09-17  
状态：**方案沉淀 / 现状基线**（采集、存储、列表、还原链路均已实现；本文档汇总设计决策、数据模型、配置与已知问题，作为后续演进（user-turn backtrack、统一 artifact 存储）的设计基线）  
范围：`backend/internal/checkpoint`、`backend/internal/artifact`、`backend/internal/agent`（采集挂载）、`backend/internal/chat`（actor Rewind）、`backend/internal/api/skills`（HTTP API）、frontend checkpoint 面板

关联文档（docs/ 索引）：

- `docs/plan/session-user-turn-backtrack-plan.md` —— checkpoint 作为 user-turn backtrack 底座的能力盘点与差距分析（**最完整的外部盘点**）
- `docs/plan/runtime-server-aicli-shared-session-mechanism-plan.md` —— artifact/checkpoint 存储统一规划（`<sessions.dir>/runtime/artifacts.sqlite`），以及"未配置时内存 SQLite"问题
- `docs/multi-agents/teams/design/team4.md`、`team5.md`、`team3.md` —— Checkpoint/Rewind 控制流早期设计（与最终实现高度一致，可当设计史参考）
- `docs/compact/README.md` —— 上下文压缩复用 checkpoint（summary checkpoint）
- `docs/skill_runtime/runtime_operations_api.md` —— checkpoint HTTP API 端点参考

---

## 1. 概述与定位

**checkpoint（还原点）** 是运行时在**会修改文件的工具调用**前后自动拍摄的文件 + 会话快照，用于把工作区/会话回滚到该时刻（`code` 恢复文件、`conversation` 恢复历史、`both` 同时恢复）。

一句话定位：

> **checkpoint rewind 是"工具变更点恢复"；Codex 的 user-turn backtrack 是"用户对话轮次重开"。两者相关但不能等同——checkpoint 是 backtrack 的底层实现底座之一。**

设计取向（Codex-inspired lean defaults）：

- **opt-in**：`checkpoint.enabled` 默认 `false`，文件突变快照默认不落盘（`config/manager.go:448` 注释明确 "Enabled defaults to false so file-mutation blobs are opt-in"）；
- **diff-first**：默认 `storeMode=diff`，只存 before blob + hash +（可选、有上限的）unified diff，不复制 after 全文；
- **按会话限流**：`maxCheckpointsPerSession=50`，每次采集后裁剪，防止无限膨胀；
- **大文件跳过**：超过 `maxFileBytes`（1MB）的文件只记元数据快照，不存内容。

## 2. 设计目标与非目标

目标：

1. 在 mutation 型工具调用前后**自动、无侵入**地记录文件状态（前置条件不满足时零成本）；
2. 支持 `code`（工作区文件回滚）/ `conversation`（会话历史回滚）/ `both` 三种还原模式；
3. 还原前可预览（`PreviewOnly`），还原操作可审计（事件 + hook + frontend audit 面板）；
4. 与上下文压缩（compact）、会话回溯（backtrack）共享同一数据面，避免重复建表。

非目标（当前不做）：

1. git 工作区级完整 time-travel（保持 per-file checkpoint 粒度）；
2. Team / multi-agent 全树级联 rewind 的一次性完美语义；
3. 重写既有 mutation checkpoint 体系；
4. 让 checkpoint 覆盖"纯对话 turn"（无 mutation 时不产生 checkpoint，属已知边界，由 backtrack 补齐）。

## 3. 总体架构

```
┌────────────────────────────────────────────────────────────────┐
│ agent 执行工具（agent/approved_tool.go:225-257）                │
│   ├─ 前置判定：GetCheckpointManager()!=nil 且工具属于             │
│   │   write-like / shell-like / 带 mutation hints                │
│   ├─ BeforeMutation ──► checkpoint.Manager.BeforeMutation         │
│   │     提取 path → 拍 before 快照（内容/hash/存在性）            │
│   │     记录 MessageCount（可选克隆会话历史）                     │
│   ├─ 工具执行                                                │
│   └─ AfterMutation ──► checkpoint.Manager.AfterMutation          │
│         重读 after 快照 → op 推导 → 无变化不建点                  │
│         → artifact.Store.SaveCheckpoint + SaveCheckpointFiles    │
│         → PruneSessionCheckpoints（保留最新 N 个）                │
│         → 发布 checkpoint_created 事件                           │
└──────────────────────────────┬─────────────────────────────────┘
                               ▼
┌────────────────────────────────────────────────────────────────┐
│ artifact.Store（SQLite artifacts.sqlite）                       │
│   checkpoints / checkpoint_files / blobs 三表（migrate v3-v5）   │
└──────────────────────────────┬─────────────────────────────────┘
                               ▼
┌────────────────────────────────────────────────────────────────┐
│ HTTP API（api/skills/checkpoint_handlers.go）                   │
│   GET    /api/runtime/sessions/{id}/checkpoints                 │
│   GET    /api/runtime/sessions/{id}/checkpoints/{id}/files      │
│   POST   /api/runtime/sessions/{id}/checkpoints/{id}/preview    │
│   POST   /api/runtime/sessions/{id}/checkpoints/{id}/restore    │
└──────────────────────────────┬─────────────────────────────────┘
                               ▼
┌────────────────────────────────────────────────────────────────┐
│ SessionActor（chat/actor.go）                                   │
│   RewindTo → handleRewindTo（状态 SessionRewinding）             │
│   → checkpointMgr.Restore（GetCheckpointRestoreManager）        │
│   → applyConversationSnapshot / applyConversationPrefix         │
│   → 发布 rewind_started / rewind_finished + hook                │
└────────────────────────────────────────────────────────────────┘
```

关键设计点：**还原与采集解耦**。`GetCheckpointRestoreManager()` 在采集关闭（`checkpoint.enabled=false`）时仍然可用——只要 artifact store 存在，就能还原历史上已采集的点。因此"还原点列表为空"根因是**采集**没开 + **store 未持久化**，而非还原链路缺失。

## 4. 数据模型

存储介质：SQLite（默认 `artifacts.sqlite`），schema 迁移版本 v3（checkpoints）、v4（checkpoint_files）、v5（blobs），见 `artifact/store.go:858-907`。

### 4.1 checkpoints（还原点主表）

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | TEXT PK | `chk_<uuid>` |
| `session_id` | TEXT NOT NULL | 所属会话 |
| `task_id` | TEXT | 关联任务（可为空） |
| `reason` | TEXT | 采集原因，如 `tool:<name>`、`history_window_summary_segment`（compact） |
| `history_hash` | TEXT NOT NULL | 会话历史 hash（还原一致性校验/复用） |
| `message_count` | INTEGER | 采集时的会话消息数（conversation 还原锚点） |
| `ledger_json` | TEXT NOT NULL | 事实账本（fact ledger）快照 |
| `metadata_json` | TEXT | 元数据：`tool_name`、`tool_call_id`、`store_mode`、`file_count`、`trace_id`、`source_refs`、`directory_roots`、`directory_snapshot_errors`、`conversation_blob_id`、`conversation_message_count` 等 |
| `created_at` | TEXT NOT NULL | 创建时间（列表按 `session_id, created_at DESC` 索引） |

### 4.2 checkpoint_files（还原点文件记录）

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | TEXT PK | |
| `checkpoint_id` | TEXT NOT NULL | 所属还原点（`idx_checkpoint_files_checkpoint`） |
| `path` | TEXT NOT NULL | 文件相对路径 |
| `op` | TEXT NOT NULL | `create` / `update` / `delete` / `noop` |
| `before_blob_id` / `after_blob_id` | TEXT | 指向 blobs 表（diff 模式通常只存 before） |
| `before_hash` / `after_hash` | TEXT | 内容 sha256（内容未变则 noop 丢弃） |
| `diff_text` | BLOB | 可选 unified diff（`maxDiffBytes` 截断，供前端渲染） |

### 4.3 blobs（内容去重存储）

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | TEXT PK | |
| `sha256` | TEXT NOT NULL UNIQUE | 内容指纹，天然去重 |
| `encoding` | TEXT NOT NULL | 编码（如 utf-8 / base64） |
| `data` | BLOB NOT NULL | 文件内容 |

存储模式（`storeMode`）：

- `diff`（默认）：before blob + hash（+ 可选 capped diff_text）；不复制 after 全文，恢复时以"反向回放"推导 after；
- `full`：before + after 全量 blob，便于全文检索与独立还原。

## 5. 配置

### 5.1 CheckpointConfig（`internal/config/manager.go:264-271`）

| YAML 键 | 默认值 | 说明 |
|---|---|---|
| `checkpoint.enabled` | `false` | 是否开启采集（opt-in；关闭时 `GetCheckpointManager()` 返回 nil，永不采集） |
| `checkpoint.maxFileBytes` | `1 MiB` | 超过该大小的文件只记元数据快照，不存内容 |
| `checkpoint.storeMode` | `"diff"` | `diff` \| `full`，非法值校验失败 |
| `checkpoint.conversationSnapshot` | `false` | 是否在采集时克隆整段会话历史为 blob（较贵，默认关） |
| `checkpoint.maxDiffBytes` | `64 KiB` | 单条 diff_text 上限 |
| `checkpoint.maxCheckpointsPerSession` | `50` | 每次采集后保留最新 N 个（0 = 不限） |

校验（`ValidateCheckpointConfig`）：三个上限不能为负；`storeMode` 仅允许 `diff`/`full`/空。

### 5.2 ArtifactConfig（`internal/config/manager.go:251-255`）

| YAML 键 | 默认值 | 说明 |
|---|---|---|
| `artifact.storePath` | 空 | SQLite 文件路径；**为空时每次新建随机内存库（`file:runtime-artifacts-<uuid>?mode=memory&cache=shared`，`artifact/store.go:922 resolveLazyDSN`），跨请求读不到历史** |
| `artifact.storeDSN` | 空 | 显式 DSN（优先于 storePath） |

### 5.3 运行时注入

- 服务端在创建 agent 时对每个 agent 执行 `a.ApplyCheckpointConfig(config.Checkpoint.Enabled, ...)`（`api/skills/handler.go:4876`）；
- `agent/checkpoint_manager.go` 中 `checkpointDisabled=true` → `GetCheckpointManager()` 返回 nil → 采集挂载点前置条件不成立；
- `checkpoint.Manager.NewManager`（`checkpoint/manager.go:49`）带 lean 默认值：`MaxFileBytes=1MB`、`MaxDirectoryFiles=128`、`MaxDirectoryBytes=4MB`、`StoreMode=diff`、`ConversationSnapshot=false`、`MaxDiffBytes=64KB`、`MaxCheckpointsPerSession=50`。

## 6. 采集链路

### 6.1 前置条件（`agent/approved_tool.go:225` 附近）

一次工具调用只有同时满足才进入采集：

1. `agent.GetCheckpointManager()` 非 nil（`checkpoint.enabled=true` 且 agent 已接线 artifact store）；
2. 工具名命中 `policy.IsWriteLikeToolName`（write/edit/patch/apply/delete/remove/rename/move/download 等）；
   或 `policy.IsShellLikeToolName`（shell/bash/exec 等）；
   或工具参数命中 `policy.HasMutationHints`（如 `file_path` / `paths` / `diff` 等字段，`internal/policy/tool_policy.go:410-475`）。

### 6.2 BeforeMutation（`checkpoint/manager.go:83`）

- 从工具参数提取目标路径（`extractPaths`）与目录根（`directoryRootsForFallback`）；
- 逐个文件记录 before 快照：内容、sha256、是否存在；超过 `MaxFileBytes` 或目录超限（`MaxDirectoryFiles=128` / `MaxDirectoryBytes=4MB`）则标记 `Skipped` 并记错误，仍保留元数据条目；
- 记录 `MessageCount`；若 `ConversationSnapshot=true` 则克隆整段会话历史。

### 6.3 AfterMutation（`checkpoint/manager.go:125` 起）

1. 工具报错（toolErr 非空）→ **不建点**；
2. 重读 after 快照，推导 `op`：`create`（before 不存在）／`update`（内容变化）／`delete`（after 不存在）／`noop`；
3. before/after hash 相同的条目丢弃（noop 不建点）；
4. `storeMode=diff`：只存 before blob + hash +（可选 capped diff_text）；`full`：before+after 都存；
5. **没有任何文件变化、没有目录快照错误、且未开启会话快照 → 不建点**（`manager.go:277` 返回空）；
6. 组装 `artifact.Checkpoint`（reason=`tool:<name>`）→ `Store.SaveCheckpoint` + `SaveCheckpointFiles`；
7. 保留策略：`PruneSessionCheckpoints(session, MaxCheckpointsPerSession)` 只保留最新 N 个；
8. 发布 `checkpoint_created` 事件（前端据此刷新列表）。

### 6.4 采集的"盲区"（设计边界）

- 纯对话 turn（无 mutation 工具）不产生 checkpoint —— 由 user-turn backtrack 能力补齐；
- 仅 `ls`/`read` 等只读工具调用不产生 checkpoint；
- 工具执行报错不建点（避免记录半完成状态）。

## 7. 存储链路

- `artifact.Store` 打开时执行 migration（v3/v4/v5），建 `checkpoints` / `checkpoint_files` / `blobs` 三表 + 索引；
- SQLite 打开参数包含 WAL、busy_timeout、auto_vacuum 等（锁行为见 `docs/ANALYSIS-sqlite-lock-problem.md`，注意其中 "checkpoint" 指 SQLite WAL checkpoint，与本功能无关）；
- **持久化前提**：必须配置 `artifact.storePath`（或 storeDSN）。服务端未配置时 `resolveLazyDSN` 每次新建随机内存库 → 采集写进去的数据在下一个请求/进程就不可见 —— 这是"还原点列表为空"的根因之一（另一根因是 `checkpoint.enabled=false`）；
- 迁移原则（见 runtime-server 共享会话计划）：checkpoint、memory ledger、tool artifact refs 应进入**统一 artifact store**，避免多份 sqlite 分叉。

## 8. 查询/列表链路

- API：`GET /api/runtime/sessions/{id}/checkpoints?limit=&offset=`（`api/skills/checkpoint_handlers.go:109`）；
- 活跃会话（在 session hub 中）→ `actor.ListCheckpoints`（`chat/actor.go:653`）→ `agent.GetArtifactStore().ListCheckpoints`；
- 非活跃会话 → `openCheckpointReadService` 按 storePath 打开 artifact store 查询；
- 列表 SQL（`artifact/store.go:753`）：`WHERE session_id=? ORDER BY created_at DESC, rowid DESC LIMIT ? OFFSET ?`，返回 summary（id/session_id/reason/message_count/created_at/metadata/provenance 等）；
- 文件明细：`GET .../checkpoints/{id}/files`（diff 模式下普通文件条目不带全文，只有 hash + 可选 diff_text）。

## 9. 还原链路

### 9.1 预览（PreviewOnly）

`POST .../checkpoints/{id}/preview` → `checkpointMgr.Restore(PreviewOnly=true)`（`checkpoint/manager.go:420`）：

- `code` 模式：取目标点**之后**的所有 checkpoint（`checkpointsAfterTarget`，manager.go:696），逐文件给出 `restore`（写回 before 内容）或 `delete`（目标点之后才创建的文件）预览；
- `conversation` 模式：按 checkpoint 记录的 `message_count`（有 conversation blob 则精确条数）给出"回退到 N 条消息"预览。

### 9.2 确认还原

`POST .../checkpoints/{id}/restore`（mode = `code` / `conversation` / `both`，`checkpoint_handlers.go:206-326`）：

1. 要求会话在 hub 中，先 `acquireSessionLease` 拿会话租约（避免与正在运行的 web turn 冲突）；
2. `actor.Rewind(checkpointID, mode)`（`chat/actor.go:1339 handleRewindTo`）：
   - 状态置 `SessionRewinding`，发布 `rewind_started`；
   - `agent.GetCheckpointRestoreManager()` —— **采集关闭也可用**（还原不依赖采集开关）；
   - `Restore()`：
     - `code`：**反向回放**目标点之后所有 checkpoint 的文件变更（`checkpointsAfterTarget` 逆序：before 写回、后建文件删除）——因为"回到旧点"意味着撤销之后的一切改动；
     - `conversation`：精确（blob 整体替换，`applyConversationSnapshot`）或按条数截断（`applyConversationPrefix`），重写 session runtime 历史；
   - 发布 `rewind_finished`（含 applied_paths/errors/conversation_changed/conversation_head/conversation_exact）+ hook `EventRewindCompleted`；
3. 前端据此重同步消息列表与文件面板。

## 10. 其他 checkpoint 生产者与易混概念

`checkpoints` 表并非只有"还原点面板"写入，以下机制也写 checkpoint，阅读时注意区分 reason：

| 生产者 | reason / 形态 | 说明 |
|---|---|---|
| mutation 采集（本方案主路径） | `tool:<name>` | 还原点面板展示的数据源 |
| 上下文压缩（compact） | `history_window_summary_segment` | 压缩 summary 落到 artifact checkpoint，相同历史片段再次压缩时**复用**已有 summary checkpoint，避免重复本地总结（`docs/compact/README.md`）；replacement history 携带 `checkpoint_id` |
| 会话回溯（backtrack） | tombstone + 审计 | 按 user-turn 锚点截断历史，可联动 code restore；编排层调用 checkpoint 但不取代它 |
| 历史持久化 checkpoint（易混） | `OnHistoryCheckpoint` / `CheckpointInterval` | 这是**会话历史周期性落库**（`session.checkpoint_persist_error` 事件），不是文件还原点（`docs/analysis/aicli-long-turn-mid-turn-persistence.md`） |
| multi-agent 执行 checkpoint（易混） | `execution_checkpoints` | 子任务进度断点，用于可靠性恢复，不是文件还原点（`docs/plan/aicli-agent-runtime-reliability-optimization-plan.md`） |
| SQLite WAL checkpoint（易混） | `wal_checkpoint` | 数据库机制，与本功能无关（`docs/ANALYSIS-sqlite-lock-problem.md`） |

## 11. 已知问题与运营建议

1. **采集默认关闭**：`checkpoint.enabled=false` → `GetCheckpointManager()` 恒 nil → 永不采集。要开启还原点，需在生效配置（如 `backend/configs/runtime.yaml`）增加 `checkpoint:` 段并设 `enabled: true`（可选 `storeMode`、`maxCheckpointsPerSession` 等）。
2. **artifact store 未持久化**：server 模式未配 `artifact.storePath`/`storeDSN` 时，每次请求新建随机内存库，跨请求/跨进程读不到历史。建议配 `<sessions.dir>/runtime/artifacts.sqlite`（与 runtime-server 共享会话计划一致）。
3. **即使开启采集，"纯查看"会话也没有还原点**：只有 mutation 型工具调用（且文件真的变化、工具未报错）才建点。
4. **多进程共享 sqlite 锁**：aicli CLI 与 runtime-server 同时写 artifacts.sqlite 可能锁冲突（WAL + busy_timeout 缓解，详见 SQLite 锁分析文档）；`Close()` 的 `wal_checkpoint(TRUNCATE)` 已降级 PASSIVE。
5. **测试残留库勿混淆**：`output/real-test/sessions/runtime/artifacts.sqlite` 是旧测试残留（checkpoints=0），与正式服务器无关。
6. **前端展示依赖事件**：`checkpoint_created` / `rewind_started` / `rewind_finished` 事件驱动面板刷新；事件丢失时列表靠轮询/重载兜底（`use-runtime-checkpoints`）。

## 12. 代码与文档索引

代码：

- `backend/internal/checkpoint/manager.go` —— Manager：BeforeMutation / AfterMutation / Restore（预览+执行）、`checkpointsAfterTarget`
- `backend/internal/checkpoint/types.go` —— FileSnapshot / PendingCheckpoint / RestoreRequest / RestoreResult
- `backend/internal/agent/approved_tool.go:225-257` —— 采集挂载点（前置判定 + Before/AfterMutation 调用）
- `backend/internal/agent/checkpoint_manager.go` —— `GetCheckpointManager` / `GetCheckpointRestoreManager`
- `backend/internal/artifact/store.go` —— SQLite store（schema v3-v5、ListCheckpoints:753、resolveLazyDSN:922）
- `backend/internal/artifact/checkpoint_files.go` —— checkpoint_files 记录模型
- `backend/internal/api/skills/checkpoint_handlers.go` —— HTTP API（list/files/preview/restore）
- `backend/internal/chat/actor.go:653-664, 1339-1433` —— ListCheckpoints / handleRewindTo / applyConversationRestore
- `backend/internal/policy/tool_policy.go:410-475` —— 写工具判定（IsWriteLikeToolName / IsShellLikeToolName / HasMutationHints）
- `backend/internal/config/manager.go:251-271, 448-455` —— ArtifactConfig / CheckpointConfig 默认值与校验
- `backend/internal/sessionruntime/paths.go:127-131` —— ArtifactStorePath 解析

文档：

- `docs/plan/session-user-turn-backtrack-plan.md`（能力盘点与差距）
- `docs/plan/runtime-server-aicli-shared-session-mechanism-plan.md`（存储统一规划）
- `docs/multi-agents/teams/design/team4.md` / `team5.md` / `team3.md`（早期设计）
- `docs/compact/README.md`（压缩复用 checkpoint）
- `docs/skill_runtime/runtime_operations_api.md`（API 端点）
- `docs/ANALYSIS-sqlite-lock-problem.md`（SQLite WAL 锁，注意"checkpoint"一词系 WAL 机制）
- `docs/analysis/aicli-long-turn-mid-turn-persistence.md`（历史持久化 checkpoint，易混概念）
- `docs/plan/workspace-right-panel-file-browser-and-git-diff-plan.md`（frontend checkpoint 面板与 diff 先例）
- `docs/plan/frontend-deepseek-harness-optimization-plan.md`（frontend checkpoint 资产清单）

## 13. 演进方向

1. **统一 artifact 存储**：`artifact.storePath` 缺省补 `<sessions.dir>/runtime/artifacts.sqlite`，与 runtime state/team/AgentControl/background 一起消除"默认内存"分叉（按 runtime-server 共享会话计划落地）；
2. **user-turn backtrack 底座**：把 checkpoint 的 code/conversation restore 作为 backtrack 的可选实现底座，对外暴露 turn 语义（`POST /api/runtime/sessions/{id}/backtrack`），而非强迫用户理解 `checkpoint_id`；
3. **保留策略增强**：支持"每 N 次 mutation 做 anchor snapshot"或时间维度合并，避免还原点过多时回放链过长；
4. **前端面板注册表化**：checkpoint surface 与 artifacts/plan/usage 收敛为标签注册表（见 frontend 重构计划），diff 渲染器与 Git diff 共用；
5. **多会话/团队级联 rewind**：从单 session 扩展到 child/team 树（当前明确为非目标，列为后续）。
