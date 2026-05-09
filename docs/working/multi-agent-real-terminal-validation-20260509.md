# Multi-Agent 真实终端验证摘要

日期: 2026-05-09

## 1. 验证范围

本摘要对应:

- `docs/plan/multi-agent-next-stage-implementation-plan.md`
- `docs/plan/multi-agent-real-terminal-validation-runbook.md`

本次在当前 Codex 执行环境中完成了可自动化验证与最新二进制构建；真实 provider 流式输出和人工 Ctrl+C 交互仍需在 Windows Terminal 等真实终端中按 runbook 执行。

## 2. 当前二进制

已基于当前工作树重新构建:

```text
E:\projects\ai\ai-agent-runtime\backend\aicli.exe
```

构建命令:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go build -o aicli.exe ./cmd/aicli
```

构建结果:

```text
Length=31024640
LastWriteTime=2026-05-09 12:23:24
```

## 3. 已自动验证项目

### 3.1 RegistryService lifecycle 与并发

已通过测试覆盖:

- service mode: `disabled` / `single_sqlite` / `split_sqlite`
- health 输出
- idempotent close
- close 后 store 访问返回 `ErrRegistryServiceClosed`
- 两个 service 实例同时打开同一 SQLite registry DB
- 并发 spawn reservation 不突破 `maxThreads`
- 并发 global-primary mailbox append
- 一个实例 close 不影响另一个实例继续读写

证据:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./internal/agentcontrol ./internal/chat ./internal/team -run "Registry|Projection|Mailbox|AgentControl|Wake|Repair|Reconcile" -count=1
```

结果:

```text
ok github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol
ok github.com/wwsheng009/ai-agent-runtime/internal/chat
ok github.com/wwsheng009/ai-agent-runtime/internal/team
```

### 3.2 Display mirror 依赖压缩

已通过测试覆盖:

- 只写 parent AgentControl completion mailbox，不写 `subagent.completed` display mirror。
- `/collab` 仍能显示 completion mailbox。
- `/agents panel` 仍能显示 completion mailbox。
- `read_agent_events mailbox_only` 仍能读取 completion mailbox。
- `read_agent_events` 无目标路径可从 parent completion mailbox 读取事件。

关键测试:

```text
TestHandleCommand_CollabAndPanelUseCompletionMailboxWithoutDisplayMirror
TestLocalActorRegistry_ReadEventsWithoutTargetUsesCompletionMailboxWithoutDisplayMirror
```

### 3.3 Projection capability 与 repair

已通过测试覆盖:

- runtime SQLite projection status。
- runtime in-memory projection status。
- team SQLite projection status。
- write-through global writer 失败后 local projection 保留。
- 后续 repair 可创建 global row 并回填 `global_seq`。
- global/local projection repair 与 reconcile 专项测试通过。

关键测试:

```text
TestSQLiteRuntimeStoreMailboxProjectionStatus
TestInMemoryRuntimeStoreMailboxProjectionStatus
TestSQLiteStoreMailboxProjectionStatus
TestSQLiteRuntimeStoreWriteThroughFailureKeepsLocalProjectionRepairable
TestSQLiteStoreWriteThroughFailureKeepsLocalProjectionRepairable
```

### 3.4 `/agents panel` follow/navigation

已通过测试覆盖:

- legacy `/agents panel` snapshot 保持。
- `/agents panel follow timeout=...` 可等待 mailbox 更新并刷新输出。
- `/agents panel target <target>` 可切换 selected target。
- `/agents panel next` / `/agents panel prev` 可在 agent target 间切换。
- fixed-bottom modal controller 支持上下移动 agent 游标、左右切换 pane、Enter 选中 target 并持久化。
- 输入编辑器 hook 支持左右方向键被 modal 面板消费。
- Esc 可退出 modal 输入。
- modal 输入收到 interactive interrupt 时会标记 session interrupted 并按取消路径返回。

关键测试:

