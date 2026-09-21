# 主 Agent 动态 Provider/Model 切换设计 — 可行性与合理性分析

> **保存日期**: 2026-09-21
> **目标**: 分析“在主 Agent 中动态切换 provider/model”的需求 — 即在每次 LLM 调用时，指示 LLM 预测下一个任务难度（easy/normal/hard/expert），然后本地 harness 根据难度切换下一次调用所用的 provider/model。
> **前提**: 项目中**已存在**一套完整的 task-difficulty → provider/model 路由机制，但**仅限于子 Agent (subagent) 派发**阶段。

---

## 0. 审查结论与修订摘要（v2.1, 2026-09-21）

> 本节记录对 v1 方案的**审查结果**与**遗留问题处置**。§1–§7 已按本节结论就地修订；§8 起为新增的落地规格（配置 / 工具契约 / 事件 / 测试）。

### 0.1 审查发现的关键缺陷（已修正）

| # | 缺陷 | 影响 | 修正 |
|---|---|---|---|
| **D1** | v1 只提议改 `requestProvider()` / `requestModel()`，**遗漏了 `resolvePromptPreflightProviderModel()`** | 🟡 **设计约束**（v2.1 降级，见 §0.1.1）。只有当 override 被实现成**旁路字段**时才会漏：`think()` 在构建请求**之前**先跑 `enforcePromptPreflightWithTools()`（`loop.go:1710`），预算来自 `resolvePromptPreflightProviderModel(runtime, agent, loopConfig)`（`loop.go:5795`），只读 `loopConfig` / `agent.config` | §4.1.1 改为**复写 `loop.config` 自身**（而非新增旁路字段）→ 三处消费者天然一致，D1 不成立 |
| **D2** | v1 用 `atomic.Pointer[RouteDecision]` 但示例中 `Load()` 调用了两次 | 🟡 已消解 | §4.1.1 取消 `atomic.Pointer`（无跨 goroutine 共享），D2 随之消失 |
| **D3** | v1 未定义 override 的**清除点** | 🟡 **结构性已解决**（v2.1 更正，见 §0.1.1）。`cloneLoopConfigForRun` 每 run 返回**结构体拷贝**，`runLoop`/`continueLoop` 每 run 新建 `ReActLoop` → 不存在跨 turn 泄漏面 | §4.1.5 改写为「**不要引入跨 run 共享的可变状态**」这一负向约束，而非新增清除代码 |
| **D4** | v1 优先级表述自相矛盾（既说 `/model` 优先于预测，又说预测 override 覆盖 `/model`） | 🟡 语义冲突，无法实现 | §3 关键决策重写为**基线 / 偏移**模型 |
| **D5** | v1 §5.2 自造 `activeContextWindowTokens` 字段 | 🟡 重复造轮子；仓库已有 per-provider/model 能力解析（`GetCapabilities().MaxContextTokens`、`modelCapabilities[].AutoCompactTokenLimit`） | 删除该字段。**无需任何替代**：preflight 读的就是被复写的 `loopConfig`，能力解析自动跟随 |
| **D10** | **v2 自身**：设计了一套新的 `atomic.Pointer` 旁路 override，未发现仓库**已有** `RunRouteOverride` → `cloneLoopConfigForRun` → per-run `ReActLoop` 全链路 | 🔴 **过度设计**（本表唯一由复核新发现的缺陷）。会把「新增机制 + 改 3 个消费者 + 加清除点」当成必要工作量，实际大部分已存在 | §0.1.1 + §4.1.1 重写：改为**复用既有链路 + 只补 2 处缺口** |
| **D6** | v1 §5.4 的 `max_consecutive_expensive_steps` 只有名字没有语义 | 🟡 无法实施 | §5.4 定义计数口径、触发动作与审计事件 |
| **D7** | v1 未定义预测工具的**契约与拦截规则**（是否消耗 step、能否单独调用） | 🟡 会导致循环空转或 step 预算被吞 | §9 给出完整契约 |
| **D8** | v1 对 `expert` 档未作说明（原始需求只提 easy/normal/hard） | 🟡 治理面扩大 | §9.3 定义：`expert` 为 **opt-in**，未配置时钳制为 `hard` |
| **D9** | v1 未声明**不持久化**边界 | 🟡 与既有“routing disabled 不应持久化 provider/model override”测试语义（`chat_actor_host_test.go:1514`）冲突风险 | §10.3 列为显式非目标 |

### 0.1.1 v2.1 复核更正：既有设施比 v1/v2 假设的更完整（D10）

本节是对 v2 的**自我更正**，结论会**缩小**实施范围。复核中逐条追证 route 的真实流向，发现仓库**已经存在**一条完整的 per-run 路由链路：

| 环节 | 既有实现 | 事实 |
|---|---|---|
| 路由载体 | `chat.RunRouteOverride{Provider, Model, ReasoningEffort}` | `internal/chat/commands.go:41`。注释明确“intentionally limited to LLM request routing fields and must not affect permission mode, tool approval, or workspace policy” |
| 注入点 | `SubmitPrompt.RouteOverride` → `startSessionRun(..., routeOverride, ...)` | `commands.go:27`、`actor.go:2578` |
| 落地 | `cloneLoopConfigForRun` → `cloneLoopConfigWithRouteOverride` | `actor.go:1732` / `actor.go:1700`。**结构体拷贝** `cfg = *base`，再把 `Provider`/`Model`/`ReasoningEffort` 写进**克隆体**（`actor.go:1718-1728`） |
| 循环构建 | `runLoop` / `continueLoop` 每 run `NewReActLoop(..., clone)` | `actor.go:1684` / `actor.go:1696` — **每次 run 新建 loop** |
| 既有生产者 | `runRouteOverrideFromRunMeta`、`chatRouteOverrideFromTaskExecutionRoute`、`localChatRunRouteOverrideFromTeamRoute` | `actor.go:1772`、`api/skills/session_runtime_support.go:104`、`chat_actor_registry.go:113` — **全部由 team/subagent 派发驱动** |

**由此得出三条会改变设计的结论：**

1. **D1 不成立（降级为设计约束）**。`resolvePromptPreflightProviderModel`（`loop.go:5795`）读的是 `loopConfig.Provider/Model`；`cloneLoopConfigWithRouteOverride` 写的正是同一个 `LoopReActConfig`。**只要 override 通过复写 `loopConfig` 落地，preflight 自动一致**。D1 只在「把 override 做成旁路字段」这一错误实现下才出现 —— 所以它是一条**实现约束**（“必须复写 loopConfig，不得旁路”），而不是一个待修 bug。
2. **D3 结构性已解决**。每 run 一次结构体拷贝 + 每 run 一个新 loop ⇒ **没有跨 turn 泄漏面**，不需要新增任何清除代码。真正需要守的是一条**负向约束**：不得引入跨 run 共享的可变状态（那才会把结构性的“无泄漏”重新变成“有泄漏”）。
3. **D5 无需替代方案**。preflight 读克隆后的 config，能力解析（`ResolveRuntimeModelCapability`、`GetCapabilities()`）自动跟随新 model。

**唯一真实缺口（这才是本需求的工作量）**：

| # | 缺口 | 说明 |
|---|---|---|
| **G1** | **没有“难度驱动”的生产者** | 既有三个生产者全部由 team/subagent 派发触发，没有任何一处由 LLM 难度预测触发 |
| **G2** | **`RunRouteOverride` 是 per-run 粒度，表达不了 per-step** | 需求是“下一次 LLM 调用换模型”，即 **turn 内**切换；而 `RunRouteOverride` 在 `runLoop` 之前就固定了。**必须在 loop 内部**再开一个 turn 内的可变路由点 |

> **D10 的教训**：v2 把「新增机制」当成了必要工作量，而实际是「复用既有链路 + 补 2 处缺口」。**先证伪“已有实现”再设计新机制**应作为本仓设计的默认动作。

### 0.2 结论是否改变？

**可行性结论不改变（仍为“高”），但改动面显著缩小。**

v2.1 复核后：`modelrouting.Resolver`、`LLMRuntime` per-request provider 解析、`ReActLoop` 的 per-step 请求构建三者齐备，**且 per-run 路由链路已存在**（§0.1.1）。

真正的功能改动面收敛为两点（另有一处**使能性收口**，见 §0.3 P0）：

1. **loop 内新增 turn 内路由复写**（G2）—— 且实现方式被 D1 约束为「复写 `loop.config`」，因此**不需要新增消费者改造**；
2. **难度预测 → 路由的生产者**（G1）—— 复用 `modelrouting.Resolver` 与 `RunRouteOverride` 的字段语义。

> v2 曾把改动面描述为「让 override 成为 loop 内唯一的 route 事实源（需改 3 类消费者 + 清除点 + 新字段）」。**该描述已作废**：3 类消费者本来就共用 `loop.config`，清除点是结构性的，字段无需新增。

### 0.3 实施建议（修订后）

1. **P0 改为“所有权收口”**（比 v2 更小）：只做 `NewReActLoop` 接管 `loopConfig` 所有权（§4.1.1），此时行为零变化。
2. **P1 做 loop 内复写**（G2）：难度预测 → 复写 `loop.config.Provider/Model/ReasoningEffort`。
3. **P2 做生产者接线**（G1）：预测工具契约（§9）+ 难度 → 路由解析（复用 `modelrouting`）。
4. **P3/P4 配置与治理**（§10）。

> 与 v2 的差异：v2 的 P0 是「引入 `activeRoute()` 单一事实源（纯重构）」。既然三类消费者本来就同源，**P0 只需保证 loop 独占 config**，工作量从“改 3 处 + 加字段”降为“1 处所有权收口”。

---

## 1. 现有机制概览（事实核对）

### 1.1 子 Agent 路由机制（已实现）

项目在以下模块中已实现了完整的“任务难度评级 → 模型路由”链路：

| 组件 | 文件 | 职责 |
|---|---|---|
| **配置 schema** | `backend/internal/agentconfig/config.go` (§652–680) | `AICLISubagentRoutingConfig` 提供 `Levels`（difficulty→profile）、`Roles`（role+difficulty→profile）、`Enabled`、`AllowExplicitProviderOverride` 等字段 |
| **路由解析器** | `backend/internal/modelrouting/resolver.go` | `Resolver.Resolve(ParentDefaults, TaskHint) → RouteDecision`，完成 difficulty 归一化、role override、level profile、explicit override、parent inherit 和 capability 校验 |
| **难度枚举** | `backend/internal/modelrouting/types.go` (§10–15) | `DifficultyEasy/Normal/Hard/Expert` 四档枚举 |
| **任务结构** | `backend/internal/agent/scheduler.go` (§33–60) | `SubagentTask` 已包含 `Difficulty`、`DifficultyRationale`、`Provider`、`Model`、`ReasoningEffort`、`RoutingSource`、`RouteWarnings` 等字段 |
| **子 Agent 工厂** | `backend/internal/agent/child_factory.go` | `ChildAgentFactory.Build()` 调用 `resolver.Resolve()`，将 `RouteDecision` 应用到 `childConfig.Provider/Model/ReasoningEffort` |
| **专家并发控制** | `backend/internal/agent/scheduler.go` (§374–384) | `acquireExpertSlot()` 按 difficulty=expert 进行并发限流 |

**关键事实**: 配置、解析器、枚举、任务结构、工厂应用 — 全套设施已齐备并有单元测试（`resolver_test.go`）。

### 1.2 主 Agent 循环（当前状态）

| 组件 | 文件 | 职责 |
|---|---|---|
| **ReActLoop** | `backend/internal/agent/loop.go` | 主循环 `run()` → `think()` → `act()` → observe 的 do-loop |
| **think()** | `loop.go` §1609–2371 | 构建 `LLMRequest`，调用 `loop.llmRuntime.Call()`，解析响应为 `thought` + `action` |
| **requestProvider()/requestModel()** | `loop.go` §290–312 | 每步读取 `loop.config.Provider/Model` →  fallbacks 到 `loop.agent.config.Provider/Model` |
| **LLMRequest** | `backend/internal/llm/runtime.go` §58–70 | 含 `Provider`、`Model`、`ReasoningEffort`、`Temperature`、`Thinking`、`Metadata` 字段 |
| **LLMRuntime.Call()** | `runtime.go` §581–671 | 按 `req.Provider` 解析 provider，调用 `provider.Call()` |

