# 会话分析与子代理可靠性 实施方案

> 来源报告：`docs/analysis/code_verification.md`、`docs/analysis/session_analysis_report.md`、`docs/analysis/session-analytics-report.md`
> 前置方案：`docs/plan/session-usage-analytics-and-agent-diagnostics-plan.md`（2026-07-27 调查）
> 本方案基于 **2026-09-17 对代码与本地数据的实测复核**，凡与报告冲突处一律以代码实测为准。
> 仓库：`E:\projects\ai\ai-agent-runtime`；Go module 根：`backend/`（下文路径均为仓库相对路径）。

---

## 0. 摘要与勘误

### 0.1 一句话结论

三份报告指出的**产品方向（会话分析、成本洞察、失败诊断、子代理可靠性）有效**，但其技术口径仍是旧 TypeScript 时代的（`analytics_*` 表、`rollup_json`、`tool.call.*` 事件、`cmd/` 布局）。实测发现两条真实断裂：

1. **新分析库（`usage_analytics.sqlite`）完全没有采集到数据**：`usage_requests` / `usage_sessions` 均为 0 行，而真正有数据的 `analytics_*` 三张旧表（35129/4131/1891 行）在 `backend/` 全仓已无任何读取代码——**采集链路断在第一环**。
2. **会话内工具与子代理的观测数据不可信**：`session_events` 中 `tool.*` 事件为 0 行、`session_tool_receipts` 为 0 行；`subagent.completed` 虽有 271 行，但两个生产者写入的载荷字段口径不同（`success` bool vs `status` string），失败分类、重试、耗时维度全部缺失。

本方案按 **P0（数据可信性）→ P1（归一化与暴露）→ P2（基线与文档）** 三优先级推进，共 8 个批次；其中**批次 6 是数据驱动的 Agent 行为优化**（父级恢复建议、只读任务有界重试、上下文/预算降级、提示词迭代），**批次 7 是三端呈现细化**（frontend 观测 tab / aicli TUI `/usage` 扩展与 Agent Panel 增强 / micro web client 分析页签，见 §9），每批次给出目标文件、验收命令与回滚方式。

### 0.2 报告口径与代码实测差异（实施前必须对齐）

| 主题 | 报告口径 | 代码实测（2026-09-17） | 本方案处置 |
| --- | --- | --- | --- |
| 分析库表结构 | `analytics_sessions` / `analytics_turns` / `analytics_llm_requests` / `rollup_json` | 现役代码只写 **`usage_requests` / `usage_sessions`**（`backend/internal/usageanalytics/store.go` DDL；`ingest.go:348-464` upsert）。`analytics_*` 在 `backend/` 仅剩一处注释（`contracts.go:16`），无读写代码 | 全部以 `usage_*` 为准；旧表按批次 0.4 归档 |
| 包路径 | `chataloganalytics` | `backend/internal/chataloganalytics/` 为空目录；现役包为 `backend/internal/usageanalytics` | 全文改口 `usageanalytics` |
| 工具事件名 | `tool.call.*` / `tool_call` | 事件注册表为 **`tool.requested` / `tool.completed`**（`backend/internal/events/contract.go:75-77`，通道 `ChannelSessionStore \| ChannelChatBridge`） | 统一用注册表事件名 |
| 工具观测完整性 | 报告判断"部分缺失" | 实测更严重：`session_events` 16062 行中 `tool.*` **0 行**，`tool_receipt_recorded` 仅 1 行，`session_tool_receipts` **0 行** | 列为 P0（批次 0.2 / 0.3） |
| 子代理字段 | 报告混用 `status` / 成功标志 | 存在**两个生产者**：`backend/internal/agent/scheduler.go:454,507` 写 `success` bool；`backend/internal/api/skills/session_runtime_support.go:1036-1053` 镜像写入父会话并携带 `status` string（另有 `control_action` / `agent_id`） | 批次 2.1 归一化，读取侧做兼容映射 |
| 目录布局 | `cmd/…` | 实际为 `backend/cmd/…`（`aicli`、`runtime-server` 等 13 个入口） | 命令与测试命令均以 `backend/` 为工作目录 |

### 0.3 数据链路现状与断点

```
aicli / runtime-server 进程
  events.Bus ──┬─ 通道A  SessionStore   → session_runtime.sqlite: session_events      [通]
               ├─ 通道B  live SSE                                                 [通]
               ├─ 通道C  chat SSE bridge                                          [通]
               └─ 通道D  turn-end tail                                            [通]
  usageanalytics.Service.Attach(bus) → ingest → usage_analytics.sqlite
                                             usage_requests / usage_sessions        [断：0 行]
```

| 编号 | 断点 | 实测证据 | 影响 |
| --- | --- | --- | --- |
| G1 | 分析采集未生效 | `usage_requests`/`usage_sessions` = 0 行；两处 attach 均为**懒加载**（`backend/internal/api/skills/analytics_handlers.go:100-130`；`backend/cmd/aicli/commands/chat_cache_local.go:235-292`） | 所有分析 API/报表返回空 |
| G2 | `tool.*` 事件未落库 | `session_events` 中 `tool.requested`/`tool.completed` = 0 行（784 个会话、22 种事件类型中不存在） | 工具失败率、空结果率、重试恢复全部无法计算 |
| G3 | 工具回执未持久化 | `session_tool_receipts` = 0 行；写入方法 `SaveToolReceipt`（`backend/internal/chat/session_runtime_store.go:2186-2215`）生产调用点缺失（仅 `sessionAgentController` 测试路径可见） | 会话恢复/审计缺少工具结果证据 |
| G4 | 子代理载荷双口径 | 库内 271 行分布：`success=true` 59 / `false` 186 / 缺字段 26；`status=idle` 59 / `failed` 186 / `stopped` 26 | 失败率统计随生产者不同而失真 |
| G5 | 失败分类缺失 | `usage_requests.error_category` 的实际生产路径仅覆盖 `"interrupted"` 一种取值 | 无法做错误类型 Top-N 与重试收益分析 |

### 0.4 本机数据基线（可复现，2026-09-17）

| 库/表 | 行数 |
| --- | --- |
| `usage_analytics.sqlite` :: `usage_requests` | 0 |
| `usage_analytics.sqlite` :: `usage_sessions` | 0 |
| `usage_analytics.sqlite` :: `analytics_llm_requests` | 35129（死表） |
| `usage_analytics.sqlite` :: `analytics_sessions` | 4131（死表） |
| `usage_analytics.sqlite` :: `analytics_turns` | 1891（死表） |
| `session_runtime.sqlite` :: `session_events` | 16062（784 个会话，22 种类型） |
| `session_runtime.sqlite` :: `subagent.completed` | 271 |
| `session_runtime.sqlite` :: `tool.*` | 0 |
| `session_runtime.sqlite` :: `tool_receipt_recorded` | 1 |
| `session_runtime.sqlite` :: `session_tool_receipts` | 0 |

复现命令见附录 A。

---

## 1. 目标、范围与验收标准

### 1.1 目标（按优先级）

| 优先级 | 目标 | 判定标准（可执行） |
| --- | --- | --- |
| P0-1 | 分析库恢复采集 | 完成一个真实会话后 `usage_requests` / `usage_sessions` 行数 > 0，且 runtime 状态快照可见采集健康字段 |
| P0-2 | 工具生命周期可观测 | 一次带工具调用的回合后，`session_events` 出现 `tool.requested` 与 `tool.completed`，`session_tool_receipts` 出现对应回执 |
| P0-3 | 子代理结果口径统一 | 两个生产者产出的 `subagent.completed` 均同时携带规范化 `success`/`status`/`failure_category` 字段，统计不再随生产者漂移 |
| P1-1 | 查询层可回答"失败在哪" | rollup 输出工具失败 Top-N、错误类型 Top-N、子代理失败率、重试恢复回合数 |
| P1-2 | 对外可消费 | 分析 API 新增子代理/工具维度；`aicli stats` 子命令可在无服务进程时只读查看 |
| P2-1 | 基线可回归 | 提供一键基线脚本，输出与 §0.4 同构的统计，用于每次改动后对比 |
| P1-3 | 子代理失败可恢复（批次 6.1/6.2） | 可重试的**只读**子代理任务自动重试且 `attempt` 可见；父代理上下文出现分类化 `retry_advice`，而非仅原始错误文本 |
| P2-2 | 提示词/守卫迭代有数据依据（批次 6.4） | 诊断输出"advisory 后同类失败重复率"指标，用于调整提示词与阈值（不改执行行为） |

### 1.2 明确不做（本方案边界）

- 不做前端展示改动（仅保证 API 向后兼容，新增为增量字段）。
- 不做 LLM 请求内容级审计（延续"低敏"原则：只落 token、时长、错误码/分类，不落 prompt/响应正文）。
- 不做历史死表数据自动迁入（默认归档；如需历史可见，执行批次 1.4 的显式 backfill）。
- 不做无界/无条件自动重试：仅对**只读任务的瞬时失败类别**做有界重试（批次 6.2）；写任务一律不自动重试，避免重复副作用。
- 不做自动模型/供应商切换、不做自动任务改写——任务拆分由父代理基于 6.1 的建议自行决策。

### 1.3 验收命令约定

所有 Go 命令以 `backend/` 为工作目录：

```powershell
# 单元测试（每批次完成后跑对应包）
go test ./internal/usageanalytics/... ./internal/events/... ./internal/chat/... ./internal/api/skills/...

# 构建（批次 3/4 后必须通过）
go build ./cmd/runtime-server ./cmd/aicli
```

**入口覆盖对照（批次 → 入口）**：

| 入口 | 覆盖批次 | 落点 |
| --- | --- | --- |
| runtime-server | 0.1（启动即 attach）、3.1（API 端点） | `backend/cmd/runtime-server/main.go`；`backend/internal/api/skills/analytics_handlers.go` |
| aicli（chat 会话） | 0.1（启动即 attach，chat 装配点） | `backend/cmd/aicli/main.go` 或 chat 启动装配点；`backend/cmd/aicli/commands/chat_cache_local.go:235-292` |
| aicli（CLI 只读） | 4.1/4.2（`aicli stats`） | 新增 `backend/cmd/aicli/commands/stats.go`，注册于 `backend/cmd/aicli/main.go` |

---

## 2. 现状架构与可复用资产

