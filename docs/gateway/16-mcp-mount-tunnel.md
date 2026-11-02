# 16 MCP 反向挂载穿透（remote tools MCP → gateway 公网端点）

> 文档状态：设计草案（2026-10-29）
> 适用基线：`aicli-remote-tools serve --http`（`internal/remotetools`）、`aicli-gateway`（`cmd/gateway`）、反向隧道（`internal/gateway/tunnel` + `internal/gateway/agent`）
> 上游文档：[01 需求](./01-requirements.md) · [02 架构](./02-architecture.md) · [03 协议](./03-protocol.md) · [04 安全](./04-security.md) · [09 边缘加固](./09-edge-hardening.md) · [10 Runbook](./10-runbook.md)
> 关联外部文档：`docs/user-guide/aicli-remote-tools.md`、`docs/plan/aicli-remote-tools-ssh-http-design-20261021.md`

---

## 0. 结论速览（直接回答四个问题）

| 问题 | 结论 |
|------|------|
| 本地 MCP 无法被外网访问，怎么穿透？ | **复用已有反向隧道**：`aicli-remote-tools` 内嵌 gateway agent（`internal/gateway/agent`），只发起出站 `wss://<gw>/gw/agent`；本机继续只绑回环，**不开放任何入站端口**。 |
| 怎么把本地 MCP「注册」到 gateway？ | 在既有 `REGISTER` 帧里**新增 `mounts` 字段**（additive，不 bump 协议版本），每个 mount 声明：本地 upstream 地址、本地路径、令牌来源环境变量、限额。gateway 侧落成 `MountRegistry` 并持久化。 |
| 需要支持多个 MCP 吗？怎么区分？ | 需要。**一条隧道承载 N 个 mount**，每个 mount = 一个独立的本地 MCP 端点（可以是 `aicli-remote-tools` 自己的 `/mcp`，也可以是同机其他 streamable-HTTP MCP，如 `127.0.0.1:18766/mcp`）。区分靠**互不相同的访问路径**，不靠 query。 |
| 是否自动生成唯一访问路径？ | 是。默认形状 **`https://gw.example.com/m/{slug}/mcp`**，`slug` 由节点侧**确定性派生**（8 位 base32，源自持久化的 mount-id，重启/failover 后 URL 不变），gateway 侧做**唯一性校验 + 冲突拒绝**（绝不静默改名）。同时提供 `--mount-slug team-api` 人工固定，和 `--mount-path-style=root` 生成用户期望的 `https://gw.example.com/xxxxxx/mcp` 形态。 |

**外部程序接入形态**（消费方机器的 MCP 客户端配置，`internal/remotetargets` 的 `remote-targets.yaml` 形态**今天即可用**：`httpURL()` 会保留 scheme+path 并补 `/mcp`，`token_env` 支持每挂载一把令牌）：

```yaml
# ~/.aicli/remote-targets.yaml （消费方机器）
remote_targets:
  prod_tools:
    transport: http
    remote:
      listen: https://gw.example.com/m/k7f3qz2a     # httpURL() 自动补 /mcp，scheme 与路径保留
      token_env: AICLI_MOUNT_PROD                    # 值是 gateway 颁发的 amt_ 令牌
```

```bash
export AICLI_MOUNT_PROD="amt_9f3c…"
# 展开结果：url=https://gw.example.com/m/k7f3qz2a/mcp
#           headers.Authorization=Bearer ${AICLI_MOUNT_PROD}
aicli chat                                     # 会话自动加载该挂载
```

> ⚠️ 短语法 `aicli chat --remote https://gw.example.com/m/k7f3qz2a` **当前不能**用于挂载：`Spec.listenAddress()` 只取 `host[:port]`（scheme 与 path 全丢，端口缺省 18765），会展开成 `http://gw.example.com:18765/mcp`。让短语法支持挂载是 P1 的一行级改动（见 §10 第 12 条与 §12）。

---

## 1. 一句话方案

把运行在 NAT 之后的 MCP Streamable HTTP 服务，以**挂载（Mount）**为单位注册进公网 `aicli-gateway`；gateway 在 `/m/{slug}/mcp` 暴露一个**面向程序**的端点，用**挂载令牌**（`amt_` 前缀、只存哈希、可撤销、按 mount 作用域）认证外部 MCP 客户端，再把请求经**既有反向隧道**（出站 WebSocket + `STREAM_*` 多路复用）回送到本机；本机 agent 剥掉网关命名空间前缀、注入本地 Bearer 凭据，转发到回环 MCP 端点，响应按 SSE 原样流式回流。

```text
外部 MCP 客户端                     公网 gateway                         用户本机（NAT 后，无入站端口）
┌──────────────────┐  HTTPS POST  ┌────────────────────┐  出站 wss(既有) ┌───────────────────────────┐
│ aicli --remote   │ ───────────▶ │ /m/{slug}/mcp      │ ◀───────────── │ aicli-remote-tools        │
│ mcp.yaml 客户端  │  Bearer amt_ │  挂载认证 + 限流    │  STREAM_OPEN   │  ├─ MCP daemon :18765/mcp │
│ Claude / 自研    │ ◀─────────── │  slug→node→mount   │ ─────────────▶ │  ├─ MCP daemon :18766/mcp │
└──────────────────┘  SSE 流式    │  审计 / 指标        │  ◀──────────── │  └─ gateway agent(内嵌)    │
                                  └────────────────────┘  STREAM_DATA   └────────────┬──────────────┘
                                                                                     │ 127.0.0.1 回环
                                                                     agent 注入 Authorization: Bearer <本机 token>
```

### 1.1 核心不变量（延续 [README §1](./README.md)，逐条有实现落点）

| # | 不变量 | 落点 |
|---|--------|------|
| 1 | **出站即接入**：本机绝不监听公网 | 复用 `agent.WSConnector`；remote-tools 只加 agent 客户端，不加公网 listener |
| 2 | **本机 MCP 令牌不出本机** | 隧道本就剥离客户端 `Authorization`（`agent/forward.go: copyForwardRequestHeaders`）；本地 Bearer 由 agent 从**本机环境变量**注入 |
| 3 | **外部凭据 ≠ 本地凭据**（两套，互不替换） | gateway 颁发 `amt_` 挂载令牌；`AICLI_REMOTE_TOKEN` 永不出机器，也不进 gateway 配置 |
| 4 | **失败可降级**：gateway 不可达 = 今天的行为 | mount 注册失败只影响公网入口；`--http` 直连与 `ssh://` 路径完全不变 |
| 5 | **未声明即拒绝**（fail-closed） | 未挂载的 slug → 404；已挂载但路径不在 mount 本地 path 白名单 → 403（沿用 `tunnel/proxy.go: routeAllowed`）；无令牌/令牌无效 → 401，**无 dev 旁路** |
| 6 | **网关被攻破也拿不到本机凭据与数据面持久化** | gateway 只存 slug→node 路由与令牌哈希；不存工具结果、不落会话正文（对齐 [README §4](./README.md)「会话归属本地」） |

---

## 2. 现状与缺口（每条都对着代码，作为设计约束的出处）

| 事实 | 代码位置 | 对本设计的约束 / 影响 |
|------|----------|----------------------|
| MCP daemon 默认 `127.0.0.1:18765/mcp`；非回环绑定**强制**要求 ≥32 字节 token | `internal/remotetools/server.go: validateHTTPAuth`、`config.go: HTTPConfig`（默认值见 `DefaultConfig`） | 公网暴露不能靠"把它绑到 0.0.0.0"，必须走隧道；本地 token 语义 = `Authorization: Bearer` |
| 认证只认 `Authorization: Bearer`（或 mTLS 客户端证书），**不认** `X-AICLI-Token` | `server.go: authorized()` / `bearerToken()` | agent 现有的 `X-AICLI-Token` 注入对 MCP 无效 → 必须新增 **mount 级凭据注入（Bearer）** |
| 反向代理入口 `/n/{node_id}/…` 外层是 `owner.RequireSession`（浏览器 Cookie） | `cmd/gateway/main.go:855` | 程序化 MCP 客户端无法使用 `/n/`；需要**新的命名空间 + 新的认证方式** |
| 隧道转发**丢弃** `host`/`authorization`/`cookie`，注入 `X-AICLI-Token`，重写 `Origin/Referer` | `internal/gateway/agent/forward.go: copyForwardRequestHeaders`、`rewriteForwardOriginHeaders` | 外部凭据天然不会到达本机（安全正确）；mount 的本地凭据必须在 agent 侧构造 |
| 转发路径 = 调用方剥前缀后的路径；`ServeProxy(w,r,nodeID,path)` 的 `path` 完全由调用方决定 | `internal/gateway/tunnel/proxy.go: ServeProxy` | **挂载可以在网关侧剥自己的前缀**，本地服务无需感知网关（对齐 [02 §4.2 前缀剥离](./02-architecture.md)） |
| 路由白名单是**本地路径**语义，空白名单 = 全拒绝 | `tunnel/proxy.go: routeAllowed`、`registry.go: Node.Routes` | mount 的本地 path 必须进 `Routes`；不能因为挂载而放宽成 `/` |
| 每个 agent 连接只有**一个** `LocalBaseURL` | `internal/gateway/agent/forward.go: ForwardConfig` | 多 MCP（多端口）无法区分 → 必须引入 **mount → upstream** 映射与 slug 传递 |
| 一个 remote-tools 进程 = 一个 MCP 端点（一个 workspace + profile + 一个 `mux.Handle(path)`） | `remotetools/server.go: RunHTTPWithListener`、`NewServer` | 多 MCP = 多进程多端口（或宿主 `toolctl serve`），设计必须按 upstream 寻址而非按进程 |
| `NodeID` 每进程随机、**故意不持久化**（重启即新实例，覆盖旧 stale 条目） | `internal/gateway/agent/nodeid.go`（含注释说明）、`tunnel/registry.go: Restore` | **slug 绝不能从 NodeID 派生**，否则每次重启公网 URL 都变 |
| 隧道请求体上限 1 MiB（两侧各一次） | `tunnel/proxy.go: maxProxyRequestBody`、`agent/forward.go: defaultMaxForwardBody` | MCP `write/apply_patch` 大内容会 413 → 必须做成可配（P0） |
| agent 转发硬超时 5 分钟 | `agent/forward.go: forwardStream`（`context.WithTimeout(..., 5*time.Minute)`） | envd 后端默认 `callTimeout=10m`、长 shell 任务会被截 → mount 级超时（P0） |
| 本地 MCP 并发槽 8，超额 429 | `remotetools/config.go: Limits.MaxConcurrentCalls`、`server.go: concurrencyMiddleware` | 外部多客户端并发需容量说明；429 必须原样透传（客户端会重试） |
| MCP Go SDK v1.4.0 默认开启 DNS-rebinding 保护：**本地地址为回环且 `Host` 非回环 → 403** | go-sdk `mcp/streamable.go: ServeHTTP`（`DisableLocalhostProtection`） | 隧道转发时 Go client 用 target URL 生成 `Host`（= `127.0.0.1:port`）→ **天然通过**；设计里必须写明"禁止把公网 Host 透传进本机"这条隐式契约 |
| 本地 MCP 走 Stateful（稳定 `Mcp-Session-Id`） | `remotetools/server.go: StreamableHTTPOptions` | `shell` / `job_*` 需要跨请求的稳定 owner；stateless 会在客户端不回传 session 时让每个请求生成随机 session id（G5）。挂载第三方 stateful MCP 的会话亲和仍天然成立（一挂载一节点） |
| 客户端已能把"基址"展开成 `/mcp` 并注入 Bearer 头 | `internal/remotetargets/registry.go: httpURL`、`case TransportHTTP`（`token_env`） | `remote-targets.yaml` 的 `remote.listen` **今天即可**填挂载基址（`httpURL` 保留 scheme+path 再补 `/mcp`），`token_env` 让多挂载各持一把令牌 |
| 短语法 `--remote` 的 http 形态只保留 `host[:port]` | `internal/remotetargets/spec.go: listenAddress`（`https://svc.internal` → `svc.internal:18765`）、`TokenEnv()` 固定 `AICLI_REMOTE_TOKEN` | `--remote https://gw/m/{slug}` **会丢 scheme 与挂载路径** → 要么走 `remote-targets.yaml`，要么按 §10 第 12 条给短语法补 path/scheme/`?token_env=` |
| 非回环 `listen` 且未配 `token_env` 会被拒 | `registry.go: case TransportHTTP` + `isLoopbackListen`（fail-closed） | 与挂载"强制令牌"天然一致：客户端不配令牌就连不上，不需要新增校验 |
| gateway 已有可复用的凭据设施：设备令牌（`agw_`，只存 SHA-256，可撤销、文件持久化）、限流/惩罚封禁、审计、指标 | `internal/gateway/device/device.go`、`internal/gateway/ratelimit`、`internal/gateway/audit`、`cmd/gateway/main.go: limited/metrics` | 挂载令牌按**同构**实现（`amt_`），不发明新凭据体系 |

