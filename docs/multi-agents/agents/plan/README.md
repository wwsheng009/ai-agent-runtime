# Multi-Agents Plan

> 更新时间：2026-03-13
> 状态：基于当前代码实现重新对齐

## 目标

对齐多代理设计与当前实现，形成差异清单、架构图与阶段性路线图。

**设计意图汇总（来自 `docs/multi-agents/design`）**
- 以 runtime 为核心，不是聊天壳。
- 多代理架构在 skills 之上，每个 agent 是独立运行环境（model/prompt/session/context/skills）。
- 主 agent 动态生成子代理 prompt；子代理只回传结构化回执，不回传完整 transcript。
- Context OS：三层上下文、预算与 admission 规则、compaction 与 recall。
- Output Gateway + Reducers：把工具输出变成可控信号，而不是无限膨胀的上下文。
- Artifact Store + FTS5 Recall：完整数据放在窗口外，按需 page-in。
- Scheduler/Policy/EventBus：先主从协议，再并发；安全与治理不写在 prompt 里。
- 交付顺序：单代理可跑通 → context/输出治理 → subagents → 规模化治理。

**现有能力与设施（可复用基建）**
- Skills runtime 全链路（loader/registry/router/executor/workflow DAG）。
- Agent 编排（route-first / planner-preferred / LLM fallback）。
- ReActLoop：Think → Act（工具调用）→ Observe → 更新 history，最多 MaxSteps 轮。
- MCP Manager + 语义路由（embedding_router）+ skill search API。
- Session/History（文件存储）与 Context Pack 注入。
- Context pack 摘要截断（`buildContextSummary`，4096 bytes 上限，防止膨胀）。
- 进程内 Memory（`memory.Memory`，observation 列表，支持 search/compact/export）。
- 基础治理：auth/usage/mutation policy 与 SSE 事件流。

**主要缺口（2026-03-13）**
- Output Gateway / Reducers：主链路已接入 gateway + artifact refs，但 reducer 覆盖仍偏 coding/runtime，缺更多工具类型的专用压缩策略。
- Context OS：已具备 admission/compaction/recall/ledger/checkpoint 基础实现，但 Hot/Warm/Cold 分层策略与预算调优仍需继续收口。
- 子代理协同：runtime 已支持 `spawn_subagents` + scheduler + 结构化回执；API 主线已支持 planned subagents，streaming 通过静态 SSE 返回 `subagent` 事件（仍非逐步增量流式编排）。
- Writer 工作流：单写者策略已实现，`FilePatch` 抽取/verification 状态/patch decision 与审批事件已落地，但 patch 应用/落盘与 verifier 自动验证链路仍未闭环。
- Tool Catalog：已具备轻量 MCP catalog/search，但尚未升级到 BM25/FTS 级别目录化。
- 治理可视化：runtime event bus / traces / governance 已可用，但跨 agent 规模化观测和运维面还不完整。

**内容索引**
- `docs/multi-agents/plan/comparison.md` — 设计 vs 实现能力矩阵
- `docs/multi-agents/plan/architecture.md` — 目标架构 vs 当前实现对照图
- `docs/multi-agents/plan/roadmap.md` — P0-P3 阶段路线图

**基线文档**
- `docs/multi-agents/design/current.md`
- `docs/skill_runtime/current_architecture.md`
- `docs/skill_runtime/skill_invocation_mechanism.md`
- `docs/skill_runtime/skills_api_result_contract.md`
