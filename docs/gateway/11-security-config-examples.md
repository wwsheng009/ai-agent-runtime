# 11 · 安全配置示例

> 上游：[04-security.md](./04-security.md)、[09-edge-hardening.md](./09-edge-hardening.md)、[10-runbook.md](./10-runbook.md)
> 本文给出可直接复制调整的配置示例（目标形态）。键名与 [02](./02-architecture.md) §8.1 一致；
> 落地时若命名调整需同步更新本文与 09/10。

---

## 1. 最小生产基线（先跑起来）

```yaml
# aicli-gateway 生产最小安全基线
security:
  enabled: true
  mode: monitor            # 先观察，验证命中率后再切 enforce
  unknown_route: count     # 先计数；确认合法路由已登记后改 reject
  trusted_proxies: ["127.0.0.1/32", "::1/128"]   # 按真实反代/LB 地址调整

auth:
  session:
    access_ttl: 12h
  login_rate_limit:
    enabled: true
    fail_mode: closed

gateway:
  tls_required: true       # 未配置证书且无可信反代时拒绝明文对外
```

> 上线前必须：① 配置 TLS；② 校准 `trusted_proxies`；③ 确认 `/api/health`、控制台、已认证会话可用。

---

## 2. 边缘防护（`security.*`）

### 2.1 静态 deny 与未知路径

```yaml
security:
  enabled: true
  mode: enforce
  unknown_route: reject
  deny_paths:
    - "/.env"
    - "/.git/config"
    - "/phpinfo.php"
  deny_path_prefixes:
    - "/.git/"
    - "/wp-admin/"
    - "/wp-login"
    - "/vendor/"
  deny_methods: ["TRACE", "CONNECT"]
  deny_cidrs: []            # 明确要拒绝的来源网段（谨慎）
```

### 2.2 可信代理（客户端 IP）

```yaml
security:
  # 只有来自这些地址的 X-Forwarded-For / X-Real-IP 才可信；
  # 不匹配则使用 socket 对端地址（防伪造 IP 绕过限流/封禁）。
  trusted_proxies:
    - "127.0.0.1/32"
    - "::1/128"
    - "10.0.0.0/8"          # 若反代在内网
```

### 2.3 IP 限流与惩罚封禁

```yaml
security:
  allow_cidrs: []           # 豁免限流/自动封禁（危险路径仍被静态拒绝）
  ip_rate_limit:
    enabled: true
    window: "1m"
    max_requests: 120
    burst: 30
    block_duration: "1m"
    status_code: 429        # 响应带 Retry-After
  penalties:
    enabled: true
    threshold: 20           # 窗口内命中次数
    window: "1m"
    ban_ttl: "10m"
    escalate: true          # 反复触发则延长封禁
    # 只对低误报信号自动封禁：deny_path / deny_path_prefix / deny_method
```

### 2.4 过载保护

```yaml
security:
  overload:
    max_global_inflight: 1000
    max_pending_requests: 200
    queue_timeout: "500ms"
    per_route:
      - name: "control-api"
        path_prefix: "/api"
        max_inflight: 50
        max_pending_requests: 0      # 控制面写请求不排队，快速 429
        overload_status_code: 429
      - name: "agent-tunnel"
        path_prefix: "/gw/agent"
        max_inflight: 200
        max_pending_requests: 20
        queue_timeout: "1s"
        overload_status_code: 503
      - name: "proxy"
        path_prefix: "/s"
        max_inflight: 400
        max_pending_requests: 80
        queue_timeout: "1s"
        overload_status_code: 503
```

---

## 3. 认证加固（`auth.*`）

### 3.1 登录限流与会话

```yaml
auth:
  session:
    access_ttl: 12h
    remember_ttl: 720h            # 仅显式"记住我"
    sliding_renewal: true
    session_version_enabled: true # 改密/撤销后旧 Cookie 立即失效
  login_rate_limit:
    enabled: true
    per_ip:      { window: "5m",  max: 10 }
    per_account: { window: "5m",  max: 5 }
    global:      { window: "1m",  max: 60 }
    fail_mode: closed             # 失败计数 fail-closed
    lockout: "15m"                # 连续失败后的短时锁定
```

### 3.2 人机识别（低摩擦优先）

