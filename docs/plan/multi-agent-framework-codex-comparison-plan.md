# Multi-Agent 运行框架对比分析与改进方案

生成日期: 2026-05-06
最新更新: 2026-05-09

范围:
- 当前项目: `E:\projects\ai\ai-agent-runtime`
- 对比项目: `E:\projects\ai\codex`
- 重点问题: 多 agent 并行执行、子 agent 输出隔离、等待/中断语义、控制台噪声、reasoning 输出、team 与 child session 的控制面边界。

当前源码同步（2026-05-09）：

- 本文前半部分保留了 2026-05-06 的差距分析；其中“缺 `fork_turns` / AgentRegistry / `list_agents` / mailbox wait / subtree close / spawn limits”等描述已经被后续实现追上。
- 当前 toolbroker 已具备 `spawn_agent`、`list_agents`、`send_input`、`send_message`、`followup_task`、`wait_agent`、`read_agent_events`、`close_agent`、`resume_agent`、`spawn_team`、`wait_team`。
- 当前 fork 语义支持 `fork_turns=none|all|N`，配置字段为 `agents.maxThreads`、`agents.maxDepth`、`agents.defaultForkTurns`。
- 当前 AgentControl identity graph、path、spawn reservation、subtree close、maxThreads/maxDepth、task registry、mailbox projection 已落地；team task/dependency/outcome 写路径正在收敛到 AgentControl substrate。
- 当前 `wait_agent` / `read_agent_events` 已接入 event watcher / mailbox watcher；无 target 时读取 parent mailbox / collab event。固定轮询描述只适合历史版本。
- 当前仍需明确的边界是：`spawn_team` teammate id 不能直接等同于 `spawn_agent` child id；team 等待应使用 `wait_team`。

## 1. 结论摘要

当前项目已经具备两套多 agent 能力:

1. 轻量子会话控制面: `spawn_agent`、`send_input`、`wait_agent`、`read_agent_events`、`close_agent`、`resume_agent`。
2. 持久化团队编排层: `spawn_team` 加 team/task/teammate/mailbox/path-claim/outcome/status 机制。

这说明当前项目不是“缺少多 agent”，而是多 agent 抽象分裂: 轻量 child session 与持久 team 是两套不同控制面，身份、状态、等待、通信、输出隔离、生命周期关闭没有统一到底层 agent graph。模型在实际运行中很容易把 `spawn_team` 创建的 `member-1` 当作 `spawn_agent` 的 child id 使用，进而触发 `wait_agent/read_agent_events` 误用；此前观察到的控制台输出混杂、Ctrl+C 难以中断子 agent、reasoning 输出干扰，也都与这层控制面边界不够统一有关。

Codex 的多 agent 机制更接近一个统一的 agent/thread tree:

- `AgentControl` 是根会话和所有子 agent 共用的控制面。
- `AgentRegistry` 持有 agent path、nickname、role、thread id、spawn slot 和总量限制。
- 每个 session 有 mailbox 与 watch 序列，`wait_agent` 等待 mailbox 变化，而不是短周期轮询所有子 agent 状态。
- V2 工具把 `send_message` 与 `followup_task` 分离: 前者只入队，后者唤醒目标执行。
- 子 agent 完成、消息、等待、TUI 展示都通过结构化 collab event 汇总到父 agent，而不是让子 agent 原始流直接进入主控制台。

建议的目标架构是: 保留本项目 `spawn_team` 的持久任务图优势，但把轻量 child agent 与 team teammate 都接到底层统一的 `AgentControl`/`AgentRegistry`/`Mailbox` 基座上。`spawn_team` 应成为“工作流/任务图层”，不是另一套并行 agent 身份体系。

## 2. 当前项目机制梳理

### 2.1 轻量 child session 控制面

> 历史基线说明：本节先描述 2026-05-06 观察到的机制。到 2026-05-09，`fork_turns`、`list_agents`、event/mailbox watcher、parent mailbox wait/read、AgentControl registry/path/subtree close 等能力已经落地；当前事实以本文开头“当前源码同步”小节为准。

主要代码:

- `backend/internal/toolbroker/broker.go`
- `backend/internal/toolbroker/types.go`
- `backend/cmd/aicli/commands/chat_actor_registry.go`
- `backend/cmd/aicli/commands/chat_actor_host.go`
- `docs/working/light-agent-control-plane-2026-03-18.md`
- `docs/skill_runtime/session_agent_api.md`

工具面:

- `spawn_agent`: 创建轻量 child session，可选发送首条 prompt。
- `send_input`: 给已有 child session 发送后续 prompt。
- `wait_agent`: 等待 child session 进入 idle/blocked/stopped/missing 等 ready 状态。
- `read_agent_events`: 从 event store 读取 child session runtime events。
- `close_agent`: 停止并关闭 child session。
- `resume_agent`: 恢复已关闭 child session。

实现特征:

- `localActorRegistry.Spawn` 基于 `SessionStore` 创建或 fork 一个 `runtimechat.Session`，再通过 `SessionHub.GetOrCreate` 获取 actor。
- `fork_context=true` 时直接 clone 父 session，粒度偏粗；当前没有 Codex V2 中 `fork_turns=none/all/N` 的精细控制。
- `send_input` 默认不排队。如果目标 session 正在 running/rewinding/waiting approval/waiting input，且没有 `interrupt=true`，会直接返回 busy。
- `wait_agent` 每 50ms 轮询 `agentSnapshot`，直到目标进入 ready 状态或超时。
- `read_agent_events` 每 50ms 轮询 event store，等待新增 event 或超时。
- `broker.go` 已经加入 `spawn_team teammate id` 误用保护，阻止 `wait_agent/read_agent_events` 作用于 team teammate id。

优点:

- 工具面简单，适合一次性派生几个轻量子任务。
- 与普通 session 复用同一个 runtime hub，落地成本低。
- 已具备 close/resume 和基础事件读取能力。

主要问题:

- child session 没有统一的 tree path，例如 `/root/worker-1/reviewer`。
- parent-child 关系主要靠 session context 和 alias，缺少一份全局 agent registry。
- `wait_agent/read_agent_events` 是轮询式，不是事件驱动。
- `send_input` 的 busy/interrupt 行为对模型不够友好，缺少“只留言”和“留言并唤醒执行”的分层。
- 没有 `list_agents` 能力，模型需要自行记忆 id。
- close 主要关闭指定 session，没有持久化 descendant graph 语义。

### 2.2 持久化 team orchestration

主要代码:

- `backend/internal/team/types.go`
- `backend/internal/team/orchestrator.go`
- `backend/internal/team/scheduler.go`
- `backend/internal/team/teammate_runner.go`
- `backend/internal/team/mailbox.go`
- `backend/internal/team/terminal_state.go`
- `backend/internal/team/events.go`
- `backend/internal/team/lead_planner.go`
- `backend/internal/team/sqlite_store.go`
- `backend/internal/toolbroker/team_tools.go`
- `backend/cmd/aicli/commands/chat_team_lifecycle.go`

工具面:

- `spawn_team`: 创建 team、teammate、task，并可 `auto_start` 后台编排。
- `send_team_message`: team 范围消息。
- `read_mailbox_digest`: 读取当前任务/team mailbox 摘要。
- `read_task_spec`: 读取当前任务规格。
- `read_task_context`: 读取任务上下文。
- `report_task_outcome`: 成员上报 done/failed/blocked 等结果。
- `block_current_task`: 阻塞当前任务并记录原因。

运行链路:

1. CLI chat 初始化 runtime host，注入 `SessionHub`、runtime event store、team SQLite store、`TeamClaims`、`team.Orchestrator` 与 `localActorRegistry`。
2. `spawn_team` 将 team/task/teammate 写入 team store，并在 chat 侧绑定 active team context。
3. `localTeamLifecycleService.SyncLoops` 扫描 active teams，为每个 team 启动一个 `Orchestrator.Run` goroutine。
4. `Orchestrator.Run` 默认每 1 秒 tick 一次。
5. tick 中标记 ready tasks，筛选 idle teammates，考虑 `max_teammates`、`max_writers`、write path claims，再调用 scheduler 生成 assignment。
6. 每个 assignment 启动 goroutine 执行 `executeAssignment`。
7. `TeammateRunner.StartTask` 生成 teammate prompt，并通过 teammate session 的 `SubmitPrompt` 执行。
8. teammate 通过 `report_task_outcome` 上报结果；runner 也有 fallback parsing。
9. `ReconcileTerminalTeamState` 在任务全部完成或失败后发布 `team.completed` 与 `team.summary`。

优点:

- 有持久化 team/task/teammate/mailbox/event/path claim 数据模型。
- 支持 task dependency、writer slots、write path ownership，适合长任务和可恢复 workflow。
- 具备 team terminal reconciliation，能在完成后生成 summary。
- 最近已经补强了 team terminal state 不降级、teammate cleanup 延迟、foreign team event 过滤、team teammate id 误用保护等问题。

主要问题:

- team teammate 与 `spawn_agent` child session 不是同一种 agent 身份。
- `spawn_team` 对模型要求高，需要一次性构造 teammate/task/spec；轻量临时协作会显得重。
- team lifecycle 是轮询 tick，响应延迟和空转开销都比 watch/event-driven 模式差。
- team mailbox 是持久 digest 模式，child agent 没有同等 mailbox；通信模型不统一。
- 成员状态是 team store 状态，child session 状态是 runtime session 状态，二者缺少统一投影。
- 控制台输出隔离靠 runtime event bridge 过滤和渲染策略维护，容易出现边界遗漏。

### 2.3 CLI runtime event bridge 与输出路径

主要代码:

- `backend/cmd/aicli/commands/chat_runtime_events.go`
- `backend/cmd/aicli/commands/chat_team_lifecycle.go`
- `backend/cmd/aicli/commands/chat_interrupt.go`
- `backend/internal/types/reasoning.go`

现状:

- CLI chat 订阅 runtime event bus，将 assistant delta、reasoning、tool timeline、team progress、team summary、approval/input 等事件渲染到终端。
- team lifecycle 会将 `team.completed`、`team.summary` 等事件转成用户可见 timeline。
- 后续实现已经增加 reasoning block 的规范化输出，并通过 primary session 过滤抑制 child/non-primary `assistant_reasoning` 与 legacy `assistant.reasoning` 事件污染主控制台。
- Ctrl+C 中断逻辑已扩展到 chat interrupt 清理路径，但多 agent descendant 级别的关闭仍更多依赖 session hub 和 team lifecycle 的组合，而不是统一 agent tree close。

风险:

- 如果事件过滤只基于 team id/session id 局部判断，后续新事件类型仍容易漏出；因此需要继续用 runtime bridge 回归测试覆盖 legacy 与新事件名。
- reasoning 输出仍由 runtime event bridge 统一处理，但 child/non-primary reasoning 已按 primary session scope 抑制；剩余风险是新增事件类型必须继续接入同一归属规则。
- team summary/event 与 child session event 是两套投递路径，不利于形成统一的“父 agent 看到的是结构化通知，而不是子 agent 原始流”的规则。

## 3. Codex 多 agent 机制梳理

主要代码:

- `E:\projects\ai\codex\codex-rs\core\src\agent\control.rs`
- `E:\projects\ai\codex\codex-rs\core\src\agent\registry.rs`
- `E:\projects\ai\codex\codex-rs\core\src\agent\mailbox.rs`
- `E:\projects\ai\codex\codex-rs\core\src\agent\role.rs`
- `E:\projects\ai\codex\codex-rs\core\src\session\multi_agents.rs`
- `E:\projects\ai\codex\codex-rs\core\src\tools\handlers\multi_agents.rs`
- `E:\projects\ai\codex\codex-rs\core\src\tools\handlers\multi_agents_v2.rs`
- `E:\projects\ai\codex\codex-rs\core\src\tools\handlers\multi_agents_v2\spawn.rs`
- `E:\projects\ai\codex\codex-rs\core\src\tools\handlers\multi_agents_v2\wait.rs`
- `E:\projects\ai\codex\codex-rs\core\src\tools\handlers\multi_agents_v2\message_tool.rs`
- `E:\projects\ai\codex\codex-rs\core\src\tools\handlers\multi_agents_v2\list_agents.rs`
- `E:\projects\ai\codex\codex-rs\tui\src\multi_agents.rs`

### 3.1 统一 AgentControl

Codex 的 `AgentControl` 是每个 root thread/session tree 的共享控制面。根 agent 和所有 sub-agent 使用同一个 control 实例，因此 registry、mailbox、status、spawn limit 都天然处在同一棵树范围内。

关键能力:

- spawn 新 thread，并将其标记为 subagent。
- 根据 parent source 计算 depth 和 agent path。
- 继承当前 turn 的 runtime config，包括 provider/model/reasoning/sandbox/approval/cwd/shell policy/environments 等。
- 支持 forked thread，且 fork 粒度可以是 full history 或 last N turns。
- 通过 graph store 持久化 parent-child edge。
- close 可以按 subtree 清理。

### 3.2 AgentRegistry 与限制

Codex `AgentRegistry` 保存:

- `agent_id`
- `agent_path`
- `agent_nickname`
- `agent_role`
- `last_task_message`
- active agent tree
- used nicknames
- total spawned count

Codex 配置中有默认限制:

- `DEFAULT_AGENT_MAX_THREADS = Some(6)`
- `DEFAULT_AGENT_MAX_DEPTH = 1`
- MultiAgentV2 还有 `max_concurrent_threads_per_session`、`min_wait_timeout_ms`、usage hints、hide metadata 等配置。

这使 Codex 在模型误用或递归 spawning 时能主动限流。本项目当前只有 team 维度的 `max_teammates/max_writers`，且对轻量 `spawn_agent` 缺少统一 thread/depth 限制。

### 3.3 Mailbox 与等待模型

Codex 每个 session 有 mailbox:

- `Mailbox::send` 写入 `InterAgentCommunication`，并递增 monotonic sequence。
- `Mailbox::subscribe` 返回 watch receiver。
- `wait_agent` V2 不是等待某个 agent 结束，而是等待当前 agent mailbox 出现新消息。
- `send_message` 是 queue-only。
- `followup_task` 是 trigger-turn，会唤醒目标 agent 执行新 turn。

这比轮询 child session 状态更清晰:

- 父 agent 等待“有新通信”。
- 子 agent 完成任务后以结构化消息通知父 agent。
- 模型不需要频繁 `read_agent_events` 扫原始 event。
- 控制台不会因为 child streaming 而混入主 transcript。

### 3.4 V1 与 V2 工具面差异

Codex V1 工具更接近当前项目轻量控制面:

- `spawn_agent`
- `send_input`
- `wait_agent`
- `close_agent`
- `resume_agent`

Codex V2 调整为更强的协作抽象:

- `spawn_agent { message, task_name, agent_type, model, reasoning_effort, fork_turns }`
- `list_agents { path_prefix }`
- `send_message { target, message }`
- `followup_task { target, message }`
- `wait_agent { timeout_ms }`
- `close_agent { target }`

V2 的重点不是“从父进程读子进程 log”，而是“agent 之间用 mailbox 通信，并由父 agent 等待通信事件”。

### 3.5 TUI 与观测

Codex TUI 有专门的 multi-agent transcript、collab agent event、agent picker/navigation。用户看到的是结构化协作状态，例如 spawn 请求、交互开始/结束、等待开始/结束、agent 状态，而不是各子线程原始 assistant delta 直接混入主输出。

这对当前项目非常关键，因为用户已经明确观察到:

- 多 agent 执行时控制台持续输出子 agent 数据。
- Ctrl+C 无法稳定中断子 agent。
- reasoning 输出需要检查。
- 多个 agent 并行调用时输出和日志难以分析。

这些问题单靠渲染层过滤可以缓解，但根治需要控制面明确“父 agent 可见内容”与“子 agent 原始运行流”的边界。

## 4. 关键对比

| 维度 | 当前项目 | Codex | 差距判断 |
| --- | --- | --- | --- |
| 基础抽象 | `spawn_agent` child session 与 `spawn_team` team workflow 并存 | 统一 `AgentControl` + `AgentRegistry` + thread tree | 当前项目控制面分裂 |
| agent 身份 | session id、alias、team teammate id 分散存在 | `AgentPath`、nickname、thread id、role 统一登记 | 缺少 canonical path/tree |
| 并发限制 | team 有 `max_teammates/max_writers`，轻量 child 缺少全局限制 | `agent_max_threads` 默认 6，`agent_max_depth` 默认 1 | 当前项目更容易失控 spawn |
| fork 语义 | `fork_context` bool，通常 clone 全会话 | `fork_turns=none/all/N` | 当前项目上下文复制粒度粗 |
| 通信模型 | child 用 `send_input`，team 用 persisted mailbox digest | session mailbox + `send_message/followup_task` | 当前项目通信语义不统一 |
| wait 语义 | `wait_agent` 轮询 child status，team 等待 lifecycle event | V2 等待 mailbox watch sequence | 当前项目延迟和空转更明显 |
| 输出隔离 | 主要依赖 event bridge 过滤和渲染策略 | 子 agent 以结构化 collab notification 汇报 | 当前项目更容易出现终端噪声 |
| 中断/关闭 | session interrupt + team cleanup 组合 | close subtree + graph/status 管理 | 当前项目 descendant 清理语义弱 |
| role 支持 | `agent_type` 更像提示字段 | role layer 可应用配置和提示 | 当前项目角色配置弱 |
| list/navigation | 无统一 `list_agents` | `list_agents` + TUI agent picker | 当前项目可观测性弱 |
| 持久任务图 | team/task/path claim 很强 | Codex 偏轻量协作线程 | 当前项目在 durable workflow 上有优势 |

## 5. 当前项目不足与潜在问题

### 5.1 控制面分裂导致模型误用

当前项目同时暴露 `spawn_agent` 和 `spawn_team`。虽然工具描述已说明两者不同，并且 `broker.go` 已阻止 `wait_agent/read_agent_events` 误用 team teammate id，但这是补丁式防护。根因是两套 id 空间对模型不可见地重叠:

- `member-1` 是 teammate id，不是 child session id。
- child session id 可以有 alias。
- team teammate 也可能有背后的 session id。
- team progress 走 lifecycle event，不走 `wait_agent/read_agent_events`。

改进方向:

- 统一底层 agent registry，team teammate 也登记为 agent。
- tool result 返回 canonical path，例如 `/root/team-a/member-1`。
- `wait_agent/list_agents/send_message/followup_task` 接受 path，而不是让模型猜 id 类型。

### 5.2 wait/read 轮询导致延迟与资源浪费

> 当前同步：固定 50ms 轮询是历史问题描述。当前实现已接入 event watcher / mailbox watcher，固定等待主要是 fallback；本节保留为问题来源记录。

当前轻量 control plane:

- `wait_agent` 每 50ms 调 `agentSnapshot`。
- `read_agent_events` 每 50ms 查 event store。
- team orchestrator 每 1s tick。

这些轮询在单 agent 情况下可接受，但多 agent 并发时会形成大量状态查询和日志噪声，并使“快完成的子任务”至少受调度/tick 间隔影响。

改进方向:

- 为 session status/event store 引入 watch/subscription。
- 为 mailbox 加 monotonic sequence。
- `wait_agent` 优先等待 mailbox/status watch，超时才返回 snapshot。
- team orchestrator 在 task ready、outcome reported、mailbox message、claim released 时被 event 唤醒，保留低频 tick 作为兜底。

### 5.3 输出隔离仍偏渲染层修补

原始问题表现为:

- 子 agent reasoning 和 assistant delta 可能出现在父控制台。
- team task progress 与 root turn 输出交错。
- 用户 Ctrl+C 后 root 被取消，但子 agent 仍可能继续输出或继续运行。

这类问题如果只在 `chat_runtime_events.go` 加过滤，会长期维护困难。更稳的规则应该是:

- 子 agent 原始 streaming event 只写入子 session event store。
- 父 agent 只能收到结构化 notification: started/done/failed/blocked/message。
- 用户显式查看某个 agent 时，才打开该 agent transcript。
- reasoning event 默认按 session scope 隔离，父控制台只显示 root reasoning 或聚合 summary。

### 5.4 轻量 child agent 缺少 mailbox 与 follow-up 分层

`send_input` 当前把“给 agent 留言”和“让 agent 马上开始一轮执行”混在一起，目标忙时需要 `interrupt=true`。这会诱导模型:

- 频繁打断正在执行的 child。
- 因 busy 错误反复重试。
- 不知道什么时候应该排队留言。

Codex V2 的拆分更合理:

- `send_message`: 只投递，不唤醒，不打断。
- `followup_task`: 投递并触发新 turn，但拒绝 root target 等危险目标。

当前项目应将 `send_input` 逐步降级为兼容工具，引入更明确的 mailbox 工具。

### 5.5 缺少统一并发限制和深度限制

当前 team 有 `max_teammates`，但轻量 `spawn_agent` 没有类似 Codex `agent_max_threads` 与 `agent_max_depth` 的全局限制。模型一旦在错误 prompt 下递归 spawn，可能造成:

- 多个 provider stream 同时占用连接。
- runtime event store 与 HTTP artifact 快速膨胀。
- Ctrl+C 后还有未闭合 child session。
- 主控制台持续收到 background events。

改进方向:

- 配置新增 `agents.max_threads`、`agents.max_depth`、`agents.default_fork_turns`。
- registry reserve spawn slot，失败要回滚 reservation。
- root/team/subagent 共用同一计数。

### 5.6 role/config 继承不够严格

当前 `agent_type` 与 model/reasoning 基本通过参数和 session context 传递。Codex 则会在 spawn 时应用 role layer，并继承当前 turn 的 provider/model/reasoning/sandbox/approval/cwd/shell policy/environments。

当前项目需要补齐:

- role 定义与配置层。
- child agent 明确继承 root 的 provider/profile/approval/sandbox/shell 权限。
- role override 与 fork mode 的兼容规则。
- spawned agent 的 effective config 可观测。

### 5.7 team 的 durable workflow 优势没有沉到底层 agent 能力

本项目 team 层有比 Codex 更强的持久任务图:

- SQLite store
- task dependency
- mailbox
- writer slot
- path claim
- terminal reconciliation
- lead planner/final summary

这些能力值得保留，但应定位为 workflow layer。当前 team teammate 执行仍是“另起 session + team metadata”，而不是“创建 agent registry 节点 + task assignment”。这导致 durable workflow 与轻量协作无法复用同一套:

- 状态机
- mailbox
- output isolation
- close/resume
- observability

