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
| LLM | 近窗 `71/3985 ≈ 1.78%` fail；主因 `model_not_found(404)`、`503 CPU overload`、`502`、TLS/流中断；近窗亦见 `thinking.adaptive.effort` 400 | **配置 + 上游稳定性** 主导；runtime 侧 adaptive thinking wire 误嵌套 effort 已修 |
| 会话成功 | `session_end` 近窗约 `success≈145 / fail≈103`（含配置错误与中断） | 工具能恢复，但 **LLM 终态失败 / 补丁 miss / 深度限制** 仍会终结会话 |
| 效率 | 单会话工具调用中位数偏高；`view/bash/grep` 占绝对多数 | 读多改少 + shell 误用 rg/heredoc 造成 token/时延浪费 |

**优先级总览**

1. ~~**P0 上游 LLM**：模型/账号不可用快速失败与可切换；`503/502` 跨 provider 故障转移~~ **（暂缓，属上游/账号配置问题）**  
2. **P0**：`bash/shell` 内容失败 vs 硬失败语义收敛（非零退出、rg 无匹配、batch 部分失败）— **已有主路径；继续压 hard rate**  
3. **P1**：`apply_patch`/`edit` 统一 `STALE_CONTEXT` + suggested view offset；`wait_agent` 超时 next_action 强化；`spawn` depth=`SPAWN_DEPTH_LIMIT`  
4. **P1**：prompt/策略层减少 shell-rg/heredoc、鼓励 toolkit 并行与 `view.files`  
5. **P2**：失败时间线看板、会话级效率指标落盘、审批/协作漏斗可视化

### 2026-07-26 夜间复测（最近 30 个有效会话）

本轮新增 `scripts/analyze-aicli-session-history.ps1`，递归读取日期分层目录，并按 `session directory + tool_call_id/request_id` 去重。旧的 `~/.aicli/analyze_tool_errors.ps1` 只扫描 `chat-logs` 第一层，容易漏掉 `YYYY/MM/DD/session` 新布局并重复计算模型切换日志；现已优先转发到新分析器。

复测窗口：`2026-07-26 14:16` 至 `23:11`；为收集 30 个有效会话，按时间倒序跳过了 24 个零消息探测会话：

| 指标 | 结果 | 解释 |
| --- | ---: | --- |
| 工具 hard error | `40 / 3313 = 1.21%` | non-fail `98.79%`；另有 empty 149、partial 2，均不算硬失败 |
| shell hard error | `10 / 481 = 2.08%` | 其中 8 次为 `timeout_ms=1/30` 单位误用导致的 1–30ms 超时；其余 2 次为 shell 兼容性预检 |
| apply_patch hard error | `9 / 177 = 5.08%` | 仍以 stale hunk / malformed patch 为主；新结果已逐步收敛到 `STALE_CONTEXT` |
| grep hard error | `7 / 788 = 0.89%` | 空 path、把 `pattern_files` 字面量当文件、缺 pattern 为主 |
| LLM request failure | `45 / 1057 = 4.26%` | 主要是 provider unavailable、流中断/EOF、TLS、配额；用户取消 4 次不属于 runtime 缺陷 |
| 语义 no-op | `update_goal` 4 次 | 工具返回成功但 `updated=false / goal_missing`，单独统计，不混入 hard error |
| replay 完整性 | unmatched tool calls `0` | chat-log 本身成对；但模型切换后的 Anthropic wire 历史仍出现 1 次 incomplete `tool_use` replay 400 |

据此补了两个 residual 修复：

1. 数值 `timeout_ms < 100` 且没有更粗粒度超时时，按模型单位混淆噪声忽略，回退到推断/默认超时；确需亚 100ms 使用 `timeout="30ms"`，避免把 `30` 误当 30 秒后立即 hard-timeout。
2. Anthropic 历史重放现在按相邻块验证 `tool_use -> tool_result` 完整性；缺任一结果时丢弃整个不完整 replay block，而不是向上游发送必然 400 的历史。完整的多工具结果仍合并并保留。
3. `update_goal` 的工具说明要求先由 `get_goal` 确认非空 active goal，或明确处于持久 goal run，减少普通会话结束时的 `goal_missing` no-op。

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

**状态（2026-07-26 residual）**