**关键事实**: 主循环的 provider/model 是 **静态的** — 由 `LoopReActConfig` 和 `Agent.Config` 在 turn 级别确定，**没有 per-step 动态切换机制**。

### 1.3 LLMRuntime 的 provider 解析机制

`LLMRuntime.Call()` 在 `resolveProviderForRequest()`（§347–381）中按如下优先级解析 provider：

1. `req.Provider`（显式指定）
2. model alias / 默认 provider
3. 路由规则（`r.router.Route`）
4. 单一注册 provider

这意味着 **LLMRequest.Provider 可以在每次调用时不同** — runtime 层面已天然支持 per-request provider 切换，只要 `LLMRequest.Provider` 被正确设置。

---

## 2. 需求理解：主 Agent 动态切换 = LLM 预测难度 → harness 切换 next call

需求的核心是：

1. **指令注入**: 在每次 LLM 调用时，告诉 LLM“预测下一个任务的难度”。
2. **预测解析**: 从 LLM 响应中提取难度 prediction (easy/normal/hard/expert)。
3. **动态路由**: harness 使用现有的 `modelrouting.Resolver`，将预测难度映射到 provider/model，应用于**下一次** LLM 调用。

### 2.1 与现有子 Agent 路由的本质区别

| 维度 | 子 Agent 路由 | 主 Agent 动态路由（本需求） |
|---|---|---|
| **切换时机** | 在 child agent 创建**之前**，静态解析一次 | 在**每次 LLM 调用之后**，动态解析，应用于下一次调用 |
| **难度来源** | 主 Agent 拆分任务时在 `spawn_subagents` 工具参数中指定 | **LLM 自己预测**“下一个任务”的难度 |
| **作用域** | 仅影响子 agent | 影响**主 agent**的后续 LLM 调用 |
| **调用颗粒度** | 一次创建 → 一次 LLM 流程 | 每个 think() step → 动态切换 |

---

## 3. 合理性分析 (Reasonableness)

> **📌 关键决策（已确认）**: 将动态切换定义为 **turn 内部的临时调整**（temporary per-turn override），而非永久改变主 Agent 的 provider/model。
> - 每次 turn 开始仍使用 `/model` 或 `aicli.chat` 的默认 provider/model。
> - 动态切换仅影响**当前 turn 内部的 steps**，turn 结束后自动清除，恢复默认。
> - 绕开了现有设计 §20.6 的约束（“route 不应应用到 parent 主模型”），因为这仅是 turn 内的临时 override，不是永久性的主模型变更。

**优先级模型（基线 / 偏移）** — 取代 v1 中“`/model` 优先 vs 预测优先”的矛盾表述：

| 层 | 来源 | 生效范围 | 可否被预测覆盖 |
|---|---|---|---|
| **基线（baseline）** | `/model` / `/provider` / `/reasoning_effort` / `aicli.chat` 配置 | 整个 turn | ❌ 不可（它是 offset 的参照系） |
| **偏移（offset）** | 难度预测 → `modelrouting.Resolver` | 当前 turn 内，自下一步起 | — |
| **兜底（default）** | `runtime.DefaultProvider()/DefaultModel()` | 基线为空时 | ❌ |

关键点：

- **`/model` 的最高优先级体现为“决定基线”**，而不是“禁止 turn 内偏移”。若 `/model` 优先到禁止偏移，本功能即不存在。
- **`/model` 的既有语义是“从下一个 turn 生效”**（`applyRuntimeModelSwitch`，`cmd/aicli/commands/chat_model_switch.go:171`），因此 turn 内偏移不会与它争抢同一层。
- **显式关闭途径**：`main_agent.routing.enabled=false`（全局）或 turn 级 pin（见 §10.5）。用户一旦关闭，偏移恒为空，行为与现状完全一致。
- **offset 只能收窄、不能突破**配置 allowlist（§10.2）。

### 3.1 ✅ 合理的部分

#### 3.1.1 复用现有路由基础设施
`modelrouting.Resolver`、`AICLISubagentRoutingConfig`、`RouteDecision` 三套设施**完全可以直接复用**。无需从零实现难度→provider/model 的映射。唯一需要的是将“预测的难度”作为 `TaskHint.Difficulty` 输入给 resolver。

#### 3.1.2 LLMRuntime 层面已支持 per-request provider 切换
正如 §1.3 所述，`LLMRuntime.Call()` 按 `req.Provider` 解析 provider。`think()` 方法在 §1745 构建 `LLMRequest` 时，`requestProvider()` 和 `requestModel()` 每步都会重新读取配置。这意味着如果我们在这两个方法之外增加一个**per-step override** 机制，理论上可以动态切换。

#### 3.1.3 与现有子 Agent 设计哲学一致
现有设计文档 `docs/plan/task-difficulty-model-routing-plan.md` §17 明确表达的设计哲学是：“**模型负责给任务评级和拆分，runtime 负责可信路由、能力校验、provider runtime 构造和审计记录**”。本需求延续了这一哲学，只是把评级对象从“子任务”扩展到“下一个任务的难度预测”。

#### 3.1.4 主 Agent do-loop 已有 per-step 结构
`loop.go` §667 的 do-loop `for step := 1; ...` 在每个 step 调用 `think()`。如果 `think()` 返回的 `action` 中能携带 difficulty prediction，那么在 do-loop 级别可以将其提取并存入 loop state，供下一次 `think()` 使用。

### 3.2 ⚠️ 存疑的部分

#### 3.2.1 “下一个任务”的语义不明
在子 Agent 路由中，每个子任务都有一个明确的 `goal`。但在**主 Agent 的对话循环**中，“下一个任务”是什么含义？

- **如果是下一个 tool call**: LLM 在当前 step 已经知道它要调用哪些工具，难度预测没有价值（应该预测当前步骤的难度）。
- **如果是下一个 turn / 用户下一轮输入**: LLM 无法预测用户的下一轮输入内容，预测难度是没有依据的。
- **如果是当前 step 应处的难度等级**: 这更像是“自我评级”，而不是“预测下一个任务难度”。

**结论**: 需求中的“预测下一个任务难度”语义需要澄清。更合理的解释可能是：**LLM 评估当前工作的整体难度等级**，然后 harness 根据这个等级选择合适的模型。

#### 3.2.2 预测 → 切换存在一次 step 延迟
LLM 在 step N 输出难度预测 → harness 解析 → 应用于 step N+1 的 LLM 调用。这意味着：

- step 0: 使用默认 provider/model。
- step N: LLM 完成工作并预测难度。
- step N+1: harness 才会根据预测切换 provider/model。

**问题**: LLM 完成 step N 的工作**之后**再切换模型，意味着**当前步骤的执行已经用了旧模型**。如果 LLM 预测“下一个任务很难”，但这个预测是在困难任务的**末尾**才给出的，切换到强模型的效果已经丧失。

**结论**: 这种“预测 → 延迟切换”模式在 ReAct 循环中效果有限，除非难度评级能够在**步骤开始前**就被确定。

**✅ 解决（v2）**: 用**两个互补机制**把延迟压到可接受范围，而不是假装它不存在。

| 机制 | 时机 | 作用 |
|---|---|---|
| **M1 — turn 开局定档** | `run()` 进入、step 1 之前 | 用**零额外 LLM 调用**的方式给出 turn 级初始难度：以 turn 基线（`/model` 对应的 level，或 `default_difficulty`）作为起点。**不做**额外的预分类 LLM 调用（避免每 turn 固定 +1 次请求） |
| **M2 — 步内上报，下一步生效** | step N 的响应解析 | LLM 在**察觉到难度变化时**调用 meta tool 上报；offset 从 step N+1 起生效 |
| **M3 — 升级重试（可选，`allow_escalation_retry`）** | step N 结束时 | 若上报为**升级**（更强模型）**且**该 step 未产生任何 tool call（说明模型在“卡住/不确定”状态），允许**用升级后的 route 重发当前 step 一次**，从而消除延迟 |

**M3 的边界（防止成本失控）**：

- 每个 step 最多重试 **1 次**；每个 turn 最多 **2 次**（可配 `max_escalation_retries_per_turn`）。
- 只在**升级**方向生效（降级重试没有收益）。
- 重试必须发事件 `main_agent.route_escalation_retry`（§10.2），使成本可审计。
- 重试使用的是**同一个 step 序号**，不推进 step 计数（否则会变相放宽 `MaxSteps`）。

**残余延迟的接受理由**：M2 的延迟（1 step）只发生在**难度变化的那一步**；而难度在 ReAct 循环中通常**稳定**（同一子目标内不变）。连续多步难度跳变属于异常，由 §5.4 的护栏兜住。

#### 3.2.3 预测的可信度和一致性
LLM 预测难度存在以下风险：

- **不稳定**: 同一任务在不同步可能预测不同难度。
- **自利性**: LLM 可能高估难度以获取更强模型，或低估以避免开销。
- **忘记预测**: LLM 可能在某些步骤忘记输出难度预测。
- **格式偏离**: 预测格式不稳定，解析可能失败。

现有设计文档 §19.4 已经识别了这些风险：“难度评级模型不可完全信任”，runtime 需要做归一化、缺省、钳制和审计。

#### 3.2.4 token 和成本开销
每一步 LLM 响应都需要额外输出难度预测，增加 token 消耗。对于 `easy` 任务，这种开销可能超过收益。

**→ v2 结论**: 改为**事件驱动上报**（只在难度变化时上报，§9.2），难度稳定阶段**零额外 token**；“未上报”是正常状态而非失败（§5.3）。成本护栏见 §5.4，收益量化见 §12.3 O5。

#### 3.2.5 与现有子 Agent 路由的 scope 渐进问题
现有路由的 `RouteDecision` 包含 provider/model/reasoning_effort/max_tokens/timeout/temperature。但主 Agent 循环的 `LoopReActConfig` 中的 `MaxTokens`、`Temperature`、`Thinking` 等是**turn 级别**的，不是 per-step 的。动态切换 provider/model 可能会带来**不一致的上下文窗口**（不同 model 有不同 context 长度），这会影响历史消息的 token 预算管理。

**→ v2 结论**: 已澄清 —— **无需新增 context 字段**（D5 废弃）。preflight 一旦读到 override，即自动跟随新 provider/model 的能力链（§5.2）；`RouteDecision` 必须**整体**应用，避免跨 provider 字段混用（§4.1.1）。

### 3.3 ❌ 不合理的部分

#### 3.3.1 “每次 LLM 调用都预测难度”的成本/收益不匹配
如果要求**每次** LLM 调用都输出难度预测，对于一个 10 step 的 ReAct 循环，这意味着 10 次额外的难度输出。多数步骤的难度并不需要动态切换 —— 例如 `easy` 步骤连续执行，只需要一次预测即可。

**更合理的做法**: **仅在任务开始时或任务边界**（如检测到需要升级模型时）才进行难度评级。

#### 3.3.2 主 Agent 不应被提示词“操控”模型选择
现有设计 §12.3 明确指出：“route provider 是否允许 provider group / failover group”和“route 是否应用到 parent 主模型 → 不应用。主模型继续由 /model 和 aicli.chat 管理”。

将难度预测从动到**主 Agent**本身，是一种从提示词层面绕过用户显式 `/model` 选择的行为，**存在治理风险**。

**结论**: 动态切换主 Agent provider/model 应该至少满足：
- 用户必须**显式启用**该功能（feature flag）。
- 预测难度只能在**配置允许的范围内**选择 provider/model，不能突破配置边界。
- 用户的 `/model` 显式设置应优先于难度预测。

---

## 4. 可行性分析 (Feasibility)

### 4.1 技术可行性：可以实现，但需要修改多个组件

#### 4.1.1 turn 内 route 复写机制（v2.1 重写，含 D10 更正）

> ⚠️ **v1 与 v2 的写法都已作废。**
> - v1 只改 `requestProvider()` / `requestModel()`：若 override 走旁路字段，preflight 会漏（D1）。
> - v2 提出新增 `atomic.Pointer[RouteDecision]` 旁路 + 改 3 类消费者 + 加清除点：**过度设计**（D10）。仓库已有 `RunRouteOverride` → `cloneLoopConfigForRun` → per-run `ReActLoop` 链路（§0.1.1），且三类消费者本来就共用 `loop.config`。

