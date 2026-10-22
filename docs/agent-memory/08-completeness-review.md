# 方案完整性与闭环审查

> 审查日期：2026-10-22  
> 审查对象：`docs/agent-memory/` 全套方案  
> 结论：**设计层面已基本形成闭环；代码层面尚未闭环，因为目前只有文档、未实施。**  
> 审查后修订：`03`、`04`、`05`、`06`、`07` 已补齐关键契约；`06 §8` 已形成 M0-M5 + v1.1 的阶段 DoD。

## 1. 审查结论先说

### 1.1 当前实际功能状态

当前仓库已有的闭环只有：

```text
手工 /memory add
  → notes.jsonl / memorystore
  → contextmgr 注入 top-k
  → Agent 使用
```

以及：

```text
会话 / 工具 observation
  → internal/memory 或 artifact/factledger
  → compact/checkpoint
```

**没有**代码层面的自动抽取、Consolidation、summary revision、引用 usage、删除抑制和后台 job 闭环。因此：

- 不能说“整体功能已经完成闭环”；
- 应该说“方案已经补齐为可实施闭环，代码尚未实现”。

### 1.2 设计状态

原方案已经覆盖主流程，但审查发现以下关键点会导致“看似有方案、实际无法闭环”：

1. `off` 与现有手工 memory 语义冲突；
2. 缺少 session 级 memory mode 契约；
3. 删除/纠正后的记忆可能被重新抽取复活；
4. 缺少 revision/audit 事件，无法追溯“怎么改的”；
5. summary 没有固定输入集合，无法精确重建/回退；
6. project key 未定义稳定 canonicalization；
7. delete/reset/session mode 的授权边界不明确；
8. summary/hybrid 模式的 token 拆分未定义；
9. 用户显式“记住/忘掉/纠正”的 Agent 工具闭环缺失；
10. 缺少明确的 transcript reader 边界与触发接入点；
11. 子 agent/team 的记忆策略未定；
12. `summary` 模式与检索工具依赖未说明。

这些问题已在相关文档中修补。修补后的设计满足闭环不变量，但仍需 M0 决策和实际实现验证。

外部借鉴项已进一步按 `README §3.4` 收敛：working digest、user scope/cross-session snapshot、semantic retrieval 等不再混入 v1；v1 只保留按 kind retention、召回诊断、session context 等必要能力。

## 2. 闭环判定

### 2.1 端到端闭环链路

```text
[1] 会话产生
  → [2] 触发/入选
  → [3] 只读 transcript
  → [4] Phase 1 抽取/校验/脱敏
  → [5] candidate 落库 + source + event
  → [6] suppression 检查
  → [7] Phase 2 去重/冲突/摘要
  → [8] summary revision + summary_items
  → [9] 动态尾部注入
  → [10] 工具检索/读取
  → [11] citation/usage
  → [12] 用户纠正/遗忘
  → [13] 淘汰/导出/回滚
```

### 2.2 闭环不变量

设计必须始终满足：

1. **来源不变量**：每条 active 派生记忆至少有一个 `memory_sources` 记录；
2. **变更不变量**：每次 create/update/correct/supersede/delete 至少写一条 `memory_events`；
3. **摘要不变量**：每个 active summary revision 有 `memory_summary_items` 和 input watermark；
4. **注入不变量**：每次注入都记录 revision、memory ids、token 估算；
5. **引用不变量**：每个 cited usage 都能映射到存在的 memory id 和 session/turn；
6. **删除不变量**：删除产生 tombstone + suppression，后续检索不注入、后续抽取不复活；
7. **纠正不变量**：correction 产生新 revision，旧记录 superseded，pinned 记录不被自动覆盖；
8. **隔离不变量**：查询必须同时匹配 scope + project_key；隔离失败 fail closed；
9. **模式不变量**：`off` 下 v2 自动行为为零，手工 memory 独立保留；
10. **回滚不变量**：summary 可回到上一 revision，schema 不自动 downgrade，notes/DB 保留。

## 3. 审查发现与修补记录

