# Skills Runtime 实施待办事项

> 基于对 `skills_runtime_design.md` 的审查，生成的实施计划
> 
> **生成时间**: 2025-03-07
> 
> **总体进度**: 原始计划已被部分实现；按 2026-03-08 仓库审计，当前主要工作已转向测试修复、API 收口与主链路集成

> **审计说明（2026-03-08）**:
> - 本文档中的复选框主要反映 2025-03-07 的原始计划，不完全代表当前仓库状态。
> - 当前代码库已经具备 LLM Runtime、Model Router、ReAct Loop、Skill Executor LLM、Chat/Session、Embedding Router、Observability、ParallelExecutor、GatewayClient、HotReload 等核心骨架。
> - 当前阻塞已从“核心能力缺失”转为“测试不全绿、API 编译链未收口、模块尚未完全接入主路径”。
> - 自 2026-03-09 起，路线图按“通用 `skill / agent` 平台”重新基线；coding-specific 高阶能力不再作为平台阻塞项。
> - 文中旧 `internal/runtime/*`、`internal/api/skills/*` 路径，如需映射到当前仓库，请按 `E:\projects\ai\ai-agent-runtime\backend\internal\...` 理解。
> - 文中 `/monitor/*`、`/metrics`、`/api/skills/runtime/*` 多为 gateway-era 或旧 runtime 观测面；当前主观测入口请改看 `/api/runtime/status`、`/api/runtime/health`、`/api/runtime/traces*`、`/api/runtime/skills/search/stats`、`/api/runtime/usage/*`。
> - 文中所有 `go test ./...` / `go run ./cmd/...` 命令，如需今天执行，请在 `E:\projects\ai\ai-agent-runtime\backend` 目录运行。

---

## 优先级分类

### 🔴 P0 - 阻塞性任务（必须完成）
这些任务如果不完成，系统无法真正运行。

### 🟡 P1 - 高优先级
这些任务影响核心功能和用户体验。

### 🟢 P2 - 中优先级
这些任务增强功能和性能。

### ⚪ P3 - 低优先级
这些任务改善开发体验和维护性。

---

## 当前优先行动项（以 2026-03-08 审计为准）

