# Context Preflight 预算与压缩收敛设计 · 审查报告

- 版本：v1.2（2026-09-21；v1.0 首轮 R1~R13 → v1.1 复核 R14~R18 → v1.2 第三轮 R19~R28）
- 状态：审查结论已回写到设计文档（v1.1 → v1.2 → **v1.3** 修复轮）；**R1~R28 全部闭合**（R1~R18 见 §8，R19~R28 见 §10）；实施前仅剩 §5 Step 0 基线冻结（硬前置）与符号再核（软前置）
- 审查对象：`docs/plan/context-preflight-budget-and-compaction-convergence-design-20260921.md`（v1.0 → v1.3）
- 审查方法：
  1. 代码事实核查（工作区快照 2026-09-21；`HEAD=f882a012` + 未提交改动，`backend/internal/agent/loop.go` 相对 HEAD +77 行）；
  2. 仓库既有约定核对（`internal/observability` 指标/标签/记录器约定；`internal/agent` 既有恢复路径的 step 门控与指纹去重）；
  3. 设计完整性逐项核对（问题→目标→设计→迁移→兼容→测试→风险→DoD 的闭环）。
- 结论：**设计方向成立**（P0-1~P0-4 的问题描述与代码事实一致），但**不能直接进入实施**：存在 1 处安全语义倒退（R2）、1 类事实锚点失锚（R1）、1 处与既有恢复机制的双重压缩风险（R6），以及观测/灰度/测试/DoD 四方面的可执行性缺口。

---

## 0. 结论摘要

| 编号 | 级别 | 结论 |
|---|---|---|
| R1 | 高（文档可验证性） | §1.1 六处行号锚点在当前工作区**全部不解析**（实际：静默返回 5114-5121、started 5163、tool_schema 5181、compacted 5228、still-exceeds 5259、not-compactable 5288）；§1.2 的 `5054-5057` 应为 5126-5129。根因：起草时未记录证据快照，工作区存在未提交改动导致行号漂移。 |
| R2 | 高（正确性/安全） | 「显式配置永远生效、推导值更小也不改变行为」会把 `hardWindow − reservedOutput` 与 `remainingBudget` 两个**硬上限**降级为可被显式配置突破，重新引入 provider 超窗失败。必须区分**硬上限**与**软阈值**。 |
| R3 | 中（可运维性） | 灰度开关无 owner、无默认值翻转条件、无移除版本、无 abort 判据；Step 1~5 只有"风险等级"，没有"何时必须回退"的可观测阈值。 |
| R4 | 中（观测约定） | 指标写法未对齐 `internal/observability` 既有约定（`MetricXxxTotal` 常量 + `LabelXxx` 低基数标签 + `RecordXxx` 记录器），也未声明基数上限与落点（注册表 vs 事件派生）。 |
| R5 | 中（工程治理） | 当前工作区有 24 个文件 / ~1083 行未提交改动（含 `loop.go` +77），在此基线上实施会与在飞改动纠缠，破坏"每步可独立回滚"前提。 |
| R6 | 中（成本/正确性） | 新增 `ScopeSession` 直委派会与既有恢复路径（调用点 807）形成**同一 step 内双重 provider compact**；设计未复用既有去重机制（`sessionCompactionRecoveryStep`、`markSessionCompactionRecoveryInput`、`compactionRecoveryMadeProgress`）。 |
| R7 | 中（成本） | provider compact 的成本、延迟与 prompt cache epoch 影响未量化，也没有"何时宁可 fail-fast 不压"的策略。 |
| R8 | 中（测试） | 测试计划缺不变量（硬上限不可突破、单调性）、负例（窗口不可解析 + 显式超大）、幂等、性能（估算调用次数与 O(n) 成本）与 schema golden 的失败模式。 |
| R9 | 低-中（验收） | §10 DoD 全部为定性表述，不可度量、不可自动断言。 |
| R10 | 低-中（决策） | §9 五条未决问题没有裁决路径（谁决定、何时决定、默认取谁）。 |
| R11 | 低（契约） | 缺"契约 delta 表"与术语表：新增/变更的配置键、事件名、payload 键、函数签名没有集中列表，消费方无法一眼评估影响面。 |
| R12 | 低（健壮性） | 缺工具 schema 膨胀视角：`tool_schema_exceeds_budget` 目前只能失败，异常 MCP 工具集可稳定占满预算。 |
| R13 | 中（观测正确性） | `started` 事件在压缩尝试前硬编码 `active_turn_replay=true`（5162），与实际 scope 无关——这正是"事件看起来失效/误导"的来源之一。 |

