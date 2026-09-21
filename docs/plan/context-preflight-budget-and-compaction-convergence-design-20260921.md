# Context Preflight 预算与压缩职责收敛设计

- 日期：2026-09-21（v1.0 起草；v1.1 / v1.2 评审修订；v1.3 复核自纠）
- 状态：**已评审，待实施**（v1.3；硬前置见 §5 Step 0）
- 审查报告：`docs/plan/context-preflight-budget-and-compaction-convergence-design-review-20260921.md`（R1~R28）
- 证据快照：工作区 @ 2026-09-21，`HEAD=1813cc87`（较起草时的 `f882a012` 前进 25 个提交）+ 未提交改动（`git status --short` 44 行；`git diff --shortstat` = 40 files / +1853 / −256）。**行号仅对该快照有效**；实施与复核以"符号名 + 关键串"为准（R28：快照已漂移一次，实施前须再核）。
- 范围：`backend/internal/agent`（preflight 判定）、`backend/internal/historyguard`（active-turn 压缩）、`backend/internal/compactruntime`（会话级压缩）、`backend/internal/contextmgr`（历史构建）、`backend/internal/observability`（指标）、`backend/cmd/aicli`（事件消费）
- 关联文档：
  - `docs/plan/session-preflight-auto-compact-recovery-analysis.md`（现场根因分析）
  - `docs/plan/session-preflight-auto-compact-test-scripts.md`（验收脚本与事件检查清单）
  - `docs/plan/aicli-event-stream-rendering-order-unified-encoder-plan.md`（`context.preflight.*` 的呈现归属）

## 修订记录

### v1.0 → v1.1

| # | 修订 | 对应审查项 |
|---|---|---|
| 1 | 修正 §1.1 / §1.2 失锚行号；锚点改为符号名优先；文首记录证据快照 | R1 |
| 2 | §4.1 预算解析改为**硬上限 / 软阈值**分层：硬上限不可被任何配置突破 | R2 |
| 3 | 新增 P1-3（`started` 硬编码 scope）与 §4.2 双重压缩防护 | R13 / R6 |
| 4 | §4.4 观测口径对齐 `internal/observability` 约定，补告警阈值 | R4 |
| 5 | 新增 §4.5 幂等与配额、§4.6 成本与 prompt cache 影响 | R6 / R7 |
| 6 | §5 新增 Step 0（基线冻结）与每步门禁 / 验收判据 / abort 判据 / 回滚动作 | R3 / R5 |
| 7 | §7 补不变量测试、负例、性能与 schema golden 失败模式 | R8 |
| 8 | §9 未决问题全部裁决；§10 DoD 改为可断言口径 | R10 / R9 |
| 9 | 新增 §11 术语表、§12 决策记录、§13 契约 delta、§14 范围外跟踪项 | R11 / R12 |

### v1.1 → v1.2（方案问题修复）

| # | 修订 | 来源 |
|---|---|---|
| 10 | 新增 P0-5 与 §4.7：**预算单一来源**（contextmgr 构建预算 ↔ preflight 闸门预算同源） | 复核新发现 R14 |
| 11 | §4.2 scope 判定改为"可达性优先"，占比启发式降为诊断/兜底 | 自查：0.6 魔法阈值不应决定行为 |
| 12 | §4.3 补事件顺序与"恰好一次"语义 | 自查：事件契约缺时序 |
| 13 | §4.4 裁决：复用 `session_compact_completed` 回填恢复结果，不新增事件 | 自查：避免事件面扩散 |
| 14 | §6 补 kill switch 生效时延与回滚演练；§7 / §9 / §10 / §13 同步 | 自查：开关可回滚但未定义生效时延 |

### v1.2 → v1.3（复核自纠：证伪、可实施性与未登记变更）

| # | 修订 | 来源 |
|---|---|---|
| 15 | **证伪 P0-5**：构建期与闸门**已同源**（`loop.go:1646` → `manager.go:397-398` → `manager.go:432`），token 维度 `builder_stricter` 不可达；§4.7 由"收敛"改为"守护 + count 维度登记"；Step 5 删除 no-op 项 | R19 |
| 16 | §4.2 补 `historyguard` 需新增**导出 API**（`activeUserTurnStart` / `latestReplayBlockStart` 均未导出，agent 包不可见） | R20 |
| 17 | §4.4 修正：`failure_reason_code` 已随 `preflightErr.Metadata()` 流入 compact 事件，只需补 `preflight_scope`，不新增同义键 | R21 |
| 18 | §4.4 指标名改 `context_preflight_*`，避免与既有 `tool_preflight_total`（`metrics.go:61`）混淆 | R22 |
| 19 | §4.2 新增"执行落点"裁决：scope 判定留 preflight、执行留既有门控路径（`loop.go:786-845`），零新增状态；§4.5 配额落点同步 | R23 |
| 20 | §4.1 明确 `inputCeiling` 前置条件（现状 5746 要求两项均 > 0）与 `auto_compact_token_limit` **不叠加 margin**（避免未登记收紧） | R26 / R27 |
| 21 | §4.3 静默路径补指标可见性（`decision="silent"` + `budget_state="unresolved"`），消除观测黑洞 | R19 |
| 22 | §5 新增 §5.1 文件级改动清单；Step 5 登记死代码 `hasPromptPreflightContextBudgetOverride`（全仓无调用者）清理 | R23 / R25 |

---

## 1. 背景与现状事实

### 1.1 事件契约（当前实现）

`context.preflight.*` 不是"每次请求都发"的生命周期事件，而是"本地判定超预算时才进入"的分支事件（`enforcePromptPreflightWithTools`，快照 5113-5148）：

- 静默返回（不发任何事件）：`messages` 为空、`loop`/`llmRuntime` 为空、`budget.PromptBudget <= 0`（5114-5121）。
- `context.preflight.started` 仅在 `promptTokensBefore > inputBudget` 时发出（5163）。
- `context.preflight.compacted` 在 active-turn replay 压缩成功且放得下时发出（5228）。
- `context.preflight.failed` 只有三条路径（触发点 / 事件点）：
  - `tool_schema_exceeds_budget`：工具 schema 吃满预算，`messageBudget <= 0`（5169 / 5181）；
  - `prompt_still_exceeds_budget_after_compaction`：压过之后仍超（5234 / 5259）；
  - `active_turn_not_compactable`：本地压缩函数返回 `compacted=false`（5265 / 5288）。
- **P1-3 事实**：`started` 在压缩尝试之前硬编码 `startedPayload["active_turn_replay"] = true`（5162），与实际 scope 无关。

失败后外层还会尝试 session 级压缩恢复（调用点 807；`trySessionCompactionRecovery` 定义于 5292），成功则 `step--; continue` 重试；只有恢复也失败才以 `error_type=prompt_preflight` 终止。因此 `context.preflight.failed` 是**可恢复信号**，不是终态。

### 1.2 预算口径

分子（本地估算，非 provider usage）：

```
promptTokensBefore = estimatePromptMessageTokens(messages) + estimateToolDefinitionTokens(tools)   // 调用点 5126-5129；定义 4895 / 4884
```

分母：

```
inputBudget = budget.enforcedInputBudget() = min(PromptBudget, EffectiveInputBudget)   // 定义 4974-4979
```

`PromptBudget` 由 `addCandidate` 在 6 个候选来源中取最小值决定（`addCandidate` 5030-5043；来源见 `resolvePromptPreflightBudget` 5638-5781）：

