# aicli Web 远程调用 API（`/web/api/*`）

> 适用版本：集成微型 Web 客户端之后的 aicli。
> 前提：`aicli chat` / `aicli resume` / `aicli` 以 `--pprof`（或 `--debug`，或 `--web-port <端口>` /
> `AICLI_PPROF`）启动，
> loopback HTTP 服务器已开启。服务器仅监听 `127.0.0.1`，并叠加
> Host/Origin 校验与写令牌（`X-AICLI-Token`）；即便如此也不要转发/暴露到网络。

## 1. 入口发现

TUI 启动时会打印会话行与写令牌：

```text
session_20260917202752_xxoO88dG  endpoints: http://127.0.0.1:61772/debug/endpoints  web: http://127.0.0.1:61772/web/
Info: web write token (X-AICLI-Token): 3f9c8a...  (开发模式: 回环地址跳过校验)
```

`/debug/endpoints` 清单包含完整 `web` 分组（页面 / 渲染 / 事件 / 输入 / invoke / turn /
会话 / 配置 / 技能 / 分析 / 缓存），脚本可只依赖该清单发现全部入口：

> 同一清单也渲染在微型 Web 客户端「关于」页签（`GET /debug/endpoints?format=json`，
> 按 web / loopback / runtime-observe 分组，POST 端点标注「需令牌」），因此新增端点
> 只需在此登记一次，About 页与 `/debug display` 自动同步。
> `?format=text` 末尾附「Debug 使用说明」速览（排查入口 / `invoke` 驱动 / 写令牌 / 文档指针），
> 与「关于」页签的「调试速览」同一口径。

```powershell
curl.exe 'http://127.0.0.1:61772/debug/endpoints?format=text'
```

```text
loopback  (aicli --pprof 本机调试服务器)
  ...
web  (aicli 微型 Web 客户端 / 远程调用 API)
  Base: http://127.0.0.1:61772/web
  Auth: POST 请求需携带 X-AICLI-Token（或 ?token=）；令牌可由 GET /web/api/token 读取（或见 aicli 启动行 web write token），内置页面自动注入
  GET http://127.0.0.1:61772/web/  [enabled]  微型 Web 客户端页面（浏览器交互入口）
  GET  .../web/api/screen          [enabled]  当前渲染快照（默认完整 transcript；?view=tui TUI 合成帧；?format=json 结构化；?msg_limit=N&msg_before=M 只取窗口）
  POST .../web/api/invoke          [enabled]  同步远程调用（wait_only/timeout_ms/client_request_id）
  GET  .../web/api/turn            [enabled]  turn 后验查询（?id={turn_id}，含 started/finished/usage）
  GET  .../web/api/sessions        [enabled]  会话列表（current_session_id + 候选会话）
  ...（完整族见清单输出）
```

### 鉴权（Host / Origin / 写令牌）

**开发模式**（`--web-dev`，默认在 `127.0.0.1`/`localhost` 回环地址自动开启）：本机开发
调试时跳过写令牌校验，POST/PUT/DELETE 无需携带 `X-AICLI-Token`。可加 `--web-dev=false`
显式关闭。**注意**：开发模式**不影响** Host/Origin 校验，仍防止 DNS rebinding 与跨站写请求。

监听在 `0.0.0.0`（所有接口）时，**从回环 IP（`127.0.0.1`/`localhost`/`[::1]`）发起的请求
始终跳过令牌校验**，便于本地浏览器调试，无需 `--web-dev`。本地私有网段 IP
（`10.x`、`172.16-31.x`、`192.168.x`）与远程 IP 均需令牌。

1. **Host 校验**：请求 Host 必须是回环地址（`127.0.0.1` / `localhost` / `[::1]`），否则 `403`
   （挡 DNS rebinding 与反向代理转发）。