**核心洞察：`loop.config` 在 chat 路径上已经是 per-run 私有对象。**

```go
// actor.go:1700 — 每 run 一次结构体拷贝，返回全新对象
func cloneLoopConfigWithRouteOverride(base *agent.LoopReActConfig, routeOverride *RunRouteOverride) *agent.LoopReActConfig {
    cfg := agent.LoopReActConfig{ /* 默认值 */ }
    if base != nil {
        cfg = *base                                   // 结构体拷贝
        cfg.Thinking = runtimetypes.CloneThinkingConfig(base.Thinking)
    }
    if routeOverride != nil {                         // 复用既有字段语义
        if v := strings.TrimSpace(routeOverride.Provider); v != "" { cfg.Provider = v }
        if v := strings.TrimSpace(routeOverride.Model); v != "" { cfg.Model = v }
        if v := strings.TrimSpace(routeOverride.ReasoningEffort); v != "" { cfg.ReasoningEffort = v }
    }
    return &cfg
}
// actor.go:1684 — 每 run 新建 loop，接管这个私有 config
loop := agent.NewReActLoop(a.agent, a.llmRuntime, a.historyCheckpointLoopConfig(routeOverride, runMeta, session))
```

因此 **turn 内切换只需复写 `loop.config` 的三个路由字段**：

```go
// applyTurnRoute 由难度预测在 step 边界调用（G2）。
// 只复写路由三字段，不触碰 MaxSteps/Temperature 等无关配置。
func (loop *ReActLoop) applyTurnRoute(route modelrouting.RouteDecision) {
    if loop == nil || loop.config == nil {
        return
    }
    if v := strings.TrimSpace(route.Provider); v != "" {
        loop.config.Provider = v
    }
    if v := strings.TrimSpace(route.Model); v != "" {
        loop.config.Model = v
    }
    if v := strings.TrimSpace(route.ReasoningEffort); v != "" {
        loop.config.ReasoningEffort = v
    }
}
```

**为什么这样就够了（消费者逐一核对，无需任何改造）：**

| 消费者 | 位置 | 读取源 | 是否需要改 |
|---|---|---|---|
| `requestProvider()` | `loop.go:290` | `loop.config.Provider` → `agent.config.Provider` | ❌ 不需要 |
| `requestModel()` | `loop.go:302` | `loop.config.Model` → `agent.config.Model` | ❌ 不需要 |
| `resolvePromptPreflightProviderModel()` | `loop.go:5795` | `loopConfig.Provider/Model` → `agent.config.*` | ❌ 不需要（**D1 由此不成立**） |
| `resolvePromptPreflightBudget()` | `loop.go:5638` | 透传 `loopConfig` | ❌ 不需要 |

**P0 唯一必要改动：`NewReActLoop` 接管所有权。**

`NewReActLoop`（`loop.go:187`）当前直接持有调用方传入的指针并**就地改写**（`config.MaxSteps = NormalizeMaxSteps(...)`，`loop.go:191`）。若调用方复用同一 config（`RunReActWithConfig`，`agent.go:991`，公开 API），turn 内复写会**泄漏到调用方**。因此：

```go
func NewReActLoop(agent *Agent, llmRuntime *llm.LLMRuntime, config *LoopReActConfig) *ReActLoop {
    if config == nil {
        config = DefaultLoopReActConfig()
    } else {
        owned := *config                                  // 接管所有权，不再改写调用方对象
        owned.Thinking = runtimetypes.CloneThinkingConfig(config.Thinking)
        config = &owned
    }
    config.MaxSteps = NormalizeMaxSteps(config.MaxSteps)
    // ... 其余不变 ...
}
```

> 这同时修掉一个**既有隐患**：当前 `NewReActLoop` 会就地改写调用方的 config（`NormalizeMaxSteps`），`RunReActWithConfig` 的调用方可能观察到意外变化。P0 属**纯重构**：`Provider/Model/ReasoningEffort` 未被复写时行为完全不变。

**必须按整体应用（三字段同源）的理由**：`RunRouteOverride`（`commands.go:41`）与 `AICLISubagentRouteProfile`（`agentconfig/config.go:672`）都把 Provider/Model/ReasoningEffort 作为**一组**。若只覆盖 provider 而 model 仍来自 config，会出现**跨 provider 的 model 名错配**（把 OpenAI 的 model 名发给 Anthropic）。`cloneLoopConfigWithRouteOverride` 已是「三字段一起覆盖」的既有范式，应保持一致。

**一致性论证（为什么 preflight 与请求构建必然同源）**：`think()` 内 `enforcePromptPreflightWithTools()`（`loop.go:1710`）与紧随其后的 `LLMRequest` 构建在**同一 step 内顺序执行**，两者都从 `loop.config` 取值；只要复写发生在**step 边界**（上一次 `act()` 结束、下一次 `think()` 之前），二者读到的就是同一份 route。

**难度**: ⭐⭐ 低 — 纯 Go 代码修改；且**改动面比 v2 描述的小**（P0 一处所有权收口 + P1 一个复写方法，无需改任何消费者）。

#### 4.1.2 难度预测的解析机制

LLM 响应解析在 `think()` §2320–2330：

```go
action.Content = response.Content
action.ToolCalls = response.ToolCalls
```

需要在此基础上增加难度预测的解析。**三个可选方案**:

| 方案 | 实现难度 | 可靠性 | token 开销 | 推荐 |
|---|---|---|---|---|
| **(A) 结构化 tool call** | 低 | 高 | 低 | ✅ 推荐 |
| **(B) 文本 JSON 抽取** | 中 | 中 | 低 | ⚠️ 可接受 |
| **(C) 文本正则提取** | 低 | 低 | 低 | ❌ 不推荐 |

**方案 A（推荐）**: 定义一个 `predict_task_difficulty` 工具（完整契约见 §9），LLM **在难度发生变化时**以 tool call 形式上报 difficulty，harness 在 `act()` 之前**拦截并剥离**该调用，因此它**不占用真实工具执行**、也**不消耗额外 step**。缺点是每次上报仍消耗少量输出 token —— 因此契约中明确“仅在难度变化时上报”，而非每 step 强制上报。

> **v1 的措辞“每个 step 结束时”已废弃**：它直接导致 §3.3.1 批评的“成本/收益不匹配”。修订后为**事件驱动**（难度变化时才上报）。

```json
{
  "name": "predict_task_difficulty",
  "arguments": {
    "difficulty": "hard",
    "rationale": "Cross-module refactoring with test verification"
  }
}
```

**方案 B**: 在 system prompt 中要求 LLM 在思考块末尾输出 JSON（如 `<difficulty>hard</difficulty>`），harness 用正则/解析器提取。

#### 4.1.3 难度 → provider/model 路由解析

直接复用现有 `modelrouting.Resolver`:

```go
func (loop *ReActLoop) resolveRouteForDifficulty(difficulty string) modelrouting.RouteDecision {
    resolver := modelrouting.Resolver{
        Config:  loop.routingConfig,  // 从 agent config 注入
        Catalog: modelrouting.NewRuntimeCatalog(loop.llmRuntime),
    }
    decision, _ := resolver.Resolve(
        modelrouting.ParentDefaults{
            Provider:        loop.agent.config.Provider,
            Model:           loop.agent.config.Model,
            ReasoningEffort: loop.config.ReasoningEffort,
            ...
        },
        modelrouting.TaskHint{
            Difficulty: difficulty,
        },
    )
    return decision
}
```

**难度**: ⭐ 低 — 直接复用，几乎零成本。

#### 4.1.4 配置接入

`LoopReActConfig` 当前没有 routing config 引用。需要将路由配置注入到 loop config 或 agent config 中（具体 schema 见 §10.1）。

**难度**: ⭐ 低 — 配置透传。

#### 4.1.5 turn 边界与基线还原机制（D3 修正）

**v2.1 重写：结论分两层 —— chat 路径结构性无泄漏；长生命周期 loop 需要「基线快照 + 入口还原」。**

**（1）chat 路径（本需求的目标路径）：结构性保证，无需任何清除代码。**

`runLoop` / `continueLoop`（`actor.go:1684` / `actor.go:1696`）**每次 run 新建 `ReActLoop`**，其 config 来自 `cloneLoopConfigForRun` 的**结构体拷贝**。loop 与 config 都是 per-run 对象，run 结束即被丢弃 ⇒ **不存在跨 turn 泄漏面**（D3 由此降级，见 §0.1.1）。

**（2）长生命周期 loop：入口还原仍然必须保留。**

`ReActLoop` 的公开 `Run()` / `RunWithSession()`（`loop.go:315` / `loop.go:323`）可被同一调用方在**同一个 loop 对象上重复调用**（`run()` 同步执行，turn 身份来自 context：`loop.turnID = TurnIDFromContext(ctx)`，`loop.go:449`）。此时 `loop.config` 被跨 turn 复用，**turn 内复写若不还原就会泄漏到下一个 turn**。

**（3）关键修正：就地复写会覆盖基线，因此必须先快照。**

v2 用 `atomic.Pointer` 且以 `nil` 表示“无偏移”，**不需要基线**。改为就地复写 `loop.config` 后，**基线被覆盖即丢失**，必须有还原来源：

```go
type ReActLoop struct {
    // ... 现有字段 ...
    // baselineRoute 是 loop 构造时的路由三字段，作为 turn 内复写的还原点。
    baselineRoute turnRoute
}

type turnRoute struct{ Provider, Model, ReasoningEffort string }

func (loop *ReActLoop) snapshotBaselineRoute() {
    if loop == nil || loop.config == nil {
        return
    }
    loop.baselineRoute = turnRoute{
        Provider:        loop.config.Provider,
        Model:           loop.config.Model,
        ReasoningEffort: loop.config.ReasoningEffort,
    }
}

// resetTurnRoute 把 loop.config 还原到基线（不是清空）。
func (loop *ReActLoop) resetTurnRoute() {
    if loop == nil || loop.config == nil {
        return
    }
    loop.config.Provider = loop.baselineRoute.Provider
    loop.config.Model = loop.baselineRoute.Model
    loop.config.ReasoningEffort = loop.baselineRoute.ReasoningEffort
}
```

`snapshotBaselineRoute()` 在 `NewReActLoop` 接管所有权之后调用一次（P0 的自然落点，§4.1.1）；`resetTurnRoute()` 在 `run()` 入口调用：

```go
// loop.go:438
func (loop *ReActLoop) run(ctx context.Context, prompt string, options loopRunOptions) (result *Result, runErr error) {
    // turn 开始：还原到基线，保证本 turn 从 /model 基线出发
    loop.resetTurnRoute()
    defer loop.resetTurnRoute()   // panic / early-return 也不泄漏

    loop.turnID = TurnIDFromContext(ctx)
    ...
}
```

**三条硬约束**：

1. **只在 `run()` 入口还原**，不要在 `think()` 里还原（否则复写无法跨 step 保持，功能失效）。
2. **还原而非清空**：基线已被就地覆盖，清空会退化成“无 provider/model”从而回退到 `agent.config`，**不等于** `cloneLoopConfigForRun` 注入的 route（例如 team/subagent 路由）。必须还原快照。
3. **绝不写入 session / 配置文件 / agent state**：偏移是 loop 内存态。会话恢复（resume）后自然回到基线。

**（4）基线语义与 `/model` 的关系**：`/model` 走 `ChatSession`（`applyRuntimeModelSwitch`，`chat_model_switch.go:171`），在**下一次 `buildLocalChatLoopConfig`**（`chat_actor_host.go:2170`）时进入新的 per-run config，因此它天然就是“下一个 turn 的基线”。这与 §3 的「基线 / 偏移」模型完全吻合：**用户意图构成基线，难度预测只做 turn 内偏移，偏移在 turn 结束时归还基线。**

**难度**: ⭐ 低 — 约 15 行；但缺了基线快照会产生“还原到错误基线”的隐性 bug，**比 v2 的跨 turn 泄漏更隐蔽**。