### 2.1 组件分层（现役）

| 层 | 位置 | 现状 |
| --- | --- | --- |
| 事件契约 | `backend/internal/events/contract.go` | `tool.requested`/`tool.completed` 已注册且声明持久化通道（:75-77）；`tool_receipt_recorded` 已声明（:115） |
| 投递管道 | `backend/internal/api/skills/runtime_event_delivery.go` | 四通道模型与丢弃计数已存在（`DeliveryChannelsFor` :57），计数经 `runtimeStatusSnapshot["runtime_event_delivery"]` 暴露 |
| 会话事件存储 | `backend/internal/chat/session_runtime_store.go` | `AppendEvent` 写入（:2354-2359）；工具回执 upsert（:2186-2215）；表 DDL 与剪枝（:4138-4153） |
| 工具结果语义 | `backend/internal/toolresult/diagnostic.go:13-50` | 已有 `ok` / `error_code` / `retryable` / `next_action` / `outcome`（`success\|empty\|partial\|failed`）/ `empty_result` / 失败计数等元数据键 |
| 事件载荷提升 | `backend/internal/agent/tool_runtime_events*.go` | `promoteToolDispositionToPayload` 已把工具处置元数据提升到 `tool.completed` 载荷 |
| 子代理完成① | `backend/internal/agent/scheduler.go:454,507` | 写 `success` bool（通道 D 尾部投递优先） |
| 子代理完成② | `backend/internal/api/skills/session_runtime_support.go:1015-1053` | 镜像写父会话 `subagent.completed`（含 `status`，:1080-1085 做 failed/stopped→终态映射） |
| 分析采集 | `backend/internal/usageanalytics/{store,ingest,service}.go` | 表迁移与 upsert 已实现；`Service.Attach(bus)` 订阅 ingest |
| 查询 | `backend/internal/usageanalytics/query.go` | `ListSessions`(:355) / `Summarize`(:515) / `Dimensions`(:627) / `SessionUsage`(:693) / `sessionRollup`(:745) / `buildTurns`(:939) / `buildDiagnostics`(:1006) |
| HTTP | `backend/internal/api/skills/analytics_handlers.go`（+`_test.go`） | `/api/runtime/analytics/*` 已挂载；另有 `cache_analytics_handlers.go`、`execution_diagnostics.go`（执行诊断，含 run/team/background/task 状态词汇） |

### 2.2 直接可复用的机制（避免重复造轮子）

1. **只读打开降级**：`usageanalytics.Open(Config{ReadOnly:true})`，库不存在时查询返回空而非报错（`store.go:67-68,96-98`；`Service.Query()` 空库返回 nil）——批次 4 的 CLI 直接复用。
2. **写锁竞争退避**：`Open` 对瞬时锁冲突已有 `openLockRetries` 退避（`store.go:104-116`），多进程（aicli + runtime-server）同库并发启动已有保护；新增表迁移沿用同一入口即可。
3. **投递计数模式**：批次 0.2 采集健康字段直接仿照 `runtime_event_delivery.go` 的计数器 + status snapshot 暴露方式。
4. **状态词汇归一**：`execution_diagnostics.go` 已建立 P3/pending/running/failed 等归一化词表（:161,:354,:395,:491,:502），子代理/运行状态诊断沿用同一词表，避免第三套状态名。

### 2.3 依赖关系

```
批次0(数据可信性) ──> 批次1(schema+采集) ──> 批次2(载荷归一化) ──> 批次3(API 暴露) ──> 批次4(CLI)
                                              ├──────────────────> 批次5(基线与文档) 依赖 1、2、3
                                              └──> 批次6(Agent 行为优化) 依赖 1.2 指标 + 2.1 规范化字段
                                                   6.1/6.2 先行；6.3/6.4 建议积累 ≥1 周数据后实施
```

批次 2 与批次 3 可部分并行；批次 1 与批次 2 在 ingest 层需要一次接口对齐（见 §5.2）。
批次 6.1/6.2 在 2.1 完成后即可与 3.x/4.x 并行；批次 6 不阻塞 0–5 的任何验收项。

---

## 3. 设计决策

### D1：单一事实库 = `usage_analytics.sqlite`，旧表只归档不兼容

`analytics_*` 三表已无代码引用，继续保留会造成"分析 API 读新表、运维查旧表"的双口径。决策：新表结构以 `usage_*` 为准扩展；批次 0.4 将旧表重命名归档；仅当明确需要历史数据时执行 backfill（批次 1.4），backfill 必须是显式子命令，不随启动自动发生。

### D2：采集器"进程启动即 attach"，懒加载只作兜底

现状两处 attach 均为访问 `/api/runtime/analytics` 或首次缓存查询时才触发（G1 根因）。决策：在 `backend/cmd/runtime-server` 与 `backend/cmd/aicli` 的装配点显式 attach；`analytics_handlers.go` 内的懒加载保留为兜底（防止测试/嵌入式场景空转），但需要把 attach 结果（成功/失败/路径）写入状态快照以便观测。

### D3：工具观测在 ingest 层聚合，不在事件层新增事件类型

`tool.requested`/`tool.completed` 语义已闭合（含 `toolresult` 处置元数据），无需新增事件。决策：flake 风险落在"投递与落库"（G2/G3），修复点分别是投递通道判定与 actor 回执写入；分析侧只是在 ingest 里消费这两个事件写入新表 `usage_tool_calls`。**禁止**为了统计而新造 `tool.metrics` 之类平行事件。

### D4：`subagent.completed` 采用"规范化字段 + 原始字段并存"的双写过渡

两个生产者短期内都要改，但历史数据与未知消费方不能破坏。决策：

- 规范化字段：`subagent_id`、`role`、`child_session_id`、`parent_session_id`、`success`(bool,权威)、`completion_reason`、`failure_category`、`error_code`、`attempt`、`max_attempts`、`duration_ms`、`source`。
- 过渡规则：写方 **同时**写 `success` 与 `status`（`status` 为兼容别名：completed/failed/stopped/idle）；读方以 `success` 优先，缺失时按 §5.2 映射表回退。
- 原始错误文本不落入分析库（低敏），只保留 `failure_category` + `error_code`。

### D5：失败分类独立成表/字典，而非自由字符串

`usage_requests.error_category` 目前实际只有 `"interrupted"` 一种取值（G5）。决策：新增 Go 枚举 `usageanalytics.FailureCategory`（`provider_error` / `rate_limited` / `timeout` / `context_overflow` / `tool_error` / `budget_exceeded` / `cancelled` / `interrupted` / `unknown`），并提供与 `llm/retry_policy.go:776 ClassifyFailureCode`、`runtimeobserve` 现有 `retry_reason_category`/`errorCategory` 的映射函数，先复用已有分类能力再补齐缺口，避免三处各写一套。

### D6：新增表用 `CREATE TABLE IF NOT EXISTS` + `PRAGMA user_version` 显式版本号

现役迁移无 `ALTER TABLE`、无版本表。决策：新增 `usage_tool_calls` / `usage_subagents` / `usage_turns` 三表走 `CREATE TABLE IF NOT EXISTS`（幂等），并在迁移尾部设置 `PRAGMA user_version = 2`；后续列变更再引入正式迁移函数。所有迁移必须在 `Open` 的同一写锁退避路径内执行（复用 `store.go:104-116` 的锁竞争退避机制）。

### D7：CLI 只读、API 增量、前端不强制联动

- `aicli stats` 一律 `ReadOnly:true` 打开，库不存在时输出"暂无数据"而非报错；支持 `--json` 供脚本消费。
- HTTP 侧只新增端点/字段，不修改既有 JSON 字段语义；`analytics_handlers_test.go` 的既有断言必须保持通过。
- 是否在 Web 前端消费新字段属于后续工作，不在本方案验收范围（见 §1.2 边界）。

---

## 4. 分阶段实施

> 行号均为 2026-09-17 快照，落地时以最新代码为准；每个批次完成后必须跑 §1.3 对应测试并回填 §8 状态。

### 批次 0：数据可信性修复（P0，预估 1.5–2 人日）

#### 0.1 采集器启动即 attach

| 项 | 内容 |
| --- | --- |
| 目标 | 消除懒加载导致的采集空转（G1） |
| 目标文件 | `backend/cmd/runtime-server/main.go`；`backend/cmd/aicli/main.go` 或 chat 启动装配点；`backend/cmd/aicli/commands/chat_cache_local.go:235-292`；`backend/internal/api/skills/analytics_handlers.go:100-130` |
| 步骤 | ① 抽出 `usageanalytics.AttachForProcess(bus, source)` 帮助函数（或复用现有 attach 封装），在两个入口 bootstrap 阶段调用；② 保留 handler 内懒加载为兜底，但 attach 结果写入 `runtimeStatusSnapshot`，字段建议 `usage_analytics: {attached, db_path, last_ingest_at, ingested_total}`；③ 确保 bus 为 nil（纯 CLI 只读场景）时仍不报错——沿用 `service.go` 中"bus 为 nil 仍打开数据库、不订阅"的既有语义 |
| 测试 | 新增 `TestUsageAnalyticsAttachedAtBootstrap`（入口装配测试）；`go test ./cmd/... ./internal/usageanalytics/...` |
| 手工验收 | 跑一次真实 chat 回合后查库：`usage_requests` > 0 且 `usage_sessions` > 0（命令见附录 A.2） |

#### 0.2 `tool.*` 事件落库根因定位与修复

| 项 | 内容 |
| --- | --- |
| 目标 | 让 `tool.requested` / `tool.completed` 进入通道 A 并持久化（G2） |
| 目标文件 | `backend/internal/api/skills/runtime_event_delivery.go`（通道判定与计数）、`backend/internal/agent/tool_runtime_events*.go`（发布点）、`backend/internal/events/contract.go`（契约，预期不改） |
| 步骤 | ① 先写失败测试锁定契约：断言 `DeliveryChannelsFor(events.EventToolRequested)` 与 `...Completed` 均包含 `ChannelSessionStore`，并在 `session_runtime_store` 追加后可在查询中读回；② 用该测试定位断点：若是通道判定遗漏 → 修 `runtime_event_delivery.go`；若是发布缺失/被 `publishTool` 早退跳过 → 修 `agent/tool_runtime_events*.go`；③ 在投递计数中区分"未订阅/未匹配通道/丢弃"，避免再次出现"契约声明了但没人发" |
| 测试 | 新增投递回归测试 + 既有 `runtime_event_delivery` 相关测试全绿 |
| 手工验收 | 执行一次带工具的回合，`session_events` 出现 `tool.requested`、`tool.completed` 各 ≥1 |

