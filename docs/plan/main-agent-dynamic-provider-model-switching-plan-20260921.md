# 主 Agent 动态 Provider/Model 切换实施方案

更新时间: 2026-09-21（方案）；2026-09-22（v3 实施回写，§15）；2026-09-22（v4：task_type 收编修订，见 §16）
状态: **partially-implemented** — MA-P0–MA-P4、MG5–MG8 与 **v4 task_type 收编（§5.4/§5.5/§6.2/§7 + §16）** 已实施并通过门禁（见 §15 与 §16 实施状态）
适用仓库: `E:\projects\ai\ai-agent-runtime`
上游依据: `docs/analysis/main-agent-dynamic-provider-model-switching.md`（v2.1，可行性论证）
姊妹方案: `docs/plan/task-difficulty-routing-audit-hardening-plan-20260921.md`（子 Agent 路径加固，共享事件契约）
审查方法: 静态代码走查（grep/view 复核上游锚点）+ 与 `docs/plan/` 既有方案覆盖比对（**无重叠**，见 §1.3）

---

## 1. 文档定位

### 1.1 本文件解决什么

上游分析（`docs/analysis/main-agent-dynamic-provider-model-switching.md` v2.1）已经完成了**可行性论证**：结论是「可行，且既有 per-run 路由链路可直接复用」。本文件把它转成**可开工的实施方案**：改动点、阶段划分、阶段门禁、测试计划、验收标准、开放项。

本文件**不重开可行性论证**。凡上游已收敛的结论（基线/偏移模型、前缀冻结不变式、反振荡迟滞、成本护栏口径），本文件只做**引用 + 落到具体符号**，不重新推导。

### 1.2 与「主设计」的关系：边界外延，不是修正

`docs/plan/task-difficulty-model-routing-plan.md` 是难度路由的**主设计**，其 §34.2 明确列出 P0 不做的事，其中三条与本文件直接相关：

| 主设计原文 | 位置 | 本文件的处理 |
| --- | --- | --- |
| 「不改变主 agent 自身 provider/model」 | `task-difficulty-model-routing-plan.md:1997` | 本文件**正是**要把这条边界**外延**到主 Agent；外延方式是「turn 内临时偏移 + turn 末还原」，**不改变**用户配置的主模型 |
| 「不允许主 agent 直接绕过本地策略指定任意 provider/model」 | `:2002` | **保留为硬约束**：预测工具只接受 `difficulty` 枚举，**不接受** provider/model 参数；provider/model 一律由本地 `modelrouting.Resolver` 决定（§5.8） |
| 「不接入 `spawn_agent` / `spawn_team`」 | `:1998-1999` | 本文件**不改**这两条；主 Agent 自身的偏移与子 Agent 路由**互不继承**（§6.4） |

> **因此本文件不构成对主设计的修正，而是其 §34.2 边界清单的一次受控放宽**——放宽范围严格限定为「主 Agent 自身、turn 内、可还原、默认关闭」。

### 1.3 与既有 123 份方案的覆盖比对

开工前已做覆盖比对，结论：**主 Agent 自身的动态 provider/model 切换在 `docs/plan/` 下无任何既有方案覆盖**。

| 既有方案 | 覆盖对象 | 与主 Agent 自身切换的关系 |
| --- | --- | --- |
| `task-difficulty-model-routing-plan.md` | 子 Agent（`spawn_subagents`） | 显式排除主 Agent（`:1997`） |
| `spawn-team-teammate-model-routing-plan.md` | `spawn_team` teammate | 同上，正交 |
| `task-difficulty-routing-audit-hardening-plan-20260921.md` | 子 Agent 路径的审计与安全网 | **正交**：它修的是「子 Agent 路由查不到账」，本文件要新建的是「主 Agent 路由**根本没有**」 |
| 其余 120 份 | 无 provider/model 路由语义 | 不相关 |

**唯一需要协同的不是「功能重叠」而是「契约共享」**：本文件新增的 `main_agent.route_*` 事件族必须走与姊妹方案 SA-G2 同一套契约注册与门禁机制，否则会**原样复现**姊妹方案正在修的那个缺陷（裸字面量发射点不在门禁覆盖内）。见 §6.2。

---

## 2. 相关文档

| 文档 | 与本文件的关系 |
| --- | --- |
| `docs/analysis/main-agent-dynamic-provider-model-switching.md` | **直接上游**。v2.1 可行性论证；本文件是其实施方案化 |
| `docs/plan/task-difficulty-model-routing-plan.md` | 主设计。本文件复用其 resolver 语义与难度归一化，并在 §34.2 边界上做受控外延（§1.2） |
| `docs/plan/task-difficulty-routing-audit-hardening-plan-20260921.md` | **姊妹方案**。共享事件契约注册面与门禁；实施顺序见 §8.1 |
| `docs/plan/sse-live-event-channel-optimization-plan.md` | A/B/C/D 通道模型与 `internal/events/contract.go` 注册表来源；本文件 §6.2 的修改落点 |
| `docs/plan/multi-agent-execution-optimization-plan.md` | 等待/唤醒/配额/可观测性主计划；本文件 §6.2 的事件需纳入其可观测面 |
| `docs/plan/session-analytics-subagent-reliability-implementation-plan.md` | 路由统计口径；本文件的 `main_agent.route_*` 应纳入同一统计面 |
| **在途特性：provider 健康 + 路由候选链**（`internal/providerhealth/`、`internal/modelrouting/candidates.go`） | **无设计文档**（§3.6 已核）。**本方案的最大外部变量**：它改变了 `Resolver.Resolve` 的签名与语义，本方案 §5.10 的耦合规则直接依赖它。**⚠️ 未提交**——见 §8.1 第 0 步与门禁 G-0 |
| `plan.md`（仓库根，2026-09-22） | **task_type 收编的评审与拍板来源**：5 项决策（`task_type` 替换路由层 `role` + 新增 `task_subject`、跨类降档 1 步确认、同步 `spawn_team` 与批次账本、修订本文件与姊妹方案、同步观测与两个前端）。本文件 v4 修订（§5.4/§5.5/§16）以它为准 |

### 2.1 编号约定（避免与姊妹方案冲突）

两份方案同处 `docs/plan/` 且都需要阶段编号，直接沿用 `P0/P1/...` 会产生**跨文件歧义**（同一个 `P0` 在两份文件里指两件不同的事）。本文件统一加作用域前缀：

| 前缀 | 含义 | 本文件中的用法 |
| --- | --- | --- |
| `MA-P0` … `MA-P4` | **M**ain **A**gent 阶段 | 本文件的实施阶段（§8） |
| `MG1` … `MG7` | **M**ain-agent **G**ap | 本文件的缺口清单（§4）；`MG5`–`MG7` 为 2026-09-21 第二轮新增（§14） |
| `A1` … `A16` | **A**cceptance | 本文件的验收标准（§11）；`A13`–`A16` 为 2026-09-21 第二轮新增（§11.2） |
| `SA-G*` / `SA-P*` | **S**ubagent **A**udit | 姊妹方案，**只引用不定义** |

**`R` 前缀存在三方冲突，已消歧（2026-09-21 第二轮）**：

| 前缀 | 含义 | 本文件中的用法 |
| --- | --- | --- |
| `R1` … `R9` | **R**isk | **§10 风险表专用**。跨节引用一律写「§10 R1」 |
| `REG1` … `REG12` | **REG**ression | §9.3 回归 / 负向测试。**本轮由 `R1`–`R12` 更名而来**（§14 第 14 项），以避开上面两个命名空间 |
| `R1` … `R5`（**上游**） | 上游主设计**五条硬规则** | §5.4 的落表（原文即为 `R1`–`R5`，为保持可追溯性**不更名**）。引用时一律带「§5.4」限定词，例如 `§5.4 规则 R3` |

> 姊妹方案在自身文件内仍写 `G1`/`P0`；本文件一律以 `SA-` 前缀引用它。该约定需在姊妹方案 §2 加一行反向说明（§12.2 改动清单最后一项）。

---

## 3. 事实基线与取证

> 本章只记录**本文件本轮新取证**的锚点（行号已复核）。上游分析的事实核对表不再重复，只标注差异。

### 3.1 取证结论一：主 Agent 动态切换**确实不存在**（先证伪）

按「先证伪『已有实现』再设计新机制」的纪律，逐条排查后确认该能力不存在：

| 排查项 | 证据 | 结论 |
| --- | --- | --- |
| resolver 的作用对象 | `modelrouting/resolver.go:11` — `// Resolve returns the effective **child-agent** route.` | 只服务子 Agent |
| 配置类型命名 | `agentconfig/config.go:667` — `AICLISubagentRoutingConfig`；`:695` — `AICLISubagentRouteProfile` | 无 main-agent 对应类型 |
| 难度的消费位置 | `agent/loop.go:6116` / `:6184` / `:6280` — 全部在 `spawn_subagents` 的参数解码路径 | 主 Agent 自身不消费难度 |
| 工具描述 | `agent/loop.go:6859` — 描述明确写 "runtime routing maps difficulty to local provider/model configuration" 且 enum 只挂在 `spawn_subagents` 的 items schema 上（`:6884`） | 难度是**子任务**属性 |
| per-run override 唯一形态 | `chat/commands.go:41` — `type RunRouteOverride struct`（Provider/Model/ReasoningEffort 三字段） | 无 per-step 表达能力 |
| override 的两个生产者 | `chat/actor.go:1772` — `runRouteOverrideFromRunMeta`，调用点 `:1017`、`:3858` | 均由 team run meta 驱动 |

### 3.2 取证结论二：**可复用的既有链路**（本方案的全部可行性来源）

主 Agent 的每次 run 已经会构造一个**全新的 `ReActLoop` 实例**，并且已经存在一条「外部 override → loop.config」的克隆链路：

```
chat/actor.go:1772   runRouteOverrideFromRunMeta(runMeta)   → *RunRouteOverride
chat/actor.go:1700   cloneLoopConfigWithRouteOverride(base, routeOverride)
chat/actor.go:1732   cloneLoopConfigForRun(base, routeOverride, runMeta)
chat/actor.go:1673   runLoop(...)      → agent.NewReActLoop(..., historyCheckpointLoopConfig(...))   ← :1684
chat/actor.go:1688   continueLoop(...) → agent.NewReActLoop(...)                                    ← :1696
```

**关键收益**：`loop.config` 的三个字段（Provider / Model / ReasoningEffort）已经是**单一事实源**，三个消费者都已读它：

| 消费者 | 位置 | 用途 |
| --- | --- | --- |
| `requestProvider()` | `agent/loop.go:291` | 实际请求的 provider |
| `requestModel()` | `agent/loop.go:303` | 实际请求的 model |
| `resolvePromptPreflightProviderModel()` | `agent/loop.go:5858` | preflight 的上下文预算口径 |

⇒ **只要在 step 边界复写 `loop.config` 三字段，三个消费者自动一致，无需改动任何消费者代码。** 这是上游 D1 缺陷「降级为设计约束」的原因，也是本方案风险可控的根本原因。

### 3.3 取证结论三：P0 要修的既有隐患是**真实存在**的

`agent/loop.go:188` 的 `NewReActLoop` 在 `:192` 就地改写调用方对象：

```go
func NewReActLoop(agent *Agent, llmRuntime *llm.LLMRuntime, config *LoopReActConfig) *ReActLoop {
    ...
    config.MaxSteps = NormalizeMaxSteps(config.MaxSteps)   // :192 ← 写回调用方
```

调用方 `chat/actor.go:1684` / `:1696` 传入的是 `historyCheckpointLoopConfig(...)` 的返回值（克隆产物），**当前无可见影响**；但 `agent/agent.go:991` 的 `RunReActWithConfig` 直接把**调用方传入的指针**转交，其调用方可观察到 `MaxSteps` 被规范化。这正是上游开放项 O3 要求「P0 内必须闭环」的原因——见 §7.1 与 §11 A1。

### 3.4 取证结论四：事件契约是**封闭注册表**，新增类型必须表态

`internal/events/contract.go:74` 的注释即规范：

> `// runtimeEventContracts 是封闭注册表：新增 runtime 事件类型必须在此表态。`

`runtimeEventContracts`（`contract.go:77`）按 A/B/C/D 四通道分区：

| 通道 | 常量 | 语义 |
| --- | --- | --- |
| A | `ChannelSessionStore`（`:30`） | 落盘到会话事件库 ⇒ 实时与回放都可见 |
| B | `ChannelLiveOnly`（`:32`） | 仅实时转发，刷新即丢 |
| C | `ChannelChatBridge`（`:34`） | chat SSE 帧桥 |
| D | `ChannelTailOnly`（`:36`） | 仅回合末尾巴补发 |

`ChannelsFor`（`:178`）对**未登记类型返回 0** ⇒ 不下帧、不落盘、不进尾巴帧。

**除 `contract.go` 外还有第二个注册面**：`internal/runtimeobserve/known_types.go`（产品已知类型目录，见其 `:162-179` 的 live-only / tail-only 清单）。两处都要登记，否则事件在「可观测性产品侧」不可见。

**本轮新增：MG3 不是「将来可能发生」，而是「此刻已经发生」**。工作区中存在**未提交**的在途改动（见 §3.6），它新增的 `llm.provider.health_opened` 事件在 `agent/loop.go:2186` **以裸字面量发射**，而 `internal/events/contract.go` 的注册表中**没有该类型**（全表检索 `health` / `provider.` 均无命中）。按 `ChannelsFor`（`:178`）语义，该事件**不落盘、不下帧、不进尾巴帧**——即在途特性的**唯一对外信号当前完全不可见**。

紧邻的 `llm.prompt_cache.breaker_tripped`（`agent/loop.go:2202`）**同样未登记**。两者都由本方案将要改动的同一个 `think()` 路径发射。

> **这条证据改变了 MG3 的性质**：它不再是「本方案新增事件时要记得登记」的流程提醒，而是**同一盲区已经在生产路径上造成实际不可观测**的既成事实。因此 §6.2 的门禁不是为将来的整洁，而是为**当下已泄漏的信号**补漏；且 `llm.provider.health_opened` 必须一并纳入登记（§7.6），否则主 Agent 的健康维度切换将没有任何证据链。

### 3.5 关键代码锚点（本轮复核后）

| 语义 | 位置 |
| --- | --- |
| loop 构造 / 所有权隐患 | `agent/loop.go:188`、`:192` |
| 三消费者 | `agent/loop.go:291`、`:303`、`:5858` |
| loop 主循环入口 | `agent/loop.go:439` |
| 事件发射器 | `agent/loop.go:1563` → `emitRuntimeEvent` |
| 思考 / 执行 | `agent/loop.go:1610` → `think()`；`:2437` → `act()` |
| preflight | `agent/loop.go:5176` → `enforcePromptPreflightWithTools` |
| 子代理工具分流 | `agent/loop.go:2701` → `tc.Name == "spawn_subagents"` |
| 子代理工具定义 | `agent/loop.go:6856` → `spawnSubagentsToolDefinition` |
| per-run 克隆链路 | `chat/actor.go:1700`、`:1732`、`:1772`、`:1673`、`:1688` |
| override 类型 | `chat/commands.go:41` |
| RunReAct 三入口 | `agent/agent.go:967`、`:991`、`:1013` |
| 事件契约注册表 | `internal/events/contract.go:77` |
| 产品已知类型目录 | `internal/runtimeobserve/known_types.go` |
| expert 并发闸门（子 Agent 侧） | `agent/scheduler.go:374` → `acquireExpertSlot` |

