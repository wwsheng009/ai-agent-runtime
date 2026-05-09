# Skills Runtime 实施进度报告

**创建时间**: 2025-03-07
**最后更新**: 2026-03-10
**状态**: 通用 `skill / agent` 平台主链路可用，当前进入架构对齐、平台治理与 AI 可调用能力收口阶段

> 迁移说明（2026-03-30）：
> - 本文是历史进度日志，不是当前操作手册。
> - 文中旧 `internal/runtime/*`、`internal/api/skills/*` 路径，如需映射到当前仓库，请按 `E:\projects\ai\ai-agent-runtime\backend\internal\...` 理解。
> - 文中 `configs/config.yaml`、`configs/runtime.yaml` 与所有 `go test ./...` / `go run ./cmd/...` 命令，如需今天重跑，请在 `E:\projects\ai\ai-agent-runtime\backend` 目录执行。
> - 文中出现的 `/monitor/*`、`/metrics`、`/api/skills/runtime/*` 多为 gateway-era 或旧 runtime 观测面；runtime 当前对外主入口请改看 `/api/runtime/status`、`/api/runtime/health`、`/api/runtime/traces*`、`/api/runtime/skills/search/stats`、`/api/runtime/usage/*`。

---

## 2026-03-08 审计结论

- 代码层面已落地的核心模块：LLM Runtime、Model Router、Agent ReAct Loop、Skill Executor LLM 集成、Chat/Session、Embedding Router、Observability、ParallelExecutor、GatewayClient、HotReload。
- 已验证通过的包（按当时命名记录）：`internal/runtime/agent`、`internal/runtime/chat`、`internal/runtime/observability`、`internal/runtime/e2e`、`internal/runtime/executor`、`internal/runtime/embedding`。
- 2026-03-08 已完成的收口项：
  1. `backend/internal/llm` 测试已通过：修复 `token_budget.go` 的估算/预算判定逻辑与策略元数据。
  2. `backend/internal/errors` 测试已通过：修复 `RuntimeError.WithContext()`，改为返回带拷贝上下文的新错误对象。
  3. `backend/internal/api/skills` 已可编译：补齐 `github.com/gorilla/mux` 依赖，并将过期的 `Agent.Execute` 调用改为 `Agent.Run`。
-  4. `backend/internal/api/skills` 已具备基础可执行能力：
     - `ExecuteSkill` 已接入 `skill.Executor`
     - `AgentChat` 已支持基础 Session 持久化、可选 Skill 路由和 LLM 直聊回退
     - 新增 `backend/internal/api/skills/handler_test.go` 验证执行与会话行为
-  5. `backend/internal/api/skills` 已补充基础 Session/History 管理接口：
     - 新增 `CreateSession` / `ListSessions` / `GetSession` / `DeleteSession` / `GetSessionHistory` / `GetSessionStats`
     - 修复 `RegisterRoutes()` 中固定路径可能被 `/{name}` 路由吞掉的问题
     - 新增测试覆盖 Session 生命周期与固定路由优先级
-  6. `ParallelExecutor` 已接入 Skill Workflow 主执行路径：
     - `backend/internal/skill/executor.go` 已改为通过 `runtime/executor.ParallelExecutor` 执行 workflow DAG
     - 为依赖失败场景补了下游节点快速失败逻辑，避免等待死锁
     - 新增 `backend/internal/skill/executor_test.go` 验证 workflow 并行执行已生效
-  7. `AgentChat` 已支持基础 SSE 流式响应：
     - 支持 `stream: true` 或 `Accept: text/event-stream`
     - 支持 `meta` / `chunk` / `done` 三类基础事件
     - 流式完成后仍会把用户消息与最终助手回复落入 session
-  8. `GatewayClient` 已补齐 Runtime 注册挂点：
     - `LLMRuntime` 新增 `RegisterGatewayClient()`
     - 运行时可直接把 `loadbalancer.ResourceManager` 注册为 LLM Provider
     - 新增 `backend/internal/llm/runtime_test.go` 覆盖该接线入口
-  9. `HotReload` 已补齐高层控制入口：
     - `backend/internal/api/skills` 新增 `start / stop / reload / stats` 控制接口
     - 修复 `HotReload.Reload()` 直接改 registry 底层字段导致索引不一致的问题
     - 为文件删除场景增加 skill 名称映射，支持正确移除 registry 中的技能
     - 新增 API 测试覆盖 `HotReload` 生命周期
- 10. 已新增统一 Runtime Bootstrap 组装层：
     - 新增 `backend/internal/bootstrap/manager.go`
     - 统一组装 `LLMRuntime`、`Registry`、`Loader`、`SessionManager`、`HotReload`
     - 当存在 `ResourceManager` 时，自动通过 `RegisterGatewayClient()` 接入 Gateway Provider
     - 提供 `ApplyToSkillsHandler()`，将 runtime 组件一次性接线到 `backend/internal/api/skills`
     - 新增 `backend/internal/bootstrap/manager_test.go` 验证组装与接线
- 11. 历史：`runtime/bootstrap` 曾接入网关入口：
     - 当时为 `internal/config` 新增了 `skills_runtime` 配置块，支持 `enabled / config_file / skill_dir / gateway_provider_name`
     - 当时 `internal/gateway/router/GinEngine` 会在初始化阶段加载 runtime config、创建 bootstrap manager，并将 `/api/skills` 路由挂入网关
     - 当 `aicli.mcp.config_file` 配置存在且 `auto_connect=true` 时，可选接入 MCP Adapter；关闭时安全跳过
     - 当前独立入口请改看 `backend/cmd/runtime-server/main.go`
- 当前剩余工作：真实 Provider/MCP 集成、`backend/internal/api/skills` 能力补全与文档持续同步。
- 12. 真实 Provider 配置已接入 runtime/bootstrap 与历史网关入口：
     - 历史上 `internal/gateway/router/GinEngine` 会从 `providers.items` 构建 `llm.ProviderConfig` 并注入 bootstrap
     - `LLMRuntime` 新增 provider alias 能力，`default model` 与 `supported_models / model_mappings` 可直接解析到真实 Provider
     - `ProviderWrapper` 已实现统一 runtime interface，并支持 `default_model` 与 `model_mappings`
- 13. 配置驱动的 live provider smoke 已通过：
     - 历史上新增了 `internal/gateway/router/gin_live_provider_test.go`，默认跳过，设置 `LIVE_PROVIDER_TEST=1` 后执行
     - 2026-03-08 已基于 `backend/configs/config.yaml` + `configs/.env` 实测跑通 agent chat 主链路；当前源码路由主入口为 `POST /api/agent/chat`
     - 本次实测命中 `nvidia`（OpenAI 兼容）provider，模型为 `z-ai/glm4.7`
- 14. 本地 toolkit MCP smoke 已通过：
     - 新增 `internal/gateway/router/gin_live_mcp_test.go`，默认跳过，设置 `LIVE_MCP_TEST=1` 后执行
     - 2026-03-08 已基于 `bin/toolkit-mcp.exe` + 临时 `mcp.yaml` 实测跑通 `POST /api/skills/local-mcp-bash/execute`
     - 本次实测完成 `AICLI MCP -> bootstrap -> skills API -> MCP tool(bash)` 本地整链路验证