#### 0.3 工具回执写入路径修复

| 项 | 内容 |
| --- | --- |
| 目标 | `session_tool_receipts` 有真实数据（G3），恢复/审计可用 |
| 目标文件 | `backend/internal/chat/actor.go:4870-4884`（调用点）、`backend/internal/chat/session_runtime_store.go:2186-2215`（写入实现，预期不改） |
| 步骤 | ① 审计 `saveStoredToolReceipt` 的触发条件与 `toolReceiptStore` 可用性判断，确认生产路径为何未触发（疑似条件互斥或仅测试路径注入）；② 修复后在工具完成事件处理链中确保"完成即回执"；③ 对失败工具同样落回执（`ok=false`），否则失败证据丢失 |
| 测试 | 新增 `TestToolReceiptPersistedOnToolCompletion`（含失败分支） |
| 手工验收 | `session_tool_receipts` > 0，且与 `tool.completed` 数量一致（允许 ±1 在途） |

#### 0.4 旧表归档与基线脚本

| 项 | 内容 |
| --- | --- |
| 目标 | 消除 `analytics_*` 死表语义混淆；建立可重复基线（§0.4） |
| 目标文件 | 新增 `backend/scripts/usage-analytics-archive-legacy.ps1`、`backend/scripts/usage-analytics-baseline.ps1`（如 `backend/scripts/` 不存在则新建，并在脚本头注释用途） |
| 步骤 | ① 归档脚本：导出三张旧表为 JSON/CSV 到 `~/.aicli/sessions/runtime/backup/<date>/`，随后将表重命名 `analytics_*` → `analytics_legacy_*`；幂等（已重命名时输出 "already archived"）；默认**不 DROP**；② 基线脚本：输出 §0.4 同构统计（两个库的表计数、事件类型分布、子代理字段分布）；③ 在两处脚本中不引入新依赖（sqlite3 CLI 或 python 均可，需在脚本头注明前置条件） |
| 测试 | 脚本在本机跑两遍，第二遍为幂等空操作 |
| 回滚 | 提供 `-Restore` 开关把 `analytics_legacy_*` 改回原名 |

### 批次 1：Schema 扩展与聚合（P0，预估 2–3 人日）

#### 1.1 schema v2（新增三表）

| 项 | 内容 |
| --- | --- |
| 目标 | `usage_tool_calls` / `usage_subagents` / `usage_turns` 三表建立（DDL 见 §5.1） |
| 目标文件 | `backend/internal/usageanalytics/store.go`（DDL 常量 + migrate + `PRAGMA user_version=2`） |
| 步骤 | ① 按 §5.1 DDL 增加三个 `CREATE TABLE IF NOT EXISTS` 与索引；② 迁移保持在 `Open` 的锁退避路径内；③ `ReadOnly` 打开必须跳过写入型迁移（沿用现有 `openReadOnly` 分支，库缺失→empty 语义不变） |
| 测试 | `TestStoreMigratesV2Idempotent`（连续两次 Open 无错、表存在）、`TestReadOnlyOpenSkipsMigration` |
| 验收 | 全量 `go test ./internal/usageanalytics/...` 通过；实机库 `user_version=2` |

#### 1.2 采集器消费新事件

| 项 | 内容 |
| --- | --- |
| 目标 | ingest 把 `tool.*`、`subagent.completed`、`session_end` 写入三张新表 |
| 目标文件 | `backend/internal/usageanalytics/ingest.go`（订阅白名单 + 4 个 handler + 事务写入） |
| 步骤 | ① 订阅注册沿用现有模式，新增 `tool.requested`/`tool.completed`/`subagent.completed`（`session_end` 已在采集范围，需确认并补 usage_turns 落库）；② `usage_tool_calls`：以 `tool_call_id` 为主键，`requested` 建立骨架行、`completed` 更新 `outcome`/`ok`/`error_code`/`duration_ms`（进度内保留 in-flight 时间戳映射，服务重启丢失时 duration 记 0）；③ `usage_subagents`：见批次 2.1 归一化函数，**同一函数**供 ingest 与回放脚本复用；④ 写入失败只记 degraded 日志，不反压事件总线 |
| 测试 | 表驱动单测：给定样本事件序列 → 断言三表行内容（含失败/超时/重复事件幂等） |
| 验收 | 实机一轮会话后三表均有行 |

#### 1.3 查询层 rollup 与诊断扩展

| 项 | 内容 |
| --- | --- |
| 目标 | `sessionRollup`/`buildDiagnostics` 输出工具与子代理维度（P1-1） |
| 目标文件 | `backend/internal/usageanalytics/query.go` |
| 步骤 | ① `sessionRollup` 增量聚合：工具调用数、结果观测数、失败数、失败率、空结果数；子代理数、失败数/失败率、超时数；回合级"重试恢复"计数（依赖 `usage_turns` 的 `tool_error_count` / `unrecovered_tool_error_count`）；② `buildDiagnostics` 追加诊断码：`tool_failures`、`tool_error_rate_high`、`subagent_failures`、`subagent_failure_rate_high`、`subagent_timeout`、`retry_recovered`，阈值以常量集中定义便于调整；③ 既有字段只增不改，保证 `analytics_handlers_test.go` 断言语义不变 |
| 测试 | query 层 golden 测试（构造三表数据 → 断言 rollup JSON） |
| 验收 | 单测通过；对实机库调用 `SessionUsage` 输出含新字段 |

#### 1.4（可选）历史数据 backfill

仅当产品确认需要"历史会话可见"时执行：新增 `aicli stats backfill --from-legacy`（或独立子命令），把 `analytics_llm_requests`→`usage_requests` 做字段映射迁入（映射表需在实现时逐列确认，禁止猜测）；幂等（按主键 upsert）；默认不执行、不随启动触发。若决定不做，本批次跳过并在 §8 标注。

### 批次 2：子代理载荷归一化（P0，预估 1–2 人日）

#### 2.1 统一 `subagent.completed` 载荷契约

| 项 | 内容 |
| --- | --- |
| 目标 | 两个生产者输出同构载荷，历史数据可兼容读取（G4、D4） |
| 目标文件 | `backend/internal/agent/scheduler.go:454,507`（补规范化字段）；`backend/internal/api/skills/session_runtime_support.go:1015-1053`（补 `success`、补分类字段）；新增共享归一化函数（建议置于 `backend/internal/usageanalytics` 或事件构造侧均可，**只允许一处实现**） |
| 步骤 | ① 按 §5.2 映射表实现 `NormalizeSubagentCompletion(payload) (normalized, ok)`，两处生产者与 ingest 三层共用；② scheduler 侧补 `status`/`completion_reason`/`failure_category`/`attempt`；controller 侧补 `success`，并把 `control_action`/`agent_id` 标记为 `source=agent_controller`；③ 冲突（`success` 与 `status` 语义不一致）时以 `success` 为准并计 `conflict_total` 暴露到状态快照 |
| 测试 | `TestSubagentCompletedPayloadNormalized`（两路径各一用例 + 冲突用例 + 仅 status 的历史兼容用例） |
| 验收 | 实机新产生的 `subagent.completed` 同时含 `success` 与 `status`；分析侧统计不再出现"26 行未知"式漂移（历史行按 §5.2 unknown 单列） |

#### 2.2 ingest 幂等与去重

| 项 | 内容 |
| --- | --- |
| 目标 | 同一子代理被两生产者先后写入时，`usage_subagents` 只有一行且信息最全（D4） |
| 目标文件 | `backend/internal/usageanalytics/ingest.go`（`usage_subagents` upsert） |
| 步骤 | ① 主键 `(subagent_id, parent_session_id)`；字段合并规则：非空优先、`source` 记录"最后写入者"、`conflict_json` 或计数记录冲突；② 子代理缺少 `subagent_id` 时用 `child_session_id+role` 派生稳定 ID，并记 `id_synthesized=1` 便于识别 |
| 测试 | 重复事件序列单测：assert 单行、字段合并正确、conflict 计数递增 |
| 验收 | 对库中 271 行历史事件做一次本地回放（脚本），回放后表行数 ≈ 去重后的子代理数 |

### 批次 3：对外暴露（P1，预估 2–3 人日）

#### 3.1 HTTP API 增量端点

| 项 | 内容 |
| --- | --- |
| 目标 | 工具与子代理维度可经 API 查询（P1-2） |
| 目标文件 | `backend/internal/api/skills/analytics_handlers.go`、`analytics_handlers_test.go`、`backend/internal/usageanalytics/query.go`（新增查询方法） |
| 步骤 | ① 新增只读查询方法：`Store.ToolStats(query)`（按会话/工具名/outcome 聚合）、`Store.SubagentStats(query)`（失败率、分类分布、来源分布）、`Store.ErrorPatterns(query)`（error_code/category Top-N）；② 挂载 `GET /api/runtime/analytics/tools`、`GET /api/runtime/analytics/subagents`、`GET /api/runtime/analytics/errors`，参数沿用现有 handler 的解析风格（session/limit/时间窗）；③ 响应保持"空库返回空数组而非 500"的既有语义 |
| 测试 | 扩展 `analytics_handlers_test.go`（沿用其现有请求助手风格），新增 `TestAnalyticsHandlersToolStats`/`SubagentStats`/`ErrorPatterns` |
| 验收 | 单测通过；对实机库 curl 冒烟（命令见附录 A.3） |

#### 3.2 采集健康可视 + 状态词表对齐

- `runtimeStatusSnapshot` 增加 `usage_analytics` 健康块（批次 0.1 已开始，此处补 e2e 断言：attached、db_path、ingested_total、conflict_total、last_ingest_at）。
- 子代理/运行状态词汇与 `execution_diagnostics.go` 既有归一表（:161,:354,:395,:491,:502）保持一致，不允许出现第三套状态名；若执行诊断要展示子代理运行态，直接复用其 `normalize*Status` 风格函数。

### 批次 4：`aicli stats` CLI（P1，预估 2–3 人日）

