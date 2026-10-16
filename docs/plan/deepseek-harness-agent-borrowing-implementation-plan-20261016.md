# DSH 子 Agent 设计借鉴实施方案

> 创建日期：2026-10-16
> 依据：`docs/analysis/deepseek-harness-vs-aar-agent-system.md`（对比分析报告）
> 参照系：deepseek-harness（DSH）`packages/subagent`、`packages/experimental/tool-ralph`、`packages/workflow` 的实测设计
> 涉及代码：`backend/internal/agent`、`agentcontrol`、`toolbroker`、`subagentbatch`、`subagentquota`、`errors`、`toolregistry`

---

## 1. 背景与目标

对比分析报告（见上）确认了 9 个可从 DSH 借鉴的设计点，按价值/成本排序为三个阶段。本方案将分析结论转为可执行的实施项，每个实施项包含：目标、改动文件、接口草图、测试计划与验收标准。

总体目标：

1. 把"子代理请求选项"从运行时拒绝升级为 **spawn 准入期能力位协商**（借鉴点 ①）。
2. 补齐**上下文继承**能力：fork 型子代理一次性快照 + KV Cache 保持约束（借鉴点 ②）。
3. 为"防钻牛角尖"补正向机制：**有界全新上下文迭代**（iterate_goal，借鉴点 ③），与既有 doom_loop 检测组成闭环。
4. 沉淀配套纪律：handoff 词汇（⑦）、结果防御解码（④）、deployment-owned 参数分层（⑥）。

## 2. 非目标

- 不引入 DSH 的 Cordis 插件容器 / patch.yml 组合层；保持 Go 显式装配。
- 不实现 provider named-registry 多实例化；单实现 + 能力位即可（见 §4.1 的取舍说明）。
- 不把 Session 事件日志改造为唯一事实源；SQLite BatchStore / AgentControl 主表路线不变。
- 不实现 JS 编排引擎（workflow 工具）；受限编排 DSL 仅列入 Phase 3 观察项（§5.4）。
- 不改变既有 `spawn_subagents` / `spawn_agent` 工具名与 payload 契约；新增字段全部可选。

## 3. 阶段总览

| 阶段 | 内容 | 预估 | 交付判据 |
|---|---|---|---|
| Phase 1 | 能力位协商（①）+ handoff 词汇（⑦） | 1–2 周 | spawn 准入期拒绝 + 契约字段落地，全量测试绿 |
| Phase 2 | fork 上下文继承（②）+ iterate_goal（③）+ 防御解码（④） | 2–4 周 | 两个新能力可用且与 doom_loop / quota 联动 |
| Phase 3 | 参数分层（⑥）+ 动态措辞（⑨）+ 外部 provider（⑧，观察项） | 按需 | 配置分层文档化 + 观察项评审结论 |

每个 Phase 结束做一次 `go test ./...` 全量回归 + 真实终端多 agent 验证（复用 `docs/plan/multi-agent-real-terminal-validation-runbook.md` 流程）。

---

## 4. Phase 1：结构准备

### 4.1 实施项 P1-1：子代理能力位协商

**问题**：当前 `SubagentTask` 的选项（`ToolsWhitelist`、`ReadOnly`、`AgentType`、结构化结果等）没有统一的准入期协商。`resolveChildToolSurface`（`agent/tool_vocabulary.go:210`）已做工具面校验，但：(a) 错误发生在 `ChildAgentFactory.Build` 内部，`spawn_subagents` 批次路径与单发路径各有一套；(b) 缺少"请求的能力 vs 当前部署支持的能力"的显式模型，模型无法在发起委派前知道哪些字段会生效。

**目标**：能力位在 **spawn 准入期一次性校验**，不支持的组合返回稳定错误码 + 可操作建议；委派工具描述按能力位动态措辞（为 P2-2 的 fork 措辞预留机制）。

**设计**（对齐 DSH `SubagentCapabilities` 语义，但保持 Go 风格）：

