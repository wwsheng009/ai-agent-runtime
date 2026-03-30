# 多代理设计对齐（基于 Skills Runtime）

> 状态：2026-03-12  
> 目标架构：Multi-agent 位于 Skills Runtime 之上；当前实现仍以单 agent runtime 为主。

## 结论先行

多代理不是与 Skills Runtime 并行的新内核，而是 **位于 Skills Runtime 之上的编排层**。  
每个 agent 都应拥有独立运行环境（model/prompt/session/context/skills runtime），主 agent 负责动态生成子代理 prompt 并汇总结构化回执。  
当前系统已完成 skills runtime/agent 编排基座，但尚未形成多 agent 独立环境，因此需要在现有基座上扩展多代理层。

基于当前实现，以下能力已具备：

- `skills runtime` 主链路已稳定可用
- `agent` 已支持 route-first / planner-preferred / llm-fallback
- `workflow` + `DAG` 已能并行执行工具步骤
- `embedding router` 已提供语义路由与搜索
- `session / history / context pack` 已在 API 层形成统一入口

## 目标架构要点

- 多代理层在 skills 之上，每个 agent 具备独立运行环境
- 每个 agent 可指定不同模型与系统提示
- 主 agent 动态生成子代理 prompt，子代理只回传结构化回执
- Context OS + Output Gateway + Artifact Store 是多代理稳定运行的前置条件

## 架构映射（旧概念 -> 现实现）

| 旧设计概念 | 当前实现 | 主要文件 |
| --- | --- | --- |
| Supervisor / Scheduler | `agent.Orchestrate` + `planner` + `workflow DAG` | `internal/runtime/agent/*` |
| Tool Broker | MCP Manager + Skill Executor | `internal/mcp/manager/*`, `internal/runtime/skill/executor.go` |
| Tool Search | `searchMode` + `embedding router` + registry | `internal/api/skills/handler.go`, `internal/runtime/skill/embedding_router.go` |
| Context Manager | `contextpack` + `workspace context` + session | `internal/runtime/contextpack/*`, `internal/runtime/workspace/*`, `internal/runtime/chat/*` |
| Artifact Store / Output Gateway | **未落地（规划项）** | 见“待补能力” |
| Subagents | **未落地（规划项）**，可先用 workflow 并行替代 | 见“下一步” |

## 当前数据流（基于现实现）

1. **Agent Chat**：`/api/agent/chat`（compat: `/api/skills/agent/chat`）；route-first -> skill executor；无匹配则 LLM fallback；可选 planner 预览。
2. **Skill Execute**：`/api/skills/{name}/execute`（admin/debug）；`skill.Executor` 选择 handler / workflow / llm；workflow 走 DAG 并行执行 MCP tools。
3. **Skill Search**：`/api/skills/search`；lexical / semantic / hybrid；embedding index 与热加载同步。
4. **Session / Context Pack**：session history 存在 `chat.SessionManager`；context pack 统一注入 workspace + session 摘要。

## 多代理落点（当前实现限制）

当前已具备的“并行”能力其实在 `workflow` + `DAG`：

- 适合并发工具调用
- 适合对同一任务做多路信息获取
- 适合替代“子代理并行搜集”的早期需求

真正的“多代理（独立 session）”应当作为下一阶段能力扩展：

- **子代理** = 新 session + 独立 history + 可控工具白名单  
- **父代理** = 仅接收结构化回执，不接收子代理全量 transcript  
- **并行度** = 由 runtime config 控制

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

- 多 agent 独立运行环境（per-agent model / prompt / session / context / skills）
- 主 agent 动态生成子代理 prompt
- 子代理调度器（独立 session 并行执行）
- Output Gateway / Artifact Store（工具输出压缩与检索）
- Context OS（预算/admission/recall/compaction）
- 单写者/多读者并发策略

## 设计调整建议

1. **不要复制 Skills Runtime**：多代理层应构建在现有 skills runtime 之上。
2. **先补 Context OS 与 Output Gateway**：否则多代理会放大上下文膨胀与输出失控问题。
3. **再引入子代理调度与独立环境**：确保回执协议与单写者策略先定好。

## 参考文档

- `docs/skill_runtime/current_architecture.md`
- `docs/skill_runtime/skill_invocation_mechanism.md`
- `docs/skill_runtime/skills_api_result_contract.md`
