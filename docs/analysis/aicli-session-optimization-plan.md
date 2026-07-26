# aicli 会话失败分析与优化方案

> 数据窗口：`~/.aicli/chat-logs` 近 75 个会话（约 2026-07-22 后）  
> 辅助样本：近 40 会话效率报告（`tmp/aicli_tool_efficiency_report.json`）  
> 生成目的：把“工具效率 / LLM 失败 / 协作等待 / 可观测性”映射到现有代码路径，给出可落地优化项。

---

## 1. 结论摘要

| 维度 | 现状 | 判断 |
| --- | --- | --- |
| 工具整体 | 近窗 clean metrics：`err_total=4.29%`（hard `2.77%` / soft `1.52%`） | 主路径已可用；剩余失败集中在少数工具与语义误标 |
| 工具热点 | `bash/shell` 批失败与非零退出、`apply_patch` 上下文 miss、`wait_agent` 超时、`spawn_agent` 深度限制 | 大多是 **可恢复业务失败**，但部分被标成 hard fail，拉低效率并诱导重试 |
| LLM | 近窗 `71/3985 ≈ 1.78%` fail；主因 `model_not_found(404)`、`503 CPU overload`、`502`、TLS/流中断 | **配置 + 上游稳定性** 主导；重试后仍失败会直接 `session_end success=false` |
| 会话成功 | `session_end` 近窗约 `success≈145 / fail≈103`（含配置错误与中断） | 工具能恢复，但 **LLM 终态失败 / 补丁 miss / 深度限制** 仍会终结会话 |
| 效率 | 单会话工具调用中位数偏高；`view/bash/grep` 占绝对多数 | 读多改少 + shell 误用 rg/heredoc 造成 token/时延浪费 |

**优先级总览**

1. ~~**P0 上游 LLM**：模型/账号不可用快速失败与可切换；`503/502` 跨 provider 故障转移~~ **（暂缓，属上游/账号配置问题）**  
2. **P0**：`bash/shell` 内容失败 vs 硬失败语义收敛（非零退出、rg 无匹配、batch 部分失败）— **已有主路径；继续压 hard rate**  
3. **P1**：`apply_patch`/`edit` 统一 `STALE_CONTEXT` + suggested view offset；`wait_agent` 超时 next_action 强化；`spawn` depth=`SPAWN_DEPTH_LIMIT`  
4. **P1**：prompt/策略层减少 shell-rg/heredoc、鼓励 toolkit 并行与 `view.files`  
5. **P2**：失败时间线看板、会话级效率指标落盘、审批/协作漏斗可视化

---

## 2. 证据基线

### 2.1 工具（clean metrics，75 sessions）

```
calls≈10687  results≈10811
err_total=464 (4.29%)  hard=300 (2.77%)  soft=164 (1.52%)
```

按工具错误率（min5）：

| tool | total err | hard | 主要原因 |
| --- | ---: | ---: | --- |
| spawn_agent | 39.4% (13/33) | 39.4% | `spawn_depth_limit` |
| wait_agent | 32.1% (9/28) | 0% | `wait_timeout`（soft） |
| shell | 22.1% (25/113) | 0.9% | 非零退出 / batch fail |
| bash | 14.6% (298/2043) | 8.2% | `TOOL_EXECUTION`、batch_or_command_failed、rg/parser |
| apply_patch | 7.2% (24/335) | 7.2% | patch_context_miss |
| grep | 1.8% | 1.8% | path not found / 参数 |
| edit | 1.5% | 1.5% | old_string miss |
| view | 0.5% | 0.5% | path / invalid args |

Top reason 计数：

- `contract:TOOL_EXECUTION` 218  
- `batch_or_command_failed` 111  
- `contract:TOOL_PATH_NOT_FOUND` 27  
- `spawn_depth_limit` 13  
- `wait_timeout` 11  
- `patch_context_miss` 10  

### 2.2 LLM（debug.log 精炼）

```
success=3914 fail=71 rate=1.78%  model≈grok-4.5
provider fails: gegeda≈68, localhost=2, grok.x4188.top=1
```

失败桶：

