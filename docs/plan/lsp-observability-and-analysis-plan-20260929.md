# LSP 观测与数据分析方案（含 micro web client 观测页面）

> 状态：方案（待评审）；micro web client「LSP 观测」页签 MVP 已落地，M1（请求级埋点 + host 接线）与 M2（observe 白名单/契约登记）已完成（2026-09-29，见 §5.6/§5.7）
> 日期：2026-09-29
> 关联事实源：`docs/lsp/02-runtime-lsp-integration-design.md` §2.2（L2 可观测注记）、`docs/lsp/03-implementation-plan-and-acceptance.md` A6/A7（数据缺口）、`docs/plan/runtime-observability-supervision-http-api-plan.md` §4.2/§6.3（采集路径与白名单）、`docs/knowledge_Layer/supplement/16_api_events_telemetry_and_rollout.md` §17/§18（事件与指标命名）、`docs/knowledge_Layer/adr/0003-exploration-attribution-metrics.md`（口径方法论）
> 边界：**不新增 SQLite 表**；不改 `lsp_servers` / `lsp_diagnostics`（属 knowledge_Layer、受 Phase4 门禁）；不新增并列设计文档，本文件是实施方案。
> 一句话：当前 LSP 只有"日志级"痕迹，无会话/工具关联、无耗时/命中/降级计数；本方案在既有 runtime.observe 基座上补 LSP 观测域，并在 aicli micro web client 增加「LSP 观测」页签，使采集数据直接产出优化依据。

---

## 1. 现状盘点：有没有 LSP 使用观测数据

### 1.1 代码事实

| 事实 | 证据 |
| --- | --- |
| LSP 侧有观测接缝，但只有 2 类事件（`server.state` / `diagnostics`） | `backend/internal/lsp/observer.go:8-17` |
| 唯一消费者是默认 `logObserver`；**生产路径 host Observer 恒为 nil**（非 nil 仅测试） | `backend/internal/lsp/bridge.go:62`、`backend/internal/tools/lsp_bridge.go:38-39`、`lsp_bridge.go:163`、`backend/internal/lsp/lifecycle_test.go:50-52` |
| `touchActive`（每次 didOpen/didChange/didSave）只更新 `LastActive`，**不发事件** | `backend/internal/lsp/client.go:290-295` |
| 事件最终写入全局 zap 日志（默认 `<home>/.aicli/logs/aicli.log`），**只有 pid，无 session/turn/tool_call 关联** | `backend/internal/tools/lsp_bridge.go:180-189`、`backend/cmd/aicli/commands/chat_logger_suppression.go:25-40` |
| LSP 包内无 metrics/计时/计数器（grep `metrics|expvar|Histogram|Counter|duration_ms` 空） | `backend/internal/lsp/` |
| runtime.observe 白名单（23 类）**没有任何 lsp 事件**（grep `lsp` 空） | `backend/internal/runtimeobserve/model.go:33-57`、`projector.go:33-58` |

### 1.2 真实日志实证（近 4 天窗口，全量会话日志扫描）

| 数据面 | 结果 |
| --- | --- |
| `events/runtime-events.jsonl` 中 `lsp.*` 事件类型 | **0 条**（LSP 字样只出现在提示词/工具名/文件路径） |
| 有 LSP 痕迹的会话 | 1849 个扫描会话中 **6 个** |
| `lsp_servers` / `lsp_diagnostics` 工具回执 | 8 次 / 4 次（`tool_receipt_recorded`，无 LSP 结果字段） |
| 编辑类工具回执尾部含 `<lsp_diagnostics …>` 内联块 | apply_patch **181**、edit **5**、write **4**（当前唯一高频 LSP 使用证据，且只是文本） |
| `<home>/.aicli/logs/aicli.log` 中 `lsp:` 行 | 41 行，全部生命周期/降级（gopls ready、pyright/typescript unavailable），无耗时、无关联 ID |
| 会话级 `debug/debug.log` | 0 行 `lsp:`（logObserver 不走会话日志） |

> 现场旁证：本机 aicli 会话中写 `.py` 文件的回执携带 `LSP diagnostics unavailable: lsp: start pyright: … not found`——内联路径正在运行、降级文案（hint）生效，但该事实只存在于回执文本，事件流不可查。

**结论**：现有数据只能回答"LLM 提示词里提到过几处 lsp"这一噪声级问题；**触发频率、命中率、降级率、等待耗时、token 增量、闭环效果均不可复算**。
---

## 2. 触发点与 LSP 能力使用矩阵（"哪些地方触发、用了什么"）

| # | 触发点 | 触发条件与调用链 | 用到的 LSP 能力 | 当前可见证据 | 观测缺口 |
| --- | --- | --- | --- | --- | --- |
| A | **编辑内联诊断（主路径）** | 工具成功 + metadata 含 `mutated_paths`（`backend/internal/tools/manager.go:168-173`；生产者：write/edit/apply_patch/multiedit/append_write/download，bash 动态注入）→ `Bridge.AppendToResult`（`internal/lsp/bridge.go:142`）→ `Diagnose`（`bridge.go:181`）→ didOpen/didChange（`client.go:371,386`）→ didSave（`client.go:415`）→ `WaitDiagnostics`（`client.go:471`）→ 文本追加 | initialize/initialized、didOpen/didChange/didSave、publishDiagnostics | 回执尾部文本；receipt 的 `message_bytes` | 无触发/命中/耗时/降级/token 增量事件；无 tool_call_id 关联 |
| B | `lsp_diagnostics` 工具（opt-in） | `lsp_bridge.go:405-413` → `Manager.LSPReport` → 与 A 同管线 | 同 A | tool_receipt 全文 | 同 A |
| C | `lsp_servers` 工具 | 只读状态，不触发启动（`lsp_bridge.go:267-289`） | 无 | tool_receipt | 可归入统一读数，无需新事件 |
| D | TUI `/lsp` 命令族 | status/diagnostics/restart/start（`chat_lsp_command.go:53-237`；10s/30s 兜底） | A 管线 + shutdown/重启 | 屏幕文本 | restart/start 结果无事件 |
| E | 启动期 bootstrap（仅 aicli chat） | `chat_setup.go:470` → 12s 扫描 → 写 `.aicli/runtime.yaml`（`chat_lsp_bootstrap.go:130-286`） | LookPath 过滤、池装配 | 少量日志 | 扫描结论/跳过原因/耗时不可查 |
| F | 生命周期与接收面 | 状态迁移（`client.go:122-246,706-711`）；publishDiagnostics、window/logMessage/showMessage（`client.go:549-568`） | 收通知 | aicli.log 行 | 未进事件基座、无会话关联 |