| source | 取值 | 性质 |
|---|---|---|
| `context_max_prompt_tokens` | agent Options 显式配置 | 用户意图 |
| `model_capability_auto_compact_token_limit` | 模型能力 `auto_compact_token_limit`（clamp 到 `max_context_tokens`） | 推导软阈值 |
| `model_capability_context_ratio` | `floor(max_context_tokens × 0.85)`（`defaultPromptPreflightAutoCompactRatio`，48） | 推导软阈值 |
| `provider_context_limit_default_ratio` | `floor(provider 窗口 × 0.85)` | 推导软阈值 |
| `default_context_fallback_max_prompt_tokens` | 兜底 `contextmgr.DefaultFallbackMaxPromptTokens = 32000`（`contextmgr/manager.go:28`） | 兜底软阈值 |
| `remaining_budget` | run 级 token 预算剩余（`--budget-tokens` / `TurnBudgetTokens`） | **硬上限** |

`EffectiveInputBudget` 还会被"窗口 − 预留输出"压低（5745-5765）：

```
reservedOutput = resolveLoopMaxTokens(DefaultMaxTokens, remainingBudget)      // 5891-5897；调用点 5641
inputCeiling   = hardWindow - reservedOutput
```

**关键事实**：现有候选集合把"用户意图 / 推导软阈值 / 硬上限"三类语义混在同一个 `min()` 里（P0-1、P0-2 的根因），这是 v1.1 §4.1 分层设计的依据。

**构建期同源事实（v1.3 新增，R19）**：构建期预算与闸门预算**已经是同一来源**——`loop.go:1631` 的 `resolveContextBuildPromptBudget` 即 `resolvePromptPreflightBudget(..., remainingBudget=0)`（5634-5636），其结果经 `BuildInput.PromptBudget`（1646）传入，并在 `contextmgr/manager.go:397-398` 覆盖 profile 值；`manager.go:432` 的层规格同样使用覆盖后的 budget。二者唯一差异是闸门额外叠加 `remainingBudget`（只能更严）。既有测试（`loop_test.go:2839-2855`：`budget_max_prompt_tokens=900000` 且 `budget_profile_max_prompt_tokens=12000`）守护该行为。

### 1.3 压缩职责现状（三套并存）

| 引擎 | 入口 | 触发者 | 作用范围 |
|---|---|---|---|
| contextmgr | `Manager.Build`（`contextmgr/manager.go:387`） | `think()` 的上下文构建 | 历史裁剪 / 摘要（受 `MaxPromptTokens`、`MaxMessages`、`KeepRecentMessages` 约束） |
| compactruntime | `Runtime.MaybeCompact`（`compactruntime/runtime.go:120`，本地/远程适配器） | run loop 的恢复路径、`/compact` | 会话级替换 |
| historyguard | `CompactActiveTurnReplayWithCounter`（`historyguard/active_turn.go:24`） | preflight | 仅 active turn 的 replay 段与 latest replay block |

`historyguard` 的进入条件包含 `overTotalTokens`（整 prompt 超 `messageBudget`，`active_turn.go:43-51`），但两个手段都只作用于 active turn：active turn 之外主导超限时必然返回 `false`（`active_turn.go:104-111`）——这是 P0-3 的机制性根因。

### 1.4 既有恢复与去重机制（v1.1 新增，供 §4.2 / §4.5 复用）

| 机制 | 位置 | 作用 |
|---|---|---|
| step 门控 | `loop.go:800`（`sessionCompactionRecoveryStep != step`） | 同一 step 只允许一次 session 恢复 |
| 输入指纹去重 | `markSessionCompactionRecoveryInput`（4842）+ `sessionCompactionRecoveryInputs`（799-807） | 同一输入不重复压缩 |
| 进展判据 | `compactionRecoveryMadeProgress`（4853-4866） | 压缩后必须"消息数变少或估算 token 变小"，否则不算进展 |
| 观测约定 | `observability/metrics.go:11-67`（`MetricXxxTotal`）、69-98（`LabelXxx`）、`RecordDoomLoop` / `RecordToolOutcome` / `RecordToolFailure` | 低基数标签 + 显式记录器；不按高基维度展开 |

---

## 2. 问题清单

### P0-1 预算候选隐式取 min，行为不可解释

6 个来源混在一个 `min()` 里，谁是决定性来源只能事后看 `budget_source` / `budget_candidates` 反推；更糟的是"用户意图 / 推导软阈值 / 硬上限"三类语义被同等对待，导致"显式配置被更小的推导值静默覆盖"这种反直觉行为。诊断字段的存在本身即不透明的证据。

### P0-2 同一语义有三种表达

`auto_compact_token_limit`、`max_context_tokens × 0.85`、fallback `32000` 表达的都是"提前量阈值"，但语义、优先级、默认值分散在三处；`hasResolvedPromptLimit` 的置位逻辑（5753-5755）还决定了 fallback 是否参与，进一步降低可预测性。

### P0-3 压缩范围与触发条件不匹配（必然失败的来源）

`CompactActiveTurnReplayWithCounter` 的进入条件包含 `overTotalTokens`（整 prompt 超 `messageBudget`，`active_turn.go:43-51`），但可用的两个手段都只作用于 active turn。当超限量由 active turn 之外（system prompt、旧历史）主导时：

- 压了仍超 → `prompt_still_exceeds_budget_after_compaction`；
- 无内容可压 → `active_turn_not_compactable`（`active_turn.go:104-111`）。

两者都是"判定进了只动 active turn 的路径，却注定救不回来"，随后还要再走一次 session compaction recovery，产生一轮无意义往返与噪声事件。

### P0-4 压缩职责重叠

三套引擎各自拥有触发判定、各自的替换语义、各自的事件。组合行为（谁先压、压完是否要换 prompt cache epoch、失败后谁兜底）难以预测，是复杂度的大头。

### P1-1 事件 payload schema 不一致

- `started` 用 `active_turn_replay`，`compacted` 用 `prompt_only`；
- `failed` 的 code 1 从 `startedPayload` 克隆（含 `message_tokens` 等），code 2/3 由 `budget.Metadata()` 重建（用 `prompt_tokens`）——同一事件类型键集不同。

消费方（UI timeline、日志解析、验收脚本）必须写分支，容易出现"字段缺失 = 看起来事件失效"。

### P1-2 观测闭环缺失

`failed` 发出后是否被恢复、由哪个 scope 恢复、恢复后是否再次超限，没有统一字段回填；验收只能靠人工比对 `session_compact_completed` 等相邻事件。

### P1-3 `started` 事件硬编码压缩范围（v1.1 新增）

`startedPayload["active_turn_replay"] = true` 在压缩尝试之前写入（5162），无论后续实际 scope 与是否可压。消费方据此认为"这是 active-turn 压缩"，与事实不符——这是"事件看起来失效/误导"的直接来源之一，与 P0-3 同源。

### P0-5 构建期与闸门的预算关系（v1.3 修正；v1.2 结论作废）

> **自纠（R19）**：v1.2 曾断言"未设置 `context_max_prompt_tokens` 时 contextmgr 走 budget profile（compact 8000）、preflight 走 fallback 链（≥32000），因此构建期更严、闸门永不触发"。**该断言与代码不符，予以作废**——它把一个已同源的链路误判为分叉，并据此设计了 no-op 的收敛步骤。

事实（工作区快照，逐条可核）：