- 15. Remote WebSocket MCP smoke passed:
     - Added `internal/gateway/router/gin_live_remote_mcp_test.go`; skipped by default and runs with `LIVE_MCP_TEST=1`
     - On 2026-03-08, started an in-process WebSocket MCP server from `internal/mcp/server/echo` and verified `POST /api/skills/remote-mcp-echo/execute`
     - Verified end-to-end path: `WebSocket MCP -> AICLI MCP Manager -> bootstrap -> skills API -> echo tool`
     - Validation split into two steps and both passed: (1) MCP manager connectivity via `internal/mcp/manager/manager_live_test.go`, (2) skills API end-to-end via `internal/gateway/router/gin_live_remote_mcp_test.go`
- 16. External ABAP ADT MCP smoke passed:
     - Added `internal/mcp/manager/manager_abap_live_test.go` and verified the user-provided stdio config for `ecc1809`
     - On 2026-03-08, manager-level validation connected to `node E:/projects/abap/mcp-abap-abap-adt-api/dist/index.js`, discovered 65 tools, and successfully called `login`
     - Added `internal/gateway/router/gin_live_abap_mcp_test.go` and verified `POST /api/skills/abap-mcp-help/execute` through the ABAP MCP `help` tool
- 17. `internal/api/skills` ??????????
     - ?? `sessions/search`?`PATCH /sessions/{id}`?`DELETE /sessions/{id}/history`
     - ?? `POST /sessions/{id}/archive|activate|close` ?? `batch/archive|batch/delete`
     - ?? `internal/api/skills/handler_test.go` ??????????????????????
- 18. `AgentChat` 结果与流式事件已增强：
     - 非流式 `result` 统一带上 `kind` / `source`，区分 `agent_route`、`agent_direct`、`llm_fallback`
     - SSE 新增 `result` 事件，并为 `reasoning` / `tool_start` / `tool_end` / `tool_call` 提供专属事件名，同时保留 `meta/chunk/done` 兼容输出
     - 新增 `internal/api/skills/handler_test.go` 覆盖 richer LLM stream 与 agent-route stream 场景
- 19. `AgentChat` 已补充统一编排摘要：
     - `result.orchestration` 统一提供 `route_attempted`、`route_matched`、`fallback_reason`、`observation_summary`、`output_preview`
     - 非流式与流式路径均输出一致的摘要语义；SSE 额外新增 `orchestration` 事件
     - 新增 fallback 与 route-match 测试，覆盖 `agent_route` 与 `llm_fallback` 两种摘要分支
- 20. `AgentChat` 编排摘要已加入 route candidates：
     - `result.orchestration` 现包含 `candidate_count` 与 `route_candidates`，展示候选 skill、score、matched_by、details
     - 已覆盖 route-match 与 no-match 两种场景，确保 route 候选在摘要和 SSE 事件中一致可见
- Remaining work: deeper Agent semantics beyond current route candidates, non-text streaming consumer contracts beyond the current envelope, ABAP business-tool validation, and ongoing doc sync.
- 21. `AgentChat` observation 摘要已增强：
     - `observation_summary` 新增 `failed_details`、`step_durations_ms`、`total_duration_ms`、`max_duration_ms`、`average_duration_ms`
     - `route_candidates` 新增 `chosen` 与 `selection_reason`，可区分 selected / fallback_to_llm / not_selected
- 22. `AgentChat` SSE 已补充稳定 envelope：
     - 所有流式事件现在统一带 `_event` 元数据，包含 `name`、`schema_version`、`timestamp`，以及流式 emitter 提供的 `sequence`
     - 保持 `meta/chunk/result/orchestration/done` 兼容事件名不变，同时为非文本 consumer 提供稳定解析锚点
- 23. `AgentChat` 非文本流式事件 schema 已显式化：
     - `reasoning` 事件固定输出 `reasoning.content/delta/length`
     - `tool_call` / `tool_start` / `tool_end` 事件固定输出 `tool.id/name/args/status/content`，并保留兼容字段 `tool_call` / `delta`
- 24. `agent_route` 静态流已补充 `route` / `observation` 事件：
     - 命中技能的静态流现在会显式发出 `route` 事件（skill + candidates）以及逐条 `observation` 事件（step/tool/success/error/duration_ms）
     - 进一步完善了非文本 consumer 对 agent-route 场景的事件可见性
- 25. 已补充正式流式协议文档：
     - 新增 `docs/skill_runtime/skills_api_stream_contract.md`
     - 覆盖 `meta/chunk/reasoning/tool_*/orchestration/route/observation/result/done/error` 事件及 `_event` envelope
- 26. 已补充非流式结果协议文档：
     - 新增 `docs/skill_runtime/skills_api_result_contract.md`
     - 覆盖 `ExecuteSkill` 与 `AgentChat` 的 JSON 响应结构、`result.kind/source` 与 `orchestration` 摘要字段
- 27. 已补充平台定位与路线文档：
     - 新增 `docs/skill_runtime/platform_runtime_roadmap.md`
     - 将目标重新定义为通用 `skill / agent` 平台，而非仅限 coding agent
- 28. 本地 embedding 语义路由已接入 bootstrap 主链路：
     - `Runtime Bootstrap` 现在会按 `router.enableEmbedding + embedding.enabled` 自动构造本地 `SemanticEmbeddingRouter`
     - 默认 embedding 生成器改为本地特征哈希实现，不再依赖外部 embedding API
     - `skills.Handler` / `AgentChat` / `HotReload` 已接上同一套 embedding 索引与增量更新
     - 已补充 `bootstrap`、`skills API`、`gin` 入口回归测试，验证语义路由可开箱使用
- 29. `SearchSkills` 与 `GetStats` 已接入 embedding 能力：
     - 当前 standalone runtime 路径为 `GET /api/runtime/skills/search`，支持 `mode=auto|lexical|semantic|hybrid`
     - 自动模式会在词法无命中时回退到本地语义搜索，并返回 `requested_mode/resolved_mode/used_embedding/matches`
     - 当前 standalone runtime 路径为 `GET /api/runtime/skills/stats`，返回 embedding router 是否启用及当前索引统计
- 30. 已补充搜索观测与索引运维接口：
     - 当前 standalone runtime 路径为 `GET /api/runtime/skills/search/stats`，返回搜索请求总量、模式分布、embedding 命中次数、最近一次查询与重建状态
     - 当前 standalone runtime 路径为 `POST /api/runtime/skills/search/reindex`，支持手动重建本地 embedding 索引
     - `GET /api/runtime/skills/stats` 同时聚合返回 `search` telemetry，便于平台统一观测
- 31. 已为搜索运维接口补充轻量安全控制：
     - `GET /api/runtime/skills/search/stats` 与 `POST /api/runtime/skills/search/reindex` 默认仅允许 loopback 访问
     - 可通过 `skills_runtime.admin_token` 放开远程运维访问，支持 `Authorization: Bearer <token>` 或 `X-Skills-Admin-Token`
     - `POST /api/runtime/skills/search/reindex` 增加冷却时间控制，默认 `30s`，超限返回 `429` 与 `Retry-After`
- 32. 已补充搜索运维审计日志：
     - `search/stats` 与 `search/reindex` 现在会记录结构化审计日志
     - 审计字段包含 `action/outcome/access_mode/remote_ip/request_id/search_summary`
     - 支持区分 `success/forbidden/rate_limited/failed`，便于后续接统一 observability
- 33. 已将搜索运维事件接入 runtime metrics：
     - 新增 `search_admin_actions_total`，按 `action/outcome/access_mode` 统计搜索运维行为
     - 新增 `search_reindex_runs_total`，统计 `search_reindex` 的成功/失败/限流等结果
     - 相关计数已在 `skills` handler 测试中覆盖，便于后续接 Prometheus 或统一监控页
