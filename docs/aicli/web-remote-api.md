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
| GET | `/web/api/health` | 网格存活探针：不依赖会话与渲染器，恒 200（`available` / `node_id` / `pid` / `uptime_sec` / `session_active` / `busy` / `mesh_ready`）；供网格探活、脚本就绪等待与 `aicli-mesh doctor` 复用 |
| GET | `/web/api/mesh/self` | 网格：本节点自述（档案同形字段 + `derived` 内存实时值 + `mesh` 根目录；`auth.token` 默认脱敏，见 §9） |
| GET | `/web/api/mesh/peers` | 网格：全量视图（`counts` 恒全量口径；`scope` / `workspace` / `state` 只过滤 `nodes[]`；`probe=0` 默认不发网络请求，见 §9） |
| GET | `/web/api/mesh/events` | 网格：实时事件流（SSE 扇入；`?since_seq=<n>` 续传游标、`?peers=auto\|none` 订阅拓扑；只连本进程即可看到全网格，见 §9.4） |
| POST | `/web/api/mesh/call` | 网格：跨进程调用（op 白名单 9 项；写操作逐次 `allow_write=true`；仅回环，见 §9.5） |
| POST | `/web/api/mesh/spawn` | 网格：拉起（在会话工作区复用活节点或拉起新进程，返回含令牌的窗口 URL；仅回环，见 §9.6） |
| POST | `/web/api/mesh/stop` | 网格：停止节点（`graceful` 投 `/exit` 等目标收尾、`force` 终止进程；仅回环，治理动作**默认关闭**：`--mesh-allow-stop=true` 才生效，见 §9.8） |
| GET | `/web/` | 浏览器微型客户端页面（同一后端；`/web?session=<id>&token=<t>` 深链自举，见 §9.6） |
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
> `assistant_preview`（实测见 [../e2e/debug-guide.md §7](../e2e/debug-guide.md)）。

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

## 9. 网格控制面（`/web/api/mesh/*` + `/web/api/health`）

多进程网格（`~/.aicli/mesh/`，设计见
[../plan/aicli-mesh-architecture.md](../plan/aicli-mesh-architecture.md)）把「本机有哪些
aicli 进程、各自在哪个工作区、哪个会话归谁」变成可发现的事实。§9.1–§9.4 是**只读控制面**：
与 `aicli-mesh ls` / `show` 消费同一份聚合（`internal/mesh` 的 `BuildView`），不另写口径；
§9.5 是本节唯一的写路径（跨进程调用，逐次显式 `allow_write`）。

发现方式与其它端点一致：`GET /debug/endpoints` 的清单新增 `scheme: "mesh"` 分组。
**URL 只从清单取**——端口由进程自选（网格负责发现），不要再假设「粘性端口 = 会话」。
`--mesh=false` 时这些路由**不注册**（404），清单里也不出现。

### 9.1 存活探针：`GET /web/api/health`

极轻量（不扫盘、不派生重活），不依赖会话与渲染器——无会话时同样 200，只是
`session_active=false`：

| 字段 | 含义 |
|------|------|
| `available` | 恒 `true`（端点存在即可用；不可用时路由不存在） |
| `node_id` | 本进程网格节点 ID（`--mesh=false` 时省略） |
| `pid` / `uptime_sec` | 进程 ID / 已运行秒数 |
| `session_active` | 是否已有活动会话（`--mesh=false` 时回退进程内 chat 会话判定） |
| `busy` | 当前是否有 turn 在跑 |
| `mesh_ready` | 网格根可用（目录可写）时为 `true` |

### 9.2 节点自述：`GET /web/api/mesh/self`

响应 = 磁盘档案 `nodes/<node_id>.json` 的同形字段（`schema_version` / `node_id` / `pid` /
`process` / `endpoint` / `auth` / `session` / `workspace` / `liveness`…），外加三段：

```jsonc
{
  "available": true,
  "node_id": "n-1a2b3c4d",
  "auth": { "mode": "loopback", "required": false, "token": "0f3a…" },  // 默认脱敏
  "session": { "id": "sess-…", "busy": false },
  "liveness": { "state": "live", "heartbeat_at": "…", "heartbeat_ttl_sec": 15 },
  "derived": {                 // 内存实时值：比心跳落盘的档案更新
    "busy": false, "pending_inputs": 0, "turn_id": "…",
    "peer_count": 1,           // 除自己以外的 live 节点数（跨工作区全量）
    "lease": "owner"           // owner | conflict | peer | none（§4.4）
  },
  "mesh": { "enabled": true, "root": "C:\\Users\\me\\.aicli\\mesh", "journal": "…\\journal\\n-1a2b3c4d.ndjson" }
}
```