- [x] 修复 `internal/runtime/llm/token_budget.go` 与测试契约不一致的问题，并消除 `Allocate_Prioritize` panic
- [x] 修复 `internal/runtime/errors/runtime_error.go` 中 `WithContext()` 的可变语义问题
- [x] 修复 `backend/internal/api/skills/handler.go` 的编译链路（路由依赖 + `Agent.Execute` 调用）
- [x] 为 `backend/internal/api/skills` 接入基础可执行链（`ExecuteSkill` → `skill.Executor`，`AgentChat` → Session/LLM 基础支持）
- [x] 为 `backend/internal/api/skills` 补充基础 Session/History 管理接口，并修复固定路径路由优先级问题
- [x] 将 `ParallelExecutor` 接入 Skill Workflow 主执行路径，并补充并行执行测试
- [x] 为 `AgentChat` 增加基础 SSE 流式响应，并在流结束后保持 Session 落库
- [x] 为 `GatewayClient` 补齐 `LLMRuntime.RegisterGatewayClient()` 挂点与基础测试
- [x] 为 `HotReload` 增加高层 API 控制入口，并修复 reload/index 一致性问题
- [x] 新增统一 `runtime/bootstrap` 组装层，统一接线 `GatewayClient`、`HotReload`、`SessionManager`、`LLMRuntime`、`Registry`
- [x] 将新的 `runtime/bootstrap` 组装层接入更上层真实应用入口并补充集成验证
- [x] 统一 `llm.Provider` 与 `llm.LLMProvider` 两套抽象，减少运行时接线复杂度
- [x] 历史：曾将真实 Provider 配置接入 `runtime/bootstrap` 与 `internal/gateway/router/GinEngine`；当前独立入口请改看 `backend/cmd/runtime-server/main.go`
- [x] 增加基于 `backend/configs/config.yaml` + `configs/.env` 的 live provider smoke test，并完成一次真实 NVIDIA provider 联调验证
- [x] 增加基于 `bin/toolkit-mcp.exe` 的本地 MCP smoke test，并完成一次 `skills ExecuteSkill -> MCP bash tool` 整链路验证
- [x] 继续扩展 `backend/internal/api/skills`（批量会话管理、更完整的 Agent 编排、更丰富的流式事件）
- [x] Add a remote WebSocket MCP smoke test using `internal/mcp/server/echo`, and verify the `skills ExecuteSkill -> WebSocket MCP echo tool` end-to-end path
- [x] Add a manager-level MCP connectivity smoke test in `internal/mcp/manager/manager_live_test.go`, and verify remote WebSocket MCP connectivity before skills API E2E
- [x] Add ABAP ADT MCP live smoke tests for the provided `ecc1809` stdio config, including manager-level `login` validation and skills API `help` tool E2E
- [x] ?? `internal/api/skills` ?? API?????????????????????/??
- [x] 增强 `AgentChat` 结果结构与 SSE 事件：统一 `kind/source`，新增 `result` 事件和 `reasoning/tool_*` 专属事件
- [x] 为 `AgentChat` 增加统一编排摘要（route/fallback/observation summary），并在 SSE 中新增 `orchestration` 事件
- [x] 在 `AgentChat` 编排摘要中加入 route candidates（score / matched_by / details），并覆盖 route-match / no-match 测试
- [x] 增强 `AgentChat` observation 摘要（failed_details / duration metrics）并为 route candidates 增加 `chosen` / `selection_reason`
- [x] 持续同步计划文档与实际代码状态，避免“代码已存在但文档仍显示未完成”
- [x] 为 `AgentChat` SSE 增加稳定 envelope（`_event.name/schema_version/timestamp/sequence`），提升非文本 consumer 可消费性
- [x] 将 `reasoning/tool_*` 流式事件 payload 固定成显式 schema，补齐非文本 consumer 的稳定字段约定
- [x] 为 `agent_route` 静态流补充 `route` / `observation` 事件，统一 agent-route 场景的事件可见性
- [x] 补充 `skills API` 正式流式协议文档，固定 SSE 事件名、envelope 与 payload 字段约定
- [x] 补充 `skills API` 非流式结果协议文档，固定 `result.kind/source/orchestration` 的 JSON contract
- [x] 补充通用 `skill / agent` 平台定位文档，明确 coding agent 只是垂直能力包而非唯一目标
- [x] 去除默认外部 embedding API 依赖：改为本地 embedding 生成器，并将 `embedding router` 接入 `bootstrap / AgentChat / HotReload` 主链路
- [x] 增强 `SearchSkills` 与 `GetStats`：支持 `auto/lexical/semantic/hybrid` 搜索模式，并暴露 embedding 索引统计
- [x] 增加搜索观测与索引运维接口：`GET /search/stats`、`POST /search/reindex`，统一返回搜索 telemetry 与最近重建状态
- [x] 为搜索运维接口增加轻量访问控制与节流：默认仅 loopback，可选 `admin_token` 放开远程访问；`reindex` 默认 `30s` 冷却
- [x] 为搜索运维接口补充结构化审计日志，记录 `action/outcome/access_mode/remote_ip/request_id/search_summary`
- [x] 将搜索运维事件接入 runtime metrics，新增 `search_admin_actions_total` 与 `search_reindex_runs_total`
- [x] 历史：曾将 `search_*` 指标桥接到 gateway 监控 API，可通过 `/monitor/search?prefix=search_` 与 `/monitor/metrics` 查看
- [x] 将 `search_*` 指标接入 Prometheus 抓取端点 `/metrics`
- [x] 历史：曾增加 gateway 聚合监控摘要接口 `GET /monitor/search-admin`
- [x] 补充搜索监控与运维指南，包含 monitor API、Prometheus、PromQL 与运维检查项
- [x] 补充搜索故障排查 playbook，覆盖 forbidden / rate_limited / reindex failed / semantic hit-rate degradation

---

## 2026-03-09 重新基线后的优先行动项

### 🔴 P1 - 平台主线

- [x] 统一 `skill / tool / workflow / agent` 的 capability 抽象、注册元数据与调用结果模型
  - [x] `skills API` 增加 capability 列表输出与 orchestration 的 `capability_candidates`
