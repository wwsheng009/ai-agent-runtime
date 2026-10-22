# 记忆层级、召回、Token 预算与可观测性

> 状态：设计补充（Memory Horizon / Recall / Budget Contract）  
> 目的：明确短期记忆、长期记忆、workspace/project 记录、user/global 记录、召回逻辑、token 控制和观测口径。  
> 本文是 `03-target-architecture.md` 与 `04-data-model-and-api.md` 的补充契约。

## 1. 结论

这六件事必须分开设计，不能用一个 `memory` 配置概念混在一起：

| 问题 | 对应机制 |
| --- | --- |
| 短期记忆 | turn working set + session observations + compact projection |
| 长期记忆 | project SQLite memory + optional user SQLite memory |
| 工作区记录 | project scope，按 `project_key` 隔离 |
| 全局记录 | 仅指显式开启的 user scope；不是默认的系统级共享记忆 |
| 观测性 | 事件/指标 + recall trace + usage/citation |
| 召回与 token | 查询构造 → 过滤 → 排序 → 去重 → 预算装配 → 注入 |

**当前项目已有短期记忆和 workspace manual notes；长期自动记忆、user scope、召回 trace 和总体预算控制仍属于实施计划。**

## 2. 记忆层级（Memory Horizons）

### 2.1 层级定义

| 层级 | 名称 | 生命周期 | 存储 | 典型内容 |
| --- | --- | --- | --- | --- |
| L0 | Turn working set | 单次 turn | 内存 | 当前用户消息、当前工具调用、pending approval/question、当前 step 上下文 |
| L1 | Session short-term | 单会话 | `internal/memory` + canonical transcript + compact projection | 最近 observation、当前 goal/todos、compact summary、fact ledger |
| L2 | Workspace/project durable | 跨会话、当前工作区 | `<workspace>/.aicli/memory/memory.db` | 项目决策、工作流、坑、项目范围事实、manual notes |
| L3 | User/global durable | 跨工作区、可选 | `~/.aicli/memory/memory.db` | 跨项目用户偏好、通用工作方式、通用 tips |
| L4 | Profile static | 随 profile | profile memory 资源 | 角色、静态指令、profile context |

### 2.2 关键边界

- L0 不持久化，不进入 memory.db；
- L1 的 transcript 可以持久化，但 `internal/memory` observations 不是长期记忆；
- compact summary 仍属于 L1，不得自动写入 L2；
- L2 是默认长期记忆，必须带 `project_key`；
- L3 只保存“跨项目仍然成立”的用户偏好，不保存项目路径、密钥、内部决策；
- L4 只读引用，不自动提升为 L2/L3；
- `.aicli/memory` 是本地数据目录，默认被 `.gitignore` 忽略；
- workspace registry、session index、file index 属于运行时记录，不是 memory。

### 2.2.1 Working digest（v1.1）

借鉴 Nuphus 的工作记忆日志，可在 L1 增加独立的近期工作摘要：

- 来源：最近 turn 的决策、关键文件、阻塞、下一步；
- 上限：2,000 字符；
- 存储：materialized working digest，不直接进入 L2；
- 注入：仅新 turn 注入一次，位于 raw history 之后的动态尾部；
- 与 long-term summary 分离，不能替代 L2；
- 默认 v1.1，不进入 v1 必做范围。

### 2.3 工作区记录 vs 全局记录

术语要先拆开：

| 术语 | 含义 | 是否属于长期记忆 |
| --- | --- | --- |
| workspace record | workspace registry、cwd、session 绑定等运行记录 | 否 |
| workspace memory | 当前工作的项目级记忆 | 是，L2 |
| global/user memory | 同一用户跨项目的偏好 | 可选，L3 |
| system/global memory | 所有用户/租户共享 | **不提供** |

“全局记录”在 AAR 中只允许解释为 **user-level cross-project memory**。不存在默认的跨用户、跨租户全局记忆池。

## 3. Scope 与优先级

### 3.1 默认 scope

```text
默认读取：project
默认不读取：user/profile
默认不写：user
```

- `project`：当前 workspace 的 L2；
- `user`：显式开启的 L3；
- `profile`：只读 L4；
- `session`：L1 运行时状态，不作为长期检索结果。

### 3.2 全局/user 记录准入

只有以下类型允许进入 L3：

- 用户明确表达的通用偏好；
- 在多个项目中重复出现的工作方式；
- 不依赖具体项目路径的通用流程；
- 用户明确说“以后都这样”。

禁止进入 L3：

