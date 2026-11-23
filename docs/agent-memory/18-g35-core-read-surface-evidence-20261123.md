# G3.5 Core Memory 读取、管理与候选重建切片证据（2026-11-23）

## 状态

**部分实施；G3.5 仍未关闭。** 本切片让已经人工审核并激活的 project Core 可通过只读 Agent 工具、HTTP GET 和 v2 stable context stage 读取，并在每次读取时重新校验 Core 来源；另提供 admin-only candidate/history 列表、显式激活与归档 API，以及本机 CLI 的候选列表、显式激活、历史归档和有界候选重建入口。当前 rebuild 只从安全的 active summary、pinned stable records 和近期 usage 记录生成待审核 candidate，不会自动激活；后台 maintenance scheduler 已支持显式 opt-in 的周期触发。分类预算当前只完成配置、selector accounting 和预算裁剪原因的最小切片；Core 专属 usage/exposure 已有独立基础落账、读取和保留期清理，但完整闭环仍未完成。

所有数据库验证都通过 Go 测试使用的 `t.TempDir()` 隔离 workspace 完成；没有手工访问、迁移、备份或修改真实 `.aicli/memory/memory.db`。

## 实施内容

- `backend/internal/memorystore/v2_core.go`
  - `GetActiveCore` / `GetCore` 读取 active Core 时，在只读事务中重新校验全部来源仍属于当前 project，且为 active/fresh、非 private、未归档/删除/过期、无 secret-shaped 内容。
  - 来源不再符合条件时以 `ErrCoreSourceIneligible` fail closed，不返回 Core 内容。候选激活时已有的事务内来源复核与 admin 显式激活边界不变。
- `backend/internal/toolkit/tools/memory_tool_defs.go`、`memory_tools.go`、`backend/internal/tools/builtin_catalog.go`、`backend/internal/toolregistry/registry.go`、`backend/internal/policy/taxonomy.go`
  - 新增只读工具 `memory_core_get`，只返回 active Core；无 active Core 返回 `status=not_found`，候选与归档 Core 不对 Agent 暴露。
  - 工具遵循现有 `memory.mode=on` 与 `memory.tools.enabled` 注册门禁，统一注册表和 taxonomy 均声明只读，不要求网络权限。
- `backend/internal/api/runtimeapi/handler.go`、`memory_handlers.go`
  - 新增 `GET /api/runtime/memory/core`，仅返回当前工作区已审核 active Core，不返回 candidates。
  - 非 `memory.mode=on` 时拒绝；来源失效时以 409 返回通用信息，不将来源 ID 或 evidence text 写入响应。
  - 增加 admin-only `GET /api/runtime/memory/cores?status=candidate|active|stale|archived`、`POST /api/runtime/memory/cores/{id}/activate` 和 `/archive`；审批身份由服务端赋值，不信任请求体身份字段。激活前在 store 写事务内复核来源，archive 保留历史审计。
- 增加 admin-only `POST /api/runtime/memory/core/rebuild`：仅触发一次同步、有界 job，响应报告 job/candidate 状态但不自动激活。候选内容由该 admin 请求返回，普通读取 API 仍只返回 active Core。
- `backend/internal/config/manager.go`、`backend/internal/memoryassembly/factory.go` 与 `backend/configs/runtime.yaml`
  - `memory.core.rebuild.maxRecords/maxUsageRows/tokenBudget` 配置串接到共享 `memoryruntime.Config`；校验上限分别为 64、500、4000，0 使用 runtime 默认值。
- `backend/cmd/aicli/commands/chat_memory_core.go`、`chat_command_result.go`、`chat_memory_command.go`、`chat_slash_command_catalog.go`
  - 新增 `/memory core` active Core 概览；active 不存在时只显示有界 candidate 摘要，不会因 active 来源失效而降级展示候选。
  - `/memory core list [active|candidate|stale|archived]` 提供有界 history 列表；`activate <id> --yes` 和 `archive <id> --reason <text> --yes` 分别显式审核激活和保留审计地归档。
  - 结构化与 legacy 命令入口复用相同实现；激活和读取都经过 store 的来源资格复核。`/memory core rebuild` 会执行一次有界、项目级合并的 `core_rebuild` job，生成 candidate 后保持 pending，不自动改变 active Core。
- `backend/internal/memoryruntime/core_rebuild.go`
  - rebuild 只在 `shadow/on` 执行，先合并并 claim 一个 project-scoped job；输入经过 active summary gate、active/fresh/non-private/未归档记录 gate 和 secret redaction。
  - pinned 来源与近期 usage 来源分开选择：usage 仅统计最近 90 天、最多 500 条 usage rows，并按 bounded frequency、最近使用时间、confidence 和更新时间排序；非 pinned 记录只有在 usage 证据存在时才进入候选。
  - 候选内容和 source IDs 均有上限，候选按 token budget 截断/跳过；提交通过 worker lease、deadline、maintenance lease 和 purge fence 校验，并与 job 成功状态在同一事务中完成。
