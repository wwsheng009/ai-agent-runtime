# aicli-gateway（外网会话网关）设计方案

> 文档状态：设计草案（2026-10-11）
> 适用基线：`aicli`（TUI + 微前端 + 回环控制面）、`runtime-server`（HTTP API + Web 控制台）、`internal/mesh`（单机多进程网格）
> 目标：为「本机优先」的 AI Agent Runtime 提供一个可部署在公网、面向**多会话管理**的接入网关（hub），
> 让用户从任意浏览器 / 移动端反向访问自己电脑上的会话，而**无需在本地开放入站端口**。

---

## 1. 一句话方案

在公网部署一个 **aicli-gateway**：本地 `aicli TUI` 与 `runtime-server` 通过一条**出站长连接**
（`wss://<gateway>/gw/agent`）向网关注册自己的会话与服务地址；网关把外网浏览器的 HTTP/SSE 请求
**多路复用**到这条反向隧道，转发给本机的回环服务，并提供一个移动优先的**多会话管理控制台**。

```text
   外网浏览器 / 移动端                公网服务器（可直连）                 用户本机（NAT 之后）
   ┌───────────────┐        HTTPS/WSS        ┌──────────────────┐       出站 wss 长连接      ┌──────────────────┐
   │  管理控制台    │ ───────────────────▶  │   aicli-gateway   │ ◀────────────────────────── │  gateway agent   │
   │  (PWA/mobile)  │ ◀───────────────────  │  注册表 + 反向代理 │  多路复用流 + 心跳 + 续传    │  (内嵌 aicli)     │
   └───────────────┘        SSE / HTTP       └──────────────────┘                             └────────┬─────────┘
                                                                                             回环转发 │ 127.0.0.1
                                                                                                      ▼
                                                                                        aicli 微前端 /web/api/*
                                                                                        runtime-server /api/runtime/*
```

**核心不变量**

1. **出站即接入**：本地进程只发起出站连接，绝不监听公网端口；NAT / 防火墙无需配置。
2. **本机令牌不出本机**：`X-AICLI-Token`、runtime-server admin token 由 agent 在本机侧注入，
   浏览器与网关控制面永远拿不到它们。
3. **单用户、多设备、多会话**：不做多租户 / 团队 / RBAC；只做「一个 owner 管理自己的若干设备与会话」。
4. **流式保真**：SSE、Range 下载、分片上传、`Content-Disposition` 在反向链路上语义不降级。
5. **失败可降级**：网关不可达时本地 `aicli` / `runtime-server` 行为与今天完全一致（网关是增强，不是依赖）。

---

## 2. 为什么需要它（现状缺口）

| 入口 | 现状 | 外网可用性缺口 |
|------|------|----------------|
| `aicli` TUI | 回环 HTTP（`127.0.0.1:随机端口`）+ 微前端 `/web/` + `/web/api/*`；令牌 + Host/Origin 校验 | 默认只回环；`--web-host 0.0.0.0` 可监听但需自行解决公网暴露、TLS、鉴权、NAT |
| `runtime-server` | `127.0.0.1:8101` + `/api/runtime/*` + 内嵌 SPA；admin token / 回环放行 | 默认只回环；直接暴露到公网缺少面向公网的认证与路由层 |
| Web 控制台 | 经 `VITE_API_BASE_URL` 连 runtime-server | 只能连到同一个 runtime-server，跨设备 / 跨进程无统一视图 |
| `aicli-mesh` | 单机多进程 hub（nodes/bindings/leases/journal，文件系统即真相） | 仅限**同一台机器**，回环控制面，不能跨广域网 |

结论：需要一个**跨广域网**的 hub，把三者的会话与服务统一纳管，且以**反向连接**方式穿透 NAT。

---

## 3. 文档地图