2. **Origin 校验**：携带 `Origin` 的请求必须与请求 Host 同源，否则 `403`（挡浏览器跨站写请求 CSRF）。
3. **写令牌**：所有非 GET 请求（`/web/api/*` 状态变更）必须携带
   `X-AICLI-Token: <token>` 或 `?token=<token>`，否则 `403`。
   令牌来源（按优先级）：
   - **`--web-token <token>` / `AICLI_WEB_TOKEN`**（显式指定，flag 优先于环境变量）：启动时
     固定令牌、**重启不轮换**，适合 CI / 服务化 / 外部 Agent 长期集成；字符集
     `A-Za-z0-9-._~`、长度 16~256 字节，校验失败**中止启动**（不回退随机值）。未指定时
     每进程随机生成、重启即轮换。
   - **`GET /web/api/token`**（推荐给脚本）：返回 `header` / `token` / `query_param` / `source` / `hint`，
     只读 GET 无需令牌自举，但同样受 Host（回环）+ Origin（同源）校验，响应 `no-store`；
   - **启动行（stderr）**：`Info: web write token (X-AICLI-Token): <token>`——写在 **stderr**，
     独立进程 / stdout、stderr 重定向到日志（**无 TTY**）时同样可见，不要求真实终端（显式指定时行尾标注
     来源，如 `来自 --web-token（固定令牌，重启不轮换，注意保管）`；也可在 TUI 用
     `/debug display` 查看，其 `Token:` 行即当前令牌）；令牌原文**不会**出现在
     `/debug/endpoints` 的 JSON/text 里（避免清单被转发时泄露）；
   - **页面注入**：`index.html` 注入 `<meta name="aicli-web-token">` 并由内联脚本包装
     `window.fetch` 自动附加，内置页面与「关于」页签无需手工操作。
4. 只读 GET（含 SSE 事件流，`EventSource` 无法设置请求头）不要求令牌，但仍受 Host/Origin 校验。

```powershell
# 外部脚本：先从端点取 token（或从启动行 / /debug display 复制）后调用
$token = (curl.exe -s http://127.0.0.1:61772/web/api/token | ConvertFrom-Json).token
curl.exe -s -X POST http://127.0.0.1:61772/web/api/invoke `
  -H "Content-Type: application/json" -H "X-AICLI-Token: $token" `
  -d '{\"prompt\":\"/status\",\"timeout_ms\":30000}'
```

> 令牌是**本机访问**凭证而非多用户鉴权：同机进程本就能读启动行与页面 meta，
> 因此把读取做成显式端点不扩大信任边界；它挡的是浏览器跨站写请求与 DNS rebinding。
> 令牌默认每进程随机、重启即轮换（用 `--web-token` / `AICLI_WEB_TOKEN` 固定时不轮换，
> 需自行保管——共享机器上优先用环境变量，避免出现在进程命令行），不要写入仓库或长期保存。

## 2. 端点总览