- [x] 让 skill 可被 AI 直接调用：已将 skills 以 AI-callable function/tool 的方式接入 `aicli chat`
- [x] 补一个最小可用 skill pack，并完成 `nvidia` provider 下的 `aicli chat -> skill function` live 验证
- [x] 为 workflow skill 增加动态参数模板渲染，支持把 `prompt/context/results` 注入 MCP tool 参数
- [x] 补首批通用 workflow skills（shell / fetch / file view），并完成 `run_shell_command` live 验证
- [x] 支持系统级 skill 目录与外部 skill 目录分层加载，并为 `aicli chat` 增加 `--skills-dir` 指定目录入口
- [x] 完成外部目录 `skills-dir` live 验证，确认业务域 skill 可脱离仓库内置系统 skill 目录独立加载
- [x] 将多目录模型收口到网关 runtime / hot reload / skills API，避免初始化后又退回单目录行为
- [x] 为每个 skill 暴露来源元数据（`path / dir / layer`），让系统级 / 外部 / runtime skill 的来源可见
- [x] 为多目录加载建立默认来源策略：同名 skill 按目录优先级决议，并为 `List/Search/Stats` 增加 `source_layer/source_dir` 过滤
- [x] 为 `skills API` 增加 runtime skill 持久化策略与真实 reload 行为，默认仅允许落到外部 skill 目录
- [x] 补正式来源/持久化文档，并将 `ReloadSkills` 的目录输入模式与 `hot-reload/start` 对齐
- [x] 将 `admin_token` 从搜索运维扩展为统一 skills 管理令牌，保护所有会修改 registry 或磁盘的 skills 写接口
- [x] 收口 external skill 的文件策略：已持久化 external skill 默认更新回写原文件，删除时支持显式 `delete_file=true`
- [x] 历史：曾将 skills 变更审计接入 runtime metrics / gateway monitor API / Prometheus，新增 `skill_mutation_actions_total` 与 `/monitor/skill-mutations`
- [x] 为 `skills_runtime` 增加轻量治理策略：`read_only / disable_import / disable_persist / disable_reload_ops / disable_hot_reload_ops`
- [x] 为 skills / agent API 补充稳定的 Go SDK / client 接口，新增 `backend/pkg/skillsapi`
- [x] 将 `backend/pkg/skillsapi` 扩展到主管理面与 session 面，覆盖 `search admin / reload / hot-reload / import / export / sessions`
- [x] 补最小 usage / quota 闭环：`usage_tracking_enabled / quota_enabled / default_max_requests / default_max_tokens`
- [x] 历史：曾将 usage / quota 接入 gateway 监控面：`skill_usage_requests_total / skill_usage_tokens_total / skill_quota_denials_total / /monitor/skill-usage`
- [x] 将 usage / quota scope 升级为 `tenant / project / user` 三层 key
- [x] 为 `tenant / project / user` 三层 scope 增加配置型 quota policy override
- [x] 为 runtime usage / quota 增加在线 policy 管理接口与 client
- [x] 为 runtime usage 增加可选持久化 ledger，并补 `GET /api/skills/usage/ledger` 与 client 查询接口
- [x] 为 scope 增加 auth / header / API key 绑定层，支持 gin context 透传与 `api_key_scopes`
- [x] 修复 `aicli chat` 全量暴露 skills 带来的 token 膨胀问题，已改为 route-first + top-K 技能暴露
- [x] 为 `aicli chat` 增加 `skills-mode`：`auto | prefer | only`，在 skill 命中时可抑制内置 tool 暴露
- [x] 新增初版 `capability abstraction`，统一 `skill / workflow / tool / agent` 的描述与 route candidate 模型
- [x] 将轻量 admin 控制扩展为平台级治理能力：`auth / tenant / project / policy / usage / quota`
- [x] 增加 runtime 配置验证、版本化与 rollout 基础能力
  - [x] runtime config 引入版本号 + rollout 配置
  - [x] runtime config 版本历史与回滚接口
  - [x] runtime config rollout 执行策略（canary/progressive）

