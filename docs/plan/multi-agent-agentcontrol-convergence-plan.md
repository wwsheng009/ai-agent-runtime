# Multi-Agent AgentControl 收敛计划

更新时间: 2026-05-09

## 1. 计划定位

本计划用于承接当前多 agent 机制已经完成的 AgentControl 主路径改造，并把剩余工作收敛为可交付、可验证、可回归的工程阶段。

当前项目已经不再处于“是否要把 task graph、mailbox、wake、agent registry 迁到 AgentControl”的论证阶段。主线已经完成:

- `agent_control_task_records` / `agent_control_task_dependencies` 已成为 task graph 主写入表。
- legacy `team_tasks` / `team_task_dependencies` 已退化为 migration-only 输入，并在 migration 中删除。
- runtime/team mailbox 已统一到 `agent_control_mailbox_records`。
- durable global mailbox registry 已支持 global-primary canonical 写入。
- SQLite runtime/team store 在可 attach registry DB 时已支持同事务写 global primary row 与 local projection。
- local/global mailbox projection 已具备双向 repair 与自动 reconcile。
- AgentRegistry 已具备 durable agent identity、atomic spawn reservation、registry-first spawn/list/path-close/team teammate projection。
- mailbox wake、task wake、agent wake 已具备 durable sequence/watch 基础。
- CLI 已具备 `/agents`、`/agents panel`、`/agents pick`、`/agents target`、`/agents send`、`/agents followup`、`/collab`、`/timeline` 等排障入口。

因此，本计划不再重复“提升主写入表”“停止写旧 mirror”“删除旧表”这些已经完成的任务，而是聚焦最后的收敛问题:

1. 把 registry service 从进程内 facade 收敛为可复用、可诊断的 durable service lifecycle。
2. 把显示层和真实通信层彻底分清，防止 display mirror 再次成为隐式权威。
3. 把非 SQLite / 不可 attach store 的一致性语义显式化，并用测试保证可修复。
4. 把 TUI 多 agent 面板从 snapshot 工具提升为可导航、可跟随、可排障的交互面板。
5. 用真实终端和真实 provider 验证用户最初遇到的输出污染、reasoning 泄漏、Ctrl+C 无法中断等问题。

## 2. 当前状态判断

### 2.1 已经完成的能力

AgentControl substrate 已经承担多 agent 的主要数据权威:

- task assignment、task lifecycle、dependency、ready/claim/release/retry/terminal 状态迁移均已通过 AgentControl-shaped seam。
- child completion 已是 mailbox-first，`subagent.completed` session event 只应作为 legacy display mirror。
- team task assignment 与 task lifecycle 已可进入 durable mailbox。
- runtime/team mailbox 使用统一 mailbox read model，支持 scope 区分 `session` 与 `team`。
- global registry 与 local projection 之间具备 backlink 与 repair 路径。
- registry service 已有 shared SQLite policy、health、mode、idempotent close 和跨实例并发测试基础。
- projection capability 已可区分 `local_only`、`transactional`、`write_through`、`reconcile_only`。

### 2.2 仍需收敛的缺口

剩余缺口不是单点功能缺失，而是工程形态还没有完全闭环:

- registry service 仍主要作为进程内对象使用，尚未形成跨 CLI、runtime server、Skills API 的单一生命周期约束与运维视图。
- display mirror 虽然不是数据权威，但如果新展示入口继续读取它，仍可能重新引入子 agent 原始输出污染主控制台的问题。
- non-SQLite store 的 eventual consistency 语义需要在 debug、文档和测试中保持一致，避免被误认为强一致。
- `/agents panel` 已有聚合视图和最小 follow，但还不是完整长驻交互面板。
- 真实 provider、真实终端、真实 Ctrl+C 的组合仍需要端到端证据，而不能只依赖 fake provider 单元测试。

## 3. 总目标

本计划完成后，多 agent 机制应达到以下状态:

- 运行时能明确说明当前使用哪个 AgentControl registry、哪种 projection mode、哪些 watcher 正在工作。
- parent console 不接收 child 原始 assistant delta、reasoning block 或 teammate 临时输出。
- child/team 的完成、失败、取消、blocked、handoff 均通过 AgentControl mailbox/timeline 进入结构化协作视图。
- Ctrl+C 能稳定取消 active child/team，并把 lifecycle 写入 mailbox/timeline；第二次 Ctrl+C 再退出 chat loop。
- `/agents panel` 能作为日常排障入口持续观察 agent graph、mailbox、timeline 和 selected target。
- `go test ./...`、`git diff --check`、真实终端 runbook 均有明确通过记录。

## 4. 非目标

本计划明确不做以下事项:

- 不恢复旧的 `team_tasks`、`team_task_dependencies`、`team_mailbox_messages`、`session_mailbox_messages`、旧 wake mirror 表写路径。
- 不让普通 root 输入因为选中了 child target 就隐式投递给 child。投递 child 必须继续使用 `/agents send` 或 `/agents followup`。
- 不把 display mirror 删除作为第一步。应先保证所有新入口不依赖 mirror，再逐步降低 mirror 的存在感。
- 不为了 TUI 面板增加新的权威数据源。面板只能消费 AgentControl mailbox、AgentRegistry、task graph 和 timeline read model。
- 不在本阶段引入远程数据库或分布式锁服务。当前收敛以 SQLite durable registry 与可替换接口边界为准。

## 5. 阶段拆分

### P0. 基线冻结与现状验证

目标:

确认当前工作树中的 AgentControl 改造处于可测试状态，避免后续计划建立在不可编译或测试破损的基线上。

实施项:

1. 修复当前测试编译问题。
   - 重点检查 `/agents panel` target navigation 相关测试。
   - 清理误插入或未使用的 `sessionManager`。
   - 确认 panel target 切换会持久化 selected target。
2. 运行最小闭环测试。
   - `go test ./cmd/aicli/commands -run "AgentsPanelCanSwitchTarget|AgentsPanelFollow|AgentsPanel|Slash" -count=1`
   - `go test ./internal/agentcontrol ./internal/chat ./internal/team ./internal/api/skills ./cmd/aicli/commands -count=1`
3. 运行格式与补丁检查。
   - `git diff --check`
   - 必要时运行 `gofmt`。

验收标准:

- 当前工作树可编译。
- 新增 `/agents panel` follow/target/next/prev 测试通过。
- 没有 whitespace error。

### P1. Registry Service 生命周期收敛

目标:

把 registry service 从“若干 handler 可打开的 store 组合”收敛为明确 owner、明确生命周期、明确健康状态的 durable substrate。

实施项:

1. 统一 service ownership。
   - local chat runtime host 持有单一 `RegistryService`。
   - Skills API handler 持有或复用同一 service。
   - 避免请求路径反复临时打开 registry DB。
2. 完善 health 输出。
   - 暴露 `mode`、`shared_db`、`mailbox_store`、`agent_store`、`projection`、`started_at`、`closed`。
   - `/debug` 与 `/agents panel` 应显示同一套核心字段。
3. 完善关闭语义。
   - `Close()` 幂等。
   - close 后 store 访问返回明确错误。
   - watcher channel 能被主动关闭，避免 goroutine 泄漏。
4. 强化跨实例并发测试。
   - 两个 registry service 打开同一 SQLite DB。
   - 并发 spawn reservation 不突破 maxThreads。
   - 并发 global-primary mailbox append 保持 canonical row 幂等与 sequence 单调。

主要文件:

- `backend/internal/agentcontrol/registry_service.go`
- `backend/internal/agentcontrol/global_mailbox_store.go`
- `backend/internal/agentcontrol/global_agent_store.go`
- `backend/internal/agentcontrol/registry_service_test.go`
- `backend/cmd/aicli/commands/chat_debug.go`
- `backend/internal/api/skills/handler.go`

验收标准:

- 同一 registry DB 可被多个进程内 service 实例安全打开。
- service close 不影响其他实例继续读写。
- debug/panel 能解释当前 registry service 状态。

### P2. Display Mirror 依赖压缩

目标:

确保新展示入口直接消费 AgentControl mailbox/collab/timeline，不再把 legacy session display mirror 当成完成信号或通信模型。

实施项:

1. 标注 display mirror 边界。
   - `subagent.completed` 继续携带 `display_mirror=true`。
   - `mirror_source=agent_control_mailbox` 必须保留。
   - 新代码不得以 display mirror 作为唯一完成依据。
2. 检查展示入口。
   - `/collab`
   - `/agents panel`
   - `/timeline`
   - `wait_agent` 无目标等待
   - `read_agent_events` 无目标读取
   - runtime event bridge 的 terminal/collab 展示
3. 增加 no-mirror 回归测试。
   - 只写 parent AgentControl completion mailbox。
   - 不写 `subagent.completed` session event。
   - `/collab`、`/agents panel`、`read_agent_events mailbox_only` 仍能看到完成。
4. 检查输出隔离。
   - child reasoning 不进入 parent primary console。
   - child assistant delta 不进入 parent primary console。
   - team member 原始 stream 不进入 lead primary console。

主要文件:

- `backend/cmd/aicli/commands/chat_actor_registry.go`
- `backend/cmd/aicli/commands/chat_actor_host.go`
- `backend/cmd/aicli/commands/chat_debug.go`
- `backend/cmd/aicli/commands/chat_session_info_test.go`
- `backend/internal/chat/mailbox_event.go`
- `backend/internal/toolbroker/broker.go`