---

## 1. 事实基线核查（工作区快照 2026-09-21）

### 1.1 锚点核查表

| 设计文档锚点 | 核查结果 | 工作区实际位置 |
|---|---|---|
| `loop.go:5041-5076`（事件契约） | **失锚** | `enforcePromptPreflightWithTools` 起于 5113 |
| `5042-5049`（静默返回） | **失锚** | 5114-5121 |
| `5091`（started 事件） | **失锚** | 5163 |
| `5109`（tool_schema_exceeds_budget） | **失锚**（5109 实为 `enforcePromptPreflight` 包装函数） | 触发 5169 / 事件 5181 |
| `5156`（compacted 事件） | **失锚** | 5228 |
| `5187`（prompt_still_exceeds_budget_after_compaction） | **失锚** | 触发 5234 / 事件 5259 |
| `5216`（active_turn_not_compactable） | **失锚** | 触发 5265 / 事件 5288 |
| `786-845`（session 恢复路径） | 成立 | 调用点 807；`trySessionCompactionRecovery` 定义 5292 |
| `4884-4900`（token 估算函数） | 成立 | 4884 / 4895 / 4902 |
| `5054-5057`（计数调用） | **失锚** | 5126-5129 |
| `4974-4979`、`5030-5043`、`5638-5781`、`5753-5755`、`5891-5897` | 成立 | 同左 |
| `historyguard/active_turn.go:24 / 43-51 / 104-111` | 成立 | 同左 |
| `contextmgr/manager.go:28`、`compactruntime/runtime.go:120` | 成立 | 同左 |

失锚原因：v1.0 起草时的行号来自更早的工作区状态，而当前工作区含未提交改动（`loop.go` 相对 `f882a012` +77 行）。**行号会再次漂移**，因此 v1.1 改为"符号名 + 关键串"为主、行号为辅，并在文首记录证据快照。

### 1.2 既有机制（v1.0 未引用但必须复用）

| 机制 | 位置 | 作用 |
|---|---|---|
| step 门控 | `loop.go:800`（`sessionCompactionRecoveryStep != step`） | 同一 step 只允许一次 session 恢复 |
| 输入指纹去重 | `markSessionCompactionRecoveryInput`（4842）+ `sessionCompactionRecoveryInputs`（799-807） | 同一输入不重复压缩 |
| 进展判据 | `compactionRecoveryMadeProgress`（4853-4866） | 压缩后必须"消息数变少或估算 token 变小"，否则不算进展 |
| 观测约定 | `observability/metrics.go:11-67`（`MetricXxxTotal`）、`69-98`（`LabelXxx`）、`RecordDoomLoop` / `RecordToolOutcome` / `RecordToolFailure` | 低基数标签 + 显式记录器，不按高基名字展开 |

### 1.3 工作区状态（实施门禁的输入）

- `git status --short`：24 个文件已修改（+1083 / −53 行），另有本文档等未跟踪文件。
- `HEAD = f882a012`（2026-09-20，"feat(multi-agent): durable lifecycle hardening"）。
- 本报告与设计文档 v1.1 的所有行号均指**该工作区快照**。

---

## 2. 逐项发现

### R1 证据锚点失锚（高，文档可验证性）

- **现象**：§1.1 六处、§1.2 一处行号无法在当前工作区解析（见 §1.1 表）。
- **影响**：任何评审者按行号核对都会得到"代码里没有这段"的结论，设计失去可验证性；实施者可能改错位置。
- **整改要求**：全部锚点改为 `符号名 + 关键串`（如 `enforcePromptPreflightWithTools` 的 `messageBudget <= 0` 分支），行号标注为快照辅助信息；文首记录 `HEAD` 与"工作区脏"标记。

### R2 显式优先会突破硬上限（高，正确性/安全）

- **现象**：v1.0 §4.1 约定"显式配置永远生效，推导值更小只标 `budget_conflict` 不改行为"。但候选集合里包含 `hardWindow − reservedOutput`（窗口约束）与 `remainingBudget`（run 预算），二者是**安全上限**而非"提前量阈值"。
- **影响**：显式配置大于窗口时，preflight 闸门失效 → 请求直达 provider → 超窗报错（且各 provider 文案不统一），这正是本地闸门存在的理由。
- **整改要求**：分层语义——`hardCap = min(inputCeiling, remainingBudget)` 永不被突破；显式配置只与**软阈值**（`auto_compact_token_limit`、`ratio`、`fallback`）竞争优先级。不变量 I1：`budget ≤ hardCap`。

