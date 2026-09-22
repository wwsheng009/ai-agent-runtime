# 任务难度路由审计与安全网加固实施方案

更新时间: 2026-09-21（方案）；2026-09-22（P0/P1/P2 实施回写）；2026-09-22（v2：task_type 收编修订，见 §12）；2026-09-22（P4 实施回写）
状态: **partially-implemented** — P0（改动点 1–7）、P1（8–13）、P2（14–18）与 **P4（task_type 收编，改动点 19–29）** 已实施并通过门禁（见 §6.1.1、§7 各阶段状态行与 §10 验收状态）；P3（字面量改常量，可选增强）待实施
适用仓库: `E:\projects\ai\ai-agent-runtime`
证据基线: 2026-09-21 工作树 + 四组只读取证（事件库 / 渲染日志 / 批次账本 / 路由代码走查）
审查方法: 静态代码走查（grep/view）+ 三类持久存储只读 SQL 取证（不写入、不触发任何子代理运行）+ 与 `docs/plan/` 既有 123 份方案的覆盖比对

---

## 1. 文档定位

本文件是 `docs/plan/task-difficulty-model-routing-plan.md`（下称「主设计」）的**加固增量**，不是重开设计。

主设计解决的是「难度如何声明、如何映射到 provider/model、如何接入调度」——这部分**已经落地**。本文件解决的是落地之后的三个新问题：

1. **审计不完整**：难度声明有账，路由决策没账；没跑到终态的子代理在所有持久存储里都查不到"当时被路由到哪个模型"。
2. **安全网可绕过**：启发式提升被显式声明短路，且关键词只认英文，在中文工作场景下等于不存在。
3. **配置陷阱**：别名键冲突静默不确定、`max_expert_concurrency: 0` 语义反直觉且静默。

本文件只处理 `docs/plan/task-difficulty-model-routing-plan.md` 已实施部分暴露的**缺口**。凡是主设计已覆盖的条目（提示词注入、schema 声明、resolver 优先级、provider runtime 构造、`routing disabled` 零行为变化），本文件只做差异标注，不重开方案。

---

## 2. 相关文档

| 文档 | 与本文件的关系 |
| --- | --- |
| `docs/plan/task-difficulty-model-routing-plan.md` | 主设计（§18-33 为修订后的实施方案）；本文件 G1-G7 全部是其已实施部分的落地偏差 |
| `docs/plan/sse-live-event-channel-optimization-plan.md` | A/B/C/D 交付通道模型与 `internal/events/contract.go` 注册表的来源；本文件 G1、G2 的修改落点 |
| `docs/plan/multi-agent-durable-lifecycle-hardening-plan-20260920.md` | 批次持久层加固（H6-H10）；本文件 G1 的 `child_session_id` 空值现象与其 H7 同源，见 §3.4 |
| `docs/plan/spawn-team-teammate-model-routing-plan.md` | `spawn_team` 路径的模型路由；本文件 G3/G4 的启发式语义变更会影响它 |
| `docs/plan/subagent-readonly-boundary-transparency-plan-20260917.md` | 只读边界回执进入 tool result 的先例；本文件 G7 沿用同一手法 |
| `docs/plan/multi-agent-execution-optimization-plan.md` | 主计划（等待/唤醒/配额/可观测性）；本文件 G6 的并发闸门与其配额章节相邻 |
| `docs/plan/session-analytics-subagent-reliability-implementation-plan.md` | 子代理可靠性统计口径；本文件 G1 的路由审计字段应纳入其统计面 |
| `docs/plan/main-agent-dynamic-provider-model-switching-plan-20260921.md` | **姊妹方案**：主 Agent 自身的动态 provider/model 切换。与本文件**功能正交**（它修的是「主 Agent 路由**根本没有**」，本文件修的是「子 Agent 路由**查不到账**」），但**共享事件契约与门禁**——见 §2.1 与 §5.2 第三步 |
| `plan.md`（仓库根，2026-09-22） | **task_type 收编的评审与拍板来源**：5 项决策（`task_type` 替换路由层 `role` + 新增 `task_subject`、跨类降档 1 步确认、同步 `spawn_team` 与批次账本、修订本文件与姊妹方案、同步观测与两个前端）。本文件 §5.4 修订块 / §6.4 / §7 P4 / §12 均以它为准 |

### 2.1 编号约定（与姊妹方案的跨文件引用）

本文件与姊妹方案同处 `docs/plan/`，且都需要阶段编号，直接沿用 `P0/P1/...` 与 `G1/G2/...` 会在评审时产生**跨文件歧义**（同一个 `P0` 在两份文件里指两件不同的事）。

约定如下：

| 前缀 | 含义 | 用法 |
| --- | --- | --- |
| 无前缀 `G1`–`G7` / `P0`–`P3` | **本文件局部编号** | 仅在本文件内部使用，含义见 §4 / §7 |
| `SA-G*` / `SA-P*` | **S**ubagent **A**udit | 姊妹方案引用本文件时使用 |
| `MA-G*` / `MA-P*` | **M**ain **A**gent | 本文件引用姊妹方案的主 Agent 编号时使用 |

> 本文件在自身范围内仍写 `G1`/`P0`（不改动正文，避免大面积重编号引入错误）；**跨文件引用一律加前缀**。

---

## 3. 事实基线与取证方法

> 本章所有数字均来自**只读**取证（`mode=ro` 打开 sqlite / 纯文本扫描），取证过程未运行任何子代理、未写入任何存储。引用行号会随代码漂移，实施前需按符号名复核。

### 3.1 取证范围

| 存储 | 路径 | 规模 | 语义 |
| --- | --- | --- | --- |
| 会话事件库 | `backend/data/runtime/session_runtime.sqlite` → `session_events` | 680,225 行 / 1,019 个 session | A 通道落盘产物（web/API 路径） |
| 渲染事件日志 | `~/.aicli/chat-logs/2026/09/22/*/events/runtime-events.jsonl` | 200+ 个会话目录 | chat runtime bridge 的 render-model 日志（会话恢复回放用） |
| 批次账本 | `~/.aicli/sessions/runtime/subagent_batches/*.sqlite` | 765 个文件 / 68 个批次 / 165 个任务 | 批次与任务的 durable 事实源 |
| 代码 | `backend/internal/{agent,events,modelrouting,supervision,toolbroker}` | — | 生产者 / 注册表 / 策略 / 交付 |

### 3.2 取证结论一：subagent 家族在事件库里只剩一个类型

对 `session_events` 按类型聚合：

| 事件类型 | 行数 |
| --- | --- |
| `subagent.completed` | **170** |
| `subagent.started` | **0** |
| `subagent.batch.started` / `.completed` / `.failed` / `.canceled` / `.timed_out` / `.orphaned` | **0** |
| `subagent.task.started` / `.completed` | **0** |
| `subagent.progress` / `.batch.progress` | **0** |

即：**680k 行的持久事件流里，subagent 家族只有"终态摘要"一种类型**。批次的开始、任务的里程碑、批次的失败态终态，在事件库中一行都没有。

`subagent.completed` 的 170 行分布在 68 个不同 `session_id` 上，其中 157 行归属 `session_*` 形态的**父会话**、13 行归属 `subagent_*` 形态的子会话——说明 agent-controller 的父会话镜像路径（`internal/api/skills/session_runtime_support.go`）**是生效的**，父会话确实能拿到终态摘要。

### 3.3 取证结论二：开工时的路由审计在渲染日志里 0 命中

对 `2026/09/22` 全部会话目录的 `runtime-events.jsonl` 扫描 subagent 家族类型：

| 会话 | 命中的 subagent 类型 |
| --- | --- |
| `session_20260922065842_sHO9QvUc`（真实 3 任务批次探针） | `subagent.batch.started×1`、`subagent.batch.completed×1`、`subagent.completed×2` |
| 其余 200+ 个会话 | 全部为空 |

**`subagent.started` 在所有会话中 0 命中**，尽管它是唯一在开工时刻携带完整路由审计的事件（`difficulty / difficulty_source / route_provider / route_model / route_reasoning_effort / route_source / route_warnings / fallback_used`）。

机制已定位，两层独立丢失：

1. **归属层**：`internal/agent/scheduler.go` 的 `emitRuntimeEvent("subagent.started", childSessionID, "", ...)` 把事件挂到**子会话 id**；而 chat runtime bridge 有 primary-session 过滤（`cmd/aicli/commands/chat_runtime_events.go` 的 `isForeignSessionTerminalEvent` / `matchesPrimarySessionID`），非主会话归属的事件不会进入父会话的 render 日志。
2. **通道层**：`internal/events/contract.go` 把 `subagent.started` 登记为 **`ChannelTailOnly`**（D 通道，不落盘），因此即使归属正确也不会进事件库。

