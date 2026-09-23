# aicli 调试端点 E2E 指南（独立进程 + `/debug/endpoints` + 屏幕回读 / invoke）

> 场景 ID：**E2E-DEBUG-01**（HTTP 控制面）
> Harness：`scripts/test-aicli-debug-endpoints-e2e.ps1`
> 首次固化验证：2026-09-23（Windows / pwsh 7，真实 provider），结果见 §7。
> 相关契约文档：[../aicli/web-remote-api.md](../aicli/web-remote-api.md)、
> [../aicli/debug-chat-status.md](../aicli/debug-chat-status.md)、
> [../user-guide/aicli-tui-remote.md](../user-guide/aicli-tui-remote.md)。

## 0. TL;DR

```powershell
# 一条命令：构建 → 独立进程启动 → 发现 → 读屏 → invoke → 幂等回放 → turn 后验 → /exit 收尾
pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e.ps1
# 退出码 0 = 全部断言通过；1 = 有 FAIL（逐条列在 stdout 与 summary.json）

# 非回环鉴权（--web-host 0.0.0.0 + --web-token）：LAN 令牌必需 / 豁免红线 / 清单不泄露令牌
pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1
```

被测命令就是用户视角的那条（根命令自动分发到默认 `chat` 子命令，两种写法等价）：

```powershell
aicli --yolo --web-port 9999          # 等价 aicli chat --yolo --web-port 9999
```

## 1. 结论：这条链路可以做 E2E 吗

**可以。** 依据（均已实机验证，见 §7）：

| # | 依据 | 说明 |
|---|------|------|
| 1 | 端点先于 TUI 启动 | loopback HTTP 服务器在 `rootCmd.PersistentPreRunE` 阶段拉起，**早于** TUI；因此后台进程 / 无 TTY 进程同样暴露 `/debug/*` 与 `/web/*` |
| 2 | 入口自描述 | `GET /debug/endpoints` 返回 `available` / `listen_mode` / `web_base_url` / `loopback_base_url` / `write_auth_header` + `endpoints[]`（`method` / `path` / `scheme` / `enabled` / `url` / `note`），脚本**只依赖该清单**即可发现全部入口 |
| 3 | 判定可机读 | `POST /web/api/invoke` 一次响应返回 `status` / `assistant` / `usage` / `screen` / `turn_id`（终态 `turn_id` 由生命周期事件回填，见 §7.2）；`GET /web/api/turn` 提供 turn 后验记录（`turn_id` / `status` / `duration_ms` / `steps` / `assistant_preview`） |
| 4 | 幂等可断言 | `client_request_id` 是确定性幂等键：重复提交回放首次结果（`duplicate=true`），可直接断言"没有第二次注入" |
| 5 | 收尾确定 | `POST /web/api/input {"prompt":"/exit"}` → 进程优雅退出（退出码 0、端口释放），**无需**窗口/键盘自动化或 `Stop-Process` |

### 1.1 能力边界（本场景**不**覆盖）

- **不启用真实 TTY**：被测进程以 `Start-Process` 独立启动且 stdout/stderr 重定向到证据目录
  （见 §2.1 拓扑），`term.IsTerminal(stdout)=false`（`backend/cmd/aicli/ui/terminal.go`）
  → 走**非交互路径**（顺序行模式），不 attach 交互式 TUI / 统一渲染器，也没有键盘输入面。
- **不覆盖真实终端渲染与键盘输入路径**：那是 `scripts/test-aicli-windows-terminal-e2e.ps1`
  与 `scripts/test-aicli-opencode-windows-terminal-e2e.ps1`（Windows Terminal + UI Automation）的职责。
  本场景只走 HTTP 控制面（读屏 = 读合成帧，不是读物理终端）。
- **非回环鉴权由姊妹场景覆盖**：本场景跑在回环默认态（`listen_mode=loopback`，写操作在
  开发模式下跳过令牌）。`--web-host 0.0.0.0` 时所有请求（含 GET/SSE）都需令牌，
  由 **E2E-DEBUG-02**（`scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1`，
  见 §7.3/§8）覆盖：LAN 令牌必需、回环与静态资产豁免、页面/SSE/`/debug/*` 同权、
  清单不泄露令牌。
- **依赖真实 provider/model**：第 4 步 invoke 需要本机已配置可用 provider；无可用 provider
  时该步如实失败（不会伪造通过）。无人值守建议加 `-Headless`（跳过交互选择器，
  无可用 provider 时直接报错退出，TUI 与调试端点照常）。
