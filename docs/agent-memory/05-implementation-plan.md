# Agent Memory v2 实施计划

> 状态：实施计划（已确认，未开工）  
> 目标：按可验证、可回滚、`off` 行为不变的方式，把现有 memorystore 升级为跨会话记忆系统。  
> 原则：先存储和读取门面，后自动写入；先 shadow，后 on；先项目级，后 user scope。
> 修订：2026-11-10，补 §11 M1 代码级施工说明；2026-11-09，按 `13-memory-injection-and-learning-update-plan.md` v1.1 回写 stable/recall split、pinned 预算、summary invalidation/summary_rebuild、trigger both、exposure fallback。

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
## 11. M1 施工说明（代码级）

> 状态：施工说明，随 M1 实施维护。
> 目的：把 §3 的 M1 阶段展开为可执行的代码切片、接口契约、迁移顺序、测试命令和提交边界。
> 范围：只覆盖 M1；不实现 M2 自动抽取、M3 summary 编译器、M4 全量工具/UI、M5 上线评估。
> 事实源：schema/API 以 `04-data-model-and-api.md` 为准，验收以 `06-test-and-acceptance.md §8.2` 为准。本节新增的 schema/API 细节必须先回写 04，再进入编码；不得让本文件成为第二套数据契约。

### 11.1 当前代码基线与开工前置

当前代码只具备 M1 之前的基础：

| 能力 | 当前落点 | 状态 |
| --- | --- | --- |
| durable notes | `backend/internal/memorystore/store.go` | `notes.jsonl` MVP |
| 注入 | `backend/internal/contextmgr/manager.go` | 单个 `project_memory` block |
| `/memory` | `backend/cmd/aicli/commands/chat_memory_command.go` | status/add/list/search |
| harness API | `backend/internal/api/runtimeapi/harness_handlers.go` | GET/POST，JSONL |
| 配置 | `backend/internal/config/manager.go` | 只有 `Agent.EnableMemory`，无 v2 `memory` 段 |
| SQLite 基线 | `backend/internal/sqliteutil`、`backend/internal/migrate` | 可复用 |
| 测试 | `backend/internal/memorystore`、`contextmgr` | 现有测试通过，无 v2 schema/mode 测试 |

M1 开始编码前必须先关闭以下前置项；未关闭时只能做 M0，不应合并 M1 业务代码：

1. `memory.mode`、`manual.enabled`、`injection.*` 配置和默认 `off` 已定义并校验；
2. `off` 下不创建 v2 DB、不启动 v2 注入的门禁有测试；
3. `Store` 的 legacy JSONL / SQLite 生命周期和迁移触发条件已选定；
4. stable/recall 的 selection 返回类型已定义；
5. 04 已补 `memory_session_modes`、active summary 唯一约束、summary watermark 算法；
6. `project_memory_recall` 的 stage 生命周期和 compact 行为已明确；
7. M1 API 的 DTO、分页、workspace 绑定和管理授权已定义。

### 11.2 默认决策（M1 实施口径）

以下决策用于消除当前文档和代码之间的歧义；若 04 尚未同步，应先回写 04，再按此实现。

| 决策 | M1 口径 |
| --- | --- |
| `Agent.EnableMemory` | 继续只表示 `internal/memory` 的 observation memory；v2 不使用该字段 |
| `memory.mode` | `off` / `shadow` / `on`，默认 `off` |
| `off` 的 DB 行为 | 不创建 DB；没有 DB 时手工 notes 走 JSONL；DB 已存在时允许手工路径只读写 manual records |
| `off` 的注入口径 | 不注入 v2 summary/records；既有 manual notes 注入按 legacy 路径保留 |
| `shadow` 的注入口径 | 不注入 v2 stable/recall；既有 manual notes 注入仍可保留 |
| `on` 的注入口径 | `injection.enabled=true` 时走 v2 stable/recall；否则保留 legacy manual 注入 |
| M1 自动写入 | 不做；`on` 只读取和人工/API 写入，不自动抽取 |
| legacy `/memory add` | 写 manual record；`pinned` 默认 `true`（用户显式记忆），可通过 API 显式改为 `false` |
| 迁移策略 | `mode=on/shadow` 首次打开时幂等迁移 JSONL；JSONL 保留，不自动删除 |
| 回退 | SQLite 打开/迁移失败时降级为 JSONL 只读，并写诊断事件；不阻断 Agent |
| user scope | M1 不启用；`allowUserScope` 保持默认 `false` |

