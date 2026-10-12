# 03 · 注册与反向隧道协议

> 上游：[02-architecture.md](./02-architecture.md)｜相关：[04-security.md](./04-security.md)
> 本文定义 agent ↔ gateway 的线上协议（wire protocol）。协议定义位于 `internal/gateway/wire`，
> 服务端与 agent **共用同一份实现**，禁止两端各写一套。

---

## 1. 传输层

| 项 | 决策 |
|----|------|
| 传输 | WebSocket over TLS（`wss://`），路径 `GET /gw/agent` |
| 帧类型 | 二进制帧（`websocket.BinaryMessage`）；每条 WS 消息 = 一个协议帧 |
| 压缩 | WebSocket `permessage-deflate`（可协商；弱网/移动端建议开启） |
| 认证 | 升级请求携带 `Authorization: Bearer <device-token>`（见 04 文档） |
| 子协议 | 升级时声明 `Sec-WebSocket-Protocol: aicli-gateway.v1`，服务端回选同一值 |
| 保活 | 应用层 `PING`/`PONG` 帧（每 20s）+ WebSocket ping（可选） |
| 关闭 | `GOAWAY` 帧 + 正常 WS Close；`1001` 表示网关重启 |

**为什么用 WebSocket 而不是 HTTP/2 server push / QUIC**：

- 反向方向（网关→agent）需要**服务端发起**的请求；HTTP/2 服务器无法任意发起请求。
- WebSocket 全双工、单连接、穿透 NAT/企业代理成熟；`gorilla/websocket` 已是现有依赖。
- QUIC/HTTP3 的**连接迁移**对"agent 所在笔记本切换网络"有价值，列入 Phase 3 可选传输（协议帧不变，仅换承载）。

---

## 2. 帧格式

所有帧使用统一 12 字节头 + 负载（小端）：

```text
 0        1        2        3        4        5        6        7
+--------+--------+--------+--------+--------+--------+--------+--------+
| ver(1) | type(1)| flags(2)|            stream_id(4)   |  payload_len(4) |
+--------+--------+--------+--------+--------+--------+--------+--------+
|                            payload (payload_len bytes)                  |
+-------------------------------------------------------------------------+
```

| 字段 | 字节 | 说明 |
|------|------|------|
| `ver` | 1 | 协议主版本，当前 `1`；不匹配由 `HELLO/WELCOME` 协商 |
| `type` | 1 | 帧类型，见 §2.1 |
| `flags` | 2 | 位标志：`FIN=0x0001`、`COMPRESSED=0x0002`、`JSON=0x0004`、`MORE=0x0008` |
| `stream_id` | 4 | 逻辑流 ID；`0` 保留给连接级控制帧 |
| `payload_len` | 4 | 负载长度（最大 `max_frame`，默认 1MiB，可协商） |
| `payload` | var | 控制帧为 UTF-8 JSON；数据帧为原始字节 |

### 2.1 帧类型