---

## 3. 概念模型：Mount（挂载）

### 3.1 定义

> **Mount = 一个可被公网程序化访问的本地 MCP 端点声明。**

四元组 `(slug, node, upstream, credential)`：

| 维度 | 归属 | 说明 |
|------|------|------|
| `slug` | gateway 命名空间 | 公网路径段，全局唯一，稳定（跨重连/跨 gateway 实例不变） |
| `node` | gateway 注册表 | 承载该挂载的隧道连接（`node_id`，进程级、易变） |
| `upstream` | **本机** | 本地 MCP 的真实地址：`http://127.0.0.1:18765` + `/mcp`；只有本机知道 |
| `credential` | 两侧各一套 | 外部：gateway 颁发的 `amt_` 令牌；本地：`token_env` 指向的环境变量 |

关键关系：

- **1 node : N mount**：一台机器上跑多个 MCP（多 workspace / 多 profile / 第三方）→ 一条隧道，多个互不相干的公网路径。省连接、省心跳、省 failover 复杂度。
- **1 mount : 1 upstream**：一个挂载只指向一个本地端点。要把同一个 workspace 以两种 profile 暴露，就跑两个 daemon（`18765` readonly / `18766` standard），挂两个 mount。
- **1 mount : N token**：一个挂载可发多个外部令牌（给不同调用方），可逐个撤销；撤销立即生效，不影响同节点其他挂载。
- **挂载不改变本地语义**：本地 daemon 看到的仍是 `POST /mcp`，与 SSH 本地转发、直连完全一致。

### 3.2 与 `/n/{node}` 的边界（为什么不复用）

| | `/n/{node_id}/…`（既有） | `/m/{slug}/…`（本设计） |
|---|---|---|
| 调用方 | owner 的浏览器 / 控制台 | **任意程序**（MCP 客户端） |
| 认证 | owner session cookie（`auth.RequireSession`） | 挂载令牌 `Authorization: Bearer amt_…` |
| 粒度 | 整个节点的 web（routes 白名单） | 单个 MCP 端点 |
| 生命周期 | 跟随进程连接（NodeID 易变） | 跟随挂载身份（slug 稳定） |
| 无 owner 密码时 | dev 旁路（放行） | **仍然强制令牌**（MCP = 远程执行等价，不给旁路） |
| 审计/指标 | `aicli_gateway_proxy_requests_total` | 独立 `mount_*` 计数与审计类型 |

两者共用同一条隧道与同一套 `ServeProxy`，只是**入口命名空间 + 认证 + 授权粒度**不同。`/n/` 不能加个"cookie 例外"来充当程序化入口：那会把节点全量 web 面（`/api/runtime/*`、`/web/*`）暴露给令牌持有者，能力远超"调用这一个 MCP"。

### 3.3 数据模型

wire 层（`internal/gateway/wire/messages.go`，与 `Register` 同处）：

```go
// MountSpec 是节点声明的一个可公网访问的本地 MCP 端点。
type MountSpec struct {
    ID       string `json:"id"`                 // 节点内稳定标识（默认 = name）
    Name     string `json:"name,omitempty"`      // 人类可读，参与 slug 派生
    Slug     string `json:"slug,omitempty"`      // 节点派生/固定后的公网路径段；空=拒绝（gateway 没有节点的稳定身份，不能替它派生）
    Kind     string `json:"kind,omitempty"`      // "mcp"（默认）；预留 "http"
    Upstream string `json:"upstream"`            // 本机地址：http://127.0.0.1:18765/mcp
    Auth     *MountAuth `json:"auth,omitempty"`  // 本地凭据来源（只传变量名，永不传值）
    Limits   *MountLimits `json:"limits,omitempty"`
    Labels   *MountLabels `json:"labels,omitempty"` // profile/workspace/roots/tools/hostname…（仅展示与自检）
}

type MountAuth struct {
    Type     string `json:"type,omitempty"`     // "bearer"（默认）| "none" | "header"
    TokenEnv string `json:"token_env,omitempty"` // 本机环境变量名；值不出本机
    Header   string `json:"header,omitempty"`    // 默认 "Authorization"
}

type MountLimits struct {
    MaxBodyBytes int64 `json:"max_body_bytes,omitempty"`  // 请求体上限
    TimeoutSec   int   `json:"timeout_sec,omitempty"`     // 单次调用上限
    MaxConcurren int   `json:"max_concurrency,omitempty"` // 网关侧并发闸
    MaxStreams   int   `json:"max_streams,omitempty"`     // GET SSE 长连接上限
}
```

gateway 侧记录（`internal/gateway/mcpmount`）：

```go
// Record 是挂载在网关侧的登记项（持久化到 data/mounts.json，0600）。
type Record struct {
    Slug        string    `json:"slug"`
    MountID     string    `json:"mount_id"`    // node 稳定身份 + "/" + spec.ID
    NodeID      string    `json:"node_id"`     // 当前承载连接（易变）
    NodeStable  string    `json:"node_stable"` // 节点持久身份（审计与冲突归因）
    Kind        string    `json:"kind"`
    Name        string    `json:"name"`
    Labels      *wire.MountLabels `json:"labels,omitempty"`
    Limits      wire.MountLimits  `json:"limits"`
    CreatedAt   time.Time `json:"created_at"`
    UpdatedAt   time.Time `json:"updated_at"`
    LastSeen    time.Time `json:"last_seen"`
    Disabled    bool      `json:"disabled,omitempty"`  // 控制台一键停用（不动节点声明）
    Stale       bool      `json:"stale,omitempty"`     // 快照恢复、当前无活连接
}

// TokenRef 是挂载令牌的可外显投影（明文与哈希都不在这里）。
type TokenRef struct {
    ID string; MountSlug string; Name string; Prefix string
    CreatedAt, LastUsed time.Time; ExpiresAt time.Time; Revoked bool
}
```

令牌存储与 `device.Store` **严格同构**：32 随机字节 → `amt_<base64url>`，只存 SHA-256，`Authenticate` 命中即刷新 `LastUsed`，`Revoke` 只标撤销、撤销后才能 `Delete`。

### 3.4 生命周期

```text
节点启动 ── 生成/读取 mount-id（持久）── 派生 slug ── REGISTER{routes=[本地 path...], mounts=[...]}
                                                            │
                                          gateway 校验：格式 / 保留字 / 唯一性 / 冲突
                                                            │
                                     成功 ┌── MountRegistry.Upsert ── REGISTER_ACK{accepted_mounts, public_urls}
                                     失败 └── rejected_mounts[{slug,reason}]（节点侧 stderr + 控制台可见）
                                                            │
外部客户端 ── POST /m/{slug}/mcp (Bearer amt_) ── 认证 ── 限流 ── ServeProxy(nodeID, "/mcp")
                                                            │
                                       agent: X-AICLI-Mount=slug → upstream → 注入本地 Bearer → 回环
                                                            │
节点断线 ── Record.Stale=true（URL 保留，响应 502 + Retry-After）── 重连同 slug 自动转 live
撤销令牌 ── 立即 401；禁用挂载 ── 立即 403；节点消失 ── 控制台可删快照（同 /api/nodes 语义）
```

---

## 4. 公网路径与 slug 生成规则

### 4.1 路径形状

| 风格 | URL | 说明 | 默认 |
|------|-----|------|------|
| **namespaced** | `https://gw.example.com/m/{slug}/mcp` | 与 `/api`、`/gw`、`/n`、`/s`、`/hooks`、`/metrics`、控制台 `static` 完全隔离；边缘规则（deny/限流/日志）一条 `PathPrefix("/m/")` 覆盖 | ✅ 推荐 |
| **root** | `https://gw.example.com/{slug}/mcp` | 用户提出的形态，最短；但 slug 落在根命名空间，必须做保留字/保留文件冲突校验，且控制台 SPA 兜底（`cmd/gateway/console.go: hasReservedPrefix`）必须同步增补 | opt-in：`--mount-path-style=root` |

`…/mcp` 这一段**就是本地路径**：网关剥掉 `/m/{slug}` 后，本机 daemon 收到 `POST /mcp`，与今天直连/SSH 转发一致（对齐 [02 §4.2](./02-architecture.md) 的"前缀剥离"原则）。若本地服务挂在 `/foo/mcp`，则公网是 `/m/{slug}/foo/mcp`——**不做路径重写魔法**，保持可预测。

### 4.2 slug 生成（确定性 + 唯一性）

```text
stable_id  = node_mount_identity + "/" + mount_spec.id
             └ node_mount_identity：本机持久随机身份
                ~/.aicli/data/gateway-agent/mount-id   （0600，首次运行生成 16 字节，重启/升级/failover 均复用）
slug       = lower_base32( sha256("aicli-mount-v1" + stable_id)[0:5] )   → 8 字符，字符集 [0-9a-z]，URL 安全
```

设计要点：

