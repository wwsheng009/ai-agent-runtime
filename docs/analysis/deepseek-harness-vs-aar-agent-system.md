# deepseek-harness 与 ai-agent-runtime 子 Agent 系统对比分析

> 生成日期：2026-10-16
> 证据来源：deepseek-harness 仓库（`packages/subagent`、`packages/workflow`、`packages/experimental/tool-ralph`、`docs/capability-seams.md`、`docs/subsystems/subagent.md` 等）与本仓库（`backend/internal/agent`、`agentcontrol`、`toolbroker`、`subagentbatch`、`subagentquota`、`supervision`、`toolregistry` 等）源码与文档实地调研。
> 配套背景：博客《DeepSeek Harness 多 Agent 实战》观点核对结论见文末附录。

---

## 1. 摘要

两个项目解决同一个问题——**让主 Agent 把工作拆给子 Agent**，但架构取向不同：

- **deepseek-harness（下称 DSH，TypeScript/Cordis）**：把子 Agent 抽象为一个**能力接缝（capability seam）** `ctx.subagents`，后端（spawn / fork / ACP / Codex / Claude Code / DSH SDK）是按名注册的可插拔 provider；模型侧只暴露薄工具（`subagent`、`send_message`、`interrupt_agent`、`list_agents`），编排由独立的工作流引擎（`ctx.workflowEngine`）和固定脚本（ralph）承担。设计重心在**分层解耦与 provider 可替换性**。
- **ai-agent-runtime（下称 AAR，Go）**：以**具体引擎 + 控制面**取胜——`SubagentScheduler` / `SubagentBatchCoordinator` 提供任务图调度、durable 批次、跨重启恢复；`agentcontrol` 提供持久 Agent 身份图与邮箱；`supervision` 提供通知收敛与控制动作。设计重心在**可靠性、并发安全与可运维**。

**结论**：AAR 在执行可靠性（单写者栅栏、有界重试、durable 恢复、监督面）上普遍领先；DSH 在**架构可插拔性、上下文继承（fork）、结构化输出契约、可编程编排与防钻牛角尖的迭代模式**上有 8 个可直接借鉴的设计点。优先级最高的三个：① provider 能力位模型；② fork 型子代理（含 KV Cache 保持约束）；③ Ralph 式有界全新上下文迭代。

---

## 2. 分析范围

| 维度 | DSH | AAR |
|---|---|---|
| 语言/运行时 | TypeScript，Cordis 插件容器 | Go，单二进制 + SQLite |
| 子 Agent 启动 | `startActivation()`（seam）+ named provider | `spawn_agent`（单发）/ `spawn_subagents`（批次） |
| 编排 | `workflow` 工具（模型写 JS 脚本）+ `ralph`（固定循环） | planner → 静态 DAG（`depends_on`）+ 批次调度 |
| 控制面 | `send_message` / `interrupt_agent` / `list_agents` | `subagent_send` / `subagent_wait` / `subagent_events` / `subagent_close` / `subagent_resume` / `subagent_status` / `subagent_control` / `supervision_*` 等 20+ 工具 |
| 持久化 | session 事件日志 + catalog/descriptor 事件 | AgentRecord / MailboxStore / BatchStore（SQLite） |
| 外部 Agent 后端 | ACP、Codex、Claude Code、DSH SDK | 无（仅本 runtime 进程内子级） |

---

## 3. 架构总览对比

### 3.1 DSH：seam + provider + 薄工具

```
ctx.subagents（能力接缝 = 服务定义）
 ├─ Provider（按名注册，声明能力位）：
 │   spawn-in-process │ fork-in-process │ acp │ codex │ claude-code │ dsh-sdk
 ├─ SubagentManager（统一 activation：准入/容量/消息/冷恢复/结算/释放）
 └─ 消费方（薄适配器）：
     tool-subagent（委派，provider 配置选择后端）
     tool-subagent-control（send_message/interrupt_agent/list_agents，全局名）
     tool-workflow / tool-ralph（经 ctx.workflowEngine 扇出 agent() 调用）
```

关键机制：

- **能力位**：`SubagentCapabilities { agentOptions, outputSchema, depthLimit, toolFilter, persona }`。请求不支持的组合在创建时即失败，而非运行时静默降级。
- **兑现即发布**：`startActivation()` 只在子级接受且句柄可取消后返回；未发布的失败回滚，已发布句柄对应唯一 activation。
- **双投递策略**：`delivery: 'parent'`（结算后通知父模型）与 `delivery: 'caller'`（工作流直接 await，不打扰父级）。这是 subagent 与 workflow 两个消费方能共享同一底座的机制基础。
- **外部后端一等公民**：ACP/Codex/Claude Code 子级"执行一次、不可继续"，但与本地子级共享准入、取消、发现与目录投影。

