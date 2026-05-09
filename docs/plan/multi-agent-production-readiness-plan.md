# Multi-Agent 生产就绪收尾计划

更新时间: 2026-05-09

## 1. 文档定位

本计划用于承接当前 multi-agent / AgentControl 主路径已经基本落地后的收尾阶段。它不是重新论证“是否迁移到 AgentControl”，也不是替代已有的设计分析文档，而是把下一步需要完成的工程门禁整理成可执行、可验证、可交付的清单。

相关文档:

- `docs/plan/multi-agent-framework-codex-comparison-plan.md`: 本项目与 Codex 多 agent 机制的对比分析。
- `docs/plan/multi-agent-agentcontrol-convergence-plan.md`: AgentControl 收敛阶段计划。
- `docs/plan/multi-agent-next-stage-implementation-plan.md`: 当前阶段实施计划与状态记录。
- `docs/plan/multi-agent-real-terminal-validation-runbook.md`: 真实终端与真实 provider 验证 runbook。
- `docs/working/multi-agent-real-terminal-validation-20260509.md`: 2026-05-09 的验证摘要与阻断原因。

本计划只关注最后一段收尾工作:

1. 确认当前工作树达到稳定可测试状态。
2. 消除残余 legacy mirror 依赖和隐式双写误用。
3. 把 registry、projection、wake、TUI、Ctrl+C 行为做成可观测、可回归的稳定机制。
4. 补齐真实终端和真实 provider 的端到端证据。
5. 在满足所有门禁后再进入提交、发布或后续功能开发。

## 2. 当前事实基线

截至本计划编写时，已知事实如下。

已完成或已基本完成:

- `agent_control_task_records` / `agent_control_task_dependencies` 已成为 task graph 主写入表。
- legacy `team_tasks` / `team_task_dependencies` 已退化为迁移输入或兼容层，不应再作为新逻辑权威。
- runtime/team mailbox 已收敛到 `agent_control_mailbox_records`。
- global mailbox registry 已支持 canonical global-primary 写入。
- local/global projection 已具备 repair 与 reconcile 能力。
- `agentcontrol.RegistryService` 已具备 mode、health、close、shared SQLite policy 与跨实例测试基础。
- display mirror 已被定位为 legacy compatibility，新展示入口应优先读取 AgentControl mailbox。
- `/debug` 与 `/agents panel` 已开始展示 registry/projection 状态。
- `/agents panel follow` 已具备 fixed-bottom modal、target 切换、上下移动、左右 pane 切换、Enter 选择、Esc 退出、interrupt 取消路径等基础交互能力。
- `/agents panel follow` 已接入 mailbox、agent、task wake，并有 modal/navigation/interrupt/wake 回归测试。
- 已有真实终端验证 runbook 与验证摘要。此前 `HTTP 401 Invalid API Key` 来自从仓库根目录执行 `backend\aicli.exe` 导致未加载 `backend\configs` 下的配置；从 `backend` 工作目录执行 provider smoke test 已通过。
- 真实 `mimo_anthropic` provider 非交互 `spawn_agent -> wait_agent -> read_agent_events` probe 已通过，并已固化进 `scripts/validate-multi-agent-real-terminal.ps1`。
- 真实 provider probe 暴露的 stale `agent_control_agents.session_id` 绑定冲突、child id 与历史 team teammate id 同名误拒绝问题已修复并补测试。
- `spawn_team` 父会话等待缺口已新增 `wait_team` broker 工具，parent 可用 `team_id` 等待 durable `team.completed/team.summary`，脚本已新增 `spawn_team auto_start=true -> wait_team` probe。

仍不能宣称完全完成的点:

- 真实 provider 非交互多 agent probe 已通过，但 Windows Terminal 中的真实 streaming 视觉隔离、reasoning 隔离、fixed-bottom TUI 视觉刷新和真实 Ctrl+C 行为仍未由人工终端证据证明。
- `/agents panel` 的 mailbox、agent、task wake 已纳入刷新模型；timeline 当前仍复用 task lifecycle wake，真实终端中的视觉刷新质量仍需人工确认。
- registry service 目前仍偏进程内 service 形态，后续需要进一步明确跨 CLI、runtime server、Skills API 的单一 durable registry 生命周期。
- 非 SQLite / 不可 attach store 的一致性语义需要继续保持 debug 可见、测试覆盖和文档一致。
- 若当前工作树存在未完成测试或编译错误，必须先修复后再推进下一阶段。