1. **确定性 → 稳定 URL**：同一台机器、同一个挂载身份，重启后 URL 不变；换 gateway 实例（主备切换，见 [15](./15-multi-instance-architecture.md)）也不变。这是"自动生成唯一路径"可用的前提——如果 URL 每次变，客户端配置就得每次改。
2. **不从 `NodeID` 派生**：`agent.ProcessNodeID()` 是**每进程随机且不持久化**的（`nodeid.go` 注释解释了为什么必须如此：两个并发 TUI 是两个节点）。用它派生 = 每次重启换 URL。
3. **不从 workspace 路径派生**：改目录名即换 URL；更糟的是两台机器同一相对路径必然撞车，把冲突概率推到最大。
4. **字符集**：小写字母+数字、8 位（≈2^40 空间），无 `-`/`_` 之外的歧义字符，便于口头传达与日志检索；不承载语义（不给枚举者线索）。
5. **可 pin**：`--mount-slug team-api` / config `slug:` 直接人工指定（校验：`^[a-z0-9][a-z0-9-]{1,31}$`，且不与保留前缀/`index.html` 等控制台静态文件冲突）。pin 的稳定性由操作者负责，gateway 只保证不冲突。
6. **多实例**：不传 `--instance` 时，新 workspace 默认按 **machine ID + workspace 绝对路径** 派生 `auto-<hostname>-<8hex>`；两台电脑即使都用 `/home`，machine-id/MachineGuid 不同也会得到不同 identity 与 slug。`--instance <id>`（config `gateway.instance`）可显式指定，identityFile 为 `~/.aicli/data/gateway-agent/instances/<id>/mount-id`，并默认 `selfName`/节点名=`<id>`。旧共享 identity 由第一个 machine+workspace 通过 `legacy-workspace`（JSON，旧单行自动升级）继承，保住既有 URL；identity 已被另一个进程锁定（`mount-id.lock`）时 gateway disabled 并提示 `--instance`。**一个并发进程一个 identity**；共享 identity 会让两个进程在 REGISTER 时互相把对方的挂载标为 stale。
7. **人类可读别名保留**：`name`（如 `prod-readonly`）进 `Record.Name`，控制台与 `GET /api/mounts` 展示；**不进 URL**（避免泄露目录/项目结构，且路径变更是破坏性的）。
8. **冲突处理 = 拒绝，不改名**：
   - gateway 维护 `slug → mount_id` 唯一表；
   - 同一 `mount_id` 重连（同 slug）→ 覆盖并转 live（幂等）；
   - 不同 `mount_id` 撞同一 slug（理论上只有 pin 才会发生）→ **后到者拒绝**，回执 `rejected_mounts=[{slug, reason:"mount_slug_conflict"}]`，节点侧打印明确错误（"请改 `--mount-name` 或 `--mount-slug`"），控制台列出冲突。
   - 理由：静默改名会让客户端 URL 悄悄漂移，故障极难定位；本仓库的既有取向是**显式失败**（对齐 [02 §3.4 双归属标 conflict](./02-architecture.md)）。
9. **slug 不是秘密**：它是路由标识，安全性只来自挂载令牌。文档必须这样写，避免"靠路径隐藏"的错误心智模型（但仍因 40-bit 空间 + 限流 + 恒定时间比较而不宜被枚举）。

---

## 5. 端到端链路

### 5.1 一次 `tools/call` 的完整路径

```text
1. 客户端  POST https://gw.example.com/m/k7f3qz2a/mcp
              Authorization: Bearer amt_xxx
              Accept: application/json, text/event-stream
              Content-Type: application/json
              {"jsonrpc":"2.0","method":"tools/call","params":{"name":"remote_prod_grep",...}}
                    │
2. 边缘     edge deny / 可信代理解析 / IP 限流 / 惩罚封禁        （[09](./09-edge-hardening.md)，现有 limited 装饰器）
3. 认证     Authorization: Bearer amt_ → mount.TokenStore.Authenticate
              ├ 无/错/撤销/过期 → 401 + WWW-Authenticate，罚分（进 ban）
              └ OK → ctx.mount = Record；校验 !Disabled；作用域含该 slug
4. 解析     slug → Record.node_id → tunnel.Server.session(node_id)
              ├ 无活连接 → 502 {"error":"node offline"} + Retry-After（绝不 404）
              └ 有 → 继续
5. 授权     localPath = strip("/m/{slug}") = "/mcp"
              routeAllowed(node.Routes, localPath)  // 节点声明的本地路径白名单
              ├ 否 → 403 "route not allowed" + OnRouteDenied 罚分
              └ 是 → ServeProxy(w, r, node_id, "/mcp")
6. 入隧道   STREAM_OPEN{method,path="/mcp?...",headers(剥 authorization/cookie/host),
                        X-Forwarded-Prefix: "/m/k7f3qz2a",   // 网关覆写，客户端伪造无效
                        X-AICLI-Mount: "k7f3qz2a"}           // 网关覆写，本设计新增
              + STREAM_DATA(body) + STREAM_END
                    │  （复用既有 1MiB→可调 body 缓冲与 flow-control）
7. 本机     agent 解析 X-AICLI-Mount → mount 表命中：
              upstream = http://127.0.0.1:18765  ，path = "/mcp"（去掉挂载前缀）
              注入 Authorization: Bearer <os.Getenv("AICLI_REMOTE_TOKEN")>
              Go client 由 target URL 生成 Host: 127.0.0.1:18765
              → 满足 mcp SDK 的 DNS-rebinding 保护（回环 Host），本地服务只看到标准 MCP 请求
8. 本地     remotetools.Server: authorized() → concurrencyMiddleware(槽) → StreamableHTTPHandler(Stateless)
              → 审计 / metrics / roots 收窄 → 工具在 jail 内执行
9. 回流     STREAM_DATA(响应头) → 分块 STREAM_DATA(SSE) ⇄ WINDOW_UPDATE → STREAM_END
              gateway 逐块 w.Write + Flush → 客户端收到实时 progress / 终态结果
10. 记账    审计 mount_proxy{slug,node_id,token_id,method,status,bytes,duration}
              指标 aicli_gateway_mount_requests_total{slug,status}
```

### 5.2 头处理规则表（唯一真源，实现时逐条对齐）

| 头 | 客户端 → 网关 | 网关 → 隧道 | 隧道 → 本机 | 说明 |
|----|--------------|-------------|-------------|------|
| `Authorization` | 必带 `amt_` | **校验后丢弃**，不进隧道 | agent 依 mount 的 `token_env` **重新注入** `Bearer <本地 token>` | 不变量 2/3 的落点；外部凭据与本地凭据物理隔离 |
| `X-AICLI-Mount` | 若带 → **覆写** | 网关写入 slug | agent 只按此头选 upstream | 与 `X-Forwarded-Prefix` 同法（`tunnel/proxy.go:64` 已是"覆写"语义） |
| `X-Forwarded-Prefix` | 若带 → **覆写** | `/m/{slug}` | 保留给本地（SPA base-href 用，MCP 忽略） | 既有行为，不改 |
| `Host` | 公网 host | — | **不透传**：Go client 用 target URL 生成回环 Host | 必须保持：否则 mcp SDK 直接 403（DNS-rebinding 保护） |
| `Origin`/`Referer` | 浏览器值 | 透传 | 重写为本地 origin（既有 `rewriteForwardOriginHeaders`） | 程序化 MCP 客户端通常不带；带上也不会因跨源被拒 |
| `Accept` | `application/json, text/event-stream` | 透传 | 透传 | SDK 校验：POST 必须双类型，缺一即 400 |
| `Mcp-Session-Id` | 有则透传 | 透传 | 透传 | remote-tools 现在也使用 stateful session（`shell` / `job_*` 需要稳定 owner）；挂载第三方 stateful MCP 时靠 mount→node 天然亲和 |
| `Cookie` | — | **丢弃** | — | 既有行为，保持不变 |
| `Content-Length`/`Transfer-Encoding` | — | hop-by-hop 清理 | — | 既有 `isHopByHopHeader` |
| `X-AICLI-Token` | 客户端带 → 丢弃（不信任） | 不进 | agent 只对本机默认转发目标注入 | 与挂载无关，避免把 runtime 写令牌泄给 MCP daemon |

---

## 6. 协议扩展（对齐 [03-protocol.md](./03-protocol.md) 口径）

### 6.1 REGISTER 增字段（additive，不 bump 版本）

```jsonc
// A→G，紧随 WELCOME 的 REGISTER payload 增量
{
  "node_id": "node-3f9a…", "kind": "remote-tools", "name": "build-box",
  "routes": ["/mcp"],                 // ← mount 的本地路径必须进白名单（fail-closed）
  "capabilities": ["mcp-mount"],
  "mounts": [                          // ← 新增
    { "id": "prod-readonly", "name": "prod-readonly", "slug": "k7f3qz2a",
      "kind": "mcp", "upstream": "http://127.0.0.1:18765/mcp",
      "auth": { "type": "bearer", "token_env": "AICLI_REMOTE_TOKEN" },
      "limits": { "max_body_bytes": 8388608, "timeout_sec": 600, "max_concurrency": 16 },
      "labels": {
        "profile": "readonly", "tool_prefix": "remote_prod",
        "workspace": "/srv/app", "roots": ["/srv/app", "/srv/shared"],
        "hostname": "build-box", "os": "linux", "arch": "amd64",
        "version": "0.1.0", "instance": "auto-build-box-3f9a",
        "backend": "local", "read_only": true, "tool_count": 4,
        "tools": ["remote_prod__view", "remote_prod__ls", "remote_prod__glob", "remote_prod__grep"]
      } }
  ]
}
```

- **不 bump `wire.ProtocolVersion`**：`tunnel/server.go: serveConn` 对版本是**严格相等**判断，bump 会让所有老 agent 掉线；`mounts` 是纯增量字段，老 gateway `json.Decode` 忽略未知字段 = 天然向后兼容（老 agent + 新 gateway 也照旧，只是没有挂载）。
- `upstream` **只在本机解释**，网关不访问、不校验可达性、不进日志明文以外的形式（它可能含内网主机名，属敏感拓扑；日志只记 slug）。
- `labels` 只用于控制台展示与自检：网关在 `prepareMounts` 落库前裁剪长度/数量（工具 128 个、目录 32 个，单值超长追加 `…`），不会把它用于路由或授权。`tools`/`roots` 只对节点自身的 self mount 上报；额外转发的 upstream 不探测、不冒充其工具面。

### 6.2 REGISTER_ACK 回执

```jsonc
{ "node_id": "node-3f9a…", "console_url": "https://gw/n/node-3f9a…/web/",
  "accepted_routes": ["/mcp"],
  "accepted_mounts": [ { "slug": "k7f3qz2a", "public_url": "https://gw.example.com/m/k7f3qz2a/mcp" } ],
  "rejected_mounts": [ { "slug": "…", "reason": "mount_slug_conflict" } ] }
```

节点侧把 `public_url` 打到 stderr（`aicli-remote-tools` 现有启动行风格：`server.go: RunHTTPWithListener` 的 `http transport on %s%s`），并在 `doctor` 报告里输出——**操作员必须能一眼看到自己的公网 URL**。

### 6.3 挂载变更

| 变更 | 载体 | 阶段 |
|------|------|------|
| 启动时声明 / 配置变更后 | 下一次 `REGISTER`（`Reconnect` 即重发，既有 `Controller.Reconnect`） | P0 |
| 运行中增删挂载 | 轻量 `MOUNT_UPDATE` 帧（stream 0，形似 `SessionUpdate` 的 `op: upsert|remove|reconcile`） | P2 |
| 令牌颁发/撤销 | **只在网关侧**（管理 API / 控制台），不经隧道；节点不感知令牌存在 | P0 |

选择"P0 用重连接受挂载变更"的理由：挂载是低频运维动作，`Reconnect` 已有正确语义（同 NodeID 覆盖 stale 条目），不必在第一阶段引入新帧与新的一致性规则。

节点侧启动健壮性：`aicli-remote-tools serve` 的首次拨号失败不再一次性放弃——宿主用
`Controller.ConnectWithRetry` 按同一退避阶梯（2s→5s→10s→30s 封顶）重试至成功或终止错误，
网关/网络未就绪时挂载会自动补注册；连接丢失、重试尝试与重连成功均以
`aicli-remote-tools:` 前缀写入 stderr，便于定位"控制台显示离线"。

### 6.4 令牌与挂载的管理面 wire（不属于隧道协议）

管理面全部走 owner cookie（`/api/mounts*`，见 §9），**不**让节点侧能自签令牌——否则本机被攻破即可自造公网入口。

---

## 7. 网关侧实现

