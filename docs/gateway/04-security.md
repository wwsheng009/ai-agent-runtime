# 04 · 单用户安全模型

> 上游：[02-architecture.md](./02-architecture.md)、[03-protocol.md](./03-protocol.md)
> 前提：**不需要多用户**。本文只解决"一个 owner 安全地从公网访问自己本机会话"这一件事，
> 并保持与现有回环安全模型（`docs/aicli/web-remote-api.md` §1）兼容。

---

## 1. 信任边界

```text
┌──────────────────────────────────────────────────────────────────────────┐
│ 不可信：公网                                                            │
│   · 任意扫描者 / 攻击者 / 中间人                                         │
│   · 浏览器（可能被 XSS / 恶意扩展污染）                                   │
└───────────────┬──────────────────────────────────────────────────────────┘
                │ ① HTTPS（TLS）——必须
┌───────────────▼──────────────────────────────────────────────────────────┐
│ 半可信：aicli-gateway（owner 自己部署的公网服务器）                        │
│   · 持有：owner 凭据哈希、设备令牌哈希、注册表元数据、审计                 │
│   · 不持有：会话正文、本机回环令牌原文（仅转发时由 agent 注入）            │
└───────────────┬──────────────────────────────────────────────────────────┘
                │ ② wss（TLS）+ 设备令牌——必须
┌───────────────▼──────────────────────────────────────────────────────────┐
│ 可信：本地设备（owner 自己的机器）                                        │
│   · 持有：会话正文、本机回环令牌、LLM 凭据                                │
│   · 回环服务仍只监听 127.0.0.1，不开放公网端口                            │
└──────────────────────────────────────────────────────────────────────────┘
```

关键信任决策：**网关是半可信**（owner 自部署）。首期不做端到端加密（E2EE），
因为网关需要按路径代理、注入/剥离头、做流控与合并；E2EE 会使其退化为纯字节中继，
并需要浏览器侧参与密钥协商（见 §8 远期项）。

---

## 2. 认证体系（三层，互不混用）

| 层 | 凭据 | 持有者 | 用途 | 存储 |
|----|------|--------|------|------|
| L1 Owner 登录 | 密码 / Passkey（+可选 TOTP） | 人类 owner | 登录控制台 | Argon2id 哈希，存网关 |
| L2 设备令牌 | 随机 256-bit，前缀 `agw_` | 本地 agent | 建立/认证隧道 | SHA-256 哈希，存网关；原文只显示一次 |
| L3 本机回环令牌 | `X-AICLI-Token` / admin token | **仅本地进程** | 通过本机 Host/Origin + 令牌校验 | 不离开本机；由 agent 注入 |

**不混用**：浏览器 Cookie（L1）绝不能被当作设备令牌（L2）；设备令牌（L2）绝不能转发给本地服务；
本机令牌（L3）绝不出现在任何网络消息里。

### 2.1 L1 Owner 登录与认证加固

借鉴 `ai-gateway` 的认证加固（`docs/plan/auth-security-hardening-plan-20260612.md`），
owner 登录按"**先观察、再挑战、后阻断**"的**分级风控**处理，兼顾安全与体验。

| 项 | 设计 |
|----|------|
| 初始化 | `aicli-gateway init` 设置 owner 密码（或 `--owner-password-hash` 注入）；哈希用 **Argon2id** |
| 登录 | `POST /api/auth/login {password, totp?, human_verification?}` → 校验 → 签发 **HttpOnly + Secure + SameSite=Lax** 会话 Cookie |
| 会话 | 短期 access（如 12h）+ 滑动续期；"记住我"需显式；Cookie 写操作要求 CSRF（`X-Gateway-CSRF` 双提交或 Origin 校验） |
| 登录限流 | 按 **IP + 账号 + 全局** 多桶；失败窗口 + 退避；**失败计数 fail-closed**；多实例用共享存储（Redis/DB），内存实现作降级 |
| 分级风控 | 低风险直接放行；中低风险优先**低摩擦人机识别**；中风险加短延迟/验证码；高风险短时锁定；严重风险封禁 |
| 人机识别 | 优先 Cloudflare Turnstile（**服务端 Siteverify** 校验，不信前端）；fallback 内置图片验证码；`none` 仅限内网/开发 |
| 会话失效 | 改密/撤销后递增 `session_version` / `security_stamp`，旧 Cookie 立即失效 |
| MFA | 高风险/管理操作强制 TOTP；Passkey（WebAuthn）推荐用于免密登录 |
| 静态令牌 | 任何静态管理令牌都必须可轮换、短期、受审计（`admin_static_token_used` / `_rejected` 事件） |
| 审计 | 成功/失败/限流/验证码失败/封禁命中均写安全事件（见 §5） |