- **令牌脱敏（默认）**：`auth.token` 只给 `0f3a…` 形式的前缀提示；只有**回环同源**请求加
  `?reveal_token=1` 才返回原文（与 `GET /web/api/token` 同一信任模型）。
- **降级**：网格未启用 → `200 {"available":false,"reason":"mesh disabled"}`；档案尚未
  可用 → `"mesh record unavailable"`。**绝不 5xx**。
- 其它方法 → `405` + `Allow: GET`。

### 9.3 全量视图：`GET /web/api/mesh/peers`

查询参数（非法取值按默认处理，不 400——诊断面容错优先）：

| 参数 | 默认 | 语义 |
|------|------|------|
| `scope` | `all` | `all`=跨工作区全量；`self`=只看本节点工作区（等价 `workspace=<本工作区>`） |
| `workspace` | 空 | 工作区路径过滤，可重复或逗号分隔；原样回显在 `filter.workspace` |
| `state` | `all` | `all` / `live`（只列 live） |
| `probe` | `0` | `1` 时对其它节点做可达性探测（**默认关闭：不发任何网络请求**） |
| `reveal_token` / `redact_token` | 脱敏 | 回环 + `reveal_token=1`（或 `redact_token=0`）时 `nodes[].auth.token` 回原文 |

**硬契约**：`counts` 恒为**全量**口径（过滤只影响 `nodes[]`，不缩小 `counts`，也绝不隐藏
`conflict`）；`filter` 原样回显生效条件；`nodes[]` 按 `state`（live 优先）→ `node_id` 稳定排序。

```jsonc
{
  "schema_version": 1, "generated_at": "…", "root": "C:\\Users\\me\\.aicli\\mesh",
  "self": { "node_id": "n-1a2b3c4d", "session_id": "sess-…" },
  "counts": { "live": 2, "stale": 1, "unknown": 0, "conflict": 0 },   // 恒全量
  "filter": { "scope": "all", "workspace": null, "state": "all" },
  "nodes": [
    {
      "node_id": "n-1a2b3c4d", "pid": 1234, "state": "live",
      "reachability": "skipped",            // probe=0 时恒为 skipped（未探测）
      "endpoint": { "port": 51234, "base_url": "http://127.0.0.1:51234", "…": "…" },
      "auth": { "required": false, "mode": "loopback", "token_hint": "0f3a…" },
      "session": { "id": "sess-…", "busy": false },
      "workspace": { "path": "E:\\proj", "name": "proj" },
      "ownership": "owner",                 // owner | peer | conflict | none（§4.2/§4.4）
      "heartbeat_at": "…", "age_sec": 1, "journal_tail": ["…"]
    }
  ],
  "workspaces": [ { "path": "E:\\proj", "name": "proj", "nodes": 2 } ]
}
```

- `state`：`live`（心跳新鲜）/ `stale`（心跳过期或 pid 已不在）/ `stopped`（优雅退出）/
  `unknown`（档案不可读或 schema 不认识；`error` 字段带原因）。
- `ownership`：`owner`=会话归本节点；`peer`=归其它 live 节点；`conflict`=同一会话被多个
  live 节点同时认领（§4.4，写路径必须停下）；`none`=无会话。
- **降级**：网格根不可读 → `200` + 空视图（`nodes: []`、`counts` 全 0），**绝不 5xx**。

```powershell
# 全量视图（默认不探测、不发网络请求）
Invoke-RestMethod http://127.0.0.1:51234/web/api/mesh/peers | ConvertTo-Json -Depth 6

# 只看本工作区、只要 live，并让服务端探测其它节点
Invoke-RestMethod 'http://127.0.0.1:51234/web/api/mesh/peers?scope=self&state=live&probe=1'
```

> 与 CLI 同源：`aicli-mesh ls --json` 输出同一份 `BuildView` 结果（S6 起可用）；
> 多进程验收（互发现 / 定向调用 / 崩溃对账 / GC）见
> [../e2e/mesh-e2e.md](../e2e/mesh-e2e.md)（E2E-DEBUG-03）。