### 🟡 P2 - 平台增强

- [x] 继续扩展 retrieval / memory 接口，把 `workspace context builder` 接到更高层 orchestrator / API
  - [x] AgentChat 已输出统一 `context_pack`（workspace + session）
- [x] 将 `workspace context builder` 接到更高层主链：`AgentChat` 已支持 `workspace_path` 并将 workspace context 注入统一 orchestrator
- [x] 将 planner-first orchestration 设计成可选执行模式；当前 `AgentChat` 非流式已支持 `planning_mode=planner_preferred`
- [x] 将 planner-first orchestration 扩展到 streaming 路径，SSE 已支持 `planning` 事件与 planning 摘要
- [x] 补更完整的 plan/result contract，并把 planning 摘要同步进正式 API 协议文档
- [x] 统一 provider / MCP 生命周期治理，包括连接状态、失败恢复、配置健康检查（当前已补 runtime status / runtime health / runtime validate 可见性，并支持 MCP runtime reload）
  - [x] provider health recheck + degraded/unhealthy 状态标记
  - [x] runtime config health validation（config file / embedding / workspace / hot-reload）
  - [x] 历史：曾由 gateway 暴露 `/monitor/runtime-health` 统一摘要输出；当前 runtime 侧请改看 `/api/runtime/status` 与 `/api/runtime/health`
  - [x] MCP 健康检查定时器 + 失败后自动重连
  - [x] MCP 更细粒度 probe / failure recovery 策略（按工具或按资源）
- [x] 为 `skills API` 增加更完整的端到端回归矩阵，覆盖 provider / MCP / session / search 组合路径

### 🟢 P3 - 垂直能力包

- [x] 已确认 `sandbox / patch / git / multi-agent` 继续收束到 `coding pack`，不作为当前通用平台主链阻塞项
- [ ] 规划 `coding pack`，把 repo map / code graph / patch / git / sandbox 收束到单独能力包
- [ ] 规划 `ABAP / ERP pack`，把已验证的 ABAP MCP 能力沉淀成稳定 skill 集
- [ ] 规划 `enterprise workflow pack`，面向非 coding 场景沉淀通用流程技能

> 以上 P3 规划项属于垂直能力包，不纳入当前平台主线“绿灯”评估。

### ⚪ 非平台阻塞项（降级为垂直能力）

- [ ] Multi-Agent reviewer / tester / coder pipeline
- [ ] Code Patch Engine
- [ ] repo map / AST-only intelligence
- [ ] coding-specific sandbox
- [ ] git automation

> 以上能力已明确降级为垂直能力包，不作为平台主线阻塞项。

---

## 原始任务清单（2025-03-07，保留供追溯）

> 以下清单保留用于历史追溯，不再作为当前实施状态的打分基准；其勾选状态不代表现有平台“绿灯”/“黄灯”判断。

## P0 任务

### 1. 实现 LLM Runtime 核心模块 [阻塞]
**影响**: Agent 无法调用 LLM，无法真正运行

**文件**: `internal/runtime/llm/runtime.go`

**任务**:
- [ ] 创建 `LLMRuntime` 结构体
- [ ] 实现 `Provider` 注册和管理
- [ ] 实现 `Call()` 统一调用接口
- [ ] 实现 `Stream()` 流式调用接口
- [ ] 集成现有的 `adapter/` 模块
- [ ] 支持多 Provider 切换

**实现要点**:
```go
type LLMRuntime struct {
    providers map[string]LLMProvider
    router    *ModelRouter
    config    *RuntimeConfig
}

func (r *LLMRuntime) Call(ctx context.Context, req *LLMRequest) (*LLMResponse, error)
func (r *LLMRuntime) Stream(ctx context.Context, req *LLMRequest) (<-chan StreamChunk, error)
```

**依赖**: `llm/adapter/*` 已存在

**预计工作量**: 4-6 小时

---

### 2. 实现 ModelRouter [阻塞]
**影响**: 无法根据请求路由到合适的模型

**文件**: `internal/runtime/llm/router.go`

