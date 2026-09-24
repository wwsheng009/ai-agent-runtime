# aicli 调试端点 E2E：实测证据与复验记录

> 本文是 [debug-guide.md](./debug-guide.md)（E2E-DEBUG-01 主线）的**证据附录**：固化运行结果、
> 姊妹场景实测、工作树复验，以及 `/debug/chat/status` agents 区块的两项修复记录。
> 结论性内容（怎么跑、断言是什么、失败怎么判读）在指南正文；本文只回答「某次运行实际发生了什么」。
>
> **本文即原 `debug-guide.md` §7 的独立成文**（2026-09-24 文档整理）：原 §7.1 → 本文 §1、
> 原 §7.3 → §2、原 §7.4 → §3、原 §7.5 + §7.6 → §4。
> 原 §7.2（已知观察「写/改 E2E 前必读」）属操作指南，仍留在 [debug-guide.md](./debug-guide.md) §7。

## 1. E2E-DEBUG-01 固化运行结果（2026-09-23）

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

> 后续复验（2026-09-24，当前工作树）见 §3：带构建 PASS=30、`-SkipBuild` PASS=29，均 FAIL=0、退出码 0。

## 2. E2E-DEBUG-02（非回环鉴权）实测（2026-09-24）

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

### 2.1 已知观察（写/改鉴权类 E2E 前必读）

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

## 3. 复验（2026-09-24，改动中的工作树）

同一 harness 在当前（未提交）工作树上复跑四次，**全部退出码 0**：

| 命令 | 端口 | 结果 | 证据目录 |
|------|------|------|----------|
| `pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e.ps1 -Headless`（含 `go build`） | 50963（自动） | **PASS=30 / FAIL=0** | `artifacts/aicli-debug-endpoints-e2e/20260924-001708/` |
| 同前 + `-Port 9999 -SkipBuild`（[debug-guide.md](./debug-guide.md) §5「用户视角复现」） | 9999 | **PASS=29 / FAIL=0** | `artifacts/aicli-debug-endpoints-e2e/20260924-001909/` |
| 同前但 `-SkipBuild` 且**不加** `-Headless`（非 TTY 默认路径） | 59534（自动） | **PASS=29 / FAIL=0** | `artifacts/aicli-debug-endpoints-e2e/20260924-002045/` |
| 修复证据文件缺失后复跑（含 `go build`，见下） | 57665（自动） | **PASS=30 / FAIL=0** | `artifacts/aicli-debug-endpoints-e2e/20260924-002429/` |

计数口径：`-SkipBuild` 不产生 `build/go-build` 一条，故为 29；带构建时为 30
（§1 的 PASS=28 记录早于 S6c 两条断言 `invoke/turn-resolved` / `invoke/turn-id-inline` 加入）。

实测值（结构性结论与 §1 一致）：`listen_mode=loopback`、`web_base_url=http://127.0.0.1:<port>/web`、
`write_auth_header=X-AICLI-Token`；`discovery/token-not-leaked` PASS —— 令牌原文取自
`GET /web/api/token`（失败则回退启动行正则，两者都取不到即判 FAIL），而 JSON 与 `?format=text`
清单均不含它；`invoke.status=completed`、`usage.total_tokens=19886`、
`invoke.turn_id == /web/api/turn` 记录的 `turn_id`、`duplicate=true` 且 `elapsed_ms` 与首次一致、
`recent` 1 → 1（无二次注入）、`exit_code=0`、端口可重新绑定。

构建产物 57,486,336 字节（§1 的 57,468,416 是 2026-09-23 修订时的值）。
本机非 TTY 下未加 `-Headless` 也走通（配置可解析出可用 provider）；无可用 provider 的环境仍应加 `-Headless`。

**顺带修复（harness 证据文件）**：`go-build.log` 此前从未真正生成 —— `go build` 静默成功
（无 stdout/stderr）时管道不产生对象，`Tee-Object` 便不会创建文件；历史 6 个证据目录
（含 §1 固化运行与 `rerun-check`）全部缺该文件，而 `run.log` 的 `build/go-build` 行仍指向它。
已在 harness 中预创建该文件（`New-Item -ItemType File -Path $buildLogPath -Force`），
并复跑确认：文件存在（0 字节 = 构建静默）、断言仍为 **PASS=30 / FAIL=0**。

## 4. `/debug/chat/status` agents 区块修复记录（2026-09-24）

