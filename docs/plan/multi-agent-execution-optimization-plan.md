# Multi-Agent 执行协作优化方案（wait / wake / 审批 / 配额 / 可观测）

更新时间: 2026-09-13
状态: implementing（P0-1/P0-2/P0-4/P1-6/P1-7/P2-8/P2-9 已实施；P0-3/P2-11/P2-12 部分实施（P0-3 补偿已覆盖 actor/prompt 失败但 `snapshot` 分支口径未定、`SubmitPromptAsync`/CLI/并发失败注入用例未补，P2-11 工具描述层未同步、P2-12 缺宿主能力过滤与 CLI `action cancel|close`，见 §5 实施注记），其中 P2-9 手动清理入口 `/agents cleanup` 与「终态/空闲自动 close」（周期对账 observe 只报候选、enforce 才落库）已落地（TTL 删除按方案建议走对账兜底、不做直接回调）；P0-4 仅剩真实终端 probe 与 production-readiness 文档差异标注；P1-6 wake 预算已补齐三处同源同序的可见性（`/supervision/digest` 与 `/supervision/snapshot` 的 `wake_budget` 投影、CLI `/debug supervision list`、以及模型可见的 preflight digest 文本行；父 turn 结束自检（方案 4）已实施并默认关闭，需显式开 `supervision.wake_self_check_per_window`）；P2-8 产品事件流 `agent.reclaimed` 已接前端轨迹（仅剩真机长跑 probe）；P1-7 最后一项待补已补齐（相同 `after_seq` 的重复读返回 `unchanged` / `repeat_count` 显式信号，不再只靠 `next_action` 间接引导）；P1-5 四个方案全部实施——前端下钻、`read_agent_events` 过滤视图（`view=tool_progress`）、父流节流镜像（`subagent.progress` 后端镜像 + 前端 `runtime-0` 折叠行）与本轮补齐的**方案 4「前端 inline 审批」**（下钻对话框内直接批准/拒绝，动作走 actor `approve_tool` 命令），仅剩真机 probe；P2-10 单测与并发压测已补（真实终端 probe 未做）。实施记录见 §10）
适用仓库: `E:\projects\ai\ai-agent-runtime`
参照仓库: `E:\projects\ai\codex`（codex-rs，只读对照，不修改）
证据基线: 2026-09-13 工作树（含未提交改动）。引用行号会随代码漂移，实施前需按符号名复核。

---

## 1. 文档定位

本文件聚焦 multi-agent 的**执行协作语义**：父 agent 与轻量 child agent 之间的等待、唤醒、审批、配额、回收、可观测性与协作引导。

它承接但不重复以下已基本收尾的主线（AgentControl 收敛、mailbox 权威、legacy mirror 压缩、registry service 生命周期），只记录**尚未闭环、且有明确代码证据**的优化项与落地方案。

本文件包含两部分内容：

1. **功能审查结论**（§3、§4）：对当前实现的机制基线复核，以及本轮新发现的 10 个问题（N1–N10）。
2. **优化详案**（§5–§9）：P0/P1/P2 共 12 项，每项包含问题、证据、方案、验收与风险。
3. **实施记录**（§10）：已落地项的代码落点、定向测试与剩余项。

审查方法：只读代码走查（grep/view），覆盖 `backend/internal/{api/skills,toolbroker,supervision,agentcontrol,agent,team,chat,config}`、`backend/cmd/aicli`、`frontend/src`，并与 codex-rs 对应实现对照。未修改任何代码。

---

## 2. 相关文档

| 文档 | 与本文件的关系 |
| --- | --- |
| `docs/plan/multi-agent-production-readiness-plan.md` | 生产就绪收尾门禁；本文件是其后续的执行协作专题 |
| `docs/plan/multi-agent-next-stage-implementation-plan.md` | AgentControl substrate 收敛计划；本文件不重复其已完成项 |
| `docs/plan/multi-agent-agentcontrol-convergence-plan.md` | registry/mailbox 收敛背景 |
| `docs/plan/multi-agent-framework-codex-comparison-plan.md` | 与 codex 的整体机制对照（本文件只做增量对照） |
| `docs/plan/spawn-subagents-async-supervisor-plan.md` | 批量 spawn 与 supervisor 设计背景 |
| `docs/plan/spawn-agent-team-supervision-timeout-recovery-plan.md` | 超时/监督恢复背景（P0-2、P0-4 依赖其语义） |
| `docs/plan/runtime-observability-supervision-http-api-plan.md` | supervision 事件与 HTTP API；P1-6 需与其对齐 |
| `docs/plan/parallel-tool-execution.md` | 并行工具执行设计；决定 broker 工具串行（N6 的依据） |
| `docs/plan/multi-agent-real-terminal-validation-runbook.md` | 真实验证 runbook；§8 的验收脚本在此扩展 |
| `docs/working/multi-agent-real-terminal-validation-20260509.md` | 最近一次真实 provider 验证摘要 |

---

## 3. 事实基线（2026-09-13 复核）

### 3.1 执行模型

当前多 agent 执行模型为「spawn 立即返回 → 父主动 wait/read（事件唤醒 + 兜底 ticker）→ 子终态由运行时落 mailbox 并镜像到父事件流」：

1. `spawn_agent` 只完成子会话创建、路由解析、registry 预留与首条 prompt 投递，不等待子 agent 的模型轮次（`session_runtime_support.go:488-606`）。
2. 父可通过 `wait_agent` 阻塞等待（默认 30s，事件唤醒 + 500ms 兜底 ticker，`session_runtime_support.go:1633-1709`），或通过 `read_agent_events` 增量读取子事件（`:1846-1924`）。
3. `waiting_approval` 在 wait 语义中被视为 ready（`:2925-2932`），父可用 `resolve_agent_approval` 处理审批（`:1596-1631`）。
4. 子会话终态会写入父 mailbox（`:1135-1147`），并向父事件存储镜像 `subagent.completed`（`:964-981`）。
5. 主动唤醒（wake turn）只对 critical + action_required 事件生效，并有速率预算（`supervision/projection.go:91-135`、`supervision/wake_scheduler.go:240-253`）。

### 3.2 关键参数

| 参数 | 默认值 | 证据 | 备注 |
| --- | --- | --- | --- |
| `agents.maxThreads` | 6（未设置/`0` = 回退默认 6；`-1` = 显式不限） | `config/manager.go:327-331`、`agentcontrol/reclaim.go:28` | `< -1` 报错 `manager.go:1065-1067`；P2-8 语义收敛 |
| `agents.reclaimIdleMs` | 0（关闭空闲驱逐） | `config/manager.go:327-331`、`agentcontrol/reclaim.go:344` | `> 0` 才回收空闲子会话，负数报错（P2-8） |
| `agents.maxDepth` | 1 | `config/manager.go:327-331` | 超限错误带 next_action `session_runtime_support.go:2565-2571` |
| `agents.defaultWaitTimeoutMs` | 30s | `config/manager.go:327-331` | 只做下界兜底，无 min/max clamp（P0-1） |
| `agents.defaultForkTurns` | `none` | `config/manager.go:327-331` | 支持 `none/all/N` |
| wait/read 超时解析 | `<=0` 取默认 | `session_runtime_support.go:1649-1656`、`chat_actor_registry.go:2136-2142,2206-2213` | CLI/API 同构，无边界 |
| supervisor 开关与周期 | enforce / 5s scan | `supervision/execution_supervisor.go:48-57` | 仅 API 侧启动 `handler.go:4621-4624` |
| execution / progress / approval 超时 | 30m / 5m / 1h | `execution_supervisor.go:48-57` | cancel grace 15s，store outage 2m |
| auto-wake 预算 | 5 次/小时/rootScope | `wake_scheduler.go:78-92,240-253` | 计数为进程内内存（N4） |
| doom loop 豁免 | wait/read 等轮询工具 | `agent/doom_loop.go:165-183` | 防重复依赖 next_action 文案 |

### 3.3 组件职责

| 组件 | 职责 | 关键文件 |
| --- | --- | --- |
| sessionAgentController | spawn/wait/read/send/close/approve 的 API 侧实现 | `backend/internal/api/skills/session_runtime_support.go` |
| toolbroker | 工具 schema、参数解析、结果收敛与 next_action 文案 | `backend/internal/toolbroker/{broker.go,types.go}` |
| supervision | 终态投影、wake 调度/消费、执行巡检 | `backend/internal/supervision/*` |
| agentcontrol | agent 身份图、路径、跨进程预留、mailbox、一致性审计 | `backend/internal/agentcontrol/*` |
| agent (loop) | broker 工具执行、并行批次门控、doom loop | `backend/internal/agent/{loop.go,tool_parallel_scheduler.go,doom_loop.go}` |
| chat | 会话存储、TTL 清理、归档 | `backend/internal/chat/manager.go` |
| CLI registry/host | 本地 CLI 的同构实现与本地 wake/preflight | `backend/cmd/aicli/commands/{chat_actor_registry.go,chat_actor_host.go}` |
| frontend | SSE 消费、trajectory 渲染 | `frontend/src/api/runtime/sse.ts`、`frontend/src/hooks/workspace/agent-chat-turn/stream-handlers.ts` |

---

## 4. 功能审查结论（本轮新增）

### 4.1 多 agent 能力矩阵（精简）

状态：已实现 / 部分 / 缺失。证据为当前工作树行号；“对照”指与 codex-rs 同类能力相比的相对位置。

| # | 能力 | 状态 | 关键证据 | 对照 |
| --- | --- | --- | --- | --- |
| 1 | `agent_type` 角色注入（extend/full、工具策略 overlay） | 已实现 | `session_runtime_support.go:565-567,3390-3401,3511`；`agentdef/definition.go:41` | 持平 |
| 2 | per-spawn model/provider/reasoning 覆盖与回显 | 已实现 | `toolbroker/broker.go:291-301,1370-1377,1514-1520,1562-1563` | 领先（requested/effective 审计字段） |
| 3 | `fork_turns=none/all/N` | 已实现 | `session_runtime_support.go:180-213,515-517,547-555`；`config/manager.go:330` | 持平 |
| 4 | 父子双向消息（send_message/send_input/followup/interrupt） | 已实现 | `session_runtime_support.go:1414-1422,1531-1544,1546-1594` | 持平 |
| 5 | `close_agent` / `resume_agent` | 部分 | close 子树 `:2093-2145`；resume 只重建 actor+snapshot `:2364-2377` | 落后（codex 支持从 rollout 重建） |
| 6 | `wait_agent`（多 id、完整 snapshot、waiting_approval=ready） | 已实现 | `:1633-1709,2538-2561,2925-2932` | 领先（codex v2 只返回 `{message,timed_out}`） |
| 7 | `read_agent_events`（after_seq 增量、长轮询） | 已实现 | `:1846-1924`；`toolbroker/types.go:543-568` | 领先（codex 无等价工具） |
| 8 | 审批链路（resolve/patched args/待审批快照） | 部分 | `:1596-1631,2434-2438`；超时升级仅 supervisor `execution_supervisor.go:352-358` | 部分领先（codex 子审批无协作工具面） |
| 9 | 批量 `spawn_subagents`（幂等、恢复、部分完成） | 已实现 | `agent/subagent_batch_coordinator.go:27-35,416-445,917,1160-1222` | 领先 |
| 10 | `spawn_team`/`wait_team` 与 teammate 投影 | 已实现 | `toolbroker/broker.go:2413-2513`；`session_runtime_support.go:2498-2535` | 持平 |
| 11 | 子 agent 用量聚合到父/全局账单 | 部分 | orchestrator 聚合 `agent/orchestrator.go:314,319,584`；API usage 按 scope `handler.go:6695`；spawn_agent 路径未见聚合 | 落后 |
| 12 | 权限/沙箱/工具集继承与降级 | 已实现 | `session_runtime_support.go:3390-3440`；`broker_agent_test.go:585-646` | 领先（requested/effective 分层） |
| 13 | 并发配额（跨进程原子预留、无驱逐） | 部分 | `session_runtime_support.go:2563-2604`；`global_agent_store.go:366-369,537-541` | 落后（codex 槽满时 LRU 驱逐） |
| 14 | 超时治理（execution/progress/approval/cancel grace） | 部分 | `execution_supervisor.go:48-57`；仅 API 接线（N3 对应项见 P0-4） | 落后（CLI 无巡检） |
| 15 | 结果回传（mailbox + 父镜像 + preflight digest） | 已实现 | `session_runtime_support.go:1060-1061,1135-1147,964-981` | 持平 |
| 16 | 错误可操作性（next_action 覆盖） | 部分 | `session_runtime_support.go` 仅 2 处显式 next_action（`:884,:2568`） | 落后（N5） |

### 4.2 本轮新发现问题

| 编号 | 优先级 | 问题 | 归属优化项 |
| --- | --- | --- | --- |
| N1 | P0 | spawn 预留成功后失败不回滚，形成“配额僵尸” | P0-3 |
| N2 | P1 | API 侧缺少 CLI 已有的 stale-binding 清理 | P0-3 |
| N3 | P1 | 一致性审计只能手动触发，无定期对账/自动收敛 | P2-9 |
| N4 | P1 | auto-wake 预算是进程内内存计数，多进程放大、重启清零 | P1-6 |
| N5 | P1 | next_action 覆盖极低，多数 agent 工具错误不可操作 | P1-7 |
| N6 | P2 | broker 工具（含 `spawn_agent`）被并行批次整体排除 | P2-10 |
| N7 | P1 | 前端只消费父流静态 `subagent` 事件，无子会话下钻 | P1-5 |
| N8 | P2 | `maxThreads=0` 语义为“无限”，与“全零回退默认”叠加易误配 | P2-8 |
| N9 | P1 | 监督通知的 ack/control 动作在本地宿主不可达，critical 项永久 unresolved、每轮复现 | P2-12 |
| N10 | P1 | 只读子代理的管道复合命令被 policy 整体拒绝，批次失败且无恢复指引 | P1-7、P2-12 |

#### N1：spawn 预留成功后失败不回滚（配额僵尸）

- 证据：`session_runtime_support.go:577-581` 完成 `reserveOrRegisterAgentSpawn` 后，`:584-588`（`GetOrCreate` 失败）与 `:591-594`（`SubmitPromptAsync` 失败）直接返回，只做隔离清理或什么都不做：既不 `storage.Delete` 子会话，也不把 registry 行标记 closed/stale。对比失败更早的分支（`:558,:570,:579`）都会删除会话。
- 计数来源：`enforceSpawnLimits` 统计的是未被关闭/未 stale 的 registry 行（`:2583-2592`；`ListAgentControlAgents` 默认排除 closed/stale，`global_agent_store.go:537-541`）。
- 现状无回滚 API：`agentcontrol` 只暴露 `ReserveAgentControlAgentSpawn`（`agent_registry.go:245-250`）与 Upsert/Close，没有“预留失败补偿/释放”。
- 影响：残留 active 行永久占用 `maxThreads`；会话随后被 TTL 删除（`chat/manager.go:513-567`）后，registry 行仍在，只能靠 `/debug` 的一致性审计（`consistency_audit.go:64-66`、`chat_debug.go:1292,2135`）发现 `ACTIVE_AGENT_SESSION_MISSING`，但审计不修复（见 N3）。

#### N2：API 与 CLI 的 stale-binding 预处理不对称

- 证据：CLI 在预留前会先执行 `closeStaleLocalAgentSessionBinding`（`chat_actor_registry.go:1180-1185`）；API 侧 `reserveOrRegisterAgentSpawn`（`session_runtime_support.go:1149-1175`）没有对应步骤。
- 影响：API 侧遇到 stale binding 时可能直接 spawn 失败（或复用错误身份），CLI 侧已被兜底，行为不一致。

#### N3：一致性审计无后台调度

- 证据：`agentcontrol.AuditAgentSessionConsistency` 明确“reports drift without mutating either store”（`consistency_audit.go:37-40`）；调用点只有 CLI debug 面板（`chat_debug.go:1292-1295,2132-2138`）。
- 影响：registry 与 session store 的漂移（含 N1 的僵尸行）不会被自动发现或收敛。

#### N4：auto-wake 预算是进程内内存

- 证据：`WakeScheduler.claims map[string][]time.Time`（`wake_scheduler.go:65-66`）由 `allowAutoWake/recordClaim` 维护（`:240-259`），默认 window=1h、max=5（`:78-92`）。
- 影响：CLI、runtime-server、Skills API 等多进程各自计算预算 → 实际唤醒频率成倍放大；进程重启后预算清零。与“按 root scope 全局节流”的设计意图不符。

#### N5：next_action 覆盖不足

- 证据：`session_runtime_support.go` 全文仅两处显式 next_action（worktree 落盘 `:884`、depth 超限 `:2568`）。以下关键错误均为裸错误串：线程数超限（`:2591,:2601`）、session busy（`:1571`）、session 已存在（`:538`）、目标路径不存在（`resolveTargetSessionID`）。
- 影响：模型在失败后缺少明确恢复动作，容易重复同一调用；也削弱了 doom loop 之外的软护栏。

#### N6：broker 工具被并行批次整体排除（设计取舍，需测试固化）

- 证据：`agent/tool_parallel_scheduler.go:32`（需 `EnableParallelTools` 且 `MaxParallelToolCalls>1`）、`:63-68`（任一 broker 工具或 `spawn_subagents` 即整批回退串行）；设计背景见 `docs/plan/parallel-tool-execution.md:17`。
- 影响：同一 assistant turn 内的多个 `spawn_agent` 实际串行执行、逐个预留配额。这不是缺陷，但需要测试固化该语义，并在将来放开 broker 并发时补并发 spawn 的 e2e（见 P2-10）。

#### N7：前端无子会话下钻

- 证据：前端仅解析父流 `subagent` 事件（`frontend/src/api/runtime/sse.ts:249-251`），在 `stream-handlers.ts:312-326` 聚成 trajectory 项与 `subagents` artifact，注释明确为 "static SSE execution"；`frontend/src` 内 grep `parent_session_id` 无匹配入口。
- 影响：子 agent 的工具进度、审批请求在 Web UI 不可见（P1-5）。

#### N8：`maxThreads=0` 语义易误配

- 证据：`MaxThreads <= 0` 直接跳过限制（`session_runtime_support.go:2572-2574`）；校验只拒绝负值（`config/manager.go:1065-1067`）；而 `agentsConfig()` 仅在**全零**时回退默认（`:2612-2613`）。
- 影响：只把 `maxThreads` 配成 0（其余字段非零）会静默变成“无限并发”，与用户预期（用默认值 6）相反。

#### N9：监督通知的收敛动作在本地宿主不可达（critical 项永久复现）