### 9.4 实时事件流：`GET /web/api/mesh/events`（SSE 扇入）

**一句话**：浏览器/脚本只连**自己进程**的这一条流，就能看到整个网格的实时状态——本节点作为
扇入点，把各 peer 的 SSE 订阅结果并入本进程的事件流（架构 §6.1 第 3 层 / §6.5）。

```bash
# 实时打印网格事件（Ctrl+C 结束）
curl -N http://127.0.0.1:51234/web/api/mesh/events

# 带续传游标（只收 seq > 42 的帧）与「只看本节点自产帧」的拓扑
curl -N 'http://127.0.0.1:51234/web/api/mesh/events?since_seq=42&peers=none'
```

查询参数（非法取值按默认处理，不 400——诊断面容错优先）：

| 参数 | 默认 | 语义 |
|------|------|------|
| `since_seq` | `0` | 跳过 `seq <= n` 的帧；每帧帧首带 `id: <seq>` 行，断线重连时回填最后收到的 seq |
| `peers` | `auto` | `auto`=本节点作为扇入点（本节点自产帧 + peer 事件）；`none`=只收本节点自产帧 |

**帧类型**（`event:` 行；每条 data 为 `schema_version` / `seq` / `ts` / `source_node_id` /
`type` / `data` 的 JSON 信封，`seq` 即帧首 `id:` 行）：

| 帧 | 触发 |
|----|------|
| `mesh.ready` | 订阅成功后的首帧：回显 `since_seq` / `peers` / 当前客户端数，并给出 `resume_hint` |
| `mesh.peer.joined` / `mesh.peer.left` | peer 进程上线 / 退出 |
| `mesh.peer.updated` | peer 心跳、端点就绪（`callable` 翻新）、忙碌翻转、会话切换 |
| `mesh.session.changed` | 会话 `activated` / `deactivated` / `ownership`（租约得失） |
| `mesh.call.invoked` / `mesh.call.completed` | 跨进程调用开始 / 结束（S8 起） |
| `mesh.peer.event` | peer 进程内的白名单事件（turn / 工具 / 审批），内层帧原样放在 `data.frame`，来源见 `data.peer_node_id` |
| `mesh.lagged` | 本连接缓冲溢出：`data.skipped` 是跳号数，**消费方应重新拉一次 `/web/api/mesh/peers` 做全量兜底** |

**硬契约**：

- **防环**：`mesh.peer.event` 绝不二次转发（§6.3）。节点间订阅固定用 `peers=none`，因此
  A↔B 互订也不会出现回声；`source_node_id` 记录事件**来源节点**，`seq` 恒为**本节点**计数器。
- **seq 与 journal 同源**：扇入帧与本节点 journal 共用同一计数器，单调递增、无重复。
- **限流**：每 peer 20 帧/秒（突发 40），超限丢弃并计数；丢弃数出现在
  `/web/api/mesh/peers` 的 `nodes[].dropped_events`（§6.4）。
- **客户端上限**：单节点 32 条 SSE 连接，超限 `429`（**不影响既有连接**）。
- **降级**：网格未启用（`--mesh=false`，路由通常不注册）→ `200 {"available":false,
  "reason":"mesh disabled"}`；扇入已关闭或订阅被拒 → `503`（客户端上限为 `429`），
  错误信封为 `{"status":"error","code":"mesh_stream_unavailable","message":…,"node_id":…,
  "schema_version":1}`；**不阻塞 chat**（MN1 / §4.7）。
- 其它方法 → `405` + `Allow: GET`。

> 流**不重放历史**（与 `/web/api/events` 同语义）：连接建立前发布的帧不会补发；需要全量
> 现状先拉一次 `/web/api/mesh/peers`，之后靠本流增量维持。

**前端消费口径（S12，Web 子方案 §5.6）**：内置 Web 客户端把本流当**刷新信号**用——
`mesh.peer.joined/left/updated`、`mesh.session.changed`、`mesh.peer.event`（turn/session 白名单）
任一到达即重拉一次 `GET /web/api/sessions?scope=all`（200ms 合并刷新），不做客户端增量合并
（分组计数 / 忙碌翻转 / 归属变化全部回到同源视图重算）；`mesh.lagged` 视为跳号，直接全量兜底；
断线按 1s→2s→4s…（≤30s）重连并回填 `?since_seq=<最后收到的 seq>`；SSE 不可用（旧节点 /
非回环无令牌 / 代理阻断）时降级为 10s 轮询同源视图，徽标仍可用（只是不实时）。
回环模式下本流无需令牌（Host/Origin 校验已足够）；非回环模式与其它端点一致，`EventSource`
只能经 `?token=` 携带**本进程**令牌（peer 令牌永不进入前端，§9.6 红线）。