```text
TestHandleCommand_AgentsPanelFollowWaitsForMailboxUpdate
TestHandleCommand_AgentsPanelCanSwitchTarget
TestChatAgentPanelModalControllerNavigatesAndSelectsTarget
TestRunChatAgentPanelModalInterruptMarksSessionInterrupted
TestReadInteractiveLineWithHooks_OnMoveConsumesCursorMovement
TestReadInteractiveLineWithHooks_OnCancelCanExitModal
```

### 3.5 `/debug` registry/projection 可见性

已通过测试覆盖:

- `/debug` 输出 `AgentControl Registry:`。
- `/debug` 复用 `/agents panel` 的 registry summary。
- 输出包含 `service=on`、`service_health=ok`、`mode=single_sqlite`、`shared_db=true`。
- 输出包含 runtime projection mode，例如 `runtime_projection=local_only:global_writer_not_configured@runtime_in_memory`。

关键测试:

```text
TestHandleCommand_DebugShowsAgentControlRegistryServiceMode
```

## 4. 测试门禁结果

### 4.1 Panel / UI 专项

命令:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./cmd/aicli/ui ./cmd/aicli/commands -run "ReadInteractiveLineWithHooks|AgentsPanelCanSwitchTarget|AgentsPanelFollow|AgentPanelModal|AgentsPanel|Slash" -count=1
```

结果:

```text
ok github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui
ok github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands
```

### 4.2 核心包门禁

命令:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./internal/agentcontrol ./internal/chat ./internal/team ./internal/api/skills ./cmd/aicli/commands -count=1
```

结果:

```text
ok github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol
ok github.com/wwsheng009/ai-agent-runtime/internal/chat
ok github.com/wwsheng009/ai-agent-runtime/internal/team
ok github.com/wwsheng009/ai-agent-runtime/internal/api/skills
ok github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands
```

### 4.3 完整测试

命令:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./...
```

结果:

```text
ok github.com/wwsheng009/ai-agent-runtime/cmd/aicli
ok github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands
ok github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui
ok github.com/wwsheng009/ai-agent-runtime/cmd/runtime-server
ok github.com/wwsheng009/ai-agent-runtime/internal/agent
ok github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol
ok github.com/wwsheng009/ai-agent-runtime/internal/api/skills
ok github.com/wwsheng009/ai-agent-runtime/internal/chat
ok github.com/wwsheng009/ai-agent-runtime/internal/team
ok github.com/wwsheng009/ai-agent-runtime/internal/toolbroker
ok github.com/wwsheng009/ai-agent-runtime/pkg/skillsapi
```

完整命令退出码为 0。

### 4.4 Diff 检查

命令:

```powershell
cd E:\projects\ai\ai-agent-runtime
git diff --check
```

结果:

```text
exit code 0
```

## 5. 当前环境未完成的真实交互项

本轮已尝试真实 provider smoke test:

```powershell
cd E:\projects\ai\ai-agent-runtime
backend\aicli.exe chat --no-interactive --request-timeout 60s --message "请只回复 OK。"
```

结果:

```text
Error: 操作错误: streaming aggregate call failed after retries: HTTP 401
message=Invalid API Key
type=invalid_key
```

本次失败产生的近期日志目录:

```text
C:\Users\vince\.aicli\chat-logs\20260509_123305
```

随后使用显式 provider 再次尝试:

```powershell
cd E:\projects\ai\ai-agent-runtime
backend\aicli.exe chat --provider mimo_anthropic --no-interactive --request-timeout 60s --message "请只回复 OK。"
```

结果同样为:

```text
HTTP 401
message=Invalid API Key
type=invalid_key
```

以下项目不能在当前非交互 Codex 执行环境中被真实完成:

1. 真实 provider 流式输出隔离验证。
2. 真实模型 reasoning block 是否在 parent console 隔离。
3. 人工第一次 Ctrl+C 取消 active child/team、第二次 Ctrl+C 退出 chat loop。
4. Windows Terminal fixed-bottom modal 的视觉刷新质量。

原因:

- 当时从仓库根目录执行 `backend\aicli.exe`，未加载 `backend\configs` 下的真实 provider 配置，因此返回 `HTTP 401 Invalid API Key`。
- 当前 shell 工具不是人工交互终端，无法真实按 Ctrl+C 并观察 TUI surface。
- 单元测试可以覆盖 signal/cancel、provider slow/stream error、mailbox lifecycle，但不能替代真实终端视觉与 provider streaming 验证。

## 6. 后续真实终端执行入口

在真实 Windows Terminal 中继续执行:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli.exe chat
```