> `pinned` 默认值必须与 03/09 的“用户显式记住 → manual active + pinned”一致；如果产品最终选择“manual 默认不 pinned”，应同时修订 03/07 和 M1 API 默认值，不能只改代码。

### 11.3 `memory` 配置和 `off` 门禁

在 `RuntimeConfig` 增加顶层字段，命名与 04 §6 的 YAML 段一致：

```go
type RuntimeConfig struct {
    // ... existing fields ...
    Memory MemoryConfig `yaml:"memory" json:"memory"`
}

type MemoryConfig struct {
    Mode       string              `yaml:"mode" json:"mode"`       // off | shadow | on
    Scope      string              `yaml:"scope" json:"scope"`     // project
    StorePath  string              `yaml:"storePath" json:"storePath"`
    Manual     MemoryManualConfig  `yaml:"manual" json:"manual"`
    Injection  MemoryInjectionConfig `yaml:"injection" json:"injection"`
}
```

M1 只要求落地以下子项；其余 04 字段可以保留在结构体中但不得被 M1 代码消费：

```yaml
memory:
  mode: off
  scope: project
  storePath: ""
  manual:
    enabled: true
  injection:
    enabled: true
    mode: notes
    maxItems: 5
    tokenBudget: 600
    pinnedBudgetRatio: 0.20
    stageSplit: true
```

实现要求：

1. `DefaultRuntimeConfig()` 显式设置 `Memory.Mode=off`、`Manual.Enabled=true`、`Injection.Mode=notes`、`PinnedBudgetRatio=0.20`、`StageSplit=true`。
2. `ValidateRuntimeConfig` 校验 `mode`、`scope`、`injection.mode`、`pinnedBudgetRatio` 范围（0–1）；非法值启动失败，而不是静默降级。
3. `backend/configs/runtime.yaml` 只增加注释/示例，不默认启用 v2；示例默认保持 `mode: off`。
4. `context_max_project_memory` / `context_project_memory_tokens` 继续作为预算 override。
5. `off` 门禁测试至少覆盖：
   - `New`/打开 status 不创建 `memory.db`；
   - 无手工访问时无 v2 表、无 job、无 event；
   - legacy `/memory add/list/search` 与当前 JSONL 行为一致；
   - `on` 打开后才允许创建 v2 DB。

### 11.4 Store 生命周期与 selector 契约

保持 `memorystore.Store` 作为兼容 facade，不把旧调用方直接改成 SQLite 类型。建议新增以下类型和可选接口：

```go
type BlockKind string

const (
    BlockStable BlockKind = "stable"
    BlockRecall BlockKind = "recall"
)

type Record struct {
    ID            string
    Kind          string
    Scope         string
    Status        string
    Text          string
    Tags          []string
    Pinned        bool
    Confidence    float64
    SourceRefs    []string
    Revision      int64
    Freshness     string
    LastUsedAt    time.Time
}

type Summary struct {
    ID            string
    Revision      int64
    Text          string
    TokenEstimate int
    Freshness     string
}

type Selection struct {
    Summary       *Summary
    Stable        []Record
    Recall        []Record
    Revision      int64
    TokenEstimate int
    IDs           []string
    Dropped       []DroppedRecord
}

type V2Selector interface {
    SelectForInjectV2(opts InjectOptions) (Selection, error)
    V2InjectionEnabled() bool
}
```

`InjectOptions` 做向后兼容扩展，新增字段不能破坏现有调用：

```go
type InjectOptions struct {
    Query       string
    Limit       int
    TokenBudget int

    SessionID   string
    TurnID      string
    ProjectKey  string
    Mode        string
    InjectionMode string
    Now         time.Time
}
```

实现要求：

