# 18 · 网关自持 Bot 通知通道（C 方案）

> 状态：设计草案（2026-10-30，已完成代码锚点审查，待产品/架构拍板）
> 基线：gateway v78（`876cc106`，连接器页只读聚合已上线）
> 关联：`docs/gateway/08-notifications.md`、`docs/bots/06-target-architecture.md`、`docs/bots/09-multi-instance-binding.md`

---

## 0. 结论摘要

**可行，建议采用 C 方案。**

C 方案的定义：

> **网关自己运行 bot 实例，bot 是网关通知分发器的一等通道；所有节点只向网关上报会话/注意力，网关直接向平台发送通知，通知路径不回调节点。**

这条路径与本轮已经上线的只读连接器聚合不同：

- 只读聚合解决“网关看得见节点有哪些 bot”；
- C 方案解决“网关自己有一个 bot，并用它向所有节点的通知目标发消息”。

首期建议拆成两个交付层级：

| 层级 | 内容 | 适用场景 |
|------|------|----------|
| **C1** | 网关新增一个专用 ops bot + 全局通知目标；节点本地 bot 继续负责会话命令回环 | 最小风险、最快上线；网关通知发给单独的运维目标（QQ 群/C2C 或微信 DM） |
| **C2** | 把原本运行在 aicli/runtime-server 上的会话 bot 迁移到网关；网关 bot 成为该会话的唯一 IM 入口 | 目标是“网关统一承接所有通知 + 命令回环” |

**C1/C2 不是两套代码**：它们共用 `GatewayBot` 通道、连接器 CRUD、绑定和目标模型。
C1 只是“网关 bot + 全局 target，节点 bot 保留”；C2 是“网关 bot + 会话 binding，
节点 bot 禁用/迁移”。因此可以先做 C1 验证通道，再按需把 C2 作为迁移模式启用；
不需要为了 C2 重写一套通知系统。

**不建议**把节点的 `connectors.json`/`config.yaml` 复制到网关，也不建议让网关通过隧道回调节点来发通知：

1. 复制密钥会让 bot token 跨宿主；网关成为新的密钥持有方；
2. 同一个轮询 bot 在两处运行会抢消息（Telegram 409、微信消息被另一端消费、QQ WS 重复连接）；
3. 回调节点依赖节点在线和可反代；用户明确指出某些节点在内网/NAT 后，通知数据流不能依赖回程；
4. 同一份共享 `config.yaml` 会被多个节点返回（当前实测 6 个在线节点各返回相同的 2 个连接器），广播式回调会产生 6 份重复消息。

因此 C 方案的边界是：

```text
所有节点 --HEARTBEAT / SESSION_UPDATE--> 网关注册表
网关注册表 --> 网关通知分发器 --> 网关自持 bot --> IM 平台
```

通知链路**不经过** `/n/{node}`、不经过 mesh peer、不要求节点回环服务可达。

---

## 1. 现状与证据

### 1.1 网关已有能力

| 能力 | 现状 | 代码锚点 |
|------|------|----------|
| 通知聚合 | `pushNotifier.observe(registry.List())` 扫描所有节点会话；按 attention 差分投递 | `cmd/gateway/push.go` |
| 通知通道 | Web Push、ntfy、原生 AppPush；`notify.Dispatcher` 支持 high 扇出 / normal 主通道 / low 收件箱 | `cmd/gateway/push.go`、`internal/notify/dispatcher.go` |
| 通知策略 | `<data-dir>/notify-policy.json`，global/session/channel 三级，热加载 | `cmd/gateway/notify_policy.go` |
| 连接器存储 | 网关自己的 `<data-dir>/connectors.json` + `connectors-state.json`（0600） | `cmd/gateway/connector_bootstrap.go`、`internal/connectors/storejson` |
| 连接器运行 | 网关已装配 Telegram / QQ / 微信 / 飞书 Provider，`HostID="gateway"` | `cmd/gateway/connector_bootstrap.go` |
| 连接器管理 API | list/get/create/update/delete/test/reload/logs/actions/bindings/schema 已具备 | `internal/connectors/restapi/restapi.go` |
| 入站到节点 | `gateway_node` Backend 经隧道投递到节点 `/web/api/invoke` | `cmd/gateway/connector_backend.go` |
| 连接器页面 | v78 已聚合网关与在线节点，只读；节点条目无动作按钮 | `cmd/gateway/console/app.js`、`node_connectors_api.go` |

### 1.2 当前缺口

1. **通知分发器没有 bot/IM 通道**：`pushNotifier.ensureDispatcher()` 只注册 WebPush、ntfy、AppPush；`channels.IM` 存在，但只在连接器投影 hook 中单独使用，不在 `notify.Dispatcher` 的通道列表里。
2. **网关连接器页是只读的**：虽然 REST API 已有完整 CRUD，控制台没有新建/编辑/启停/测试/日志/绑定表单。
3. **没有“发给所有节点”的目标模型**：现有 `Binding` 是“chat ↔ 某个会话”的一对一指针；没有“这个 bot 的某个 chat 接收所有节点通知”的表达。
4. **没有失败回退**：现有 gateway store 的 IM hook 通过 `imMainChannel` 让 normal push 静默；如果 IM 发送失败，normal 通知可能既没有 IM 也没有 push。
5. **没有单归属校验**：节点本地 bot 已经在发审批/提问/完成通知；如果网关 bot 再发同一会话/同一 chat，会重复。

### 1.3 节点本地 bot 的既有行为（重要）

aicli 与 runtime-server 都已经接：

```text
connectors.SubscribeAttention(...)
  -> approval/question/completed/tool/subagent 事件
  -> 本地 policy.notify 过滤
  -> Service.NotifyAttentionScoped
  -> 节点本地绑定 chats
```

代码锚点：