| 环节 | 证据 | 结论 |
|---|---|---|
| 构建期预算 | `loop.go:1631` → `resolveContextBuildPromptBudget`（5634-5636）→ `resolvePromptPreflightBudget(..., 0)` | **与闸门同一函数**（闸门见 5118） |
| 传入构建器 | `loop.go:1646` `PromptBudget: contextBudget.PromptBudget` | 闸门预算进入构建器 |
| 构建器采用 | `contextmgr/manager.go:397-398`（`input.PromptBudget > 0` 时覆盖 profile 值） | profile 值被覆盖 |
| 层规格 | `contextmgr/manager.go:432` `ResolvedLayerPlan(..., budget, ...)`（覆盖后的 budget） | `Hot.MaxTokens` = 闸门预算 |
| 既有守护 | `loop_test.go:2839-2855`（`budget_max_prompt_tokens=900000`、`budget_profile_max_prompt_tokens=12000`） | 同源行为已被测试固化 |

唯一差异是 `remainingBudget`：闸门传入（5118），构建期固定传 0（5634-5636；理由见 `loop.go:1666-1668` 的 turn-fixed 工具面约束）。该差异**只能让闸门更严**，因此 token 维度关系恒为 `same` 或 `gate_stricter`，**`builder_stricter` 不可达**。

因此"事件不触发"的真实机制（按可观测性排序）：

1. **契约本身**：`context.preflight.*` 是分支事件（仅超预算时发），不是生命周期事件（§1.1）——"总是失效"的第一层是期望错配；
2. **构建期已裁剪到同一预算**：`promptTokensBefore <= inputBudget` → 静默返回（5146-5148）。同源意味着闸门是**兜底**，不是独立信号源；
3. **预算不可解析**：`budget.PromptBudget <= 0` → 静默返回（5119-5121）且**不发任何事件**——真正的观测黑洞（配置缺失无法与"未超预算"区分），由 §4.3 的指标维度补齐；
4. **scope 与触发条件不匹配**（P0-3 / P1-3）带来的失败噪声与误导字段。

**count 维度差异（latent，登记不改行为）**：`KeepRecentMessages` / `MaxMessages` 仍来自 profile（compact 5 / extended 12 / balanced 8，`manager.go:204-232`），**不被** `PromptBudget` 覆盖；它们受 `shouldApplyPromptCompactionPressure` 门控（`manager.go:451`），仅在 token 压力下生效，故当前是潜在差异而非活动差异。本设计不改其语义，仅在 §7 增加一条守护测试。

---

## 3. 设计目标与非目标

目标：

1. 单次请求输入闸门语义唯一、可解释、可配置：**硬上限不可突破，软阈值显式优先**；
2. 压缩职责单一入口，压缩范围显式（active-turn / session）且与触发条件匹配；
3. `context.preflight.*` 事件 schema 统一，消费方零分支；scope 由判定结果写入，不再预设；
4. 不降低现有恢复能力，不新增失败路径，保持 fail-fast 的可测试性；
5. 新增路径复用既有去重/门控/进展判据，不引入恢复风暴与双重压缩。

非目标：

- 不改 provider 协议层与 compact 请求形态；
- 不引入精确 tokenizer（tiktoken / provider count API）——见 §14 跟踪项；
- 不改 run 级 token 预算（`--budget-tokens`）语义；
- 不做工具 schema 裁剪（见 §14 跟踪项）。

设计不变量（实施与测试必须守护）：

| 编号 | 不变量 |
|---|---|
| I1 | `budget ≤ hardCap`，其中 `hardCap = min(inputCeiling, remainingBudget)`（可解析项）——任何配置都不能突破 |
| I2 | 单调性：显式配置设定后，软推导值变化不得使 `budget` 变小（除非触及硬上限） |
| I3 | 幂等：`resolveInputBudget` 对同一输入返回同一结果，无副作用 |
| I4 | 不存在"判定进入压缩路径但注定无法压缩"的代码路径 |
| I5 | 同一 step 内 provider compact ≤ 1 次；同一输入指纹不重复压缩 |
| I6 | 构建期裁剪预算与闸门预算**同源**（既有事实：同一 `resolvePromptPreflightBudget`，差异仅 `remainingBudget`）；token 维度关系恒为 `same` / `gate_stricter`，**`builder_stricter` 不可达**，由测试断言守护 |

---

## 4. 目标设计

### 4.1 统一输入预算解析（`resolveInputBudget`）

用单一函数替换现有 `resolvePromptPreflightBudget` 的候选拼装，并把三类语义显式分层：

```
// 1) 硬上限（不可突破，I1）
hardWindow     = minNonZero(providerContextLimit, capabilityMaxContextTokens)   // 解析不到 → 未知
reservedOutput = resolveLoopMaxTokens(DefaultMaxTokens, remainingBudget)
inputCeiling   = hardWindow - reservedOutput
                 // 前置条件（v1.3，R27）：仅当 hardWindow 与 reservedOutput 均 > 0 时成立；
                 // 否则为"未知"（现状 5746 亦要求两者 > 0，本设计不改变该条件）
hardCap        = minKnown(inputCeiling, remainingBudget)   // 两项都未知 → 无硬上限，标 degraded

// 2) 软阈值（按优先级取一个）
softCap =
    explicitCap                                     若 context_max_prompt_tokens 已设置
  | autoCap                                         若 capability auto_compact_token_limit > 0
                                                    （按现状 clamp 到窗口，**不叠加 margin**，见约定 6）
  | floor(inputCeiling × marginRatio)               若窗口可解析且无 autoCap（marginRatio 默认 0.85）
  | fallback(32000)                                 否则（标 degraded）

// 3) 合成
budget = minKnown(hardCap, softCap)
```

语义约定：

1. **硬上限优先于一切**：`context_max_prompt_tokens` 再大也不能突破 `inputCeiling` 与 `remainingBudget`；突破时不报错，取硬上限并标 `budget_conflict=true`（安全不变量 I1，替代 v1.0 的"显式永远生效"表述）。
2. **显式优先于软推导**：显式设置后，`auto_compact_token_limit` / `ratio` 不再作为竞争性最小值覆盖它；若软推导更小 → 标 `budget_conflict=true` 并列出候选，行为仍以显式为准（不变量 I2）。
3. **窗口不可解析时降级**：走 fallback（默认 32000），标 `budget_degraded=true` + `budget_source=default_context_fallback_max_prompt_tokens`，让"配置缺失"在观测上一眼可见，而不是伪装成正常预算。
4. **候选全量保留**：`budget_candidates` 继续输出所有候选值，并新增每个候选的处置（`used` / `clamped_by_hard_cap` / `shadowed_by_explicit` / `not_applicable`）。
5. **counter 注入点**：token 计数以函数参数注入（现状已是 `countPromptMessages`），未来替换精确 tokenizer 不影响本函数（§14 跟踪项）。
6. **保持既有推导行为（v1.3，R26）**：`auto_compact_token_limit` 存在时**不叠加** margin（现状 5697-5708 即如此，仅 clamp 到 `max_context_tokens`）。v1.2 写的 `min(autoCap, floor(inputCeiling × marginRatio))` 会对 autoCap 额外收紧 15%，属**未登记的行为变化**（更频繁压缩、更多 cache epoch break），本设计明确不做；如未来要做，必须走开关 + 风险登记。

### 4.2 压缩职责收敛

preflight 只保留三件事：**判定、发事件、选择压缩范围**；实际压缩统一经 scope 接口执行。

新增 `selectCompactionScope(messages, toolSchemaTokens, budget) -> (scope, reason, achievability)`：

```
messageBudget     = budget - toolSchemaTokens
若 messageBudget <= 0             → 无可用 scope（工具 schema 主导）

activeTurnTokens  = estimate(messages[activeUserTurnStart:])
historyTokens     = estimate(messages[:activeUserTurnStart])
activeReducible   = estimate(messages[userIndex+1 : preserveStart])   // historyguard 可摘要的 replay 段
activeResidual    = activeTurnTokens - activeReducible

// 可达性优先：由"哪个 scope 真能把输入压到预算内"决定行为，而不是占比阈值
scope = ScopeActiveTurn  若 historyTokens + activeResidual <= messageBudget
      | ScopeSession     若 historyTokens > 0 且 session 压缩可达
      | none             否则

dominance = activeTurnTokens / messageBudget   // 仅用于诊断与回归，不决定行为
```