**能力边界**：hover/definition/references/documentSymbol/codeAction/formatting/rename **均未接入**；`didClose` 已实现但无调用方（`client.go:423-442`）。观测口径应聚焦"编辑 → 诊断"这一条闭环，不为未用能力设计。

---

## 3. 观测模型与埋点设计

### 3.1 事件模型（新增 3 类，命名向 `supplement/16` §17/§18 收敛）

| 事件 | 发射点 | 关键字段（全部低敏、可复算） |
| --- | --- | --- |
| `lsp.server.state` | `Client.setStatus → emit`（`client.go:264-288`），host 注入后转总线 | server、state(starting/ready/unavailable/crashed/stopped)、pid、reason、encoding |
| `lsp.request.finished` | `Bridge.Diagnose` 包裹计时（`bridge.go:181`）；每次触发**至多 1 条** | trigger(inline/tool/command/bootstrap)、tool_call_id、server(s)、outcome(injected / degraded_no_server / degraded_wait_timeout / degraded_no_fresh / error)、duration_ms、diag_count、appended_bytes、document_version、encoding |
| `lsp.diagnostics.updated` | `handlePublishDiagnostics`（`client.go:619-621`），按 turn 聚合或采样 | server、path_fingerprint、count、version |

要求：三类事件都必须带 `session_id`/`turn_id`/`tool_call_id`（内联路径在 `manager.go:168-173` 处注入最自然）；`tool_call_id` 可与既有 `tool_receipt_recorded` / `tool.completed` 对齐。

### 3.2 埋点位置清单

| 文件 | 改动 |
| --- | --- |
| `internal/lsp/observer.go` | EventKind 扩为 4 类；Event 增加 duration/outcome/count/bytes 字段（zero-value 兼容） |
| `internal/lsp/bridge.go` | `AppendToResult`/`Diagnose` 埋 `request.finished`；**no-server 静默跳过分支也要发**（`bridge.go:189-193`，它是覆盖率分母的一部分） |
| `internal/lsp/client.go` | `WaitDiagnostics` 回传实际等待时长/新鲜度；`setStatus` 已有 |
| `internal/tools/lsp_bridge.go` | **接上 Observer**（当前 `lsp_bridge.go:38-39/163` 传 nil）：转 bus 事件 |
| `internal/runtimeobserve/` | `model.go:33-57` 常量、`projector.go:33-58` 白名单、`projector.go:75-133` 字段投影、`known_types.go:95-119` 目录 |
| `internal/events/contract.go` | 登记 3 类型 + 通道表态（M4 已兑现：`lsp.request.finished` → `session_store` 供长窗口复算；`server.state` / `diagnostics.updated` 保持 live-only 约束体积） |
| `runtimeobserve/service.go:170-173` + `model.go:209-222` | Snapshot 新增顶层 `LSP` 域 + `components["lsp"].revision` |
| 新增 `internal/lsp/metrics.go` | `ComputeLSPMetrics()` 单一读数来源（范式见 `internal/supervision/metrics_readout.go:12-27`） |
| `cmd/aicli/commands/chat_lsp_command.go`、`chat_debug_document.go` | `/lsp status` 与 `/debug/chat/status` 增加读数小节 |

**门禁测试（必须同步处理）**：`internal/events/contract_test.go:140/157/287`、`internal/runtimeobserve/runtimeobserve_test.go:405/344/419`。

### 3.3 口径表（分子/分母现在定死，阈值后置——遵循 ADR-0003）

| 指标 | 口径 | 数据来源 |
| --- | --- | --- |
| `lsp_edit_coverage_ratio` | trigger=inline 的 request 数 / 带 `mutated_paths` 且 LSP enabled 的工具调用数 | request + tool 事件 |
| `lsp_diag_hit_ratio` | outcome=injected 且 diag_count>0 / outcome=injected | request |
| `lsp_fallback_ratio`（上游已定名） | outcome∈degraded_* / attempted | request |
| `lsp_error_ratio`（上游已定名） | outcome=error / attempted | request |
| `lsp_wait_latency_p50/p95` | duration_ms 分位（仅等待段） | request |
| `lsp_append_bytes_ratio` | appended_bytes / 工具回执 message_bytes | request + receipt |
| `lsp_server_failure_ratio` / restart 次数 | (unavailable+crashed) / starting；restart 计数 | server.state |
| `lsp_closure_ratio`（M4） | 同 file+diag fingerprint 在下一次该文件编辑后消失 / 有注入的编辑 | 需 fingerprint（只存 hash） |

### 3.4 约束

- 脱敏：路径用 fingerprint（`internal/runtimeobserve/redaction.go:125` 已有 HMAC 工具）；诊断正文一概不存。
- 限流：`lsp.diagnostics.updated` 按 publish 频率可能很吵，**按 turn 聚合或采样**；`request.finished` 每触发至多 1 条。
- 传输现状：observe v1 **无 SSE**（`model.go:16` 的 `SchemaVersionSSE` 无生产者；`handler.go:1183` 注释"SSE 与 renderer 依赖 Phase 3/4"），页面用轮询 `/events`；SSE 是 chat 事件流那条并行通道（`web_schema.go:135-173`）。
- 事件白名单是封闭目录，新增必须同时改 4 处并过门禁；本期不落任何 SQLite 新表。
---

## 4. 数据分析 → 优化决策闭环（采集数据必须能回答"接下来改什么"）

### 4.1 优化决策矩阵

每个现象都必须给出：**判定证据 → 候选动作 → 回归验证 → 决策留痕**。阈值一律标注"待基线标定"，不写死。

| 现象（待标定） | 判定证据 | 候选优化动作 | 回归验证 | 留痕位置 |
| --- | --- | --- | --- | --- |
| `lsp_fallback_ratio` 高且多数为 `degraded_no_server` | request.outcome 分布 | 补装/配置 server 二进制（pyright、typescript-language-server 等）；bootstrap LookPath 提示前置到 `/lsp status` | 重跑对照实验：降级率下降 | 本文档 §4.3 基线表 + `docs/lsp/03` §4 配置表 |
| `degraded_wait_timeout` 高 / `wait_latency_p95` 高 | duration_ms 分位 + outcome | 调 `diagnostics.wait_ms`、`scope=changed`、`lsp.prewarm`、增量 sync | A/B：P95 与降级率同时达标 | 配置默认值（`internal/lsp/spec.go` 默认值注释） |
| `lsp_append_bytes_ratio` 高 / 截断频繁 | appended_bytes、截断标记 | 调 `max_items` / `max_chars`；`scope=changed`（Q1 结论的数据来源） | token 增量与噪声可视对比 | `docs/lsp/03` A6/A7 回填 |
| `lsp_diag_hit_ratio` 异常低 | request.diag_count=0 占比 + server/root | 检查 server 根目录/`workspaceFolders`、能力协商（`client.go:203` 的 workspaceFolders=false 与发送行为矛盾）、sync kind | 命中率上升 | 本文档 §4.3 |
| `lsp_closure_ratio` 低 | fingerprint 消失率 | 提示词/工具面（先修诊断的引导）、诊断渲染可读性、编辑循环策略 | 修复回合数下降 | docs/lsp 02 §4.2 工具面 |
| `lsp_server_failure_ratio` 高 | server.state 原因分布 | 二进制路径/权限/Job Object 生命周期（ADR-0005）、重启预算 `lsp.restart_limit` | 失败率与崩溃次数下降 | 配置表 |
| 触发覆盖低（编辑多、inline 少） | coverage_ratio | `mutated_paths` 覆盖缺口（bash/download 动态注入是否漏）、LSP 开关默认值 | 覆盖率提升 | `manager.go:168-173` |