| 方法 | 路径 | 用途 |
|------|------|------|
| POST | `/web/api/invoke` | **同步远程调用**：注入 prompt（或 `wait_only`），等待 turn 结束，一次响应返回最终状态 + assistant 回复 + TUI 渲染 + token 用量；`Accept: text/event-stream` 时改为流式 delta + 最终 result |
| POST | `/web/api/input` | 异步注入：prompt / 审批决议 / 提问回答 / 中断，立即返回 `queued` |
| GET | `/web/api/turn` | turn 后验查询：`?id={turn_id}` 取单条（含耗时/步数/`assistant_preview`/`usage`+`usage_scope`/`usage_source`），无参数返回当前 turn + 最近 20 条 |
| GET | `/web/api/screen` | 当前渲染：默认完整 transcript（`messages` 结构化）；`?view=tui` 返回 TUI 合成帧；`?format=json` 结构化；`?tail=N` 只取末尾 N 行（≤2000）；`?msg_limit=N&msg_before=M` 只取结构化消息的一段窗口（附 `message_window` 分页元信息，见 §5.1） |
| GET | `/web/api/status` | 渲染器/显示状态快照（等价 `/debug/chat/status`） |
| GET | `/web/api/statusbar` | 底部状态栏快照（balance / context used / directory / git branch / window 等段，与 TUI 底部状态行同源；provider/model 见底部 cfg-bar，不在此重复） |
| GET | `/web/api/runtime` | 运行时元数据（provider/model/reasoning 权威值） |
| GET | `/web/api/events` | SSE 实时事件流（turn/工具/审批/提问…） |
| GET | `/web/api/events/schema` | SSE 事件类型定义 |
| GET/POST | `/web/api/sessions[...]` | 会话列表 / 新建 / 恢复 / 重命名 / 删除 |
| GET | `/web/api/export` | 会话导出（下载）：`?format=full\|body\|tools\|trace`（格式词同 TUI `/export`、`aicli export`），`?session_id=<id>` 指定会话（缺省当前会话）；响应为 attachment（`Content-Disposition` 同时给 ASCII 回退名与 RFC 5987 `filename*`），附 `X-AICLI-Export-Format` / `-Messages` / `-Session` 头；无活动会话 503、未知格式 400、非 GET/HEAD 405 |
| GET/POST | `/web/api/config[...]` | 配置快照 / provider 增删改与模型拉取探测 / chat 配置保存 |
| GET | `/web/api/skills[/{name}]` | 技能目录与详情 |
| GET | `/web/api/analysis[/status\|tools\|subagents\|errors]` | 用量分析 |
| GET | `/web/api/cache[/overview\|requests\|messages/{id}/trace]` | LLM 缓存分析 |
| GET/POST | `/web/api/mcps` | MCP 列表（`config`+`status`，并附 `config` 解析诊断与 `summary` 计数）/ 新增（写 `mcp.yaml` 并热重载） |
| GET/PUT/DELETE | `/web/api/mcps/{name}` | 查看 / 更新 / 删除单个 MCP |
| POST | `/web/api/mcps/{name}/enable\|disable` | 启用/停用（持久化 `enabled` + 重连，刷新会话工具） |
| GET | `/web/api/mcps/{name}/tools` | 工具清单（含被禁用项；`enabled` 有效暴露位、`configured_enabled` 用户配置位、`healthy` 运行时健康位） |
| POST | `/web/api/mcps/{name}/tools/{tool}/enable\|disable` | 启停单个工具（持久化 `tools` 段，不重连 MCP 服务） |
| POST | `/web/api/mcps/{name}/tools/enable\|disable` | 批量启停工具，body `{"tools":[...]}`；缺省/空数组 = 全部 |
| POST | `/web/api/mcps/reload` | 热重载 MCP 配置并重连 |
| GET | `/web/api/token` | 读取本进程写令牌（`X-AICLI-Token`；含 `source` 来源标识，回环 + 同源可读，`no-store`） |
| GET | `/web/` | 浏览器微型客户端页面（同一后端） |
| GET | `/debug/chat/screen` | 与 `/web/api/screen?view=tui` 同源的调试入口（保留） |

> 所有 POST 均需 `X-AICLI-Token`（见上一节）。
>
> **Windows/PowerShell 客户端**：不要用 `curl.exe … | ConvertFrom-Json` 直接接收
> 中文响应（PS 按控制台代码页解码原生命令 stdout，会乱码甚至解析失败）；用
> `Invoke-RestMethod`，或 `curl.exe -o <file>` + `[IO.File]::ReadAllText($f, UTF8)`。
> 可复制脚本见
> [../user-guide/aicli-tui-remote.md](../user-guide/aicli-tui-remote.md) 的 §3.3。

### 会话导出（`/web/api/export`）

与 TUI `/export`、顶层 `aicli export` 共用同一套会话解析、格式归一化与写出实现
（`chat_export_command.go`），导出内容与 CLI 同源，不产生第二条实现路径。

- `format`：`full`（完整 JSON，缺省）/ `body`（正文 Markdown）/ `tools`（正文 + 工具调用）/
  `trace`（正文 + 工具轨迹）；未知取值 → `400 {"error":{"code":"invalid_format",...}}`。
- `session_id`：目标会话 ID，语义同 `/export <session-id>`；缺省导出当前活动会话。
- 响应头：`Content-Disposition: attachment; filename="<ASCII 回退名>"; filename*=UTF-8''<...>`
  （文件名与 CLI 默认命名同规则 `{session}_{ts}_{format}.json|md`，非 ASCII 会话 ID 由
  RFC 5987 编码承载）、`Content-Type`（JSON 或 `text/markdown`）、
  `X-AICLI-Export-Format` / `X-AICLI-Export-Messages` / `X-AICLI-Export-Session`
  （供前端提示文件名与消息条数）。