- 现象：本轮 preflight 连续报告 `critical_unresolved: 2`，条目为 `agent_session codex-ref2`（terminated）与 `agent_run batch_52d54fa3c2ec843b`（blocked），并宣告 `allowed=[inspect,acknowledge,defer,cancel,close]`。
- durable 事实（库 `~/.aicli/sessions/runtime/supervision/supervision.db`，只读查得）：两行均为 `decision_state=unacknowledged`、`resolution_state=unresolved`、`severity=critical`、`delivery_state=seen`，`acknowledged_at`/`resolved_at` 为 NULL；`allowed_actions_json` 分别为 `[]`（codex-ref2，version=6）与 `["inspect","cancel","close"]`（batch，version=4）。四行通知的 `updated_at` 完全相同（`2026-09-13T01:15:24.057Z`），此后无任何状态推进。
- 动作不可达：`backend/internal/toolbroker/broker.go:29-55` 的工具集中不存在 `ack_lifecycle` / `control_descendant` / `supervision_snapshot`（Go 代码零命中）；这三个名字只出现在设计稿提案里（`docs/plan/spawn-agent-team-supervision-timeout-recovery-plan.md:878-890,1465-1475`）。因此 preflight 宣告的 `acknowledge`/`defer` 在本会话没有任何执行入口，注入摘要的 allowed 列表也与 durable 行不一致。
- HTTP 动作面存在，但作用于另一个 store：`backend/internal/api/skills/handler.go:840-852` 已挂载 `/api/runtime/supervision/*`（含 `notifications/{id}/ack`、`/defer`、`actions`），实现见 `supervision_handlers.go:397-425` 起，动作服务见 `internal/supervision/action_service.go:84-96,477-519`。但实测 `runtime-server`（PID 23520，监听 `::8101`）对同一 scope 的通知数为 0 —— CLI 本地库与服务端进程库不是同一份数据。
- 唯一可执行的写动作及其边界：对已死会话可 `close_agent`（本轮已执行，返回 `status: stopped`），但 close 不改写监督台账（`version`/`updated_at` 未变），既不算 ack 也不算 resolve。
- 附带发现（批量收敛语义）：`action_service.go:457-471` 在成功动作后会遍历同 root scope 下**所有** `unresolved` 通知并一并 `ResolveNotification` —— 若未来把 ack/control 暴露给模型，需要先确认这种批量收敛是否会把无关告警误消。
- 影响：`critical_unresolved` 只增不减，已终态对象的历史告警在后续每个 turn 重复出现，稀释真正的“新”信号；模型看到 action 却无工具可用，等于在 preflight 里制造不可执行指令。

#### N10：只读子代理的复合 shell 命令被整体拒绝，批次失败且无恢复指引

- 事实：本轮 `spawn_subagents` 只读批次 `batch_52d54fa3c2ec843b` 以 `2/3` 失败告终，三条子任务错误全部是 `read-only policy blocks compound shell command; submit one command per shell.commands entry`。
- 被拒命令示例（原样保留在 supervision 通知的 `reason` 字段）：`Get-ChildItem -Path backend -Recurse -Directory | Select-Object ... | Select-Object -First 100` 等管道链。
- 性质判断：这不是子代理能力缺陷，而是“只读命令分类器按 entry 粒度判定 + 子代理工具用法未对齐”造成的机械失败；错误文本虽说明了原因，但没有 next_action、没有拆分示例，批次协调器也没有针对该错误类型的重试/降级。
- 影响：只读调查类批次在这种语境下成功率不可控；父代理只能靠 supervision 通知事后发现，或在下一批里自行改写成单命令。

### 4.3 已核实为“设计取舍”而非缺陷的项

1. **终态成功不触发 wake**：success → info/resolved 不排 wake（`supervision/projection.go:91-135`），依赖 mailbox + 下一个自然 turn 的 preflight digest 消费。与 codex 的 `trigger_turn=false` 行为一致，保留为默认；仅建议做限速分层与多进程一致性（P1-6）。
2. **broker 工具串行**：见 N6，属并行工具执行的保守门控，不建议为“同 turn 多 spawn”单独开洞。
3. **轮询工具豁免 doom loop**：`agent/doom_loop.go:165-183` 豁免 wait/read，避免正常长轮询被误判；代价是防重复依赖文案，建议补软护栏而非取消豁免（P1-7）。

---

## 5. 优化项详案

优先级口径：P0 = 正确性/健壮性（会产生错误状态或资源泄漏）；P1 = 可观测性与体验（行为正确但不可见/不可控）；P2 = 效率、治理与测试补充。

### P0-1 `wait_agent` / `read_agent_events` 增加超时上下界

**问题**

等待工具只做下界兜底、没有上界：模型传 `timeout_ms=3600000` 会把父 turn 钉在单次工具调用里 1 小时；传 `timeout_ms=1` 则退化为高频空转。配置项 `defaultWaitTimeoutMs` 同样只拒绝负值。

**证据**

- API：`session_runtime_support.go:1649-1656`（wait）、`:1724-1730`（mailbox-only wait），均只 `<=0 → 默认`。
- CLI：`chat_actor_registry.go:2136-2142`、`:2206-2213` 同构。
- 配置校验：`config/manager.go:1071-1073`；默认值 `:327-331`（测试 `manager_test.go:140-142` 只覆盖负值）。
- codex 对照：默认 30s、最小 10s、最大 1h（`codex-rs/core/src/tools/handlers/multi_agents_common.rs:28-30`、`core/src/config/mod.rs:208-210,266-267`）；v1 越界 clamp，v2 越界报错（`multi_agents/wait.rs:89-97`、`multi_agents_v2/wait.rs:50-66`）。

**目标**

任何路径的等待时长都落在 `[minWaitTimeoutMs, maxWaitTimeoutMs]`；越界行为可配置（clamp 或报错），且默认兼容现有调用。

**方案**

1. 扩展 `config.AgentsConfig`：新增 `minWaitTimeoutMs`（默认 10000）与 `maxWaitTimeoutMs`（默认 3600000），并纳入 `DefaultRuntimeConfig`（`config/manager.go:79-85,325-331`）与校验（`:1065-1080`：`min<=max`、两者非负）。
2. 抽公共函数，例如 `agentcontrol.NormalizeWaitTimeout(requested, defaultMs, minMs, maxMs) (timeout time.Duration, clamped bool, err error)`，避免 API/CLI 两处再次分叉。
3. API 侧在 `Wait`（`session_runtime_support.go:1649-1656`）与 `waitForMailboxEvent`（`:1724-1730`）接入；CLI 侧在 `chat_actor_registry.go:2136-2142,2206-2213` 接入。
4. 结果体新增回显字段（`toolbroker.AgentWaitResult`）：`requested_timeout_ms`、`effective_timeout_ms`、`timeout_clamped`，让父模型知道实际等待时长。
5. 越界时发运行时事件 `agent.wait_timeout.clamped`（可选给父流），便于排查“为什么没等够”。

**实施注记（2026-09-13 复核）**：回显字段命名与本文不同，实际是 `wait_timeout_requested_ms` / `wait_timeout_ms` / `wait_timeout_clamped`（`backend/internal/toolbroker/types.go:492-493`）；上面第 5 条的运行时事件 `agent.wait_timeout.clamped` 未实现（原文即标注“可选”）。

**测试与验收**

- 单测：`0 → 默认`、`1 → min`、`>max → max`、`min<=x<=max` 原样；API/CLI 两条路径参数化用例一致。
- 集成：`wait_agent(timeout_ms=1)` 不再产生 1ms 空转循环；`wait_agent(timeout_ms=7200000)` 实际等待 ≤1h 且结果体回显 clamped。
- 命令：`go test ./internal/toolbroker ./internal/api/skills ./cmd/aicli/commands -run Wait -count=1`。

**兼容与风险**

- clamp 会改变既有调用行为（例如依赖超长等待的脚本）。建议第一版对“超过 max”**报错并附可操作提示**（对齐 codex v2），配置 `waitTimeoutMode=clamp|error` 提供选择；在事件与结果体中同时回显，避免静默截断。

---

### P0-2 子 agent 审批/提问的主动通知

**问题**

子 agent 停在 `waiting_approval` 时，只有“正在 wait 的父”会被 `approval_requested` 唤醒；父不在等待（已结束本轮、或在做别的工作）时没有任何主动路径，子 agent 会一直等到审批超时（默认 1h，且仅当 supervisor 已接线）。

**证据**

- 终态订阅只接受 `session_end`/`session_interrupted`：API `session_runtime_support.go:922-925`；CLI `chat_actor_registry.go:1011-1018`。
- wait 的唤醒事件集合包含 `approval_requested`：`session_runtime_support.go:2934-2941`（仅对正在 wait 的父有效）。
- 唯一主动升级：supervisor 的审批超时（`execution_supervisor.go:352-358`），且仅 API 侧接线（`handler.go:4621-4624`）。
- 快照已暴露待审批信息：`:2434-2438`（说明信息层已具备，缺的是“推”）。

**目标**

父 agent 在下一回合开始时必然看到 pending 审批；在关键情形下可被主动唤醒。

**方案**

1. **Inbox + preflight digest（必做，低风险）**：把子会话的 `approval_requested` / `question_asked` 投影为 supervision notification（`reason=approval_required`, `action_required=true`），并在父 turn 的 preflight digest 中输出“待审批列表（session_id/path/tool/request_id/等待时长）”。参考现有 digest 注入点 `session_runtime_support.go:1060-1061` 与 `supervision/digest.go`。
2. **受控 wake（可选，默认开）**：将审批/提问纳入 auto-wake 的 critical 类别（`projection.go` 的 critical 判定），但受 P1-6 的分层预算约束（审批不占用失败类配额）。
3. **去重与收敛**：同一子会话同一 `request_id` 只通知一次；审批被 resolve 后发 resolved 通知，避免父每次 turn 都看到陈旧项。
4. CLI 与 API 同步实现（两处订阅/投影逻辑需一致，可共用 helper）。

**测试与验收**

- 单测：子会话进入 waiting_approval → 产生 notification + digest 条目；resolve 后条目消失；重复事件不重复通知。
- 集成：父已结束本轮（idle）时子进入审批，下一自然 turn 的 preflight 必含该审批；父在 wait 时行为不变（仍被立即唤醒）。
- 命令：`go test ./internal/api/skills ./internal/supervision ./cmd/aicli/commands -run "Approval|Digest|Wake" -count=1`。

**兼容与风险**

- 主动 wake 会增加父 turn 次数，需纳入限速与去重；默认先做“digest 必达 + wake 可配置”，避免影响现有节流行为。
- 与 `docs/plan/runtime-observability-supervision-http-api-plan.md` 的事件结构需保持一致。

---

### P0-3 spawn 失败回滚与配额一致性

**问题**

`spawn_agent` 在 registry 预留成功之后仍可能失败（hub/actor 创建失败、首条 prompt 投递失败），但当前代码在这两个分支直接返回错误，不回滚子会话、不释放/关闭 registry 行，导致 active 行永久占用 `maxThreads`（N1）；同时 API 侧缺少 CLI 已有的 stale-binding 预处理（N2）。

**证据**

- 成功预留：`session_runtime_support.go:577-581`。
- 泄漏分支：`:584-588`（`GetOrCreate` 失败）、`:591-594`（`SubmitPromptAsync` 失败）——只清隔离或直接返回，无 `storage.Delete`、无 registry 回写。
- 对照分支：`:558`、`:570`、`:579` 失败时均 `storage.Delete`。
- 无释放 API：`agentcontrol/agent_registry.go:245-250` 只有 Reserve；`global_agent_store.go:366-369` 注释明确其职责是“atomic upsert + 计数 + 建 child row”。
- 计数只看未关闭行：`session_runtime_support.go:2583-2592` + `global_agent_store.go:537-541`（默认排除 closed/stale）。
- CLI 已有 stale 清理：`chat_actor_registry.go:1180-1185`；API 无对应（`:1149-1175`）。
- 漂移靠手动审计发现：`consistency_audit.go:64-66`、`chat_debug.go:1292-1295`。

**目标**

任何 spawn 失败都不留下“active 但不可用”的 registry 行；会话存储与 registry 状态在失败后保持一致；API/CLI 行为对齐。

**方案**

1. **补偿事务**：在 `Spawn` 中把预留之后的所有失败路径统一收敛到一个 `rollbackSpawn`（defer/错误包装均可）：
   - 将 registry 行标记为 `stale`（推荐，保留诊断价值）或 `closed`；
   - 删除本轮创建的 child session（仅当本函数创建，未复用用户传入的既有 id）；
   - 清理 worktree/isolation（复用现有 `cleanupAPISpawnIsolation`）。
2. **补 Store API**：在 `AgentSpawnReservationStore`（或相邻接口）增加 `ReleaseAgentControlAgentSpawn(ctx, agentID, reason)`，语义为“预留未生效 → stale”；CLI/API 共用。
3. **对齐 stale 预检**：API 的 `reserveOrRegisterAgentSpawn` 增加 CLI 同款 stale binding 清理（把 `closeStaleLocalAgentSessionBinding` 的通用逻辑下沉到 `agentcontrol`，两处共用）。
4. **兜底对账**：即使补偿失败，也由 P2-9 的周期性审计兜底（审计发现 `ACTIVE_AGENT_SESSION_MISSING` / `ACTIVE_AGENT_SESSION_TERMINAL` 后自动置 stale）。

**实施注记（2026-09-13 复核）**：补偿已落地，覆盖 `GetOrCreate` 与 `SubmitPromptAsync` 失败（API `backend/internal/api/skills/session_runtime_support.go:589-613`、CLI `backend/cmd/aicli/commands/chat_actor_registry.go:705-729`），`ReleaseAgentControlAgentSpawn`（`backend/internal/agentcontrol/agent_registry.go:249-258`）与 API 侧 stale 预检也已下沉；**`snapshot` 读回失败分支仍直接 return、未走补偿**（API `:614-617`、CLI `:730-733`）——该分支子会话已创建且 prompt 已入队，是否也要回滚需先明确口径。验收侧已补：API 级故障注入 `backend/internal/api/skills/session_agent_spawn_rollback_test.go`（注入 `GetOrCreate` 失败，断言 stale 终态 + 半成品会话删除 + active 只剩 root）、store 级 release 幂等（`global_agent_store_test.go:445-465`）与并发守恒（`concurrency_stress_test.go`）；**未补**：`SubmitPromptAsync` 失败注入、CLI 同场景用例、“并发达上限 + 失败注入” 用例。本条按“部分实施”计。

**测试与验收**

- 单测（故障注入）：`GetOrCreate` 失败、`SubmitPromptAsync` 失败后断言：session 不存在（新建场景）、registry 行非 active、`maxThreads` 计数恢复。
- 并发用例：并发 spawn 达到上限 + 失败注入，最终 active 计数 ≤ maxThreads 且无长期残留。
- CLI 用例：同场景断言本地路径行为一致。
- 命令：`go test ./internal/api/skills ./internal/agentcontrol ./cmd/aicli/commands -run "Spawn.*(Fail|Rollback|Limit)" -count=1`。

**兼容与风险**

- 新增 Store 方法需为旧实现提供可选接口（type assertion），不实现时降级为 `Upsert(status=stale)`。
- 若用户显式传入 `id` 复用既有 session，回滚不得删除其数据，只能释放 registry 行。

---

### P0-4 CLI 接入 ExecutionSupervisor（或显式声明无保障）

**问题**

`ExecutionSupervisor` 只在 API/runtime-server 启动；CLI 本地路径没有 deadline/progress/审批超时巡检，本地长跑子 agent 缺少兜底。

**证据**

- 启动点仅 API：`api/skills/handler.go:4621-4624`；默认参数（enforce / 5s scan / 30m exec / 5m progress / 1h approval / 15s cancel grace / 2m store outage）`supervision/execution_supervisor.go:48-57`。
- `backend/cmd/aicli` 无 `ExecutionSupervisor`/`StartRun` 命中；本地路径只有 wake/preflight（`chat_actor_registry.go:50`、`chat_actor_host.go:2062`）。
- CLI 的 child 会话同样会走 `execution_run_hook.go:17-57` 的 run 注册（由 store 与 supervisor 协同），但没有 supervisor 消费这些 run。

**目标**

CLI 本地会话获得与 API 等价的执行超时治理，或至少在文档与 `/debug` 中明示“本地模式无巡检”。

**方案**

1. **接线（推荐）**：在 `chat_actor_host.go` 启动本地 supervisor 实例（复用同一 config 与 store；若本地无 durable store，则提供内存 store 实现或降级为仅审批/progress 超时）。
2. **降级模式**：若本地不想引入执行取消语义，至少实现“审批/进度超时 → 产生 wake/preflight 提醒”，不做强制 cancel。
3. **可见性**：`/debug` 输出当前 supervisor 状态（enabled/mode/scan interval/运行中 run 数/最近一次超时动作）。
4. **文档**：在 `docs/user-guide/aicli.md` 与 production-readiness 文档标注差异。

**实施注记（2026-09-13 复核）**：`docs/user-guide/aicli.md` §4.8 已补；`docs/plan/multi-agent-production-readiness-plan.md` 未见看门狗/差异标注（grep `ExecutionSupervisor|watchdog` 无命中），本方案第 4 条只完成一半。

**测试与验收**

- 单测：本地 supervisor 启动/关闭生命周期；超时触发提醒；模式=observe 时不打断执行。
- 手工验证：CLI 内 spawn 一个故意超时的子 agent，观察提醒/取消事件与 `/debug` 状态。
- 命令：`go test ./internal/supervision ./cmd/aicli/commands -run Supervisor -count=1`。

**兼容与风险**

- 本地启用 enforce 会改变既有“子 agent 可长时间运行”的行为；默认建议 `observe`（仅提醒）或沿用全局配置但加 CLI 覆盖项。
- 需确认本地 store 是否具备 supervision 所需的持久化能力（`Store` 接口的最小实现）。

---

### P1-5 子 agent 进度可观测（父流与前端下钻）

**问题**

父事件流与 Web UI 看不到子 agent 的工具进度/审批，仅能看到终态镜像；模型想了解进度必须显式 `read_agent_events`。

**证据**

- 事件流按 session 过滤：`session_runtime_stream.go:150-153`（live `tool.progress` 仅匹配当前 sessionID）。
- 父流只有终态镜像：`session_runtime_support.go:964-981`（`subagent.completed`）。
- 前端只消费父流 `subagent` 事件：`frontend/src/api/runtime/sse.ts:249-251`、`stream-handlers.ts:312-326`（"static SSE execution" artifact），`frontend/src` 内无 `parent_session_id` 入口（N7）。
- CLI 侧 `/agents panel` 已有 agent 图与状态（见 `multi-agent-production-readiness-plan.md` §2），可作为信息架构参考。

**目标**

Web UI 能在不污染父 transcript 的前提下查看子会话实时进度与审批；父模型可按需获取节流摘要。

**方案**

1. **前端下钻（首选）**：新增子会话视图（从父 trajectory 的 `subagent` 项或 artifacts 打开），数据源直接复用按 session 的 SSE/事件接口；不把子工具事件混入父流。
2. **父流节流镜像（可选）**：为每个子会话镜像 `subagent.progress`（tool name/status/耗时）到父流，服务端按窗口节流（例如 ≥2s 或状态变化才发），并在父 transcript 中折叠展示。
3. **模型可读摘要（可选）**：`read_agent_events` 支持 `view=tool_progress` 之类的过滤视图，减少父模型读原始事件的 token 成本。
4. **审批联动**：P0-2 的待审批清单同时进入前端（inline 审批入口）。

**测试与验收**

- 前端：打开子会话能看到与 CLI `/agents panel` 一致的实时工具事件；父 transcript 不被子事件污染（沿用 production-readiness §3 的隔离标准）。
- 服务端：节流窗口内合并事件，不丢终态；`read_agent_events` 过滤视图返回结构稳定。
- 命令：前端 `npm test -- trajectory`；后端 `go test ./internal/api/skills -run Stream -count=1`。

**兼容与风险**

- 需要明确“父子事件隔离”边界，避免破坏既有“父 console 不被 child 原始输出污染”的门禁。
- 节流镜像会新增事件类型，需同步前端与会话回放兼容。

---

