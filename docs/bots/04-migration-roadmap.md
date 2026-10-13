# 04 · 移植映射与实施路线

> 上游：[02-solution-design.md](./02-solution-design.md) ｜ [03-host-integration.md](./03-host-integration.md)

## 1. 从 codex-server 的移植映射

参考仓库：`E:\projects\ai\codex-server\backend\internal\bots`（下称源实现）。

| 源实现 | 处置 | 说明 |
|--------|------|------|
| `Provider` / 能力接口（`types.go:39-147`） | **直接复用（改名/去 store 类型）** | `Provider`、`WebhookResultProvider`、`StreamingProvider`、`TypingProvider`、`PollingProvider(+Ownership)`、`ReplyDeliveryRetryDecider` 原样保留语义 |
| `InboundMessage`/`OutboundMessage`/`AIResult`（`types.go:172-194`） | **直接复用** | 把 `ProviderData` 保留为 `map[string]any` 逃生舱 |
| Telegram 适配器（`telegram.go` 等） | **小幅改写** | 去掉 `store.BotConnection` 引用换成 `ConnectorConfig`；`getUpdates` offset、secret token 校验、媒体组聚合、编辑式流式全部保留 |
| 飞书适配器（`feishu.go` / `feishu_ws.go` 双模式） | **小幅改写** | challenge 处理、WS 长连接与重连、打字表情、三种纯文本流式策略、卡片清洗保留；审批事件路由改造为本项目 `approval_requested` |
| QQ 适配器（`qqbot.go`） | **中等改写** | WS Identify/Resume/Heartbeat 状态机、session_id+seq 持久化、2000 rune 分片、媒体上传保留；`Settings` 读写换成本项目 `Conversation.cursor/state` |
| 微信适配器（`wechat*.go`） | **中等改写** | 长轮询 `getupdates` + `get_updates_buf` 游标、typing ticket、追加式流式、errcode -14/-2 处理、失败重放保留；扫码登录（`wechat_auth.go`）需重接到本项目的本地账号/凭据存储 |
| 媒体与转码工具（`*_media.go`、`*_transcode.go`、`markdown_filter.go`） | **直接复用** | 依赖 ffmpeg 的按可用性降级（本项目已有"工具缺失即降级"惯例） |
| `Service` 编排（`service.go`：worker/queue/poller/command/approval） | **重写（保留结构）** | 源实现硬依赖 `*store.MemoryStore`（`service.go:55-85`）与 `threads/turns`；本项目改为 `Store` 接口 + `Backend` 接口，其余模型（每会话串行 worker、命令旁路、轮询所有权、幂等账本）照搬 |
| `workspace_thread_backend.go`（默认 AIBackend） | **替换** | 换成三个宿主 Backend 适配（见 03 文档 §2-§4） |
| `store.Bot*` 模型（`models.go:785-1015`） | **收敛替换** | 降为 6 个模型（见 02 文档 §4），去掉 `Bot/Trigger/DeliveryTarget/WorkspaceID` 维度 |
| `notificationcenter` / `trigger_dispatcher` | **不移植** | 本项目已有注意力事件 + Web Push/ntfy；IM 作为第三通道即可 |
| REST 层（`router.go` bots 段） | **重写（对齐契约）** | 采用 03 文档 §6 的统一契约（`/api/connectors*` + `/hooks/connectors/{id}`） |
| 前端（`BotsPage.tsx` / `BotConnectionDetailPage.tsx`） | **重写为 Schema 渲染器** | 源页面每通道硬编码字段；本项目用 Schema 统一渲染 |

## 2. 分阶段实施

### P-1 · 基础加固（≈2–3 天，详见 [05-review-and-hardening.md](./05-review-and-hardening.md) §8）

- 交付：`internal/events` 异步订阅原语（有界队列 + panic 隔离 + 丢弃策略 + 指标）；
  `internal/notify` 通知中台内核抽取（gateway push/ntfy 改造，行为由现有单测护栏）；
  通知语义对照表；`internal/connectors/api` 契约包与三宿主契约测试矩阵骨架。
- 理由：不先做这三项，连接器会把"慢通道阻塞 agent、通知多响、前端漂移"三个问题带进现有系统。

### P0 · 骨架 + Telegram + runtime-server（≈3–4 天）

- 交付：`internal/connectors` 核心（Provider/Backend/Store/Queue/Ledger/Schema）+ Telegram 适配器（webhook 与长轮询双模式）
  + runtime-server `connector_bootstrap.go`（Backend 复用 `submit_prompt`）+ `GET/POST /api/connectors*` + 最小 React 页面。
- 验收：Telegram 发「你好」→ `runtime-server` 建会话跑 turn → 回复到 Telegram；流式编辑可用；重启进程后 offset 续传不重复。
- 理由：Telegram 第 3 方依赖最简单，webhook/polling 双模式可一次性验证两种入口形态。

### P1 · 飞书 + gateway 前置 + 三端配置页（≈4–5 天）

- 交付：飞书适配器（webhook challenge + 事件签名 + 长连接 WS 可选）+ gateway `Backend=gateway_node`
  （IM → 指定节点会话）+ `/hooks/connectors/{id}` 公网入口 + gateway console / aicli micro web 两个 Schema 页面。
