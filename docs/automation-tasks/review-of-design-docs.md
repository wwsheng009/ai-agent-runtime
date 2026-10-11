# 设计文档评审：主方案 / 数据存储 / 前端业务链条

> 评审对象：
> - [`analysis-and-implementation-plan.md`](./analysis-and-implementation-plan.md)
> - [`data-storage-design.md`](./data-storage-design.md)
> - [`frontend-business-chain.md`](./frontend-business-chain.md)
> （旁参：[`completeness-review-and-integration.md`](./completeness-review-and-integration.md)）
>
> 评审方式：逐条把文档中的**架构假设**与 `backend/` 真实代码核验；标注"已核实 / 需更正 / 缺失"。
> 结论：**方向正确、结构完整，但有 2 个阻断级事实错误、5 个重要缺口必须先修**，否则 P0 无法按文档落地。

---

## 1. 事实核验结果

| 文档假设 | 核验结果 | 证据 |
|---|---|---|
| `sqliteutil.OpenFile(dsn, true)` 提供 WAL/busy_timeout/单连接池 | ✅ 正确 | `internal/sqliteutil/sqliteutil.go:195` |
| `internal/migrate` + `schema_migrations` | ✅ 正确（`Migration{Version,Name,UpSQL}`、`Apply`） | `internal/migrate/migrate.go:16,43` |
| `state_version` CAS + `ensureXColumn` 增量列 | ✅ 正确 | `internal/background/store.go:278,944` |
| 子 Agent 判据 `agentcontrol.AgentRecord.Depth` | ✅ 正确 | `internal/agentcontrol/agent_registry.go:31,39` |
| 工具注册在 `toolbroker`，有参数审计表 | ✅ 位置正确 | `internal/toolbroker/broker.go:216`、`broker_arg_kinds.go` |
| 四端代码位置（frontend / micro web / TUI / console） | ✅ 正确 | `frontend/src/`、`commands/web/js/`、`cmd/aicli/ui/`、`cmd/gateway/console/` |
| **投递"复用现有 durable inbox + wake 通道"** | ❌ **错误** | 见 F1 |
| **投递"会话 flush 两阶段确认"** | ❌ **错误** | 见 F2 |
| **"接入已有会话归档/停止钩子"** | ❌ **过度乐观** | 见 F3 |
| **工具定义用 dsh 的 `defineTool({output.schema})`** | ❌ **错误** | 见 F4 |

---

## 2. 阻断级问题（必须改，否则 P0 不可落地）

### F1 投递机制表述错误（§6.3 / storage §8）

文档写"到期消息复用现有 **durable inbox + wake** 通道"。核验：

- 本项目确实存在 "durable inbox" 字样，但它是 **supervision 的通知存储**（`supervision/background_job.go:87`、`background_job_monitor.go:51` 的 "records a durable inbox item"），用于后台作业完成通知，**不是通用的"向会话注入 user 消息"API**。
- 真正的会话消息注入入口是 **`chat.SessionHub.GetOrCreate(sessionID)` → `SessionActor.SubmitPromptAsync(ctx, prompt, runMeta, opts...)`**（`chat/hub.go:157`、`chat/actor.go:593`）；同步版为 `SubmitPrompt`（`chat/actor.go:525`）。
- 语义差异：`SubmitPromptAsync` 会**真正启动一次模型 turn**（与 dsh 的 `followup` 触发一轮一致），而不是"静默追加一条消息"。因此回执应定义为"prompt 被 actor 接受"，而非"消息已持久化"。

**必改**：§6.3、§3.2、storage §8 全部改为"经 `SessionHub.GetOrCreate` 解析会话 → `SubmitPromptAsync` 投递提醒文本"；删除对 supervision durable inbox 的复用声明。

### F2 "会话 flush 两阶段确认"不存在（§6.1/§6.3）

文档照搬 dsh 的"`followup` 入队 + `sessions.flush()` 确认 → 才写回执"。核验：

- 本项目**没有 `sessions.flush` 这一 API**；会话持久化由 `chat` 包的 session store 异步写（`chat/sqlite_storage.go`），事件通过 `internal/events` 发布。
- 因此文档 §4.3"追加回执 + 推进任务同一事务"的前提不成立——**回执不是"投递确认"，而是"已提交 prompt"的记账**。

**必改**：重新定义投递确认语义，二选一并写入文档：
- 方案 A（推荐，简单）：确认 = `SubmitPromptAsync` 返回 nil（actor 接受），回执记 `deliveredAt`；
- 方案 B（严格）：确认 = 等待会话首个 user 消息落盘事件（需定义事件名与超时），复杂度高。

### F3 归档准入钩子不存在（§8.3）

文档说"接入本项目已有的会话归档/停止钩子（对齐 dsh `workspace/session-activity`）"。核验：

- 本项目有归档实现：`chat.Manager.Archive`（`chat/manager.go:465`）、`handler.ArchiveSession`（`api/runtimeapi/handler.go:3473`）、`BatchArchiveSessions`（`:3523`）、自动归档 `startArchive`（`chat/manager.go:579`）。
- 但**没有 dsh 式的可插拔"活动族准入"扩展点**；归档是直接执行。