```yaml
auth:
  human_verification:
    enabled: true
    default_provider: cloudflare_turnstile
    login_policy: risk_based      # 低风险不挑战；失败/新设备才触发
    register_policy: always       # 如需注册入口
    fallback_provider: builtin_captcha
    cloudflare_turnstile:
      site_key: ${CLOUDFLARE_TURNSTILE_SITE_KEY:-}
      secret_key: ${CLOUDFLARE_TURNSTILE_SECRET_KEY:-}   # 仅服务端
      verify_timeout: 3s
      expected_hostname: ${PUBLIC_HOSTNAME:-}
      expected_action_login: login
```

> 服务端必须调用 Turnstile Siteverify 校验，不能只信前端；`secret_key` 不返回前端、不写审计明文。

### 3.3 MFA 与静态令牌收敛

```yaml
auth:
  mfa:
    totp_required_for_high_risk: true   # 撤销令牌/发布安全策略/批量解封等
  passkey:
    enabled: true                        # 推荐；免密登录
  static_admin_token:
    enabled: false                       # 默认关闭；如需则短期、可轮换、受审计
```

---

## 4. 可选：GeoIP（默认关闭，启用时先 monitor）

```yaml
security:
  geo:
    enabled: false
    mode: monitor                 # monitor | enforce
    fail_mode: allow              # 数据库/查询失败默认放行，避免全站失败
    provider: maxmind
    database_path: "/var/lib/aicli-gateway/geoip/GeoLite2-Country.mmdb"
    custom_cidrs:
      - { cidr: "203.0.113.0/24", country: "XX", asn: 64500 }
    rules:
      - { country: "XX", action: "monitor" }
      - { path_prefix: "/api", country: "XX", action: "block" }
```

> 私网/回环/unspecified 跳过 Geo；数据库过期（如 > 35 天）应告警；公开 API 谨慎 `block`。

---

## 5. 安全策略 monitor → enforce 工作流

```powershell
# 1) 观察：只计数不拦，采集 7–14 天命中率
aicli-gateway security policy set --mode monitor --reason "观察窗口" --json

# 2) 模拟：确认规则命中与状态码
aicli-gateway security simulate --method GET --path "/.git/config" --remote-addr 203.0.113.7 --json

# 3) 小范围 enforce（先对管理端/明确恶意路径）
aicli-gateway security policy set --mode enforce --reason "命中率达标，切 enforce" --json

# 4) 回滚
aicli-gateway security policy rollback --reason "误伤正常请求" --json
```

---

## 6. 环境变量速查

| 变量 | 用途 | 默认 |
|------|------|------|
| `AICLI_GATEWAY_SECURITY_MODE` | `monitor` / `enforce` | `monitor` |
| `AICLI_GATEWAY_TRUSTED_PROXIES` | 可信代理 CIDR（逗号分隔） | `127.0.0.1/32,::1/128` |
| `AICLI_GATEWAY_IP_RATE_LIMIT_ENABLED` | 入口 IP 限流开关 | `true` |
| `AICLI_GATEWAY_OVERLOAD_MAX_INFLIGHT` | 全局 in-flight 上限 | `1000` |
| `AICLI_GATEWAY_OVERLOAD_MAX_PENDING` | 全局 pending 上限 | `200` |
| `CLOUDFLARE_TURNSTILE_SITE_KEY` | Turnstile site key | 空 |
| `CLOUDFLARE_TURNSTILE_SECRET_KEY` | Turnstile secret（仅服务端） | 空 |
| `PUBLIC_HOSTNAME` | 期望 hostname（Turnstile 校验） | 空 |

> 敏感项（secret、owner 哈希）优先环境变量/密钥管理，不写入仓库或日志明文。

---

## 7. 上线检查清单

- [ ] TLS 已配置（或可信反代终止），管理 API 匿名不可达。
- [ ] `trusted_proxies` 与真实反代/LB 一致；伪造 XFF 不生效。
- [ ] 静态 deny + 路径规范化生效；`unknown_route` 已从 `count` 评估后可切 `reject`。
- [ ] IP 限流返回 `429`+`Retry-After`；allowlist 不误封且危险路径仍拒。
- [ ] 过载阈值经压测校准；控制面写请求不排队。
- [ ] 登录限流/分级风控/会话失效生效；安全事件可查。
- [ ] `/api/health`、控制台、已认证会话在阈值内不受影响（门禁回归）。
- [ ] 指标可采集、告警已配置；Runbook（[10](./10-runbook.md)）可用。