验收标准:

- display mirror 缺失时，新入口仍能工作。
- primary console 只展示 parent 输出与结构化 collab/mailbox 通知。
- 测试能防止后续代码重新依赖 mirror。

### P3. Projection 兼容矩阵闭环

目标:

把不同 store 类型下的 mailbox consistency 语义变成显式能力，而不是隐含行为。

实施项:

1. 统一 projection status。
   - `local_only`: 未配置 global registry。
   - `transactional`: global primary 与 local projection 同 SQLite transaction。
   - `write_through`: 分步写入，可 repair。
   - `reconcile_only`: 写路径不直接同步，只依赖 reconcile materialize。
2. 在 debug/panel 中展示 projection status。
   - runtime projection。
   - team projection。
   - reason。
   - source。
3. 补齐失败恢复测试。
   - global writer 失败时 local projection 保留。
   - repair 后 global row 创建并回填 `global_seq`。
   - global-only row 能 repair 回 local projection。
   - 重复 reconcile 幂等，不重复唤醒。
4. 文档化一致性语义。
   - SQLite same DB: 强一致。
   - SQLite attachable separate DB: 同事务投影。
   - SQLite non-attachable DSN: write-through eventual consistency。
   - InMemory: write-through 或 local-only。
   - future remote store: reconcile-only 或显式 remote transaction。

主要文件:

- `backend/internal/agentcontrol/mailbox.go`
- `backend/internal/chat/session_runtime_store.go`
- `backend/internal/team/sqlite_store.go`
- `backend/internal/chat/session_runtime_store_test.go`
- `backend/internal/team/sqlite_store_test.go`
- `backend/cmd/aicli/commands/chat_debug.go`

验收标准:

- 每种 projection mode 均可从调试输出中看到。
- fallback 失败不会丢 local mailbox。
- repair/reconcile 有测试证明可补偿。

### P4. Multi-Agent TUI 面板增强

目标:

把 `/agents panel` 从一次性排障 snapshot 扩展为可持续观察、多 target 导航、可与 mailbox/timeline 联动的 TUI 面板。

实施项:

1. 完善命令入口。
   - `/agents panel`
   - `/agents panel follow timeout=<duration> [limit]`
   - `/agents panel target <path|agent_id|session_id>`
   - `/agents panel next`
   - `/agents panel prev`
2. 固化 panel state。
   - selected target。
   - focused pane。
   - cursor index。
   - follow mode。
   - filter text。
3. 接入 wake。
   - mailbox wake 刷新 mailbox pane。
   - agent wake 刷新 graph pane。
   - task/timeline wake 刷新 timeline pane。
4. 保持降级策略。
   - fixed-bottom surface 可用时用 popup/panel。
   - legacy terminal 输出 snapshot。
   - 非交互或 JSON 模式不进入长驻 UI。
5. 防止输入误投递。
   - target 切换只影响 `/agents send` 和 `/agents followup` 默认目标。
   - 普通 root 输入仍进入 parent session。

主要文件:

- `backend/cmd/aicli/commands/chat_debug.go`
- `backend/cmd/aicli/commands/chat_interaction.go`
- `backend/cmd/aicli/commands/chat_slash_command_catalog.go`
- `backend/cmd/aicli/commands/chat_session_info_test.go`
- `backend/cmd/aicli/ui/inputbox_editor.go`

验收标准:

- panel target 切换可持久化 selected target。
- next/prev 可在 agent list 中稳定切换。
- follow 能等待 mailbox 更新并刷新视图。
- root 输入不会因为 selected target 变化而隐式投 child。

### P5. 真实终端与 Provider 验证

目标:

用真实 `aicli chat`、真实终端、真实 provider 验证用户最初报告的问题已经收敛。

验证项:

1. 多 `spawn_agent` 并行。
   - child 原始 assistant delta 不进入主控制台。
   - child reasoning 不进入主控制台。
   - parent 通过 `/collab all`、`/agents panel` 看到结构化 mailbox。
2. `spawn_team auto_start=true`。
   - 多 teammate 并行 assignment 不出现 busy error storm。
   - task lifecycle 可通过 timeline/mailbox 观察。
   - team 最终收敛为 done/failed/cancelled。
3. Ctrl+C。
   - 第一次 Ctrl+C 取消 active child/team，不退出 chat loop。
   - cancelled task 写入 lifecycle mailbox。
   - provider context 取消后不继续写 terminal team summary。
   - 第二次 Ctrl+C 退出。
4. provider stream error。
   - stream/internal error 不导致后台 team 永久 running。
   - 错误进入结构化 event/mailbox。