## 3. 总目标

本收尾阶段完成后，multi-agent 机制应达到以下标准:

- AgentControl 是 child/team 协作通信、task lifecycle、mailbox completion、agent identity 的唯一新主路径。
- legacy display mirror 只作为兼容输出存在，新展示和等待逻辑不依赖它。
- registry service 的运行模式、健康状态、projection mode、watcher 状态可通过 `/debug` 和 `/agents panel` 解释清楚。
- parent console 不被 child 原始 assistant delta、reasoning block、tool stream 或 teammate 临时输出污染。
- Ctrl+C 第一次稳定取消 active child/team 并写入 lifecycle mailbox/timeline，第二次才退出 chat loop。
- `/agents panel` 可作为日常排障入口持续观察 agent graph、mailbox、task/timeline 状态，并能切换 selected target。
- 自动化测试、完整测试、构建、diff 检查、真实终端 runbook 均有明确通过记录。

## 4. 非目标

本计划不做以下事情:

- 不恢复旧的 `team_tasks`、`team_task_dependencies`、`team_mailbox_messages`、`session_mailbox_messages` 或旧 wake mirror 表写路径。
- 不让普通 root 输入因为 selected target 存在就隐式投递给 child agent。向 child 投递必须继续使用 `/agents send`、`/agents followup` 或明确的 broker API。
- 不把 display mirror 直接删除作为第一步。先完成所有新入口去依赖，再考虑删除兼容输出或降级为可配置项。
- 不引入远程数据库、分布式锁或新的外部服务作为当前收尾前提。
- 不把 fake provider 单元测试等同于真实 provider streaming 和真实终端 Ctrl+C 验证。

## 5. 阶段拆分

### P0. 工作树稳定化与测试基线恢复

目标:

先保证当前工作树可编译、可测试、可审查。任何后续优化都必须建立在稳定基线上。

实施项:

1. 修复当前可能存在的测试编译错误。
   - 重点检查 `/agents panel`、task wake、agent wake、modal controller 相关新增测试。
   - 确认 `team.Task` 字段、`CreateTask` 返回值、task status wake 触发路径与当前接口一致。
2. 运行 gofmt。
   - 仅格式化本次改动涉及的 Go 文件。
3. 运行专项测试。
   - panel/modal/UI hook 专项。
   - registry/projection/mailbox/wake 专项。
4. 运行核心包测试与完整测试。
5. 运行 `git diff --check`。