| 文档 | 回答什么问题 |
|------|--------------|
| [01-requirements.md](./01-requirements.md) | 背景、角色、场景、功能/非功能需求、约束、非目标、风险（需求分析与扩展） |
| [02-architecture.md](./02-architecture.md) | 组件、部署拓扑、数据模型、会话聚合、请求路由、生命周期 |
| [03-protocol.md](./03-protocol.md) | 注册协议、心跳、多路复用帧协议、SSE/上传/下载保真、版本协商 |
| [04-security.md](./04-security.md) | 单用户认证、设备令牌、能力范围、本机令牌隔离、审计与限流 |
| [05-wan-and-mobile.md](./05-wan-and-mobile.md) | 广域网连接优化（多路复用/续传/压缩/背压）与移动端优化（PWA/推送/低带宽） |
| [06-implementation-plan.md](./06-implementation-plan.md) | 分阶段落地计划、对 `micro web client` 与 `runtime-server` 的具体改造、验收标准 |
| [07-console.md](./07-console.md) | 轻量独立控制台设计（参考 `micro web client` 范式：`go:embed` + 零构建 + 原生 ES 模块） |
| [08-notifications.md](./08-notifications.md) | 事件驱动的通知/注意力模型（turn 完成/审批/提问）、与 hooks 的关系、会话 Web UI 反向访问 |
| [09-edge-hardening.md](./09-edge-hardening.md) | 公网边缘防护（静态 deny、可信代理、IP 限流/惩罚封禁、过载、可观测、Runbook），参考 ai-gateway Edge 安全 |
| [10-runbook.md](./10-runbook.md) | 安全事件处置 Runbook（P1–P4 分级、检查清单、命令与阈值、误封恢复、回滚、复盘） |
| [11-security-config-examples.md](./11-security-config-examples.md) | 可直接复制的安全配置示例（边缘防护、认证加固、GeoIP、monitor→enforce、上线检查） |
| [12-connection-control.md](./12-connection-control.md) | 连接控制面：自动/手动连接、aicli TUI `/gateway` 命令、runtime-server 前端入口与 API、状态机与语义 |
| [13-connection-control-tasks.md](./13-connection-control-tasks.md) | 连接控制面实现任务清单（文件级改动点 + 测试点 + PR 序列 + DoD） |
| [14-implementation-record.md](./14-implementation-record.md) | 实现记录：落地状态、运行方式、命令/API 参考、测试、已知缺口 |

---

## 4. 关键设计取舍（摘要）

| 议题 | 决策 | 理由 |
|------|------|------|
| 反向连接方式 | **出站 WebSocket + 自定义多路复用帧**（`gorilla/websocket` 已是现有依赖） | 穿透 NAT/代理最稳；避免引入新的隧道库；全双工 + 单连接多流 |
| 是否扩展 `runtime-server` 当网关 | **否，新增独立二进制 `aicli-gateway`** | 网关是公网暴露面，安全边界、部署形态、升级节奏都与本地 runtime-server 不同 |
| 会话归属 | 会话仍归属本地进程；网关只做**注册表 + 路由**，不落会话内容 | 延续「本机优先 / 文件系统即真相」；网关被攻破也拿不到会话正文 |
| 认证 | 单 owner 登录 + 每设备注册令牌（可撤销）；浏览器会话用 HttpOnly Cookie | 无多用户需求，最小可信面 |
| 会话视图 | 网关聚合 `aicli` 与 `runtime-server` 的会话元数据，正文按需反向代理 | 控制台轻量、移动端友好 |
| 管理控制台 | **新增轻量独立控制台**（`go:embed` + 零构建 + 原生 ES 模块，范式对齐 `micro web client`） | 部署零前端依赖、单二进制自带；首期不复用 React/Vite 工作台，见 [07](./07-console.md) |
| 通知机制 | **订阅本地运行时事件总线**（`agent.turn.finished`/`approval_requested`/`question_asked`…）派生 `attention` + `NOTIFY`；hooks 仅作用户自定义外呼扩展点 | 事件总线是权威、已 SSE 暴露、零侵入 turn 路径；不用 hook 做网关通知（同步 `EventStop` 会加延迟），见 [08](./08-notifications.md) |
| 公网入口防护 | **分层防御**：L0 Edge admission（静态 deny/可信代理/IP 限流/惩罚封禁/过载/未知路径）→ L1 认证 → L2 隧道代理 → L3 本地；策略先 `monitor` 再 `enforce` | 借鉴 ai-gateway Edge 安全；恶意流量在最外层廉价拒绝，见 [09](./09-edge-hardening.md) |
| 连接控制 | **自动 + 手动并存**：启动按配置自动注册；`aicli` TUI `/gateway` 命令、`runtime-server` 前端与 `/api/runtime/gateway/*` 手动连/断；`disconnect` 为运行态（不改配置），`enable/disable` 才持久化 | 运行态与持久化分离，避免"断开即改配置"，见 [12](./12-connection-control.md) |
| 本地回环暴露 | 本地**继续只监听回环**；agent 在进程内转发 | 不改变现有安全模型，无需开放端口 |
| 与 `aicli-mesh` 关系 | 互补：mesh 管单机多进程，gateway 管广域网多设备；gateway 可选用 mesh 视图发现本机节点 | 复用已有节点/会话模型，不重复造轮子 |

