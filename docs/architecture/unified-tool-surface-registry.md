# 统一工具面注册与接入机制（含 Profile 裁剪）— 架构设计方案

> 状态：设计稿（2026-10-14）。本文是**工具面注册/接入/profile 裁剪统一化**的设计方案；
> profile 体系的既有事实见 [docs/profile/architecture.md](../profile/architecture.md)（已实施），
> 本文不重复其内容，只定义工具面域与其对接方式。
> 现场依据：会话 `session_20261014141903_lb0uSShK`（runtime web，25 工具）vs
> `session_20261014142651_sezoj1oD`（aicli TUI attach，冻结面 55 工具 / 本地直连 86 工具）的实测对比。

---

## 1. 问题陈述

同一个用户问题（"pwd"）在两条入口产生的 LLM 请求相差 **~30.5k prompt tokens**，
其中消息内容差仅 ~1.8k，其余全部来自工具定义差异：

| 入口 | 冻结/请求工具面 | prompt tokens |
| --- | --- | --- |
| runtime web（`/api/agent/chat` 直连） | 25 | 3.6k |
| aicli TUI attach（actor 稳定面） | 55 | 34.1k |
| aicli TUI 本地直连 | 86 | —（同会话 55 是注册到 actor 的子集） |

25 个核心工具两侧**逐个比对零差异**——执行核与核心目录确实统一（E3 抽取的成果）。
分叉发生在**宿主装配层**，且分叉没有任何文档化的边界声明，被 `StableToolSurface`
冻结机制静默固化。本方案的目标：把"哪些工具面在哪个宿主、按什么 profile 裁剪"
变成**单一注册表 + 单一裁剪入口**的可审计机制。

---

## 2. 现状梳理：三层结构与差异归因

### 2.1 三层结构（现状）

```
┌────────────────────────────────────────────────────────────┐
│ L1 执行核（已统一，不动）                                    │
│   internal/agent loop + internal/chat actor                 │
│   E3.4b 后 aicli 默认走 runtimehost.BuildSessionActor       │
├────────────────────────────────────────────────────────────┤
│ L2 核心工具目录（已统一，不动）                              │
│   internal/tools（内置 25 个）                               │
│   internal/goal capability（get_goal/update_goal）           │
│   aiclitools.ExposurePath: shared / actor / runtime_server   │
├────────────────────────────────────────────────────────────┤
│ L3 宿主装配层（分叉点，本方案的改造对象）                    │
│   aicli TUI：toolbroker + 本地 supervisor + 每会话 stdio MCP │
│   runtime-server：handler 内联装配 + runtimeServerToolSurface│
│   gateway connector：connector_backend 独立小面              │
└────────────────────────────────────────────────────────────┘
```

### 2.2 差异归因（86 = 25 核心 + 30 MCP + 31 编排）

| # | 差异块 | 归因 | 定性 |
| --- | --- | --- | --- |
| D1 | 核心面 25 个 | `internal/tools` + goal capability，两侧同源 | ✅ 已统一 |
| D2 | chrome-devtools 30 个 | stdio MCP，配置只在 aicli TUI 会话生效；runtime-server MCP 注册表为空（`configs/mcp.yaml` = `mcpServers: {}`） | ⚠️ **配置未共享** |
| D3 | 编排面 ~31 个（`subagent_*`×17、`job_*`×8、`team_*`×2、`schedule_*`×4、`plan_*`×3、`ask_user_question`） | 后端协调层（`internal/subagentbatch` / `schedule` / `teamsupervisor` / supervision）**已在 runtime-server**，但工具注册层没人把它们包装成 capability 暴露 | ❌ **注册层欠账** |
| D4 | 核心面内部小差异（web 有 `bash`/`execute_shell_command`/`read`；aicli 用 `shell`/`view`） | 两套目录注册点各自维护名单 | ⚠️ 名单漂移风险 |

### 2.3 Profile 现状（与工具面的对接缺口）

profile 体系（`internal/profile`，单一实现点）已具备：
- `ToolPolicySpec`（allowlist/denylist/read_only/sandbox）→ `ResolvedToolPolicy`；
- aicli 侧消费完整：`buildLocalChatToolPolicy` → `SetToolExecutionPolicy`（actor 构建期固化），
  切换经 `applySessionProfileSwitch` + actor 驱逐重建（TUI 半程闭环）；
- runtime-server 侧：`/api/agent/chat` 请求级 `profile` + 会话绑定回落（`handler.go:1963-2004`），
  `profileRuntimeState.ToolPolicy` 已解析出来。

**缺口**：runtime-server 把 `profileState.ToolPolicy` 应用到工具面的路径与 aicli 不同源，
且两侧的"全量基线"本身就不一致（D2/D3）——profile 只能在一个不一致的基线上收窄，
换宿主等于换基线，`/profile use` 的 Switch Report 在跨宿主语义上是不可比的。

---

## 3. 设计目标与非目标

**目标**

1. **单一注册表**：所有模型可见工具（内置/MCP/编排/能力面）在装配期收敛到一个
   `ToolSurfaceBuilder` 输出，宿主差异只能通过**声明式配置**表达，不能通过第二套注册代码表达。
2. **Profile 是唯一裁剪入口**：无论 aicli / runtime-server / connector，工具面 = 基线目录
   → profile 裁剪 → 宿主硬边界（read-only / 子代理深度）三层叠加，前两层跨宿主同源。
3. **可解释**：任一会话可以回答"这个工具为什么在/不在"（来源层 + 裁剪规则 + 生效 profile），
   `/api/runtime/tools` 的响应携带这些元数据。
4. **跨宿主可比**：同一 profile 在 aicli 与 runtime-server 解析出的工具面**名单一致**
   （宿主硬边界除外），Switch Report 的 diff 因此跨宿主有效。

**非目标**

- 不改变 `StableToolSurface` 冻结/prompt-cache 机制（本方案只改"冻结前的装配输入"）。
- 不做请求级动态增删工具（工具面变更仍以 turn 边界为生效点）。
- 不把 aicli TUI 的 UI 状态型工具（`plan_*`、`ask_user_question`）搬进 runtime-server。
- 不改变 foldertrust/D29 门控语义（profile 裁剪仍只收窄，prompts 门控照旧）。

---

## 4. 目标架构

### 4.1 单一注册表：ToolSurfaceBuilder

```
                    ┌──────────────────────────────────────────────┐
  L2 目录（既有）    │ internal/tools（内置）                        │
                    │ internal/goal + 未来 capability（编排/调度）  │
                    │ aiclitools.Registry（ExposurePath 过滤）      │
                    └──────────────┬───────────────────────────────┘
                                   │ 全量候选（按 ExposurePath）
                    ┌──────────────▼───────────────────────────────┐
  装配器（新增）     │ internal/toolsurface.Builder                  │
                    │  1. 候选收集：core ∪ capability ∪ MCP(声明来源)│
                    │  2. profile 裁剪：ToolPolicySpec（唯一裁剪口） │
                    │  3. 宿主硬边界：read_only / 子代理深度 / 策略  │
                    │  4. 产出 Surface（定义列表 + 来源账本）        │
                    └───────┬──────────────────┬────────────────────┘
                            │                  │
              ┌─────────────▼───┐   ┌──────────▼──────────────┐
  宿主        │ aicli TUI 装配   │   │ runtime-server 装配      │
  （薄适配）   │ (替换散装注册点) │   │ (替换 runtimeServerTool…)│
              └─────────────────┘   └─────────────────────────┘
```