## 6. 推荐目标架构

建议将多 agent 分成三层:

1. Agent substrate: 统一 agent registry、path、status watch、mailbox、spawn slot、depth limit、subtree close。
2. Collaboration tools: `spawn_agent`、`list_agents`、`send_message`、`followup_task`、`wait_agent`、`close_agent`，面向低延迟临时协作。
3. Workflow tools: `spawn_team`、task dependency、path claim、writer slots、task outcome、team summary，面向持久化复杂任务。

关系:

- `spawn_agent` 直接创建一个 agent registry child。
- `spawn_team` 创建 team workflow，同时为 teammate 创建或绑定 agent registry node。
- team task assignment 不是绕过 registry 直接操作 session，而是通过 agent substrate 向 teammate agent 发送 `followup_task`。
- team mailbox 可以保留持久化 digest，但需要与 agent mailbox 桥接。
- root CLI 只订阅 root transcript 和 collab/team summary event；子 agent 原始 transcript 默认不渲染。

目标示意:

```text
root session
  AgentControl
    AgentRegistry
      /root
      /root/researcher
      /root/reviewer
      /root/team-docs/member-1
      /root/team-docs/member-2
    Mailbox/Event Watch
    Spawn Limits
    Subtree Close

spawn_team
  TeamStore
    team/task/dependency/path-claim/outcome
  uses AgentControl to assign work
```

## 7. 分阶段改进方案

### 阶段 1: 收敛工具契约和安全限制

目标: 先降低模型误用和并发失控风险，不大改架构。

建议任务:

1. 新增配置:
   - `agents.max_threads`，默认建议 6。
   - `agents.max_depth`，默认建议 1。
   - `agents.default_wait_timeout_ms`。
   - `agents.default_fork_turns`，默认建议设为 `none`。实测发现，在当前 Go runtime 中如果默认设为 `all`，子 agent 会继承根会话当前用户 turn，容易把父任务误判为自己的任务并继续递归 `spawn_agent`，随后撞上 `max_depth=1`。需要父上下文时应由模型显式传 `fork_turns=all` 或 `fork_turns=N`。
2. 在 `localActorRegistry.Spawn` 前加入 spawn slot reservation。
3. `spawn_agent` result 返回稳定字段:
   - `id`
   - `session_id`
   - `agent_type`
   - `parent_session_id`
   - `status`
   - 预留 `path`
4. 新增 `list_agents` 轻量工具，至少列出当前 root 下 spawned child sessions。
5. 更新工具描述:
   - 明确 `spawn_team` 是 workflow。
   - 明确 `spawn_agent` 是 lightweight agent。
   - 明确 team teammate id 不可传入 child tools。
6. 增加回归测试:
   - 超过 max threads 返回明确错误。
   - 超过 max depth 返回明确错误。
   - team teammate id 误用仍被拒绝。
   - Ctrl+C 后 child sessions 被 close 或至少不再向 root 输出。

### 阶段 2: 引入 AgentRegistry 与 AgentPath

目标: 建立统一身份体系。

建议任务:

1. 新增包，例如 `backend/internal/agentcontrol`:
   - `Registry`
   - `AgentMetadata`
   - `AgentPath`
   - `SpawnReservation`
   - `StatusWatcher`
   - `Mailbox`
2. root session 初始化时注册 `/root`。
3. `spawn_agent` 时:
   - 申请 spawn slot。
   - 生成 path 和 nickname。
   - 创建 child session。
   - 写入 registry。
   - spawn 失败时释放 reservation。
4. `close_agent` 支持 path target，并关闭 subtree。
5. session store 持久化 parent-child edge，便于 resume 后恢复 agent graph。
6. CLI debug 输出当前 agent graph 摘要。

### 阶段 3: Mailbox 化 communication 和 wait

目标: 替代轮询式 wait/read，让父 agent 等待结构化通信。

建议任务:

1. 给每个 runtime session 增加 mailbox:
   - monotonic seq
   - pending messages
   - `subscribe`
   - `drain`
2. 新增工具:
   - `send_message { target, message }`: queue-only。
   - `followup_task { target, message }`: queue + trigger turn。
   - `wait_agent { timeout_ms }`: 等待当前 session mailbox seq 变化。
3. 保留 `send_input/read_agent_events` 为兼容工具，但在 prompt guidance 中降级。
4. 子 agent 完成时，将 summary 作为 inter-agent communication 发送给 parent。
5. root 控制台只显示 communication summary，不显示 child raw stream。
6. 测试:
   - `send_message` 不打断 running child。
   - `followup_task` 在 child idle 后触发新 turn。
   - `wait_agent` 在 mailbox 收到 done message 后立即返回。
   - child reasoning event 不出现在 root console。

### 阶段 4: team 迁移到统一 agent substrate

目标: 保留 team workflow，同时让 teammate 成为真正 agent node。

建议任务:

1. `spawn_team` 创建 team 时，为 teammate 分配 agent path:
   - `/root/team-<id>/<teammate-id>`
2. `ensureTeammateSessionIDs` 改为通过 registry 预留或查询 teammate agent。
3. `TeammateRunner.StartTask` 不直接裸调 `SubmitPrompt`，而是通过 agent control 发送 trigger task。
4. `report_task_outcome` 同时写:
   - team task outcome
   - agent mailbox notification
   - collab event
5. `Orchestrator.Run` 从纯 tick 改为 event-driven:
   - task created/ready
   - teammate idle
   - outcome reported
   - claim released
   - mailbox message
   - fallback tick
6. team terminal cleanup 使用 registry subtree close。
7. 测试:
   - team teammate 可在 `list_agents` 中看到。
   - `wait_agent` 不再需要区分 child id 和 teammate id，因为都可用 path。
   - team 完成后 teammate subtree 被关闭或标记 completed。

### 阶段 5: 输出隔离、TUI 与可观测性

目标: 解决用户感知最强的控制台噪声和中断问题。

建议任务:

1. 定义 event visibility:
   - root visible
   - parent notification
   - child transcript only
   - debug only
2. runtime event 统一带:
   - `session_id`
   - `agent_path`
   - `parent_agent_path`
   - `visibility`
   - `team_id/task_id` 可选
3. `chat_runtime_events.go` 默认只渲染 root visible 与 parent notification。
4. reasoning event 默认 visibility 为 child transcript only，除非来自当前 foreground agent。
5. Ctrl+C 行为:
   - 首次 Ctrl+C interrupt foreground turn。
   - 若存在 background child/team，发送 subtree interrupt。
   - 再次 Ctrl+C 强制 close subtree。
6. debug/log:
   - 分 session 写 raw transcript。
   - root log 中只记录 child notification 和 artifact path。
   - `/debug` 增加 agent graph、open children、mailbox pending、team active loops。
7. UI/TUI:
   - 增加 agent list/picker。
   - 增加 team/agent progress compact row。
   - 提供打开某个 child transcript 的命令，而不是默认混入主控制台。

## 8. 建议优先级

P0: 先解决用户已经观察到的问题。

- root 控制台不得持续输出 child agent raw assistant delta/reasoning。
- Ctrl+C 必须能中断或关闭当前 root 及其 background children。
- 多 agent 并发时不会用 `wait_agent/read_agent_events` 误等 team teammate。
- reasoning 输出按 session/agent scope 隔离。
- `spawn_agent` 加 max thread/max depth 限制。

P1: 建立统一 agent 身份。

- AgentRegistry。
- AgentPath。
- `list_agents`。
- path target 的 `close_agent`。
- spawn slot reservation。

P2: 引入 mailbox V2 工具。

- `send_message`。
- `followup_task`。
- mailbox-driven `wait_agent`。
- child completion notification。

P3: team substrate 迁移。

- team teammate registry 化。
- team task assignment 走 agent control。
- orchestrator event-driven。
- subtree cleanup。

P4: TUI 和观测。

- agent picker / target（已落地 `/agents pick` 命令/弹窗版与 `/agents target` 默认目标；完整 target switching 面板仍是后续工作）。
- collab event timeline。
- `/debug` agent graph。
- event visibility 分类。

## 9. 建议测试矩阵

### 单元测试

- `spawn_agent` 超过 `agents.max_threads` 时失败，并释放 reservation。
- `spawn_agent` 超过 `agents.max_depth` 时失败。
- `fork_turns=none/all/N` 解析正确，非法值返回模型可读错误。
- `send_message` 不打断 busy child。
- `followup_task` 在 idle child 上触发执行。
- mailbox seq 单调递增。
- `wait_agent` 在 mailbox change 后返回，在超时后返回 timed_out。
- `close_agent` 关闭 subtree。
- team teammate path 能映射到 session id。
- team terminal cleanup 不重复 summary，不降级 done 状态。

### 集成测试

- 3 个 `spawn_agent` 并发执行，root 控制台只显示 spawn/progress/done summary。
- child 产生 reasoning，root 控制台不显示 child reasoning block。
- child 执行中按 Ctrl+C，child 被 interrupt 或 close，之后不再继续输出。
- `spawn_team auto_start=true` 后:
  - team progress 正常显示。
  - 不需要 `wait_agent member-1`。
  - team 完成后 root 收到 `team.summary`。
- 多 team 并行时，foreign team event 不进入当前 foreground session。
- resume session 后，agent graph 与 active team loops 能恢复。

### 手工验证场景

1. 轻量并行:

```text
启动 4 个 spawn_agent 分别阅读 docs/aicli 的不同文档，等待完成后汇总。
```

期望:

- 主控制台只显示每个 child 的 started/done summary。
- 不出现 child 原始 reasoning。
- `list_agents` 可看到 4 个 child。

2. team workflow:

```text
使用 spawn_team 创建 4 个 teammate 查看项目文档，auto_start=true。
```

期望:

- team task progress 通过 team event 显示。
- teammate 不被误当作 child session id。
- team completed 后只输出一次 summary。

3. 中断:

```text
启动多个长任务 child/team 后按 Ctrl+C。
```

期望:

- root turn 停止。
- background children/team task 收到 interrupt。
- 再次 Ctrl+C close subtree。
- 后续没有 background raw output 继续刷屏。

## 10. 实施落点建议

第一批代码落点:

- `backend/internal/config/manager.go`
  - 增加 agents 配置读取与默认值。
- `backend/internal/toolbroker/types.go`
  - 扩展 `SpawnAgentArgs`，预留 `ForkTurns`。
  - 增加 `ListAgentsArgs/Result`。
- `backend/internal/toolbroker/broker.go`
  - 注册 `list_agents`。
  - 将 `send_message/followup_task` 作为后续 V2 工具加入。
- `backend/cmd/aicli/commands/chat_actor_registry.go`
  - 引入 spawn limit。
  - 返回更稳定的 status result。
  - 后续接入 AgentRegistry。
- 新包 `backend/internal/agentcontrol`
  - 放 registry/mailbox/path/status watch。
- `backend/cmd/aicli/commands/chat_runtime_events.go`
  - 增加 event visibility 过滤。
  - child reasoning 默认不渲染到 root。
- `backend/cmd/aicli/commands/chat_interrupt.go`
  - 中断时遍历当前 root agent subtree。
- `backend/internal/team/teammate_runner.go`
  - 后续改为通过 agent control 派发 teammate task。

第二批代码落点:

- `backend/internal/team/orchestrator.go`
  - 从 tick-only 改为 watch + fallback tick。
- `backend/internal/team/mailbox.go`
  - 与 agent mailbox 桥接。
- `backend/cmd/aicli/commands/chat_team_lifecycle.go`
  - active team loops 与 agent graph 联动。
- `backend/internal/team/sqlite_store.go`
  - 增加 teammate agent path/session binding 字段，或增加独立映射表。

## 11. 验收标准

改进完成后，应满足:

1. 模型可以可靠区分临时 child agent 与持久 team workflow，或在统一 path target 下无需区分。
2. 多 agent 并行执行时，root 控制台不会持续输出子 agent raw stream。
3. reasoning 输出按 foreground session 隔离，子 agent reasoning 不污染 root transcript。
4. Ctrl+C 能中断当前 foreground turn，并能清理 background child/team descendant。
5. `wait_agent` 不再依赖频繁轮询 event store。
6. `list_agents` 能展示 root 下所有 active/completed child/team teammate。
7. `spawn_agent` 有默认并发和深度限制。
8. `spawn_team` 保留 task dependency/path claim/outcome 等 durable workflow 优势，同时复用统一 agent substrate。
9. debug log 能定位每个 agent/team/task 的执行状态、artifact path 和最后消息。

## 12. 总结

当前项目的多 agent 实现已经有相当多的工程基础，尤其是 team workflow 的持久化、任务依赖、path claim、writer slot 和 outcome 机制，这些是 Codex 轻量 multi-agent 机制中没有直接覆盖的优势。

真正需要改进的是底层协作控制面: 当前轻量 child session 和 persistent team 是并列体系，导致 id、等待、通信、中断、输出隔离和观测都需要额外桥接。Codex 的价值不在于更多工具，而在于统一的 `AgentControl`、`AgentRegistry`、mailbox wait、agent path、spawn limit 和结构化 collab event。

因此建议不要废弃 `spawn_team`，而是将它降为 durable workflow layer；底层新建统一 agent substrate，让 `spawn_agent` 和 team teammate 都成为同一棵 agent tree 上的节点。这样可以同时获得 Codex 式低噪声协作体验，以及当前项目已经具备的持久化团队任务编排能力。

## 13. 已落地修复状态（更新至 2026-05-08）

本节记录按本报告继续推进后的当前实现状态，避免后续重复分析同一批问题。

已完成:

- P0 输出隔离与 reasoning 隔离相关回归: child/non-primary reasoning 不再默认污染 root 控制台，当前覆盖 `assistant_reasoning` 与 legacy `assistant.reasoning` 两类 runtime event；非交互输出中的 reasoning-only 内容会被抑制。
- P0 Ctrl+C 清理: `chat_interrupt.go` 已能中断 foreground run，并清理当前 root 关联的 child/team runtime 状态；child 清理已从 direct child 扩展到当前 root 的 descendant subtree，避免 nested `spawn_agent` 在父会话 Ctrl+C 后继续运行，同时不会误停其他 root 的 sibling subtree。
- P0 team teammate id 误用保护: `wait_agent/read_agent_events` 会拒绝直接作用于 `spawn_team` teammate id，并提示等待 team lifecycle/team.summary。
- P0 spawn 限制: 新增 `agents.maxThreads`、`agents.maxDepth`、`agents.defaultWaitTimeoutMs`、`agents.defaultForkTurns`，默认 `maxThreads=6`、`maxDepth=1`、`defaultForkTurns=none`。
- P1 agent path/list: `spawn_agent` session 写入 parent/root/path/depth，`list_agents` 可展示 root 下 child session，并支持 `path_prefix` 与 `include_closed`。
- P1 path target 与 subtree: `send_message`、`send_input`、`wait_agent`、`read_agent_events`、`resume_agent`、`close_agent` 支持 `/root/...` agent path 目标；`close_agent` 关闭父 path 会同步关闭该节点及其 descendant child sessions，并返回 `closed_count/closed_session_ids`。
- P1 API controller path 修正: `/root/...` 目标解析不再默认只列 `agent` 用户 session，而是通过 all-session index 查找 path，避免非默认用户会话下 path target 找不到。
- P2 `wait_agent` event-store wake: `wait_agent` 会优先订阅目标 session 的 event store watcher，在目标 session 写入 `session_end`、`session_interrupted`、`assistant_message`、`approval_requested`、`question_asked`、`mailbox_received` 等 ready/wake event 时立即重新快照状态；runtime event bus 仍作为兼容 wake 源，固定 fallback 从 50ms 高频轮询降为 500ms。新增 CLI/API 测试覆盖“不 publish runtime bus，仅 AppendEvent 也能唤醒 wait_agent”的场景。
- P2 `wait_agent` parent mailbox 模式: `wait_agent` 现在支持不传 `id/ids/session_id/session_ids`。无目标调用会自动等待当前 parent session mailbox/collab 事件，并支持 `after_seq` cursor；当 runtime store 实现 `MailboxReaderStore/WatchMailbox` 时，CLI 与 runtime API 会优先读取/监听独立 session mailbox substrate，返回的 `event/events/latest_seq` 是 mailbox sequence；缺少 mailbox substrate 时才回退到 `mailbox_received/subagent.completed/team.completed/team.summary` session event 镜像。传入 id 时仍保持旧的 child status wait 兼容语义。这让工具面更接近 Codex V2 “父 agent 等待 mailbox 通信”的模型，同时不破坏既有 child-session wait。
- P2 runtime API parent mailbox wait: `POST /api/runtime/sessions/{id}/agents/wait` 现在在请求体没有 `id/ids/session_id/session_ids` 时也会自动进入 parent mailbox wait，目标 session 使用 URL 中的 `{id}`；typed client `WaitSessionAgentsRequest/Response` 已补齐 `after_seq/mailbox_only/event/events/latest_seq` 字段，`skillsapi-demo` 的 session-agent `wait` action 也允许不传 `agent-id`，用于真实验证 parent mailbox 等待。
- P2 `read_agent_events` durable event-store wake: `chat.EventStore` 新增可选 `EventWatcherStore/EventSequenceStore`，内存与 SQLite runtime store 均支持 `WatchEvents` 和 `LastEventSeq`。`read_agent_events` 带 `wait_ms` 时会优先订阅目标 session 的 event store watcher，目标 session 写入新 event 后即刻重新读取；runtime event bus 仍作为兼容 wake 源，固定 fallback 从 50ms 高频轮询降为 500ms。新增测试覆盖“不 publish runtime bus，仅 AppendEvent 也能唤醒 read_agent_events”的场景。
- P2 `read_agent_events` parent mailbox 模式: `read_agent_events` 现在也支持不传 `id/session_id`。无目标调用会读取当前 parent session mailbox/collab 事件，并支持 `after_seq/limit/wait_ms/latest_seq`，用于 durable catch-up；当 runtime store 实现 `MailboxReaderStore/WatchMailbox` 时，CLI 与 runtime API 会优先从独立 session mailbox substrate 读取，返回 cursor 是 mailbox sequence；缺少 mailbox substrate 时才回退到 `mailbox_received/subagent.completed/team.completed/team.summary` session event 镜像。传入 id 时仍保持旧的 child runtime event 读取语义。Runtime HTTP 新增 `GET /api/runtime/sessions/{id}/agents/events`，typed client 新增 `ListSessionAgentMailboxEvents`，`skillsapi-demo -agent-action events` 无 `agent-id` 时会走 parent mailbox/collab event 读取。
- P2 child completion 兼容展示事件: `spawn_agent` child session 的 `session_end/session_interrupted` 仍会向 parent session event store 追加一条轻量 `subagent.completed` 事件，payload 包含 child session、path、agent type、status、success/error/trace/source_event_seq 等摘要字段；但它现在明确标记 `display_mirror=true`、`mirror_source=agent_control_mailbox` 和 `mailbox_delivery_status`，语义上只是 CLI/TUI/旧事件消费者的展示镜像，不再是 completion 的主控制记录。CLI 与 runtime API controller 均已覆盖。
- P2 child completion 父级 mailbox 主路径: child completion 现在会先向 parent session 写入一条 `mailbox_received` / `kind=subagent.completed` 的 durable AgentControl mailbox message，metadata 透传 child session、parent、path、agent type、status、success/error 和 child terminal source event seq；成功或失败状态再反映到后续展示镜像。CLI `localActorRegistry` 与 runtime API `sessionAgentController` 均已覆盖。该路径优先通过 `DeliverMailboxEventFirst` / `DeliverMailboxStoreFirst` 进入 mailbox substrate 和 `AppendAgentControlMailbox` canonical writer，再写兼容 session event 并发布 runtime bus；父 actor 不在线或不可创建时也不会丢 completion 通知；actor delivery 只作为没有 durable mailbox/event store 时的兜底。completion mailbox 携带 `message_type=agent_control.subagent_completed`、`control_action=agent.completed`、`workflow=spawn_agent`、`mailbox_delivery=session_mailbox`、`mailbox_kind=subagent.completed`，因此 child completion 已从“session event mirror + mailbox mirror”升级为可被统一 AgentControl mailbox/substrate 消费的 durable control message 主路径。
- P2 session mailbox 统一投递入口: 新增 `chat.DeliverMailboxEventFirst`，把 CLI/API 两条路径中重复的 mailbox 投递逻辑收口为共享 helper。该 helper 的语义是“先写 mailbox substrate，带 durable `mailbox_seq` 后再写兼容 session event 并发布 runtime bus；event store 不可用或失败时才显式回退到 actor delivery；没有 fallback 时返回真实持久化错误”。目前 child completion、`send_message`、busy `followup_task`、team task assignment 和 team mailbox session dispatch 均已通过该 helper 走 mailbox-first 路径。
- P2 mailbox substrate 接口化与独立 session mailbox 表: 新增 `chat.MailboxStore` / `MailboxReaderStore` / `MailboxWatcherStore` / `MailboxSequenceStore` / `SessionEventMailboxStore` / `DeliverMailboxStoreFirst`，把 mailbox 投递、读取和 watch 调用点统一到可替换 substrate。`InMemoryRuntimeStore` 维护独立 per-session mailbox sequence；`SQLiteRuntimeStore` 曾在 migration v9 新增 `session_mailbox_messages`，现在 migration v14 已把历史行回填到 `agent_control_mailbox_records(scope=session)` 并删除该 legacy 表。`AppendMailbox` / `AppendAgentControlMailbox` 均先写统一 mailbox record，再写 `mailbox_received` session event 展示兼容镜像；event payload 同时带 `seq` 和 `mailbox_seq`。`wait_agent/read_agent_events` 的 parent mailbox 模式现在优先使用 mailbox substrate 的 `ListMailbox/WatchMailbox`，session event 镜像只作为兼容 fallback。后续完整 AgentControl mailbox 表仍可直接实现这些 mailbox substrate 接口，不需要再改 CLI/API/team 投递和读取调用点。
- P2/P3 AgentControl mailbox 持久表与独立 sequence: `SQLiteRuntimeStore` migration v10 新增旧 scoped `agent_control_mailbox_messages`，migration v11 为其增加 per-session `seq`，migration v12 新增统一形态 `agent_control_mailbox_records` 并以 `scope=session` 承载 runtime/session mailbox 主记录；migration v13/v14 会在历史数据回填后分别删除旧 scoped table 与 legacy `session_mailbox_messages`。`ListMailbox` / `LastMailboxSeq` 现在从统一 record 的 `session_mailbox_seq` 投影；`ListAgentControlMailbox` / `LastAgentControlMailboxSeq` 只返回带 AgentControl workflow/envelope 的统一 record，并用 `id` 作为 control cursor。这让 `GET /agent-control/mailbox`、`AgentControlMailboxSequenceStore` 与普通 session mailbox API 都不再依赖普通 session mailbox seq 表或旧 scoped 表。
- P2/P3 AgentControl mailbox 可读/可写 substrate: `chat` 新增 `AgentControlMailboxReaderStore` / `AgentControlMailboxWatcherStore` / `AgentControlMailboxSequenceStore` / `AgentControlMailboxStore` 与 `IsAgentControlMailboxMessage`。`SQLiteRuntimeStore` 的普通 mailbox 与 AgentControl-only list/sequence 都已读取 `agent_control_mailbox_records(scope=session)`；AgentControl-only API 会过滤 `workflow` 非空的 control rows，普通 `ListMailbox` 则从同一表按 `session_mailbox_seq` 返回完整 mailbox 流。`InMemoryRuntimeStore` 也维护独立 `controlMailbox/controlSeq`，不再只是每次过滤普通 mailbox。两种 store 都提供独立 AgentControl mailbox watch，control watcher 现在由统一 control record / `controlMailbox` 写入直接唤醒。`AppendAgentControlMailbox` canonical writer surface 要求消息携带 AgentControl envelope；SQLite/InMemory 的 canonical writer 现在先写 AgentControl control row，再同步 `mailbox_received` session event 展示镜像，`AppendMailbox` 仍保留普通 mailbox 兼容入口并会把携带 envelope 的消息写入 control row。新增 `chat.ListMailboxAgentControlFirst` / `chat.WatchMailboxAgentControlFirst`，把“AgentControl control rows 优先、普通 mailbox rows 兼容合并、durable high-water mark 判断、watch 去重”的逻辑从 CLI/API 两套实现收口为共享 helper；parent mailbox 合并视图使用 `session_mailbox_seq` 去重和排序，AgentControl-only API 使用 `seq/control_seq` 作为 cursor。Runtime HTTP 进一步新增 `GET /api/runtime/sessions/{id}/agent-control/mailbox`，直接返回 AgentControl mailbox row，而不是转换成 `mailbox_received` runtime event；typed client 新增 `ListSessionAgentControlMailbox`，`skillsapi-demo -mode session-agent -agent-action control-mailbox` 可直接读取 control mailbox substrate。`/collab` 现在会优先探测 AgentControl mailbox reader，并在 header 中标出 `source=agent_control+mailbox` 与 `control_events`，同时仍补全普通 mailbox 行和非 mailbox 协作终态事件，便于验证新 substrate 是否真的有数据。`wait_agent/read_agent_events` 的 parent mailbox 模式现在也会优先读取/订阅 AgentControl mailbox substrate，再与普通 mailbox 行按 `session_mailbox_seq` 合并去重，因此 control message 已经进入主等待/读取路径；旧 session event 镜像仍保留兼容返回。
- P2/P3 team 终态 parent mailbox 通知: 新增 `team.BuildTeamLifecycleMailboxMessage`，把新产生的 `team.completed/team.summary` 终态事件额外写入 lead/root session 的 durable mailbox substrate，`kind=team.lifecycle`，metadata 包含 `message_type=agent_control.team_lifecycle`、`control_action=team.lifecycle`、`event_type`、`team_id/status/summary` 等字段。原 `team.completed/team.summary` session event 与 runtime bus 渲染保持不变，mailbox mirror 只用于 parent mailbox wait/read 与 `/collab` 审计，避免控制台重复刷屏。CLI `localChatRuntimeHost` 与 Skills API orchestrator event subscription 两条路径均已覆盖，并且测试已直接通过 `AgentControlMailboxReaderStore.ListAgentControlMailbox` 读取到 team lifecycle control row。
- P2/P3 child completion envelope builder 收口: 新增 `toolbroker.BuildSubagentCompletionMailboxMessage`，由 CLI `localActorRegistry.deliverSubagentCompletionMailbox` 与 Runtime API `sessionAgentController.deliverSubagentCompletionMailbox` 共同生成 `agent_control.subagent_completed` / `agent.completed` durable mailbox envelope；新增 `toolbroker.AnnotateSubagentCompletionDisplayMirror` 统一标注兼容 `subagent.completed` 展示镜像。两条路径仍保留各自的 delivery wiring，但 metadata/Body/From/To/Kind 与 display mirror 标记已不再重复手写，后续迁到单一 AgentControl mailbox table/API 时只需要替换共享 builder 或 writer surface。
- P2 team mailbox durable sequence/watch 基础: `team.MailMessage` 增加 per-team `seq`，`MailFilter.AfterSeq` 支持按 durable sequence 增量读取；SQLite migration v7 会为既有 `team_mailbox_messages` 回填 sequence 并建立 `(team_id, seq)` 索引；`SQLiteStore.WatchMail/LastMailSeq` 与 `MailboxService.Wait` 提供“内存通知 + durable catch-up”的 watch 语义，通知丢失时仍可通过 `AfterSeq` 恢复。
- P2 runtime API mailbox 增量读取: `GET /api/runtime/teams/{id}/mailbox` 支持 `after_seq` / `after` 参数，并在响应 filters 中回显 `after_seq`；返回的 `MailMessage.seq` 可作为下一次增量读取 cursor。
- P2 工具面分层: 新增 `send_message` 与 `followup_task`。前者只投递消息，后者在 idle child 上触发新 turn，busy child 上只投递消息不打断。
- P2 child agent mailbox durable event-store 优先: `send_message` 和 busy/unavailable `followup_task` 的 mailbox envelope 现在复用 `toolbroker.BuildAgentMailboxMessage`，优先直接向目标 child session event store 写入 `mailbox_received` 并发布 runtime bus；只有没有 event store/bus 时才回退到 actor delivery。queue-only `send_message` 不再为了投递消息而启动目标 actor，目标 actor 不在线时仍可通过 `read_agent_events` 读取 durable mailbox event。
- P2 child agent mailbox control envelope 标准化: 普通 child `send_message` 与 busy `followup_task` 的 mailbox metadata 现在也携带 `message_type=agent_control.agent_message/agent_control.followup_task`、`control_action=agent.message/agent.followup_task`、`workflow=spawn_agent`、`mailbox_delivery=session_mailbox`、`mailbox_kind=agent_message/followup_task`。这让轻量 child mailbox 与 `team.task_assignment`、`team.lifecycle` 使用同一类 AgentControl 风格 envelope，后续统一 AgentControl mailbox/substrate 可以按 control metadata 消费，而不需要识别零散的 `trigger_turn` 局部字段。
- P2/P3 共享 AgentControl envelope helper: 新增 `backend/internal/agentcontrol`，集中定义 `workflow`、`message_type`、`control_action`、`mailbox_delivery`、`mailbox_kind` 与 `agent_control.trigger_task` 等控制面常量，并提供 `Envelope.Metadata` / `ApplyEnvelope` / `MetadataString` / `HasEnvelopeMetadata` helper。`spawn_agent` 的 `send_message/followup_task/subagent.completed`、`spawn_team` 的 `team.task_assignment/team.lifecycle` 均改为复用该包，避免 CLI、runtime API、tool broker 和 team workflow 各自硬编码 AgentControl metadata。SQLite runtime store 会基于这些 canonical metadata 直接写入 `agent_control_mailbox_records(scope=session)`；后续完整 AgentControl mailbox/registry table 只需继续消费同一组控制字段。
- P2/P3 共享 AgentRegistry projection helper: `backend/internal/agentcontrol` 进一步集中定义 agent session context key、path segment sanitize、root/path/depth 解析、child path 计算、team teammate path 投影和 context 写入去重 helper。`toolbroker.AgentSessionContext*` 现在只是对 `agentcontrol.SessionContext*` 的兼容别名，CLI `localActorRegistry` 与 Runtime API `sessionAgentController` 均复用同一套 projection helper。Runtime API 现在也会像 CLI 一样把 `spawn_team` teammate session 投影到 `/root/teams/<team>/<member>` 并持久化 parent/root/path/depth/type context，因此 `list_agents`、path prefix 过滤和后续 path target 解析可以在 API 控制面看到 team teammate，而不再只存在于本地 CLI registry。
- P3 team task lifecycle AgentRegistry 投影: 新增 `team.FindTeammateBySession` / `team.ActiveTaskForAssignee`，并在 CLI `localActorRegistry` 与 Runtime API `sessionAgentController` 的 `AgentStatusResult` 中填充 `team_id`、`teammate_id`、`current_task_id`、`current_task_status`。`/agents`、agent picker 和 `/debug` agent graph 会展示 `team/teammate/task/task_status` 字段，使 teammate task lifecycle 至少在 AgentRegistry 读模型中成为 first-class projection。进一步新增 storage-neutral `agentcontrol.TaskRecord`、`agentcontrol.TaskFilter` 和 `agentcontrol.TaskRegistryReader` seam，并由 `team.AgentControlTaskRegistry` adapter 把 team task 投影为统一 read-model，字段包含 workflow、team、assignee、session、agent path、title、summary、status、priority 和时间戳；CLI/API 的当前 task 展示也已改为通过 `team.ActiveAgentControlTaskRecordForAssignee` 消费该 AgentControl task registry read seam。Runtime HTTP 新增 `GET /api/runtime/agent-control/tasks`，typed client 新增 `ListAgentControlTasks`，可按 workflow/team/assignee/status/path_prefix/limit 读取统一 `TaskRecord` 列表，不再只能通过 team-native `/teams/{id}/tasks` 或 `/agents` 局部投影间接查看。新增写侧 seam `agentcontrol.TaskRegistryCreateWriter` / `TaskCreateRequest`、`TaskDependencyCreateWriter` / `TaskDependencyCreateRequest`、`TaskRegistryStatusWriter` / `TaskStatusUpdateRequest`、`TaskRegistryReleaseWriter` / `TaskReleaseRequest`、`TaskRegistryLeaseRenewWriter` / `TaskLeaseRenewRequest`、`TaskRegistryClaimWriter` / `TaskClaimRequest`、`TaskRegistryTerminalWriter` / `TaskTerminalUpdateRequest`、`TaskRegistryBlockWriter` / `TaskBlockRequest`，`team.AgentControlTaskRegistry` 可把 AgentControl-shaped create、dependency edge create、status/summary 更新、lease release、lease renew、claim、done/failed terminal transition 和 blocked status transition 映射到现有 team store，并返回统一 `TaskRecord` 或持久 graph edge。Runtime API `CreateTask` 已接入 `CreateAgentControlTask`，Runtime API `AddTaskDependency`、toolbroker `spawn_team` 初始 task/dependency 创建、`LeadPlanner` 自动 replan/follow-up task/dependency 创建也已改为通过 `team.AgentControlTaskRegistry` 对应 writer，`ReleaseTaskLease` 已接入 `ReleaseAgentControlTask`，`RenewTaskLease` 已接入 `RenewAgentControlTaskLease`；`Orchestrator.ClaimReadyTasks` 已通过 `ClaimAgentControlTask` 执行普通 claim 与 path-claim 原子分支；`ApplyTerminalTaskOutcome` 已通过 `UpdateAgentControlTaskTerminal` 执行 done/failed 终态落库，`ApplyBlockedTaskOutcome` 已通过 `BlockAgentControlTask` 执行 blocked/handoff 的 task blocked 落库，不再直接在 outcome 层调用 store update/release/block。Runtime HTTP 进一步新增显式 AgentControl task 写接口: `POST /api/runtime/agent-control/tasks`、`POST /api/runtime/agent-control/tasks/{task_id}/dependencies`、`/status`、`/claim`、`/lease`、`/release`、`/terminal`、`/block`；typed client 也新增 `CreateAgentControlTask`、`CreateAgentControlTaskDependency`、`UpdateAgentControlTaskStatus`、`ClaimAgentControlTask`、`RenewAgentControlTaskLease`、`ReleaseAgentControlTask`、`UpdateAgentControlTaskTerminal`、`BlockAgentControlTask`。这些入口全部调用 `AgentControlTaskRegistry` writer seam，并在 release/lease/terminal 路径保留现有 path claim 兼容副作用。SQLite `agent_control_task_records` 现在是 task 主表，`CreateTask`、`UpdateTask`、`UpdateTaskStatus`、`MarkReadyTasks`、`ClaimTask`、`ClaimTaskWithPathClaims`、`RenewTaskLease`、`ReleaseTask`、`BlockTask` 和 teammate session upsert 都直接读写该表；legacy `team_tasks` 已退化为 migration-only 回填输入并在 migration v19 删除。
- P3 task retry/reclaim/cancel 写路径继续收敛: 新增 `agentcontrol.TaskRegistryRetryWriter` / `TaskRetryRequest`，由 `team.AgentControlTaskRegistry.RetryAgentControlTask` 统一执行 retry count increment、lease/assignee release、状态回队列和可选 summary 更新。`RetryTask` HTTP handler、offline teammate sweep 中的 task reclaim、`LeaseManager.ReclaimExpiredTasks`、`LeaseManager.RenewTask` 和 CLI Ctrl+C `cancelActiveTeamTasks` 已不再直接调用 team-native `IncrementTaskRetry` / `ReleaseTask` / `RenewTaskLease` / `UpdateTask` 作为外层编排路径，而是通过 AgentControl retry/release/lease writer seam 进入 task graph adapter。这样中断取消、lease 回收、离线回收、人工 retry 都和前面的 create/claim/terminal/block 路径一样，依赖统一 AgentControl-shaped task lifecycle API，并直接落到 AgentControl task graph 主表。
- P3 full task patch 写路径继续收敛: 新增 `agentcontrol.TaskRegistryUpdateWriter` / `TaskUpdateRequest`，由 `team.AgentControlTaskRegistry.UpdateAgentControlTask` 统一执行完整 task patch，包括 title/goal/status/priority/assignee/paths/deliverables/summary/result_ref/parent_task_id 等字段。Runtime API `PATCH /api/runtime/teams/{id}/tasks/{task_id}` 已改为通过该 update writer seam，而不是在 handler 中直接修改 `team.Task` 后调用 `store.UpdateTask`；同时新增显式 AgentControl endpoint `PATCH /api/runtime/agent-control/tasks/{task_id}` 和 typed client `UpdateAgentControlTask`。这把 create/update/status/release/lease/retry/claim/terminal/block 这些任务写入口进一步统一到 AgentControl-shaped writer surface；后续切换单一 AgentControl task table 时，API handler 不需要再理解 team-native task mutation 细节。
- P3 AgentControl task dependency read seam 与 graph event cursor: 新增 `agentcontrol.TaskDependencyRecord` / `TaskDependencyFilter` / `TaskDependencyReader`，`team.AgentControlTaskRegistry.ListAgentControlTaskDependencies` 可把 task DAG 投影成统一 dependency edge read-model，并支持按 `task_id` 读取 dependencies、按 `include_dependents` 读取 dependents、按 `team_id/workflow/depends_on_id` 过滤。SQLite `agent_control_task_dependencies` 现在是 dependency 主表，dependency create 会写入 `task.dependency.created` team event，payload 包含 `dependency_id/task_id/depends_on_id/team_id`，重复 edge 不会重复写事件；legacy `team_task_dependencies` 已退化为 migration-only 回填输入并在 migration v19 删除。进一步新增 `agentcontrol.TaskGraphEvent` / `TaskGraphEventFilter` / `TaskGraphEventReader`，SQLite migration 新增 `agent_control_task_graph_events` 表；`AppendTeamEvent` 会把 `task.*` team event 同步镜像到该表，`team.AgentControlTaskRegistry.ListAgentControlTaskGraphEvents` 则优先读取该 AgentControl task graph event 表，并保留 workflow/team/event_type/task/dependency/全局 `seq`/`team_seq`/payload/created_at。Runtime HTTP 新增 `GET /api/runtime/agent-control/tasks/{task_id}/dependencies` 和 `GET /api/runtime/agent-control/tasks/events`，typed client 新增 `ListAgentControlTaskDependencies` 与 `ListAgentControlTaskGraphEvents`，`skillsapi-demo -mode agent-control -control-action tasks/update/dependencies/events/add-dependency` 也可直接验证 task read/update、dependency graph 读写入口和 graph event cursor。该 read seam 已把 cursor 从 per-team seq 升级为 `agent_control_task_graph_events.id` 全局 cursor，同时保留 `team_seq` 兼容原有 team timeline 语义。
- P3 blocked/handoff 与 replan 路径继续收敛: `LeadPlanner.materializePlan` 在 `AutoPersist=true` 时已改为通过 `team.AgentControlTaskRegistry.CreateAgentControlTask` 创建 replan/follow-up 任务，并通过 `CreateAgentControlTaskDependency` 创建 replan 依赖边，因此 `ApplyBlockedTaskOutcome` 自动重规划产生的新任务和依赖都能从 AgentControl seam 进入现有持久层。`ApplyBlockedTaskOutcome` 的 blocked/handoff 通知现在统一通过 `BuildBlockedTaskOutcomeMailboxMessage` 构造，保留旧 `warning` / `handoff` kind 以兼容 mailbox digest 和旧 UI，同时补齐 `message_type=agent_control.task_lifecycle`、`control_action=task.lifecycle`、`workflow=spawn_team`、`mailbox_kind=team.task_lifecycle`、`event_type=task.blocked/task.handoff` 等 envelope metadata；经 CLI/API `DispatchTeamMailboxMessage` 投递到目标 session 后，会进入 `ListAgentControlMailbox` 可读的 control mailbox row。task/dependency 实体主写入、replan/follow-up 创建、blocked/handoff lifecycle mailbox 和 graph event cursor 已进入 AgentControl seam，后续重点不再是这条路径的旧 mirror 写权威，而是跨进程部署时的单一 registry 服务化和同事务提交边界。
- P2 followup busy 判定持久化: `followup_task` 触发前会同时检查内存 actor state 与持久 `RuntimeStateStore`；当目标 child 处于 running/approval/input/rewinding 等 busy 状态时，不会误触发新 turn，而是退化为 durable `mailbox_received kind=followup_task`。这覆盖了恢复后或跨进程场景里 actor 内存状态与持久状态不一致的问题。
- P2 team mailbox session dispatch event-store 优先: `DispatchTeamMailboxMessage` 不再默认为了向 lead/teammate 展示 team mailbox 而启动目标 actor；当 session event store 存在时，CLI/API 两条路径都会把 team mailbox 转成目标 session 的 `mailbox_received` 持久事件，并通过 runtime bus 唤醒读取方。API handler 即使没有 session manager/hub，只要配置了 session event store，也能完成 durable dispatch；只有没有 event store 时才回退 live actor delivery。这样 `send_team_message`、orchestrator lead progress、blocked/handoff 通知等 team mailbox 消息也能被 `read_agent_events after_seq` 看到，并减少后台 actor 被通知路径意外拉起的情况。
- P3 team teammate AgentRegistry 主路径: team teammate session 会被规范化投影为 `/root/teams/<team>/<member>`，并且 API `UpsertTeammate` / `UpdateTeammate`、`spawn_team` tool path 与本地 CLI projector 都会在 teammate 写入成功后立即 upsert durable `agent_control_agents`；清空 session 会关闭旧 active registry row，closed row 不会被重新打开。`list_agents`、path target、subtree close 和 `/debug` agent graph 因此可以直接消费 AgentRegistry 主表，缺少 durable store 时才回退 session/team projection。
- P3 team task assignment trigger seam: `TeammateRunner` 新增 `TaskTriggerClient` / `TaskTriggerRequest`，runner 会优先通过 `AgentControl.TriggerTask` 语义触发 teammate task，缺省再回退到旧的 `SessionClient.SubmitPrompt`；本地 CLI `localActorRegistry` 与 runtime API `sessionActorClient` 均已实现 `TriggerTask`，`getTeamOrchestrator` 和本地 host wiring 会显式注入 `Runner.AgentControl`。这已把“裸 SubmitPrompt 派发”收口到可替换的 agent-control seam，后续切换底层 substrate 时可以保留同一触发接口。
- P3 team task dispatch 持久审计: `AgentControl.TriggerTask` 派发前后会向 `team_events` 追加 `task.dispatch.requested` / `task.dispatch.completed`，payload 包含 `team_id/task_id/agent_id/assignee/session_id/prompt_chars/via/success/trace_id/steps/error` 等字段；本地 CLI 与 runtime API 两条路径均复用 `internal/team` 的统一 helper。这样 teammate task assignment 不再只是不可见的 `SubmitPrompt` 调用，`/timeline` 和日志分析可以按 seq 看到派发请求与派发结果。
- P3 team task assignment teammate mailbox mirror: `AgentControl.TriggerTask` 在提交 teammate prompt 前，还会向目标 teammate session 写入 `mailbox_received kind=team.task_assignment`，metadata 包含 `team_id/task_id/agent_id/assignee/session_id/target_session_id/prompt_chars/prompt_preview/via` 等字段；CLI 与 runtime API 均覆盖。这样 task assignment 同时出现在 team event timeline 和 teammate agent event stream 中，进一步接近统一 mailbox/substrate 语义。
- P3 team task assignment control message 标准化: `team.task_assignment` mailbox envelope 现在携带 `message_type=agent_control.task_assignment`、`control_action=task.assign`、`workflow=spawn_team`、`mailbox_delivery=session_mailbox`、`mailbox_kind=team.task_assignment` 等标准 metadata。CLI `localActorRegistry.TriggerTask` 与 runtime API `sessionActorClient.TriggerTask` 的测试均证明 task assignment 不只存在于 `team_events` 审计流或 session event 镜像中，也可以通过 `MailboxReaderStore.ListMailbox` 和 `AgentControlMailboxReaderStore.ListAgentControlMailbox` 从目标 teammate session 的 durable mailbox substrate 读取到。这把 `spawn_team` task assignment 从“裸 SubmitPrompt + 附属日志”推进为可替换 AgentControl substrate 能消费的 control/task message。
- P3 team task terminal lifecycle teammate mailbox mirror: `task.completed/task.failed` 仍保留在 `team_events` 作为 team timeline 的权威审计流，同时 CLI `localChatRuntimeHost` 与 Runtime API `Handler` 会根据 event payload 中的 `assignee` 找到对应 teammate session，并向该 session 写入 `mailbox_received kind=team.task_lifecycle`。该消息携带 `message_type=agent_control.task_lifecycle`、`control_action=task.lifecycle`、`workflow=spawn_team`、`mailbox_delivery=session_mailbox`、`mailbox_kind=team.task_lifecycle`、`event_type/task_id/team_id/assignee/summary/error_type` 等 metadata。CLI/API 测试已同时覆盖普通 `ListMailbox` 与 `ListAgentControlMailbox` 读取。这样 task terminal transition 不再只存在于 team workflow 内部 timeline，teammate agent event stream 也能通过 durable AgentControl mailbox 看到自己的 task lifecycle 终态；同时不会把每个 task 终态刷到 parent mailbox，避免主协作时间线噪声增加。
- P3 team orchestrator wake: `Orchestrator.RunWithWake` 支持事件唤醒 + fallback tick，启动后会先做一次 immediate tick；本地 CLI 与 runtime API 的 team lifecycle loop 会在 `SyncLoops` 命中已有 active team 时发送非阻塞 wake signal，减少 ready task 等待下一个 tick 的延迟。
- P3 team orchestrator mailbox sequence wake: `Orchestrator.RunWithWake` 现在会订阅 team mailbox watcher，并用 `LastMailSeq` 作为 durable high-water mark；当 mailbox 插入新 sequence 时可在 fallback tick 前唤醒编排 loop，外部 lifecycle wake 仍作为兼容入口保留。
- P3 team task lifecycle durable wake: 新增内部 `TaskSignal` / `TaskWatcherStore` / `TaskSequenceStore`，SQLite migration v8 增加 `team_task_signals(team_id, seq, task_id, kind, status, created_at)`；`CreateTask(ready/running 等非 pending)`、`MarkReadyTasks`、`ClaimTask`、`ClaimTaskWithPathClaims`、`ReleaseTask`、`BlockTask`、`UpdateTaskStatus`、`UpdateTask`、`IncrementTaskRetry`、`RenewTaskLease` 和 SQLite terminal outcome 快路径都会记录 task lifecycle signal。`agentcontrol` 进一步新增 `TaskWakeFilter` / `TaskWakeEvent` / `TaskWakeWatcher` / `TaskWakeSequencer` / `TaskWakeSource` seam，SQLite migration v16 新增统一 `agent_control_wake_events`；当前写路径直接写统一 wake registry，migration v18 删除旧 `agent_control_task_wake_signals` 兼容表。`Orchestrator.RunWithWake` 现在优先消费 `agentcontrol.TaskWakeSource`，并用 AgentControl task wake sequence 做 durable high-water mark。pending task creation 不发 signal，避免 planner 先建 pending task、后写 dependency edge 的窗口里被提前提升为 ready。
- P3 team mailbox durable wake AgentControl seam: `agentcontrol` 新增 `MailboxWakeFilter` / `MailboxWakeEvent` / `MailboxWakeWatcher` / `MailboxWakeSequencer` / `MailboxWakeSource` seam；当前写路径直接写统一 `agent_control_wake_events`，migration v18 删除旧 `agent_control_mailbox_wake_messages` 兼容表。`Orchestrator.RunWithWake` 不再直接依赖 team `MailWatcherStore/MailSequenceStore`，而是消费 `agentcontrol.MailboxWakeSource`，并用 AgentControl mailbox wake sequence 做 durable high-water mark。
- P3 team mailbox 实体 AgentControl-first: SQLite migration v15 新增历史 team-scoped `agent_control_mailbox_messages`，并从 `team_mailbox_messages` 回填历史消息；migration v17 新增统一形态 `agent_control_mailbox_records`，以 `scope=team` 承载 team mailbox 主记录，并从 v15 scoped 表回填；migration v18 删除 `agent_control_mailbox_messages` 与 `team_mailbox_messages`。`SQLiteStore.InsertMail/ListMail/LastMailSeq/RecordMailReceipt` 现在只以 `agent_control_mailbox_records` 作为主写入/主读取/主 sequence/主 ack 更新来源；公开 `ListMail` 返回的 `Seq` 来自 `team_seq`，`ControlSeq` 来自统一 mailbox record id。新增 `TestSQLiteStoreListMailPrefersAgentControlMessages` 和 `TestSQLiteStoreRecordMailReceiptDoesNotRequireMirrorRows` 确认旧表不存在时公开读取、ack、receipt 与 unread 视图仍以统一 AgentControl mailbox record 为准。
- P3 统一 AgentControl wake registry: `agentcontrol` 新增 generic `WakeFilter/WakeEvent/WakeSource` 与 `WakeKindMailbox/WakeKindTask`；SQLite migration v16 新增 `agent_control_wake_events`，把 task wake 和 mailbox wake 统一写入同一张 AgentControl wake registry。`SQLiteStore.InsertMail` 和 `appendTaskSignal` 现在只写 `agent_control_wake_events`，不再写 legacy `agent_control_mailbox_wake_messages` / `agent_control_task_wake_signals`；migration v18 删除旧 wake mirror 表。`team.AgentControlMailboxWake` 与 `team.AgentControlTaskRegistry` 也会优先 watch/read 统一 registry，只有缺少该能力时才回退 team-native signal。新增测试确认旧 wake mirror table 不存在时 high-water mark 仍从统一 registry 读取。
- P3 共享 AgentControl mailbox read model 与统一物理表形态: `agentcontrol` 新增 `MailboxRecordFilter/MailboxRecord/MailboxRegistryReader/MailboxRegistrySequencer`，统一表达 runtime/session mailbox control row 与 team-scoped mailbox control row。SQLite runtime store 与 SQLite team store 均新增同名同结构 `agent_control_mailbox_records`，分别以 `scope=session` 和 `scope=team` 写入主 mailbox record；旧 runtime/team scoped `agent_control_mailbox_messages` 与 legacy `team_mailbox_messages` 已在迁移中回填后删除。`SQLiteRuntimeStore` / `InMemoryRuntimeStore` 可把 session control mailbox 投影成 `scope=session` 记录，`SQLiteStore` 可把 team mailbox 投影成 `scope=team` 记录；两侧 high-water mark 也都暴露为 `LastAgentControlMailboxRecordSeq`。在此基础上新增 `agentcontrol.CombinedMailboxRegistry` 作为 in-process cursor bridge，并进一步新增 `agentcontrol.GlobalMailboxRegistry` / `NamedMailboxRegistrySource` 作为命名 global registry substrate；Runtime HTTP `GET /api/runtime/agent-control/mailbox` 现在消费这个 global registry substrate，而不是在 handler 内手写 sources + registry 并行状态。typed client 新增 `ListAgentControlMailbox`，`skillsapi-demo -mode agent-control -control-action mailbox` 可直接读取 runtime/session 与 team 组合后的 mailbox registry。`chat.NewAgentControlMailboxRegistry` 与 `team.NewAgentControlMailboxRegistry` 同时提供非 SQLite fallback projection：runtime 可从 AgentControl mailbox reader、普通 mailbox reader 或 event store 投影，team 可从 `Store.ListMail` 与 team id 枚举投影。测试已覆盖旧表不存在时 shared registry record 仍可读，也覆盖 lower-source 新消息不会被 combined cursor 跳过。这里的“统一物理表”指 runtime/team 两个 SQLite store 都落地同名同 schema 主表；`GlobalMailboxRegistry` 目前仍委托 `CombinedMailboxSeq` 生成 time-major 的 in-process bridge cursor，不是跨进程 durable global row id；由于 runtime store 与 team store 仍可能是不同 SQLite 数据库，尚不是单库/单服务全局 registry。
- P3 非 SQLite fallback cursor 收敛: runtime/session mailbox fallback、team mailbox fallback 和 team task graph event fallback 在没有显式 `session_id` / `team_id` 过滤时，不再把 `after_seq` 直接下推为每个 session/team 的本地 sequence，而是先读取各本地流并生成稳定的合并 cursor，再在 projection 层应用 `after_seq`。这修复了多个 session/team 并行时“session A 的 seq=1 导致 session B 后来的 seq=1 被跳过”的兼容投影问题；SQLite 主路径仍继续使用 durable AgentControl/global row id。
- P3 orchestrator cancellation 归一化: `RunWithWake` 在 SQLite 查询/sequence 读取因 context cancellation 返回 `sqlite3: interrupted` 等驱动错误时，会优先返回 `context.Canceled`，避免 Ctrl+C 或 lifecycle stop 路径把正常取消误报为 SQLite 执行错误。
- P3 terminal wait replay 保护: `waitForTeamTerminal` / `localTeamLifecycleService.WaitForTerminal` 在确认 team 已 settle 后会 replay 已持久化的 `team.completed/team.summary` 终态事件，确保 no-interactive drain 和测试场景不会因为异步 runtime bus 事件到达时序而漏掉 base session summary 镜像。
- P3 terminal summary settled 判定收紧: 真实全量回归暴露出一个窗口: SQLite `ReconcileTerminalTeamState` 会先把 team 状态更新为 done 并持久化 `team.completed`，随后才调用 lead planner 生成并持久化 `team.summary`；在高并发测试或慢 summary provider 下，`WaitForTerminal` 可能看到 team 已 done 就过早返回，导致 base session history 偶发缺少最终 summary 镜像。现在 `localTeamLifecycleService.RunSettled` 在 team 状态为 done 且本地 orchestrator 配置了 `LeadPlanner` 时，会继续等待 `team.summary*` 事件持久化后才返回 settled；`team.summary.failed` 也算终态摘要结果，避免 summary 失败路径无限等待。
- P3 terminal reconcile 保护: team 终态 reconcile 现在会把 busy teammate 视为未 settle，避免运行中的 teammate 尚未发布 `task.completed` 时提前发出 `team.completed/team.summary`。
- P3 terminal task event 顺序修复: `task.completed/task.failed` 不再由 `Orchestrator.executeAssignment` 在 teammate session 返回后单独补发，而是下沉到共享 `ApplyTerminalTaskOutcome` 路径。无论结果来自 child 的 `report_task_outcome` 工具调用、Skills API task outcome handler，还是 orchestrator fallback parser，都会先持久化并去重 terminal task event，再触发 `ReconcileTerminalTeamState`。这修复了真实日志中 `team.completed` seq 早于 `task.completed` 的倒序问题；CLI/API broker 也会把同一个 `TeamEventBus` 传入 outcome path，使 live timeline 与 durable `team_events` 顺序一致。terminal wait replay 现在也会重放 `task.completed/task.failed`，作为 live bus 漏通知时的兜底；parent mailbox 仍只镜像 `team.completed/team.summary`，避免每个 task 终态重复刷入 `/collab`。
- P3 team SQLite memory store 稳定性: `SQLiteStore` 对 `:memory:` / `mode=memory` DSN 限制单连接，避免 HTTP/API 测试或轻量运行时跨连接访问时出现 schema 丢失。
- P4 `/debug` agent graph: `/debug` 已增加当前 root 下 agent tree 摘要，包含 path、status、session、session state、parent、depth、type、pending approval/question/tool 等排障字段；运行时调试区也会显示当前 `Agent Target`，便于确认恢复会话后默认 child 目标是否仍然生效。
- P4 `/debug` mailbox pending: `/debug` 已增加当前 active team 的 unread mailbox 摘要，展示 team、agent、unread count、message id、kind、from/to、task、body preview，且不会 mark read。
- P4 CLI 可见 agent/timeline/collab 入口: 新增 `/agents` 输出当前 agent tree，复用 `list_agents`/path 投影结果；当前已选默认目标会以 `selected=<target>` 行展示。新增 `/agents pick` / `/agents select`，在可用时复用现有 runtime selection popup，否则回退为编号输入选择器，可按编号、agent path、session id 或 agent id 选择并打印 agent 明细。新增 `/agents target <target|clear>`，可设置或清空默认 agent 消息目标；无参数 `/agents target` 现在会显示当前 selected target，并列出可用 agent target 候选且用 `*` 标记当前选中项；该选择写入 runtime session metadata context 的 `aicli_selected_agent_target`，恢复会话时会重新注入 `ChatSession.SelectedAgentTarget`。新增 `/agents send [target] <message>` 和 `/agents followup [target] <message>`，复用 `send_message/followup_task` 控制面显式向目标 child agent 投递 mailbox 或触发后续任务；省略 `target` 时使用 `/agents target` 或 picker 选中的默认目标。正常 root 用户输入仍不会被隐式重定向到 child，避免误把主对话 turn 发送给后台 agent。新增 `/timeline [team|active] [limit] [filter=<text>]` 输出当前 active team 或显式 team id 的持久 `team_events` 协作时间线，包括 seq、event type、task、session、assignee、status、via、success、trace、summary/error 等核心字段；`filter=` / `match=` 会保留 header 上下文并按事件行文本过滤，便于按 `task=`、`assignee=`、`session=`、`trace=` 或 `via=` 快速定位。`task.dispatch.requested/completed` 的派发请求、目标 session、AgentControl via 和执行结果现在可直接在 timeline 中审计，恢复会话或多 team 排障时也可绕过 active team binding 直接指定 team。新增 `/collab [follow] [target|selected|parent|all] [limit] [filter=<text>] [timeout=10s]` 输出 parent、当前选中 agent、显式 session/path 目标或 parent + 所有 child/team teammate 的 mailbox/collab 时间线，并支持 `filter=` / `match=` 对具体事件行做文本过滤；显式 `follow` 会通过 AgentControl-first mailbox watcher 等待下一次 mailbox 更新并刷新视图，`timeout=` / `wait=` 控制等待时长；当 store 支持 `MailboxReaderStore` 时，`/collab` 会优先读取 durable session mailbox substrate row，并标记 `source=mailbox`，同时过滤 session event mirror 避免重复展示，再补充 `subagent.completed/team.completed/team.summary` 等非 mailbox 协作终态事件；不支持 mailbox substrate 时才回退到兼容 session event 镜像。这便于直接排查 child completion、team summary、parent mailbox 通知、selected child mailbox 以及跨 agent mailbox 是否 durable 到达。这些都是低风险 CLI/TUI 可见性入口；完整富交互可切换 target 的多 agent 面板仍是后续 P4 工作。
- P4 `/collab` AgentControl envelope 可见性: `/collab` 的 mailbox substrate 行和 session-event fallback 行现在都会把 mailbox metadata 中的 `message_type`、`control_action`、`workflow`、`mailbox_delivery`、`mailbox_kind`、`event_type`、`target_session_id` 渲染为 `msg/action/workflow/delivery/mailbox/event/target` 字段。这样在排查 child completion、`send_message/followup_task`、`team.task_assignment`、`team.lifecycle` 时，不需要打开原始 JSON 即可确认控制消息是否已经走统一 AgentControl envelope。

