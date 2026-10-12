# 上下文用量（ctx used）数据更新链路分析与逐步验证（2026-10-12）

- 日期：2026-10-12
- 对象会话：`session_20261012111114_8DyOk3e2`（provider `free_models` / model `mimo-v2.6-flash-free`，窗口 1,048,576）
- 关联文档：
  - `docs/compact/README.md`（ctx used 口径声明）
  - `docs/compact/provider-usage-first-auto-compact-implementation-plan-20261011.md`（观测优先方案与两轮实施/实机验证记录）
  - `docs/plan/codex-compact-token-usage-observation-analysis.md`（Codex 观察值口径分析）
- 相关提交：`4901f788`（provider 用量锚定上下文计量）、`8489834c`（防止 CLI 同步覆写 loop 锚点）
- 代码基线：本仓库 2026-10-12 工作区（含未提交的并发工作流改动，本文引用行号为该时点实测）

## 0. 结论摘要（TL;DR）

1. **状态栏 `Context X% used` 的读数链路是三级取值**：runtime session 上的 provider 锚点 → CLI 会话快照 `ContextTokenCount` → 本地估算；百分比 = `round(max(used-12000,0)/(window-12000)*100)`（baseline 12000，clamp 0–100）。
2. **新建会话**从零起步；**加载/恢复**时从 runtime store 读回 `context_token_count / context_window_token_count / turn_context_token_count`（canonical + legacy 双键兼容）；**每次 LLM 请求完成后**由事件桥 `llm.request.finished` 把 provider usage 写成 `ContextTokenCount`（last-turn 覆盖语义），同时 ReAct loop 每次调用把 provider `InputTotal` 写成持久化锚点（legacy 键 + 7 个结构化身份键）。
3. **实机逐步验证通过**：137 次请求、268 次 `anchored` 投影、0 次 `context.usage_over_budget`；锚点持续刷新（357,742 → 358,568 → 386,751）；`used_tokens=301,548` 与事件流里的 `usage_total_tokens=301548` 精确对应，`(301548-12000)/(1048576-12000) = 27.93% → 28%`，与用户观察一致。
4. **验证中发现一个真实缺陷（P0 候选）**：canonical 键 `context_token_count` 会停留在“锚点出现之前”的旧值（本会话为 `2395`），与 loop 实时维护的 legacy 锚点 `aicli_context_token_count`（`358,568+`）分歧；而读取端 `sessionmeta` 按 **canonical 优先**解析，导致状态栏/恢复读到陈旧小值。用探针测试复现：同一份 store 形态下 `resolveChatStatusContextUsedTokens` 返回 `2395 → 0%`；这正是“Context 0% used”这一类误读的确定性解释。修复建议见 §5；**修复已落地（§5.6、§5.7）**。
5. `0%` 与 `28%` 两个读数的归属：`0%` ⇔ 上述 canonical 影子（探针确定性复现；另一种成因是首请求前无数据，属正常形态，见 §5.4）；`28%/used=301548` ⇔ 事件桥按 provider `total_tokens` 写入的内存快照路径（事件流实证）。两者之间的翻转时刻无法从留存的工件中完全还原（无状态栏快照），但两个写入路径的分歧本身就是需要修复的根因。

## 1. 背景

用户观察：状态栏先显示 `Context 0% used`（同屏累计 `2.8M in · 24K out`），随后回合中翻转为 `Context 28% used`（`used_tokens=301548`），窗口 1M。

数据侧事实（本次取证）：

| 证据 | 值 | 来源 |
|---|---|---|
| 事件流 `llm.request.finished`（2026-10-12T03:54:04.629Z） | `usage_prompt_tokens=300799`、`usage_completion_tokens=749`、`usage_total_tokens=301548`、`usage_cache_read_tokens=299200` | `chat-logs/.../events/runtime-events.jsonl` |
| 百分比复算 | `(301548-12000)/(1048576-12000)=27.93% → 28%` | `chat_status.go:510-525` 公式 |
| store 锚点（同会话） | legacy `aicli_context_token_count = 358568`（第二次读数 386751，会话仍在活跃更新） | `session_history.sqlite.sessions.metadata_json.context` |
| store canonical | `context_token_count = 2395`（陈旧，锚点出现前写入） | 同上 |
| debug.log 投影来源计数 | `anchored=268`、`estimated=5`（`no_anchor=3`、`prefix_changed=2`） | `chat-logs/.../debug/debug.log` |
| 事件流计数 | 137 started / 137 finished / `context.usage_over_budget=0` | `runtime-events.jsonl` |

## 2. 数据更新链路总览

