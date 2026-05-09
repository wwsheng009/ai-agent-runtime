# Multi-Agent 下一阶段实施计划

更新时间: 2026-05-09

实施状态更新:

- P0 `RegistryService` lifecycle、health、mode、idempotent close、shared SQLite policy 和跨实例并发测试已实施。
- P1 display mirror 依赖压缩已补 no-mirror 回归测试；`/collab`、`/agents panel` 和 `read_agent_events mailbox_only` 均可直接读取 completion mailbox。
- P1 projection capability 已新增 `MailboxProjectionStatus` / `MailboxProjectionReporter`，runtime/team store 可报告 `local_only`、`write_through`、`transactional`，并在 `/agents panel` 与 `/debug` 中展示。
- P2 `/agents panel follow` 已支持 fixed-bottom modal follow/navigation；legacy terminal 下仍保持 snapshot/follow 输出。已覆盖上下移动 agent 游标、左右切换 pane、Enter 选中 target、Esc 退出、interactive interrupt 标记 session interrupted，并已接入 mailbox、agent、task wake 触发刷新。
- 真实终端/provider 验证 runbook 已新增到 `docs/plan/multi-agent-real-terminal-validation-runbook.md`，验证摘要已新增到 `docs/working/multi-agent-real-terminal-validation-20260509.md`。此前从仓库根目录执行 `backend\aicli.exe` 导致未加载 `backend\configs\.env` / `backend\configs\config.yaml`，因此出现 `HTTP 401 Invalid API Key` 误判；从 `backend` 工作目录执行 provider smoke test 已通过。真实 `mimo_anthropic` 非交互 `spawn_agent -> wait_agent -> read_agent_events` 复验已通过，并修复了 stale AgentControl session binding 与 child/team teammate 同名误拒绝问题。`spawn_team` 父会话等待缺口已新增 `wait_team` durable 工具；真实 `mimo_anthropic` 非交互 `spawn_team auto_start=true -> wait_team` probe 已通过，并修复了 provider 将 `tasks` 作为 JSON 字符串传入时被静默吞成 0 task 的问题。真实人工 Ctrl+C 与 Windows Terminal TUI 验证仍未通过。

## 1. 背景与当前基线

上一阶段已经把多 agent 机制的核心数据权威从 legacy mirror 推进到 AgentControl substrate。最新提交基线为:

- `011a4b1 feat: unify agent control runtime substrate`

当前已经落地的关键能力:

- `agent_control_task_records` / `agent_control_task_dependencies` 已成为 team task graph 主写入表。
- legacy `team_tasks` / `team_task_dependencies` 已退化为 migration-only 回填输入，并在 migration 中删除。
- runtime/team mailbox 均已收敛到 `agent_control_mailbox_records` 主物理表。
- `agent_control_global_mailbox_records` 已支持 global-primary canonical mailbox 写入。
- local/global mailbox projection 已有双向 repair 与自动 reconcile。
- `agentControl.storePath` / `agentControl.storeDSN` 已支持 mailbox registry 与 agent registry 共用单一 SQLite DB。
- SQLite runtime/team store 在 registry DB 可 attach 时已支持同一 SQLite transaction 写 global primary row 与 local projection。
- AgentRegistry 已支持 durable `agent_control_agents`、atomic spawn reservation、registry-first spawn/list/path close/team teammate projection。
- AgentRegistry wake、mailbox wake、task wake 已具备 durable sequence/watch 基础。
- CLI 已有 `/agents`、`/agents panel`、`/agents pick`、`/agents target`、`/agents send`、`/agents followup`、`/collab`、`/timeline` 等可见入口。

因此下一阶段不再重复处理“把 task graph 从 mirror 提升为主表”“停止写旧 mirror”“删除旧 task/mailbox/wake 表”这些已经完成的主线。新的重点是把 AgentControl substrate 从“主路径已落地”推进到“跨进程、可观测、可运维、可交互”的稳定形态。

## 2. 总目标

下一阶段目标:

1. 将 `agentcontrol.RegistryService` 从进程内 facade 推进为明确的 durable registry service lifecycle。
2. 压缩 runtime 展示型 session event mirror 依赖，使新展示入口直接消费 AgentControl mailbox/registry。
3. 明确并测试非 SQLite 或不可 attach store 的 projection 兼容模型。
4. 把 `/agents panel` 从 snapshot/popup 扩展为可持续跟随、可导航、可切换 target 的 TUI 面板。
5. 用真实终端与真实 provider 场景验证多 agent 输出隔离、reasoning 隔离和 Ctrl+C 中断收敛。

## 3. 非目标

本阶段不做以下事项:

- 不重新引入或恢复 legacy `team_tasks`、`team_mailbox_messages`、`session_mailbox_messages` 等旧物理表写路径。
- 不把 root 普通输入隐式投递给 selected child agent。显式 `/agents send` / `/agents followup` 仍是子 agent 投递入口。
- 不为了 TUI 面板引入新的权威数据源。面板应读取 AgentControl mailbox、AgentRegistry、task graph 和 timeline read model。
- 不在没有明确边界前改造所有 runtime API 路由。先收敛 service lifecycle 与依赖方向，再扩展 API。

## 4. 阶段拆分

### P0. Durable Registry Service 生命周期

问题:

当前 `RegistryService` 已经可以在单进程内统一打开 mailbox registry 与 agent registry，但它仍主要是一个进程内对象。多个 CLI、Skills API、runtime server 同时使用同一个 registry DB 时，缺少明确的 lifecycle、shared DB policy、busy retry、health check 和关闭语义。

实施项:

1. 为 `agentcontrol.RegistryService` 增加显式 lifecycle 状态。
   - `Start` / `Close` / `Health` 或等价接口。
   - 关闭后再次访问应返回明确错误。
   - 多次关闭应幂等。
2. 统一 SQLite shared DB policy。
   - 打开 registry DB 时明确启用 WAL、busy timeout、foreign keys。
   - 记录或暴露当前 registry mode: `single_sqlite`、`split_sqlite`、`disabled`。
3. 增加并发访问测试。
   - 两个 `RegistryService` 实例打开同一个 DB。
   - 并发 spawn reservation 不突破 `max_threads`。
   - 并发 mailbox global-primary append 保持 sequence 单调和幂等。
   - 一个实例关闭不影响另一个实例继续读写。
4. 在本地 chat 与 Skills API 注入点中收口 service 持有关系。
   - 避免每个 handler 临时重复打开 registry store。
   - 明确 owner 负责 close。
   - debug 输出显示 registry service 配置来源和模式。

主要文件:

- `backend/internal/agentcontrol/registry_service.go`
- `backend/internal/agentcontrol/registry_service_test.go`
- `backend/internal/agentcontrol/global_mailbox_store.go`
- `backend/internal/agentcontrol/global_agent_store.go`
- `backend/internal/api/skills/handler.go`
- `backend/cmd/aicli/commands/chat_actor_host.go`

验收标准:

- 同一个 SQLite registry DB 可被两个 service 实例同时打开并安全读写。
- 并发 spawn reservation 只产生合法 agent row，不突破限制。
- registry service close 后无 goroutine/watch 泄漏。
- `go test ./internal/agentcontrol ./internal/api/skills ./cmd/aicli/commands -count=1` 通过。

### P1. 压缩展示型 Session Event Mirror 依赖

问题:

child completion 已经是 AgentControl mailbox-first，但仍会写 `display_mirror=true` 的 `subagent.completed` session event 供 CLI/TUI 兼容展示。当前这不是数据权威，但后续展示入口如果继续依赖 mirror，会让“子 agent 原始事件污染主控制台”的旧问题反复出现。

实施项:

1. 梳理展示入口依赖。
   - `/collab`
   - `/agents panel`
   - `/timeline`
   - `wait_agent` 无目标等待
   - `read_agent_events` 无目标读取
   - runtime event bridge 中的 terminal/collab 展示
2. 将新展示逻辑统一改为优先读取 AgentControl mailbox/collab substrate。
3. 将 `subagent.completed` display mirror 限定为 legacy compatibility。
   - 保留写入时必须携带 `display_mirror=true` 与 `mirror_source=agent_control_mailbox`。
   - 新代码不得把它当作权威完成信号。