| # | 发现 | 严重性 | 修补 |
| --- | --- | --- | --- |
| 1 | `off` 与手工 notes 冲突 | P1 | `03 §5.4`、`04 §6`、`05 M0/M1` 明确 `manual.enabled` 独立 |
| 2 | 无 session memory mode 契约 | P1 | `03 §5.6`、`04 §7.2`、`05 M0/M2` 增加 enabled/disabled/polluted |
| 3 | 删除后可能被重新抽取 | P0/P1 | `04 §3.3.1` 增加 `memory_suppressions`，`03 §7.2` 增加检查 |
| 4 | 缺少 revision/audit | P1 | `04 §3.2.1` 增加 `memory_events` |
| 5 | summary 不可精确重建 | P1 | `04 §3.5.1` 增加 `memory_summary_items` 和 `record_ids_json` |
| 6 | project key 不稳定 | P1 | `03 §5.5`、`07 §2.3` 定义 git origin/canonical path |
| 7 | destructive API 权限不清 | P1 | `04 §7.2` 增加管理员/本机授权要求 |
| 8 | summary/hybrid 预算不明 | P2 | `03 §8.2.1` 增加 60/40 拆分 |
| 9 | 缺用户显式 memory 操作闭环 | P1 | `03 §8.6`、`04 §9`、`05 M4` 增加 add/correct/forget |
| 10 | 缺 transcript reader/触发边界 | P1 | `03 §7.1` 增加只读 reader 和接入点 |
| 11 | 子 agent/team 策略不明 | P2 | `03 §5.7`、`04` 增加默认排除/无写权限 |
| 12 | summary 模式无工具依赖说明 | P2 | `03 §8.2.1`、`04 §9` 增加依赖矩阵 |
| 13 | 手工/纠正记录仍可能被自动覆盖 | P1 | `04 §3.1` 增加 `pinned`，`03 §8.6`、`05 M3`、`06` 增加测试 |
| 14 | 缺少“用户说忘掉”与自动删除的边界 | P1 | `03 §8.6` 明确 forget 默认仅用户/管理 API |
| 15 | audit/usage 表级联删除会丢失审计 | P1 | `04 §3.2.1/3.3` 取消审计表级联，物理清理前归档 |
| 16 | summary 只能切换 active，缺少显式回滚/导入 | P2 | `04 §7.2` 增加 summary activate 与 import，`06` 增加测试 |
| 17 | citation 可被模型编造，usage 可能虚增 | P1 | `03 §8.5`、`04 §10.2` 限制为本 turn 注入/读取集合，`06` 增加测试 |
| 18 | 读取使用边界不清，可能每轮都查记忆 | P2 | `03 §8.4`、`04 §10.1` 增加使用/跳过和过期验证规则 |
| 19 | summary 内容格式与 fallback 未定义 | P1 | `04 §10.1.1` 固化 Markdown 结构、长度、校验和确定性 fallback |
| 20 | hard-deleted 记录无法 restore | P2 | `04 §7.2` 明确 restore 只接受 tombstone，物理删除需导入恢复 |
| 21 | 短期/长期记忆与生命周期不够集中 | P1 | 新增 `09`：L0-L4 分层、晋升/降级/淘汰 |
| 22 | workspace 与 global 记录边界不够直观 | P1 | `09 §2/§3` 明确 workspace memory 与 user-scope global |
| 23 | 召回漏斗与 recall trace 不够具体 | P1 | `09 §5/§7`、`04 §3.3.2` 增加召回阶段与 trace |
| 24 | memory 预算与总 prompt 预算关系未公式化 | P1 | `09 §6` 增加总预算 clamp、拆分和截断顺序 |
| 25 | 缺少外部成熟实现的对照与取舍 | P2 | `10`：Nuphus、DeepSeek Harness、ai-memory 对比与借鉴优先级 |
| 26 | 外部借鉴项未明确 v1/v1.1/后续 | P1 | `README §3.4`、`05 §1.1` 固化版本范围；`04` 增加 retention_class |
| 27 | ai-memory 的 handoff / tiered brief / aging 尚未进入主设计 | P1 | 作为 v1.1 候选记录在 `10 §4/§6`，不改变 v1 范围 |
| 28 | v1.1 扩展点未在主架构/schema 中留位 | P1 | `03 §8.1` 增加扩展点；`04 §3.8` 定义 handoff、RRF trace 与不新增事实源的边界；`05 M0` 冻结“不建表/不写入” |
| 29 | 阶段验收缺 M0，M1-M5 只有定性门禁 | P1 | `06 §8` 扩为阶段 DoD：通用门禁、M0-M5 退出条件、量化门槛、v1.1 门禁和验收证据包 |
| 30 | 遗忘/更新有状态机，但缺“过时信息”契约 | P0 | `04 §3.1.1/§3.5.2` 增加 freshness 字段、TTL、summary watermark 门禁；`03 §7.5`、`09 §5.3.1`、`06/07` 增加 stale 过滤、回源验证、测试与指标 |

## 4. 按维度审查

### 4.1 功能完整性

