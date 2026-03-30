# Multi-Agents TODO

> 更新时间：2026-03-14  
> 目标：把 `docs/multi-agents/plan` 中已识别缺口拆成可执行任务，并持续回写状态。

## 状态标记

- `[x]` 已完成（代码与测试已落地）
- `[-]` 进行中（已有实现但未完全达成验收）
- `[ ]` 未开始

## 优先级与任务清单

### P0 - 主线质量收口（高优先级）

- `[x]` 修复 planner 在 `tool_catalog` 路径退化为单步计划的问题
  - 现象：`CreatePlan()` 在多数场景仅输出一步，影响子代理协同质量。
  - 目标：优先尝试 LLM 规划；失败时回退到启发式多步计划。
  - 验收：
    - `CreatePlan()` 在有 LLM 时优先调用 `CreatePlanWithLLM()`。
    - LLM 返回非法计划时仍可生成有效 fallback plan。
    - 新增/更新测试覆盖上述分支。
  - 代码：`internal/runtime/agent/planner.go`，`internal/runtime/agent/planner_test.go`

- `[x]` 修复 memory 统计结构不完整问题
  - 现象：`GetStats()` 中 `ObservationsByTool` 未初始化，存在 panic 风险。
  - 目标：补齐 map 初始化与 usage 计算，保证统计稳定可用。
  - 验收：
    - `GetStats()` 返回结构字段完整，不会空 map 写入。
    - `Usage` 按 `TotalObservations / MaxSize` 计算并做边界保护。
    - 新增单测覆盖。
  - 代码：`internal/runtime/memory/memory.go`，`internal/runtime/memory/memory_test.go`

- `[x]` 完善 System role fallback 覆盖范围
  - 现象：`buildSystemRoleFallbackMessages` 仅覆盖部分模型分支与边缘情况。
  - 进展：已补齐常见 provider 报错模式匹配，并完善“历史以 assistant 开头”时的 fallback 消息重写。
  - 目标：完善 system role 不支持场景下的消息构建策略与回退逻辑。
  - 验收：
    - 覆盖常见模型/入口的 system role fallback 分支。
    - 新增测试覆盖边缘 case。
  - 代码：`internal/runtime/skill/executor.go`

### P1 - Output Gateway + Context OS / 检索与路由质量（高优先级）

- `[x]` 扩展 Output Gateway Reducer 覆盖
  - 当前：Reducer 主要覆盖 git log / go test / playwright 等少量类型，通用文本/JSON/表格类输出压缩策略不足。
  - 进展：已补齐 `JSONReducer` / `TableReducer` / `LogReducer`，默认 reducer 链现覆盖 `go test -json` / `git log` / `playwright` / `json` / `table` / `log` / `text`。
  - 目标：补齐通用 reducer + 常用工具专用 reducer；确保原始输出落 Artifact Store，仅注入 envelope。
  - 验收：
    - 常见工具输出（text/json/table/log）均走 reducer，并生成 artifact refs。
    - 上下文注入长度稳定可控，有回归测试。
  - 代码：`internal/runtime/output/*`

- `[x]` 完善 Context OS 分层策略（Hot/Warm/Cold）与预算 profile
  - 当前：admission/compaction/recall/ledger/checkpoint 已有，但缺少清晰分层策略与可配置 profile。
  - 目标：形成可配置分层策略（至少环境级 profile）。
  - 进展：已引入显式 `ContextConfig` 与 `compact/balanced/extended` profile；已接入 agent context manager 和 runtime `status/health` 可观测输出；`compact/extended` 已具备可验证的策略行为差异（summary vs ledger-preferred、disabled vs broad recall、failures-only vs all observations）。
  - 进展（补充）：`ContextConfig` 已支持行为策略与阈值 override（`compactionMode` / `recallMode` / `observationMode` / `minCompactionMessages` / `minRecallQueryLength` / `ledgerLoadLimit`）；现已补充 `hot/warm/cold` alias、显式 `context_layers` / `context_layer_metrics` 摘要，以及长会话 profile 对照测试。
  - 验收：
    - profile 可配置并接入主线。
    - 增加长会话回归测试与指标对照。
  - 代码：`internal/runtime/contextmgr/*`