#### 4.1.6 前缀冻结不变式（新增硬约束，v2.1 复核）

**背景**：v2.1 曾把「prompt cache 失效」列为 O5 的未量化风险。复核请求构建链路后确认：**本地结构本来就是 provider 中立的，适配只发生在请求时**。该风险的性质需要重新表述，并升格为**硬约束**。

**事实核对**（本次实测，非推断）：

| 事实 | 位置 |
|---|---|
| 本地工具结构 provider 中立（`Name`/`Description`/`Parameters`/`Metadata`） | `types.ToolDefinition` |
| 唯一适配点：请求构建时才按协议序列化 | `llm/provider_adapter_request.go:40` `buildProviderAdapterRequest()` |
| 工具序列化**只依赖 protocol，不依赖 model** | `llm/mcp_meta_tools_convert.go:23` `buildToolDefinitionsForProtocol(tools, protocol, includeMeta)` |
| 按协议分派 `convertNamedToolsTo{Codex,Anthropic,Gemini,OpenAI}` | 同上 `:45-54` |
| 消息适配**只作用于副本，不改 canonical history** | `providercompat/openai_default.go:41-46` 注释原文：“It copies changed messages and **never mutates canonical/runtime history**” |
| 已有成文原则：tool 定义属于 prompt-cache 前缀，禁止改写 | `llm/request_tools.go:91-94` |
| `RunRouteOverride` 只写 3 个字段，不碰 prompt/messages/tools | `chat/actor.go:1700-1730` |

**结论：需求方的前提成立，且代码已满足。** `cloneLoopConfigWithRouteOverride` 仅复写 `Provider`/`Model`/`ReasoningEffort`，**没有任何路径**会因路由决策改写 system prompt、消息列表或工具集合。

**但这不等于「切换零成本」**，必须区分三种情况：

| 切换类型 | 序列化前缀 | 上游 cache 复用 | 说明 |
|---|---|---|---|
| 同 provider 同 protocol 换 model | **字节级不变** | 受上游 cache 作用域（通常 provider×model）限制 | 工具序列化不依赖 model（见上表） |
| 同 provider 换 protocol | 变 | 否 | 四种 `convertNamedToolsTo*` 输出结构不同 |
| **跨 provider** | **必变** | **构造上不可能** | 三条独立原因，见下 |

> 因此跨 provider 切换的成本**不是「A 的 cache 被失效」**（A 的 cache 未被销毁，切回 A 且在 TTL 内仍可命中），而是 **「B 侧首次请求冷启动」**。§12.3 O5 的原始表述据此更正。

**「为什么跨 provider 无法复用」—— 三个独立原因（任一条都足以否决）**

> **常见误解**：既然本地 canonical 的 prompt/messages/tools 在切换时保持稳定（INV-1/INV-2 已保证，且适配只作用于副本），为什么缓存不能复用？
>
> **关键在于**：缓存的 key **不是我们的 canonical 结构**，而是「**provider 实际收到的字节**」+「**provider×model 身份**」。适配器是**纯函数，但不是恒等函数** —— `f_anthropic(canonical) ≠ f_openai(canonical)`。缓存建立在**输出侧**，不在输入侧。

| # | 原因 | 性质 | 说明 |
|---|---|---|---|
| 1 | **缓存是 provider 侧基础设施，物理上不跨 provider** | **归属问题（与字节无关）** | 即使把**完全相同**的字节发给 A 和 B，B 仍是 0 命中 —— B 从未见过那些字节。**这一条独立于序列化，单独就足以否决复用** |
| 2 | **协议级序列化分歧 → 字节必不同** | 字节问题 | Anthropic 把 leading system **提升为顶层 `system` 参数**（`anthropic.go:133-153`），`messages` 中不存在 system role；OpenAI 则保留为 `role:"system"` 消息。同一份 canonical 状态 → 两种字节序列 |
| 3 | **上游缓存通常按 model 分桶** | 作用域问题 | 因此「同 provider 换 model」即使前缀字节完全相同，也可能不命中（上表第 1 行） |

**那么「本地结构稳定」买到了什么**（不能因为跨 provider 不可复用就低估它）：

1. **把冷启动收窄到「身份变化」时**：若按朴素做法在 system prompt 里注入「你现在的 model 是 X」，则**同 provider 同 protocol 换 model 也会击穿前缀**。INV-1/INV-2 正是堵住这条自毁路径。
2. **成本上界清晰**：冷启动**每次切换一次**（而非每 step 一次）—— B 侧首次请求会**写入 B 自己的缓存**，此后在 B 上继续跑即为热命中。
3. **可归因、可复现**：`prompt_fingerprint`（cacheanalytics）在切换前后保持可比，O5 的量化才有基线。

> 一句话：**本地稳定性是「必要不充分」** —— 它保证我们这一侧不做任何破坏缓存的事，但缓存能否命中最终由**上游的归属与作用域语义**决定，不在我们控制范围内。

**不变式（新增，必须由测试守住）**：

- **INV-1** —— `applyTurnRoute()` **只允许**写 `loop.config.{Provider, Model, ReasoningEffort}`；**禁止**写任何 prompt / messages / tools 相关字段。
- **INV-2** —— 不得为路由目的增删、重排或改写 tool 定义集合，不得改写 system prompt 与消息列表。
- **INV-3** —— **不向 LLM 暴露「当前 provider/model」**（§12.1 **N9**，原 O4 已降级为非目标）。唯一窄例外：**升级重试**时可注入一条**行为指令**，且必须置于消息**尾部**、不得进入前缀。理由见下。
- **INV-4** —— 同 provider 内换 model 时，`BuildToolDefinitionsForRequestWithImageOptions` 的输出必须**字节稳定**（唯一例外见下）。

**INV-3 的完整理由（v2.1 更正：原 O4 已从「开放项」降级为「非目标」）**

O4 的原始动机是「不可见时 LLM 难以判断『是否已足够强』」。该动机**本身不成立** —— 「是否已足够强」**不该由 LLM 判断**：LLM 只输出**任务难度**这一观测量，`modelrouting.Resolver`（§4.1.3）才负责把难度映射为 provider/model。让观测量依赖策略输出，构成**观测污染**：

1. **闭环自激 → 振荡**：当前 model 弱 → LLM 报「相对很难」→ 升级 → model 强 → 报「相对不难」→ 降级 → 弱 → 报难 …… 形成振荡。每次振荡都伴随一次冷启动（§4.1.6 上表）与成本波动。
2. **放大已识别的自利性风险**：§3.2.3 已指出「LLM 可能高估难度以获取更强模型」。展示当前 model 等于**直接给出该博弈的标的**，把已识别的风险武器化。
3. **与 §3.3.2 直接冲突**：本方案明确「主 Agent 不应被提示词『操控』模型选择」。

**注意**：即便按「只放尾部、不进前缀」实现（前缀 cache 得以保住），上述**闭环与自利性问题依然存在**。因此正确的处置**不是「约束实现位置」，而是「不做」** —— 原 O4 的「建议先做只读展示并观察」是错误建议，已撤回。

**窄例外（必要，且不属于 O4）**：**升级重试**（§3.2.2 M3 / O2）时，若不告知，模型可能**再次上报 hard** 导致重复升级。解法是**一次性、事件驱动的行为指令**，而非展示策略状态：

| | 展示当前 model（**不做**，N9） | 升级重试指令（**要做**） |
|---|---|---|
| 注入内容 | **策略状态**（「你现在是 X」） | **行为指令**（「本次为升级重试，请直接完成该步骤，不要再次上报难度」） |
| 触发时机 | 每个 step 持续 | **仅**升级重试的那一次请求 |
| 对观测量的影响 | **污染**（难度退化为相对量） | **无**（不暴露策略状态） |
| 位置 | — | 消息尾部，**不进前缀** |

**同 provider 换 model 唯一可能破坏前缀的路径**：`filterOpenAIImageGenerateToolForCodexNative`（`codex_image_generation.go:70`）依赖 model + protocol + capabilities。若 model 切换改变了 image_generation 工具的过滤结果，tools 数组随之变化。该路径**仅存在于 Codex 协议且需 provider 级显式 opt-in**，默认关闭；由 R7 覆盖。

**难度**: ⭐ 低（不加代码，只加约束与回归测试）；但**违反 INV-3 的代价是收益假设归零**，故列为必须守住的红线。

#### 4.1.7 防振荡（迟滞）规则（新增，v2.1）

**背景**：INV-3（§4.1.6）切断了「LLM 看到 model → 报相对难度」这条自激路径。但**振荡风险并不因此消失**：§3.2.3 已指出「同一任务在不同步可能预测不同难度」。当任务难度**恰好落在档位边界**时，route 会在两个 model 之间反复横跳 —— 每次横跳都伴随一次冷启动（§4.1.6 上表）与成本波动。

**现有护栏不足以解决它**：§5.4 的 `consecutiveExpensiveSteps` + cost guard 是**成本刹车**（事后、单向、只防升级），不是**行为阻尼**（不防横跳）。

**规则（三条互补）**：

| 规则 | 语义 | 默认 | 理由 |
|---|---|---|---|
| **升级即时** | 上报更高难度 → 立即生效 | — | 安全方向：避免在难任务上卡住 |
| **降级迟滞** | 需**连续 N 个 step** 上报更低难度才降级 | `downgrade_confirm_steps: 3` | 抑制边界抖动 |
| **最小驻留** | 一次切换后，至少 **K 个 step** 内不再切换 | `min_dwell_steps: 2` | 抑制高频横跳 |

> **为什么是「非对称」迟滞**：两个方向的**误判代价不对称** ——
>
> | 方向 | 迟滞的代价（该切而没切） | 误切的代价（不该切而切了） |
> |---|---|---|
> | 升级 | 难任务上继续用弱模型 → 反复失败 / 卡死（**用户可感知**） | 多花一点钱 |
> | 降级 | 简单任务上多跑几步强模型 → 只多花钱（**用户无感**） | 难任务上退回弱模型 → 同左列 |
>
> 结论：**升级的迟滞代价 ≫ 降级的迟滞代价**，因此只对降级加迟滞。这与 §5.4「只拦升级」是同一风险偏好的两个侧面（宁可略贵，不要卡住）。

**三条规则治的不是同一种病**（这是它们不能互相替代的原因）：

| 规则 | 性质 | 依赖什么 | 保证什么 |
|---|---|---|---|
| 降级迟滞 | **软过滤**：过滤报告噪声 | 依赖 LLM **报了什么** | 单点抖动不会触发降级 |
| 最小驻留 | **硬上界**：给切换频率封顶 | 只依赖**时间**，与报告内容无关 | 任意报告序列下，切换频率 ≤ 1 次 / (K+1) step |

> 最小驻留的价值在于**对抗性鲁棒**：§3.2.3 已认定难度上报**不可全信**，而「连续 3 次」这类规则原则上可被精心构造的上报序列满足。只有与报告内容无关的驻留窗口能给出与 LLM 行为无关的硬上界。因此二者**必须同时存在**。

**优先级（必须固定，否则语义有歧义）**：`升级即时` > `最小驻留` > `降级迟滞`。
即：**驻留窗口只拦降级，不拦升级** —— 否则会在难任务上被驻留窗口卡住，正好破坏「升级即时」要保护的那个方向。

**具体走一遍**（`downgrade_confirm_steps: 3`、`min_dwell_steps: 2`）：

| step | 上报 | 切换前 route | 动作 | 切换后 route |
|---|---|---|---|---|
| 1 | normal | 基线 | 初始化 | normal |
| 2 | easy | normal | 降级计数 = 1 | normal |
| 3 | normal | normal | **计数清零** | normal |
| 4 | easy | normal | 降级计数 = 1 | normal |
| 5 | easy | normal | 降级计数 = 2 | normal |
| 6 | easy | normal | 计数 = 3 → **降级**；驻留计时归零 | easy |
| 7 | hard | easy | **升级即时**（穿透驻留窗口） | hard |

- step 2 / 4 的单点抖动**没有**触发切换 —— 降级迟滞的作用；
- step 3 清零，说明是**连续**确认而非**累计**（若为累计，step 2+4 会被误判为已确认）；
- step 7 的升级**穿透**了驻留窗口 —— 优先级规则的具体体现，也说明驻留只用于压降级抖动。