### 9.5 跨进程调用：`POST /web/api/mesh/call`

**一句话**：把一次调用送到**另一个进程**，由目标端点执行后原样返回结果——网格层只做
「解析目标 → 带令牌转发 → 折叠状态」，不产生第二套语义（架构 §5.6 / §5.9）。

```bash
# 只读：问目标节点「你是谁」（目标 node_id 从 /web/api/mesh/peers 或 aicli-mesh ls 取）
curl -s -X POST http://127.0.0.1:51234/web/api/mesh/call \
  -H 'Content-Type: application/json' \
  -d '{"target":"node-9001-20260924T073500Z","op":"node.info"}'

# 写操作：逐次显式 allow_write=true（无隐式放行）
curl -s -X POST http://127.0.0.1:51234/web/api/mesh/call \
  -H 'Content-Type: application/json' \
  -d '{"target":"node-9001-20260924T073500Z","op":"invoke","args":{"prompt":"只回复两个字：收到"},"client_request_id":"mesh-42-1","allow_write":true}'
```

请求体：

| 字段 | 必需 | 语义 |
|------|------|------|
| `op` | 是 | 白名单 op（见下表）；未知 op → `400` + `mesh_unknown_op` |
| `target` | 否 | 节点引用（node_id / node_id 前缀 / 会话 id）。被调方只用它做**防串线自检**：形如 `node-*` 且不等于本进程 `node_id` → `404` + `mesh_target_mismatch` |
| `args` | 否 | 端点参数；**按白名单逐项搬运**，未列出的键被丢弃——网格层不是「任意端点参数」的旁路 |
| `client_request_id` | 否 | 幂等键，原样透传给目标端点（网格层不重复实现幂等） |
| `timeout_ms` | 否 | 调用方等待上限（默认 `130000`）；与 `args.timeout_ms`（`invoke` 的服务端等待）不是同一层 |
| `allow_write` | 写操作必需 | 缺省即拒绝：`403` + `mesh_write_not_allowed` |

op 白名单（9 项，硬编码于 `internal/mesh`；args 与转发目标都是契约的一部分）：

| op | args（透传） | 转发到 | 写操作 |
|----|--------------|--------|--------|
| `node.info` | — | `GET /web/api/mesh/self` | 否 |
| `status` | — | `GET /web/api/status` | 否 |
| `screen` | `view`,`tail`,`format` | `GET /web/api/screen` | 否 |
| `turn` | `id` | `GET /web/api/turn` | 否 |
| `sessions.list` | — | `GET /web/api/sessions` | 否 |
| `invoke` | `prompt`,`wait_only`,`timeout_ms`,`session_id` | `POST /web/api/invoke` | **是** |
| `input` | `type`,`prompt`,`request_id`,`allow`,`question_id`,`answer`,`discard_pending` | `POST /web/api/input` | **是** |
| `cancel` | `discard_pending` | `POST /web/api/input`（`type=interrupt`） | **是** |
| `sessions.resume` | `session_id` | `POST /web/api/sessions/resume` | **是** |

响应 = 统一信封（`schema_version` / `status` / `code` / `message` / `node_id` / `op` /
`elapsed_ms` / `duplicate` / `result`）：

```json
{ "schema_version": 1, "status": "ok", "node_id": "node-9001-20260924T073500Z",
  "op": "invoke", "elapsed_ms": 3211, "result": { "...": "目标端点的原始响应" } }
```

`result` 是目标端点的**原始响应体**（`invoke` 的 `turn_id`/`assistant`、`screen` 的快照…）：
网格层不重写语义，直接调端点与经网格调用拿到的结构一致；`duplicate` 只在目标端点报出幂等命中时出现。

状态与 HTTP 映射（§5.9）：