| 桶 | 占比 | 语义 |
| --- | ---: | --- |
| HTTP 404 model_not_found | 26.8% | 模型未配置到当前账号/组，**不应无限重试** |
| HTTP 503（含 CPU overload） | 21.1% | 上游过载，可退避 + **换 provider/key** |
| HTTP 502 | 16.9% | 网关/上游瞬时故障 |
| context canceled | 9.9% | 用户中断，不重试 |
| TLS verify failed | 9.9% | 环境/证书配置 |
| TCP/stream abort / 524 | ~8.4% | 流式中断 |
| Auth 401 | 4.2% | 凭证问题 |

### 2.3 会话终态

- `session_end` 中 fail 与 LLM 桶高度重合：`model_not_found`、`503`、`502`、TLS、中断。  
- 工具侧终态杀手：`apply_patch` hunk miss、路径不存在、spawn depth。  
- 大量会话虽 `success=true` 但 `recovered_tool_error_count` 可达两位数——说明恢复路径在工作，但成本高。

### 2.4 效率样本（~40 chat-log dirs）

- tool success ≈ 94.9%；LLM success ≈ 98.7%  
- `avg_tool_calls/session ≈ 227`，`tools_per_llm_request ≈ 2.67`  
- 读工具（view/grep/bash）主导；写工具失败更昂贵（edit/apply_patch 重试会再烧一轮 LLM）

---

## 3. 根因 → 代码路径映射

### 3.1 LLM 重试与故障转移

| 现象 | 代码路径 | 问题 |
| --- | --- | --- |
| 404 model_not_found 仍走重试环 | `backend/internal/llm/gateway_client.go` `Call` 循环；`retry_policy.go` `classifyRetryableLLMError*` | 模型不存在属于 **请求/配置错误**，跨 attempt 同模型重试浪费时延 |
| 503 CPU overload 后同 provider 反复撞 | `ResourceManager.SelectResource` + `RecordResult`（`balancer.go` 接口；宿主实现） | 健康反馈依赖宿主；runtime 侧缺 **error_code → 排除 key/provider** 的强约束 |
| 参数不兼容可降级 | `agent/loop.go` `downgradeUnsupportedProviderRequest` | 已有 parallel_tool_calls/reasoning/temperature 降级；**model_not_found 无等价“换模型/换组”** |
| 终态 next_action | `retry_policy.go` `FailureDiagnostic` / `llmFailureNextAction` | 对 `UPSTREAM_UNAVAILABLE` 已有文案；对 model_not_found 需固定为 **switch model/provider，do not retry unchanged** |

### 3.2 Shell / Bash 语义

| 现象 | 代码路径 | 问题 |
| --- | --- | --- |
| 非零退出被标 hard | shell toolkit / bash adapter + tool result contract 导出 | 策略已要求“进程非零=内容结果”，日志里仍大量 `ok:false`/`TOOL_EXECUTION` |
| `rg` 无匹配 exit 1 | shell 内直接 rg | 与 toolkit `grep` 空结果契约不一致，诱导模型“搜索失败→换写法” |
| PowerShell heredoc/`&&` | prompt + shell 执行器 | Windows 上 ParserError；应在 **参数校验/预检** 拦截并 next_action |
| batch 一条失败整批 failed | bash batch 聚合 | soft 语义未统一：应用 `partial_failure` + 成功命令输出仍可用 |

### 3.3 补丁与编辑

| 现象 | 代码路径 | 问题 |
| --- | --- | --- |
| apply_patch context miss | patch 工具实现 + 错误文案 | 错误已提示 re-view；缺 **自动附带文件片段 / 建议 @@ 上下文** |
| edit old_string miss | edit 工具 | 与 apply_patch 重复失败模式；应统一 `STALE_CONTEXT` error_code |

### 3.4 多 Agent 协作

| 现象 | 代码路径 | 问题 |
| --- | --- | --- |
| wait_agent 超时 | `toolbroker/types.go` `FinalizeAgentWaitResult` | 已设 `next_action=continue_independent_work_before_waiting_again`；模型仍可能忙等 |
| spawn depth limit | spawn_agent 深度校验 | hard fail 正确；缺 **降级为本地完成 / 使用 spawn_team** 的 next_action |
| approval 环 | approval_requested/resolved 事件 | 样本显示 shell 审批可被放行；子 agent 等待审批时父侧需强制 `resolve_agent_approval` |

### 3.5 可观测性缺口

- chat JSON 中 tool_result 形态不完全统一（contract 包装 vs 原始 result），导致多份分析脚本口径漂移。  
- 缺统一 **失败时间线**（LLM fail / tool hard fail / wait timeout / session_end）导出。  
- `recovered_tool_error_count` 有用，但未进入日常 dashboard。

