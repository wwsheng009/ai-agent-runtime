# 05 · 方案审查与架构加固

> 审查对象：[01-requirements.md](./01-requirements.md) ~ [04-migration-roadmap.md](./04-migration-roadmap.md)
> 审查维度：架构完整性、消息分发可靠性与性能、多前端一致性、多通知通道治理、Bot 交互设计、现有架构需做的梳理。

## 0. 审查结论

**结论：方案方向正确（Provider/AIBackend 解耦 + Connector/Binding 两层配置 + Schema 驱动），但作为落地设计还缺三块硬骨头**：

| 缺口 | 现状证据 | 影响 | 处置 |
|------|----------|------|------|
| **G1 事件订阅无隔离/背压** | `events.Bus.Publish` 在发布者 goroutine **同步**遍历订阅者，无 recover、无超时、无队列（`backend/internal/events/bus.go:314-344`） | 一个慢/挂的 IM 发送会阻塞 agent 主循环；这是**对现有系统也成立**的风险 | P0：总线提供异步订阅原语（有界队列 + recover + 丢弃策略） |
| **G2 通知没有统一中台** | 四条独立链路：gateway `pushNotifier`（优先级/静默/合并/presence，`cmd/gateway/notify_policy.go`）；runtime-server supervision 收件箱（`supervision.Notification`）；React 端本地桌面通知（`frontend/src/hooks/workspace/agent-chat-turn/notifications.ts`）；aicli 终端标题/声音 | 同一事件可能在多个面重复提醒；无统一偏好/静默/去重；每加一个通道都要改宿主 | P0：抽 `internal/notify` 调度中台，IM 只是其中一个 Channel |
| **G3 多前端契约无护栏** | 三个前端（React / 微前端 / 网关 console）+ TUI，各自实现表单与状态；当前只有"文档约定" | 4 份实现漂移；改一个通道要动 4 处；行为不一致 | P0：单一 Schema 源 + 宿主无关 API 包 + **契约测试矩阵** + 前端快照测试 |

另外 **G4 交互设计**在 01/02 中只有骨架（命令列表、流式策略），缺会话模型决策、错误 UX、引导与限速设计；**G5 性能/SLO** 未量化；**G6 多实例语义**未定义。本文逐一补齐，并给出需要拍板的决策清单（§7）。

评分（满分 5）：

| 维度 | 现方案 | 目标 |
|------|--------|------|
| 架构分层 | 4 | 5（补两张中台图 + 并发模型） |
| 消息分发 | 3 | 5（可靠性分级/隔离/死信/限速） |
| 多前端一致性 | 3 | 5（契约包 + 测试矩阵） |
| 通知通道治理 | 2 | 5（`internal/notify` 中台） |
| Bot 交互 | 2.5 | 4.5（会话模型/错误 UX/引导/限速） |
| 现有架构梳理 | 3 | 4.5（分 P0/P1/P2 落地） |

---

## 1. 架构设计：补齐两张中台与并发模型

### 1.1 现状（事实）

```
gateway:        registry → pushNotifier(策略) → Web Push / ntfy        ； nodeHub(SSE, drop-oldest) → console
runtime-server: agent loop → events.Bus → supervision inbox / SSE 桥    ； React 页面 visibility→ 本地桌面通知
aicli:          chat actor → 终端标题/声音                              ； 微前端 SSE → 浏览器内提示
```

四条链路各自实现"什么算重要、要不要静音、发到哪里"，**没有共享策略**。

### 1.2 目标形态（两张中台并列）

```
                    ┌──────────────── internal/notify（通知中台，新）──────────────┐
宿主事件源 ──投影──▶│ 归一化 NotifyEvent → 策略链 → 通道注册表 → 投递账本/回执/熔断 │
（attention/       └───┬─────────┬─────────┬────────━┬──────────┬──────────────┘
 supervision/          ▼         ▼         ▼         ▼          ▼
 chat events）      WebPush    ntfy      IM(连接器)  SSE/toast   桌面/终端
                                    ▲
                    ┌───────────────┴── internal/connectors（连接器中台，本方案）──┐
    IM 来/回 ──────▶│ Provider(通道) + Backend(宿主 agent) + 队列/账本/路由/命令桥 │
                    └──────────────────────────────────────────────────────────┘
```

