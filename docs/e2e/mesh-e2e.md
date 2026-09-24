# aicli 多进程网格控制面 E2E（E2E-DEBUG-03）

> 场景 ID：**E2E-DEBUG-03**（多进程网格控制面，已落地并固化验证）
> Harness：`scripts/test-aicli-debug-endpoints-e2e-mesh.ps1`（M1–M12；含启动自检与清单覆盖门禁共 16 条断言，基线已固化）
> 一键回归：`scripts/test-aicli-e2e-all.ps1` 已把本场景排在 01 → 02 之后（`03-mesh/`）
> 姊妹场景：[debug-guide.md](./debug-guide.md)（E2E-DEBUG-01 回环 HTTP 控制面）、
> [nonloopback-auth-e2e.md](./nonloopback-auth-e2e.md)（E2E-DEBUG-02 非回环鉴权）。

> **本文即原 `debug-guide.md` §8 的独立成文**（2026-09-24 文档整理）。章节号沿用原 `§8.x` 的 x：
> 原 §8.1 → 本文 §1、原 §8.5（M1–M10 断言表）→ 本文 §5（2026-09-24 扩到 M1–M12）、原 §8.8（安全红线）→ 本文 §8。
> 命名、目录、数据模型、API 契约与路线图见 [../plan/aicli-mesh-architecture.md](../plan/aicli-mesh-architecture.md)（下称「网格方案」）；
> 落地偏差（Windows 判活口径、目标解析统一、M3/M5 断言口径）见
> [../plan/aicli-mesh-implementation-plan.md](../plan/aicli-mesh-implementation-plan.md) §15.3 的 D5–D13
> （其中 Web 侧两项 D11/D12 已由 S11 收敛，见该计划 §19.4）。

## 1. 为什么要升级：单进程假设的边界

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

## 2. 拓扑

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
不承载令牌副本（令牌只在活动节点档案里，见 §8）。

## 3. 发现与调用契约（网格侧）

| 能力 | HTTP | CLI | 用途（本场景） |
|------|------|-----|----------------|
| 存活探针 | `GET /web/api/health` | — | 就绪等待、探活（`--probe`） |
| 节点自述 | `GET /web/api/mesh/self` | `aicli-mesh show <node>` | 断言令牌脱敏、地址自描述 |
| 全量视图 | `GET /web/api/mesh/peers?probe=1` | `aicli-mesh ls --json` | M1 / M2（CLI 与 HTTP 同源） |
| 实时扇入 | `GET /web/api/mesh/events` | `aicli-mesh watch` | M5（跨进程实时可见） |
| 定向调用 | `POST /web/api/mesh/call` | `aicli-mesh call/send/screen` | M4（跨进程 invoke） |
| 拉起节点 | `POST /web/api/mesh/spawn` | `aicli-mesh open` | 新窗口打开（不在 03 断言范围，01/02 不覆盖） |
| 停止节点 | `POST /web/api/mesh/stop` | `aicli-mesh stop` | M11（非回环下整机拒绝）；开关与 CLI 语义不在 03 断言范围 |

**入口仍然只有一个**：`GET /debug/endpoints` 的清单新增 `mesh` 分组（`scheme: "mesh"`），
本场景与 01/02 一样**只从清单取 URL**，不硬编码路径。清单覆盖门禁
（`Test-AicliEndpointCoverage`，见 [harness-observability.md](./harness-observability.md)）会强制要求新端点要么被断言、要么带理由豁免。

> **与 01 的分工**：`health` / `self` / `peers` 的**单进程**只读契约（形状、默认口径、
> 令牌脱敏与回环 reveal）已由 E2E-DEBUG-01 断言（[debug-guide.md](./debug-guide.md) §3 的 S4f/S4g；网格根经
> `AICLI_MESH_DIR` 隔离到证据目录）。本场景补的是**多进程**语义——两节点互发现、
> 定向调用、归属冲突、崩溃对账与 GC；`events` / `call` / `spawn` / `stop` 同样只从
> 清单取 URL（`stop` 由 M11 断言非回环拒绝，`spawn` 显式豁免）。

## 4. 手工复现（多进程，最小步骤）

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

## 5. 断言表（M1–M12）

