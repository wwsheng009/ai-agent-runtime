# 站点类型检测与账户余额/订阅共用能力方案

日期：2026-07-29

状态：plan（待评审实现）

适用范围：

- `aicli`（`aicli login` / chat `/login` / doctor / 后续 balance 命令）
- `ai-agent-runtime` Web（workspace settings → provider 配置）
- 后续可桥接 `ai-gateway` 用户侧 AI Site 私有账户同步（本方案先做 runtime 本地通用能力，不强制依赖 gateway DB）

参考源码：

| 项目 | 关键路径 |
| --- | --- |
| ai-gateway 站点类型探测 | `internal/service/aisiteresource/site_detect.go`、`site_detect_newapi.go`、`site_detect_sub2api.go` |
| ai-gateway New-API 账户全量同步 | `internal/service/aisiteuseraccount/newapi_account_sync.go` |
| ai-gateway Sub2API 账户全量同步 | `internal/service/aisiteuseraccount/account_full_sync.go` |
| ai-gateway 额度换算 | `internal/service/aisiteuseraccount/newapi_quota_display.go` |
| ai-gateway New-API 客户端 | `internal/service/aisitenewapi/client.go` |
| ai-gateway Sub2API 客户端 | `internal/service/aisitesub2api/client.go` |
| ai-gateway 用户账户 API | `internal/gateway/handlers/user_ai_site_account.go` |
| sub2api API Key 用量/余额 | `backend/internal/handler/gateway_handler.go` → `GET /v1/usage` |
| sub2api 管理端用量 | `backend/internal/handler/usage_handler.go` → `GET /api/v1/usage`、`/api/v1/usage/stats` |
| new-api 额度展示 | `common/constants.go`（`QuotaPerUnit`）、`controller/billing.go`、`controller/misc.go`（`/api/status`） |
| aicli login | `backend/cmd/aicli/commands/provider_login.go`、`login.go`、`chat_login_command.go` |
| provider schema | `backend/internal/agentconfig/config.go` |
| runtime web provider 编辑 | `frontend/src/components/workspace/settings/runtime-provider-domain-editor.tsx` |

---

## 1. 背景与问题

当前 `aicli login` 只完成：

1. 收集 `base_url` / `api_key`（或 Codex OAuth）
2. 校验 models endpoint
3. 写回 `providers.items.<name>`

它**不知道**上游是 New-API、Sub2API 还是纯 OpenAI 兼容中转，也**无法**展示钱包余额、额度余额、订阅剩余等信息。用户在多个第三方站点间切换时，经常需要打开站点控制台才能确认“还能不能用”。

`ai-gateway` 已具备较完整的能力：

- 站点类型探测（`site-detect`）
- New-API / Sub2API 个人账户全量同步
- New-API `quota → 金额/币种` 换算
- 私有账户投影、事件与 portal UI

但这些能力绑定 gateway 的 resource/account/DB 模型，不能直接被 aicli / runtime-server 本地配置路径复用。

本方案目标是在 **ai-agent-runtime** 内建设一套**无 DB 依赖、可 CLI/Web 共用**的轻量能力：

1. 登录时自动识别站点类型（new-api / sub2api / unknown）
2. 按类型拉取账户余额或订阅摘要
3. 把结果写入 provider 配置（或旁路 account cache），供 CLI、TUI、Web 统一展示与刷新

---

## 2. 目标与非目标

### 2.1 目标

- **登录增强**：`aicli login` / `/login` 在 models 校验成功后，自动做 best-effort 站点类型探测。
- **Sub2API 余额**：在已有 API Key 的前提下，优先调用 `GET /v1/usage` 获取 key/账户级余额与订阅信息（用户口语中的 `/v1/usages`，**实际正式路径为 `/v1/usage`**）。
- **New-API 余额**：可选收集 `access_token`（system access token）与 `user_id`（subject user id），调用 `/api/user/self` + `/api/status` 获取额度，并按站点 `quota_per_unit` / 展示类型换算。
- **通用模块**：检测与账户拉取逻辑放在 runtime 共享 Go 包，CLI 与 runtime-server Web 都调用同一实现。
- **配置可观测**：provider 上可看到 `site_type`、余额摘要、最近同步时间、同步来源与失败原因。
- **失败不阻断登录**：探测/余额失败不得导致 models 校验成功后的登录整体失败（除非用户显式 `--require-account`）。

### 2.2 非目标（本阶段不做）

