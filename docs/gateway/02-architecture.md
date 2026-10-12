# 02 · 总体架构与数据模型

> 上游：[01-requirements.md](./01-requirements.md)｜下游：[03-protocol.md](./03-protocol.md)
> 本文给出组件划分、部署拓扑、注册表数据模型、请求路由与完整生命周期。

---

## 1. 组件划分

```text
backend/
├── cmd/
│   └── gateway/                     # 新增：aicli-gateway 二进制（部署到公网）
├── internal/
│   └── gateway/                     # 新增：网关核心
│       ├── registry/                #   节点/会话注册表（内存 + SQLite 持久化）
│       ├── tunnel/                  #   反向隧道服务端：握手、多路复用、流调度、fencing
│       ├── proxy/                   #   反向代理：HTTP/SSE/下载/上传 → 隧道流
│       ├── auth/                    #   owner 登录、设备令牌、会话 Cookie
│       ├── console/                 #   轻量独立控制台（//go:embed console/，零构建，见 07 文档）
│       ├── edge/                    #   公网边缘防护：静态 deny/IP 限流/惩罚封禁/过载（见 09 文档）
│       ├── notify/                  #   通知聚合：attention 状态 + NOTIFY 事件 + Web Push（见 08 文档）
│       └── audit/                   #   审计事件
├── internal/gateway/agent/          # 新增：客户端接入器（被 aicli / runtime-server 复用）
│   ├── client.go                    #   出站连接、握手、心跳、重连
│   ├── mux.go                       #   帧多路复用（与 tunnel 共享 wire 定义）
│   ├── forwarder.go                 #   把隧道流转发到本机回环服务
│   ├── discover.go                  #   枚举本机会话、能力、路由白名单
│   ├── events.go                    #   订阅本地运行时事件总线 → attention + NOTIFY（见 08 文档）
│   └── control.go                   #   连接控制面：Connect/Disconnect/Reconnect/Status（见 12 文档）
└── internal/gateway/wire/           # 新增：协议帧/控制消息的共享定义（server 与 agent 共用）
```

**为什么共享 `wire` 包**：与 `internal/mesh` 的"同源聚合"原则一致——服务端与 agent 必须使用同一份
帧定义与控制消息结构，避免两端各写一套而漂移。

### 1.1 复用既有资产

| 复用对象 | 用途 |
|----------|------|
| `gorilla/websocket` | 隧道传输（已是依赖） |
| `gorilla/mux` | 网关 HTTP 路由（与 runtime-server 同构） |
| `internal/mesh`（`NodeRecord` / `SessionInfo` / `LivenessInfo` / `HasCapability`） | 注册信息字段与能力位的**语义参考**，保持词汇一致 |
| `internal/webui` | 控制台 SPA 的静态资源嵌入方式（`Available()` + `Handler()`） |
| 微前端 `/web/api/*` | 反向代理的主要目标与移动端会话视图 |
| runtime-server `/api/runtime/*` | 反向代理的第二个目标（完整 Web 控制台） |

> 注意：复用是**语义与模式**层面；`internal/gateway` 不 import `internal/mesh` 的文件系统实现
> （mesh 是单机文件系统真相，网关是网络注册表，两者真相来源不同）。

---

## 2. 部署拓扑

### 2.1 单实例（Phase 0–2 推荐）

```text
                        ┌─────────────────────────────────────────────┐
   公网                 │            aicli-gateway (单进程)             │
                        │                                             │
  浏览器/移动 ── HTTPS ─▶ :443                                        │
     │                  │  ├─ /            控制台 SPA                 │
     │                  │  ├─ /api/*       管理 API（owner cookie）    │
     │                  │  ├─ /gw/agent    WebSocket 隧道入口          │
     │                  │  ├─ /n/{node}/…  按节点反向代理              │
     │                  │  └─ /s/{sess}/…  按会话反向代理              │
     │                  │                                             │
     │                  │  registry (内存) + SQLite(gateway-registry.db) │
     │                  └─────────────────────────────────────────────┘
     │                                    ▲
     │                                    │ wss（出站，穿透 NAT）
     │                          ┌─────────┴──────────┐
     │                          │  device A           │  device B
     │                          │  aicli TUI + agent  │  runtime-server + agent
     │                          │  127.0.0.1:PORT     │  127.0.0.1:8101
     │                          └────────────────────┘
```