按 `docs/plan/multi-agent-real-terminal-validation-runbook.md` 执行以下场景:

- 验证 A: spawn_agent 并行输出隔离。
- 验证 B: spawn_team auto_start 并行与 busy 收敛。
- 验证 C: Ctrl+C 中断收敛。
- 验证 D: provider stream error 收敛。

验证后需要补充:

- Session ID。
- Session File。
- Chat Log File。
- Debug Log File。
- HTTP Artifact Dir。
- Shell Artifact Dir。
- `/agents panel`、`/collab all`、`/timeline active` 的关键输出摘要。
- 是否通过，以及失败时的具体异常。

## 7. 当前结论

自动化层面已经覆盖并通过计划中的 registry lifecycle、display mirror no-mirror、projection fallback/repair、panel follow/navigation 和完整 Go 测试门禁。

真实终端/provider 层面已有 runbook 和当前二进制，但尚未在本环境中完成真实人工交互验证。因此该部分不能标记为真实通过，只能标记为“待真实终端执行”。

## 8. 2026-05-09 后续自动化复验

本轮继续推进 `/agents panel follow` 的 wake 完整性，修复并验证 fixed-bottom modal 对 task wake 的刷新路径。

变更点:

- `watchChatAgentPanelModalUpdates` 已覆盖 mailbox wake、agent wake、task wake。
- `TestChatAgentPanelModalWatchesTaskWake` 使用真实 `team.SQLiteStore` 创建 pending task 后更新状态，以 `task.status` 信号触发 AgentControl task wake。
- `docs/plan/multi-agent-next-stage-implementation-plan.md` 已更新 P2 状态，不再保留“agent/task wake 后续扩展”的过期表述。

本轮通过的命令:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./cmd/aicli/commands -run "PanelModal|AgentsPanel" -count=1
```

结果:

```text
ok github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands
```

```powershell
go test ./cmd/aicli/commands ./cmd/aicli/ui ./internal/agentcontrol ./internal/chat ./internal/team -run "DebugShowsAgentControl|PanelModal|AgentsPanel|ReadInteractiveLineWithHooks|Registry|Projection|Mailbox|Repair|Reconcile|Wake" -count=1
```

结果:

```text
ok github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands
ok github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui
ok github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol
ok github.com/wwsheng009/ai-agent-runtime/internal/chat
ok github.com/wwsheng009/ai-agent-runtime/internal/team
```

```powershell
go test ./internal/agentcontrol ./internal/chat ./internal/team ./internal/api/skills ./cmd/aicli/commands ./cmd/aicli/ui -count=1
```

结果:

```text
ok github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol
ok github.com/wwsheng009/ai-agent-runtime/internal/chat
ok github.com/wwsheng009/ai-agent-runtime/internal/team
ok github.com/wwsheng009/ai-agent-runtime/internal/api/skills
ok github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands
ok github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui
```

```powershell
go test ./...
git diff --check
```

结果:

```text
go test ./... 通过
git diff --check 通过
```

当前仍未完成的门禁:

- 真实终端 Ctrl+C 与 fixed-bottom modal 视觉刷新仍需人工终端执行 runbook。
- 真实 provider smoke test 必须从 `backend` 工作目录执行，确保加载 `backend\configs\.env` 与 `backend\configs\config.yaml`。

本轮使用重新构建后的二进制再次执行 `mimo_anthropic` smoke test:

```powershell
cd E:\projects\ai\ai-agent-runtime
backend\aicli.exe chat --provider mimo_anthropic --no-interactive --request-timeout 60s --message "请只回复 OK。"
```

结果仍为:

```text
HTTP 401
message=Invalid API Key
type=invalid_key
```

该失败后来确认是工作目录导致的配置发现问题: 从仓库根目录执行 `backend\aicli.exe ...` 不会加载 `backend\configs\.env` / `backend\configs\config.yaml`。

## 9. 2026-05-09 Provider Smoke Test 更正

用户在真实命令行中从 `backend` 目录直接执行:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli.exe chat --provider mimo_anthropic --no-interactive --request-timeout 60s --message "请只回复 OK。"
```