- `cmd/aicli/commands/connector_host.go`：`SubscribeAttention` + `AttentionInlet`；
- `cmd/runtime-server/connector_bootstrap.go`：同样的 `SubscribeAttention` + `AttentionInlet`；
- `internal/connectors/attention.go`：`AttentionNoticeForEvent` 实际只映射 approval/question/completion/tool/subagent，**不映射 error/waiting**；
- 绑定会话的 approval/question 始终发送；completion 受 `policy.notify.enabled` 控制（默认关）。

这意味着：

- 节点本地 bot 已经会发 approval/question；如果网关 bot 也发给同一个 chat，一定重复；
- 节点本地 bot 不发 error/waiting；网关 bot 可以补这个缺口；
- 当前网关只读页显示的 6 个在线节点共享同一份配置（实测每个节点返回 2 个连接器、
  2 个绑定，只是 `active_owner` 不同）；任何“逐节点回调”都会放大重复。

---

## 2. 目标与非目标

### 2.1 目标

1. 网关自持 bot 可以作为通知通道，接收所有节点的会话注意力并直接发到平台，不依赖节点回程。
2. 网关 bot 的配置、绑定、目标、测试、日志全部在网关控制台完成。
3. bot 通道进入现有通知策略：优先级、kind 白名单、免打扰、合并、通道偏好、去重都复用网关通知内核。
4. normal 类通知在 bot 无目标/发送失败时能回退 Web Push/ntfy/AppPush，不静默丢失。
5. 明确单归属：同一 `(session, chat)` 不能同时由节点本地 bot 和网关 bot 重复发送；给出迁移路径。
6. 安全：密钥只落在网关或节点各自的 0600 存储；不允许通过只读连接器视图复制节点密钥。

### 2.2 非目标（首期）

- 不在网关控制台直接编辑节点本地 `config.yaml`（这是后续远程写管理面，单独设计）。
- 不把所有 Provider 都改造成“outbound-only”模式；首期使用正常网关连接器实例。
- 不实现多租户/RBAC；仍是单 owner。
- 不改变现有 Web Push/ntfy/AppPush 的优先级语义（除 C 方案引入的 IM 通道与回退）。
- 不自动解决节点本地 bot 的命令回环迁移；C2 需要明确迁移步骤。

---

## 3. 可行性判断

| 问题 | 判断 | 依据 |
|------|------|------|
| 网关能否运行自己的 bot？ | **能** | 已有 `connectors.Service` + 四个 Provider + `gateway_node` Backend |
| 网关能否保存 bot 密钥？ | **能** | `storejson` 已把 secrets 与 config 写 `<data-dir>/connectors.json`（0600） |
| 网关能否向任意 chat 发送？ | **能** | `Service.SendOutbound(ctx, connectorID, chat, msgs)` 已存在；测试消息端点也走同一能力 |
| 网关能否发所有节点的通知？ | **能** | `pushNotifier.observe` 已经遍历所有注册节点；不需要节点回调 |
| 网关是否需要新的通知通道抽象？ | **需要** | 当前 bot 只是投影 hook；要进入 Dispatcher 的 high/normal 语义、合并、免打扰、回退，需要新增 `GatewayBot` Channel |
| 网关能否配置 bot？ | **后端已能，前端缺失** | `restapi` CRUD/test/logs/bindings/schema 已落地 |
| 是否有重复消费风险？ | **是，必须约束** | 网关 store 与节点 store 不共享，`runtime_owner` 不能跨 store 防护 |
| 是否适合内网节点？ | **适合** | 通知只依赖节点出站心跳到网关，网关直接出站到 IM；不回调节点 |

**结论：C 方案不需要新的节点协议，也不需要节点反代；主要工作在网关通知内核、目标模型、控制台 UI 和单归属治理。**

---

## 4. 总体架构

```text
                         ┌──────────────────────────────┐
                         │        IM Platform           │
                         │ Telegram / Feishu / QQ / ... │
                         └──────────────▲───────────────┘
                                        │ bot API（出站）
                                        │
┌──────────────┐   wss/心跳   ┌─────────┴────────────────────────────┐
│ aicli 节点 A │ ───────────▶ │            aicli-gateway             │
│ (NAT/内网)   │              │                                      │
└──────────────┘              │  Registry（所有节点会话/attention）   │
                              │        │                             │
┌──────────────┐              │        ▼                             │
│ aicli 节点 B │ ───────────▶ │  pushNotifier.observe()               │
│ (NAT/内网)   │              │    ├─ Web Push / ntfy / AppPush       │
└──────────────┘              │    └─ GatewayBot Channel（新增）      │
                              │          │                           │
┌──────────────┐              │          ▼                           │
│ runtime-server│ ───────────▶│  Gateway Connector Store/Service      │
│ 节点 C        │              │  （`connectors.json` + `state.json`）│
└──────────────┘              │          │                           │
                              │          ▼                           │
                              │  Provider.SendOutbound → IM Platform │
                              └──────────────────────────────────────┘
```

可选入站链路（独立于通知链路）：

```text
IM Platform --webhook/WS--> gateway connector service
  -> gateway_node Backend -> 目标节点 /web/api/invoke
```

通知链路和入站链路解耦：即使某个节点在内网、没有反代路由，网关仍可以发送该节点的会话通知。

---

## 5. 通知语义与路由

### 5.1 通道命名

新增 `im` 通道（实现名 `GatewayBot`），进入 `notify.Dispatcher`：

```text
channels:
  webpush
  ntfy
  apppush
  im        # 新增：网关自持 bot
```

控制台「通知设置」页的通道列表增加 `im`；现有 `notify-policy.json` 的 `channels.im` 可直接复用偏好覆盖（mute / min_priority / allow_kinds）。

注意：`channels.im` / `allow_kinds` 只作用于**网关自持 bot 通道**；节点本地 bot
仍由节点自己的 `policy.notify` / `policy.events` 控制。两套策略不会因为共享
gateway notify policy 而自动合并。