| `status` | HTTP | 何时 |
|----------|------|------|
| `ok` | 200 | 目标端点正常返回 |
| `busy` | 409 | 目标会话正忙（单飞锁；与 `/web/api/invoke` 的 busy 同源，可重试） |
| `not_found` | 404 | 目标不存在/已死，或 `target` 与本次接收方不符（`mesh_target_mismatch`） |
| `refused` | 403 | 策略拒绝：非回环（`mesh_nonloopback_denied`）、写操作未显式允许（`mesh_write_not_allowed`）、收敛开关下的跨工作区**写**调用（`mesh_cross_workspace_denied`）、令牌轮换后仍失败（`mesh_token_stale`）、网格关闭（`mesh_disabled`） |
| `unreachable` / `timeout` | 504 | 目标不可达 / 超过调用方等待上限 |
| `error` | 400 / 413 / 500 | 参数与形状错误（`mesh_unknown_op` → 400、`mesh_body_too_large` → 413）/ 分发或目标端点内部错误（`mesh_upstream_error` → 500） |

**硬契约**：

- **鉴权**：与其它写端点同一层（`X-AICLI-Token`；回环 + 开发模式免令牌），**额外要求调用者来自回环**——
  跨机一律拒绝。`X-AICLI-Mesh-Caller: <caller node_id>` 只用于审计与跨工作区判定，不参与鉴权。
- **写操作逐次显式允许**：`allow_write=true` 必须每次给出；没有「网格级 yolo」开关（架构 §9.2）。
- **跨工作区默认放行**：工作区是筛选维度、不是权限边界；仅当目标进程以 `--mesh-restrict-workspace`
  启动时，跨工作区的**写调用**被拒（只读调用不受影响，架构 §9.3）。
- **审计**：目标进程写 `mesh.call.received` / `mesh.call.completed`（只记 op / 状态 / 耗时与调用者，
  **不记 args 正文**——args 可能含用户 prompt）；令牌绝不出现在响应、journal 与视图里。
- **请求体上限** 1 MiB → `413` + `mesh_body_too_large`；其它方法 → `405` + `Allow: POST`。
- **降级**：`--mesh=false` 时路由**不注册**（404，进程不在网格里）；真被调用到也只回
  `403` + `mesh_disabled`，不 panic、不 5xx。

> CLI 侧等价物：`aicli-mesh call <target> <op> [--args JSON] [--allow-write]`；`send` 与 `screen`
> 是它的两个高频封装（见 [mesh-cli.md](mesh-cli.md) §4.7–§4.9）。CLI 在目标返回 401 时
> 会重读目标档案并重试一次（令牌轮换，架构 §5.6 要点 5）。

### 9.6 拉起与窗口深链：`POST /web/api/mesh/spawn`

**一句话**：在**会话所属工作区**复用活节点，必要时拉起一个新进程，返回可直接交给浏览器的
§7.3 窗口 URL——前端「在新窗口打开」按钮与 `aicli-mesh open` 共用同一套实现（`mesh.Spawn`），
区别只是调用方标签（`origin`）。

```bash
# 复用或拉起：返回的 url 含令牌，直接丢给浏览器即可
curl -s -X POST http://127.0.0.1:51234/web/api/mesh/spawn \
  -H 'Content-Type: application/json' \
  -d '{"session_id":"sess-20260924-abc"}'
```

```powershell
# CLI 等价物（退出码见 §7.3：0 成功 / 2 目标不存在 / 3 不可达 / 5 失败）
aicli-mesh open sess-20260924-abc --json
```

请求体：

| 字段 | 必需 | 语义 |
|------|------|------|
| `session_id` | 是 | 目标会话（决定工作区与单飞锁键）；空 → `400` |
| `port` | 否 | 期望端口（1–65535）；缺省用 binding 的 sticky 端口，再缺省由子进程自选 |
| `wait_ms` | 否 | 就绪等待预算（默认 `8000`，上限 `60000`）；超预算 → `not_running` |
| `detach` | 否 | 只接受 `true`/缺省；`false` → `400`（拉起必须是脱离进程，见 §5.7） |
| `origin` | 否 | 审计标签，默认 `web` |

响应（四态 + §5.9 错误信封，字段见 `ChatWebAPIMeshSpawnResponse`）：

