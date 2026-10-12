# 10 · 安全事件处置 Runbook

> 上游：[09-edge-hardening.md](./09-edge-hardening.md)、[04-security.md](./04-security.md)
> 受众：网关 owner / 值班运维。目标：公网入口出现扫描、爆破、异常高频、疑似 DDoS/CC 或误封时，
> **在不中断正常请求的前提下**完成确认 → 缓解 → 观测 → 回滚 → 复盘。
> 命令示例统一使用 **`aicli-gateway`（外网会话网关）**；管理 API 走 owner Cookie（浏览器/CLI）。

---

## 1. 处置原则

1. **先观测/模拟，再逐步扩大阻断**：`monitor` → `simulate` → 小范围 `enforce` → 全量。
2. **最外层廉价拒绝**：能在 L0 Edge 挡住的，绝不放进去消耗隧道/注册表/DB/日志。
3. **写操作必须带 `reason` 且走 API/CLI**：不手工修改运行态，保证可审计、可回滚。
4. **误封恢复与封禁同等优先级**：封禁前先确认 allowlist、trusted proxy 与正常入口。
5. **正常请求是硬门禁**：`/api/health`、控制台、已认证会话在阈值内必须不受影响。

---

## 2. 快速分级

| 等级 | 现象 | 首要动作 |
|------|------|----------|
| **P1** | `/api/health` 失败；网关 CPU/连接数/内存耗尽；正常会话不可用 | 收紧过载阈值；对明确恶意路径/IP/CIDR 临时封禁；必要时回滚最近安全策略 |
| **P2** | 大量扫描路径（`/.env`、`/.git/*`、`/wp-login`）但正常可用 | 确认/补充静态 deny，开启 penalty 自动封禁，观察事件与指标 |
| **P3** | 某国家/地区/ASN 异常访问（仅启用 Geo 时） | 先 `monitor`，验证命中后对管理端路径切 `block` |
| **P4** | 单 IP / NAT 出口 / 某设备令牌高频超限 | 调 IP 限流或设备令牌配额；**优先避免大范围 CIDR 封禁** |

---

## 3. 处置前检查

### 3.1 安全状态

```powershell
# 概览：enabled / mode / 限流 / 惩罚 / 过载 / 最近错误 / risks
aicli-gateway security status --json
```

重点字段：

| 字段 | 判断 |
|------|------|
| `enabled` | `false` → 安全预检未生效（应立即排查） |
| `mode` | `monitor` 表示只计数不拦；确认是否有意为之 |
| `ip_rate_limit.enabled` | `false` → 入口 IP 限流不挡流量 |
| `penalties.enabled` | `false` → 危险路径不触发自动临时封禁 |
| `trusted_proxies` | 与真实反代/LB 地址是否一致（不一致会导致 IP 解析错误） |
| `overload` 阈值 | 是否为非零生产基线 |
| `risks[]` | `security_disabled` / `ip_rate_limit_disabled` / `penalties_disabled` / `trusted_proxies_mismatch` / `overload_unset` |

### 3.2 最近安全事件

```powershell
# 最近 100 条安全事件（block / ban / unban / rate_limited …）
curl.exe -s -b cookies.txt "https://gw.example.com/api/security/events?limit=100" | ConvertFrom-Json
```

重点看：

- `action=block` 的来源 IP、`path_group` 与 `rule`（`deny_path` / `deny_path_prefix` / `deny_method` / `penalty_ban` / `ip_rate_limit` / `geo:*`）。
- 是否出现**正常业务路径**被阻断（误封信号）。
- 是否集中在**单个 IP / 单个设备令牌**，避免误判为全局攻击。
- `auth` 类事件中 `login_failed` / `login_rate_limited` 是否突增。

### 3.3 指标与告警

```text
gateway_security_decisions_total{method,path_group,rule,action}
gateway_unknown_route_total{method,path_group}
gateway_inflight / gateway_pending
gateway_path_inflight{route} / gateway_path_pending{route}
gateway_agent_connections / gateway_tunnel_streams
gateway_auth_events_total{event,result}
```