关键约束：

- **Builder 是唯一装配实现点**（对齐 profile 的 I5"单一实现点"不变量）：宿主代码只提供
  输入（workspace、MCP 清单、宿主硬边界、profile ref），不再各自拼装工具名单。
  现状中 `handler.go` 内联路径、`session_agent_build.go`、`chat_actor_host.go` 三处拼装
  收敛为对 Builder 的三次调用。
- **MCP 清单来源统一**：MCP 声明走既有 profile 路径解析链（`ResolvedPaths.WorkspaceMCPFile`
  → 用户级 → 项目级 → 进程级），宿主不再各自持有清单。aicli TUI 的每会话 stdio MCP
  保留（生命周期属于宿主），但其**声明**必须登记进同一清单来源，runtime-server 才能对
  同一 workspace 解析出同一 MCP 面（连接由各自宿主负责）。
- **编排能力 capability 化**（D3 消解）：`subagentbatch` / `schedule` / `team` 协调层已在
  runtime-server 进程内，按 goal capability 的既有模式包装成 `aiclitools.Capability`
  （`Exposure: [shared, actor, runtime_server]`），注册进 Builder 的候选收集。
  `job_*` 的执行端点同进程，无回连问题。

### 4.2 Surface 账本（可解释性）

Builder 产出的不只是 `[]ToolDefinition`，还有来源账本：

```go
type Surface struct {
    Tools   []types.ToolDefinition
    Ledger  []ToolLedgerEntry // 每工具一条：name, source(core|capability:<pkg>|mcp:<server>),
                              // exposure, kept_by(基线|profile:allow|profile:deny-exempt|host),
                              // dropped_by(profile:deny|host:read_only|mcp:disabled), schema_hash
}
```

- `/api/runtime/tools` 与 TUI `/tools` 面都暴露账本（现响应只有 name/description）。
- `StableToolSurfaceFingerprint` 继续按冻结面计算；账本随 Surface 一起冻结，
  使"为什么这个 turn 没有这个工具"可事后回答（对齐 profile 的 I7"切换可解释"）。

### 4.3 Profile 裁剪的统一接入

现状：aicli 侧裁剪作用于 `ToolExecutionPolicy`（执行策略，运行时拦截），
web 侧 `profileState.ToolPolicy` 的应用路径不同源。统一为**双落点、单语义**：

| 落点 | 作用 | 说明 |
| --- | --- | --- |
| Builder 装配期裁剪 | 决定**请求面**（哪些 schema 进 prompt） | deny 的工具不进请求 → 省 token、模型看不见 |
| ToolExecutionPolicy（执行期） | 决定**执行面**（允许调用哪些） | 保留：执行策略还承担 read-only / 子代理策略 / 能力交集 |

deny 在装配期生效后，执行策略的名单由 Builder 同步生成（策略 = Surface 的执行投影），
消除"面里有但策略拦"与"策略放但面里没有"两种漂移。`profile use` 切换链路不变：
解析 → 写绑定 → 驱逐 actor → 下一 turn 用 Builder 重建（D20/A3 语义保留）。

### 4.4 各宿主的最终工具面（目标态）

| 宿主 | 基线（Builder 无 profile） | 说明 |
| --- | --- | --- |
| runtime-server web | 核心 25 + 编排 ~31 ≈ 56 | MCP 按 workspace 清单解析（chrome-devtools 声明进清单后 +30） |
| aicli TUI attach | 核心 25 + 编排 ~31 + MCP（同清单） | 与 web 同面；`plan_*`/`ask_user_question` 留在 TUI 本地直连面 |
| aicli TUI 本地直连 | 同上 + UI 工具 | UI 工具不注册进 actor 稳定面（维持现状边界） |
| gateway connector | 核心面 + connector 声明 | 经同一 Builder，profile 可裁剪 |

预期 token 成本：编排面 ~+3k（schema 小）、MCP 共享 ~+10k（可被 prompt cache 摊薄，
aicli 侧已实测 `cache_read_reported: true`）。提供 per-profile 的 MCP 编排面开关
（`mcp.use_servers` / `tools.denylist` 已有机制），让"省 token"成为显式选择而非架构分歧。

---

## 5. 实施计划（三阶段，每阶段可独立验收）

### 阶段 1：编排能力 capability 化（D3，收益最高、schema 最小）

1. 新增 `internal/orchestrationcap`（或并入现有 capability 包）：把
   `subagent_*` / `job_*` / `team_*` / `schedule_*` 包装为 `aiclitools.Capability`，
   `Exposure: [shared, actor, runtime_server]`；执行实现注入 runtime-server 侧既有协调层
   （aicli TUI 侧复用现有 broker 实现作为 Execute 适配）。
2. `runtimeServerToolSurfaceForSession` 注册新 capability 组。
3. 验收：web 会话 `/api/runtime/tools` 包含编排面；web 直连请求 tools 数 = 56（无 MCP 时）；
   `spawn_subagents` 在 web 会话可发起并走 durable batch（与 TUI 同一存储）。
   测试：capability 注册表一致性 + actor 稳定面冻结 + web 直连 e2e 各一组。

### 阶段 2：ToolSurfaceBuilder 收敛（D4 + 装配点统一）

1. 新增 `internal/toolsurface.Builder`（候选收集/裁剪/账本），先接 runtime-server
   `session_agent_build.go` 与 handler 内联路径（三处拼装 → 一次调用）。
2. aicli `chat_actor_host.go` 切到同一 Builder（`buildLocalChatToolPolicy` 的输入改由
   Surface 提供）；`/api/runtime/tools` 与 TUI `/tools` 挂账本。
3. 核心面名单去重：`bash`/`execute_shell_command`/`read` 与 `shell`/`view` 的映射
   在 Builder 内归一（别名机制），两侧冻结面 diff 归零。
4. 验收：同一 workspace、同一 profile（或同无 profile）下，aicli attach 冻结面与
   web `/api/runtime/tools` 名单**完全一致**（新增回归测试钉死）。

### 阶段 3：MCP 清单共享 + profile 裁剪双落点（D2 + §4.3）

1. MCP 声明统一走 profile 路径链；aicli TUI 会话级 stdio MCP 的声明登记进同一来源
   （保持进程生命周期在宿主）。runtime-server 按 workspace 解析同一清单并连接。
2. profile `ToolPolicySpec` 在 Builder 装配期裁剪请求面；`ToolExecutionPolicy` 由
   Surface 投影生成；Switch Report 增加"基线差异"字段（跨宿主可比的前提）。
3. 验收：`/profile use` 在 web 与 TUI 各执行一次，Report 的 `changed.tools` 名单一致；
   deny 的工具不再出现在下一 turn 请求体（http 日志断言）；chrome-devtools 在
   runtime-server 会话按 workspace 清单出现/缺席可配置。

### 风险与对策