**分级处置**：

| 风险 | 信号 | 处置 |
|------|------|------|
| 低 | 常用设备/地区、失败少 | 直接放行，仅审计 |
| 中低 | 新设备/IP、首次验证码失败 | Turnstile 无感/托管挑战 |
| 中 | 同账号/IP 多次失败 | Turnstile + 短延迟 |
| 高 | 窗口内连续失败、自动化特征 | 短时限流/锁定 |
| 严重 | 已封禁 IP、命中异常路径 | 阻断 + 告警 + 人工处置 |

**体验原则**：不对低风险用户强制图片验证码；阈值先 `observe/shadow` 采集 7–14 天再 `enforce`；
共享出口（公司/校园/NAT）不单凭 IP 封禁；错误提示保持模糊（"请求过于频繁"/"密码校验失败"），不泄露阈值。

> 网络层防护（静态 deny、IP 限流、惩罚封禁、过载、可信代理、Runbook）见
> [09-edge-hardening.md](./09-edge-hardening.md)。

### 2.2 L2 设备令牌

- 生成：`POST /api/devices {name}` → 返回**仅一次**的令牌原文 `agw_<base64url(32B)>`。
- 存储：网关只存 SHA-256 哈希 + 前缀（前 8 位）用于识别；列表只显示前缀。
- 使用：agent 在 WS 升级时 `Authorization: Bearer agw_...`；网关常量时间比较哈希。
- 撤销：`DELETE /api/devices/{id}` → 立即关闭该令牌的所有隧道，并拒绝重连（`auth_invalid`，agent 不重试）。
- 轮换：支持生成新令牌并设置旧令牌宽限期。
- 绑定（可选）：令牌可绑定 `node_id` / 期望 `kind`，防止令牌被用于注册成别的主体。

### 2.3 L3 本机回环令牌（保持不变）

- `aicli` 微前端：`X-AICLI-Token`（或 `?token=`）；回环请求免令牌、非回环需令牌。
- `runtime-server`：`X-Skills-Admin-Token` / `Bearer`，回环放行。
- agent 转发时以本机回环身份请求，并注入对应令牌（从本地进程内存读取，不落网关）。
- **为什么可行**：agent 就在本机，请求从 `127.0.0.1` 发出，Host/Origin 校验自然通过；
  令牌由 agent 注入而非浏览器，攻击面不扩大。

---

## 3. 路由白名单与能力裁剪（防开放代理）

网关**不是**通用反向代理。转发前必须同时满足：

1. 路径命中该 node 的 `routes` 白名单（前缀匹配，规范化后比较，拒绝 `..`、`//`、编码绕过）；
2. 该路径所需能力位已在 node `capabilities` 中声明；
3. 方法在允许集合内（默认：`GET/POST/PUT/DELETE/PATCH/HEAD/OPTIONS`，可按能力裁剪）；
4. 未命中 → **403**（`route_not_allowed` / `capability_not_granted`），并记审计。

默认白名单（`aicli`）：

```text
/web, /web/api, /debug/endpoints, /health   （可按需裁剪）
```

默认白名单（`runtime-server`）：

```text
/api/runtime, /api/agent, /healthz, /        （/ 为 SPA；可裁剪）
```