**符号可达性（v1.3 新增，R20）**：上式依赖的 `activeUserTurnStart`、`latestReplayBlockStart`、`estimatedMessagesBytes` **都是 `historyguard` 的未导出函数**（`active_turn.go:32 / 53 / 133 / 141`）；该包当前导出面只有 `CompactActiveTurnReplay`、`CompactActiveTurnReplayWithCounter`、`HasActiveTurnCompactionSummary`（`active_turn.go:20 / 24 / 132`）。因此 `agent` 包**无法**自行计算 `activeTurnTokens` / `activeReducible` / `preserveStart`——Step 1 必须先在 `historyguard` 新增导出 API（建议 `ActiveTurnBounds(messages) (userIndex, preserveStart int, ok bool)`，内部复用既有未导出函数），否则 `selectCompactionScope` 不可实现。

判定口径说明（v1.2）：`dominanceRatio` 不再决定 scope。理由——占比高不等于"压得动"（active turn 可能全是不可摘要的 tool 结果），占比低也不等于"只能走 session"（active turn 可能刚好可摘要到预算内）。占比阈值降级为**兜底**：仅在估算不可用（如 `preserveStart` 无法定位）时启用，默认 0.6，并写入 `compaction.scope_reason` 说明走了兜底路径。

执行约定：

- `ScopeActiveTurn` → 调 `historyguard.CompactActiveTurnReplayWithCounter`（保持现有实现与 tool-call/tool-result 邻接保护）；
- `ScopeSession` → **不在 preflight 内执行压缩**（v1.3 裁决，见下"执行落点"），而是返回带 `preferred_scope="session"` 的 `PromptPreflightError`，由既有门控路径（`loop.go:786-845`）执行 session 恢复；**不再先跑一遍注定失败的 active-turn 压缩**；
- 两个 scope 都判定不可压时，才发 `context.preflight.failed`，code 显式化为 `no_compaction_scope_available`（保留 `active_turn_not_compactable` 作为别名一个版本）。

**执行落点与状态可达性（v1.3 新增，R23）**：既有 step 门控与指纹去重是 think 循环的**函数局部变量**（`loop.go:621-622`，使用点 800-806），而 `enforcePromptPreflightWithTools`（5113）是方法，**无法访问**这些局部量。因此 v1.2 的"在 preflight 内委派 session 压缩并复用门控"缺少落点。裁决：**判定留 preflight，执行留既有门控路径**：

| 项 | 落点 |
|---|---|
| 判定 | preflight 内 `selectCompactionScope`（纯函数） |
| 信号 | `PromptPreflightError` 新增 `PreferredScope` 字段，经 `Metadata()` 输出 `preflight_scope` |
| 执行 | 既有 `loop.go:806-807`（`markSessionCompactionRecoveryInput` + `trySessionCompactionRecovery`） |
| 门控 | 既有 800-806（step + 指纹），**零新增状态**；I5 由构造保证 |
| 进展判据 | 既有 810（`compactionRecoveryMadeProgress`） |
| 重试 | 既有 842-843（`step--; continue`） |

**双重压缩防护（v1.1 新增，R6；v1.3 落点收敛）**：上表即复用机制；`ScopeSession` 不再拥有独立压缩入口，因此"同一 step 内连续压两次"由构造消除：

| 复用项 | 位置 | 约束 |
|---|---|---|
| step 门控 | 800 | 同一 step 内 provider compact ≤ 1 次；本 step 已压过则直接失败 |
| 输入指纹去重 | 4842 | 同一输入指纹不重复压缩 |
| 进展判据 | 4853 | 压缩后必须"消息数变少或估算 token 变小"，否则视为失败并终止，不重试 |
| 每 run 配额（可选） | 与 `loop.go:621-622` 同作用域新增函数局部计数器 | `context_preflight_max_provider_compactions`（默认 3）；"per run" 语义须由一次断言测试固化（该作用域是 think 调用级，是否等于 run 级取决于调用层级） |

historyguard 的定位调整：**保留为 active-turn 压缩的实现细节**，不再独立拥有触发判定与事件。

### 4.3 事件与 payload 统一 schema

三个事件共用同一构造器 `preflightEventPayload(...)`，字段分层：

```
{
  "trace_id", "step", "turn_id",
  "prompt_tokens_before", "prompt_tokens_after",
  "message_tokens_before", "message_tokens_after",
  "tool_schema_tokens", "tool_count", "largest_tool_schema_tokens",
  "message_count_before", "message_count_after",
  "budget": {
    "prompt_budget", "effective_input_budget", "reserved_output_tokens",
    "hard_cap", "soft_cap",
    "source", "source_detail", "candidates", "degraded", "conflict"
  },
  "resolved": {
    "provider", "model", "context_limit", "output_limit",
    "capability_max_context_tokens", "auto_compact_ratio", "auto_compact_token_limit"
  },
  "compaction": {
    "required", "reason",
    "scope",            // "pending"（started）→ "active_turn" | "session" | "none"
    "scope_reason", "dominance", "applied"
  },
  "failure": {          // 仅 failed 事件
    "code", "reason", "detail", "suggested_action",
    "can_retry_after_compaction",
    "active_turn_message_count", "latest_replay_block_message_count"
  },
  "recovery_expected": true   // 仅 failed 事件：说明这是可恢复信号
}
```

约定：

- `started` 的 `compaction.scope = "pending"`，**不再硬编码** `active_turn_replay=true`（P1-3 / R13）；最终 scope 在 `compacted` / `failed` 回填；
- 三个事件的键集由同一构造器保证一致；`failure` 仅在 `failed` 出现，其余事件该键缺省（golden 测试按事件类型断言键集）；
- **事件时序与"恰好一次"（v1.2 新增）**：一次 preflight 判定周期内，`started` 至多一次；终态事件（`compacted` | `failed`）恰好一次；失败后外层恢复成功时**不补发** `started`，恢复结果回填到 `session_compact_completed`（同 `trace_id` + 既有 `failure_reason_code` + 新增 `preflight_scope`）；静默分支不发任何事件（保持现状），但**必须发指标**（v1.3，R19）：`context_preflight_total{decision="silent"}`；其中 `budget.PromptBudget <= 0` 的"预算不可解析"静默另记 `context_preflight_budget_total{state="unresolved"}`，使"配置缺失导致的静默"与"未超预算的正常静默"可区分。
- 兼容策略：旧键名（`active_turn_replay`、`prompt_only`、顶层 `message_tokens` 等）保留一个版本并标注 `deprecated`，由 `cmd/aicli/ui/render/encoding/encoder_test.go` 一类断言守护前缀白名单不变。

### 4.4 失败语义与观测闭环

- `context.preflight.failed` 明确为"本地闸门判定需要更强压缩"，payload 增加 `recovery_expected: true`；
- **恢复结果回填（v1.2 裁决；v1.3 核实落点）**：**不新增事件**。闭环所需的 `failure_reason_code` **已经**随 `preflightErr.Metadata()`（`prompt_preflight_error.go:70-84`）经 `loop.go:790` → `trySessionCompactionRecovery` 的 `budgetMetadata`（5315-5320 注入 started、5374 克隆进 completed）流入 `session_compact_started` 与 `session_compact_completed`。因此本项**只需新增 `preflight_scope`**（由 `PromptPreflightError.PreferredScope` 带出），**不新增 `preflight_failure_code` 同义键**——闭环判据直接使用既有 `failure_reason_code` + 同一 `trace_id`，形成 `failed → recovered | terminated`。理由：事件面已经过多（P0-4 的精神是收敛），且 `session_compact_completed` 已是该恢复路径的既有终态事件。
- **指标对齐既有约定**（`internal/observability`，R4）：新增常量 + 记录器，标签全部低基数、不按 model/provider 展开：