### 3.4 取证结论三：批次账本记了难度，但没记路由

`subagent_tasks` 表结构包含 `difficulty` 列，但不含任何 `route_*` 列。逐字段核对 `spec_json` / `result_json`：

| 任务 | status | difficulty | `spec_json` 中的路由信息 | `result_json` 中的路由信息 |
| --- | --- | --- | --- | --- |
| `pipe-code-audit` | failed | hard | `difficulty` + `difficulty_rationale` | 无 |
| `pty-semantics-research` | failed | expert | `difficulty` + `difficulty_rationale` | 无 |
| `history-probe-audit` | failed | hard | `difficulty` + `difficulty_rationale` | 无 |

**结论：难度声明有账（批次账本 + 事件库终态摘要），路由决策只在事件库的 `subagent.completed` 里才有账。** 批次账本这一侧完全没有 `route_provider / route_model / route_reasoning_effort / route_source / route_warnings / fallback_used`。

### 3.5 取证结论四：未跑到终态的子代理规模

对 765 个批次账本文件聚合（43 个含任务行）：

| 维度 | 分布 |
| --- | --- |
| 批次终态（68 个） | `failed` 42 / `timed_out` 15 / `completed` 10 / `running` 1 |
| 任务终态（165 个） | `failed` 124 / `succeeded` 37 / `failed_with_result` 2 / `running` 2 |
| 124 个 failed 任务中 `child_session_id` 为空 | **88 个（71%）** |
| 57 个有 `child_session_id` 的任务中，事件库查不到 `subagent.completed` | **50 个（88%）** |

> **口径说明（重要）**：事件库只覆盖 web/API 路径，批次账本覆盖 CLI + web 两条路径，两者**并非同源可比**。因此 50/57 应读作「父会话事件流中无法反查路由记录的比例指示」，而非严格缺陷计数。严格的缺陷计数需要在同一条路径上做受控复现，已列入 §10 验收项 A1。
>
> 同一批取证里还有两个旁证：(a) 88/124 的 failed 任务连 `child_session_id` 都没写（与 `multi-agent-durable-lifecycle-hardening-plan-20260920.md` 的 H7「子会话绑定到终态才写」同源）；(b) 765 个批次库文件中有 4 个打开失败（需在实施时单独复核是空文件还是损坏，本文件不据此下结论）。

### 3.6 关键代码锚点

| 语义 | 位置（符号名优先） |
| --- | --- |
| 路由审计载荷合并 | `internal/agent/child_factory.go` → `mergeRouteAuditPayload` |
| 开工审计发射点 | `internal/agent/scheduler.go` → `emitRuntimeEvent("subagent.started", childSessionID, ...)` |
| 终态审计发射点 | `internal/agent/scheduler.go`（成功/失败两处）、`internal/agent/subagent_retry.go` |
| 事件契约注册表 | `internal/events/contract.go` → `runtimeEventContracts` |
| 契约门禁 | `internal/events/contract_test.go` → `TestExternalEventFamilyConstantsAreRegistered` 等 |
| 产品已知类型目录 | `internal/runtimeobserve/known_types.go` |
| 难度归一 | `internal/modelrouting/types.go` → `NormalizeDifficulty` |
| 难度解析与提升 | `internal/modelrouting/resolver.go` → `resolveDifficulty` / `promotedDifficulty` |
| 配置校验 | `internal/modelrouting/validate.go` → `Validate` |
| expert 并发闸门 | `internal/agent/scheduler.go` → expert semaphore 获取路径 |
| 批次终态事件名 | `internal/agent/subagent_batch_coordinator.go` → `terminalNotificationFromBatch` |
| 终态投递 | `internal/agent/subagent_batch_coordinator.go` → `deliverTerminalOnce` |

---

## 4. 缺口清单

严重度按「出事时能不能查到」与「会不会静默放行高风险任务」两个维度排序。

### G1（高）开工路由审计无持久记录

**现象**：子代理在开工时刻的路由决策（provider / model / reasoning_effort / 来源 / 告警 / fallback）没有任何持久存储承载。

**证据**：
- `subagent.started` 在 200+ 会话的 render 日志中 0 命中（§3.3）；在 680k 行事件库中 0 行（§3.2）。
- 唯一带 `route_*` 的持久记录是 `subagent.completed`（170 行 / 68 个子会话），只在子代理跑到终态时产生。
- 批次账本只记 `difficulty`，不记 `route_*`（§3.4）。

**根因**（两层，互相独立）：
1. `subagent.started` 被登记为 `ChannelTailOnly`（不落盘）；
2. 发射时挂的是 `childSessionID`，被父会话 bridge 的 primary-session 过滤挡掉。

**影响**：取消 / 超时 / 孤儿 / 进程崩溃的子代理，其"当时被路由到哪个模型"在**所有**持久存储里都查不到。这正是事后追责与成本归因最需要的一类记录。同时 `subagent.batch.*` 全家族在事件库 0 行，意味着**批次维度的审计在事件库里整体缺席**。

**修复方向**：见 §5.1。

### G2（高）批次失败态终态事件未登记进交付契约

**现象**：`subagent.batch.failed | canceled | timed_out | orphaned` 由 `terminalNotificationFromBatch` 产出并经 `deliverTerminalOnce` 投递，但注册表只登记了 `subagent.batch.started` 与 `subagent.batch.completed`。

**证据**：`internal/events/contract.go` 的 D 通道段落只有 `started` / `completed` 两条 batch 记录；未登记类型 `ChannelsFor` 返回 0，`DeliveryChannelsFor` 派生不出任何通道 → 不下帧、不落盘、不进尾巴帧。

**影响**：一个被取消或超时的批次，在 chat 侧**没有任何可观测的终态事件**。父代理只能靠 mailbox 或轮询 durable 状态发现，用户界面则完全没有信号。这与 §3.5 的 `timed_out 15 / failed 42` 规模叠加，属于高频路径而非边角。

**根因**：生产者用字符串字面量直接产出事件名，而门禁 `contract_test.go` 只断言 `internal/chat/events.go` 常量与少数具名常量（`toolprotocol.EventTypeProgress`、`supervision.EventTypeSubagentProgress` 等），**覆盖不到 `internal/agent` 里的裸字面量**，因此这类漂移 CI 抓不到。

**修复方向**：见 §5.2。

### G3（中）启发式提升被「显式声明」完全短路

**现象**：只要 LLM 显式给出合法 difficulty，`verifier` 角色提升与关键词提升都不会执行。

**证据**：`internal/modelrouting/resolver.go` → `resolveDifficulty` 在 `NormalizeDifficulty` 成功时**立即 return**，`promotedDifficulty` 位于其后，仅在「难度缺失或非法」的分支里被调用。

**影响**：安全网只对"没写"和"写错"生效，对"写了个偏低的合法值"完全无效。一个 goal 含 `security` / `migration` 的写任务被标成 `easy`，就直接走 easy 档路由，且没有任何告警。这不是未覆盖分支，是设计上的短路。

**修复方向**：见 §5.3。

### G4（中）关键词启发式只认英文

**现象**：提升关键词表为 `security | permission | migration | architecture | provider | protocol`，对 `strings.ToLower(task.Goal)` 做子串匹配。

**证据**：`internal/modelrouting/resolver.go` → `promotedDifficulty`。

**影响**：本仓库的实际工作语言是中文（批次账本中的 `difficulty_rationale` 大量为中文），"安全 / 权限 / 迁移 / 架构 / 协议 / 跨系统一致性"这类高风险表述**一条都不命中**。叠加 G3，中文 + 显式低难度 = 完全无保护。实测中 expert 档能命中，靠的是显式声明而非启发式。

**修复方向**：见 §5.4。

### G5（中低）别名键冲突不报错，路由档位不确定

**现象**：配置里同时出现 `normal:` 与 `medium:`（或 `hard:` 与 `complex:`）时，命中哪个取决于 Go map 迭代顺序。

**证据**：`internal/modelrouting/types.go` → `NormalizeDifficulty` 把 `medium/standard/default` 归一为 `normal`；`internal/modelrouting/resolver.go` → `routeProfileFromMap` 遍历 map 取第一个归一后相等的键；`internal/modelrouting/validate.go` → `Validate` 只逐个校验键合法性，不检查归一后撞车。