### 3.6 取证结论五：**在途未提交**的「provider 健康 + 候选链」特性（本方案的最大外部变量）

工作区存在一批**尚未提交**的改动，实现的是「provider 级动态健康门禁 + 路由候选链降级」。它与本方案**正交但强耦合**，必须先取证再设计。

**改动范围**（`git status` 复核）：

| 类别 | 文件 |
| --- | --- |
| 新增包 | `internal/providerhealth/`（`config.go` / `registry.go` / `registry_test.go`） |
| 新增文件 | `internal/modelrouting/candidates.go`、`candidates_test.go`、`health_test.go`、`internal/agentconfig/circuit_breaker.go` |
| 已改文件 | `agent/loop.go`、`agent/child_factory.go`、`modelrouting/resolver.go`、`modelrouting/types.go`、`modelrouting/validate.go`、`agentconfig/config.go`、`cmd/aicli/commands/chat_actor_registry.go`、`doctor_subagent_route.go`、`api/skills/session_runtime_support.go`、`runtimeserver/config_agent_route_preview.go` |

**关键语义变化（本方案必须对齐的五条）**：

| # | 变化 | 锚点 | 对本方案的影响 |
| --- | --- | --- | --- |
| 1 | `Resolver.Resolve` **新增 error 返回**：`(RouteDecision, error)` | `modelrouting/resolver.go:14` | §5.8 的解析流程**必须补 error 分支**（MG7） |
| 2 | 新增候选链耗尽错误 `errRouteHealthExhausted` | `candidates.go:92`、`:97`、`:185` | 主 Agent 无「父」可退 ⇒ 需重新定义降级目标（MG6） |
| 3 | 新增 `degradeToParentForHealth` + warning `route_health_exhausted_parent` | `resolver.go:243`、`:245`、`:246` | **「退到父 Agent」在主 Agent 语境下不成立**（MG6） |
| 4 | 新增来源 `SourceFailoverCandidate = "failover_candidate"` | `types.go:25`、`:27` | 必须并入 `route_applied` 的 `source` 枚举（§6.2） |
| 5 | `Resolver` 新增字段 `Health *providerhealth.Registry`；`RouteDecision` 新增 `Candidates []RouteCandidateEvaluation` | `types.go:115`、`:134` | 主 Agent 复用类型即**免费获得**可用性/缓存门禁与候选评估留痕 |

**profile 侧新增字段**（`AICLISubagentRouteProfile`）：`Availability`、`AvailabilityReason`、`PromptCache *bool`、`Candidates []AICLISubagentRouteCandidate`；**config 侧新增字段**：`Failover *bool`、`AvailabilityPolicy`、`RequirePromptCache`。

**健康状态是进程级共享的**：`providerhealth.Default()`（`agent/loop.go:2185` 调用点）是**包级单例**，主 Agent 与所有子 Agent **共享同一份健康观测**。这既是收益（健康知识跨路径复用），也是新的耦合面（§5.10）。

**在途特性自述的不变式，恰是本方案的冲突点**。`resolver.go` 明确写着：

> 候选链「**在此处恰好解析一次**，因此子 Agent 在其整个生命周期内保持单一 model，其 prompt cache 保持温热」。

**本方案 MA-P1 是逐 step 重解析 —— 直接违反该不变式。** 这是本轮修订的**核心发现**，展开为 §5.10 与 MG5。

> **文档缺口（附带发现）**：该在途特性在 `docs/plan/` 与 `docs/analysis/` 中**均无对应设计文档**（全目录检索无命中）。本方案因此无法引用其设计意图，只能引用代码。**建议其作者补一份方案文档**，否则本方案 §5.10 的耦合规则将建立在一份无设计依据的实现之上。

---

## 4. 缺口清单

严重度按「不修会不会出事」排序。**MG1/MG2 是上游已识别的功能缺口；MG3/MG4 是本文件本轮新发现的实施风险；MG5–MG7 来自 §3.6 的在途特性取证，是本轮修订新增的、优先级最高的一组。**

### MG1（高）主 Agent 无难度驱动的 route 生产者

**现象**：主 Agent 的 provider/model 只由两件事决定——用户 `/model`（基线）与 team run meta（per-run override，`actor.go:1772`，且被 `runMeta.Team.TeamID != "" && runMeta.Team.CurrentRunTaskID != ""` 守卫）。**没有任何「按任务难度自动切换」的通路。**

**证据**：§3.1 全表。

**影响**：用户原始需求（easy/normal/hard 自动切档）无法实现；且不存在任何降级/升级的可观测信号。

**修复方向**：§5.4（预测生产者）+ §5.8（难度→route 解析）。

### MG2（高）`RunRouteOverride` 是 per-run，无法表达 per-step

**现象**：`RunRouteOverride`（`chat/commands.go:41`）在 `NewReActLoop` **构造时**一次性注入，loop 生命周期内不可变。

**影响**：需求是「step N 上报难度 → **step N+1** 生效」，同一 turn 内**多次**切换。per-run 结构表达不了 per-step 语义。

**修复方向**：§5.3。**不新增并行的单一事实源**——改为让 loop 自己在 step 边界复写 `loop.config`（§3.2 的既有单一事实源）。

### MG3（中，本文件新发现）事件族未在契约注册表表态

**现象**：本方案要新增 `main_agent.route_applied` / `_cleared` 等 6 个事件类型（§6.2）。按 `contract.go:74` 的规范它们是**必须表态**的；若不登记，`ChannelsFor` 返回 0（`:178`），事件**不落盘、不下帧、不进尾巴帧**。

**为什么必须在本方案内解决而不是「以后再说」**：姊妹方案 SA-G2 的根因正是「生产者用裸字面量产出事件名，而门禁覆盖不到 `internal/agent` 里的裸字面量」。本方案的发射点在 `internal/agent/loop.go`（`emitRuntimeEvent`，`:1563`）——**与 SA-G2 是同一个盲区**。若只登记不修门禁，本方案上线即复现同一缺陷。

**修复方向**：§6.2。

### MG4（中，本文件新发现）P0 所有权收口会改变 `RunReActWithConfig` 调用方可见行为

**现象**：见 §3.3。收口后 `MaxSteps` 规范化**不再回写**调用方 config。

**影响**：`agent/agent.go:991` 的调用方若依赖该回写（例如读回 `loopConfig.MaxSteps` 做预算/展示），收口后会读到未规范化值。

**修复方向**：§7.1 第 4 项 —— **P0 内必须显式验证**；若确有依赖，改为显式赋值而非依赖副作用。这是上游 O3 的落点。

### MG5（高，本轮修订新增）逐 step 重解析 × 健康门禁 ⇒ **双切换源振荡**

**现象**：在途特性（§3.6）的候选链解析被设计为**每个子 Agent 生命周期恰好一次**，其自述理由是「保持单一 model、prompt cache 温热」。本方案 MA-P1 让主 Agent **每个 step 重新解析一次**。

**两个切换源会叠加**：

| 切换源 | 驱动量 | 本方案原设计 |
| --- | --- | --- |
| 难度偏移 | LLM 上报的 `difficulty` | ✅ 已用 `downgrade_confirm_steps` + `min_dwell_steps` 迟滞（§5.5） |
| **健康门禁** | `providerhealth` 状态机（healthy → open → half-open → recover） | ❌ **完全未考虑** |

**危害**：健康状态机的 **half-open 探测会周期性放行请求**，一旦探测失败又转回 open。若主 Agent 每 step 重新解析，则 provider 状态**每翻转一次就横跳一次 route**——而每次横跳都可能是**跨 provider 冷启动**（本方案 R1 风险）。这把 R1 从「每 turn 至多一次」放大到「**每 step 可能一次**」，**足以吃掉本方案的全部收益假设**。

**关键点**：§5.5 的迟滞**只覆盖难度维度，管不住健康维度**——因为健康门禁的切换发生在 `Resolver` 内部，绕过了 `applyTurnRoute()` 的迟滞状态机。**这是一个真实的架构漏洞，不是配置问题。**

**修复方向**：§5.10 第 1–2 条（健康维度必须在 turn 内**闩锁**，且不得绕过 `min_dwell_steps`）。

### MG6（高，本轮修订新增）`degradeToParentForHealth` 在**主 Agent 语境下语义不成立**

**现象**：在途特性在候选链被健康门禁全部拦下时，调用 `degradeToParentForHealth`（`resolver.go:243`）退回父 Agent 的 route，并打 warning `route_health_exhausted_parent`（`:245`）。其设计前提是「父 Agent 此刻正在运行，是当前唯一可证可用的目标」。

**为什么对主 Agent 不成立**：**主 Agent 没有父。** 该路径若被主 Agent 复用，要么退化为未定义行为，要么错误地退回一个并不存在的「父」。

**第二个混淆点**：主 Agent 语境下确实存在一个自然的降级目标——**基线 route**（用户 `/model` 的显式选择，§5.1）。但它与 `resetTurnRoute()`（清除难度偏移）**不是同一个动作**：

| 动作 | 语义 | 何时用 |
| --- | --- | --- |
| `resetTurnRoute()` | 清除**难度偏移**，回到基线 | turn 结束、成本护栏触发 |
| **健康降级到基线** | 因**候选链耗尽**而放弃偏移，回到基线 | `errRouteHealthExhausted` |

**两者结果相同（都回到基线）但原因不同、证据不同**，若共用同一事件与同一 `source`，事后审计将无法区分「正常收尾」与「健康熔断导致的降级」。

**修复方向**：§5.10 第 3 条 —— 主 Agent **不得**调用 `degradeToParentForHealth`；改为显式降级到基线并发射**独立来源**的 `route_applied`。

### MG7（中，本轮修订新增）§5.8 的解析流程缺 error 分支

**现象**：§5.8 的流程图为纯函数式四步（`difficulty → profiles → 沿用基线 → Resolver 校验 → 复写`），假定解析**不会失败**。在途特性已把 `Resolver.Resolve` 改为返回 `(RouteDecision, error)`（`resolver.go:14`）。

**影响**：按现方案实现会**编译失败**；即使补上 error 处理，若简单地把 error 当作 `unresolvable` 处理，会**掩盖 MG6 的候选链耗尽**——两者应走不同路径（`unresolvable` 是配置问题，候选链耗尽是可恢复的运行时状态）。

**修复方向**：§5.10 第 4 条 —— 区分 `unresolvable`（保持当前 route、turn 不禁用，§5.8 原文）与 `errRouteHealthExhausted`（降级到基线 + 独立来源）。

### MG8（高，v3 实施期新发现）配置可解析但**送不进 loop**

**现象**：`LoopReActConfig.MainAgentRouting` 是 loop 侧唯一入口，但宿主侧没有任何一处把 `aicli.main_agent.routing` 写进去。MA-P0..P4 全部落地后，`enabled=true` 仍然**零行为变化**。

**为什么这是最危险的失效形态**：配置加载期校验通过、`/config` 显示已启用、日志无 error、无 warning——**唯一缺失的是效果本身**。且由于 route 事件（§6.2）只在 loop 内发射，宿主漏接线时**连一条可用于归因的事件都没有**，排查只能靠读代码。

**修复方向**：§7.7（两条宿主路径 + 两条硬约束 + 两侧测试）。

**教训（写进验收口径）**：「配置项存在」与「配置项生效」之间必须有一条**可测试的接线段**；凡新增 loop 级配置，都要在宿主侧找到它的写入点，否则该配置在功能上等价于不存在。

### 4.1 已核实**不是**缺口（避免误报）

| 疑似缺口 | 核实结论 |
| --- | --- |
| 「preflight 读不到 override，跨 provider 会算错上下文预算」 | **不是缺口**。`resolvePromptPreflightProviderModel`（`loop.go:5858`）读的就是 `loop.config`，与 `requestProvider`/`requestModel` 同源。只要复写 `loop.config`（而非旁路），三者自动一致。上游 D1 因此「降级为设计约束」而非缺陷 |
| 「需要新增 provider/model 解析组件」 | **不需要**。`modelrouting.Resolver` + `NormalizeDifficulty` 已具备全部语义（`loop.go:6184` 已在用） |
| 「需要在 `chat` 包加 offset 状态」 | **不需要**。offset 只存在于 loop 内存；per-run 新 loop 天然保证 turn 隔离 |
| 「需要新 session DB 字段 / 迁移」 | **不需要**，且**明确禁止**（§6.4） |

---

## 5. 方案设计

### 5.1 语义骨架：基线 / 偏移

| 概念 | 定义 | 来源 |
| --- | --- | --- |
| **基线**（baseline） | 用户在 turn 开始时的显式 provider/model/reasoning_effort | `/model` 的用户意图；loop 构造时的 `loop.config` |
| **偏移**（offset） | turn 内由难度预测驱动的临时 route | 本方案新增 |
| **还原**（reset） | turn 结束时把三字段**写回基线** | 本方案新增 |

三条不变式：

1. **`/model` 优先级最高**：偏移**永不**改写用户配置；用户 turn 内切 `/model` → 该 turn 末偏移被还原，**下一 turn 从新基线开始**。
2. **turn 是治理边界**：偏移不跨 turn、不落盘、不进 session 持久化。
3. **偏移只影响 step N+1**：当前 step 已在执行中，不可改。

### 5.2 MA-P0：`NewReActLoop` 所有权收口（纯重构，零行为变化）

**目标**：让 loop **拥有**自己的 config，从而具备「在 step 边界安全复写三字段」的前提。

| 动作 | 说明 |
| --- | --- |
| 结构体拷贝 | `NewReActLoop` 内先 `cfg := *config`（值拷贝），再在 `cfg` 上做 `MaxSteps` 规范化，**不再写回 `config`** |
| 深拷贝嵌套 | 若 `LoopReActConfig` 含指针/切片字段（如 thinking 配置），用既有 `CloneThinkingConfig` 同款手法深拷贝，避免共享可变状态 |
| 基线快照 | 构造时 `loop.baselineRoute = snapshotBaselineRoute(cfg)`，记下三字段 |
| 还原入口 | `resetTurnRoute()`：把 `loop.config` 三字段**写回 `baselineRoute`**（是**还原**，不是清零） |
| 调用时机 | `run()`（`loop.go:439`）**入口**调用一次 + `defer` 再调用一次（覆盖提前 return / panic 路径） |
| `applyTurnRoute()` | 本阶段**只定义、无调用方** ⇒ `loop.config` 三字段恒等于基线 |

> **MA-P0 可独立合入**：此时从未调用 `applyTurnRoute()`，对外行为不变（除 MG4 需显式验证）。这是把高风险改动拆成「可单独验证的重构」+「后置功能」的关键。