- 34. 已桥接到现有监控 API：
     - 历史：曾新增 `pkg/monitoring` bridge collector，将 runtime observability 中的 `search_*` 指标暴露给 gateway 的 `/monitor/metrics` 与 `/monitor/search?prefix=search_`
     - 网关初始化时已自动注册该 collector
     - 已补充网关回归测试，验证触发 `search/stats` 后可从监控 API 读取到 `search_admin_actions_total`
- 35. 已接入 Prometheus 抓取端点：
     - 历史：`pkg/monitoring` 曾新增 Prometheus collector bridge，collector 指标可直接出现在 gateway 的 `/metrics`
     - 历史：`search_admin_actions_total` 与 `search_reindex_runs_total` 曾同时可从 gateway `/monitor/*` 与 `/metrics` 获取
     - 已补充 monitoring 包与网关层回归测试，验证 Prometheus 文本输出中包含 `search_admin_actions_total`
- 36. 已补充聚合监控摘要接口：
     - 历史：曾新增 gateway 侧 `GET /monitor/search-admin`
     - 直接返回 `total_actions / total_reindex_runs / by_action / by_outcome / by_access_mode / reindex_outcomes`
     - 便于 dashboard 或运维脚本直接消费，无需自行解析原始 metric 列表
- 37. 已补充搜索监控与运维指南：
     - 新增 `docs/skill_runtime/search_monitoring_guide.md`
     - 历史上覆盖 gateway `/monitor/search-admin`、`/monitor/search?prefix=search_`、`/metrics`，当前 runtime 侧请改看 `/api/runtime/skills/search/*` 与 `/api/runtime/*`
     - 包含 access control、cooldown、PromQL 示例、运维检查项与建议 dashboard blocks
- 38. 已补充搜索故障排查 playbook：
     - 新增 `docs/skill_runtime/search_monitoring_playbook.md`
     - 覆盖 `forbidden` 激增、`rate_limited` 激增、reindex 失败、语义命中下降四类故障
     - 明确 symptom / checks / causes / remediation / escalation rule
- 39. 已完成 `docs/skill_runtime/design` 全量设计审计：
     - 读取并归纳 `agent_skill_runtime-1.md` 到 `agent_skill_runtime-16.md`
     - 设计主线已拆分为 `Skill Runtime 内核`、`通用 Agent/Tool/MCP Runtime`、`Context/Retrieval Runtime`、`垂直场景增强`
     - 确认当前项目更适合按“通用 `skill / agent` 平台”解释，而不是按“coding-agent-only runtime”解释
- 40. 已补充当前架构与设计对照文档：
     - 新增 `docs/skill_runtime/current_architecture.md`
     - 明确记录当前启动流、执行流、AgentChat 流、搜索流、监控流的数据流转方式
     - 明确标记平台内核低偏离、平台治理中偏离、coding-specific 高偏离但不再视为平台阻塞
- 41. 已重置路线图与实施优先级：
     - 更新 `docs/skill_runtime/platform_runtime_roadmap.md`
     - 更新 `docs/skill_runtime/plan/IMPLEMENTATION_TODO.md`
     - 下一阶段优先级调整为：`capability abstraction`、`aicli` 集成、平台治理、SDK/交付面、retrieval/memory abstraction
- 42. `aicli chat` 已可将 skills 暴露为 AI 可调用 functions：
     - 新增 `backend/cmd/aicli/commands/skills_integration.go`
     - `HandleChat()` 现在会按 `skills_runtime` 配置加载本地 skills runtime，并把每个 skill 注册为 `skill__*` function
     - skill function 复用当前 chat 会话的 provider/model 元数据与最近对话历史，执行链为 `aicli chat -> FunctionRegistry -> SkillFunction -> skill.Executor`
     - 新增 `backend/cmd/aicli/commands/skills_integration_test.go` 覆盖 skill function schema、请求构建与注册流程
- 43. `aicli chat` 已完成真实 provider + skill function 联调：
     - 新增默认 smoke skill：`.agents/skills/skill_runtime_smoke/skill.yaml`
     - 2026-03-09 已基于 `nvidia` / `z-ai/glm4.7` 实测跑通 `aicli chat -> skill__skill_runtime_smoke -> skill.Executor -> LLMRuntime -> final answer`
     - 同时修复了对不接受 `system` 角色的上游兼容问题：`backend/internal/skill/executor.go` 现在会在命中相关 400 错误时，把 system prompt 合并进 user 消息后重试
     - 新增 `backend/internal/skill/executor_test.go` 覆盖该兼容回退逻辑
- 44. workflow skill 已支持动态参数模板：
     - `backend/internal/skill/executor.go` 新增 `{{prompt}}`、`{{context.*}}`、`{{options.*}}`、`{{metadata.*}}`、`{{results.*}}` 模板渲染
     - workflow 步骤现在可以把用户输入和前置步骤结果安全地映射到 MCP tool 参数
     - 新增 `backend/internal/skill/executor_test.go` 覆盖模板渲染与单步输出格式化
- 45. 已补充首批可实际使用的通用 workflow skills：
     - `.agents/skills/run_shell_command/skill.yaml`
     - `.agents/skills/fetch_url_content/skill.yaml`
     - `.agents/skills/view_file_content/skill.yaml`
     - 当前默认 skill pack 数量提升到 4 个（含 smoke skill）
- 46. `aicli chat` 已基于真实 `nvidia` provider 完成 `run_shell_command` live 验证：
     - 2026-03-09 已实测跑通 `aicli chat -> skill__run_shell_command -> workflow -> toolkit MCP bash`
     - 非交互验证命令中，模型成功发起 skill function call，并执行 `echo SKILL_SHELL_OK`
- 47. skills 已支持“系统目录 + 外部目录”分层加载：
     - `skills_runtime.skill_dir` 保留为系统级 skills 目录，仅放平台通用功能
     - 新增 `skills_runtime.extra_skill_dirs`，用于加载外部或业务域 skill pack
     - `aicli chat` 新增 `--skills-dir` 参数，可按会话追加外部 skills 目录
     - 新增 `.agents/skills/README.md` 明确：ABAP / ERP 等垂直能力不应放入系统 skill 目录
- 48. 已完成外部目录 live 验证：
     - 2026-03-09 使用临时目录 + `aicli chat --skills-dir <dir>` 实测加载外部 skill
     - 基于 `nvidia` / `z-ai/glm4.7` 跑通 `aicli chat -> skill__external_runtime_smoke -> final answer`
     - 验证说明：外部 skills 无需放在 `skill_runtime` 仓库目录内，按配置或 CLI 指定目录即可加载
- 49. 网关侧 skills runtime 已补齐多目录模型：
     - `Loader` 已支持 `SetSkillDirs / GetSkillDirs / LoadAllWithRegistry`
     - `HotReload` 已支持 `StartMany()`，并能对多目录执行 reload / stats
     - `runtime/bootstrap` 现在会把系统目录与扩展目录一起接入 loader 与 hot reload
     - `skills API` 的 `GetStats` 与 `hot-reload/start` 响应现在会显式返回 `skill_dirs / dirs`
- 50. 系统 skills 目录已从 `docs/skill_runtime/skills` 迁移到 `.agents/skills`：
     - `backend/configs/config.yaml` 与 `backend/configs/config.runtime.snapshot.yaml` 的默认 `skill_dir` 已切换为 `./.agents/skills`
     - `docs/skill_runtime/skills` 旧目录已删除，`.agents/skills` 下的新 system skills 清单已落地
     - 2026-04-29 已回归验证：`internal/bootstrap` 的 `SKILL.md` 自动加载测试、`internal/runtimeserver` 路径解析测试、`cmd/runtime-server` 与 `cmd/aicli/commands` 相关测试全部通过