**实现形态**（loop 内存态，turn 级，随 turn 结束丢弃；与 §4.1.5 的 baseline/offset 模型一致）：

```go
// 三个 turn 级计数器
pendingDowngradeLevel string // 正在被确认的更低档位
pendingDowngradeCount int    // 已连续确认的步数
stepsSinceLastSwitch  int    // 距上次切换的步数

func (l *ReActLoop) maybeApplyRoute(reported Level) {
    cur := l.currentRouteLevel()

    switch {
    case rank(reported) > rank(cur): // 升级：即时，穿透驻留
        l.applyTurnRoute(reported)
        l.onSwitch()

    case rank(reported) == rank(cur): // 同档：no-op，但时间在走
        l.stepsSinceLastSwitch++

    default: // 降级：先过驻留，再过连续确认
        if l.stepsSinceLastSwitch < l.cfg.MinDwellSteps {
            l.stepsSinceLastSwitch++ // 驻留期内不计确认数
            return
        }
        if reported != l.pendingDowngradeLevel {
            l.pendingDowngradeLevel, l.pendingDowngradeCount = reported, 1
        } else {
            l.pendingDowngradeCount++
        }
        if l.pendingDowngradeCount >= l.cfg.DowngradeConfirmSteps {
            l.applyTurnRoute(reported)
            l.onSwitch()
        }
    }
}

// onSwitch: stepsSinceLastSwitch = 0; pendingDowngradeCount = 0; pendingDowngradeLevel = ""
```

> **待定实现细节（不阻塞 P0）**：驻留期内是否**累计**降级确认数（上例按「不计」处理，语义更简单、切换更少）。两种做法都能满足硬上界，实现时固定一种并写入 §11.1 单测。

**与 O2 / 升级重试的关系**：`allow_escalation_retry`（§3.2.2 M3）的「每 turn 最多 2 次」是**上限**；最小驻留是**节流**。二者不重复，需同时存在。

**与 INV-1 的一致性**：三条规则只影响**何时**调用 `applyTurnRoute()`，不改变**写什么**（仍只写三个字段），因此不触碰 §4.1.6 的前缀冻结不变式。

**难度**: ⭐ 低（loop 内计数器 + 三个配置项）；**不做则功能「能跑但会抖」**，且抖动成本直接落在 O5 要量化的那一项上。

### 4.2 非功能可行性

| 维度 | 评估 | 说明 |
|---|---|---|
| **实施复杂度** | ⭐⭐ 中 | 需修改 loop.go（`NewReActLoop` 所有权收口 + `applyTurnRoute()`/`resetTurnRoute()` + 响应解析），**不改** 三类 route 消费者，无需改动 LLMRuntime 核心 |
| **架构风险** | ⭐⭐ 中 | 对主 Agent 核心循环的修改需谨慎，测试覆盖率要求高 |
| **向后兼容性** | ⭐ 低 | 复写既有 `loop.config` 三字段（不新增 route 字段）+ feature flag，默认关闭即可保持原行为；唯一需验证的是 P0 所有权收口对 `RunReActWithConfig` 的可见影响（§12.3 O3） |
| **性能影响** | ⭐ 低 | 解析 overhead **只发生在难度变化时**（事件驱动，§3.2.1/§9.2）；难度稳定阶段零额外 token |
| **治理安全** | ⭐⭐⭐ 高 | 预测难度可能被 prompt injection 操控，必须通过配置 allowlist 约束 |

### 4.3 实施路线（修订后）

| 阶段 | 目标 | 关键修改 | 预计难度 |
|---|---|---|---|
| **P0** | **所有权收口**（结构性地基，先做） | `NewReActLoop` 接管 config 所有权（结构体拷贝 + `CloneThinkingConfig`）；`baselineRoute` 快照；`resetTurnRoute()` 接在 `run()` 入口 + `defer` | ⭐ |
| **P1** | turn 内复写路径（**G2**） | 新增 `applyTurnRoute()`；由难度预测在 step 边界调用（**含 §4.1.7 迟滞/驻留规则**），复写 `loop.config.Provider/Model/ReasoningEffort` | ⭐ |
| **P2** | 预测机制（**G1**） | 注入 system prompt 提示 + meta tool 契约与拦截（§9）+ 难度 → `modelrouting.Resolver.Resolve()` | ⭐⭐ |
| **P3** | 配置 & 开关 | 新增 `aicli.main_agent.routing` 配置节（§10.1），默认关闭 | ⭐ |
| **P4** | 治理 & 审计 | 事件 `main_agent.route_*`（§10.2）+ 成本护栏（§5.4） | ⭐⭐ |

> **与 v2 的差异（v2.1 更正）**：v2 的 P0 是「新增 `activeRouteOverride` + `activeRoute()` + 改造 3 类消费者 + 入口清除」。既然三类消费者本来就共用 `loop.config`（§0.1.1），P0 收敛为**一处所有权收口**，难度由 ⭐⭐ 降为 ⭐。
>
> **与 v1 的差异**：v1 把“解析 + 路由复用”放在 P0、“override 应用”放在 P1，这会先产生一个**写了没人读**的字段。修订后 P0 只做“所有权与基线”，P1 才写值 —— 每阶段结束都可独立验证且不改变默认行为。

---

## 5. 风险与缓解

### 5.1 风险：难度预测被操纵

**风险**: 坏 Actor 或 prompt injection 引导 LLM 输出 `expert` 难度，诱导使用昂贵模型。

**缓解**:
- 仅允许预测难度映射到**用户本地配置中显式定义的** provider/model，不能突破 allowlist。
- `allow_explicit_provider_override` 默认 `false`（现已实现）。
- 记录 `routing_source` 为 `predicted_difficulty`，便于审计。

### 5.2 风险：多模型 context 窗口不一致

**风险**: 不同难度等级映射到不同 model，不同 model 有不同 context 长度，可能导致历史消息被截断或请求超限。

**❌ v1 的缓解方案已废弃**：v1 提议给 `LoopReActConfig` 新增 `activeContextWindowTokens` override 字段。**这是重复造轮子** —— 仓库已有完整的 per-provider/model 能力解析链：

| 既有设施 | 位置 | 作用 |
|---|---|---|
| `provider.GetCapabilities().MaxContextTokens` | `loop.go:5678` | provider 级 context 上限 |
| `llm.ResolveRuntimeModelCapability(runtime, provider, model)` | `loop.go:5686` | 解析出 canonical provider/model + capability |
| `capability.MaxContextTokens` / `AutoCompactTokenLimit` / `AutoCompactRatio` | `loop.go:5695–5714` | 模型级上限与自动压缩阈值 |

**✅ 解决（v2.1 更正）**: 不需要任何新字段，**也不需要改动 `resolvePromptPreflightProviderModel()`**。该函数读的就是 `loopConfig.Provider/Model`（`loop.go:5799-5800`），而 turn 内复写写的正是同一个 `loop.config`（§4.1.1）—— 二者天然同源：

- `resolvePromptPreflightProvider()`（`loop.go:5827`）会自动取到**新 provider** 的 `MaxContextTokens`；
- `ResolveRuntimeModelCapability()` 会自动取到**新 model** 的 `AutoCompactTokenLimit`；
- 自动压缩、工具面压缩、preflight 拒绝阈值**全部自动跟随**新 route。

**必须补的验证**：`think()` 中 preflight（`loop.go:1710`）与请求构建（`loop.go:1745`）**顺序执行且读同一 route**（见 §4.1.1）。这是唯一需要新增的断言，建议加集成测试（§11.2 **I3**）。

**降级方向的安全性**：从大窗口模型切到小窗口模型时，preflight 会在同一 step 内先压缩历史再发请求，因此不会出现“请求超限”错误。反向（小→大）只是少压缩，无正确性问题。

### 5.3 风险：预测失效导致 fallback 混乱

**风险**: LLM 忘记预测，或预测格式错误，harness 无法解析。

**缓解（v2 明确化）**:

| 情形 | 处理 |
|---|---|
| **本 step 未上报**（最常见） | **保持当前 offset 不变**。不要回退到 `default_difficulty` —— 因为“未上报”是**正常状态**（契约要求只在难度变化时上报），把它当成失败会导致每步都在重置 |
| 上报了但**格式非法 / 难度值非法** | 忽略本次上报，保持当前 offset；发 warning `main_agent.route_prediction_invalid` |
| 上报了但 **provider/model 无法解析**（未注册 / 未启用 / capability 不支持） | 忽略本次上报，保持当前 offset；发 warning `main_agent.route_prediction_unresolvable`；**不要把整个 turn 的 routing 关掉** |
| 连续 `N`（默认 3）次**非法上报** | 关闭**本 turn** 的 routing（不是全局），发事件 `main_agent.route_disabled_for_turn`，回退到基线直到 turn 结束 |

> **v1 的错误**：“parser 失败 → 使用 default_difficulty”在“未上报”是正常状态的前提下会**每步重置 offset**，使功能事实上失效。修订后区分“未上报”与“非法上报”。

### 5.4 风险：token/cost 暴涨

**风险**: 预测难度持续为 `hard` 或 `expert`，导致所有步骤使用昂贵模型。

**缓解（v2 给出可实施语义）**:

`max_expert_concurrency` **不适用**于主 Agent —— 它是子 Agent 的**并发槽位**限流（`scheduler.go:374` `acquireExpertSlot()`），主 Agent 是单条串行循环，没有并发可言。**改用**：

```yaml
aicli:
  main_agent:
    routing:
      max_consecutive_expensive_steps: 6   # 默认 6，0 = 不限
      expensive_levels: [hard, expert]     # 哪些 level 算“昂贵”
```

**计数口径（必须精确，否则实现会漂移）**：

- **计数器**：`consecutiveExpensiveSteps`，loop 内存态，**随 turn 重置**（与 offset 同一生命周期，见 §4.1.5）。
- **+1 条件**：某个 step 实际使用的 route **与基线不同**（`loop.config` 被 `applyTurnRoute()` 复写过）且其 difficulty ∈ `expensive_levels`。
- **归零条件**：某个 step 使用的 route 等于基线（无偏移），或 difficulty ∉ `expensive_levels`。
- **触发动作**：计数达到阈值时，调用 **`resetTurnRoute()` 还原基线**（§4.1.5），发事件 `main_agent.route_cost_guard_tripped`（含 `consecutive_steps`、`provider`、`model`）。
- **不禁止再次升级**：还原后若 LLM 再次上报升级，允许重新生效，但计数器继续累加（避免“还原→立刻升级”无限循环）。可选更严策略 `cost_guard_mode: hard`（本 turn 内禁止再次升级）。
- **审计**：每个使用 offset 的 step 都发 `main_agent.route_applied`，含 token 用量，使成本可归因（§10.2）。

---

## 6. 与现有设计的冲突与澄清

### 6.1 现有计划文档的约束 → 已澄清并确认

`docs/plan/task-difficulty-model-routing-plan.md` §20.6 明确：

> **route 是否应用到 parent 主模型** → **不应用**。主模型继续由 `/model` 和 `aicli.chat` 管理。

**澄清（已确认）**: 将动态切换定义为 **turn 内部的临时调整**，而非永久改变主模型。

- 每次 turn 开始仍使用 `/model` 或 `aicli.chat` 设置的默认 provider/model。
- 动态切换仅影响**当前 turn 内部的 steps**，turn 结束后自动清除，恢复默认。
- **绕了** 现有约束：因为这仅是 turn 内的临时 override，不是永久性的主模型变更，符合“route 不应永久应用到 parent 主模型”的意图。

### 6.2 现有 plan §18.2 识别的缺失闭环

现有设计文档 §18.2 指出：

> **Provider 切换方案过重**: 原方案提出 `ChildRuntimeFactory`，但当前 `LLMRuntime` 本身已经支持按 `LLMRequest.Provider` 路由。MVP 不应先引入 runtime factory。

本需求**完全避免了这个问题** — 主 Agent 只有一个 `LLMRuntime`，切换 provider 仅需修改 `LLMRequest.Provider`，无需构造新的 runtime。这是本需求比子 Agent 路由**更简单**的地方。