```go
// backend/internal/agent/capabilities.go（新文件）

// SubagentCapabilities 声明当前部署在 spawn 准入期支持的可选请求位。
// 借鉴 DSH SubagentCapabilities；nil 视为全默认（等价于零值）。
type SubagentCapabilities struct {
    AgentOptions      bool // 子级 provider/model/reasoning 覆盖（现状已支持，经 modelrouting）
    OutputSchema      bool // 子级结构化最终报告（P1-2 引入后置 true）
    DepthLimit        bool // 任务级 depth 覆盖（现状由 scheduler config 决定）
    ToolFilter        bool // tools_whitelist（现状已支持）
    Persona           bool // agentdef persona 影子提示词（agentdef bridge 已支持）
    ForkContext       bool // P2-1 引入后置 true
}

// DefaultCapabilities 从既有运行时特征推导，供宿主装配；
// P1-1 阶段 ForkContext/OutputSchema 为 false，P2 对应实施项置 true。
func DefaultCapabilities() SubagentCapabilities

// ResolveSubagentCapabilities 读宿主覆盖（runtime.yaml agents.capabilities.*），
// 未配置时回退 DefaultCapabilities。
```

**准入校验**：

```go
// backend/internal/agent/capabilities.go

// SubagentRequestWants 声明一次任务请求了哪些可选能力位。
type SubagentRequestWants struct {
    OverridesModel  bool // task.Provider/Model/ReasoningEffort 非空
    WantsToolFilter bool // task.ToolsWhitelist 非空
    WantsPersona    bool // task.AgentType 解析出 persona
    WantsFork       bool // task.Context == ContextFork（P2-1）
    WantsSchema     bool // task.ReportSchema 非空（P1-2）
}

// CheckCapabilities 在任务进入 ChildAgentFactory 之前调用；
// 返回 nil 或稳定错误码错误。调用方：SubagentScheduler.Run（批次与单发共用）。
func CheckCapabilities(caps SubagentCapabilities, wants SubagentRequestWants, taskID string) error
```

**错误码**（`backend/internal/errors/codes.go` 新增，风格对齐既有 `SPAWN_DEPTH_LIMIT` / `AGENT_THREAD_LIMIT`）：

- `SUBAGENT_CAPABILITY_UNSUPPORTED`——消息格式：`task %s requests unsupported capability %q; available: %s; next_action=remove_field_or_extend_deployment`。不可重试（重试同请求必然同败）。

**接线点**：

| 文件 | 改动 |
|---|---|
| `agent/scheduler.go` | `SubagentScheduler.Run`（或任务入队前）调用 `CheckCapabilities`；单发 `spawn_agent` 路径同样经过 |
| `agent/child_factory.go` | `Build` 移除与能力位重复的隐式降级分支（保留只读继承——那是策略不是能力位） |
| `runtime.yaml` 装配 | `agents.capabilities` 分节（可缺省）；文档见 `docs/config/` 现有配置目录风格 |
| `toolregistry` 描述 | `spawn_subagents` / `subagent_spawn` 的 description 附一行当前能力位（静态生成即可，P3-2 升级为动态） |

**测试计划**：

- `capabilities_test.go`：矩阵用例——每能力位 × 请求/不请求 × 部署开/关，断言错误码与消息可操作性。
- `spawn_subagents_policy_test.go` 补充：能力不足的任务整批拒绝（batch 拒绝于持久化之前，复用 `LiveSlotGate` 的"拒绝发生在任何 batch/task 行写入之前"语义）。
- 兼容性：现有任务包（不请求任何可选位）零行为变化。

**验收**：能力不足的 spawn 在任何子会话/批次行产生之前失败；错误信息含 `next_action`；现有测试全绿。

### 4.2 实施项 P1-2：handoff 契约词汇与结果防御解码

**问题**：批次内 writer→verifier、跨批次续作目前靠任务描述或文件传递状态，没有规范化交接概念；`SubagentResult.Summary` 是自由文本，无归一化校验与上限。