**任务**:
- [ ] 定义 `RoutingRule` 结构
- [ ] 实现规则匹配逻辑
- [ ] 支持基于任务类型路由
- [ ] 支持基于 Token 限制路由
- [ ] 支持基于性能需求路由

**实现要点**:
```go
type ModelRouter struct {
    rules []*RoutingRule
}

func (r *ModelRouter) Route(req *LLMRequest) string
```

**预计工作量**: 2-3 小时

---

### 3. 实现 Agent ReAct Loop [阻塞]
**影响**: Agent 无法进行推理和工具调用循环

**文件**: `internal/runtime/agent/loop.go` (新增)

**任务**:
- [ ] 创建 `ReActLoop` 结构
- [ ] 实现 `Think-Act-Observe` 循环
- [ ] 集成 LLM Runtime 进行推理
- [ ] 集成 Skill Router 和 Executor
- [ ] 支持并发工具调用
- [ ] 实现步骤限制和终止条件

**实现要点**:
```go
func (a *Agent) RunReActLoop(ctx context.Context, prompt string) (*Result, error) {
    history := []Message{NewUserMessage(prompt)}
    
    for step := 0; step < a.config.MaxSteps; step++ {
        // 1. 检查路由匹配
        // 2. LLM 推理
        // 3. 执行工具调用
        // 4. 记录观察
        // 5. 更新历史
    }
    
    return result, nil
}
```

**依赖**: Task 1, Task 4

**预计工作量**: 6-8 小时

---

### 4. 完善 Skill Executor LLM 集成 [阻塞]
**影响**: Skill 无法调用 LLM 生成响应

**文件**: `internal/runtime/skill/executor.go`

**任务**:
- [ ] 修改 `executeDefault()` 集成 LLM Runtime
- [ ] 实现 Prompt 模板渲染
- [ ] 集成 Token 预算管理
- [ ] 支持工具调用结果格式化
- [ ] 添加 Token 统计

**实现要点**:
```go
func (e *Executor) executeDefault(ctx context.Context, skill *Skill, req *types.Request) (*ExecuteResult, error) {
    // 构建 prompt
    systemPrompt := skill.SystemPrompt
    userPrompt := skill.UserPrompt
    
    // 调用 LLM Runtime
    response, err := e.llmRuntime.Call(ctx, &LLMRequest{
        System: systemPrompt,
        Prompt: userPrompt,
        Tools: skill.Tools,
    })
    
    // 返回结果
    return &ExecuteResult{
        Success: err == nil,
        Output: response.Content,
        Usage: response.Usage,
    }, nil
}
```

**依赖**: Task 1

**预计工作量**: 3-4 小时

---

## P1 任务

### 5. 实现 Chat/Session 层
**影响**: 无法支持多轮对话和会话管理

**文件**: 
- `internal/runtime/chat/session.go` (新增)
- `internal/runtime/chat/storage.go` (新增)

**任务**:
- [ ] 创建 `Session` 结构
- [ ] 创建 `SessionManager`
- [ ] 实现 `SessionStorage` 接口
- [ ] 实现 `InMemoryStorage`
- [ ] 实现 `SQLiteStorage` (可选)
- [ ] 实现会话 TTL 管理

**实现要点**:
```go
type Session struct {
    ID        string
    UserID    string
    History   []Message
    State     SessionState
    CreatedAt time.Time
    UpdatedAt time.Time
}

func (m *SessionManager) Create(ctx context.Context, userID string) (*Session, error)
func (m *SessionManager) AddMessage(ctx context.Context, sessionID string, msg Message) error
```

**预计工作量**: 6-8 小时

---

### 6. 完善 Embedding Router
**影响**: 语义路由功能不完整

**文件**: `internal/runtime/skill/embedding_router.go`

**任务**:
- [ ] 集成实际 embedding 模型
- [ ] 实现向量相似度计算
- [ ] 构建 Skill 向量索引
- [ ] 实现语义搜索
- [ ] 集成到 Router 中

**预计工作量**: 4-6 小时

---

### 7. 实现 Tokenizer
**影响**: 无法准确计算 Token 使用量

**文件**: `internal/runtime/llm/tokenizer.go` (新增)

