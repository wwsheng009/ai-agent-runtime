# 06 · 落地计划与本地程序改造

> 上游：全部设计文档｜产出：分阶段计划、对 `micro web client` / `runtime-server` / `aicli` 的具体改造、测试与验收。
> 原则：**增量、可回退、网关可选**。每一步都不破坏"未配置网关时行为与现状一致"。

---

## 1. 里程碑总览

| 阶段 | 目标 | 关键交付 | 退出标准 |
|------|------|----------|----------|
| **P0 打通** | 一条隧道 + 单会话反向访问 | `internal/gateway/wire` + `internal/gateway/tunnel` + `internal/gateway/agent` + `cmd/gateway` 最小实现；`aicli` 接入 | 外网浏览器经网关打开一个 `aicli` 会话，能看屏幕 + 发消息 |
| **P1 多会话** | 注册表 + 控制台 + 多会话 | registry + 管理 API + 控制台（会话列表/切换）+ `runtime-server` 接入 | 多设备多会话统一列表、切换、打开；SSE 实时 |
| **P2 体验** | 广域网 + 移动端 | 差分/合并/背压/续传；PWA + 推送 + 低带宽；base-path 感知 | 弱网可用、可安装、软键盘无遮挡、断线自愈 |
| **P3 健壮** | 运维与 HA | 审计/指标、令牌轮换、版本协商完善；可选 QUIC/HA | 撤销即断、指标齐全、可选多实例 |

---

## 2. P0：打通（最小可用隧道）

### 2.1 交付物

```text
internal/gateway/wire/            # 帧定义 + 控制消息（golden 测试）
internal/gateway/tunnel/          # 服务端：握手、REGISTER、多路复用、流调度、fencing
internal/gateway/agent/           # 客户端：连接、心跳、转发到本机回环
internal/gateway/console/         # 轻量独立控制台（//go:embed console/，零构建，见 07 文档）
internal/gateway/notify/          # 通知聚合：attention + NOTIFY + Web Push（见 08 文档）；ntfy 第二通道在 cmd/gateway 与 internal/gateway/ntfy
internal/gateway/edge/            # 公网边缘防护：静态 deny/IP 限流/惩罚封禁/过载/可信代理（见 09 文档）
cmd/gateway/                      # 二进制：--listen/--data-dir/--tls-*/init
cmd/aicli/                        # --gateway/--gateway-token；启动 agent
```

### 2.2 步骤

1. **`wire` 包**：实现 §03 的帧编解码 + 控制消息结构；golden 测试固定字节布局（借鉴 mesh schema/golden 思路）。
2. **隧道服务端**：`/gw/agent` 升级 → `HELLO/WELCOME` → `REGISTER` → 内存注册表（暂不落库）→ 流调度。
3. **agent**：连接、注册、心跳；把 `STREAM_OPEN` 转成对 `local.base_url` 的 `http.Request`，响应分块回流。
4. **最小网关路由**：`/n/{node_id}/web/...` 反向代理（无控制台，直接用 URL 访问）。
5. **`aicli` 接入**：`--gateway <wss-url> --gateway-token <tok>`；在回环 web server 就绪后启动 agent。

### 2.3 验收（P0）

- [ ] `aicli chat --web --gateway wss://... --gateway-token agw_...` 后，网关侧 `/n/{node}/web/` 可打开微前端。
- [ ] 外网浏览器能读到屏幕快照、能 `POST invoke` 发消息（写令牌由 agent 注入，浏览器无感）。
- [ ] 未配置 `--gateway` 时，`aicli` 行为与现状**逐字一致**（回归测试）。
- [ ] 本地进程未新增任何公网监听端口（`netstat` 断言）。
- [ ] 隧道断开后 agent 自动重连。
- [ ] 网关**强制 TLS**（未配置证书且无可信反代时拒绝明文对外）；管理 API 匿名不可达。
- [ ] 静态 deny + 路径规范化生效；不可信来源的 `X-Forwarded-For` 被忽略（伪造 IP 无法绕过）。
- [ ] `/api/health` 与已认证会话在阈值内不受安全策略影响（正常请求门禁）。

---

## 3. P1：多会话与控制台

### 3.1 注册表与持久化