建议命令:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./cmd/aicli/commands -run "PanelModal|AgentsPanel" -count=1
go test ./cmd/aicli/commands ./cmd/aicli/ui ./internal/agentcontrol ./internal/chat ./internal/team -run "DebugShowsAgentControl|PanelModal|AgentsPanel|ReadInteractiveLineWithHooks|Registry|Projection|Mailbox|Repair|Reconcile|Wake" -count=1
go test ./internal/agentcontrol ./internal/chat ./internal/team ./internal/api/skills ./cmd/aicli/commands ./cmd/aicli/ui -count=1
git diff --check
go test ./...
```

验收标准:

- 当前工作树可编译。
- 上述专项测试、核心测试、完整测试全部通过。
- `git diff --check` 无 whitespace error。
- 若存在失败，必须在文档或提交说明中记录失败原因，不能把该阶段标记完成。

### P1. AgentControl 主路径依赖审计

目标:

确认新功能不再依赖 legacy mirror 表、display mirror event 或旧 session event 作为通信权威。

实施项:

1. 审计写路径。
   - child completion 写入 AgentControl mailbox。
   - task lifecycle 写入 AgentControl task/mailbox。
   - team assignment 写入 AgentControl task graph。
   - agent identity 写入 AgentRegistry。
2. 审计读路径。
   - `/collab` 读取 AgentControl completion mailbox。
   - `/agents panel` 读取 AgentControl mailbox、AgentRegistry、task/timeline read model。
   - `wait_agent` / `read_agent_events` 不以 display mirror 作为唯一信号。
3. 保留 legacy compatibility 边界。
   - `subagent.completed` 如果仍写入，必须带 `display_mirror=true`。
   - mirror 必须标明来源，例如 `mirror_source=agent_control_mailbox`。
4. 增加或保留 no-mirror 回归测试。
   - 手动只写 AgentControl completion mailbox，不写 display mirror。
   - `/collab`、`/agents panel`、`read_agent_events mailbox_only` 仍可正常展示。

建议检查命令:

```powershell
rg -n "display_mirror|subagent.completed|team_tasks|team_task_dependencies|team_mailbox_messages|session_mailbox_messages" backend
```

验收标准:

- 新展示入口可在 display mirror 缺失时工作。
- 新通信逻辑不读取 legacy task/mailbox 表作为权威。
- 兼容 mirror 的存在不会改变 AgentControl mailbox 的完成语义。

### P2. Registry Service 生产化收口

目标:

把 registry service 从“可用的进程内 facade”推进为“可诊断、可关闭、可复用、可跨入口共享”的 durable substrate。

实施项:

1. 明确 owner。
   - local chat runtime host 持有 registry service。
   - runtime server / Skills API 使用同一生命周期模型。
   - 避免 handler 或 command 临时反复打开 registry DB。
2. 统一 health 输出。
   - mode: `disabled` / `single_sqlite` / `split_sqlite`。
   - shared DB 状态。
   - mailbox store 状态。
   - agent store 状态。
   - runtime/team projection mode。
   - closed / healthy / degraded 状态。
3. 强化关闭语义。
   - `Close()` 幂等。
   - close 后所有 store wrapper 返回明确错误。
   - watcher channel 主动关闭。
4. 强化跨实例并发。
   - 两个 registry service 打开同一 SQLite registry DB。
   - 并发 spawn reservation 不突破 `maxThreads`。
   - 并发 global-primary mailbox append 不产生重复 canonical row。
   - 一个实例关闭不影响另一个实例继续读写。

验收标准:

- `/debug` 与 `/agents panel` 展示一致的 registry summary。
- registry close 后无 panic、无静默写入、无 watcher 泄漏。
- 跨实例并发测试稳定通过。

### P3. Projection 一致性与非 SQLite 兼容

目标:

让 transactional、write-through、reconcile-only、local-only 四种 projection 模式都有明确语义、可见状态和修复测试。

实施项:

1. 固化 projection mode。
   - `transactional`: global primary 与 local projection 同事务。
   - `write_through`: local/global 分步写入，失败后可 repair。
   - `reconcile_only`: 写路径不强制同步，依靠 reconcile materialize。
   - `local_only`: 没有 global registry writer。
2. 增强 debug 可见性。
   - runtime projection 与 team projection 分开展示。
   - 对 degraded 或 fallback mode 输出原因。
3. 补齐修复测试。
   - global 写失败后 local row 保留。
   - repair 创建 global row 并回填 `global_seq`。
   - global-only row 可 repair 回 local projection。
   - 重复 reconcile 幂等。
4. 文档化支持矩阵。
   - SQLite same DB。
   - SQLite attachable separate DB。
   - SQLite non-attachable DSN。
   - InMemory。
   - 未来 remote store。

验收标准:

- 每一种 projection mode 均有测试或明确不可用说明。
- debug 输出能解释当前一致性语义。
- 非 SQLite store 不被误描述为强一致。

### P4. `/agents panel` 长驻交互面板完成

目标:

把 `/agents panel` 从 snapshot/follow 工具补齐为可长驻、可导航、可刷新、可排障的 multi-agent TUI 面板。

实施项:

1. 完整 panel state。
   - selected target。
   - focused pane: agent graph / mailbox / task timeline。
   - cursor index。
   - follow mode。
   - limit/filter。
2. 完整 wake 接入。
   - mailbox wake 刷新 mailbox pane。
   - agent wake 刷新 agent graph。
   - task wake 刷新 task/timeline pane。
   - timeline wake 或等价 task lifecycle wake 刷新 timeline。
3. 完整键盘行为。
   - 上下移动 agent cursor。
   - 左右切换 pane。
   - Enter 选中 target 并持久化。
   - Esc 退出 modal。
   - Ctrl+C 走 interrupt/cancel 路径，不留下后台刷新 goroutine。
4. 降级策略。
   - fixed-bottom surface 不可用时输出 snapshot。
   - 非交互模式不进入 modal。
   - legacy terminal 不刷屏。

验收标准:

- mailbox、agent、task wake 均可触发 panel refresh。
- modal controller 测试覆盖导航、选择、取消、中断。
- panel 输出不混入 child 原始 stream。

### P5. Ctrl+C 与 active run lifecycle 收敛

目标:

解决用户最初反馈的“Ctrl+C 无法中断子 agent 执行”问题，并留下可自动化与真实终端双重证据。

实施项:

1. active run registry。
   - root session 能知道当前 active child/team run。
   - Ctrl+C 第一击取消 active run。
   - Ctrl+C 第二击退出 chat loop。
2. provider ctx 取消。
   - child provider stream 能收到 context cancel。
   - team teammate auto-start 能收敛到 cancelled/failed terminal state。
3. lifecycle 写入。
   - task cancelled 写 task lifecycle。
   - child/team mailbox 写 cancellation event。
   - timeline 可观察到取消原因。
4. 输出收敛。
   - 取消后不再持续向 parent terminal 输出 child stream。
   - 后台 goroutine 退出或进入明确 terminal 状态。

验收标准:

- fake provider 慢响应测试通过。
- provider stream error 测试通过。
- signal handler 第一次/第二次 Ctrl+C 测试通过。
- 真实终端 runbook 能证明 Ctrl+C 行为符合预期。

### P6. 真实 Provider 与真实终端验证

目标:

用真实 `aicli chat`、真实 provider、真实 Windows Terminal 或等价交互终端验证端到端行为。

当前阻断:

- 当前执行环境不是人工交互终端，不能真实按 Ctrl+C 观察 TUI 和 stream 收敛。
- 真实 provider smoke test 已可从 `backend` 工作目录通过；后续真实验证必须保持该工作目录或显式指定等价配置。

实施项:

1. 凭证修复后重新构建二进制。
2. 可先运行 `scripts/validate-multi-agent-real-terminal.ps1` 生成 smoke test、`spawn_agent` probe、`spawn_team + wait_team` probe 与验证记录模板。
3. 按 `docs/plan/multi-agent-real-terminal-validation-runbook.md` 执行。
4. 至少覆盖以下场景。
   - 单 child agent。
   - 多 child agent 并行。
   - team auto-start 多 teammate。
   - parent 通过 `wait_team` 等待 team terminal summary，不再对 teammate id 调用 `wait_agent` / `read_agent_events`。
   - provider 慢响应。
   - provider stream error 或中途取消。
   - reasoning 输出开启时的 parent/child 隔离。
   - `/agents panel follow` 运行中切 target。
   - Ctrl+C 第一次取消、第二次退出。
5. 保存证据。
   - session file。
   - chat log。
   - debug log。
   - runtime-http artifact。
   - local-shell artifact。
   - 终端观察摘要。

验收标准:

- 真实 provider 不再返回认证错误。
- parent console 无 child reasoning/assistant delta 污染。
- 多 agent 并行输出只进入结构化协作视图或目标 mailbox。
- Ctrl+C 行为与设计一致。
- 验证摘要写入 `docs/working`。

### P7. 文档与提交收尾

目标:

确保代码、测试、验证、文档互相一致，再提交所有变更。

实施项:

1. 更新状态文档。
   - 若某阶段完成，修改对应计划中的状态。
   - 若真实验证仍阻断，明确写阻断原因，不写“已完成”。
2. 清理过期表述。
   - 不再强调“不是完整全局 AgentControl”这种已经过期的阶段性表述。
   - 改为描述当前剩余工程门禁，例如真实验证、watcher 完整性、projection fallback 等。
3. 最终命令门禁。

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./...
go build -o aicli.exe ./cmd/aicli
git diff --check
```