| 风险 | 对策 |
| --- | --- |
| 编排面暴露扩大模型攻击面（web 会话可 spawn 子代理） | capability 沿用 `permission_mode` / read-only 策略；`ExposureRuntimeServer` 面默认继承会话策略，不做静默放宽（对齐 profile I1"只收窄"） |
| token 成本上升 | MCP 共享为显式配置 + profile 可裁剪；编排面 schema 精简（参数描述压缩在 capability 定义处统一做） |
| 冻结面漂移导致 prompt cache 失效 | Builder 只在 turn 边界重建（`InvalidateStableToolSurface` 既有语义）；账本指纹变化才触发失效 |
| 双落点裁剪语义分叉 | 执行策略一律从 Surface 投影生成（禁止旁路 SetToolExecutionPolicy 名单），加静态测试断言 |

---

## 6. 不变量（新增，与 profile I1–I8 并列）

| # | 不变量 |
| --- | --- |
| T1 | 工具面装配只有 Builder 一个实现点；宿主装配代码不得自拼名单 |
| T2 | 同 workspace + 同 profile 跨宿主解析出的基线面名单一致（宿主硬边界除外） |
| T3 | 每个工具可回答来源与保留/剔除原因（账本随冻结面持久） |
| T4 | profile 裁剪先于宿主硬边界；硬边界只能再收窄，不得放大 |
| T5 | 工具面变更只发生在 turn 边界；在途 turn 的冻结前缀不动（沿用既有语义） |

---

## 7. 相关文档

- profile 体系（已实施）：[docs/profile/architecture.md](../profile/architecture.md) /
  [design.md](../profile/design.md) / [configuration.md](../profile/configuration.md)
- 执行核统一：`docs/automation-tasks/aicli-executor-reuse-design.md`（E3.4 系列）
- MCP 集成：[docs/mcp/mcp-tool-llm-integration.md](../mcp/mcp-tool-llm-integration.md)
- 现场依据：会话 `session_20261014141903_lb0uSShK` / `session_20261014142651_sezoj1oD`
  用量明细（usageanalytics）与 aicli chat-logs http 请求体解剖（2026-10-14）。

---

## 8. 特有工具审计与适配矩阵（2026-10-14 补充）

> 结论先行：**不存在天生绑定 aicli TUI 的工具**。86 − 25 = 61 个差异工具中，
> 30 个是 MCP 配置问题（§2.2 D2），31 个编排工具的**执行层已全部在 runtime-server
> 进程内**（`applyAgentRuntimeServices`，handler.go:5652-5763），缺的是注册/观测/
> 交互控制器的适配。下表逐族给出适配路径。

### 8.1 审计方法

- 执行层归属：`broker.Definitions()`（toolbroker/broker.go:244）按能力字段是否为 nil
  门控产出（"悬空工具比缺少工具更糟"）；server 侧接线点在
  `applyAgentRuntimeServices`（hub/team store/supervision store/background manager）。
- 观测口径：`/api/runtime/sessions/{id}/runtime/tools` 读
  `StableToolSurface`（actor 路径）或 `runtimeServerToolSurfaceForSession`
  （MCP + goal capability 回退）——**不含 broker 定义**，与 LLM 实际请求面存在口径缺口。
- TUI 实测：attach 会话冻结面 55（25 核心 + 30 MCP），本地直连请求 86
  （+31 broker 工具留在 TUI 本地，未注册进 actor 稳定面）。

### 8.2 适配矩阵（31 个编排工具逐族）

| 工具族 | 数量 | server 执行层现状 | 缺口 | 适配动作 | 阶段 |
| --- | --- | --- | --- | --- | --- |
| `job_*`（start/output/cancel/monitor/control/input/list/search） | 8 | ✅ `broker.Background` = `h.getBackgroundManager(config)`（handler.go:5750-5763），durable 作业在 server 进程内 | 无执行缺口；观测端点不显示 | 无需适配执行；阶段 2 让 Builder 把 broker 定义纳入 `/runtime/tools` 与冻结面 | 2 |
| `spawn_subagents` + `subagent_*`（spawn/wait/status/events/input/followup/approve/close/control/inspect/list/resume/send/apply_worktree/discard/ack_lifecycle） | 17 | ✅ `broker.AgentSessions = &sessionAgentController{handler: h}` + `broker.Supervision = newHandlerSupervisionToolController(h)`（handler.go:5682/5728-5731），durable batch store 在 server | 同上 | 同上；web 会话 spawn 走同一 durable batch 存储 | 2 |
| `team_spawn` / `team_wait` | 2 | ✅ `broker` 经 `h.getTeamStore()` 接线（handler.go:5686-5716）；`DefinitionsForContext` 按 active team run 过滤（team_tools.go:48） | 同上 | 同上（含"无 active run 时隐藏 team 专属工具"的上下文语义保留） | 2 |
| `schedule_*`（create/list/update/delete） | 4 | ✅ `h.scheduleService`（handler.go:360，gateway 进程内 schedule.Service + sqlite store） | 无执行缺口 | 同上 | 2 |
| `plan_enter` / `plan_exit` / `plan_review` | 3 | ⚠️ broker 字段 `PlanMode`/`PlanReview` 是 TUI 本地控制器；server 侧 `internal/planmode` 已被 chat actor 引用（挂起/恢复/压缩），但 web 直连路径未接 PlanMode 控制器 | 交互控制器缺适配 | 把 plan mode 控制器接到 web 路径：actor 侧 planmode 状态 + server 审批通道（已有 pending_approval 基建）；`plan_review` 的交互回合走 P1-7 待交互注册表 | 2（后置可独立） |
| `ask_user_question` | 1 | ⚠️ broker 版本面向 TUI 本地 stdin；server 已有 `pending_question` 通道（P1-7 待交互注册表，`/runtime` 快照已暴露） | 交互通道需对接 | 定义保留、执行改写：web 会话调用时写入 pending_question 并等待 resume（与审批同一唤醒机制） | 2（后置可独立） |

### 8.3 观测口径修复（适配的前置）

`/api/runtime/sessions/{id}/runtime/tools` 当前只反映 actor 冻结面或 MCP+goal 回退，
**不反映 web 直连请求的真实工具面**。这直接导致了本次审计的误判起点（"web 只有 25 工具"）。
修复：该端点改为消费 Builder 产出的 Surface（§4.1），并附来源账本（§4.2），
使"模型这次请求看见哪些工具"有了权威只读视图。

**运行时验证步骤**（阶段 2 验收前置）：
1. web 会话发起一个含 `job_start` 调用的回合，从 server 侧 http 捕获（或 provider 日志）
   确认请求 tools 数组是否含 broker 定义；
2. 若含 → 缺口仅在观测端点（纯阶段 2 范畴）；若不含 → 排查 inline 路径
   `applyAgentRuntimeServices` 的字段接线条件（如 `getBackgroundManager` 的 config 依赖）
   并在 Builder 收敛时一并修复。

### 8.4 真正保留在 TUI 本地的面（非"特有工具"）