| type | 名称 | 方向 | stream_id | 负载 |
|------|------|------|-----------|------|
| 0x01 | `HELLO` | A→G | 0 | JSON：`{protocol_version, agent_version, node_id?, resume?}` |
| 0x02 | `WELCOME` | G→A | 0 | JSON：`{server_version, heartbeat_interval_sec, max_frame, capabilities[]}` |
| 0x03 | `REGISTER` | A→G | 0 | JSON：节点元数据 + 路由白名单 + 会话列表（§3.2） |
| 0x04 | `REGISTER_ACK` | G→A | 0 | JSON：`{node_id, console_url, accepted_routes[], rejected_routes[]}` |
| 0x05 | `HEARTBEAT` | A→G | 0 | JSON：`{sessions_digest, load, ts}` |
| 0x06 | `HEARTBEAT_ACK` | G→A | 0 | JSON：`{server_time, pending[]}` |
| 0x07 | `SESSION_UPDATE` | A→G | 0 | JSON：会话增量（新增/更新/移除） |
| 0x08 | `PING` | 双向 | 0 | 空或 JSON |
| 0x09 | `PONG` | 双向 | 0 | 空或 JSON |
| 0x0A | `STREAM_OPEN` | G→A | >0 | JSON：`{method, path, headers, body_len?}` |
| 0x0B | `STREAM_DATA` | 双向 | >0 | 原始字节（`MORE` 表示后续还有） |
| 0x0C | `STREAM_END` | 双向 | >0 | 空（半关闭） |
| 0x0D | `STREAM_RESET` | 双向 | >0 | JSON：`{code, message}` |
| 0x0E | `WINDOW_UPDATE` | 双向 | >0 或 0 | 4 字节 uint32 增量（流控信用） |
| 0x0F | `GOAWAY` | 双向 | 0 | JSON：`{code, message, last_stream_id}` |
| 0x10 | `DEREGISTER` | A→G | 0 | JSON：`{reason}` |
| 0x11 | `ERROR` | 双向 | 0 | JSON：`{code, message}` |
| 0x12 | `NOTIFY` | A→G | 0 | JSON：瞬时通知（turn 完成/审批/提问/出错/等待），**只带元数据、不含正文**，见 [08](./08-notifications.md) §4 |

> 说明：`STREAM_*` 的请求方向是 **G→A**（网关发起，agent 转发到本机），
> 响应方向是 **A→G**。SSE 场景下响应流长期不 `END`，持续 `STREAM_DATA`。

### 2.2 流 ID 约定

- `0`：连接级控制。
- 网关发起的新流使用**奇数**递增 ID（`1,3,5,…`）。
- 保留偶数给未来 agent 主动发起（如本地主动上报大对象）。
- 流关闭后 ID 不复用；长时间运行需考虑 32 位回绕（回绕前用 `GOAWAY` 优雅重启连接，或改用 64 位扩展帧——首期以 32 位 + 重启足够）。

---

## 3. 控制消息定义

### 3.1 HELLO / WELCOME

```json
// A→G
{ "protocol_version": 1, "agent_version": "1.2.3", "node_id": "node-aicli-9f3a", "resume": false }

// G→A
{ "server_version": "1.0.0", "heartbeat_interval_sec": 30, "max_frame": 1048576,
  "capabilities": ["resume", "flow_control", "permessage_deflate"] }
```

**版本协商规则**：`protocol_version` 主版本不一致 → 网关回 `ERROR{code:"protocol_version_unsupported"}`
并关闭，agent 打印明确升级提示（不得静默降级）。

### 3.2 REGISTER（节点元数据 + 会话列表）

```json
{
  "node_id": "node-aicli-9f3a",
  "kind": "aicli",
  "name": "vince-laptop · aicli",
  "device_name": "vince-laptop",
  "version": "1.2.3",
  "build_time": "2026-10-01T00:00:00Z",
  "os": "windows", "arch": "amd64",
  "capabilities": ["manifest","screen","status","events","invoke","input","sessions","files","git","mcp","skills"],
  "workspace": { "path": "E:/projects/ai/ai-agent-runtime", "name": "ai-agent-runtime" },
  "routes": ["/web", "/web/api", "/debug/endpoints"],
  "local": { "base_url": "http://127.0.0.1:61772", "auth_mode": "token" },
  "sessions": [
    { "session_id": "session_20261011_...", "title": "网关设计", "state": "busy",
      "attention": "approval", "busy": true, "turn_id": "turn_...",
      "provider": "anthropic", "model": "claude-sonnet-4",
      "workspace": { "path": "E:/projects/ai/ai-agent-runtime", "name": "ai-agent-runtime" },
      "updated_at": "2026-10-11T10:00:00Z", "preview": "…最后一条消息预览…" }
  ]
}
```