### P1-6 终态通知与 auto-wake 预算治理

**问题**

成功终态不主动唤醒（设计取舍），但存在两个治理缺口：预算计数为进程内内存（N4），且所有 critical 事件共用同一 5 次/小时预算；密集失败时重要通知可能被推迟。

**证据**

- success → info/resolved 不排 wake：`supervision/projection.go:91-135`。
- 预算实现：`wake_scheduler.go:65-66`（内存 map）、`:240-259`（window=1h、max=5）。
- 父 busy 时挂起：`wake_consumer.go:36-40`（pending 队列）。
- 消费路径：父流镜像 + mailbox + preflight digest（`session_runtime_support.go:961,964-981,1060-1061,1135-1147`）。

**目标**

唤醒预算在进程间语义一致、可持久化；关键事件（审批、失败、超时）与普通失败类通知分层，不被小额预算吞掉。

**方案**

1. **预算分层**：将 wake reason 分为 `approval_required`、`execution_failed`、`execution_timeout`、`lifecycle_failed` 等类别；不同类别独立预算（例如审批不设硬上限但有去重，失败类沿用 5/h）。
2. **持久化预算（可选）**：把 `claims` 落到 supervision store（复用 `InsertWakePending` 所在表，新增 claim 记录），实现跨进程一致；至少提供“内存 or durable”配置项。
3. **去重增强**：同一 root scope + 同一 reason + 同一 target 在窗口内合并（现有 dedupKey 已按 root/parent/reason 合并 pending，需要扩展到 claim 计数）。
4. **父 turn 结束时的巡检兜底（可选）**：若父回合结束时仍存在 pending 子任务，允许触发一次“自检 turn”仅消费 mailbox/digest（不影响默认行为，开关控制）。→ 已实施，开关 `supervision.wake_self_check_per_window`（默认 0 关闭），行为口径见下文「P1-6 行为口径」。

**测试与验收**

- 单测：多进程/多实例共享 store 时预算一致；重启后不丢失计数；不同 reason 互不挤占。
- 集成：5 个失败 + 1 个审批场景中，审批通知必达。
- 命令：`go test ./internal/supervision -run "Wake|Rate|Budget" -count=1`。

**兼容与风险**

- 持久化 claim 会增加一次写放大，需评估 SQLite 压力（参考 `ANALYSIS-sqlite-lock-problem.md`）。
- 父 turn 结束自检若默认开启会改变交互节奏，建议默认关闭。

---

### P1-7 轮询工具软护栏与 next_action 统一

**问题**

轮询工具被 doom loop 豁免，防重复完全依赖文案；同时大量 agent 工具错误没有 next_action（N5），模型失败后缺少明确恢复动作。

**证据**

- 豁免：`agent/doom_loop.go:165-183`。
- next_action 现状：`session_runtime_support.go:884,2568` 仅 2 处；`:538,:1571,:2591,:2601` 等均为裸错误。
- 工具结果层已有 next_action 机制可复用：`toolbroker/types.go:531-571`、`toolresult/diagnostic.go`。

**目标**

模型的重复等待/重复读取有软性退避；所有 agent 工具错误都带可执行 next_action。

**方案**

1. **错误分类补齐**：为 spawn 相关错误定义错误码与 next_action 模板（depth/thread limit/busy/already exists/unknown path/storage unavailable），统一走 `runtimeerrors` 与 `toolresult` 渲染。
   - 线程数超限示例：`next_action=reuse_existing_child_or_close_idle_agent`（并返回 `active_children` 列表摘要）。
   - busy 示例：`next_action=wait_for_child_or_retry_with_interrupt`。
2. **轮询退避**：在 loop 层记录同一 `(tool, target_ids, timeout_ms)` 的连续调用；命中阈值时在结果中注入提示（不直接拒绝），例如“连续相同等待 3 次，建议改为做其他独立工作或增大 timeout”。
3. **read_agent_events 响应增强**：返回 `latest_seq`/`has_more`（当前已有 `latest_seq`，见父邮箱返回体的 `latest_seq` 字段）与“未读数量”；对相同 `after_seq` 的重复调用直接返回缓存摘要 + next_action。

**测试与验收**

- 单测：各错误分支的 next_action 存在且可解析；连续 wait 触发退避提示；`read_agent_events` 重复 after_seq 返回提示。
- 命令：`go test ./internal/toolbroker ./internal/api/skills ./internal/agent -run "NextAction|Doom|Poll" -count=1`。

**兼容与风险**

- 退避只提示不拦截，避免误伤合法的长轮询；阈值需可配置。

---

### P2-8 并发配额语义、驱逐与超限可操作性

**问题**

配额超限直接硬失败、无排队无驱逐；`maxThreads=0` 静默等于无限（N8）；超限错误没有 next_action（N5）；与 codex 的 LRU 驱逐相比，空闲子会话会长期占额。

**证据**

- 闸门实现：`session_runtime_support.go:2563-2604`；`MaxThreads<=0 → nil`（`:2572-2574`）；错误串 `:2591,:2601`。
- 配置语义：校验只拒负值（`config/manager.go:1065-1067`）；全零回退默认（`session_runtime_support.go:2612-2613`，CLI 同构 `chat_actor_registry.go:3113-3117`）。
- 跨进程原子性已有基础：`global_agent_store.go:366-369` 的 Reserve 在同一事务内计数建行。
- codex 对照：槽满且无可驱逐对象时返回 `AgentLimitReached`（`codex-rs/core/src/agent/control/residency.rs:93-100`），存在 LRU 驱逐能力。

**目标**

配额语义明确（0 不等于无限）、超限可操作、空闲子会话可被回收；不引入排队语义（保持快速失败）。

**方案**

1. **语义收敛**：将 `maxThreads<=0` 视为“使用默认值”而非“无限”；如需无限，要求显式配置 `maxThreads: -1`（校验单独放行）或新增 `unlimited: true`。
2. **错误增强**：超限错误附 next_action（`reuse_existing_child_or_close_idle` / `close_agent`）+ 当前 active 子会话摘要（path/status/idle 时长）。
3. **驱逐（可选，建议）**：spawn 超限时，若存在“终态但未 close”或“空闲超过 N 分钟”的子会话，自动 `close`（或标记 stale）后重试一次；仅对可安全回收的对象生效（不打断 running / waiting_approval）。
4. **审计联动**：回收动作写入事件流（`agent.reclaimed`），供前端/CLI 展示。

**测试与验收**

- 单测：`0` 走默认；`-1`（或 unlimited）不限制；超限错误含 next_action 与摘要；驱逐只作用终态/空闲对象。
- 并发用例：并发 spawn 不突破限额，失败释放后计数恢复。
- 命令：`go test ./internal/api/skills ./internal/agentcontrol -run "SpawnLimit|Quota" -count=1`。

**兼容与风险**

- “0 → 默认”是行为变更，需要配置迁移说明（`MIGRATION.md` / `docs/user-guide`）。
- 自动驱逐会关闭用户可能想保留的会话，默认建议只驱逐“终态未关闭”对象；空闲驱逐需显式开启。

---

### P2-9 生命周期回收与一致性对账

**问题**

普通 `spawn_agent` 子会话没有专属后台回收；TTL 只清理会话存储不同步 registry（N1/N3 的放大因素）；一致性审计存在但无调度、无修复。

**证据**

- 会话 TTL：`chat/manager.go:513-567`（含失败静默）。
- 审计只手动：`consistency_audit.go:37-40`；CLI debug 调用 `chat_debug.go:1292-1295,2132-2138`。
- team 有 reclaim（`team/orchestrator.go:623` 等），batch 有恢复（`agent/subagent_batch_coordinator.go:917`），普通 spawn 没有对应物。
- API supervisor 的 orphan 处理仅对已注册 run 生效（`execution_supervisor.go:329-379`），不覆盖“从未注册成功”的行。

**目标**

长期运行的部署中，registry active 集合与真实存活会话一致；漂移可自动发现并在安全范围内自动收敛。

**方案**

1. **周期性对账**：在 API 与 CLI 各启动一个低频（如 10min）审计任务：`AuditAgentSessionConsistency` → 对 `ACTIVE_AGENT_SESSION_MISSING` / `ACTIVE_AGENT_SESSION_TERMINAL` / `STALE` 的记录执行“标记 stale/closed”（可配置为仅告警）。
2. **TTL 联动**：`chat/manager.go` 删除会话时触发 registry 回调（或由对账兜底）；建议后者，避免 chat 包依赖 agentcontrol。
3. **普通 spawn 回收**：为终态子会话提供自动 close 策略（与 P2-8 驱逐共用开关），或至少在 `/agents panel` 提供一键清理。**已实施**两个分支：① 「一键清理」CLI `/agents cleanup [--dry-run] [--idle <时长>]`（`backend/cmd/aicli/commands/chat_agent_cleanup.go`）；② 「自动 close」——周期对账在审计后复用 P2-8 判定（`SweepAgentQuotaReclaim`），并把 CLI 投影 sweep 原先静默的终态关闭也改走同一路径，于是终态/容器消失的行在最迟一个对账周期内自动释放配额（observe 只报 `reclaim_candidates`，enforce 才落库），事件统一为 `agent.reclaimed` + `reclaimed:<reason>`、`source=reconcile`（详见「P2-9 行为口径」）。
4. **可见性**：`/debug`、`/agents panel`、HTTP supervision API 增加 `consistency_issues` 与最近一次对账时间。

**测试与验收**

- 单测：审计→收敛幂等；不会误标仍活跃/正在审批的会话。
- 场景：手工制造 `ACTIVE_AGENT_SESSION_MISSING`，对账后配额释放、状态变 stale。
- 命令：`go test ./internal/agentcontrol ./cmd/aicli/commands -run "Audit|Reconcile" -count=1`。

**兼容与风险**

- 自动收敛默认建议 `observe`（只报告），确认无误后再开 `enforce`。
- 多进程同时对账需幂等（用条件更新 `closed_at IS NULL` 保证）。

---

### P2-10 测试与验证补齐

**问题**

以下语义缺少或仅部分有测试固化：同 turn 多 spawn 的串行行为与配额、spawn 后同 turn 继续工作再 wait、失败回滚、wait 越界、审批通知、并发配额竞态。真实验证脚本覆盖 `spawn_agent→wait→read` 与 `spawn_team→wait_team`，但未覆盖上述新增场景。

**证据**

- 已有：批量 wait（`broker_agent_test.go:1046-1074`）、timeout 后继续独立工作（`:1108-1123`）、审批 next_action（`:1125-1146`）、连续 spawn（`session_agent_controller_test.go:384-386`）。
- 并行门控：broker 工具整批串行（`agent/tool_parallel_scheduler.go:63-68`）。
- 真实验证脚本：`scripts/validate-multi-agent-real-terminal.ps1`（`multi-agent-production-readiness-plan.md:42` 记载的 probe 范围）。

**目标**

把本轮新增行为的期望固化成可回归测试，并扩展真实终端 probe。

**方案**

1. 单测/集成补充：
   - spawn 失败回滚（P0-3 故障注入）；
   - wait/read 越界（P0-1）；
   - 审批/提问通知与去重（P0-2）；
   - 同 turn 多个 `spawn_agent`（断言串行 + 配额逐个占用）；
   - spawn → 同 turn 其他工具 → wait 的完整链路（断言父不被阻塞）。
2. 并发压测：N 个 goroutine 并发 spawn/close，断言 active 计数守恒、无死锁、无泄漏。
3. 真实终端 runbook 扩展：在 `scripts/validate-multi-agent-real-terminal.ps1` 增加“审批超时唤醒”“配额回滚”“wait clamp”三个 probe，并在 `docs/working/` 记录结果。**未实施**（需要真实交互终端，与 P0-4 待补项合并）。

**已实施（本轮，2026-09-13）**

1. **spawn 失败回滚（P0-3 故障注入）**：`backend/internal/api/skills/session_agent_spawn_rollback_test.go` 用「hub 无 actor factory」注入 `GetOrCreate` 失败（`Spawn` 中预留成功之后唯一不依赖外部环境即可稳定触发的失败点），断言预留行落 `stale` 终态且 `Closed()`、半成品子会话被删除、active 集合只剩 root 行。
2. **并发配额守恒（压测）**：`backend/internal/agentcontrol/concurrency_stress_test.go` 8 goroutine × 6 轮并发 reserve，交替走 `release`（回滚路径 → stale）与 `close`（正常关闭 → closed），结束后断言 24 行 stale + 24 行 closed、active 只剩 root、同一 `limit` 仍可重新预留（无配额泄漏、无死锁；清单只重试 `agent spawn thread limit reached`，用 5ms 短退避消化争抢，实测 0.46s）。
3. **同 turn 多 spawn 串行语义（N6 固化）**：`backend/internal/agent/tool_parallel_scheduler_test.go` 的 `TestParallelToolBatchPlan_RejectsBrokerTools` 扩为表驱动，覆盖 `spawn_agent` / `wait_agent` / `close_agent` / `read_agent_events` / `spawn_team` 与只读工具的混合批次，并含「两个 `spawn_agent` 同批仍串行」用例，把“broker 工具整批回退串行、配额逐个预留”固化成回归门禁。
4. **既有覆盖（本轮未重复造）**：wait/read 越界（P0-1）、审批 next_action、批量 wait 与 timeout 后继续独立工作（`backend/internal/toolbroker/broker_agent_test.go:1046-1146`）、registry release 幂等（`global_agent_store_test.go:445-465`）、跨实例并发预留限额（`registry_service_test.go:220`）。

**验收**

- 新增用例全部通过，且不改变既有行为；
- `go test ./internal/api/skills ./internal/toolbroker ./internal/supervision ./internal/agentcontrol ./cmd/aicli/commands -count=1` 全绿；
- 真实 probe 输出归档到 `docs/working/multi-agent-real-terminal-validation-*.md`。

**风险**

- 涉及时间/超时的用例需可注入时钟或使用短超时配置，避免 CI 不稳定。

---

### P2-11 协作引导：prompt 与工具描述双层

**问题**

系统提示词层没有多 agent 协作指导，引导只存在于工具描述与 next_action 文案；模型容易“spawn 后立刻死等”或忽略“先做独立工作”。

**证据**

- `backend/internal/prompt`（11 个文件）grep `wait_agent|spawn_agent|independent work` 无命中。
- 现有引导在工具描述与结果文案：`toolbroker/types.go:531-571`、`broker.go` 中 spawn/wait 描述；与本会话工具契约中“spawn 后先做非重叠工作”的表述一致。
- codex 做法：在全局规格段落里明确“While the subagent is running in the background, do meaningful non-overlapping work immediately”（`codex-rs/core/src/tools/handlers/multi_agents_spec.rs:737`），并在工具描述中提示“prefer longer waits”（`:863`）。

**目标**

在 prompt 与工具描述两处给出一致、可执行的协作规范。

**方案**

1. 在 `backend/internal/prompt` 增加可选“协作指引”段落（受配置开关控制，默认开启；仅当 broker 工具可见时注入）：
   - spawn 后优先做非重叠工作，不要立即长时间 wait；
   - 等待用较大 timeout（分钟级）而非短轮询；
   - 子 agent 完成会自动进入 mailbox/下一个 turn，无需反复 `read_agent_events`；
   - 审批/提问：父负责 resolve，先看 preflight 的待审批清单。
2. 工具描述与 next_action 文案复用同一份常量，避免多处漂移（可在 `toolbroker` 暴露文案常量供 prompt 层引用）。
3. 对 `wait_agent` 描述补充“timeout 上下界与默认值”说明（与 P0-1 联动）。

**实施注记（2026-09-13 复核）**：方案 1 已落地（`backend/internal/prompt/environment_context.go` 的协作段落 + API/CLI 双宿主注入）；**方案 2（共享文案常量）与方案 3（`wait_agent` 描述补上下界/默认值）未实施**——工具描述仍是字面量、只有 `Optional wait timeout in milliseconds.`（`backend/internal/toolbroker/broker.go:417,591`，两处变体各自复制文案，漂移风险仍在）。因此本条按“部分实施”计，“双层齐备”的说法不成立。

**测试与验收**

- 快照测试：prompt 组装输出包含/不包含该段落的两种配置。
- 行为验证：在同一任务下对比开启前后 `wait_agent` 调用次数/等待时长（定性即可）。
- 命令：`go test ./internal/prompt -count=1`。

**风险**

- 指引过多会挤占上下文；建议控制在 8-12 行，并允许项目级配置关闭。

---

### P2-12 监督通知闭环：给本地宿主补上 ack/control 通道（或收敛动作宣告）

**问题**

digest/preflight 能持续投递未决通知，但 CLI/本地宿主没有任何 `acknowledge`/`defer`/`cancel`/`close` 的执行入口；HTTP 动作面只作用于服务端进程自己的 store。结果是 failure 类 critical 通知永久停在 `unresolved`，并在每个后续 turn 重复注入（N9）。

**证据**

- 通知与动作集合：见 N9 的 durable 行；`allowed_actions_json` 与注入摘要宣告的集合不一致。
- 工具面缺失：`backend/internal/toolbroker/broker.go:29-55`；设计稿提案：`docs/plan/spawn-agent-team-supervision-timeout-recovery-plan.md:878-890,1465-1475`。
- 服务端已实现、可直接复用：路由 `backend/internal/api/skills/handler.go:840-852`；ack/defer 处理器 `supervision_handlers.go:397-425` 起（CAS `expected_version`，冲突 409）；动作服务 `internal/supervision/action_service.go:84-96,151-154,477-519`；store 状态更新 `sqlite_store.go:531,846`。
- 收敛现状：非 critical 行已被投影标成 `resolution_state=closed`（`resolved_at` 仍为空，说明来自 Upsert 而非 Resolve）；critical 行因需要人工决策而停在 `unresolved`。

**目标**

1. preflight 宣告的每个动作（inspect/acknowledge/defer/cancel/close）在宿主侧都有真实入口；
2. 对象已终态或已消失的通知可安全收敛，不再永久占用 `critical_unresolved`；
3. 动作集合口径唯一：由同一策略按“宿主能力”计算，避免摘要与 durable 行两套说法。

**方案（按成本递增）**

1. **口径修正（先做）**：preflight/digest 的动作集合按宿主能力过滤，只宣告存在入口的动作；其余保留 `inspect` 并附 `next_action`（如 `ack_requires_local_command`），避免模型规划不存在的步骤。
2. **CLI 命令入口（低风险，建议先落地）**：在 `aicli` 增加 `/debug supervision` 子命令组：`list`、`ack <notification_id> --note`、`defer <id> --until`、`action cancel|close --subject-kind ... --subject-id ... --expected-version ...`。CLI 进程已直接持有同一 store 文件，复用 `internal/supervision` 的 store + `ActionService` 即可，不需要新协议或新服务。
3. **模型工具入口（与设计稿对齐）**：实现 `supervision_snapshot`（只读）与 `ack_lifecycle`/`control_descendant`（写）并注册进 toolbroker；约束：`requested_by` 绑定当前父会话、仅允许操作 `root_scope_id == 当前会话` 的 subject、写动作必须带 `expected_version` 与 `reason`。
4. **终态自动收敛（与 P2-9 联动）**：subject 已达终态且 `severity != critical` 时维持自动 `closed`；对 critical 行不自动 close，但增加“标的缺失”判定 —— subject 在对应控制面 store 中不再存在时标记 `resolution=stale`，从 `critical_unresolved` 计数中剔除并降级为 info 提示（本轮 `batch_52d54fa3c2ec843b` 正是这种：通知仍在，batch 行已不存在）。

