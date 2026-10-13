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
| Telegram 适配器 | ◐ polling 完整 | `internal/connectors/providers/telegram`：`getMe` 激活、`sendMessage`、`getUpdates` 长轮询（游标 `update_offset` 持久化 + 指数退避）、`sendChatAction` typing 保活、占位消息 + `editMessageText` 流式编辑；`api_base` 配置项（默认 `https://api.telegram.org`，可指向自建 Bot API 服务器/测试替身，按连接器生效）；错误文本脱敏 `bot_token`（`client.Do` 的 URL 内嵌）；Schema 仅声明已实现的 `polling`（webhook 待 `HandleWebhook`/`setWebhook` 实现后加回，旧 `mode=webhook` 配置不报错、仅不轮询）；宿主 webhook 入口已就绪，Provider 侧 `HandleWebhook` 解析待 P1 |
| 端到端（假后端） | ✅ 完成 | `service_test.go`（12 例：绑定/去重/白名单/命令/节流/队列/失败路径）+ `telegram_test.go`（8 例，`httptest` 假 Bot API：解析/游标/重试/typing/流式）；`go build ./...`、`go vet`、`go test -race` 全绿 |
| 管理面 REST（三端共用） | ✅ 完成 | `internal/connectors/restapi`：`/connectors*` 增删改查 + schema + 测试消息 + reload；`/hooks/connectors/{id}` 公网入口（`WebhookProvider` 能力探测，签名失败 400/队列满 429/未知 404）；密钥只写不读、留空=保持、`__clear__`=清除；`Authorize` 未接线整体 403；restapi_test 12 例 |
| 宿主接线 | ◐ runtime-server + aicli 完成 | runtime-server：`cmd/runtime-server/connector_bootstrap.go`（Backend 走进程内 `runtimeapi.SubmitConnectorPrompt`，管理面复用 skills 鉴权口径，随宿主 `close()` 停机）。aicli：`cmd/aicli/commands/connector_host.go`（`StartLocalConnectors`/`MountLocalConnectorRoutes`，交互式会话装配、随 chat cleanup 停机；Backend 走 `localActorRegistry.SubmitPrompt`，无绑定按 `conn-<connector>-<chat>` 自动建会话；路由挂在 loopback 同一 mux，沿用 `ChatWebAuthGuard`）。两端共享 `config.yaml` 的 `connectors:` 节（storefile：YAML Node 往返 + 原子写，密钥 `connectors.secrets.yaml`(0600)）与 `state.json`（目录随 `configPath` 同源推导）。**审查修复（2026-10-13）**：轮询/异步任务与请求 ctx 解耦（`Service` 持宿主基 ctx）、去重键含 chat、队列关闭竞态与轮询代际登记修复、白名单 fail-closed（aicli `RequireAllowedUsers` 强制 / runtime-server 仅告警）、IM 轮次统一显式 `bypass_permissions`、环回判定不再无条件信任 `X-Forwarded-For`。**待续**：gateway 宿主。**运行归属（2026-10-12，P1 切片）**：`runtime_owner` 抢占 + 跨进程数据目录锁完成——`state.json` 增 `owners: {owner, heartbeat_at, pid}`（旧文件兼容），storefile 认领在 `<stateDir>/connectors.lock` 目录锁内完成「读旧值 → 判存活 → 写新值」；`Service` 归属闸（`Deps.HostID`、90s 陈旧接管、30s 心跳，aicli/runtime-server 已接线）、停机与轮询退出释放；REST 视图 `active_owner`/`owned_by_current_host` + `POST /connectors/{id}/takeover`（见 03 §7 / 07 §5.6） |
| React 配置页（runtime-server） | ✅ 完成 | `frontend/src/pages/connectors-page.tsx`（路由 `/runtime/connectors`，懒加载）：列表 + 启停/编辑/测试消息/重载/删除 + 新建；`pages/connectors/{connector-dialog,test-message-dialog}.tsx` + `shared.ts`（纯逻辑），表单由 `GET /connectors/schema` 驱动（text/secret/bool/enum/url），密钥留空=保持、`__clear__`=清除，客户端预校验与字段级错误回填；`api/runtime/connectors.ts` + `types/runtime/connectors.ts`；i18n `connectors` 命名空间（zh-CN/en-US）；api/shared/dialog 单测 15 例 |
| 端到端（假 Bot API，重启续传） | ✅ 完成 | `internal/connectors/e2e_restart_test.go`：httptest 假 Bot API（**总是重投同一 update**，模拟 at-least-once）+ Service + storefile 文件态；首轮处理 → 占位 + `editMessageText` 回执，游标 `update_offset` 落盘；重建 Store/Service/Provider = 进程内重启，首次 getUpdates 续传 offset，重投消息被持久化去重账本拦截（不二次处理/不二次回复）；`go test -race` 全绿。真实 token 联通为手动步骤（`api_base` 可指向任意 Bot API 端点） |

### P1 · 飞书 + gateway 前置 + 三端配置页（≈4–5 天）