| # | 环节 | 入口（主） | 行为 |
|---|---|---|---|
| 1 | 状态栏读取/渲染 | `chatSurfaceContextUsedStatusSegment`（`chat_interaction.go:3446-3473`） | 三级取值 + baseline 百分比，纯读取 |
| 2 | 新建/重置 | `resetChatContextTokenUsage`（`chat_token_usage.go:62-71`） | 上下文快照清零 |
| 3 | 加载/恢复 | `loadRuntimeConversation`（`chat_session.go:483`）→ `restoreChatStateFromRuntimeSession`（`:160`）→ `restoreChatContextTokenUsage`（`chat_token_usage.go:295-309`） | 从 runtime store 读回快照（canonical+legacy） |
| 4 | 请求完成写入（展示值） | 事件桥 `applyLLMRequestStatus`（`chat_runtime_events.go:6624-6663`）→ `applyChatContextTokensFromUsage`（`chat_token_usage.go:114-127`） | provider usage → `ContextTokenCount`（覆盖） |
| 5 | 请求完成写入（持久锚点） | ReAct loop `observeContextUsage`（`loop.go:5748-5784`）→ actor `storeRuntimeSessionContextObservation`（`actor.go:2197-2213`） | `InputTotal` + prompt 身份 7 键，每次调用写一次 |
| 6 | 回合收尾同步（CLI→store） | `syncRuntimeSessionFromChatMode`（`chat_session.go:1467`） | token/window/turn 回写；结构化锚点存在时跳过 canonical 回写（`:1542-1548`） |

投影判定口径（loop 内统一使用，`internal/contextledger`）：

```
delta      = max(0, 当前消息估算 - 观测时消息估算)
projection = max(anchor + delta, 当前消息估算 + 工具 schema 估算)
```

锚点在 provider/model/epoch/工具面指纹/消息前缀变化时失效（`contextledger.go:238+`），失效即回退 `estimated`。

## 3. 各环节细节（含代码位置）

### 3.1 状态栏读取（纯读取，不改数据）

- 渲染段：`chatSurfaceContextUsedStatusSegment`（`backend/cmd/aicli/commands/chat_interaction.go:3446-3473`）输出 `Context %d%% used` / `Ctx %d%%`。窗口为 0 时不渲染该段。
- 分母（窗口）：`resolveChatStatusContextWindowTokens`（`chat_status.go:484-499`）优先级：
  1. `session.ContextWindowTokenCount`；
  2. 预算 `ModelCapabilityMaxContextTokens`；
  3. 预算 `ProviderContextLimit`。
- 分子（used）：`resolveChatStatusContextUsedTokens`（`chat_status.go:537-557`）三级：
  1. `session.RuntimeSession` 上的锚点（`runtimeSessionContextInt(session.RuntimeSession, chatRuntimeContextContextTokenCount)`，>0 时直接返回，注释明确“provider 观测的最后确认值”）；
  2. `session.ContextTokenCount`（CLI 内存快照，事件桥/同步路径写入）；
  3. `resolveChatContextSnapshotTokens`（本地估算兜底）。
- 百分比：`chatStatusContextUsedPercent`（`chat_status.go:510-525`）：

  ```
  effective = window - 12000        // chatStatusContextBaselineTokens, :466
  used_eff  = max(used - 12000, 0)
  percent   = clamp(round(used_eff / effective * 100), 0, 100)
  ```

- 键与别名解析（三级读取的第 1 级底层）：
  - `chatRuntimeContextContextTokenCount = sessionmeta.LegacyAICLIContextTokenCount = "aicli_context_token_count"`（`chat_token_usage.go:11-16`）；
  - `runtimeSessionContextInt`（`chat_token_usage.go:311-316`）→ `sessionmeta.Int`（`internal/sessionmeta/sessionmeta.go:343+`）→ `Value`（`:310-321`）→ `CandidateKeys`（`:172-196`）；
  - `CandidateKeys` 的解析顺序是 **canonical 优先**：`[context_token_count, aicli_context_token_count]`（canonical 常量 `:32`，legacy 映射 `:87/:120/:150-152`）。
  - **注意**：这一顺序是 §5 中“canonical 影子”缺陷成立的关键。

### 3.2 新建会话 / 重置

- `resetChatContextTokenUsage`（`chat_token_usage.go:62-71`）清零 `ContextTokenCount / ContextWindowTokenCount / TurnContextTokenCount / providerContextTokenCount / providerContextWindowTokenCount`；
- 新会话在首个 provider usage 到达前没有锚点，used 走第 3 级估算；若估算值 ≤ baseline(12000)，显示 `0%`——这是“新会话 0%”的正常形态。

### 3.3 加载 / 恢复（数据来源）

- 入口 `loadRuntimeConversation`（`chat_session.go:483`）：
  1. `SessionManager.Get`（`internal/chat/manager.go:86-89` → `storage.Load`）从 runtime store 取会话；
  2. `restoreChatStateFromRuntimeSession`（`chat_session.go:160`）：
     - `restoreChatContextTokenUsage`（`chat_token_usage.go:295-309`）读取 `context_token_count / context_window_token_count / turn_context_token_count`（canonical+legacy 候选）写回 CLI 字段；
     - `restoreChatTokenCount`（`:274-293`）读回累计 Token/Input/Output；
     - 窗口与配置对账后 `RefreshStatus` 重绘；
  3. `syncRuntimeSessionFromChatPreservingUpdatedAt` 仅做 CLI→store 写回，不做反向回灌。