---

## 4. 优化方案（按优先级）

### P0-1 模型不可用与上游过载：快速失败 + 故障转移

**目标**

- `model_not_found` / `unsupported model`：**单次判定后停止同模型重试**，`error_code=MODEL_NOT_AVAILABLE`，`retryable=false`，`next_action=switch_model_or_provider`。  
- `503 system_cpu_overloaded` / `502`：计入 `RecordResult` 并在 `RetryInfo` 中排除当前 key/provider，优先换资源。  
- 用户中断：`USER_CANCELLED`，禁止自动续跑。

**改动点**

1. `backend/internal/llm/retry_policy.go`  
   - 识别 `model_not_found` / `not supported by any configured account` → 非重试。  
   - `llmFailureNextAction` 增加对应分支。  
2. `backend/internal/llm/gateway_client.go`  
   - 非重试错误立即返回，不消耗剩余 attempt。  
   - 对 overload/502 保证 `TriedProviders`/`TriedAPIKeys` 写入后再 `SelectResource`。  
3. 宿主 `ResourceManager`（ai-gateway loadbalancer）  
   - `RecordResult` 对 404-model / 401 / TLS 标记不健康时长策略不同（配置错误 vs 瞬时）。  
4. aicli 配置校验（启动或首次 call 前）  
   - 校验 `provider+model` 是否在可用账号组；失败给出本地可读错误。

**验收**

- 注入 model_not_found：attempt=1 结束，session 错误码稳定。  
- 注入 503 后第二次选择不同 provider/key（单测 mock ResourceManager）。  
- 失败诊断 JSON 含 `error_code/retryable/next_action`。

### P0-2 Shell 内容失败语义收敛

**目标**

- 进程启动成功且仅 exit≠0：**outcome=success（或 content_success）**，`ok=true` 或至少 **非 hard fail**；把 exit code/stdout 当内容。  
- `rg` 无匹配：若仍走 shell，映射为 empty content，不进 `TOOL_EXECUTION`。  
- batch：`partial_failure` + 分命令状态；仅 launch/timeout/cancel/permission 为 hard。  
- 预检：检测 bash heredoc、不可靠 `&&` 链、把 toolkit 名当 shell 命令 → `TOOL_INVALID_ARGS` + 明确 next_action。

**改动点**

1. shell/bash 结果规范化层（toolbroker 或 shell toolkit）统一 `normalizeShellOutcome`。  
2. contract 导出：避免把 content exit 再包一层 `Tool execution failed`。  
3. system/tool guidance 已有 Windows/pwsh 约束；在 **schema description / runtime hint** 重复关键反例（heredoc、shell rg）。

**验收**

- `rg pattern` 无匹配不再计入 hard fail 指标。  
- `go test` 失败 exit=1 计 soft/content，不抬高 hard rate。  
- hard rate 目标：shell/bash hard **< 3%**（当前 bash hard 8.2%）。

**状态（2026-07-25）**

- 非零退出 / batch 混合 / rg no-match 已走 content/empty success 主路径。  
- Windows heredoc、path-position shell glob、shell 调用 toolkit 名：预检拦截并统一 `error_code=TOOL_SHELL_COMPAT`、`retryable=false`。  
- hard fail（超时/取消/权限/缺失可执行/启动失败）现打结构化 `error_code`（`TOOL_TIMEOUT` / `AGENT_RUN_CANCELED` / `AGENT_PERMISSION` / `TOOL_SHELL_COMPAT` / `PROCESS_START_FAILED` / 兜底 `TOOL_EXECUTION`），并保留 partial stdout。  
- 历史 chat-logs 仍可能显示 `TOOL_EXECUTION`；新会话应以结构化码 + content success 为主。

### P1-1 apply_patch / edit 上下文失效

**目标**

- 统一 `error_code=STALE_CONTEXT`（或保留 TOOL_EXECUTION 但加 `failure_class`）。  
- 错误 payload 附：`file_path`、最近可读 hint、建议 `view` offset。  
- prompt：context miss 后 **禁止连打同一 patch**；先 view 再缩小 hunk。

**改动点**

- patch/edit/multiedit 工具错误结构。  
- agent tool result 渲染，确保模型看到“当前文件片段”而非仅失败句。

**验收**

