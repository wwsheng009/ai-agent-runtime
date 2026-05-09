# Skill Runtime 当前架构与设计对照

> 2026-03-30 更新：本文描述的 runtime 能力已经迁移到 `E:\projects\ai\ai-agent-runtime\backend`。`ai-gateway` 不再挂载 `/api/agent`、`/api/skills`，也不再保留 `internal/api/skills`、`internal/runtime`、`internal/mcp`、`internal/team` 的实现。本文中的核心实现路径已尽量改为当前 `backend/` 真实位置；仍保留的旧 `internal/runtime/<subpkg>` 文案，请按 `backend/internal/<subpkg>` 理解。涉及 `internal/gateway/router/gin.go`、`pkg/monitoring/runtime_observability_collector.go`、`/monitor/*` 的描述视为历史状态。

## 目标重述

`skill_runtime` 当前的正确目标是：

- 构建一个由 `skill / agent / tool / workflow / mcp / llm / session` 驱动的通用 AI 平台
- 将 coding agent 视为平台上的垂直能力包，而不是平台本体

这与 `docs/skill_runtime/design/agent_skill_runtime-1.md` 到 `docs/skill_runtime/design/agent_skill_runtime-16.md` 的底层抽象是一致的，只是需要把 coding-specific 能力从“平台主线”降级为“场景扩展”。

## 设计主线归纳

16 份设计文档大致可以归成四层：

### 第一层：Skill Runtime 内核

对应文档：`agent_skill_runtime-1.md`、`agent_skill_runtime-2.md`、`agent_skill_runtime-7.md`、`agent_skill_runtime-8.md`、`agent_skill_runtime-16.md`

核心意图：

- `skill.yaml` / `.skill` 描述能力
- 可选 `prompt.md` 作为 companion prompt 文件
- `loader + registry + router + executor` 形成最小闭环
- skill 可以注入 prompt、tools、workflow、context
- skill 应被 AI 动态选择和调用

### 第二层：通用 Agent / Tool / Workflow Runtime

对应文档：`agent_skill_runtime-3.md`、`agent_skill_runtime-9.md`、`agent_skill_runtime-10.md`、`agent_skill_runtime-11.md`

核心意图：

- 统一 `LLM Runtime`
- 统一 tool call loop
- workflow / DAG / parallel execution
- observation memory
- MCP runtime，把 skills 外化为 remote tool servers

### 第三层：Context / Retrieval Runtime

对应文档：`agent_skill_runtime-4.md`、`agent_skill_runtime-12.md`、`agent_skill_runtime-13.md`、`agent_skill_runtime-15.md`

核心意图：

- session / memory
- workspace context
- symbol / reference / repo tree
- embedding / semantic search
- context packing / token budget

### 第四层：垂直场景增强

对应文档：`agent_skill_runtime-5.md`、`agent_skill_runtime-6.md`、`agent_skill_runtime-14.md`

核心意图：

- planner-first coding loop
- multi-agent
- patch engine
- git integration
- code graph / repo intelligence
- coding-specific sandbox

这层更接近 Claude Code / Cursor 的垂直能力，不应再被视为平台主线阻塞项。

## 当前实现分层

### 1. 启动与组装层

核心文件：

- `E:\projects\ai\ai-agent-runtime\backend\cmd\runtime-server\main.go`
- `E:\projects\ai\ai-agent-runtime\backend\internal\bootstrap\manager.go`
- `E:\projects\ai\ai-agent-runtime\backend\internal\agentconfig\config.go`
- `E:\projects\ai\ai-agent-runtime\backend\internal\config\manager.go`

职责：

- 从独立 runtime 配置读取 `skills_runtime`
- 读取 runtime 配置
- 组装 `Registry / Loader / LLMRuntime / SessionManager / HotReload / EmbeddingRouter`
- 把 `skills.Handler` 挂到独立 `runtime-server` 路由

