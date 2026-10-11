# 08 · 通知、注意力与会话 Web UI 反向访问

> 上游：[02-architecture.md](./02-architecture.md)、[03-protocol.md](./03-protocol.md)、[07-console.md](./07-console.md)
> 本文回答两件事：
> 1. **turn 完成后如何经网关通知用户**（含多会话注意力聚合）——先说清现有 hook / 事件能力，再定方案；
> 2. **用户切换到某会话后，如何直接反向代理访问本机 aicli / runtime-server 的 Web 界面**。

---

## 1. 现状核实（本仓库已有什么）

### 1.1 Hook 功能：已支持，且两个宿主都已接线

`internal/hooks` 是一套**策略/门控**型 hook 系统（不是通知系统）：

| 维度 | 现状（证据） |
|------|--------------|
| 事件 | `session_start`/`session_end`、`user_prompt_submit`、`pre_tool_use`、`permission_request`、`post_tool_use`/`post_tool_use_failure`、`subagent_start`/`subagent_stop`、**`stop`**（一轮成功收尾）、**`stop_failure`**（失败收尾）、`pre/post_compact`、`checkpoint_created`、`rewind/backtrack_completed`（`internal/hooks/types.go`） |
| 执行器 | `shell`、`http`（`executor_shell.go` / `executor_http.go`） |
| 决策 | `continue` / `block` / `modify` / `notify` / `enrich`（`types.go:44-50`） |
| 配置 | `HookConfig{ID,Event,Match{Tools,PathGlobs,CommandGlobs},Exec{Type,Cmd,URL,Method,Headers},Timeout,OnError}`（`config.go`） |
| 派发点 | `internal/agent/lifecycle_hooks.go`：`EventStop` **同步**（可 block 停止）、`EventStopFailure` **异步**；`approved_tool.go`/`loop.go` 派发 `EventPreToolUse` |
| 宿主接线 | aicli：`cmd/aicli/commands/chat_actor_host.go:2541` `apiAgent.SetHookManager(runtimehooks.NewManager(mergedHooks))`；runtime-server：`internal/api/runtimeapi/handler.go:5559` `a.SetHookManager(...)`；插件可合并 hook（`internal/plugins`） |

`stop` hook 的 payload 已含：`session_id`、`trace_id`、`output`、`error`、`observation_count`、`tool_error_count`、`limit_reached`、`completion_satisfied`（`lifecycle_hooks.go:23-97`）。

### 1.2 运行时事件总线：已发 turn / 审批 / 提问事件

| 事件 | 位置 | 语义 |
|------|------|------|
| `agent.turn.started` / `agent.turn.finished` | `internal/agent/loop.go:722`；allowlist `internal/toolbroker/types.go:1284` | 一轮开始 / **本次 run 结束** |
| `turn.suspended` / `turn.resumed` | `internal/events/turn_events.go` | 托管挂起 / 恢复 |
| `approval_requested` / `approval_resolved` | `internal/chat/events.go:22-23` | 工具审批 |
| `question_asked` / `question_answered` | `internal/chat/events.go:24-25` | 向用户提问 |
| `tool_started` / `tool_finished` | `internal/chat/events.go:18-19` | 工具执行 |
| `session_start` / `session_end` / `session_interrupted` | `internal/chat/events.go:5-7` | 会话生命周期 |

这些事件**已经**通过 `/web/api/events`（aicli 微前端）与 runtime-server 的事件流对外 SSE 推送，微前端已在消费。

### 1.3 本地"注意力"概念：已存在

`cmd/aicli/commands/chat_notification.go:17-23` 定义终端标题状态：`idle` / `waiting` / `running` / `stopping` / **`actionRequired`**，并有 `BeginActionRequired(label)`。也就是说"需要用户处理"在本地已有语义，**可直接映射为网关的 `attention`**。

### 1.4 通知渠道：目前仅终端标题 + 声音

`chat_notification.go`（标题动画/`actionRequired`）与 `chat_notification_sound.go`。**没有** OS 通知或 Web Push。网关需要补上"远程/移动端"这一层。

---

## 2. 设计原则：事件总线为主，hooks 为扩展点

| 用途 | 选谁 | 理由 |
|------|------|------|
| 网关通知（turn 完成 / 待审批 / 待回答 / 出错） | **本地运行时事件总线** | 事件总线是**权威、已 SSE 暴露、无需用户配置**的生命周期流；语义与本地 UI 同源 |
| 用户自定义外呼（IM / Webhook / 脚本） | **hooks（`stop`/`stop_failure`，shell/http）** | hooks 本就是用户可配的**策略/外呼**扩展点；网关不需要它 |

