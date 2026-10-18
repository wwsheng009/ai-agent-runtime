# 01 · 需求分析：IM 通道接入

> 上游：[README.md](./README.md) ｜ 参考实现：`E:\projects\ai\codex-server\backend\internal\bots` ｜ 相关：[08-notifications](../gateway/08-notifications.md)、[05-wan-and-mobile](../gateway/05-wan-and-mobile.md)

## 1. 背景与目标

`codex-server` 已实现飞书 / QQ / 微信 / Telegram 四类 bot 接入：收消息、绑定会话、跑 agent、
回复到 IM，并覆盖流式编辑、typing、媒体、审批命令、长轮询断线重连等工程细节。本项目的三个宿主
（`gateway`、`aicli`、`runtime-server`）目前只有 Web UI 与会话能力，**没有 IM 入口**。

目标：**一套共享的「连接器」核心 + 三家适配器（+ 后续扩展），在三个宿主中都能装配使用**，
并把配置成本压到「填几个字段即可」。

### 1.1 与本项目现状的差距（事实）

| 能力 | codex-server | 本项目（现状） |
|------|--------------|----------------|
| 通道适配抽象 | `Provider` + 可选能力接口（streaming/typing/polling） | 无（只有 ntfy/push 出站通道） |
| 入站 webhook | `POST /hooks/bots/{connectionId}`（`internal/api/router.go:182`） | 无（现有 `/api/*`、`/n/` 全部 owner session） |
| 会话绑定 | Bot / Connection / Conversation / Binding / Trigger / DeliveryTarget 多层 | 无 |
| Agent 执行入口 | `threads/turns`（自有模型） | runtime-server：`POST /api/agent/chat`（`handler.go:1833`）、`/sessions/{id}/runtime/commands`（`session_runtime_handlers.go:851`）；aicli：`localActorRegistry.SubmitPrompt`（`chat_actor_registry.go:60`） |
| 事件（出站） | `turn/completed`、`notification/created`、`bot/message/*` | `internal/events` 总线（`bus.go:11-19`）：`assistant_delta`/`assistant_message`/`approval_requested`/`question_asked`/`session_end` 等 |
| 出站提醒 | 独立通知中心 | 已有 `Web Push + ntfy`、attention 投影与合并/静音策略（`cmd/gateway/push.go`、`notify_policy.go`） |

## 2. 角色与场景

| 角色 | 说明 |
|------|------|
| Owner | 部署者/唯一管理员，配置连接器、授权哪些 IM 用户可用 |
| IM 用户 | 在飞书/QQ/微信/Telegram 里与 bot 对话的人（默认仅 owner 自己） |
| 宿主 | `runtime-server`（常开服务）、`aicli`（本机 CLI/TUI，进程内）、`gateway`（公网常驻入口） |

**用户故事**

- S1 我在 Telegram 里给 bot 发一句话，它在本机 `runtime-server` 上开一个会话跑 agent，并把结果回给我。
- S2 我在飞书里 @bot 提问，长回答以「流式编辑」方式逐步可见；处理慢时显示 typing/表情。
- S3 agent 需要审批/提问时，我在 IM 里直接回复 `/approve`、`/deny` 或选项编号即可继续。
- S4 我在同一个 IM 会话里 `/new`、`/threads`、`/rename` 切换/管理多个 agent 会话。
- S5 我在手机 IM 里收到「某会话已完成/待审批」的提醒（复用现有注意力事件）。
- S6 我给 bot 发图片/文件，agent 能拿到并处理（按通道能力降级）。
- S7 我在 gateway 控制台/前端/微前端里点几下就完成通道配置，不需要改配置文件。
- S8 三个宿主的行为一致：同一份通道配置，换个宿主也能跑（凭据可从文件迁移）。

## 3. 功能需求（FR）