结果:

```text
OK
```

本轮复验一致通过，并通过辅助脚本生成记录:

```powershell
cd E:\projects\ai\ai-agent-runtime
.\scripts\validate-multi-agent-real-terminal.ps1 -Provider mimo_anthropic -SkipBuild
```

生成报告:

```text
docs/working/multi-agent-real-terminal-validation-20260509-130054.md
```

结论:

- `mimo_anthropic` provider smoke test 当前可用。
- 此前记录的 `HTTP 401 Invalid API Key` 不是 provider 凭证当前不可用的证据，而是从仓库根目录执行导致配置文件发现路径不同。
- 后续真实验证必须从 `backend` 工作目录执行，或显式指定等价 config/env。
- 真实终端 Ctrl+C、Windows Terminal fixed-bottom modal 视觉刷新、多 agent 流式输出隔离仍需人工终端 runbook 继续验证。

## 10. 2026-05-09 真实 Provider 非交互 spawn_agent 复验

本轮在 `backend` 工作目录使用真实 `mimo_anthropic` provider 执行非交互 multi-agent 验证，覆盖 `spawn_agent`、`wait_agent`、`read_agent_events` 的真实工具链路。

构建命令:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
$env:GOMAXPROCS='2'
go build -p 1 -o aicli.exe ./cmd/aicli
```

构建结果:

```text
E:\projects\ai\ai-agent-runtime\backend\aicli.exe
Length=31070720
LastWriteTime=2026-05-09 13:41:00
```

执行命令:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli.exe chat --provider mimo_anthropic --no-interactive --request-timeout 240s --message "请使用 spawn_agent 并行启动 2 个子 agent。agent A 查看 E:\projects\ai\ai-agent-runtime\docs\plan\multi-agent-real-terminal-validation-runbook.md 并总结验证目标；agent B 查看 E:\projects\ai\ai-agent-runtime\docs\plan\multi-agent-next-stage-implementation-plan.md 并总结当前剩余门禁。等待两个子 agent 完成后，parent 用不超过 200 字中文汇总。"
```

### 10.1 第一次真实复验暴露的问题

第一次复验产生 session:

```text
Session File: C:\Users\vince\.aicli\sessions\session_20260509132737_VLsxL9tT.json
Chat Log Dir: C:\Users\vince\.aicli\chat-logs\20260509_132737
```

证据:

- `spawn_agent` 成功创建 `agent-a` 与 `agent-b`，返回 `created, queued`。
- 没有再出现 `UNIQUE constraint failed: agent_control_agents.session_id`。
- 但 `wait_agent agent-a/agent-b` 被旧 `spawn_team` teammate 同名记录误拒绝:

```text
wait_agent is a spawn_agent child-session tool, but "agent-a" is a spawn_team teammate id in team "team_538b37151dcd4c359c8f4bd3faeec91c"
```

根因:

- 历史 team store 中存在同名 teammate id，例如 `agent-a` / `agent-b`。
- broker 的 `rejectTeamTeammateAgentRefs` 在调用当前 root 的 spawn_agent controller 前，直接按 teammate id 拒绝。
- 当当前 root 下确实存在同名 lightweight child agent 时，应优先解析为当前 child，而不是被历史 team teammate 记录拦截。

修复:

- `backend/internal/toolbroker/broker.go`
  - `rejectTeamTeammateAgentRefs` 增加 `parentSessionID` 入参。
  - 拒绝 team teammate 前先调用当前 root 的 `AgentSessions.List(..., IncludeClosed=true)` 收集当前 `spawn_agent` child refs。
  - 仅当 ref 不属于当前 child refs 时，才按 `TeamStore.GetTeammate` 拒绝。
  - 当前 child refs 只接受 `agent_type=child` 或无 team metadata 的 lightweight agent，避免真正的 team teammate 被误放行。
- `backend/internal/toolbroker/broker_agent_test.go`
  - 新增 `TestBroker_Execute_AgentToolsPreferCurrentSpawnAgentWhenIDCollidesWithTeammate`，覆盖 child id 与历史 teammate id 同名时 `wait_agent` / `read_agent_events` 应优先走 current spawn_agent child。

此前同一轮还修复了真实 provider 暴露的 stale AgentControl session binding:

- `backend/cmd/aicli/commands/chat_actor_registry.go`
  - spawn/materialize 前关闭同 session_id 的 stale active AgentControl binding。
- `backend/cmd/aicli/commands/chat_actor_host_test.go`
  - 新增 `TestLocalActorRegistry_SpawnClosesStaleSessionBinding`。
  - 新增 `TestLocalActorRegistry_ConcurrentSpawnAgentReservationsUseUniqueSessions`。

### 10.2 修复后真实复验通过

修复后重新构建并再次执行同一真实 provider 命令，结果成功。

最新证据:

```text
Session File: C:\Users\vince\.aicli\sessions\session_20260509134110_FOzywBBW.json
Chat Log Dir: C:\Users\vince\.aicli\chat-logs\20260509_134110
Chat Log File: C:\Users\vince\.aicli\chat-logs\20260509_134110\chat_mimo_anthropic_anthropic_mimo-v2.5-pro_20260509_134110.json
Debug Log File: C:\Users\vince\.aicli\chat-logs\20260509_134110\debug.log
HTTP Artifact Dir: C:\Users\vince\.aicli\chat-logs\20260509_134110\runtime-http
Shell Artifact Dir: C:\Users\vince\.aicli\chat-logs\20260509_134110\local-shell
```

日志确认:

- `spawn_agent` 创建两个 child:
  - `session_ref_b7ef7b7f0bab`
  - `session_ref_bec7dcad5eb6`
- `wait_agent` 对两个 child 均成功返回 idle 与 output preview。
- `read_agent_events` 对两个 child 均成功读取 4 个事件:
  - `session_start`
  - `session_compact_skipped`
  - `assistant_message`
  - `session_end`
- 未再出现:
  - `UNIQUE constraint failed: agent_control_agents.session_id`
  - `spawn_team teammate id`
  - `session is busy (running)`

命令输出摘要:

```text
两个子 agent 均已完成。
验证目标: 在真实终端 + 真实 provider 环境下验证多 agent 输出隔离与事件收敛。
剩余门禁: Windows Terminal 下的人工 Ctrl+C 中断行为和 TUI 交互验证尚未通过。
```

### 10.3 本轮重新通过的测试门禁

串行核心测试:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
$env:GOMAXPROCS='2'
go test -p 1 ./internal/toolbroker ./cmd/aicli/commands ./cmd/aicli/ui ./internal/agentcontrol ./internal/chat ./internal/team ./internal/api/skills -count=1
```

结果:

```text
ok github.com/wwsheng009/ai-agent-runtime/internal/toolbroker
ok github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands
ok github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui
ok github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol
ok github.com/wwsheng009/ai-agent-runtime/internal/chat
ok github.com/wwsheng009/ai-agent-runtime/internal/team
ok github.com/wwsheng009/ai-agent-runtime/internal/api/skills
```

完整测试与 diff 检查:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
$env:GOMAXPROCS='2'
go test -p 1 ./...

cd E:\projects\ai\ai-agent-runtime
git diff --check
```