### R3 灰度与回滚缺可执行判据（中，可运维性）

- **现象**：`context_preflight_budget_mode` 无 owner、无翻转条件、无移除版本；Step 1~5 仅标"低/中"风险。
- **影响**：灰度只能靠人工感觉；出问题时无法快速判定"是否回退"。
- **整改要求**：开关登记表（名称/默认/owner/翻转条件/移除版本）+ 每步 abort 判据（可观测阈值 + 观察窗口 + 回退动作）。

### R4 指标未对齐既有观测约定（中，观测）

- **现象**：v1.0 直接写 `preflight_failed_total{code, scope}`，未给常量名、标签常量、记录函数、基数上限，也未说明落点。
- **影响**：实现者会各写一套；高基数风险（若把 model/provider 塞进标签）。
- **整改要求**：按 `observability` 约定给出 `MetricPreflight*Total` 常量、复用/新增 `LabelXxx`、`RecordPreflight*(...)` 记录器签名，并声明基数上限（code ≤4 × scope ≤2 = 8 序列）。

### R5 与在飞改动冲突（中，工程治理）

- **现象**：工作区 24 文件 / ~1083 行未提交改动，`loop.go` +77。
- **影响**：Step 1/2 直接改 `loop.go` 会与在飞改动混在一个提交里，"每步可独立回滚"不成立，code review 也无法聚焦。
- **整改要求**：新增 Step 0（基线冻结）：先提交或隔离在飞改动，实施分支记录基线 commit；本设计的所有改动以该基线为 diff 基准。

### R6 与既有恢复路径的双重压缩（中，成本/正确性）

- **现象**：v1.0 §4.2 要求 `ScopeSession` 时"直接委派 compactruntime"，但外层 786-845 在同一 step 失败后还会再走一次 session 恢复。
- **影响**：同一 step 内可能连续两次 provider compact（成本、延迟、cache epoch 双跳），且第二次大概率无进展。
- **整改要求**：`ScopeSession` 委派必须复用既有 step 门控（800）、输入指纹去重（4842）与进展判据（4853）；同一 step 内 provider compact ≤ 1 次，每 run 上限可配（默认 3）。

### R7 成本与 cache 影响未量化（中，成本）

- **现象**：v1.0 仅在风险表提了一句"压缩代价上升"。
- **影响**：无法判断"压 vs 直接失败"哪个更优；成本回归无基线。
- **整改要求**：给出成本模型（被替换内容 tokens + 摘要输出 tokens）与触发前置条件（`remainingBudget` 不足以覆盖一次 compact 成本时直接失败，code `insufficient_budget_for_compaction`）；明确 prompt cache epoch break 是**已付费**的既有行为（5162 区域的 `prompt_cache_epoch_break` 标记），新增路径必须沿用同一标记。

### R8 测试计划缺不变量/负例/性能（中，测试）

- **现象**：v1.0 §7 只有"表驱动 + 回归 + 快照 + e2e"。
- **影响**：最容易出错的边界（硬上限被突破、显式超大、窗口不可解析、重复调用）没有守护。
- **整改要求**：补 I1~I4 不变量测试、负例表、估算函数 benchmark 与"每 step 估算调用次数"预算、schema golden 的失败模式（键缺失/键多余/类型变化分别报错）。

### R9 DoD 不可度量（低-中，验收）

- **现象**：§10 五条均为定性表述（"可被一条事件完整解释"）。
- **整改要求**：改为可断言口径（见设计文档 v1.1 §10）。

### R10 未决问题无裁决路径（低-中，决策）

- **现象**：§9 五条问题既无 owner 也无默认取值，实施者会各自决定。
- **整改要求**：评审直接给出建议裁决 + 跟踪项（见 §4 与设计文档 v1.1 §9）。

### R11 缺契约 delta 与术语表（低，契约）

- **整改要求**：新增集中列表：配置键（新增/语义变更/废弃）、事件名、payload 键、函数签名、开关；术语表覆盖"输入闸门/硬上限/软阈值/scope/recovery"。

### R12 工具 schema 膨胀无出口（低，健壮性）

- **现象**：`tool_schema_exceeds_budget` 是终态失败路径，异常 MCP 工具集（或工具数量增长）可稳定占满预算。
- **整改要求**：本期至少在事件里给出 `tool_count`、`tool_schema_tokens`、最大单工具 schema tokens；把"工具 schema 裁剪/上限"登记为独立跟踪项，不混入本期范围。

### R13 started 事件硬编码 scope（中，观测正确性）