**任务**:
- [ ] 定义 `Tokenizer` 接口
- [ ] 实现 OpenAI Tokenizer
- [ ] 实现 Anthropic Tokenizer
- [ ] 实现通用 Tokenizer (fallback)
- [ ] 集成到 LLM Runtime

**预计工作量**: 2-3 小时

---

### 8. 扩展 Planner LLM 集成
**影响**: Planner 无法生成实际计划

**文件**: `internal/runtime/agent/planner.go`

**任务**:
- [ ] 修改 `CreatePlan()` 使用 LLM Runtime
- [ ] 完善 Prompt 工程
- [ ] 实现 JSON 解析和验证
- [ ] 添加计划优化和回退逻辑

**预计工作量**: 3-4 小时

---

## P2 任务

### 9. 实现并行执行器
**影响**: 无法并发执行工具调用

**文件**: `internal/runtime/executor/parallel_executor.go` (新增)

**任务**:
- [ ] 创建 `ParallelExecutor`
- [ ] 实现并发控制（信号量）
- [ ] 实现依赖解析
- [ ] 实现错误处理和重试
- [ ] 集成到 DAG 执行

**预计工作量**: 4-5 小时

---

### 10. 实现 Gateway Client
**影响**: Runtime 无法通过 Gateway 调用上游 LLM

**文件**: `internal/runtime/llm/gateway_client.go` (新增)

**任务**:
- [ ] 创建 `GatewayClient`
- [ ] 集成 `gateway/loadbalancer`
- [ ] 实现请求转换
- [ ] 实现响应转换
- [ ] 支持重试和错误处理

**预计工作量**: 4-5 小时

---

### 11. 实现可观测性
**影响**: 无法监控和调试系统

**文件**:
- `internal/runtime/observability/metrics.go` (新增)
- `internal/runtime/observability/tracing.go` (新增)
- `internal/runtime/observability/logging.go` (新增)

**任务**:
- [ ] 定义 Prometheus metrics
- [ ] 集成 OpenTelemetry tracing
- [ ] 结构化日志记录
- [ ] Agent/Skill/Tool 调用追踪
- [ ] 性能指标收集

**预计工作量**: 6-8 小时

---

### 12. 实现热加载
**影响**: 无法动态更新 Skills

**文件**: `internal/runtime/skill/hot_reload.go` (新增)

**任务**:
- [ ] 集成 `fsnotify`
- [ ] 实现文件监听
- [ ] 实现防抖处理
- [ ] 实现增量加载
- [ ] 提供事件回调

**预计工作量**: 3-4 小时

---

## P3 任务

### 13. 实现 Multi-Agent 协作
**影响**: 无法支持复杂的多 Agent 任务

**文件**:
- `internal/runtime/orchestrator/orchestrator.go` (新增)
- `internal/runtime/orchestrator/coordinator.go` (新增)

**任务**:
- [ ] 创建 `Orchestrator`
- [ ] 实现协作模式
- [ ] 实现任务分发
- [ ] 实现结果聚合
- [ ] 支持共识决策

**预计工作量**: 8-10 小时

---

### 14. 实现 Sandbox 执行器
**影响**: 工具执行无安全隔离

**文件**: `internal/runtime/executor/sandbox.go` (新增)

**任务**:
- [x] 落地 `internal/runtime/executor/sandbox.go` 基础原语，覆盖路径/命令/环境校验与统一执行包装
- [x] 将 `sandbox` 配置接入 `internal/runtime/config.RuntimeConfig` 与 `configs/runtime.yaml`
- [x] 把本地 toolkit 文件工具接入 sandbox 路径策略
- [x] 把本地 shell 工具接入命令 / workdir / env 策略
- [ ] 区分本地 sandbox 与远程 MCP governance，避免误报隔离能力
- [ ] 支持进程隔离

**预计工作量**: 5-7 小时

**补充设计**: `docs/skill_runtime/sandbox_execution_plan.md`


---

### 15. 实现 Streaming Tool Calls
**影响**: 无法流式渲染工具调用过程

**文件**: `internal/runtime/executor/stream_executor.go` (新增)

**任务**:
- [ ] 创建 `StreamExecutor`
- [ ] 实现实时解析
- [ ] 实现事件流
- [ ] 集成到 Agent Loop