- 上游/账号类（TLS / 401 / model_not_found）仍属配置与宿主问题，保持暂缓。  
- **runtime 可修 residual 已落地**：Anthropic adaptive thinking 误把 `effort` 嵌在 `thinking` 下，触发  
  `thinking.adaptive.effort: Extra inputs are not permitted`（看板 `other_llm` 桶，会话 `20260726_150533...`）。  
  现已：  
  1) `AnthropicAdapter.BuildRequest` adaptive wire 仅 `{type:"adaptive"}`，effort 只进 `output_config`；  
  2) `Thinking.MarshalJSON` 对 adaptive 省略 nested effort（防御）；  
  3) `downgradeUnsupportedProviderRequest` 识别 `thinking.*` 时同步清 `ReasoningEffort`（派生 adaptive 路径）；  
  4) remembered downgrade 在 thinking unsupported 时一并清 effort，避免下一轮重建。  
- 单测：`adapter` adaptive body、`types/anthropic` marshal、`agent` downgrade、`llm` IsUnsupportedRequestParameter。

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

**状态（2026-07-26 residual denoise）**

- **简单纯 `rg` 代码搜索软重定向**：无管道/链式/重定向的 standalone `rg` 不再执行，返回 `Success=true` + `shell_search_redirected=true` + next_action 指向 toolkit `grep`（不抬 hard rate）。管道/`rg --files` 等仍可走 shell。  
- **Select-String / findstr no-match**：仅当其为 **primary** 命令时 exit 1 空输出可 soft-empty；`tsc | Select-String` 等过滤管道不伪装 empty，避免吞掉构建失败。  
- 管道 `rg ... | Select-Object` no-match / regex parse 仍分别走 empty success / content success（既有契约）。  
- 离线 `analyze_tool_errors.ps1` 已按新契约区分 `hard` / `empty` / `content_nonzero` / `redirected`，避免把 `ok=true` 的 no-match 与 content 非零退出算进 hard。

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

**状态（2026-07-25；管道收口 2026-07-26）**

- `apply_patch` / `edit` 已输出 `error_code=STALE_CONTEXT`、`failure_class=stale_context`、`retryable=false`。  
- `edit` / `multiedit` 现补齐 `suggested_view_offset` / `suggested_view_limit`（与 apply_patch 一致）。  
- `multiedit` 全失败（old_string 全 miss）现与 edit 同一错误码，不再落回裸 `TOOL_EXECUTION`。  
- **2026-07-26**：`recordToolExecutionOutcome` 曾把工具自带 `STALE_CONTEXT` 覆盖成顶层 `TOOL_EXECUTION`，chat-log 看板因此显示通用 next_action。现已：  
  1) 优先保留 tool-authored `error_code`；  
  2) 将 `next_action` / `retryable` / `suggested_view_*` 提升到顶层 metadata；  
  3) 无结构化码时，消息启发也把 `old_string 未在文件中找到` / hunk miss 归为 `STALE_CONTEXT`。  
- **2026-07-26（管道完备）**：  
  4) `toolresult.Diagnose` 对 nested `tool_metadata.next_action` 与 top-level 同等优先（历史 payload 不丢 tool-authored 指引）；  
  5) `tool.completed` / chat-log payload 导出 `failure_class` / `file_path` / `suggested_view_offset|limit`，看板可直接读。  
- **2026-07-26（导出 refine residual）**：live chat-log 仍见 `edit` miss → `error_code=TOOL_EXECUTION` + 通用 next_action（历史 stamp / 部分 promotion）。现已：  
  6) `Diagnose` 在 **generic `TOOL_EXECUTION`** 上用 message / `failure_class=stale_context` **refine → `STALE_CONTEXT`**，并替换 generic default next_action；特定码（如 `TOOL_TIMEOUT`）不 demote；  
  7) live `failCategoryForCode` + offline `tool_efficiency_common` 映射 `STALE_CONTEXT`→`stale_context`、`SPAWN_DEPTH_LIMIT`→`spawn_depth`，并补 `stale_context_failures` flag；  
  8) manager `ExecuteWithMeta` 保留 toolkit STALE metadata 的集成测 + payload refine 测。  
