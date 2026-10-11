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

> 说明：命令/端点名称以 [02-architecture.md](./02-architecture.md) §4.1 与实现为准；
> 本文给出的是设计约定的目标形态，落地时若命名调整需同步更新。