TLS 终止建议：

- **方式 A（推荐）**：网关自身监听 `:443` 并加载证书（`--tls-cert` / `--tls-key`），或监听 `:8080` 由前置反代 / LB 终止 TLS。
- **方式 B**：Caddy/Nginx 终止 TLS 后反代到网关明文端口（仅内网），网关信任 `X-Forwarded-*`（需配置可信代理）。
- 无论哪种，浏览器与 agent 都必须走 `https/wss`。

### 2.2 高可用（Phase 3，可选）

隧道是**有状态**的（每条隧道绑定到一个网关实例）。HA 方案：

- 网关实例**无状态化**注册表（写外部共享存储：SQLite on 共享盘 / PostgreSQL / Redis）；
- 用一致性哈希或"会话→实例"映射表把某 node 的请求路由到持有其隧道的实例；
- 或使用支持长连接粘滞的 LB（`ip_hash` / `sticky`）。

Phase 3 再评估，本文其余部分以单实例为主。

---

## 3. 注册表数据模型

网关注册表分两层：**Node（设备/进程）** 与 **Session**。持久化到 SQLite（默认
`<--data-dir>/gateway-registry.db`，`--data-dir` 默认 `./data`；刻意与 usage ledger 的
`usage_ledger.db` 区分，见 §8.1），**只存元数据与审计，不存会话正文**。

### 3.1 Node（节点）

| 字段 | 类型 | 说明 |
|------|------|------|
| `node_id` | string | 稳定 ID；建议 `node-<kind>-<machine-hash>-<pid?>` 或首次生成后持久化 |
| `kind` | enum | `aicli` / `runtime-server` |
| `name` | string | 人类可读名（默认主机名 + kind） |
| `device_name` | string | 设备名（同一机器多进程共享） |
| `version` / `build_time` | string | 构建身份，用于版本协商与排障 |
| `os` / `arch` | string | 平台信息 |
| `capabilities` | []string | 能力位，见 §3.3 |
| `workspace` | {path,name} | 仅作分组/展示维度，**不是权限边界**（与 mesh 一致） |
| `routes` | []string | 允许反向访问的本地路径前缀白名单 |
| `state` | enum | `online` / `offline`（由心跳与连接推导，非持久真相） |
| `last_seen` | time | 最近心跳 |
| `connected_at` | time | 本次连接建立时间 |
| `tunnel_id` | string | 当前隧道标识，用于 fencing（新连接接管旧连接） |

### 3.2 Session（会话）

| 字段 | 类型 | 说明 |
|------|------|------|
| `session_id` | string | 会话 ID（沿用本地 `session_*` 命名） |
| `node_id` | string | 归属节点 |
| `title` | string | 标题 |
| `state` | enum | `idle` / `running` / `busy` / `error` / `unknown` |
| `attention` | enum | `none` / `approval` / `question` / `completed` / `error` |
| `busy` | bool | 是否正在跑一轮（单飞） |
| `turn_id` | string | 当前轮次（可选） |
| `preview` | string | 最后一条消息预览（截断，**不含敏感正文**，由本地决定是否提供） |
| `provider` / `model` | string | 当前 provider/model（可选，用于展示） |
| `workspace` | {path,name} | 会话工作目录 |
| `updated_at` | time | 最后活跃 |

### 3.3 能力位（capabilities）

沿用 `internal/mesh` 的能力位思路（能力位只影响"能否调用"，不做隐藏）：

| 能力位 | 含义 |
|--------|------|
| `manifest` | 可提供端点清单 / 版本信息 |
| `screen` | 可读屏幕快照 |
| `status` | 可读运行状态 |
| `events` | 可订阅 SSE 事件流 |
| `invoke` | 可同步注入 prompt |
| `input` | 可异步注入输入 / 审批 / 中断 |
| `sessions` | 可列出 / 新建 / 恢复会话 |
| `files` | 可反向访问文件浏览 / 下载 |
| `git` | 可反向访问 Git 面板 |
| `mcp` / `skills` | 可反向访问 MCP / 技能管理 |
| `runtime-api` | runtime-server 的 `/api/runtime/*` 全量能力 |

