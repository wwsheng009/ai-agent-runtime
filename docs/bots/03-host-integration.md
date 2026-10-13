# 03 · 宿主接入与前端配置设计

> 上游：[02-solution-design.md](./02-solution-design.md) ｜ 本文回答两件事：**三个宿主怎么接**、**前端配置怎么做到不复杂**

## 1. 共性装配模式（三宿主一致）

```text
宿主启动
 → 读取本机连接器配置（宿主各自的存储）
 → connectors.New(Deps{Store, Backend, Log, Metrics}) + Register(providers...)
 → svc.Start(ctx)                 // 启停轮询/WS、恢复游标、重建队列
 → 暴露管理 API（列表/增删改/自测/日志）
 → 视网络形态决定是否暴露 /hooks/connectors/{id}
```

- **只装需要的**：`aicli` 是本机 CLI，优先**长轮询通道**（Telegram / QQ / 微信）；`gateway` 是公网常驻，
  优先**webhook 通道**（飞书）并可作为长轮询的统一前置；`runtime-server` 在服务端部署时两者皆可。
- **共享核心、各自装配**：`internal/connectors` 不 import 任何 `cmd/*`；宿主只写一个 `connector_bootstrap.go`，
  镜像现有 `gateway_bootstrap.go:23-48` 的写法（读配置 → 注入依赖 → 订阅事件 → 注册 handler）。

## 2. `runtime-server` 接入

| 关注点 | 现状锚点 | 接入方式 |
|--------|----------|----------|
| 事件总线 | `NewBusWithRetention(2048)`（`internal/api/runtimeapi/handler.go:165,527`）；订阅样板 `gateway_attention.go:49-119` | 新 `connector_bootstrap.go` 订阅 `assistant_delta`/`assistant_message`/`approval_requested`/`question_asked`/`session_end` 等，驱动出站；**新事件类型须登记** `internal/events/contract.go:62-106` |
| 入站执行 | `POST /api/agent/chat`（`handler.go:1833`）；会话命令 `POST /sessions/{id}/runtime/commands`（`session_runtime_handlers.go:851`，命令 `submit_prompt`/`approve_tool`/`answer_question`） | `Backend` 适配器直接复用这两个端点（同进程内可走 handler 或 HTTP 自调；推荐复用内部服务层，避免自环 HTTP） |
| 会话创建 | `POST /api/sessions`（`handler.go:2999`）；分支/克隆等已有 | auto-binding：首次来信时建会话并存 `Binding.session_id` |
| 管理 API | `RegisterRoutes`（`handler.go:946`）；`/config/document`（GET/PUT `:992-995`）、`/runtime/events`（`:1022`） | 新增 `/api/connectors*` 同风格 handler（Bearer/admin 鉴权沿用现有约定） |
| 前端 | `frontend/src/pages/runtime-config-page.tsx` + i18n `frontend/src/i18n/resources/*/runtime-config/` | 新增 `connectors-page.tsx`（Schema 渲染器）+ 路由 + i18n |
| 流式出站 | SSE 桥 `session_runtime_stream.go:502`、`live_tool_stream.go:350` | 连接器订阅总线做节流合并（默认 800ms），无需复用 SSE 层 |

## 3. `aicli` 接入

| 关注点 | 现状锚点 | 接入方式 |
|--------|----------|----------|
| 入站执行 | `localActorRegistry.SubmitPrompt(ctx, sessionID, prompt, runMeta)`（`cmd/aicli/commands/chat_actor_registry.go:60`）；`ChatSession`/actor 同 TUI 路径 | `Backend` 适配器实现 `local_chat`；审批/提问响应走 actor 的命令通道（与 TUI 同一入口） |
| 会话生命周期事件 | `internal/chat/events.go:5-63`；actor 惰性建 bus（`internal/chat/actor.go:363`） | 订阅同一批事件做「agent 输出 → IM」 |
| 配置系统 | `internal/agentconfig/config.go:31-74`（含 `gateway:` 节）；分层与写路由 `internal/aiclipaths/paths.go:40-86,150-160`、`config_layers.go`/`config_write_route.go`（带跨进程锁） | 新增 `connectors:` 节（与 `gateway:` 并列）；密钥单独放 `~/.aicli/connectors.secrets.yaml`（0600），配置里只留 key 引用 |
| 热应用 | `chat_config_refresh.go` 会话内热刷新；`chat_gateway_config.go:372` 说明"写入即热应用" | 保存连接器配置后调用 `svc.Reload()`（启停增量），无需重启进程 |
| 微前端（零构建） | tab 注册 `cmd/aicli/commands/web/js/menu.js:141-156`；样板 `js/provider-editor.js`、`js/config-admin.js`；后端接口落 `chat_http.go`/`chat_debug_endpoints.go` | 新增 `js/connectors.js`（Schema 渲染器）+ `index.html` tab + `chat_http.go` 的 `/web/api/connectors*` |
| 兜底方案 | `internal/hooks`（http executor，`stop`/`approval_requested` 等事件） | 不做连接器时，可用 hook 把完成/审批 POST 到自建桥（单向），文档标注即可 |