- 不复制 ai-gateway 的 AI Site 资源库、私有账户表、snapshot 全量投影、affiliate/checkin 全链路。
- 不强制用户绑定 gateway 平台账号。
- 不实现多账号密钥池、自动充值、余额告警推送（可预留字段，后续单独计划）。
- 不把 New-API 用户名密码登录作为 aicli 默认交互（可列为后续增强；MVP 仅可选 access_token + user_id）。
- 不把 Sub2API 完整 JWT 会话管理作为登录必选（MVP 以 API Key + `/v1/usage` 为主）。

---

## 3. 现状分析（代码事实）

### 3.1 ai-gateway 站点类型检测

探测入口：

- User：`POST /user/ai-sites/site-detect`
- Admin：`POST .../site-detect`
- Site Probe 复用 `siteprobe/site_metadata.go` → `DetectSite`

核心候选（节选）：

| 路径 | 站点类型 |
| --- | --- |
| `/api/v1/status`、`/api/v1/auth/me`、`/api/v1/settings/public`、`/setup/status`、`/health` | sub2api |
| `/api/status`、`/api/user/self`、`/api/user/self/groups` | new-api |
| `/v1/models`、`/api/v1/models` 等 | 仅协议提示（openai/gemini），**不决定** site type |

评分：对 endpoint 命中、HTML/`__APP_CONFIG__`、New-API version header、公共设置字段等加权，输出 `site_type` + confidence（high/medium/low）。

**对 runtime 的启示**：应移植“候选探测 + 打分 + confidence”，而不是只靠用户手工选择。

### 3.2 Sub2API 账户/余额路径

存在两套互补接口：

#### A. API Key 网关路径（最适合 aicli login）

- `GET /v1/usage`
- 鉴权：API Key（与 chat 请求相同）
- 实现：`sub2api/.../gateway_handler.go` `Usage`
- 模式：
  - `quota_limited`：返回 key 的 `quota.limit/used/remaining`、rate_limits、usage
  - `unrestricted`：返回订阅（daily/weekly/monthly limit/usage）或钱包余额路径
- 设计目标注释写明：**for CC Switch integration**，天然适合 CLI/桌面工具

#### B. 用户 JWT 管理路径（适合 gateway 全量同步）

- `GET /api/v1/auth/me` → `balance`、用户状态
- `GET /api/v1/usage/stats?start_date&end_date` → 请求量/成本
- `GET /api/v1/subscriptions` → 订阅列表
- `GET /api/v1/usage` → 分页 usage 明细

ai-gateway `SyncSub2APIAccountFull` 使用 B 路径：

- profile.Balance → `WalletBalanceNumeric`
- usage stats → `UsedQuotaNumeric` / usage summary
- subscriptions → 订阅投影

**本方案 MVP 选择 A 为主、B 为可选增强**，因为 aicli 登录默认只有 API Key。

> 路径纠偏：用户描述的 `/v1/usages` 在源码中对应 **`/v1/usage`**（单数）。实现与文档统一使用 `/v1/usage`，兼容探测时可额外尝试历史别名（若存在）但不作为主路径。

### 3.3 New-API 账户/额度路径

ai-gateway `SyncNewAPIAccountFull`：

1. 解析 session：`system_access_token` + `subject_user_id`，或 username/password 登录后 `IssueSystemAccessToken`
2. `GET /api/user/self`（Header：`Authorization: Bearer <token>` + `New-Api-User: <id>`）
3. `GET /api/status` 读取 `quota_per_unit` / `quota_display_type` / 汇率
4. 可选：tokens、subscriptions、`/api/log/self/stat`、checkin

额度换算（`newapi_quota_display.go`，与 new-api 自身一致）：

```text
default QuotaPerUnit = 500000   # $1 ≈ 500000 quota units

display_value =
  if display_type == TOKENS: raw
  else if display_type == QUOTA: raw
  else: (raw / scale) * exchange_rate

display_type ∈ {USD, CNY, TOKENS, CUSTOM, QUOTA}
```

new-api 源码：

- `common.QuotaPerUnit = 500 * 1000.0`
- `/api/status` 暴露 `quota_per_unit`
- `controller/billing.go` 的 OpenAI 兼容 billing 接口也按同样规则换算

**本方案 MVP**：可选 access_token + user_id → self + status；不强制 username/password。

另有 **仅 API Key** 的弱路径：

- New-API OpenAI 兼容 `GET /v1/dashboard/billing/subscription`、`/v1/dashboard/billing/usage`
- 通常只能反映 token 视角或 used 额度，**不如 self 准确**，可作为 fallback，不作为主路径。

### 3.4 aicli / runtime 现状