#### 4.1 命令实现

| 项 | 内容 |
| --- | --- |
| 目标 | 无 runtime-server 进程时也能只读洞察（P1-2） |
| 目标文件 | 新增 `backend/cmd/aicli/commands/stats.go`（及 `stats_test.go`）；注册点 `backend/cmd/aicli/main.go`（参照现有 `AddCommand` 链，如 config/init 命令） |
| 子命令 | `aicli stats sessions --limit N [--json]`；`aicli stats session <id> [--json]`；`aicli stats errors --top N`；`aicli stats subagents [--failed-only]`；`aicli stats doctor`（采集健康自检：两库路径/表计数/最近事件时间/attached 状态，等价基线脚本的轻量版） |
| 步骤 | ① 统一 `--db` 覆盖（默认 `DefaultDBPath()` / `PathFromRuntimeStore()`，env `AICLI_USAGE_ANALYTICS_DB` 优先）；② 一律 `Open(Config{ReadOnly:true})`，库/表缺失输出"暂无数据"并退出码 0；③ 表格输出走现有 TUI/表格工具风格，`--json` 输出稳定字段名（与 API 字段同名，降低心智负担） |
| 测试 | `stats_test.go`：空库降级、字段渲染、`--json` 稳定性 |
| 验收 | 单测 + 手工运行（命令见附录 A.4） |

#### 4.2 退出码约定

- 0：查询成功（含空库）；1：参数错误；2：库损坏等确定性错误。写入 runbook（批次 5.3），供脚本化使用。

### 批次 5：基线固化与文档（P2，预估 1 人日）

| 项 | 内容 |
| --- | --- |
| 目标 | 让"数据可信性"成为可回归项，后来者不再按旧口径实施 |
| 步骤 | ① 用批次 0.4 脚本生成修复后基线，落地 `docs/analysis/session-analytics-baseline-<YYYYMMDD>.md`，并在 `docs/analysis/README.md` 登记；② 在 `docs/analysis/README.md` 增加三份来源报告的勘误指引（指向本方案 §0.2），**不改写原始调查报告**以保留调查记录；③ 新增 `docs/plan/session-analytics-runbook.md`：采集健康检查、常见故障（库锁、路径覆盖、bus nil、多进程并发迁移）、CLI/脚本用法与退出码 |
| 验收 | 新基线文档与 §0.4 表结构一致；runbook 按步骤可独立复现一次故障排查 |

### 批次 6：Agent 行为优化（数据驱动）（P1/P2，预估 3–4 人日）

> 前置：批次 1.2（工具/子代理落库）与批次 2.1（`subagent.completed` 规范化字段）完成。
> 原则：只做**有界、可回滚、可观测**的行为改动；所有自动动作必须先按 D5 的 `failure_category` 分类，禁止无条件重试；6.3/6.4 建议在积累 ≥1 周真实数据后再实施。
> 与报告的关系：报告 §5.1「降低子Agent失败率」、§5.8「增强错误恢复能力」在本批次落实为 6.1–6.3；报告 §5.2 提出的"拆分 `subagent.completed` 为 success/failure 事件"**不采纳**（破坏既有消费方），改由批次 2.1 的 `success` + `completion_reason` 字段区分，6.1 只在其上叠加建议层。

#### 6.1 子代理结果的父级恢复建议（P1，1 人日）

| 项 | 内容 |
| --- | --- |
| 目标 | 父代理收到失败子代理时，从"原始 error 文本"升级为"分类 + 建议动作"，使父代理能选择重派/拆分/本地完成，而不是盲目重试或直接放弃（报告 §5.1「改进任务分解」、§5.8「自动降级」） |
| 现状证据 | `renderSubagentResults`（`backend/internal/agent/loop.go:6271-6318`）当前只渲染 `- <id> (failed): <error 原文>`；`SubagentResult`（`backend/internal/agent/scheduler.go:441-453`、`:485-498`）没有 `failure_category`/`retryable`/`attempt` 字段；父代理无法区分 timeout 与 permission 失败 |
| 改动 | ① `SubagentResult` 增 `FailureCategory / ErrorCode / Retryable / Attempt / MaxAttempts`；进程内错误直接用 `llm.ClassifyFailureCode`（`backend/internal/llm/retry_policy.go:776`）分类后映射到 D5 枚举，**不新建第二套分类**；② `renderSubagentResults` 在 `error:` 行后追加一行机器可读建议 `retry_advice=...`，写法参照既有工具类 advisory（`backend/internal/agent/loop.go:4013-4016`）；③ 建议同时写入 `subagent.completed` 载荷（`retry_advice`），分析侧落 `usage_subagents.record_json`，**不新增列**（避免分析库与建议文案耦合）；`retry_reason` 列语义保持为"实际发生重试的原因"，由 6.2 使用 |
| 分类 → 建议映射 | `timeout`/`rate_limited`/`provider_error` → `retry_with_changed_inputs`；`context_overflow`/`budget_exceeded` → `split_task`；`tool_error` → `complete_locally` 或换工具重派；`cancelled`/`interrupted` → `retry_with_changed_inputs`（仅只读任务）；`unknown` → 不给具体建议，仅提示可本地完成 |
| 测试 | `TestRenderSubagentResultsCarriesRetryAdvice`（表驱动覆盖映射表 5 类）；`TestSubagentCompletedPayloadCarriesRetryAdvice` |
| 手工验收 | 构造一次子代理失败（如只读子代理读取不存在路径），父回合上下文出现 `retry_advice=`；`aicli stats subagents --failed-only` 中 `failure_category` 非空 |
| 回滚 | 纯增量字段与渲染分支，删除该分支即回退到现状 |

#### 6.2 子代理有界自动重试（P1/P2，1–1.5 人日）

| 项 | 内容 |
| --- | --- |
| 目标 | 对**只读任务的瞬时失败**自动重试有限次，减少"临时性故障 → 永久性失败"（报告 §5.1/§5.8 的核心诉求） |
| 现状证据 | `runSubagent` 单次执行，`err != nil` 直接产出 `success=false`（`backend/internal/agent/scheduler.go:432-483`）；现有保护只有批次级熔断 `MaxConsecutiveFailures`（`:91-93`，提示语 `:1322`），**没有任务级重试** |
| 改动 | ① `SubagentSchedulerConfig`（`scheduler.go:82-95`）增 `MaxAttemptsPerTask`（默认 2；`1` = 关闭）与退避参数（指数退避 + jitter，封顶 5s）；② **仅当 `task.ReadOnly == true` 且分类属于瞬时集合**（`timeout`/`rate_limited`/`provider_error`/`interrupted`）时自动重试；写任务不自动重试，只产出 6.1 的建议交父代理决策，防止重复副作用；③ 每次尝试产出 `subagent.completed`，携带 `attempt`/`max_attempts`/`retry_reason`（D4 已定义），最终结果以最后一次为准、中间尝试计入 `conflict_count`；④ 熔断器语义保持：自动重试不计入"连续失败批次"（实现时二选一并注释说明） |
| 测试 | `TestSubagentAutoRetryOnlyTransientReadOnly`；`TestSubagentAutoRetryDisabledWhenMaxAttemptsIsOne`；`TestSubagentAutoRetryDoesNotCountTowardCircuitBreaker` |
| 手工验收 | 用只读子代理制造一次超时/注入失败：`usage_subagents.attempt=2` 且最终成功；写任务失败时不重试（`attempt=1`） |
| 回滚 | 配置项置 1 即关闭；重试路径独立于主路径，可单独删除 |

#### 6.3 上下文/预算耗尽的降级（P2，1 人日，数据触发后实施）

| 项 | 内容 |
| --- | --- |
| 目标 | `context_overflow`/`budget_exceeded` 失败不再表现为"无产出的通用失败"：尽可能保留部分结果，并给出 `split_task` 建议 |
| 现状证据 | 上下文超限被明确设计为**不重试**（`backend/internal/llm/retry_policy_test.go:140-157`）；子代理错误路径只用 `err.Error()` 构造 `SubagentResult`（`scheduler.go:440-483`），部分观察/用量未保留 |
| 改动 | ① 前置核对：`loop.run` 错误路径是否返回部分 `result`（`scheduler.go:432`）；若返回，则落到 `report.Findings/Patches/Usage` 并置 `completion_reason=budget_exceeded/context_overflow`；若不返回，先给 `loop.run` 增加部分结果返回（工作量计入本项）；② 分析侧按 `completion_reason` 把"有部分产出"的失败单列，失败率不再把其计为全失败；③ **不做**自动拆分/自动重派（避免无界递归），只出 `split_task` 建议 |
| 测试 | `TestSubagentContextOverflowKeepsPartialFindings`（前置条件满足时） |
| 回滚 | 若 `loop.run` 无法提供部分结果，本项退化为 6.1 的建议映射，不改执行路径 |

#### 6.4 advisory 采纳率 → 提示词/阈值迭代（P2，0.5–1 人日，数据触发后实施）

| 项 | 内容 |
| --- | --- |
| 目标 | 让运行时提示词的迭代有数据依据：度量 `next_action`/advisory 发出后代理是否仍重复同类失败动作 |
| 现状证据 | 已有两类 advisory：相同工具调用重放提示（`backend/internal/agent/loop.go:4013-4016`）与轮询去重提示（`backend/internal/agent/polling_guard.go:334-341`）；但没有"提示是否生效"的度量 |
| 改动 | ① 在查询层（`backend/internal/usageanalytics/query.go:1006 buildDiagnostics`）增加会话内指标：同一 `turn_id` 内相同 `tool_name` 连续 `outcome=failed` 的分布、`error_code` Top-N、以及 failed→succeeded 的恢复配对（用 `usage_tool_calls` 既有列即可，不新增 schema）；② 输出到 `aicli stats errors --top N`（4.1）与 API 诊断字段；③ 依据 Top 工具/错误调整 advisory 触发阈值与文案——**只改提示词与阈值，不改执行行为**，每次改动用批次 5 基线脚本回归 |
| 测试 | 查询层单测（构造 failed→failed→succeeded 序列断言恢复率）；golden 输出对齐 |
| 回滚 | 纯只读查询，删除该诊断项即可 |

---

## 5. 契约与 DDL 明细

### 5.1 新增表结构（schema v2）