### 7.1 新包 `internal/gateway/mcpmount`

| 文件 | 职责 | 同构参照 |
|------|------|----------|
| `slug.go` | `DeriveSlug(nodeIdentity, mountID)`、`ValidateSlug`（字符集/长度/保留字）、`PathStyle` | 纯函数，表驱动测试 |
| `registry.go` | `slug → *Record` 内存表：`Upsert/Get/List/Remove/MarkStale/Restore`；冲突返回 `ErrSlugConflict{ExistingMountID}` | `internal/gateway/tunnel/registry.go`（含 `RemoveStale` 的 sorted 返回） |
| `tokens.go` | `amt_` 令牌存储：`Create/List/Authenticate/Revoke/Delete/Count`，只存 SHA-256，恒定时间比较 | `internal/gateway/device/device.go` |
| `store.go` | `mounts.json`（0600）落盘：records（不含哈希以外的秘密？令牌单独文件 `mount-tokens.json`）+ 撤销持久化 | `device/persist.go` |
| `handler.go` | `ServeMount(w,r)`：认证 → 解析 → 禁用校验 → 并发闸 → `ServeProxy(nodeID, localPath)` | `tunnel/proxy.go: ServeProxy` |
| `persist.go` | 快照恢复（gateway 重启后 stale 挂载仍占位，URL 不漂移） | `tunnel/persist.go` |

令牌与挂载**分文件存储**（`mounts.json` / `mount-tokens.json`）：挂载表是"路由事实"（可从 REGISTER 重建），令牌表是"凭据事实"（不可重建）。混在一个文件会让"清离线快照"这类操作误删凭据。

### 7.2 路由挂载（`cmd/gateway/main.go`）

```go
// 反向代理：/n/{node_id}/… → 节点本地服务（owner cookie）
router.PathPrefix("/n/{node_id}/").Handler(limited(owner.RequireSession(...)))

// MCP 挂载：/m/{slug}/… → 该挂载对应的本地 MCP（挂载令牌，无 cookie 旁路）
router.PathPrefix("/m/{slug}/").Handler(limited(mountHandler.ServeMount))
```

顺序与兜底约束：

- 必须在控制台 `router.PathPrefix("/")` **之前**注册（gorilla/mux 显式路由优先，与 connectors `/hooks/*` 的注册顺序注释同法）。
- `cmd/gateway/console.go: hasReservedPrefix` 增补 `"/m"`（以及 root 风格时改为**白名单式**判定：`/api|/gw|/n|/s|/m|/hooks|/metrics` + 已注册 slug 集合），否则未知路径会被 SPA 兜底吞成 `index.html`，客户端拿到 HTML 而不是 404 JSON——这是最容易被漏掉的一条。
- **不套 `owner.RequireSession`**：这是设计要点，程序化客户端没有 cookie。
- **保留 `limited`**：限流与惩罚封禁（`ratelimit.Resolver` + `BanList`）对挂载同样生效，并按 §7.4 追加倍率罚分。

### 7.3 挂载认证中间件

```go
func (h *Handler) authenticate(r *http.Request) (*TokenRef, *Record, error) {
    // 1) 只认 Authorization: Bearer amt_…（不支持 query / cookie / form：URL 会进访问日志与浏览器历史）
    // 2) tokens.Authenticate → 恒定时间；未知/撤销/过期 → ErrUnauthorized
    // 3) record, ok := registry.Get(slug); !ok → ErrMountGone（404）
    // 4) token.Mounts 不含该 slug → ErrScope（403）
    // 5) record.Disabled → ErrDisabled（403）
    // 6) per-token / per-mount 并发闸：满 → 429 + Retry-After
}
```

| 情况 | 响应 | 罚分 | 说明 |
|------|------|------|------|
| 缺/错令牌 | `401` + `WWW-Authenticate: Bearer realm="aicli-gateway-mount"` | +5 | 与 `remotetools` 本地行为同形，客户端能复用重试逻辑 |
| 令牌已撤销/过期 | `401` + `{"error":"token_revoked"}` | +5 | 明确区分"没带"和"被撤"，便于 Runbook |
| slug 未注册 | `404` + `{"error":"mount_not_found"}` | 0 | 不泄露"曾经存在过"（撤销挂载与从未挂载同响应） |
| slug 已注册但节点离线/stale | `502` + `Retry-After: 5` | 0 | **绝不 404**：客户端会据此决定是否重试退避 |
| 令牌作用域不含该 slug | `403` | +3 | 一个调用方被攻破不能横向调其他挂载 |
| 挂载被控制台禁用 | `403` + `{"error":"mount_disabled"}` | 0 | 运维急停 |
| 路径不在挂载本地白名单 | `403`（`routeAllowed` 语义） | +5 | 防"挂载即全通"，例如 `/m/x/mcp/../admin` |
| 超过挂载并发 | `429` + `Retry-After: 1` | 0 | 与 `remotetools.concurrencyMiddleware` 同形 |
| body 超限 | `413` | 0 | 语义明确，见 §10 |

**挂载端点无条件要求令牌**：即便 `--owner-password` 未设（gateway 现有 dev 旁路放行 `/n/` 与 `/api/`），`/m/*` 仍然 401。理由：MCP 面 = 文件读写 + shell 执行等价（`remotetools` 自己的 `validateHTTPAuth` 就是同一条判断——非回环无 token 直接拒绝启动）。这条差异要在代码注释和 `docs/gateway/11-security-config-examples.md` 里写明。

### 7.4 限流、配额与过载

| 层 | 键 | 默认 | 目的 |
|----|-----|------|------|
| 既有 IP 限流 | `client_ip` | `--rate-limit` | 全局洪水（复用，不改） |
| 令牌速率 | `token_id` | 600 rpm（可调，`--mount-token-rate-limit`） | 单个外部调用方失控不拖垮其他调用方 |
| 挂载并发 | `slug` | `limits.max_concurrency`（默认 16） | 保护本机 `MaxConcurrentCalls`（8）之前先限住上游 |
| 挂载 SSE 流 | `slug` | `limits.max_streams`（默认 64） | GET SSE 是会话状态，不占工具调用并发槽；超限返回 `mount_stream_busy` |
| 挂载熔断 | `slug` 连续 5xx/超时 ≥ N | 打开 30s，返回 503 | 节点抖动时不堆积隧道流 |
| 惩罚封禁 | `client_ip` | 认证失败累计进 `bans` | 与 [09](./09-edge-hardening.md) 同一条链路 |

### 7.5 审计与指标

新增 `audit.Type` 常量：`mount_registered`、`mount_rejected`、`mount_removed`、`mount_disabled`、`mount_token_created`、`mount_token_revoked`、`mount_auth_failed`、`mount_route_denied`、`mount_proxy`。

- `mount_proxy` 记录：`slug / node_id / token_id / method / local_path / status / bytes / duration_ms / client_ip`；**不记** `Authorization`、不记请求体、不记工具参数（与 `remotetools` 的 secret-hygiene 取向一致，见 `audit_secret_hygiene_test.go`）。
- Prometheus（对齐 `cmd/gateway/main.go: writePrometheusMetrics`）：
  - `aicli_gateway_mount_requests_total{slug,status}`
  - `aicli_gateway_mount_request_bytes_total{slug}` / `aicli_gateway_mount_response_bytes_total{slug}`
  - `aicli_gateway_mount_duration_seconds{slug}`（histogram）
  - `aicli_gateway_mount_active_streams{slug}`（gauge，来自隧道 allocStream）
  - `aicli_gateway_mount_auth_failures_total{reason}`
  - `aicli_gateway_mounts_registered` / `_live`（gauge）

### 7.6 持久化与多实例

- `mounts.json` + `mount-tokens.json` 在 `--data-dir`，0600，与 `devices.json` 同级同策略。
- gateway 重启：`Registry.Restore` 已有"恢复即标 stale"语义；挂载表照做 —— **URL 保号**（客户端不因此 404），连接回来自动转 live。
- 主备/多实例（[15](./15-multi-instance-architecture.md)）：挂载表与令牌表必须落**共享存储**，否则备实例既不认识 slug 也不认识 `amt_`，failover 直接不可用。这一条与 registry.json 的既有约束同源，文档里显式列出而不是留给运维发现。

---

## 8. 节点侧实现（`aicli-remote-tools` 内嵌 gateway agent）

### 8.1 装配形态

```text
aicli-remote-tools serve --http [gateway flags]
  ├─ MCP daemon（现状不变：RunHTTPWithListener，回环 + 本地 Bearer）
  └─ 可选 gateway agent（internal/gateway/agent.Controller）
        ├─ RegisterFunc：routes=[每个挂载的本地 path] + mounts=[...]
        ├─ ForwardConfig{ LocalBaseURL（默认挂载 = 自身）, Mounts[...] }
        └─ 稳定身份：~/.aicli/data/gateway-agent/mount-id（0600，一次生成）
```

新增 flags（与既有 `--http-*` 同风格，也可全走 `--config` YAML / `--config-b64`）：

```
--gateway-url <wss|https>      隧道入口（或 AICLI_GATEWAY_URL，复用 agent.EnvURL/EnvToken）
--gateway-token-env <NAME>     设备令牌来源（默认 AICLI_GATEWAY_TOKEN；值不上命令行）
--gateway-token-file <FILE>    设备令牌文件（0600；优先于 env，读一次缓存）
--instance <id>                每进程实例标识：派生 instances/<id>/mount-id，并默认 selfName/节点名
--gateway-mount                把本 daemon 自身作为默认挂载（--http 且已配 gateway 时默认 true）
--mount-name <name>            挂载显示名，参与 slug 派生（默认 = target-name + profile）
--mount-slug <slug>            人工固定公网路径段
--mount-upstream <url>         追加挂载：任意本地 streamable-HTTP MCP（可重复，N 个）
--mount-token-env <NAME>       该挂载注入的本地 Bearer 来源（默认沿用 --http-token-env）
--mount-token-file <FILE>      该挂载的本地 Bearer 文件（0600；优先于 env）
--mount-timeout <dur>          单次调用上限（默认 10m，对齐 envd callTimeout）
--mount-max-body <bytes>       请求体上限（默认 8 MiB）
--mount-max-concurrency <n>    网关侧并发闸（默认 16）
--mount-path-style <m|root>    公网路径风格（默认 m）
```

配置形态（`remotetools.Config` 增 `Gateway` 节，沿用 YAML/base64 双入口与"flags 覆盖文件"规则）：

```yaml
http:
  listen: 127.0.0.1:18765
  path: /mcp
  tokenEnv: AICLI_REMOTE_TOKEN
gateway:
  url: wss://gw.example.com
  token_env: AICLI_GATEWAY_TOKEN
  mount_self: true
  mounts:
    - name: prod-standard
      upstream: http://127.0.0.1:18766/mcp      # 同机另一个 daemon / 第三方 MCP
      token_env: AICLI_REMOTE_TOKEN_STD
      timeout: 10m
      max_body_bytes: 8388608
```

### 8.2 转发改造（`internal/gateway/agent/forward.go`）

