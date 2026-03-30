# Design vs Implementation

> 更新时间：2026-03-13
> 对比范围：`docs/multi-agents/design` 全部文档（idea1-idea13 + ide12 + current.md），代码事实来源以当前 `internal/` 实现为准。

## Design Intent Synthesis

- **Runtime-first**：核心是可控执行内核，而不是聊天壳。
- **Multi-agent above skills**：每个 agent 独立运行环境，主 agent 动态生成子代理 prompt。
- **子代理协议**：任务包 + 结构化回执，父代理不接收子代理 transcript。
- **Context OS**：三层上下文（Hot/Warm/Cold）、预算与 admission 规则、compaction 与 recall。
- **Output Gateway + Reducers**：工具输出要被归一化与压缩，避免上下文膨胀。
- **Artifact Store + Recall**：完整数据存储在窗口外，按需 page-in（FTS/BM25）。
- **安全与治理**：policy/hook/event bus，工具调用与写操作要隔离与审计。

## Status Legend

| Status | 含义 |
| --- | --- |
| Implemented | 已落地并可用 |
| Partial | 有实现但能力不完整或未与主链路打通 |
| Not Implemented | 尚未落地 |

## Capability Matrix

| 能力 | 设计目标 | 当前实现 | Status | 主要位置 |
| --- | --- | --- | --- | --- |
| Skills Runtime 主链路 | loader/registry/router/executor 完整链路 | 已实现并在 API 中使用 | Implemented | `internal/runtime/skill/*` |
| Skill 结构化描述 | `skill.yaml` + `prompt.md` | loader/manifest 支持 | Implemented | `internal/runtime/skill/manifest.go`, `internal/runtime/skill/loader.go` |
| Workflow + DAG 并行 | 并发工具执行 | workflow DAG 并行执行，concurrency 自适应 | Implemented | `internal/runtime/skill/executor.go`, `internal/runtime/skill/dag.go` |
| ReActLoop | Think→Act→Observe 循环，MaxSteps 控制 | 已实现，支持 tool_use 多轮 | Implemented | `internal/runtime/agent/loop.go` |
| Agent 编排 | route-first / planner-preferred / LLM fallback | 已实现 | Implemented | `internal/runtime/agent/orchestrator.go` |
| Planner | 计划生成与 workflow 映射 | 计划器 + workflow 转换 | Implemented | `internal/runtime/agent/planner.go` |
| LLM Model Adapter | 统一模型调用与适配 | LLM runtime + adapters | Implemented | `internal/runtime/llm/*` |
| MCP 管理与注册 | Tool Broker 能力 | MCP Manager + registry + health check | Implemented | `internal/mcp/manager/*` |
| Tool Search（skills） | 语义路由与检索 | embedding router + search API | Implemented | `internal/runtime/skill/embedding_router.go`, `internal/api/skills/handler.go` |
| Session / History | 对话状态可持久化 | 文件存储 + session manager | Implemented | `internal/runtime/chat/*` |
| Context Pack 注入 | 统一上下文注入 | 已接入主链路 | Implemented | `internal/runtime/contextpack/*` |
| Context Pack 摘要截断 | 防止 context 膨胀 | `buildContextSummary`，4096B 上限，只注入标量和摘要字段 | Implemented | `internal/runtime/skill/executor.go` |
| 进程内 Memory | 任务记忆与检索 | observation list，支持 search/compact/export | Partial | `internal/runtime/memory/memory.go` |
| System role fallback | 模型不支持 system role 时降级 | 已实现失败检测 + system merge 回退（含测试） | Partial | `internal/runtime/skill/executor.go` |
| MCP Gateway/Host 化 | 在模型前做治理与隔离 | 仅具备管理器与信任级别配置 | Partial | `internal/mcp/manager/*`, `internal/mcp/config/*` |
| 每 agent 可指定模型 | per-agent model | 父/子 agent 均支持 model 覆盖，API 主线接线仍有限 | Partial | `internal/runtime/agent/agent.go`, `internal/runtime/agent/scheduler.go` |
| Tool Sandbox / Read-only 执行 | 只读与写操作隔离 | tool policy + sandbox + read-only 已接入 API/agent | Implemented | `internal/runtime/agent/tool_policy.go`, `internal/api/skills/handler.go` |
| 安全与治理（policy/hook/event bus） | 不靠 prompt 做安全 | policy/hook/event bus 已落地，治理视图已接入 API | Implemented | `internal/runtime/agent/hooks.go`, `internal/runtime/events/bus.go`, `internal/api/skills/handler.go` |
| Observability / Telemetry | 事件流与指标 | runtime traces/governance + SSE/usage 观测均可用 | Implemented | `internal/runtime/events/bus.go`, `internal/api/skills/handler.go` |
| **Message Builder** | tool_use/tool_result 顺序一致，自动修复 | 已落地并接入 ReActLoop | Implemented | `internal/runtime/agent/message_builder.go` |
| **Output Gateway + Reducers** | 工具输出归一化与压缩成 Envelope | 已落地并接入 ReAct/tool_result | Implemented | `internal/runtime/output/*` |
| **Artifact Store + Recall (FTS/BM25)** | 结构化存储与按需检索 | SQLite store + artifact search 已落地，FTS5 可用时自动启用 | Implemented | `internal/runtime/artifact/store.go` |
| **Context OS（预算/admission/recall/compaction）** | 三层上下文，稳定长任务 | admission/recall/ledger/checkpoint 已实现 | Partial | `internal/runtime/contextmgr/*` |
| **Tool Catalog（MCP tools）** | 目录化 + BM25/FTS 搜索 | 轻量 catalog + ranked search（IDF/短语命中）已落地 | Partial | `internal/mcp/catalog/*` |
| **子代理独立运行环境** | 每 agent 独立 session/context/skills runtime | fresh child env + 独立 session/config/prompt/policy/skills runtime（共享 MCP/LLM runtime） | Implemented | `internal/runtime/agent/agent.go`, `internal/runtime/agent/scheduler.go` |
| **子代理调度器** | 并行执行与限流，read-only + single writer | 已落地 | Implemented | `internal/runtime/agent/scheduler.go` |
| **子代理任务包/回执协议** | SubagentTask + SubagentResult，父子协同 | 已落地，父代理仅接 structured report | Implemented | `internal/runtime/agent/scheduler.go`, `internal/runtime/agent/loop.go` |
| **主 agent 动态生成子代理 prompt** | Prompt Builder | 已落地 | Implemented | `internal/runtime/agent/prompt_builder.go` |
| **`spawn_subagents` 工具** | 模型自主调用，触发子代理调度 | 已落地 | Implemented | `internal/runtime/agent/loop.go` |
| **主线 planned subagents（含 streaming 静态 SSE）** | 默认入口可执行计划子代理并产出事件 | 已接入 API 主线，streaming 通过静态 SSE 返回 planning/subagent 事件 | Implemented | `internal/api/skills/handler.go`, `internal/runtime/agent/orchestrator.go` |
| **单写者/Verifier 策略** | 避免多 agent 并发写 | single-writer/read-only 已实现，patch 抽取/verification 状态/patch decision 已落地，apply 流程未闭环 | Partial | `internal/runtime/agent/scheduler.go`, `internal/runtime/agent/tool_policy.go`, `internal/runtime/agent/orchestrator.go` |
| **Hooks / EventBus** | PreToolUse/PostToolUse/SubagentStart 等钩子 | 已落地 | Implemented | `internal/runtime/agent/hooks.go`, `internal/runtime/events/bus.go` |

## 结论

**已覆盖**（P0 基本完成）：
- Skills Runtime + ReActLoop + Agent 编排 + Workflow DAG
- MCP Manager + Embedding Router + Session/History + Context Pack 摘要
- 基础治理（auth/usage/mutation policy）

**已覆盖的 P1/P2/P3 骨架**：
- Message Builder、Output Gateway、Artifact Store、Context Manager
- `spawn_subagents`、Subagent Scheduler、Prompt Builder、Task/Result 协议
- Hooks / EventBus、Sandbox、runtime traces/governance

**当前真正的核心缺口**：
- Context OS 策略继续完善：Hot/Warm/Cold 分层与预算调优
- Tool Catalog 升级到 BM25/FTS 级目录化
- writer patch / verifier 工作流闭环
- subagent streaming 仍为静态 SSE，未形成增量编排流
