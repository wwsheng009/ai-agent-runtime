# Team 产品闭环实施文档

- **日期**：2026-11-22
- **状态**：工程实施计划（未表示已实现）
- **基线方案**：[`../plan/team-product-closure-and-usage-plan-20261122.md`](../plan/team-product-closure-and-usage-plan-20261122.md)
- **补充审查**：[`../plan/team-product-closure-gap-review-and-roadmap-20261122.md`](../plan/team-product-closure-gap-review-and-roadmap-20261122.md)
- **实施范围**：Team Runtime/API、`aicli` Chat/TUI、Workspace Web、嵌入式 Team Experience、可靠性/历史/可观测性
- **文档性质**：本文件把产品方案转换为可执行的工程任务、契约和验收项；不是实现完成记录，也不是对所有后端能力的现状承诺。

> 本文依据当前工作区的静态代码、路由注册、类型和测试文件整理。代码中已存在的 Team 运行能力与产品闭环能力必须分开描述。所有 `待验证` 项在验证完成前不得作为前端可用能力、权限边界或预算保证。

## 1. 实施目标与非目标

### 1.1 目标

本项目的工程目标是让用户能够在统一语义下完成：

```text
模式选择
  -> Quick Start / Guided / Advanced 配置
  -> teammate 与 worker session readiness
  -> task graph 与 route preview
  -> Preflight
  -> Start Run
  -> Run Monitor / team_wait
  -> task outcome / final summary
  -> retry、clone config、run again、archive（按阶段交付）
```

具体目标：

1. 复用现有 Team store、orchestrator、teammate runner、Runtime API 和 `aicli` Team tool，不在 Web 重新实现调度器、claim 或终态判定。
2. 明确区分普通 subagent、同一 Team 内多成员协作、独立并行和 Fan-out 多 Team。
3. 在 UI 和 API 中分离 Team Definition/Template 与一次 Team Run；若底层暂时兼容复用 Team record，必须公开记录兼容限制，不能宣称已经有独立 Run history。
4. 让 roster、worker session、readiness、task、attempt、outcome、artifact、summary 的责任边界可追踪。
5. 让 TUI、Web 和嵌入入口使用同一组 Team/Task/Session/观察窗口状态语义。
6. 对 Start、Retry、Replan、Cancel、Provision 等网络重试和并发竞争建立服务端状态查询、幂等或版本冲突策略。
7. 让权限、写路径、工具能力和预算限制由 Runtime enforcement 支撑；前端状态、host context、props 和文案不能成为安全边界。

### 1.2 非目标

以下内容不作为本轮直接交付承诺：

- 不在前端复制 `team.Orchestrator`、task claim、dependency scheduler 或 terminal reconciliation。
- 不把现有 `PATCH /teams/{id}` 可写 status 字段直接等同于经过审计的 pause/resume/cancel 状态机。
- 不在未确认后端契约前实现或宣称 Team 级 pause、cancel、budget hard stop、SSE/WebSocket、完整 Run History 或 artifact 下载。
- 不把 `team_spawn` 的 teammate 绑定与普通 `subagent_spawn` child session 混为同一生命周期。
- 不把现有 Web Provision/Dispatch 的多个单成员 Team 包装成一个多成员 Team。
- 不让 task payload 任意指定 provider、model、reasoning 或 tool policy；执行路由由 Runtime resolver 决定并审计。
- 不用前端 mock 的 running/failed/summary 状态遮盖 Runtime 缺口。
- 不清理、覆盖或回滚工作区已有的无关未提交修改。

### 1.3 成功定义

当且仅当以下条件全部满足，才可称为“最小闭环完成”：

- 同一 Team 中可以持久化至少两个 teammate、两个 task 和一条 dependency；
- 每个 teammate 的 roster 记录与 worker session readiness 可区分；
- Preflight 能返回可解释的 blocker/warning/info，且结果来自 Runtime 或明确的服务端校验；
- Start/观察/终态/summary 的 API 语义在 Web 与 TUI 一致；
- `team_wait` timeout 被呈现为观察窗口结束，不被转换成 Team/task 失败；
- task outcome、event、mailbox、path claim、route audit 和 final summary 可追溯；
- 未支持的动作被隐藏或 disabled 并说明原因，而不是点击后无效。

## 2. 实施原则与术语

### 2.1 产品模式边界

| 模式 | 对象关系 | 选择规则 | 实施约束 |
| --- | --- | --- | --- |
| 普通 subagent | 一个 parent 下的独立 child session | 只需一次边界清楚的委派 | 使用 `subagent_spawn`/普通 child wait 体系 |
| 协作 Team | 一个 Team，多个 teammate、task、依赖、mailbox 和 summary | 需要 handoff、共享协作状态或统一 task graph | 使用 `team_spawn`、orchestrator 和 `team_wait` |
| 独立并行 | 多个互不依赖的独立委派 | 需要多份结果但不需要 Team 内协调 | 不因 agent 数量自动升级为 Team |
| Fan-out | 多个隔离 Team/Run | 同一输入的多种尝试、比较或吞吐 | 每行保留独立 Team boundary，不合并成一个 roster |

Web 文案和 TUI 帮助应先解释协作关系，再展示 `team_spawn`、`subagent_spawn` 等内部术语。

### 2.2 对象责任

| 对象 | 本文目标含义 | 当前代码对应/限制 |
| --- | --- | --- |
| Team Definition / Template | 可复用的角色、默认限制、routing 引用和任务模式 | 当前未发现独立持久化对象；必须先定义迁移契约 |
| Team Run | 一次目标、配置快照、成员绑定、task graph、attempt 和结果 | 当前 `team.Team` 更接近运行记录；没有独立 Run resource 的证据 |
| Teammate | Run 中的逻辑角色和 roster 记录 | `team.Teammate` 已有；不等于 session ready |
| Worker Session | 实际承载 teammate task 的会话 | `session_id`、runtime session 和 actor registry 已有，但 readiness 产品化不足 |
| Task | 可分派、依赖、claim、报告结果的工作单元 | `team.Task` 已有状态、版本、retry/attempt、路径和 result ref |
| Attempt | 一个 task 的具体执行尝试 | Task 有 `Attempt`/`RetryCount` 字段；完整历史资源仍需验证 |
| Outcome | task 的结构化完成、阻塞、失败或交接结果 | 当前契约是 `task_status=done|blocked|failed|handoff`，不能未经契约修改为 accepted/rejected |
| Artifact | 可验证、可保留、可引用的产物 | 当前可通过 `result_ref`/result contract 引用；统一 artifact API/保留策略未确认 |
| Event | 可按 Team sequence 读取的生命周期和审计记录 | Team event store 与 `after` cursor 已有；跨 Team cursor 需单独处理 |
| Summary | lead/planner 生成的最终汇总 | `team.summary`/final summary 已有；terminal 与 summary ready 仍必须分开显示 |

### 2.3 Definition、Run 与兼容策略

Phase A 必须先回答：

- 一个 Definition 是否可多次 Run；
- Run 是否拥有独立 ID；
- 配置是否在启动时形成不可变 snapshot；
- Definition 修改是否影响历史 Run；
- `Clone config`、`Run again`、`Retry failed task` 是否创建不同对象；
- 删除/归档 Definition 是否保留历史 Run。

推荐目标模型：

```text
TeamDefinition
  ├─ definition settings
  ├─ teammate role/profile defaults
  └─ task recipe

TeamRun
  ├─ immutable config snapshot
  ├─ runtime teammate/session bindings
  ├─ task graph
  ├─ attempt(s), route audit, outcome, artifact
  ├─ events/mailbox/path claims
  └─ terminal state + summary
```

迁移期间可以提供兼容映射：

```text
legacy Team record -> compatibility TeamRun view
new Definition     -> new TeamRun(s)
```

兼容映射必须在 API response 中标出 `compatibility_mode` 或等价字段，避免把一个旧 Team ID 误报为完整 Run ID。未完成迁移前，Web 不能提供看似可靠的 Run History、Run again 或跨 Run 对比按钮。

## 3. 当前基线与能力矩阵

状态定义：

- **已有**：代码/路由/类型已找到，且可以作为实现基础；不代表用户闭环已完成。
- **部分**：底层或局部入口存在，仍缺少产品操作、错误语义或统一封装。
- **缺失**：本次静态核对未找到可复用实现，必须新增契约/实现。
- **待验证**：存在相关字段或路径，但尚未通过完整测试、跨进程、权限或真实运行验证。
- **未来阶段**：不阻塞 P0 MVP，按 Phase E/F 交付。

| 能力 | 当前代码位置 | 当前状态 | 本实施需补齐 | 验证方式 |
| --- | --- | --- | --- | --- |
| Team store/CRUD | `backend/internal/team/store.go`、`sqlite_store.go`、`backend/internal/api/runtimeapi/team_handlers.go` | 已有 | 统一 draft/run 语义、版本和权限 | Runtime handler/store contract test |
| Team supervisor/orchestrator | `backend/internal/team/orchestrator.go`、`backend/internal/teamsupervisor`、`team_lifecycle.go` | 已有 | 只由 Runtime 作为执行真相；补动作契约 | fake session/store integration |
| Team teammate/task 持久化 | `backend/internal/team/types.go`、`store.go` | 已有 | snapshot、attempt history、artifact 关联 | SQLite reopen/replay test |
| `team_spawn` | `backend/internal/toolbroker/broker.go`、`team_tools.go` | 已有 | 结构化回执、Web facade 对齐 | broker contract/integration test |
| `team_wait` | `broker.go`、`broker_team_test.go` | 已有 | TUI 反馈、`after_seq` 续接和 summary pending 展示 | timeout/cursor/terminal test |
| Team-only context gating | `backend/internal/toolbroker/team_tools.go` | 已有 | Help/诊断显示真实可用工具 | active-context regression test |
| Team 与普通 subagent 边界 | broker、`chat_actor_team_obligations.go`、`chat_actor_agent_obligations.go` | 已有代码约束 | Web/TUI 文案、模式选择 | cross-entry UX/contract test |
| 多成员同 Team | `team_spawn` teammates/tasks、orchestrator | 部分 | Web builder 与 readiness/preflight | two-member dependency E2E |
| teammate roster | `GET/POST /teams/{id}/teammates` | 部分 | PATCH、heartbeat、retire/rebind UI 和错误语义 | API + Web component test |
| worker session binding | `Teammate.SessionID`、runner、actor registry | 部分/待验证 | readiness endpoint/view、stale/offline/rebind | fake session + restart test |
| task graph/dependencies | task/dependency handlers、`GetTaskGraph` | 部分 | task editor、cycle/assignee/path 本地和服务端校验 | graph contract + UI test |
| path claims | `path-claims` handlers、`PathClaimManager` | 已有基础 | preflight 集成和冲突修复入口 | conflict/lease test |
| Team route resolution | `modelrouting`、`chat_actor_registry.go`、`TaskExecutionRoute` | 已有基础 | builder route preview 和 actual-vs-preview audit | dry-run + runner audit test |
| route preview API | `POST /api/runtime/config/document/agent-route-preview`、frontend config client | 部分 | Team Studio query、warning/fallback 展示 | request shape + resolver test |
| Preflight | plan/path/readiness 等分散校验 | 缺失统一能力/待验证 | 统一报告和修复 action | blocker/warning/info contract test |
| Team Start | create/status/supervisor 有基础 | 部分 | 受控 Start/idempotency/status transition | duplicate request/race test |
| Team pause/resume | `TeamStatusPaused` 存在 | 待验证 | 仅在后端定义 transition 后开放 | runtime state-machine test |
| Team cancel | `TeamStatusCanceled` 存在 | 待验证/未发现 Team cancel route | 明确 cancel semantics 或暂不开放 | handler + running session test |
| task retry/replan/reclaim | task handlers 已有 | 部分 | attempt history、影响范围和 UI recovery | retry/replan race test |
| structured task outcome | `task_outcome_contract.go`、outcome route | 已有基础 | Web client/result inspector/acceptance layer | outcome contract test |
| artifact | `result_ref`/result contract 关联 | 部分/待验证 | metadata、权限、retention、retry lineage | artifact integration test |
| final summary | terminal state/summary handlers | 已有基础 | summary pending/failure 状态和单独刷新 | terminal-summary test |
| Team-local event cursor | `GET /teams/{id}/events?after=` | 已有 | Web incremental polling/dedup/backoff | cursor replay test |
| `team_wait` `after_seq` | broker Team event wait | 已有 | TUI structured continuation | TUI integration test |
| Web Details | `runtime-teams.tsx`、`use-team-details-loader.ts`、details panels | 已有基础 | last-known-good、cursor、results/attempt | frontend test/e2e |
| Web Fan-out | `use-runtime-team-dispatch.ts`、dispatch panels | 已有 | 文案/导航明确 Fan-out，保留 partial success | existing + regression test |
| Web multi-member builder | 当前未发现 | 缺失 | Team Studio | Web journey test |
| API client full lifecycle | `frontend/src/api/runtime/teams.ts` | 部分 | PATCH/DELETE/outcome/retry/replan/readiness/action wrappers | TypeScript/API tests |
| Definition/Run/snapshot/history | 当前 Team record/event 查询 | 缺失/待验证 | Phase A schema + Phase E migration | migration/reopen test |
| embedded Team shell | 当前 Workspace Teams surface | 缺失 | shared shell + host adapter | host integration test |
| permission enforcement | write paths/claims/permission modes 有基础 | 待验证 | Runtime authorization matrix | negative security test |
| budget hard stop | `RunMeta.BudgetTokens` 等字段存在 | 待验证 | 只有 Runtime enforcement 后才开放 | enforcement test |
| SSE/WebSocket | 当前路线以 polling 为主 | 未来阶段 | 先稳定 cursor/polling，再评估 streaming | load/reconnect test |

