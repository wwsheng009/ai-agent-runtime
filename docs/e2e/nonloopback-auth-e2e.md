# aicli 非回环鉴权 E2E 指南（`--web-host 0.0.0.0` + `X-AICLI-Token`）

> 场景 ID：**E2E-DEBUG-02**（非回环鉴权）
> Harness：`scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1`
> 首次固化验证：2026-09-23（Windows / pwsh 7），结果见 §7。
> 相关契约文档：[../aicli/web-remote-api.md](../aicli/web-remote-api.md)、
> [../aicli/debug-chat-status.md](../aicli/debug-chat-status.md)；
> 互补场景：[debug-guide.md](./debug-guide.md)（E2E-DEBUG-01）。

## 0. TL;DR

```powershell
# 一条命令：构建 → 非回环启动 → 清单自描述 → LAN 令牌矩阵 → 豁免红线 → /exit 收尾
pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1
# 退出码 0 = 无 FAIL（SKIP 不算失败）；1 = 有 FAIL（逐条列在 stdout 与 summary.json）

# 复跑（复用 E2E-DEBUG-01 的构建产物，省一次 go build）
pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1 -SkipBuild

# 一键回归（01 + 02 + 断言基线）：pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1
```

被测命令就是用户开放到局域网时的那条：

```powershell
aicli chat --yolo --web-host 0.0.0.0 --web-port 9999 --web-token <至少 16 位令牌>
```

## 1. 结论：非回环鉴权可以做 E2E 吗

**可以，但有一个环境前提**：需要一个**非回环的本机 IPv4**（局域网地址）作为请求源。
鉴权判定看 `RemoteAddr`，从 127.0.0.1 发起的请求在非回环模式下**仍然豁免**令牌
（这是实现契约，见 §3 的豁免红线）。因此脚本自动探测本机非回环 IPv4 并用它发请求；
探测不到时相关断言记为 **SKIP**（显式记录，绝不伪造通过）。

| # | 依据 | 说明 |
|---|------|------|
| 1 | 拒绝可机读 | 拒绝体是 JSON：`{"status":"forbidden","reason":"missing or invalid X-AICLI-Token (required on non-loopback)"}`，可直接断言 `reason` 含 `non-loopback` |
| 2 | 清单自描述 | `/debug/endpoints` 返回 `listen_mode=non-loopback` 与 `write_auth_hint`（提示 **ALL 请求**需令牌），且**不泄露令牌原文** |
| 3 | 令牌可复现 | `--web-token` 显式指定（≥16 位），启动行回显，脚本从 stderr 反解并断言与入参一致 |
| 4 | 双凭据等价 | `X-AICLI-Token` 头与 `?token=` 查询参数都能通过；无令牌/错误令牌一律 403 |
| 5 | 收尾确定 | 带令牌 `POST /web/api/input {"prompt":"/exit"}` → 优雅退出（退出码 0、端口释放） |
| 6 | 覆盖面同权 | `/web/`（index.html，不算静态资产）、`/web/api/*`、`/debug/*` 与 SSE 在非回环模式下**同权**：都要令牌；页面靠 `?token=` + `meta` 注入自举，`EventSource` 靠 `?token=` 拼接 |
| 7 | 豁免可复现 | 回环 IP 发起的请求读**与写**都免令牌；`/web/**` 静态资产（CSS/JS/favicon）免令牌 |

### 1.1 能力边界（本场景**不**覆盖）

- **不启用真实 TTY**：被测进程以 `Start-Process` 独立启动，stdout/stderr 重定向到证据目录
  （见 §2.1 拓扑与脚本启动段），因此 `term.IsTerminal(stdout)=false`
  （`backend/cmd/aicli/ui/terminal.go`）——进程走**非交互路径**，不 attach 交互式 TUI /
  统一渲染器，也没有键盘输入面；本场景的观测全部来自 HTTP 控制面。
- **不覆盖 TUI 渲染 / 键盘路径**：见 E2E-DEBUG-01 §8 的分工表。
- **不覆盖真实 turn 的 provider 行为**：本场景只发 `interrupt` 与 `/exit` 这类**无害控制动作**，
  避免为验鉴权引入 provider 依赖——因此本场景**不需要**配置 provider。