| 断言名（`Add-Result` 字面量） | 动作 | 机器可判 | 失败典型原因 |
|------------------------------|------|----------|--------------|
| `mesh/discovery-both-nodes` | A、B 都启动就绪后 `aicli-mesh ls --json` + `GET /web/api/mesh/peers` | 两节点均 `state=live`，`endpoint.base_url` 非空且可达 | 档案未写 / 心跳未启 / 扫描路径不一致 |
| `mesh/cli-api-parity` | 比对 CLI 与 HTTP 两个视图 | 节点集合、会话 ID、`base_url` 完全一致 | 工具与端点各写一套聚合逻辑（违反「同源」） |
| `mesh/session-lease-exclusive` | B 尝试 resume A 的会话 | **拒绝即可**（`session not found` / `busy` / `running_elsewhere` 都是合法拒绝）；`peers` 中该会话仍只有一个 `owner` 且 `counts.conflict=0` | 租约未生效（无互斥 → 双开）/ 归属漂移 |
| `mesh/cross-call-invoke` | A 通过 `mesh/call`（op=`invoke`，`allow_write`）让 B 跑一轮 | `status=ok`；B 侧 `/web/api/turn` 新增一条 `completed`；`duplicate=false` | op 白名单 / 令牌读取 / 幂等键透传任一环节断裂 |
| `mesh/realtime-fanin` | 先在 A 上订阅 `/web/api/mesh/events`，**等 A 的流里出现 B 的帧**（扇入订阅接通，A 会合成 `mesh.peer.joined`）再让 A 跨进程调用 B | 时间窗内收到 B 的 `mesh.peer.updated`（`busy=true` 与回落），且同窗口 A 自己不曾 `busy`（定向投递反证） | 订阅未接通就发调用（`busy=true` 落在订阅之前，流不重放历史）/ 扇入未订阅 / 事件白名单漏 `session.*` |
| `mesh/crash-reconcile` | 强杀 B | A 的 `peers` 在 TTL 内把 B 标 `stale`；`gc --apply` 后 B 的档案与租约消失 | 判活只看文件时间不看 pid / GC 条件过宽误删活节点 |
| `mesh/no-token-leak` | 扫描 peers 输出、journal 文件、`/debug/endpoints`（JSON+text）、证据目录 | 均不含令牌原文（只允许 `0f3a…` 形式脱敏提示） | 令牌被写进绑定/journal/清单（回归红线） |
| `mesh/legacy-purge` | `gc --purge-legacy --apply` | 只删旧目录（`web-ports/` 等），`mesh/` 完全不受影响 | 清理路径写错，误伤新目录 |
| `mesh/self-containment` | 杀掉全部节点后 `aicli-mesh ls` | 正常返回（含 `stale` 节点与绑定），不报错、不卡住 | 工具依赖「有进程活着」才能工作 |
| `mesh/cross-workspace-ops` | B 以**另一个工作区**（不同 cwd）启动，比对 `ls`/`peers` 与跨工作区写调用 | 默认同时列出两个工作区（`workspace.path` 不同）；A→B 写调用默认成功；`--mesh-restrict-workspace` 下同一调用 `refused`（`mesh_cross_workspace_denied`） | 工作区被误当权限边界（默认拒绝）/ 过滤实现误伤归属判定 |
| `mesh/nonloopback-default-deny` | 目标进程带 `--web-host 0.0.0.0` 启动（非回环监听；另带 `--mesh-allow-stop=true` 只为让 stop 越过开关检查），用**回环地址**、带档案里的 `X-AICLI-Token` 直连同一端口发 `POST /web/api/mesh/call`（op=`node.info`）与 `POST /web/api/mesh/stop`（target=自己） | 两者都 HTTP 403 + `status=refused` + `code=mesh_nonloopback_denied`（整机退出网格写路径，与客户端来源无关）；同一节点的 `/web/api/status` 仍 200（拒绝是网格专属） | 非回环判定被短路/绕过（安全红线回归）；或把「非回环模式」误实现成「只拦非回环客户端」；或新端点（stop）漏接判定 |
| `mesh/nonloopback-cli-parity` | 同一节点：`aicli-mesh call <pid> node.info --json`（CLI 照档案 advertise 的地址直连，无客户端侧回环豁免） | 退出码 6 + `code=mesh_nonloopback_denied`；档案地址在本机不可达时如实记 SKIP（HTTP 侧已锁规则本体） | CLI 侧偷偷加了回环豁免（两套口径）/ advertise 地址不可达且未登记 SKIP |
| `mesh/journal-disabled` | 目标进程带 `--mesh-journal=false` 启动（A 保持默认作对照） | `mesh/self` 回显 `mesh.journal_enabled=false`（A=true）；该节点不落 `journal/<node>.ndjson`（A 的文件有内容）；`watch --once` 退出码 0 且回放里没有它的事件；`call` / `gc` 照常 | 开关未接线（回显与落盘不一致）/ 审计关闭被实现成「整机拒绝服务」 |