- **现象**：`startedPayload["active_turn_replay"] = true` 在压缩尝试之前写入（5162），无论后续实际 scope。
- **影响**：消费方按该字段判断"这是 active-turn 压缩"，与事实不符——直接对应"事件看起来失效"的用户感知。
- **整改要求**：scope 由 `selectCompactionScope` 判定后写入；`started` 只写"待判定"，判定结果在 `compacted` / `failed` 事件回填。

---

## 3. 整改清单（已回写到设计文档 v1.1）

| 编号 | 设计文档落点 | 整改内容 |
|---|---|---|
| R1 | v1.1 文首 + §1.1 + §1.2 | 记录证据快照（HEAD + 工作区脏标记）；锚点改符号名优先；修正 7 处失锚行号 |
| R2 | v1.1 §4.1 | 预算解析分层：硬上限（窗口−预留输出、remainingBudget）不可突破；显式配置只与软阈值竞争 |
| R3 | v1.1 §5 + §6 | 每步增加"前置门禁 / 验收判据 / abort 判据 / 回滚动作"；新增开关登记表与生命周期 |
| R4 | v1.1 §4.4 | 给出 `MetricPreflight*Total` 常量、标签复用、`RecordPreflight*(...)` 签名、基数上限与告警阈值 |
| R5 | v1.1 §5 Step 0 | 新增基线冻结步骤：先提交/隔离在飞改动，记录基线 commit |
| R6 | v1.1 §4.2 + §4.5 | `ScopeSession` 委派复用 step 门控 + 指纹去重 + 进展判据；新增幂等与配额小节 |
| R7 | v1.1 §4.6 | 新增成本模型、触发前置条件与 `insufficient_budget_for_compaction` code |
| R8 | v1.1 §7 | 补 I1~I4 不变量测试、负例表、benchmark、schema golden 失败模式 |
| R9 | v1.1 §10 | DoD 改为可断言口径 |
| R10 | v1.1 §9 | 未决问题全部裁决并附跟踪项 |
| R11 | v1.1 §11 + §13 | 新增术语表与契约 delta 表 |
| R12 | v1.1 §4.4 + §14 | 事件补工具 schema 维度；工具 schema 裁剪登记为范围外跟踪项 |
| R13 | v1.1 §2 P1-3 + §4.2 | `started` 不再硬编码 scope；scope 判定后回填 |

---

## 4. 未决问题裁决建议

| # | 问题 | 裁决建议 | 理由 |
|---|---|---|---|
| 1 | `context_max_prompt_tokens` 与更小的 capability 推导冲突时以谁为准 | 显式优先于**软**推导（capability / ratio / fallback），**硬上限**（窗口−预留输出、remainingBudget）不可突破；冲突时 `budget_conflict=true` | 用户意图不应被静默覆盖，但安全不变量优先级更高（R2） |
| 2 | `marginRatio` 是否按 provider / tokenizer 配置 | 本期保持全局 0.85（与现状一致）；仅预留配置键，观测到某 provider 显著超窗或显著浪费后再引入 | 无数据不引入维度（避免 6 候选 × N provider 的组合爆炸） |
| 3 | 是否引入精确 tokenizer | 本期不引入；在 `resolveInputBudget` 保留 counter 注入点，未来可替换 | 依赖体积与离线可用性；估算 + margin 已覆盖目标场景 |
| 4 | session compact 的成本控制与配额 | 每 step ≤ 1 次、每 run ≤ 3 次（可配 `context_preflight_max_provider_compactions`），复用既有去重与进展判据 | 防止恢复风暴；机制已存在（R6） |
| 5 | `no_compaction_scope_available` 别名期长度 | 一个发布周期，由 schema golden 测试守护；下一版本移除 `active_turn_not_compactable` 别名 | 与 §6 的 deprecated 策略一致 |

---

## 5. 实施门禁（Definition of Ready）

进入编码前必须全部满足：

1. **R5 基线冻结**：在飞改动已提交或隔离到独立分支；实施分支记录基线 commit。
2. **R1 锚点复核**：设计文档 v1.1 中的符号名锚点在实施基线上可解析（逐条 `grep` 验证）。
3. **R2 不变量确认**：`budget ≤ hardCap` 的断言位置与失败行为（拒绝配置 vs 标记 conflict）已与实现者确认。
4. **R6 复用确认**：`ScopeSession` 委派路径明确复用 `sessionCompactionRecoveryStep` / `markSessionCompactionRecoveryInput` / `compactionRecoveryMadeProgress`。
5. **R4 观测落点确认**：`internal/observability` 中新增常量与记录函数的命名已定稿。
6. **R3 abort 判据确认**：每个 Step 的回退触发阈值已写入设计文档。