**反模式（明确禁止）**：不要用 hook 来实现网关通知——原因：
1. `EventStop` 在 turn 收尾路径上是**同步**的，加网络外呼会**增加 turn 延迟**、甚至被 `fail_closed` 阻塞；
2. 需要用户为"自己的网关"额外配置 hook，部署体验差；
3. 事件总线已提供同样信息且**不侵入**执行路径。

> 结论：**agent 订阅本地事件总线**派生 `attention` 并推送；hooks 保留为可选扩展点（文档说明即可，不改 hooks 语义）。

---

## 3. 事件 → 会话状态 / 注意力 映射

agent 侧订阅本地事件总线，按下表更新会话状态并（可选）触发通知：

| 本地事件 | `state` | `attention` | 是否通知 | 通知文案（示例） |
|----------|---------|-------------|----------|------------------|
| `agent.turn.started` | running | none（清除） | 否 | — |
| `agent.turn.finished` | idle | **completed** | ✅ | "「<会话>」已完成" |
| `turn.suspended` | running | **waiting** | ✅（可合并） | "「<会话>」等待子任务" |
| `turn.resumed` | running | none | 否 | — |
| `approval_requested` | running | **approval** | ✅（高优先） | "「<会话>」等待审批" |
| `approval_resolved` | running | none | 否 | — |
| `question_asked` | running | **question** | ✅（高优先） | "「<会话>」需要回答" |
| `question_answered` | running | none | 否 | — |
| `session_interrupted` | idle | none | 否 | — |
| `session_start` / `session_end` | — | — | 会话增删 | — |
| 失败（`stop_failure` / run 失败） | error | **error** | ✅ | "「<会话>」执行失败" |

`attention` 是**状态**（持久于注册表，控制台据此显示徽标/收件箱）；**通知**是**事件**（瞬时，用于 toast / Web Push），两者分离：

- 状态：`SESSION_UPDATE{op:upsert, session:{...,attention}}`（已有，[03](./03-protocol.md) §3.4）；
- 事件：新增 `NOTIFY` 帧（见 §4）。

**优先级**（当多个并存时取最高）：`approval`/`question` > `error` > `completed` > `waiting` > `none`。

---

## 4. 协议扩展

### 4.1 新增 `NOTIFY` 控制帧（type `0x12`，A→G，stream 0）

```json
{ "session_id": "session_20261011_...",
  "kind": "turn_completed",          // turn_completed | approval | question | error | waiting
  "priority": "high",                // high | normal
  "title": "网关设计",                // 元数据（会话标题），非正文
  "dedup_key": "turn_<turn_id>",     // 去重键
  "ts": "2026-10-11T10:00:00Z" }
```

- `NOTIFY` 只承载**元数据 + 事件类型**，**不含正文**（与 [README](./README.md) §4"不落会话正文"一致；正文预览的取舍见 §6）。
- 网关收到后：更新 `attention`（若带状态）→ 推控制台 SSE → 需要时发 Web Push。
- `dedup_key` 供网关去重（同一 turn 的完成只通知一次）。

### 4.2 `SESSION_UPDATE` 保持承载持久状态

`attention` 仍随 `SESSION_UPDATE` 增量/对账（[03](./03-protocol.md) §3.4），保证控制台重连后能重建收件箱，而不依赖瞬时 `NOTIFY`。

---

## 5. 网关侧行为

1. **注册表**：`Session.attention`（持久）+ 最近 `notify` 摘要（短 TTL，不入长期审计正文）。
2. **控制台 SSE**：`/api/events` 推送 `session.updated` / `notify`，驱动徽标、收件箱、toast（[07](./07-console.md) §5.4）。
3. **Web Push**（移动端后台）：仅在**无活跃控制台会话**或**页面不可见**时推送；点击直达 `/s/{session_id}/web/`。
4. **抑制（presence）**：若某控制台客户端**正打开该会话**（心跳/SSE 订阅标记），则不 toast、不推送（用户已在看）。
5. **去重与合并**：
   - 按 `dedup_key` 去重；
   - 时间窗合并同类（如连续多次 `completed` 合并为"N 个会话已完成"）；
   - 与 [05-wan-and-mobile.md](./05-wan-and-mobile.md) §2.5 一致。
6. **优先级与免打扰**：`approval`/`question` 高优先（可穿透免打扰）；`completed` 可静音/批量。

---