- 项目专属路径、分支、内部决策；
- secret、token、账号；
- 单次任务的选择；
- 未经验证的推测；
- 用户未确认的自动提升。

### 3.3 结果冲突时的解释顺序

检索结果不是简单按 scope 拼接，需要明确优先级：

1. 当前 turn 的显式 user/system 指令；
2. 当前项目明确 decision/correction；
3. 当前项目 pinned/manual 记录；
4. 项目 fact/reference；
5. user/global preference（不得覆盖项目事实，但可补充风格）；
6. profile static context（最低）。

出现冲突时：

- 保留更高优先级记录；
- 将冲突写入 `memory_events` 或 recall trace 的 `dropped_reason=conflict`；
- 不自动把冲突内容合并成一条“平均结论”。

## 4. 生命周期：短期如何变成长期

### 4.1 晋升路径

```text
L0 turn working set
  └─(会话结束/空闲) 不自动晋升

L1 session transcript / observations
  └─ Phase 1 抽取 → candidate（L2）
        ├─ 低置信/一次性 → 丢弃或短期保留
        ├─ 重复证据/稳定决策 → Consolidation → active（L2）
        └─ 用户显式“记住” → manual active + pinned（L2）

L2 project active
  └─ 用户显式要求 / 多项目重复 / 非项目专属
        → candidate（L3） → 用户确认 → active（L3）
```

### 4.2 晋升门槛

| 路径 | 必要条件 |
| --- | --- |
| L1 → L2 candidate | 有 source；非一次性；secret 过滤通过 |
| candidate → L2 active | Consolidation 校验；confidence 门槛；无 pinned/suppression 冲突 |
| L2 → L3 candidate | 用户明确要求，或跨项目重复证据；非项目专属 |
| L3 candidate → active | 用户确认或管理员审核；默认不自动 active |

### 4.3 降级与淘汰

| 状态 | 条件 |
| --- | --- |
| candidate → expired | 超过 `candidateDays` 未被 Consolidation 采纳 |
| active → superseded | 新 correction/decision 替代旧记录 |
| active → expired | 超过 `maxUnusedDays` 未使用且非 pinned/manual |
| fresh → stale | source hash/revision 变化、TTL 到期、源缺失或出现矛盾 |
| stale → fresh | 回源校验确认源未变化，更新 `last_verified_at` |
| any → deleted | 用户显式遗忘或管理操作 |
| L3 → deleted | 用户撤销全局偏好；项目 fact 不应被提升 |

`pinned`、用户 correction、审计事件和 suppression 不因普通 retention 被静默删除。

## 5. 召回逻辑

### 5.1 召回漏斗

```text
[1] Query 构造
  → [2] 硬过滤
  → [3] 候选生成
  → [4] 排序/重排
  → [5] 去重与多样性
  → [6] Token 预算装配
  → [7] 注入
  → [8] 使用反馈
```

### 5.2 Query 构造

输入：

- 当前 goal / 最近一条用户请求；
- 最近 N 条用户消息（建议 2-4 条）；
- 当前 workspace/project key；
- 可选显式引用（文件、模块、任务 id）。

规则：

- 不把完整工具输出作为 query；
- 过滤停用词；
- 对 query 记录 hash 和脱敏 preview，不默认保存全文；
- query 为空时只使用 summary + pinned/recent，不进行全库扫描；
- `user` scope 开启时，query 仍先按 project 过滤，再补充通用偏好。

### 5.3 硬过滤

按以下顺序短路：

1. `mode` 是否允许注入；
2. scope 是否允许；
3. `project_key` 是否匹配；
4. `status` 是否为 active（或显式允许 candidate 的 shadow 视图）；
5. 是否删除/过期/suppressed；
6. `freshness_status` 是否允许（默认仅 `fresh`）；
7. 敏感级别；
8. source 是否可访问；
9. 是否属于当前 profile/agent 允许的层。

#### 5.3.1 Freshness gate

- `fresh`：可进入普通候选；
- `unverified`：仅 manual、审核通过或显式允许时进入；
- `stale`：默认排除并记录 `dropped_reason=stale`；`warn` 模式仅允许低风险 kind 注入，且必须标记 `possibly stale`；
- `expired/deleted/suppressed`：永不进入普通候选；
- `valid_until < now` 直接按 expired 处理；
- summary 还必须通过 `input_watermark + memory_summary_items freshness` 门禁；
- 高风险事实（路径、API、版本、配置）只作为 pointer，回答前回源读取。

### 5.4 候选生成

至少四类候选，合并后去重：