- 故意 stale patch：一次失败后模型下一工具为 view/grep 的比例上升（评测集）。  
- apply_patch hard rate 目标 **< 4%**。

**状态（2026-07-25）**

- `apply_patch` / `edit` 已输出 `error_code=STALE_CONTEXT`、`failure_class=stale_context`、`retryable=false`。  
- `edit` / `multiedit` 现补齐 `suggested_view_offset` / `suggested_view_limit`（与 apply_patch 一致）。  
- `multiedit` 全失败（old_string 全 miss）现与 edit 同一错误码，不再落回裸 `TOOL_EXECUTION`。  
- 历史 chat-logs 若早于该改动，看板仍可能以 message 启发归类 `patch_context_miss` / `edit_old_string_miss`，`error_code` 列会显示 `TOOL_EXECUTION`；新会话应以 `STALE_CONTEXT` 为主。

### P1-2 wait_agent / spawn 协作

**目标**

- 超时结果强制暴露 `next_action` 与 `pending` 列表（已有 Finalize）；在 tool description 与结果首行重复。  
- depth limit：`next_action=complete_locally_or_use_spawn_team`，`retryable=false`。  
- 父 agent 见 `waiting_approval`：优先 `resolve_agent_approval`，禁止空转 wait。

**改动点**

- `toolbroker/types.go` Finalize 已较完善；补 spawn 错误分类。  
- multi-agent prompt / tool schema 文案。  
- 可选：wait 超时默认缩短 + 要求并行推进独立任务。

**验收**

- 超时后重复纯 wait 的比例下降。  
- depth limit 会话不再无意义重试 spawn。

**状态（2026-07-25）**

- `FinalizeAgentWaitResult` 超时/ready/pending 已带 next_action；`waiting_approval` 现扩展为含 `resolve_agent_approval` + id/request_id 的可执行指引。  
- spawn depth 预检改为 `runtimeerrors.ErrAgentSpawnDepthLimit`（`[SPAWN_DEPTH_LIMIT]`），broker/agent 记录路径可结构化 `error_code`；message 启发分类仍作兜底。  
- tool schema 文案已声明 depth 不可盲重试；父会话 prompt 已要求 approval 优先 resolve。

### P1-3 效率：读路径与并行

**目标**

- 同轮多文件：`view.files` / `grep.patterns+paths` / `shell.commands`。  
- 禁止 shell 版全库 rg；默认 toolkit grep。  
- 大文件强制 offset/limit，减少重复 view 全文件。

**改动点**

- tool guidance / skill 文本。  
- 可选 runtime soft-warning：检测连续 N 次同 path 全量 view。

**验收**

- `tools_per_llm_request` 不上升的前提下，`avg_tool_calls/session` 下降 10%+（同类任务对比）。  
- shell 中 `rg` 调用占比下降。

**状态（2026-07-25）**

- parallel / shell guidance 已强调 `view.files`、`grep.patterns+paths`、`shell.commands`；并补大文件 `offset/limit` 与禁止反复全量 view。  
- `view` 截断结果现带 `suggested_next_offset`；默认窗口（offset=0, limit>=2000）且仍 truncated 时追加 soft `[efficiency]` 文案与 `efficiency_advisory=prefer_offset_limit`（不阻断执行）。  
- 通用 semantic-repeat advisory 已存在于 agent loop；本项聚焦 view 大窗口效率提示。

### P2 可观测性与看板

**目标**

- 固定脚本输出：失败时间线、按小时/会话的 LLM&工具失败、协作漏斗。  
- 统一字段：`error_code`、`outcome`、`retryable`、`next_action`、`provider`、`model`、`tool_name`。  
- 每次 release 或夜间扫描 `~/.aicli/chat-logs` 生成 JSON+Markdown。  
- 兼容 **flat** 与 **`YYYY/MM/DD/<session_id>`** 两种 chat-logs 布局。

**交付物**

- `tmp/chatlog_common.py`：会话目录发现（flat + date partition）  
- `tmp/aicli_failure_timeline_dashboard.py`：失败时间线 + hard/soft + error_code + 协作漏斗 + 效率快照  
- `tmp/analyze_aicli_tools_v2.py`：效率报告（已接入同一会话发现）  
- 后续可迁到 `scripts/` 并接入 CI 工件

**状态（2026-07-25）**