```sql
-- 工具调用生命周期：tool.requested 建行，tool.completed 补全
CREATE TABLE IF NOT EXISTS usage_tool_calls (
  tool_call_id            TEXT PRIMARY KEY,
  session_id              TEXT NOT NULL DEFAULT '',
  trace_id                TEXT NOT NULL DEFAULT '',
  turn_id                 TEXT NOT NULL DEFAULT '',
  step                    INTEGER NOT NULL DEFAULT 0,
  tool_name               TEXT NOT NULL DEFAULT '',
  source                  TEXT NOT NULL DEFAULT '',
  kind                    TEXT NOT NULL DEFAULT '',
  outcome                 TEXT NOT NULL DEFAULT '',      -- success|empty|partial|failed|''(在途)
  ok                      INTEGER,                        -- NULL=未知
  empty_result            INTEGER NOT NULL DEFAULT 0,
  error_code              TEXT NOT NULL DEFAULT '',
  retryable               INTEGER,                        -- NULL=未知
  failed_count            INTEGER NOT NULL DEFAULT 0,
  succeeded_count         INTEGER NOT NULL DEFAULT 0,
  started_at_unix_nano    INTEGER NOT NULL DEFAULT 0,
  completed_at_unix_nano  INTEGER NOT NULL DEFAULT 0,
  duration_ms             INTEGER NOT NULL DEFAULT 0,
  record_json             BLOB
);
CREATE INDEX IF NOT EXISTS idx_usage_tool_calls_session ON usage_tool_calls(session_id, started_at_unix_nano DESC);
CREATE INDEX IF NOT EXISTS idx_usage_tool_calls_name    ON usage_tool_calls(tool_name);
CREATE INDEX IF NOT EXISTS idx_usage_tool_calls_outcome ON usage_tool_calls(outcome, error_code);

-- 子代理完成记录：两个生产者归一化后的落点
CREATE TABLE IF NOT EXISTS usage_subagents (
  subagent_id             TEXT NOT NULL DEFAULT '',
  parent_session_id       TEXT NOT NULL DEFAULT '',
  child_session_id        TEXT NOT NULL DEFAULT '',
  role                    TEXT NOT NULL DEFAULT '',
  read_only               INTEGER NOT NULL DEFAULT 0,
  success                 INTEGER,                        -- NULL=未知（历史缺字段行）
  completion_reason       TEXT NOT NULL DEFAULT '',       -- completed|failed|stopped|timeout|cancelled|budget_exceeded|unknown
  failure_category        TEXT NOT NULL DEFAULT '',       -- 见 D5 枚举
  error_code              TEXT NOT NULL DEFAULT '',
  attempt                 INTEGER NOT NULL DEFAULT 1,
  max_attempts            INTEGER NOT NULL DEFAULT 1,
  retry_reason            TEXT NOT NULL DEFAULT '',
  id_synthesized          INTEGER NOT NULL DEFAULT 0,
  duration_ms             INTEGER NOT NULL DEFAULT 0,
  started_at_unix_nano    INTEGER NOT NULL DEFAULT 0,
  completed_at_unix_nano  INTEGER NOT NULL DEFAULT 0,
  usage_total_tokens      INTEGER NOT NULL DEFAULT 0,
  source                  TEXT NOT NULL DEFAULT '',       -- scheduler|agent_controller
  conflict_count          INTEGER NOT NULL DEFAULT 0,
  record_json             BLOB,
  PRIMARY KEY (subagent_id, parent_session_id)
);
CREATE INDEX IF NOT EXISTS idx_usage_subagents_session ON usage_subagents(parent_session_id, completed_at_unix_nano DESC);
CREATE INDEX IF NOT EXISTS idx_usage_subagents_fail    ON usage_subagents(success, failure_category);

-- 回合级终值：session_end 为权威，session_start 建骨架
CREATE TABLE IF NOT EXISTS usage_turns (
  session_id                  TEXT NOT NULL,
  turn_id                     TEXT NOT NULL,
  trace_id                    TEXT NOT NULL DEFAULT '',
  success                     INTEGER,                    -- NULL=未知
  completion_reason           TEXT NOT NULL DEFAULT '',
  error_code                  TEXT NOT NULL DEFAULT '',
  steps                       INTEGER NOT NULL DEFAULT 0,
  duration_ms                 INTEGER NOT NULL DEFAULT 0,
  tool_error_count            INTEGER NOT NULL DEFAULT 0,
  recovered_tool_error_count  INTEGER NOT NULL DEFAULT 0,
  unrecovered_tool_error_count INTEGER NOT NULL DEFAULT 0,
  prompt_tokens               INTEGER NOT NULL DEFAULT 0,
  completion_tokens           INTEGER NOT NULL DEFAULT 0,
  total_tokens                INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens           INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens            INTEGER NOT NULL DEFAULT 0,
  started_at_unix_nano        INTEGER NOT NULL DEFAULT 0,
  ended_at_unix_nano          INTEGER NOT NULL DEFAULT 0,
  record_json                 BLOB,
  PRIMARY KEY (session_id, turn_id)
);
CREATE INDEX IF NOT EXISTS idx_usage_turns_time ON usage_turns(session_id, ended_at_unix_nano DESC);
```

> 实现注意（批次 1.2 前置核对）：`usage_turns` 的列名（如 `tool_error_count` / `recovered_tool_error_count` / usage_* 细分）必须与 `session_end` 事件载荷的实际字段逐个对齐后再写 handler；对不上的列先写 0 并在 `record_json` 保留原始载荷，禁止把未确认字段名当既有契约使用。迁移收尾设置 `PRAGMA user_version = 2`。

### 5.2 `subagent.completed` 归一化映射表

| 输入（生产者/历史数据） | 规范化输出 | 备注 |
| --- | --- | --- |
| `success=true`（scheduler） | `success=true, completion_reason=completed` | 直接采用 |
| `status=idle`（agent_controller 镜像） | `success=true, completion_reason=completed` | 与 :1080-1085 现有映射一致 |
| `status=stopped` / `interrupted` | `success=false, completion_reason=stopped` | 终态映射为 canceled，统计归"未完成" |
| `status=failed` / `error` | `success=false, completion_reason=failed` | 需补 `failure_category` |
| `success` 与 `status` 并存且冲突 | 以 `success` 为准；`conflict_count += 1` | 冲突计数进状态快照 |
| 两者都缺（历史 26 行） | `success=NULL, completion_reason=unknown` | 统计单列"未知"，**不得**计入失败 |

`failure_category` 枚举（D5）：`provider_error`、`rate_limited`、`timeout`、`context_overflow`、`tool_error`、`budget_exceeded`、`cancelled`、`interrupted`、`unknown`。分类来源优先级：载荷显式 `failure_category` > `error_code` 经映射函数推导 > `unknown`。映射函数与 `backend/internal/llm/retry_policy.go:776`、`backend/internal/runtimeobserve/projector.go` 的既有分类保持同源，全仓只保留一处映射实现。

### 5.3 事件 → 分析表映射总表

| 事件（注册表名） | 目标表 | 关键来源字段 |
| --- | --- | --- |
| `session_start` | `usage_sessions`（meta）、`usage_turns`（骨架） | session_id、turn_id、trace_id、时间 |
| `session_end` | `usage_turns`（终值）、`usage_sessions`（聚合） | success、steps、duration、tool_error_count、usage_* |
| LLM 请求/响应（现役采集） | `usage_requests` | token、时长、model、`error_category` |
| `tool.requested` | `usage_tool_calls`（upsert 骨架） | tool_call_id、tool_name、step、trace_id |
| `tool.completed` | `usage_tool_calls`（补全） | outcome、ok、error_code、retryable、failed/succeeded_count |
| `subagent.completed` | `usage_subagents`（归一化 upsert） | 见 §5.2 |
| `tool_receipt_recorded` | 不落分析库 | 会话库 `session_tool_receipts` 自有职责 |

---

## 6. 测试与验收矩阵

| 批次 | 测试 | 命令（工作目录 `backend/`） |
| --- | --- | --- |
| 0.1 | `TestUsageAnalyticsAttachedAtBootstrap` | `go test ./cmd/... ./internal/usageanalytics/...` |
| 0.2 | 投递通道断言 + 回归 | `go test ./internal/events/... ./internal/api/skills/... ./internal/chat/...` |
| 0.3 | `TestToolReceiptPersistedOnToolCompletion` | `go test ./internal/chat/...` |
| 1.1 | `TestStoreMigratesV2Idempotent`、`TestReadOnlyOpenSkipsMigration` | `go test ./internal/usageanalytics/...` |
| 1.2 | ingest 表驱动单测（三表） | `go test ./internal/usageanalytics/...` |
| 1.3 | rollup golden 测试 | `go test ./internal/usageanalytics/...` |
| 2.1 | `TestSubagentCompletedPayloadNormalized` | `go test ./internal/agent/... ./internal/api/skills/... ./internal/usageanalytics/...` |
| 2.2 | 幂等/冲突单测 + 历史回放脚本 | 同上 + 附录 A.5 |
| 3.x | `TestAnalyticsHandlers*` | `go test ./internal/api/skills/...` |
| 4.x | `stats_test.go` | `go test ./cmd/aicli/...` |
| 6.1 | `TestRenderSubagentResultsCarriesRetryAdvice` 等 | `go test ./internal/agent/... ./internal/usageanalytics/...` |
| 6.2 | 重试策略表驱动测试（瞬态/写任务/熔断） | `go test ./internal/agent/...` |
| 6.3 | 部分结果保留测试（前置条件满足时） | `go test ./internal/agent/...` |
| 6.4 | 诊断查询单测（恢复率/重复率） | `go test ./internal/usageanalytics/... ./cmd/aicli/...` |
| 全量 | 构建与总测试 | `go build ./cmd/runtime-server ./cmd/aicli`；`go test ./...` |

**端到端手工验收（每批次结束跑一次）**：真实 chat 会话执行含工具调用与子代理的回合 → 按对应批次的小节验收项核对两库数据 → 运行 `aicli stats doctor` 输出健康。

---

## 7. 风险、回滚与兼容