4. 增加 no-mirror 测试。
   - 手动只写 parent AgentControl mailbox，不写 display mirror。
   - `/collab`、`/agents panel`、wait/read 仍能展示 completion。
5. 调整文档表述。
   - 明确 mirror 是兼容输出，不是通信模型。

主要文件:

- `backend/cmd/aicli/commands/chat_actor_registry.go`
- `backend/cmd/aicli/commands/chat_actor_host.go`
- `backend/cmd/aicli/commands/chat_debug.go`
- `backend/cmd/aicli/commands/chat_session_info_test.go`
- `backend/internal/chat/mailbox_event.go`
- `backend/internal/toolbroker/broker.go`

验收标准:

- 删除或缺失 display mirror 时，AgentControl mailbox 仍可驱动 collab/panel/wait/read。
- 子 agent reasoning、assistant delta、legacy terminal event 不会直接混入 primary session 输出。
- 新增测试能明确防止“展示层又依赖 session event mirror”回归。

### P1. 非 SQLite Store Projection 兼容矩阵

问题:

SQLite store 在 registry DB 可 attach 时已经支持同事务提交 global primary row 与 local projection。非 SQLite、in-memory 或不可 attach DSN 的 store 仍使用 repairable write-through/reconcile。这个策略是合理的，但目前缺少明确 capability 标记、debug 可见性和覆盖完整的失败修复测试。

实施项:

1. 增加 projection capability 描述。
   - `transactional`: global primary 与 local projection 同事务。
   - `write_through`: local/global 分步写入，失败可 repair。
   - `reconcile_only`: 不在写路径同步，只依靠 reconcile materialize。
2. 在 registry/debug 输出中显示当前 projection mode。
   - SQLite attach 成功: `transactional`。
   - SQLite attach 失败或跨 DSN 不可 attach: `write_through`。
   - 没有 global registry store: `local_only`。
3. 补齐 fallback 测试。
   - write-through global 写失败后 local row 保留。
   - repair 后 global row 创建并回填 `global_seq`。
   - global-only row 可 repair 回 local projection。
   - 重复 reconcile 幂等，不重复唤醒。
4. 文档化支持矩阵。
   - SQLite same DB。
   - SQLite attachable separate DB。
   - SQLite non-attachable DSN。
   - InMemory。
   - 未来可能的 remote store。

主要文件:

- `backend/internal/agentcontrol/mailbox.go`
- `backend/internal/agentcontrol/global_mailbox_store.go`
- `backend/internal/chat/session_runtime_store.go`
- `backend/internal/team/sqlite_store.go`
- `backend/internal/chat/session_runtime_store_test.go`
- `backend/internal/team/sqlite_store_test.go`

验收标准:

- debug 或 registry summary 能解释当前 mailbox projection 模式。
- 所有非事务 fallback 都有 repair/reconcile 测试。
- 文档明确说明非 SQLite store 的一致性语义是 eventual consistency，而不是同事务。

### P2. 长驻 Multi-Agent TUI 面板

问题:

`/agents panel [limit]` 已经能显示 selected target、parent session、active team、registry state、agent graph、mailbox snapshot 和 team timeline，但它仍是一次性 snapshot 或 popup，不是完整的长驻交互面板。

实施项:

1. 设计 panel state。
   - selected target。
   - focused pane: agent graph / mailbox / timeline。
   - cursor index。
   - follow mode。
   - filter text。
2. 扩展命令入口。
   - `/agents panel` 保持 snapshot 兼容。
   - `/agents panel follow` 进入跟随刷新模式。
   - `/agents panel target <path|id>` 快速切换目标。
   - `/agents panel next` / `/agents panel prev` 快速切换目标。
3. 接入 AgentControl wake。
   - mailbox wake 触发 mailbox pane 刷新。
   - agent wake 触发 agent graph 刷新。
   - task wake 触发 task/timeline pane 刷新。
   - timeline 当前复用 task lifecycle wake 作为刷新源。