## 4. `gateway` 接入（公网常驻前置）

| 关注点 | 现状锚点 | 接入方式 |
|--------|----------|----------|
| 配置 | `--config` flagfile（键=flag 名，标量；`configfile.go:18-85`）+ `--data-dir` JSON 存储（`devices.json`/`push.json`/`registry.json`） | 连接器配置落 `data-dir/connectors.json`（0600）；flag 仅开总开关（如 `--connectors-enabled`） |
| 路由与鉴权 | `main.go:405-711`；`/api/*`、`/n/` 全为 owner session（`:445-543,701`） | **新增独立命名空间** `POST /hooks/connectors/{id}`（按连接器签名校验，不走 owner session）；管理 API 仍在 `/api/connectors*`（owner） |
| 会话执行 | 注册表 `nodeID|sessionID`（`push.go:102`）＋反代 `/n/{node_id}/`（`main.go:701`） | `Backend` 适配器 `gateway_node`：把 IM 消息转成对目标 node 的会话操作（经隧道转发 `submit_prompt`），输出经 SSE/事件回流后再发 IM |
| 出站提醒 | `pushNotifier` + `notify_policy`（优先级/静音/合并）+ `internal/gateway/ntfy` | 把 IM 当**第三通道**：高优先即时、低优先合并；复用同一 attention 差分，不重复推送 |
| 控制台（micro web） | `//go:embed console` + SPA（`console.go:11-64`）；无通用配置编辑器 | 新增 `#connectors` 视图：列表 + Schema 表单 + 测试按钮 + 日志（复用 `app.js` 现有 toast/表单样式） |

> 语义边界：gateway 不执行 agent，只做**入口 + 路由 + 提醒**；agent 始终跑在 node 上（与本项目现有架构一致）。

## 5. 前端配置：Schema 驱动（"不复杂"的关键）

> 详细的页面线框、扫码/webhook 交互流程、配置项清单（凭据层 + 操作逻辑层）、三处存储与密钥语义、
> 导入导出与验收，见 [07-config-ux.md](./07-config-ux.md)。

### 5.1 用户路径（以 Telegram 为例）

1. 打开「连接器」页 → 点「新增」；
2. 选通道（卡片：飞书 / QQ / 微信 / Telegram）；
3. 表单只出现该通道需要的字段（Telegram：`bot_token`、可选 `mode=webhook|polling`、可选代理）；
4. 保存 → 状态灯变绿 → 点「发送测试消息」→ 手机收到。
   （飞书为 `app_id`+`app_secret`，保存后自动注册 webhook 并回填 `webhook_url`；微信用「扫码登录」按钮。）

### 5.2 渲染器契约

- 核心导出 `GET /api/connectors/schema`（三端同一份）：

```json
{
  "providers": [
    {"provider":"telegram","label":"Telegram","modes":["polling","webhook"],
     "fields":[
       {"key":"bot_token","label":"Bot Token","type":"secret","required":true,"placeholder":"123456:ABC..."},
       {"key":"proxy_url","label":"代理","type":"url","required":false}
     ],"actions":[]},
    {"provider":"qqbot","label":"QQ 机器人","modes":["gateway"],
     "fields":[
       {"key":"app_id","label":"AppID","type":"text","required":true},
       {"key":"app_secret","label":"AppSecret","type":"secret","required":true},
       {"key":"sandbox","label":"沙箱环境","type":"bool","default":"false"},
       {"key":"api_base","label":"API 基址","type":"url","required":false}
     ],"actions":[]},
    {"provider":"wechat","label":"微信","modes":["polling"],
     "fields":[{"key":"account_id","label":"账号","type":"text","required":true}],
      "actions":[{"key":"start_login","label":"扫码登录","kind":"qr"}]},
    {"provider":"feishu","label":"飞书","modes":["webhook"],
     "fields":[
       {"key":"app_id","label":"App ID","type":"text","required":true},
       {"key":"app_secret","label":"App Secret","type":"secret","required":true},
       {"key":"verification_token","label":"Verification Token","type":"secret","required":false},
       {"key":"encrypt_key","label":"Encrypt Key","type":"secret","required":false},
       {"key":"api_base","label":"API 基址","type":"url","required":false}
     ],"actions":[]}
  ]
}
```