### 2. Skill / Tool / MCP Runtime

核心文件：

- `backend/internal/skill/loader.go`
- `backend/internal/skill/registry.go`
- `backend/internal/skill/router.go`
- `backend/internal/skill/embedding_router.go`
- `backend/internal/skill/executor.go`
- `backend/internal/skill/mcp_adapter.go`
- `backend/internal/mcp/manager/manager.go`

职责：

- 加载和注册 skills
- 支持从 skill 目录自动装载 companion `prompt.md`
- 支持 lexical + embedding 路由
- 执行默认 skill、workflow skill、MCP-backed tool skill
- 在执行期校验 `skill.permissions`
- 将 skill 执行桥接到 MCP manager

### 3. Model / Agent Runtime

核心文件：

- `backend/internal/llm/runtime.go`
- `backend/internal/llm/provider.go`
- `backend/internal/agent/agent.go`
- `backend/internal/agent/loop.go`
- `backend/internal/agent/planner.go`
- `backend/internal/llm/retry_policy.go`

职责：

- 统一 provider 调用与流式接口
- 支持 provider alias、gateway provider、default model
- 从 provider config 接入 runtime retry tuning / retry rules；`Call()` 与 `Stream()` 共用 retry policy
- 流式路径只在 provider 直接打开失败或首个有效输出前收到 error chunk 时重试；一旦已经发出 text/image/tool 内容，就把后续错误原样传给调用方，避免重复输出
- `bootstrap.Manager.ReloadProviderConfigs()` 会同步更新现有 `LLMRuntime` 的 retry 配置，不需要重建整个 runtime
- 提供 route-first agent chat、LLM fallback、基础 ReAct / planner 能力

### 4. Session / API / Delivery Surface

核心文件：

- `backend/internal/chat/manager.go`
- `backend/internal/api/skills/handler.go`
- `backend/internal/api/skills/session_runtime_handlers.go`
- `docs/skill_runtime/skills_api_result_contract.md`
- `docs/skill_runtime/skills_api_stream_contract.md`
- `docs/skill_runtime/session_agent_api.md`

职责：

- 管理 session / history / state
- 提供 `ExecuteSkill`、`AgentChat`、`SearchSkills`、session 管理、search admin API
- 提供 session-scoped child-agent 控制面：`spawn/input/wait/events/close/resume`
- 固定非流式与流式协议

### 5. Retrieval / Search / Observability

核心文件：

- `backend/internal/embedding/index.go`
- `backend/internal/embedding/search.go`
- `backend/internal/observability/metrics.go`
- `backend/cmd/runtime-server/main.go`
- `backend/internal/api/skills/handler.go`

职责：

- 默认本地 embedding 生成与语义搜索
- search telemetry / audit / reindex ops
- 由 runtime 自身的 `/api/runtime/*`、trace / governance / usage 视图对外暴露

## 组件关系总览

从组件关系上看，`skill_runtime` 更像一个“内核 + 组装层 + 宿主适配层 + 观测层”的系统，而不是单一模块。

其中最核心的状态中心是 `Registry`：

- `Loader / HotReload` 负责向 `Registry` 写入或同步 skill
- `Router / Executor / Agent / Capability projection` 负责从 `Registry` 读取
- `EmbeddingRouter` 是 `Registry` 的语义检索投影，而不是独立真相源

可以简化为下面这张关系图：

```text
runtime config / aicli flags / profile inputs
        |
        v
SkillsRuntimeConfig + RuntimeConfig/RuntimeManager
        |
        v
bootstrap.Manager
  |       |        |         |          |
  v       v        v         v          v
Loader -> Registry -> Router -> Executor -> LLMRuntime
   |         |         |         |
   |         |         |         -> MCPAdapter / MCPManager
   |         |         |
   |         |         -> SemanticEmbeddingRouter
   |         |
   |         -> Capability projection
   |
   -> HotReload
        |
        -> sync embedding index

Registry + LLM/MCP + Session + Policies
        |
        +--> skills API handler
        |
        +--> aicli skill functions
        |
        +--> runtime status / health / traces
```