- **不做并发压测**：`/web/api/invoke` 是单飞锁，并发调用返回 `409 {"status":"busy"}`；
  并发行为属于契约文档范围，不在本场景。

## 2. 场景定义

### 2.1 拓扑

```text
    ┌──────────────────────────── pwsh (harness) ────────────────────────────┐
    │ 1. go build → backend/.tmp/aicli-debug-e2e.exe                          │
    │ 2. Start-Process（独立进程，无 TTY，stdout/stderr 重定向到证据目录）      │
    │ 3. GET  /debug/endpoints            → 入口清单（URL 全部取自这里）        │
    │ 4. GET  /debug/chat/screen          → 合成帧（读屏）                     │
    │ 5. GET  /web/api/screen?view=tui&format=json → TUI 合成帧（读屏）        │
    │ 6. POST /web/api/invoke             → 注入 prompt，等 turn 结束           │
    │ 7. POST /web/api/invoke（同 id）    → 幂等回放（duplicate=true）          │
    │ 8. GET  /web/api/turn?id=<turn_id>  → turn 后验                          │
    │ 9. POST /web/api/input {"prompt":"/exit"} → 优雅退出（exit code 0）      │
    └─────────────────────────────────────────────────────────────────────────┘
                    │ 独立进程
                    ▼
    aicli chat --yolo --web-port <port>   （loopback 127.0.0.1，开发模式：回环跳过写令牌）
```

### 2.2 约定

| 项 | 取值 | 备注 |
|----|------|------|
| 监听地址 | `127.0.0.1` | 仅回环；不要转发/暴露到网络 |
| 端口 | 缺省自动挑空闲端口（`-Port 0`） | 复现用户场景时传 `-Port 9999`；CI 建议自动挑，避免与本机实例冲突 |
| 鉴权 | `listen_mode=loopback` + 开发模式 | 回环写操作跳过 `X-AICLI-Token`；令牌原文**不出现在**清单里（JSON 与 text 双查） |
| 权限模式 | `--yolo` = `--permission-mode bypass_permissions` | 避免工具审批阻塞无人值守流程 |
| 退出 | `/web/api/input {"prompt":"/exit"}` | 断言进程退出码 0 与端口释放 |
| 证据 | `artifacts/aicli-debug-endpoints-e2e/<stamp>/` | `run.log` / `aicli.stdout.log` / `aicli.stderr.log` / `summary.json` / `go-build.log` |

### 2.3 判定标准

- **PASS**：§3 全部断言通过（脚本退出码 0）。
- **FAIL**：任一条不成立（脚本退出码 1），失败明细同时写入 `summary.json.results[]`。
- 脚本**不做**"降级通过"：取不到令牌原文就无法完成"清单不泄露令牌"的反证时，该条判 FAIL（fail-closed）。

## 3. 步骤与断言