## 6. 与"会话预览"取舍的关系

通知**不含正文**（只带标题 + 类型），避免正文离开本机。若用户开启 `gateway.report_preview`（[06](./06-implementation-plan.md) 已定为默认关闭的 opt-in），通知文案**仍不**携带正文，预览只在控制台列表按需拉取。

---

## 7. 用户切换会话 → 反向代理访问本机 Web UI

**确认可行**，这就是 `/s/{session_id}/...` 与 `/n/{node_id}/...` 的设计（[02](./02-architecture.md) §4.2）。完整链路：

```text
控制台点会话卡片
  └─ iframe src = /s/{session_id}/web/            （aicli：微前端）
                 /s/{session_id}/                 （runtime-server：SPA）
       │
网关：校验 owner Cookie → 解析 session→node → 校验能力/路由白名单 → 打开隧道流
       │
agent：剥离 /s/{session_id} 前缀 → 转发 127.0.0.1:<port> → 注入本机令牌 → 回流
       │
本机：返回微前端 / SPA（含其 /web/api/* 或 /api/runtime/* 交互）
```

要点：

| 项 | 说明 |
|----|------|
| **前缀剥离** | 网关剥离 `/s/{id}`（或 `/n/{node}`）后，本地看到的路径与直连一致（`/web/...`、`/api/runtime/...`） |
| **base-path 感知** | 微前端 / runtime-server SPA 必须用相对/`<base>` 构造请求，否则根绝对路径会指回网关根（[06](./06-implementation-plan.md) §4.1、[07](./07-console.md) §6） |
| **打开方式** | 默认 **iframe 内嵌**（控制台保留网关工具栏：返回/切换/状态）；另提供"在新标签打开" |
| **同源** | 控制台与代理 UI 同源 → owner Cookie 自动携带，iframe 无跨域问题 |
| **本机令牌** | 由 agent 注入，浏览器/网关不接触 |
| **会话深链** | 若本地 UI 支持 `?session=`（微前端已有 `/web?session=<id>` 深链语义），网关可在转发 URL 上附加，直达目标会话 |
| **安全** | 代理 UI 响应可设 `Content-Security-Policy: frame-ancestors 'self'`；控制台设 `frame-src 'self'`；禁止第三方嵌入 |

**两种被代理目标**：

| 目标 | 入口 | 备注 |
|------|------|------|
| aicli 微前端 | `/s/{id}/web/` | 单进程当前会话 + 本机会话列表；已有会话切换 UI |
| runtime-server SPA | `/s/{id}/` 或 `/n/{node}/` | 完整工作台；其会话由 runtime-server 自身管理 |

> 因此控制台**不重复实现**聊天/文件/Git/MCP UI——它们由被代理的本地 Web 界面提供（[07](./07-console.md) §6）。

---

## 8. 需要扩展程序的地方（落地清单）

| 程序 | 改动 | 阶段 |
|------|------|------|
| `aicli` / `runtime-server`（agent 侧） | 订阅本地运行时事件总线（复用微前端同一总线），按 §3 映射为 `SESSION_UPDATE` + `NOTIFY` | P1 |
| `aicli` / `runtime-server` | **不改** hooks 语义；文档说明"自定义外呼可用 `stop` hook" | — |
| 网关 | `NOTIFY` 帧处理、注册表 `attention`、控制台 `/api/events`、Web Push、去重/抑制 | P1–P2 |
| 控制台 | 收件箱/徽标/toast/推送订阅、会话视图 iframe | P1–P2 |
| 微前端 / runtime-server SPA | base-path 感知（UI 反向访问前置） | P1–P2 |

**关键点**：通知能力**不需要新增 hook**，也不需要在 turn 收尾路径插入网络调用——它是对**已有事件总线**的旁路订阅，零侵入、可降级（网关不可用时本地不受影响）。

---

## 9. 验收清单

- [ ] `aicli` turn 完成后，控制台在无正文的前提下收到 `attention=completed` 与一次通知（可配置静音）。
- [ ] `approval_requested` / `question_asked` 触发高优先通知，控制台徽标与收件箱即时更新。
- [ ] 会话重连/网关重连后，收件箱可由 `SESSION_UPDATE` 对账重建（不依赖瞬时 `NOTIFY`）。
- [ ] 用户正打开该会话时不重复 toast/推送（presence 抑制）。
- [ ] 移动端后台可收到 Web Push，点击直达 `/s/{session_id}/web/`。
- [ ] 未配置网关时，本地通知（终端标题/声音）与 hooks 行为**逐字不变**。
- [ ] 点会话卡片可在控制台 iframe 内打开本机 aicli 微前端 / runtime-server SPA 并正常交互。
- [ ] 通知与预览均**不含正文**（除非显式开启 `report_preview`，且通知仍不含正文）。