## 4. 技术架构与责任边界

### 4.1 目标调用链

```text
Web Team Studio / TUI / Embedded host
          |
          v
Runtime API / tool broker facade
          |
          +--> Team contract validation / preflight
          +--> Team store (SQLite or configured store)
          +--> lifecycle service / supervisor
          +--> orchestrator / path claims / leases
          +--> teammate runner / session actor client
          +--> route resolver + route audit
          +--> events / mailbox / summary / outcome
```

Web、TUI 和 Embedded 只提交声明式配置、调用受支持的生命周期动作、读取 server state；以下逻辑必须留在 Runtime：

- task ready 判定、dependency completion、claim/reclaim；
- writer/path claim 和 lease fencing；
- Team/Task terminal reconciliation；
- session provisioning 的真实状态；
- route resolution、provider health fallback 和 attempt audit；
- 权限、工具、文件和预算 enforcement；
- idempotency、版本冲突和最终状态。

### 4.2 现有模块与改造模块

| 层 | 复用模块 | 新增/改造方向 |
| --- | --- | --- |
| Runtime domain | `backend/internal/team/*` | definition/run snapshot、preflight、attempt/artifact 关联；若不能一次迁移，先增加兼容 facade |
| Runtime API | `backend/internal/api/runtimeapi/team_handlers.go`、`team_lifecycle.go`、`handler.go` | operation-oriented endpoints、错误 envelope、幂等/版本、readiness/preflight；禁止前端绕过 handler |
| Routing | `backend/internal/modelrouting/*`、`chat_actor_registry.go` | Team preview wrapper、preview/audit 对照，不复制 resolver |
| Broker/TUI | `backend/internal/toolbroker/broker.go`、`team_tools.go`、`backend/cmd/aicli/commands/chat_team_*` | structured spawn/wait feedback、帮助/诊断、cursor continuation |
| Web API/types | `frontend/src/api/runtime/teams.ts`、`frontend/src/types/runtime/teams.ts` | 单资源/lifecycle/outcome/preflight/query types；统一 query key 和 error mapping |
| Web surface | `frontend/src/components/workspace/runtime-teams*` | Team Studio、Run Monitor、Results/Task Inspector；Dispatch 保持 Fan-out 独立 |
| Embedded | 新建 `frontend/src/components/.../team-experience` 或按现有组件约定落位 | shell、host adapter、URL recovery、subscription cleanup |
| Tests | `backend/internal/*team*_test.go`、`frontend/src/components/workspace/runtime-teams/*.test.ts` | contract, integration, e2e, negative permission and recovery tests |

### 4.3 现有 Web 组件接入注意事项

当前实际入口使用：

- `frontend/src/components/workspace/runtime-teams.tsx`；
- `runtime-teams/directory-view.tsx`；
- `runtime-teams/runtime-team-details-panel.tsx`；
- `use-team-details-loader.ts`；
- `use-runtime-team-dispatch.ts` 及其拆分目录。

仓库中还存在 `runtime-teams/team-details-panel.tsx` 及同名旧目录。接入 Team Studio 前必须确认旧组件是否仍被入口引用；不得在两个并行组件树中重复实现状态和 API 调用。

当前 `useTeamDetailsLoader` 用 `Promise.allSettled` 并行读取 summary、teammates、tasks、graph、events、mailbox、path claims；这应保留为部分成功加载基础，但需要增加 cursor、last-known-good 和 summary readiness，而不是用空响应覆盖已有状态。

## 5. API 与类型契约

### 5.1 契约原则

1. **先读后写**：所有写操作前端先读取当前资源版本和能力声明；写请求携带 `expected_version` 或等价的条件版本。服务端返回版本冲突时，前端必须重新读取并提示用户选择合并或重试，不能静默覆盖。
2. **请求可重放**：创建 Run、Start、Retry、Replan、Provision 等可能因网络超时被客户端重试的动作，必须支持 `Idempotency-Key` 或服务端可识别的等价键。幂等键应绑定用户、资源、动作和请求体摘要，并设置明确保留时间。
3. **动作与字段更新分离**：普通 `PATCH` 只更新允许编辑的配置字段；Start、Retry、Replan、Cancel 等改变生命周期的动作使用独立 operation endpoint 或 tool action，不让客户端通过任意 status 字段绕过状态机。
4. **服务端返回事实**：响应中的 `status`、`readiness`、`route`、`attempt`、`outcome`、`summary` 和权限结论必须来自 Runtime。前端可以计算展示排序、折叠和本地草稿，但不得生成执行状态。
5. **可观测请求**：每个响应包含或可关联 `request_id`；异步动作返回 `operation_id`、资源 ID、当前版本和可继续观察的 cursor。日志、event 和错误中不得泄露未授权路径或 provider secret。
6. **能力协商**：对尚未支持的动作返回结构化 capability/unsupported 信息。UI 应据此隐藏或 disabled，而不是依赖 HTTP 404 的文案猜测。

### 5.2 现有 API 的封装边界

下表区分当前已找到的 API surface 与本实施所需的统一 facade。路径为现有路径时仍需以 router 注册和 handler contract test 为准；带“目标”的路径不得在实施前当作已存在接口。

| 领域 | 当前可复用 surface | 前端 client 目标封装 | 说明 |
| --- | --- | --- | --- |
| Team list/get | Team list/get handlers | `listTeams`、`getTeam` | 统一 query 参数、分页/排序和错误 envelope |
| Team update | `UpdateTeam` 对部分字段可写 | `updateTeamDraft` | 仅允许 draft/config 字段；status 不经此接口自由写入 |
| Teammates | `GET/POST /teams/{id}/teammates` | `listTeammates`、`createTeammate`；目标：`updateTeammate`、`heartbeat` | `session_id` 不是 readiness 的唯一证明 |
| Tasks | task list/create、task graph | `listTasks`、`createTask`、`getTaskGraph`；目标：`updateTask` | 依赖、assignee、paths 必须服务端校验 |
| Events | Team events `after`/`after_seq` | `listTeamEvents({ after })` | 只对同一 Team cursor 续读；跨 Team 不能直接复用 |
| Mailbox | mailbox list/send/ack | `listMailbox`、`sendMailboxMessage`、`ackMailboxMessage` | ack 的权限、幂等和消息状态需保留 |
| Path claims | list/check path claims | `listPathClaims`、`checkPathClaims` | check 不能取代真正 claim/lease |
| Final summary | `GetTeamFinalSummary` | `getFinalSummary` | terminal 与 summary ready 分开呈现 |
| Task outcome | `ReportTaskOutcome` 等 outcome handlers | `getTaskOutcome`、`reportTaskOutcome` | 继续使用当前 outcome contract；不要自行改枚举 |
| Retry/replan | `RetryTask`、`ReplanTask` | `retryTask`、`replanTask` | 目标是返回新的 attempt/plan version 和影响范围 |
| Route preview | agent route preview API | `previewTaskRoute`/`previewRunRoutes`（目标 facade） | 复用 resolver，不在 Web 复制路由逻辑 |
| Readiness/preflight | 分散 readiness 检查 | `getTeamReadiness`、`preflightTeamRun`（目标） | 统一 blocker/warning/info 之前不得作为已存在 API |
| Lifecycle actions | Team status/lifecycle 基础 | `startTeamRun`（目标）；pause/resume/cancel 待确认 | 不把 `PATCH status` 作为动作契约 |
| Definition/Run | 未发现独立资源契约 | `createDefinition`、`createRun`、`getRun`、`listRuns`（目标） | Phase A 先定 schema，再决定兼容实现方式 |

### 5.3 目标资源和动作契约（Phase A 待确认）

以下是供后端、Web 和 TUI 对齐的目标形状，不代表当前路由已经实现。Phase A 必须将其转换为真实 OpenAPI/Go/TypeScript 契约；若决定沿用现有 Team resource，应在 response 中明确 compatibility mode。

```text
TeamDefinition
  id: string
  workspace_id: string
  name: string
  description: string?
  version: integer
  config: DefinitionConfig
  archived_at: timestamp?
  created_at: timestamp
  updated_at: timestamp

TeamRun
  id: string
  definition_id: string?
  legacy_team_id: string?
  workspace_id: string
  goal: string
  status: active|paused|done|failed|partially_completed|canceled
  phase: draft|preflight|starting|running|reconciling|terminal
  compatibility_mode: none|legacy_team_view|migrated
  config_snapshot: immutable object
  version: integer
  event_seq: integer
  summary_state: pending|ready|failed|not_applicable
  created_at: timestamp
  started_at: timestamp?
  terminal_at: timestamp?

PreflightReport
  resource_id: string
  checked_at: timestamp
  result: pass|blocked|warning
  checks: Check[]
  effective_routes: RoutePreview[]
  current_version: integer

Check
  code: string
  severity: blocker|warning|info
  subject: team|teammate|session|task|dependency|path|route|permission|budget
  message: string
  remediation: string?
  action: string?
  details: object?
```

推荐动作请求/响应规则：

| 动作 | 请求最少字段 | 成功结果 | 失败/竞争语义 |
| --- | --- | --- | --- |
| Create draft | definition/config、`Idempotency-Key` | draft ID、version、capabilities | 同幂等键返回同一资源；校验失败返回 field errors |
| Preflight | resource ID、version | 完整 `PreflightReport` | 不创建运行；服务端不得只返回布尔值 |
| Start | resource ID、`expected_version`、`Idempotency-Key` | run ID、operation ID、当前状态和 event cursor | 已启动返回同一 operation；版本冲突 409；blocker 409/422 |
| Retry task | run/task ID、失败 attempt、`expected_version`、幂等键 | 新 attempt 或明确的 no-op、影响范围 | 已 retry 返回同一结果；不可 retry 返回原因 |
| Replan task | run/task ID、变更说明、`expected_version` | 新 plan version、受影响 task 列表 | 依赖/权限/运行竞争冲突不得静默覆盖 |
| Report outcome | run/task/attempt ID、当前 task version、outcome | 应用后的 task/attempt 状态和 event sequence | stale attempt、非法 transition 返回冲突或契约错误 |
| Pause/resume/cancel | run ID、动作版本、幂等键 | 仅在后端状态机确认后开放 | 未支持返回 capability `unsupported`，不调用猜测路径 |

