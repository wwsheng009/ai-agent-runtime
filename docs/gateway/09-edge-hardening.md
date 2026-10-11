# 09 · 公网边缘防护设计（参考 ai-gateway Edge 安全）

> 上游：[04-security.md](./04-security.md)、[02-architecture.md](./02-architecture.md)
> 参考来源：外部项目 `E:\projects\ai\ai-gateway` 的安全设计——
> `docs/plan/gateway-edge-traffic-attack-protection-analysis-plan-20260521.md`、
> `docs/plan/gateway-edge-security-protection-implementation-tracking-20260521.md`、
> `docs/plan/auth-security-hardening-plan-20260612.md`、
> `docs/plan/gateway-edge-security-attack-runbook-20260521.md`。
> 本文把这些经验落到 **aicli-gateway（外网会话网关）** 的公网入口层，并明确**借用/不借用**。

---

## 1. 借用与不借用

| 外部能力 | 是否借用 | 说明 |
|----------|----------|------|
| 分层防御（Edge admission 在最外层、廉价拒绝） | ✅ | 核心；恶意流量不得进入隧道/注册表/DB/日志 |
| 静态 deny（path/prefix/method/CIDR）+ 路径规范化 | ✅ | 我们合法路由极少，可**默认拒绝未知路径** |
| 可信代理下的客户端 IP 解析 | ✅ | 网关常置于 Nginx/Caddy/LB 之后，必须防 XFF 伪造 |
| IP 限流 + 惩罚自动封禁（TTL/升级/解除/审计） | ✅ | 公网入口第一道 |
| 过载保护（全局/路径级 in-flight、pending、queue timeout） | ✅ | 结合隧道流控与长连接预算 |
| 管理面保护（管理端口不公网、写操作需 reason + 审计） | ✅ | 我们的 `/api/*` 与 `/gw/agent` 分别保护 |
| 策略即版本化产物（draft/validate/simulate/publish/rollback/drift） | ⚠️ 轻量化借用 | 单用户场景**不需要**多实例编排与 RBAC；保留"dry-run → monitor → enforce + 可回滚 + 审计" |
| 低基数安全指标 + 安全事件 + 告警 | ✅ | 见 §8 |
| fail-open / fail-closed 默认值显式化 | ✅ | 见 §9 |
| 正常请求保障作为验收门禁 | ✅ | 见 §10 |
| 安全事件处置 Runbook（P1–P4） | ✅ | 见 §11 |
| 多租户 / RBAC / 计费 / provider 熔断 | ❌ | 非本网关范围（单用户 hub） |
| GeoIP 国家/ASN 屏蔽 | ⚠️ 可选 | 单用户场景收益有限；如需可后置，且默认 `monitor` |
| 第三方人机识别（Turnstile） | ✅（认证层） | 见 [04](./04-security.md) §2.1 |

---

## 2. 分层防御模型

我们的网关是**单二进制**，但按职责分四层，**由外向内**逐层收紧：

```text
L0 Edge admission（无认证、最廉价、最先执行）
   ├─ 静态 deny（path/prefix/method/CIDR）+ 路径规范化
   ├─ 客户端 IP 解析（可信代理）+ IP 限流
   ├─ 惩罚自动封禁（低误报信号）
   ├─ 过载保护（全局/路径级 in-flight、pending）
   └─ 未知路径计数/拒绝（默认拒绝未知路径）
        │  恶意流量在此被挡，不进入后续层
        ▼
L1 控制面认证
   ├─ /api/*：owner 登录（Cookie）+ CSRF + 限流 + 分级风控
   └─ /gw/agent：设备令牌
        ▼
L2 隧道/反向代理
   ├─ 路由白名单 + 能力位裁剪（[02](./02-architecture.md) §4.2）
   ├─ 每流信用窗口/背压；单节点流数/帧/body 上限（[03](./03-protocol.md) §5）
   └─ 会话→节点解析、presence
        ▼
L3 本地（不变）
   └─ 仅回环 + Host/Origin 校验 + 本机令牌（agent 注入）
```