### 4.1 status 的 agents 区块：`?fast=1` 曾经不设防（2026-09-24 修复）

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

> **已于同日完成**（`?fast=1` 之外的全量路径也收口）：见 §4.2。

### 4.2 agents 区块改为「样本 + 年龄」：全量路径不再排队（2026-09-24 修复）

**接 §4.1 的残留**：全量（非 fast）`/debug/chat/status` 仍同步等 agents 区块的三项数据
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
§4.1 观测到的数秒级延迟），现在共用同一个 `chatAgentRegistryAudit`（`chat_debug.go`），
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
| 对照 §4.1（同机、修复前） | 全量单请求 **924~3467ms** |

**E2E 复验**（`scripts/test-aicli-debug-endpoints-e2e.ps1`，两次复跑均退出码 0）：

| 运行 | 结果 |
|------|------|
| 带构建（`artifacts/aicli-debug-endpoints-e2e/20260924-1009*`） | PASS=42 / FAIL=0，`debug/status-latency :: full=21ms fast=6ms` |
| `-SkipBuild`（`artifacts/aicli-debug-endpoints-e2e/20260924-101356`） | PASS=41 / FAIL=0，`full=23ms fast=6ms` |

（两次差 1 条 = 第二次未构建，`build/go-build` 断言不参与；§4.1 记录修复前 full=2790ms。）

**回归守护**（`chat_debug_bounded_contract_test.go` 三条契约测试）：冷启动读取不得等待采集
（注入阻塞采集器，读仍 <250ms 且报 `collecting`）、TTL 内复用同一样本且年龄单调、JSON 与
文本在采集未完成时显式标注且不输出零值 `registry`。

## 5. live 终端 E2E（opencode + Windows Terminal）转全绿记录（2026-09-24）

**背景**：用户反馈"交互问答时答案无声无息消失"。排查结论：**不在后端**（HTTP 200、`chat.json` 内容完整、
`agent.turn.finished` 秒级收尾），而在 **aicli TUI 的渲染/输出链路**。因此把
[debug-guide.md](./debug-guide.md) §9 的「统一渲染 + marker exactly-once」live 场景从"恒红"
推到可复现的全绿门禁。

**复跑命令**（约 25~60 秒一轮；二进制已在，无需重建）：

```pwsh
pwsh -NoProfile -File scripts/test-aicli-opencode-windows-terminal-e2e.ps1 `
  -Provider opencode.ai -Model deepseek-v4.1-flash -ReasoningEffort max