**实施注记（2026-09-13 复核）**：方案 3（模型工具 `supervision_snapshot` / `ack_lifecycle` / `control_descendant`）与方案 4（stale 判定）已落地；方案 2 只落了 `list|ack|defer|resolve`（`backend/cmd/aicli/commands/chat_debug_supervision.go:302-313`），**`action cancel|close` 子命令未实现**；方案 1（动作集合按宿主能力过滤）**未实施**——`Evaluator.EvaluateAllowedActions` 仍只接收 `Notification`、无宿主能力输入（`backend/internal/supervision/evaluator.go:25-50`），全仓 grep `ack_requires_local_command` 只命中测试名。本条按“部分实施”计。

**测试与验收**

- 单测：动作集合的能力过滤；ack/defer 的 CAS（重复 ack、过期 version → 冲突错误码）；`stale` 判定（标的缺失后不再计入 critical）。
- CLI 集成：`/debug supervision list` 能复现两条 critical；`ack` 后 preflight 的 `critical_unresolved` 递减；`defer --until` 未到期不再注入、到期恢复注入。
- 命令：`go test ./internal/supervision ./internal/toolbroker ./cmd/aicli/commands -count=1`。

**兼容与风险**

- 防“误消警”：ack 仅允许作用于终态或已复核的 subject；必须带 `note`，并保留 `acknowledged_at` 供审计。
- 批量收敛需收窄：`action_service.go:457-471` 目前会把同 scope 下全部 `unresolved` 通知一起 resolve，接入模型工具前建议改为只收敛与本次动作同一 subject 的通知。
- 多进程 store 各自独立（N9 实测），若后续要求跨进程统一视图，需要把 supervision store 收敛到单一服务；本文不把它作为前置条件。

---

## 6. 不建议回退的能力

以下能力是本项目相对 codex 的差异化优势，优化过程中不应为“对齐参考实现”而删减：

1. **`wait_agent` 多 id + 完整 snapshot**：一次等待即可获得全部子会话状态（`session_runtime_support.go:1662-1700`），codex v2 只返回 `{message,timed_out}`（`codex-rs/core/src/tools/handlers/multi_agents_spec.rs:288`）。
2. **`read_agent_events`**：`after_seq` 增量、可读子工具/审批事件（`:1846-1924`），codex 无等价工具。
3. **`waiting_approval` 作为 ready 状态 + `resolve_agent_approval`**：父可经协作工具处理子审批（`:1596-1631,2925-2932`），codex 的 `AgentStatus` 没有等待审批变体（`protocol/src/protocol.rs:1731-1747`）。
4. **事件唤醒 + 500ms 兜底 ticker**：父不依赖密集轮询（`:1701-1707`），比纯 watch 更抗漏事件。
5. **跨进程原子预留**：`ReserveAgentControlAgentSpawn` 在单事务内完成计数与建行（`global_agent_store.go:366-369`），是配额正确性的基础，P0-3/P2-8 只做增强不做替换。

---

## 7. 实施路线图

| 批次 | 内容 | 依赖 | 粗估 | 完成标志 |
| --- | --- | --- | --- | --- |
| Batch 1（正确性） | P0-1 wait 边界、P0-2 审批通知、P0-3 失败回滚 | 无 | 2–4 人日 | 单测 + 故障注入全绿；无配额僵尸 |
| Batch 2（体验与治理） | P0-4 CLI supervisor、P1-5 子会话可观测、P1-6 预算治理、P1-7 next_action/护栏 | P0-2（事件分类） | 4–6 人日 | `/debug` 可见 supervisor；前端可下钻；错误全部带 next_action |
| Batch 3（资源与验证） | P2-8 配额语义/驱逐、P2-9 对账、P2-10 测试与真实 probe、P2-11 协作引导、P2-12 监督通知闭环 | P0-3、Batch 1/2 稳定 | 5–7 人日 | 长跑无漂移；runbook 四个新 probe 通过；preflight 的 critical 项可被本地宿主收敛 |

**完成标志复核（2026-09-13）**：Batch 1/2 标志基本达成（Batch 1 的“无配额僵尸”已由补偿 + API 级故障注入用例验证；`SubmitPromptAsync`/CLI/并发失败注入用例与 `snapshot` 分支口径仍待补，见 P0-3 实施注记）；“runbook 四个新 probe 通过”**未达成**（`scripts/validate-multi-agent-real-terminal.ps1` 尚无 wait clamp / spawn 失败注入 / 审批 digest / supervision ack 四类 probe）；“preflight 的 critical 项可被本地宿主收敛”已由 P2-12 的 CLI `list|ack|defer|resolve` 与 stale 判定达成（模型侧写动作为 `ack_lifecycle` / `control_descendant`）。

批次内建议顺序：P0-3 → P0-1 → P0-2（回滚修复收益最直接、风险最低）。

---

## 8. 验证与回归

**专项命令（backend 目录）**

```powershell
go test ./internal/api/skills ./internal/toolbroker ./internal/supervision ./internal/agentcontrol -count=1
go test ./cmd/aicli/commands -count=1
go test ./internal/agent -run "Parallel|Doom" -count=1
```

**真实终端验证（在现有 runbook 上扩展）**

- 基线：`scripts/validate-multi-agent-real-terminal.ps1`（已有 `spawn_agent→wait_agent→read_agent_events`、`spawn_team→wait_team` probe）。
- 新增 probe：
  1. wait 越界（`timeout_ms=1` 与 `>max`）→ 断言 effective/clamped 行为；
  2. spawn 失败注入（不存在的 provider/存储错误）→ 断言无残留 active 行；
  3. 子 agent 审批 → 断言父下一 turn 的 digest 必含待审批项。
  4. supervision 闭环：`/debug supervision list` 复现 critical 项 → `ack` → 断言 preflight 的 `critical_unresolved` 递减且该条目不再重复注入。
- 结果归档到 `docs/working/multi-agent-real-terminal-validation-<date>.md`。

**前端手工检查**

- 子会话视图可见实时工具进度与审批；父 transcript 无子原始输出污染；
- 审批在 UI 处理一次后，digest 不再重复出现。

---

## 9. 风险与回滚

| 风险 | 说明 | 缓解 |
| --- | --- | --- |
| 配置语义变更 | P0-1 clamp、P2-8 “0=默认”会改变既有行为 | 新字段全部可选；`MIGRATION.md` 记录；第一版越界报错而非静默截断 |
| 通知放大 | P0-2/P1-6 主动唤醒可能增加 turn 次数 | 去重 + 分层预算；wake 可配置、默认保守 |
| 自动收敛误伤 | P2-9 自动标 stale/close 可能关闭仍在使用的会话 | 默认 `observe`，仅报告；enforce 需显式开启 |
| 自动驱逐误伤 | P2-8 自动 close 空闲子会话 | 默认只驱逐“终态未关闭”；空闲驱逐需显式开启 |
| SQLite 压力 | 持久化 wake 预算/对账增加写入 | 复用现有 store，批量/低频执行；参考 `ANALYSIS-sqlite-lock-problem.md` |

回滚策略：所有新增行为置于配置开关后（`agents.waitTimeoutMode`、`supervision.wakeBudgetMode`、`agentRegistry.autoReconcileMode` 等），关闭即回到当前行为。

---

## 10. 实施记录（2026-09-13）

状态口径：**已实施** = 代码与定向测试均在本工作树落地；**部分** = 主路径已落地，周边（可视化/文档/真实验证）待补。

| 项 | 状态 | 代码落点 | 定向测试 |
| --- | --- | --- | --- |
| P0-1 wait 超时边界 | 已实施 | 公共解析 `backend/internal/agentcontrol/wait_timeout.go`；API 接入 `backend/internal/api/skills/session_runtime_support.go:1843,1907,2058`；CLI 接入 `backend/cmd/aicli/commands/chat_actor_registry.go:2300,2364,2508`；结果回显 `backend/internal/toolbroker/types.go:492-493`（`wait_timeout_requested_ms` / `wait_timeout_clamped`） | `go test ./internal/agentcontrol -run WaitTimeout -count=1` |
| P0-2 审批/提问通知 | 已实施 | 投影层 `backend/internal/supervision/approval_projection.go`；digest 内联 `backend/internal/supervision/digest.go:194-199`；API 桥接 `session_runtime_support.go:937-1155`；CLI 桥接 `chat_actor_registry.go:1029-1197` | `go test ./internal/supervision ./internal/api/skills ./cmd/aicli/commands -run "Approval\|Digest\|Wake" -count=1` |
| P0-3 spawn 失败回滚与配额一致性 | 部分实施（补偿覆盖 actor/prompt 失败；`snapshot` 分支口径未定，`SubmitPromptAsync`/CLI/并发失败注入用例未补，见 §5 实施注记） | 补偿事务 API `session_runtime_support.go:584-612`、`releaseAgentSpawnReservation:1286-1325`；CLI `chat_actor_registry.go:696-711,1287-1325`；Store API `backend/internal/agentcontrol/agent_registry.go:249-258`、`global_agent_store.go:367-485`；stale 预检下沉 `backend/internal/agentcontrol/binding.go` | `go test ./internal/agentcontrol -run "ReleaseSpawn\|CloseStaleAgentSessionBindings" -count=1` |
| P0-4 CLI 接入 ExecutionSupervisor | 已实施（真实 probe 与 production-readiness 文档标注待补） | 看门狗构建 `backend/cmd/aicli/commands/chat_actor_execution_supervisor.go`（`peekLocalExecutionSupervisor` 只读入口）；broker 接线 `chat_actor_host.go:1325,1339-1345`；快照 `backend/internal/supervision/execution_supervisor.go`（`ExecutionSupervisorStats`/`Stats()`）；可见性 `/debug supervision watchdog` `backend/cmd/aicli/commands/chat_debug_supervisor.go`（路由 `chat_debug_supervision.go`）；用户手册 `docs/user-guide/aicli.md` §4.8 | `go test ./internal/supervision -run "Stats" -count=1`；`go test ./cmd/aicli/commands -run "ChatDebugSupervisionWatchdog" -count=1` |
| P1-6 auto-wake 预算治理 | 已实施 | 分级账本 `backend/internal/supervision/wake_budget.go:49-65,86,108`（reason→类别映射、`WakeBudgetState`、`FormatWakeBudgetLine`；`WakeBudgetMode` 定义在 `wake_scheduler.go:34-46`）；配置面 `config.go:31-38,79-95`（`wake_max_approval_wake` / `wake_budget_mode`，默认 memory）；claim 行 `types.go:275`；Store API `store.go:58-69`；SQLite 表与索引 `sqlite_store.go:251-261`，读写 `:1219,1254,1270`；调度器 `wake_scheduler.go:115,215,352,407`（按类计费、durable 窗口计数、机会式保留）；CLI 展示 `backend/cmd/aicli/commands/chat_debug_supervision.go:359-393` | `go test ./internal/supervision -run "Wake\|Budget\|Claim" -count=1`；`go test ./cmd/aicli/commands -run "ChatDebugSupervision" -count=1` |
| P1-7 轮询软护栏与 next_action 统一 | 已实施 | 错误码 `backend/internal/errors/codes.go:73,76`（`AGENT_THREAD_LIMIT`、`AGENT_REGISTRY_UNAVAILABLE`）；分类与文案 `backend/internal/toolresult/diagnostic.go:2013-2027,2099-2100,2196-2199`；轮询退避 `backend/internal/agent/polling_guard.go`；loop 接线 `backend/internal/agent/loop.go:504-506,875-888`；提醒类型 `backend/internal/agent/system_reminder.go:38`；读窗口元数据 `backend/internal/toolbroker/types.go`（`ApplyAgentEventsPagination`/`CapAgentEventsUnread`）、工具描述 `backend/internal/toolbroker/broker.go:423,597`、CLI 本地 host 计数 `backend/cmd/aicli/commands/chat_actor_registry.go:3843-3846` | `go test ./internal/agent -run "PollingBackoff" -count=1`；`go test ./internal/toolresult -run Diagnose -count=1`；`go test ./internal/toolbroker -run AgentEventsPagination -count=1` |
| P2-12 监督通知闭环（本地宿主） | 部分实施（方案 1 能力过滤与 CLI `action cancel|close` 未做，见 §5 实施注记） | stale 判定 `backend/internal/supervision/digest.go:23-35,120-131`；API 宿主 presence `backend/internal/api/skills/supervision_handlers.go:168-215`（digest 接线 `:253,298`）；CLI 宿主 presence `backend/cmd/aicli/commands/chat_actor_host.go:2085-2131,2160`；CLI 控制入口 `backend/cmd/aicli/commands/chat_debug_supervision.go`，分发接线 `chat_debug_archive.go:73-86`、`chat_command_result.go:695-704`；模型工具入口 `backend/internal/toolbroker/supervision_tools.go`（派发 `broker.go:1706-1707`、定义门控 `:196-202`）、宿主控制器 `backend/cmd/aicli/commands/chat_supervision_tools.go`（复用 `backend/internal/supervision/local_control.go`），装配点 `chat_actor_host.go:1360-1367` | `go test ./internal/supervision -run "Digest" -count=1`；`go test ./cmd/aicli/commands -run "ChatDebugSupervision\|LocalSupervisionSubjectPresence\|LocalSupervisionToolController" -count=1`；`go test ./internal/toolbroker -run "Supervision\|AckLifecycle\|ControlDescendant" -count=1 -v` |
| P2-9 生命周期回收与一致性对账 | 已实施（对账循环 + 手动清理入口 + 终态/空闲自动 close；TTL 删除走对账兜底） | 对账器 `backend/internal/agentcontrol/reconciler.go`（`Reconciler` / `RunLoop` / `ReconcileSummary`，复用 `reconcile.go`、`consistency_audit.go`）；配置面 `backend/internal/config/manager.go`（`agents.registryReconcileInterval` / `registryReconcileMode`，默认 10m / observe）；CLI 宿主 `backend/cmd/aicli/commands/chat_actor_reconcile.go`（装配点 `chat_actor_host.go` 的 `startLocalRegistryReconcile`）；API 宿主 `backend/internal/api/skills/agent_registry_reconcile.go`（接线 `refreshAgentControlRegistryService` / `refreshAgentControlAgentStore`）；可见性 `/debug` + `/agents panel`（`backend/cmd/aicli/commands/chat_debug.go`）与 HTTP supervision API（`backend/internal/api/skills/supervision_handlers.go` 的 snapshot/digest 响应）；手动清理入口 `backend/cmd/aicli/commands/chat_agent_cleanup.go`（`/agents cleanup [--dry-run] [--idle <时长>]`，复用 spawn 闸门同一决策链 `QuotaChildren` → `observeLocalQuotaChildren` → `SelectReclaimable` → `ReclaimAgentQuota`），分发接线 `backend/cmd/aicli/commands/chat_debug.go:239,277`；自动 close 共享驱动器 `backend/internal/agentcontrol/reclaim.go`（`EvaluateReclaimOutcome` / `QuotaRoots` / `SweepAgentQuotaReclaim` / `ReclaimObserver` / `ReclaimedSink` / `AppendReclaimReason`）与 `agentcontrol.Reconciler.Reclaim` 钩子（`reconciler.go`；报告新增 `reclaim_candidates` / `reclaimed` / `reclaimed_rows` / `reclaim_failed` / `reclaim_reasons` / `reclaim_error`）；CLI 钩子 `backend/cmd/aicli/commands/chat_agent_reconcile_reclaim.go`（装配 `chat_actor_reconcile.go`）、投影 sweep 改造 `backend/cmd/aicli/commands/chat_actor_registry.go:1594-1680`，API 钩子 `backend/internal/api/skills/agent_registry_reconcile.go:87-122`；事件来源 `backend/internal/agentcontrol/reclaim_events.go` 的 `ReclaimSourceReconcile` | `go test ./internal/agentcontrol -run "Reclaim|QuotaRoots|AppendReclaimReason|Reconciler" -count=1`；`go test ./cmd/aicli/commands -run "LocalRegistryReconcile" -count=1`；`go test ./internal/api/skills -run "AgentRegistryReconcile" -count=1`；`go test ./cmd/aicli/commands -run "ChatAgentCleanup|StructuredAgentsCleanup" -count=1`；`go test ./cmd/aicli/commands -run "LocalReconcileReclaim|LocalRegistryReconciler|LocalRegistrySweep" -count=1` |
| P2-8 并发配额语义、驱逐与超限可操作性 | 已实施 | 决策与文案 `backend/internal/agentcontrol/reclaim.go`（`ResolveMaxThreads:28`、`Reclaimable`/`SelectReclaimable:179`、`ThreadLimitMessage:272`、`AgentReclaimStore:292`、`ReclaimAgentQuota:344`）；store 驱逐 `backend/internal/agentcontrol/global_agent_store.go:742-748`（`reclaimed:<reason>`）、预留期超限文案 `:425`；配置面 `backend/internal/config/manager.go:112,1105`（`agents.reclaimIdleMs`；`maxThreads: -1` = 显式不限）；API 闸门 `backend/internal/api/skills/session_runtime_support.go:2755-2860`（`observeQuotaChildren:2833`）；CLI 闸门 `backend/cmd/aicli/commands/chat_actor_registry.go:3204-3279`（`observeLocalQuotaChildren:3285`）；配置迁移说明 `MIGRATION.md` §9（`maxThreads` 三态、`reclaimIdleMs`、`registryReconcile*`、`agent.maxRepeatedPollCalls`）；产品事件 `backend/internal/agentcontrol/reclaim_events.go`（`EventAgentReclaimed`、`ReclaimSourceSpawnGate`/`ReclaimSourceManualCleanup`、`ReclaimEventPayload` 稳定键 `source/reclaimed/summary/reasons/agent_paths`）；持久化白名单 `backend/internal/api/skills/handler.go:3754`；前端轨迹白名单与摘要 `frontend/src/lib/trajectory/recovery.ts`、`frontend/src/lib/trajectory/trajectory-reducer/event-readers.ts`（`agent.reclaimed` 分支 + `readStringArray`） | `go test ./internal/agentcontrol -run "Reclaim\|ThreadLimit\|MaxThreads" -count=1`；`go test ./internal/config -run AgentsConfig -count=1`；`go test ./internal/api/skills -run "ShouldPersistRuntimeSessionEvent\|SpawnGatePublishesReclaimEvent" -count=1`；`go test ./cmd/aicli/commands -run "LocalActorRegistry_Quota\|LocalActorRegistry_Reclaims\|ReclaimEvent" -count=1`；`npx vitest run src/lib/trajectory/recovery.test.ts src/lib/trajectory/trajectory-reducer.events.test.ts`（frontend 目录） |
| P2-11 协作引导（prompt 层） | 部分实施（prompt 层已实施；工具描述层方案 2/3 未做） | 渲染器 `backend/internal/prompt/environment_context.go:236-254`（`RenderMultiAgentCollaborationGuidance`，段落头 `Multi-agent collaboration guidance:`，7 条纪律）；API 宿主注入 `backend/internal/api/skills/instruction_messages.go:118-166`（`withDelegationGuidance:127` = difficulty + collaboration，`withInstructionGuidance:143` 幂等判定，接线 `buildRuntimeInstructionMessages:11-18`，覆盖 handler/子代理/调试三处调用）；CLI 宿主注入 `backend/cmd/aicli/commands/chat_session.go:1590-1592`（`composeDurableChatSystemPromptWithGuidanceForCWD`） | `go test ./internal/prompt -run "MultiAgentCollaboration" -count=1`；`go test ./internal/api/skills -run "InstructionMessages" -count=1`；`go test ./cmd/aicli/commands -run "TestComposeLocalChatSystemPrompt_IncludesWorkspaceGuidance" -count=1` |
| P1-5 子 agent 进度可观测（方案 1 前端下钻 + 方案 2 父流镜像 + 方案 3 过滤视图 + 方案 4 inline 审批） | 已实施（方案 1、方案 2、方案 3、方案 4） | 下钻对话框 `frontend/src/components/workspace/trajectory/subagent-session-dialog.tsx`（入口解析 `subagentSessionTarget`，行渲染 `SubagentSessionRows`，Esc/刷新/状态条）；独立实时流 `frontend/src/hooks/workspace/use-subagent-session.ts`（`GET /runtime/events` 分页回填 → `GET /runtime/stream?live=1&after=<cursor>` 跟随；独立 `TrajectoryStore`，断线按游标退避重连，`session_end`/`chat.sse.done` 收敛为 closed）；live-only 进度折叠 `frontend/src/lib/trajectory/recovery.ts`（`TOOL_PROGRESS_EVENT_TYPE` / `toolProgressEventToTrajectoryPush`，`tool.progress` → kind=`tool_call`、`_event.sequence=0`，与 `chat.sse.tool_start/tool_end` 折叠为同一 `tool:<call_id>` 行；终态后到达由 `trajectory-reducer/snapshot-ops.ts:86` 终态冻结忽略）；入口接线 `trajectory-detail-panel.tsx:110`（`data-open-subagent-session`）与 `trajectory-view.tsx`（`subagentTarget` state + dialog 挂载）；过滤视图 `backend/internal/toolbroker/types.go`（`AgentEventsViewToolProgress` / `NormalizeAgentEventsView` / `KeepsAgentEventForView` / `ApplyAgentEventsView`：`view=tool_progress` 只保留 `tool.*` 与终态/审批事件，回报 `view`/`filtered`）与接线 `backend/internal/toolbroker/broker.go`（`view` 参数解析 + 投影、两处 tool schema/描述同步）；协作引导 `backend/internal/prompt/environment_context.go:247`（建议优先用 `view=tool_progress`）；**方案 2 父流节流镜像**：镜像器 `backend/internal/supervision/subagent_progress.go`（`EventTypeSubagentProgress`、`DefaultSubagentProgressWindow=2s`、`SubagentProgressMirror.Observe`：leading-edge 窗口合并 + 状态变化穿透 + `Forget`/stale/512 上限剪枝），API 接线 `backend/internal/api/skills/session_runtime_support.go`（`subscribeAgentCompletion` 内 `progressTarget`/`progressMirror`，仅 `bus.Publish` 不落库，会话结束时 `Forget`），live-only 通道 `backend/internal/api/skills/session_runtime_stream.go`（`sessionLiveOnlyRuntimeEventTypes` / `isSessionLiveOnlyRuntimeEvent` 纳入 `tool.progress` + `subagent.progress`）；前端折叠行 `frontend/src/lib/trajectory/recovery.ts`（`SUBAGENT_PROGRESS_EVENT_TYPE` / `subagentProgressEventToTrajectoryPush`：`runtime_type` 行、`_event.sequence=0`、`path`→`agent_path`）与摘要 `frontend/src/lib/trajectory/trajectory-reducer/event-readers.ts`（`subagent.progress` → `agent progress: <path> <tool> <state> …`）、非终态 live 行 `frontend/src/lib/trajectory/trajectory-reducer/apply.ts`（`live:true` 用 `running`，否则 `runtime-0` 被 `snapshot-ops.ts:86` 终态冻结后再不更新） | `npx vitest run src/components/workspace/trajectory/subagent-session-dialog.test.tsx`（12 例：目标解析、回填+live 跟随、实时追加、`tool.progress` 折叠、inline 审批（请求条渲染、批准/拒绝打 `approve_tool`、失败保留入口、`approval_resolved` 精确清除）、Esc/刷新、错误与重连、TrajectoryView 入口且父 store 不被写入）；`npx vitest run src/lib/trajectory`（96 例，含新增：镜像映射/无主子行拒收、摘要四态、`runtime-0` 单行就地更新且保持 `running`）；`go test ./internal/supervision -run TestSubagentProgressMirror -count=1`；`go test ./internal/api/skills -run "APIAgentProgressMirror|StreamSessionRuntimeEvents" -count=1`；`go test ./internal/toolbroker -run "AgentEventsView\|KeepsAgentEvent\|ReadAgentEvents" -count=1`；`go test ./internal/prompt -count=1`；`npx vitest run src/hooks/workspace/use-subagent-session.test.ts src/api/runtime/sessions.test.ts`（7 例：pending 审批状态机与 `approve_tool` 命令体/`patched_args`/错误透传） |

