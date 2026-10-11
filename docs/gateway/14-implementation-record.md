# 14 · 实现记录（aicli-gateway MVP）

> 上游：[06-implementation-plan.md](./06-implementation-plan.md)、[13-connection-control-tasks.md](./13-connection-control-tasks.md)
> 本文记录设计方案（01–13）的**落地状态**：已实现的组件、如何运行、命令/API 参考、测试方式与已知缺口。

---

## 1. 已实现总览

| 层 | 组件 | 位置 |
|----|------|------|
| 协议 | 帧编解码 + 控制消息 | `backend/internal/gateway/wire` |
| 客户端 | 连接状态机 + 控制面 + WS 拨号 + 反向转发 + 流控 | `backend/internal/gateway/agent` |
| 服务端 | 隧道（握手/注册/心跳/注销）+ 注册表 + 反向代理 + 流控 | `backend/internal/gateway/tunnel` |
| 服务端 | owner 认证 | `backend/internal/gateway/auth` |
| 服务端 | 设备令牌（含持久化） | `backend/internal/gateway/device` |
| 服务端 | 限流 + 可信代理 + 惩罚封禁 | `backend/internal/gateway/ratelimit` |
| 服务端 | 审计事件 | `backend/internal/gateway/audit` |
| 服务端 | Prometheus `/metrics` + 实时 `/api/events` SSE | `backend/cmd/gateway` |
| 服务端 | Web Push（VAPID + RFC 8291 + 订阅 + 触发） | `backend/internal/gateway/push`、`backend/cmd/gateway/push.go` |
| 服务端 | 二进制 + 轻量控制台（PWA） | `backend/cmd/gateway`（`console/`） |
| 宿主 | aicli `/gateway` 命令 + 自动连接 + 转发目标 + 会话上报 | `backend/cmd/aicli/commands/chat_gateway_*.go`、`chat_mesh.go`、`cmd/aicli/main.go` |
| 宿主 | runtime-server 控制 API + 启动装配 + 会话上报 | `backend/internal/api/runtimeapi/gateway_handlers.go`、`backend/cmd/runtime-server/gateway_bootstrap.go` |
| 前端 | runtime-server "Gateway" 设置页 | `frontend/src/components/workspace/settings/gateway-settings-page.tsx`、`api/runtime/gateway.ts` |
| 配置 | 分层 `gateway.*` + env 覆盖 | `backend/internal/agentconfig/config.go` |
| base-path | aicli 微前端 + runtime-server SPA | `web_page.go`、`internal/webui/handler.go`、`frontend/vite.config.ts` |
| 移动端 | 低带宽模式（微前端）+ 控制台 PWA | `web/js/ui.js`、`cmd/gateway/console/` |
| 移动端 | 控制台 Web Push 订阅 + Service Worker 通知 | `cmd/gateway/console/{app.js,sw.js}` |
| 控制台 | 会话 `attention` 徽标 + 新增注意力 toast | `cmd/gateway/console/{app.js,style.css}` |
| 宿主 | 会话注意力上报（turn 边界 + 事件总线） | `cmd/aicli/commands/chat_gateway_attention.go`、`internal/api/runtimeapi/gateway_attention.go` |

---

## 2. 如何运行

### 2.1 启动网关

```powershell
# 构建
go build -o bin/aicli-gateway ./cmd/gateway   # 在 backend/ 下

# 运行（示例：直接 TLS 或前置反代）
./bin/aicli-gateway serve `
  --listen :8443 `
  --public-base-url https://gw.example.com `
  --owner-password "choose-a-strong-password" `
  --data-dir ./data `
  --tls-cert cert.pem --tls-key key.pem `
  --trusted-proxies "127.0.0.1/32" `
  --rate-limit 300 `
  --ban-threshold 20 --ban-window 1m --ban-ttl 10m
```

- 打开 `https://gw.example.com/` → 控制台（owner 登录）。
- 在控制台签发**设备令牌**（`POST /api/devices`，仅显示一次）。

### 2.2 本地进程接入

`aicli` / `runtime-server` 通过分层配置 `gateway.*` 或环境变量接入（env 覆盖配置）：

```yaml
# config.yaml
gateway:
  enabled: true
  url: wss://gw.example.com
  token: agw_...
  name: vince-laptop
  auto_connect: true
  routes: ["/web", "/web/api", "/debug/endpoints"]
```

```powershell
# 或环境变量
$env:AICLI_GATEWAY_URL = "wss://gw.example.com"
$env:AICLI_GATEWAY_TOKEN = "agw_..."
aicli chat --pprof            # 自动注册；TUI 内 /gateway 可手动连/断
runtime-server serve          # 同样自动注册
```

### 2.3 访问会话

- 控制台 → 设备/会话列表 → 打开（aicli：`/n/{node}/web/?session=<id>`；runtime-server：`/n/{node}/`）。
- 或直接访问 `https://gw.example.com/n/<node_id>/web/`。

---

## 3. 参考

### 3.1 `cmd/gateway` 参数