**路径规范化**：先 `url.PathUnescape` → `path.Clean` → 拒绝含 `..` 的路径 → 再做前缀匹配；
防止 `/web/../api/...`、`%2e%2e` 等绕过。网关与被代理端**双重校验**（本地原有校验保留）。

---

## 4. 头处理策略

| 类别 | 处理 |
|------|------|
| Hop-by-hop（`Connection`、`Keep-Alive`、`Transfer-Encoding`、`Upgrade`、`Proxy-*`、`TE`、`Trailer`） | 剥离，不转发 |
| 网关内部头（`X-Gateway-*`） | 进入网关时剥离（防注入），仅在网关→agent 的受控注入中使用 |
| `Cookie` | **不下发**给本地（浏览器 Cookie 属于网关）；本地若需要会话选择用 `X-Gateway-Session` 或查询串 |
| `Authorization` | 剥离浏览器侧的（owner 用 Cookie）；本地令牌由 agent 注入 |
| `Host` | agent 转发时改写为本地回环 Host（`127.0.0.1:<port>`），保证本机 Host 校验通过 |
| `Origin` | agent 转发时按需设置/清除，保证本机 Origin 校验通过 |
| `X-Forwarded-For` / `-Proto` / `-Host` | 网关可选注入（仅可信代理场景），供本地审计；默认关闭 |
| `Accept-Encoding` | 网关可自行压缩下游响应（见 05 文档）；转发上游时按需处理 |
| `Range` / `If-*` / `Last-Event-ID` | 透传 |

---

## 5. 审计与可观测（安全相关）

记录以下**安全/审计事件**（不含正文与令牌原文；借鉴 `ai-gateway` 的统一安全事件模型）：

| 类别 | 事件 |
|------|------|
| 认证 | `login_success` / `login_failed` / `login_rate_limited` / `captcha_failed` / `captcha_rate_limited` / `refresh_rate_limited` |
| 凭据生命周期 | `password_change_success` / `password_change_failed` / `password_change_rate_limited` / `password_reset_by_admin` |
| 设备令牌 | `device_created` / `device_revoked` / `device_used` / `access_key_ip_denied`（如启用 IP 白名单） |
| 边缘防护 | `security_blocked` / `security_penalty_banned` / `manual_ban` / `manual_unban` / `route_denied` / `capability_denied` / `rate_limited` |
| 隧道 | `tunnel_connected` / `tunnel_disconnected` / `tunnel_replaced`（fencing） |
| 管理面 | `admin_static_token_used` / `admin_static_token_rejected` |
| 代理 | `proxy_request`（node、session、method、path_group、status、bytes、耗时；路径脱敏） |

事件字段建议：`created_at`、`actor_type`（owner/device/system/anonymous）、`event_type`、`result`（success/failed/blocked/rate_limited）、
`risk_level`（low/medium/high/critical）、`client_ip_hash`（哈希化，避免明文扩散）、`user_agent_hash`、`policy_key`、`request_id`。
默认保留 90 天（可配），高风险事件可长期归档。

**指标（Prometheus，低基数）**：在线节点数、活跃隧道、活跃流、转发字节、错误率、重连次数、P50/P95 延迟；
安全决策 `gateway_security_decisions_total{method,path_group,rule,action}`、`gateway_unknown_route_total`、
`gateway_inflight`/`gateway_pending`、`gateway_auth_events_total{event,result}`（详见 [09](./09-edge-hardening.md) §8）。

---

## 6. 限流与资源上限

| 维度 | 默认 | 说明 |
|------|------|------|
| 登录尝试 | 5/min/IP，全局 30/min | 失败退避 + 锁定 |
| 设备注册连接 | 单 IP 10/min | 防扫描 |
| 节点数 | 64 | `--max-nodes` |
| 单节点流数 | 256 | `--max-streams-per-node` |
| 单连接总缓冲 | 8MiB | 超限背压/丢弃 |
| 单流缓冲 | 256KB | 同上 |
| 单帧 | 1MiB | `max_frame` |
| 请求体 | 可配（默认 32MiB，上传端点另设） | 防大 body DoS |
| 空闲流超时 | 120s | 无数据且非 SSE → reset |