| 步 | 动作 | 断言（机器可判） | 失败典型原因 |
|----|------|------------------|--------------|
| S0 | `go build -trimpath -ldflags "-X main.version=e2e"` → `backend/.tmp/aicli-debug-e2e.exe` | 构建退出码 0 | 源码编译失败 |
| S1 | `Start-Process aicli chat --yolo --web-port <port>`（独立进程、stdout/stderr 落证据目录） | 进程存活；`/debug/endpoints` 在 `-StartupTimeoutSec`（缺省 90s）内 `available=true` | 端口被占用、启动即崩（stderr 尾部会打进 `run.log`） |
| S2 | `GET /debug/endpoints` + `?format=text` | `listen_mode=loopback`；`web_base_url=http://127.0.0.1:<port>/web`；`write_auth_header=X-AICLI-Token`；text 含「Debug 使用说明」与 invoke 驱动行 | 旧构建（字段缺失）→ 先重建二进制 |
| S3 | 清单 `endpoints[]` 过滤 | `GET /debug/chat/screen`、`GET /web/api/screen`、`POST /web/api/invoke`、`GET /web/api/turn`、`POST /web/api/input` 均 `enabled=true` 且 `url` 非空（后续 URL 只从清单取） | 清单与实现脱节（新增端点未登记） |
| S4 | 令牌反证 | `GET /web/api/token`（或启动行兜底）取到令牌原文，且 **JSON 与 `?format=text` 都不包含它** | 清单泄露令牌（回归红线） |
| S5 | 读屏 | `GET /debug/chat/screen` → `available=true`；`GET /web/api/screen?view=tui&format=json` → `available=true` 且 `text`/`lines` 非空 | 渲染未安装 / 会话未就绪 |
| S6 | `POST /web/api/invoke`（带 `client_request_id`） | `status=completed`；`assistant.content` 非空；`llm_observed=true`、`busy=false`、`pending_inputs=0`；`usage.total_tokens>0`；`screen.available=true` | 无可用 provider、模型报错、超时（`-InvokeTimeoutMs`） |
| S6b | `GET /web/api/turn` 定位本轮记录 | `recent` 中存在 `status=completed` 的记录，且其 `assistant_preview` 与本轮回复一致 | turn 记录未落 / 记录器未安装 |
| S6c | invoke 响应自带 `turn_id` | `invoke.turn_id` 非空且等于 S6b 记录的 `turn_id`（终态回填，见 §7.2） | 回填回归（字段再次缺失/不一致） |
| S7 | 回读屏幕 | `?view=tui&format=json` 的合成帧文本包含回复首个非空行前 40 字符（needle） | 渲染未提交该轮（渲染回归） |
| S8 | 幂等回放 | 同 `client_request_id` 重发 → `duplicate=true`、`assistant.content` 与 `elapsed_ms` 与首次一致；`/web/api/turn` 的 `recent` 条数不增加 | 幂等键被忽略（会重复注入，回归红线） |
| S9 | turn 后验 | `GET /web/api/turn?id=<S6b 记录的 turn_id>` → `found=true`、`status=completed`、`duration_ms>0`、`steps>=1`、`assistant_preview` 非空；`current.busy=false`、`pending_inputs=0` | turn 记录未落 / 记录器未安装 |
| S10 | 收尾 | `POST /web/api/input {"prompt":"/exit"}` → `status=queued`；进程在 `-ExitTimeoutSec`（缺省 60s）内退出且 `exit_code=0`；端口可重新绑定 | 退出路径被阻塞（有在跑任务/等待输入） |

> `wait_only` 与审批/提问分支（`requires_approval` / `requires_answer`）不在本场景断言范围，
> 它们需要构造审批/提问态；契约与用法见 [web-remote-api.md §5](../aicli/web-remote-api.md)。

## 4. 手工复现（不跑脚本时的最小步骤）

```powershell
$repoRoot = 'E:\projects\ai\ai-agent-runtime'
$exe = Join-Path $repoRoot 'backend\.tmp\aicli-debug-e2e.exe'   # 先 go build 产出，或复用已有构建

# 1) 独立进程启动（后台运行；stdout/stderr 建议重定向，避免污染当前控制台）
Start-Process -FilePath $exe -ArgumentList 'chat','--yolo','--web-port','9999' -WorkingDirectory $repoRoot -PassThru

# 2) 发现入口（只认这一个 URL）
Invoke-RestMethod 'http://127.0.0.1:9999/debug/endpoints' | ConvertTo-Json -Depth 4
Invoke-RestMethod 'http://127.0.0.1:9999/debug/endpoints?format=text'

# 3) 读屏（?view=tui 缺省是纯文本帧；要结构化快照再加 &format=json）
Invoke-RestMethod 'http://127.0.0.1:9999/web/api/screen?view=tui&tail=40'
Invoke-RestMethod 'http://127.0.0.1:9999/web/api/screen?view=tui&format=json'

# 4) 同步调用（POST 用 UTF-8 字节，避免 PowerShell 代码页把中文解码坏）
$body = '{"prompt":"只回复两个字：收到","timeout_ms":120000,"client_request_id":"manual-1"}'
$r = Invoke-WebRequest -Uri 'http://127.0.0.1:9999/web/api/invoke' -Method POST `
     -Body ([System.Text.Encoding]::UTF8.GetBytes($body)) `
     -ContentType 'application/json; charset=utf-8' -UseBasicParsing
([System.Text.Encoding]::UTF8.GetString($r.RawContentStream.ToArray()) | ConvertFrom-Json) |
    Select-Object status, elapsed_ms, turn_id, llm_observed, busy, duplicate, usage

# 5) 幂等回放：重发同一 $body → duplicate=true，elapsed_ms 与首次一致
# 6) turn 后验
Invoke-RestMethod 'http://127.0.0.1:9999/web/api/turn' | ConvertTo-Json -Depth 5

# 7) 收尾（优雅退出）
Invoke-RestMethod 'http://127.0.0.1:9999/web/api/input' -Method POST `
    -Body ([System.Text.Encoding]::UTF8.GetBytes('{"prompt":"/exit"}')) `
    -ContentType 'application/json; charset=utf-8'
```

