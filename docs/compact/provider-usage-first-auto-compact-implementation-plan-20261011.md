# Provider 用量优先的上下文计量与自动压缩触发 实施方案

- 日期：2026-10-11
- 事故样本：`session_20261011081831_m6tv296u`（runtime-server，262K 窗口，146 步长回合最终被 provider 以 400 拒绝）
- 关联文档：`docs/compact/README.md`（Auto Compact 实施方案，其中已声明"ctx used 由 provider usage 确认"的设计口径）

## 0. 结论摘要

当前实现并不是"有意选择本地计算器作为主口径"，而是**接线不全的历史遗留**：

1. 发送前判定（preflight gate / auto-compact trigger）是"先决策、后发送"的前置问题，T2 的真实 token 数在发送前只有 provider 才能给出；早期实现因此用本地估算器作为唯一可用信号。
2. provider 上报的 usage 后来接入时，只接了三处：计费台账、CLI 状态栏展示、以及 chat actor 的"上一回合结束 → 下一回合开始"的 pre-turn 锚点；**真正发请求的 agent loop 层完全不读锚点**。
3. `compactruntime.Request.ObservedTokens` 字段本来支持"观测优先"，但调用方把**估算值**填了进去（`loop.go` 的 `ObservedTokens: promptTokens`），字段语义被错配，观测优先逻辑形同虚设。
4. 服务端锚点只在**回合成功结束**时写入，长回合（本事故 146 步）内从不更新；失败回合（正是最需要它的场景）还不写。

本方案的目标口径与用户提出的一致：

> **观测为锚、估算为增量与兜底（Observe-first, Estimate-for-delta）。**
> 本地 token 计算只承担三件事：① 无 provider usage 时的兜底；② 计算"自锚点以来的增量 Δ"；③ 预算/成本预估。任何"是否超限、是否压缩"的判定必须使用 `P = anchor + Δ`，而不是全量估算 `E`。

## 1. 背景

### 1.1 事故证据（已核实）

模型 `inclusionai/ling-3.1-flash:free`（provider `commandgo`），窗口 262,144（262K），prompt budget 235,929（窗口 × 0.9），对齐工具 schema 后的估算触发线 214,466。

| step | 本地估算（`prompt_layout_total_tokens`） | provider 实际（`usage_prompt_tokens`） | 结果 |
|---|---|---|---|
| 143 | 168,555 | **258,789** | 成功 |
| 144 | 169,498 | **260,174** | 成功 |
| 145 | 170,441 | **261,559** | 成功 |
| 146 | 171,384 | 262,948 | **HTTP 400：input (262948 tokens) is longer than the model's context length (262144 tokens)** |

- 状态栏 "Context 64% used"（171,384/262,144）用的是**纯内容布局指标**（`prompt_layout_total_tokens`：只统计消息文本，不含 tool_calls / ContentParts / 工具 schema），并非发送门限口径；门限的实际估算为 223,594（85.3%），provider 实际 262,948（100.3%）。口径差异本身也是事故的一部分（见 §11）。
- 压缩评估事件只有 3 条（会话开头）：`session_compact_skipped, reason=below_limit, token_before=2,593/25,184/25,504, trigger=235,929/214,466`；此后整个长回合没有任何 mid-turn 压缩事件（估算未超预算时静默早退）。
- 事后恢复路径也未触发：`IsContextWindowError` 的匹配列表不含本次错误文案（`is longer than the model's context length`），`provider_context_window_recovery` 分支（`loop.go:964`）未进入，事件流中无 `session_compact_started/failed`。
- 估算偏差的精确口径（2026-10-11 复核，见 §11）：发送门限估算 223,594 vs provider 计数 262,948，低估 ≈ **1.18x**。估算器对**线上 JSON 报文**的实测（折算全量 ≈253K-281K，偏差 ±7% 内）与 provider 计数接近，说明缺口主要来自"估算解析后的内容 vs provider 计线上序列化报文"的 JSON 转义/结构开销（约 15-25%），而非 profile 整体失准。
- 把 1.18x 代入触发线：`0.9 × 262,144 × 1.18 ≈ 278K` —— 估算口径下需要约 278K 真实 token 才会触发，仍高于 262K 硬窗口，**估算触发线在该内容构成下依然无法先于硬窗口生效**。

### 1.2 为什么现状用了本地计算器（直接回答设计疑问）

| 层 | 现状 | 为什么 |
|---|---|---|
| preflight gate（发送前预算门限） | 全量估算（`estimatePromptMessageTokens` + 工具 schema 估算），`loop.go:5898-5923` | 发送前唯一全局可得信号就是估算；早期实现先落地了这一层 |
| mid-turn auto-compact trigger | 全量估算；`observedUsage` 只用于 eligibility，不参与阈值比较（`loop.go:6215-6219`、`6349-6354`） | provider usage 是"上一请求"的事后数据；接线时没有引入"锚点 + 增量"投影，直接沿用估算 |
| compact runtime | `Request.ObservedTokens` 支持观测优先（`runtime.go:174-177`），但调用方填的是估算值（`loop.go:6282`） | 字段语义未被强制，调用方把 `promptTokens`（估算）误当作 observed 传入 |
| chat actor pre-turn | 已实现锚点机制：`runtimeSessionActiveContextTokens` = 锚点 + 待估算增量，且 `max(锚点, 全量估算)`（`actor.go:2179-2190`） | 设计正确，但 ① 锚点只在回合成功结束时写（`actor.go:3030-3037`）；② 该函数只被 pre-turn 调用，agent loop 不读 |
| 状态栏 / context% | 服务端会话走估算；CLI 部分路径已接 provider usage（`applyChatContextTokensFromUsage`） | 与判定层同源问题；`docs/compact/README.md` 声明的"provider 确认口径"只覆盖了部分路径 |

结论：**设计上应该观测优先**，现状是"估算先行、观测后补且只补了一半"。修复方向不是发明新机制，而是把已有的锚点设施（`runtimeSessionActiveContextTokens` 的公式）提升为全局唯一口径，并补齐三处接线：agent loop 读/写锚点、compact 请求传真实观测值、锚点按次而非按回合更新。

### 1.3 现有可复用设施（不需要从零建设）

- 观测锚点存储：runtime session context 键 `aicli_context_token_count`（`actor.go:37, 2154-2177`），随 session 持久化。
- 锚点投影公式：`runtimeSessionActiveContextTokens`（`actor.go:2179-2190`）与增量估算 `countRuntimeChatPendingTokensAfterLastModelGenerated`（`actor.go:2192-2211`）。
- provider 完整输入量：`types.TokenUsage.InputTotal()`（含缓存命中/写入，兼容 Anthropic 不含式口径），`internal/types/token.go:88-96`。
- 用量来源标记：`usage.UsageSource`（`provider_reported` / `local_estimate`），`loop.go:2886-2935, 3020-3033`。
- prompt 前缀指纹：`promptMessageFingerprint`（`loop.go:5569`）、`promptHistoryPrefixChanged`（`loop.go:5439`）、`promptCacheEpoch`（loop 内与持久化已存在）。
- 超窗恢复框架：`IsContextWindowError` + `trySessionCompactionRecovery`（`loop.go:958-1010`），只需让匹配生效。
- CLI 侧 provider 口径应用：`applyChatContextTokensFromUsage`（`chat_token_usage.go:111-127`）、`restoreChatTokenCount`（读取 runtime session 上下文键）。