## 配置层与运行层的边界

当前有两层配置，不应混为一谈。

### 1. 宿主接线配置：`SkillsRuntimeConfig`

这层配置位于网关主配置中，决定：

- 是否启用 `skills runtime`
- 系统级和外部 skills 目录
- `aicli chat` 的 skills 暴露策略
- admin token
- usage / quota / auth / mutation policy

它回答的是：

- runtime 是否挂到网关
- 暴露哪些管理面
- 使用哪些目录和治理策略

### 2. 运行行为配置：`RuntimeConfig`

这层配置位于 runtime config 文件中，决定：

- `agent`
- `router`
- `embedding`
- `workspace`
- `hotReload`
- `rollout`

它回答的是：

- skill/agent 在运行时如何路由、执行、检索、热更新
- 针对不同 `scope` 是否命中 rollout candidate config

因此，前者更像“平台装配与治理配置”，后者更像“runtime 内核行为配置”。

## 组件依赖与状态归属

| 组件 | 创建者/宿主 | 主要依赖 | 持有的关键状态 | 主要被谁使用 |
| --- | --- | --- | --- | --- |
| `RuntimeManager` | `runtime-server` / `aicli` | runtime config file | active config、candidate config、history、rollout 选择 | bootstrap、handler |
| `bootstrap.Manager` | `runtime-server` / `aicli` | `RuntimeConfig`、MCP、provider config | 运行时组件引用 | HTTP 服务、CLI |
| `Loader` | bootstrap | manifest parser、MCP tool 校验 | skills 目录、文件模式 | bootstrap、hot reload |
| `Registry` | bootstrap | MCP manager | skill map、keyword index、pattern index | router、executor、agent、API |
| `Router` | handler / agent / `aicli` | `Registry`、可选 `EmbeddingRouter` | route 参数、候选结果 | agent chat、search、CLI skills exposure |
| `SemanticEmbeddingRouter` | bootstrap | vector index、`Registry` | embedding index | router、search、hot reload sync |
| `Executor` | handler / agent / `aicli` | `Registry`、`LLMRuntime`、MCP | 无长期状态，按请求执行 | skill execute、agent、CLI function |
| `SessionManager` | bootstrap | session storage | session / history / lifecycle | handler、agent chat |
| `skills.Handler` | `runtime-server` | runtime 全组件 + policies | API 级状态、usage tracker、search telemetry、policy snapshot | HTTP `/api/runtime/*` + `/api/agent/chat`（主入口：`/api/agent/chat`） |
| `SkillFunction` | `aicli` | `Executor`、session history、metadata | 无长期状态，按轮暴露 skill function | `aicli chat` |
| runtime observability API | `skills.Handler` + runtime event bus | runtime metrics、trace/event store、governance snapshot | status/health/traces/governance 视图 | `/api/runtime/*` |

从这张表可以看到：

- `Registry` 是 skill 事实来源
- `Handler` 是最厚的集成面
- `bootstrap.Manager` 是唯一合理的 runtime 组装根
- `aicli` 与 `runtime-server` 并不是两套 runtime，只是两种宿主适配方式

## 宿主适配层

### HTTP 宿主：`runtime-server`

当前独立 HTTP 宿主是 `backend/cmd/runtime-server/main.go`。它负责：

- 解析 `serve/start/stop/status` 子命令、配置路径、监听地址与 PID 文件
- 按 `$HOME/.aicli/config.yaml -> ./.aicli/config.yaml -> ./config.yaml -> ./configs/config.yaml` 搜索配置
- 创建 `RuntimeManager`
- 创建 `bootstrap.Manager`
- 建立 MCP runtime、session hub、background manager、service control、config document service、file transfer service
- 创建并接线 `skills.Handler`
- 将 handler 注册到 mux route table