| 项 | 原因 |
| --- | --- |
| TUI 键盘/渲染/补全等 UI 能力 | 不是模型工具，不进任何工具面 |
| TUI 本地 stdio MCP 进程生命周期 | 进程归宿主，**声明**进统一清单（§4.1）；server 侧按同一声明自行连接 |
| `plan_*` 的 TUI 交互体验（编辑器/评审 UI） | 交互层保留 TUI 实现，执行语义走 server 通道（同 ask_user_question 模式） |

> 设计原则重申（T2 的推论）：**同一会话从两个入口进入时，模型可见工具面必须一致**；
> 宿主差异只允许体现在"谁承载交互"（stdin/stdout vs SSE/REST），不允许体现在
> "模型能调用什么"。工具定义的 schema 与描述在 capability 定义处单点维护，
> 两个入口天然同文（消除 schema 漂移即消除行为偏移）。

### 8.5 对实施计划的影响

- 阶段 1（编排 capability 化）**范围收窄**：执行层已接线，capability 包装只剩
  "把 broker.Definitions() 纳入候选收集 + ExposurePath 标注"，工作量比原估小；
  `plan_*` / `ask_user_question` 的交互适配从阶段 1 挪到独立后置项（依赖
  pending_approval/question 唤醒链路的 e2e）。
- 阶段 2 新增前置项：`/runtime/tools` 口径修复（§8.3）。
- 验收标准不变：同一 workspace + 同 profile 下，aicli attach 冻结面与 web
  `/runtime/tools` 名单完全一致（含账本）。

---

## 9. 运行时验证结论（2026-10-14 深度核查）

### 9.1 接线事实（代码已证实）

runtime-server（`cmd/runtime-server`，端口 8101，前端 Vite 5193 代理）的 web inline 路径：

- `bootstrapManager.ApplyToSkillsHandler(handler)`（main.go:1191）→ `SetSessionManager`
  （bootstrap/manager.go:607）——sessionManager **已接线**；
- `getSessionHub()` 惰性建 hub（session_runtime_support.go:4500-4505）；
- `applyAgentRuntimeServices`（handler.go:5650+）的 hub 分支成立：
  `broker.AgentSessions / ExecutionSupervisor / Supervision / TeamStore / Background`
  全部非 nil（supervision/team/background 的 store 都在 main.go 显式 Set）；
- `computeAvailableTools`（loop.go:4244-4255）**明确包含** `broker.DefinitionsForContext(ctx)`。

### 9.2 矛盾与两种候选根因

实测数学：`lb0uSShK` 两步 prompt 3572/3679，消息 layout 2365/2426 → 工具定义差额
≈1207/1253 tokens。25 个核心工具的 schema 体量与此吻合；**31 个 broker 工具
（含 job_* 的长 description）至少再占 ~2-3k tokens，塞不进这个差额**。

因此 web 直连请求面实际不含 broker 定义，与 9.1 的代码结论矛盾。两种候选根因
（阶段 2 开工前必须先裁决，验证成本各一次运行）：

| # | 假设 | 裁决方法 |
| --- | --- | --- |
| H1 | 运行中的 `runtime-server.exe` 是旧二进制（早于 broker 接线/supervision 注入提交），代码是新的、进程是旧的 | 对比进程启动时间与最近相关提交时间；`go build` 后重启再发一 turn，看请求面是否变为 ~56 |
| H2 | 运行中二进制不旧，但请求面在 `computeAvailableTools` 之后被进一步过滤：`filterToolDefinitionsByShouldList`（tool_list.go:57，`ShouldListMetadata`）或 `filterPolicyBlockedToolDefinitions`（策略名单）把 broker 定义丢掉 | 给 `computeAvailableTools` 末尾加一行诊断日志（tools 名单 + 各过滤阶段计数），重启后发一 turn 读日志 |

### 9.3 观测端点的确定性缺口（无需裁决即成立）

无论 H1/H2，`GET /sessions/{id}/runtime/tools` 的口径缺口独立存在：

- `lb0uSShK`：`state=null`（从未进 durable actor）→ 端点回退到
  `runtimeServerToolSurfaceForSession`（MCP + goal capability）→ 25 个；
- `sezoj1oD`：aicli attach 进过 actor → 端点读 `StableToolSurface`（55 个，含 MCP）；
- 两条路径都**不反映 web 直连请求的真实工具面**（后者每次 inline 现算）。

阶段 2 的第一个交付就是统一这个口径（Builder Surface + 账本，§4.1/4.2），
使"端点显示的"与"请求携带的"同源——这正是本次全部误判的源头。

### 9.4 对实施计划的最终修订

1. **阶段 0（新增，半天）**：裁决 H1/H2（9.2），确认 web 请求面的真实构成。
   若 H1 → 升级二进制后重测两会话，分叉可能自动收窄大半；若 H2 → 过滤点即修复点。
2. 阶段 1（编排 capability 注册）与阶段 2（Builder 收敛 + 观测口径）按 §5 执行，
   但阶段 2 的第一交付改为 §9.3 的端点统一。
3. `plan_*` / `ask_user_question` 交互适配维持后置项不变。

---

## 10. 阶段 0 裁决结果（2026-10-14 执行完毕）

### 10.1 裁决：H1 / H2 均被否定，真相是"当前代码没有此问题"

**执行过程**：
1. H1 否定：`runtime-server.exe` 进程 13:42:07 启动，二进制 13:41:56 编译，
   HEAD = `7251c498`（12:43，含全部 broker 接线提交——接线代码 2026-09-26 即存在）。
   不是旧二进制。
2. 加 `AICLI_TOOL_SURFACE_DEBUG` 诊断（`computeAvailableTools` 末尾输出最终名单 +
   broker 定义数 + MCP 数 + 策略过滤计数），重编译重启，发 web 直连 turn 实测：

```
[tool-surface-debug] final=51 mcp=23 broker_defs=31 filtered=[]
tools=[aicli_exec ... job_start ... spawn_subagents ... schedule_* ... team_spawn ...]
```

LLM 出站日志交叉印证：`tool_count: 51`。**当前 HEAD 的 web 直连请求面 = 51 工具**
（核心 20 + broker 编排 31），`filtered=[]` 即无策略过滤——H2（后续过滤）也否定。

3. 裁决后已还原：诊断代码删除、`loop.go` 回到 git 干净态、正式二进制重编译重启
   （healthz `commit=7251c498` 确认）。

### 10.2 `lb0uSShK`（25 工具 / 3.5k prompt）的最终解释

该会话 14:19 运行于同一新进程，但请求面只有 ~25 工具体量。结合其
`session_compact_skipped`（`token_before: 1126`）与 `/runtime/tools` 的 MCP+goal
回退口径（23 MCP + 2 goal = 25），其请求面构成与 **MCP 工具 schema 为主**的
回退面一致。两种可能（不阻断主线，留观测项）：

- 该 turn 命中了**simple-goal 投影**（`projectSimpleGoalToolSurface`，"pwd" 属于
  simple goal 词表 → 请求面被投影裁剪）——与 3.5k 体量最吻合；
- 或当时进程内 broker 接线分支未走全（无运行时证据，不采信）。

