# aicli 调试端点 E2E 指南（独立进程 + `/debug/endpoints` + 屏幕回读 / invoke）

> 场景 ID：**E2E-DEBUG-01**（HTTP 控制面，本文主线）
> Harness：`scripts/test-aicli-debug-endpoints-e2e.ps1`
> 首次固化验证：2026-09-23（Windows / pwsh 7，真实 provider），结果见
> [debug-guide-evidence.md](./debug-guide-evidence.md) §1。
> 姊妹场景：**E2E-DEBUG-02**（非回环鉴权，[nonloopback-auth-e2e.md](./nonloopback-auth-e2e.md)）、
> **E2E-DEBUG-03**（多进程网格控制面，已落地，[mesh-e2e.md](./mesh-e2e.md)）。
> 相关契约文档：[../aicli/web-remote-api.md](../aicli/web-remote-api.md)、
> [../aicli/debug-chat-status.md](../aicli/debug-chat-status.md)、
> [../user-guide/aicli-tui-remote.md](../user-guide/aicli-tui-remote.md)。
>
> **本文只保留 E2E-DEBUG-01 主线的简要内容**（2026-09-24 文档整理）。其它内容已独立成文：
> 实测证据 / 复验 / status agents 修复记录 → [debug-guide-evidence.md](./debug-guide-evidence.md)；
> 多进程网格场景（E2E-DEBUG-03）→ [mesh-e2e.md](./mesh-e2e.md)；
> harness 观测与取证工具集（原 §5.2）→ [harness-observability.md](./harness-observability.md)。

## 0. TL;DR

```powershell
# 一条命令：构建 → 独立进程启动 → 发现 → 读屏 → invoke → 幂等回放 → turn 后验 → /exit 收尾
pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e.ps1
# 退出码 0 = 全部断言通过；1 = 有 FAIL（逐条列在 stdout 与 summary.json）

# 非回环鉴权（--web-host 0.0.0.0 + --web-token）：LAN 令牌必需 / 豁免红线 / 清单不泄露令牌
pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1
```

```powershell
# 一键回归（提交前推荐）：断言基线校验 → 01 → 02 → 03 → 聚合结论（artifacts/aicli-e2e-all/<stamp>/summary.json）
pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1

# 只做断言基线门禁（秒级，不跑 E2E；CI 上跑这条）：断言被删/改名立刻变红
pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1 -BaselineOnly
```

```powershell
# 多进程网格（E2E-DEBUG-03，已落地；手册见 mesh-e2e.md，架构见 ../plan/aicli-mesh-architecture.md）
# 两进程互发现 → 定向调用（aicli-mesh call/send）→ 崩溃对账 → GC
# harness：scripts/test-aicli-debug-endpoints-e2e-mesh.ps1
```

被测命令就是用户视角的那条（根命令自动分发到默认 `chat` 子命令，两种写法等价）：

```powershell
aicli --yolo --web-port 9999          # 等价 aicli chat --yolo --web-port 9999
```

## 1. 结论：这条链路可以做 E2E 吗

**可以。** 依据（均已实机验证，见 [debug-guide-evidence.md](./debug-guide-evidence.md)）：

| # | 依据 | 说明 |
|---|------|------|
| 1 | 端点先于 TUI 启动 | loopback HTTP 服务器在 `rootCmd.PersistentPreRunE` 阶段拉起，**早于** TUI；因此后台进程 / 无 TTY 进程同样暴露 `/debug/*` 与 `/web/*` |
| 2 | 入口自描述 | `GET /debug/endpoints` 返回 `available` / `listen_mode` / `web_base_url` / `loopback_base_url` / `write_auth_header` + `endpoints[]`（`method` / `path` / `scheme` / `enabled` / `url` / `note`），脚本**只依赖该清单**即可发现全部入口 |
| 3 | 判定可机读 | `POST /web/api/invoke` 一次响应返回 `status` / `assistant` / `usage` / `screen` / `turn_id`（终态 `turn_id` 由生命周期事件回填，见 §7）；`GET /web/api/turn` 提供 turn 后验记录（`turn_id` / `status` / `duration_ms` / `steps` / `assistant_preview`） |
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
  见 [nonloopback-auth-e2e.md](./nonloopback-auth-e2e.md) 与 §9）覆盖：LAN 令牌必需、回环与静态资产豁免、页面/SSE/`/debug/*` 同权、
  清单不泄露令牌。
