# Roadmap

> 更新时间：2026-03-13
> 基于当前实现重新标注状态。P0 已完成，P1-P3 已有骨架实现，当前重点是主线收口与能力补全。

本路线图以 `docs/multi-agents/design/README.md` 的 P0/P1/P2 为主线，结合当前实现做状态标注。

## 阶段总览

| Phase | 设计意图 | 当前状态 | 主要范围 | Exit Criteria |
| --- | --- | --- | --- | --- |
| P0 单代理 Runtime | 单代理可跑通 + 工具网关 + Context 摘要 | **Done** | Skills Runtime 全链路；ReActLoop；workflow DAG；context pack 摘要截断；进程内 memory | 单代理能稳定执行工具；上下文摘要不超限；history 可持久化 |
| P1 Output Gateway + Context OS | 工具输出压缩归档 + 三层上下文预算 | **Partial** | Message Builder；Output Gateway + Reducers；Artifact Store（SQLite/FTS5）；Context OS（预算/admission/compaction/recall） | 工具原始输出不直接进 context；长会话可持续；context pack 具备预算与裁剪 |
| P2 子代理与 MCP Catalog | 子代理协同 + MCP Gateway + Tool Catalog | **Partial** | 子代理调度器；子代理任务包/回执协议；MCP Gateway/Host 化；Tool Catalog + BM25/FTS 搜索；`spawn_subagents` 工具 | 子代理并行可用；父代理仅接收结构化回执；工具搜索避免全量 prompt |
| P3 规模化治理 | 安全边界与规模化运维 | **Partial** | 单写者策略；Sandbox/只读执行；Policy/Hooks/EventBus；跨 agent 观测 | 并发控制稳定；审计可追溯；权限与配额可控 |

---

## P0 单代理 Runtime — Done

### 已完成

| 能力 | 实现位置 |
| --- | --- |
| Skills loader/registry/router/executor | `internal/runtime/skill/*` |
| Workflow DAG 并行执行 | `internal/runtime/skill/executor.go`, `internal/runtime/skill/dag.go` |
| Agent 编排（route-first / planner / LLM fallback） | `internal/runtime/agent/orchestrator.go` |
| ReActLoop（Think→Act→Observe，MaxSteps 控制） | `internal/runtime/agent/loop.go` |
| Session / History 持久化 | `internal/runtime/chat/*` |
| Context Pack 注入 + 4096B 摘要截断 | `internal/runtime/contextpack/*`, `internal/runtime/skill/executor.go` |
| 进程内 Memory（observation list + search/compact/export） | `internal/runtime/memory/memory.go` |
| MCP Manager + trust level + health check | `internal/mcp/manager/*` |
| Embedding Router + Skill Search API | `internal/runtime/skill/embedding_router.go`, `internal/api/skills/handler.go` |
| 基础治理（auth/usage/mutation policy） | `internal/api/skills/handler.go` |

### P0 剩余缺口

- **System role fallback**：`buildSystemRoleFallbackMessages` 已有雏形，需覆盖更多边缘情况。

---

## P1 Output Gateway + Context OS — Partial

### 已落地

- **Message Builder**：`tool_use / tool_result` 顺序修复和自动补齐已实现。
- **Output Gateway + Reducers**：ReAct/tool_result 已通过 gateway 压缩输出并生成 artifact refs。
- **Artifact Store（SQLite + FTS5）**：artifact、ledger、checkpoint 基础表与检索已实现。
- **Context OS**：已具备 admission、compaction、ledger、recall、checkpoint 基础流程。

### 当前缺口

- reducer 类型覆盖仍需扩展到更多工具输出。
- Hot/Warm/Cold 策略和预算参数仍需按真实长任务继续调优。
- recall / compaction 仍偏保守，尚未形成完整的生产级策略集。

### P1 联调顺序

1. Message Builder → 保证消息合法性
2. Output Gateway + 最简 Reducer（text truncation）
3. Artifact Store 基础表（SQLite）
4. Context OS admission + token 预算
5. Compaction（确定性）
6. Recall / page-in

---

## P2 子代理与 MCP Catalog — Partial

### 已落地

- `spawn_subagents` 已注册为工具，模型可主动触发子代理批次。
- 子代理调度器已实现，支持独立 session、prompt、tool policy、budget、timeout。
- 父代理仅接收 `SubagentResult` 结构化回执，不接子代理 transcript。
- Tool Catalog 已具备轻量 ranked search，并在 ReAct 中按需注入工具。
- streaming 下 `execute_planned_subagents` / `enable_react` 已接入静态 SSE 返回，并输出 `subagent` 事件。

### 当前缺口

```go
type SubagentTask struct {
    ID            string
    Goal          string
    ToolsWhitelist []string
    Model         string
    BudgetTokens  int
    TimeoutSec    int
    ReadOnly      bool
}

type SubagentResult struct {
    ID       string
    Success  bool
    Summary  string         // 压缩摘要，不是完整 transcript
    Patches  []FilePatch    // 仅 writer agent 填充
    Findings []string       // 仅 read-only agent 填充
    Usage    *types.TokenUsage
    Error    string
}
```

- writer agent 的 `FilePatch` 流程尚未真正打通。
- streaming 仍为静态 SSE，未形成增量子代理/工具流式输出链路。
- Tool Catalog 仍是轻量实现，尚未升级到 BM25/FTS。

---

## P3 规模化治理 — Partial

### 已落地

- 单写者 / read-only 约束已在 subagent scheduler 中实现。
- Hooks / EventBus 已实现，并接入 runtime traces / governance 视图。
- Tool Sandbox 已升级为 agent 级 tool policy + sandbox 校验。
- 统一 trace ID 和 parent/child runtime events 已打通。

### 当前缺口

- writer patch / verifier 工作流仍未闭环。
- 跨 agent 观测虽然已可用，但 dashboard/运维面仍偏基础。
- 更细粒度的权限、配额和规模化治理策略仍需补全。

---

## 实现映射说明

- 当前已具备：Skills Runtime 全链路、ReActLoop、Agent 编排、Workflow DAG、Output Gateway、Artifact Store、Context Manager、Subagent Scheduler、Tool Catalog、Hooks/EventBus、Sandbox。
- 当前主要缺口：writer patch / verifier 工作流，Tool Catalog 深化，Context OS 策略完善，以及更完整的主线入口接入。