### 4.2 分析工作流（三层）

1. **实时（会话内）**：micro web client「LSP 观测」页签 + `/observe/v1/events?type=lsp.*`（`internal/api/runtimeapi/observe_handlers.go:129/192`），回答"这次编辑为什么没出诊断"。
2. **会话复盘**：`~/.aicli/chat-logs/**/runtime-events.jsonl` 按 session 聚合（脚本 `scripts/`，字段口径同 §3.3），回答"哪个工具/项目/服务器在失败"。
3. **周期基线**：按天/项目聚合出基线报告，写回 §4.3；阈值只在基线产出后固化（ADR-0003 D4）。

### 4.3 基线登记表（首次回填 2026-10-01，阈值仍待标定）

| 指标 | 基线值 | 采样窗口 | 样本量 | 结论/阈值 | 日期 |
| --- | --- | --- | --- | --- | --- |
| `lsp_edit_coverage_ratio` | 0.8807 | 09-29T22:50Z → 10-01T00:48Z | inline 753 / LSP 活跃会话内 edit 855（全部 5585） | 待标定 | 2026-10-01 |
| `lsp_diag_hit_ratio` | 1.0000 | 同上 | hit 39 / injected 39 | 待标定 | 2026-10-01 |
| `lsp_fallback_ratio` | 0.5597 | 同上 | degraded 422 / 754（no_server 133、clean 160） | 待标定；含 274 条"未分类降级"（见下） | 2026-10-01 |
| `lsp_wait_latency_p95` | 1000 ms | 同上 | n=754（P50 1 ms） | 待标定 | 2026-10-01 |
| `lsp_append_bytes_ratio` | 0.1943 | 同上 | 追加 109258 B / 回执可见 562370 B | 待标定 | 2026-10-01 |
| `lsp_closure_ratio` | 1.0000 | 同上 | closed 2 / eligible 2 | 待标定；样本仅 2（fingerprint 仅新构建事件携带） | 2026-10-01 |

> 反模式（明令禁止）：把"未采集"渲染成 0；把分母含未启用 LSP 的会话算进覆盖率；阈值未标定就写进告警。

**首次回填的口径限制（固化阈值前必须处理）**：

1. 窗口跨三个构建。旧构建事件记录 `outcome="degraded"` 但不带 `reason`（274 条，36%）：
   它们确实是降级——计入 fallback 分子**是正确口径，不是污染**——但**原因不可分类**
   （no_fresh / starting / read error 不可区分），所以这段样本只支撑总体 fallback，
   不支撑原因级结论；新构建请求事件的 `outcome` 已是细分类别，且**自第六轮起请求事件落盘
   低敏 `reason_category`**（wait_timeout / no_publish / starting / …），可按原因细分——
   本窗口的旧事件没有该字段。
   报告事实区已单独标注"未分类降级"（Go 基线包与 Python 脚本同步，保持互锁）。
2. 优化构建后的两个会话（25+12=37 请求）样本：clean 14 / injected 2 / no_fresh 14 /
   starting 2 / pyright 缺二进制 4 / no_server 1。其中"缺二进制 4"在事件里曾是裸 `degraded`
   （第七轮起细分为 `degraded_binary_missing`）。样本含实机测试脚本污染（冷视图轮次
   本身就是被测场景），只作方向参考，不作阈值依据。
3. 阈值固化前置条件：优化构建纯新样本 ≥1 周（ADR-0003 D4）；未分类标注已完成。

**回填工具（M4，已就绪）**：`scripts/analyze-lsp-baseline.py` 读取 `chat-logs/**/runtime-events.jsonl`（口径见 §3），
直接产出上表格式并附事实明细与按日分布：

```powershell
py scripts/analyze-lsp-baseline.py --days 14 --out .tmp/lsp-baseline.md
py scripts/analyze-lsp-baseline.py --selftest   # 内置样例自验（不依赖真实数据）
```

纪律：未采集项输出 `n/a` + 原因；覆盖率分母限定在**LSP 活跃会话**内的编辑调用；
阈值列一律"待标定"，脚本不做阈值告警。

**产品内入口（同口径）**：`/lsp baseline [--days N | --since YYYY-MM-DD | --root DIR]`
（`internal/lsp/baseline`，走副屏文档；与脚本用同一 fixture 数字互锁：3 请求 /
覆盖率 1.0 / P50 10ms / 追加比 100:1100）。默认最近 14 天，按文件 mtime 整文件跳过；
真实库全量（1432 文件 / 49.5 万行）冷扫约 8s，输出头尾都带耗时与跳过计数。

---

## 5. micro web client 观测页面（`backend/cmd/aicli/commands/web/`）

### 5.1 定位与页面结构

新增「LSP 观测」页签（菜单挂载点：`web/index.html:36-49` 视图/帮助菜单；页签实现样式与 `js/analysis.js` 同构：无构建、ES module、15s 可见性自动刷新、会话感知）。

| 区块 | 内容 | 数据源 |
| --- | --- | --- |
| A 池状态卡 | 每个 server：state/pid/encoding/reason/lastActive；配置摘要（scope/max_items/wait_ms/degrade_mode/tool_enabled） | `/web/api/lsp/status`（复用 `chatLSPManager(session)`：`chat_lsp_command.go:118-145`） |
| B 会话读数条 | 触发次数、命中率、降级率（按原因分桶）、等待 P50/P95、追加字节占比 | `/web/api/lsp/overview?scope=session|all` |
| C 最近事件表 | 时间 / trigger / server / outcome / duration_ms / diag_count / appended_bytes / tool_call_id（可跳转对话中对应工具行） | `/web/api/lsp/events?limit=50`（M2 后源自 observe ring/Query） |
| D 优化建议条 | 由读数生成的人话结论（例："pyright 未安装，3 次内联降级"；"等待 P95 超 wait_ms 的 60%，建议调低 scope"）；**阈值未标定则不显示判断，只显事实** | 前端规则表（阈值引用 §4.3） |
| E 链路入口 | 链到 `/debug/chat/status`、runtime-server 观测页 | 现有端点 |
| F 基线登记表（跨会话） | §4.3 六项指标（未采集显示 n/a）+ 窗口/扫描事实；阈值列固定"待标定"，不做告警 | `/web/api/lsp/baseline?days=14`（`internal/lsp/baseline`，服务端 10 分钟 TTL 缓存；与 TUI `/lsp baseline` 同源） |