| 指标 | 条件 | 说明 |
|------|------|------|
| `increase(gateway_security_decisions_total{action="block"}[5m])` | > 平时基线 3× | 阻断量异常增长 |
| `increase(gateway_unknown_route_total[5m])` | > 基线 3× | 未知路径扫描增长 |
| `gateway_pending` | 持续接近上限 | 排队压力过高 |
| `gateway_auth_events_total{event="login_failed"}` | 突增 | 爆破尝试 |
| `gateway_inflight` | 持续接近上限 | 过载风险 |

---

## 4. 场景 A：扫描 / 路径探测

典型路径：`/.env`、`/.git/config`、`/wp-login.php`、`/wp-admin/`、`/phpinfo.php`、`/vendor/`。

```powershell
# 1) 先模拟：确认规则命中与状态码（不改运行态）
aicli-gateway security simulate --method GET --path "/.git/config" --remote-addr 203.0.113.7 --json

# 2) 若 deny 未覆盖，补充静态规则（需 reason，落审计）
aicli-gateway security policy set --deny-path-prefix "/.git/" --deny-path "/.env" `
  --reason "P2 扫描路径治理" --json

# 3) 开启/确认 penalty 自动封禁（低误报信号）
aicli-gateway security policy set --penalties-enabled true --penalty-threshold 20 `
  --penalty-window 1m --penalty-ban-ttl 10m --reason "P2 扫描自动封禁" --json

# 4) 观察事件与指标，确认正常请求未受影响
curl.exe -s -b cookies.txt "https://gw.example.com/api/health"
```

**要点**：扫描路径应返回 `403` 且**不占 in-flight**；先 `mode=monitor` 验证命中分布，再 `enforce`。

---

## 5. 场景 B：高频 IP / 登录爆破

```powershell
# 1) 确认限流与阈值
aicli-gateway security status --json    # 看 ip_rate_limit / auth 限流

# 2) 模拟某 IP 是否会被限流
aicli-gateway security simulate --remote-addr 203.0.113.7 --json

# 3) 对明确恶意 IP 临时封禁（优先单 IP，避免大段 CIDR）
aicli-gateway security bans add --ip 203.0.113.7 --ttl 30m --reason "P4 爆破源" --json

# 4) 若是共享出口（公司/校园/NAT）：不要整段封禁
#    改为收紧登录限流 + 触发人机识别，叠加 UA 摘要与失败率
aicli-gateway security policy set --auth-login-max-per-ip 10 --auth-login-window 5m `
  --reason "共享出口防爆破，避免误封" --json
```

**要点**：`429` 必须带 `Retry-After`；登录失败计数 **fail-closed**；错误提示模糊，不泄露阈值。

---

## 6. 场景 C：过载 / 资源耗尽（DDoS/CC）

```powershell
# 1) 看 in-flight / pending 与路由分布
#    指标：gateway_inflight, gateway_pending, gateway_path_inflight{route}

# 2) 收紧过载阈值（保命优先）
aicli-gateway security policy set `
  --overload-max-global-inflight 500 `
  --overload-max-pending 100 `
  --overload-queue-timeout 500ms `
  --reason "P1 过载保护收紧" --json

# 3) 控制面写请求不排队（快速 429），避免堆积
#    在 per_route 中把 /api 的 max_pending 设为 0
aicli-gateway security policy set --overload-route "/api" --max-inflight 50 --max-pending 0 `
  --reason "P1 控制面保护" --json

# 4) 确认 /api/health 仍可用、正常低速请求仍 2xx
curl.exe -s -o NUL -w "%{http_code}`n" "https://gw.example.com/api/health"
```

**要点**：长请求（SSE/隧道）允许短队列；控制面写请求不排队；超限返回配置的 `503`/`429`。

---

## 7. 场景 D：设备令牌异常

```powershell
# 1) 列出设备令牌与最近使用
aicli-gateway device list --json

# 2) 撤销可疑令牌（立即断开其全部隧道，agent 不重试）
aicli-gateway device revoke --id <device-id> --reason "P4 令牌疑似泄露" --json