4. 增加键盘导航。
   - 上下移动列表。
   - 左右切换 pane。
   - Enter 选中 agent target。
   - Esc 退出 panel。
5. 保持降级策略。
   - fixed-bottom surface 不可用时，不刷屏。
   - legacy terminal 下输出一次 snapshot，并提示 follow unavailable。
   - JSON/非交互模式不进入长驻 UI。

主要文件:

- `backend/cmd/aicli/commands/chat_debug.go`
- `backend/cmd/aicli/commands/chat_interaction.go`
- `backend/cmd/aicli/ui/inputbox_editor.go`
- `backend/cmd/aicli/commands/chat_session_info_test.go`
- `backend/cmd/aicli/commands/chat_slash_command_catalog.go`

验收标准:

- `/agents panel` 旧行为保持。
- `/agents panel follow` 能在 fixed-bottom surface 下进入 modal follow/navigation，并在 mailbox、agent、task wake 后刷新；legacy terminal 下等待 mailbox 更新后刷新一次。
- target 切换只改变 selected target，不会让普通 root 输入隐式发给 child。
- legacy terminal、非交互、JSON 输出行为保持稳定。

### P2. 真实终端与真实 Provider 验证

问题:

单元测试已经覆盖 Ctrl+C cleanup、provider 慢响应、stream error、task cancelled lifecycle mailbox 等关键分支。但用户最初反馈来自真实 `aicli chat` 运行日志，因此仍需要补一轮真实终端和真实 provider 的验证，尤其是输出隔离、reasoning 隔离和 Ctrl+C 行为。

实施项:

1. 编写 runbook。
   - 固定 provider/model。
   - 固定 session store、chat log、debug log、HTTP artifact、shell artifact。
   - 固定多 agent prompt。
2. 验证多 child agent 并行。
   - 子 agent assistant delta 不直接输出到主控制台。
   - 子 agent reasoning 不直接输出到主控制台。
   - parent 只收到结构化 mailbox/collab event。
3. 验证 `spawn_team auto_start=true`。
   - 多 teammate assignment 不出现 `session is busy (running)` 的错误风暴。
   - task assignment、task lifecycle、team completed 都能通过 AgentControl mailbox/timeline 观察。
   - parent 使用 `wait_team` 等待 durable `team.completed/team.summary`，不再误用 `wait_agent` / `read_agent_events` 查询 teammate id。
4. 验证 Ctrl+C。
   - 第一次 Ctrl+C cancel active team/child，不退出 chat loop。
   - 第二次 Ctrl+C 退出。
   - cancelled task 产生 lifecycle mailbox。
   - provider context 被取消后不会继续写 terminal team summary。
5. 保存验证材料。
   - runbook 保存到 `docs/plan` 或 `docs/working`。
   - 摘要写回本计划或 comparison plan。
   - 失败日志归档并生成修复项。

当前验证状态:

- 自动化测试已覆盖 registry lifecycle、display mirror no-mirror、projection fallback/repair、panel follow/navigation、modal interrupt 和 `go test ./...`。
- 已基于当前工作树构建 `backend/aicli.exe`。
- 已重新确认真实 provider smoke test 需要从 `backend` 工作目录执行，以便加载 `backend\configs\.env` 与 `backend\configs\config.yaml`；按该方式执行 `mimo_anthropic` 已返回 `OK`。
- 已使用真实 `mimo_anthropic` provider 在非交互模式执行 `spawn_agent` 并行验证，确认 `spawn_agent`、`wait_agent`、`read_agent_events` 均能完成。该验证暴露并已修复两个问题:
  - stale active `agent_control_agents.session_id` 绑定导致新 child spawn 触发唯一索引冲突。
  - 当前 child id 与历史 `spawn_team` teammate id 同名时，`wait_agent` / `read_agent_events` 被误拒绝为 team teammate。