---

## 7. 结论

### 7.1 总体评价（v2.1 复核后）

| 维度 | 评级 | 说明 |
|---|---|---|
| **合理性** | ⭐⭐⭐⭐☆ 4/5 | v1 评 3/5 是因为“每次调用都预测”语义不明 + 成本失衡。修订后改为**事件驱动上报 + turn 内偏移**，语义闭合，上调一档 |
| **可行性** | ⭐⭐⭐⭐⭐ 5/5 | v2.1 复核发现 per-run 路由链路**已存在**（§0.1.1）：三类消费者本来就共用 `loop.config`，清除点是结构性的，能力解析自动跟随。改动面收敛为「1 处所有权收口 + 1 个复写方法 + 1 处基线快照」 |
| **实施难度** | ⭐⭐☆☆☆ 2/5 | 代码修改集中在 `loop.go`，复用现有组件；无需新 runtime、无需新依赖 |
| **治理风险** | ⭐⭐⭐☆☆ 3/5 | 需要 feature flag + allowlist + 审计 + 成本护栏（§5.4/§10）。**默认关闭**下风险为 0 |

### 7.2 推荐的精化方案（v2.1 定稿）

1. **语义澄清**: 把“预测**下一个任务**难度”改为 **“上报**当前工作**的难度等级**”**（§3.2.1），用于当前 turn 后续 steps 的 route 选择。

2. **事件驱动而非每步预测**: LLM **只在难度变化时**上报（§9.1），未上报是正常状态（§5.3）。这消除了 v1 最大的成本问题。

3. **结构化上报**: 定义 `predict_task_difficulty` meta tool（§9），由 loop 在 `act()` 前**拦截剥离**，不占真实工具执行、不额外消耗 step。

4. **配置隔离（定稿）**: 新增独立配置节 `aicli.main_agent.routing`，**不复用** `aicli.subagents.routing`（理由见 §10.1），`enabled` 默认 `false`。

5. **审计与治理**:
   - 事件族 `main_agent.route_*`（§10.2），每次偏移都有可归因记录。
   - `allow_explicit_provider_override` 保持默认 `false`。
   - **`expert` 档为 opt-in**（§9.3），未显式配置时钳制为 `hard`。
   - 成本护栏 `max_consecutive_expensive_steps`（§5.4）。

6. **优先级定稿**: 采用**基线 / 偏移**模型（§3），取代 v1 的矛盾表述。

### 7.3 核心代码修改点（v2.1 修正版）

**改动面复核（依据 §0.1.1 证据表）**：`RunRouteOverride` → `cloneLoopConfigWithRouteOverride` → `cloneLoopConfigForRun` → per-run `ReActLoop` 链路**已存在**，且三类 route 消费者本来就共用 `loop.config`。故改动面**远小于 v2 的估计** —— 不需要新字段、不需要旁路解析入口、不需要改 3 处消费者：

```
backend/internal/agent/
  ├── loop.go          ← ① NewReActLoop：接管 config 所有权（结构体拷贝）      ★v2.1，P0
  │                      ② baselineRoute 快照 + snapshotBaselineRoute()        ★v2.1，P0
  │                      ③ resetTurnRoute()：run() 入口 + defer 还原基线       ★v2.1，P0
  │                      ④ applyTurnRoute()：step 边界复写 loop.config 三字段   P1
  │                      ⑤ think() 响应解析：拦截剥离 meta tool                 P2
  │                      ⑥ 成本护栏计数器（§5.4）                               P4
  │                      ❌ 不改 requestProvider() / requestModel()（已读 loop.config）
  │                      ❌ 不改 resolvePromptPreflightProviderModel()（已读 loop.config）
  └── agent.go         ← 注入 MainAgentRoutingConfig

backend/internal/chat/
  └── actor.go         ← 直接复用 cloneLoopConfigWithRouteOverride（无需改动）

backend/internal/agentconfig/
  └── config.go        ← 新增 AICLIMainAgentRoutingConfig（复用 AICLISubagentRouteProfile）

backend/internal/modelrouting/
  ├── resolver.go      ← 直接复用，无需改动
  └── types.go         ← 直接复用，无需改动

backend/internal/llm/
  └── runtime.go       ← 直接复用，LLMRuntime.Call() 已支持 per-request provider

configs/config.yaml    ← 新增 aicli.main_agent.routing 节（§10.1）
```

> **核心结论（v2.1）**: 需求技术上**高度可行**，且**既有基础设施已覆盖大半**。v1 的「几乎零成本改动」偏乐观（漏了 preflight 与 turn 边界）；v2 的「需新增单一事实源 + 改 4 类消费者」则**过度设计**（D10）。真实改动面是：**1 处所有权收口（P0）+ 1 个复写方法（P1）+ 1 个预测生产者（P2）**。
>
> **关键设计决策（已确认）**: 动态切换 = **turn 内部的临时调整**（基线/偏移模型，§3）。turn 从 `/model` 基线出发，预测只产生 turn 内偏移，turn 结束**还原基线** —— 在不违反现有 §20.6 约束的前提下实现动态切换，同时保持治理安全。
>
> **落地规格**: §9 预测工具契约、§10 配置与事件 schema、§11 测试计划、§12 非目标与遗留问题清单。

---

## 8. 落地规格总览（v2 新增）

§3–§7 解决了**设计与冲突**问题。§8–§12 补齐**可直接开工的规格**，覆盖 v1 缺失的工程细节：

| 章节 | 解决的问题 | 对应缺陷 |
|---|---|---|
| §9 预测工具契约 | 工具 schema、拦截剥离规则、`expert` 档位、system prompt | D7, D8 |
| §10 配置与事件 schema | 配置节结构、事件字段、与既有配置的关系、非目标 | D3, D9 |
| §11 测试计划 | 单测/集成/回归用例清单 | 全局 |
| §12 非目标与遗留问题 | 明确不做什么、剩余开放项 | D9 |

---

## 9. 预测工具契约（D7 / D8 修正）

### 9.1 工具定义

复用 `spawnSubagentsToolDefinition()`（`loop.go:6793`）的既有写法，新增一个**同名风格**的定义函数：

```go
func predictTaskDifficultyToolDefinition(cfg *AICLIMainAgentRoutingConfig) types.ToolDefinition {
    return types.ToolDefinition{
        Name: "predict_task_difficulty",
        Description: "Report the difficulty of the work you are currently doing so the local runtime " +
            "can select an appropriate provider/model for the NEXT step. Call this ONLY when the " +
            "difficulty level changes (e.g. moving from routine edits to deep architecture reasoning, " +
            "or back). Do NOT call it every step. If you never call it, the current routing is kept " +
            "unchanged, which is the normal case. This tool has no side effects and is not counted as " +
            "a real tool call.",
        Parameters: map[string]interface{}{
            "type": "object",
            "properties": map[string]interface{}{
                "difficulty": map[string]interface{}{
                    "type": "string",
                    "enum": cfg.AllowedLevels(), // 默认 ["easy","normal","hard"]；expert 需 opt-in（§9.3）
                    "description": "Difficulty level of the current work.",
                },
                "rationale": map[string]interface{}{
                    "type":        "string",
                    "description": "One short sentence explaining why this level applies. Used for audit only.",
                },
            },
            "required": []string{"difficulty"},
        },
    }
}
```

**关键点**:

- **`enum` 由配置动态生成**（`cfg.AllowedLevels()`），使 `expert` 的 opt-in 在 **schema 层**就生效 —— 未启用时 LLM 根本看不到该选项，无需依赖事后校验（比 §5.3 的“事后拒绝”更早一层）。
- 工具的**注册**只在 `cfg.Enabled == true` 时发生，默认关闭时该工具不出现在工具列表里（连 prompt 都不占）。

### 9.2 拦截与剥离规则（必须精确定义）

拦截点位于 `think()` 拿到响应后、进入 `act()` 之前，与既有内部工具分派（`loop.go:2638` 的 `tc.Name == "spawn_subagents"` 模式）**并列**：

```go
// 伪代码：think() 内部，响应解析阶段
var stripped []types.ToolCall
for _, tc := range resp.ToolCalls {
    switch tc.Name {
    case "predict_task_difficulty":
        loop.handlePredictTaskDifficulty(tc)   // 只更新 override，不产生 step
        stripped = append(stripped, tc)        // 从 ToolCalls 中移除
    default:
        // 保留
    }
}
resp.ToolCalls = remaining
```

**五条硬规则**:

| # | 规则 | 理由 |
|---|---|---|
| **R1** | 拦截后的 `ToolCall` **不写入对话历史**（或写入一条精简的 `[route reported: hard]` 占位） | 避免污染上下文；但完全静默会让 LLM 以为调用失败，**推荐占位方案** |
| **R2** | **不消耗 step**：`handlePredictTaskDifficulty` 只写内存态，不触发 `act()` 执行、不计入 tool budget | 否则“上报”会有代价，LLM 会倾向不报 |
| **R3** | **允许与真实工具并存**：同一个响应里既有 `predict_task_difficulty` 又有真实工具调用时，**两者都处理**（先处理上报，再执行真实工具） | LLM 自然会在同一次响应里“声明难度 + 继续干活” |
| **R4** | **meta-only 响应**（只有上报、没有真实工具、也没有最终回答）时，**立即继续下一轮 `think()`**，不返回用户可见输出 | 避免出现空 step；与 R2 一致 |
| **R5** | 上报**只影响 `step N+1`**（当前 step 已在执行中，不可改） | 与 §3.2.2 的“一步延迟”结论一致 |

> **R5 与 M3 的关系**: 默认不做“重试当前 step”。若启用 `allow_escalation_retry`（§3.2.2），升级类上报可在**同一 step 号**内重试一次，但仍受 1 次/step、2 次/turn 的硬上限约束。

### 9.3 难度档位与 `expert` opt-in（D8 修正）

| 档位 | 默认可用 | 说明 |
|---|---|---|
| `easy` | ✅ | 映射到轻量 provider/model |
| `normal` | ✅ | 映射到基线（通常等于 `default_difficulty` 对应的 route） |
| `hard` | ✅ | 映射到强模型 |
| `expert` | ❌ **需显式 opt-in** | 原始需求只要求 easy/normal/hard；`expert` 在子 Agent 语义里是“最贵的档位”（`scheduler.go:374` 的并发槽位限流对象），主 Agent 无对应限流设施，**默认不开放** |

**opt-in 方式**（二选一，推荐前者）:

```yaml
aicli:
  main_agent:
    routing:
      levels: [easy, normal, hard]        # 显式列出即锁定可选档位
      allow_expert: false                 # 或单独开关；true 时等价于把 expert 加进 levels
```

**启用 `expert` 时的强制配套**（缺一不可）:

1. §5.4 的 `max_consecutive_expensive_steps` **必须**为有限值（不接受 `0 = 不限`）；
2. `expert` 必须出现在 `main_agent.routing.profiles` 中（配置隔离，§10.1），否则视为**不可解析**（§5.3 的 `route_prediction_unresolvable`）；
3. 每次 `expert` 生效发**独立事件** `main_agent.route_applied` 且 `difficulty=expert`，便于成本审计告警。

### 9.4 System prompt 片段（建议文案）

只在 `cfg.Enabled == true` 时注入，插入位置与既有 routing 提示（子 Agent 的 difficulty 说明）保持一致：

```text
[routing]
Report the difficulty of your current work — treat it as a property of the work
itself, not of the model you happen to be running on.
If — and only if — that difficulty changes significantly, call
`predict_task_difficulty` to report the new level.
- Routine, mechanical, or well-understood work → easy
- Ordinary multi-step engineering work → normal
- Deep architecture, cross-system reasoning, tricky debugging → hard

Judge the work, not your situation:
- Rate the task as if any competent engineer were doing it. Do not raise the
  level to obtain a different model, and do not lower it to avoid scrutiny.
- Not calling this tool is normal and is the expected behavior on most steps.
  Reporting the same level again is a no-op and wastes tokens.
- This report does not change the user's configured default, is scoped to the
  current turn, and is discarded when the turn ends.
```