### 5.4 错误 envelope 和版本语义

目标错误结构应至少能区分输入错误、权限错误、资源竞争、服务暂时不可用和动作不支持：

```text
RuntimeError
  error: object
    code: string
    message: string
    category: validation|authorization|not_found|conflict|unavailable|unsupported|internal
    retryable: boolean
    request_id: string
    resource_id: string?
    current_version: integer?
    expected_version: integer?
    field_errors: map<string, string[]>?
    remediation: string?
```

前端映射规则：

- `authorization`：不显示资源内部细节，不通过重试绕过；
- `conflict`：保留用户编辑草稿，重新读取 server state 后提供 diff；
- `unavailable` 且 `retryable=true`：指数退避，保留 last-known-good；
- `unsupported`：刷新能力声明，隐藏动作并说明版本/部署限制；
- `validation`：定位到具体字段或 task graph 节点；
- `internal`：展示 `request_id`，不把错误文本当作 task outcome。

### 5.5 API client 实施规则

`frontend/src/api/runtime/teams.ts` 应是所有 Web Team 请求的唯一入口；组件不得拼接 Team URL。类型放在 `frontend/src/types/runtime/teams.ts` 或既有类型约定位置，并遵守：

- query key 包含 workspace、Team/Run ID、cursor 和 config version；
- mutation 成功后以服务端响应为准更新 cache，不根据按钮动作乐观生成 terminal 状态；
- `Promise.allSettled` 只用于独立读取或 Fan-out partial success，不把失败吞成空数组；
- 每个 mutation 都有明确 pending/error/success 状态和 abort/unmount 清理；
- API client 保留未知字段或通过显式 `unknown` 扩展处理，避免新后端字段被静默丢弃；
- target endpoint 尚未实现时，先增加类型/contract test 和 capability gate，不写假 adapter。

## 6. 数据模型与迁移

### 6.1 目标数据模型

#### TeamDefinition

用于保存可复用的配置意图，而不是某次运行的事实。包括 workspace owner、名称、默认目标、成员角色模板、任务模板、max teammates/writers、工具/权限声明引用、默认路径和 routing 引用。Definition 修改必须递增版本；历史 Run 只读取启动时 snapshot。

#### TeamRun

用于保存一次实际执行。至少包含：Run ID、workspace、Definition ID（可空表示临时运行）、兼容 legacy Team ID、目标、状态、phase、版本、创建/开始/终止时间、配置 snapshot 引用、event sequence、summary state 和权限主体。Run 的 roster、task graph、attempt、outcome、artifact、mailbox、path claim 和 route audit 都以 Run 作为归属边界。

#### Run snapshot

启动前由 Runtime 生成不可变快照，至少覆盖：

- 原始用户输入和规范化配置；
- Definition ID/version（如有）；
- teammate role/profile/capabilities/session binding 来源；
- task、assignee、dependency、difficulty、task type、paths 和 writer 声明；
- resolver 输入和有效 route 摘要（不含 secret）；
- 权限/工具/profile 的策略引用；
- 预算声明和 enforcement 能力版本（仅声明不代表 hard stop）；
- schema version、创建请求 ID 和 idempotency key 摘要。

快照必须能回答“当时 Runtime 看到的是什么配置”，不能由当前 Definition 回放推导。若当前 SQLite store 不适合存 JSON snapshot，应采用版本化表或 canonical blob，并做 checksum/大小限制。

#### Teammate 与 Worker Session Binding

`Teammate` 表示 Team 内逻辑成员；binding 记录 session ID、provisioning source、session state、last heartbeat、readiness reason、绑定版本、retired 时间和错误摘要。必须允许以下状态同时存在：

- roster 已创建但尚未绑定 session；
- session 已绑定但 offline/stale；
- session ready 但没有可分派 task；
- teammate retired，历史 task/attempt 仍可审计。

#### Task 与 Dependency

Task 保持当前 task status、priority、goal、inputs、deliverables、assignee、paths、difficulty、task type、retry/reclaim 信息和 `result_ref`。依赖边必须有稳定 ID 或可确定的 `(run_id, predecessor, successor)` 主键，禁止自环和跨 Run 边。图版本随结构化编辑递增；Start 后 snapshot 中的图不可被普通 draft update 修改。

#### Attempt

每次真正开始执行或被 Runtime 接受为一次 retry 时生成 Attempt ID。至少记录：task/run ID、attempt number、触发原因、parent attempt、claim/lease 信息、session ID、effective route、route resolver version、开始/结束时间、最后 heartbeat、terminal reason、outcome ID、artifact refs 和 error class。只有 Runtime 可以创建 Attempt；客户端不能通过任意 `attempt` 数字覆盖历史。

#### Outcome

沿用当前 `task_outcome_contract.go` 的合法 task status 和结构化字段。实施补齐：提交者身份、attempt ID、task version、summary/narrative、deliverables、blockers、handoff、artifact refs、observed event sequence、reported_at 和 apply result。Outcome 应区分“worker 报告了什么”和“Runtime 接受后 task 变成什么状态”；非法或过期报告必须保留拒绝原因的 event，而不是覆盖上一次有效结果。

#### Artifact

第一阶段只将 artifact 视为可验证引用：artifact ID、kind、display name、URI/ref、checksum（若可用）、size、content type、producer attempt、created_at、visibility、retention state 和 authorization scope。真实下载、对象存储、保留/删除策略在没有后端契约前不开放。`result_ref` 只能作为 legacy/opaque reference，不自动承诺可下载。

#### Event 与 Summary

Event 采用 Team/Run-local 单调 sequence，记录 actor、kind、resource、payload 摘要、created_at、request_id 和 schema version。Summary 独立保存生成状态、source event sequence、生成 attempt、内容引用、error 和 ready time。Team terminal 不自动意味着 summary ready。

### 6.2 Legacy Team 兼容映射

迁移顺序：

1. 为现有 `team.Team` 增加兼容读取视图，不改变旧 caller 的 ID 和状态值；
2. 为新运行写入 `compatibility_mode`、schema version 和 snapshot reference；
3. 以双读 contract test 验证旧 Team Details、TUI `team_wait` 和新 Run view 返回等价状态；
4. 新 Definition/Run API 稳定后，再把新创建入口切到新模型；
5. 旧 Team 不做隐式回填为完整 Definition/Run，除非迁移脚本能提供确定的 snapshot、owner 和历史边界；
6. 迁移期间保留只读 fallback 和 feature flag，出现问题可切回旧查询 surface。

禁止操作：

- 直接把旧 Team ID 改成另一个 Run ID；
- 通过当前 Definition 重算历史 Run 的 task graph；
- 删除仍被 event、mailbox、outcome 或 artifact ref 引用的记录；
- 未验证 SQLite migration/reopen/replay 就修改生产 schema。

### 6.3 SQLite 与存储迁移要求

每个 schema migration 必须包含：版本号、向前迁移、启动时校验、回滚/降级说明（若不可逆则明确备份恢复）、旧数据兼容查询、空/大数据测试、重启后 cursor/sequence 验证。至少覆盖：

- Team/Run/Definition ID 与兼容字段；
- snapshot blob/checksum 或规范化 snapshot 表；
- attempt/outcome/artifact reference 表；
- event sequence 唯一性和索引；
- task graph version 与 dependency 外键；
- owner/workspace authorization 需要的索引。

迁移脚本不得修改与本项目无关的 memory/cache 表或未列入迁移说明的工作区文件。

## 7. 状态机与控制动作

### 7.1 Team 状态的现状与目标视图

当前代码已有 `active`、`paused`、`done`、`failed`、`partially_completed`、`canceled` 枚举。实施时不得仅因枚举存在就开放所有转移。推荐在 API 层将“配置阶段”和“运行状态”分开：

```text
draft -> preflight -> starting -> active -> reconciling -> terminal
                           |          |
                           |          +-> done / failed / partially_completed / canceled
                           +-> preflight_blocked
```

对 legacy Team，`active/paused/done/...` 仍是服务端事实；`draft/preflight/starting/reconciling` 可以作为 operation/phase view，只有后端有持久化和恢复语义时才写入资源。前端不得凭请求开始/结束时间猜 phase。

### 7.2 Task 状态

以现有 Task status 为准，至少包括 `pending`、`ready`、`running`、`blocked`、`done`、`failed`、`cancelled`、`reclaim_pending`。预期合法路径：

```text
pending -> ready -> running -> done
                         |       |
                         |       +-> retry -> ready/running (new attempt)
                         +-> blocked -> replan/unblock -> ready
                         +-> failed  -> retry -> ready/running
                         +-> cancelled
ready/running -> reclaim_pending -> ready/running
```

依赖完成、claim、lease、reclaim 和 terminal reconciliation 均由 Runtime 决定。UI 可以显示建议动作，但不能本地把 `blocked` 改为 `ready`。

### 7.3 Summary 和观察窗口

- **Team terminal**：所有可执行 task 已达到终态，或 Runtime 明确给出 terminal 状态；
- **summary pending**：Team 已 terminal，但 final summary 尚未生成或尚未读取到；
- **summary ready**：summary 生成并通过 schema 校验；
- **summary failed**：summary 生成失败，有独立 error/request ID；不能回写为 Team failed；
- **observation timeout**：`team_wait`/polling 观察窗口结束，没有改变 Team/task 状态；必须显示“仍在运行或未观察到终态”。

### 7.4 动作权限表

| 动作 | draft | preflight blocked | active | terminal | 当前开放规则 |
| --- | --- | --- | --- | --- | --- |
| 编辑配置 | 允许 | 允许 | 禁止修改 snapshot | 创建新 Definition/Run | 由 server version 控制 |
| Preflight | 允许 | 允许重算 | 可选只读 | 可对历史做只读诊断 | 目标 API，Phase A 定义 |
| Start | 允许且 preflight pass | 禁止 | 幂等返回现状 | 禁止或创建新 Run | 目标动作，Phase A/B |
| Send mailbox | 按权限 | 按权限 | 按权限 | 依产品定义只读/禁止 | 复用现有 mailbox contract |
| Retry task | 否 | 否 | 仅 failed/blocked 等合法状态 | 仅在历史策略允许时 | 复用 handler，补 attempt 语义 |
| Replan | 依 graph lock | 否 | 只在后端允许的窗口 | 否 | 复用 handler，补影响范围 |
| Pause/resume | 未确认 | 未确认 | 未确认 | 否 | 不因 enum 存在而开放 |
| Cancel | 未确认 | 未确认 | 未确认 | 幂等读取 terminal | 未发现完整 Team route，不开放 |
| Archive/delete | 依 Definition/历史策略 | 依策略 | 不可删除 active Run | 仅保留策略允许 | Phase E/F |

### 7.5 幂等和并发

- Start 的重复请求必须返回同一个 Run/operation 或明确冲突，不得创建两个同名运行；
- Retry 以 task version + failed attempt 作为条件，两个客户端同时 retry 至多生成一个有效 next attempt；
- Replan 必须锁定 graph version；若期间有 task outcome 改变图可见状态，返回 409 并要求重新读取；
- outcome apply 以 task version/attempt ID fencing；迟到的 worker 报告只能生成 rejected event；
- terminal reconciliation 可重复执行，结果必须稳定；
- 前端卸载、浏览器刷新或 TUI 重连不得取消 Runtime operation。

## 8. Preflight、权限、路径和预算

### 8.1 Preflight 检查顺序

Preflight 是服务端可审计的报告，不是前端表单校验的集合。建议顺序如下：

1. **资源与版本**：workspace、Definition/Team、引用 ID、schema version、graph version；
2. **Team 容量**：max teammates、max writers、重复角色、空 task graph；
3. **teammate/session**：至少一个合法 roster、session 绑定、offline/stale、heartbeat、能力匹配；
4. **task graph**：未知 assignee、重复 task ID、自环、cycle、跨 Run dependency、孤立 task；
5. **执行 route**：逐 task 调用同一 route resolver，返回 effective route、fallback/warning 和 route audit 预览；
6. **权限/工具**：执行主体、profile、工具集合、workspace scope、write capability；
7. **path claims**：读路径、写路径、writer 冲突、当前 lease、可恢复冲突；
8. **outcome/summary 依赖**：result contract、summary consumer、artifact reference 能力；
9. **预算声明**：仅检查配置是否存在和 Runtime 是否声明 enforcement，不把预算字段存在当作 hard stop；
10. **部署能力**：runtime build、feature flags、API capability、schema version。