| 维度 | 状态 | 说明 |
| --- | --- | --- |
| 手工记忆 | 已存在 | 不能因 v2 上线回归 |
| 自动抽取 | 设计完成 | 需要 M2 实现 |
| 会话筛选 | 设计完成 | 需要 session mode |
| 去重/合并 | 设计完成 | hash + source + Consolidation |
| 冲突/纠正 | 设计完成 | supersede + pinned + event |
| 摘要/索引 | 设计完成 | summary revision + items |
| 注入 | 设计完成 | 动态尾部，受预算 |
| 按需检索 | 设计完成 | 工具或降级 |
| 引用/usage | 设计完成 | citation parser + usage |
| 删除/遗忘 | 设计完成 | tombstone + suppression |
| 导出/恢复 | 设计完成 | JSONL/Markdown + restore |
| 后台任务 | 设计完成 | lease/retry/watermark |

### 4.2 数据完整性

- 每条记录有稳定 ID、scope、project_key、content hash；
- 每次变更写 event；
- summary 有 revision、input watermark、record items；
- 删除是状态变化，不是静默物理删除；
- 迁移幂等且保留原 notes；
- 读路径失败可降级。

### 4.3 安全完整性

- 记忆明确标记为历史数据；
- 不能授权工具/权限；
- secret 落库前脱敏；
- 跨项目/跨用户 fail closed；
- destructive API 需要授权；
- 自动记录默认 candidate；
- pinned/manual 不被自动覆盖。

### 4.4 运维完整性

- 有 status/jobs/audit/export；
- 有 retention 和 cleanup；
- 有回滚/降级；
- 有告警指标；
- 有数据库并发策略。

### 4.5 测试完整性

- 有迁移、隔离、注入、cache、抽取、Consolidation、usage、删除、权限测试；
- 有 E2E 和 Windows 7 验证计划；
- 但测试尚未实现，当前只能作为验收清单。

## 5. 仍然存在的残余风险

这些不是文档缺口，而是需要实现/实测才能关闭的风险：

1. **语义相似但 hash 不同的记忆复活**：v1 只做 hash 去重，未引入 embedding；需要 Consolidation 人工规则和用户 correction 优先。
2. **模型抽取质量不稳定**：必须通过 shadow 评估校准，不可仅靠 prompt。
3. **SQLite 在真实多进程/高负载下的锁行为**：需要实测和 health 回归。
4. **Windows 7 的 FTS5 能力**：未验证；设计已允许降级。
5. **citation block 的流式渲染/导出边界**：需要前后端联调。
6. **session mode 存 metadata 还是独立表**：按建议默认（独立表），M0 转 ADR。
7. **`summary` 模式无工具时是否默认**：按建议默认（允许，但标记 `no_retrieval_tools`），M0 转 ADR。
8. **历史 `.aicli/memory` 的 ID 兼容**：迁移实现时确认。
9. **项目移动/克隆的 project key 语义**：设计有规则，需真实路径测试。
10. **自动记忆的成本**：需要 shadow 数据决定默认触发频率和模型。
11. **源变化检测依赖 source hash/revision**：无法访问的 source 只能标记 `stale`，不能证明 `fresh`；per-kind TTL 仍需 M0 基线校准。

## 6. 可实施性判断

| 阶段 | 是否可以立即开始 | 前置条件 |
| --- | --- | --- |
| M0 基线与契约 | 可以 | 按 `07 §8.1` 默认决策转 ADR；记录基线与失败矩阵 |
| M1 存储与读取 | 可以 | M0 基线；不依赖自动抽取 |
| M2 抽取 | 部分可以 | 必须先有 M1 + session mode + suppression |
| M3 Consolidation | 部分可以 | 必须先有 M2 candidate、summary items、去重规则 |
| M4 工具/API/UI | 可以并行设计 | 需要 M0 权限与工具命名决策 |
| M5 上线 | 不可以 | 需要 shadow 数据和验收结果 |

## 7. 最终判断

**是否完成闭环：**

- **代码功能：没有。** 当前只有 memorystore MVP，自动记忆和 Consolidation 尚未实现。
- **设计方案：在本次审查修补后，已达到“可实施闭环”。** 从触发、抽取、存储、摘要、注入、引用、纠正、删除、迁移、回滚都有明确契约。
- **生产可用：还没有。** 必须完成 M1-M5、通过测试与 shadow 评估，才能说生产功能闭环。

## 8. 下一步建议

1. 在 M0 将 `07-risks-operations-migration.md §8.1` 的默认决策转为 ADR/配置契约；
2. 先实现 M1，不引入自动抽取；
3. M1 验收通过后启用 M2 shadow；
4. 用真实会话建立标注集，再决定 M3 默认策略；
5. 任何默认开启前，先通过 `06-test-and-acceptance.md §8` 的对应阶段 DoD，重点验证删除复活、跨项目隔离、prompt cache 和 SQLite 锁四项 P0/P1 门禁。