### 5.3 MA-P1：应用点 —— `applyTurnRoute()`

在 **step 边界**（`think()` 返回后、下一次 `think()` 前）执行：

| 约束 | 说明 |
| --- | --- |
| **整体应用** | 跨 provider 时 Provider / Model / ReasoningEffort **三者同时**复写，禁止出现「新 provider + 旧 model」的混用 |
| **必须复写 `loop.config`** | 不得旁路、不得另建状态字段——这是 §3.2 三消费者自动一致的前提（上游 D1 约束） |
| **不触碰 prompt 侧** | 不注入、不重写 system prompt / 消息列表 / tool 定义（INV-1..INV-4，§5.6） |
| **不触碰持久化** | 不写 session、不写 config、不发 DB 变更 |

### 5.4 MA-P2：预测生产者 —— `predict_task_difficulty` 工具契约

**工具形态**：新增一个只读、无副作用的「上报」工具，参数为**难度 + 可选的任务类别与说明**（v4 修订，`plan.md` 决策 1）：

| 参数 | 类型 | 说明 |
| --- | --- | --- |
| `difficulty` | string enum | `easy` / `normal` / `hard`（`expert` 仅在 opt-in 时进入 enum，§5.9） |
| `task_type` | string enum | **可选**（v4 新增）。有限任务类别：`explore`/`understand`/`modify`/`implement`/`refactor`/`test`/`verify`/`migrate`/`security`/`config`/`integration`/`generate`（全表与底档见姊妹方案 §5.4 修订块）。未知值 → `task_type_unknown:<v>` warning + 忽略，不影响档位 |
| `task_subject` | string | **可选**（v4 新增）。短说明（如 `explore the files list`），截断后仅进事件/payload，不进 prompt、不参与映射；与 `rationale`（为何这个难度）语义不同、**并存不合并**（决策 2） |
| `rationale` | string | 可选，短理由；仅进事件，不进 prompt |

> **硬约束（主设计 `:2002`）**：该工具**不接受** provider/model 参数。LLM 只表达「多难」，本地 resolver 决定「用谁」——防止主 Agent 绕过本地策略指定任意 provider/model。

**五条硬规则**（上游 R1–R5，本文件落为实施口径）：

| # | 规则 | 实施口径 |
| --- | --- | --- |
| **R1** | 拦截后的 `ToolCall` **不污染上下文** | 写入一条精简占位（`[route reported: hard]`），而非完全静默——否则 LLM 会以为调用失败并重试 |
| **R2** | **不消耗 step** | 只写内存态，不触发 `act()`、不计入 tool budget；否则「上报有代价」⇒ LLM 倾向不报 |
| **R3** | **允许与真实工具并存** | 同一响应内既有上报又有真实工具时**两者都处理**：先处理上报，再执行真实工具 |
| **R4** | **meta-only 响应立即续轮** | 只有上报、无真实工具、无最终回答时，直接进入下一轮 `think()`，不产生空 step、不返回用户可见输出 |
| **R5** | 上报**只影响 step N+1** | 与 §5.1 不变式 3 一致；默认**不**重试当前 step |

**注入位置**：与既有 routing 提示（子 Agent 的 difficulty 说明）保持一致；仅在 `enabled=true` 时注入。

**提示词文案的两条修订（上游 v2.1 结论，必须遵守）**：

1. **删除**「报告 → 换模型」的因果披露。原文案中「Not calling it … keeps the current model」「Your reported level affects the NEXT step's model」会**直接诱发自利性博弈**（LLM 为换模型而抬档），且把策略状态交给被观测方。
2. **保留**治理边界表述（不影响用户默认值 / turn 级作用域 / 结束即弃），并**新增反博弈条款**：以「任何合格工程师来做都算多难」为基准，明确禁止为换取模型而抬高或压低档位。

**前缀稳定性**：该片段注入 system prompt，因此 turn 内**必须保持稳定**——**不得因 route 切换而重写**（例如插入「当前 model: X」）。这既是前缀冻结要求，也是 INV-3 要求。

**实施补充（v3，2026-09-22）——注入通道与片段规格**：

| 维度 | 实施口径 |
| --- | --- |
| 通道 | 复用既有 system-reminder 通道（`ReminderKindMainAgentRouting = "main_agent_routing"` + `FormatSystemReminder`），**不新增**旁路通道 |
| 注入位置 | `run()` 入口、首个 `think()` **之前**一次性写入本回合请求历史（紧随 `WithTurnSystemMessages` 注入点之后） |
| 持久化 | `MetaSystemReminderDurable=false` + `MetaEphemeralInstruction=true` ⇒ 落盘前被 `DurableMessagesForPersist` 剥掉：不改写任何已发送消息（INV-1），也不跨 turn 累积重复片段 |
| 片段内容 | 档位清单（`mainAgentAllowedLevels`，排序后）+ **有限 `task_type` 列表与各类别风险倾向的纯文字说明**（v4 新增，决策 1；**不含任何「类别→model/provider」映射**，INV-3）+ 未上报时的默认档 + 反博弈条款；**无** provider/model，**无**「上报会换模型」因果披露（本节两条修订） |
| 关闭态 | `nil` 或 `enabled=false` ⇒ 不产出消息，请求历史**逐字节不变**（REG1） |
| turn 内稳定性 | 片段只依赖配置、**不随 route 切换变化** ⇒ 满足 INV-2/INV-4（U18 断言） |

> **为什么走 system-reminder 而不是拼接 system prompt 文本**：拼接会改写既有 system prompt（违反 INV-1/INV-2），且在 turn 中途重算时会产生漂移；reminder 通道是既有的「回合级、可剥离」机制，语义与生命周期天然对齐。

### 5.5 MA-P1 配套：防振荡迟滞

纯「按上报立即切换」会在边界难度反复上报时导致 route **横跳**，每次横跳都可能触发 provider 侧冷启动。规则（v4 修订：加入类别感知，`plan.md` 决策 3）：

| 规则 | 配置项 | 语义 |
| --- | --- | --- |
| **同档降级迟滞** | `downgrade_confirm_steps`（默认 3） | **同一 `task_type` 内**降难度：需**连续 N step** 上报更低难度才生效 |
| **跨类降级确认（v4 新增）** | 固定 **1 步** | 降档同时发生**类别跨类**（如 `modify→explore`，`floor(task_type)` 更低）：确认步数收敛为 **1 步**（第 N 步上报、第 N+1 步生效）。**不是即时生效**——1 步确认防止 LLM 在类别边界反复跳类导致模型横跳，同时比同档 N 步更及时准确（决策 3） |
| **最小驻留** | `min_dwell_steps`（默认 2） | 一次切换后至少 K step 不再切换；**同档与跨类降档均不绕过** |
| **升级** | —— | 始终立即生效（升级是安全方向），与既有不对称设计一致 |

> **口径（v4）**：`effective_confirm_steps = 跨类降档 ? 1 : downgrade_confirm_steps`，再叠加 `min_dwell_steps`。`task_type` 缺省时全部上报按「同档」处理 ⇒ 与 v3 行为逐字一致。

> **不对称设计的理由**：升级的代价是钱，降级的代价是**质量**。让降级更难发生，符合「宁可多花钱，不可掉质量」的默认倾向；反之升级的误判代价可被成本护栏（§5.7）兜住。v4 的跨类 1 步是该原则的**语义细分**：类别跨类本身是强语义信号（比同档难度波动更可信），故确认门槛更低，但仍保留 1 步以防边界振荡。

### 5.6 前缀冻结不变式（INV-1 … INV-4）

| # | 不变式 | 违反后果 |
| --- | --- | --- |
| **INV-1** | `applyTurnRoute()` 不产生任何 prompt 侧写入 | 前缀被改写 ⇒ cache 失效 |
| **INV-2** | system prompt / 消息列表 / tool 定义在 turn 内**逐字节稳定** | 同上 |
| **INV-3** | **不向 LLM 暴露「当前 provider/model」** | 观测污染 ⇒ 闭环自激/振荡，并武器化自利性风险 |
| **INV-4** | 同 provider 内跨难度换 model 时，序列化前缀**字节一致** | 持续 miss |

> **INV-3 是最容易被"顺手做掉"的红线**：直觉上「告诉 LLM 当前用什么模型」像是无害的可观测性改进，实际是把策略状态交给被观测方。**该实现位置不是风格问题，是收益红线。**
>
> **v4 澄清（task_type 列表不违反 INV-3）**：§5.4 注入的有限 `task_type` 枚举与风险倾向纯文字，**不含任何「类别→model/provider」映射**，也不随 route 切换变化（INV-2/INV-4）——它是分类语义，不是策略状态，与 INV-3 禁止的「暴露当前 model」是两回事。

**与回合级路由提示的关系（v3 澄清）**：§5.4 的提示片段是**唯一**一处 prompt 侧新增物，其位置满足 INV-1/INV-2——在**首个请求之前**写入，此后 turn 内不再改写；`applyTurnRoute()` 仍然**不产生任何** prompt 侧写入（A13 的机械检查口径不变）。「回合级注入」与「route 切换不改写前缀」并不矛盾：前者是 turn 的开局事实，后者是 turn 内的不变式。

### 5.7 MA-P4：成本护栏

| 项 | 口径 |
| --- | --- |
| `max_consecutive_expensive_steps` | 连续处于「昂贵档位」的 step 上限；达阈值 ⇒ `resetTurnRoute()` 还原基线 + 发事件 |
| 计数口径 | 昂贵档位 step **+1**；基线档位**归零**；还原后**允许再升级**且计数**继续累加**（不是清零重来） |
| `expensive_levels` | 默认 `[hard, expert]` |
| `cost_guard_mode` | `soft`（只告警）/ `hard`（触发后**本 turn 内不再升级**） |

### 5.8 难度 → route 解析：复用既有链路

```
difficulty (easy|normal|hard|expert)
  → cfg.MainAgent.Routing.Profiles[difficulty]     ← 只给 delta（provider/model/reasoning_effort）
  → 未填字段沿用基线
  → modelrouting.Resolver.Resolve(...)             ← 返回 (RouteDecision, error)
      ├─ err == nil                    → 采用 decision
      ├─ err == errRouteHealthExhausted → 降级到基线 + source=health_exhausted_baseline（§5.10 规则 3/4）
      └─ 其他 error                     → 视为 unresolvable（保持当前 route、turn 不禁用）
  → applyTurnRoute() 复写 loop.config 三字段
```

**不引入新的解析组件**（上游 N7）。解析失败按 §6.2 的 `unresolvable` 处理：**忽略该次上报、保持当前 route、turn 不被禁用**。

> **本轮修订（MG7）**：在途特性已把 `Resolve` 改为**返回 error**（`modelrouting/resolver.go:14`），原四步流程会编译失败。新增的两条分支中，**`errRouteHealthExhausted` 必须与 `unresolvable` 分开处理**——前者是运行时状态（应降级到基线），后者是配置问题（应保持不动）。把两者混为一谈会掩盖 §3.6 的候选链耗尽，使健康熔断在主 Agent 侧静默。详见 §5.10。

### 5.9 档位与 `expert` opt-in

| 档位 | 默认可用 | 说明 |
| --- | --- | --- |
| `easy` / `normal` / `hard` | ✅ | `normal` 通常等于基线 |
| `expert` | ❌ **需显式 opt-in** | 子 Agent 语义里 `expert` 是「最贵档位」，受 `acquireExpertSlot`（`agent/scheduler.go:374`）并发限流保护；**主 Agent 无对应限流设施**，故默认不开放 |

**启用 `expert` 时的强制配套（缺一不可）**：

1. `max_consecutive_expensive_steps` **必须**为有限值（**不接受** `0 = 不限`）；
2. `expert` 必须出现在 `main_agent.routing.profiles` 中，否则视为不可解析；
3. 每次 `expert` 生效发独立事件且 `difficulty=expert`，便于成本审计告警。

### 5.10 与在途健康门禁的耦合规则（MG5 / MG6 / MG7 的修复）

**总原则：难度维度可以逐 step 变化，健康维度必须在 turn 内保持稳定。**

两者的时间尺度本就不同——难度是**任务属性**（step 级可变），健康是**基础设施属性**（分钟级可变）。原设计把二者都塞进「每 step 重解析」，等于用任务的时间尺度去响应基础设施的变化，必然振荡。

| # | 规则 | 说明 |
| --- | --- | --- |
| **1** | **健康维度闩锁**：turn 开始时解析一次健康门禁结果，**turn 内不再因健康状态变化而重解析** | 难度变化仍可逐 step 生效（走 §5.5 迟滞）；健康变化在 turn 内被冻结 |
| **2** | **健康驱动的变更不得绕过 `min_dwell_steps`** | 即使跨 turn，若上次切换距今不足 `min_dwell_steps`，健康降级也需等待。防止跨 turn 的快速横跳 |
| **3** | **禁止调用 `degradeToParentForHealth`** | 主 Agent 无父。候选链耗尽 ⇒ **显式降级到基线**（§5.1 的 `baselineRoute`），并发射 `source=health_exhausted_baseline` 的 `route_applied` |
| **4** | **区分两类失败** | `unresolvable`（配置问题）⇒ 保持当前 route、turn 不禁用（§5.8 原文不变）；`errRouteHealthExhausted`（运行时状态）⇒ 降级到基线 + 独立来源 |
| **5** | **`source` 枚举扩为四值** | `predicted` / `baseline` / `failover_candidate`（在途特性已定义，`types.go:27`）/ `health_exhausted_baseline`（本方案新增） |
| **6** | **`RouteDecision.Candidates` 进事件** | 把候选评估结果（`types.go:115`）写入 `route_applied` payload，使「为什么没选某个候选」可追溯 |

**规则 1 的实现要点（也是本方案唯一需要额外状态的地方）**：

```
turn 开始（run() 入口）
  → 解析一次 route（含健康门禁）→ 冻结为 turnRouteFloor
step 边界（applyTurnRoute）
  → 只叠加难度偏移（在 turnRouteFloor 之上）
  → 不重新查询 providerhealth
```

> **为什么闩锁是正确而非偷懒**：健康状态机的 half-open 探测**本身就需要放行请求**才能判定恢复。若主 Agent 每 step 重解析，它会把「探测失败」当成「立即改道」的信号，导致**探测永远无法成功**——即**观测行为反过来阻止了被观测状态的恢复**。这与 INV-3（不向 LLM 暴露 provider/model，防止闭环自激）是**同一类错误的两个实例**：让策略响应一个本该独立演化的状态。

**规则 3 的降级目标选择**：降级到**基线**而非「当前难度偏移」的理由是——候选链耗尽意味着**所有候选都不可用**，此时任何非基线 route 都无依据；基线是用户显式选择、且是主 Agent 启动时的实际运行 route，**是唯一有证据支持的可用目标**。

**与 §5.5 的关系**：§5.5 的迟滞是**难度维度**的防振荡；本节规则 1–2 是**健康维度**的防振荡。两者**不可互相替代**，必须同时实现（测试 U15/U16 分别覆盖）。