**影响**：同一份配置在不同进程可能落到不同 profile（不同 model / reasoning_effort），且**没有任何日志或告警**。排障时表现为"偶发的模型不一致"。

**修复方向**：见 §5.5。

### G6（中低）`max_expert_concurrency: 0` 语义反直觉且静默

**现象**：`0` 被当作"无上限"，而运维通常理解为"禁止 expert"或"没有 expert 槽位"。

**证据**：`validate.go` 只拒绝负数；`scheduler.go` 的 expert 闸门把 `<= 0` 视为不限流（semaphore 为 nil 时直接放行）。

**影响**：最需要限流的一档（expert = 最贵、最慢、最可能触发 provider 限流）反而完全不限流，且没有任何审计字段体现"限流已关闭"。

**修复方向**：见 §5.6。

### G7（低）路由审计不进 tool result，决策者本人看不到

**现象**：`spawn_subagents` 返回给模型的文本只含 finding 与 read-only 边界，不含 `difficulty` 解析结果与 `route_*`。

**证据**：真实会话 `chat.json` 中工具结果无任何 `route_*` 字段，模型自己在回复中报告"未包含"。

**影响**：live tail、web trace、`doctor subagent-route` 都能看到路由结果，**唯独发起决策的父模型看不到**，因此无法自我纠正"我把难度标低了"。这是 G3/G4 的天然补偿通道，成本极低。

**修复方向**：见 §5.7。

### 4.1 已核实**不是**缺口（避免误报）

| 观察 | 核实结论 |
| --- | --- |
| 非法 difficulty 值 | `spawn_agent` / `spawn_subagents` / `spawn_team` 入口都先归一校验并报错；`difficulty_invalid_defaulted` 只可能从 planner / 内部路径进来，且已有单测覆盖 |
| `subagent.completed` 双生产者重复写 | `ProducerPersistedEvent` 以 `payload["seq"]` 为判据跳过 A 通道桥，实测 170 行无重复堆积 |
| 父会话拿不到终态摘要 | 不成立：157/170 行归属父会话，镜像路径生效 |
| `difficulty` 未持久化 | 不成立：批次账本 `subagent_tasks.difficulty` 与 `spec_json.difficulty_rationale` 都有账 |

---

## 5. 方案设计

### 5.1 G1：开工路由审计落盘

**方案：新增一个 A 通道事件类型 `subagent.route.resolved`，由调度器在决策点发射，归属父会话。**

| 项 | 设计 |
| --- | --- |
| 事件类型 | `subagent.route.resolved` |
| 交付通道 | `ChannelSessionStore \| ChannelTailOnly`（对齐 `subagent.completed` 的既有先例） |
| 发射点 | `internal/agent/scheduler.go` → `runChildUncontracted`，在 `spec.Decision` 已可用处（即现有 `subagent.started` 发射点紧邻位置） |
| 会话归属 | **`options.ParentSessionID`**（与 `subagent.started` 的 `childSessionID` 不同，这是本方案的关键点） |
| 载荷 | `subagent_id`、`role`、`difficulty`、`difficulty_source`、`route_provider`、`route_model`、`route_reasoning_effort`、`route_source`、`route_warnings`、`fallback_used`、`parent_session_id`、`child_session_id`、`batch_id`、`attempt`、`trace_id`，外加截断后的 `goal` / `difficulty_rationale` |
| 载荷约束 | `goal` 与 `difficulty_rationale` 各截断到 256 字符；整行目标 ≤ 2 KB。理由见 §3.2 的字节分布教训——审计行必须小而稳，不能重演"75% 字节是流式增量"的膨胀 |
| 重试语义 | 不做去重；每次 attempt 发一行并带 `attempt` 序号，保留完整重试轨迹（`subagent_retry.go` 已有多次发射的既有形态） |
| 与既有事件的关系 | **保留 `subagent.started` 原样**（遵守仓库既有的"只补登记不改行为"纪律）。D 通道的 `subagent.started` 继续服务实时 UI 里程碑；新事件是纯增量 |

**为什么是新增而不是改造 `subagent.started`**：D 通道的定义是「仅回合末尾巴补发，实时通道与事件库都没有它们」——它是**瞬时 UI 里程碑**。而路由决策是**治理数据**，语义上属于 A 通道。把治理数据塞进瞬时通道，正是 G1 的成因；再改造它会把两个语义继续绑死。同时改归属会让子会话自己的尾巴帧丢失里程碑，属于回归风险。

**常量与门禁**（必须同时完成，否则重演 G2 的漂移）：

1. 在 `internal/supervision` 新增导出常量（沿用 `EventTypeSubagentProgress` 的既有模式）。
2. `internal/events/contract.go` 的注册表用**字符串字面量**登记——该文件的硬约束是「不得 import 任何内部包」，不可用常量替代。
3. `internal/events/contract_test.go` 的门禁表引用 `supervision` 常量并断言 `IsRegisteredEventType` + `ChannelsFor` 期望值。
4. 同步 `internal/runtimeobserve/known_types.go` 的产品已知类型清单（注册表 ↔ 目录双向一致门禁会强制这一点）。

> 实施前需确认：`internal/agent` → `internal/supervision` 的 import 方向是否已存在（测试文件已存在该 import）。若生产代码方向受限，退路是把常量放在 `internal/events` 的同包文件里（同包常量不违反"不得 import 内部包"的约束）。

**配套（可选，P2）**：给批次终态载荷加一个紧凑 `route_digest`（`task_id → difficulty/route_model`），使批次维度也有一条可读的路由摘要，便于 `subagent.batch.completed` 单行自解释。

### 5.2 G2：批次失败态终态事件登记 + 门禁扩展

**第一步：登记 4 个类型。**

| 事件类型 | 建议通道 | 理由 |
| --- | --- | --- |
| `subagent.batch.failed` | `ChannelSessionStore \| ChannelTailOnly` | 批次可能永远等不到父代理醒来读 mailbox，终态必须 durable |
| `subagent.batch.canceled` | 同上 | 同上 |
| `subagent.batch.timed_out` | 同上 | 同上（§3.5 实测 15 个批次属此态，是高频路径） |
| `subagent.batch.orphaned` | 同上 | 同上 |

通道选择存在一个已知的**不一致**：`subagent.batch.started` / `.completed` 是纯 `ChannelTailOnly`。本方案选择"失败态更重"（落盘），理由是失败态需要事后追责与统计，而成功态有 `subagent.completed` 与批次账本兜底。该取舍记入 §9 风险 R3，并给出替代方案（先与 started/completed 保持 tail-only 一致，另发一条 A 通道摘要）。

**第二步：修门禁（防复发的关键）。**

G2 的真正病因不是漏登记，而是**门禁覆盖不到生产者的裸字面量**。建议双管齐下：

1. **主措施（低成本、合既有模式）**：把 `terminalNotificationFromBatch` 返回的字符串字面量改为 `internal/supervision` 常量，再把这些常量加入 `contract_test.go` 的门禁表。这样门禁天然覆盖，且与 `supervision.EventTypeSubagentProgress` 的既有做法一致。
2. **辅措施（覆盖盲区）**：新增一个扫描型测试，对 `internal/agent`、`internal/api/skills`、`internal/toolbroker` 中形如 `emitRuntimeEvent("<literal>"` 的调用提取字面量并断言已登记。用于捕获未来新增的裸字面量发射点。

**第三步（与姊妹方案的接口，必须一并覆盖）**：上述扫描型测试的覆盖目录 `internal/agent` **正是姊妹方案 `main-agent-dynamic-provider-model-switching-plan-20260921.md` 的发射点所在**（其 `main_agent.route_*` 事件族经 `internal/agent/loop.go` → `emitRuntimeEvent` 发射）。

- 姊妹方案承诺其发射点**只引用具名常量**（其 §6.2 主措施）；
- 本文件的扫描测试**必须**把该发射点纳入覆盖范围，否则姊妹方案落地后会**原样复现本文件 G2 的病因**；
- 实施顺序上：**本文件的门禁扩展先合入，姊妹方案的契约登记后合入**（姊妹方案 §8.1 已列为硬门禁）。

> 这是一处**单向依赖**：本文件不依赖姊妹方案的任何代码，但姊妹方案的治理阶段依赖本文件的扫描测试。

**第四步**：确认 nil-batch 兜底路径（`terminalNotificationFromBatch(nil)` 直接返回 `subagent.batch.failed`）也走同一注册与投递链路。