- 恢复路径同样受 canonical 优先顺序影响：若 store 同时存在陈旧的 canonical 与实时 legacy 锚点，`restoreChatContextTokenUsage` 会恢复陈旧值（探针实测：`2395`）。

### 3.4 请求完成后写入（展示值：事件桥 + 同步路径）

- 事件桥（本会话实际路径）：`handleEvent`（`chat_runtime_events.go:5801`）在 **回合所有权门之前** 调用 `applyLLMRequestStatus`（`:5827`；说明注释 `:5822-5826`——上下文计量是会话级观测，2026-10-11 事故中曾因 turn 不匹配被整批抑制）；
- `applyLLMRequestStatus`（`:6624-6663`）：
  - `llm.request.started`：`message_count` + turn 上下文与窗口（`:6633-6639`）；
  - `llm.request.finished`：组装 `TokenUsage`（`usage_prompt_tokens / usage_completion_tokens / usage_total_tokens / cache_*`，`:6643-6654`）→ `applyChatContextTokensFromUsage`（`:6655`）；若 applied<=0，回退 payload 的 `context_prompt_tokens / total_tokens` 估算（`:6656-6658`）。
- `applyChatContextTokensFromUsage`（`chat_token_usage.go:114-127`）→ `chatContextTokensFromProviderUsage`（`:129-151`）：

  ```
  contextTokens = usage.TotalTokens（存在时优先）
  Anthropic 协议且缓存上报在 prompt 之外时：追加 cache read/creation（:153-164）
  否则 PromptTokens + CompletionTokens（+cache）
  ```

  → 写 `providerContextTokenCount` → `applyChatContextTokensLocked(..., providerReported=true, monotonic=false)` **覆盖** `ContextTokenCount`（Codex last-turn 语义，不做 high-water）→ `RefreshStatus`（`:86-109`）。
- 同族其它调用点（不同运行模式）：`chat_provider_turn.go:317`、`chat_core.go:328`、`chat_actor_executor.go:698-711`、runtime-server 平面 `applyRuntimeServerUsage`（`chat_runtime_server.go:1423`）与 `refreshChatSessionFromRuntimeServer`（`:1399`）。

### 3.5 请求完成后写入（持久锚点：ReAct loop + actor）

- loop 每次 LLM 调用在 `loop.go:3071` 调用 `observeContextUsage`（`:5748-5784`）：
  - 门槛：`usage_source == provider_reported`；跳过 compact 内部请求；`promptTokens = usage.InputTotal()`（含缓存命中）> 0；
  - 构造 `contextledger.Observation`：`PromptTokens + MessageEstimate + Fingerprint + MessageCount + ToolFingerprint + Epoch + Provider + Model`（`:5767-5777`）；
  - 先入内存 ledger（`Ledger.Observe`，`contextledger.go:137-155`），再经 `cfg.ObserveContextUsage` 钩子持久化（`:5781-5783`）。
- actor 侧持久化 `storeRuntimeSessionContextObservation`（`internal/chat/actor.go:2197-2213`）：
  - `setRuntimeSessionObservedTokenUsage` 写 **legacy 键** `aicli_context_token_count`（`:2180-2192`，只写这一个键）；
  - 结构化身份 7 键（常量 `:44-52`）：`aicli_context_token_fingerprint / _message_count / _message_estimate / _tool_fingerprint / _epoch / _provider / _model`。
- 判定侧统一使用投影：`projectPromptTokens`（`loop.go:5798-5821`）+ `contextledger.Project`（`contextledger.go:191-218`）：
  - `delta = max(0, messageEstimateNow - observation.MessageEstimate)`；
  - `tokens = max(anchor + delta, estimate)`；
  - 失效原因：`provider_changed / model_changed / epoch_changed / tool_surface_changed / history_shrank / fingerprint_unavailable / prefix_changed`（`contextledger.go:238+`）。
  - 调用点：preflight 门限 `loop.go:6130`、mid-turn 压缩 `:6308`、恢复 `:6452`；超预算信号 `emitContextUsageOverBudget`（`:5868+`）。
- 读取辅助：`loadRuntimeSessionContextObservation`（`actor.go:2218-2238`，仅结构化锚点才算有效）；`HasRuntimeSessionContextAnchor`（`:2250-2256`，检查 fingerprint 键）；pre-turn `runtimeSessionActiveContextTokens`（`:2262-2292`，与 loop 同投影，legacy-only 会话保留 `max(anchor+pending, estimate)` 回退）。

### 3.6 回合收尾同步（CLI→store）与锚点保护