- `[x]` 提升 MCP Tool Catalog 检索排序质量
  - 现象：当前为轻量关键词打分，语义覆盖与短语匹配能力弱。
  - 目标：增强词项区分度与短语匹配，减少同分下字典序误排。
  - 验收：
    - Search 引入区分度权重（IDF 类）与短语命中加分。
    - 排序稳定且更贴近 query 意图。
    - 新增回归测试（短语命中优先等）。
  - 代码：`internal/mcp/catalog/catalog.go`，`internal/mcp/catalog/catalog_test.go`

### P2 - 多代理工作流闭环（中优先级）

- `[ ]` Profile-based aicli runtime（默认目录约定，不再通过配置指定路径）
  - 当前：aicli 只读取全局 `configs/config.yaml`/`configs/runtime.yaml`，会话默认落在 `~/.aicli/sessions`。
  - 目标：
    - 支持 `aicli chat --profile <dir> --agent <id>`。
    - 按约定目录解析：
      - `profiles/<profile>/profile.yaml`
      - `profiles/<profile>/skills/`
      - `profiles/<profile>/agents/<agent>/workspace/`
      - `profiles/<profile>/agents/<agent>/skills/`
      - `profiles/<profile>/agents/<agent>/workspace/mcp.yaml`（可选）
      - `profiles/<profile>/mcp.yaml`（可选）
      - `profiles/<profile>/runtime.yaml`（可选）
    - 会话存储落到 `profiles/<profile>/agents/<agent>/sessions/`，未使用 profile 时保持 `~/.aicli/sessions`。
    - Prompt 与工具策略按约定目录加载（`agents/<agent>/prompts/*`, `agents/<agent>/workspace/workspace.yaml`, `agents/<agent>/tools/policy.yaml`）。
  - 验收：
    - profile 模式下可正常加载 skills/MCP/runtime 覆盖并执行；
    - 会话 JSON 文件落到 `agents/<agent>/sessions/`；
    - 提供明确的缺失目录/文件错误提示。
  - 代码：`cmd/aicli/commands/chat.go`, `cmd/aicli/commands/chat_options.go`, `cmd/aicli/commands/skills_integration.go`（新增 profile loader）

- `[x]` 打通 writer patch -> verifier -> 落盘确认闭环
  - 当前：`FilePatch` 主要从 observation 抽取并注入 prompt，上层未形成统一 apply/verify pipeline。
  - 目标：定义 patch 产物协议与应用阶段，纳入统一审计事件。
  - 进展：已增强 writer 回执抽取，支持从工具输出中的 unified diff 自动提取 `FilePatch`（metadata 缺失时可回退）；已增加 verifier 回写，writer patch 会携带 `verification_status` 与 `verified_by`；writer patch 现额外携带 `apply_status` / `applied_by` / `artifact_refs`，并在 parent 侧发出 `patch.applied` 审计事件；parent 已增加 patch 决策阶段（`patch_decision=approved/blocked`）并回写到 planning/orchestration/SSE。
  - 验收：
    - writer 产出可机器消费 patch（非仅文本摘要）。
    - verifier 基于实际变更结果验证并回执。
    - parent 侧有明确成功/回滚决策。
  - 代码：`internal/runtime/agent/scheduler.go`，`internal/runtime/agent/prompt_builder.go`，`internal/runtime/agent/loop.go`

- `[x]` 补齐 streaming 与 planned subagents 的主线接入
  - 原问题：`enable_react` 与 `execute_planned_subagents` 在 streaming 模式下被拒绝。
  - 目标：定义增量事件协议与流式执行保护策略。
  - 进展：已支持 `stream + execute_planned_subagents` 通过静态 SSE 结果返回，并新增 `subagent` 事件输出子代理结构化回执；`enable_react + stream` 也已接入静态 SSE 返回。
  - 验收：
    - streaming 下可观测子代理开始/完成/失败事件。
    - 保留当前治理边界（single-writer/read-only/sandbox）。
  - 代码：`internal/api/skills/handler.go`，`internal/runtime/agent/orchestrator.go`