- 50. skills 来源元数据已接入 runtime/API：
     - `skill.Skill` 新增运行时 `source` 字段，包含 `path / dir / layer`
     - loader / hot reload 会为文件加载的 skills 标记 `system` 或 `external`
     - API 动态创建的 skills 会标记为 `runtime`
     - `ListSkills` / `GetSkill` / `GetStats` 现在都能看到 skill 来源层级与目录
- 51. 来源策略已进一步收口：
     - 多目录加载遇到同名 skill 时，默认采用“先加载目录优先”，后续目录中的同名 skill 会被跳过
     - 在当前目录模型下，这意味着系统级 skill 默认优先于外部扩展 skill
     - `ListSkills`、`SearchSkills`、`GetStats` 已支持按 `source_layer` / `source_dir` 过滤
     - `GetStats` 额外返回 `source_summary`，便于直接查看系统级 / 外部 / runtime skill 分布
- 52. `skills API` 已补充基础持久化与真实 reload 能力：
     - `CreateSkill` / `UpdateSkill` / `BatchCreateSkills` 支持通过 `?persist=true` 持久化到外部 skill 目录
     - 默认会优先落到第一个外部目录；若无外部目录则要求显式提供 `target_dir`
     - 明确禁止将 runtime skill 持久化到系统级 skill 目录
     - `ReloadSkills` 不再是占位实现，现已按当前多目录配置真实重载 registry 和 embedding 索引
- 53. 来源过滤与持久化已补充回归测试：
     - 覆盖外部目录持久化、拒绝写入系统目录、按 `source_layer/source_dir` 过滤，以及多目录 reload 行为
- 54. 已补充正式来源/持久化文档：
     - 新增 `docs/skill_runtime/skill_sources_and_persistence.md`
     - 覆盖 `system / external / runtime` 三层来源、目录优先级、`persist=true`、`target_dir`、`source_layer/source_dir` 过滤、reload 行为
- 55. `ReloadSkills` 已支持 request body `dir / dirs`：
     - 现在与 `hot-reload/start` 的目录指定方式保持一致
     - 已补充 `handler` 测试验证 request body 多目录 reload
- 56. `admin_token` 已扩展为统一 skills 管理令牌：
     - 不再只保护 `search/stats` 与 `search/reindex`
     - 现在同时保护 `create/update/delete/batch/import/reload/hot-reload` 等 skills 写操作
     - 已补充 loopback / token 模式测试，验证远程无 token 时返回 `403`
- 57. 外部 skill 文件策略已进一步收口：
     - 更新已持久化的 external skill 时，若未显式 `persist=false`，默认回写原始 manifest 文件
     - 删除 skill 时支持 `?delete_file=true` 同步删除 external skill 的 manifest；system skill 明确禁止物理删除
     - `ImportSkills` 导入的 skill 现在会统一标记为 `runtime` 来源
- 58. skills 变更治理已接入 runtime metrics 与监控面：
     - 新增 `skill_mutation_actions_total`，按 `action/outcome/access_mode` 统计 `create/update/delete/import/reload/hot-reload` 等变更操作
     - 历史：曾新增 gateway `/monitor/skill-mutations` 摘要接口，并可通过 `/monitor/search?prefix=skill_mutation_` 与 `/metrics` 查看原始指标
     - 已补充 `handler`、`monitoring`、`gateway` 回归，验证 skills 变更触发后指标可从 API 与 Prometheus 抓取端看到
- 59. 已补充 skills 轻量治理策略：
     - `skills_runtime` 新增 `read_only / disable_import / disable_persist / disable_reload_ops / disable_hot_reload_ops`
     - `skills API` 会在变更入口前执行策略校验，并将被策略拒绝的操作标记为 `disabled`
     - `GET /api/skills/stats` 现已返回 `mutation_policy`，便于运维核对当前生效策略
- 60. 已新增 Go typed client：`pkg/skillsapi`
     - 当前已覆盖 `List/Get/Search/Stats/Execute/AgentChat/AgentChatStream/Create/Update/Delete`
     - 已补充集成风格测试，直接对接真实 `skills.Handler`
     - 已新增使用文档 `docs/skill_runtime/skills_api_client.md`
- 61. `pkg/skillsapi` 已扩展到管理与 session 全量主路径：
     - 新增 `import/export/reload/hot-reload/search admin/session` 等 client 方法
     - 已补充集成风格测试，覆盖 session 生命周期、search reindex、reload/hot-reload、import/export
     - `skills_api_client.md` 已更新为完整 client 能力清单与示例
- 62. 已补最小 usage / quota 闭环：
     - `skills_runtime` 新增 `usage_tracking_enabled / quota_enabled / default_max_requests / default_max_tokens`
     - `ExecuteSkill` 与 `AgentChat` 已接入进程内 usage tracking，并在执行前支持默认 request/token quota 拒绝
     - 新增 admin 接口 `/api/skills/usage/stats` 与 `/api/skills/usage/reset`
- 63. usage / quota 已接入观测面：
     - 新增 runtime metrics：`skill_usage_requests_total`、`skill_usage_tokens_total`、`skill_quota_denials_total`
     - 历史：曾新增 gateway 侧监控摘要接口 `/monitor/skill-usage`
     - 已补充 `monitoring`、`skills handler`、`gateway`、`pkg/skillsapi` 回归
- 64. 文档已同步 usage / quota：
     - 新增 `docs/skill_runtime/skills_usage_quota.md`
     - 更新 `docs/skill_runtime/skills_api_client.md`
- 65. usage / quota scope 已升级为 `tenant / project / user`：
     - 当前 quota key 已从单一 `user_id` 升级为 `tenant_id / project_id / user_id`
     - 同一用户在不同 tenant 或 project 下的 usage / quota 现在彼此隔离
     - `pkg/skillsapi` 已新增 `GetUsageStatsWithScope()`，并允许 `ExecuteSkill` / `AgentChat` 直接携带 `tenant_id` 与 `project_id`
- 66. 已支持配置型 scoped quota policy：
     - `skills_runtime.quota_policies.tenants / projects / users` 已生效
     - key 支持 `tenant`、`tenant/project`、`tenant/project/user`，并按 `user > project > tenant > default` 解析
     - 已补充 `handler` 与 `gateway` 回归，验证 override precedence 与网关接线
- 67. 已补 runtime usage policy 管理接口：
     - 新增 `GET /api/skills/usage/policy`、`PUT /api/skills/usage/policy`、`DELETE /api/skills/usage/policy`
     - `pkg/skillsapi` 已新增 `GetUsagePolicy()`、`UpdateUsagePolicy()`、`DeleteUsagePolicyEntry()`
     - 已补充 `handler` 与 client 回归，验证 policy merge、delete 与 precedence 更新
- 68. 已补持久化 usage ledger 最小闭环：
     - 新增 `skills_runtime.usage_ledger_enabled`
     - `GET /api/skills/usage/ledger` 现可读取持久化 usage 历史，并支持按 scope / entrypoint / skill / success / since 过滤
     - `pkg/skillsapi` 已新增 `GetUsageLedger()`；已补充 handler/client 回归验证 ledger 写入与查询
- 69. 已补 scope resolver 与 auth/header/API key 绑定：
     - `skills_runtime` 新增 `scope_resolver_enabled / tenant_headers / project_headers / user_headers / api_key_scopes`
     - `skills runtime` 现支持从请求体、query、认证头、API key 映射中解析 `tenant/project/user` scope
     - 网关转发 `/api/skills/*` 时会把 gin context 中的 `user_id / tenant_id / project_id` 透传为 `X-Skills-Auth-*`