**剩余项（主体已实施；仅剩真机 probe 与下列部分实施项）**

- P0-3 spawn 失败回滚与配额一致性：**部分实施**——补偿覆盖 actor/prompt 失败（`ReleaseAgentControlAgentSpawn` + API 侧 stale 预检已下沉），API 级故障注入用例已补；**未做/未决**：`snapshot` 失败分支是否回滚口径未定，`SubmitPromptAsync`/CLI/并发达上限故障注入用例未补（见 §5 P0-3 实施注记）。
- P0-4 CLI 接入 ExecutionSupervisor：**已实施（probe 与文档标注待补）**——本地看门狗默认 observe、`/debug supervision watchdog` 可见；缺真实终端 probe 与 production-readiness 文档差异标注（见 §5 P0-4 实施注记）。
- P1-5 子 agent 进度可观测：**四个方案全部实施**——方案 1（前端下钻）、方案 2（父流节流镜像 `subagent.progress`）、方案 3（`read_agent_events` 过滤视图 `view=tool_progress`）与方案 4（前端 inline 审批：下钻对话框内直接批准/拒绝，动作走 actor `approve_tool` 命令）（详见 §10 行与「P1-5 行为口径」）。**未做**的是真机验证（真实子 agent 长跑时下钻的滚动/重连手感、父流镜像行的观感与节流强度、活体审批的往返延迟）。
- P2-8 并发配额语义/驱逐/超限可操作：**已实施**（语义收敛、超限文案带 next_action 与 occupants、store 级驱逐与 `reclaimed:<reason>` 事件区分；配置迁移说明已补入 `MIGRATION.md` §9，覆盖 `maxThreads` 三态、`reclaimIdleMs`、`registryReconcile*`、`agent.maxRepeatedPollCalls`）。① 回收动作的产品事件流已实施：`agent.reclaimed`（`agentcontrol` 侧 `reclaim_events.go`，稳定键 `source/reclaimed/summary/reasons/agent_paths`；spawn 闸门与 `/agents cleanup` 双来源）经持久化白名单进入前端轨迹流，父流渲染为一行 system note，`/debug` 侧经同一事件流可见。**未做**的是 ② 真机长跑 probe（并发 spawn 不越限、空闲驱逐只作用于可安全回收对象）。
- P2-9 生命周期回收与一致性对账：周期审计 + 可配置自动收敛（observe/enforce）已在 API 与 CLI 双宿主落地，**手动清理入口 `/agents cleanup` 与「终态/空闲自动 close」两个分支均已实施**（方案 3，见「P2-9 行为口径」：审计后同一 pass 跑 P2-8 驱逐判定，投影 sweep 的静默关闭也改走同一路径，`source=reconcile`）；**按方案建议不做直接回调**：会话 TTL/删除时不联动 registry（避免 `chat` 包依赖 `agentcontrol`），由对账与投影 sweep 的 `session_missing` 判定兜底（observe 只报候选、enforce 才落库）。**未做**的是真机长跑 probe（终态子会话在一个对账周期内释放、空闲驱逐只作用于可安全回收对象）。
- P2-10 测试与验证补齐：**单测与并发压测已补** —— 并发配额守恒 `backend/internal/agentcontrol/concurrency_stress_test.go` 的 `TestSQLiteGlobalAgentRegistryStoreConcurrentReserveCloseConservesQuota`（8 goroutine × 6 轮 reserve + release/close）、spawn 回滚 `backend/internal/api/skills/session_agent_spawn_rollback_test.go` 的 `TestSessionAgentController_SpawnRollsBackReservationOnActorFailure`、并行工具门控 `backend/internal/agent/tool_parallel_scheduler_test.go` 的 `TestParallelToolBatchPlan_RejectsBrokerTools`，另有随 P1-7 产生的读窗口分页用例（`backend/internal/toolbroker/types_pagination_test.go`）。**未做**的是 runbook 真机 probe（`scripts/validate-multi-agent-real-terminal.ps1` 尚无 wait clamp / spawn 失败注入 / 审批 digest / supervision ack 四类 probe）。
- P2-11 协作引导：**部分实施**（`internal/prompt` 新增 `RenderMultiAgentCollaborationGuidance`，API 与 CLI 两宿主注入 system instruction；但工具描述层未同步新纪律——方案 2 的共享文案常量与方案 3 的 `wait_agent` timeout 上下界/默认值均未做，见 §5 P2-11 实施注记）。**未做**的是 ① 真机验证引导对模型行为的实际影响（spawn 后是否先做独立工作、是否减少同窗口重读）② `internal/agent` 子代理 prompt 的协作段落（默认 `maxDepth=1` 时子代理无 spawn 能力，暂不注入）。
- P2-12 监督通知闭环（本地宿主）：**部分实施**——CLI `list|ack|defer|resolve`、模型工具 `supervision_snapshot` / `ack_lifecycle` / `control_descendant` 与 stale 判定已落地；**未做**的是 ① `action cancel|close` 子命令 ② 方案 1 的「动作集合按宿主能力过滤」（`Evaluator.EvaluateAllowedActions` 仍只接收 `Notification`，见 §5 P2-12 实施注记）。

**P1-7 行为口径**

- **软护栏**：`wait_agent` / `read_agent_events` / `list_agents` 等轮询工具仍豁免语义重复检测（不误判 doom loop）；新增 `PollingBackoffTracker` 按「同一批轮询调用 + 归一化参数摘要（含 target ids / after_seq / timeout_ms）」计连续次数，达到阈值（默认 3，`agent.maxRepeatedPollCalls` 可调，负数关闭）后向模型注入 `polling_backoff` system-reminder，**只提示不拦截**；批次中出现任何真实工作（非豁免工具）或参数变化即重置计数，产品事件 `tool_loop.polling_backoff_observed` 每段连续只发一次。
- **next_action 补齐**：`AGENT_THREAD_LIMIT`（`spawn thread limit reached: max_threads=… active_children=…`）与 `AGENT_REGISTRY_UNAVAILABLE`（store 未初始化/已关闭）从通用 `TOOL_EXECUTION` 中细分，分别给出「先释放配额（close_agent / 复用 / 缩小批次）」与「不要重试、上报宿主接线问题」的可执行指引；此前的 `AGENT_ALREADY_EXISTS` / `AGENT_BUSY` / `AGENT_SESSION_NOT_FOUND` / `SPAWN_DEPTH_LIMIT` 已带 next_action。
- **读窗口元数据（已实施）**：`read_agent_events` 返回 `latest_seq` / `has_more` / `unread_count`（`backend/internal/toolbroker/types.go` 的 `ApplyAgentEventsPagination` / `CapAgentEventsUnread`），CLI 本地 host 的剩余计数由 `localAgentEventsUnreadCount`（`backend/cmd/aicli/commands/chat_actor_registry.go:3843-3846`）提供；工具描述明确 `has_more=true` 时先消费返回事件，再以 `after_seq=latest_seq` 续读。未读计数不可得时仍返回 `has_more` 并提示推进 `after_seq`，不阻塞调用方。
- **重复读窗口的显式返回（已实施）**：`read_agent_events` 按「调用方会话 + 目标会话 + `view` + `after_seq` + `limit`」记住最近一次读窗口（`backend/internal/toolbroker/agent_events_read_memo.go`，容量 128 的 FIFO、跨 caller/target 隔离、`close_agent` 时 `forgetTarget` 清理），窗口高水位未推进时结果携带 `unchanged=true` 与 `repeat_count`（第 N 次相同读，≥1），`next_action` 改写为 `unchanged_window: …`，cache-safe summary 追加 `Unchanged window: identical read #N …`，broker metadata 同步 `unchanged` / `repeat_count`；工具描述两处（含 teammate 变体）提示 `unchanged=true` 即「无新事件」、不要重读同一窗口。窗口推进或换 `after_seq` 自动重置计数；`sessionID` 为空的调用不记账，避免跨调用方误报。

**P0-4 行为口径**

- CLI 默认 `Mode=observe`（对齐本文件 P0-4 风险条）：deadline / progress / approval 到期只投影通知并进入父 preflight 与 wake 提醒，不打断仍在运行的本地子 agent；`AICLI_EXECUTION_SUPERVISOR_MODE=enforce` 可切到与 API 一致的 interrupt + cancel grace。
- 阈值与 API 同源：`supervision.Config`（`execution_deadline`=30m、`heartbeat_timeout`=5m → 看门狗 execution/progress 超时），扫描周期 5s，approval 超时 1h，cancel grace 15s。
- 兜底：无 durable control plane（或 store 未实现 `ExecutionRunStore`）时返回 nil，spawn 行为与实施前完全一致；循环绑定 host `lifecycleCtx`，`Close()` 时停止。
- 可见性（已实施）：`/debug supervision watchdog` 渲染接线状态（未接线/已就绪未启动/运行中）、生效 mode 与阈值、累计 scans/decisions/enforced、最近一次扫描与决策、最近完成出件与未终态 run 数；渲染严格只读——`peekLocalExecutionSupervisor` 不触发惰性构建、不启动后台巡检、不改动 store（`chat_debug_supervisor.go`）。
- 文档（已实施）：`docs/user-guide/aicli.md` §4.8「子 Agent 执行看门狗与监督通知（本地模式）」记录默认 `observe` 与 API `enforce` 的差异、阈值同源、前提（durable control plane）、`AICLI_EXECUTION_SUPERVISOR_MODE` 覆盖与 `/debug supervision ...` 入口；环境变量表新增该变量。
- 待补（不阻塞 P0-4 主路径）：真实终端 runbook probe（与 P2-10 第 3 项合并）。

**P2-12 行为口径**

- **动作入口**：本地宿主补齐 `/debug supervision list | ack | defer | resolve`（`backend/cmd/aicli/commands/chat_debug_supervision.go`）。写路径直接复用同一 SQLite supervision store 与 CAS 语义：`ack` 要求 `--note` 审计理由并额外落一条 durable action 行（`actions` 表，可经 `ListActions` 回读，通知表本身不新增列）；`defer --until 30m|2h|RFC3339` 复用 `DeferNotification(reason)`；`resolve --state closed|recovered|failed` 复用 `ResolveNotification`。`--expected-version` 不匹配即报冲突（`ErrActionConflict`），不静默重试。
- **边界与防误消警**：命令只允许操作当前会话 root scope（会话 ID 或活动团队）内的通知，跨 scope 一律拒绝；`ack` 前先按 `Evaluator.EvaluateAllowedActions` 复核动作是否被允许，避免绕过策略。
- **stale 判定（口径唯一化）**：`BuildDigest` 通过 `DigestRequest.SubjectPresence` 询问标的是否仍存在（`backend/internal/supervision/digest.go:23-35,120-131`）。API 与 CLI 宿主分别用 `supervisionSubjectPresence` / `localSupervisionSubjectPresence` 提供同一语义：agent_run 查 `ExecutionRunStore.GetExecutionRun`、team 查 `TeamStore.GetTeam`；标的缺失 → 降级 stale，不计入 `critical_unresolved` / `action_required`，摘要输出 `stale (subject absent from control plane; no action required)`；`checked=false`（store 未接线或查询失败）保持原 severity，宁可重复提示也不漏报。
- **模型工具入口（方案 3，已实施）**：`supervision_snapshot`（只读）/ `ack_lifecycle` / `control_descendant`（写）注册进 toolbroker（`backend/internal/toolbroker/supervision_tools.go`；识别与派发见 `backend/internal/toolbroker/broker.go:120,129,1706-1707`），且仅在宿主装配了 `Broker.Supervision` 时才出现在 `Definitions()`（`broker.go:196-202`），宿主没有 durable 控制面时三个工具整体缺席，不会出现“看得见调不通”的悬空工具。CLI 宿主由 `newLocalSupervisionToolController`（`backend/cmd/aicli/commands/chat_supervision_tools.go:26-37`）注入，装配点 `chat_actor_host.go:1360-1367`，先于合成默认工具策略（`chat_actor_host.go:1558-1571` 自动纳入 broker 定义，无需额外白名单）。
- **模型入口的 scope 口径**：与 preflight 完全一致——普通会话 = 自身 root scope（`TargetParentSessionID` = 会话 ID）；活动 Team 的 lead = 团队 scope（`RootScopeID` = `TargetParentTeamID` = 团队 ID，`TargetParentSessionID` 仍为 lead 会话 ID）。模型不能指定 root scope；`AckLifecycle` / `ControlDescendant` 先取本地 scope 再交给 `supervision.LocalControlService`（`backend/internal/supervision/local_control.go`），scope 校验、`EvaluateAllowedActions` 复核与 `expected_version` CAS 与 `/debug supervision` 是同一实现，因此模型既不能跨 scope 收敛，也不能绕过 `allowed_actions`。参数契约：`ack_lifecycle` 的 acknowledge 需 `note`、defer 需 `reason`+`until`（RFC3339 或 Go duration）、resolve 需 `state=closed|recovered|failed`；`control_descendant` 需 `reason` 且 action 必须在该通知的 `allowed_actions` 内；冲突返回 `ErrActionConflict`，工具描述要求模型重新读快照而不是盲目重试。

**P1-5 行为口径**