### 5.3 G3：提升不再被显式声明短路

**核心改动**：`resolveDifficulty` 的显式分支不再提前返回，而是**继续执行提升并取 rank 最大值**（单调，永不降级）。

| 输入 | 今天 | 改后 |
| --- | --- | --- |
| `difficulty=easy`，goal 含 `migration` | easy（`explicit`） | hard（`explicit_promoted`） |
| `difficulty=easy`，role=verifier | easy（`explicit`） | normal（`explicit_promoted`） |
| `difficulty=expert`，goal 含 `security` | expert（`explicit`） | expert（`explicit`，提升不降级） |

**新增审计语义**：`difficulty_source` 增加 `explicit_promoted`；`route_warnings` 增加 `difficulty_promoted_over_explicit`。

**三态开关（避免一次性改变全量行为）**：`AICLISubagentRoutingConfig.PromoteExplicitDifficulty` 取 `off | warn | enforce`。

| 取值 | 行为 |
| --- | --- |
| `off` | 回到今天的行为（短路），不告警 |
| `warn`（**首版默认**） | 不改变档位，但写入 `route_warnings` 与日志，用于观测真实命中率 |
| `enforce` | 真正提升档位（目标行为，下个大版本切为默认） |

**为什么默认 `warn`**：本改动会实打实提高部分任务的模型档位与成本。先在 `warn` 下收集命中率，确认不会把大量正常任务误升档，再切 `enforce`——这是"零行为变化上线"纪律与"安全网必须生效"之间的最小冲突路径。

**修订（2026-09-22 v2，task_type 收编）**：G3 的 rank-max 公式扩为 `rank = max(显式 difficulty, floor(task_type), 角色底, 关键词命中)`——`floor(task_type)` 是**新增的第四个输入**，与本节的三态开关、单调性、`explicit_promoted` 审计语义**正交叠加**；`task_type` 缺省时公式退化为今天的形态，`off` 下逐字节不变。详见 §5.4 修订块与 §6.4。

### 5.4 G4：关键词启发式中文化与可配置

**改动**：`promotedDifficulty` 的关键词表从「英文 6 词」扩展为「高信号 + 弱信号组合」两档，并支持配置追加。

| 档位 | 触发规则 | 建议词表 |
| --- | --- | --- |
| 高信号（单命中即升 hard） | 任一命中 | 英文：`security` `permission` `migration` `architecture` `provider` `protocol`；中文：`安全` `权限` `鉴权` `认证` `迁移` `架构` `协议` `加密` `密钥` `跨系统` `并发安全` |
| 弱信号（需 ≥2 命中，或 1 命中 + 写任务） | 计数阈值 | 中文：`一致性` `兼容` `重构` `回滚` `灰度` `发布` `边界` `契约`；英文：`consistency` `compatibility` `refactor` `rollback` `release` `boundary` |

**匹配前归一**：全角→半角、大小写折叠、空白折叠。匹配分两级，第二级是第一级的**纯增量**（历史子串语义零回归）：

1. **子串匹配**（历史语义，保持不变）：中文无词边界问题，直接子串匹配；英文也先走子串。不引入正则，避免配置注入风险。
2. **英文词形归一**（已实现，2026-09-22）：词尾形态不同时按词干再比一次，使 `migrate` / `migrating` / `migrated` / `migrations` 都能命中词表里的 `migration`，`encrypt` 命中 `encryption`。规则是**固定后缀表的单次剥离**（`-ies` / `-y` / `-ions` / `-ion` / `-ing` / `-ed` / `-es` / `-s` / `-e`），不递归、不做编辑距离或相似度比较；只处理纯 ASCII 单词，中文与含空格/连字符的多词条目只走子串匹配。两条护栏：词长 < 5 不归一（挡 `act`/`action`、`use`/`using`），剥出的词干 < 6 则放弃该规则（挡 `question`→`quest`、`version`→`vers`）。命中记的仍是**词表条目**，`route_warnings` 格式与"按词调优"的方式不变。

**可配置**（新增，追加而非替换）：`heuristics.promote_keywords` / `heuristics.promote_keywords_combo` / `heuristics.disabled`。使新增术语无需发版。

**误报控制**：每次提升把命中的词写进 `route_warnings`（形如 `difficulty_promoted_by_keyword:migration`），使误报可观测、可回溯、可按词调优。

**词表来源建议**：从批次账本里真实的中文 `difficulty_rationale` 反推词表，而不是凭空构造——§3.4 已证明该字段有充足的中文语料。

**修订（2026-09-22 v2）：关键词启发式收编为 `task_type` 查表的可选后手**（用户 5 项拍板，来源见 §2 的 `plan.md`）。

1. **分类职责上移**：`spawn_subagents` / `spawn_agent` / `spawn_team` 任务与主 Agent `predict_task_difficulty` 均新增可选字段 `task_type`（封闭枚举）+ `task_subject`（短说明，只进审计不进映射）。harness 不再对 `goal` 做子串猜测，而是直接消费 LLM 的结构化声明——本节词表从「主判据」降级为「可选后手」。
2. **枚举与底档**（12 类，`taskTypeFloor` 封闭 map + `Validate` 门禁；未知值 → `task_type_unknown:<v>` warning 且档位不变）：

| task_type | floor | 说明 |
| --- | --- | --- |
| `explore` | easy | 只读探查；任意类 → `explore` 属跨类降档 |
| `understand` | normal | 理解/解释 |
| `modify` / `test` / `config` | normal | 局部写 |
| `implement` / `refactor` / `integration` | hard | 结构性写；`implement` 承接原 `role=writer&&!readonly` 的写语义 |
| `verify` | normal | 只读复核；**对齐既有 `role=verifier` 底**，不抬到 hard |
| `migrate` / `security` | hard（`allow_expert` 时可 expert） | 对齐既有高信号词 floor=hard；**不默认 expert**（`allow_expert=false` 是默认，expert floor 会与之冲突） |
| `generate` | easy | 低风险写；相对原 writer 抬到 normal 的**有意放宽** |

3. **`role` 的替换边界**：被替换的是**路由层** `role`（`promotedDifficulty` 的 verifier/writer 角色底 + `routeProfileForTask` 的 `role_override` 查表）；**编排层** `role`（`SubagentTask.Role` 的 writer→verifier 拓扑、强制只读、`only one writer`、`requires hard-or-higher verifier`）**保留不动**——它是结构轴不是风险轴。`task_type` 缺省时按别名推导隐式 `task_type`：`verifier→verify`、`writer&&!readonly→implement`、`researcher→explore`，其余不推导（回落 level default）；配置 `routing.roles.<role>` 映射为 `routing.task_types.<task_type>` 并给 deprecation warning。
4. **关键词的去留**：`heuristics.promote_keywords*` 保留为可选后手，默认关（`heuristics.disabled` 语义沿用），观测一个 release 后移除；`floor(task_type)` 为长期主判据。新增 warnings：`difficulty_floor_by_task_type:<t>` / `difficulty_downgraded_by_task_type:<t>` / `task_type_unknown:<v>`（口径对齐既有 `difficulty_promoted_by_keyword:*`）。
5. **同步范围（决策 4）**：`spawn_team` teammate 任务（`team_tasks` 表增列、派发事件、teammate runner）与批次账本 `subagent_tasks`（结构体 + 增列）一并透传 `task_type`/`task_subject`，避免「子路径有、team 路径无」的审计缺口（G1 同款教训）。
6. **观测与两个前端（决策 5）**：`usage_routes` 增 `task_type`/`task_subject` 列，聚合新增 `by_task_type`（`by_role` 保留一个 release）；micro web client（`backend/cmd/aicli/commands/web/js/analysis.js`）与 React `frontend/`（`routing-observability-panel.tsx`、`subagent-stats-panel.tsx`、i18n、路由配置编辑器 `roles→task_types`）同步展示。
7. **跨类降档（决策 3）**：定义在姊妹方案 §5.5（v4）——同档降级仍需 `downgrade_confirm_steps`，跨类降档收敛为 1 步确认，`min_dwell_steps` 一律不绕过；本文件只消费其结果（子 Agent 路径每任务只解析一次，无迟滞状态机）。

### 5.5 G5：别名键冲突门禁

**改动 1（校验期）**：`validate.go` 在 `Levels` 与 `Roles` 两处构建 `归一结果 → 原始键` 映射，撞车时分级处理：