---

## 5. 与现有代码的对接点（便于落地时定位）

| 能力 | 现有位置 | 网关方案中的角色 |
|------|----------|------------------|
| 微前端页面与远程 API | `backend/cmd/aicli/commands/web/*`、`docs/aicli/web-remote-api.md` | 反向代理目标；需改造为 base-path 感知 + 移动优先 |
| 回环控制面启动 | `backend/cmd/aicli/pprof.go` | agent 挂载点（web server 就绪后注册） |
| 节点/会话模型 | `backend/internal/mesh/{registry,host,view}.go` | 注册信息的字段参考（capabilities / session / workspace / liveness） |
| runtime-server 路由 | `backend/cmd/runtime-server/main.go`（`router`）、`internal/api/runtimeapi/handler.go` | 反向代理目标；agent 挂载点 |
| 内嵌静态资源范式 | `backend/internal/webui/*`、`backend/cmd/aicli/commands/web_page.go`（`//go:embed`） | 网关**轻量独立控制台**照搬 `//go:embed` + 零构建范式（见 [07](./07-console.md)）；`webui` 的 React SPA 仅作为被代理的 runtime-server 工作台 |
| 网关持久化 | usage ledger 的 `database.dsn` 默认 `~/.aicli/data/usage_ledger.db`（`configs/config.yaml:93`，原 `./data/gateway.db`） | ✅ **命名冲突已解决**：ledger 已改名并锚定用户目录；网关数据文件用 `gateway-registry.db` 以区别于 ledger 与 LLM provider `gateway`（见 [02](./02-architecture.md) §8.1） |

---

## 6. 阅读顺序建议

1. 先读 [01-requirements.md](./01-requirements.md) 对齐需求边界与非目标；
2. 再读 [02-architecture.md](./02-architecture.md) 建立整体心智模型；
3. 实现前精读 [03-protocol.md](./03-protocol.md) 与 [04-security.md](./04-security.md)；
4. 体验与性能相关改动见 [05-wan-and-mobile.md](./05-wan-and-mobile.md)；
5. 控制台实现见 [07-console.md](./07-console.md)（轻量独立控制台，参考 micro web client）；
6. 通知与会话 Web UI 反向访问见 [08-notifications.md](./08-notifications.md)；
7. 面向公网的安全加固见 [04-security.md](./04-security.md) 与 [09-edge-hardening.md](./09-edge-hardening.md)；
8. 安全事件处置见 [10-runbook.md](./10-runbook.md)，配置示例见 [11-security-config-examples.md](./11-security-config-examples.md)；
9. 手动连接/断开控制面见 [12-connection-control.md](./12-connection-control.md)；
10. 控制面实现任务见 [13-connection-control-tasks.md](./13-connection-control-tasks.md)；
11. 已实现的落地状态与运行方式见 [14-implementation-record.md](./14-implementation-record.md)；
12. 排期与验收见 [06-implementation-plan.md](./06-implementation-plan.md)。