### 5.11 meta 工具的治理豁免（v3 实施补充）

`predict_task_difficulty` 是**只上报、无副作用**的 meta 工具（R1–R5）。这带来一个 §5.4 未覆盖的治理问题：**重复上报是合法且无害的，但既有 doom-loop 检测会把重复的工具调用指纹累计到 `MaxRepeatedToolCalls`，把一个正常的多步 turn 判成死循环并硬停。**

**实施口径**：

| 维度 | 决定 | 理由 |
| --- | --- | --- |
| 语义重复指纹 | **豁免**（`semanticToolCallRepeatExempt` 早返回，与 `supervision_inspect` 同类） | 引擎侧对「同一档位重复上报」已闩锁为 no-op（`TestSameDifficultyReportIsNoop`），不存在无限重试风险；而误判的代价是**硬停一个正在正常工作的 turn** |
| 工具调用预算 | **不豁免** | 上报不触发 `act()`、不消耗 step（R2），真实工具的预算仍由 `MaxToolCalls` / `MaxSteps` 兜底；若在此处一并豁免预算，会打开「用 meta 工具无限续轮」的口子 |
| 关闭态 | 工具根本不注册 ⇒ 该分支不可达 | 豁免是**纯防御**，不改变 `enabled=false` 的任何行为 |

> **为什么不改成「上报后立即清空指纹」**：那会让真实工具也搭便车（同一响应里上报 + 真实工具时，真实工具的重复指纹被顺带清掉）。豁免的粒度必须是**工具名**，不是**响应**。

---

## 6. 配置与事件 schema

### 6.1 配置节 `aicli.main_agent.routing`

**为什么不复用 `aicli.subagents.routing`**：

| 维度 | `aicli.subagents.routing` | 主 Agent 需要 |
| --- | --- | --- |
| 生效对象 | 子 Agent（每个 child 一次静态解析） | 主 Agent（同一 loop 内**多次**动态解析） |
| 生命周期 | child session 级 | **turn 级** |
| 默认值 | 影响所有子 Agent | **默认关闭**，不影响任何既有行为 |
| 字段语义 | `levels` / `roles` 是子任务分类 | 需要 `max_consecutive_expensive_steps` 等**串行**护栏 |
| 回归风险 | —— | 复用会让「打开主 Agent 动态切换」意外改变子 Agent 路由 |

**结论**：**独立配置节**，两者互不干扰。`main_agent.routing.profiles` 复用 `AICLISubagentRouteProfile`（`agentconfig/config.go:695`）的**类型**，但**不共享配置实例**。

```yaml
aicli:
  main_agent:
    routing:
      enabled: false                        # 默认关闭（治理要求）
      levels: [easy, normal, hard]          # 可选档位；显式列出即锁定
      allow_expert: false                   # expert 需 opt-in
      default_difficulty: normal            # turn 起始基线档位（通常 = 无偏移）
      allow_escalation_retry: false         # 是否允许升级类上报在同一 step 内重试一次
      cost_guard_mode: soft                 # soft | hard
      max_consecutive_expensive_steps: 6    # 0 = 不限；allow_expert=true 时禁止为 0
      expensive_levels: [hard, expert]
      max_invalid_reports_per_turn: 3       # 连续非法上报上限
      downgrade_confirm_steps: 3            # 降级迟滞
      min_dwell_steps: 2                    # 最小驻留
      health_gate:                          # 与在途健康门禁的耦合（§5.10）
        respect_provider_health: true       # 是否读取 providerhealth 门禁结果
        latch_scope: turn                   # 健康维度闩锁范围；**固定 turn，不接受 step**
        on_chain_exhausted: baseline        # 候选链耗尽时的目标；**固定 baseline**
        honor_min_dwell: true               # 健康驱动的降级也受 min_dwell_steps 约束
      profiles:                             # difficulty -> route delta
        easy:
          provider: ""                      # 留空 = 沿用基线 provider
          model: ""
          reasoning_effort: low
        normal: {}                          # 空 = 完全沿用基线
        hard:
          reasoning_effort: high
```

```go
// backend/internal/agentconfig/config.go（新增）
type AICLIMainAgentHealthGateConfig struct {
    RespectProviderHealth bool   `yaml:"respect_provider_health"`
    LatchScope            string `yaml:"latch_scope"`
    OnChainExhausted      string `yaml:"on_chain_exhausted"`
    HonorMinDwell         bool   `yaml:"honor_min_dwell"`
}

type AICLIMainAgentRoutingConfig struct {
    Enabled                      bool                                 `yaml:"enabled"`
    Levels                       []string                             `yaml:"levels"`
    AllowExpert                  bool                                 `yaml:"allow_expert"`
    DefaultDifficulty            string                               `yaml:"default_difficulty"`
    AllowEscalationRetry         bool                                 `yaml:"allow_escalation_retry"`
    CostGuardMode                string                               `yaml:"cost_guard_mode"`
    MaxConsecutiveExpensiveSteps int                                  `yaml:"max_consecutive_expensive_steps"`
    ExpensiveLevels              []string                             `yaml:"expensive_levels"`
    MaxInvalidReportsPerTurn     int                                  `yaml:"max_invalid_reports_per_turn"`
    DowngradeConfirmSteps        int                                  `yaml:"downgrade_confirm_steps"`
    MinDwellSteps                int                                  `yaml:"min_dwell_steps"`
    HealthGate                   AICLIMainAgentHealthGateConfig      `yaml:"health_gate"`
    Profiles                     map[string]AICLISubagentRouteProfile `yaml:"profiles"`
}
```

**`health_gate.latch_scope` 与 `on_chain_exhausted` 为何只接受单一值**：这两个字段**不是**为了可配置性而存在，而是为了**把 §5.10 的两条硬规则显式化到配置层**——写死取值（`turn` / `baseline`）使其在配置评审与 diff 中可见，任何试图改为 `step` 或其他降级目标的改动都会成为一次**显式的配置变更**，而不是藏在代码里。**校验规则**：取值非 `turn` / `baseline` ⇒ 配置加载期报错。

**与在途特性的关系（§3.6）**：`profiles` 复用的 `AICLISubagentRouteProfile` 已新增 `Availability` / `AvailabilityReason` / `PromptCache` / `Candidates` 字段，因此本配置**自动获得**可用性与 prompt-cache 门禁的表达能力，无需新增类型。但**子 Agent config 侧**的 `Failover` / `AvailabilityPolicy` / `RequirePromptCache` **不迁移到主 Agent**——主 Agent 的 `health_gate` 是独立命名空间，语义不同（前者是「构造时选一次」，后者是「turn 内闩锁」）。

**与顶层 `circuit_breaker` 的关系**：在途特性在 `agentconfig` 新增了顶层 `circuit_breaker:` 块（`circuit_breaker.go`）驱动 `providerhealth.Spec`。该块**属于全局基础设施配置，不在本配置节内**，本方案**不修改它**，只**读取**其运行结果（`providerhealth.Default()`）。**边界：本方案不新增任何健康阈值配置**——阈值是全局的，主 Agent 只是消费者。

**校验规则（配置加载期 fail-fast，不留到运行期）**：

| 规则 | 违反时 |
| --- | --- |
| `enabled=true` 且 `levels` 为空 | 报错：必须显式列出档位 |
| `levels` 含 `expert` 但 `allow_expert=false` | 报错（防配置歧义） |
| `allow_expert=true` 且 `max_consecutive_expensive_steps == 0` | 报错（§5.9 强制配套第 1 条） |
| `default_difficulty` ∉ `levels` | 报错 |
| `profiles` 的 key ∉ `levels` | 报错（避免死配置） |
| `expensive_levels` 含未在 `levels` 中的档位 | 警告并忽略该项 |
| `downgrade_confirm_steps < 1` 或 `min_dwell_steps < 0` | 报错（否则迟滞失效） |
| `profiles[d].provider` 未在 runtime 注册 / 未启用 | **不报错**，运行期视为 `unresolvable` 并忽略 |
| `health_gate.latch_scope != "turn"` | 报错（§5.10 规则 1 是硬约束，不允许逐 step 重解析） |
| `health_gate.on_chain_exhausted != "baseline"` | 报错（§5.10 规则 3：主 Agent 无父可退，只允许降级到基线） |

> **最后一条是刻意的**：配置期无法可靠判断 runtime 可用性（provider 可能延迟注册），因此降级为「运行期忽略 + warning」，避免一个未启用的 provider 导致整个 aicli 启动失败。

> **新增两条的取向与上面相反——它们是「宁可启动失败也不许配错」**：`latch_scope` 与 `on_chain_exhausted` 若被配成 `step` 或非 `baseline`，会**直接复现 MG5/MG6**（逐 step 振荡 / 退向不存在的父），且症状是「运行一段时间后才出现的成本异常」，极难归因。**因此不接受运行期降级，必须在配置加载期拦下。**

### 6.2 事件 schema 与**契约注册**（对接姊妹方案）

沿用既有 `loop.emitRuntimeEvent`（`agent/loop.go:1563`）机制。

| 事件名 | 触发时机 | payload 关键字段 |
| --- | --- | --- |
| `main_agent.route_applied` | 某 step **实际使用**了非基线 route | `step`, `difficulty`, `source`(**四值**，见下), `provider`, `model`, `baseline_provider`, `baseline_model`, `rationale`, **`task_type`, `task_subject`（v4 新增）**, `candidates`, `input_tokens`, `output_tokens` |
| `main_agent.route_prediction_invalid` | 上报格式非法 / 难度值非法 | `step`, `raw_difficulty`, **`raw_task_type`（v4 新增，原始非法值）**, `reason`, `consecutive_invalid` |
| `main_agent.route_prediction_unresolvable` | provider/model 无法解析 | `step`, `difficulty`, `requested_provider`, `requested_model`, `reason` |
| `main_agent.route_disabled_for_turn` | 连续非法上报达阈值 | `step`, `consecutive_invalid`, `turn_id` |
| `main_agent.route_cost_guard_tripped` | 昂贵档位连续达阈值 | `step`, `consecutive_steps`, `difficulty`, `provider`, `model`, `cost_guard_mode` |
| `main_agent.route_cleared` | turn 结束**还原基线** | `turn_id`, `final_difficulty`, `steps_with_override`, `steps_total`, `restored_provider`, `restored_model` |

**字段口径**：

- `source=baseline` 的 `route_applied` **照常发**，便于统计「本 turn 有多少 step 走了基线」；此时 `difficulty = default_difficulty`；
- `baseline_provider` / `baseline_model` 取**用户 `/model` 的显式值**，不是 `default_difficulty` 的映射结果；
- v4 新增：`task_type` / `task_subject`（最近一次上报的类别与短说明；缺省为空串），进 `usage_routes` 与 `by_task_type` 聚合（姊妹方案 §6.4 改动点 28）；两者只进事件，不进 prompt；
- token 字段允许为 `0`（部分 provider 不回传用量），但**字段必须存在**，便于下游统一解析。

**`source` 四值口径（本轮修订，对齐 §5.10 规则 5）**：

| 取值 | 含义 | 来源 |
| --- | --- | --- |
| `predicted` | 难度偏移生效 | 本方案 MA-P1 |
| `baseline` | 本 step 走基线（无偏移） | 本方案 MA-P1 |
| `failover_candidate` | 首选候选被健康门禁拦下，**改用了候选链中的其他候选** | 在途特性（`types.go:27`） |
| `health_exhausted_baseline` | **整条候选链耗尽**，降级到基线 | 本方案新增（§5.10 规则 3） |

> **为什么要区分后两者**：两者都表现为「没按预期用首选 route」，但一个是**换了个可用候选**（仍在候选链内），另一个是**完全放弃候选链退回基线**。混为一谈会让「健康熔断的严重程度」在审计中不可分辨——而这恰恰是运维最需要区分的两种情况。

**`candidates` 字段**：取 `RouteDecision.Candidates`（`types.go:115`），记录每个候选的评估结果（含被拦下的原因）。这是「为什么没选某个候选」的唯一证据来源。

**为什么 `route_cleared` 必须存在**：turn 隔离在结构上已由「per-run 新 loop + 结构体拷贝」保证，**不再依赖该事件**。但 §5.1 引入的是**还原语义**（把三字段写回 `baselineRoute`，而非清零），这是一个**显式动作**，需要可观测证据——事件里带上 `restored_provider`/`restored_model` 才能证明「确实还原到了正确基线」，而不是靠读代码相信。

**契约注册（MG3 的修复，三步，缺一不可）**：

| 步骤 | 动作 | 落点 |
| --- | --- | --- |
| 1 | 在 `runtimeEventContracts` 中登记 6 个**本方案新增**类型并选定通道 | `internal/events/contract.go:77` |
| 2 | 在 `known_types.go` 的类型目录中登记 | `internal/runtimeobserve/known_types.go`（对照 `:162-179`） |
| 3 | 门禁覆盖：**不得**以裸字面量发射 | 见下方通道选择与门禁说明 |
| **4** | **补登记在途特性已泄漏的 `llm.provider.health_opened`**（本轮修订新增） | `agent/loop.go:2186` 发射点；登记落点同上 |

> **第 4 步为什么由本方案承担**：该事件是**主 Agent 健康维度切换的唯一信号**（§5.10 规则 1 的闩锁决策、规则 3 的降级触发都以它为前提）。若它不可见，主 Agent 的健康降级将在审计中**完全无痕**。虽然它由在途特性引入（§3.6），但**本方案是该事件的第一个消费者**，因此**补登记责任落在本方案**。
>
> **同一盲区的第二个实例**：`llm.prompt_cache.breaker_tripped`（`agent/loop.go:2202`）同样未登记。它与本方案无直接功能关系，**建议一并补登记或显式声明为无通道**，并交由姊妹方案的扫描测试覆盖（§8.2 门禁 G-B）。

**通道选择建议**：

| 事件 | 建议通道 | 理由 |
| --- | --- | --- |
| `route_applied` | `ChannelSessionStore` | 成本归因与事后追责的主证据，必须 durable |
| `route_prediction_invalid` / `_unresolvable` / `_disabled_for_turn` | `ChannelSessionStore` | 失败/降级路径正是「出事时最需要查到」的一类 |
| `route_cost_guard_tripped` | `ChannelSessionStore` + `PersistCritical` | 成本护栏触发是关键事件，应压缩崩溃窗口 |
| `route_cleared` | `ChannelSessionStore` | 还原证据，与 `route_applied` 配对才能算出「本 turn 偏移了几步」 |

> **通道选择的取舍**：全部选 A 通道意味着主 Agent 每个 turn 可能多出 1–3 行事件。相对 `assistant_delta` 的量级可忽略，但它换来了「主 Agent 路由可审计」这一本方案的核心治理价值。**该取舍记入 §10 风险 R2。**

**门禁（防复发的关键，与 SA-G2 共用同一措施）**：