- `backend/internal/memoryassembly/maintenance.go`
  - `memory.core.rebuild.enabled=true` 时，已有 maintenance owner lease loop 按 `memory.core.rebuild.interval` 周期调用 `CoreRebuilder`；默认关闭，单轮有界且复用 project job/worker lease，不自动激活 candidate。
- `backend/internal/config/manager.go`、`backend/configs/runtime.yaml`
  - 新增 Core scheduler 的显式 `enabled` 与 `interval` 配置；默认 `false`/`24h`，不改变既有 memory v2 默认运行态。
- `backend/internal/memorystore/v2_runtime.go`、`v2_core.go`
  - `EnqueueCoreRebuild` 对同一 project 合并 pending/running/terminal 请求，限制 `maxAttempts`，并保留 job priority、租约和审计边界。
  - `CreateCoreCandidateForWorker` 在事务内复核来源和 worker attempt；candidate 写入失败不会把 job 标成成功，active Core 永不由 rebuild 路径覆盖。
- `backend/internal/memorystore/v2_store.go`
  - v2 selector 在 stable stage 读取同一个 `GetCore` active/read-time source gate；无 active Core 或来源失效时 fail closed 为“不注入”，不把 Core 内容降级成未经校验的记录。
  - Core 先占用现有 `TokenBudget`，按 context 中实际的 `[reviewed project core vN] content` 行估算 token；无法完整容纳时以 `core_budget` 丢弃，不截断 reviewed assertion。summary、pinned stable 和 recall 继续从同一总预算扣减，最终 `Selection.TokenEstimate` 不超过预算。
  - `Selection.Core` 和 Core ID 进入 selection/why-not 选择面；Core 不伪装成 `memory_entries`，因此当前 context metadata 不把 synthetic Core ID 写入 record usage id 集合。
- `backend/internal/memorystore/v2_schema.go`、`v2_core_usage.go`
  - migration v23 新增独立 `memory_core_usage` ledger，记录 project/Core/version/session/turn/message/time/use kind；Core ID 不写进 `memory_usage`，也不更新普通 record 的 `use_count/last_used_at`。
  - `RecordCoreExposure` 去重写入并按当前 project 校验 Core identity；`ListCoreUsage` 支持 Core/session/turn/use-kind 查询，默认 50、最大 500。
- `backend/internal/memorystore/v2_citation.go`、`backend/internal/agent/loop.go`、`backend/internal/toolkit/tools/memory_tools.go`
  - 答案引用 allowlist 从独立 `project_core_id` metadata 取 Core ID，普通 `memoryIDsFromContextMetadata` 仍只接受 memory record IDs。
  - answer exposure 将普通记录与 Core citation/exposure 分流写入各自 ledger；citation parse 失败时 Core 仍只记 `injected_only`。`memory_core_get` 成功读取时 best-effort 记 `read`，不让 telemetry 错误阻塞工具结果。
- `backend/internal/api/runtimeapi/memory_handlers.go`
  - 新增 admin-only `GET /api/runtime/memory/core/usage`，按 Core/session/turn/use-kind 有界读取独立 ledger；未授权请求被拒绝。
- `backend/internal/memorystore/v2_retention.go`、`v2_governance.go`
  - 诊断 exposure retention 同时清理过期 Core `injected_only` rows；生命周期 `AuditDays` 同时清理 Core usage；项目 Reset 同事务删除该 project Core usage。
- `backend/internal/contextmgr/project_memory.go`、`manager.go`
  - stable block 顺序为 reviewed Core → summary → pinned/manual stable records；Core 的 ID/version/source IDs 仅作为内部 metadata，Core 内容不会进入 recall block。
  - 既有 legacy/off 路径未改变；v2 stable message 增加 Core 注入标记和选择预算 metadata。