- **入口与隔离**：入口只出现在父轨迹 `subagent` item 的详情面板（`frontend/src/components/workspace/trajectory/trajectory-detail-panel.tsx:110` 的 `data-open-subagent-session`），事件必须带 `session_id` / `agent_id` 才会渲染（`subagentSessionTarget` 解析；只有 `role` 的占位事件不显示入口）。下钻使用**独立 `TrajectoryStore`**（`frontend/src/hooks/workspace/use-subagent-session.ts`），子事件不写入父 store —— 打开/关闭子会话前后父 transcript 的 items 不变（回归用例：`subagent-session-dialog.test.tsx` 的「TrajectoryView 入口且父 store 不被写入」）。行渲染上限 400 条（`MAX_VISIBLE_ROWS`，`subagent-session-dialog.tsx:38`），避免长跑子会话拖垮 DOM。
- **数据源（复用既有接口，无后端新接口）**：`GET /api/sessions/{id}/runtime/events`（`after`/`limit` 分页回填，页大小 `TRAJECTORY_RECOVERY_PAGE_SIZE`=500，`frontend/src/lib/trajectory/recovery.ts:17`）→ `GET /api/sessions/{id}/runtime/stream?live=1&after=<已收最大 seq>` 持续跟随。`live=1`（`backend/internal/api/skills/session_runtime_stream.go:54-55`）是宿主进程内 bus 的可选 fan-out，**只转发 live-only 类型**（当前为 `tool.progress` + `subagent.progress`，`backend/internal/api/skills/session_runtime_stream.go:183-186`），不落库、无持久 seq，事件视图带 `payload.live=true` 以与 durable 行区分（`:182-195`）；durable 事件仍走 EventStore。
- **live-only 进度的折叠语义**：`tool.progress`（`recovery.ts:20` 的 `TOOL_PROGRESS_EVENT_TYPE`，转换入口 `:239`）映射为**既有工具行的进行中更新**——kind=`tool_call`、`tool_call.id` 取 `payload.tool_call_id`、`tool.output_summary` 取 `partial` 优先回退 `message`、`percent` 透传，`_event.sequence=0` 按到达顺序即时应用；与 `chat.sse.tool_start` / `tool_end` 折叠为同一 `tool:<call_id>` 行，**不新增行**。无 `tool_call_id` 的进度事件直接忽略（避免无主行）。
- **终态冻结优先于迟到进度**：工具行进入 `completed` / `failed` / `canceled` 后 `upsertItem` 拒绝后续写入（`frontend/src/lib/trajectory/trajectory-reducer/snapshot-ops.ts:85-88`），因此 live 流与 `tool_end` 竞态时，迟到进度既不会覆盖定稿摘要，也不会把 phase 打回 running（用例：`trajectory-reducer.state.test.ts`「live tool.progress 折叠进既有工具行，终态后到达被冻结忽略」）。
- **连接生命周期**：断线按 `400ms × 尝试次数` 退避重连，默认 3 次（`use-subagent-session.ts:47-48`）；重连沿用已收最大 seq 增量续传，不重放已渲染事件；读到 `session_end` / `chat.sse.done` 主动断开并置 `closed`（`:51-56`），用户可手动 `reconnect()` 或让子会话再次输入后刷新。对话框关闭或 sessionId 变化时 abort 流并重建 store（`generation` 递增）。
- **验收口径（方案 1）**：下钻能看到与 CLI `/agents panel` 同源（同一 EventStore + bus）的工具/审批/生命周期事件；父 transcript 无子事件写入（隔离用例）；live-only 进度只更新既有工具行、终态后不覆盖。方案 2（父流节流镜像）与方案 4（inline 审批动作）已补齐（下文条目）；**未覆盖**：真机（活体子 agent）长跑观感与审批往返验证。
- **inline 审批（方案 4）**：带 `request_id` 的 `approval_requested` 在下钻对话框顶部渲染审批条（工具名 / 风险级别 / reason，`data-subagent-session-approval`），`approval_resolved` 按 `request_id` 精确清除（缺 id 时保守清除——同一子会话同一时刻只可能有一个 pending）；提交复用既有 `POST /api/runtime/sessions/{子会话 id}/runtime/commands` 的 `approve_tool` 分支（`frontend/src/api/runtime/sessions.ts` 的 `resolveSessionToolApproval`，body `{type, request_id, allow, [patched_args]}`），与 CLI/HTTP 的审批走同一 actor 路径（`actor.ApproveToolWithArgs` 唤醒阻塞中的工具调用），因此**不需要新后端接口**，也不会改写父子会话关系；提交失败（网络/网关拒绝）保留入口并显示原因（`data-subagent-session-approval-error`），不静默丢弃审批；成功后立即收掉入口，随后到达的 durable `approval_resolved` 成为 no-op；sessionId 变化或子会话终态时清空 pending。
- **`read_agent_events` 过滤视图（方案 3）**：可选 `view=tool_progress` 只保留 `tool.*` 事件与终态/审批事件（`session_end`、`agent.completed`/`failed`/`cancelled`/`reclaimed`、`approval_requested`/`approval_resolved`，`backend/internal/toolbroker/types.go` 的 `agentEventsToolProgressSticky`），其余（assistant 文本/推理等）被丢弃；结果新增 `view` 与 `filtered`（丢弃计数），`count` 反映投影后条数，缺省/未知 view 原样返回（`NormalizeAgentEventsView` 把拼写错误归一到 `all`，避免静默隐藏事件）。**分页契约不变**：`has_more`/`unread_count`/`next_action` 仍描述原始窗口，`after_seq=latest_seq` 依然能越过被过滤事件推进游标（`ApplyAgentEventsView` 不触碰分页字段）。工具 schema/描述两处与协作引导同步提示该视图。

**P1-6 行为口径**

- **分层预算**：wake reason 归一为三类（`backend/internal/supervision/wake_budget.go:49-65`）——`approval`（`approval_required` / `question_asked` / `permission_requested`）、`failure`（`execution_failed` / `execution_timeout` / `lifecycle_failed` 及含 failed/stalled 的 reason）、`other`（其余，含默认 `critical_lifecycle`）；三类各自独立计费，密集失败不会挤占审批。
- **上限语义**：`failure` / `other` 共用 `wake_max_auto_wake`（默认 5/小时；负数 = 不设硬上限）；`approval` 由 `wake_max_approval_wake` 单独控制，**默认 0 = unlimited**（延迟审批会卡住等待决策的子 agent），只有显式正数才封顶。超限返回 `ErrWakeRateLimited`，**只延后不丢弃**：`wake_pending` 行保持 durable，窗口滚动或下一个自然父回合仍会送达。
- **账本模式**：`wake_budget_mode: memory|durable`（默认 `memory`，保持历史行为）。`durable` 时每次已投递的 auto-wake 追加一行 `supervision_wake_claims`（`sqlite_store.go:251-261,1219`），窗口用量按 `root_scope_id + budget_class + claimed_at` 统计（`:1254`），因此多进程共享同一 DB 时预算一致、重启不清零；写失败降级为进程内窗口（best-effort），不会让已投递的回合失败。
- **保留与写放大**：每次记账后按 `now-2*window` 机会式剪枝（`wake_scheduler.go:407-433`），账本行数随窗口而非进程寿命增长。实测 200 次 durable 记账 + 剪枝 20.19ms（≈101µs/次，单条 SQLite 写 + 一次删除，剪枝通常 0 行），满足「每次 auto-wake 一次小写入」的预算。
- **claim id 抗碰撞**：`wakeclaim-<class>-<unixnano>-<seq>`，同一纳秒（注入时钟 / 并发 drain）下也不会因唯一索引幂等吞掉记账；`RecordWakeClaim` 用 `INSERT OR IGNORE`，重复 claim 不重复计数。
- **可观测**：`/debug supervision list` 追加 `wake 预算` 段，按 scope × class 输出 `used/limit` 或 `used/unlimited`（`backend/cmd/aicli/commands/chat_debug_supervision.go:359-393`）。
- **可见性（API 宿主，已实施）**：`GET /api/runtime/supervision/digest`（`root_scope_id`）与 `GET /api/runtime/supervision/snapshot`（`root_session_id` / `root_team_id`，同名 scope 去重）新增 `wake_budget` 字段——按 scope × class（approval → failure → other，与 CLI 同序）投影 `supervision.WakeBudgetState`（`used` / `limit` / `window` / `window_start` / `unlimited`），来源是同一 `WakeScheduler.BudgetState`，`wake_budget_mode=durable` 时即共享账本（`backend/internal/api/skills/supervision_handlers.go` 的 `supervisionWakeBudgetStates`）。scheduler 未接线、或未传任何非空 scope 时**整体省略该字段**（不渲染成误导性的 0/limit）；store 未接线仍是 503。CLI 侧 `/debug supervision list` 的「wake 预算」段与它同源同序。
- **模型可见（已实施，补上最后一处静默盲区）**：API 与 CLI 两条独立实现的 preflight 注入路径都把预算行追加到 `digest.Text` 之后、用户 prompt 之前，文案由共享的 `supervision.FormatWakeBudgetLine` 渲染（`backend/internal/supervision/wake_budget.go`），形如 `wake_budget: approval=0/unlimited failure=5/5 other=0/5 (exhausted classes defer wakes, not drop them)`；类顺序与 HTTP `wake_budget` 字段、CLI `/debug supervision list` 一致。两宿主的 scope 口径相同——目标会话 + 所属 team（去重后逐 scope × class 取 `WakeScheduler.BudgetState`，team lead 因此能同时看到自身与 team 两条 scope）。`FormatWakeBudgetLine` 对空切片返回空串，宿主未接线 `Wakes` 或 scope 全空时不追加，digest 文本字节级保持原样（两条路径各有「无 scheduler 不含该行」的用例）。语义上该行与 `ErrWakeRateLimited` 的「只延后不丢弃」对齐，模型无需等父回合结束或人工 `/debug` 就能发现巡查被推迟。
- **父 turn 结束自检（方案 4，已实施，默认关闭）**：`supervision.wake_self_check_per_window`（`backend/internal/supervision/config.go`，默认 `0` 关闭）> 0 时，两宿主的 turn-end 钩子在自动唤醒被预算延后（`errors.Is(err, supervision.ErrWakeRateLimited)`）之后，再尝试一次「自检 turn」（`MaybeSelfCheckParent`，`backend/internal/supervision/wake_self_check.go`）：先过与自动唤醒同一个 `Runnable` 门禁，再要求该 scope 仍有**未认领**的 durable wake，然后用同一条 `Deliver` 路径投递一个只消费 mailbox/digest 的 `AutoWakePrompt` 回合（`RootDigest` 与 preflight 同源，因此注入的仍是同一份生命周期摘要）。自检**不计入** approval/failure/other 类预算（不破坏「预算抑制唤醒风暴」的设计），改走自己的窗口配额：`WakeScheduler.AllowSelfCheck` 与类预算共用 `RateWindow`，一个 scope 每窗口最多一次，因此自检回合再次结束时配额已耗尽、不会递归；digest 为空（通知已 resolve/ack、wake 成了遗留行）时不投递也不消耗配额，直接由 `ResolveUnclaimedWakes` 清掉遗留行，避免每窗口空转。CLI 接在 `chat_actor_host.go` 的 root session turn-end 订阅，API 接在 `supervision_handlers.go` 的 `EventSessionEnd` 订阅。
- **待补（不阻塞主体）**：P1-6 的四个方案（分层预算 / durable 账本 / 去重合并 / 父 turn 结束自检）均已实施；方案 4 需在配置里显式打开，默认仍与历史行为一致。

**P2-9 行为口径**

- **对账循环**：`agentcontrol.Reconciler` 启动即跑一次（收敛上次非正常退出留下的漂移），随后按 `Interval` 周期执行；同一实例互斥，慢审计不会叠加。`observe` 只报告，`enforce` 才调用 `ReconcileAgentSessionConsistency` 收敛（`ACTIVE_AGENT_SESSION_MISSING` → close，`ACTIVE_AGENT_SESSION_TERMINAL` / `STALE` → stale），默认 observe。审计成功后同一 pass 再跑一次配额 sweep（`Reconciler.Reclaim` 钩子，见下条），因此「一致性收敛」与「自动 close」共享一次调度、一次投影刷新。
- **配置优先级（两宿主一致）**：进程 env（`AICLI_REGISTRY_RECONCILE_INTERVAL` / `AICLI_REGISTRY_RECONCILE_MODE`）> `RuntimeConfig.Agents`（`agents.registryReconcileInterval` / `registryReconcileMode`，默认 10m / observe）> 内置默认；间隔下限 1 分钟（`MinReconcileInterval`），非法 duration 静默回退到下一优先级。
- **宿主绑定**：CLI 循环绑定 `lifecycleCtx`（`Close()` 时停止）；API 循环绑定当前 durable agent store，配置热加载换 store 时重建、store 被清空时停止循环，避免对已关闭的 store 反复报错。
- **审计口径**：`List` 先刷新投影（CLI `materializeLocalAgentRegistry`，API `materializeAgentControlAgentProjections`）再读 `ListAgentControlAgents(IncludeClosed)`；`Lookup` 复用同一份 session storage + runtime state store（含 lease 过期判定），保证 `/debug` 的只读审计与周期对账不会给出不同结论。
- **可见性**：`/debug`（registry 审计块 + `/agents panel` 行）与 HTTP supervision API（snapshot / digest 响应的 `agent_registry_reconcile` 字段）输出缓存摘要 `reconcile=<mode> converged=<n> last_reconcile=<ts>`；未接线或尚未完成首次 pass 时为 `reconcile=not_run`，失败时为 `reconcile=error detail=...`。
- **手动清理入口（方案 3「一键清理」）**：`/agents cleanup|prune|gc [--dry-run] [--idle <时长>]`（`backend/cmd/aicli/commands/chat_agent_cleanup.go`）复用 spawn 闸门的同一决策链 —— `materializeLocalAgentRegistry` 刷新投影 → `QuotaChildren` → `observeLocalQuotaChildren` → `SelectReclaimable` → `ReclaimAgentQuota`，因此手工清理不会关闭任何 spawn 会拒绝驱逐的对象；默认策略只回收 `session_missing` / `session_terminal`，`--idle 30m` 才额外回收长期空闲行，`running` / `waiting_approval` / `waiting_input` / 有后台 job 的子 agent 永不回收；`--dry-run` 只输出 `would_reclaim` 预览、不落库；成功输出含 `root_session=… quota_children=… reclaimable=… idle_policy=…`、`reclaimed=` / `reclaim_reasons=`、`remaining_quota_children=`，回收事件仍写 `reclaimed:<reason>`（与 P2-8 口径一致）；宿主未装配 durable registry 或 store 不支持 `agentcontrol.AgentReclaimStore` 时输出 `reclaim=unavailable` 提示而非报错。unified 管道走 `executeStructuredAgentCleanupCommand` 返回 `CommandResult`，legacy 路径经 `printChatCommandOutput` / `printfChatCommandOutput` 输出，不新增 terminal 直写者（`TestChatInteractiveDirectWriterInventory` 基线不变）；命令目录 `/help`（`chat_slash_command_catalog.go`）与 `/agents ` 参数补全（`chat_slash_argument_completion.go`）同步列出 `cleanup|prune|gc` 与 `--dry-run` / `--idle`。
- **自动 close（方案 3「与 P2-8 共用开关」）**：周期对账在审计之后调用 `Reconciler.Reclaim`；两宿主各自实现该钩子（CLI `localActorRegistry.reclaimLocalAgentRegistryQuota`，API `Handler.reclaimAgentControlAgentQuota`），内部都交给共享驱动器 `SweepAgentQuotaReclaim`（`backend/internal/agentcontrol/reclaim.go`）：先 `QuotaRoots` 按 root 分组，再各自 `observe`（CLI `observeLocalQuotaChildren` / API 同一控制器）得到 `ReclaimObservation`，最后按模式分流。**判定与事件完全复用 P2-8**：策略 `ReclaimPolicy{IdleTimeout: agents.reclaimIdleMs, Now}`（空闲驱逐仍是显式开关，终态/容器消失的判定不依赖它），关闭走 `ReclaimAgentQuota` → `ReclaimAgentControlAgentSubtree`，行落到 `closed` 且 wake event 为 `reclaimed:<reason>`（`session_terminal` / `session_missing` / `idle_timeout`），产品事件 `agent.reclaimed` 的 `source=reconcile`（另两处为 `spawn_gate` / `manual_cleanup`），事件落在 root 会话自己的流上（周期回收没有「正在 spawn 的父会话」）。
- **observe / enforce 的差别只在是否写库**：observe（默认，含 `AICLI_REGISTRY_RECONCILE_MODE=observe`）只调 `EvaluateReclaimOutcome`，报告 `reclaim_candidates=<n> reclaim_reasons=<去重原因>`，不关闭任何行、不产生产品事件；enforce 才真正关闭，报告 `reclaimed=<n> reclaimed_rows=<行数> reclaim_reasons=…`（reasons 取自实际执行的决策），单个对象失败只累计 `reclaim_failed` / `reclaim_error` 而不断整轮。两模式共用 `AppendReclaimReason` 去重，`/debug` 一行摘要不会随触发者变长。
- **投影 sweep 不再静默关闭**：CLI `materializeLocalAgentRegistry` 原先对终态行直接 `CloseAgentControlAgentSubtree`（wake event 为 `closed`，与有序 `close_agent` 无法区分），现改为把终态行交给同一条 `SweepAgentQuotaReclaim`（`source=reconcile`）；store 不支持 `AgentReclaimStore` 时回退旧行为，保证宿主兼容。API 侧 `materializeAgentControlAgentProjections` 不关闭任何行，只做投影刷新。
- **三条触发点的分工**：`spawn_gate`（父会话已到上限、拒绝 spawn 前先尽力释放哨位）、`manual_cleanup`（`/agents cleanup`，含 `--dry-run`）、`reconcile`（周期对账 + 投影 sweep）共用同一 `QuotaChildren` → `SelectReclaimable` 判定与 `reclaimed:<reason>` 事件口径；任何一处都不会关闭 `running` / `waiting_approval` / `waiting_input` / 有后台 job 的对象，差别只在触发时机、`source` 标签与是否允许写库。

- **TTL 兜底（方案 2 选定的路径）**：会话 TTL/删除（`backend/internal/chat/manager.go:513-567`）不回调 registry，避免 `chat` 包依赖 `agentcontrol`；被删除的行在下一次对账或投影 sweep 中以 `session_missing` 命中同一条 `QuotaChildren` → `SelectReclaimable` 判定链（observe 只报 `reclaim_candidates`，enforce 才落库），因此不需要新增跨包调用。

**P2-8 行为口径**

- **配置语义（两宿主同源）**：`agents.maxThreads` 三态——`0`/未设置 = 回退内置默认（6），`-1` = 显式不限，`< -1` 报错（`agents.maxThreads must be -1 (unlimited), 0 (default), or a positive integer`）；`agents.reclaimIdleMs`（默认 0 = 关闭空闲驱逐，`> 0` 开启，毫秒，负数报错）。解释权收在 `agentcontrol.ResolveMaxThreads`（`backend/internal/agentcontrol/reclaim.go:28`），`api/skills` 与 CLI 不再各自判断 `<=0`。
- **超限文案**：固定前缀 `agent spawn thread limit reached: max_threads=%d active_children=%d`（`toolresult` 的 `AGENT_THREAD_LIMIT` 分类依赖此前缀），后接 `next_action=reuse_existing_child_or_close_idle`、`close_agent`、`set maxThreads=-1 for unlimited`，并附 `occupants=[path=… status=… idle=…]`（按空闲时长降序，超出 `defaultThreadOccupantLimit` 折叠 `+N more`）；驱逐尝试后追加 `reclaimed=N reclaimed_rows=M reclaim_reasons=…`，失败追加 `reclaim_failed=K reclaim_error=…`，store 不支持驱逐时 `reclaim=unavailable`。
- **驱逐边界（保守优先）**：只回收「容器已消失（`session_missing`）」或「容器已终态（`session_terminal`）」的子节点；`busy`（actor 忙 / 等审批 / 等输入 / 有后台 job）**永不回收**；空闲驱逐必须显式开启 `reclaimIdleMs>0`，空闲时长优先取会话 `UpdatedAt`，取不到才退化为 registry 行时间。回收后重新计数，仍超限才报错——不会为放行而连环驱逐。
- **与 `close_agent` 可区分**：回收写 `AgentStatusClosed` 终态（行保留可见，便于诊断），但 wake 事件 kind 固定为 `reclaimed:<reason>`（`idle_timeout` / `session_missing` / `session_terminal`，reason 为空兜底 `quota`），父会话经 `read_agent_events`、`/debug` 能区分「自动驱逐」与「人为关闭」。预留事务内的计数超限（`backend/internal/agentcontrol/global_agent_store.go:425`）没有 occupants 明细（事务内无法做会话观测），其余字段一致。
- **失败降级**：驱逐逐个对象执行，单个失败只累计 `Failed` / `FirstError` 并继续处理其余对象，整轮不中断；调用方无论驱逐结果如何都以重新计数为准。
- **待补（不阻塞主体）**：真机长跑 probe 未做（并发 spawn 不越限、空闲驱逐只作用于可安全回收对象）；配置迁移说明已补入 `MIGRATION.md` §9。产品事件流 `agent.reclaimed` 已接前端轨迹（白名单 + 一行 system note 摘要，见下方定向用例）。