### 5.2 后端 API 契约（新增 `backend/cmd/aicli/commands/web_lsp_handlers.go`）

| 端点 | 返回 | 降级语义（对齐 `web_analysis_handlers.go:38-42`） |
| --- | --- | --- |
| `GET /web/api/lsp/status` | 池状态 + 配置 + `available` | 池未启用：200 + `enabled=false`（**不是错误**，页面显示"LSP 未启用"与开启指引） |
| `GET /web/api/lsp/overview` | §3.3 指标读数 + `degraded` 标记 + `schema_version` | M2 前：200 + `available=false` + 原因码（不伪造 0） |
| `GET /web/api/lsp/events` | `{events:[…], next_cursor}` | ring 空：200 + 空数组 |
| `GET /web/api/lsp/baseline?days=14` | §4.3 报告 `{available, days, rows[], digest, scan, cached_at}` | 日志根不存在：200 + `available=false` + `lsp_baseline_no_log_root`；`days` 非法：400 `lsp_baseline_invalid_days`；未采集项由归因包输出 n/a（不伪造 0） |
| `GET /web/api/events`（既有 SSE） | 增加 `lsp.*` → SSE 映射 | `web_schema.go:135-173` 加映射分支 |

挂载：`cmd/aicli/pprof.go:442-446` 同款方式追加 `commands.ChatWebAPILSPPath`（`+"/"` 前缀匹配）。鉴权与 token 自举沿用现有 web 中间件。

### 5.3 前端模块

| 文件 | 改动 |
| --- | --- |
| `web/js/lsp.js` | 页签渲染 + fetch + 自动刷新 + 空态/降级/错误态（照 `analysis.js` 结构） |
| `web/app.js` | 注册模块与页签路由 |
| `web/index.html` | 菜单项「LSP 观测」（帮助菜单下，与"缓存分析/用量分析"并列） |
| `web/style.css` | 卡片/表格/状态色（复用既有变量） |

### 5.4 页面验收

1. 池状态与实际进程一致（对照 `Get-Process gopls` 与 `/lsp status`，同源 `LSPStatuses()`）；
2. 未启用 LSP 的会话：页面显示"未启用"，不报错、不显示 0 指标；
3. M2 前 `overview` 返回 `available=false` 时不得渲染假数据；M2 后能看到真实 `lsp.*` 事件；
4. 会话切换后旧快照失效（与 `analysis.js` 的会话感知约定一致）；
5. 后端 handler 有 Go 单测（形状/降级码/参数归一），页面不纳入自动化测试（micro web client 既有约定）。

### 5.5 `frontend/` React 应用（舰队级视图，次要目标）

面向 runtime-server 的跨会话 LSP 面板，挂在用量分析页现有观测面板组（`frontend/src/pages/usage-analytics/`，已有 `tool-stats-panel.tsx`、`routing-observability-panel.tsx` 模板）：

- 新增 `frontend/src/api/runtime/lsp.ts`（消费 `/api/runtime/analytics/lsp/*` 或 observe `/api/runtime/observe/v1/events?type=lsp.*`；前端目前尚无 observe 客户端，需新建）；
- 新增 `pages/usage-analytics/lsp-observability-panel.tsx` + i18n（zh-CN/en-US）；
- 数据来源与 micro client 同一 §3.3 口径，避免第二套数字。

### 5.6 实现记录（2026-09-29，MVP）

- **后端**：`cmd/aicli/commands/web_lsp_handlers.go`（`/web/api/lsp/status|overview|events`，只读；池状态与 TUI `/lsp status` 同源）+ 前缀常量 `ChatWebAPILSPPath`（`web_schema.go`）+ 路由挂载（`pprof.go`）+ `/debug/endpoints` 清单条目（`chat_debug_endpoints.go`）。
- **前端**：`web/js/lsp.js`（池状态 / 使用读数 / 最近事件 / 优化建议四区，15s 自动刷新、会话感知、`no-store` 拉取、诚实降级）；`web/index.html`（页签 + 面板 + 帮助菜单项 + 关于页签列表）；`web/js/ui.js`/`menu.js`/`app.js` 接线。
- **测试**：`web_lsp_handlers_test.go` 覆盖未启用降级 / `available=false` 且 `metrics=null`（不伪造 0）/ 事件空数组 / 405 / 404 / 前端接线；回归 `TestHandleChatWebPage*` 全绿（`go test ./cmd/aicli/commands/ -run "TestChatWebLSP|TestHandleChatWebPage"`）。
- **当前能力边界**：池状态为真实数据（server/state/pid/编码/原因 + 缺失二进制与崩溃的优化建议）；`overview`/`events` 在 M2 埋点落地前返回 `available=false, reason=lsp_instrumentation_pending`，前端已按契约显式识别，**埋点落地后无需再改前端即可点亮**。

### 5.7 实现记录（M1/M2，2026-09-29）