> 回环默认态下写操作不需要令牌；若改到非回环（`--web-host 0.0.0.0`）或显式
> `--web-dev=false`，请按 [web-remote-api.md §1 鉴权](../aicli/web-remote-api.md) 取
> `X-AICLI-Token` 后附在请求头。

## 5. Harness 用法

```powershell
pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e.ps1 [参数]
```

| 参数 | 缺省 | 说明 |
|------|------|------|
| `-ExePath` | `<repo>/backend/.tmp/aicli-debug-e2e.exe` | 被测二进制；相对路径按 repo 根解析 |
| `-Port` | `0`（自动挑空闲端口） | 复现用户场景用 `-Port 9999`；CI 建议留 0 |
| `-Prompt` | `只回复两个字：收到` | invoke 注入的 prompt |
| `-InvokeTimeoutMs` | `120000` | invoke 等待 turn 结束的超时（毫秒） |
| `-StartupTimeoutSec` | `90` | 等待 `/debug/endpoints` 就绪 |
| `-ExitTimeoutSec` | `60` | 等待 `/exit` 生效 |
| `-SkipBuild` | 关 | 跳过 `go build`（复用 `-ExePath`） |
| `-Headless` | 关 | 启动参数追加 `--headless`（无人值守；跳过交互选择器） |
| `-KeepAlive` | 关 | 人工排查：不执行 `/exit`，脚本收尾强制结束并**判 FAIL**（避免误当通过） |
| `-ArtifactDir` | `artifacts/aicli-debug-endpoints-e2e/<yyyyMMdd-HHmmss>` | 证据目录 |

退出码：`0` = 全部断言通过；`1` = 存在 FAIL（明细见 stdout、`run.log`、`summary.json`）。

证据目录结构：

```text
artifacts/aicli-debug-endpoints-e2e/<stamp>/
├── run.log              # 逐步 PASS/FAIL 时间线（含最终总结）
├── summary.json         # results[] + evidence.invoke / evidence.replay / evidence.turns
├── aicli.stdout.log     # 被测进程 stdout（启动行、会话行）
├── aicli.stderr.log     # 被测进程 stderr（端点行、web write token 行、Warning/Error）
└── go-build.log         # go build 输出（-SkipBuild 时不存在）
```

```powershell
# 用户视角复现：固定 9999，复用已有构建
pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e.ps1 -Port 9999 -SkipBuild

# CI：自动端口 + headless + 指定证据目录
pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e.ps1 -Headless -ArtifactDir artifacts/ci-debug-e2e
```

## 6. 失败模式与排查