| 风险 | 说明 | 缓解 |
| --- | --- | --- |
| 多进程并发迁移 | aicli 与 runtime-server 可能同时 `Open` 写库 | 复用 `store.go:104-116` 锁退避；新表 `CREATE IF NOT EXISTS` 幂等；busy_timeout 沿用现配置 |
| 采集热路径开销 | ingest 在事件总线同步路径 | 写入失败只降级日志不反压；必要时对高频 `tool.*` 采用批量事务（实现时实测再定） |
| 隐私 | 工具参数/错误文本可能含敏感信息 | 分析库只落 `error_code`/`failure_category` 等枚举，原始载荷仅存 `record_json` 且需在实现时确认脱敏范围（低敏原则，§1.2） |
| 兼容性 | 未知消费方读取分析 API / CLI | 只增字段不改旧字段；`analytics_handlers_test.go` 既有断言必须保持通过；D7 约定 |
| 历史数据 | 271 行双口径旧数据 | 按 §5.2 兼容读取，unknown 单列；可选回放归一化，不强制 |
| 自动重试副作用 | 写任务重试可能造成重复文件/命令副作用 | 6.2 仅对只读任务自动重试；写任务只出建议；`MaxAttemptsPerTask=1` 可一键关闭 |
| Agent 行为回归 | 批次 6 改动渲染文本与执行路径，可能触碰既有 agent 测试/提示词断言 | 6.1 纯增量文本、6.2 配置门控且默认只作用于只读任务；每项独立回滚；6 完成后用批次 5 基线脚本重跑对比 |
| 回滚 | 新表是新数据面 | 批次 0.4 归档脚本支持 `-Restore`；新表可直接 drop（`user_version` 改回 0 的脚本在 runbook 提供）；事件契约与生产者改动均为增量字段，可单独回退 |

---

## 8. 排期与批次状态表

| 批次 | 优先级 | 预估 | 依赖 | 状态 | 落地位置 / 完成判据 |
| --- | --- | --- | --- | --- | --- |
| 0.1 采集启动即 attach | P0 | 0.5 人日 | — | ✅ 代码完成 | aicli 启动期挂载（`chat_cache_local.go` + `chat_actor_host.go` 初始化链）；runtime-server 侧启动装配 + `usage_analytics` 健康块见 `internal/api/skills/{handler.go,analytics_handlers.go}`；实机 `usage_* > 0` 待真实会话复核 |
| 0.2 `tool.*` 落库修复 | P0 | 0.5 人日 | — | ✅ 完成 | 本地 A 通道桥 `localChatRuntimeHost.bindRuntimeEventPersistence`（`cmd/aicli/commands/chat_actor_host.go`）+ 别名单一实现 `internal/events/session_store_alias.go`；回归测试 `TestLocalHostPersistsToolLifecycleEvents` / `TestLocalHostEventPersistenceSkipsAlreadyPersistedAndLiveOnly` |
| 0.3 工具回执写入 | P0 | 0.5 人日 | — | ✅ 完成 | 终局回执补齐 `internal/chat/tool_receipt_reconcile.go`（含失败分支 `ok=false`；已回放消费的回执不复活）；测试 `TestToolReceiptPersistedOnToolCompletion` |
| 0.4 旧表归档 + 基线脚本 | P0 | 0.5 人日 | — | ✅ 完成 | `backend/scripts/usage-analytics-{archive-legacy,baseline}.ps1`（幂等、`-Restore`、`-Json`）；实机已归档（备份 `~/.aicli/sessions/runtime/backup/20260917-121922/`） |
| 1.1 schema v2 | P0 | 0.5 人日 | 0.4 | ✅ 完成 | `internal/usageanalytics/store.go`（三表 + 索引 + `PRAGMA user_version=2`）；测试 `TestStoreMigratesV2Idempotent` / `TestReadOnlyOpenSkipsMigration` |
| 1.2 ingest 扩展 | P0 | 1 人日 | 1.1 | ✅ 完成 | `internal/usageanalytics/ingest_v2.go`（tool/subagent/turn 三类 handler + 幂等 upsert）；测试 `TestCollectorIngestsSchemaV2Events` 等 |
| 1.3 rollup/诊断 | P1 | 1 人日 | 1.2 | ✅ 完成 | `internal/usageanalytics/query_v2.go`（ToolStats/SubagentStats/ErrorPatterns + rollup/诊断扩展）；测试 `TestStoreV2StatsQueries` / `TestSessionUsageCarriesV2Dimensions` |
| 1.4（可选）历史 backfill | P2 | 1 人日 | 1.2 | ⏸ 跳过（默认不迁） | 依 §1.2 边界：历史死表只归档；如需历史可见再单独实施显式子命令 |
| 2.1 载荷归一化 | P0 | 1 人日 | — | ✅ 完成 | 写侧 `usageanalytics.NormalizeSubagentCompletionPayload`（scheduler / api+cli agent-controller 镜像共用）+ 读侧 `NormalizeSubagentCompletion`；测试 `TestSubagentCompletedPayloadNormalized` |
| 2.2 ingest 幂等去重 | P0 | 0.5 人日 | 2.1 | ✅ 完成（回放脚本未做） | `usage_subagents` 主键 upsert + 冲突计数；历史 271 行回放脚本（附录 A.5）未实施，读取侧已兼容 |
| 3.1 API 端点 | P1 | 1 人日 | 1.3 | ✅ 代码完成 | `internal/api/skills/analytics_handlers.go` + 路由挂载；测试 `TestAnalyticsHandlersToolStats/SubagentStats/ErrorPatterns`；curl 冒烟待服务进程 |
| 3.2 健康快照 + 词表对齐 | P1 | 0.5 人日 | 0.1 | ✅ 代码完成 | `runtimeStatusSnapshot["usage_analytics"]`（attached/db_path/ingested_total/conflict_total/last_ingest_at）+ `internal/usageanalytics/query_v2_health.go`；测试 `TestAnalyticsHandlersUsageAnalyticsHealthBlock` |
| 4.1 CLI 命令 | P1 | 1.5 人日 | 1.3 | ✅ 完成 | `backend/cmd/aicli/commands/stats.go`（sessions/session/errors/subagents/doctor + `--json`/`--db`）；`go run ./cmd/aicli stats doctor` 手工验证通过 |
| 4.2 退出码与 doctor | P1 | 0.5 人日 | 4.1 | ✅ 完成 | 退出码 0/1/2 约定 + `docs/plan/session-analytics-runbook.md` |
| 5.x 基线与文档 | P2 | 1 人日 | 1–3 | ✅ 完成 | `docs/analysis/session-analytics-baseline-20260917.md`、`docs/analysis/README.md` 勘误指引、本表回填 |
| 6.1 恢复建议 | P1 | 1 人日 | 2.1 | ✅ 完成 | `internal/agent/subagent_retry.go`（分类→建议映射）+ `renderSubagentResults` 追加 `retry_advice=`；测试 `TestRenderSubagentResultsCarriesRetryAdvice` 等 |
| 6.2 有界重试 | P1 | 1–1.5 人日 | 2.1 | ✅ 完成 | `SubagentSchedulerConfig.MaxAttemptsPerTask`（默认 2、1=关闭、封顶 5）+ 只读瞬时重试 + 指数退避 jitter 封顶 5s；测试 `TestSubagentAutoRetryOnlyTransientReadOnly` 等 |
| 6.3 部分结果降级 | P2 | 1 人日 | 6.1 + 前置核对 | 🟡 部分实施 | 生产者侧已落 `completion_reason=budget_exceeded/context_overflow` + Findings/Patches 保留 + `split_task` 建议；分析侧「部分产出单列」待 ≥1 周数据后补 |
| 6.4 采纳率指标 | P2 | 0.5–1 人日 | 1.2、4.1 | ⏸ 数据触发后实施 | 依 §1.2 边界：需 ≥1 周真实数据后再定阈值与文案 |
| 7.1 micro web 分析页签 | P1 | 1–1.5 人日 | 3.1、3.2 | ✅ 代码完成 | `cmd/aicli/commands/web/{index.html,js/analysis.js,app.js,js/ui.js}` + `web_analysis_handlers.go`（薄 adapter，直接序列化 usageanalytics 查询结构体）+ `web_handlers_test.go` 页签断言；`go test ./cmd/aicli/commands/ -run 'Web|Analysis'` 通过 |
| 7.2 frontend 观测 tab | P1 | 1.5–2 人日 | 3.1、3.2 | ✅ 代码完成 | `frontend/src/pages/usage-analytics/{tool,subagent,error-patterns}-panel.tsx` + sessions/overview/charts + i18n + vitest + `e2e/usage-observability.spec.ts`；`npm run lint`、`npx tsc -b` 通过（e2e 未运行，见 §10） |
| 7.3 TUI `/usage` 扩展 | P1 | 1–1.5 人日 | 1.3、3.2 | 🟡 部分完成 | `chat_usage_tools.go` 新增 `/usage tools|subagents|errors` + 首行采集健康 + 空库降级；`go test ./cmd/aicli/commands/ -run Usage` 通过；§9.3 的 T1（`/status` 采集健康行）与 T3（Agent Panel outcome 列）未实现（超出本次改动面） |

**建议实施顺序**：0.1 → 0.2 → 0.3 → 0.4（可并行 2.1）→ 1.1 → 1.2 → 1.3 → 2.2 → 3.1 → 3.2 → 4.x → 7.x（三端呈现，可与 5.x 并行）→ 5.x；6.1/6.2 可在 2.1 完成后与 3.x/4.x 并行，6.3/6.4 待 ≥1 周真实数据后实施。

---

## 9. 三端呈现细化（frontend / TUI / micro web client）

> 前置：批次 1.3（查询层 rollup/诊断）、3.1（HTTP 端点）、3.2（采集健康快照）。
> 形态原则（延续 `cacheanalytics` 既定设计，见 `llm-cache-analytics-unified-plan.md` §3.1/§6/§7.1）：**采集与聚合只做一次、HTTP 契约只定义一次、三端各自消费同一 Source / 同一契约**。三端不得自造第三套状态词表（统一复用 `execution_diagnostics.go` 归一表），不得绕过统一降级语义（`not_reported→--`、空库→"暂无数据"、覆盖率不足→横幅标注）。

### 9.1 aicli micro web client：新增"分析"页签