| 参数 | 默认 | 说明 |
|------|------|------|
| `--listen` | `127.0.0.1:8090` | 监听地址 |
| `--public-base-url` | 空 | 生成 console URL |
| `--owner-password` | 空（dev，警告） | owner 登录密码（Argon2id） |
| `--session-ttl` | `12h` | owner 会话时长 |
| `--device-token` | 空 | 静态设备令牌回退（可选） |
| `--data-dir` | `./data` | 持久化目录（`devices.json`、`audit.log`） |
| `--trusted-proxies` | `127.0.0.1/32,::1/128` | 可信代理（决定是否信任 XFF） |
| `--rate-limit` | `0` | 每 IP 每分钟请求上限（0=关） |
| `--ban-threshold` / `--ban-window` / `--ban-ttl` | `0` / `1m` / `10m` | 惩罚封禁 |
| `--metrics-token` | 空 | `/metrics` 的 bearer；空则仅回环 |
| `--tls-cert` / `--tls-key` | 空 | 直接 TLS |
| `--server-version` / `--heartbeat-interval` | `0.1.0` / `0` | WELCOME 内容 |

### 3.2 网关 HTTP 端点

| 方法 | 路径 | 认证 | 说明 |
|------|------|------|------|
| GET | `/` | 无 | 控制台（PWA） |
| GET | `/gw/agent` | 设备令牌 | 隧道升级 |
| GET | `/api/health` | 无 | 存活探针 |
| POST | `/api/auth/login` | 无 | owner 登录（Cookie） |
| POST | `/api/auth/logout` | Cookie | 退出 |
| GET | `/api/nodes` | Cookie | 节点/会话列表 |
| GET | `/api/devices` / POST / DELETE | Cookie | 设备令牌管理 |
| GET | `/api/metrics` | Cookie | 计数（JSON） |
| GET | `/api/audit` | Cookie | 审计事件 |
| GET | `/api/events` | Cookie | 注册表实时 SSE |
| GET/POST/DELETE | `/api/bans[...]` | Cookie | 封禁管理 |
| GET | `/metrics` | bearer 或回环 | Prometheus 文本 |
| `/n/{node_id}/...` | Cookie | 反向代理到节点本机服务 |

### 3.3 aicli `/gateway` 命令

```
/gateway [status|connect|disconnect|reconnect|url|help]
```

`disconnect` 为运行态（抑制自动重连、不改配置）。

---

## 4. 测试

```powershell
# 网关各包（含端到端反向代理 + 会话增量）
go test ./internal/gateway/... -race

# runtime-server 控制面
go test ./internal/api/runtimeapi/ -run TestGateway

# SPA 注入
go test ./internal/webui/

# 控制台静态资源
go test ./cmd/gateway/

# 前端
cd frontend && npx tsc -b && npm run lint:i18n
```

关键集成测试：
- `tunnel`：真实 `agent.WSConnector` 对接服务端（握手/心跳/注销/反向代理/路由白名单/会话增量/前缀透传）。
- `cmd/gateway`：控制台资源伺服 + SPA fallback + 保留前缀不遮蔽。

---

## 5. 已知缺口（未实现）

| 项 | 说明 |
|----|------|
| Web Push 的 presence 抑制/合并/静音 | 控制台未上报"正在查看"，无时间窗合并与免打扰 |
| 注册表持久化 | 节点表为内存；重连即恢复（影响小） |
| 多实例 HA | 隧道态单实例；注册表未外置 |
| `runtime-server` 增量心跳 | 会话增删已推送；busy/attention 变化未推送 |
| 低带宽模式深化 | 已隐藏图片并提供模式钩子；差分/节流/按需历史未接线 |
| 微前端 base-path 的 query 透传 | `?session=` 深链已支持；更复杂的路由前缀透传未覆盖 |

---

## 6. 与设计文档的偏差

- 设备令牌持久化用 **JSON 文件**（`devices.json`）而非 SQLite（`gateway-registry.db`）——语义等价、依赖更少；SQLite 可作为后续替换。
- 审计用 **JSONL 追加** 而非 SQLite 表。
- 网关控制台为**轻量独立控制台**（`go:embed`，零构建），与 [07](./07-console.md) 一致。

> 2026-10-11 补记：流控（WINDOW_UPDATE，1 MiB/流）、Prometheus `/metrics`、安全审计事件 + `/api/audit`、
> 控制台实时 SSE（`/api/events`）、封禁管理 API、分层 `gateway.*` 配置、设备令牌持久化、
> 会话增删增量推送、微前端低带宽模式均已实现并测试通过。
>
> 2026-10-11 再补记：**Web Push**（`internal/gateway/push`：VAPID/ES256 + RFC 8291 aes128gcm + 订阅存储 +
> 投递 + `ErrGone` 自动清理）、网关侧注意力侦测与推送触发（`cmd/gateway/push.go`）、控制台推送 API
> 与 Service Worker 通知/点击跳转、`--push-enabled` / `--vapid-subject` 配置均已实现并有单测。
>
> 2026-10-11 三补记：**agent 侧注意力上报**已落地——aicli 在 turn 边界（`beginChatMeshTurn`）上报
> `completed`/`error`/`busy`；runtime-server 订阅运行时事件总线（`agent.turn.*`、`approval_*`、
> `question_*`、`session_end/interrupted`）映射为 `attention`。**推送链路已端到端打通**：
> agent 上报 → 网关注册表 → `pushNotifier` 去重触发 → VAPID 加密投递 → Service Worker 展示。
> 剩余待办集中在控制台展示与 presence 抑制，见 [08-notifications.md](./08-notifications.md) §10.2。