- `internal/gateway/registry`：内存 + SQLite（`gateway-registry.db`：nodes / sessions / devices / audit 表）。
- 落库字段见 [02-architecture.md](./02-architecture.md) §3；**只存元数据**。
- 心跳与 `SESSION_UPDATE` 更新内存，周期性/关键事件落库（避免每心跳写库）。
- 复用 mesh 词汇：`state`（online/offline）、`ownership`（owner/peer/conflict）、`attention`。

> **命名冲突已解决（2026-10-11）**：`backend/data/gateway.db` 曾是 **usage ledger 的
> SQLite**（非网关产物）——默认 DSN 来自 `configs/config.yaml:93`，由
> `internal/runtimeserver/usage_ledger.go`、`backend/cmd/aicli/commands/chat_actor_host.go`
> （`ledgerDSN`）写入，并被 `internal/knowledge/baseline_run_test.go` 读取；它随进程 CWD
> 落在仓库 `backend/data/`（`.gitignore:88`）。
> 现默认 DSN 已改为 **`~/.aicli/data/usage_ledger.db`**（ledger 补了 `~` 展开，落点锚定
> 用户目录），与网关彻底解耦。新网关仍使用 **`gateway-registry.db`**。详见 [02](./02-architecture.md) §8.1。

### 3.2 管理 API 与控制台

- 管理 API：`/api/auth/*`、`/api/nodes`、`/api/sessions`、`/api/devices`、`/api/audit`（见 02 §4.1）。
- 控制台：**新增轻量独立控制台**（决策已定，见 [07-console.md](./07-console.md)）——
  范式对齐 `aicli micro web client`：`//go:embed console/` + `index.html`/`style.css`/`app.js`/`js/*.js`（ES 模块）+ **零构建**，
  内嵌到 `cmd/gateway`。首期**不复用** `frontend/`（React/Vite 工作台）；若 P2+ 有深度功能需求再评估合并。
- 会话打开方式：`/s/{session_id}/web/`（内嵌 iframe 或跳转）；SSE 直连网关。
- **通知/注意力**（[08-notifications.md](./08-notifications.md)）：agent **订阅本地运行时事件总线**
  （`agent.turn.finished`/`approval_requested`/`question_asked`/`turn.suspended`/失败）派生
  `attention`（随 `SESSION_UPDATE`）与瞬时 `NOTIFY`；网关聚合到控制台 `/api/events`（徽标/收件箱/toast），
  带 `dedup_key` 去重与 presence 抑制。**不使用 hooks 实现网关通知**（`EventStop` 同步、会加延迟）。
- **连接控制面**（[12-connection-control.md](./12-connection-control.md)）：`internal/gateway/agent.Controller`
  （`Connect`/`Disconnect`/`Reconnect`/`Status`）；`aicli` TUI `/gateway` 命令；`runtime-server`
  `/api/runtime/gateway/*` + 前端 "Gateway" 设置页。`disconnect` 为运行态、`enable/disable` 才持久化。

### 3.3 `runtime-server` 接入

- 新增配置段 `gateway`（与 `aicli` 同源键名）；启动后启动 agent，`kind=runtime-server`，
  `routes=["/api/runtime","/api/agent","/healthz","/"]`，`capabilities` 含 `runtime-api`。
- 会话枚举：优先复用 runtime-server 现有会话列表（`/api/runtime/sessions`）投影为注册会话元数据。
- **约束**：`runtime-server` 继续只监听回环；不得因网关绑定公网。

### 3.4 验收（P1）

- [ ] 控制台列出多设备、多会话；同设备多会话正确分组。
- [ ] 点击会话可打开并实时看到输出（SSE）。
- [ ] 可在控制台切换会话、发消息、审批、回答、中断。
- [ ] 设备离线后控制台显示离线并可查看"最近活跃"。
- [ ] `runtime-server --gateway` 的 `/api/runtime/*` 经网关可访问。

---

## 4. P2：广域网与移动端体验

### 4.1 对 `micro web client` 的改造（重点）

