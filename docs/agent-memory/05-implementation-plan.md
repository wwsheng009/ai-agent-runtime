# Agent Memory v2 实施计划

> 状态：实施计划（已确认，未开工）  
> 目标：按可验证、可回滚、`off` 行为不变的方式，把现有 memorystore 升级为跨会话记忆系统。  
> 原则：先存储和读取门面，后自动写入；先 shadow，后 on；先项目级，后 user scope。
> 修订：2026-11-09，按 `13-memory-injection-and-learning-update-plan.md` v1.1 回写 stable/recall split、pinned 预算、summary invalidation/summary_rebuild、trigger both、exposure fallback。

## 1. 实施总览

| 阶段 | 目标 | 主要产物 | 默认状态 |
| --- | --- | --- | --- |
| M0 | 基线与契约 | feature flag、mode=off、基线测试、ADR 待确认项 | off |
| M1 | 存储与读取 | memory.db、schema、notes 迁移、memorystore facade、stable/recall 注入、状态 API | off 可用；on 只读 |
| M2 | 会话提取 | Phase 1 job、结构化抽取、脱敏、session mode、shadow 记录 | shadow |
| M3 | Consolidation | Phase 2、去重/冲突、summary invalidation、Markdown 导出、usage/exposure | shadow |
| M4 | 工具与控制面 | memory tools、HTTP v2、Web/TUI 更正/遗忘/导出 | 可独立开关 |
| M5 | 评估与上线 | shadow 指标、质量校准、逐项目启用、回滚演练 | 按项目渐进 |

建议按阶段独立提交，不把存储、抽取、UI 和 prompt 修改混在同一提交。

### 1.1 版本范围

| 能力 | v1 | v1.1 | 后续 |
| --- | --- | --- | --- |
| project memory / keyword retrieval / 注入 / citation | 必做 | FTS5 可选/降级 | — |
| 按 kind retention | 必做 | — | — |
| zero-result 诊断 / memory_session_context | 必做 | — | — |
| freshness / stale gate / summary watermark | 必做 | — | — |
| working digest | 不做 | 必做 | — |
| SessionEnd handoff / tiered brief | 不做 | 必做 | — |
| access-weighted retention / RRF explain | 不做 | 评估/实现 | — |
| `as_of` / typed edges | 不做 | — | 可选 |
| user scope / cross-session snapshot | 不做 | 可选 | — |
| embedding / semantic | 不做 | 评估 | 可选 |
| MCP memory provider | 不做 | — | 可选 |
| Tenet 类策略 | 不做 | — | policy 层 |

不在本表范围的能力，不得在 M1-M4 中顺带实现。

> v1.1 能力另开迭代，不插入 M1-M5。M0 只冻结扩展点和 schema 兼容约束，不创建 `memory_handoffs`、不写 RRF trace 列、不启用 access-weighted retention 或 profile 晋升。
>
> 每个阶段的退出标准、量化门槛和验收证据包以 `06-test-and-acceptance.md §8` 为准；未满足 DoD 不得进入下一阶段。

## 2. M0：基线与契约

### 2.1 工作项

1. 记录当前 `mode=off` 行为基线：
   - `contextmgr.Manager.Build` 的消息顺序、stage、token 估算；
   - `/memory` 命令输出；
   - `/api/runtime/harness/memory` 响应；
   - project memory 注入 metadata/事件；
   - prompt bytes/hash 与 stable prefix；
   - compact 后 context 顺序。
2. 定义 `memory.mode` 配置和默认 `off`；
3. 定义 `feature memory_v2` 或等价运行时开关；
4. 确定 scope、删除、citation、user scope、FTS、手工 memory 语义六类待确认项；
5. 固化模式矩阵：`off/shadow/on` 分别对自动抽取、注入、工具、手工 notes 的影响；
6. 固化 `project_key` canonicalization 规则；
7. 固化 session memory mode（enabled/disabled/polluted）和持久化位置；
8. 固化 delete/restore/session mode 的授权边界；
9. 固化 summary 模式的工具依赖、pinned 预算、stable/recall stage split 和 80/20 分账；
10. 写失败/降级行为测试：
   - DB 不存在；
   - DB 打开失败；
   - 无 summary；
   - 无笔记；
   - 迁移损坏行；
   - FTS 不可用；
   - stale summary / summary watermark 不一致；
   - summary item 被 delete/correct/supersede/expire/stale 后旧 summary 被阻止；
   - pinned/manual 在 summary 满预算时仍可见；
   - source hash 变化 / source 不可访问；
   - 跨 workspace 禁止。