- `internal/notify`：**通道无关**的调度与治理（去重/优先级/静默/合并/在场/偏好/回执）。
- `internal/connectors`：**IM 双向**（收消息 → turn → 回复），其出站投递**复用** `notify` 的账本与重试；
  其"主动提醒"入口则是 `notify` 的一个 Channel（IM）。
- gateway 现有 `pushNotifier` 退化为 `notify` 的一个宿主适配器（策略内核抽走，行为由测试护栏保证不变）。

### 1.3 并发模型（必须显式定义）

| 层 | 模型 | 上界与回收 |
|----|------|-----------|
| 入站（webhook） | HTTP 短任务：校验 → 归一化 → 入队即返回 200；不在此处调用 agent | 请求级超时 5s；每连接器/每 IP 限流 |
| 入站（polling/WS） | 每连接器 1 个常驻 goroutine，游标持久化（Telegram offset / 微信 buf / QQ session+seq） | 退避重连 1s→30s；进程退出可续传 |
| 会话执行 | 每 `connector+chat` 1 个串行 worker（有界队列，默认 32，空闲 2min 回收） | 队列满：命令/终态优先，delta 丢弃并计数 |
| 出站 | 每通道一个调度池（并发上限 + 令牌桶：平台侧 QPS 与自身保护） | Provider 内部重试（指数退避 + Retry-After） |
| 事件消费 | **异步订阅**（有界队列 + panic recover），禁止在 `Publish` 调用栈上做 IO | 队列深度/丢弃/延迟进指标 |
| 通知中台 | 每通道独立 worker + 投递账本（幂等键） | 通道熔断（连续失败 N 次 → 降级/暂停） |

### 1.4 多实例语义

| 形态 | 语义 | 结论 |
|------|------|------|
| webhook 连接器 | 无状态，可多实例（负载均衡），依赖共享 store 或黏性路由 | P2 支持；首期单实例 |
| polling/WS 连接器 | 必须**单所有者**（沿用 codex-server `PollingOwnerKey` + 冲突报错） | 复用该模式；`/api/connectors/{id}` 暴露 owner |
| 通知去重 | 需共享账本才能跨实例去重 | 首期单实例；多实例时账本落共享存储（Redis/DB） |

---

## 2. 消息分发：可靠性与性能设计

### 2.1 可靠性分级（不同消息不同保证，避免"一刀切"）

| 类别 | 例子 | 保证 | 实现 |
|------|------|------|------|
| 入站命令/审批 | `/approve`、文本提问 | **至少一次**（去重后恰好一次语义） | `InboundLedger` claim/ack + 幂等键（provider+message_id）；失败可重放 |
| 出站回复 | agent 文本/媒体 | **幂等 + 有界重试**；流式编辑天然幂等 | `OutboundLedger`（幂等键 = connector+chat+turn+seq）；编辑式失败降级为新建消息 |
| 主动提醒 | 完成/待审批/出错 | **尽力而为 + 收件箱兜底**（可丢） | `notify` 账本 + 宿主 inbox（runtime-server 已有 supervision inbox） |
| 流式 delta | 打字机效果 | **可丢** | 有界队列 + 合并窗口（默认 800ms，文本变化才发） |

### 2.2 有序性

- 同一 IM 会话**严格有序**（单 worker）；跨会话并行。
- 编辑式流式必须串行提交：后续 update 若失败，退化为"下一条追加"而非覆盖（参考源实现 Telegram/微信策略）。
- 命令旁路：`/stop`、`/status` 走独立高优先通道（不等长回复队列），避免"卡在队尾"。

### 2.3 背压与隔离（G1 的解决）