- 交付：飞书适配器（webhook challenge + 事件签名 + 长连接 WS 可选）+ gateway `Backend=gateway_node`
  （IM → 指定节点会话）+ `/hooks/connectors/{id}` 公网入口 + gateway console / aicli micro web 两个 Schema 页面。
- 验收：飞书 @bot → 经 gateway 路由到本机 node 的会话；审批/提问在飞书内以命令闭环；
  完成提醒经 IM 送达（与 Web Push/ntfy 同源不重复）。
- 依赖：gateway webhook 命名空间与每连接器签名校验（安全评审点）。
- 状态（2026-10-12，飞书切片 3）：飞书适配器 ◇ **webhook 完成**——
  `internal/connectors/providers/feishu`：`modes=["webhook"]`（WS 未实现前不声明），
  字段 `app_id`/`app_secret`(secret)/`verification_token`(secret)/`encrypt_key`(secret)/
  `api_base`（默认 `https://open.feishu.cn`），无 actions；`tenant_access_token`
  按 connectorID 缓存 + 提前 5 分钟刷新（Activate 验凭据、bot info 轻量探测）、
  `url_verification` challenge（token 常量时间比较，未配置 verification_token 时
  兼容放行并告警）、事件 v2 `header.token` 校验与 `encrypt_key` AES-256-CBC 解密
  （密钥 `sha256(encrypt_key)`，失败 400）、`im.message.receive_v1` 文本入站
  （幂等键=header.event_id、非文本占位）、回复优先 reply 原消息（msg_id 进程内记忆，
  失败降级 chat 直发）、错误脱敏 app_secret/token。测试：`httptest` 假飞书 API
  覆盖 challenge（正确/错配/未配 token）、明文/密文事件解析与解密失败、token 缓存
  与提前刷新、Send reply/create/降级/脱敏，`go test -race` 全绿；
  runtime-server 与 aicli 均已注册。
  **待续**：WS 长连接与重连、卡片式流式回复、typing 表情、媒体上传/下载、
  审批事件路由、msg_id 记忆跨重启持久化。
- 状态（2026-10-12，gateway 宿主接线切片，与前端切片并行）：**gateway 宿主 ◐ 完成**
  ——`cmd/gateway/connector_bootstrap.go` 按 `--data-dir` 装配 `storejson`
  （`connectors.json` 配置+密钥 0600、`connectors-state.json` 状态 0600，
  跨进程写锁复用 `storefile.AcquireDirLock`）+ 四 Provider + `HostID="gateway"`；
  `/api/connectors*` 包 `owner.RequireSession`、`/hooks/connectors/*` 直挂，均在
  console `/` 兜底之前；失败只 warn、退出时 Stop。`Backend=gateway_node` 经
  `tunnel.Server.ServeProxy` 调节点 `POST /web/api/invoke`：`settings["node_id"]` >
  唯一非 stale 节点 > 明确报错；默认 180s/上限 300s；节点令牌由节点侧隧道代理注入
  （gateway 不持令牌）。console 增 `#connectors` 最小列表页（读取 `/api/connectors`
  + Schema/JSON 跳转，零构建）。单测：storejson（往返/级联/归属/双实例）+
  gateway_node（节点选择/成功回复/无节点/超时/错误传播/httptest 节点转发）。
  **待续**：三端前端编辑页对接（gateway console 表单 / aicli micro web /
  runtime-server React）、真实 token 联通验证、飞书 WS/Telegram webhook。
- 状态（2026-10-13，审批/提问命令闭环 + 注意力出站基础，FR-7/FR-8）：核心侧
  ◐ **基础完成**——`CommandBackend` 可选能力接口（`DecideApproval`/`AnswerQuestion`；
  宿主未实现时命令回明确提示、不落入普通 turn）+ 命令路由 `/approve [request_id]`、
  `/deny [request_id]`、`/answer <序号|request_id> <内容>`（参数原样透传，request_id
  格式由宿主解释；命令路径不计入 `stats.inbound/outbound`）；`Store.ListBindings`
  反向查询（Memory/storefile/storejson 三实现，只读、输出排序）+ `Service.NotifySession`/
  `NotifyAttention`（session → 全部 IM 绑定扇出、无绑定 `(0,nil)`、单点失败
  `errors.Join` 聚合；去重/降级由调用方/notify 切片负责）；单测覆盖命令路由
  （实现/未实现/参数透传/未处理/错误）、三实现 ListBindings、NotifySession
  多绑定/无绑定/部分失败，`go test -race` 全绿。**待续**：三宿主实现 `CommandBackend`
  （runtime-server `approve_tool`/`answer_question`、aicli actor 响应通道、gateway
  隧道节点命令）并把 approval_requested/question_asked/completion/error 事件接
  `NotifyAttention`；与 Web Push/ntfy 的跨通道去重（notify.Dispatcher）。

### P2 · aicli 本地接入 + QQ（≈3–4 天）

- 交付：aicli `Backend=local_chat`（`localActorRegistry.SubmitPrompt`）+ 微前端页面 + `connectors:` 配置节与热应用；
  QQ 适配器（WS gateway、断线 resume）。