---

## 6. 范围外跟踪项

| 项 | 说明 | 建议归属 |
|---|---|---|
| 工具 schema 裁剪 / 上限 | `tool_schema_exceeds_budget` 目前只能失败；异常 MCP 工具集可稳定占满预算 | 独立方案（工具面治理） |
| 精确 tokenizer（tiktoken / provider count API） | 与本地估算偏差、离线可用性、依赖体积相关 | 独立方案 |
| 按 provider 的 `marginRatio` 覆盖 | 需先有超窗/浪费的观测数据 | 观测驱动，暂缓 |
| 压缩后的会话可解释性（用户可见的摘要质量） | 属产品体验，非闸门语义 | 独立方案 |

---

## 7. 复核补充发现（v1.1 → v1.2 修复轮）

在设计文档 v1.1 修订完成后做了一轮"方案自身问题"复核（本轮任务），发现 5 项尚未闭合的问题，已全部回写到 v1.2。

### R14 contextmgr 构建预算与闸门预算不同源（高，机制性）

- **证据**：`context_max_prompt_tokens` 同时驱动 contextmgr 的 `overrides.MaxPromptTokens`（`agent.go:1094-1095`）与 preflight 的显式候选（`promptPreflightContextBudgetOverride`，`loop.go:5788-5793`）；但**未设置该键时两侧各自取默认**——contextmgr 走 budget profile（compact 8000 / extended 20000，`contextmgr/manager.go:204-232`），preflight 走 fallback 链（`loop.go:5771-5781`，最低 32000）。
- **影响**：① 构建期更严时（compact 8000 < 闸门 32000），历史在 `Manager.Build` 阶段已被裁到闸门以下，闸门几乎不触发——**这是"`context.preflight.*` 总是失效"的机制性原因之一**，比 P0-1/P0-2 更直接；② 闸门更严时，构建期不裁，触发后只能靠 active-turn 压缩救，落回 P0-3。
- **整改**：设计 v1.2 新增 P0-5、不变量 I6、§4.7（预算单一来源：Step 3 只观测 `source_relation`，Step 5 让 contextmgr 消费 `resolveInputBudget`）。

### R15 scope 判定由魔法阈值决定行为（中）

- **现象**：v1.1 用 `dominance ≥ 0.6` 决定 `ScopeActiveTurn` / `ScopeSession`，0.6 无依据；且"占比高"不等于"压得动"（active turn 可能全是不可摘要的 tool 结果）。
- **整改**：改为**可达性优先**——用 `historyTokens + activeResidual ≤ messageBudget` 判断 active-turn 是否真能压到预算内；`dominance` 降级为诊断与兜底（估算不可用时才启用），并在 `scope_reason` 标明兜底路径。

### R16 事件契约缺时序与"恰好一次"语义（低-中）

- **现象**：v1.1 定义了字段，但未定义 `started` 次数、终态事件次数、恢复后是否补发。
- **整改**：v1.2 §4.3 明确：`started` 至多一次、终态（`compacted` | `failed`）恰好一次、恢复成功不补发 `started`；§7 增加对应断言。

### R17 恢复回填方式未裁决（低）

- **现象**：v1.1 写"`session_compact_completed`（或新增 `context.preflight.recovered`）"，留了二选一。
- **整改**：v1.2 §4.4 裁决**不新增事件**，在既有 `session_compact_completed` 上追加 `preflight_failure_code` / `preflight_scope`（事件面收敛优先）。→ **v1.3 修正（R21）**：`failure_reason_code` 本就已流入该事件，最终只新增 `preflight_scope`，不再新增同义键。

### R18 开关未定义生效时延与回滚演练（中）

- **现象**：v1.1 只给了翻转条件，未说明开关是"下一 turn 生效"还是"需重启"。若只在进程启动时读取，"立即回退"在实现上不成立，abort 判据的观察窗口也随之失效。
- **整改**：v1.2 §6 要求显式声明生效时延（`next_turn` / `restart`），并要求 Step 3 / Step 4 上线前各做一次回滚演练并归档记录。

---

## 8. 整改关闭核对（截至 v1.2）