已验证:

- `go test ./internal/agentcontrol ./internal/toolbroker ./internal/team -run "Test(Envelope|BuildAgentMailboxMessageUsesAgentControlEnvelope|TeammateRunnerPrefersAgentControlTriggerTask)" -count=1 -v`
- `go test ./internal/agentcontrol ./internal/toolbroker -run "Test(Envelope|RegistryProjection|SetContext|Broker_Definitions|BuildAgentMailboxMessageUsesAgentControlEnvelope)" -count=1 -v`
- `go test ./cmd/aicli/commands ./internal/api/skills -run "Test(LocalActorRegistry_ListIncludesTeamTeammateSessions|SessionAgentController_(PathTargetsAndCloseSubtree|ProjectsTeamTeammatesIntoAgentList))" -count=1 -v`
- `go test ./internal/team ./internal/toolbroker -run "Test(AgentProjectionFindsTeammateAndActiveTask|BrokerAgent|Agent|CacheSafe)" -count=1 -v`
- `go test ./cmd/aicli/commands ./internal/api/skills -run "Test(LocalActorRegistry_ListIncludesTeamTeammateSessions|SessionAgentController_ProjectsTeamTeammatesIntoAgentList|ChatAgentPickerPopupLinesIncludeAgentDetails|ChatDebugAgentGraphLinesListsLocalAgents)" -count=1 -v`
- `go test ./cmd/aicli/commands -run "Test(ChatCollabLinesListsParentMailboxEvents|HandleCommand_CollabPrintsParentMailboxTimeline|ChatTimelineLinesIncludesTaskDispatchDetails)" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestHandleCommand_CollabPrints(Parent|Selected)AgentMailboxTimeline|TestChatCollabLinesListsParentMailboxEvents|TestChatSlashCommandCatalogMatchesHandleCommandRoutes|TestChatSlash" -count=1 -v`
- `go test ./internal/toolbroker -run "TestBuild(SubagentCompletionMailbox|AgentMailbox)MessageUsesAgentControlEnvelope" -count=1 -v`
- `go test ./internal/toolbroker ./cmd/aicli/commands ./internal/api/skills -run "TestBuild(SubagentCompletionMailbox|AgentMailbox)MessageUsesAgentControlEnvelope|Test(LocalActorRegistry|SessionAgentController)_(MirrorsChildCompletionToParentEvents|PersistsCompletionMailboxWithoutParentActor)" -count=1 -v`
- `go test ./internal/chat -run "TestDeliverMailbox(Event|Store)FirstPrefersAgentControlWriterForEnvelope|TestListMailboxAgentControlFirst|TestWatchMailboxAgentControlFirst" -count=1 -v`
- `go test ./cmd/aicli/commands ./internal/api/skills -run "TestLocalActorRegistry_TriggerTaskUsesSessionHub|TestSessionActorClientTriggerTaskPersistsDispatchEvents" -count=1 -v`
- `go test ./cmd/aicli/commands ./internal/api/skills -run "Test(LocalChatRuntimeHost_(TeamLifecyclePersistsParentMailbox|TaskLifecyclePersistsTeammateMailbox)|HandlerDeliverTeam(LifecycleMailboxPersistsToLeadSession|TaskLifecycleMailboxPersistsToTeammateSession)|LocalActorRegistry_TriggerTaskUsesSessionHub|SessionActorClientTriggerTaskPersistsDispatchEvents)" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestChatTimeline(CommandLinesListsExplicitTeamEvents|LinesListsActiveTeamEvents|LinesShowsRecentEventsInSequenceOrder|LinesIncludesTaskDispatchDetails)|TestChatSlashCommandCatalogMatchesHandleCommandRoutes|TestChatSlash" -count=1 -v`
- `go test ./cmd/aicli/commands ./internal/api/skills ./internal/agentcontrol -run "Collab|Timeline|Agent|RegistryProjection|SetContext|Envelope" -count=1 -v`
- `go test ./cmd/aicli/commands ./internal/api/skills -run "Test(LocalActorRegistry_(MirrorsChildCompletionToParentEvents|PersistsCompletionMailboxWithoutParentActor)|SessionAgentController_(MirrorsChildCompletionToParentEvents|PersistsCompletionMailboxWithoutParentActor)|HandlerDeliverTeamLifecycleMailboxPersistsToLeadSession)" -count=1 -v`
- `go test ./cmd/aicli/commands -run "Test(LocalChatRuntimeHost_MirrorsTeamSummaryIntoBaseSession|LocalTeamLifecycleService_RunSettledWaitsForDoneSummaryEvent)" -count=1 -v`
- `go test ./cmd/aicli/commands ./internal/api/skills ./internal/team -run "TeamLifecycle|RunSettled|WaitForTerminal|team.summary|Terminal" -count=1 -v`
- `go test ./internal/api/skills -run "TestSessionAgentController_(PathTargetsAndCloseSubtree|WaitUsesEventStoreWakeup|ReadEventsUsesEventStoreWakeup)"`
- `go test ./cmd/aicli/commands -run "TestLocalActorRegistry_(WaitUsesEventStoreWakeup|ReadEventsUsesEventStoreWakeup|AgentPathTargetsResolveToSession|CloseAgentPathClosesSubtree)"`
- `go test ./cmd/aicli/commands -run TestLocalActorRegistry_MirrorsChildCompletionToParentEvents -count=1 -v`
- `go test ./internal/api/skills -run TestSessionAgentController_MirrorsChildCompletionToParentEvents -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestLocalActorRegistry_(MirrorsChildCompletionToParentEvents|PersistsCompletionMailboxWithoutParentActor|DispatchTeamMailboxMessageRoutesToActor|TriggerTaskUsesSessionHub)" -count=1 -v`
- `go test ./internal/api/skills -run "TestSessionAgentController_(MirrorsChildCompletionToParentEvents|PersistsCompletionMailboxWithoutParentActor)|TestHandlerDispatchTeamMailboxMessageHandlesEmptyTargets|TestSessionActorClientTriggerTaskPersistsDispatchEvents" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestLocalActorRegistry_(SendMessagePersistsMailboxWithoutTargetActor|ReadEventsUsesEventStoreWakeup|DispatchTeamMailboxMessageRoutesToActor)" -count=1 -v`
- `go test ./internal/api/skills -run "TestSessionAgentController_(SendMessagePersistsMailboxWithoutTargetActor|ReadEventsUsesEventStoreWakeup)|TestHandlerDispatchTeamMailboxMessageHandlesEmptyTargets" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestLocalActorRegistry_(SendMessagePersistsMailboxWithoutTargetActor|FollowupTaskPersistsMailboxWhenTargetBusy|TriggerTaskUsesSessionHub)" -count=1 -v`
- `go test ./internal/api/skills -run "TestSessionAgentController_(SendMessagePersistsMailboxWithoutTargetActor|FollowupTaskPersistsMailboxWhenTargetBusy)|TestSessionActorClientTriggerTaskPersistsDispatchEvents" -count=1 -v`
- `go test ./internal/team -run "Test(SQLiteStoreListMailAfterSeqReturnsLaterMessages|MailboxServiceWaitUsesDurableSequenceAndWake|OrchestratorRun(WakesFromMailboxSequenceBeforeFallbackTick|WithWakeProcessesReadyTaskBeforeFallbackTick|TicksImmediately)|MailboxReceiptsArePerAgentAndIncludeBroadcast|SQLiteStoreListMailFilters)" -count=1 -v`
- `go test ./internal/api/skills -run "TestListMailboxHandler(MarksMessagesReadForAgent|SupportsAfterSeq)" -count=1 -v`
- `go test ./internal/team -run "TestTeammateRunner(PrefersAgentControlTriggerTask|StartTaskIncludesRunMeta|ParsesStructuredJSONOutcome|MarksMissingStructuredOutcomeAsProtocolError)" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestLocalActorRegistry_(SubmitPromptUsesSessionHub|TriggerTaskUsesSessionHub|DispatchTeamMailboxMessageRoutesToActor)" -count=1 -v`
- `go test ./internal/api/skills -run "TestGetTeamOrchestratorEnrichesInjectedOrchestrator|TestSyncTeamLifecycleLoopsCompletesTeamWhenTeammateReportsOutcomeViaBroker" -count=1 -v`
- `go test ./internal/team -run "TestSQLiteStore(TaskSignalsPersistSequenceAndWake|MarkReadyTasksEmitsTaskSignal|PendingTaskCreationDoesNotEmitTaskSignal)|TestApplyTerminalTaskOutcomeReleasesTaskAndSetsResultRef|TestOrchestratorRunWakesFrom(TaskSignal|MailboxSequence)BeforeFallbackTick|TestOrchestratorRunWithWakeProcessesReadyTaskBeforeFallbackTick" -count=1 -v`
- `go test ./internal/team -run "TestOrchestratorRunTicksImmediately|TestSQLiteStore(TaskSignalsPersistSequenceAndWake|MarkReadyTasksEmitsTaskSignal|PendingTaskCreationDoesNotEmitTaskSignal)|TestApplyTerminalTaskOutcomeReleasesTaskAndSetsResultRef|TestOrchestratorRunWakesFrom(TaskSignal|MailboxSequence)BeforeFallbackTick|TestOrchestratorRunWithWakeProcessesReadyTaskBeforeFallbackTick" -count=1 -v`
- `go test ./cmd/aicli/commands -run "Test(ChatDebugAgentGraphLinesListsLocalAgents|ChatDebugMailboxLinesListsPendingTeamMessages|ChatTimelineLinesListsActiveTeamEvents|HandleCommand_DebugPrintsSessionArtifactsAndRuntimeState)|TestChatSlash" -count=1 -v`
- `go test ./internal/team -run "TestAgentControl(MailboxRegistryFallbackUsesCombinedCursorAcrossTeams|TaskGraphFallbackUsesCombinedCursorAcrossTeams)" -count=1`
- `go test ./internal/chat -run "TestAgentControlMailboxRegistry(EventFallbackUsesCombinedCursorAcrossSessions|ProjectsEventStoreFallback)" -count=1`
- `go test ./cmd/aicli/commands -run "TestLocalActorRegistry_TriggerTaskUsesSessionHub|TestChatTimelineLines" -count=1 -v`
- `go test ./internal/api/skills -run "TestSessionActorClientTriggerTaskPersistsDispatchEvents|TestGetTeamOrchestratorEnrichesInjectedOrchestrator|TestSyncTeamLifecycleLoopsCompletesTeamWhenTeammateReportsOutcomeViaBroker" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestLocalActorRegistry_TriggerTaskUsesSessionHub" -count=1 -v`
- `go test ./internal/api/skills -run "TestSessionActorClientTriggerTaskPersistsDispatchEvents|TestGetTeamOrchestratorEnrichesInjectedOrchestrator" -count=1 -v`
- `go test ./cmd/aicli/commands ./internal/api/skills ./internal/team -run "Test(LocalActorRegistry_TriggerTaskUsesSessionHub|SessionActorClientTriggerTaskPersistsDispatchEvents|TeammateRunnerPrefersAgentControlTriggerTask)" -count=1 -v`
- `go test ./internal/chat -run TestDeliverMailboxEventFirst -count=1 -v`
- `go test ./internal/chat -run "TestDeliverMailbox(EventFirst|StoreFirst)" -count=1 -v`
- `go test ./internal/chat -run "Test(InMemoryRuntimeStoreAppendMailbox|SQLiteRuntimeStoreAppendMailbox|NewSessionEventMailboxStorePrefersNativeMailboxStore)" -count=1 -v`
- `go test ./internal/chat -run "Test(SQLiteRuntimeStoreAppendMailbox|SQLiteRuntimeStoreMigratesSessionMailboxTable|InMemoryRuntimeStoreAppendMailbox|DeliverMailbox(EventFirst|StoreFirst))" -count=1 -v`
- `go test ./cmd/aicli/commands ./internal/api/skills ./internal/chat -run "Test(LocalActorRegistry_(WaitWithoutTargetUsesParentMailbox|ReadEventsWithoutTargetUsesParentMailbox)|SessionAgentController_(WaitWithoutTargetUsesParentMailbox|ReadEventsWithoutTargetUsesParentMailbox)|InMemoryRuntimeStoreAppendMailbox|SQLiteRuntimeStoreAppendMailbox|SQLiteRuntimeStoreMigratesSessionMailboxTable)" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestLocalActorRegistry_DispatchTeamMailboxMessage" -count=1 -v`
- `go test ./internal/api/skills -run "TestHandlerDispatchTeamMailboxMessage" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestChatTimelineLines|TestLocalActorRegistry_(SendMessagePersistsMailboxWithoutTargetActor|FollowupTaskPersistsMailboxWhenTargetBusy|PersistsCompletionMailboxWithoutParentActor|TriggerTaskUsesSessionHub)" -count=1 -v`
- `go test ./internal/api/skills -run "TestSessionAgentController_(SendMessagePersistsMailboxWithoutTargetActor|FollowupTaskPersistsMailboxWhenTargetBusy|PersistsCompletionMailboxWithoutParentActor)|TestSessionActorClientTriggerTaskPersistsDispatchEvents" -count=1 -v`
- `go test ./internal/chat -run "Test(InMemoryRuntimeStoreWatchEventsAndLastSeq|SQLiteRuntimeStoreWatchEventsAndLastSeq|SQLiteRuntimeStoreAppendEventIsSerialized)" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestLocalActorRegistry_(WaitUsesEventStoreWakeup|ReadEventsUsesEventStoreWakeup)" -count=1 -v`
- `go test ./internal/api/skills -run "TestSessionAgentController_(WaitUsesEventStoreWakeup|ReadEventsUsesEventStoreWakeup|WaitUsesRuntimeEventWakeup)" -count=1 -v`
- `go test ./internal/toolbroker -run "TestBroker_Execute_WaitAgent" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestLocalActorRegistry_WaitWithoutTargetUsesParentMailbox" -count=1 -v`
- `go test ./internal/api/skills -run "TestSessionAgentController_WaitWithoutTargetUsesParentMailbox" -count=1 -v`
- `go test ./internal/api/skills ./pkg/skillsapi ./cmd/skillsapi-demo ./internal/toolbroker ./cmd/aicli/commands -run "WaitSessionAgentsWithoutTarget|SessionAgentEndpointsEncodePathsAndQuery|SessionAgentWaitParentMailbox|WaitWithoutTarget|WaitAgentWithoutTarget" -count=1 -v`
- `go test ./cmd/skillsapi-demo -count=1 -v`
- `go test ./cmd/aicli/commands -run TestLocalActorRegistry_ReadEventsUsesEventStoreWakeup -count=1 -v`
- `go test ./internal/api/skills -run TestSessionAgentController_ReadEventsUsesEventStoreWakeup -count=1 -v`
- `go test ./internal/toolbroker ./cmd/aicli/commands ./internal/api/skills ./pkg/skillsapi ./cmd/skillsapi-demo -run "ReadAgentEventsWithoutTarget|ReadEventsWithoutTarget|ListSessionAgentEventsWithoutAgent|SessionAgentMailboxEvents|SessionAgentEndpointsEncodePathsAndQuery" -count=1 -v`
- `go test ./cmd/aicli/commands -run TestLocalChatRuntimeHost_MirrorsTeamSummaryIntoBaseSession -count=1 -v`
- `go test ./internal/team -run "Test(OrchestratorRun(TicksImmediately|WithWakeProcessesReadyTaskBeforeFallbackTick|StopsWhenTeamNotActive)|ReconcileTerminalTeamStateWaitsForBusyTeammate|ReconcileTerminalTeamStateDoesNotDuplicateSummarySideEffects)"`
- `go test ./internal/api/skills -run "TestSyncTeamLifecycleLoops(EnrichesInjectedOrchestratorBeforeStartingLoops|SignalsExistingLoop|CompletesTeamWhenTeammateReportsOutcomeViaBroker)" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestAICLIChatActorExecutor_(InteractiveAutoStartRendersTeamTimeline|AutoStartQueuedTasksStaySerializedPerTeammate)"`
- `go test ./cmd/aicli/commands -run "Test(ChatDebugAgentGraphLinesListsLocalAgents|HandleCommand_DebugPrintsSessionArtifactsAndRuntimeState)"`
- `go test ./cmd/aicli/commands -run "Test(ChatDebugAgentGraphLinesListsLocalAgents|ChatDebugMailboxLinesListsPendingTeamMessages|HandleCommand_DebugPrintsSessionArtifactsAndRuntimeState)"`
- `go test ./pkg/skillsapi -run TestClient_CreateAndListTeams -count=1 -v`
- `go test ./internal/events ./internal/toolbroker ./internal/api/skills ./cmd/aicli/commands`
- `go test ./...`
- `go test ./cmd/aicli/commands -run "Test(ChatCollabLinesListsParentMailboxEvents|HandleCommand_CollabPrintsParentMailboxTimeline|ChatSlashCommandCatalogMatchesHandleCommandRoutes|ChatSlash)" -count=1 -v`
- `go test ./internal/toolbroker ./cmd/aicli/commands ./internal/api/skills ./internal/chat ./pkg/skillsapi ./cmd/skillsapi-demo -run "WaitAgent|ReadAgentEvents|ReadEvents|Mailbox|SessionAgentEvents|SessionAgentMailboxEvents|SessionAgentEndpoints|DeliverMailbox|AppendMailbox|Collab|Timeline|ChatSlash" -count=1 -v`
- `go test ./cmd/aicli/commands -run "Test(ChatAgentPickerResolvesByNumberPathAndSession|ChatAgentPickerPopupLinesIncludeAgentDetails|ChatSlashCommandCatalogMatchesHandleCommandRoutes|ChatSlash)" -count=1 -v`
- `go test ./internal/toolbroker ./cmd/aicli/commands ./internal/api/skills ./internal/chat ./internal/team ./pkg/skillsapi ./cmd/skillsapi-demo -run "WaitAgent|ReadAgentEvents|ReadEvents|Mailbox|SessionAgentEvents|SessionAgentMailboxEvents|SessionAgentEndpoints|DeliverMailbox|AppendMailbox|Collab|Timeline|ChatSlash|TriggerTask|TaskDispatch|TeammateRunner|AgentPicker|ChatAgentPicker" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestLocalChatRuntimeHost_(TeamLifecyclePersistsParentMailbox|MirrorsTeamSummaryIntoBaseSession)|TestLocalActorRegistry_(WaitWithoutTargetUsesParentMailbox|ReadEventsWithoutTargetUsesParentMailbox)" -count=1 -v`
- `go test ./internal/api/skills -run "Test(HandlerDeliverTeamLifecycleMailboxPersistsToLeadSession|SessionActorClientTriggerTaskPersistsDispatchEvents|WaitSessionAgentsWithoutTargetUsesParentMailbox|ListSessionAgentEventsWithoutAgentReadsParentMailbox)" -count=1 -v`
- `go test ./internal/toolbroker ./cmd/aicli/commands ./internal/api/skills ./internal/chat ./internal/team ./pkg/skillsapi ./cmd/skillsapi-demo -run "WaitAgent|ReadAgentEvents|ReadEvents|Mailbox|SessionAgentEvents|SessionAgentMailboxEvents|SessionAgentEndpoints|DeliverMailbox|AppendMailbox|Collab|Timeline|ChatSlash|TriggerTask|TaskDispatch|TeammateRunner|AgentPicker|ChatAgentPicker|TeamLifecycle" -count=1 -v`
- `go test ./cmd/aicli/commands -run "Test(HandleCommand_AgentsSendPersistsMailboxMessage|ParseChatAgentMessageCommandPreservesMessageSpaces|ChatAgentPickerResolvesByNumberPathAndSession|ChatAgentPickerPopupLinesIncludeAgentDetails|ChatSlashCommandCatalogMatchesHandleCommandRoutes|ChatSlash|LocalActorRegistry_SendMessagePersistsMailboxWithoutTargetActor|LocalActorRegistry_FollowupTaskPersistsMailboxWhenTargetBusy)" -count=1 -v`
- `go test ./internal/toolbroker ./cmd/aicli/commands ./internal/api/skills ./internal/chat ./internal/team ./pkg/skillsapi ./cmd/skillsapi-demo -run "WaitAgent|ReadAgentEvents|ReadEvents|Mailbox|SessionAgentEvents|SessionAgentMailboxEvents|SessionAgentEndpoints|DeliverMailbox|AppendMailbox|Collab|Timeline|ChatSlash|TriggerTask|TaskDispatch|TeammateRunner|AgentPicker|ChatAgentPicker|AgentsSend|TeamLifecycle" -count=1 -v`
- `go test ./cmd/aicli/commands -run "Test(ChatRuntimeContext_RoundTripsSelectedAgentTarget|HandleCommand_AgentsTargetProvidesDefaultSendTarget|HandleCommand_AgentsSendPersistsMailboxMessage|ParseChatAgentMessageCommandPreservesMessageSpaces|ChatSlashCommandCatalogMatchesHandleCommandRoutes|ChatSlash)" -count=1 -v`
- `go test ./cmd/aicli/commands -run "Test(HandleCommand_DebugPrintsSessionArtifactsAndRuntimeState|ChatDebugAgentGraphLinesListsLocalAgents|ChatRuntimeContext_RoundTripsSelectedAgentTarget|HandleCommand_AgentsTargetProvidesDefaultSendTarget|ChatSlashCommandCatalogMatchesHandleCommandRoutes|ChatSlash)" -count=1 -v`
- `go test ./internal/toolbroker ./cmd/aicli/commands ./internal/api/skills -run "Test(BuildAgentMailboxMessageUsesAgentControlEnvelope|LocalActorRegistry_SendMessagePersistsMailboxWithoutTargetActor|LocalActorRegistry_FollowupTaskPersistsMailboxWhenTargetBusy|SessionAgentController_SendMessagePersistsMailboxWithoutTargetActor|SessionAgentController_FollowupTaskPersistsMailboxWhenTargetBusy)" -count=1 -v`
- `go test ./internal/agentcontrol -run "TestEnvelope" -count=1 -v`
- `go test ./internal/chat -run "Test(SQLiteRuntimeStoreAppendMailbox|SQLiteRuntimeStoreMigratesSessionMailboxTable|DeliverMailbox|InMemoryRuntimeStoreAppendMailbox)" -count=1 -v`
- `go test ./internal/chat -run "Test(InMemoryRuntimeStore(AgentControlMailboxFiltersEnvelope|AppendMailbox)|SQLiteRuntimeStoreAppendMailbox|SQLiteRuntimeStoreMigratesSessionMailboxTable|DeliverMailbox)" -count=1 -v`
- `go test ./cmd/aicli/commands -run "Test(ChatCollabLinesListsParentMailboxEvents|HandleCommand_CollabPrintsParentMailboxTimeline)" -count=1 -v`
- `go test ./internal/chat -run "Test(MergeMailboxMessagesBySeq|InMemoryRuntimeStoreAgentControlMailboxFiltersEnvelope|SQLiteRuntimeStoreAppendMailboxMirrorsAgentControlEnvelope)" -count=1 -v`
- `go test ./internal/chat -run "Test(DeliverMailbox|NewSessionEventMailboxStore|MergeMailbox|InMemoryRuntimeStoreAgentControlMailboxFiltersEnvelope|SQLiteRuntimeStoreAppendMailboxMirrorsAgentControlEnvelope)" -count=1 -v`
- `go test ./internal/chat -run "Test(ListMailboxAgentControlFirst|WatchMailboxAgentControlFirst|DeliverMailbox|MergeMailbox|InMemoryRuntimeStoreAgentControlMailboxFiltersEnvelope|SQLiteRuntimeStoreAppendMailboxMirrorsAgentControlEnvelope)" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestLocalActorRegistry_(WaitWithoutTargetUsesParentMailbox|ReadEventsWithoutTarget(UsesParentMailbox|MergesAgentControlMailbox))" -count=1 -v`
- `go test ./internal/api/skills -run "TestSessionAgentController_(WaitWithoutTargetUsesParentMailbox|ReadEventsWithoutTarget(UsesParentMailbox|MergesAgentControlMailbox))" -count=1 -v`
- `go test ./internal/api/skills -run "TestListSessionAgent(ControlMailbox|EventsWithoutAgentReadsParentMailbox)|TestWaitSessionAgentsWithoutTargetUsesParentMailbox" -count=1 -v`
- `go test ./pkg/skillsapi -run "TestClient_SessionAgentEndpointsEncodePathsAndQuery" -count=1 -v`
- `go test ./internal/api/skills ./pkg/skillsapi ./cmd/skillsapi-demo -run "Test(ListSessionAgent(ControlMailbox|EventsWithoutAgentReadsParentMailbox)|WaitSessionAgentsWithoutTargetUsesParentMailbox|Client_SessionAgentEndpointsEncodePathsAndQuery|Run_SessionAgent(ControlMailbox|MailboxEvents))" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestLocalActorRegistry_(WaitWithoutTargetUsesParentMailbox|ReadEventsWithoutTarget(UsesParentMailbox|MergesAgentControlMailbox))" -count=1 -v`
- `go test ./internal/api/skills -run "TestSessionAgentController_(WaitWithoutTargetUsesParentMailbox|ReadEventsWithoutTarget(UsesParentMailbox|MergesAgentControlMailbox))" -count=1 -v`
- `go test ./internal/team -run "TestBuildTaskLifecycleMailboxMessageUsesAgentControlEnvelope" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestLocalChatRuntimeHost_(TeamLifecyclePersistsParentMailbox|TaskLifecyclePersistsTeammateMailbox)" -count=1 -v`
- `go test ./internal/api/skills -run "TestHandlerDeliverTeam(TaskLifecycleMailboxPersistsToTeammateSession|LifecycleMailboxPersistsToLeadSession)" -count=1 -v`
- `go test ./internal/agentcontrol ./internal/team -run "Test(TaskRecordNormalize|AgentControlTaskRecords|AgentProjection)" -count=1 -v`
- `go test ./internal/agentcontrol ./internal/team -run "Test(TaskRecordNormalize|AgentControlTaskRecordsProjectTeamTasks|AgentControlTaskRegistryUpdatesTaskStatus|AgentProjection)" -count=1 -v`
- `go test ./internal/agentcontrol ./internal/team ./internal/api/skills -run "Test(TaskRecordNormalize|AgentControlTaskRecordsProjectTeamTasks|AgentControlTaskRegistry(CreatesTask|UpdatesTaskStatus|ReleasesTask|RenewsTaskLease)|CreateTaskUsesAgentControlTaskCreateWriter|ReleaseTaskLeaseUsesAgentControlTaskReleaseWriter|RenewTaskLeaseUsesAgentControlTaskLeaseRenewWriter)" -count=1 -v`
- `go test ./internal/agentcontrol ./internal/team -run "Test(TaskRecordNormalize|TaskClaimRequestNormalize|AgentControlTaskRecordsProjectTeamTasks|AgentControlTaskRegistry(CreatesTask|UpdatesTaskStatus|ClaimsTask|ClaimsTaskWithPathClaims|ReleasesTask|RenewsTaskLease)|OrchestratorClaimReadyTasks)" -count=1 -v`
- `go test ./internal/agentcontrol ./internal/team -run "Test(TaskRecordNormalize|TaskClaimRequestNormalize|TaskTerminalUpdateRequestNormalize|AgentControlTaskRecordsProjectTeamTasks|AgentControlTaskRegistry(CreatesTask|UpdatesTaskStatus|ClaimsTask|ClaimsTaskWithPathClaims|UpdatesTerminalTask|ReleasesTask|RenewsTaskLease)|ApplyTerminalTaskOutcome|OrchestratorClaimReadyTasks)" -count=1 -v`
- `go test ./internal/agentcontrol ./internal/team -run "Test(TaskRecordNormalize|TaskClaimRequestNormalize|TaskTerminalUpdateRequestNormalize|TaskBlockRequestNormalize|AgentControlTaskRecordsProjectTeamTasks|AgentControlTaskRegistry(CreatesTask|UpdatesTaskStatus|ClaimsTask|ClaimsTaskWithPathClaims|UpdatesTerminalTask|BlocksTask|ReleasesTask|RenewsTaskLease)|Apply(Blocked|Terminal)TaskOutcome|OrchestratorClaimReadyTasks)" -count=1 -v`
- `go test ./internal/agentcontrol ./internal/team ./internal/toolbroker ./internal/api/skills -run "Test(TaskDependencyCreateRequestNormalize|AgentControlTaskRegistryCreatesTaskDependency|ApplyBlockedTaskOutcomeBlocksAndReplans|LeadPlannerReplanOnFailureIncludesTeamContextInPrompt|BrokerExecuteSpawnTeamCreatesTeamTeammatesAndTasks|CreateTaskUsesAgentControlTaskCreateWriter|AgentControlTaskWriteHandlersUseTaskRegistrySeams)" -count=1 -v`
- `go test ./internal/api/skills ./pkg/skillsapi ./internal/team ./internal/agentcontrol ./cmd/skillsapi-demo -run "Test(AgentControlTaskWriteHandlersUseTaskRegistrySeams|AgentControlTaskRegistryCreatesTaskDependency|AgentControlTaskRegistryListsTaskDependencies|AgentControlTaskRegistryListTaskGraphEventsUsesGlobalCursor|SQLiteStoreListTaskDependencyRecords|SQLiteStoreListTaskGraphEventsUsesGlobalCursor|TaskDependencyCreateRequestNormalize|TaskDependencyRecordAndFilterNormalize|TaskGraphEventAndFilterNormalize|Client_AgentControlTaskWriteEndpoints|Client_ListAgentControlTask(GraphEvents|Dependencies)|Run_AgentControl(Tasks|Dependencies|Events|AddDependency))" -count=1 -v`
- `go test ./cmd/skillsapi-demo -run "Test(ParseDemoOptions_AgentControlAddDependencyRequiresDependencyID|Run_AgentControl(Tasks|Dependencies|AddDependency))" -count=1 -v`
- `go test ./internal/toolbroker ./internal/api/skills ./internal/team -run "Test(BrokerExecuteReportTaskOutcome(HandlesHandoff|MarksTaskDone|MarksTeamDoneWhenLastTaskCompletes)|BrokerReportTaskOutcomePersistsTaskEventBeforeTeamCompleted|ReportTaskOutcomeHandler|Apply(Blocked|Terminal)TaskOutcome|OrchestratorExecuteAssignment)" -count=1 -v`
- `go test ./internal/api/skills -run "Test(ListAgentControlTasksHandlerProjectsTeamTasks|CreateTaskUsesAgentControlTaskCreateWriter|ReleaseTaskLeaseUsesAgentControlTaskReleaseWriter|RenewTaskLeaseUsesAgentControlTaskLeaseRenewWriter)" -count=1 -v`
- `go test ./pkg/skillsapi -run "TestClient_List(AgentControlTasks|TeamTasks)" -count=1 -v`
- `go test ./internal/agentcontrol ./internal/team -run "Test(SQLiteStoreTaskSignalsPersistSequenceAndWake|TaskRecordNormalize|TaskWake|AgentControlTaskRegistryWatchesTaskWake|OrchestratorRunUsesAgentControlTaskWakeSourceBeforeFallbackTick|OrchestratorRunWakesFromTaskSignalBeforeFallbackTick|OrchestratorClaimReadyTasks)" -count=1 -v`
- `go test ./internal/agentcontrol ./internal/team ./internal/api/skills ./pkg/skillsapi -run "Test(TaskRecordNormalize|TaskWake|AgentControlTaskRegistry(WatchesTaskWake|CreatesTask|UpdatesTaskStatus|ClaimsTask|RenewsTaskLease|ReleasesTask|UpdatesTerminalTask|BlocksTask)|OrchestratorRunUsesAgentControlTaskWakeSourceBeforeFallbackTick|AgentControlTask(WriteHandlersUseTaskRegistrySeams|BlockHandlerUsesTaskRegistrySeam)|ListAgentControlTasksHandlerProjectsTeamTasks|Client_(ListAgentControlTasks|AgentControlTaskWriteEndpoints))" -count=1 -v`
- `go test ./internal/agentcontrol ./internal/team -run "Test(Mailbox|TaskWake|AgentControlMailboxWakeWatchesTeamMailbox|OrchestratorRunUsesAgentControlMailboxWakeSourceBeforeFallbackTick|OrchestratorRunUsesAgentControlTaskWakeSourceBeforeFallbackTick|OrchestratorRunWakesFromMailboxSequenceBeforeFallbackTick|OrchestratorRunWakesFromTaskSignalBeforeFallbackTick)" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestLocalActorRegistry_ListIncludesTeamTeammateSessions|TestChatDebugAgentGraphLinesListsLocalAgents|TestChatAgentPickerPopupLinesIncludeAgentDetails" -count=1 -v`
- `go test ./internal/api/skills -run "TestSessionAgentController_ProjectsTeamTeammatesIntoAgentList" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestInterruptActiveRuns" -count=1 -v`
- `go test ./internal/team -run "TestApplyTerminalTaskOutcome|TestOrchestratorExecuteAssignment" -count=1 -v`
- `go test ./internal/toolbroker -run "TestBrokerReportTaskOutcomePersistsTaskEventBeforeTeamCompleted|TestBrokerExecuteSpawnTeamCreatesTeamTeammatesAndTasks" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestLocalChatRuntimeHost_DispatchTeamLifecycleEventUsesTeamLeadSession|TestAICLIChatActorExecutor_InteractiveAutoStartRendersTeamTimeline" -count=1 -v`
- `go test ./internal/api/skills -run "TestHandlerDeliverTeamLifecycleMailboxPersistsToLeadSession|TestSyncTeamLifecycleLoopsCompletesTeamWhenTeammateReportsOutcomeViaBroker" -count=1 -v`
- `go test ./internal/toolbroker ./cmd/aicli/commands ./internal/api/skills ./internal/chat ./internal/team ./pkg/skillsapi ./cmd/skillsapi-demo -run "WaitAgent|ReadAgentEvents|ReadEvents|Mailbox|SessionAgentEvents|SessionAgentMailboxEvents|SessionAgentEndpoints|DeliverMailbox|AppendMailbox|Collab|Timeline|ChatSlash|TriggerTask|TaskDispatch|TeammateRunner|AgentPicker|ChatAgentPicker|AgentsSend|AgentsTarget|TeamLifecycle|AgentMailboxMessage|FollowupTask" -count=1 -v`
- `go test ./cmd/aicli/commands ./internal/api/skills ./internal/toolbroker ./internal/chat -run "Test.*(MirrorsChildCompletion|PersistsCompletionMailbox|BuildSubagentCompletionMailbox|DeliverMailboxStoreFirstPrefersAgentControlWriterForEnvelope|ListMailboxAgentControlFirst|WatchMailboxAgentControlFirst|WaitWithoutTarget|ReadEventsWithoutTarget)" -count=1 -v`
- `go test ./internal/chat -run "Test(SQLiteRuntimeStoreMigratesAgentControlMailboxSequence|SQLiteRuntimeStoreMigratesSessionMailboxTable|SQLiteRuntimeStoreAppendMailboxMirrorsAgentControlEnvelope|InMemoryRuntimeStoreAgentControlMailboxFiltersEnvelope|ListMailboxAgentControlFirst|WatchMailboxAgentControlFirst)" -count=1 -v`
- `go test ./cmd/aicli/commands ./internal/api/skills ./pkg/skillsapi ./cmd/skillsapi-demo -run "WaitWithoutTarget|ReadEventsWithoutTarget|ListSessionAgent(ControlMailbox|EventsWithoutAgentReadsParentMailbox)|WaitSessionAgentsWithoutTarget|SessionAgentEndpointsEncodePathsAndQuery|Run_SessionAgent(ControlMailbox|MailboxEvents)|Client_SessionAgentEndpointsEncodePathsAndQuery|Collab" -count=1 -v`
- `go test ./internal/toolbroker ./cmd/aicli/commands ./internal/api/skills ./internal/chat ./internal/team ./pkg/skillsapi ./cmd/skillsapi-demo -run "AgentControl|Mailbox|WaitAgent|ReadAgentEvents|ReadEvents|Collab|Timeline|TeamLifecycle|TaskDispatch|AgentPicker|AgentsTarget|AgentsSend|FollowupTask|MirrorsChildCompletion|PersistsCompletionMailbox|InterruptActiveRuns" -count=1`
- `go test ./internal/toolbroker ./internal/team -run "Test(BrokerExecuteSpawnTeam|BrokerReportTaskOutcome|BrokerExecuteReportTaskOutcome|AgentControlTaskRegistry|TaskRecordNormalize|TaskWake|OrchestratorClaimReadyTasks|Apply(Blocked|Terminal)TaskOutcome)" -count=1 -v`
- `go test ./internal/team -run "TestApplyBlockedTaskOutcome|TestLeadPlannerReplanOnFailure" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestLocalActorRegistry_Dispatch(BlockedHandoffMailboxPersistsAgentControlRow|TeamMailboxMessagePersistsWithoutActor|TeamMailboxMessageRoutesToActor)|TestLocalActorRegistry_TriggerTaskUsesSessionHub" -count=1 -v`
- `git diff --check`
- `go test ./...`
- 使用 `mimo_anthropic / mimo-v2.5-pro` 真实非交互调用验证两个 `spawn_agent` 并行、`list_agents`、`wait_agent` 全链路可用。可追溯证据: 父会话 `C:\Users\vince\.aicli\sessions\session_20260506152749_5v4KTsQK.json` 同一 assistant turn 同时调用 `spawn_agent` 创建 `live2-agent-a` 与 `live2-agent-b`；随后 `list_agents` 返回两个 child 均为 `running`；`wait_agent ids=[live2-agent-a, live2-agent-b]` 先返回 `live2-agent-b` ready，再单独等待 `live2-agent-a` ready；最终 `list_agents` 返回两个 child 均为 `idle`。子会话证据为 `C:\Users\vince\.aicli\sessions\live2-agent-a.json` 与 `C:\Users\vince\.aicli\sessions\live2-agent-b.json`，两者 metadata 均记录 `agent_parent_session_id=session_20260506152749_5v4KTsQK`、`agent_path=/root/live2-agent-*`，并使用 `mimo_anthropic` 完成各自文档读取任务。
- `go test ./cmd/aicli/commands -run TestAICLIChatActorExecutor_InterruptStopsSlowAutoStartTeam -count=1 -v`
- 新增确定性慢任务中断回归: `TestAICLIChatActorExecutor_InterruptStopsSlowAutoStartTeam` 使用 `spawn_team auto_start=true` 创建后台 team，让 teammate provider 阻塞到 ctx cancellation；测试确认 `session.Interrupt()` 后 provider 收到取消，team 保持 `paused`，task 保持 `cancelled`，lead runtime state 变为 `stopped`，并且不会追加 `task.completed/task.failed/team.completed/team.summary` 或对应 terminal timeline 行。这覆盖了“慢 provider 响应期间 Ctrl+C 后子 agent 不应继续刷完成输出”的核心本地可复现场景。
- `go test ./cmd/aicli/commands -run "TestSignalHandlerFirstInterruptCancelsActiveTeamRuns|TestInterruptActiveRunsPausesTeamAndStopsRuntimeStates|TestAICLIChatActorExecutor_InterruptStopsSlowAutoStartTeam" -count=1 -v`
- 新增 Ctrl+C signal handler 入口回归: `TestSignalHandlerFirstInterruptCancelsActiveTeamRuns` 不直接调用 `session.Interrupt()`，而是通过 `setupSignalHandler` 接收 `os.Interrupt`，验证第一次 Ctrl+C 会异步清理 active team: task 进入 `cancelled`、assignee/lease 被释放、team 进入 `paused`、lead/teammate runtime state 进入 `stopped`，且第一次 signal 不触发 chat loop exit；第二次 Ctrl+C 才设置 `shouldExit`。这补上了从终端信号入口到 active team cleanup 的确定性覆盖。
- 新增 Ctrl+C cancel durable lifecycle 覆盖: `cancelActiveTeamTasks` 通过 `ReleaseAgentControlTask` 将 active task 标记为 `cancelled` 后，现在会追加 `task.cancelled` team event，并向原 assignee teammate session 投递 `agent_control.task_lifecycle` / `team.task_lifecycle` mailbox，metadata 包含 `event_type=task.cancelled`、`task_id`、`assignee`、`status=cancelled`、`reason=user_interrupt`。`TestInterruptActiveRunsPausesTeamAndStopsRuntimeStates` 已验证 team event 与 child session `ListAgentControlMailbox` 均能看到该取消终态。
- 新增 HTTP cancel durable lifecycle 覆盖: legacy `POST /api/runtime/teams/{id}/tasks/{task_id}/release`、显式 `POST /api/runtime/agent-control/tasks/{task_id}/release`、显式 `POST /api/runtime/agent-control/tasks/{task_id}/status`、legacy `PATCH /api/runtime/teams/{id}/tasks/{task_id}` 与显式 `PATCH /api/runtime/agent-control/tasks/{task_id}` 在 `status=cancelled` 时都会追加 `task.cancelled` team event，并向原 assignee teammate session 投递 `agent_control.task_lifecycle` mailbox，metadata 与 CLI Ctrl+C 取消路径保持一致；新增 `TestReleaseTaskCancelledPersistsAgentControlLifecycleMailbox` 覆盖这些 HTTP 入口。
- 新增 AgentControl cancelled status/update 释放语义: `UpdateAgentControlTaskStatus` 收到 `status=cancelled` 时改为通过 release 语义释放 assignee/lease；`UpdateAgentControlTask` full patch 即使同时携带 assignee，只要 status 是 `cancelled`，最终也会清空 assignee/lease，避免出现 cancelled task 仍被 teammate 占用。新增 `TestAgentControlTaskRegistryCancelledUpdatesReleaseLease` 覆盖 status writer 与 full update writer 两条路径。
- `go test ./cmd/aicli/commands -run TestAICLIChatActorExecutor_AutoStartTeamHandlesTeammateProviderStreamError -count=1 -v`
- 新增确定性 provider stream error 回归: `TestAICLIChatActorExecutor_AutoStartTeamHandlesTeammateProviderStreamError` 使用 `spawn_team auto_start=true` 创建后台 team，让 teammate provider 直接返回 `failed to handle stream response: stream error: ... INTERNAL_ERROR ...`。测试确认 `waitForTeamTerminal` 不挂死，task 进入 `failed` 且 summary 保留 `stream error`，team 进入 `failed`，持久 timeline 有 `task.failed/team.completed status=failed`，且不会生成 `team.summary`。
- `go test ./internal/team ./internal/agentcontrol -run "Test(SQLiteStoreAgentControlTaskRecordsMirrorTaskLifecycle|AgentControlTaskRecordsProjectTeamTasks|AgentControlTaskRegistry|TaskRecordNormalize|TaskWake|OrchestratorClaimReadyTasks)" -count=1 -v`
- `go test ./internal/api/skills ./pkg/skillsapi ./internal/team ./internal/agentcontrol ./cmd/skillsapi-demo -run "Test(AgentControlTaskWriteHandlersUseTaskRegistrySeams|AgentControlTaskRegistry(CreatesTask|UpdatesTaskStatus|ClaimsTask|ReleasesTask|RenewsTaskLease|UpdatesTerminalTask|BlocksTask|ListsTaskDependencies|ListTaskGraphEventsUsesGlobalCursor)|SQLiteStoreAgentControlTaskRecordsMirrorTaskLifecycle|TaskRecordNormalize|Client_(AgentControlTaskWriteEndpoints|ListAgentControlTasks|ListAgentControlTask(GraphEvents|Dependencies))|Run_AgentControl(Tasks|Dependencies|Events|AddDependency))" -count=1 -v`
- 新增 AgentControl task read-model mirror: SQLite migration v12 增加 `agent_control_task_records`，task create/update/status/ready/claim/lease/release/block 和 teammate session upsert 会同步维护 mirror row；`ListAgentControlTasks` 优先读 mirror，测试覆盖 create -> ready -> running -> teammate session refresh -> done summary 的镜像生命周期。
- `go test ./internal/team ./internal/agentcontrol -run "Test(SQLiteStoreListTask(Dependents|DependencyRecords|GraphEventsUsesGlobalCursor)|AgentControlTaskRegistry(ListsTaskDependencies|ListTaskGraphEventsUsesGlobalCursor|CreatesTaskDependency)|TaskDependency|TaskGraphEvent)" -count=1 -v`
- `go test ./internal/api/skills ./pkg/skillsapi ./internal/team ./internal/agentcontrol ./cmd/skillsapi-demo -run "Test(AgentControlTaskWriteHandlersUseTaskRegistrySeams|AgentControlTaskRegistry(CreatesTaskDependency|ListsTaskDependencies|ListTaskGraphEventsUsesGlobalCursor)|SQLiteStoreListTaskDependencyRecords|SQLiteStoreListTaskGraphEventsUsesGlobalCursor|TaskDependency(CreateRequestNormalize|RecordAndFilterNormalize)|TaskGraphEventAndFilterNormalize|Client_ListAgentControlTask(GraphEvents|Dependencies)|Run_AgentControl(Dependencies|Events|AddDependency))" -count=1 -v`
- 新增 AgentControl dependency mirror: SQLite migration v13 增加 `agent_control_task_dependencies`，`AddTaskDependency` 同步维护 mirror row，重复 edge 不重复写；`ListTaskDependencyRecords` 现在从该 mirror 读取 edge `id/created_at`，测试覆盖 dependency、dependent、graph event cursor 和 API/client/demo 读取。
- `go test ./internal/team -run "Test(AgentControlTaskRegistry(ReleasesTask|RetriesTask|RenewsTaskLease)|LeaseManagerReclaimExpiredTasks)" -count=1 -v`
- `go test ./internal/api/skills -run "Test(RetryTaskUsesAgentControlTaskRetryWriter|ReleaseTaskLeaseUsesAgentControlTaskReleaseWriter|RenewTaskLeaseUsesAgentControlTaskLeaseRenewWriter|AgentControlTaskWriteHandlersUseTaskRegistrySeams)" -count=1 -v`
- `go test ./cmd/aicli/commands -run "TestAICLIChatActorExecutor_InterruptStopsSlowAutoStartTeam|TestInterruptActiveRuns" -count=1 -v`
- 新增 AgentControl retry/reclaim/cancel writer seam 覆盖: `TaskRegistryRetryWriter` / `RetryAgentControlTask` 统一 retry count、lease release、assignee cleanup 和可选 summary；API retry、offline reclaim、lease reclaim/renew 与 CLI Ctrl+C cancel active team tasks 已走 AgentControl retry/release/lease seam。新增 `TestAgentControlTaskRegistryRetriesTask` 覆盖 retry seam，既有中断慢任务测试也覆盖 interrupt cancel 经 release seam 后仍稳定收敛为 `cancelled`。
- `go test ./internal/team -run "TestAgentControlTaskRegistry(UpdatesTask|CreatesTask|RetriesTask|ReleasesTask|RenewsTaskLease)" -count=1 -v`
- `go test ./internal/api/skills -run "Test(UpdateAgentControlTaskEndpointUsesTaskUpdateWriter|UpdateTaskUsesAgentControlTaskUpdateWriter|CreateTaskUsesAgentControlTaskCreateWriter|RetryTaskUsesAgentControlTaskRetryWriter|ReleaseTaskLeaseUsesAgentControlTaskReleaseWriter|RenewTaskLeaseUsesAgentControlTaskLeaseRenewWriter)" -count=1 -v`
- `go test ./pkg/skillsapi -run "TestClient_AgentControlTaskWriteEndpoints" -count=1 -v`
- `go test ./cmd/skillsapi-demo -run "TestRun_AgentControl(Tasks|Update|Dependencies|Events|AddDependency)" -count=1 -v`
- 新增 AgentControl full task update writer seam 覆盖: `TaskRegistryUpdateWriter` / `UpdateAgentControlTask` 统一处理完整 task patch；`PATCH /api/runtime/teams/{id}/tasks/{task_id}` 已从 handler 直接 `store.UpdateTask` 迁到 update writer seam，显式 `PATCH /api/runtime/agent-control/tasks/{task_id}`、typed client `UpdateAgentControlTask` 和 `skillsapi-demo -control-action update` 也已接入。新增 `TestAgentControlTaskRegistryUpdatesTask`、`TestUpdateTaskUsesAgentControlTaskUpdateWriter`、`TestUpdateAgentControlTaskEndpointUsesTaskUpdateWriter`、`TestClient_AgentControlTaskWriteEndpoints` 与 `TestRun_AgentControlUpdate` 覆盖 registry、legacy HTTP handler、AgentControl HTTP endpoint、client 和 demo 五层。
- `go test ./cmd/aicli/commands -run "TestHandleCommand_Collab(AllAggregatesParentAndAgentMailboxes|PrintsParentMailboxTimeline|PrintsSelectedAgentMailboxTimeline)|TestChatCollabLinesListsParentMailboxEvents" -count=1 -v`
- 新增 `/collab all [limit] [filter=<text>]` 聚合视图、行级筛选和 bounded follow: CLI 现在可以在一个输出中按 session 分组显示 parent mailbox 与 ActorRegistry 投影出的所有 child/team teammate mailbox，输出每个目标的 path/session/status，并复用现有 AgentControl-first mailbox substrate 与非 mailbox 协作终态补充逻辑；`filter=` / `match=` 会保留分组头并过滤具体事件行，便于在多 agent mailbox 中定位某类正文或 envelope 字段；显式 `/collab follow ... timeout=...` 会订阅 AgentControl-first mailbox watcher，等待下一次 mailbox update 后输出 `follow=update` 并刷新 snapshot。新增 `TestHandleCommand_CollabAllAggregatesParentAndAgentMailboxes` 覆盖 parent + child agent mailbox 聚合、命令标题、`filter=body=...` 行级筛选和 follow 更新刷新。
- 新增 `/agents target` 候选目标视图与 `/timeline filter=<text>`: 无参数 `/agents target` 现在会列出当前可用 agent targets，并用 `*` 标出当前 selected target；`/timeline` 与 `/collab` 对齐支持 `filter=` / `match=` 行级过滤，保留 team header 但只显示匹配事件行，便于在多 task/team event 中快速定位某个 task、assignee、trace 或 AgentControl dispatch via。新增 `TestChatAgentTargetLinesListsAvailableTargets`、`TestChatTimelineCommandLinesFiltersEventRows` 与 `TestChatSlashCommandCatalogTimelineIncludesFilterArg` 覆盖 target 候选展示、timeline 行级过滤和 slash catalog 文案。
- `go test ./internal/team ./internal/api/skills -run "TestAgentControlTaskRegistry(TerminalStatusUpdatesReleaseLease|CancelledUpdatesReleaseLease)|TestUpdateTaskTerminalStatusesReleaseLease" -count=1 -v`
- 修复 AgentControl 通用 status/update 终态泄漏: `UpdateAgentControlTaskStatus` 与 `UpdateAgentControlTask` 现在把 `done/failed/cancelled` 统一视为 closed task status。无论调用 legacy `PATCH /teams/{id}/tasks/{task_id}`、显式 `PATCH /agent-control/tasks/{task_id}`，还是 AgentControl status writer，只要把任务置为终态，都会清空 `assignee/lease_until`，防止终态任务继续被 teammate 视为占用。新增 registry 与 HTTP 测试覆盖 `done/failed/cancelled` 三类路径。
- `go test ./internal/agentcontrol ./internal/team ./internal/api/skills -run "TestTaskReadyRequestNormalizeTrimsFields|TestAgentControlTaskRegistryMarksReadyTasks|TestOrchestratorClaimReadyTasksHonorsMaxWriters|TestMarkReadyTasksUsesAgentControlReadyWriter" -count=1 -v`
- 新增 AgentControl ready promotion writer seam: `agentcontrol.TaskReadyRequest` / `TaskRegistryReadyWriter` / `team.AgentControlTaskRegistry.MarkAgentControlTasksReady` 已把 pending -> ready 的依赖满足推进收口到统一 task registry seam；`Orchestrator.ClaimReadyTasks`、`tick` 和 Runtime API `POST /api/runtime/teams/{id}/tasks/ready` 不再直接调用 `Store.MarkReadyTasks`，而是通过 AgentControl ready writer 进入当前 adapter。这样 ready promotion、claim、terminal、blocked、retry/release/lease 等核心 orchestration 状态迁移均已走 AgentControl-shaped 写入口，剩余差距进一步收敛到实体权威表和少量兼容读写面。
- `go test ./internal/team -run "TestAgentControlTaskRegistry(CreatesTask|UpdatesTask|CreatesTaskDependency|MarksReadyTasks|TerminalStatusUpdatesReleaseLease|CancelledUpdatesReleaseLease)|TestSQLiteStoreAgentControlTaskRecordsMirrorTaskLifecycle|TestSQLiteStoreListTaskDependencyRecords" -count=1 -v`
- 完成 AgentControl task graph 主写入迁移: SQLite migration v14 扩展 `agent_control_task_records`，补齐 `goal/inputs/read_paths/write_paths/deliverables/lease_until/retry_count/result_ref/version` 等完整 task 字段；当前 `team.AgentControlTaskRegistry` 与 legacy Store task 方法的 create/update/dependency/ready/status/retry/claim/lease/release/terminal/block 写路径都直接写 `agent_control_task_records` / `agent_control_task_dependencies`。legacy `team_tasks` / `team_task_dependencies` 不再作为同步镜像写入，只作为历史 migration 回填输入，并在 migration v19 删除。
- `go test ./internal/team -count=1`
- 完成 legacy read 降级: `SQLiteStore.GetTask` / `ListTasks` 已直接读取 `agent_control_task_records`，不再回退 legacy `team_tasks`。`ListTaskDependencies` / `ListTaskDependents` / `ListTaskDependencyRecords` 也读取 `agent_control_task_dependencies`。`reconcileTerminalTeamStateSQLite` 的 task status 统计已改为在同一事务内统计 AgentControl task 表，避免 terminal 判定依赖被删除的 legacy 表。新增 `TestSQLiteStoreGetAndListTasksUseAgentControlRecordsWithoutLegacyTaskMirror` 与 dependency 测试确认旧 task/dependency 表不存在时公开读写仍可工作。
- `go test ./internal/team -run "TestSQLiteStoreClaimTaskPrefersAgentControlRecords|TestAgentControlTaskRegistryUpdatesTerminalTask|TestAgentControlTaskRegistryClaimsTask|TestSQLiteStoreClaimTaskWithPathClaims" -count=1 -v`
- `go test ./internal/team -count=1`
- 完成 legacy Store task 写路径停写: SQLite `CreateTask` / `UpdateTask` / `UpdateTaskStatus` / `IncrementTaskRetry` / `MarkReadyTasks` / `ClaimTask` / `ClaimTaskWithPathClaims` / `RenewTaskLease` / `ReleaseTask` / `BlockTask` / `AddTaskDependency` 现在只写 `agent_control_task_records` 或 `agent_control_task_dependencies`，不再同步 legacy `team_tasks` / `team_task_dependencies` mirror；普通 claim 与 path-claim 以 AgentControl row 的 status/version 作为乐观锁来源。terminal SQLite 快路径也只更新 AgentControl task 表。新增 `TestSQLiteStoreClaimTaskDoesNotRequireLegacyTaskMirror` 确认旧 task 表不存在时 claim 仍按 AgentControl version 成功。
- `go test ./internal/chat -run "Test(InMemoryRuntimeStoreAgentControlMailboxFiltersEnvelope|SQLiteRuntimeStoreAppendMailboxMirrorsAgentControlEnvelope|SQLiteRuntimeStoreAppendAgentControlMailboxPrefersControlRows|WatchMailboxAgentControlFirst|ListMailboxAgentControlFirst)" -count=1 -v`
- `go test ./internal/chat -count=1`
- `go test ./...`
- 推进 session/runtime mailbox 主写入与独立 watch: `SQLiteRuntimeStore.AppendMailbox` / `AppendAgentControlMailbox` 现在都先写 `agent_control_mailbox_records(scope=session)`；旧 scoped `agent_control_mailbox_messages` 与 legacy `session_mailbox_messages` 已在迁移中回填后删除，当前仅保留 `mailbox_received` session event 作为展示兼容镜像。普通 `ListMailbox/LastMailboxSeq` 从统一 record 的 `session_mailbox_seq` 投影；`ListAgentControlMailbox/LastAgentControlMailboxSeq` 从同一表过滤 control rows 并使用 record `id` 作为 cursor。`WatchAgentControlMailbox` 也改为独立 control watcher，由 control row 写入直接唤醒，不再依赖普通 mailbox watcher 后二次读取。新增测试确认旧物理表不存在时，普通 mailbox、AgentControl mailbox、迁移回填和 control watcher 仍正常工作。
- 推进 team mailbox 实体 AgentControl-first: SQLite migration v15 新增历史 `agent_control_mailbox_messages` 并从 legacy `team_mailbox_messages` 回填；migration v17 新增统一形态 `agent_control_mailbox_records`；migration v18 删除 `agent_control_mailbox_messages` 与 `team_mailbox_messages`。`SQLiteStore.InsertMail/ListMail/LastMailSeq/RecordMailReceipt` 现在只写/读/更新 `agent_control_mailbox_records`，旧 scoped 表和 legacy 表不再是公开 mailbox 读取、sequence 分配和 ack 更新的权威来源。新增 `TestSQLiteStoreListMailPrefersAgentControlMessages` 与 `TestSQLiteStoreRecordMailReceiptDoesNotRequireMirrorRows` 覆盖旧物理表不存在时公开 `ListMail`、ack、receipt 和 unread 视图仍返回统一 AgentControl mailbox record。
- `go test ./internal/team -run "TestSQLiteStore(ListMail|ListMailAfterSeq|ListMailPrefersAgentControlMessages)|TestMailboxServiceWaitUsesDurableSequenceAndWake|TestAgentControlMailboxWakeWatchesTeamMailbox|TestMailboxReceipts" -count=1 -v`
- `go test ./internal/team -count=1`
- `go test ./internal/api/skills ./cmd/aicli/commands ./internal/chat -run "Mailbox|Collab|AgentControlMailbox|TeamMailbox|OrchestratorRunUsesAgentControlMailboxWakeSource" -count=1`
- 全量回归过程中发现 `SessionManagerConfig.TTL` 只驱动后台 cleanup，新建 session 没有设置 `ExpiresAt`，导致 `GetSession` 对短 TTL 的即时过期判断依赖 cleanup goroutine 调度并偶发返回 nil error。`SessionManager.Create` 现在会在正数 TTL 配置下为新 session 设置 TTL，`TestSessionTTLWithLLMExecution` 与 `go test ./internal/chat -count=1` 已覆盖。
- 推进统一 AgentControl wake registry: 新增 `agentcontrol.WakeFilter/WakeEvent/WakeSource` 和 SQLite `agent_control_wake_events`。team mailbox wake 与 task wake 现在只写统一 wake registry；migration v18 删除 legacy `agent_control_mailbox_wake_messages` / `agent_control_task_wake_signals`。`team.AgentControlMailboxWake` 与 `team.AgentControlTaskRegistry` 优先从统一 registry watch/read high-water mark。`TestAgentControlMailboxWakeWatchesTeamMailbox` 和 `TestAgentControlTaskRegistryWatchesTaskWake` 已覆盖旧 wake mirror table 不存在时 high-water mark 仍可读取。
- `go test ./internal/agentcontrol ./internal/team -run "TestAgentControl(TaskRegistryWatchesTaskWake|MailboxWakeWatchesTeamMailbox)|TestSQLiteStoreTaskSignalsPersistSequenceAndWake|TestOrchestratorRunUsesAgentControl(Mailbox|Task)WakeSourceBeforeFallbackTick" -count=1 -v`
- 推进共享 AgentControl mailbox read model、统一物理表形态和 global read substrate: 新增 `agentcontrol.MailboxRecordFilter/MailboxRecord` 与 registry reader/sequencer seam。runtime SQLite store 和 team SQLite store 都已新增同名同结构 `agent_control_mailbox_records`，分别以 `scope=session` 与 `scope=team` 作为 mailbox 主记录；旧 scoped `agent_control_mailbox_messages`、legacy `session_mailbox_messages`、legacy `team_mailbox_messages` 均已迁移回填后删除。runtime InMemory store 继续提供等价 read model。新增 `agentcontrol.CombinedMailboxRegistry`、`agentcontrol.GlobalMailboxRegistry`、`GET /api/runtime/agent-control/mailbox`、typed client `ListAgentControlMailbox` 和 `skillsapi-demo -mode agent-control -control-action mailbox`，把 runtime/session 与 team mailbox registry 合并为一个命名的 in-process global read substrate；`chat.NewAgentControlMailboxRegistry` 与 `team.NewAgentControlMailboxRegistry` 已提供非 SQLite fallback projection。`TestSQLiteRuntimeStoreAppendAgentControlMailboxPrefersControlRows`、`TestSQLiteStoreListMailPrefersAgentControlMessages`、`TestCombinedMailboxRegistryDoesNotSkipNewLowerSourceRows`、`TestGlobalMailboxRegistryExposesNamedSources`、`TestListAgentControlMailboxCombinesRuntimeAndTeamRegistries`、`TestClient_ListAgentControlMailbox` 和 `TestRun_AgentControlMailbox` 已覆盖 shared registry record 不受旧表缺失影响、combined cursor 不跳过低 source 新行、命名 source 诊断、HTTP/client/demo 可读组合 registry。
- 推进 durable global AgentControl mailbox registry: 新增 `agentcontrol.SQLiteGlobalMailboxRegistryStore` 与 `agent_control_global_mailbox_records`，用 `(source, source_scope, source_id, source_seq)` 做幂等 materialization key，并提供单一 durable `id` 作为 global mailbox cursor。`GlobalMailboxRegistry` 现在支持 durable source 优先读取；未配置 durable store 时仍回退到 runtime/team local source 的组合读。Runtime config 新增 `agentControl.mailboxStorePath` / `agentControl.mailboxStoreDSN`，Skills API `GET /api/runtime/agent-control/mailbox` 在配置 durable store 后会先把 runtime/team source materialize 到 global 表，再按 durable global cursor 返回，响应 `sources` 会显示 `global` 与本地 source。`GlobalMailboxWriter` write-through seam 已接入 `SQLiteRuntimeStore`、`InMemoryRuntimeStore` 与 `SQLiteStore`：本地 control mailbox 主写入成功后，会把 runtime/session source 或 team source 幂等写入 global registry；跨 store write-through 失败不会回滚本地主写入，API materialization 仍作为漏写修复和非写接入 store 的兜底路径。`SQLiteGlobalMailboxRegistryStore` 现在同时实现 `MailboxWakeSource`：真正插入新 global row 时按 global `id` 发 wake，重复 materialize/refresh 同一 source row 只更新投影、不重复唤醒；`LastAgentControlMailboxWakeSeq` 也直接读取 global 表 high-water mark。Skills API handler 和本地 `aicli chat` runtime host 都会在配置或默认落地 `agentControl.mailboxStorePath` 后把 global store 挂到 runtime/team store，并把 team orchestrator 的 mailbox wake source 切到该 global store；本地 chat 默认路径为 session runtime 目录下的 `agent_control_mailbox.sqlite`。新增 `TestSQLiteGlobalMailboxRegistryStoreAppendAndList`、`TestSQLiteGlobalMailboxRegistryStoreIdempotentSourceRows`、`TestSQLiteGlobalMailboxRegistryMaterializeFromSources`、`TestSQLiteGlobalMailboxRegistryStoreWatchesMailboxWake`、`TestSQLiteGlobalMailboxRegistryStoreDoesNotWakeOnIdempotentRefresh`、`TestSQLiteGlobalMailboxRegistryStoreWakeHonorsSessionFilter`、`TestGlobalMailboxRegistryPrefersDurableSource`、`TestListAgentControlMailboxUsesDurableGlobalRegistryWhenConfigured`、`TestGetTeamOrchestratorUsesGlobalMailboxWakeStore`、`TestOrchestratorRunUsesGlobalMailboxWakeBeforeFallbackTick`、`TestSQLiteRuntimeStoreAppendAgentControlMailboxWritesGlobalRegistry`、`TestInMemoryRuntimeStoreAppendAgentControlMailboxWritesGlobalRegistry`、`TestSQLiteStoreInsertMailWritesGlobalRegistry`、`TestConfigureLocalChatMailboxWriteThroughWritesRuntimeAndTeamRows` 与 `TestValidateAgentControlConfig` 覆盖 durable store、幂等回填、durable 优先读取、global wake/watch、write-through、API/CLI 接入和配置互斥校验。
- 推进 local mailbox projection backlink: runtime/team 本地 `agent_control_mailbox_records` 已新增 `global_seq` 字段，用来记录对应的 durable global mailbox row id。`SQLiteRuntimeStore`、`InMemoryRuntimeStore` 与 `SQLiteStore` 在 global registry 写入成功后会回写/保存该 `global_seq`，`ListMailbox`、`ListAgentControlMailbox`、`ListAgentControlMailboxRecords` 和 team `ListMail` 都会把该字段投影到 `MailMessage.GlobalSeq` 或 `MailboxRecord.GlobalSeq`。这一步还不是完整的“global 事务内唯一主写入”，但它消除了本地 row 与 global row 的身份断裂，是后续把 local mailbox 表降级为 projection/cache 的必要前置。`TestSQLiteRuntimeStoreAppendAgentControlMailboxWritesGlobalRegistry`、`TestInMemoryRuntimeStoreAppendAgentControlMailboxWritesGlobalRegistry` 与 `TestSQLiteStoreInsertMailWritesGlobalRegistry` 已补充覆盖本地 projection backlink。
- 继续推进 global-primary canonical mailbox 写入口: 新增 `agentcontrol.GlobalMailboxPrimaryWriter` 与 `SQLiteGlobalMailboxRegistryStore.AppendPrimaryGlobalMailboxRecord`，配置 durable global registry 后，runtime `AppendAgentControlMailbox` 与 team `InsertMail` 会先写 `agent_control_global_mailbox_records(source=global)`，再写本地 `agent_control_mailbox_records(scope=session/team)` 兼容 projection，并把 global row id 写入本地 `global_seq`；未配置 durable global registry 时仍保留 local-first fallback。`MaterializeMailboxRecords` 现在会跳过已经带 `GlobalSeq` 的本地 projection，避免 global-primary row 被后续 materialization 再复制成 runtime/team source duplicate。新增 `TestSQLiteGlobalMailboxRegistryStoreAppendPrimaryAssignsGlobalSeq`、`TestSQLiteGlobalMailboxRegistryStoreAppendPrimaryIsIdempotent`、`TestSQLiteGlobalMailboxRegistryMaterializeSkipsProjectedRows`，并更新 `TestSQLiteRuntimeStoreAppendAgentControlMailboxWritesGlobalRegistry`、`TestInMemoryRuntimeStoreAppendAgentControlMailboxWritesGlobalRegistry` 与 `TestSQLiteStoreInsertMailWritesGlobalRegistry` 验证 canonical 写入口已从 write-through 小步推进为 global-primary + local projection；普通 control envelope `AppendMailbox` 与 projection repair/auto reconcile 已由后续条目补齐。
- 继续推进普通 control envelope mailbox 写入口: runtime `AppendMailbox` 在消息携带 AgentControl envelope 时也会走 global-primary，再写本地 session mailbox projection；普通无 envelope mailbox 保持本地写入。新增 `TestSQLiteRuntimeStoreAppendMailboxControlEnvelopeUsesGlobalPrimary` 验证 `AppendMailbox` 的 control envelope 不再通过 runtime source write-through 复制到 global，而是直接形成 `source=global` 的 durable row，并回填本地 `global_seq`。
- 补齐 projection repair 双向兜底与自动 reconcile: local-to-global `RepairAgentControlMailboxProjection` 已能把本地 `global_seq=0` 行 materialize 到 durable global registry 并回写 backlink；新增 global-to-local `RepairAgentControlMailboxLocalProjection`，可从 durable global registry 扫描缺失的 session/team mailbox row 并重建本地 `agent_control_mailbox_records(scope=session/team)` projection。新增 `agentcontrol.ReconcileMailboxProjections` 统一执行双向 repair，并在 Skills API `SetAgentControlMailboxStore` / 自动 store reload、本地 `aicli chat` global mailbox store 注入点后台触发一次 reconcile。新增 `TestSQLiteRuntimeStoreRepairsMailboxLocalProjectionFromGlobal`、`TestSQLiteStoreRepairsMailboxLocalProjectionFromGlobal`、`TestConfigureLocalChatMailboxWriteThroughReconcilesExistingProjections` 与 `TestSetAgentControlMailboxStoreReconcilesExistingProjections` 覆盖 global-primary 成功但 local projection 缺失、以及配置 global store 后已有 local-only/global-only row 通过本地 chat 与 Skills API handler 自动收敛。剩余尾项收窄为跨进程单一 AgentControl registry service/table 和完整富交互 TUI。
- 推进 AgentRegistry identity graph 主表与 registry-first 写路径: 新增 storage-neutral `agentcontrol.AgentRecord`、`AgentFilter`、`AgentRegistryReader`、`AgentRegistryWriter`、`AgentRegistryStore` 与 `AgentSpawnReservationStore`，把 agent 身份图从 chat session context projection 中抽出独立契约；新增 `SQLiteGlobalAgentRegistryStore` 与 durable `agent_control_agents` 表，字段覆盖 `agent_id/root_session_id/parent_agent_id/parent_session_id/session_id/agent_path/depth/agent_type/nickname/workflow/team_id/teammate_id/status/created_at/updated_at/closed_at`，并建立 `UNIQUE(root_session_id, agent_path)`、active session 唯一绑定、parent 和 team 查询索引。SQLite registry 已支持原子 `ReserveAgentControlAgentSpawn`，在同一事务中 upsert root、检查 active child 数量、写入 child row，从而把跨进程 spawn limit 从 session scan 推进到 durable registry reservation。
- Runtime API `sessionAgentController` 与本地 CLI `localActorRegistry` 均已接入 registry-first: `spawn_agent` 保存 session 后写入/预约 AgentRegistry，失败会回滚刚创建的 child session；`list_agents` 优先读取 durable registry，registry 缺数据时才回退 session/team projection；path target、path subtree close、`max_threads` 统计和 team teammate identity 也优先走 `agent_control_agents`。`GET /api/runtime/agent-control/agents` 在配置 durable store 时会先 materialize session/team 投影再读主表，无 durable store 时返回 `source=agent_control_projection` 的兼容投影；materialize 遵守 durable closed 状态，不会把另一个进程已经关闭的 row 重新打开。新增/更新测试覆盖 durable identity row、path 唯一性、active session 唯一绑定、subtree close、IncludeClosed 读取、HTTP 投影、atomic reservation、API registry-first spawn/list/close、team teammate materialization、本地 CLI registry 写入/关闭、durable registry max_threads 和 closed row 不复活。
- AgentRegistry wake 已补上最小 durable watch/sequence: 新增 `AgentWakeFilter`、`AgentWakeEvent` 与 `AgentWakeSource`，SQLite `agent_control_agent_wake_events` 记录 upsert/spawn reservation/close 事件并提供单调递增 id；`WatchAgentControlAgentWake` 提供进程内订阅，`LastAgentControlAgentWakeSeq` 提供 durable high-water mark。新增 `TestSQLiteGlobalAgentRegistryStoreWatchesAgentWake` 与 `TestSQLiteGlobalAgentRegistryStoreAgentWakeSequenceAndClose` 覆盖 path/root 过滤、spawn reservation wake、close wake 和 close 后 sequence 递增。
- 补齐 API team teammate 即时写入与 canonical identity 收敛: `UpsertTeammate` / `UpdateTeammate` 成功写入 team store 后会立即把有 `session_id` 的 teammate upsert 到 durable `agent_control_agents`，不再等后续 `ListAgentControlAgents` materialize 才生成 AgentRecord；合成 team root 不绑定 teammate session，避免 active session 唯一索引冲突。API session projection 也会把带 `agent_team_id` 的 session 规范化为 `team:<team>:<teammate|session>` AgentID，和 team projection/即时写入使用同一个 canonical identity。SQLite registry 的 upsert 现在允许同一 `root_session_id + agent_path` 被 canonical AgentID 接管，用来修复旧 session-id projection 留下的同路径 row，同时仍保留 active session 唯一绑定约束。新增 `toolbroker.TeamTeammateAgentProjector` 可选 hook，`spawn_team` 在 `TeamStore.UpsertTeammate` 成功后会把完整 teammate row 交给 dispatcher 立即投影到 AgentRegistry；API handler 与本地 `localActorRegistry` 均实现该 hook，无 durable registry 时保持兼容退化。新增 `TestBrokerExecuteSpawnTeamProjectsTeammatesImmediately`、`TestSpawnTeamToolProjectsTeammatesIntoAgentRegistryImmediately`、`TestLocalActorRegistry_SyncTeamTeammateAgentWritesRegistry`、`TestUpsertTeammateWritesAgentRegistryImmediately`、`TestUpdateTeammateWritesAgentRegistryAndDoesNotReopenClosedRecord`、`TestUpdateTeammateClearsAgentRegistrySessionBinding`、`TestListAgentControlAgentsMaterializesTeamSessionContextAsCanonicalTeammate` 与 `TestSQLiteGlobalAgentRegistryStoreCanonicalizesDuplicateRootPath` 覆盖 `spawn_team` tool path、HTTP upsert/patch、本地 projector、清空 session 时关闭旧 active registry row、closed row 不复活、session/team projection 不双写身份。
- 继续清理旧 mirror 依赖: runtime/team mailbox 和 team wake 的旧物理 mirror 已从正常写路径移除，并通过 migration drop。`RecordMailReceipt` 现在只要求 `agent_control_mailbox_records(scope=team)` 主表更新成功；旧 scoped/legacy mailbox 表不存在也不会影响 AgentControl 主 ack。新增 `TestSQLiteStoreRecordMailReceiptDoesNotRequireMirrorRows`、`TestSQLiteRuntimeStoreAppendMailbox` 与 wake 相关测试覆盖旧物理表不存在时仍能写主记录、写 receipt、读取 unread、读取 mailbox sequence 和获取 durable wake high-water mark。
- `go test ./internal/chat -run "Test(SQLiteRuntimeStoreAppendAgentControlMailboxPrefersControlRows|InMemoryRuntimeStoreAgentControlMailboxFiltersEnvelope)" -count=1 -v`
- `go test ./internal/team -run "TestSQLiteStore(ListMailPrefersAgentControlMessages|ListMail|ListMailAfterSeqReturnsLaterMessages)" -count=1 -v`
- `git diff --check`
- `go test ./...`