**目标**：引入**结构化、有界**的 handoff 字段（借鉴 DSH Ralph handoff 的状态机与 `maxHandoffChars`，但作为通用契约而非 ralph 专属）；父级渲染前做防御解码（借鉴 DSH `readReport` 的键集合 + 字段校验 + 超限报错）。

**设计**：

```go
// backend/internal/agent/subagent_handoff.go（新文件）

// HandoffStatus 与 DSH Ralph 对齐的状态机；通用化后 continue/complete/blocked
// 三态 + blocker 约束同样适用于普通子代理的最终报告（可选字段）。
type HandoffStatus string

const (
    HandoffContinue HandoffStatus = "continue"
    HandoffComplete HandoffStatus = "complete"
    HandoffBlocked  HandoffStatus = "blocked"
)

// Handoff 是跨任务/跨轮的规范化交接报告。作为 SubagentTask / SubagentResult
// 的可选字段；空值表示"不使用 handoff"（完全向后兼容）。
type Handoff struct {
    Status    HandoffStatus `json:"status"`
    Summary   string        `json:"summary"`
    Evidence  []string      `json:"evidence,omitempty"`
    NextSteps []string      `json:"next_steps,omitempty"`
    Blocker   string        `json:"blocker,omitempty"`
}

// MaxHandoffChars 默认上限（对齐 DSH 16_384；AAR 侧可经 agents.handoff.maxChars 覆盖）。
const MaxHandoffChars = 16384

// ValidateHandoff 状态机校验（直接移植 DSH 语义）：
//   - continue：必须有 NextSteps 且 Blocker 为空
//   - complete：必须有 Evidence、无 NextSteps、Blocker 为空
//   - blocked ：必须有具体（非空白）Blocker
//   - Summary 非空且 trim 相等；Evidence/NextSteps 每项非空 trim 相等
//   - JSON 序列化长度 <= MaxHandoffChars，超限报错（含实测值）
func ValidateHandoff(h Handoff, maxChars int) error

// DecodeHandoff 防御解码：子级输出 → Handoff。
// 键集合精确校验（多余/缺失字段报错，不静默忽略）、字段类型逐一断言、
// 错误消息携带实测值（如 `handoff summary must be non-empty; got ""`）。
func DecodeHandoff(raw map[string]interface{}, expectedStatus HandoffStatus, maxChars int) (Handoff, error)
```

**接线点**：

| 文件 | 改动 |
|---|---|
| `agent/scheduler.go` | `SubagentTask` 增加 `HandoffIn *Handoff`（入参，可选）；`SubagentResult` 增加 `HandoffOut *Handoff`（出参，可选） |
| `agent/subagent_parent_summary.go` | 渲染父级回执时：若有 `HandoffOut` 先 `ValidateHandoff`；无效则降级为普通 summary 并附加 `RouteWarning:"handoff_invalid:<reason>"` |
| `agent/subagent_batch_coordinator.go` | 批次行持久化包含 handoff 字段（`subagentbatch` schema v 迁移，新增列可空） |
| `prompt_builder.go` / 子级提示 | 子级提示词注入 handoff schema 说明（仅当任务带 `HandoffIn` 或请求 `ReportSchema` 时） |

**测试计划**：

- `subagent_handoff_test.go`：状态机三态 × 合法/非法字段组合；超限边界（=maxChars 合法、+1 拒绝）；防御解码的键集合变体（多键/少键/类型错）。
- `subagent_parent_summary_test.go` 补充：无效 handoff 降级路径不破坏既有回执。
- `subagentbatch` schema 迁移测试：老库（无列）→ 新库自动迁移，旧记录 handoff 为空。

**验收**：handoff 可在 writer→verifier 间传递并经状态机校验；无效输入永远显式报错不静默截断。

---

## 5. Phase 2：核心能力

### 5.1 实施项 P2-1：fork 型上下文继承

**问题**：子代理全新上下文，父级前期分析结论必须靠 prompt 转述；`docs/multi-agents` 甚至以"不 fork 会话前缀"为纪律。跨子任务共享前期上下文的 token 成本高且易失真。