### 5.2 优先级路由

沿用 D6，不另造一套：

| 优先级 | 路由 |
|--------|------|
| high（approval / question / error） | Web Push、ntfy、AppPush、im **全扇出** |
| normal（completed / waiting） | `im` 有目标时作为主通道；无目标/发送失败时回退 Web Push/ntfy/AppPush |
| low | 只进收件箱，不发 bot |

这样做的理由：

- high 是多通道确认语义，existing 行为不变；
- normal 用 bot 作为主通道，符合“网关承接所有节点通知”的目标；
- 没有 bot 目标时绝不能把 normal 丢掉，必须有回退。

### 5.3 目标解析

`GatewayBot` 的投递目标由两部分组成，按 `(connector_id, chat)` 去重：

1. **全局/范围目标**（新增 `gateway-im.json`）
   - `scope=all`：所有节点；
   - `scope=nodes`：指定 node_id 列表；
   - `scope=sessions`：指定 session_id 列表；
   - 按 `kinds` 与 `min_priority` 过滤。
2. **会话绑定目标**（已有 gateway store bindings）
   - `Binding.Session == event.Session`；
   - 连接器 enabled；
   - `policy.notify` 接受该 kind（approval/question 始终接收；completion 需 `notify.enabled`）。

如果同一事件同时命中全局目标和会话绑定，只发送一次（按 connector+chat 去重）。

全局目标与连接器 `policy.notify` 的关系：全局目标是显式配置的网关通知路由，
不要求连接器 `policy.notify.enabled=true`，但仍要求连接器 enabled；会话绑定目标
仍按 `policy.notify` 过滤。这样可以支持“一个 bot 既接收命令、又只把全局告警发到
运维群”的组合，也避免为了全局目标而改变节点共享的 policy。

过滤器优先级：`notify-policy.json` 的 `allow_kinds` / `min_priority` 先过滤
（决定是否进入 Dispatcher），target 的 `kinds` / `min_priority` 再过滤
（决定发给哪个 chat）；两者是交集，不是替代。

### 5.4 事件映射

| 网关 attention | 通知 kind | 默认是否发 bot |
|----------------|-----------|----------------|
| `approval` | approval | 是（high） |
| `question` | question | 是（high） |
| `error` | error | 是（high） |
| `completed` | completion | 是（normal） |
| `waiting` | waiting | 默认否；可由 target `kinds` 显式打开 |

节点本地 `SubscribeAttention` 不映射 error/waiting；网关 bot 可以补 error，这是 C 方案的增值点。waiting 默认不发，避免“等待子任务”的噪音。

### 5.5 去重、合并、免打扰

全部复用现有通知内核，不在 bot 通道另写一套：

- **差分去重**：`pushNotifier.seen` 以 `node|session` 为键，attention 变化才产生事件；
- **跨通道去重**：`notify.Dispatcher` 的 `DedupKey` / `Ledger`；
- **normal 合并**：沿用 `Dispatcher` 的 coalesce 窗口（默认 30s）；
- **免打扰**：`notify-policy.json` 的 quiet_hours 对 low/normal 生效，high 穿透；
- **在场**：`presence` 门默认生效（与 push 一致）；如果运营希望在“正在看控制台”时仍收 IM，可给 target 增加 `ignore_presence=true`（后续）。
- **旧 hook 迁移差异**：旧 `connectorAttentionHook` 不经过 Dispatcher 的合并/免打扰/账本；
  新通道会经过，启用新通道后行为会变得更安静、更可配置，这是预期变化。

### 5.6 失败与回退

新增 `notify.ErrNoRecipient` 与 normal 回退链：

- `GatewayBot.Deliver` 无目标 → `ErrNoRecipient`；
- 目标存在但全部发送失败 → `ErrAllTargetsFailed`（或包装错误）；
- `Dispatcher` 对 normal：
  1. 尝试主通道 `im`；
  2. `ErrNoRecipient` / 全部失败 → 依次尝试 fallback `webpush`、`ntfy`、`apppush`；
  3. 任一成功即停止；全失败记录回执；
- high：所有通道照常扇出；`ErrNoRecipient` 记为 skipped，不算失败；真实发送失败只记录错误，push/ntfy 已经覆盖；
- 部分成功：记录成功/失败，不触发全量回退，避免重复。

现有 `normalChannels` 显式 fanout 语义保持不变；新的回退链只在“未显式配置 normalChannels 且 primary=im”时启用，避免改变 `webpush,ntfy` 的既有行为。

实现约束：

- fallback 必须在**同一个** `Dispatcher.deliver` 调用内完成，复用已经认领的
  ledger key；不能递归调用 `Dispatch`，否则会被去重账本短路；
- `ErrNoRecipient` 必须支持 `errors.Is`（用 `fmt.Errorf("%w", ErrNoRecipient)` 包装），
  `Dispatcher` 才能把它和“真实发送失败”区分开；
- fallback 名单中未注册的通道跳过并记录日志；
- normal 主通道被 `prefs` 以 muted/allow_kinds 跳过时，也应视为“不可用”并进入
  fallback；否则用户静音 `im` 会连带吞掉 normal 通知；
- `GatewayBot.Deliver` 的 32 个 target 不应串行阻塞 30s：按 target 并发（建议上限 4）、
  单 target 5s 超时，整体仍受 Dispatcher 30s context 约束。

### 5.7 消息格式

首期统一文本：

```text
[会话标题]
审批：有待审批的工具调用
回复 /approve 或 /deny
https://aicli.ttai.online/n/{node}/web/?session={session}
```

文本来源分两部分：

- 标题/URL 取 `notify.Event.Notification.Title` / `URL`，与 Web Push 共用；
- 正文优先取 `connectors.AttentionNoticeForProjection(ev.Session, ev.Kind)` 的
  `Text`（包含 `/approve`、`/deny`、`/answer` 等可操作提示）；该映射对
  `waiting` 返回 false，此时回退 `Notification.Body`。