```go
// metrics.go：常量（v1.3 改名加 context_ 前缀，避免与既有 MetricToolPreflightTotal = "tool_preflight_total"（metrics.go:61）混淆）
MetricContextPreflightTotal          = "context_preflight_total"            // decision=started|compacted|failed|silent
MetricContextPreflightFailedTotal    = "context_preflight_failed_total"     // error_code, scope
MetricContextPreflightRecoveredTotal = "context_preflight_recovered_total"  // scope, outcome=recovered|terminated
MetricContextPreflightBudgetTotal    = "context_preflight_budget_total"     // source, budget_state=ok|degraded|conflict|unresolved

// 标签：复用 LabelDecision / LabelSource / LabelErrorCode / LabelOutcome，新增 LabelScope = "scope"（metrics.go:70-98 现无 scope 标签）

// 记录器（与 RecordToolOutcome / RecordDoomLoop 同风格：包级函数 → IncrementCounter）
func RecordContextPreflightDecision(decision, scope string)
func RecordContextPreflightFailed(code, scope string)
func RecordContextPreflightRecovered(scope, outcome string)
func RecordContextPreflightBudget(source, state string)

// 基数上限（v1.3 更新）：error_code ≤ 5、scope ≤ 3、source ≤ 6、budget_state = 4（含 unresolved）→ 单指标最大 24 序列
```

  基数上限（v1.3）：`error_code ≤ 5`、`scope ≤ 3`、`source ≤ 6`、`budget_state = 4`（含 `unresolved`）→ 单指标最大 24 序列。
- **告警阈值**（写入 runbook）：

| 信号 | 阈值 | 含义 |
|---|---|---|
| `context_preflight_failed_total{code="no_compaction_scope_available"}` | > 0 / 5min | 回归信号：该 code 只应在真不可压时出现 |
| `context_preflight_failed_total{code="active_turn_not_compactable"}` | 应恒为 0（别名期） | 别名未清理或旧路径复活 |
| `context_preflight_budget_total{state="degraded"}` | 占比 > 20% / 1h | 窗口配置缺失，闸门退化为 fallback |
| `context_preflight_budget_total{state="unresolved"}` | > 0 / 1h | **预算完全不可解析**（`PromptBudget <= 0` 的静默路径，v1.3 新增）；意味着闸门未生效 |
| `context_preflight_recovered_total{outcome="terminated"}` | 环比上升 | 恢复能力退化 |

- **工具 schema 维度**（R12）：`tool_schema_exceeds_budget` 事件必须带 `tool_count`、`tool_schema_tokens`、`largest_tool_schema_tokens`，为"工具 schema 裁剪"（§14 跟踪项）提供数据。

### 4.5 幂等与配额（v1.1 新增）

| 约束 | 口径 | 实现落点 |
|---|---|---|
| 幂等 | `resolveInputBudget` / `selectCompactionScope` 无副作用，同输入同输出（I3） | 纯函数 + 表驱动测试 |
| step 配额 | 同一 step 内 provider compact ≤ 1 次（I5） | 复用 `sessionCompactionRecoveryStep` 门控（800） |
| 指纹去重 | 同一输入指纹不重复压缩 | 复用 `markSessionCompactionRecoveryInput`（4842） |
| 进展判据 | 压缩后必须"消息数变少或估算 token 变小" | 复用 `compactionRecoveryMadeProgress`（4853） |
| run 配额 | 每 run provider compact ≤ `context_preflight_max_provider_compactions`（默认 3） | 新增计数器，随 run 生命周期重置 |
| 配额耗尽 | 直接失败并终止，不重试 | 新 code `compaction_quota_exhausted` |

### 4.6 成本、延迟与 prompt cache 影响（v1.1 新增）

- **成本模型**：`cost ≈ tokens(被替换内容) + tokens(摘要输出) + 一次额外请求的固定开销`。`ScopeSession` 的成本显著高于 active-turn 局部压缩，因此：
  - 触发前置条件：`remainingBudget ≥ 预估成本`，否则直接失败（code `insufficient_budget_for_compaction`），避免"压完仍然超限"的付费失败；
  - 收益判据：仅当"压缩后预期输入 ≤ budget"才值得压，由 `compactionRecoveryMadeProgress` 兜底。
- **prompt cache**：任何历史替换都会 break cache epoch；现有 active-turn 路径已标 `prompt_cache_epoch_break` / `prompt_cache_epoch_reason`（5199-5200），`ScopeSession` 必须沿用同一标记并补 `cache_epoch_break_reason`，使"付费换空间"在观测上可见。
- **延迟**：session compact 是同步阻塞路径，需在事件里记录耗时（`compaction.duration_ms`），纳入 §7 的 benchmark 与告警（如 P95 > 10s 告警）。

---

### 4.7 预算单一来源（v1.3 重写：从"收敛"改为"守护 + 登记"）

针对修正后的 P0-5：**同源已经成立**（证据见 §1.2 / P0-5），因此本节职责不是"建立同源"，而是"守护同源 + 登记真实差异"。

1. **Step 3 只观测（保留）**：事件输出 `budget.prompt_budget`（闸门）与 `resolved.context_manager_max_prompt_tokens`（构建期，可直接取 `BuildResult.Metadata["budget_max_prompt_tokens"]` 既有键），并给出关系判定：

| `budget.source_relation` | 含义 | 处置 |
|---|---|---|
| `same` | 两侧一致（无 run 预算收紧） | 正常 |
| `gate_stricter` | 闸门因 `remainingBudget` 更严 | 记录占比；这是**设计内**差异（turn-fixed 构建 vs run 预算闸门） |
| `builder_stricter` | 理论上不可达 | **断言为 0**；出现即 I6 破坏（构建期被 profile 或其他路径重新收紧），按缺陷处理 |

2. **Step 5 删除 no-op 项**（v1.2 的"让 contextmgr 消费 `resolveInputBudget`"已存在，删除）：`contextmgr` 已消费闸门预算（`manager.go:397-398`）。Step 5 只做：清理别名与死代码、移除不可达分支的观测代码、翻转默认开关。
3. **同源守护**：`loop_test.go:2839-2855` 是既有守护（构建预算 = 900000 而非 profile 12000）；Step 3 在该用例上追加 `source_relation == "same"` 断言，作为 I6 的回归门。
4. **count 维度登记**：`KeepRecentMessages` / `MaxMessages` 来自 profile 且不被 `PromptBudget` 覆盖（见 P0-5 末段），本期只加守护测试、不改语义；若未来要收敛，须先有"count 裁剪先于 token 闸门生效"的观测数据。

---

## 5. 迁移步骤（每步可独立验证、可独立回滚）