- `[-]` Tool Catalog 升级到 BM25/FTS 级目录化检索
  - 当前：轻量 catalog + ranked search，缺少结构化索引与 BM25/FTS 评分。
  - 目标：引入 FTS/BM25 索引与查询解析，支持短语匹配与字段权重。
  - 进展：已升级为内存型 BM25 风格目录化索引（name/description/args 字段权重、doc freq、phrase/exact-name 优先）；已补 catalog refresh diff stats（added/removed/updated/last_refresh_at）并接入 runtime status / MCP reload 事件；已补 `SnapshotStore` 接口与 `NewWithStore/NewGatewayWithStore` 边界，并落地了文件型/SQLite 型 snapshot store；SQLite store 已具备持久化查询、短语/exact-name 提升与增量同步能力；handler/runtime config 已支持选择 `memory/file/sqlite` backend。当前剩余主要是生产级索引维护与更深入的 FTS 查询增强。
  - 子任务：
    - 设计目录化索引结构（表结构 / 字段权重 / 归一化方案）。
    - 实现索引构建与增量刷新（与 MCP refresh 生命周期对齐）。
    - 查询解析与 BM25/FTS 打分，支持短语查询与字段权重。
    - 回退路径：FTS 不可用时自动降级到现有 ranked search。
    - 覆盖测试：短语优先、字段权重、同分排序与回退路径。
  - 验收：
    - 目录化索引可重建，查询稳定且可解释。
    - 新增回归测试覆盖短语/字段/同分排序。
  - 代码：`internal/mcp/catalog/*`

- `[x]` 多 agent 能力主线收口（非 `enable_react` 入口）
  - 当前：默认主线已通过 Orchestrate 走 planner/subagents；`enable_react` 作为可选路径保留。
  - 进展：`AgentChat` 默认走 Orchestrate 主线；非 `enable_react` 模式下可执行 planned subagents，且 streaming 静态 SSE 会发出 `subagent` 事件。
  - 目标：将 subagent / planner / streaming 能力收口到默认主线入口。
  - 验收：
    - 主线 API 在非 `enable_react` 模式下可触发 subagents。
    - streaming 下可观测子代理事件与治理边界。
  - 代码：`internal/api/skills/handler.go`，`internal/runtime/agent/orchestrator.go`

- `[x]` Agent-first API 入口调整（避免 `/api/skills` 语义误导）
  - 当前：主入口为 `/api/skills/agent/chat`，名称容易被理解成“skills 执行 API”。
  - 进展：已新增 `/api/agent/chat` canonical 路由；`/api/skills/agent/chat` 增加 compatibility warning header；Go client 与对外文档默认切到 `/api/agent/chat`；`/api/skills/{name}/execute` 增加 admin/debug warning header。
  - 目标：
    - 新增 `/api/agent/chat` 作为主入口（agent-first 命名）。
    - `/api/skills/agent/chat` 保持兼容别名。
    - `/api/skills/{name}/execute` 明确为 admin/debug（可加 warning header 或 admin token gating）。
    - Go client 与示例优先使用 `/api/agent/chat`。
  - 验收：
    - `/api/agent/chat` 与 `/api/skills/agent/chat` 都可正常执行。
    - 旧入口保持兼容，但有明显“兼容/legacy”提示。
    - 关键路由/测试已更新覆盖双入口。
  - 代码：`internal/gateway/router/gin.go`，`internal/gateway/router/*_test.go`，`pkg/skillsapi/client.go`，`docs/skill_runtime/*`

### P3 - Context OS 与治理完善（中优先级）

- `[x]` 增强跨 agent 观测与运维视图
  - 当前：event bus 已可用，但聚合与运维视图仍偏基础。
  - 目标：补齐 trace 聚合查询与治理告警维度。
  - 进展：SSE 已补充 `subagent` 事件；planning/subagent summary 已补充 patch verification 统计；`/runtime/traces*` 现支持 `tool_name` 过滤，并新增 `execution` 聚合视图，可直接观测 `tool.reduced` / reducer / artifact refs / subagent batch wave / patch.applied 关键路径。
  - 验收：
    - 按 trace/session/agent 的查询和统计更完整。
    - 能直接观测 denied policy、subagent wave、tool reduction 关键路径。
  - 代码：`internal/runtime/events/*`，`internal/api/skills/handler.go`

## 本次实施记录（2026-03-14）