1. **Pinned/manual**：用户明确维护，数量少；
2. **Summary**：materialized summary，不是候选记录；
3. **FTS/keyword**：按 query 匹配 text/tags；
4. **Recent/high-usage**：最近高使用记录，作为空查询或弱查询兜底。

不使用 embedding；FTS 不可用时使用 LIKE + token overlap。

### 5.5 排序

建议：

```text
score =
  0.40 × text_match
+ 0.20 × recency
+ 0.15 × usage(decayed)
+ 0.10 × confidence
+ 0.05 × scope_priority
+ 0.10 × pinned_or_correction_boost
```

约束：

- `pinned` 有上限 boost，不能淹没当前任务匹配；
- correction 优先于同主题旧记录；
- 低置信 candidate 在 `on` 模式下不注入；
- usage 使用时间衰减，避免热门记录垄断。

### 5.6 去重与多样性

- 相同 `content_hash` 只保留一条；
- 同主题/同标签过多时，限制每个 tag/kind 的条数；
- 不把同一 summary 内容和记录正文重复注入；
- 多 scope 冲突按 §3.3 保留高优先级；
- 丢弃原因记入 recall trace。

### 5.7 空结果和降级

- 无匹配：只注入 summary；summary 为空则零注入；
- FTS 失败：LIKE/token overlap；
- DB 不可用：不注入，不阻断对话；
- token 不足：按排序丢弃；
- summary 过期/输入 watermark 不一致：重建；失败则回退到 fresh records 或 `notes`；
- user scope 未授权：忽略且不报错（但记录 denied 指标）。

零结果诊断至少返回：

- 被过滤的层级/原因（scope/status/freshness/suppression/budget）；
- 查询分词后的逐词命中数（若可用）；
- 建议动作（缩小关键词、切换 kind、使用 semantic/扩展 scope）。

可解释读取工具：

- `memory_search`：检索；
- `memory_recent`：最近记忆；
- `memory_stats`：数量/kind/usage/embedding 覆盖；
- `memory_session_context`：按 session/turn 查看因果证据。

### 5.8 召回不调用 LLM

读取路径不允许同步调用 LLM。查询构造、过滤、排序和预算装配均为本地确定性步骤；LLM 只用于离线抽取和 Consolidation。

## 6. Token 控制

必须同时控制三类预算：

### 6.1 抽取输入预算

- 每个 session 的 transcript 选取有上限；
- 默认只取与 memory-relevant 的近期/关键片段；
- 使用模型 context window 的保守比例（建议 60-70%）；
- 超长工具输出先替换为摘要 + `artifact_ref`；
- 不把完整 transcript 送入抽取模型；
- 全局 job 有 session 数、record 数、token 上限。

### 6.2 Summary 生成预算

- `summaryTokenBudget` 默认 800，硬上限建议 1200；
- 输入记录数有上限（默认 256，实际按 token 再截）；
- 生成后校验 token；
- 超预算先删低 rank 内容，再压缩正文；
- 失败保留旧 revision。

### 6.3 注入预算

现有 `contextmgr` 预算：

| profile | max items | memory tokens |
| --- | --- | --- |
| compact | 3 | 400 |
| balanced | 5 | 600 |
| extended | 8 | 900 |

v2 仍沿用并按配置覆盖，但必须有总 prompt 约束：

```text
memory_budget =
  min(
    config.memory.injection.tokenBudget,
    max(0, max_prompt_tokens - mandatory_prompt_tokens)
  )

mandatory_prompt_tokens =
  system + safety + tool_schema + stable profile +
  active turn + required history + response headroom
```

规则：

- `memory_budget` 不是额外增加总 prompt，而是总预算内的子预算；
- mandatory layers 先满足；剩余不足时 memory 降至 0；
- `summary` 模式：summary 独占 memory_budget；
- `hybrid` 模式：summary <= 60%，records <= 40%；
- `notes` 模式：records 独占；
- 超预算时按排序从低到高丢弃；单条超预算时截断正文，但保留 id/kind/tags/source 摘要；
- 每个逻辑 turn 只注入一次，保持 prompt cache 稳定；
- 估算使用 `internal/tokenestimate`，并保留 10-15% 安全余量；
- provider 返回 usage 时用于事后校准，不改变本轮已经形成的稳定前缀。

### 6.4 存储与增长预算

- active records 默认最多 4096；
- 每会话自动 candidate 默认最多 20；
- candidate 保留 30 天；
- deleted tombstone 保留 30 天；
- audit 保留 180 天；
- summary 每个 scope 只保留有限历史 revision；
- cleanup job 按 retention 和 scope 执行。