网关据能力位**裁剪反向代理路由**：例如设备未声明 `files`，则 `/n/{node}/web/api/fs/*` 直接 403。

### 3.4 注册表一致性规则

- 一个 `session_id` 同一时刻只应归属一个 `node_id`；出现双归属时标记 `conflict` 并在控制台暴露（借鉴 mesh 的显式冲突）。
- `online` 由"连接存活 **且** 心跳新鲜"共同判定（借鉴 mesh 判活：不能只看心跳，也不能只看连接）。
- 心跳超时（默认 TTL 90s，与 mesh 默认一致）→ 标记 `offline`，保留元数据供控制台展示"最近离线"。
- 连接被新隧道接管（同 `node_id` 重连）→ 旧连接进入 draining 并关闭（fencing）。

---

## 4. 请求路由模型

### 4.1 外部路由表

| 路径 | 认证 | 说明 |
|------|------|------|
| `GET /` | 无（返回登录页） | 控制台 SPA |
| `POST /api/auth/login` | 无（限流） | owner 登录，签发 HttpOnly Cookie |
| `POST /api/auth/logout` | Cookie | 退出 |
| `GET /api/nodes` | Cookie | 设备列表 |
| `GET /api/sessions` | Cookie | 聚合会话列表（过滤/分页） |
| `GET /api/devices` / `POST` / `DELETE` | Cookie | 设备令牌管理 |
| `GET /api/audit` | Cookie | 审计事件 |
| `GET /api/health` | 无 | 存活探针（不含敏感信息） |
| `GET /api/security/status` | Cookie | 安全状态（enabled/mode/限流/惩罚/过载/`risks`，见 [09](./09-edge-hardening.md)） |
| `POST /api/security/simulate` | Cookie | 安全策略 dry-run（不改运行态） |
| `GET /api/security/events` | Cookie | 最近安全事件（block/ban/unban/rate_limited…） |
| `GET/POST/DELETE /api/security/bans` | Cookie | 封禁查询 / 手动封禁 / 解除（需 `reason`） |
| `GET/POST /api/security/policy` | Cookie | 安全策略查看 / 设置（需 `reason`，支持 `monitor`/`enforce`） |
| `POST /api/security/policy/rollback` | Cookie | 安全策略回滚 |
| `GET /gw/agent` | 设备令牌 | WebSocket 升级（隧道入口） |
| `/n/{node_id}/…` | Cookie | 按节点反向代理（路径白名单 + 能力裁剪） |
| `/s/{session_id}/…` | Cookie | 按会话反向代理（解析 node 后同上，并可注入会话选择） |

### 4.2 反向代理路径映射

以微前端为例：

```text
浏览器请求        GET /n/{node}/web/api/screen?view=tui
     │
     ▼  网关剥掉 /n/{node} 前缀，按 node 的能力与 routes 白名单校验
隧道流 HEADERS    GET /web/api/screen?view=tui   Host: 127.0.0.1:<local-port>
     │
     ▼  agent 转发到本机回环服务，注入 X-AICLI-Token（若需要）
本机响应          200 + JSON
     │
     ▼  原样经隧道回流，网关写回浏览器
```

要点：

- **前缀剥离**：网关把 `/n/{node_id}` 剥离后，本地看到的路径与直连一致（`/web/...`、`/api/runtime/...`），
  最大化复用现有路由，不要求本地服务感知网关。
- **绝对路径问题**：微前端页面内若有根绝对路径（`/web/api/...`），经前缀代理会指回网关根而丢失 node 前缀。
  解决优先级：
  1. **首选**：改造微前端为 **base-path 感知**（以 `document.baseURI` / 注入的 `<base href>` 构造请求），
     见 [06](./06-implementation-plan.md) §3；
  2. **兜底**：网关做响应体重写（仅限 HTML/JSON 中明确的根路径引用），有性能与正确性成本，默认关闭。
- **会话路由**：`/s/{session_id}/…` 先解析 `node_id`，再按 §4.2 转发；可选在转发头注入
  `X-Gateway-Session: <id>`，供本地"按会话"路由（本地已有 `?session=` 深链语义，见 web-remote-api.md §9.6）。

### 4.3 子域名备选（隔离更强）