| 编号 | 需求 | 要点 |
|------|------|------|
| FR-1 | 连接器管理 | 增删改查「通道实例」：provider、名称、凭据、开关；支持启用/停用/重启 |
| FR-2 | 四通道接入 | 飞书（webhook + 长连接 WS）、QQ（WS gateway）、微信（官方长轮询 + 扫码登录）、Telegram（webhook + 长轮询） |
| FR-3 | 入站消息归一 | 统一 `InboundMessage`（文本/媒体/来源/会话键），去重（消息 ID 幂等账本） |
| FR-4 | 会话绑定 | IM 会话 ↔ 本机 agent 会话；默认「自动建会话」，可指定已有 session / 节点 |
| FR-5 | 出站回复 | 文本 + 媒体；按通道能力选择 流式编辑 / 追加 / 一次性；失败重试与降级 |
| FR-6 | 命令 | 通用：`/new`、`/threads`、`/rename`、`/archive`、`/help`；审批：`/approve`、`/deny`、`/answer <n>`；控制：`/stop`（停止当前回合，与 TUI Esc 同语义） |
| FR-7 | 审批与提问往返 | 订阅 `approval_requested` / `question_asked` → 发 IM；IM 回复关联 request_id 回注宿主 |
| FR-8 | 注意力事件出站 | 复用 attention 语义，把 completion/approval/question/error 推给 IM（第三条通道，与 Web Push/ntfy 并列） |
| FR-9 | 媒体 | 入站下载/转存与出站上传；语音/视频转码、markdown 清洗按通道降级 |
| FR-10 | 多宿主一致 | 同一 `Provider` 实现可在三宿主运行；宿主差异只在 Backend 适配与配置存储 |
| FR-11 | 前端配置 | schema 驱动表单；密钥「写入不读出」；二维码登录（微信）等特殊控件可选 |
| FR-12 | 诊断 | 每连接器：状态、最近错误、收/发计数、消息往返日志、一键「发送测试消息」、webhook 可达性检查 |

## 4. 非功能需求（NFR）

| 维度 | 要求 |
|------|------|
| 通用性 | 核心不依赖任何宿主概念；宿主差异通过 `Backend` 接口注入（编译期同一模块，运行期装配） |
| 配置复杂度 | 首次接入 ≤ 3 个字段（如 Telegram：token；飞书：app_id+app_secret）+ 一次授权；**不引入 workspace/bot/binding/trigger 多层模型** |
| 前端一致 | 三端共用同一份字段 Schema；新增通道只改核心，不改前端 |
| 可靠性 | 长轮询/WS 断线重连与退避；入站去重（至少一次投递语义）；出站幂等键 + 有界重试；进程重启后恢复轮询游标 |
| 性能 | 事件总线**无背压**（`bus.go:314` 同步回调）→ 连接器自带**有界队列**与节流（流式 delta 合并） |
| 安全 | webhook 校验（Telegram secret token、飞书 challenge、QQ/微信签名）；用户白名单；密钥本地 0600 存储；API 不返回明文 |
| 可观测 | 结构化日志 + 指标（连接器状态、入/出站计数、失败率、队列深度）；控制台/前端可见 |
| 兼容 | 不改变现有 `/api/*`、`/n/`、通知与情绪语义；新增 webhook 命名空间独立 |
| 可降级 | 未配置连接器时行为与现在完全一致；通道不可用时只影响该通道 |

## 5. 约束与不做（NG）

- 不把 codex-server 的 `store.MemoryStore`、`threads/turns`、NotificationCenter 一并搬过来——只搬**能力与模式**。
- 不做「多租户 SaaS / workspace 级共享 bot」；首期单 owner、可选用户白名单（对齐本项目安全模型）。
- 不实现各 IM 的「全量能力」（如飞书卡片交互、微信支付类能力）；只做对话 + 媒体 + 审批。
- 不引入重型依赖：媒体转码按可用性检测（ffmpeg 存在才启用），否则降级为提示。

## 6. 验收清单（摘要，详见 04 文档）

- [ ] 四个通道中至少 **Telegram / 飞书** 完成端到端：IM 发消息 → 本机 agent 执行 → 回复回 IM。
- [ ] 审批/提问能在 IM 内闭环（收到卡片/文本 → 回复命令 → agent 继续），本机 Web UI 同步可见。
- [ ] 完成/待审批提醒能经 IM 送达（与 Web Push/ntfy 同一事件源，不重复推送）。
- [ ] 同一通道配置可在 `runtime-server` 与 `aicli` 间迁移（凭据文件），行为一致。
- [ ] gateway 控制台/前端/微前端均可不改代码新增通道表单（Schema 驱动）。
- [ ] 未配置任何连接器时，三宿主行为与现状无差异（回归测试）。
- [ ] 长轮询/WS 断网重连、进程重启后消息不丢（去重账本 + 游标持久化）。