# 3) 重新签发并设置旧令牌宽限期（正常轮换）
aicli-gateway device create --name "vince-laptop" --json
```

**要点**：令牌只存哈希、仅显示一次；可绑定 `node_id`/`kind`；`auth_invalid` 时 agent **不重试**（避免风暴）。

---

## 8. 场景 E：区域 / 国家屏蔽（可选 Geo）

```powershell
# 1) 先 monitor：只记录命中，不拦截
aicli-gateway security policy set --geo-enabled true --geo-mode monitor `
  --geo-block-country CN --geo-fail-mode allow --reason "P3 区域观察" --json

# 2) 验证命中分布后，仅对管理端路径切 block
aicli-gateway security policy set --geo-mode enforce --geo-rule "/api/*:block" `
  --reason "P3 管理端区域限制" --json
```

**要点**：GeoIP 数据不可用默认 `fail_mode=allow`（不得全站失败）；私网/回环/unspecified 跳过 Geo；公开 API 谨慎启用。

---

## 9. 误封恢复

```powershell
# 查看当前封禁（手动 + penalty）
aicli-gateway security bans list --json

# 解除单个（可同时清手动与 penalty 封禁）
aicli-gateway security bans remove --ip 203.0.113.7 --reason "P4 误封恢复" --json

# 批量解除
aicli-gateway security bans remove --cidr 203.0.113.0/24 --reason "P4 网段误封恢复" --json
```

**要点**：解除与封禁同等优先；全部动作写审计（`manual_unban` / `penalty_unban`）；恢复后回归 §3.3 指标。

---

## 10. 策略发布与回滚

```powershell
# 当前生效策略
aicli-gateway security policy get --json

# 应用（先 monitor，再 enforce；带 reason）
aicli-gateway security policy set --mode monitor --reason "观察窗口" --json

# 回滚到上一版本
aicli-gateway security policy rollback --reason "P1 回滚误伤策略" --json
```

**要点**：`monitor → enforce` 前建议采集 7–14 天命中率；变更有 `reason`、有审计、可回滚。

---

## 11. 复盘清单

- [ ] 事件时间线（首次命中 → 处置 → 恢复）与影响范围。
- [ ] 命中规则分布（Top IP / Top path_group / 动作）是否与判断一致。
- [ ] 是否有正常请求被误伤；误封解除耗时。
- [ ] 阈值是否需要按容量重新压测调整。
- [ ] 是否需要新增静态 deny / allowlist / trusted proxy 修正。
- [ ] 告警是否及时触发；是否需要新增/调整告警条件。
- [ ] 处置命令是否都带 `reason` 且可从审计复原。

---

## 12. 常用命令速查

| 目的 | 命令 |
|------|------|
| 安全概览 | `aicli-gateway security status --json` |
| 模拟判定 | `aicli-gateway security simulate --method M --path P --remote-addr IP --json` |
| 最近事件 | `GET /api/security/events?limit=100` |
| 封禁列表 | `aicli-gateway security bans list --json` |
| 封禁 / 解除 | `aicli-gateway security bans add|remove ... --reason R --json` |
| 策略查看 / 设置 / 回滚 | `aicli-gateway security policy get|set|rollback ... --json` |
| 设备令牌 | `aicli-gateway device list|create|revoke ... --json` |
| 健康 | `GET /api/health` |
| 审计 | `GET /api/audit?limit=100` |
| 清除离线快照 | `DELETE /api/nodes?stale=1`（或控制台「节点 → 离线快照 → 清除全部」） |

> 说明：命令/端点名称以 [02-architecture.md](./02-architecture.md) §4.1 与实现为准；
> 本文给出的是设计约定的目标形态，落地时若命名调整需同步更新。

---

## 13. 场景 F：主备切换（active/passive failover）

当前实现（`b484018a` + 稳定 node id）已支持**主备热切换**：agent 侧具备 GOAWAY 触发的自动重连
（2/5/10/30s 封顶退避），网关具备优雅下线广播（`CloseAll` → GOAWAY + 注册表条目转 stale）与
注册表快照恢复（standby 启动时还原为 stale 条目）。

### 13.1 部署形态

```text
            ┌──── LB / DNS（健康检查：GET /api/health）────┐
console ──▶ │  active:  gateway-1 (:8443, data/ 共享或同步) │
            │  standby: gateway-2（冷备或热备）             │
            └──────────────────────────────────────────────┘
                  ▲ agents 断线后按 backoff 重连当前 active