- 看板已输出：`tool_error_codes`、`collab_funnel`、`efficiency`（含 hard/soft rate）、live 对齐 `tool_efficiency`。  
- 会话扫描已覆盖 date-partition 新布局，避免只扫到顶层 `2026/` 目录噪声。

---

## 5. 分阶段落地计划

### Phase A（1–2 天）— 止血

1. LLM：`model_not_found` / 401 / TLS 非重试分类 + 单测。  
2. Shell：content exit 与 hard fail 分界单测 + 指标脚本按 hard/soft 分开报。  
3. 跑 dashboard，确认 404/503 桶与 bash hard 变化可观测。

### Phase B（3–5 天）— 恢复质量

1. apply_patch/edit stale context 结构化错误。  
2. wait/spawn next_action 与审批路径提示强化。  
3. prompt 侧 Windows shell 反模式预检。

### Phase C（1 周内）— 效率与宿主协同

1. 与 ai-gateway ResourceManager 对齐 overload 冷却与 key 轮换。  
2. 启动期 model/provider 预检。  
3. 效率基线入库：每次分析输出 `tmp/aicli_tool_efficiency_report.json` + timeline。

---

## 6. 目标指标（近 75 会话窗口复测）

| 指标 | 当前 | 目标 |
| --- | --- | --- |
| tool hard fail rate | 2.77% | ≤ 1.5% |
| bash hard fail rate | 8.2% | ≤ 3% |
| apply_patch hard rate | 7.2% | ≤ 4% |
| wait_agent soft timeout | 32% | ≤ 20%（或超时后无空转） |
| LLM fail rate | 1.78% | ≤ 1.0%（配置修复后） |
| session_end fail 中 model_not_found | 高 | 启动预检拦截 ≥ 80% |
| avg tool calls / session（同类任务） | ~227（混杂） | 相对基线 -10% |

---

## 7. 风险与边界

- **不要**把真实测试失败（编译错误、断言失败）改成“成功欺骗模型”；只调整 **outcome/ok/error_code 语义**，stdout 必须保留。  
- 故障转移依赖宿主 ResourceManager；runtime 只能保证 RetryInfo 与 RecordResult 契约。  
- 日志口径不统一时，先修导出 contract，再改模型策略，避免“指标变好但行为不变”。  
- 用户中断与配额耗尽必须保持 `retryable=false`。

---

## 8. 配套命令

```powershell
# 失败时间线 + 看板 JSON/MD（含 hard/soft、error_code、协作漏斗、效率快照）
python tmp/aicli_failure_timeline_dashboard.py
python tmp/aicli_failure_timeline_dashboard.py --limit 80 --hours 0

# 会话发现单测
python -m unittest tmp/test_chatlog_common.py

# 已有效率/深度分析（仓库内临时脚本；已支持 YYYY/MM/DD 分区）
python tmp/analyze_aicli_tools_v2.py --limit 60
python tmp/final_metrics.py
python tmp/analyze_aicli_errors_deep.py
```

输出默认：

- `tmp/aicli_failure_timeline.json`  
- `tmp/aicli_failure_dashboard.md`  
- `tmp/aicli_tool_efficiency_report.json`（效率脚本）

---

## 9. 附录：优先改动文件清单

| 文件 | 动作 |
| --- | --- |
| `backend/internal/llm/retry_policy.go` | model_not_found / overload 分类与 next_action |
| `backend/internal/llm/gateway_client.go` | 非重试快返；Tried* 填充 |
| `backend/internal/llm/*_test.go` | 覆盖 404/503/cancel |
| shell/bash toolkit + toolbroker 结果规范化 | content vs hard |
| apply_patch / edit 错误结构 | STALE_CONTEXT |
| multiedit 全失败 | STALE_CONTEXT + suggested_view_offset |
| `backend/internal/toolbroker/types.go` | spawn/wait 错误 next_action（如需） |
| aicli tool guidance / system hints | Windows/rg/并行 |
| `tmp/chatlog_common.py` | 会话目录发现（flat + date partition） |
| `tmp/aicli_failure_timeline_dashboard.py` | 持续观测：时间线 / hard-soft / 协作漏斗 / 效率 |
| `tmp/analyze_aicli_tools_v2.py` | 效率报告（与 dashboard 共用会话发现） |

---

*本方案基于本地 chat-logs 与现有 runtime 代码静态映射，实施时以单测 + 复跑 dashboard 为门禁。*