- 重建 runtime 后的新会话：`tool.completed` / chat-log 应以 `STALE_CONTEXT` + 可执行 next_action 为主；离线看板即使读到旧 `TOOL_EXECUTION` 正文，也会按 message 归入 `stale_context`。  
- **2026-07-26（disposition replay residual）**：此前 `dispositionReplayAdvisory` 只跟踪 empty/partial，模型在 `edit`/`apply_patch` STALE 后仍会盲重试同参。现已：  
  9) loop 跟踪 `failed` disposition + dominant `error_code`（优先 `STALE_CONTEXT`）；  
  10) 同 fingerprint 重放时注入 STALE 专属 advisory（re-view / suggested_view_offset / 禁止同 stale hunk）；  
  11) observability `RecordToolDispositionReplay` 计入 `failed`；  
  12) 离线 dashboard `rehydrate_tool_result_fields` 把历史 `TOOL_EXECUTION`+old_string/hunk miss 回填为 `STALE_CONTEXT` 并替换 generic next_action（复测：`error_codes` 以 `STALE_CONTEXT` 为主）。

- **2026-07-26（edit indent/whitespace auto-heal residual）**：近窗 hard 主峰仍是 `edit::edit_old_string_miss`。chat-log 抽样显示大量 miss 并非语义 stale，而是模型 `old_string` **丢掉外层 tab/空格** 或 **行尾空白不一致**（closest 与 old 仅 indent 差）。现已：  
  13) `matchEditStrings` 在 exact + CRLF/LF 之后增加 **whitespace/indent-tolerant** 匹配（与 `apply_patch` line matchers 同级：`TrimRight` → `TrimSpace`）；  
  14) 命中时用 **文件真实子串** 做替换，并对 `new_string` 做 **按行 indent 对齐**（避免改写到错误缩进）；  
  15) **多窗口命中则拒绝 auto-heal**，回落 `STALE_CONTEXT`，防止错误位点编辑；  
  16) STALE 诊断改为多行“最接近的当前内容”块（含行号），便于模型直接重建 `old_string`；文案标明已尝试 CRLF/空白对齐。  
  - 单测：`TestEditTool_HealsMissingLeadingIndent` / trailing-ws / unique-window / CRLF+indent；`TestMultieditTool_HealsMissingLeadingIndent`。  
  - 预期：indent-only miss 不再抬 hard rate；真 content drift 仍走 STALE + 可复制 closest 块。  
- **2026-07-26（STALE recovery copy-paste residual）**：live 复测（~60 sessions）写失败后 **~53% next tool 仍是同 write（edit）**，且 `suggested_view_offset` 未进 model contract；closest 仅在 error 正文。现已：  
  17) `edit`/`multiedit` metadata 增加 **`current_snippet` + `current_snippet_start_line`**（与多行 closest 块同源）；  
  18) promote / `tool.completed` 导出 `current_snippet*`；`Diagnose` + model contract 暴露 `file_path` / `suggested_view_offset|limit` / `current_snippet_start_line`；  
  19) STALE `next_action` / disposition replay advisory 改为 **优先复制 current_snippet**，而非仅“再 view”；  
  20) closest 行文本 **不做 mid-line truncate**（只限行数），保证可精确复制 indent。  
  - 验收信号：新会话 STALE 后 `next_same_write` 下降、`read_within_3` 或同轮用 snippet 重建成功上升；chat-log 顶层可见 `current_snippet`。
- **2026-07-26（apply_patch current_snippet residual）**：`edit` 已导出结构化 `current_snippet*`，但 `apply_patch` hunk miss 仍只把 closest 塞进 error 正文 + `suggested_view_offset`，模型/下游无法像 edit 一样直接 copy-paste 重建。现已：  
  20b) `patchHunkNotFoundError` 结构化携带 `startLine` + `closest`（`errors.As` 穿透 wrap）；  
  20c) `applyPatchToolFailure` 导出 `current_snippet` / `current_snippet_start_line`（与 edit 同字段）；  
  20d) STALE `next_action` 改为优先 `current_snippet`，保留 message 解析 fallback。  
  - 单测：`TestApplyPatchTool_MissingContextIncludesClosestCurrentLines` 断言 snippet metadata。  
  - 预期：apply_patch STALE 与 edit 走同一 recovery contract，减少盲重试 stale @@。