1. `SelectForInject` 保持现有签名；M1 中只返回 legacy/manual active records，不返回自动生成的 summary/records。
2. 新增 `SelectForInjectV2`；`contextmgr` 通过类型断言使用它，旧 fake/旧调用方继续使用旧接口。
3. `V2InjectionEnabled()` 只在 `mode=on && injection.enabled=true`（且 `stageSplit` 允许）时返回 true；`off/shadow` 或 injection 关闭时走 legacy 路径。
4. `Store` 增加 `Close()`、`DBPath()`、`SchemaVersion()`、`Status()`；现有 `Path()` 继续返回 `notes.jsonl` 以保持兼容。
5. `Store` 采用 lazy open：构造时不打开 SQLite；首次需要 v2 能力时打开/迁移。`off` 且 DB 不存在时不得创建 DB。
6. `Store` 不允许同时把 JSONL 和 SQLite 当作可写事实源；`on` 以 SQLite 为准，`off` 的 manual 路径按 11.2 的决策执行。
7. 任何 DB 打开/迁移错误都返回可诊断错误，并允许 legacy 只读回退；不得静默吞掉。

### 11.5 必须在 M1 前回写 04 的 schema 增补

#### 11.5.1 `memory_session_modes`

M1 的 `POST /sessions/{id}/mode` 和 M2 的 session 选择都需要持久化 session mode；04 当前没有该表。建议增加：

```sql
CREATE TABLE memory_session_modes (
    session_id      TEXT PRIMARY KEY,
    scope           TEXT NOT NULL DEFAULT 'project',
    project_key     TEXT NOT NULL DEFAULT '',
    mode            TEXT NOT NULL, -- enabled | disabled | polluted
    updated_at      TEXT NOT NULL,
    metadata_json   TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_memory_session_modes_project
  ON memory_session_modes(project_key, mode, updated_at DESC);
```

#### 11.5.2 active summary 唯一约束

`memory_summaries` 目前只有 revision 唯一约束，不能防止两个 active revision 同时存在。增加：

```sql
CREATE UNIQUE INDEX idx_memory_summaries_active
  ON memory_summaries(scope, project_key, summary_kind)
  WHERE status = 'active';
```

activate/rollback 必须在同一个 `WriteTxOptions` 事务内完成“旧 active → superseded、新 revision → active”，否则并发请求可能产生两个 active。

#### 11.5.3 summary watermark

M1 必须冻结一个确定性算法，供 M3 summary 编译和 M1 gate 共用。建议：

```text
summary_input_watermark =
  "v1:" + sha256(canonical_json([
    {id, revision, content_hash, status, freshness_status}
    for each memory_summary_item
    ordered by id
  ]))
```

规则：

- `memory_summary_items` 是权威输入集合；`record_ids_json` 只是同集合的快照校验；
- gate 重新读取 items 关联的 entries，计算当前 watermark；
- item 缺失、revision/content_hash/status/freshness 变化都会导致 watermark 不一致；
- `summary_rebuild` 的 `job_key` 使用 `summary_id + ":" + current_watermark`；
- M1 不实现 summary 编译器，但必须实现 watermark 计算函数、gate 判定和 seeded DB 测试。

#### 11.5.4 schema 版本账本

`internal/migrate` 当前写入 `schema_migrations`，而 04 要求 `memory_schema_version`。建议 M1 采用：

- 唯一写入账本：`schema_migrations`；
- `memory_schema_version` 作为只读兼容视图：

```sql
CREATE VIEW IF NOT EXISTS memory_schema_version AS
SELECT version, applied_at FROM schema_migrations;
```

如果 04 坚持物理表，必须在 M1 migration 内同步维护两处，不能只建表不更新。

#### 11.5.5 迁移状态

为保证迁移可观测，建议增加 `memory_migration_runs`（或在 04 中明确等价位置）：

```sql
CREATE TABLE memory_migration_runs (
    id            TEXT PRIMARY KEY,
    source_path   TEXT NOT NULL,
    source_size   INTEGER NOT NULL DEFAULT 0,
    source_mtime  TEXT,
    scanned       INTEGER NOT NULL DEFAULT 0,
    imported      INTEGER NOT NULL DEFAULT 0,
    skipped       INTEGER NOT NULL DEFAULT 0,
    corrupt       INTEGER NOT NULL DEFAULT 0,
    status        TEXT NOT NULL, -- running | succeeded | failed
    error         TEXT,
    started_at    TEXT NOT NULL,
    finished_at   TEXT
);
```

迁移的 idempotency 仍以 `memory_entries` 的 content hash 唯一索引为准；`memory_migration_runs` 只用于诊断和重试，不作为唯一判重依据。

### 11.6 M1-A：schema、SQLite store 与 notes 迁移

建议新增/调整文件：

