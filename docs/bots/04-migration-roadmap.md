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

**实现状态（2026-10-13 首轮）**：

| 交付物 | 状态 | 位置 |
|--------|------|------|
| 事件异步订阅原语 | ✅ 完成 | `backend/internal/events/async.go`（`SubscribeAsync` + 队列/丢弃策略/panic 隔离/计数）+ `async_test.go`（9 例） |
| notify 策略内核 | ◐ 部分 | `backend/internal/notify/policy.go`（优先级/免打扰/合并）+ 薄适配 `cmd/gateway/notify_policy.go`；Channel/Dispatcher 留待 P1 |
| 契约包骨架 | ✅ 完成 | `backend/internal/connectors/api/schema.go`（Schema 契约 + 校验/脱敏/合并提交）+ `internal/connectors/restapi`（管理面 CRUD/schema/test/reload + webhook 入口，12 例）；三宿主契约矩阵随各宿主接线补齐 |
| 通知语义对照表 | ◐ 部分 | `internal/notify` 的 `PriorityOf` 即落点（approval/question/error→high，completed/waiting→normal）；与 supervision/IM 文案的映射在 P1 随通道落地补齐 |

### P0 · 骨架 + Telegram + runtime-server（≈3–4 天）

- 交付：`internal/connectors` 核心（Provider/Backend/Store/Queue/Ledger/Schema）+ Telegram 适配器（webhook 与长轮询双模式）
  + runtime-server `connector_bootstrap.go`（Backend 复用 `submit_prompt`）+ `GET/POST /api/connectors*` + 最小 React 页面。
- 验收：Telegram 发「你好」→ `runtime-server` 建会话跑 turn → 回复到 Telegram；流式编辑可用；重启进程后 offset 续传不重复。
- 理由：Telegram 第 3 方依赖最简单，webhook/polling 双模式可一次性验证两种入口形态。

**实现状态（2026-10-13 第一切片）**：

| 交付物 | 状态 | 位置 |
|--------|------|------|
| 连接器核心 | ✅ 完成 | `backend/internal/connectors/{types,store,queue,throttle,service}.go`：Provider/Backend 两层契约、内存 Store（配置/绑定/去重账本/轮询游标）、每会话串行有界队列（满则 `ErrQueueFull`，跨会话并行）、流式节流（≥800ms 且内容变化）、命令 `/help` `/new` `/status`、失败回执 |
| Telegram 适配器 | ◐ polling 完整 | `internal/connectors/providers/telegram`：`getMe` 激活、`sendMessage`、`getUpdates` 长轮询（游标 `update_offset` 持久化 + 指数退避）、`sendChatAction` typing 保活、占位消息 + `editMessageText` 流式编辑；`api_base` 配置项（默认 `https://api.telegram.org`，可指向自建 Bot API 服务器/测试替身，按连接器生效）；宿主 webhook 入口已就绪，Provider 侧 `HandleWebhook` 解析待 P1 |
| 端到端（假后端） | ✅ 完成 | `service_test.go`（12 例：绑定/去重/白名单/命令/节流/队列/失败路径）+ `telegram_test.go`（8 例，`httptest` 假 Bot API：解析/游标/重试/typing/流式）；`go build ./...`、`go vet`、`go test -race` 全绿 |
| 管理面 REST（三端共用） | ✅ 完成 | `internal/connectors/restapi`：`/connectors*` 增删改查 + schema + 测试消息 + reload；`/hooks/connectors/{id}` 公网入口（`WebhookProvider` 能力探测，签名失败 400/队列满 429/未知 404）；密钥只写不读、留空=保持、`__clear__`=清除；`Authorize` 未接线整体 403；restapi_test 12 例 |
| 宿主接线 | ◐ runtime-server 完成 | `cmd/runtime-server/connector_bootstrap.go`：配置读写共享 `config.yaml` 的 `connectors:` 节（`internal/connectors/storefile`：YAML Node 往返 + 原子写，密钥单独落 `connectors.secrets.yaml`(0600)）、绑定/游标/去重账本落 `data/connectors/state.json`；Backend 走进程内 `runtimeapi.SubmitConnectorPrompt`（`submit_prompt` 执行路径 + 事件总线 `assistant_delta` 增量回推）；管理面 `/api/connectors*`（复用 skills 管理面鉴权口径）与 webhook `/hooks/connectors/{id}` 已挂载并随宿主 `close()` 停机。**待续**：aicli 宿主、真通道 e2e |
| React 配置页（runtime-server） | ✅ 完成 | `frontend/src/pages/connectors-page.tsx`（路由 `/runtime/connectors`，懒加载）：列表 + 启停/编辑/测试消息/重载/删除 + 新建；`pages/connectors/{connector-dialog,test-message-dialog}.tsx` + `shared.ts`（纯逻辑），表单由 `GET /connectors/schema` 驱动（text/secret/bool/enum/url），密钥留空=保持、`__clear__`=清除，客户端预校验与字段级错误回填；`api/runtime/connectors.ts` + `types/runtime/connectors.ts`；i18n `connectors` 命名空间（zh-CN/en-US）；api/shared/dialog 单测 15 例 |
| 端到端（假 Bot API，重启续传） | ✅ 完成 | `internal/connectors/e2e_restart_test.go`：httptest 假 Bot API（**总是重投同一 update**，模拟 at-least-once）+ Service + storefile 文件态；首轮处理 → 占位 + `editMessageText` 回执，游标 `update_offset` 落盘；重建 Store/Service/Provider = 进程内重启，首次 getUpdates 续传 offset，重投消息被持久化去重账本拦截（不二次处理/不二次回复）；`go test -race` 全绿。真实 token 联通为手动步骤（`api_base` 可指向任意 Bot API 端点） |

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
  （◐ 通道核心 + polling + runtime-server 宿主接线 + React 配置页已就绪；「重启续传不重复」已有自动化 e2e
  （假 Bot API 重投 + storefile 文件态，`internal/connectors/e2e_restart_test.go`）；真实 token 联通验证与 aicli 宿主待续）
- [ ] P1：飞书 ↔ gateway → 节点会话端到端；审批/提问命令闭环；完成提醒送达且与 Web Push/ntfy 去重。
- [ ] P2：aicli 本地 Telegram/QQ 端到端；配置在三宿主间可迁移；未配置连接器时回归全绿。
- [ ] P3：微信扫码登录 + 文本/图片/语音；IM 通道可开关/静音。
- [ ] 通用性：新增一个通道（假 provider）只需改核心，三端前端零改动。
- [ ] 安全：签名校验、白名单、密钥不回流三项有单测/端到端证据。
- [ ] 可观测：每连接器状态/计数/最近错误/测试消息在两端 UI 可见。