| 改造项 | 说明 | 关联 |
|--------|------|------|
| **base-path 感知** | 所有请求以 `document.baseURI` / 注入的 `<base href>` 为基准，不再硬编码根绝对路径；被 `/n/{node}` 前缀代理时仍正确 | 02 §4.2 |
| **多会话切换器** | 页面内提供会话下拉/抽屉，调用本地 `/web/api/sessions` 并切换；移动端优先 | FR-19 |
| **注意力徽标** | 顶部/列表展示 waiting approval/question | FR-9 |
| **移动优先布局** | 窄屏单列、抽屉侧栏、可折叠工具输出、横向滚动代码块 | 05 §2 |
| **软键盘安全** | `visualViewport` + sticky composer + 安全区 | 05 §2.2 |
| **PWA** | manifest + service worker（缓存外壳，不缓存 API/SSE） | 05 §2.1 |
| **低带宽模式** | 差分/节流/按需历史/图片占位 | 05 §2.6 |
| **SSE 续订** | 前台恢复按 `Last-Event-ID`/`since_seq` 重连 | 05 §2.5 |
| **令牌处理** | 网关模式下不要求浏览器持有本机令牌（agent 注入）；本地直连模式保持不变 | 04 §2.3 |

### 4.2 对 `runtime-server` 的改造（可选增强）

| 改造项 | 说明 |
|--------|------|
| 网关状态端点 | `/api/runtime/gateway/status`（在线状态、node_id、控制台 URL；不泄露令牌） |
| 会话元数据投影 | 提供轻量会话摘要供 agent 上报（避免 agent 直接读存储） |
| SPA base-path | 内嵌 SPA 支持子路径部署（被网关前缀代理时） |
| 反向代理友好 | 确认 SSE/上传/下载经代理时行为（`X-Accel-Buffering`、Range） |

### 4.3 广域网优化（隧道侧）

- 差分屏幕（`delta_screen`）、事件合并窗口、每流信用背压、分级丢弃（见 05 §1）。
- 重连退避 + 抖动 + fencing + 版本协商完善。

### 4.3b 通知与推送（P2）

- 控制台 Web Push 订阅与投递（仅"某会话需要处理"，**不含正文**；[08](./08-notifications.md) §5）。
- **ntfy 第二通道**：与 Web Push 并行扇出（`--ntfy-url/--ntfy-topic`），用于 FCM/WNS 不可达时的兜底；高优先 `priority=5`。
- presence 抑制（用户正打开该会话时不 toast/推送）、免打扰与优先级（`approval`/`question` 可穿透）。

### 4.4 验收（P2）

- [ ] 见 [05-wan-and-mobile.md](./05-wan-and-mobile.md) §5 优化验收清单全部通过。
- [ ] 移动端真机（iOS Safari + Android Chrome）完成核心闭环。

### 4.5 公网安全加固（贯穿 P0–P2，参考 [09-edge-hardening.md](./09-edge-hardening.md)）

借鉴 `ai-gateway` Edge 安全设计，公网入口按**分层防御 + 分级风控 + 可回滚策略**落地：

| 阶段 | 安全项 | 说明 |
|------|--------|------|
| P0 | 静态 deny（path/prefix/method/CIDR）+ 路径规范化 + 未知路径 `count`；可信代理下的 client IP 解析 | 最外层廉价拒绝；不可信来源不信任 XFF |
| P0 | 强制 TLS；管理 API 不匿名可达；设备令牌哈希存储 | 基线安全默认值（[04](./04-security.md) §9） |
| P1 | IP 限流（`429`+`Retry-After`）、惩罚自动封禁（TTL/升级/解除/审计）、allowlist 豁免但危险路径仍拒 | `/api/health` 等正常请求不受影响 |
| P1 | 过载保护（全局/路径级 in-flight、pending、queue timeout） | 控制面写请求快速 `429` 不排队 |
| P1 | owner 登录限流（IP+账号+全局）+ 分级风控 + 会话失效（`session_version`）；安全事件审计 | 见 [04](./04-security.md) §2.1/§5 |
| P1 | 安全策略 `mode: monitor\|enforce` + 变更需 `reason` + 可回滚 | 先观察 7–14 天再 enforce |
| P2 | 低基数安全指标 + 安全事件聚合（Top IP/路径）+ 告警；Runbook | 见 [09](./09-edge-hardening.md) §8/§11 |
| P2 | 可选：Turnstile 人机识别、MFA、GeoIP（默认 monitor/fail-open） | 按需启用 |

---

## 5. P3：健壮与运维

- 审计与指标齐全（Prometheus 文本端点 + 结构化日志）。
- 设备令牌轮换 / 撤销即时生效；撤销后 agent **不重试**。
- 版本协商：major 不匹配明确拒绝；minor 能力协商。
- 可选：QUIC/HTTP3 传输；多实例 HA（注册表外置 + 会话粘滞）。
- 可选：Passkey；E2EE 评估（见 04 §8）。