**形态**：micro web client 是 `aicli` 的 `--pprof` 内嵌 Web 服务（`backend/cmd/aicli/main.go:198`；默认 `127.0.0.1:0` 随机端口，`main.go:91`；启动 `pprof.go:95-104`，mux 注册 `pprof.go:221-249`），默认入口 `http://127.0.0.1:<port>/web/`（`chat_debug_pprof.go:74-82`）。前端为 `//go:embed web` 的内嵌静态 ES-module SPA（`web_page.go:13-14`），无构建步骤。

**改动文件**（全部在 `backend/cmd/aicli/`）：

| 层 | 文件 | 改动 |
| --- | --- | --- |
| 页签骨架 | `commands/web/index.html` | tabs 区（`:85-93`）追加"分析"按钮 + 面板骨架（overview 卡片行 + 工具/子代理/失败三张表 + 下钻弹层）；**必须同步 `commands/web_handlers_test.go:141-196`（`TestHandleChatWebPage_Tabs` 锁定页签 id 与顺序）** |
| 页签逻辑 | `commands/web/js/analysis.js`（新增） | 照 `js/cache.js` 模式：`fetch(..., {cache:"no-store"})`（`cache.js:22-28`）、表格渲染、过滤、下钻；仅页签激活时拉数据 |
| 注册 | `commands/web/app.js:5-15,18-30`、`commands/web/js/ui.js:10-16,54-84` | import 新模块、元素引用、`activateTab` 懒加载分支 |
| 薄 adapter | `commands/web_analysis_handlers.go`（新增，照 `web_cache_handlers.go:18-53`） | 当前会话→Source 回退→缺省 session_id→委托共享 handler；失败写稳定码 `analytics_disabled`（对齐既有 `cache_analytics_disabled` 风格） |
| 路径常量/SSE | `commands/web_schema.go:19-30,73+` | 注册 `/web/api/analysis/*` 常量；v1 不新增 SSE 事件（按需拉取）；P2 可选 `analytics_rollup_updated` |
| mux | `pprof.go:221-249` | 注册新前缀与子路由（含尾斜杠双注册，照 `:244-245`） |

**共享契约落点**：批次 3.1 的查询端点实现为**单点 handler**（建议 `internal/usageanalytics/httpapi`，对照 `internal/cacheanalytics/httpapi.go` 的 `Handler:12-20`/`Mount:35-45`），两处挂载：
- runtime-server：`/api/runtime/analytics/{tools,subagents,errors}`（批次 3.1）；
- aicli micro web：`/web/api/analysis/*`（本页签）。

即"HTTP 契约只定义一次"（§9 引言），禁止两侧各写一套查询/投影（`web_cache_handlers.go` 已是该形态的正确先例）。

**页签布局**（示意）：

```
┌─ 分析 ────────────────────────────────────────────────┐
│ [工具 42 · 失败 3 · 失败率 7.1%] [子代理 18 · 完成率 83%] │
│ 失败分类: ●timeout 2 ●denied 1        [刷新] [自动 ⟳]    │
│ [工具表]   名称 │ 调用 │ 失败 │ p50 │ p95  → 点击行下钻    │
│ [子代理表] 目标 │ 来源 │ 状态 │ 分类 │ 重试 │ token │ 耗时 │
│ [失败 Top] code/category/count/rate（按会话或全局）        │
└───────────────────────────────────────────────────────┘
```

**降级**：空库→"暂无数据"；`attached=false`→页签顶部健康条（与 9.2 横幅同文案）；服务缺失→`analytics_disabled` 稳定码。会话切换复用现有 sidebar 逻辑，切换后清空重拉（cache 页签先例）。

**测试**：更新 `web_handlers_test.go` 页签清单；新增 `web_analysis_handlers_test.go`，照 `web_cache_handlers_test.go:88-139`（`newCacheTestSession` 的 `LocalRuntimeHost`+EventBus+临时 SQLite+attach service）与 `:193-203` 直调 handler 模式；可选 `pprof_test.go:49-54` 起真实 mux 端到端。

### 9.2 frontend（React SPA）：会话观测 tab 与全局失败视图

**入口决策**：不新增顶级路由与导航。全局失败/效率指标进现有 `/usage` Overview；会话级工具/子代理/失败明细进 `/usage/sessions/:sessionId` 的新 tab。理由：主导航只有工作台顶栏一个 `/usage` 入口（`components/workspace/workspace-shell-topbar.tsx:327-334`），缓存面板先例同样是页内 panel 而非独立路由（`/usage/cache` 路由已删除且不做重定向，`pages/usage-analytics-page.tsx:2`；单测断言见 `pages/usage-analytics-page.test.tsx:134-136`、`pages/cache-analytics-page.test.tsx:143-144`）。

**改动文件**（`frontend/src/`）：

| 文件 | 改动 |
| --- | --- |
| `api/runtime/analytics.ts` | 新增 `listAnalyticsTools/getAnalyticsSubagents/getAnalyticsErrors`，映射批次 3.1 的三个端点；沿用 `fetchRuntimeJson` + `Authorization: Bearer <adminToken>` 模式（`analytics.ts:16-24`） |
| `types/runtime/analytics.ts` | 新增 `ToolStat/SubagentStat/ErrorPattern` 类型（与 §5 契约字段一一对应；现有 `Diagnostic` 在 `:216-223`，扩展而不替换） |
| `pages/usage-analytics/sessions.tsx` | tab 机制从 `?tab=tokens`（`:148`、`:203-206`）扩展为 `overview/tokens/tools/subagents/diagnostics`；`Diagnostics` 区块（`:238-259`）升级为独立 tab 并支持 Top-N 下钻 |
| `pages/usage-analytics/tool-stats-panel.tsx`（新增） | 工具维度：调用次数/失败率/耗时 p50-p95，按 outcome 过滤，行内可展开失败样本 |
| `pages/usage-analytics/subagent-stats-panel.tsx`（新增） | 子代理维度：完成率、`failure_category` 分布、来源（scheduler/spawn_team）分布、重试次数、token 与耗时 |
| `pages/usage-analytics/error-patterns-panel.tsx`（新增） | `error_code/category` Top-N + 会话/时间窗过滤；点击行跳转 `?tab=diagnostics` 并带入过滤条件 |
| `pages/usage-analytics/overview.tsx` | 指标行（`:179-186`，现有 6 卡已含 `llmErrorRate/failedTurns/toolErrorRate`）追加"子代理失败率"；`attached=false` 时在 `QualityNotice` 位置（`:171-177`）显示采集健康横幅并链到 `/logs` |
| `pages/usage-analytics-charts.tsx` | 复用现有 bar 组件（`:30-151`）新增"失败分类分布"图，点击 bucket 走既有 `onSelect` 过滤 |
| `i18n/resources/zh-CN/usage-analytics.ts`、`en-US/usage-analytics.ts` | 新 key；en-US 经 `DeepStringShape` 与 zh 键树编译期对齐（`resources/shape.ts:1-5`） |
| `e2e/usage-observability.spec.ts`（新增） | `page.route("**/api/runtime/analytics/**")` 打桩（照 `e2e/usage-quota.spec.ts:55-93`），断言 tab 切换、空库降级（空数组→"暂无数据"）、采集健康横幅、admin token 403 提示 |

**交互与降级**：
- 切 tab 只改 URL query（现有 `selectTab` 先例 `:174-181`），刷新/分享可复现视图；
- 数据在 tab 激活时并行拉取，失败沿用页面本地 `loading/error` + `role="alert"` 模式（`overview.tsx:38-39,171-177`）；
- 空库返回空数组（批次 3.1 语义）→ 面板渲染"暂无数据"，不得显示空图或 500；
- 不新增 SSE；需要近实时时复用 workspace 事件流 hook 触发重拉（缓存方案 §6.2 先例）；
- 新组件受 `scripts/verify-max-lines.mjs`（≤500 非空行）与 `scripts/verify-frontend-i18n.ts`（禁 JSX 裸文本/aria-label 等硬编码）双重门禁，一律独立文件。

**验收**：`npm run lint`（含两个 verify 脚本）+ `npm run test`（vitest：面板渲染与降级分支）+ `npm run test:e2e`（新 spec）。

### 9.3 aicli chat TUI：`/usage` 聚合视图与 Agent Panel 增强

**现有可复用资产**（不重建）：
- `/usage` 备用屏查看器：`UsageScreenRequest`（`chat_command_result.go:92`）+ 模式常量 `overview/requests/trace`（`chat_usage_screen.go:22-29`）+ ScreenLease 降级契约（`chat_usage_screen.go:36-46,56-90`）；
- Agent 控制面板：`/debug panel [limit] [follow] [full] [timeout] [nav …]`，Agents/Mailbox/Timeline 三窗格模态（`chat_debug.go:1603-1615,1808-1888`）、live watcher（`:1890-1933`）、摘要行 `chatAgentPanelSummaryLines`（`:1987`）、单 agent 行 `chatAgentPanelSummaryAgentLine`（`:2047`）；
- 会话内子代理生命周期已上时间线：`subagent.*`/`subagent.batch.*` 渲染（`chat_runtime_events.go:7229-7266`）、`renderSubagentCompletedTimelineEvent`（`:9165`）；
- `/status` 信息盒行清单（`chat_status.go:93-133`）。

**改动清单**：

| 步骤 | 文件 | 内容 |
| --- | --- | --- |
| T1 采集健康入 `/status` | `chat_status.go` | 行清单追加 `Usage analytics`：attached / db_path / ingested_total / last_ingest_at（取批次 3.2 快照）；未启用显示"未启用"，不新增错误分支 |
| T2 聚合子命令 | `chat_usage_command.go`、`chat_usage_screen.go`、`chat_command_result.go` | `UsageScreenRequest.Mode` 扩展 `tools`/`subagents`/`errors`；新增 `/usage tools [N]`、`/usage subagents [N] [--failed]`、`/usage errors [top N]`；备用屏与 plain/JSON 文档回退同步实现（`chat_usage_screen.go:56-68` 的降级路径必须覆盖新模式） |
| T3 Agent Panel 增强 | `chat_debug.go` | Agents 窗格行（`:2047`）追加 outcome 列（最近完成/失败、`failure_category`）；`nav` 支持 `failed` 快速定位失败目标；数据缺失时整列省略而非显示 0 |
| T4（可选，P2）会话收尾摘要 | `chat_runtime_events.go` | 会话结束输出一行"本会话工具失败 Top / 子代理失败率"，与批次 4.1 `stats` 同口径；积累 ≥1 周数据后实施 |