- **M1 埋点（`internal/lsp`）**：`EventRequest` 新增请求级字段（trigger/outcome/duration_ms/diag_count/appended_bytes/omitted_*）；`Metrics`（`metrics.go`）聚合触发次数、结果分桶、命中率、降级率、P50/P95（512 样本环）与最近 64 条明细；`Bridge` 在内联（`AppendToResult`）与工具（`Report`）两条路径上按 `classifyOutcome` 记录并投递事件；`Bridge.MetricsSnapshot()` 对外读数。
- **M1 host 接线（`internal/tools`）**：`Manager.LSPMetrics()`；`lspObserverHub` + `Manager.SetLSPObserver` 提供**可后置注入**的转发槽（会话 runtime host 晚于工具管理器构造，构造期与 late attach 两条挂池路径共用同一 hub）。
- **M2 观测基座**：`internal/events/lsp_events.go` 三个事件常量；`contract.go` 登记为 **B 通道 live-only**；`runtimeobserve` 白名单、字段投影（仅标量 + 短枚举，path/reason/诊断正文不入载荷）与 `known_types` 目录同步；`filtered_by_type` 上界 128→160（目录规模 131，`TestKnownEventTypeCatalogInvariants` 通过）。
- **M2 事件发布**：`internal/lsp/eventbridge` 把池事件投影为 `lsp.request.finished` / `lsp.server.state` / `lsp.diagnostics.updated`（两个宿主共用同一份投影）；aicli 侧由 `cmd/aicli/commands/chat_lsp_events.go` 发布到会话 EventBus，接线点 `chat_setup.go`（`session.LocalRuntimeHost` 建立后立即注入）。
- **web 页面点亮**：`/web/api/lsp/overview` 在池已启用时返回 `available=true` + 真实 `metrics`（lsp.MetricsSnapshot 单一来源）；`/events` 返回最近请求明细（最新在前）。**前端无需再改**：池挂载后自动显示真实读数与优化建议。
- **验证**：`go test ./internal/lsp/ ./internal/runtimeobserve/ ./internal/tools/ -run ...`、`go test ./internal/events/`、`go test ./cmd/aicli/commands/ -run "TestChatWebLSP|TestHandleChatWebPage"` 全绿；`go build ./cmd/aicli/...` 通过。
- **runtime-server 接线（补记）**：新增共享投影包 `internal/lsp/eventbridge`（aicli chat 与 runtime-server 共用一份投影，避免两处漂移）；`lsp.Bridge` 增加 `SessionIDFromContext`（接 `toolctx.SessionID`——agent loop 已为每次工具执行注入会话 id）与 `Event.SessionID`；`AgentAdapter.SetLSPObserver` 透传；`runtimeapi.NewHandler` 装配时 `attachLSPObservation`（池事件 → Handler 运行时事件总线）。runtime-server 的进程级共享工具管理器由此把 LSP 事件**按执行上下文归属到具体会话**；无执行上下文的池生命周期事件作为无会话事件进入 observe 流。
- **profile 级接线（补记）**：`runtimeapi.wireLSPObservation` 同时覆盖 `resolveProfileMCPAdapter` 新建的临时工具管理器，runtime-server 各条工具面路径的池事件都进同一总线。
- **M4 数据入口（补记）**：`lsp.request.finished` 挂 A 通道落盘（含 session_id），配合 `scripts/analyze-lsp-baseline.py`（`--selftest` 自验、`--days/--since/--root` 窗口过滤、`--json/--out` 输出）直接产出 §4.3 六项指标与事实明细。真实库冷跑：1432 个 runtime-events.jsonl / 49.5 万行 / 27.7s，编辑回执 4751 次（校验 `logical_tool` 与 `output_model_visible_bytes` 口径）。
- **基线产品化（补记）**：新增 `internal/lsp/baseline`（归因 + §4.3 报告渲染，与脚本同 fixture 互锁）、`/lsp baseline` 子命令（screen+read 只读长文档，忙时不阻塞）与页签 F 区块 `GET /web/api/lsp/baseline`（跨会话，服务端 10 分钟 TTL 缓存兜住 15s 自动刷新；日志根缺失 → 200 + `available=false` + 稳定原因码）。默认 14 天窗口按文件 mtime 整文件跳过；真实库全量 Go 扫描 7.9s（冷）、单测 0.65s。
- **遗留**：仅剩运行期积累——部署后收集 ≥2 周数据，回填 §4.3 并固化阈值（ADR-0003 D4）。
- **真实链路缺陷与修复（补记 2）**：为「事件发布 → A 通道落盘」补回归测试时发现——bridge 的 `shouldSuppressMismatchedPrimaryTurnEvent` 对**无 turn_id 的会话级事件**在 `runActive && activeTurnID != ""`（即正常轮次进行中）时一律丢弃，`lsp.request.finished` 因此永远进不了 `runtime-events.jsonl`，M4 基线与页签 F 会静默显示 n/a（与 `agent.reclaimed` 曾整批丢失同一失败模式）。修复：`isLSPObservationBusEvent` 豁免归属门，并把三个 `lsp.*` 类型加入 `isChatRenderDataPlaneSuppressedEvent`（只进事件日志/observe，不进 Scene 消息流）。回归：`TestChatRuntimeEventBridge_PersistsLSPRequestFinished`（生产形态：轮次进行中 + 已识别 turn）、`TestChatRuntimeEventBridge_TurnOwnershipKeepsSuppressingStaleTurnEvents`（豁免不得削弱过期轮次抑制）、`TestLSPEventChannelsPinBaselineStorage`（通道表态钉死）。

### 5.8 优化落地记录（2026-10-01，基于 490 请求真实基线）

首次真实数据回填暴露的问题与对应修复（数据快照：8 会话 / 490 请求，fallback 54.3%，
gopls 有 228/324 请求打满 `wait_ms`；详见本轮分析报告）：