- 总线提供 `SubscribeAsync(eventType, opts{QueueSize, DropPolicy, Timeout})`：
  - 每条订阅独立 goroutine + 有界 channel；
  - `DropPolicy`: `drop_oldest`（delta）/ `drop_newest`（告警）/ `block`（终态，带超时熔断）；
  - 每次调用 `recover()`，panic 只废该订阅并告警，不杀宿主。
- 连接器统一通过该原语订阅（不自建裸订阅）；这样**现有宿主也受益**（当前任何订阅者 panic 都会打到发布者调用栈）。
- 队列指标：`notify_sub_queue_depth{sub}`、`notify_sub_dropped_total{sub}`。

> **实现状态（2026-10-13，P-1/N1 已完成）**：`backend/internal/events/async.go` 已落地
> `b.SubscribeAsync(eventType, handler, AsyncOptions{Name, QueueSize, DropPolicy, BlockTimeout, OnDrop, OnPanic})`，
> 返回 `*AsyncSubscription`（`Stats()` 计数快照 / `WaitDrained(ctx)` / `Unsubscribe()`）；
> `DropPolicy` 提供 `DropOldest`（默认，delta）/`DropNewest`（告警）/`DropBlock`（终态，带超时）；
> 处理器 panic 被隔离计数，绝不传播到发布者。现有 `Subscribe/SubscribeCancelable` 语义与性能**不变**
> （`internal/events` 既有测试全绿，另有 9 个异步订阅单测覆盖顺序/隔离/背压/丢弃策略/退订）。

### 2.4 死信与重放

- 入站：`/api/connectors/{id}/inbound?status=failed` 列出并支持重放。
- 出站：死信表 + 手动重试；截图/媒体保留到成功或超期（默认 24h）。
- 参考源实现的 `wechat_failed_reply_recovery`（失败回复重放）作为通用能力，而不是微信专属。

### 2.5 限速

| 层级 | 默认 | 目的 |
|------|------|------|
| 平台侧 | 尊重 429/Retry-After，自动退避 | 不被封 |
| 连接器级 | 出站 N msg/s（按通道配置） | 保护自身与平台 |
| 用户级 | 每用户 M 条/分钟；超限提示一次后静默 5min | 防刷 |
| 会话级 | 并发 turn = 1；禁止重复提交 | 语义正确 |

### 2.6 性能目标（SLO 草案）

| 指标 | 目标 |
|------|------|
| IM→agent 首反馈（typing/ack） | P95 < 1s（不含 agent 执行） |
| agent→IM 终态回复 | P95 < 2s（相对 turn 结束） |
| 流式编辑间隔 | ≥ 1s，且文本有变化才编辑 |
| 单连接器吞吐 | ≥ 20 msg/s（轮询/短消息场景） |
| 单通道故障影响面 | 0（其它通道与会话不受影响） |
| 内存上界 | 每连接器队列 + 账本 ≤ 64MB；全局队列可配上限 |

---

## 3. 多前端：契约优先，禁止"前端各自为政"

### 3.1 前端清单与现状

| 前端 | 技术 | 配置能力现状 |
|------|------|--------------|
| gateway console | 原生 JS（`cmd/gateway/console/`） | 无设置页，仅 tokens/开关式控件 |
| aicli 微前端 | 原生 JS（`cmd/aicli/commands/web/js/`） | 有 `config-admin.js`/`provider-editor.js` 先例 |
| runtime-server 前端 | React/Vite（`frontend/`） | 有 `runtime-config-page.tsx` + i18n |
| aicli TUI | Go TUI | 仅少量开关，不适合复杂表单（**不承载连接器配置**） |

### 3.2 护栏设计

1. **单一契约包**：`internal/connectors/api`
   - `Schema()`（provider 字段/动作/模式）+ REST handler 构造器（`List/Upsert/Delete/Test/Logs/Bindings/Actions`）；
   - 鉴权以接口注入：`type Authorizer interface { Authorize(r *http.Request) error }`；
   - 三宿主仅做：实现 `Authorizer` + `Store` + `Backend`，挂载 handler。**业务逻辑零重复**。