- 前端**只实现一次渲染器**（三份宿主代码各一份，逻辑相同）：
  `字段 → 控件`（text/secret/bool/enum/url）、`必填校验`、`secret 占位（已配置）`、`action 按钮`。
- 新增通道：核心实现 provider + Schema；三端页面 **零改动**（列表由 schema 驱动）。
- 复杂流程（微信扫码）统一走 `actions`：前端点按钮 → `POST /api/connectors/{id}/actions/start_login` →
  返回二维码图/链接 → 前端轮询 `.../actions/login_status`。

### 5.3 三端 UI 对照

| 宿主 | 技术栈 | 页面 | 备注 |
|------|--------|------|------|
| gateway console | 原生 JS + CSS（`cmd/gateway/console/`） | `app.js` 新视图 `#connectors` | 复用现有 `.btn/.panel/.toast` 样式；无构建 |
| aicli micro web | 原生 JS（`cmd/aicli/commands/web/js/`） | 新 `connectors.js` + tab | 样板：`provider-editor.js`、`config-admin.js` |
| runtime-server | React/Vite（`frontend/`） | `pages/connectors-page.tsx` + i18n | 与 `runtime-config-page.tsx` 同级 |

> 三端共用同一份 Schema：当前已注册 `telegram`（polling）、`qqbot`（gateway，字段
> `app_id`/`app_secret`/`sandbox`/`api_base`）、`wechat`（polling + 扫码登录 action）与
> `feishu`（webhook，见 §6）四个通道；页面由 `GET /connectors/schema` 驱动，新增通道三端零改动。

> **实现状态（2026-10-13）**：runtime-server 端最小 React 配置页已落地，路由 `/runtime/connectors`
> （懒加载，入口在 `runtime-config-page.tsx` 导航）。构成：`pages/connectors-page.tsx`（列表 +
> 启停/编辑/测试消息/重载/删除 + 新建）、`pages/connectors/{connector-dialog,test-message-dialog}.tsx`、
> `pages/connectors/shared.ts`（纯逻辑：schema 取字段/表单构建/提交载荷/客户端预校验）、
> `api/runtime/connectors.ts` + `types/runtime/connectors.ts`（契约客户端与类型，错误按字段级
> `errors` 归一）。表单完全由 `GET /connectors/schema` 驱动（text/secret/bool/enum/url → 控件映射，
> 新增通道零改动）；密钥语义 = 留空保持、显式清除 `__clear__`；i18n 走 `connectors` 命名空间
> （zh-CN/en-US 成对）。gateway console / aicli micro web 两个原生 JS 页面按 P1 待续。

## 6. API 契约（三宿主统一语义）

| 方法 | 路径 | 说明 | 鉴权 |
|------|------|------|------|
| GET | `/api/connectors/schema` | 全部 provider 的字段/动作/模式 | 管理面鉴权 |
| GET | `/api/connectors` | 连接器列表（`secrets` 脱敏为 `secret_keys`） | 管理面 |
| POST | `/api/connectors` | 新建（provider + settings + secrets） | 管理面 |
| PATCH | `/api/connectors/{id}` | 更新（secrets 为空表示不改） | 管理面 |
| DELETE | `/api/connectors/{id}` | 删除（同时停轮询/WS） | 管理面 |
| POST | `/api/connectors/{id}/test` | 发送测试消息 | 管理面 |
| GET | `/api/connectors/{id}/logs` | 最近日志（分页） | 管理面 |
| GET/PUT | `/api/connectors/{id}/bindings` | 查看/调整 IM↔会话绑定 | 管理面 |
| POST | `/api/connectors/{id}/actions/{action}` | 特殊动作（微信扫码等）；执行面已实现（`connectors.ActionProvider`） | 管理面 |
| POST | `/hooks/connectors/{id}` | **入站 webhook**（通道签名校验） | 每连接器密钥 |