## 2. 目标与非目标

### 2.1 目标

1. 建立会话级 **Context Ledger（观测锚点）**：以最近一次 provider 上报的完整输入 token 数为锚，本地估算只负责增量 Δ 与兜底。
2. preflight gate、pre-turn/mid-turn auto-compact trigger、compact 请求的 `ObservedTokens`、状态栏 context% 四处判定统一使用同一投影 `P = anchor + Δ`。
3. 锚点按**每次 LLM 调用**更新（成功即更新；失败回合不丢上一次有效观测），而非只在回合成功结束时更新。
4. 让 provider 超窗 400 能被识别、停止无意义重试、并触发"压缩后重试"恢复。
5. 全链路可观测：事件与日志同时携带 `estimate_tokens / anchor_tokens / projection_tokens / projection_source`，任何一次"below_limit 跳过"可离线解释。

### 2.2 非目标

- 不改变压缩算法本身（本地/远程适配器、replacement history 构造不变）。
- 不引入第三方 tokenizer（BPE 词表体积问题依旧用启发式估算兜底；本方案只纠正"口径"，不追求估算器精确到 1%）。
- 不改变 canonical transcript 追加写语义与 SQLite 存储结构。
- 不改动用户可见的命令/配置语义；新增配置仅一个 kill-switch。

## 3. 设计

### 3.1 核心模型：T1 → 校正 → T2 投影

用户提出的目标流程形式化如下：

```
T1 发送前:  P1 = E(h1)                     （无锚点，退化路径）
T1 发送:    provider 实际计数 U1（事后可得）
T1 返回:    anchor ← U1,  anchorEstimate ← E(h1),  anchorFingerprint ← fp(sent messages), anchorEpoch ← promptCacheEpoch
T2 发送前:  Δ = max(0, E_msg(h2) − anchorMessageEstimate)        （增量估算，仅 messages 口径）
            P2 = max(anchor + Δ, E_msg(h2) + toolTokens(h2))     （工具面变化由 floor 兜住）
            判定: P2 与 inputBudget / trigger 比较 → 需要则先压缩再发送
T2 发送:    provider 实际计数 U2 → anchor ← U2 …（循环）
```

关键点：

- **锚点是"真实绝对值"**：`anchor = usage.InputTotal()`（完整输入，含缓存命中；兼容 Anthropic 不含式口径）。
- **估算只做差值**：`Δ` 是两次估算之差，绝对误差远小于全量估算（内容越接近，差值越准）；即使单次估算存在 ~1.2x 的线上开销偏差，只要偏差比例稳定，`anchor + Δ` 仍贴合真实值，且每次调用后锚点都会重新校正。
- **保守下限**：`P = max(anchor + Δ, E)`。当锚点缺失/异常时自动退化为 `E`（与现状行为一致）。
- **判定与展示同源**：状态栏、事件、gate、trigger 全部使用同一个 `P`，杜绝"UI 64%、真实 100%"的割裂。

### 3.2 数据结构与不变量

```go
// internal/contextledger/ledger.go（新增，纯逻辑、无外部依赖、可单测）
type Observation struct {
    PromptTokens         int    // provider 上报的完整输入量（InputTotal）
    MessageEstimate      int    // 观测时"仅 messages"的本地估算（不含工具 schema）
    Fingerprint          string // fp(sent messages)，发送时冻结
    MessageCount         int    // 发送时消息条数（前缀校验用）
    ToolFingerprint      string // ToolDefinitionsFingerprint(sent tools)
    Epoch                int    // 实际请求使用的 requestCacheEpoch
    Provider             string
    Model                string
    Source               string // "provider_reported"
    ObservedAt           time.Time
}

type Ledger struct{ last Observation; ok bool }

// Project 返回当前 prompt 的投影 token 数与来源。
//   anchored   = max(anchor + max(0, E_msg_now − MessageEstimate), E_msg_now + toolTokens_now)
//   estimated  = E_now（无有效锚点时）
func (l *Ledger) Project(now EstimateInput) (tokens int, source string)

// Observe 仅在 source == "provider_reported" 且 PromptTokens > 0 时生效。
func (l *Ledger) Observe(obs Observation)

// 失效条件（任一命中即回到 estimated）：
//   1. Provider/Model 变化；
//   2. Epoch 变化（compact / 缓存代际断裂 / 历史重写）；
//   3. 当前历史的前 MessageCount 条消息指纹 != Fingerprint（前缀被改写）；
//   4. 工具面指纹变化（ToolFingerprint != 当前 ToolDefinitionsFingerprint）；
//   5. PromptTokens <= 0 或明显异常（如 > 4 × E_now，可配置上限）。
```

不变量：

- **I1（判定口径）**：任何发送前判定（preflight gate、pre/mid-turn trigger）只允许使用 `P`；`E` 只作为 `P` 的组成部分出现。
- **I2（按次校正）**：每次收到 `usage_source=provider_reported` 且 `InputTotal()>0` 的响应，立即更新锚点；成功/失败回合一视同仁（失败回合保留最后一次有效观测）。
- **I3（失效优先）**：前缀/代际/provider/model 任一变化，锚点立即失效，不允许"跨改写沿用旧锚"。
- **I4（兜底等价）**：无任何观测时，行为与当前实现完全一致（`P = E`），保证灰度与回退安全。
- **I5（保守性）**：`P >= E` 恒成立；宁可提前压缩，不允许因锚点异常导致低估。
- **I6（同源展示）**：状态栏、`llm.request.started/finished` 事件、compact 事件中的 token 字段都来自同一个 `P`。

### 3.3 判定规则（统一口径）

沿用现有触发线定义，仅替换"被比较的值"：

```
inputBudget = min(prompt budget, window − reserved output)     // 现有 budget 解析不变
trigger     = alignTriggerWithInputBudget(0.9 × window, inputBudget, toolSchemaTokens)  // 现有对齐不变

发送前 gate:      P > inputBudget          → 触发 preflight 压缩
mid-turn trigger: P > trigger              → 触发 mid-turn 压缩
pre-turn trigger: P > trigger              → 触发 pre-turn 压缩
状态栏:           percent = P / window
```

本事故复算（用真实数字验证设计有效性）：
- step 143 返回后锚点 = 258,789；step 144 前投影 `P ≈ 258,789 + Δ` > 235,929 → **mid-turn 压缩在 step ≤ 143 即触发**，不会走到 step 146 的 400。
- 反事实校验：若采用 `P = max(anchor + Δ, E)`，由于 `anchor` 本身就超过 trigger，触发只取决于锚点是否被写入，不再依赖估算器精度。

### 3.4 失效与回退规则