- 共享登录：`runProviderLogin`（CLI 与 TUI 已共用）
- Provider 字段无 `site_type` / account 摘要
- Web：`runtime-provider-domain-editor` 可编辑 provider 基础字段，尚无站点类型与余额展示
- 已有计划风格文档：`docs/plan/aicli-provider-login-plan.md`（可对齐其“共享 service、失败不落盘、CLI/TUI 同源”原则）

---

## 4. 总体架构

```text
┌─────────────────────┐   ┌──────────────────────────────┐
│ aicli login / /login│   │ runtime-server Web settings  │
│ aicli account ...   │   │ provider editor / balance UI │
└─────────┬───────────┘   └──────────────┬───────────────┘
          │                              │
          ▼                              ▼
┌────────────────────────────────────────────────────────┐
│ backend/internal/siteaccount  (shared capability)      │
│  - DetectSiteType(baseURL)                             │
│  - FetchAccountSnapshot(siteType, creds)               │
│  - ConvertNewAPIQuota(...)                             │
│  - NormalizeAccountView (CLI/JSON/Web DTO)             │
└───────────────┬───────────────────────────┬────────────┘
                │                           │
     ┌──────────▼──────────┐     ┌──────────▼──────────┐
     │ Sub2API adapters    │     │ New-API adapters    │
     │  /v1/usage (APIKey) │     │  /api/status        │
     │  optional JWT me/   │     │  /api/user/self     │
     │  stats/subs         │     │  optional billing   │
     └─────────────────────┘     └─────────────────────┘
                │
                ▼
     providers.items.<name>  (+ optional auth store secrets)
```

设计原则：

1. **协议层与产品层分离**：`siteaccount` 不依赖 cobra/TUI/React。
2. **站点类型 ≠ 协议**：`protocol=openai` 仍可能是 new-api/sub2api。
3. **凭证分级**：
   - L1 API Key：chat + Sub2API `/v1/usage` + New-API 弱 billing
   - L2 Account Token：New-API system access token + user id；Sub2API JWT（可选）
4. **best-effort**：余额能力增强登录体验，不替代 models 校验作为登录成功条件。
5. **与 gateway 语义对齐**：字段命名、换算公式、endpoint 契约尽量兼容，便于未来双向同步。

---

## 5. 共享模块设计

建议新增包：

```text
backend/internal/siteaccount/
  detect.go                 # 站点类型探测
  detect_test.go
  types.go                  # SiteType, AccountSnapshot, Credential, Confidence
  quota_newapi.go           # 从 gateway 移植的换算逻辑（精简）
  client_http.go            # 统一 HTTP（timeout、UA、body limit）
  adapter_sub2api.go        # /v1/usage (+ optional management APIs)
  adapter_newapi.go         # /api/status + /api/user/self (+ optional)
  normalize.go              # 统一 AccountView
  errors.go                 # 可分类错误码
```

可选后续：

```text
backend/internal/siteaccount/gatewaybridge/
  # 若未来要把 snapshot 推到 ai-gateway 用户账户 API，再加
```

### 5.1 核心类型

```go
type SiteType string

const (
    SiteTypeUnknown SiteType = "unknown"
    SiteTypeNewAPI  SiteType = "new-api"
    SiteTypeSub2API SiteType = "sub2api"
)

type Confidence string // high | medium | low

type DetectInput struct {
    BaseURL string
    Timeout time.Duration
    // 可选：已验证的 models path，用于辅助但不得单独定型
}

type DetectResult struct {
    SiteType      SiteType
    Confidence    Confidence
    Score         map[SiteType]int
    Hits          []EndpointHit
    PlatformHints map[string]any // e.g. quota_per_unit, version
    DetectedAt    time.Time
    Warnings      []string
}

type AccountCredential struct {
    APIKey            string
    // New-API account-level
    SystemAccessToken string
    SubjectUserID     int64
    // Sub2API account-level (optional enhancement)
    AccessToken       string
    RefreshToken      string
}

type AccountSnapshot struct {
    SiteType        SiteType
    Source          string // v1_usage | newapi_user_self | newapi_billing | sub2api_auth_me ...
    Currency        string
    Mode            string // quota_limited | unrestricted | account_quota | unknown

    WalletBalance   *float64
    QuotaBalanceRaw *float64
    QuotaBalance    *float64 // display units after conversion
    UsedQuotaRaw    *float64
    UsedQuota       *float64
    QuotaLimit      *float64
    QuotaRemaining  *float64

    QuotaDisplayScale        *float64
    QuotaDisplayExchangeRate *float64
    QuotaDisplayType         string // USD/CNY/TOKENS/CUSTOM/QUOTA
    QuotaDisplayUnit         string

    Subscriptions []SubscriptionSummary
    Usage         *UsageSummary
    ExternalUser  *ExternalUserSummary

    FetchedAt time.Time
    Partial   bool
    Errors    []string
}
```