> 管理面鉴权按宿主既有约定：gateway=owner session；runtime-server=admin/Bearer；aicli=本机会话。

**实现状态（2026-10-13，P0 第二切片）**：上表子集已落地为宿主机无关的
`backend/internal/connectors/restapi`（gorilla/mux，与 runtimeapi 同风格）：

| 已实现路由 | 说明 |
|------------|------|
| `GET /connectors`、`GET /connectors/{id}` | 列表/详情；`secrets` 脱敏为 `secret_keys`（只回字段名） |
| `GET /connectors/schema` | 三端渲染器输入（`api.Schema{version, providers}`） |
| `POST /connectors` | 新建；`api.Validate` 必填/enum 校验，失败返回字段级错误 |
| `PATCH`/`PUT /connectors/{id}` | 部分更新；密钥留空=保持、`__clear__`=清除（`api.MergeSubmit`）；provider 不可变更 |
| `DELETE /connectors/{id}` | 删除并停止轮询 |
| `POST /connectors/{id}/reload` | 应用配置变更（重启轮询/重连），更新接口内已自动调用 |
| `POST /connectors/{id}/test` | 发送测试消息（控制台按钮），body `{chat,text}` |
| `POST /connectors/{id}/actions/{action}` | 通道特有动作执行面（`connectors.ActionProvider.RunAction`）；body 可选 JSON object；未知/停用 404、未实现能力 405、provider 失败 400 |
| `POST /hooks/connectors/{id}` | 公网入站；能力探测 `connectors.WebhookProvider`，签名失败 400、队列满 429、未知/停用 404 |

- 挂载：`NewRouter` → `/api`；`NewHooksRouter` → 根（`/hooks/connectors/{id}`）。
  `Authorize` 为 nil 时管理面整体 403（安全默认，避免宿主忘接线）。
- 待续（P1/P2）：`/{id}/logs`、`/{id}/bindings`；三端前端的 action 按钮控件（微信扫码登录等，Schema.Actions 驱动）仍待续。
- **安全与生命周期（2026-10-13 审查修复）**：管理面/入站不再把请求 ctx 当生命周期
  ——`Service` 持有宿主基 ctx（`Start` 建立/`Stop` 取消），轮询与异步任务基于它派生，
  REST handler 返回（`r.Context()` 取消）不会让新建/Reload 的连接器静默停摆；
  去重键 = `connector+chat+messageID`（telegram `message_id` 仅 chat 内唯一，
  多会话同 ID 不再互斥丢弃）；`Stop` 后队列拒绝新入队，`enqueue` 与 `closeAll`
  无 send-on-closed-channel 竞态；轮询实例按代际登记，旧实例退出不误删新实例；
  队列满（429）不再因「先认领后入队」被账本吞掉；Telegram 错误文本脱敏
  `bot_token`（`client.Do` 的 URL 内嵌）；Schema 只声明已实现的能力
  （Telegram 暂仅 `polling`，`HandleWebhook` 实现后再加回 `webhook`）。

## 7. 配置存储与迁移

| 宿主 | 配置文件 | 密钥文件 | 迁移性 |
|------|----------|----------|--------|
| gateway | `<data-dir>/connectors.json`（gateway 不读 agentconfig） | 同文件内 `secrets`（0600） | 可整文件复制到另一实例 |
| **aicli + runtime-server（共用同一份）** | 同一个 **agent 配置 `config.yaml`** 的 `connectors:` 节；两端共用 `agentconfig.ConfigLayerStack()` 的分层与优先级（runtime-server 经 `/api/runtime/config/document` 读写，aicli 直接读写，原子写 + 跨进程锁） | `~/.aicli/connectors.secrets.yaml`（0600） | 写一次两端可见；另有独立 `runtime.yaml` 栈保留原样 |

> 三处结构一致（同 schema、同 key 名），因此"把 aicli 的 Telegram 配置搬到 gateway"只需复制字段值。
> **配置共享 ≠ 运行共享**：同一轮询/WS 通道只能一个宿主实际运行，靠 `runtime_owner` + 启动抢占（见
> [07-config-ux.md](./07-config-ux.md) §5.6）。