```text
backend/internal/memorystore/
  types.go              # Scope/Kind/Status/Record/Selection/DroppedRecord
  config.go             # Config、默认值、mode/manual/injection 解析
  projectkey.go         # project_key canonicalization
  schema.go             # migration SQL、schema version
  sqlite_store.go       # SQLite CRUD/query/tx
  retrieval.go          # filter/rank/budget/format
  notes_migration.go    # notes.jsonl -> memory_entries
  compat.go             # 旧公开方法兼容与 legacy fallback
  store.go              # facade: New/Append/List/Search/SelectForInject
```

实施顺序：

1. `types.go`：先定 scope/kind/status/freshness/retention/content hash；`project_key` 规范化函数优先写测试。
2. `schema.go`：按 04 + 11.5 增补建表；迁移一次事务完成；重复执行不重复插入。
3. `sqlite_store.go`：仅暴露最小 CRUD/query；所有写事务使用 `sqliteutil.WriteTxOptions`，不把 LLM 调用放进事务。
4. `notes_migration.go`：逐行解析、损坏行计数、content hash 去重、保留原文件、失败不标记完成。
5. `compat.go`：旧 `Append/List/Search/SelectForInject` 包装到 v2 或 legacy；`off` 不创建 DB。
6. `store.go`：lazy open、`Close`、DB path/status、fallback。
7. 旧调用方接入：`/memory`、harness memory、agent attach 时传入 mode/config；不要直接改 API DTO。

M1-A 必须新增/通过的测试：

- `TestMemoryStore_CreateSchema`
- `TestMemoryStore_MigrateNotesIdempotent`
- `TestMemoryStore_CorruptJSONL`
- `TestMemoryStore_OpenFailureDegrades`
- `TestMemoryStore_OffDoesNotCreateDB`
- `TestMemoryStore_OffKeepsManualMemory`
- `TestMemoryStore_ProjectKeyCanonicalization`
- `TestMemoryStore_ScopeIsolation`
- `TestMemoryStore_TombstoneNotInjected`
- `TestMemoryStore_RevisionEventWritten`
- `TestMemoryStore_SummaryItemsMatchRevision`
- `TestMemoryStore_SummaryGateRejectsMissingOrStaleItems`
- `TestMemoryStore_SummaryInvalidatedOnItemMutation`
- `TestMemoryStore_PinnedBudgetSurvivesSummaryFull`
- `TestMemoryStore_WALConcurrentReaders`

### 11.7 M1-B：stable/recall 注入

建议新增 `backend/internal/contextmgr/project_memory.go`，把选择、渲染和 metadata 从 `manager.go` 拆出。

接口兼容策略：

```go
type ProjectMemoryStore interface {
    SelectForInject(opts memorystore.InjectOptions) ([]memorystore.Note, error)
}

type ProjectMemoryV2Store interface {
    SelectForInjectV2(opts memorystore.InjectOptions) (memorystore.Selection, error)
    V2InjectionEnabled() bool
}
```

`Manager.Build` 的选择顺序：

1. `ProjectMemory` 实现 `ProjectMemoryV2Store` 且 `V2InjectionEnabled()` 为 true → 使用 V2 selection；
2. 否则使用现有 `SelectForInject` legacy 路径；
3. 任何 V2 选择错误都只记录 `project_memory_error`，不阻断构建；必要时回退 legacy。

注入位置和 metadata：

| 块 | stage | 位置 | metadata |
| --- | --- | --- | --- |
| stable | `project_memory` | 现有 `project_memory` 动态尾部位置 | `memory_version=2`、`memory_block_kind=stable`、`memory_revision`、`memory_ids`、`project_memory_count` |
| recall | `project_memory_recall` | 所有稳定 stage 之后、active goal 之后 | `memory_version=2`、`memory_block_kind=recall`、`memory_revision`、`memory_ids` |
| legacy | `project_memory` | 现有位置 | 保持现有 `project_memory_injected/count/ids`，不写 v2 字段 |

每个 turn 的 group guard：

```text
memoryGroupPresent := stagePresent("project_memory") ||
                     stagePresent("project_memory_recall") ||
                     activeTurnHasReplay

if memoryGroupPresent:
    suppress stable and recall
else:
    plan once
    append stable at project_memory position if non-empty
    append recall after active_goal if non-empty
```

关键实现点：