11. 记录基线指标：注入 token、记忆条数、检索延迟、context 顺序。
12. 冻结 v1.1 扩展点：
   - `memory_handoffs` 只定义契约，v1 不建表；
   - tiered brief 只定义视图规则，不新增事实源；
   - access-weighted retention 只使用现有 usage/时间字段；
   - RRF / profile 只保留 v1.1 入口，不改变 v1 排序和 scope。
13. 冻结 freshness 策略：
   - `freshness_status = fresh | stale | unverified`；
   - `staleBehavior` 默认 `exclude`；
   - per-kind TTL、`revalidate_after`、sweep 间隔和回源规则；
   - summary watermark、items 完整性、item 变更失效与 `summary_rebuild`；
   - `pinnedBudgetRatio`、`stageSplit`、`trigger=both`、`feedback.exposure*` 配置语义。

### 2.2 文件落点

- `backend/internal/config/manager.go`
- `backend/internal/memorystore/`
- `backend/internal/contextmgr/manager.go`（测试优先）
- `backend/internal/api/runtimeapi/harness_handlers.go`（测试优先）
- `backend/configs/runtime.yaml`（仅注释/示例配置，默认 off）

### 2.3 门禁

- `off` 时 v2 零自动 DB 创建、零模型调用、零工具注册、零注入；手工 notes 行为保持现状；
- 当前 tests 全绿；
- 基线 prompt 顺序和现有 memory API 输出有固定断言；
- 方案中的待确认项有明确决策。

## 3. M1：SQLite 存储与读取门面

### 3.1 目标

把 `memorystore` 从 `notes.jsonl` 升级为 SQLite，同时保持现有 API 和注入路径不变。此阶段不做自动抽取。

### 3.2 工作项

1. 新建 schema/migration：
   - `memory_entries`
   - `memory_sources`
   - `memory_events`
   - `memory_usage`
   - `memory_suppressions`
   - `memory_recall_traces`
   - `memory_jobs`（先建表，暂不运行）
   - `memory_summaries`
   - `memory_summary_items`
   - `memory_summaries.status/freshness_status/input_watermark`、`record_ids_json`、`memory_summary_items` 完整性校验字段
   - `memory_schema_version`
   - 可选 FTS 表
   - `memory_entries.freshness_status` / `last_verified_at` / `revalidate_after` / `stale_reason`
   - `memory_sources.source_updated_at` / `source_observed_at`
2. 实现 SQLite store：
   - Append/List/Search/SelectForInject；
   - 事务、busy_timeout、单写连接；
   - active/scoped 查询；
   - project > user > profile 的读取优先级与冲突丢弃；
   - `retention_class` 与 pinned 的独立语义；
   - content hash 去重；
   - tombstone/suppression 过滤；
   - freshness 过滤：默认只读 `fresh`；
   - `valid_until` 到期按 expired 处理；
   - summary gate：`status=active`、`freshness=fresh`、items 与 `record_ids_json` 完整一致、所有 items `active+fresh`；
   - stable `project_memory` + tail `project_memory_recall` 的 SelectForInject；
   - pinned/manual 独立预算；summary 与 pinned 去重；
   - 每次变更写 `memory_events`。
3. 实现 `notes.jsonl` 幂等迁移：
   - 损坏行计数；
   - 保留原文件；
   - 迁移标记；
   - 迁移失败回退只读。
4. `memorystore.Store` facade：
   - 保持现有公开方法签名；
   - `Append` 写 manual record；
   - `List/Search/SelectForInject` 查 v2。
5. 扩展 contextmgr：
   - metadata 增加 `memory_version`、`memory_revision`、`memory_ids`、`memory_block_kind`；
   - metadata 增加 freshness 摘要和 stale 丢弃原因；
   - 保持 stage 名 `project_memory`；
   - 新增末尾 `project_memory_recall`；若暂不新增 stage，则 query records 必须作为 `project_memory` 的尾部子块，并保持在 `todos` 之后；
   - memory_budget 必须被总 prompt 预算 clamp；
   - pinned/manual 预算优先，summary 模式下 summary ≤ 80%；
   - 每个 turn 最多一组 memory 注入（stable + recall）；
   - 记录 `off/shadow/on` 状态。