| 现象 | 判读 | 处置 |
|------|------|------|
| S1 超时未就绪（`startup/endpoints-ready` FAIL） | 进程启动即崩或端口被占用；`run.log` 会附 stderr 尾部 | 换端口（`-Port 0`）；查看 `aicli.stderr.log` |
| S6 `status=requires_approval` / `requires_answer` | 会话停在审批/提问等待（默认 `--yolo` 不应出现） | 按 [web-remote-api.md §5](../aicli/web-remote-api.md) 先决议（`POST /web/api/input {"type":"approval",...}` / `{"type":"question_answer",...}`），再用 `{"wait_only":true}` 续等 |
| S6 HTTP `409 {"status":"busy"}` | 已有另一个 invoke 在等同一会话（单飞锁） | 等其结束后重试；**不要**换新 `client_request_id` 重发 prompt |
| S6 超时（`timeout_ms` 用尽） | turn 未结束（长任务/后台作业） | 加大 `-InvokeTimeoutMs`；用 `/web/api/turn`、`/web/api/screen`、`/web/api/events` 继续观察；**不要**重发 prompt |
| S4 令牌反证 FAIL | `/debug/endpoints` 泄露了写令牌原文（回归红线） | 检查 `chatDebugEndpointsSnapshot.WriteAuthToken` 是否仍为 `json:"-"`，以及 `?format=text` 渲染路径 |
| S3/S4 清单 text 出现 `token=<真实令牌>` | 文本清单的 LAN 区块泄露令牌（回归红线） | 检查 `BuildChatDebugEndpointsText` 是否仍用 `<token>` 占位符；单测 `TestChatDebugEndpointsTextNonLoopbackDoesNotLeakToken` |
| 403 但"响应体为空"（探针读不到 JSON 拒绝体） | 探针把非 2xx 当异常且取不到 body：PowerShell 7 的 `Invoke-WebRequest` 在 403 上 `ErrorDetails.Message` 为空 | 用 `System.Net.Http.HttpClient` 直发（harness 的 `Invoke-AuthProbe`），不要用 `Invoke-WebRequest`/`Invoke-RestMethod` 断言错误码 |
| 非回环模式下"清单 `enabled=true` 却 403" | `enabled` 只表示处理器已注册，不代表免鉴权；非回环模式下 `/debug/*` 与 `/web/api/*` 都要令牌 | 带 `X-AICLI-Token`（或 `?token=`）重试，这是预期行为 |
| S10 `/exit` 返回 `status=rejected`（`reason=input rejected by command gate`） | 会话刚被 interrupt 占用（清理中），命令门短暂拒绝；**不是**鉴权或接口故障 | harness 在 `ExitTimeoutSec` 内有界重试（证据里的 `exit.attempts` 会 >1）；手工复现时等 1~2s 重发 |
| S3 `enabled=false` 或 `url` 为空 | 清单与实现脱节（端点未登记 / 未启用） | 在 `chat_debug_endpoints.go` 的 `webDebugEndpoints` / `loopbackDebugEndpoints` 补齐登记 |
| 响应中文乱码 / JSON 解析失败 | PowerShell 按控制台代码页解码原生命令 stdout | 用 `Invoke-RestMethod`，或 `Invoke-WebRequest` + `RawContentStream` 按 UTF-8 解码（harness 已内置）；不要 `curl.exe … \| ConvertFrom-Json` |
| S10 未优雅退出 | 会话里还有在跑任务或等待输入 | 先 `GET /web/api/turn` 看 `current`；必要时 `POST /web/api/input {"type":"interrupt"}`（缺省保留排队输入，`discard_pending=true` 清队）后再 `/exit` |
| 启动停在 provider/model 交互选择器 | 无人值守环境下的常见卡点 | 加 `-Headless`，或先在交互终端完成 provider 选择/`aicli login` |

## 7. 实测证据（2026-09-23）

### 7.1 固化运行结果

```powershell
pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e.ps1              # 含 go build
pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e.ps1 -SkipBuild   # 复跑（复用构建）
```

| 项 | 实测值 |
|----|--------|
| 构建 | `go build -trimpath -ldflags "-X main.version=e2e"` exit 0 → `backend/.tmp/aicli-debug-e2e.exe`（57,468,416 字节） |
| 启动 | 独立进程 `chat --yolo --web-port <自动端口>`（无 TTY，stdout/stderr 重定向）；`uptime_sec=2` 时 `/debug/endpoints` 已 `available=true` |
| 清单 | `listen_mode=loopback`；`web_base_url=http://127.0.0.1:<port>/web`；`write_auth_header=X-AICLI-Token`；text 含「Debug 使用说明」；**JSON 与 text 均不含令牌原文** |
| 读屏 | `/debug/chat/screen` `available=true`；`/web/api/screen?view=tui&format=json` `available=true`（9,940 字符合成帧） |
| invoke | `status=completed`、`elapsed_ms=6000`、`assistant="收到"`、`llm_observed=true`、`busy=false`、`pending_inputs=0`、`total_tokens=21014`（`context_window_tokens=128000`） |
| turn 后验 | `turn_id=turn_d12761bc-c6e3-48c2-aa57-c1799f1b5c0f`、`status=completed`、`duration_ms=4875`、`steps=1`、`usage_scope=turn`；`current.busy=false` |
| 幂等 | `duplicate=true`、`elapsed_ms` 与首次一致（6000）、`assistant` 与首次一致；`/web/api/turn` 的 `recent` 1 → 1（无第二次注入） |
| 屏幕回读 | TUI 合成帧包含本轮回复（needle `收到` 命中） |
| 收尾 | `POST /web/api/input {"prompt":"/exit"}` → `{"status":"queued"}`；`exit_code=0`；端口可重新绑定 |
| 结论 | **PASS=28 / FAIL=0**，脚本退出码 0；证据目录 `artifacts/aicli-debug-endpoints-e2e/20260923-233927/` |