| 步骤 | 内容 | 对应问题 | 风险 |
|---|---|---|---|
| Step 0 | 基线冻结：提交 / 隔离在飞改动，记录基线 commit | R5 | 低 |
| Step 1 | scope 判定 + 双重压缩防护（历史主导时跳过 active-turn 压缩，把 `preferred_scope` 交回既有门控恢复路径） | P0-3 / P1-3 / R6 / R20 / R23 | 低-中 |
| Step 2 | 事件 payload 统一构造器 + 旧键兼容 + `recovery_expected` 回填 | P1-1 / P1-2 | 低（纯观测） |
| Step 3 | 预算解析改为硬上限 / 软阈值分层 + `degraded` / `conflict` 标记（开关灰度） | P0-1 / P0-2 / R2 | 中（改变预算选择行为） |
| Step 4 | 压缩入口收敛：preflight 不再直接调 historyguard，统一经 scope 接口 | P0-4 | 中（涉及替换语义与 cache epoch） |
| Step 5 | 清理旧候选语义、别名与死代码（`hasPromptPreflightContextBudgetOverride`）、更新文档与验收脚本、翻转默认开关 | 收尾 / R25 | 低 |

每步的门禁与判据（**实施前必须逐条确认**，R3 / R9）：

**Step 0 — 基线冻结**
- 前置：无。
- 动作：提交或隔离在飞改动（当前 24 文件 / ~1083 行）；实施分支记录基线 commit 并回填到本文档。
- 验收：实施分支 `git status` 干净，diff 基准明确。
- Abort：在飞改动无法隔离 → 暂停实施。

**Step 1 — scope 判定与防护**
- 前置：Step 0。
- 动作：`historyguard` 新增导出 API `ActiveTurnBounds`（R20）；新增 `selectCompactionScope`；`PromptPreflightError` 新增 `PreferredScope`；历史主导时跳过 active-turn 压缩并把 `preferred_scope` 交回既有门控路径（R23）；`started` 不再硬编码 scope；复用 step 门控 / 指纹去重 / 进展判据。
- 验收判据：① 历史主导用例不再产生 `active_turn_not_compactable`（单测 + e2e）；② 同一 step 内 provider compact 次数 ≤ 1（计数断言）；③ 既有回归测试全绿。
- Abort 判据：`no_compaction_scope_available` > 0/5min，或出现 provider 超窗错误 → 回退该步。

**Step 2 — 事件 schema 统一**
- 前置：Step 1（scope 字段来源确定）。
- 验收判据：① 三个事件键集由同一构造器产生且 golden 通过；② 旧键仍存在且标 `deprecated`；③ `failed → recovered|terminated` 可在同一 `trace_id` 下闭合。
- Abort 判据：`encoder_test.go` 前缀白名单断言失败，或 UI 出现字段缺失 → 回退。

**Step 3 — 预算分层解析**
- 前置：Step 0；开关 `context_preflight_budget_mode` 已就绪（默认 `legacy`）。
- 验收判据：① I1 / I2 不变量测试通过；② `degraded` / `conflict` 在事件中可见；③ unified 灰度下 `context_preflight_failed_total` 不高于 legacy 基线；④ **I6 观测期（v1.3 修正）**：`budget.source_relation` 在事件中可见，`builder_stricter` 断言为 0，`gate_stricter` 占比被记录（它是 `remainingBudget` 收紧的设计内差异，不是缺陷）。
- Abort 判据：出现任一 provider 超窗错误，或 `budget > hardCap` 断言失败 → **立即切回 `legacy`**。

**Step 4 — 压缩入口收敛**
- 前置：Step 1（scope 接口已存在）。
- 验收判据：① preflight 不再直接调用 historyguard（代码检查）；② 替换语义与 `prompt_cache_epoch_break` 标记一致；③ 压缩耗时与成本指标可见。
- Abort 判据：session compact 的 P95 延迟或成本超阈值 → 回退。

**Step 5 — 清理与默认翻转**
- 前置：Step 3 / Step 4 灰度观察期结束。
- 验收判据：① 别名与旧候选语义移除；② 文档与验收脚本同步更新；③ 开关默认翻转为 `unified` 且观察期无回归；④ **I6 守护（v1.3 修正）**：同源由既有测试（`loop_test.go:2839-2855`）+ `builder_stricter == 0` 断言守护——不再有"让 contextmgr 消费 `resolveInputBudget`"这一步（该行为已存在，v1.2 的该项为 no-op，删除）。
- Abort 判据：翻转后 24h 内出现回归 → 切回 `legacy`。

### 5.1 文件级改动清单（v1.3 新增，实施落点）

| 步骤 | 文件 | 改动 |
|---|---|---|
| Step 1 | `backend/internal/historyguard/active_turn.go` | 新增导出 `ActiveTurnBounds(messages) (userIndex, preserveStart int, ok bool)`（内部复用 `activeUserTurnStart` / `latestReplayBlockStart`） |
| Step 1 | `backend/internal/agent/loop.go` | 新增 `selectCompactionScope`（纯函数）；`enforcePromptPreflightWithTools` 在 scope=`none`/`session` 时跳过 active-turn 尝试并按新 code 失败；`started` 不再写 `active_turn_replay=true` |
| Step 1 | `backend/internal/agent/prompt_preflight_error.go` | `PromptPreflightError` 新增 `PreferredScope`；`Metadata()` 输出 `preflight_scope` |
| Step 2 | `backend/internal/agent/loop.go` | 三个事件共用 `preflightEventPayload` 构造器；旧键保留并标 `deprecated` |
| Step 3 | `backend/internal/agent/loop.go` | `resolveInputBudget`（硬上限/软阈值分层）替换候选拼装；`resolvePromptPreflightBudget` 保留为 `legacy` 分支 |
| Step 3 | `backend/internal/observability/metrics.go` | 新增 `MetricContextPreflight*` 常量与 `LabelScope` |
| Step 3 | `backend/internal/observability/tool_efficiency.go` | 新增 `RecordContextPreflight*` 记录器（同 `RecordDoomLoop` 风格） |
| Step 4 | `backend/internal/agent/loop.go` | 压缩入口收敛：preflight 不再直接调用 `historyguard`，统一经 scope 接口 |
| Step 5 | `backend/internal/agent/loop.go` | 删除死代码 `hasPromptPreflightContextBudgetOverride`（全仓无调用者）；清理别名与不可达观测分支 |
| Step 5 | `docs/plan/session-preflight-auto-compact-test-scripts.md` | 验收脚本同步（新 code、`preflight_scope`、`source_relation`） |

---

## 6. 兼容性与配置映射

| 配置 / 字段 | 现状 | 目标 | 兼容性 |
|---|---|---|---|
| `context_max_prompt_tokens` | 作为候选参与 min，可能被更小推导值覆盖 | 优先于软阈值，但不可突破硬上限 | 行为变化，需开关灰度 |
| `context_fallback_max_prompt_tokens` | fallback 候选 | 保留，标记 `degraded` | 兼容 |
| `auto_compact_token_limit` | 候选（可与显式竞争） | 无显式配置时的 softCap 来源 | 兼容 |
| `auto_compact_ratio`（默认 0.85） | `max_context_tokens × ratio` | 统一为 `marginRatio`，可按 provider 覆盖（本期仅预留键） | 兼容（默认值不变） |
| `remaining_budget` | 候选 + 预留输出约束 | **硬上限**，始终生效 | 兼容（语义显式化） |
| `budget_source` / `budget_candidates` | 诊断字段 | 保留，新增 `degraded` / `conflict` / 候选处置 | 兼容 |
| `failure_reason_code` | 3 个 code | 新增 `no_compaction_scope_available`、`insufficient_budget_for_compaction`、`compaction_quota_exhausted`；旧 code 保留别名一版 | 兼容 |

### 开关登记表（R3）