4. 提交前审查。
   - `git status --short`
   - `git diff --stat`
   - 核对新增文档路径。
   - 确认没有误提交临时凭证、私有 token 或超大 artifact。

验收标准:

- 文档中的完成状态与实际测试/验证证据一致。
- 所有计划文档的下一步指向清晰。
- 变更可一次性提交，提交说明准确描述 scope。

## 6. 风险清单

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| display mirror 再次被展示入口当作权威 | 子 agent 输出污染主 console，completion 状态不一致 | no-mirror 测试、读路径审计、mirror metadata 强制标注 |
| registry service 多入口重复打开 DB | 锁竞争、watcher 泄漏、状态不可诊断 | 统一 owner、health 输出、close 测试 |
| write-through projection 被误认为强一致 | 故障时 global/local 状态短暂不一致被误判为数据丢失 | projection mode debug、repair/reconcile 测试、文档矩阵 |
| panel follow watcher 不完整 | task 或 agent 状态变化后 UI 不刷新 | mailbox/agent/task wake 全覆盖测试 |
| Ctrl+C 只取消 parent，不取消后台 child/team | 后台任务继续占 provider 或继续输出 | active run registry、ctx cancel、lifecycle mailbox、真实终端 runbook |
| provider 凭证无效 | 无法完成真实 provider 门禁 | 明确阻断，不把 fake provider 测试当真实通过 |
| Windows 命令过长 | 补丁或脚本执行失败 | 分块补丁，避免超长 inline PowerShell/cmd |