1. stable/recall 必须来自同一次 selection/plan；不能在后续 Build 中重新 query 后追加第二组。
2. `project_memory_recall` 标记为 turn-local/transient，不进入 durable compact 保留清单；compact 后应由下一次 build 重新生成。
3. `project_memory` 的 durable 语义保持现状；但 V2 stable 必须带 revision metadata，便于诊断旧 block。
4. 更新 `contextmgr/compact.go`、`compactruntime/local.go` 的 stage 列表和 `manager.go` 的 trim 分类；至少保证 `project_memory_recall` 会被视为 dynamic tail，不会被误归入 stable history。
5. legacy 路径保持现有 header、预算、metadata 和 tests；`off` 的 prompt 顺序、hash、消息数必须与基线一致。
6. V2 渲染必须明确 historical-data 提示，不能使用指令性模板；memory 内容不得进入 system prompt 或 tool schema。

M1-B 必须新增/通过的测试：

- `TestBuildProjectMemoryKeepsDynamicTail`
- `TestBuildDoesNotPrependProjectMemory`
- `TestBuildProjectMemorySuppressedOnActiveReplay`
- `TestBuildProjectMemoryStableAndRecallSplit`
- `TestBuildProjectMemoryGroupInjectedOncePerTurn`
- `TestBuildProjectMemoryPinnedVisibleInSummaryMode`
- `TestBuildProjectMemorySummaryGateRejectsMissingItems`
- `TestBuildProjectMemorySummaryInvalidatedOnItemMutation`
- `TestBuildMemoryOffByteIdentical`
- `TestBuildProjectMemoryCompactResumeNoDuplicate`
- `go test ./internal/compactruntime -count=1`

### 11.8 M1-C：状态与管理 API

建议新增 `backend/internal/api/runtimeapi/memory_handlers.go`，避免继续膨胀 `harness_handlers.go`；在 `handler.go` 注册：

```text
GET    /api/runtime/memory/status
GET    /api/runtime/memory/records
POST   /api/runtime/memory/records
GET    /api/runtime/memory/records/{id}
PATCH  /api/runtime/memory/records/{id}
DELETE /api/runtime/memory/records/{id}
POST   /api/runtime/memory/sessions/{id}/mode
GET    /api/runtime/memory/sessions/{id}/context
GET    /api/runtime/memory/recall-traces
```

最小响应契约：

| 接口 | M1 要求 |
| --- | --- |
| `status` | mode、scope、db_path、schema_version、record counts by status、freshness counts、summary revision/status、job counts、fallback 状态 |
| `records` list | `status` 默认 `active`，`limit` 默认 50、最大 200，支持 scope/kind/tag/q/cursor；返回 `items/count/next_cursor` |
| `records` create | `{text,kind,tags,pinned,source,session_id}`；manual 默认 active；返回 record DTO |
| `records/{id}` | 详情包含 sources、revision、freshness、usage；删除/纠正不返回 secret 原文 |
| `sessions/{id}/mode` | 只接受 `enabled/disabled/polluted`，持久化到 `memory_session_modes`，写 audit |
| `sessions/{id}/context` | 只读有界摘要，不返回完整 transcript 或跨 project source |
| `recall-traces` | 有界分页；默认配置关闭时返回空；不返回完整 query/正文 |

安全与兼容：

1. 新增 `authorizeMemoryAdmin`：loopback、有效 admin token 或 admin role 三者之一；DELETE、session mode、revalidate/reset 必须走该路径。
2. `workspace_path` 只用于选择当前 runtime workspace；服务端 canonicalize 后与当前 workspace 不一致则拒绝。不得复用 `resolveHarnessWorkspace` 的 CWD fallback 作为授权依据。
3. 旧 `/api/runtime/harness/memory` GET/POST 保持响应字段和 append/search 行为；`Path` 继续返回 `notes.jsonl`，另加 `db_path` 时保持向后兼容。
4. 分页/参数上限、错误码、空结果、DB unavailable、FTS fallback 都要有测试。
5. M1 不注册 memory tools，不实现 citation parser；相关字段可预留但不得写 usage。

M1-C 必须新增/通过的测试：