- **2026-07-26（edit column-ws / blank-run residual）**：近窗 hard 主峰仍是 `edit::edit_old_string_miss`（看板 ~33）。抽样显示两类 **非语义** miss：  
  (a) Go 列对齐内部多空格（`Name  Event` vs `Name Event`）；  
  (b) 模型编造 blank-run 长度（3 vs 4/5 空行）而显著行相同。此前 `edit` 仅 TrimRight/TrimSpace，且 `normalizePatchComparableLine` 不折叠内部空白。现已：  
  20e) `normalizePatchComparableLine` **collapse internal whitespace**（`strings.Fields`），`apply_patch` 与 `edit` 共享；  
  20f) `edit` whitespace-tolerant matcher 增加 collapse-ws 级；  
  20g) fixed-window 失败后 **blank-run flexible** 匹配（非空行序列唯一命中，允许中间空行数漂移）；  
  20h) `rebuildEditReplacement`：**未变行保留文件精确字节**（列 padding / blank-run），仅改写真正变化的 body 并继承 file indent；真 content drift（多余显著行）仍 STALE。  
  - 单测：`TestEditTool_HealsInternalColumnWhitespace` / `TestEditTool_HealsBlankRunLengthDrift`；trailing-ws 期望改为保留文件行尾空白。  
  - 预期：列对齐 / blank-run 类 miss 不再抬 hard rate；语义 drift 仍 STALE + current_snippet。  
- **2026-07-26（apply_patch blank-run residual）**：看板次峰 `apply_patch::patch_context_miss`（~7）。`edit` 已有 blank-run 柔性匹配，但 `apply_patch` 的 `locateHunk` 仍固定窗口，模型编造 3 vs 4 空行即 STALE。现已：  
  20i) 抽出共享 `locateBlankRunLineSpan`（edit/apply_patch 共用）；  
  20j) `applyPatchHunks` 在 fixed-window miss 后走 blank-run 唯一 span 定位；  
  20k) `rebuildPatchBlankRunReplacement`：保留文件 blank-run，仅改写变化的非空行 body 并继承 file indent；真 content drift 仍 STALE。  
  - 单测：`TestApplyPatchTool_HealsBlankRunLengthDrift`。  
  - 预期：blank-run 类 apply_patch miss 不再抬 hard rate；语义 drift 仍 STALE + current_snippet。
- **2026-07-26（closest near-identifier typo residual）**：真 content drift 场景里模型常把标识符打成近邻 typo（`HelloWord` vs `HelloWorld`），且 multi-line old/hunk 前缀是 generic `import (` / `)`。此前 closest 只做 token 精确重合 + contains，late distinctive 行锚不住；apply_patch 还会用完整 expected 对 tail 窗口 tighten，再 min-8 左扩到 import 噪声。现已：  
  20k2) `editLineSimilarity` / `patchLineSimilarity` 增加 `runeEditSimilarity` 全行/软 token 近邻匹配（**仅 ranking，绝不 auto-heal 写**）；  
  20k3) generic structural 行（`}`/`)`/`{`…）降权，避免到处命中；  
  20k4) apply_patch 记录 winning `bestExpected`（full vs tail）再 tighten；`padPatchClosestWindow` **先右后左**，避免 late core 的 suggested_view_offset 漂到文件头。  
  - 单测：`TestFindClosestEditSnippetWithLine_AnchorsOnLaterDistinctiveLine` / `TestClosestPatchCurrentContext_AnchorsOnNearIdentifierTypo` / `TestApplyPatchTool_NotFoundIncludesNearIdentifierClosest`。  
  - 预期：近 typo + 弱前缀的 STALE 仍给出可复制 HelloWorld 块与靠近目标的 offset。

- **2026-07-26（model contract current_snippet residual）**：metadata / `tool.completed` 已有 `current_snippet`，但 model-visible contract **只导出** `current_snippet_start_line`；error 正文 closest 又常 mid-line truncate → 模型读 contract 时无法 copy-paste。现已：  
  20l) `Diagnostic.CurrentSnippet` + `attachRecoveryHints` 读取（**不 TrimSpace**，保 indent）；  
  20m) `capCurrentSnippetForContract`（4KiB、**整行截断**）；  
  20n) model contract JSON 导出 `current_snippet` 文本。  
  - 单测：`TestDiagnoseAttachesStaleViewHints` / `TestDiagnoseCapsOversizedCurrentSnippet` / `TestRenderToolResultContentForModel_ExposesStaleViewHints`。  
  - 预期：新会话 STALE 后 contract 顶层可见可复制 snippet；语义 drift 场景 `next_same_write` 下降。