## 完成情况

### ✅ 已完成 (Sprint 1 - 核心运行能力)

#### 1. LLM Runtime 核心模块
- ✅ `backend/internal/llm/runtime.go` - 统一 LLM 运行时
  - Provider 注册和管理
  - 统一调用接口 `Call()` 和 `Stream()`
  - 错误处理和重试机制
  - 健康检查
  - Token 计数集成

- ✅ `backend/internal/llm/router.go` - 模型路由器
  - 路由规则管理
  - 优先级排序
  - 多种路由条件：
    - TokenBudgetCondition (Token 预算)
    - TaskTypeCondition (任务类型)
    - ModelCondition (模型名称)
    - ToolRequirementCondition (工具需求)
    - CompositeCondition (组合条件)
    - OrCondition (或条件)

- ✅ `backend/internal/llm/tokenizer.go` - Token 计数器
  - 多策略支持：OpenAI、Anthropic、Simple
  - 消息计算
  - Token 限制验证
  - 自动截断
  - messages 转换支持

- ✅ `backend/internal/llm/token_budget.go` - Token 预算管理
  - TokenEstimator 接口
  - DefaultEstimator 实现
  - AllocationStrategy: Truncate, Summarize, Prioritize, Window
  - AllocationResult: 分配结果和元数据
  - TokenBudgetManager: 预算管理核心

- ✅ `backend/internal/llm/mock_provider.go` - 模拟提供者
  - 用于测试和演示
  - 模拟流式响应
  - 预设响应

### 2. Agent ReAct Loop
- ✅ `backend/internal/agent/loop.go` - ReAct 循环 (新文件, 349 行)
  - Think - LLM 推理阶段
  - Act - 工具调用阶段
  - Observe - 结果观察阶段
  - 迭代控制和终止条件
  - getAvailableTools() - 动态获取工具列表
  - updateHistory() - 对话历史更新
  - Run() - 完整 ReAct 循环执行

- ✅ `backend/internal/agent/agent.go` - Agent 更新
  - 集成 LLM Runtime
  - 添加 `NewAgentWithLLM()` 工厂方法
  - Duration 指针修复

- ✅ `backend/internal/agent/planner.go` - Planner 增强
  - NewPlannerWithLLM() - 支持 LLM Runtime 的规划器
  - CreatePlanWithLLM() - 使用 LLM 生成计划
  - buildPlanningPrompt() - 构建计划提示词
  - parseLLMResponseToPlan() - 解析 LLM 响应为计划
  - extractJSONFromResponse() - 从响应提取 JSON
  - buildToolDescriptions() - 构建工具描述
  - SetLLMRuntime(), GetLLMRuntime() - LLM Runtime 访问

### 3. Skill Executor LLM 集成
- ✅ `backend/internal/skill/executor.go` - LLM 集成增强
  - 添加 LLM Runtime 引用
  - 实现 executeDefault() 使用 LLM 生成响应
  - 构建完整消息列表（系统提示、历史、用户消息）
  - 构建工具定义列表
  - buildToolDefinitions() - MCP 工具转换为 LLM 工具定义
  - SetLLMRuntime() - 动态设置 LLM Runtime

### 4. 类型系统增强
- ✅ `backend/internal/types/common.go` - NewDuration() 工厂函数

---

### ✅ 已完成 (Sprint 2 - 会话和增强功能)

#### 5. Chat/Session 层 (新目录, 4 个文件, 1651 行)
- ✅ `backend/internal/chat/session.go` - 会话结构 (250+ 行)
  - Session 结构：ID, UserID, History, Metadata, Tags, TTL
  - NewSession() - 创建会话
  - AddMessage() - 添加消息
  - GetMessages() - 获取消息列表
  - ClearHistory() - 清空历史
  - IsExpired() - 检查过期
  - UpdateTTL() - 更新 TTL
  - RandomString() - 使用 crypto/rand 生成 ID

- ✅ `backend/internal/chat/storage.go` - 存储接口 (350+ 行)
  - Storage 接口定义
  - SessionNotFoundError
  - InMemoryStorage 实现
  - Get, Set, Delete, Exists 方法
  - ListByUser, ListAll 查询方法
  - SearchContext, SearchTags 搜索方法
  - Clear() - 清空存储

- ✅ `backend/internal/chat/manager.go` - 会话管理器 (350+ 行)
  - SessionManager 结构
  - NewSessionManager() - 创建管理器
  - CreateSession() - 创建会话（带 ID 生成）
  - GetSession, ExistsSession
  - AddMessage()
  - AddTags, RemoveTag, UpdateContext
  - GetActiveSessions, GetByUser
  - CleanupExpired - TTL 自动清理
  - SearchContext, SearchTags

- ✅ `backend/internal/chat/session_test.go` - 完整测试 (13 个测试全部通过)

#### 6. Embedding Router 增强 (新文件, 270+ 行)
- ✅ `backend/internal/embedding/openai_provider.go` - OpenAI Embedding 提供者
  - Generate() - 生成单个文本向量
  - GenerateBatch() - 批量生成向量
  - GenerateWithContext() - 支持取消上下文的向量生成
  - CheckHealth() - 健康检查
  - Config-based 初始化

- ✅ `backend/internal/embedding/index_test.go` - 测试修复
  - 修复 TestEmbedding_Normalize：使用正确的向量维度（768）
  - 20+ 嵌入测试全部通过

---

### ✅ 已完成 (Sprint 3 - 性能和可观测性)

#### 7. Observability - 完整观测性系统 (新目录, 4 个文件, 1502 行)
- ✅ `backend/internal/observability/metrics.go` - 指标系统 (~400 行)
  - Counter - 计数器指标（Increment）
  - Gauge - 仪表盘指标（Set, Get, Inc, Dec）
  - Histogram - 直方图指标（Record）
  - Registry - 指标注册中心
  - 全局辅助函数：IncrementCounter, RecordDuration, SetGauge, IncGauge, DecGauge

- ✅ `backend/internal/observability/tracing.go` - 追踪系统 (~350 行)
  - Span - 分布式追踪基本单位
  - Trace - 追踪链管理
  - DefaultTracer - 默认追踪器
  - TraceContext - 通过 context 传播追踪 ID
  - InstrumentedSpan - 自动计时的 span

- ✅ `backend/internal/observability/logging.go` - 日志系统 (~350 行)
  - JSONWriter - JSON 格式日志写入器
  - TextWriter - 文本格式日志写入器
  - Logger - 结构化日志记录器
    - Level 过滤
    - 支持多 Writer
  - 全局辅助函数：Debug, Info, Warn, Error

- ✅ `backend/internal/observability/observability_test.go` - 测试 (~140 行)
  - 12 个测试全部通过
  - 覆盖 Counter, Gauge, Histogram, Registry
  - 覆盖 Tracer, Span, Timing, TraceContext

#### 8. 编译错误修复与代码重构
- ✅ Tokenizer 命名冲突修复
  - `token_budget.go`: Tokenizer 接口 → TokenEstimator
  - DefaultTokenizer → DefaultEstimator
  - 更新所有引用和测试

- ✅ Mock Provider 修复
  - 添加 types 导入
  - 修复 strings.Builder.String() 调用
  - 使用 errors.New 代替 fmt.Errorf 变量

- ✅ LLM Runtime 修复
  - 修复 CountMessagesTokens 类型转换（types.Message → interface{}）
  - 添加对 types.Message 的完整支持