---

## 6. 配置与 CLI 变更清单

### 6.1 `aicli`

```text
--gateway <wss://gw.example.com>      # 或 AICLI_GATEWAY_URL
--gateway-token <agw_...>             # 或 AICLI_GATEWAY_TOKEN（优先 env）
--gateway-name <name>                 # 或 AICLI_GATEWAY_NAME
--gateway=false                       # 显式关闭
```

- 配置键（分层配置栈）：`gateway.enabled/url/token/name/routes`。
- 启动日志新增一行：`Info: gateway registered node=<id> console=<url>`（stderr，便于无 TTY 捕获）。
- **TUI 命令**（[12-connection-control.md](./12-connection-control.md)）：
  `/gateway [status|connect|disconnect|reconnect|url|enable|disable|help]`。
  `disconnect` 为运行态（抑制自动重连、**不改配置**）；`enable/disable` 才写配置。
  接入 `chat_slash_command_catalog.go`（补全/帮助）与忙时策略；无头/ACP 仅暴露 `status`。
- 可选 CLI 对等：`aicli gateway status|connect|disconnect|reconnect|enable|disable`（脚本/排障；不控制已运行的 TUI）。

### 6.2 `runtime-server`

```text
--gateway <wss://gw.example.com>
--gateway-token <agw_...>
--gateway-name <name>
```

- 配置段 `gateway`（与 `aicli` 同源）。
- **本地控制 API**：`/api/runtime/gateway/{status,connect,disconnect,reconnect,config}`（回环/admin token，写操作返回更新后状态，**不回传 token 明文**）。
- **前端入口**：设置页 "Gateway"（`gateway-settings-page.tsx`，接入 `settings-dialog.tsx` 的 `activeSection`）：状态 + 连接/断开/重连 + 配置；过渡态禁用按钮。

### 6.3 `aicli-gateway`（新）

```text
aicli-gateway init                 # 初始化 owner 凭据 + 数据目录
aicli-gateway serve \              # `serve` 为可选位置参数（被忽略，后续 --flags 正常解析）
  --listen :8443 \
  --data-dir ./data \
  --tls-cert cert.pem --tls-key key.pem \
  --public-base-url https://gw.example.com \
  --trusted-proxies 127.0.0.1 \
  --max-nodes 64 --max-streams-per-node 256
aicli-gateway device create --name "vince-laptop"   # 打印一次性令牌
aicli-gateway device list / revoke <id>
aicli-gateway version
```

---

## 7. 测试策略

| 层 | 内容 |
|----|------|
| 单元 | `wire` 帧编解码 golden；注册表状态机（online/offline/conflict）；路径白名单规范化（`..`/编码绕过）；流控窗口；退避抖动 |
| 集成 | 本地起 `httptest` 上游 + 网关 + agent（内存环回），验证请求/响应/SSE/下载/上传保真 |
| E2E | 真实 `aicli` 进程 + 网关 + 浏览器（Playwright）完成"看/发/审批/中断/切换"闭环 |
| 弱网 | 注入延迟/丢包/断连（toxiproxy 或测试钩子），验证重连、续订、背压、内存有界 |
| 移动 | Playwright 设备模拟 + 真机手测：视口、键盘、PWA、触控 |
| 安全 | 认证/限流/白名单/头注入/令牌隔离的负向用例 |
| 回归 | **未配置网关**时 `aicli`/`runtime-server` 行为与基线逐字一致 |

复用现有 E2E 风格（`frontend/e2e/*`、`docs/e2e/*`）与 `scripts/test-aicli-debug-endpoints-e2e-*.ps1`。

---

## 8. 部署与运维

### 8.1 部署清单

1. 服务器：安装 `aicli-gateway` 二进制；配置域名 DNS → 服务器。
2. TLS：直接加载证书，或 Caddy/Nginx 终止 TLS 后反代（配置 `--trusted-proxies`）。
3. 初始化：`aicli-gateway init`（owner 密码）+ `device create`（得到设备令牌）。
4. 本地：在 `aicli` / `runtime-server` 配置 `gateway.url` + `gateway.token`。
5. 验证：控制台登录 → 看到设备与会话 → 打开会话。