| 问题（数据证据） | 修复 | 验证 |
| --- | --- | --- |
| 空诊断（clean 107 次）与 no_fresh（121 次）全部打满 1s | ① `ServerSpec.EmptyPublishConclusive`（gopls 预设 true）：带版本号的空发布视为结论；② `diagnostics.empty_confirm_ms`（默认 150ms）防中间空集；③ `diagnostics.start_wait_ms`（默认 250ms）：冷启动不再烧满预算 | `TestEmptyPublishEarlyAcceptWithConfirmWindow`（<600ms）、`TestEmptyPublishConservativeWithoutServerOptIn`（不误判 rust-analyzer） |
| 重启预算耗尽后整场会话静默降级（rNo0YL1N 会话 106 次） | `lsp.restart_window`（默认 10m）滑动窗口：静默期后恢复预算；崩溃 reason 增加 stderr 尾因；事件新增 `reason_category` | `TestRestartWindowResetsBudget`；`TestReasonCategory` |
| 降级提示重复 266 次（~47KB 噪声） | `diagnostics.hint_once`（默认 true）：同一 (server, reason) 只解释一次；`lsp_diagnostics` 显式调用不受限 | `TestHintOnceDedupesInlineAppend` |
| 埋点缺 `tool_call_id`/`turn_id`/fingerprint（M1 验收项） | toolctx 增加 ToolCallID/TurnID 键并由 agent loop 注入；请求事件带 `tool_call_id`/`turn_id`/`path_fingerprint`/`diag_fingerprint`；发布事件带 `version`/`diag_fingerprint` | `observer_join_test.go`、`context_join_test.go` |
| fallback 分母含 no_server（被稀释）；追加字节无法区分诊断/噪声 | `attempted` 单列（fallback 分母改为排除 no_server）；追加字节拆分 `appended_diag_bytes`/`appended_note_bytes`/`appended_empty_bytes` | `TestMetricsSnapshotAggregates` |
| `docs` 常驻全文、`didClose` 无调用方；每次全量 didChange | `lsp.max_tracked_docs`（默认 128）LRU + didClose；增量同步发送单区间替换（rune 对齐，编码边界复用既有转换） | `TestDocumentLRUEvictionSendsDidClose`、`TestIncrementalDidChangeSendsMinimalRange` |
| 能力协商矛盾（发 workspaceFolders 却声明 false） | capability 置 true | 现有握手测试 + 真机冒烟 |
| 多 server 共享同一 deadline（慢者饿死后写 server） | 每个成员独立预算 | 现有桥接测试全绿 |
| `MaxChars` 按字节计（中文偏严）；空结果块 171B/次 | 预算改为 rune 计数；空结果输出自闭合单行 | `internal/lsp` 全量测试 |
| `diagnostics.updated` 1653 条 vs 请求 490 条 | 按诊断集合指纹去重：集合未变不再发事件 | `TestDiagnosticsEventDedupedByFingerprint` |
| `lsp_closure_ratio` 长期 n/a（无法验证闭环） | path/diag fingerprint 落地；Go 基线包与 Python 脚本按「同会话同文件下一次编辑为 clean」计算闭环率 | Go/Python 同 fixture 互锁（closure 1.0） |
| 冷视图首次分析超预算（实机：backend 视图重启后 ~85s 才发布；期间内联编辑连续 no_fresh） | ① `diagnostics.cold_start_grace_ms`（默认 1500，负值关闭）：连接**从未发布过任何诊断**时，对每个路径授予一次有界延长（之后与稳态一致，不拖慢被忽略目录的重复编辑）；② 启动中降级后**后台预热**：服务就绪即 didOpen+didSave，模块视图立即开始加载，不再等下一次编辑；③ 池状态新增 `first_publish_ms`（启动→首个发布的延迟）作为冷启动观测 | `TestColdGraceLetsLateFirstPublishLand`、`TestColdGraceGrantedOncePerPath`、`TestPrewarmOpensDocumentAfterColdStart`、`TestClientStatusReportsFirstPublishMS` |
| "空发布提前采信"存在假 clean 风险（历史事件扫描：同版本 empty→non-empty 8 次、Δ=1.3–7.9s，且**全部**发生在"该版本先出现非空诊断"之后） | 护栏：同一版本文档出现过非空诊断后，空发布不再作为结论性 clean（提前采信与 settle 两处同时收紧，诚实降级为 no_fresh）；新增 `empty_accept_superseded` 计数（服务器状态）作快路径回归守卫 | `TestEarlyAcceptSkippedAfterProblemsForSameVersion`、`TestSettleRefusesChurnEmpty`、`TestFalseCleanCounterCountsSupersededEmpty`；快路径旧用例不回归 |
| 冷视图加载期（实机 ~85s）内每次编辑都付满预算（1s/次），单轮多编辑累计数秒 | 冷路径快速失败：**宽限已授予 + 连接从未发布 + 该路径无快照**（强信号，避免误判热连接偶发慢分析）才标记"已知冷"，后续编辑只等 `diagnostics.cold_retry_ms`（默认 250）；该路径首个发布即清除标记并恢复常规路径（含空发布提前采信） | `TestColdRetryAfterGraceTimeout`（首编辑 ~600ms 宽限预算 → 次编辑 ~100ms 快速降级 → 发布后恢复 fast clean）；`TestColdGrace*` 不回归 |
| 请求事件缺 `reason_category`（只有 server-state 事件有；projector 白名单与方案 §3.1 均已按请求级字段设计）→ 基线无法按原因细分，"从未发布"（模块外/忽略目录）与"普通超时"（有旧快照）不可区分，阻碍快速失败启发式的证据化调参 | ① `ReasonCategory` 新增 `no_publish`（"published nothing" 优先于 wait_timeout）；② `Event`/`RequestRecord` 增 `ReasonCategory`，Bridge 在请求事件上计算并下发；③ eventbridge 落盘请求事件 `reason_category`（低敏短枚举，自由文本 reason 仍不出进程）；④ Go/Python 基线新增"降级原因分布"事实行 | `TestReasonCategory`（no_publish 用例）、`observer_join_test`（请求 payload 带 reason_category）、`TestNoPublishReasonIsActionable`（MetricsSnapshot 记录为 no_publish）、Go/Python fixture 互锁新增 degrade_reasons 断言 |
| 编辑覆盖率疑似 12% 缺口（855 编辑 vs 753 请求） | 事件级配对审计（离线脚本，按会话+`tool_call_id`）：**新构建会话 42/45 配对（93%），0 个请求没有对应编辑**；缺口来自旧构建事件无 `tool_call_id`（786/828）+ 少量幂等/失败编辑（已文档化的预期行为）——**结论：无需修复**，避免后续重复怀疑 | 审计脚本（同 §4.3 口径：`tool.completed` × `lsp.request.finished` 配对） |
| 缺二进制/崩溃/传输关闭被折叠成裸 `degraded`（live：pyright 缺二进制 4 条与崩溃无法区分，基线与告警不可行动） | `classifyOutcome` 改为复用 `ReasonCategory`（outcome 与 reason_category 共用单一事实源，防漂移）；新增 `degraded_binary_missing` / `degraded_crashed` / `degraded_transport_closed` / `degraded_canceled`；裸 `degraded` 只兜底真正未知的原因 | `metrics_test` 分类表新增 4 例；`bridge_test` 缺二进制端到端断言（dial 报 not found → `degraded_binary_missing`） |
| rust-analyzer 真机冒烟在 Windows 偶发红灯（TempDir 清理 sharing violation） | 显式工作区目录 + 带重试的清理：`Stop` 已等 `cmd.Wait`（ShutdownTimeout），但句柄释放可能再滞后数毫秒（ADR-0005） | 连续两轮 `go test ./internal/lsp/...` 全绿（含真机用例） |
| `first_publish_ms` 只存在于池状态（live-only）：首个发布不改变状态，`setStatus` 不会发事件 → **从未落盘**，跨会话基线拿不到冷启动延迟（方案 §5.8 第三轮只做了观测面） | ① 首个发布时显式补发一条 `lsp.server.state` 事件（带 `first_publish_ms`，锁外发送）；② eventbridge 落盘该字段、projector 白名单同步；③ Go/Python 基线新增 `lsp_cold_first_publish_p95` 行（按 (session, server) 取首个发布；未采集输出 n/a） | `TestClientStatusReportsFirstPublishMS`（观察者收到带 `first_publish_ms` 的状态事件）、Go/Python fixture 互锁新增冷启动断言 |
| 真机冒烟 `TestRealRustAnalyzerRoundTrip` 在握手中被快速失败拦下（`start_wait_ms` 默认 250ms，实测 rust-analyzer 握手可超 250ms） | 用例显式放宽 `StartWaitMS`（该用例断言完整往返；快速失败已有专门单测 `StartWaitMS=50`）；**默认值不动**——live 证据里 gopls 握手通常在 250ms 内（`degraded_starting` 仅 2 次），等 rust-analyzer 会话的 `degraded_starting` 数据再决定是否调默认 | 连续两轮 `go test ./internal/lsp/...` 全绿 |