## 7. 完成定义

只有同时满足以下条件，才能把 multi-agent 收尾阶段标记为完成:

- `go test ./...` 通过。
- `git diff --check` 通过。
- `go build -o aicli.exe ./cmd/aicli` 通过。
- display mirror no-mirror 回归测试通过。
- registry service lifecycle / projection / wake 专项测试通过。
- `/agents panel` mailbox、agent、task wake 和导航测试通过。
- fake provider 的 cancel、slow stream、stream error 测试通过。
- 真实 provider smoke test 通过，并完成真实终端 runbook。
- 真实终端验证摘要已写入 `docs/working`。
- 文档中没有把阻断项描述为已完成。

如果从错误工作目录执行导致 provider 返回 `HTTP 401 Invalid API Key`，不能作为真实 provider 不可用的证据；应改为从 `backend` 工作目录或显式配置路径复验。真实终端 Ctrl+C/TUI 验证未完成前，仍不能标记为整体完成。

## 8. 推荐执行顺序

1. 修复当前工作树编译和测试问题。
2. 跑 P0 专项测试和完整测试，冻结可用基线。
3. 审计 AgentControl 主路径与 legacy mirror 读写依赖。
4. 补齐 `/agents panel` mailbox、agent、task wake 测试和实现。
5. 强化 registry service health、owner、close 和并发用例。
6. 补齐 projection fallback 的 debug、repair、reconcile 证据。
7. 重新构建 `aicli.exe`。
8. 凭证有效后执行真实终端 runbook。
9. 更新 `docs/working` 验证摘要。
10. 运行最终门禁并提交。

## 9. 当前下一步

当前最优先处理顺序:

1. 从 `backend` 工作目录重新执行真实 provider smoke test。
2. 在真实 Windows Terminal 中按 `docs/plan/multi-agent-real-terminal-validation-runbook.md` 执行多 agent 并行、reasoning 隔离、Ctrl+C 中断和 `/agents panel follow` 验证。
3. 将真实验证结果补充到 `docs/working/multi-agent-real-terminal-validation-20260509.md` 或新的同类验证摘要。
4. 若真实验证暴露新的输出污染、取消不收敛或 TUI 刷新问题，再针对失败路径补测试并修复。
5. 真实门禁通过后，运行最终 `go test ./...`、`go build -o aicli.exe ./cmd/aicli`、`git diff --check`，再提交变更。

当前自动化层面已通过 panel/modal、registry/projection/mailbox/wake、完整 `go test ./...`、`go build` 和 `git diff --check`。provider smoke test 已从 `backend` 工作目录通过；剩余阻断集中在真实交互终端中的 Ctrl+C、TUI surface 和多 agent 流式输出观察。