- **不覆盖 TLS / 反向代理 / 多用户**：`aicli` 的 Web 控制面是明文 HTTP + 单令牌模型。
- **不覆盖"只绑单个网卡 IP"（`--web-host 192.168.x.x`）**：`startPprofServer` 用
  `net.Listen` **精确绑定**给定 host，只绑网卡 IP 时回环不可达；而本场景的回环豁免断言
  与 `/exit` 收尾都走 `127.0.0.1`。harness 对此**显式拒绝**（`-ListenHost` 只接受
  通配地址 `0.0.0.0` / `::`），避免回环断言无谓变红；要覆盖该形态需另建场景。
- **不覆盖 `--web-token` 的强度校验边界**（长度/字符集拒绝逻辑），那属于参数校验单测范围。

## 2. 场景定义

### 2.1 拓扑

```
本机（测试机）
├── aicli 进程：Start-Process 独立进程（无 TTY；stdout/stderr 重定向到证据目录）
│     ├── 启动参数：chat --yolo --web-host 0.0.0.0 --web-port <p> --web-token <T>
│     ├── 监听 0.0.0.0:<p>（listen_mode = non-loopback）
│     └── stderr: Info: web write token (X-AICLI-Token): <T> (...)
├── 回环请求（RemoteAddr = 127.0.0.1）      → 免令牌（豁免 1）
└── 非回环请求（RemoteAddr = 本机 LAN IPv4） → 必须令牌
      ├── /web/api/*        → 无/错令牌 403；令牌正确 200
      └── /web/** 静态资产  → 免令牌（豁免 2；浏览器 <link>/<script> 带不了令牌）
```

### 2.2 约定

- **令牌**：缺省 `e2e-nonloopback-token-0123456789`（固定值便于断言启动行回显）；`-WebToken` 可覆盖。
- **端口**：缺省 `0` = 自动挑空闲端口；LAN 请求用 `http://<LAN IP>:<port>`，回环请求用 `http://127.0.0.1:<port>`。
- **LAN IP**：自动探测（`Dns.GetHostAddresses` + `Get-NetIPAddress`）；`-LanIp` 可指定。
- **非 2xx 不是异常**：403 是本场景的预期结果，探针必须拿到状态码与响应体。脚本内
  `Invoke-AuthProbe` 用 `System.Net.Http.HttpClient` 直发——**不要**用 `Invoke-WebRequest`
  断言错误码：PowerShell 7 在 403 上抛 `HttpResponseException` 且 `ErrorDetails.Message`
  为空，会把"403 + JSON 拒绝体"误判成"403 + 空体"（首版实测踩坑，见 §7 观察 1）。
  可能长时间流式的 URL（SSE）用 `-HeadersOnly` 只读响应头，避免护栏失效被放行时读超时。

### 2.3 判定标准

- 退出码 0 ⟺ 无 FAIL（SKIP 不导致失败，但会写入 `summary.json.skipped[]` 与 `run.log` 的 `[SKIP]` 行）。
- 任何一条 403/200 期望不成立即 FAIL；**不允许**把 403 当异常吞掉后继续（fail-closed）。

## 3. 步骤与断言

| 步骤 | 动作 | 断言（name） |
|------|------|--------------|
| S0 | `go build`（或 `-SkipBuild` 复用） | `build/go-build` |
| S1 | 启动并轮询 `/debug/endpoints` 就绪 | `startup/endpoints-ready` |
| S2 | 清单：监听模式与鉴权提示 | `discovery/listen-mode`、`discovery/write-auth-hint-all`、`discovery/web-base-url` |
| S3 | 令牌不外泄（反证 + 占位符正向锁定）+ 启动行回显 | `discovery/token-not-leaked`、`discovery/lan-url-placeholder`、`discovery/startup-line-token` |
| S4 | 从清单取路径（不硬编码；含 `/web/api/token`） | `discovery/paths-from-catalog` |
| S5 | LAN 令牌矩阵（11 条：读/写/页面/调试端点/SSE） | `auth/lan-get-no-token`、`auth/lan-get-token-header`、`auth/lan-get-token-query`、`auth/lan-get-wrong-token`、`auth/lan-page-no-token`、`auth/lan-page-token-meta`、`auth/lan-debug-no-token`、`auth/lan-debug-token`、`auth/lan-sse-no-token`、`auth/lan-post-no-token`、`auth/lan-post-token` |
| S5b | 令牌发现端点 `/web/api/token` 同权（3 条） | `auth/lan-token-no-token`、`auth/lan-token-echo`、`auth/loopback-token-exempt` |
| S6 | 豁免红线（3 条） | `auth/loopback-exempt`、`auth/loopback-write-exempt`、`auth/static-asset-exempt` |
| S7 | 收尾 | `exit/queued`、`exit/graceful`、`exit/port-released`（`-KeepAlive` 时为 `exit/keep-alive` FAIL） |