- **2026-07-26（path_missing unique auto-heal residual）**：近窗 hard 次峰为 `view/ls/grep::path_not_found`（`TOOL_PATH_NOT_FOUND`≈11）。抽样常见模式是 **漏扩展名 / 近邻唯一高分**（如 `.../ui` → `.../ui.tsx`），此前仅 surface candidates 仍 hard-fail。现已：
  21) read-safe preflight 在 **单 content path** 且存在 **唯一高置信 sibling**（score≥70，次名与 top 分差>10）时 **原地改写 path-like args 并 Allow**；  
  22) 用同一 `pathExistsChecker` 校验 healed 路径存在（测试 stub 仍走 deny+candidates）；  
  23) metadata：`path_auto_healed` / `original_path` / `resolved_path` / `path_candidates` + `preflight=path_auto_heal`；promote 到 tool.completed / chat-log；  
  24) 多候选 / 多 path batch / mutation 工具 **不 auto-heal**；  
  25) disposition replay 对 `TOOL_PATH_NOT_FOUND` 专属 advisory（用 path_candidates / ls/glob，禁止同 missing path 盲重试）。  
  - 单测：`TestPathPreflightAutoHealsUniqueNearbyTypo` / ambiguous deny；`TestDispositionReplayAdvisory` PATH 分支。  
  - 预期：唯一扩展名/大小写类 miss 不再抬 hard rate；真歧义路径仍结构化 fail + candidates。  
- **2026-07-26（path candidate quality residual）**：live 复测 path fails 中 **~36% 仅建议 `.backups`**（空 stem / `strings.Contains(want, "")` 噪声），且无 sibling 时缺少可执行发现指引。现已：  
  26) `rankNearbyPathCandidates` 过滤 `.backups` / VCS / cache 噪声；修复 pure-dotfile empty-stem 误匹配；  
  27) 无有效 sibling 时把 **已存在 parent dir** 写入 `path_candidates`，`next_action` 引导 `ls/glob`；  
  28) 默认 `TOOL_PATH_NOT_FOUND` next_action 改为 prefer `path_candidates` / parent discovery。  
  - 单测：`TestSuggestNearbyPathCandidatesIgnoresBackupNoise` / `TestPathPreflightSurfacesParentWhenNoSiblingMatch`。  
  - 预期：新会话不再把 `.backups` 当主候选；invented leaf 后下一工具更常 `ls/glob` 或使用真实 sibling。  
- **2026-07-26（path separator-fold auto-heal residual）**：近窗 path 次峰中仍有 **唯一 sibling 仅差 `_`/`-`** 却 hard-fail 的样本（如 `providertoken`→`provider_token`、`aiSites`→`ai_sites`）。此前 stem 比较只做大小写/扩展名/Levenshtein，折叠分隔符后相等也只落在 contains=40，达不到 auto-heal 阈值。现已：  
  29) `separatorFoldedPathStemEqual` / `foldPathStemSeparators`：去掉 `_`/`-` 后 case-insensitive 比较（最短 4 rune，防短 stem 误 heal）；  
  30) `rankNearbyPathCandidates` 对折叠相等给 **score=85**（≥`minPathAutoHealScore=70`），并在更弱 typo/contains 分上 upgrade 到 85；  
  31) 唯一高分时走既有 `path_auto_heal` 改写；多候选仍 deny + surface candidates。  
  - 单测：`TestPathPreflightAutoHealsSeparatorFoldedStem` / `TestSuggestNearbyPathCandidatesRanksSeparatorFoldedStem`。  
  - 预期：唯一 underscore/hyphen 漂移不再抬 `TOOL_PATH_NOT_FOUND` hard rate。  
- **2026-07-26（path placeholder empty residual）**：最新 live fail：`grep path=""`（两字符引号字面量，非空串）被当 missing relative path，`rankNearbyPathCandidates` 读 workspace root 列出 `.agents/.backups/.git…` 作 candidates 并 hard-deny。工具本身对空 path 本会默认 `.`。现已：  
  31b) `normalizePathArgPlaceholder` / `clearPlaceholderPathLikeArgs`：在 digest/required/path preflight 前把 `""`/`''`/`null`/`undefined`/`None` 等占位 token 归一为真 empty；  
  31c) `stringValues` / path collectors / ranking base 同步跳过 placeholder；可选 path（grep）Allow 后由工具默认 workspace root；必填 `file_path` placeholder → `TOOL_INVALID_ARGS`（missing required），不再 `PATH_NOT_FOUND`+根目录噪声。  
  - 单测：`TestPathPreflightAllowsPlaceholderEmptyPath` / `TestPathPreflightRequiredPlaceholderIsMissingArg` / `TestNormalizePathArgPlaceholder`。  
  - 预期：新会话不再出现 `path not found: "" (candidates: .agents, .backups, …)`。  
