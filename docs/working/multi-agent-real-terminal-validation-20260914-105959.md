# Multi-Agent 真实终端验证记录

生成时间: 2026-09-14T10:59:59.7472077+08:00
Repo: E:\projects\ai\ai-agent-runtime
Provider: opencode.ai
Model: deepseek-v4.1-flash
ReasoningEffort: max
AICLI: E:\projects\ai\ai-agent-runtime\backend\aicli-probe.exe
WorkDir: E:\projects\ai\ai-agent-runtime\backend
Config: E:\projects\ai\ai-agent-runtime\output\real-test\config.team-probe.yaml（局部配置副本；用户全局配置未改动）

## 1. 构建

跳过构建: -SkipBuild

## 2. Provider Smoke Test

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli-probe.exe chat --provider opencode.ai --model deepseek-v4.1-flash --reasoning-effort max --config E:\projects\ai\ai-agent-runtime\output\real-test\config.team-probe.yaml --no-interactive --request-timeout 240s --message "请只回复 OK。"
```
ExitCode: 0
```text
OK
```

结论: provider smoke test 通过，可以继续人工终端验证。

## 3. 真实 Provider 非交互 spawn_agent 验证

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli-probe.exe chat --provider opencode.ai --model deepseek-v4.1-flash --reasoning-effort max --config E:\projects\ai\ai-agent-runtime\output\real-test\config.team-probe.yaml --no-interactive --request-timeout 600s --message "请使用 spawn_agent 并行启动 2 个子 agent，不要读取文件。agent A 只基于内联短句 '验证目标是确认多 agent 输出隔离、reasoning 隔离、等待语义和中断语义' 总结一句话；agent B 只基于内联短句 '剩余门禁是真实 Windows Terminal Ctrl+C 与 TUI 面板人工验证' 总结一句话。两个子任务都标记为 normal 难度，请在 spawn_agent 调用里把 difficulty 参数显式设为 normal。spawn_agent 后请调用 wait_agent 等待，并调用 read_agent_events 读取两个子 agent 事件，最后 parent 用不超过 120 字中文汇总。不要向用户提问或请求确认，也不需要其它收尾动作，汇总后直接结束。"
```
ExitCode: 0
```text
汇总（≤120字）：

已并行启动 2 个 normal 难度子 agent（agent-inline-a/b，原 id agent-a/b 已存在故改名）。A 输出：验证目标是确认多 agent 输出隔离、reasoning 隔离与等待/中断语义是否正确；B 输出：剩余门禁为真实 Windows Terminal Ctrl+C 行为与 TUI 面板人工验证。二者均无工具调用、单步成功 idle。
```

最新 artifact:
- Session File: <not found: 会话正文现由 session_history.sqlite 承载>
- Session History DB: C:\Users\vince\.aicli\sessions\session_history.sqlite (updated 2026-09-14 11:00:31)
- Chat Log Dir: C:\Users\vince\.aicli\chat-logs\2026

Pattern check:
- Required found: spawn_agent, wait_agent, read_agent_events
- Forbidden scan source: command output
- Required evidence source: command output + session_history.sqlite（父会话 tool_calls）+ agent_control.sqlite（子 agent 行）
- Parent session: session_20260914110000_mU3yIdST, session_20260914110004_hauNtw0P, context-budget
- Parent tool calls: spawn_agent, list_agents, wait_agent, read_agent_events
- Forbidden found: <none>
- Child agent count: 2
- Child agent routes: opencode.ai/deepseek-v4.1-flash
- Child agent row: /root/agent-inline-a | opencode.ai/deepseek-v4.1-flash | difficulty=normal | route=parent_inherit | fallback=0 | warnings=["reasoning_effort_capability_unknown"] | closed | session=agent-inline-a
- Child agent row: /root/agent-inline-b | opencode.ai/deepseek-v4.1-flash | difficulty=normal | route=parent_inherit | fallback=0 | warnings=["reasoning_effort_capability_unknown"] | closed | session=agent-inline-b
- Child agent output: 2/2 个子 agent session 的 message_count >= 2
- Child agent difficulty (requested): normal（若 levels.easy 指向不可达 provider，探针显式指定其它难度以走 inherit_parent_when_missing 继承父 provider）

结论: 真实 provider 非交互 spawn_agent 验证通过。

