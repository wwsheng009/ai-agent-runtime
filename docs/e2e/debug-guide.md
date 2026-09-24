# aicli 调试端点 E2E 指南（独立进程 + `/debug/endpoints` + 屏幕回读 / invoke）

> 场景 ID：**E2E-DEBUG-01**（HTTP 控制面，本文主线）
> Harness：`scripts/test-aicli-debug-endpoints-e2e.ps1`
> 首次固化验证：2026-09-23（Windows / pwsh 7，真实 provider），结果见 §7。
> 姊妹场景：**E2E-DEBUG-02**（非回环鉴权，§7.3 / §9）、
> **E2E-DEBUG-03**（多进程网格控制面，规划中，§8）。
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

```powershell
# 一键回归（提交前推荐）：断言基线校验 → 01 → 02 → 聚合结论（artifacts/aicli-e2e-all/<stamp>/summary.json）
pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1

# 只做断言基线门禁（秒级，不跑 E2E；CI 上跑这条）：断言被删/改名立刻变红
pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1 -BaselineOnly
```

```powershell
# 多进程网格（E2E-DEBUG-03，规划中；设计见 §8，架构见 ../plan/aicli-mesh-architecture.md）
# 两进程互发现 → 定向调用（aicli-mesh call/send）→ 崩溃对账 → GC；harness 尚未落地
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
  见 §7.3/§9）覆盖：LAN 令牌必需、回环与静态资产豁免、页面/SSE/`/debug/*` 同权、
  清单不泄露令牌。
- **依赖真实 provider/model**：第 4 步 invoke 需要本机已配置可用 provider；无可用 provider
  时该步如实失败（不会伪造通过）。无人值守建议加 `-Headless`（跳过交互选择器，
  无可用 provider 时直接报错退出，TUI 与调试端点照常）。
- **不做并发压测**：`/web/api/invoke` 是单飞锁，并发调用返回 `409 {"status":"busy"}`；
  并发行为属于契约文档范围，不在本场景。
- **不做多进程协作**：本场景只驱动**一个**进程，进程之间的发现、定向调用、归属冲突与
  崩溃对账都不在此处，由 **E2E-DEBUG-03**（多进程网格控制面，规划中，见 §8）覆盖。
  但网格控制面的**单进程只读契约**在本场景断言：`mesh` 分组进清单、`health`/`self`/
  `peers` 形状、令牌默认脱敏与回环 reveal（见 §3 的 S4f/S4g）。为了让口径确定
  （`counts.live` 恒为 1）且不污染真实 `~/.aicli/mesh`，脚本把 `AICLI_MESH_DIR`
  隔离到证据目录的 `mesh/` 子目录。

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
| S4b | 状态端点契约（B1） | `GET /debug/chat/status` → `available=true`；`app_state` **显式存在**——有渲染器时 `available=true`，无渲染器（headless / stdout 重定向）时 `available=false` 且 `reason` 非空 | 状态端点整个省略 `app_state`，读屏断言无法区分「没有渲染器」与「渲染器正常」 |
| S4c | 有界降级（B3） | `GET /debug/chat/status?fast=1` → `fast=true`；`skipped_sections` 至少含 `files`、`storage`、`agents`（有渲染器时还须含 `plan_layout`）；full 响应耗时 < 5000ms | 降级不登记（消费者把缺席区块当「健康」）/ status 在风暴期拖住轮询方 |
| S4d | executor 诊断入断言（B4） | `GET /debug/pprof/executor` → HTTP 200 且 JSON 含 `diagnosis` / `total_recoveries`（脚本启动参数已含 `--pprof`） | 未开 pprof、端点未注册 |
| S4e | 清单覆盖门禁（B4） | `/debug/endpoints` 中每个 `enabled=true` 的端点，要么出现在断言表内，要么在脚本的豁免列表里**带理由**；二者都不占 = FAIL | 新端点悄悄进清单却无人断言 |
| S4f | 网格只读控制面（S5/M1） | 清单 `scheme=mesh` 分组含 `GET /web/api/health`、`GET /web/api/mesh/self`、`GET /web/api/mesh/peers`（URL 仍只从清单取）；`health` → 200 + `available=true` + `node_id` 非空 + `pid>0` + `mesh_ready=true`；`self` → 200 + `mesh.enabled=true` + `mesh.root` 等于隔离目录 + `liveness.state=live` + `derived` 非空；`peers` → 200 + `counts.live=1` + `nodes[]` 恰一个本节点 + `state=live` + `ownership=owner` + `reachability=skipped`（默认不探测）+ `filter={all,all}` + `self.node_id` 一致 | 端点未登记进清单 / 网格未接入（`mesh.enabled=false`）/ 归属判定或默认探测口径漂移 |
| S4g | 网格令牌反证（S5/M7） | `self.auth.token` 与 `peers` 节点 `auth.token_hint` 都只能是 `0f3a…` 形式，且两份原始响应文本都不含 `GET /web/api/token` 取到的原文；同一 URL 加 `?reveal_token=1`（回环）后 `auth.token` 与原文**逐字相等** | 默认输出泄露令牌（回归红线）/ reveal 通道失效（CLI 与 UI 拿不到原文） |
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
| `-KeepAliveOnFailSec` | `0` | 失败时保留被测进程 N 秒（端口与端点仍可用，便于人工连上复现），并自动抓取诊断包 + 双通道屏幕取证 |
| `-NoTimeline` | 关（即默认开启采样） | 关闭后台时序采样（`timeline.jsonl`） |
| `-TimelineIntervalMs` | `500` | 时序采样间隔（毫秒） |
| `-LatencyBudgetMs` | `5000` | `/debug/chat/status` 全量响应的延迟预算（毫秒）；饱和主机上排队延迟整体抬高（实测空载 3~7ms / 满载 2.9~11.9s），阈值用于区分「端点回归」与「机器在跑别的重负载」 |
| `-ArtifactDir` | `artifacts/aicli-debug-endpoints-e2e/<yyyyMMdd-HHmmss>` | 证据目录 |

退出码：`0` = 全部断言通过；`1` = 存在 FAIL（明细见 stdout、`run.log`、`summary.json`）。

证据目录结构：

```text
artifacts/aicli-debug-endpoints-e2e/<stamp>/
├── run.log              # 逐步 PASS/FAIL 时间线（含最终总结）
├── summary.json         # results[] + evidence.invoke / evidence.replay / evidence.turns
│                        #   + evidence.timeline{path,samples} / evidence.diagnostics{dir,captured}
├── timeline.jsonl       # A1 后台时序采样：每帧含 screen_hash/lines/chars、
│                        #   app_state/history/pending、skipped_sections（失败时给出连续帧而非孤立终态）
├── diag/                # A2 仅失败时：goroutine / executor / status(full+fast+text) / screen /
│                        #   endpoints + timeline 副本 + index.json + reason.txt
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

### 5.1 一键回归：聚合入口 + 断言基线（提交前 / 发布前推荐）

```powershell
# 基线门禁 → 顺序跑 01 → 02（02 恒 -SkipBuild 复用 01 的二进制）→ 聚合 summary.json
pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1

# 复用已有二进制（两场景都不 go build）
pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1 -SkipBuild

# 只做基线门禁（秒级；断言被删/改名就红，不跑 E2E）
pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1 -BaselineOnly

# 确实新增了断言，用它固化基线（新增名会写进本次日志）
pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1 -UpdateBaseline
```

| 项 | 说明 |
|----|------|
| 入口 | `scripts/test-aicli-e2e-all.ps1`；聚合证据在 `artifacts/aicli-e2e-all/<yyyyMMdd-HHmmss>/`（`run.log` + `summary.json` + 两个子场景目录 + 各自 stdout/stderr） |
| 断言基线 | `scripts/e2e-assertion-baseline.json`：静态抽取两个 harness 的 `Add-Result` / `Add-Skip` 调用点。**缺失 = FAIL**（防"悄悄删断言换绿"），**新增 = 提示**（用 `-UpdateBaseline` 固化） |
| 字段归一化 | 01 的 `summary.json` 用 `passed` / `failed`，02 用 `pass` / `fail` / `skip`；聚合脚本统一归一化。两套字段都读不到 = schema 漂移 → **直接 FAIL**（不是"读不到就记 0"）；`results` 条数还会与 `PASS+FAIL` 交叉校验 |
| 聚合退出码 | `0` = 无 FAIL；`1` = 有 FAIL（基线缺失断言 / 场景 FAIL / 缺 `summary.json` / 计数对不上） |
| 自测 | `scripts/test-aicli-e2e-all-selftest.ps1`：在 `%TEMP%` 沙箱里用**桩 harness** 跑真聚合脚本，验证 4 个分支（ok / schema 漂移 / 计数不符 / 子场景 FAIL）；不碰 provider、终端、端口 |

> 全量回归需要真实 provider（01 要注入 prompt）与一块非回环 IPv4（02），因此无 provider 的 CI
> 上建议只跑 `-BaselineOnly` 这条秒级门禁；有 provider 的环境（本机 / 发布前）再跑全量。

### 5.2 观测工具集（`scripts/aicli-e2e-harness.ps1`）

01 / 02 两个 harness 都 dot-source 同一个工具集。把「等固定秒数 + 只看终态」换成
「采样 + 指纹 + 自动取证」，是这套 E2E 从「能跑」到「失败可判读」的分界：

| 能力 | 函数 | 契约 |
|------|------|------|
| HTTP 原语 | `Invoke-HarnessRequest -Url -TimeoutSec -AsJson` | 显式按 **UTF-8 字节**解码（避免 PowerShell 代码页把中文回复解坏）；**从不抛异常**，统一返回 `ok / status_code / text / json / ms / error`——失败是数据，不是中断 |
| A1 时序采样 | `Start-AicliTimeline -BaseUrl -TimelinePath -IntervalMs -Tag -HarnessPath`、`Stop-AicliTimeline -Job`、`Get-AicliTimelineSample` | 每帧一行 JSONL：`screen_hash / screen_lines / screen_chars`、`app_state / history / pending`、`skipped_sections`、`ms`。只读 `?fast=1` 与屏幕文本，不参与会话调度 |
| A3 稳态判据 | `Wait-AicliScreenStable -BaseUrl -StableSamples -MinLines -TimeoutSec -IntervalMs -TimelinePath -Tag` | 连续 `-StableSamples`（缺省 3）次屏幕指纹不变且行数 ≥ `-MinLines` 才算就绪，返回 `stable / samples / ...`；替代「sleep 两秒后祈祷」 |
| A2 失败诊断包 | `Save-AicliDiagnostics -BaseUrl -DiagDir -Reason -TimelinePath -TimeoutSec` | 一次抓齐 `goroutine.txt` / `executor.json` / `status.json` / `status-fast.json` / `status.txt` / `screen.txt` / `endpoints.json` + timeline 副本，并写 `index.json`（每项 ok/ms/error）与 `reason.txt` |
| B4 清单覆盖门禁 | `Test-AicliEndpointCoverage -EndpointsJson -Asserted -Exempt` | 清单里每个 `enabled=true` 的端点必须命中 `-Asserted`（精确匹配或查询串变体），否则必须在 `-Exempt` 里**带理由**；返回 `ok / missing / exempt / checked` |
| C4 双通道取证 | `Get-AicliUiaScreenText -WindowTitle`、`Save-AicliDualChannelForensics -BaseUrl -DiagDir -WindowTitle -Label` | 同时落盘 HTTP 的「应然帧」（`<label>-http-expected.txt`）与 UI Automation 读到的物理终端帧（`<label>-uia-physical.txt`），用于回答「是没渲染，还是没显示」 |

> A1 的采样在**失败时**最有价值：它给出失败前后的连续帧，而不是一个孤立的终态。
> C4 依赖交互桌面（真实终端窗口）；无桌面时 UIA 侧写 `uia capture unavailable`，
> 不阻断 HTTP 侧取证。

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
| `coverage/endpoints-asserted` FAIL（`uncovered=[/x]`） | 清单里新增了端点，但既不在 `$assertedPaths` 也不在豁免表里——门禁**故意**拦这种情况 | 要么补断言，要么在 `$exemptPrefixes` 加前缀 + 理由；只从豁免里删而不补断言会造成「已断言却报无人断言」（20260924-074517 实测踩过） |
| `debug/status-app-state-explicit` FAIL | `app_state` 区块整个消失（B1 回归），读屏断言将无法区分「没有渲染器」与「渲染器正常」 | 检查 `BuildChatDebugDisplaySnapshotWithOptions` 的两个分支是否都设置 `snap.AppState`（含 `available/reason`） |
| `debug/status-fast-bounded` FAIL | `?fast=1` 未登记 `skipped_sections`（消费者会把缺席区块当「健康」） | 检查 `noteSkipped` 调用点；`agents` 与 `files/storage` 同属必登记重区块（registry 审计走会话库单连接，见 §7.5）；无渲染器时 `plan_layout` 本就不该出现（该条按 `app_state.available` 条件判定） |
| `debug/status-latency` FAIL | 可能是端点回归，也可能是机器在跑重负载（本机实测满载时 2.9~11.9s） | 先空载复测一次；确认是回归再查 `storage/files/scene_layout/agents` 是否漏进 fast 路径，必要时用 `-LatencyBudgetMs` 显式放宽并在结论里注明 |
| `diag/*` 全是 `connection refused` | 取证发生在进程**退出之后**（例如失败点在 S10 `/exit` 之后），端点已消失 | 已修：首个 FAIL 就**就地**取证（`Invoke-FailureForensics`，幂等）；若仍为空，看 `diag/reason.txt` 与 `aicli.stderr.log` |

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

### 7.5 status 的 agents 区块：`?fast=1` 曾经不设防（2026-09-24 修复）

**症状**：饱和主机上 `GET /debug/chat/status?fast=1` 实测 2517 / 4061 / 7298ms（孤立探针；
同机同期 `chat/screen?format=text` 仅 3ms）——「有界降级」在有界性上失效，A1 后台采样
（500ms 间隔）会被单次请求拖住数帧。

**定位**：goroutine 现场显示 3 个并发 status 请求全部阻塞在
`chat_debug_display_sections.go:366/367`，即 `buildChatDebugDisplayAgentsInfo` 里的
`chatAgentPanelRegistryLine` / `chatAgentControlConsistencyLines`；后者经
`auditLocalAgentRegistryForDebug`（`chat_debug.go:1344`，单次 2s 上限）读 agent registry。
会话库连接池恒为单连接，而后台 reconciler 正在同一连接上跑
`materializeLocalAgentRegistry` → `sweepStaleLocalAgentRegistry` → `(*Row).Scan`。
每次审计各自排队最多 2s，叠加即观测到的数秒级延迟。

**修复**（JSON 与文本两条路径同时收口，避免只修一条）：

- `BuildChatDebugDisplaySnapshotWithOptions`：agents 区块改由 `heavySectionSkipped()` 把关，
  跳过时 `noteSkipped(snap, "agents")`。
- `buildChatDebugDisplayDocumentWithOptions`（`?format=text`）：同一区块显式输出
  `Agents: skipped (fast/deadline)`。
- E2E 断言 S4c 相应升级：`skipped_sections` 必须含 `agents`（与 `files`/`storage` 同级）。

**实测**（`artifacts/aicli-debug-endpoints-e2e/20260924-075611`，带构建，PASS=36 / FAIL=0，退出码 0）：

| 项 | 修复前 | 修复后 |
|----|--------|--------|
| `?fast=1` 耗时 | 2517 / 4061 / 7298ms（探针，同机同期 screen=3ms） | **6ms** |
| `skipped_sections` | `[files,storage]` | `[files,agents,storage]` |
| full 耗时 | 924~3467ms | 2790ms（仍含 registry 审计，见下） |

**残留（已知、未隐藏）**：全量路径仍同步等待 registry 审计，单请求上限约 2s（并发时在
单连接上排队）。这是「全量」语义的一部分，不放进 fast 是有意为之；若要让全量也彻底无阻塞，
需要给注册表审计加短 TTL 缓存或独立只读连接——属 agent registry 连接模型的改动，另行评估。

> **已于同日完成**（`?fast=1` 之外的全量路径也收口）：见 §7.6。

### 7.6 agents 区块改为「样本 + 年龄」：全量路径不再排队（2026-09-24 修复）

**接 §7.5 的残留**：全量（非 fast）`/debug/chat/status` 仍同步等 agents 区块的三项数据
（registry 行 + 一致性审计、agent graph、mailbox）。三项都要读会话库**单连接**，且与后台
reconciler 争用，实测单请求 924~3467ms。

**修复**：整个 agents 区块改为 **stale-while-revalidate** 缓存
（`backend/cmd/aicli/commands/chat_agent_block_cache.go`）：

| 机制 | 行为 |
|------|------|
| 读 | **永不阻塞**：命中即返回；样本过期（TTL 5s）照常返回旧样本，刷新放后台 |
| 单飞 | 同一时刻最多一个采集在途——采集卡住也不会堆成风暴 |
| 冷启动 | 显式 `collecting`：既不排队等，也不把空区块渲染成「没有 agent / 健康」 |
| 年龄 | 读取时计算 `now - collectedAt`（不把年龄冻结进样本，否则永远显示采集那一刻的值） |
| 面板 | `/debug display`（零值选项）**保留同步直读**：人工排查要的正是当场那一份 |

顺带消除重复审计：同一次渲染里 registry 行与一致性行原先**各审计一次**（各 ≤2s，叠加即
§7.5 观测到的数秒级延迟），现在共用同一个 `chatAgentRegistryAudit`（`chat_debug.go`），
单次采集只读一次会话库。

**线上契约（新增字段，向后兼容）**：

| 字段 | 含义 |
|------|------|
| `agents.age_seconds` | 样本年龄（秒）；`-1` = 尚无样本 |
| `agents.collecting` | `true` = 首次采集在途；此时不输出 `registry`/`graph`/`mailbox` |
| `agents.consistency` | 采集完成时带 `age=`；未完成时为 `collecting (first sample in flight)` |

文本路径（`?format=text`）对应新增 `Agents Sample Age:` 行；采集未完成时三段都输出
`<collecting>`。`?fast=1` 语义不变：整块跳过并登记 `skipped_sections`（`agents`）。

**实测**（`.tmp/probe_agents_cache.ps1`，独立进程 `chat --yolo --web-port <port>`，2026-09-24 10:12）：

| 项 | 实测 |
|----|------|
| 冷读（首条有会话的快照） | **5ms**，`collecting=true`、`age_seconds=-1`、无 `registry` |
| 暖读 | **3ms**，`age_seconds=1.07`、`registry=local service=on … consistency_issues=13` |
| 6 并发全量请求 | 合计 **27ms** |
| `?fast=1` | 2ms，`skipped=[files,agents,storage]`，无 agents 区块 |
| 对照 §7.5（同机、修复前） | 全量单请求 **924~3467ms** |

**E2E 复验**（`scripts/test-aicli-debug-endpoints-e2e.ps1`，两次复跑均退出码 0）：

| 运行 | 结果 |
|------|------|
| 带构建（`artifacts/aicli-debug-endpoints-e2e/20260924-1009*`） | PASS=42 / FAIL=0，`debug/status-latency :: full=21ms fast=6ms` |
| `-SkipBuild`（`artifacts/aicli-debug-endpoints-e2e/20260924-101356`） | PASS=41 / FAIL=0，`full=23ms fast=6ms` |

（两次差 1 条 = 第二次未构建，`build/go-build` 断言不参与；§7.5 记录修复前 full=2790ms。）

**回归守护**（`chat_debug_bounded_contract_test.go` 三条契约测试）：冷启动读取不得等待采集
（注入阻塞采集器，读仍 <250ms 且报 `collecting`）、TTL 内复用同一样本且年龄单调、JSON 与
文本在采集未完成时显式标注且不输出零值 `registry`。

## 8. 多进程网格控制面（E2E-DEBUG-03，规划中）

> **状态：设计已定，harness 未落地。** 本节是 debug-guide 从「单进程 HTTP 控制面」升级到
> 「多进程网格控制面」的施工图。命名、目录、数据模型、API 契约与路线图见
> [../plan/aicli-mesh-architecture.md](../plan/aicli-mesh-architecture.md)（下称「网格方案」）。
> 本节的命令与端点按该方案书写；**在 harness 落地前不要把它当作可运行手册**。

### 8.1 为什么要升级：单进程假设的边界

| 维度 | 现状（01 / 02） | 网格化后 |
|------|-----------------|----------|
| 发现 | `GET /debug/endpoints` 只能发现**自己**这个进程的入口 | `aicli-mesh ls` / `GET /web/api/mesh/peers` 发现**本机全部节点**（跨工作区） |
| 地址 | 端口靠 `--web-port` 指定或粘性缓存（`~/.aicli/web-ports/`，待作废） | 端口由进程自选，地址从节点档案读（`~/.aicli/mesh/nodes/<node_id>.json`） |
| 调用 | 每个进程各自为战：脚本必须自己记住「哪个端口是哪个会话」 | 按**会话或节点**定向调用：`aicli-mesh call <node\|session> <op>` |
| 归属 | 无概念：两个进程可同时 resume 同一会话而无人察觉 | 租约 + 归属视图（`owner` / `peer` / `conflict`），冲突可见 |
| 生命周期 | 进程死了就只是「连不上」，残留记录与存活状态脱节 | 心跳 + pid 判活（`live` / `stale`）+ `gc` 对账 |
| 实时 | 每个进程一条 `/web/api/events` SSE，浏览器要连 N 个进程 | 节点做扇入：`GET /web/api/mesh/events` 一条流看全部 |

一句话：**01/02 验证「一个进程能不能被可靠驱动」，03 验证「多个进程能不能互相看见、互相调用、
并且不互相踩」**。

### 8.2 拓扑

```text
    ┌─────────────────────────── pwsh (harness) ───────────────────────────┐
    │ 1. go build → backend/.tmp/aicli-debug-e2e.exe                       │
    │ 2. Start-Process ×2（A、B：独立进程，无 TTY，端口自选）                │
    │ 3. aicli-mesh ls --json            → 网格发现（不猜端口）             │
    │ 4. aicli-mesh send <session> ...   → 定向调用（跨进程 invoke）         │
    │ 5. aicli-mesh screen <node>        → 读另一个进程的合成帧             │
    │ 6. GET /web/api/mesh/events        → 扇入实时流（A 看得到 B 变忙）     │
    │ 7. Stop-Process B（模拟崩溃）      → A 的视图转 stale                 │
    │ 8. aicli-mesh gc --apply           → 死节点/过期租约/旧目录对账        │
    └───────────────────────────────────────────────────────────────────────┘
            │ 读写                                  │ 读写
            ▼                                       ▼
    ~/.aicli/mesh/nodes/<node_id>.json      ~/.aicli/mesh/journal/<node_id>.ndjson
    ~/.aicli/mesh/bindings/<session_id>.json  ~/.aicli/mesh/leases/*.lock
            ▲                                       ▲
            └──── A（aicli chat --yolo --pprof） ───┘   B（aicli resume <sid> --pprof）
```

**红线**：网格目录只承载「谁在跑、跑的是谁、地址在哪、最近发生什么」；不承载会话内容，
不承载令牌副本（令牌只在活动节点档案里，见 §8.8）。

### 8.3 发现与调用契约（网格侧）

| 能力 | HTTP | CLI | 用途（本场景） |
|------|------|-----|----------------|
| 存活探针 | `GET /web/api/health` | — | 就绪等待、探活（`--probe`） |
| 节点自述 | `GET /web/api/mesh/self` | `aicli-mesh show <node>` | 断言令牌脱敏、地址自描述 |
| 全量视图 | `GET /web/api/mesh/peers?probe=1` | `aicli-mesh ls --json` | M1 / M2（CLI 与 HTTP 同源） |
| 实时扇入 | `GET /web/api/mesh/events` | `aicli-mesh watch` | M5（跨进程实时可见） |
| 定向调用 | `POST /web/api/mesh/call` | `aicli-mesh call/send/screen` | M4（跨进程 invoke） |
| 拉起节点 | `POST /web/api/mesh/spawn` | `aicli-mesh open` | 新窗口打开（不在 03 断言范围，01/02 不覆盖） |

**入口仍然只有一个**：`GET /debug/endpoints` 的清单新增 `mesh` 分组（`scheme: "mesh"`），
本场景与 01/02 一样**只从清单取 URL**，不硬编码路径。清单覆盖门禁
（`Test-AicliEndpointCoverage`，§5.2）会强制要求新端点要么被断言、要么带理由豁免。

> **与 01 的分工**：`health` / `self` / `peers` 的**单进程**只读契约（形状、默认口径、
> 令牌脱敏与回环 reveal）已由 E2E-DEBUG-01 断言（§3 的 S4f/S4g；网格根经
> `AICLI_MESH_DIR` 隔离到证据目录）。本场景补的是**多进程**语义——两节点互发现、
> 定向调用、归属冲突、崩溃对账与 GC；`events` / `call` / `spawn` 同样只从清单取 URL。

### 8.4 手工复现（多进程，最小步骤）

```powershell
$repoRoot = 'E:\projects\ai\ai-agent-runtime'
$exe = Join-Path $repoRoot 'backend\.tmp\aicli-debug-e2e.exe'   # 先 go build 产出，或复用已有构建

# 1) 两个独立进程（--pprof 自选随机空闲端口；网格负责发现，脚本不再需要记住端口。
#    注意 --web-port 不接受 0：要固定端口就显式给 1-65535 的值）
$a = Start-Process -FilePath $exe -ArgumentList 'chat','--yolo','--pprof' `
     -WorkingDirectory $repoRoot -PassThru `
     -RedirectStandardOutput "$env:TEMP\mesh-a.out.log" -RedirectStandardError "$env:TEMP\mesh-a.err.log"
$b = Start-Process -FilePath $exe -ArgumentList 'chat','--yolo','--pprof' `
     -WorkingDirectory $repoRoot -PassThru `
     -RedirectStandardOutput "$env:TEMP\mesh-b.out.log" -RedirectStandardError "$env:TEMP\mesh-b.err.log"

# 2) 网格发现（不猜端口、不读别人的缓存文件）
aicli-mesh ls
aicli-mesh ls --json | ConvertFrom-Json | Select-Object -ExpandProperty nodes |
    Format-Table node_id, pid, state, reachability, session_id, @{n='url';e={$_.endpoint.base_url}}

# 3) 定向调用：让 B 跑一轮 prompt 并等结果（A 不受影响）
aicli-mesh send <session-id-of-B> "只回复两个字：收到" --wait --json

# 4) 读另一个进程的屏幕（只读，不打扰它）
aicli-mesh screen <node-id-of-B> --tail 40

# 5) 实时（journal tail；全部进程退出后仍可复盘）
aicli-mesh watch --since 5m

# 6) 崩溃对账：强杀 B → A 的视图转 stale → GC 清理
Stop-Process -Id $b.Id -Force
aicli-mesh ls
aicli-mesh gc --apply
```

> 强杀进程在本场景是**预期动作**（模拟崩溃），不算 bug：网格不隐式杀进程，也不把「pid 不存在」
> 当作健康。GC 默认 dry-run，只有 `--apply` 才动手。

### 8.5 断言表（M1–M10）

| 断言名（`Add-Result` 字面量） | 动作 | 机器可判 | 失败典型原因 |
|------------------------------|------|----------|--------------|
| `mesh/discovery-both-nodes` | A、B 都启动就绪后 `aicli-mesh ls --json` + `GET /web/api/mesh/peers` | 两节点均 `state=live`，`endpoint.base_url` 非空且可达 | 档案未写 / 心跳未启 / 扫描路径不一致 |
| `mesh/cli-api-parity` | 比对 CLI 与 HTTP 两个视图 | 节点集合、会话 ID、`base_url` 完全一致 | 工具与端点各写一套聚合逻辑（违反「同源」） |
| `mesh/session-lease-exclusive` | B 尝试 resume A 的会话 | 返回 `busy` / `running_elsewhere`；`peers` 中该会话仍只有一个 `owner` | 租约未生效（无互斥 → 双开） |
| `mesh/cross-call-invoke` | A 通过 `mesh/call`（op=`invoke`，`allow_write`）让 B 跑一轮 | `status=ok`；B 侧 `/web/api/turn` 新增一条 `completed`；`duplicate=false` | op 白名单 / 令牌读取 / 幂等键透传任一环节断裂 |
| `mesh/realtime-fanin` | B 忙碌翻转期间订阅 A 的 `/web/api/mesh/events` | 时间窗内收到 B 的 `mesh.peer.updated`（`busy=true` 与回落） | 扇入未订阅 / 事件白名单漏 `session.*` |
| `mesh/crash-reconcile` | 强杀 B | A 的 `peers` 在 TTL 内把 B 标 `stale`；`gc --apply` 后 B 的档案与租约消失 | 判活只看文件时间不看 pid / GC 条件过宽误删活节点 |
| `mesh/no-token-leak` | 扫描 peers 输出、journal 文件、`/debug/endpoints`（JSON+text）、证据目录 | 均不含令牌原文（只允许 `0f3a…` 形式脱敏提示） | 令牌被写进绑定/journal/清单（回归红线） |
| `mesh/legacy-purge` | `gc --purge-legacy --apply` | 只删旧目录（`web-ports/` 等），`mesh/` 完全不受影响 | 清理路径写错，误伤新目录 |
| `mesh/self-containment` | 杀掉全部节点后 `aicli-mesh ls` | 正常返回（含 `stale` 节点与绑定），不报错、不卡住 | 工具依赖「有进程活着」才能工作 |
| `mesh/cross-workspace-ops` | B 以**另一个工作区**（不同 cwd）启动，比对 `ls`/`peers` 与跨工作区写调用 | 默认同时列出两个工作区（`workspace.path` 不同）；A→B 写调用默认成功；`--mesh-restrict-workspace` 下同一调用 `refused`（`mesh_cross_workspace_denied`） | 工作区被误当权限边界（默认拒绝）/ 过滤实现误伤归属判定 |

判定标准与 01 一致：**PASS** = 全部通过（退出码 0）；**FAIL** = 任一条不成立（退出码 1），
明细写 `summary.json.results[]`；**不做降级通过**（拿不到令牌原文就无法完成反证时判 FAIL）。

能力边界（与 01 的差异）：

- **需要真实 provider**：M4 要真的跑一轮 turn（无 provider 时如实 FAIL，不伪造通过）。
- **不需要交互桌面**：全程走 HTTP + 文件，无 UI Automation 依赖。
- **不做 spawn 断言**（P1 能力）：拉起新进程会创建高权限进程，留给手工验收与后续场景。
- **不做跨机**：默认拒绝非回环的网格写路径，跨机协作不在本场景。

### 8.6 harness 落地要点（与既有机制对接）

| 事项 | 位置（已验证） | 动作 |
|------|----------------|------|
| 场景表 | `scripts/test-aicli-e2e-all.ps1` 的 `$script:scenarios`（第 108–131 行，当前**硬编码 01/02 两项**） | 追加 `E2E-DEBUG-03` 一项（脚本名、超时、是否 `-SkipBuild`） |
| 参数分支 | 同脚本第 236–245 行（`if ($sc.id -eq 'E2E-DEBUG-01') {...} else {...}`） | 增加第三分支（二进制路径、端口策略、超时） |
| 断言名 | 基线抽取正则 `Add-(?:Result\|Skip)\s+(?:"([^"]+)"\|''([^'']+)'')`（第 138 行） | 新 harness 必须用 `Add-Result "mesh/..."` **字面量**，否则基线抓不到（等于没有门禁） |
| 基线固化 | `scripts/e2e-assertion-baseline.json`（`{version,generated_at,note,scenarios[]}`） | 跑 `-UpdateBaseline` 固化 M1–M10；此后**缺失=FAIL、新增=提示** |
| 字段归一化 | 01 用 `passed/failed`，02 用 `pass/fail/skip`；聚合做交叉校验 `results == PASS+FAIL` | 03 建议直接用 `pass/fail/skip` + `results[]`，**不要再添第三种方言** |
| 观测工具 | `scripts/aicli-e2e-harness.ps1`：`Invoke-HarnessRequest`（从不抛异常，返回 `ok/status_code/text/json/ms/error`）、`Wait-AicliScreenStable`、`Save-AicliDiagnostics`、`Test-AicliEndpointCoverage` | 03 harness dot-source 复用，不新造轮子 |
| 证据目录 | 沿用 `artifacts/.../<stamp>/` 约定 | 03 额外落盘 `mesh-ls.json` / `peers.json` / `journal/*.ndjson` 副本（**需脱敏**） |

### 8.7 失败模式与排查（网格专属）

| 现象 | 判读 | 处置 |
|------|------|------|
| `mesh/discovery-both-nodes` 只见一个节点 | 另一个进程没起来 / 没写档案 / 网格根目录不一致 | 看该进程 stderr 的启动行；确认两边 `AICLI_MESH_DIR` 相同（多用户/多环境变量混用时最容易踩） |
| 节点 `state=live` 但 `reachability=unreachable` | pid 活着但 HTTP 不可达（端口被防火墙拦、进程卡死、监听未起） | 手工 `curl /web/api/health`；区分「进程僵死」与「探活误判」 |
| `conflict` 出现在 peers 视图 | 两个 live 节点声明同一会话（租约失效或用户显式接管） | 这是**设计要暴露**的状态，不是 bug；按网格方案 §4.4 决定接管或退出其中一个 |
| `mesh/cross-call-invoke` 返回 `refused` | 策略拒绝：写 op 未带 `allow_write`、非回环、或（仅在 `--mesh-restrict-workspace` 开启时）跨工作区被禁 | 读响应 `code`（`mesh_write_not_allowed` / `mesh_cross_workspace_denied`）；按需显式开关，**不要**放宽默认值 |
| `mesh/cross-call-invoke` 返回 `busy` | 目标会话正忙（单飞锁），与 01 的 `409 busy` 同源 | 等目标 turn 结束再重试；不要换 `client_request_id` 重发 |
| `mesh/realtime-fanin` 收不到事件 | 扇入未订阅 / peer SSE 断线退避中 / 事件被白名单过滤 | 看 peers 视图的 `dropped_events` 与订阅状态；确认事件类型在白名单内 |
| `mesh/crash-reconcile` 未转 `stale` | 判活只看心跳时间没看 pid，或心跳 TTL 配得过大 | 检查判活实现（pid 不存在必须立刻 `stale`）与 `--stale-ttl` |
| `aicli-mesh` 命令不存在 | 工具未构建/未登记 | 按网格方案 §7.5 登记进 `scripts/build.ps1` 的 `$script:toolRegistry` 与 `Makefile` 后重新构建 |
| 03 在 `-BaselineOnly` 下报「基线缺失」 | 断言名被改名，或基线未固化 | 先 `-UpdateBaseline` 固化；改名视为回归（门禁故意拦） |

### 8.8 安全红线（E2E 也要守）

1. **令牌不落盘**：journal、绑定、`/debug/endpoints`、证据目录都不得出现令牌原文；
   M7 是这条红线的机器化检查（与 01 的 S4 令牌反证同源）。
2. **默认拒绝**：跨机与非回环的网格写路径默认拒绝；E2E **不**为图方便打开
   `--mesh-allow-nonloopback`（要测非回环，用 02 的鉴权场景）。
3. **写操作显式**：`invoke` / `input` / `cancel` / `sessions.resume` 必须 `allow_write`；
   只读 op（`status` / `screen` / `turn` / `sessions.list`）不需要。
4. **不隐式杀进程**：网格与工具都不提供「顺手清理」的隐式行为；停止进程永远需要显式动作
   （E2E 里的 `Stop-Process` 是场景自身的模拟崩溃，不是工具行为）。
5. **证据脱敏**：03 的证据目录会包含 peers/journal 快照，落盘前必须走脱敏（只留 `token_hint`）。

## 9. 与既有 E2E 的分工

| 场景 | Harness | 覆盖 | 依赖 |
|------|---------|------|------|
| **E2E-DEBUG-01**（本文） | `scripts/test-aicli-debug-endpoints-e2e.ps1` | 独立进程启动、`/debug/endpoints` 入口发现、读屏、同步 invoke、幂等回放、turn 后验、`/exit` 优雅退出（HTTP 控制面） | 真实 provider（第 4 步）；无交互桌面要求 |
| **E2E-DEBUG-02**（§7.3） | `scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1` | 非回环（`--web-host 0.0.0.0` + `--web-token`）鉴权契约：LAN 令牌必需、`?token=`、错误令牌、页面/SSE/`/debug/*` 同权、回环与静态资产豁免、清单不泄露令牌、`/exit` 收尾 | 真实 provider（只走 interrupt 与 `/exit`，不注入 prompt）；需非回环 IPv4（无则相关断言 SKIP） |
| **E2E-DEBUG-03**（§8，规划中） | `scripts/test-aicli-debug-endpoints-e2e-mesh.ps1`（待建） | 多进程网格控制面：两节点互发现、CLI/HTTP 视图同源、会话租约互斥、跨进程定向调用、实时扇入、崩溃对账与 GC、令牌不泄露、旧目录清理、无进程时仍可读、跨工作区默认可显示可操作 | 真实 provider（跨进程 invoke）；无交互桌面要求；需 `aicli-mesh` 工具已构建 |
| **一键回归**（§5.1） | `scripts/test-aicli-e2e-all.ps1` | 断言基线门禁 + 顺序跑 01 → 02 + 聚合结论（`artifacts/aicli-e2e-all/<stamp>/summary.json`） | 01 与 02 依赖的并集（真实 provider；02 需非回环 IPv4） |
| 聚合逻辑自测（§5.1） | `scripts/test-aicli-e2e-all-selftest.ps1` | 桩 harness 验证聚合脚本的 4 个分支（字段归一化 / 计数交叉校验 / schema 漂移 / 子场景 FAIL） | 无（不碰 provider、终端、端口） |
| 统一渲染 + marker exactly-once | `scripts/test-aicli-opencode-windows-terminal-e2e.ps1` | 真实 provider + Windows Terminal（UI Automation）下的渲染/历史/退出 | 交互桌面 |
| 终端渲染基线 | `scripts/test-aicli-windows-terminal-e2e.ps1` | 合成数据在真实宿主终端中的渲染 | 交互桌面 |
| turn 预算 / 生命周期 | `scripts/test-aicli-turn-budget-e2e.ps1` | 受控注入（无网络）的 turn 生命周期、预算熔断 | 无 |

> 三者互补：本场景回答"HTTP 控制面能不能可靠驱动/验收一次真实 turn"；
> E2E-DEBUG-03 回答"多个进程能不能互相发现、定向调用且不互相踩"；
> Windows Terminal 场景回答"用户实际看到的终端渲染对不对"。
> 方法论见 [../plan/aicli-terminal-e2e-methodology.md](../plan/aicli-terminal-e2e-methodology.md)。

## 10. 相关文档

- [../aicli/web-remote-api.md](../aicli/web-remote-api.md) — `/web/api/*` 契约、鉴权（Host/Origin/写令牌）、
  `invoke` / `input` / `turn` / `screen` 语义与错误码。
- [../aicli/debug-chat-status.md](../aicli/debug-chat-status.md) — `/debug/chat/*` 与 `/debug/endpoints` 启用方式、
  会话粘性端口、启动行样例。
- [../user-guide/aicli-tui-remote.md](../user-guide/aicli-tui-remote.md) — 远程驱动 aicli TUI 的操作手册（含可复制脚本）。
- [../aicli/web-testing.md](../aicli/web-testing.md) — 微型 Web 客户端前端测试（手工回归清单 / stub API）。
- [../plan/aicli-terminal-e2e-methodology.md](../plan/aicli-terminal-e2e-methodology.md) — 终端 E2E 方法论。
- [../plan/aicli-mesh-architecture.md](../plan/aicli-mesh-architecture.md) — 多进程网格架构方案
  （命名与目录、节点档案 / 绑定 / 租约 / journal、`/web/api/mesh/*` 契约、`aicli-mesh` 工具、路线图）。
- [../plan/aicli-micro-web-client-session-window-plan.md](../plan/aicli-micro-web-client-session-window-plan.md)
  — 网格的 Web 客户端子方案（v2）：会话切换「新窗口打开」前端交互、`sessions`/`resume` Web 侧契约、
  E1–E8 → M1–M10 验收映射。
- `scripts/test-aicli-debug-endpoints-e2e.ps1` — 本场景 harness（本文 §5 参数说明）。
- `scripts/aicli-e2e-harness.ps1` — 观测工具集（A1 时序采样 / A2 诊断包 / A3 稳态判据 /
  B4 清单覆盖门禁 / C4 双通道取证），由 01、02 两个 harness dot-source（本文 §5.2）。
- `scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1` — 姊妹场景 E2E-DEBUG-02 harness（本文 §7.3）。
- `scripts/test-aicli-e2e-all.ps1` — 一键回归聚合入口（断言基线 + 01 + 02 + 聚合结论，本文 §5.1）。
- `scripts/e2e-assertion-baseline.json` — 断言基线（"断言只增不减"的机器化检查）。
- `scripts/test-aicli-e2e-all-selftest.ps1` — 聚合脚本自测（桩 harness、负例驱动，本文 §5.1）。