### 3.2 AAR：引擎 + 控制面 + 工具族

```
spawn_agent / spawn_subagents（委派工具）
 └─ SubagentScheduler（MaxConcurrent/MaxDepth/WriterArbiter/GlobalLimiter/
    电路熔断/任务级重试/路由）
     └─ ChildAgentFactory（路由解析 + 工具面收敛 + 只读继承边界）
 └─ SubagentBatchCoordinator（durable 批次：BatchStore、fencing token、
    terminal outbox、投递重试、心跳、重启后 replay）
 └─ subagentquota.Gate（两条 spawn 路径共享的 live 名额上限）

agentcontrol（持久身份图：AgentRecord/邮箱/reconciler/reclaim）
supervision（通知 digest、ack CAS、control_descendant）
toolbroker（mailbox 信封、监督工具、投递）
modelrouting（difficulty→provider/model，providerhealth 动态健康源）
```

关键机制：

- **结构化任务包与回执**：`SubagentTask`（role/task_type/goal/difficulty/tools_whitelist/depends_on/read_only/agent_type/write_paths…）与 `SubagentResult`（failure_category/retryable/attempt/route receipt/read_only_source…），父代理拿到的是带审计证据的契约。
- **进程级写者栅栏**：`SubagentWriterArbiter`——声明 `write_paths` 不相交的 writer 可并行，重叠或未声明者跨批次串行；delegation 链经 context 继承 claim 防自锁。
- **只读边界不可放宽**：父只读 → 子强制只读，且 `ReadOnlyFilteredTools` 明示父级"为什么写工具被剥掉"。
- **durable 批次**：批次/任务行落库、心跳与进度写回、终态通知 outbox（重试 + 退避 + 幂等去重）、重启后 replay 扫描。

---

## 4. 逐能力对比矩阵

| 能力 | DSH | AAR | 胜出方 | 备注 |
|---|---|---|---|---|
| 子代理启动语义 | `startActivation`（seam），兑现即发布 | scheduler 同步 spawn + durable batch 异步 | 平手 | AAR 的异步/durable 更强；DSH 的发布边界更干净 |
| 上下文继承 | `fork` provider：父"已完成轮次"一次性快照 | 无 fork；子级全新上下文（仅 goal/任务描述） | **DSH** | AAR 借鉴点 ② |
| 结构化输出 | provider 能力位 `outputSchema` + schema 校验 + 结果防御解码 | SubagentResult 固定 schema + failure 分类 | **DSH**（调用方自定义 schema） | AAR 回执契约更丰富但 schema 固定 |
| Provider 可插拔 | named registry，能力位协商 | 单一进程内实现，modelrouting 只管路由 | **DSH** | AAR 借鉴点 ① |
| 外部 Agent 调用 | ACP/Codex/Claude Code/DSH SDK | 无 | **DSH** | AAR 可经 ACP 补齐（借鉴点 ⑧） |
| 编程式编排 | `workflow` 工具：模型写 JS，`agent()/parallel()/pipeline()/phase()` | planner 生成静态 DAG，无模型可编程编排 | **DSH** | AAR 借鉴点 ⑤ |
| 防钻牛角尖迭代 | `ralph`：每轮全新子级 + 有界结构化 handoff | doom_loop 检测（指纹重复告警/可选硬停） | 互补 | AAR 借鉴点 ③ |
| 任务依赖 | Agent Teams（实验，`team_task_create` 依赖链） | `depends_on` 静态 DAG + 自动 verifier 注入 | **AAR** | AAR 的 DAG 是一等契约 |
| 结果投递 | `delivery: parent \| caller` | mailbox 持久投递 + terminal outbox 重试 | 各有所长 | AAR 的 durable 投递更强 |
| 中断语义 | 本地=暂停轮次（排队消息保留），外部=永久停止 | interrupt + close 分离；排队/唤醒/supension gate 完整 | **AAR** | 中断后可 send_message 继续与 DSH 一致 |
| 并发/名额 | `maxActiveSubagents`（每树，默认 8）+ maxDepth | MaxConcurrent（每批 4）+ GlobalLimiter（进程级）+ maxThreads（live 上限）+ MaxDepth + MaxQueueDepth + QueueTimeout | **AAR** | 多层预算更完备 |
| 写冲突防护 | 文档建议"读并行、写主 Agent 汇总"（纯约定） | WriterArbiter 结构化强制（write_paths 不相交才并行） | **AAR** | DSH 靠提示词，AAR 靠机制 |
| 子代理可继续性 | continuable 子级（inbox + 冷恢复） | subagent_resume / subagent_input / mailbox | 平手 | 双方都有；AAR 有 durable 邮箱恢复 |
| 监督/可观测 | session 事件（subagent/start·end·catalog·descriptor） | supervision notification + ack CAS + control_descendant + route receipt + usage ledger | **AAR** | AAR 监督面显著更厚 |
| 父级通知 | 结算后通知父模型（provider 回放规则） | subagent.completed 邮箱 + batch terminal outbox | 平手 | 语义相似，AAR 持久化更强 |
| 失败处理 | 失败分类 + dispose 纪律 | failure_category/retryable/attempt + 只读任务有界自动重试（写任务永不自动重试） | **AAR** | AAR 重试策略更精细 |
| 工具面治理 | toolFilter 能力位（allow/deny） | tools_whitelist + role defaults + read_only 过滤 + tool surface 分层 | 平手 | AAR 有角色默认白名单词汇 |
| 安全隔离 | provider 各自权限系统；同进程可信借约 | permission overlay、foldertrust、worktree isolation、fsscope | **AAR** | AAR 本地安全栈更完整 |
| Token/成本意识 | KV cache 保持设计（fork 前缀复用、schema 稳定） | prompt_cache_breaker、turn budget、tokenestimate、usage analytics | 平手 | 双方都认真对待，取向不同 |
| 跨重启恢复 | session 事件重放（回放事实） | BatchStore replay + reclaim + reconciler | **AAR** | AAR 有显式恢复面 |