URL 沿用 `pushNotifier.sessionURL`：aicli 为 `/n/{node}/web/?session=...`，
runtime-server 为 `/n/{node}/`（不拼 session query）：

- Telegram：纯文本/HTML 均可；
- 飞书：首期 text；后续可升级 interactive card；
- QQ：纯文本；
- 微信：文本，注意 proactive 限制与 context_token。

审批/提问消息中的 `/approve`、`/deny`、`/answer` 只有在网关 bot 绑定到目标节点/会话时才可闭环；C1 的 ops bot 可以只做通知，不承诺命令回环。

### 5.8 Provider 能力矩阵（首期微信 + QQ）

首期已定微信与 QQ；两者的现实边界不同：

| Provider | 目标形态 | 出站约束 | 首期建议 |
|----------|----------|----------|----------|
| QQ 官方 bot | 群 / C2C（openid） | 无 `msg_id` 走主动消息；有单聊/群/关系/日配额；平台仍兜底 | **C1 首选**：用 QQ 群或 C2C 做网关 ops target |
| 微信 | 当前 Provider 是 **1:1 chat/openid**，无群/chatroom 能力 | `Send` 必须携带该 chat 最近入站的 `context_token`；无 token 直接失败；会话过期需重新入站刷新 | C1 只能作为 owner 的 **微信 DM**，且必须先给网关 bot 发一条消息播种；C2 更适合按会话迁移 |

补充：

- QQ 群/C2C 目标不需要先有入站消息；主动消息受平台配额限制，适合低频运维通知。
- 微信不支持“把消息发到一个微信群”：当前 Provider 没有 group/chatroom 解析；如果目标是群，
  首期应使用 QQ 群，而不是微信。
- 微信 `context_token` 是 **按 connector+chat** 的回复上下文；网关 bot 自己的 store
  必须有该 token（入站播种或迁移时显式导入），不能靠节点只读 API 自动抓取。
- 网关 bot 的 Provider 选择应由配置时校验 + 测试消息先行验证；测试失败不能阻断
  push/ntfy 的既有回退。

---

## 6. 配置与存储

### 6.1 网关 bot 配置（复用现有）

```text
<data-dir>/connectors.json         # bot 配置 + secrets（0600）
<data-dir>/connectors-state.json   # 绑定/所有权/游标/账本（0600）
```

网关 bot 的 Provider、Schema、密钥语义与 aicli 完全一致；直接复用 `internal/connectors/restapi`。

默认关闭：`gateway-im.json` 缺失、`enabled=false` 或 targets 为空时，bot
不投递；现有 Web Push/ntfy/AppPush 行为完全不变。只有显式配置并启用后才改变
normal 主通道。

为避免热加载导致 Dispatcher 重建：建议**连接器服务存在时就注册** `GatewayBot`
通道，通道内部用原子快照读取 `enabled/targets`；文件变化只替换快照，不重建
Dispatcher。`enabled=false` 或无 target 时返回 `ErrNoRecipient`，自然回退。

可复用的现有管理面：

| 方法/路径 | 用途 |
|-----------|------|
| `GET/POST /api/connectors` | 列表 / 新建 |
| `PATCH/DELETE /api/connectors/{id}` | 编辑 / 删除 |
| `POST /api/connectors/{id}/test` | 测试消息（显式 chat） |
| `POST /api/connectors/{id}/reload` | 应用配置 |
| `POST /api/connectors/{id}/actions/{action}` | QQ 授权校验等 |
| `GET /api/connectors/{id}/logs` | 运行日志 |
| `GET /api/connectors/bindings` | 绑定列表 |
| `PUT/DELETE /api/connectors/{id}/bindings/{chat}` | 绑定/解绑 |

网关控制台只需渲染 Schema 表单与调用这些接口，不需要新增连接器 CRUD 后端。

### 6.2 新增通知目标（`gateway-im.json`）

新增文件，而不是塞进 `notify-policy.json`：

```json
{
  "version": 1,
  "enabled": true,
  "targets": [
    {
      "id": "ops-main",
      "connector_id": "tg-ops",
      "chat": "123456789",
      "label": "运维群",
      "enabled": true,
      "kinds": ["approval", "question", "error", "completed"],
      "min_priority": "normal",
      "scope": { "mode": "all" }
    },
    {
      "id": "box-a-only",
      "connector_id": "feishu-ops",
      "chat": "oc_xxx",
      "enabled": true,
      "kinds": ["error"],
      "min_priority": "high",
      "scope": { "mode": "nodes", "node_ids": ["node-a"] }
    }
  ]
}
```

字段语义：

| 字段 | 说明 |
|------|------|
| `id` | 目标稳定标识（唯一） |
| `connector_id` | 必须存在于网关 `<data-dir>/connectors.json` 且 enabled |
| `chat` | 平台 chat id/openid；不同 Provider 语义不同 |
| `label` | 控制台展示名 |
| `enabled` | 目标级开关 |
| `kinds` | 允许的 attention 词表；省略 = 默认 approval/question/error/completed |
| `min_priority` | `normal` / `high` |
| `scope.mode` | `all` / `nodes` / `sessions` |
| `scope.node_ids` | mode=nodes 时必填 |
| `scope.session_ids` | mode=sessions 时必填 |

`kinds` 面向控制台使用 gateway attention 词表（`completed` 而不是内部
`completion`）；通道实现负责映射到 `connectors.AttentionNotice` 的 kind。

**为什么单独文件：**

- 这是网关专用的“通知路由”，不是节点共享的连接器配置；
- 不污染 aicli/runtime-server 的 `config.yaml`；
- 可以独立版本化、校验、热加载；
- 文件不包含 token，只包含 chat id/路由；token 仍在 `connectors.json` 0600。