可选方案：每个节点一个子域 `https://{node-slug}.gw.example.com/`，本地路径在根，无前缀剥离问题，
Cookie 天然按域隔离。代价是 DNS 通配符与证书。作为 Phase 2+ 可选增强，不作首期依赖。

---

## 5. 完整生命周期

### 5.1 节点从启动到退出

```text
本地进程启动
  ├─ 启动回环服务（aicli: --web/--web-port；runtime-server: 默认 8101）
  ├─ 若配置了 gateway：启动 agent
  │     ├─ 连接 wss://gateway/gw/agent（设备令牌）
  │     ├─ 发送 REGISTER（元数据 + 路由白名单 + 会话列表）
  │     ├─ 收到 REGISTER_ACK（分配/确认 node_id，返回控制台访问地址）
  │     ├─ 周期 HEARTBEAT（含会话状态增量）
  │     └─ 收到 STREAM_OPEN → 转发本机回环 → 回流响应
  └─ 正常运行（网关不可用时自动降级，不影响本地）

会话事件（新建/切换/结束/进入审批）→ agent 推送 SESSION_UPDATE

进程退出
  ├─ 尽力发送 DEREGISTER + GOAWAY
  └─ 关闭连接；网关侧超时判离线
```

### 5.2 一次外部访问的完整链路

```text
1. 移动端打开 https://gw.example.com/（PWA）
2. 登录 → GET /api/sessions → 渲染会话列表（含注意力徽标）
3. 点开会话 → 打开 /s/{session_id}/web/（或 /n/{node}/web/）
4. 微前端发起 GET /s/{session_id}/web/api/screen
5. 网关：校验 Cookie → 解析 session→node → 校验能力/白名单 → 打开隧道流
6. agent：剥网关头 → 转发 127.0.0.1:<port> + 注入本机令牌 → 回流
7. 微前端建立 GET /s/{session_id}/web/api/events（SSE）→ 网关流式转发
8. 用户在移动端发消息 → POST /s/{session_id}/web/api/invoke（或 input）
9. 本地会话执行 → SSE 增量回流 → 移动端实时更新
10. 会话等待审批 → agent 推 SESSION_UPDATE(attention=approval) → 控制台徽标 + 推送
11. 用户在移动端批准 → POST .../web/api/input → 本地继续
12. 用户切换会话 → 返回步骤 3，选择另一 session
```

### 5.3 连接控制面（手动连接 / 断开）

除"启动时按配置自动注册"外，提供**手动控制**（详见 [12-connection-control.md](./12-connection-control.md)）：

- **aicli TUI**：`/gateway [status|connect|disconnect|reconnect|url|enable|disable]`；
- **runtime-server**：本地控制 API `/api/runtime/gateway/{status,connect,disconnect,reconnect,config}` +
  前端设置页 "Gateway"；
- **共享接口**：`internal/gateway/agent.Controller`（`Connect`/`Disconnect`/`Reconnect`/`Status`）。

**语义**：`disconnect` 是**运行态**（置 `operator_disconnected`，抑制自动重连，**不改配置**）；
`enable`/`disable` 才写配置影响下次启动。控制面仅本机 owner 可触发（TUI / 回环或 admin token），
网关侧**没有**反向强制连接的入口。

---

## 6. 与 `aicli-mesh` 的关系

| 维度 | `aicli-mesh` | `aicli-gateway` |
|------|--------------|-----------------|
| 范围 | 单机多进程 | 广域网多设备 |
| 真相来源 | 文件系统（nodes/bindings/leases/journal） | 网络注册表（内存 + SQLite） |
| 连接 | 回环 HTTP 控制面 | 出站 WebSocket 隧道 |
| 治理 | 租约 / 接管 / gc | 连接接管（fencing）/ 令牌撤销 |
| 关系 | **互补**：gateway 可选地通过 mesh 视图枚举"本机所有 aicli 会话"，把一台机器的多进程会话一起注册 | |

**可选增强**：agent 在 `aicli` 侧可读取 `internal/mesh` 的本地视图，把同机其它进程的会话一并
作为"可接入会话"上报（需用户显式开启，且仅上报元数据）。这样"一台机器多开 aicli 窗口"能在
网关控制台统一呈现，而不必每个窗口各自连一次网关（减少连接数）。