2. **契约测试矩阵**：同一组 golden 用例（httptest）分别装配三种宿主，断言：
   - 列表脱敏（只回 `secret_keys`）、错误码、分页、测试消息、action 流程、未授权行为；
   - CI 门禁：矩阵任一宿主失败即禁止合并。
3. **前端渲染器规范**：`FieldSchema.Type → 控件` 映射固定表（text/secret/url/bool/enum/qr-action），
   三份实现共享同一份 JSON snapshot 测试；**Schema 变更需评审快照 diff**。
4. **降级原则**：前端对未知字段类型降级为文本输入并显示原始 key（保证核心版本先行、前端后随时不炸）。
5. **性能**：`GET /api/connectors` 分页；`GET /api/connectors/schema` 带 `ETag`（前端缓存）；
   状态变化走 SSE（可选）而不是前端轮询；日志接口强制 `limit`。

### 3.3 一致性检查清单（CI 常驻）

- [ ] 三宿主的 `/api/connectors*` 行为一致（契约矩阵）
- [ ] 三前端渲染同一 Schema 快照
- [ ] 新增 provider 时：核心单测 + Schema 快照 + 契约矩阵用例齐备
- [ ] 未配置连接器时三宿主回归全绿

---

## 4. 多通知通道治理：`internal/notify` 中台

### 4.1 现状盘点（为什么必须统一）

| 通道 | 触发源 | 策略现状 | 缺口 |
|------|--------|----------|------|
| Web Push | gateway attention | 优先级/静默/合并/presence（`cmd/gateway/notify_policy.go`） | 仅 gateway 有 |
| ntfy | gateway attention | 同上（`internal/gateway/ntfy`） | 同上 |
| SSE toast | 各前端 | 前端本地逻辑 | 无优先级/静默 |
| 浏览器桌面通知 | React 页面 | `visibilityState` 判断（`agent-chat-turn/notifications.ts:20-27`） | 与 Web Push 可能**重复** |
| supervision 收件箱 | runtime-server 事件 | 服务端持久化（severity/subject/wake） | 不投递到外部 |
| 终端标题/声音 | aicli chat | 本地 | 不跨设备 |
| **IM（本方案）** | 同上 | 待建 | 若各自为政，将成第 4 套策略 |

### 4.2 中台设计

```go
package notify

type Event struct {              // 宿主投影产物（与具体事件名解耦）
    Kind      string             // attention kind: approval|question|error|completed|waiting|job_failed|...
    Priority  Priority           // high|normal|low
    DedupKey  string             // 例: "gw|node|session|approval|req_123"
    Session   SessionRef         // 宿主无关引用（node/session 或 runtime session）
    Title, Body, URL string
    Audience  Audience           // owner / user(id) / device(id)
    TS        time.Time
}

type Channel interface {
    Name() string
    Kind() string                          // webpush|ntfy|im|sse|desktop|terminal|inbox
    Capacity() int
    Send(ctx context.Context, d Delivery) error   // 幂等：按 d.DedupKey 去重由账本保证
    Healthy() Health
    // 可选能力：回执/撤销/编辑（IM 流式用）
}

type Policy struct {
    QuietHours   []QuietRange
    Coalesce     CoalesceRule              // 低优先合并窗口
    Presence     PresenceResolver           // 用户在看 → 只留 toast/inbox
    Preferences  PreferenceStore            // 全局/会话/通道三级
}

type Dispatcher struct { /* channels, policy, ledger, metrics */ }
func (d *Dispatcher) Dispatch(ctx context.Context, ev Event) error
```

### 4.3 路由矩阵（默认策略）

| 优先级 | 在场（任一前端可见） | 后台/离线 | 通道 |
|--------|----------------------|-----------|------|
| high（approval/question/error） | toast + inbox | Web Push **且** ntfy **且** IM（去重后扇出，首个成功即记回执，其余按偏好保留） | 全通道，穿透静默 |
| normal（completed/waiting） | inbox + 轻提示 | 合并后按偏好：Web Push 或 IM（默认只其一，避免双响） | 合并窗 30s，静默期延后 |
| low（job 进度等） | inbox | 不发（除非显式订阅） | — |