```go
type ForwardConfig struct {
    LocalBaseURL string          // 既有：默认转发目标（/n/ 场景）
    LocalToken   string          // 既有：注入 X-AICLI-Token
    Mounts       []MountForward  // 新增
    Client *http.Client; MaxBodyBytes int64
}

type MountForward struct {
    Slug        string
    BaseURL     string   // http://127.0.0.1:18765
    StripPrefix string   // ""（网关已剥）；保留字段以支持 root 风格/前缀型上游
    TokenEnv    string   // 注入 Authorization: Bearer <env>
    Timeout     time.Duration
    MaxBody     int64
}

// forwardStream 起点：
func (c *wsConn) resolveTarget(open wire.StreamOpen) (base, path string, auth *string, td time.Duration, maxBody int64) {
    if slug := strings.TrimSpace(headerOf(open.Headers, "X-AICLI-Mount")); slug != "" {
        if m, ok := c.forwardMount(slug); ok {
            return m.BaseURL, stripMountPrefix(m, open.Path), bearerFromEnv(m.TokenEnv), m.Timeout, m.MaxBody
        }
        return "", "", nil, 0, 0 // 未知 slug → reset(mount_unknown)，fail-closed，不回落到默认目标
    }
    return c.forward.LocalBaseURL, open.Path, nil, 5 * time.Minute, defaultMaxForwardBody
}
```

三条必须锁死的细节：

1. **未知 slug 绝不回落到默认目标**。否则一个被撤销的挂载还能借道 `/n/` 的 runtime-api 打进去——正是 `routeAllowed` 空白名单全拒绝所防范的那类错误。
2. **本地 Bearer 只在挂载流上注入**，且只从 `TokenEnv` 读；`LocalToken`（`X-AICLI-Token`）注入路径保持不变，两者互不污染。
3. **`MaxBodyBytes` 与超时改为挂载级**：`handleStreamData` 现在用全局 `c.forward.MaxBodyBytes`，多挂载需按 `X-AICLI-Mount` 先定位再决定上限（当前实现是在 STREAM_DATA 阶段就判上限，因此 `resolveTarget` 必须在 `handleStreamOpen` 时完成，把结果缓存在 `inboundStream` 上）。这条是真实的实现顺序约束，写进任务清单避免踩坑。

### 8.3 routes 白名单

节点 `Routes` = 各挂载的**本地 path 集合**（如 `["/mcp"]`），**不再声明 `/`**：

- 网关对挂载请求走 `routeAllowed(node.Routes, "/mcp")` → 命中；
- 同一节点若还开了 `/n/`（人用的 web），那是它自己另外声明的 routes，互不干扰；
- 纯 remote-tools 节点（没有 web 面）声明 `/` 等于把"挂载 `/mcp`"扩成"任意本机路径可代理"，是明确要避免的。

### 8.4 部署形态（三种都支持，按隔离需求选）

| 形态 | 拓扑 | 适用 |
|------|------|------|
| A. daemon 自带 agent | 1 进程 = 1 MCP + 1 挂载 | 单机单 workspace，最省事（**默认推荐**） |
| B. 汇聚 agent 进程 | 1 进程 = N 挂载（`--mount-upstream` 指向同机 N 个 daemon） | 多 workspace/多 profile 统一出入口，一条隧道 |
| C. 宿主 agent | runtime-server / aicli 进程内 agent 附带挂载第三方 MCP | 本机已有 gateway 连接，加挂载即可（零新进程） |

形态 C 说明本设计**不是 remote-tools 专属**：挂载对象是"任何本地 Streamable HTTP MCP"（`internal/toolctl/serve.go` 已经是这个形状）。这也回答了"支持多个 MCP"的另一半：不止多个 remote-tools。

### 8.5 复用 aicli TUI 已配置的 gateway 连接

aicli 的 `/gateway config` 已经把连接信息持久化在分层 `config.yaml` 的 `gateway:` 段（`url / token / name / proxy_url / auto_connect`）。节点侧不必重复配置：

```yaml
# aicli-remote-tools 的配置
gateway:
  useAICLIConfig: true
  aicliConfigFile: ""        # 空 = 与 aicli 相同的分层搜索路径（项目 .aicli/config.yaml → ~/.aicli/config.yaml）
```

等价 flag：`--gateway-from-aicli`、`--gateway-aicli-config <file>`。

**优先级（高 → 低）**：

1. `AICLI_GATEWAY_*` 环境变量（与 aicli 自身一致）；
2. aicli-remote-tools 显式字段 / flag（`gateway.url`、`gateway.name`、`gateway.proxyUrl`）；
3. 本节点 `gateway.tokenFile` / `gateway.tokenEnv` 提供的令牌（文件 > env）；
4. aicli 配置里的 `gateway.token`（仅在 3 为空时使用）；
5. aicli 配置里的 `url / name / proxy_url / auto_connect`（仅在 2 为空时使用）。

读取实现：`agentconfig.LoadGatewaySection`（只解码 `gateway:` 一个键，不触碰全局单例，也不受其它段形状差异影响），`aicli-remote-tools` 在 `EnableGateway` 时解析一次；`proxy_url` 会传给 agent 的 WebSocket 拨号器，因此受限网络下 TUI 能连上的代理，节点也能复用。

---

## 9. 安全模型

### 9.1 信任边界

```text
[外部程序] --amt_ 令牌--> [gateway 公网边缘] --设备令牌隧道--> [本机 agent] --本地 Bearer--> [MCP daemon/jail]
   不可信                    半可信（可被攻破：只持有路由+令牌哈希）        本机可信域           最终执行域（workspace jail + 命令策略）
```

- **gateway 被攻破得不到什么**：本地 MCP 令牌（只在 `AICLI_REMOTE_TOKEN` 环境变量里）、workspace 内容、工具结果、会话正文。它能做的是"以已登记的挂载继续代理"——因此撤销要快、禁用要一键、令牌要短 TTL（见 §9.3）。
- **本机被攻破得不到什么**：外部令牌明文表（在网关侧）、gateway 管理面凭据。节点**不能**自签令牌（§6.4）。
- **隧道两侧都不是开放代理**：`/n/` 要 cookie，`/m/` 要挂载令牌 + 本地路径白名单，其他路径 404。

### 9.2 威胁 → 控制

| 威胁 | 控制 | 出处/校验 |
|------|------|-----------|
| 公网 MCP 端点被匿名调用 = 远程文件读写 + shell 执行 | 强制挂载令牌（无 dev 旁路）；默认 profile `all`（写/shell 可见，shell 命令仍受 allowlist fail-closed）；需要最小权限时显式 `readonly`；`--tool-prefix` 明示工具归属 | `remotetools.validateHTTPAuth` 同源判断；`mount_auth_failed` 审计 |
| 令牌泄露（某个调用方被攻破） | 令牌**按挂载作用域**、可逐个撤销、支持短 TTL + 轮换；撤销只影响该挂载 | `TokenRef.Mounts`、`Revoke` 立即生效；`token_rotation_test.go` 既有轮换测试 |
| 跨挂载横向（A 调用方打 B 挂载） | `token.scope` 校验 → 403 + 罚分 | §7.3 |
| 伪造 `X-AICLI-Mount` 借用别人的 upstream | 网关**覆写**该头；agent 未知 slug → `reset(mount_unknown)`，绝不回落默认目标 | §5.2、§8.2 第 1 条 |
| 借挂载探测本机其他路径（`/m/x/mcp/../admin`） | 剥离前缀后走 `routeAllowed`（本地白名单，`pathpkg.Clean` + 拒 `..`） | `tunnel/proxy.go: routeAllowed` 既有实现，测试锁死 |
| slug 枚举 | 8 位 base32（≈2^40）+ 恒定时间比较 + 认证失败罚分进 ban + IP 限流；未注册一律 404 不回显差异 | §4.2、§7.3、[09](./09-edge-hardening.md) |
| DNS rebinding / Host 伪造打本机 daemon | 隧道不透传 `Host`；Go client 用回环 target 生成 Host；本地 daemon 只绑回环 | §5.2、mcp SDK v1.4.0 内建保护 |
| 长连接/慢客户端耗尽隧道流 | per-mount 并发闸 + 熔断 + `flowControlWindow` 背压 + agent 侧 mount 超时 | §7.4、`forward.go: acquireCredit` |
| 大请求体打满内存 | 两侧上限（挂载级 `max_body_bytes`，默认 8 MiB）→ 413，**不缓冲无界** | §10 第 1 条 |
| 越权 CORS（网页脚本直接调挂载） | 默认**不发**任何 CORS 头（浏览器跨源自然失败）；确需 Web 客户端时 per-mount `allow_origin` 白名单 | 单测断言 401/403 不带 `Access-Control-Allow-Origin` |
| 审计泄露隐私 | 不记 body/工具参数/令牌；`mount_proxy` 仅元数据；`upstream` 只在内存与隧道内出现，日志只记 slug | 对齐 `remotetools` secret-hygiene 测试取向 |
| 挂载残留（节点长期离线、URL 占位） | `record.Stale` + `LastSeen` 可见；控制台"清离线快照"一并处理（`Registry.RemoveStale` 同语义） | `tunnel/registry.go: RemoveStale` |
| 中间人 | 网关必须 TLS（`--tls-cert/key` 既有）；文档明确 `wss://`/`https://` 为上线前置条件；agent 侧支持 `ProxyURL` 出站代理 | `cmd/gateway/main.go` flags |

### 9.3 令牌策略（默认值）

| 项 | 默认 | 理由 |
|----|------|------|
| 长度/熵 | 32 随机字节（`amt_` + base64url） | 与设备令牌一致（`device.TokenPrefix = "agw_"`） |
| 存储 | 仅 SHA-256 | `device.hashToken` 同法 |
| TTL | **90 天**，`--mount-token-ttl` 可改；创建时可选 `expires_at` | MCP 客户端普遍不会自动刷新凭据，短 TTL 需配套轮换流程 |
| 不过期 | 可选（`never_expires: true` 或 `ttl_seconds: -1`） | 固定设备/离线客户端需要；泄露后只能手工撤销，控制台显式警告 |
| 作用域 | 精确挂载集合（不是 `*`） | 最小能力 |
| 轮换 | 双令牌并行 → 切流 → 撤销旧令牌（文档给流程） | 不断服 |
| 传输位置 | 仅 `Authorization: Bearer` | query 会进 access log / 代理日志 / 浏览器历史 |
| 撤销粒度 | 令牌级即时；挂载级禁用即时（`Disabled`）；节点级下线 = 断隧道 | 三层急停，运维可选择代价最小的一层 |

---

## 10. 已知协议约束（必须一并修，否则功能上线即坏）