**核心原则**：能在外层挡住的，绝不放进去；被拒的请求**不消耗** in-flight/流/DB/日志预算（借鉴外部项目"deny 不占 in-flight"）。

---

## 3. 静态拦截（L0）

### 3.1 规则

| 规则 | 说明 |
|------|------|
| `deny_paths` | 精确路径拒绝（如 `/.env`、`/.git/config`、`/phpinfo.php`） |
| `deny_path_prefixes` | 前缀拒绝（如 `/.git/`、`/wp-admin/`、`/wp-login`） |
| `deny_methods` | 方法拒绝（如 `TRACE`、`CONNECT`） |
| `deny_cidrs` | 来源网段拒绝 |
| **未知路径策略** | 我们合法路由极少（`/`、`/api/*`、`/gw/agent`、`/n/*`、`/s/*`、静态资源）→ **默认拒绝**未知路径（`404`/`403`），仅先以 `count` 观察 |

### 3.2 路径规范化（防绕过）

借鉴外部项目：`url.PathUnescape` → `path.Clean` → 拒绝含 `..` → 再做前缀匹配；
拒绝 `//`、`%2e%2e`、大小写变体等绕过；**双层校验**（L0 + 被代理端原有校验保留）。

### 3.3 默认基线（示例）

```yaml
security:
  enabled: true
  mode: enforce            # monitor | enforce（先 monitor 观察）
  deny_paths: ["/.env", "/.git/config", "/phpinfo.php"]
  deny_path_prefixes: ["/.git/", "/wp-admin/", "/wp-login", "/vendor/"]
  deny_methods: ["TRACE", "CONNECT"]
  unknown_route: reject    # count | reject（生产建议 reject）
```

---

## 4. 客户端 IP 与可信代理（L0）

- **只在来源可信时**才使用 `X-Forwarded-For` / `X-Real-IP`；否则一律用 socket 对端地址。
- 配置 `security.trusted_proxies`（CIDR 列表，默认 `127.0.0.1/32`、`::1/128`）；不匹配则忽略转发头。
- 安全模块使用**独立的** client IP 解析（与业务日志分离），避免不同模块口径不一致（外部项目经验）。
- **风险**：若错误信任 XFF，攻击者可伪造 IP 绕过限流/封禁；文档与测试都要钉住"不可信来源不信任 XFF"。

```yaml
security:
  trusted_proxies: ["127.0.0.1/32", "::1/128"]   # 反代/LB 地址
```

---

## 5. IP 限流与惩罚封禁（L0）

### 5.1 IP 限流

- 维度：来源 IP（哈希化存储，避免明文扩散）；窗口 + burst。
- 超限返回 `429` + `Retry-After`；**在 admission 前执行**，独立于控制面认证限流。
- 默认**开启**（生产基线），阈值按容量压测确定；`monitor` 模式只计数不拦。

### 5.2 惩罚自动封禁（penalty）

- 只对**低误报信号**自动封禁：命中静态 deny、明显扫描路径、异常方法。
- 达阈值 → TTL 临时封禁（可升级）；支持**手动封禁/解除**；`allow_cidrs` **永不自动封禁**。
- **重要**：allowlist 豁免限流/自动封禁，但**危险路径仍被静态拒绝**（避免 allowlist 变成路径绕过）。
- 所有封禁/解除**必须审计**（`manual_ban` / `penalty_ban` / `manual_unban` / `penalty_unban`）。

```yaml
security:
  allow_cidrs: []                  # 豁免限流/自动封禁（危险路径仍拒绝）
  ip_rate_limit:
    enabled: true
    window: "1m"
    max_requests: 120
    burst: 30
    block_duration: "1m"
    status_code: 429
  penalties:
    enabled: true
    threshold: 20
    window: "1m"
    ban_ttl: "10m"
    escalate: true
```

### 5.3 误封恢复（与封禁同等优先级）

- 提供 ban 列表查询、手动封禁、单个/批量解除；解除可同时清理手动与 penalty 封禁。
- 解除需 `reason` + 审计；控制台"安全"页展示 Top 来源 IP / Top 路径 / 动作分布。

---

## 6. 过载与资源保护（L0/L2）