**必改**：§8.3 改为"**新增**归档准入检查点"——在 `ArchiveSession`/`Manager.Archive` 前调用 `ScheduleService.ActiveTasks(sessionID)`，有活动任务则拒绝并返回可解释原因；stop 语义需显式实现。并标注这是**新增改动**而非"接入已有钩子"。

### F4 工具定义 API 用错（§7.2）

文档 §7 用 dsh 的 `defineTool({name, description, parameters, output:{schema,render}, execute})`。核验：

- 本项目是 `types.ToolDefinition{Name, Description, Parameters map[string]interface{}, ...}`，经 `Broker.Definitions()` 返回（`internal/toolbroker/broker.go:216`、`:406` 的 `ToolTaskOutput` 定义），工具接口见 `internal/toolkit/interface.go:22` 的 `Parameters() map[string]interface{}`。
- **本项目没有 dsh 的 `output.schema` / lossless-JSON 输出契约**，§7.2 的"输出 schema `oneOf`"不适用。

**必改**：§7 改写为本项目形式（`ToolDefinition` + `Parameters` JSON Schema + execute 分支），删除 output schema 段落；同时保留"参数审计表需同步"的结论（正确）。

---

## 3. 重要问题（影响正确性/工作量）

### F5 缺少鉴权说明（§9.1）
本项目 frontend 有 admin token（`frontend/src/lib/admin-token.ts`）。§9.1 未说明 schedule API 是否需要鉴权。dsh 明确"绑定校验 ≠ 调用者鉴权"；本项目必须补：API 鉴权级别、SSE 是否需要 token。

### F6 cron 自实现工作量被低估（§5.3、G5）
"自实现五字段解析器"是正确选择，但文档未给工作量/风险：解析 + 规范化 + DOM/DOW + 400 年搜索 + DST 叠加，属**数百行 + 大量边界测试**。建议在文档中标注为"独立子模块 + 独立测试集"，并评估是否可参考成熟库（同时保留规范化与 Vixie 语义自控）。

### F7 Go 的 DST 实现算法不完整（§5.2）
文档方向（构造后回读墙钟比较）正确，但**重叠（fall-back）需要枚举同一墙钟的两个 offset 并取较小者**，缺口检测需要 `+1h` 探测；Go `time` 无 `Temporal.PlainTime` 语义。建议补一段 Go 具体算法与测试用例（缺口/重叠各 2 例）。

### F8 缺少投递重试/死信策略（§6.1）
drive 只写"失败成员保留定时义务"。对照 `background` 的 `RecoveryBackoffSchedule`（`internal/config/manager.go:385,595`），Schedule 需要明确：**最大重试次数、退避、以及"永久不可达会话"（已删除/已归档）如何终止**，否则每轮重试会空转。

### F9 "单定时器 vs wake"未决（§6.2 vs storage §3.4）
main plan 说"可复用 wake"，storage doc 把 `schedule_wake_link` 列为 P2 可选。两处需**统一为明确决策**：建议 P0 = 进程内单定时器 + 启动扫描；P2 = 引入 wake（届时建 `schedule_wake_link`）。

### F10 多实例 owner 列跨库不可复用（storage §6）
storage doc 说"复用 background 的 `state_version`/owner 范式"。CAS 可复用（同一实现思路），但 **`runtime_instances` 表在 `background.sqlite`，与 `schedule.sqlite` 不同库**，不能共用实例注册。需明确：schedule 自建实例/租约，或复用 supervision 的实例注册。

---

## 4. 建议级问题（澄清/补全）

| # | 问题 | 建议 |
|---|---|---|
| F11 | P0-2 写"六种规则创建"与 P0 范围（after/at/every）矛盾 | 统一为"P0 三选一、P1 三钟表" |
| F12 | `schedule_wake_link.wake_id` 直接引用 supervision 内部 id | 只用 wake API 返回值，不建跨模块 FK |
| F13 | micro web 静态不变量约束未在 frontend 文档重申 | 引用 `docs/aicli/web-testing.md` 的 `var(--token)` 约束 |
| F14 | SSE 无鉴权、删除不可撤回未显式声明 | 补安全边界小节 |
| F15 | 前端面板 tone/widthClass 未定 | `panel-registry.ts` 需新增 union 成员 + tone（建议复用 `artifact`）+ widthClass |
| F16 | storage doc 未写"任务删除后历史 CASCADE 是否启用 FK" | SQLite 默认 FK 关闭，需显式 `PRAGMA foreign_keys=ON` 或应用层同事务删 |
| F17 | 时间展示"设备时区"在 Go 侧还是前端 | 明确：后端一律 UTC，设备时区仅前端展示（避免后端依赖浏览器时区） |

---

## 5. 逐文档修订清单