### 6.3 API

```text
GET  /api/notify/im         owner 鉴权；返回有效 targets + 关联 bot 摘要
PUT  /api/notify/im         owner 鉴权；整表替换，校验后原子写 + 热加载
POST /api/notify/im/test    可选：向指定 target 发送测试文本
```

校验：

- connector 存在、enabled、provider 在网关已注册；
- chat 非空；
- kinds 合法；
- `scope.mode` 与 ids 匹配；
- `id` 唯一、最多 32 个 targets；
- 路径/内容长度上限沿用 REST 管理面。

生命周期约束：

- 删除仍被 `gateway-im.json` 引用的网关连接器时，`DELETE /api/connectors/{id}`
  应返回 409 并列出引用 target；先禁用/删除 target，再删连接器；
- 连接器 disabled 时，target 视为 skipped（不是发送失败），`GET /api/notify/im`
  返回 `invalid_reason` 供控制台提示；
- connector 被替换/轮换后，旧 target 不应静默发到新 token；要求重新确认或
  显式 `connector_id` 不变。

`PUT` 要原子写（temp + rename + fsync）并使用独立的 `gateway-im.lock`；不把
通知路由写锁与连接器配置写锁耦合，避免连接器保存/轮询重载阻塞通知配置。

建议响应形状：

```json
{
  "version": 1,
  "enabled": true,
  "targets": [
    {
      "id": "ops-main",
      "connector_id": "tg-ops",
      "provider": "telegram",
      "chat": "123456789",
      "kinds": ["approval", "question", "error", "completed"],
      "min_priority": "normal",
      "scope": { "mode": "all" },
      "last_error": ""
    }
  ]
}
```

`GET` 可附带 `meta.connectors`（网关 bot 的 id/provider/enabled/active_owner）供控制台
下拉选择；不返回 secrets。

### 6.4 控制台

1. **连接器页**
   - 「网关托管」区：新建/编辑/启停/测试/日志/删除/绑定；
   - 「节点连接器」区：保持 v78 只读，展示来源节点与配置 JSON 链接；
   - 明确标识“网关 bot 使用网关自己的密钥，不复制节点密钥”。
2. **通知设置页**
   - 新「Bot 通道」区：目标列表、connector 选择、chat、label、kinds、min_priority、scope；
   - 测试按钮（发送一条测试通知）；
   - 显示 bot 通道状态（enabled/targets/last error）；
   - 显示与节点本地 bot 的潜在重复警告。
3. **绑定**
   - 网关 bot 支持 `PUT/DELETE /api/connectors/{id}/bindings/{chat}`；
   - 节点 bot 绑定只读展示；
   - 如果同一 session 同时存在网关与节点通知绑定，显示“双通知源”警告。

---

## 7. 单归属与迁移

### 7.1 核心不变量

> **同一 `(session, chat)` 只能有一个通知所有者：`gateway` 或 `node`，不能同时。**

该不变量适用于**会话绑定通知**；全局 ops target 是一个独立的运维 chat，
可以跨所有会话接收摘要/告警，但不应与节点本地会话 bot 绑定到同一个 chat。

原因：

- 节点本地 bot 已经对绑定会话发送 approval/question（不受 notify 开关影响）；
- 网关 bot 如果也发，用户会收到两条相同的审批提醒；
- 两个 store 没有共享锁，代码无法自动仲裁。

### 7.2 C1：专用网关 ops bot（推荐首期）

- 网关新增一个**独立的 bot/token**（不要复用节点正在轮询的 token）；
- 目标是一个独立的运维目标（QQ 群/C2C，或微信 owner DM），通常不绑定会话；
- 节点本地 bots 继续负责会话命令回环与本地通知；
- 网关 bot 负责：网关级告警、跨节点摘要、以及可选的 error/waiting 补充；
- 优点：不需要迁移节点 bot，不会抢消息，不要求节点回程。

首期微信/QQ 的具体落点：

- **QQ**：建议作为 C1 主 ops target，使用 QQ 群或 owner C2C；主动消息配额足够低频通知；
- **微信**：C1 只能是 owner 的 **1:1 微信 DM**（当前 Provider 无群能力）。网关 bot
  必须先收到该 owner 的一条入站消息产生 `context_token`，之后才能向该 DM 发通知；
  若 context 过期，需要再次入站播种。不要把微信 C1 描述成“运维群”。

注意：如果 C1 使用 QQ WS / 微信长轮询等会消费入站的通道，网关 bot 仍会收到
平台消息。此时要么在 `settings.node_id` 指定一个目标节点（命令回环有明确落点），
要么在后续 `notify_only` 模式下不启动轮询；否则多节点时入站会按现有
`gateway_node` 规则报“需要指定 node_id”并记日志，虽然不影响出站通知，但会产生噪声。

### 7.3 C2：迁移会话 bot 到网关

如果目标是“网关统一承接所有通知 + 命令回环”：

有两种迁移方式，必须二选一：

**C2-A：迁移同一个 bot（保留原 token/群关系）**

1. 在网关创建连接器但保持 `enabled=false`，写入 settings/secrets；
2. 立即关闭节点上的原连接器（`enabled=false`），停止节点 poller；
3. 设置目标节点（`settings.node_id`，或确保只有一个在线节点），启用网关连接器，
   并在网关绑定 chat ↔ session（gateway bindings）；
4. 验证网关 `gateway_node` 入站命令回到目标节点；
5. 删除/禁用节点本地绑定，避免双通知。

**C2-B：新建独立 bot（推荐用于长期运营）**

1. 新建一个独立 token 的 bot，邀请到目标群/会话；
2. 在网关创建连接器、设置 `settings.node_id`，并绑定 chat ↔ session；
3. 节点旧 bot 继续工作，直到新 bot 验证通过；
4. 切换 chat/群到新 bot 后，禁用旧 bot；
5. 迁移窗口内不要让两个 bot 同时向同一 chat 发通知。