新增配置键（全部有内置默认，不改代码即可调整）：
`diagnostics.start_wait_ms` / `diagnostics.empty_early_accept` / `diagnostics.empty_confirm_ms` /
`diagnostics.hint_once` / `diagnostics.cold_start_grace_ms` / `diagnostics.cold_retry_ms` / `lsp.restart_window` / `lsp.max_tracked_docs` /
`servers[].empty_publish_conclusive`。

**实机验证（第一轮，2026-10-01 07:31–07:34，进程 59697 / 会话 QJDrIzkp）**：
- clean 快路径实测 **162ms / 273ms / 377ms / 422ms**（旧基线同场景为 1000ms）；注入 221ms
  （`diag_fingerprint=701f…`、`appended_diag_bytes=244`）；修复后 clean 且闭合成对。
- 缺二进制提示：首次 201B，第二次同因编辑 **0B**（去重生效）；空结果块 117–128B（自闭合）。
- 落盘字段全量核对：`lsp.request.finished` 含 `tool_call_id`/`turn_id`/`path_fingerprint`/
  `diag_fingerprint`；`lsp.server.state` 含 `reason_category`；`lsp.diagnostics.updated`
  含 `version`/`has_version`/`diag_fingerprint`/`path_fingerprint`，且仅集合变化时发布。
- 基线脚本对同一会话复算 `lsp_closure_ratio = 1.0000`（eligible 1 / closed 1）。
- 实测发现：点目录（`.tmp`）与未加载的独立模块内文件，gopls 不会发布任何诊断（Go 工具链
  忽略 `.`/`_` 目录），此前只会显示笼统的 no_fresh。已修复：`HasSnapshot` 区分"未发布"，
  降级原因追加"server published nothing (file may be outside its module or in an ignored
  directory)"，并新增 `TestNoPublishReasonIsActionable`。

**实机验证（第二轮，2026-10-01 08:16–08:26，重制构建，同会话）**：
- 冷启动短等待生效：首个编辑 `degraded_starting` **250ms**（旧 1000ms）；缺二进制首提示
  201B、第二次同因编辑 **0B**（去重）；热态注入 **4ms**（`diag_fingerprint`+244B 诊断字节）；
  clean 空发布 **150–209ms**、空块 117–128B；注入→修复闭合成对。
- "server published nothing" 新文案实机出现（227B），随后扩展为同时覆盖"首次分析仍在加载
  （retry shortly）"——见下条。
- **新发现（重要）**：多模块目录且仓库根无 go.mod 时，gopls 对**非首个绑定模块**的首次分析
  可能远超 1s 预算：本轮 backend 视图在重启后约 **85s** 才产出首批诊断（机器同时有 5 个
  aicli 会话/多个 gopls 与构建负载），期间内联请求全部 `no_fresh`（无任何 publish 到达，
  故 `HasSnapshot=false`）；一旦视图就绪，显式 `lsp_diagnostics` 150ms、后续内联编辑 209ms
  即恢复 clean。属 gopls 冷加载行为，非池缺陷；调试路径：先显式 `lsp_diagnostics` 触发/确认
  视图就绪，再继续编辑。排查中曾误判为"视图被首个模块绑定"，被显式工具调用证据推翻——
  记录以避免重蹈。

**第四轮优化（2026-10-01，基于历史事件流的假 clean 排查）**：
- 扫描全量持久事件（1804 文件 / 57.6 万行）中带版本号的 `lsp.diagnostics.updated`：
  同版本 empty→non-empty 转换 **8 次**，Δ=1.285–7.855s（P50 4.3s），全部为 gopls，
  且**全部先出现过非空诊断**（重新分析抖动）；未发现"首发布即空、随后非空"的样本。
- 结论：150ms confirm 窗口防不住秒级抖动；真正的护栏是"该版本是否出现过非空"。
  已落地（见上表新行），并用 `empty_accept_superseded` 计数持续度量假 clean。
- 边界：护栏只影响"同一版本等待"的场景（重复写同内容、显式 `lsp_diagnostics` 重查）；
  正常编辑递增版本，快路径不受影响（旧用例全绿）。
- 基线首次回填见 §4.3（754 请求全窗口 + 口径限制）。

**第五轮优化（2026-10-01，冷视图长窗口的重复等待）**：
- 场景：backend 视图冷加载 ~85s；宽限只覆盖一次请求，窗口内每次编辑都付 1s 预算，
  单轮多编辑累计数秒（第二轮的实测轮次即如此）。
- 设计：只用**强信号**触发快速失败——宽限已授予 + 连接从未发布任何诊断 + 该路径无快照——
  避免把热连接的偶发慢分析误判为冷；标记由该路径**首个发布**清除，随后恢复常规路径
  （空发布提前采信仍然生效）。
- 配置：`diagnostics.cold_retry_ms`（默认 250，负值关闭）。
- 取舍：冷窗口内"发布恰好落在快速降级之后"的概率 ≈ 预算/加载时长（~85s 时约 1%），
  代价是那一次编辑少拿一次诊断、下一次编辑即恢复；换回每次编辑 ~750ms。

**第六轮优化（2026-10-01，请求级归因缺口）**：
- 发现：`reason_category` 只挂在 `lsp.server.state` 事件上；请求事件（落盘 → 基线 → observe 平面）
  没有它，而 projector 白名单注释与方案 §3.1 都把它当请求级字段——M1 验收项存在实际缺口。
- 影响：基线只能看到 `degraded_no_fresh` 粗类，"从未发布（模块外/忽略目录）"与"普通等待超时
  （存在旧快照）"无法区分——两者处置完全不同（改工作区 vs 加预算），这也是下一轮
  "暖连接快速失败"能否放宽的判据。
- 落地：见上表新行；自下一构建起落盘。自由文本 reason 仍不出进程（低敏纪律不变）。

**第七轮优化（2026-10-01，覆盖率审计 + outcome 归因细分）**：
- 审计（负结果，价值在止损）：编辑覆盖率 12% 缺口被证伪——新构建会话 42/45 配对、0 个请求缺编辑，
  缺口全部来自旧构建事件无 `tool_call_id` 与幂等/失败编辑。**不做修复**。
- 修复（真缺口）：`classifyOutcome` 的兜底把"缺二进制/崩溃/传输关闭"折叠成裸 `degraded`；
  现改为复用 `ReasonCategory`，两类归因字段（outcome 与 reason_category）从此共用同一事实源。