### `analysis-and-implementation-plan.md`
- **改** §3.2/§6.3：投递改为 `SessionHub.GetOrCreate` + `SubmitPromptAsync`；删除 durable inbox/flush 表述（F1/F2）。
- **改** §6.1：补投递重试/死信策略（F8）。
- **改** §7.2：工具定义改 `types.ToolDefinition`；删 output schema（F4）。
- **改** §8.3：归档准入标注为**新增检查点**（F3）。
- **改** §5.2：补 Go DST 算法（F7）；§5.3 标注 cron 子模块工作量（F6）。
- **改** §9.1：补鉴权（F5）。
- **改** §10 P0-2：范围与 P0 一致（F11）。

### `data-storage-design.md`
- **改** §3.4：`schedule_wake_link` 与 F9 决策对齐；不建跨模块 FK（F12）。
- **改** §6：多实例 owner 不跨库复用（F10）。
- **改** §4.3：投递事务边界随 F2 重述（回执 ≠ flush 确认）。
- **补** §3.3：显式 `PRAGMA foreign_keys` 或应用层同事务删（F16）。
- **补** 后端 UTC / 前端展示边界（F17）。

### `frontend-business-chain.md`
- **补** §4.1：创建引导在 E1 的具体落点（`/workspace/chats/new`）。
- **补** micro web 静态不变量引用（F13）。
- **补** §4.3 保存链路的前端**冲突重拉**具体动作（F5 相关）。
- **补** 面板 tone/widthClass 取值（F15）。

---

## 6. 评审结论

| 文档 | 结构 | 正确性 | 可落地性 |
|---|---|---|---|
| 主方案 | ✅ 完整 | ⚠️ 有 2 处阻断错误（F1/F2）+ 工具 API 错误（F4） | 修 F1–F4 后可落地 |
| 数据存储 | ✅ 清晰 | ⚠️ 依赖 F2 的投递语义、F10 的多实例 | 修 F2/F10/F16 后可落地 |
| 前端业务链条 | ✅ 完整 | ✅ 无事实错误 | 补 F13/F15 后可落地 |

**总体**：文档的**架构判断与分层是正确的**，四端分工与 14 项业务场景覆盖完整；但**投递机制（F1/F2）是整条链路的地基，必须先更正并重新推导回执/事务语义**。建议：
1. 先修 F1–F4（阻断）；
2. 再定 F8/F9/F10（重要决策）；
3. 其余建议级问题随实现 PR 附带。

修完上述问题后，P0 可按修订后的 §10 开工。

---

## 7. 修订状态（2026-10-11 执行）

| 编号 | 问题 | 状态 | 落点 |
|---|---|---|---|
| F1 | 投递机制表述错误 | ✅ 已修 | 主方案 §3.1/§3.2/§6.3；存储 §8 |
| F2 | "会话 flush 两阶段确认"不存在 | ✅ 已修（采用方案 A） | 主方案 §6.1/§6.3；存储 §4.3 |
| F3 | 归档准入钩子不存在 | ✅ 已修（标注为新增检查点） | 主方案 §8.3 |
| F4 | 工具定义 API 用错 | ✅ 已修 | 主方案 §7.2 |
| F5 | 缺鉴权说明 | ✅ 已修 | 主方案 §9.1 |
| F6 | cron 工作量低估 | ✅ 已补提示 | 主方案 §5.3 |
| F7 | Go DST 算法不完整 | ✅ 已补算法 | 主方案 §5.2 |
| F8 | 缺投递重试/死信 | ✅ 已补 | 主方案 §6.1；存储 §10 |
| F9 | 单定时器 vs wake 未决 | ✅ 已定，**后被守护进程方案取代**：定时器移到独立 `cmd/scheduled`，**不用 wake** | 主方案 §6.2；存储 §3.4；[`scheduler-daemon-design.md`](./scheduler-daemon-design.md) |
| F10 | 多实例跨库不可复用 | ✅ 已修 | 主方案 §6.2/§6.4；存储 §6 |
| F11 | P0-2 与范围矛盾 | ✅ 已修 | 主方案 §10 P0-2 |
| F12 | wake_link 跨模块 FK | ✅ 已修 | 存储 §3.4 |
| F13 | micro web 静态不变量未重申 | ✅ 已补 | 前端 §5.1 |
| F14 | SSE 无鉴权未声明 | ✅ 已补 | 主方案 §9.1 |
| F15 | 面板 tone/widthClass 未定 | ✅ 已补 | 前端 §5.1 |
| F16 | FK 级联未说明 | ✅ 已补 | 存储 §3.3/§4.3/§10 |
| F17 | UTC/本地展示边界 | ✅ 已补 | 存储 §9；前端 §5.1 |

**结论**：F1–F17 全部落文档。三份文档当前版本可直接作为 P0 实施依据；剩余为**实现期验证项**（DST 边界用例、cron 子模块、`sqliteutil` 是否已开 `foreign_keys`、`SessionHub.GetOrCreate` 对冷会话/已归档会话的行为），应在 P0 首个 PR 内确认。