复跑（`-SkipBuild`，自动端口 56847，`artifacts/aicli-debug-endpoints-e2e/rerun-check/`）：
同为 **PASS=28 / FAIL=0**；`invoke.elapsed_ms=3300`、`turn.duration_ms=2199`、
`turn_id=turn_946801d4-e918-488b-b2ec-3ae0f53a65e2` —— 断言集合稳定，
时间与 turn_id 随每次 LLM 调用浮动（断言不依赖固定值，只依赖结构与一致性关系）。

> 后续复验（2026-09-24，当前工作树）见 §7.4：带构建 PASS=30、`-SkipBuild` PASS=29，均 FAIL=0、退出码 0。

### 7.2 已知观察（写/改 E2E 前必读）

1. **`invoke` 响应的 `turn_id` 已做终态回填（2026-09-23 起）**。actor 在 turn 收尾后
   会清空 `state.CurrentTurnID`，因此该字段原先在 `status=completed` 时缺省；现在
   观察器从 `session_start` / `session_end` 事件载荷记录 turn 身份并在终态响应回填
   （turn 运行中仍以 state 探测值为准；`wait_only` 空闲短路等"无 turn 可归属"时保持缺省）。
   E2E 可直接断言 `invoke.turn_id == /web/api/turn` 的 `recent` 中 `status=completed`
   记录的 `turn_id`（S6c）；需要人工交叉核对时仍可用 `assistant_preview` 与本轮回复比对。
2. **`/web/api/screen?view=tui` 缺省返回纯文本合成帧**，只有追加 `&format=json`
   才是结构化快照（`available` / `lines` / `text`）。直接对该 URL 做 JSON 解析会失败。
3. **PowerShell 变量插值陷阱**：`"$url?view=tui"` 会被解析为变量名 `url?view`
   （`?` 是合法变量名字符），必须写 `"${url}?view=tui"`；本 harness 首版即因此失败
   （`Invalid URI: The hostname could not be parsed`），现已修复并在代码中留注释。
4. **启动窗口（就绪门必须判 `available`）**：`/debug/endpoints` 只要 HTTP 处理器注册完成就
   返回 **200**，但此刻 `available=false`、`reason="no active chat session"`；**必须判
   `available=true`**（会话已注册）再发起后续调用，否则写接口会返回
   `409 {"status":"error","reason":"no active chat session"}`。两个 harness 的就绪循环
   都按 `available=true` 实现，invoke 另有 `409` 重试窗口语义。
   （早期探针只判"HTTP 200"就开始 POST，因而把就绪窗口误判为接口缺陷——见 §7.3 观察 1。）
5. **端口与构建**：端口一律自动挑选（`-Port 0`）时无冲突；固定 `9999` 前先确认本机无占用
   （同一端口已被别的 aicli 实例占用时会回退随机端口并打印 Warning，断言会因 `web_base_url` 不匹配而失败）。

### 7.3 E2E-DEBUG-02（非回环鉴权）实测（2026-09-24）

```powershell
pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1              # 含 go build
pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1 -SkipBuild   # 复跑（复用构建）
```

| 项 | 实测值 |
|----|--------|
| 启动 | `chat --yolo --web-host 0.0.0.0 --web-port <自动端口> --web-token e2e-nonloopback-token-0123456789`；`listen_mode=non-loopback`；`uptime_sec=1` 时 `/debug/endpoints` 已 `available=true` |
| 清单 | `write_auth_hint=ALL 请求（含 GET/SSE）需携带 X-AICLI-Token（或 ?token=）…`；`web_base_url=http://0.0.0.0:<port>/web`；**JSON 与 text 均不含令牌原文**，LAN 区块为 `…?format=text&token=<token>` 占位符 |
| LAN 鉴权 | 无令牌 GET `/web/api/screen` → `403 {"status":"forbidden","reason":"missing or invalid X-AICLI-Token (required on non-loopback)"}`；令牌头 / `?token=` → `200`（9,975 字节合成帧）；错误令牌 → `403` |
| 页面 | 无令牌 GET `/web/` → `403`（index.html 不算静态资产）；`?token=` → `200`（40,696 字节，含 `aicli-web-token` meta 注入） |
| 调试端点 | 无令牌 GET `/debug/pprof/` → `403`；带令牌 → `200`（2,599 字节） |
| SSE | 无令牌 GET `/web/api/events` → `403`（只读响应头判定，不把长连接读成超时） |
| 写鉴权 | 无令牌 POST `/web/api/input` → `403`（拒绝发生在执行之前）；带令牌 → `200 {"status":"interrupted"}` |
| 豁免 | 回环 GET `/web/api/screen` → `200`（9,975 字节）；回环 POST `/web/api/input`（无令牌）→ `200`；LAN 静态资产 `/web/style.css` → `200`（70,472 字节） |
| 收尾 | 第 1 次 `/exit` → `200 {"status":"rejected","reason":"input rejected by command gate"}`（interrupt 后的清理态）；第 2 次 → `{"status":"queued"}`；`exit_code=0`；端口释放 |
| 结论 | **PASS=25 / FAIL=0 / SKIP=0**，脚本退出码 0；证据目录 `artifacts/aicli-debug-auth-e2e/20260924-001635/` |