| 情况 | 处理 |
| --- | --- |
| 两个原始键归一后相同，且两个 profile **完全一致** | 允许，记 warning（无行为风险） |
| 两个原始键归一后相同，profile **不一致** | **返回 error**，消息含两个原始键与归一结果，并给出修复建议 |

**改动 2（解析期兜底）**：`routeProfileFromMap` 改为**排序后**扫描键，消除对 Go map 迭代顺序的依赖。即使校验被绕过（如代码路径未走 `Validate`），结果也是确定性的。

**改动 3（迁移辅助）**：错误消息与 `aicli doctor subagent-route` 预检输出一致的诊断文本，避免用户拿到一个启动期报错却不知道改哪里。

### 5.6 G6：expert 并发上限语义

分两步，避免直接打断现有配置：

**P1（兼容，零行为变化）**
1. `0` 仍等于不限流，但启动时输出一次 warning（"max_expert_concurrency=0 表示不限流；如需限制请设为正数，如需显式不限请用 -1"）。
2. 接受 `-1` 作为"显式不限"的同义词（今天只有 `<=0` 这一条含混语义）。
3. 路由审计新增 `expert_limit: unlimited | <N>`，使"限流是否生效"永远可见。

**P2（收紧，需 release note）**
1. `0` 改为配置错误，提示使用 `-1` 或省略。
2. `-1` 成为唯一显式不限写法。

**同时补齐文档**：闸门获取失败时的行为（排队等待 vs 直接拒绝）需在用户文档中写明——这决定了 expert 任务在拥塞时是变慢还是变失败。

### 5.7 G7：路由审计进入 tool result

**改动**：`spawn_subagents` / `spawn_agent` 的 tool result 增加紧凑回执行，每任务一行：

```text
route: arch-core · hard(explicit) · ds2api/deepseek-v4-flash · effort=max
route: doc-plan  · normal(explicit_promoted) · hanhe/deepseek-v4-flash · warnings=1
```

**边界约束**（沿用大输出治理的既有口径）：
- 最多 8 行，超出折叠为 `+k more`；
- 回执总字节上限 1 KB；
- 只在 `route_*` 解析成功时输出，未路由时明确写 `route: <id> · <difficulty>(<source>) · unrouted`；
- v4 修订：回执行可附 `task_type`（如 `route: <id> · hard(explicit) · explore · provider/model`），截断口径与 G1 载荷一致。

**异步模式**：批次 start ack 给出同一回执行；完成态沿用同一格式（不重复展开）。

**先例**：`subagent-readonly-boundary-transparency-plan-20260917.md` 用同一手法把只读边界回执塞进 tool result，已证明可行且不破坏 schema 稳定性。

---

## 6. 改动点清单

> 行号会漂移，**按符号名定位**。实施状态：**改动点 1–7（P0）已实施**（2026-09-22，含 3 处落点偏离，见 §6.1.1）；改动点 8–18（P1/P2）未开始。

### 6.1 审计完整性（P0）

| # | 文件 | 符号 / 位置 | 动作 | 缺口 |
| --- | --- | --- | --- | --- |
| 1 | `backend/internal/events/contract.go` | `runtimeEventContracts` D 通道段落 | 新增 4 条 batch 失败态登记；新增 `subagent.route.resolved` 登记 | G1 G2 |
| 2 | `backend/internal/supervision/subagent_progress.go`（或同包新文件） | `EventTypeSubagentProgress` 邻位 | 新增 `EventTypeSubagentRouteResolved` 与 4 个 batch 终态常量 | G1 G2 |
| 3 | `backend/internal/agent/scheduler.go` | `runChildUncontracted` 中 `emitRuntimeEvent("subagent.started", childSessionID, ...)` 邻位 | 新增 `emitRuntimeEvent(EventTypeSubagentRouteResolved, options.ParentSessionID, ...)`，载荷经 `mergeRouteAuditPayload` + 截断 | G1 |
| 4 | `backend/internal/agent/subagent_batch_coordinator.go` | `terminalNotificationFromBatch` | 4 个返回字面量改为 `supervision` 常量 | G2 |
| 5 | `backend/internal/events/contract_test.go` | `TestExternalEventFamilyConstantsAreRegistered` 门禁表 | 追加 5 个新常量的注册与通道断言 | G1 G2 |
| 6 | `backend/internal/events/contract_test.go` | 新增测试 | 扫描 `internal/agent` / `api/skills` / `toolbroker` 的 `emitRuntimeEvent("<literal>"` 字面量，断言已登记 | G2 |
| 7 | `backend/internal/runtimeobserve/known_types.go` | `buildKnownEventTypes` | 追加新类型（双向一致门禁会强制） | G1 G2 |

#### 6.1.1 实施记录（2026-09-22，P0 已完成）

逐项状态与实际落点（**按符号名定位**）：

| # | 状态 | 实际落点 |
| --- | --- | --- |
| 1 | 已实施 | `backend/internal/events/contract.go`：5 条新登记（`subagent.route.resolved` + 4 个批次失败态），通道 = A+D（`ChannelSessionStore` + `ChannelTailOnly`）；**另加 20 条 0 通道登记**（见偏离 ②） |
| 2 | 已实施（**落点偏离**） | 常量放在 `backend/internal/events/subagent_audit_events.go`（同包），**不是** `internal/supervision/subagent_progress.go`（见偏离 ①） |
| 3 | 已实施 | 新建 `backend/internal/agent/subagent_route_audit.go`（载荷组装 + 字符截断 + 整行字节收敛）；`scheduler.go` 在 `loop.run` 前发射，归属 `options.ParentSessionID`，每次 attempt 一行并带 `attempt` / `max_attempts` |
| 4 | 已实施 | `subagent_batch_coordinator.go`：`terminalNotificationFromBatch` / `batchTerminalEventType` 的返回字面量改为常量；`runTasksWithProgress` 传 `BatchID: batchID` |
| 5 | 已实施 | `contract_test.go` 新增 `TestSubagentAuditEventChannelRegistrations`：常量 ↔ 注册表 ↔ `known_types.go` 目录三方一致 |
| 6 | 已实施 | `contract_test.go` 新增 `TestEmitRuntimeEventLiteralsAreRegistered`：AST 扫描 `internal/agent`、`internal/api/skills`、`internal/toolbroker` 的 `emitRuntimeEvent("<literal>")` 调用 |
| 7 | 已实施 | `known_types.go`：新增「来源 9」段落 + 25 条目录条目（5 条新审计事件 + 20 条门禁收编的历史字面量） |
| 追加 | 已实施 | 新增 `internal/agent/subagent_route_audit_test.go`：4 个单测锁定 §5.1 载荷契约（A1 的单元级证据） |

**三处落点偏离（与 §6.1 原表的差异，理由均已写入代码注释）**：

① **改动点 2 落点改到 `internal/events`**。原文建议 `internal/supervision/subagent_progress.go`；改放 `events` 包是为了避免 `agent → supervision` 新增生产依赖（`agent` 本已依赖 `events`），并与姊妹方案 `internal/events/main_agent_routing.go` 的常量落点同构。常量命名与语义（`EventSubagentRouteResolved` 等 5 个）与原方案一致。

② **改动点 6 的门禁强制扩面**。扫描门禁落地后立即暴露 **20 个已在生产发射、但既不在交付注册表也不在已知目录**的裸字面量类型（`context.preflight.*`、`llm.*`、`patch.*`、`tool_loop.*`、`subagent.batch.created` / `.circuit_open` / `subagent.denied` / `subagent.requires_write` 等）。不登记则门禁无法通过。故按「只表态、不改行为」原则把它们登记为 **0 通道**，并同步收编进 `known_types.go` 目录——否则三分法无法把「已知但当前无 chat 通道」与「完全未知」区分开。**这是本次唯一超出原方案改动面的部分，且零投递行为变化**（0 通道 = 不新增任何 SSE 交付）。

③ **改动点 3 附加 `BatchID`**。为让批次内可反查，`SubagentRunOptions` 新增 `BatchID` 字段；sync 路径由 `loop.go` 传 `syncBatchID`（`loop.go` 同时是姊妹方案的在改文件，本处为单行改动、无冲突）。

**验证证据（2026-09-22 实测；工作树含姊妹方案并发改动）**：

| 命令 | 结果 |
| --- | --- |
| `go build ./...` | exit 0 |
| `go test ./internal/events/... ./internal/runtimeobserve/...` | ok（events 2.110s；runtimeobserve ok） |
| `go test ./internal/api/skills/... ./internal/toolbroker/...` | ok（24.567s / 12.350s） |
| `go test ./internal/agent/...` | ok（12.773s） |
| `go test ./internal/agent/ -run 'SubagentRouteAudit\|SubagentRouteResolved' -v` | 4/4 PASS |
| `gofmt -l`（本次全部改动文件） | 干净 |

