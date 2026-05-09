# 多代理设计对齐（基于 Skills Runtime）

> 状态：2026-05-09
> 目标架构：Multi-agent 位于 Skills Runtime 之上；当前实现已经具备 session-scoped child agent、Team orchestration、AgentControl task/mailbox/read-model 与 `aicli chat` 本地 actor-first orchestration。

## 结论先行

多代理不是与 Skills Runtime 并行的新内核，而是 **位于 Skills Runtime 之上的编排层**。  
每个 agent 都应拥有独立运行环境（model/prompt/session/context/skills runtime），主 agent 负责动态生成子代理 prompt 并汇总结构化回执。  
当前系统已完成 skills runtime/agent 编排基座，并已把多代理能力分成两条可运行路径：轻量 child session 控制面与更持久的 team/AgentControl 协作面。

基于当前实现，以下能力已具备：

- `skills runtime` 主链路已稳定可用
- `agent` 已支持 route-first / planner-preferred / llm-fallback
- `workflow` + `DAG` 已能并行执行工具步骤
- `embedding router` 已提供语义路由与搜索
- `session / history / context pack` 已在 API 层形成统一入口
- `spawn_agent` / `list_agents` / `send_input` / `send_message` / `followup_task` / `wait_agent` / `read_agent_events` / `close_agent` / `resume_agent` 已作为 session child-agent 控制面落地
- `spawn_team` / `wait_team` / `report_task_outcome` 等 team broker tools 已接入 `aicli chat` 本地 host
- `/api/runtime/teams/*` 与 `/api/runtime/agent-control/*` 已形成 team task graph、mailbox、dependency、path claim、task lifecycle 的 HTTP 控制面
- `fork_turns=none|all|N`、AgentControl identity graph、path、spawn reservation、subtree close、parent mailbox wait/read 已落地；team teammate id 与 child session id 仍需按工具边界区分

## 目标架构要点

- 多代理层在 skills 之上，每个 agent 具备独立运行环境
- 每个 agent 可指定不同模型与系统提示
- 主 agent 动态生成子代理 prompt，子代理只回传结构化回执
- Context OS + Output Gateway + Artifact Store 是多代理稳定运行的前置条件

## 架构映射（旧概念 -> 现实现）

| 旧设计概念 | 当前实现 | 主要文件 |
| --- | --- | --- |
| Supervisor / Scheduler | `agent.Orchestrate` + `planner` + `workflow DAG` + session child-agent controller | `backend/internal/agent/*`, `backend/internal/api/skills/session_runtime_support.go` |
| Tool Broker | MCP Manager + Skill Executor + broker tools | `backend/internal/mcp/manager/*`, `backend/internal/skill/executor.go`, `backend/internal/toolbroker/*` |
| Tool Search | `searchMode` + `embedding router` + registry | `backend/internal/api/skills/handler.go`, `backend/internal/skill/embedding_router.go` |
| Context Manager | `contextpack` + `workspace context` + session | `backend/internal/contextpack/*`, `backend/internal/workspace/*`, `backend/internal/chat/*` |
| Artifact Store / Output Gateway | checkpoint / generated image / background output 已部分落地，通用 artifact gateway 仍在演进 | `backend/internal/artifact/*`, `backend/internal/background/*`, `backend/internal/api/skills/handler.go` |
| Subagents | 轻量 child session 与 planned subagents 已落地；持久 team 使用 Team + AgentControl | `backend/internal/api/skills/session_runtime_handlers.go`, `backend/internal/team/*`, `backend/internal/agentcontrol/*` |

## 当前数据流（基于现实现）

1. **Agent Chat**：`POST /api/agent/chat`；route-first -> skill executor；无匹配则 LLM fallback；可选 `planner_preferred` 与 planned subagents。
2. **Skill Execute**：`POST /api/runtime/skills/{name}/execute`（admin/debug）；`skill.Executor` 选择 handler / workflow / llm；workflow 走 DAG 并行执行 MCP tools。
3. **Skill Search**：`GET /api/runtime/skills/search`；lexical / semantic / hybrid；embedding index 与热加载同步。
4. **Session / Context Pack**：session history 存在 `chat.SessionManager`；context pack 统一注入 workspace + session 摘要。
5. **Session Child Agents**：`/api/runtime/sessions/{id}/agents*` 暴露 spawn/list/input/message/followup/wait/events/close/resume；无 target 的 wait/read 进入 parent mailbox / collab event 模式。
6. **Teams / AgentControl**：`/api/runtime/teams/*` 与 `/api/runtime/agent-control/*` 暴露 team、task graph、mailbox、dependencies、leases、path claims 和 task lifecycle。

## 多代理落点（当前实现）

当前多代理能力有三层：

- `workflow` + `DAG`：适合同一 skill 内并发工具调用。
- `session child agent`：适合父 session 拆出轻量子会话并通过 wait/events 收集结果。
- `Team + AgentControl`：适合长任务、可恢复任务图、mailbox 协作、路径占用和 task lifecycle。

真正的“多代理（独立 session）”不再只是规划项；当前源码已经把它落到 child session 和 team teammate session 两种形态。后续重点是把 profile routing、artifact/output gateway、跨进程 registry lifecycle 和前端/TUI 长驻控制面继续收敛。

## 已实现能力清单（与设计文档对齐）

- skill.yaml / prompt.md 结构化能力描述
- loader / registry / router / executor 全链路
- workflow + DAG 并行执行
- MCP tool 接入与治理信息透传
- embedding 路由 + search telemetry
- agent route-first + planner-preferred + LLM fallback
- session/history + context pack
- governance: auth / quota / mutation policy
- LLM 执行时注入 context pack 精简摘要（避免原始上下文膨胀）

## 待补能力（属于多代理增强）

以下能力在旧设计中提过，但目前仍是增量能力：

- subagent profile 外置与自动 profile routing
- 更统一的 Output Gateway / Artifact Store（工具输出压缩、检索、预览）
- Context OS（预算/admission/recall/compaction）的跨 agent 一致策略
- 跨进程 registry service 生命周期与更多 store 后端投影兼容
- 前端/TUI 长驻多 agent 面板与键盘导航

## 设计调整建议

1. **不要复制 Skills Runtime**：多代理层应构建在现有 skills runtime 之上。
2. **先补 Context OS 与 Output Gateway**：否则多代理会放大上下文膨胀与输出失控问题。
3. **再引入子代理调度与独立环境**：确保回执协议与单写者策略先定好。

## 参考文档

- `docs/skill_runtime/current_architecture.md`
- `docs/skill_runtime/skill_invocation_mechanism.md`
- `docs/skill_runtime/skills_api_result_contract.md`