**数据源**：进程内直连 `usageanalytics` 查询层（批次 1.3 产物），**不 import `httpapi`、不发 HTTP**——与 `/usage cache` 的 `CacheAnalyticsSource` 同模式（`llm-cache-analytics-unified-plan.md` §6.4），是"同一后端，不同前端"在 TUI 的落点。Agent Panel 的实时行仍走既有 toolbroker/registry，仅聚合列来自分析库。

**渲染降级**（与 9.1/9.2 逐条一致）：`not_reported`→`--`；空会话→"当前会话暂无记录"；分析库缺失/未启用→"分析数据不可用（需批次 0/1 采集）"；备用屏不可用→文档 cell（现有契约，不新增分支）。

**测试**：扩展 `chat_usage_command_test.go`/`chat_usage_screen_test.go`（新模式、参数边界、降级）；Agent Panel 行渲染与 `nav failed` 沿用现有模态测试模式（`chat_composer_test.go:483` 系列）。

### 9.4 三端一致性对照（同一数据点，可得性一致）

| 数据点 | frontend | TUI | micro web client | aicli stats CLI |
| --- | --- | --- | --- | --- |
| 工具调用/失败/耗时 | `?tab=tools` 面板 | `/usage tools [N]` | "分析"页签-工具区 | `stats sessions/session` 明细 |
| 子代理完成率/失败分类 | `?tab=subagents` 面板 | `/usage subagents [--failed]` | "分析"页签-子代理区 | `stats subagents --failed-only` |
| 失败模式 Top-N | `?tab=diagnostics` 面板 | `/usage errors [top N]` | "分析"页签-失败区 | `stats errors --top N` |
| 采集健康 | Overview 横幅（attached=false） | `/status` 行 + `/usage` 首行 | 页签顶部健康条 | `stats doctor` |
| 实时运行态 | workspace 事件流（既有） | Agent Panel / timeline（既有） | SSE（既有通道） | 不适用（只读快照） |
| 降级语义 | 空数组→"暂无数据" | `--`/文档 cell | 同左 | 空库退出码 0 |

### 9.5 客户端批次与排期补充

| 批次 | 优先级 | 预估 | 依赖 | 状态 | 完成判据 |
| --- | --- | --- | --- | --- | --- |
| 7.1 micro web client"分析"页签 | P1 | 1–1.5 人日 | 3.1、3.2 | 未开始 | 页签渲染 + 降级用例 |
| 7.2 frontend 会话观测 tab | P1 | 1.5–2 人日 | 3.1、3.2 | 未开始 | tab/指标卡 + e2e |
| 7.3 TUI `/usage` 扩展 + Agent Panel | P1 | 1–1.5 人日 | 1.3、3.2 | 未开始 | 命令单测 + 降级用例 |

---

## 附录 A：复现与验收命令

> 以下命令在 PowerShell 下执行；`~` 指向用户主目录（`$HOME`）。库路径默认 `~/.aicli/sessions/runtime/usage_analytics.sqlite` 与 `session_runtime.sqlite`，可用 `AICLI_USAGE_ANALYTICS_DB` 覆盖。

### A.1 基线统计（§0.4 复现）

```powershell
python -c "import sqlite3,sys,os; p=os.path.expanduser('~/.aicli/sessions/runtime/usage_analytics.sqlite'); c=sqlite3.connect(p); [print(t, c.execute('select count(*) from '+t).fetchone()[0]) for t in ('usage_requests','usage_sessions','analytics_llm_requests','analytics_sessions','analytics_turns')]"
python -c "import sqlite3,os; p=os.path.expanduser('~/.aicli/sessions/runtime/session_runtime.sqlite'); c=sqlite3.connect(p); print('events', c.execute('select count(*) from session_events').fetchone()[0]); print(c.execute(\"select type,count(*) from session_events where type like 'tool.%' or type='subagent.completed' group by type\").fetchall())"
```

### A.2 采集恢复验证（批次 0.1）

```powershell
# 跑一次真实会话后执行，期望 usage_requests / usage_sessions 均 > 0
python -c "import sqlite3,os; p=os.path.expanduser('~/.aicli/sessions/runtime/usage_analytics.sqlite'); c=sqlite3.connect(p); print(c.execute('select count(*) from usage_requests').fetchone()[0], c.execute('select count(*) from usage_sessions').fetchone()[0])"
```

### A.3 API 冒烟（批次 3.1，服务已启动时）

```powershell
curl.exe -s "http://127.0.0.1:8080/api/runtime/analytics/sessions?limit=5"
curl.exe -s "http://127.0.0.1:8080/api/runtime/analytics/subagents?limit=5"
curl.exe -s "http://127.0.0.1:8080/api/runtime/analytics/tools?limit=5"
```

> 端口与路由前缀以 runtime-server 实际配置为准，落地时先确认。

### A.4 CLI 验收（批次 4.x）

```powershell
go run ./cmd/aicli stats doctor
go run ./cmd/aicli stats sessions --limit 10
go run ./cmd/aicli stats subagents --failed-only --json
```

### A.5 历史事件回放（批次 2.2，一次性）

回放脚本从 `session_runtime.sqlite` 读取 `subagent.completed` 事件，经 §5.2 归一化函数写入 `usage_subagents`；脚本需幂等（按主键 upsert），执行前备份分析库。脚本落地位置建议 `backend/scripts/usage-analytics-replay-subagents.ps1`，与批次 0.4 脚本同目录同风格。

---

## 10. 交付与验证记录（2026-09-17）

> 本节记录本轮实施的**实际**验证证据与未闭环项，供复核与后续接手使用。

### 10.1 已执行的验证（工作目录）

| 范围 | 命令 | 结果 |
| --- | --- | --- |
| 分析库/事件/会话/agent | `go test ./internal/usageanalytics/... ./internal/events/... ./internal/chat/ ./internal/agent/ -count=1`（`backend/`） | 全部 ok（使用 `-timeout 900s`） |
| HTTP API | `go test ./internal/api/skills/ -run 'Analytics' -count=1` | ok（新增 4 个 handler 用例） |
| CLI + micro web + TUI | `go test ./cmd/aicli/commands/ -count=1`（`backend/`） | ok（含 `stats`/`Web|Analysis`/`Usage` 新增用例；整包 91s） |
| 构建 | `go build ./cmd/aicli`、`go build ./internal/usageanalytics/... ./internal/agent/ ./internal/chat/ ./internal/api/skills/ ./internal/events/` | ok |
| 采集链路端到端（本地） | `TestLocalHostPipelinePersistsToolObservability` | ok：同一条 `tool.requested/completed` 同时落 `session_events`（`tool_started/tool_finished`）与 `usage_tool_calls` |
| 基线脚本 | `pwsh -File backend/scripts/usage-analytics-baseline.ps1 [-Json]` | ok（输出见 `docs/analysis/session-analytics-baseline-20260917.md`） |
| 归档脚本 | `pwsh -File backend/scripts/usage-analytics-archive-legacy.ps1`（先临时副本跑两遍 + `-Restore`） | ok：导出 → 重命名；第二遍 `already archived`；`-Restore` 可回滚；实机已归档 |
| CLI 手工验收 | `go run ./cmd/aicli stats doctor / sessions --limit 3 / subagents --failed-only --json / errors --top 5` | ok（空库降级：退出码 0、「暂无数据」、JSON 字段稳定） |
| frontend 门禁 | `npm run lint`（`frontend/`） | ok（0 error；i18n / max-lines / message-tokens 全过；2 条既有 warning 与本方案无关） |
| frontend 类型 | `npx tsc -b` | ok（修复了新面板 2 处 i18n 类型错误） |
| frontend 单测 | `npm run test`（vitest run） | ok：298 个文件 / 2420 个用例全部通过（含新增 `observability-panels.test.tsx` 7 例） |

### 10.2 未闭环项（需真实进程/环境，或按方案边界有意延后）

1. **真实会话验收**：`usage_requests/usage_sessions/usage_tool_calls/usage_subagents/usage_turns` 需要在**修复后的进程**里跑一次真实 chat 回合才会出现行（本机历史数据产生于修复之前）。验收命令见附录 A.2 与 `docs/plan/session-analytics-runbook.md`。
2. **curl 冒烟**（附录 A.3）：需启动 runtime-server；端点与鉴权已由 handler 单测覆盖。
3. **`go build ./...` 全仓**：本仓库当前有**另一条并行工作流**（supervision batch store / agent result 控制面，涉及 `internal/supervision`、`internal/toolbroker`、`cmd/runtime-server` 等）同时在改动，其编译状态在实施期间多次抖动；本方案涉及的包在上表命令中独立构建/测试通过。
4. **frontend e2e**：`frontend/e2e/usage-observability.spec.ts` 已落地，但未在本机运行 playwright（需要浏览器依赖）；按 `npm run test:e2e -- usage-observability` 执行。
5. **有意延后（方案自身边界）**：1.4 历史 backfill（默认不迁历史）、2.2 的历史回放脚本（附录 A.5）、6.3 分析侧「部分产出单列」、6.4 采纳率指标（均标注为数据触发后实施）、§9.3 的 T1/T3（`/status` 健康行与 Agent Panel outcome 列）。
6. **历史事件的工具观测空白**：批次 0.2/0.3 只对**新回合**生效；16062 行历史 `session_events` 中 `tool.*` 仍为 0，属预期（不做历史回填）。

### 10.3 实施期发现的额外问题（已一并修复）

- 分析库是单连接池（`SetMaxOpenConns(1)`）：`ToolStats` 原先在遍历结果集时嵌套查询耗时样本，会永久阻塞。已改为「先读完并关闭游标，再补齐 p50/p95 与 error_top」，并加注释防回归。
- 回执语义冲突：`session_tool_receipts` 的既有语义是"待回放结果、消费后删除"。终局补回执若不排除已回放消费的调用，会把陈旧结果重新写成待回放状态。已在 `SessionActor` 记录一次性回放标记并在补齐时跳过。
- 落盘别名：`tool.requested/tool.completed` 在**会话事件库**中必须保持历史别名 `tool_started/tool_finished`（前端 trajectory 回放契约有显式断言），分析库则消费总线原名。别名单点实现在 `internal/events/session_store_alias.go`。