### 5.2 站点类型探测（轻量移植）

MVP 探测集（按优先级并行/有限并发）：

**Sub2API**

- `GET /api/v1/status`
- `GET /api/v1/settings/public`
- `GET /setup/status`
- `GET /health`
- （可选）`GET /api/v1/auth/me` 期望 401/无 token 形态

**New-API**

- `GET /api/status`（重点：`quota_per_unit`、`version`、系统名）
- 响应头 `X-New-Api-Version`
- （可选）`GET /api/user/self` 期望 401 形态

**不作为定型依据**

- 仅 `/v1/models` 成功 → `unknown` + protocol openai_compatible

打分规则建议直接对齐 gateway 的“核心 endpoint 命中 > 协议 endpoint”原则；输出 confidence：

- high：明确 status/public 字段或 version header
- medium：多个 401 形态 endpoint 符合预期
- low：弱信号
- 冲突时：保留 scores，`site_type=unknown` 或按更高分，并 warning

登录场景优化：

- 超时默认 3–5s，总探测预算 ≤ 8s
- 失败返回 `unknown`，不抛致命错误
- 可缓存：同一 host 在进程内短缓存（如 10min），避免重复 login 反复打点

### 5.3 Sub2API 账户拉取

#### 主路径（MVP，API Key）

```http
GET {baseURL}/v1/usage?days=7
Authorization: Bearer <api_key>
```

归一化映射建议：

| `/v1/usage` 字段 | AccountSnapshot |
| --- | --- |
| `mode` | `Mode` |
| `quota.limit/used/remaining` | `QuotaLimit/UsedQuota/QuotaRemaining`（USD） |
| `remaining` + `unit` | 主展示余额 |
| `subscription.*` | `Subscriptions[0]` + period usage |
| `usage.today/total` | `Usage` |
| wallet 分支（若响应含 balance） | `WalletBalance` |

展示优先级：

1. `quota_limited.remaining`
2. subscription remaining（按日/周/月最紧约束）
3. wallet balance
4. 仅 usage（无余额时仍展示已用）

#### 增强路径（P1，可选 JWT）

当用户提供 Sub2API access/refresh token 或未来支持用户名密码时：

- `GET /api/v1/auth/me` → wallet
- `GET /api/v1/usage/stats`
- `GET /api/v1/subscriptions`

语义对齐 gateway `buildSub2APIAccountProjection`。

### 5.4 New-API 账户拉取

#### 主路径（MVP，可选 access_token + user_id）

```http
GET {baseURL}/api/status
# public

GET {baseURL}/api/user/self
Authorization: Bearer <system_access_token>
New-Api-User: <subject_user_id>
```

处理：

1. 从 status 构建 `newAPIQuotaDisplayConfig`（默认 scale=500000, rate=1, type=USD）
2. 从 self 读取 `quota` / `used_quota` / `request_count` / profile
3. `applyNewAPIQuotaDisplay*` 得到展示值
4. `Source = newapi_user_self`

凭证来源（交互）：

- flags：`--newapi-access-token`、`--newapi-user-id`
- 交互提示（仅 interactive 且 site_type=new-api）：
  - “检测到 New-API。可选填写系统访问令牌与用户 ID 以同步账户额度（可跳过）”
- 非交互且无 token：跳过账户同步，只写 `site_type`

安全：

- access_token **不得**写入明文 `config.yaml` 的普通字段
- 建议复用/扩展 `$HOME/.aicli/auth.json`（或 `account_auth_ref`）保存：
  - `providers.<name>.account_auth_ref`
  - auth store record: `kind=newapi_system_access_token`, token, subject_user_id, updated_at
- config 中只保留非敏感摘要与 ref

#### Fallback（API Key only）

尝试：

- `GET /v1/dashboard/billing/subscription`
- `GET /v1/dashboard/billing/usage`

若成功：标记 `Source=newapi_billing_compat`，`Partial=true`，提示完整额度需 access_token。

### 5.5 换算逻辑（必须单点实现）

把 gateway `newapi_quota_display.go` 精简移植到 `quota_newapi.go`：

- 单一函数：`ConvertNewAPIQuota(raw float64, cfg DisplayConfig) float64`
- 单元测试覆盖：
  - default 500000 → 1_000_000 raw = 2.0 USD
  - CNY + exchange_rate
  - TOKENS 不除 scale
  - scale<=0 回退 raw
  - 与 gateway 测试样例对齐（避免双实现漂移）