- 无活动会话 → `503 {"error":{"code":"chat_session_not_ready",...}}`；只读 GET，回环模式免令牌，
  非回环模式由页面注入的 fetch 包装附 `X-AICLI-Token`（见上一节）。
- 页面入口：「文件」菜单 → 导出会话（完整 JSON）/ 导出正文（Markdown）/ 导出正文 + 工具调用 /
  导出正文 + 工具轨迹；前端按 `Content-Disposition` 命名并以 `Blob` + `<a download>` 落地文件
  （`backend/cmd/aicli/commands/web/js/menu.js`）。

```powershell
# -OJ 让 curl 按 Content-Disposition 命名落盘（当前会话导出为工具轨迹 Markdown）
curl.exe -s -OJ 'http://127.0.0.1:61772/web/api/export?format=trace'
```

### MCP 管理（`/web/api/mcps`）

与 `aicli mcp ...`、runtime-server `/api/runtime/mcps` 共用 `internal/mcp/admin` 的同一套
读写实现，编辑的始终是 MCP 配置文件（优先级：`./.aicli/mcp.yaml` > `~/.aicli/mcp.yaml` >
显式配置 > 向上搜索 > `configs/mcp.yaml`）。页面「MCP」页签即调用本组端点。

`GET /web/api/mcps` 额外返回（向后兼容）：`config`（`path` / `source`
（`explicit|project|user|upward|executable|default|user-fallback|session-override`）/
`exists` / `size_bytes` / `mod_time` / `manager_loaded` / `candidates[]`）与
`summary`（`{total, enabled, disabled, connected, tools}`）；`candidates` 按优先级列出
全部候选位置及 `exists`，用于定位不同 CWD 启动解析到不同 `mcp.yaml` 的问题。

`UpsertRequest` 字段：`name`、`type`（`stdio`/`sse`/`websocket`/`streamable`）、
`command`/`args`（stdio）、`url`（其余传输）、`env`、`headers`、`description`、
`enabled`、`trustLevel`、`timeoutSeconds`、`maxParallelCalls`；响应中 `config` 为落盘后的
配置，`status` 为当前运行时状态（`connected`/`toolCount`/`lastError` 等）。错误统一为
非 2xx + `{"error":{"code":"...","message":"..."}}`（校验失败 400、不存在 404）。

字段语义与约定：`env`/`headers` 省略 = 保持原值，显式 `{}` = 清空，非空 map = 整体替换；
`headers` 以 `HEADER_<Name>` 写入配置文件的 `env`（`admin/configfile.go applyHeaders`），
URL 传输据此生成 HTTP 头。页面「MCP」页签的键值行编辑器：stdio 全部按环境变量行编辑，
URL 传输把 `HEADER_*` 拆成请求头行（去前缀），保存时合并回 `env` 并整体替换
（支持删除行来清空）；行内支持 `KEY=VALUE` / `Key: Value` 多行粘贴自动拆分。

## 3. 同步远程调用：`POST /web/api/invoke`

一次请求内完成"发送 prompt → 等待 turn 结束 → 返回状态与渲染"，适合脚本 / 外部 Agent 调用。

请求体（`application/json`）：

```json
{
  "prompt": "把 README 的构建章节更新为当前命令",
  "timeout_ms": 120000,
  "client_request_id": "job-42-step-1",
  "session_id": "session_20260917202752_xxoO88dG"
}
```

- `timeout_ms` 可选，默认 120000，钳制范围 `[1000, 600000]`。
- `wait_only: true`：**不注入 prompt**，只等待当前 turn 结束（审批/提问决议后继续等待、
  或外部编排等待既有 turn 时使用）；此时可省略 `prompt`，响应 `queued=false`。
  会话**本来已空闲**（无活动 turn、无待审批/提问、队列为空）时**立即返回 `settled`**
  （`reason="session already idle: …"`），不会空等 `timeout_ms`。