- **依赖真实 provider/model**：第 4 步 invoke 需要本机已配置可用 provider；无可用 provider
  时该步如实失败（不会伪造通过）。无人值守建议加 `-Headless`（跳过交互选择器，
  无可用 provider 时直接报错退出，TUI 与调试端点照常）。
- **不做并发压测**：`/web/api/invoke` 是单飞锁，并发调用返回 `409 {"status":"busy"}`；
  并发行为属于契约文档范围，不在本场景。
- **不做多进程协作**：本场景只驱动**一个**进程，进程之间的发现、定向调用、归属冲突与
  崩溃对账都不在此处，由 **E2E-DEBUG-03**（多进程网格控制面，已落地，见 [mesh-e2e.md](./mesh-e2e.md)）覆盖。
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
| S6c | invoke 响应自带 `turn_id` | `invoke.turn_id` 非空且等于 S6b 记录的 `turn_id`（终态回填，见 §7） | 回填回归（字段再次缺失/不一致） |
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
# 基线门禁 → 顺序跑 01 → 02 → 03（02/03 恒 -SkipBuild 复用 01 的二进制）→ 聚合 summary.json
pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1

# 复用已有二进制（三个场景都不 go build）
pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1 -SkipBuild

# 只做基线门禁（秒级；断言被删/改名就红，不跑 E2E）
pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1 -BaselineOnly