**P0 实现状态（2026-10-13）**：aicli/runtime-server 共用实现已落地为
`backend/internal/connectors/storefile`——配置写共享 `config.yaml` 的 `connectors:`
节（yaml.v3 Node 往返保留注释/其它键，原子写），密钥单独写同目录
`connectors.secrets.yaml`（0600，可 `WithSecretsPath` 覆盖），绑定/去重账本/轮询游标
写宿主数据目录 `data/connectors/state.json`。runtime-server 已在
`cmd/runtime-server/connector_bootstrap.go` 接线（管理面挂 `/api/connectors*`、
webhook 挂 `/hooks/connectors/{id}`）。Telegram Provider 提供 `api_base` 配置项
（默认 `https://api.telegram.org`，可指向自建 Bot API 服务器/测试替身），
`internal/connectors/e2e_restart_test.go` 以 httptest 假 Bot API + storefile 文件态
覆盖「重启续传不重复」（游标续传 + 去重账本拦截重投，`go test -race` 全绿）。
QQ 机器人 Provider（`internal/connectors/providers/qqbot`，P2 切片 1）已在
runtime-server 与 aicli 两端注册：`modes=["gateway"]`，字段
`app_id`/`app_secret`(secret)/`sandbox`/`api_base`；WS Identify/Resume/心跳与
指数退避重连、群@/C2C 文本入站、被动回复 `msg_id`+`msg_seq` 已实现，游标
`gateway_session_id`/`gateway_seq` 与 Telegram `update_offset` 共用
`state.json` 的 cursors 视图（重启后 Resume）；媒体与主动消息待续。
微信 Provider（`internal/connectors/providers/wechat`，P3 切片 2b：文本收发 +
扫码登录 + typing）已在 runtime-server 与 aicli 两端注册：`modes=["polling"]`，
字段 `bot_token`(secret)/`base_url`/`api_base`/`account_id`/`owner_user_id`/
`route_tag`/`mode`；`getupdates` 长轮询（游标 `sync_buf` 持久化 + 指数退避重连）、
`sendmessage` 文本与 markdown 清洗、`sendtyping` typing ticket、入站
文本/引用/语音转写解析（图片/文件/视频降级为文本占位）已实现；真实媒体待续。
飞书 Provider（`internal/connectors/providers/feishu`，P1 切片 3：**webhook 文本收发**）
已在 runtime-server 与 aicli 两端注册：`modes=["webhook"]`（WS 长连接未实现前不声明），
字段 `app_id`(text,required)/`app_secret`(secret,required)/`verification_token`(secret)/
`encrypt_key`(secret)/`api_base`(url，默认 `https://open.feishu.cn`)，无 actions；
`tenant_access_token` 按 connectorID 缓存并提前 5 分钟刷新（Activate 调用 token 接口
验凭据，bot info 轻量探测失败仅告警；错误文本脱敏 `app_secret`/token）。
**签名/解密口径与 webhook 入口约定**：

- 入口为宿主公网路由 `POST /hooks/connectors/{id}`（`restapi/webhooks.go`）：
  未知/停用 404、队列满 429、Provider 返回 error → 400、`WebhookResult.Response`
  非空时按 `Status`（默认 200）直应答、解析出 Message → 202 并异步投递。
- `url_verification` challenge：配置了 `verification_token` 时校验 `token`（常量时间
  比较，不匹配 400），随后直应答 `{"challenge":"..."}`；**未配置时兼容放行并告警**
  （无法验签，建议控制台开启事件订阅时至少保留其一凭据）。
- 事件 v2：校验 `header.token`（同 verification_token）；配置 `encrypt_key` 时按
  飞书口径 AES-256-CBC 解密（密钥 `sha256(encrypt_key)`，载荷
  `base64(iv[16]||ciphertext)`、PKCS#7），解密/验签失败一律 400；收到密文但未配置
  `encrypt_key` 也返回 400。
- 仅处理 `im.message.receive_v1`：`Chat=chat_id`、`UserID=sender open_id`、
  **幂等键 `MessageID=header.event_id`**（平台重试同 id，交宿主去重账本）、文本取
  `message.content` 的 `text`（剔除 @占位符、HTML 反转义）；非文本降级为「[图片]/[语音]/
  [文件]/[视频]」等文本占位并附带 `image_key/file_key` 元数据；其它事件类型静默 202。
- 回复优先 `POST /open-apis/im/v1/messages/{message_id}/reply`（quote 原消息，msg_id
  按 connector+chat 进程内记忆，失败按上游口径降级 chat 直发），否则
  `POST /open-apis/im/v1/messages?receive_id_type=chat_id`；`msg_type=text`、
  `content=json({"text":...})`，错误脱敏。
