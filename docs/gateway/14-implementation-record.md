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
| 控制台 | presence 抑制（可见时续期，网关据此免打扰） | `cmd/gateway/presence.go`、`cmd/gateway/console/app.js` |
| 宿主 | 会话注意力上报（turn 边界 + 事件总线） | `cmd/aicli/commands/chat_gateway_attention.go`、`internal/api/runtimeapi/gateway_attention.go` |
| 服务端 | 通知策略（时间窗合并 + 免打扰/静音） | `cmd/gateway/push.go`、`cmd/gateway/notify_policy.go` |
| 协议/宿主 | `NOTIFY` 帧（事件元数据 + `dedup_key`） | `internal/gateway/wire/messages.go`、`internal/gateway/tunnel/notify.go`、`internal/gateway/agent/controller.go` |
| 控制台 | 聚合收件箱（按优先级 + 已读联动） | `cmd/gateway/console/{index.html,app.js,style.css}` |
| 服务端 | 注册表持久化（防抖快照 + 重启恢复 stale） | `internal/gateway/tunnel/{registry,persist}.go`、`cmd/gateway/main.go` |
| 控制台 | 一键清除离线快照节点（批量 stale 清理 + 单节点移除） | `cmd/gateway/nodes_api.go`、`internal/gateway/tunnel/registry.go`、`cmd/gateway/console/{index.html,app.js}` |
| 服务端 | 主备切换原语（GOAWAY 下线广播 + 优雅退出 + live/stable 计数） | `internal/gateway/tunnel/server.go`（`CloseAll`）、`cmd/gateway/main.go`（SIGINT/SIGTERM） |
| agent | 断线自动重连（GOAWAY 触发，2/5/10/30s 退避） | `internal/gateway/agent/{control,wsconn}.go` |
| 宿主 | 机器稳定 node id（failover 后同 ID 替换 stale 条目） | `internal/gateway/agent/nodeid.go`、`cmd/aicli/commands/chat_gateway_host.go`、`cmd/runtime-server/gateway_bootstrap.go` |
| 运维 | 主备切换 runbook（场景 F） | `docs/gateway/10-runbook.md` §13 |


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

> 参数说明：网关无子命令；文档中的 `serve` 写法是**可选的位置参数**（会被忽略并继续解析后续 `--flags`）。
> 其它未知位置参数会直接报错退出（exit 2），不会静默使用默认值启动。

完整参数参考始终以 `aicli-gateway --help` 为准（含数据文件清单与退出码说明）。逐参数补充语义：

| 参数 | 默认 | 语义要点 |
|------|------|----------|
| `--listen` | `127.0.0.1:8090` | 同时承载 `/gw/agent` 隧道、控制台与反代；公网部署必须配 TLS 或前置反代 |
| `--config` | 空 | **可选 YAML flagfile**（键=flag 名，值按 flag 类型解析）；优先级 **命令行 > 配置文件 > 默认值**；未知键/meta 键/无法解析的值一律拒绝启动；裸数字不作时长单位猜测（`presence-ttl: 90` 被拒，须写 `90s`） |
| `--public-base-url` | 空 | 拼接 console URL / Web Push 点击跳转；不配则通知无跳转链接 |
| `--owner-password` | 空 | **空=控制台/owner API 完全无认证（dev only）**；配置后存 PBKDF 哈希，不落盘明文 |
| `--session-ttl` | `12h` | owner 登录会话时长 |
| `--device-token` | 空 | 静态共享令牌；**与设备令牌并存**（store 命中或静态匹配任一通过）；两者皆空且 store 为空=隧道放行（dev only） |
| `--data-dir` | `./data` | 状态根目录，见 §2.1.1；主备部署须共享其内容，**VAPID 密钥必须一致** |
| `--tls-cert/--tls-key` | 空 | 成对出现才启用 https；只配其一直接拒绝启动 |
| `--trusted-proxies` | `127.0.0.1/32,::1/128` | X-Forwarded-For 仅信这些来源；前置反代必须把反代网段加进来，否则限流/封禁按反代 IP 计 |
| `--rate-limit` | `0`（关） | 每 IP 每分钟对 proxy + 隧道升级限流；超限计惩罚并返回 429 |
| `--ban-threshold/--ban-window/--ban-ttl` | `0`（关） | 窗口内惩罚达阈值自动封禁；解除走控制台误封恢复（runbook §9） |
| `--metrics-token` | 空 | `/metrics` bearer；空=仅回环可抓取 |
| `--heartbeat-interval` | `0` | 写入 WELCOME 建议值；0 用 agent 默认（30s） |
| `--push-enabled/--vapid-subject` | `true` / `mailto:…` | 关闭后控制台隐藏订阅入口；subject 需 mailto: 或 https: |
| `--presence-ttl` | `90s` | 控制台"正在观看"抑制时长；期间该会话不推送且不记入去重快照 |
| `--notify-coalesce-window` | `30s` | 低优先（completed/waiting）合并窗；0=逐条即发；高优先（approval/question/error）不受影响 |
| `--notify-quiet-hours` | 空 | `HH:MM-HH:MM` 逗号分隔，本地时间，支持跨夜；仅静音低优先 |

退出码：`0` 正常运行结束或优雅下线；`1` 运行失败（端口占用、数据文件损坏等）；`2` 用法错误（未知位置参数、flag 解析失败）。

### 2.1.1 数据文件与重载语义

`--data-dir`（默认 `./data`）下的状态文件均在**启动时一次性加载**，运行期间手工修改**不会**被重新读取（排障时需先停网关再改文件）：

| 文件 | 内容 | 加载时机 |
|------|------|----------|
| `devices.json` | 设备令牌 | 启动（写入路径实时落盘） |
| `vapid.key` | Web Push VAPID P-256 私钥（0600，缺失自动生成） | 启动 |
| `push.json` | Web Push 订阅（0600） | 启动（写入路径实时落盘） |
| `registry.json` | 节点注册表快照（0600，防抖 ~2s + 退出强制落盘） | 启动（恢复为 stale 条目，见 [08](./08-notifications.md) §10.1） |
| `audit.log` | 审计事件（追加写） | 启动 |

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
| GET / DELETE | `/api/nodes` | Cookie | 节点/会话列表；`DELETE ?stale=1` 一键清除全部离线快照节点（仅 stale，逐个记审计） |
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
| Web Push 的时间窗合并/静音 | 连续 `completed` 未合并为"N 个会话已完成"；无免打扰 |
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
>
> 2026-10-12 补记：控制台新增**一键清除离线快照节点**——`DELETE /api/nodes?stale=1` 单次批量删除所有
> stale（重启恢复/下线遗留）条目（`Registry.RemoveStale`，逐节点记 `node_removed` 审计并广播 SSE），
> 控制台「节点 → 离线快照」面板一键触发（单节点「移除」保留）；仅清理 stale，live 节点不受影响。