| `status` | HTTP | 何时 |
|----------|------|------|
| `reused` | 200 | 工作区已有活节点（含单飞锁被别人持有时读到的那个）——**不新起进程** |
| `started` | 200 | 本次拉起的进程已就绪（或 `--no-wait` 下已 spawn） |
| `not_running` | 504 | 进程起了但未在预算内就绪（`reason` + 脱敏 `log_tail`） |
| `failed` | 500 | 拉起本身失败（可执行文件缺失、日志目录不可建…） |
| `refused` | 403 | `mesh_disabled`（`--mesh=false`）/ `mesh_spawn_not_allowed`（`--mesh-allow-spawn=false`）/ `mesh_nonloopback_denied` |
| `error` | 400 / 413 | 参数形状错误 / 请求体超 1 MiB |

**硬契约**：

- **令牌唯一出口**：`url` 是 M7 里唯一允许出现令牌原文的字段，形如
  `http://127.0.0.1:<port>/web?token=<tok>&session=<sid>`；其余响应字段、日志、journal 一律脱敏
  （`log_tail` 已过 `redactSpawnTail`）。调用方拿到 `url` 后**立即**交给窗口，不得落
  `localStorage`/`sessionStorage`/DOM。
- **单飞**：先抢 `spawn-<session>` 租约；抢不到 → 直接按对方档案返回 `reused`，并发点击不会起第二个进程。
- **可执行文件**：拉起用的 aicli 二进制按 `AICLI_BIN` → 自身（仅当文件名就叫 `aicli`）→
  同目录 `aicli.exe` → `PATH` 解析；`aicli-mesh doctor` 的 `spawn-executable` 会打印结果与
  来源。`AICLI_BIN` 指错时**不退回**其它候选，直接 `failed` + `mesh_spawn_bin_unavailable`
  （CLI 侧的等价入口是 `aicli-mesh open --bin <路径>`，见 mesh-cli.md §4.10）。
- **仅回环**：与 `/web/api/mesh/call` 同层（`X-AICLI-Token` + 回环），跨机一律拒绝。
- **降级**：网格关闭 → `refused`（不是 5xx），前端据此提示而不是白屏。

前端侧（`web/js/sessions.js` + `web_page.go` 注入的内联脚本）：

- 会话列表悬停 → `⧉`「在新窗口打开」：在**点击手势内同步** `window.open('', '_blank')` 占位
  （否则 fetch 之后的 `window.open` 会被弹窗拦截），`POST /web/api/mesh/spawn` 成功后在占位窗口里
  `location.replace(url)`；`not_running`/`failed`/`refused` → 关闭占位窗口 + Toast（`code — reason`）。
- 深链 `/web?session=<id>&token=<t>`：页面 `<head>` 内联脚本（先于 ES 模块执行，模块顶层的第一个
  fetch 之前）把 `token` 转存 `sessionStorage` 并用 `history.replaceState` 从地址栏抹掉；
  `session` 交给 `applyDeepLinkSession()`——与 `current_session_id` 相同则什么都不做（子进程本就以
  该会话启动），不同才走 `/web/api/sessions/resume`。
- 手工验证步骤见 [web-testing.md](web-testing.md)。

### 9.7 会话列表的网格便捷视图与 resume 归属检查（S11）

**一句话**：`GET /web/api/sessions` 在旧字段之外附带「这个会话归谁 / 在哪个节点 / 什么状态」，
`POST /web/api/sessions/resume` 在目标会话正被**另一个活节点**服务时先拦下——前端侧栏的徽标、
端点行、跨工作区分组与冲突弹窗都只读这两处，不新增第二套聚合（Web 子方案 §0.2 纪律 2）。

#### 会话条目的新增字段

| 字段 | 类型 | 语义 |
|------|------|------|
| `session_state` | string | `running` / `busy` / `idle` / `unknown`：活节点占用 → `running\|busy`；无活节点且网格可用 → `idle`；网格不可用或档案不可读 → `unknown` |
| `ownership` | string | `owner` / `peer` / `conflict` / `none`，与 `peers.nodes[].ownership` 同源同义（§9.3） |
| `conflict_count` | int | 仅 `ownership=conflict` 时非 0：声称该会话的活节点数（侧栏「⚠ 冲突（N 个节点）」） |
| `workspace_path` / `workspace_name` | string | 条目所属工作区；本进程条目取自会话档案，peer 条目取自节点档案 |
| `endpoint` | object \| null | 活节点端点摘要（`node_id` / `base_url` / `web_url` / `loopback` / `auth_required` / `reachability` / `busy` / `heartbeat_at`）；无活节点时恒为 `null` |
| `last_known` | object \| null | 「上次地址」（`host` / `port` / `from`，来自 `mesh/bindings/<session>.json`）：节点已死、端口已换后仍可展示 |

