# Multi-Agent 真实终端验证记录

生成时间: 2026-09-13T16:26:51.4581447+08:00
Repo: E:\projects\ai\ai-agent-runtime
Provider: opencode.ai
Model: deepseek-v4.1-flash
ReasoningEffort: max
AICLI: E:\projects\ai\ai-agent-runtime\backend\aicli.exe
WorkDir: E:\projects\ai\ai-agent-runtime\backend
Config: E:\projects\ai\ai-agent-runtime\output\real-test\config.team-probe.yaml（局部配置副本；用户全局配置未改动）

## 1. 构建

跳过构建: -SkipBuild

## 2. Provider Smoke Test

跳过 provider smoke test: -SkipProviderSmoke

## 3. 真实 Provider 非交互 spawn_agent 验证

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli.exe chat --provider opencode.ai --model deepseek-v4.1-flash --reasoning-effort max --config E:\projects\ai\ai-agent-runtime\output\real-test\config.team-probe.yaml --no-interactive --request-timeout 420s --message "请使用 spawn_agent 并行启动 2 个子 agent，不要读取文件。agent A 只基于内联短句 '验证目标是确认多 agent 输出隔离、reasoning 隔离、等待语义和中断语义' 总结一句话；agent B 只基于内联短句 '剩余门禁是真实 Windows Terminal Ctrl+C 与 TUI 面板人工验证' 总结一句话。两个子任务都标记为 normal 难度，请在 spawn_agent 调用里把 difficulty 参数显式设为 normal。spawn_agent 后请调用 wait_agent 等待，并调用 read_agent_events 读取两个子 agent 事件，最后 parent 用不超过 120 字中文汇总。不要向用户提问或请求确认，也不需要其它收尾动作，汇总后直接结束。"
```
ExitCode: 0
```text
已按要求并行启动 2 个 normal 难度子 agent（原定 id `agent-a`/`agent-b` 已被占用，改用 `iso-a1`/`gate-b1`），二者均未读文件、仅基于内联短句输出，事件中 reasoning 各自隔离且均以 session_end/success 正常结束。

汇总（108 字）：A 认为验证目标是确认多 agent 输出隔离、reasoning 隔离、等待与中断语义是否符合预期；B 认为剩余门禁是在真实 Windows Terminal 完成 Ctrl+C 与 TUI 面板人工验证。两子任务均成功、无报错，隔离与等待语义表现正常。
```

最新 artifact:
- Session File: <not found: 会话正文现由 session_history.sqlite 承载>
- Session History DB: C:\Users\vince\.aicli\sessions\session_history.sqlite (updated 2026-09-13 16:27:25)
- Chat Log Dir: C:\Users\vince\.aicli\chat-logs\2026

Pattern check:
- Required found: spawn_agent, wait_agent, read_agent_events
- Forbidden scan source: command output
- Required evidence source: command output + session_history.sqlite（父会话 tool_calls）+ agent_control.sqlite（子 agent 行）
- Parent session: session_20260913162651_h2TxUKWH
- Parent tool calls: list_agents, spawn_agent, wait_agent, read_agent_events
- Forbidden found: <none>
- Child agent count: 2
- Child agent routes: opencode.ai/deepseek-v4.1-flash
- Child agent row: /root/iso-a1 | opencode.ai/deepseek-v4.1-flash | difficulty=normal | route=parent_inherit | fallback=0 | warnings=["reasoning_effort_capability_unknown"] | closed | session=iso-a1
- Child agent row: /root/gate-b1 | opencode.ai/deepseek-v4.1-flash | difficulty=normal | route=parent_inherit | fallback=0 | warnings=["reasoning_effort_capability_unknown"] | closed | session=gate-b1
- Child agent output: 2/2 个子 agent session 的 message_count >= 2
- Child agent difficulty (requested): normal（若 levels.easy 指向不可达 provider，探针显式指定其它难度以走 inherit_parent_when_missing 继承父 provider）

结论: 真实 provider 非交互 spawn_agent 验证通过。

## 4. 真实 Provider 非交互 spawn_team + wait_team 验证

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli.exe chat --provider opencode.ai --model deepseek-v4.1-flash --reasoning-effort max --config E:\projects\ai\ai-agent-runtime\output\real-test\config.team-probe.yaml --no-interactive --request-timeout 600s --message "请使用 spawn_team auto_start=true 创建 3 个 team 成员和 3 个 task，不要读取文件，也不要写文件。三个 task 分别基于这些内联短句各用一句中文总结：task-1 'AgentControl task graph 已成为 team task 的主写入路径'；task-2 'spawn_team 完成等待应使用 wait_team 而不是 wait_agent/read_agent_events'；task-3 '真实终端仍需验证 Ctrl+C 与 TUI 面板表现'。spawn_team 返回后，必须使用工具结果里的 team_id 调用 wait_team 等待团队完成和 team.summary，然后 parent 用不超过 120 字中文汇总。不要对 teammate id 调用 wait_agent 或 read_agent_events。不要向用户提问或请求确认，也不需要其它收尾动作，汇总后直接结束。"
```
ExitCode: 0
```text
团队 team-summary-3t 已完成（3 成员 / 3 任务全部 done，team.summary 就绪）：

1. **task-1**：AgentControl task graph 已成为 team task 的主写入路径。
2. **task-2**：spawn_team 的完成等待应使用 wait_team，而非 wait_agent/read_agent_events。
3. **task-3**：真实终端仍需验证 Ctrl+C 与 TUI 面板表现。

（本次未读取或写入任何文件，全程仅基于内联短句。）
```

最新 artifact:
- Session File: <not found: 会话正文现由 session_history.sqlite 承载>
- Session History DB: C:\Users\vince\.aicli\sessions\session_history.sqlite (updated 2026-09-13 16:32:29)
- Chat Log Dir: C:\Users\vince\.aicli\chat-logs\2026

Pattern check:
- Required found: spawn_team, wait_team, team.summary
- Forbidden scan source: command output
- Forbidden found: <none>
- Route target blocked signals: <none>
- Route target blocked evidence: <none>

结论: 真实 provider 非交互 spawn_team + wait_team 验证通过。

## 5. 真实终端人工验证

在 Windows Terminal 中执行:
```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli.exe chat
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