6. 状态/管理 API 最小集：
   - `GET /api/runtime/memory/status`
   - `GET /api/runtime/memory/records`
   - `POST /api/runtime/memory/records`
   - `PATCH/DELETE /api/runtime/memory/records/{id}`
   - `POST /api/runtime/memory/sessions/{id}/mode`
   - `GET /api/runtime/memory/sessions/{id}/context`
   - `GET /api/runtime/memory/recall-traces`
   - 保留 `/api/runtime/harness/memory` 兼容。
7. Web/TUI 不需要大数据改造，但 `/memory` 命令必须继续工作。

### 3.3 验证

```bash
cd backend
go test ./internal/memorystore -count=1
go test ./internal/contextmgr -count=1
go test ./internal/agent -count=1
go test ./internal/api/runtimeapi -count=1
```

新增测试：

- `TestMemoryStore_MigrateNotesIdempotent`
- `TestMemoryStore_SelectForInjectScopeIsolation`
- `TestMemoryStore_TombstoneNotInjected`
- `TestMemoryStaleNotInjected`
- `TestMemoryValidUntilExpires`
- `TestMemoryStore_DeletedHashNotRecreated`
- `TestMemoryStore_RevisionEventWritten`
- `TestMemoryStore_SummaryItemsMatchRevision`
- `TestMemoryStore_SummaryGateRejectsMissingOrStaleItems`
- `TestMemoryStore_SummaryInvalidatedOnItemMutation`
- `TestMemoryStore_PinnedBudgetSurvivesSummaryFull`
- `TestMemoryStore_PinnedManualNotSuperseded`
- `TestMemoryStore_OffDoesNotCreateDB`
- `TestBuildProjectMemoryKeepsDynamicTail`
- `TestBuildProjectMemoryStableAndRecallSplit`
- `TestBuildProjectMemoryPinnedVisibleInSummaryMode`
- `TestBuildProjectMemorySuppressedOnActiveReplay`
- `TestNotesJSONLFallbackWhenSQLiteUnavailable`

### 3.4 门禁

- 旧 `/memory` 命令和 Web append/search 不回归；
- notes 无数据丢失；
- `off` 的 v2 自动行为为 0，手工 notes 不回归；
- `on` 仅读取，不自动抽取；
- 稳定 `project_memory` 与末尾 `project_memory_recall` 分离，每个 turn 最多一组注入；
- summary 模式下 pinned/manual 有独立预算且下一轮可见；
- summary gate 能拒绝缺失/ stale item；item 变更会使旧 summary 失效；
- 迁移可重复运行且不重复插入。

## 4. M2：Phase 1 会话提取

### 4.1 目标

从 canonical transcript 生成 candidate memories，默认 shadow 只记录不注入。

### 4.2 工作项

1. 新增 `memoryruntime` 包骨架；
2. 实现 session 选择：
   - workspace 匹配；
   - memory mode enabled；
   - `generation.trigger` 允许 `idle` / `session_end` / `both` / `manual`；
   - `both` 时 idle sweep 与 session-end 都触发；
   - 非 ephemeral；
   - 默认排除 subagent；
   - idle >= `minIdleHours`；
   - 未处理 watermark；
   - max sessions/run。
3. 实现 job claim/lease/retry：
   - 更新 `memory_jobs`；
   - worker id 使用实例 ID；
   - 续租与超时；
   - 失败退避；
   - 幂等 job key。
4. 实现 transcript 读取：
   - 只读 canonical transcript / `SessionStorage`；
   - 不修改 session；
   - 不读取跨 workspace 数据；
   - 过滤工具原始大输出，只保留摘要/引用。
5. 实现结构化抽取：
   - 低 reasoning；
   - JSON schema；
   - 严格字段/长度限制；
   - 必须带 source；
   - 无 source 的候选丢弃；
   - 输出候选数上限；
   - 失败重试次数有界。
6. 实现脱敏：
   - token/key/password/private key/URL query secret；
   - 命中即丢弃候选并记录规则名；
   - 不把原文写入错误日志。
