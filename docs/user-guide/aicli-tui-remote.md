# aicli TUI 远程操作手册（loopback HTTP）

> 对应程序：`backend/cmd/aicli`（`aicli` / `aicli.exe`）
> 作用：在**本机**对**正在运行的** aicli TUI 会话做脚本化远程操作——注入 prompt、
> 等待/流式取回复、回答审批与提问、中断、切换会话、抓取"用户当前看到的屏幕"。
> 前提：TUI 以 `--pprof`（或 `--debug` / `--web-port` / `AICLI_PPROF`）启动，loopback HTTP 服务器已开启。

---

## 目录

1. [概述](#1-概述)
2. [启动与入口发现](#2-启动与入口发现)
3. [鉴权与写令牌](#3-鉴权与写令牌)
4. [端点速查](#4-端点速查)
5. [常用操作](#5-常用操作)
6. [错误码与排查](#6-错误码与排查)
7. [安全边界与限制](#7-安全边界与限制)
8. [相关文档](#8-相关文档)

---

## 1. 概述

`aicli chat` 的 TUI 是人机交互界面；当它以 `--pprof` 启动时，会在 `127.0.0.1`
的随机空闲端口上额外开启一个 **loopback HTTP 服务器**，把同一个会话暴露成一组
`/web/api/*` 接口，于是**脚本、外部 Agent、CI 任务、另一个终端**都能远程操作它：

| 想做的事 | 用什么 |
|----------|--------|
| 发一句话并拿到回复（同步、一次请求） | `POST /web/api/invoke` |
| 长任务边跑边看输出 | `POST /web/api/invoke` + `Accept: text/event-stream`（SSE） |
| 只投递不等结果 | `POST /web/api/input` |
| 回答工具审批 / AskUser 提问 / 中断当前 turn | `POST /web/api/input`（`type=approval\|question_answer\|interrupt`） |
| 查 turn 终态、耗时、本轮 token 用量 | `GET /web/api/turn` |
| 看"用户当前看到的 TUI 画面" / 完整对话 | `GET /web/api/screen`（`?view=tui` / 默认 transcript） |
| 实时订阅 turn / 工具 / 审批事件 | `GET /web/api/events`（SSE） |
| 列出 / 新建 / 恢复 / 重命名 / 删除会话 | `GET|POST /web/api/sessions[...]` |
| 用浏览器点着操作 | `GET /web/`（内置微型 Web 客户端） |

不适合做的事：**跨机器**调用、多用户鉴权、公网暴露。这套接口只监听回环地址，
鉴权模型是"本机同源 + 写令牌"（见 [第 7 节](#7-安全边界与限制)）。

---

## 2. 启动与入口发现

### 2.1 开启方式与固定端口

```powershell
# 1) 显式开启（监听 127.0.0.1 的随机空闲端口）
aicli chat --pprof

# 2) 用 --debug 启动 chat 时也会自动开启（内置渲染状态端点）
aicli chat --debug

# 3) 用 --web-port 固定端口（推荐；等价 AICLI_PPROF=127.0.0.1:<port> 且优先级更高）
aicli chat --web-port 64562

# 4) 用环境变量固定地址（可带自定义 host，便于书签/脚本复用）
$env:AICLI_PPROF = '127.0.0.1:64562'
aicli chat
```

`--pprof` 是根命令的持久 flag，`aicli chat --pprof` 与 `aicli --pprof chat` 等价；
`--web-port` 是它的端口版：只接受 1-65535，固定绑定 `127.0.0.1`，越界或占用会直接报错退出
（不会静默退化成随机端口）；优先级 **`--web-port` > `AICLI_PPROF` > 随机空闲端口**。
固定后端口不再变化，脚本可以硬编码 `http://127.0.0.1:64562`。

### 2.2 固定写令牌（可选）

默认每进程随机生成写令牌、重启即轮换；若希望**重启后令牌不变**（CI、服务化管理、
外部 Agent 长期集成），可在启动时显式指定（优先级：`--web-token` > `AICLI_WEB_TOKEN` > 随机）：

```powershell
# 1) 命令行指定
aicli chat --pprof --web-token 0123456789abcdef0123456789abcdef

# 2) 环境变量（推荐给脚本/容器：不出现在进程命令行里）
$env:AICLI_WEB_TOKEN = '0123456789abcdef0123456789abcdef'
aicli chat --pprof
```

| 项 | 说明 |
|----|------|
| 字符集 | `A-Z a-z 0-9 - . _ ~`（URL 安全；请求头与 `?token=` 都无需转义） |
| 长度 | 16 ~ 256 字节，低于 16 字节直接拒绝 |
| 校验失败 | **中止启动**（exit 1，stderr 打印原因，不回退到环境变量/随机值），避免"传了 token 却仍 403"的静默状态 |
| 生效范围 | 进程级：`GET /web/api/token`、页面 meta（关于页）、`/debug display` 都显示该值 |
| 运行中替换 | 不支持；要换令牌请重启进程 |
| 启动行 | 标注来源，如 `(POST /web/api/* 必需; 来自 --web-token（固定令牌，重启不轮换，注意保管）)` |

> 固定令牌 = **凭证长期有效**：不要用弱口令，不要写进仓库或 CI 明文日志；
> 共享机器上优先用环境变量而不是 `--web-token`（后者会出现在进程命令行里）。
> 生成一个够强的值：`python -c "import secrets; print(secrets.token_hex(16))"`。

### 2.3 启动行（stderr）

服务器就绪后终端会打印（节选，实际端口以输出为准）：

```text
Info: pprof endpoint enabled: http://127.0.0.1:64562/debug/pprof/
Info: chat render status endpoint: http://127.0.0.1:64562/debug/chat/status (JSON; ?format=text for plain text)
Info: chat screen content endpoint: http://127.0.0.1:64562/debug/chat/screen (JSON; ?format=text for plain text)
Info: chat debug endpoints list: http://127.0.0.1:64562/debug/endpoints (JSON; ?format=text for plain text)
Info: chat web client / remote invoke endpoint: http://127.0.0.1:64562/web/ (POST http://127.0.0.1:64562/web/api/invoke)
Info: web write token (X-AICLI-Token): 3f9c8a1b2c3d4e5f60718293a4b5c6d7 (POST /web/api/* 必需)
Info: runtime observe plane: http://127.0.0.1:64562/api/runtime/observe/v1 (local in-process; ...)
```

### 2.4 TUI 内查看（`/debug display`）

在 TUI 里输入 `/debug display`，其中「HTTP 调试端点:」区块会按
`loopback` / `web` / `runtime-observe` 分组列出当前环境**可用**的端点，
`web` 分组下额外打印 `Auth:` 与 `Token:` 两行（令牌可直接复制）：

```text
HTTP 调试端点: (GET /debug/endpoints)
web  (微型 Web 客户端 / 远程调用 API)
  Base: http://127.0.0.1:64562/web
  Auth: POST 请求需携带 X-AICLI-Token（或 ?token=）；令牌可由 GET /web/api/token 读取（或见 aicli 启动行 web write token），内置页面自动注入
  Token: 3f9c8a1b2c3d4e5f60718293a4b5c6d7  (GET /web/api/token)
  GET http://127.0.0.1:64562/web/  [enabled]  微型 Web 客户端页面（浏览器交互入口）
  POST .../web/api/invoke          [enabled]  同步远程调用（wait_only/timeout_ms/client_request_id）
  ...
```

> TUI 未开启 loopback 服务器时该区块显示 `Status: 未启用` 与开启提示
> （`--pprof` / `--debug`，或 `--web-port <端口>` / `AICLI_PPROF=127.0.0.1:<端口>` 固定地址）。

### 2.5 机器可读的入口清单

```powershell
# 文本（人读）
curl.exe 'http://127.0.0.1:64562/debug/endpoints?format=text'
# JSON（脚本发现全部端点；新增端点无需改脚本）
curl.exe -s 'http://127.0.0.1:64562/debug/endpoints?format=json'
```

清单包含三组：`web`（本手册的 `/web/api/*`）、`loopback`（pprof 与 `/debug/*`）、
`runtime-observe`（运行时观察平面 `GET /api/runtime/observe/v1/*`）。

---

## 3. 鉴权与写令牌

三层校验（按顺序）：

| 层 | 规则 | 失败表现 |
|----|------|----------|
| Host | 请求 Host 必须是回环地址（`127.0.0.1` / `localhost` / `[::1]`） | `403` `{"status":"forbidden","reason":"non-loopback Host header rejected"}` |
| Origin | 带 `Origin` 的请求必须与 Host 同源（挡浏览器跨站写请求） | `403` `cross-origin request rejected` |
| 写令牌 | **所有非 GET/HEAD/OPTIONS 请求**必须携带令牌 | `403` `missing or invalid X-AICLI-Token` |

只读 GET（含 SSE）不需要令牌，但仍受 Host/Origin 校验。

### 3.1 取令牌的几种方式

```powershell
# 方式 1（脚本推荐）：读专用端点，JSON 返回 header/token/query_param/source/hint
#   source=random 表示重启会轮换；=--web-token/AICLI_WEB_TOKEN 表示固定不变
$token = (curl.exe -s 'http://127.0.0.1:64562/web/api/token' | ConvertFrom-Json).token

# 方式 2：从启动行复制（Info: web write token (X-AICLI-Token): <token>）
# 方式 3：TUI 里 /debug display 的 Token: 行，或浏览器打开 /web/ → 「关于」页签点「复制」
# 方式 4：启动时自己指定（--web-token / AICLI_WEB_TOKEN，见 2.2）——值已知，无需再读取
```

```bash
# bash 版本
export AICLI_WEB_TOKEN=$(curl -s http://127.0.0.1:64562/web/api/token | jq -r .token)
```

### 3.2 两种携带方式

```powershell
# 请求头（推荐；不会进访问日志/命令行历史）
-H "X-AICLI-Token: $token"
# 查询参数（给无法自定义请求头的工具用）
'http://127.0.0.1:64562/web/api/invoke?token=...'
```

令牌每进程随机生成（32 位 hex）、**重启即轮换**、不落盘；`/debug/endpoints` 的
JSON/text 输出**不含**令牌原文（避免清单被转发时泄露）。

### 3.3 可复用的 PowerShell 助手

```powershell
# 推荐：Invoke-RestMethod（.NET 按 Content-Type charset 解码，Windows 中文零乱码）
function Get-AicliToken([string]$Base = 'http://127.0.0.1:64562') {
  (Invoke-RestMethod -Uri "$Base/web/api/token" -TimeoutSec 15).token
}

function Invoke-AicliPrompt {
  param(
    [Parameter(Mandatory)][string]$Prompt,
    [string]$Base = 'http://127.0.0.1:64562',
    [int]$TimeoutMs = 120000,
    [string]$ClientRequestId
  )
  $body = @{ prompt = $Prompt; timeout_ms = $TimeoutMs }
  if ($ClientRequestId) { $body.client_request_id = $ClientRequestId }
  # 注意：客户端超时必须大于 timeout_ms，否则服务端还在等就被本地中断
  Invoke-RestMethod -Uri "$Base/web/api/invoke" -Method Post `
    -Headers @{ 'X-AICLI-Token' = (Get-AicliToken -Base $Base) } `
    -ContentType 'application/json; charset=utf-8' `
    -Body ($body | ConvertTo-Json -Compress) `
    -TimeoutSec ([math]::Ceiling($TimeoutMs / 1000) + 30)
}

$r = Invoke-AicliPrompt -Prompt '用一句话总结当前工作区状态' -TimeoutMs 180000
$r.status
$r.assistant.content
```

备选（curl.exe）——**必须字节安全**：响应先落盘、再按 UTF-8 显式读取：

```powershell
$tmp = Join-Path $env:TEMP 'aicli-invoke.json'
curl.exe -s -o $tmp -X POST "$Base/web/api/invoke" `
  -H 'Content-Type: application/json' `
  -H "X-AICLI-Token: $(Get-AicliToken -Base $Base)" `
  -d ($body | ConvertTo-Json -Compress)
$r = [IO.File]::ReadAllText($tmp, [Text.Encoding]::UTF8) | ConvertFrom-Json
```

> ⚠️ **Windows 编码陷阱（实测踩过）**：`curl.exe` 输出的是 UTF-8 字节，而 PowerShell 用
> 控制台代码页（简体中文机器上是 GBK/936）解码**原生命令**的 stdout。把 `curl.exe …`
> 直接管道给 `ConvertFrom-Json`，非 ASCII 内容会乱码，甚至因字节被破坏而解析失败
> （`After parsing a value an unexpected character was encountered … Path 'assistant.content'`）。
> 稳妥做法二选一：① 用 `Invoke-RestMethod`（推荐）；② `curl.exe -o <file>` 落盘 +
> `[IO.File]::ReadAllText($file, [Text.Encoding]::UTF8)`。
> 纯 ASCII 的小响应（如 `/web/api/token` 的 `.token`）通常看不出问题，但涉及中文
> prompt/回复/`hint` 时必然踩坑——脚本模板统一按上面两种写法。

---

## 4. 端点速查

| 方法 | 路径 | 用途 |
|------|------|------|
| POST | `/web/api/invoke` | 同步远程调用：注入 prompt（或 `wait_only`）→ 等 turn 结束 → 一次返回状态 + assistant + TUI 渲染 + 用量 |
| POST | `/web/api/input` | 异步注入：`prompt` / `approval` / `question_answer` / `interrupt`，立即返回 `queued` |
| GET | `/web/api/turn` | turn 后验查询：`?id={turn_id}` 单条；缺省返回 `current` + `recent`（含 `assistant_preview`/`assistant_chars`、`usage` + `usage_scope` + `usage_source`） |
| GET | `/web/api/screen` | 默认完整 transcript；`?view=tui` 用户实际看到的合成帧；`?format=json` 结构化；`?tail=N` 只取末尾 N 行 |
| GET | `/web/api/status` | 渲染器/显示状态快照（等价 `/debug/chat/status`） |
| GET | `/web/api/runtime` | 运行时元数据（provider / model / reasoning 权威值） |
| GET | `/web/api/events` | SSE 实时事件流（turn / 工具 / 审批 / 提问…） |
| GET | `/web/api/events/schema` | SSE 事件类型与字段定义 |
| GET | `/web/api/sessions` | 会话列表（`current_session_id` + 候选会话） |
| POST | `/web/api/sessions/new` | 新建会话（注入 `/new`） |
| POST | `/web/api/sessions/resume` | 恢复历史会话 `{"session_id":...}` |
| POST | `/web/api/sessions/rename` | 重命名 `{"session_id":...,"title":...}`（≤100 字符） |
| POST | `/web/api/sessions/delete` | 删除历史会话（仅非当前会话） |
| GET | `/web/api/token` | 读取本进程写令牌（回环 + 同源可读，`no-store`；含 `source` 来源标识） |
| GET | `/web/` | 浏览器微型 Web 客户端（同一后端，自动注入令牌） |
| GET | `/debug/endpoints` | 全部调试/远程端点清单（JSON 或 `?format=text`；含 `version`/`build_time`/`started_at`/`uptime_sec`，用于识别旧构建实例） |

> 配置类端点（`/web/api/config/*`：provider 增删改、模型拉取探测、chat 配置保存）、
> 技能与用量分析（`/web/api/skills/*`、`/web/api/analysis/*`、`/web/api/cache/*`）
> 见 [docs/aicli/web-remote-api.md](../aicli/web-remote-api.md) 与 `/debug/endpoints` 清单。
> 上表所有 POST 均需 `X-AICLI-Token`。

---

## 5. 常用操作

以下示例统一假定 `$base = 'http://127.0.0.1:64562'`、`$token = Get-AicliToken`（见 3.3）。

### 5.1 就绪探测（重要）

进程刚启动时可能出现「HTTP 服务器已就绪、chat 会话尚未绑定」的短暂窗口，
此时写接口返回 `409` + `{"status":"error","reason":"no active chat session"}`。
脚本应先探测就绪，或把 409 当成可重试状态：

```powershell
# 会话就绪：current_session_id 非空；屏幕就绪：available=true
$ready = $false
for ($i = 0; $i -lt 30; $i++) {
  $s = curl.exe -s "$base/web/api/sessions" | ConvertFrom-Json
  if ($s.current_session_id) { $ready = $true; break }
  Start-Sleep -Milliseconds 500
}
```

### 5.2 同步问答：`POST /web/api/invoke`

```powershell
curl.exe -s -X POST "$base/web/api/invoke" `
  -H 'Content-Type: application/json' `
  -H "X-AICLI-Token: $token" `
  -d '{\"prompt\":\"把 README 的构建章节更新为当前命令\",\"timeout_ms\":180000,\"client_request_id\":\"job-42-step-1\"}'
```

请求字段：

| 字段 | 说明 |
|------|------|
| `prompt` | 要注入的文本（也可直接 POST 纯文本 body，非 JSON 时整个 body 视为 prompt） |
| `timeout_ms` | 可选，默认 `120000`，钳制范围 `[1000, 600000]` |
| `wait_only` | `true` 时**不注入**，只等当前 turn 结束（审批/提问决议后继续等待也用它）；会话本来已空闲时**立即返回 `settled`**，不空等 `timeout_ms` |
| `client_request_id` | 可选幂等键（≤128 字符）：同会话重复提交相同 id 回放首次结果（`duplicate: true`），不会重复注入 |
| `session_id` | 可选；与当前活动会话不一致时返回 `409`（防误投递，切换会话请先 `sessions/resume`） |

响应 `status` 取值与后续动作：

| status | 含义 | 下一步 |
|--------|------|--------|
| `completed` | turn 结束、会话空闲 | 读 `assistant.content` / `screen` |
| `settled` | 已就绪且空闲：输入已被消费但未观察到 LLM turn（如斜杠命令），或 `wait_only` 时本来就没有在跑的 turn（`reason` 说明 `session already idle`） | 读 `screen` 看命令输出/当前界面 |
| `timeout` | 超过 `timeout_ms` 未结束（长任务） | 用 `events` 或轮询 `screen` 继续观察，可再次 invoke |
| `interrupted` | turn 被中断（TUI 按 Esc 或远程 `interrupt`） | 视需要重新发起 |
| `requires_approval` | 停在工具审批，`pending_approval` 带 `request_id`/`tool_name`/`prompt` | 见 5.5，再用 `wait_only` 继续等 |
| `requires_answer` | 停在提问，`pending_question` 带 `question_id`/`prompt`/`suggestions` | 见 5.5，再用 `wait_only` 继续等 |
| `rejected` | 输入被命令闸门拒绝（如忙时的状态变更命令） | 看 `reason` |
| `error` | 调用方断开 / 会话关闭 / 输入队列不可用 | — |

响应正文形如：

```json
{
  "status": "completed",
  "session_id": "session_20260917202752_xxoO88dG",
  "turn_id": "turn_...",
  "elapsed_ms": 8421,
  "queued": true,
  "busy": false,
  "pending_inputs": 0,
  "usage": { "input_tokens": 16278, "output_tokens": 2, "total_tokens": 16280 },
  "assistant": { "role": "assistant", "content": "..." },
  "screen": { "available": true, "width": 120, "height": 40, "lines": ["..."], "text": "..." }
}
```

> `screen` 是"用户此刻实际看到的 TUI 合成帧"，不是网页 transcript。

### 5.3 长任务：流式（SSE）

```bash
curl -N -X POST http://127.0.0.1:64562/web/api/invoke \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -H "X-AICLI-Token: $AICLI_WEB_TOKEN" \
  -d '{"prompt":"重构 processor.go 并跑测试","timeout_ms":600000}'
```

建立 SSE 后 HTTP 状态恒为 `200`；帧类型为 `start` / `delta`（assistant 增量）/
`result`（终态，含 `status` 与 `http_status`）。中途断开不会取消 turn，可再用
`sessions`/`turn`/`screen` 查询。

搭配要点：

- **超 10 分钟的活儿用 SSE**：`timeout_ms` 上限是 `600000`（10 分钟），同步调用到点
  只会给你 `timeout`；SSE 下 `delta` 帧能实时看进展，断线后用 `turn`/`screen` 幂等续看。
- **长 prompt（几 KB 以上）**：`Invoke-AicliPrompt -Prompt (Get-Content -Raw -Encoding UTF8 .\task.md)`
  即可；curl 路线把请求体落文件后用 `--data-binary "@$bodyFile"`（避免 shell 引号截断）。
- **到点未结束不要重发 prompt**：重发会重复注入；改发 `{"wait_only":true,"timeout_ms":...}`
  继续等（空闲会话会立即回 `settled`，见 5.2）。

### 5.4 只投递不等结果 + turn 后验查询

```powershell
# 异步注入（立即返回 queued），可选带 client_request_id 便于后续核对
curl.exe -s -X POST "$base/web/api/input" -H "X-AICLI-Token: $token" `
  -H 'Content-Type: application/json' -d '{\"prompt\":\"继续\"}'

# 用 turn_id 查终态/耗时/本轮 token 增量
curl.exe -s "$base/web/api/turn?id=turn_20260917_abc"
# 缺省：current（busy/turn_id/pending_inputs/pending_approval/pending_question）+ recent（最近 20 条）
curl.exe -s "$base/web/api/turn"
```

`turn.status` = `running` / `completed` / `failed` / `interrupted`；记录保留
最近 128 条、30 分钟（重启清空；持久用量看 `/web/api/analysis/*`）。

记录里的几个字段口径：

| 字段 | 口径 |
|------|------|
| `assistant_preview` / `assistant_chars` | 本轮最后一条 assistant 消息的预览（≤200 rune，超出以 `…` 结尾）与完整字符数；完整回复走 `screen`（transcript）或 invoke 响应 |
| `usage` + `usage_scope` | `turn`：本轮增量（优先取 turn 结束事件载荷 `usage_*`，即 actor 结算值；无载荷时退回计数器差值）；`session`：增量确实不可得时回退的会话累计快照，**仅作参考**（采集时刻可能与 invoke 响应不同） |
| `usage_source` | 透传事件载荷的 `usage_source`（如 `provider_reported`），区分 provider 真报与本地估算 |
| `steps` | 本轮工具/步骤计数（来自 turn 结束事件）|

### 5.5 审批 / 提问 / 中断

```powershell
# 允许一次工具调用（拒绝用 \"allow\":false）
curl.exe -s -X POST "$base/web/api/input" -H "X-AICLI-Token: $token" `
  -H 'Content-Type: application/json' `
  -d '{\"type\":\"approval\",\"request_id\":\"req_1\",\"allow\":true}'

# 回答 AskUser 提问
curl.exe -s -X POST "$base/web/api/input" -H "X-AICLI-Token: $token" `
  -H 'Content-Type: application/json' `
  -d '{\"type\":\"question_answer\",\"question_id\":\"q_1\",\"answer\":\"深色主题\"}'

# 中断当前 turn
curl.exe -s -X POST "$base/web/api/input" -H "X-AICLI-Token: $token" `
  -H 'Content-Type: application/json' -d '{\"type\":\"interrupt\"}'

# 决议后继续等待（不产生新用户消息）
curl.exe -s -X POST "$base/web/api/invoke" -H "X-AICLI-Token: $token" `
  -H 'Content-Type: application/json' -d '{\"wait_only\":true,\"timeout_ms\":180000}'
```

> `invoke` 等待期间不持有输入互斥锁，所以审批/提问既可在 TUI 里手工回答，
> 也可远程回答；回答完用 `wait_only` 继续等是推荐姿势。

### 5.6 读屏幕与渲染状态

```powershell
# 用户实际看到的界面（合成帧文本）
curl.exe -s "$base/web/api/screen?view=tui"
# 只取末尾 30 行（长会话/长 transcript 时省流量，N 上限 2000）
curl.exe -s "$base/web/api/screen?view=tui&tail=30"
# 合成帧结构化（lines 数组）
curl.exe -s "$base/web/api/screen?view=tui&format=json"
# 完整会话 transcript（含角色结构化 messages）
curl.exe -s "$base/web/api/screen?format=json"
# 渲染器内部状态（编码/提交/门控诊断，等价 aicli /debug）
curl.exe -s "$base/web/api/status?format=text"
```

> `?view=tui` 返回的是**剥掉 ANSI 的逻辑合成帧**（内容=用户所见，不含样式/光标状态）；
> 需要字节级真值（含 ANSI）用 `aicli chat --render-output-file <file>` 的终端镜像。

### 5.7 实时事件订阅（SSE）

```bash
curl -N http://127.0.0.1:64562/web/api/events              # 实时打印
curl -s http://127.0.0.1:64562/web/api/events/schema | jq '.[].event'   # 事件定义
```

事件含 `connected`（连接快照）、`turn_start` / `turn_end`、`assistant_delta`、
`tool_start` / `tool_end`、`approval_requested`、`question_asked`、`session_interrupted`
等；每条 data 带 `_event.sequence`，断线重连可按序列补偿。

### 5.8 会话管理

```powershell
# 列表（默认按创建时间倒序；?sort=updated_at 按更新时间）
curl.exe -s "$base/web/api/sessions"                       # {sessions:[{id,title,summary,message_count,created_at,updated_at,current}], current_session_id}
# 新建（注入 /new）
curl.exe -s -X POST "$base/web/api/sessions/new" -H "X-AICLI-Token: $token"
# 恢复历史会话
curl.exe -s -X POST "$base/web/api/sessions/resume" -H "X-AICLI-Token: $token" `
  -H 'Content-Type: application/json' -d '{\"session_id\":\"session_2026...\"}'
# 重命名
curl.exe -s -X POST "$base/web/api/sessions/rename" -H "X-AICLI-Token: $token" `
  -H 'Content-Type: application/json' -d '{\"session_id\":\"session_2026...\",\"title\":\"回归测试\"}'
# 删除（仅非当前会话）
curl.exe -s -X POST "$base/web/api/sessions/delete" -H "X-AICLI-Token: $token" `
  -H 'Content-Type: application/json' -d '{\"session_id\":\"session_2026...\"}'
```

这些都是**注入式**的：请求立即返回 `queued`，由 TUI 主循环在安全时机完成切换；
用 `current_session_id` 变化确认完成（切换期间 SSE 会发 `session_end`/`session_start`/`screen_refresh`）。

### 5.9 幂等重试（CI / 网络抖动）

```bash
curl -s -X POST http://127.0.0.1:64562/web/api/invoke \
  -H 'Content-Type: application/json' -H "X-AICLI-Token: $AICLI_WEB_TOKEN" \
  -d '{"prompt":"跑一遍测试","timeout_ms":600000,"client_request_id":"ci-1024-attempt-1"}'
```

同一 `client_request_id` 重复提交返回首次结果（`duplicate: true`），**不会重复注入**；
窗口 10 分钟 / 最多 256 条。注意回放里的 `screen` 是首次完成时刻的快照，
要最新界面请另调 `/web/api/screen`。

### 5.10 浏览器操作

浏览器打开 `http://127.0.0.1:64562/web/` 即是内置微型 Web 客户端：左侧会话列表、
对话区、技能/配置/缓存/调试/关于页签；页面自动注入令牌，点按钮即可远程操作同一会话。
「关于」页签可查看/复制当前写令牌与全部端点清单（`GET /debug/endpoints`）。
前端手工回归清单见 [docs/aicli/web-testing.md](../aicli/web-testing.md)。

---

## 6. 错误码与排查

### 6.1 HTTP 状态码

| 状态码 | 常见原因 | 处理 |
|--------|----------|------|
| `400` | body 读取失败 / 空 prompt（非 `wait_only`）/ `client_request_id` 超长（>128）/ 缺少或非法字段（如 `session_id`、`title`） | 检查请求体 |
| `403` | Host 非回环、`Origin` 跨域、缺少/错误写令牌 | 用 `127.0.0.1` 访问并附 `X-AICLI-Token`；令牌默认重启即换，重新获取（用 `--web-token`/`AICLI_WEB_TOKEN` 固定时不会变） |
| `404` | `sessions/resume|delete|rename` 目标会话不存在 | 先 `GET /web/api/sessions` 拿 id |
| `405` | 方法不对（写接口只接受 POST、`/web/api/token` 只接受 GET） | 换方法；若 `POST /web/api/invoke` 也 405，见 6.2 |
| `409` | 无活动会话（启动早期）/ 已有 invoke 在等待（单飞）/ `session_id` 与当前会话不一致 / 输入队列不可用 | 先探测就绪（5.1）；串行调用；切换会话用 `sessions/resume` |
| `500` | 输入队列不可用 / 存储写入失败 | 看 TUI 日志与 `reason` |

### 6.2 常见现象

| 现象 | 原因与处理 |
|------|------------|
| 启动行没有 `write token`，`/debug/endpoints` 没有 `web` 分组，`POST /web/api/invoke` 返回 `405 method not allowed` | 该进程是**旧构建**（未包含远程调用/令牌能力）。重新构建并重启：`go build ./cmd/aicli` 后用 `--pprof` 启动 |
| `403 missing or invalid X-AICLI-Token` | 令牌取自另一个进程/重启前，或用了 `?token=` 但参数被 shell 吃掉；重新 `GET /web/api/token` |
| `wait_only` 立刻返回 `settled` | 正常：会话本来空闲（无在跑的 turn），短路返回而不是空等到 `timeout`；`busy=false`、`reason` 含 `session already idle` |
| 启动时传了 token，写请求仍 `403` | 传的值与**当前进程**实际生效值不一致（旧实例、输错、被 shell 截断）；`GET /web/api/token` 对照即可 |
| 浏览器打开 `/web/` 能看不能写（按钮报错） | 页面 meta 未注入（不是同一后端/旧构建）；重新从 `http://127.0.0.1:<port>/web/` 打开，不要用文件方式打开静态页 |
| `409 no active chat session` | 服务器已就绪但会话还没绑定；轮询 `current_session_id` 或把 409 视为可重试 |
| `timeout` 后 TUI 仍在跑 | 正常：长任务/后台作业未结束；用 `events`、`turn`、`screen` 继续观察，或再发 `wait_only` 等待 |
| 端口每次都变 | 未固定端口；用 `--web-port 64562`（推荐）或 `AICLI_PPROF=127.0.0.1:64562` 固定 |
| `--pprof` 后终端无输出 | 服务器地址打在 **stderr**；若在管道/重定向中启动，把 stderr 落文件再 grep `web write token` |

### 6.3 快速自检脚本

```powershell
$base = 'http://127.0.0.1:64562'
Write-Host ("healthy   : " + (curl.exe -s -o NUL -w '%{http_code}' "$base/web/api/screen?view=tui"))
Write-Host ("token     : " + (curl.exe -s "$base/web/api/token" | ConvertFrom-Json).status)
$s = curl.exe -s "$base/web/api/sessions" | ConvertFrom-Json
Write-Host ("session   : " + $s.current_session_id)
Write-Host ("write(403 expected without token): " + (curl.exe -s -o NUL -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{}' "$base/web/api/invoke"))
```

---

## 7. 安全边界与限制

- **仅回环**：服务器只监听 `127.0.0.1`；Host/Origin 校验挡浏览器跨站写请求与
  DNS rebinding。**不要做端口转发、反向代理或公网暴露。**
- **令牌定位**：它是"本机访问凭证"，不是多用户鉴权。同机进程本就能读启动行、
  页面 meta 与 `/web/api/token`，所以令牌不是同机隔离手段；它防的是浏览器侧
  被第三方页面利用。令牌默认每进程随机、重启轮换、不落盘。
- **固定令牌的代价**：用 `--web-token` / `AICLI_WEB_TOKEN` 指定后令牌不再轮换、长期有效，
  只建议用在自动化场景，并自行保管与轮换（换值需重启进程）。校验失败会中止启动，
  不会静默回退随机值。
- **读取面收敛**：令牌原文只出现在三处——①进程启动行；②页面注入的 meta
  （「关于」页签直接显示并可复制）；③`GET /web/api/token`。
  此外只有 TUI 的 `/debug display` 会打印 `Token:` 行（便于人工复制）；
  `/debug/endpoints`（JSON 与 `?format=text`）**不含**令牌原文，
  避免端点清单被转发/贴日志时连带泄露。
- **单飞**：`/web/api/invoke` 同一时刻只允许一个调用在等待，并发返回 `409`
  （`wait_only` 共用同一把锁）。
- **请求体上限**：1 MiB；`timeout_ms` 钳制到 `[1000, 600000]`。
- **状态记录窗口**：turn 记录 128 条 / 30 分钟；幂等回放 256 条 / 10 分钟；进程重启清空
  （需要长期统计请用 `/web/api/analysis/*` 与 `/web/api/cache/*`）。
- **不做会话内人为并发**：向同一会话并发注入复杂指令仍受 TUI 命令闸门约束，
  被拒时返回 `rejected` 并带 `reason`。

---

## 8. 相关文档

- [docs/aicli/web-remote-api.md](../aicli/web-remote-api.md) — `/web/api/*` 完整 API 契约
  （字段、SSE 帧、状态机、幂等与单飞细节）
- [docs/aicli/debug-chat-status.md](../aicli/debug-chat-status.md) — `/debug/*` 端点与
  `/debug display` 面板说明
- [docs/aicli/web-testing.md](../aicli/web-testing.md) — 微型 Web 客户端前端测试与回归清单
- [docs/aicli/README.md](../aicli/README.md) — aicli 文档索引
- [docs/user-guide/aicli.md](aicli.md) — aicli 命令与参数手册
- [docs/user-guide/runtime-server.md](runtime-server.md) — runtime-server HTTP API（跨会话/服务化场景）