每项返回 `blocker`、`warning` 或 `info`，并提供稳定 code、subject、message、remediation 和可选 action。Web/TUI 展示可以按严重性分组，但不能丢失 code 和 request ID。

### 8.2 权限边界

授权必须在 Runtime handler、store query、orchestrator action、session provisioning、path claim 和 artifact access 各层执行。至少覆盖：

- workspace/Team/Run 所属主体；
- Definition 的读取、编辑、运行、归档；
- teammate/session 的绑定、heartbeat、retire；
- task 的创建、改图、claim、outcome、retry/replan；
- mailbox 读取/发送/ack；
- path claim 和 artifact 读取；
- route/provider/model 信息是否对当前用户可见。

`host context`、浏览器路由、React props、TUI 展示模式、隐藏按钮和 disabled 状态只能改善体验，不能授权。所有负向测试都应直接调用 API/broker，验证绕过 UI 仍被拒绝。

### 8.3 Path claims 与 writer 隔离

Preflight 的 path check 是启动前诊断；真正运行时仍由 `PathClaimManager`/lease 做 claim、renew、release、fence 和 reclaim。实施要求：

- canonicalize 路径后再比较，避免大小写、分隔符和 `..` 绕过；
- 读写权限按 Runtime workspace policy 判断，不能只检查路径字符串；
- writer 冲突返回参与 task/teammate/lease 的可解释摘要，但不泄露无权限资源；
- lease 过期、worker offline、retry 和 replan 不得留下可被新 writer 无条件抢占的幽灵 claim；
- UI 的“修复冲突”只能修改声明或请求 Runtime reclaim，不能直接删除其他成员 claim。

### 8.4 Route preview 与实际执行

Route preview 调用现有 route resolver 的服务端 wrapper，返回 route family/provider/model/profile 的非敏感摘要、fallback、理由、resolver version 和 warning。实际 Attempt 生成时再解析一次并记录 effective route。若 preview 与 actual 不同：

- attempt audit 记录差异原因；
- UI 显示“实际执行路由与预览不同”，不伪造为预览结果；
- provider/model secret、内部 endpoint 和未授权健康细节不进入 Web payload；
- 不允许 task payload 自行设置 provider/model/reasoning/tool policy 以绕过 resolver。

### 8.5 预算

当前字段或 `RunMeta.BudgetTokens` 的存在只证明有预算相关数据，不能证明 Runtime 会在 token、时间、并发或成本达到阈值时停止执行。只有完成以下条件后才可开放 hard stop 或强保证文案：

- 每次 model/tool/session 消耗可由 Runtime 记录；
- 预算在 attempt/task/Team 边界定义清楚；
- 并发请求与重试不会绕过计数；
- 超限动作可阻止新工作并安全终止/回收 lease；
- 重启后账本可恢复；
- 负向测试证明超限不会继续启动新 attempt。

在此之前，预算只作为 `info`/`warning`，UI 不能写“保证不超预算”。

## 9. 事件、cursor、polling 与结果一致性

### 9.1 Cursor 语义

Team event cursor 是 Team-local 的单调 sequence。Web API 和 TUI `team_wait` 必须明确使用哪个参数（`after` 或 `after_seq`），并在 facade 层统一为 `next_cursor`。规则：

- 首次读取使用 server 返回的最新窗口或 `after=0`，不使用客户端时间戳代替 sequence；
- 成功处理事件后保存 `next_cursor`，分页/轮询请求携带它；
- 重复事件按 `(run/team_id, sequence)` 去重；相同 sequence 内容变化视为服务端错误并记录；
- cursor 过旧、资源迁移或不可用时返回 resync required，客户端重新获取 snapshot 后再续读；
- 不把 Team cursor 与普通 child `subagent_events` cursor 互换；
- Fan-out 每个 Team/Run 保留自己的 cursor，批次比较器不得假设全局 sequence。

### 9.2 Polling 策略（P0）

在没有已确认的 SSE/WebSocket 契约前，统一使用可取消的 polling：

1. Start/操作响应返回初始资源、`event_seq` 和建议轮询间隔；
2. active/starting 时按 1s、2s、4s、8s 的指数退避，设上限并在有新事件时重置到较短间隔；
3. 429/503/网络错误遵守 `Retry-After`，并在 UI 显示“连接暂时不可用”，不把资源标记 failed；
4. 每轮可并行读取 snapshot、events、tasks/summary，但共享 AbortController 和版本检查；
5. 收到 terminal 后停止高频 task polling，继续短窗口刷新 summary，直到 ready/failed 或超出观察上限；
6. 页面隐藏、TUI 暂停渲染或组件卸载时取消请求，恢复/重连后从 last cursor 继续；
7. 观察窗口超时只结束本次 poll/team_wait，不改变 Team/task status；
8. 遇到 resync required，先拉取完整 snapshot，再从返回 cursor 继续，不把缺失中间事件当作没有发生。

### 9.3 Last-known-good 与部分成功

`useTeamDetailsLoader` 当前使用 `Promise.allSettled`，实施时保留部分读取能力但增加：

- 每个数据片段的 `loadedAt`、`cursor`、`error` 和 `isStale`；
- 新请求失败时保留上一次合法数据，并显式标为 stale；
- 空数组仅在服务端明确返回空数组时覆盖已有列表；请求失败不能清空 roster/tasks/events；
- summary、task、event、mailbox 的失败独立显示，不能让 summary 失败覆盖整个 Team 状态；
- 当 snapshot version 变化时使草稿和局部 cache 进入 reconcile 状态；
- 最终结果必须能从 snapshot + events + outcome/summary 重新加载，不依赖浏览器内存。

### 9.4 一致性验收

至少验证：刷新页面、关闭再打开 TUI、网络断开后恢复、重复事件、乱序响应、终态先于 summary、summary 先于最后一条 event、worker heartbeat 过期、task retry 与旧 attempt 报告同时到达。每个场景都要定义最终 UI 期望和 Runtime 的权威结果。

## 10. 分阶段实施任务清单

### 10.1 计划约定

- **P0**：最小闭环和安全/一致性基础；没有 P0 不应对外宣称 Team Builder/Run 已完成。
- **P1**：提升可恢复性、跨入口体验和历史能力；不阻塞受限 MVP，但必须有明确 feature gate。
- **P2**：高级实时操作和优化；只有后端契约、运行成本和安全边界明确后实施。
- **后端依赖状态**：`已有基础` 表示可复用但仍需 contract test；`部分` 表示需补齐；`新建` 表示当前没有统一实现；`待确认` 表示不得进入默认 UI。
- 每个任务的 DoD 都包含：代码/文档变更、针对性测试、错误和权限语义、可观测字段、无关工作区改动检查。

### 10.2 Phase A：产品契约与 Runtime 基础（P0）

#### A-01 统一术语、模式和能力矩阵

- **目标**：冻结 Team、普通 subagent、独立并行、Fan-out、Definition、Run、Snapshot、Attempt、Outcome、Artifact、Summary 的用户和工程含义。
- **路径/模块**：本实施文档、产品帮助、`backend/internal/team` 契约注释、TUI/Web copy。
- **依赖**：无。
- **API/数据契约**：确认状态枚举、合法 outcome status、summary pending 语义、`compatibility_mode` 和 capability 命名。
- **实现**：建立术语表、事件/错误 code 注册表和“不支持即不显示”规则；记录所有尚未验证项。
- **测试**：文档链接/标题检查；API/TypeScript/TUI 文案中关键术语一致性检查。
- **DoD**：产品、Runtime、Web、TUI 负责人签字；Fan-out 不再被描述为单个多成员 Team；开放问题有 owner 和验证方式。
- **后端依赖**：已有代码事实 + 新的跨团队契约确认。

#### A-02 Definition/Run/Snapshot schema 与兼容读取

- **目标**：确定是否新增独立 Definition/Run 资源，并让新运行形成不可变 snapshot。
- **路径/模块**：`backend/internal/team/types.go`、`store.go`、`sqlite_store.go`、迁移目录和 store tests。
- **依赖**：A-01。
- **API/数据契约**：ID、版本、owner/workspace、compatibility mode、snapshot schema version、event sequence、summary state。
- **实现**：先实现只读兼容映射和新 schema version；禁止把 legacy Team 强行回填为完整历史 Run。
- **测试**：新旧数据读取、SQLite reopen、迁移前后 task/event 查询、snapshot checksum/不可变性测试。
- **DoD**：能从存储重建完整 Run view；Definition 修改不改变既有 snapshot；迁移失败有安全停止和恢复说明。
- **后端依赖**：新建；依赖 store/migration review。

#### A-03 生命周期动作、错误 envelope 和幂等层

- **目标**：把 Start、Retry、Replan、Outcome apply 等动作变成可重放、可审计的服务端契约。
- **路径/模块**：`backend/internal/api/runtimeapi/team_handlers.go`、`team_lifecycle.go`、`handler.go`、通用错误/operation 代码。
- **依赖**：A-01、A-02。
- **API/数据契约**：`request_id`、`operation_id`、`Idempotency-Key`、`expected_version`、409 conflict、unsupported capability。
- **实现**：分离字段 PATCH 与 lifecycle action；统一错误 category/retryable；把重复请求映射到同一 operation/result。
- **测试**：重复请求、并发 Start/Retry、超时后重放、stale version、未授权调用、未知 action。
- **DoD**：Runtime 不会因客户端重试创建重复 Run/Attempt；所有 mutation 有 request ID 和可读 error code；现有 caller contract 不回归。
- **后端依赖**：部分；需要 handler/router 与 store version 支持。

#### A-04 Runtime Preflight、readiness 和 route preview facade

- **目标**：提供统一 blocker/warning/info 报告，不把前端校验当作执行前置条件。
- **路径/模块**：`backend/internal/team`、`backend/internal/api/runtimeapi`、`backend/internal/modelrouting`、path claims/session readiness。
- **依赖**：A-01、A-02、A-03。
- **API/数据契约**：`PreflightReport`、稳定 check code、effective route preview、readiness reason、remediation action。
- **实现**：按资源、成员/session、graph、route、permission、path、artifact/summary、budget capability 顺序校验；复用现有 resolver/claims。
- **测试**：cycle、unknown assignee、offline session、writer conflict、route fallback、无权限、budget unsupported；报告可重放。
- **DoD**：至少一个两成员/两任务依赖 Team 能被服务端准确判定；每个 blocker 有稳定 code 和 remediation；不泄露 secret/path。
- **后端依赖**：新建统一层，复用已有分散检查。

#### A-05 Web/TUI API types、capability 和 query contract

- **目标**：让所有入口消费同一组资源/动作/错误类型。
- **路径/模块**：`frontend/src/api/runtime/teams.ts`、`frontend/src/types/runtime/teams.ts`、TUI broker argument/result types。
- **依赖**：A-03、A-04。
- **API/数据契约**：资源 schema、cursor、capability、error envelope、summary state、attempt/outcome refs。
- **实现**：增加单一 client wrapper 和类型；组件不得直接拼 URL；保留 unknown/forward-compatible fields。
- **测试**：TypeScript compile、API fixture contract、未知字段、error mapping、cursor serialization。
- **DoD**：Web/TUI 不再各自定义状态含义；unsupported action 可由 capability gate 阻断。
- **后端依赖**：部分；可先用 contract fixtures，但不得用假执行状态。

#### A-06 Feature flag、审计和观测基线