### 8.2 运维

- **日志**：结构化；审计事件单独留存；不含正文/令牌。
- **指标**：在线节点、活跃隧道/流、字节、错误率、重连、延迟分位。
- **备份**：`gateway-registry.db`（注册表 + 审计）。
- **升级**：网关先升级（向后兼容旧 agent 的 minor），再升级本地；major 变更需同步。
- **故障**：网关不可用时本地零影响；控制台不可用时本地 TUI 零影响。

---

## 9. 风险登记（落地视角）

| 风险 | 阶段 | 缓解 |
|------|------|------|
| base-path 重写不彻底 | P1/P2 | 优先改造微前端为 base-path 感知；子域名方案作为备选 |
| SSE 经多层代理被缓冲 | P0/P2 | `X-Accel-Buffering: no`、`Cache-Control: no-cache`、flush；E2E 覆盖 |
| 隧道单连接成为瓶颈 | P2 | 单连接多流 + 背压；必要时按能力拆多条连接（SSE 单独一条） |
| 移动端后台挂起丢事件 | P2 | 前台快照对齐 + `Last-Event-ID` 续订 + 推送兜底 |
| `gateway.db` 命名冲突 | P0 | **已解决**：ledger 改名 `usage_ledger.db` 并锚定 `~/.aicli/data/`；网关用 `gateway-registry.db`（02 §8.1） |
| 本地进程被误配置为监听公网 | 全程 | 代码层禁止；启动断言 + 文档 + 测试 |
| 令牌泄露 | 全程 | 可撤销 + 绑定 node_id + 审计 + 仅存哈希 |

---

## 10. 待确认问题（Open Questions）

以下均已按最佳实践**定案**（保留编号以便追溯）：

1. ~~控制台路线~~：**已定——新增轻量独立控制台**（`go:embed` + 零构建，见 [07-console.md](./07-console.md)）；首期不复用 `frontend/`，P2+ 再评估合并。
2. ~~会话预览是否上报正文片段~~：**已定——默认不上报正文**（纯元数据）；预览**按需经反向代理拉取**（`/web/api/screen?msg_limit=1`），用完即弃；正文推送为 `gateway.report_preview` opt-in（截断+脱敏+TTL）。
3. ~~是否聚合同机多进程会话~~：**已定——默认每个进程独立连接，网关按 `device_name` 分组**；`internal/mesh` 聚合（机器 hub）列为 Phase 3 opt-in，且须遵守 mesh 令牌披露面收窄。
4. ~~是否跨设备合并展示~~：**已定——默认按设备分组**（主结构），另加**跨设备注意力收件箱 + 全局搜索**；不做无差异扁平合并；`workspace` 仅作次级分组维度（非权限边界）。
5. ~~是否需要网关侧会话搜索~~：**已定——需要**（关键词 = 标题 + 按需预览），与 #4 的收件箱/过滤同一入口。
6. ~~是否引入 QUIC~~：**已定——隧道保持 WS/TLS**；公网浏览器侧可先启用 HTTP/3（P2 可选，与隧道协议解耦）；隧道 QUIC 为 Phase 3 可选，按"网络切换导致的重连频率"测量门槛决定。
7. ~~`runtime-server` SPA base-path~~：**已定——需要支持**（`/n/{node}` 或 `/s/{id}` 前缀代理），与微前端 base-path 改造同批（P1–P2）。

新增待确认（来自 [08-notifications.md](./08-notifications.md)）：

8. 通知默认静音范围：`completed` 是否默认静音（只留徽标/收件箱），`approval`/`question` 才推送？
9. Web Push 的推送内容粒度：仅"某会话需要处理"（推荐）还是可带会话标题？（**均不含正文**）

---

## 11. 建议的下一步

1. **确认本设计文档**（尤其是 §10 的开放问题与 §3.2 控制台路线）。
2. 先做 **P0 打通**：`wire` + `tunnel` + `agent` + `cmd/gateway` 最小实现 + `aicli` 接入；
   用最小 E2E（本机 agent + 网关 + httptest 上游）验证多路复用与 SSE 保真。
3. P0 验证通过后再进入 P1（注册表持久化 + 控制台 + `runtime-server` 接入）。
4. 每阶段结束更新本文档的验收勾选，并补 `docs/gateway/` 下的实现记录（沿用仓库既有 plan/analysis 习惯）。