**禁止**在 CLI 层、Web TS 层各自再写一套除法。

Web 展示直接消费后端已换算字段；若需要前端预览未保存凭证的探测结果，也必须调用 runtime-server API，而不是在浏览器重算。

---

## 6. 配置模型扩展

### 6.1 Provider schema（`agentconfig.Provider`）

新增字段（建议）：

```yaml
providers:
  items:
    my-sub2api:
      enabled: true
      protocol: openai
      base_url: https://example.com
      api_key: sk-***
      # --- site / account (new) ---
      site_type: sub2api                 # new-api | sub2api | unknown
      site_type_confidence: high
      site_type_detected_at: "2026-07-29T12:00:00Z"
      site_type_scores:
        sub2api: 12
        new-api: 0
      account_auth_ref: my-sub2api-account   # optional, auth store
      account:
        source: v1_usage
        mode: quota_limited
        currency: USD
        wallet_balance: null
        quota_balance: 12.34
        quota_remaining: 12.34
        quota_used: 7.66
        quota_limit: 20
        quota_display_type: USD
        quota_display_unit: USD
        quota_display_scale: null          # new-api only usually
        external_user_id: ""
        external_username_masked: ""
        subscriptions:
          - name: pro
            status: active
            remaining: 3.2
            period_end: "2026-08-01T00:00:00Z"
        usage:
          total_requests: 128
          total_cost: 7.66
        fetched_at: "2026-07-29T12:00:05Z"
        partial: false
        last_error: ""
```

实现注意：

- YAML 局部写回需扩展 `provider_persistence`，避免抹掉无关字段
- `account` 为**缓存快照**，不是计费权威源；每次 refresh 可覆盖
- 敏感 token 只进 auth store

### 6.2 Auth store 扩展

在现有 OAuth auth store 上增加 record kind：

| kind | 字段 |
| --- | --- |
| `codex_oauth`（已有） | access/refresh... |
| `newapi_system_access_token`（新） | access_token, subject_user_id |
| `sub2api_jwt`（可选） | access_token, refresh_token, expires_at |

`Provider.AccountAuthRef` 指向 record 名。

---

## 7. 产品流程

### 7.1 `aicli login` 主流程（改造点）

在现有 `runProviderLogin` 中，**models 校验成功之后、写配置之前**插入：

```text
1. resolve provider/base_url/api_key（现有）
2. validate models（现有，仍为登录硬条件）
3. [NEW] DetectSiteType(base_url)           # best-effort
4. [NEW] 若 interactive 且 new-api：
         可选 prompt access_token + user_id
   若 flags 提供则直接用
5. [NEW] FetchAccountSnapshot(site_type, creds)  # best-effort
6. 组装 provider 写回：原字段 + site_type + account 摘要
7. 输出：models 信息 + site_type + balance 一行摘要
```

CLI flags 建议：

| Flag | 含义 |
| --- | --- |
| `--skip-site-detect` | 跳过类型探测 |
| `--skip-account` | 跳过余额拉取 |
| `--site-type new-api\|sub2api\|auto` | 强制/自动 |
| `--newapi-access-token` | New-API system access token |
| `--newapi-user-id` | New-API subject user id |
| `--require-account` | 账户同步失败则登录失败（默认 false） |
| `--refresh-account-only` | 仅刷新账户（也可放到独立命令） |

输出示例（人类可读）：

```text
Provider: my-site (openai)
Models: 42 verified
Site type: sub2api (high)
Balance: $12.34 remaining (quota_limited, source=v1_usage)
```

JSON 输出需包含 `site_type` 与 `account` 对象，供脚本使用。

### 7.2 chat `/login` 与 `/balance`

- `/login`：复用同一 `runProviderLogin`，自动获得 site/account
- 新增 slash（建议）：
  - `/balance` 或 `/account`：对当前 provider 刷新并展示
  - `/account refresh [provider]`

### 7.3 独立命令（建议 P1）

```text
aicli account show [provider]
aicli account refresh [provider]
aicli site detect --base-url https://...
```

`doctor provider` 可增加非致命检查：

- site_type unknown 提示
- account.fetched_at 过旧
- new-api 有 site_type 但无 account_auth_ref

### 7.4 runtime Web

入口：Settings → Runtime Provider 编辑器

能力：

1. 显示 site type badge（new-api / sub2api / unknown）
2. “检测站点类型”按钮 → `POST /api/runtime/site-detect`
3. “同步账户余额”按钮 → `POST /api/runtime/providers/:name/account-sync`
4. 余额卡片：remaining / used / subscription / 更新时间 / partial warning
5. New-API 可选表单：access_token、user_id（token 写 auth store 或服务端内存配置通道，**响应中永远脱敏**）