- `TestHarnessMemoryListSearchAppend`（保持现有）
- `TestMemoryAPIStatusOffDoesNotCreateDB`
- `TestMemoryAPIRecordsPaginationAndFilters`
- `TestMemoryAPIRecordsWorkspaceBinding`
- `TestMemoryAPIDestructiveRequiresAdmin`
- `TestMemoryAPISessionModePersists`
- `TestMemoryAPIRecallTracesBounded`
- `TestMemoryAPIRecordsDeleteTombstone`
- `TestMemoryAPIRecordsPatchRevisionEvent`

### 11.9 验证命令

每个 M1 切片至少运行：

```bash
cd backend
go test ./internal/memorystore -count=1
go test ./internal/contextmgr -count=1
go test ./internal/compactruntime -count=1
go test ./internal/config -count=1
go test ./internal/agent -count=1
go test ./internal/api/runtimeapi -count=1
go vet ./internal/memorystore ./internal/contextmgr ./internal/compactruntime ./internal/config ./internal/api/runtimeapi
```

M1 合并前再运行：

```bash
cd backend
go test ./... -count=1
```

涉及 Windows 路径/锁行为时，额外记录：

- 中文路径和空格路径；
- 大小写不同的同一 workspace；
- 两个进程同时打开 `memory.db`；
- 关闭后重新打开 schema/version；
- `off` 下 legacy notes 与基线 diff。

### 11.10 提交拆分

建议按以下顺序提交，每个提交独立可构建：

```text
docs(agent-memory): add M1 code-level implementation guide
feat(memory): add v2 config and off gate
feat(memory): add sqlite schema and notes migration
feat(memory): add compatible store facade
feat(context): add stable and recall memory injection
feat(memory): add v2 status and record APIs
test(memory): add M1 migration, scope, summary gate and exposure coverage
```

注意：

- `feat(memory): add sqlite schema` 不得顺带启用自动抽取或工具；
- `feat(context)` 必须保持 `off` 的 prompt baseline；
- `feat(memory): add APIs` 不得改变 `/harness/memory` 旧响应；
- 测试提交不能只包含 happy path。

### 11.11 M1 合并门禁与停止条件

满足以下条件才能合并 M1：

- [ ] `off` 下 v2 DB 创建、v2 注入、v2 自动任务均为 0；
- [ ] legacy `/memory`、harness memory GET/POST 不回归；
- [ ] `notes.jsonl` 迁移幂等，损坏行有计数，原文件保留；
- [ ] project scope 隔离、tombstone、suppression、revision/event 测试通过；
- [ ] summary gate 拒绝缺失/stale item，item 变更后旧 summary 不新增注入；
- [ ] stable `project_memory` 与 tail `project_memory_recall` 分离，每 turn 最多一组；
- [ ] pinned/manual 独立预算，下一轮可见；
- [ ] memory budget 被总 prompt 预算 clamp；
- [ ] compact/resume 后不重复追加 memory；
- [ ] 非 memory stage 的 prompt 前缀变化为 0；
- [ ] SQLite busy/lock 测试无死锁；
- [ ] `go test ./...` 与 `go vet` 通过。

出现以下任一情况必须停止合并：

- 需要修改 system prompt 或 tool schema 才能让 M1 工作；
- 需要把 JSONL 和 SQLite 同时作为可写事实源却没有明确同步规则；
- summary gate 测试被跳过或用 seeded 假数据掩盖真实缺失；
- `off` 基线 diff 无法解释；
- 跨 workspace 读取/写入没有 fail closed；
- M1 顺手引入 M2 自动抽取、M3 summary 编译器或 M4 工具。

### 11.12 施工前回写 04 的清单

进入 M1-A 编码前，先把以下内容并入 04，避免实现与事实源分叉：

1. `memory_session_modes` 表和权限/审计语义；
2. `memory_summaries` active 唯一索引与 activate/rollback 事务语义；
3. `input_watermark` 的 canonical_json 算法和 `summary_rebuild` job_key；
4. `memory_schema_version` 的视图/双写选择；
5. `memory_migration_runs` 或等价迁移诊断契约；
6. legacy Note 的 kind/pinned/retention/source 映射；
7. `memory_recall_traces` 的写入时机和采样开关；
8. M1 API 的 DTO、分页、错误码和 workspace 绑定；
9. `project_memory_recall` 的 transient/durable 生命周期；
10. `off` 已存在 DB 时 manual 路径的读写语义。

完成回写并通过 `git diff --check` 后，再进入 M1-A。