结果:

```text
go test ./... 通过
git diff --check 通过
```

当前仍未完成的真实交互门禁:

- Windows Terminal 中真实按键 Ctrl+C:
  - 第一次取消 active child/team，不退出 chat loop。
  - 第二次退出。
  - cancelled task 写入 lifecycle mailbox。
- Windows Terminal fixed-bottom TUI 面板视觉刷新和键盘导航人工确认。
  - 自动化已覆盖 modal follow/navigation/interrupt/wake，但不能替代真实终端 surface 验证。

### 10.4 验证脚本补强

`scripts/validate-multi-agent-real-terminal.ps1` 已从 smoke-test 模板升级为两段自动验证:

1. provider smoke test:
   - 从 `backend` 工作目录执行。
   - 验证 `mimo_anthropic` 可返回 `OK`。
2. 真实 provider 非交互 `spawn_agent` probe:
   - 执行 `spawn_agent -> wait_agent -> read_agent_events`。
   - 自动记录最新 session/chat/debug/http/shell artifact。
   - 自动检查 session 中包含 `spawn_agent`、`wait_agent`、`read_agent_events`。
   - 自动检查未出现 `UNIQUE constraint failed: agent_control_agents.session_id`、`spawn_team teammate id`、`session is busy (running)`。

本轮脚本复验命令:

```powershell
cd E:\projects\ai\ai-agent-runtime
.\scripts\validate-multi-agent-real-terminal.ps1 -Provider mimo_anthropic -SkipBuild
```

生成报告:

```text
docs/working/multi-agent-real-terminal-validation-20260509-134956.md
```

报告结论:

```text
provider smoke test 通过
真实 provider 非交互 spawn_agent 验证通过
Required found: spawn_agent, wait_agent, read_agent_events
Forbidden found: <none>
```

该脚本仍不能替代 Windows Terminal 真实 Ctrl+C 和 fixed-bottom TUI 视觉验证；报告中保留了人工验证 checklist。

## 11. 2026-05-09 真实 Provider spawn_team + wait_team 复验通过

本轮继续修复并验证 `spawn_team auto_start=true -> wait_team` 的真实 provider 链路。

### 11.1 失败根因

上一轮报告 `docs/working/multi-agent-real-terminal-validation-20260509-150320.md` 中，`spawn_team + wait_team` 命令退出 `context deadline exceeded`。复查 session 发现根因不是 team summary 生成链路本身，而是两个实现/验证问题叠加:

- `mimo_anthropic` 将 `spawn_team.tasks` 作为 JSON 数组字符串传入；broker 当时只接受 `[]interface{}`，导致 tasks 被静默忽略，创建了 `3 teammates and 0 tasks` 的 active 空 team。
- `spawn_team` 的 cache-safe 摘要没有展示 `team_id`，模型只能省略 `team_id` 调用 `wait_team`，并在空 team 上重复等待直至超时。
- 验证脚本的提示词要求读取 `docs/plan` 大文档；当 `aicli` 从 `backend` 工作目录运行时，模型会尝试复制/嵌入仓库根目录文档内容，造成上下文膨胀和不稳定等待。

### 11.2 修复内容

代码修复:

- `backend/internal/toolbroker/broker.go`
  - `spawn_team.teammates` / `spawn_team.tasks` 现在兼容 JSON 数组字符串、`[]interface{}` 与 `[]map[string]interface{}`。
  - 非法格式直接返回错误，不再静默吞掉任务。