runtime-server API 草图：

```text
POST /v1/siteaccount/detect
  body: { base_url }
  resp: DetectResult

POST /v1/siteaccount/fetch
  body: { base_url, site_type?, api_key?, system_access_token?, subject_user_id? }
  resp: { detect?, account: AccountSnapshot }

POST /v1/providers/{name}/account/refresh
  # 使用已保存 provider 配置 + auth store
```

前后端都只依赖 `siteaccount` 归一化 DTO，避免 TS 解析各站点原始 JSON。

---

## 8. 与 ai-gateway 的关系与复用策略

| 能力 | gateway | runtime 本方案 | 策略 |
| --- | --- | --- | --- |
| 站点探测 | 完整（含 public catalog 富化） | 轻量 endpoint 探测 | 移植候选与打分思想；不做 DB 投影 |
| Sub2API 全量同步 | JWT + keys + subs + stats | API Key `/v1/usage` 为主 | 契约对齐字段；增强路径可后补 |
| New-API 全量同步 | token 列表/订阅/checkin | self + status 摘要 | 换算逻辑必须一致 |
| 私有账户存储 | Postgres 实体 + events | provider YAML 缓存 | 本地缓存；未来可 bridge |
| 用户 portal | user-app 账户页 | runtime settings 卡片 | UX 独立 |

**明确不直接 import ai-gateway 包**（模块边界、依赖过重）。采用：

1. 复制精简算法 + 契约测试（换算、detect fixtures）
2. 在文档中标注 upstream 源文件与同步日期
3. 若长期双维护成本高，再评估抽 `ai-site-contracts` 独立模块（后续议题）

与 gateway 用户账户 API 的可选未来桥接：

- runtime 已登录 gateway 时，可调用 user AI site account full sync
- 本方案接口预留 `source=gateway_bridge`，但不纳入 MVP

---

## 9. 安全与隐私

1. **密钥分级存储**：API key 仍沿用现有 provider 配置策略；account token 进 auth store。
2. **日志脱敏**：禁止打印 access_token、api_key 全文；JSON 输出使用 masked。
3. **错误回显**：上游 body 截断，去掉 cookie/token。
4. **SSR/Web**：浏览器不直连用户第三方站点（CORS/混合内容/密钥泄露风险大）；一律经 runtime-server 服务端出站。
5. **跳过默认**：用户可永远不提供 New-API access_token，不影响基本 chat。
6. **最小权限说明**：文档需写明 New-API system access token 的权限含义与获取位置（用户控制台/系统令牌）。

---

## 10. 错误模型

统一错误码（示例）：

| Code | 含义 | 是否致命于 login |
| --- | --- | --- |
| `SITE_DETECT_TIMEOUT` | 探测超时 | 否 |
| `SITE_DETECT_AMBIGUOUS` | 分数冲突 | 否 |
| `ACCOUNT_UNSUPPORTED_SITE` | unknown 无法拉余额 | 否 |
| `ACCOUNT_AUTH_REQUIRED` | new-api 缺 token | 否（提示可选） |
| `ACCOUNT_UNAUTHORIZED` | 401/403 | 否（除非 require-account） |
| `ACCOUNT_UNEXPECTED_RESPONSE` | 解析失败 | 否 |
| `ACCOUNT_PARTIAL` | 部分字段成功 | 否 |

login 汇总策略：

- 默认：写 `account.last_error`，stdout warning，exit 0（若 models ok）
- `--require-account`：账户失败 → login 失败且不写配置（或写配置但不 set-default，需在实现时二选一并测清楚；**建议失败不落盘 account 字段但可落盘 site_type**）

---

## 11. 分阶段实施

### Phase 0 — 契约与共享内核（0.5–1d）

- 新建 `internal/siteaccount` 类型与 HTTP 工具
- 移植 New-API quota 换算 + 单测
- 用 sub2api/new-api 真实响应 fixture 固化解析测试
- 文档确认路径：`/v1/usage` 非 `/v1/usages`

### Phase 1 — 探测 + Sub2API 余额 + login 集成（MVP）

- `DetectSiteType`
- Sub2API `Fetch` via `/v1/usage`
- Provider schema + persistence 扩展
- `runProviderLogin` 接入（skip flags、JSON 字段）
- `aicli account show/refresh` 最小命令或先只在 login 输出
- 单测：detect scoring、usage 两种 mode、login dry-run

**验收**：

- 对 Sub2API base_url login 后 config 含 `site_type=sub2api` 与 `account.quota_remaining` 或 subscription
- 探测失败不阻断 login