> `local.base_url` 与 `local.auth_mode` 属于**本地私密信息**：`base_url` 是回环地址，`auth_mode`
> 只说明 agent 如何注入令牌，**不包含令牌原文**。网关不下发这些字段给浏览器。

### 3.3 REGISTER_ACK

```json
{ "node_id": "node-aicli-9f3a",
  "console_url": "https://gw.example.com/n/node-aicli-9f3a/web/",
  "accepted_routes": ["/web", "/web/api", "/debug/endpoints"],
  "rejected_routes": [] }
```

网关可裁剪/拒绝超范围路由（例如声明了未实现的能力位）。

### 3.4 HEARTBEAT 与 SESSION_UPDATE

```json
// HEARTBEAT（周期；sessions_digest 是轻量摘要，不是全量）
{ "ts": "2026-10-11T10:00:30Z", "load": { "active_streams": 2 },
  "sessions_digest": [ {"session_id":"…","state":"running","attention":"","status_text":"Running shell","updated_at":"…"} ] }

// SESSION_UPDATE（事件驱动增量）
{ "op": "upsert", "session": { "session_id":"…", "state":"idle", "attention":"completed", "…":"…" } }
{ "op": "remove", "session_id": "…" }
{ "op": "reconcile", "sessions": [ /* 全量 */ ] }
```

**对账原则**（借鉴 mesh）：心跳是"我在"的证据；会话状态以 `SESSION_UPDATE` 增量为主
（状态迁移即推，不做定时全量），心跳携带的 `sessions_digest` 只是**轻量对账网**：
agent 侧每个心跳都会带上最新的 state/attention/status_text 摘要，网关仅在摘要与
registry 实际不同时才合并、广播并持久化（无变化的心跳零副作用），这样即使某个
`SESSION_UPDATE` 丢失，状态也会在下一次心跳内收敛，同时不产生全量数据同步。

> 状态语义（state/attention/status_text 的取值与耦合规则）由共享核心
> `internal/gateway/agent.SessionStateStore` 统一实现：aicli TUI 与 runtime-server
> 只在各自侧做"本地事件 → 核心调用"的映射，保证两端词表与迁移规则不漂移。

**时间戳语义**：`updated_at` 是宿主侧内容时间；`synced_at` 由**网关盖章**
（网关最后收到该会话任何帧——SESSION_UPDATE 或心跳 digest——的服务器时间），
agent 上报的同名字段会被覆盖，保证始终是服务器时钟。控制台用二者渲染
"内容 x 前 / 同步 x 前"两条时间轴（`synced_at` 以心跳周期为常态上限，掉线后
自然增大，是上传链路的活性信号）。

**实时输出尾段（P1）**：`status_tail` 是活动期内容的展示尾段（≤512 runes，由共享核心
`agent.TruncateSessionTail` 统一截断、保留最新），经 `SESSION_UPDATE` 以 ≤1Hz 节流上报
（无变化不推）；网关以 **`event: session` 单会话增量**（`{node_id, session}`）下发给控制台，
避免 1Hz × 全量快照的放大。turn 结束/中断时清空；完整正文仍只在会话页（07-console §6）。

**尾流门控（P1-b）**：控制台对"展开的卡片"上报 `POST /api/presence {node_id, session_id, tail:true}`
（收起/关闭开关即转为 `tail:false`）；网关把 `tail=true` 且会话活跃的集合按连接做差集，随
`HEARTBEAT_ACK.pending` 下发 `tail-gating:on`（首次通告参与）、`watch:<session_id>` / `unwatch:<session_id>`。
agent 在收到 `tail-gating:on` 之前保持 **P1-a 的无门控行为**（始终推尾流），之后仅推 watch 集合内的会话；
旧 agent 不消费 pending ⇒ 始终推尾流（安全降级，无行为破坏）。

### 3.5 通知（NOTIFY）与注意力（attention）

