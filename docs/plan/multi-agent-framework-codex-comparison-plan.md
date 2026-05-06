# Multi-Agent 运行框架对比分析与改进方案

生成日期: 2026-05-06

范围:
- 当前项目: `E:\projects\ai\ai-agent-runtime`
- 对比项目: `E:\projects\ai\codex`
- 重点问题: 多 agent 并行执行、子 agent 输出隔离、等待/中断语义、控制台噪声、reasoning 输出、team 与 child session 的控制面边界。

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
- recent code 已经增加 reasoning block 的规范化输出，并避免一部分子会话/team 外来事件污染主控制台。
- Ctrl+C 中断逻辑已扩展到 chat interrupt 清理路径，但多 agent descendant 级别的关闭仍更多依赖 session hub 和 team lifecycle 的组合，而不是统一 agent tree close。

风险:

- 如果事件过滤只基于 team id/session id 局部判断，后续新事件类型很容易漏出。
- reasoning 输出当前仍由 runtime event bridge 统一处理。若 child session 的 reasoning event 未被严格归属和抑制，仍可能进入 root 控制台。
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

当前项目的问题表现为:

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

- agent picker。
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