**已知观察（写/改鉴权类 E2E 前必读）**

1. **探针必须能读非 2xx 的响应体**。PowerShell 7 的 `Invoke-WebRequest` 在 403 上抛出
   `HttpResponseException`，`ErrorDetails.Message` 为空且拿不到可读的 Response 流——首版
   harness 因此把"403 + JSON 拒绝体"误判成"403 + 空体"（2 条假红）。现改为
   `System.Net.Http.HttpClient` 直发（脚本内 `Invoke-AuthProbe`）：状态码与正文都稳定可得，
   行为与 PowerShell 版本无关；`-HeadersOnly` 专供可能长时间流式的 URL（SSE），
   避免"护栏失效被放行"时把断言失败变成读超时异常。
2. **令牌外泄（已修，回归红线）**。`?format=text` 的 LAN 区块曾把真实令牌拼进
   `…?format=text&token=<真实令牌>`（JSON 侧有 `WriteAuthToken json:"-"` 契约，文本侧漏了）。
   现统一为 `<token>` 占位符；单测 `TestChatDebugEndpointsTextNonLoopbackDoesNotLeakToken`
   锁死文本 + JSON 双通道，harness 另做正向断言（占位符必须存在，避免"整段 LAN 区块被删"
   也被判通过）。
3. **interrupt 之后 `/exit` 会被命令门拒绝**：`{"status":"rejected","reason":"input rejected by command gate"}`
   ——不是鉴权问题，等 1~2s 重发即入队。因此"验证写鉴权"的探针优先用 `{"type":"interrupt"}`
   （幂等无害，但会让会话短暂进入清理态），收尾一律用有界重试并在证据里记录 `attempts`。
4. **豁免是两条红线**：回环 IP 发起的请求读**与写**都免令牌；`/web/**` 静态资产
   （CSS/JS/favicon）免令牌。反之 `index.html`（`/web/`）、`/web/api/*`、`/debug/*` 与
   SSE 都不豁免——页面靠 `?token=` + meta 注入自举，`EventSource` 靠 `?token=` 拼接。
5. **本机无非回环 IPv4 时**（受限网卡 / CI 容器），依赖 LAN 的断言记为 **SKIP**（不计入
   PASS/FAIL，但写入 `run.log` 与 `summary.json` 的 `skipped[]`），避免假红；可用 `-LanIp` 指定。

### 7.4 复验（2026-09-24，改动中的工作树）

同一 harness 在当前（未提交）工作树上复跑四次，**全部退出码 0**：

| 命令 | 端口 | 结果 | 证据目录 |
|------|------|------|----------|
| `pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e.ps1 -Headless`（含 `go build`） | 50963（自动） | **PASS=30 / FAIL=0** | `artifacts/aicli-debug-endpoints-e2e/20260924-001708/` |
| 同前 + `-Port 9999 -SkipBuild`（§5「用户视角复现」） | 9999 | **PASS=29 / FAIL=0** | `artifacts/aicli-debug-endpoints-e2e/20260924-001909/` |
| 同前但 `-SkipBuild` 且**不加** `-Headless`（非 TTY 默认路径） | 59534（自动） | **PASS=29 / FAIL=0** | `artifacts/aicli-debug-endpoints-e2e/20260924-002045/` |
| 修复证据文件缺失后复跑（含 `go build`，见下） | 57665（自动） | **PASS=30 / FAIL=0** | `artifacts/aicli-debug-endpoints-e2e/20260924-002429/` |

计数口径：`-SkipBuild` 不产生 `build/go-build` 一条，故为 29；带构建时为 30
（§7.1 的 PASS=28 记录早于 S6c 两条断言 `invoke/turn-resolved` / `invoke/turn-id-inline` 加入）。

