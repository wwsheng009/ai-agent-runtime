# 02 · 解决方案设计：连接器核心（`internal/connectors`）

> 上游：[01-requirements.md](./01-requirements.md) ｜ 宿主接入细节见 [03-host-integration.md](./03-host-integration.md) ｜ 移植路线见 [04-migration-roadmap.md](./04-migration-roadmap.md)

## 1. 设计原则

1. **两层解耦（照搬 codex-server 的成功抽象）**：
   - `Provider`（通道适配：收/发/媒体/鉴权）——与被接入的 IM 绑定；
   - `Backend`（agent 执行：把一条消息变成一次 turn 并回吐输出）——与宿主绑定。
   两者通过 `InboundMessage` / `OutboundMessage` 数据契约连接，核心不感知具体 IM 或宿主。
2. **能力接口而非胖接口**：流式、typing、长轮询、媒体等用可选接口声明（`StreamingProvider`、`PollingProvider`…），
   通道按能力实现，编排层运行时探测（codex-server 已验证该模式，`types.go:47-124`）。
3. **配置模型收敛为两层**：`Connector`（通道实例）+ `Binding`（IM 会话 → 本机会话）。
   不做 workspace/bot/trigger/delivery-target 多层模型；主动推送复用现有注意力通道。
4. **Schema 驱动前端**：核心导出字段 Schema（JSON），三个前端共用渲染器——**新增通道不改前端**。
5. **默认零配置可用**：不配置连接器时，宿主行为与现状完全一致（新增代码全部惰性装配）。
6. **总线无背压 → 连接器自带边界**：`events.Bus.Publish` 在发布者 goroutine 同步回调
   （`backend/internal/events/bus.go:314`），连接器必须：有界队列 + 每会话串行 + 流式节流 + 幂等账本。

## 2. 总体架构

```text
        IM 侧（飞书/QQ/微信/Telegram）
             │  webhook / 长轮询 / WS
             ▼
┌──────────────────────────── 宿主进程（gateway / aicli / runtime-server）────────────────────────────┐
│  ingress 层                                                                                          │
│   • HTTP: POST /hooks/connectors/{id}   （签名/挑战校验，独立于 owner session）                        │
│   • Poller/WS Runner（进程内 goroutine，游标持久化）                                                  │
│        │                                                                                             │
│        ▼   Connectors.Service（共享核心 internal/connectors）                                         │
│   ┌───────────────┐   ┌───────────────┐   ┌────────────────┐   ┌──────────────────┐                  │
│   │ Registry      │   │ Router        │   │ Queue/Worker   │   │ Ledger/Retry     │                  │
│   │ providers+cfg │   │ IM chat→sess  │   │ 每会话串行+有界 │   │ 入站去重/出站幂等 │                  │
│   └───────┬───────┘   └───────┬───────┘   └───────┬────────┘   └──────────────────┘                  │
│           │                   │                   │                                                  │
│           ▼                   ▼                   ▼                                                  │
│   Provider 实现         Binding 存储          Backend 适配                                            │
│   （providers/*）      （宿主 Store）        （宿主 agent 执行 + 事件订阅）                            │
│                                                    │                                                 │
│        ┌───────────────────────────────────────────┼──────────────────────────────────┐              │
│        ▼                                           ▼                                  ▼              │
│  runtime-server: POST /api/agent/chat      aicli: localActorRegistry.SubmitPrompt   gateway: 隧道转发   │
│  + runtimeEventBus 订阅                     + chat 事件总线                          /n/{node} 到会话   │
└──────────────────────────────────────────────────────────────────────────────────────────────────────┘
```

## 3. 核心抽象（接口草案）

> 命名与 codex-server 对齐以便移植；`store.Bot*` 引用统一替换为本仓的轻量模型。