**总览**：约 19 项能力里，AAR 在可靠性/并发/监督/安全/恢复 5 类领先，DSH 在可插拔/继承/结构化输出/编排/外部后端 5 类领先，其余平手或互补。两者高度互补——这正是借鉴清单的价值所在。

---

## 5. 可借鉴设计清单（按优先级）

### ① Provider 能力位模型（优先级：高）

**DSH 设计**：`SubagentCapabilities { agentOptions, outputSchema, depthLimit, toolFilter, persona }`。委派工具启动时校验请求的能力位，不支持的组合**创建时即失败**（`UNSUPPORTED_CAPABILITY`），绝不运行时静默降级；能力缺失让相关工具措辞动态变化（如无 `outputSchema` 则不承诺结构化结果）。

**AAR 现状**：路由（provider/model/difficulty）有协商与回退记录（`RouteSource/RouteWarnings/FallbackUsed`），但"子代理请求选项"（工具过滤、只读、agent_type 等）没有统一的能力位协商——policy 拒绝发生在执行期，而非 spawn 准入期。

**借鉴落点**：在 `SubagentTask` 增加与 `SubagentSchedulerConfig` 协商的能力位（如 `wantsStructuredOutput`、`wantsToolFilter`、`wantsPersona`），`ChildAgentFactory.Build` 在创建前一次性校验并产出机器可路由的错误码；同时让 `spawn_subagents` 的工具描述按当前能力位动态措辞，避免模型承诺兑现不了的字段。

### ② fork 型子代理（优先级：高）

**DSH 设计**：`subagent-fork-in-process` 把父级**已配平的已完成轮次**作为子会话初始内容。三个精妙约束：
1. 一次性快照——fork 后父级新产出不再到达子级；
2. 只继承对话，**不继承工具作用域与权限**；
3. KV Cache 保持——父子的消息工具定义逐字节相同、强制继承父 provider/model，使继承前缀可复用缓存；因此"fork 工具不公开子级 LLM 路由选择"。

**AAR 现状**：子级全新上下文（仅继承 goal/任务包），父级前期分析结论必须靠 prompt 转述；`spawn_subagents` 文档甚至专门要求"不要 fork 会话前缀"。跨子任务共享前期上下文的成本高。

**借鉴落点**：为 `spawn_agent` 增加 `context: fresh | fork` 选项。fork 路径取父 session 已完成轮次的快照注入子 session 初始历史；约束照抄 DSH：不继承权限与工具面、fork 时刻即冻结、路由必须继承父级（禁用子级 provider/model 覆盖以保住前缀缓存复用）。durable 化可在 fork 时把继承范围（起止 seq）写入 AgentRecord，冷恢复时不重取。

### ③ Ralph 式有界全新上下文迭代（优先级：高）