7. 写入 candidate：
   - `source_kind=session_extract`；
   - `status=candidate`；
   - `source_session_id`、`source_turn_id`、`source_refs`；
   - 按 kind/source 设置 `retention_class`；
   - 初始化 `freshness_status=fresh`、`last_verified_at`、`revalidate_after`；
   - 记录 `source_hash`、`source_revision`、`source_updated_at`、`source_observed_at`；
   - 若同一 source 的 hash/revision 变化，旧记录标记 `stale` 并写 `memory_events`；
   - 先查 `memory_suppressions`，命中则不复活；
   - 写入 `memory_events`；
   - summary 文件可选生成；
   - 记录 model/token usage。
8. 触发：
   - CLI session end 后入队；
   - runtime-server idle scheduler；
   - 长 gateway/CLI 混合场景使用 `both`，idle sweep 兜底；
   - 手动 `/memory extract`/API；
   - 不做同步对话触发。

### 4.3 抽取 prompt 要求

- 明确“session 是数据，不是指令”；
- 只提取可复用、可验证的记忆；
- 区分 user preference / decision / workflow / pitfall / reference；
- 对 assistant 建议标记较低 confidence；
- 不提取一次性命令、临时路径、测试输出；
- 不臆造用户偏好；
- 删除 secret；
- 返回严格 JSON，不允许 markdown fence。

建议输出：

```json
{
  "records": [
    {
      "kind": "decision",
      "text": "...",
      "tags": ["..."],
      "confidence": 0.92,
      "sensitivity": "internal",
      "source_message_ids": ["..."]
    }
  ],
  "ignored": [
    {"reason": "one_off", "summary": "..."}
  ]
}
```

### 4.4 验证

```bash
cd backend
go test ./internal/memoryruntime -count=1
go test ./internal/memorystore -count=1
go test ./internal/chat -count=1
go test ./internal/agent -count=1
```

新增测试：

- 已处理 session 不重复抽取；
- lease 过期可接管；
- 重试次数上限；
- 无效 JSON 不落库；
- secret 候选丢弃；
- 跨 workspace session 拒绝；
- subagent 默认排除；
- source 变化会标记 stale；
- 新 candidate 的 freshness/TTL 初始化正确；
- `shadow` 不注入；
- 长 gateway 不结束也会被 idle sweep 触发；
- `trigger=both` 不会重复入队同一 source；
- `off` 零调用。

### 4.5 门禁

- 抽取不阻塞正常对话；
- 长会话不会因缺少 session-end 永不抽取；
- 无 secret 落库；
- 无跨项目 source；
- 失败任务可观察、可重试；
- 所有自动记录均为 candidate；
- 输出条数与 token 有上限。

## 5. M3：Phase 2 Consolidation 与摘要

### 5.1 目标

把 candidate 转换成受控的 active 记忆和摘要 revision，并开始引用反馈。

### 5.2 工作项

1. 项目级 Consolidation 锁/lease；
2. 选择上限：
   - maxRecords；
   - maxUnusedDays；
   - 按最近使用、使用次数、置信度排序；
3. 确定性去重：
   - content hash；
   - 同一 manual 记录不自动覆盖；
   - 相同事实合并 sources；
4. 冲突消解：
   - conflict_key = `(scope, project_key, kind, normalized_subject)`；
   - 优先级：correction > manual+pinned > manual > verified fact > derived high-confidence > derived low-confidence candidate > stale/expired；
   - correction 优先；
   - 用户手工记录优先；
   - `pinned=1` 记录不得被自动 supersede/merge/delete；
   - 新决策可 supersede 旧决策；
   - 无法判定的同优先级冲突保留两条并标记 `open_question`，不自动合并；
   - 保留旧 revision。
5. 摘要编译：
   - 生成 `memory_summary.md`；
   - 生成 `MEMORY.md`；
   - 生成 `summaries/<session_id>.md`（如有）；
   - summary 有 revision、watermark、token 估算；
   - 写入 `memory_summary_items`，固定输入记录；
   - summary gate 校验 `status=active`、`freshness=fresh`、items 与 `record_ids_json` 的预期集合完整一致、所有 items `active+fresh`；
   - summary 生成与注入分别受 `summaryTokenBudget` 和 injection budget 约束；
   - 失败保留旧 revision；
   - 注入前校验 `input_watermark` 与 `memory_summary_items` 的 freshness；不一致时重建，失败则回退 fresh records/notes；
   - entry `delete/correct/supersede/expire/stale` 时，包含它的 summary revision 标记 `stale/superseded`，入队 `summary_rebuild`，下一次读不得注入旧 summary。