| # | 约束 | 现值 / 出处 | 不修会怎样 | 处置 |
|---|------|-------------|-----------|------|
| 1 | 隧道请求体上限 **1 MiB** | `tunnel/proxy.go: maxProxyRequestBody`；`agent/forward.go: defaultMaxForwardBody` | `write`/`apply_patch`/长 prompt 的工具调用直接 413，是**挂载最常见的第一个失败** | 提升为挂载级 `max_body_bytes`（默认 8 MiB）；`handleStreamOpen` 先解析 mount 再决定上限（§8.2）；413 响应体写清上限值 |
| 2 | agent 转发硬超时 **5 min** | `agent/forward.go: forwardStream` | envd 后端 `callTimeout` 默认 **10m**、`shell` 长任务、`job_*` 流式被腰斩 | mount 级 `timeout`（默认 10m，与 envd 对齐）；响应头已写入后只能 reset，需在文档说明"超时会得到连接重置而非 504" |
| 3 | 本地 POST 并发槽 **8**，超额 429 | `remotetools/config.go: Limits.MaxConcurrentCalls`、`concurrencyMiddleware` | 多个外部客户端 + 多工具并行 → 频繁 429 | GET SSE 使用独立 `maxConcurrentStreams`（默认 64），DELETE 无条件放行；429 原样透传（不吞、不重试） |
| 4 | `Route` 白名单是**本地路径**语义 | `tunnel/proxy.go: routeAllowed` | 若沿用 runtime-server 的 `/` 声明，挂载等于全通 | §8.3：纯 MCP 节点只声明 `/mcp` |
| 5 | 控制台 SPA 兜底吞未知路径 | `cmd/gateway/console.go: hasReservedPrefix`（只认 `/api`、`/gw`、`/n`） | root 风格挂载下 `/xxxxxx/mcp` 可能返回 HTML，客户端报解析错误 | `/m` 入保留前缀；root 风格额外注册到 SPA 之前 + 单测断言返回 404 JSON |
| 6 | mcp SDK **DNS-rebinding 保护**（回环监听 + 非回环 Host → 403） | go-sdk `mcp/streamable.go`（v1.4.0 起默认开） | 若哪天改为透传公网 `Host`，挂载全量 403（且现象诡异） | §5.2 把"不透传 Host"写成硬契约 + 回归测试（真实 SDK handler 跑一遍隧道） |
| 7 | SDK 要求 `Accept` 同时含 `application/json` 与 `text/event-stream` | 同上 | 简易客户端 400 | 文档在客户端示例里显式带双 Accept；错误体保持透传（不改写） |
| 8 | Stateful session（`SessionTimeout: 30m`） | `remotetools/server.go: StreamableHTTPOptions` | 客户端必须回传 `Mcp-Session-Id`；节点重启后旧 session 失效，客户端需重新 `initialize` | 文档说明"重启后客户端需重新 `initialize`"；mount→node 单跳天然保证会话亲和 |
| 9 | 大响应无总量上限（只有流控） | `forward.go: flowControlWindow = 1 MiB` + `WINDOW_UPDATE` 补充 | 语义正确（边读边写），但**慢客户端**会长时间占住隧道流 | per-mount 活跃流指标 + 熔断；文档建议大输出用 `artifact_read` 分页（remote-tools 已有 artifact 面） |
| 10 | gateway 无 write timeout（`ReadHeaderTimeout:10s`、`IdleTimeout:120s`） | `cmd/gateway/main.go` | SSE 长挂起会被 `IdleTimeout` 砍（Go 的 IdleTimeout 只计无活动连接，实践中 MCP SSE 有心跳即安全） | 验证项：挂载 30 分钟挂起 GET 流不被网关判 idle；必要时 per-mount `SSE` 保活注释与用例 |
| 11 | 隧道协议版本严格相等校验 | `tunnel/server.go: serveConn` | bump 版本会让所有老 agent 掉线 | 只做 additive 字段，不 bump（§6.1） |
| 12 | `--remote` 短语法的 http 形态丢 URL path 与 scheme | `internal/remotetargets/spec.go: listenAddress`（返回 `host:port`）+ `httpURL`（非 `http(s)://` 前缀则补 `http://`） | `--remote https://gw/m/{slug}` 静默变成 `http://gw:18765/mcp`，用户以为连的是挂载、实际连的是公网 18765（必然失败且错误信息误导） | P1：http transport 的 listen 保留 `scheme + path`（`parsed.EscapedPath()`），端口缺省仅在**无 path** 时生效；同时给 http 形态开 `?token_env=NAME`（现仅 envd 支持），使多挂载可在短语法里区分。无该参数时仍固定 `AICLI_REMOTE_TOKEN`（保持既有语义与测试） |

---

## 11. 控制面、控制台与运维

### 11.1 管理 API（owner cookie，`cmd/gateway`）

| 方法 | 路径 | 语义 |
|------|------|------|
| `GET` | `/api/mounts` | 列出挂载（slug / name / node / live / stale / disabled / labels / tokens 数 / 最近调用） |
| `GET` | `/api/mounts/logs` | 最近挂载调用（内存环形，最近 1000 条，含 2xx；`?slug=&limit=`） |
| `POST` | `/api/mounts/{slug}/tokens` | 颁发令牌（body: `name, mounts[], ttl_seconds, never_expires`；省略 TTL=90 天，`never_expires:true` 或 `ttl_seconds:-1`=不过期）→ **明文只返回一次** |
| `GET` | `/api/mounts/{slug}/tokens` | **该挂载**的令牌投影列表（`Prefix`/`LastUsed`/`ExpiresAt`/`Revoked`） |
| `DELETE` | `/api/mounts/{slug}/tokens/{id}` | 撤销；令牌作用域不含该 slug → 404（`?purge=1` 且已撤销 → 删除） |
| `POST` | `/api/mounts/{slug}/disable` | 急停挂载（`reason` 必填，进审计） |
| `POST` | `/api/mounts/{slug}/enable` | 恢复 |
| `DELETE` | `/api/mounts/{slug}` | 删除登记（节点在线时拒绝，防误删活入口） |
| `POST` | `/api/mounts/{slug}/probe` | 经隧道对 upstream 发一次 `initialize` + `tools/list`，回工具数（只读自检，不落业务数据） |

### 11.2 控制台（`cmd/gateway/console`，对齐 [07](./07-console.md) 轻量原则）

「MCP 服务」页/卡片：每行 `slug`（复制完整 URL）· 节点与在线状态 · **主机名 / OS·arch / profile（含只读标记与工具前缀）/ workspace / 版本·实例·backend** · 令牌数 · `禁用/启用` · `令牌`（列表 / 签发 / 一次性明文 / 撤销 / 删除记录）· `日志`（跳到 `#logs?slug=`）。主机、目录与工具清单来自 REGISTER 的 `labels` 快照；`工具（N）` 与 `目录（N）` 可展开检查，旧节点缺字段时显示 `—`，不会伪造信息。网关在落库前对标签做长度/数量裁剪（工具 128 个、目录 32 个），并只把标签当展示文本。令牌明文只在签发响应出现一次，列表只显示 `prefix` 与时间；有效期支持 30/90/365 天、指定天数（1–3650）与**不过期**（有显式警告）。`GET /tokens` 按挂载过滤，`DELETE` 校验作用域。另设「日志」页：MCP 调用环（含 2xx）与审计事件两个 tab，支持过滤与 5s 自动刷新。QPS 与 `probe` 仍未实现，留作 P2。
审计 tab 走 `GET /api/audit?limit=&type=&q=&errors=`：`type` 为前缀匹配（`mount`/`tunnel`/`login`/`device`/`rate`），`q` 匹配 type/actor/result/ip/detail，`errors=1` 只看有 `result` 的失败/拒绝事件；`audit.log` 仍是持久化来源。
离线挂载显示为"待节点上线"（灰），与节点的 stale 显示同源；`clear offline snapshots` 同时清 stale 挂载。

### 11.3 CLI 与 TUI

| 入口 | 作用 |
|------|------|
| `aicli-remote-tools gateway status`（新增子命令） | 打印隧道状态 + 已登记挂载 + **公网 URL** + 本地 upstream 健康 |
| `aicli-remote-tools doctor --json` | 报告新增：`gatewayURLConfigured`、`mounts[].slug/publicURL/upstream/authEnvSet/rejected`；缺 `token_env` 视为 `problem`（非回环无 token 同条规则） |
| `aicli gateway mounts` / TUI `/gateway mounts` | 宿主（runtime-server/aicli 形态 C）查看本机挂载（走 `/api/runtime/gateway/*` 控制面，对齐 [12](./12-connection-control.md)） |
| `gateway` 启动日志 | 打印"已登记挂载数 / 待上线挂载数"，与现有 `restored N nodes` 同风格 |

### 11.4 客户端接入示例

```bash
# 1) aicli 消费：remote-targets.yaml（今天即可，见 §0）；多挂载 = 多条 target + 各自 token_env
export AICLI_MOUNT_PROD="amt_9f3c…"
aicli chat

# 1b) 短语法（P1 起可用，依赖 §10 第 12 条修复）
export AICLI_REMOTE_TOKEN="amt_9f3c…"          # 短语法当前固定读这个变量
aicli chat --remote https://gw.example.com/m/k7f3qz2a

# 2) 通用 MCP 客户端 JSON
{
  "mcpServers": {
    "prod-tools": {
      "type": "streamableHttp",
      "url": "https://gw.example.com/m/k7f3qz2a/mcp",
      "headers": { "Authorization": "Bearer amt_9f3c…" }
    }
  }
}

# 3) 自检（initialize 握手；期望 200 + result.serverInfo；再发 tools/list 期望工具名）
curl -sS -X POST https://gw.example.com/m/k7f3qz2a/mcp \
  -H "Authorization: Bearer $AICLI_MOUNT_PROD" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
```

### 11.5 Runbook 摘要（对齐 [10](./10-runbook.md) 分级）

| 现象 | 首查 | 处置 |
|------|------|------|
| `404 mount_not_found` | `GET /api/mounts` 是否有该 slug；节点是否从未连上 | 节点侧 `gateway status` 看 `rejected_mounts`；冲突则改 `--mount-name` |
| `401` | 令牌是否撤销/过期；`mount_auth_failed` 审计的 IP | 重发令牌；必要时对该 IP 检查封禁 |
| `403 mount_disabled` / `route not allowed` | 控制台禁用状态；节点 `routes` 是否含本地 path | 启用挂载；修 `routes` 声明（不要放开成 `/`） |
| `502 node offline` + `Retry-After` | 节点隧道是否断（`/api/nodes` stale）；心跳是否超时 | 节点重连（`/gateway reconnect`）；确认 failover 后 agent 指向新实例 |
| `413` | `--mount-max-body` 与 `remotetools` 的 `--max-bytes-per-call` 孰小 | 明确告知客户端上限；大文件走分块 patch |
| 调用被 reset / 无终态 | mount `timeout` 是否短于任务时长；envd `call_timeout` | 调 `--mount-timeout`；长任务改用 `job_*` 工具族 |
| `429` | 网关 `mount_busy` / `mount_stream_busy`，或本地 `MaxConcurrentCalls` / `MaxConcurrentStreams` | 29B `too many concurrent requests` = POST 工作槽；`too many concurrent streams` 文本 = GET 流槽；79B JSON = 网关 per-mount 闸 |
| URL 变了（客户端突然全断） | 是否换机器/删了 `mount-id` 文件/pin 冲突 | **这是 P0 必须防的**：`mount-id` 纳入备份说明；pin 关键挂载 |

---

## 12. 落地计划（阶段 / 文件级改动点 / 测试点 / DoD）

### P0 — 打通一条最小可用链路（协议 + 路由 + 凭据 + 硬约束修复）

| 改动点 | 文件 |
|--------|------|
| `MountSpec`/`MountAuth`/`MountLimits`/`MountLabels` + `Register.Mounts` + `RegisterAck.AcceptedMounts/RejectedMounts` | `internal/gateway/wire/messages.go` |
| slug 派生/校验/保留字 | `internal/gateway/mcpmount/slug.go`（新） |
| 挂载登记表 + 令牌表 + 持久化 | `internal/gateway/mcpmount/{registry,tokens,store,persist}.go`（新） |
| 挂载入口 handler（认证/作用域/禁用/并发/剥前缀/代理/审计/指标） | `internal/gateway/mcpmount/handler.go`（新） |
| 路由注册 + 保留前缀 + 启动日志 + flags | `cmd/gateway/main.go`、`cmd/gateway/console.go`、`cmd/gateway/flagset.go` |
| 审计类型 / Prometheus 指标 | `internal/gateway/audit`、`cmd/gateway/main.go: writePrometheusMetrics` |
| agent 挂载转发（`MountForward`、`X-AICLI-Mount` 解析、mount 级 body/timeout、Bearer 注入、未知 slug fail-closed） | `internal/gateway/agent/{forward.go,config.go,controller.go}` |
| 请求体上限从 `1 MiB` 改为可配（默认 8 MiB） | `internal/gateway/tunnel/proxy.go`、`internal/gateway/agent/forward.go` |
| remote-tools 内嵌 agent + 挂载自身 | `cmd/aicli-remote-tools/main.go`、`internal/remotetools/{config.go,gateway_mount.go(新),server.go}` |