- 测试稳定性：真机冒烟在 Windows 的 TempDir 清理竞态修复（带重试清理）。

**第八轮优化（2026-10-01，冷启动延迟闭环）**：
- 缺口：`first_publish_ms` 从未落盘（首个发布不改变状态 → 状态事件不会发）→ 基线拿不到冷启动延迟。
- 落地：首个发布补发带 `first_publish_ms` 的状态事件；eventbridge/projector 同步；基线新增
  `lsp_cold_first_publish_p95` 行（未采集 n/a，不渲染成 0）。
- 附带：真机冒烟的 `start_wait_ms` 敏感性修复（用例放宽；默认值等 rust-analyzer 会话证据再评估）。
- 行为备注：内容未变化的 write 会按幂等回放处理且不触发 LSP 请求（无变更不诊断，符合预期）。

**崩溃根因定位（已闭环）**：跨会话同秒崩溃（09-30 10:37:33×3、10:43:04×3）确认为
**整机内存耗尽导致的外部终止**，不是 LSP 池缺陷。证据：Windows 事件日志
`Resource-Exhaustion-Detector`（ID 2004，低虚拟内存）在 09-30 10:35:25/10:39:01、
10-01 06:42/06:56/07:01/07:12 成簇出现；10-01 07:14:46 三个 aicli 进程的 gopls 在
80ms 内同时退出（`exit status 0xffffffff`），Application 日志无 gopls 异常记录（排除
自身崩溃；同日 07:02 的 `exit status 2` 才是 Go runtime fatal）。缓解已落地：重启窗口
（静默期后自动恢复）+ 归因字段（stderr 尾因/`reason_category`）。运维建议：并发会话
避免整仓 `go build ./...`/`go test ./...` 同时开跑，构建用 `-p=1` 或分包执行。

遗留：`lsp_closure_ratio` 与其余读数需要新事件积累后回填 §4.3 阈值（本次实机已可计算）。

---

## 6. 里程碑与验收

| M | 内容 | 交付物 | 验收 |
| --- | --- | --- | --- |
| M1 | LSP 内部埋点 + host 接 Observer（先只进结构化日志，不动白名单） | `internal/lsp` 新事件 + `lsp_bridge.go` 接线 | 一次编辑产出 1 条 request 记录（trigger/outcome/duration_ms/diag_count/appended_bytes），带 tool_call_id |
| M2 | 进 observe 基座：白名单 + 目录 + 契约登记 + 字段投影 + SSE 映射 | 3 个事件类型全链路可查 | `/observe/v1/events?type=lsp.*` 可查；契约/目录门禁测试全绿；`filtered_by_type` 不再吞 LSP 事件 |
| M3 | 读数 + 快照 + **micro web client「LSP 观测」页签** | `ComputeLSPMetrics`、Snapshot.LSP、`web_lsp_handlers.go`、`web/js/lsp.js` | §5.4 五条验收全过；读数与脚本复算一致（同一函数） |
| M4 | 基线采集（≥2 周）→ §4.3 回填 → 优化动作执行与回归 | 基线报告 + 配置/提示词调整 | 至少 1 条 §4.1 决策闭环走完（现象→动作→回归→留痕）；Q1/Q2 结论回填 `docs/lsp/03` |
| M5（可选） | React 舰队视图 + 阈值告警 | `frontend/src/api/runtime/lsp.ts` + panel | 与 micro client 同口径同数字 |

## 7. 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| 事件量放大（diagnostics 高频） | `request.finished` 每次触发至多 1 条；`diagnostics.updated` 按 turn 聚合/采样；ring 条数/TTL 既有策略兜底（`collector.go:322-368`） |
| 观测改动触碰 LSP 热路径 | 观察者回调契约"必须非阻塞"（`observer.go:19-21`）；埋点只做标量记录与计数，不做 I/O |
| 口径漂移（三端各算各的） | 读数函数单一来源（`internal/lsp/metrics.go`），HTTP 只序列化该结构（对齐 `web_analysis_handlers.go:16-24` 的契约只定义一次原则） |
| 上游命名冲突 | 事件名按 `supplement/16` §17 收敛；内部既有 `server.state` 用映射表达，不重命名既有缝 |
| 观测先行、实现落后 | M1 先只写日志，页面 M3 才接；M2 前 `overview` 明确返回 `available=false`，禁止假数据 |
| 隐私 | 只存 fingerprint/标量；路径、诊断正文不进事件载荷；脱敏走既有 `redaction.go` |

---

## 附录 A：埋点前可用的临时对账（已实际执行）

```powershell
# 1) 事件流里 LSP 相关回执/内联块（无类型事件，只能文本匹配）
Select-String -Path "$env:USERPROFILE\.aicli\chat-logs\*\*\*\*\events\runtime-events.jsonl" -Pattern 'lsp_diagnostics|<lsp_diagnostics' -SimpleMatch
# 2) 全局日志中的 LSP 生命周期行（无 session 关联）
Select-String -Path "$env:USERPROFILE\.aicli\logs\aicli.log" -Pattern 'lsp:'
# 3) 进程面（gopls serve + telemetry sidecar 成对出现）
Get-CimInstance Win32_Process -Filter "Name='gopls.exe'" | Select-Object ProcessId,ParentProcessId,CommandLine
```

局限：只有"次数级"结论；无耗时、无命中/降级、无会话与 tool_call 关联。

## 附录 B：关键证据索引

- 触发与管线：`internal/tools/manager.go:168-173`、`internal/lsp/bridge.go:142/181/189-193/199-204`、`internal/lsp/client.go:371/386/415/471/619-621`
- 观测接缝与消费：`internal/lsp/observer.go:8-44/49-71`、`internal/lsp/bridge.go:62`、`internal/tools/lsp_bridge.go:38-39/163/180-189`
- 基座链路：`internal/runtimeobserve/model.go:33-57`、`projector.go:33-58/137-142`、`collector.go:140/190-260/299-345`、`service.go:136-195`、`internal/events/contract.go:77-244`、`contract_test.go:140/157/287`
- 指标读数范式：`internal/supervision/metrics_readout.go:12-27/126`
- micro web client：`cmd/aicli/commands/web/index.html:36-49`、`web/js/analysis.js:1-50`、`web_analysis_handlers.go:38-105`、`cmd/aicli/pprof.go:442-446`
- React 观测面板：`frontend/src/api/runtime/analytics.ts:155-228`、`frontend/src/pages/usage-analytics/`
- 规格锚点：`docs/knowledge_Layer/supplement/16_api_events_telemetry_and_rollout.md:71-74/105-106`、`docs/knowledge_Layer/adr/0003-exploration-attribution-metrics.md:61-91`、`docs/lsp/03-implementation-plan-and-acceptance.md:103-112`