```go
package connectors

// ── 通道适配 ────────────────────────────────────────────────────────────────
type Provider interface {
    Name() string                                   // "telegram" | "feishu" | "qqbot" | "wechat"
    Schema() ProviderSchema                         // 配置字段（前端渲染 + 校验）
    Activate(ctx context.Context, cfg ConnectorConfig) (Activation, error)
    Deactivate(ctx context.Context, cfg ConnectorConfig) error
    ParseWebhook(r *http.Request, cfg ConnectorConfig) ([]InboundMessage, error)
    Send(ctx context.Context, cfg ConnectorConfig, chat ChatKey, msgs []OutboundMessage) error
}

// 可选能力（运行时探测）
type StreamingProvider interface {                   // 流式编辑/追加
    Provider
    StartStreaming(ctx context.Context, cfg ConnectorConfig, chat ChatKey) (StreamingSession, error)
}
type TypingProvider interface { Provider; StartTyping(ctx, cfg, chat) (TypingSession, error) }
type PollingProvider interface {                     // 长轮询 / WS
    Provider
    SupportsPolling(cfg ConnectorConfig) bool
    RunPolling(ctx context.Context, cfg ConnectorConfig, h InboundHandler, state StateStore) error
}
type WebhookResultProvider interface {               // 自定义 HTTP 响应（飞书 challenge）
    Provider
    ParseWebhookResult(r *http.Request, cfg ConnectorConfig) (WebhookReply, []InboundMessage, error)
}
type MediaDownloader interface { Download(ctx, cfg, media MediaRef) (LocalFile, error) }
type MediaUploader   interface { Upload(ctx, cfg, chat ChatKey, file LocalFile) (MediaRef, error) }

// ── Agent 执行（宿主实现）────────────────────────────────────────────────────
type Backend interface {
    Name() string                                    // "runtime_session" | "local_chat" | "gateway_node"
    // Handle 把一条入站消息转成一次 agent 交互；实现可长驻直至回合结束，
    // 期间通过 sink 推送流式片段/终态；返回最终结果与绑定信息。
    Handle(ctx context.Context, req BackendRequest, sink ReplySink) (BackendResult, error)
}
type BackendRequest struct {
    ConnectorID string
    Chat        ChatKey          // IM 侧会话标识（provider + external id）
    SessionID   string           // 绑定已有会话时非空
    Text        string
    Media       []LocalFile
    Command     Command          // 已解析的命令（/new、/approve…）
    User        UserRef
}
type ReplySink interface {       // 出站回调（核心 → Provider）
    Delta(text string) error                        // 流式片段（已由核心节流合并）
    Final(msgs []OutboundMessage) error
    Fail(err error, userText string) error
}

// ── 核心服务 ────────────────────────────────────────────────────────────────
type Service struct { /* registry/bindings/queue/ledger/backends/providers */ }
func (s *Service) Start(ctx context.Context) error
func (s *Service) Stop(ctx context.Context) error
func (s *Service) WebhookHandler(connectorID string) http.Handler        // 挂到 /hooks/connectors/{id}
func (s *Service) SendOutbound(ctx context.Context, chat ChatKey, msgs []OutboundMessage) error  // 主动推送/触发
```

### 3.1 Schema（前端契约）

```go
type ProviderSchema struct {
    Provider string        `json:"provider"`
    Label    string        `json:"label"`
    Fields   []FieldSchema `json:"fields"`      // key/label/type(text|secret|bool|enum|url)/required/placeholder/help
    Actions  []ActionSpec  `json:"actions"`     // 例如 wechat: start_login(qr) / poll_login
    Modes    []ModeSpec    `json:"modes"`       // 例如 telegram: webhook|polling
}
```

前端只做三件事：按 `fields` 渲染表单 → 调 `/api/connectors` 保存 → 按 `actions` 触发特殊流程（扫码）。
**新增通道 = 核心加一个 provider + 实现 Schema；前端零改动。**

## 4. 数据模型（收敛版）