- **状态**（持久）：`attention` 随 `SESSION_UPDATE` 上报/对账，控制台据此重建徽标与收件箱；
- **事件**（瞬时）：`NOTIFY` 承载"刚刚发生了什么"，用于 toast / Web Push，带 `dedup_key` 去重；
- 两者由 agent **订阅本地运行时事件总线**派生（`agent.turn.finished` / `approval_requested` /
  `question_asked` / `turn.suspended` / 失败等），映射表与网关侧行为见
  [08-notifications.md](./08-notifications.md) §3–§5。
- `NOTIFY` **不含正文**（与"网关不落会话正文"一致）；`attention` 取值：
  `none` / `approval` / `question` / `completed` / `waiting` / `error`。

---

## 4. 反向请求/响应流

### 4.1 请求

```text
G→A  STREAM_OPEN(stream=1)  { "method":"GET",
                              "path":"/web/api/screen?view=tui",
                              "headers": { "Accept":"application/json",
                                           "X-Forwarded-For":"203.0.113.7" },
                              "body_len": 0 }
G→A  STREAM_END(stream=1)   （无 body，立即半关闭）
```

网关在 `headers` 中**只透传安全白名单头**（`Accept`、`Content-Type`、`Range`、`If-*`、`Accept-Encoding`、
`Last-Event-ID` 等），**剥离** hop-by-hop 头与网关内部头；`Cookie` 不下发（浏览器 Cookie 是 owner 会话，
属于网关，不转发给本地）。`X-Forwarded-*` 可选注入供本地审计，默认关闭。

### 4.2 响应

```text
A→G  STREAM_DATA(stream=1)  <response headers JSON? 见下> 
```

响应头需要一个明确的承载方式。两种方案：

- **方案 A（推荐）**：`STREAM_OPEN` 后 agent 先回一个 `STREAM_DATA`，其 payload 是 JSON
  `{ "status":200, "headers":{...} }`，`flags.JSON=1`；随后是原始 body 分块（`flags.JSON=0`）。
- **方案 B**：新增帧类型 `STREAM_RESPONSE_HEADERS`。为减少帧类型，首期用方案 A。

约定：**每个流的第一条 A→G `STREAM_DATA` 必为响应头 JSON**，后续为 body 分块。

### 4.3 半关闭与多路复用

- 请求方发 `STREAM_END` 表示 body 发送完毕（半关闭）；响应方仍可继续发 `STREAM_DATA`。
- 双向都 `END` 后流关闭。
- 任一方向发 `STREAM_RESET{code}` 立即中止该流，不影响其它流。

### 4.4 SSE 保真

- `GET /web/api/events`（或 runtime 事件流）被识别为 SSE 后：
  - 网关立即回响应头（`Content-Type: text/event-stream`、`Cache-Control: no-cache`、
    `X-Accel-Buffering: no`、`Connection: keep-alive`）。
  - agent 持续 `STREAM_DATA` 推送原始 SSE 字节（不做解析，保持 `event:`/`data:`/`id:` 原样）。
  - 断线时网关向下游发注释行或关闭；浏览器按 `Last-Event-ID` 重连 → 网关把该头透传给 agent →
    本地按 `since_seq` 续传（现有 `/web/api/events?since_seq=` 与 `Last-Event-ID` 语义，见 web-remote-api.md §9.4）。
- **合并与节流**：网关可对高频 `STREAM_DATA` 做有界合并（见 05 文档），但**不得改变 SSE 语义**
  （不能把两条 `event:` 合并成一条；只能延迟 flush 或合并同一 event 的连续 data 行）。

### 4.5 下载与上传保真