**P2-11 行为口径**

- **双层引导的分工**：协作纪律同时存在于两处——工具描述（`backend/internal/toolbroker/broker.go` 的 `spawn_agent` / `wait_agent` 契约：结果批量返回、`timed_out` 后不得原地重试、`waiting_approval` 需先处理审批）与 system prompt（`internal/prompt` 的 `RenderMultiAgentCollaborationGuidance`）；后者覆盖工具描述表达不了的“何时不该 spawn / spawn 后父代理该干什么”。
- **段落内容（7 条）**：只派发有界、互不重叠且确实需要的子任务并写清交付物；`spawn_agent` 返回后先做同回合的独立工作，而不是立刻 `wait_agent`；用 `read_agent_events` + `after_seq` 增量消费，不重读已消费窗口；必须等待时一次等所有仍需的子会话并取最长可承受超时，超时后按 `next_action` 走；spawn 前用 `list_agents` 看槽位占用，超限时复用/关闭空闲子会话或本地完成，而不是原样重试；`waiting_approval` / `waiting_input` 的子会话要用 `resolve_agent_approval` / `send_input` / `followup_task` 处理；不再需要的子会话用 `close_agent` 释放槽位，teammate 走独立的 `wait_team` 生命周期。
- **幂等与注入点**：API 宿主把两个段落交给同一个 `withInstructionGuidance` 追加到首个 system instruction message，段落头命中即跳过，因此 handler、子代理、调试渲染三条路径重复组合也不会重复文本（`TestBuildRuntimeInstructionMessages_IncludesMultiAgentCollaborationGuidanceOnce`）；CLI 宿主把段落并入会话持久化 system prompt（`composeDurableChatSystemPromptWithGuidanceForCWD`），文本静态，不影响 prompt cache 前缀的字节稳定性。
- **不做宿主门控**：段落与 `Task difficulty rating and subagent delegation policy` 一起注入（含 `maxDepth=1` 的子代理与 skill executor），与现有 difficulty 段落口径一致；理由是子代理在 `maxDepth>1` 时同样具备 spawn 能力，且按宿主静默省略会让“引导缺失”再次漂移。
- **与 P0-1/P1-7/P2-8 的闭环**：段落里的每条都能落到既有实现——等待/超时语义与 `next_action`（P0-1、P1-7）、读窗口分页 `after_seq`/`has_more`（P1-7）、配额与驱逐（P2-8）、审批桥接（P0-2、P2-12），提示词不承诺运行时做不到的行为。

**验证记录（backend 目录）**

- `go build ./...` 通过（exit 0）。
- 定向用例全绿：`agentcontrol`（wait 边界、reserve/release 配额回收、stale binding 预检）、`supervision`（审批投影/digest/stale 判定/去重/wake，`go test ./internal/supervision -count=1` 整包通过；含模型入口复用的 `LocalControlService` 6 用例）、`api/skills`（事件总线端到端审批桥接、digest presence 接线）、`cmd/aicli/commands`（审批桥接、supervisor 生命周期/模式、`/debug supervision` list/ack/defer/resolve 与 stale presence：`go test ./cmd/aicli/commands -run "ChatDebugSupervision|LocalSupervisionSubjectPresence" -count=1`）、`agent`（轮询退避 tracker + loop 级不拦截契约 `TestReActLoop_InjectsPollingBackoffAdvisoryWithoutStopping`）、`toolresult`（线程上限/注册表不可用错误码与 next_action）、`toolbroker`（`read_agent_events` 分页元数据 `TestApplyAgentEventsPagination_*`）。
- P2-12 方案 3 定向用例：`go test ./internal/toolbroker -run "Supervision|AckLifecycle|ControlDescendant" -count=1 -v`（7 用例：定义门控、`IsBrokerTool` 识别、快照、ack 校验与转发、非法 deadline、control 动作、无宿主能力时报错）与 `go test ./cmd/aicli/commands -run "LocalSupervisionToolController" -count=1`（5 用例：snapshot→ack 收敛、scope 绑定与跨 scope 拒绝、Team lead 走团队 scope、无 store 时控制器缺席）均通过；`go test ./internal/toolbroker -count=1` 整包通过（7.6s）。`snapshot` 的 stale 口径由 `localSupervisionSubjectPresence` 决定，用例通过 seeding 一条 execution run 覆盖「标的存活 → 计入 critical_unresolved」的一侧，标的缺失一侧由 `supervision` 包用例覆盖。
- 本轮修掉的两个模型入口缺陷（均由「未被 `-run "Supervision"` 匹配」的用例暴露）：① 参数解析把缺失键经通用 `stringValue` 渲染成字面量 `"<nil>"`，导致 `until`/`state`/`cascade` 等可选参数被当成真实取值（例如 acknowledge 会因 `until "<nil>"` 报错）；新增 `supervisionArgValue`（`backend/internal/toolbroker/supervision_tools.go:250-260`）对缺失键与显式 null 一律按空值处理，10 处调用点全部替换；② 用例断言的元数据键名与实现不符（实现写 `cache_safe_summary`，用例读 `summary`），已统一到 `cacheSafeSummaryMetadataKey`。另观察到一次与本专题无关的 Windows 偶发：`TestBrokerBackgroundTaskPersistsStableJobAliasAcrossBrokerInstances` 的 `TempDir RemoveAll` 清理报 “directory is not empty”，重跑即绿。
- P1-6 定向用例与实测：`go test ./internal/supervision -run "Wake" -count=1` 17 例全绿，其中本轮新增 11 例（类别映射、审批不被失败预算饿死、durable 跨 scheduler 实例共享、durable 重启不清零、memory 模式对照、不限量逃生门、显式审批上限、claim 台账记录/计数/剪枝、同纳秒 claim 不丢、保留窗口有界）；`go test ./cmd/aicli/commands -run "ChatDebugSupervision" -count=1` 覆盖 `/debug supervision list` 的 `wake 预算` 段。写放大实测（`TestWakeClaims_RetentionStaysBoundedUnderRepeatedClaims` 日志）：`200 durable claims + opportunistic prunes in 20.19ms (100.953µs per claim)`，账本行数稳定在 ~2 个窗口（200 条中仅保留 121 条）。
- P2-9 定向用例：`go test ./internal/agentcontrol -run "Reconcile" -count=1`（6 例：enforce 收敛并缓存报告、observe 只读、`List` 失败摘要、`RunLoop` 启动即跑且可取消、mode/interval 归一化、nil 接收者）；`go test ./cmd/aicli/commands -run "LocalRegistryReconcile" -count=1`（3 例：tuning 优先级、enforce 收敛 + 摘要 + `Close()` 取消 + 幂等、无 store 时 `not_run`）；`go test ./internal/api/skills -run "AgentRegistryReconcile" -count=1`（tuning 优先级/边界，以及 enforce 收敛 + 摘要 + 幂等 + 摘除 store 后回到 `not_run`）。
- P2-9 方案 3（`/agents cleanup` 手动清理入口）定向用例与门禁：`go test ./cmd/aicli/commands -run "ChatAgentCleanup|StructuredAgentsCleanup" -count=1` 6 例全绿（终态子回收并放行 spawn、默认策略保留活跃子、`--dry-run --idle 1ns` 预览不落库、`--idle 1ns` 实回收、未知参数/缺参/非法时长返回用法文本、无 durable registry 时输出 `reclaim=unavailable`）；`go test ./cmd/aicli/commands -run "DirectWriter|NoDirectTerminalWriter" -count=1`（`TestChatInteractiveDirectWriterInventory` + `TestStructuredCommandHandlersHaveNoDirectTerminalWriter`）保持 PASS，确认新增命令未引入 fmt 直写者（legacy 入口改走 `printChatCommandOutput`）；`gofmt -l` 对新增与修改文件无输出、`go build ./...` exit 0；`go test ./cmd/aicli/commands -run "ChatSlashArgumentCompletion|ChatSlashCommandCatalog" -count=1` 通过（`/help` 目录与参数补全同步 `cleanup`，`TestChatSlashCommandCatalogMatchesHandleCommandRoutes` 路由一致性门禁未破坏）。
- P2-11 定向用例：`go test ./internal/prompt -run "MultiAgentCollaboration" -count=1 -v`（1 例：段落头唯一、spawn 后先做独立工作、`after_seq` 增量读、最长超时、`next_action`、`list_agents` 配额、`resolve_agent_approval`/`close_agent`/`wait_team` 提示齐备）；`go test ./internal/api/skills -run "InstructionMessages" -count=1 -v`（5 例，含新增 `TestBuildRuntimeInstructionMessages_IncludesMultiAgentCollaborationGuidanceOnce`：段落存在且重复组合不重复注入）；`go test ./cmd/aicli/commands -run "TestComposeLocalChatSystemPrompt_IncludesWorkspaceGuidance" -count=1 -v`（CLI system prompt 断言新增协作段落两行）。`gofmt -l` 干净、`go build ./...` exit 0、`go vet ./internal/prompt ./internal/api/skills ./cmd/aicli/commands` 通过。
- P0-4 周边（可见性 + 文档）定向用例：`go test ./internal/supervision -run "Stats" -count=1 -v`（3 例：nil 接收者可渲染、扫描/决策/完成出件计数与快照字段、enforce 计数与决策窗口上限 `maxSupervisorStatsDecisions`、RunLoop 的 running/idle 生命周期）；`go test ./cmd/aicli/commands -run "ChatDebugSupervisionWatchdog" -count=1 -v`（4 例：三种接线状态渲染齐备、渲染只读——`peekLocalExecutionSupervisor` 仍为 nil 且不建巡检 ctx、运行中快照含循环/计数/store 视图、watchdog 路由不要求通知 store 且 help 同步）。`go test -race ./internal/supervision -count=1` 整包通过（新增 mutex 保护的统计块无竞态）；`gofmt -l` 对新增与修改文件干净、`go vet ./internal/supervision ./cmd/aicli/commands` 通过、`go build ./...` exit 0。
- 包级回归（默认 HOME）：`go test ./cmd/aicli/commands ./internal/agentcontrol ./internal/config ./internal/api/skills -count=1` 中 `agentcontrol`/`config`/`api/skills` 整包绿，`cmd/aicli/commands` 仅 `TestProviderCommand_ListAndShow` 失败；该失败已在洁净 HEAD 工作树（`git worktree add --detach HEAD`，无本轮改动）复现同一输出（多出本机 `C:\Users\vince\.aicli\config.yaml` 的 `opencode.ai` provider），定性为环境相关既有失败而非本轮回归。
- P1-5 方案 3 定向用例（`read_agent_events` 过滤视图）：`go test ./internal/toolbroker -run "AgentEventsView\|KeepsAgentEvent\|ReadAgentEvents" -count=1 -v`（8 例新增 + 2 例既有读回归：view 归一化（空/`all`/大小写/未知回退）、`tool.*` 与终态/审批留存、assistant 文本/推理丢弃并计 `filtered`、`all`/未知为 no-op 且不打 `view` 标记、空窗口仅打标记、nil 安全、broker 层 `view` 透传到 controller 并投影结果）；`go test ./internal/prompt -count=1` 与 `go test ./internal/api/skills -run "InstructionMessages" -count=1`、`go test ./cmd/aicli/commands -run "TestComposeLocalChatSystemPrompt_IncludesWorkspaceGuidance" -count=1` 全绿（协作引导新增 `view=tool_progress` 提示后段落契约不变）；包级回归 `go test ./internal/toolbroker -count=1` 绿（12.25s）、`go test ./internal/api/skills -run "ReadAgentEvents\|RuntimeSessionEvents\|AgentEvents" -count=1` 绿、`go build ./...` exit 0、`go vet ./internal/toolbroker` 通过、`gofmt -l` 对新增/修改文件无输出。
- P2-8 方案 4 定向用例（产品事件流 `agent.reclaimed`）：`go test ./internal/api/skills -run "ShouldPersistRuntimeSessionEvent\|SpawnGatePublishesReclaimEvent\|NoReclaimNoEvent\|RuntimeSessionEvents" -count=1`（绿）；`go test ./cmd/aicli/commands -run "ReclaimEvent\|LocalAgentReclaim\|LocalActorRegistry_Reclaims" -count=1 -v`（3 例：空闲驱逐发布 `agent.reclaimed`、`/agents cleanup` 手动回收发布事件、summary 区分 unavailable 与 empty）；前端 `npx vitest run src/lib/trajectory/recovery.test.ts src/lib/trajectory/trajectory-reducer.events.test.ts`（2 文件 34 例：白名单命中、payload 原样推送且剥离 `seq`、`describeRuntimeEvent` 摘要（路径/原因/来源/截断）与 system 行渲染），`npx tsc --noEmit` exit 0、`gofmt -l` 无输出、`go vet ./internal/api/skills ./internal/agentcontrol` 通过。
- 包级回归（隔离 HOME，P2-8 + P2-11 全部改动）：`go test ./internal/prompt ./internal/api/skills ./internal/agentcontrol ./internal/config -count=1` 全绿（1.4s / 15.1s / 2.4s / 1.6s），`HOME`/`USERPROFILE` 指向空目录后 `go test ./cmd/aicli/commands -count=1` 整包绿（72.6s）。
- `cmd/aicli/commands` 整包状态（含历史阻塞）：2026-09-13 复跑时隔离 `HOME`/`USERPROFILE` 后 `go -C backend test ./cmd/aicli/commands -count=1 -json` 无任何 `"Action":"fail"` 事件，整包绿。历史上还曾在默认 10m 超时下失败：`TestHandleChatWebAPICache_RequestsAndOverview` 阻塞于 `internal/usageanalytics.(*Store).query`（`database/sql` 等待 ≥8 分钟，栈含 `cacheanalytics.SessionFallbackSource.Requests`）。该用例属缓存分析专题（`docs/plan/llm-cache-analytics-unified-plan.md`）；现象出现在多个 aicli/runtime-server 进程并发持有 SQLite 时，建议按 SQLite 锁竞争单独排查。上述四个 P0 项的定向用例不受影响。
- `cmd/aicli/commands` 默认 HOME 下两个失败的具体定性：其一 `TestChatInteractiveDirectWriterInventory`（`backend/cmd/aicli/commands/chat_command_result_test.go:1002`）是本轮的真实门禁回归——P2-12 的 `/debug supervision` 分支原先直接 `fmt.Printf`/`fmt.Println`，使 `backend/cmd/aicli/commands/chat_debug_archive.go` 中 `handleDebugCommand` 的直写者由基线 2 变成 4；现已改为复用既有输出边界 `printChatCommandOutput`/`printfChatCommandOutput`（`backend/cmd/aicli/commands/chat_surface_output.go:445,455`），并按「同文件内迁移、总数不变」修正基线里两处过期条目（`printChatAgentMessageResult` 2→1；新增 `sendChatAgentMessageCommand` 0→1，源于 `/agents send|followup` 的打印迁到包装函数）。修复后该门禁 PASS（`-run "TestChatInteractiveDirectWriterInventory|TestStructuredCommandHandlersHaveNoDirectTerminalWriter"`）。其二 `TestProviderCommand_ListAndShow`（`backend/cmd/aicli/commands/provider_command_test.go:26`）为环境相关而非代码缺陷：`config.InitGlobalConfig` 会与用户真实配置合并，本机 `C:\Users\vince\.aicli\config.yaml` 多出一个 `opencode.ai` provider，导致 `list.Total != 1` 断言失败；把 `HOME`/`USERPROFILE` 指向空目录后该用例 PASS。
- P2-10 定向用例（单测与并发压测）：`go test ./internal/agentcontrol -run "ConcurrentReserveCloseConservesQuota" -count=1 -v`（1 例，0.46s：8 goroutine × 6 轮并发 reserve + release/close，断言 24 行 stale、24 行 closed、active 仅 root、同 limit 可重新预留、无死锁）；`go test ./internal/api/skills -run "SpawnRollsBackReservationOnActorFailure" -count=1 -v`（1 例，0.40s：actor 创建失败后预留回滚为 stale 且保留可审计行、子会话删除、active 仅 root）；`go test ./internal/agent -run "RejectsBrokerTools" -count=1`（6 子例：`spawn_agent` / `wait_agent` / `close_agent` / `read_agent_events` / `spawn_team` 混批均整批串行，两个 `spawn_agent` 同批亦串行）。三个改动文件 `gofmt -l` 无输出。包级回归：`go test ./internal/api/skills ./internal/toolbroker ./internal/supervision -count=1` 三包全绿（14.58s / 7.63s / 2.35s；首轮多包并行时 `TestWakeBudget_ApprovalNotStarvedByFailureBudget` 失败一次（单包复跑通过）；`TestBrokerBackgroundTaskPersistsStableJobAliasAcrossBrokerInstances` 经干净 HEAD worktree（detached `b19cf11b`）单包 `-count=3` 复现 3/3，是 Windows 下**确定性**的既有失败（`TempDir RemoveAll cleanup: unlinkat …\001\logs: The directory is not empty.`），与本轮改动无关，未修）；`go test ./internal/agentcontrol ./internal/agent -count=1` 全绿（2.52s / 2.27s）；`go test -race ./internal/agentcontrol -run "ConcurrentReserveCloseConservesQuota" -count=1` 通过（5.91s，新增并发用例无数据竞态）；隔离 `HOME`/`USERPROFILE` 后 `go test ./cmd/aicli/commands -count=1` 整包绿（71.84s）；`go build ./...` exit 0、`go vet ./internal/agentcontrol ./internal/agent ./internal/api/skills` 通过。**未实施**：真实终端三个 probe（需交互终端）。

- 补充（2026-09-13，第二轮）：提问回答并入底部 prompt 后的**草稿归还修复** —— 只渲染正文、不拥有底部输入行的 popup 清理不再走 `resetPromptState()`，改走新增的 `clearPopupHandlePreservePromptInput`（`backend/cmd/aicli/commands/chat_surface_output.go:162`；仍拥有 composer line 的 `showPriorityPrompt` 继续用 `clearPopupHandle`），回归 `backend/cmd/aicli/commands/chat_question_merged_prompt_test.go`。验证：`gofmt -l` 无输出，`go build ./...` / `go vet ./...` exit 0，`go test ./cmd/aicli/commands -run "MergedAnswer|MergedIntoBottomPrompt|AnswerPromptBodyLines" -count=1` 通过。口径与代码锚点见 `docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md` §15.10。