| 开关 | 取值 | 默认 | Owner | 翻转条件 | 移除版本 |
|---|---|---|---|---|---|
| `context_preflight_budget_mode` | `legacy` / `unified` | `legacy` | agent 运行时 owner | 连续 7 天 unified 灰度：`context_preflight_failed_total` 不高于 legacy 基线且无 provider 超窗错误 | 下一个小版本 |
| `context_preflight_max_provider_compactions` | int | `3` | agent 运行时 owner | —（成本观测驱动调整） | 保留 |
| `context_preflight_dominance_ratio` | float | `0.6` | agent 运行时 owner | 出现 scope 误判（压了不该压的 / 该压没压）时调整 | 保留 |

**开关生效时延与回滚演练（v1.2 新增）**：

- 生效时延必须显式声明：`next_turn`（下一 turn 生效）或 `restart`（需重启）；若开关只在进程启动时读取，则"立即回退"在实现上不成立，必须写明并据此设定 abort 判据的观察窗口。
- Step 3 / Step 4 上线前各做一次**回滚演练**：切开关 → 观察目标指标回落 → 记录耗时；演练记录作为上线证据（与 §5 的验收判据一并归档）。

---

## 7. 测试计划

> 锚点约定：测试文件与用例名以实施基线复核后为准（v1.0 记录的行号随工作区漂移，见审查报告 R1）。

**1) 预算解析（表驱动 + 不变量）**

| 用例 | 断言 |
|---|---|
| 六类来源两两组合（显式 / autoCap / ratio / provider / fallback / remaining） | `budget`、`source`、候选处置正确 |
| 显式 > 窗口 | `budget = hardCap`，`budget_conflict=true`（I1） |
| 显式 < 软推导 | `budget = 显式`，软推导标 `shadowed_by_explicit`（I2） |
| 窗口不可解析 | `budget = fallback`，`budget_degraded=true` |
| `remainingBudget = 0` / 负数 | 走无硬上限分支或直接静默返回，不 panic |
| 同输入重复调用 | 结果一致（I3） |

**2) scope 选择（可达性口径，v1.2 更新）**

- 可达于 active turn（`historyTokens + activeResidual ≤ messageBudget`）→ `ScopeActiveTurn`；
- active turn 不可达、历史可压 → `ScopeSession`；
- 两者都不可达 → `none` + `no_compaction_scope_available`；
- 估算不可用（`preserveStart` 无法定位）→ 走 `dominance` 兜底，且 `scope_reason` 必须标出"兜底路径"；
- 工具 schema 吃满 → `tool_schema_exceeds_budget`（保持现状）。

**3) 幂等与配额（I5）**

- 同一 step 连续两次触发 preflight → 仅一次 provider compact；
- 同一输入指纹重复 → 跳过；
- 压缩无进展（`compactionRecoveryMadeProgress=false`）→ 视为失败并终止，不重试；
- 超出 run 配额 → `compaction_quota_exhausted`。

**4) 事件 schema golden**

- 三个事件的键集快照；**失败模式分别断言**：键缺失 / 键多余 / 类型变化各自报错（避免"多一个键也通过"）；
- 旧键兼容断言 + `cmd/aicli/ui/render/encoding/encoder_test.go` 前缀白名单不变；
- `started` 的 `compaction.scope == "pending"`，`compacted` / `failed` 回填最终 scope。

**5) 性能**

- `estimatePromptMessageTokens` / `estimateToolDefinitionTokens` benchmark（大 messages、大工具集）；
- 每 step 估算调用次数预算（当前实现多次重复计数，需断言不超过既定次数，防止收敛后反而放大开销）。

**6) 回归（保持绿）**

`backend/internal/agent/loop_test.go`、`backend/internal/chatcore/provider_loop_test.go`、`backend/internal/api/skills/session_runtime_handlers_test.go`、`backend/cmd/aicli/ui/render/encoding/encoder_test.go`（按实施基线复核锚点）。

**7) 事件时序与预算关系（v1.2 新增；v1.3 更新）**

- `started` 至多一次、终态事件（`compacted` | `failed`）恰好一次；
- 恢复成功不补发 `started`，且 `session_compact_completed` 带既有 `failure_reason_code` + 新增 `preflight_scope` + 同 `trace_id`；
- `budget.source_relation`：`same` / `gate_stricter` 各自断言；**`builder_stricter` 断言为 0**（不可达，v1.3 修正）。
- **I6 同源守护（v1.3，R19）**：在既有 `loop_test.go:2839-2855` 用例（构建预算 900000 vs profile 12000）上追加 `source_relation == "same"` 断言，防止同源链路再次分叉；
- **静默路径可见性（R19）**：`budget.PromptBudget <= 0` 时断言 `context_preflight_budget_total{state="unresolved"}` 被记录且**不发事件**（观测黑洞封堵）；
- **count 维度守护（latent）**：`KeepRecentMessages` 仅在 token 压力下生效（`manager.go:451`），断言"无压力不裁剪、有压力时裁剪口径与闸门同源"；
- **autoCap 不叠加 margin（R26 负例）**：`auto_compact_token_limit` 存在时断言 `budget == min(autoCap, hardCap)`，不出现 ×0.85 收紧；
- **inputCeiling 前置条件（R27）**：`reservedOutput == 0` 或 `hardWindow` 不可解析时断言不施加窗口上限（与现状 `loop.go:5746` 一致）。

**8) 端到端**

按 `docs/plan/session-preflight-auto-compact-test-scripts.md` 执行，新增通过标准：

- 历史主导的超限不再出现 `active_turn_not_compactable`；
- 每个 `failed` 事件都能在同一 `trace_id` 下找到 `recovered` 或终止原因；
- `budget_degraded=true` 时事件给出缺失的配置项；
- 同一 step 内 provider compact ≤ 1 次。

---

## 8. 风险与回滚

| 风险 | 概率 | 影响 | 监测信号 | 缓解 / 回滚 |
|---|---|---|---|---|
| 显式优先改变既有行为（原本更小的软推导值会覆盖显式配置） | 高 | 中 | `context_preflight_budget_total{state="conflict"}` | 开关灰度；回滚 = 切回 `legacy` |
| scope 误判导致压缩代价上升（把可本地解决的超限推给 provider compact） | 中 | 中 | `context_preflight_total{scope="session"}` 占比、compact 耗时/成本 | scope 判定保守（`dominance_ratio` 可调）+ 事件记录判定依据；回滚 = Step 1 单独回退 |
| 未登记的行为收紧（如对 autoCap 叠加 margin） | 中 | 中 | `context_preflight_total{decision="started"}` 环比、cache epoch break 计数 | §4.1 约定 6 明确不叠加；变更须走开关 + 风险登记（v1.3，R26） |
| 双重压缩 / 恢复风暴 | 中 | 高 | 同 step compact 次数、`compaction_quota_exhausted` | 复用 step 门控 + 指纹去重 + 进展判据（§4.5） |
| 事件字段变更影响 UI / 验收脚本 | 中 | 中 | encoder 白名单测试、UI timeline 字段缺失 | 旧键保留一版 + golden 测试守护 |
| 硬上限被绕过（I1 破坏） | 低 | 高 | `budget > hardCap` 断言、provider 超窗错误 | 不变量测试 + 断言埋点；回滚 = 切回 `legacy` |
| session compact 延迟拖慢交互 | 中 | 中 | `compaction.duration_ms` P95 | 超阈值告警 + 配额收紧 |

**回滚粒度**：Step 1 / Step 2 可单独回退且互不依赖；Step 3 / Step 4 由开关控制；Step 0 不产生代码变更。

---

## 9. 未决问题（已裁决：v1.1 五条 + v1.2 一条 + v1.3 两条）