- **目标**：让新入口可灰度、可回退、可关联请求和事件。
- **路径/模块**：Runtime config/feature flags、Web Workspace route、TUI capability/help、logging/metrics。
- **依赖**：A-01 至 A-05。
- **API/数据契约**：feature capability、schema version、request/operation ID、migration version、health/readiness 指标。
- **实现**：定义 `team_studio`、`team_run_v2` 等受控开关（实际命名以仓库约定为准），默认关闭未验证动作；记录 preflight/start/outcome latency/error。
- **测试**：flag off 不暴露入口；flag on 只暴露已实现能力；跨版本降级读取。
- **DoD**：发布清单能说明每个 flag 的 owner、默认值、回滚动作和数据兼容性。
- **后端依赖**：部分；需要部署/配置系统确认。

### 10.3 Phase B：Workspace Web Team Quick Start 与 Team Studio（P0）

#### B-01 Team mode selector 与导航边界

- **目标**：把协作 Team、普通 subagent、独立并行、Fan-out 作为不同模式呈现。
- **路径/模块**：`frontend/src/components/workspace/runtime-teams.tsx`、`directory-view.tsx`、相关 dialog/navigation。
- **依赖**：A-01、A-05、A-06。
- **API/数据契约**：读取 capability 和已有 Team/Run；不因选择模式直接创建资源。
- **实现**：增加 Quick Start/Guided/Advanced 入口；保留现有 Details 和独立 Fan-out Dispatch；文案明确 Provision 的实际语义。
- **测试**：模式切换、深链、flag off、Fan-out regression、键盘和 screen reader 导航。
- **DoD**：用户不会把 Fan-out 行误认为一个多成员 Team；旧 Team Details 入口仍可用。
- **后端依赖**：无新后端，但依赖能力声明。

#### B-02 多成员 roster/session editor

- **目标**：在一个 Team draft 中增删编辑多个 teammate，并显示 session binding/readiness。
- **路径/模块**：新建 `frontend/src/components/workspace/runtime-teams/team-studio/members-editor.tsx`、API client/types。
- **依赖**：A-02、A-04、A-05、B-01。
- **API/数据契约**：teammate role/profile/capabilities、session source、heartbeat、readiness reason、writer limit。
- **实现**：草稿编辑与 server version reconciliation；已有 session、自动 provisioning、待绑定三种来源必须区分。
- **测试**：两成员保存、session offline/stale、重复角色、权限拒绝、并发编辑、撤销未提交草稿。
- **DoD**：至少两个成员可持久化到同一 Team/Run draft；UI 不把 roster 存在误报为 worker ready。
- **后端依赖**：部分；需要 readiness/teammate update 契约。

#### B-03 Task graph editor

- **目标**：在同一 Team 中编辑多个 task、assignee、depends_on、paths 和执行属性。
- **路径/模块**：新建 `team-studio/task-graph-editor.tsx`，复用 `task-queue-section.tsx`、`GetTaskGraph`。
- **依赖**：A-02、A-04、B-02。
- **API/数据契约**：graph version、task ID、assignee、difficulty/task type、read/write paths、dependency edge。
- **实现**：本地即时提示 + 服务端最终校验；cycle/unknown assignee/path conflict 不能只在浏览器判定。
- **测试**：自环、环、未知成员、跨 draft 依赖、批量编辑、版本冲突、保存/刷新恢复。
- **DoD**：可创建两 task 一 dependency；Preflight 能复核图；Start 后 snapshot 不被普通编辑覆盖。
- **后端依赖**：部分；现有 graph API 需补版本和校验返回。

#### B-04 Preflight、route preview 和启动确认

- **目标**：启动前给出可解释的 readiness、路径、route、权限和限制报告。
- **路径/模块**：新建 `team-studio/preflight-panel.tsx`、`route-preview.tsx`、`setup-panel.tsx`。
- **依赖**：A-04、A-05、B-02、B-03。
- **API/数据契约**：`PreflightReport`、effective routes、blocker/warning/info、current version。
- **实现**：报告按严重性和成员/task 分组；允许重新检查/跳转修复；只有 blocker 清零时启用 Start，除非 Runtime 明确允许 warning override。
- **测试**：所有检查等级、预览与实际 route 不同、网络失败 last-known-good、不可用动作。
- **DoD**：Start 按服务端报告被阻断/允许；UI 不自造 readiness 或 route。
- **后端依赖**：新建统一 API。

#### B-05 Start/Run Monitor 与 polling

- **目标**：从 Start 到 active、task progress、terminal 和 summary pending/ready 提供可恢复观察。
- **路径/模块**：`runtime-team-details-panel.tsx`、新建 `team-studio/run-monitor.tsx`、`use-team-details-loader.ts`、query hooks。
- **依赖**：A-03、A-05、A-06、B-04。
- **API/数据契约**：operation ID、run/team status、task status、event cursor、summary state、retryable error。
- **实现**：统一 cursor polling、AbortController、backoff、last-known-good、terminal 后 summary 短轮询；禁止 timeout 改状态。
- **测试**：刷新/重连、重复事件、终态先于 summary、404/429/503、unmount cancellation、Fan-out partial success。
- **DoD**：用户能从稳定 URL 恢复 monitor；观察窗口结束后再次进入仍能看到 Runtime 状态。
- **后端依赖**：部分；依赖 event cursor/summary contract。

#### B-06 Team Results、task inspector 和 Fan-out 对比边界

- **目标**：展示 task outcome、attempt、event、mailbox、artifact reference 和 final summary，同时不把 Fan-out 结果混成 Team Details。
- **路径/模块**：`runtime-team-details-panel/*`、`runtime-team-dispatch-panel/*`、新建 `results/task-inspector.tsx`。
- **依赖**：A-02、A-05、B-05。
- **API/数据契约**：outcome apply result、attempt lineage、artifact metadata/opaque refs、summary readiness。
- **实现**：复用现有 comparison rows、mailbox fallback 和 details sections；缺失 artifact 下载能力时只显示 reference/权限状态。
- **测试**：done/blocked/failed/handoff、missing summary、retry lineage、artifact forbidden、Fan-out comparison regression。
- **DoD**：结果页可追溯“哪个 attempt 产生什么结果”；未支持下载/accept 动作不出现。
- **后端依赖**：部分/待验证 artifact contract。

### 10.4 Phase C：aicli Chat/TUI 体验闭环（P0）

#### C-01 Team spawn 参数、帮助和结构化回执

- **目标**：让 TUI 明确创建/复用 Team、成员、task graph 和 session binding 的输入边界。
- **路径/模块**：`backend/internal/toolbroker/broker.go`、`team_tools.go`、`spawn_team_arg_types.go`、`chat_team_lifecycle.go`。
- **依赖**：A-01、A-03、A-05。
- **API/数据契约**：team_spawn response 包含 Team/Run ID、operation、event cursor、capability 和 preflight/start result。
- **实现**：保留 active Team context gating；render async launch notice；明确普通 subagent 不是 Team。
- **测试**：参数校验、no active context、multi-member payload、duplicate idempotency、structured response rendering。
- **DoD**：TUI 可完成受支持的 Team start；帮助不会暗示固定 slash command 已存在。
- **后端依赖**：已有 broker 基础，需统一 response。

#### C-02 team_wait cursor、timeout 和 summary pending

- **目标**：把 `team_wait` 变成可续接的观察窗口，并正确展示 terminal/summary 分离。
- **路径/模块**：`chat_actor_team_obligations.go`、`chat_team_drain.go`、`team_tools.go`、`broker_team_test.go`。
- **依赖**：A-03、A-05、B-05 的共享语义。
- **API/数据契约**：Team-local `after_seq`、wait outcome、terminal status、summary state、next cursor。
- **实现**：timeout 只返回观察结束；terminal 后检查 summary；重连用 last cursor；不调用普通 `subagent_wait` 管理 Team。
- **测试**：timeout while active、terminal without summary、summary delayed/failed、duplicate event、cursor resync。
- **DoD**：TUI 文案能区分“仍运行”“已终态但 summary pending”“summary ready”。
- **后端依赖**：已有 `team_wait`，需补 cursor/summary response contract。

#### C-03 Team task outcome、mailbox 和恢复动作

- **目标**：让 TUI 在 Team context 中报告/读取 task outcome，并提供受支持的 retry/replan 提示。
- **路径/模块**：`team_tools.go`、`chat_actor_executor.go`、task outcome broker wrappers、mailbox tools。
- **依赖**：A-03、A-05、C-02。
- **API/数据契约**：task/attempt IDs、合法 outcome status、apply result、retry/replan operation。
- **实现**：显示 Runtime 接受结果；迟到/冲突 outcome 显示拒绝原因；未支持 pause/cancel 不提供命令。
- **测试**：合法/非法 outcome、stale attempt、retry idempotency、mailbox ack、权限错误。
- **DoD**：TUI 不通过文字回复伪造任务完成；恢复动作可追踪 operation。
- **后端依赖**：现有 outcome/retry handlers，需 broker facade。

#### C-04 重连、失败反馈和长期运行安全

- **目标**：TUI 退出/重连、工具错误、后端不可用时不丢 Team 事实、不误报失败。
- **路径/模块**：`chat_actor_host.go`、`chat_actor_executor.go`、`chat_team_drain.go`、logging/metrics。
- **依赖**：C-02、C-03、A-06。
- **API/数据契约**：request ID、retryable error、last cursor、operation state。
- **实现**：bounded wait、重连 resync、不可恢复错误停止局部观察而非取消 Runtime；输出诊断信息。
- **测试**：进程重启、网络断开、broker timeout、event gap、operation still running。
- **DoD**：重新进入 Team context 后可继续观察同一 Run；没有重复 Start。
- **后端依赖**：部分；需要 operation/cursor 持久化或可恢复读取。

#### C-05 跨入口语义回归

- **目标**：验证 Web/TUI 同一 Runtime 资源的状态、outcome 和 summary 显示一致。
- **路径/模块**：backend broker tests、frontend fixture/contract tests、E2E harness。
- **依赖**：B-05、B-06、C-02、C-04。
- **API/数据契约**：共享 fixture 覆盖所有状态、error、cursor 和 summary state。
- **实现**：建立 golden resource fixtures，不复制执行逻辑。
- **测试**：同一 fixture 的 Web/TUI snapshot/semantic assertions、Fan-out separation。
- **DoD**：跨入口差异仅限呈现，不存在相互矛盾的生命周期解释。
- **后端依赖**：已有 API/broker contract。

### 10.5 Phase D：Embedded Team Experience（P1）

#### D-01 共享 Team shell 与 host adapter

- **目标**：把 Team Setup、Monitor、Results 抽成可嵌入容器，保留 Runtime 为事实源。
- **路径/模块**：新建 `frontend/src/components/.../team-experience/`（实际位置先按现有组件结构确认）、host adapter、context types。
- **依赖**：B-01 至 B-06。
- **API/数据契约**：显式传入 workspace/identity/capability/URL adapter，不把 host props 当授权。
- **实现**：shell 与业务 hooks 分离；支持 full page 和 embedded layout；统一 loading/error/empty/stale。
- **测试**：不同 host、缺失 context、无权限、卸载清理、尺寸/键盘。
- **DoD**：嵌入与 Workspace 页面使用相同 API 和 lifecycle；没有第二套状态机。
- **后端依赖**：无新后端，依赖能力和授权接口。

#### D-02 深链接、恢复和身份边界

- **目标**：支持 Team/Run/Task 深链接、刷新恢复和 host route 变化。
- **路径/模块**：embedded router/URL adapter、API query hooks。
- **依赖**：A-05、B-05、D-01。
- **API/数据契约**：稳定资源 ID、workspace scope、missing/forbidden/not-found 分类。
- **实现**：URL 不保存 secret；路由变更取消旧请求；加载后以 Runtime authorization 结果为准。
- **测试**：刷新、复制 URL、错误资源、跨 workspace、host unmount/remount。
- **DoD**：页面刷新不丢 Run 观察能力；未授权资源不泄露标题/路径。
- **后端依赖**：已有 resource auth，需验证。

#### D-03 Embedded subscription/polling 生命周期