| 场景 | 行为 |
|---|---|
| provider 不报 usage（`usage_source=local_estimate` 或缺失） | 不更新锚点；`P = E`（现状） |
| compact 完成 / replacement history 替换 | `promptCacheEpoch++`（已有）+ 前缀指纹变化 → 锚点失效；用估算过渡到下一次 usage |
| 会话 resume / 进程重启 | 锚点与指纹、epoch 一起持久化在 session context；恢复时校验前缀，不匹配则丢弃 |
| 用户回退/撤销（backtrack） | 与现有 `aicli_context_token_count` 删除逻辑一致，删除全部 ledger 键（`backtrack_actor.go:263` 扩展） |
| provider/model 切换 | 键不同即失效；新 provider 首次调用走估算 |
| 子代理 | 各自 session/loop 独立 ledger，互不污染 |
| 锚点异常（0、负数、> 4×E） | 丢弃锚点并记录告警事件，回退估算 |
| 压缩请求自身消耗 | 压缩调用也返回 usage，但**不更新**业务锚点（内部操作，见 §4.2 的过滤条件） |

## 4. 实施步骤

> 分期原则：P0 全部是"接线 + 语义修正"，不改压缩算法；P1 是口径收敛与估算器校准；P2 是观测与回放。每步独立可测、可回滚。

### P0-1 新增 Context Ledger 并接入 agent loop（核心）

**现状**：`loop.go` 的 gate/trigger 只调用 `estimatePromptMessageTokens`（`5670-5675`），锚点设施在 `internal/chat` 内，agent 包不可见。

**改法**：

1. 新增 `backend/internal/contextledger/`（§3.2 的 `Ledger/Observation/Project/Observe`），纯逻辑，单测覆盖投影与失效矩阵。
2. `LoopReActConfig` 增加两个可选钩子（由 chat actor / CLI 注入，nil 时 loop 仅用内存锚点）：

```go
// ObserveContextUsage 把一次 provider 观测写回会话（供 actor/CLI 持久化）。
ObserveContextUsage func(observation contextledger.Observation)
// LoadContextUsage 读取会话上次持久化的观测（进程内 mid-turn 用内存缓存）。
LoadContextUsage func() (contextledger.Observation, bool)
```

3. **写入点（按次）**：在 `think()` 中，`usage = response.Usage.Clone()`（`loop.go:3020`）之后立即执行：

```go
if usage != nil && usage.UsageSource == "provider_reported" && usage.InputTotal() > 0 &&
    !isInternalCompactRequest(req) {           // §4.2：压缩内部请求不污染业务锚点
    obs := contextledger.Observation{
        PromptTokens:    usage.InputTotal(),
        MessageEstimate: estimatePromptMessageTokens(loop.llmRuntime, req.Messages),
        Fingerprint:     promptMessageFingerprint(req.Messages),
        MessageCount:    len(req.Messages),
        ToolFingerprint: ToolDefinitionsFingerprint(req.Tools),
        Epoch:           requestCacheEpoch,
        Provider:        req.Provider, Model: req.Model,
        Source:          usage.UsageSource,
    }
    loop.ledger.Observe(obs)
    if loop.config.ObserveContextUsage != nil { loop.config.ObserveContextUsage(obs) }
}
```

   注意：指纹与估算必须取 **实际发送的 `req.Messages`**（managedHistory 冻结之后），而不是 promptBuilder 的原始历史；两者在 reconcile 注入前缀后可能不同（`loop.go:2189-2219`）。

4. **读取点（发送前）**：新增一个统一入口，替换所有 `estimatePromptMessageTokens` 的判定用法：

```go
func (loop *ReActLoop) projectPromptTokens(messages []types.Message, tools []types.ToolDefinition) (tokens int, source string) {
    now := contextledger.EstimateInput{
        Estimate:     estimatePromptMessageTokens(loop.llmRuntime, messages) + estimateToolDefinitionTokens(loop.llmRuntime, tools),
        Fingerprint:  promptMessageFingerprint(messages),
        MessageCount: len(messages),
        Epoch:        currentPromptCacheEpoch,
        Provider:     loop.requestProvider(), Model: loop.requestModel(),
    }
    return loop.ledger.Project(now)   // 无效锚点 → (now.Estimate, "estimated")
}
```

5. **改造四处判定**：
   - `enforcePromptPreflightWithTools`（`5888-5923`）：`promptTokensBefore, source := loop.projectPromptTokens(messages, tools)`；metadata 增加 `projection_source / anchor_tokens / estimate_tokens`。
   - `tryActiveTurnSemanticCompaction`（`6198-6219`）：`promptTokens` 改为投影值；`observedUsage` 仅保留 eligibility 职责。
   - pre-turn 调用（`902-912`）：通过 `LoadContextUsage` 载入会话锚点后走同一投影（首步 `toolSchemaTokens=0` 的场景由投影内部统一加工具估算，调用方不再手工传 0）。
   - `trySessionCompactionRecovery`（`6067-6130`）的 `token_before` 同样改用投影值，便于离线对账。

### P0-2 修正 `compactruntime.Request.ObservedTokens` 语义

**现状**：`loop.go:6282` 传 `ObservedTokens: promptTokens`，而 `promptTokens` 是估算值——`MaybeCompact` 的观测优先逻辑（`runtime.go:174-177`）因此从未拿到真实数字。

**改法**：

1. 调用方一律传投影/观测值：`ObservedTokens: projectedTokens`（由 `projectPromptTokens` 产出），并在事件 payload 中区分 `observed_source`。
2. 字段语义收紧（可选但推荐）：`ObservedTokens` 重命名为 `ObservedPromptTokens`，注释明确"必须是 provider 观测或锚点投影，禁止传原始估算"，编译期强制所有调用方过一遍（编译器会指出 `loop.go`、`actor.go`、`chat_core.go` 三处）。
3. 同步修正 `internal/chat/actor.go:2561`（`countRuntimeChatContextTokens` 是纯估算，手工压缩入口）与 `cmd/aicli/commands/chat_core.go:470-481`（`resolveChatContextSnapshotTokens` 优先 `session.ContextTokenCount`，该值需保证已被 provider usage 校正）。
4. 压缩请求 `MetadataKeyInternalOperation == "compact"`（`runtime_test.go` 已固定该元数据）作为 §P0-1 第 3 步的过滤依据：内部压缩调用的 usage 只用于压缩结果自身的 `ReplacementTokenLimit` 计算，不写业务锚点。

### P0-3 服务端 actor：锚点按次写入 + pre-turn 投影升级

**现状**：`setRuntimeSessionObservedTokenUsage` 只在回合成功结束时调用（`actor.go:3030-3037`，条件 `execErr == nil`）；长回合内锚点从不更新，失败回合不写。

**改法**：

1. 在 `SessionActor` 构造 agent loop 时注入钩子：

```go
loopCfg.ObserveContextUsage = func(obs contextledger.Observation) {
    setRuntimeSessionObservedTokenUsage(session, obs.PromptTokens)          // 现有键，保留兼容
    setRuntimeSessionContextValue(session, ctxKeyTokenFingerprint, obs.Fingerprint)
    setRuntimeSessionContextValue(session, ctxKeyTokenEpoch, obs.Epoch)
    setRuntimeSessionContextValue(session, ctxKeyTokenProvider, obs.Provider)
    setRuntimeSessionContextValue(session, ctxKeyTokenModel, obs.Model)
}
loopCfg.LoadContextUsage = func() (contextledger.Observation, bool) { /* 读上述键 */ }
```

   新增键建议：`aicli_context_token_fingerprint`、`aicli_context_token_epoch`、`aicli_context_token_provider`、`aicli_context_token_model`；全部走 `session.Metadata.Context`，随 `persistSession` 持久化，`backtrack_actor.go:263` 的清理逻辑同步扩展为删除全部键。