- `client_request_id` 可选（≤128 字符）：**幂等键**。同一会话内重复提交相同 id 直接回放
  首次结果（`duplicate: true`）且不会重复注入；保留窗口 10 分钟、最多 256 条。
- `session_id` 可选：与当前活动会话不一致时返回 `409`（避免 prompt 误投递；切换会话请先
  `POST /web/api/sessions/resume`）。
- 非 JSON body 时整个 body 视为 prompt 文本（`text/plain` 兼容）。
- 同一时刻只允许一个 invoke 在等待；并发调用返回 `409` + `{"status":"busy"}`。
- 会话正在执行其他 turn 时，新 prompt 会排队，invoke 在当前 turn 与新 turn 全部结束后返回。
- `Accept: text/event-stream`：流式模式，先发 `start` 帧，运行中转发
  `assistant_delta` / `assistant_reasoning_delta` / `tool_started` / `tool_finished`，
  最后发 `result` 帧（内含完整响应 JSON + `http_status`）。SSE 连接一旦建立 HTTP 状态恒为 200，
  结果语义以 `result` 帧内的 `status` 为准。

示例（bash / Git Bash / WSL）：

```bash
curl -s -X POST http://127.0.0.1:61772/web/api/invoke \
  -H 'Content-Type: application/json' \
  -H "X-AICLI-Token: $AICLI_WEB_TOKEN" \
  -d '{"prompt":"用一句话总结当前会话状态","timeout_ms":120000}' | jq .
```

示例（PowerShell）：

```powershell
curl.exe -s -X POST http://127.0.0.1:61772/web/api/invoke `
  -H "Content-Type: application/json" `
  -H "X-AICLI-Token: $token" `
  -d '{\"prompt\":\"用一句话总结当前会话状态\",\"timeout_ms\":120000}'
```

响应（HTTP 200，`status` 表达结果）：

```json
{
  "status": "completed",
  "session_id": "session_20260917202752_xxoO88dG",
  "turn_id": "turn_...",
  "elapsed_ms": 8421,
  "queued": true,
  "busy": false,
  "pending_inputs": 0,
  "llm_observed": true,
  "usage": { "input_tokens": 16278, "output_tokens": 2, "total_tokens": 16280,
             "context_tokens": 16280, "context_window_tokens": 1000000 },
  "assistant": { "role": "assistant", "content": "当前会话正在..." },
  "screen": {
    "available": true,
    "width": 120,
    "height": 40,
    "lines": ["..."],
    "text": "..."
  }
}
```

> `turn_id` 由 turn 生命周期事件（`session_start` / `session_end`）回填：turn 结束后
> actor 已清空 `CurrentTurnID`，但响应仍会带上本轮的 turn 身份（turn 运行中以实时探测
> 为准；`wait_only` 空闲短路等"无 turn 可归属"时缺省）。可直接用它走 `?id=<turn_id>`
> 后验；需要交叉核对时用 `GET /web/api/turn` 的 `recent` 中 `status=completed` 记录的
> `assistant_preview`（实测见 [../e2e/debug-guide.md §7.2](../e2e/debug-guide.md)）。

`screen` 与 `/debug/chat/screen`、`/web/api/screen?view=tui` 同源，是"用户当前实际看到的 TUI 界面渲染"（合成帧文本），不是 web 页的完整 transcript。

### status 取值

| status | 含义 | 后续动作 |
|--------|------|----------|
| `completed` | turn 结束，会话空闲 | 读取 `assistant` / `screen` |
| `settled` | 输入已被消费且会话空闲但未观察到 LLM turn（斜杠命令等）；或 `wait_only` 时本来就没有在跑的 turn | 读取 `screen` 确认命令输出/当前界面 |
| `timeout` | 超过 `timeout_ms` 仍未结束（长任务/后台作业） | 用 `/web/api/events` 或轮询 `/web/api/screen` 继续观察，可再次 invoke |
| `interrupted` | turn 被中断（终端 Esc 或 `POST /web/api/input {"type":"interrupt"}`） | 视需要重新发起 |
| `requires_approval` | 会话停在审批等待，`pending_approval` 携带 `request_id` / `tool_name` / `prompt` | `POST /web/api/input {"type":"approval","request_id":"...","allow":true}` 后用 `{"wait_only":true}` 继续等待 |
| `requires_answer` | 会话停在提问等待，`pending_question` 携带 `question_id` / `prompt` / `suggestions` | `POST /web/api/input {"type":"question_answer","question_id":"...","answer":"..."}` 后用 `{"wait_only":true}` 继续等待 |
| `rejected` | 输入被命令闸门拒绝（如忙时的 `/exit` 等状态变更命令） | 查看 `reason` |
| `error` | 调用方断开 / 会话关闭 / 输入队列不可用 | — |

HTTP 错误码：`400` body 读取失败 / 空 prompt（非 wait_only）/ `client_request_id` 超长；
`403` Host 非回环、跨域 Origin 或缺少写令牌；`405` 非 POST；`409` 无活动会话、已有 invoke 在等待、
或 `session_id` 与当前会话不一致；`500` 输入队列不可用。

> 进程刚启动时可能出现「loopback 服务器已就绪、但 chat 会话尚未绑定」的短暂窗口，
> 此时写接口返回 `409 no active chat session`。外部脚本应先探测会话就绪
> （`GET /web/api/sessions` 的 `current_session_id` 非空，或 `GET /web/api/screen` 的
> `available=true`），或把 409 视为可重试状态。

> 幂等回放（`duplicate: true`）始终返回首次的 `status` / `assistant` / `screen`，
> 但其中的 `screen` 是**首次完成时刻**的快照；需要最新界面请调用 `/web/api/screen`。

## 4. 异步注入：`POST /web/api/input`

立即返回 `{"status":"queued"}`，后续通过 SSE 或轮询读取：

```bash
# 普通 prompt
curl -s -X POST http://127.0.0.1:61772/web/api/input \
  -H 'Content-Type: application/json' -H "X-AICLI-Token: $AICLI_WEB_TOKEN" -d '{"prompt":"继续"}'