- **目标**：嵌入多个 Team panel 时不产生重复轮询、泄漏或 cursor 交叉。
- **路径/模块**：shared polling/query layer、team experience hooks。
- **依赖**：B-05、D-01、D-02。
- **API/数据契约**：每个 Run 独立 cursor、AbortSignal、poll lease/visibility policy。
- **实现**：共享 cache、引用计数和卸载清理；不使用全局 Team cursor。
- **测试**：多实例、快速切换、隐藏/恢复、网络恢复、长时间运行。
- **DoD**：资源和网络请求随组件数量可预测，卸载后无后台请求。
- **后端依赖**：已有 polling API。

#### D-04 Embedded accessibility 和发布门槛

- **目标**：为宿主环境提供可访问、可降级、可 feature-gated 的 Team surface。
- **路径/模块**：team experience components、design system、release checklist。
- **依赖**：D-01 至 D-03。
- **API/数据契约**：capability 缺失时的 fallback view，不把 unsupported 当 error。
- **实现**：键盘顺序、焦点恢复、状态 live region、窄屏布局、错误重试。
- **测试**：a11y lint/axe（若项目已有）、键盘/屏幕阅读器、host feature flag。
- **DoD**：嵌入入口在能力不足时仍能安全查看已有 Team Details；不暴露未实现动作。
- **后端依赖**：无新后端。

### 10.6 Phase E：可靠性、恢复、历史和可观测性（P1）

#### E-01 Attempt/outcome/artifact lineage

- **目标**：形成 task -> attempt -> outcome -> artifact -> summary 的可审计链。
- **路径/模块**：`backend/internal/team/task_outcome_contract.go`、`task_outcome_apply.go`、store、Web Results。
- **依赖**：A-02、A-03、B-06。
- **API/数据契约**：attempt IDs、producer、route audit、outcome apply result、artifact metadata/opaque refs。
- **实现**：持久化拒绝/迟到报告事件；补 result inspector；下载/retention 未确认前只读引用。
- **测试**：retry lineage、late report、missing artifact、permission, SQLite replay。
- **DoD**：每个最终结果可定位到 accepted attempt；artifact 能力边界在 UI 明确。
- **后端依赖**：部分/待确认 artifact store。

#### E-02 Retry/replan/reclaim 并发安全

- **目标**：在失败、阻塞、lease 过期和 graph 变化时安全恢复。
- **路径/模块**：task handlers、orchestrator、path claims、broker/Web mutations。
- **依赖**：A-03、A-04、E-01。
- **API/数据契约**：expected task/graph version、new attempt、impact list、reclaim reason。
- **实现**：Runtime fencing、幂等 action、明确不可 retry 状态；UI 展示影响范围。
- **测试**：双 retry、retry 与旧 outcome race、replan 与 running task、lease expiry、worker restart。
- **DoD**：不会出现两个有效 attempt 争写同一 task/path；失败恢复可回放。
- **后端依赖**：已有 handlers，需补一致性测试和字段。

#### E-03 SQLite restart/replay 与迁移演练

- **目标**：验证重启、升级、降级/备份恢复下 Team 事实不丢失。
- **路径/模块**：`sqlite_store.go`、migration tests、runtime startup/health。
- **依赖**：A-02、E-01、E-02。
- **API/数据契约**：schema version、event sequence、snapshot/attempt/outcome foreign keys。
- **实现**：fixture migration、故障注入、reopen/replay、旧数据兼容。
- **测试**：空/大/中断迁移、重复 event、terminal/summary pending、backup restore。
- **DoD**：发布前有可重复演练报告；失败时服务安全拒绝写入而不是静默降级。
- **后端依赖**：新 schema/migration。

#### E-04 Run History、Clone config 和 Run again

- **目标**：在 Definition/Run 语义稳定后提供可解释历史和重跑入口。
- **路径/模块**：Run list/history API、Web directory/results、Definition store。
- **依赖**：A-02、E-01、E-03；不得提前于兼容迁移。
- **API/数据契约**：独立 Run ID、snapshot version、parent_run/clone source、retention/archived state。
- **实现**：Clone 生成新 draft；Run again 生成新 Run；历史只读不修改原 snapshot。
- **测试**：Definition 修改不污染旧 Run、权限过滤、归档、分页、跨 Run 对比。
- **DoD**：每个历史条目能解释来源和配置版本；没有 legacy compatibility 时不展示强保证历史。
- **后端依赖**：待确认/新建。

#### E-05 运行观测、告警和负载基线

- **目标**：量化 preflight/start/claim/outcome/summary/polling latency 和失败率。
- **路径/模块**：Runtime metrics/logging、Web telemetry（如已有）、load/failure harness。
- **依赖**：A-06、B-05、C-04、E-02。
- **API/数据契约**：request/operation/run/task/attempt correlation；敏感字段脱敏。
- **实现**：指标、structured logs、slow poll/summary pending 告警；不把用户文本作为高基数 label。
- **测试**：负载、限流、网络抖动、长 summary、并发 Team。
- **DoD**：有容量/超时预算和告警阈值；故障能按 request/Run 定位。
- **后端依赖**：部分；依赖部署观测栈。

### 10.7 Phase F：Advanced Live Operations（P2，条件交付）

#### F-01 Team pause/resume/cancel 状态机

- **目标**：仅在后端定义并验证动作语义后，提供可恢复的 live operation。
- **路径/模块**：`team_lifecycle.go`、supervisor、runner、API/UI/TUI action surfaces。
- **依赖**：A-03、E-02、E-03；必须先关闭开放问题 O-01。
- **API/数据契约**：合法 transitions、operation/ack、running session stop、lease release、restart semantics。
- **实现**：服务端先实现/审计状态机；客户端只展示 capability；不可用时不显示按钮。
- **测试**：每个状态 transition、重复 cancel、pause 后重启、worker lease、summary/terminal。
- **DoD**：Runtime 可在重启后恢复动作语义；负向测试证明任意 status PATCH 不能绕过。
- **后端依赖**：待确认/新建。

#### F-02 Budget enforcement 和超限恢复

- **目标**：只有能真正阻止新工作并回收资源时，才提供 hard budget control。
- **路径/模块**：RunMeta/usage accounting、orchestrator、session/tool/model adapters、UI warnings。
- **依赖**：A-04、E-02、E-05；必须关闭 O-02。
- **API/数据契约**：预算维度、消耗账本、超限 event、hard/soft policy、recovery reason。
- **实现**：Runtime 统一计量、持久化和 fencing；前端只呈现服务端 policy。
- **测试**：并发/重试/重启超限、model/tool 计费缺失、hard stop 后新 attempt 被拒绝。
- **DoD**：通过负向 enforcement test 后才能使用“保证不超”的产品文案。
- **后端依赖**：待确认/新建。

#### F-03 Streaming（SSE/WebSocket）评估与可选实现

- **目标**：在 polling 语义稳定后评估是否需要 streaming，且不破坏 cursor/replay。
- **路径/模块**：Runtime event transport、Web/TUI subscription layer、deployment proxy。
- **依赖**：E-05、B-05、D-03；必须先关闭 O-03。
- **API/数据契约**：连接认证、事件 sequence、重连/resync、backpressure、fallback polling。
- **实现**：先做成本/并发/代理兼容性评估；若实施，stream 仍以事件 cursor 为事实源并保留 polling fallback。
- **测试**：断线、乱序、重复、慢消费者、代理超时、权限撤销、fallback。
- **DoD**：没有 SSE/WebSocket 也不阻塞 MVP；启用后不改变 Team/Task 状态语义。
- **后端依赖**：未来阶段/待确认。

### 10.8 推荐执行顺序与并行边界

1. A-01 先行；A-02/A-03 可在术语冻结后并行设计，但 schema 先于迁移实现。
2. A-04 与 A-05 必须完成 contract review 后再实现 B-02/B-04；Web 可以先做只读壳，但不能接假 Start。
3. B-01、A-06 可并行；B-02/B-03 使用相同 draft/version contract，避免两个独立保存模型。
4. B-05 与 C-02 共享 cursor/polling fixture；不要让 Web 和 TUI 各自发明 timeout 语义。
5. E 阶段只能在 A/B/C P0 验收完成后扩大历史和恢复能力；F 阶段的每项都是独立 gate，不因计划日期自动进入发布。
6. Fan-out 相关改动只修改 dispatch surface 和其 API facade；不得把 fan-out 临时改写为 Team Studio 的内部实现。

## 11. Backend / Runtime 改造明细

### 11.1 Domain 与 store

- 明确 `team.Team` 在迁移期间代表 legacy Team/Run view，避免调用端擅自把它解释为 Definition。
- 把合法状态转移封装为 domain/service action，不允许 handler 直接写任意 status。
- 为 Definition、Run snapshot、attempt、outcome application、summary state 增加 schema version 和持久化边界；是否拆独立表由 A-02 决定。
- 对 task graph 提供原子写入或版本 fencing：一批 task/edge 要么全部成功，要么全部拒绝。
- 在 SQLite store 中建立必要索引和外键约束；对 event sequence、attempt number、idempotency key 设唯一性边界。
- 继续复用现有 store interface，不让 Web/handler 依赖 SQLite 实现细节。
- 添加 migration lock/transaction，迁移失败时不启动新写操作；健康检查报告 schema version 与 migration status。

### 11.2 Lifecycle service 与 orchestrator

- 将 Start/Retry/Replan/Outcome/Cancel 等 operation 先在 service 层定义输入、合法状态、并发条件和持久化事件，再接 handler/tool broker。
- 对每个 operation 记录 requested/accepted/rejected/completed；长操作返回 operation ID，可从新连接读取状态。
- `team.Orchestrator` 保持 task ready、dependency、claim、lease 和 terminal reconciliation 真相；客户端不能重实现这些规则。
- Worker session actor 无响应或 lease 超时，应通过当前 reclaim/reconciliation 机制处理；不通过 Web 直接把 task 改回 pending。
- Attempt 与 outcome 使用 fencing：当前 task attempt/version 与报告匹配才 apply；迟到报告记录拒绝原因及 event。
- 对 `team_wait` 定义 timeout、cursor expired、team terminal、summary pending 的独立结果形状。

### 11.3 Runtime API

- 先审查 `handler.go` 路由注册和 `team_handlers.go` 具体 handler，再对外增加 endpoints；文件中存在方法不等于路由已注册。
- API 支持 capability document 或资源 capability 字段，明确受支持的 lifecycle actions、Preflight、Run view、attempt/outcome/artifact read scope。
- Readiness/Preflight 必须调用 session registry、route resolver、path claim manager 和 permission layer 的 Runtime 接口；不能从 Web 提交的“ready=true”信任。
- List/events 支持稳定分页和 cursor；资源删除/归档的 tombstone 与引用保留策略先于 DELETE API。
- 所有敏感读写路由进行 owner/workspace authorization，并在 API integration test 中覆盖跨 workspace ID 猜测。
- 对旧 API 保持兼容窗口：deprecated header/metric/日志记录 caller，避免未通知地改变 status patch、event cursor 或 outcome contract。

### 11.4 路由、权限与审计

- Route preview 和 actual route 调用同一 resolver 版本；preview 不承诺执行时 provider 一定可用。
- `TaskExecutionRoute` 或等价 audit 记录 effective route、fallback、resolution time、resolver version；敏感 provider detail 按权限脱敏。
- 权限校验在 handler/service 两层复核，异步 worker 启动时再次确认有效权限策略。
- path claim acquire/renew/release/reclaim 都写入可关联 Team/Run/task/attempt 的事件。
- Tools/profile 权限应与 session 实际启动配置一致；配置变更后旧 session 是否失效需由 Phase A 确认。

## 12. Web 前端改造明细

### 12.1 页面与信息架构

推荐入口结构：

```text
Workspace > Teams
  ├─ Directory / existing Team Details
  ├─ Create collaboration Team (Team Studio)
  │    ├─ Quick Start
  │    ├─ Guided setup
  │    └─ Advanced task graph
  ├─ Runs (only after Definition/Run API is stable)
  └─ Fan-out Dispatch (separate product surface)
```