注意：C2-A 有短暂的“同一 token 双宿主”窗口，必须用维护窗口完成；C2-B
不存在 token 双宿主，但需要重新邀请/绑定。无论哪种，都不能长期并行。

C2-A 的 secrets 只能由 owner 手工导出/导入（或使用离线迁移工具），**不能**
让网关通过节点的只读 `/api/connectors` 自动抓取；后者只返回 `secret_keys`，
这是安全边界，不应为 C2 放宽。

微信 C2 额外约束：迁移后网关 bot 的 store 需要该 chat 的 `context_token`；
迁移时要么复制 state（人工/离线）并验证，要么在切换后让 owner 重新发一条消息
播种。QQ C2 没有 context_token 依赖，但受主动消息配额与被动回复窗口约束。

### 7.4 多实例

当前 gateway 主备是“单写者”约定，代码没有跨实例 bot 租赁。C 方案要求：

- 只有 active gateway 实例运行 bot；
- 主备切换前停止旧实例的 connector service；
- 如果两个实例同时启动同一 data-dir 的 bots，可能双轮询；沿用 `docs/gateway/15-multi-instance-architecture.md` 的 P0 约束。

---

## 8. 安全设计

| 面 | 措施 |
|----|------|
| 密钥 | 只在网关 `<data-dir>/connectors.json`（0600）；不通过只读节点 API 复制；API 只回 `secret_keys` |
| 管理面 | bot CRUD、binding、targets 全部 owner 会话鉴权；沿用现有 `/api/*` 安全边界 |
| 目标写入 | `gateway-im.json` 原子写 + 校验；不把任意 chat/connector 拼进 URL |
| 平台入站 | webhook 签名/WS 凭据校验仍由 Provider 负责；不因通知通道而放宽 |
| 日志 | 不记录 token、secret、完整 chat 内容；chat id 可哈希/截断展示 |
| 频率 | 每 target 限速 + Dispatcher 去重/合并/免打扰；防止 bot 被刷 |
| 权限 | 仍是单 owner；不引入多租户 |
| 远程管理 | 首期不开放节点配置写入；只读聚合端点保持 GET-only |

**不做**：把节点 token 读回网关、通过浏览器 localStorage 存 bot token、用 URL query 传 bot token。

---

## 9. 可观测性

指标（建议前缀 `gateway_bot_*`）：

```text
gateway_bot_targets{enabled}
gateway_bot_deliveries_total{connector,kind,result}
gateway_bot_delivery_latency_seconds{connector}
gateway_bot_fallbacks_total{from="im",to="webpush|ntfy|apppush"}
gateway_bot_no_recipient_total{kind}
```

日志字段：`connector_id`、`provider`、`kind`、`node_id`、`session_id`、`target_id`、`result`、`duration_ms`；不记录 token/secret。

控制台：

- 连接器卡片显示 bot 通道状态、最近一次发送结果、最近错误；
- 通知设置页显示 targets 数量、最近 fallback 次数；
- 测试按钮返回每个 target 的 result（成功/失败原因）。

---

## 10. 失败矩阵

| 场景 | 行为 |
|------|------|
| 没有 bot target | `ErrNoRecipient` → normal 回退 Web Push/ntfy/AppPush；high 仍走 push |
| bot target 存在但 provider 发送失败 | normal 回退 push；high 记录错误，push 已发 |
| 部分 target 成功 | 记录部分成功，不触发全量回退；避免重复 |
| 网关 bot 未配置/禁用 | 等同无 target，不影响现有通知 |
| 节点离线 | 注册表不产生新 attention；不会回调节点；网关机器人仍可用于网关级告警（后续） |
| 网关重启 | 沿用 seen 快照 seed；bot 不重发旧 attention |
| 多实例双活 | 当前不支持；必须单 active，避免双轮询 |
| 同一 token 同时在网关/节点 | 明确禁止；UI 警告/迁移步骤；可能抢消息 |
| 节点 bot 与网关 bot 同时绑定 | 双通知源警告；要求迁移/解绑 |
| 免打扰/静默 | Dispatcher 统一处理；high 穿透 |
| normal 合并窗口 | Dispatcher 统一处理；bot 收到合并后的一条 |
| presence（正在看控制台） | 默认抑制；可给 target 提供 `ignore_presence` 后续开关 |

---

## 11. 落地计划

### 阶段 0：决策与前置（0.5–1 人日）

- 确认 C1 vs C2；
- 首期 Provider 已定微信 + QQ：C1 用 QQ 群/C2C；微信仅 owner DM + context_token 播种；
- 确认“网关 bot 是否必须命令回环”；
- 确认 normal 的 bot 主通道与 fallback 链。

### 阶段 1：C1 最小闭环（3–5 人日）

- 新增 `gateway-im.json` + `GET/PUT /api/notify/im` + 校验/热加载；
- 新增 `notify/channels/gatewaybot.go`（`Name()=im`）；
- Dispatcher 增加 `ErrNoRecipient` / normal fallback；
- `pushNotifier.ensureDispatcher` 注册 `im`，`SetPrimary("im")`，`SetNormalFallback("webpush","ntfy","apppush")`；
- `pushNotifier.observe` 的唤醒门必须把 `im` 算进去（当前只有 push/ntfy/apppush 时才会进入 observe）；
- 旧 `connectorAttentionHook` 改为 feature flag，启用新通道时禁用；同时移除/停用
  `connectorIMMainChannel`/`imMain` 判定，避免旧 hook 与新 Dispatcher 双路径；
- `/api/notify/policy` 的 `meta.channels` 增加 `im`（Available 取决于 gateway bot
  service/targets），否则「通知设置」页不会渲染 bot 通道开关；
- 测试：high/normal/no-target/failure/partial/coalesce/presence；
- 控制台：Bot 通道目标列表 + 测试按钮（最小 UI）。