- ✅ Agent ReAct Loop 修复
  - 移除未使用的 skill 导入（解决循环依赖）
  - 修复 Duration 指针解引用
  - AgentAction 定义为本地类型

- ✅ aicli Protocol Adapter 迁移
  - cmd/aicli/protocol 已移除，统一 internal/runtime/llm/adapter
  - 更新 chat.go, message.go, pipe.go 引用
  - 解决导入循环问题

- ✅ Transformer Parser 优化
  - 添加 protocolParserProvider 接口
  - 优先使用 baseTransformer parser
  - 避免不同转换器共享协议名的类型冲突

- ✅ API Handler 修复
  - 添加 mcpManager 字段和参数
  - 修复 NewAgent 调用（使用 agent.Config）
  - 移除不存在的 MarshalYAML 调用

---

## 下一步计划（建议按优先级）

### P2 - 系统功能（高优先级）

#### 9. 并行执行器 [4-5 小时] ✅ 完成
**文件**: `internal/runtime/executor/parallel_executor.go` (新增, 312 行)
**测试文件**: `internal/runtime/executor/parallel_executor_test.go` (新增, 336 行)

**任务**:
- [x] 创建 `ParallelExecutor` 结构
- [x] 实现并发控制（信号量）
- [x] 实现依赖解析和 DAG 执行
- [x] 实现错误处理和重试
- [x] 集成到 Skill Executor

**实现要点**:
```go
type ParallelExecutor struct {
    maxConcurrency int
    sem            chan struct{}
}

func (e *ParallelExecutor) ExecuteParallel(ctx context.Context, dag *DAG) ([]*types.Observation, error)
```

**测试结果**: 6/6 测试通过 (1.566s)
- TestNewParallelExecutor
- TestExecuteParallel_Simple
- TestExecuteParallel_WithDeps
- TestExecuteParallel_ContextCancellation
- TestExecuteParallel_EmptyDAG
- TestBuildDAG (valid_DAG, cyclic_dependency)

**实现要点**:
```go
type ParallelExecutor struct {
    maxConcurrency int
    sem            chan struct{}
    dagBuilder     *skill.DAGBuilder
}

func (e *ParallelExecutor) ExecuteParallel(ctx context.Context, steps []PlanStep) ([]*types.Observation, error)
```

#### 10. Gateway Client 集成 [4-5 小时] ✅ 完成
**文件**: `internal/runtime/llm/gateway_client.go` (新增, 573 行)

**任务**:
- [x] 创建 `GatewayClient` 实现 LLMProvider
- [x] 集成 gateway/loadbalancer
- [x] 实现请求转换（LLMRequest → Gateway Request）
- [x] 实现响应转换（Gateway Response → LLMResponse）
- [x] 支持重试和错误处理
- [x] 注册到 LLM Runtime

**核心实现**:
- GatewayClient 结构集成 loadbalancer.ResourceManager
- Call() 方法：完整请求流程（选择资源→构建请求→执行→转换响应）
- Stream() 方法：流式响应支持
- Token 计数：自动统计输入/输出 Token
- 重试机制：支持自动重试和失败资源标记

---

### P2/P3 - 优化与扩展（中低优先级）

#### 11. 热加载 [3-4 小时] ✅ 完成
**文件**: `internal/runtime/skill/hot_reload.go` (新增, 486 行)

**任务**:
- [x] 集成 fsnotify
- [x] 实现文件监听
- [x] 实现防抖处理
- [x] 实现增量加载
- [x] 提供事件回调

**核心实现**:
- HotReload 结构：完整文件监听系统
- fsnotify 集成：递归目录监听
- 防抖机制：可配置防抖时间（100ms-5s）
- 4 种事件类型：Create, Modify, Delete, Rename
- ReloadCallback：事件回调接口
- 线程安全：完整的互斥锁保护

#### 12. 测试覆盖补充 [12-16 小时]
**任务**:
- [x] 补充 Agent ReAct Loop 集成测试
- [x] 补充 Planner LLM 集成测试
- [x] 添加 Chat/Session 集成测试
- [x] 添加 Observability 集成测试
- [x] 添加端到端测试
**状态**: ✅ 集成测试文件已补齐，且 runtime 全量测试已通过 (2026-03-08)
**新增测试文件 (Sprint 5)**:
- ✅ `internal/runtime/agent/loop_test.go` - ReAct Loop 测试 (2.2 KB, 8 个测试用例)
  - TestNewReActLoop, TestNewReActLoop_WithNilConfig
  - TestReActLoop_Run_WithoutAgent, TestReActLoop_Run_WithoutLLMRuntime
  - TestReActLoop_Run_BasicExecution, TestReActLoop_Run_WithMaxSteps
  - TestReActLoop_Run_WithTimeout, TestAgent_IsRunning_AfterLoop
  - MockLLMProvider 实现 LLMProvider 接口
  - 测试结果: 8/8 通过 (0.057s)

- ✅ `internal/runtime/agent/planner_test.go` - Planner 测试 (7.2 KB, 9 个测试用例)
  - TestNewPlanner, TestNewPlannerWithLLM
  - TestPlanner_CreatePlanWithLLM_InvalidGoal, TestPlanner_CreatePlanWithLLM_NoLLMRuntime, TestPlanner_CreatePlanWithLLM_BasicExecution
  - TestPlanner_buildToolDescriptions, TestPlanner_buildPlanningPrompt
  - TestPlanner_parseLLMResponseToPlan_EmptyContent, TestPlanner_parseLLMResponseToPlan_InvalidJSON
  - TestPlanner_SetLLMRuntime, TestPlanner_GetLLMRuntime
  - 测试结果: 9/9 通过 (与 loop_test 合计 15/15 通过, 1.380s)

- ✅ `internal/runtime/chat/integration_test.go` - Chat/Session 集成测试 (9 个测试用例)
  - 涵盖会话持久化、TTL、多用户、History、MCP 集成、并发操作
  - 当前验证结果：`go test ./internal/runtime/chat` 通过

- ✅ `internal/runtime/observability/integration_test.go` - Observability 集成测试 (12 个测试用例)
  - 涵盖 LLM、Agent、Skill、Session、Embedding、Router、Logging、Token Metrics
  - 当前验证结果：`go test ./internal/runtime/observability` 通过

- ✅ `internal/runtime/e2e/e2e_integration_test.go` - 端到端测试 (5 个测试用例)
  - 涵盖多轮对话、流式响应、重试、多 Provider
  - 当前验证结果：`go test ./internal/runtime/e2e` 通过

- ✅ `internal/runtime/llm` - Token budget 测试已通过
- ✅ `internal/runtime/errors` - `WithContext()` 语义已与测试对齐

#### 13. 文档编写 [8-12 小时]
**任务**:
- [ ] API 文档（LLM Runtime, Agent, Skill）
- [ ] 使用指南和示例
- [ ] 部署文档
- [ ] 开发指南（贡献者）

---

## 代码统计

| 模块 | 文件数 | 代码行数 | 状态 |
|------|--------|---------|------|
| LLM Runtime 核心 | 6 | ~1500 | ✅ 完成 |
| Agent ReAct Loop | 4 | ~1150 | ✅ 完成 (Sprint 5新增2测) |
| Skill Executor 集成 | 1 | ~110 | ✅ 完成 |
| Chat/Session 层 | 4 | ~1650 | ✅ 完成 |
| Embedding Router | 1 | ~270 | ✅ 完成 |
| Observability | 4 | ~1500 | ✅ 完成 |
| Planner LLM 集成 | 1 | ~430 | ✅ 完成 |
| 并行执行器 | 2 | ~650 | ✅ 完成 (Sprint 4) |
| Gateway Client | 1 | ~575 | ✅ 完成 (Sprint 4) |
| 热加载 | 1 | ~490 | ✅ 完成 (Sprint 4) |
| 修复与优化 | 8+ | ~2000 | ✅ 完成 |
| 文档 | 2 | ~1400 | ✅ 完成 |