**测试点**：`mcpmount` 包表驱动单测；`tunnel` 侧复用 `proxy_test.go`/`route_test.go` 形状；端到端 `cmd/gateway/mount_e2e_test.go`（`httptest` 起 gateway + 真 `tunnel.Server` + 假 agent 连接或真 `agent.Controller` + 真实 `mcp.NewStreamableHTTPHandler` 本地服务）跑 `initialize`→`tools/list`→`tools/call`。

**DoD**：外网机器用 `curl`/`aicli chat --remote` 经 `/m/{slug}/mcp` 调到 NAT 后 daemon；重启节点 URL 不变；撤销令牌 5s 内 401；`git grep` 证明本地 token 未出现在任何 gateway 侧日志/响应/配置字段。

### P1 — 多挂载与运维可用性

`--mount-upstream` 多挂；`gateway` 子命令 + `doctor` 扩展；`X-AICLI-Mount` 覆盖与 spoof 回归；挂载级熔断与并发闸；`mount-id` 迁移/备份说明；**`--remote` 短语法挂载化**（`spec.go: listenAddress` 保留 scheme+path、http 形态支持 `?token_env=`，见 §10 第 12 条；附 `spec_test.go` 用例：`--remote https://gw/m/abc` → `https://gw/m/abc/mcp`）；文档 `docs/user-guide/aicli-remote-tools.md` 增「经 gateway 暴露」章节、`docs/gateway/11-security-config-examples.md` 增挂载加固样例。

**DoD**：同机两个不同 profile 的 daemon 经一条隧道暴露两个 URL，工具前缀互不串；其中一个节点离线只影响自己。

### P2 — 管理面与控制台

`/api/mounts*`（§11.1）+ 控制台「挂载」页 + 一次性令牌展示 + `probe` 自检 + `MOUNT_UPDATE` 帧（运行中增删挂载）+ stale 挂载清理。

**DoD**：不改节点配置即可在控制台完成"颁发/撤销/急停"；`probe` 报告工具数与在线状态。

### P3 — 泛化与配额

挂载非 remote-tools 的第三方 Streamable HTTP MCP（形态 C）；per-token 配额与用量报表；上游 readiness 探测（探测失败不声明挂载，控制台标"未就绪"）；可选 mTLS 外部客户端凭据（与 `remotetools` 的 `clientCAFile` 语义对齐）。

### P4 — 面向 MCP 规范演进

OAuth 2.1 授权流（MCP 规范方向：`WWW-Authenticate` + 元数据发现）替代裸 `amt_`；请求体分片流（去掉总量上限，改为 WINDOW_UPDATE 驱动的流式上传）；挂载级多租户/团队隔离（**当前明确非目标**，见 §14）。

**非目标**：多租户 SaaS 化 MCP 市场；把 stdio MCP 转成 HTTP 挂载（已有 `toolctl serve --http` 这条正路，挂载层不重复实现）；公网侧内容缓存/Mock 重放；跨 owner 共享挂载。

---

## 13. 测试矩阵

| 层 | 用例 | 断言要点 |
|----|------|----------|
| 纯函数 | slug 派生稳定性（同 identity 多次同值）、字符集、长度、保留字拒绝、pin 校验 | 跨进程/跨 gateway 实例同值；非法字符与 `/api`/`index.html` 等被拒 |
| Registry | Upsert/覆盖转 live/`MarkStale`/`RemoveStale`/冲突返回既有 mount_id | 同 slug 不同 mount_id → `ErrSlugConflict`，**不改名** |
| TokenStore | 明文只回一次、只存哈希、撤销即时、过期即时、作用域、purge 需先撤销 | 与 `device_test.go` 同形状 |
| Handler 认证 | 缺/错/撤销/过期/作用域外/禁用/未注册/离线/并发满/body 超限 | 状态码 401/401/401/401/403/403/404/502/429/413 逐条；401 带 `WWW-Authenticate`；无 CORS 头 |
| Handler 代理 | `X-AICLI-Mount`/`X-Forwarded-Prefix` 覆写（客户端伪造被丢）；`Authorization` 不进隧道；`Cookie`/`Host` 不进隧道 | 头表 §5.2 逐条 |
| routeAllowed | 挂载本地 path 命中；`/mcp/../admin`、`/mcpx`、空 Routes | 与既有 `route_test.go` 对齐（前缀边界 `/mcp` 不误匹配 `/mcpx`） |
| agent 转发 | 未知 slug → `reset(mount_unknown)` 且不回落默认目标；Bearer 从 env 注入；per-mount 超时/体上限；`STREAM_DATA` 阶段上限判定 | `inboundStream` 在 `handleStreamOpen` 已绑定 mount |
| 端到端（真 SDK） | gateway + tunnel + agent + 真 `mcp.StreamableHTTPHandler` 回环服务：`initialize`/`tools/list`/`tools/call`/SSE progress/大结果 | **必须显式覆盖 DNS-rebinding 保护路径**（Host 为回环 → 通过；构造透传公网 Host 的用例 → 403，锁死契约） |
| 端到端（真 remote-tools） | `remotetools.Server` over loopback HTTP + `--http-token-env`；profile readonly 工具面；审计落盘无 token | `tools/call` 结果与直连字节级等价（对齐现有 `*_local_remote_equivalence_test.go` 思路） |
| 多挂载 | 同节点 2 upstream（不同端口/不同 token env）并行调用；交错不串 | 每个请求只注入自己的凭据 |
| 流式与超时 | 慢响应跨 `IdleTimeout`；超 mount timeout → 客户端见 reset；断隧道 → 502 | 不出现"半截 200 无终态" |
| 兼容回归 | 老 agent（无 `mounts`）+ 新 gateway；新 agent + 老 gateway（忽略未知字段）；`/n/` 行为不变；owner 未设密码时 `/m/` 仍 401 | 无协议 bump；`/n/` 与 `/m/` 互不污染 |
| 安全 | 未鉴权挂载 URL 不可达；限流触发 429 + ban；伪造 slug 不越界；日志/错误体/指标不含令牌与 upstream 明文 | `audit_secret_hygiene` 同思路的挂载版 |

---

## 14. 设计取舍

| 议题 | 决策 | 理由 / 被否决项 |
|------|------|----------------|
| 穿透方式 | **复用既有反向隧道**（`/gw/agent` + `STREAM_*`） | 已实现且带流控、心跳、快照、failover；否决：新引 `frp`/`cloudflared`（多一个进程与信任域）、本机直绑 `0.0.0.0`（`validateHTTPAuth` 明确防的就是这个） |
| 入口命名空间 | 新增 `/m/{slug}`，不复用 `/n/{node}` | `/n/` 绑 owner cookie 且能力=整节点 web；程序化 Bearer + 最小能力必须独立入口。否决：给 `/n/` 开 cookie 例外（等于把 `/api/runtime/*` 交给令牌持有者） |
| 路径粒度 | **每挂载一段唯一 slug**，不用 query/header 区分 | 路由天然、可观测（日志按路径聚合）、可反代/限流/WAF 规则化；query 令牌会进日志；单挂载共用 `/mcp` 会让一个令牌打通全部 workspace |
| slug 生成 | **节点确定性派生 + 网关唯一性校验**（可 pin） | 重启/failover URL 不变；纯网关分配会把稳定性寄托在 `mounts.json` 不丢上（换机/清数据即变，客户端全断且难归因）。否决：从 NodeID 或 workspace 路径派生（§4.2） |
| 冲突处理 | 拒绝后到者，**绝不静默改名** | 静默改名 = URL 漂移 = 极难定位；本仓库取向是显式失败（[02 §3.4](./02-architecture.md)） |
| 路径改写位置 | **网关剥前缀**（`/m/{slug}` → `/mcp`），本地零改造 | 与 [02 §4.2](./02-architecture.md) 一致；否决：把公网路径原样送到本地（则本地必须为每个 slug 配 mux，且挂载改名要改本机） |
| 多挂载寻址 | 隧道内 `X-AICLI-Mount` 头传 slug（网关覆写） | 一隧道承载 N upstream 的最小增量；未知 slug fail-closed。否决：一挂载一隧道（连接/心跳/failover ×N，注册表噪声）；用端口区分（本机端口不可路由到网关） |
| 凭据模型 | **两套**：外部 `amt_`（网关）+ 本地 `Bearer`（本机 env） | 保持"本机令牌不出本机"（README 不变量 2）；否决：外部令牌直传本地（则令牌进网关配置与日志，且轮换要通知本机） |
| 本地凭据注入形式 | agent 按挂载 `token_env` 注入 `Authorization: Bearer` | `remotetools.authorized()` 只认 Bearer/mTLS；否决：让本地 daemon 改认 `X-AICLI-Token`（把 runtime 写令牌信任域扩到文件/shell 面） |
| 令牌呈现形式 | `Authorization: Bearer amt_…` 唯一 | URL 令牌入日志；cookie 令牌引入 CSRF/浏览器面 |
| 协议演进 | REGISTER **additive** 字段，不 bump 版本 | `serveConn` 严格版本相等，bump = 全体老 agent 掉线 |
| 挂载与 web 面共存 | 同一节点可同时有 `/n/`（人）与 `/m/`（程序），routes 分别声明 | 能力隔离；避免为暴露 MCP 而被迫声明整节点 web |
| 多租户 | 明确非目标 | 延续 [README §1](./README.md)「单用户、多设备、多会话」 |

---

## 15. 落地对接点索引

| 需要改的东西 | 精确位置 |
|--------------|----------|
| 挂载声明/回执 | `internal/gateway/wire/messages.go`（`Register`、`RegisterAck`） |
| 隧道注册/快照/stale | `internal/gateway/tunnel/server.go: serveConn`（`reg → Node`）、`tunnel/registry.go`、`tunnel/persist.go` |
| 反向代理入口与白名单 | `internal/gateway/tunnel/proxy.go: ServeProxy`、`routeAllowed`、`maxProxyRequestBody` |
| 网关路由与认证装饰器 | `cmd/gateway/main.go`（`/n/` 注册处 855 行附近、`limited`、`owner`）、`cmd/gateway/console.go: hasReservedPrefix` |
| 凭据设施同构参照 | `internal/gateway/device/device.go` + `device/persist.go` |
| 审计/指标 | `internal/gateway/audit`、`cmd/gateway/main.go: gatewayMetrics/writePrometheusMetrics` |
| 限流/封禁 | `internal/gateway/ratelimit`（`Resolver`/`BanList`） |
| 本机转发 | `internal/gateway/agent/forward.go`（`ForwardConfig`、`handleStreamOpen/Data`、`forwardStream`）、`agent/wsconn.go`（`Register`/`ForwardProvider`）、`agent/controller.go: SetForward/Reconnect` |
| 节点身份与稳定身份 | `agent/nodeid.go`（易变，**不可用于 slug**）；新增 `~/.aicli/data/gateway-agent/mount-id` |
| remote-tools 服务与配置 | `internal/remotetools/{server.go,config.go}`、`cmd/aicli-remote-tools/main.go` |
| 客户端消费 | `internal/remotetargets/registry.go: httpURL/case TransportHTTP`（保留 scheme+path 的路径）、`spec.go: listenAddress`（P1 需补 path/scheme/`token_env`）、`internal/mcp/config/types.go`（`url`/`headers`） |
| 文档 | 本文件；`docs/gateway/README.md` 文档地图；`docs/user-guide/aicli-remote-tools.md`；`docs/gateway/11-security-config-examples.md` |