**目标**：`spawn_agent` / `spawn_subagents` 任务包支持 `context: fork`——子会话以父会话**已完成轮次的一次性快照**作为初始历史。三条 DSH 约束全量移植：

1. **一次性快照**：fork 时刻冻结；此后父会话新产出不到达子级（无实时共享）。
2. **只继承对话**：不继承工具面、权限、写入能力（`resolveChildToolSurface` 照常独立执行）。
3. **KV Cache 保持**：fork 子级禁用 provider/model/reasoning 覆盖（强制继承父级路由），消息工具定义与父级逐字节一致——这是前缀复用的前提，也是 DSH "fork 不公开子级路由选择"决策的直接移植。

**设计**：

```go
// backend/internal/agent/scheduler.go 增量

// SubagentTask 新增：
//   Context string `json:"context,omitempty"`
//   // "" | "fresh" | "fork"。空值与 "fresh" 等价（现状）。
//   // "fork" 时：任务包不得携带 Provider/Model/ReasoningEffort 覆盖
//   //（连同 agentdef 解析出的路由覆盖一并拒绝），否则准入失败。

// backend/internal/agent/child_factory.go 增量
// Build 的 fork 分支：
//   1. parentSession 读取已完成轮次的"配平前缀"（与 DSH balanced completed-turn
//      prefix 语义一致：整轮为单位，进行中的轮次绝不包含）。
//      数据源：本仓库 session 存储的已有轮次消息（复用 sessionmeta / checkpoint
//      既有读取面，不新造查询）。
//   2. 快照以只读注入子会话初始历史；写入 AgentRecord 新字段
//      ForkParentSessionID + ForkUpToSeq（durable，供冷恢复与审计）。
//   3. 路由决策跳过 modelrouting.Resolver（decision = parent defaults），
//      RouteSource 记 "fork_inherit"。
```

**能力位联动**：`SubagentCapabilities.ForkContext` 在本实施项置 true；`CheckCapabilities` 拒绝 `WantsFork && !caps.ForkContext`。fork + 路由覆盖的组合在准入期直接拒绝（错误码 `SUBAGENT_CAPABILITY_UNSUPPORTED`，`next_action=drop_route_overrides_or_use_fresh`）。

**配平前缀的截断策略**：DSH 侧无上限（父级历史即边界），但 AAR 场景存在超长会话；补充 AAR 特有约束——快照超过 `agents.fork.maxSnapshotTokens`（默认对齐单轮预算）时：优先整体拒绝并建议 `fresh`；不静默截断历史（避免子级看到半截上下文却不知道）。这是对 DSH 设计的一个有意偏离，理由记录在测试注释中。

**测试计划**：

- `child_fork_context_test.go`：
  - 快照边界——父级进行中轮次不含、已完成整轮全含、首轮 fork 等价 fresh。
  - 冻结性——fork 后父级追加轮次不影响子级（用注入时钟 + 双会话并发写断言）。
  - 缓存前提——fork 子级的 provider/model 与父级一致；带路由覆盖的 fork 请求被准入拒绝。
  - 只读/工具面独立——fork 不改变 `resolveChildToolSurface` 结果。
- `agentcontrol` 持久化测试：`ForkParentSessionID/ForkUpToSeq` 落库与冷恢复重建。
- 真实终端验证：多轮对话后 fork 子级能正确复述父级前期结论（人工 runbook 项）。

**验收**：fork 子级在无需转述的情况下继承前期结论；snapshot 冻结与路由继承约束有测试钉死；配额（quota gate）与写者栅栏对 fork 子级照常生效。

### 5.2 实施项 P2-2：iterate_goal——有界全新上下文迭代

**问题**：`doom_loop.go` 只做**检测**（语义指纹重复告警/可选硬停），没有"换新上下文重新推进"的正向机制；planner 静态 DAG 不适合"逐步打磨同一目标"的迭代场景。

**目标**：新增 `iterate_goal` 工具（借鉴 DSH `ralph` 的固定脚本 + 结构化 handoff + 轮预算），与 doom_loop 组合为"检测→止损→换脑"闭环。

**设计**：