响应顶层新增两段（网格关闭时 `self=null`、`workspaces=[]`）：

| 段 | 语义 |
|----|------|
| `self` | `{node_id, mesh_root, workspace_path, workspace_name, counts}`；`counts` 与 §9.3 同口径 |
| `workspaces[]` | `{path, name, nodes, session_count, running_count}`：`nodes` 复用视图；`session_count` 是本进程清单里绑定到该工作区的会话数；`running_count` 是该工作区「活节点且带会话」的节点数 |

查询参数：

| 参数 | 默认 | 语义 |
|------|------|------|
| `sort` | `created_at` | 不变（`updated_at` 可选） |
| `scope` | `self` | `all` 时额外并入 peers 发现的跨工作区会话，并按 id 去重；合并条目的 `id/title` 取自节点档案 `session` 段、`created_at/updated_at` 用 `activated_at` 顶替（仅展示）、`message_count=0`、`ownership=peer\|conflict` |

**硬契约**：`sessions[].endpoint` 与 `peers.nodes[].endpoint` 同源同形（同一次 `BuildView` 派生）。
**降级**：网格关闭 / 根不可读 → `200` + 旧口径（`endpoint` / `last_known` 全 `null`、`self=null`、
`workspaces=[]`），本进程清单条目本身不变，**绝不 5xx**（MN1）。

```powershell
# 跨工作区视图：主列表 + 其他工作区分组的数据源
Invoke-RestMethod 'http://127.0.0.1:51234/web/api/sessions?scope=all&sort=updated_at' | ConvertTo-Json -Depth 6
```

#### resume 的归属检查（`force` 逃生门）

请求体新增 `force`（bool，缺省 `false`）。未带 `force` 时先做**只读**归属判定（不探测网络）：

| `status` | HTTP | 何时 | 响应附加字段 |
|----------|------|------|--------------|
| `running_elsewhere` | 200 | 恰有 1 个**别的**活节点声称该会话 | `node_id` / `endpoint`（同 §9.3 形状）/ `web_url`（有端点时）/ `workspace` / `takeover_available:true`（S15：接管入口已落地，见下） |
| `conflict` | 200 | ≥2 个活节点声称同一会话（§4.4 冲突） | `nodes[]`：`node_id` / `pid` / `workspace` / `heartbeat_at`，按 `node_id` 升序 |

两者都**不注入队列**；`force=true` 跳过检查直接注入（前端在冲突弹窗里由用户显式选
「仍在本进程切换」时才带；`conflict` 时前端禁用该动作，提示先跑 `aicli-mesh doctor`）。

请求体另可带 `takeover`（bool，缺省 `false`，**S15 落地**）：仅在 `running_elsewhere` 上生效——
显式回收会话租约（网格 §4.4：这是唯一会抢活租约的入口）后按旧语义注入队列，响应
`status=taken_over` + `previous_owner_node_id`。**旧节点不会被杀**：它在下一次心跳发现自己
不再持有租约，把档案的会话段标成 `orphaned` 并提示操作者（`aicli-mesh show <session>`）。
回收失败 → `status=takeover_failed` + `reason`（不注入、HTTP 仍 200）；`conflict`（≥2 节点）
时带 `takeover` 也一律拒绝，且不回收任何租约（§5.7）。CLI 等价入口：`aicli-mesh open <session> --takeover`。

```bash
# 被拦下：换到那个窗口，或显式 force 在本进程切换
curl -s -X POST http://127.0.0.1:51234/web/api/sessions/resume \
  -H 'Content-Type: application/json' -d '{"session_id":"sess-20260924-abc"}'
curl -s -X POST http://127.0.0.1:51234/web/api/sessions/resume \
  -H 'Content-Type: application/json' -d '{"session_id":"sess-20260924-abc","force":true}'
# 显式接管：回收租约并切换（旧窗口继续运行，但会标记为已让渡）
curl -s -X POST http://127.0.0.1:51234/web/api/sessions/resume \
  -H 'Content-Type: application/json' -d '{"session_id":"sess-20260924-abc","takeover":true}'
```