当前启动链路可以概括为：

`cmd/runtime-server/main.go -> RuntimeManager -> bootstrap.Manager -> skills.Handler -> mux routes`

当前 route source of truth 是 `backend/internal/api/skills/handler.go`，主 route group 包括：

- `POST /api/agent/chat`
- `/api/runtime/skills/*`
- `/api/runtime/sessions/*`
- `/api/runtime/teams/*`
- `/api/runtime/agent-control/*`
- `/api/runtime/status`、`/api/runtime/health`、`/api/runtime/traces*`、`/api/runtime/usage/*`
- `/api/runtime/service`、`/api/runtime/service/restart`
- `/api/runtime/logs`、`/api/runtime/logs/stream`
- `/api/runtime/config/document*`、`/api/runtime/skills/config/write`
- `/api/runtime/models`
- `/api/runtime/fs/*`
- `/api/runtime/background/jobs*`

历史上的 `internal/gateway/router/GinEngine` 属于 gateway-hosted runtime 阶段的宿主适配器说明；当前 standalone runtime 不再以它作为 live HTTP 入口。

### CLI 宿主：`aicli chat`

`aicli` 走的是另一条宿主路径：

- 同样创建 bootstrap
- 建立统一 function catalog
- 读取系统级和外部 skill 目录
- 把每个 skill 映射成 `skill__*` function
- 先 route 出候选 skill，再由 catalog 统一选择 builtin tools + `top-k` skills 暴露给模型
- 真正执行时仍然回到 `skill.Executor`

因此 `aicli` 本质上是“functions delivery surface”，不是并行的第二套技能系统。

更细的“AI 如何感知 skill、如何发起 skill 调用、`prompt` 在哪一层被消费”的说明，见：

- `docs/skill_runtime/skill_invocation_mechanism.md`

## 关键耦合点

### 1. `Registry` 是运行时中心

这是当前最重要的架构事实。

- `Loader` 负责把文件 skill 放进来
- `HotReload` 负责让文件变化同步到这里
- `Router` 在这里做 lexical route
- `Executor` 在这里拿到 skill 定义
- `CapabilityDescriptor` 也从这里投影

如果没有 `Registry`，其余组件只能各自维护副本，系统会迅速失去一致性。

### 2. `EmbeddingRouter` 不是第二套注册表

它只是 `Registry` 的语义检索索引：

- 从 `Registry` 建索引
- 给 `Router` 提供 embedding route 结果
- 由 `HotReload` 触发增量更新或重建

这一点很重要，因为它保证了 lexical route 和 semantic route 不会各自漂移。

### 3. `Handler` 是当前最厚的 façade

`skills.Handler` 目前同时承接：

- execution API
- agent chat
- sessions
- search
- runtime status/health
- usage/quota
- auth/mutation governance
- context pack 拼装

这使它成为当前最重的集成点。它的价值是把交付面统一了，但代价是应用层责任偏多。

### 4. 观测层是单向桥接

runtime 本身直接产出 metrics、telemetry、trace 和治理统计；
`skills.Handler` 会把这些状态整理成 self-hosted API：

- `/api/runtime/status`
- `/api/runtime/health`
- `/api/runtime/traces*`
- `/api/runtime/skills/search/stats`
- `/api/runtime/usage/*`

如果需要 Prometheus 或外部指标抓取，再由独立 collector/exporter 读取 runtime 暴露的数据，而不是反向侵入执行主路径。

## 当前数据流

### 启动流

`backend/configs/config.yaml -> runtime-server -> RuntimeManager -> bootstrap.Manager -> skills.Handler -> mux routes`

关键落点：

- `backend/cmd/runtime-server/main.go`
- `backend/internal/bootstrap/manager.go`
- `backend/internal/api/skills/handler.go`

### 单 Skill 执行流