6. 注入模式切换：
   - 先 `notes`；
   - 再 `hybrid`；
   - 最后 `summary`；
   - 每项目独立开关。
7. 召回诊断：
   - zero-result 返回 filter/token 命中原因；
   - `memory_recent` / `memory_stats` / `memory_session_context` 只读诊断入口。
8. citation parser：
   - 从最终回答提取 memory id；
   - 写入 `memory_usage`（`cited` / `read` / `injected_only`）；
   - citation 解析失败仍为注入集合写 `injected_only` exposure；
   - 只有 `cited/read` 增加 `use_count`；
   - 失败不阻断；
   - 不暴露隐藏 block。
9. 淘汰：
   - candidate 超期删除；
   - superseded 保留；
   - tombstone 后台清理；
   - audit retention。
10. 删除/纠正闭环：
   - 删除写 tombstone + `memory_suppressions`；
   - 纠正写新 revision、supersede 旧记录、写 suppression；
   - 后续抽取/Consolidation 必须跳过 suppression；
   - 只有显式 restore 才允许恢复。

### 5.3 验证

```bash
cd backend
go test ./internal/memoryruntime -count=1
go test ./internal/memorystore -count=1
go test ./internal/contextmgr -count=1
go test ./internal/agent -count=1
```

新增测试：

- 去重幂等；
- correction 覆盖旧记录；
- manual 不被自动覆盖；
- summary 失败回退旧 revision；
- summary watermark 不一致不会注入旧 summary；
- stale item 会阻止 summary 注入并触发重建/回退；
- summary item 缺失/物理清理后 gate 拒绝注入；
- entry delete/correct/supersede/expire/stale 使旧 summary 失效并入队 `summary_rebuild`；
- pinned/manual 在 summary 满预算时仍可见；
- 冲突优先级与 `open_question` 行为正确；
- usage 计数只对 cited/read；citation 失败写 injected_only；
- sleep 不影响测试（使用 fake clock/固定时间）；
- token budget 不超；
- summary 注入后 prompt 前缀不变；
- active-turn replay 抑制重复注入。

### 5.4 门禁

- summary 可由 DB 重建；
- 删除记录后下一轮不再注入；
- stale summary 不注入；
- summary item 变更会使旧 revision stale/superseded；`summary_rebuild` 幂等；
- pinned/manual 在 summary 模式下可见；
- citation 失败仍写 exposure，不增加 use_count；
- `shadow` 只生成 summary，不注入；
- 项目级 summary 不包含 user scope；
- 摘要长度 p95 不超过预算；
- context 顺序不回归。

## 6. M4：工具、API 与 UI

### 6.1 工具

- `memory_search`：只读、按 scope/kind/tag 过滤；
- `memory_read`：只读、按 id；
- `memory_session_context`：只读、按 session/turn 查看因果证据；
- `memory_recent` / `memory_stats`：只读诊断；
- `memory_why` / `why-not`：只读解释注入/丢弃原因；
- `memory_add`：用户明确要求记住时写 manual；
- `memory_correct`：用户明确纠正时写 correction revision；
- `memory_forget`：默认关闭，需显式审批/用户命令。

实现位置：

- 复用现有 tool manager/registry；
- 不直接暴露 SQLite；
- 工具参数和输出都有上限；
- 工具结果用现有 data block/结构化 output 包装；
- 工具调用事件可观测。

### 6.2 API

按 `04-data-model-and-api.md` 实现 v2 稳定接口，并保留 `/harness/memory` 兼容。

- 读接口有分页上限；
- 写接口带 workspace 绑定和权限校验；
- delete/restore/reset/session mode 使用管理员/本机授权路径；
- summary activate/rollback 与 import 使用管理员授权，并记录 `memory_events`；
- `records/{id}/revalidate` 与 `freshness/sweep` 使用管理员授权，并记录事件；
- delete/reset 需要确认参数；
- export 支持 JSONL + Markdown；
- jobs 返回有限窗口；
- summary status（active/stale/superseded）、`summary_rebuild` job、pinned 预算和 exposure 可查询；
- 错误不泄漏 secret。

### 6.3 Web/TUI