**降级**：网格关闭（`--mesh=false`）/ 网格不可读 / 占用者就是本进程 → 旧语义（`queued`），
与 S11 之前逐字一致（MN1）。

> 前端落点：`web/js/sessions.js`（徽标 / 端点行 / 跨工作区分组 / 打开方式开关 / 冲突弹窗）与
> `web/js/ui.js`（关于页只读网格小节）；手工验证步骤见 [web-testing.md](web-testing.md) §2.7。

### 9.8 停止节点：`POST /web/api/mesh/stop`（治理动作，默认关闭）

**一句话**：把「让那个进程退场」做成一次可审计的网格调用——`graceful` 投 `/exit` 让目标自己
收尾，`force` 直接终止进程（架构 §5.7 / §9.2）。CLI 侧等价物是 `aicli-mesh stop <节点|会话>`
（见 [mesh-cli.md](mesh-cli.md) §4.11），两者共用 `mesh.StopNode`，Web 层不产生第二套语义。

```bash
# 优雅停止：把 /exit 投给目标的 /web/api/input，等它保存会话、注销档案、释放租约
curl -s -X POST http://127.0.0.1:51234/web/api/mesh/stop \
  -H 'Content-Type: application/json' \
  -d '{"target":"node-9001-20260924T073500Z"}'

# 强制停止：目标卡死 / 没有回环控制面（没带 --pprof）时用
curl -s -X POST http://127.0.0.1:51234/web/api/mesh/stop \
  -H 'Content-Type: application/json' \
  -d '{"target":"node-9001-20260924T073500Z","mode":"force","wait_ms":5000}'
```

请求体（1 MiB 上限，超限 `413` + `mesh_body_too_large`）：

| 字段 | 必填 | 语义 |
|------|------|------|
| `target` | 是 | 节点引用：`node_id` / 前缀 / 会话 id / 前缀 / `pid:<PID>`（与 §9.5 同一解析口径） |
| `mode` | 否 | `graceful`（默认）/ `force`；其它取值 → `400` + `mesh_stop_bad_mode` |
| `wait_ms` | 否 | 等待预算（默认 30s，上限 5 分钟） |

状态映射（与 §9.5 同构）：

| `status` | HTTP | 说明 |
|----------|------|------|
| `stopped` | 200 | 目标已不在运行；幂等（重试安全，`code=mesh_stop_already_stopped`） |
| `not_found` | 404 | 目标不存在或已从档案消失 |
| `refused` | 403 | 策略拒绝：网格关闭（`mesh_disabled`）、**开关未开**（`mesh_stop_not_allowed`）、非回环（`mesh_nonloopback_denied`）、自停（`mesh_stop_self_refused`） |
| `timeout` | 504 | `/exit` 已投递 / 信号已发，但进程在 `wait_ms` 内没消失 |
| `error` | 400 / 413 / 500 | 参数形状错误 / 请求体超限 / 终止进程的系统调用失败 |

**硬契约**：

- **默认关闭**：停止是治理动作，目标进程必须显式 `--mesh-allow-stop=true` 才放行，否则一律
  `refused` + `mesh_stop_not_allowed`——「谁能停我」由被停者决定。端点始终注册（未开启也回
  `refused` 而不是 404），调用方读得到原因码；
- **不自杀**：`target` 是本进程，或等于 `X-AICLI-Mesh-Caller` 指名的调用方 → `refused` +
  `mesh_stop_self_refused`（停自己用 `/exit`，不是网格调用）；CLI 不是节点，`caller` 为空；
- **graceful 的真实语义**：借目标档案里的令牌向目标的 `/web/api/input` 投一行 `/exit`
  （`allow_write=true`），随后轮询等进程消失——**判据是进程消失，不是请求成功**；
- **force 无收尾**：`TerminateProcess`（Windows）/ `SIGKILL`（Unix）；残留档案由 `aicli-mesh gc`
  按「可证已死」回收（§4.5）。目标已消失不算错误（幂等）；
- **审计**：调用方与被调方各写一行 journal（`mesh.stop.requested` / `mesh.stop.completed`，
  只记 target / mode / 状态 / 耗时与调用者，**不记令牌**）；
- **仅回环**：与 §9.5 / §9.6 同层（`X-AICLI-Token` + 回环），跨机一律拒绝。

> 前端**不提供**停止按钮：治理动作留在 CLI / 运维面，避免误点（Web 子方案 §5.8 / §8 R13）。