`POST /api/runtime/skills/{name}/execute (admin/debug) -> skills.Handler -> types.Request -> skill.Executor -> handler/workflow/llm/mcp -> session persistence -> response`

关键落点：

- `backend/internal/api/skills/handler.go`
- `backend/internal/skill/executor.go`

### Agent Chat 流

`POST /api/agent/chat -> session/history -> route candidates -> matched skill or llm fallback -> orchestration summary -> optional SSE -> session persistence`

关键落点：

- `backend/internal/api/skills/handler.go`
- `backend/internal/agent/agent.go`
- `backend/internal/llm/runtime.go`

### 搜索流

`GET /api/runtime/skills/search -> lexical / semantic / hybrid selection -> local embedding index -> search telemetry -> response`

关键落点：

- `backend/internal/api/skills/handler.go`
- `backend/internal/skill/embedding_router.go`
- `backend/internal/embedding/index.go`

### 运维与监控流

`runtime/search/usage/governance api -> audit log + runtime metrics + trace stats -> self-hosted runtime APIs`

关键落点：

- `backend/internal/api/skills/handler.go`
- `backend/internal/observability/metrics.go`
- `backend/cmd/runtime-server/main.go`

## 设计对照与偏离度

| 设计层 | 原始意图 | 当前实现 | 偏离判断 |
| --- | --- | --- | --- |
| Skill Runtime 内核 | 动态加载、路由、执行、热更新 | 已基本落地，且已接入网关与 API | 低 |
| Tool / MCP Runtime | tool call loop、MCP skills、workflow | 已具备统一 runtime，local/remote/ABAP MCP 已验证 | 低到中 |
| Context / Retrieval | session、embedding、semantic routing、search | session 与 local embedding 已到位，context pack 已形成统一入口 | 中到低 |
| Agent 编排 | planner、ReAct、observation、fallback | route-first 为主，planner-first 已可配置为默认模式 | 中到低 |
| 平台治理 | auth、tenant、policy、usage、versioning | governance API + usage/quota/auth 已落地，versioning 已接入 | 低到中 |
| Coding 垂直能力 | repo map、AST、patch、git、multi-agent | 作为垂直能力包单独推进 | 不纳入平台主线评估 |

## 当前实施状态

### 绿灯

- skill runtime 主链路可用
- skills API 可执行、可流式、可管理 session
- 本地 embedding 已替代默认外部 embedding API 依赖
- search admin / runtime observability 已打通
- provider / local MCP / remote MCP / ABAP MCP 已完成 live 验证
- capability abstraction 已统一并暴露到 skills API / orchestration
- context pack 已统一 workspace + session 入口
- planner-first 已支持默认模式配置
- governance（auth/usage/quota/policy）已形成稳定运行面
- SDK / `aicli` 已完成落地与 live 验证

### 黄灯

- 当前无显著黄灯阻塞项

### 红灯

- 当前没有“平台无法运行”的核心阻塞
- 主要缺口是治理与统一抽象，不是骨架缺失

## 下一阶段建议

### P1 平台主线

1. 统一 capability 抽象  
   把 `skill / tool / workflow / agent` 统一到一套平台可注册、可路由、可编排的能力模型上。

2. 让 skill 真正被 AI 直接调用  
   优先把 skills 以 AI-callable functions 的方式接入 `aicli chat`，而不是单独再造一套聊天链路。

3. 平台治理  
   增加 `auth / tenant / project / policy / usage / quota / audit` 的统一框架。

4. 稳定交付面  
   固化 REST / SSE contract，并补上 SDK / CLI 绑定。

### P2 平台增强

1. retrieval / memory abstraction
2. planner-first optional orchestration
3. provider / MCP 生命周期治理
4. 配置验证、版本化、灰度发布

### P3 垂直能力包

- coding pack
- ABAP / ERP pack
- enterprise workflow pack
- support / knowledge pack

这些能力可以继续建设，但不应再反向定义平台边界。