| 项 | 状态 | 落点 |
|---|---|---|
| R1 证据锚点失锚 | ✅ 已关闭 | v1.1 文首证据快照 + §1.1/§1.2 重锚（符号名优先） |
| R2 显式优先突破硬上限 | ✅ 已关闭 | §4.1 硬上限/软阈值分层 + 不变量 I1 |
| R3 灰度缺可执行判据 | ✅ 已关闭 | §5 每步门禁/验收/abort + §6 开关登记表（v1.2 补生效时延与演练） |
| R4 指标未对齐约定 | ✅ 已关闭 | §4.4 常量/标签/记录器/基数/告警阈值 |
| R5 与在飞改动冲突 | ✅ 已关闭 | §5 Step 0 基线冻结 + 本报告 §5 门禁 |
| R6 双重压缩 | ✅ 已关闭 | §4.2 复用表 + §4.5 幂等与配额 |
| R7 成本与 cache 未量化 | ✅ 已关闭 | §4.6 成本模型/前置条件/cache 标记/延迟 |
| R8 测试缺不变量/负例/性能 | ✅ 已关闭 | §7 不变量、负例、benchmark、golden 失败模式 |
| R9 DoD 不可度量 | ✅ 已关闭 | §10 可断言口径（v1.2 补 8/9 条） |
| R10 未决问题无裁决 | ✅ 已关闭 | §9 六条裁决（v1.2 补第 6 条） |
| R11 缺契约 delta/术语表 | ✅ 已关闭 | §11 / §13 |
| R12 工具 schema 膨胀无出口 | ✅ 已关闭 | §4.4 事件维度 + §14 跟踪项 |
| R13 started 硬编码 scope | ✅ 已关闭 | §2 P1-3 + §4.3 `scope="pending"` |
| R14 预算不同源 | ✅ 已关闭 | §2 P0-5 + 不变量 I6 + §4.7（Step 3 观测 / Step 5 收敛） |
| R15 scope 魔法阈值 | ✅ 已关闭 | §4.2 可达性优先 + §7 用例更新 |
| R16 事件时序缺失 | ✅ 已关闭 | §4.3 时序与"恰好一次" + §7 断言 |
| R17 恢复回填未裁决 | ✅ 已关闭 | §4.4 裁决（复用 `session_compact_completed`）+ §9 第 6 条 |
| R18 开关生效时延/演练 | ✅ 已关闭 | §6 生效时延 + 回滚演练要求 |

**结论**：R1~R18 全部在设计文档 v1.2 中闭合（第三轮复核见 §9 / §10，其中 R14 的结论已在 v1.3 中被证伪并重写）。剩余前置条件只有 §5 的实施门禁（其中 Step 0 基线冻结为硬前置，取决于工作区在飞改动何时提交或隔离）。

---

## 9. 第三轮复核（v1.3）：实施就绪性（R19~R28）

**复核目标**：从"设计是否自洽"推进到"**能不能照着做**"——逐条把设计里的符号、落点、数据流对回代码，凡"设计写了但代码里不存在/不可达"的一律暴露。

**复核手段**：逐跳读数据流（调用点 → 被调函数 → 消费方）+ 全仓引用检索（导出面 / 调用者 / 死代码）+ 既有测试反查（守护断言是否存在）。

**与前两轮的差别**：本轮出现**自纠**——v1.2 的一处高等级结论（P0-5 / R14）被代码证伪并作废，见 R19。

### R19 v1.2 的 P0-5 / R14 结论作废：构建期与闸门**已同源**（高，自纠）

- **原断言（v1.2）**：未设置 `context_max_prompt_tokens` 时，contextmgr 走 budget profile（compact 8000）、preflight 走 fallback 链（≥32000），构建期更严 → 闸门永不触发 → 需要"单一来源收敛"。
- **反证（逐跳读码）**：
  1. `loop.go:1631` `resolveContextBuildPromptBudget` → `resolvePromptPreflightBudget(..., remainingBudget=0)`（5634-5636）——**与闸门调用的是同一个函数**（闸门调用 5118）；
  2. `loop.go:1646` `PromptBudget: contextBudget.PromptBudget` → `contextmgr/manager.go:397-398` 用该值**覆盖 profile** → `manager.go:432` 层规格使用覆盖后的预算；
  3. 既有守护测试 `loop_test.go:2838-2855` 已经固定该语义：`budget_max_prompt_tokens=900000`（resolved）胜出，`budget_profile_max_prompt_tokens=12000`（profile）被标为 superseded；
  4. 两者唯一差异是 `remainingBudget`（闸门传入、构建期传 0，理由见 `loop.go:1666-1668`：构建期按 turn-fixed 工具面估算）→ 该差异**只能让闸门更严**，故 token 维度上 `builder_stricter` **不可达**。