# 审批 / 提问 / 中断
curl -s -X POST http://127.0.0.1:61772/web/api/input -H "X-AICLI-Token: $AICLI_WEB_TOKEN" -d '{"type":"approval","request_id":"req_1","allow":true}'
curl -s -X POST http://127.0.0.1:61772/web/api/input -H "X-AICLI-Token: $AICLI_WEB_TOKEN" -d '{"type":"question_answer","question_id":"q_1","answer":"深色主题"}'
curl -s -X POST http://127.0.0.1:61772/web/api/input -H "X-AICLI-Token: $AICLI_WEB_TOKEN" -d '{"type":"interrupt"}'
```

异步路径配合 turn 后验查询使用：input 立即返回后，用 turn_id（或轮询最近记录）
判断终态与耗时，无需反复拉整屏：

```bash
curl -s 'http://127.0.0.1:61772/web/api/turn?id=turn_20260917_abc' | jq '.turn'
curl -s 'http://127.0.0.1:61772/web/api/turn' | jq '.current, .recent[0]'
```

`/web/api/turn` 返回：`found` / `turn`（`status` = running|completed|failed|interrupted，
`started_at` / `finished_at` / `duration_ms` / `steps` / `error` / `usage` + `usage_scope` + `usage_source`）/
`current`（活动 turn 实时探测：`turn_id` / `busy` / `pending_inputs` / `pending_approval` / `pending_question`）/
`recent`（最近 20 条，最新在前）。记录上限 128 条、保留 30 分钟。

- `assistant_preview`（≤200 rune，超出以 `…` 结尾）/ `assistant_chars`：本轮最后一条
  assistant 消息的预览与完整字符数——查“这轮回了什么”不必再拉整份 transcript。
- `usage` 的口径由 `usage_scope` 标注：
  - `turn` = **本轮增量**。优先取 session_end / session_interrupted 事件载荷里的
    `usage_prompt_tokens` / `usage_completion_tokens` / `usage_total_tokens`（actor 在结算时刻
    写入的 `result.Usage`，无竞态、无需等待）；载荷缺失时退回会话计数器差值。
  - `session` = 增量确实不可得时的**会话累计快照**，仅作参考（采集时刻可能与 invoke 响应不同）。
- `usage_source` 透传事件载荷的 `usage_source`（如 `provider_reported` / 估算值），
  用于区分“provider 真报”与“本地估算”。

## 5. 读取状态与渲染

```bash
# TUI 合成帧（用户实际看到的界面）
curl -s 'http://127.0.0.1:61772/web/api/screen?view=tui'
curl -s 'http://127.0.0.1:61772/web/api/screen?view=tui&format=json' | jq .lines