仍保留:

- team mailbox 已具备 durable sequence/watch 基础，并且 team mailbox 到 session mailbox 的 dispatch 已是 mailbox-first；`wait_agent` 无目标调用与 runtime HTTP `/agents/wait` 空目标请求均已支持等待当前 parent session mailbox/collab event，`read_agent_events` 无目标调用与 runtime HTTP `/agents/events` 也已支持读取 parent mailbox/collab event。runtime 与 team mailbox 现在都以 `agent_control_mailbox_records` 作为主物理表：runtime 使用 `scope=session` 与 `session_mailbox_seq` 投影普通 session mailbox，team 使用 `scope=team` 与 `team_seq` 投影 team mailbox；`AppendMailbox`、`AppendAgentControlMailbox`、`InsertMail`、`ListMail`、`LastMailSeq` 和 `RecordMailReceipt` 都不再依赖旧 scoped/legacy mailbox 表。旧 `agent_control_mailbox_messages`、`session_mailbox_messages`、`team_mailbox_messages` 仅存在于历史 migration 的回填阶段，随后会被 drop；当前 runtime 只保留 `mailbox_received` session event 作为 CLI/TUI 展示兼容镜像。两侧现在都能投影成共享 `agentcontrol.MailboxRecord` read model，分别标记 `scope=session` / `scope=team`；`agentcontrol.GlobalMailboxRegistry` 既能无配置时合并本地 source，也能在配置 `agentControl.storePath/storeDSN`、`agentControl.mailboxStorePath` 或 `agentControl.mailboxStoreDSN` 后 materialize 到 `agent_control_global_mailbox_records` 并使用 durable global row id。配置 durable global registry 后，Skills API handler 与本地 `aicli chat` runtime host 都会把 store 挂到 runtime/team store；`AppendAgentControlMailbox`、携带 AgentControl envelope 的 `AppendMailbox` 与 team `InsertMail` 会先写 `agent_control_global_mailbox_records(source=global)`，再写本地 `agent_control_mailbox_records(scope=session/team)` projection 并回填 `global_seq`。配置前已有的 local-only row 会通过 local-to-global repair 物化到 global registry，global-only row 也会通过 global-to-local repair 重建本地 projection；Skills API handler 与本地 chat 注入点都会后台触发 reconcile。team orchestrator 也会优先使用该 store 的 `MailboxWakeSource`，直接监听 global 表新 row 和读取 global wake sequence。`DeliverMailboxStoreFirst` 对带 envelope 的 control message 会优先走 `AppendAgentControlMailbox`，`chat.ListMailboxAgentControlFirst` / `chat.WatchMailboxAgentControlFirst` 已把 CLI/API 的 parent mailbox 兼容读取逻辑集中到共享 helper。Runtime API 现在有显式 `GET /api/runtime/sessions/{id}/agent-control/mailbox`、`GET /api/runtime/agent-control/mailbox`、`GET /api/runtime/agent-control/agents`、typed client `ListSessionAgentControlMailbox` / `ListAgentControlMailbox`，以及 `skillsapi-demo` 的 `control-mailbox` / `agent-control mailbox` action 可直接读取 control mailbox row、组合 registry 或可配置 durable global registry。Agent identity graph 已从表/API 地基推进到 API 与本地 CLI registry-first 写读路径: spawn reservation、`spawn_agent` 写入、`list_agents`、path target/path close 和 team teammate AgentRecord 投影与 API teammate upsert/patch 即时写入都优先使用 `agent_control_agents`，且同路径旧 session-id row 会被 canonical team AgentID 接管；AgentRegistry wake 已有独立 durable event sequence 与进程内 watch；`agentcontrol.RegistryService` 已支持单一 SQLite DB 同时承载 global mailbox 与 agent registry 表，Skills API 与本地 chat 默认可通过 `agentControl.storePath/storeDSN` 使用同一个 durable registry substrate；SQLite runtime/team store 在 global mailbox writer 可附加 registry DB 时，会通过 `AppendPrimaryGlobalMailboxRecordTx` 在同一 SQLite transaction 中写 global primary row 与 local projection，非 SQLite 或不可附加 DSN 继续走 repairable write-through/reconcile。local `agent_control_mailbox_records` 已经是可修复 projection/cache，但仍承担兼容读取和 CLI/TUI 展示桥接。
- child completion 已改为 parent AgentControl mailbox 主路径: child `session_end/session_interrupted` 先写 parent durable mailbox / `agent_control.subagent_completed` envelope，再追加 `display_mirror=true`、`mirror_source=agent_control_mailbox` 的兼容 `subagent.completed` session event；父 actor 不在线时仍能持久化 completion mailbox，且 display mirror 会记录 `mailbox_delivery_status`。普通 child/team mailbox、team task assignment 和 team terminal lifecycle mailbox 也已经通过共享 `internal/agentcontrol` envelope helper 收敛到 mailbox-first 投递。普通 child `send_message/followup_task`、child completion `subagent.completed`、team task assignment 和 team lifecycle 均携带统一 `message_type/control_action/workflow/mailbox_delivery/mailbox_kind` metadata，其中 child completion 的 envelope 构造和 display mirror 标记已进一步收口到 `toolbroker.BuildSubagentCompletionMailboxMessage` / `toolbroker.AnnotateSubagentCompletionDisplayMirror`，CLI/API 不再各自手写 metadata 拼装。当前 durable mailbox 已经接口化为可读可 watch 的 mailbox substrate，runtime AgentControl envelope 消息会进入 `agent_control_mailbox_records(scope=session)`，普通 session mailbox 也从同一主表投影；旧 scoped control table 和 legacy session mailbox table 只作为 migration 输入并在回填后删除。team mailbox 实体也已升级为 `agent_control_mailbox_records(scope=team)` 主写入，且两侧已有 shared mailbox read model、本地 combined registry、可配置 durable global registry、global-primary canonical 写入口、双向 projection repair、自动 reconcile 和 `global_seq` projection backlink。Agent identity graph 已接入 registry-first spawn/list/path close/team teammate 投影，并补齐 AgentRegistry durable wake sequence/watch；单一 SQLite registry service/config 与可附加 SQLite 同事务 projection 已落地，剩余缺口是进一步压缩展示型 session event mirror、把更多非 SQLite store 明确投影为兼容缓存，以及后续真正独立 daemon 化的 registry service 生命周期。
- `spawn_team` 的 task assignment 已有 `TaskTriggerClient` seam，派发请求/结果已进入持久 `team_events` 审计流，并且目标 teammate session 也会收到 durable `agent_control.task_assignment` / `team.task_assignment` control mailbox message，可通过 mailbox substrate 读取；`task.completed/task.failed/task.cancelled` 也会镜像为目标 teammate session 的 durable `agent_control.task_lifecycle` / `team.task_lifecycle` control mailbox message，其中 cancelled 现在覆盖 CLI Ctrl+C、legacy HTTP release、显式 AgentControl release、AgentControl status update、legacy full task update 与显式 AgentControl full task update；blocked/handoff 通知也已补齐同类 task lifecycle envelope 并可被 `ListAgentControlMailbox` 读取。AgentControl task/dependency 已具备统一 read/update/dependency/event seam 和 create/update/dependency/ready/status/release/lease/retry/claim/terminal/block writer seam；Runtime API、typed client、toolbroker `spawn_team`、`LeadPlanner` replan/follow-up、orchestrator ready/claim、terminal outcome、blocked outcome、retry/reclaim/lease/cancel 等外层入口均已通过这些 seam 进入 task graph。SQLite `agent_control_task_records` / `agent_control_task_dependencies` 已从 mirror 升级为主写入表，公开 `GetTask/ListTasks/ListTaskDependencies/ListTaskDependents` 与 terminal task status 统计也读取 AgentControl 表；legacy `team_tasks` / `team_task_dependencies` 已退化为 migration-only 回填输入，并在 migration v19 删除，legacy Store task 方法内部也不再同步这些表。当前剩余缺口不再是 task/mailbox 实体写权威、统一 mailbox 表形态、wake registry、非 SQLite mailbox projection、旧 task mirror 停写删除、global-primary mailbox 写入口、global mailbox wake/watch、projection repair/auto reconcile、local/global row 身份断裂、AgentRegistry spawn/list/path-close 接线或 AgentRegistry wake；单一 registry service/config、SQLite 同事务 mailbox projection 和 `/agents panel` 已补上，下一步重点是独立 registry service 生命周期、更多 store 后端投影兼容和 TUI 长驻/键盘导航。
- team orchestrator 已支持 mailbox sequence wake、task lifecycle sequence wake、lifecycle wake 和 fallback tick；mailbox wake 已经通过 `agentcontrol.MailboxWakeSource` seam 被 `Orchestrator.RunWithWake` 消费。未配置 global registry 时，`team.AgentControlMailboxWake` 作为 adapter 提供 durable sequence/watch，并优先使用统一 `agent_control_wake_events`；配置 durable global registry 时，`SQLiteGlobalMailboxRegistryStore` 直接作为 mailbox wake source，使用 `agent_control_global_mailbox_records.id` 作为 wake sequence。task lifecycle wake 也已经通过 `agentcontrol.TaskWakeSource` seam 被 `Orchestrator.RunWithWake` 消费，`team.AgentControlTaskRegistry` 作为当前 adapter 提供 durable sequence/watch，并优先使用同一张 `agent_control_wake_events`。旧 `agent_control_mailbox_wake_messages` / `agent_control_task_wake_signals` 仅作为 migration 回填输入，migration v18 会删除这些表，正常写路径不再写旧 wake mirror。剩余缺口不再是 wake registry、mailbox 主表形态、global mailbox wake/watch、非 SQLite projection、global-primary canonical 写入、单一 registry 配置入口或 SQLite 同事务 projection；下一步是把 registry service 生命周期继续从进程内 facade 推到可独立运行/复用的 durable service，并压缩展示兼容 mirror。
- 已有 `/agents`、`/agents panel [limit]`、`/agents pick`、`/agents target [target|clear]`、`/agents send [target] <message>`、`/agents followup [target] <message>`、`/timeline [team|active] [limit] [filter=<text>]` 和 `/collab [follow] [target|selected|parent|all] [limit] [filter=<text>] [timeout=10s]` 可见入口，timeline 已能审计 task dispatch 请求/完成与关键派发字段，支持显式 team id 或 active team，并支持 `filter=` / `match=` 行级筛选；collab 已优先查看 parent、当前 selected agent 或显式目标 session/path 的 mailbox substrate，并补充非 mailbox 协作终态事件，且现在会直接展示 AgentControl envelope 的 `msg/action/workflow/delivery/mailbox/event/target` 字段；`/collab all` 已支持 parent + 所有 child/team teammate mailbox 的分组聚合视图，`filter=` / `match=` 已支持事件行文本筛选，显式 `follow` 已支持等待下一次 mailbox 更新并刷新 snapshot；picker 已支持命令/弹窗选择并打印 agent 明细，无参数 `/agents target` 已能列出候选 target 并标记当前选中项，默认 target 已能持久化到 runtime session context 并在恢复时还原，send/followup 已支持显式目标或默认目标两种方式向 child agent 投递 mailbox 或触发后续任务；`/agents panel` 会在支持 fixed-bottom surface 时显示 TUI popup，否则输出同一份文本面板，聚合 selected target、parent session、active team、registry 状态、agent graph、mailbox snapshot 和 team timeline。正常 root 输入仍不会隐式改投 child，后续 P4 是把 panel 扩展成长驻可切换 target 的键盘导航视图。