- 测试覆盖 Core 读取后的来源失效、工具读路径/空状态/只读 policy、builtin 工具注册，以及 HTTP active/无 active/来源失效与错误信息脱敏、admin-gated candidate list/activate/archive 和失效来源的激活拒绝。
- 另覆盖 Core rebuild API 的 admin 授权、配置接线后的 bounded candidate 生成、候选保持 pending 及 active Core 不被重建流程改变；配置测试覆盖 YAML/JSON round-trip、runtime wiring 与安全上限。
- Core rebuild HTTP 契约另覆盖 4 KiB 请求体上限（包括首个 JSON 后的超长空白）、拒绝多个 JSON 文档、workspace path mismatch、`memory.mode=off` 不创建数据库，以及无安全来源时不产出候选并保持既有 active Core 不变。
- 另外覆盖 active Core 的 selection、来源失效后的不注入、Core 超预算、Core+summary+pinned 总预算，以及 context stable 顺序和 why-not `stage=core`。
- 本增量另以 `t.TempDir()` SQLite fixture 覆盖 Core `cited/read/injected_only` 写入和 admin 查询、project 隔离、missing Core skip、session/kind 校验、查询过滤/上限、普通 `memory_usage` 与 record `use_count` 不受 Core ID 影响，以及 Core injected-only retention。
- memoryruntime citation 回归验证同一 answer 中普通 record 与 Core citation 的 ledger 分流、Core citation parse 失败时降级为 `injected_only`；agent helper 测试锁定 `project_core_id` 不混入普通 record ID 列表。
- 本切片新增注入分类预算的最小闭环：`memory.injection.budgets.{core,task,retrieved,procedure,reference}` 默认按
  `0.20/0.15/0.40/0.15/0.10` 分配现有总预算；`extendedPreset=true` 才把缺省总预算提升到 5000（仍受
  12000 上限约束）。selector 记录每类 effective cap/usage，未出现的分类预算回流 retrieved，且
  `reference → procedure → retrieved → task → core` 的超限丢弃写入 `DroppedRecord{Reason:"category_budget",Category:...}`。
  Core 仍先于普通 recall，并且 active Core 的来源安全 gate 未改变。

## 可复现验证

工作目录：`backend/`

```text
gofmt -w internal/policy/taxonomy.go internal/memorystore/v2_core.go internal/memorystore/v2_core_test.go internal/toolkit/tools/memory_tool_defs.go internal/toolkit/tools/memory_tools.go internal/toolkit/tools/memory_policy_test.go internal/toolkit/tools/memory_tools_test.go internal/tools/builtin_catalog.go internal/tools/memory_tools_test.go internal/api/runtimeapi/memory_handlers.go internal/api/runtimeapi/handler.go internal/api/runtimeapi/memory_handlers_test.go
```

结果：完成，无格式化错误。

定向验证：

```text
go test ./cmd/aicli/commands -run 'TestChatMemoryCore' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands

gofmt -w internal/memorystore/v2_store.go internal/memorystore/v2_core_test.go internal/memorystore/v2_types.go internal/contextmgr/project_memory.go internal/contextmgr/project_memory_v2_test.go internal/contextmgr/manager.go internal/api/runtimeapi/memory_handlers.go internal/api/runtimeapi/memory_handlers_test.go
完成，无格式化错误。

go test ./internal/memorystore -run 'TestCoreMemory|TestActiveCoreRead' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/memorystore

go test ./internal/toolkit/tools ./internal/tools -run 'TestMemory(CoreGet|Tools|ToolRegistration|ToolPolicy)' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools
ok   github.com/wwsheng009/ai-agent-runtime/internal/tools

go test ./internal/api/runtimeapi -run 'TestMemoryAPICore' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi

go test ./internal/memorystore -run 'TestCoreMemory|TestActiveCoreRead|TestSelectForInjectV2' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/memorystore

go test ./internal/contextmgr -run 'TestBuildProjectMemory' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/contextmgr

go test ./internal/api/runtimeapi -run 'TestMemoryAPIWhyNotRequiresAdminAndExplainsSelection' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi

go test ./internal/memorystore -run 'TestSelectForInjectV2Category' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/memorystore

go test ./internal/config ./internal/memoryassembly ./internal/agent -run 'Test(MemoryConfig|BuildFromRuntimeConfig|MemoryConfigFromOptions)' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/config
ok   github.com/wwsheng009/ai-agent-runtime/internal/memoryassembly
ok   github.com/wwsheng009/ai-agent-runtime/internal/agent

go test ./internal/memorystore -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/memorystore

go test ./internal/memorystore ./internal/memoryruntime -run 'Test(RecordCoreExposure|ListCoreUsage|PruneDiagnosticsAlsoPrunesCoreInjectedOnlyExposure|RecordAnswerExposure)' -count=1 -v
ok   github.com/wwsheng009/ai-agent-runtime/internal/memorystore
ok   github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime

go test ./internal/api/runtimeapi -run 'TestMemoryAPICore(UsageIsAdminReadOnlyAndSeparate|ReadUsesReviewedActiveCoreAndHidesInvalidationDetails|ReviewLifecycleIsAdminGated|RebuildIsAdminGatedAndLeavesCandidatePending)' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi

go test ./internal/config ./internal/memoryassembly ./internal/runtimehost -count=1
ok   all listed packages

go test ./internal/contextmgr ./internal/toolkit/tools ./internal/tools -count=1
ok   all listed packages

go test ./internal/agent -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/agent

go test ./cmd/aicli/commands -count=1
ok   github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands

go test ./internal/api/runtimeapi -count=1
全包运行存在间歇性时序失败：此前分别出现 TestAgentChat_ResumeOnDisconnect_ContinuesAfterClientAbort、
TestUpdateTaskTerminalStatusesReleaseLease；最近一次运行失败于
TestSyncTeamLifecycleLoopsCompletesTeamWhenTeammateReportsOutcomeViaBroker。上述失败项各自隔离重跑均通过，
因此不能把 runtimeapi 全包写成稳定通过；Core/API 定向测试通过。
```