断言细节（要点）：

1. **S2**：`listen_mode` 必须恰为 `non-loopback`；`write_auth_hint` 必须含 `ALL 请求`；
   `web_base_url` 必须含实际端口。
2. **S3**：`/debug/endpoints`（JSON 与 `?format=text`）**都不得**出现令牌原文；
   非回环模式下 text 的 LAN 区块必须是 `…?format=text&token=<token>` 占位符
   （正向断言：只测"不含令牌"会放过"整段 LAN 区块被删"的退化实现）；
   启动行反解的令牌必须与 `-WebToken` 一致（日志里只打印长度，不打印原文）。
3. **S5**：`auth/lan-post-no-token` 用 `{"type":"interrupt"}`——即便鉴权失效也只触发无害动作，
   不会把 prompt 注入会话（fail-safe 设计）。`auth/lan-get-no-token` 额外断言 `reason` 含 `non-loopback`。
   `auth/lan-get-token-*` 断言"鉴权放行 + 正文非空"（`/web/api/screen` 缺省返回 TUI 合成帧，
   不是 JSON，不要按 JSON 字段判读）。
4. **S5 覆盖面**：`/web/`（index.html）无令牌 → 403、`?token=` → 200 且页面含
   `aicli-web-token` meta；`/debug/pprof/` 无令牌 → 403、带令牌 → 200；
   `/web/api/events`（SSE）无令牌 → 403（只读响应头判定）。
5. **S6 豁免 1**：回环请求在非回环模式下**仍然免令牌**，且**读与写都免**
   （`auth/loopback-exempt` / `auth/loopback-write-exempt` 期望 200）。
6. **S6 豁免 2**：`GET /web/style.css`（静态资产，非 `/web/api/*`）从 LAN 发起**免令牌** 200；
   该条只在 LAN 可用时判定（否则无法与回环豁免区分）→ 无 LAN 时 SKIP。
7. **S7**：`/exit` 走**回环 + 令牌**（两种模式下都成立，避免 LAN 不可达卡住收尾）；
   断言链：`status=queued` → 进程退出码 0 → 端口可再绑定。前面的 `interrupt` 探针会让
   会话短暂进入清理态，此时命令门返回 `{"status":"rejected","reason":"input rejected by command gate"}`
   （不是鉴权问题），因此 `/exit` 在 `-ExitTimeoutSec` 内有界重试，`exit.attempts` 写进证据。
8. **S5b**：`GET /web/api/token`（读本进程写令牌的端点）与其它 `/web/api/*` **同权**：
   非回环模式下从 LAN 无令牌 → 403（否则鉴权模型漏了一个"能读出令牌"的洞）；
   带令牌 → 200 且 `token` 与 `-WebToken` 一致、`source` 非空；
   从**回环**无令牌 → 200（豁免红线对该端点同样成立）。
   证据里只记录比对结论（`evidence.lanTokenEcho`：`status` / `header` / `query_param` / `source` /
   `token_match` 布尔值），**不写令牌原文**。

### 3.1 SKIP 语义

| 条件 | 受影响断言 | 记录位置 |
|------|-----------|----------|
| 无非回环 IPv4 且未指定 `-LanIp` | `auth/lan-*`（11 条） | `summary.json.skipped[]` + `run.log` 的 `[SKIP]` 行 |
| 同上 | `auth/static-asset-exempt` | 同上 |
| `listen_mode` 非 non-loopback 或服务端 LAN 列表为空 | `discovery/lan-url-placeholder` | 同上 |

SKIP 是**环境不具备**的显式记录，不是"通过"：总结行会打印 `SKIP=n`，明细逐条列出。

## 4. 手工复现（不跑脚本时的最小步骤）