完成度审计（更新至 2026-05-09，含 unified registry service/config、SQLite 同事务 projection、global-primary canonical mailbox、projection repair 与自动 reconcile）:

注: 下表中的“global registry 仍是 write-through/materialization 目标”已被上面的 global-primary mailbox、unified registry service 与 reconcile 工作推进；最新准确表述是: `AppendAgentControlMailbox`、携带 control envelope 的 `AppendMailbox` 与 `InsertMail` 在配置 durable global registry 时已 global-primary，`agentControl.storePath/storeDSN` 会让 mailbox registry 与 agent registry 共享同一个 SQLite DB，local/global 双向 projection repair 已有显式调用接口，并且 Skills API handler 与本地 chat 注入点都会自动触发一次 reconcile。Agent identity graph 的 storage-neutral contract、SQLite `agent_control_agents` 表、Runtime HTTP API、atomic spawn reservation、API controller registry-first、本地 CLI registry-first、无 durable store 投影 fallback 和 durable AgentRegistry wake 已新增。SQLite runtime/team store 在可附加 durable registry DB 时已支持同事务写 global primary row 与 local projection；非 SQLite 或不可附加 DSN 仍使用 repairable write-through/reconcile。TUI 已有 `/agents panel` 聚合视图，长驻键盘导航仍属后续 P4。