实测值（结构性结论与 §7.1 一致）：`listen_mode=loopback`、`web_base_url=http://127.0.0.1:<port>/web`、
`write_auth_header=X-AICLI-Token`；`discovery/token-not-leaked` PASS —— 令牌原文取自
`GET /web/api/token`（失败则回退启动行正则，两者都取不到即判 FAIL），而 JSON 与 `?format=text`
清单均不含它；`invoke.status=completed`、`usage.total_tokens=19886`、
`invoke.turn_id == /web/api/turn` 记录的 `turn_id`、`duplicate=true` 且 `elapsed_ms` 与首次一致、
`recent` 1 → 1（无二次注入）、`exit_code=0`、端口可重新绑定。

构建产物 57,486,336 字节（§7.1 的 57,468,416 是 2026-09-23 修订时的值）。
本机非 TTY 下未加 `-Headless` 也走通（配置可解析出可用 provider）；无可用 provider 的环境仍应加 `-Headless`。

**顺带修复（harness 证据文件）**：`go-build.log` 此前从未真正生成 —— `go build` 静默成功
（无 stdout/stderr）时管道不产生对象，`Tee-Object` 便不会创建文件；历史 6 个证据目录
（含 §7.1 固化运行与 `rerun-check`）全部缺该文件，而 `run.log` 的 `build/go-build` 行仍指向它。
已在 harness 中预创建该文件（`New-Item -ItemType File -Path $buildLogPath -Force`），
并复跑确认：文件存在（0 字节 = 构建静默）、断言仍为 **PASS=30 / FAIL=0**。

## 8. 与既有 E2E 的分工

| 场景 | Harness | 覆盖 | 依赖 |
|------|---------|------|------|
| **E2E-DEBUG-01**（本文） | `scripts/test-aicli-debug-endpoints-e2e.ps1` | 独立进程启动、`/debug/endpoints` 入口发现、读屏、同步 invoke、幂等回放、turn 后验、`/exit` 优雅退出（HTTP 控制面） | 真实 provider（第 4 步）；无交互桌面要求 |
| **E2E-DEBUG-02**（§7.3） | `scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1` | 非回环（`--web-host 0.0.0.0` + `--web-token`）鉴权契约：LAN 令牌必需、`?token=`、错误令牌、页面/SSE/`/debug/*` 同权、回环与静态资产豁免、清单不泄露令牌、`/exit` 收尾 | 真实 provider（只走 interrupt 与 `/exit`，不注入 prompt）；需非回环 IPv4（无则相关断言 SKIP） |
| 统一渲染 + marker exactly-once | `scripts/test-aicli-opencode-windows-terminal-e2e.ps1` | 真实 provider + Windows Terminal（UI Automation）下的渲染/历史/退出 | 交互桌面 |
| 终端渲染基线 | `scripts/test-aicli-windows-terminal-e2e.ps1` | 合成数据在真实宿主终端中的渲染 | 交互桌面 |
| turn 预算 / 生命周期 | `scripts/test-aicli-turn-budget-e2e.ps1` | 受控注入（无网络）的 turn 生命周期、预算熔断 | 无 |

> 三者互补：本场景回答"HTTP 控制面能不能可靠驱动/验收一次真实 turn"；
> Windows Terminal 场景回答"用户实际看到的终端渲染对不对"。
> 方法论见 [../plan/aicli-terminal-e2e-methodology.md](../plan/aicli-terminal-e2e-methodology.md)。

## 9. 相关文档

- [../aicli/web-remote-api.md](../aicli/web-remote-api.md) — `/web/api/*` 契约、鉴权（Host/Origin/写令牌）、
  `invoke` / `input` / `turn` / `screen` 语义与错误码。
- [../aicli/debug-chat-status.md](../aicli/debug-chat-status.md) — `/debug/chat/*` 与 `/debug/endpoints` 启用方式、
  会话粘性端口、启动行样例。
- [../user-guide/aicli-tui-remote.md](../user-guide/aicli-tui-remote.md) — 远程驱动 aicli TUI 的操作手册（含可复制脚本）。
- [../aicli/web-testing.md](../aicli/web-testing.md) — 微型 Web 客户端前端测试（手工回归清单 / stub API）。
- [../plan/aicli-terminal-e2e-methodology.md](../plan/aicli-terminal-e2e-methodology.md) — 终端 E2E 方法论。
- `scripts/test-aicli-debug-endpoints-e2e.ps1` — 本场景 harness（本文 §5 参数说明）。
- `scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1` — 姊妹场景 E2E-DEBUG-02 harness（本文 §7.3）。