# 确实新增了断言，用它固化基线（新增名会写进本次日志）
pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1 -UpdateBaseline
```

| 项 | 说明 |
|----|------|
| 入口 | `scripts/test-aicli-e2e-all.ps1`；聚合证据在 `artifacts/aicli-e2e-all/<yyyyMMdd-HHmmss>/`（`run.log` + `summary.json` + 各子场景目录 + 各自 stdout/stderr） |
| 断言基线 | `scripts/e2e-assertion-baseline.json`：静态抽取三个 harness 的 `Add-Result` / `Add-Skip` 调用点。**缺失 = FAIL**（防"悄悄删断言换绿"），**新增 = 提示**（用 `-UpdateBaseline` 固化） |
| 字段归一化 | 01 的 `summary.json` 用 `passed` / `failed`，02/03 用 `pass` / `fail` / `skip`；聚合脚本统一归一化。两套字段都读不到 = schema 漂移 → **直接 FAIL**（不是"读不到就记 0"）；`results` 条数还会与 `PASS+FAIL` 交叉校验 |
| 聚合退出码 | `0` = 无 FAIL；`1` = 有 FAIL（基线缺失断言 / 场景 FAIL / 缺 `summary.json` / 计数对不上） |
| 自测 | `scripts/test-aicli-e2e-all-selftest.ps1`：在 `%TEMP%` 沙箱里用**桩 harness** 跑真聚合脚本，验证 4 个分支（ok / schema 漂移 / 计数不符 / 子场景 FAIL）；不碰 provider、终端、端口 |

> 全量回归需要真实 provider（01/03 要注入 prompt）与一块非回环 IPv4（02），因此无 provider 的 CI
> 上建议只跑 `-BaselineOnly` 这条秒级门禁；有 provider 的环境（本机 / 发布前）再跑全量。

### 5.2 观测与取证工具集

已独立成文：[harness-observability.md](./harness-observability.md) —— `scripts/aicli-e2e-harness.ps1`
提供的 HTTP 原语 / A1 时序采样 / A2 失败诊断包 / A3 稳态判据 / B4 清单覆盖门禁 / C4 双通道取证，
三个 harness 共同 dot-source。

独立取证入口（C4 的单文件形态，用于 **aicli 进程已退出、只剩终端窗口** 的现场）：

```powershell
# 只读：把某个已打开终端窗口的物理缓冲区整段落盘，并按模式计数
pwsh -File scripts/read-terminal-buffer.ps1 -WindowTitle 'ai-agent-runtime' -Pattern 'AICLI-E2E-HISTORY-\d{3}'
# → artifacts/terminal-buffer-<stamp>.txt（完整文档）+ 命中计数 + 尾部若干行
```

判读口径不变：`/debug/chat/screen` 是"应然帧"，UIA 读到的才是"物理帧"；两者不一致
才说明渲染/终端链路丢了内容。**该脚本不向窗口发送任何输入**（不写 console input、
不 PostMessage），因此可以对用户正在使用的会话做只读取证。

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
| `debug/status-fast-bounded` FAIL | `?fast=1` 未登记 `skipped_sections`（消费者会把缺席区块当「健康」） | 检查 `noteSkipped` 调用点；`agents` 与 `files/storage` 同属必登记重区块（registry 审计走会话库单连接，见 [debug-guide-evidence.md](./debug-guide-evidence.md) §4.1）；无渲染器时 `plan_layout` 本就不该出现（该条按 `app_state.available` 条件判定） |
| `debug/status-latency` FAIL | 可能是端点回归，也可能是机器在跑重负载（本机实测满载时 2.9~11.9s） | 先空载复测一次；确认是回归再查 `storage/files/scene_layout/agents` 是否漏进 fast 路径，必要时用 `-LatencyBudgetMs` 显式放宽并在结论里注明 |
| `diag/*` 全是 `connection refused` | 取证发生在进程**退出之后**（例如失败点在 S10 `/exit` 之后），端点已消失 | 已修：首个 FAIL 就**就地**取证（`Invoke-FailureForensics`，幂等）；若仍为空，看 `diag/reason.txt` 与 `aicli.stderr.log` |
| live 终端 E2E 的 marker 计数恒为 0（`marker 1..40 count=0`） | 先看证据里 `http/*_response_provider_wrapper.json` 的 `response_status_code`：**429 + `GoUsageLimitError` = 模型额度耗尽**（2026-09-24 实测）。此时 `assistant_message.content=""`，模型零产出，终端里本来就没有 marker | **不是渲染缺陷**；换可用模型复跑同一套断言（harness 已参数化）：`-Model deepseek-v4.1-flash`，或恢复额度后重跑默认模型 |
| 同一个 provider/model，交互式客户端能正常对话、E2E 却 429/401（窗口停在「模型列表（逗号分隔）」提示、`aicli.log` 只有启动行 + 一次 DNS 解析） | **凭据来源错位**（2026-09-24 实测）：E2E 原先读 `~/.local/share/opencode/auth.json` 的 `opencode-go`，该 workspace 月度额度已耗尽（429 `wrk_01M12VKS57YGE9RHKE1VV57E2M`）；而交互式客户端用的是 `~/.aicli/auth.json` → `providers.<provider>.api_key`，同一时刻实测 chat **HTTP 200** | 凭据解析顺序已对齐为 `OPENCODE_API_KEY` → `~/.aicli/auth.json` → opencode `auth.json`（`Get-AicliStoreApiKey`，与 `-Provider` 同名条目优先）。判定口径：**只有同源凭据下仍失败**才算产品缺陷；下结论前先用同源 key 打一次 `/v1/models` 与最小 chat |
| 需要判定"内容到底有没有写进终端"，但 aicli 进程已退出 | 窗口仍在时，物理缓冲区是唯一还能读到的现场（HTTP 端点已 `connection refused`，`/debug/chat/status` 的历史效果计数也不再推进） | `pwsh -File scripts/read-terminal-buffer.ps1 -WindowTitle <title>`（只读、不发输入）；与 `*-http-expected.txt`（应然帧）逐行比对 |
| `uia_capture.document_characters=0` / `captured=false`，于是 `marker 1..40 count=0`，但 `http/*_response_provider_wrapper.json` 是 200 且 `assistant_message.content` 非空 | **抓取锚点被合法擦除**（2026-09-24 实测）：harness 原先用启动哨兵行做 UI Automation 锚点，而 TUI 重绘/回滚会把哨兵行挤出可见区 → 找不到锚点 → 整屏读取返回空。此时 `agent.turn.finished`（`elapsed_ms` 只有几秒）与 `chat.json` 已证明答案早就渲染完，**不是渲染缺陷** | 抓取在锚点失败时**回退为无锚点整屏读取**（`CaptureCore.Capture(windowHandle, needle)`，空 `needle` 即整屏）。判读口径：先看 `uia_capture.capture_attempts` 与 `*-http-expected.txt`，再用 `pwsh -File scripts/read-terminal-buffer.ps1 -WindowTitle <title>` 独立复核窗口物理缓冲区 |
| `marker N count=2`（多出的那一次出现在 `──── reasoning ────` 与 `──── end reasoning ────` 之间） | 模型会在思考里复述行格式、试写首尾行；断言原先在**未屏蔽的整屏文档**上计数 → 把"思考里的草稿"误判成"重复渲染" | marker exactly-once 与顺序断言限定在**答案区**：`Remove-ReasoningBlock` 先把 reasoning 分隔线之间的内容替换为**等长空格**（文档索引不变）再计数；`$document` 仍保留给其他断言 |
| `reasoning sentinel was not projected before marker 01`（而 sentinel 行本身 `count=1` 且在文档里） | 判据是"**整行独立**的 sentinel 行且其 index < `firstMarkerIndex`"；若 `firstMarkerIndex` 取自未屏蔽文档，会落在 reasoning 里的试写行上 → 必然早于答案区真正的 sentinel | `firstMarkerIndex` 与 marker 计数同源，都取自答案区文档；不要改 sentinel 判据去迁就污染值 |
| `provider reasoning summary artifact was not projected exactly once before marker 01`，且 manifest 里 `reasoning_projection.artifact_found=false` | **供应商/模型差异**（2026-09-24 实测：同一份代码 run3/run4 有签名摘要、run5 没有）：本次响应不带**签名的 reasoning summary**，终端自然无从投影它；reasoning 正文照常渲染，`raw assistant.reasoning` 泄露另有独立断言覆盖 | harness 记为**显式跳过**（manifest 新字段 `reasoning_projection_skipped`）而不是失败，避免门禁随机变红；**摘要存在但没被恰好投影一次 / 投影晚于 marker 01 仍是硬失败**——不要为了"变绿"放宽这一半 |
| live 终端 E2E 跑了 >4 分钟仍没有 `manifest.json`，`runner-exit-code.txt` 不存在，`aicli-live-e2e.exe` 与 `run-chat.ps1` 仍在 | harness 在等 marker 40，上限是 `-TimeoutSeconds`（默认 300s）；若**父进程被外部杀掉**（例如前台 shell 命令自身超时），runner 与 TUI 会变成孤儿继续跑，这一轮永远不 finalize | 跑 live E2E 用后台任务或足够长的超时；被中断后先 `Stop-Process -Name aicli-live-e2e` 并结束 `run-chat.ps1` 孤儿，再复跑，避免残留窗口干扰下一轮的窗口查找 |

## 7. 已知观察（写/改 E2E 前必读）

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
   （早期探针只判"HTTP 200"就开始 POST，因而把就绪窗口误判为接口缺陷——见
   [debug-guide-evidence.md](./debug-guide-evidence.md) §2.1 观察 1。）
5. **端口与构建**：端口一律自动挑选（`-Port 0`）时无冲突；固定 `9999` 前先确认本机无占用
   （同一端口已被别的 aicli 实例占用时会回退随机端口并打印 Warning，断言会因 `web_base_url` 不匹配而失败）。

> 实测证据、复验记录与 `/debug/chat/status` agents 修复记录已移至
> [debug-guide-evidence.md](./debug-guide-evidence.md)（原 §7.1 / §7.3–§7.6）。

## 8. 多进程网格控制面（E2E-DEBUG-03）

已独立成文：[mesh-e2e.md](./mesh-e2e.md)（原 §8 全文：升级动机、拓扑、发现与调用契约、
手工复现、M1–M12 断言表、harness 落地要点、失败排查、安全红线）。

> 章节号沿用原 `§8.x` 的 x：原 §8.5 断言表（M1–M10）→ [mesh-e2e.md](./mesh-e2e.md) §5（现为 M1–M12）；
> 原 §8.8 安全红线 → 其 §8。
> harness：`scripts/test-aicli-debug-endpoints-e2e-mesh.ps1`；单进程只读契约
> （`health` / `self` / `peers` 形状、令牌脱敏与回环 reveal）仍由本文 §3 的 S4f/S4g 断言。

## 9. 与既有 E2E 的分工

| 场景 | Harness | 覆盖 | 依赖 |
|------|---------|------|------|
| **E2E-DEBUG-01**（本文） | `scripts/test-aicli-debug-endpoints-e2e.ps1` | 独立进程启动、`/debug/endpoints` 入口发现、读屏、同步 invoke、幂等回放、turn 后验、`/exit` 优雅退出（HTTP 控制面） | 真实 provider（第 4 步）；无交互桌面要求 |
| **E2E-DEBUG-02**（[手册](./nonloopback-auth-e2e.md)） | `scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1` | 非回环（`--web-host 0.0.0.0` + `--web-token`）鉴权契约：LAN 令牌必需、`?token=`、错误令牌、页面/SSE/`/debug/*` 同权、回环与静态资产豁免、清单不泄露令牌、`/exit` 收尾 | 真实 provider（只走 interrupt 与 `/exit`，不注入 prompt）；需非回环 IPv4（无则相关断言 SKIP） |
| **E2E-DEBUG-03**（[手册](./mesh-e2e.md)，已落地） | `scripts/test-aicli-debug-endpoints-e2e-mesh.ps1` | 多进程网格控制面：两节点互发现、CLI/HTTP 视图同源、会话租约互斥、跨进程定向调用、实时扇入、崩溃对账与 GC、令牌不泄露、旧目录清理、无进程时仍可读、跨工作区默认可显示可操作 | 真实 provider（跨进程 invoke）；无交互桌面要求；需 `aicli-mesh` 工具已构建 |
| **一键回归**（§5.1） | `scripts/test-aicli-e2e-all.ps1` | 断言基线门禁 + 顺序跑 01 → 02 → 03 + 聚合结论（`artifacts/aicli-e2e-all/<stamp>/summary.json`） | 01/02/03 依赖的并集（真实 provider；02 需非回环 IPv4；03 需 `aicli-mesh`） |
| 聚合逻辑自测（§5.1） | `scripts/test-aicli-e2e-all-selftest.ps1` | 桩 harness 验证聚合脚本的 4 个分支（字段归一化 / 计数交叉校验 / schema 漂移 / 子场景 FAIL） | 无（不碰 provider、终端、端口） |
| 统一渲染 + marker exactly-once | `scripts/test-aicli-opencode-windows-terminal-e2e.ps1` | 真实 provider + Windows Terminal（UI Automation）下的渲染/历史/退出；`-Provider` / `-Model` / `-ReasoningEffort` 可覆盖（默认 `opencode.ai` / `deepseek-v4-flash` / `max`），某个模型额度耗尽时可用本机可用模型复跑**同一套**断言。**2026-09-24 已转全绿**（`opencode-wt-3c82e753…`、`opencode-wt-7f825835…` 两轮 `status=passed`、`failures=[]`、marker exactly-once 违例 0）；`manifest.json` 的 `reasoning_projection_skipped` 非空 = 该 provider 本次没返回带签名的 reasoning summary，投影断言按 §6 显式跳过（记录见 [debug-guide-evidence.md](./debug-guide-evidence.md)） | 交互桌面 |
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
- [debug-guide-evidence.md](./debug-guide-evidence.md) — 本文证据附录：固化运行结果、02 实测、复验、status agents 修复记录。
- [mesh-e2e.md](./mesh-e2e.md) — E2E-DEBUG-03 多进程网格控制面场景（原 §8）。
- [nonloopback-auth-e2e.md](./nonloopback-auth-e2e.md) — E2E-DEBUG-02 非回环鉴权场景。
- [harness-observability.md](./harness-observability.md) — harness 观测与取证工具集（原 §5.2）。
- [../plan/aicli-terminal-e2e-methodology.md](../plan/aicli-terminal-e2e-methodology.md) — 终端 E2E 方法论。
- [../plan/aicli-mesh-architecture.md](../plan/aicli-mesh-architecture.md) — 多进程网格架构方案
  （命名与目录、节点档案 / 绑定 / 租约 / journal、`/web/api/mesh/*` 契约、`aicli-mesh` 工具、路线图）。
- [../plan/aicli-micro-web-client-session-window-plan.md](../plan/aicli-micro-web-client-session-window-plan.md)
  — 网格的 Web 客户端子方案（v2）：会话切换「新窗口打开」前端交互、`sessions`/`resume` Web 侧契约、
  E1–E8 → M1–M12 验收映射。
- `scripts/test-aicli-debug-endpoints-e2e.ps1` — 本场景 harness（本文 §5 参数说明）。
- `scripts/test-aicli-debug-endpoints-e2e-mesh.ps1` — E2E-DEBUG-03 harness（[mesh-e2e.md](./mesh-e2e.md) §5 断言表）。
- `scripts/aicli-e2e-harness.ps1` — 观测工具集（A1 时序采样 / A2 诊断包 / A3 稳态判据 /
  B4 清单覆盖门禁 / C4 双通道取证），由三个 harness dot-source（见 [harness-observability.md](./harness-observability.md)）。
- `scripts/test-aicli-debug-endpoints-e2e-nonloopback.ps1` — 姊妹场景 E2E-DEBUG-02 harness（见 [nonloopback-auth-e2e.md](./nonloopback-auth-e2e.md)）。
- `scripts/test-aicli-e2e-all.ps1` — 一键回归聚合入口（断言基线 + 01 + 02 + 03 + 聚合结论，本文 §5.1）。
- `scripts/e2e-assertion-baseline.json` — 断言基线（"断言只增不减"的机器化检查）。
- `scripts/test-aicli-e2e-all-selftest.ps1` — 聚合脚本自测（桩 harness、负例驱动，本文 §5.1）。