判定标准与 01 一致：**PASS** = 全部通过（退出码 0）；**FAIL** = 任一条不成立（退出码 1），
明细写 `summary.json.results[]`；**不做降级通过**（拿不到令牌原文就无法完成反证时判 FAIL）。

能力边界（与 01 的差异）：

- **需要真实 provider**：M4 要真的跑一轮 turn（无 provider 时如实 FAIL，不伪造通过）。
- **不需要交互桌面**：全程走 HTTP + 文件，无 UI Automation 依赖。
- **不做 spawn 断言**（P1 能力）：拉起新进程会创建高权限进程，留给手工验收与后续场景。
- **不做跨机**：默认拒绝非回环的网格写路径，跨机协作不在本场景。M11 只用
  `--web-host 0.0.0.0` 复现**默认拒绝**（连回环客户端也拒）；逃生门
  `--mesh-allow-nonloopback` 的放行路径**不在本场景**打开，由 Go 单测覆盖
  （`web_handlers_mesh_nonloopback_test.go`）。

## 6. harness 落地要点（与既有机制对接）

| 事项 | 位置 | 落地结果 |
|------|------|----------|
| 场景表 | `scripts/test-aicli-e2e-all.ps1` 的 `$script:scenarios` | 已追加 `E2E-DEBUG-03`（`dirName=03-mesh`；恒 `-SkipBuild`，复用 01 构建的 `aicli` 二进制） |
| 参数分支 | 同脚本的场景执行分支 | 已加第三分支：不占固定端口（`--pprof` 随机端口，地址从节点档案读）；`aicli-mesh` 缺失时由 03 自行构建 |
| 断言名 | 基线抽取正则 `Add-(?:Result\|Skip)\s+(?:"([^"]+)"\|''([^'']+)'')` | 已全部写成字面量：13 条 `mesh/*` + `gate/mesh-endpoint-coverage` + 2 条 `startup/*` = 16（基线可抓） |
| 基线固化 | `scripts/e2e-assertion-baseline.json`（`{version,generated_at,note,scenarios[]}`） | 已固化 01=40 / 02=31 / 03=16（M11/M12 于 2026-09-24 追加并重新固化）；`-BaselineOnly` 三项全 PASS，此后**缺失=FAIL、新增=提示** |
| 字段归一化 | 01 用 `passed/failed`，02 用 `pass/fail/skip`；聚合交叉校验 `results == PASS+FAIL` | 03 沿用 `pass/fail/skip` + `results[]`，未添第三种方言 |
| 观测工具 | `scripts/aicli-e2e-harness.ps1`：`Invoke-HarnessRequest`、`Test-AicliEndpointCoverage`、`Save-AicliDiagnostics` 等 | 03 harness dot-source 复用，未新造轮子（含「清单里的端点必须被断言覆盖」门禁） |
| 证据目录 | 沿用 `artifacts/.../<stamp>/` 约定 | 已落盘 `mesh-events.sse`（原始 SSE）、`evidence/*.json`（peers/ls/gc/resume/send 等，落盘前脱敏）、`A\|B\|B2` 的 stdout/stderr |
| 固化验证 | 单跑 / 聚合 | 单跑 16/16 绿（`artifacts/mesh-e2e-m11m12-r3/`）；聚合 01 → 02 → 03 全绿（`PASS=6 FAIL=0`，`artifacts/aicli-e2e-all/20260924-205713/`，01=41 / 02=28 / 03=16） |

## 7. 失败模式与排查（网格专属）