**总计**: 35+ 个文件，约 11725 行代码 (Sprint 4 新增 3 模块 1715 行，Sprint 5 新增 2 测试 520 行)

---

## 架构关系图（更新）

```
┌─────────────────────────────────────────────────────────────────────┐
│                         AI Gateway Runtime                           │
├─────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  ┌─────────────────────┐    ┌─────────────────────┐                │
│  │   LLM Runtime       │────│   Agent System       │                │
│  │   (runtime.go)      │    │   (agent/loop.go)   │                │
│  └──────────┬──────────┘    └──────────────┬────────┘                │
│             │                              │                       │
│  ┌──────────▼──────────┐    ┌──────────▼────────┐              │
│  │  Model Router       │    │  Skill Executor    │              │
│  │  (router.go)        │    │  (executor.go)    │              │
│  └──────────┬──────────┘    └──────────┬────────┘              │
│             │                              │                        │
│  ┌──────────▼──────────────────────────▼────────┐              │
│  │          Providers                          │              │
│  │  ┌─────────┐ ┌─────────┐ ┌──────────┐    │              │
│  │  │  Mock   │ │ OpenAI   │ │Anthropic │    │              │
│  │  │ (test)  │ │(future) │ │(future)  │    │              │
│  │  └─────────┘ └─────────┘ └──────────┘    │              │
│  └─────────────────────────────────────────────┘              │
│                                                                      │
│  ┌─────────────────────┐    ┌─────────────────────┐                │
│  │  Chat/Session       │    │   Observability      │                │
│  │  (chat/)            │    │   (observability/)   │                │
│  │  SessionManager     │    │   Metrics/Tracing    │                │
│  └─────────────────────┘    │   Logging            │                │
│                             └─────────────────────┘                │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 测试覆盖

- ✅ 通过：`internal/runtime/agent`
- ✅ 通过：`internal/runtime/chat`
- ✅ 通过：`internal/runtime/observability`
- ✅ 通过：`internal/runtime/e2e`
- ✅ 通过：`internal/runtime/executor`
- ✅ 通过：`internal/runtime/embedding`
- ✅ 通过：`internal/runtime/llm`
- ✅ 通过：`internal/runtime/errors`
- ✅ 编译通过：`internal/api/skills`

**2026-03-08 验证摘要**:
- `go test ./internal/runtime/errors ./internal/runtime/llm ./internal/api/skills`：通过
- `go test ./internal/runtime/... ./internal/api/...`：通过

---

## 已解决的问题

1. ✅ 缺少 LLM 统一调用接口
2. ✅ 缺少 Agent ReAct Loop
3. ✅ Skill 无法调用 LLM 生成响应
4. ✅ 缺少模型路由机制
5. ✅ 缺少 Token 预算管理
6. ✅ Tokenizer 命名冲突
7. ✅ Protocol Adapter 导入循环
8. ✅ Transformer 协议解析器冲突
9. ✅ API Handler 编译错误
10. ✅ 缺少会话管理层
11. ✅ Embedding Router 不完整
12. ✅ 缺少观测性系统

---

## 已知限制
1. ⚠️ 当前仍以 Mock Provider 为主要测试场景，但 adapter-based 真实 Provider 已可通过 bootstrap / gateway 主链路接入
3. ⚠️ Skill Executor 仅完成基础 LLM 调用，Prompt 模板、Token Budget、工具结果格式化尚未闭环
4. ✅ 历史阶段中 `GatewayClient` 与 `HotReload` 曾接入 `internal/gateway/router/GinEngine`；当前 standalone runtime 的真实入口请看 `backend/cmd/runtime-server/main.go` 与 `backend/internal/api/skills/handler.go`。`ParallelExecutor` 已接入 workflow 主执行路径
5. ⚠️ Observability 目前是自研实现，尚未看到 Prometheus / OpenTelemetry 的正式接入
6. ⚠️ `internal/api/skills` 已具备基础执行、Session/History 与 SSE 能力，但批量会话管理与更完整的 Agent 编排仍未收口

---

## 技术债务
1. 需要补齐基于真实 OpenAI/Anthropic 凭证的联调验证，而不仅是本地 `httptest` 场景
2. 需要继续补齐真实 Provider / MCP 集成验证，尤其是真实 MCP 与上游模型联调场景
4. 需要补齐基于真实 Provider 的端到端验证，而不仅是 Mock Provider 场景
5. 需要持续同步文档状态，避免“代码已实现但文档仍显示未完成”

---

## 使用方法

### 快速开始

```go
import (
    "github.com/wwsheng009/ai-agent-runtime/internal/llm"
    "github.com/wwsheng009/ai-agent-runtime/internal/agent"
    "github.com/wwsheng009/ai-agent-runtime/internal/types"
    "github.com/wwsheng009/ai-agent-runtime/internal/chat"
    "github.com/wwsheng009/ai-agent-runtime/internal/observability"
)

// 1. 初始化观测性
observability.SetGlobalLogger(observability.NewLogger(
    observability.NewTextWriter(os.Stdout),
    "info",
))

// 2. 创建 LLM Runtime
config := &llm.RuntimeConfig{
    DefaultModel: "gpt-4-turbo",
    MaxRetries:    3,
}
runtime := llm.NewLLMRuntime(config)

// 3. 注册 Provider
providers := llm.MockProvidersFactory()
for _, provider := range providers {
    runtime.RegisterProvider(provider.Name, provider)
}

// 4. 创建 Session Manager
sessionManager := chat.NewSessionManager(
    chat.NewInMemoryStorage(),
    1*time.Hour, // TTL
)

// 5. 创建 Agent（带 LLM Runtime 和 Session）
agentConfig := &agent.Config{
    Name:         "my-agent",
    Model:        "gpt-4-turbo",
    MaxSteps:     10,
    SystemPrompt: "You are a helpful assistant.",
}
agent := agent.NewAgentWithLLM(agentConfig, mcpManager, runtime)

// 6. 创建会话
session, _ := sessionManager.CreateSession(ctx, "user123")

// 7. 添加用户消息到会话
session.AddMessage(*types.NewUserMessage("Help me write code"))

// 8. 使用 ReAct Loop 运行 Agent
result, err := agent.Run(ctx, "Help me write code")