- `syncRuntimeSessionFromChatMode`（`chat_session.go:1467`）把 CLI 快照写回 store：
  - Token/Input/Output（`:1520-1534`）、窗口（`:1549-1553`）、turn 上下文（`:1554-1558`）均为 canonical+legacy 双写；
  - **上下文锚点回写被跳过**：`if !runtimechat.HasRuntimeSessionContextAnchor(runtimeSession)`（`:1542-1548`）——存在 loop 写入的结构化锚点时，CLI 不用展示值覆盖锚点（2026-10-11 实机发现：展示值 24,559 曾覆写精确锚点 24,523，提交 `8489834c`）。
  - 该函数只改 clone 再写 store，不更新内存中的 `session.RuntimeSession` 锚点副本。
- 运行中还有整体回灌路径（替换内存 `RuntimeSession` 副本）：goal 工具 `internal/goal/capability.go` → `RefreshRuntimeSession` → `chat_tool_session_context.go`（restore + 同步），以及 `syncRuntimeSessionBackIntoCLIWithOptions`（`chat_session.go` 失败/结束同步）。

## 4. 逐步验证记录

验证按链路顺序（读 → 建 → 载 → 写 → 锚 → 同步）逐环节进行：先跑该环节的既有单测/回放测试，再用真实会话数据核对，最后用探针测试验证边界组合。

### 4.1 链路静态核对

第 2、3 节所有引用的函数/常量/调用点均为本次实测（`grep`/`view` 逐条核对）。关键常量字符串：`"aicli_context_token_count"`（loop 写、状态栏读）、`"context_token_count"`（canonical）、`"aicli_context_token_fingerprint"` 等 7 键。

### 4.2 定向测试（全绿）

工作目录 `backend/`，命令与结果：

| 环节 | 命令 | 结果 |
|---|---|---|
| 投影/失效矩阵（锚点层） | `go test ./internal/contextledger/ -count=1` | ✅ `ok internal/contextledger 0.459s` |
| loop 按次观测 + mid-turn anchored 压缩回放 | `go test ./internal/agent/ -run TestReActLoop_MidTurnCompactionAnchorsOnProviderUsage -count=1` | ✅ `ok internal/agent 3.772s` |
| 结构化锚点有效性（工具面/前缀失效） | `go test ./internal/chat/ -run TestRuntimeSessionActiveContextTokensUsesStructuredAnchor -count=1` | ✅ `ok internal/chat 1.796s` |
| 事件桥 gating + 投影调试字段 + 配置化 + 同步防覆写（4 例） | `go test ./cmd/aicli/commands/ -run 'TestChatRuntimeEventBridge_ContextUsageAppliesDespiteTurnMismatch\|TestFormatRuntimeLLMRequestDebugInfoIncludesContextProjection\|TestBuildLocalChatLoopConfig_ContextAccountingMode\|TestSyncRuntimeSessionFromChatPreservesLoopContextAnchor' -count=1` | ✅ `ok cmd/aicli/commands 2.106s` |
| CLI 构建 | `go build ./cmd/aicli/` | ✅（在回归作业前完成） |

### 4.3 实机会话数据核对（`session_20261012111114_8DyOk3e2`）

- **持久锚点（loop 写）**：store `metadata.context` 中结构化 7 键齐全：

  ```
  aicli_context_token_count          = 358568   （第二次读数 386751，会话仍活跃更新）
  aicli_context_token_fingerprint    = 38f9ac93…a21
  aicli_context_token_message_count  = 380
  aicli_context_token_message_estimate = 255632
  aicli_context_token_tool_fingerprint = 38e1d4ff…b23
  aicli_context_token_epoch          = 0
  aicli_context_token_provider       = free_models
  aicli_context_token_model          = mimo-v2.6-flash-free
  aicli_context_window_token_count   = 1048576
  ```

  其中 `tool_fingerprint` 与会话消息里的工具面指纹一致（`38e1d4ff…b23`），说明锚点身份链（loop 写 ⇄ 请求实际工具面）对齐。
- **canonical 键（sync 写）**：`context_token_count = 2395`（陈旧，见 §5）；`context_window_token_count = 1048576`（窗口正常跟随）。
- **事件流**：137 次 `llm.request.started` / 137 次 `finished`；`context.usage_over_budget = 0`；典型 finished 载荷：`context_anchor_tokens=357425 → 357742`、`context_projection_tokens=357655 → 358292`、`context_projection_source=anchored`。
- **debug.log**：`context_projection_source` 计数 `anchored=268` / `estimated=5`（失效原因 `no_anchor=3`、`prefix_changed=2`）；`render suppressed` 138 行（渲染路径仍受 turn 所有权保护，计量路径已前移，验证了 2026-10-11 事件桥修复的行为差异）。
- **用户观察值定位**：`2026-10-12T03:54:04.629Z` 的 finished 事件载荷 `usage_total_tokens=301548`（= prompt 300799 + completion 749，cache read 299200），与观察 `used_tokens=301548` 精确一致。