| 层 | 控制 | 说明 |
|----|------|------|
| 全局 | `max_global_inflight`、`max_pending_requests`、`queue_timeout` | 超限 `503`（或 `429`），可短队列 |
| 路径级 | 每路由 in-flight/pending（`/gw/agent`、`/api/*`、`/n/*`、`/s/*`） | 保护控制面与代理 |
| 隧道 | 单节点最大流数、单流缓冲、单连接总缓冲、帧上限 | 见 [03](./03-protocol.md) §5 |
| 长连接 | SSE 空闲超时、h2/HTTP3 连接预算 | 见 [05](./05-wan-and-mobile.md) |
| 请求体 | 上传端点单独上限；大 body 流式不整包缓冲 | 见 [03](./03-protocol.md) §4.5 |

原则：**LLM/SSE 类长请求允许短队列**；**控制面写请求不排队**（快速 429），避免重复提交与堆积。

---

## 7. 管理面与控制面保护（L1）

| 入口 | 保护 |
|------|------|
| `/api/*`（owner 管理 API） | owner 登录（HttpOnly Cookie + CSRF）+ 限流 + 分级风控；**不公网匿名可达** |
| `/gw/agent` | 设备令牌（哈希存储、可撤销、绑定 node_id） |
| 高风险写操作（撤销令牌、发布安全策略、批量解除封禁） | 需 `reason`、二次确认、审计；静态管理令牌可轮换/短期化 |
| 管理端口 | 不单独暴露公网；如需本地运维端口，默认仅回环 + token（借鉴外部"Edge 管理端仅本地/token"） |

---

## 8. 可观测与告警（低基数）

### 8.1 指标

```text
gateway_security_decisions_total{method,path_group,rule,action}   # allow|block|monitor
gateway_unknown_route_total{method,path_group}
gateway_inflight / gateway_pending
gateway_path_inflight{route} / gateway_path_pending{route}
gateway_agent_connections / gateway_tunnel_streams
gateway_auth_events_total{event,result}                          # 见 04 §5
```

- `path_group` 归类（`/api`、`/gw`、`/n`、`/s`、`/static`、`other`、`/suspicious/*`），**避免高基数**（不把原始路径当标签）。

### 8.2 安全事件

- 本地 ring buffer + 可持久化：记录 `block` 决策、手动/自动封禁、解除。
- 聚合视图：Top 来源 IP、Top 路径组、动作分布、时间线。

### 8.3 告警（示例）

| 条件 | 说明 |
|------|------|
| `increase(gateway_security_decisions_total{action="block"}[5m])` > 平时基线 3× | 阻断量异常 |
| `increase(gateway_unknown_route_total[5m])` > 基线 3× | 扫描增长 |
| `gateway_pending` 持续接近上限 | 排队压力过高 |
| `gateway_auth_events_total{event="login_failed"}` 突增 | 爆破尝试 |

---

## 9. 默认值：fail-open / fail-closed

| 控制 | 默认 | 失败时 | 理由 |
|------|------|--------|------|
| 静态 deny | 开启 | fail-closed（拒绝） | 低误报、纯本地判定 |
| IP 限流 | 开启（生产基线） | fail-closed（超限拒绝） | 内存/共享存储不可用时按配置降级并告警 |
| 惩罚封禁 | 开启 | fail-open（不封） | 避免存储故障误封正常用户 |
| 过载保护 | 开启 | fail-closed（拒绝超载） | 保命优先 |
| 未知路径 | `count` → `reject` | 先观察后拒绝 | 避免误伤未登记的合法路由 |
| 认证限流 | 开启 | **登录失败计数 fail-closed**；验证码获取可 fail-open | 防爆破优先 |
| GeoIP（可选） | 关闭 | fail-open | 数据/第三方故障不得全站失败 |
| 安全策略发布 | `monitor` 优先 | 不自动 block | 先观察命中率再 enforce |

> 与外部项目一致：**先 observe/shadow 采集命中率（建议 7–14 天），再切 enforce**，避免一次性启用导致误伤。

---

## 10. 正常请求保障（验收门禁）