```

- **数据目录**：主备共享同一 `--data-dir` 内容（共享盘 / 定时同步 / 对象存储），
  包含 `registry.json`（快照）、`devices.json`（令牌）、`vapid.key`（Web Push 身份）、`push.json`；
- **VAPID 密钥必须主备一致**（否则 Web Push 订阅在切换后失效）；
- 健康检查判定"可服务"以 `/gw/agent` 可升级为准，`nodes=0` 是刚重启实例的合法状态
  （`live_nodes` 字段仅用于观测，不作为摘除依据）。

### 13.2 计划内切换（升级/维护 active）

1. standby 就绪（同版本 + 同步的数据目录）；
2. 向 active 发 `SIGINT`/`SIGTERM`：网关对全部 live agent 广播 `GOAWAY`，
   注册表条目转 stale 并落盘快照，退出码 0；
3. LB 把流量切到 standby；standby 读快照 → stale 条目原样可见；
4. agents 按 backoff 重连（同 NodeID 替换 stale 条目 → live）；
5. 验收：`GET /api/health` → `live_nodes` 回升到切换前数量；
   控制台会话/attention 视图与切换前一致。

### 13.3 故障切换（active 崩溃）

- LB 健康检查失败自动摘除 → standby 接管；agents 因连接断开走同一条自动重连路径；
- active 若来不及写快照，standby 用最后一次防抖快照（最多丢 ~2s 的注册表变更，
  agent 重连即重建，无实质影响）。

### 13.4 验证

`backend/.tmp/ha-e2e/failover_e2e.ps1` 是一次完整的真实进程演练（起 A → 注册 →
CTRL_BREAK 优雅停止 → 起 B（同数据目录）→ 断言同一 NodeID 以 live 状态出现在 B，
即 stale 条目被替换）。核心断言：

```text
A registry: node=node-<id> LIVE
（drain A, start B on the same data dir）
B registry: node=node-<id> LIVE   # 同 ID 替换 stale，而非新增第二个节点
```

**身份前提**：aicli / runtime-server 的 REGISTER 携带机器稳定 node id
（`~/.aicli/gateway-node-id`，`gatewayagent.LoadOrCreateNodeID`）。若该文件缺失
（极端环境无法写 home），agent 退化为每次进程随机 id——failover 仍工作，
但 standby 上会短暂并存一条 stale 条目直到 TTL/人工清理（控制台「节点 → 离线快照 → 清除全部」一键批量
清除，等价 `DELETE /api/nodes?stale=1`；仅删 stale，不影响 live 节点），不影响可用性。

---

## 14. 线上入口：gateway-edge 多域名反代

线上 80/443 由 **ai-gateway-edge**（另一仓库 `ai-gateway` 的 `cmd/gateway-edge`）持有，
ACME（Let's Encrypt，http-01）与按 Host 路由都在 Edge 侧完成；控制台不要另装 Caddy（会端口冲突）。
路由规则支持 `match.hosts`（**精确匹配**，不支持通配符）到不同 backend group。

| 域名 | 后端 | 说明 |
|------|------|------|
| `ttai.online` / `api.ttai.online` | `127.0.0.1:18443`（ai-gateway，默认组） | 经 Cloudflare 代理 |
| `aicli.ttai.online` | `127.0.0.1:8080`（aicli-gateway 控制台） | 经 Cloudflare 代理（源站证书 LE，边缘为 CF 通用证书）；独立域名满足 Service Worker / Web Push 的 scope 与同源要求 |
| agent 隧道 | `wss://aicli.ttai.online/gw/agent`（443） | CF → edge → `127.0.0.1:8080/gw/agent`；网关只监听回环，公网 8080 已关闭 |

> 2026-10-12 14:48 UTC 完成端口收敛：`--listen 0.0.0.0:8080` → `--listen 127.0.0.1:8080`，
> 公网 `ttai.online:8080` 不再可达。各 agent 需把 gateway URL 改为 `wss://aicli.ttai.online/gw/agent`
> （旧 `wss://ttai.online:8080/gw/agent` 已失效，agent 会持续重试直到更新配置）。
> 回滚：还原 `/etc/systemd/system/aicli-gateway.service.bak-*` → `systemctl daemon-reload && systemctl restart aicli-gateway`。

配置与生效（`/opt/ai-gateway-edge/configs/config.yaml`）：