# 完整会话 transcript（含角色结构化 messages）
curl -s 'http://127.0.0.1:61772/web/api/screen'
curl -s 'http://127.0.0.1:61772/web/api/screen?format=json' | jq '.messages[-1]'

# 渲染器内部状态（编码/提交/门控诊断）
curl -s 'http://127.0.0.1:61772/web/api/status?format=text'
```

### 5.1 长会话窗口化：`?msg_limit=N&msg_before=M`

会话很长时（几千个 turn），一次返回完整 transcript 会让响应体、前端 DOM 与内存随会话线性增长。
`/web/api/screen?format=json` 支持只取一段窗口：

| 参数 | 含义 |
|------|------|
| `msg_limit=N` | 最多返回 N 条结构化消息（钳制到 `[1, 500]`） |
| `msg_before=M` | 窗口右边界（排他，绝对消息索引）；缺省或 > 总数时归一为消息总数，即「最新一页」 |

两个参数都缺省时行为与历史完全一致：返回完整 `messages`（兼容既有调用方，`message_window` 也不出现）。
窗口激活时，`messages` 之外的 `lines` / `text` 同步按窗口内消息重建，避免「裁了 `messages`
却仍回传全量文本」的假优化。

服务端同样是窗口化的：窗口激活时先统计消息条数、再按区间提取窗口内消息
（`buildChatWebScreenSnapshotWindowed`），不再「先构造全量 transcript 再切片」，
因此单次刷新的内存开销只与窗口大小相关。5000 条消息取 40 条的一页（本机
`go test ./cmd/aicli/commands/ -run '^$' -bench WindowVsFull -benchmem`）：

| 路径 | ns/op | B/op | allocs/op |
|------|-------|------|-----------|
| 窗口化提取（现实现） | ≈229µs | ≈7.7 KB | 28 |
| 全量后切片（历史实现） | ≈453µs | ≈588 KB | 2529 |

两条路径的输出逐字段一致（`messages` / `lines` / `text` / `message_window`），
由 `chat_debug_screen_window_test.go` 的等价性用例守住。

响应中的 `message_window` 是分页元信息：

```json
{ "message_window": { "total": 4210, "start": 4170, "end": 4210, "limit": 40, "has_more": true } }
```

- `start` 是本次第一条消息的绝对索引（左闭），`end` 是排他右边界；
- `has_more=true` 表示还有更早的消息：以 `msg_before={start}` 作为游标取下一页，直到 `has_more=false`（`start=0`）；
- `total` 不受窗口影响，可用于显示进度（如「已加载 4170 / 4210」）。

```bash
# 最新 40 条（首屏）
curl -s 'http://127.0.0.1:61772/web/api/screen?format=json&msg_limit=40' | jq .message_window

# 再往前 40 条（用上一页的 start 作为游标）
curl -s 'http://127.0.0.1:61772/web/api/screen?format=json&msg_limit=40&msg_before=4170' \
  | jq '.messages[0].role, .message_window'
```

> 微型 Web 客户端页面已默认使用该窗口：首屏只拉 `msg_limit=40`（最新一页），
> 用户向上滚动到顶部时自动以 `msg_before` 前插更早的消息。因此浏览器侧的内存 / DOM
> 规模只与「已加载的页数」相关，不再随会话总 turn 数增长。

## 6. 实时事件：`GET /web/api/events`（SSE）

```bash
# 实时打印 turn / 工具 / 审批事件（Ctrl+C 结束）
curl -N http://127.0.0.1:61772/web/api/events