```powershell
# 1) 取本机 LAN IP，然后非回环启动（stderr 重定向，便于核对启动行令牌）
$lan = (Get-NetIPAddress -AddressFamily IPv4 |
    Where-Object { $_.IPAddress -notlike '127.*' -and $_.PrefixOrigin -ne 'WellKnown' } |
    Select-Object -First 1).IPAddress
$tok = 'e2e-nonloopback-token-0123456789'
Start-Process -FilePath .\backend\.tmp\aicli-debug-e2e.exe `
    -ArgumentList 'chat','--yolo','--web-host','0.0.0.0','--web-port','9999','--web-token',$tok `
    -RedirectStandardError .\artifacts\aicli.stderr.log

# 2) 清单：模式 + 提示（注意 "?" 前用 ${} 防变量误解析）
Invoke-RestMethod "http://127.0.0.1:9999/debug/endpoints" |
    Select-Object listen_mode, write_auth_hint, web_base_url

# 3) 无令牌（从 LAN IP 发起）→ 403 forbidden
#    curl.exe -i 同时给出状态行与 JSON 体；不要用 Invoke-WebRequest（见下方提示）
curl.exe -i "http://${lan}:9999/web/api/screen"

# 4) 带令牌头 → 200；改用 ?token= → 200；错误令牌 → 403
Invoke-RestMethod "http://${lan}:9999/web/api/screen" -Headers @{ 'X-AICLI-Token' = $tok }
Invoke-RestMethod "http://${lan}:9999/web/api/screen?token=$tok"

# 5) 豁免 1：回环发起（无令牌）→ 200
Invoke-RestMethod "http://127.0.0.1:9999/web/api/screen"

# 6) 豁免 2：静态资产（无令牌，从 LAN）→ 200
(Invoke-WebRequest "http://${lan}:9999/web/style.css" -UseBasicParsing).StatusCode

# 6b) 页面同权：/web/（index.html 不算静态资产）无令牌 → 403；?token= → 200 且注入 meta
curl.exe -i "http://${lan}:9999/web/"
(curl.exe -s "http://${lan}:9999/web/?token=$tok") -match 'aicli-web-token'

# 6c) 调试端点同权：/debug/pprof/ 无令牌 → 403，带令牌 → 200
curl.exe -i "http://${lan}:9999/debug/pprof/"
curl.exe -s -o NUL -w '%{http_code}' -H "X-AICLI-Token: $tok" "http://${lan}:9999/debug/pprof/"

# 6d) SSE 只验拒绝：无令牌 → 403（--max-time 兜底，护栏失效时不会永久挂住）
curl.exe -i --max-time 3 "http://${lan}:9999/web/api/events"

# 7) 收尾（回环 + 令牌）：{"prompt":"/exit"} → status=queued，进程退出码 0
#    刚发过 interrupt 时首次可能被命令门拒绝（reason=input rejected by command gate），
#    隔 1~2 秒重试即可；harness 内有界重试并把 attempts 写进证据。
Invoke-RestMethod "http://127.0.0.1:9999/web/api/input" -Method POST `
    -ContentType 'application/json' -Body '{"prompt":"/exit"}' `
    -Headers @{ 'X-AICLI-Token' = $tok }
```

> **手工看 403 体用 `curl.exe -i`**；harness 内部用 `System.Net.Http.HttpClient` 直发并按
> `StatusCode` 判定，**不用** `Invoke-WebRequest`——pwsh 7 在 403 上抛 `HttpResponseException`
> 且 `ErrorDetails.Message` 为空，会把"403 + JSON 体"误判成"403 + 空体"。SSE 这类可能长流的
> URL 在 harness 里走 `-HeadersOnly`（只读响应头）。

## 5. Harness 用法

### 5.1 参数

| 参数 | 缺省 | 说明 |
|------|------|------|
| `-ExePath` | `<repo>/backend/.tmp/aicli-debug-e2e.exe` | 被测二进制（与 E2E-DEBUG-01 共用） |
| `-Port` | `0`（自动） | 监听端口 |
| `-ListenHost` | `0.0.0.0` | 监听地址（非回环模式）；**只接受通配地址**（`0.0.0.0` / `::`），绑单个网卡 IP 会被显式拒绝（回环不可达，见 §1.1） |
| `-WebToken` | `e2e-nonloopback-token-0123456789` | 显式写令牌（≥16 位，字符集 `A-Za-z0-9-._~`） |
| `-LanIp` | 自动探测 | 用于非回环请求的本机 IP |
| `-StartupTimeoutSec` | `90` | 等待 `/debug/endpoints` 就绪 |
| `-ExitTimeoutSec` | `60` | 等待 `/exit` 后进程退出 |
| `-SkipBuild` | 关 | 跳过 `go build`，复用 `-ExePath` |
| `-KeepAlive` | 关 | 人工排查：不收尾，脚本判 FAIL（避免误当通过） |
| `-ArtifactDir` | `artifacts/aicli-debug-auth-e2e/<yyyyMMdd-HHmmss>` | 证据目录 |

