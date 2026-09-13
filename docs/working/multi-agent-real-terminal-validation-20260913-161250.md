# Multi-Agent 真实终端验证记录

生成时间: 2026-09-13T16:12:50.7836036+08:00
Repo: E:\projects\ai\ai-agent-runtime
Provider: opencode.ai
Model: deepseek-v4.1-flash
ReasoningEffort: max
AICLI: E:\projects\ai\ai-agent-runtime\backend\aicli.exe
WorkDir: E:\projects\ai\ai-agent-runtime\backend

## 1. 构建

跳过构建: -SkipBuild

## 2. Provider Smoke Test

跳过 provider smoke test: -SkipProviderSmoke

## 3. 真实 Provider 非交互 spawn_agent 验证

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli.exe chat --provider opencode.ai --model deepseek-v4.1-flash --reasoning-effort max --no-interactive --request-timeout 420s --message "请使用 spawn_agent 并行启动 2 个子 agent，不要读取文件。agent A 只基于内联短句 '验证目标是确认多 agent 输出隔离、reasoning 隔离、等待语义和中断语义' 总结一句话；agent B 只基于内联短句 '剩余门禁是真实 Windows Terminal Ctrl+C 与 TUI 面板人工验证' 总结一句话。两个子任务都标记为 normal 难度，请在 spawn_agent 调用里把 difficulty 参数显式设为 normal。spawn_agent 后请调用 wait_agent 等待，并调用 read_agent_events 读取两个子 agent 事件，最后 parent 用不超过 120 字中文汇总。"
```
ExitCode: 0
```text
已完成（注：`agent-a`/`agent-b` 两个 id 已被占用，改为运行时自动分配 id 重新 spawn，未重复盲试）。

**最终汇总（≤120 字）**

两子 agent（difficulty=normal）并行完成，输出与事件各自隔离。A：验证目标是多 agent 输出隔离、reasoning 隔离、等待与中断语义。B：剩余门禁为真实 Windows Terminal Ctrl+C 与 TUI 面板人工验证。

**关键证据**
- spawn：两个子会话均 `difficulty=normal`（`difficulty_source=explicit`），`isolation=none`，模型 `deepseek-v4.1-flash`。
- wait_agent：一次等待即 `ready_count=2`，两者 `status=idle`、`session_state=active`。
- 事件：各自 4 条（session_start → compact_skipped → assistant_message → session_end），`turn success=true`、`steps=1`，输出分别为 A/B 的单句总结，互不串扰。
- 全程未读取任何文件。
```

最新 artifact:
- Session File: <not found: 会话正文现由 session_history.sqlite 承载>
- Session History DB: C:\Users\vince\.aicli\sessions\session_history.sqlite (updated 2026-09-13 16:13:22)
- Chat Log Dir: C:\Users\vince\.aicli\chat-logs\2026

Pattern check:
- Required found: wait_agent, spawn_agent, read_agent_events
- Forbidden scan source: command output
- Required evidence source: command output + session_history.sqlite（父会话 tool_calls）+ agent_control.sqlite（子 agent 行）
- Parent session: session_20260913161251_6MJ5uzxa
- Parent tool calls: list_agents, spawn_agent, wait_agent, read_agent_events
- Forbidden found: <none>
- Child agent count: 2
- Child agent routes: opencode.ai/deepseek-v4.1-flash
- Child agent row: /root/session_20260913161306_QDvBzH3o | opencode.ai/deepseek-v4.1-flash | difficulty=normal | route=parent_inherit | fallback=0 | warnings=["reasoning_effort_capability_unknown"] | closed | session=session_20260913161306_QDvBzH3o
- Child agent row: /root/session_20260913161306_X4QZhXak | opencode.ai/deepseek-v4.1-flash | difficulty=normal | route=parent_inherit | fallback=0 | warnings=["reasoning_effort_capability_unknown"] | closed | session=session_20260913161306_X4QZhXak
- Child agent output: 2/2 个子 agent session 的 message_count >= 2
- Child agent difficulty (requested): normal（若 levels.easy 指向不可达 provider，探针显式指定其它难度以走 inherit_parent_when_missing 继承父 provider）

结论: 真实 provider 非交互 spawn_agent 验证通过。

## 4. 真实 Provider 非交互 spawn_team + wait_team 验证

跳过 spawn_team probe: -SkipSpawnTeamProbe

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