### Phase 2 — New-API 可选账户同步

- 交互/flag 收集 access_token + user_id
- auth store kind
- `/api/status` + `/api/user/self`
- 换算后写入 account 摘要
- billing fallback（可选）

**验收**：

- 给定 token+id，额度展示与 gateway/new-api 控制台一致（允许四舍五入误差）
- token 不出现在 config.yaml 明文

### Phase 3 — runtime Web + server API

- runtime-server siteaccount endpoints
- provider editor：badge、余额卡、同步按钮、New-API 可选凭证表单
- 前端单测/组件测

### Phase 4 — 体验增强

- `/balance` slash、doctor 检查
- 过期自动 refresh 策略（打开 chat 时 stale > N 小时后台刷）
- Sub2API JWT 增强路径
- 与 ai-gateway bridge（可选）
- 余额不足提示（仅本地提示，不做通知系统）

---

## 12. 测试计划

### 12.1 单元测试

- `ConvertNewAPIQuota` 全分支
- Detect：fixture HTML/JSON/header → site_type/confidence
- Sub2API usage：quota_limited / unrestricted subscription / unrestricted wallet
- New-API self+status 解析与 partial
- provider persistence 局部写回保留 account 字段
- auth store 读写 newapi token

### 12.2 集成/命令测试

- `login --dry-run --json` 含 site/account 块
- `login --skip-account` 无 account 调用
- `login --site-type new-api` 无 token 时 warning
- 401 上游不导致非 0（默认）

### 12.3 手工验收矩阵

| 站点 | 凭证 | 期望 |
| --- | --- | --- |
| 纯 OpenAI | api key | site_type=unknown，无余额或跳过 |
| Sub2API | api key | detect=sub2api，`/v1/usage` 有 remaining/subscription |
| New-API | api key only | detect=new-api，账户 skipped 或 billing partial |
| New-API | api key + access_token + uid | quota 换算后余额 |
| 错误 base_url | any | detect warning，login 仍取决于 models |

---

## 13. 文档与 UX 文案

需更新：

- `docs/aicli/install.md` / `quickstart.md` / `faq.md`
- login help：说明自动探测与 New-API 可选项
- 明确路径名：`GET /v1/usage`

FAQ 条目建议：

1. 为什么 login 成功但没有余额？→ 站点 unknown / New-API 未提供 token / 上游 401
2. New-API access_token 在哪获取？→ 用户系统访问令牌 + 用户 ID（`New-Api-User`）
3. 余额与控制台不一致？→ 缓存未 refresh；New-API 展示类型/汇率；Sub2API key 限额 vs 钱包

---

## 14. 风险与缓解

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| 第三方站点定制/关接口 | 探测失败或 usage 404 | best-effort；允许手动 `--site-type`；partial |
| 双实现换算漂移 | 金额错误 | 单测对齐 gateway；注释引用源 |
| 用户把 system token 当 api key | 鉴权混乱 | 文案区分；字段分离；校验 self 成功再保存 |
| Web 直连第三方 | 密钥泄露/CORS | 强制 server-side fetch |
| login 变慢 | 体验差 | 并发探测、总超时、可 skip |
| config 膨胀 | 可读性 | account 只存摘要，不存原始大 JSON |

---

## 15. 推荐落地文件索引（实施时）

### Runtime / aicli

| 区域 | 路径 |
| --- | --- |
| 共享内核 | `backend/internal/siteaccount/*` |
| Provider schema | `backend/internal/agentconfig/config.go` |
| 写回 | `backend/internal/agentconfig/provider_persistence.go` |
| Auth store | `backend/internal/agentconfig/auth_store.go` |
| Login 接入 | `backend/cmd/aicli/commands/provider_login.go` |
| 命令 | `backend/cmd/aicli/commands/login.go`、`account_command.go`（新） |
| TUI | `backend/cmd/aicli/commands/chat_login_command.go`、slash catalog |
| runtime-server | `backend/internal/runtimeserver/siteaccount_*.go`（新） |
| Web | `frontend/src/components/workspace/settings/runtime-provider-*.tsx` |
| 本计划 | `docs/plan/site-type-account-balance-shared-capability-plan-20260729.md` |

### 行为对齐参考（只读）

| 区域 | 路径 |
| --- | --- |
| gateway detect | `ai-gateway/internal/service/aisiteresource/site_detect*.go` |
| gateway newapi sync | `ai-gateway/internal/service/aisiteuseraccount/newapi_account_sync.go` |
| gateway quota | `ai-gateway/internal/service/aisiteuseraccount/newapi_quota_display.go` |
| gateway sub2api sync | `ai-gateway/internal/service/aisiteuseraccount/account_full_sync.go` |
| sub2api usage | `sub2api/backend/internal/handler/gateway_handler.go` |
| new-api quota | `new-api/common/constants.go`, `controller/billing.go` |