| 现象 | 判读 | 处置 |
|------|------|------|
| `mesh/discovery-both-nodes` 只见一个节点 | 另一个进程没起来 / 没写档案 / 网格根目录不一致 | 看该进程 stderr 的启动行；确认两边 `AICLI_MESH_DIR` 相同（多用户/多环境变量混用时最容易踩） |
| 节点 `state=live` 但 `reachability=unreachable` | pid 活着但 HTTP 不可达（端口被防火墙拦、进程卡死、监听未起） | 手工 `curl /web/api/health`；区分「进程僵死」与「探活误判」 |
| `conflict` 出现在 peers 视图 | 两个 live 节点声明同一会话（租约失效或用户显式接管） | 这是**设计要暴露**的状态，不是 bug；按网格方案 §4.4 决定接管或退出其中一个 |
| `mesh/cross-call-invoke` 返回 `refused` | 策略拒绝：写 op 未带 `allow_write`、非回环、或（仅在 `--mesh-restrict-workspace` 开启时）跨工作区被禁 | 读响应 `code`（`mesh_write_not_allowed` / `mesh_cross_workspace_denied`）；按需显式开关，**不要**放宽默认值 |
| `mesh/cross-call-invoke` 返回 `busy` | 目标会话正忙（单飞锁），与 01 的 `409 busy` 同源 | 等目标 turn 结束再重试；不要换 `client_request_id` 重发 |
| `mesh/realtime-fanin` 收不到事件 | 扇入未订阅 / peer SSE 断线退避中 / 事件被白名单过滤 | 看 peers 视图的 `dropped_events` 与订阅状态；确认事件类型在白名单内 |
| `mesh/realtime-fanin` 只有 `busy=false`、缺 `busy=true` | 发调用时 A 的扇入订阅还没接上 B：订阅由 `Subscriber.Sync` 按 tick 建立，接通前 B 的帧不会被扇入（流不重放历史） | 看 run 日志的 `fanin subscription to B: ready=... waited=...ms`；harness 已内置「扇入就绪门」（`-FaninReadySec`，缺省 20s），见方案 §15.3 D10 |
| `mesh/crash-reconcile` 未转 `stale` | 判活只看心跳时间没看 pid，或心跳 TTL 配得过大 | 检查判活实现（pid 不存在必须立刻 `stale`）与 `--stale-ttl` |
| `mesh/crash-reconcile` 里被强杀的 B 长期 `live`（Windows 特有） | 判活只看 `OpenProcess` 是否成功：Windows 上只要还有句柄指向进程对象，PID 就不回收，**已退出**进程照样「打开成功」 | 判活必须读退出码（`GetExitCodeProcess == STILL_ACTIVE`）；见方案 §15.3 D7 与 `process_alive_windows_test.go` |
| `mesh/session-lease-exclusive` 返回 `session not found` | 目标节点的本地会话存储没有该会话（源会话尚未落盘时必然如此），压根到不了租约判定 | 这是**合法拒绝**：断言断的是「归属不变」（owner 仍为 A、`conflict=0`）；租约语义由 `lease_test.go` 覆盖，见方案 §15.3 D9 |
| `aicli-mesh` 命令不存在 | 工具未构建/未登记 | 按网格方案 §7.5 登记进 `scripts/build.ps1` 的 `$script:toolRegistry` 与 `Makefile` 后重新构建 |
| 03 在 `-BaselineOnly` 下报「基线缺失」 | 断言名被改名，或基线未固化 | 先 `-UpdateBaseline` 固化；改名视为回归（门禁故意拦） |
| `mesh/nonloopback-default-deny` 没拿到 403 | 非回环监听下的写路径被放行（安全红线回归）；`call` 与 `stop` 两探针共用同一判定，只有一个被放行说明判定没做到三 handler 同源 | 检查 `IsChatWebLoopbackMode()` 短路与三个 handler 的跨机判定（`web_handlers_mesh_call/spawn/stop.go`）；确认**没有**传 `--mesh-allow-nonloopback`（B3 带的 `--mesh-allow-stop=true` 只为让 stop 越过开关检查、走到非回环判定，探针 target 指向 B3 自己，不是放行逃生门） |
| `mesh/journal-disabled` 里 self 回显 `true` 或仍落 journal 文件 | 开关未接线（flag → `HostConfig.JournalDisabled` → `Journal.Append` 任一段断） | 检查 `mesh_flags.go` 的 `applyMeshGovernanceFlags` 与 `mesh_bootstrap.go` 的 `JournalDisabled` 传参 |

## 8. 安全红线（E2E 也要守）

1. **令牌不落盘**：journal、绑定、`/debug/endpoints`、证据目录都不得出现令牌原文；
   M7 是这条红线的机器化检查（与 01 的 S4 令牌反证同源）。
2. **默认拒绝**：跨机与非回环的网格写路径默认拒绝；E2E **不**为图方便打开
   `--mesh-allow-nonloopback`（要测非回环，用 02 的鉴权场景）。M11 反过来把这条
   红线机器化：用 `--web-host 0.0.0.0` 复现「默认拒绝」，断言连回环客户端也被拒
   （`mesh_nonloopback_denied`）。
3. **写操作显式**：`invoke` / `input` / `cancel` / `sessions.resume` 必须 `allow_write`；
   只读 op（`status` / `screen` / `turn` / `sessions.list`）不需要。
4. **不隐式杀进程**：网格与工具都不提供「顺手清理」的隐式行为；停止进程永远需要显式动作
   （E2E 里的 `Stop-Process` 是场景自身的模拟崩溃，不是工具行为）。
5. **证据脱敏**：03 的证据目录会包含 peers/journal 快照，落盘前必须走脱敏（只留 `token_hint`）。