要点：
- **跨通道去重**：同一 `DedupKey` 在所有通道共享账本 → 二次到达直接短路（解决"浏览器桌面通知 + Web Push + IM"三响）。
- **回执与降级**：通道失败 → 降级到次选（IM → ntfy → inbox）；连续失败熔断并告警。
- **偏好粒度**：全局开关 / 每会话静音 / 每通道最低优先级 / 免打扰时段；三前端用同一 Schema 渲染设置页。
- **迁移**：先把 gateway 策略内核（`notify_policy.go` + `push.go` 的 observe/dedup）抽进 `notify`，
  以现有单测作护栏（行为不变）；再逐通道接入。

---

## 5. Bot 交互设计（补齐）

### 5.1 会话模型（需决策 D2）

| 方案 | 说明 | 取舍 |
|------|------|------|
| **A. 1 IM 会话 ↔ 1 runtime session**（默认） | 首次来信自动建会话；`/new` 开新；`/sessions`+`/use N` 切换 | 简单、可预期；不支持"一个群多话题" |
| B. IM thread/topic ↔ session | Telegram topic、飞书 thread 各自成会话 | 体验好但映射复杂；作为可选策略 |

建议：首期 A + 群聊默认**忽略非 @bot 消息**；B 留配置开关。

### 5.2 命令集（最小集）

```
/start /help                    首次引导与帮助（含"本机 agent 在线/离线"）
/new [title]                    新建会话并切换
/sessions, /use N, /rename, /archive
/stop                           中断当前 turn（高优先旁路）
/status                         agent 状态/模型/队列深度（轻量）
/approve <id> /deny <id>        审批
/answer <id> <text|选项号>       回答
```
- 命令前缀可配（默认 `/`）；未知命令 → 提示一次，不做模糊猜测。
- 命令与自然语言混发：仅当首个 token 命中白名单才是命令。

### 5.3 回复 UX

| 场景 | 设计 |
|------|------|
| 流式 | 支持编辑的通道：先发占位（"思考中…"≤1 行），≥1s 且文本变化才编辑；不支持：typing/表情 + 终态一次性 |
| 长文本 | 分片（平台上限的 90%）；分片带序号，防乱序 |
| 富文本 | markdown→平台能力映射（飞书卡片/微信过滤/QQ markdown 开关），失败降级纯文本 |
| 媒体 | 入站：类型白名单 + 大小上限 + SSRF 防护；出站：能力不支持时文本占位 |
| 审批 | 文本命令 + `request_id` 短前缀；后续可加原生按钮（飞书卡片/Telegram inline keyboard） |
| 错误 | 翻译为人类可读 + 建议 + trace_id（与 Web UI 可对照）；连续失败提示"查看控制台日志" |

### 5.4 首次使用与安全交互

- 未授权用户：首次回复一次"未授权，请联系 owner 获取配对码"，随后静默并记日志。
- `/start` 输出配对说明（owner 在管理面生成一次性配对码，IM 内输入完成绑定）。
- 敏感操作（`/approve`）默认仅 owner / 白名单用户可执行。

### 5.5 限速与防滥用

- 每用户 QPS/条数限制；超限提示一次（5min 静默）；
- 单会话并发 turn=1，重复提交合并为"已在进行中"提示；
- 全局熔断：同一连接器连续失败 → 暂停收取（保留入站账本，恢复后重放）。

---

## 6. 现有架构梳理与优化项（分级落地）