## 4. 真实 Provider 非交互 spawn_team + wait_team 验证

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli-probe.exe chat --provider opencode.ai --model deepseek-v4.1-flash --reasoning-effort max --config E:\projects\ai\ai-agent-runtime\output\real-test\config.team-probe.yaml --no-interactive --request-timeout 600s --message "请使用 spawn_team auto_start=true 创建 3 个 team 成员和 3 个 task，不要读取文件，也不要写文件。三个 task 分别基于这些内联短句各用一句中文总结：task-1 'AgentControl task graph 已成为 team task 的主写入路径'；task-2 'spawn_team 完成等待应使用 wait_team 而不是 wait_agent/read_agent_events'；task-3 '真实终端仍需验证 Ctrl+C 与 TUI 面板表现'。spawn_team 返回后，必须使用工具结果里的 team_id 调用 wait_team 等待团队完成和 team.summary，然后 parent 用不超过 120 字中文汇总。不要对 teammate id 调用 wait_agent 或 read_agent_events。不要向用户提问或请求确认，也不需要其它收尾动作，汇总后直接结束。"
```
ExitCode: 0
```text
team-summary-3_v2 已完成（3 成员 / 3 任务）：task-1 AgentControl task graph 主导 team task 写入；task-2 完成等待须用 wait_team，勿用 wait_agent/read_agent_events；task-3 真实终端 Ctrl+C 与 TUI 面板表现仍待验证。
```

最新 artifact:
- Session File: <not found: 会话正文现由 session_history.sqlite 承载>
- Session History DB: C:\Users\vince\.aicli\sessions\session_history.sqlite (updated 2026-09-14 11:02:45)
- Chat Log Dir: C:\Users\vince\.aicli\chat-logs\2026

Pattern check:
- Required found: wait_team
- Forbidden scan source: command output
- Forbidden found: <none>
- Route target blocked signals: <none>
- Route target blocked evidence: <none>

结论: 真实 provider 非交互 spawn_team + wait_team 验证未通过或证据不足。请检查 session/chat log。

## 5. 真实终端人工验证

在 Windows Terminal 中执行:
```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli-probe.exe chat
```

按以下清单记录结果:

- [ ] 验证 A: spawn_agent 并行输出隔离。
- [ ] 验证 B: spawn_team auto_start 并行与 busy 收敛。
- [ ] 验证 C: Ctrl+C 第一次取消 active child/team，第二次退出。
- [ ] 验证 D: provider stream error 收敛。
- [ ] /agents panel follow timeout=10s 20 可刷新且不污染 primary console。
- [ ] /collab all 50 和 /timeline active 50 可看到结构化 mailbox/timeline。

需要补充的证据:

- Session ID:
- Session File:
- Chat Log File:
- Debug Log File:
- HTTP Artifact Dir:
- Shell Artifact Dir:
- 通过/失败结论:
- 失败细节:

---

## 追记（2026-09-14 11:07）：§4 结论修正为「判定口径假红」

- 本报告 §4「未通过或证据不足」**不是** team 逻辑回归，而是 §4 只扫描命令输出 + `session_*.json`（当前版本会话正文写入 `session_history.sqlite`，该 json 已不落盘）造成的假红：`team.summary` 是**持久事件名**，不会出现在 parent 最终正文里。
- 原始证据（只读取证，未改动任何 store）：
  - `session_history.sqlite`，父会话 `session_20260914110032_uaPNfwDE`：工具调用序列 `spawn_team@seq4 → wait_team@seq6`。
  - `session_history.sqlite` seq=7（`role=tool`，`wait_team` 结果）：`Team team-summary-3_v2 status=done. Team is terminal. Summary is ready. Summary: … Returned 21 lifecycle events.`
  - `team_store.sqlite`：`teams.status=done`（`lead_session_id=session_20260914110032_uaPNfwDE`）；`team_events` 含 `team.completed`×1、`team.summary`×1（`result_contract.status=succeeded`）、`task.completed`×3；`agent_control_task_records` 三条 task 均 `done`、路由 `opencode.ai/deepseek-v4.1-flash`。
- 处置：`scripts/validate-multi-agent-real-terminal.ps1` §4 已补齐取证（新增 `Get-TeamTerminalEvidence` 查 `teams`/`team_events`，并复用 `Get-RecentRootSessions`/`Get-SessionToolCallNames` 取父会话 tool_calls），报告新增 `Required evidence source` / `Parent session` / `Parent tool calls` / `Team row` 四行证据。
- 修正后重跑（仅 §4，`-SkipProviderSmoke -SkipSpawnAgentProbe`）：`Required found: wait_team, team.summary, spawn_team`、`Parent tool calls: spawn_team, wait_team`、`Team row: summary-team | status=done | team.summary=1 | team.completed=1`、forbidden 与环境阻断信号均 `<none>`，结论「真实 provider 非交互 spawn_team + wait_team 验证通过」——见 `docs/working/multi-agent-real-terminal-validation-20260914-110526.md`。
- 本报告 §5 的人工交互项（Ctrl+C 两次语义、TUI `/agents` 面板、`/collab`、`/timeline`）仍未验证，保持未勾选。