### 4.4 探针复现（确定性）

用临时测试复现 store 的双键形态 {canonical=2395, legacy=358568, fingerprint 存在}（测试已删除，仅留结果）：

```
resolveChatStatusContextUsedTokens = 2395   (live anchor 358568 / stale canonical 2395)
runtimeSessionContextInt(legacy key) => ok=true value=2395
percent(358568, 1048576) = 33
percent(2395, 1048576)  = 0
restoreChatContextTokenUsage => ContextTokenCount=2395
```

结论：当前读取端在双键并存时**确定性返回陈旧 canonical 值**（2395 → 0%），恢复路径同样被污染。

### 4.5 跨会话横向对比

读取 `session_history.sqlite` 最近 14 条会话：

- 只有本会话（`8DyOk3e2`）出现 canonical 与 legacy 双键并存且分歧（`2395` vs `386751`）；
- 其余 13 条会话均为 `canonical=None`、只有 legacy 锚点（历史格式）；
- 即：双写路径（canonical 由 CLI sync 写、legacy 由 loop 写）+ 单边跳过（结构化锚点存在时跳过 canonical 回写）组合下，**新会话会天然进入该形态**。

### 4.6 百分比口径复算

| used | window | 公式结果 | 对应读数 |
|---|---|---|---|
| 2395 | 1048576 | 0% | canonical 影子（当前 store 形态） |
| 301548 | 1048576 | 28% | provider total（事件流在同回合的写入值） |
| 357742 | 1048576 | 33% | loop 锚点（回合后段） |
| 386751 | 1048576 | 36% | 第二次读数（会话仍在增长） |

### 4.7 全量回归

- 两轮全量回归均 exit 1；第二轮完整输出落盘 847KB 备查（`go test ./cmd/aicli/commands/ -count=1 -skip TestRunProviderLogin_OpenAICompatibleCodexModelsUseCodexTemplate -timeout 900s`）：
  - 结果：**`FAIL cmd/aicli/commands 255.302s`，exit 1**；
  - 定性：日志中**不存在测试级 `--- FAIL` 行**，唯一致命错误是 `fatal error: runtime: cannot allocate memory`（Go 运行时 OOM，整包测试二进制被终止）——属于本机资源限制/环境问题，不是链路断言失败；
  - 背景：该包全量回归此前（2026-10-11）已受并发工作流（事件日志 flush 重构）阻塞；本次工作区仍有大量未提交改动，且本机同时运行多个 aicli/go 进程。
- 结论：链路验证以定向测试（§4.2，全绿）+ 上下文相关子集（§4.7.1）+ 实机数据（§4.3-4.6）为准；全量回归建议在低负载环境/CI 复跑。
- §4.7.1 上下文相关子集复跑：`go test ./cmd/aicli/commands/ -count=1 -run 'Context|Token|Status|Compact' -timeout 600s` → ✅ `ok cmd/aicli/commands 22.506s`（exit 0）。与 §4.2 的定向用例合计，链路相关测试全部通过；唯一的红色是全包二进制在本机 OOM（环境问题）。

## 5. 验证发现与结论

### 5.1 读数归属：28% 来自哪条路径

`used_tokens` / `context_percent` 是 web 状态栏 API（`GET /web/api/statusbar`）的响应字段（`web_statusbar.go:64-83`），其取值与 TUI 状态栏**同源**（`web_statusbar.go:142-148` 调用 `chatSurfaceContextUsedStatusSegment` + `resolveChatStatusContextUsedTokens`）。因此两个观察读数（0% / 28%）都是同一 resolver 在不同时刻的输出。

28% 读数的机制定位：观察时刻 `used_tokens=301,548` 恰等于事件流中 `03:54:04.629Z` 的 `usage_total_tokens`（prompt 300,799 + completion 749），而同一事件里的锚点是 `context_anchor_tokens=299,261`（若 tier1 命中锚点，`used_tokens` 应显示 299,261 而不是 301,548）。结论：该时刻 **tier1（快照锚点）未命中**，落到 **tier2 `session.ContextTokenCount`**——即事件桥按 provider total 写入的内存快照路径；`(301548-12000)/(1048576-12000)=27.93% → 28%`。

### 5.2 发现的新缺陷（P0 候选）：canonical 陈旧值遮蔽实时锚点

**现象**：状态栏/恢复路径读到陈旧的小值（本会话 `2395 → 0%`），而实时锚点为 35 万+。

**证据**：
- store 双键并存且分歧：`context_token_count=2395`（canonical）vs `aicli_context_token_count=358568`（legacy，且持续增长到 386751）；
- 探针测试（复现 store 形态）：`resolveChatStatusContextUsedTokens = 2395`、`restoreChatContextTokenUsage → 2395`；对照 `percent(358568)=33%`；
- 跨会话 14 条中仅本会话双键分歧（其余会话 canonical 缺失、仅 legacy）——说明该形态由“新双写路径 + 单边跳过”组合产生，新会话常见。