- `runtime.edge.tls.acme.domains`：新增域名必须在此登记，且**需要 `systemctl restart ai-gateway-edge`**
  才会签发（热加载会置 `tls_restart_required=true` 并提示 controlled restart）；
- `runtime.edge.edge_backend_groups` + `edge_route_rules`（`match.hosts` + `path_prefixes: ["/"]`）：支持热加载；
- 校验：`cd /opt/ai-gateway-edge && ./bin/gateway-edge configtest -c configs/config.yaml`
  （需在服务目录下运行，token 由 `configs/.env` 自动加载）；
- 验证：`curl --resolve aicli.ttai.online:443:127.0.0.1 https://aicli.ttai.online/`。

网关侧：`aicli-gateway.service` 需带 `--public-base-url https://aicli.ttai.online`
（Web Push 点击深链由此生成），改后 `systemctl daemon-reload && systemctl restart aicli-gateway`。

**通知通道（Web Push + ntfy）**：Web Push 之外的第二通道 ntfy 已自建在网关服务器上：

| 项 | 值 |
|----|----|
| 服务 | `ntfy`（deb 安装，systemd），配置 `/etc/ntfy/server.yml`：`listen-http: 127.0.0.1:2586`、`base-url: https://ntfy.ttai.online`、`cache-file: /var/lib/ntfy/cache.db`、`behind-proxy: true` |
| 入口 | `ntfy.ttai.online`（CF 代理）→ edge 路由组 `ntfy` → `127.0.0.1:2586`；组内 `health.ready_path: "/v1/health"`（默认 `/health/ready` 会 404 导致组不 ready） |
| 网关发布 | `--ntfy-url http://127.0.0.1:2586 --ntfy-topic aicli-gateway-0bf5wo3tkd`（走本机回环，不依赖 CF） |
| 客户端订阅 | 服务器 `https://ntfy.ttai.online` + topic `aicli-gateway-0bf5wo3tkd`（Android 需开 Instant delivery） |

高优先（approval/question/error）→ `priority=5` + `warning` 标签，其余 `priority=3`；点击打开对应会话。
`POST /api/push/test` 返回 `channels.webpush / channels.ntfy` 明细；控制台"发送测试通知"会展示各通道结果。
当前 ntfy 未启用鉴权（topic 名即口令）；如需访问控制再启用 `auth-*` 并用 `--ntfy-token`。

**edge 配置变更纪律**（2026-10-12 事故教训）：
1. 改完先 `cd /opt/ai-gateway-edge && ./bin/gateway-edge configtest -c configs/config.yaml` 再重启；
   YAML 缩进错误（如 `acme:` 子键被拉到 `tls:` 同级）会导致重复键 → 崩溃循环、全站 521；
2. `configs/.env` 必须保持 `root:ai-gateway-edge` + `0640`：
   改成 `0600` 后服务用户读不到 `GATEWAY_EDGE_UPLOAD_TOKEN`，启动即 "resumable_upload.token is required"；
3. ACME 生产域名不要切到 staging CA / `acme-wildcard-staging` 存储，除非确认 CF 回源为 Full（非 strict）。

**发布（含缓存）**：Cloudflare 会按扩展名缓存 `*.js`/`*.css` 4 小时（HTML/manifest 默认不缓存）。
因此每次发布必须同步 bump 控制台资源版本（当前 `v=34`）：
`console/index.html` 的 `app.js?v=`/`style.css?v=`、`console/sw.js` 的 `CACHE` 与 `SHELL`、
`console/app.js` 里 `serviceWorker.register("./sw.js?v=")`；否则手机端会继续拿到旧 JS，
直到 CF TTL（4h）过期或手动 purge。发布步骤：`GOOS=linux GOARCH=amd64 go build -trimpath -o gateway ./cmd/gateway`
→ 上传替换 `/opt/aicli-gateway/gateway`（保留 `gateway.bak-*`）→ `systemctl restart aicli-gateway`
→ 校验 `curl -s 'https://aicli.ttai.online/app.js?v=<新版本>' | grep -c 'push-test'`。

回滚点：`/opt/ai-gateway-edge/configs/config.yaml.pre-aicli-online-*`、
`/etc/systemd/system/aicli-gateway.service.bak-*`。