按 kind 的 retention class：

| class | 默认 | 策略 |
| --- | --- | --- |
| permanent | manual、pattern、核心 decision | 不自动过期 |
| standard | preference/fact/workflow/pitfall/reference | 按 maxUnusedDays |
| ephemeral | task trace、低置信 candidate | 更短周期 |

`pinned` 与 retention_class 独立；pinned 永不自动删除。

## 7. 可观测性

### 7.1 三层观测

| 层 | 内容 |
| --- | --- |
| Metrics | 计数、token、延迟、失败率、命中率 |
| Events | 抽取、Consolidation、注入、引用、删除、迁移 |
| Recall trace | 单次召回的候选、过滤、排序、预算和丢弃原因 |

### 7.2 Recall trace

建议新增 `memory_recall_traces`（可采样/短期保留）：

```sql
CREATE TABLE memory_recall_traces (
    id              TEXT PRIMARY KEY,
    session_id      TEXT NOT NULL,
    turn_id         TEXT,
    project_key     TEXT NOT NULL,
    query_hash      TEXT NOT NULL,
    query_preview   TEXT, -- 脱敏、限长
    summary_revision INTEGER,
    candidate_count INTEGER NOT NULL DEFAULT 0,
    filtered_count  INTEGER NOT NULL DEFAULT 0,
    selected_ids_json TEXT NOT NULL DEFAULT '[]',
    dropped_json    TEXT NOT NULL DEFAULT '[]',
    token_estimate  INTEGER NOT NULL DEFAULT 0,
    token_budget    INTEGER NOT NULL DEFAULT 0,
    budget_source   TEXT,
    injected        INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL
);
```

约束：

- 默认不保存完整 query 和完整记忆正文；
- 采样率可配置，例如 1% 或仅 debug；
- 保留 7-30 天；
- 用户可通过 `why`/`why-not` 查看脱敏摘要；
- 不允许跨 scope 查询。

### 7.3 指标

```text
memory_layer_entries{scope,status}
memory_entries_active{scope,kind}
memory_recall_candidates
memory_recall_filtered{reason}
memory_recall_selected
memory_injection_count{scope,mode}
memory_injection_tokens{scope,mode}
memory_budget_used_ratio
memory_budget_saturated
memory_citation_total
memory_read_total
memory_candidate_to_active_ratio
memory_promotion_total{from,to}
memory_demotion_total{reason}
memory_stale_source_total
memory_freshness_status_total{status,kind}
memory_revalidate_total{result}
memory_stale_summary_blocked_total
memory_suppression_hit_total
memory_extract_success_total
memory_extract_invalid_json_total
memory_consolidate_duration_ms
memory_cleanup_deleted_total
memory_cross_scope_denied_total
```

### 7.4 `why` / `why-not`

`/memory why <id>`：

- 来源、scope、kind、confidence；
- created/last used/usage；
- revision/events；
- 为什么被选中；
- 是否 pinned/manual/correction；
- `freshness_status`、`last_verified_at`、`revalidate_after`、`stale_reason`；
- 是否可能过期，以及最后一次回源验证结果。

`/memory why-not <query>` 或 debug trace：

- 哪些记录被过滤；
- 过滤原因（scope/status/freshness/suppression/budget/token/duplicate/conflict）；
- 最终 token 预算与丢弃项。

### 7.5 告警

- 注入预算长期饱和；
- recall 空结果率异常；
- citation 率异常低；
- candidate 长时间不晋升；
- suppression 后仍被创建；
- stale summary 被注入；
- stale 记录比例异常升高；
- source changed 后长时间未失效；
- 跨 scope denied 激增；
- DB busy/lock；
- 抽取连续失败；
- user scope 未授权但被频繁请求。

## 8. 测试与验收

必须新增：

- 层级与 scope：
  - `TestMemorySessionNeverLeaksIntoProject`
  - `TestMemoryProjectNeverLeaksIntoUser`
  - `TestMemoryUserScopeRequiresOptIn`
  - `TestMemoryWorkspaceRecordsNotStoredAsMemory`
- 生命周期：
  - `TestMemoryPromotionRequiresEligibility`
  - `TestMemoryAutoPromotionToUserDisabled`
  - `TestMemoryPinnedSurvivesRetention`
  - `TestMemoryCorrectionSupersedesOlderRecord`