姊妹方案 SA-G2 的病因是「门禁覆盖不到 `internal/agent` 里的裸字面量」。本方案的发射点**同在 `internal/agent`**（`loop.go:1563` 的 `emitRuntimeEvent`），因此**必须**：

1. **主措施**：把 6 个事件名定义为 Go 常量（放在 `internal/events` 或与 `supervision.EventTypeSubagentProgress` 同款的位置），**发射点只引用常量**，再把常量加入 `contract_test.go` 的门禁表（对照 `contract_test.go:171` 的 `TestExternalEventFamilyConstantsAreRegistered`）。
2. **辅措施**：姊妹方案拟新增的**扫描型测试**（对 `internal/agent` 等目录提取 `emitRuntimeEvent("<literal>"` 字面量并断言已登记）**必须**把本方案的发射点纳入覆盖。

> **两条措施的实施归属**：主措施在**本方案**内完成（本方案的新事件本方案负责）；辅措施的扫描测试**归属姊妹方案 SA-G2**，本方案只需保证发射点用常量。**若姊妹方案未实施，本方案不得以裸字面量上线**——这是硬门禁，见 §8.2。

### 6.3 配置隔离

| 隔离要求 | 验证 |
| --- | --- |
| 修改 `main_agent.routing` **不影响**子 Agent 路由 | §9.3 REG3 |
| 主 Agent 偏移**不被**子 Agent 继承 | §6.4 |
| `profiles` 类型复用但实例不共享 | §6.1 |

### 6.4 非目标：不做持久化

| 不做的事 | 理由 |
| --- | --- |
| ❌ 不写回 session 的持久化 provider/model | 会破坏 `chat_actor_host_test.go:1514` 已有的「routing override 不得持久化」断言 |
| ❌ 不修改 `applyRuntimeModelSwitch` 的 next-turn 语义 | `/model` 是**用户意图**，优先级最高，不应被自动机制改写 |
| ❌ 不新增 session DB 字段 / 不新增迁移 | 无状态变更，降低回滚成本 |
| ❌ 不跨 turn 记忆难度 | turn 是治理边界；跨 turn 记忆会让「临时调整」变成事实上的持久覆盖 |
| ❌ 不让子 Agent 继承主 Agent 的偏移 | 子 Agent 有自己的 `subagents.routing`，两者独立 |

> **一句话边界**：主 Agent 的动态 route **只存在于 loop 内存中、只存活于一个 turn 内、只在事件流里留痕**。任何形式的落盘都需要新开 RFC。

---

## 7. 改动点清单

> 所有行号为 2026-09-21 复核值；实施前按**符号名**再核一次（行号会漂移）。

### 7.1 MA-P0：所有权收口（纯重构）

| # | 文件 | 符号 | 改动 | 风险 |
| --- | --- | --- | --- | --- |
| 1 | `agent/loop.go:188` | `NewReActLoop` | 值拷贝 config；规范化只作用于副本 | 低 |
| 2 | `agent/loop.go:192` | （同上） | **删除**对调用方 config 的就地写回 | 低 |
| 3 | `agent/loop.go` | `snapshotBaselineRoute` / `baselineRoute`（新增） | 构造时快照三字段 | 低 |
| 4 | `agent/agent.go:991` | `RunReActWithConfig` | **显式验证**调用方是否依赖 `MaxSteps` 回写；若依赖 ⇒ 改为显式赋值（MG4 / 上游 O3） | **中** |
| 5 | `agent/loop.go:439` | `run()` | 入口 + `defer` 调用 `resetTurnRoute()` | 低 |
| 6 | `agent/loop.go` | `applyTurnRoute()`（新增） | **只定义、无调用方** | 极低 |

### 7.2 MA-P1：应用点 + 迟滞

| # | 文件 | 符号 | 改动 |
| --- | --- | --- | --- |
| 1 | `agent/loop.go` | `applyTurnRoute()` | 实现三字段**整体**复写 |
| 2 | `agent/loop.go` | `resetTurnRoute()` | 实现三字段**还原**到 `baselineRoute` |
| 3 | `agent/loop.go` | 迟滞状态机（新增） | 同档 `downgrade_confirm_steps` 连续计数 + **跨类降档 1 步确认（v4，§5.5）** + `min_dwell_steps` 驻留 |
| 4 | `agent/loop.go` | step 边界接线 | `think()` 返回后、下一轮 `think()` 前应用待生效 route |

### 7.3 MA-P2：预测生产者

| # | 文件 | 符号 | 改动 |
| --- | --- | --- | --- |
| 1 | `agent/loop.go` | 工具定义（新增，对照 `spawnSubagentsToolDefinition` `:6856`） | `predict_task_difficulty` 的 schema；`enum` 随 `allow_expert` 变化；**v4 增可选 `task_type`（enum）/`task_subject` 参数（§5.4）** |
| 2 | `agent/loop.go` | 工具注册 | **仅** `enabled=true` 时注册（`enabled=false` ⇒ 工具列表逐字节不变） |
| 3 | `agent/loop.go:2701` | 工具分流点 | 在 `spawn_subagents` 分流**之前/之后**新增 `predict_task_difficulty` 拦截分支 |
| 4 | `agent/loop.go:1610` | `think()` 返回处理 | 实现 **§5.4 的 R1–R5**（上游五条硬规则）：剥离上报、占位、不消耗 step、允许并存、meta-only 续轮 |
| 5 | system prompt | 注入片段 | 仅 `enabled=true`；turn 内**稳定**（INV-2）。**v3 落地口径见 §5.4 实施补充**（回合级 system-reminder、`Durable=false`、run 入口一次性注入）；**v4 片段含有限 `task_type` 列表**（不含类别→model 映射，INV-3） |
| 6 | `modelrouting` | 复用 `NormalizeDifficulty`（对照 `loop.go:6184`） | 难度归一；**不新增**解析组件 |

### 7.4 MA-P3：配置

| # | 文件 | 符号 | 改动 |
| --- | --- | --- | --- |
| 1 | `agentconfig/config.go`（对照 `:667` / `:695`） | `AICLIMainAgentRoutingConfig`（新增） | 类型定义 |
| 2 | `agentconfig/config.go` | 父级挂载点 | `AICLI.MainAgent.Routing` |
| 3 | 配置校验（对照 `modelrouting/validate.go` 的手法） | 校验函数 | §6.1 **十条**规则，fail-fast |
| 4 | 默认值 | —— | `enabled=false` ⇒ 零行为变化 |

### 7.5 MA-P4：治理 + 成本护栏

| # | 文件 | 符号 | 改动 |
| --- | --- | --- | --- |
| 1 | `internal/events/contract.go:77` | `runtimeEventContracts` | 登记 6 个 `main_agent.route_*` 类型 + 通道 |
| 2 | `internal/runtimeobserve/known_types.go` | 类型目录 | 登记同一批类型 |
| 3 | `internal/events`（或同款位置） | 事件名常量（新增） | 发射点只引用常量（§6.2 主措施） |
| 4 | `agent/loop.go` | 成本护栏计数 | `max_consecutive_expensive_steps` 口径（§5.7） |
| 5 | `agent/loop.go` | `route_cost_guard_tripped` 发射 | 触发 + `resetTurnRoute()` |
| 6 | `internal/events/contract.go:77` | `runtimeEventContracts` | **补登记 `llm.provider.health_opened`**（§6.2 第 4 步） |

### 7.6 在途健康门禁耦合（MG5 / MG6 / MG7 的修复，本轮修订新增）

| # | 阶段 | 文件 | 符号 | 改动 |
| --- | --- | --- | --- | --- |
| 1 | MA-P1 | `agent/loop.go` | `run()` 入口 | turn 开始时解析一次 route（含健康门禁）⇒ 冻结 `turnRouteFloor`（§5.10 规则 1） |
| 2 | MA-P1 | `agent/loop.go` | `applyTurnRoute()` | **只叠加难度偏移**；**禁止**在此处调用 `providerhealth` 或重新 `Resolve` |
| 3 | MA-P1 | `agent/loop.go` | 迟滞状态机 | 健康驱动的变更也走 `min_dwell_steps` 判定（§5.10 规则 2） |
| 4 | MA-P1 | `agent/loop.go` | 降级路径（新增） | **不调用** `degradeToParentForHealth`；候选链耗尽 ⇒ 显式还原 `baselineRoute` + `source=health_exhausted_baseline`（§5.10 规则 3） |
| 5 | MA-P1 | `agent/loop.go` | 解析结果分流 | 区分 `errRouteHealthExhausted` 与其他 error（§5.10 规则 4） |
| 6 | MA-P3 | `agentconfig/config.go` | `AICLIMainAgentHealthGateConfig`（新增） | §6.1 的 `health_gate` 节 + 两条 fail-fast 校验 |
| 7 | MA-P4 | `agent/loop.go` | `route_applied` payload | 增加 `source` 四值 + `candidates` 字段（§6.2） |

> **第 2 项是本组的关键约束**：它是**唯一**能防止 MG5 复发的实现位置。评审时应把「`applyTurnRoute()` 内不出现 `providerhealth` / `Resolver.Resolve`」当作一条**可机械检查的规则**（测试 U15）。

### 7.7 宿主接线（v3 实施补充，MG8 的修复）

> **MG8（高，v3 新发现）配置能解析但送不进 loop。** `LoopReActConfig.MainAgentRouting`（`agent/loop.go:83`）是 loop 侧的唯一入口，但**没有任何宿主把配置送进去**：`chatcore` 的 `buildSessionLoopConfig` / `buildLocalChatLoopConfig` 都不读 `aicli.main_agent.routing`。后果是**最危险的失效形态**——配置侧一切正常、校验通过、`/config` 显示已启用，而运行期零行为变化，且不产生任何 `main_agent.route_*` 事件可供归因。

**修复原则**：接线必须走**与子 Agent / team 路由完全相同的两条宿主路径**，不新增第三条。两条路径的失效模式不同（一条漏拷快照、一条漏调应用函数），因此**两侧都要有测试**。

| # | 文件 | 符号 | 改动 |
| --- | --- | --- | --- |
| 1 | `internal/api/skills/handler.go` | `cloneAICLIRoutingConfig` | 深拷贝 `config.AICLI.MainAgent`（新增 `cloneMainAgentRoutingConfigForHandler`：`Levels` / `ExpensiveLevels` 逐项拷贝 + `Profiles` map 深拷贝） |
| 2 | `internal/api/skills/handler.go` | `mainAgentRoutingConfig()`（新增） | 从 handler 持有的配置快照读出主 Agent 路由配置 |
| 3 | `internal/api/skills/session_runtime_support.go` | `applyAPISessionMainAgentRouting()`（新增） | `buildSessionLoopConfig` 之后写入 `loopConfig.MainAgentRouting` |
| 4 | `cmd/aicli/commands/chat_actor_host.go` | `applyLocalChatMainAgentRouting()` / `localChatMainAgentRoutingConfig()`（新增） | `buildLocalChatLoopConfig` 之后写入；配置源为 `config.EffectiveMainAgentRoutingConfig(session.Config)` |

**两条硬约束（与 §6.3 一致）**：

| 约束 | 口径 | 测试 |
| --- | --- | --- |
| **只接主会话** | 子 Agent / team 成员会话**不得**继承主 Agent 偏移：API 侧以 `childAgentType == "" && childDepth == 0 && !childReadOnly` 判定，CLI 侧以 `isBaseSession` 判定 | `TestApplyAPISessionMainAgentRoutingGate` / `TestApplyLocalChatMainAgentRoutingHostWiring` |
| **关闭态零行为变化** | `nil` 或 `enabled=false` ⇒ 不写入任何字段 | 同上（两侧均含关闭态子用例） |

> **为什么深拷贝是必须的**：handler 持有的是**可热重载的配置快照**，loop 持有的是**会就地复写 provider/model 的副本**（§5.2）。若共享 `Profiles` map / `Levels` slice，热重载与 route 复写会互相污染——这正是 §5.2 所有权收口要解决的问题，宿主侧不能重新引入。

---

## 8. 实施阶段与依赖顺序

### 8.1 与姊妹方案（子 Agent 审计加固）的顺序

两份方案**功能正交**，但**共享事件契约与门禁**。顺序建议：

| 顺序 | 方案 | 理由 |
| --- | --- | --- |
| **第 0**（本轮修订新增） | **在途特性（§3.6）先落地提交** | 它已修改 `agent/loop.go` / `modelrouting/*` / `agentconfig/*`，与本方案**文件重叠**。未提交状态下行号、符号、`Resolve` 签名都会漂移——MA-P0 会建在移动地基上（R9） |
| **第 1** | 姊妹方案 SA-P0 / SA-P1（含 SA-G2 的门禁扩展） | 门禁与扫描测试是**共享基础设施**。先建门禁，本方案的新事件一落地即被覆盖 |
| **第 2** | 本方案 MA-P0（**rebase 到第 0 步之后**） | 纯重构，可独立合入，与姊妹方案零冲突 |
| **第 3** | 本方案 MA-P1 → MA-P2 → MA-P3 | 应用点 → 生产者 → 配置 |
| **第 4** | 本方案 MA-P4 + 姊妹方案 SA-P2/SA-P3 | 治理收尾 |

> **可以并行**：第 0 步落地后，MA-P0 与姊妹方案 SA-P0/SA-P1 改动文件**不重叠**（前者 `agent/loop.go` 构造与 run 入口，后者 `scheduler.go` / `subagent_batch_coordinator.go` / `contract.go`），可并行开工。
>
> **⚠️ 本轮修订修正了一条原判断**：原文称「MA-P0 与姊妹方案文件不重叠，可并行开工」——该结论**在在途特性存在时不成立**。MA-P0 要改 `agent/loop.go:188`/`:192`，而在途特性**已经改过同一个文件**（新增 `providerhealth` 调用与 `llm.provider.health_opened` 发射）。**必须等它提交后再 rebase**，否则冲突解决会混入两批语义无关的改动。
>
> **不可颠倒**：**MA-P4 的契约登记必须在门禁扩展之后**，否则本方案会先制造一批「已登记但门禁覆盖不到」的新盲区——恰好复现 SA-G2 的病因。

### 8.2 阶段门禁

| 门禁 | 条件 | 不满足时的后果 |
| --- | --- | --- |
| **G-0**（**开工前**，本轮修订新增） | **在途特性（§3.6）已提交落地**；`git status` 中 `loop.go` / `modelrouting/*` / `agentconfig/*` **干净** | **不得开始 MA-P0**——否则建在移动地基上（R9），且 `Resolve` 签名可能在实现中途变化 |
| **G-A**（进 MA-P2 前） | MA-P0 + MA-P1 全绿；`enabled=false` 时行为逐字节不变 | 不得进入生产者阶段 |
| **G-B**（进 MA-P4 前） | 姊妹方案的门禁扩展已合入，或本方案自建等价扫描测试 | **不得以裸字面量上线**（§6.2） |
| **G-C**（MA-P4 完成前） | **O5 成本量化完成**（§12） | 收益假设不成立 ⇒ 回到 §5 重新评估，而不是继续推进 |
| **G-D**（MA-P1 完成前，本轮修订新增） | §9.1 U15/U16/U17 全绿；§9.3 **REG11** 在途测试无回归 | **MG5/MG6/MG7 未修复 ⇒ 不得进入 MA-P2**（一旦有了难度生产者，振荡会立刻被放大） |