---

## 7. 关键时序（握手与接管）

```text
Agent                                   Gateway
  │  ── WS upgrade (Authorization: Bearer <device-token>) ──▶  │
  │  ◀── 101 Switching Protocols ────────────────────────────  │
  │  ── HELLO {protocol_version, node_id?} ──────────────────▶  │
  │  ◀── WELCOME {server_version, heartbeat_interval} ───────   │
  │  ── REGISTER {meta, routes, sessions[]} ─────────────────▶  │
  │  ◀── REGISTER_ACK {node_id, console_url, accepted_routes}  │
  │  ── HEARTBEAT {sessions_digest} ─────────────────────────▶  │  （周期）
  │  ◀── HEARTBEAT_ACK {server_time, pending_actions?} ──────   │
  │                                                             │
  │  （同一 node_id 的旧连接若仍在，网关发 GOAWAY 并关闭旧连接 = fencing）
```

---

## 8. 数据目录与配置

### 8.1 网关侧

| 项 | 默认 | 说明 |
|----|------|------|
| `--listen` | `:8443`（或 `:8080` 前置反代） | 监听地址 |
| `--data-dir` | `./data` | 存放 `gateway-registry.db`（注册表 + 审计） |
| `--tls-cert` / `--tls-key` | 空 | 直接 TLS；为空则期望前置反代 |
| `--trusted-proxies` | `127.0.0.1` | 信任 `X-Forwarded-*` 的来源 |
| `--public-base-url` | 必填（TLS 反代时） | 生成控制台/会话访问地址 |
| `--owner-password-hash` | 首次启动设置 | owner 登录凭据（Argon2id 哈希） |
| `--max-nodes` / `--max-streams-per-node` | 64 / 256 | 资源上限 |
| `--security-mode` | `monitor` | 安全策略模式 `monitor` / `enforce`（见 [09](./09-edge-hardening.md)） |
| `--security-config` | 空（内置基线） | 静态 deny、IP 限流、惩罚封禁、过载阈值（见 [09](./09-edge-hardening.md) §3/§5/§6） |

> ✅ **命名冲突已解决（2026-10-11）**：usage ledger 的默认 DSN 已从 `./data/gateway.db`
> （会落在 `backend/data/`，随进程 CWD 漂移）改为 **`~/.aicli/data/usage_ledger.db`**
> （`configs/config.yaml:93`），文件名与落点都与网关解耦。本仓库中 `gateway` 一词的其余
> 占用保持不变——LLM provider 默认名 `gateway`（`internal/bootstrap/manager.go:19`）、
> logger module `gateway`（`internal/pkg/logger/module.go:10`）、archive layer `gateway`
> （`internal/observability/tool_output_artifact.go:45`）。
> 因此：
> 1. 新网关的数据库文件命名为 **`gateway-registry.db`**，与 ledger 的 `usage_ledger.db` 明确区分；
> 2. 文档/日志中一律写 **`aicli-gateway`（外网会话网关）** 以区别于 LLM provider `gateway`；
> 3. 不新增名为 `gateway` 的 LLM provider 或 logger module 语义。

### 8.2 本地侧（agent）

| 配置键 | flag | env | 说明 |
|--------|------|-----|------|
| `gateway.url` | `--gateway` | `AICLI_GATEWAY_URL` | 网关地址（`wss://` 或 `https://`，自动推导 WS 路径） |
| `gateway.token` | `--gateway-token` | `AICLI_GATEWAY_TOKEN` | 设备注册令牌（敏感，优先 env/配置文件） |
| `gateway.name` | `--gateway-name` | `AICLI_GATEWAY_NAME` | 设备/节点显示名 |
| `gateway.enabled` | `--gateway=false` | — | 显式关闭（覆盖配置） |
| `gateway.routes` | — | — | 覆盖默认路由白名单（高级） |
| `gateway.insecure_skip_verify` | — | `AICLI_GATEWAY_INSECURE` | **仅测试**：跳过 TLS 校验 |

配置加载遵循现有分层配置栈（`./.aicli/config.yaml` → `$HOME/.aicli/config.yaml` → `./aicli.yaml` → …），
与 `runtime-server` / `aicli` 同源，见 `docs/plan/layered-config-merge-design-20260917.md`。
