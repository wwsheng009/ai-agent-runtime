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
    {"provider":"wechat","label":"微信","modes":["polling"],
     "fields":[{"key":"account_id","label":"账号","type":"text","required":true}],
     "actions":[{"key":"start_login","label":"扫码登录","kind":"qr"}]}
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
| POST | `/api/connectors/{id}/actions/{action}` | 特殊动作（微信扫码等） | 管理面 |
| POST | `/hooks/connectors/{id}` | **入站 webhook**（通道签名校验） | 每连接器密钥 |

> 管理面鉴权按宿主既有约定：gateway=owner session；runtime-server=admin/Bearer；aicli=本机会话。

## 7. 配置存储与迁移

| 宿主 | 配置文件 | 密钥文件 | 迁移性 |
|------|----------|----------|--------|
| gateway | `<data-dir>/connectors.json` | 同文件内 `secrets`（0600） | 可整文件复制到另一实例 |
| aicli | `~/.aicli/config.yaml` 的 `connectors:` 节（分层合并 + 写锁） | `~/.aicli/connectors.secrets.yaml`（0600） | 与现有 config 分层/热刷新一致 |
| runtime-server | 现有 config document（`/api/runtime/config/document`）扩展 `connectors` 节 | 同目录 `connectors.secrets.yaml`（0600） | 与 runtime 配置一起备份 |

> 三处结构一致（同 schema、同 key 名），因此"把 aicli 的 Telegram 配置搬到 gateway"只需复制字段值。

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