### 8.3 阶段一览

| 阶段 | 内容 | 可独立合入 | 对外行为变化 |
| --- | --- | --- | --- |
| **MA-P0** | 所有权收口 + 基线快照 + 还原入口（`applyTurnRoute` 无调用方） | ✅ | 无（MG4 需验证） |
| **MA-P1** | `applyTurnRoute` 实现 + 迟滞状态机（仍无生产者）+ **健康维度闩锁与降级路径（§7.6 第 1–5 项）** | ✅ | 无（**G-D 门禁**） |
| **MA-P2** | `predict_task_difficulty` 工具 + **§5.4 R1–R5** + prompt 片段 | ✅ | 仅 `enabled=true` 时 |
| **MA-P3** | 配置节 + 校验（**含 `health_gate` 子节与两条 fail-fast 规则**） | ✅ | 仅 `enabled=true` 时 |
| **MA-P4** | 事件契约登记（**含补登记 `llm.provider.health_opened`**）+ 成本护栏 + 审计 | ✅ | 仅 `enabled=true` 时 |

> **MA-P0 与 MA-P1 的关键性质**：两阶段都**没有生产者**——没有任何代码会调用 `applyTurnRoute()` 传入非基线值。因此它们合入后，`loop.config` 三字段恒等于基线，属**可单独验证的重构**。这是本方案最重要的风险控制手段。

---

## 9. 测试计划

### 9.1 单元测试（`backend/internal/agent`）

| # | 用例 | 断言 | 阶段 |
| --- | --- | --- | --- |
| **U1** | `NewReActLoop` 所有权收口 | 构造后修改 `loop.config` **不影响**调用方传入的 config；`MaxSteps` 规范化不外泄 | MA-P0 |
| **U2** | `snapshotBaselineRoute()` | `baselineRoute` == 构造时 `loop.config` 三字段；之后复写 `loop.config` **不改变** `baselineRoute` | MA-P0 |
| **U3** | `resetTurnRoute()` 还原 | 复写后调用，三字段**逐字段等于** `baselineRoute`（**不是**零值/空串） | MA-P0 |
| **U4** | `run()` 入口 + `defer` | 上一 turn 残留偏移在入口被还原；提前 return / panic 路径下 `defer` 仍还原 | MA-P0 |
| **U5** | `applyTurnRoute()` 整体应用 | 跨 provider 时三字段**同时**复写，**不出现**新 provider + 旧 model 的混用 | MA-P1 |
| **U6** | 三类消费者一致性（上游 D1 约束） | 复写后 `requestProvider()`（`loop.go:291`）/ `requestModel()`（`:303`）/ `resolvePromptPreflightProviderModel()`（`:5858`）返回**同一** route | MA-P1 |
| **U7** | 迟滞：降级确认 | 连续 `downgrade_confirm_steps` 次更低难度才降级；未达次数不降 | MA-P1 |
| **U8** | 迟滞：最小驻留 | `min_dwell_steps` 内不重复切换 | MA-P1 |
| **U9** | 未上报难度 | **保持当前 offset 不变**（不重置为 `default_difficulty`） | MA-P2 |
| **U10** | 非法难度值上报 | 忽略 + `route_prediction_invalid`；连续达阈值 → `route_disabled_for_turn` | MA-P2 |
| **U11** | 不可解析 route 上报 | 忽略 + `route_prediction_unresolvable`；**turn 不被禁用** | MA-P2 |
| **U12** | `expert` opt-in | `allow_expert=false` 时工具 schema 的 `enum` **不含** `expert` | MA-P2 |
| **U13** | 配置校验 | §6.1 **十条**规则各自触发预期错误/警告；`latch_scope=step` 与 `on_chain_exhausted=parent` **必须报错** | MA-P3 |
| **U14** | 成本护栏计数 | 昂贵档位 +1、基线归零、达阈值还原基线 + 发事件、还原后允许再升级且计数继续累加 | MA-P4 |
| **U15** | **健康维度闩锁**（MG5） | turn 内翻转 `providerhealth` 状态（healthy→open→half-open），`applyTurnRoute()` 产出的 route **不随之变化**；且 `applyTurnRoute()` 内**不调用** `providerhealth` / `Resolver.Resolve`（§5.10 规则 1/2） | MA-P1 |
| **U16** | **健康降级不绕过迟滞**（MG5） | 健康驱动的降级在 `min_dwell_steps` 内**不生效**（§5.10 规则 2） | MA-P1 |
| **U17** | **候选链耗尽降级**（MG6/MG7） | 注入 `errRouteHealthExhausted` ⇒ 还原 `baselineRoute` + `source=health_exhausted_baseline`；**不调用** `degradeToParentForHealth`；其他 error ⇒ 保持当前 route 且 **turn 不禁用** | MA-P1 |
| **U18** | **回合级提示片段**（v3 新增，§5.4） | 片段内容只含档位清单 + **有限 `task_type` 列表（v4）** + 默认档 + 反博弈条款；**不含**任何 provider/model 字样、**不含**类别→model 映射；`enabled=false` / `nil` ⇒ 不产出消息；同一配置重复调用**逐字节相同**（INV-2） | MA-P2 |
| **U19** | **meta 工具不参与语义重复指纹**（v3 新增，§5.11） | 连续 N 次相同 `predict_task_difficulty` 调用**不触发** doom-loop warning/hard stop；真实工具的重复调用**仍然触发**（豁免粒度是工具名，不是响应） | MA-P2 |
| **U20** | **跨类降档 1 步确认**（v4，§5.5） | `modify→explore`（跨类）：第 1 次连续上报不生效、第 2 次生效；同档降级仍需 `downgrade_confirm_steps`；两类降档在 `min_dwell_steps` 内均不生效；`task_type` 缺省 ⇒ 全部按同档（与 v3 一致） | MA-P1 |
| **U21** | **`task_type` 契约**（v4，§5.4/§6.2） | 合法 `task_type`/`task_subject` 进 `route_applied` payload（截断）；未知 `task_type` → `task_type_unknown:<v>` warning 且档位不变；缺省 `task_type` ⇒ 与 v3 行为逐字一致 | MA-P2 |

### 9.2 集成测试（loop 级 + mock provider）

| # | 用例 | 断言 |
| --- | --- | --- |
| **I1** | 完整 turn：step 1 上报 `hard` | step 2 的 `LLMRequest` 使用 hard route 的 provider/model；事件流含 `route_applied` |
| **I2** | meta-only 响应（只有上报） | **不产生空 step、不消耗 step、不返回用户可见输出**，直接进入下一轮 `think()` |
| **I3** | preflight 与请求构建读**同一** route | 跨 provider（两 provider 的 `MaxContextTokens` 不同）时，`enforcePromptPreflightWithTools()`（`loop.go:5176`）与请求构建使用**同一** route 的预算。**这是上游要求补的唯一断言** |
| **I4** | turn 结束 | `resetTurnRoute()` 还原三字段并发 `route_cleared`（含 `restored_*`）；**下一个 turn 的第一个 step 从基线开始** |
| **I5** | 同响应内「上报 + 真实工具」并存 | 上报被剥离、真实工具正常执行（**§5.4 规则 R3**） |
| **I6** | `/model` 优先级 | turn 内用户切换 `/model` → 当前 turn 的偏移在结束时被还原；**下一个 turn 从新基线开始** |
| **I7** | **跨 turn 的健康稳定性**（MG5） | provider 在 turn A 内进入 `open`：turn A 的 route **不变**；turn B 开始时才反映新健康状态。**验证 R1 风险未被放大到 per-step** |
| **I8** | **请求面端到端**（v3 新增，MG8 的回归钉） | 启用态：真实 `LLMRequest.Tools` **含** `predict_task_difficulty`，且 `Messages` 中该片段**恰好出现一次**；关闭态：两者**均不存在**；两种状态下 `PersistHistory` 落盘的消息**都不含**片段（INV-1） |

### 9.3 回归 / 负向测试

| # | 用例 | 断言 |
| --- | --- | --- |
| **REG1** | `enabled=false`（**默认**） | 工具**不注册**；无任何 `main_agent.route_*` 事件；工具列表与现状**逐字节一致** |
| **REG2** | 持久化边界 | 扩展 `chat_actor_host_test.go:1514`，确认主 Agent offset **不写入** session 持久化 provider/model |
| **REG3** | 配置隔离 | 修改 `main_agent.routing` **不影响**子 Agent 路由 |
| **REG4** | 未启用 provider 出现在 `profiles` | 启动**不失败**；运行期按 `unresolvable` 忽略 |
| **REG5** | `cost_guard_mode: hard` | 触发后**本 turn 内不再升级** |
| **REG6** | 多 session 隔离 | 同一 loop 实例下，不同 session 的 offset 不互相污染（若存在共享实例） |
| **REG7** | **前缀冻结不变式** | 同 provider 内跨难度切换 model 时，序列化后的 system prompt / 消息列表 / tool 定义**逐字节一致**；`applyTurnRoute()` 不产生任何 prompt 侧写入（INV-1/2/4） |
| **REG8** | **不暴露当前 model** | 路由切换**不产生任何**向 LLM 暴露 provider/model 的注入；升级重试的窄例外**只注入行为指令**（非策略状态），且**不进前缀**（INV-3） |
| **REG9** | **防振荡** | 降级需连续 N step 确认；`min_dwell_steps` 内不重复切换；边界难度反复上报时 route **不横跳** |
| **REG10** | **契约门禁**（MG3） | `contract_test.go` 对 6 个新类型断言已登记；扫描测试确认**无裸字面量**发射点 |
| **REG11** | **在途特性既有测试不回归** | `modelrouting/candidates_test.go`、`health_test.go`、`providerhealth/registry_test.go` **全部保持通过**；主 Agent 改动**不得**改变子 Agent 的「解析一次」语义（§3.6） |
| **REG12** | **`health_opened` 可见性**（MG3 补漏） | `llm.provider.health_opened` 登记后 `ChannelsFor` **非 0**；构造一次 provider 归因失败 ⇒ 事件**实际落盘**（当前该断言会失败，是本次修订的验收证据） |
| **REG13** | **宿主接线**（v3 新增，MG8） | ① 配置快照深拷贝**保留** `main_agent.routing`，且改快照不污染源配置；② 子 Agent / 只读 / 有深度会话**不继承**主 Agent 路由；③ `enabled=false` ⇒ 写入点为空操作（`internal/api/skills` 与 `cmd/aicli/commands` 两侧各自断言） |

### 9.4 验证命令

```powershell
go test ./backend/internal/agent/... -run 'Route|Routing|Difficulty' -count=1
go test ./backend/internal/agentconfig/... -count=1
go test ./backend/internal/events/... -count=1
go test ./backend/internal/modelrouting/... -count=1
go test ./backend/internal/providerhealth/... -count=1
go build ./...
```

> **最后两个包是本轮修订新增的验证目标**（§3.6）：主 Agent 的健康耦合改动若破坏了在途特性的候选链语义，会首先在这两个包暴露。**它们必须全绿，且不允许为通过而修改在途特性的测试**——若确实需要改，说明本方案越界，应回到 §5.10 重新设计。

---

## 10. 风险与取舍

| # | 风险 | 等级 | 处置 |
| --- | --- | --- | --- |
| **R1** | 跨 provider 切换导致 **B 侧首次请求冷启动**（上游 O5） | **高** | 性质是「B 侧冷启动」，不是「A 的 cache 被失效」；且**已有度量设施**：`internal/cacheanalytics` 已采集 `cache_read_tokens` / `cached_tokens` / `cache_creation_tokens` / `usage_cache_hit_ratio` / `prompt_fingerprint`。**MA-P4 前必须用现有仪表做 A/B 量化**（门禁 G-C） |
| **R2** | 全 A 通道事件给主 Agent 每 turn 增 1–3 行事件 | 低 | 相对 `assistant_delta` 量级可忽略；换得主 Agent 路由可审计。若实测有压力，降级为「仅 `route_applied` 落盘 + 其余走 D 通道」 |
| **R3** | 前缀冻结被实现方式破坏（例如把「当前 model」写进 prompt） | **高** | 以 INV-1..INV-4 + **REG7/REG8** 作为回归硬断言；**实现位置不是风格问题，是收益红线** |
| **R4** | LLM 自利性抬档（为换模型而报 `hard`） | 中 | 提示词删因果披露 + 反博弈条款；成本护栏（§5.7）兜底；上报频次天然受限（R2 不消耗 step 但也无收益） |
| **R5** | 难度上报频次过高，抵消收益 | 中 | 提示词明确「大多数 step 不调用是正常的」「重复报同一档位是 no-op 且浪费 token」 |
| **R6** | MG4：`RunReActWithConfig` 调用方依赖 `MaxSteps` 回写 | 中 | MA-P0 内显式验证（§7.1 第 4 项）；若依赖改为显式赋值 |
| **R7** | 与子 Agent 路由语义混淆（同一个 `expert` 在两处含义不同） | 低 | §6.1 配置隔离 + R3；文档层面以 `MA-` / `SA-` 前缀区分（§2.1） |
| **R8** | **健康门禁驱动的 per-step 振荡**（MG5，本轮修订新增） | **高** | §5.10 规则 1/2：健康维度 turn 内闩锁 + 不绕过 `min_dwell_steps`。**该风险若发生，会把 R1 从「每 turn 一次冷启动」放大到「每 step 可能一次」，直接吃掉全部收益**——因此与 R1/R3 同级 |
| **R9** | **在途特性未提交 ⇒ 本方案建立在移动地基上**（§3.6） | **中高** | 在途改动触及 `loop.go` / `modelrouting/*` / `agentconfig/*`，与本方案**文件重叠**。处置：§8.1 新增前置条件——**在途特性必须先落地提交**，MA-P0 再 rebase；否则行号与语义都会漂移 |

### 10.1 收益假设的诚实表述

本方案的核心收益假设是「**简单任务用便宜模型 → 省钱**」。但必须承认：

- 跨 provider 切换的成本**不是**「cache 失效」这种笼统说法，而是 **B 侧首次请求冷启动**；
- 同 provider 同 protocol 换 model 时前缀**字节稳定**，成本远低于跨 provider；
- 因此 **`profiles` 里「同 provider 换 model」的档位配置，收益风险比显著优于「跨 provider」**。

> **建议的默认配置取向**：`easy` / `hard` 优先映射到**同一 provider 内的不同 model**；跨 provider 映射只在明确收益（单价差 >> 冷启动成本）时启用。

---

## 11. 验收标准（DoD）

### 11.1 MA-P0 验收（不含任何预测能力）