| 级别 | 项 | 落点 | 理由 |
|------|----|------|------|
| **P0** | 事件总线异步订阅原语（队列+recover+丢弃策略+指标） | `backend/internal/events` | 保护 agent 主循环，对所有现有订阅者生效（G1） |
| **P0** | `internal/notify` 策略内核抽取（行为不变） | 新包 + `cmd/gateway` 改造 | 消除四套策略，IM 才能安全接入（G2） |
| **P0** | 通知语义对照表（attention ↔ supervision ↔ IM 文案 ↔ SLO） | `docs/bots/` + 代码常量校验 | 防止三处语义漂移 |
| **P0** | `internal/connectors/api` 契约包 + 契约测试矩阵骨架 | 新包 + CI | 多前端护栏（G3） |
| P1 | gateway webhook 命名空间 + 每连接器 secret + 限流复用 | `cmd/gateway` | 入站安全 |
| P1 | 三前端 Schema 渲染器 + 快照测试 | console/微前端/React | 配置体验 |
| P1 | 连接器诊断接口（状态/计数/最近错误/测试消息/重放） | `internal/connectors/api` | 可运维 |
| P2 | polling ownership + 共享 store（多实例） | `internal/connectors` | 横向扩展 |
| P2 | 配置 schema 版本化 + 迁移函数 | `internal/connectors` | 长期兼容 |
| P2 | 指标/日志字段统一（`notify_*` / `connectors_*`） | 三宿主 | 可观测一致 |

> 说明：P0-1 与 P0-2 是**对现有系统的加固**，独立于连接器也有价值；建议与本专题并行开工。

---

## 7. 决策清单（请拍板）

> **状态：已定稿**——按最佳实践确认的最终结论、依据与影响见
> [06-target-architecture.md](./06-target-architecture.md) §1（D1–D10）。下表保留原始选项与本轮建议，作为决策记录。

| 编号 | 决策 | 选项 | 建议 |
|------|------|------|------|
| D1 | 通知去重/在场的**权威源** | gateway presence / 各前端 visibility / 新 `notify` 中台 | 新中台，宿主只上报"是否在场" |
| D2 | IM 会话 ↔ runtime 会话映射 | 1:1 / thread-topic 映射 | 首期 1:1，thread 映射做配置项 |
| D3 | 连接器运行拓扑 | 仅 gateway / 三宿主均可 / gateway 为 webhook 统一入口 | 三宿主可跑；webhook 必须经 gateway（或公网宿主） |
| D4 | 是否引入 `internal/notify` 中台 | 是 / 否（继续各自为政） | **是**（G2 是最大风险） |
| D5 | 事件总线异步订阅改造是否本轮做 | 是（P0）/ 延后 | **是**（影响面小、收益大） |
| D6 | 通道偏好粒度与默认 | 全局+会话+通道 / 仅全局 | 三级；低优先默认只推一个通道 |
| D7 | 微信/QQ 账号来源 | 官方开放平台 / 第三方 | 官方优先，第三方作实验通道 |
| D8 | 密钥管理与主备一致性 | 本地文件（0600）/ 外部 KMS | 本地文件，主备同步同文件 |

---

## 8. 路线增量（在 04 的分阶段前插入 P-1）

| 阶段 | 内容 | 人日 |
|------|------|------|
| **P-1 基础加固** | 事件异步订阅；`notify` 内核抽取（gateway 改造，测试护栏）；语义对照表；`connectors/api` 契约包骨架 + CI 矩阵骨架 | 2–3 |
| P0 | Telegram（webhook+polling）+ runtime-server 端到端（原 P0） | 3–4 |
| P1 | 飞书 + gateway 前置 + 三端 Schema 页面（含契约矩阵补全） | 4–5 |
| P2 | aicli 本地 + QQ + 偏好设置页（三端） | 3–4 |
| P3 | 微信 + 媒体 + IM 作为 notify 通道（含静音/去重） | 4–5 |
| 合计 | — | **16–21 人日** |

## 9. 验收补充（在 04 §6 之外）

- [ ] 慢速/挂起的 IM 通道不阻塞 agent 主循环（注入延迟的 e2e 证明；总线异步订阅生效）。
- [ ] 同一事件（approval）在"浏览器桌面通知 + Web Push + IM"三通道**只响一次**（跨通道去重）。
- [ ] 三宿主的 `/api/connectors*` 契约矩阵在 CI 全绿；前端 Schema 快照一致。
- [ ] 连接器可观测：队列深度、丢弃数、失败率、端到端延迟在两端 UI 可查。
- [ ] 未授权用户/未签名 webhook/超限用户的拒绝行为有测试覆盖。