- **真实机制（"事件不触发"的解释不变）**：① 分支事件契约期望错配（R13）；② 构建期已裁到同一预算 → 闸门静默返回（5114-5121 区间）；③ `budget.PromptBudget <= 0` 静默且**不发任何事件**（观测黑洞）；④ scope/触发条件不匹配（P0-3 / P1-3）。
- **整改**：§2 P0-5 重写为"已同源 + 守护 + count 维度登记"；§4.7 由"收敛"改为"守护"；Step 5 删除 no-op 项（v1.2 的"让 contextmgr 消费 `resolveInputBudget`"已存在）；I6 判据改为 `builder_stricter == 0` 断言 + 既有测试追加 `source_relation == "same"`。
- **教训（写入方法要求）**：v1.2 的"不同源"叙事建立在"函数名不同 ⇒ 链路不同"的推断上，没有逐跳读数据流。**凡断言"某配置/函数不生效"，必须给出该值的完整消费路径，或明确标注"未验证"。**

### R20 `historyguard` 关键函数未导出，scope 判定不可实现（高，可实施性）

- **现象**：§4.2 的 `activeTurnTokens` / `activeReducible` / `preserveStart` 依赖 `activeUserTurnStart` / `latestReplayBlockStart` / `estimatedMessagesBytes`。
- **证据**：三者**均为未导出**（`historyguard/active_turn.go:32 / 53 / 133 / 141`）；该包导出面只有 `CompactActiveTurnReplay`（20）、`CompactActiveTurnReplayWithCounter`（24）、`HasActiveTurnCompactionSummary`（132）。`agent` 包**无法**计算这三个量。
- **整改**：Step 1 增加硬前置——`historyguard` 新增导出 API `ActiveTurnBounds(messages) (userIndex, preserveStart int, ok bool)`，内部复用既有未导出函数；§5.1 已登记文件级落点。若不做此步，`selectCompactionScope` 只能退回"占比启发式"，等于 v1.2 的整改未落地。

### R21 闭环所需字段已存在，不应新增同义键（低）

- **现象**：v1.2 要求 `session_compact_completed` 追加 `preflight_failure_code`。
- **证据**：`failure_reason_code` **已经**随 `preflightErr.Metadata()`（`prompt_preflight_error.go:70-84`）经 `loop.go:790` → `trySessionCompactionRecovery` 的 `budgetMetadata`（5315-5320 注入 `started`、5374 克隆进 `completed`）流入两个 compact 事件。
- **整改**：只新增 `preflight_scope`；闭环判据使用既有 `failure_reason_code` + 同一 `trace_id`。新增 `preflight_failure_code` 会制造同义双键，属反模式。

### R22 指标名与既有 `tool_preflight_total` 易混（低）

- **证据**：`metrics.go:61` 已有 `MetricToolPreflightTotal = "tool_preflight_total"`（工具面预检，语义不同）；`metrics.go:70-98` 的标签集**没有** `scope`。
- **整改**：四个指标统一改名 `context_preflight_*`，新增 `LabelScope`；`budget_state` 由 3 态扩为 4 态（含 `unresolved`），单指标基数上限 18 → 24 序列（§4.4 已同步）。

### R23 `ScopeSession` 的执行落点不可达（高，可实施性）

- **现象**：v1.2 要求 `ScopeSession` "委派 `compactruntime`（phase=preflight）并复用 step 门控 / 指纹去重 / 进展判据"。
- **证据**：step 门控与输入指纹是 **think 循环的函数局部变量**（`loop.go:621-622`，使用点 800-806）；`enforcePromptPreflightWithTools`（5113）是方法，**无法访问**这些局部量。照 v1.2 实现只能新增一套并行状态 → 破坏 I5 的"零新增状态"初衷。
- **整改（裁决）**：**判定留 preflight，执行留既有门控路径**（`loop.go:786-845`）。preflight 只负责产出 `PromptPreflightError.PreferredScope` → `Metadata()` 输出 `preflight_scope` → 既有 806-807 执行 session 恢复；门控（800-806）、进展判据（810）、重试（842-843）全部复用。同一 step 双重压缩由构造消除（§4.5 复用表已改为"落点"表）。

### R24 count 维度差异未登记（低-中）

- **现象**：设计只讨论了 token 维度，未登记**消息条数**维度。
- **证据**：`KeepRecentMessages` / `MaxMessages` 来自 budget profile（compact 5 / extended 12 / balanced 8，`contextmgr/manager.go:204-232`），且受 `shouldApplyPromptCompactionPressure`（`manager.go:451`）门控——仅在 token 压力下生效。
- **整改**：本期**不改语义**，只在 §7 增加守护断言（无压力不裁剪、有压力时裁剪口径与闸门同源），避免"同源"结论在 count 维度被静默推翻。