**验收**：配置一个专用 QQ ops bot + 全局 target，所有节点的 approval/question/error 能到 QQ 群/C2C；微信 DM 作为第二 target（先播种 context_token）；normal completion 能到，且 push 不重复；断网/失败时 push 回退。

### 阶段 2：网关 bot CRUD + 绑定 UI（3–5 人日）

- 连接器页「网关托管」区：新建/编辑/启停/测试/日志/删除/绑定；
- 复用 `restapi` schema/form；密钥留空=保持/`__clear__`；
- 节点连接器继续只读；
- 冲突警告（同 provider/同 bot identity/同 session 双绑定）；
- 首期 QQ（群/C2C）+ 微信（DM + context_token 播种）；Telegram/飞书留作后续扩展。

**验收**：不通过 curl，只在控制台创建 QQ/微信网关 bot，绑定 chat，发送测试，收到消息；
微信测试必须先完成 context_token 播种；节点连接器不变。

### 阶段 3：C2 迁移与高级能力（3–5 人日）

- 迁移向导：从节点 bot → 网关 bot（禁用节点、绑定网关、验证入站/命令）；
- 可选 `notify_only` 模式（不启动轮询，仅出站；按 Provider 能力）；
- QQ 富媒体/微信 context_token 失效提示/后续 Telegram/飞书卡片；
- 网关级告警源（node offline、bot failure）接入 `GatewayBot`；
- `scope=nodes/sessions` 与 per-target `ignore_presence`/rate limit。

### 阶段 4（可选）：节点配置远程编辑

- 扩展只读代理为受控写代理（POST/PATCH/DELETE）；
- 方法/路径白名单、CSRF/同源、审计；
- 节点配置层/写入源提示；
- 不作为 C1 前置。

### 文件级改动点（预估）

| 文件/包 | 改动 |
|---------|------|
| `internal/notify/dispatcher.go` | `ErrNoRecipient`、normal primary fallback、prefs-skipped 回退、回执语义 |
| `internal/notify/channels/gatewaybot.go`（新） | `Name()=im`；target 解析、`Service.SendOutbound`、并发/超时/部分成功 |
| `cmd/gateway/notify_im.go`（新） | `gateway-im.json` 读写、校验、热加载、`GET/PUT /api/notify/im`、test |
| `cmd/gateway/push.go` | 注册 `im`、`SetPrimary`/`SetNormalFallback`、唤醒门、移除旧 `imMain` 路径 |
| `cmd/gateway/connector_bootstrap.go` | 旧 `connectorAttentionHook` feature flag / 退役；连接器服务与 bot channel 接线 |
| `cmd/gateway/main.go` | 注册 `/api/notify/im*`；装配 GatewayBot；审计/指标 |
| `cmd/gateway/console/{app.js,index.html,style.css}` | bot CRUD、bindings、Bot 通道目标、测试、警告、v79 版本 |
| `cmd/gateway/connector_*_test.go` | 目标解析、fallback、去重、迁移/双发回归 |

注意：`notify.Dispatcher` 的 fallback 是**共享内核改动**，必须先补测试再改
`pushNotifier`；否则容易把现有 `webpush,ntfy` fanout 语义改坏。

---

## 12. 测试计划

### 单元

- `GatewayBot.Deliver`：global target、session binding、dedupe、scope、kinds、min_priority；
- `ErrNoRecipient` / all-failed / partial；
- Dispatcher normal fallback 顺序；high 扇出；coalesce/quiet/presence；
- `im` 被 `channels.im` prefs mute 时 normal 仍回退 push（不是静默丢弃）；
- target 校验：未知 connector、disabled、空 chat、bad kinds、重复 id；
- `gateway-im.json` 原子写/热加载/非法文件保留旧值。

### 集成

- fake Provider：global target 收 approval/question/error/completed；
- normal 无 target → Web Push fallback；
- IM 发送失败 → push fallback；
- 两个 target 一个成功一个失败 → 不回退重复；
- gateway store binding + global target 同 chat → 一次；
- 旧 `connectorAttentionHook` 关闭时无双发；
- 节点本地 bot 绑定 + 网关 global target → 文档要求的双通知源警告（或测试不启动双发）。

### 真机

- 创建 Feishu/Telegram 网关 bot；
- 发送测试通知；
- 节点离线/网络断开时网关 bot 仍可发；
- 网关重启不重发旧 attention；
- 节点本地 bot 迁移 C2：禁用节点、网关入站 `/approve` 回到节点；
- 验证无重复消息、无 token 日志。

### 回归

- 现有 `cmd/gateway`、`internal/notify`、`internal/connectors` 测试；
- 现有 Web Push/ntfy/AppPush 行为；
- 控制台 v78 连接器只读聚合不回归；
- `/api/health`、SSE、owner auth。

---

## 13. 备选方案与取舍

| 方案 | 描述 | 结论 |
|------|------|------|
| A：节点本地 bot + 网关感知 | 网关只抑制重复 push，不新增 bot | 适合作为过渡，但不满足“网关自持 bot” |
| B：网关回调节点 bot | 网关 POST 节点 `/api/connectors/attention` | **拒绝**：NAT/内网、节点离线、数据流不可控 |
| C：网关自持 bot | 网关运行 bot，直接发平台 | **采用** |
| D：复制节点配置到网关 | 把 token/config 复制到网关 store | **拒绝**：密钥跨宿主、双轮询、漂移 |
| E：只做网关 ops 告警 | 网关 bot 只发网关级事件 | C1 的最小化形态，可作为首期子集 |

---

## 14. 开放问题（评审需拍板）

