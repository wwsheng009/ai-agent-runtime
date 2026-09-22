# Multi-Agent 真实终端验证记录

生成时间: 2026-09-14T11:05:26.7268619+08:00
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

跳过 provider smoke test: -SkipProviderSmoke

## 3. 真实 Provider 非交互 spawn_agent 验证

跳过 spawn_agent probe: -SkipSpawnAgentProbe

## 4. 真实 Provider 非交互 spawn_team + wait_team 验证

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli-probe.exe chat --provider opencode.ai --model deepseek-v4.1-flash --reasoning-effort max --config E:\projects\ai\ai-agent-runtime\output\real-test\config.team-probe.yaml --no-interactive --request-timeout 600s --message "请使用 spawn_team auto_start=true 创建 3 个 team 成员和 3 个 task，不要读取文件，也不要写文件。三个 task 分别基于这些内联短句各用一句中文总结：task-1 'AgentControl task graph 已成为 team task 的主写入路径'；task-2 'spawn_team 完成等待应使用 wait_team 而不是 wait_agent/read_agent_events'；task-3 '真实终端仍需验证 Ctrl+C 与 TUI 面板表现'。spawn_team 返回后，必须使用工具结果里的 team_id 调用 wait_team 等待团队完成和 team.summary，然后 parent 用不超过 120 字中文汇总。不要对 teammate id 调用 wait_agent 或 read_agent_events。不要向用户提问或请求确认，也不需要其它收尾动作，汇总后直接结束。"
```
ExitCode: 0
```text
团队已完成三个任务并经 team.summary 汇总：

**三任务完成**：AgentControl task graph 已是 team task 主写入路径；用 wait_team 等待摘要而非 wait_agent/read_agent_events；终端待验 Ctrl+C/TUI。
```

最新 artifact:
- Session File: <not found: 会话正文现由 session_history.sqlite 承载>
- Session History DB: C:\Users\vince\.aicli\sessions\session_history.sqlite (updated 2026-09-14 11:07:28)
- Chat Log Dir: C:\Users\vince\.aicli\chat-logs\2026

Pattern check:
- Required found: wait_team, team.summary, spawn_team
- Forbidden scan source: command output
- Required evidence source: command output + session_history.sqlite（父会话 tool_calls）+ team_store.sqlite（teams/team_events）
- Parent session: session_20260914110526_BK8XEXwQ
- Parent tool calls: spawn_team, wait_team
- Team row: summary-team | status=done | team.summary=1 | team.completed=1 | lead=session_20260914110526_BK8XEXwQ
- Forbidden found: <none>
- Route target blocked signals: <none>
- Route target blocked evidence: <none>

结论: 真实 provider 非交互 spawn_team + wait_team 验证通过。

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