> **注意（v2.1 修订）**: 原文案含两句**泄露操纵杠杆**的表述 ——「Not calling it at all … keeps the current model」与「Your reported level affects the NEXT step's model」。它们明确告诉 LLM「报告会换模型」，直接诱发 §3.2.3 的自利性博弈，并与 §3.3.2「主 Agent 不应被提示词『操控』模型选择」冲突。修订后：
>
> - **保留**治理边界（不影响用户默认值 / turn 级作用域 / 结束即弃）—— 用于抑制越权尝试，且不泄露杠杆；
> - **删除**「报告 → 换模型」的因果披露（这是把策略状态交给被观测方，等价于 §12.1 N9 要避免的观测污染）；
> - **新增**反博弈条款：以「任何合格工程师来做都算多难」为基准，明确禁止为换取模型而抬高或压低档位。
>
> **配套硬约束**：本片段注入 system prompt，因此在 turn 内**必须保持稳定**（§4.1.6 INV-2）—— **不得因 route 切换而重写该片段**（例如插入「当前 model: X」）。这既是前缀冻结的要求，也是 N9 的要求。

---

## 10. 配置与事件 schema（D3 / D9 修正）

### 10.1 配置节 `aicli.main_agent.routing`

**为什么不复用 `aicli.subagents.routing`**（回应 §7.2 第 4 条）:

| 维度 | `aicli.subagents.routing` | 主 Agent 需要 |
|---|---|---|
| **生效对象** | 子 Agent（每个 child 一次静态解析） | 主 Agent（同一 loop 内**多次**动态解析） |
| **生命周期** | child session 级 | **turn 级**（§4.1.5） |
| **默认值** | 影响所有子 Agent 的模型选择 | **默认关闭**，不影响任何既有行为 |
| **字段语义** | `levels` / `roles` 是子 Agent 任务分类 | 需要 `max_consecutive_expensive_steps` 等**串行**护栏 |
| **回归风险** | —— | 复用会让「打开主 Agent 动态切换」意外改变子 Agent 路由 |

结论：**独立配置节**，两者互不干扰；`main_agent.routing.profiles` 复用 `AICLISubagentRouteProfile`（`agentconfig/config.go:672`）**类型**，但**不共享配置实例**。

```yaml
aicli:
  main_agent:
    routing:
      enabled: false                        # 默认关闭（治理要求，§7.1）
      levels: [easy, normal, hard]          # 可选档位；显式列出即锁定（§9.3）
      allow_expert: false                   # expert 需 opt-in（§9.3）
      default_difficulty: normal            # turn 起始基线档位（通常 = 无偏移）
      allow_escalation_retry: false         # M3 可选重试（§3.2.2）
      cost_guard_mode: soft                 # soft | hard（§5.4）
      max_consecutive_expensive_steps: 6    # 0 = 不限；allow_expert=true 时禁止为 0
      expensive_levels: [hard, expert]      # 哪些档位算“昂贵”（§5.4）
      max_invalid_reports_per_turn: 3       # 连续非法上报上限（§5.3）
      downgrade_confirm_steps: 3            # 降级迟滞：连续 N step 报更低难度才降级（§4.1.7）
      min_dwell_steps: 2                    # 最小驻留：一次切换后至少 K step 不再切换（§4.1.7）
      profiles:                             # difficulty -> route
        easy:
          provider: ""                      # 留空 = 沿用基线 provider
          model: ""
          reasoning_effort: low
        normal: {}                          # 空 = 完全沿用基线
        hard:
          reasoning_effort: high
          max_tokens: 0
```

```go
// backend/internal/agentconfig/config.go（新增）
type AICLIMainAgentRoutingConfig struct {
    Enabled                      bool                                `yaml:"enabled"`
    Levels                       []string                            `yaml:"levels"`
    AllowExpert                  bool                                `yaml:"allow_expert"`
    DefaultDifficulty            string                              `yaml:"default_difficulty"`
    AllowEscalationRetry         bool                                `yaml:"allow_escalation_retry"`
    CostGuardMode                string                              `yaml:"cost_guard_mode"`
    MaxConsecutiveExpensiveSteps int                                 `yaml:"max_consecutive_expensive_steps"`
    ExpensiveLevels              []string                            `yaml:"expensive_levels"`
    MaxInvalidReportsPerTurn     int                                 `yaml:"max_invalid_reports_per_turn"`
    DowngradeConfirmSteps        int                                 `yaml:"downgrade_confirm_steps"`
    MinDwellSteps                int                                 `yaml:"min_dwell_steps"`
    Profiles                     map[string]AICLISubagentRouteProfile `yaml:"profiles"`
}
```

**校验规则**（在配置加载期完成，fail-fast，不要留到运行期）:

| 规则 | 违反时 |
|---|---|
| `enabled=true` 且 `levels` 为空 | 报错：必须显式列出档位 |
| `levels` 含 `expert` 但 `allow_expert=false` | 报错（防止配置歧义，§9.3） |
| `allow_expert=true` 且 `max_consecutive_expensive_steps == 0` | 报错（§9.3 强制配套第 1 条） |
| `default_difficulty` ∉ `levels` | 报错 |
| `profiles` 的 key ∉ `levels` | 报错（避免死配置） |
| `expensive_levels` 含未在 `levels` 中的档位 | 警告并忽略该项 |
| `downgrade_confirm_steps < 1` 或 `min_dwell_steps < 0` | 报错（否则迟滞失效，§4.1.7） |
| `profiles[d].provider` 未在 runtime 注册 / 未启用 | **不报错**，运行期按 §5.3 视为 `unresolvable` 并忽略 |

> **最后一条是刻意的**：配置期无法可靠判断 runtime 可用性（可能延迟注册），因此降级为**运行期忽略 + warning**，避免因为一个 provider 未启用导致整个 aicli 启动失败。

### 10.2 事件 schema（`main_agent.route_*` 事件族）

沿用既有 `loop.emitRuntimeEvent(name, sessionID, toolName, payload)` 机制：

| 事件名 | 触发时机 | payload 关键字段 |
|---|---|---|
| `main_agent.route_applied` | 某个 step **实际使用**了非基线 route | `step`, `difficulty`, `source`(`predicted`/`baseline`), `provider`, `model`, `baseline_provider`, `baseline_model`, `rationale`, `input_tokens`, `output_tokens` |
| `main_agent.route_prediction_invalid` | 上报格式非法 / 难度值非法（§5.3） | `step`, `raw_difficulty`, `reason`, `consecutive_invalid` |
| `main_agent.route_prediction_unresolvable` | provider/model 无法解析（§5.3） | `step`, `difficulty`, `requested_provider`, `requested_model`, `reason` |
| `main_agent.route_disabled_for_turn` | 连续非法上报达阈值（§5.3） | `step`, `consecutive_invalid`, `turn_id` |
| `main_agent.route_cost_guard_tripped` | 昂贵档位连续达阈值（§5.4） | `step`, `consecutive_steps`, `difficulty`, `provider`, `model`, `cost_guard_mode` |
| `main_agent.route_cleared` | turn 结束**还原基线**（§4.1.5） | `turn_id`, `final_difficulty`, `steps_with_override`, `steps_total`, `restored_provider`, `restored_model` |

**为什么 `route_cleared` 必须存在**: 结构性上 turn 隔离由「per-run 新 loop + 结构体拷贝」保证（§0.1.1），**不再依赖该事件**。但 v2.1 引入的是**还原语义**（`resetTurnRoute()` 把三字段写回 `baselineRoute`，而非清空），这是一个**显式动作**，需要可观测证据 —— 事件里带上 `restored_provider`/`restored_model` 才能证明“确实还原到了正确基线”，而不是靠读代码相信。

**字段口径**:

- `source=baseline` 的 `route_applied` 也**照常发**（便于统计“本 turn 有多少 step 走了基线”），此时 `difficulty` 为 `default_difficulty`；
- `baseline_provider`/`baseline_model` 取**用户 `/model` 的显式值**（§3 基线定义），不是 `default_difficulty` 的映射结果；
- token 字段允许为 `0`（部分 provider 不回传用量），但**字段必须存在**，便于下游统一解析。

### 10.3 非目标：不做持久化（D9 修正）

| 不做的事 | 理由 |
|---|---|
| ❌ 不写回 session 的持久化 provider/model | 会破坏 `chat_actor_host_test.go:1514` 已有的「routing override 不得持久化」断言 |
| ❌ 不修改 `applyRuntimeModelSwitch`（`chat_model_switch.go:171`）的 next-turn 语义 | `/model` 是**用户意图**，优先级最高（§3），不应被自动机制改写 |
| ❌ 不新增 session DB 字段 / 不新增迁移 | 无状态变更，降低回滚成本 |
| ❌ 不跨 turn 记忆难度 | turn 是治理边界（§4.1.5）；跨 turn 记忆会让“临时调整”变成事实上的持久覆盖 |
| ❌ 不让子 Agent 继承主 Agent 的偏移 | 子 Agent 有自己的 `subagents.routing`，两者独立（§10.1） |

> **一句话边界**: 主 Agent 的动态 route **只存在于 `loop` 内存中、只存活于一个 turn 内、只在事件流里留痕**。任何形式的落盘都需要新开 RFC。

---

## 11. 测试计划

### 11.1 单元测试（`backend/internal/agent`）

| # | 用例 | 断言 | 覆盖 |
|---|---|---|---|
| **U1** | `NewReActLoop` **所有权收口** | 构造后修改 `loop.config` **不影响**调用方传入的 config（结构体拷贝；`MaxSteps` 规范化不外泄） | **P0** |
| **U2** | `snapshotBaselineRoute()` | `baselineRoute` == 构造时 `loop.config` 的三字段；之后复写 `loop.config` **不改变** `baselineRoute` | §4.1.5 |
| **U3** | `applyTurnRoute()` **整体应用** | 跨 provider 时 `provider`/`model`/`reasoning_effort` **三者同时**复写，**不会**出现新 provider + 旧 model 的混用 | §4.1.1 |
| **U4** | 三类消费者一致性（D1 约束） | 复写后 `requestProvider()` / `requestModel()` / `resolvePromptPreflightProviderModel()` 返回**同一** route —— 不需改代码，只验证「必须复写 `loop.config`」这一约束 | **D1** |
| **U5** | `resetTurnRoute()` **还原** | 复写后调用 `resetTurnRoute()`，三字段**逐字段等于 `baselineRoute`**（不是零值/空串） | D3 |
| **U6** | `run()` 入口 + `defer` | 上一 turn 残留偏移在 `run()` 入口被还原；提前 return / panic 路径下 `defer` 仍还原 | D3 |
| **U7** | 未上报难度 | **保持当前 offset 不变**（不重置为 `default_difficulty`） | §5.3 |
| **U8** | 非法难度值上报 | 忽略 + `route_prediction_invalid`；连续 3 次 → `route_disabled_for_turn` | §5.3 |
| **U9** | 不可解析 route 上报 | 忽略 + `route_prediction_unresolvable`；**turn 不被禁用** | §5.3 |
| **U10** | 成本护栏计数 | 昂贵档位 +1、基线归零、达阈值 `resetTurnRoute()` 还原基线 + 发事件、还原后允许再升级且计数继续累加 | §5.4 |
| **U11** | `expert` opt-in | `allow_expert=false` 时工具 schema 的 `enum` **不含** `expert` | D8 |
| **U12** | 配置校验 | §10.1 六条规则各自触发预期错误/警告 | §10.1 |

### 11.2 集成测试（loop 级 + mock provider）

| # | 用例 | 断言 |
|---|---|---|
| **I1** | 完整 turn：step 1 上报 `hard` | step 2 的 `LLMRequest` 使用 hard route 的 provider/model；事件流含 `route_applied` |
| **I2** | meta-only 响应（只有上报） | **不产生空 step、不消耗 step、不返回用户可见输出**，直接进入下一轮 `think()` |
| **I3** | preflight 与请求构建读**同一** route | 跨 provider（两 provider 的 `MaxContextTokens` 不同）时，`enforcePromptPreflightWithTools()`（`loop.go:1710`）与请求构建（`loop.go:1745`）使用**同一** route 的预算；**这是 §5.2 要求补的唯一断言** |
| **I4** | turn 结束 | `resetTurnRoute()` 还原三字段并发 `route_cleared`（含 `restored_*`）；**下一个 turn 的第一个 step 从基线开始** |
| **I5** | 同响应内「上报 + 真实工具」并存 | 上报被剥离、真实工具正常执行（§9.2 R3） |
| **I6** | `/model` 优先级 | turn 内用户切换 `/model` → 当前 turn 的偏移在结束时被还原；**下一个 turn 从新基线开始**（§3） |