证据保存:

- Session ID。
- Session File。
- Chat Log File。
- Debug Log File。
- HTTP Artifact Dir。
- Shell Artifact Dir。
- 执行 prompt。
- `/agents panel`、`/collab all`、`/timeline active` 关键输出摘要。
- 通过/失败结论。

建议保存位置:

```text
docs/working/multi-agent-real-terminal-validation-YYYYMMDD.md
```

验收标准:

- 真实日志证明 child/team 原始输出不污染 parent console。
- Ctrl+C 行为与单元测试一致。
- provider 错误能稳定收敛，不挂死。

## 6. 推荐执行顺序

建议顺序如下:

1. P0 基线冻结与现状验证。
2. P1 Registry Service 生命周期收敛。
3. P2 Display Mirror 依赖压缩。
4. P3 Projection 兼容矩阵闭环。
5. P4 Multi-Agent TUI 面板增强。
6. P5 真实终端与 Provider 验证。

顺序原因:

- P0 先保证当前代码可测，避免后续排障混入已有编译问题。
- P1 是跨进程、多入口、durable watch 的基础。
- P2 必须早于 TUI 深化，否则 TUI 可能继续绑定 legacy mirror。
- P3 让 debug 输出能解释一致性语义，便于真实验证时判断问题归属。
- P4 依赖 registry、mailbox、wake、projection 都稳定。
- P5 必须在关键实现完成后执行，否则真实验证只能证明旧机制行为。

## 7. 测试门禁

### 7.1 阶段内最小门禁

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./cmd/aicli/commands -run "AgentsPanelCanSwitchTarget|AgentsPanelFollow|AgentsPanel|Slash" -count=1
go test ./internal/agentcontrol ./internal/chat ./internal/team ./internal/api/skills ./cmd/aicli/commands -count=1
```

### 7.2 Registry / Projection 专项门禁

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./internal/agentcontrol ./internal/chat ./internal/team -run "Registry|Projection|Mailbox|AgentControl|Wake|Repair|Reconcile" -count=1
```

### 7.3 提交前门禁

```powershell
git diff --check
cd E:\projects\ai\ai-agent-runtime\backend
go test ./...
```

### 7.4 真实终端门禁

执行:

```text
docs/plan/multi-agent-real-terminal-validation-runbook.md
```

并保存验证摘要到:

```text
docs/working/multi-agent-real-terminal-validation-YYYYMMDD.md
```

## 8. 风险与控制

| 风险 | 影响 | 控制方式 |
| --- | --- | --- |
| 当前工作树已有测试编译错误 | 后续实现无法判断新旧问题 | P0 先修复并冻结基线 |
| display mirror 被新入口继续当作权威 | 子 agent 输出污染和完成信号混乱复发 | no-mirror 测试覆盖 `/collab`、`/agents panel`、wait/read |
| SQLite registry 跨实例 busy/lock | 并发 spawn/mailbox 写失败或延迟 | WAL、busy timeout、并发测试、明确 close 语义 |
| non-SQLite projection 被误判为强一致 | 排障时错判 mailbox 丢失或重复 | debug 输出 projection mode 和 reason |
| TUI follow 抢输入或刷屏 | 交互体验退化 | fixed-bottom gating，legacy fallback，非交互禁用 |
| Ctrl+C 只取消 parent 不取消后台 team | 后台任务继续输出或占用 provider | active run registry、task lifecycle mailbox、真实终端 runbook |
| provider stream error 与 fake provider 行为不同 | 后台 team 挂死 | 真实 provider 验证，失败归档后补确定性测试 |

## 9. 完成定义

本计划完成必须同时满足:

- 当前工作树 `go test ./...` 通过。
- `git diff --check` 通过。
- registry service lifecycle、health、projection mode 在 `/debug` 或 `/agents panel` 可见。
- no-mirror 场景下 `/collab`、`/agents panel`、wait/read 仍能读取 child completion。
- projection failure/repair/reconcile 测试覆盖 runtime 和 team store。
- `/agents panel` 支持 follow、target、next、prev，并保持 legacy fallback。
- 真实终端 runbook 已执行并保存验证摘要。
- 文档不再把 legacy display mirror 描述为通信模型或数据权威。

## 10. 后续可选方向

这些方向不阻塞本计划完成，但适合作为后续版本:

- 把 registry service 从进程内生命周期继续推进为可选 daemon。
- 为 runtime server 暴露更完整的 AgentControl health API。
- 为 TUI panel 增加按 agent、task、trace、error 的交互过滤。
- 为真实终端 runbook 增加自动化 harness，减少人工观察成本。
- 为 provider stream error 分类建立统一错误码，便于 timeline 与 mailbox 展示。