**预计工作量**: 4-5 小时

---

### 16. 实现 Self-Reflection
**影响**: Agent 无法自我纠错

**文件**: `internal/runtime/agent/reflector.go` (新增)

**任务**:
- [ ] 创建 `Reflector`
- [ ] 实现错误分析
- [ ] 实现修复建议
- [ ] 实现自动重试
- [ ] 集成到 Agent Loop

**预计工作量**: 4-6 小时

---

### 17. 实现 Code Patch Engine
**影响**: 无法安全应用代码修改

**文件**: `internal/patch/` (新增整个目录)

**任务**:
- [ ] 创建 `PatchEngine`
- [ ] 实现补丁解析
- [ ] 实现安全检查
- [ ] 实现备份机制
- [ ] 支持多种补丁类型

**预计工作量**: 6-8 小时

---

### 18. 实现认证授权
**影响**: API 无安全保护

**文件**: `internal/runtime/auth/` (新增整个目录)

**任务**:
- [ ] 实现 JWT 认证
- [ ] 实现 RBAC 授权
- [ ] 实现 API Key 管理
- [ ] 实现中间件集成

**预计工作量**: 6-8 小时

---

### 19. 扩展 API 路由
**影响**: 缺少完整的 REST API

**文件**: `internal/api/skills/handler.go`

**任务**:
- [ ] 实现技能 CRUD
- [ ] 实现 Agent 执行
- [ ] 实现会话管理
- [ ] 实现历史查询

**预计工作量**: 4-6 小时

---

### 20. 添加配置验证器
**影响**: 配置错误难以发现

**文件**: `internal/runtime/config/validator.go` (新增)

**任务**:
- [ ] 定义验证规则
- [ ] 实现结构化验证
- [ ] 提供友好的错误信息
- [ ] 集成到配置加载

**预计工作量**: 3-4 小时

---

### 21. 实现使用量跟踪
**影响**: 无法监控资源消耗

**文件**: `internal/runtime/tracking/` (新增整个目录)

**任务**:
- [ ] 实现 `UsageTracker`
- [ ] 实现 `RateLimiter`
- [ ] 实现 `QuotaManager`
- [ ] 集成存储

**预计工作量**: 4-5 小时

---

### 22. 完善测试覆盖
**影响**: 代码质量无法保证

**任务**:
- [ ] 补充单元测试 (目标 70% 覆盖)
- [ ] 添加集成测试
- [ ] 添加端到端测试
- [ ] 性能测试

**预计工作量**: 12-16 小时

---

### 23. 编写文档
**影响**: 难以维护和使用

**任务**:
- [ ] API 文档
- [ ] 使用指南
- [ ] 部署文档
- [ ] 开发指南

**预计工作量**: 8-12 小时

---

## 实施路线图

### Sprint 1: 核心运行能力 (必须)
**目标**: 让 Agent 能够真正运行

**任务**:
1. ✅ Task 1: LLM Runtime 核心
2. ✅ Task 2: ModelRouter
3. ✅ Task 3: Agent ReAct Loop
4. ✅ Task 4: Skill Executor LLM 集成
5. ✅ Task 7: Tokenizer

**预计时间**: 15-20 小时

**里程碑**: Agent 可以完成基本推理和工具调用

---

### Sprint 2: 会话和增强功能
**目标**: 支持多轮对话和更好的路由

**任务**:
1. ✅ Task 5: Chat/Session 层
2. ✅ Task 6: Embedding Router
3. ✅ Task 8: Planner LLM 集成

**预计时间**: 12-15 小时

**里程碑**: 支持完整的对话流程和智能路由

---

### Sprint 3: 性能和可观测性
**目标**: 提升性能和监控能力

**任务**:
1. ✅ Task 9: 并行执行器
2. ✅ Task 10: Gateway Client
3. ✅ Task 11: 可观测性

**预计时间**: 15-20 小时

**里程碑**: 生产级性能和监控

---

### Sprint 4: 高级特性
**目标**: 实现高级功能

