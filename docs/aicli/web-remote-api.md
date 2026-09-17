# aicli Web 远程调用 API（`/web/api/*`）

> 适用版本：集成微型 Web 客户端之后的 aicli。
> 前提：`aicli chat` / `aicli resume` / `aicli` 以 `--pprof`（或 `--debug`）启动，
> loopback HTTP 服务器已开启。服务器仅监听 `127.0.0.1`，**无鉴权**，不要转发/暴露到网络。

## 1. 入口发现

TUI 启动时会打印会话行：

```text
session_20260917202752_xxoO88dG  endpoints: http://127.0.0.1:61772/debug/endpoints  web: http://127.0.0.1:61772/web/
```

`/debug/endpoints` 清单现在包含 `web` 分组（`/web/*` 远程调用端点族），脚本可只依赖该清单发现全部入口：

```powershell
curl.exe 'http://127.0.0.1:61772/debug/endpoints?format=text'
```

```text
loopback  (aicli --pprof 本机调试服务器)
  ...
web  (aicli 微型 Web 客户端 / 远程调用 API)
  Base: http://127.0.0.1:61772/web
  GET http://127.0.0.1:61772/web/  [enabled]  微型 Web 客户端页面（浏览器交互入口）
  GET http://127.0.0.1:61772/web/api/screen  [enabled]  当前渲染快照（默认完整 transcript；?view=tui TUI 合成帧；?format=json 结构化）
  GET http://127.0.0.1:61772/web/api/status  [enabled]  渲染/显示状态快照（JSON / ?format=text）
  GET http://127.0.0.1:61772/web/api/events  [enabled]  SSE 事件流（实时 turn 事件）
  POST http://127.0.0.1:61772/web/api/input  [enabled]  异步注入 prompt / 审批决议 / 提问回答
  POST http://127.0.0.1:61772/web/api/invoke  [enabled]  同步远程调用：注入 prompt 并等待 turn 结束，返回状态/渲染
  GET http://127.0.0.1:61772/web/api/events/schema  [enabled]  SSE 事件 schema
```

## 2. 端点总览

| 方法 | 路径 | 用途 |
|------|------|------|
| POST | `/web/api/invoke` | **同步远程调用**：注入 prompt，等待 turn 结束，一次响应返回最终状态 + assistant 回复 + TUI 渲染 |
| POST | `/web/api/input` | 异步注入：prompt / 审批决议 / 提问回答 / 中断，立即返回 `queued` |
| GET | `/web/api/screen` | 当前渲染：默认完整 transcript（`messages` 结构化）；`?view=tui` 返回 TUI 合成帧；`?format=json` 结构化 |
| GET | `/web/api/status` | 渲染器/显示状态快照（等价 `/debug/chat/status`） |
| GET | `/web/api/events` | SSE 实时事件流（turn/工具/审批/提问…） |
| GET | `/web/api/events/schema` | SSE 事件类型定义 |
| GET | `/web/` | 浏览器微型客户端页面（同一后端） |
| GET | `/debug/chat/screen` | 与 `/web/api/screen?view=tui` 同源的调试入口（保留） |

## 3. 同步远程调用：`POST /web/api/invoke`

一次请求内完成"发送 prompt → 等待 turn 结束 → 返回状态与渲染"，适合脚本 / 外部 Agent 调用。

请求体（`application/json`）：

```json
{
  "prompt": "把 README 的构建章节更新为当前命令",
  "timeout_ms": 120000
}
```

- `timeout_ms` 可选，默认 120000，钳制范围 `[1000, 600000]`。
- 非 JSON body 时整个 body 视为 prompt 文本（`text/plain` 兼容）。
- 同一时刻只允许一个 invoke 在等待；并发调用返回 `409` + `{"status":"busy"}`。
- 会话正在执行其他 turn 时，新 prompt 会排队，invoke 在当前 turn 与新 turn 全部结束后返回。

示例（bash / Git Bash / WSL）：

```bash
curl -s -X POST http://127.0.0.1:61772/web/api/invoke \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"用一句话总结当前会话状态","timeout_ms":120000}' | jq .
```

