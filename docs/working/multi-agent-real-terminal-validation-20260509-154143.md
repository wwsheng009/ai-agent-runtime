# Multi-Agent 真实终端验证记录

生成时间: 2026-05-09T15:41:43.7812462+08:00
Repo: E:\projects\ai\ai-agent-runtime
Provider: mimo_anthropic
AICLI: E:\projects\ai\ai-agent-runtime\backend\aicli.exe
WorkDir: E:\projects\ai\ai-agent-runtime\backend

## 1. 构建

跳过构建: -SkipBuild

## 2. Provider Smoke Test

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli.exe chat --provider mimo_anthropic --no-interactive --request-timeout 60s --message "请只回复 OK。"
```
ExitCode: 0
```text
OK
```

结论: provider smoke test 通过，可以继续人工终端验证。

## 3. 真实 Provider 非交互 spawn_agent 验证

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli.exe chat --provider mimo_anthropic --no-interactive --request-timeout 240s --message "请使用 spawn_agent 并行启动 2 个子 agent，不要读取文件。agent A 只基于内联短句 '验证目标是确认多 agent 输出隔离、reasoning 隔离、等待语义和中断语义' 总结一句话；agent B 只基于内联短句 '剩余门禁是真实 Windows Terminal Ctrl+C 与 TUI 面板人工验证' 总结一句话。spawn_agent 后请调用 wait_agent 等待，并调用 read_agent_events 读取两个子 agent 事件，最后 parent 用不超过 120 字中文汇总。"
```
ExitCode: 0
```text
两个子 agent 均已完成并返回结果：

- **Agent A** 输出：*"验证的核心目标是确保多 agent 在输出、推理、等待与中断四个维度上实现正确的隔离与语义保障。"*
- **Agent B** 输出：*"剩余门禁为真实 Windows Terminal 环境下的 Ctrl+C 中断操作与 TUI 面板的人工验证。"*

---

**Parent 汇总（≤120 字）：**

> 两子 agent 并行启动、独立运行、各自返回一句话摘要，验证了输出隔离与 spawn/wait 语义正确。当前自动化验证已基本完成，仅余真实 Windows Terminal Ctrl+C 中断及 TUI 面板两项需人工确认的门禁。
```

最新 artifact:
- Session File: C:\Users\vince\.aicli\sessions\session_20260509154157_zD7gJ6H2.json
- Chat Log Dir: C:\Users\vince\.aicli\chat-logs\20260509_154157
- Chat Log File: C:\Users\vince\.aicli\chat-logs\20260509_154157\chat_mimo_anthropic_anthropic_mimo-v2.5-pro_20260509_154157.json
- Debug Log File: C:\Users\vince\.aicli\chat-logs\20260509_154157\debug.log
- HTTP Artifact Dir: C:\Users\vince\.aicli\chat-logs\20260509_154157\runtime-http
- Shell Artifact Dir: C:\Users\vince\.aicli\chat-logs\20260509_154157\local-shell

Session pattern check:
- Required found: spawn_agent, wait_agent, read_agent_events
- Forbidden scan source: command output
- Forbidden found: <none>

结论: 真实 provider 非交互 spawn_agent 验证通过。

## 4. 真实 Provider 非交互 spawn_team + wait_team 验证

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli.exe chat --provider mimo_anthropic --no-interactive --request-timeout 300s --message "请使用 spawn_team auto_start=true 创建 3 个 team 成员和 3 个 task，不要读取文件，也不要写文件。三个 task 分别基于这些内联短句各用一句中文总结：task-1 'AgentControl task graph 已成为 team task 的主写入路径'；task-2 'spawn_team 完成等待应使用 wait_team 而不是 wait_agent/read_agent_events'；task-3 '真实终端仍需验证 Ctrl+C 与 TUI 面板表现'。spawn_team 返回后，必须使用工具结果里的 team_id 调用 wait_team 等待团队完成和 team.summary，然后 parent 用不超过 120 字中文汇总。不要对 teammate id 调用 wait_agent 或 read_agent_events。"
```
ExitCode: 0
```text
三个任务全部完成，团队汇总如下（≤120字）：

> **团队总结：** AgentControl task graph 已成为 team task 的主写入路径；spawn_team 任务完成后应使用 wait_team 等待汇总，而非 wait_agent 或 read_agent_events；此外，真实终端中 Ctrl+C 中断与 TUI 面板的表现仍需进一步验证。
```

最新 artifact:
- Session File: C:\Users\vince\.aicli\sessions\session_20260509154321_UVtIPaEZ.json
- Chat Log Dir: C:\Users\vince\.aicli\chat-logs\20260509_154321
- Chat Log File: C:\Users\vince\.aicli\chat-logs\20260509_154321\chat_mimo_anthropic_anthropic_mimo-v2.5-pro_20260509_154321.json
- Debug Log File: C:\Users\vince\.aicli\chat-logs\20260509_154321\debug.log
- HTTP Artifact Dir: C:\Users\vince\.aicli\chat-logs\20260509_154321\runtime-http
- Shell Artifact Dir: C:\Users\vince\.aicli\chat-logs\20260509_154321\local-shell

Session pattern check:
- Required found: spawn_team, wait_team, team.summary
- Forbidden scan source: command output
- Forbidden found: <none>

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