- 验收：飞书 @bot → 经 gateway 路由到本机 node 的会话；审批/提问在飞书内以命令闭环；
  完成提醒经 IM 送达（与 Web Push/ntfy 同源不重复）。
- 依赖：gateway webhook 命名空间与每连接器签名校验（安全评审点）。

### P2 · aicli 本地接入 + QQ（≈3–4 天）

- 交付：aicli `Backend=local_chat`（`localActorRegistry.SubmitPrompt`）+ 微前端页面 + `connectors:` 配置节与热应用；
  QQ 适配器（WS gateway、断线 resume）。
- 验收：本机 aicli 不联网关也能用 Telegram/QQ 对话；配置在 aicli 与 gateway 间可迁移。

### P3 · 微信 + 媒体/主动推送完善（≈4–5 天）

- 交付：微信长轮询适配器 + 扫码登录（actions 流程）+ 媒体转码（ffmpeg 降级）+
  IM 作为注意力通道的可选项（与 Web Push/ntfy 并列，支持每绑定开关/静音）。
- 验收：微信扫码登录 → 收发文本/图片/语音（转码）；手机上收到「待审批/已完成」提醒，可关闭。

## 3. 测试策略

| 层级 | 内容 |
|------|------|
| 单元 | Provider 解析（各通道报文样例）、命令解析、去重账本、节流器、重试退避（源实现已有大量样例可参考） |
| 契约 | 用 `httptest` 假 IM 服务端：webhook challenge/签名、`getUpdates` 分页与 offset、媒体上传/下载；Backend 假实现验证 sink 语义（Delta→Final） |
| 端到端 | P0 起每阶段一个真通道 e2e（Telegram 双模式；飞书 challenge + 事件；QQ WS resume；微信长轮询）＋「配置迁移」用例 |
| 回归 | 未配置连接器时三宿主既有测试全绿；`internal/events` 新增事件须过 `contract.go` 登记门禁 |
| 安全 | 未签名/错误签名的 webhook 一律 401/403；白名单外用户静默忽略；API 不返回明文 secrets |

## 4. 风险与对策

| 风险 | 影响 | 对策 |
|------|------|------|
| 飞书 WS / QQ WS 稳定性（源实现有指数重连） | 掉线漏消息 | 照搬重连状态机 + 游标/seq 持久化；UI 显示 `last_connected_at` |
| 微信登录链路（扫码、errcode -14/-2） | 接入门槛高 | 登录流程独立为 `actions`，失败可重试；文档标注"需要官方账号能力" |
| 媒体转码依赖 ffmpeg | 无 ffmpeg 时失败 | 启动检测 + 降级提示；只转换需要的类型 |
| 事件总线无背压（`bus.go:314`） | 高并发下阻塞宿主 | 连接器侧有界队列 + 流式节流 + 终态优先；队列满丢弃 delta 并计数 |
| 公网 webhook 暴露 | 被探测/伪造 | 每连接器独立路径 + 签名/secret 校验 + 速率限制（复用网关限流） |
| 多实例部署时轮询冲突 | 重复回复 | 沿用源实现的 `PollingOwnershipProvider`（owner key + 冲突报错） |
| 与现有 owner-session 鉴权冲突 | 无法收 webhook | 入站**独立命名空间**，仅做通道校验；管理面仍走 owner |
| 配置项膨胀导致前端复杂 | 违背需求 2 | 强制 Schema 驱动 + 字段最小化（首期每通道 ≤ 4 个字段）；评审新增字段 |

## 5. 工作量粗估

| 阶段 | 人日 | 备注 |
|------|------|------|
| P-1 | 2–3 | 事件异步订阅 + notify 内核 + 契约包骨架（对现有系统亦有价值） |
| P0 | 3–4 | 核心骨架可复用源实现结构，主要成本在 Store/Backend 接口化 |
| P1 | 4–5 | 飞书 + gateway 入口是安全与联调重点 |
| P2 | 3–4 | aicli 装配 + 配置分层 + QQ WS |
| P3 | 4–5 | 微信登录 + 媒体链路最重 |
| 合计 | **16–21 人日** | 不含产品化打磨（多语言、告警、文档站点） |

## 6. 验收清单（汇总）

- [ ] P0：Telegram（webhook + polling）↔ runtime-server 端到端；重启续传不重复。
- [ ] P1：飞书 ↔ gateway → 节点会话端到端；审批/提问命令闭环；完成提醒送达且与 Web Push/ntfy 去重。
- [ ] P2：aicli 本地 Telegram/QQ 端到端；配置在三宿主间可迁移；未配置连接器时回归全绿。
- [ ] P3：微信扫码登录 + 文本/图片/语音；IM 通道可开关/静音。
- [ ] 通用性：新增一个通道（假 provider）只需改核心，三端前端零改动。
- [ ] 安全：签名校验、白名单、密钥不回流三项有单测/端到端证据。
- [ ] 可观测：每连接器状态/计数/最近错误/测试消息在两端 UI 可见。