- `backend/internal/toolbroker/cache_safe_summary.go`
  - `spawn_team` cache-safe 摘要现在包含 `team_id`，例如 `Created team run team_xxx with 3 teammates and 3 tasks.`，便于 parent 模型直接调用 `wait_team`。
- `scripts/validate-multi-agent-real-terminal.ps1`
  - `spawn_agent` 与 `spawn_team` probe 改为内联短任务，不再要求读取大文档，降低真实 provider 验证的不确定性。
  - `spawn_team` probe 明确要求基于 `team_id` 调用 `wait_team`。

新增/调整测试:

- `TestBrokerExecuteSpawnTeamAcceptsJSONStringTeammatesAndTasks`
- `TestBrokerSpawnTeamCacheSafeSummaryIncludesTeamIDAndOmitsTaskIDsInEnvelope`

### 11.3 自动化门禁

目标包测试:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
$env:GOMAXPROCS='1'
$env:GOGC='50'
go test -p 1 ./internal/toolbroker ./internal/team ./cmd/aicli/commands ./pkg/skillsapi -count=1
```

结果:

```text
ok github.com/wwsheng009/ai-agent-runtime/internal/toolbroker
ok github.com/wwsheng009/ai-agent-runtime/internal/team
ok github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands
ok github.com/wwsheng009/ai-agent-runtime/pkg/skillsapi
```

完整测试、构建与 diff 检查:

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
$env:GOMAXPROCS='1'
$env:GOGC='50'
go test -p 1 ./...
go build -p 1 -o aicli.exe ./cmd/aicli

cd E:\projects\ai\ai-agent-runtime
git diff --check
```

结果:

```text
go test ./... 通过
go build 通过
git diff --check 通过
```

当前二进制:

```text
E:\projects\ai\ai-agent-runtime\backend\aicli.exe
Length=31084032
LastWriteTime=2026-05-09 15:40:14
```

### 11.4 真实 provider 非交互复验

执行命令:

```powershell
cd E:\projects\ai\ai-agent-runtime
.\scripts\validate-multi-agent-real-terminal.ps1 -Provider mimo_anthropic -SkipBuild
```

生成报告:

```text
docs/working/multi-agent-real-terminal-validation-20260509-154143.md
```

结论:

```text
provider smoke test 通过
真实 provider 非交互 spawn_agent 验证通过
真实 provider 非交互 spawn_team + wait_team 验证通过
Forbidden found: <none>
```

关键 artifact:

```text
spawn_agent Session File: C:\Users\vince\.aicli\sessions\session_20260509154157_zD7gJ6H2.json
spawn_agent Chat Log Dir: C:\Users\vince\.aicli\chat-logs\20260509_154157
spawn_team Session File: C:\Users\vince\.aicli\sessions\session_20260509154321_UVtIPaEZ.json
spawn_team Chat Log Dir: C:\Users\vince\.aicli\chat-logs\20260509_154321
```

`spawn_team` session 证据:

```text
Created team run team_4fce44118a704162a84444e41ce996fc with 3 teammates and 3 tasks.
wait_team arguments: team_id=team_4fce44118a704162a84444e41ce996fc, require_summary=true, timeout_ms=60000
Team status=done
Summary is ready
Returned 15 lifecycle events
```

未出现以下回归信号:

```text
UNIQUE constraint failed: agent_control_agents.session_id
spawn_team teammate id
session is busy (running)
context deadline exceeded
0 tasks
```

### 11.5 仍未完成的真实人工门禁

当前已完成真实 provider 非交互链路验证，但以下仍必须在 Windows Terminal 人工完成:

- 第一次 Ctrl+C 取消 active child/team，且 chat loop 不退出。
- 第二次 Ctrl+C 退出 chat loop。
- cancelled task 写入 lifecycle mailbox，且 provider context 取消后不继续写 terminal team summary。
- fixed-bottom `/agents panel follow` 的视觉刷新、键盘导航和 target 快速切换在真实终端中表现稳定。