**对架构方案的净影响**：分叉不是"接线缺失"，而是**口径与投影语义**：
1. `/runtime/tools` 观测端点与真实请求面不同源（已实锤，阶段 2 第一交付不变）；
2. `projectSimpleGoalToolSurface` 会在 simple goal 时把请求面裁到极小面——这是
   token 优化的**有意行为**，但它与 aicli TUI 侧（TUI 路径 `allowSimpleGoalProjection`
   同样生效）的相互作用没有被 `/runtime/tools` 或任何观测面表达，造成"看起来两套路线"
   的观感。阶段 2 的账本（Surface Ledger）必须把 `projected_by: simple_goal` 记进去。

### 10.3 修订后的阶段定义（最终版）

| 阶段 | 内容 | 状态 |
| --- | --- | --- |
| 0 | H1/H2 裁决 → 否定；定位真实分叉 = 观测口径 + simple-goal 投影不可见 | ✅ 本轮完成 |
| 1 | 编排 capability 标注（broker 定义已进请求面，只需 ExposurePath/账本元数据补全） | 工作量比原估再降 |
| 2 | ToolSurfaceBuilder 收敛 + `/runtime/tools` 口径统一（含 `projected_by` 账本字段） | 主战场 |
| 3 | MCP 清单共享 + profile 裁剪双落点 | 不变 |
| 后置 | `plan_*` / `ask_user_question` 交互通道对接（pending_approval/question 唤醒链） | 不变 |

> 复测入口（阶段 2 验收用）：同一 workspace（`E:\temp`）发同一 prompt，
> 对比 web 请求 `tool_count`（LLM 出站日志）与 `/runtime/tools` 响应 `count` +
> 账本；aicli 侧同 prompt 对比 chat-logs http 请求 `body_json.tools`。

---

## 11. 阶段 2 第一交付：观测口径统一（2026-10-14 已实施）

### 11.1 实现

`sessionRuntimeToolDefinitions`（session_runtime_handlers.go）改为三级口径：

1. actor 冻结面（`StableToolSurface`）——actor 路径真实请求前缀，最优先（不变）；
2. **新增 live 请求面**：`sessionLiveRequestSurface` 按与 inline web turn 相同的
   `buildSessionAgentForContext` 装配同一个 agent，取
   `agent.CollectToolCatalogDefinitions`（冻结面前的完整候选目录，单一实现点：
   内置核心 + MCP + scheduler + broker 四源同采）；
3. 兜底：装配不可得（无 runtime config / 无会话行 / 空目录）时退回 MCP+goal 旧口径。

刻意不走 profile 解析：基线目录与冻结面 eligibility 同口径（profile 裁剪作用于
执行策略与后续 Builder 收敛，不改变候选目录）。

### 11.2 真实端点验证（runtime-server.exe 重启后）

| 会话 | 修复前 | 修复后 | 解释 |
| --- | --- | --- | --- |
| `lb0uSShK`（web，workspace=E:\temp） | 25（MCP+goal 回退，与请求面无关） | **53** | live 目录 = 核心 20 + broker 31 + 该 workspace 的 MCP 面 |
| `sezoj1oD`（aicli attach，workspace=本项目） | 55（冻结面） | **83** | live 目录 + 项目级 chrome-devtools MCP 30 个（该 workspace 的 MCP 声明命中） |

**双入口对比结论**：剩余差异（30 个 devtools 工具）完全由 workspace 层级 MCP
解析解释——同一清单来源、不同 workspace 命中不同 MCP 集，这正是 §4.1"MCP 声明
统一来源"的预期行为，不再是口径分裂。核心+编排面（53）两入口完全一致。

### 11.3 验证与收尾

- `go build` / `go vet` / `go test ./internal/api/runtimeapi/`（含 4 个既有
  `TestListSessionRuntimeTools_*` 全过，43.6s）；
- 观测端点与 LLM 请求面自此同源（同一装配函数），阶段 0 的诊断需求消失。

### 11.4 剩余工作（移交后续）

- Surface 账本（`projected_by: simple_goal` 等来源字段，§4.2）——让 simple-goal
  投影可见；
- `SimpleGoal` 投影的跨入口行为对齐说明（aicli TUI 与 web 同语义，已确认同一实现点）；
- 阶段 3：MCP 清单共享的配置化（`mcpServers` 进程级注册链）与 profile 裁剪双落点；
- 后置：`plan_*` / `ask_user_question` 交互通道对接。

---

## 12. 阶段 2 第二交付：来源账本（2026-10-14 已实施）

### 12.1 实现

`sessionLiveRequestSurface` 为每个工具打 `metadata.surface_source` 标注
（`toolSurfaceSourceLabel`，单一分类函数）：

| 标签 | 判据 | 数量（实测） |
| --- | --- | --- |
| `mcp:<server>` | 定义带 `mcp_name`（含 goal capability 的 `aicli_goal` 包装） | 2 |
| `orchestration` | `broker.IsBrokerTool`（job/subagent/team/schedule/plan/question 家族） | 31 |
| `capability` | goal 工具名直配（保留给未来非 MCP 包装的 capability 面） | 0（当前都经 aicli_goal 包装） |
| `core` | 其余内置（tools.Manager 经 AgentAdapter） | 20 |

标注不覆盖既有 `surface_source`（幂等）；观测面据此可回答"这个工具从哪来"（§4.2）。

### 12.2 真实端点验证（重启后）

两会话账本**完全一致**：

```
lb0uSShK / sezoj1oD：count=53
  orchestration=31, core=20, mcp:aicli_goal=2
```

与阶段 0 诊断实测的请求面构成（final=51 + goal 2）对齐；此前 sezoj 偶发 83
是 actor 冻结面含 chrome-devtools 的旧快照读点，live 统一口径下两会话同源。

### 12.3 测试

新增 `TestListSessionRuntimeTools_LiveSurfaceCarriesSourceLedger`：
live 目录覆盖三源 + `job_start→orchestration` / `get_goal→mcp:aicli_goal`
（goal 经 `aicli_goal` capability MCP 包装，`mcp_name` 优先——语义正确，
`capability` 标签留给未来非包装面）。既有 4 个口径用例 + 全包 42s 回归 +
vet 全绿。

### 12.4 剩余（移交后续）

- `projected_by: simple_goal`（simple-goal 投影的账本字段）需在 loop 侧投影点
  埋账本，属 Builder 收敛（§5 阶段 2 主体）；
- 阶段 3：MCP 清单配置化 + profile 裁剪双落点；
- 后置：`plan_*` / `ask_user_question` 交互通道对接。

---

## 13. 阶段 2 第三交付：simple-goal 投影账本（2026-10-14 已实施 + 端到端验证）

### 13.1 实现（三处，最小侵入）

1. `agent/loop.go`：`ReActLoop.lastToolSurfaceProjection`（atomic.Value）——
   `computeAvailableTools` 在投影**实际收缩**目录时置 `simple_goal`，否则清空
   （词表命中但目录无对应工具不算投影，防误标；显式禁用投影的 actor 稳定面路径
   同样清空，防 loop 复用跨 turn 残留）。
2. `agent/loop.go` think() 请求装配：投影标记写入
   `req.Metadata["tool_surface_projected"]`——与既有 `surface_filtered_tools`
   同一套可审计 metadata 口径，自动随 `llm.request.started` 事件与 http debug 链路。