**任务**:
1. ✅ Task 12: 热加载
2. ⏸️ Task 13: Multi-Agent 协作（降级为垂直能力，未进入当前平台主路径）
3. ⏸️ Task 14: Sandbox 执行器（通用平台未完成统一实现）
4. ⏸️ Task 15: Streaming Tool Calls（平台已有流式协议，但非完整 streaming tool runtime）
5. ⏸️ Task 16: Self-Reflection（未进入当前平台主路径）

**预计时间**: 25-35 小时

**里程碑**: 完整的高级功能集

---

### Sprint 5: 基础设施和生产就绪
**目标**: 生产环境部署就绪

**任务**:
1. ⏸️ Task 17: Code Patch Engine（降级为垂直能力）
2. ⏸️ Task 18: 认证授权（仅完成局部 admin 控制，未完成平台级 auth）
3. ✅ Task 19: API 路由扩展
4. ⏸️ Task 20: 配置验证器（未完成统一 validator）
5. ⏸️ Task 21: 使用量跟踪（未完成平台级 usage / quota）

**预计时间**: 20-25 小时

**里程碑**: 生产环境就绪

---

### Sprint 6: 质量保证
**目标**: 代码质量和文档完整

**任务**:
1. ⏳ Task 22: 测试覆盖与失败用例收口
2. ⏳ Task 23: 文档编写与状态对齐

**预计时间**: 20-25 小时

**里程碑**: 全量测试通过、文档与实现对齐、主链路可稳定运行

---

## 总体估算

- **P0 任务**: 15-21 小时
- **P1 任务**: 18-23 小时
- **P2 任务**: 22-27 小时
- **P3 任务**: 52-73 小时

**总计**: 107-144 小时 (约 13-18 个工作日)

---

## 风险和缓解

### 风险
1. **LLM 集成复杂度高** - 可能需要多次迭代
2. **并发执行正确性** - 需要仔细测试
3. **性能问题** - 可能需要优化
4. **测试覆盖不足** - 可能留有隐藏 bug

### 缓解措施
1. 优先完成最小可用版本
2. 逐步添加功能，保持迭代
3. 充分测试核心流程
4. 详细的代码审查
5. 性能测试和优化

---

## 备注

- 本计划基于对现有代码的审查
- 自 2026-03-08 起，本文档同时承担“原始计划归档 + 当前行动项索引”的作用
- 2026-03-09 新增完成项：`JWT claims -> scope` 绑定，当前 scope 来源为 `body > query > header/gin-context > JWT claims > api_key_scopes > default`
- 2026-03-09 新增完成项：管理类接口已支持 `admin_token` 与 `admin_roles` 双通路授权
- 2026-03-09 新增完成项：网关已支持 `gin context claims -> skills auth headers` 透传
- 2026-03-09 新增完成项：已提供 `auth/scope resolver policy` 查询接口与 Go client
- 2026-03-09 新增完成项：`auth/scope policy` 运行期更新已与 `configManager` 当前快照同步（非文件持久化）
- 2026-03-09 新增完成项：`auth/scope policy` 运行期更新已支持定点写回 YAML 配置文件（仅 `skills_runtime` auth 字段）
- 2026-03-09 新增完成项：`usage/quota policy` 运行期更新已支持定点写回 YAML 配置文件（仅 `skills_runtime` usage 字段）
- 2026-03-09 新增完成项：`mutation policy` 运行期更新已支持定点写回 YAML 配置文件（仅 `skills_runtime` mutation 字段）
- 2026-03-09 新增完成项：governance 三件套（mutation / usage / auth）已统一具备 runtime update + configManager 同步 + YAML 定点写回
- 2026-03-09 新增完成项：已提供统一治理视图 API 与治理指标摘要，并补治理指南
- 2026-03-09 新增完成项：`aicli chat` 已改为 route-first + top-K 暴露 skill functions，避免每次全量发送所有 skill schema
- 2026-03-09 新增完成项：`aicli chat` 的 top-K 暴露数量已支持配置文件与 CLI 覆盖
- 优先级可根据实际需求调整
- 预计工作量仅供参考
- 需要与现有 MCP 系统保持兼容
- 遵循 Go 最佳实践和项目规范