| 模型 | 字段（要点） | 说明 |
|------|--------------|------|
| `Connector` | `id, provider, name, enabled, mode, settings(map), secrets(map), status, last_error, counters` | 通道实例；`secrets` 只写不读（API 返回 `secret_keys`） |
| `Binding` | `connector_id, chat_key, session_ref, node_id?, policy(command_prefix, allowed_users, auto_session), created_at` | IM 会话 → 本机会话；默认自动建会话 |
| `Conversation` | `connector_id, chat_key, session_id, cursor(map), state(map)` | 游标（Telegram offset / 微信 get_updates_buf / QQ session_id+seq）与状态（typing ticket 等） |
| `InboundLedger` | `connector_id, message_id, status, claimed_at` | 入站去重（至少一次投递） |
| `OutboundLedger` | `connector_id, chat_key, idempotency_key, status, attempts, next_retry_at` | 出站幂等 + 有界重试 |
| `ConnectorLog` | `connector_id, ts, level, direction, summary` | 诊断日志（滚动上限） |

> 与 codex-server 的差异：去掉 `Bot`、`ThreadBinding`、`Trigger`、`DeliveryTarget`、`WorkspaceID` 维度；
> 主动推送直接复用现有注意力事件与 `SendOutbound`（P3 再考虑「IM 作为通知通道」的偏好设置）。

## 5. 消息流转

### 5.1 入站（IM → agent）

```
webhook/poller → ParseWebhook/解析 → 归一化 InboundMessage
 → InboundLedger.Claim（幂等）→ Binding 查找（无则 auto 建会话）
 → 命令拦截（/new /threads /rename /archive /approve /deny /answer）
 → 媒体下载/转码（按 MediaDownloader 能力）
 → Queue.Enqueue（key=connector+chat，串行、有界、2min 空闲回收）
 → Backend.Handle（宿主 agent 执行，sink 出站）
```

### 5.2 出站（agent → IM）

```
Backend sink（Delta/Final/Fail）
 → 节流器（delta 合并窗口，默认 800ms；终态直通）
 → 能力探测：StreamingProvider → StartStreaming().Update() 逐段编辑
             否则 TypingProvider → StartTyping() 保活，最终一次性发送
 → OutboundLedger（幂等键 = connector+chat+turn_id+seq）→ Provider.Send
 → 失败：指数退避重试（尊重 Retry-After）；不可重试 → 降级为纯文本 → 仍失败写 ConnectorLog
```

### 5.3 审批 / 提问往返

- 出站：宿主事件 `approval_requested` / `question_asked`（`backend/internal/chat/events.go:22-25`）
  → 渲染为 IM 文本（含 `/approve <id>`、`/deny <id>`、`/answer <id> <n>`）。
- 入站：IM 命令 → 宿主对应动作：
  - runtime-server：`POST /sessions/{id}/runtime/commands` 的 `approve_tool` / `answer_question`
    （`session_runtime_handlers.go:851` 及测试中的命令样例）；
  - aicli：actor 的审批/提问响应通道（与 TUI 同一路径）。
- 关联键：请求 `request_id`/`question_id` 写入 `Conversation.state`，回复时校验归属，避免串会话。

### 5.4 主动推送（复用现有注意力链路）

- gateway：`pushNotifier` 已经是「attention 差分 → 多通道扇出」（Web Push + ntfy）。
  接入 IM 时把它当**第三条通道**：`connectors.SendOutbound(chat, msgs)`，优先级沿用
  `isHighPriorityAttention`（approval/question/error 立即；completed 合并/静音，见 `notify_policy.go:16-122`）。
- aicli / runtime-server：订阅同一批事件（`internal/events/contract.go:62-106` 已登记类型），
  用 `Binding` 找到「发到哪个 IM 会话」；无绑定则不发（避免打扰）。

## 6. 目录规划与依赖注入