3. `runtimeapi/handler.go` 日志白名单：`tool_surface_projected` 进 LLM upstream
   request 日志字段（注意键在 `RequestMetadata` 顶层而非 diagnostics 子 map——
   首版读错路径已修正）。

### 13.2 端到端验证（真实服务器 + "pwd" turn）

```
ts=16:51:57 tools=1 projected=simple_goal key=session_20261014141903_lb0uSShK
```

- `tools=1`：51 → 1（pwd 词表 shell 家族经 `optimizeModelToolSurface` 别名收敛后
  仅剩 `shell`）——**这解释了 lb0uSShK 最初 3.5k prompt 的全部构成**；
- `projected=simple_goal`：投影从"完全不可见"变为请求日志一行可查。

### 13.3 测试

新增 `TestComputeAvailableToolsRecordsSimpleGoalProjectionLedger`（专用夹具
`projectionLedgerMCPManager` 提供 ls/glob/view/shell/bash 目录）：三断言——
非投影目标清标记 / 投影命中置标记且目录实际收缩（ElementsMatch ls+glob）/
显式禁用投影不误标。期间修正两处实现缺陷（词表命中但无收缩不置位、loop 复用
残留标记）——账本语义由此收紧为"只记录真实发生的收缩"。

回归：`internal/agent`（20s）+ `internal/api/runtimeapi`（46.8s）+ `chat` +
`chatcore` 全绿；vet 干净。

### 13.4 至此的观测闭环

| 问题 | 现在的回答方式 |
| --- | --- |
| 这次请求带了哪些工具？ | `/runtime/tools`（live 口径，§11） |
| 每个工具从哪来？ | `metadata.surface_source`（§12） |
| 为什么这次只有一两个工具？ | 日志 `tool_surface_projected=simple_goal`（本节） |
| 两入口面为什么不同？ | workspace 级 MCP 命中差异（§11.2），同一清单来源 |

阶段 2 剩余：Builder 收敛（把 §11/§12 的散装实现迁入 `internal/toolsurface`）。
阶段 3（MCP 清单配置化 + profile 裁剪双落点）与后置交互通道不变。

---

## 14. 阶段 2 主体交付：internal/toolsurface 包落位（2026-10-14 已实施）

### 14.1 新包

`internal/toolsurface`（依赖最小：types + goal 常量 + brokerClassifier 接口注入，
不依赖 internal/agent——调用方传目录与 broker，TUI 侧可零成本接入）：

- `Classify(def, broker)`：来源分类单一函数（`core` / `mcp:<server>` /
  `orchestration` / `capability`；优先级 mcp_name > capability 名单 > broker > core，
  包装层归属优先于能力层语义——观测面回答"定义从哪个暴露层来"）；
- `Build(catalog, broker)`：候选目录 → 幂等去重 + `surface_source` 账本标注
  （不覆盖既有值，冻结面已带账本）+ 稳定排序；空目录返回 ok=false 让调用方
  回退旧口径，不虚构空面。

测试 5 组：分类优先级（含 goal 经 aicli_goal 包装的包装层优先）、无 broker 时
core 兜底、去重/排序/账本、不覆盖冻结账本、空目录语义。

### 14.2 runtimeapi 切换（行为零变化）

`sessionLiveRequestSurface` 从 60 行散装实现（内联分类 + 手工去重排序）收敛为
`toolsurface.Build(apiAgent.CollectToolCatalogDefinitions(ctx), apiAgent.GetToolBroker())`
一行装配；`toolSurfaceSourceLabel` 从 runtimeapi 删除（单一实现点达成）。

### 14.3 行为复验（迁移前后逐字节一致）

- `/runtime/tools`（lb0uSShK）：count=53，账本分布 `orchestration=31 / core=20 /
  mcp:aicli_goal=2` —— 与迁移前完全相同；
- pwd turn 投影日志：`tools=1 projected=simple_goal` —— 不受迁移影响；
- `go test ./internal/api/runtimeapi/ ./internal/toolsurface/` + vet 全绿。

### 14.4 收敛状态与剩余

| 宿主 | 状态 |
| --- | --- |
| runtime-server | ✅ 观测端点已走 Builder（本节） |
| aicli TUI | 待切换：`chat_actor_host.go` 的 `buildLocalChatToolPolicy` 输入面与
  本地目录观测接 `toolsurface.Build`（同名 broker 分类器已具备） |
| gateway connector | 待切换（同模式） |

剩余主线：aicli/gateway 两宿主装配点切换 → Builder 成为三宿主唯一装配点（T1 达成）；
阶段 3（MCP 清单配置化 + profile 裁剪双落点）；后置交互通道。

---

## 15. 阶段 2 收敛：请求路径账本 + 包落位定稿（2026-10-14 已实施）

### 15.1 包形态定稿（依赖方向修正）

初版 `toolsurface` 直接 import `internal/goal` 做 capability 名单——这会形成
`toolsurface_test → goal → chat → agent → toolsurface` 测试环（goal 包依赖 chat，
chat 依赖 agent）。定稿改为**依赖注入**：

```go
type Ledger struct {
    Broker          brokerClassifier   // 接口（*toolbroker.Broker 满足）
    CapabilityTools map[string]bool    // 非 MCP 包装的能力面名单（宿主传入）
}
func (l Ledger) Classify(def) SourceLabel
func Build(catalog, ledger) ([]ToolDefinition, bool)
```

`toolsurface` 现在只依赖 `types`——`agent` 可安全 import 它（T1 达成的关键）；
capability 名单从包级常量改为宿主输入（goal 常量的同值性由 goal 包自己的测试保障）。

### 15.2 请求路径账本（T1 真正达成）

`computeAvailableTools` 末尾统一过 `toolsurface.Build`：

```go
if stamped, ok := toolsurface.Build(tools, loop.toolSurfaceLedger()); ok {
    tools = stamped
}
```

效果：**aicli TUI 与 runtime-server 的 LLM 请求体工具定义、actor 冻结面
（SaveTurnToolSurface 存的就是标注后的副本）、`/runtime/tools` 观测——三者
surface_source 同源同值**。单一装配实现点（T1）与可解释性（T3）同时闭环；
标注失败不阻塞请求（观测能力，非执行依赖）。

### 15.3 验证

- 单测：`TestClassifyLabelsByPriority`（Ledger 注入形态）、
  `TestComputeAvailableToolsRecordsSimpleGoalProjectionLedger` 增补
  `surface_source == "core"` 断言（请求路径账本）；
- 回归：`toolsurface` / `agent`（19.6s）/ `runtimeapi`（44.8s）全绿，vet 干净
  （含一次测试环的发现与修复：toolsurface_test 弃用 goal 常量改字面量）；
- 真实端点：`/runtime/tools` 53 工具账本不变（orchestration=31 / core=20 /
  mcp:aicli_goal=2）；非投影 turn（inspect 长句）`tools=51`，请求面账本随
  `tools_sha256` 稳定（aba5d752…，与装配内容一致）。

### 15.5 剩余主线（最终清单）