| 用户显式目标 | 当前证据 | 判定 | 仍需推进 |
| --- | --- | --- | --- |
| 分析 multi agent 执行过程与日志，找出问题并修复 | 本文档已记录真实日志问题、mimo 并行验证证据、`go test ./...` 与 `git diff --check`；代码已覆盖 Ctrl+C descendant cleanup、child completion mailbox mirror、parent mailbox wait/read、team terminal ordering、dispatch audit 等修复；新增 `TestSignalHandlerFirstInterruptCancelsActiveTeamRuns` 确认 `setupSignalHandler` 收到 `os.Interrupt` 后第一次 Ctrl+C 会清理 active team/task/runtime state 且不退出 chat loop，第二次 Ctrl+C 才设置 exit；新增 `TestInterruptActiveRunsPausesTeamAndStopsRuntimeStates` 进一步确认 Ctrl+C cancel 会产生 `task.cancelled` team event，并向原 assignee teammate session 写入 `agent_control.task_lifecycle` mailbox；新增 `TestAICLIChatActorExecutor_InterruptStopsSlowAutoStartTeam` 确认 `spawn_team auto_start=true` 慢 teammate 运行期间触发 `session.Interrupt()` 后 provider ctx 被取消，team/task/runtime 状态稳定收敛且不产生 terminal team/timeline 输出；新增 `TestAICLIChatActorExecutor_AutoStartTeamHandlesTeammateProviderStreamError` 确认 teammate provider 直接报 stream/internal error 时后台 team 不挂死并稳定失败收敛 | 部分完成 | 仍需要用真实 provider/真实终端进程场景继续覆盖手动 Ctrl+C 与后台 team settle 的组合；signal handler 入口、本地 Ctrl+C cleanup、provider 慢响应、stream error 和 cancel lifecycle mailbox 已有确定性测试覆盖 |
| child completion durable mailbox 通信模型 | child completion 已 mailbox-first: CLI/API 在收到 child terminal event 后先写 parent durable `agent_control.subagent_completed` mailbox，再追加 `display_mirror=true`、`mirror_source=agent_control_mailbox` 的兼容 `subagent.completed` session event；CLI/API 共用 `toolbroker.BuildSubagentCompletionMailboxMessage` 和 `toolbroker.AnnotateSubagentCompletionDisplayMirror`；公共 `DeliverMailboxEventFirst` 已验证优先走 AgentControl writer；Runtime API 已有 `GET /api/runtime/sessions/{id}/agent-control/mailbox` 与 `GET /api/runtime/agent-control/mailbox`，typed client `ListSessionAgentControlMailbox` / `ListAgentControlMailbox` 和 `skillsapi-demo -agent-action control-mailbox` / `-control-action mailbox` 可读取单 session control mailbox 或组合 registry；runtime `AppendAgentControlMailbox` 与普通 `AppendMailbox` 均写 `agent_control_mailbox_records(scope=session)`，旧 scoped control table 与 legacy session mailbox table 已在 migration 回填后删除；team mailbox 实体也已使用 `agent_control_mailbox_records(scope=team)` 主写入，旧 team mailbox/scoped control table 已在 migration v18 删除；runtime/team 两侧均可投影为 shared `agentcontrol.MailboxRecord`，并可通过 `GlobalMailboxRegistry` 形成命名的 in-process global read substrate；`agentControl.storePath/storeDSN` 与 `agentcontrol.RegistryService` 已提供单 SQLite registry DB | 接近完成但非最终形态 | child completion 语义已是 AgentControl mailbox 主路径；剩余缺口是 runtime 展示型 session event mirror 仍为兼容输出，以及后续把 registry service 从进程内 facade 推进为独立可复用 daemon/service |
| `spawn_team` task assignment 迁移到统一 AgentControl/substrate | `TaskTriggerClient` seam、dispatch requested/completed 事件、`team.task_assignment` / `team.task_lifecycle` durable mailbox、AgentRegistry teammate projection、AgentControl task read/update/dependency/event seam，以及 create/update/dependency/ready/status/release/lease/retry/claim/terminal/block writer seam 已完成并有测试；Runtime API、typed client、toolbroker `spawn_team`、`LeadPlanner` replan/follow-up、orchestrator ready/claim、terminal outcome、blocked outcome、retry/reclaim/lease/cancel 等外层入口均已通过对应 AgentControl writer；SQLite `agent_control_task_records` / `agent_control_task_dependencies` 已补齐完整字段并升级为主写入表，公开 `GetTask/ListTasks/ListTaskDependencies/ListTaskDependents` 与 terminal task status 统计都读取 AgentControl 表，legacy `team_tasks` / `team_task_dependencies` 仅作为 migration 回填输入并在 v19 删除；SQLite team mailbox 实体也已使用 `agent_control_mailbox_records(scope=team)` 主写入/主读取，旧 `team_mailbox_messages` 与 scoped `agent_control_mailbox_messages` 已在 migration v18 删除；team wake 已统一进入 `agent_control_wake_events`，旧 wake mirror 表也在 migration v18 删除；runtime/team mailbox 均可投影为 shared `agentcontrol.MailboxRecord`，非 SQLite store 可走 fallback projection；配置 durable global registry 后 canonical control mailbox 写入口已 global-primary，双向 projection repair 与自动 reconcile 已覆盖本地 chat 和 Skills API handler；Agent identity graph 已新增 `AgentRecord/AgentFilter/AgentRegistryStore/AgentSpawnReservationStore/AgentWakeSource` contract、SQLite `agent_control_agents` durable 表、`agent_control_agent_wake_events` durable wake 表和 `GET /api/runtime/agent-control/agents` API，覆盖 path 唯一、active session 唯一绑定、atomic reservation、registry-first spawn/list/path-close/team teammate 投影、API teammate 即时写入、spawn_team tool path 即时 teammate projection、canonical team identity 收敛、closed row 不复活和 agent graph watch/sequence；graph event cursor 已使用 SQLite `agent_control_task_graph_events.id` 作为全局 `seq`，并用 `team_seq` 保留原 team timeline sequence；`skillsapi-demo -mode agent-control` 可验证 tasks/update/dependencies/events/add-dependency/mailbox；单一 registry service/config、SQLite 同事务 mailbox projection 和 `/agents panel` 已新增测试覆盖 | 接近完成但非最终形态 | 已完成单一 SQLite registry facade 与可附加 SQLite 同事务 projection；剩余是独立 registry service 生命周期、更多 store 后端兼容投影和 TUI 长驻键盘导航 |
| orchestrator wake 使用 durable mailbox watch/sequence | team mailbox sequence wake、task lifecycle signal sequence wake、lifecycle wake、fallback tick 已完成；mailbox wake 已抽象为 `agentcontrol.MailboxWakeSource` 并由 orchestrator 消费，task lifecycle wake 已抽象为 `agentcontrol.TaskWakeSource` 并由 orchestrator 消费；runtime AgentControl mailbox watcher 已改为 control row 直接唤醒；team mailbox entity 已迁到 AgentControl-first `agent_control_mailbox_records(scope=team)`；team mailbox/task wake 已统一进入 `agent_control_wake_events`，两个 team adapter 都优先使用该 registry watch/sequence，旧 mailbox/task wake 表仅作为 migration 回填输入并在 v18 删除；durable global mailbox registry 现在也实现 `MailboxWakeSource`，配置后 orchestrator 会直接使用 `agent_control_global_mailbox_records.id` 作为 mailbox wake sequence；runtime/team local mailbox row 也会保存 `global_seq` backlink 到 global row；新增 `TestAgentControlMailboxWakeWatchesTeamMailbox`、`TestOrchestratorRunUsesAgentControlMailboxWakeSourceBeforeFallbackTick`、`TestOrchestratorRunUsesGlobalMailboxWakeBeforeFallbackTick`、`TestSQLiteGlobalMailboxRegistryStoreWatchesMailboxWake`、`TestSQLiteGlobalMailboxRegistryStoreDoesNotWakeOnIdempotentRefresh`、`TestSQLiteGlobalMailboxRegistryStoreWakeHonorsSessionFilter`、`TestSQLiteRuntimeStoreAppendAgentControlMailboxWritesGlobalRegistry`、`TestSQLiteRuntimeStoreAppendAgentControlMailboxCanCommitGlobalAndLocalInOneTx`、`TestInMemoryRuntimeStoreAppendAgentControlMailboxWritesGlobalRegistry`、`TestSQLiteStoreInsertMailWritesGlobalRegistry`、`TestSQLiteStoreInsertMailCanCommitGlobalAndLocalInOneTx`、`TestAgentControlTaskRegistryWatchesTaskWake`、`TestSQLiteStoreTaskSignalsPersistSequenceAndWake` 与 `TestOrchestratorRunUsesAgentControlTaskWakeSourceBeforeFallbackTick` 覆盖；配置 durable global registry 后 canonical control mailbox 写入口已 global-primary，local/global projection repair 可补偿漏投影 | 接近完成但非最终形态 | task、team mailbox 实体权威、同名同结构 mailbox 主表、team wake registry、runtime control watcher、global mailbox wake/watch、local/global projection backlink、非 SQLite mailbox projection、旧 wake mirror 删除、global-primary 写入口、projection repair、单 SQLite registry facade 和可附加 SQLite 同事务 projection 已迁到 AgentControl；剩余是独立 registry service 生命周期和展示 mirror 压缩 |
| TUI agent picker / collab timeline | 已有 `/agents panel [limit]`、`/agents pick/target/send/followup`、`/timeline [team|active] [limit] [filter=<text>]`、`/collab [follow] [target|selected|parent|all] [limit] [filter=<text>] [timeout=10s]`，并展示 envelope metadata；`/agents target` 已能列出候选 target 并标记当前选中项；timeline 和 collab 均支持 `filter=` / `match=` 行级筛选；`/collab all` 已提供 parent + child/team teammate mailbox 聚合视图，显式 `follow` 已支持等待下一次 mailbox 更新并刷新 snapshot；`/agents panel` 聚合 selected target、parent、active team、registry、agent graph、mailbox 和 timeline，并在 fixed-bottom surface 可用时渲染为 popup | 部分完成 | 已有可用面板入口；仍缺长驻 panel、键盘导航和 target 快速切换 |