**根因链**（三处组合）：
1. **loop 只写 legacy 键**：`setRuntimeSessionObservedTokenUsage` 直接写 map 的 `aicli_context_token_count`（`actor.go:2180-2192`），结构化身份 7 键也不含 canonical token 计数；
2. **CLI sync 单边跳过**：结构化锚点存在时跳过 canonical 回写（`chat_session.go:1542-1548`），canonical 永久停留在“锚点出现之前”的最后一次 pre-anchor 写入（本会话 = 首请求前估算 2395）；窗口键不受影响（`:1549-1553` 总是写）；
3. **读取端 canonical 优先**：`sessionmeta.CandidateKeys`/`Value`（`sessionmeta.go:172-196, 310-321`）按 `[context_token_count, aicli_context_token_count]` 顺序取值 → 命中陈旧 canonical。

**影响面**：状态栏（TUI/web）used/percent；恢复路径 `restoreChatContextTokenUsage`（污染 `ContextTokenCount`，进而影响 tier2 与本地判定）；只要 store 里 canonical 陈旧，**重启/恢复后读数会稳定停在陈旧小值**，且后续 provider usage 无法通过 tier1 纠正（tier1 恒命中）。

**修复建议（按推荐度）**：
- **A（推荐）**：让 loop 观测写入时保持双键一致，例如 `setRuntimeSessionObservedTokenUsage` 改用 `sessionmeta.Set(ctx, sessionmeta.ContextTokenCount, value, chatRuntimeContextContextTokenCount)`；CLI sync 的“锚点存在即跳过”保持不变。这样任何读取顺序都得到同一真值。
- **B（备选，兼容历史）**：sync 跳过回写时，若发现 canonical 与 legacy 不一致则删除 canonical（读取自然回落到 legacy 锚点）；或把 legacy 锚点值复制到 canonical。
- **C（纵深防御）**：读取端对“结构化锚点”提供显式解析（优先 legacy 锚点/结构化键），绕开 canonical-first 顺序（**已实施：见 §5.7**）。
- **回归测试**：固化本次探针形态（canonical 陈旧 + legacy 锚点 + fingerprint 并存），断言 `resolveChatStatusContextUsedTokens` 与 `restoreChatContextTokenUsage` 返回锚点值（现有 `TestSyncRuntimeSessionFromChatPreservesLoopContextAnchor` 未覆盖双键并存场景）。
- **可观测性增强**：状态栏调试输出记录 used 的解析来源（anchor-canonical / anchor-legacy / snapshot / estimate），使“0% vs 28%”一类读数争议可直接归因（**已实施：`/web/api/statusbar` 新增 `used_tokens_source` 字段，见 §5.7**）。

**修复状态（2026-10-12）**：已按 A + B′ 组合落地并新增回归测试，见 §5.6。

### 5.3 其它验证观察

- **无锚点首请求**：本会话 `no_anchor=3`、`estimated=5`——新会话前几次请求必然走估算；`context.usage_over_budget=0` 说明投影门限在本会话未触发超预算路径（残余风险的监控信号工作正常）。
- **渲染抑制与计量解耦**：debug.log 有 138 行 `render suppressed`（turn 所有权仍保护渲染路径），而计量路径已前移到所有权门之前（`chat_runtime_events.go:5822-5828`）——本会话 268 次 `anchored` 投影证明 usage 未被抑制。
- **compact 边界**：压缩成功后 loop 清空内存锚点（`clearContextLedger`），store 锚点保留，下一请求重新锚定；跨 compact 的短暂展示窗口属已知语义。

### 5.4 验证局限

- 无法从留存工件还原状态栏两个读数的精确时序载体（哪个进程/哪次刷新显示 0%、哪次显示 28%）；`0%` 的两种成因（首请求前无数据 / canonical 影子）均成立，其中后者已确定性复现。
- 全量回归受工作区并发工作流影响（见 §4.7），链路相关结论以定向测试 + 实机数据为准。

### 5.5 结论

1. 上下文计量更新链路本身完整且按设计工作：loop 每次调用锚定（268 次 anchored）、事件桥在所有权门外记账、状态栏三级取值、同步防覆写均已验证。
2. 阻断“读数始终可信”的最后一环是 **canonical/legacy 双键不一致 + canonical 优先读取**（§5.2）；修复面很小（单点写入一致性 + 一条回归测试），建议作为 P0 跟进。
3. 验证完成后已按 §5.2 的 A + B′ 组合落地修复并新增两条回归测试（见 §5.6）；临时探针与脚本已清理（见 §6）。

### 5.6 修复实施记录（2026-10-12）