### 11.3 回归 / 负向测试

| # | 用例 | 断言 |
|---|---|---|
| **R1** | `enabled=false`（**默认**） | 工具**不注册**；无任何 `main_agent.route_*` 事件；工具列表与现状**逐字节一致** |
| **R2** | 持久化边界 | 扩展 `chat_actor_host_test.go:1514`，确认主 Agent offset **不写入** session 持久化 provider/model（§10.3） |
| **R3** | 配置隔离 | 修改 `main_agent.routing` **不影响**子 Agent 路由（§10.1） |
| **R4** | 未启用 provider 出现在 `profiles` | 启动**不失败**；运行期按 `unresolvable` 忽略（§10.1 末条） |
| **R5** | `cost_guard_mode: hard` | 触发后**本 turn 内不再升级**（§5.4） |
| **R6** | 多 session 隔离 | 同一 loop 实例下，不同 session 的 offset 不互相污染（若存在共享实例） |
| **R7** | **前缀冻结不变式**（§4.1.6） | 同 provider 内跨难度切换 model 时，序列化后的 system prompt / 消息列表 / tool 定义**逐字节一致**；`applyTurnRoute()` 不产生任何 prompt 侧写入（INV-1/INV-2/INV-4） |
| **R8** | **不暴露当前 model**（§4.1.6 INV-3 / §12.1 N9） | 路由切换**不产生任何**向 LLM 暴露 provider/model 的注入；升级重试的窄例外**只注入行为指令**（非策略状态），且**不进前缀** |
| **R9** | **防振荡迟滞**（§4.1.7） | 降级需连续 N step 确认；`min_dwell_steps` 内不重复切换；边界难度反复上报时 route **不横跳** |

### 11.4 验证命令

```powershell
go test ./backend/internal/agent/... -run 'Route|Routing|Difficulty' -count=1
go test ./backend/internal/agentconfig/... -count=1
go build ./...
```

### 11.5 P0 完成定义（DoD）

P0（§4.3 的「所有权收口」）完成的判定标准 —— **不包含任何预测能力**：

1. `NewReActLoop` **接管 config 所有权**（结构体拷贝 + `CloneThinkingConfig`），不再就地改写调用方对象；
2. `baselineRoute` 快照在构造时写入（`snapshotBaselineRoute()`）；
3. `resetTurnRoute()` 在 `run()` 入口 + `defer` 调用，**还原到基线**（不是清空）；
4. `applyTurnRoute()` 存在但**无调用方**（P0 不接预测）⇒ `loop.config` 三字段恒等于基线；
5. U1–U6 + I3 + I4 全绿；
6. **R1 全绿**（默认关闭时行为零变化）。

> P0 可以**独立合入**：此时从未调用 `applyTurnRoute()`，`loop.config` 三字段恒等于基线，属**纯重构**，对外行为不变。这是把「高风险改动」拆成「可单独验证的重构 + 后置的功能」的关键。
>
> **P0 附带收益**：修掉 `NewReActLoop` 就地改写调用方 config 的既有隐患（`RunReActWithConfig`，`agent.go:991` 的调用方可能观察到 `MaxSteps` 被规范化）。

---

## 12. 非目标与遗留问题清单

### 12.1 非目标（明确不做）

| # | 不做 | 理由 |
|---|---|---|
| N1 | 不新增 session 持久化字段 / 迁移 | §10.3 |
| N2 | 不修改 `applyRuntimeModelSwitch` 的 next-turn 语义 | §3、§10.3 |
| N3 | 不让主 Agent offset 影响子 Agent | §10.1 |
| N4 | 不在 P0 阶段引入任何配置字段 | §4.3（P0 是纯重构，只做所有权收口与基线快照） |
| N5 | 不做跨 turn 难度记忆 | §4.1.5 |
| N6 | 不做「每个 step 强制预测」 | §3.2.1、§5.3（v1 方案已废弃） |
| N7 | 不引入新的 provider/model 解析组件 | §5.2（复用既有 capability 链） |
| N8 | 不默认开放 `expert` | §9.3 |
| N9 | **不向 LLM 暴露「当前 provider/model」**（观测污染 → 闭环自激/振荡） | §4.1.6 INV-3（完整论证）、§4.1.7 |

### 12.2 缺陷处置状态（D1–D12）

| # | 缺陷 | 状态 | 落点 |
|---|---|---|---|
| **D1** | preflight 未读 override | 🟡 **降级为设计约束**（v2.1） | §0.1.1、§4.1.1（**必须复写 `loop.config`，不得旁路**）、§5.2、§11.2 I3 |
| **D2** | `atomic.Pointer` 双重 `Load()` | ⚪ **已消解**（不再引入 `atomic.Pointer`） | §4.1.1 |
| **D3** | 无清除点 | 🟡 **结构性已解决 + 保留入口还原**（v2.1） | §0.1.1、§4.1.5（**基线快照 + 还原**）、§11.1 U5/U6、§11.2 I4 |
| **D4** | 优先级自相矛盾 | ✅ 已解决 | §3（基线/偏移模型）、§11.2 I6 |
| **D5** | 臆造 `activeContextWindowTokens` | ✅ 已废弃（**且无需替代**） | §5.2、§12.1 N7 |
| **D6** | `max_consecutive_expensive_steps` 无定义 | ✅ 已定义 | §5.4（计数口径）、§10.1（配置）、§11.1 U10 |
| **D7** | 无工具契约 | ✅ 已定义 | §9.1、§9.2（R1–R5）、§11.2 I2/I5 |
| **D8** | `expert` 档位未处理 | ✅ 已定义 | §9.3（opt-in + 强制配套）、§11.1 U11 |
| **D9** | 非持久化边界不明确 | ✅ 已明确 | §10.3、§11.3 R2、§12.1 N1–N3 |
| **D10** | **v2 自身过度设计**：未发现既有 per-run 路由链路，另起一套旁路 override | ✅ **已更正**（v2.1） | §0.1.1（证据表）、§4.1.1、§4.1.5、§4.3、§7.3 |
| **D11** | **O5 风险性质误述 + 缺少前缀冻结约束**：把「跨 provider 切换」描述为「cache 失效」，掩盖了真实成本（B 侧冷启动）；且未约束 O4 的实现位置，留下「注入 system prompt ⇒ 持续 miss」的陷阱 | ✅ **已更正**（v2.1） | §4.1.6（事实核对表 + INV-1~INV-4）、§11.3 R7/R8、§12.3 O4/O5 |
| **D12** | **O4 缺少合理性论证 + 缺防振荡机制**：把「让 LLM 看到当前 model」当作开放项（默认倾向"可做"），但其动机不成立且会引入闭环自激；方案亦无迟滞/驻留规则 | ✅ **已更正**（v2.1）：O4 **降级为非目标**（N9），并新增迟滞规则 | §4.1.6 INV-3、§4.1.7（防振荡）、§11.3 R8、§12.1 N9、§12.3 O4 |

### 12.3 仍待决策的开放项（不阻塞开工；O3 属 P0 验收、O5 是 P4 前置门槛）

> **完整性说明**：本表是「方案已完整」这一判断的**唯一保留项**。
>
> - **O1 / O2 / O4 / O6** —— 属「运行后再定」的参数或增强项，**不阻塞任何阶段开工**；
> - **O3** —— 必须在 **P0 内闭环**：它是 P0 的验收条件之一（§11.5），不是"以后再说"；
> - **O5** —— 是 **P4 的准入条件**：若量化结果显示 prompt cache 失效成本**超过**模型单价差带来的节省，则本方案的**核心收益假设不成立**，应回到 §3.3 重新评估是否继续，而不是继续推进 P4。
>
> 除上述三项外，方案的机制、契约、配置/事件 schema、测试计划、分阶段路线与 DoD 均已收敛，可开工。

| # | 开放问题 | 影响 | 建议 |
|---|---|---|---|
| **O1** | `expert` 是否最终开放 | 成本上限 | 先保持关闭；跑一段时间 `hard` 的成本数据后再决策（§9.3） |
| **O2** | `allow_escalation_retry` 是否默认开启 | 单 step 成本 +1 次 | 默认 `false`，观察「上报后仍失败」的实际频率再定（§3.2.2 M3） |
| **O3** | P0 的 `NewReActLoop` **所有权收口**是否影响 `RunReActWithConfig` 调用方 | 兼容性 | 收口后 `MaxSteps` 规范化**不再回写**调用方 config（§4.1.1）。**P0 内必须验证** `agent.go:991` 的调用方是否依赖该回写；若依赖，改为显式赋值 |
| **O4** | ~~是否让 LLM 看到「当前使用的 provider/model」~~ | — | ❌ **已决策：不做**（v2.1 降级为 §12.1 **N9**）。原动机「LLM 需判断是否已足够强」**不成立** —— 该判断属 harness 策略而非 LLM 观测量；展示会形成闭环自激与振荡，并武器化 §3.2.3 的自利性风险。完整论证见 §4.1.6 INV-3 |
| **O5** | 切换 provider/model 的 prompt cache 成本 | 实际收益 | **性质已更正**（§4.1.6）：不是「A 的 cache 被失效」，而是「**B 侧首次请求冷启动**」；跨 provider 前缀构造上不可能复用，同 provider 同 protocol 换 model 前缀**字节稳定**。**且已有度量设施**：`internal/cacheanalytics` 已采集 `cache_read_tokens` / `cached_tokens` / `cache_creation_tokens` / `usage_cache_hit_ratio` / `prompt_fingerprint`。P4 前用现有仪表做 A/B 即可，**无需新建埋点** |
| **O6** | `main_agent.route_*` 事件是否接入成本告警阈值 | 运维 | 建议接入，但独立于本方案（§10.2 字段已预留 token） |

> **O5 值得单独强调（性质已更正）**: 本方案的核心收益假设是「简单任务用便宜模型 → 省钱」。原表述「每次切换 provider 都可能使 prompt cache 失效」**不准确**：本地结构 provider 中立、适配只在请求时发生（§4.1.6），跨 provider 前缀本就无法复用（协议序列化不同），同 provider 同 protocol 换 model 前缀**字节稳定**。真实成本是 **「B 侧首次请求冷启动」**，且**已有 `cacheanalytics` 仪表可直接测量**。**在 P4 之前仍必须先量化这一项** —— 若冷启动 + 上游 cache 作用域（provider×model）导致的命中率下降，其成本超过模型单价差，则收益假设不成立。
>
> **但更该警惕的是 INV-3（§4.1.6）**：若把「当前 model」注入 system prompt（O4 的直觉做法），每次切换都会改写前缀，使切换后**持续** miss —— 这是**由实现方式引入**的、比 O5 本身更严重的风险。**O4 的实现位置不是风格问题，是收益红线。**

### 12.4 一句话总结

> v1 的**方向正确、可行性判断正确**，但存在 **3 个红色缺陷**（preflight 遗漏 / 双重 Load / 无清除点）与 **6 个黄色缺陷**（优先级矛盾、臆造字段、护栏未定义、工具契约缺失、expert 档位缺失、边界模糊），且**低估了改动面**。
>
> v2 已**全部处置**，v2.1 又对 v2 自身做了一次**证伪**（D10）：以「基线/偏移 + turn 内临时调整」为语义骨架，**复用**既有 `RunRouteOverride` → `cloneLoopConfigForRun` 链路（而非另造单一事实源），以 **P0 所有权收口**为第一步降低风险，并补齐了工具契约、配置 schema、事件 schema、测试计划与开放项清单。
>
> **建议的开工顺序**: P0（纯重构，可独立合入）→ P1（写路径）→ P2（预测 + §9 契约）→ P3（配置）→ P4（治理 + 成本护栏）。**在 P4 之前完成 O5 的成本量化**。