- Team Directory 保持现有 Team 详情和操作；新增入口不能要求用户先懂全部运行时术语。
- Quick Start 只询问目标、最少成员/角色和一组简单工作项；仍调用 Runtime Preflight，不能绕过规则。
- Guided setup 展示成员、task graph、route/path readiness；Advanced 才提供完整属性编辑。
- Fan-out 独立保留：Provision 当前创建 N 个单成员 Team；Dispatch 是多 Team partial success；导航和结果标题应使用批次/隔离比较术语。
- Run History 要等 E-04 的稳定 schema 和授权后再展示；当前 Team Directory 不能伪装为历史列表。

### 12.2 建议组件边界

在确认旧/新 details panel 引用后再落文件，建议职责：

```text
runtime-teams/
  team-studio/
    setup-panel.tsx
    members-editor.tsx
    task-graph-editor.tsx
    route-preview.tsx
    preflight-panel.tsx
  run-monitor/
    run-monitor.tsx
    event-timeline.tsx
    task-progress-list.tsx
    recovery-actions.tsx
  results/
    results-overview.tsx
    task-inspector.tsx
    attempt-history.tsx
    artifact-references.tsx
```

职责约束：

- `setup-panel` 管理本地 draft，调用统一 mutation hooks，不持有 Runtime lifecycle 状态机；
- `members-editor` 只编辑 roster/config，不把 session ID 存在等价于 readiness；
- `task-graph-editor` 可本地做快速 cycle hint，但 server Preflight 结论优先；
- `run-monitor` 订阅共享 query/polling layer，按 cursor 去重；
- `results` 只读 Outcome/Attempt/Artifact/Summary，不在浏览器重新解释 terminal。

### 12.3 数据获取、状态和错误

- 以当前 `useTeamDetailsLoader` 的并行 partial load 为基础，提取共享 Team query facade；旧 panel 不再单独发不同 shape 的重复请求。
- 每个 section 独立加载、独立 stale/error/retry；保持 last-known-good，不用失败产生的空数组清空用户正在看的资料。
- 资源切换时使用 AbortController/query cancellation；响应带版本，忽略比当前缓存旧的迟到响应。
- 草稿的 dirty fields 与 server snapshot 分离；发生版本冲突保留草稿并显示差异。
- 所有 async action 防止双击/重复提交，但防抖不替代服务端幂等。
- 组件卸载和 route change 取消 poll；刷新后从稳定 URL 重新取 server state。

### 12.4 可用性、无障碍与安全呈现

- 每个 blocker 指向可修复的成员/task/路径/权限控件，并可键盘访问。
- 长任务状态提供可读文本和 live region，但避免每次 poll 都播报；终态和 summary ready 单独播报。
- 状态不仅靠颜色表示；blocked、stale、summary pending、unsupported、permission denied 用文字/icon/aria 状态呈现。
- 复制日志/结果前按权限做服务端过滤；不在前端日志泄漏完整路径、token 或 provider secret。
- 删除、取消、重试等有副作用的动作必须显示对象范围、attempt/version 和确认结果；未实现能力不绘制可点击假控件。

## 13. aicli TUI 改造明细

### 13.1 生命周期编排

- 保持 `team_spawn`、`team_wait`、Team task/mailbox tools 为 Team 协作入口；普通 subagent tool 继续独立。
- spawn 回执提供结构化 Team/Run ID、状态、operation ID、cursor、preflight blockers 和 capabilities；后台启动提示必须说清“运行已提交”和“可继续观察”。
- `team_wait` 支持有限观察窗口、从上次 cursor 续读、summary pending 单独提示；超时后让用户可再次 wait，不要自动生成 failed。
- Team terminal 后，只有 `summary_state=ready` 才展示 final summary；summary failed 提示独立 retry/read action（只有后端支持时）。

### 13.2 TUI 状态展示和错误恢复

- 用结构化状态行展示每个 task 的 status、assignee、attempt、last event 和阻塞原因；避免仅靠自然语言叙述。
- 对 `409 conflict`、`403 forbidden`、`429/503 unavailable`、unsupported capability 给出不同说明和下一步。
- 退出/重新进入对话后从服务端恢复 Team ID/cursor；本地聊天 history 不作为唯一 Team 状态存储。
- 长运行操作使用 bounded wait 与 request cancellation；取消本地等待不自动取消后台 Team。
- 诊断/help 能说明当前 context 可用的 Team tools；没有 active Team 时正确解释工具受限。

### 13.3 测试边界

- 更新 `broker_team_test.go`、Team lifecycle/chat actor tests 覆盖 spawn/wait cursor/timeout/summary/outcome。
- 用同一 contract fixture 验证 TUI 展示和 Web 状态语义一致。
- 测试普通 `subagent_spawn` obligation 与 Team obligation 并存时各自 wait/drain 不交叉消费。
- 任何新增固定 slash command 必须先实现解析、帮助、权限、错误、兼容和测试；不能在文档/UI 先宣称有此命令。

## 14. Embedded Team Experience

### 14.1 集成模型

嵌入入口复用 Team Studio/Monitor/Results 组件和统一 API hooks，以小型 host adapter 提供：

- workspace/resource ID；
- 用户 identity token provider（不直接传明文 token 到 URL）；
- navigation/URL builder；
- telemetry callback；
- viewport/layout capability；
- feature capability 和产品上下文。

Host adapter 只决定呈现与导航，不决定授权和执行状态。所有读写仍通过同一 Runtime API，并由服务端校验身份、workspace 和资源权限。

### 14.2 隔离与生命周期

- 每个 embed instance 有独立 routing scope、query key、event cursor 和 abort lifecycle；共享 cache 时按 authenticated identity/workspace 分区。
- host token 刷新、logout 或 workspace 切换时清理敏感 cache，取消在途请求。
- 对 iframe/跨域场景需单独威胁建模 CSP、postMessage origin、cookie/token 生命周期；未完成审查前不作为默认集成。
- 嵌入布局支持狭窄宽度和有限高度；仅隐藏非关键导航，不隐藏 blocker、权限提示或 stale state。

### 14.3 发布要求

Embedded 功能在 Phase D feature flag 下灰度；先内部宿主，再有限 workspace，最后默认启用。每阶段观察错误率、重复轮询数、权限拒绝、未处理 promise 和卸载后的活动请求。出现安全泄露、错 workspace 或状态交叉立即关闭 flag。

## 15. 测试计划

### 15.1 单元测试

**Backend/domain：**

- Team/Task 状态转移合法与非法表；
- graph cycle、自依赖、assignee 和 version fencing；
- preflight check code/severity/remediation；
- idempotency key normalization 和请求体绑定；
- terminal/summary 独立状态；
- attempt/outcome apply/rejected late report；
- route preview/actual audit 脱敏；
- path canonicalization 和 claim lease fencing。

**Frontend：**

- reducer/state mapping 不把 observation timeout 变成 failure；
- details loader partial failure/last-known-good；
- cursor 去重、resync、backoff、abort；
- draft conflict、graph editor、本地 blocker presentation；
- unsupported capability 与权限错误显示；
- Fan-out partial success 和多 Team 隔离。

### 15.2 API contract tests

- 每个注册路由与 OpenAPI/Go/TypeScript fixture 对齐；
- request/response schema 含版本、cursor、request ID 和 error code；
- 每个 operation 重复请求、stale version、unauthorized、not found、unsupported；
- status patch 不可绕过 lifecycle action；
- event cursor、resync required、分页稳定性；
- outcome、attempt 和 summary state 保持现有合法枚举。

### 15.3 Runtime integration tests

使用 fake session actor、fake route resolver、SQLite 临时库和 deterministic clock 验证：

1. 创建一个 Team/Run；
2. 两个 teammate 绑定两个可用 worker session；
3. 创建两个有依赖 task；
4. Preflight blocker/warning/pass 三种结果；
5. Start 后 orchestrator 只运行 ready task；
6. dependency 满足后第二个 task 被 claim；
7. structured outcome 被 apply，attempt 和 result ref 可查询；
8. Team terminal 与 summary ready 可分别观察；
9. Retry/Replan/lease reclaim 在重复请求和竞争下不生成重复写入；
10. SQLite close/reopen 后 snapshot/event cursor/outcome/summary 恢复。

### 15.4 Security 与权限负向测试

- 未授权主体 list/get/update/start/retry/read events/outcome/artifact；
- 跨 workspace 资源 ID 猜测和 IDOR；
- 直接 API 调用绕过 UI；
- teammate session 不能访问未授权路径；
- task payload 试图指定 provider/model/tool policy；
- artifact URI traversal、敏感 header、path disclosure；
- permission 被撤销后异步 worker、新 polling 请求和 embed cache 的处理；
- 未验证 budget enforcement 时不能启动 hard-stop capability。

### 15.5 Web/TUI E2E 与稳定性

- Web：模式选择 -> 两成员 Team -> 两任务依赖 -> Preflight -> Start -> 刷新恢复 -> terminal -> summary；
- TUI：`team_spawn` -> `team_wait` timeout -> 再次 wait -> terminal/summary；
- Web/TUI 对同一 Fixture 显示一致的 status/outcome/summary；
- Fan-out 多 Team partial success，逐 Team 查看、比较，不合并 roster；
- 断网、恢复、重复/乱序 event、浏览器刷新、TUI 退出重连；
- 键盘导航、可读状态、窄屏以及长时间运行 poll 负载。

### 15.6 CI 与门禁

- P0 每个 Phase 至少有后端/API 和对应入口的自动化测试；
- schema migration 变更需 SQLite migration/reopen 测试通过；
- API route change 需 contract fixture 更新和兼容说明；
- 安全负向测试任何一项失败都阻断发布；
- 只读文档/代码核对不能代替真实 Runtime integration 和多进程测试；
- 所有测试命令应明确范围和副作用，避免修改无关生成文件或 memory/cache 工作区内容。

## 16. 发布、迁移与回滚

### 16.1 发布阶段

1. **内部 contract 阶段**：A-01 到 A-05 完成；新接口仅对内部测试和 feature flag 暴露；数据库 migration 在备份副本和 fixture 上演练。
2. **只读灰度阶段**：启用 Team/Run view、readiness 和 Preflight，但不开放尚未验证的 lifecycle action；核对 legacy Team Details 与新视图一致。
3. **受限写入阶段**：为内部 workspace 开启 Team Studio Start/Outcome/Retry；监控重复 operation、版本冲突、task stuck、event gap 和 summary pending。
4. **TUI 同步阶段**：开启结构化 `team_spawn/team_wait` 回执和恢复语义，使用共享 contract fixture 证明与 Web 状态一致。
5. **扩大灰度**：逐 workspace/用户组扩大；每阶段观察至少覆盖一个完整长运行周期和一次服务重启/恢复演练。
6. **P1/P2 单独放量**：Embedded、History、pause/cancel、budget hard stop 和 streaming 各自独立 flag，不以 P0 发布作为自动开启条件。

具体灰度百分比和观察时长由发布负责人结合部署环境确定；本计划不臆造固定 SLO 或天数。

### 16.2 Schema 与 API 兼容

- migration 必须向前兼容旧 Web/TUI/runtime 版本的读取窗口，或通过部署顺序保证旧 caller 不再访问；
- 若使用 additive schema，旧字段的含义不得改变；若必须改变，提供新版本字段/endpoint；
- Run snapshot schema 独立版本化，读取器支持历史版本或返回明确 unsupported；
- API deprecation 记录调用方、最后使用时间和删除门槛；
- release notes 标明能力 flag、兼容映射、已知限制、回滚方式和数据影响；
- rollback 前确认新写入数据是否可被旧版读取；不可读时采用 feature disable + forward fix，不盲目降级二进制。

### 16.3 回滚与故障止损

满足任一条件时暂停灰度或关闭相关 feature flag：

- 未授权读写或跨 workspace 信息泄露；
- 同一 Start/Retry 因重放生成多个有效运行/attempt；
- task/path 双重写入、lease fencing 失效；
- event cursor 丢失导致 monitor 显示与 Runtime terminal 状态矛盾；
- migration 启动后无法安全 reopen/replay；
- TUI/Web 把 observation timeout 显示为 Team/task failed；
- summary/artifact 错误泄露其他用户内容；
- poll 请求持续堆积或嵌入卸载后仍访问资源。