- 已新增 `wait_team` broker 工具，允许 parent 在 `spawn_team auto_start=true` 后按 `team_id` 等待持久 `team.completed/team.summary`，并返回 team status、summary、latest seq 和最近 team lifecycle events。该工具不要求 active team run，适合 lead/root 会话直接调用。
- 已修复真实 provider 下 `spawn_team` 参数兼容性: 当模型把 `teammates` / `tasks` 作为 JSON 数组字符串传入时，broker 会解码为对象数组；非法格式会直接报错，不再静默创建 0 task 团队。`spawn_team` 工具摘要现在包含 `team_id`，便于 parent 直接传给 `wait_team`。
- `scripts/validate-multi-agent-real-terminal.ps1` 已新增并通过真实 provider 非交互 `spawn_team auto_start=true -> wait_team` probe，用于回归检查是否仍出现 teammate id 误用、`session is busy (running)`、0 task 创建或唯一索引冲突。
- 当前 shell 不是人工交互终端，无法真实按 Ctrl+C 验证 Windows Terminal TUI surface；该项仍需按 runbook 在真实终端执行。

验收标准:

- 真实运行日志证明 primary console 不再被 child reasoning/assistant delta 污染。
- Ctrl+C 行为与确定性测试一致。
- 出现 provider stream error 时后台 team 能稳定进入 failed/cancelled/done terminal state，不挂死。

## 5. 推荐执行顺序

建议顺序:

1. P0 registry service lifecycle。
2. P1 display mirror 依赖压缩。
3. P1 非 SQLite projection capability 与测试。
4. P2 长驻 TUI panel。
5. P2 真实终端/provider 验证。

原因:

- registry service lifecycle 是所有跨进程和 durable watch 的基础。
- display mirror 压缩应先于 TUI 深化，否则 TUI 可能继续绑定兼容事件。
- 非 SQLite projection capability 能让 debug/panel 展示真实一致性状态。
- 长驻 TUI 需要依赖前面稳定的 AgentControl mailbox/registry/wake substrate。
- 真实验证应放在关键实现后执行，避免验证仍停留在旧机制上。

## 6. 测试命令

阶段内建议持续运行:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./internal/agentcontrol ./internal/chat ./internal/team ./internal/api/skills ./cmd/aicli/commands -count=1
```

涉及 registry 并发和 SQLite 事务时额外运行:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./internal/agentcontrol ./internal/chat ./internal/team -run "Registry|Projection|Mailbox|AgentControl|Wake" -count=1
```

提交前运行:

```powershell
git diff --check
cd E:\projects\ai\ai-agent-runtime\backend
go test ./... 
```

## 7. 风险与控制

| 风险 | 影响 | 控制方式 |
| --- | --- | --- |
| 跨进程同时写 SQLite registry 时出现 busy/lock | spawn/mailbox 写入失败或延迟 | 配置 busy timeout、WAL，并增加并发测试 |
| display mirror 过早删除导致 legacy UI 断裂 | 用户看不到完成事件 | 先降级为兼容输出，新入口不依赖，最后再考虑删除 |
| TUI follow mode 刷屏或抢输入 | 交互体验退化 | fixed-bottom surface gating，legacy/非交互降级为 snapshot |
| 非 SQLite projection 被误认为强一致 | 调试判断错误 | debug 输出 projection mode，文档写明 eventual consistency |
| 真实 provider stream error 与测试 fake 行为不同 | 后台 team 挂死或状态不收敛 | runbook 固定验证，失败归档并补确定性测试 |

## 8. 完成定义

本阶段完成时应满足:

- `RegistryService` 有明确 lifecycle、health、shared DB policy 和跨实例测试。
- 新展示入口不依赖 display mirror 作为数据权威。
- 非 SQLite / 不可 attach store 的 projection mode 可见、可测、可 repair。
- `/agents panel` 有可用的长驻 follow/navigation 形态，并保持 legacy fallback。
- 真实终端/provider 验证材料落盘，能证明多 agent 输出隔离、reasoning 隔离和 Ctrl+C 行为稳定。当前已有 runbook、provider smoke test 通过记录、历史 401 配置路径更正说明、真实 provider 非交互 `spawn_agent -> wait_agent -> read_agent_events` 通过记录，以及真实 provider 非交互 `spawn_team -> wait_team` 通过记录；真实人工 Ctrl+C 与 Windows Terminal TUI 验证尚未达到通过标准。
- `go test ./...` 与 `git diff --check` 通过。