| 场景 | 关键点 |
|------|--------|
| 下载 | 透传 `Content-Disposition` / `Content-Type` / `Content-Length`；支持 `Range`（请求头透传，响应 `206` 与 `Content-Range` 回流）；`HEAD` 无 body |
| 大下载 | 以 `STREAM_DATA` 分块回流，受 `WINDOW_UPDATE` 信用控制；不在网关整包缓冲 |
| 上传 | 请求 body 分块 `STREAM_DATA`（G→A），`Content-Type: multipart/form-data` 透传；`MORE` 标志表示还有后续 |
| 中断 | 浏览器断开 → 网关 `STREAM_RESET{code:"client_gone"}` → agent 取消本机请求 |

---

## 5. 流控与背压

- **信用窗口**：每条流初始窗口 `initial_window`（默认 1MiB，可协商）。发送方每发 `n` 字节，
  可用信用减 `n`；接收方消费后发 `WINDOW_UPDATE{delta=n}`。
- 信用耗尽 → 发送方暂停该流，其它流不受影响（多路复用隔离）。
- 网关对**慢消费者**（浏览器读得慢）的策略：
  1. 先背压到 agent（不 `WINDOW_UPDATE`），让本机也慢下来（最保真）；
  2. 超时后对**可丢弃流**（如高频屏幕快照）改为"只保留最新"（丢旧帧）；
  3. SSE 事件流**不可丢弃**（用 `Last-Event-ID` 续传兜底），必要时只做 flush 节流。
- 单连接总缓冲上限（默认 8MiB）与单流缓冲上限（默认 256KB）均可配；超限按 §5 策略处理。

---

## 6. 重连与续传（resume）

```text
隧道断开（网络抖动 / 网关重启）
  ├─ agent：指数退避（1s,2s,4s,… 上限 30s）+ 抖动 ±20% 重连
  ├─ HELLO 携带 { node_id, resume: true }
  ├─ 网关：同 node_id 已有旧连接 → 关闭旧连接（fencing），接受新连接
  ├─ REGISTER：agent 发送最新会话全量（相当于 reconcile）
  └─ 浏览器侧：SSE 按 Last-Event-ID 重连；普通请求由前端重试或用户重试

网关重启（GOAWAY code=restart）
  └─ agent 收到后立即重连（短退避），不视为错误
```

**resume 语义**：已建立的流**不跨连接恢复**（流是内存态）；断连时所有在途流 `STREAM_RESET`。
恢复的是"节点身份 + 会话状态"，不是"单条流的字节续传"。SSE 的续传靠事件 ID，不靠流级续传。

---

## 7. 错误码

| code | 含义 | 处置 |
|------|------|------|
| `protocol_version_unsupported` | 主版本不匹配 | 关闭连接；agent 提示升级 |
| `auth_required` | 缺少设备令牌 | 关闭；agent 提示配置令牌 |
| `auth_invalid` | 令牌无效/已撤销 | 关闭；**不重试**（避免风暴），提示重新签发 |
| `node_limit_exceeded` | 超过最大节点数 | 关闭；提示 |
| `stream_limit_exceeded` | 超过单节点最大流数 | 该流 `STREAM_RESET` |
| `route_not_allowed` | 请求路径不在白名单 | `STREAM_RESET`（HTTP 403） |
| `capability_not_granted` | 能力位未声明 | `STREAM_RESET`（HTTP 403） |
| `upstream_unreachable` | agent 无法连到本机回环服务 | 响应 502 |
| `upstream_timeout` | 本机响应超时 | 响应 504 |
| `frame_too_large` | 帧超 `max_frame` | 关闭连接（防内存攻击） |
| `internal_error` | 其它 | 记录并返回 500 |

---

## 8. 协议演进

- `ver` 主版本不兼容变更；次版本通过 `WELCOME.capabilities[]` 声明可选能力，向后兼容。
- 新增能力示例：`resume`（§6）、`flow_control`（§5）、`delta_screen`（05 文档）、`web_push`。
- agent 必须忽略不认识的 `capabilities` 项（前向兼容）。
- 协议定义集中在 `internal/gateway/wire`，用 golden 测试固定线上字节（借鉴 mesh 的 schema/golden 思路）。