- 补充（2026-09-13，第三轮）：**P1-5 方案 2 父流节流镜像（`subagent.progress`）**。后端新增 `backend/internal/supervision/subagent_progress.go`：`EventTypeSubagentProgress`、`DefaultSubagentProgressWindow=2s`、`SubagentProgressMirror`（`Observe`/`Forget`/`Pending`；key = 子会话 + `tool_call_id`（缺省退化 `tool:<name>`）；**窗口内同状态合并、状态变化立即穿透**，窗口锚定最后一次发射时刻；stamp 最多 512 条，先按 `10×窗口` 判 stale 剪枝、再按上限淘汰最旧；镜像 payload 稳定键 `agent_id`/`session_id`（子会话）、`parent_session_id`、`source_event_type=tool.progress`、`state`、`live=true`、可选 `tool_call_id`/`tool_name`/`message`/`partial`/`percent`/`path`/`depth`/`agent_type`/`role`/`source_event_timestamp`/`metadata`/`mirror_window_ms`，且**不修改 source 载荷**）。接线 `backend/internal/api/skills/session_runtime_support.go`（`subscribeAgentCompletion`：`toolprotocol.EventTypeProgress` 分支经镜像后 `bus.Publish` 到父总线；`chat.EventSessionEnd`/`EventSessionInterrupted` 时 `Forget(childSessionID)`）与 `backend/internal/api/skills/session_runtime_stream.go`（`sessionLiveOnlyRuntimeEventTypes` 改为列表订阅，`tool.progress` + `subagent.progress` 同为 live-only：不落库、不注入持久化 seq）。前端新增 `frontend/src/lib/trajectory/recovery.ts` 的 `SUBAGENT_PROGRESS_EVENT_TYPE` / `subagentProgressEventToTrajectoryPush`（`runtime` 行、`_event.sequence=0`、`path`→`agent_path`、缺子会话身份则拒收），摘要 `trajectory-reducer/event-readers.ts`（`agent progress: <path> <tool> <state> [percent] [— partial]`），并让 `trajectory-reducer/apply.ts` 的 `runtime` 分支对 `live:true` 行使用非终态 `running` —— 否则 `snapshot-ops.ts` 的终态冻结会让第二条镜像再也无法更新 `runtime-0`。语义取舍：同一父流的全部镜像共用一个 `runtime-0` 槽位（父轨迹只保留最新一条折叠行，与服务端节流一致），父/子 EventStore 均不新增行；多子代理时该行显示"最近一次进度"，完整名单仍在子会话下钻与 registry 行。

  验证（backend 目录）：`gofmt -l` 对新增/修改文件无输出；`go build ./...`、`go vet ./internal/api/skills` exit 0；`go test ./internal/supervision -run TestSubagentProgressMirror -count=1 -v` 6 例全绿（窗口合并与状态穿透、payload 逐键契约与 source 隔离、不可镜像事件拒收、tool_name 回退与 `Forget`、512 上限与 stale 剪枝、16×32 并发安全）；`go test ./internal/api/skills -run "APIAgentProgressMirror|StreamSessionRuntimeEvents" -count=1 -v` 全绿（父总线收到镜像且字段完整、窗口内第二次 progress 被合并、`background_complete` 穿透、`child-other` 不串流、父/子 EventStore 均无镜像行；SSE `?live=1` 正文含 `"type":"subagent.progress"`、`"live":true` 与 `mirror-should-not-appear` 缺席）。验证（frontend 目录）：`npx vitest run src/lib/trajectory` 10 文件 96 例全绿（新增 4 例：镜像→`runtime` push、无主子行拒收 + `session_id` 兜底、摘要四态、`runtime-0` 单行就地更新且状态保持 `running`）；父流订阅侧 `frontend/src/hooks/workspace/use-session-runtime-stream.ts:115-120` 补 `live: true`（此前只有子会话下钻 `use-subagent-session.ts:150` 带该开关，父流漏传会让镜像事件在服务端就被过滤），并加回归用例 `frontend/src/hooks/workspace/use-session-runtime-stream.test.tsx`「subscribes with live=true and keeps live-only subagent progress」（断言开关透传 + 无 `seq` 的镜像仍进 thread 快照，`lastRuntimeEventType`/`runtimeEventCount` 同步）；`npx vitest run src/hooks/workspace/use-session-runtime-stream.test.tsx` 7 例绿、`npx vitest run src/lib/trajectory src/lib/thread-state` 15 文件 126 例绿、`npx tsc --noEmit` exit 0。**未做**：真机长跑观感验证。

- 补充（2026-09-13，第四轮）：**P1-5 方案 4 前端 inline 审批** —— 在下钻对话框内直接批准/拒绝子 agent 的 pending 工具审批，动作复用既有 actor `approve_tool` 命令，后端零改动。命令封装 `frontend/src/api/runtime/sessions.ts`（`ResolveSessionToolApprovalRequest` + `resolveSessionToolApproval`：`POST /api/runtime/sessions/{id}/runtime/commands`，body `{type:"approve_tool", request_id, allow, [patched_args]}`；会话 ID 由调用方显式给定、服务端不改写父子关系，`patched_args` 仅在提供时透传）。pending 状态机 `frontend/src/hooks/workspace/use-subagent-session.ts`（`PendingSubagentApproval` + `nextPendingApproval`：`approval_requested` 缺 `request_id` 不建入口、`tool_name` 回退事件级字段、`approval_resolved` 按 id 精确清除（缺 id 时保守清除，因为同一子会话同一时刻至多一个 pending）、无关事件原样返回；hook 暴露 `pendingApproval`/`resolvingApproval`/`approvalError`/`resolveApproval`，提交成功后立即收掉入口（随后到达的 durable `approval_resolved` 成为 no-op），失败保留入口并给出原因，sessionId 变化或子会话终态时清空）。UI `frontend/src/components/workspace/trajectory/subagent-session-dialog.tsx`（header 下方审批条：工具名 / 风险级别 / reason + 批准/拒绝按钮，`data-subagent-session-approval`；失败提示 `data-subagent-session-approval-error`；提交中按钮 disabled）；文案 `frontend/src/i18n/resources/{zh-CN,en-US}/workspace/panels-shell.ts`（`panels.shell.trajectory.subagentSession.approval.*`）。结构整理：把非组件导出（`SubagentSessionTarget` 类型、`subagentSessionTarget()` 解析、`readText()` 辅助）拆到新文件 `frontend/src/components/workspace/trajectory/subagent-session-target.ts`，对话框文件只导出组件（消除 `react-refresh/only-export-components` 报错），引用点 `trajectory-view.tsx` / `trajectory-detail-panel.tsx` / 对应测试同步。

  验证（frontend 目录）：`npx vitest run src/components/workspace/trajectory/subagent-session-dialog.test.tsx` 12 例全绿（新增「批准后打 `approve_tool` 命令且收掉入口」「提交失败保留入口 + 非匹配 `approval_resolved` 不清除」）；`npx vitest run src/hooks/workspace/use-subagent-session.test.ts src/api/runtime/sessions.test.ts` 7 例全绿（pending 状态机四态 + 命令体 / `patched_args` / 错误透传）；合并回归 `npx vitest run src/components/workspace/trajectory src/hooks/workspace/use-subagent-session.test.ts src/api/runtime/sessions.test.ts` 8 文件 66 例全绿；`npx tsc --noEmit` exit 0；`npx eslint` 对改动文件 0 问题；`npm run lint:i18n` OK（scanned=503, violations=0）。**未做**：真机（活体子 agent 审批往返）验证。

- 补充（2026-09-13，第五轮）：**P1-6 API 宿主侧 wake 预算可见性**（原「未实施项」的 API 等价展示）。新增只读投影 `Handler.supervisionWakeBudgetStates`（`backend/internal/api/skills/supervision_handlers.go`）：对每个非空且去重后的 root scope，按 approval → failure → other 顺序取 `WakeScheduler.BudgetState`（与 CLI `/debug supervision list` 同源、同 class 顺序），并接入两个读模型响应 —— `GET /api/runtime/supervision/digest`（scope 取 `root_scope_id`）与 `GET /api/runtime/supervision/snapshot`（scope 取 `root_session_id` + `root_team_id`，同名去重），字段名 `wake_budget`，元素即 `supervision.WakeBudgetState`（`used`/`limit`/`window`/`window_start`/`unlimited`）。语义边界：scheduler 未接线或未传任何非空 scope 时**整体省略该字段**（避免把"没有账本"误报成"预算未使用"的 0/limit），store 未接线仍保持 503；投影只读，不触发任何记账/剪枝。

  验证（backend 目录）：新增 `backend/internal/api/skills/supervision_wake_budget_test.go` 3 例全绿 —— `TestSupervisionDigest_WakeBudgetProjection`（durable 账本记 2 条 failure claim + 1 条重复 claim id，响应 3 行且 `failure.used=2`（幂等不重复计数）/`limit=5`、`approval.unlimited=true`、`other.used=0`，window 与 window_start 已填充）、`TestSupervisionSnapshot_WakeBudgetProjection`（`root_team_id` 单 scope 3 行、approval unlimited 但 `used=1`、同名双参数去重仍 3 行、会话+团队双 scope 6 行）、`TestSupervisionWakeBudget_OmittedWithoutScheduler`（无 scheduler、有 scheduler 但无 scope 均无 `wake_budget` 字段；无 store 仍 503）。`gofmt -l` 无输出、`go build ./...` 与 `go vet ./internal/api/skills` exit 0；包级回归 `go test ./internal/api/skills -count=1` 绿（13.87s）、`go test ./internal/supervision -count=1` 绿（0.80s）。

- 补充（2026-09-13，第六轮）：**P1-7 最后一项待补 —— 相同 `after_seq` 重复读的显式返回**（原先只有 `next_action` 间接引导；窗口未推进时两次读逐字节相同、命中缓存摘要，模型拿不到「游标没动」的直接证据）。实现为 broker 侧只读 memo + 结果字段，宿主零改动：
  - `backend/internal/toolbroker/agent_events_read_memo.go`（新增）：`agentEventsReadKey`（caller / target / view / after_seq / limit，`\x00` 连接，跨 caller 不串味）、`agentEventsReadMemo`（互斥锁 + 容量 128 FIFO；`observe` 返回「本次是否重复 + 连续重复次数」，高水位推进即归零；`forgetTarget` 供 `close_agent` 清理；nil 接收者安全）。
  - `backend/internal/toolbroker/types.go`：`AgentEventsResult` 新增 `unchanged` / `repeat_count`（`omitempty`，普通读的 JSON 形状不变）与纯函数 `MarkAgentEventsRepeatedRead`（`next_action` 改写为 `unchanged_window: identical read #N with after_seq=…`，`repeatCount<1` 归一为 1，nil 安全）。
  - `backend/internal/toolbroker/broker.go`：`Broker` 新增惰性创建的 `agentEventsReads *agentEventsReadMemo`（包级互斥锁保证并发安全，pointer 语义保证拷贝 Broker 不复制 memo）；`case ToolReadAgentEvents` 在 `FinalizeAgentEventsResult` 之后、alias 之前记账，`case ToolCloseAgent` 成功后 `forgetTarget`；cache-safe metadata 在重复读时补 `unchanged=true` / `repeat_count`；`read_agent_events` 两处工具描述补一句「相同 `after_seq` 未推进即返回 `unchanged=true` + `repeat_count`，视为无新事件、不要重读同一窗口」。
  - `backend/internal/toolbroker/cache_safe_summary.go`：`agentEventsCacheSafeSummary` 增加 `Unchanged window: identical read #N …` 一行，让模型侧看到显式差异而不是逐字节相同的摘要。
  - 边界：只有「同一 caller + 同一 target + 同 view/after_seq/limit + 高水位不变」才算重复；窗口推进、换游标、换 caller 都重新计数；`sessionID` 为空（无归属）时不记账；memo 只补提示字段，不改变读结果本身与 `has_more`/`unread_count` 分页契约。
  验证（backend 目录）：新增 `backend/internal/toolbroker/agent_events_repeat_read_test.go` 全绿 —— 首次读不标记、第二次 `unchanged=true`/`repeat_count=1` 且 summary 含 `identical read #1`、metadata 同步、第三次计数 2、换 `after_seq` 重新计数、窗口推进重置、跨 caller 隔离、无 caller 不记账、`close_agent` 清理后重新计数、memo 的 fresh/repeat/reset/forget/FIFO 驱逐/nil 安全、`MarkAgentEventsRepeatedRead` 归一与 nil 保护、两处工具描述都含 `unchanged=true`。`gofmt -l` 无输出；`go build ./...` 与 `go vet ./internal/toolbroker` exit 0；`go test ./internal/toolbroker -count=1` 绿（6.79s，含全部既有 broker 用例）；宿主侧定向回归 `go test ./internal/agent -run "Polling|Cache|ToolExec" -count=1`、`go test ./cmd/aicli/commands -run "ReadAgentEvents|AgentEvents|ToolProgress" -count=1`、`go test ./internal/api/skills -run "AgentEvents|ToolProgress|InstructionMessages" -count=1` 全绿。**未做**：真机验证模型收到 `unchanged=true` 后是否减少同窗口重读（与 P2-11 的真机 probe 同批验证）。

- 补充（2026-09-13，第七轮）：**真机 probe 发现并修复「spawn run 永不终态 → 假性 `progress_stalled`」（P0-4/P2-9/P2-10 的交集缺口）**。证据（本机 runtime 数据 `~/.aicli/sessions/runtime/supervision/supervision.db`）：`supervision_execution_runs` 中 `run_20260913115954_5aa14203`（p0-verify）、`run_20260913115954_0b9c243b`（p1-5-verify）、`run_20260913115955_42aed38c`（p1-67-verify）、`run_20260913115956_d25f05d6`（p2-verify）四条 `workflow=spawn_agent` 的 run，在对应子会话已经 `agent_completed`/`agent_failed` 之后仍停留在 `queued`（`finished_at` 为空、`progress_seq=0`、无 owner），于是 watchdog 每轮把 `progress_stalled` 投影给父会话；同时 `supervision_actions` 表为空，`supervision_snapshot`/`ack_lifecycle`/`control_descendant` 三个模型侧工具在本地宿主未暴露，通知只增不减（N9 的真机形态）。根因：`backend/internal/toolbroker/execution_run_hook.go:25`（`startSpawnExecutionRun`）注册 run 之后，没有任何宿主路径调用 `MarkExecutionRunTerminal`/`RecordExecutionProgress`（两 API 的调用点仅存在于 `internal/supervision` 内部与测试）。修复：`backend/internal/supervision/projection.go` 的 `ProjectAgentCompletion` 在投影子会话终态时，best-effort 收敛该子会话下仍 active 的 run（completed→`succeeded`、failed/error→`failed`、stopped/interrupted/canceled→`canceled`；已终态 run 不被改写；store 未实现 `ExecutionRunStore` 或 child id 为空时 no-op；run 写入失败不影响生命周期通知投影）。

  验证（backend 目录）：`go build ./...` exit 0；`go test ./internal/supervision -count=1` 绿；新增 `backend/internal/supervision/projection_run_finalize_test.go` 全绿（三状态映射、已终态不被覆盖、可选 store 与空 child id 退化）；宿主侧定向回归 `go test ./internal/api/skills -run "Spawn|Supervision|Rollback" -count=1`、`go test ./cmd/aicli/commands -run "Supervision|Completion" -count=1` 全绿。**未做 / 待宿主补齐**：本次之前的 6 条历史 unresolved 通知（2 条 `agent_failed` + 4 条 `progress_stalled`）需要宿主侧动作（模型侧工具或 `/debug supervision ack|resolve`）才能收敛，当前 local 宿主未暴露该路径；修复需重建 runtime-server/CLI 后才对新 spawn 生效。

---

## 附录 A：关键代码索引

| 主题 | 位置 |
| --- | --- |
| spawn 主流程 / 失败路径 | `backend/internal/api/skills/session_runtime_support.go:488-606` |
| spawn 预留 | 同上 `:1149-1175`；`backend/internal/agentcontrol/global_agent_store.go:366-369` |
| 并发闸门 | 同上 `:2563-2604`；配置兜底 `:2606-2616` |
| wait / mailbox wait | 同上 `:1633-1709`、`:1711-1768` |
| read_agent_events | 同上 `:1876-1969` |
| 审批处理 | 同上 `:1596-1631`；快照字段 `:2434-2438` |
| 终态镜像 / mailbox | 同上 `:950-981`、`:1135-1147` |
| 唤醒事件集合 | 同上 `:2925-2932`、`:2934-2946` |
| 配置定义与校验 | `backend/internal/config/manager.go:79-85,325-331,1065-1080` |
| supervisor 默认参数 | `backend/internal/supervision/execution_supervisor.go:48-57` |
| wake 调度与预算 | `backend/internal/supervision/wake_scheduler.go:62-93,240-259` |
| 投影规则 | `backend/internal/supervision/projection.go:91-135` |
| 一致性审计 | `backend/internal/agentcontrol/consistency_audit.go:20-77` |
| 并行工具门控 | `backend/internal/agent/tool_parallel_scheduler.go:28-80` |
| doom loop 豁免 | `backend/internal/agent/doom_loop.go:165-183` |
| CLI 同构实现 | `backend/cmd/aicli/commands/chat_actor_registry.go:1169-1195,2136-2142,2206-2213,3113-3117` |
| 会话 TTL | `backend/internal/chat/manager.go:513-567` |
| 前端 subagent 消费 | `frontend/src/api/runtime/sse.ts:249-251`；`frontend/src/hooks/workspace/agent-chat-turn/stream-handlers.ts:312-326` |

## 附录 B：审查方法与边界

- 方法：父 agent 对多 agent 控制面做只读代码走查（grep/view），并派出 3 个只读子代理分别覆盖“能力矩阵 / 可观测性与 CLI-API 差异 / 并发与生命周期健壮性”。能力矩阵中部分行号来自子代理走查，父 agent 对与优化项直接相关的部分（wait、审批订阅、并发预留、wake 预算、并行门控、前端消费）做了二次复核。
- 未决项（实施前需确认）：
  1. ~~CLI `Spawn` 调用方在预留成功后的失败清理路径未逐行核实~~（2026-09-13 已核实：补偿覆盖 actor/prompt 失败，`snapshot` 读回失败分支未走补偿（口径未定）→ 转为 P0-3 的已知缺口，见 §5 P0-3 实施注记）。
  2. ~~前端是否存在非 grep 命中形式的子会话入口~~（已由 P1-5 方案 1 覆盖：`trajectory-detail-panel.tsx:110` 的 `data-open-subagent-session` + `subagent-session-dialog.tsx`；如需更严格确认可再做一次 UI 走查）。
  3. ~~wake 预算持久化后的 SQLite 写放大未实测~~（已补：`internal/supervision/wake_budget_test.go` 的保留窗口用例实测 200 次 durable 记账 + 剪枝 20.19ms ≈ 101µs/次，行数稳定在 ~2 个窗口，见 P1-6 行为口径）。
  4. `config.AgentsConfig` 其他字段（如 `defaultForkTurns`）在部分为 0/空时的组合语义未穷尽。
  5. N9/P2-12 的动作集合口径仍未闭环（2026-09-13 复核）：`Evaluator.EvaluateAllowedActions`（`backend/internal/supervision/evaluator.go:25-50`）仍只接收 `Notification`，方案 1 的「按宿主能力过滤」未实现——见 §5 P2-12 实施注记。
- 本轮补充取证（2026-09-13，全部只读）：以一次真实只读子代理批次（`batch_52d54fa3c2ec843b`，2/3 失败）与 supervision 台账（`~/.aicli/sessions/runtime/supervision/supervision.db`）做端到端复核 —— 通知停在 `unresolved`；对已死会话执行 `close_agent` 未改动台账（`version`/`updated_at` 不变）；`runtime-server`（`::8101`）的 HTTP digest 对本 scope 返回 0 条。N9、N10、P2-12 由此而来。
- 本文件的前两部分（§3–§9）为只读走查产出；自 2026-09-13 起进入实施阶段，代码改动与定向测试证据见 §10。