- Web Settings 显示：
  - mode；
  - scope；
  - record count；
  - summary revision；
  - last consolidation；
  - job 状态/错误；
  - 来源和 usage；
  - recall trace、why/why-not、budget 饱和状态；
  - summary status/rebuild、pinned 预算占用、exposure/usage 区分；
  - 更正/遗忘/导出。
- `/memory` 命令扩展：
  - show/why/why-not/correct/forget/export/jobs/consolidate/reset/mode。
- 任何删除操作二次确认；
- 跨项目不可见。

### 6.4 验证

```bash
cd backend
go test ./internal/memorytools ./internal/api/runtimeapi ./internal/tools -count=1
cd ../frontend
pnpm test
pnpm build
```

新增前端测试：

- list/search/append 不回归；
- forget 二次确认；
- 来源和 scope 可见；
- freshness、last_verified_at、stale_reason 可见；
- summary status/rebuild、pinned 预算占用、exposure/usage 区分可见；
- 错误态/空态；
- 非当前 workspace 不显示记录。

## 7. M5：Shadow 评估与逐项目上线

### 7.1 评估

- 选择 10-20 个真实项目会话；
- `mode=shadow` 运行至少一个完整工作日/多个 idle 周期；
- 手工标注抽取结果；
- 统计：
  - 真阳性、假阳性、遗漏；
  - secret/敏感数据命中；
  - 跨项目泄漏；
  - 重复率；
  - summary token、注入延迟；
  - stable/recall 注入 token 与 cache 命中；
  - pinned 预算占用、pinned 可见性；
  - candidate 被 citation 的比例；
  - `injected_only` exposure 与 cited/read 的比例；
  - 用户删除/纠正的原因；
  - stale/unverified 比例、revalidation 成功/失败；
  - stale summary 被阻止注入的次数；
  - summary item 变更/`summary_rebuild` 次数与失败率。

### 7.2 上线策略

1. 开发仓库先 `shadow`；
2. 单项目 `on` + `notes` 注入；
3. 切换到 `hybrid`；
4. 稳定后再默认 `summary`；
5. 每次只改一个维度，保留回滚开关。

### 7.3 回滚

- `mode=off` 立即停止生成/注入；
- 回退到 `notes` 注入；
- 保留 memory.db 供审计，不自动删除；
- 回退 summary 到上一 revision；
- 迁移只前滚，不自动降级 schema。

## 8. 建议提交拆分

```text
docs(agent-memory): add design and implementation plan
feat(memory): add SQLite schema and notes migration
feat(memory): add compatibility store facade
feat(context): add memory v2 injection metadata
feat(memory): add phase1 extraction jobs
feat(memory): add redaction and extraction validation
feat(memory): add phase2 consolidation and summary
feat(memory): add pinned budget, summary invalidation and summary_rebuild
feat(memory): add usage citation parsing
feat(memory): add injected_only exposure fallback
feat(memory): add memory tools and v2 APIs
feat(web): add memory management and audit views
test(memory): add shadow/e2e/scope isolation coverage
```

每个提交必须能独立构建；`off` 行为在任一提交后都不能回归。

## 9. 实施估时（粗估）

| 阶段 | 规模 | 说明 |
| --- | --- | --- |
| M0 | 1-2 天 | 基线、开关、契约 |
| M1 | 3-5 天 | SQLite、迁移、facade、读取注入 |
| M2 | 4-7 天 | transcript 选择、抽取、脱敏、job |
| M3 | 4-7 天 | Consolidation、summary、usage |
| M4 | 3-6 天 | 工具、API、UI |
| M5 | 3-5 天 | shadow、标注、调优、上线 |

估时不包含大规模 prompt 质量调优；实际取决于现有测试稳定性和 provider 输出一致性。

> v1.1 修订的 summary gate/invalidation、pinned 预算、exposure 可能在 M1/M3 各增加约 1-2 天；以 M0 基线评估为准。

## 10. 第一版必须砍掉的范围

为保证 M1/M2 可控，v1 先不做：

- 向量检索/embedding；
- 跨项目 user scope 默认开启；
- 让内部 Agent 自动编辑记忆工作区；
- 复杂记忆图谱；
- 自动学习/微调；
- 完整 Git baseline；
- 云同步；
- 自动将记忆转成权限。

这些能力只有在 v1 的 delete/audit/scope/quality 数据证明需要时再引入。