**待续（P1 后续切片）**：WS 长连接与重连、卡片式流式回复（StreamingProvider）、
typing 表情、媒体上传/下载、审批事件路由、msg_id 记忆跨重启持久化。
**扫码登录 action 与返回约定（三端统一）**：

- `POST /api/connectors/{id}/actions/start_login`（Schema.Actions kind=qr）：
  body 可选 `{"base_url":"https://…"}`（未配置 `base_url`/`api_base` 时必需）；
  返回 `{login_id,status:"wait",qr_code_content,expires_at,…}`，其中
  `qr_code_content` 是二维码图片（data URI/base64）或链接，前端直接渲染。
- `POST /api/connectors/{id}/actions/login_status`（kind=button）：body 可选
  `{"login_id":"…"}`（缺省取该连接器最近一次会话）；`status` 取
  `wait|scanned|confirmed|expired`，二维码过期自动刷新（≤3 次，返回
  `refresh_count` 与新 `qr_code_content`）。
- **回填约定**：`status=confirmed` 时响应额外携带 `settings`/`secrets` 两个合并
  补丁（`{"settings":{"base_url","account_id","owner_user_id"},
  "secrets":{"bot_token"}}`）。前端（gateway console / aicli micro web /
  runtime-server React）应把这两个对象提交给现有
  `PATCH/PUT /api/connectors/{id}`（secrets 留空=保持、`__clear__`=清除）完成回填；
  回填前凭据已在 Provider 进程内缓存 24h，轮询/收发即时可用；回填后重启仍有效。
**aicli 宿主接线（2026-10-13 落地）**：`cmd/aicli/commands/connector_host.go`
（`StartLocalConnectors` + `MountLocalConnectorRoutes`）——

- 装配点：chat 能力挂载（`chat_setup.go`，交互式会话才启动；一次性/JSON 运行
  不装配，避免平台侧反复上线抖动），随 chat cleanup 停机；
- Backend：`localActorRegistry.SubmitPrompt`（IM 会话无绑定时按
  `conn-<connector>-<chat>` 派生确定性 session id，`ensureSession` 自动建会话），
  P0 只回终态，流式增量 P1 接 actor 事件；
- 管理面：`/api/connectors*` 与 `/hooks/connectors/*` 挂在 aicli loopback
  Web 服务器同一 mux（`cmd/aicli/pprof.go`），鉴权沿用 `ChatWebAuthGuard`
  （回环/同源/写令牌）；服务未启动时 503；
- 共享同一份 `config.yaml` 的 `connectors:` 节 + `data/connectors/state.json`，
  与 runtime-server 配置互通（运行归属抢占见 §5.6，P1）。

**安全闸与语义统一（2026-10-13 审查修复）**：aicli 对「启用但 `allowed_users`
为空」的连接器 **fail-closed**（跳过启动并告警，`connectors.Deps.RequireAllowedUsers`，
需在策略里配置白名单或显式关闸）；runtime-server 保持兼容（仅告警）；两端 IM
轮次统一显式 `bypass_permissions`（IM 入站无交互审批通道，白名单先行过滤）；
`state.json` 目录改为优先随 `configPath` 推导（与 aicli 同源，项目层/`--config`
下两端账本共享）；`runtimeapi` 的环回判定不再无条件信任 `X-Forwarded-For`
（仅直连对端本身是环回时才采纳转发头，远程伪造 `127.0.0.1` 不再绕过管理面鉴权）。

**待续**：跨进程锁与 `runtime_owner` 抢占、aicli micro web 连接器页（P1）、
真实 token 联通验证。

## 8. 宿主差异汇总（共享核心 vs 宿主专属）

| 关注点 | 共享核心（`internal/connectors`） | 宿主专属 |
|--------|-----------------------------------|----------|
| 通道协议/签名/媒体/重连 | ✅ 全部 | — |
| 入站归一/去重/队列/节流/重试 | ✅ 全部 | — |
| 命令解析（通用命令） | ✅ 全部 | 命令的**执行**（审批/提问/建会话） |
| 会话绑定路由 | ✅ 策略与存储接口 | 会话的创建与查询实现 |
| Agent 执行 | ❌ | ✅ `Backend` 适配（runtime-server / aicli / gateway_node） |
| 配置存储与 UI | ✅ Schema 与模型 | ✅ 各自的存储/页面/鉴权 |
| 出站提醒（attention） | ❌ | ✅ 复用各宿主已有事件源（gateway 直接复用 pushNotifier） |