2. **删除回合末锚点写入（审查新发现）**：现有 `setRuntimeSessionObservedTokenUsage(session, result.Usage.PromptTokens)` 写入的是 **run 级累计用量**（`totalUsage` 累加所有 step 的 prompt tokens，见 `loop.go:894/921/939`），语义上根本不是"当前上下文大小"——长回合会把锚点写成数倍于真实窗口的值（随后被 `max(anchor, E)` 的 floor 掩盖，或触发误压缩）。改为：锚点只由 loop 内"按次观测"写入（P0-1）；回合末不再写入，`execErr == nil` 门槛随之消失。**失败回合不得清除已有锚点**。
3. `runtimeSessionActiveContextTokens`（`actor.go:2179-2190`）升级为同一套投影：加入 fingerprint/epoch/provider/model 校验；保持 `max(anchor+Δ, E)` 公式。pre-turn compact（`actor.go:2321-2345`）与 `internal/chat` 内其他 compact 调用随之获得观测口径。
4. 持久化写入频率控制：按次写内存、按回合/每 N 步 `persistSession`（现有 persist 节奏不变，避免每步落盘）。

### P0-4 超窗错误识别与恢复（次因修复）

**现状**：`IsContextWindowError`（`provider_retry.go:104-123`）匹配列表不含事故文案 `The input (262948 tokens) is longer than the model's context length (262144 tokens)`，导致 ① 恢复分支（`loop.go:964`）不触发；② wrapper 重试策略对同 payload 的超窗请求缺少明确分类。

**改法**：

1. 扩展匹配与数值解析：

```go
var contextWindowExceededPatterns = []*regexp.Regexp{
    // 本次事故：The input (262948 tokens) is longer than the model's context length (262144 tokens).
    regexp.MustCompile(`(?i)input\s*\((\d+)\s*tokens?\)\s*is longer than the model'?s context length\s*\((\d+)\s*tokens?\)`),
    // 通用兜底：... longer than the (model's )?context length ...
    regexp.MustCompile(`(?i)longer than the (?:model'?s )?context length`),
}