1. aicli TUI 观测面接 `toolsurface.Build`（其装配消费的是 agent 侧共享目录，
   请求体已自动带账本——剩余只是 TUI 展示层如需账本可直读 metadata）；
2. 阶段 3：MCP 清单配置化共享 + profile 裁剪双落点（装配期裁请求面 +
   `ToolExecutionPolicy` 投影）；
3. 后置：`plan_*` / `ask_user_question` 交互通道对接（pending_approval/question
   唤醒链 e2e）。

---

## 16. 阶段 3 实施结论（2026-10-14）

### 16.1 MCP 清单共享：**已存在，无需新建**（裁决）

`aiclipaths.ResolveMCPConfigPathDetailedFrom`（注释明确 "runtime-server sessions,
aicli chat" 共用）已实现项目级 `.aicli/mcp.yaml` → 用户级 → 向上搜索的分层解析；
runtime-server 的 `WorkspaceMCPSupport` 按会话 workspace 重锚并异步建连。

**决定性实测**：probe 会话（workspace=`E:\projects\ai\ai-agent-runtime`）→
`count=83`（53 + chrome-devtools 30，`mcp:chrome-devtools` 账本出现）；
workspace=`E:\temp` → 53。此前 sezoj(83)/lb(53) 的差异**就是该机制的正确输出**
——两入口的 MCP 面差异由 workspace 归属决定，同一清单来源，无配置分裂。
probe 会话已清理。

### 16.2 Profile 裁剪双落点：一致性护栏落地

两宿主消费链确认同源（`profile.Resolve` → `MergeToolPolicies` →
`profileinput.BuildToolExecutionPolicy`）：

- runtime-server：`profileStateToolPolicy` → `applyAgentExecutionPolicy`（装配期 allowlist）；
- aicli TUI：`applyProfileStateToChatSession` → `session.ToolPolicy` →
  `buildLocalChatToolPolicy`（actor 构建期固化）。

新增 `profile_tool_policy_guard_test.go`（T4 护栏）：
`TestProfileToolPolicyDualSinkEquivalence`——同一 ResolvedToolPolicy 在两落点
产出的 ReadOnly / AllowlistEnabled / allowlist 名单 / deny 名单逐字段相等 +
deny 恒优先抽检；`TestProfileToolPolicyReadOnlyPropagation`——宿主基线只读 +
profile 未声明 = 保持只读（只收不放宽）。

### 16.3 请求级账本上线（§15.4 修订）

**关键教训**：每定义 metadata 账本（初版 §15.2）会把 json.Marshal 体积推高，
被 `estimateToolDefinitionTokens` 计入，把贴边预算的压缩/preflight 时序推偏
——`TestReActLoop_RunWithSession_AutoCompaction…` 实测 flaky（stash 二分确认
与账本改动直接相关）。修订：

- 定义保持原样（token 估算零增量）；
- 账本改为**请求级汇总**：`toolsurface.SummarizeSources`（与 Build 共用
  Classify；冻结面已带账本优先）→ `req.Metadata["tool_surface_sources"]`（map）
  + `tool_surface_sources_flat`（k=v 空格分隔串——与已验证透出的
  `tool_surface_projected` 同型，规避嵌套 map 在日志链路的类型保真问题）；
- 日志白名单登记 `tool_surface_sources`（flat 串）。

**端到端实测**（inspect 长句，非投影）：`tools=51` + 51 项逐工具
`surface_source` 完整输出（core=20 / orchestration=31 分布正确，
`spawn_subagents=core` 亦正确——它来自 scheduler 注入而非 broker）。

### 16.4 测试修正记录

- `TestComputeAvailableToolsRecordsSimpleGoalProjectionLedger` 的账本断言从
  "定义级 metadata" 改为 `SummarizeSources`（与 think() 装配同一函数）；
- 回归：agent 19.1s / runtimeapi 46.8s / toolsurface 1.6s 全绿；vet 干净；
  新二进制已部署。

### 16.5 阶段 3 收口状态

| 项 | 结论 |
| --- | --- |
| MCP 清单共享 | ✅ 已存在（aiclipaths 分层解析 + workspace 重锚），实测验证 |
| profile 裁剪双落点 | ✅ 双宿主同源 + T4 护栏测试落地 |
| 请求级账本 | ✅ 上线（flat 串 + map 双形态），逐工具可审计 |

**主线全部收口**。剩余均为可选增强：TUI 展示层读账本 metadata、
`plan_*`/`ask_user_question` 交互通道对接、future capability 直配面。

---

## 17. 后置项第一步：plan_review 的 web 适配（2026-10-14 已实施）

### 17.1 交互通道三工具的适配矩阵（定稿）

| 工具 | 跨请求挂起？ | 适配 |
| --- | --- | --- |
| `plan_review`（只读） | 否 | ✅ 本节：web inline broker 接入 `planReviewController` |
| `plan_enter` / `plan_exit`（写状态 + 权限切换） | 是（turn 边界生效） | 归 actor 路径（web 会话经 durable actor 使用） |
| `ask_user_question` | 是（waiter + `answer_question` 唤醒） | 归 actor 路径；`SessionActor.AskUserQuestion`（actor.go:5210）已完整实现挂起/事件/waiter/resume |

### 17.2 实现（`plan_review_controller.go`）

- `PlanReviewController` 接口实现：PlanID → planstore 归档读取（version=0 最新，
  CompareVersion 走 `planmode.DiffArchivedVersions` 同引擎）；session plan →
  durable 会话 planmode 状态 + workspace 锚定文件读取；
- 相对路径锚定 workspace，越出 workspace 拒绝（只读工具不放大读取面）；
- 64KB 截断、VerdictOptions / Hint 与 `SessionActor.ReviewPlan` 同串同形——
  两个表面的输出契约逐字节同源；
- 接线点：`applyAgentRuntimeServices` 的 hub 分支补
  `broker.PlanReview = h.planReviewControllerForSession()`（nil 保持即插即用）。

### 17.3 验证

新增 4 用例（`plan_review_controller_test.go`）：session plan 读工作区文件、
相对路径越界拒绝、归档两轮快照 + `DiffArchivedVersions` 跨轮 diff
（FromVersion/ToVersion/Text 断言）、托管会话 planmode 默认非活跃。
`runtimeapi` 全包 48.3s + `toolsurface` + `agent` 回归全绿；vet 干净；
新二进制已部署。

### 17.4 主线终态

工具面统一（T1）+ 可解释（T3）+ profile 双落点护栏（T4）+ 交互通道第一步
（plan_review web 可用）全部落地。剩余：`plan_enter/exit` 与
`ask_user_question` 的 web 适配需先落跨请求挂起协议（建议经 durable actor
承接，作为独立设计项），不在本方案范围。

---

## 18. TUI 观测面收官：`/tools` 会话内命令（2026-10-14 已实施）

### 18.1 实现

`/tools`（categoryDiagnostics 屏命令）——TUI 侧观测面与 web
`/api/runtime/tools` 消费**同一 `toolsurface` 实现点**，T1 观测闭环收官：