### R25 死代码 `hasPromptPreflightContextBudgetOverride`（低）

- **证据**：`loop.go:5783` 定义，全仓无调用者。
- **整改**：Step 5 登记清理（§5 / §5.1 / §13 已同步），避免后续读者误以为存在"context budget 覆盖"这条路径。

### R26 `auto_compact_token_limit` 叠加 margin 属未登记行为收紧（中）

- **现象**：v1.2 写的 `min(autoCap, floor(inputCeiling × marginRatio))` 会对 autoCap **额外收紧 15%**。
- **证据**：现状 `loop.go:5697-5708` 只把 autoCap clamp 到 `max_context_tokens`，不乘 margin。
- **整改**：明确**不叠加**（§4.1 约定 6 + §9 第 8 条裁决 + §7 负例断言）。收益是避免"更频繁压缩 / 更多 cache epoch break"这类未登记变化；若未来要做，必须走开关 + 风险登记。

### R27 `inputCeiling` 前置条件未声明（中）

- **证据**：现状 `loop.go:5746` 仅在 `hardWindow > 0` 且 `reservedOutput > 0` 时施加窗口上限。
- **整改**：§4.1 注明前置条件，§7 增加负例断言（缺任一即不施加窗口上限），防止实现者"顺手补齐"而改变既有行为。

### R28 证据快照漂移（低，流程）

- **证据**：起草时 `HEAD=f882a012`；当前 `HEAD=1813cc87`（+25 commits），工作区仍有未提交改动（`git status --short` 44 行；`git diff --shortstat` = 40 files / +1853 / −256）。`f882a012` 仍是 `HEAD` 祖先（`git merge-base --is-ancestor` exit 0）。
- **整改**：文首快照更新；明确"行号仅对该快照有效，实施与复核以**符号名 + 关键串**为准"；Step 0 门禁要求实施前再核一次锚点。

---

## 10. 整改关闭核对（R19~R28，截至 v1.3）

| 项 | 状态 | 落点 |
|---|---|---|
| R19 预算不同源（证伪） | ✅ 已关闭（**结论作废并重写**） | §2 P0-5 + §4.7 重写 + §7 I6 守护 + Step 5 删 no-op |
| R20 historyguard 未导出 | ✅ 已关闭 | §4.2 符号可达性 + §5 Step 1 + §5.1 文件清单 |
| R21 同义键 | ✅ 已关闭 | §4.3 时序 + §4.4 回填 + §13 事件行 |
| R22 指标名冲突 | ✅ 已关闭 | §4.4 常量/标签/基数 + §13 指标行 |
| R23 执行落点不可达 | ✅ 已关闭 | §4.2 落点表 + §4.5 复用表 + §9 第 7 条 + §5.1 |
| R24 count 维度未登记 | ✅ 已关闭 | §7 count 守护 + §4.7 登记 |
| R25 死代码 | ✅ 已关闭 | §5 Step 5 + §5.1 + §13 函数行 |
| R26 autoCap 未登记收紧 | ✅ 已关闭 | §4.1 约定 6 + §9 第 8 条 + §7 负例 |
| R27 inputCeiling 前置条件 | ✅ 已关闭 | §4.1 + §7 负例 |
| R28 快照漂移 | ✅ 已关闭 | 文首快照 + Step 0 再核要求 |

**第三轮结论**：设计文档 v1.3 已把"可实施性"缺口补齐——每个新增符号都有落点、每条观测都有指标、每步验收都有可断言判据。**R1~R28 全部闭合**。

**实施前仅剩两类前置条件（与设计质量无关）**：

1. **Step 0 基线冻结（硬前置）**：工作区 40 files / +1853 / −256 未提交改动需先提交或隔离，否则"每步可独立回滚"不成立（R5 / R28）；
2. **符号再核（软前置）**：实施第一步用符号名 + 关键串复核 §5.1 清单中的锚点（行号仅对 `HEAD=1813cc87` 快照有效）。

**方法学提醒（供后续评审复用）**：本轮 3 个高等级问题（R19 / R20 / R23）全部来自同一动作——**把设计里的每个符号/落点对回代码**。前两轮做的是"设计自洽性审查"，只有这一动作能发现"设计写了但代码里不可达"。建议固化为 checklist：**凡设计新增函数/字段/事件键，必须给出"定义处 + 调用处 + 消费方"三要素。**