| # | 条件 |
| --- | --- |
| **A1** | `NewReActLoop` 接管 config 所有权（结构体拷贝 + 嵌套深拷贝），不再就地改写调用方对象；**`agent.go:991` 调用方已显式验证**（MG4/O3 闭环） |
| **A2** | `baselineRoute` 快照在构造时写入 |
| **A3** | `resetTurnRoute()` 在 `run()` 入口 + `defer` 调用，**还原到基线**（不是清空） |
| **A4** | `applyTurnRoute()` 存在但**无调用方** ⇒ `loop.config` 三字段恒等于基线 |
| **A5** | U1–U4 + I3 + I4 全绿 |
| **A6** | **REG1 全绿**（默认关闭时行为零变化） |

### 11.2 MA-P1 验收（本轮修订新增，含健康耦合）

| # | 条件 |
| --- | --- |
| **A13** | `applyTurnRoute()` 内**不出现** `providerhealth` / `Resolver.Resolve` 调用（§5.10 规则 1；U15） |
| **A14** | 健康维度在 turn 内**闩锁**：turn 内翻转健康状态，route **不变**（U15/I7） |
| **A15** | 候选链耗尽 ⇒ 降级到基线 + `source=health_exhausted_baseline`；**未调用** `degradeToParentForHealth`（U17） |
| **A16** | 健康驱动的降级受 `min_dwell_steps` 约束（U16）；**REG11 全绿**（在途特性测试无回归） |

> **A13 是本方案唯一可「机械检查」的验收项**——它不需要理解语义，只需确认函数体内无这两个调用。**建议直接做成静态检查**（正则扫描 `applyTurnRoute` 函数体），使其无法被后续重构悄悄破坏。

### 11.3 MA-P2 验收

| # | 条件 |
| --- | --- |
| **A7** | `enabled=true` 时 step N 上报 ⇒ step N+1 生效；I1/I2/I5 全绿 |
| **A8** | REG7 / REG8 全绿（前缀冻结 + 不暴露 model） |
| **A9** | 工具 schema **不含** provider/model 参数（主设计 `:2002` 硬约束） |
| **A17** | **v4（task_type 收编）**：`predict_task_difficulty` 接受可选 `task_type`/`task_subject`；U20/U21 全绿；缺省 `task_type` 下 REG1/REG8 保持（枚举注入不违反 INV-3） |

### 11.4 MA-P4 验收

| # | 条件 |
| --- | --- |
| **A10** | 6 个事件类型在 `contract.go` 与 `known_types.go` 双登记；**并补登记 `llm.provider.health_opened`**（§6.2 第 4 步）；REG10 + **REG12** 全绿 |
| **A11** | 成本护栏 U14 + REG5 全绿 |
| **A12** | **O5 成本量化已完成**，且结论支持继续（否则回退到 §5 重评） |

---

## 12. 开放项

### 12.1 仍待决策（不阻塞开工）

| # | 开放问题 | 影响 | 建议 |
| --- | --- | --- | --- |
| **O1** | `expert` 是否最终开放 | 成本上限 | 先保持关闭；跑一段时间 `hard` 的成本数据后再决策 |
| **O2** | `allow_escalation_retry` 是否默认开启 | 单 step 成本 +1 次 | 默认 `false`，观察「上报后仍失败」的实际频率再定 |
| **O3** | `NewReActLoop` 所有权收口是否影响 `RunReActWithConfig` 调用方 | 兼容性 | **不阻塞但必须在 MA-P0 内闭环**（A1） |
| **O4** | ~~是否让 LLM 看到「当前使用的 provider/model」~~ | —— | ❌ **已决策：不做**。原动机「LLM 需判断是否已足够强」**不成立**——该判断属 harness 策略而非 LLM 观测量；展示会形成闭环自激并武器化自利性风险 |
| **O5** | 切换 provider/model 的 prompt cache 成本 | 实际收益 | **MA-P4 的准入条件**（门禁 G-C）。已有 `cacheanalytics` 仪表，**无需新建埋点** |
| **O6** | `main_agent.route_*` 是否接入成本告警阈值 | 运维 | 建议接入，但独立于本方案（payload 已预留 token 字段） |
| **O7** | **在途特性（§3.6）是否补一份设计文档** | 本方案 §5.10 的依据强度 | **建议补**。当前本方案的耦合规则只能引用代码，无法引用其设计意图；若其设计意图与代码不符，§5.10 需再修订 |
| **O8** | **`providerhealth` 的闩锁是否也应施加于子 Agent** | 一致性 | 子 Agent 已是「生命周期解析一次」，天然闩锁，**无需改动**。但若将来子 Agent 支持重解析，须同步引入本节规则——**建议在其设计文档中预留该约束**（依赖 O7） |
| **O9** | **`llm.prompt_cache.breaker_tripped` 是否补登记** | 可观测性 | 与本方案无直接功能关系，但同属 §3.4 盲区。建议**随本方案 MA-P4 一并补登记**，成本极低 |

### 12.2 已在姊妹方案中同步的改动（跨文档一致性收尾）

> **状态：三项均已于 2026-09-21 随本文件一并完成**（纯文档改动，不涉及代码）。

| # | 文件 | 改动 | 状态 |
| --- | --- | --- | --- |
| 1 | `docs/plan/task-difficulty-routing-audit-hardening-plan-20260921.md` §2 相关文档表 | 新增一行指向本文件 | ✅ 已完成 |
| 2 | 同上 §2.1（新增小节） | 「编号约定」：该文件内 `G*`/`P*` 为**局部编号**，跨文件引用时加 `SA-` 前缀；本文件用 `MA-` 前缀 | ✅ 已完成 |
| 3 | 同上 §5.2 第三步（新增） | 明确扫描测试的覆盖目录包含本方案的发射点（`internal/agent/loop.go` 的 `emitRuntimeEvent`），并把原「第三步」顺延为「第四步」 | ✅ 已完成 |

> **为什么必须做这三项**：两份方案同处 `docs/plan/` 且都需要阶段编号，若不约定前缀，评审时会因 `P0`/`G1` 撞车而产生误读；而第 3 项是**实施层面的硬依赖**——姊妹方案的扫描测试是防止本方案复现 SA-G2 病因的唯一机制（§8.2 门禁 G-B）。
>
> **本文件对该分析文档的反向引用**：上游 `docs/analysis/main-agent-dynamic-provider-model-switching.md` 此前在仓库内**无任何入站引用**（孤立文档）；本文件 §1.1 / §2 已建立引用关系，使其进入 `docs/plan/` 的文档图谱。

### 12.3 已决策（2026-09-22，`plan.md` 拍板，v4 修订的依据）

| # | 原开放问题 | 决策 | 落点 |
| --- | --- | --- | --- |
| D1 | 难度之外是否让 LLM 返回任务类别 + 说明 | **是**：`task_type`（封闭枚举）+ `task_subject`（短说明）；`task_type` 替换**路由层** `role`（编排层 role 保留） | §5.4、§16 |
| D2 | `task_subject` 与 `rationale` 是否合并 | 不合并，语义不同、并存 | §5.4 |
| D3 | 跨类降档是否需最小确认 | **保留 1 步确认**防边界反跳；同档仍 `downgrade_confirm_steps`；`min_dwell` 不绕过 | §5.5 |
| D4 | 是否进 `spawn_team` 与批次账本 | 是 | 姊妹方案 §6.4 改动点 25–27 |
| D5 | 是否改观测采集与两个前端 | 是（`usage_routes` + micro web client + React `frontend/`） | 姊妹方案 §6.4 改动点 28–29、本文件 §6.2 |

---

## 13. 一句话结论

> 主 Agent 的动态 provider/model 切换**不存在**，但**可行**——因为 per-run 新 loop 与 `loop.config` 单一事实源这条链路**已经在了**，本方案只需补「step 边界的复写能力」与「难度生产者」两件事，无需改动任何消费者。
>
> **但可行性有一个前提（本轮修订新增）**：在途的 provider 健康门禁特性（§3.6）已经把 `Resolver.Resolve` 变成**有状态、可失败**的函数，其自述不变式是「每个子 Agent 生命周期只解析一次」。本方案的逐 step 重解析**与该不变式直接冲突**，若不处理会产生**难度 + 健康双切换源振荡**（MG5），把冷启动成本从每 turn 一次放大到每 step 一次。
>
> 实施路径是 **（G-0：在途特性先落地）→ MA-P0（所有权收口，纯重构可独立合入）→ MA-P1（应用点 + 迟滞 + 健康闩锁）→ MA-P2（预测生产者）→ MA-P3（配置）→ MA-P4（治理 + 成本护栏）**，全程**默认关闭**、**turn 内可还原**、**不落盘**。
>
> **三条红线不可越**：**① 不向 LLM 暴露当前 provider/model**（否则闭环自激）；**② 健康维度必须在 turn 内闩锁**（否则与难度维度叠加成 per-step 振荡）；**③ MA-P4 之前必须完成 O5 成本量化**（否则收益假设不成立）。
>
> 与姊妹方案（子 Agent 审计加固）**功能正交、契约共享**：本方案的新事件必须走同一套契约注册与门禁，**先建门禁、再登记事件**（§8.1）。
>
> **v4 补充（2026-09-22）**：难度生产者扩展为 `difficulty + task_type + task_subject` 三元组；跨类降档以「1 步确认」替代即时生效（§5.5），分类枚举注入不触碰 INV-3（§5.6）；两项均以仓库根 `plan.md` 的 5 项拍板为准（§12.3、§16）。

---

## 14. 本轮修订记录（v2，2026-09-21 第二轮）

**触发**：取证时发现工作区存在**未提交**的在途特性（`providerhealth` + `modelrouting` 候选链），它改变了本方案的一个基础假设。

| # | 修订内容 | 落点 |
| --- | --- | --- |
| 1 | 新增在途特性的完整取证（范围 / 五条语义变化 / 自述不变式 / 无设计文档） | §3.6 |
| 2 | **MG3 性质变更**：从「将来可能发生」改为「此刻已经发生」——`llm.provider.health_opened` 与 `llm.prompt_cache.breaker_tripped` 均已泄漏 | §3.4、§6.2 第 4 步、§9.3 REG12 |
| 3 | 新增 **MG5**（双切换源振荡）、**MG6**（`degradeToParentForHealth` 语义不成立）、**MG7**（`Resolve` error 分支缺失） | §4 |
| 4 | 新增 **§5.10 耦合规则六条**（健康闩锁 / 不绕过迟滞 / 禁止退父 / 区分两类失败 / `source` 四值 / `candidates` 入事件） | §5.10 |
| 5 | §5.8 流程图补 error 分支 | §5.8 |
| 6 | 配置新增 `health_gate` 子节 + 两条 fail-fast 规则（十条） | §6.1、§7.4 |
| 7 | `route_applied` 的 `source` 扩为四值 + 新增 `candidates` 字段 | §6.2 |
| 8 | 新增 §7.6 改动清单（7 项） | §7.6 |
| 9 | **修正原判断**：MA-P0 与姊妹方案「可并行」在在途特性存在时**不成立**；新增第 0 步与门禁 **G-0** | §8.1、§8.2 |
| 10 | 新增测试 U15/U16/U17、I7、REG11/REG12 与两个验证包 | §9 |
| 11 | 新增风险 **R8**（与 R1/R3 同级）、**R9** | §10 |
| 12 | 新增开放项 O7/O8/O9 | §12.1 |
| 13 | 新增 **§11.2 MA-P1 验收 A13–A16**（原 §11.2/§11.3 顺延为 §11.3/§11.4）；A10 增补 `health_opened` 登记与 REG12 | §11 |
| 14 | **编号消歧**：§9.3 回归用例 `R1`–`R12` 重命名为 **`REG1`–`REG12`**，消除与 §10 风险 `R1`–`R9`、§5.4 上游规则 `R1`–`R5` 的三方冲突 | §2.1、§9.3 |

**未改动**：§1 的文档定位、§2 的文档清单、§5.1–§5.7 的语义骨架与 MA-P0/P1/P2 主体设计、§6.4 的非目标边界、§11.1/§11.3/§11.4 的既有 A1–A12。**本轮修订不推翻任何原有决策**，只补齐与在途特性的耦合面，并消除 `R` 前缀的编号歧义。

---

## 15. 实施状态（v3，2026-09-22）

> **性质**：本节是**实施记录**，不是新设计。§1–§14 的设计结论未变；本节只回答三个问题——**哪些已落地、证据在哪、哪些还没落地**。

### 15.1 阶段状态

| 阶段 / 缺口 | 状态 | 落点（符号级） |
| --- | --- | --- |
| MA-P0 所有权收口 | ✅ 已实施 | `agent/loop.go` `NewReActLoop`（值拷贝 + `cloneMainAgentRoutingConfig` 深拷贝，不外泄 `MaxSteps` 规范化） |
| MA-P1 应用点 + 迟滞 | ✅ 已实施 | `agent/main_agent_route.go`：`beginTurnRoute` / `applyPredictedDifficulty` / `resolveMainAgentRoute` / `tripMainAgentCostGuard` / `endTurnRoute` |
| MA-P2 预测生产者 | ✅ 已实施 | `predictTaskDifficultyToolDefinition` / `reportPredictedDifficulty` / `rejectPredictedDifficulty` + `loop.go` 工具面叠加与 meta 分流 + **§5.4 实施补充**（回合级提示片段 `mainAgentRoutingSystemMessage`） |
| MA-P3 配置 | ✅ 已实施 | `agentconfig/main_agent_routing.go`（类型 / `ApplyMainAgentRoutingDefaults` / `ValidateMainAgentRoutingConfig` / `EffectiveMainAgentRoutingConfig`）+ `config.go` 挂载与加载期校验接线 |
| MA-P4 治理 + 成本护栏 | ✅ 已实施 | `internal/events/main_agent_routing.go`（6 个常量）+ `contract.go`（6 类型登记，并**补登记** `llm.provider.health_opened` / `llm.prompt_cache.breaker_tripped`）+ `runtimeobserve/known_types.go` + `contract_test.go` 门禁（MG3 闭环） |
| §5.10 健康耦合（MG5/MG6/MG7） | ✅ 已实施 | `main_agent_route.go`：turn 内闩锁、`source=health_exhausted_baseline`、**不调用** `degradeToParentForHealth`、区分两类失败 |
| **MG8 宿主接线**（v3） | ✅ 已实施 | §7.7 四项；两条宿主路径各自带测试 |
| **§5.11 meta 工具豁免**（v3） | ✅ 已实施 | `agent/doom_loop.go` `semanticToolCallRepeatExempt` 早返回（**仅**指纹维度，预算维度不动） |
| O5 成本量化（A12） | 🟡 **成本侧已实测**（2026-09-22，见本节末「O5 实测记录」）；收益侧（触发频率）待小流量试用 | 仍是 MA-P4 的准入条件，**不是代码缺口**。可测性核实结论：`usage_requests` 按 `(session_id, step, provider, model)` 记录 `prompt_tokens` / `cache_read_tokens` / `cache_creation_tokens`，`main_agent.route_applied` 的 payload 带 `step` / `provider` / `model` / `reasoning_effort` / `route_changed`——二者按 `(session_id, step)` 联表即可得到「切换后首步 `cache_read_tokens≈0` 的全价输入 token」这一冷启动成本口径，无需新增埋点。**已知限制**：会话明细接口当前不返回 step 级 provider/model（`usageanalytics/query.go` `sessionSteps` 扫描后丢弃），故联表须在 SQL/分析层完成，或由在途的观测采集工作单独接线（本方案不改动该文件，避免与其冲突）。**2026-09-22 更新**：成本侧已用**既有真实数据**实测（无需新埋点、无需等新数据）；请求级 `provider/model/cache_read_tokens/cache_status/cache_epoch` 亦可直接由 `/web/api/cache/requests?session_id=…` 读取 |