- 已完成：
  - system role fallback 覆盖增强（补齐常见报错格式与 assistant-first 历史场景）
  - 新增 `/api/agent/chat` canonical 入口，兼容入口与 `/execute` 增加 warning header
  - Go client 默认改用 `/api/agent/chat`
  - skills / multi-agents 相关文档默认入口切换到 `/api/agent/chat`
  - Output Gateway 新增通用 `JSONReducer` / `TableReducer` / `LogReducer` 并接入默认链
  - Context OS 新增 `hot/warm/cold` profile alias、层级摘要与长会话对照测试
  - writer patch 闭环新增 `apply_status/applied_by/artifact_refs`，并接入 `patch.applied` 审计事件
  - runtime traces 新增 `tool_name` 过滤与 `execution` 聚合视图，覆盖 reducer / artifact refs / subagent wave / patch apply
  - planner 主线退化修复（LLM 优先 + 启发式 fallback）
  - tool catalog 检索排序增强
  - memory 统计稳定性修复
  - streaming 支持执行 planned subagents（静态 SSE 返回）
  - streaming 增加 `subagent` 事件，输出子代理回执
  - streaming 支持 `enable_react`（静态 SSE 返回）
  - writer patch 抽取支持 unified diff 输出回退
  - writer patch 增加 `verification_status/verified_by`，由依赖 verifier 回写
  - planning/subagent summary 增加 patch verification 统计字段
  - parent 增加 patch 决策字段（`patch_decision/patch_decision_reason/patch_decision_required`）
  - parent 决策为 `blocked` 时，API/SSE 最终状态显式标注为 `blocked`
  - parent 支持 `strict/warn/override` 三种 patch 决策路径
  - 结构化 `patch_approval` 审计对象已落地（`ticket_id/approver/reason`）
  - `patch_approval` 已接入 runtime event bus，可通过 trace 检索审批票据
  - `/runtime/traces` 与 `/runtime/traces/stats` 已汇总 patch decision / override / ticket 覆盖率
  - `TraceSummary` / 单 trace summary 已包含 `patch_approval_tickets`
  - `trace / traces / governance / stats` 四类查询入口均已提供 patch governance 专用统计块或票据摘要
  - `runtime/status` 与 `runtime/health` 已补充独立 `patch_governance` 汇总块
  - `Context OS` 已支持显式 profile/config，并在 runtime `status/health` 中可见
- 已执行测试：
  - `go test ./internal/runtime/output`
  - `go test ./internal/runtime/agent -run "TestReActLoop_Run_UsesOutputGatewayForToolResults|TestReActLoop_Run_ContextManagerRecallsArtifacts"`
  - `go test ./internal/runtime/contextmgr`
  - `go test ./internal/runtime/config -run "TestValidateContextConfig|TestRuntimeManager_VersionHistoryAndRollback"`
  - `go test ./internal/runtime/agent -run "TestNewDefaultContextManager_UsesProfileAndOverrides"`
  - `go test ./internal/runtime/agent -run "TestSubagentScheduler_RunChildren_WriterReportsPatches|TestSubagentScheduler_RunChildren_WriterExtractsDiffFromOutput|TestSubagentScheduler_RunChildren_DependencyInjectsWriterPatchesIntoVerifier|TestAgent_Orchestrate_PlannerPreferredPatchDecisionBlockedWhenVerifierFails|TestAgent_Orchestrate_PlannerPreferredPatchDecisionManualOverrideObject"`
  - `go test ./internal/runtime/events`
  - `go test ./internal/runtime/agent -run "TestSubagentScheduler_RunChildren_WriterReportsPatches"`
  - `go test ./internal/api/skills -run "TestGetRuntimeTrace_ReturnsEventsForTrace|TestGetRuntimeTraces_ReturnsRecentSummaries|TestGetRuntimeTraceStats_ReturnsAggregates|TestGetRuntimeTraces_FiltersByToolName|TestGetRuntimeTraceGovernance_ReturnsDeniedView"`
  - `go test ./internal/api/skills -run "TestAgentChat_PlannerPreferredCanExecutePlannedSubagents|TestAgentChat_StreamSSE_ExecutesPlannedSubagents"`
  - `go test ./pkg/skillsapi -run "TestAgentChatResponse_DecodeResult"`
  - `go test ./internal/api/skills -run "TestGetRuntimeStatus_RequiresAdmin|TestContextSnapshotFromRuntimeConfig_ResolvesLayers"`
  - `go test ./internal/runtime/agent ./internal/mcp/catalog ./internal/runtime/memory`
  - `go test ./internal/api/skills`