---

## 16. 未决问题（需要在 P0 前拍板）

1. **root 风格是否默认开启**：`/xxxxxx/mcp` 最短、最贴合直觉，但把动态 slug 放进根命名空间，要求控制台保留前缀改成"已注册 slug 集合"判定。建议默认 `/m/{slug}/mcp`，root 作 `--mount-path-style=root` 满足洁癖需求。
2. **`mount-id` 的备份语义**：换机/重装导致 `mount-id` 丢失 = 全部公网 URL 变化。是否提供 `--mount-slug` pin 为推荐做法并在 `doctor` 里告警？（倾向：单挂载关键的生产场景必须 pin，并在文档写明。）
3. **`X-AICLI-Mount` 是否升级为协议字段**：走 `StreamOpen` 新字段（更干净，需两侧同步升级）vs 走 header（零协议变更、老 agent 忽略即安全降级）。当前选 header，理由是失败方向安全（老 agent 收到挂载流会走默认目标 → 由 route allowlist 403，而不是静默打到错误服务）。**待核实项**：老 agent + 新 gateway 的挂载请求必须以 `mount_not_supported` 显式失败，不能出现"打到 runtime-server 默认目标"——P0 用例必须锁这条（办法：agent 版本能力位 `mcp-mount` + 网关只在节点声明 `mounts` 时才发布挂载；未声明则不发布）。
4. **挂载级速率/配额是否落令牌维度**：per-token 限流 vs per-mount 限流，先用哪个做默认（倾向：mount 默认宽松、token 默认 600rpm，出问题在 token 维度定位更准）。
5. **第三方 stateful MCP 的会话粘性**：当前天然成立（一挂载一节点），但节点重启后 `Mcp-Session-Id` 失效。是否在 `probe` 里显式报"stateful 挂载，重启需重连"？
---

## 17. 实现进度（2026-10-29，P0 第一刀）

### 17.1 已落地（`go build ./...` 与相关包单测通过）

| 范围 | 文件 | 内容 |
|------|------|------|
| 协议类型 | `internal/gateway/wire/messages.go` | `MountSpec` / `MountAuth` / `MountLimits` / `MountLabels` / `MountAck`；`Register.Mounts`、`RegisterAck.AcceptedMounts/RejectedMounts`（additive，未 bump 版本）；`MountLabels` 含 hostname/os/arch/version/instance/backend/read_only/tool_count/tools/roots |
| slug 规则 | `internal/gateway/mcpmount/slug.go` | `DeriveSlug`（sha256 → base32hex，8 字符，稳定）、`ValidateSlug`（保留段/控制台静态文件）、`NormalizeLocalPath`（拒 `..`/query）、`PublicPath`/`PublicURL`（namespaced 与 root 两种风格） |
| 挂载登记表 | `internal/gateway/mcpmount/registry.go` | `Upsert`（同 mount 幂等、清 stale、保留 Disabled、pin 变更释放旧 slug）、`SlugConflictError`、`Get/GetByMountID/List`、`MarkStale/Bind/RemoveStale/Restore`、`SetDisabled` |
| 令牌 | `internal/gateway/mcpmount/tokens.go` | `amt_` 前缀、32 字节熵、只存 SHA-256、按挂载作用域（非通配）、TTL/过期、撤销/两段式删除、`CountForMount`、恒定时间比较在调用侧（`Authenticate` 用 map + TLS-safe 语义同 device） |
| 持久化 | `internal/gateway/mcpmount/persist.go` | `mounts.json` / `mount-tokens.json` 分文件、0600、tmp+rename 原子写、恢复即 stale |
| 挂载入口 | `internal/gateway/mcpmount/handler.go` | 先认证后查表（无枚举 oracle）、401/403/404/502/429、`Retry-After`、按挂载并发闸、`Proxy` 接口解耦 tunnel、`Event` 观测（审计/指标输入）、SSE 透传的 `statusRecorder` |
| 本机转发 | `internal/gateway/agent/forward.go` | `ForwardConfig.Mounts` + `MountForward`；`X-AICLI-Mount` 解析；未知 slug `reset(mount_unknown)` **不回落默认目标**；mount 级 `TokenEnv`→`Authorization: Bearer`（或自定义头/`none`）、超时、body 上限；`X-AICLI-Mount`/`X-AICLI-Token` 不再透传上游 |

单测覆盖：slug 稳定性/保留字、冲突拒绝且不改名、pin 变更、stale 清理、恢复转 stale、令牌作用域/过期/撤销/持久化不含明文、认证矩阵（缺/错/越权/禁用/离线/未知/遍历）、并发 429、agent 目标解析与头剥离。

### 17.2 待接线（下一刀的顺序）

1. **`internal/gateway/tunnel`**：`wire.Register.NodeStable`（新增，稳定身份）→ `Node` 字段；注册流程 `PrepareMounts`（校验+唯一性+落表，先标 stale）→ 写 `REGISTER_ACK` → 会话入表 → `ActivateMounts`；断连 `DeactivateMounts`（标 stale，URL 保号）。回执冲突原因 `mount_slug_conflict` / `mount_slug_missing`。
2. **`cmd/gateway`**：`--mount-path-style`、`--mount-token-ttl`、`--mount-max-body` 等 flags；`/m/{slug}/` 路由 + `limited`（**不套** owner cookie）；`hasReservedPrefix` 增补 `/m`；`mounts.json`/`mount-tokens.json` 打开与关闭落盘；审计类型与 Prometheus 指标；`GET/POST/DELETE /api/mounts*`。
3. **`cmd/aicli-remote-tools`**：`gateway.*` 配置 + flags；`mount-id` 持久化（`~/.aicli/data/gateway-agent/mount-id`，0600）；默认挂载自身 + `--mount-upstream` 多挂；`doctor`/`gateway status` 输出公网 URL 与 rejected 原因。
4. **端到端**：真实 `mcp.NewStreamableHTTPHandler` 回环服务 + 真 `tunnel.Server` + 真 `agent.Controller`，覆盖 `initialize → tools/list → tools/call`、SSE、>1MiB body、节点离线 502、未知 slug 不回落。
5. **P1 客户端短语法**：`remotetargets.Spec.listenAddress` 保留 scheme+path、http 形态支持 `?token_env=`（§10 第 12 条）。

### 17.3 与设计稿的差异（实现时收敛）

- `MountSpec.Slug` **必须由节点发送**（节点才有稳定身份）；gateway 空值一律 `mount_slug_missing` 拒绝，不做"中心派生"（见 §3.3 注释修订）。
- 令牌存储的 `Prefix` 取前 12 字符（`amt_` + 8 位熵指纹），比设备令牌的 8 字符更长，因为前缀本身占用 4 字符。
- 挂载入口**先认证后查表**：未认证调用未知 slug 得到 401 而不是 404（避免枚举 oracle）；已认证调用未知 slug 仍是 404（与 §7.3 矩阵一致）。

### 17.4 P0 实机验收（2026-10-29，Windows / loopback 双进程）

用真实二进制跑了一遍全链路（gateway :18090 + aicli-remote-tools :18775，测试目录已清理）：

```text
节点侧 stderr   aicli-remote-tools: mount f60250tp -> http://127.0.0.1:18090/m/f60250tp/mcp
节点身份        .aicli/.../identity = 23bb3d2dcee0b32c2aa7bd93095531ab（mount_id = 身份/smoke）
网关 mounts.json slug=f60250tp · local_path=/mcp · limits{8MiB,600s} · labels{profile:readonly,…}
网关 tokens.json 只存 prefix=amt_Ky-1iOAo + sha256 hash（无明文），撤销位 revoked=true
```

| 验收项 | 结果 |
|--------|------|
| `initialize` 经 `/m/{slug}/mcp` | `200 text/event-stream`，`serverInfo{name:"toolkit-mcp"}` |
| `tools/list` | 返回 `glob/grep/ls/view`（readonly profile 的工具面，与本地直连一致） |
| `tools/call ls` | 正常执行并在 jail 内返回（`_meta.workspace` 指向节点工作区） |
| 错误令牌 | `401`（`token_invalid`，审计留痕 + 指标计数） |
| 未知 slug + 合法令牌 | `404`（`mount_not_found`） |
| 控制台禁用 / 恢复 | `403 mount_disabled` → `200`（`mount_disabled/mount_enabled` 审计） |
| 令牌撤销 | 下一次调用即 `401`（`mount_token_revoked` 审计） |
| 网关重启 | 挂载记录以 `stale` 恢复、URL 保号；节点重连后自动转 live（`live_nodes` 0→1） |
| 指标 | `aicli_gateway_mount_requests_total` / `_auth_failures_total` / `_mounts_registered` / `_mounts_live` 均有值 |

**这一轮实机跑出的两个真实缺陷（已修复并有回归用例）**：

1. **网关没有把 slug 送进隧道**：`ServeProxy` 只转发路径，agent 落到空的默认目标，返回 `node has no local web service`。
   → 新增 `tunnel.Server.ServeProxyMount(w,r,nodeID,path,mount)`，由**网关**写入 `X-AICLI-Mount` 并把 `X-Forwarded-Prefix` 改写成 `/m/{slug}`；`mcpmount.Proxy` 接口随之改为 `ServeProxyMount`。`mount_e2e_test.go` 锁死「伪造头被覆盖」「默认目标不受挂载路由污染」「未知 slug 502 不回落」。
2. **默认身份文件路径不可写**：`~/.aicli/data/gateway-agent` 在受限账户下 `mkdir` 被拒，导致挂载静默不启用。
   → 新增 `--gateway-identity-file`（配置项 `gateway.identityFile`），并在文档层把「身份文件必须可写且需备份」列为运维前置项（§10 第 12/13 类风险的同类）；失败时节点侧显式打印 `gateway disabled: …` 而不是静默。

**顺带修正的实现-文档偏差**：请求体上限最终落在**网关全局** `--proxy-max-request-body`（默认 8 MiB，覆盖 `/n/` 与 `/m/`），而非 §10 第 1 条设想的「按挂载」；节点侧仍是按挂载 `max_body_bytes`（默认 8 MiB）判定。若后续要严格 per-mount 网关上限，需要在 `serveProxy` 前按 slug 取限额——列为 P1 可选增强，不影响当前语义正确性。

### 17.5 剩余项（下一刀）

1. 控制台「挂载」页（`cmd/gateway/console/{app.js,index.html,style.css}`）：现只有 API，无 UI。
2. `aicli-remote-tools doctor` 增挂载字段（`gatewayUrlConfigured` / `mounts[].slug` / `identityFileWritable`）；新增 `gateway status` 子命令读取 `Controller.Status().Mounts`。
3. 文档：`docs/user-guide/aicli-remote-tools.md`「经 gateway 暴露」章节、`docs/gateway/11-security-config-examples.md` 挂载加固样例、`docs/gateway/10-runbook.md` 增补挂载条目（404/401/403/502/413 与 §11.5 对齐）。
4. `--remote` 短语法挂载化（§10 第 12 条）；运行中挂载变更的 `MOUNT_UPDATE` 帧（§6.3 P2）。