止损顺序：关闭新入口 flag -> 保留只读 legacy Details/Run view -> 停止新的写操作但允许已提交 operation 在 Runtime 安全收敛 -> 导出 request/operation/event 诊断 -> 修复并通过回归测试 -> 小流量恢复。不得为了回滚而删除历史 Team/task/event/outcome 数据。

## 17. 风险、依赖与待确认问题

| ID | 问题/风险 | 影响 | 决策/验证要求 | 默认处理 |
| --- | --- | --- | --- | --- |
| O-01 | Team pause/resume/cancel 的服务端状态转移、session stop、lease 和 restart 语义未确认 | 可能出现状态显示 paused 但 worker 仍写入，或 cancel 不可恢复 | 在 supervisor/runner 上写状态机 contract 和故障注入测试 | Phase F 前不开放按钮/API |
| O-02 | `RunMeta.BudgetTokens` 等预算字段是否参与统一实时计量和 hard enforcement 未确认 | 可能误导预算保证、并发越限 | 追踪 model/tool/session usage，验证重启账本、并发和超限拒绝 | 只显示非保证性信息，不承诺 hard cap |
| O-03 | Team event streaming 的负载、认证、代理和重连策略未知 | 可能增加连接、丢事件或 cursor gap | 在 polling 基线稳定后压测并评估 SSE/WebSocket | P0 使用 cursor polling |
| O-04 | Definition/Run 独立资源是否需要数据库拆分 | 影响迁移、history、clone/run again | Phase A 数据模型 review，评估 legacy Team 映射、ID 和 snapshot | 先定义兼容 facade，不伪称已分离 |
| O-05 | worker session provisioning 属于本 Runtime 还是外部 host | builder readiness、自动化程度和权限边界受影响 | 明确 provisioning owner、失败恢复、重绑和租户 scope | 支持已有 session/待绑定，不承诺自动 provision |
| O-06 | Attempt 是否已有足够的可查询持久化历史 | retry lineage 和结果审计可能缺失 | 检查 SQLite schema、retry handler、outcome apply 和 restart test | 当前计数只作为字段，不当作历史表 |
| O-07 | Artifact URI、下载授权、大小/保留/删除策略未统一 | 结果打不开或敏感数据泄露 | 定义 Artifact metadata/provider、permission、retention contract | 只展示 opaque ref，不做下载按钮 |
| O-08 | 旧 `team-details-panel` 与实际使用的 `runtime-team-details-panel` 并存 | 改错组件树、重复 API/状态 | B-01 前从 imports/routes/test 查明真实引用，制定删除或兼容策略 | 只在活动入口对应组件实现 |
| O-09 | API handler 存在不代表 router 已注册，也不代表跨 workspace authorization 完整 | UI 可调用失败或产生 IDOR | 检查 handler.go 注册、middleware 和 integration tests | 以注册路由 + auth test 为准 |
| O-10 | Team-only tool context 与普通 subagent obligation 共存时 drain/wait 交互复杂 | 错误等待或丢失 child/Team 结果 | 用 actor integration test 覆盖 mixed obligations | 维持不同 lifecycle 和独立 cursor |
| O-11 | SQLite 单库迁移与多实例运行的一致性约束未明确 | event sequence、idempotency 和 lease 竞争风险 | 确认部署实例数、事务/锁策略和 store sharing | 在部署模型确认前不作多实例保证 |
| O-12 | Frontend 现有 API client 的 error/cache/query 约定可能与目标 envelope 不同 | 形成第二套请求层和 stale data bug | 先审查既有 runtime client、query library 和错误组件 | 扩展既有约定，避免新建平行 transport |
| O-13 | Team plan 中的 product Definition 与 Agent/profile catalog 的边界 | 重复维护 profile/capability/routing 信息 | 明确 Definition 存引用还是快照，权限谁拥有 | Definition 存引用，Run snapshot 存有效摘要（待确认） |
| O-14 | 高负载 polling 在 Fan-out 与 Embedded 多实例下放大请求量 | Runtime 负载和限流 | 以真实并发和观察窗口压测，增加共享 polling/cache 和 backoff | P0 bounded polling，不启用 streaming 猜测方案 |

## 18. 逐项验收标准

### 18.1 P0：最小 Team 产品闭环

#### 产品与模式

- [ ] 用户可以辨别普通 subagent、协作 Team、独立并行和 Fan-out 的用途与对象关系。
- [ ] Web Fan-out Provision 明确说明其创建多个独立 Team；单个 Team Studio 能创建多个 teammate。
- [ ] TUI 不把普通 child wait 用于 Team；Team 使用 `team_wait` 和 Team-local cursor。

#### Runtime/API

- [ ] 能读取 Team/Run capability、状态、版本、event cursor 和 summary readiness。
- [ ] 两个 teammate、两个 task 和 dependency 能持久化并通过 Runtime Preflight。
- [ ] Start 有幂等和版本冲突语义；重复提交不会创建重复运行。
- [ ] 任务 claim、dependency、path claim、终态由 Runtime 执行和测试。
- [ ] structured outcome 可应用、查询并关联 attempt/result reference。
- [ ] 未实现 pause/cancel/budget/streaming 以 unsupported 或 capability gate 明确阻断。

#### Web

- [ ] Team Studio 可完成 roster/task graph 配置和 Preflight；blocker 可定位修复。
- [ ] Start 后从稳定 URL 进入 Run Monitor，刷新后可恢复。
- [ ] Events 按 cursor 去重；断网/timeout 不更改 Team/task 状态。
- [ ] Terminal、summary pending、summary ready、summary failed 有不同展示。
- [ ] Details 部分接口失败保留 last-known-good 并显示 stale/error。
- [ ] Results 能查看 task outcome 和已存在的 attempt/result reference；未支持能力不显示伪按钮。

#### TUI

- [ ] `team_spawn` 显示结构化创建/启动回执和 Team ID。
- [ ] `team_wait` timeout 表示观察结束；用户可以续等。
- [ ] terminal 后独立观察 summary readiness；失败信息包含 request ID。
- [ ] 重连/重开后可继续读取同一 Team 状态，而非依赖内存中的 wait 进程。

#### 安全与可靠性

- [ ] 未授权 API/broker 请求被 Runtime 拒绝；跨 workspace 访问通过负向测试。
- [ ] Start/Retry/Replan/outcome race 有幂等或版本 fencing。
- [ ] SQLite reopen 后状态、snapshot、event cursor、attempt/outcome 仍一致。
- [ ] Path claim conflict 和 lease reclaim 不产生双写；route preview 不泄露 secret。
- [ ] P0 安全、contract、Runtime integration 和 Web/TUI E2E 门禁均通过。

### 18.2 P1：Embedded 与历史/恢复

- [ ] Embedded shell 与 Workspace 共用 API/types/polling，host adapter 不授权。
- [ ] URL deep link、refresh、logout、workspace switch 正确处理权限和 cache 清理。
- [ ] Run History 使用独立稳定 Run ID 和不可变 snapshot，不能从 Definition 当前值推导过去。
- [ ] Clone config/Run again 创建新 draft/run，不覆盖原始 Run。
- [ ] Retry/replan/attempt/artifact lineage 可审计；artifact 权限和 retention 有正式 contract。
- [ ] 迁移演练、备份恢复和生产灰度具有记录和回滚 gate。

### 18.3 P2：Advanced Live Operations

- [ ] Pause/resume/cancel 状态机经过 Runtime 故障/重启/lease 测试，并可跨入口一致观察。
- [ ] Budget hard stop 有 Runtime 端负向 enforcement 测试，重启和重试不绕过。
- [ ] Streaming 若实施，支持认证、cursor replay、backpressure、resync 和 polling fallback。
- [ ] 任一高级能力未达到对应验收项时保持 disabled，不影响 P0/P1 使用。

## 19. 当前验证边界

本实施文档完成时的状态是“基于代码静态检查整理出的实施方案”，不是实现验收。已核对 Team store/domain、Runtime API、broker/TUI、Workspace Web 入口、现有测试和 route preview surface；但下列内容仍需动态测试或负责人确认：

- 所有 Team handler 的最终 router 注册和 middleware 覆盖；
- 真实运行环境下 worker session readiness、provisioning、heartbeat 和 offline recovery；
- SQLite 多进程访问/事务隔离与 migration 失败恢复；
- pause/cancel/resume 的 session、lease、reconciliation 行为；
- provider/tool usage 的预算实时计量和 hard enforcement；
- artifact storage/download/retention/auth 的完整路径；
- route preview 与实际运行 route 的一致性及 fallback audit；
- 跨 workspace 负向授权测试；
- 端到端两成员、两任务、有依赖 Team 的真实 Web/TUI 长运行；
- 实际部署下 polling 容量、限流、proxy 和 browser/tab 生命周期。

在这些验证完成前，本文件中的目标 endpoint、resource shape、migration 和 phase deliverable 均是工程建议/验收契约，不应作为已经上线的功能说明。

## 20. 参考实现位置

### Team domain、store 和 orchestrator

- `backend/internal/team/types.go`
- `backend/internal/team/store.go`
- `backend/internal/team/sqlite_store.go`
- `backend/internal/team/orchestrator.go`
- `backend/internal/team/teammate_runner.go`
- `backend/internal/team/terminal_state.go`
- `backend/internal/team/task_outcome_contract.go`
- `backend/internal/team/task_outcome_apply.go`
- `backend/internal/team/run_meta.go`
- `backend/internal/team/events.go`
- `backend/internal/team/path_claims.go`
- `backend/internal/teamsupervisor/`

### Runtime API 与 routing

- `backend/internal/api/runtimeapi/team_handlers.go`
- `backend/internal/api/runtimeapi/team_lifecycle.go`
- `backend/internal/api/runtimeapi/handler.go`
- `backend/internal/api/runtimeapi/team_task_outcome_handler_test.go`
- `backend/internal/api/runtimeapi/config_document_handlers_test.go`
- `backend/internal/modelrouting/`
- `backend/internal/api/runtimeapi/` 下 Team/route preview 相关路由与测试

### TUI / tool broker

- `backend/internal/toolbroker/broker.go`
- `backend/internal/toolbroker/team_tools.go`
- `backend/internal/toolbroker/spawn_team_arg_types.go`
- `backend/internal/toolbroker/broker_team_test.go`
- `backend/cmd/aicli/commands/chat_team_lifecycle.go`
- `backend/cmd/aicli/commands/chat_actor_team_obligations.go`
- `backend/cmd/aicli/commands/chat_actor_agent_obligations.go`
- `backend/cmd/aicli/commands/chat_actor_host.go`
- `backend/cmd/aicli/commands/chat_actor_executor.go`
- `backend/cmd/aicli/commands/chat_team_drain.go`

### Workspace Web

- `frontend/src/api/runtime/teams.ts`
- `frontend/src/types/runtime/teams.ts`
- `frontend/src/hooks/workspace/use-runtime-teams-data.ts`
- `frontend/src/components/workspace/runtime-teams.tsx`
- `frontend/src/components/workspace/runtime-teams/directory-view.tsx`
- `frontend/src/components/workspace/runtime-teams/runtime-team-details-panel.tsx`
- `frontend/src/components/workspace/runtime-teams/use-team-details-loader.ts`
- `frontend/src/components/workspace/runtime-teams/use-runtime-team-dispatch.ts`
- `frontend/src/components/workspace/runtime-teams/use-runtime-team-dispatch/`
- `frontend/src/components/workspace/runtime-teams/runtime-team-dispatch-panel.tsx`
- `frontend/src/components/workspace/runtime-teams/runtime-team-details-panel/`
- `frontend/src/components/workspace/runtime-teams-dialog.tsx`

### 上游计划文档

- `docs/plan/team-product-closure-and-usage-plan-20261122.md`
- `docs/plan/team-product-closure-gap-review-and-roadmap-20261122.md`