#### O5 实测记录（2026-09-22，真实数据，只读）

数据源：`~/.aicli/sessions/runtime/usage_analytics.sqlite`（本机真实运行库，`mode=ro` 只读打开；3035 条带 usage 的请求 / 38 个会话），分析脚本 `backend/.tmp/o5_switch_cost.py`。

| 观测 | 数字 |
| --- | --- |
| 发生过跨 provider/model 切换的会话 | 7 个（共 23 个切换点） |
| 跨 provider 切换后**首个请求** | `cache_read_tokens = 0`、`cache_status = reported_zero`、`prompt_tokens` 19.6k–33.2k **全价** |
| 同会话稳态全价 token（同 provider，cache hit） | 中位数 0.7k–3.8k |
| **单次跨 provider 切换增量**（冷启动全价 − 稳态全价） | commandgo→opencode.ai 中位 **+15.8k**（n=5）；opencode.ai→commandgo 中位 **+20.0k**（n=4）；最大单样本 **+31.1k**（827 请求会话，切换后 step 1：prompt 33,166 / cache_read 0） |
| 同 provider 内换 model | 混合：多数仍命中（`cache_read` 19.2k–26.9k，增量 +0.1k–+5.7k）；少数未命中（+9.5k） |
| 切回**已预热**的 provider | 命中（`cache_read` 19k–27k）⇒ 冷启动约「每 provider 每会话/epoch 付一次」，不是每次切换都付 |

**结论（成本侧）**：跨 provider 切换的代价 ≈ **一个完整 prompt 的全价**（样本中 16k–31k token）；同 provider 内换 model 通常便宜一个量级。这为 §5.5 迟滞提供了量化依据，并给出一条设计倾向：**档位优先落在同一 provider 内，跨 provider 升级留给「剩余工作量明显大于一个 prompt」的场景**。

**诚实边界**：这些切换**不是**主 Agent 路由产生的（该特性从未开启，`usage_routes` 表为空），而是手工切换 / failover / 子 Agent 路由产生的。机制相同（换 provider ⇒ 前缀缓存冷启动），故可用于量化**单位成本**；**触发频率**仍需 `enabled=true` 小流量试用取得。

### 15.2 测试状态（诚实版）

**已落地且通过**（`internal/agent` 路由专项用例 **26 项** = `main_agent_route_test.go` 18 + `main_agent_routing_e2e_test.go` 5 + `main_agent_routing_prompt_test.go` 3；另有 `internal/api/skills` 与 `cmd/aicli/commands` 的宿主侧用例，见下表后两行）：

| 覆盖 | 用例 |
| --- | --- |
| 所有权 / 关闭态 | `TestNewReActLoopDeepClonesRoutingConfig`、`TestMainAgentRouteInertWhenDisabled` |
| 基线 / 迟滞 / 护栏 | `TestTurnFloorIsLatchedAndBaselineRestored`、`TestEscalationAppliesImmediately`、`TestDowngradeRequiresConfirmAndDwell`、`TestDowngradeBlockedByMinDwell`、`TestCostGuardResetsRouteAndAllowsLaterEscalation`、`TestHardCostGuardBlocksFurtherEscalation` |
| 上报治理 | `TestInvalidReportsDisableTurnRouting`、`TestExpertRequiresOptIn`、`TestUnlistedDifficultyIsRejected`、`TestSameDifficultyReportIsNoop`、`TestPredictToolDefinitionIsStableAndHidesBackends`、`TestRouteSourceEnumIsClosed` |
| 健康耦合 | `TestTurnFloorConsultsProviderHealth`、`TestInTurnReportLatchesProviderHealth`、`TestMainAgentHealthSourceHasSingleGuardedCallSite`（A13 机械检查）、`TestTurnEndRestoresBaselineAfterHealthDegradedFloor` |
| **v3 新增** | `TestMainAgentRoutingSystemFragmentStableAndGuarded`（U18）、`TestMainAgentRoutingSystemMessageIsPromptOnly`、`TestPredictTaskDifficultyExemptFromDoomLoopRepeat`（U19）、`TestMainAgentRoutingRequestSurfaceEndToEnd`（**I8**）、`TestMainAgentRoutingEscalationReachesNextStepRequest`（**I1**：step 1 上报 `hard` ⇒ step 2 请求头换成 `claude-hard`/`high`，且占位行不含 provider/model） |
| **v3 宿主接线** | `TestCloneAICLIRoutingConfigKeepsMainAgentRouting`、`TestHandlerMainAgentRoutingConfigReadsSnapshot`、`TestApplyAPISessionMainAgentRoutingGate`（REG13，API 侧）、`TestApplyLocalChatMainAgentRoutingHostWiring`、`TestLocalChatMainAgentRoutingConfigIsIndependentFromSubagents`（REG13，CLI 侧） |
| **v3 续作（第二轮）** | `TestMainAgentRoutingMetaOnlyReportDoesNotConsumeStep`（**I2**：`MaxSteps=1` 下 meta-only 响应不吃 step 预算，仍能续轮换到 `claude-hard`）、`TestMainAgentRoutingReportCoexistsWithRealTools`（**I5**：meta 与真实工具同批，`MaxToolCalls=1` 不被 meta 挤占，占位行与真实结果并存）、`TestMainAgentRoutingPreflightReadsLiveRoute`（**I3**：preflight 与请求构建读同一 route，跨 provider 上下文预算随档位收缩）、`TestLocalChatMainAgentRoutingOffsetNotPersisted`（**REG2**，CLI 宿主包） |

**尚未落地**（按风险排序）：

| # | 缺口 | 状态 |
| --- | --- | --- |
| 1 | **I2 / I5**：meta-only 响应**不消耗 step** 的显式断言、上报与真实工具**并存** | ✅ **已闭环**：`TestMainAgentRoutingMetaOnlyReportDoesNotConsumeStep` / `TestMainAgentRoutingReportCoexistsWithRealTools`。灵敏度已取证——把 `mainAgentMetaOnlyStepCredit` 置 0、或令 `budgetedToolCallCount` 退化为 `len(calls)`，两个反向注入各自令对应用例 FAIL，撤销后恢复绿 |
| 2 | **I3**：preflight 与请求构建读同一 route（跨 provider 上下文预算） | ✅ **已闭环**：`TestMainAgentRoutingPreflightReadsLiveRoute`（128k vs 4k 上下文两 provider，宿主 `agent.config` 故意滞后于 live route，断言预算与请求同时改道） |
| 3 | **REG2**：主 Agent offset 不落盘扩展断言 | ✅ **已闭环**：CLI 宿主包 `TestLocalChatMainAgentRoutingOffsetNotPersisted`（turn 内已改道 + 宿主对象/持久化上下文无偏移 + 下一 turn 回基线）。**正对照**：同包 `TestLocalChatRuntimeHostBuildSessionActorUsesChildRouteContext` 对**同一组 key**断言子 Agent 必须落盘 `hard-provider`/`hard-model`/`high`，故主 Agent 的「空值」断言非空洞 |
| 4 | **O5 / A12**：跨 provider 冷启动成本量化 | ⛔ **仍未实施**：需要真实运行数据；收益假设的准入条件，无代码改动 |

### 15.3 验证证据（2026-09-22）

```powershell
# 1) 目标包全量测试（本次实施后）
go test ./internal/agent/ ./internal/agentconfig/ ./internal/events/ `
        ./internal/modelrouting/ ./internal/providerhealth/ -count=1
# → ok（agent 12.6s / agentconfig 3.1s / events 2.0s / modelrouting 1.2s / providerhealth 1.2s）

# 2) 新增用例的定点验证（宿主接线 + 提示片段 + 端到端）
go test ./internal/agent/ ./internal/api/skills/ ./cmd/aicli/commands/ -run `
  'MainAgentRouting|PredictTaskDifficulty|CloneAICLIRoutingConfig|ApplyLocalChatMainAgentRouting|ApplyAPISessionMainAgentRouting|LocalChatMainAgentRoutingConfig|HandlerMainAgentRoutingConfig' -count=1
# → ok（三个包全部通过）

# 3) 构建
go build ./...   # → BUILD_EXIT=0

# 4) 收尾复跑（并发改动落定后）
go build ./...                                              # → BUILD_EXIT=0
go test ./internal/api/skills/ ./cmd/aicli/commands/ -count=1
# → ok（api/skills 23.8s；cmd/aicli/commands 94.0s）
go test ./internal/agent/ -run 'MainAgent|Routing|Difficulty|DoomLoop|Reminder|Route' -count=1
# → ok
```

```powershell
# 5) 第二轮（I2 / I5 / I3 / REG2）定点验证
go test ./internal/agent/ -run 'MainAgentRoutingMetaOnlyReportDoesNotConsumeStep|MainAgentRoutingReportCoexistsWithRealTools|MainAgentRoutingPreflightReadsLiveRoute' -count=1
# → ok
go vet ./internal/agent/                                      # → 干净
go test ./cmd/aicli/commands/ -run 'TestLocalChatMainAgentRoutingOffsetNotPersisted' -count=1
# → ok（1.0s）
go test ./cmd/aicli/commands/ -run 'TestMainAgentRouting|TestLocalChatMainAgentRouting|TestLocalChatRuntimeHostBuildSessionActorUsesChildRouteContext' -count=1 -v
# → ok（PASS: ...UsesChildRouteContext / ...ConfigIsIndependentFromSubagents / ...OffsetNotPersisted）
```

> **灵敏度证明（反证，已撤销）**：① `mainAgentMetaOnlyStepCredit` 置 0 ⇒ `TestMainAgentRoutingMetaOnlyReportDoesNotConsumeStep` FAIL；② `budgetedToolCallCount` 退化为 `len(calls)` ⇒ `TestMainAgentRoutingReportCoexistsWithRealTools` FAIL。两次注入均已完整撤销并复跑恢复绿，故上述用例对 §5.4 的 R2/R3/R4 契约具备真实判别力，而非恒真断言。

> **一条必须记录的干扰项（非本方案改动，已消解）**：验证期间，仓库内有**并发编辑**的 `backend/internal/usageanalytics/ingest_routes.go`（新建于 2026-09-22 08:17:31，属「路由切换观测采集」的独立在途工作）。该文件在写入过程中处于不可编译状态，一度导致 `internal/api/skills`（传递依赖它）与 `cmd/aicli/commands` 的**测试二进制构建**被阻塞。
>
> **判定依据**：报错全部集中在 `internal/usageanalytics`（`routeEventTime` 参数类型、`strconv` 未导入等），与本方案改动的文件集合（`agent` / `agentconfig` / `events` / `api/skills` / `cmd/aicli/commands` 中的路由接线）无交集；`go list -deps ./internal/api/skills` 确认其为**传递依赖**而非直接引用。**结论：该失败既不由本方案引入，也不应由本方案修复**（修复会与在途工作冲突）。**该文件落定后第 4 组复跑全绿**，上述判定得到验证。

### 15.4 实施结论

1. **设计侧**：MA-P0–MA-P4 + §5.10 全部落地，MG1–MG8 **八个缺口全部有对应实现或明确记录**。
2. **默认安全**：`enabled=false` 是唯一默认；关闭态下工具不注册、片段不注入、宿主写入点为空操作——三条路径各有测试。
3. **剩余工作的性质**：I1/I2/I3/I5/REG2 五个**取证缺口已全部闭环**（行为已实现 + loop/宿主级断言 + 反向灵敏度证明，见 §15.2 与 §15.3 第 5 组）；**唯一未完成项是 O5**——**数据缺口**，需要真实运行样本，非代码缺口（可测性核实见 §15.1）。它不阻塞 `enabled=true` 的小流量试用，但 **O5 未完成前不得作为默认配置发布**（A12）。

---

## 16. 修订记录（v4，2026-09-22：task_type 收编）

**触发**：用户对「用 LLM 结构化返回任务类别替代关键词防降档」的 5 项拍板（评审与全文见仓库根 `plan.md`；§12.3 为决策表）。本节只列**对本文件的净改动**，不推翻 v2/v3 的任何结论。

| # | 修订内容 | 落点 |
| --- | --- | --- |
| 1 | `predict_task_difficulty` 增可选 `task_type`（封闭枚举）/`task_subject` 参数；注入片段增有限类别列表（不含类别→model 映射） | §5.4、§7.3、§9.1 U21 |
| 2 | 迟滞状态机类别感知：同档降级仍需 `downgrade_confirm_steps`，**跨类降档收敛为 1 步确认**，`min_dwell_steps` 两类均不绕过；`task_type` 缺省 ⇒ 与 v3 逐字一致 | §5.5、§7.2、§9.1 U20 |
| 3 | INV-3 增 v4 澄清：类别枚举注入是分类语义、不是策略状态 | §5.6 |
| 4 | 事件 schema：`route_applied` 增 `task_type`/`task_subject`，`route_prediction_invalid` 增 `raw_task_type`；进 `usage_routes` 与 `by_task_type` | §6.2、§11.3 A17 |
| 5 | 跨文档：`spawn_team`/批次账本/观测与两个前端的同步项记在姊妹方案 §6.4 改动点 25–29（本文件只定义主 Agent 侧契约） | §12.3 D4/D5 |
| 6 | 头部状态修正为 partially-implemented（v3 已实施未回写头部）；新增 §2 的 `plan.md` 引用与 §12.3 决策表 | 头部、§2、§12.3 |

**实施状态**：**v4 已实施（2026-09-22）**——工具契约增 `task_type`（12 类封闭枚举）/`task_subject`（U21：required 仍仅 difficulty）；注入片段增类别列表与风险倾向纯文字（U18 断言补类别存在，INV-3 复验通过）；迟滞状态机跨类降档 1 步确认、同档 N 步、`min_dwell` 两类均不绕过（U20 正反用例绿：migrate→explore 首报生效、migrate→migrate 仍需 3 确认）；`route_applied`/`route_cleared`/`route_prediction_invalid` 增 `task_type`/`task_subject`（invalid 另带 `raw_task_type`），turn_floor/cost_guard 载荷字段必存在。门禁：`go test ./internal/agent/...` 全绿。v3 的 MA-P0–MA-P4 与 MG1–MG8 结论不变。
**核实口径**：实施后对照 `plan.md` §11 决策表逐行核对 + grep 本文件 `role` 残留（确认仅存「编排层 role / 兼容别名」语义）+ 跑 §9.4 验证命令含 U20/U21。
