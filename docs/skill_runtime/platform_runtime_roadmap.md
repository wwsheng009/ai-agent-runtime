# Skill / Agent Platform Roadmap

> 迁移说明（2026-03-30）：
> - 本文现由 `ai-agent-runtime/docs` 维护
> - 凡出现旧 `internal/runtime/*` 路径，请按 `backend/internal/*` 理解
> - 对外主交付面以 `backend/internal/api/skills` 和 `backend/cmd/runtime-server` 为准

## 定位

主目标不是做一个只服务于 coding 的 agent runtime。

主目标是建设一个通用 AI 能力平台，由以下运行时共同驱动：

- `skill`
- `agent`
- `tool`
- `workflow`
- `mcp`
- `llm`
- `session / memory / retrieval`

coding 只是平台上的一个垂直能力包。

## 当前结论

结合 `docs/skill_runtime/design` 下 16 份设计文档与当前代码实现，当前仓库已经更接近：

- 一个可运行的通用 `skill / agent` 平台内核

而不是：

- 一个完整的 Claude Code / Cursor 级 coding agent 产品

这意味着路线图需要围绕“平台治理、统一抽象、稳定交付面”展开，而不是继续把 coding-specific 能力当成当前阻塞项。

## 平台层次

### 1. Capability Runtime

- skill manifest / loader / registry
- lexical + semantic routing
- workflow execution
- MCP-backed tool runtime
- agent orchestration entry

### 2. Model Runtime

- unified LLM runtime
- provider alias / default model
- gateway provider integration
- streaming contract
- route / fallback summary

### 3. Context Runtime

- session lifecycle
- conversation history
- local embedding / semantic retrieval
- future shared memory abstraction

### 4. Governance Runtime

- auth
- tenant / project isolation
- policy / permission
- usage tracking / quota
- audit / observability

### 5. Delivery Surfaces

- REST API
- SSE API
- standalone `runtime-server`
- SDK / CLI integration
- `aicli chat` integration

## 当前阶段判断

### 已形成的平台骨架

- `runtime/bootstrap`
- `skills API`
- `LLM Runtime`
- `Session / Search / Monitoring`
- `MCP integration`
- `local embedding`
- `provider / MCP live validation`

### 当前主要缺口

- rollout 执行仍是基础版本（基于 scopeKey 的 canary/progressive），缺少指标驱动的自动推进/回滚
- retrieval / memory 仍以 context pack 为主，缺少可插拔的长期记忆与统一检索接口
- 垂直能力包尚未实现（coding pack / ABAP / enterprise workflow）

## 分阶段路线

### Phase 1：平台内核收口（已完成）

目标：固化当前已实现的可运行内核。

- 固化 `skill / tool / workflow / agent` 注册与调用协议
- 巩固 REST / SSE contract
- 完成 bootstrap / config / runtime 文档化
- 持续保持主链路测试可回归

### Phase 2：AI 可调用能力统一（已完成）

目标：让 skill 真正成为 AI 可直接调用的能力。

- 在 `aicli chat` 中注册 skills 为 function/tool
- 提供 SDK / client bindings
- 为 skill 增加稳定 schema、参数契约和错误契约
- 统一 tool call 与 skill call 的返回结构

### Phase 3：平台治理（已完成）

目标：从“可运行”走向“可管理”。

- auth / tenant / project / policy
- usage tracking / quota / rate limit
- admin ops / audit 扩展到全平台
- config validation / versioning / rollout

### Phase 4：平台增强（进行中）

目标：增强上下文、检索和编排能力。

- retrieval / memory abstraction
- planner-first optional orchestration
- provider / MCP lifecycle governance
- richer search / indexing / background rebuild

### Phase 5：垂直能力包（计划）

目标：把场景能力作为 pack 叠加在平台上。

- coding pack
- ABAP / ERP pack
- enterprise workflow pack
- support / knowledge pack

## 近期待办优先级

### P1

- rollout 自动推进/回滚（指标驱动）
- retrieval / memory 可插拔抽象
- provider / MCP lifecycle 深度治理

### P2

- planner-first 默认化策略与可观测优化
- search / indexing 背景重建与稳定性增强

### P3

- coding pack 规划与拆分
- ABAP / ERP pack 规划
- enterprise workflow pack 规划

## 未来计划开发（垂直能力包）

以下能力纳入 roadmap，但不作为平台主线阻塞项：

- repo map
- AST-only code intelligence
- git automation
- patch engine
- coding-specific sandbox
- reviewer / tester / coder multi-agent pipelines

## 执行原则

1. 优先强化 `backend/internal/*` 作为可复用平台内核
2. 保持 `backend/internal/api/skills` 作为稳定交付面
3. 优先补平台治理与统一抽象，而不是追加 coding-only 复杂度
4. 把垂直能力包建立在稳定平台内核之上