示例（PowerShell）：

```powershell
curl.exe -s -X POST http://127.0.0.1:61772/web/api/invoke `
  -H "Content-Type: application/json" `
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

`screen` 与 `/debug/chat/screen`、`/web/api/screen?view=tui` 同源，是"用户当前实际看到的 TUI 界面渲染"（合成帧文本），不是 web 页的完整 transcript。

### status 取值

| status | 含义 | 后续动作 |
|--------|------|----------|
| `completed` | turn 结束，会话空闲 | 读取 `assistant` / `screen` |
| `settled` | 输入已被消费且会话空闲，但未观察到 LLM turn（斜杠命令等） | 读取 `screen` 确认命令输出 |
| `timeout` | 超过 `timeout_ms` 仍未结束（长任务/后台作业） | 用 `/web/api/events` 或轮询 `/web/api/screen` 继续观察，可再次 invoke |
| `interrupted` | turn 被中断（终端 Esc 或 `POST /web/api/input {"type":"interrupt"}`） | 视需要重新发起 |
| `requires_approval` | 会话停在审批等待，`pending_approval` 携带 `request_id` / `tool_name` / `prompt` | `POST /web/api/input {"type":"approval","request_id":"...","allow":true}` 后可再次 invoke 续跑 |
| `requires_answer` | 会话停在提问等待，`pending_question` 携带 `question_id` / `prompt` / `suggestions` | `POST /web/api/input {"type":"question_answer","question_id":"...","answer":"..."}` 后可再次 invoke 续跑 |
| `rejected` | 输入被命令闸门拒绝（如忙时的 `/exit` 等状态变更命令） | 查看 `reason` |
| `error` | 调用方断开 / 会话关闭 / 输入队列不可用 | — |

HTTP 错误码：`400` body 读取失败或空 prompt；`405` 非 POST；`409` 无活动会话或已有 invoke 在等待；`500` 输入队列不可用。

## 4. 异步注入：`POST /web/api/input`

立即返回 `{"status":"queued"}`，后续通过 SSE 或轮询读取：

```bash
# 普通 prompt
curl -s -X POST http://127.0.0.1:61772/web/api/input \
  -H 'Content-Type: application/json' -d '{"prompt":"继续"}'

# 审批 / 提问 / 中断
curl -s -X POST http://127.0.0.1:61772/web/api/input -d '{"type":"approval","request_id":"req_1","allow":true}'
curl -s -X POST http://127.0.0.1:61772/web/api/input -d '{"type":"question_answer","question_id":"q_1","answer":"深色主题"}'
curl -s -X POST http://127.0.0.1:61772/web/api/input -d '{"type":"interrupt"}'
```

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

若 `status=requires_approval`，先回答再调用一次 invoke（第二次 invoke 会等待续跑的 turn 结束）：

```bash
curl -s -X POST http://127.0.0.1:61772/web/api/input \
  -d '{"type":"approval","request_id":"req_1","allow":true}'
curl -s -X POST http://127.0.0.1:61772/web/api/invoke \
  -H 'Content-Type: application/json' -d '{"prompt":"继续","timeout_ms":180000}'
```

> 二次 invoke 的 prompt 会作为新的用户消息注入；如果只想等待当前 turn 结束，请改用 `/web/api/events` 或轮询 `/web/api/screen`。

## 8. 限制与安全

- 仅 loopback（`127.0.0.1`）监听、无鉴权：任何能访问本机该端口的进程都可控制会话；不要做端口转发/公网暴露。
- 请求体上限 1 MiB；`timeout_ms` 钳制到 `[1000, 600000]`。
- `/web/api/invoke` 单飞（single-flight）：并发 invoke 返回 409，避免多个远程调用方互相等待。
- invoke 等待期间不持有输入互斥锁，审批/提问仍可通过 `/web/api/input` 或 TUI 本机操作回答。
- SSE 与 invoke 均复用当前活动会话；无活动会话时返回 409 / `available=false`。