- 验收：本机 aicli 不联网关也能用 Telegram/QQ 对话；配置在 aicli 与 gateway 间可迁移。
- 状态（2026-10-13）：aicli 宿主接线已提前在 P0 落地（`cmd/aicli/commands/connector_host.go`，
  Backend 即 `localActorRegistry.SubmitPrompt`，共享 `connectors:` 配置节）；
  剩余为微前端连接器页与热应用细节（QQ 适配器切片 1 见下条）。
- 状态（2026-10-13，QQ 切片 1）：QQ 适配器 ◇ **gateway 完成**——`internal/connectors/providers/qqbot`
  （App Access Token 缓存/提前刷新、WS 状态机 op 10 hello → op 2 Identify / op 6 Resume →
  op 0 事件分发 → op 1 心跳/op 11 ack、断线指数退避重连；群@/C2C 文本入站与
  频道 @ 防御性解析；被动回复 msg_id + msg_seq 递增；游标 `gateway_session_id`/`gateway_seq`
  经 `PollingState` 持久化，重启后 Resume）。范围：文本收发；测试：`httptest` + gorilla/websocket
  假网关 15 例（令牌缓存/脱敏、握手、群/C2C 解析、seq 持久化、重启 Resume、心跳、断线重连、
  Send 群/C2C/msg_seq/主动降级），`go test -race` 全绿；runtime-server 与 aicli 均已注册。
  **待续**：媒体（上传/下载）与主动消息（无 msg_id 的配额/权限路径）。

### P3 · 微信 + 媒体/主动推送完善（≈4–5 天）

- 交付：微信长轮询适配器 + 扫码登录（actions 流程）+ 媒体转码（ffmpeg 降级）+
  IM 作为注意力通道的可选项（与 Web Push/ntfy 并列，支持每绑定开关/静音）。
- 验收：微信扫码登录 → 收发文本/图片/语音（转码）；手机上收到「待审批/已完成」提醒，可关闭。
- 状态（2026-10-13，微信切片 2b）：微信适配器 ◇ **文本 + 扫码登录完成**——
  `internal/connectors/providers/wechat`：`getupdates` 长轮询（游标 `sync_buf` 经
  `PollingState` 持久化、重启续传、断线指数退避重连、errcode -14 告警等待重登）、
  `sendmessage` 文本收发（markdown 按上游策略清洗、bot_token 脱敏）、
  `sendtyping` typing ticket（getconfig 缓存 24h）、入站文本/引用/语音转写解析，
  图片/文件/视频降级为文本占位；扫码登录 `start_login` → `login_status`
  （wait/scanned/confirmed/expired，二维码过期自动刷新 ≤3 次），confirmed 返回
  `settings`/`secrets` 合并补丁回填 `base_url`/`account_id`/`owner_user_id`/`bot_token`
  （约定见 03 文档 §6）。测试：`httptest` 假 iLink 服务 14 例（激活缓存/脱敏、
  长轮询解析与游标续传、退避重连、Send 清洗、typing、登录态机与刷新、action 错误），
  `go test -race` 全绿；runtime-server 与 aicli 均已注册。
  **待续**：真媒体（CDN 上传/下载/转码，ffmpeg 降级）、errcode -2 失败重放、
  context_token 跨重启持久化（当前进程内记忆，重启后需该会话下次入站才能回复）、
  微信作为注意力通道的开关/静音。

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
  （假 Bot API 重投 + storefile 文件态，`internal/connectors/e2e_restart_test.go`）；aicli 宿主接线已落地
  （`cmd/aicli/commands/connector_host.go`）；真实 token 联通验证待续）
  - [ ] P1：飞书 ↔ gateway → 节点会话端到端；审批/提问命令闭环（核心命令路由与出站助手已就绪，宿主 `CommandBackend` 接线待续）；完成提醒送达且与 Web Push/ntfy 去重。
  （◐ gateway 宿主接线已落地：storejson 存储 + `Backend=gateway_node`（经隧道
  `POST /web/api/invoke`）+ `/api/connectors*`（owner）/`/hooks/connectors/*`
  （Provider 验签）+ console `#connectors` 最小列表页；「飞书 ↔ gateway → 节点会话」
  与审批/提醒闭环的真实联通验证待续）
- [ ] P2：aicli 本地 Telegram/QQ 端到端；配置在三宿主间可迁移；未配置连接器时回归全绿。
- [ ] P3：微信扫码登录 + 文本/图片/语音；IM 通道可开关/静音。
- [ ] 通用性：新增一个通道（假 provider）只需改核心，三端前端零改动。
- [ ] 安全：签名校验、白名单、密钥不回流三项有单测/端到端证据。
- [ ] 可观测：每连接器状态/计数/最近错误/测试消息在两端 UI 可见。
  （◐ 后端闭环已落地：`GET /api/connectors/{id}/logs`（`{entries,stats}`，进程内环默认 200 条、
  limit≤0→50 且上限 200）+ 列表/详情视图 additive `runtime.stats`，契约见 03 §6；三端 UI 展示待续）