### 5.2 常用组合

```powershell
# 用户视角复现：固定 9999 + 指定 LAN IP + 复用构建
pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1 -SkipBuild -Port 9999 -LanIp 192.168.1.10

# CI：自动端口 + 指定证据目录
pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1 -ArtifactDir artifacts/ci-auth-e2e

# 先跑 01（含构建），再复用同一二进制跑 02
pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e.ps1
pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1 -SkipBuild
```

### 5.3 证据目录

| 文件 | 内容 |
|------|------|
| `run.log` | 逐条 `[PASS]` / `[FAIL]` / `[SKIP]` + 总结行 + FAIL/SKIP 明细 |
| `aicli.stdout.log` / `aicli.stderr.log` | 被测进程输出（stderr 含启动行令牌回显） |
| `summary.json` | `scenario` / `port` / `lan_ip` / `listen_host` / `pass` / `fail` / `skip` / `results[]` / `skipped[]` / `evidence` |
| `build.log` | `go build` 输出（`-SkipBuild` 时无） |

`evidence` 当前保留：`endpoints`（`/debug/endpoints` 原文快照）、`lanGetNoToken`（403 响应体原文）、
`lanUrlPlaceholder`（文本清单 LAN 区块的占位符行）、`pageTokenMeta`（页面 meta 注入片段）、
`lanTokenEcho`（`/web/api/token` 回显比对结论：`status` / `header` / `query_param` / `source` /
`token_match`，**不含令牌原文**）、`exit`（`attempts` 与最终响应体）。

### 5.4 一键回归（聚合入口 + 断言基线）

```powershell
# 基线门禁 → 01 → 02（02 恒 -SkipBuild 复用 01 的二进制）→ 聚合结论
pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1
```

- 入口 `scripts/test-aicli-e2e-all.ps1`，断言基线 `scripts/e2e-assertion-baseline.json`：
  **删除/改名断言 = FAIL**（防"悄悄删断言换绿"），新增 = 提示（`-UpdateBaseline` 固化）；
  `-BaselineOnly` 是秒级门禁（不跑 E2E；无 provider / 无非回环 IPv4 的 CI 上跑这条）。
- 聚合脚本统一归一化两个 harness 的 summary 字段名（01 = `passed`/`failed`，02 = `pass`/`fail`/`skip`），
  并把 `results` 条数与 `PASS+FAIL` 交叉校验；schema 漂移或计数不符 → 直接 FAIL。
- 聚合逻辑自身的负例自测：`scripts/test-aicli-e2e-all-selftest.ps1`（桩 harness 在 `%TEMP%` 沙箱内跑，
  不碰端口 / provider / 终端）。
- 参数、退出码与证据目录明细见 [debug-guide.md](./debug-guide.md) §5.1。

## 6. 失败模式与排查

