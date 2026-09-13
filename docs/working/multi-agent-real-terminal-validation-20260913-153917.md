# Multi-Agent 真实终端验证记录

生成时间: 2026-09-13T15:39:17.7579018+08:00
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

跳过 spawn_agent probe: -SkipSpawnAgentProbe

## 4. 真实 Provider 非交互 spawn_team + wait_team 验证

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli.exe chat --provider opencode.ai --model deepseek-v4.1-flash --reasoning-effort max --no-interactive --request-timeout 420s --message "请使用 spawn_team auto_start=true 创建 3 个 team 成员和 3 个 task，不要读取文件，也不要写文件。三个 task 分别基于这些内联短句各用一句中文总结：task-1 'AgentControl task graph 已成为 team task 的主写入路径'；task-2 'spawn_team 完成等待应使用 wait_team 而不是 wait_agent/read_agent_events'；task-3 '真实终端仍需验证 Ctrl+C 与 TUI 面板表现'。spawn_team 返回后，必须使用工具结果里的 team_id 调用 wait_team 等待团队完成和 team.summary，然后 parent 用不超过 120 字中文汇总。不要对 teammate id 调用 wait_agent 或 read_agent_events。"
```
ExitCode: 0
```text
`spawn_team` 返回的 team_id 为 `inline-summary-team_v2`，我已据此调用 `wait_team`（require_summary=true, 300s）等待，未对任何 teammate id 调用 wait_agent/read_agent_events。

结果说明（如实反馈）：
- 团队状态：`failed`（终态），team.summary 已生成。
- 三个 task 均失败，原因一致：provider 传输层在重试后仍超时（fast-fail 重试耗尽），属基础设施/供应商侧故障，而非任务逻辑问题。因此 teammate 未产出各自的总结文本。

由于三句素材是内联给出的，parent 直接完成汇总（119 字）：

> 1) AgentControl task graph 成为 team task 主写入路径；2) 等待完成用 wait_team，非 wait_agent/read_agent_events；3) 真实终端待验证 Ctrl+C 与 TUI。

如需在 provider 恢复后重跑该团队以取得 teammate 产出的总结，请告知，我可以重试同一组 task。
```

最新 artifact:
- Session File: <not found: 会话正文现由 session_history.sqlite 承载>
- Session History DB: C:\Users\vince\.aicli\sessions\session_history.sqlite (updated 2026-09-13 15:43:58)
- Chat Log Dir: C:\Users\vince\.aicli\chat-logs\2026

Session pattern check:
- Required found: 
- Forbidden scan source: command output
- Forbidden found: <none>
- Route target blocked signals: response-header guard after 20s, timeout awaiting response headers, provider transport stream failed, provider call failed after repeated fast-fail retries
- Route target blocked evidence: team_store.sqlite -> task: task-2_v2 | inline-summary-team_v2 | failed | vsllm2_codex/gpt-5.4-mini | provider call failed after repeated fast-fail retries: provider transport stream failed after retries: failed to send request: timeout awaiting response headers (response-header guard after 20s)

结论: spawn_team + wait_team 未通过，且属于环境阻断: 子任务路由目标的 provider 在本机不可达（transport 快速失败 / 20s response-header guard）。
- 命中信号: response-header guard after 20s, timeout awaiting response headers, provider transport stream failed, provider call failed after repeated fast-fail retries
- 证据来源: team_store.sqlite -> task: task-2_v2 | inline-summary-team_v2 | failed | vsllm2_codex/gpt-5.4-mini | provider call failed after repeated fast-fail retries: provider transport stream failed after retries: failed to send request: timeout awaiting response headers (response-header guard after 20s)
- 先做可达性预检: 读取 $env:USERPROFILE\.aicli\config.yaml 中 aicli.teams.routing.levels.* / aicli.subagents.routing.levels.* 的 provider，再探测其 base_url 是否可达。
- 处置: 把 levels.* 指向可达 provider，或把 routing.enabled 置为 false 让子任务继承父会话 provider，然后重跑本脚本。
- 该结论不代表 spawn_team/wait_team 语义回归；需要与真实实现缺陷区分记录。

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