---

## 7. 与现有安全模型的兼容性检查

| 现有机制 | 网关接入后 | 结论 |
|----------|-----------|------|
| `aicli` Host 校验（必须回环） | agent 以 `127.0.0.1` 转发 | ✅ 通过 |
| `aicli` Origin 校验（同源） | agent 不携带浏览器 Origin 或设为回环 | ✅ 通过 |
| `aicli` 写令牌 `X-AICLI-Token` | agent 注入 | ✅ 通过；浏览器/网关无令牌 |
| `aicli` 非回环 cookie 导航 | 网关场景走回环转发，不触发 | ✅ 不影响 |
| `runtime-server` 回环放行 / admin token | agent 以回环转发并注入 token | ✅ 通过 |
| `runtime-server` scope resolver（JWT/api key） | 网关不注入这些；沿用回环 identity | ✅ 通过（单用户场景足够） |
| `mesh` 令牌披露面收窄 | 网关控制台不显示任何本机令牌 | ✅ 一致 |

> **重要**：不得为了网关而放宽本机校验（例如把 Host 校验改成允许任意来源）。网关的价值正是
> 让本地保持"只信任回环"，把公网信任问题收敛到网关一层。

---

## 8. 威胁模型与缓解（STRIDE 摘要）

| 威胁 | 场景 | 缓解 |
|------|------|------|
| Spoofing | 伪造 agent 注册 | 设备令牌（L2）+ TLS 校验 + 令牌绑定 node_id |
| Tampering | 中间人改请求 | 全链路 TLS；网关内部头剥离防注入 |
| Repudiation | 否认操作 | 审计日志（登录/令牌/访问/隧道） |
| Information disclosure | 令牌/正文泄露 | 本机令牌不出本机；网关不存正文；令牌仅存哈希；响应脱敏 |
| DoS | 扫描/洪泛/大 body | 限流 + 资源上限 + 帧/流/连接上限 |
| Elevation of privilege | 路径穿越越权访问 | 路径规范化 + 白名单 + 能力裁剪 + 双重校验 |
| 开放代理滥用 | 把网关当跳板 | 只允许注册节点的白名单路由；拒绝任意 host/路径 |
| 浏览器 XSS 窃取会话 | 控制台被注入 | HttpOnly Cookie + CSP + 前端转义；令牌不在 JS 可见处 |
| 设备令牌泄露 | 令牌被复制 | 可撤销 + 绑定 node_id + 使用审计 + 轮换 |

**远期可选（不在首期）**：

- **E2EE（端到端加密）**：浏览器与 agent 协商密钥，网关只做密文中继。代价：网关无法做路径代理/流控/合并，
  需要浏览器侧运行代理逻辑（Service Worker + WebCrypto）。仅在"不完全信任网关部署"时有意义，列入远期评估。
- **mTLS**：agent 用客户端证书替代/叠加设备令牌。
- **Passkey 强制**：完全去掉密码。

---

## 9. 安全默认值清单（落地时必须遵守）

1. 网关默认**要求 TLS**：未配置证书且未声明可信反代时，拒绝以明文对外服务（或仅监听回环并警告）。
2. agent 默认**校验 TLS**；`insecure_skip_verify` 仅允许在显式测试配置下，且打印显著警告。
3. 本地进程**绝不**因配置了网关而把回环服务绑定到公网。
4. 浏览器侧**绝不**接触任何本机令牌（L3）。
5. 新增路由默认**不在白名单**，必须显式声明。
6. 任何"跳过认证"的开发开关都必须**默认关闭**，且仅对回环生效。
7. 审计日志**不得**记录令牌原文与会话正文。
8. 控制台静态资源由 `//go:embed console/` 自带（无外部文件、无 CDN）；下发
   `Content-Security-Policy` / `X-Content-Type-Options: nosniff` / `Referrer-Policy`；
   `index.html` 不缓存、**不注入任何本机令牌**（见 [07-console.md](./07-console.md) §7.2）。