---

## 10. 实现状态（2026-10-11）

### 10.1 已实现

| 项 | 位置 | 说明 |
|----|------|------|
| VAPID 密钥 | `internal/gateway/push/vapid.go` | P-256 密钥生成/持久化（`data/vapid.key`，0600）；`SignVAPIDJWT`（ES256） |
| 载荷加密 | `internal/gateway/push/encrypt.go` | RFC 8291（`aes128gcm`）+ RFC 8188 记录；HKDF-SHA256 派生 CEK/NONCE |
| 订阅存储 | `internal/gateway/push/store.go` | `data/push.json`（0600），按 endpoint upsert/去重 |
| 投递 | `internal/gateway/push/sender.go` | `Sender.Send` / `Manager.Broadcast`；404/410 视为 `ErrGone` 并自动清理 |
| 注意力侦测 | `cmd/gateway/push.go`（`pushNotifier`） | 对比注册表 `attention` 快照，**新增/变化**时推送；状态清除后再触发可再次通知（天然去重） |
| 控制台 API | `cmd/gateway/main.go` | `GET /api/push/config`、`GET|POST /api/push/subscriptions`、`POST /api/push/unsubscribe`、`POST /api/push/test`（均 owner 保护） |
| 指标 | `/metrics` | `aicli_gateway_push_subscriptions`；`/api/metrics` 亦含 `push_subscriptions` |
| 控制台 PWA | `cmd/gateway/console/{sw.js,app.js,index.html}` | 服务进程 `push`/`notificationclick` 事件；`开启通知/关闭通知` 切换按钮，订阅后 POST 到网关 |
| 配置 | `cmd/gateway` | `--push-enabled`（默认 true）、`--vapid-subject` |
| 控制台收件箱/徽标/toast | `cmd/gateway/console/{app.js,style.css}` | 会话行渲染 `attention` 徽标（已完成/待审批/待回答/出错/等待中）；SSE 收到**新增**注意力时弹出可点击 toast（首次加载只做种子，不刷屏） |
| agent（aicli）上报 | `cmd/aicli/commands/chat_gateway_attention.go`、`chat_mesh.go`、`chat_gateway_host.go` | turn 边界（`beginChatMeshTurn`）驱动 `attention`：开始时清空 + busy，结束时 `completed`/`error`（按 turn 最终 err）；随 `SESSION_UPDATE` 上报 |
| agent（runtime-server）上报 | `internal/api/runtimeapi/gateway_attention.go`、`gateway_handlers.go` | 订阅运行时事件总线：`agent.turn.started/finished`、`approval_requested/resolved`、`question_asked/answered`、`session_end/interrupted` → `attention`/`busy`；`EnableGatewayAttention()` 在启动装配时调用 |

**通知触发语义（当前实现）**：网关在 `OnRegistryChanged` 时扫描注册表，对每个 `attention != ""` 的会话记录 `nodeID|sessionID → attention`；仅当该值**首次出现或发生变化**时广播一次 Web Push。因此：

- 同一注意力状态**重复上报不会重复推送**（等价 `dedup_key` 的粗粒度实现）；
- 状态被清除（`attention` 变空）后再进入注意力，会**再次**通知。

### 10.2 待办（与 §3/§5/§9 的差距）

| 项 | 说明 |
|----|------|
| `NOTIFY` 帧（§4.1） | 尚未新增帧；当前依赖 `SESSION_UPDATE` 的 `attention` 字段（已足以驱动网关推送） |
| presence 抑制（§5.4） | 控制台未上报"正在查看某会话"，因此无法抑制已读会话的推送 |
| 时间窗合并（§5.5） | 连续 `completed` 未合并为"N 个会话已完成" |
| 免打扰/静音（§5.6） | 未实现 |
| 控制台独立收件箱视图 | 已有会话行徽标 + toast；未做聚合收件箱页面 |

> 说明：推送链路已**端到端打通**（agent 上报 → 网关注册表 → 去重触发 → VAPID 加密投递 →
> Service Worker 展示 → 控制台徽标/toast），各段均有单测；剩余待办（presence 抑制、时间窗合并、
> 免打扰、聚合收件箱、`NOTIFY` 帧）均为**增强项**，不影响主链路可用性。