// ParseContextWindowExceeded 返回 (inputTokens, windowTokens, ok)
func ParseContextWindowExceeded(err error) (int, int, bool)
```

2. `IsContextWindowError` 命中后：provider wrapper 将该错误标记为**确定性不可重试**（避免同 payload 反复重试），并保留类型化错误向上传递；`loop.go:958-1010` 现有恢复路径自然生效（`trySessionCompactionRecovery` + `Force: true`）。
3. 若错误中解析出 provider 实际窗口且小于 capability 窗口：记录 `provider_context_limit_observed` 事件（长期用于修正 model card；本事故两者一致，均为 262,144，无修正动作）。
4. 恢复成功后必须重置/失效锚点（replacement history 前缀已变），由 P0-1 的失效规则自动处理。

### P1-1 状态栏 / context% 口径收敛

**现状**：服务端会话状态栏（`web_statusbar.go:147` → `resolveChatStatusContextUsedTokens`，`chat_status.go:537-545`）优先 `session.ContextTokenCount`，回退估算；而服务端长回合内 `ContextTokenCount` 从不被 provider usage 刷新。

**改法**：

1. 服务端状态栏数据源改为与判定同源的投影值：
   - 首选：runtime session context 的锚点 + 当前投影（复用 `runtimeSessionActiveContextTokens` 升级版）；
   - 次选：`session.ContextTokenCount`（CLI 直连路径已有 provider 校正，`applyChatContextTokensFromUsage`）；
   - 兜底：估算。
2. 新增状态栏字段 `context_used_source`（`anchored|provider|estimated`），TUI/Web 可在调试模式下展示，避免再次出现"UI 64% vs 真实 100%"的不可解释差异。
3. `docs/compact/README.md` 第 15 行的既有设计声明（"ctx used 由 provider usage 确认"）保持不变——本项是让实现回归该声明。

### P1-2 估算器校准（代码/JSON 密集场景）

锚点方案落地后，估算器只影响"增量 Δ"与兜底，风险大幅降低；但仍建议校准：

1. 用本次事故请求体（`334,119 bytes`，捕获前缀 4.6% CJK，provider 计数 262,948）作为校准样本之一，补充到 `tokenestimate` 的校准语料说明。
2. 为代码/JSON/工具输出密集内容单独评估参数（当前 `ProfileGeneric: WordDivisor=6`、`SymbolMixedDivisor=3` 明显偏乐观）；若无法在不引入 BPE 词表的前提下拟合，则保持现状，但把 `ProfileFor` 对未知 provider 的兜底在文档中显式标注"仅兜底，不用于绝对值判定"。
3. 不追求把估算器做准到替代锚点；**观测优先是本方案的主体，校准只是让 Δ 更准**。

### P1-3 配置与灰度

1. 新增配置 `context.accounting_mode: provider_first | estimate_only`（默认 `provider_first`）：
   - `provider_first`：本方案行为；
   - `estimate_only`：完全回到旧行为，用于紧急回退与对照实验。
2. 读取位置建议与现有 chat/context 配置同级（`resolveContextBuildPromptBudget` 的配置来源），在 loop 构造时解析一次。

### P2 观测与回放

1. 事件字段扩展（`llm.request.started/finished`、`session_compact_skipped/started/completed`）：
   - `context_estimate_tokens`（E）
   - `context_anchor_tokens`（A，含 `anchor_source`）
   - `context_projection_tokens`（P）与 `context_projection_source`（`anchored|estimated`）
   - `context_projection_invalid_reason`（锚点失效原因：`epoch_changed|prefix_changed|provider_changed|no_anchor|anchor_anomaly`）
2. 把本次事故的 6 个关键数字（估算/真实 × step 143-146 + 触发线）固化为 `testdata` 回放样本（见 §5.2），纳入 CI。
3. 监控指标（落地后观测一周）：
   - provider 超窗 400 次数（目标：0）；
   - `usage_prompt_tokens > prompt_budget` 但仍成功发送的次数（目标：0，出现即说明 gate 仍有估算残留）；
   - `session_compact_skipped` 中 `projection_source=estimated` 的占比（衡量锚点覆盖率）；
   - 误触发率：`projection < trigger` 的压缩次数（目标：0）。

## 5. 测试计划

### 5.1 单元测试

| 模块 | 用例 |
|---|---|
| `internal/contextledger` | ① 无锚点 → `P=E`；② 锚点 + Δ 正常投影；③ 前缀改写（fingerprint 不匹配）→ 失效；④ epoch 变化 → 失效；⑤ provider/model 切换 → 失效；⑥ 锚点异常（0 / >4×E）→ 丢弃；⑦ `P >= E` 恒成立（property test） |
| `internal/agent`（loop） | ① mock provider 返回 usage=258,789 而估算=168,555，断言下一次 mid-turn 检查以投影值超过 trigger 并调用压缩；② 压缩内部请求（`MetadataKeyInternalOperation=compact`）不写锚点；③ 失败回合后锚点保留；④ 无 usage 时行为与旧实现一致（回归对照） |
| `internal/llm` | `IsContextWindowError` / `ParseContextWindowExceeded` 表驱动：事故原文、OpenAI `context_length_exceeded`、Anthropic `prompt is too long`、非超窗 400（`invalid_request_error` 其他文案）不得误判 |
| `internal/chat` | `runtimeSessionActiveContextTokens` 升级后：锚点 + pending Δ + 失效矩阵；`backtrack` 清理全部 ledger 键 |
| `cmd/aicli/commands` | 状态栏 `context_used_source` 三态；`applyChatContextTokensFromUsage` 既有行为不回归 |

### 5.2 事故回放回归（CI 必跑）

用 `session_20261011081831_m6tv296u` 的真实序列构造确定性回放：

```
窗口=262,144; prompt_budget=235,929; tool_schema≈21,463 → trigger=214,466
step 143: E=168,555, U=258,789
step 144: E=169,498, U=260,174
step 145: E=170,441, U=261,559
step 146: E=171,384, U=262,948（旧实现：HTTP 400）
```

断言（新实现）：
1. step 143 的 usage 返回后，step 144 的 mid-turn 检查投影 `P > 235,929` → 触发压缩（不再进入 step 146）；
2. 若强制跳过 mid-turn（模拟极端），preflight gate 在发送前以同一投影拦截；
3. 回放中不得出现 provider 超窗 400。

### 5.3 e2e

1. runtime-server e2e（参考 `scripts/acp_e2e_notifications.go` 的用法）：mock/录制 provider 返回高 usage，断言事件序列 `context.mid_turn_compact.started → completed`，且后续请求的 `usage_prompt_tokens` 回落。
2. CLI e2e：`aicli chat` 直连 provider，断言状态栏 `context%` 与 provider 上报一致（±Δ 估算误差）。
3. 回退验证：`context.accounting_mode=estimate_only` 时行为与修复前一致（对照实验通过）。

## 6. 验收标准

| # | 标准 | 验证方式 |
|---|---|---|
| 1 | 事故场景不再发生：当 provider 观测值 > trigger 时，下一步发送前必然触发压缩 | §5.2 回放断言 1/2 |
| 2 | 状态栏/事件中的 context 占用与 provider 口径一致（差异 ≤ Δ 估算误差） | §5.3 e2e 断言 2；P2 监控指标 |
| 3 | provider 不报 usage 时行为与修复前完全一致 | §5.1 回归对照 + `estimate_only` 对照 |
| 4 | 失败回合不清除已有锚点；resume 后锚点经前缀校验仍可用 | §5.1 用例 ③/④ |
| 5 | 超窗 400 不再被无意义重试，且能触发压缩恢复 | §5.1 表驱动 + e2e 断言 1 |
| 6 | 事件可离线解释每次 skip/trigger 的判定依据（E/A/P/source 四元组齐全） | P2 事件字段验收 |

## 7. 风险与回滚

| 风险 | 缓解 |
|---|---|
| 锚点被错误 usage 污染（provider 计数异常） | §3.4 异常阈值（>4×E 丢弃）+ `P=max(P,E)` 保守下限 + 告警事件 |
| 指纹计算引入每步额外 CPU | 只对"发送时冻结的 messages"做一次哈希（`think()` 内已有同等量级的序列化成本）；必要时按前缀缓存增量摘要 |
| 提前压缩导致缓存命中率下降 | 触发线沿用 0.9 窗口，观测口径只会让触发更贴近真实窗口（不会更早于旧估算口径——旧口径是低估，触发被推迟而非提前）；监控 cache hit 指标 |
| 持久化键增多导致 session 文件膨胀 | 4 个标量键，可忽略；按回合落盘节奏不变 |
| 行为变化引发回归 | `context.accounting_mode=estimate_only` kill-switch，一键回到旧行为 |

回滚路径：配置切换 `estimate_only`（无需回滚代码）→ 若问题在代码侧，回滚 P0-1/P0-2 两个提交即可恢复估算口径（P0-3/P0-4 可独立保留）。

## 8. 附录

### 8.1 关键代码位置索引（2026-10-11 版本）

| 位置 | 内容 |
|---|---|
| `backend/internal/agent/loop.go:5888-5923` | preflight gate（估算判定，待改投影） |
| `backend/internal/agent/loop.go:6198-6219` | mid-turn trigger（估算判定 + eligibility） |
| `backend/internal/agent/loop.go:6269-6287` | compact 请求构造（`ObservedTokens` 误填估算） |
| `backend/internal/agent/loop.go:902-912 / 1446-1456` | pre-turn / mid-turn 压缩调用点 |
| `backend/internal/agent/loop.go:2189-2219` | managedHistory 冻结（锚点指纹取样点） |
| `backend/internal/agent/loop.go:2886-2935, 3020-3033` | usage / usage_source 组装（锚点写入点） |
| `backend/internal/agent/loop.go:5569 / 5439` | `promptMessageFingerprint` / `promptHistoryPrefixChanged` |
| `backend/internal/compactruntime/runtime.go:44-46, 171-177` | `ObservedTokens` 字段与观测优先触发 |
| `backend/internal/chat/actor.go:2135-2211` | 估算器、锚点读写、`runtimeSessionActiveContextTokens` 投影 |
| `backend/internal/chat/actor.go:2321-2345` | actor pre-turn compact（已用观测） |
| `backend/internal/chat/actor.go:3030-3037` | 锚点仅回合成功结束时写入（待改按次） |
| `backend/internal/llm/provider_retry.go:104-123` | `IsContextWindowError`（待扩展） |
| `backend/internal/types/token.go:88-96` | `InputTotal()` 完整输入口径 |
| `backend/cmd/aicli/commands/chat_token_usage.go:111-127` | provider usage → context 展示（CLI 路径已接） |
| `backend/cmd/aicli/commands/chat_status.go:537-545` | 状态栏 context used 取值 |
| `docs/compact/README.md:15` | 既有设计声明："ctx used 由 provider usage 确认" |

### 8.2 变更清单（建议提交拆分）

1. `feat(contextledger): 新增观测锚点与投影`（纯逻辑 + 单测）
2. `fix(agent): preflight/compact 判定改用观测锚点投影，修正 ObservedTokens 语义`（P0-1/P0-2 + 回放测试）
3. `fix(chat): 锚点按次写入并持久化，pre-turn 投影校验失效`（P0-3 + 单测）
4. `fix(llm): 识别 provider 超窗 400 并接入压缩恢复`（P0-4 + 表驱动测试）
5. `feat(web): 状态栏 context% 收敛到投影口径`（P1-1）
6. `chore(tokenestimate): 代码/JSON 校准样本与文档`（P1-2）
7. `feat(config): context.accounting_mode kill-switch`（P1-3）
8. `feat(observability): E/A/P/source 事件字段与回放样本`（P2）

### 8.3 工作量估计

| 项 | 预估 |
|---|---|
| P0-1 + P0-2 | 1.5 天（含单测与回放） |
| P0-3 | 1 天 |
| P0-4 | 0.5 天 |
| P1 | 1 天 |
| P2 | 0.5 天 |
| 合计 | ≈ 4.5 人天 |

## 9. 实施前审查记录（2026-10-11）

> 对方案逐条对照真实代码核验后的修正与结论。**§9 优先于前文**：与前文冲突处以本节为准。

### 9.1 审查发现（按严重度）

| # | 发现 | 影响 | 处置 |
|---|---|---|---|
| A | **actor 回合末锚点写入的是 run 级累计用量**（`actor.go:3036` 用 `result.Usage.PromptTokens`，而 `result.Usage` 是 `totalUsage` 多步累加，`loop.go:894/921/939`）。语义错误：长回合会写入数倍于窗口的"锚点" | 现有 pre-turn 观测口径本身就是错的，可能误压缩或掩盖失效 | P0-3 已修正：删除回合末写入，锚点只按次写入（见 §4 P0-3 第 2 条） |
| B | 投影 Δ 若用"含工具 schema 的全量估算"做差，会在工具面变化/口径错位时失真 | 低估 Δ → 触发偏晚 | §3.1/§3.2 已修正：Δ 只用 messages 口径；工具变化由 `E_msg + toolTokens` floor 与工具指纹失效兜住 |
| C | 锚点有效性缺工具面校验：会话中途工具面变化（MCP 热重载、skill pin）会让旧锚点数值失真 | 错误锚点被沿用 | 观测新增 `ToolFingerprint`（复用 `ToolDefinitionsFingerprint`，`tool_surface_binding.go:17`），失效条件 +1 |
| D | 服务端状态栏的"provider usage → ContextTokenCount"事件桥（`chat_runtime_events.go:6386-6417`）在事故会话未生效（状态栏仍是估算值；debug.log 存在大量 `render suppressed reason="event turn does not match active run"`） | 即使判定修好，UI 仍可能显示估算值 | P1-1 改为**直读 runtime session 锚点**（`aicli_context_token_count`），不依赖事件桥；事件桥 gating 的深挖列为独立后续项 |
| E | `promptMessageFingerprint` 已每请求计算（`loop.go:2574` 存入 `req.Metadata["prompt_fingerprint"]`），`promptHistoryPrefixChanged`（`5439-5448`）已确立"前缀指纹比较"模式 | 无新成本模型，复用既有设施 | P0-1 观测复用 `promptMessageFingerprint(req.Messages)`；投影校验按 `promptHistoryPrefixChanged` 同款逻辑 |
| F | `LoopReActConfig` 已具备大量 `yaml:"-"` 回调字段的先例（`StreamSink`/`OnHistoryCheckpoint`/`OnProgress` 等，`loop.go:93-146`） | 钩子注入零架构风险 | 按既有模式加 `ObserveContextUsage` / `LoadContextUsage` 两个回调 |
| G | 服务端与 TUI 统一经 `SessionActor` → `historyCheckpointLoopConfig`（`actor.go:3544`）注入 loop 配置；CLI 未直接调用 `RunReActWithSession` | 注入点唯一、覆盖全部主路径 | 钩子注入放 `historyCheckpointLoopConfig`；`internal/agent/child_factory.go`（子代理）不注入，子代理仅用进程内 ledger |
| H | 测试脚手架已就绪：`SequenceLLMProvider`（多响应 + modelCapabilities）、`RetrySequenceLLMProvider`（先错后成）、`newTestHistorySession`/`testContextHistorySession`；已有 mid-turn / provider-context-error 恢复用例可扩展 | 回放测试可实现，无需新框架 | 新增 `internal/contextledger` 单测 + loop 回放测试 + `IsContextWindowError` 表驱动扩展 |
| I | kill-switch 若走 `runtimecfg.RuntimeConfig` 管道会扩大改动面 | 范围膨胀 | 本次以 `LoopReActConfig.ContextAccountingMode` 字段 + `agent.config.Options["context_accounting_mode"]` 读取实现（`estimate_only` 回退），正式配置化留 P1 |
| J | `IsContextWindowError` 命中后 `classifyLLMFailureCode` 将错误分类为 `CONTEXT_BUDGET_EXCEEDED`（`retry_policy.go:1019-1021`），事件 `error_category` 会从 `UPSTREAM_INVALID_REQUEST` 变为该值 | 行为变化，需在测试断言 | 属预期改进（可观测性更强），测试同步更新 |

### 9.2 实施范围（本次落地）

- ✅ P0-1：`internal/contextledger` + loop 观测/投影/四处判定接线
- ✅ P0-2：`ObservedTokens` 语义修正（传投影值；字段名保留、注释澄清）
- ✅ P0-3：actor 钩子注入 + 观测持久化 + 删除回合末累计写入 + `runtimeSessionActiveContextTokens` 校验升级
- ✅ P0-4：`IsContextWindowError` 扩展（事故文案 + 数值解析）+ 恢复路径测试
- ✅ P1-1：状态栏直读 runtime session 锚点
- ⏸ P1-2（估算器校准）、P1-3（正式配置化）、P2（事件字段与监控）→ 后续批次，本文档保留设计

### 9.3 验收口径（本次）

1. `go test ./internal/contextledger/... ./internal/agent/... ./internal/llm/... ./internal/chat/... ./internal/compactruntime/... ./cmd/aicli/...` 全绿（`cmd/aicli` 如过重则跑受影响包）。
2. 回放测试：mock provider 上报 usage=258,789/260,174/261,559 而估算 ≈168K，断言 mid-turn 压缩在超过 trigger 的下一步触发（不得发到 262,948）。
3. `IsContextWindowError` 对事故原文返回 true，且恢复路径可复用（复用 `TestReActLoop_ProviderContextErrorCompactsAndRetriesSameStep` 结构）。
4. `estimate_only` 模式下行为与修复前一致（对照测试）。

## 10. 实施记录（2026-10-11）

### 10.1 落地范围

| 项 | 状态 | 说明 |
|---|---|---|
| P0-1 Context Ledger + loop 接线 | ✅ 已落地 | 新增 `internal/contextledger`；`think()` 内按次观测；preflight gate / mid-turn trigger / recovery 事件统一走投影 |
| P0-2 `ObservedTokens` 语义修正 | ✅ 已落地 | 压缩请求传入投影值；字段名保留并补注释（`runtime.go`） |
| P0-3 actor 锚点按次持久化 | ✅ 已落地 | `historyCheckpointLoopConfig` 注入 hooks；结构化锚点 7 键持久化；删除回合末累计写入；`runtimeSessionActiveContextTokens` 升级为同一投影 |
| P0-4 超窗错误识别与恢复 | ✅ 已落地 | `IsContextWindowError` 新增事故文案；`ParseContextWindowExceeded` 解析 input/window；恢复事件带 `provider_reported_input_tokens/context_limit` |
| P1-1 状态栏口径 | ✅ 已落地 | `resolveChatStatusContextUsedTokens` 直读 runtime session 锚点 |
| P1-2/P1-3/P2/事件桥 | ✅ 第二轮已落地（2026-10-11，见 §11） | 估算器实测评估与数字修正、`context.accountingMode` 正式配置化、`context.usage_over_budget` 监控信号 + debug 日志 E/A/P 字段、事件桥 gating 修复（上下文计量不再受回合所有权抑制） |

### 10.2 变更清单（文件）

- `internal/contextledger/contextledger.go` + `_test.go`（新增）
- `internal/agent/loop.go`
  - `LoopReActConfig`：`ObserveContextUsage` / `LoadContextUsage` / `ContextAccountingMode`
  - `NewReActLoop`：默认 provider_first，`estimate_only` kill-switch（字段或 agent options `context_accounting_mode`）
  - `think()`：按次观测（仅 `usage_source=provider_reported` 且非 compact 内部请求）；`action.toolFingerprint`
  - `enforcePromptPreflightWithTools` / `tryActiveTurnSemanticCompaction` / `trySessionCompactionRecovery`：判定改投影，新增 epoch / 工具指纹参数
  - 压缩成功即清空内存锚点（前缀已重写，下一次 usage 重新锚定）
- `internal/agent/tool_surface_binding.go`：导出 `PromptMessageFingerprint`（跨层同指纹）
- `internal/chat/actor.go`
  - 结构化锚点读写（`aicli_context_token_fingerprint` 等 7 键）
  - `historyCheckpointLoopConfig` 注入 hooks
  - 删除回合末 `result.Usage`（累计值）写入
  - `runtimeSessionActiveContextTokens` 升级（结构化锚点走同一 Project；旧 token-count 锚点保留 legacy 回退）
- `internal/chat/backtrack_actor.go`：回退清理全部锚点键
- `internal/llm/provider_retry.go`：`IsContextWindowError` 扩展 + `ParseContextWindowExceeded`
- `cmd/aicli/commands/chat_status.go`：状态栏直读锚点

### 10.3 实施中发现并修正的增量问题（§9 之外）

1. **`frozenTurnToolSurface` 与请求实际工具面不一致**：回合冻结后叠加的 pinned/routing 工具不在冻结快照里；若用快照指纹做锚点校验，这些会话的锚点会被误判失效（退回估算，事故复现）。修正：`action.toolFingerprint` 记录**实际发送**的工具面指纹并贯穿 mid-turn 投影；pre-turn（工具面尚未冻结）传空指纹，校验自动跳过工具项（前缀/代际/模型仍校验）。
2. **异常护栏阈值**：8x 在"估算极端低估"的合法场景可能误杀锚点；按"拦截数量级垃圾（累计用量为 100x+）"的目标调整为 16x，并保留 `P=max(anchor+Δ, E)` 兜底。
3. **既有测试契约对齐**：`TestSessionActorSubmitPrompt_PublishesAssistantMessageBeforeSessionEnd` 原断言"回合末写入 run 累计 usage"；新架构改为按次写入后，测试桩 `MockLLMProviderForChat` 补齐 `usage_source=provider_reported`（与真实 provider wrapper 契约一致），断言语义保持不变。

### 10.4 验证证据

- `go test ./internal/contextledger/` ✅（投影/失效矩阵/异常护栏）
- `go test ./internal/agent/` ✅（含新增回放 `TestReActLoop_MidTurnCompactionAnchorsOnProviderUsage`：估算 2,174 / provider 上报 9,000 → mid-turn 触发 anchored 压缩，3 次请求，事件 `context_projection_source=anchored`、`context_anchor_tokens=9000`）
- `go test ./internal/chat/` ✅（含新增 `TestRuntimeSessionActiveContextTokensUsesStructuredAnchor`：锚点生效 + 工具面/前缀变化失效）
- `go test ./internal/llm/ ./internal/compactruntime/` ✅（含事故文案识别与数值解析）
- `go test ./cmd/aicli/commands/ -skip TestRunProviderLogin_OpenAICompatibleCodexModelsUseCodexTemplate` ✅
  - 该 skip 项为**既有失败**：HEAD 干净工作树复跑同样失败（provider login 的 codex model card 模板断言），与本次改动无关。

### 10.5 回滚方式

`LoopReActConfig.ContextAccountingMode = "estimate_only"`（或 agent options `context_accounting_mode=estimate_only`）即可整体回到修复前行为；代码级回滚为移除 P0-1/P0-2 两个提交，P0-3/P0-4/P1-1 可独立保留。

## 11. 第二轮实施记录（2026-10-11：P1-2 / P1-3 / P2 / 事件桥）

### 11.1 P1-3 正式配置化（已落地）

- `internal/config`：`ContextConfig.AccountingMode`（`context.accountingMode`，校验白名单 `"" | provider_first | estimate_only`）。
- `cmd/aicli/commands/chat_actor_host.go` `buildLocalChatLoopConfig`：读取 `runtimeConfig.Context.AccountingMode` → `LoopReActConfig.ContextAccountingMode`（未配置时留空，由 loop 回退到 agent options / 默认 provider_first）。
- `backend/configs/runtime.yaml`、`runtime.win7.yaml`：`context:` 段新增注释示例。
- 测试：`TestBuildLocalChatLoopConfig_ContextAccountingMode`。

### 11.2 P2 观测与监控信号（已落地）

1. **`context.usage_over_budget` 事件**（`loop.go` `emitContextUsageOverBudget`）：当 provider 上报的完整输入超过发送门限当时采用的预算（`effective_input_budget`/`prompt_budget`）时发布，携带 `provider_reported_input_tokens / input_budget / tool_schema_tokens` 与 E/A/P 投影字段。provider-first 生效后该事件应接近 0；一旦出现即说明"无锚点/锚点失效的首个超限请求"这一残余风险被触发（也即本次事故的同类信号）。
2. **debug 日志字段**（`chat_runtime_events.go` `appendContextProjectionDebugFields`）：`[llm-debug] request_started/finished` 行新增 `context_projection_tokens / context_projection_source / context_anchor_tokens / context_estimate_tokens / context_projection_invalid_reason`，单条日志即可解释任何发送门限判定。
3. 测试：回放测试新增 over-budget 断言；`TestFormatRuntimeLLMRequestDebugInfoIncludesContextProjection`。

### 11.3 事件桥 gating 修复（已落地）

**根因**：`handleEvent` 在回合所有权抑制（`shouldSuppressMismatchedPrimaryTurnEvent`）之后才调用 `applyLLMRequestStatus`；事故会话的 `llm.request.*` 事件因 turn 不匹配被整批抑制（debug.log 满屏 `render suppressed reason="event turn does not match active run"`），provider usage 因此从未写入 `ContextTokenCount`，状态栏只能回退到估算。

**修复**：
- `applyLLMRequestStatus` / `applySessionCompactStatus` 前移到所有权门之前——上下文计量是**会话级观测**，不是视口变更；回合所有权继续保护全部渲染路径。
- `applyLLMRequestStatus` 去掉 `isRunActive()` 前置条件（仅保留主会话身份校验）：迟到的 usage 报告按"最近一次确认值"记账，后续调用会再次校正。
- 测试：`TestChatRuntimeEventBridge_ContextUsageAppliesDespiteTurnMismatch`（turn-B 事件在 turn-A 为活动渲染所有者时仍写入 usage）。

### 11.4 P1-2 估算器实测评估与数字修正（重要）

复核从事件流提取了发送门限的真实数字（此前误用了"纯内容布局指标"）：

| step | 门限估算（msg+tool） | provider 计数 | 比值 |
|---|---|---|---|
| 143 | 220,118（198,655+21,463） | 258,789 | 1.176 |
| 144 | 221,274（199,811+21,463） | 260,174 | 1.176 |
| 145 | 222,438（200,975+21,463） | 261,559 | 1.176 |
| 146 | 223,594（202,131+21,463） | 262,948 | 1.176 |

- 真实低估系数 ≈ **1.18x**（非早期结论的 1.53x；后者来自把 `prompt_layout_total_tokens`（只统计 `msg.Content`，不含 tool_calls/ContentParts/工具 schema）误当作门限口径）。
- 估算器对**线上 JSON 报文**的实测（`backend/tools/estimate_eval`，读取事故请求捕获体）：`body_bytes=1,039,121`、捕获前缀 262,144 bytes；`ProfileGeneric` 对前缀 JSON 文本估算 63,833 tokens，折算全量 ≈253K-281K（视折算口径），与 provider 262,948 的偏差在 ±7% 内。
- 结论：偏差主因是**口径**而非 profile 失准——门限估算"解析后的消息内容 + 工具 schema"，provider 计"线上序列化报文"（JSON 转义/结构开销，escape-heavy 内容约 +15-25%）。
- 触发线复算：`0.9 × 262,144 × 1.18 ≈ 278K > 262K` —— 估算口径下的触发线依然无法先于硬窗口生效，**锚点方案的必要性成立**。
- 处置：
  - 不修改 `tokenestimate` 的 Profile 常量（实测其在正确口径下误差 ≤7%，且改动会广泛影响既有行为）。
  - `tokenestimate` 包文档新增"wire-format caveat"说明：估算只服务增量与兜底，绝对门限必须锚定 provider 用量。
  - 遗留可选优化（未实施）：无锚点首请求可引入可配置线上开销系数（`context.wireOverheadRatio`，默认 1.0，escape-heavy 场景建议 1.15-1.25）；当前以 `context.usage_over_budget` 事件 + 超窗 400 恢复兜底。

### 11.5 口径修正说明（对 §1.1 的修订）

- 状态栏事故读数 171,384 是**布局指标**，不是门限估算（223,594）；两者与 provider（262,948）的差距分别为 1.53x 与 1.18x。
- 事件中的 `total_tokens`/`context_prompt_tokens` 仍沿用布局口径（保持兼容与既有测试），但 `context_preflight.prompt_tokens_before` 与新增的 `context_projection_*` 才是门限/投影口径；分析事故时应以后者为准。

### 11.6 第二轮验证证据

- `go test ./cmd/aicli/commands/ -run "TestChatRuntimeEventBridge_ContextUsageAppliesDespiteTurnMismatch|TestFormatRuntimeLLMRequestDebugInfoIncludesContextProjection|TestBuildLocalChatLoopConfig_ContextAccountingMode"` ✅
- `go test ./internal/agent/ -run "TestReActLoop_MidTurnCompactionAnchorsOnProviderUsage"` ✅（新增 over-budget 断言）
- `go test ./internal/agent/`（全量）✅；`go test ./internal/config/ ./internal/contextledger/` ✅
- 评估工具复现：`go run ./tools/estimate_eval <事故请求捕获体> 262948` → `ProfileGeneric` 前缀估算 63,833，折算全量 253,030（byte-ratio）/280,610（rune-ratio），provider_ratio 0.962..1.067（±7% 内）。
- `go build ./cmd/aicli/... ./internal/config/` ✅
- **`cmd/aicli/commands` 全量回归暂被并发工作流阻塞（非本次改动）**：第二轮实施期间，工作区另一条工作流正在重构事件日志 flush 路径（`flushEventLog` 被删除、`event_persist_buffer` / `chat_actor_executor` / `chat_post_turn_reconcile` 等文件持续在改），期间出现包不可编译与约 20 个 `*EventLog*` / `*Replay*` 用例失败（如 `TestChatRuntimeEventBridge_EphemeralSupplementNotJournaled`：durable supplement 未落日志），失败域与上下文计量无交集；本次全部定向用例（上列）在该工作区状态下仍通过。建议待该工作流收敛后复跑 `go test ./cmd/aicli/commands/`。

## 12. 实机验证记录（2026-10-11，57718 实例）

**验证对象**：`aicli-3x.exe`（构建 11:37:54，二进制含 `context_projection_source` / `aicli_context_token_fingerprint` / `context.usage_over_budget` / `accountingMode` 标记）；会话 `session_20261011113800_l6hXN7tO`；provider `workbuddy/cn:hy3`（本地网关），窗口 192,000，prompt budget 172,800。

### 12.1 回合 1（无锚点退化路径）

- `llm.request.started`：`context_projection_source=estimated`、`context_projection_invalid_reason=no_anchor`、投影 25,408；
- `llm.request.finished`：`usage_prompt_tokens=24,523`、`usage_source=provider_reported`；
- 状态栏：`used_tokens=24,559`、`Context 7% used`（provider 口径；同一时刻的布局指标仅 3,933，证明状态栏已不再使用布局口径）；
- 无 `context.usage_over_budget` 事件（24,523 < 172,800，符合预期）。

### 12.2 回合 2（跨回合锚定路径）

- `llm.request.started`：`context_projection_source=anchored`、`context_anchor_tokens=24,559`、`context_delta_tokens=37`、投影 25,445（= `max(anchor+Δ, E)` 的估算 floor 生效）；
- 证明链路完整：loop 按次观测 → hooks 持久化（结构化 7 键）→ 下一回合 `LoadContextUsage` → 投影判定锚定。

### 12.3 验证中发现并修复的问题

**CLI→runtime 同步覆写锚点**：回合收尾的 `syncRuntimeSessionFromChatMode` 把显示口径 `ContextTokenCount`（provider total 或估算）写回 `aicli_context_token_count`，覆盖 loop 写入的精确锚点（实测 24,523 → 24,559；provider 不报用量时会以估算值覆盖真实锚点）。

- 修复：`internal/chat.HasRuntimeSessionContextAnchor` + 同步侧存在结构化锚点时跳过写回（legacy 会话保留历史行为）；
- 回归测试：`TestSyncRuntimeSessionFromChatPreservesLoopContextAnchor`（锚点不被覆写 + legacy 写回不变），隔离工作树验证通过（含既有 7 个 sync/restore 上下文用例）；
- 提交：`8489834c fix(context): 防止 CLI 同步覆写 loop 上下文锚点（实机验证发现）`。

### 12.4 备注

- 本轮实机验证覆盖"观测 → 锚定 → 投影 → 状态栏"全链路；压缩触发本身由单测/回放测试覆盖（实机触发需要 ~172K 真实上下文，成本不划算）。
- `cmd/aicli/commands` 全量回归仍受并发工作流的事件日志重构影响（多个测试文件在改、暂不可编译），待其收敛后复跑。