---

## 16. 决策清单（评审时确认）

1. **MVP 是否只做 Sub2API `/v1/usage` + detect，New-API 放 Phase 2？**  
   建议：是（更快交付可见价值）。
2. **account 快照是否写入 config.yaml？**  
   建议：写入摘要；完整 raw 不写。
3. **New-API token 存储位置**  
   建议：auth store + `account_auth_ref`。
4. **Web 是否允许浏览器直连站点？**  
   建议：否，统一 runtime-server 出站。
5. **是否与 gateway 账户体系打通？**  
   建议：本计划不打通，仅契约对齐；单列后续 bridge 计划。
6. **路径命名**  
   确认对外文档使用 `/v1/usage`。

---

## 17. 成功标准（Definition of Done）

1. 共享包 `internal/siteaccount` 可在无 UI 依赖下完成 detect + fetch，并有单测。
2. `aicli login` 对 Sub2API 自动写入 `site_type` 与余额/订阅摘要。
3. New-API 在提供 access_token+user_id 时可展示换算后额度；不提供时可跳过。
4. CLI 与 Web（Phase 3 后）展示同一套 AccountSnapshot 字段语义。
5. 默认情况下，账户同步失败**不**破坏既有 login 成功率。
6. 文档说明站点类型、凭证层级、换算规则与刷新命令。

---

## 18. 附录 A — 接口对照速查

### Sub2API

| 用途 | Method | Path | Auth |
| --- | --- | --- | --- |
| Key/账户用量余额（推荐） | GET | `/v1/usage` | API Key |
| 当前用户资料 | GET | `/api/v1/auth/me` | JWT |
| 用量统计 | GET | `/api/v1/usage/stats` | JWT |
| 用量列表 | GET | `/api/v1/usage` | JWT |
| 订阅列表 | GET | `/api/v1/subscriptions` | JWT |
| 公共状态 | GET | `/api/v1/status` | Public |

### New-API

| 用途 | Method | Path | Auth |
| --- | --- | --- | --- |
| 站点状态/换算参数 | GET | `/api/status` | Public |
| 用户资料/额度 | GET | `/api/user/self` | Bearer system token + `New-Api-User` |
| 用户分组 | GET | `/api/user/self/groups` | 同上 |
| 用量统计 | GET | `/api/log/self/stat` | 同上 |
| OpenAI 兼容订阅 | GET | `/v1/dashboard/billing/subscription` | API Token |
| OpenAI 兼容用量 | GET | `/v1/dashboard/billing/usage` | API Token |

### 换算摘要

```text
New-API display amount = f(raw_quota, quota_per_unit, quota_display_type, exchange_rate)
默认：amount_usd = raw_quota / 500000
```

---

## 19. 附录 B — login 伪代码

```go
result, err := validateModels(...)
if err != nil { return err }

detect := siteaccount.Detect(ctx, baseURL)
siteType := detect.SiteType
if req.ForceSiteType != "" { siteType = req.ForceSiteType }

cred := siteaccount.AccountCredential{APIKey: apiKey}
if siteType == siteaccount.SiteTypeNewAPI {
    cred.SystemAccessToken, cred.SubjectUserID = resolveOptionalNewAPIAccountAuth(req)
}

var account *siteaccount.AccountSnapshot
if !req.SkipAccount {
    account, accErr = siteaccount.Fetch(ctx, siteType, baseURL, cred)
    // log warning on accErr unless req.RequireAccount
}

applyProviderSiteAccount(&candidate, detect, account)
// then existing config write + output
```

---

## 20. 总结

本方案把 ai-gateway 已验证的 **站点类型识别** 与 **账户余额/订阅同步** 能力，收敛为 ai-agent-runtime 内可复用的 `siteaccount` 模块：

- **Sub2API**：登录已有 API Key 即可走 `/v1/usage` 拿余额/订阅（最贴近用户“login 自动看余额”诉求）
- **New-API**：可选 access_token + user id，复用 gateway/new-api 同一套额度换算
- **共用**：CLI、TUI、Web 共用检测与拉取，避免三套解析
- **边界清晰**：先做本地 provider 缓存与体验，不绑 gateway DB；契约对齐便于未来 bridge

建议实施顺序：**Phase 0 内核 → Phase 1 Sub2API+detect+login → Phase 2 New-API token → Phase 3 Web**。
