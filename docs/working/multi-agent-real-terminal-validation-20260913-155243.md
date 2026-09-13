# Multi-Agent 真实终端验证记录

> **撤回说明（2026-09-13 稍晚补记）**：本报告 §3 的「验证通过」结论已撤回。该判定只扫父进程输出（`spawn_agent` / `wait_agent` / `read_agent_events` 三个工具名都在），但两个子 agent 实际零产出：`agent_control_agents` 记录它们被 difficulty 路由到 `vsllm2_codex/gpt-5.4-mini`（本机不可达），`session_history.sqlite` 里两个子 agent session 的 `message_count` 均为 1。脚本已改为「子 agent 必须有产出才判通过」，根因与证据见 `docs/working/multi-agent-real-provider-probe-20260913.md` §6.1。

生成时间: 2026-09-13T15:52:43.1687435+08:00
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
.\aicli.exe chat --provider opencode.ai --model deepseek-v4.1-flash --reasoning-effort max --no-interactive --request-timeout 600s --message "请使用 spawn_agent 并行启动 2 个子 agent，不要读取文件。agent A 只基于内联短句 '验证目标是确认多 agent 输出隔离、reasoning 隔离、等待语义和中断语义' 总结一句话；agent B 只基于内联短句 '剩余门禁是真实 Windows Terminal Ctrl+C 与 TUI 面板人工验证' 总结一句话。spawn_agent 后请调用 wait_agent 等待，并调用 read_agent_events 读取两个子 agent 事件，最后 parent 用不超过 120 字中文汇总。"
```
ExitCode: 0
```text
已按序执行 spawn_agent→wait_agent→read_agent_events。汇总：并行启动两个子 agent 并完成等待与事件读取；两者会话/turn 完全独立，隔离语义成立。但均因 provider 传输超时（20s 响应头守卫）失败，无总结文本产出。
```

最新 artifact:
- Session File: <not found: 会话正文现由 session_history.sqlite 承载>
- Session History DB: C:\Users\vince\.aicli\sessions\session_history.sqlite (updated 2026-09-13 15:57:27)
- Chat Log Dir: C:\Users\vince\.aicli\chat-logs\2026

Pattern check:
- Required found: spawn_agent, wait_agent, read_agent_events
- Forbidden scan source: command output
- Forbidden found: <none>

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