**DSH 设计**：`ralph` 工具 = 固定编排脚本（模型只供数据，不能改循环/路由/schema）+ 每轮全新子级 + 有界结构化 handoff（≤16K 字符，状态机校验：`continue` 必须有 nextSteps 且无 blocker；`complete` 必须有 evidence；`blocked` 必须有具体 blocker）+ 轮数预算（默认上限 256）+ 终态四分类（complete/blocked/budget-limited/round-failed）。**共享工作区是长期记忆，只有 handoff 跨轮**。且启动时强制 provider 全新（继承父上下文的 provider 直接挂载失败）。

**AAR 现状**：`doom_loop.go` 是**检测端**——同一语义工具批指纹重复时告警（阈值 4）、可选硬停，轮询/控制类工具豁免。它防止"在同一思路上打转"，但不提供"换新上下文重新出发"的正向机制；planner 静态 DAG 也不适合"逐步打磨同一目标"的迭代场景。

**借鉴落点**：新增 `iterate_goal` 工具（或 `spawn_subagents` 的 `mode: ralph`）：入参仅 `objective + maxRounds`；每轮 spawn 一个全新子级（含自动注入的 round 报告 schema）；handoff 状态机校验与字符上限直接抄 DSH 表；轮预算复用 `turn_budget` 基建。与 doom_loop 组合成完整的"检测→止损→换脑"闭环：doom_loop 触发时，AI 可主动改用 iterate_goal 换新上下文推进。

### ④ 结果防御性解码 + 状态机校验（优先级：中高）

**DSH 设计**：工作流结果跨 provider 边界后在宿主侧**再校验一遍**（`readReport`：键集合精确匹配、字段类型、字符上限、状态机字段约束全查），错误信息带实测值（`oversized handoff (N > max)`）。

**AAR 现状**：`subagent_decode_validation` 已有入参校验；回执字段齐全但 handoff/summary 这类自由文本没有结构化状态机与上限强制。

**借鉴落点**：给子级最终报告定义可选 schema（尤其 iterate_goal 的 handoff）；`subagent_parent_summary` 渲染前对 summary 做归一化校验（非空、trim 相等、上限），超限给出可操作错误而非截断。

### ⑤ 模型可编程编排（workflow 工具）（优先级：中）

**DSH 设计**：模型提交一段纯 JS 编排脚本（`meta`/`script`/`args`），钩子 `agent()/parallel()/pipeline()/phase()/log()`；关键纪律：`parallel` 有 barrier、`pipeline` 无 barrier（多阶段首选）、`agent()` 失败 resolve null 让脚本处理、钩子误用明确终止不降级；脚本跑在沙箱 PTC 进程，受调用 Session 文件策略约束；工具描述里写明"一两件委派请用普通 subagent"。

**AAR 现状**：planner 生成静态 DAG（`depends_on`），表达能力受限于预先生成的图；没有"模型现场写一段并发逻辑"的通道。

**借鉴落点**：借鉴其**脚本契约**而非必上 JS 引擎——可先用 Go 侧受限 DSL（步骤 + barrier/pipeline 语义 + phase 标注），或走 `goja`（Go 的 ES5 解释器）跑受限脚本。关键照抄三条纪律：失败逐项 null 而非整体失败、钩子误用 fatal、以及"何时不用它"的明确指引。

### ⑥ 固定编排的"deployment-owned"原则（优先级：中）

**DSH 设计**：ralph 的循环、provider 路由、schema、handoff 校验全部固定在部署侧，"The model supplies data only"。模型连 maxRounds 都只能请求、不能超过部署上限。

**AAR 现状**：planner DAG 由模型生成，写者栅栏等是运行时强制——方向一致，但"模型可见的编排参数 = 部署固定 + 模型仅可选"这条纪律没有系统化。

**借鉴落点**：为编排类工具（spawn_subagents、未来的 iterate_goal/workflow）统一一个配置分层：部署上限（config）> 宿主覆盖（runtime.yaml）> 模型请求（工具入参），超限报 `X exceeds deployment ceiling` 类错误。当前 `agents.maxThreads` 等已如此，可把它作为编排参数的通用模式写进 docs/design。

### ⑦ 交接报告与跨轮状态的规范化词汇（优先级：中）

**DSH 设计**：术语表（glossary）显式定义 Ralph loop/round/handoff；handoff 是"补充工作区、不取代工作区权威"的一等概念；UI 侧有对应的徽章与结果视图（complete/blocked/round-limit）。

**AAR 现状**：SubagentResult 契约完整但缺少"跨任务共享状态"的词汇——批次间传递依赖任务描述或文件，没有 handoff 概念。