- 新鲜度：
  - `TestMemoryStaleNotInjected`
  - `TestMemoryStaleWarnOnlyLowRisk`
  - `TestMemorySourceChangeMarksStale`
  - `TestMemoryValidUntilExpires`
  - `TestMemoryRevalidationRestoresFresh`
  - `TestMemorySourceMissingBecomesStale`
  - `TestMemorySummaryWatermarkMismatchRebuilds`
  - `TestMemoryStaleSummaryFallback`
  - `TestMemoryWhyShowsFreshness`
- 召回：
  - `TestMemoryRecallFunnel`
  - `TestMemoryRecallUsesProjectThenUserPrecedence`
  - `TestMemoryRecallFiltersSuppressed`
  - `TestMemoryRecallDiversityLimit`
  - `TestMemoryRecallTraceCapturesDropReasons`
- Token：
  - `TestMemoryBudgetClampedByPromptBudget`
  - `TestMemorySummaryBudget`
  - `TestMemoryHybridBudgetSplit`
  - `TestMemoryTokenTruncationKeepsProvenance`
  - `TestMemoryNoInjectionWhenMandatoryLayersExceedBudget`
- 观测：
  - `TestMemoryMetricsEmitted`
  - `TestMemoryWhyShowsProvenance`
  - `TestMemoryWhyNotShowsBudgetDrop`
  - `TestMemoryTraceRedactsQuery`

### 验收门禁

- L0/L1 不写入 L2；
- L2 不跨 project_key；
- L3 默认关闭，未授权不可读；
- 每条 active 记录有 source/event；
- stale/unverified 记录默认不注入；
- summary input watermark 不一致时不注入旧 summary；
- source 变化能在 freshness sweep 或读取校验中标记 stale；
- 高风险事实具备回源读取路径；
- recall 结果可解释；
- memory token 不超总 prompt 预算；
- 注入 token 超预算为 0；
- 删除/纠正后不复活；
- trace 不含 secret/完整敏感正文；
- `off` 下 v2 自动行为为 0。

## 9. 默认参数建议

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `injection.mode` | `summary`（M3 后） | 迁移期可 `notes` |
| `injection.tokenBudget` | 600 | balanced |
| compact | 400 | 小上下文 |
| extended | 900 | 大上下文 |
| summaryTokenBudget | 800 | 生成上限 |
| maxItems | 5 | 记录条数 |
| maxSessionsPerRun | 2 | 抽取节流 |
| maxRecordsPerSession | 20 | 抽取上限 |
| candidateDays | 30 | candidate 保留 |
| maxUnusedDays | 30 | active 淘汰 |
| deletedDays | 30 | tombstone 保留 |
| auditDays | 180 | 审计保留 |
| `freshness.staleBehavior` | `exclude` | 默认不注入 stale |
| `freshness.defaultTtlDays` | 90 | 未配置 kind 的 TTL |
| `freshness.revalidateBatchSize` | 50 | 单次 sweep 上限 |
| `freshness.revalidateIntervalHours` | 24 | sweep 间隔 |
| `freshness.perKind.fact` | 30 天 | 高风险事实 |
| `freshness.perKind.reference` | 30 天 | 引用/链接 |
| recall trace 保留 | 7-30 天 | 可配置 |
| recall trace 采样 | 1% / debug | 避免膨胀 |

## 10. 仍需在 M0 决定

1. L3 user scope 是否进入 v1；
2. L2 → L3 的用户确认 UX；
3. `summary` 模式默认是否要求只读工具；
4. recall trace 是否默认采样，采样率；
5. FTS5 在 Windows 7 构建中的可用性；
6. 总 prompt 预算中 memory 的硬上限；
7. 是否需要多轮 query 重写（v1 建议不做）。
8. per-kind TTL、staleBehavior 和 freshness sweep 间隔的基线校准。

## 11. 结论

- **短期记忆**：已有 L0/L1 基础，不进入 memory.db；
- **长期记忆**：L2 是默认实现目标，L3 可选；
- **工作区记录**：L2 按 project_key 隔离；
- **全局记录**：只允许 user-level，不允许默认系统级共享；
- **观测性**：由 metrics + events + recall trace 三层组成；
- **新鲜度**：freshness_status + source hash/revision + TTL + summary watermark + 回源验证；
- **召回**：本地确定性漏斗，不调用 LLM；
- **Token**：分层预算，memory 必须服从总 prompt 预算，不能额外膨胀。

外部项目（Nuphus / DeepSeek Harness / ai-memory）的对照与可借鉴项见 `10-nuphus-deepseek-harness-memory-comparison.md`。