```go
// 工具名：iterate_goal（toolregistry 注册，Group: Orchestration, Delegation: true）
// 入参（对齐 DSH "model supplies data only"）：
//   objective  string  // 不可变目标（每轮原样传递）
//   max_rounds int     // 可选；<= 部署上限，超限报错
//
// 固定编排（部署所有，模型不可改循环/路由/schema/handoff 校验）：
//   for round := 1; round <= maxRounds; round++ {
//       child := spawn 全新子级（fresh 上下文 + iterategoal 角色 + handoff schema）
//       prompt := [目标、轮次进度、共享工作区为长期记忆、上一轮 handoff（或首轮占位）、
//                 输出要求]（文案移植 DSH RALPH_SCRIPT 语义，措辞本地化）
//       report := 等待子级完成并 DecodeHandoff(report, expected)
//       switch report.Status {
//       case complete: return {status: complete, rounds_started, report}
//       case blocked:  return {status: blocked, rounds_started, report}
//       case continue: prev = report
//       }
//   }
//   return {status: budget_limited, rounds_started: maxRounds, report: prev}
```

**与 DSH 的差异（有意偏离，注释记录理由）**：

| 项 | DSH | AAR |
|---|---|---|
| 默认轮数 | 256 | **16**（本地单机成本考虑；`agents.iterateGoal.maxRounds` 可调，硬上限 64） |
| 提供方 | 专用 fresh provider 校验 | 复用既有 spawn 路径 + `subagentquota.Gate`（每轮占 1 live 名额） |
| 状态归集 | workflow 引擎 | 复用 `SubagentBatchCoordinator` durable 语义（每轮 = 一个任务行），重启后 replay 续跑 |
| 预算联动 | maxRounds 即预算 | maxRounds + 每轮复用 `turn_budget`（steps/wall_clock/tokens 三维，轮级软水位事件照发） |

**与 doom_loop 闭环**：`doom_loop.go` 的 advisory 文案追加一句建议——"若在重复同一思路，可改用 iterate_goal 换全新上下文推进"。不改硬停逻辑（opt-in 语义保持）。

**子级角色默认**：`role_defaults.go` 新增 `iterategoal` 角色默认工具面（读为主 + 目标相关写），白名单经既有 `DefaultToolsForRole` 机制。

**测试计划**：

- `iterate_goal_test.go`（对齐 DSH `tool-ralph.spec.ts` 的状态机矩阵）：
  - 首轮 complete/blocked 直接返回；continue 链推进；轮数耗尽 budget-limited 且 rounds_started == maxRounds。
  - handoff 校验失败 → 该轮 round-failed，返回 lastReport（复用 P1-2 校验器）。
  - 模型试图传 max_rounds > 部署上限 → 稳定错误码 `ITERATE_ROUNDS_EXCEEDS_CEILING`（新错误码，errors/codes.go）。
  - quota 门禁：`Gate` 满时轮启动失败且整次调用失败（不静默跳轮）。
- durable 恢复：轮次中途 kill 进程 → 重启后 replay 续跑（复用 batch coordinator 既有测试基建）。
- 真实终端验证：给定一个可分轮推进的目标（如"逐步完善某文档"），观察多轮 handoff 交接与终态。

**验收**：iterate_goal 全状态机有测试覆盖；与 quota/turn_budget/doom_loop 联动成立；durable 恢复可用。

### 5.3 实施项 P2-3：结果防御解码（随 P2-2 落地）

P1-2 的 `DecodeHandoff` 已覆盖 handoff 通道；本项把同一纪律扩展到**子级最终文本报告**：

- `subagent_parent_summary.go` 渲染前对 summary 做归一化（非空、trim、长度上限 `agents.report.maxSummaryChars` 默认 4096），超限**显式报错并在回执中标记**，不静默截断。
- 错误消息携带实测值（对齐 DSH `oversized (N > max)` 风格），保证父级可据此决策（重派/要求摘要）。

测试并入 `subagent_parent_summary_test.go`；验收标准同 P1-2。

---