本轮新增/修复后的定向验证：

```text
go test ./internal/memoryruntime -run 'TestCoreRebuilder' -count=1 -v
ok   github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime

go test ./internal/memorystore -run 'Test(CreateCoreCandidateForWorker|EnqueueCoreRebuild)' -count=1 -v
ok   github.com/wwsheng009/ai-agent-runtime/internal/memorystore

go test ./cmd/aicli/commands -run 'TestChatMemoryCore' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands
```

后台 scheduler/worker 定向验证：

```text
go test ./internal/memoryassembly -run 'TestMaintenance(CoreRebuild|EnabledAndScheduleDerivation)' -count=1
ok   github.com/wwsheng009/ai-agent-runtime/internal/memoryassembly
```

该测试使用 `t.TempDir()` SQLite fixture，验证 scheduler 取得 maintenance owner lease 后按短 interval 执行一次 bounded `core_rebuild` worker，生成 `candidate` 且没有 active Core。

上述 runtime 测试覆盖 pinned + recent usage candidate、无安全来源、过期 usage 过滤、active Core 保持不变；memorystore 测试覆盖 worker 事务提交、失效 lease 不写 candidate、job 合并和 terminal job 重入队。所有数据库均来自测试 `t.TempDir()`。

此前合并 package 验证 `go test ./internal/memorystore ./internal/memoryruntime ./internal/contextmgr ./internal/toolkit/tools ./internal/tools ./internal/api/runtimeapi -count=1` 及后续包含 `memorystore` 的组合验证中，`memoryruntime`、`contextmgr`、`toolkit/tools`、`tools`、`api/runtimeapi` 均通过；`memorystore` 的 `TestConcurrentMaintenanceLeaseAcquiresHaveSingleWinner` 间歇报告竞争错误。该测试单独重跑通过，完整 `go test ./internal/memorystore -count=1` 也两次通过。本切片未修改该 lease 逻辑或测试，因此组合并发状态如实保留为待观察。

## 明确未完成 / 门禁状态

- `core_rebuild` 已由 CLI、admin-only HTTP API 和 opt-in 的后台 maintenance scheduler 有界触发；scheduler 复用现有 maintenance owner lease 与 project-scoped worker job。仍没有 Core job 的独立 runtime 状态面，CLI/API 也没有把周期调度本身做成独立控制面。
- CLI `/memory core` 的候选/历史列表、人工激活与归档，以及 `/memory core rebuild` 的待审核 candidate 生成已实现；自动批准、自动激活和模型直接写 active Core 仍被禁止。
- 当前已完成分类预算配置、runtime/store 传递、selector effective cap/usage 和 category why-not 的最小闭环；但 Context Builder 尚未把
   task/procedure/reference 分成独立 prompt 分区，Core 与 summary/pinned/source 的完整语义去重、Core usage 对 rebuild 候选排序/诊断聚合的接入、完整 recall trace/why-not
   字段仍未完成。Core usage ledger 目前只记录并提供有界读取；没有 session-generation 字段，不能宣称 session-generation 精确 purge 已覆盖该 ledger。
- rebuild 的 usage 输入是最近 90 天、最多 500 条 rows 的 bounded sample；当前没有完整历史 exposure 聚合、按 session/turn 的去重/权重校准或 feedback/evaluation 闭环，因此不能把它称为完整 usage 分析。
- 当前 Core 预算仍受 project-memory 总预算中的 `core` 分类比例限制；extended preset 会把总预算提升到至少 5k（配置硬上限 12k），但尚未实现独立的 1k–2.5k Core 预算校准或更细的分类分区，因此不代表完整参考预算策略已经落地。
- Core 与 summary/pinned 的去重和 summary 失效规则仍未完成。
- tenant/owner/agent 隔离（G2）、FTS5/BM25（G1）、Episode/Dreaming（G4）、entity/RRF/Trust/feedback/eval（G5）均未由本切片关闭。

因此，此证据证明 Core 的安全读取/注入面和人工管理面已可用，**不代表 G3.5 或 B5 完成**。