**未完成项**：A1 / A2 的**端到端**取证（真实批次运行 + 事件库/渲染日志查询，见 §8.3）尚未执行——单元与契约门禁已就绪，但「受控批次跑完后父会话事件流中确实出现 `subagent.route.resolved`」「取消/超时批次确实留下终态行」仍需一次真实运行确认。

### 6.2 安全网（P1）

| # | 文件 | 符号 | 动作 | 缺口 |
| --- | --- | --- | --- | --- |
| 8 | `backend/internal/modelrouting/resolver.go` | `resolveDifficulty` | 显式分支不再提前返回；按开关执行提升并取 rank 最大值；新增 `explicit_promoted` 来源 | G3 |
| 9 | `backend/internal/modelrouting/resolver.go` | `promotedDifficulty` | 关键词表扩为高信号 + 弱信号两档，含中文；命中词写入 warnings | G4 |
| 10 | `backend/internal/agentconfig`（subagent routing 配置结构） | `AICLISubagentRoutingConfig` | 新增 `promote_explicit_difficulty`（三态）、`heuristics.*` | G3 G4 |
| 11 | `backend/internal/modelrouting/validate.go` | `Validate` | 校验三态取值；校验 heuristics 词表非空与去重 | G3 G4 |
| 12 | `backend/internal/toolbroker/broker.go`（`spawn_subagents` / `spawn_agent` 结果渲染） | 工具结果拼装处 | 追加路由回执行（≤8 行 / ≤1 KB） | G7 |
| 13 | `backend/internal/agent/planner.go` 或 prompt 渲染 | 难度指引 | 提示词补充"显式声明不再是免检通道"的说明（与 G3 语义对齐） | G3 |

### 6.3 配置陷阱（P2）

| # | 文件 | 符号 | 动作 | 缺口 |
| --- | --- | --- | --- | --- |
| 14 | `backend/internal/modelrouting/validate.go` | `Validate` 的 `Levels` / `Roles` 循环 | 归一撞车检测：profile 相同→warning，不同→error | G5 |
| 15 | `backend/internal/modelrouting/resolver.go` | `routeProfileFromMap` | 排序后扫描键，消除 map 迭代顺序依赖 | G5 |
| 16 | `backend/internal/modelrouting/validate.go` + `scheduler.go` expert 闸门 | `MaxExpertConcurrency` 校验与 semaphore 构造 | P1：`0` 告警 + 支持 `-1`；P2：`0` 改报错 | G6 |
| 17 | 路由审计载荷 | `mergeRouteAuditPayload` 邻位 | 新增 `expert_limit` 字段 | G6 |
| 18 | `aicli doctor subagent-route` | 诊断命令 | 输出别名冲突与 expert 限流语义的预检结论 | G5 G6 |

### 6.4 task_type 收编（P4，2026-09-22 新增，**已实施 2026-09-22**）

对应 §5.4 修订块。缺口列标注收编来源（`G3 G4`）与审计同步（`G1`）。完整分步与验证口径见仓库根 `plan.md` §6–§9。

| # | 文件 | 符号 | 动作 | 缺口 |
| --- | --- | --- | --- | --- |
| 19 | `backend/internal/modelrouting/types.go` | `TaskHint` | 新增可选 `TaskType`/`TaskSubject`；`Role` 保留为兼容别名 | G3 G4 |
| 20 | `backend/internal/modelrouting/resolver.go` | `promotedDifficulty` / `routeProfileForTask` | `floor(task_type)` 并入 rank-max；查表改 `cfg.TaskTypes`；`role` 别名推导（§5.4 修订块第 3 条） | G3 G4 |
| 21 | `backend/internal/modelrouting/validate.go` + `agentconfig/config.go` | `ValidateTaskType` / `roles→task_types` 别名 | 未知 `task_type` → warning；配置键别名 + deprecation | G4 G5 |
| 22 | `backend/internal/modelrouting/types.go` + `config.yaml` | `heuristics.*` | 关键词降级为可选后手，默认关 | G4 |
| 23 | `agent/loop.go` + `toolbroker/broker.go` + `spawn_*_arg_types.go` | `spawn_subagents` / `spawn_agent` / `spawn_team` schema | 新增 `task_type`/`task_subject`；`role`/`agent_type` 标注 deprecated | G3 |
| 24 | `agent/scheduler.go` / `subagent_route_audit.go` / `child_factory.go` / `planner.go` | 子任务结构体 + 审计 payload + planner 契约 + 工具回执 | 透传并写入 `task_type`/`task_subject`（截断） | G1 G7 |
| 25 | `agent/subagent_batch_coordinator.go` + 批次账本 schema | `subagent_tasks` | 结构体与表增 `task_type`/`task_subject` 列 | G1 |
| 26 | `internal/team/*`（types / lead_planner / run_meta / task_dispatch_event / sqlite_store / teammate_runner） | `team_tasks` + 派发事件 | spawn_team 任务透传 + 增列 | G1 |
| 27 | `backend/pkg/skillsapi/client.go` | `PlanningSubagentTask` | `task_type`/`task_subject` 透传 | G1 |
| 28 | `usageanalytics`（ingest / schema / contracts） | `usage_routes` + `RouteStats` | 增列 + `by_task_type` 聚合（`by_role` 保留一个 release） | G1 |
| 29 | micro web client `analysis.js` + React `frontend/` | 分布卡 / 事件行 / 统计表 / i18n / 路由配置编辑器 | 展示 `task_type`/`task_subject`；配置编辑器 `roles→task_types` | G1 |

---

## 7. 实施阶段

四个阶段**各自独立可发布**，P0 与 P1 均不改变任何既有的模型选择行为。

### P0：审计完整性（零行为变化）

| 项 | 内容 |
| --- | --- |
| 范围 | 改动点 1-7 |
| 状态 | **已完成（2026-09-22）**：改动点 1–7 全部落地并通过门禁，见 §6.1.1 |
| 交付物 | `subagent.route.resolved` 落盘；4 个批次失败态登记；门禁扩展 |
| 行为变化 | **无**。只新增事件与登记，不改任何路由决策与投递顺序 |
| 验收 | A1、A2、A3 |
| 回滚 | 移除新登记与发射点即可；已落盘的事件行不影响回放（未知类型按既有三分法处理） |

### P1：安全网（`warn` 模式）

| 项 | 内容 |
| --- | --- |
| 范围 | 改动点 8-13（G3 走 `warn`，G4 词表生效但仅告警） |
| 交付物 | 提升命中率可观测；中文关键词进入告警；工具结果回执 |
| 行为变化 | 仅新增 `route_warnings` 与 tool result 文本；**档位不变** |
| 验收 | A4、A5、A6 |
| 回滚 | 开关置 `off` |
| 状态 | **已完成（2026-09-22）**：改动点 8–13 全部落地。按用户指令与 P2 合并实施——G3 默认即 `enforce`（不再先只发告警），`warn` / `off` 保留为回滚开关；新增 `promotion_test.go`（15 例）锁定三态、单调性、弱信号阈值、词表追加与 `heuristics.disabled` 边界。门禁：`go build ./...`、`go vet ./internal/... ./cmd/...`、`go test ./internal/modelrouting/... ./internal/agentconfig/... ./internal/agent/... ./internal/toolbroker/... ./internal/usageanalytics/...` 全绿。 |

### P2：收紧

| 项 | 内容 |
| --- | --- |
| 范围 | 改动点 14-18 + G3 切 `enforce` + G6-P2 |
| 交付物 | 别名冲突报错；expert 限流语义确定；显式声明不再免检 |
| 行为变化 | **有**：部分任务升档（成本上升）、部分历史配置启动报错。必须写 release note |
| 验收 | A7、A8、A9 |
| 回滚 | 配置层可回退（`promote_explicit_difficulty: off`）；别名冲突报错需修配置 |
| 状态 | **已完成（2026-09-22）**：14（同 profile 撞车→warning、不同 profile→error）、15（`routeProfileFromMap` 排序扫描）、17（审计载荷 `expert_limit`）、18（doctor 预检输出 `promote_explicit_difficulty` / `expert_limit` / `config_warnings`）落地；G3 已切 `enforce`。16 见下方偏差说明。 |