## 6. Phase 3：增强与观察项

### 6.1 P3-1：deployment-owned 参数分层（⑥）

把"编排类参数 = 部署上限 > 宿主覆盖 > 模型请求"固化为通用模式并文档化：

- 盘点现状：`agents.maxThreads`（quota gate）、`agents.capabilities.*`（P1-1）、`agents.iterateGoal.*`（P2-2）已符合该模式。
- 统一错误文案约定：`<param> <value> exceeds the deployment ceiling <limit>`，并全部携带 `next_action`。
- 产出：`docs/design/orchestration-parameter-layering.md`（短文），作为后续编排类工具的评审清单。

### 6.2 P3-2：工具描述动态措辞（⑨）

- 依赖：P2-1（fork）落地后才有措辞差异可表达。
- 实现：`spawn_subagents` / `subagent_spawn` 的 description 在宿主装配时按 `SubagentCapabilities`（及任务是否允许 fork）生成——fork 可用时注明"子级可继承父会话已完成轮次（一次性快照）"，不可用时注明"子级全新上下文，任务需自包含"。
- 测试：两种措辞的 schema 快照测试。

### 6.3 P3-3（观察项）：受限编排 DSL（⑤）与外部 Agent Provider（⑧）

- **受限编排 DSL**：观察 goja（Go 侧 ES5 解释器）+ 受限钩子（`agent/parallel/pipeline/phase`）的可行性；先决条件是 P1-2/P2-2 的 handoff 契约稳定。本阶段仅产出评估笔记，不实施。
- **外部 Agent Provider**：评估把外部 CLI（codex exec 等）经 `internal/acp` 消费面接入为"一次性子级"（叶子节点、不可 send_message）。复用 DSH 边界："外部条目是叶子"。本阶段仅评审，不实施。

---

## 7. 风险与回滚

| 风险 | 缓解 | 回滚 |
|---|---|---|
| fork 快照读放大会话存储 | 复用既有 checkpoint/sessionmeta 读取面；快照 tokens 上限硬拒 | `agents.capabilities.ForkContext=false` 一键回到 fresh 语义 |
| iterate_goal token 成本失控 | 默认 16 轮 + 硬上限 64 + 每轮 turn_budget + quota gate | 工具从 toolregistry 摘除注册行即不可见 |
| handoff 字段污染既有 payload | 全部可选、`omitempty`、老客户端忽略未知字段 | 停止写入即恢复；列保留无害 |
| 能力位拒绝破坏既有任务 | `DefaultCapabilities` 覆盖现状全部已支持项（零行为差异起步） | 错误码仅新增不改旧 |
| schema 迁移风险 | subagentbatch 新列可空 + 迁移测试（老库→新库） | 列冗余不阻塞回退 |

## 8. 验证与度量

- 每个 Phase 收口跑 `go test ./...`；多 agent 相关包（agent/agentcontrol/toolbroker/subagentbatch/subagentquota）测试全绿。
- 真实终端验证按 `docs/plan/multi-agent-real-terminal-validation-runbook.md`，Phase 2 新增两个场景：fork 上下文复述、iterate_goal 多轮交接。
- 观测指标（复用 usageanalytics/session analytics）：fork 子级的 prompt token 相对 fresh 基线的节省；iterate_goal 的轮数分布与 budget_limited 占比；handoff 校验失败率。

## 9. 实施顺序与依赖图

```
P1-1 能力位 ──┬──> P2-1 fork（需要 capabilities.ForkContext）
P1-2 handoff ─┼──> P2-2 iterate_goal（需要 handoff 校验器 + capabilities.OutputSchema）
              └──> P2-3 防御解码（复用 handoff 解码器）
P2-1 ──> P3-2 动态措辞
P1-1 ──> P3-1 参数分层文档（盘点即可启动）
P2-2 ──> P3-3 观察项（编排 DSL / 外部 provider 评估）
```

关键路径：P1-1 → P1-2 → P2-2（iterate_goal 是价值最高的单一交付）；P2-1（fork）可与 P2-2 并行推进。