```

**固化结果**（同一份代码连续两轮，run 目录在 `output/aicli-terminal-e2e/`）：

| 运行 | 结果 | marker exactly-once | UIA 抓取 | reasoning 投影 |
|------|------|--------------------|----------|----------------|
| `opencode-wt-3c82e753cde04b57a6cbf2cbdad5ee82` | `status=passed`、`failures=[]` | 违例 0（40 行各一次、顺序递增） | `stable=true`、`capture_attempts=3`、11422 字符 | `artifact_found=true`、`valid=true` |
| `opencode-wt-7f825835e857482ba9037b8a3d192a11` | `status=passed`、`failures=[]` | 违例 0 | `stable=true`、12034 字符 | `artifact_found=true`、`valid=true` |

两轮 `runner-exit-code.txt` 均为 `0`，`completion` 四项（`marker_40_observed` / `request_completed` /
`ready_prompt_restored` / `status_identity_validated`）全 `true`。

**证据链**（判定"渲染到底有没有问题"必须按这个顺序看，否则极易把环境问题误判成产品缺陷）：

| 层 | 证据 | 说明 |
|----|------|------|
| 模型是否产出 | `chat-logs/<date>/<session>/http/001_response_provider_wrapper.json` | 看 `response_status_code` 与 `assistant_message.content`；429 + `GoUsageLimitError` = 额度耗尽、模型零产出，终端本来就没有 marker |
| 客户端是否渲染 | `agent.turn.finished`（`elapsed_ms`）、`chat.json` | 答案在秒级完成，远早于 harness 的 300s 等待窗口 |
| 终端里到底有什么 | `uia-document-full.txt`（harness 抓的整屏文档）+ `scripts/read-terminal-buffer.ps1`（独立只读复核物理缓冲区） | 两者都空才算"没渲染"；只有 harness 抓的为空 = 抓取问题，不是渲染问题 |

**本轮定位并修掉的 4 个真实缺陷**（全部在 harness 侧，产品代码未改）：

| # | 现象 | 根因 | 修复 |
|---|------|------|------|
| 1 | E2E 429/401，而同一时刻交互式客户端能正常对话 | harness 读的是 opencode `auth.json` 的 `opencode-go`（该 workspace 月度额度已耗尽），不是交互式客户端实际使用的 `~/.aicli/auth.json` | 凭据解析顺序对齐为 `OPENCODE_API_KEY` → `~/.aicli/auth.json` → opencode `auth.json`（`Get-AicliStoreApiKey`，与 `-Provider` 同名条目优先） |
| 2 | `uia_capture.capture_attempts=9`、9 次 `captured=false`、`document_characters=0`（于是 marker 全 0） | 抓取以**启动哨兵行**为 UI Automation 锚点，而 TUI 重绘/回滚会合法擦除该行 → 找不到锚点 → 整屏读取为空 | `CaptureCore.Capture(windowHandle, needle)` 支持空 `needle`（无锚点整屏读取）；锚点失败即回退 |
| 3 | `marker N count=2`、`reasoning sentinel was not projected before marker 01` | 模型会在 **reasoning 块**里复述行格式、试写首尾行；断言在未屏蔽的整屏文档上计数，`firstMarkerIndex` 也取自未屏蔽文档 | 新增 `Remove-ReasoningBlock`：把 `reasoning` / `end reasoning` 分隔线之间的内容替换为**等长空格**（文档索引不变）；marker exactly-once / 顺序断言与 `firstMarkerIndex` 均改用答案区文档 |
| 4 | `provider reasoning summary artifact was not projected exactly once…`，且 `reasoning_projection.artifact_found=false` | **供应商/模型差异**：有些响应不带签名的 reasoning summary（同一份代码 run3/run4 有、run5 没有），终端无从投影它 | 记为**显式跳过**（manifest 新字段 `reasoning_projection_skipped`）而非失败；**摘要存在但没被恰好投影一次 / 投影晚于 marker 01 仍是硬失败** |

**已知边界**：live 场景需要交互桌面；harness 等 marker 40 的上限是 `-TimeoutSeconds`（默认 300s）。
若父进程被外部杀掉（例如前台 shell 命令自身超时），`run-chat.ps1` 与 `aicli-live-e2e.exe` 会变成孤儿
继续跑，且该轮永远不 finalize —— 复跑前先清理孤儿（`Stop-Process -Name aicli-live-e2e`），
或直接用后台任务跑。

## 6. 三层复验汇总（2026-09-24 20:00 前后，同一工作树）

| 层 | 命令 | 结果 |
|----|------|------|
| 控制面（E2E-DEBUG-01） | `pwsh -NoProfile -File scripts/test-aicli-debug-endpoints-e2e.ps1 -SkipBuild` | **PASS=41 / FAIL=0**、exit 0；证据 `artifacts/aicli-debug-endpoints-e2e/20260924-200055/`（`-SkipBuild` 不参与 `build/go-build` 断言，故比 §1 的 42 少 1 条） |
| 终端渲染基线（合成数据） | `pwsh -NoProfile -File scripts/test-aicli-windows-terminal-e2e.ps1` | 5/5 PASS、exit 0：72 行 history exactly-once、最旧/最新行经宿主文本缓冲区可达、增量历史把最旧行推进 scrollback、prompt/status 各一次、Markdown 渲染一次且无原始语法 |
| live 统一渲染 + marker exactly-once | `pwsh -NoProfile -File scripts/test-aicli-opencode-windows-terminal-e2e.ps1 -Provider opencode.ai -Model deepseek-v4.1-flash -ReasoningEffort max` | 两轮 `status=passed`、`failures=[]`（详见 §5） |

**结论与边界**：三层门禁下，live 场景的"渲染 / 历史 / 退出"可复现全绿，期间修掉的 4 个问题
（凭据来源、抓取锚点、断言作用域、供应商差异判定）**全部在 harness 侧**，产品渲染链路未发现缺陷。
需要注意：这只覆盖"**本场景**能构造出来的现场"（40 行长输出 + reasoning 块 + 滚动回读 + 退出）。
若用户报的某一次"答案消失"发生在别的会话/窗口状态下，按 §5 的证据链顺序再取一次现场
（`chat.json` 看内容 → `agent.turn.finished` 看时序 → `read-terminal-buffer.ps1` 看物理缓冲区），
不要用"E2E 绿了"直接反推那一次没问题。