- 数据源优先级与 web 端点同口径：
  1. 会话 actor 的 `StableToolSurface`（冻结面 = 真实请求前缀，含 §15 账本）；
  2. 兜底：本地 `ToolSurface.ListTools()` 经 `toolsurface.Build`（无 actor /
     未冻结时；Ledger 空 = 全部按无账本 core/包装层归类）；
- 渲染：按 `surface_source` 分组 + 组内字母序 + 首行描述截断（96 rune + …）；
- 注册：`chat_runtime_command_registry.go` categoryDiagnostics 屏命令
  （备用屏不可用降级内联文档，与 /status、/usage 同样板）。

### 18.2 验证

3 用例（账本分组计数 / 无账本 core 兜底 / 长描述截断）+ `cmd/aicli/commands`
全包回归（284s，含他人 connectors WIP 一并跑过）+ vet 干净。

### 18.3 全案收官清单

| 交付 | 章节 |
| --- | --- |
| 架构方案文档 | §1–§7 |
| 阶段 0 裁决 / MCP 共享裁决 | §9–§10 / §16.1 |
| 观测口径统一 + toolsurface 包 | §11 / §14 |
| 来源账本（请求级 + 观测端点） | §12 / §15.4 |
| 投影账本 | §13 |
| profile 双落点护栏（T4） | §16.2 |
| plan_review web 适配 | §17 |
| `/tools` TUI 观测面 | 本节 |

不变量 T1（单一装配实现点，双宿主观测+请求同源）、T2（同 workspace+profile
跨宿主名单一致）、T3（工具可解释）、T4（profile 先于宿主硬边界）全部有代码
与测试锚定。T5（turn 边界生效）沿用既有冻结语义未改动。

---

## 19. ConPTY 实测验证（2026-10-14，真实环境）

### 19.1 实测环境

- `aicli-e2e-test.exe`（HEAD `3a1b12ae`+修复，chat 命令）经 ConPTY 作业运行于 `E:\temp`；
- runtime-server（8101，同 HEAD 重编译部署）；
- 会话：`session_20261014203629_b9GGqTTl` 等（server 持久化，无 actor 冻结面）。

### 19.2 实测发现并修复的两个缺口

| # | 现象 | 根因 | 修复 |
| --- | --- | --- | --- |
| 1 | `--headless --prompt "/tools"` → 「未知命令: /tools」 | legacy/plain 出口（`handleCommand`）没有 /tools 分支——统一 TTY 分支已接，no-interactive/兼容终端路径漏了 | `command.go` handleCommand 增加 `/tools` case（printChatCommandOutput） |
| 2 | 分支接上后 → 「当前会话没有可枚举的工具面」 | headless 首帧路径 `session.ChatExecutor` 由后台装载 goroutine **异步建立**，slash 分支直接读时为 nil → 三分支全 miss | `/tools` 作为只读命令像 turn 一样先 `ensureChatExecutor` await，再断言 runtime-server executor |

（诊断过程：先用一行诊断 return 定位「server 分支根本没进」，再修 await——两次重编译重测收敛。）

### 19.3 实测结果（端到端账本闭环）

`aicli chat --headless --runtime-server http://127.0.0.1:8101 --prompt "/tools"`：

```
当前会话工具面：54 个工具（与 /api/runtime/tools 同源）
[core] ×20               （aicli_exec … write）
[mcp:aicli_goal] ×2      （get_goal / update_goal）
[orchestration] ×32      （job_*×8 + subagent_*×17 + team_*×2 + schedule_*×4
                           + plan_review + ask_user_question）
```

**与 web `/api/runtime/tools` 同一会话同值**（54；plan_review 为本轮新增工具）。
T1 观测闭环在真实 ConPTY 环境下达成：TUI、web 端点、LLM 请求三面同源同值。

### 19.4 提交

- `d39e8d78` fix(aicli)：await ChatExecutor + legacy 出口分支（本节两缺口）；
- `29910263` / `8d77c794` / `2752d273` / `ccdbc2ff`：方案主体（见 §18 清单）。

回归：`cmd/aicli/commands` 全包（284s，含他人 connectors WIP）+ 渲染 3 用例 + vet 全绿。

---

## 20. 验证矩阵终版（2026-10-14，如实声明）

| # | 交付 | 验证方式 | 证据 | 状态 |
| --- | --- | --- | --- | --- |
| 1 | 观测口径统一（live 口径） | 真实端点 curl | `/runtime/tools` count=53/83（§11.2） | ✅ 端到端 |
| 2 | surface_source 观测账本 | 真实端点 curl | sources 分布 core=20/orchestration=31/mcp=2（§12.2） | ✅ 端到端 |
| 3 | 请求级来源账本 | 真实日志 | gateway.log `tool_surface_sources` flat 串，51 工具逐工具标注（§16.3） | ✅ 端到端 |
| 4 | simple-goal 投影账本 | 真实日志 | `tools=1 projected=simple_goal`（§13.2） | ✅ 端到端 |
| 5 | MCP 清单共享（workspace 重锚） | 真实端点 curl | probe 会话 83 含 `mcp:chrome-devtools`（§16.1） | ✅ 端到端 |
| 6 | `/tools` TUI 观测面 | **ConPTY 实测** | 54 工具账本输出（§19.3） | ✅ 端到端 |
| 7 | profile 双落点裁剪 | **真实 turn**（本轮补做） | denylist 端到端生效：apply 成功（switch_report）→ 请求面 51→50，`openai_image_generate`/`web_search` 从 LLM 请求消失（gateway.log sources 实测） | ✅ 端到端（本轮补齐） |
| 8 | plan_review web 适配 | 单测 4 用例 + 真实 turn（工具在请求面） | 控制器逻辑单测；真实 turn 中模型自主选择 ls/shell（plan 文件不存在，措辞未强制），工具面在册已由 #3 证实 | ⚠️ 部分端到端 |
| 9 | TurnID 历史匹配（前端吸收） | 单测 + 前端 vitest 3272/3272 | history-merge 10 用例；新会话前端端到端未另做（吸收逻辑是前端投影纯函数，输入形状由单测锁定） | ⚠️ 单测级 |

### 20.1 本轮补做的端到端验证（#7 细节）

1. 创建临时 profile `toolsurface-e2e`（denylist: openai_image_generate + web_search）；
2. `POST /profiles/{ref}/apply`（session_id 显式）→ switch_report 成功、
   `effective_at=next_turn`、cache_notice 如实；
3. 发真实 turn → gateway.log 实测：`tools=50`（基线 51 − 2 个 denied），
   `tool_surface_sources` 中两个 denied 工具**不再出现**——装配期策略过滤端到端生效；
4. probe 会话与临时 profile 已清理。

### 20.2 诚实边界

- #8 plan_review：真实 turn 中模型未选择调用 plan_review（行为不可强制）；
  控制器实现由 4 个单测锁定（含归档 diff），工具在请求面由 #3 证实。
  要 100% 强制验证需要造一个带 planmode 状态 + plan 文件的会话再驱动模型，
  收益/成本比低，如实标注为部分端到端。
- #9 前端吸收：逻辑为纯函数且输入形状单测锁定，真实浏览器验证在 absorb 修复
  当轮已完成（§11 同轮的浏览器实测），TurnID 对齐的新会话复验未另做。