| 现象 | 可能原因 | 排查 |
|------|----------|------|
| `auth/lan-get-no-token` 返回 200 | 请求实际走了回环（`-LanIp` 误填 `127.*`），或鉴权未生效 | 确认 `-LanIp` 是本机非回环地址；核对 `listen_mode=non-loopback` |
| 全部 `auth/lan-*` 为 SKIP | 本机无非回环 IPv4（仅回环网卡 / 受限容器网络） | 用 `-LanIp` 指定；或换有网卡的机器跑 |
| `discovery/startup-line-token` FAIL | 启动行格式变了，或 `--web-token` 未生效 | 看 `aicli.stderr.log` 的 `Info: web write token` 行；核对令牌字符集（§2.2） |
| `discovery/token-not-leaked` FAIL | 清单/文本目录里出现令牌原文（**安全问题**） | 修 `backend/cmd/aicli/commands/chat_debug_endpoints.go` 的输出，**不要**改断言 |
| `auth/static-asset-exempt` FAIL | 静态豁免规则被收紧（`/web/**` 也要求令牌） | 对照 `web_auth.go` 的豁免顺序：静态资产 → 回环 → 令牌 |
| `exit/graceful` FAIL | `/exit` 未送达（令牌错）或进程卡住 | 看 `aicli.stdout.log` 尾部；收尾刻意走回环，正常不受 LAN 影响 |
| `auth/lan-get-no-token` "403 但无拒绝体" | 探针用了 `Invoke-WebRequest`（pwsh 7 丢 403 体） | 改回 `HttpClient`/`curl.exe -i`；拒绝体应含 `reason=missing or invalid X-AICLI-Token (required on non-loopback)` |
| `discovery/lan-url-placeholder` FAIL | 文本清单的 LAN 区块缺失，或未用 `token=<token>` 占位符 | 看 `chat_debug_endpoints.go` 的 text 渲染；**不能**为了让它 PASS 而回显真实令牌 |
| `auth/lan-sse-no-token` 探针超时 | 用普通探针读了可能长流的 SSE 响应 | 该条必须用 `-HeadersOnly`；放行才超时说明护栏失效（此时应修实现，而不是加长超时） |
| `exit/queued` 首答 `rejected` | 前一步 `interrupt` 后会话在清理态，命令门短暂拒绝 | 正常现象：看 `evidence.exit.attempts`；持续 rejected 才排查 |
| 端口占用 | 上一次运行残留进程 | `Get-NetTCPConnection -LocalPort <p>`；脚本 `finally` 会兜底杀进程 |

## 7. 实测证据（2026-09-24）

> 环境：Windows + pwsh 7，本机 LAN IP `172.30.48.1`，端口自动分配（实测 53068）。
> 命令：`pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1 -SkipBuild`
> （二进制复用 E2E-DEBUG-01 构建产物）。证据目录：`artifacts/aicli-debug-auth-e2e/20260924-001954/`
> （含 `summary.json.evidence`：`endpoints` / `lanUrlPlaceholder` / `lanGetNoToken` / `pageTokenMeta` / `exit`；
> 其中 `pageTokenMeta.snippet` 已把真实令牌替换为 `<token>`，证据文件本身不含令牌原文）。

| 项 | 结果 |
|----|------|
| 汇总 | **PASS=25 / FAIL=0 / SKIP=0**，退出码 0 |
| LAN 无令牌 GET `/web/api/screen` | 403，体 `{"status":"forbidden","reason":"missing or invalid X-AICLI-Token (required on non-loopback)"}` |
| LAN 令牌头 / `?token=` | 均 200，正文 9,975 字节 |
| LAN 错误令牌 | 403 |
| LAN `/web/` 无令牌 / `?token=` | 403 / 200（40,696 字节，含 `aicli-web-token` meta） |
| LAN `/debug/pprof/` 无令牌 / 带令牌 | 403 / 200（2,599 字节） |
| LAN `/web/api/events`（SSE）无令牌 | 403（只读响应头判定） |
| LAN 无令牌 POST / 带令牌 POST | 403 / 200 `{"status":"interrupted"}` |
| 回环无令牌 GET / POST | 均 200（GET 9,975 字节）——两条豁免红线成立 |
| LAN `/web/style.css` | 200（70,472 字节）——静态资产豁免成立 |
| 收尾 | 第 1 次 `/exit` 200 `{"status":"rejected","reason":"input rejected by command gate"}`，第 2 次 `{"status":"queued"}`（`evidence.exit.attempts=2`）；退出码 0、端口释放 |

### 7.1 观察（本轮踩坑与结论）

1. **403 的响应体在 pwsh 7 会丢**：`Invoke-WebRequest` 抛异常且 `ErrorDetails.Message` 为空，
   首版探针因此把"403 + 正确拒绝体"判成 FAIL。已改为 `HttpClient` 直发（§2.2）。
2. **`?token=` 会被 `$` 变量名吞掉**：`"...?token=$tok"` 里 `?` 是合法变量名字符，必须写 `${}` 或
   先拼好字符串；harness 已统一用 `"$lanBase${path}?token=$WebToken"` 形式。
3. **只测"不含令牌"不足以防泄露**：若实现把整段 LAN 区块删掉，反证断言照样通过；故补
   `discovery/lan-url-placeholder` 正向锁定 `token=<token>` 占位符（占位符 ≠ 真令牌）。