# 事件类型与字段定义
curl -s http://127.0.0.1:61772/web/api/events/schema | jq '.[].event'
```

事件含 `connected`（会话/忙碌/待审批快照）、`turn_start` / `turn_end`、`assistant_delta`、`tool_start` / `tool_end`、`approval_requested`、`question_asked`、`session_interrupted` 等；每条 data 带 `_event.sequence` 序列号，断线重连后可按序列补偿。

## 7. 端到端脚本示例（等待回答并取文本）

```bash
resp=$(curl -s -X POST http://127.0.0.1:61772/web/api/invoke \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"列出当前目录的 Go 包名","timeout_ms":180000}')
echo "$resp" | jq -r '.status'
echo "$resp" | jq -r '.assistant.content // "（无 assistant 文本，见 screen）"'
```

若 `status=requires_approval`，先回答，再用 **wait_only** 继续等待（不产生额外用户消息）：

```bash
curl -s -X POST http://127.0.0.1:61772/web/api/input \
  -H "X-AICLI-Token: $AICLI_WEB_TOKEN" -d '{"type":"approval","request_id":"req_1","allow":true}'
curl -s -X POST http://127.0.0.1:61772/web/api/invoke \
  -H 'Content-Type: application/json' -H "X-AICLI-Token: $AICLI_WEB_TOKEN" \
  -d '{"wait_only":true,"timeout_ms":180000}'
```

带幂等键的重试脚本（网络抖动时安全重试，不会重复注入）：

```bash
curl -s -X POST http://127.0.0.1:61772/web/api/invoke \
  -H 'Content-Type: application/json' -H "X-AICLI-Token: $AICLI_WEB_TOKEN" \
  -d '{"prompt":"跑一遍测试","timeout_ms":600000,"client_request_id":"ci-1024-attempt-1"}'
```

流式调用（边跑边收 delta，最后一个 `result` 帧给出终态）：

```bash
curl -N -X POST http://127.0.0.1:61772/web/api/invoke \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream' \
  -H "X-AICLI-Token: $AICLI_WEB_TOKEN" \
  -d '{"prompt":"重构 processor.go 并跑测试","timeout_ms":600000}'
```

## 8. 限制与安全

- 仅 loopback（`127.0.0.1`）监听；Host/Origin 校验挡浏览器跨站与 DNS rebinding，写令牌挡本机
  非授权进程/误配置客户端。**不要做端口转发或公网暴露。**
- 开发模式（`--web-dev`，默认在 `127.0.0.1`/`localhost` 自动开启）跳过写令牌校验，便于本地
  调试。监听在 `0.0.0.0` 时，回环 IP（`127.0.0.1/localhost/[::1]`）的请求始终
  跳过令牌校验（无需 `--web-dev`）；本地网络 IP 与远程 IP 均需令牌。生产环境
  （`--web-host 0.0.0.0` 等非回环）时务必确保 `--web-dev=false`，此时除回环 IP 外
  所有请求（含 GET/SSE/页面加载）都需要令牌。
- 令牌在进程启动时随机生成、不落盘；重启后失效，需要重新从启动行获取。
- 请求体上限 1 MiB；`timeout_ms` 钳制到 `[1000, 600000]`。
- `/web/api/invoke` 单飞（single-flight）：并发 invoke 返回 409，避免多个远程调用方互相等待。
- invoke 等待期间不持有输入互斥锁，审批/提问仍可通过 `/web/api/input` 或 TUI 本机操作回答；
  `wait_only` 请求与普通 invoke 共用同一把单飞锁。
- 幂等回放窗口 10 分钟 / 256 条；回放内容为首次结果快照（含当时的 `screen`）。
- 流式 invoke 在建立 SSE 后 HTTP 状态恒为 200，最终语义看 `result` 帧的 `status` 与 `http_status`。
- SSE 与 invoke 均复用当前活动会话；无活动会话时返回 409 / `available=false`。
- turn 记录只保留最近 128 条、30 分钟；服务重启后历史记录清空（持久用量查询请用
  `/web/api/analysis/*` 与 `/web/api/cache/*`）。