```
backend/internal/connectors/                 # 共享核心（无宿主依赖）
  service.go registry.go router.go queue.go ledger.go schema.go
  message.go media.go commands.go throttle.go store.go        # store 为接口
  providers/telegram/{telegram.go,schema.go,media.go}
  providers/feishu/{feishu.go,feishu_ws.go,schema.go,media.go,card.go}
  providers/qqbot/{qqbot.go,schema.go,media.go}
  providers/wechat/{wechat.go,login.go,schema.go,media.go}
backend/cmd/gateway/connectors.go            # 装配 + /hooks/connectors/{id} + console 页面数据接口
backend/cmd/aicli/commands/connectors*.go    # 装配 + 微前端接口（chat_http.go 旁路）
backend/cmd/runtime-server/connector_bootstrap.go   # 镜像 gateway_bootstrap.go:23-48 的装配写法
frontend/src/pages/connectors-page.tsx       # React 页面（Schema 渲染器 + 连接器列表）
backend/cmd/aicli/commands/web/js/connectors.js     # 微前端页面（同一 Schema 渲染器，vanilla JS）
backend/cmd/gateway/console/app.js           # gateway console 新增 "连接器" 视图（同一 Schema）
```

注入方式（三宿主一致）：

```go
svc := connectors.New(connectors.Deps{
    Store:    hostStore,          // 宿主实现 connectors.Store 接口
    Backend:  backendAdapter,     // 宿主 agent 适配
    Log:      logger, Metrics: metrics,
})
svc.Register(telegram.New(), feishu.New(), qqbot.New(), wechat.New())
svc.Start(ctx)
router.Handle("/hooks/connectors/", svc.WebhookMux())   // 仅 gateway / runtime-server 暴露
```

## 7. 安全模型

| 面 | 措施 |
|----|------|
| webhook 鉴权 | Telegram：`X-Telegram-Bot-Api-Secret-Token` 常量时间比较（参考 `telegram.go:283-289`）；飞书：URL challenge + 事件签名；QQ/微信：官方签名/回调校验；校验失败一律 401/403 |
| 命名空间 | 新入口独立为 `/hooks/connectors/{id}`，**不**挂在 owner session 下；每连接器独立路径（可轮换 id） |
| 用户白名单 | `Binding.policy.allowed_users`（默认仅 owner 的 IM openid/uid）；未授权用户静默忽略并记日志 |
| 密钥 | 存宿主本地（gateway `--data-dir`、aicli `~/.aicli`、runtime-server 配置目录），文件 0600；API 只返回 `secret_keys` |
| 越权防护 | 命令解析白名单（不使用 shell 拼接）；媒体下载限制大小/类型/域名白名单；出站只允许回原会话 |
| 审计 | 每条 IM→turn、turn→IM 落 ConnectorLog；与网关审计（`internal/gateway/audit`）可对齐 |

## 8. 可观测性

- 指标：`connectors_up{provider}`、`connectors_inbound_total`、`connectors_outbound_total`、`connectors_failures_total`、
  `connectors_queue_depth`、`connectors_poll_lag_seconds`。
- 日志：结构化（connector_id/provider/direction/summary），错误附可操作建议（token 失效/被踢下线/限流）。
- 三端 UI 一致的「连接器」页：状态灯、最近错误、收发计数、**发送测试消息**、webhook 可达性自检。
- 复用 push/ntfy 的「自测端点」样板（`cmd/gateway/push.go:265-385`）。

## 9. 失败与降级

| 场景 | 行为 |
|------|------|
| 通道凭据失效 | 状态置 `error` + 提示；其它通道不受影响；不阻塞宿主启动 |
| 长轮询/WS 断开 | 退避重连（1s→2s→…→30s，抖动）；游标持久化，重启续传 |
| 出站限流 | 尊重 Retry-After，指数退避，超限降级纯文本/丢弃并记日志 |
| 媒体不支持 | 降级为「收到附件（类型）」文字提示，agent 仍可继续 |
| Backend 不可用（如节点离线） | 回复固定失败文案 + 记录；消息保留在账本供重放（参考 `wechat_failed_reply_recovery` 思路） |
| 总线突发 | 有界队列满 → 丢弃过期流式 delta（保留终态），并计数告警 |