| # | 问题 | 裁决 | 理由 |
|---|---|---|---|
| 1 | `context_max_prompt_tokens` 与更小的 capability 推导冲突时以谁为准 | 显式优先于**软**推导；**硬上限**不可突破；冲突标 `budget_conflict` | 用户意图不应被静默覆盖，但安全不变量优先级更高（I1） |
| 2 | `marginRatio` 是否按 provider / tokenizer 配置 | 本期全局 0.85；仅预留配置键，观测到显著超窗/浪费后再引入 | 无数据不引入维度，避免候选组合爆炸 |
| 3 | 是否引入精确 tokenizer | 本期不引入；保留 counter 注入点 | 依赖体积与离线可用性；估算 + margin 已覆盖目标场景 |
| 4 | session compact 的成本控制与配额 | 每 step ≤ 1、每 run ≤ 3（可配）；复用既有去重与进展判据 | 防止恢复风暴与双重压缩 |
| 5 | `no_compaction_scope_available` 别名期长度 | 一个发布周期，schema golden 守护，下一版本移除 | 与 §6 deprecated 策略一致 |
| 6 | 恢复结果回填方式（v1.2 新增） | 复用 `session_compact_completed` 追加字段，**不新增事件** | 事件面已过多，收敛优先（P0-4 精神） |
| 7 | `ScopeSession` 的执行落点（v1.3 新增） | 判定在 preflight，**执行留在既有门控路径**（`loop.go:786-845`）；preflight 只返回 `preferred_scope` | step 门控与指纹去重是 think 函数局部量，preflight 方法不可达（R23）；零新增状态、I5 由构造保证 |
| 8 | `auto_compact_token_limit` 是否叠加 margin（v1.3 新增） | **不叠加**（保持现状 `loop.go:5697-5708`） | 叠加会静默收紧 15%，属未登记行为变化（R26） |

---

## 10. 验收标准（DoD，可断言口径）

1. 单次请求输入闸门可由**一条事件**完整解释：事件包含 `source` / `hard_cap` / `soft_cap` / `candidates`（含处置）/ `degraded` / `conflict`，golden 断言通过；
2. 历史主导场景中 `active_turn_not_compactable` 计数为 **0**（单测 + e2e）；
3. 不变量 I1~I5 全部有测试守护且通过；
4. **100%** 的 `failed` 事件可在同一 `trace_id` 下找到 `recovered` 或 `terminated` 结论；
5. 事件 schema 与 golden **100%** 一致；旧键兼容断言通过；`encoder` 前缀白名单不变；
6. 同一 step 内 provider compact ≤ 1 次，run 配额生效（计数断言）；
7. `unified` 灰度连续 7 天：`context_preflight_failed_total` 不高于 `legacy` 基线，且 provider 超窗错误为 0。
8. 事件时序断言通过：`started` 至多一次、终态恰好一次、恢复不补发 `started`；
9. `budget.source_relation` 在事件中可见；`builder_stricter` 断言为 **0**（不可达，v1.3 修正），`gate_stricter` 占比被记录（I6）。

---

## 11. 术语表

| 术语 | 定义 |
|---|---|
| 输入闸门（input gate） | 本地在请求 provider 前对"估算输入 tokens"施加的上限判定 |
| 硬上限（hardCap） | 不可被任何配置突破的上限：`min(inputCeiling, remainingBudget)` |
| 软阈值（softCap） | 提前量性质的上限：显式配置、`auto_compact_token_limit`、`ratio`、fallback |
| 预算来源（source） | 决定最终 `budget` 的唯一来源，写入事件 |
| scope | 压缩作用范围：`active_turn` / `session` / `none` |
| dominance | active turn tokens 占 `messageBudget` 的比例，用于 scope 判定 |
| 进展（progress） | 压缩后"消息数变少或估算 token 变小"，否则不算进展 |
| 别名期 | 旧 code / 旧键保留一个发布周期的兼容窗口 |

---

## 12. 决策记录（ADR 摘要）

| # | 决策 | 依据 |
|---|---|---|
| D1 | 预算解析分层为硬上限 / 软阈值 | 三类语义混在 `min()` 是 P0-1/P0-2 根因；安全不变量优先 |
| D2 | 显式配置优先于软推导，但不突破硬上限 | 用户意图可解释性 + 不重新引入 provider 超窗 |
| D3 | `marginRatio` 本期全局 0.85 | 无观测数据不引入 provider 维度 |
| D4 | 不引入精确 tokenizer | 依赖与离线可用性；保留注入点 |
| D5 | 每 step ≤ 1、每 run ≤ 3 次 provider compact | 防止恢复风暴；机制已存在 |
| D6 | 新 code 与旧 code 并存一个发布周期 | 消费方（UI / 验收脚本）平滑过渡 |
| D7 | 指标直接记录（非事件派生），标签低基数 | 与 `RecordToolOutcome` / `RecordDoomLoop` 既有约定一致 |

---

## 13. 契约 delta 表

| 类别 | 新增 | 变更 | 废弃（一个版本） |
|---|---|---|---|
| 配置键 | `context_preflight_max_provider_compactions`、`context_preflight_dominance_ratio` | `context_max_prompt_tokens`（优先于软推导）、`remaining_budget`（显式化为硬上限） | — |
| 开关 | `context_preflight_budget_mode`（legacy/unified） | — | — |
| 事件 | —（v1.2 裁决：不新增 `context.preflight.recovered`，恢复结果回填到 `session_compact_completed`） | `context.preflight.*` 三个事件 payload 统一；`session_compact_completed` 追加 `preflight_scope`（`failure_reason_code` 既有，不新增同义键，R21） | — |
| payload 键 | `budget.hard_cap`、`budget.soft_cap`、`budget.source_relation`、`budget.candidates[].disposition`、`resolved.context_manager_max_prompt_tokens`、`compaction.scope`、`compaction.scope_reason`、`compaction.achievability`、`compaction.dominance`、`compaction.duration_ms`、`largest_tool_schema_tokens`、`recovery_expected`、`preflight_scope` | `started` 不再硬编码 `active_turn_replay`（改 `compaction.scope="pending"`）；`source_relation` 取值收敛为 `same` / `gate_stricter`（`builder_stricter` 保留枚举但不可达，R19） | `active_turn_replay`、`prompt_only`、顶层 `message_tokens` |
| failure code | `no_compaction_scope_available`、`insufficient_budget_for_compaction`、`compaction_quota_exhausted` | — | `active_turn_not_compactable`（别名） |
| 函数 | `resolveInputBudget`、`selectCompactionScope`、`preflightEventPayload`、`historyguard.ActiveTurnBounds`（R20）、`PromptPreflightError.PreferredScope` | `resolvePromptPreflightBudget`（由 `resolveInputBudget` 取代；保留为 `legacy` 分支） | `hasPromptPreflightContextBudgetOverride`（死代码，R25） |
| 指标 | `context_preflight_total`、`context_preflight_failed_total`、`context_preflight_recovered_total`、`context_preflight_budget_total`、标签 `scope` | —（v1.3 改名：避开既有 `tool_preflight_total`，R22） | — |

---

## 14. 范围外跟踪项

| 项 | 说明 | 建议归属 |
|---|---|---|
| 工具 schema 裁剪 / 上限 | `tool_schema_exceeds_budget` 目前只能失败；异常 MCP 工具集可稳定占满预算 | 独立方案（工具面治理） |
| 精确 tokenizer（tiktoken / provider count API） | 与估算偏差、离线可用性、依赖体积相关 | 独立方案 |
| 按 provider 的 `marginRatio` 覆盖 | 需先有超窗 / 浪费的观测数据 | 观测驱动，暂缓 |
| 压缩摘要质量与用户可见性 | 属产品体验，非闸门语义 | 独立方案 |