**G6-P2 偏差（有意为之，需进 release note）**：计划要求 `max_expert_concurrency: 0` 变为配置错误，但校验函数只拿到结构体，而 `0` 与「键省略」在 Go 里不可区分（零值）——直接报错会让所有未显式声明该键的存量配置启动失败，代价远大于收益。因此收紧落在**写入路径**（presence 已知处）：Web 编辑器现在可以表达 `-1`（此前 `parseNonNegativeInteger` 会把用户输入的 `-1` 静默改成 `0`，即「想限流却写成不限」），保存时把 `0` 与非法输入统一归一为 `-1`；加载路径维持 `0` = 不限流 + 启动 warning（`max_expert_concurrency_zero_means_unlimited`）。文档、i18n、审计字段 `expert_limit` 三处语义已一致（A9）。

**release note**：`max_expert_concurrency` 的推荐写法是 `-1`（显式不限）或正数；`0` 仍被接受但启动时会告警，从 Web 保存时自动写成 `-1`。

**release note（词形归一）**：英文词表现在会命中同词干的词尾变体（`migrate` / `migrating` / `migrated` 命中 `migration`），因此**可能出现此前不会发生的升档**；护栏为词长 ≥5 且词干 ≥6。要完全回到旧行为：`promote_explicit_difficulty: off`（整体回退），或 `heuristics.disabled: true`（只关词表、保留角色提升）。`route_warnings` 里记的仍是词表条目，按词调优方式不变。

### P3：可选增强

批次终态载荷加 `route_digest`；把路由审计字段纳入子代理可靠性统计口径（`session-analytics-subagent-reliability-implementation-plan.md`）。

### P4：task_type 收编（2026-09-22 新增，**已实施 2026-09-22**）

| 项 | 内容 |
| --- | --- |
| 范围 | §6.4 改动点 19–29；分五步（字段透传 → floor 查表 → 类别感知降档 → 观测/界面 → 移除词表），见 `plan.md` §8 |
| 交付物 | `task_type`/`task_subject` 贯通子 Agent、spawn_team、批次账本；关键词降级为后手；`by_task_type` 与两个前端 |
| 行为变化 | Step 1 **零**（字段可选，`off` 下逐字节不变）；Step 2 起升档走三态开关；Step 5 移除词表 |
| 验收 | A10–A13（§10） |
| 回滚 | `task_type` 缺省 + `heuristics.disabled: false`（重开词表）即回退到关键词路径；路由层 `role` 别名一个 release 内可回退 |
| 状态 | **已完成（2026-09-22）**：改动点 19–29 全部落地（Step 1–4；Step 5「满一个 release 移除词表」按计划延后）。门禁：`go build ./...` + 10 包 `go test` 全绿（modelrouting/agentconfig/agent/toolbroker/team/agentcontrol/api/skills/subagentbatch/skillsapi/usageanalytics），`node --check analysis.js` + Go 侧 Analysis 断言绿，React 侧 `tsc -b` + 定向 vitest（34 用例）+ i18n lint 绿。落点修正：`team_tasks` 表已在此前 V19 迁移中删除，A13 所述 team 落点实际为 `agent_control_task_records`（新增迁移 V25 `agent_control_task_type_metadata`）；`subagent_tasks` 走 `internal/migrate` 追加迁移 v2；`usage_routes` 增量列记 v6、`usage_subagents` 记 v7（均沿用列存在性探测，只读旧库退化不改写） |

---

## 8. 测试计划

### 8.1 单元测试

| 目标 | 用例 |
| --- | --- |
| G3 显式不再短路 | 表驱动：`(difficulty, role, read_only, goal) → 期望档位与 source`，至少覆盖 §5.3 表格的三行，且 `off` 模式下结果与今天逐字一致 |
| G3 单调性 | 显式 `expert` + 命中关键词 → 仍为 `expert`（提升永不降级） |
| G4 中文关键词 | `goal` 含 `迁移` / `权限` / `跨系统` → 升 hard；含单个弱信号词 → 不升；含两个弱信号词 → 升 |
| G4 可配置 | 追加自定义词生效；`heuristics.disabled` 关闭全部提升 |
| G5 别名冲突 | `normal`+`medium` 同 profile → 通过并告警；不同 profile → 报错且消息含两个原始键 |
| G5 确定性 | `routeProfileFromMap` 对同一 map 多次调用（含不同插入顺序）结果一致 |
| G6 限流语义 | `0` / `-1` / 正数三态下的 semaphore 构造与告警输出；审计字段 `expert_limit` 取值正确 |
| G1 载荷约束 | `goal` / `difficulty_rationale` 超长时按 256 字符截断；整行 ≤ 2 KB |
| task_type floor（P4） | 表驱动：`(difficulty, task_type) → 期望档位与 warnings`；`verify→normal`、`migrate/security→hard`、未知 `task_type` → `task_type_unknown:<v>` 且档位不变；`off` 模式与今天逐字一致 |
| role 别名推导（P4） | `task_type` 缺省 + `role=verifier/writer/researcher` → 隐式 `task_type`，档位与 v3 逐字一致；编排层 `role`（writer/verifier 约束）行为不变 |
| 配置别名（P4） | `routing.roles.<role>` 读入映射 `task_types` + deprecation warning；显式 `task_types` 优先 |
| 账本 / team 同步（P4） | `subagent_tasks` 与 `team_tasks` 均落 `task_type`/`task_subject`；缺省为空与既有行/旧库迁移兼容 |

### 8.2 契约测试

| 目标 | 用例 |
| --- | --- |
| 新类型登记 | 5 个新常量在 `contract_test.go` 门禁表中断言已登记且通道符合预期 |
| 双向一致 | `known_types.go` 与注册表双向一致门禁通过 |
| 字面量扫描 | 扫描型测试对既有全部 `emitRuntimeEvent("<literal>"` 调用通过（含本次改造后的 coordinator） |

**P0 落地情况（2026-09-22）**：上表三项门禁均已实现并通过（`TestSubagentAuditEventChannelRegistrations`、`TestEmitRuntimeEventLiteralsAreRegistered`）。需注意扫描门禁当前断言的是「三个包的**字面量发射点均已登记**」（其中 20 个历史类型按 0 通道登记），而非「字面量已全部改为常量」——R5 里「以字面量改常量为主措施」在 P0 只对 coordinator 的 4 个失败态执行，其余 20 处保留字面量 + 登记（把字面量改常量列为 P3 可选增强）。

### 8.3 集成 / 端到端

| 目标 | 方法 |
| --- | --- |
| A1 受控复现 | 跑一个真实 `spawn_subagents` 批次，断言父会话事件流中出现 `subagent.route.resolved`，且 `route_model` 非空 |
| A2 失败态落盘 | 主动取消一个批次（或构造超时），断言 `subagent.batch.canceled` / `.timed_out` 出现在事件库中 |
| A3 回归 | `subagent.started` 的尾巴帧行为与今天逐字段一致；`subagent.completed` 载荷字段集合不减少 |
| A6 回执预算 | 3 / 8 / 20 任务三档下，tool result 回执行数 ≤ 8 行且总字节 ≤ 1 KB |

### 8.4 取证脚本固化

建议把 §3 的三组只读查询（事件库类型聚合、render 日志类型扫描、批次账本聚合）固化为一个诊断脚本（Python + `sqlite3` 只读打开），放在既有诊断工具链下。这样"审计是否完整"从一次性取证变成可重复验收——本次缺口正是靠这类查询才暴露出来的。

---

## 9. 风险与取舍