- **2026-07-26（edit STALE mislabel refine residual）**：live chat-log 见 `edit` old_string miss 被标成 `TOOL_TIMEOUT` / `TOOL_PATH_NOT_FOUND`（closest 片段含 `timeout` / `no such file` map 键，message 启发抢先；structured 码也偶发错贴）。模型随后跟 timeout/path recovery，绕开 `current_snippet`。现已：  
  32) `classifyToolErrorCode`：**STALE 判定先于 PATH/TIMEOUT**（`messageLooksLikeStaleEditOrPatch`）；  
  33) `refineMislabeledStructuredCode`：仅 **edit 家族**（edit/multiedit/apply_patch）在 structured=`TOOL_TIMEOUT|TOOL_PATH_NOT_FOUND` 且 body 明确 STALE 时 refine→`STALE_CONTEXT`；bash 真 timeout 不 demote；  
  34) 误标 refine 后丢弃 timeout/path 类 `next_action`，换 STALE recovery 文案；`retryable=false`。  
  - 单测：`TestDiagnoseRefinesMislabeledTimeoutOnEditStaleBody` / Path 变体 / `TestClassifyToolErrorCodePrefersStaleOverTimeoutInSnippet`；保留 bash 不 demote。  
  - 预期：新会话 edit miss 的 error_code/next_action 以 `STALE_CONTEXT` 为主，看板不再把 STALE 计入 timeout/path 桶。  
- **2026-07-26（STALE recovery field export residual）**：live 复测 edit/apply_patch fail **`has_current_snippet=0/50`**，chat-log 仅有 error 正文里的「最接近片段」，`protocol_result.metadata` 只有 disposition 五元组；模型/看板无法结构化 copy-paste。根因：`Diagnose` 能挂 recovery 字段，但 `ApplyDiagnosticMetadata` / `promoteToolDispositionToPayload` / `thinEventMetadata` 未稳定导出，且历史 body 未回填。现已：  
  35) `attachRecoveryHints(toolErr)`：从 error 正文解析 `最接近的当前内容`（行号前缀 `|`/`:`）与 `最接近片段:"…"`（`%q` unescape），并回填 `suggested_view_offset`；  
  36) `promoteStaleRecoveryFields` + `ApplyDiagnosticMetadata` 失败路径写出 `file_path` / `current_snippet*` / view hints（不覆盖已有）；  
  37) `promoteToolDispositionToPayload` 把 diagnostic recovery 补到 flat tool.completed；  
  38) `ResultFromParts` 先 `ApplyDiagnosticMetadata`；`thinEventMetadata` 纳入 recovery keys（snippet 已 4KiB cap）。  
  - 单测：`TestDiagnoseParsesClosestSnippetFromErrorBody` / quoted fragment；`TestApplyDiagnosticMetadataPromotesStaleRecoveryFields`；`TestToolCompletedEventPayloadRehydratesSnippetFromErrorBody`；`TestResultFromPartsPromotesStaleSnippetIntoThinMetadata`。  
  - 预期：新会话 STALE fail 顶层与 `protocol_result.metadata` 可见 `current_snippet`；离线扫描 `has_current_snippet` 上升；`next_same_write` 可继续压。  
- **2026-07-26（web_search network fail residual）**：看板出现 `web_search::tool_failed_other`（connectex / dial tcp 被标成通用 `TOOL_EXECUTION` + “Inspect the error details…”）。模型对同一 query 盲重试。现已：  
  39) `failureSearchResult` 结构化失败：优先 Instant 错误，回落 HTML；  
  40) `classifyWebSearchFailureCode`：typed `net/url` 错误 + 消息启发；**HTTP 429/5xx 优先于** 泛网络类（避免 `url.Error` 吞掉 503）；  
  41) 导出 `error_code=NETWORK_UNAVAILABLE|NETWORK_TIMEOUT|API_RATE_LIMIT|API_SERVER_ERROR`、`failure_class`、含 query 的 `next_action`（backoff / 拆 query / 可离线继续）、`attempted_args.query`；  
  42) agent `knownToolOutcomeErrorCode` 纳入 network/rate/upstream 码以便 promotion。  
  - 单测：`TestWebSearchTool_NetworkFailureIsStructured` / HTTP 503 / `TestClassifyWebSearchFailureCode`。  
  - 预期：新会话 web_search 传输失败不再进 bare `TOOL_EXECUTION`；看板可分桶 network；模型 next_action 明确禁止 spam 同 query。