**借鉴落点**：在 docs/glossary（若建）与 `SubagentTask` 中引入 handoff 字段（结构化、有界），用于批次内 writer→verifier 传递与跨批次续作；与 ②③ 联动。

### ⑧ 外部 Agent 后端（ACP 桥）（优先级：低）

**DSH 设计**：ACP/Codex/Claude Code 是一等 provider：执行一次、不可继续、独立权限系统、与本地子级共享统一管理器与目录投影；"Claude Code 可作为 Profile Bundle 安装进 DSH 并被当作子代理调用"。

**AAR 现状**：已有 `internal/acp`（ACP 协议面），但仅作为对外服务面而非"外部 agent 作为子级"的消费面；子级只能是本 runtime 进程内的 agent。

**借鉴落点**：远期可定义 `ExternalAgentProvider` 接口（start/cancel/result），把外部 CLI（如 codex exec、claude）作为一次性子级接入——DSH 的"外部条目是叶子、不授予继续执行"边界可直接沿用。当前优先级低：AAR 的单进程模型已覆盖主要场景。

### ⑨ 工具描述的动态措辞（优先级：低）

**DSH 设计**：`providerWording(inheritsConversation)` 按 provider 能力**动态生成工具 description 与 prompt 参数描述**——fork 型告诉模型"它已看到已完成轮次，只说新内容"；spawn 型要求"自包含任务"。杜绝模型对子级上下文的错误假设。

**AAR 现状**：工具 schema 静态。

**借鉴落点**：若实现 ②（fork），配套此设计成本极低收益明显；单独实现价值有限。

---

## 6. 不建议借鉴的部分（及理由）

| DSH 设计 | 不建议原因 |
|---|---|
| Cordis 插件容器 + patch.yml 组合层 | AAR 的 Go 单二进制 + 显式装配已足够；引入声明式组合层会显著增加复杂度且收益不匹配 |
| provider 注册表多实例化（同名工具多后端） | AAR 场景单一（本机 agent），单实现 + 能力位协商（借鉴点①）即可，无需 registry 抽象 |
| Session 事件日志作为唯一持久事实源 | AAR 的 SQLite BatchStore/AgentRecord 已是更强的持久化；事件溯源改造代价远超收益 |
| ralph 默认 256 轮的预算量级 | 对本地单机成本过高；AAR 若借鉴 ③ 应默认 8–16 轮，上限可配 |

---

## 7. 落地路线建议

**第一阶段（结构准备，约 1–2 周）**
- 能力位协商（①）：`SubagentTask` + `ChildAgentFactory` 前置校验 + 错误码。
- handoff 词汇（⑦）：`SubagentTask/Result` 增加字段 + 校验。

**第二阶段（核心功能，约 2–4 周）**
- fork 上下文继承（②）：spawn_agent 加 `context: fork`；快照、冻结、缓存保持三约束。
- iterate_goal（③）：固定脚本 + handoff 状态机 + 轮预算；与 doom_loop 联动。

**第三阶段（增强，按需）**
- 受限编排 DSL（⑤）+ deployment-owned 参数分层（⑥）。
- 结果防御解码（④）在 ③ 落地时顺带完成。
- 外部 agent provider（⑧）视需求启动 ACP 消费面扩展。

**验证方式**：每个借鉴点以现有测试风格落地（DSH 的对应测试矩阵可作参照：ralph 的状态机校验测试、fork 的前缀复用测试、provider 能力位拒绝测试），并补充 AAR 特有的 durable 恢复与 quota 门禁回归。

---

## 8. 附录：博客观点核对结论（背景）

《DSH 多 Agent 实战》博客与代码库核对的主要偏差（详见上一轮分析）：

1. **ralph 并非"内置开箱即用"**——它是实验包（`experimental/tool-ralph`），默认禁用，需经 `ralph-bundle` 插件显式启用。
2. **4 个派活工具实为 2 个原语 seam（subagent + workflowEngine）上的消费工具**——ralph 是"工作流 + subagent 原语的组合策略"，非独立机制。
3. **fork 是 provider 后端**（或第二个工具实例），不是独立工具；其一次性快照与 KV Cache 约束博客未提。
4. **管理工具的权限设计**（相邻关系寻址、ancestor 血缘凭据、一次性子级隐藏）比博客描述精细得多。
5. **Agent Teams 表述前后矛盾**（先称社区插件后称官方扩展）。
6. 博客的核心使用建议（上下文隔离、星型结构、读并行写汇总、避免过度拆分）与代码设计一致，方向正确。

本分析文档以上述核对结论为背景，聚焦两个代码库的功能与架构对比。