| # | 风险 | 影响 | 缓解 |
| --- | --- | --- | --- |
| R1 | G3 `enforce` 提高部分任务档位，成本上升 | 中 | 三态开关，先 `warn` 观测一个版本；命中率与成本可量化后再切 |
| R2 | 新增 A 通道事件导致事件库增长 | 低 | 载荷 ≤ 2 KB；每 attempt 一行；上线后监控行数与字节增速（参照 §3.2 的既有教训） |
| R3 | 批次失败态通道（A+D）与 started/completed（纯 D）不一致 | 低 | 已记录取舍理由；替代方案是全部 tail-only + 另发 A 通道摘要，若评审倾向一致性可切换 |
| R4 | 中文关键词误报（如"保持风格一致性"被升档） | 中 | 弱信号需组合命中；命中词写入 warnings 可回溯；可配置关闭 |
| R5 | 字面量扫描门禁脆弱（字符串匹配易误判） | 低 | 以"字面量改常量"为主措施，扫描测试为辅，且只匹配明确的 emit 调用形态 |
| R6 | 别名冲突报错打断现有部署 | 中 | profile 相同时只告警；`doctor` 预检；错误消息给出可直接照做的修复建议 |
| R7 | 765 个批次库文件中 4 个打开失败 | 未知 | 本方案不据此下结论；实施时顺带复核是空文件还是损坏（与既有 sqlite 损坏观察一并处理） |
| R8 | 事件归属从子会话改为父会话引发下游误判 | 中 | 本方案不改既有事件的归属，只新增事件；父/子会话 id 同时写入载荷，下游可自行判别 |
| R9 | `task_type` 误分类（LLM 把 `migrate` 报成 `modify`）导致底档偏低 | 中 | 单调 rank 底 + 关键词后手可重开 + `task_type` 落审计可回看；`migrate/security` floor=hard 对齐既有词表强度 |
| R10 | 迁移期双分类轴（路由 `task_type` vs 编排 `role`）造成理解成本 | 低 | 文档明确两层职责（§5.4 修订块第 3 条）；路由层 `role` 只作别名，一个 release 后移除 |

---

## 10. 验收标准

| 编号 | 标准 | 阶段 |
| --- | --- | --- |
| A1 | 受控批次跑完后，**父会话**事件流中存在 `subagent.route.resolved`，且携带非空 `route_model` / `route_provider` / `route_source` | P0 |
| A2 | 取消或超时的批次在事件库中留下终态行（`subagent.batch.canceled` / `.timed_out`），不再"发进虚空" | P0 |
| A3 | 既有 `subagent.started` / `subagent.completed` 的行为与载荷字段无回归（逐字段比对） | P0 |
| A4 | `warn` 模式下，显式低难度 + 高风险关键词的任务产生 `difficulty_promoted_over_explicit` 告警，且档位不变 | P1 |
| A5 | 中文 goal 命中高风险关键词（迁移 / 权限 / 跨系统等）时产生提升告警 | P1 |
| A6 | `spawn_subagents` 结果中出现路由回执行，≤8 行且 ≤1 KB | P1 |
| A7 | `enforce` 模式下，显式 `easy` + 高风险关键词的任务实际升档，且审计 `difficulty_source=explicit_promoted` | P2 |
| A8 | 同时配置 `normal:` 与 `medium:` 且 profile 不同时，启动期报错并指明两个原始键 | P2 |
| A9 | `max_expert_concurrency` 的 `0` / `-1` / 正数三态语义在文档、校验、审计字段三处一致 | P2 |
| A10 | `spawn_subagents`/`spawn_agent`/`spawn_team` 任务带 `task_type`/`task_subject` 时，路由审计与 `subagent.route.resolved` payload 均携带截断后的字段；缺省时行为与今天逐字一致 | P4 |
| A11 | `task_type=migrate`（缺省难度 easy）→ 升 hard 且 warning `difficulty_floor_by_task_type:migrate`；未知 `task_type` → `task_type_unknown:<v>` 且档位不变 | P4 |
| A12 | `usage_routes` 出现 `task_type`/`task_subject` 列且 `by_task_type` 聚合有数；micro web client 与 React 面板均能显示任务类型分布 | P4 |
| A13 | 批次账本 `subagent_tasks` 与 `team_tasks` 均落 `task_type`/`task_subject`；旧库缺列时迁移不破坏读写 | P4 |

**P0 验收状态（2026-09-22）**：

- **A1**：单元级证据已就绪（`TestBuildSubagentRouteResolvedPayload*` 断言 `route_model` / `route_provider` / `route_source` 非空且与决策一致）；**端到端取证待跑**（§8.3 的受控批次）。
- **A2**：4 个失败态已登记为 A+D 通道并改由常量发射（由 `TestSubagentAuditEventChannelRegistrations` 锁定）；**端到端取证待跑**（主动取消或构造超时后查事件库）。
- **A3**：无回归——`subagent.started` / `subagent.completed` 的发射点与载荷均未改动（本次只新增事件与登记），`internal/agent`、`internal/api/skills` 全包测试 ok。
- A4–A9（P1/P2）：实现已完成（见 §7 各阶段状态行与门禁记录）。
- **A10–A13（P4，2026-09-22 已验收）**：A10——三条委派路径（spawn_subagents/spawn_agent/spawn_team）schema、解析、审计载荷与回执均带 `task_type`/`task_subject`（`task_subject` 参与 256 字符截断与 2 KB 收敛；缺省路径由 modelrouting/agent/team/toolbroker 全包单测锁定与今日逐字一致）；A11——`TestResolveExplicitDifficulty_TaskTypeFloor`（migrate→hard + `difficulty_floor_by_task_type:migrate`）与 `task_type_unknown:bogus-class` 且档位不变用例绿；A12——`usage_routes` v6 迁移 + `by_task_type` 聚合用例、micro web（`node --check` + Go needle 断言）、React（`tsc -b` + `observability-panels.test.tsx` by_task_type/by_role 并存断言）全绿；A13——`subagent_tasks` 迁移 v2 旧库补列用例与 `agent_control_task_records` V25 迁移随 `internal/team`/`internal/subagentbatch` 全包测试绿（`team_tasks` 表已于 V19 删除，见 §7 P4 状态行的落点修正）。

**全局门禁**：`go test ./...`（`internal/events`、`internal/modelrouting`、`internal/agent`、`internal/toolbroker` 为重点包）+ 契约门禁 + §8.4 的取证脚本输出前后对比。

---

## 11. 一句话结论

难度**声明**已经有账，难度**路由决策**没有账；安全网**存在但可被绕过**，且在中文场景下基本失效。本方案用「一条新的 A 通道审计事件 + 四个失败态登记 + 门禁覆盖裸字面量」补齐审计，用「提升不再短路 + 中文关键词 + 三态开关」补齐安全网，全部改动可按 P0→P2 分阶段独立发布，P0/P1 零行为变化。

截至 2026-09-22：**P0/P1/P2 已实施并通过门禁**（§6.1.1、§7 各阶段状态行），「零行为变化」口径由字段可选与 `off` 开关延续到 P4；A1/A2 的端到端取证与 **P4（task_type 收编）** 待推进。

---

## 12. 修订记录（v2，2026-09-22：task_type 收编）

**触发**：用户对「用 LLM 结构化返回任务类别替代关键词防降档」的 5 项拍板（评审与全文见仓库根 `plan.md`）。

| # | 决策 | 本文件落点 |
| --- | --- | --- |
| 1 | `task_type` 替换**路由层** `role`，新增 `task_type`/`task_subject` | §5.3 修订注、§5.4 修订块 1–3、§6.4 改动点 19–23 |
| 2 | `task_subject` 与 `difficulty_rationale` 并存不合并 | §5.4 修订块 1 |
| 3 | 跨类降档保留**最小 1 步确认**（同档仍 N 步，`min_dwell` 不绕过） | 定义在姊妹方案 §5.5（v4）；本文件 §5.4 修订块 7 消费其结果 |
| 4 | 同步进 `spawn_team` 与批次账本 `subagent_tasks` | §5.4 修订块 5、§6.4 改动点 25–27 |
| 5 | 同步观测采集与两个前端 | §5.4 修订块 6、§6.4 改动点 28–29 |

**核实口径**（P4 实施后执行）：① 全文 grep `role_override` / `roles:` / `subject` 残留，确认落在「编排层 role / 兼容别名 / 保留一个 release」三类之一；② 对照 `plan.md` §5 枚举 floor（`verify=normal`、`migrate/security=hard` 且 `allow_expert` 才 expert）；③ 对照上表逐行确认 5 项决策均已落点。

**状态**：**P4（§6.4/§7）已实施（2026-09-22），核实口径三步均通过**——① 全仓 grep `role_override`/`by_role`/`subject` 残留全部落在「编排层 role / 兼容别名 / 保留一个 release」三类（modelrouting Roles 兜底、source 历史标签、orchestration 测试与 UI 兼容键）；② floor 表与 `plan.md` §5 一致（`verify=normal`、`migrate/security=hard`，expert 仅来自显式声明 + allow_expert 门禁，floor 永不产出 expert）；③ 5 项决策逐条落地（枚举与两字段、task_subject 不并入 rationale、跨类 1 步确认、spawn_team/账本同步、观测与两前端），证据见 §7 P4 状态行与 §10 A10–A13。P0–P2 的实施结论不受本轮修订影响（`task_type` 只增 rank-max 输入，不改已落地的三态开关、单调性、别名门禁与 expert 限流语义）。