按 §5.2 的 **A（loop 双键同写）+ B′（sync 对齐陈旧 canonical）** 落地：

| 文件 | 改动 |
|---|---|
| `backend/internal/chat/actor.go` | `setRuntimeSessionObservedTokenUsage` 改为**规范化双键写/删**：loop 观测同时写 `aicli_context_token_count`（legacy）与 `context_token_count`（canonical，新常量 `aicliRuntimeContextCanonicalTokenCountKey`）；清零时双键同删。注：`internal/chat` 不能直接 import `internal/sessionmeta`（`sessionmeta/patch.go:8` 反向依赖 `internal/chat`，会成环），常量与注释对齐维护 |
| `backend/internal/chat/backtrack_actor.go` | backtrack 清理锚点键列表加入 canonical 键（避免回退后读侧命中旧的 canonical 快照） |
| `backend/cmd/aicli/commands/chat_token_usage.go` | 新增 `runtimeSessionContextRawInt`：逐字读取单一 context 键，绕开 canonical-first 解析，供锚点对齐使用 |
| `backend/cmd/aicli/commands/chat_session.go` | `syncRuntimeSessionFromChatMode` 在“结构化锚点存在”分支新增对齐：用 loop 锚点（逐字读取）重写 canonical+legacy 双键；历史会话在下一次 sync（resume/回合收尾）即被修复 |

新增回归测试：

| 测试 | 断言 |
|---|---|
| `internal/chat/actor_test.go` `TestStoreRuntimeSessionContextObservationWritesCanonicalTwin` | 观测后双键同为锚点值；`setRuntimeSessionObservedTokenUsage(0)` 后双键同删 |
| `cmd/aicli/commands/chat_setup_test.go` `TestSyncRuntimeSessionFromChatAlignsStaleCanonicalContextCount` | canonical=2395 陈旧 + legacy=358568 锚点形态下，sync 后 canonical-first 读取与 `resolveChatStatusContextUsedTokens` 均返回 358568 |

验证（2026-10-12，全部通过）：

```bash
go build ./cmd/aicli/                                                                           # ✅
go test ./internal/chat/ -run 'TestStoreRuntimeSessionContextObservationWritesCanonicalTwin|TestRuntimeSessionActiveContextTokens|TestBacktrack' -count=1   # ✅ ok internal/chat 4.320s
go test ./cmd/aicli/commands/ -run 'TestSyncRuntimeSessionFromChat|TestRestoreChatStateFromRuntimeSession|TestRestoreChatPersistenceState|TestChatRuntimeEventBridge_ContextUsageAppliesDespiteTurnMismatch' -count=1   # ✅ ok 2.763s（含新增用例）
go test ./cmd/aicli/commands/ -run 'Context|Token|Status|Compact' -count=1                       # ✅ ok 13.319s
```

生效范围与未做项：

- 新观测双键恒一致；历史双键分歧会话在下一次 sync 时对齐（已打开旧进程的内存快照需 resume/重启后生效）。
- 读取端纵深防御（§5.2-C）与 used 解析来源可观测性已在续作中实施，见 §5.7。
- 本次改动以独立提交入库（6 个代码/测试文件 + 本文档），与工作区里的其它并发工作流改动隔离。

### 5.7 读取端纵深防御与来源可观测（2026-10-12 续作）

对应 §5.2-C 与“可观测性增强”两项后续：

| 文件 | 改动 |
|---|---|
| `backend/cmd/aicli/commands/chat_token_usage.go` | 新增 `runtimeSessionAnchoredContextInt`（带来源解析）：结构化锚点存在时**逐字读取 legacy 锚点**（`anchor_legacy`），绕开 canonical 优先顺序；无结构化锚点保持 canonical → legacy 历史顺序。`restoreChatContextTokenUsage` 的上下文键改用该解析（恢复路径同样获得纵深防御） |
| `backend/cmd/aicli/commands/chat_status.go` | `resolveChatStatusContextUsedTokens` 拆出 `resolveChatStatusContextUsedTokensWithSource`（返回 used + 来源），原函数保持向后兼容签名 |
| `backend/cmd/aicli/commands/web_statusbar.go` | `GET /web/api/statusbar` 响应新增 `used_tokens_source` 字段（`anchor_canonical` / `anchor_legacy` / `snapshot` / `estimate`），状态栏读数来源可直接归因 |

新增测试（commands）：

- `TestResolveChatStatusContextUsedTokensWithSource`：结构化锚点 + 陈旧 canonical → `(358568, anchor_legacy)`；web 快照 `used_tokens` / `used_tokens_source` 同步断言；无结构化锚点保持 `(2395, anchor_canonical)`；快照回退 `snapshot`；全空回退 `estimate`。
- `TestRestoreChatContextTokenUsagePrefersStructuredAnchor`：恢复路径读取结构化锚点（358568）而非陈旧 canonical（2395）。

验证（2026-10-12）：