借鉴外部项目"正常请求保障是验收门禁"：**安全策略不得误伤阈值内的正常请求**。必须回归验证：

- `GET /api/health`、控制台静态资源、登录流程在阈值内正常；
- 已认证的会话打开、SSE、文件下载/上传在阈值内正常；
- 隧道注册/心跳不受 IP 限流误伤（`/gw/agent` 单独配额）；
- 共享出口（公司/校园/运营商 NAT）不应仅凭 IP 直接封禁——叠加 UA 摘要、失败率、设备令牌维度。

---

## 11. 安全事件处置 Runbook（摘要）

> 完整版（含具体命令、阈值、误封恢复、回滚与复盘）见 [10-runbook.md](./10-runbook.md)；
> 可复制的配置见 [11-security-config-examples.md](./11-security-config-examples.md)。

分级（借鉴外部 P1–P4）：

| 等级 | 现象 | 首要动作 |
|------|------|----------|
| P1 | 网关不可用；`/api/health` 失败；连接/CPU 耗尽 | 收紧过载阈值；对明确恶意路径/IP/CIDR 临时封禁；必要时回滚最近安全策略 |
| P2 | 大量扫描路径（`/.env`、`/.git/*`）但正常可用 | 确认/补充 deny，开启 penalty 自动封禁，观察事件与指标 |
| P3 | 某区域/ASN 异常访问（可选 Geo） | 先 `monitor`，验证命中后对管理端路径切 `block` |
| P4 | 单 IP/NAT/设备令牌高频超限 | 调 IP 限流或设备令牌配额，**优先避免大范围 CIDR 封禁** |

处置原则：**先观测/模拟，再逐步扩大阻断**；写操作必须带 `reason` 且经 API/CLI（不手工改运行态）；**误封恢复与封禁同等优先级**；处置后复盘并更新基线。

---

## 12. 与认证加固的衔接

认证层（owner 登录）的分级风控、限流、人机识别、会话失效、MFA、安全事件审计见
[04-security.md](./04-security.md) §2.1 与 §5。两者关系：

- **L0 Edge**：网络层与协议层防护（IP/路径/方法/过载/封禁）；
- **L1 认证**：身份层防护（登录限流、分级挑战、会话撤销）。

---

## 13. 落地清单与验收

### 13.1 落地清单

| 项 | 落点 | 阶段 |
|----|------|------|
| `internal/gateway/edge/`（静态 deny + 路径规范化 + 未知路径） | 新包 | P0 |
| client IP 解析（trusted proxy） | `edge/` | P0 |
| IP 限流 + 惩罚封禁 + ban API + 审计 | `edge/` | P1 |
| 过载保护（全局/路径级） | `edge/` | P1 |
| 安全策略 `mode: monitor\|enforce` + 变更需 reason + 回滚 | `edge/` + `cmd/gateway` | P1 |
| 低基数指标 + 安全事件 + 告警 | `edge/metrics.go`、`edge/events.go` | P1–P2 |
| Runbook 文档 | [10-runbook.md](./10-runbook.md) | P1 |
| 配置示例 | [11-security-config-examples.md](./11-security-config-examples.md) | P1 |

### 13.2 验收

- [ ] 未知路径默认拒绝（可切 `count` 观察），扫描路径 `403` 且不占 in-flight。
- [ ] 不可信来源的 `X-Forwarded-For` 被忽略（伪造 IP 无法绕过限流）。
- [ ] 超过 IP 限流返回 `429` + `Retry-After`；allowlist 不误封且危险路径仍拒绝。
- [ ] 惩罚封禁可查询/解除，全部动作有审计。
- [ ] 过载时正常低速请求仍 2xx；控制面写请求快速 `429` 不排队。
- [ ] `/api/health`、控制台、已认证会话在阈值内不受安全策略影响（门禁回归）。
- [ ] 安全指标低基数、可告警；安全事件可查 Top IP/路径。
- [ ] 安全策略支持 `monitor → enforce` 与回滚，变更有 `reason` 与审计。
- [ ] fail-open/fail-closed 默认值与 §9 一致，且有测试覆盖。