1. **C1 还是 C2 作为首期目标？** 推荐 C1（独立 ops bot），C2 作为迁移项目。
2. **首期 Provider？** 已定微信 + QQ：QQ 适合 C1 群/C2C；微信只适合 C1 owner DM（先播种 context_token）或 C2 会话迁移。
3. **normal 主通道是否默认 `im`？** 推荐“有 target 则 im 主通道，无 target/失败回退 push”。
4. **presence 是否抑制 bot？** 推荐默认抑制，与 push 一致；target 可后续加 `ignore_presence`。
5. **waiting 是否发 bot？** 推荐默认不发；target 显式 kinds 才发。
6. **C2 的节点本地通知如何关闭？** 需要节点侧迁移步骤（禁用/解绑），还是新增 `policy.notify.mode=gateway`？后者需要节点协议/配置改动。
7. **是否允许同一 bot token 双宿主？** 推荐禁止；UI 至少警告，严重时阻止启用。
8. **是否支持 `notify_only`？** 需要 Provider 能力矩阵；首期可不做。
9. **目标 chat 的发现方式？** 手工输入 / 测试消息 / 通过入站绑定；需要 UI 设计。
10. **是否将网关级告警（node offline）纳入同一通道？** 推荐后续阶段，不与会话 attention 混在一起。
11. **C1 的 bot 是否允许消费入站？** 若允许，必须指定 `settings.node_id`；若不允许，
    需要 `notify_only` 或明确忽略入站，避免日志噪声。

---

## 15. 审查结论

### 15.1 结论

**有条件通过，按 C1 先行。**
方案在现有代码上可实现，且比回调节点更符合 NAT/内网拓扑；但必须先把“单归属、回退、目标模型”三件事定死，否则会出现重复通知或静默丢失。

### 15.2 审查发现与处置

| # | 发现 | 严重度 | 处置 |
|---|------|--------|------|
| R1 | 节点本地 bot 已发送 approval/question，网关 bot 再发会重复 | 高 | C1 使用独立 ops chat；C2 迁移时禁用节点 bot；UI 双通知源警告 |
| R2 | 网关/节点 store 不共享 `runtime_owner`，同 token 双轮询无法自动阻止 | 高 | 禁止复制 token；C2 迁移必须停节点；启动/配置时做冲突警告；多实例单 active |
| R3 | 当前通知 Dispatcher 无 fallback；IM 无目标会静默吞 normal | 高 | 新增 `ErrNoRecipient` + normal fallback 链；测试覆盖无目标/失败/部分成功 |
| R4 | 现有 `connectorAttentionHook` 绕过 Dispatcher 的合并/免打扰/账本，直接新增 bot hook 会绕过策略 | 高 | 采用 `GatewayBot` 一等 Channel；旧 hook 用 feature flag 关闭，避免双路径 |
| R5 | 只有 Binding 模型，无法表达“发给所有节点”的全局 target | 高 | 新增 `gateway-im.json` + scope all/nodes/sessions；与 bindings 去重 |
| R6 | 网关 bot 的 secrets 会进入网关，需确认安全边界 | 中 | 复用 0600 store；API 只回 secret_keys；不复制节点 secrets；C1 用独立 token |
| R7 | 微信 context_token/无群能力、QQ 主动消息配额可能限制网关 bot | 中 | 首期 QQ 群/C2C + 微信 owner DM；微信先播种 context_token；QQ 先验证配额 |
| R8 | presence 是否抑制 IM 未定 | 中 | 默认沿用 push 抑制；target 加 `ignore_presence` 作为后续 |
| R9 | C2 迁移不是复制配置，而是迁移 bot 所有权 | 高 | 迁移向导：创建网关 bot → 绑定 → 禁用节点 → 验证入站 → 删除节点绑定 |
| R10 | 网关多实例没有 bot 租约 | 中 | 明确 active/passive 约束；不得双 active；沿用 gateway 15 号文档 |
| R11 | 控制台当前只读，CRUD UI 工作量大 | 中 | 分阶段：先 C1 targets/test，再 bot CRUD/bindings；复用 REST/schema |
| R12 | 通知策略（gateway notify-policy）与节点 connector policy 两套 | 中 | 文档明确：gateway 策略管 push/bot；节点 policy 管本地命令/通知；C2 迁移时关闭节点通知来源 |

### 15.3 评审门槛（必须满足后才进入实现）

- [ ] 明确 C1 首期范围与目标 chat；
- [ ] 确认 allowed providers（Telegram/飞书优先）；
- [ ] 定义 `gateway-im.json` 最终字段与 API；
- [ ] 定义 normal fallback 的精确语义与测试；
- [ ] 定义 C2 迁移 checklist（禁止同 token 并行）；
- [ ] 明确多实例约束与 UI 冲突警告；
- [ ] 明确 secrets 不复制、只存网关 0600；
- [ ] 确认 bot 通道与节点本地通知的单归属策略。

---

## 16. 代码锚点与参考

| 主题 | 文件 |
|------|------|
| 网关通知观察/分发 | `backend/cmd/gateway/push.go` |
| 通知策略 | `backend/cmd/gateway/notify_policy.go`、`internal/notify/dispatcher.go` |
| 连接器装配 | `backend/cmd/gateway/connector_bootstrap.go` |
| 网关 bot 入站 | `backend/cmd/gateway/connector_backend.go` |
| 连接器管理 API | `backend/internal/connectors/restapi/restapi.go` |
| IM 适配器 | `backend/internal/notify/channels/im.go` |
| 节点本地通知 | `backend/cmd/aicli/commands/connector_host.go`、`backend/cmd/runtime-server/connector_bootstrap.go` |
| 节点注意力接收 | `backend/internal/connectors/attention.go`（`InletAttention`） |
| 多实例约束 | `docs/gateway/15-multi-instance-architecture.md` |
| 现有通知语义 | `docs/gateway/08-notifications.md` |
| v78 连接器只读聚合 | `backend/cmd/gateway/node_connectors_api.go`、`backend/cmd/gateway/console/app.js` |