```bash
gofmt -l <4 个改动文件>                            # ✅ 无输出
go build ./cmd/aicli/                              # ✅
go test ./cmd/aicli/commands/ -run 'TestResolveChatStatusContextUsedTokensWithSource|TestRestoreChatContextTokenUsagePrefersStructuredAnchor|TestSyncRuntimeSessionFromChatAlignsStaleCanonicalContextCount|TestRestoreChatPersistenceState_LoadedSessionRestoresExplicitContextUsage|TestSyncRuntimeSessionFromChatPreservesLoopContextAnchor' -count=1 -v
# ✅ 5/5 PASS（含两条新增）
go test ./cmd/aicli/commands/ -run 'Context|Token|Status|Compact' -count=1   # ✅ ok 11.677s
```

效果：即使某会话 canonical 与 legacy 双键分歧（sync 尚未对齐），状态栏与恢复路径也会优先使用结构化锚点真值；web 状态栏可一眼确认 used 的解析来源。

**WSL 实机数据验证（2026-10-12，真实 store）**：在 WSL（go1.26 linux/amd64）用真实会话 `session_20261012111114_8DyOk3e2` 的 store 快照（当时 canonical=2395 / legacy 锚点=528,380 / fingerprint 存在）跑修复后的代码路径（临时验证测试，验证后已删除）：

```text
real store: canonical=2395(present=true) legacyAnchor=528380(present=true) fingerprint=true
resolveChatStatusContextUsedTokensWithSource => used=528380 source=anchor_legacy
restoreChatContextTokenUsage => ContextTokenCount=528380
after sync: canonical=528380, resolver=528380
--- PASS: TestZZWSLRealStoreContextShape (0.10s)
```

WSL 侧其余验证（全部通过）：

```bash
go build -o /tmp/aicli-wsl-verify ./cmd/aicli/                                                 # ✅ BUILD=0（Linux 构建）
go test ./internal/contextledger/ -count=1                                                     # ✅ ok
go test ./internal/agent/ -run TestReActLoop_MidTurnCompactionAnchorsOnProviderUsage -count=1  # ✅ ok
go test ./cmd/aicli/commands/ -run 'Context|Token|Status|Compact' -count=1                      # ✅ ok 6.720s
go test ./cmd/aicli/commands/ -run 'TestResolveChatStatusContextUsedTokensWithSource|TestRestoreChatContextTokenUsagePrefersStructuredAnchor|TestSyncRuntimeSessionFromChatAlignsStaleCanonicalContextCount|TestSyncRuntimeSessionFromChatPreservesLoopContextAnchor' -count=1 -v   # ✅ 4/4 PASS
```

（说明：WSL 网络无法访问 `proxy.golang.org`，依赖下载使用 `GOPROXY=https://goproxy.cn,direct GOSUMDB=off`。）

## 6. 附录：验证命令与证据

### 6.1 命令清单（工作目录 `backend/`）

```bash
go build ./cmd/aicli/
go test ./internal/contextledger/ -count=1
go test ./internal/agent/ -run TestReActLoop_MidTurnCompactionAnchorsOnProviderUsage -count=1
go test ./internal/chat/ -run TestRuntimeSessionActiveContextTokensUsesStructuredAnchor -count=1
go test ./cmd/aicli/commands/ -run 'TestChatRuntimeEventBridge_ContextUsageAppliesDespiteTurnMismatch|TestFormatRuntimeLLMRequestDebugInfoIncludesContextProjection|TestBuildLocalChatLoopConfig_ContextAccountingMode|TestSyncRuntimeSessionFromChatPreservesLoopContextAnchor' -count=1
go test ./cmd/aicli/commands/ -count=1 -skip TestRunProviderLogin_OpenAICompatibleCodexModelsUseCodexTemplate   # 全量回归，结果见 §4.7
```

### 6.2 实机证据路径（WSL）

- 会话日志：`~/.aicli/chat-logs/2026/10/12/session_20261012111114_8DyOk3e2/`（`events/runtime-events.jsonl` 51,894 行、`debug/debug.log`、`chat/chat.json`）
- runtime store：`~/.aicli/sessions/runtime/chat_runtime.sqlite`（actor 状态/事件）
- 会话元数据：`~/.aicli/sessions/session_history.sqlite` → `sessions.metadata_json.context`
- 取证脚本与输出（临时，验证后清理）：`.tmp_verify_session*.py` / `.tmp_verify_*_out.txt`

### 6.3 文档生成说明

- 本文由本轮“链路分析 + 逐步验证”工作产出；所有行号为 2026-10-12 工作区实测（含未提交的并发改动）。
- 与 2026-10-11 方案的关系：本文是对 `docs/compact/provider-usage-first-auto-compact-implementation-plan-20261011.md` §10-§12 落地结果的**独立复核**，新增 §5.2 缺陷发现与探针证据、§5.6 修复实施记录（2026-10-12）。