// 9. 记录观察结果到会话
for _, obs := range result.Observations {
    // 添加到会话
}
```

---

## 2026-03-09 最新进展

- ✅ `skills runtime` 已支持从 Bearer JWT claims 解析 `tenant / project / user` scope
- ✅ scope 来源优先级更新为：`body > query > header/gin-context > JWT claims > api_key_scopes > default`
- ✅ 网关已把 `auth.jwt_secret` 和 `skills_runtime.jwt_*_claims` 接入 `skills` handler
- ✅ 已补 handler / gateway 回归测试，验证 JWT scope 绑定生效
- ✅ skills 管理面已支持 `admin_token` 之外的管理员角色授权（header / gin context / JWT claims）
- ✅ 网关现在会把 gin context claims map 透传到 `skills`，无需在 `skills` 侧重复解析已认证上下文
- ✅ `GET /api/skills/auth/policy` 已提供生效中的 auth/scope resolver 可见性，Go client 已同步支持
- ✅ `auth/scope policy` 运行期更新已同步回 `configManager` 当前快照，并通过 reload callback 回灌到 `skills` handler
- ✅ 如果 `configManager` 绑定了配置文件，`auth/scope policy` 运行期更新现在会定点写回 YAML 文件中的 `skills_runtime` auth 字段
- ✅ `usage/quota policy` 运行期更新也已支持同步回 `configManager` 并定点写回 YAML 文件中的 `skills_runtime` usage 字段
- ✅ `mutation policy` 运行期更新也已打通到 handler / gateway / configManager / YAML 文件持久化
- ✅ governance 三件套（mutation / usage / auth）现在都具备：runtime update、回滚保护、configManager 同步、YAML 定点写回、Go client 支持
- ✅ 已新增统一治理视图：当前 runtime 主入口为 `GET /api/skills/governance/policy`；历史上 gateway 也曾暴露 `GET /monitor/skills-governance`
- ✅ 已补治理指南：`docs/skill_runtime/governance_api_guide.md`
- ✅ `aicli chat` 已从“全量暴露所有 skills”切换为“route-first + top-K skills exposure”，减少 `tools` token 开销
- ✅ `aicli chat` 的 skills 暴露数量现已可配置：`skills_runtime.aicli_skill_exposure_top_k`，并支持 `--skills-top-k` 覆盖
- ✅ 已完成 `docs/skill_runtime/design` 全量设计审查，并确认当前项目应按“通用 skill / agent 平台”而不是“coding-agent-only”解释
- ✅ 已识别并修复 `aicli` 的一项关键设计偏差：新增 `aicli_skill_exposure_mode` / `--skills-mode`
- ✅ `skills-mode=prefer` 在存在 route 命中的 skill 候选时，会抑制内置 tools 暴露；`skills-mode=only` 仅暴露候选 skills
- ✅ 该修复使 `aicli chat` 更接近 `Intent Router -> candidate skills -> LLM tool selection -> Skill Executor` 的目标路径
- ✅ 2026-03-10 已基于 `nvidia / z-ai/glm4.7` 实测 `aicli chat --skills-mode prefer`，确认 shell 场景会优先命中 `skill__run_shell_command`，而不是直接走内置 `bash`
- ✅ 已补充 `docs/skill_runtime/aicli_skills_usage.md`，固定 `skills-top-k`、`skills-mode`、prompt 写法与 `aicli chat` 集成用法
- ✅ `aicli chat` 已新增 `--skills-debug`，可直接输出当前请求的 route 候选、score、matched_by 与 exposed skills，便于调试 prompt/skill 选择
- ✅ 已新增 `backend/internal/capability`，开始统一 `skill / tool / workflow / agent` 的能力描述与路由候选模型
- ✅ `agent` 已新增统一编排入口 `Orchestrate()`，并已接入 `backend/internal/api/skills` 的非流式 `AgentChat` 主链路
- ✅ `backend/internal/workspace` 已补齐 `SymbolIndex`、`ReferenceGraph` 与 `ContextBuilder`，workspace retrieval 不再只有 scanner
- ✅ `AgentChat` 已支持可选 `workspace_path`，会在请求入口构建 workspace context 并注入统一 orchestrator
- ✅ 已新增可选 `planner_preferred` 编排模式：`AgentChat` 非流式请求可通过 `planning_mode=planner_preferred` 触发计划构建，并在结果中返回 `planning` 摘要
- ✅ `planner_preferred` 已扩展到 streaming 路径：SSE 现在会输出 `planning` 事件，并在 `meta / result / done` 中携带 planning 摘要
- ✅ `backend/pkg/skillsapi` 请求模型已支持 `workspace_path / planning_mode`，并已补 client 文档说明 planning 响应与 `planning` SSE 事件
- ✅ 已补统一 runtime status 可见性：`GetStats` 现在聚合返回 `runtime`，且当前入口为 `GET /api/runtime/status` 与 `backend/pkg/skillsapi.GetRuntimeStatus()`
- ✅ 已补 `GET /api/runtime/health` 与 `backend/pkg/skillsapi.GetRuntimeHealth()`，提供 provider / MCP 的健康摘要、问题列表与连接计数
- ✅ 已补 `POST /api/runtime/mcps/reload` 与 `backend/pkg/skillsapi.ReloadRuntimeMCPs()`，支持按当前配置重新加载并重连 MCP runtime
- ✅ 已补 `GET /api/runtime/validate` 与 `backend/pkg/skillsapi.ValidateRuntime()`，提供 runtime 配置健康校验与 warnings/issues 摘要
- ✅ runtime health 现支持 `recheck` 触发复查，并标注 provider `degraded / unhealthy` 状态与最近检查时间
- ✅ runtime validate 已补充 config file / embedding / hot-reload / workspace 维度的健康检查
- ✅ 历史：曾新增 gateway 监控摘要 `GET /monitor/runtime-health`，统一输出 runtime health + validation 状态；当前 runtime 侧请改看 `GET /api/runtime/status` 与 `GET /api/runtime/health`
- ✅ MCP manager 已支持健康检查定时器与自动重连：health check 失败会尝试重建连接并刷新 tools
- ✅ MCP health probe 支持按 tool/resource 维度检查，失败的工具会被自动标记为不可用
- ✅ runtime config 已引入版本号与 rollout 配置，并在 update/load 时记录版本历史
- ✅ capability abstraction 已接入 runtime API：当前入口为 `GET /api/runtime/capabilities`，并在 orchestration 中输出 `capability_candidates`
- ✅ AgentChat 已支持统一 context pack（workspace + session），并可通过默认 planning mode 配置控制 planner-first
- ✅ runtime rollout 已支持 canary/progressive 策略，并按 scopeKey 选择 candidate config
- ✅ 新增 skills API E2E matrix 回归，覆盖 provider / session / search / agent 路径
- ✅ 计划清单中的平台主线已收口：skills API / lifecycle / rollout / doc sync 均标记完成
- ✅ 当前决策：`sandbox / patch / git / multi-agent` 继续保留在 `coding pack` 路线中，不进入当前平台主链，先等待 capability / workspace / orchestration 稳定

---

## 提交历史（最近）

- ✅ docs(skill_runtime): 添加实现计划和进度跟踪文档
- ✅ refactor(config): 添加 OpenAI Codex 提供者配置，更新 Provider Groups
- ✅ fix(transformer): 修复协议解析器冲突，优先使用当前转换器自带的 Parser
- ✅ refactor(aicli): 迁移 Protocol Adapter 到 runtime/llm/adapter
- ✅ fix(api/skills): 修复 Handler 编译错误，正确集成 Agent 和 MCP Manager
- ✅ feat(runtime/skill): 增强 Skill Executor，集成 LLM Runtime 执行
- ✅ feat(runtime/agent): 实现 ReAct 循环，完善 Planner 和 Executor LLM 集成
- ✅ feat(runtime/observability): 实现完整的观测性系统
- ✅ feat(runtime/embedding): 实现 OpenAI Embedding 提供者，修复测试
- ✅ feat(runtime/chat): 实现 Chat/Session 会话管理层
- ✅ fix(runtime): 修复类型冲突和编译错误，完成 LLM Runtime 核心实现

---

## 贡献者

- AI Assistant (Crush)

---

**更新时间**: 2026-03-10  
**版本**: v0.2.0-alpha  
**当前结论**: 核心骨架基本完成，通用 `skill / agent` 平台主链路可用，当前重点转向平台治理与高阶编排收口