- **2026-07-26（dashboard STALE snippet rehydrate + path ambiguity residual）**：近窗扫描 `edit` STALE `has_current_snippet=0/40`（历史二进制未导出 structured 字段；body 常有「最接近片段」却未回填）。另 path 多高置信候选时 next_action 仍偏 generic。现已：  
  43) `tool_efficiency_common.rehydrate_tool_result_fields` 统一看板/离线 rehydrate：从 error body 解析 `最接近的当前内容（第…）` 多行块与 `最接近片段:"…"`（%q unescape），回填 `current_snippet*` / `suggested_view_offset`，并把 generic next_action 换成 copy-snippet 指引；  
  44) 解析收紧：只认 header 形 `最接近的当前内容（第`，且要求至少一行带行号——避免 next_action 散文引用 + `期望内容` 误当 current_snippet；  
  45) `uniqueHighConfidencePathCandidate` 返回 `ambiguous`；多高置信 sibling deny 时 next_action 明确 **Pick exactly one path_candidates**（`no unique auto-heal`），禁止 invent 第三条路径。  
  - 单测：`TestStaleSnippetRehydrate*` / prose reject；`TestParseClosestSnippetRejectsProseMentionOfClosestMarker`；`TestPathPreflightDoesNotAutoHealAmbiguousCandidates` next_action。  
  - 验收：离线 `snip_rehydrated` 对含「最接近片段」的历史 edit 上升；apply_patch 仅 prose+期望内容不再假 snip；歧义 path deny 文案可执行。
- **2026-07-26（shell multi-key timeout_ms=1 residual）**：近窗 hard 主峰切到 `shell::TOOL_TIMEOUT`（~6/40 session fails）。dump 显示 **全部 1ms 假超时** 同源：`attempted_args.timeout_ms=1` **且** `timeout_sec=20/30/60`、`timeout=""`，`timeout_source=tool_argument`。根因是 schema 多字段噪声 + 解析器严格 `timeout_ms` 优先，1ms 在进程真正启动前 deadline 即到。现已：  
  46) `parseShellCommandTimeout` / `parseShellFunctionTimeout` 先收集 ms/sec/named 候选，再协调：当 `timeout_ms < 100ms` 且存在 ≥1s 的 `timeout_sec`/`timeout` 时 **丢弃噪声 ms**，改用更粗粒度候选；  
  47) 合理短预算（如 250/500ms）仍保持 `timeout_ms` 优先；单独 `timeout_ms=1`（无 coarse 备选）仍显式生效；  
  48) shell function 对齐 bash：`timeout_ms=0` 视作 absent placeholder；schema 文案提醒勿同时乱填 ms+sec。  
  - 单测：`TestBashTool_PrefersTimeoutSecWhenTimeoutMsIsSchemaNoise` / `TestParseShellCommandTimeout_NoiseReconciliation`；`TestShellFunction_PrefersTimeoutSecWhenTimeoutMsIsSchemaNoise` / `TestParseShellFunctionTimeout_NoiseReconciliation`；保留 short-ms priority 与 negative reject。  
  - 预期：新会话不再出现 `execution timed out after 1ms` + `timeout_source=tool_argument` 的假超时；真超时仍按 sec/ms 语义执行。
- **2026-07-26（true content_diff / 旧二进制无 closest residual）**：近窗 hard 主峰仍是 `edit::edit_old_string_miss`。离线分类：`true_first_gone`≈31、`rh_snip` 39/42，但 **no_snip=3** 里含「旧二进制无 closest 块」+「首行全编造 / 标识符仍在文件」空窗。根因：`findClosestEditSnippetWithLine` 多行窗 **score<0.45 直接空返回**（与 apply_patch 单行 fallback 不对齐）；历史 body `最接近片段:"…"` 常 **mid-line 截断无闭引号**，rehydrate 丢弃；apply_patch 正文仍 `truncateDiagnosticText(…,240)` 行内截断。现已：  
  49) edit multi-line miss 后 **token/single-line fallback**（`editDistinctiveTokens` + 邻行扩展 + pad 窗）导出 `current_snippet*` / 多行「最接近的当前内容」——**仅 ranking，绝不 auto-heal 写**；  
  50) `formatPatchCurrentLines` 改为与 edit 同形 **`N|full line`**（无 mid-line truncate）；  
  51) `parseClosestSnippetFromErrorMessage` + offline `parse_closest_snippet_from_error_message` 接受 **截断 %q**（无闭引号时切到 next_action/old_string trailer）。  
  - 单测：`TestFindClosestEditSnippetWithLine_TokenFallbackWhenFirstLineGone` / `TestEditTool_NotFoundTokenFallbackExportsCurrentSnippet`；`TestParseClosestSnippetFromTruncatedQuotedFragment`；`test_parse_truncated_quoted_fragment`；apply_patch line-prefix 断言更新。  
  - 预期：新会话真 content_diff 只要文件仍有 distinctive id，STALE 必带可复制 snippet；历史截断 quote 离线 `has_current_snippet` 上升；完全无关 old_string 仍可空窗。