4. **`/web/api/screen` 不是 JSON**：缺省返回 TUI 合成帧纯文本，断言只能判"200 + 正文非空"。
5. **`interrupt` 会短暂关闭命令门**：收尾的 `/exit` 需有界重试（`exit.attempts` 留痕），
   否则会把正常清理态误判成失败。

### 7.2 复验（2026-09-24，+3 条 `/web/api/token` 断言）

> 命令：`pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1 -SkipBuild`
> （复用 E2E-DEBUG-01 的二进制）。证据目录：`artifacts/aicli-debug-auth-e2e/20260924-002917/`（首跑）
> 与 `artifacts/verify-02-tokenecho/`（补 `evidence.lanTokenEcho` 后复跑，结论一致）。

| 项 | 结果 |
|----|------|
| 汇总 | **PASS=28 / FAIL=0 / SKIP=0**，退出码 0（§7 的 25 条 + §3 S5b 的 3 条） |
| LAN `/web/api/token` 无令牌 | 403（拒绝体与其它 `/web/api/*` 同形） |
| LAN `/web/api/token` 带令牌 | 200，`token` 与 `-WebToken` 一致、`header`/`query_param`/`source` 符合契约 |
| 回环 `/web/api/token` 无令牌 | 200（豁免红线成立） |
| 一键回归（聚合） | `pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1 -SkipBuild` → 基线 28 / 31 条一致、01 PASS=29（`-SkipBuild` 时无 `build/go-build` 条）、02 PASS=28，聚合 **PASS=4 / FAIL=0**、退出码 0；证据目录 `artifacts/aicli-e2e-all/20260924-004003/` |

## 8. 与 E2E-DEBUG-01 的分工

| 维度 | E2E-DEBUG-01 | E2E-DEBUG-02（本文） |
|------|--------------|----------------------|
| 监听模式 | `listen_mode=loopback`（缺省） | `listen_mode=non-loopback`（`--web-host 0.0.0.0`） |
| 鉴权重点 | 写操作才校验写令牌（GET 免令牌）+ Host/Origin 校验 | **所有** `/web/api/*`（含 GET/SSE）都要令牌；两条豁免红线 |
| 依赖 provider | 是（第 4 步真实 invoke） | 否（只发 `interrupt` / `/exit`） |
| 关键断言 | invoke 终态 `turn_id` 回填、幂等回放、turn 后验、读屏 | LAN 令牌矩阵（11 条：读/写/页面/调试端点/SSE）、令牌不外泄 + 占位符正向锁定、启动行回显、豁免红线（回环读/写 + 静态资产） |
| 回答的问题 | "HTTP 控制面能不能可靠驱动/验收一次真实 turn" | "开放到局域网时鉴权边界对不对" |

> 两者共用同一构建产物 `backend/.tmp/aicli-debug-e2e.exe`：先跑 01（含 build），再跑 02 `-SkipBuild`；
> 02 也可单独跑（不带 `-SkipBuild` 时自己构建）。
>
> 覆盖面是**互补**的：01 负责"控制面能可靠驱动一次真实 turn"（需要 provider），
> 02 负责"开放到局域网时鉴权边界正确"（不需要 provider）。两条链都过了，才谈得上
> Web 控制面端到端可用。
>
> 两条链的**一键回归**（断言基线门禁 + 顺序执行 + 聚合结论）由 `scripts/test-aicli-e2e-all.ps1` 承担，见 §5.4。

## 9. 相关文档

- [../aicli/web-remote-api.md](../aicli/web-remote-api.md) — `/web/api/*` 契约与鉴权模型（§1 鉴权）。
- [../aicli/debug-chat-status.md](../aicli/debug-chat-status.md) — `/debug/endpoints` 字段（`listen_mode` / `write_auth_hint`）。
- [debug-guide.md](./debug-guide.md) — E2E-DEBUG-01（回环 HTTP 控制面）指南。
- `scripts/test-aicli-e2e-all.ps1` — 一键回归聚合入口（断言基线 + 01 + 02 + 聚合结论，本文 §5.4）。
- `scripts/e2e-assertion-baseline.json` — 断言基线（"断言只增不减"的机器化检查）。
- `scripts/test-aicli-e2e-all-selftest.ps1` — 聚合脚本自测（桩 harness、负例驱动）。
- `scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1` — 本场景 harness（本文 §5 参数说明）。