- **2026-07-26（Chinese invalid-args next_action residual）**：toolkit 中文校验文案（`pattern/file_path 参数缺失或无效` 等）虽已分类为 `TOOL_INVALID_ARGS`，但 `invalidArgsNextAction` 未覆盖中文 pattern，仍落默认 schema 文案或过宽 generic。现已：  
  52) `invalidArgsNextAction` 覆盖 `参数缺失*` → required 补齐指引，`参数无效/错误/类型错误` → 类型/形态纠正指引；  
  53) 单测：`TestDiagnoseChineseToolkitInvalidArgsNextAction`。  
  - 预期：新会话中文 missing-arg fail 的 `next_action` 明确要求补齐/修正 schema 参数，禁止同 incomplete payload 盲重试。  
- **2026-07-26（parent sibling discovery residual）**：invented leaf + 无 ranked sibling 时，此前 `path_candidates` 仅 parent，模型仍常 invent 第三条路径或再开 ls。现已：  
  54) `listParentSiblingDiscoveryHints`：parent 存在且无 typo-rank 命中时，在 parent 后附最多 4 个非噪声 sibling 样本（过滤 `.backups`/VCS/cache）；  
  55) parent-only next_action 区分「仅 parent」vs「parent + sample siblings」；**不 auto-heal** 这些 discovery 样本。  
  - 单测：`TestPathPreflightSurfacesParentWhenNoSiblingMatch`（含真实 sibling）/ `TestPathPreflightParentOnlyWhenOnlyNoiseSiblings`。  
  - 预期：invented leaf 后 `path_candidates` 可直接点选真实 sibling，减少 invent/ls 往返。
- **2026-07-26（apply_patch invalid syntax / path crumb residual）**：近窗仍见 `apply_patch::TOOL_EXECUTION` 非 STALE 样本：  
  (a) hunk 中嵌套 `*** Begin Patch` / 非法 hunk 行 → 通用 “Inspect the error details…”；  
  (b) `Update File: snippet.go",` 路径粘贴引号/标点 → Windows “filename syntax is incorrect”。现已：  
  56) `sanitizeApplyPatchPath` 剥离 Update/Add/Delete/Move 路径外层引号与尾部 `",` 等标点；  
  57) `isApplyPatchInvalidSyntaxError` + `applyPatchToolFailure` 将解析/信封失败标为 **`TOOL_INVALID_ARGS`**（`failure_class=invalid_patch_syntax`，`retryable=false`）并给 syntax next_action；  
  58) empty patch / 无操作 同步 `TOOL_INVALID_ARGS`；`toolResultFailureWithCode` 对 INVALID_ARGS 默认不可盲重试；  
  59) `classifyToolErrorCode` 覆盖「不是合法的 hunk/补丁」等中文 parse 文案。  
  - 单测：`TestApplyPatchTool_InvalidHunkSyntaxIsToolInvalidArgs` / `TestSanitizeApplyPatchPath` / `TestApplyPatchTool_SanitizesTrailingQuoteOnUpdatePath`；`TestApplyPatchTool_RejectsMalformedPatch` 断言 INVALID_ARGS；diagnostic 中文 illegal hunk case。  
  - 预期：新会话非法 patch 不再 bare `TOOL_EXECUTION`；尾部引号路径可成功 apply；模型按 syntax next_action 重建而非盲重试。

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
# 最近有效会话的去重统计（flat + YYYY/MM/DD 分区、多模型日志）
.\scripts\analyze-aicli-session-history.ps1 -Sessions 30 -JsonOut .\tmp\aicli_recent_session_analysis.json

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
